package claude_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/target/claude"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("claude-code agent MCP servers and permission mode", func() {
	c := claude.Target{}
	const root = "/proj/.lola/modules/example"
	var ctx target.PlanContext
	BeforeEach(func() {
		testenv.Isolate()
		ctx = target.PlanContext{Scope: target.Project, ContentDir: "/cache/example/content", PluginRoot: root, PluginData: "/proj/.lola/plugin-data/example"}
	})
	agent := func(fm string) *module.Module {
		d, err := module.ParseDoc([]byte("---\ndescription: d\n" + fm + "---\nA\n"))
		Expect(err).NotTo(HaveOccurred())
		return &module.Module{Name: "example", Agents: []module.Agent{{Name: "browser", File: "agents/browser.md", Doc: d}}}
	}

	It("gates an agent with an inline server under its consent, with the servers as rendered and no hooks", func() {
		ins, fs, err := c.Plan(agent("mcpServers:\n  - github\n  - pw: {command: '${PLUGIN_ROOT}/pw', args: [-y]}\n"), ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(fs).To(BeEmpty())
		Expect(ins).To(HaveLen(1))
		Expect(ins[0].Consent).To(PointTo(MatchFields(IgnoreExtras, Fields{"Settings": Equal(".claude/settings.local.json"),
			"Kind": Equal("agent"), "Name": Equal("browser"), "Hooks": BeNil()})))
		Expect(string(ins[0].Consent.MCPServers)).To(MatchJSON(`[{"pw":{"command":"` + root + `/pw","args":["-y"]}}]`))
		Expect(string(ins[0].Data)).To(ContainSubstring("- github\n"), "installed as written")
	})

	It("joins an agent's frontmatter hooks, under one consent", func() {
		ins, _, err := c.Plan(agent("hooks: {Stop: [{hooks: [{type: prompt, prompt: p}]}]}\nmcpServers:\n  - pw: {url: 'https://pw.example'}\n"), ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(ins[0].Consent.Hooks)).To(MatchJSON(`{"Stop":[{"hooks":[{"type":"prompt","prompt":"p"}]}]}`))
		Expect(string(ins[0].Consent.MCPServers)).To(MatchJSON(`[{"pw":{"url":"https://pw.example"}}]`))
	})

	It("leaves an agent that only names configured servers ungated", func() {
		ins, fs, err := c.Plan(agent("mcpServers: [github, slack]\n"), ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(fs).To(BeEmpty())
		Expect(ins).To(HaveLen(1))
		Expect(ins[0].Consent).To(BeNil())
	})

	It("gates a server defined through an alias", func() {
		ins, _, err := c.Plan(agent("x: &s {pw: {command: npx}}\nmcpServers: [github, *s]\n"), ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(ins[0].Consent).NotTo(BeNil())
		Expect(string(ins[0].Consent.MCPServers)).To(MatchJSON(`[{"pw":{"command":"npx"}}]`))
	})

	It("treats a case variant of the key as MCP servers", func() {
		ins, _, err := c.Plan(agent("McpServers:\n  - pw: {command: npx}\n"), ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(ins[0].Consent).NotTo(BeNil())
		Expect(string(ins[0].Consent.MCPServers)).To(MatchJSON(`[{"pw":{"command":"npx"}}]`))
	})

	It("installs permissionMode as written, with a warning naming the agent and the sanitized mode", func() {
		ins, fs, err := c.Plan(agent("permissionMode: bypassPermissions\n"), ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(fs).To(ConsistOf(MatchFields(IgnoreExtras, Fields{"Severity": Equal(target.SeverityWarn), "Target": Equal("claude-code"),
			"Kind": Equal("agent"), "Artifact": Equal("browser"), "Code": Equal("claude-code/permission-mode"),
			"Text": Equal("sets the permission mode it runs in: bypassPermissions (Claude Code v2.1.267+ ignores bypassPermissions in a subagent's frontmatter)")})))
		Expect(string(ins[0].Data)).To(ContainSubstring("permissionMode: bypassPermissions\n"))

		_, fs, err = c.Plan(agent("PermissionMode: \"plan\\e[2J\\u202e\"\n"), ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(fs).To(ConsistOf(HaveField("Text", "sets the permission mode it runs in: plan?[2J? (Claude Code v2.1.267+ ignores bypassPermissions in a subagent's frontmatter)")))
	})
})
