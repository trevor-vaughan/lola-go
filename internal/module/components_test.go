package module_test

import (
	"encoding/json"
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

var _ = Describe("lola.yml components", func() {
	var root string
	BeforeEach(func() { root = filepath.Join(testenv.Isolate(), "mod") })

	load := func(lolaYML string, files map[string]string) (*module.Module, error) {
		files["lola.yml"] = "name: example\n" + lolaYML
		write(root, files)
		return module.Load(root, "x", targets.IDs())
	}
	names := func(m *module.Module) map[string][]string {
		out := map[string][]string{}
		for _, s := range m.Skills {
			out["skills"] = append(out["skills"], s.Name)
		}
		for _, c := range m.Commands {
			out["commands"] = append(out["commands"], c.Name)
		}
		for _, a := range m.Agents {
			out["agents"] = append(out["agents"], a.Name)
		}
		for _, s := range m.MCPServers {
			out["mcp"] = append(out["mcp"], s.Name)
		}
		for _, r := range m.Rules {
			out["rules"] = append(out["rules"], r.Name)
		}
		return out
	}
	native := map[string]string{
		"skills/native/SKILL.md": "---\ndescription: n\n---\n",
		"SKILL.md":               "---\ndescription: root\n---\n",
		"commands/native.md":     "n",
		"agents/native.md":       "---\ndescription: n\n---\n",
		"mcps.json":              `{"mcpServers":{"native":{"command":"n"}}}`,
		"rules/native.md":        "n",
		"extra/skill/SKILL.md":   "---\ndescription: e\n---\n",
		"extra/cmd.md":           "e",
		"extra/agent.md":         "---\ndescription: e\n---\n",
		"extra/servers.json":     `{"mcpServers":{"extra":{"command":"e"}}}`,
		"extra/rule.md":          "e",
		"a/build.md":             "a",
		"b/build.md":             "b",
		"more.json":              `{"mcpServers":{"extra":{"command":"m"}}}`,
	}
	clone := func(m map[string]string) map[string]string {
		out := map[string]string{}
		for k, v := range m {
			out[k] = v
		}
		return out
	}

	DescribeTable("a listed kind replaces its discovery; unlisted kinds are still discovered",
		func(listed, entry string, want []string) {
			m, err := load("components:\n  "+listed+": ["+entry+"]\n", clone(native))
			Expect(err).NotTo(HaveOccurred())
			got := names(m)
			Expect(got[listed]).To(Equal(want))
			for _, kind := range []string{"skills", "commands", "agents", "mcp", "rules"} {
				if kind == listed {
					continue
				}
				Expect(got[kind]).To(ContainElement(Or(Equal("native"), Equal("example"))), kind)
			}
		},
		Entry("skills, including the root SKILL.md", "skills", "{path: extra/skill}", []string{"skill"}),
		Entry("commands", "commands", "{path: extra/cmd.md}", []string{"cmd"}),
		Entry("agents", "agents", "{path: extra/agent.md}", []string{"agent"}),
		Entry("mcp", "mcp", "{path: extra/servers.json}", []string{"extra"}),
		Entry("rules", "rules", "{path: extra/rule.md}", []string{"rule"}),
		Entry("a kind listed with no entries", "commands", "", nil),
	)

	It("loads a module whose only artifacts are runtime hooks", func() {
		m, err := load("components:\n  runtime_hooks:\n    - {hooks: {Stop: [{hooks: [{type: command, command: echo}]}]}}\n", map[string]string{})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.RuntimeHooks).To(HaveLen(1))
	})

	It("reads an inline command, and frontmatter that overrides a file's own", func() {
		m, err := load(`components:
  commands:
    - {name: about, content: "Explain $ARGUMENTS.", frontmatter: {description: About}}
    - path: cmds/status.md
      frontmatter: {argument-hint: "[env]", description: Status now, allowed-tools: [Bash]}
`, map[string]string{"cmds/status.md": "---\n# kept\ndescription: Status\nmodel: opus\n---\nShow status.\n"})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Commands).To(HaveLen(2))
		about, status := m.Commands[0], m.Commands[1]
		Expect(about.Name).To(Equal("about"))
		Expect(about.File).To(BeEmpty())
		Expect(about.Doc.Render()).To(Equal("---\ndescription: About\n---\nExplain $ARGUMENTS."))
		Expect(status.File).To(Equal("cmds/status.md"))
		Expect(status.Doc.Frontmatter).To(Equal(map[string]any{"description": "Status now", "model": "opus",
			"argument-hint": "[env]", "allowed-tools": []any{"Bash"}}))
		Expect(status.Doc.RawFrontmatter).To(HavePrefix("# kept\ndescription: Status now\nmodel: opus\n"))
		Expect(status.Doc.Body).To(Equal("Show status.\n"))
	})

	It("reads inline MCP servers in the IR's shape, merged and sorted with a file's", func() {
		m, err := load(`components:
  mcp:
    - {path: .mcp.json}
    - {name: api, server: {type: http, url: "https://x/mcp", headers: {X-A: b}}, targets: [claude-code]}
    - {name: gh, server: {type: stdio, command: gh-mcp, args: [--stdio], env: {K: v}, cwd: "${PLUGIN_ROOT}"}}
`, map[string]string{".mcp.json": `{"mcpServers":{"zz":{"type":"sse","url":"https://x/sse"}}}`})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.MCPServers).To(Equal([]module.MCPServer{
			{Name: "api", Transport: "http", URL: "https://x/mcp", Headers: map[string]string{"X-A": "b"}, Targets: []string{"claude-code"}},
			{Name: "gh", Transport: "stdio", Command: "gh-mcp", Args: []string{"--stdio"}, Env: map[string]string{"K": "v"}, Cwd: "${PLUGIN_ROOT}"},
			{Name: "zz", Transport: "sse", URL: "https://x/sse"},
		}))
	})

	It("reads runtime hooks from a hooks file and inline, as canonical JSON, never by discovery", func() {
		m, err := load(`components:
  runtime_hooks:
    - {path: hooks/hooks.json, targets: [claude-code]}
    - hooks: {PostToolUse: [{matcher: "Write|Edit", hooks: [{type: command, command: ./fmt.sh, timeout: 30}]}]}
`, map[string]string{"hooks/hooks.json": `{"description":"d","hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"./check.sh"}]}]}}`,
			"commands/a.md": "x"})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.RuntimeHooks).To(Equal([]module.RuntimeHookSet{
			{Source: "hooks/hooks.json", Hooks: json.RawMessage(`{"PreToolUse":[{"hooks":[{"command":"./check.sh","type":"command"}],"matcher":"Bash"}]}`), Targets: []string{"claude-code"}},
			{Hooks: json.RawMessage(`{"PostToolUse":[{"hooks":[{"command":"./fmt.sh","timeout":30,"type":"command"}],"matcher":"Write|Edit"}]}`)},
		}))
		Expect(m.RuntimeHooks[1].Name()).To(Equal("inline"))

		write(root, map[string]string{"lola.yml": "name: example\n"})
		m, err = module.Load(root, "x", targets.IDs())
		Expect(err).NotTo(HaveOccurred())
		Expect(m.RuntimeHooks).To(BeEmpty())
	})

	It("keeps per-entry targets on every kind", func() {
		m, err := load(`components:
  skills: [{path: extra/skill, targets: [opencode]}]
  commands: [{path: extra/cmd.md, targets: [opencode]}]
  agents: [{path: extra/agent.md, targets: [opencode]}]
  mcp: [{path: extra/servers.json, targets: [opencode]}]
  rules: [{path: extra/rule.md, targets: [opencode, gemini-cli]}]
`, clone(native))
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Skills[0].Targets).To(Equal([]string{"opencode"}))
		Expect(m.Commands[0].Targets).To(Equal([]string{"opencode"}))
		Expect(m.Agents[0].Targets).To(Equal([]string{"opencode"}))
		Expect(m.MCPServers[0].Targets).To(Equal([]string{"opencode"}))
		Expect(m.Rules[0].Targets).To(Equal([]string{"opencode", "gemini-cli"}))
	})

	It("names a rule after its file unless the entry names it", func() {
		m, err := load("components:\n  rules: [{path: dev.getlola/INSTRUCTIONS.md, name: review}]\n",
			map[string]string{"dev.getlola/INSTRUCTIONS.md": "---\ndescription: d\n---\nBe kind.\n"})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Rules).To(Equal([]module.Rule{{Name: "review", Body: "Be kind.\n"}}))
		_, err = load("components:\n  rules: [{path: dev.getlola/INSTRUCTIONS.md}]\n", map[string]string{})
		Expect(err).To(MatchError(ContainSubstring(`components.rules[0]: rule name "INSTRUCTIONS" is invalid`)))
		Expect(errs.Hints(err)).To(ContainElement("set name: on the entry"))
	})

	It("names a skill entry for the module root after the module, without the module's own files", func() {
		m, err := load("components:\n  skills: [{path: .}]\n", map[string]string{"SKILL.md": "---\ndescription: r\n---\n"})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Skills).To(HaveLen(1))
		Expect(m.Skills[0]).To(MatchFields(IgnoreExtras, Fields{"Name": Equal("example"), "Dir": Equal(".")}))
		Expect(m.Skills[0].Exclude).To(ContainElements("lola.yml", "SKILL.md", ".claude-plugin", "plugin.json"))
	})

	DescribeTable("rejects unknown keys at every level, naming the keys known there",
		func(yml, key, known string) {
			_, err := load(yml, clone(native))
			Expect(err).To(MatchError(ContainSubstring("lola.yml")))
			Expect(errs.Hints(err)).To(ConsistOf(`remove or fix "` + key + `"; known keys there are ` + known))
		},
		Entry("a kind", "components:\n  skils: []\n", "skils", "skills, commands, agents, mcp, rules, runtime_hooks"),
		Entry("a path entry", "components:\n  agents: [{path: extra/agent.md, model: x}]\n", "model", "path, name, targets"),
		Entry("a command entry", "components:\n  commands: [{path: extra/cmd.md, source: x}]\n", "source", "path, name, content, frontmatter, targets"),
		Entry("an mcp entry", "components:\n  mcp: [{path: extra/servers.json, servers: {}}]\n", "servers", "path, name, server, targets"),
		Entry("an inline server", "components:\n  mcp: [{name: a, server: {type: stdio, command: x, transport: stdio}}]\n", "transport", "type, command, args, env, cwd, url, headers"),
		Entry("a hooks entry", "components:\n  runtime_hooks: [{hooks: {}, matcher: x}]\n", "matcher", "path, hooks, targets"),
		Entry("the author", "author: {name: a, mail: b}\n", "mail", "name, email, url"),
	)

	It("lets formats hold anything", func() {
		m, err := load("formats:\n  claude-plugin: {displayName: X, metadata: {any: [1, {deep: true}]}}\n  mine: 3\n", clone(native))
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Formats).To(HaveKey("mine"))
	})

	DescribeTable("refuses a bad path, naming the entry and the path",
		func(entry string, files map[string]string, msg string) {
			_, err := load("components:\n  "+entry+"\n", files)
			Expect(err).To(MatchError(ContainSubstring(msg)))
			Expect(errs.Hints(err)).NotTo(BeEmpty())
		},
		Entry("absolute", "commands: [{path: /etc/passwd}]", map[string]string{}, `components.commands[0]: path "/etc/passwd" must be relative and inside the module`),
		Entry("escaping", "agents: [{path: ../x.md}]", map[string]string{}, `components.agents[0]: path "../x.md" must be relative and inside the module`),
		Entry("escaping after a subdirectory", "rules: [{path: a/../../x.md}]", map[string]string{"a/b": ""}, `must be relative and inside the module`),
		Entry("empty", "skills: [{path: ''}]", map[string]string{}, `components.skills[0]: path "" must be relative`),
		Entry("missing", "commands: [{path: nope.md}]", map[string]string{}, `components.commands[0]: path "nope.md" does not exist`),
		Entry("through a file", "commands: [{path: a.md/b.md}]", map[string]string{"a.md": ""}, `path "a.md/b.md" does not exist`),
		Entry("a skill without SKILL.md", "skills: [{path: s}]", map[string]string{"s/readme.md": ""}, `components.skills[0]: s has no SKILL.md`),
	)

	DescribeTable("refuses a path of the wrong type",
		func(entry string, files map[string]string, msg string) {
			_, err := load("components:\n  "+entry+"\n", files)
			Expect(err).To(MatchError(ContainSubstring(msg)))
			Expect(errs.Hints(err)).NotTo(BeEmpty())
		},
		Entry("a directory as a command", "commands: [{path: d.md}]", map[string]string{"d.md/x": ""}, `components.commands[0]: path "d.md" is not a regular file`),
		Entry("a file as a skill", "skills: [{path: s}]", map[string]string{"s": ""}, `components.skills[0]: path "s" is not a directory`),
		Entry("a command that is not markdown", "commands: [{path: c.txt}]", map[string]string{"c.txt": ""}, `components.commands[0]: path "c.txt" does not end in .md`),
		Entry("the root as an mcp file", "mcp: [{path: .}]", map[string]string{"commands/a.md": ""}, `components.mcp[0]: path "." is not a regular file`),
	)

	It("refuses a path through a symbolic link, at any element", func() {
		write(root, map[string]string{"real/skill/SKILL.md": "---\ndescription: d\n---\n", "real/cmd.md": "x"})
		Expect(os.Symlink("real", filepath.Join(root, "linkdir"))).To(Succeed())
		Expect(os.Symlink("cmd.md", filepath.Join(root, "real", "link.md"))).To(Succeed())
		Expect(os.Symlink(filepath.Join(root, "real", "skill", "SKILL.md"), filepath.Join(root, "real", "SKILLS.md"))).To(Succeed())
		for entry, msg := range map[string]string{
			"skills: [{path: linkdir/skill}]":  `components.skills[0]: path "linkdir/skill" goes through the symbolic link linkdir`,
			"commands: [{path: real/link.md}]": `components.commands[0]: path "real/link.md" goes through the symbolic link real/link.md`,
		} {
			_, err := load("components:\n  "+entry+"\n", map[string]string{})
			Expect(err).To(MatchError(msg), entry)
			Expect(errs.Hints(err)).To(ConsistOf(ContainSubstring("never follows links")))
		}
		Expect(os.Remove(filepath.Join(root, "real", "skill", "SKILL.md"))).To(Succeed())
		Expect(os.Symlink("../cmd.md", filepath.Join(root, "real", "skill", "SKILL.md"))).To(Succeed())
		_, err := load("components:\n  skills: [{path: real/skill}]\n", map[string]string{})
		Expect(err).To(MatchError("components.skills[0]: real/skill has no SKILL.md"))
	})

	DescribeTable("refuses an entry that is not one of the shapes its kind takes",
		func(entry, msg string) {
			_, err := load("components:\n  "+entry+"\n", clone(native))
			Expect(err).To(MatchError(msg))
			Expect(errs.Hints(err)).NotTo(BeEmpty())
		},
		Entry("a command with path and content", "commands: [{path: extra/cmd.md, content: x}]", "components.commands[0]: takes path or content, not both"),
		Entry("a command with neither", "commands: [{name: x}]", "components.commands[0]: needs a path or content"),
		Entry("an inline command without a name", "commands: [{content: x}]", "components.commands[0]: an inline command needs a name"),
		Entry("an mcp entry with path and server", "mcp: [{path: extra/servers.json, name: a, server: {type: stdio, command: x}}]", "components.mcp[0]: takes a path, or a name and a server, not both"),
		Entry("an mcp server without a name", "mcp: [{server: {type: stdio, command: x}}]", "components.mcp[0]: needs a path, or a name and a server"),
		Entry("a stdio server without a command", "mcp: [{name: a, server: {type: stdio}}]", "components.mcp[0]: a stdio server needs a command"),
		Entry("a stdio server with a url", "mcp: [{name: a, server: {type: stdio, command: x, url: 'https://x'}}]", "components.mcp[0]: a stdio server has no url or headers"),
		Entry("an http server without a url", "mcp: [{name: a, server: {type: http}}]", "components.mcp[0]: an http server needs a url"),
		Entry("an sse server with a command", "mcp: [{name: a, server: {type: sse, url: 'https://x', command: x}}]", "components.mcp[0]: an sse server has no command, args, env or cwd"),
		Entry("an unknown server type", "mcp: [{name: a, server: {type: ws, url: 'wss://x'}}]", `components.mcp[0]: server type "ws" is unknown`),
		Entry("a server without a type", "mcp: [{name: a, server: {command: x}}]", `components.mcp[0]: server type "" is unknown`),
		Entry("a bad server name", "mcp: [{name: A, server: {type: stdio, command: x}}]", `components.mcp[0]: MCP server name "A" is invalid: A is uppercase; use 1-64 characters from a-z, 0-9, '.', '_' and '-', starting with a letter or digit, with no '..'`),
		Entry("hooks with path and hooks", "runtime_hooks: [{path: x, hooks: {}}]", "components.runtime_hooks[0]: takes path or hooks, not both"),
		Entry("hooks with neither", "runtime_hooks: [{targets: [claude-code]}]", "components.runtime_hooks[0]: needs a path or hooks"),
		Entry("hooks that are not an event map", "runtime_hooks: [{hooks: {Stop: x}}]", "components.runtime_hooks[0]: hooks must map each event to a list of matcher groups"),
		Entry("hook groups that are not objects", "runtime_hooks: [{hooks: {Stop: [1]}}]", "components.runtime_hooks[0]: hooks must map each event to a list of matcher groups"),
		Entry("hooks with a non-string key", "runtime_hooks: [{hooks: {Stop: [{1: x}]}}]", "components.runtime_hooks[0]: hooks: every key must be a string"),
		Entry("an empty inline hook set", "runtime_hooks: [{hooks: {}}]", "components.runtime_hooks[0]: hooks lists no event"),
		Entry("an inline event with no matcher group", "runtime_hooks: [{hooks: {Stop: []}}]", `components.runtime_hooks[0]: hooks event "Stop" lists no matcher group`),
	)

	DescribeTable("refuses a hooks file that is not Claude's hooks file shape",
		func(doc, msg string) {
			_, err := load("components:\n  runtime_hooks: [{path: h.json}]\n", map[string]string{"h.json": doc})
			Expect(err).To(MatchError(msg))
		},
		Entry("no wrapper", `{"PreToolUse":[]}`, `components.runtime_hooks[0]: h.json has no "hooks" object`),
		Entry("an empty hook set", `{"hooks":{}}`, "components.runtime_hooks[0]: hooks lists no event"),
		Entry("a list", `[]`, "h.json must be a JSON object"),
		Entry("a wrong event value", `{"hooks":{"Stop":{}}}`, "components.runtime_hooks[0]: hooks must map each event to a list of matcher groups"),
		Entry("a repeated event", `{"hooks":{"Stop":[],"Stop":[]}}`, `h.json: key "Stop" appears twice in /hooks`),
		Entry("an event with no matcher group", `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"x"}]}],"Stop":[]}}`,
			`components.runtime_hooks[0]: hooks event "Stop" lists no matcher group`),
		Entry("an empty event with control characters in its name", "{\"hooks\":{\"S\\u001b[2J\":[]}}",
			`components.runtime_hooks[0]: hooks event "S\x1b[2J" lists no matcher group`),
	)

	DescribeTable("refuses matcher groups and handlers of the wrong shape, naming where",
		func(groups, msg string) {
			_, err := load("components:\n  runtime_hooks: [{path: h.json}]\n", map[string]string{"h.json": `{"hooks":{"PreToolUse":` + groups + `}}`})
			Expect(err).To(MatchError("components.runtime_hooks[0]: hooks /PreToolUse/" + msg))
			Expect(errs.Hints(err)).NotTo(BeEmpty())
		},
		Entry("a matcher that is not a string", `[{"matcher":1,"hooks":[{"type":"command","command":"x"}]}]`, "0: matcher must be a string"),
		Entry("a group without hooks", `[{"matcher":"Bash"}]`, "0: hooks must be a list of one or more handlers"),
		Entry("a group with no handler", `[{"hooks":[]}]`, "0: hooks must be a list of one or more handlers"),
		Entry("hooks that are not a list", `[{"hooks":{"type":"command"}}]`, "0: hooks must be a list of one or more handlers"),
		Entry("a handler that is not an object", `[{"hooks":["x"]}]`, "0: hooks must be a list of one or more handlers"),
		Entry("a handler without a type", `[{"hooks":[{"command":"x"}]}]`, "0/hooks/0: type must be a non-empty string"),
		Entry("a blank type", `[{"hooks":[{"type":"","command":"x"}]}]`, "0/hooks/0: type must be a non-empty string"),
		Entry("a command handler without a command", `[{"hooks":[{"type":"command"}]}]`, "0/hooks/0: command must be a non-empty string"),
		Entry("a command that is not a string", `[{"hooks":[{"type":"command","command":["x"]}]}]`, "0/hooks/0: command must be a non-empty string"),
		Entry("args that are not strings", `[{"hooks":[{"type":"command","command":"x","args":["a",1]}]}]`, "0/hooks/0: args must be a list of strings"),
		Entry("args that are null", `[{"hooks":[{"type":"command","command":"x","args":null}]}]`, "0/hooks/0: args must be a list of strings"),
		Entry("a shell that is not a string", `[{"hooks":[{"type":"command","command":"x","shell":true}]}]`, "0/hooks/0: shell must be a string"),
		Entry("an if that is not a string", `[{"hooks":[{"type":"command","command":"x","if":1}]}]`, "0/hooks/0: if must be a string"),
		Entry("an async that is not a boolean", `[{"hooks":[{"type":"command","command":"x","async":"yes"}]}]`, "0/hooks/0: async must be true or false"),
		Entry("an http handler without a url", `[{"hooks":[{"type":"http"}]}]`, "0/hooks/0: url must be a non-empty string"),
		Entry("headers that are not strings", `[{"hooks":[{"type":"http","url":"https://x","headers":{"A":1}}]}]`, "0/hooks/0: headers must map names to strings"),
		Entry("allowedEnvVars that are not strings", `[{"hooks":[{"type":"http","url":"https://x","allowedEnvVars":"A"}]}]`, "0/hooks/0: allowedEnvVars must be a list of strings"),
		Entry("an mcp_tool handler without a tool", `[{"hooks":[{"type":"mcp_tool","server":"s"}]}]`, "0/hooks/0: tool must be a non-empty string"),
		Entry("an mcp_tool handler without a server", `[{"hooks":[{"type":"mcp_tool","tool":"t"}]}]`, "0/hooks/0: server must be a non-empty string"),
		Entry("a prompt handler without a prompt", `[{"hooks":[{"type":"prompt"}]}]`, "0/hooks/0: prompt must be a non-empty string"),
		Entry("an agent handler with a blank prompt", `[{"hooks":[{"type":"agent","prompt":""}]}]`, "0/hooks/0: prompt must be a non-empty string"),
		Entry("the second group", `[{"hooks":[{"type":"command","command":"x"}]},{"hooks":[{"type":"command","command":"x"},{"type":"agent"}]}]`,
			"1/hooks/1: prompt must be a non-empty string"),
	)

	It("names a hostile event safely when its group is wrong", func() {
		_, err := load("components:\n  runtime_hooks: [{path: h.json}]\n", map[string]string{"h.json": `{"hooks":{"a/b~\u001b[2J":[{"hooks":[]}]}}`})
		Expect(err).To(MatchError("components.runtime_hooks[0]: hooks /a~1b~0?[2J/0: hooks must be a list of one or more handlers"))
	})

	It("keeps every handler type, and fields lola does not know, as authored", func() {
		doc := `{"hooks":{"PostToolUse":[{"matcher":"Write|Edit","hooks":[` +
			`{"type":"command","command":"x","args":[],"async":true,"timeout":30,"statusMessage":"s"},` +
			`{"type":"http","url":"https://h","headers":{"A":"$T"},"allowedEnvVars":["T"]},` +
			`{"type":"mcp_tool","server":"s","tool":"t","input":{"p":"${tool_input.file_path}"}},` +
			`{"type":"prompt","prompt":"p","model":"m"},{"type":"agent","prompt":"a"},{"type":"future","x":1}]}]}}`
		m, err := load("components:\n  runtime_hooks: [{path: h.json}]\n", map[string]string{"h.json": doc})
		Expect(err).NotTo(HaveOccurred())
		Expect(string(m.RuntimeHooks[0].Hooks)).To(MatchJSON(doc[len(`{"hooks":`) : len(doc)-1]))
	})

	DescribeTable("checks frontmatter hooks in skills, commands and agents like runtime hooks",
		func(files map[string]string, msg string) {
			_, err := load("", files)
			Expect(err).To(MatchError(msg))
			Expect(errs.Hints(err)).NotTo(BeEmpty())
		},
		Entry("a skill's hooks that are not a mapping", map[string]string{"skills/s/SKILL.md": "---\ndescription: d\nhooks: echo\n---\n"},
			"skill s: frontmatter hooks: hooks must map each event to a list of matcher groups"),
		Entry("a case variant of the key", map[string]string{"commands/c.md": "---\nHooks: {Stop: []}\n---\nC\n"},
			`command c: frontmatter Hooks: hooks event "Stop" lists no matcher group`),
		Entry("an agent's handler without a command", map[string]string{"agents/a.md": "---\ndescription: d\nhooks: {Stop: [{hooks: [{type: command}]}]}\n---\nA\n"},
			"agent a: frontmatter hooks: hooks /Stop/0/hooks/0: command must be a non-empty string"),
		Entry("a non-string key", map[string]string{"commands/c.md": "---\nhooks: {1: []}\n---\nC\n"},
			"command c: frontmatter hooks: every key must be a string"),
		Entry("a first line of --- that does not open frontmatter", map[string]string{"commands/c.md": "--- \nhooks: {Stop: [{hooks: [{type: prompt, prompt: p}]}]}\n---\nC\n"},
			`commands/c.md: the first line starts with "---" but is not exactly "---", so lola cannot read the frontmatter an assistant might`),
	)

	DescribeTable("refuses hooks in a .md file under a command's directory, which Claude Code reads as a command of its own",
		func(rel string) {
			_, err := load("", map[string]string{"commands/deploy.md": "Deploy\n", "commands/deploy/run.sh": "#!/bin/sh\n",
				rel: "---\nHOOKS: {Stop: [{hooks: [{type: prompt, prompt: p}]}]}\n---\nN\n"})
			Expect(err).To(MatchError("command deploy: " + rel + ": frontmatter HOOKS: Claude Code reads this file as a command, so lola would install its hooks without asking"))
			Expect(errs.Hints(err)).To(ConsistOf("put hooks in the command itself"))
		},
		Entry("beside the command's files", "commands/deploy/notes.md"),
		Entry("in a subdirectory, with an upper-case suffix", "commands/deploy/sub/notes.MD"),
	)

	It("keeps a command's directory whose .md files have no hooks, recording each .md file Claude Code reads as a command", func() {
		m, err := load("", map[string]string{"commands/deploy.md": "Deploy\n", "commands/deploy/notes.md": "---\ndescription: n\n---\nN\n",
			"commands/deploy/sub/b.MD": "B\n", "commands/deploy/data.txt": "x\n"})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Commands[0].SidecarDir).To(Equal("commands/deploy"))
		Expect(m.Commands[0].Sidecars).To(HaveExactElements(
			MatchFields(IgnoreExtras, Fields{"File": Equal("commands/deploy/notes.md"), "Doc": HaveField("Frontmatter", Equal(map[string]any{"description": "n"}))}),
			MatchFields(IgnoreExtras, Fields{"File": Equal("commands/deploy/sub/b.MD"), "Doc": HaveField("Body", "B\n")})))
	})

	It("keeps valid frontmatter hooks as written", func() {
		m, err := load("", map[string]string{"skills/s/SKILL.md": "---\ndescription: d\nhooks: {PreToolUse: [{matcher: Bash, hooks: [{type: command, command: ./x}]}]}\n---\n"})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Skills[0].Doc.Frontmatter).To(HaveKey("hooks"))
	})

	DescribeTable("checks the MCP servers in agent frontmatter, which Claude Code starts with the agent",
		func(fm, msg string, hints ...string) {
			_, err := load("", map[string]string{"agents/a.md": "---\ndescription: d\n" + fm + "---\nA\n"})
			Expect(err).To(MatchError(msg))
			Expect(errs.Hints(err)).To(ConsistOf(hints))
		},
		Entry("a value that is not a list", "mcpServers: {pw: {command: npx}}\n",
			"agent a: frontmatter mcpServers: must be a list of server names and inline servers",
			"write each server as a list item: - github names a configured server, - pw: {command: npx} defines one"),
		Entry("an empty list item", "mcpServers: [github, '  ']\n",
			"agent a: frontmatter mcpServers[1]: a server name must not be empty", "name a configured MCP server, or remove the item"),
		Entry("a list item that is neither a name nor a mapping", "mcpServers: [[github]]\n",
			"agent a: frontmatter mcpServers[0]: must be a server name or one server's definition",
			"write each server as a list item: - github names a configured server, - pw: {command: npx} defines one"),
		Entry("a mapping of more than one server", "mcpServers:\n  - pw: {command: npx}\n    db: {url: 'https://db.example'}\n",
			"agent a: frontmatter mcpServers[0]: an inline server must map exactly one server name to its definition",
			"write each server as its own list item"),
		Entry("a server mixing stdio and remote fields", "McpServers:\n  - pw: {command: npx, url: 'https://pw.example'}\n",
			`agent a: frontmatter McpServers[0]: server "pw" mixes stdio fields (command, args, env, cwd) with remote ones (url, headers)`,
			"keep command, args, env and cwd for a server lola starts, or url and headers for one it connects to"),
		Entry("a type contradicting the fields", "mcpServers:\n  - pw: {type: http, command: npx}\n",
			`agent a: frontmatter mcpServers[0]: server "pw" has type "http", so it takes no command, args, env or cwd`,
			`remove them, or set type to "stdio"`),
		Entry("a bad server name", "mcpServers:\n  - \"Bad/\\e\": {command: npx}\n",
			`agent a: frontmatter mcpServers[0]: MCP server name "Bad/?" is invalid: B is uppercase, '/' and '\x1b' are not allowed; use 1-64 characters from a-z, 0-9, '.', '_' and '-', starting with a letter or digit, with no '..'`,
			"rename the server"),
		Entry("a definition that is not a mapping", "mcpServers:\n  - pw: npx\n",
			`agent a: frontmatter mcpServers[0]: server "pw" must be a mapping of its fields`,
			"give it command and args for a server Claude Code starts, or url for one it connects to"),
		Entry("fields of the wrong kind", "mcpServers:\n  - pw: {command: npx, args: [1, {x: y}]}\n",
			`agent a: frontmatter mcpServers[0]: server "pw": type, command, cwd and url must be strings, args a list of strings, and env and headers must map names to strings`,
			"fix the field's value"),
		Entry("a non-string key", "mcpServers:\n  - pw: {command: npx, env: {1: x}}\n",
			"agent a: frontmatter mcpServers: every key must be a string", "quote keys that look like numbers or booleans"),
		Entry("a null item", "mcpServers: [github, null]\n",
			"agent a: frontmatter mcpServers[1]: must be a server name or one server's definition",
			"write each server as a list item: - github names a configured server, - pw: {command: npx} defines one"),
		Entry("an alias to a definition, checked like the definition", "x: &s {pw: {command: npx, url: 'https://pw.example'}}\nmcpServers: [*s]\n",
			`agent a: frontmatter mcpServers[0]: server "pw" mixes stdio fields (command, args, env, cwd) with remote ones (url, headers)`,
			"keep command, args, env and cwd for a server lola starts, or url and headers for one it connects to"),
		Entry("a server defined twice", "mcpServers:\n  - pw: {command: npx}\n  - pw: {url: 'https://pw.example'}\n",
			`agent a: frontmatter mcpServers[1]: server "pw" is defined more than once`, "rename one of the servers"),
		Entry("a server defined twice under case variants of the key", "McpServers:\n  - pw: {command: npx}\nmcpServers:\n  - pw: {command: npx}\n",
			`agent a: frontmatter mcpServers[0]: server "pw" is defined more than once`, "rename one of the servers"),
		Entry("a definition with the name of a configured server listed after it", "mcpServers:\n  - github: {command: npx}\n  - github\n",
			`agent a: frontmatter mcpServers[1]: server "github" is both defined here and named as a configured server`,
			"rename the inline server, or remove the name"),
		Entry("a definition with the name of a configured server listed before it", "mcpServers:\n  - github\n  - github: {command: npx}\n",
			`agent a: frontmatter mcpServers[1]: server "github" is both defined here and named as a configured server`,
			"rename the inline server, or remove the name"),
	)

	It("keeps an agent's MCP servers as written: names of configured servers, and inline definitions", func() {
		fm := "mcpServers:\n  - github\n  - pw: {type: stdio, command: npx, args: [-y, '@playwright/mcp'], env: {A: b}, timeout: 5}\n"
		m, err := load("", map[string]string{"agents/a.md": "---\ndescription: d\n" + fm + "---\nA\n"})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Agents[0].Doc.Frontmatter["mcpServers"]).To(Equal([]any{"github",
			map[string]any{"pw": map[string]any{"type": "stdio", "command": "npx", "args": []any{"-y", "@playwright/mcp"}, "env": map[string]any{"A": "b"}, "timeout": 5}}}))
	})

	It("refuses a hooks file over 1 MiB", func() {
		_, err := load("components:\n  runtime_hooks: [{path: h.json}]\n", map[string]string{"h.json": `{"hooks":{}}` + strings.Repeat(" ", 1<<20)})
		Expect(err).To(MatchError("h.json is larger than 1 MiB"))
	})

	It("refuses an unknown or empty per-entry targets list", func() {
		_, err := load("components:\n  commands: [{path: extra/cmd.md, targets: [vim]}]\n", clone(native))
		Expect(err).To(MatchError(`components.commands[0]: unknown target "vim"`))
		Expect(errs.Hints(err)).To(ConsistOf("valid targets: claude-code, gemini-cli, opencode"))
		_, err = load("components:\n  rules: [{path: extra/rule.md, targets: []}]\n", clone(native))
		Expect(err).To(MatchError("components.rules[0]: targets: lists no target"))
	})

	DescribeTable("refuses two artifacts of one kind and name whose targets overlap, naming both",
		func(yml string, msg string) {
			_, err := load(yml, clone(native))
			Expect(err).To(MatchError(msg))
			Expect(errs.Hints(err)).To(ConsistOf("rename one with name:, or give them targets: that do not overlap"))
		},
		Entry("two skills", "components:\n  skills: [{path: extra/skill, name: s}, {path: skills/native, name: s}]\n",
			`components: two skills are named "s": extra/skill and skills/native`),
		Entry("nested commands with one base name", "components:\n  commands: [{path: a/build.md}, {path: b/build.md}]\n",
			`components: two commands are named "build": a/build.md and b/build.md`),
		Entry("a file and an inline command", "components:\n  commands: [{path: extra/cmd.md}, {name: cmd, content: x, targets: [opencode]}]\n",
			`components: two commands are named "cmd": extra/cmd.md and inline entry components.commands[1]`),
		Entry("an agent everywhere and one for a target", "components:\n  agents: [{path: extra/agent.md, targets: [claude-code]}, {path: agents/native.md, name: agent}]\n",
			`components: two agents are named "agent": extra/agent.md and agents/native.md`),
		Entry("rules for overlapping targets", "components:\n  rules: [{path: extra/rule.md, targets: [opencode, claude-code]}, {path: rules/native.md, name: rule, targets: [claude-code]}]\n",
			`components: two rules are named "rule": extra/rule.md and rules/native.md`),
		Entry("an MCP server in a file and inline", "components:\n  mcp: [{path: extra/servers.json}, {name: extra, server: {type: stdio, command: x}}]\n",
			`components: two MCP servers are named "extra": server "extra" in extra/servers.json and inline entry components.mcp[1]`),
		Entry("an MCP server in two files", "components:\n  mcp: [{path: extra/servers.json}, {path: more.json}]\n",
			`components: two MCP servers are named "extra": server "extra" in extra/servers.json and server "extra" in more.json`),
	)

	It("accepts one name on targets that do not overlap, also through the module's targets", func() {
		m, err := load(`targets: [claude-code, opencode]
components:
  commands: [{path: a/build.md, targets: [claude-code]}, {path: b/build.md, targets: [opencode]}]
  mcp: [{path: extra/servers.json, targets: [gemini-cli]}, {path: more.json}]
`, clone(native))
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Commands).To(HaveLen(2))
		Expect(m.MCPServers).To(HaveLen(2))
	})

	It("records the format an adapter wrote and refuses any other", func() {
		m, err := load("format: claude-plugin\n", clone(native))
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Format).To(Equal("claude-plugin"))
		_, err = load("format: lola\n", clone(native))
		Expect(err).To(MatchError(`lola.yml: format "lola" is unknown`))
		Expect(errs.Hints(err)).To(ConsistOf(ContainSubstring("claude-plugin or agent-plugins")))
	})

	It("parses an inline command's content like a file's, then applies the entry's frontmatter", func() {
		text := "---\nbogus-key: 1\nmodel: m\n---\nhi\n"
		m, err := load("components:\n  commands:\n    - {name: inline, content: \"---\\nbogus-key: 1\\nmodel: m\\n---\\nhi\\n\", frontmatter: {description: d}}\n    - {path: f.md, frontmatter: {description: d}}\n",
			map[string]string{"f.md": text})
		Expect(err).NotTo(HaveOccurred())
		file, inline := m.Commands[0], m.Commands[1]
		Expect(inline.Name).To(Equal("inline"))
		Expect(inline.Doc.Frontmatter).To(Equal(map[string]any{"bogus-key": 1, "model": "m", "description": "d"}))
		Expect(inline.Doc.Body).To(Equal("hi\n"))
		Expect(inline.Doc).To(Equal(file.Doc))
		_, err = load("components:\n  commands: [{name: bad, content: \"---\\nkey: [unclosed\\n---\\nx\"}]\n", map[string]string{})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(HavePrefix("components.commands[0]: "))
	})

	It("prints hostile hook-file and document names with control characters replaced", func() {
		hostile := "h\x1b[2J\tx.json"
		m, err := load("components:\n  runtime_hooks: [{path: \"h\\e[2J\\tx.json\"}]\n", map[string]string{hostile: `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"x"}]}]}}`})
		Expect(err).NotTo(HaveOccurred())
		for _, shown := range []string{m.RuntimeHooks[0].Source, m.RuntimeHooks[0].Name()} {
			Expect(shown).To(Equal("h?[2J?x.json"))
		}
		_, err = load("components:\n  agents: [{path: \"e\\e[31mX.md\", name: x}]\n", map[string]string{"e\x1b[31mX.md": "---\nkey: [unclosed\n---\n"})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).NotTo(ContainSubstring("\x1b"))
		Expect(err.Error()).To(ContainSubstring("e?[31mX.md"))
	})

	It("prints hostile names from components with control characters replaced", func() {
		_, err := load("components:\n  commands: [{path: \"\\e[2Jx.md\"}]\n", map[string]string{})
		Expect(err).To(MatchError(`components.commands[0]: path "?[2Jx.md" does not exist`))
	})
})

var _ = Describe("Normalize with components", func() {
	var root string
	BeforeEach(func() { root = filepath.Join(testenv.Isolate(), "mod") })

	It("writes the components section back, keeping a kind listed with no entries", func() {
		write(root, map[string]string{
			".lola.yaml": `name: example
format: agent-plugins
components:
  skills: []
  commands: [{name: about, content: hi, frontmatter: {description: d}, targets: [claude-code]}]
  mcp: [{name: gh, server: {type: stdio, command: gh, env: {PLUGIN_ROOT: "${PLUGIN_ROOT}"}}}]
  runtime_hooks: [{hooks: {Stop: [{hooks: [{type: command, command: x}]}]}}]
`,
			"skills/native/SKILL.md": "---\ndescription: n\n---\n",
		})
		m, err := module.Load(root, "x", targets.IDs())
		Expect(err).NotTo(HaveOccurred())
		Expect(module.Normalize(root, m)).To(Succeed())
		again, err := module.Load(root, "x", targets.IDs())
		Expect(err).NotTo(HaveOccurred())
		Expect(again.Manifest).To(Equal("lola.yml"))
		Expect(again.Format).To(Equal("agent-plugins"))
		Expect(again.Skills).To(BeEmpty())
		Expect(again.Commands).To(Equal(m.Commands))
		Expect(again.MCPServers).To(Equal(m.MCPServers))
		Expect(again.RuntimeHooks).To(Equal(m.RuntimeHooks))
		b, err := os.ReadFile(filepath.Join(root, "lola.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(ContainSubstring("    skills: []\n"))
	})
})
