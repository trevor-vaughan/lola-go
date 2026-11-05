package gemini_test

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/target/gemini"
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

// codes lists each finding as "<severity> <code> <artifact>".
func codes(fs []target.Finding) []string {
	out := []string{}
	for _, f := range fs {
		out = append(out, string(f.Severity)+" "+f.Code+" "+f.Artifact)
	}
	return out
}

// decode reads a rendered Gemini command file.
func decode(data []byte) map[string]any {
	var v map[string]any
	_, err := toml.Decode(string(data), &v)
	Expect(err).NotTo(HaveOccurred())
	return v
}

var _ = Describe("gemini-cli", func() {
	g := gemini.Target{}
	var ctx target.PlanContext
	BeforeEach(func() {
		ctx = target.PlanContext{Scope: target.Project, Root: testenv.Isolate(), ContentDir: "/store/example/versions/abc/content",
			PluginRoot: "/proj/.lola/modules/example", PluginData: "/proj/.lola/plugin-data/example"}
	})
	plan1 := func(m *module.Module) ([]plan.Intent, []target.Finding) {
		ins, fs, err := g.Plan(m, ctx)
		Expect(err).NotTo(HaveOccurred())
		return ins, fs
	}
	commands := func(cs ...module.Command) *module.Module { return &module.Module{Name: "example", Commands: cs} }

	It("renders skills under .gemini/skills/, adding a missing name", func() {
		ins, fs := plan1(&module.Module{Name: "example", Skills: []module.Skill{
			{Name: "review", Dir: "skills/review", Doc: doc("---\ndescription: r\nlicense: MIT\n---\nRun ${PLUGIN_ROOT}/x\n")}}})
		Expect(fs).To(BeEmpty())
		Expect(ins[0]).To(Equal(plan.Intent{Target: "gemini-cli", Kind: "skill", Op: plan.OpCopyTree, Src: ctx.ContentDir + "/skills/review",
			Path: ".gemini/skills/review", Exclude: []string{"SKILL.md"}}))
		Expect(ins[1].Path).To(Equal(".gemini/skills/review/SKILL.md"))
		Expect(string(ins[1].Data)).To(Equal("---\nname: review\ndescription: r\nlicense: MIT\n---\nRun /proj/.lola/modules/example/x\n"))
	})

	It("warns that skills reach the model with Claude's shell and argument placeholders as written", func() {
		bodies := map[string]string{
			"inline":  "Diff:\n!`git diff`\n",
			"fence":   "Status:\n```!\ngit status\n```\n",
			"args":    "Fix $ARGUMENTS[0] in $1 for $ARGUMENTS\n",
			"doubled": "Path C:\\\\$1\n",
			"plain":   "Costs \\$1.00; set KEY=!`x`, $HOME and $ARGUMENTS[x]\n",
		}
		var ss []module.Skill
		for _, n := range []string{"inline", "fence", "args", "doubled", "plain"} {
			ss = append(ss, module.Skill{Name: n, Dir: "skills/" + n, Doc: doc("---\ndescription: d\n---\n" + bodies[n])})
		}
		ins, fs := plan1(&module.Module{Name: "example", Skills: ss})
		for n, body := range bodies {
			Expect(string(byPath(ins)[".gemini/skills/"+n+"/SKILL.md"].Data)).To(Equal("---\nname: "+n+"\ndescription: d\n---\n"+body), "rendered unchanged")
		}
		Expect(codes(fs)).To(Equal([]string{"warn skill-shell-dropped inline", "warn skill-shell-dropped fence",
			"warn skill-arguments-dropped args", "warn skill-arguments-dropped doubled"}), "an escaped $ or one after a character is not a placeholder")
		Expect(fs[0]).To(Equal(target.Finding{Severity: target.SeverityWarn, Target: "gemini-cli", Kind: "skill", Artifact: "inline", Code: "skill-shell-dropped",
			Text: "gemini-cli does not run shell in skills; it reaches the model as text",
			Hint: `wrap the shell in {% if target != "gemini-cli" %}…{% endif %} in the skill's SKILL.md.liquid template`}))
		Expect(fs[2]).To(Equal(target.Finding{Severity: target.SeverityWarn, Target: "gemini-cli", Kind: "skill", Artifact: "args", Code: "skill-arguments-dropped",
			Text: "gemini-cli does not fill argument placeholders in skills; they reach the model as written",
			Hint: `wrap the placeholders in {% if target != "gemini-cli" %}…{% endif %} in the skill's SKILL.md.liquid template`}))
	})

	It("skips a skill without a description, and renders it with Force", func() {
		m := &module.Module{Name: "example", Skills: []module.Skill{{Name: "review", Dir: "skills/review", Doc: doc("Review.\n")}}}
		ins, fs := plan1(m)
		Expect(ins).To(BeEmpty())
		Expect(fs).To(Equal([]target.Finding{{Severity: target.SeveritySkip, Target: "gemini-cli", Kind: "skill", Artifact: "review",
			Code: "required-key-missing", Text: "gemini-cli needs a description in the skill's frontmatter",
			Hint: "add description: to the skill's frontmatter", Forceable: true}}))
		ctx.Force = true
		ins, fs = plan1(m)
		Expect(string(byPath(ins)[".gemini/skills/review/SKILL.md"].Data)).To(Equal("---\nname: review\n---\nReview.\n"))
		Expect(fs).To(ConsistOf(HaveField("Forced", true)))
	})

	It("encodes commands as TOML with {{args}}, drops keys Gemini does not read, and copies the sidecar directory", func() {
		ins, fs := plan1(commands(
			module.Command{Name: "ship", SidecarDir: "commands/ship", Doc: doc("---\ndescription: Ship it\nallowed-tools: Bash\n---\nShip $ARGUMENTS from ${PLUGIN_ROOT}\n")},
			module.Command{Name: "bare", Doc: doc("Just do it\n")},
		))
		Expect(codes(fs)).To(Equal([]string{"warn frontmatter-key-dropped ship", "info gemini-cli/arguments ship"}))
		p := byPath(ins)
		Expect(p[".gemini/commands/ship.toml"].Mode).To(Equal(os.FileMode(0o644)))
		Expect(decode(p[".gemini/commands/ship.toml"].Data)).To(Equal(map[string]any{
			"description": "Ship it", "prompt": "Ship {{args}} from /proj/.lola/modules/example\n"}))
		Expect(p[".gemini/commands/ship"]).To(Equal(plan.Intent{Target: "gemini-cli", Kind: "command", Op: plan.OpCopyTree,
			Src: ctx.ContentDir + "/commands/ship", Path: ".gemini/commands/ship"}))
		Expect(decode(p[".gemini/commands/bare.toml"].Data)).To(Equal(map[string]any{"prompt": "Just do it\n"}))
	})

	It("gemini TOML preserves backslashes", func() {
		body := "Match \\d+ in C:\\Users\\me and keep \"quotes\" and ''' triples\n"
		ins, _ := plan1(commands(module.Command{Name: "go", Doc: doc(body)}))
		Expect(decode(ins[0].Data)["prompt"]).To(Equal(body))
	})

	It("leaves positional arguments as written, prefixes the whole argument string and warns", func() {
		ins, fs := plan1(commands(module.Command{Name: "fix", Doc: doc("Fix issue $1 in $2\n")}))
		Expect(decode(ins[0].Data)["prompt"]).To(Equal("Arguments: {{args}}\n\nFix issue $1 in $2\n"))
		Expect(fs).To(Equal([]target.Finding{{Severity: target.SeverityWarn, Target: "gemini-cli", Kind: "command", Artifact: "fix",
			Code: "gemini-cli/positional-arguments", Hint: "write $ARGUMENTS in place of $N, $ARGUMENTS[N] and declared names; gemini-cli fills it with the whole argument string",
			Text: `gemini-cli has no positional or named arguments; they are left as written and the prompt starts with "Arguments: {{args}}"`}}))
	})

	It("treats Claude's 0-based $0, $10 and $ARGUMENTS[N] as positional, never as {{args}}", func() {
		for _, body := range []string{"Fix $0\n", "Fix $10\n", "Fix $ARGUMENTS[0]\n"} {
			ins, fs := plan1(commands(module.Command{Name: "fix", Doc: doc(body)}))
			Expect(decode(ins[0].Data)["prompt"]).To(Equal("Arguments: {{args}}\n\n"+body), body)
			Expect(codes(fs)).To(Equal([]string{"warn gemini-cli/positional-arguments fix"}), body)
		}
	})

	It("rewrites $ARGUMENTS beside a positional argument and prefixes the prompt once", func() {
		ins, fs := plan1(commands(module.Command{Name: "fix", Doc: doc("Fix $ARGUMENTS, first $1 then $2\n")}))
		Expect(decode(ins[0].Data)["prompt"]).To(Equal("Arguments: {{args}}\n\nFix {{args}}, first $1 then $2\n"))
		Expect(codes(fs)).To(Equal([]string{"info gemini-cli/arguments fix", "warn gemini-cli/positional-arguments fix"}))
	})

	It("treats names declared in arguments: as positional, and leaves undeclared ones alone", func() {
		ins, fs := plan1(commands(
			module.Command{Name: "fix", Doc: doc("---\narguments: [issue]\n---\nFix $issue in $HOME\n")},
			module.Command{Name: "move", Doc: doc("---\narguments: from to\n---\nMove $from to $to and $1\n")},
			module.Command{Name: "home", Doc: doc("Go $HOME\n")},
			module.Command{Name: "plural", Doc: doc("---\narguments: [issue]\n---\nList $issues and $ARGUMENTS[x]\n")},
		))
		p := byPath(ins)
		Expect(decode(p[".gemini/commands/fix.toml"].Data)["prompt"]).To(Equal("Arguments: {{args}}\n\nFix $issue in $HOME\n"))
		Expect(decode(p[".gemini/commands/move.toml"].Data)["prompt"]).To(Equal("Arguments: {{args}}\n\nMove $from to $to and $1\n"))
		Expect(decode(p[".gemini/commands/home.toml"].Data)["prompt"]).To(Equal("Go $HOME\n"))
		Expect(decode(p[".gemini/commands/plural.toml"].Data)["prompt"]).To(Equal("List $issues and $ARGUMENTS[x]\n"),
			"$issues is not the declared $issue, and $ARGUMENTS[x] has no numeric index")
		Expect(codes(fs)).To(Equal([]string{
			"warn frontmatter-key-dropped fix", "warn gemini-cli/positional-arguments fix",
			"warn frontmatter-key-dropped move", "warn gemini-cli/positional-arguments move",
			"warn frontmatter-key-dropped plural",
		}), "one positional-arguments finding per command")
		Expect(fs[1].Text).To(Equal(`gemini-cli has no positional or named arguments; they are left as written and the prompt starts with "Arguments: {{args}}"`))
		Expect(fs[1].Hint).To(Equal("write $ARGUMENTS in place of $N, $ARGUMENTS[N] and declared names; gemini-cli fills it with the whole argument string"))
	})

	It("drops the backslash of a Claude-escaped placeholder, which Gemini would show the model", func() {
		ins, fs := plan1(commands(
			module.Command{Name: "price", Doc: doc("Costs \\$1.00, see \\$ARGUMENTS and \\$ARGUMENTS[0] or \\$99999999999999999999\n")},
			module.Command{Name: "named", Doc: doc("---\narguments: [issue]\n---\nSee \\$issue in \\$HOME and \\$ARGUMENTS[x]\n")},
		))
		p := byPath(ins)
		Expect(decode(p[".gemini/commands/price.toml"].Data)["prompt"]).To(Equal("Costs $1.00, see $ARGUMENTS and $ARGUMENTS[0] or $99999999999999999999\n"))
		Expect(decode(p[".gemini/commands/named.toml"].Data)["prompt"]).To(Equal("See $issue in \\$HOME and \\$ARGUMENTS[x]\n"),
			"a backslash before any other $ is not an escape")
		Expect(codes(fs)).To(Equal([]string{"warn frontmatter-key-dropped named"}))
	})

	It("reads a doubled backslash as two literal ones, so the placeholder after it is still filled", func() {
		ins, fs := plan1(commands(
			module.Command{Name: "fix", Doc: doc("Path C:\\\\$0\n")},
			module.Command{Name: "all", Doc: doc("Path C:\\\\$ARGUMENTS\n")},
		))
		p := byPath(ins)
		Expect(decode(p[".gemini/commands/fix.toml"].Data)["prompt"]).To(Equal("Arguments: {{args}}\n\nPath C:\\\\$0\n"))
		Expect(decode(p[".gemini/commands/all.toml"].Data)["prompt"]).To(Equal("Path C:\\\\{{args}}\n"))
		Expect(codes(fs)).To(Equal([]string{"warn gemini-cli/positional-arguments fix", "info gemini-cli/arguments all"}))
	})

	It("skips a command that runs shell with !`…`, which Gemini would send to the model as text", func() {
		m := commands(module.Command{Name: "status", Doc: doc("---\ndescription: s\n---\nSummarize !`git status`\n")})
		ins, fs := plan1(m)
		Expect(ins).To(BeEmpty())
		Expect(fs).To(Equal([]target.Finding{{Severity: target.SeveritySkip, Target: "gemini-cli", Kind: "command", Artifact: "status",
			Code: "gemini-cli/shell-injection", Text: "gemini-cli does not run !`…`; the command would send it to the model as text",
			Hint: "write the shell as Gemini's !{…} for gemini-cli in the command's .md.liquid template: " +
				"{% if target == \"gemini-cli\" %}!{git status}{% else %}!`git status`{% endif %}", Forceable: true}}))
		ctx.Force = true
		ins, fs = plan1(m)
		Expect(decode(ins[0].Data)["prompt"]).To(Equal("Summarize !`git status`\n"), "rendered as written, never converted")
		Expect(fs).To(ConsistOf(SatisfyAll(HaveField("Forced", true), HaveField("Severity", target.SeverityWarn))))
	})

	It("finds shell only where Claude runs it: inline at a line start or after whitespace, or a ```! fence", func() {
		ins, fs := plan1(commands(
			module.Command{Name: "start", Doc: doc("!`git status`\n")},
			module.Command{Name: "tab", Doc: doc("Status:\t!`git status`\n")},
			module.Command{Name: "fence", Doc: doc("Status:\n```!\ngit status\n```\n")},
			module.Command{Name: "long", Doc: doc("Status:\n  ````!\ngit status\n````\n")},
			module.Command{Name: "literal", Doc: doc("Set KEY=!`cmd` in the file\n")},
		))
		Expect(codes(fs)).To(Equal([]string{"skip gemini-cli/shell-injection start", "skip gemini-cli/shell-injection tab",
			"skip gemini-cli/shell-injection fence", "skip gemini-cli/shell-injection long"}))
		Expect(ins).To(HaveLen(1))
		Expect(decode(ins[0].Data)["prompt"]).To(Equal("Set KEY=!`cmd` in the file\n"))
	})

	It("keeps only the agent keys Gemini reads, with the shapes it needs, and adds a missing name", func() {
		ins, fs := plan1(&module.Module{Name: "example", Agents: []module.Agent{
			{Name: "rev", Doc: doc("---\ndescription: reviews\nmodel: opus\ncolor: red\ntemperature: 0.2\n---\nYou review.\n")},
			{Name: "fast", Doc: doc("---\nname: fast\ndescription: quick\nmodel: gemini-2.5-flash\ntools:\n  - read_file\nmax_turns: 5\n---\nGo.\n")},
		}})
		p := byPath(ins)
		Expect(string(p[".gemini/agents/rev.md"].Data)).To(Equal("---\nname: rev\ndescription: reviews\ntemperature: 0.2\n---\nYou review.\n"))
		Expect(string(p[".gemini/agents/fast.md"].Data)).To(Equal("---\nname: fast\ndescription: quick\nmodel: gemini-2.5-flash\ntools:\n  - read_file\nmax_turns: 5\n---\nGo.\n"))
		Expect(codes(fs)).To(Equal([]string{"warn frontmatter-key-dropped rev", "warn frontmatter-value-dropped rev"}))
		Expect(fs[1].Text).To(Equal("model dropped: gemini-cli needs a string matching ^gemini-"))
	})

	It("skips an agent whose tool list Gemini cannot enforce, and with Force renders it without the list", func() {
		m := &module.Module{Name: "example", Agents: []module.Agent{
			{Name: "claude", Doc: doc("---\ndescription: d\ntools: [Read, Grep]\n---\nC.\n")},
			{Name: "string", Doc: doc("---\ndescription: d\ntools: Read, Grep\n---\nS.\n")},
			{Name: "mcp", Doc: doc("---\ndescription: d\ntools: [read_file, mcp__gh__issues]\n---\nM.\n")},
			{Name: "native", Doc: doc("---\ndescription: d\ntools: [read_file, \"*\", mcp_gh_issues]\n---\nN.\n")},
		}}
		ins, fs := plan1(m)
		Expect(ins).To(HaveLen(1))
		Expect(string(ins[0].Data)).To(Equal("---\nname: native\ndescription: d\ntools: [read_file, \"*\", mcp_gh_issues]\n---\nN.\n"))
		Expect(codes(fs)).To(Equal([]string{"skip agent-tools-unenforceable claude", "skip agent-tools-unenforceable string",
			"skip agent-tools-unenforceable mcp"}), "Claude's names, a Claude comma-separated string and mcp__server__tool")
		Expect(fs[0]).To(Equal(target.Finding{Severity: target.SeveritySkip, Target: "gemini-cli", Kind: "agent", Artifact: "claude",
			Code: "agent-tools-unenforceable", Text: "gemini-cli cannot enforce this agent's tool list; without it the agent can use every tool",
			Hint: "under gemini-cli: in the agent's .targets.yaml, list its tools by gemini-cli's names, such as read_file", Forceable: true}))
		ctx.Force = true
		ins, fs = plan1(m)
		Expect(string(byPath(ins)[".gemini/agents/claude.md"].Data)).To(Equal("---\nname: claude\ndescription: d\n---\nC.\n"))
		Expect(fs).To(HaveEach(SatisfyAll(HaveField("Forced", true), HaveField("Severity", target.SeverityWarn))))
	})

	It("drops agent values Gemini's strict agent schema would reject, so the agent still loads", func() {
		ins, fs := plan1(&module.Module{Name: "example", Agents: []module.Agent{
			{Name: "claude", Doc: doc("---\ndescription: d\nkind: weird\nmax_turns: 1.5\ntimeout_mins: 0\n---\nC.\n")},
			{Name: "native", Doc: doc("---\ndescription: d\nkind: local\nmax_turns: 5\ntimeout_mins: 10\n---\nN.\n")},
			{Name: "whole", Doc: doc("---\ndescription: d\nkind: remote\nmax_turns: 5.0\n---\nW.\n")},
		}})
		p := byPath(ins)
		Expect(string(p[".gemini/agents/claude.md"].Data)).To(Equal("---\nname: claude\ndescription: d\n---\nC.\n"))
		Expect(string(p[".gemini/agents/native.md"].Data)).To(Equal("---\nname: native\ndescription: d\nkind: local\nmax_turns: 5\ntimeout_mins: 10\n---\nN.\n"))
		Expect(string(p[".gemini/agents/whole.md"].Data)).To(Equal("---\nname: whole\ndescription: d\nkind: remote\nmax_turns: 5.0\n---\nW.\n"))
		Expect(codes(fs)).To(Equal([]string{"warn frontmatter-value-dropped claude", "warn frontmatter-value-dropped claude",
			"warn frontmatter-value-dropped claude"}))
		Expect(fs[0].Text).To(Equal("kind dropped: gemini-cli needs a string matching ^(local|remote)$"))
		Expect(fs[1].Text).To(Equal("max_turns dropped: gemini-cli needs a positive-integer"))
		Expect(fs[2].Text).To(Equal("timeout_mins dropped: gemini-cli needs a positive-integer"))
	})

	It("skips an agent whose name Gemini rejects or that has no description; a rejected name key is replaced", func() {
		ins, fs := plan1(&module.Module{Name: "example", Agents: []module.Agent{
			{Name: "v1.2", Doc: doc("---\ndescription: d\n---\nx\n")},
			{Name: "rev", Doc: doc("You review.\n")},
			{Name: "review", Doc: doc("---\nname: Rev\ndescription: d\n---\nx\n")},
		}})
		Expect(codes(fs)).To(Equal([]string{"skip name-rejected v1.2", "skip required-key-missing rev", "warn frontmatter-value-dropped review"}))
		Expect(fs[0].Text).To(Equal("gemini-cli needs agent names matching ^[a-z0-9_-]+$"))
		Expect(fs[0].Forceable).To(BeTrue())
		Expect(ins).To(HaveLen(1))
		Expect(string(ins[0].Data)).To(Equal("---\nname: review\ndescription: d\n---\nx\n"))
	})

	It("renders MCP servers in settings.json shape, keeping cwd", func() {
		ins, fs := plan1(&module.Module{Name: "example", MCPServers: []module.MCPServer{
			{Name: "gh", Transport: "stdio", Command: "${PLUGIN_ROOT}/bin/gh", Args: []string{"--data", "${PLUGIN_DATA}"}, Env: map[string]string{"T": "1"}, Cwd: "${PLUGIN_ROOT}"},
			{Name: "web", Transport: "http", URL: "https://x/mcp", Headers: map[string]string{"A": "b"}},
			{Name: "ev", Transport: "sse", URL: "https://x/sse"},
		}})
		Expect(fs).To(BeEmpty())
		p := byPath(ins)
		Expect(p[".gemini/settings.json/mcpServers/gh"].Value).To(MatchJSON(`{"command":"/proj/.lola/modules/example/bin/gh",
			"args":["--data","/proj/.lola/plugin-data/example"],"env":{"T":"1"},"cwd":"/proj/.lola/modules/example"}`))
		Expect(p[".gemini/settings.json/mcpServers/web"].Value).To(MatchJSON(`{"httpUrl":"https://x/mcp","headers":{"A":"b"}}`))
		Expect(p[".gemini/settings.json/mcpServers/ev"].Value).To(MatchJSON(`{"url":"https://x/sse"}`))
		ctx.Scope = target.User
		ins, _ = plan1(&module.Module{Name: "example", MCPServers: []module.MCPServer{{Name: "ev", Transport: "sse", URL: "https://x/sse"}}})
		Expect(ins[0].Path).To(Equal(".gemini/settings.json"), "relative to $HOME at user scope")
	})

	It("joins a module's rules into one verbatim GEMINI.md and lists its directory in includeDirectories", func() {
		m := &module.Module{Name: "example", Rules: []module.Rule{{Name: "a", Body: "Keep ${PLUGIN_ROOT}.\n\n"}, {Name: "b", Body: "Be brief."}}}
		ins, fs := plan1(m)
		Expect(fs).To(BeEmpty())
		Expect(ins).To(Equal([]plan.Intent{
			{Target: "gemini-cli", Kind: "rule", Op: plan.OpWriteFile, Path: ".gemini/lola/example/GEMINI.md", Data: []byte("Keep ${PLUGIN_ROOT}.\n\nBe brief.\n"), Mode: 0o644},
			{Target: "gemini-cli", Kind: "rule", Op: plan.OpAddJSONMember, Path: ".gemini/settings.json", Pointer: "/context/includeDirectories", Member: plan.StringMember(".gemini/lola/example")},
		}))

		ctx.Scope, ctx.Root = target.User, "/home/u"
		ins, _ = plan1(m)
		Expect(ins[0].Path).To(Equal(".gemini/lola/example/GEMINI.md"))
		Expect(ins[1].Member).To(Equal(plan.StringMember("/home/u/.gemini/lola/example")), "an absolute member: user settings apply in every project")

		ctx.SkipRules = true
		ins, _ = plan1(m)
		Expect(ins).To(BeEmpty())
	})

	It("detects .gemini/ or GEMINI.md, roots user scope at $HOME and reads gemini's bare version", func() {
		dir := testenv.Isolate()
		Expect(g.Detect(target.Project, dir)).To(BeFalse())
		Expect(os.WriteFile(filepath.Join(dir, "GEMINI.md"), nil, 0o644)).To(Succeed())
		Expect(g.Detect(target.Project, dir)).To(BeTrue())
		home := filepath.Join(dir, "home")
		Expect(g.Detect(target.User, home)).To(BeFalse())
		Expect(os.Mkdir(filepath.Join(home, ".gemini"), 0o700)).To(Succeed())
		Expect(g.Detect(target.User, home)).To(BeTrue())
		Expect(g.Root(target.User, "/proj", paths.Paths{Home: "/home/u"})).To(Equal("/home/u"))
		Expect(g.Root(target.Project, "/proj", paths.Paths{Home: "/home/u"})).To(Equal("/proj"))
		p := g.Probe()
		Expect(p.Binaries).To(Equal([]string{"gemini"}))
		Expect(p.VersionRE.FindStringSubmatch("0.63.0\n")).To(Equal([]string{"0.63.0", "0.63.0"}))
	})
})
