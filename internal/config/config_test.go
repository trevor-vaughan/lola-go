package config_test

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/gofrs/flock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// example is the selections.yaml of the design (§6.2), with its project key under dir.
func example(dir string) string {
	return `# lola selections: components you chose not to install.
version: 1
global:
  review-kit:                       # module name as lola list shows it
    skip: [skill/slow-lint, mcp/internal-db]
user:
  review-kit:
    include: [mcp/internal-db]      # overrides a global skip at user scope
projects:
  ` + dir + `:
    review-kit:
      skip: [command/deploy]
      include: [skill/slow-lint]
`
}

var _ = Describe("config.yaml", func() {
	var dir, path string
	BeforeEach(func() {
		dir = testenv.Isolate()
		path = filepath.Join(dir, "config.yaml")
	})
	write := func(s string) { Expect(os.WriteFile(path, []byte(s), 0o600)).To(Succeed()) }

	It("defaults when the file is missing or empty", func() {
		c, err := config.LoadConfig(path, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(c.DefaultScope).To(BeEmpty())
		Expect(c.Pager).To(BeTrue())
		write("# nothing yet\n")
		c, err = config.LoadConfig(path, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(c.DefaultScope).To(BeEmpty())
		Expect(c.Pager).To(BeTrue())
	})

	It("reads pager (Plan 6b §9)", func() {
		write("version: 1\npager: false\n")
		c, err := config.LoadConfig(path, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Pager).To(BeFalse())
		write("version: 1\npager: true\n")
		c, err = config.LoadConfig(path, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Pager).To(BeTrue())
	})

	It("reads default_scope", func() {
		write("# lola settings. Safe to edit by hand.\nversion: 1\ndefault_scope: both   # project | user | both\n")
		c, err := config.LoadConfig(path, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(c.DefaultScope).To(Equal("both"))
	})

	It("reads pre_install and post_install, expanding ~/ (Plan 9 §5.1)", func() {
		write("version: 1\npre_install: ~/bin/pre.sh\npost_install: /opt/ci/after.sh\n")
		c, err := config.LoadConfig(path, "/home/u")
		Expect(err).NotTo(HaveOccurred())
		Expect(c.PreInstall).To(Equal(config.Script{Path: "/home/u/bin/pre.sh", Line: 2, Key: "pre_install"}))
		Expect(c.PostInstall).To(Equal(config.Script{Path: "/opt/ci/after.sh", Line: 3, Key: "post_install"}))
	})

	DescribeTable("refuses invalid files, naming the file, line and key with a hint",
		func(content, msg, hint string) {
			write(content)
			_, err := config.LoadConfig(path, "")
			Expect(err).To(MatchError(ContainSubstring(path + ":")))
			Expect(err).To(MatchError(ContainSubstring(msg)))
			Expect(errs.Hints(err)).To(ContainElement(ContainSubstring(hint)))
		},
		Entry("an unknown scope", "version: 1\ndefault_scope: everywhere\n", `:2: default_scope: "everywhere" is not a scope`, "project, user or both"),
		Entry("an unknown key", "version: 1\ndefault_scop: user\n", `:2: unknown key "default_scop"`, "known keys: default_scope, pager, post_install, pre_install, version"),
		Entry("no version", "default_scope: user\n", ":1: missing key version", "add version: 1"),
		Entry("a newer version", "version: 2\n", ":1: version: version 2 is not supported", "upgrade lola"),
		Entry("a version that is not a number", "version: one\n", ":1: version: must be the number 1", "write version: 1"),
		Entry("a list for a scope", "version: 1\ndefault_scope: [user]\n", ":2: default_scope: must be a non-empty value, not a list", "use project, user or both"),
		Entry("a pager that is not a boolean", "version: 1\npager: less\n", ":2: pager: must be true or false, not a value", "write pager: false"),
		Entry("an empty pager", "version: 1\npager:\n", ":2: pager: must be true or false, not empty", "write pager: false"),
		Entry("a list at the top", "- version: 1\n", ":1: must be a mapping, not a list", "key: value"),
		Entry("a key written twice", "version: 1\nversion: 1\n", ":2: version: is written twice", "keep one"),
	)

	// Spec 33's config.yaml rows: what config.yaml refuses as it is read; a script missing, irregular or
	// not executable is refused where a run plans it (the engine's rows).
	DescribeTable("a missing, irregular or non-executable configured script is refused naming file, line and key",
		func(content, msg, hint string) {
			write(content)
			_, err := config.LoadConfig(path, "/home/u")
			Expect(err).To(MatchError(path + msg))
			Expect(errs.Hints(err)).To(HaveExactElements(hint))
		},
		Entry("config.yaml: a relative path", "version: 1\npre_install: bin/pre.sh\n", `:2: pre_install: "bin/pre.sh" is not an absolute path`,
			"write an absolute path, or one that starts with ~/"),
		Entry("config.yaml: no path", "version: 1\npost_install:\n", ":2: post_install: must be a non-empty value, not empty",
			"write an absolute path, or one that starts with ~/"),
		Entry("config.yaml: a list", "version: 1\npost_install: [/a, /b]\n", ":2: post_install: must be a non-empty value, not a list",
			"write an absolute path, or one that starts with ~/"),
	)

	It("refuses a file over 1 MiB without reading it all", func() {
		write("version: 1\n# " + strings.Repeat("x", 1<<20) + "\n")
		_, err := config.LoadConfig(path, "")
		Expect(err).To(MatchError(ContainSubstring("is larger than 1048576 bytes")))
	})

	It("refuses a directory in place of the file", func() {
		Expect(os.Mkdir(path, 0o700)).To(Succeed())
		_, err := config.LoadConfig(path, "")
		Expect(err).To(MatchError(ContainSubstring("is not a regular file")))
	})

	It("refuses a FIFO in place of the file without blocking on it", func() {
		mkfifo(path)
		done := make(chan error, 1)
		go func() { _, err := config.LoadConfig(path, ""); done <- err }()
		Eventually(done, "5s").Should(Receive(MatchError(ContainSubstring("is not a regular file"))))
	})
})

var _ = Describe("selections.yaml", func() {
	var dir, proj, path string
	BeforeEach(func() {
		var err error
		dir, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj = filepath.Join(dir, "src", "api")
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		path = filepath.Join(dir, "selections.yaml")
	})
	write := func(s string) { Expect(os.WriteFile(path, []byte(s), 0o600)).To(Succeed()) }
	load := func() *config.Selections {
		s, err := config.LoadSelections(path)
		Expect(err).NotTo(HaveOccurred())
		return s
	}

	It("has no exclusions when the file is missing", func() {
		Expect(load().Exclusions(config.User(), "review-kit").Excludes("skill", "x")).To(BeFalse())
	})

	It("computes effective exclusions: global skip plus the scope's skip, minus the scope's include", func() {
		write(example(proj))
		s := load()
		user := s.Exclusions(config.User(), "review-kit")
		Expect(user.Excludes("skill", "slow-lint")).To(BeTrue())
		Expect(user.Excludes("mcp", "internal-db")).To(BeFalse(), "user include overrides the global skip")
		Expect(user.Excludes("command", "deploy")).To(BeFalse())

		p := s.Exclusions(config.Project(proj), "review-kit")
		Expect(p.Excludes("skill", "slow-lint")).To(BeFalse())
		Expect(p.Excludes("mcp", "internal-db")).To(BeTrue())
		Expect(p.Excludes("command", "deploy")).To(BeTrue())
		Expect(p.Excludes("skill", "deploy")).To(BeFalse(), "a pattern matches its own kind only")

		other := s.Exclusions(config.Project(filepath.Join(dir, "elsewhere")), "review-kit")
		Expect(other.Excludes("skill", "slow-lint")).To(BeTrue(), "a project without a section gets global")
		Expect(s.Exclusions(config.User(), "other-module").Excludes("skill", "slow-lint")).To(BeFalse())
	})

	It("matches globs", func() {
		write("version: 1\nuser:\n  m:\n    skip: ['skill/review-*', 'agent/[ab]?']\n")
		x := load().Exclusions(config.User(), "m")
		Expect(x.Excludes("skill", "review-go")).To(BeTrue())
		Expect(x.Excludes("skill", "lint")).To(BeFalse())
		Expect(x.Excludes("agent", "ax")).To(BeTrue())
		Expect(x.Excludes("agent", "cx")).To(BeFalse())
	})

	It("keeps a project key it cannot resolve as written", func() {
		file := filepath.Join(dir, "notes.txt")
		Expect(os.WriteFile(file, nil, 0o600)).To(Succeed())
		under := filepath.Join(file, "api")
		write("version: 1\nprojects:\n  " + under + ":\n    m:\n      skip: [skill/x]\n")
		Expect(load().Exclusions(config.Project(under), "m").Excludes("skill", "x")).To(BeTrue())
	})

	It("matches a project key written another way: through a symlink or with .. in it", func() {
		Expect(os.Symlink(filepath.Join(dir, "src"), filepath.Join(dir, "link"))).To(Succeed())
		for _, key := range []string{filepath.Join(dir, "link", "api"), filepath.Join(dir, "src", "x", "..", "api")} {
			write("version: 1\nprojects:\n  " + key + ":\n    m:\n      skip: [rule/r]\n")
			Expect(load().Exclusions(config.Project(proj), "m").Excludes("rule", "r")).To(BeTrue(), key)
		}
	})

	It("accepts empty sections, entries and lists", func() {
		write("version: 1\nglobal:\nuser: {}\nprojects:\n  " + proj + ":\n    m:\n    n:\n      skip: []\n      include:\n")
		Expect(load().Exclusions(config.Project(proj), "m").Excludes("skill", "x")).To(BeFalse())
	})

	DescribeTable("refuses hostile or broken files, naming the file, line and key path with a hint",
		func(content func(proj string) string, msg, hint string) {
			write(content(proj))
			_, err := config.LoadSelections(path)
			Expect(err).To(MatchError(ContainSubstring(path + ":")))
			Expect(err).To(MatchError(ContainSubstring(msg)))
			Expect(errs.Hints(err)).To(ContainElement(ContainSubstring(hint)))
			Expect(err.Error()).NotTo(ContainSubstring("yaml.Node"), "never a Go type name")
		},
		Entry("a relative project key", func(string) string { return "version: 1\nprojects:\n  ../src/api:\n    m: {}\n" },
			`:3: projects["../src/api"]: a project key must be an absolute path`, "absolute path"),
		Entry("a ~ project key", func(string) string { return "version: 1\nprojects:\n  ~/src/api:\n    m: {}\n" },
			"cannot start with ~", "home directory spelled out"),
		Entry("two keys for one project", func(p string) string {
			return "version: 1\nprojects:\n  " + p + ":\n    m: {}\n  " + filepath.Dir(p) + "/./api/:\n    n: {}\n"
		}, "names the same project as", "merge the two entries"),
		Entry("a module written twice", func(string) string {
			return "version: 1\nuser:\n  m:\n    skip: [skill/a]\n  m:\n    skip: [skill/b]\n"
		},
			":5: user.m: is written twice", "merge"),
		Entry("a case-variant module name", func(string) string { return "version: 1\nuser:\n  Review-Kit:\n    skip: [skill/a]\n" },
			`:3: user.Review-Kit: module name "Review-Kit" is invalid`, "as lola list shows it"),
		Entry("a list for a section", func(string) string { return "version: 1\nglobal: [m]\n" },
			":2: global: must be a mapping of module names, not a list", "module-name:"),
		Entry("a list for projects", func(string) string { return "version: 1\nprojects: [x]\n" },
			":2: projects: must be a mapping of project paths, not a list", "/absolute/project/path"),
		Entry("a list for a module entry", func(string) string { return "version: 1\nuser:\n  m: [skill/a]\n" },
			":3: user.m: must be a mapping, not a list", "key: value"),
		Entry("a string for skip", func(string) string { return "version: 1\nuser:\n  m:\n    skip: skill/a\n" },
			":4: user.m.skip: must be a list, not a value", "[skill/slow-lint]"),
		Entry("a mapping in a list", func(string) string { return "version: 1\nuser:\n  m:\n    skip: [{a: b}]\n" },
			"user.m.skip[0]: must be a non-empty value, not a mapping", "write kind/name"),
		Entry("an empty selector", func(string) string { return "version: 1\nuser:\n  m:\n    skip: ['']\n" },
			"user.m.skip[0]: must be a non-empty value", "write kind/name"),
		Entry("an unknown kind", func(string) string { return "version: 1\nuser:\n  m:\n    skip: [hook/a]\n" },
			`user.m.skip[0]: selector "hook/a" has an unknown kind "hook"`, "kind/name"),
		Entry("a bad glob", func(string) string { return "version: 1\nuser:\n  m:\n    skip: ['skill/[a']\n" },
			"is not a valid glob", "kind/name"),
		Entry("a module-qualified selector", func(string) string { return "version: 1\nuser:\n  m:\n    skip: [m:skill/a]\n" },
			"names a module; the module is the key above", "write kind/name"),
		Entry("an escape sequence in a selector", func(string) string { return "version: 1\nuser:\n  m:\n    skip: [\"skill/a\\e[2J\"]\n" },
			`user.m.skip[0]: selector "skill/a?[2J" holds a control or format character`, "kind/name"),
		Entry("an unknown entry key", func(string) string { return "version: 1\nuser:\n  m:\n    exclude: [skill/a]\n" },
			`:4: user.m: unknown key "exclude"`, "known keys: include, skip"),
		Entry("an unknown top-level key", func(string) string { return "version: 1\nproject:\n  m: {}\n" },
			`:2: unknown key "project"`, "known keys: global, projects, user, version"),
		Entry("a merge key", func(string) string {
			return "version: 1\nglobal:\n  base: &b\n    skip: [skill/a]\nuser:\n  m:\n    <<: *b\n"
		}, "anchors, aliases and merge keys (<<) are not supported", "write each entry out in full"),
		Entry("an anchor and alias", func(string) string { return "version: 1\nglobal:\n  m: &x\n    skip: [skill/a]\nuser:\n  m: *x\n" },
			":3: anchors, aliases and merge keys", "in full"),
		Entry("a second document", func(string) string { return "version: 1\n---\nuser: {}\n" },
			"holds more than one YAML document", "remove every ---"),
		Entry("a syntax error", func(string) string { return "version: 1\nuser:\n  m: {skip: [\n" },
			"does not parse", "fix it by hand, or move it aside"),
		Entry("no version", func(string) string { return "user: {}\n" }, ":1: missing key version", "add version: 1"),
		Entry("an include in global", func(string) string { return "version: 1\nglobal:\n  m:\n    include: [skill/a]\n" },
			":4: global.m.include: global only skips", "move it to the user: section or a projects: entry"),
	)

	It("refuses an oversized file", func() {
		write("version: 1\n#" + strings.Repeat("y", 1<<20) + "\n")
		_, err := config.LoadSelections(path)
		Expect(err).To(MatchError(ContainSubstring("is larger than 1048576 bytes")))
	})

	It("shows a hostile key through untrusted.Text", func() {
		write("version: 1\nuser:\n  \"bad\\e[31mname\":\n    skip: [skill/a]\n")
		_, err := config.LoadSelections(path)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).NotTo(ContainSubstring("\x1b"))
	})
})

var _ = Describe("selectors", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("parses bare and module-qualified selectors", func() {
		s, err := config.ParseSelector("skill/review-*")
		Expect(err).NotTo(HaveOccurred())
		Expect(s).To(Equal(config.Selector{Pattern: config.Pattern{Kind: "skill", Name: "review-*"}}))
		Expect(s.Literal()).To(BeFalse())
		s, err = config.ParseSelector("review-kit:mcp/internal-db")
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Module).To(Equal("review-kit"))
		Expect(s.String()).To(Equal("review-kit:mcp/internal-db"))
		Expect(s.Literal()).To(BeTrue())
	})

	DescribeTable("refuses bad selectors with the valid forms",
		func(in, msg, hint string) {
			_, err := config.ParseSelector(in)
			Expect(err).To(MatchError(ContainSubstring(msg)))
			Expect(errs.Hints(err)).To(ContainElement(ContainSubstring(hint)))
		},
		Entry("no kind", "review", `selector "review" has no kind/name`, "kind/name or module:kind/name"),
		Entry("no name", "skill/", "has no kind/name", "kind is one of skill, command, agent, mcp, rule"),
		Entry("an unknown kind", "skills/review", `unknown kind "skills"`, "kinds: skill, command, agent, mcp, rule"),
		Entry("a bad module", "Review:skill/x", `module name "Review" is invalid`, "module:kind/name"),
		Entry("a path in the name", "skill/a/b", "a component name has no /", "kind/name"),
		Entry("a bad glob", "skill/[x", "is not a valid glob", "close every ["),
		Entry("an overlong selector", "skill/"+strings.Repeat("a", 200), "longer than 128 characters", "kind/name"),
		Entry("a control character", "skill/a\x1b[31m", `selector "skill/a?[31m" holds a control or format character`, "printable"),
		Entry("a bidi override", "skill/a\u202eb", `selector "skill/a?b" holds a control or format character`, "printable"),
		Entry("a zero-width space", "skill/a\u200bb", "holds a control or format character", "printable"),
		Entry("a line separator", "skill/a\u2028b", "holds a control or format character", "printable"),
		Entry("a paragraph separator", "skill/a\u2029b", "holds a control or format character", "printable"),
	)
})

var _ = Describe("saving selections", func() {
	var dir, proj, path, lock string
	BeforeEach(func() {
		var err error
		dir, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj = filepath.Join(dir, "src", "api")
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		path = filepath.Join(dir, "config", "lola", "selections.yaml")
		lock = filepath.Join(dir, "state", "lola", "locks", "selections.lock")
	})
	read := func() string {
		b, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		return string(b)
	}
	skip := func(ps ...string) config.Entry {
		var e config.Entry
		for _, p := range ps {
			pat, err := config.ParsePattern(p)
			Expect(err).NotTo(HaveOccurred())
			e.Skip = append(e.Skip, pat)
		}
		return e
	}

	It("creates the file with its header, mode 0600, under a lock other processes wait on", func() {
		unlock, err := config.Lock(lock)
		Expect(err).NotTo(HaveOccurred())
		other := flock.New(lock)
		ok, err := other.TryLock()
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeFalse(), "the lock is held")
		Expect(config.Save(path, []config.Change{{Section: config.Project(proj), Module: "m", Entry: skip("skill/a")}})).To(Succeed())
		unlock()
		ok, err = other.TryLock()
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(other.Unlock()).To(Succeed())

		Expect(read()).To(Equal("# lola selections: components you chose not to install.\n" +
			"# Written by `lola install`; safe to edit by hand; takes effect on the next install or render.\n" +
			"# A scope's section overrides global. Project keys are absolute paths (no ~).\n" +
			"version: 1\nprojects:\n  " + proj + ":\n    m:\n      skip: [skill/a]\n"))
		fi, err := os.Stat(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o600)))
		s, err := config.LoadSelections(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Exclusions(config.Project(proj), "m").Excludes("skill", "a")).To(BeTrue())
	})

	It("replaces only the changed modules' entries, keeping comments, order and edits made since the caller read", func() {
		Expect(os.MkdirAll(filepath.Dir(path), 0o700)).To(Succeed())
		Expect(os.WriteFile(path, []byte(example(proj)), 0o644)).To(Succeed())
		before, err := config.LoadSelections(path)
		Expect(err).NotTo(HaveOccurred())
		// Another process adds a module after this one read the file.
		Expect(os.WriteFile(path, []byte(example(proj)+"  /elsewhere:\n    other:\n      skip: [agent/x]\n"), 0o644)).To(Succeed())
		before.Set(config.User(), "review-kit", skip("command/deploy"))
		Expect(config.Save(path, []config.Change{{Section: config.User(), Module: "review-kit", Entry: before.Entry(config.User(), "review-kit")}})).To(Succeed())
		out := read()
		Expect(out).To(ContainSubstring("# lola selections: components you chose not to install.\n"))
		Expect(out).To(ContainSubstring("  review-kit: # module name as lola list shows it\n    skip: [skill/slow-lint, mcp/internal-db]\n"))
		Expect(out).To(ContainSubstring("user:\n  review-kit:\n    skip: [command/deploy]\n"), "include was dropped, skip added")
		Expect(out).To(ContainSubstring("  /elsewhere:\n    other:\n      skip: [agent/x]\n"), "the other process's edit survives")
		Expect(out).To(MatchRegexp(`(?s)global:.*user:.*projects:`))
	})

	It("finds a hand-written project key in another form and keeps its spelling", func() {
		Expect(os.Symlink(filepath.Join(dir, "src"), filepath.Join(dir, "link"))).To(Succeed())
		linked := filepath.Join(dir, "link", "api")
		Expect(os.MkdirAll(filepath.Dir(path), 0o700)).To(Succeed())
		Expect(os.WriteFile(path, []byte("version: 1\nprojects:\n  "+linked+":  # my api\n    m:\n      skip: [skill/a]\n"), 0o600)).To(Succeed())
		Expect(config.Save(path, []config.Change{{Section: config.Project(proj), Module: "m", Entry: skip("skill/b")}})).To(Succeed())
		Expect(read()).To(Equal("version: 1\nprojects:\n  " + linked + ": # my api\n    m:\n      skip: [skill/b]\n"))
	})

	It("keeps the comments of a file that holds only comments", func() {
		Expect(os.MkdirAll(filepath.Dir(path), 0o700)).To(Succeed())
		Expect(os.WriteFile(path, []byte("# my notes: keep slow-lint off at work\n"), 0o600)).To(Succeed())
		Expect(config.Save(path, []config.Change{{Section: config.User(), Module: "m", Entry: skip("skill/a")}})).To(Succeed())
		Expect(read()).To(Equal("# my notes: keep slow-lint off at work\nversion: 1\nuser:\n  m:\n    skip: [skill/a]\n"))
	})

	It("removes a module whose entry became empty", func() {
		Expect(os.MkdirAll(filepath.Dir(path), 0o700)).To(Succeed())
		Expect(os.WriteFile(path, []byte("version: 1\nuser:\n  m:\n    skip: [skill/a]\n  n:\n    skip: [skill/b]\n"), 0o600)).To(Succeed())
		Expect(config.Save(path, []config.Change{{Section: config.User(), Module: "m"}})).To(Succeed())
		Expect(read()).To(Equal("version: 1\nuser:\n  n:\n    skip: [skill/b]\n"))
	})

	It("writes a symlinked file at its target and keeps the link", func() {
		real := filepath.Join(dir, "dotfiles", "selections.yaml")
		Expect(os.MkdirAll(filepath.Dir(real), 0o700)).To(Succeed())
		Expect(os.WriteFile(real, []byte("version: 1\n"), 0o600)).To(Succeed())
		Expect(os.MkdirAll(filepath.Dir(path), 0o700)).To(Succeed())
		Expect(os.Symlink(real, path)).To(Succeed())
		Expect(config.Save(path, []config.Change{{Section: config.Global(), Module: "m", Entry: skip("rule/r")}})).To(Succeed())
		fi, err := os.Lstat(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode() & os.ModeSymlink).NotTo(BeZero())
		b, err := os.ReadFile(real)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal("version: 1\nglobal:\n  m:\n    skip: [rule/r]\n"))
	})

	It("never overwrites a file that does not parse or does not validate", func() {
		Expect(os.MkdirAll(filepath.Dir(path), 0o700)).To(Succeed())
		for _, bad := range []string{"version: 1\nuser: {m: [\n", "version: 1\nuser:\n  m:\n    skip: [hook/x]\n"} {
			Expect(os.WriteFile(path, []byte(bad), 0o600)).To(Succeed())
			err := config.Save(path, []config.Change{{Section: config.User(), Module: "n", Entry: skip("skill/a")}})
			Expect(err).To(MatchError(ContainSubstring("not saving your selections: " + path)))
			Expect(errs.Hints(err)).To(ContainElement("your picks were not written and nothing was installed"))
			Expect(read()).To(Equal(bad))
		}
	})

	It("shows no raw escape sequence from a symlink's target in its errors", func() {
		Expect(os.MkdirAll(filepath.Dir(path), 0o700)).To(Succeed())
		Expect(os.Symlink(filepath.Join(dir, "gone\x1b[31m", "selections.yaml"), path)).To(Succeed())
		err := config.Save(path, []config.Change{{Section: config.User(), Module: "m", Entry: skip("skill/a")}})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).NotTo(ContainSubstring("\x1b"))
	})

	DescribeTable("keeps the comments of a file whose document is empty",
		func(content string) {
			Expect(os.MkdirAll(filepath.Dir(path), 0o700)).To(Succeed())
			Expect(os.WriteFile(path, []byte(content), 0o600)).To(Succeed())
			Expect(config.Save(path, []config.Change{{Section: config.User(), Module: "m", Entry: skip("skill/a")}})).To(Succeed())
			Expect(read()).To(ContainSubstring("# my notes"))
			s, err := config.LoadSelections(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(s.Exclusions(config.User(), "m").Excludes("skill", "a")).To(BeTrue())
		},
		Entry("a document marker", "# my notes\n---\n"),
		Entry("a tilde", "# my notes\n~\n"),
		Entry("null", "# my notes\nnull\n"),
	)

	It("leaves the file byte-identical when removing an entry that is not there", func() {
		Expect(os.MkdirAll(filepath.Dir(path), 0o700)).To(Succeed())
		content := "version: 1\nuser:\n  n:\n    skip: [skill/b]\n"
		Expect(os.WriteFile(path, []byte(content), 0o600)).To(Succeed())
		Expect(config.Save(path, []config.Change{
			{Section: config.Global(), Module: "m"},
			{Section: config.User(), Module: "m"},
			{Section: config.Project(proj), Module: "m"},
		})).To(Succeed())
		Expect(read()).To(Equal(content))
		s := &config.Selections{}
		s.Set(config.Project(proj), "m", config.Entry{})
		Expect(s.Entry(config.Project(proj), "m").Empty()).To(BeTrue())
	})

	It("drops a project key and a section that a removal leaves empty", func() {
		Expect(os.MkdirAll(filepath.Dir(path), 0o700)).To(Succeed())
		Expect(os.WriteFile(path, []byte("version: 1\nuser:\n  m:\n    skip: [skill/a]\nprojects:\n  "+proj+":\n    m:\n      skip: [skill/a]\n"), 0o600)).To(Succeed())
		Expect(config.Save(path, []config.Change{
			{Section: config.Project(proj), Module: "m"},
			{Section: config.User(), Module: "m"},
		})).To(Succeed())
		Expect(read()).To(Equal("version: 1\n"))
	})

	It("keeps a commented section as an empty mapping when its last module is removed", func() {
		Expect(os.MkdirAll(filepath.Dir(path), 0o700)).To(Succeed())
		Expect(os.WriteFile(path, []byte("version: 1\n# my user skips\nuser:\n  m:\n    skip: [skill/a]\n"), 0o600)).To(Succeed())
		Expect(config.Save(path, []config.Change{{Section: config.User(), Module: "m"}})).To(Succeed())
		Expect(read()).To(Equal("version: 1\n# my user skips\nuser: {}\n"))
	})

	It("leaves a section that was already empty alone when the module to remove is absent", func() {
		Expect(os.MkdirAll(filepath.Dir(path), 0o700)).To(Succeed())
		content := "version: 1\n# my user skips\nuser: {}\n"
		Expect(os.WriteFile(path, []byte(content), 0o600)).To(Succeed())
		Expect(config.Save(path, []config.Change{{Section: config.User(), Module: "m"}})).To(Succeed())
		Expect(read()).To(Equal(content))
	})

	It("coaches a write that fails and leaves the file as it was", func() {
		Expect(os.MkdirAll(filepath.Dir(path), 0o700)).To(Succeed())
		Expect(os.WriteFile(path, []byte("version: 1\n"), 0o600)).To(Succeed())
		Expect(os.Chmod(filepath.Dir(path), 0o500)).To(Succeed())
		DeferCleanup(func() { _ = os.Chmod(filepath.Dir(path), 0o700) })
		err := config.Save(path, []config.Change{{Section: config.User(), Module: "n", Entry: skip("skill/a")}})
		Expect(err).To(MatchError(ContainSubstring("could not write " + path)))
		Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("close any program that holds it open")))
		Expect(read()).To(Equal("version: 1\n"))
	})
})
