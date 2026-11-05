package module_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// write creates files under root from a path → content map; files ending in .sh are
// executable.
func write(root string, files map[string]string) {
	for p, c := range files {
		full := filepath.Join(root, p)
		mode := os.FileMode(0o644)
		if filepath.Ext(p) == ".sh" {
			mode = 0o755
		}
		Expect(os.MkdirAll(filepath.Dir(full), 0o755)).To(Succeed())
		Expect(os.WriteFile(full, []byte(c), mode)).To(Succeed())
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
			"rules/kind.md":              "---\ndescription: d\n---\nAlways be kind.\n",
		})
		m, err := module.Load(root, "example", targets.IDs())
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
		Expect(m.Rules).To(Equal([]module.Rule{{Name: "kind", Body: "Always be kind.\n"}}))
		Expect(m.Warnings).To(BeEmpty())
	})

	It("reads lola.yml metadata, declared hooks and targets", func() {
		write(root, map[string]string{
			"lola.yml":         "name: example\ndescription: d\nversion: 1.2.0\nauthor: {name: A, email: a@x}\nlicense: MIT\nkeywords: [git]\ntargets: [claude-code]\nhooks: {pre-install: scripts/setup.sh, post-install: scripts/post.sh}\n",
			"commands/a.md":    "x",
			"scripts/setup.sh": "#!/bin/sh\n",
			"scripts/post.sh":  "#!/bin/sh\n",
		})
		m, err := module.Load(root, "ignored", targets.IDs())
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Format).To(Equal("lola"))
		Expect(m.Name).To(Equal("example"))
		Expect(m.Version).To(Equal("1.2.0"))
		Expect(m.Author).To(Equal(&module.Author{Name: "A", Email: "a@x"}))
		Expect(m.Targets).To(Equal([]string{"claude-code"}))
		Expect(m.DeclaredHooks).To(Equal([]module.Hook{{Event: "post-install", Script: "scripts/post.sh"}, {Event: "pre-install", Script: "scripts/setup.sh"}}))
	})

	DescribeTable("refuses hooks lola could not run",
		func(hooks string, files map[string]string, msg string) {
			files["lola.yml"] = "name: example\nhooks: " + hooks + "\n"
			files["commands/a.md"] = "x"
			write(root, files)
			_, err := module.Load(root, "x", targets.IDs())
			Expect(err).To(MatchError(ContainSubstring(msg)))
		},
		Entry("unknown event", "{pre-uninstall: x.sh}", map[string]string{"x.sh": ""}, `lola.yml: hook "pre-uninstall" is unknown`),
		Entry("absolute script", "{pre-install: /bin/sh}", map[string]string{}, `pre-install hook "/bin/sh" must be a relative path inside the module`),
		Entry("script outside the module", "{pre-install: ../x.sh}", map[string]string{}, "must be a relative path inside the module"),
		Entry("missing script", "{post-install: nope.sh}", map[string]string{}, "post-install hook \"nope.sh\" does not exist"),
		Entry("directory", "{pre-install: scripts}", map[string]string{"scripts/a.sh": ""}, "pre-install hook \"scripts\" is not a regular file"),
	)

	It("refuses a hook script without an execute bit, with the fix", func() {
		write(root, map[string]string{"lola.yml": "name: example\nhooks: {pre-install: setup.txt}\n", "setup.txt": "", "commands/a.md": "x"})
		_, err := module.Load(root, "x", targets.IDs())
		Expect(err).To(MatchError("lola.yml: pre-install hook \"setup.txt\" is not executable"))
		Expect(errs.Hints(err)).To(Equal([]string{"chmod +x setup.txt"}))
	})

	It("rejects unknown lola.yml keys with a hint naming the key", func() {
		write(root, map[string]string{"lola.yml": "name: example\nsummary: typo\n", "commands/a.md": "x"})
		_, err := module.Load(root, "x", targets.IDs())
		Expect(err).To(MatchError(ContainSubstring("lola.yml")))
		Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("summary")))
	})

	It("requires name in lola.yml", func() {
		write(root, map[string]string{"lola.yml": "description: d\n", "commands/a.md": "x"})
		_, err := module.Load(root, "x", targets.IDs())
		Expect(err).To(MatchError(ContainSubstring("name")))
	})

	It("reads the first manifest by precedence and warns about each one it ignores", func() {
		write(root, map[string]string{".lola.yml": "name: d\n", "lola.yml": "name: b\n", ".lola.yaml": "name: c\n", "commands/a.md": "x"})
		m, err := module.Load(root, "x", targets.IDs())
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Name).To(Equal("b"))
		Expect(m.Manifest).To(Equal("lola.yml"))
		Expect(m.Shadowed).To(Equal([]string{".lola.yaml", ".lola.yml"}))
		Expect(m.Warnings).To(Equal([]string{".lola.yaml was ignored: lola.yml takes precedence", ".lola.yml was ignored: lola.yml takes precedence"}))
		write(root, map[string]string{"lola.yaml": "name: a\n"})
		m, err = module.Load(root, "x", targets.IDs())
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Name).To(Equal("a"))
	})

	It("treats a root SKILL.md as a skill named after the module, excluding module files", func() {
		write(root, map[string]string{"SKILL.md": "---\ndescription: s\n---\n", "helper.py": "", "lola.yml": "name: solo\n", "commands/c.md": "x"})
		m, err := module.Load(root, "x", targets.IDs())
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Skills).To(HaveLen(1))
		Expect(m.Skills[0].Name).To(Equal("solo"))
		Expect(m.Skills[0].Dir).To(Equal("."))
		Expect(m.Skills[0].Exclude).To(ContainElements("lola.yml", "commands", "SKILL.md"))
	})

	It("skips a skill directory without SKILL.md, with a warning", func() {
		write(root, map[string]string{"skills/empty/readme.txt": "", "commands/a.md": "x"})
		m, err := module.Load(root, "x", targets.IDs())
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Skills).To(BeEmpty())
		Expect(m.Warnings).To(ContainElement(ContainSubstring("skills/empty")))
	})

	It("warns about legacy INSTRUCTIONS.md and AGENTS.md and never loads them as rules", func() {
		write(root, map[string]string{"INSTRUCTIONS.md": "x", "AGENTS.md": "x", "commands/a.md": "x"})
		m, err := module.Load(root, "x", targets.IDs())
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Rules).To(BeEmpty())
		Expect(m.Warnings).To(Equal([]string{
			"INSTRUCTIONS.md in a module is not installed: lola never edits your assistants' instruction files. Move it to rules/<topic>.md to ship it as always-on rules",
			"AGENTS.md in a module is not installed: lola never edits your assistants' instruction files. Move it to rules/<topic>.md to ship it as always-on rules",
		}))
	})

	It("loads a module that ships only rules, sorted by name, and skips anything else in rules/ with a warning", func() {
		write(root, map[string]string{"rules/a-b.md": "AB\n", "rules/a.md": "A\n", "rules/notes.txt": "n", "rules/sub/x.md": "x"})
		m, err := module.Load(root, "x", targets.IDs())
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Rules).To(Equal([]module.Rule{{Name: "a", Body: "A\n"}, {Name: "a-b", Body: "AB\n"}}))
		Expect(m.Warnings).To(ConsistOf("rules/notes.txt is not a rule (rules/<name>.md); skipped", "rules/sub is not a rule (rules/<name>.md); skipped"))
	})

	It("refuses a path-scoped rule and points at skills", func() {
		write(root, map[string]string{"rules/x.md": "---\npaths: [\"src/**\"]\n---\nX\n"})
		_, err := module.Load(root, "x", targets.IDs())
		Expect(err).To(MatchError("rules/x.md: a rule applies in every session, so paths: is not supported"))
		Expect(errs.Hints(err)).To(Equal([]string{"path-scoped guidance belongs in a skill: skills/<name>/SKILL.md"}))
	})

	It("reports ErrNoModule naming what it looked for", func() {
		write(root, map[string]string{"README.md": "x", "AGENTS.md": "x"})
		_, err := module.Load(root, "x", targets.IDs())
		Expect(errors.Is(err, module.ErrNoModule)).To(BeTrue())
		Expect(err).To(MatchError(ContainSubstring("skills/*/SKILL.md")))
	})

	DescribeTable("rejects invalid artifacts",
		func(files map[string]string, msg string) {
			write(root, files)
			_, err := module.Load(root, "x", targets.IDs())
			Expect(err).To(MatchError(ContainSubstring(msg)))
		},
		Entry("bad skill name", map[string]string{"skills/Bad/SKILL.md": "x"}, "skill name"),
		Entry("bad command name", map[string]string{"commands/a b.md": "x"}, "command name"),
		Entry("bad rule name", map[string]string{"rules/Bad.md": "x"}, "rule name"),
		Entry("broken rule frontmatter", map[string]string{"rules/a.md": "---\nx: [\n---\n"}, "rules/a.md"),
		Entry("MCP server without command or url", map[string]string{"mcps.json": `{"mcpServers":{"x":{}}}`}, "command or a url"),
		Entry("unknown MCP type", map[string]string{"mcps.json": `{"mcpServers":{"x":{"type":"ws","url":"u"}}}`}, "transport"),
		Entry("corrupt mcps.json", map[string]string{"mcps.json": `{`}, "mcps.json"),
		Entry("broken agent frontmatter", map[string]string{"agents/a.md": "---\nx: [\n---\n"}, "agents/a.md"),
	)

	It("rejects an invalid fallback module name", func() {
		write(root, map[string]string{"commands/a.md": "x"})
		_, err := module.Load(root, "Bad Name", targets.IDs())
		Expect(err).To(MatchError(ContainSubstring("module name")))
	})

	It("sorts commands and agents by name, not file name", func() {
		write(root, map[string]string{
			"commands/a-b.md": "cmd a-b",
			"commands/a.md":   "cmd a",
			"agents/a-b.md":   "---\ndescription: d\n---\nagent a-b",
			"agents/a.md":     "---\ndescription: d\n---\nagent a",
		})
		m, err := module.Load(root, "x", targets.IDs())
		Expect(err).NotTo(HaveOccurred())
		Expect([]string{m.Commands[0].Name, m.Commands[1].Name}).To(Equal([]string{"a", "a-b"}))
		Expect([]string{m.Agents[0].Name, m.Agents[1].Name}).To(Equal([]string{"a", "a-b"}))
	})
})

var _ = Describe("Normalize", func() {
	var root string
	BeforeEach(func() { root = filepath.Join(testenv.Isolate(), "mod") })

	It("leaves exactly one generated lola.yml that loads to the same module", func() {
		write(root, map[string]string{
			".lola.yaml":    "name: example\ndescription: d\nauthor: {name: A}\nkeywords: [git]\ntargets: [claude-code]\nformats: {claude-code: {x: 1}}\nhooks: {pre-install: setup.sh}\n",
			".lola.yml":     "name: shadowed\n",
			"commands/a.md": "x",
			"setup.sh":      "#!/bin/sh\n",
		})
		m, err := module.Load(root, "fallback", targets.IDs())
		Expect(err).NotTo(HaveOccurred())
		Expect(module.Normalize(root, m)).To(Succeed())
		Expect(filepath.Join(root, ".lola.yml")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(root, ".lola.yaml")).NotTo(BeAnExistingFile())
		again, err := module.Load(root, "fallback", targets.IDs())
		Expect(err).NotTo(HaveOccurred())
		Expect(again.Manifest).To(Equal("lola.yml"))
		Expect(again.Shadowed).To(BeEmpty())
		Expect(again.Name).To(Equal("example"))
		Expect(again.Description).To(Equal("d"))
		Expect(again.Author).To(Equal(&module.Author{Name: "A"}))
		Expect(again.Keywords).To(Equal([]string{"git"}))
		Expect(again.Targets).To(Equal([]string{"claude-code"}))
		Expect(again.Formats).To(Equal(map[string]any{"claude-code": map[string]any{"x": 1}}))
		Expect(again.DeclaredHooks).To(Equal(m.DeclaredHooks))
	})

	It("gives a native-layout module a lola.yml carrying its name", func() {
		write(root, map[string]string{"commands/a.md": "x"})
		m, err := module.Load(root, "fallback", targets.IDs())
		Expect(err).NotTo(HaveOccurred())
		Expect(module.Normalize(root, m)).To(Succeed())
		b, err := os.ReadFile(filepath.Join(root, "lola.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal("name: fallback\n"))
	})
})

var _ = Describe("module JSON files", func() {
	var root string
	BeforeEach(func() { root = filepath.Join(testenv.Isolate(), "mod") })

	DescribeTable("refuses a repeated key at any depth, naming the key, where it is and the file",
		func(doc, msg string) {
			write(root, map[string]string{"mcps.json": doc})
			_, err := module.Load(root, "x", targets.IDs())
			Expect(err).To(MatchError(msg))
			Expect(errs.Hints(err)).To(ConsistOf(ContainSubstring("keep one of them")))
		},
		Entry("top level", `{"mcpServers":{},"mcpServers":{}}`, `mcps.json: key "mcpServers" appears twice in the top-level object`),
		Entry("a server", `{"mcpServers":{"gh":{"command":"a"},"gh":{"command":"b"}}}`, `mcps.json: key "gh" appears twice in /mcpServers`),
		Entry("a server field", `{"mcpServers":{"gh":{"command":"a","command":"b"}}}`, `mcps.json: key "command" appears twice in /mcpServers/gh`),
		Entry("env", `{"mcpServers":{"gh":{"command":"a","env":{"A":"1","A":"2"}}}}`, `mcps.json: key "A" appears twice in /mcpServers/gh/env`),
		Entry("an object in an array", `{"x":[1,{"a":1},{"a/b":[{"k":1,"k":2}]}],"mcpServers":{}}`, `mcps.json: key "k" appears twice in /x/2/a~1b/0`),
		Entry("a key written two ways", `{"mcpServers":{"gh":{"command":"a"},"g\u0068":{"command":"b"}}}`, `mcps.json: key "gh" appears twice in /mcpServers`),
		Entry("a key with control characters", "{\"mcpServers\":{\"\\u001b[2J\":{},\"\\u001b[2J\":{}}}", `mcps.json: key "\x1b[2J" appears twice in /mcpServers`),
		Entry("a key with a newline", "{\"mcpServers\":{\"a\\nb\":{},\"a\\nb\":{}}}", `mcps.json: key "a\nb" appears twice in /mcpServers`),
		Entry("env, after a key differing in case", `{"mcpServers":{"gh":{"command":"a","env":{"a":"1","A":"2","A":"3"}}}}`, `mcps.json: key "A" appears twice in /mcpServers/gh/env`),
	)

	DescribeTable("refuses keys that differ only in case, outside env and headers",
		func(doc, msg string) {
			write(root, map[string]string{"mcps.json": doc})
			_, err := module.Load(root, "x", targets.IDs())
			Expect(err).To(MatchError(msg))
			Expect(errs.Hints(err)).To(ConsistOf("keep one spelling: lola reads these keys regardless of case, so the two would clash; " +
				"only the names in a server's env and headers may differ just in case"))
		},
		Entry("a top-level key", `{"mcpServers":{"gh":{"command":"a"}},"MCPSERVERS":{"gh":{"command":"evil"}}}`, `mcps.json: key "MCPSERVERS" differs from "mcpServers" only in case, in the top-level object`),
		Entry("a server field", `{"mcpServers":{"gh":{"command":"a","Command":"evil"}}}`, `mcps.json: key "Command" differs from "command" only in case, in /mcpServers/gh`),
		Entry("a key differing by Unicode case folding", `{"mcpServers":{"gh":{"command":"a"}},"mcp\u017fervers":{"gh":{"command":"evil"}}}`, `mcps.json: key "mcpſervers" differs from "mcpServers" only in case, in the top-level object`),
		Entry("a server name", `{"mcpServers":{"gh":{"command":"a"},"GH":{"command":"b"}}}`, `mcps.json: key "GH" differs from "gh" only in case, in /mcpServers`),
		Entry("an object inside env", `{"mcpServers":{"gh":{"command":"a","env":{"x":{"k":1,"K":2}}}}}`, `mcps.json: key "K" differs from "k" only in case, in /mcpServers/gh/env/x`),
	)

	DescribeTable("accepts env and header names that differ only in case",
		func(files map[string]string, field string) {
			write(root, files)
			m, err := module.Detect(root, "x", targets.IDs())
			Expect(err).NotTo(HaveOccurred())
			Expect(m.MCPServers).To(HaveLen(1))
			got := m.MCPServers[0].Env
			if field == "headers" {
				got = m.MCPServers[0].Headers
			}
			Expect(got).To(And(HaveKeyWithValue("HTTP_PROXY", "a"), HaveKeyWithValue("http_proxy", "b")))
		},
		Entry("env in mcps.json", map[string]string{"mcps.json": `{"mcpServers":{"s":{"command":"x","env":{"HTTP_PROXY":"a","http_proxy":"b"}}}}`}, "env"),
		Entry("headers in mcps.json", map[string]string{"mcps.json": `{"mcpServers":{"s":{"url":"https://x","headers":{"HTTP_PROXY":"a","http_proxy":"b"}}}}`}, "headers"),
		Entry("env in a Claude plugin's .mcp.json", map[string]string{".claude-plugin/plugin.json": `{"name":"p"}`,
			".mcp.json": `{"mcpServers":{"s":{"command":"x","env":{"HTTP_PROXY":"a","http_proxy":"b"}}}}`}, "env"),
		Entry("env in a Claude plugin's inline server", map[string]string{
			".claude-plugin/plugin.json": `{"name":"p","mcpServers":{"s":{"command":"x","env":{"HTTP_PROXY":"a","http_proxy":"b"}}}}`}, "env"),
		Entry("env in an Agent Plugins mcp.json", map[string]string{"plugin.json": `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"p"}`,
			"mcp.json": `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"s":{"type":"stdio","command":"x","env":{"HTTP_PROXY":"a","http_proxy":"b"}}}}`}, "env"),
		Entry("headers in an Agent Plugins mcp.json", map[string]string{"plugin.json": `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"p"}`,
			"mcp.json": `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"s":{"type":"sse","url":"https://x","headers":{"HTTP_PROXY":"a","http_proxy":"b"}}}}`}, "headers"),
		Entry("env in a Claude plugin's list-form inline server", map[string]string{
			".claude-plugin/plugin.json": `{"name":"p","mcpServers":[{"s":{"command":"x","env":{"HTTP_PROXY":"a","http_proxy":"b"}}}]}`}, "env"),
		Entry("headers in a Claude plugin's list-form inline server", map[string]string{
			".claude-plugin/plugin.json": `{"name":"p","mcpServers":[{"s":{"url":"https://x","headers":{"HTTP_PROXY":"a","http_proxy":"b"}}}]}`}, "headers"),
	)
	apManifest := `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"p"}`
	apMCP := func(servers string) string {
		return `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{` + servers + `}}`
	}
	envServer := `"env":{"command":"shown","Command":"evil"}`
	headersServer := `"headers":{"url":"https://shown","URL":"https://evil"}`
	DescribeTable("exempts only /mcpServers/<server>/env and /headers, so servers named env or headers keep the case rule",
		func(files map[string]string, msg string) {
			write(root, files)
			_, err := module.Detect(root, "x", targets.IDs())
			Expect(err).To(MatchError(msg))
		},
		Entry("a server named env in mcps.json", map[string]string{"mcps.json": `{"mcpServers":{` + envServer + `}}`},
			`mcps.json: key "Command" differs from "command" only in case, in /mcpServers/env`),
		Entry("a server named headers in mcps.json", map[string]string{"mcps.json": `{"mcpServers":{` + headersServer + `}}`},
			`mcps.json: key "URL" differs from "url" only in case, in /mcpServers/headers`),
		Entry("a server named env in a Claude plugin's .mcp.json", map[string]string{".claude-plugin/plugin.json": `{"name":"p"}`, ".mcp.json": `{"mcpServers":{` + envServer + `}}`},
			`.mcp.json: key "Command" differs from "command" only in case, in /mcpServers/env`),
		Entry("a server named headers in a Claude plugin's .mcp.json", map[string]string{".claude-plugin/plugin.json": `{"name":"p"}`, ".mcp.json": `{"mcpServers":{` + headersServer + `}}`},
			`.mcp.json: key "URL" differs from "url" only in case, in /mcpServers/headers`),
		Entry("a server named env inline in a Claude plugin", map[string]string{".claude-plugin/plugin.json": `{"name":"p","mcpServers":{` + envServer + `}}`},
			`.claude-plugin/plugin.json: key "Command" differs from "command" only in case, in /mcpServers/env`),
		Entry("a server named headers inline in a Claude plugin", map[string]string{".claude-plugin/plugin.json": `{"name":"p","mcpServers":{` + headersServer + `}}`},
			`.claude-plugin/plugin.json: key "URL" differs from "url" only in case, in /mcpServers/headers`),
		Entry("a server named env in an Agent Plugins mcp.json", map[string]string{"plugin.json": apManifest, "mcp.json": apMCP(`"env":{"type":"stdio","command":"shown","Command":"evil"}`)},
			`mcp.json: key "Command" differs from "command" only in case, in /mcpServers/env`),
		Entry("a server named headers in an Agent Plugins mcp.json", map[string]string{"plugin.json": apManifest, "mcp.json": apMCP(`"headers":{"type":"sse","url":"https://shown","URL":"https://evil"}`)},
			`mcp.json: key "URL" differs from "url" only in case, in /mcpServers/headers`),
		Entry("an env object elsewhere", map[string]string{"mcps.json": `{"mcpServers":{"s":{"command":"x"}},"x":{"env":{"A":"1","a":"2"}}}`},
			`mcps.json: key "a" differs from "A" only in case, in /x/env`),
		Entry("a server named env in a Claude plugin's list form", map[string]string{".claude-plugin/plugin.json": `{"name":"p","mcpServers":[{` + envServer + `}]}`},
			`.claude-plugin/plugin.json: key "Command" differs from "command" only in case, in /mcpServers/0/env`),
		Entry("a server named headers in a Claude plugin's list form", map[string]string{".claude-plugin/plugin.json": `{"name":"p","mcpServers":[{` + headersServer + `}]}`},
			`.claude-plugin/plugin.json: key "URL" differs from "url" only in case, in /mcpServers/0/headers`),
		Entry("an env object one level too deep in the list form", map[string]string{".claude-plugin/plugin.json": `{"name":"p","mcpServers":[{"s":{"command":"x","env":{"x":{"k":1,"K":2}}}}]}`},
			`.claude-plugin/plugin.json: key "K" differs from "k" only in case, in /mcpServers/0/s/env/x`),
	)
})

var _ = Describe("MCP servers in Claude's .mcp.json shape", func() {
	var root string
	BeforeEach(func() { root = filepath.Join(testenv.Isolate(), "mod") })

	mixed := `{"mcpServers":{"x":{"command":"a","url":"https://x"}}}`
	mixedMsg := `server "x" mixes stdio fields (command, args, env, cwd) with remote ones (url, headers)`
	DescribeTable("refuses a server whose fields do not fit one transport, naming where it is",
		func(files map[string]string, msg string) {
			write(root, files)
			_, err := module.Detect(root, "x", targets.IDs())
			Expect(err).To(MatchError(msg))
			Expect(errs.Hints(err)).To(HaveLen(1), "each hint once")
		},
		Entry("a command and a url in mcps.json", map[string]string{"mcps.json": mixed}, "mcps.json: "+mixedMsg),
		Entry("env and a url in mcps.json", map[string]string{"mcps.json": `{"mcpServers":{"x":{"url":"https://x","env":{"A":"1"}}}}`}, "mcps.json: "+mixedMsg),
		Entry("a command and headers in mcps.json", map[string]string{"mcps.json": `{"mcpServers":{"x":{"command":"a","headers":{"A":"1"}}}}`}, "mcps.json: "+mixedMsg),
		Entry("type stdio with a url", map[string]string{"mcps.json": `{"mcpServers":{"x":{"type":"stdio","command":"a","url":"https://x"}}}`},
			`mcps.json: server "x" has type "stdio", so it takes no url or headers`),
		Entry("type stdio without a command", map[string]string{"mcps.json": `{"mcpServers":{"x":{"type":"stdio"}}}`}, `mcps.json: server "x" has type "stdio" but no command`),
		Entry("type sse with a command", map[string]string{"mcps.json": `{"mcpServers":{"x":{"type":"sse","url":"https://x","command":"a"}}}`},
			`mcps.json: server "x" has type "sse", so it takes no command, args, env or cwd`),
		Entry("type http with a cwd", map[string]string{"mcps.json": `{"mcpServers":{"x":{"type":"http","url":"https://x","cwd":"w"}}}`},
			`mcps.json: server "x" has type "http", so it takes no command, args, env or cwd`),
		Entry("type streamable-http without a url", map[string]string{"mcps.json": `{"mcpServers":{"x":{"type":"streamable-http"}}}`},
			`mcps.json: server "x" has type "streamable-http" but no url`),
		Entry("a Claude plugin's .mcp.json", map[string]string{".claude-plugin/plugin.json": `{"name":"p"}`, ".mcp.json": mixed}, ".mcp.json: "+mixedMsg),
		Entry("a file a Claude plugin lists", map[string]string{".claude-plugin/plugin.json": `{"name":"p","mcpServers":"./more.json"}`, "more.json": mixed}, "more.json: "+mixedMsg),
		Entry("a Claude plugin's inline server", map[string]string{".claude-plugin/plugin.json": `{"name":"p","mcpServers":{"x":{"command":"a","url":"https://x"}}}`},
			".claude-plugin/plugin.json: mcpServers: "+mixedMsg),
	)

	It("accepts distinct keys that only look alike", func() {
		write(root, map[string]string{"mcps.json": `{"mcpServers":{"gh":{"command":"a"},"gh2":{"command":"b"}}}`})
		m, err := module.Load(root, "x", targets.IDs())
		Expect(err).NotTo(HaveOccurred())
		Expect(m.MCPServers).To(HaveLen(2))
	})

	It("accepts the same key in sibling objects", func() {
		write(root, map[string]string{"mcps.json": `{"mcpServers":{"a":{"command":"x","env":{"K":"1"}},"b":{"command":"y","env":{"K":"2"}}}}`})
		m, err := module.Load(root, "x", targets.IDs())
		Expect(err).NotTo(HaveOccurred())
		Expect(m.MCPServers).To(HaveLen(2))
	})

	It("refuses a file over 1 MiB", func() {
		write(root, map[string]string{"mcps.json": `{"mcpServers":{}}` + strings.Repeat(" ", 1<<20)})
		_, err := module.Load(root, "x", targets.IDs())
		Expect(err).To(MatchError("mcps.json is larger than 1 MiB"))
		Expect(errs.Hints(err)).NotTo(BeEmpty())
	})

	It("accepts a file of exactly 1 MiB", func() {
		doc := `{"mcpServers":{"a":{"command":"x"}}}`
		write(root, map[string]string{"mcps.json": doc + strings.Repeat(" ", 1<<20-len(doc))})
		_, err := module.Load(root, "x", targets.IDs())
		Expect(err).NotTo(HaveOccurred())
	})

	It("refuses deep nesting without exhausting the stack", func() {
		write(root, map[string]string{"mcps.json": `{"x":` + strings.Repeat("[", 200000) + strings.Repeat("]", 200000) + `}`})
		_, err := module.Load(root, "x", targets.IDs())
		Expect(err).To(MatchError("mcps.json: nested deeper than 64 levels"))
		Expect(errs.Hints(err)).NotTo(BeEmpty())
	})

	DescribeTable("refuses a file that is not what lola reads",
		func(doc, msg string) {
			write(root, map[string]string{"mcps.json": doc})
			_, err := module.Load(root, "x", targets.IDs())
			Expect(err).To(MatchError(ContainSubstring(msg)))
			Expect(errs.Hints(err)).NotTo(BeEmpty())
		},
		Entry("truncated", `{"mcpServers":{`, "mcps.json: unexpected end of JSON input"),
		Entry("two values", `{} {}`, "mcps.json: invalid character '{' after top-level value"),
		Entry("servers as an array", `{"mcpServers":[]}`, "mcps.json: mcpServers must be a JSON object"),
		Entry("args as a string", `{"mcpServers":{"a":{"command":"x","args":"-y"}}}`, "mcps.json: mcpServers.args must be a JSON array"),
		Entry("env value as a number", `{"mcpServers":{"a":{"command":"x","env":{"K":1}}}}`, "mcps.json: mcpServers.env must be a JSON string"),
	)

	It("refuses an mcps.json that is a symbolic link", func() {
		write(root, map[string]string{"real.json": `{"mcpServers":{}}`, "commands/a.md": "x"})
		Expect(os.Symlink("real.json", filepath.Join(root, "mcps.json"))).To(Succeed())
		_, err := module.Load(root, "x", targets.IDs())
		Expect(err).To(MatchError("mcps.json is a symbolic link"))
		Expect(errs.Hints(err)).To(ConsistOf("lola never follows links in a module"))
	})

	It("refuses an mcps.json that is not a regular file", func() {
		Expect(os.MkdirAll(filepath.Join(root, "mcps.json"), 0o755)).To(Succeed())
		write(root, map[string]string{"commands/a.md": "x"})
		_, err := module.Load(root, "x", targets.IDs())
		Expect(err).To(MatchError("mcps.json is not a regular file"))
		Expect(errs.Hints(err)).NotTo(BeEmpty())
	})

	It("refuses a symlinked mcps.json without reading its target", func() {
		target := filepath.Join(root, "..", "secret.json")
		Expect(os.WriteFile(target, []byte(`{"mcpServers":{"a":{"command":"x"}}}`), 0o600)).To(Succeed())
		write(root, map[string]string{"commands/a.md": "x"})
		Expect(os.Symlink(target, filepath.Join(root, "mcps.json"))).To(Succeed())
		_, err := module.Load(root, "x", targets.IDs())
		Expect(err).To(MatchError("mcps.json is a symbolic link"))
		Expect(errs.Hints(err)).NotTo(BeEmpty())
	})

	It("refuses an mcps.json that is a FIFO without blocking on it", func() {
		write(root, map[string]string{"commands/a.md": "x"})
		mkfifo(filepath.Join(root, "mcps.json"))
		done := make(chan error, 1)
		go func() { _, err := module.Load(root, "x", targets.IDs()); done <- err }()
		Eventually(done, "5s").Should(Receive(MatchError("mcps.json is not a regular file")))
	})
})
