package claude_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/target/claude"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

func doc(src string) module.Doc {
	d, err := module.ParseDoc([]byte(src))
	Expect(err).NotTo(HaveOccurred())
	return d
}

// byPath indexes intents by Path (and Pointer for JSON members).
func byPath(ins []plan.Intent) map[string]plan.Intent {
	out := map[string]plan.Intent{}
	for _, in := range ins {
		out[in.Path+in.Pointer] = in
	}
	return out
}

var _ = Describe("claude-code", func() {
	c := claude.Target{}
	ctx := target.PlanContext{Scope: target.Project, ContentDir: "/store/example/versions/abc/content", PluginRoot: "/proj/.lola/modules/example",
		PluginData: "/proj/.lola/plugin-data/example"}

	It("renders skills as a tree plus an expanded SKILL.md", func() {
		m := &module.Module{Name: "example", Skills: []module.Skill{
			{Name: "review", Dir: "skills/review", Doc: doc("---\ndescription: r\n---\nRun ${PLUGIN_ROOT}/bin/x\n")},
			{Name: "solo", Dir: ".", Exclude: []string{"lola.yml"}, Doc: doc("S\n")},
		}}
		ins, notes, err := c.Plan(m, ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(notes).To(BeEmpty())
		Expect(ins).To(HaveLen(4))
		Expect(ins[0]).To(Equal(plan.Intent{Target: "claude-code", Kind: "skill", Op: plan.OpCopyTree, Src: ctx.ContentDir + "/skills/review",
			Path: ".claude/skills/review", Exclude: []string{"SKILL.md"}}))
		Expect(string(ins[1].Data)).To(Equal("---\ndescription: r\n---\nRun /proj/.lola/modules/example/bin/x\n"))
		Expect(ins[1].Path).To(Equal(".claude/skills/review/SKILL.md"))
		Expect(ins[2].Src).To(Equal(ctx.ContentDir))
		Expect(ins[2].Exclude).To(Equal([]string{"lola.yml", "SKILL.md"}))
	})

	It("a command without frontmatter installs, with its sidecar directory", func() {
		m := &module.Module{Name: "example", Commands: []module.Command{{Name: "go", File: "commands/go.md", SidecarDir: "commands/go", Doc: doc("Run $ARGUMENTS\n")}}}
		ins, _, err := c.Plan(m, ctx)
		Expect(err).NotTo(HaveOccurred())
		p := byPath(ins)
		Expect(string(p[".claude/commands/go.md"].Data)).To(Equal("Run $ARGUMENTS\n"))
		Expect(p[".claude/commands/go"].Op).To(Equal(plan.OpCopyTree))
	})

	It("an author's agent model is kept for claude-code, and a missing name is added", func() {
		m := &module.Module{Name: "example", Agents: []module.Agent{{Name: "rev", Doc: doc("---\ndescription: reviews\nmodel: opus\ntools: [Read]\n---\nYou review.\n")}}}
		ins, _, err := c.Plan(m, ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(ins[0].Data)).To(Equal("---\nname: rev\ndescription: reviews\nmodel: opus\ntools: [Read]\n---\nYou review.\n"))
	})

	It("drops a non-string agent description and skips the agent as missing one", func() {
		m := &module.Module{Name: "example", Agents: []module.Agent{{Name: "num", Doc: doc("---\ndescription: 5\n---\nx\n")}}}
		ins, finds, err := c.Plan(m, ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(ins).To(BeEmpty())
		Expect(finds).To(ConsistOf(
			SatisfyAll(HaveField("Code", "frontmatter-value-dropped"), HaveField("Severity", target.SeverityWarn)),
			SatisfyAll(HaveField("Code", "required-key-missing"), HaveField("Severity", target.SeveritySkip), HaveField("Forceable", true))))
	})

	It("skips an agent without a description, and renders it with Force", func() {
		m := &module.Module{Name: "example", Agents: []module.Agent{{Name: "rev", Doc: doc("body\n")}, {Name: "ok", Doc: doc("---\ndescription: d\n---\nx\n")}}}
		ins, finds, err := c.Plan(m, ctx)
		Expect(err).NotTo(HaveOccurred(), "a content problem is a finding, not an error")
		Expect(ins).To(HaveLen(1))
		Expect(ins[0].Path).To(Equal(".claude/agents/ok.md"))
		Expect(finds).To(Equal([]target.Finding{{Severity: target.SeveritySkip, Target: "claude-code", Kind: "agent", Artifact: "rev",
			Code: "required-key-missing", Text: "claude-code needs a description in the agent's frontmatter",
			Hint: "add description: to the agent's frontmatter", Forceable: true}}))

		forced := ctx
		forced.Force = true
		ins, finds, err = c.Plan(m, forced)
		Expect(err).NotTo(HaveOccurred())
		Expect(ins).To(HaveLen(2))
		Expect(finds[0].Severity).To(Equal(target.SeverityWarn))
		Expect(finds[0].Forced).To(BeTrue())
	})

	It("renders MCP servers in .mcp.json shape with expansion, and ~/.claude.json at user scope", func() {
		m := &module.Module{Name: "example", MCPServers: []module.MCPServer{
			{Name: "gh", Transport: "stdio", Command: "${PLUGIN_ROOT}/bin/gh", Args: []string{"--data", "${PLUGIN_DATA}"}, Env: map[string]string{"T": "1"}, Cwd: "/tmp"},
			{Name: "web", Transport: "http", URL: "https://x/mcp", Headers: map[string]string{"A": "b"}},
			{Name: "ev", Transport: "sse", URL: "https://x/sse"},
		}}
		ins, finds, err := c.Plan(m, ctx)
		Expect(err).NotTo(HaveOccurred())
		p := byPath(ins)
		Expect(p[".mcp.json/mcpServers/gh"].Value).To(MatchJSON(`{"command":"/proj/.lola/modules/example/bin/gh","args":["--data","/proj/.lola/plugin-data/example"],"env":{"T":"1"}}`))
		Expect(p[".mcp.json/mcpServers/web"].Value).To(MatchJSON(`{"type":"http","url":"https://x/mcp","headers":{"A":"b"}}`))
		Expect(p[".mcp.json/mcpServers/ev"].Value).To(MatchJSON(`{"type":"sse","url":"https://x/sse"}`))
		Expect(finds).To(ConsistOf(target.Finding{Severity: target.SeverityWarn, Target: "claude-code", Kind: "mcp", Artifact: "gh",
			Code: "mcp-cwd-dropped", Text: `claude-code has no working-directory setting; cwd "/tmp" dropped`}))

		user := ctx
		user.Scope = target.User
		ins, _, err = c.Plan(m, user)
		Expect(err).NotTo(HaveOccurred())
		Expect(ins[0].Path).To(Equal(".claude.json"))
	})

	It("delivers rules verbatim to .claude/rules/lola/<module>/, at project and user scope", func() {
		m := &module.Module{Name: "example", Rules: []module.Rule{{Name: "style", Body: "Keep ${PLUGIN_ROOT} as written.\n"}}}
		want := []plan.Intent{{Target: "claude-code", Kind: "rule", Op: plan.OpWriteFile, Path: ".claude/rules/lola/example/style.md",
			Data: []byte("Keep ${PLUGIN_ROOT} as written.\n"), Mode: 0o644}}
		ins, notes, err := c.Plan(m, ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(notes).To(BeEmpty())
		Expect(ins).To(Equal(want))
		user := ctx
		user.Scope = target.User
		ins, _, err = c.Plan(m, user)
		Expect(err).NotTo(HaveOccurred())
		Expect(ins).To(Equal(want))
	})

	It("skips artifacts restricted to other targets", func() {
		m := &module.Module{Name: "example", Commands: []module.Command{{Name: "x", Doc: doc("x"), Targets: []string{"opencode"}}}}
		ins, _, err := c.Plan(m, ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(ins).To(BeEmpty())
	})

	It("detects .claude/ or CLAUDE.md at either scope and roots user scope at $HOME", func() {
		dir := testenv.Isolate()
		Expect(c.Detect(target.Project, dir)).To(BeFalse())
		Expect(os.WriteFile(filepath.Join(dir, "CLAUDE.md"), nil, 0o644)).To(Succeed())
		Expect(c.Detect(target.Project, dir)).To(BeTrue())
		home := filepath.Join(dir, "home")
		Expect(c.Detect(target.User, home)).To(BeFalse())
		Expect(os.Mkdir(filepath.Join(home, ".claude"), 0o700)).To(Succeed())
		Expect(c.Detect(target.User, home)).To(BeTrue())
		Expect(c.Root(target.User, "/proj", paths.Paths{Home: "/home/u"})).To(Equal("/home/u"))
		Expect(c.Root(target.Project, "/proj", paths.Paths{Home: "/home/u"})).To(Equal("/proj"))
	})
})
