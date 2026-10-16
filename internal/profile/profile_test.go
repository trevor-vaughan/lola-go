package profile_test

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("profile", func() {
	var p paths.Paths
	BeforeEach(func() {
		testenv.Isolate()
		var err error
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
	})
	write := func(name, content string) string {
		GinkgoHelper()
		path := filepath.Join(p.UserProfiles, name)
		Expect(os.MkdirAll(filepath.Dir(path), 0o700)).To(Succeed())
		Expect(os.WriteFile(path, []byte(content), 0o600)).To(Succeed())
		return path
	}

	Describe("Load", func() {
		It("reads entries, giving those without targets the profile's", func() {
			path := write("go.yml", `version: 1
description: Go tooling
targets: [claude-code]
modules:
  - name: gopls
    source: https://example.com/gopls.git
    ref: v1.2.0
  - name: style
    source: ../style
    targets: [gemini-cli]
    rules: false
`)
			pr, err := profile.Load(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(pr.Description).To(Equal("Go tooling"))
			off := false
			Expect(pr.Entries()).To(Equal([]manifest.Entry{
				{Name: "gopls", Source: "https://example.com/gopls.git", Ref: "v1.2.0", Targets: []string{"claude-code"}},
				{Name: "style", Source: "../style", Targets: []string{"gemini-cli"}, Rules: &off},
			}))
			Expect(pr.Modules[0].Targets).To(BeEmpty(), "Entries copies; the profile as read keeps its own")
		})

		It("reads an empty file as a profile with no modules, and a missing one as fs.ErrNotExist", func() {
			pr, err := profile.Load(write("empty.yml", ""))
			Expect(err).NotTo(HaveOccurred())
			Expect(pr.Entries()).To(BeEmpty())
			_, err = profile.Load(filepath.Join(p.UserProfiles, "gone.yml"))
			Expect(err).To(MatchError(fs.ErrNotExist))
		})

		DescribeTable("refuses a crafted file with its path and line",
			func(content, want string) {
				path := write("bad.yml", content)
				_, err := profile.Load(path)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(HavePrefix(path))
				Expect(err.Error()).To(ContainSubstring(want))
				Expect(err.Error()).NotTo(ContainSubstring("\x1b"), "what the file says is printed safely")
				Expect(errs.Hints(err)).To(ContainElement("fix it by hand; lola never writes it"))
				Expect(errs.Hints(err)).NotTo(ContainElement(ContainSubstring("move it aside")), "lola writes no profile to start anew")
			},
			Entry("a newer version, before its keys", "version: 2\nextends: [go]\n", "written by a newer lola (profile version 2); upgrade lola"),
			Entry("a version that is not a number, quoted safely", "version: \"1\\u001b[2J\"\n", "cannot unmarshal"),
			Entry("an unknown key", "version: 1\nextends: [go]\n", "line 2: field extends not found"),
			Entry("a control character in a source", "version: 1\nmodules:\n  - name: a\n    source: \"https://x/a\\u0007.git\"\n",
				":3: module a: source holds control or invisible characters"),
			Entry("an invisible character in the description", "version: 1\ndescription: \"a\\u200bb\"\n",
				":2: description holds control or invisible characters"),
			Entry("an oversized description", "version: 1\ndescription: "+strings.Repeat("x", 257)+"\n",
				":2: description is longer than 256 bytes"),
			Entry("a hostile ref", "version: 1\nmodules:\n  - name: a\n    source: https://x/a.git\n    ref: --upload-pack=touch PWNED\n",
				":3: module a: ref \"--upload-pack=touch PWNED\" starts with '-'"),
			Entry("a module listed twice", "version: 1\nmodules:\n  - {name: a, source: ../a}\n  - {name: a, source: ../b}\n",
				":4: module a is listed twice"),
			Entry("an invalid module name", "version: 1\nmodules:\n  - {name: ../a, source: ../a}\n", ":3: module name \"../a\" is invalid"),
			Entry("a file that does not parse", "version: 1\nmodules: [\n", ": does not parse: "),
			Entry("anchors", "version: 1\nmodules:\n  - &a {name: a, source: ../a}\n", "anchors, aliases and merge keys"),
		)

		It("refuses more than 512 entries", func() {
			var b strings.Builder
			b.WriteString("version: 1\nmodules:\n")
			for i := range 513 {
				fmt.Fprintf(&b, "  - {name: m%d, source: ../m}\n", i)
			}
			_, err := profile.Load(write("big.yml", b.String()))
			Expect(err).To(MatchError(ContainSubstring(":3: modules lists more than 512 entries")))
			Expect(errs.Hints(err)).To(ContainElement("fix it by hand; lola never writes it"))
		})

		It("reads a user profile through a symbolic link (stow), and refuses one that is not a regular file", func() {
			if runtime.GOOS == "windows" {
				Skip("symbolic links need privileges on Windows")
			}
			real := filepath.Join(p.Home, "dotfiles", "go.yml")
			Expect(os.MkdirAll(filepath.Dir(real), 0o700)).To(Succeed())
			Expect(os.WriteFile(real, []byte("version: 1\ndescription: from stow\n"), 0o600)).To(Succeed())
			Expect(os.MkdirAll(p.UserProfiles, 0o700)).To(Succeed())
			link := filepath.Join(p.UserProfiles, "go.yml")
			Expect(os.Symlink(real, link)).To(Succeed())
			pr, err := profile.Load(link)
			Expect(err).NotTo(HaveOccurred())
			Expect(pr.Description).To(Equal("from stow"))

			dir := filepath.Join(p.UserProfiles, "dir.yml")
			Expect(os.Mkdir(dir, 0o700)).To(Succeed())
			_, err = profile.Load(dir)
			Expect(err).To(MatchError(dir + " is not a regular file"))
			Expect(errs.Hints(err)).To(Equal([]string{"fix it by hand; lola never writes it"}))
		})
	})

	Describe("Visible", func() {
		It("lists the user's profiles by name, with default at user scope only", func() {
			write("go.yml", "")
			write("default.yml", "")
			write("base.yml", "")
			write("notes.txt", "")
			write("Bad Name.yml", "")
			infos, err := profile.Visible(p, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(infos).To(Equal([]profile.Info{
				{Name: "base", Kind: profile.KindUser, Path: filepath.Join(p.UserProfiles, "base.yml")},
				{Name: "default", Kind: profile.KindDefault, Path: filepath.Join(p.UserProfiles, "default.yml")},
				{Name: "go", Kind: profile.KindUser, Path: filepath.Join(p.UserProfiles, "go.yml")},
			}))
			Expect(infos[1].Qualified()).To(Equal("default"))
			Expect(infos[2].Qualified()).To(Equal("user:go"))

			infos, err = profile.Visible(p, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(infos).To(HaveLen(2))
			Expect(infos).NotTo(ContainElement(HaveField("Name", "default")))
		})

		It("reads no file: a profile that does not load is still listed", func() {
			write("broken.yml", "version: 9\n")
			infos, err := profile.Visible(p, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(infos).To(ConsistOf(HaveField("Name", "broken")))
		})

		It("refuses a profiles path that is not a directory, naming it and coaching", func() {
			Expect(os.MkdirAll(filepath.Dir(p.UserProfiles), 0o700)).To(Succeed())
			Expect(os.WriteFile(p.UserProfiles, nil, 0o600)).To(Succeed())
			_, err := profile.Visible(p, true)
			Expect(err).To(MatchError(ContainSubstring(p.UserProfiles)))
			Expect(errs.Hints(err)).To(ContainElement("make " + p.UserProfiles + " a directory lola can read, or remove it"))
		})

		It("lists none without a profiles directory and refuses more than 256 files", func() {
			Expect(profile.Visible(p, true)).To(BeEmpty())
			for i := range 257 {
				write(fmt.Sprintf("p%d.yml", i), "")
			}
			_, err := profile.Visible(p, true)
			Expect(err).To(MatchError(p.UserProfiles + " holds more than 256 files"))
		})
	})
})
