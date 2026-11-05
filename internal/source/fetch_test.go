package source_test

import (
	"context"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("Fetch", func() {
	var tmp, src, staging string
	ctx := context.Background()
	BeforeEach(func() {
		tmp = testenv.Isolate()
		src = filepath.Join(tmp, "example")
		staging = filepath.Join(tmp, "staging")
		writeTree(src, map[string]string{"commands/a.md": "A"})
	})

	It("refuses a hostile Spec that never went through Parse", func() {
		// Coached by a hint, or, when no command fixes it, in the error.
		refused := func(spec source.Spec, msg string, hints []string) {
			GinkgoHelper()
			_, err := source.Fetch(ctx, spec, staging)
			Expect(err).To(MatchError(msg), spec.Location+" "+spec.Ref)
			Expect(errs.Hints(err)).To(Equal(hints), spec.Location+" "+spec.Ref)
		}
		refused(source.Spec{Kind: source.KindFolder, Location: src, Ref: "--upload-pack=touch x"},
			`ref "--upload-pack=touch x" starts with '-'`, []string{"a branch, tag or commit never starts with '-'"})
		refused(source.Spec{Kind: source.KindFolder, Location: "fd::0"},
			`source "fd::0" names a git remote helper (<transport>::<address>), which lola never runs; sources are `+source.AcceptedForms, nil)
		refused(source.Spec{Kind: source.KindFolder, Location: "-x"},
			`source "-x" starts with '-', which would be read as an option`, []string{"for a path, write ./-x"})
		Expect(staging).NotTo(BeADirectory())
	})

	It("refuses a hand-built location of the wrong form without echoing its password", func() {
		// Coached by a hint, or, when no command fixes it, in the error.
		refused := func(kind source.Kind, loc, msg string, hints []string) {
			GinkgoHelper()
			_, err := source.Fetch(ctx, source.Spec{Kind: kind, Location: loc}, staging)
			Expect(err).To(MatchError(msg), loc)
			Expect(errs.Hints(err)).To(Equal(hints), loc)
		}
		refused(source.KindGit, "ftp://u:s3cr3t@h/r",
			"git source ftp://h/r is not an https://, ssh:// or user@host:path URL; sources are "+source.AcceptedForms, nil)
		folder := []string{"give the absolute path of the folder"}
		refused(source.KindFolder, "https://u:s3cr3t@h/r", "folder source https://… is not an absolute path", folder)
		refused(source.KindFolder, "u:s3cr3t@h://x", "folder source (not shown: it looks like a URL) is not an absolute path", folder)
		archive := []string{"give the archive's https:// URL or its absolute path"}
		refused(source.KindArchive, "//u:s3cr3t@h/r", "archive location starts with //, a URL without a scheme", archive)
		refused(source.KindArchive, "//u:s3cr3t@h/r.zip", "archive location starts with //, a URL without a scheme", archive)
	})

	It("names what is wrong with a URL-shaped location it does not echo", func() {
		_, err := source.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: "u:s3cr3t@h://x"}, staging)
		Expect(err).To(MatchError("folder source (not shown: it looks like a URL) is not an absolute path"))
		_, err = source.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: "ftp://h/x"}, staging)
		Expect(err).To(MatchError("folder source ftp://… is not an absolute path"))
		_, err = source.Fetch(ctx, source.Spec{Kind: source.KindArchive, Location: "//u:s3cr3t@h/r"}, staging)
		Expect(err).To(MatchError("archive location starts with //, a URL without a scheme"))
	})

	It("shows a scheme-less location as given, not as ://location", func() {
		for kind, loc := range map[source.Kind]string{source.KindGit: "rel/repo", source.KindArchive: "rel/m.zip"} {
			_, err := source.Fetch(ctx, source.Spec{Kind: kind, Location: loc}, staging)
			Expect(err).To(MatchError(ContainSubstring(loc)), string(kind))
			Expect(err.Error()).NotTo(ContainSubstring("://"+loc), string(kind))
		}
	})

	It("names a relative folder path that holds '@', '?' or '#' in full", func() {
		loc := "rel/a@b?c#d"
		_, err := source.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: loc}, staging)
		Expect(err).To(MatchError(ContainSubstring(loc)))
	})

	It("refuses --ref on a folder, with a hint", func() {
		_, err := source.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: src, Ref: "v1"}, staging)
		Expect(err).To(MatchError(ContainSubstring("--ref applies to git sources only")))
		Expect(errs.Hints(err)).NotTo(BeEmpty())
	})

	It("says a folder that is a git work tree or bare repository is still a folder source", func() {
		work := filepath.Join(tmp, "work")
		writeTree(work, map[string]string{".git/HEAD": "ref: refs/heads/main\n", "commands/a.md": "A"})
		bare := filepath.Join(tmp, "bare")
		writeTree(bare, map[string]string{"HEAD": "ref: refs/heads/main\n", "objects/.keep": "", "refs/.keep": ""})
		for _, loc := range []string{work, bare} {
			_, err := source.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: loc, Ref: "v1"}, staging)
			Expect(err).To(MatchError(ContainSubstring("--ref applies to git sources only")), loc)
			Expect(errs.Hints(err)).To(HaveLen(1), loc)
			Expect(errs.Hints(err)[0]).To(And(
				ContainSubstring("a local path is always read as a folder, even a git repository"),
				ContainSubstring("lola reads no branch, tag or commit of it"),
				ContainSubstring("drop --ref"),
				Not(ContainSubstring("has no branches"))), loc)
		}
	})

	It("names a bare git repository and says how to clone it", func() {
		bare := filepath.Join(tmp, "mods.git")
		writeTree(bare, map[string]string{"HEAD": "ref: refs/heads/main\n", "objects/.keep": "", "refs/.keep": "", "commands/a.md": "A"})
		_, err := source.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: bare}, staging)
		Expect(err).To(MatchError(ContainSubstring("mods.git is a bare git repository; lola reads a local path as a folder")))
		Expect(errs.Hints(err)).To(ConsistOf("git clone " + bare + " mods"))

		work := filepath.Join(tmp, "work2")
		writeTree(work, map[string]string{".git/HEAD": "ref: refs/heads/main\n", ".git/objects/.keep": "", ".git/refs/.keep": "", "commands/a.md": "A"})
		_, err = source.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: work}, staging)
		Expect(err).NotTo(HaveOccurred(), "a work tree is a folder")
	})

	It("refuses an unknown kind", func() {
		_, err := source.Fetch(ctx, source.Spec{Kind: "svn", Location: src}, staging)
		Expect(err).To(MatchError(ContainSubstring(`unknown type "svn"`)))
	})
})
