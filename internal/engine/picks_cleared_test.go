package engine_test

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("Pick-P2-f: uninstall clears the module's picks at that scope", func() {
	var (
		tmp, proj, src string
		p              paths.Paths
		e              *engine.Engine
		sc             engine.Scope
		ctx            = context.Background()
		claude         = engine.Options{Targets: []string{"claude-code"}, Force: true} // --force confirms the files a pick change deletes
	)
	sel := func(s string) config.Selector {
		GinkgoHelper()
		out, err := config.ParseSelector(s)
		Expect(err).NotTo(HaveOccurred())
		return out
	}
	install := func(global bool, edits ...engine.SelectionEdit) {
		GinkgoHelper()
		spec := engine.InstallSpec{Cwd: proj, Options: claude, Modules: []engine.InstallRequest{{Arg: src}},
			Scopes: []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: sc, Source: engine.SourceFlag}, Targets: claude.Targets}}}
		Expect(spec.AddFlagEdits(edits, global)).To(Succeed())
		_, err := e.InstallAll(ctx, spec)
		Expect(err).NotTo(HaveOccurred())
	}
	entry := func(w config.Where) config.Entry {
		GinkgoHelper()
		s, err := config.LoadSelections(p.Selections)
		Expect(err).NotTo(HaveOccurred())
		return s.Entry(w, "example")
	}
	projectWhere := func() config.Where {
		GinkgoHelper()
		key, err := paths.ProjectKey(proj)
		Expect(err).NotTo(HaveOccurred())
		return config.Project(key)
	}

	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		proj, src = filepath.Join(tmp, "proj"), filepath.Join(tmp, "src", "example")
		e.Getwd = func() (string, error) { return proj, nil }
		writeTree(src, exampleModule)
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		sc = engine.Scope{Kind: target.Project, ProjectDir: proj}
	})

	It("removes the project entry, leaves the global section alone and reports what it cleared with the command that restores it", func() {
		install(true, engine.SelectionEdit{Op: engine.EditSkip, Selector: sel("command/ship")})
		install(false, engine.SelectionEdit{Op: engine.EditSkip, Selector: sel("skill/review")})
		Expect(entry(projectWhere()).Skip).NotTo(BeEmpty())

		res, err := e.Uninstall(ctx, sc, []string{"example"}, engine.Options{Display: []string{"--json"}})
		Expect(err).NotTo(HaveOccurred())

		Expect(entry(projectWhere()).Empty()).To(BeTrue(), "the project's entry is gone")
		Expect(entry(config.Global()).Skip).To(HaveLen(1), "the global section is never touched")
		Expect(res.PicksCleared).To(HaveLen(1))
		Expect(res.PicksCleared[0].Module).To(Equal("example"))
		Expect(res.PicksCleared[0].Skip).To(Equal([]string{"skill/review"}))
		Expect(res.PicksCleared[0].Include).To(Equal([]string{}))
		Expect(res.PicksCleared[0].Only).To(Equal([]string{}))
		Expect(res.PicksCleared[0].Restore).To(Equal("lola install --project example --skip skill/review --json"))
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo,
			Text: "example: picks cleared: skill/review (restore: lola install --project example --skip skill/review --json)"}))
	})

	It("reports an empty picks_cleared for a module with no picks of its own, and a later plain install installs everything", func() {
		install(false)
		res, err := e.Uninstall(ctx, sc, []string{"example"}, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.PicksCleared).NotTo(BeNil())
		Expect(res.PicksCleared).To(BeEmpty())
	})

	It("a later plain install installs everything the cleared picks had left out", func() {
		install(false, engine.SelectionEdit{Op: engine.EditSkip, Selector: sel("skill/review")})
		Expect(filepath.Join(proj, ".claude/skills/review/SKILL.md")).NotTo(BeARegularFile())
		_, err := e.Uninstall(ctx, sc, []string{"example"}, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		install(false)
		Expect(filepath.Join(proj, ".claude/skills/review/SKILL.md")).To(BeARegularFile())
	})

	It("restores only: picks with --only --exact, and never gains the -y that answered the uninstall (R-8)", func() {
		install(false, engine.SelectionEdit{Op: engine.EditOnly, Selector: sel("skill/review"), Exact: true})
		res, err := e.Uninstall(ctx, sc, []string{"example"}, engine.Options{Target: engine.TargetRequest{Yes: true}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.PicksCleared[0].Only).To(Equal([]string{"skill/review"}))
		Expect(res.PicksCleared[0].Restore).To(Equal("lola install --project example --only skill/review --exact"))
	})

	It("under -n writes nothing and says what it would clear", func() {
		install(false, engine.SelectionEdit{Op: engine.EditSkip, Selector: sel("skill/review")})
		before, err := os.ReadFile(p.Selections)
		Expect(err).NotTo(HaveOccurred())
		res, err := e.Uninstall(ctx, sc, []string{"example"}, engine.Options{DryRun: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.PicksCleared).To(HaveLen(1))
		Expect(res.Notices).To(ContainElement(HaveField("Text", HavePrefix("example: picks would be cleared: skill/review (restore: "))))
		Expect(os.ReadFile(p.Selections)).To(Equal(before))
	})

	It("keeps the picks while -t leaves the module installed on another target", func() {
		spec := engine.InstallSpec{Cwd: proj, Options: engine.Options{Targets: []string{"claude-code", "opencode"}}, Modules: []engine.InstallRequest{{Arg: src}},
			Scopes: []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: sc, Source: engine.SourceFlag}, Targets: []string{"claude-code", "opencode"}}}}
		Expect(spec.AddFlagEdits([]engine.SelectionEdit{{Op: engine.EditSkip, Selector: sel("skill/review")}}, false)).To(Succeed())
		_, err := e.InstallAll(ctx, spec)
		Expect(err).NotTo(HaveOccurred())
		res, err := e.Uninstall(ctx, sc, []string{"example"}, engine.Options{Targets: []string{"opencode"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.PicksCleared).To(BeEmpty())
		Expect(entry(projectWhere()).Skip).NotTo(BeEmpty())
	})
})
