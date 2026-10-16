package module_test

import (
	"errors"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// write creates files under root from a path → content map.
func write(root string, files map[string]string) {
	for p, c := range files {
		full := filepath.Join(root, p)
		Expect(os.MkdirAll(filepath.Dir(full), 0o755)).To(Succeed())
		Expect(os.WriteFile(full, []byte(c), 0o644)).To(Succeed())
	}
}

var _ = Describe("Load", func() {
	var root string
	BeforeEach(func() { root = filepath.Join(testenv.Isolate(), "mod") })

	It("loads a full native layout, sorted, under the fallback name", func() {
		write(root, map[string]string{
			"skills/zeta/SKILL.md":       "---\ndescription: z\n---\nZ\n",
			"skills/alpha/SKILL.md":      "---\ndescription: a\n---\nA\n",
			"skills/alpha/ref/notes.txt": "n",
			"commands/go.md":             "Run $ARGUMENTS\n",
			"commands/go/helper.sh":      "#!/bin/sh\n",
			"agents/rev.md":              "---\ndescription: reviews\nmodel: opus\n---\nYou review.\n",
			"mcps.json":                  `{"mcpServers":{"gh":{"command":"npx","args":["-y","gh"],"env":{"T":"1"}},"web":{"type":"sse","url":"https://x/sse"},"api":{"url":"https://x/mcp"}}}`,
			"INSTRUCTIONS.md":            "Always be kind.\n",
		})
		m, err := module.Load(root, "example")
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Name).To(Equal("example"))
		Expect(m.Format).To(Equal("native"))
		Expect([]string{m.Skills[0].Name, m.Skills[1].Name}).To(Equal([]string{"alpha", "zeta"}))
		Expect(m.Skills[0].Dir).To(Equal("skills/alpha"))
		Expect(m.Skills[0].Doc.String("description")).To(Equal("a"))
		Expect(m.Commands).To(HaveLen(1))
		Expect(m.Commands[0]).To(MatchFields(IgnoreExtras, Fields{"Name": Equal("go"), "File": Equal("commands/go.md"), "SidecarDir": Equal("commands/go")}))
		Expect(m.Commands[0].Doc.Frontmatter).To(BeNil())
		Expect(m.Agents[0].Doc.String("model")).To(Equal("opus"))
		Expect(m.MCPServers).To(HaveLen(3))
		Expect(m.MCPServers[0]).To(MatchFields(IgnoreExtras, Fields{"Name": Equal("api"), "Transport": Equal("http"), "URL": Equal("https://x/mcp")}))
		Expect(m.MCPServers[1]).To(MatchFields(IgnoreExtras, Fields{"Name": Equal("gh"), "Transport": Equal("stdio"), "Command": Equal("npx")}))
		Expect(m.MCPServers[1].Args).To(Equal([]string{"-y", "gh"}))
		Expect(m.MCPServers[1].Env).To(Equal(map[string]string{"T": "1"}))
		Expect(m.MCPServers[2].Transport).To(Equal("sse"))
		Expect(m.Instructions).To(Equal("Always be kind.\n"))
		Expect(m.LegacyAgentsMD).To(BeFalse())
	})

	It("reads lola.yml metadata, declared hooks and targets", func() {
		write(root, map[string]string{
			"lola.yml":      "name: example\ndescription: d\nversion: 1.2.0\nauthor: {name: A, email: a@x}\nlicense: MIT\nkeywords: [git]\ntargets: [claude-code]\nhooks: {pre-install: scripts/setup.sh, post-install: scripts/post.sh}\n",
			"commands/a.md": "x",
		})
		m, err := module.Load(root, "ignored")
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Format).To(Equal("lola"))
		Expect(m.Name).To(Equal("example"))
		Expect(m.Version).To(Equal("1.2.0"))
		Expect(m.Author).To(Equal(&module.Author{Name: "A", Email: "a@x"}))
		Expect(m.Targets).To(Equal([]string{"claude-code"}))
		Expect(m.DeclaredHooks).To(Equal([]module.Hook{{Event: "post-install", Command: "scripts/post.sh"}, {Event: "pre-install", Command: "scripts/setup.sh"}}))
	})

	It("rejects unknown lola.yml keys with a hint naming the key", func() {
		write(root, map[string]string{"lola.yml": "name: example\nsummary: typo\n", "commands/a.md": "x"})
		_, err := module.Load(root, "x")
		Expect(err).To(MatchError(ContainSubstring("lola.yml")))
		Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("summary")))
	})

	It("requires name in lola.yml and refuses both lola.yml and lola.yaml", func() {
		write(root, map[string]string{"lola.yml": "description: d\n", "commands/a.md": "x"})
		_, err := module.Load(root, "x")
		Expect(err).To(MatchError(ContainSubstring("name")))
		write(root, map[string]string{"lola.yml": "name: a\n", "lola.yaml": "name: b\n"})
		_, err = module.Load(root, "x")
		Expect(err).To(MatchError(ContainSubstring("both")))
	})

	It("treats a root SKILL.md as a skill named after the module, excluding module files", func() {
		write(root, map[string]string{"SKILL.md": "---\ndescription: s\n---\n", "helper.py": "", "lola.yml": "name: solo\n", "commands/c.md": "x"})
		m, err := module.Load(root, "x")
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Skills).To(HaveLen(1))
		Expect(m.Skills[0].Name).To(Equal("solo"))
		Expect(m.Skills[0].Dir).To(Equal("."))
		Expect(m.Skills[0].Exclude).To(ContainElements("lola.yml", "commands", "SKILL.md"))
	})

	It("skips a skill directory without SKILL.md, with a warning", func() {
		write(root, map[string]string{"skills/empty/readme.txt": "", "commands/a.md": "x"})
		m, err := module.Load(root, "x")
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Skills).To(BeEmpty())
		Expect(m.Warnings).To(ContainElement(ContainSubstring("skills/empty")))
	})

	It("flags a legacy AGENTS.md without installing it", func() {
		write(root, map[string]string{"AGENTS.md": "x", "commands/a.md": "x"})
		m, err := module.Load(root, "x")
		Expect(err).NotTo(HaveOccurred())
		Expect(m.LegacyAgentsMD).To(BeTrue())
	})

	It("reports ErrNoModule naming what it looked for", func() {
		write(root, map[string]string{"README.md": "x", "AGENTS.md": "x"})
		_, err := module.Load(root, "x")
		Expect(errors.Is(err, module.ErrNoModule)).To(BeTrue())
		Expect(err).To(MatchError(ContainSubstring("skills/*/SKILL.md")))
	})

	DescribeTable("rejects invalid artifacts",
		func(files map[string]string, msg string) {
			write(root, files)
			_, err := module.Load(root, "x")
			Expect(err).To(MatchError(ContainSubstring(msg)))
		},
		Entry("bad skill name", map[string]string{"skills/Bad/SKILL.md": "x"}, "skill name"),
		Entry("bad command name", map[string]string{"commands/a b.md": "x"}, "command name"),
		Entry("MCP server without command or url", map[string]string{"mcps.json": `{"mcpServers":{"x":{}}}`}, "command or a url"),
		Entry("unknown MCP type", map[string]string{"mcps.json": `{"mcpServers":{"x":{"type":"ws","url":"u"}}}`}, "transport"),
		Entry("corrupt mcps.json", map[string]string{"mcps.json": `{`}, "mcps.json"),
		Entry("broken agent frontmatter", map[string]string{"agents/a.md": "---\nx: [\n---\n"}, "agents/a.md"),
	)

	It("rejects an invalid fallback module name", func() {
		write(root, map[string]string{"commands/a.md": "x"})
		_, err := module.Load(root, "Bad Name")
		Expect(err).To(MatchError(ContainSubstring("module name")))
	})

	It("sorts commands and agents by name, not file name", func() {
		write(root, map[string]string{
			"commands/a-b.md": "cmd a-b",
			"commands/a.md":   "cmd a",
			"agents/a-b.md":   "---\ndescription: d\n---\nagent a-b",
			"agents/a.md":     "---\ndescription: d\n---\nagent a",
		})
		m, err := module.Load(root, "x")
		Expect(err).NotTo(HaveOccurred())
		Expect([]string{m.Commands[0].Name, m.Commands[1].Name}).To(Equal([]string{"a", "a-b"}))
		Expect([]string{m.Agents[0].Name, m.Agents[1].Name}).To(Equal([]string{"a", "a-b"}))
	})
})
