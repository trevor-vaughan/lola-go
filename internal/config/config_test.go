package config_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gofrs/flock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// example is the selections.yaml with its project keys home and dir.
func example(home, dir string) string {
	return `# lola selections: components you chose not to install.
version: 1
global:
  review-kit:                       # module name as lola list shows it
    skip: [skill/slow-lint, mcp/internal-db]
projects:
  ` + home + `:
    review-kit:
      include: [mcp/internal-db]    # overrides a global skip in this project
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

	It("reads pager", func() {
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

	It("reads pre_install and post_install, expanding ~/", func() {
		write("version: 1\npre_install: ~/bin/pre.sh\npost_install: /opt/ci/after.sh\n")
		c, err := config.LoadConfig(path, "/home/u")
		Expect(err).NotTo(HaveOccurred())
		Expect(c.PreInstall).To(Equal(config.Script{Path: "/home/u/bin/pre.sh", Line: 2, Key: "pre_install"}))
		Expect(c.PostInstall).To(Equal(config.Script{Path: "/opt/ci/after.sh", Line: 3, Key: "post_install"}))
	})

	It("reads theme: dark or light", func() {
		write("version: 1\ntheme: dark\n")
		c, err := config.LoadConfig(path, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Theme).To(Equal(config.ThemeDark))
		write("version: 1\ntheme: light\n")
		c, err = config.LoadConfig(path, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Theme).To(Equal(config.ThemeLight))
		write("version: 1\n")
		c, err = config.LoadConfig(path, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Theme).To(BeEmpty(), "no theme: lola asks the terminal, then you")
	})

	It("saves theme, creating config.yaml or keeping its comments and other keys", func() {
		Expect(config.SaveTheme(path, "", config.ThemeDark)).To(Succeed())
		Expect(os.ReadFile(path)).To(Equal([]byte("# lola settings. Safe to edit by hand.\nversion: 1\ntheme: dark\n")))

		write("# mine\nversion: 1\ndefault_scope: user # where I work\ntheme: dark\npre_install: ~/bin/pre.sh\n")
		Expect(config.SaveTheme(path, "/home/u", config.ThemeLight)).To(Succeed())
		Expect(os.ReadFile(path)).To(Equal([]byte("# mine\nversion: 1\ndefault_scope: user # where I work\ntheme: light\npre_install: ~/bin/pre.sh\n")))

		write("# mine\nversion: 1\ndefault_scope: user # where I work\n")
		Expect(config.SaveTheme(path, "", config.ThemeDark)).To(Succeed())
		Expect(os.ReadFile(path)).To(Equal([]byte("# mine\nversion: 1\ndefault_scope: user # where I work\ntheme: dark\n")))
	})

	It("never overwrites a config.yaml that does not load", func() {
		write("version: 1\ntheme: blue\n")
		err := config.SaveTheme(path, "", config.ThemeDark)
		Expect(err).To(MatchError(ContainSubstring("not saving your theme: " + path + `:2: theme: "blue" is not a theme`)))
		Expect(errs.Hints(err)).To(ContainElement("fix config.yaml by hand, then rerun; nothing was written to it"))
		Expect(os.ReadFile(path)).To(Equal([]byte("version: 1\ntheme: blue\n")))
	})

	DescribeTable("reads cache_unused_for",
		func(value string, want config.Age) {
			write("version: 1\ncache_unused_for: " + value + "\n")
			c, err := config.LoadConfig(path, "")
			Expect(err).NotTo(HaveOccurred())
			Expect(c.CacheUnusedFor).To(Equal(want))
		},
		Entry("days", "30d", config.Age{N: 30}),
		Entry("weeks", "2w", config.Age{N: 2, Weeks: true}),
		Entry("never", "never", config.Age{Never: true}),
	)

	// Spec 44's config.yaml row; its flag row is the CLI's.
	DescribeTable("an invalid age is refused naming the value",
		func(value, msg string) {
			write("version: 1\n\ncache_unused_for: " + value + "\n")
			_, err := config.LoadConfig(path, "")
			Expect(err).To(MatchError(path + ":3: cache_unused_for: " + msg + "; write a whole number of days or weeks, such as 30d or 2w, or never"))
			Expect(errs.Hints(err)).To(BeEmpty())
		},
		Entry("no unit", "30", `"30" is not an age`),
		Entry("another unit", "3m", `"3m" is not an age`),
		Entry("zero", "0d", `"0d" is not an age`),
		Entry("a sign", "+3d", `"+3d" is not an age`),
		Entry("too large to count", "99999999999999999999d", `"99999999999999999999d" is not an age`),
		Entry("empty", "", "must be an age, not empty"),
		Entry("a list", "[2w]", "must be an age, not a list"),
	)

	DescribeTable("reads cache_fresh_for",
		func(value string, want config.Age) {
			write("version: 1\ncache_fresh_for: " + value + "\n")
			c, err := config.LoadConfig(path, "")
			Expect(err).NotTo(HaveOccurred())
			Expect(c.CacheFreshFor).To(Equal(want))
		},
		Entry("days", "3d", config.Age{N: 3}),
		Entry("weeks", "2w", config.Age{N: 2, Weeks: true}),
		Entry("never", "never", config.Age{Never: true}),
	)

	It("leaves cache_fresh_for unset when config.yaml does not say", func() {
		write("version: 1\n")
		c, err := config.LoadConfig(path, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(c.CacheFreshFor).To(BeZero())
	})

	DescribeTable("an invalid cache_fresh_for is refused naming the value",
		func(value, msg string) {
			write("version: 1\n\ncache_fresh_for: " + value + "\n")
			_, err := config.LoadConfig(path, "")
			Expect(err).To(MatchError(path + ":3: cache_fresh_for: " + msg + "; write a whole number of days or weeks, such as 30d or 2w, or never"))
		},
		Entry("hours", "1h", `"1h" is not an age`),
		Entry("zero", "0d", `"0d" is not an age`),
		Entry("a word", "x", `"x" is not an age`),
		Entry("empty", "", "must be an age, not empty"),
	)

	DescribeTable("refuses invalid files, naming the file, line and key with a hint",
		func(content, msg, hint string) {
			write(content)
			_, err := config.LoadConfig(path, "")
			Expect(err).To(MatchError(ContainSubstring(path + ":")))
			Expect(err).To(MatchError(ContainSubstring(msg)))
			Expect(errs.Hints(err)).To(ContainElement(ContainSubstring(hint)))
		},
		Entry("an unknown scope", "version: 1\ndefault_scope: everywhere\n", `:2: default_scope: "everywhere" is not a scope`, "project, user or both"),
		Entry("an unknown key", "version: 1\ndefault_scop: user\n", `:2: unknown key "default_scop"`, "pre_install, theme, version"),
		Entry("an unknown theme", "version: 1\ntheme: blue\n", `:2: theme: "blue" is not a theme`, "write theme: dark or theme: light, for your terminal's background"),
		Entry("an empty theme", "version: 1\ntheme:\n", ":2: theme: must be a non-empty value, not empty", "write theme: dark or theme: light"),
		Entry("a list for a theme", "version: 1\ntheme: [dark]\n", ":2: theme: must be a non-empty value, not a list", "write theme: dark or theme: light"),
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
	var dir, proj, home, path string
	BeforeEach(func() {
		var err error
		dir, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj = filepath.Join(dir, "src", "api")
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		home, err = paths.ProjectKey(filepath.Join(dir, "src", "home"))
		Expect(err).NotTo(HaveOccurred())
		path = filepath.Join(dir, "selections.yaml")
	})
	write := func(s string) { Expect(os.WriteFile(path, []byte(s), 0o600)).To(Succeed()) }
	load := func() *config.Selections {
		s, err := config.LoadSelections(path)
		Expect(err).NotTo(HaveOccurred())
		return s
	}

	It("has no exclusions when the file is missing", func() {
		Expect(load().Exclusions(config.Project(proj), "review-kit").Excludes("skill", "x")).To(BeFalse())
	})

	It("refuses a selections.yaml user section, naming its line and the profile-entry way out", func() {
		path := filepath.Join(testenv.Isolate(), "selections.yaml")
		Expect(os.WriteFile(path, []byte("version: 1\n# mine\nuser:\n  kit:\n    skip: [skill/a]\n"), 0o600)).To(Succeed())
		_, err := config.LoadSelections(path)
		Expect(err).To(MatchError(path + ":3: user: is no longer used; picks for user-scope modules live in their profile entries"))
		Expect(errs.Hints(err)).To(Equal([]string{"delete the user: section from " + path,
			"then re-apply its picks: lola install <module> --user --skip <kind/name>"}))
	})

	It("computes effective exclusions: global skip plus the scope's skip, minus the scope's include", func() {
		write(example(home, proj))
		s := load()
		h := s.Exclusions(config.Project(home), "review-kit")
		Expect(h.Excludes("skill", "slow-lint")).To(BeTrue())
		Expect(h.Excludes("mcp", "internal-db")).To(BeFalse(), "the project's include overrides the global skip")
		Expect(h.Excludes("command", "deploy")).To(BeFalse())

		p := s.Exclusions(config.Project(proj), "review-kit")
		Expect(p.Excludes("skill", "slow-lint")).To(BeFalse())
		Expect(p.Excludes("mcp", "internal-db")).To(BeTrue())
		Expect(p.Excludes("command", "deploy")).To(BeTrue())
		Expect(p.Excludes("skill", "deploy")).To(BeFalse(), "a pattern matches its own kind only")

		other := s.Exclusions(config.Project(filepath.Join(dir, "elsewhere")), "review-kit")
		Expect(other.Excludes("skill", "slow-lint")).To(BeTrue(), "a project without a section gets global")
		Expect(s.Exclusions(config.Project(home), "other-module").Excludes("skill", "slow-lint")).To(BeFalse())
	})

	It("matches globs", func() {
		write("version: 1\nprojects:\n  " + proj + ":\n    m:\n      skip: ['skill/review-*', 'agent/[ab]?']\n")
		x := load().Exclusions(config.Project(proj), "m")
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
		write("version: 1\nglobal:\nprojects:\n  " + proj + ":\n    m:\n    n:\n      skip: []\n      include:\n")
		Expect(load().Exclusions(config.Project(proj), "m").Excludes("skill", "x")).To(BeFalse())
	})

	pats := func(ss ...string) []config.Pattern {
		GinkgoHelper()
		var out []config.Pattern
		for _, s := range ss {
			p, err := config.ParsePattern(s)
			Expect(err).NotTo(HaveOccurred())
			out = append(out, p)
		}
		return out
	}

	It("refuses a selector holding a comma, saying a selector names one component", func() {
		_, err := config.ParsePattern("skill/a,skill/b")
		Expect(err).To(MatchError(`selector "skill/a,skill/b" holds a comma; a selector names one component, and the flags take comma-separated selectors`))
		Expect(errs.Hints(err)).To(Equal([]string{"list several selectors as separate items in a file, or comma-separated on the command line (--only skill/a,skill/b)", config.SelectorForms}))
	})

	It("reads only: in every section, and only: [] as no list", func() {
		write("version: 1\nglobal:\n  m:\n    only: [skill/a]\nprojects:\n  " + home + ":\n    m:\n      only: ['command/*']\n  " + proj +
			":\n    m:\n      only: [agent/x]\n    n:\n      only: []\n")
		s := load()
		Expect(s.Entry(config.Global(), "m").Only).To(Equal(pats("skill/a")))
		Expect(s.Entry(config.Project(home), "m").Only).To(Equal(pats("command/*")))
		Expect(s.Entry(config.Project(proj), "m").Only).To(Equal(pats("agent/x")))
		Expect(s.Entry(config.Project(proj), "n").Only).To(BeNil())
		Expect(s.Exclusions(config.Project(proj), "n").Excludes("skill", "anything")).To(BeFalse(), "only: [] excludes nothing")
		Expect(config.Entry{Only: pats("skill/a")}.Empty()).To(BeFalse(), "an entry with only an only: list is kept")
	})

	DescribeTable("merges only: by precedence: the scope's list replaces global's, skips and the scope's include apply over it",
		func(content func(proj string) string, at, comp string, excluded bool) {
			write("version: 1\n" + content(proj))
			w := config.Project(proj)
			if at == "other" {
				w = config.Project(home)
			}
			kind, name, _ := strings.Cut(comp, "/")
			Expect(load().Exclusions(w, "m").Excludes(kind, name)).To(Equal(excluded))
		},
		Entry("global only excludes what it misses", func(string) string { return "global:\n  m:\n    only: [skill/a]\n" }, "project", "skill/b", true),
		Entry("global only keeps what it lists", func(string) string { return "global:\n  m:\n    only: [skill/a]\n" }, "project", "skill/a", false),
		Entry("a scope's only replaces global's (no intersection)", func(p string) string {
			return "global:\n  m:\n    only: [skill/a]\nprojects:\n  " + p + ":\n    m:\n      only: [skill/b]\n"
		}, "project", "skill/b", false),
		Entry("what only global's list names is excluded under a scope list", func(p string) string {
			return "global:\n  m:\n    only: [skill/a]\nprojects:\n  " + p + ":\n    m:\n      only: [skill/b]\n"
		}, "project", "skill/a", true),
		Entry("a scope skip beats an only match", func(p string) string {
			return "projects:\n  " + p + ":\n    m:\n      only: [skill/a]\n      skip: [skill/a]\n"
		}, "project", "skill/a", true),
		Entry("a global skip beats a scope only match", func(p string) string {
			return "global:\n  m:\n    skip: [skill/a]\nprojects:\n  " + p + ":\n    m:\n      only: [skill/a]\n"
		}, "project", "skill/a", true),
		Entry("the scope's include beats the only list", func(p string) string {
			return "projects:\n  " + p + ":\n    m:\n      only: [skill/a]\n      include: [skill/b]\n"
		}, "project", "skill/b", false),
		Entry("the scope's include beats global only and global skip together", func(p string) string {
			return "global:\n  m:\n    only: [skill/a]\n    skip: [skill/b]\nprojects:\n  " + p + ":\n    m:\n      include: [skill/b]\n"
		}, "project", "skill/b", false),
		Entry("a project's only does not apply at another project", func(p string) string {
			return "projects:\n  " + p + ":\n    m:\n      only: [skill/a]\n"
		}, "other", "skill/b", false),
		Entry("a project's only applies at that project", func(p string) string {
			return "projects:\n  " + p + ":\n    m:\n      only: [skill/a]\n"
		}, "project", "skill/b", true),
	)

	It("says which only: list is in force, and what that list alone leaves out", func() {
		write("version: 1\nglobal:\n  m:\n    only: [skill/a]\n    skip: [skill/c]\nprojects:\n  " + proj + ":\n    m:\n      only: [skill/b]\n")
		s := load()
		x := s.Exclusions(config.Project(proj), "m")
		list, origin := x.Only()
		Expect(list).To(Equal(pats("skill/b")))
		Expect(origin).To(Equal("selections.yaml projects[" + proj + "]"))
		Expect(x.LeftOutByOnly("skill", "a")).To(BeTrue())
		Expect(x.LeftOutByOnly("skill", "c")).To(BeFalse(), "a skip excludes it")
		Expect(x.LeftOutByOnly("skill", "b")).To(BeFalse(), "it is installed")

		_, origin = s.Exclusions(config.Project(home), "m").Only()
		Expect(origin).To(Equal("selections.yaml global"))
		none := s.Exclusions(config.Project(home), "other")
		list, origin = none.Only()
		Expect(list).To(BeNil())
		Expect(origin).To(BeEmpty())
		Expect(none.LeftOutByOnly("skill", "a")).To(BeFalse(), "no only: list leaves nothing out")
	})

	It("lists every pattern of every layer with its origin", func() {
		write("version: 1\nglobal:\n  m:\n    skip: [skill/a]\n    only: [skill/b]\nprojects:\n  " + proj + ":\n    m:\n      include: [skill/c]\n      only: [skill/d]\n")
		Expect(load().Exclusions(config.Project(proj), "m").Patterns()).To(ConsistOf(
			config.Sourced{Pattern: pats("skill/a")[0], Origin: "selections.yaml global"},
			config.Sourced{Pattern: pats("skill/b")[0], Origin: "selections.yaml global"},
			config.Sourced{Pattern: pats("skill/c")[0], Origin: "selections.yaml projects[" + proj + "]"},
			config.Sourced{Pattern: pats("skill/d")[0], Origin: "selections.yaml projects[" + proj + "]"}))
	})

	It("holds a profile entry's picks for the run, which replace every section's but the global skip", func() {
		write("version: 1\nglobal:\n  kit:\n    skip: [skill/never]\n    only: [skill/g]\n")
		sel := load()
		w := config.Picks("profile:user:go")
		Expect(w.Origin()).To(Equal("profile user:go"))
		only, err := config.ParsePattern("skill/a")
		Expect(err).NotTo(HaveOccurred())
		sel.Set(w, "kit", config.Entry{Only: []config.Pattern{only}})
		x := sel.Exclusions(w, "kit")
		Expect(x.Excludes("skill", "a")).To(BeFalse())
		Expect(x.Excludes("skill", "b")).To(BeTrue(), "the entry's only: list is the one in force")
		Expect(x.Excludes("skill", "never")).To(BeTrue(), "the global skip still applies")
		_, from := x.Only()
		Expect(from).To(Equal("profile user:go"))
		bare := sel.Exclusions(config.Picks("profile:user:other"), "kit")
		Expect(bare.Excludes("skill", "b")).To(BeFalse(), "the global only: does not apply")
		Expect(bare.Excludes("skill", "never")).To(BeTrue())
		Expect(sel.Clone().Entry(w, "kit")).To(Equal(config.Entry{Only: []config.Pattern{only}}))
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
			return "version: 1\nglobal:\n  m:\n    skip: [skill/a]\n  m:\n    skip: [skill/b]\n"
		},
			":5: global.m: is written twice", "merge"),
		Entry("a case-variant module name", func(string) string { return "version: 1\nglobal:\n  Review-Kit:\n    skip: [skill/a]\n" },
			`:3: global.Review-Kit: module name "Review-Kit" is invalid`, "as lola list shows it"),
		Entry("a list for a section", func(string) string { return "version: 1\nglobal: [m]\n" },
			":2: global: must be a mapping of module names, not a list", "module-name:"),
		Entry("a list for projects", func(string) string { return "version: 1\nprojects: [x]\n" },
			":2: projects: must be a mapping of project paths, not a list", "/absolute/project/path"),
		Entry("a list for a module entry", func(string) string { return "version: 1\nglobal:\n  m: [skill/a]\n" },
			":3: global.m: must be a mapping, not a list", "key: value"),
		Entry("a string for skip", func(string) string { return "version: 1\nglobal:\n  m:\n    skip: skill/a\n" },
			":4: global.m.skip: must be a list, not a value", "[skill/slow-lint]"),
		Entry("a mapping in a list", func(string) string { return "version: 1\nglobal:\n  m:\n    skip: [{a: b}]\n" },
			"global.m.skip[0]: must be a non-empty value, not a mapping", "write kind/name"),
		Entry("an empty selector", func(string) string { return "version: 1\nglobal:\n  m:\n    skip: ['']\n" },
			"global.m.skip[0]: must be a non-empty value", "write kind/name"),
		Entry("an unknown kind", func(string) string { return "version: 1\nglobal:\n  m:\n    skip: [hook/a]\n" },
			`global.m.skip[0]: selector "hook/a" has an unknown kind "hook"`, "kind/name"),
		Entry("a bad glob", func(string) string { return "version: 1\nglobal:\n  m:\n    skip: ['skill/[a']\n" },
			"is not a valid glob", "kind/name"),
		Entry("a module-qualified selector", func(string) string { return "version: 1\nglobal:\n  m:\n    skip: [m:skill/a]\n" },
			"names a module; the module is the key above", "write kind/name"),
		Entry("an escape sequence in a selector", func(string) string { return "version: 1\nglobal:\n  m:\n    skip: [\"skill/a\\e[2J\"]\n" },
			`global.m.skip[0]: selector "skill/a?[2J" holds a control or format character`, "kind/name"),
		Entry("an unknown entry key", func(string) string { return "version: 1\nglobal:\n  m:\n    exclude: [skill/a]\n" },
			`:4: global.m: unknown key "exclude"`, "known keys: include, only, skip"),
		Entry("an unknown top-level key", func(string) string { return "version: 1\nproject:\n  m: {}\n" },
			`:2: unknown key "project"`, "known keys: global, projects, version"),
		Entry("a merge key", func(string) string {
			return "version: 1\nglobal:\n  base: &b\n    skip: [skill/a]\n  m:\n    <<: *b\n"
		}, "anchors, aliases and merge keys (<<) are not supported", "write each entry out in full"),
		Entry("an anchor and alias", func(string) string { return "version: 1\nglobal:\n  m: &x\n    skip: [skill/a]\n  n: *x\n" },
			":3: anchors, aliases and merge keys", "in full"),
		Entry("a second document", func(string) string { return "version: 1\n---\nglobal: {}\n" },
			"holds more than one YAML document", "remove every ---"),
		Entry("a syntax error", func(string) string { return "version: 1\nglobal:\n  m: {skip: [\n" },
			"does not parse", "fix it by hand, or move it aside"),
		Entry("no version", func(string) string { return "global: {}\n" }, ":1: missing key version", "add version: 1"),
		Entry("an include in global", func(string) string { return "version: 1\nglobal:\n  m:\n    include: [skill/a]\n" },
			":4: global.m.include: global takes skip: and only:, not include:", "move it to a projects: entry"),
		Entry("an unknown kind in only", func(string) string { return "version: 1\nglobal:\n  m:\n    only: [hook/a]\n" },
			`:4: global.m.only[0]: selector "hook/a" has an unknown kind "hook"`, "kind/name"),
		Entry("a module-qualified selector in a project's only", func(p string) string {
			return "version: 1\nprojects:\n  " + p + ":\n    m:\n      only: [m:skill/a]\n"
		}, "names a module; the module is the key above", "write kind/name"),
		Entry("a string for only", func(string) string { return "version: 1\nglobal:\n  m:\n    only: skill/a\n" },
			":4: global.m.only: must be a list, not a value", "[skill/slow-lint]"),
	)

	It("refuses an oversized file", func() {
		write("version: 1\n#" + strings.Repeat("y", 1<<20) + "\n")
		_, err := config.LoadSelections(path)
		Expect(err).To(MatchError(ContainSubstring("is larger than 1048576 bytes")))
	})

	It("shows a hostile key through untrusted.Text", func() {
		write("version: 1\nglobal:\n  \"bad\\e[31mname\":\n    skip: [skill/a]\n")
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
	var dir, proj, home, path, lock string
	BeforeEach(func() {
		var err error
		dir, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj = filepath.Join(dir, "src", "api")
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		home, err = paths.ProjectKey(filepath.Join(dir, "src", "home"))
		Expect(err).NotTo(HaveOccurred())
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
			"# A scope's section overrides global. Project keys are git:<host>/<path>#<dir> for a clone, else absolute paths (no ~).\n" +
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
		Expect(os.WriteFile(path, []byte(example(home, proj)), 0o644)).To(Succeed())
		before, err := config.LoadSelections(path)
		Expect(err).NotTo(HaveOccurred())
		// Another process adds a module after this one read the file.
		Expect(os.WriteFile(path, []byte(example(home, proj)+"  /elsewhere:\n    other:\n      skip: [agent/x]\n"), 0o644)).To(Succeed())
		before.Set(config.Project(home), "review-kit", skip("command/deploy"))
		Expect(config.Save(path, []config.Change{{Section: config.Project(home), Module: "review-kit", Entry: before.Entry(config.Project(home), "review-kit")}})).To(Succeed())
		out := read()
		Expect(out).To(ContainSubstring("# lola selections: components you chose not to install.\n"))
		Expect(out).To(ContainSubstring("  review-kit: # module name as lola list shows it\n    skip: [skill/slow-lint, mcp/internal-db]\n"))
		Expect(out).To(ContainSubstring("  "+home+":\n    review-kit:\n      skip: [command/deploy]\n"), "include was dropped, skip added")
		Expect(out).To(ContainSubstring("  /elsewhere:\n    other:\n      skip: [agent/x]\n"), "the other process's edit survives")
		Expect(out).To(MatchRegexp(`(?s)global:.*projects:.*` + regexp.QuoteMeta(home) + `:.*` + regexp.QuoteMeta(proj) + `:`))
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
		Expect(config.Save(path, []config.Change{{Section: config.Project(proj), Module: "m", Entry: skip("skill/a")}})).To(Succeed())
		Expect(read()).To(Equal("# my notes: keep slow-lint off at work\nversion: 1\nprojects:\n  " + proj + ":\n    m:\n      skip: [skill/a]\n"))
	})

	It("removes a module whose entry became empty", func() {
		Expect(os.MkdirAll(filepath.Dir(path), 0o700)).To(Succeed())
		Expect(os.WriteFile(path, []byte("version: 1\nprojects:\n  "+proj+":\n    m:\n      skip: [skill/a]\n    n:\n      skip: [skill/b]\n"), 0o600)).To(Succeed())
		Expect(config.Save(path, []config.Change{{Section: config.Project(proj), Module: "m"}})).To(Succeed())
		Expect(read()).To(Equal("version: 1\nprojects:\n  " + proj + ":\n    n:\n      skip: [skill/b]\n"))
	})

	It("refuses a profile entry's picks and the user section, writing nothing", func() {
		Expect(os.MkdirAll(filepath.Dir(path), 0o700)).To(Succeed())
		Expect(os.WriteFile(path, []byte("version: 1\n"), 0o600)).To(Succeed())
		Expect(config.Save(path, []config.Change{{Section: config.Project(proj), Module: "m", Entry: skip("skill/a")},
			{Section: config.Picks("profile:user:go"), Module: "m", Entry: skip("skill/b")}})).
			To(MatchError("not saving your selections: selections.yaml has no profile user:go section"))
		Expect(config.Save(path, []config.Change{{Section: config.User(), Module: "m", Entry: skip("skill/b")}})).
			To(MatchError("not saving your selections: selections.yaml has no user section"))
		Expect(read()).To(Equal("version: 1\n"))
	})

	It("writes only: in every section after skip, and reads it back", func() {
		e := config.Entry{Skip: []config.Pattern{{Kind: "skill", Name: "b"}}, Only: []config.Pattern{{Kind: "skill", Name: "a"}}}
		Expect(config.Save(path, []config.Change{{Section: config.Global(), Module: "m", Entry: e}, {Section: config.Project(home), Module: "m", Entry: e},
			{Section: config.Project(proj), Module: "m", Entry: e}})).To(Succeed())
		Expect(read()).To(HaveSuffix("version: 1\nglobal:\n  m:\n    skip: [skill/b]\n    only: [skill/a]\n" +
			"projects:\n  " + home + ":\n    m:\n      skip: [skill/b]\n      only: [skill/a]\n  " + proj + ":\n    m:\n      skip: [skill/b]\n      only: [skill/a]\n"))
		s, err := config.LoadSelections(path)
		Expect(err).NotTo(HaveOccurred())
		for _, w := range []config.Where{config.Global(), config.Project(home), config.Project(proj)} {
			Expect(s.Entry(w, "m")).To(Equal(e), w.String())
		}
		e.Only = nil
		Expect(config.Save(path, []config.Change{{Section: config.Project(home), Module: "m", Entry: e}})).To(Succeed())
		Expect(read()).To(ContainSubstring("  "+home+":\n    m:\n      skip: [skill/b]\n  "+proj+":"), "an emptied only: list loses its key")
	})

	It("keeps a deep copy of only: in Clone", func() {
		Expect(os.MkdirAll(filepath.Dir(path), 0o700)).To(Succeed())
		Expect(os.WriteFile(path, []byte("version: 1\nprojects:\n  "+proj+":\n    m:\n      only: [skill/a]\n"), 0o600)).To(Succeed())
		s, err := config.LoadSelections(path)
		Expect(err).NotTo(HaveOccurred())
		c := s.Clone()
		c.Entry(config.Project(proj), "m").Only[0] = config.Pattern{Kind: "skill", Name: "changed"}
		Expect(s.Entry(config.Project(proj), "m").Only).To(Equal([]config.Pattern{{Kind: "skill", Name: "a"}}))
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
		for _, bad := range []string{"version: 1\nglobal: {m: [\n", "version: 1\nglobal:\n  m:\n    skip: [hook/x]\n"} {
			Expect(os.WriteFile(path, []byte(bad), 0o600)).To(Succeed())
			err := config.Save(path, []config.Change{{Section: config.Project(proj), Module: "n", Entry: skip("skill/a")}})
			Expect(err).To(MatchError(ContainSubstring("not saving your selections: " + path)))
			Expect(errs.Hints(err)).To(ContainElement("your picks were not written and nothing was installed"))
			Expect(read()).To(Equal(bad))
		}
	})

	It("shows no raw escape sequence from a symlink's target in its errors", func() {
		Expect(os.MkdirAll(filepath.Dir(path), 0o700)).To(Succeed())
		Expect(os.Symlink(filepath.Join(dir, "gone\x1b[31m", "selections.yaml"), path)).To(Succeed())
		err := config.Save(path, []config.Change{{Section: config.Project(proj), Module: "m", Entry: skip("skill/a")}})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).NotTo(ContainSubstring("\x1b"))
	})

	DescribeTable("keeps the comments of a file whose document is empty",
		func(content string) {
			Expect(os.MkdirAll(filepath.Dir(path), 0o700)).To(Succeed())
			Expect(os.WriteFile(path, []byte(content), 0o600)).To(Succeed())
			Expect(config.Save(path, []config.Change{{Section: config.Project(proj), Module: "m", Entry: skip("skill/a")}})).To(Succeed())
			Expect(read()).To(ContainSubstring("# my notes"))
			s, err := config.LoadSelections(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(s.Exclusions(config.Project(proj), "m").Excludes("skill", "a")).To(BeTrue())
		},
		Entry("a document marker", "# my notes\n---\n"),
		Entry("a tilde", "# my notes\n~\n"),
		Entry("null", "# my notes\nnull\n"),
	)

	It("leaves the file byte-identical when removing an entry that is not there", func() {
		Expect(os.MkdirAll(filepath.Dir(path), 0o700)).To(Succeed())
		content := "version: 1\nprojects:\n  " + home + ":\n    n:\n      skip: [skill/b]\n"
		Expect(os.WriteFile(path, []byte(content), 0o600)).To(Succeed())
		Expect(config.Save(path, []config.Change{
			{Section: config.Global(), Module: "m"},
			{Section: config.Project(home), Module: "m"},
			{Section: config.Project(proj), Module: "m"},
		})).To(Succeed())
		Expect(read()).To(Equal(content))
		s := &config.Selections{}
		s.Set(config.Project(proj), "m", config.Entry{})
		Expect(s.Entry(config.Project(proj), "m").Empty()).To(BeTrue())
	})

	It("drops a project key and a section that a removal leaves empty", func() {
		Expect(os.MkdirAll(filepath.Dir(path), 0o700)).To(Succeed())
		Expect(os.WriteFile(path, []byte("version: 1\nglobal:\n  m:\n    skip: [skill/a]\nprojects:\n  "+proj+":\n    m:\n      skip: [skill/a]\n"), 0o600)).To(Succeed())
		Expect(config.Save(path, []config.Change{
			{Section: config.Project(proj), Module: "m"},
			{Section: config.Global(), Module: "m"},
		})).To(Succeed())
		Expect(read()).To(Equal("version: 1\n"))
	})

	It("keeps a commented section as an empty mapping when its last module is removed", func() {
		Expect(os.MkdirAll(filepath.Dir(path), 0o700)).To(Succeed())
		Expect(os.WriteFile(path, []byte("version: 1\n# my global skips\nglobal:\n  m:\n    skip: [skill/a]\n"), 0o600)).To(Succeed())
		Expect(config.Save(path, []config.Change{{Section: config.Global(), Module: "m"}})).To(Succeed())
		Expect(read()).To(Equal("version: 1\n# my global skips\nglobal: {}\n"))
	})

	It("leaves a section that was already empty alone when the module to remove is absent", func() {
		Expect(os.MkdirAll(filepath.Dir(path), 0o700)).To(Succeed())
		content := "version: 1\n# my global skips\nglobal: {}\n"
		Expect(os.WriteFile(path, []byte(content), 0o600)).To(Succeed())
		Expect(config.Save(path, []config.Change{{Section: config.Global(), Module: "m"}})).To(Succeed())
		Expect(read()).To(Equal(content))
	})

	It("coaches a write that fails and leaves the file as it was", func() {
		Expect(os.MkdirAll(filepath.Dir(path), 0o700)).To(Succeed())
		Expect(os.WriteFile(path, []byte("version: 1\n"), 0o600)).To(Succeed())
		Expect(os.Chmod(filepath.Dir(path), 0o500)).To(Succeed())
		DeferCleanup(func() { _ = os.Chmod(filepath.Dir(path), 0o700) })
		err := config.Save(path, []config.Change{{Section: config.Project(proj), Module: "n", Entry: skip("skill/a")}})
		Expect(err).To(MatchError(ContainSubstring("could not write " + path)))
		Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("close any program that holds it open")))
		Expect(read()).To(Equal("version: 1\n"))
	})
})
