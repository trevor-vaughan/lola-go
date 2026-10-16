package engine_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("scope resolution (Plan 5c §5.1, Plan 6b §3.1)", func() {
	var (
		tmp, proj, sub, bare string
		p                    paths.Paths
		e                    *engine.Engine
	)
	project := func(dir string) engine.Scope { return engine.Scope{Kind: target.Project, ProjectDir: dir} }
	user := engine.Scope{Kind: target.User}
	config := func(s string) {
		Expect(os.MkdirAll(filepath.Dir(p.Config), 0o700)).To(Succeed())
		Expect(os.WriteFile(p.Config, []byte(s), 0o600)).To(Succeed())
	}
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		proj = filepath.Join(tmp, "proj")
		sub = filepath.Join(proj, "src", "deep")
		bare = filepath.Join(tmp, "bare")
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		Expect(os.MkdirAll(sub, 0o755)).To(Succeed())
		Expect(os.MkdirAll(bare, 0o755)).To(Succeed())
	})

	DescribeTable("resolves, first match wins",
		func(req func() engine.ScopeRequest, cfg string, want func() []engine.ScopeChoice) {
			if cfg != "" {
				config(cfg)
			}
			res, err := e.ResolveScope(req())
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Pick).To(BeFalse())
			Expect(res.Scopes).To(Equal(want()))
		},
		Entry("--scope project uses the found project", func() engine.ScopeRequest { return engine.ScopeRequest{Scope: "project", Cwd: sub} }, "",
			func() []engine.ScopeChoice { return []engine.ScopeChoice{{Scope: project(proj), Source: "flag"}} }),
		Entry("--scope project without a project uses the current directory", func() engine.ScopeRequest { return engine.ScopeRequest{Scope: "project", Cwd: bare} }, "",
			func() []engine.ScopeChoice { return []engine.ScopeChoice{{Scope: project(bare), Source: "flag"}} }),
		Entry("--scope both with -C", func() engine.ScopeRequest {
			return engine.ScopeRequest{Scope: "both", ProjectDir: bare, Cwd: sub, Both: true}
		}, "default_scope: user\nversion: 1\n",
			func() []engine.ScopeChoice {
				return []engine.ScopeChoice{{Scope: project(bare), Source: "flag"}, {Scope: user, Source: "flag"}}
			}),
		Entry("--scope project,user is both", func() engine.ScopeRequest { return engine.ScopeRequest{Scope: "project,user", Cwd: sub, Both: true} }, "",
			func() []engine.ScopeChoice {
				return []engine.ScopeChoice{{Scope: project(proj), Source: "flag"}, {Scope: user, Source: "flag"}}
			}),
		Entry("--user", func() engine.ScopeRequest { return engine.ScopeRequest{User: true, Cwd: sub, Interactive: true} }, "",
			func() []engine.ScopeChoice { return []engine.ScopeChoice{{Scope: user, Source: "flag"}} }),
		Entry("--user agrees with --scope user", func() engine.ScopeRequest { return engine.ScopeRequest{Scope: "user", User: true, Cwd: sub} }, "",
			func() []engine.ScopeChoice { return []engine.ScopeChoice{{Scope: user, Source: "flag"}} }),
		Entry("--project uses the found project, on a terminal too", func() engine.ScopeRequest { return engine.ScopeRequest{Project: true, Cwd: sub, Interactive: true} }, "",
			func() []engine.ScopeChoice { return []engine.ScopeChoice{{Scope: project(proj), Source: "flag"}} }),
		Entry("--project without a project uses the current directory", func() engine.ScopeRequest { return engine.ScopeRequest{Project: true, Cwd: bare} }, "",
			func() []engine.ScopeChoice { return []engine.ScopeChoice{{Scope: project(bare), Source: "flag"}} }),
		Entry("--project with -C", func() engine.ScopeRequest {
			return engine.ScopeRequest{Scope: "project", Project: true, ProjectDir: bare, Cwd: sub}
		}, "",
			func() []engine.ScopeChoice { return []engine.ScopeChoice{{Scope: project(bare), Source: "flag"}} }),
		Entry("-C", func() engine.ScopeRequest { return engine.ScopeRequest{ProjectDir: bare, Cwd: sub, Interactive: true} }, "",
			func() []engine.ScopeChoice { return []engine.ScopeChoice{{Scope: project(bare), Source: "flag"}} }),
		Entry("a project found walking up, over default_scope", func() engine.ScopeRequest { return engine.ScopeRequest{Cwd: sub} }, "version: 1\ndefault_scope: user\n",
			func() []engine.ScopeChoice { return []engine.ScopeChoice{{Scope: project(proj), Source: "discovered"}} }),
		Entry("default_scope", func() engine.ScopeRequest { return engine.ScopeRequest{Cwd: bare, Both: true} }, "version: 1\ndefault_scope: both\n",
			func() []engine.ScopeChoice {
				return []engine.ScopeChoice{{Scope: project(bare), Source: "config"}, {Scope: user, Source: "config"}}
			}),
	)

	DescribeTable("read-only commands: the flags, else the found project and user scope, never asking (Plan 6b §19 R6)",
		func(req func() engine.ScopeRequest, want func() []engine.ScopeChoice) {
			config("version: 1\ndefault_scope: project\n")
			got, err := e.ResolveReadScope(req())
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(want()))
		},
		Entry("a found project and user scope, on a terminal too", func() engine.ScopeRequest { return engine.ScopeRequest{Cwd: sub, Interactive: true} },
			func() []engine.ScopeChoice {
				return []engine.ScopeChoice{{Scope: project(proj), Source: "discovered"}, {Scope: user, Source: "discovered"}}
			}),
		Entry("user scope alone outside a project, whatever default_scope says", func() engine.ScopeRequest { return engine.ScopeRequest{Cwd: bare} },
			func() []engine.ScopeChoice { return []engine.ScopeChoice{{Scope: user, Source: "discovered"}} }),
		Entry("--scope both", func() engine.ScopeRequest { return engine.ScopeRequest{Scope: "both", Cwd: bare, Interactive: true} },
			func() []engine.ScopeChoice {
				return []engine.ScopeChoice{{Scope: project(bare), Source: "flag"}, {Scope: user, Source: "flag"}}
			}),
		Entry("--project", func() engine.ScopeRequest { return engine.ScopeRequest{Project: true, Cwd: sub} },
			func() []engine.ScopeChoice { return []engine.ScopeChoice{{Scope: project(proj), Source: "flag"}} }),
		Entry("--user", func() engine.ScopeRequest { return engine.ScopeRequest{User: true, Cwd: sub} },
			func() []engine.ScopeChoice { return []engine.ScopeChoice{{Scope: user, Source: "flag"}} }),
	)

	It("refuses read-only scope flags that disagree as writing commands do", func() {
		_, err := e.ResolveReadScope(engine.ScopeRequest{Scope: "user", Project: true, Cwd: sub})
		Expect(err).To(MatchError("--project and --scope user disagree"))
	})

	It("leaves the scope to the picker on a terminal, preselecting nothing whatever default_scope or a found project says (Plan 5c §14)", func() {
		config("version: 1\ndefault_scope: user\n")
		res, err := e.ResolveScope(engine.ScopeRequest{Cwd: sub, Interactive: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(res).To(Equal(engine.ScopeResolution{Pick: true, ProjectRoot: proj, Found: true}))

		Expect(os.Remove(p.Config)).To(Succeed())
		res, err = e.ResolveScope(engine.ScopeRequest{Cwd: bare, Interactive: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(res).To(Equal(engine.ScopeResolution{Pick: true, ProjectRoot: bare}))
	})

	It("refuses no scope at all, naming the command and every way to give one", func() {
		_, err := e.ResolveScope(engine.ScopeRequest{Cwd: bare, Command: "install", Both: true})
		Expect(err).To(MatchError("lola install needs a scope: no .lola/ project was found in " + bare + " or above it"))
		Expect(errs.Hints(err)).To(Equal([]string{
			"--project acts on this directory (-C <dir> picks another)",
			"--user acts on your user scope, for every project",
			"--scope both does both",
			"or set a default: default_scope: project in " + p.Config}))

		_, err = e.ResolveScope(engine.ScopeRequest{Cwd: bare, Command: "uninstall"})
		Expect(err).To(MatchError("lola uninstall needs a scope: no .lola/ project was found in " + bare + " or above it"))
		Expect(errs.Hints(err)).NotTo(ContainElement(ContainSubstring("--scope both")), "uninstall acts on one scope")
	})

	It("prints no raw control characters from the working directory in the no-scope error", func() {
		dir := filepath.Join(tmp, "evil\x1b[31mdir")
		Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
		_, err := e.ResolveScope(engine.ScopeRequest{Cwd: dir})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).NotTo(ContainSubstring("\x1b"))
	})

	It("refuses an invalid config.yaml with its key path", func() {
		config("version: 1\ndefault_scope: all\n")
		_, err := e.ResolveScope(engine.ScopeRequest{Cwd: bare})
		Expect(err).To(MatchError(p.Config + `:2: default_scope: "all" is not a scope`))
	})

	It("refuses default_scope: both for a command that acts on one scope", func() {
		config("version: 1\ndefault_scope: both\n")
		_, err := e.ResolveScope(engine.ScopeRequest{Cwd: bare, Command: "render"})
		Expect(err).To(MatchError("lola render acts on one scope at a time, and default_scope: both in " + p.Config + " names two"))
		Expect(errs.Hints(err)).To(Equal([]string{"name one: --project or --user"}))
	})

	DescribeTable("refuses with a hint",
		func(req func() engine.ScopeRequest, msg string, hints ...string) {
			_, err := e.ResolveScope(req())
			Expect(err).To(MatchError(msg))
			for _, h := range hints {
				Expect(errs.Hints(err)).To(ContainElement(ContainSubstring(h)))
			}
		},
		Entry("--scope user -C", func() engine.ScopeRequest { return engine.ScopeRequest{Scope: "user", ProjectDir: bare, Cwd: sub} },
			"--scope user cannot take -C: user scope has no project directory", "drop -C"),
		Entry("--user --scope both", func() engine.ScopeRequest { return engine.ScopeRequest{Scope: "both", User: true, Cwd: sub} },
			"--user and --scope both disagree", "drop --user, or use --scope user"),
		Entry("--scope both for a command that acts on one", func() engine.ScopeRequest {
			return engine.ScopeRequest{Scope: "both", Cwd: sub, Command: "uninstall"}
		}, "lola uninstall acts on one scope at a time, and --scope both names two", "name one: --project or --user"),
		Entry("--user --project", func() engine.ScopeRequest { return engine.ScopeRequest{User: true, Project: true, Cwd: sub} },
			"--user and --project disagree", "name one scope: --project or --user"),
		Entry("--user -C", func() engine.ScopeRequest { return engine.ScopeRequest{User: true, ProjectDir: bare, Cwd: sub} },
			"--user cannot be combined with -C/--root", "user scope has no project directory"),
		Entry("--project --scope user", func() engine.ScopeRequest { return engine.ScopeRequest{Scope: "user", Project: true, Cwd: sub} },
			"--project and --scope user disagree", "drop --project, or use --scope project"),
		Entry("--project --scope both", func() engine.ScopeRequest { return engine.ScopeRequest{Scope: "both", Project: true, Cwd: sub} },
			"--project and --scope both disagree", "drop --project, or use --scope project"),
		Entry("an unknown scope", func() engine.ScopeRequest { return engine.ScopeRequest{Scope: "global", Cwd: sub} },
			`--scope "global" is not a scope`, "--scope project, --scope user or --scope both"),
	)
})
