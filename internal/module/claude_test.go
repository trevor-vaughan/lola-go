package module_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/target/claude"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("the Claude Code plugin adapter", func() {
	var root string
	BeforeEach(func() { root = filepath.Join(testenv.Isolate(), "plugin") })

	detect := func(manifest string, files map[string]string) (*module.Module, error) {
		files[".claude-plugin/plugin.json"] = manifest
		write(root, files)
		return module.Detect(root, "fallback", targets.IDs())
	}
	paths := func(m *module.Module) map[string][]string {
		out := map[string][]string{}
		for _, s := range m.Skills {
			out["skills"] = append(out["skills"], s.Name+"="+s.Dir)
		}
		for _, c := range m.Commands {
			out["commands"] = append(out["commands"], c.Name+"="+c.File)
		}
		for _, a := range m.Agents {
			out["agents"] = append(out["agents"], a.Name+"="+a.File)
		}
		for _, s := range m.MCPServers {
			out["mcp"] = append(out["mcp"], s.Name)
		}
		for _, h := range m.RuntimeHooks {
			out["hooks"] = append(out["hooks"], h.Name())
		}
		return out
	}

	It("maps the metadata and keeps what Claude Code does not load with under formats", func() {
		m, err := detect(`{"$schema":"https://x/s.json","name":"deploy-tools","displayName":"Deploy Tools","version":"1.2.0",
			"description":"d","author":{"name":"A","email":"a@x","url":"https://a"},"homepage":"https://h","repository":"https://r",
			"license":"MIT","keywords":["ci"],"metadata":{"id":"c-1"},"defaultEnabled":true}`,
			map[string]string{"commands/a.md": "x"})
		Expect(err).NotTo(HaveOccurred())
		Expect(m).To(PointTo(MatchFields(IgnoreExtras, Fields{"Name": Equal("deploy-tools"), "Version": Equal("1.2.0"),
			"Description": Equal("d"), "Author": Equal(&module.Author{Name: "A", Email: "a@x", URL: "https://a"}),
			"Homepage": Equal("https://h"), "Repository": Equal("https://r"), "License": Equal("MIT"), "Keywords": Equal([]string{"ci"}),
			"Format": Equal("claude-plugin"), "Manifest": Equal(".claude-plugin/plugin.json")})))
		Expect(m.Formats).To(Equal(map[string]any{"claude-plugin": map[string]any{"$schema": "https://x/s.json",
			"displayName": "Deploy Tools", "metadata": map[string]any{"id": "c-1"}, "defaultEnabled": true}}))
		Expect(m.Warnings).To(BeEmpty())
	})

	It("adds listed skill directories to skills/, one entry per skill", func() {
		m, err := detect(`{"name":"p","skills":["./extra/","./one","./skills/a"]}`, map[string]string{
			"skills/a/SKILL.md":     "---\ndescription: a\n---\n",
			"skills/empty/notes.md": "",
			"extra/b/SKILL.md":      "---\ndescription: b\n---\n",
			"extra/c/SKILL.md":      "---\ndescription: c\n---\n",
			"one/SKILL.md":          "---\ndescription: one\n---\n",
			"one/ref/x.md":          "",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(paths(m)["skills"]).To(Equal([]string{"a=skills/a", "b=extra/b", "c=extra/c", "one=one"}))
		Expect(m.Warnings).To(ConsistOf("skills/empty has no SKILL.md; skipped"))
	})

	It("loads a plugin with only a root SKILL.md as one skill, and the root when listed as .", func() {
		m, err := detect(`{"name":"solo"}`, map[string]string{"SKILL.md": "---\ndescription: s\n---\n"})
		Expect(err).NotTo(HaveOccurred())
		Expect(paths(m)["skills"]).To(Equal([]string{"solo=."}))
		m, err = detect(`{"name":"solo","skills":"."}`, map[string]string{"skills/a/SKILL.md": "---\ndescription: a\n---\n"})
		Expect(err).NotTo(HaveOccurred())
		Expect(paths(m)["skills"]).To(Equal([]string{"a=skills/a", "solo=."}))
		Expect(m.Skills[1].Exclude).To(ContainElements(".claude-plugin", "skills", "commands"))
	})

	It("reads the default commands/ and agents/ recursively when the manifest does not set them", func() {
		m, err := detect(`{"name":"p"}`, map[string]string{
			"commands/a.md": "a", "commands/sub/b.md": "b", "commands/sub/notes.txt": "",
			"agents/r.md": "---\ndescription: r\n---\n", "agents/team/q.md": "---\ndescription: q\n---\n",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(paths(m)["commands"]).To(Equal([]string{"a=commands/a.md", "b=commands/sub/b.md"}))
		Expect(paths(m)["agents"]).To(Equal([]string{"q=agents/team/q.md", "r=agents/r.md"}))
	})

	It("replaces commands/ and agents/ with the listed paths, warning that the default is ignored", func() {
		m, err := detect(`{"name":"p","commands":["./extras/","./one.md"],"agents":"./custom/rev.md"}`, map[string]string{
			"commands/a.md": "a", "extras/x.md": "x", "extras/deep/y.md": "y", "one.md": "1",
			"agents/r.md": "---\ndescription: r\n---\n", "custom/rev.md": "---\ndescription: rev\n---\n",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(paths(m)["commands"]).To(Equal([]string{"one=one.md", "x=extras/x.md", "y=extras/deep/y.md"}))
		Expect(paths(m)["agents"]).To(Equal([]string{"rev=custom/rev.md"}))
		Expect(m.Warnings).To(ConsistOf(
			"the default commands/ directory is ignored because .claude-plugin/plugin.json sets commands",
			"the default agents/ directory is ignored because .claude-plugin/plugin.json sets agents"))
	})

	It("does not warn when the listed paths are inside the default directory, and reads a path listed twice once", func() {
		m, err := detect(`{"name":"p","commands":["./commands/a.md","./commands/"]}`, map[string]string{"commands/a.md": "a", "commands/b.md": "b"})
		Expect(err).NotTo(HaveOccurred())
		Expect(paths(m)["commands"]).To(Equal([]string{"a=commands/a.md", "b=commands/b.md"}))
		Expect(m.Warnings).To(BeEmpty())
	})

	It("turns the commands object map into inline commands with frontmatter", func() {
		m, err := detect(`{"name":"p","commands":{
			"status":{"source":"./commands/status.md","argumentHint":"[env]","allowedTools":["Bash"]},
			"about":{"content":"Explain what this plugin provides.","description":"About","model":"haiku"}}}`,
			map[string]string{"commands/status.md": "---\ndescription: Status\n---\nShow it.\n", "commands/other.md": "o"})
		Expect(err).NotTo(HaveOccurred())
		Expect(paths(m)["commands"]).To(Equal([]string{"about=", "status=commands/status.md"}))
		Expect(m.Commands[0].Doc.Frontmatter).To(Equal(map[string]any{"description": "About", "model": "haiku"}))
		Expect(m.Commands[0].Doc.Body).To(Equal("Explain what this plugin provides."))
		Expect(m.Commands[1].Doc.Frontmatter).To(Equal(map[string]any{"description": "Status", "argument-hint": "[env]", "allowed-tools": []any{"Bash"}}))
	})

	It("refuses commands whose names collide, as nested directories do", func() {
		_, err := detect(`{"name":"p"}`, map[string]string{"commands/a/build.md": "a", "commands/b/build.md": "b"})
		Expect(err).To(MatchError(`.claude-plugin/plugin.json: commands/a/build.md and commands/b/build.md are both command "build"; lola gives each command one name, with no namespace`))
		Expect(errs.Hints(err)).To(ConsistOf("rename one of them"))
		Expect(os.RemoveAll(root)).To(Succeed())
		_, err = detect(`{"name":"p","agents":["./x/r.md","./y/r.md"]}`, map[string]string{"x/r.md": "", "y/r.md": ""})
		Expect(err).To(MatchError(ContainSubstring(`x/r.md and y/r.md are both agent "r"`)))
		Expect(os.RemoveAll(root)).To(Succeed())
		_, err = detect(`{"name":"p","skills":"./more"}`, map[string]string{"skills/s/SKILL.md": "", "more/s/SKILL.md": ""})
		Expect(err).To(MatchError(ContainSubstring(`skills/s and more/s are both skill "s"`)))
	})

	It("merges .mcp.json, listed files and inline servers, keeping bundles out with a warning", func() {
		m, err := detect(`{"name":"p","mcpServers":["./mcp/more.json",{"deploy":{"command":"node","args":["${CLAUDE_PLUGIN_ROOT}/server.js"]}},
			"./tools.mcpb","https://example.com/s.dxt"]}`, map[string]string{
			".mcp.json":     `{"mcpServers":{"gh":{"command":"gh-mcp","env":{"D":"${CLAUDE_PLUGIN_DATA}"}}}}`,
			"mcp/more.json": `{"mcpServers":{"web":{"type":"http","url":"https://x/mcp"}}}`,
			"mcps.json":     `{"mcpServers":{"native":{"command":"n"}}}`,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(paths(m)["mcp"]).To(Equal([]string{"deploy", "gh", "web"}))
		Expect(m.MCPServers[0]).To(Equal(module.MCPServer{Name: "deploy", Transport: "stdio", Command: "node", Args: []string{"${CLAUDE_PLUGIN_ROOT}/server.js"}}))
		Expect(m.Formats["claude-plugin"]).To(Equal(map[string]any{"mcpServers": []any{"./tools.mcpb", "https://example.com/s.dxt"}}))
		Expect(m.Warnings).To(ConsistOf(
			".claude-plugin/plugin.json: mcpServers: the MCP bundle ./tools.mcpb is not supported: lola does not install it; it is kept under formats.claude-plugin",
			".claude-plugin/plugin.json: mcpServers: the MCP bundle https://example.com/s.dxt is not supported: lola does not install it; it is kept under formats.claude-plugin"))

		ins, _, err := claude.Target{}.Plan(m, target.PlanContext{Scope: target.Project, PluginRoot: "/copies/p", PluginData: "/data/p"})
		Expect(err).NotTo(HaveOccurred())
		Expect(ins).To(ContainElement(HaveField("Value", MatchJSON(`{"command":"node","args":["/copies/p/server.js"]}`))))
		Expect(ins).To(ContainElement(HaveField("Value", MatchJSON(`{"command":"gh-mcp","env":{"D":"/data/p"}}`))))
	})

	DescribeTable("refuses an MCP server name declared twice",
		func(manifest string, msg string) {
			_, err := detect(manifest, map[string]string{".mcp.json": `{"mcpServers":{"gh":{"command":"a"}}}`, "more.json": `{"mcpServers":{"gh":{"command":"b"}}}`})
			Expect(err).To(MatchError(ContainSubstring(msg)))
		},
		Entry("in .mcp.json and inline", `{"name":"p","mcpServers":{"gh":{"command":"c"}}}`, `.mcp.json and mcpServers.gh are both MCP server "gh"`),
		Entry("in .mcp.json and a listed file", `{"name":"p","mcpServers":"./more.json"}`, `.mcp.json and more.json are both MCP server "gh"`),
	)

	It("merges hooks/hooks.json with listed hooks files and inline event maps into runtime hooks", func() {
		m, err := detect(`{"name":"p","hooks":["./config/extra.json",{"PostToolUse":[{"matcher":"Write","hooks":[{"type":"command","command":"\"${CLAUDE_PLUGIN_ROOT}\"/fmt.sh"}]}]}]}`,
			map[string]string{
				"hooks/hooks.json":  `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"x"}]}]}}`,
				"config/extra.json": `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"y"}]}]}}`,
				"commands/a.md":     "a",
			})
		Expect(err).NotTo(HaveOccurred())
		Expect(paths(m)["hooks"]).To(Equal([]string{"hooks/hooks.json", "config/extra.json", "inline"}))
		Expect(string(m.RuntimeHooks[2].Hooks)).To(Equal(`{"PostToolUse":[{"hooks":[{"command":"\"${CLAUDE_PLUGIN_ROOT}\"/fmt.sh","type":"command"}],"matcher":"Write"}]}`))
	})

	It("ignores the native mcps.json and rules/, which a Claude plugin does not have", func() {
		m, err := detect(`{"name":"p"}`, map[string]string{"commands/a.md": "a", "mcps.json": `{"mcpServers":{"n":{"command":"n"}}}`, "rules/r.md": "r"})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.MCPServers).To(BeEmpty())
		Expect(m.Rules).To(BeEmpty())
	})

	It("warns about keys for things lola does not install, and about unknown keys, keeping both", func() {
		m, err := detect(`{"name":"p","lspServers":"./.lsp.json","userConfig":{"k":{"type":"string"}},"frob\u001b[2J":1}`,
			map[string]string{"commands/a.md": "a"})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Warnings).To(Equal([]string{
			".claude-plugin/plugin.json: frob?[2J is not a plugin key lola knows; it is kept under formats.claude-plugin",
			".claude-plugin/plugin.json: lspServers is not supported: lola does not install it; it is kept under formats.claude-plugin",
			".claude-plugin/plugin.json: userConfig is not supported: lola does not install it; it is kept under formats.claude-plugin",
		}))
		Expect(m.Formats["claude-plugin"]).To(HaveKeyWithValue("lspServers", "./.lsp.json"))
		Expect(m.Formats["claude-plugin"]).To(HaveKey("userConfig"))
	})

	DescribeTable("refuses a component path that breaks Claude's path rules",
		func(manifest string, files map[string]string, msg string) {
			files["commands/a.md"] = "a"
			_, err := detect(manifest, files)
			Expect(err).To(MatchError(ContainSubstring(msg)))
			Expect(errs.Hints(err)).NotTo(BeEmpty())
		},
		Entry("without ./", `{"name":"p","commands":"commands/a.md"}`, map[string]string{}, `.claude-plugin/plugin.json: commands: path "commands/a.md" must start with ./`),
		Entry("bare ./ for a file key", `{"name":"p","agents":"./"}`, map[string]string{}, `.claude-plugin/plugin.json: agents: path "./" must start with ./`),
		Entry("leaving the plugin", `{"name":"p","agents":"./../x.md"}`, map[string]string{}, `.claude-plugin/plugin.json: agents: path "./../x.md" must stay inside the plugin`),
		Entry("absolute after ./", `{"name":"p","skills":".//etc"}`, map[string]string{}, `.claude-plugin/plugin.json: skills: path ".//etc" must stay inside the plugin`),
		Entry("missing", `{"name":"p","hooks":"./nope.json"}`, map[string]string{}, `.claude-plugin/plugin.json: hooks: path "nope.json" does not exist`),
		Entry("in an object map source", `{"name":"p","commands":{"x":{"source":"x.md"}}}`, map[string]string{}, `.claude-plugin/plugin.json: commands.x: path "x.md" must start with ./`),
	)

	It("coaches a path leaving the plugin with the ./ form Claude Code reads", func() {
		_, err := detect(`{"name":"p","agents":"./../x.md"}`, map[string]string{"commands/a.md": "a"})
		Expect(err).To(HaveOccurred())
		Expect(errs.Hints(err)).To(ConsistOf("give a path from the plugin root, written ./path, such as ./skills/review"))
	})

	It("warns about and drops an unknown key whose value lola cannot represent", func() {
		m, err := detect(`{"name":"p","weird":1e999}`, map[string]string{"commands/a.md": "a"})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Warnings).To(Equal([]string{
			".claude-plugin/plugin.json: weird is not a plugin key lola knows, and its value cannot be kept (number 1e999 is out of range); it was dropped",
		}))
		Expect(m.Formats).NotTo(HaveKey("claude-plugin"))
	})

	It("refuses a component path through a symbolic link", func() {
		write(root, map[string]string{"real/a.md": "a"})
		Expect(os.Symlink("real", filepath.Join(root, "link"))).To(Succeed())
		_, err := detect(`{"name":"p","commands":"./link/a.md"}`, map[string]string{})
		Expect(err).To(MatchError(`.claude-plugin/plugin.json: commands: path "link/a.md" goes through the symbolic link link`))
		_, err = detect(`{"name":"p","commands":"./real/"}`, map[string]string{})
		Expect(err).NotTo(HaveOccurred())
		Expect(os.Symlink("a.md", filepath.Join(root, "real", "b.md"))).To(Succeed())
		_, err = detect(`{"name":"p","commands":"./real/"}`, map[string]string{})
		Expect(err).To(MatchError(".claude-plugin/plugin.json: commands: real/b.md is a symbolic link"))
	})

	DescribeTable("refuses a manifest value of the wrong type, naming the key",
		func(manifest, msg string) {
			_, err := detect(manifest, map[string]string{"commands/a.md": "a", "agents/x/r.md": "r", "README.txt": ""})
			Expect(err).To(MatchError(msg))
			Expect(errs.Hints(err)).NotTo(BeEmpty())
		},
		Entry("not an object", `[]`, ".claude-plugin/plugin.json must be a JSON object"),
		Entry("null", `null`, ".claude-plugin/plugin.json must be a JSON object"),
		Entry("no name", `{"version":"1"}`, ".claude-plugin/plugin.json: name: is required"),
		Entry("a number as name", `{"name":1}`, ".claude-plugin/plugin.json: name: must be a string"),
		Entry("keywords as a string", `{"name":"p","keywords":"ci"}`, ".claude-plugin/plugin.json: keywords: must be a list of strings"),
		Entry("an unknown author key", `{"name":"p","author":{"name":"a","phone":"1"}}`, ".claude-plugin/plugin.json: author: must be an object with name, email and url strings"),
		Entry("skills as a number", `{"name":"p","skills":1}`, ".claude-plugin/plugin.json: skills: must be a path or a list of paths"),
		Entry("a skills path to a file", `{"name":"p","skills":"./commands/a.md"}`, `.claude-plugin/plugin.json: skills: path "./commands/a.md" is not a directory`),
		Entry("commands as a number", `{"name":"p","commands":1}`, ".claude-plugin/plugin.json: commands: must be a path, a list of paths or an object of commands"),
		Entry("a command not in markdown", `{"name":"p","commands":"./README.txt"}`, `.claude-plugin/plugin.json: commands: path "./README.txt" is not a .md file`),
		Entry("a command with source and content", `{"name":"p","commands":{"x":{"source":"./commands/a.md","content":"c"}}}`, ".claude-plugin/plugin.json: commands.x: sets exactly one of source or content"),
		Entry("a command with neither", `{"name":"p","commands":{"x":{"description":"d"}}}`, ".claude-plugin/plugin.json: commands.x: sets exactly one of source or content"),
		Entry("a command with empty content", `{"name":"p","commands":{"x":{"content":""}}}`, ".claude-plugin/plugin.json: commands.x: content is empty"),
		Entry("a command with an unknown key", `{"name":"p","commands":{"x":{"content":"c","hint":"h"}}}`, ".claude-plugin/plugin.json: commands.x: must be an object with source or content, and description, argumentHint, model or allowedTools"),
		Entry("a command with a bad name", `{"name":"p","commands":{"X Y":{"content":"c"}}}`, `.claude-plugin/plugin.json: commands.X Y: command name "X Y" is invalid: X and Y are uppercase, ' ' is not allowed; use 1-64 characters from a-z, 0-9, '.', '_' and '-', starting with a letter or digit, with no '..'`),
		Entry("an agents directory", `{"name":"p","agents":"./agents/x"}`, `.claude-plugin/plugin.json: agents: path "./agents/x" is a directory; list the .md files`),
		Entry("mcpServers as a number", `{"name":"p","mcpServers":1}`, ".claude-plugin/plugin.json: mcpServers: must be a path, an object of servers, or a list of them"),
		Entry("an inline server without command or url", `{"name":"p","mcpServers":{"s":{}}}`, `.claude-plugin/plugin.json: mcpServers: server "s" needs a command or a url`),
		Entry("an out of range number in inline hooks", `{"name":"p","hooks":{"Stop":1e999}}`, ".claude-plugin/plugin.json: hooks: number 1e999 is out of range"),
		Entry("hooks as a number", `{"name":"p","hooks":1}`, ".claude-plugin/plugin.json: hooks: must be a path, an object of hook events, or a list of them"),
	)

	It("refuses a plugin name lola cannot use, with a way out", func() {
		_, err := detect(`{"name":"Deploy Tools"}`, map[string]string{"commands/a.md": "a"})
		Expect(err).To(MatchError(ContainSubstring(`.claude-plugin/plugin.json: module name "Deploy Tools" is invalid`)))
		Expect(errs.Hints(err)).To(ConsistOf(ContainSubstring("a lola.yml replaces the plugin manifest, so it must also list the plugin's components")))
	})

	It("refuses a manifest with a repeated key", func() {
		_, err := detect(`{"name":"a","name":"b"}`, map[string]string{"commands/a.md": "a"})
		Expect(err).To(MatchError(`.claude-plugin/plugin.json: key "name" appears twice in the top-level object`))
	})

	It("yields to a lola manifest, which shadows it", func() {
		m, err := detect(`{"name":"p"}`, map[string]string{"lola.yml": "name: mine\n", "commands/a.md": "a"})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Name).To(Equal("mine"))
		Expect(m.Shadowed).To(Equal([]string{".claude-plugin/plugin.json"}))
		Expect(m.Warnings).To(ConsistOf(".claude-plugin/plugin.json was ignored: lola.yml takes precedence"))
	})

	Describe("coaching and plugin-path errors", func() {
		noGoTypes := func(err error) {
			Expect(err).To(HaveOccurred())
			for _, leak := range []string{"Go value", "Go struct", "module.", "json.RawMessage", "map[string]"} {
				Expect(err.Error()).NotTo(ContainSubstring(leak))
			}
			Expect(errs.Hints(err)).NotTo(BeEmpty())
		}

		It("reports JSON of the wrong shape without Go type names", func() {
			_, err := detect(`{"name":"p"}`, map[string]string{".mcp.json": `{"mcpServers":[]}`, "commands/a.md": "a"})
			Expect(err).To(MatchError(".mcp.json: mcpServers must be a JSON object"))
			noGoTypes(err)
			Expect(os.RemoveAll(root)).To(Succeed())
			_, err = detect(`{"name":"p"}`, map[string]string{"hooks/hooks.json": `[]`})
			Expect(err).To(MatchError("hooks/hooks.json must be a JSON object"))
			noGoTypes(err)
			Expect(os.RemoveAll(root)).To(Succeed())
			_, err = detect(`[]`, map[string]string{"commands/a.md": "a"})
			noGoTypes(err)
		})

		DescribeTable("names the unreadable path without the staging directory",
			func(manifest string, key string, files map[string]string) {
				if os.Geteuid() == 0 {
					Skip("root reads mode 000 directories")
				}
				files["commands/keep.md"] = "k"
				m := manifest
				write(root, files)
				Expect(os.Chmod(filepath.Join(root, key), 0)).To(Succeed())
				DeferCleanup(func() { _ = os.Chmod(filepath.Join(root, key), 0o755) })
				_, err := detect(m, map[string]string{})
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).NotTo(ContainSubstring(root))
				Expect(err.Error()).To(ContainSubstring(key))
				Expect(errs.Hints(err)).NotTo(BeEmpty())
			},
			Entry("a skills directory", `{"name":"p","skills":"./locked"}`, "locked", map[string]string{"locked/x": ""}),
			Entry("a commands directory", `{"name":"p","commands":"./locked/"}`, "locked", map[string]string{"locked/x.md": ""}),
			Entry("the hooks directory", `{"name":"p"}`, "hooks", map[string]string{"hooks/hooks.json": `{"hooks":{}}`}),
		)

		It("refuses an inline MCP server name declared in two inline objects, naming both", func() {
			_, err := detect(`{"name":"p","mcpServers":[{"a":{"command":"x"}},{"a":{"command":"y"}}]}`, map[string]string{})
			Expect(err).To(MatchError(ContainSubstring(`mcpServers[0].a and mcpServers[1].a are both MCP server "a"`)))
			Expect(err.Error()).NotTo(ContainSubstring("components"))
			Expect(errs.Hints(err)).NotTo(BeEmpty())
		})

		It("warns that commands/ is ignored when the commands object map does not read from it", func() {
			m, err := detect(`{"name":"p","commands":{"about":{"content":"c"}}}`, map[string]string{"commands/a.md": "a"})
			Expect(err).NotTo(HaveOccurred())
			Expect(m.Warnings).To(ConsistOf("the default commands/ directory is ignored because .claude-plugin/plugin.json sets commands"))
			Expect(os.RemoveAll(root)).To(Succeed())
			m, err = detect(`{"name":"p","commands":{"a":{"source":"./commands/a.md"}}}`, map[string]string{"commands/a.md": "a"})
			Expect(err).NotTo(HaveOccurred())
			Expect(m.Warnings).To(BeEmpty())
		})

		It("warns about symbolic links among the defaults instead of skipping them silently", func() {
			write(root, map[string]string{"real/s/SKILL.md": "---\ndescription: s\n---\n", "agents/a.md": "---\ndescription: a\n---\n",
				"skills2/s/SKILL.md": "---\ndescription: s\n---\n"})
			Expect(os.Symlink("real", filepath.Join(root, "skills"))).To(Succeed())
			Expect(os.Symlink("real", filepath.Join(root, "commands"))).To(Succeed())
			m, err := detect(`{"name":"p"}`, map[string]string{})
			Expect(err).NotTo(HaveOccurred())
			Expect(m.Warnings).To(ConsistOf("skills is a symbolic link; skipped", "commands is a symbolic link; skipped"))
			Expect(os.RemoveAll(root)).To(Succeed())
			write(root, map[string]string{"skills/s/SKILL.md": "---\ndescription: s\n---\n"})
			Expect(os.Symlink("s", filepath.Join(root, "skills", "link"))).To(Succeed())
			m, err = detect(`{"name":"p"}`, map[string]string{})
			Expect(err).NotTo(HaveOccurred())
			Expect(m.Warnings).To(ConsistOf("skills/link is a symbolic link; skipped"))
		})

		It("refuses a plugin that declares nothing lola can install, keeping the warnings as hints", func() {
			_, err := detect(`{"name":"p","mcpServers":"./tools.mcpb","lspServers":{}}`, map[string]string{})
			Expect(err).To(MatchError(".claude-plugin/plugin.json declares nothing lola can install"))
			Expect(err.Error()).NotTo(ContainSubstring(root))
			Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("the MCP bundle ./tools.mcpb is not supported")))
			Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("lspServers is not supported")))
		})

		DescribeTable("checks its own paths and names the plugin's",
			func(manifest string, files map[string]string, msg string) {
				_, err := detect(manifest, files)
				Expect(err).To(MatchError(ContainSubstring(msg)))
				Expect(err.Error()).NotTo(ContainSubstring("components"))
				Expect(errs.Hints(err)).NotTo(BeEmpty())
			},
			Entry("an object map source that is not markdown", `{"name":"p","commands":{"x":{"source":"./README.txt"}}}`,
				map[string]string{"README.txt": ""}, `.claude-plugin/plugin.json: commands.x: source "./README.txt" is not a .md file`),
			Entry("a listed hooks file without the wrapper", `{"name":"p","hooks":"./h.json"}`,
				map[string]string{"h.json": `{}`}, `.claude-plugin/plugin.json: hooks: h.json has no "hooks" object`),
			Entry("the default hooks file without the wrapper", `{"name":"p"}`,
				map[string]string{"hooks/hooks.json": `{}`}, `.claude-plugin/plugin.json: hooks: hooks/hooks.json has no "hooks" object`),
			Entry("inline hooks naming no event", `{"name":"p","hooks":{}}`,
				map[string]string{"commands/a.md": "a"}, `.claude-plugin/plugin.json: hooks: hooks lists no event`),
		)

		It("refuses a symbolic link as the default hooks file by its plugin path", func() {
			write(root, map[string]string{"real.json": `{"hooks":{}}`, "commands/a.md": "a"})
			Expect(os.MkdirAll(filepath.Join(root, "hooks"), 0o755)).To(Succeed())
			Expect(os.Symlink("../real.json", filepath.Join(root, "hooks", "hooks.json"))).To(Succeed())
			_, err := detect(`{"name":"p"}`, map[string]string{})
			Expect(err).To(MatchError(ContainSubstring("hooks/hooks.json is a symbolic link")))
			Expect(err.Error()).NotTo(ContainSubstring("components"))
			Expect(errs.Hints(err)).NotTo(BeEmpty())
		})

		DescribeTable("refuses null for a component key",
			func(key string) {
				_, err := detect(`{"name":"p","`+key+`":null}`, map[string]string{"commands/a.md": "a"})
				Expect(err).To(MatchError(".claude-plugin/plugin.json: " + key + ": must not be null"))
				Expect(errs.Hints(err)).NotTo(BeEmpty())
			},
			Entry("skills", "skills"), Entry("commands", "commands"), Entry("agents", "agents"),
			Entry("mcpServers", "mcpServers"), Entry("hooks", "hooks"),
		)

		DescribeTable("lists only .json files for hooks and mcpServers",
			func(key, file string) {
				_, err := detect(`{"name":"p","`+key+`":"./x.txt"}`, map[string]string{"x.txt": file, "commands/a.md": "a"})
				Expect(err).To(MatchError(ContainSubstring(`.claude-plugin/plugin.json: ` + key + `: path "./x.txt" is not a .json file`)))
				Expect(errs.Hints(err)).NotTo(BeEmpty())
			},
			Entry("hooks", "hooks", `{"hooks":{}}`), Entry("mcpServers", "mcpServers", `{"mcpServers":{}}`),
		)

		It("warns about a field of an inline MCP server that lola does not read", func() {
			m, err := detect(`{"name":"p","mcpServers":{"s":{"command":"c","transprot\u001b[2J":"x"}}}`, map[string]string{})
			Expect(err).NotTo(HaveOccurred())
			Expect(m.Warnings).To(ConsistOf(`.claude-plugin/plugin.json: mcpServers: server "s" has the field "transprot?[2J", which lola does not read; it is ignored`))
		})
	})
})

var _ = Describe("module formats reading an unreadable directory", func() {
	It("names the directory inside the module, never the absolute path", func() {
		if os.Geteuid() == 0 {
			Skip("root ignores directory permissions")
		}
		root := filepath.Join(testenv.Isolate(), "plugin")
		write(root, map[string]string{".claude-plugin/plugin.json": `{"name":"p"}`, "commands/sub/a.md": "a"})
		locked := filepath.Join(root, "commands", "sub")
		Expect(os.Chmod(locked, 0o000)).To(Succeed())
		DeferCleanup(os.Chmod, locked, os.FileMode(0o755))
		_, err := module.Detect(root, "x", targets.IDs())
		Expect(err).To(MatchError(ContainSubstring("commands/sub: permission denied")))
		Expect(err.Error()).NotTo(ContainSubstring(root))

		Expect(os.Chmod(locked, 0o755)).To(Succeed())
		Expect(os.RemoveAll(filepath.Join(root, ".claude-plugin"))).To(Succeed())
		write(root, map[string]string{"plugin.json": `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"p"}`,
			"dev.getlola/commands/a.md": "a"})
		locked = filepath.Join(root, "dev.getlola", "commands")
		Expect(os.Chmod(locked, 0o000)).To(Succeed())
		DeferCleanup(os.Chmod, locked, os.FileMode(0o755))
		_, err = module.Detect(root, "x", targets.IDs())
		Expect(err).To(MatchError(ContainSubstring("dev.getlola/commands: permission denied")))
		Expect(err.Error()).NotTo(ContainSubstring(root))
	})

	It("names an unreadable adapter manifest inside the module", func() {
		if os.Geteuid() == 0 {
			Skip("root ignores directory permissions")
		}
		root := filepath.Join(testenv.Isolate(), "plugin")
		write(root, map[string]string{".claude-plugin/plugin.json": `{"name":"p"}`})
		locked := filepath.Join(root, ".claude-plugin")
		Expect(os.Chmod(locked, 0o000)).To(Succeed())
		DeferCleanup(os.Chmod, locked, os.FileMode(0o755))
		_, err := module.Detect(root, "x", targets.IDs())
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(HavePrefix(".claude-plugin/plugin.json: "))
		Expect(err.Error()).NotTo(ContainSubstring(root))
	})
})
