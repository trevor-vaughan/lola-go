package claude_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/target/claude"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("claude-code runtime hooks", func() {
	c := claude.Target{}
	const root, data = "/proj/.lola/modules/example", "/proj/.lola/plugin-data/example"
	const export = "export CLAUDE_PLUGIN_ROOT='" + root + "' CLAUDE_PLUGIN_DATA='" + data + "'; "
	var ctx target.PlanContext
	BeforeEach(func() {
		testenv.Isolate()
		ctx = target.PlanContext{Scope: target.Project, ContentDir: "/cache/example/content", PluginRoot: root, PluginData: data}
	})

	hooks := func(events string) *module.Module {
		return &module.Module{Name: "example", RuntimeHooks: []module.RuntimeHookSet{{Source: "hooks/hooks.json", Hooks: []byte(events)}}}
	}
	// handler is the only handler of the only group the module renders.
	handler := func(ins []plan.Intent) map[string]any {
		ExpectWithOffset(1, ins).To(HaveLen(1))
		var g struct{ Hooks []map[string]any }
		ExpectWithOffset(1, json.Unmarshal(ins[0].Member, &g)).To(Succeed())
		ExpectWithOffset(1, g.Hooks).To(HaveLen(1))
		return g.Hooks[0]
	}
	command := func(cmd string) *module.Module {
		b, err := json.Marshal(cmd)
		Expect(err).NotTo(HaveOccurred())
		return hooks(`{"Stop":[{"hooks":[{"type":"command","command":` + string(b) + `}]}]}`)
	}
	execForm := func(cmd string, args ...string) *module.Module {
		b, err := json.Marshal(map[string]any{"type": "command", "command": cmd, "args": append([]string{}, args...)})
		Expect(err).NotTo(HaveOccurred())
		return hooks(`{"Stop":[{"hooks":[` + string(b) + `]}]}`)
	}

	It("renders each matcher group as a member of /hooks/<Event> in settings.local.json, or ~/.claude/settings.json at user scope", func() {
		m := hooks(`{"PreToolUse":[{"hooks":[{"command":"a","type":"command"}],"matcher":"Bash"},{"hooks":[{"type":"prompt","prompt":"p"}]}],` +
			`"Stop":[{"hooks":[{"type":"agent","prompt":"q"}]}]}`)
		ins, fs, err := c.Plan(m, ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(fs).To(BeEmpty())
		Expect(ins).To(HaveLen(3))
		for _, in := range ins {
			Expect(in.Target).To(Equal("claude-code"))
			Expect(in.Kind).To(Equal("runtime-hook"))
			Expect(in.Op).To(Equal(plan.OpAddJSONMember))
			Expect(in.Path).To(Equal(".claude/settings.local.json"))
			Expect(in.Consent).To(Equal(&plan.HookConsent{Settings: ".claude/settings.local.json"}), "every hook group passes the consent gate")
		}
		Expect([]string{ins[0].Pointer, ins[1].Pointer, ins[2].Pointer}).To(Equal([]string{"/hooks/PreToolUse", "/hooks/PreToolUse", "/hooks/Stop"}))
		Expect(string(ins[0].Member)).To(MatchJSON(`{"matcher":"Bash","hooks":[{"type":"command","command":"` + export + `a"}]}`))
		Expect(string(ins[1].Member)).To(MatchJSON(`{"hooks":[{"type":"prompt","prompt":"p"}]}`))

		ctx.Scope = target.User
		ins, _, err = c.Plan(m, ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(ins[0].Path).To(Equal(".claude/settings.json"))
	})

	It("keeps every handler type and every other field as authored", func() {
		group := `{"matcher":"Write|Edit","hooks":[` +
			`{"type":"http","url":"http://localhost:8080/h","headers":{"Authorization":"Bearer $MY_TOKEN"},"allowedEnvVars":["MY_TOKEN"],"timeout":30},` +
			`{"type":"mcp_tool","server":"my_server","tool":"security_scan","input":{"file_path":"${tool_input.file_path}"}},` +
			`{"type":"prompt","prompt":"Is $ARGUMENTS safe? ${CLAUDE_PLUGIN_ROOT}","model":"m"},{"type":"agent","prompt":"check"},` +
			`{"type":"future","command":"${CLAUDE_PLUGIN_ROOT}/x"}],"extra":{"k":[1,2]}}`
		ins, fs, err := c.Plan(hooks(`{"PostToolUse":[`+group+`]}`), ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(fs).To(BeEmpty())
		Expect(string(ins[0].Member)).To(MatchJSON(group))
	})

	It("adds the export prefix to a shell-form command and keeps the command verbatim after it", func() {
		for _, cmd := range []string{"./x; rm -rf /tmp/y", "cd sub && ./x || true", "echo a\necho b", `echo "$(date)" '$HOME'`} {
			ins, fs, err := c.Plan(command(cmd), ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(fs).To(BeEmpty())
			Expect(handler(ins)["command"]).To(Equal(export + cmd))
		}
	})

	DescribeTable("replaces the plugin variables' textual forms in command and args",
		func(in, want string) {
			ins, _, err := c.Plan(command(in), ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(handler(ins)["command"]).To(Equal(export + want))
			ins, _, err = c.Plan(execForm("node", in), ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(handler(ins)["args"]).To(Equal([]any{"--", "CLAUDE_PLUGIN_ROOT=" + root, "CLAUDE_PLUGIN_DATA=" + data, "node", want}))
		},
		Entry("braced", "${CLAUDE_PLUGIN_ROOT}/a ${CLAUDE_PLUGIN_DATA}/b", root+"/a "+data+"/b"),
		Entry("lola's names", "${PLUGIN_ROOT}/a ${PLUGIN_DATA}/b", root+"/a "+data+"/b"),
		Entry("bare", "$CLAUDE_PLUGIN_ROOT/a \"$CLAUDE_PLUGIN_DATA\"", root+"/a \""+data+"\""),
		Entry("bare at the end", "cd $CLAUDE_PLUGIN_ROOT", "cd "+root),
		Entry("bare followed by an identifier character", "$CLAUDE_PLUGIN_ROOTS $CLAUDE_PLUGIN_DATA_DIR $CLAUDE_PLUGIN_ROOT9",
			"$CLAUDE_PLUGIN_ROOTS $CLAUDE_PLUGIN_DATA_DIR $CLAUDE_PLUGIN_ROOT9"),
		Entry("bare forms of lola's names, which are not plugin variables", "$PLUGIN_ROOT $PLUGIN_DATA", "$PLUGIN_ROOT $PLUGIN_DATA"),
		Entry("an unknown braced name", "${CLAUDE_PLUGIN_ROOTS} ${CLAUDE_PROJECT_DIR}", "${CLAUDE_PLUGIN_ROOTS} ${CLAUDE_PROJECT_DIR}"),
	)

	It("wraps an exec-form command in env after --, and passes paths with spaces, $ and quotes verbatim", func() {
		ctx.PluginRoot, ctx.PluginData = "/p q/$(x)/it's", `/d "e"`
		ins, fs, err := c.Plan(execForm("${CLAUDE_PLUGIN_ROOT}/bin/run", "--flag", "${CLAUDE_PLUGIN_DATA}/f"), ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(fs).To(BeEmpty())
		h := handler(ins)
		Expect(h["command"]).To(Equal("env"))
		Expect(h["args"]).To(Equal([]any{"--", "CLAUDE_PLUGIN_ROOT=/p q/$(x)/it's", `CLAUDE_PLUGIN_DATA=/d "e"`, "/p q/$(x)/it's/bin/run", "--flag", `/d "e"/f`}))

		ins, _, err = c.Plan(execForm("tool"), ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(handler(ins)["args"]).To(Equal([]any{"--", "CLAUDE_PLUGIN_ROOT=/p q/$(x)/it's", `CLAUDE_PLUGIN_DATA=/d "e"`, "tool"}), `"args": [] is exec form`)
	})

	DescribeTable("skips an exec-form command env would misread, forceably",
		func(cmd string) {
			ins, fs, err := c.Plan(execForm(cmd, "a"), ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(ins).To(BeEmpty())
			Expect(fs).To(ConsistOf(MatchFields(IgnoreExtras, Fields{"Severity": Equal(target.SeveritySkip), "Kind": Equal("runtime-hook"),
				"Artifact": Equal("hooks/hooks.json"), "Code": Equal("claude-code/runtime-hook-path-unsafe"), "Forceable": BeTrue(),
				"Text": ContainSubstring("/Stop/0/hooks/0"), "Hint": ContainSubstring("does not start with -")})))

			ctx.Force = true
			ins, fs, err = c.Plan(execForm(cmd, "a"), ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(fs).To(ConsistOf(HaveField("Forced", true)))
			Expect(handler(ins)).To(Equal(map[string]any{"type": "command", "command": cmd, "args": []any{"a"}}), "forced: kept unwrapped")
		},
		Entry("an option", "-x"),
		Entry("an assignment", "A=b"),
		Entry("an assignment inside a path", "./bin/a=b"),
	)

	It("skips an exec-form command that the plugin root makes misreadable", func() {
		ctx.PluginRoot = "/p=q"
		_, fs, err := c.Plan(execForm("${CLAUDE_PLUGIN_ROOT}/run"), ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(fs).To(ConsistOf(HaveField("Code", "claude-code/runtime-hook-path-unsafe")))
	})

	DescribeTable("skips shell-form hooks when a path is not shell-safe, forceably unless it holds a quoting character",
		func(rootPath, dataPath string, forceable bool) {
			ctx.PluginRoot, ctx.PluginData = rootPath, dataPath
			ins, fs, err := c.Plan(command("run ${CLAUDE_PLUGIN_ROOT}/x"), ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(ins).To(BeEmpty())
			Expect(fs).To(ConsistOf(MatchFields(IgnoreExtras, Fields{"Severity": Equal(target.SeveritySkip),
				"Code": Equal("claude-code/runtime-hook-path-unsafe"), "Forceable": Equal(forceable), "Hint": ContainSubstring("args")})))
			Expect(fs[0].Text).NotTo(ContainSubstring("\x1b"))

			ctx.Force = true
			ins, fs, err = c.Plan(command("run ${CLAUDE_PLUGIN_ROOT}/x"), ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(fs).To(ConsistOf(HaveField("Forced", forceable)))
			Expect(ins).To(HaveLen(map[bool]int{true: 1, false: 0}[forceable]), "a path holding a quoting character is never forced into shell text")
		},
		Entry("a space", "/my proj/m", data, true),
		Entry("a control character", "/a\x1b[2J", data, true),
		Entry("a semicolon, ampersand and pipe", "/a;b&c|d", data, true),
		Entry("a dollar", root, "/d/$HOME", false),
		Entry("a single quote", "/it's/m", data, false),
		Entry("a double quote", root, `/d"/x`, false),
		Entry("a backquote", "/a`b`/m", data, false),
		Entry("a backslash", root, `/d\x`, false),
	)

	It("on a path with a backslash, such as any Windows path, names exec form as the only way, not simpler paths", func() {
		ctx.PluginRoot, ctx.PluginData = `C:\Users\me\proj\.lola\modules\example`, `C:\Users\me\proj\.lola\plugin-data\example`
		_, fs, err := c.Plan(command("run ${CLAUDE_PLUGIN_ROOT}/x"), ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(fs).To(ConsistOf(MatchFields(IgnoreExtras, Fields{"Code": Equal("claude-code/runtime-hook-path-unsafe"), "Forceable": BeFalse(),
			"Hint": And(ContainSubstring("args"), ContainSubstring("exec form"), Not(ContainSubstring("letters, digits")))})))

		ctx.PluginRoot, ctx.PluginData = "/my proj/m", data
		_, fs, err = c.Plan(command("run ${CLAUDE_PLUGIN_ROOT}/x"), ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(fs).To(ConsistOf(HaveField("Hint", ContainSubstring("letters, digits"))), "a path a user can change keeps that advice")
	})

	It("forced past an unsafe path, exports quoted values and refers to them, so the path never runs", func() {
		dir := GinkgoT().TempDir()
		hostile := filepath.Join(dir, "a b;touch pwned&&touch pwned2|cat")
		Expect(os.MkdirAll(hostile, 0o755)).To(Succeed())
		ctx.PluginRoot, ctx.PluginData, ctx.Force = hostile, filepath.Join(dir, "data"), true
		ins, fs, err := c.Plan(command(`printf '%s|%s' "${CLAUDE_PLUGIN_ROOT}" "$CLAUDE_PLUGIN_DATA"`), ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(fs).To(ConsistOf(HaveField("Forced", true)))
		cmd := exec.Command("sh", "-c", handler(ins)["command"].(string))
		cmd.Dir = dir
		out, err := cmd.Output()
		Expect(err).NotTo(HaveOccurred())
		Expect(string(out)).To(Equal(hostile + "|" + filepath.Join(dir, "data")))
		Expect(filepath.Join(dir, "pwned")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(dir, "pwned2")).NotTo(BeAnExistingFile())
	})

	It("the export reaches every part of a compound command and a script reading its environment", func() {
		dir := GinkgoT().TempDir()
		ctx.PluginRoot, ctx.PluginData = filepath.Join(dir, "copy"), filepath.Join(dir, "data")
		Expect(os.MkdirAll(ctx.PluginRoot, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(ctx.PluginRoot, "show.sh"), []byte("#!/bin/sh\nprintf '%s,%s;' \"$CLAUDE_PLUGIN_ROOT\" \"$CLAUDE_PLUGIN_DATA\"\n"), 0o755)).To(Succeed())
		ins, _, err := c.Plan(command(`cd "${CLAUDE_PLUGIN_ROOT}" && ./show.sh && printf '%s' "$CLAUDE_PLUGIN_DATA"`), ctx)
		Expect(err).NotTo(HaveOccurred())
		out, err := exec.Command("sh", "-c", handler(ins)["command"].(string)).Output()
		Expect(err).NotTo(HaveOccurred())
		Expect(string(out)).To(Equal(ctx.PluginRoot + "," + ctx.PluginData + ";" + ctx.PluginData))

		ins, _, err = c.Plan(execForm("${CLAUDE_PLUGIN_ROOT}/show.sh"), ctx)
		Expect(err).NotTo(HaveOccurred())
		h := handler(ins)
		var args []string
		for _, a := range h["args"].([]any) {
			args = append(args, a.(string))
		}
		out, err = exec.Command(h["command"].(string), args...).Output()
		Expect(err).NotTo(HaveOccurred())
		Expect(string(out)).To(Equal(ctx.PluginRoot + "," + ctx.PluginData + ";"))
	})

	It("runs an exec-form hook from a hostile path without reading the path as code", func() {
		dir := GinkgoT().TempDir()
		ctx.PluginRoot, ctx.PluginData = filepath.Join(dir, "a b'$(touch pwned)"), filepath.Join(dir, "d=\"x\"")
		Expect(os.MkdirAll(ctx.PluginRoot, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(ctx.PluginRoot, "show.sh"), []byte("#!/bin/sh\nprintf '%s,%s,%s' \"$CLAUDE_PLUGIN_ROOT\" \"$CLAUDE_PLUGIN_DATA\" \"$1\"\n"), 0o755)).To(Succeed())
		ins, fs, err := c.Plan(execForm("${CLAUDE_PLUGIN_ROOT}/show.sh", "$CLAUDE_PLUGIN_DATA;x"), ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(fs).To(BeEmpty())
		h := handler(ins)
		var args []string
		for _, a := range h["args"].([]any) {
			args = append(args, a.(string))
		}
		cmd := exec.Command(h["command"].(string), args...)
		cmd.Dir = dir
		out, err := cmd.Output()
		Expect(err).NotTo(HaveOccurred())
		Expect(string(out)).To(Equal(ctx.PluginRoot + "," + ctx.PluginData + "," + ctx.PluginData + ";x"))
		Expect(filepath.Join(dir, "pwned")).NotTo(BeAnExistingFile())
	})

	It("skips a shell-form handler run by PowerShell, forceably, and forced renders it without the export", func() {
		m := hooks(`{"Stop":[{"hooks":[{"type":"command","command":"& ${CLAUDE_PLUGIN_ROOT}/x.ps1","shell":"powershell"}]}]}`)
		ins, fs, err := c.Plan(m, ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(ins).To(BeEmpty())
		Expect(fs).To(ConsistOf(MatchFields(IgnoreExtras, Fields{"Severity": Equal(target.SeveritySkip),
			"Code": Equal("claude-code/runtime-hook-shell-unsupported"), "Forceable": BeTrue(), "Text": ContainSubstring("/Stop/0/hooks/0")})))
		ctx.Force = true
		ins, _, err = c.Plan(m, ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(handler(ins)["command"]).To(Equal("& " + root + "/x.ps1"))

		ins, fs, err = c.Plan(hooks(`{"Stop":[{"hooks":[{"type":"command","command":"pwsh","args":[],"shell":"powershell"}]}]}`), ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(fs).To(BeEmpty(), "exec form ignores shell")
		Expect(handler(ins)["command"]).To(Equal("env"))
	})

	It("skips the hooks at project scope when git could commit settings.local.json, forceably", func() {
		ctx.LocalSettingsExposed = ".claude/settings.local.json is not ignored by git"
		ins, fs, err := c.Plan(command("x"), ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(ins).To(BeEmpty())
		Expect(fs).To(ConsistOf(MatchFields(IgnoreExtras, Fields{"Severity": Equal(target.SeveritySkip), "Kind": Equal("runtime-hook"),
			"Code": Equal("claude-code/settings-local-not-ignored"), "Forceable": BeTrue(), "Text": Equal(ctx.LocalSettingsExposed),
			"Hint": Equal("add .claude/settings.local.json to .gitignore; Claude Code ignores it only when Claude Code creates it")})))
		ctx.Force = true
		ins, _, err = c.Plan(command("x"), ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(ins).To(HaveLen(1))

		ctx.Force, ctx.Scope = false, target.User
		ins, fs, err = c.Plan(command("x"), ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(fs).To(BeEmpty(), "~/.claude/settings.json is outside any repository")
		Expect(ins).To(HaveLen(1))
	})

	It("skips a set naming an event that is not an identifier, even with Force", func() {
		for _, event := range []string{"<<", "a/b", "Pre\u001b[2JToolUse", "9Stop"} {
			b, err := json.Marshal(map[string]any{event: []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "x"}}}}})
			Expect(err).NotTo(HaveOccurred())
			ctx.Force = true
			ins, fs, err := c.Plan(hooks(string(b)), ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(ins).To(BeEmpty(), event)
			Expect(fs).To(ConsistOf(MatchFields(IgnoreExtras, Fields{"Severity": Equal(target.SeveritySkip),
				"Code": Equal("claude-code/runtime-hook-event-invalid"), "Forceable": BeFalse()})), event)
			Expect(fs[0].Text).NotTo(ContainSubstring("\x1b"))
		}
	})

	It("renders only the sets meant for claude-code", func() {
		m := hooks(`{"Stop":[{"hooks":[{"type":"command","command":"x"}]}]}`)
		m.RuntimeHooks[0].Targets = []string{"opencode"}
		ins, fs, err := c.Plan(m, ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(ins).To(BeEmpty())
		Expect(fs).To(BeEmpty())
	})
})

var _ = Describe("claude-code frontmatter hooks", func() {
	c := claude.Target{}
	const root, data = "/proj/.lola/modules/example", "/proj/.lola/plugin-data/example"
	const export = "export CLAUDE_PLUGIN_ROOT='" + root + "' CLAUDE_PLUGIN_DATA='" + data + "'; "
	var ctx target.PlanContext
	BeforeEach(func() {
		testenv.Isolate()
		ctx = target.PlanContext{Scope: target.Project, ContentDir: "/cache/example/content", PluginRoot: root, PluginData: data}
	})
	parse := func(src string) module.Doc {
		d, err := module.ParseDoc([]byte(src))
		Expect(err).NotTo(HaveOccurred())
		return d
	}
	skill := func(fm string) *module.Module {
		return &module.Module{Name: "example", Skills: []module.Skill{{Name: "review", Dir: "skills/review", Doc: parse("---\ndescription: d\n" + fm + "---\nRun ${CLAUDE_PLUGIN_ROOT}/x\n")}}}
	}
	written := func(ins []plan.Intent, path string) module.Doc {
		for _, in := range ins {
			if in.Path == path {
				return parse(string(in.Data))
			}
		}
		Fail("no intent writes " + path)
		return module.Doc{}
	}

	It("gates every file of a skill whose frontmatter has hooks, and rewrites the hooks like settings hooks", func() {
		ins, fs, err := c.Plan(skill("hooks:\n  Stop:\n    - hooks: [{type: command, command: '${CLAUDE_PLUGIN_ROOT}/stop.sh'}]\n"), ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(fs).To(BeEmpty())
		Expect(ins).To(HaveLen(2))
		for _, in := range ins {
			Expect(in.Consent).NotTo(BeNil(), in.Path)
			Expect(in.Consent.Settings).To(Equal(".claude/settings.local.json"))
			Expect(in.Consent.Kind).To(Equal("skill"))
			Expect(in.Consent.Name).To(Equal("review"))
			Expect(string(in.Consent.Hooks)).To(MatchJSON(`{"Stop":[{"hooks":[{"type":"command","command":"` + export + root + `/stop.sh"}]}]}`))
		}
		d := written(ins, ".claude/skills/review/SKILL.md")
		Expect(d.Body).To(Equal("Run "+root+"/x\n"), "component content is still expanded")
		b, err := json.Marshal(d.Frontmatter["hooks"])
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(MatchJSON(string(ins[0].Consent.Hooks)))

		ctx.Scope = target.User
		ins, _, err = c.Plan(skill("hooks: {Stop: [{hooks: [{type: prompt, prompt: p}]}]}\n"), ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(ins[0].Consent.Settings).To(Equal(".claude/settings.json"))
	})

	It("leaves a component without hooks ungated", func() {
		ins, _, err := c.Plan(skill(""), ctx)
		Expect(err).NotTo(HaveOccurred())
		for _, in := range ins {
			Expect(in.Consent).To(BeNil())
		}
	})

	It("treats a case variant of the key as hooks", func() {
		ins, _, err := c.Plan(skill("Hooks: {Stop: [{hooks: [{type: prompt, prompt: p}]}]}\n"), ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(ins[0].Consent).NotTo(BeNil())
		Expect(string(ins[0].Consent.Hooks)).To(MatchJSON(`{"Stop":[{"hooks":[{"type":"prompt","prompt":"p"}]}]}`))
	})

	It("gates a command with its sidecar files, and an agent", func() {
		m := &module.Module{Name: "example",
			Commands: []module.Command{{Name: "tools/deploy", File: "commands/tools/deploy.md", SidecarDir: "commands/tools/deploy",
				Doc: parse("---\nhooks: {Stop: [{hooks: [{type: agent, prompt: check}]}]}\n---\nDeploy\n")}},
			Agents: []module.Agent{{Name: "critic", Doc: parse("---\ndescription: c\nhooks: {Stop: [{hooks: [{type: prompt, prompt: p}]}]}\n---\nC\n")}}}
		ins, _, err := c.Plan(m, ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(ins).To(HaveLen(3))
		Expect(ins[0].Consent).To(HaveField("Kind", "command"))
		Expect(ins[1].Consent).To(HaveField("Kind", "command"))
		Expect(ins[1].Op).To(Equal(plan.OpCopyTree))
		Expect(ins[2].Consent).To(HaveField("Name", "critic"))
	})

	It("skips a component whose hooks it cannot render safely, and forced never puts the path in shell text", func() {
		ctx.PluginRoot = "/p q;touch x/m"
		m := skill("hooks: {Stop: [{hooks: [{type: command, command: 'run ${CLAUDE_PLUGIN_ROOT}/a'}]}]}\n")
		ins, fs, err := c.Plan(m, ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(ins).To(BeEmpty())
		Expect(fs).To(ConsistOf(MatchFields(IgnoreExtras, Fields{"Kind": Equal("skill"), "Artifact": Equal("review"),
			"Code": Equal("claude-code/runtime-hook-path-unsafe"), "Forceable": BeTrue()})))
		ctx.Force = true
		ins, _, err = c.Plan(m, ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(ins[0].Consent.Hooks)).To(ContainSubstring(`'; run ${CLAUDE_PLUGIN_ROOT}/a`))
		hooks := written(ins, ".claude/skills/review/SKILL.md").Frontmatter["hooks"].(map[string]any)
		handler := hooks["Stop"].([]any)[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
		Expect(handler["command"]).To(HaveSuffix("; run ${CLAUDE_PLUGIN_ROOT}/a"), "target.Expand must not reach the rewritten hooks")
	})

	It("never reads the body as frontmatter once the hooks are removed from it", func() {
		src := "---\nHooks: {Stop: [{hooks: [{type: prompt, prompt: shown}]}]}\n---\n---\nhooks: {Stop: [{hooks: [{type: command, command: evil}]}]}\nallowed-tools: Bash\n---\nBody\n"
		m := &module.Module{Name: "example", Commands: []module.Command{{Name: "c", Doc: parse(src)}}}
		ins, _, err := c.Plan(m, ctx)
		Expect(err).NotTo(HaveOccurred())
		out := string(ins[0].Data)
		Expect(out).NotTo(ContainSubstring("evil, allowed-tools"))
		d := written(ins, ".claude/commands/c.md")
		Expect(d.Frontmatter).To(HaveLen(1))
		b, err := json.Marshal(d.Frontmatter["Hooks"])
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(MatchJSON(string(ins[0].Consent.Hooks)))
		Expect(d.Body).To(Equal("---\nhooks: {Stop: [{hooks: [{type: command, command: evil}]}]}\nallowed-tools: Bash\n---\nBody\n"))
	})

	It("keeps a body that begins with a horizontal rule in the body", func() {
		src := "---\nhooks: {Stop: [{hooks: [{type: prompt, prompt: p}]}]}\n---\n---\nafter the rule ${CLAUDE_PLUGIN_ROOT}\n"
		m := &module.Module{Name: "example", Commands: []module.Command{{Name: "c", Doc: parse(src)}}}
		ins, _, err := c.Plan(m, ctx)
		Expect(err).NotTo(HaveOccurred())
		d := written(ins, ".claude/commands/c.md")
		Expect(d.Frontmatter).To(HaveLen(1))
		Expect(d.Body).To(Equal("---\nafter the rule " + root + "\n"))
	})

	It("skips a component naming an event that is not an identifier, even with Force", func() {
		ctx.Force = true
		ins, fs, err := c.Plan(skill("hooks: {'a b': [{hooks: [{type: prompt, prompt: p}]}]}\n"), ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(ins).To(BeEmpty())
		Expect(fs).To(ConsistOf(HaveField("Code", "claude-code/runtime-hook-event-invalid")))
	})
})

var _ = Describe("claude-code pre-approved tools", func() {
	c := claude.Target{}
	ctx := target.PlanContext{Scope: target.Project, ContentDir: "/cache/example/content", PluginRoot: "/p", PluginData: "/d"}
	BeforeEach(func() { testenv.Isolate() })

	It("installs allowed-tools as written, with a warning naming the component and the tools", func() {
		parse := func(src string) module.Doc {
			d, err := module.ParseDoc([]byte(src))
			Expect(err).NotTo(HaveOccurred())
			return d
		}
		m := &module.Module{Name: "example",
			Skills:   []module.Skill{{Name: "review", Dir: "skills/review", Doc: parse("---\ndescription: d\nallowed-tools: [Bash(git *), \"Read\\e[2J\"]\n---\nR\n")}},
			Commands: []module.Command{{Name: "ship", Doc: parse("---\nallowed-tools: Bash(npm *), Edit\n---\nS\n")}},
			Agents:   []module.Agent{{Name: "critic", Doc: parse("---\ndescription: c\n---\nC\n")}}}
		ins, fs, err := c.Plan(m, ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(fs).To(ConsistOf(
			MatchFields(IgnoreExtras, Fields{"Severity": Equal(target.SeverityWarn), "Kind": Equal("skill"), "Artifact": Equal("review"),
				"Code": Equal("claude-code/allowed-tools"), "Text": Equal("pre-approves tools Claude Code then runs without asking: Bash(git *), Read?[2J")}),
			MatchFields(IgnoreExtras, Fields{"Kind": Equal("command"), "Artifact": Equal("ship"),
				"Text": Equal("pre-approves tools Claude Code then runs without asking: Bash(npm *), Edit")})))
		Expect(string(ins[1].Data)).To(ContainSubstring("allowed-tools: [Bash(git *), \"Read\\e[2J\"]"), "installed as written")
	})

	It("warns about allowed-tools in a command's sidecar .md, which Claude Code runs as a command, naming the sidecar", func() {
		d, err := module.ParseDoc([]byte("---\nallowed-tools: [Bash(*)]\n---\nN\n"))
		Expect(err).NotTo(HaveOccurred())
		m := &module.Module{Name: "example", Commands: []module.Command{{Name: "ship", Doc: module.Doc{Body: "S\n"}, SidecarDir: "commands/ship",
			Sidecars: []module.Sidecar{{File: "commands/ship/notes\x1b.md", Doc: d}, {File: "commands/ship/plain.md", Doc: module.Doc{Body: "P\n"}}}}}}
		_, fs, err := c.Plan(m, ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(fs).To(ConsistOf(MatchFields(IgnoreExtras, Fields{"Severity": Equal(target.SeverityWarn), "Kind": Equal("command"),
			"Artifact": Equal("commands/ship/notes?.md"), "Code": Equal("claude-code/allowed-tools"),
			"Text": Equal("pre-approves tools Claude Code then runs without asking: Bash(*)")})))
	})

	It("names every tool, once each, however long the list, so padding cannot hide one", func() {
		tools := make([]string, 0, 202)
		for i := range 200 {
			tools = append(tools, fmt.Sprintf("Tool%03d", i))
		}
		tools = append(tools, "Tool000", "Bash(*)")
		d, err := module.ParseDoc([]byte("---\nallowed-tools: [" + strings.Join(tools, ", ") + "]\n---\nS\n"))
		Expect(err).NotTo(HaveOccurred())
		_, fs, err := c.Plan(&module.Module{Name: "example", Commands: []module.Command{{Name: "ship", Doc: d}}}, ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(fs).To(HaveLen(1))
		Expect(fs[0].Text).To(HaveSuffix(", Tool199, Bash(*)"))
		Expect(strings.Count(fs[0].Text, "Tool000")).To(Equal(1))
	})
})
