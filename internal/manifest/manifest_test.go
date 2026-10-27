package manifest_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("manifest", func() {
	var path string
	BeforeEach(func() { path = filepath.Join(testenv.Isolate(), ".lola", "project.yml") })

	It("loads a missing file as version 1 with no modules", func() {
		m, err := manifest.Load(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(m).To(Equal(&manifest.Manifest{Version: 1}))
	})

	It("finds entries by name", func() {
		m := &manifest.Manifest{Version: 1, Modules: []manifest.Entry{{Name: "a", Source: "../a"}}}
		Expect(m.Find("a")).To(HaveField("Source", "../a"))
		Expect(m.Find("nope")).To(BeNil())
	})

	Describe("Edit", func() {
		sha1 := strings.Repeat("a", 40)
		digest := "sha256:" + strings.Repeat("0", 64)
		write := func(content string) {
			Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
			Expect(os.WriteFile(path, []byte(content), 0o600)).To(Succeed())
		}
		edit := func(c manifest.Change) string {
			dst, b, err := manifest.Edit(path, c)
			Expect(err).NotTo(HaveOccurred())
			Expect(dst).To(Equal(path))
			return string(b)
		}
		commented := `# the team's modules
version: 1
targets: [claude-code]
modules:
  # pinned by hand-review
  - name: zeta
    source: https://example.com/zeta.git
    ref: v1 # what --update follows
  - name: alpha
    source: tools/alpha # a folder
`

		It("changes only the fields of the entries it sets, keeping comments and order", func() {
			write(commented)
			out := edit(manifest.Change{Set: []manifest.Entry{
				{Name: "zeta", Source: "https://example.com/zeta.git", Ref: "v1", Commit: sha1, Digest: digest},
				{Name: "alpha", Source: "tools/alpha"},
			}})
			Expect(out).To(Equal(`# the team's modules
version: 1
targets: [claude-code]
modules:
  # pinned by hand-review
  - name: zeta
    source: https://example.com/zeta.git
    ref: v1 # what --update follows
    commit: ` + sha1 + `
    digest: ` + digest + `
  - name: alpha
    source: tools/alpha # a folder
`))
		})

		It("appends a new entry and deletes a removed one with its comment", func() {
			write(commented)
			out := edit(manifest.Change{Set: []manifest.Entry{{Name: "beta", Source: "../beta", Targets: []string{"gemini-cli"}}},
				Remove: []string{"zeta"}})
			Expect(out).NotTo(ContainSubstring("zeta"))
			Expect(out).NotTo(ContainSubstring("hand-review"))
			Expect(out).To(ContainSubstring("# the team's modules"))
			Expect(strings.Index(out, "alpha")).To(BeNumerically("<", strings.Index(out, "beta")))
			Expect(out).To(ContainSubstring("targets: [gemini-cli]"))
		})

		It("Applied says in memory what Edit writes, and leaves the manifest it applies to alone", func() {
			write(commented)
			loaded, err := manifest.Load(path)
			Expect(err).NotTo(HaveOccurred())
			off := false
			c := manifest.Change{Targets: []string{"opencode"}, Remove: []string{"zeta"},
				Set: []manifest.Entry{{Name: "alpha", Source: "tools/alpha", Rules: &off, Targets: []string{"cursor"}}, {Name: "beta", Source: "../beta"}}}
			Expect(os.WriteFile(path, []byte(edit(c)), 0o600)).To(Succeed())
			written, err := manifest.Load(path)
			Expect(err).NotTo(HaveOccurred())
			before := loaded.Clone()
			Expect(loaded.Applied(c)).To(Equal(written))
			Expect(loaded).To(Equal(before))
		})

		It("drops fields an entry no longer has and writes rules: false", func() {
			write(commented)
			off := false
			out := edit(manifest.Change{Set: []manifest.Entry{{Name: "zeta", Source: "https://example.com/zeta.git", Rules: &off}}})
			Expect(out).NotTo(ContainSubstring("ref:"))
			Expect(out).To(ContainSubstring("rules: false"))
		})

		It("starts a new file with the version, the targets and the entries", func() {
			out := edit(manifest.Change{Targets: []string{"claude-code"}, Set: []manifest.Entry{{Name: "kit", Source: "https://example.com/kit.git", Ref: "1.0"}}})
			Expect(out).To(MatchYAML(`
version: 1
targets: [claude-code]
modules:
  - name: kit
    source: https://example.com/kit.git
    ref: "1.0"
`))
			write(out)
			m, err := manifest.Load(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(m.Find("kit").Ref).To(Equal("1.0"))
		})

		It("keeps a file of comments and gives it a version", func() {
			write("# notes for the team\n")
			out := edit(manifest.Change{Set: []manifest.Entry{{Name: "kit", Source: "../kit"}}})
			Expect(out).To(HavePrefix("# notes for the team\n"))
			Expect(out).To(ContainSubstring("version: 1"))
		})

		It("reads and writes an entry's market as provenance, after its other keys, and refuses one it could not name back", func() {
			write("version: 1\nmodules:\n  - name: formatter\n    source: https://example.com/monorepo.git\n    path: tools/formatter\n" +
				"    market: {name: acme, source: https://example.com/plugins.git}\n")
			m, err := manifest.Load(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(m.Modules[0].Market).To(Equal(&manifest.MarketRef{Name: "acme", Source: "https://example.com/plugins.git"}))
			out := edit(manifest.Change{Set: []manifest.Entry{{Name: "kit", Source: "https://example.com/kit.git", Commit: sha1, Digest: digest,
				Targets: []string{"claude-code"}, Market: &manifest.MarketRef{Name: "acme", Source: "/home/me/acme"}}}})
			Expect(out).To(HaveSuffix("  - name: kit\n    source: https://example.com/kit.git\n    commit: " + sha1 + "\n    digest: " + digest +
				"\n    targets: [claude-code]\n    market:\n      name: acme\n      source: /home/me/acme\n"))
			Expect(edit(manifest.Change{Set: []manifest.Entry{{Name: "formatter", Source: "https://example.com/monorepo.git", Path: "tools/formatter"}}})).
				NotTo(ContainSubstring("market"), "an entry set without market loses it")
			for market, want := range map[string]string{
				"{name: Acme, source: https://example.com/p.git}":          `module formatter: market: marketplace name "Acme" is invalid`,
				"{name: acme, source: \"https://e.com/p\\u0007\"}":         "module formatter: market: source holds control or invisible characters",
				"{name: \"a\\u200bb\", source: https://example.com/p.git}": "module formatter: market: name holds control or invisible characters",
				"{name: acme}": "module formatter: market: source is empty",
				"{name: acme, source: https://e.com/p.git, ref: main}": "field ref not found",
			} {
				write("version: 1\nmodules:\n  - name: formatter\n    source: https://example.com/monorepo.git\n    market: " + market + "\n")
				_, err := manifest.Load(path)
				Expect(err).To(MatchError(ContainSubstring(want)), market)
			}
		})

		It("reads and writes the components an entry selects, and refuses a selection lola could not apply", func() {
			out := edit(manifest.Change{Set: []manifest.Entry{{Name: "sheets", Source: "https://example.com/skills.git", Commit: sha1, Digest: digest,
				Components: &source.Components{Skills: []string{"skills/xlsx", "skills/docx"}, Agents: []string{"agents/a.md"}}}}})
			Expect(out).To(HaveSuffix("  - name: sheets\n    source: https://example.com/skills.git\n    components:\n      skills: [skills/xlsx, skills/docx]\n" +
				"      agents: [agents/a.md]\n    commit: " + sha1 + "\n    digest: " + digest + "\n"))
			write(out)
			m, err := manifest.Load(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(m.Find("sheets").Components).To(Equal(&source.Components{Skills: []string{"skills/xlsx", "skills/docx"}, Agents: []string{"agents/a.md"}}))
			for comps, want := range map[string]string{
				"{skills: [../x]}":          `components: skills path "../x" is not a clean relative path`,
				"{skills: []}":              "components: selects no skill, command or agent",
				"{skills: [a], tools: [b]}": "field tools not found",
				"{commands: [a/x, b/x]}":    "components: commands lists two paths named x",
				"{skills: [" + strings.Repeat("s/a,", 1024) + "s/a]}": "components: selects 1025 paths; lola takes at most 1024",
			} {
				write("version: 1\nmodules:\n  - name: sheets\n    source: https://example.com/skills.git\n    components: " + comps + "\n")
				_, err := manifest.Load(path)
				Expect(err).To(MatchError(ContainSubstring(want)), comps)
			}
		})

		It("never edits a file that does not load", func() {
			write("version: 1\nmodules:\n  - name: kit\n    source: https://example.com/kit.git\n    ref: --upload-pack=x\n")
			_, _, err := manifest.Edit(path, manifest.Change{Set: []manifest.Entry{{Name: "other", Source: "../other"}}})
			Expect(err).To(MatchError(ContainSubstring("starts with '-'")))
			write("version: 1\nmodules: [\n")
			_, _, err = manifest.Edit(path, manifest.Change{})
			Expect(err).To(MatchError(ContainSubstring("does not parse")))
		})

		It("refuses to produce a file it could not load again", func() {
			write(commented)
			_, _, err := manifest.Edit(path, manifest.Change{Set: []manifest.Entry{
				{Name: "zeta", Source: "https://example.com/zeta.git", Ref: "v1", Commit: "nope", Digest: digest}}})
			Expect(err).To(MatchError(And(ContainSubstring("module zeta"), ContainSubstring("commit"))))
			Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("nothing was written")))
			Expect(errs.Hints(err)).NotTo(ContainElement(ContainSubstring("fix it by hand")))
			Expect(os.ReadFile(path)).To(Equal([]byte(commented)))
		})

		It("writes a symlinked manifest at the link's target", func() {
			real := filepath.Join(filepath.Dir(filepath.Dir(path)), "shared", "project.yml")
			Expect(os.MkdirAll(filepath.Dir(real), 0o755)).To(Succeed())
			Expect(os.WriteFile(real, []byte(commented), 0o600)).To(Succeed())
			Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
			Expect(os.Symlink(real, path)).To(Succeed())
			dst, _, err := manifest.Edit(path, manifest.Change{})
			Expect(err).NotTo(HaveOccurred())
			Expect(dst).To(Equal(mustEval(real)))
		})
	})

	Describe("an entry whose source is a marketplace address", func() {
		write := func(content string) {
			Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
			Expect(os.WriteFile(path, []byte(content), 0o600)).To(Succeed())
		}
		const two = "version: 1\nmodules:\n  - name: formatter\n    source: \"@acme/formatter\"\n" +
			"  - name: ok\n    source: https://example.com/ok.git\n  - {name: lint, source: '@other/lint', ref: v1}\n"

		It("is reported as unresolved, naming the file, the module and its source's line, with the file as read", func() {
			write(two)
			m, err := manifest.Load(path)
			Expect(m).To(BeNil())
			ue, ok := errors.AsType[*manifest.UnresolvedError](err)
			Expect(ok).To(BeTrue(), "%v", err)
			Expect(ue.Path).To(Equal(path))
			Expect(ue.Entries).To(Equal([]manifest.Unresolved{{Name: "formatter", Market: "acme", Module: "formatter", Line: 4},
				{Name: "lint", Market: "other", Module: "lint", Line: 7}}))
			Expect(ue.Manifest.Modules).To(HaveLen(3), "every entry as written")
			Expect(ue.Manifest.Find("lint")).To(And(HaveField("Source", "@other/lint"), HaveField("Ref", "v1")))
			Expect(err).To(MatchError(path + " records 2 marketplace addresses as sources; a file must record each module's real source\n" +
				"  " + path + ":4: formatter's source is @acme/formatter; edit the entry to name the module's source, ref and path\n" +
				"  " + path + ":7: lint's source is @other/lint; edit the entry to name the module's source, ref and path"))
			Expect(errs.Hints(err)).To(BeEmpty(), "the engine coaches it from the reader's marketplaces")

			ue.Entries[0].Coached = true
			Expect(err.Error()).To(ContainSubstring(":4: formatter's source is @acme/formatter\n"), "a coached entry has its hint instead")

			write("version: 1\nmodules:\n  - name: formatter\n    source: \"@acme/formatter\"\n")
			_, err = manifest.Load(path)
			Expect(err).To(MatchError(path + ":4: formatter's source is @acme/formatter, a marketplace address; a file must record the module's " +
				"real source; edit the entry to name the module's source, ref and path"))
		})

		It("is reported only once the file passes every other check", func() {
			write(two + "  - name: ok\n    source: https://example.com/ok.git\n")
			_, err := manifest.Load(path)
			Expect(err).To(MatchError(ContainSubstring("module ok is listed twice")))
			_, unresolved := errors.AsType[*manifest.UnresolvedError](err)
			Expect(unresolved).To(BeFalse())

			write("version: 1\nmodules:\n  - {name: x, source: '@Acme/x'}\n  - {name: y, source: '@acme'}\n")
			_, err = manifest.Load(path)
			Expect(err).NotTo(HaveOccurred(), "not the form @<market>/<module>: a directory of that name, as before")
		})

		It("is kept as written when Edit sets another entry", func() {
			write(two)
			_, b, err := manifest.Edit(path, manifest.Change{Set: []manifest.Entry{{Name: "formatter", Source: "https://example.com/fmt.git"}}})
			Expect(err).NotTo(HaveOccurred())
			Expect(string(b)).To(Equal("version: 1\nmodules:\n  - name: formatter\n    source: \"https://example.com/fmt.git\"\n" +
				"  - name: ok\n    source: https://example.com/ok.git\n  - {name: lint, source: '@other/lint', ref: v1}\n"))
		})
	})

	It("rejects unknown keys and newer versions", func() {
		Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
		Expect(os.WriteFile(path, []byte("version: 1\nmodule: []\n"), 0o600)).To(Succeed())
		_, err := manifest.Load(path)
		Expect(err).To(MatchError(ContainSubstring("module")))
		Expect(os.WriteFile(path, []byte("version: 9\n"), 0o600)).To(Succeed())
		_, err = manifest.Load(path)
		Expect(err).To(MatchError(ContainSubstring("newer lola")))
	})

	It("validates module names and rejects path traversal attempts", func() {
		Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
		for _, badName := range []string{"..", "../x", "Bad Name"} {
			Expect(os.WriteFile(path, []byte("version: 1\nmodules:\n  - name: "+badName+"\n    source: x\n"), 0o600)).To(Succeed())
			_, err := manifest.Load(path)
			Expect(err).To(HaveOccurred(), "should reject module name %q", badName)
			Expect(err.Error()).To(ContainSubstring(path), "error should name the file")
		}
	})

	Describe("reading the file", func() {
		write := func(content string) {
			Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
			Expect(os.WriteFile(path, []byte(content), 0o600)).To(Succeed())
		}

		It("refuses a newer version before it reads the other keys", func() {
			write("version: 9\nprofiles: [a]\n")
			_, err := manifest.Load(path)
			Expect(err).To(MatchError(ContainSubstring("newer lola (manifest version 9)")))
		})

		It("refuses a file over 1 MiB", func() {
			write("version: 1\n# " + strings.Repeat("x", 1<<20) + "\n")
			_, err := manifest.Load(path)
			Expect(err).To(MatchError(ContainSubstring("is larger than 1048576 bytes")))
		})

		It("refuses a FIFO in place of the file without blocking on it", func() {
			Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
			mkfifo(path)
			done := make(chan error, 1)
			go func() { _, err := manifest.Load(path); done <- err }()
			Eventually(done, "5s").Should(Receive(MatchError(ContainSubstring("is not a regular file"))))
		})

		It("refuses a second YAML document", func() {
			write("version: 1\n---\nversion: 1\n")
			_, err := manifest.Load(path)
			Expect(err).To(MatchError(ContainSubstring("holds more than one YAML document")))
		})

		It("refuses anchors, aliases and merge keys", func() {
			write("version: 1\nmodules:\n  - &m\n    name: a\n    source: https://x/a.git\n  - *m\n")
			_, err := manifest.Load(path)
			Expect(err).To(MatchError(ContainSubstring("anchors, aliases and merge keys (<<) are not supported")))
		})

		It("reads a file holding only comments as an empty manifest", func() {
			write("# nothing yet\n")
			m, err := manifest.Load(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(m).To(Equal(&manifest.Manifest{Version: 1}))
		})
	})

	Describe("entry fields", func() {
		sha1 := strings.Repeat("a", 40)
		digest := "sha256:" + strings.Repeat("0", 64)
		write := func(content string) {
			Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
			Expect(os.WriteFile(path, []byte(content), 0o600)).To(Succeed())
		}
		entry := func(fields string) string {
			return "version: 1\nmodules:\n  - name: kit\n" + fields
		}

		It("loads pins and saves them back", func() {
			write(entry("    source: https://example.com/kit.git\n    ref: v1\n    commit: " + sha1 + "\n    digest: " + digest + "\n"))
			m, err := manifest.Load(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(m.Find("kit")).To(And(HaveField("Commit", sha1), HaveField("Digest", digest)))
		})

		It("accepts a pinned archive and an unpinned folder", func() {
			write("version: 1\nmodules:\n  - name: a\n    source: https://example.com/a.tgz\n    digest: " + digest +
				"\n  - name: b\n    source: tools/b\n")
			_, err := manifest.Load(path)
			Expect(err).NotTo(HaveOccurred())
		})

		DescribeTable("refuses, naming the file, the entry and the field",
			func(content, msg string) {
				write(content)
				_, err := manifest.Load(path)
				Expect(err).To(MatchError(And(ContainSubstring(path), ContainSubstring(msg))))
				Expect(errs.Hints(err)).NotTo(BeEmpty())
			},
			Entry("a hostile ref", entry("    source: https://example.com/kit.git\n    ref: --upload-pack=touch PWNED\n"),
				`module kit: ref "--upload-pack=touch PWNED" starts with '-'`),
			Entry("a remote helper source", entry("    source: ext::sh -c touch PWNED\n"), "module kit: source"),
			Entry("a git:// source", entry("    source: git://example.com/kit.git\n"), "module kit: a git:// source is refused"),
			Entry("a path leaving the source", entry("    source: https://example.com/kit.git\n    path: ../x\n"),
				`module kit: path "../x" must be a relative path inside the source`),
			Entry("a short commit", entry("    source: https://example.com/kit.git\n    commit: abc\n    digest: "+digest+"\n"),
				"module kit: commit"),
			Entry("a commit without a digest", entry("    source: https://example.com/kit.git\n    commit: "+sha1+"\n"),
				"module kit: commit without digest"),
			Entry("a commit that is not the full-SHA ref", entry("    source: https://example.com/kit.git\n    ref: "+strings.Repeat("b", 40)+
				"\n    commit: "+sha1+"\n    digest: "+digest+"\n"), "module kit: commit must equal ref"),
			Entry("a pinned folder", entry("    source: tools/kit\n    digest: "+digest+"\n"),
				"module kit: tools/kit is a folder, and folders are never pinned"),
			Entry("a name used twice", "version: 1\nmodules:\n  - name: kit\n    source: a/kit\n  - name: kit\n    source: b/kit\n",
				"module kit is listed twice"),
			Entry("a control character in a target", "version: 1\ntargets: [\"claude\\u202ecode\"]\n",
				"targets holds control or invisible characters"),
			Entry("a control character in an entry's target", entry("    source: a/kit\n    targets: [\"x\\ny\"]\n"),
				"module kit: targets holds control or invisible characters"),
		)
	})
})

func mustEval(p string) string {
	r, err := filepath.EvalSymlinks(p)
	Expect(err).NotTo(HaveOccurred())
	return r
}
