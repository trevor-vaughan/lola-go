package migrate_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/migrate"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("Home", func() {
	BeforeEach(func() { testenv.Isolate() })
	env := func(vars map[string]string) func(string) string {
		return func(k string) string { return vars[k] }
	}

	It("is $LOLA_HOME when it is absolute, else ~/.lola", func() {
		Expect(migrate.Home(env(map[string]string{"LOLA_HOME": "/srv/lola/"}), "/home/u")).To(Equal("/srv/lola"))
		Expect(migrate.Home(env(map[string]string{}), "/home/u")).To(Equal("/home/u/.lola"))
		Expect(migrate.Home(env(map[string]string{"LOLA_HOME": ""}), "/home/u")).To(Equal("/home/u/.lola"))
	})

	It("refuses a relative $LOLA_HOME with a hint naming it", func() {
		_, err := migrate.Home(env(map[string]string{"LOLA_HOME": "lola-state"}), "/home/u")
		Expect(err).To(MatchError(ContainSubstring("LOLA_HOME must be an absolute path")))
		Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("LOLA_HOME")))
	})
})

var _ = Describe("Load", func() {
	var tmp, home, userHome, proj string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		userHome = filepath.Join(tmp, "home")
		home = filepath.Join(userHome, ".lola")
		proj = filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
	})
	installed := func(s string) { writeFile(filepath.Join(home, "installed.yml"), s) }
	sourceYML := func(module, s string) { writeFile(filepath.Join(home, "modules", module, ".lola", "source.yml"), s) }
	load := func() (*migrate.State, error) { return migrate.Load(home, userHome) }

	It("reads nothing when the Python home does not exist", func() {
		st, err := load()
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Records).To(BeEmpty())
		Expect(st.Exists).To(BeFalse())
	})

	It("reads installed.yml as the Python lola writes it, aliases and the old append_context included", func() {
		installed(`version: '1.0'
installations:
- module: review
  assistant: claude-code
  scope: project
  skills:
  - review
  - review_lint
  commands:
  - ship
  agents: []
  mcps:
  - lola-gh
  has_instructions: true
  project_path: ` + proj + `
  append_context: &id001
  - AGENTS.md
- module: review
  assistant: opencode
  scope: user
  skills: []
  commands: []
  agents:
  - critic
  mcps: []
  has_instructions: false
  ref: v1.2.0
  append_context: *id001
- module: legacy
  assistant: gemini-cli
  project_path: ` + proj + `
  append_context: docs/old.md
`)
		sourceYML("review", "source: https://example.com/acme/review.git\ntype: git\nref: main\ncontent_dirname: plugins/review\n")
		st, err := load()
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Exists).To(BeTrue())
		Expect(st.Warnings).To(BeEmpty())
		Expect(st.Records).To(HaveLen(3))
		r := st.Records[0]
		Expect(r.Module).To(Equal("review"))
		Expect(r.Assistant).To(Equal("claude-code"))
		Expect(r.Scope).To(Equal("project"))
		Expect(r.ProjectPath).To(Equal(proj))
		Expect(r.Problem).To(BeEmpty())
		Expect(r.Skills).To(Equal([]string{"review", "review_lint"}))
		Expect(r.Commands).To(Equal([]string{"ship"}))
		Expect(r.Agents).To(BeEmpty())
		Expect(r.MCPs).To(Equal([]string{"lola-gh"}))
		Expect(r.HasInstructions).To(BeTrue())
		Expect(r.AppendContext).To(Equal([]string{"AGENTS.md"}))
		Expect(st.Records[1].Scope).To(Equal("user"))
		Expect(st.Records[1].Ref).To(Equal("v1.2.0"))
		Expect(st.Records[1].AppendContext).To(Equal([]string{"AGENTS.md"}))
		Expect(st.Records[2].Scope).To(Equal("project"), "the Python lola's default scope")
		Expect(st.Records[2].Skills).To(BeEmpty())
		Expect(st.Records[2].AppendContext).To(Equal([]string{"docs/old.md"}))

		src, ok := st.Sources["review"]
		Expect(ok).To(BeTrue())
		Expect(src).To(Equal(migrate.Source{Location: "https://example.com/acme/review.git", Type: "git", Ref: "main", ContentDirname: "plugins/review"}))
		_, ok = st.Sources["legacy"]
		Expect(ok).To(BeFalse(), "a module without source.yml has no source")
	})

	It("warns about unknown keys instead of failing, since the Python format may grow", func() {
		installed("version: '1.0'\nfuture: 1\ninstallations:\n- module: m\n  assistant: claude-code\n  scope: user\n  color: blue\n")
		sourceYML("m", "source: /x\ntype: folder\nchecksum: abc\n")
		st, err := load()
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Warnings).To(ConsistOf(
			ContainSubstring("installed.yml:2: future: unknown key, ignored"),
			ContainSubstring("installed.yml:7: installations[0].color: unknown key, ignored"),
			ContainSubstring("source.yml:3: checksum: unknown key, ignored"),
		))
	})

	DescribeTable("refuses a malformed installed.yml, naming the file, line and key, with a hint",
		func(body, want string) {
			installed(body)
			_, err := load()
			Expect(err).To(MatchError(ContainSubstring(want)))
			Expect(err.Error()).To(ContainSubstring(filepath.Join(home, "installed.yml")))
			Expect(errs.Hints(err)).NotTo(BeEmpty())
			Expect(err.Error()).NotTo(ContainSubstring("\x1b"))
			Expect(err.Error()).NotTo(ContainSubstring("\u202e"))
			Expect(err.Error()).NotTo(MatchRegexp(`\[\]string|map\[|\*yaml|interface`), "never Go type names")
		},
		Entry("not YAML", "installations: [\n", "does not parse"),
		Entry("a list at the top", "- a\n", "must be a mapping"),
		Entry("a newer format", "version: '2.0'\ninstallations: []\n", `version "2.0"`),
		Entry("installations is a mapping", "installations: {a: 1}\n", "installations: must be a list"),
		Entry("a record is a string", "installations:\n- review\n", "installations[0]: must be a mapping"),
		Entry("no module", "installations:\n- assistant: claude-code\n", "installations[0]: module is missing"),
		Entry("module is a list", "installations:\n- module: [a]\n  assistant: claude-code\n", "installations[0].module: must be a string"),
		Entry("module is a number", "installations:\n- module: 7\n  assistant: claude-code\n", "installations[0].module: must be a string"),
		Entry("traversal in a module name", "installations:\n- module: ../../etc\n  assistant: claude-code\n", `"../../etc" is not a single path component`),
		Entry("a dot-dot skill", "installations:\n- module: m\n  assistant: claude-code\n  skills: ['..']\n", `installations[0].skills[0]: ".." is not a single path component`),
		Entry("a backslash in a command", "installations:\n- module: m\n  assistant: claude-code\n  commands: ['a\\b']\n", `installations[0].commands[0]`),
		Entry("a control character in an agent", "installations:\n- module: m\n  assistant: claude-code\n  agents: [\"a\\eb\"]\n", `installations[0].agents[0]: "a?b" is not a single path component`),
		Entry("a format character in an MCP name", "installations:\n- module: m\n  assistant: claude-code\n  mcps: [\"a\\u202eb\"]\n", `installations[0].mcps[0]: "a?b" is not a single path component`),
		Entry("a NUL in an assistant", "installations:\n- module: m\n  assistant: \"a\\0b\"\n", `installations[0].assistant`),
		Entry("skills is a mapping", "installations:\n- module: m\n  assistant: claude-code\n  skills: {a: b}\n", "installations[0].skills: must be a list"),
		Entry("a skill is a list", "installations:\n- module: m\n  assistant: claude-code\n  skills: [[a]]\n", "installations[0].skills[0]: must be a string"),
		Entry("has_instructions is a string", "installations:\n- module: m\n  assistant: claude-code\n  has_instructions: 'yes'\n", "installations[0].has_instructions: must be true or false"),
		Entry("project_path is a list", "installations:\n- module: m\n  assistant: claude-code\n  project_path: [a]\n", "installations[0].project_path: must be a string"),
		Entry("append_context holds a number", "installations:\n- module: m\n  assistant: claude-code\n  append_context: [1]\n", "installations[0].append_context[0]: must be a string"),
		Entry("a key written twice", "installations:\n- module: m\n  module: n\n  assistant: claude-code\n", "installations[0].module: written twice"),
		Entry("a second document", "installations: []\n---\ninstallations: []\n", "more than one YAML document"),
	)

	It("refuses an installed.yml over 1 MiB", func() {
		installed("installations: []\n# " + strings.Repeat("x", 1<<20) + "\n")
		_, err := load()
		Expect(err).To(MatchError(ContainSubstring("is larger than 1048576 bytes")))
	})

	It("refuses an installed.yml that expands aliases beyond the entry budget", func() {
		var b strings.Builder
		b.WriteString("installations:\n- module: m\n  assistant: claude-code\n  skills: &s\n")
		for i := range 2000 {
			b.WriteString("  - s" + strings.Repeat("x", i%3) + "\n")
		}
		for range 60 {
			b.WriteString("- module: m\n  assistant: claude-code\n  skills: *s\n")
		}
		installed(b.String())
		_, err := load()
		Expect(err).To(MatchError(ContainSubstring("more than 100000 names")))
	})

	It("refuses an installed.yml that expands an aliased record with many unknown keys beyond the budget", func() {
		var b strings.Builder
		b.WriteString("installations:\n- &r\n  module: m\n  assistant: claude-code\n  scope: user\n")
		for i := range 3000 {
			b.WriteString("  k" + strconv.Itoa(i) + ": 1\n")
		}
		for range 100 {
			b.WriteString("- *r\n")
		}
		installed(b.String())
		_, err := load()
		Expect(err).To(MatchError(ContainSubstring("more than 100000 names")))
		Expect(errs.Hints(err)).NotTo(BeEmpty())
	})

	It("refuses an installed.yml that is not a regular file, without blocking on a FIFO", func() {
		Expect(os.MkdirAll(home, 0o755)).To(Succeed())
		mkfifo(filepath.Join(home, "installed.yml"))
		_, err := load()
		Expect(err).To(MatchError(ContainSubstring("is not a regular file")))
	})

	It("refuses a Python home that is not a directory", func() {
		writeFile(home, "not a directory")
		_, err := load()
		Expect(err).To(MatchError(ContainSubstring("is not a directory")))
		Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("LOLA_HOME")))
	})

	DescribeTable("refuses a malformed source.yml",
		func(body, want string) {
			installed("installations:\n- module: m\n  assistant: claude-code\n  scope: user\n")
			sourceYML("m", body)
			_, err := load()
			Expect(err).To(MatchError(ContainSubstring(want)))
			Expect(err.Error()).To(ContainSubstring(filepath.Join(home, "modules", "m", ".lola", "source.yml")))
			Expect(errs.Hints(err)).NotTo(BeEmpty())
		},
		Entry("not a mapping", "- a\n", "must be a mapping"),
		Entry("no source", "type: git\n", "source is missing"),
		Entry("no type", "source: x\n", "type is missing"),
		Entry("source is a list", "source: [a]\ntype: git\n", "source: must be a string"),
		Entry("ref is a mapping", "source: x\ntype: git\nref: {a: b}\n", "ref: must be a string"),
		Entry("a control character in the source", "source: \"a\\eb\"\ntype: git\n", "source: holds a control or format character"),
	)

	It("refuses an oversized source.yml", func() {
		installed("installations:\n- module: m\n  assistant: claude-code\n  scope: user\n")
		sourceYML("m", "source: x\ntype: git\n# "+strings.Repeat("x", 1<<20)+"\n")
		_, err := load()
		Expect(err).To(MatchError(ContainSubstring("is larger than 1048576 bytes")))
	})

	DescribeTable("marks a project_path the design refuses as a problem, not an error",
		func(path func() string, want string) {
			installed("installations:\n- module: m\n  assistant: claude-code\n  project_path: '" + path() + "'\n")
			st, err := load()
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Records[0].Problem).To(ContainSubstring(want))
		},
		Entry("relative", func() string { return "proj" }, "is not an absolute path"),
		Entry("not clean", func() string { return proj + "/../proj" }, "is not in its clean form"),
		Entry("gone", func() string { return filepath.Join(tmp, "gone") }, "no longer exists"),
		Entry("a symbolic link", func() string {
			link := filepath.Join(tmp, "link")
			Expect(os.Symlink(proj, link)).To(Succeed())
			return link
		}, "is a symbolic link or has one on its way"),
		Entry("the root", func() string { return "/" }, "is your home directory or one of its parents"),
		Entry("the home directory", func() string { return userHome }, "is your home directory or one of its parents"),
		Entry("a parent of the home directory", func() string { return tmp }, "is your home directory or one of its parents"),
		Entry("a file", func() string {
			writeFile(filepath.Join(tmp, "file"), "x")
			return filepath.Join(tmp, "file")
		}, "is not a directory"),
	)

	It("marks a project record without project_path, and a scope it does not know", func() {
		installed("installations:\n- module: m\n  assistant: claude-code\n- module: m\n  assistant: claude-code\n  scope: galaxy\n")
		st, err := load()
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Records[0].Problem).To(Equal("project scope without a project_path"))
		Expect(st.Records[1].Problem).To(Equal(`unknown scope "galaxy"`))
	})

	It("marks a project_path this machine does not have as missing, another OS's absolute path included", func() {
		installed("installations:\n" +
			"- {module: m, assistant: claude-code, project_path: '" + filepath.Join(tmp, "gone") + "'}\n" +
			"- {module: m, assistant: claude-code, project_path: 'C:\\Users\\alice\\proj'}\n" +
			"- {module: m, assistant: claude-code, project_path: proj}\n" +
			"- {module: m, assistant: claude-code, project_path: '" + proj + "'}\n")
		st, err := load()
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Records[0].Missing).To(BeTrue())
		Expect(st.Records[0].Problem).To(Equal("project_path " + filepath.Join(tmp, "gone") + " no longer exists"))
		Expect(st.Records[1].Missing).To(BeTrue(), "another OS's absolute path never exists here")
		Expect(st.Records[1].Problem).To(Equal(`project_path C:\Users\alice\proj no longer exists`))
		Expect(st.Records[2].Missing).To(BeFalse(), "a relative path is refused, never looked for")
		Expect(st.Records[3].Missing).To(BeFalse())
		Expect(st.Records[3].Problem).To(BeEmpty())
	})
})
