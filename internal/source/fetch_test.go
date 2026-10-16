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
		for _, spec := range []source.Spec{
			{Kind: source.KindFolder, Location: src, Ref: "--upload-pack=touch x"},
			{Kind: source.KindFolder, Location: "fd::0"},
			{Kind: source.KindFolder, Location: "-x"},
		} {
			_, err := source.Fetch(ctx, spec, staging)
			Expect(err).To(HaveOccurred(), spec.Location+" "+spec.Ref)
			Expect(errs.Hints(err)).NotTo(BeEmpty())
		}
		Expect(staging).NotTo(BeADirectory())
	})

	It("refuses a hand-built location of the wrong form without echoing its password", func() {
		refused := func(kind source.Kind, loc string) {
			_, err := source.Fetch(ctx, source.Spec{Kind: kind, Location: loc}, staging)
			Expect(err).To(HaveOccurred(), loc)
			Expect(err.Error()).NotTo(ContainSubstring("s3cr3t"), loc)
			Expect(errs.Hints(err)).NotTo(BeEmpty(), loc)
		}
		refused(source.KindGit, "ftp://u:s3cr3t@h/r")
		refused(source.KindFolder, "https://u:s3cr3t@h/r")
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

	It("refuses an unknown kind", func() {
		_, err := source.Fetch(ctx, source.Spec{Kind: "svn", Location: src}, staging)
		Expect(err).To(MatchError(ContainSubstring(`unknown type "svn"`)))
	})
})
