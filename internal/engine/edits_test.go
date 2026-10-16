package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("selection edits (Plan 5c §5.2, §7)", func() {
	var (
		tmp, proj, src, other string
		p                     paths.Paths
		e                     *engine.Engine
		ctx                   = context.Background()
	)
	project := func() engine.ScopeInstall {
		return engine.ScopeInstall{ScopeChoice: engine.ScopeChoice{Scope: engine.Scope{Kind: target.Project, ProjectDir: proj}, Source: engine.SourceFlag},
			Targets: []string{"claude-code"}}
	}
	user := func() engine.ScopeInstall {
		return engine.ScopeInstall{ScopeChoice: engine.ScopeChoice{Scope: engine.Scope{Kind: target.User}, Source: engine.SourceFlag},
			Targets: []string{"claude-code"}}
	}
	edit := func(op, sel string) engine.SelectionEdit {
		s, err := config.ParseSelector(sel)
		Expect(err).NotTo(HaveOccurred())
		return engine.SelectionEdit{Op: op, Selector: s}
	}
	install := func(opts engine.Options, global []engine.SelectionEdit, scoped []engine.SelectionEdit, mods ...string) (*engine.InstallResult, error) {
		si := project()
		si.Edits = scoped
		spec := engine.InstallSpec{Cwd: proj, Scopes: []engine.ScopeInstall{si}, GlobalEdits: global, Options: opts}
		for _, m := range mods {
			spec.Modules = append(spec.Modules, engine.InstallRequest{Arg: m})
		}
		return e.InstallAll(ctx, spec)
	}
	saved := func() string {
		b, err := os.ReadFile(p.Selections)
		if errors.Is(err, os.ErrNotExist) {
			return ""
		}
		Expect(err).NotTo(HaveOccurred())
		return string(b)
	}
	exists := func(rel string) bool {
		_, err := os.Stat(filepath.Join(proj, rel))
		return err == nil
	}

	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		proj = filepath.Join(tmp, "proj")
		src, other = filepath.Join(tmp, "src", "example"), filepath.Join(tmp, "src", "other")
		writeTree(src, exampleModule)
		writeTree(other, map[string]string{"lola.yml": "name: other\n", "commands/ship.md": "Other ship\n", "commands/lint.md": "Lint\n"})
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
	})

	It("--skip saves a bare selector for every chosen module with a match, in the scope's section", func() {
		res, err := install(engine.Options{}, nil, []engine.SelectionEdit{edit("skip", "command/ship")}, src)
		Expect(err).NotTo(HaveOccurred())
		Expect(exists(".claude/commands/ship.md")).To(BeFalse())
		Expect(res.Modules[0].Skipped).To(Equal([]string{"command/ship"}))
		Expect(res.Modules[0].SavedTo).To(Equal(p.Selections))
		Expect(saved()).To(HaveSuffix("version: 1\nprojects:\n  " + proj + ":\n    example:\n      skip: [command/ship]\n"))
	})

	It("--skip removes a matching include in the written section", func() {
		Expect(os.MkdirAll(filepath.Dir(p.Selections), 0o700)).To(Succeed())
		Expect(os.WriteFile(p.Selections, []byte("version: 1\nglobal:\n  example:\n    skip: [skill/review]\nprojects:\n  "+proj+":\n    example:\n      include: [skill/review]\n"), 0o600)).To(Succeed())
		_, err := install(engine.Options{}, nil, []engine.SelectionEdit{edit("skip", "example:skill/review")}, src)
		Expect(err).NotTo(HaveOccurred())
		Expect(saved()).To(ContainSubstring("    example:\n      skip: [skill/review]\n"))
		Expect(saved()).NotTo(ContainSubstring("include"))
		Expect(exists(".claude/skills/review/SKILL.md")).To(BeFalse())
	})

	It("--only excludes what does not match, as the components that exist now, and leaves a module without a match alone", func() {
		_, err := install(engine.Options{}, nil, []engine.SelectionEdit{edit("only", "skill/*"), edit("only", "agent/critic")}, src, other)
		Expect(err).NotTo(HaveOccurred())
		Expect(saved()).To(ContainSubstring("    example:\n      skip: [command/ship, mcp/lola-gh, rule/style]\n"))
		Expect(saved()).NotTo(ContainSubstring("other:"))
		Expect(exists(".claude/skills/review/SKILL.md")).To(BeTrue())
		Expect(exists(".claude/agents/critic.md")).To(BeTrue())
		Expect(exists(".claude/commands/ship.md")).To(BeTrue(), "other's ship command is installed")
		Expect(exists(".claude/commands/lint.md")).To(BeTrue())

		writeTree(src, map[string]string{"commands/new.md": "New\n"})
		_, err = install(engine.Options{}, nil, nil, src)
		Expect(err).NotTo(HaveOccurred())
		Expect(exists(".claude/commands/new.md")).To(BeTrue(), "a component a later version adds is installed")
	})

	It("--unskip removes a scope skip, and overrides a global skip with an include", func() {
		Expect(os.MkdirAll(filepath.Dir(p.Selections), 0o700)).To(Succeed())
		Expect(os.WriteFile(p.Selections, []byte("version: 1\nglobal:\n  example:\n    skip: [rule/style]\nprojects:\n  "+proj+":\n    example:\n      skip: [command/ship]\n"), 0o600)).To(Succeed())
		res, err := install(engine.Options{}, nil, []engine.SelectionEdit{edit("unskip", "command/ship"), edit("unskip", "rule/style")}, src)
		Expect(err).NotTo(HaveOccurred())
		Expect(saved()).To(ContainSubstring("global:\n  example:\n    skip: [rule/style]\n"))
		Expect(saved()).To(ContainSubstring("    example:\n      include: [rule/style]\n"))
		Expect(res.Modules[0].Skipped).To(BeEmpty())
		Expect(exists(".claude/commands/ship.md")).To(BeTrue())
		Expect(exists(".claude/rules/lola/example/style.md")).To(BeTrue())
	})

	It("--unskip --global warns when a scope section still skips the component", func() {
		Expect(os.MkdirAll(filepath.Dir(p.Selections), 0o700)).To(Succeed())
		Expect(os.WriteFile(p.Selections, []byte("version: 1\nglobal:\n  example:\n    skip: [command/ship]\nprojects:\n  "+proj+":\n    example:\n      skip: [command/ship]\n"), 0o600)).To(Succeed())
		res, err := install(engine.Options{}, []engine.SelectionEdit{edit("unskip", "command/ship")}, nil, src)
		Expect(err).NotTo(HaveOccurred())
		Expect(saved()).NotTo(ContainSubstring("global:\n  example"))
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
			Text: "example:command/ship stays skipped in the projects[" + proj + "] section; --unskip without --global removes it there"}))
		Expect(exists(".claude/commands/ship.md")).To(BeFalse())
	})

	It("--unskip --global does not warn when only the global section skipped the component", func() {
		Expect(os.MkdirAll(filepath.Dir(p.Selections), 0o700)).To(Succeed())
		Expect(os.WriteFile(p.Selections, []byte("version: 1\nglobal:\n  example:\n    skip: [command/ship]\n"), 0o600)).To(Succeed())
		res, err := install(engine.Options{}, []engine.SelectionEdit{edit("unskip", "command/ship")}, nil, src)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(BeEmpty())
		Expect(exists(".claude/commands/ship.md")).To(BeTrue())
	})

	It("prints no raw escape from a project path in a notice", func() {
		proj = filepath.Join(tmp, "p\x1b[31m")
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		Expect(os.MkdirAll(filepath.Dir(p.Selections), 0o700)).To(Succeed())
		Expect(os.WriteFile(p.Selections, []byte("version: 1\nglobal:\n  example:\n    skip: [command/ship]\nprojects:\n  \""+strings.ReplaceAll(proj, "\x1b", `\x1b`)+"\":\n    example:\n      skip: [command/ship]\n"), 0o600)).To(Succeed())
		res, err := install(engine.Options{}, []engine.SelectionEdit{edit("unskip", "command/ship")}, nil, src)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(HaveLen(1))
		Expect(res.Notices[0].Text).NotTo(ContainSubstring("\x1b"))
	})

	It("--only warns when a wider include keeps a component it excludes installed", func() {
		Expect(os.MkdirAll(filepath.Dir(p.Selections), 0o700)).To(Succeed())
		Expect(os.WriteFile(p.Selections, []byte("version: 1\nprojects:\n  "+proj+":\n    example:\n      include: [command/*]\n"), 0o600)).To(Succeed())
		res, err := install(engine.Options{}, nil, []engine.SelectionEdit{edit("only", "skill/review")}, src)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(HaveField("Text", ContainSubstring("example:command/ship stays installed: an include in"))))
		Expect(exists(".claude/commands/ship.md")).To(BeTrue())
	})

	DescribeTable("refuses a selector that names nothing, coaching with what it could name",
		func(sel, op, msg, hint string) {
			_, err := install(engine.Options{}, nil, []engine.SelectionEdit{edit(op, sel)}, src, other)
			Expect(err).To(MatchError(msg))
			Expect(errs.Hints(err)).To(ContainElement(hint))
			Expect(saved()).To(BeEmpty())
			Expect(filepath.Join(proj, ".lola")).NotTo(BeADirectory())
		},
		Entry("a bare selector", "command/deploy", "skip", "--skip command/deploy matches no component of the modules being installed",
			"commands here: example:command/ship, other:command/lint, other:command/ship"),
		Entry("a qualified selector", "other:skill/x", "unskip", "--unskip other:skill/x matches no component of the modules being installed",
			"no chosen module has a skill"),
		Entry("an --only glob", "agent/z*", "only", "--only agent/z* matches no component of the modules being installed",
			"agents here: example:agent/critic"),
		Entry("a module not being installed", "gone:skill/x", "skip", "--skip gone:skill/x: module gone is not part of this install",
			"modules in this install: example, other"),
	)

	It("refuses flag edits with more than one scope unless --global", func() {
		spec := engine.InstallSpec{Scopes: []engine.ScopeInstall{project(), user()}}
		err := spec.AddFlagEdits([]engine.SelectionEdit{edit("skip", "skill/a")}, false)
		Expect(err).To(MatchError("--skip skill/a with more than one scope does not say which scope's selections it changes"))
		Expect(errs.Hints(err)).To(ContainElement("add --global to change the selections of every scope"))
		Expect(spec.AddFlagEdits([]engine.SelectionEdit{edit("skip", "skill/a")}, true)).To(Succeed())
		Expect(spec.GlobalEdits).To(HaveLen(1))
	})

	It("saves a picker's answer for both scopes in one run", func() {
		pr, us := project(), user()
		pr.Edits = []engine.SelectionEdit{edit("skip", "example:command/ship")}
		us.Edits = []engine.SelectionEdit{edit("skip", "example:agent/critic")}
		res, err := e.InstallAll(ctx, engine.InstallSpec{Cwd: proj, Scopes: []engine.ScopeInstall{pr, us}, Modules: []engine.InstallRequest{{Arg: src}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(saved()).To(ContainSubstring("user:\n  example:\n    skip: [agent/critic]\n"))
		Expect(saved()).To(ContainSubstring("    example:\n      skip: [command/ship]\n"))
		Expect(res.Modules).To(HaveLen(2))
		Expect(exists(".claude/agents/critic.md")).To(BeTrue())
		Expect(filepath.Join(p.Home, ".claude/agents/critic.md")).NotTo(BeAnExistingFile())
	})

	It("installs into two project roots and the user scope, each destination with its own section and root (Plan 5c §14)", func() {
		proj2 := filepath.Join(tmp, "proj2")
		Expect(os.MkdirAll(proj2, 0o755)).To(Succeed())
		pr, pr2, us := project(), project(), user()
		pr2.Scope.ProjectDir, pr2.Source = proj2, engine.SourcePicked
		pr.Edits = []engine.SelectionEdit{edit("skip", "example:command/ship")}
		pr2.Edits = []engine.SelectionEdit{edit("skip", "example:agent/critic")}
		res, err := e.InstallAll(ctx, engine.InstallSpec{Cwd: proj, Scopes: []engine.ScopeInstall{pr, pr2, us}, Modules: []engine.InstallRequest{{Arg: src}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Scopes).To(Equal([]engine.ScopeResult{{Scope: "project", Root: proj, ScopeSource: "flag"},
			{Scope: "project", Root: proj2, ScopeSource: "picked"}, {Scope: "user", ScopeSource: "flag"}}))
		Expect(res.Modules).To(HaveLen(3))
		Expect([]string{res.Modules[0].Root, res.Modules[1].Root, res.Modules[2].Root}).To(Equal([]string{proj, proj2, ""}))
		js, err := json.Marshal(res)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(js)).To(ContainSubstring(`"scopes":[{"scope":"project","root":"` + proj + `","scope_source":"flag"},` +
			`{"scope":"project","root":"` + proj2 + `","scope_source":"picked"},{"scope":"user","scope_source":"flag"}]`))
		Expect(res.Steps).To(ContainElement(SatisfyAll(HaveField("Root", proj2), HaveField("Path", ".claude/commands/ship.md"))))
		Expect(res.Steps).NotTo(ContainElement(SatisfyAll(HaveField("Root", proj), HaveField("Path", ".claude/commands/ship.md"))))
		Expect(exists(".claude/commands/ship.md")).To(BeFalse())
		Expect(exists(".claude/agents/critic.md")).To(BeTrue())
		Expect(filepath.Join(proj2, ".claude/commands/ship.md")).To(BeARegularFile())
		Expect(filepath.Join(proj2, ".claude/agents/critic.md")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(p.Home, ".claude/commands/ship.md")).To(BeARegularFile())
		Expect(saved()).To(ContainSubstring("  " + proj + ":\n    example:\n      skip: [command/ship]\n"))
		Expect(saved()).To(ContainSubstring("  " + proj2 + ":\n    example:\n      skip: [agent/critic]\n"))
	})

	It("refuses a destination listed twice, however its root is spelled", func() {
		link := filepath.Join(tmp, "link")
		Expect(os.Symlink(proj, link)).To(Succeed())
		again := project()
		again.Scope.ProjectDir = link
		_, err := e.InstallAll(ctx, engine.InstallSpec{Cwd: proj, Scopes: []engine.ScopeInstall{project(), again}, Modules: []engine.InstallRequest{{Arg: src}}})
		Expect(err).To(MatchError("project scope at " + link + " is listed twice"))
		Expect(errs.Hints(err)).To(Equal([]string{"list each destination once"}))
		Expect(filepath.Join(proj, ".lola")).NotTo(BeADirectory())
	})

	Describe("a project rooted at the home directory (Plan 5c §14): it renders where the user scope does", func() {
		homeProject := func() engine.ScopeInstall {
			si := project()
			si.Scope.ProjectDir = p.Home
			return si
		}
		refused := func(err error) {
			Expect(err).To(MatchError("project scope at " + p.Home + " is your home directory, where the user scope installs"))
			Expect(errs.Hints(err)).To(Equal([]string{"install at user scope: lola install " + src + " --user"}))
		}

		It("is refused on its own, and writes nothing", func() {
			_, err := e.InstallAll(ctx, engine.InstallSpec{Cwd: proj, Scopes: []engine.ScopeInstall{homeProject()}, Modules: []engine.InstallRequest{{Arg: src}}})
			refused(err)
			Expect(filepath.Join(p.Home, ".claude")).NotTo(BeADirectory())
			Expect(filepath.Join(p.Home, ".lola")).NotTo(BeADirectory())
			Expect(saved()).To(BeEmpty())

			_, err = e.InstallAll(ctx, engine.InstallSpec{Cwd: proj, Scopes: []engine.ScopeInstall{homeProject(), user()}, Modules: []engine.InstallRequest{{Arg: src}}})
			refused(err)
		})

		It("is refused before or after a user-scope install, so two ledgers never claim one file", func() {
			_, err := e.InstallAll(ctx, engine.InstallSpec{Cwd: proj, Scopes: []engine.ScopeInstall{homeProject()}, Modules: []engine.InstallRequest{{Arg: src}}})
			refused(err)
			_, err = e.InstallAll(ctx, engine.InstallSpec{Cwd: proj, Scopes: []engine.ScopeInstall{user()}, Modules: []engine.InstallRequest{{Arg: src}}})
			Expect(err).NotTo(HaveOccurred())
			_, err = e.InstallAll(ctx, engine.InstallSpec{Cwd: proj, Scopes: []engine.ScopeInstall{homeProject()}, Modules: []engine.InstallRequest{{Arg: src}}})
			refused(err)
			Expect(filepath.Join(p.Home, ".lola", "lock.json")).NotTo(BeAnExistingFile())
			Expect(filepath.Join(p.Home, ".claude/commands/ship.md")).To(BeARegularFile())
		})

		It("that an older lola installed can still be rendered and uninstalled", func() {
			_, err := e.InstallAll(ctx, engine.InstallSpec{Cwd: proj, Scopes: []engine.ScopeInstall{project()}, Modules: []engine.InstallRequest{{Arg: src}}})
			Expect(err).NotTo(HaveOccurred())
			for _, d := range []string{".lola", ".claude"} {
				Expect(os.CopyFS(filepath.Join(p.Home, d), os.DirFS(filepath.Join(proj, d)))).To(Succeed())
			}
			old := engine.Scope{Kind: target.Project, ProjectDir: p.Home}
			Expect(os.Remove(filepath.Join(p.Home, ".claude/commands/ship.md"))).To(Succeed())
			_, err = e.Render(ctx, old, engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(filepath.Join(p.Home, ".claude/commands/ship.md")).To(BeARegularFile())
			_, err = e.Uninstall(ctx, old, p.Home, []string{"example"}, engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(filepath.Join(p.Home, ".claude/commands/ship.md")).NotTo(BeAnExistingFile())
		})
	})

	It("words the flag-edit refusal's hint per destination", func() {
		second := project()
		second.Scope.ProjectDir = filepath.Join(tmp, "proj 2\x1b[2J")
		spec := engine.InstallSpec{Scopes: []engine.ScopeInstall{project(), second, user()}}
		err := spec.AddFlagEdits([]engine.SelectionEdit{edit("skip", "skill/a")}, false)
		Expect(errs.Hints(err)).To(Equal([]string{"add --global to change the selections of every scope",
			"or install one destination at a time: --scope project -C " + proj + ", then --scope project -C '" + filepath.Join(tmp, "proj 2?[2J") + "', then --scope user"}))
	})

	Context("writes selections.yaml only after every check and prompt has passed", func() {
		skipShip := []engine.SelectionEdit{edit("skip", "command/ship")}

		It("--dry-run plans with the edits and writes neither", func() {
			res, err := install(engine.Options{DryRun: true}, nil, skipShip, src)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Modules[0].Skipped).To(Equal([]string{"command/ship"}))
			Expect(res.Modules[0].SavedTo).To(BeEmpty())
			Expect(res.Steps).NotTo(ContainElement(HaveField("Path", ".claude/commands/ship.md")))
			Expect(saved()).To(BeEmpty())
			Expect(filepath.Join(proj, ".lola")).NotTo(BeADirectory())
		})

		It("--strict refuses and writes nothing", func() {
			writeTree(src, map[string]string{"agents/bare.md": "No description.\n"})
			_, err := install(engine.Options{Strict: true}, nil, skipShip, src)
			Expect(err).To(MatchError(ContainSubstring("--strict: 1 finding(s); nothing was changed")))
			Expect(saved()).To(BeEmpty())
		})

		It("a conflict refuses and writes nothing", func() {
			writeTree(proj, map[string]string{".claude/agents/critic.md": "mine"})
			_, err := install(engine.Options{}, nil, skipShip, src)
			Expect(err).To(MatchError(ContainSubstring("refusing to change anything")))
			Expect(saved()).To(BeEmpty())
		})

		It("a cancelled consent prompt writes nothing", func() {
			writeTree(src, map[string]string{"lola.yml": "name: example\nhooks: {pre-install: hooks/pre.sh}\n", "hooks/pre.sh": "#!/bin/sh\n"})
			Expect(os.Chmod(filepath.Join(src, "hooks/pre.sh"), 0o755)).To(Succeed())
			cancel := errors.New("aborted")
			_, err := install(engine.Options{Ask: func(string, []engine.HookScript) (bool, error) { return false, cancel }}, nil, skipShip, src)
			Expect(err).To(MatchError(cancel))
			Expect(saved()).To(BeEmpty())
		})

		It("a failure while applying leaves the selections written and says the next render converges", func() {
			writeTree(src, map[string]string{"lola.yml": "name: example\nhooks: {pre-install: hooks/pre.sh}\n", "hooks/pre.sh": "#!/bin/sh\nexit 3\n"})
			Expect(os.Chmod(filepath.Join(src, "hooks/pre.sh"), 0o755)).To(Succeed())
			_, err := install(engine.Options{AllowHooks: true, HookOutput: GinkgoWriter}, nil, skipShip, src)
			Expect(err).To(HaveOccurred())
			Expect(errs.Hints(err)).To(ContainElement("your selections were saved to " + p.Selections + "; lola render applies them once the problem is fixed"))
			Expect(saved()).To(ContainSubstring("skip: [command/ship]"))
			Expect(exists(".claude/agents/critic.md")).To(BeFalse())
		})
	})
})
