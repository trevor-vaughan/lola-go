package target_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
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
	c := target.Claude{}
	ctx := target.PlanContext{Scope: target.Project, ContentDir: "/store/example/versions/abc/content", PluginData: "/proj/.lola/plugin-data/example"}

	It("is registered", func() {
		t, ok := target.Lookup("claude-code")
		Expect(ok).To(BeTrue())
		Expect(t.ID()).To(Equal("claude-code"))
		Expect(target.IDs()).To(ContainElement("claude-code"))
	})

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
		Expect(string(ins[1].Data)).To(Equal("---\ndescription: r\n---\nRun /store/example/versions/abc/content/bin/x\n"))
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

	It("requires an agent description", func() {
		m := &module.Module{Name: "example", Agents: []module.Agent{{Name: "rev", Doc: doc("body\n")}}}
		_, _, err := c.Plan(m, ctx)
		Expect(err).To(MatchError(ContainSubstring(`agent "rev": claude-code needs a description`)))
	})

	It("renders MCP servers in .mcp.json shape with expansion, and ~/.claude.json at user scope", func() {
		m := &module.Module{Name: "example", MCPServers: []module.MCPServer{
			{Name: "gh", Transport: "stdio", Command: "${PLUGIN_ROOT}/bin/gh", Args: []string{"--data", "${PLUGIN_DATA}"}, Env: map[string]string{"T": "1"}, Cwd: "/tmp"},
			{Name: "web", Transport: "http", URL: "https://x/mcp", Headers: map[string]string{"A": "b"}},
			{Name: "ev", Transport: "sse", URL: "https://x/sse"},
		}}
		ins, notes, err := c.Plan(m, ctx)
		Expect(err).NotTo(HaveOccurred())
		p := byPath(ins)
		Expect(p[".mcp.json/mcpServers/gh"].Value).To(MatchJSON(`{"command":"/store/example/versions/abc/content/bin/gh","args":["--data","/proj/.lola/plugin-data/example"],"env":{"T":"1"}}`))
		Expect(p[".mcp.json/mcpServers/web"].Value).To(MatchJSON(`{"type":"http","url":"https://x/mcp","headers":{"A":"b"}}`))
		Expect(p[".mcp.json/mcpServers/ev"].Value).To(MatchJSON(`{"type":"sse","url":"https://x/sse"}`))
		Expect(notes).To(ConsistOf(target.Notice{Level: "warn", Text: `MCP server gh: claude-code has no working-directory setting; cwd "/tmp" dropped`}))

		user := ctx
		user.Scope = target.User
		ins, _, err = c.Plan(m, user)
		Expect(err).NotTo(HaveOccurred())
		Expect(ins[0].Path).To(Equal(".claude.json"))
	})

	It("installs instructions at project scope only", func() {
		m := &module.Module{Name: "example", Instructions: "Be kind.\n"}
		ins, _, err := c.Plan(m, ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(ins).To(Equal([]plan.Intent{{Target: "claude-code", Kind: "instructions", Op: plan.OpWriteFile, Path: ".claude/lola/example.md", Data: []byte("Be kind.\n"), Mode: 0o644}}))
		user := ctx
		user.Scope = target.User
		ins, notes, err := c.Plan(m, user)
		Expect(err).NotTo(HaveOccurred())
		Expect(ins).To(BeEmpty())
		Expect(notes[0].Text).To(ContainSubstring("not installed at user scope"))
	})

	It("skips artifacts restricted to other targets", func() {
		m := &module.Module{Name: "example", Commands: []module.Command{{Name: "x", Doc: doc("x"), Targets: []string{"opencode"}}}}
		ins, _, err := c.Plan(m, ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(ins).To(BeEmpty())
	})

	It("derives one sorted CLAUDE.md pointer block, none at user scope or for no modules", func() {
		Expect(c.Shared(target.Project, []string{"zeta", "alpha"})).To(Equal([]plan.Intent{{Target: "claude-code", Kind: plan.KindPointerBlock,
			Op: plan.OpPointerBlock, Path: "CLAUDE.md", Lines: []string{"@.claude/lola/alpha.md", "@.claude/lola/zeta.md"}}}))
		Expect(c.Shared(target.Project, nil)).To(BeEmpty())
		Expect(c.Shared(target.User, []string{"a"})).To(BeEmpty())
	})

	It("detects .claude/ or CLAUDE.md and roots user scope at $HOME", func() {
		dir := testenv.Isolate()
		Expect(c.Detect(dir)).To(BeFalse())
		Expect(os.WriteFile(filepath.Join(dir, "CLAUDE.md"), nil, 0o644)).To(Succeed())
		Expect(c.Detect(dir)).To(BeTrue())
		Expect(c.Root(target.User, "/proj", paths.Paths{Home: "/home/u"})).To(Equal("/home/u"))
		Expect(c.Root(target.Project, "/proj", paths.Paths{Home: "/home/u"})).To(Equal("/proj"))
	})
})
