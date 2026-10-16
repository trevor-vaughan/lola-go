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
		Entry("a bad server name", "mcp: [{name: A, server: {type: stdio, command: x}}]", `components.mcp[0]: MCP server name "A" is invalid: use 1-64 characters from a-z, 0-9, '.', '_' and '-', starting with a letter or digit, with no '..'`),
		Entry("hooks with path and hooks", "runtime_hooks: [{path: x, hooks: {}}]", "components.runtime_hooks[0]: takes path or hooks, not both"),
		Entry("hooks with neither", "runtime_hooks: [{targets: [claude-code]}]", "components.runtime_hooks[0]: needs a path or hooks"),
		Entry("hooks that are not an event map", "runtime_hooks: [{hooks: {Stop: x}}]", "components.runtime_hooks[0]: hooks must map each event to a list of matcher groups"),
		Entry("hook groups that are not objects", "runtime_hooks: [{hooks: {Stop: [1]}}]", "components.runtime_hooks[0]: hooks must map each event to a list of matcher groups"),
		Entry("hooks with a non-string key", "runtime_hooks: [{hooks: {Stop: [{1: x}]}}]", "components.runtime_hooks[0]: hooks: every key must be a string"),
		Entry("an empty inline hook set", "runtime_hooks: [{hooks: {}}]", "components.runtime_hooks[0]: hooks lists no event"),
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
	)

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
		m, err := load("components:\n  runtime_hooks: [{path: \"h\\e[2J\\tx.json\"}]\n", map[string]string{hostile: `{"hooks":{"Stop":[]}}`})
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
