package profile_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/source"
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

		It("entry vars refuse bad shapes at decode in project.yml and profiles, naming the line", func() {
			entries := "version: 1\nmodules:\n  - name: kit\n    source: ../kit\n    vars:\n"
			pr, err := profile.Load(write("go.yml", entries+"      language: go\n      tools: [rg, jq]\n"))
			Expect(err).NotTo(HaveOccurred())
			Expect(pr.Entries()[0].Vars).To(Equal(map[string]any{"language": "go", "tools": []string{"rg", "jq"}}))
			for vars, want := range map[string]string{
				"      Lang: go\n":          ":6: modules[0].vars.Lang: a var name is a-z, 0-9 and _, starts with a letter, at most 64 characters",
				"      since: 2024-01-02\n": `:6: modules[0].vars.since: dates are not supported; quote it to make it a string: "2024-01-02"`,
				"      x: [[a]]\n":          ":6: modules[0].vars.x[0]: a var is a string, true or false, a number, or a list of strings",
			} {
				path := write("bad.yml", entries+vars)
				_, err := profile.Load(path)
				Expect(err).To(MatchError(path+want), vars)
			}
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
				Expect(errs.Hints(err)).To(ContainElement("fix it by hand"))
				Expect(errs.Hints(err)).NotTo(ContainElement(ContainSubstring("move it aside")), "lola writes no profile to start anew")
			},
			Entry("a newer version, before its keys", "version: 2\nextends: [go]\n", "written by a newer lola (profile version 2); upgrade lola"),
			Entry("a version that is not a number, quoted safely", "version: \"1\\u001b[2J\"\n", "cannot unmarshal"),
			Entry("an unknown key", "version: 1\nshell: rm -rf\n", "line 2: field shell not found"),
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

		It("a remote profile's entry with market, components or picks refuses at load", func() {
			src := filepath.Join(testenv.Isolate(), "src", "profiles")
			Expect(os.MkdirAll(src, 0o700)).To(Succeed())
			pin := "    commit: " + strings.Repeat("a", 40) + "\n    digest: sha256:" + strings.Repeat("b", 64) + "\n"
			for key, value := range map[string]string{"market": "{name: acme, source: https://example.com/plugins.git}",
				"components": "{skills: [skills/a]}", "only": "[skill/a]", "skip": "[skill/b]"} {
				body := "version: 1\nmodules:\n  - name: formatter\n    source: https://example.com/monorepo.git\n" + pin + "    " + key + ": " + value + "\n"
				Expect(os.WriteFile(filepath.Join(src, "go.yml"), []byte(body), 0o600)).To(Succeed())
				_, _, err := profile.Fetched(src, "go")
				Expect(err).To(MatchError(ContainSubstring("go.yml:3: module formatter: a remote profile's entry takes no "+key+":")), key)
				Expect(errs.Hints(err)).To(ContainElement("set it on the entry of a profile of yours"), key)
			}
		})

		It("a profile entry whose source is a marketplace address is reported as unresolved, in every kind of profile", func() {
			const body = "version: 1\nmodules:\n  - name: formatter\n    source: '@acme/formatter'\n    ref: v1\n"
			unresolved := func(err error, path string) {
				GinkgoHelper()
				ue, ok := errors.AsType[*manifest.UnresolvedError](err)
				Expect(ok).To(BeTrue(), "%v", err)
				Expect(ue.Path).To(Equal(path))
				Expect(ue.Entries).To(Equal([]manifest.Unresolved{{Name: "formatter", Market: "acme", Module: "formatter", Line: 4}}))
				Expect(ue.Manifest).To(BeNil(), "a profile is never edited to replace it")
				Expect(errs.Hints(err)).To(BeEmpty(), "the engine coaches it from the reader's marketplaces")
			}
			user := write("go.yml", body)
			_, err := profile.Load(user)
			unresolved(err, user)
			Expect(err).To(MatchError(user + ":4: formatter's source is @acme/formatter, a marketplace address; a file must record the module's " +
				"real source; edit the entry to name the module's source, ref and path"))
			_, err = profile.Load(write("bad.yml", body+"  - {name: formatter, source: ../f}\n"))
			Expect(err).To(MatchError(ContainSubstring("module formatter is listed twice")), "every other rule first")

			root := filepath.Join(testenv.Isolate(), "proj")
			path := filepath.Join(root, ".lola", "profiles", "team.yml")
			Expect(os.MkdirAll(filepath.Dir(path), 0o700)).To(Succeed())
			Expect(os.WriteFile(path, []byte(body), 0o600)).To(Succeed())
			_, err = profile.LoadProject(path)
			unresolved(err, path)

			src := filepath.Join(root, "src", "profiles")
			Expect(os.MkdirAll(src, 0o700)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(src, "go.yml"), []byte(body), 0o600)).To(Succeed())
			_, _, err = profile.Fetched(src, "go")
			unresolved(err, "profiles/go.yml")
		})

		It("refuses more than 512 entries", func() {
			var b strings.Builder
			b.WriteString("version: 1\nmodules:\n")
			for i := range 513 {
				fmt.Fprintf(&b, "  - {name: m%d, source: ../m}\n", i)
			}
			_, err := profile.Load(write("big.yml", b.String()))
			Expect(err).To(MatchError(ContainSubstring(":2: modules lists more than 512 entries")))
			Expect(errs.Hints(err)).To(ContainElement("fix it by hand"))
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
			Expect(errs.Hints(err)).To(Equal([]string{"fix it by hand"}))
		})
	})

	Describe("Parse", func() {
		It("reads the bytes of a write in progress as Load reads the file, and refuses what Load refuses, naming the path", func() {
			path := filepath.Join(p.UserProfiles, "go.yml")
			got, err := profile.Parse(path, []byte("version: 1\nmodules:\n  - {name: kit, source: https://example.com/kit.git, ref: main}\n"))
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Modules).To(Equal([]manifest.Entry{{Name: "kit", Source: "https://example.com/kit.git", Ref: "main"}}))

			_, err = profile.Parse(path, []byte("version: 1\nmodules: [\n"))
			Expect(err).To(MatchError(HavePrefix(path + ": does not parse")))
			Expect(errs.Hints(err)).To(Equal([]string{"fix it by hand"}))
			_, err = profile.Parse(path, []byte("version: 1\nmodules:\n  - {name: ../kit, source: https://example.com/kit.git}\n"))
			Expect(err).To(MatchError(HavePrefix(path + ":3:")))
		})
	})

	Describe("entries", func() {
		It("reads market, components and picks in a profile of yours and in a project's", func() {
			body := `version: 1
targets: [claude-code]
modules:
  - name: kit
    source: https://example.com/kit.git
    components: {skills: [skills/a]}
    only: [skill/golang-*]
    skip: [command/slow]
    market: {name: acme, source: https://example.com/catalog.git}
`
			want := manifest.Entry{Name: "kit", Source: "https://example.com/kit.git", Components: &source.Components{Skills: []string{"skills/a"}},
				Only: []string{"skill/golang-*"}, Skip: []string{"command/slow"}, Market: &manifest.MarketRef{Name: "acme", Source: "https://example.com/catalog.git"}}
			pr, err := profile.Load(write("go.yml", body))
			Expect(err).NotTo(HaveOccurred())
			Expect(pr.Modules).To(Equal([]manifest.Entry{want}))

			root := filepath.Join(testenv.Isolate(), "proj")
			path := filepath.Join(root, ".lola", "profiles", "team.yml")
			Expect(os.MkdirAll(filepath.Dir(path), 0o700)).To(Succeed())
			Expect(os.WriteFile(path, []byte(body), 0o600)).To(Succeed())
			pr, err = profile.LoadProject(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(pr.Modules).To(Equal([]manifest.Entry{want}))
		})

		DescribeTable("refuses picks that are not kind/name, naming the line and the fix",
			func(only, want string, hints []string) {
				path := write("go.yml", "version: 1\nmodules:\n  - name: kit\n    source: https://example.com/kit.git\n    only: "+only+"\n")
				_, err := profile.Load(path)
				Expect(err).To(MatchError(path + ":5: " + want))
				Expect(errs.Hints(err)).To(Equal(append([]string{"fix it by hand"}, hints...)))
			},
			Entry("a module-qualified selector", "[kit:skill/x]", `only: "kit:skill/x" names a module; an entry's selectors are kind/name`,
				[]string{"write it as skill/x"}),
			Entry("a module with nothing after it", "['kit:']", `only: "kit:" names a module; an entry's selectors are kind/name`,
				[]string{config.PatternForms}),
			Entry("a module before an invalid selector", "[kit:widget/x]", `only: "kit:widget/x" names a module; an entry's selectors are kind/name`,
				[]string{config.PatternForms}),
			Entry("an unknown kind", "[widget/x]", `only: selector "widget/x" has an unknown kind "widget"`,
				[]string{"kinds: skill, command, agent, mcp, rule"}),
			Entry("no kind", "[review]", `only: selector "review" has no kind/name`,
				[]string{config.PatternForms}),
			Entry("a comma", "['skill/a,skill/b']", `only: selector "skill/a,skill/b" holds a comma; a selector names one component, and the flags take comma-separated selectors`,
				[]string{"list several selectors as separate items in a file, or comma-separated on the command line (--only skill/a,skill/b)", config.PatternForms}),
			Entry("a control character", `["skill/a\u0007"]`, `only: selector "skill/a?" holds a control or format character`,
				[]string{"retype it with printable characters only", config.PatternForms}),
		)
	})

	Describe("Edit", func() {
		const before = `version: 1
description: Go tooling # mine
targets: [claude-code]
modules:
  # the language server
  - name: gopls
    source: https://example.com/gopls.git
`
		It("edits the bytes it read, keeping comments and order, and never writes a pin of its own", func() {
			path := write("go.yml", before)
			pr, err := profile.Load(path)
			Expect(err).NotTo(HaveOccurred())
			dst, out, err := pr.Edit(path, manifest.Change{Set: []manifest.Entry{
				{Name: "gopls", Source: "https://example.com/gopls.git", Skip: []string{"skill/slow"}},
				{Name: "kit", Source: "https://example.com/kit.git", Only: []string{"skill/a", "skill/b"}}}})
			Expect(err).NotTo(HaveOccurred())
			Expect(dst).To(Equal(path))
			Expect(string(out)).To(Equal(before + `    skip: [skill/slow]
  - name: kit
    source: https://example.com/kit.git
    only: [skill/a, skill/b]
`))
			Expect(os.ReadFile(path)).To(Equal([]byte(before)), "Edit returns the bytes; the caller writes them")
			Expect(pr.Current(path)).To(Succeed())
		})

		It("writes through a symbolic link of yours and keeps the link", func() {
			if runtime.GOOS == "windows" {
				Skip("symbolic links need privileges on Windows")
			}
			real := filepath.Join(p.Home, "dotfiles", "go.yml")
			Expect(os.MkdirAll(filepath.Dir(real), 0o700)).To(Succeed())
			Expect(os.WriteFile(real, []byte(before), 0o600)).To(Succeed())
			Expect(os.MkdirAll(p.UserProfiles, 0o700)).To(Succeed())
			link := filepath.Join(p.UserProfiles, "go.yml")
			Expect(os.Symlink(real, link)).To(Succeed())
			pr, err := profile.Load(link)
			Expect(err).NotTo(HaveOccurred())
			dst, _, err := pr.Edit(link, manifest.Change{Remove: []string{"gopls"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(dst).To(Equal(real), "the caller writes the file the link leads to")
		})

		It("refuses a profile that changed after the run read it, keeping the hand edit", func() {
			path := write("go.yml", before)
			pr, err := profile.Load(path)
			Expect(err).NotTo(HaveOccurred())
			edited := before + "  - {name: mine, source: https://example.com/mine.git}\n"
			Expect(os.WriteFile(path, []byte(edited), 0o600)).To(Succeed())
			_, _, err = pr.Edit(path, manifest.Change{Remove: []string{"gopls"}})
			Expect(err).To(MatchError(path + " changed while lola was reading it; nothing was written"))
			Expect(errs.Hints(err)).To(Equal([]string{"run the same command again"}))
			Expect(pr.Current(path)).To(MatchError(path + " changed while lola was reading it; nothing was written"))
			Expect(os.ReadFile(path)).To(Equal([]byte(edited)))
		})

		It("creates a profile of yours that did not exist, and refuses one that appeared since", func() {
			path := filepath.Join(p.UserProfiles, "default.yml")
			var none *profile.Profile
			dst, out, err := none.Edit(path, manifest.Change{Targets: []string{"claude-code"},
				Set: []manifest.Entry{{Name: "kit", Source: "https://example.com/kit.git"}}})
			Expect(err).NotTo(HaveOccurred())
			Expect(dst).To(Equal(path))
			Expect(string(out)).To(Equal("version: 1\ntargets: [claude-code]\nmodules:\n  - name: kit\n    source: https://example.com/kit.git\n"))
			Expect(none.Current(path)).To(Succeed())
			write("default.yml", "version: 1\n")
			Expect(none.Current(path)).To(MatchError(path + " changed while lola was reading it; nothing was written"))
		})

		It("refuses a result that would not load again, writing nothing", func() {
			path := write("go.yml", before)
			pr, err := profile.Load(path)
			Expect(err).NotTo(HaveOccurred())
			_, _, err = pr.Edit(path, manifest.Change{Set: []manifest.Entry{{Name: "kit", Source: "https://example.com/kit.git", Only: []string{"kit:skill/a"}}}})
			Expect(err).To(MatchError(And(HavePrefix("not writing "+path+", which lola could not load again: "+path+":"),
				ContainSubstring(`only: "kit:skill/a" names a module`))))
			Expect(errs.Hints(err)).To(ContainElement("nothing was written"))
		})

		It("keeps the marketplace addresses the file already holds, and refuses one the edit would add", func() {
			const addressed = before + "  - name: formatter # from acme\n    source: '@acme/formatter'\n"
			path := write("go.yml", addressed)
			pr, err := profile.Load(path)
			_, unresolved := errors.AsType[*manifest.UnresolvedError](err)
			Expect(unresolved).To(BeTrue(), "%v", err)
			Expect(pr).NotTo(BeNil(), "the profile as read comes with the refusal")

			_, out, err := pr.Edit(path, manifest.Change{Set: []manifest.Entry{{Name: "kit", Source: "https://example.com/kit.git"}}})
			Expect(err).NotTo(HaveOccurred())
			Expect(string(out)).To(Equal(addressed + "  - name: kit\n    source: https://example.com/kit.git\n"))

			_, out, err = pr.Edit(path, manifest.Change{Set: []manifest.Entry{{Name: "formatter", Source: "https://example.com/monorepo.git",
				Path: "tools/formatter", Market: &manifest.MarketRef{Name: "acme", Source: "https://example.com/catalog.git"}}}})
			Expect(err).NotTo(HaveOccurred())
			Expect(string(out)).NotTo(ContainSubstring("@acme"), "an edit may replace the address")

			_, _, err = pr.Edit(path, manifest.Change{Set: []manifest.Entry{{Name: "lint", Source: "@acme/lint"}}})
			Expect(err).To(MatchError(And(HavePrefix("not writing "+path+", which lola could not load again: "),
				ContainSubstring("lint's source is @acme/lint"))))
			Expect(errs.Hints(err)).To(ContainElement("nothing was written"))
		})

		It("reads a project's profile again without following a link, and writes it where it is", func() {
			root := filepath.Join(testenv.Isolate(), "proj")
			path := filepath.Join(root, ".lola", "profiles", "team.yml")
			Expect(os.MkdirAll(filepath.Dir(path), 0o700)).To(Succeed())
			Expect(os.WriteFile(path, []byte(before), 0o600)).To(Succeed())
			pr, err := profile.LoadProject(path)
			Expect(err).NotTo(HaveOccurred())
			dst, _, err := pr.Edit(path, manifest.Change{Remove: []string{"gopls"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(dst).To(Equal(path))
			if runtime.GOOS == "windows" {
				return
			}
			moved := filepath.Join(root, "elsewhere.yml")
			Expect(os.Rename(path, moved)).To(Succeed())
			Expect(os.Symlink(moved, path)).To(Succeed())
			_, _, err = pr.Edit(path, manifest.Change{Remove: []string{"gopls"}})
			Expect(err).To(MatchError(ContainSubstring(path)), "a link swapped in for a project's profile is refused")
		})
	})

	Describe("Visible", func() {
		It("lists the user's profiles by name, with default at user scope only", func() {
			write("go.yml", "")
			write("default.yml", "")
			write("base.yml", "")
			write("notes.txt", "")
			write("Bad Name.yml", "")
			infos, invalid, reserved, err := profile.Visible(p, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(invalid).To(Equal(1), "Bad Name.yml is counted, notes.txt is not")
			Expect(reserved).To(Equal(filepath.Join(p.UserProfiles, "base.yml")), "base.yml is no profile (spec 2026-10-29 §3.1)")
			Expect(infos).To(Equal([]profile.Info{
				{Name: "default", Kind: profile.KindDefault, Path: filepath.Join(p.UserProfiles, "default.yml")},
				{Name: "go", Kind: profile.KindUser, Path: filepath.Join(p.UserProfiles, "go.yml")},
			}))
			Expect(infos[0].Qualified()).To(Equal("default"))
			Expect(infos[1].Qualified()).To(Equal("user:go"))

			infos, invalid, _, err = profile.Visible(p, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(invalid).To(Equal(1))
			Expect(infos).To(HaveLen(1))
			Expect(infos).NotTo(ContainElement(HaveField("Name", "default")))
		})

		It("reads no file: a profile that does not load is still listed", func() {
			write("broken.yml", "version: 9\n")
			infos, _, _, err := profile.Visible(p, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(infos).To(ConsistOf(HaveField("Name", "broken")))
		})

		It("refuses a profiles path that is not a directory, naming it and coaching", func() {
			Expect(os.MkdirAll(filepath.Dir(p.UserProfiles), 0o700)).To(Succeed())
			Expect(os.WriteFile(p.UserProfiles, nil, 0o600)).To(Succeed())
			_, _, _, err := profile.Visible(p, true)
			Expect(err).To(MatchError(ContainSubstring(p.UserProfiles)))
			Expect(errs.Hints(err)).To(ContainElement("make " + p.UserProfiles + " a directory lola can read, or remove it"))
		})

		It("lists none without a profiles directory and refuses more than 256 files", func() {
			Expect(profile.Visible(p, true)).To(BeEmpty())
			for i := range 257 {
				write(fmt.Sprintf("p%d.yml", i), "")
			}
			_, _, _, err := profile.Visible(p, true)
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
			infos, invalid, ignored, _, err := profile.Project(root)
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
			infos, _, _, _, err := profile.Project(root)
			Expect(err).NotTo(HaveOccurred())
			Expect(infos).To(BeEmpty())
			Expect(os.MkdirAll(filepath.Join(root, ".lola"), 0o755)).To(Succeed())
			infos, _, _, _, err = profile.Project(root)
			Expect(err).NotTo(HaveOccurred())
			Expect(infos).To(BeEmpty())
			Expect(os.MkdirAll(filepath.Join(root, ".lola", "profiles"), 0o755)).To(Succeed())
			for i := range 257 {
				writeProject(fmt.Sprintf("p%d.yml", i), "")
			}
			_, _, _, _, err = profile.Project(root)
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

		It("names a profile in a directory holding control or format characters only as text that cannot drive the terminal", func() {
			dir := filepath.Join(p.Home, "evil\x1b[31m\u202eproj", ".lola", "profiles")
			Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
			path := filepath.Join(dir, "bad.yml")
			shown := strings.NewReplacer("\x1b", "?", "\u202e", "?").Replace(path)
			check := func(err error, want, why string) {
				GinkgoHelper()
				Expect(err).To(MatchError(And(ContainSubstring(shown), ContainSubstring(want))), why)
				Expect(err.Error()).NotTo(Or(ContainSubstring("\x1b"), ContainSubstring("\u202e")), why)
			}
			for content, want := range map[string]string{
				"version: 2\n":                                             "written by a newer lola",
				"version: 1\nshell: rm -rf\n":                              "field shell not found",
				"version: 1\ndescription: [a, b]\n":                        "cannot unmarshal",
				"version: 1\n---\nversion: 1\n":                            "holds more than one YAML document",
				"version: 1\nbundle: [a, a]\n":                             ":2: bundle lists a twice",
				"version: 1\nmodules:\n  - {name: f, source: '@acme/f'}\n": ":3: f's source is @acme/f",
			} {
				Expect(os.WriteFile(path, []byte(content), 0o644)).To(Succeed())
				_, err := profile.LoadProject(path)
				check(err, want, content)
			}
			Expect(os.Remove(path)).To(Succeed())
			_, err := profile.LoadProject(path)
			check(err, "file does not exist", "missing")
			Expect(err).To(MatchError(fs.ErrNotExist))

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
			err = pr.Unchanged(root, path)
			Expect(err).To(MatchError(path+" changed while lola was reading it; nothing was written"), "spec 2026-10-29 §6.3")
			Expect(errs.Hints(err)).To(Equal([]string{"run the same command again"}))

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
			_, _, _, _, err := profile.Project(root)
			Expect(err).To(MatchError(ContainSubstring(profiles + " is not a directory of the project's own")))

			Expect(os.Remove(profiles)).To(Succeed())
			Expect(os.WriteFile(profiles, nil, 0o600)).To(Succeed())
			_, _, _, _, err = profile.Project(root)
			Expect(err).To(MatchError(ContainSubstring(profiles + " is not a directory of the project's own")))

			Expect(os.RemoveAll(filepath.Join(root, ".lola"))).To(Succeed())
			Expect(os.Symlink(elsewhere, filepath.Join(root, ".lola"))).To(Succeed())
			_, _, _, _, err = profile.Project(root)
			Expect(err).To(MatchError(ContainSubstring(filepath.Join(root, ".lola") + " is not a directory of the project's own")))
			Expect(errs.Hints(err)).To(Equal([]string{"make it a directory, or remove it"}))
		})
	})
	Describe("bundles", func() {
		writeProject := func(root, name, content string) string {
			GinkgoHelper()
			path := filepath.Join(root, ".lola", "profiles", name)
			Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
			Expect(os.WriteFile(path, []byte(content), 0o644)).To(Succeed())
			return path
		}

		// extends: is gone (spec 2026-10-29 §7); its cap and its twice-named and invalid names are
		// bundle:'s own refusals now (the bundle shape entries below).
		It("refuses extends:, which bundles replaced, at its line, hinting the edit that keeps what it did", func() {
			refused := func(path, line string, hints ...string) {
				GinkgoHelper()
				_, err := profile.Load(path)
				if strings.Contains(path, ".lola") {
					_, err = profile.LoadProject(path)
				}
				Expect(err).To(MatchError(path + line + ": extends: is no longer a key; a bundle combines profiles now"))
				Expect(errs.Hints(err)).To(Equal(hints))
			}
			path := write("go.yml", "version: 1\ndescription: Go\nextends: [go, docs]\n")
			refused(path, ":3", "make it a bundle: in "+path+", replace extends: with bundle: [remote:go, remote:docs]")

			write("mine-modules.yml", "version: 1\n")
			path = write("mine.yml", "version: 1\ntargets: [claude-code]\nextends:\n  - go\nmodules:\n  - {name: a, source: ../a}\n")
			refused(path, ":3", "make it a bundle: move its modules: and targets: into a new profile beside it, "+
				filepath.Join(p.UserProfiles, "mine-modules-2.yml")+", starting with version: 1; then in "+path+
				" replace extends:, modules:, targets: with bundle: [remote:go, mine-modules-2]; the entries of remote:go then install to their own targets,"+
				" not targets: [claude-code]")

			path = write("default.yml", "version: 1\nextends: [go]\nmodules:\n  - {name: a, source: ../a}\n")
			refused(path, ":2", "remove extends: from "+path+", then switch to a bundle of them: lola profile --user default-bundle --switch --new --bundle remote:go")

			path = write("work.yml", "version: 1\nbundle: [docs]\nextends: [go]\n")
			refused(path, ":3", "remove extends: from "+path+" and list its profiles first in bundle: as remote:go")

			path = write("none.yml", "version: 1\nextends: []\n")
			refused(path, ":2", "remove extends: from "+path+"; it names no profile")
			path = write("odd.yml", "version: 1\nextends: go\n")
			refused(path, ":2", "replace extends: in "+path+" with bundle:, naming each remote profile it listed as remote:<name>")

			root := filepath.Join(p.Home, "proj")
			path = writeProject(root, "team.yml", "version: 1\nextends: [\"go\\u001b\"]\nmodules:\n  - {name: a, source: ../a}\n")
			refused(path, ":2", "make it a bundle: move its modules: into a new profile beside it, "+
				filepath.Join(root, ".lola", "profiles", "team-modules.yml")+", starting with version: 1; then in "+path+
				" replace extends:, modules: with bundle: [remote:go?, team-modules]")
		})

		It("classifies a file as modules, a bundle or empty, and keeps each member's line", func() {
			mods, err := profile.Load(write("go.yml", "version: 1\nmodules:\n  - {name: a, source: ../a}\n"))
			Expect(err).NotTo(HaveOccurred())
			Expect(mods.Shape()).To(Equal(profile.ShapeModules))
			empty, err := profile.Load(write("try.yml", "version: 1\ndescription: an experiment\n"))
			Expect(err).NotTo(HaveOccurred())
			Expect(empty.Shape()).To(Equal(profile.ShapeEmpty))
			none, err := profile.Load(write("none.yml", "version: 1\nbundle: []\n"))
			Expect(err).NotTo(HaveOccurred())
			Expect(none.Shape()).To(Equal(profile.ShapeBundle), "bundle: [] is a bundle of nothing yet")
			Expect(none.Bundle).To(BeEmpty())
			work, err := profile.Load(write("work.yml", "version: 1\ndescription: Go and docs work\nbundle:\n  - go-review\n  - docs\n  - remote:go\n"))
			Expect(err).NotTo(HaveOccurred())
			Expect(work.Shape()).To(Equal(profile.ShapeBundle))
			Expect(work.Bundle).To(Equal([]string{"go-review", "docs", "remote:go"}))
			Expect([]int{work.MemberLine(0), work.MemberLine(1), work.MemberLine(2)}).To(Equal([]int{4, 5, 6}))
			flow, err := profile.Load(write("flow.yml", "version: 1\nbundle: [go, user:docs, project:review]\n"))
			Expect(err).NotTo(HaveOccurred())
			Expect(flow.MemberLine(2)).To(Equal(2))
			spaced, err := profile.Load(write("spaced.yml", "version: 1\nbundle:\n  # the review half\n  - go-review\n\n  - docs\n"))
			Expect(err).NotTo(HaveOccurred())
			Expect([]int{spaced.MemberLine(0), spaced.MemberLine(1)}).To(Equal([]int{4, 6}), "each member's own line, past comments and blank lines")
		})

		It("refuses a file with both bundle: and modules: as the one shape lola can split", func() {
			path := write("work.yml", "version: 1\nmodules:\n  - {name: a, source: ../a}\nbundle: [go]\n")
			_, err := profile.Load(path)
			mixed, ok := errors.AsType[*profile.MixedError](err)
			Expect(ok).To(BeTrue(), "%v", err)
			Expect(mixed).To(Equal(&profile.MixedError{Path: path, Line: 4, Name: "work"}))
			Expect(err).To(MatchError(path + ":4: work has both bundle: and modules:; a bundle combines profiles and a profile lists modules"))
			Expect(errs.Hints(err)).To(Equal([]string{"fix it by hand"}), "the engine adds --split's hint, with the run's scope")
		})

		It("names the key's line when a block-style bundle: sits beside modules:", func() {
			path := write("mix.yml", "version: 1\nbundle:\n  - x\nmodules:\n  - {name: a, source: ../a}\n")
			_, err := profile.Load(path)
			mixed, ok := errors.AsType[*profile.MixedError](err)
			Expect(ok).To(BeTrue(), "%v", err)
			Expect(mixed.Line).To(Equal(2))
			Expect(err).To(MatchError(HavePrefix(path + ":2: ")))
		})

		DescribeTable("refuses a bundle lola would never write, naming the line",
			func(content, want string) {
				path := write("bad.yml", content)
				_, err := profile.Load(path)
				Expect(err).To(MatchError(HavePrefix(path + want)))
				_, mixed := errors.AsType[*profile.MixedError](err)
				Expect(mixed).To(BeFalse(), "no split fixes it")
			},
			Entry("targets", "version: 1\ntargets: [claude-code]\nbundle: [go]\n",
				":2: a bundle has no targets: its members' targets apply; remove targets:, or set them in its member profiles"),
			Entry("targets as a block list", "version: 1\nbundle: [go]\ntargets:\n  - claude-code\n",
				":3: a bundle has no targets: its members' targets apply; remove targets:, or set them in its member profiles"),
			Entry("default as a member", "version: 1\nbundle:\n  - go\n  - default\n", ":4: bundle: default is always loaded at user scope; a bundle does not need it"),
			Entry("user:default as a member", "version: 1\nbundle: [user:default]\n", ":2: bundle: default is always loaded at user scope; a bundle does not need it"),
			Entry("base as a member", "version: 1\nbundle: [base]\n", ":2: bundle: base is the scope's base, always loaded; a bundle does not need it"),
			Entry("a member twice", "version: 1\nbundle:\n  - go\n  - go\n", ":4: bundle lists go twice"),
			Entry("an unknown kind", "version: 1\nbundle: [market:go]\n", ":2: bundle: market:go is not a profile name"),
			Entry("an invalid name", "version: 1\nbundle: [\"../x\"]\n", ":2: bundle: profile name \"../x\" is invalid"),
			Entry("a member twice, its own line", "version: 1\ndescription: x\nbundle:\n  - go\n\n  - docs\n  - go\n", ":7: bundle lists go twice"),
			Entry("the same profile under two spellings", "version: 1\nbundle: [go, user:go]\n", ":2: bundle lists user:go twice, as go and user:go"),
			Entry("a null member", "version: 1\nbundle: [go, ~]\n", ":2: bundle: a member must be a profile name"),
			Entry("a null member before others", "version: 1\nbundle:\n  - ~\n  - go\n  - go\n", ":3: bundle: a member must be a profile name"),
			Entry("a list as a member", "version: 1\nbundle: [[a]]\n", ":2: bundle: a member must be a profile name"),
			Entry("a number as a member", "version: 1\nbundle: [go, 7]\n", ":2: bundle: a member must be a profile name"),
			Entry("bundle: ~", "version: 1\nbundle: ~\n", ":2: bundle: lists nothing; write bundle: [] for a bundle of no profiles yet, or list its profiles"),
			Entry("a bare bundle:", "version: 1\nbundle:\n", ":2: bundle: lists nothing; write bundle: [] for a bundle of no profiles yet, or list its profiles"),
			Entry("a name as bundle:", "version: 1\nbundle: go\n", ":2: bundle: is not a list of profile names; write it as bundle: [go, docs]"),
			Entry("a mapping as bundle:", "version: 1\nbundle: {a: b}\n", ":2: bundle: is not a list of profile names; write it as bundle: [go, docs]"),
			Entry("user:base as a member", "version: 1\nbundle: [user:base]\n", ":2: bundle: base is the scope's base, always loaded; a bundle does not need it"),
			Entry("project:base as a member", "version: 1\nbundle: [project:base]\n", ":2: bundle: base is the scope's base, always loaded; a bundle does not need it"),
			Entry("remote:base as a member", "version: 1\nbundle: [remote:base]\n", ":2: bundle: base is the scope's base, always loaded; a bundle does not need it"),
			Entry("project:default as a member", "version: 1\nbundle: [project:default]\n", ":2: bundle: default is always loaded at user scope; a bundle does not need it"),
			Entry("remote:default as a member", "version: 1\nbundle: [remote:default]\n", ":2: bundle: default is always loaded at user scope; a bundle does not need it"),
			Entry("an invalid qualified name", "version: 1\nbundle: [\"user:../x\"]\n", ":2: bundle: user:../x: profile name \"../x\" is invalid"),
			Entry("more than 64 members as a block list", "version: 1\nbundle:\n"+strings.Repeat("  - p\n", 65), ":2: bundle lists more than 64 profiles"),
			Entry("more than 64 members", "version: 1\nbundle: ["+strings.TrimSuffix(strings.Repeat("p, ", 65), ", ")+"]\n", ":2: bundle lists more than 64 profiles"),
		)

		It("refuses user: in a project's bundle, and reads an invalid member there as one it does not have", func() {
			root := filepath.Join(p.Home, "proj")
			path := writeProject(root, "team.yml", "version: 1\nbundle:\n  - review\n  - user:me\n")
			_, err := profile.LoadProject(path)
			Expect(err).To(MatchError(path + ":4: bundle: user:me is a profile of yours; a project's bundle names project and remote profiles only"))
			for _, m := range []string{"\"../x\"", "market:go"} {
				path = writeProject(root, "team.yml", "version: 1\nbundle: ["+m+"]\n")
				_, err = profile.LoadProject(path)
				Expect(err).To(MatchError(MatchRegexp(`team\.yml:2: bundle: .* is not a profile of this project or a remote profile you have$`)), m)
			}
			path = writeProject(root, "team.yml", "version: 1\nbundle: [review, project:review]\n")
			_, err = profile.LoadProject(path)
			Expect(err).To(MatchError(path+":2: bundle lists project:review twice, as review and project:review"), "a bare name is the project's")
			path = writeProject(root, "team.yml", "version: 1\nbundle: [project:base]\n")
			_, err = profile.LoadProject(path)
			Expect(err).To(MatchError(path+":2: bundle: base is the scope's base, always loaded; a bundle does not need it"), "the reservation is the same on every machine")
			Expect(profile.LoadProject(writeProject(root, "team.yml", "version: 1\nbundle: [review, project:lint, remote:go]\n"))).NotTo(BeNil())
		})

		It("refuses bundle: in a remote profile", func() {
			dir := filepath.Join(p.Home, "src", "profiles")
			Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(dir, "go.yml"), []byte("version: 1\nbundle: [docs]\n"), 0o644)).To(Succeed())
			_, _, err := profile.Fetched(dir, "go")
			Expect(err).To(MatchError("profiles/go.yml:2: bundle: a remote profile cannot combine profiles; its names would resolve on each reader's machine"))
			Expect(errs.Hints(err)).To(ContainElement("only your profiles and a project's may use bundle:"))
		})

		It("lists no base.yml as a profile, in any profiles directory, and names it for the listing to warn about", func() {
			write("go.yml", "")
			base := write("base.yml", "version: 1\n")
			infos, _, reserved, err := profile.Visible(p, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(reserved).To(Equal(base))
			Expect(infos).To(Equal([]profile.Info{{Name: "go", Kind: profile.KindUser, Path: filepath.Join(p.UserProfiles, "go.yml")}}))

			root := filepath.Join(p.Home, "proj")
			writeProject(root, "go.yml", "")
			pbase := writeProject(root, "base.yml", "version: 1\n")
			pinfos, _, _, preserved, err := profile.Project(root)
			Expect(err).NotTo(HaveOccurred())
			Expect(preserved).To(Equal(pbase))
			Expect(pinfos).To(ConsistOf(HaveField("Name", "go")))

			src := filepath.Join(p.Home, "src", "profiles")
			Expect(os.MkdirAll(src, 0o755)).To(Succeed())
			for _, n := range []string{"base.yml", "go.yml"} {
				Expect(os.WriteFile(filepath.Join(src, n), []byte("version: 1\n"), 0o644)).To(Succeed())
			}
			names, invalid, err := profile.FetchedNames(src)
			Expect(err).NotTo(HaveOccurred())
			Expect(names).To(Equal([]string{"go"}))
			Expect(invalid).To(Equal(1), "a source's base.yml is counted, as an invalid name is")
		})
	})
	Describe("SetBundle", func() {
		It("sets a bundle's members in place, keeping its comments and keys, and gives an empty profile its bundle:", func() {
			path := write("work.yml", "version: 1\n# Go and docs work\ndescription: work\nbundle: [go] # members\n")
			p, err := profile.Load(path)
			Expect(err).NotTo(HaveOccurred())
			out, err := p.SetBundle(path, []string{"go", "docs", "remote:team"})
			Expect(err).NotTo(HaveOccurred())
			Expect(string(out)).To(Equal("version: 1\n# Go and docs work\ndescription: work\nbundle: [go, docs, 'remote:team'] # members\n"))
			Expect(os.ReadFile(path)).To(Equal([]byte("version: 1\n# Go and docs work\ndescription: work\nbundle: [go] # members\n")), "the caller writes it")

			empty := write("idle.yml", "# nothing yet\nversion: 1\n")
			p, err = profile.Load(empty)
			Expect(err).NotTo(HaveOccurred())
			out, err = p.SetBundle(empty, []string{})
			Expect(err).NotTo(HaveOccurred())
			Expect(string(out)).To(Equal("# nothing yet\nversion: 1\nbundle: []\n"))
			Expect(profile.Parse(empty, out)).To(HaveField("Bundle", []string{}), "bundle: [] is a bundle of nothing")
		})

		It("empties a block list under a commented key into a list that loads again", func() {
			path := write("work.yml", "version: 1\nbundle: # members\n  - a\n")
			p, err := profile.Load(path)
			Expect(err).NotTo(HaveOccurred())
			out, err := p.SetBundle(path, []string{})
			Expect(err).NotTo(HaveOccurred())
			Expect(profile.Parse(path, out)).To(HaveField("Bundle", []string{}))
		})

		It("keeps a block list's style and each member's comments, adding new members in that style", func() {
			path := write("work.yml", "version: 1\nbundle:\n  # the go kit\n  - go   # go\n  - docs # docs\n")
			p, err := profile.Load(path)
			Expect(err).NotTo(HaveOccurred())
			out, err := p.SetBundle(path, []string{"docs", "go", "notes"})
			Expect(err).NotTo(HaveOccurred())
			Expect(string(out)).To(Equal("version: 1\nbundle:\n  - docs # docs\n  # the go kit\n  - go # go\n  - notes\n"))
		})

		It("refuses more members than a bundle holds, naming the line", func() {
			path := write("work.yml", "version: 1\nbundle: []\n")
			p, err := profile.Load(path)
			Expect(err).NotTo(HaveOccurred())
			many := make([]string, profile.MaxMembers+1)
			for k := range many {
				many[k] = fmt.Sprintf("m%d", k)
			}
			_, err = p.SetBundle(path, many)
			Expect(err).To(MatchError("not writing " + path + ", which lola could not load again: " + path + ":2: bundle lists more than 64 profiles"))
			Expect(os.ReadFile(path)).To(Equal([]byte("version: 1\nbundle: []\n")))
		})

		It("refuses a profile with targets: naming its line, a member the parse refuses, and a file changed since it was read", func() {
			path := write("work.yml", "version: 1\ntargets: [claude-code]\n")
			p, err := profile.Load(path)
			Expect(err).NotTo(HaveOccurred())
			_, err = p.SetBundle(path, []string{"go"})
			Expect(err).To(MatchError(path + ":2: targets: a bundle has none (its members' apply)"))
			Expect(errs.Hints(err)).To(Equal([]string{"remove targets: there, then run this again"}))

			path = write("work.yml", "version: 1\nbundle: []\n")
			p, err = profile.Load(path)
			Expect(err).NotTo(HaveOccurred())
			_, err = p.SetBundle(path, []string{"go", "default"})
			Expect(err).To(MatchError(ContainSubstring("not writing " + path + ", which lola could not load again: " + path + ":2:")))
			Expect(os.WriteFile(path, []byte("version: 1\nbundle: [x]\n"), 0o600)).To(Succeed())
			_, err = p.SetBundle(path, []string{"go"})
			Expect(err).To(MatchError(path + " changed while lola was reading it; nothing was written"))
		})
	})

	Describe("Mixed", func() {
		It("splits a file with both bundle: and modules: into a bundle and a profile, keeping comments and moving entries whole", func() {
			path := write("work.yml", "version: 1\n# Go and docs work\ndescription: work\ntargets: [claude-code]\nbundle: [go, docs]\nmodules:\n"+
				"  # the notes kit\n  - {name: notes, source: ../notes}\n")
			_, err := profile.Load(path)
			_, mixed := errors.AsType[*profile.MixedError](err)
			Expect(mixed).To(BeTrue())
			m, err := profile.ReadMixed(path, false)
			Expect(err).NotTo(HaveOccurred())
			bundle, mods, err := m.Split("work-modules")
			Expect(err).NotTo(HaveOccurred())
			Expect(string(bundle)).To(Equal("version: 1\n# Go and docs work\ndescription: work\nbundle: [go, docs, work-modules]\n"))
			Expect(string(mods)).To(Equal("version: 1\ntargets: [claude-code]\nmodules:\n  # the notes kit\n  - {name: notes, source: ../notes}\n"))
			Expect(profile.Parse(path, bundle)).To(HaveField("Bundle", []string{"go", "docs", "work-modules"}))
			Expect(m.Current("")).To(Succeed())
			Expect(os.WriteFile(path, []byte("version: 1\n"), 0o600)).To(Succeed())
			Expect(m.Current("")).To(MatchError(path + " changed while lola was reading it; nothing was written"))
		})

		It("moves the comments on the moved keys' lines and above them with the keys", func() {
			path := write("work.yml", "version: 1\nbundle: [go] # members\n# where it installs\ntargets: [claude-code] # one\n"+
				"# its own\nmodules: # kit\n  - {name: a, source: ../a}\n# the end\n")
			m, err := profile.ReadMixed(path, false)
			Expect(err).NotTo(HaveOccurred())
			bundle, mods, err := m.Split("work-modules")
			Expect(err).NotTo(HaveOccurred())
			Expect(string(bundle)).To(Equal("version: 1\nbundle: [go, work-modules] # members\n"))
			Expect(string(mods)).To(Equal("version: 1\n# where it installs\ntargets: [claude-code] # one\n# its own\nmodules: # kit\n  - {name: a, source: ../a}\n# the end\n"))
		})

		It("refuses a file that has not both keys, one whose bundle: is no list, and a project's that is a link", func() {
			_, err := profile.ReadMixed(write("go.yml", "version: 1\nmodules: []\n"), false)
			Expect(err).To(MatchError(filepath.Join(p.UserProfiles, "go.yml") + " has not both bundle: and modules:; there is nothing to split"))
			path := write("empty.yml", "version: 1\nbundle: [x]\nmodules: []\n")
			_, err = profile.ReadMixed(path, false)
			Expect(err).To(MatchError(path+" has not both bundle: and modules:; there is nothing to split"), "a bundle beside no modules is a bundle")
			path = write("work.yml", "version: 1\nbundle: go\nmodules:\n  - {name: a, source: ../a}\n")
			_, err = profile.ReadMixed(path, false)
			Expect(err).To(MatchError(path+":2: bundle: is not a list of profile names; write it as bundle: [go, docs]"), "the split would drop go")
			Expect(errs.Hints(err)).To(Equal([]string{"fix it by hand"}))
			link := filepath.Join(filepath.Dir(path), "linked.yml")
			Expect(os.WriteFile(path, []byte("version: 1\nbundle: [go]\nmodules:\n  - {name: a, source: ../a}\n"), 0o600)).To(Succeed())
			Expect(os.Symlink(path, link)).To(Succeed())
			_, err = profile.ReadMixed(link, true)
			Expect(err).To(MatchError(link + " is a symbolic link; a project's profile must be a file of its own"))
			Expect(errs.Hints(err)).To(Equal([]string{"replace it with the file it points to", "fix it by hand, or restore it from version control"}))
		})
	})
})
