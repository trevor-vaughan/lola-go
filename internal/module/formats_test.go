package module_test

import (
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// rendered is what every target plans for a module.
type rendered struct {
	Intents  []plan.Intent
	Findings []target.Finding
}

func renderAll(m *module.Module, contentDir string) map[string]rendered {
	out := map[string]rendered{}
	for _, t := range targets.All() {
		ctx := target.PlanContext{Scope: target.Project, Root: filepath.Join(contentDir, "..", "project"), ContentDir: contentDir,
			PluginRoot: "/copies/" + m.Name, PluginData: "/data/" + m.Name}
		ins, fs, err := t.Plan(m, ctx)
		Expect(err).NotTo(HaveOccurred())
		out[t.ID()] = rendered{ins, fs}
	}
	return out
}

// sameJSON compares values by their JSON encoding: YAML and JSON decode numbers to different
// Go types.
func sameJSON(a, b any) {
	ja, err := json.Marshal(a)
	Expect(err).NotTo(HaveOccurred())
	jb, err := json.Marshal(b)
	Expect(err).NotTo(HaveOccurred())
	Expect(string(jb)).To(Equal(string(ja)))
}

// cachedLoadsLikeFresh checks the fixture in root: detected and normalized as cache add does,
// then loaded the way the cache loads it, which runs no format adapter (Plan 5b design §4).
func cachedLoadsLikeFresh(root string, format string) {
	fresh, err := module.Detect(root, "fallback", targets.IDs())
	Expect(err).NotTo(HaveOccurred())
	Expect(fresh.Format).To(Equal(format))
	Expect(module.Normalize(root, fresh)).To(Succeed())
	first, err := os.ReadFile(filepath.Join(root, "lola.yml"))
	Expect(err).NotTo(HaveOccurred())

	cached, err := module.Load(root, "fallback", targets.IDs())
	Expect(err).NotTo(HaveOccurred())
	Expect(cached.Manifest).To(Equal("lola.yml"))
	Expect(cached.Shadowed).To(BeEmpty())
	Expect(cached.Format).To(Equal(fresh.Format))
	Expect([]any{cached.Name, cached.Description, cached.Version, cached.License, cached.Homepage, cached.Repository, cached.Author, cached.Keywords, cached.Targets}).
		To(Equal([]any{fresh.Name, fresh.Description, fresh.Version, fresh.License, fresh.Homepage, fresh.Repository, fresh.Author, fresh.Keywords, fresh.Targets}))
	sameJSON(fresh.Formats, cached.Formats)
	Expect(cached.Skills).To(Equal(fresh.Skills))
	Expect(cached.Commands).To(Equal(fresh.Commands))
	Expect(cached.Agents).To(Equal(fresh.Agents))
	Expect(cached.MCPServers).To(Equal(fresh.MCPServers))
	Expect(cached.Rules).To(Equal(fresh.Rules))
	Expect(cached.RuntimeHooks).To(Equal(fresh.RuntimeHooks))
	Expect(cached.DeclaredHooks).To(Equal(fresh.DeclaredHooks))
	Expect(renderAll(cached, root)).To(Equal(renderAll(fresh, root)))

	again, err := module.Detect(root, "fallback", targets.IDs())
	Expect(err).NotTo(HaveOccurred())
	Expect(module.Normalize(root, again)).To(Succeed())
	second, err := os.ReadFile(filepath.Join(root, "lola.yml"))
	Expect(err).NotTo(HaveOccurred())
	Expect(string(second)).To(Equal(string(first)), "normalizing a cached module changes nothing, so its digest is stable")
}

var _ = Describe("module formats", func() {
	var root string
	BeforeEach(func() { root = filepath.Join(testenv.Isolate(), "mod") })

	It("loads a cached lola module and renders it like the fetched one", func() {
		write(root, map[string]string{
			".lola.yaml": "name: example\ndescription: d\ncomponents:\n  commands:\n    - {name: about, content: \"Hi $ARGUMENTS\", frontmatter: {description: About}}\n" +
				"  runtime_hooks:\n    - {path: hooks/hooks.json}\n",
			".lola.yml":              "name: shadowed\n",
			"hooks/hooks.json":       `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"x"}]}]}}`,
			"skills/review/SKILL.md": "---\ndescription: Review\n---\nReview ${PLUGIN_ROOT}.\n",
			"rules/style.md":         "Small diffs.\n",
		})
		cachedLoadsLikeFresh(root, "lola")
	})

	It("loads a cached Claude Code plugin and renders it like the fetched one", func() {
		write(root, map[string]string{
			".claude-plugin/plugin.json": `{"name":"deploy-tools","version":"1.2.0","displayName":"Deploy","metadata":{"n":30,"f":1.5,"big":12345678901},
				"skills":["./extra/"],"commands":{"status":{"source":"./commands/status.md","argumentHint":"[env]"},
				"about":{"content":"About ${CLAUDE_PLUGIN_ROOT}","description":"About"}},"agents":["./agents/rev.md"],
				"mcpServers":[{"deploy":{"command":"node","args":["${CLAUDE_PLUGIN_ROOT}/s.js"],"env":{"D":"${CLAUDE_PLUGIN_DATA}"}}},"./b.mcpb"],
				"hooks":{"PostToolUse":[{"matcher":"Write","hooks":[{"type":"command","command":"x","timeout":30}]}]},
				"lspServers":{"go":{"command":"gopls","extensionToLanguage":{".go":"go"}}}}`,
			"skills/review/SKILL.md": "---\ndescription: Review\n---\nReview ${CLAUDE_PLUGIN_ROOT}.\n",
			"extra/lint/SKILL.md":    "---\ndescription: Lint\n---\n",
			"commands/status.md":     "---\ndescription: Status\n---\nStatus of $1.\n",
			"agents/rev.md":          "---\ndescription: Reviewer\n---\nReview.\n",
			".mcp.json":              `{"mcpServers":{"gh":{"command":"gh-mcp"}}}`,
			"hooks/hooks.json":       `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"y"}]}]}}`,
			"rules/not-a-rule.md":    "Claude plugins have no rules.\n",
		})
		cachedLoadsLikeFresh(root, "claude-plugin")
		b, err := os.ReadFile(filepath.Join(root, ".claude-plugin", "plugin.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(ContainSubstring(`"name":"deploy-tools"`), "the plugin's own files stay where they are")
		b, err = os.ReadFile(filepath.Join(root, "lola.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(ContainSubstring("big: 12345678901\n"), "a whole number stays one")
	})

	It("loads a cached Agent Plugins package and renders it like the fetched one", func() {
		write(root, map[string]string{
			"plugin.json": `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"dev.tools","version":"0.1.0",
				"extensions":{"dev.getlola":{"instructions":"./dev.getlola/INSTRUCTIONS.md"},"com.example":{"n":3}}}`,
			"skills/deploy/SKILL.md":               "---\nname: deploy\ndescription: Deploy\n---\nDeploy.\n",
			"skills/Bad/SKILL.md":                  "---\nname: Bad\ndescription: d\n---\n",
			"mcp.json":                             `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"v":{"type":"stdio","command":"./bin/v","cwd":"./w","args":["${PLUGIN_DATA}"]},"api":{"type":"streamable-http","url":"https://x/mcp"}}}`,
			"dev.getlola/INSTRUCTIONS.md":          "Be kind.\n",
			"dev.getlola/commands/ship.md":         "---\ndescription: Ship\n---\nShip.\n",
			"ai.opencode/commands/ship.md":         "OpenCode's ship.\n",
			"ai.opencode/agents/oc.md":             "---\ndescription: OC\n---\nOC.\n",
			"com.anthropic.claude/commands/cc.md":  "Claude only.\n",
			"com.google.gemini-cli/commands/gm.md": "Gemini only.\n",
			"commands/native.md":                   "not portable\n",
			"rules/native.md":                      "not portable\n",
		})
		cachedLoadsLikeFresh(root, "agent-plugins")
	})

	// YAML reads a plain "<<" key as a merge key, so the cached lola.yml must quote it.
	DescribeTable("keeps a \"<<\" key a plain key in the cached lola.yml", func(format string, files map[string]string) {
		write(root, files)
		cachedLoadsLikeFresh(root, format)
	},
		Entry("in a stdio server's env", "claude-plugin", map[string]string{
			".claude-plugin/plugin.json": `{"name":"mk","mcpServers":{"s":{"command":"x","env":{"<<":"v"}}}}`,
		}),
		Entry("as an unknown Agent Plugins namespace", "agent-plugins", map[string]string{
			"plugin.json":       `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"mk","extensions":{"<<":"x"}}`,
			"skills/s/SKILL.md": "---\nname: s\ndescription: S\n---\n",
		}),
		Entry("as a Claude hooks event", "claude-plugin", map[string]string{
			".claude-plugin/plugin.json": `{"name":"mk","hooks":{"<<":[{"matcher":"<<","hooks":[{"type":"command","command":"<<"}]}]}}`,
		}),
	)
})
