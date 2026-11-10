package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("exact selections", func() {
	var (
		tmp, proj, src, other, key string
		p                          paths.Paths
		e                          *engine.Engine
		sc                         engine.Scope
		ctx                        = context.Background()
		claude                     = engine.Options{Targets: []string{"claude-code"}}
	)
	selections := func(yaml string) {
		GinkgoHelper()
		Expect(os.MkdirAll(filepath.Dir(p.Selections), 0o700)).To(Succeed())
		Expect(os.WriteFile(p.Selections, []byte("version: 1\n"+yaml), 0o600)).To(Succeed())
	}
	saved := func(w config.Where, module string) config.Entry {
		GinkgoHelper()
		s, err := config.LoadSelections(p.Selections)
		Expect(err).NotTo(HaveOccurred())
		return s.Entry(w, module)
	}
	pats := func(ss ...string) []config.Pattern {
		GinkgoHelper()
		var out []config.Pattern
		for _, s := range ss {
			pt, err := config.ParsePattern(s)
			Expect(err).NotTo(HaveOccurred())
			out = append(out, pt)
		}
		return out
	}
	edit := func(op, s string) engine.SelectionEdit {
		GinkgoHelper()
		sel, err := config.ParseSelector(s)
		Expect(err).NotTo(HaveOccurred())
		return engine.SelectionEdit{Op: op, Selector: sel}
	}
	exact := func(s string) engine.SelectionEdit {
		GinkgoHelper()
		ed := edit(engine.EditOnly, s)
		ed.Exact = true
		return ed
	}
	// installAt installs args at sc with the edits given as flags (--global with global).
	installAt := func(sc engine.Scope, global bool, args []string, edits ...engine.SelectionEdit) (*engine.InstallResult, error) {
		GinkgoHelper()
		spec := engine.InstallSpec{Cwd: proj, Options: claude,
			Scopes: []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: sc, Source: engine.SourceFlag}, Targets: claude.Targets}}}
		for _, a := range args {
			spec.Modules = append(spec.Modules, engine.InstallRequest{Arg: a})
		}
		Expect(spec.AddFlagEdits(edits, global)).To(Succeed())
		return e.InstallAll(ctx, spec)
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
		e = engine.New(p, targets.Builtin())
		proj = filepath.Join(tmp, "proj")
		e.Getwd = func() (string, error) { return proj, nil }
		src, other = filepath.Join(tmp, "src", "example"), filepath.Join(tmp, "src", "zeta")
		writeTree(src, exampleModule)
		writeTree(other, map[string]string{"lola.yml": "name: zeta\n", "commands/z.md": "Z\n"})
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		sc = engine.Scope{Kind: target.Project, ProjectDir: proj}
		key, err = paths.ProjectKey(proj)
		Expect(err).NotTo(HaveOccurred())
	})

	It("saves the --only patterns as only:, replacing a list there, and drops the skips that name a match", func() {
		selections("global:\n  example:\n    skip: [skill/review]\nprojects:\n  " + proj +
			":\n    example:\n      skip: [command/ship, agent/critic]\n      only: [rule/style]\n")
		res, err := installAt(sc, false, []string{src}, exact("command/ship"), exact("skill/*"))
		Expect(err).NotTo(HaveOccurred())
		Expect(saved(config.Project(key), "example")).To(Equal(config.Entry{Skip: pats("agent/critic"), Include: pats("skill/*"),
			Only: pats("command/ship", "skill/*")}), "the old list is replaced; agent/critic's skip stays; the global skip of a listed skill gets an include")
		Expect(res.Modules[0].Skipped).To(Equal([]string{"agent/critic", "mcp/lola-gh", "rule/style"}))
		Expect(exists(".claude/commands/ship.md")).To(BeTrue(), "skipped and listed: installed")
		Expect(exists(".claude/skills/review/SKILL.md")).To(BeTrue())
	})

	It("a stale selector warns once with a runnable hint, and a flag naming it removes it from where it is saved", func() {
		selections("projects:\n  " + proj + ":\n    example:\n      only: [command/ship, agent/planner]\n")
		stale := HaveField("Text", ContainSubstring("has no component agent/planner"))
		res, err := installAt(sc, false, []string{src})
		Expect(err).NotTo(HaveOccurred())
		var warned []engine.Notice
		Expect(res.Notices).To(ContainElement(stale, &warned))
		Expect(warned).To(HaveLen(1))
		Expect(warned[0].Level).To(Equal(engine.LevelWarn))
		Expect(warned[0].Text).To(HaveSuffix("→ lola install --project example --unskip agent/planner"))

		for _, op := range []string{engine.EditSkip, engine.EditUnskip, engine.EditOnly} {
			selections("projects:\n  " + proj + ":\n    example:\n      skip: [agent/planner]\n      only: [command/ship, agent/planner]\n")
			res, err = installAt(sc, false, []string{src}, edit(op, "agent/planner"))
			Expect(err).NotTo(HaveOccurred(), op)
			Expect(saved(config.Project(key), "example")).To(Equal(config.Entry{Only: pats("command/ship")}), op)
			Expect(res.Notices).To(ContainElement(HaveField("Text", "example: removed agent/planner from selections.yaml projects["+key+"]; the module has no such component")), op)
			Expect(res.Notices).NotTo(ContainElement(stale), op)
		}

		_, err = installAt(sc, false, []string{src}, edit(engine.EditSkip, "agent/other"))
		Expect(err).To(MatchError(ContainSubstring("matches no component")), "a selector that is not saved is still refused")
	})

	It("a stale selector given twice is removed, and said, once", func() {
		selections("projects:\n  " + proj + ":\n    example:\n      skip: [agent/planner]\n")
		res, err := installAt(sc, false, []string{src}, edit(engine.EditSkip, "agent/planner"), edit(engine.EditUnskip, "agent/planner"))
		Expect(err).NotTo(HaveOccurred())
		var removed []engine.Notice
		Expect(res.Notices).To(ContainElement(HaveField("Text", ContainSubstring("removed agent/planner from")), &removed))
		Expect(removed).To(HaveLen(1))
		Expect(saved(config.Project(key), "example").Skip).To(BeEmpty())
	})

	It("removing the last entry of an only: list says the module now installs everything, and how to narrow it again", func() {
		selections("projects:\n  " + proj + ":\n    example:\n      only: [agent/planner, command/ship]\n")
		res, err := installAt(sc, false, []string{src}, edit(engine.EditSkip, "agent/planner"))
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).NotTo(ContainElement(HaveField("Text", ContainSubstring("is now empty"))), "a list that keeps an entry still narrows")

		selections("projects:\n  " + proj + ":\n    example:\n      only: [agent/planner]\n")
		res, err = installAt(sc, false, []string{src}, edit(engine.EditSkip, "agent/planner"))
		Expect(err).NotTo(HaveOccurred())
		Expect(saved(config.Project(key), "example").Only).To(BeEmpty())
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
			Text: "example: only: in selections.yaml projects[" + key + "] is now empty, so example installs all its 5 components → lola install --project example --only skill/review"}))
		Expect(exists(".claude/commands/ship.md")).To(BeTrue())
	})

	It("removing a stale selector deletes nothing, so it asks nothing", func() {
		selections("projects:\n  " + proj + ":\n    example:\n      skip: [agent/planner]\n")
		_, err := installAt(sc, false, []string{src})
		Expect(err).NotTo(HaveOccurred())
		Expect(exists(".claude/commands/ship.md")).To(BeTrue())
		res, err := installAt(sc, false, []string{src}, edit(engine.EditUnskip, "agent/planner"))
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Steps).To(BeEmpty())
		Expect(exists(".claude/commands/ship.md")).To(BeTrue())
	})

	It("picks do not narrow runtime hooks, and the run says so", func() {
		plugin := filepath.Join(tmp, "src", "deploy")
		writeTree(plugin, map[string]string{".claude-plugin/plugin.json": `{"name":"deploy"}`, "commands/ship.md": "Ship\n", "commands/other.md": "Other\n",
			"hooks/hooks.json": `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/stop.sh"}]}]}}`})
		notice := engine.Notice{Level: engine.LevelInfo, Text: "deploy: runtime hooks are installed for the whole module; picks do not narrow them"}
		res, err := installAt(sc, false, []string{plugin}, edit(engine.EditOnly, "command/ship"))
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(notice))
		res, err = installAt(sc, false, []string{plugin}, edit(engine.EditUnskip, "command/ship"))
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(notice), "the picks saved earlier still apply")

		selections("projects:\n  " + proj + ":\n    deploy:\n      skip: [agent/planner]\n")
		res, err = installAt(sc, false, []string{plugin})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).NotTo(ContainElement(notice), "a stale selector narrows nothing")

		res, err = installAt(sc, false, []string{other}, edit(engine.EditOnly, "command/z"))
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).NotTo(ContainElement(HaveField("Text", ContainSubstring("runtime hooks"))), "a module without runtime hooks says nothing")
	})

	It("components only: leaves out are named apart from those a skip: leaves out", func() {
		selections("projects:\n  " + proj + ":\n    example:\n      skip: [agent/critic]\n      only: [command/ship, agent/critic]\n")
		res, err := installAt(sc, false, []string{src})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Modules[0].Skipped).To(Equal([]string{"skill/review", "agent/critic", "mcp/lola-gh", "rule/style"}))
		Expect(res.Modules[0].SkippedIn).To(Equal("selections.yaml"), "only agent/critic is skipped by a skip:")
		Expect(res.Modules[0].NotInOnly).To(Equal([]string{"skill/review", "mcp/lola-gh", "rule/style"}))
		Expect(res.Modules[0].OnlyIn).To(Equal("selections.yaml projects[" + key + "]"))
	})

	It("is idempotent", func() {
		_, err := installAt(sc, false, []string{src}, exact("command/ship"))
		Expect(err).NotTo(HaveOccurred())
		before, err := os.ReadFile(p.Selections)
		Expect(err).NotTo(HaveOccurred())
		res, err := installAt(sc, false, []string{src}, exact("command/ship"))
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Modules[0].SavedTo).To(BeEmpty())
		Expect(os.ReadFile(p.Selections)).To(Equal(before))
	})

	It("leaves a module without a match alone", func() {
		_, err := installAt(sc, false, []string{src, other}, exact("skill/review"))
		Expect(err).NotTo(HaveOccurred())
		Expect(saved(config.Project(key), "example").Only).To(Equal(pats("skill/review")))
		Expect(saved(config.Project(key), "zeta").Empty()).To(BeTrue())
		Expect(exists(".claude/commands/z.md")).To(BeTrue())
	})

	It("warns when an include in the section keeps an unlisted component installed", func() {
		selections("projects:\n  " + proj + ":\n    example:\n      include: [agent/critic]\n")
		res, err := installAt(sc, false, []string{src}, exact("command/ship"))
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
			Text: "example:agent/critic stays installed: an include in projects[" + key + "] overrides --only; edit " + p.Selections}))
		Expect(exists(".claude/agents/critic.md")).To(BeTrue())
	})

	It("without --exact keeps an existing only: list and saves skips, as before", func() {
		selections("projects:\n  " + proj + ":\n    example:\n      only: [skill/review, command/ship]\n")
		_, err := installAt(sc, false, []string{src}, edit(engine.EditOnly, "command/ship"))
		Expect(err).NotTo(HaveOccurred())
		got := saved(config.Project(key), "example")
		Expect(got.Only).To(Equal(pats("skill/review", "command/ship")))
		Expect(got.Skip).To(Equal(pats("skill/review", "agent/critic", "mcp/lola-gh", "rule/style")))
	})

	It("--unskip under a scope's only: adds an include there", func() {
		selections("projects:\n  " + proj + ":\n    example:\n      only: [command/ship]\n")
		_, err := installAt(sc, false, []string{src}, edit(engine.EditUnskip, "agent/critic"))
		Expect(err).NotTo(HaveOccurred())
		Expect(saved(config.Project(key), "example")).To(Equal(config.Entry{Include: pats("agent/critic"), Only: pats("command/ship")}))
		Expect(exists(".claude/agents/critic.md")).To(BeTrue())
	})

	It("--unskip --global under the global only: extends it with the matched components' names", func() {
		selections("global:\n  example:\n    only: ['command/s*']\n")
		_, err := installAt(sc, true, []string{src}, edit(engine.EditUnskip, "agent/c*"))
		Expect(err).NotTo(HaveOccurred())
		Expect(saved(config.Global(), "example").Only).To(Equal(pats("command/s*", "agent/critic")), "the literal name, not the glob")
		Expect(exists(".claude/agents/critic.md")).To(BeTrue())
	})

	It("--unskip --global without a global only: list creates none", func() {
		selections("global:\n  example:\n    skip: [agent/critic]\n")
		_, err := installAt(sc, true, []string{src}, edit(engine.EditUnskip, "agent/critic"))
		Expect(err).NotTo(HaveOccurred())
		Expect(saved(config.Global(), "example").Empty()).To(BeTrue())
		Expect(exists(".claude/commands/ship.md")).To(BeTrue(), "everything installs")
	})

	It("--only --global under the global only: list adds the missing component to that list", func() {
		selections("global:\n  example:\n    only: [skill/review]\n")
		_, err := installAt(sc, true, []string{src}, edit(engine.EditOnly, "command/ship"))
		Expect(err).NotTo(HaveOccurred())
		Expect(saved(config.Global(), "example")).To(Equal(config.Entry{Skip: pats("skill/review", "agent/critic", "mcp/lola-gh", "rule/style"),
			Only: pats("skill/review", "command/ship")}))
		Expect(exists(".claude/commands/ship.md")).To(BeTrue())
	})

	It("--global --exact writes the global only: and warns when the scope's only: shadows it", func() {
		selections("projects:\n  " + proj + ":\n    example:\n      only: [skill/review]\n")
		res, err := installAt(sc, true, []string{src}, exact("command/ship"))
		Expect(err).NotTo(HaveOccurred())
		Expect(saved(config.Global(), "example").Only).To(Equal(pats("command/ship")))
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
			Text: "example: only: in selections.yaml projects[" + key + "] shadows the global only: list there; edit " + p.Selections}))
		Expect(exists(".claude/skills/review/SKILL.md")).To(BeTrue(), "the project's list decides here")
		Expect(exists(".claude/commands/ship.md")).To(BeFalse())
	})

	It("--global --exact without a shadowing list warns about nothing", func() {
		res, err := installAt(sc, true, []string{src}, exact("command/ship"))
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).NotTo(ContainElement(HaveField("Text", ContainSubstring("shadows"))))
	})
})

var _ = Describe("new components an only: list leaves out", func() {
	var (
		tmp, proj, src string
		p              paths.Paths
		e              *engine.Engine
		sc             engine.Scope
		ctx            = context.Background()
		claude         = engine.Options{Targets: []string{"claude-code"}}
	)
	selections := func(yaml string) {
		GinkgoHelper()
		Expect(os.MkdirAll(filepath.Dir(p.Selections), 0o700)).To(Succeed())
		Expect(os.WriteFile(p.Selections, []byte("version: 1\n"+yaml), 0o600)).To(Succeed())
	}
	exactShip := func(sc engine.Scope) {
		GinkgoHelper()
		sel, err := config.ParseSelector("command/ship")
		Expect(err).NotTo(HaveOccurred())
		spec := engine.InstallSpec{Cwd: proj, Options: claude, Modules: []engine.InstallRequest{{Arg: src}},
			Scopes: []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: sc, Source: engine.SourceFlag}, Targets: claude.Targets,
				Edits: []engine.SelectionEdit{{Op: engine.EditOnly, Selector: sel, Exact: true}}}}}
		_, err = e.InstallAll(ctx, spec)
		Expect(err).NotTo(HaveOccurred())
	}
	sync := func(sc engine.Scope) *engine.SyncResult {
		GinkgoHelper()
		res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: engine.TrustAll}})
		Expect(err).NotTo(HaveOccurred())
		return res
	}
	adds := func(ns []engine.Notice) []engine.Notice {
		var out []engine.Notice
		for _, n := range ns {
			if strings.HasPrefix(n.Text, "example adds ") || strings.Contains(n.Text, "cannot tell which components are new") {
				out = append(out, n)
			}
		}
		return out
	}
	installedID := func() string {
		GinkgoHelper()
		l, err := ledger.Load(filepath.Join(proj, ".lola", "lock.json"))
		Expect(err).NotTo(HaveOccurred())
		return strings.TrimPrefix(l.Modules["example"].Digest, "sha256:")[:12]
	}

	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p, targets.Builtin())
		proj = filepath.Join(tmp, "proj")
		e.Getwd = func() (string, error) { return proj, nil }
		src = filepath.Join(tmp, "src", "example")
		writeTree(src, exampleModule)
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		sc = engine.Scope{Kind: target.Project, ProjectDir: proj}
	})

	It("names a later version's new component, does not install it, and hints the install that adds it", func() {
		exactShip(sc)
		writeTree(src, map[string]string{"commands/lint.md": "Lint\n"})
		res := sync(sc)
		Expect(adds(res.Notices)).To(Equal([]engine.Notice{{Level: engine.LevelInfo,
			Text: "example adds command/lint, not installed (only: in selections.yaml projects[" + proj + "]) → lola install --project example --unskip command/lint"}}))
		Expect(filepath.Join(proj, ".claude/commands/lint.md")).NotTo(BeAnExistingFile())
	})

	It("installs the new component without --exact, and names nothing", func() {
		sel, err := config.ParseSelector("command/ship")
		Expect(err).NotTo(HaveOccurred())
		spec := engine.InstallSpec{Cwd: proj, Options: claude, Modules: []engine.InstallRequest{{Arg: src}},
			Scopes: []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: sc, Source: engine.SourceFlag}, Targets: claude.Targets,
				Edits: []engine.SelectionEdit{{Op: engine.EditOnly, Selector: sel}}}}}
		_, err = e.InstallAll(ctx, spec)
		Expect(err).NotTo(HaveOccurred())
		writeTree(src, map[string]string{"commands/lint.md": "Lint\n"})
		res := sync(sc)
		Expect(adds(res.Notices)).To(BeEmpty())
		Expect(filepath.Join(proj, ".claude/commands/lint.md")).To(BeARegularFile())
	})

	It("names at most five, then how many more, and hints those five", func() {
		exactShip(sc)
		for _, n := range []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7"} {
			writeTree(src, map[string]string{"commands/" + n + ".md": n + "\n"})
		}
		res := sync(sc)
		Expect(adds(res.Notices)).To(HaveLen(1))
		Expect(adds(res.Notices)[0].Text).To(Equal("example adds command/a1, command/a2, command/a3, command/a4, command/a5 and 2 more, not installed " +
			"(only: in selections.yaml projects[" + proj + "]) → lola install --project example --unskip command/a1 --unskip command/a2 " +
			"--unskip command/a3 --unskip command/a4 --unskip command/a5"))
	})

	It("names nothing a skip or include also decides, nor a rule while rules are off", func() {
		selections("projects:\n  " + proj + ":\n    example:\n      only: [command/ship]\n      skip: [command/lint2]\n      include: [command/lint3]\n")
		off := false
		_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: src}}, engine.Options{Targets: claude.Targets, Rules: &off})
		Expect(err).NotTo(HaveOccurred())
		writeTree(src, map[string]string{"commands/lint.md": "x\n", "commands/lint2.md": "x\n", "commands/lint3.md": "x\n", "rules/new.md": "x\n"})
		res := sync(sc)
		Expect(adds(res.Notices)).To(HaveLen(1))
		Expect(adds(res.Notices)[0].Text).To(HavePrefix("example adds command/lint, not installed"))
	})

	It("names nothing when the installed version is no longer in the cache", func() {
		exactShip(sc)
		Expect(os.RemoveAll(filepath.Join(p.Store, "example", "versions", installedID()))).To(Succeed())
		writeTree(src, map[string]string{"commands/lint.md": "Lint\n"})
		res := sync(sc)
		Expect(adds(res.Notices)).To(BeEmpty())
		Expect(filepath.Join(proj, ".claude/commands/lint.md")).NotTo(BeAnExistingFile(), "still left out")
	})

	It("warns, without failing, when the installed version cannot be read", func() {
		exactShip(sc)
		Expect(os.WriteFile(filepath.Join(p.Store, "example", "versions", installedID(), ".lola-origin"), []byte("digest: sha256:00\n"), 0o644)).To(Succeed())
		writeTree(src, map[string]string{"commands/lint.md": "Lint\n"})
		res := sync(sc)
		Expect(adds(res.Notices)).To(ConsistOf(SatisfyAll(HaveField("Level", engine.LevelWarn),
			HaveField("Text", HavePrefix("example: cannot tell which components are new: ")))))
	})

	It("names nothing, and does not read the installed version, when no only: list is in force", func() {
		_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: src}}, claude)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(p.Store, "example", "versions", installedID(), ".lola-origin"), []byte("digest: sha256:00\n"), 0o644)).To(Succeed())
		writeTree(src, map[string]string{"commands/lint.md": "Lint\n"})
		Expect(adds(sync(sc).Notices)).To(BeEmpty())
	})

	It("names nothing for a version that is the one installed", func() {
		exactShip(sc)
		Expect(adds(sync(sc).Notices)).To(BeEmpty())
	})

	It("comes from install too, with the user scope's flag", func() {
		user := engine.Scope{Kind: target.User}
		exactShip(user)
		writeTree(src, map[string]string{"commands/lint.md": "Lint\n"})
		res, err := e.Install(ctx, user, proj, []engine.InstallRequest{{Arg: src}}, claude)
		Expect(err).NotTo(HaveOccurred())
		Expect(adds(res.Notices)).To(Equal([]engine.Notice{{Level: engine.LevelInfo,
			Text: "example adds command/lint, not installed (only: in default (base)) → lola install --user example --unskip command/lint"}}))
	})

	// A sync's re-pick names the layer that provides the module.
	It("hints, from a sync, the edit of the profile that provides the module, or project.yml's change that asks nothing", func() {
		user := engine.Scope{Kind: target.User}
		switchTo := func(at engine.Scope, name string) *engine.SyncResult {
			GinkgoHelper()
			res, err := e.Sync(ctx, engine.SyncRequest{Scope: at, Profiles: &engine.ProfileChange{Action: engine.ProfileSwitch, Name: name},
				Options: engine.Options{Trust: engine.TrustAll}, ConfirmRemovals: func([]engine.Removal) (bool, error) { return true, nil }})
			Expect(err).NotTo(HaveOccurred())
			return res
		}
		exactShip(user)
		writeTree(src, map[string]string{"commands/lint.md": "Lint\n"})
		Expect(adds(sync(user).Notices)).To(Equal([]engine.Notice{{Level: engine.LevelInfo,
			Text: "example adds command/lint, not installed (only: in default (base)) → lola profile --user default --edit example --unskip command/lint"}}),
			"your default provides it: its edit, not an install")

		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\nmodules:\n  - {name: example, source: " + src + ", only: [command/ship]}\n",
			"example.yml": "version: 1\nmodules:\n  - {name: example, source: " + src + ", only: [command/ship]}\n"})
		switchTo(user, "go")
		writeTree(src, map[string]string{"commands/lint2.md": "Lint\n"})
		Expect(adds(sync(user).Notices)[0].Text).To(HaveSuffix("(only: in profile go) → lola profile --user go --edit example --unskip command/lint2"))
		writeTree(p.UserProfiles, map[string]string{"work.yml": "version: 1\nbundle: [go]\n"})
		switchTo(user, "work")
		writeTree(src, map[string]string{"commands/lint5.md": "Lint\n"})
		Expect(adds(sync(user).Notices)[0].Text).To(HaveSuffix("(only: in profile go) → lola profile --user go --edit example --unskip command/lint5"),
			"under a bundle, the member whose entry wins is the provider")
		switchTo(user, "example")
		writeTree(src, map[string]string{"commands/lint3.md": "Lint\n"})
		Expect(adds(sync(user).Notices)[0].Text).To(HaveSuffix("(only: in profile example) → lola profile --user user:example --edit example --unskip command/lint3"),
			"a profile named as the module is qualified, so the edit reads it as the profile")

		exactShip(sc)
		writeTree(p.UserProfiles, map[string]string{"empty.yml": "version: 1\n",
			"over.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: example, source: " + src + ", only: [command/ship]}\n"})
		switchTo(sc, "empty")
		writeTree(src, map[string]string{"commands/lint4.md": "Lint\n"})
		Expect(adds(sync(sc).Notices)[0].Text).To(HaveSuffix("→ lola profile --project base --edit example --unskip command/lint4"),
			"project.yml provides it under a profile on: an install would ask where the change goes")
		switchTo(sc, "over")
		writeTree(src, map[string]string{"commands/lint6.md": "Lint\n"})
		Expect(adds(switchTo(sc, "base").Notices)[0].Text).To(HaveSuffix("(only: in selections.yaml projects["+proj+"]) → lola install --project example --unskip command/lint6"),
			"the run that turns the profile off hands the module back to project.yml with no profile on")
	})

	It("names, from a sync, a profile with a namesake of another kind qualified in the only: label", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: example, source: " + src + ", only: [command/ship]}\n"})
		writeTree(proj, map[string]string{".lola/profiles/go.yml": "version: 1\n"})
		e.Getwd = func() (string, error) { return tmp, nil }
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileSwitch, Name: "user:go"},
			Options: engine.Options{Targets: claude.Targets, Trust: engine.TrustAll}, ConfirmRemovals: func([]engine.Removal) (bool, error) { return true, nil }})
		Expect(err).NotTo(HaveOccurred())
		writeTree(src, map[string]string{"commands/lint.md": "Lint\n"})
		Expect(adds(sync(sc).Notices)[0].Text).To(HaveSuffix("(only: in profile user:go) → lola profile -C " + proj + " user:go --edit example --unskip command/lint"))
	})

	It("hints -C for a project that is not the one found from the working directory", func() {
		e.Getwd = func() (string, error) { return tmp, nil }
		exactShip(sc)
		writeTree(src, map[string]string{"commands/lint.md": "Lint\n"})
		Expect(adds(sync(sc).Notices)[0].Text).To(HaveSuffix("→ lola install -C " + proj + " example --unskip command/lint"))
	})

	It("shows a hostile project path through untrusted.Text", func() {
		bad := filepath.Join(tmp, "p\x1b[31m")
		Expect(os.MkdirAll(bad, 0o755)).To(Succeed())
		e.Getwd = func() (string, error) { return tmp, nil }
		at := engine.Scope{Kind: target.Project, ProjectDir: bad}
		exactShip(at)
		writeTree(src, map[string]string{"commands/lint.md": "Lint\n"})
		text := adds(sync(at).Notices)[0].Text
		Expect(text).NotTo(ContainSubstring("\x1b"))
		Expect(text).To(ContainSubstring("(only: in selections.yaml projects[" + filepath.Join(tmp, "p?[31m") + "])"))
	})
})
