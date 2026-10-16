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
			Entry("an unknown key", "version: 1\nshell: rm -rf\n", "line 2: field shell not found"),
			Entry("more than 16 names in extends", "version: 1\nextends: [a1, a2, a3, a4, a5, a6, a7, a8, a9, b1, b2, b3, b4, b5, b6, b7, b8]\n",
				":2: extends lists more than 16 profiles"),
			Entry("a name twice in extends", "version: 1\nextends:\n  - go\n  - go\n", ":4: extends lists go twice"),
			Entry("an invalid name in extends, read as a missing one", "version: 1\nextends:\n  - go\n  - \"../x\\u001b\"\n",
				":4: extends: ../x? is not a remote profile you have"),
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
			infos, invalid, err := profile.Visible(p, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(invalid).To(Equal(1), "Bad Name.yml is counted, notes.txt is not")
			Expect(infos).To(Equal([]profile.Info{
				{Name: "base", Kind: profile.KindUser, Path: filepath.Join(p.UserProfiles, "base.yml")},
				{Name: "default", Kind: profile.KindDefault, Path: filepath.Join(p.UserProfiles, "default.yml")},
				{Name: "go", Kind: profile.KindUser, Path: filepath.Join(p.UserProfiles, "go.yml")},
			}))
			Expect(infos[1].Qualified()).To(Equal("default"))
			Expect(infos[2].Qualified()).To(Equal("user:go"))

			infos, invalid, err = profile.Visible(p, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(invalid).To(Equal(1))
			Expect(infos).To(HaveLen(2))
			Expect(infos).NotTo(ContainElement(HaveField("Name", "default")))
		})

		It("reads no file: a profile that does not load is still listed", func() {
			write("broken.yml", "version: 9\n")
			infos, _, err := profile.Visible(p, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(infos).To(ConsistOf(HaveField("Name", "broken")))
		})

		It("refuses a profiles path that is not a directory, naming it and coaching", func() {
			Expect(os.MkdirAll(filepath.Dir(p.UserProfiles), 0o700)).To(Succeed())
			Expect(os.WriteFile(p.UserProfiles, nil, 0o600)).To(Succeed())
			_, _, err := profile.Visible(p, true)
			Expect(err).To(MatchError(ContainSubstring(p.UserProfiles)))
			Expect(errs.Hints(err)).To(ContainElement("make " + p.UserProfiles + " a directory lola can read, or remove it"))
		})

		It("lists none without a profiles directory and refuses more than 256 files", func() {
			Expect(profile.Visible(p, true)).To(BeEmpty())
			for i := range 257 {
				write(fmt.Sprintf("p%d.yml", i), "")
			}
			_, _, err := profile.Visible(p, true)
			Expect(err).To(MatchError(p.UserProfiles + " holds more than 256 files"))
		})
	})

	Describe("project profiles", func() {
		var root string
		BeforeEach(func() {
			root = filepath.Join(p.Home, "proj")
			Expect(os.MkdirAll(filepath.Join(root, ".lola", "profiles"), 0o755)).To(Succeed())
		})
		writeProject := func(name, content string) string {
			GinkgoHelper()
			path := filepath.Join(root, ".lola", "profiles", name)
			Expect(os.WriteFile(path, []byte(content), 0o644)).To(Succeed())
			return path
		}
		symlinks := func() {
			if runtime.GOOS == "windows" {
				Skip("symbolic links need privileges on Windows")
			}
		}

		It("lists a project's profiles by name, and ignores the default.yml a project cannot define", func() {
			writeProject("go.yml", "")
			writeProject("docs.yml", "")
			def := writeProject("default.yml", "version: 1\n")
			writeProject("Bad Name.yml", "")
			writeProject("README.md", "")
			infos, invalid, ignored, err := profile.Project(root)
			Expect(err).NotTo(HaveOccurred())
			Expect(invalid).To(Equal(1))
			Expect(ignored).To(Equal(def))
			Expect(infos).To(Equal([]profile.Info{
				{Name: "docs", Kind: profile.KindProject, Path: filepath.Join(root, ".lola", "profiles", "docs.yml")},
				{Name: "go", Kind: profile.KindProject, Path: filepath.Join(root, ".lola", "profiles", "go.yml")},
			}))
			Expect(infos[1].Qualified()).To(Equal("project:go"))
		})

		It("lists none without .lola/ or .lola/profiles/, and refuses more than 256 files", func() {
			Expect(os.RemoveAll(filepath.Join(root, ".lola"))).To(Succeed())
			infos, _, _, err := profile.Project(root)
			Expect(err).NotTo(HaveOccurred())
			Expect(infos).To(BeEmpty())
			Expect(os.MkdirAll(filepath.Join(root, ".lola"), 0o755)).To(Succeed())
			infos, _, _, err = profile.Project(root)
			Expect(err).NotTo(HaveOccurred())
			Expect(infos).To(BeEmpty())
			Expect(os.MkdirAll(filepath.Join(root, ".lola", "profiles"), 0o755)).To(Succeed())
			for i := range 257 {
				writeProject(fmt.Sprintf("p%d.yml", i), "")
			}
			_, _, _, err = profile.Project(root)
			Expect(err).To(MatchError(filepath.Join(root, ".lola", "profiles") + " holds more than 256 files"))
		})

		It("a crafted profile file refuses at load", func() {
			for content, want := range map[string]string{
				"version: 2\nextends: [go]\n": "written by a newer lola (profile version 2); upgrade lola",
				"version: 1\nshell: rm -rf\n": "field shell not found",
				"version: 1\nmodules:\n  - name: a\n    source: \"https://x/a\\u0007.git\"\n": ":3: module a: source holds control or invisible characters",
				"version: 1\ndescription: " + strings.Repeat("x", 257) + "\n":                 ":2: description is longer than 256 bytes",
			} {
				path := writeProject("bad.yml", content)
				_, err := profile.LoadProject(path)
				Expect(err).To(MatchError(And(HavePrefix(path), ContainSubstring(want))), content)
				Expect(err.Error()).NotTo(ContainSubstring("\x07"))
				Expect(errs.Hints(err)).To(ContainElement("fix it by hand, or restore it from version control"))
			}

			symlinks()
			real := filepath.Join(p.Home, "elsewhere.yml")
			Expect(os.WriteFile(real, []byte("version: 1\n"), 0o600)).To(Succeed())
			link := filepath.Join(root, ".lola", "profiles", "go.yml")
			Expect(os.Symlink(real, link)).To(Succeed())
			_, err := profile.LoadProject(link)
			Expect(err).To(MatchError(link + " is a symbolic link; a project's profile must be a file of its own"))
			Expect(profile.Load(link)).NotTo(BeNil(), "a user profile may be a link (P7); a project's may not")
		})

		It("writes pins into the bytes it read, keeping comments and other keys, and never over a file changed since", func() {
			commit, digest := strings.Repeat("a", 40), "sha256:"+strings.Repeat("b", 64)
			path := writeProject("go.yml", `version: 1
description: Go tooling # shared by the team
targets: [claude-code]
modules:
  # the language server
  - name: gopls
    source: https://example.com/gopls.git
    ref: v1.2.0
  - name: style
    source: ../../mods/style
`)
			pr, err := profile.LoadProject(path)
			Expect(err).NotTo(HaveOccurred())
			out, err := pr.Pin(path, []manifest.Entry{{Name: "gopls", Commit: commit, Digest: digest}})
			Expect(err).NotTo(HaveOccurred())
			Expect(string(out)).To(Equal(`version: 1
description: Go tooling # shared by the team
targets: [claude-code]
modules:
  # the language server
  - name: gopls
    source: https://example.com/gopls.git
    ref: v1.2.0
    commit: ` + commit + `
    digest: ` + digest + `
  - name: style
    source: ../../mods/style
`))
			Expect(os.ReadFile(path)).NotTo(Equal(out), "Pin returns the bytes; the caller writes them")

			Expect(os.WriteFile(path, []byte("version: 1\nmodules:\n  - {name: gopls, source: https://evil.example/gopls.git}\n"), 0o644)).To(Succeed())
			_, err = pr.Pin(path, []manifest.Entry{{Name: "gopls", Commit: commit, Digest: digest}})
			Expect(err).To(MatchError(path + " changed while lola was running; not writing its pins"))
		})

		It("checks just before a write that nothing moved since the profile was read", func() {
			path := writeProject("go.yml", "version: 1\nmodules:\n  - {name: a, source: https://example.com/a.git}\n")
			pr, err := profile.LoadProject(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(pr.Unchanged(root, path)).To(Succeed())

			Expect(os.WriteFile(path, []byte("version: 1\nmodules:\n  - {name: a, source: https://evil.example/a.git}\n"), 0o644)).To(Succeed())
			Expect(pr.Unchanged(root, path)).To(MatchError(ContainSubstring(path + " changed while lola was running")))

			symlinks()
			Expect(os.WriteFile(path, []byte("version: 1\nmodules:\n  - {name: a, source: https://example.com/a.git}\n"), 0o644)).To(Succeed())
			Expect(pr.Unchanged(root, path)).To(Succeed())
			profiles, elsewhere := filepath.Join(root, ".lola", "profiles"), filepath.Join(p.Home, "elsewhere")
			Expect(os.Rename(profiles, elsewhere)).To(Succeed())
			Expect(os.Symlink(elsewhere, profiles)).To(Succeed())
			Expect(pr.Unchanged(root, path)).To(MatchError(ContainSubstring("is not a directory of the project's own")),
				"the same bytes, reached through a link swapped in for .lola/profiles")
		})

		It("refuses a .lola/ or .lola/profiles/ that is a symbolic link or not a directory, before listing it", func() {
			symlinks()
			elsewhere := filepath.Join(p.Home, "elsewhere")
			Expect(os.MkdirAll(filepath.Join(elsewhere, "profiles"), 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(elsewhere, "profiles", "go.yml"), nil, 0o600)).To(Succeed())

			profiles := filepath.Join(root, ".lola", "profiles")
			Expect(os.RemoveAll(profiles)).To(Succeed())
			Expect(os.Symlink(filepath.Join(elsewhere, "profiles"), profiles)).To(Succeed())
			_, _, _, err := profile.Project(root)
			Expect(err).To(MatchError(ContainSubstring(profiles + " is not a directory of the project's own")))

			Expect(os.Remove(profiles)).To(Succeed())
			Expect(os.WriteFile(profiles, nil, 0o600)).To(Succeed())
			_, _, _, err = profile.Project(root)
			Expect(err).To(MatchError(ContainSubstring(profiles + " is not a directory of the project's own")))

			Expect(os.RemoveAll(filepath.Join(root, ".lola"))).To(Succeed())
			Expect(os.Symlink(elsewhere, filepath.Join(root, ".lola"))).To(Succeed())
			_, _, _, err = profile.Project(root)
			Expect(err).To(MatchError(ContainSubstring(filepath.Join(root, ".lola") + " is not a directory of the project's own")))
			Expect(errs.Hints(err)).To(Equal([]string{"make it a directory, or remove it"}))
		})
	})
})
