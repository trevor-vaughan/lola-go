package module_test

import (
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

const (
	apSchema  = `"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json"`
	mcpSchema = `"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json"`
)

var _ = Describe("the Agent Plugins adapter", func() {
	var root string
	BeforeEach(func() { root = filepath.Join(testenv.Isolate(), "package") })

	detect := func(manifest string, files map[string]string) (*module.Module, error) {
		if manifest != "" {
			files["plugin.json"] = manifest
		}
		write(root, files)
		return module.Detect(root, "fallback", targets.IDs())
	}
	// refused asserts err is a coached refusal that leaks neither Go type names nor the staging path.
	refused := func(err error, msg any) {
		Expect(err).To(MatchError(msg))
		Expect(errs.Hints(err)).NotTo(BeEmpty(), "every refusal coaches")
		for _, leak := range []string{"Go value", "module.", "json.RawMessage", "struct {", root} {
			Expect(err.Error()).NotTo(ContainSubstring(leak))
		}
	}
	skill := func(name string) string {
		return "---\nname: " + name + "\ndescription: Does " + name + "\n---\nBody.\n"
	}
	servers := func(json string) string { return `{` + mcpSchema + `,"mcpServers":{` + json + `}}` }

	It("reads the manifest, the portable skills and mcp.json, writing servers inline with the host's variables", func() {
		m, err := detect(`{`+apSchema+`,"name":"dev.tools","version":"0.1.0","description":"d","author":{"name":"A"},
			"keywords":["k"],"license":"MIT","extensions":{"com.example.other":{"x":1}}}`, map[string]string{
			"skills/deploy/SKILL.md": skill("deploy"),
			"skills/deploy/run.sh":   "#!/bin/sh\n",
			"mcp.json": servers(`"local":{"type":"stdio","command":"./bin/validator","args":["--data","${PLUGIN_DATA}/v"],
				"env":{"CONFIG":"${PLUGIN_ROOT}/c.json"},"cwd":"./work"},
				"npx":{"type":"stdio","command":"npx","cwd":"${PLUGIN_DATA}/cache/"},
				"api":{"type":"streamable-http","url":"https://deploy.example.com/mcp","headers":{"X-Tenant":"t"}},
				"events":{"type":"sse","url":"http://127.0.0.1:8080/sse"}`),
			"mcps.json":     `{"mcpServers":{"native":{"command":"n"}}}`,
			"commands/x.md": "not portable",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(m).To(PointTo(MatchFields(IgnoreExtras, Fields{"Name": Equal("dev.tools"), "Version": Equal("0.1.0"),
			"Author": Equal(&module.Author{Name: "A"}), "Keywords": Equal([]string{"k"}), "License": Equal("MIT"),
			"Format": Equal("agent-plugins"), "Manifest": Equal("plugin.json")})))
		Expect(m.Skills).To(HaveLen(1))
		Expect(m.Skills[0]).To(MatchFields(IgnoreExtras, Fields{"Name": Equal("deploy"), "Dir": Equal("skills/deploy")}))
		Expect(m.Commands).To(BeEmpty(), "commands/ is not part of the portable core")
		Expect(m.MCPServers).To(Equal([]module.MCPServer{
			{Name: "api", Transport: "http", URL: "https://deploy.example.com/mcp", Headers: map[string]string{"X-Tenant": "t"}},
			{Name: "events", Transport: "sse", URL: "http://127.0.0.1:8080/sse"},
			{Name: "local", Transport: "stdio", Command: "${PLUGIN_ROOT}/bin/validator", Args: []string{"--data", "${PLUGIN_DATA}/v"},
				Env: map[string]string{"CONFIG": "${PLUGIN_ROOT}/c.json", "PLUGIN_ROOT": "${PLUGIN_ROOT}", "PLUGIN_DATA": "${PLUGIN_DATA}"},
				Cwd: "${PLUGIN_ROOT}/work"},
			{Name: "npx", Transport: "stdio", Command: "npx", Env: map[string]string{"PLUGIN_ROOT": "${PLUGIN_ROOT}", "PLUGIN_DATA": "${PLUGIN_DATA}"},
				Cwd: "${PLUGIN_DATA}/cache"},
		}))
		Expect(m.Formats).To(Equal(map[string]any{"agent-plugins": map[string]any{"extensions": map[string]any{"com.example.other": map[string]any{"x": int64(1)}}}}))
		Expect(m.Warnings).To(ConsistOf("plugin.json: extensions: lola renders nothing for the namespace com.example.other; it was ignored"))
	})

	It("falls through a plugin.json that is not Agent Plugins, with a warning", func() {
		m, err := detect(`{"name":"other-tool","version":2}`, map[string]string{"commands/a.md": "a"})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Format).To(Equal("native"))
		Expect(m.Name).To(Equal("fallback"))
		Expect(m.Warnings).To(ConsistOf("plugin.json is not an Agent Plugins 1.0.0 manifest (its $schema is not https://agent-plugins.org/schemas/1.0.0/plugin.schema.json); it was ignored"))
		m, err = detect(`[1]`, map[string]string{".claude-plugin/plugin.json": `{"name":"claude-one"}`})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Format).To(Equal("claude-plugin"))
		Expect(m.Shadowed).To(BeEmpty())
	})

	It("takes precedence over a Claude plugin manifest, which it shadows", func() {
		m, err := detect(`{`+apSchema+`,"name":"both"}`, map[string]string{".claude-plugin/plugin.json": `{"name":"claude-one"}`, "skills/s/SKILL.md": skill("s")})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Name).To(Equal("both"))
		Expect(m.Shadowed).To(Equal([]string{".claude-plugin/plugin.json"}))
		Expect(m.Warnings).To(ConsistOf(".claude-plugin/plugin.json was ignored: plugin.json takes precedence"))
	})

	DescribeTable("validates the manifest against the closed schema",
		func(fields, msg string) {
			_, err := detect(`{`+apSchema+fields+`}`, map[string]string{"skills/s/SKILL.md": skill("s")})
			refused(err, msg)
		},
		Entry("an unknown field", `,"name":"p","targets":["x"]`, "plugin.json: targets: is not an Agent Plugins manifest field"),
		Entry("no name", ``, "plugin.json: name: is required"),
		Entry("a name that is not a string", `,"name":7`, "plugin.json: name: must be a string"),
		Entry("an uppercase name", `,"name":"Tools"`, `plugin.json: name: "Tools" is not an Agent Plugins name`),
		Entry("a double hyphen", `,"name":"a--b"`, `plugin.json: name: "a--b" is not an Agent Plugins name`),
		Entry("a double dot", `,"name":"a..b"`, `plugin.json: name: "a..b" is not an Agent Plugins name`),
		Entry("a trailing dot", `,"name":"a."`, `plugin.json: name: "a." is not an Agent Plugins name`),
		Entry("an underscore", `,"name":"a_b"`, `plugin.json: name: "a_b" is not an Agent Plugins name`),
		Entry("65 characters", `,"name":"`+strings.Repeat("a", 65)+`"`, `plugin.json: name: "`+strings.Repeat("a", 65)+`" is not an Agent Plugins name`),
		Entry("an author with another field", `,"name":"p","author":{"name":"a","x":1}`, "plugin.json: author: must be an object with name, email and url strings"),
		Entry("keywords that are not strings", `,"name":"p","keywords":[1]`, "plugin.json: keywords: must be a list of strings"),
		Entry("a null version", `,"name":"p","version":null`, "plugin.json: version: must be a string"),
		Entry("a null description", `,"name":"p","description":null`, "plugin.json: description: must be a string"),
		Entry("a null keyword", `,"name":"p","keywords":[null]`, "plugin.json: keywords: must be a list of strings"),
		Entry("a null author", `,"name":"p","author":null`, "plugin.json: author: must be an object with name, email and url strings"),
		Entry("a null author name", `,"name":"p","author":{"name":null}`, "plugin.json: author: must be an object with name, email and url strings"),
		Entry("a version that is not a string", `,"name":"p","version":1`, "plugin.json: version: must be a string"),
	)

	It("accepts a 64-character name with dots and hyphens", func() {
		name := "a.b-" + strings.Repeat("c", 60)
		m, err := detect(`{`+apSchema+`,"name":"`+name+`"}`, map[string]string{"skills/s/SKILL.md": skill("s")})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Name).To(Equal(name))
	})

	It("skips an invalid skill with a warning and loads the rest", func() {
		m, err := detect(`{`+apSchema+`,"name":"p"}`, map[string]string{
			"skills/good/SKILL.md":     skill("good"),
			"skills/other/SKILL.md":    skill("different"),
			"skills/Upper/SKILL.md":    skill("Upper"),
			"skills/a--b/SKILL.md":     skill("a--b"),
			"skills/nodesc/SKILL.md":   "---\nname: nodesc\n---\n",
			"skills/long/SKILL.md":     "---\nname: long\ndescription: " + strings.Repeat("x", 1025) + "\n---\n",
			"skills/compat/SKILL.md":   "---\nname: compat\ndescription: d\ncompatibility: " + strings.Repeat("x", 501) + "\n---\n",
			"skills/broken/SKILL.md":   "---\nname: [\n---\n",
			"skills/nofile/readme.md":  "",
			"skills/.hidden/SKILL.md":  skill(".hidden"),
			"skills/loose.md":          "",
			"skills/maxdesc/SKILL.md":  "---\nname: maxdesc\ndescription: " + strings.Repeat("x", 1024) + "\ncompatibility: " + strings.Repeat("y", 500) + "\n---\n",
			"skills/\x1b[2J/SKILL.md":  skill("x"),
			"skills/mbdesc/SKILL.md":   "---\nname: mbdesc\ndescription: " + strings.Repeat("é", 1024) + "\ncompatibility: " + strings.Repeat("é", 500) + "\n---\n",
			"skills/mblong/SKILL.md":   "---\nname: mblong\ndescription: " + strings.Repeat("é", 1025) + "\n---\n",
			"skills/mbcompat/SKILL.md": "---\nname: mbcompat\ndescription: d\ncompatibility: " + strings.Repeat("é", 501) + "\n---\n",
		})
		Expect(err).NotTo(HaveOccurred())
		var names []string
		for _, sk := range m.Skills {
			names = append(names, sk.Name)
		}
		Expect(names).To(Equal([]string{"good", "maxdesc", "mbdesc"}))
		Expect(m.Warnings).To(ConsistOf(
			`skills/other/SKILL.md is not a valid Agent Skill: name "different" differs from its directory; skipped`,
			"skills/Upper/SKILL.md is not a valid Agent Skill: name must be 1-64 lowercase letters, digits and single hyphens; skipped",
			"skills/a--b/SKILL.md is not a valid Agent Skill: name must be 1-64 lowercase letters, digits and single hyphens; skipped",
			"skills/nodesc/SKILL.md is not a valid Agent Skill: description must be 1-1024 characters; skipped",
			"skills/long/SKILL.md is not a valid Agent Skill: description must be 1-1024 characters; skipped",
			"skills/compat/SKILL.md is not a valid Agent Skill: compatibility must be at most 500 characters; skipped",
			"skills/broken/SKILL.md is not a valid Agent Skill: its frontmatter does not parse; skipped",
			"skills/nofile has no SKILL.md; skipped",
			`skills/?[2J/SKILL.md is not a valid Agent Skill: name "x" differs from its directory; skipped`,
			"skills/mblong/SKILL.md is not a valid Agent Skill: description must be 1-1024 characters; skipped",
			"skills/mbcompat/SKILL.md is not a valid Agent Skill: compatibility must be at most 500 characters; skipped",
		))
	})

	DescribeTable("refuses an mcp.json that breaks a rule",
		func(doc, msg string) {
			_, err := detect(`{`+apSchema+`,"name":"p"}`, map[string]string{"mcp.json": doc, "skills/s/SKILL.md": skill("s")})
			refused(err, msg)
		},
		Entry("another top-level field", `{`+mcpSchema+`,"mcpServers":{},"x":1}`, "mcp.json must hold exactly $schema and mcpServers"),
		Entry("no $schema", `{"mcpServers":{}}`, "mcp.json must hold exactly $schema and mcpServers"),
		Entry("another $schema", `{"$schema":"https://agent-plugins.org/schemas/2.0.0/mcp.schema.json","mcpServers":{}}`, "mcp.json: $schema: must be https://agent-plugins.org/schemas/1.0.0/mcp.schema.json"),
		Entry("servers as a list", `{`+mcpSchema+`,"mcpServers":[]}`, "mcp.json: mcpServers: must be an object of servers"),
		Entry("an unknown transport", servers(`"s":{"type":"ws","url":"wss://x"}`), `mcp.json: mcpServers.s.type: "ws" is not stdio, streamable-http or sse`),
		Entry("no type", servers(`"s":{"command":"x"}`), `mcp.json: mcpServers.s.type: "" is not stdio, streamable-http or sse`),
		Entry("a field of another variant", servers(`"s":{"type":"stdio","command":"x","url":"https://x"}`), "mcp.json: mcpServers.s: a stdio server takes only type, command, args, env and cwd, as strings (args a list, env an object)"),
		Entry("no command", servers(`"s":{"type":"stdio"}`), "mcp.json: mcpServers.s.command: is required"),
		Entry("a multi-token command", servers(`"s":{"type":"stdio","command":"npx -y srv"}`), "mcp.json: mcpServers.s.command: must be one executable token"),
		Entry("a command path that is not ./", servers(`"s":{"type":"stdio","command":"/usr/bin/x"}`), `mcp.json: mcpServers.s.command: "/usr/bin/x" is neither a bare executable name nor a ./ path`),
		Entry("a command leaving the package", servers(`"s":{"type":"stdio","command":"./../x"}`), `mcp.json: mcpServers.s.command: "./../x" leaves the package`),
		Entry("a command placeholder", servers(`"s":{"type":"stdio","command":"${PLUGIN_ROOT}"}`), "mcp.json: mcpServers.s.command: takes no placeholders"),
		Entry("env PLUGIN_ROOT", servers(`"s":{"type":"stdio","command":"x","env":{"PLUGIN_ROOT":"/"}}`), "mcp.json: mcpServers.s.env: PLUGIN_ROOT is reserved: the host sets it"),
		Entry("env PLUGIN_DATA", servers(`"s":{"type":"stdio","command":"x","env":{"PLUGIN_DATA":"/"}}`), "mcp.json: mcpServers.s.env: PLUGIN_DATA is reserved: the host sets it"),
		Entry("env values that are not strings", servers(`"s":{"type":"stdio","command":"x","env":{"A":1}}`), "mcp.json: mcpServers.s: a stdio server takes only type, command, args, env and cwd, as strings (args a list, env an object)"),
		Entry("an absolute cwd", servers(`"s":{"type":"stdio","command":"x","cwd":"/tmp"}`), `mcp.json: mcpServers.s.cwd: "/tmp" must start with ./, ${PLUGIN_ROOT} or ${PLUGIN_DATA}`),
		Entry("a cwd leaving the root", servers(`"s":{"type":"stdio","command":"x","cwd":"${PLUGIN_ROOT}/../x"}`), `mcp.json: mcpServers.s.cwd: "${PLUGIN_ROOT}/../x" leaves the directory it starts in`),
		Entry("a ./ cwd leaving the root", servers(`"s":{"type":"stdio","command":"x","cwd":"./a/../../x"}`), `mcp.json: mcpServers.s.cwd: "./a/../../x" leaves the directory it starts in`),
		Entry("a non-loopback http url", servers(`"s":{"type":"streamable-http","url":"http://example.com/mcp"}`), "mcp.json: mcpServers.s.url: http://example.com/mcp must use https: plain http is allowed only for a loopback host"),
		Entry("a url with user information", servers(`"s":{"type":"sse","url":"https://u:s3cr3t@example.com/mcp"}`), "mcp.json: mcpServers.s.url: https://example.com/mcp must not carry user information or a fragment"),
		Entry("a url with a fragment", servers(`"s":{"type":"sse","url":"https://example.com/mcp#x"}`), "mcp.json: mcpServers.s.url: https://example.com/mcp must not carry user information or a fragment"),
		Entry("a url that is not http", servers(`"s":{"type":"sse","url":"ftp://example.com/mcp"}`), "mcp.json: mcpServers.s.url: must be an absolute http or https URL"),
		Entry("a relative url", servers(`"s":{"type":"sse","url":"/mcp"}`), "mcp.json: mcpServers.s.url: must be an absolute http or https URL"),
		Entry("a url placeholder", servers(`"s":{"type":"sse","url":"https://x/${PLUGIN_ROOT}"}`), "mcp.json: mcpServers.s.url: takes no placeholders"),
		Entry("a remote server with a command", servers(`"s":{"type":"streamable-http","url":"https://x","command":"x"}`), "mcp.json: mcpServers.s: a remote server takes only type, url and headers, as strings (headers an object)"),
		Entry("a header twice", servers(`"s":{"type":"sse","url":"https://x","headers":{"X-A":"1","X-A":"2"}}`), `mcp.json: key "X-A" appears twice in /mcpServers/s/headers`),
		Entry("an invalid header name", servers(`"s":{"type":"sse","url":"https://x","headers":{"X A":"1"}}`), `mcp.json: mcpServers.s.headers: "X A" is not a valid header name`),
		Entry("a header value with a newline", servers(`"s":{"type":"sse","url":"https://x","headers":{"X":"a\nb"}}`), "mcp.json: mcpServers.s.headers.X: is not a valid header value"),
		Entry("a header value with a control character", servers(`"s":{"type":"sse","url":"https://x","headers":{"X":"a\u0001b"}}`), "mcp.json: mcpServers.s.headers.X: is not a valid header value"),
		Entry("a header value with DEL", servers(`"s":{"type":"sse","url":"https://x","headers":{"X":"a\u007fb"}}`), "mcp.json: mcpServers.s.headers.X: is not a valid header value"),
		Entry("a command that is ..", servers(`"s":{"type":"stdio","command":".."}`), `mcp.json: mcpServers.s.command: ".." is not an executable name`),
		Entry("a command with a control byte", servers(`"s":{"type":"stdio","command":"a\u0001b"}`), "mcp.json: mcpServers.s.command: must not contain control characters"),
		Entry("null args", servers(`"s":{"type":"stdio","command":"x","args":[null]}`), "mcp.json: mcpServers.s: a stdio server takes only type, command, args, env and cwd, as strings (args a list, env an object)"),
		Entry("null env", servers(`"s":{"type":"stdio","command":"x","env":{"A":null}}`), "mcp.json: mcpServers.s: a stdio server takes only type, command, args, env and cwd, as strings (args a list, env an object)"),
		Entry("null cwd", servers(`"s":{"type":"stdio","command":"x","cwd":null}`), "mcp.json: mcpServers.s: a stdio server takes only type, command, args, env and cwd, as strings (args a list, env an object)"),
		Entry("a null header", servers(`"s":{"type":"sse","url":"https://x","headers":{"X":null}}`), "mcp.json: mcpServers.s: a remote server takes only type, url and headers, as strings (headers an object)"),
		Entry("a header placeholder", servers(`"s":{"type":"sse","url":"https://x","headers":{"X":"${PLUGIN_DATA}"}}`), "mcp.json: mcpServers.s.headers.X: takes no placeholders"),
		Entry("a server name lola cannot use", servers(`"Bad Name":{"type":"stdio","command":"x"}`), `mcp.json: MCP server name "Bad Name" is invalid: B and N are uppercase, ' ' is not allowed; use 1-64 characters from a-z, 0-9, '.', '_' and '-', starting with a letter or digit, with no '..'`),
		Entry("a repeated server", `{`+mcpSchema+`,"mcpServers":{"s":{"type":"stdio","command":"x"},"s":{"type":"stdio","command":"y"}}}`, `mcp.json: key "s" appears twice in /mcpServers`),
	)

	It("warns about placeholders other than PLUGIN_ROOT and PLUGIN_DATA in args, env values and cwd", func() {
		m, err := detect(`{`+apSchema+`,"name":"p"}`, map[string]string{"mcp.json": servers(`"s":{"type":"stdio","command":"x",
			"args":["${HOME}/a","${PLUGIN_ROOT}","${\u001b[2J}","$PLAIN"],"env":{"K":"${TOKEN}","D":"${PLUGIN_DATA}/d"},"cwd":"${PLUGIN_ROOT}/${SUB}"}`)})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.MCPServers[0].Args).To(Equal([]string{"${HOME}/a", "${PLUGIN_ROOT}", "${\x1b[2J}", "$PLAIN"}), "kept as written")
		Expect(m.Warnings).To(ConsistOf(
			"mcp.json: mcpServers.s.args: ${HOME} is literal text under Agent Plugins, but the assistants expand it",
			"mcp.json: mcpServers.s.args: ${?[2J} is literal text under Agent Plugins, but the assistants expand it",
			"mcp.json: mcpServers.s.env.K: ${TOKEN} is literal text under Agent Plugins, but the assistants expand it",
			"mcp.json: mcpServers.s.cwd: ${SUB} is literal text under Agent Plugins, but the assistants expand it",
		))
	})

	It("still refuses PLUGIN_ROOT and PLUGIN_DATA below the cwd base", func() {
		for _, cwd := range []string{"${PLUGIN_ROOT}/${PLUGIN_DATA}", "${PLUGIN_DATA}/a/${PLUGIN_ROOT}"} {
			_, err := detect(`{`+apSchema+`,"name":"p"}`, map[string]string{"mcp.json": servers(`"s":{"type":"stdio","command":"x","cwd":"` + cwd + `"}`)})
			Expect(err).To(MatchError(ContainSubstring("leaves the directory it starts in")), cwd)
		}
	})

	It("allows plain http to a loopback host", func() {
		m, err := detect(`{`+apSchema+`,"name":"p"}`, map[string]string{"mcp.json": servers(`"a":{"type":"sse","url":"http://localhost:1/x"},
			"b":{"type":"streamable-http","url":"http://[::1]:2/x"},"c":{"type":"sse","url":"http://127.0.0.2/x"}`)})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.MCPServers).To(HaveLen(3))
	})

	It("routes each client namespace's commands and agents to its own target, and dev.getlola's to all", func() {
		m, err := detect(`{`+apSchema+`,"name":"p","extensions":{"ai.opencode":{"commands":"./oc/cmds"}}}`, map[string]string{
			"com.anthropic.claude/commands/c1.md":       "c1",
			"com.anthropic.claude-code/agents/a1.md":    "---\ndescription: a\n---\n",
			"ai.opencode/commands/ignored.md":           "replaced by the declared directory",
			"oc/cmds/o1.md":                             "o1",
			"com.google.gemini-cli/commands/g1.md":      "g1",
			"dev.getlola/commands/all.md":               "all",
			"dev.getlola/agents/sub/nested.md":          "one level only",
			"dev.getlola/INSTRUCTIONS.md":               "not declared, so not delivered",
			"com.anthropic.claude/commands/notes.txt":   "",
			"com.anthropic.claude/commands/deeper/x.md": "",
		})
		Expect(err).NotTo(HaveOccurred())
		route := map[string][]string{}
		for _, c := range m.Commands {
			route["command "+c.Name] = c.Targets
		}
		for _, a := range m.Agents {
			route["agent "+a.Name] = a.Targets
		}
		Expect(route).To(Equal(map[string][]string{
			"command c1": {"claude-code"}, "agent a1": {"claude-code"}, "command o1": {"opencode"},
			"command g1": {"gemini-cli"}, "command all": nil,
		}))
		Expect(m.Rules).To(BeEmpty())
	})

	It("delivers a namespace's instructions only when it declares them, as a rule named after the module", func() {
		m, err := detect(`{`+apSchema+`,"name":"p","extensions":{"dev.getlola":{"instructions":"./dev.getlola/INSTRUCTIONS.md"},
			"com.google.gemini-cli":{"instructions":"./gemini.md"}}}`, map[string]string{
			"dev.getlola/INSTRUCTIONS.md": "---\ndescription: d\n---\nBe kind.\n",
			"gemini.md":                   "Gemini only.\n",
			"ai.opencode/AGENTS.md":       "never read",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Rules).To(Equal([]module.Rule{{Name: "p", Body: "Be kind.\n"}}))
		Expect(m.Warnings).To(ConsistOf(`rule "p" from dev.getlola takes precedence over the one from com.google.gemini-cli on gemini-cli`))
	})

	It("lets dev.getlola win a name a client namespace also provides, narrowing the client's artifact away", func() {
		m, err := detect(`{`+apSchema+`,"name":"p"}`, map[string]string{
			"dev.getlola/commands/ship.md": "dev", "ai.opencode/commands/ship.md": "oc", "ai.opencode/commands/only.md": "oc",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Commands).To(HaveLen(2))
		Expect(m.Commands[1]).To(MatchFields(IgnoreExtras, Fields{"Name": Equal("ship"), "File": Equal("dev.getlola/commands/ship.md"), "Targets": BeNil()}))
		Expect(m.Warnings).To(ConsistOf(`command "ship" from dev.getlola takes precedence over the one from ai.opencode on opencode`))
	})

	It("refuses the two Anthropic namespaces providing one name, and accepts one name on different targets", func() {
		_, err := detect(`{`+apSchema+`,"name":"p"}`, map[string]string{
			"com.anthropic.claude/agents/rev.md": "---\ndescription: a\n---\n", "com.anthropic.claude-code/agents/rev.md": "---\ndescription: b\n---\n",
		})
		Expect(err).To(MatchError(`plugin.json: com.anthropic.claude and com.anthropic.claude-code both provide agent "rev" for claude-code`))
		Expect(errs.Hints(err)).To(ConsistOf("keep it in one of the namespaces, or move it to dev.getlola"))
		root = filepath.Join(testenv.Isolate(), "other")
		m, err := detect(`{`+apSchema+`,"name":"p"}`, map[string]string{
			"ai.opencode/commands/x.md": "oc", "com.google.gemini-cli/commands/x.md": "gm",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Commands).To(HaveLen(2))
	})

	It("warns about extension data it does not read, and ignores it", func() {
		m, err := detect(`{`+apSchema+`,"name":"p","extensions":{"ai.opencode":"x","dev.getlola":{"hooks":"./h","commands":"./c"},"com.cursor.editor":{}}}`,
			map[string]string{"c/a.md": "a"})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Commands).To(HaveLen(1))
		Expect(m.Warnings).To(ConsistOf(
			"plugin.json: extensions.ai.opencode is not an object; it was ignored",
			"plugin.json: extensions.dev.getlola.hooks is not a key lola reads (commands, agents, instructions); it was ignored",
			"plugin.json: extensions: lola renders nothing for the namespace com.cursor.editor; it was ignored",
		))
		m, err = detect(`{`+apSchema+`,"name":"p","extensions":[]}`, map[string]string{"skills/s/SKILL.md": skill("s")})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Warnings).To(ContainElement("plugin.json: extensions is not an object; it was ignored"))
	})

	DescribeTable("refuses a namespace path that is not a ./ path to what it names",
		func(ext string, files map[string]string, msg string) {
			files["skills/s/SKILL.md"] = skill("s")
			_, err := detect(`{`+apSchema+`,"name":"p","extensions":{"dev.getlola":`+ext+`}}`, files)
			refused(err, ContainSubstring(msg))
		},
		Entry("without ./", `{"commands":"c"}`, map[string]string{"c/a.md": ""}, `plugin.json: extensions.dev.getlola.commands: path "c" must start with ./`),
		Entry("not a string", `{"agents":1}`, map[string]string{}, "plugin.json: extensions.dev.getlola.agents: must be a ./ path"),
		Entry("leaving the package", `{"commands":"./../c"}`, map[string]string{}, `extensions.dev.getlola.commands: path "../c" must be relative and inside the module`),
		Entry("missing", `{"commands":"./nope"}`, map[string]string{}, `extensions.dev.getlola.commands: path "nope" does not exist`),
		Entry("a file for a directory", `{"commands":"./c.md"}`, map[string]string{"c.md": ""}, `plugin.json: extensions.dev.getlola.commands: path "./c.md" is not a directory`),
		Entry("a directory for instructions", `{"instructions":"./d"}`, map[string]string{"d/x": ""}, `plugin.json: extensions.dev.getlola.instructions: path "./d" is not a file`),
		Entry("instructions that are not markdown", `{"instructions":"./i.txt"}`, map[string]string{"i.txt": ""}, `plugin.json: extensions.dev.getlola.instructions: path "./i.txt" is not a .md file`),
		Entry("a command name lola cannot use", `{}`, map[string]string{"dev.getlola/commands/Bad Name.md": ""}, `dev.getlola/commands/Bad Name.md: command name "Bad Name" is invalid`),
	)

	It("warns about skills that is not a directory and loads the rest", func() {
		m, err := detect(`{`+apSchema+`,"name":"p"}`, map[string]string{"skills": "a file, not a directory", "commands/x": "", "dev.getlola/commands/c.md": "c"})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Skills).To(BeEmpty())
		Expect(m.Commands).To(HaveLen(1))
		Expect(m.Warnings).To(ConsistOf("skills is not a directory; skipped"))
	})

	It("keeps and ignores an unknown namespace whose data lola cannot parse", func() {
		m, err := detect(`{`+apSchema+`,"name":"p","extensions":{"x.y":{"n":1e999},"com.ok":{"n":1}}}`, map[string]string{"skills/s/SKILL.md": skill("s")})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Warnings).To(ConsistOf(
			"plugin.json: extensions: lola renders nothing for the namespace com.ok; it was ignored",
			"plugin.json: extensions: lola renders nothing for the namespace x.y; it was ignored"))
	})

	It("says what is wrong with a skill or namespace directory it skips", func() {
		write(root, map[string]string{
			"plugin.json":        `{` + apSchema + `,"name":"p"}`,
			"skills/ok/SKILL.md": skill("ok"), "real/SKILL.md": skill("real"), "real.md": "x", "elsewhere/commands/a.md": "a",
			"skills/linkedfile/other.md": skill("linkedfile"), "skills/unparsed/SKILL.md": "---\nname: [\n---\n",
			"dev.getlola/agents": "a file",
		})
		Expect(os.Symlink("../real", filepath.Join(root, "skills", "linkeddir"))).To(Succeed())
		Expect(os.Symlink("../other.md", filepath.Join(root, "skills", "linkedfile", "SKILL.md"))).To(Succeed())
		Expect(os.Symlink("../elsewhere/commands", filepath.Join(root, "dev.getlola", "commands"))).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(root, "skills", "dirskill", "SKILL.md"), 0o755)).To(Succeed())
		m, err := module.Detect(root, "fallback", targets.IDs())
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Warnings).To(ConsistOf(
			"skills/linkeddir is a symbolic link; skipped",
			"skills/linkedfile/SKILL.md is a symbolic link; skipped",
			"skills/dirskill/SKILL.md is not a regular file; skipped",
			"skills/unparsed/SKILL.md is not a valid Agent Skill: its frontmatter does not parse; skipped",
			"dev.getlola/commands is a symbolic link; skipped",
			"dev.getlola/agents is not a directory; skipped",
		))
	})

	It("coaches a package whose skills are all invalid and that holds nothing else", func() {
		_, err := detect(`{`+apSchema+`,"name":"p"}`, map[string]string{"skills/Bad/SKILL.md": skill("Bad")})
		refused(err, "plugin.json declares nothing lola can install")
		Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("skills/Bad/SKILL.md is not a valid Agent Skill")))
	})

	DescribeTable("a root plugin.json lola cannot read safely is a load error, never a fall-through to the native layout",
		func(setup func(), msg string) {
			write(root, map[string]string{"commands/a.md": "a"})
			setup()
			m, err := module.Detect(root, "fallback", targets.IDs())
			Expect(m).To(BeNil())
			refused(err, ContainSubstring(msg))
		},
		Entry("a repeated key", func() {
			write(root, map[string]string{"plugin.json": `{` + apSchema + `,"name":"a","name":"b"}`})
		}, `key "name" appears twice`),
		Entry("a file over 1 MiB", func() {
			write(root, map[string]string{"plugin.json": `{` + apSchema + `,"name":"p","description":"` + strings.Repeat("x", 1<<20) + `"}`})
		}, "is larger than 1 MiB"),
		Entry("a symbolic link", func() {
			write(root, map[string]string{"real.json": `{` + apSchema + `,"name":"p"}`})
			Expect(os.Symlink("real.json", filepath.Join(root, "plugin.json"))).To(Succeed())
		}, "plugin.json is a symbolic link"),
	)
})
