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
	"github.com/trevor-vaughan/lola-go/internal/targets"
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
		e = engine.New(p, targets.Builtin())
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

	It("sync removes a component skipped after installation as an orphan, and a hand-edited one is kept and reported", func() {
		_, err := install(claude)
		Expect(err).NotTo(HaveOccurred())
		Expect(exists(".claude/commands/ship.md")).To(BeTrue())

		selections("projects:\n  " + proj + ":\n    example:\n      skip: [command/ship, agent/critic]\n")
		Expect(os.WriteFile(filepath.Join(proj, ".claude/agents/critic.md"), []byte("mine now"), 0o644)).To(Succeed())
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc})
		Expect(err).To(MatchError(ContainSubstring(".claude/agents/critic.md: hand-edited since lola wrote it; deleting it would lose your edits")))
		Expect(exists(".claude/commands/ship.md")).To(BeTrue(), "a refused sync changes nothing")

		Expect(os.Remove(filepath.Join(proj, ".claude/agents/critic.md"))).To(Succeed())
		res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Steps).To(ContainElement(SatisfyAll(HaveField("Action", plan.ActionDelete), HaveField("Path", ".claude/commands/ship.md"))))
		Expect(exists(".claude/commands/ship.md")).To(BeFalse())
		Expect(exists(".claude/skills/review/SKILL.md")).To(BeTrue())
		Expect(res.Modules[0].Skipped).To(Equal([]string{"command/ship", "agent/critic"}))
		rows, _, err := e.Status(ctx, sc, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows[0].Status).To(Equal("ok"))
		Expect(rows[0].Skipped).To(Equal([]string{"command/ship", "agent/critic"}))
	})

	It("uses the user section at user scope and ignores project sections there", func() {
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\nmodules:\n  - {name: example, source: " + src + ", skip: [rule/style]}\n"})
		selections("projects:\n  " + proj + ":\n    example:\n      skip: [command/ship]\n")
		res, err := e.Install(ctx, engine.Scope{Kind: target.User}, proj, []engine.InstallRequest{{Arg: src}}, claude)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Modules[0].Skipped).To(Equal([]string{"rule/style"}), "default's entry picks it")
		Expect(filepath.Join(p.Home, ".claude/commands/ship.md")).To(BeARegularFile())
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "default.yml"))).To(ContainSubstring("skip: [rule/style]"), "the install keeps the entry's picks")
	})

	It("warns about a selector that names no component of the module", func() {
		selections("global:\n  example:\n    skip: [skill/gone, 'agent/x*']\n")
		res, err := install(claude)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElements(
			engine.Notice{Level: engine.LevelWarn, Text: "selections.yaml global: example has no component skill/gone → lola install -C " + proj + " example --global --unskip skill/gone"},
			engine.Notice{Level: engine.LevelWarn, Text: "selections.yaml global: example has no component agent/x* → lola install -C " + proj + " example --global --unskip 'agent/x*'"}))
	})

	It("installs, syncs, lists and checks through a scope's only: list", func() {
		selections("projects:\n  " + proj + ":\n    example:\n      only: [command/ship, 'skill/*']\n")
		res, err := install(claude)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Modules[0].Skipped).To(Equal([]string{"agent/critic", "mcp/lola-gh", "rule/style"}))
		Expect(exists(".claude/commands/ship.md")).To(BeTrue())
		Expect(exists(".claude/skills/review/SKILL.md")).To(BeTrue())
		Expect(exists(".claude/agents/critic.md")).To(BeFalse())

		selections("projects:\n  " + proj + ":\n    example:\n      only: [command/ship]\n")
		sres, err := e.Sync(ctx, engine.SyncRequest{Scope: sc})
		Expect(err).NotTo(HaveOccurred())
		Expect(exists(".claude/skills/review/SKILL.md")).To(BeFalse(), "a component the narrowed list misses is removed")
		want := []string{"skill/review", "agent/critic", "mcp/lola-gh", "rule/style"}
		Expect(sres.Modules[0].Skipped).To(Equal(want))
		rows, _, err := e.Status(ctx, sc, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows[0].Skipped).To(Equal(want))
		rep, err := e.Check(ctx, sc, proj, []string{src}, []string{"claude-code"}, nil, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(rep.Modules[0].Skipped).To(Equal(want))
	})

	It("lets a scope's only: replace the global one, under every skip and the scope's include", func() {
		selections("global:\n  example:\n    only: [skill/review]\n    skip: [agent/critic]\nprojects:\n  " + proj +
			":\n    example:\n      only: [command/ship, agent/critic]\n      include: [rule/style]\n")
		res, err := install(claude)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Modules[0].Skipped).To(Equal([]string{"skill/review", "agent/critic", "mcp/lola-gh"}),
			"the project list replaces global's; the global skip still applies; the include adds rule/style")
		res, err = e.Install(ctx, engine.Scope{Kind: target.User}, proj, []engine.InstallRequest{{Arg: src}}, claude)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Modules[0].Skipped).To(Equal([]string{"agent/critic"}),
			"default's entry, a profile entry, takes the global skip but not global's only: list")
	})

	It("an only: list that matches nothing installs nothing of the module and warns, naming its section", func() {
		selections("projects:\n  " + proj + ":\n    example:\n      only: [skill/gone]\n")
		res, err := install(claude)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Modules[0].Skipped).To(Equal([]string{"skill/review", "command/ship", "agent/critic", "mcp/lola-gh", "rule/style"}))
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
			Text: "selections.yaml projects[" + proj + "]: example has no component skill/gone → lola install -C " + proj + " example --unskip skill/gone"}))
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
		selections("projects:\n  " + proj + ":\n    example:\n      skip: [hook/x]\n")
		_, err := install(claude)
		Expect(err).To(MatchError(ContainSubstring(p.Selections + ":5: projects[\"" + proj + "\"].example.skip[0]")))
		Expect(filepath.Join(proj, ".lola")).NotTo(BeADirectory())
	})

	It("check reports nothing lost for a skipped component", func() {
		writeTree(src, map[string]string{"agents/bare.md": "No description.\n"})
		rep, err := e.Check(ctx, sc, proj, []string{src}, []string{"claude-code"}, nil, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(rep.Modules[0].Findings).To(ContainElement(HaveField("Artifact", "bare")))

		selections("projects:\n  " + proj + ":\n    example:\n      skip: [agent/bare]\n")
		rep, err = e.Check(ctx, sc, proj, []string{src}, []string{"claude-code"}, nil, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(rep.Modules[0].Findings).NotTo(ContainElement(HaveField("Artifact", "bare")))
		Expect(rep.Modules[0].Skipped).To(Equal([]string{"agent/bare"}))

		rep, err = e.Check(ctx, engine.Scope{Kind: target.User}, proj, []string{src}, []string{"claude-code"}, nil, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(rep.Modules[0].Skipped).To(BeEmpty(), "the project's section does not apply at user scope")
	})
})
