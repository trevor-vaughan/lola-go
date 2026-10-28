package opencode_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/target/opencode"
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

var _ = Describe("opencode", func() {
	o := opencode.Target{}
	var ctx target.PlanContext
	BeforeEach(func() {
		ctx = target.PlanContext{Scope: target.Project, Root: testenv.Isolate(), ContentDir: "/store/example/versions/abc/content",
			PluginRoot: "/proj/.lola/modules/example", PluginData: "/proj/.lola/plugin-data/example"}
	})
	plan1 := func(m *module.Module) ([]plan.Intent, []target.Finding) {
		ins, fs, err := o.Plan(m, ctx)
		Expect(err).NotTo(HaveOccurred())
		return ins, fs
	}
	skills := func(ss ...module.Skill) *module.Module { return &module.Module{Name: "example", Skills: ss} }
	mcp := func(ss ...module.MCPServer) *module.Module { return &module.Module{Name: "example", MCPServers: ss} }

	It("renders skills under .opencode/skills/, adding a missing name", func() {
		ins, fs := plan1(skills(
			module.Skill{Name: "review", Dir: "skills/review", Doc: doc("---\ndescription: r\n---\nRun ${PLUGIN_ROOT}/x\n")},
			module.Skill{Name: "lint", Dir: "skills/lint", Doc: doc("---\nname: lint\ndescription: l\nlicense: MIT\n---\nLint.\n")},
		))
		Expect(fs).To(BeEmpty())
		p := byPath(ins)
		Expect(p[".opencode/skills/review"]).To(Equal(plan.Intent{Target: "opencode", Kind: "skill", Op: plan.OpCopyTree, Src: ctx.ContentDir + "/skills/review",
			Path: ".opencode/skills/review", Exclude: []string{"SKILL.md"}}))
		Expect(string(p[".opencode/skills/review/SKILL.md"].Data)).To(Equal("---\nname: review\ndescription: r\n---\nRun /proj/.lola/modules/example/x\n"))
		Expect(string(p[".opencode/skills/lint/SKILL.md"].Data)).To(Equal("---\nname: lint\ndescription: l\nlicense: MIT\n---\nLint.\n"))
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
			Expect(string(byPath(ins)[".opencode/skills/"+n+"/SKILL.md"].Data)).To(Equal("---\nname: "+n+"\ndescription: d\n---\n"+body), "rendered unchanged")
		}
		Expect(codes(fs)).To(Equal([]string{"warn skill-shell-dropped inline", "warn skill-shell-dropped fence",
			"warn skill-arguments-dropped args", "warn skill-arguments-dropped doubled"}), "an escaped $ or one after a character is not a placeholder")
		Expect(fs[0]).To(Equal(target.Finding{Severity: target.SeverityWarn, Target: "opencode", Kind: "skill", Artifact: "inline", Code: "skill-shell-dropped",
			Text: "opencode does not run shell in skills; it reaches the model as text",
			Hint: `wrap the shell in {% if target != "opencode" %}…{% endif %} in the skill's SKILL.md.liquid template`}))
		Expect(fs[2]).To(Equal(target.Finding{Severity: target.SeverityWarn, Target: "opencode", Kind: "skill", Artifact: "args", Code: "skill-arguments-dropped",
			Text: "opencode does not fill argument placeholders in skills; they reach the model as written",
			Hint: `wrap the placeholders in {% if target != "opencode" %}…{% endif %} in the skill's SKILL.md.liquid template`}))
	})

	It("skips, forceably, skills OpenCode would reject", func() {
		m := skills(
			module.Skill{Name: "review", Dir: "skills/review", Doc: doc("Review.\n")},
			module.Skill{Name: "my_skill", Dir: "skills/my_skill", Doc: doc("---\ndescription: d\n---\nx\n")},
			module.Skill{Name: "a--b", Dir: "skills/a--b", Doc: doc("---\ndescription: d\n---\nx\n")},
			module.Skill{Name: "lint", Dir: "skills/lint", Doc: doc("---\nname: linter\ndescription: d\n---\nx\n")},
		)
		ins, fs := plan1(m)
		Expect(ins).To(BeEmpty())
		Expect(codes(fs)).To(Equal([]string{"skip required-key-missing review", "skip name-rejected my_skill", "skip name-rejected a--b", "skip name-rejected lint"}))
		Expect(fs[1].Text).To(Equal("opencode needs skill names matching ^[a-z0-9]+(-[a-z0-9]+)*$"))
		Expect(fs[3]).To(Equal(target.Finding{Severity: target.SeveritySkip, Target: "opencode", Kind: "skill", Artifact: "lint", Code: "name-rejected",
			Text: "opencode needs the skill's name (linter) to equal its directory name", Hint: "rename the directory to linter, or set name: lint",
			Forceable: true}))
		ctx.Force = true
		ins, fs = plan1(m)
		Expect(string(byPath(ins)[".opencode/skills/lint/SKILL.md"].Data)).To(Equal("---\nname: linter\ndescription: d\n---\nx\n"), "rendered as written")
		Expect(fs).To(HaveEach(HaveField("Forced", true)))
	})

	It("keeps only the command keys OpenCode reads, passes $ARGUMENTS and shell through and leaves the sidecar out", func() {
		ins, fs := plan1(&module.Module{Name: "example", Commands: []module.Command{
			{Name: "ship", SidecarDir: "commands/ship", Doc: doc("---\ndescription: Ship it\nallowed-tools: Bash\nmodel: anthropic/claude-sonnet-5-5\nsubtask: true\n---\nShip $ARGUMENTS !`git status`\n")},
			{Name: "bare", Doc: doc("Just do it\n")},
			{Name: "fast", Doc: doc("---\nmodel: haiku\n---\nGo\n")},
		}})
		Expect(ins).To(HaveLen(3), "OpenCode would read a sidecar's markdown files as commands")
		p := byPath(ins)
		Expect(string(p[".opencode/commands/ship.md"].Data)).To(Equal("---\ndescription: Ship it\nmodel: anthropic/claude-sonnet-5-5\nsubtask: true\n---\nShip $ARGUMENTS !`git status`\n"))
		Expect(string(p[".opencode/commands/bare.md"].Data)).To(Equal("Just do it\n"))
		Expect(string(p[".opencode/commands/fast.md"].Data)).To(Equal("Go\n"))
		Expect(codes(fs)).To(Equal([]string{"warn frontmatter-key-dropped ship", "warn opencode/shell-unchecked ship", "warn command-sidecar-dropped ship",
			"warn frontmatter-value-dropped fast"}))
		Expect(fs[2].Text).To(Equal("opencode would read the files in commands/ship/ as commands; the sidecar directory is not copied"))
	})

	It("warns that OpenCode runs inline !`…` anywhere without permission checks, and skips a ```! fence it would not run", func() {
		m := &module.Module{Name: "example", Commands: []module.Command{
			{Name: "inline", Doc: doc("Set KEY=!`whoami` in the file\n")},
			{Name: "fence", Doc: doc("Status:\n  ````!\ngit status\n````\n")},
			{Name: "plain", Doc: doc("Use `code`, a ```go fence and !important\n```go\nx\n```\n")},
		}}
		ins, fs := plan1(m)
		Expect(byPath(ins)).To(HaveKey(".opencode/commands/inline.md"))
		Expect(byPath(ins)).To(HaveKey(".opencode/commands/plain.md"))
		Expect(byPath(ins)).NotTo(HaveKey(".opencode/commands/fence.md"))
		Expect(fs).To(Equal([]target.Finding{
			{Severity: target.SeverityWarn, Target: "opencode", Kind: "command", Artifact: "inline", Code: "opencode/shell-unchecked",
				Text: "OpenCode runs !`…` without Claude's permission checks (allowed-tools is dropped)",
				Hint: "if the shell needs Claude's permission checks, wrap it in {% if target != \"opencode\" %}…{% endif %} in the command's .md.liquid template"},
			{Severity: target.SeveritySkip, Target: "opencode", Kind: "command", Artifact: "fence", Code: "opencode/shell-fence",
				Text: "OpenCode does not run fenced shell; the command would send it to the model as text", Hint: "use inline !`…`", Forceable: true},
		}), "a command without shell gets neither finding")
		ctx.Force = true
		ins, fs = plan1(m)
		Expect(string(byPath(ins)[".opencode/commands/fence.md"].Data)).To(Equal("Status:\n  ````!\ngit status\n````\n"), "rendered as written")
		Expect(fs[1]).To(SatisfyAll(HaveField("Forced", true), HaveField("Severity", target.SeverityWarn)))
	})

	It("renumbers Claude's 0-based argument placeholders to OpenCode's 1-based ones in one pass", func() {
		ins, fs := plan1(&module.Module{Name: "example", Commands: []module.Command{
			{Name: "fix", Doc: doc("Fix $0 in $1, then $ARGUMENTS[2] and $9\n")},
			{Name: "move", Doc: doc("---\ndescription: m\narguments: [from, branch]\n---\nMove $from to $branch, not $HOME\n")},
			{Name: "copy", Doc: doc("---\narguments: src dst\n---\nCopy $src to $dst\n")},
			{Name: "all", Doc: doc("---\narguments: [issue]\n---\nDo $ARGUMENTS for $HOME, $issues, $ARGUMENTS[x] and $99999999999999999999\n")},
		}})
		p := byPath(ins)
		Expect(string(p[".opencode/commands/fix.md"].Data)).To(Equal("Fix $1 in $2, then $3 and $10\n"), "$0 becomes $1 and stays $1")
		Expect(string(p[".opencode/commands/move.md"].Data)).To(Equal("---\ndescription: m\n---\nMove $1 to $2, not $HOME\n"))
		Expect(string(p[".opencode/commands/copy.md"].Data)).To(Equal("Copy $1 to $2\n"))
		Expect(string(p[".opencode/commands/all.md"].Data)).To(Equal("Do $ARGUMENTS for $HOME, $issues, $ARGUMENTS[x] and $99999999999999999999\n"),
			"only declared names, numeric indexes that fit an int and a bare $ARGUMENTS are placeholders")
		Expect(codes(fs)).To(Equal([]string{"warn opencode/arguments-renumbered fix", "warn frontmatter-key-dropped move",
			"warn opencode/arguments-renumbered move", "warn frontmatter-key-dropped copy", "warn opencode/arguments-renumbered copy",
			"warn frontmatter-key-dropped all"}), "no renumbering warn for a command that did not change")
		Expect(fs[0]).To(Equal(target.Finding{Severity: target.SeverityWarn, Target: "opencode", Kind: "command", Artifact: "fix",
			Code: "opencode/arguments-renumbered", Text: "Claude's 0-based argument placeholders became OpenCode's 1-based $1, $2, …",
			Hint: "check the command with a few arguments; OpenCode may split or join them differently"}))
	})

	It("keeps Claude-escaped placeholders, which OpenCode cannot escape, and warns once per command", func() {
		ins, fs := plan1(&module.Module{Name: "example", Commands: []module.Command{
			{Name: "price", Doc: doc("Costs \\$1.00 or \\$2.00\n")},
			{Name: "raw", Doc: doc("Say \\$ARGUMENTS and \\$ARGUMENTS[0]\n")},
			{Name: "mix", Doc: doc("Fix $0, not \\$1\n")},
			{Name: "named", Doc: doc("---\narguments: [issue]\n---\nSee \\$issue\n")},
		}})
		p := byPath(ins)
		Expect(string(p[".opencode/commands/price.md"].Data)).To(Equal("Costs \\$1.00 or \\$2.00\n"))
		Expect(string(p[".opencode/commands/raw.md"].Data)).To(Equal("Say \\$ARGUMENTS and \\$ARGUMENTS[0]\n"))
		Expect(string(p[".opencode/commands/mix.md"].Data)).To(Equal("Fix $1, not \\$1\n"))
		Expect(string(p[".opencode/commands/named.md"].Data)).To(Equal("See \\$issue\n"))
		Expect(codes(fs)).To(Equal([]string{"warn opencode/escaped-arguments price", "warn opencode/escaped-arguments raw",
			"warn opencode/arguments-renumbered mix", "warn opencode/escaped-arguments mix", "warn frontmatter-key-dropped named"}),
			"OpenCode has no named arguments, so an escaped $name is safe")
		Expect(fs[0]).To(Equal(target.Finding{Severity: target.SeverityWarn, Target: "opencode", Kind: "command", Artifact: "price",
			Code: "opencode/escaped-arguments", Text: `OpenCode has no escape for $; \$1 and \$ARGUMENTS will still be replaced by arguments`,
			Hint: "reword the text so it does not contain $ followed by a digit or ARGUMENTS"}))
	})

	It("reads a doubled backslash as two literal ones, so the placeholder after it is still renumbered", func() {
		ins, fs := plan1(&module.Module{Name: "example", Commands: []module.Command{{Name: "fix", Doc: doc("Path C:\\\\$0\n")}}})
		Expect(string(ins[0].Data)).To(Equal("Path C:\\\\$1\n"))
		Expect(codes(fs)).To(Equal([]string{"warn opencode/arguments-renumbered fix"}), "not an escaped placeholder")
	})

	It("reads the config file only for a module with MCP servers or rules", func() {
		ctx.Root = filepath.Join(ctx.Root, "file")
		Expect(os.WriteFile(ctx.Root, nil, 0o644)).To(Succeed())
		ins, _ := plan1(&module.Module{Name: "example",
			Skills:   []module.Skill{{Name: "review", Dir: "skills/review", Doc: doc("---\ndescription: r\n---\nR\n")}},
			Commands: []module.Command{{Name: "go", Doc: doc("Go\n")}}})
		Expect(ins).To(HaveLen(3))
		_, _, err := o.Plan(mcp(module.MCPServer{Name: "ev", Transport: "sse", URL: "https://x/sse"}), ctx)
		Expect(err).To(MatchError(HavePrefix("opencode: cannot read " + filepath.Join(ctx.Root, "opencode.jsonc") + ": ")))
	})

	It("makes agents subagents, drops name, and keeps only provider/model models", func() {
		ins, fs := plan1(&module.Module{Name: "example", Agents: []module.Agent{
			{Name: "rev", Doc: doc("---\nname: rev\ndescription: reviews\nmodel: opus\ncolor: red\n---\nYou review.\n")},
			{Name: "lead", Doc: doc("---\ndescription: leads\nmode: primary\nmodel: anthropic/claude-opus-5-5\n---\nLead.\n")},
		}})
		p := byPath(ins)
		Expect(string(p[".opencode/agents/rev.md"].Data)).To(Equal("---\nmode: subagent\ndescription: reviews\n---\nYou review.\n"))
		Expect(string(p[".opencode/agents/lead.md"].Data)).To(Equal("---\ndescription: leads\nmode: primary\nmodel: anthropic/claude-opus-5-5\n---\nLead.\n"))
		Expect(fs).To(Equal([]target.Finding{
			{Severity: target.SeverityWarn, Target: "opencode", Kind: "agent", Artifact: "rev", Code: "frontmatter-value-dropped",
				Text: "color dropped: opencode needs a string matching ^#[0-9a-fA-F]{6}$",
				Hint: "in rev.targets.yaml beside the agent, set opencode's color to a string matching ^#[0-9a-fA-F]{6}$, or to null to leave it out"},
			{Severity: target.SeverityWarn, Target: "opencode", Kind: "agent", Artifact: "rev", Code: "frontmatter-value-dropped", Text: "model dropped: opencode needs a string matching ^[^/]+/.+$",
				Hint: "in rev.targets.yaml beside the agent, set opencode's model to a string matching ^[^/]+/.+$, or to null to leave it out"},
			{Severity: target.SeverityInfo, Target: "opencode", Kind: "agent", Artifact: "rev", Code: "frontmatter-key-dropped", Text: "name dropped: the file name names the agent"},
		}))
	})

	It("skips an agent with a tool list, which OpenCode cannot express, and with Force renders it without the list", func() {
		m := &module.Module{Name: "example", Agents: []module.Agent{
			{Name: "list", Doc: doc("---\ndescription: d\ntools: [Read, Grep]\n---\nL.\n")},
			{Name: "string", Doc: doc("---\ndescription: d\ntools: Read, Grep\n---\nS.\n")},
			{Name: "empty", Doc: doc("---\ndescription: d\ntools: []\n---\nE.\n")},
			{Name: "none", Doc: doc("---\ndescription: d\n---\nN.\n")},
		}}
		ins, fs := plan1(m)
		p := byPath(ins)
		Expect(ins).To(HaveLen(2))
		Expect(string(p[".opencode/agents/empty.md"].Data)).To(Equal("---\nmode: subagent\ndescription: d\n---\nE.\n"), "an empty list restricts nothing")
		Expect(string(p[".opencode/agents/none.md"].Data)).To(Equal("---\nmode: subagent\ndescription: d\n---\nN.\n"))
		Expect(codes(fs)).To(Equal([]string{"skip agent-tools-unenforceable list", "skip agent-tools-unenforceable string"}))
		Expect(fs[0]).To(Equal(target.Finding{Severity: target.SeveritySkip, Target: "opencode", Kind: "agent", Artifact: "list",
			Code: "agent-tools-unenforceable", Text: "opencode cannot enforce this agent's tool list; without it the agent can use every tool",
			Hint: "under opencode: in the agent's .targets.yaml, set tools: null and restrict the agent with permission:, as OpenCode does", Forceable: true}))
		ctx.Force = true
		ins, fs = plan1(m)
		Expect(string(byPath(ins)[".opencode/agents/list.md"].Data)).To(Equal("---\nmode: subagent\ndescription: d\n---\nL.\n"))
		Expect(fs).To(HaveEach(SatisfyAll(HaveField("Forced", true), HaveField("Severity", target.SeverityWarn))))
	})

	It("drops agent values OpenCode would refuse to load, keeping valid ones and keys it does not check", func() {
		ins, fs := plan1(&module.Module{Name: "example", Agents: []module.Agent{
			{Name: "bad", Doc: doc("---\ndescription: d\ncolor: red\nmode: weird\ntemperature: hot\ntop_p: x\nsteps: 0\nmaxSteps: 1.5\n" +
				"hidden: \"yes\"\ndisable: \"true\"\nvariant: [a]\noptions: x\n---\nB.\n")},
			{Name: "good", Doc: doc("---\ndescription: d\ncolor: \"#ff0000\"\nmode: all\ntemperature: 0.2\ntop_p: 0.9\nsteps: 5\nmaxSteps: 3\n" +
				"hidden: true\ndisable: false\nvariant: high\noptions: {reasoning: low}\npermission: {bash: ask}\nfoo: bar\n---\nG.\n")},
			{Name: "theme", Doc: doc("---\ndescription: d\ncolor: accent\n---\nT.\n")},
		}})
		p := byPath(ins)
		Expect(string(p[".opencode/agents/bad.md"].Data)).To(Equal("---\nmode: subagent\ndescription: d\n---\nB.\n"))
		Expect(string(p[".opencode/agents/good.md"].Data)).To(Equal("---\ndescription: d\ncolor: \"#ff0000\"\nmode: all\ntemperature: 0.2\ntop_p: 0.9\nsteps: 5\nmaxSteps: 3\n" +
			"hidden: true\ndisable: false\nvariant: high\noptions: {reasoning: low}\npermission: {bash: ask}\nfoo: bar\n---\nG.\n"))
		Expect(string(p[".opencode/agents/theme.md"].Data)).To(Equal("---\nmode: subagent\ndescription: d\n---\nT.\n"),
			"OpenCode V2 rejects theme colors, which 1.18 accepts")
		Expect(codes(fs)).To(Equal([]string{"warn frontmatter-value-dropped bad", "warn frontmatter-value-dropped bad", "warn frontmatter-value-dropped bad",
			"warn frontmatter-value-dropped bad", "warn frontmatter-value-dropped bad", "warn frontmatter-value-dropped bad", "warn frontmatter-value-dropped bad",
			"warn frontmatter-value-dropped bad", "warn frontmatter-value-dropped bad", "warn frontmatter-value-dropped bad",
			"warn frontmatter-value-dropped theme"}), "one warn per invalid key, none for good")
		Expect(fs[4].Text).To(Equal("mode dropped: opencode needs a string matching ^(subagent|primary|all)$"))
		Expect(fs[6].Text).To(Equal("steps dropped: opencode needs a positive-integer"))
	})

	It("skips an agent without a description", func() {
		ins, fs := plan1(&module.Module{Name: "example", Agents: []module.Agent{{Name: "rev", Doc: doc("x\n")}}})
		Expect(ins).To(BeEmpty())
		Expect(codes(fs)).To(Equal([]string{"skip required-key-missing rev"}))
	})

	It("renders MCP servers in opencode.json shape and turns environment references into {env:VAR}", func() {
		ins, fs := plan1(mcp(
			module.MCPServer{Name: "gh", Transport: "stdio", Command: "${PLUGIN_ROOT}/bin/gh", Args: []string{"--token", "${GH_TOKEN}"},
				Env: map[string]string{"DATA": "${PLUGIN_DATA}", "LEVEL": "${LOG_LEVEL:-info}"}, Cwd: "/tmp"},
			module.MCPServer{Name: "web", Transport: "http", URL: "https://x/mcp", Headers: map[string]string{"Authorization": "Bearer ${API_KEY}"}},
			module.MCPServer{Name: "ev", Transport: "sse", URL: "https://x/sse"},
		))
		p := byPath(ins)
		Expect(p["opencode.json/mcp/gh"].Value).To(MatchJSON(`{"type":"local","command":["/proj/.lola/modules/example/bin/gh","--token","{env:GH_TOKEN}"],
			"environment":{"DATA":"/proj/.lola/plugin-data/example","LEVEL":"{env:LOG_LEVEL}"}}`))
		Expect(p["opencode.json/mcp/web"].Value).To(MatchJSON(`{"type":"remote","url":"https://x/mcp","headers":{"Authorization":"Bearer {env:API_KEY}"}}`))
		Expect(p["opencode.json/mcp/ev"].Value).To(MatchJSON(`{"type":"remote","url":"https://x/sse"}`))
		Expect(fs).To(Equal([]target.Finding{
			{Severity: target.SeverityInfo, Target: "opencode", Kind: "mcp", Artifact: "gh", Code: "opencode/env-reference", Text: "${GH_TOKEN} becomes {env:GH_TOKEN}"},
			{Severity: target.SeverityWarn, Target: "opencode", Kind: "mcp", Artifact: "gh", Code: "opencode/env-default-dropped",
				Text: `opencode has no defaults for environment references; ${LOG_LEVEL:-info} becomes {env:LOG_LEVEL}, dropping "info"`,
				Hint: "set LOG_LEVEL in the environment opencode runs in"},
			{Severity: target.SeverityWarn, Target: "opencode", Kind: "mcp", Artifact: "gh", Code: "mcp-cwd-dropped", Text: `opencode has no working-directory setting; cwd "/tmp" dropped`},
			{Severity: target.SeverityInfo, Target: "opencode", Kind: "mcp", Artifact: "web", Code: "opencode/env-reference", Text: "${API_KEY} becomes {env:API_KEY}"},
		}))
	})

	It("writes MCP servers to opencode.jsonc when the project has one, even beside an opencode.json", func() {
		m := mcp(module.MCPServer{Name: "ev", Transport: "sse", URL: "https://x/sse"})
		Expect(os.WriteFile(filepath.Join(ctx.Root, "opencode.json"), []byte("{}"), 0o644)).To(Succeed())
		ins, _ := plan1(m)
		Expect(ins[0].Path).To(Equal("opencode.json"))
		Expect(os.WriteFile(filepath.Join(ctx.Root, "opencode.jsonc"), []byte("{}"), 0o644)).To(Succeed())
		ins, _ = plan1(m)
		Expect(ins[0].Path).To(Equal("opencode.jsonc"))
	})

	It("delivers rules verbatim to .opencode/lola/<module>/ with one instructions member per module", func() {
		m := &module.Module{Name: "example", Rules: []module.Rule{{Name: "a", Body: "Keep ${PLUGIN_ROOT}.\n"}, {Name: "b", Body: "Be brief.\n"}}}
		ins, fs := plan1(m)
		Expect(fs).To(BeEmpty())
		Expect(ins).To(Equal([]plan.Intent{
			{Target: "opencode", Kind: "rule", Op: plan.OpWriteFile, Path: ".opencode/lola/example/a.md", Data: []byte("Keep ${PLUGIN_ROOT}.\n"), Mode: 0o644},
			{Target: "opencode", Kind: "rule", Op: plan.OpWriteFile, Path: ".opencode/lola/example/b.md", Data: []byte("Be brief.\n"), Mode: 0o644},
			{Target: "opencode", Kind: "rule", Op: plan.OpAddJSONMember, Path: "opencode.json", Pointer: "/instructions", Member: plan.StringMember(".opencode/lola/example/*.md")},
		}))

		Expect(os.WriteFile(filepath.Join(ctx.Root, "opencode.jsonc"), []byte("{}"), 0o644)).To(Succeed())
		ins, _ = plan1(m)
		Expect(ins[2].Path).To(Equal("opencode.jsonc"))

		ctx.Scope = target.User
		ins, _ = plan1(m)
		Expect(ins[0].Path).To(Equal("lola/example/a.md"))
		Expect(ins[2].Member).To(Equal(plan.StringMember(filepath.Join(ctx.Root, "lola/example/*.md"))), "OpenCode globs only the last component of an absolute entry")

		ctx.SkipRules = true
		ins, _ = plan1(m)
		Expect(ins).To(BeEmpty())
	})

	It("roots user scope at $XDG_CONFIG_HOME/opencode with artifact directories directly under it", func() {
		Expect(o.Root(target.User, "/proj", paths.Paths{Home: "/home/u", ConfigHome: "/home/u/.config"})).To(Equal("/home/u/.config/opencode"))
		Expect(o.Root(target.Project, "/proj", paths.Paths{})).To(Equal("/proj"))
		Expect(o.BaseDirs(target.User)).To(Equal([]string{"skills", "commands", "agents", "lola"}))
		Expect(o.BaseDirs(target.Project)).To(Equal([]string{".opencode/skills", ".opencode/commands", ".opencode/agents", ".opencode"}))
		ctx.Scope = target.User
		ins, _ := plan1(&module.Module{Name: "example", Commands: []module.Command{{Name: "go", Doc: doc("Go\n")}}})
		Expect(ins[0].Path).To(Equal("commands/go.md"))
	})

	It("detects .opencode/, opencode.json or opencode.jsonc in a project", func() {
		for _, name := range []string{"opencode.json", "opencode.jsonc"} {
			dir := testenv.Isolate()
			Expect(o.Detect(target.Project, dir)).To(BeFalse())
			Expect(os.WriteFile(filepath.Join(dir, name), nil, 0o644)).To(Succeed())
			Expect(o.Detect(target.Project, dir)).To(BeTrue(), name)
		}
		dir := testenv.Isolate()
		Expect(os.Mkdir(filepath.Join(dir, ".opencode"), 0o755)).To(Succeed())
		Expect(o.Detect(target.Project, dir)).To(BeTrue())
	})

	It("detects OpenCode at user scope by its config directory alone, and reads its version", func() {
		root := filepath.Join(testenv.Isolate(), "opencode")
		Expect(o.Detect(target.User, root)).To(BeFalse())
		Expect(os.Mkdir(root, 0o700)).To(Succeed())
		Expect(o.Detect(target.User, root)).To(BeTrue(), "no opencode.json needed")
		p := o.Probe()
		Expect(p.Binaries).To(Equal([]string{"opencode"}))
		Expect(p.VersionRE.FindStringSubmatch("1.0.12\n")).To(Equal([]string{"1.0.12", "1.0.12"}))
	})
})
