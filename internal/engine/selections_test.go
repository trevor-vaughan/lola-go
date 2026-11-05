package engine_test

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("the component resolver and selections.yaml", func() {
	var (
		tmp, proj, src string
		p              paths.Paths
		e              *engine.Engine
		sc             engine.Scope
		ctx            = context.Background()
		claude         = engine.Options{Targets: []string{"claude-code"}}
	)
	selections := func(yaml string) {
		Expect(os.MkdirAll(filepath.Dir(p.Selections), 0o700)).To(Succeed())
		Expect(os.WriteFile(p.Selections, []byte("version: 1\n"+yaml), 0o600)).To(Succeed())
	}
	install := func(opts engine.Options) (*engine.Result, error) {
		return e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: src}}, opts)
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
		src = filepath.Join(tmp, "src", "example")
		writeTree(src, exampleModule)
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		sc = engine.Scope{Kind: target.Project, ProjectDir: proj}
	})

	It("installs the cached module minus the effective exclusions for the scope", func() {
		selections("global:\n  example:\n    skip: [command/ship, skill/review]\nprojects:\n  " + proj + ":\n    example:\n      include: [skill/review]\n      skip: ['mcp/*']\n")
		res, err := install(claude)
		Expect(err).NotTo(HaveOccurred())
		Expect(exists(".claude/commands/ship.md")).To(BeFalse())
		Expect(exists(".claude/skills/review/SKILL.md")).To(BeTrue(), "the project's include overrides the global skip")
		Expect(exists(".mcp.json")).To(BeFalse())
		Expect(exists(".claude/agents/critic.md")).To(BeTrue())
		Expect(res.Modules[0].Skipped).To(Equal([]string{"command/ship", "mcp/lola-gh"}))
		Expect(res.Modules[0].MCPServers).To(BeEmpty(), "a skipped server is not reported as running")
	})

	It("render removes a component skipped after installation as an orphan, and a hand-edited one is kept and reported", func() {
		_, err := install(claude)
		Expect(err).NotTo(HaveOccurred())
		Expect(exists(".claude/commands/ship.md")).To(BeTrue())

		selections("projects:\n  " + proj + ":\n    example:\n      skip: [command/ship, agent/critic]\n")
		Expect(os.WriteFile(filepath.Join(proj, ".claude/agents/critic.md"), []byte("mine now"), 0o644)).To(Succeed())
		_, err = e.Render(ctx, sc, engine.Options{})
		Expect(err).To(MatchError(ContainSubstring(".claude/agents/critic.md: hand-edited since lola wrote it; kept")))
		Expect(exists(".claude/commands/ship.md")).To(BeTrue(), "a refused render changes nothing")

		Expect(os.Remove(filepath.Join(proj, ".claude/agents/critic.md"))).To(Succeed())
		res, err := e.Render(ctx, sc, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Steps).To(ContainElement(SatisfyAll(HaveField("Action", plan.ActionDelete), HaveField("Path", ".claude/commands/ship.md"))))
		Expect(exists(".claude/commands/ship.md")).To(BeFalse())
		Expect(exists(".claude/skills/review/SKILL.md")).To(BeTrue())
		Expect(res.Modules[0].Skipped).To(Equal([]string{"command/ship", "agent/critic"}))
		rows, err := e.Status(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows[0].Status).To(Equal("ok"))
		Expect(rows[0].Skipped).To(Equal([]string{"command/ship", "agent/critic"}))
	})

	It("uses the user section at user scope and ignores project sections there", func() {
		selections("user:\n  example:\n    skip: [rule/style]\nprojects:\n  " + proj + ":\n    example:\n      skip: [command/ship]\n")
		res, err := e.Install(ctx, engine.Scope{Kind: target.User}, proj, []engine.InstallRequest{{Arg: src}}, claude)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Modules[0].Skipped).To(Equal([]string{"rule/style"}))
		Expect(filepath.Join(p.Home, ".claude/commands/ship.md")).To(BeARegularFile())
	})

	It("warns about a selector that names no component of the module", func() {
		selections("global:\n  example:\n    skip: [skill/gone, 'agent/x*']\n")
		res, err := install(claude)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElements(
			engine.Notice{Level: engine.LevelWarn, Text: "selections.yaml: example has no component skill/gone; remove it from the file"},
			engine.Notice{Level: engine.LevelWarn, Text: "selections.yaml: example has no component agent/x*; remove it from the file"}))
	})

	It("--no-rules wins over a rule the selections include", func() {
		selections("projects:\n  " + proj + ":\n    example:\n      include: [rule/style]\n")
		off := false
		res, err := install(engine.Options{Targets: []string{"claude-code"}, Rules: &off})
		Expect(err).NotTo(HaveOccurred())
		Expect(exists(".claude/rules/lola/example/style.md")).To(BeFalse())
		Expect(res.Modules[0].Skipped).To(BeEmpty())
	})

	It("refuses an invalid selections.yaml before writing anything", func() {
		selections("user:\n  example:\n    skip: [hook/x]\n")
		_, err := install(claude)
		Expect(err).To(MatchError(ContainSubstring(p.Selections + ":4: user.example.skip[0]")))
		Expect(filepath.Join(proj, ".lola")).NotTo(BeADirectory())
	})

	It("check reports nothing lost for a skipped component", func() {
		writeTree(src, map[string]string{"agents/bare.md": "No description.\n"})
		rep, err := e.Check(ctx, sc, proj, []string{src}, []string{"claude-code"})
		Expect(err).NotTo(HaveOccurred())
		Expect(rep.Modules[0].Findings).To(ContainElement(HaveField("Artifact", "bare")))

		selections("projects:\n  " + proj + ":\n    example:\n      skip: [agent/bare]\n")
		rep, err = e.Check(ctx, sc, proj, []string{src}, []string{"claude-code"})
		Expect(err).NotTo(HaveOccurred())
		Expect(rep.Modules[0].Findings).NotTo(ContainElement(HaveField("Artifact", "bare")))
		Expect(rep.Modules[0].Skipped).To(Equal([]string{"agent/bare"}))

		rep, err = e.Check(ctx, engine.Scope{Kind: target.User}, proj, []string{src}, []string{"claude-code"})
		Expect(err).NotTo(HaveOccurred())
		Expect(rep.Modules[0].Skipped).To(BeEmpty(), "the project's section does not apply at user scope")
	})
})
