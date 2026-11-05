package cli_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// writeTree creates files under root; files ending in .sh are executable.
func writeTree(root string, files map[string]string) {
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

// fakeAssistant puts an executable named name on the spec's PATH (testenv.Isolate's bin/)
// that prints version, standing in for an installed assistant.
func fakeAssistant(root, name, version string) {
	Expect(os.WriteFile(filepath.Join(root, "bin", name), []byte("#!/bin/sh\necho '"+version+"'\n"), 0o755)).To(Succeed())
}

var _ = Describe("core commands", func() {
	var tmp, proj, mod string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, mod = filepath.Join(tmp, "proj"), filepath.Join(tmp, "example")
		writeTree(mod, map[string]string{
			"commands/ship.md": "Ship $ARGUMENTS\n",
			"mcps.json":        `{"mcpServers":{"lola-gh":{"command":"gh-mcp","args":["--stdio"]}}}`,
		})
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
	})

	It("installs into detected targets off a terminal, lists, renders and uninstalls", func() {
		r := lola("install", mod, "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("✓ create claude-code .claude/commands/ship.md"))
		Expect(r.out).To(ContainSubstring("lola-gh (stdio) runs: gh-mcp --stdio"))
		Expect(r.out).To(ContainSubstring("✓ installed example (claude-code)"))

		r = lola("ls", "-C", proj)
		Expect(r.out).To(MatchRegexp(`example\s+claude-code\s+✓ ok`))
		Expect(lola("ls", "-C", proj, "--plain").out).To(Equal("example\tclaude-code\tok\ton\t" + mod + "\tproject\n"))

		r = lola("update", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("✓ rendered example (claude-code)"))

		r = lola("rm", "example", "-y", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("✗ delete claude-code .claude/commands/ship.md"))
		Expect(r.out).To(ContainSubstring("→ still in your cache: lola cache rm example"))
		Expect(filepath.Join(proj, ".mcp.json")).NotTo(BeAnExistingFile())
	})

	It("prints a module's file names, MCP command lines and hook scripts without driving the terminal", func() {
		writeTree(mod, map[string]string{"skills/x/SKILL.md": "---\ndescription: x\n---\nx\n", "skills/x/ref/a\x1b[2Jb.md": "x",
			"mcps.json": `{"mcpServers":{"lola-gh":{"command":"gh-mcp","args":["\u001b]0;pwned\u0007"]}}}`,
			"lola.yml":  "name: example\nhooks: {post-install: \"hooks/p\\e[31m.sh\"}\n", "hooks/p\x1b[31m.sh": "#!/bin/sh\nexit 0\n"})
		r := lola("install", mod, "-C", proj, "-n")
		Expect(r.out).To(ContainSubstring(".claude/skills/x/ref/a?[2Jb.md"))
		Expect(r.out).To(ContainSubstring("lola-gh (stdio) runs: gh-mcp \"\\x1b]0;pwned\\a\""))
		Expect(r.out).To(ContainSubstring("post-install hook hooks/p?[31m.sh"))
		const control = "[\x00-\x08\x0b-\x1f\x7f]"
		for _, args := range [][]string{{"install", mod, "-C", proj, "-n"}, {"install", mod, "-C", proj, "--allow-hooks"}} {
			r := lola(args...)
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out+r.err).NotTo(MatchRegexp(control), strings.Join(args, " "))
		}
		Expect(os.Remove(filepath.Join(proj, ".claude", "skills", "x", "ref", "a\x1b[2Jb.md"))).To(Succeed())
		r = lola("render", "-C", proj, "--plain")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring(".claude/skills/x/ref/a?[2Jb.md"))
		Expect(r.out + r.err).NotTo(MatchRegexp(control))
	})

	It("prints a module's rule and hook file names in warnings and errors without driving the terminal", func() {
		writeTree(mod, map[string]string{"skills/x/SKILL.md": "---\ndescription: x\n---\nx\n", "rules/a\x1b[2Jb.txt": "x"})
		r := lola("cache", "add", mod)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out + r.err).To(ContainSubstring("rules/a?[2Jb.txt is not a rule"))
		Expect(r.out + r.err).NotTo(ContainSubstring("\x1b"))
		r = lola("cache", "show", "example")
		Expect(r.out + r.err).NotTo(ContainSubstring("\x1b"))
		r = lola("cache", "show", "example", "--json")
		Expect(r.out + r.err).NotTo(ContainSubstring("\x1b"))
		Expect(r.out + r.err).NotTo(ContainSubstring(`\u001b`))

		bad := filepath.Join(GinkgoT().TempDir(), "bad")
		writeTree(bad, map[string]string{"skills/x/SKILL.md": "---\ndescription: x\n---\nx\n", "skills/\x1b[2Jy/note": "x",
			"lola.yml": "name: bad\nhooks: {post-install: \"hooks/p\\e[31m.sh\"}\n", "hooks/p\x1b[31m.sh": "#!/bin/sh\nexit 0\n"})
		Expect(os.Chmod(filepath.Join(bad, "hooks", "p\x1b[31m.sh"), 0o644)).To(Succeed())
		r = lola("install", bad, "-C", proj, "-n")
		Expect(r.code).NotTo(Equal(0))
		Expect(r.out + r.err).To(ContainSubstring("hooks/p?[31m.sh"))
		Expect(r.out + r.err).NotTo(ContainSubstring("\x1b"))
		r = lola("install", bad, "-C", proj, "-n", "--json")
		Expect(r.out + r.err).NotTo(ContainSubstring("\x1b"))
		Expect(r.out + r.err).NotTo(ContainSubstring(`\u001b`))
	})

	It("prints a cached module's hook script without driving the terminal", func() {
		writeTree(mod, map[string]string{"skills/x/SKILL.md": "---\ndescription: x\n---\nx\n",
			"lola.yml": "name: example\nhooks: {post-install: \"hooks/p\\e[31m.sh\"}\n", "hooks/p\x1b[31m.sh": "#!/bin/sh\nexit 0\n"})
		Expect(lola("cache", "add", mod).code).To(Equal(0))
		r := lola("cache", "show", "example")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("post-install: hooks/p?[31m.sh"))
		Expect(r.out + r.err).NotTo(ContainSubstring("\x1b"))
		r = lola("cache", "show", "example", "--json")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("hooks/p?[31m.sh"))
		Expect(r.out).NotTo(ContainSubstring(`\u001b`))
	})

	It("prints what a target skipped with a hint, and --strict fails without writing", func() {
		writeTree(mod, map[string]string{"agents/bare.md": "No description.\n"})
		r := lola("install", mod, "-C", proj, "--strict")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("--strict: 1 finding(s); nothing was changed"))
		Expect(filepath.Join(proj, ".lola/project.yml")).NotTo(BeAnExistingFile())

		r = lola("install", mod, "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("✗ skipped example: claude-code agent bare: claude-code needs a description in the agent's frontmatter\n    hint: add description: to the agent's frontmatter"))
		Expect(lola("install", mod, "-C", proj, "--plain").out).To(ContainSubstring("finding\tskip\texample\tclaude-code\tagent\tbare\trequired-key-missing\t"))
	})

	It("--plain leaves out info findings on install, as human output does; check keeps them", func() {
		Expect(os.Remove(filepath.Join(tmp, "bin", "claude"))).To(Succeed())
		r := lola("install", mod, "-C", proj, "-t", "claude-code", "--plain")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).NotTo(ContainSubstring("finding\tinfo"))

		GinkgoT().Chdir(mod)
		r = lola("check", "--plain", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("finding\tinfo"))
	})

	It("lists a module nothing of which a target could render with the warn glyph", func() {
		Expect(os.RemoveAll(mod)).To(Succeed())
		writeTree(mod, map[string]string{"agents/bare.md": "No description.\n"})
		Expect(lola("install", mod, "-C", proj).code).To(Equal(0))
		Expect(lola("ls", "-C", proj).out).To(MatchRegexp(`example\s+claude-code\s+⚠ skipped`))
	})

	It("lola check reports findings in every output mode and fails only under --strict", func() {
		r := lola("check", mod)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("✓ example: claude-code renders everything"))

		writeTree(mod, map[string]string{"agents/bare.md": "No description.\n"})
		GinkgoT().Chdir(mod)
		r = lola("check")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("✗ skipped example: claude-code agent bare: claude-code needs a description"))
		Expect(lola("check", "--plain").out).To(ContainSubstring("finding\tskip\texample\tclaude-code\tagent\tbare\trequired-key-missing\t"))

		var rep struct {
			Modules []struct {
				Name     string           `json:"name"`
				Findings []map[string]any `json:"findings"`
			} `json:"modules"`
			Findings []map[string]any `json:"findings"`
		}
		Expect(json.Unmarshal([]byte(lola("check", "--json").out), &rep)).To(Succeed())
		Expect(rep.Modules[0].Name).To(Equal("example"))
		Expect(rep.Modules[0].Findings).To(ContainElement(HaveKeyWithValue("code", "required-key-missing")))
		Expect(rep.Findings).To(ConsistOf(HaveKeyWithValue("target", "gemini-cli"), HaveKeyWithValue("target", "opencode")),
			"claude is installed in this spec, the others are not")

		r = lola("check", "--strict", "-t", "claude-code")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("--strict: 1 finding(s); nothing was changed"))
		Expect(r.err).To(ContainSubstring("hint: fix the module; lola install renders what each target supports"))
		Expect(r.err).NotTo(ContainSubstring("--force"))
		Expect(r.err).NotTo(ContainSubstring("rerun without --strict"))
	})

	It("shows which list member a plan adds to a shared config file", func() {
		writeTree(mod, map[string]string{"rules/style.md": "Be brief.\n"})
		r := lola("install", mod, "-C", proj, "-t", "gemini-cli,opencode", "--dry-run")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring(`✓ create gemini-cli  .gemini/settings.json /context/includeDirectories ".gemini/lola/example"`))
		Expect(r.out).To(ContainSubstring(`✓ create opencode    opencode.json /instructions ".opencode/lola/example/*.md"`))
	})

	It("install --no-rules is remembered in the manifest and shown by list; --rules turns them back on", func() {
		writeTree(mod, map[string]string{"rules/style.md": "Prefer small diffs.\n"})
		r := lola("install", mod, "--no-rules", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(filepath.Join(proj, ".claude/rules/lola/example/style.md")).NotTo(BeAnExistingFile())
		b, err := os.ReadFile(filepath.Join(proj, ".lola/project.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(ContainSubstring("rules: false"))
		Expect(lola("ls", "-C", proj).out).To(MatchRegexp(`example\s+claude-code\s+✓ ok\s+⚠ off`))
		var rows struct {
			Modules []map[string]any `json:"modules"`
		}
		Expect(json.Unmarshal([]byte(lola("ls", "-C", proj, "--json").out), &rows)).To(Succeed())
		Expect(rows.Modules[0]["rules"]).To(BeFalse())

		Expect(lola("update", "-C", proj).code).To(Equal(0))
		Expect(filepath.Join(proj, ".claude/rules/lola/example/style.md")).NotTo(BeAnExistingFile(), "render keeps rules off")
		r = lola("install", "example", "--rules", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(filepath.Join(proj, ".claude/rules/lola/example/style.md")).To(BeARegularFile())
		Expect(lola("ls", "-C", proj, "--plain").out).To(HavePrefix("example\tclaude-code\tok\ton\t"))

		r = lola("install", "example", "--rules", "--no-rules", "-C", proj)
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("none of the others can be"))
	})

	It("runs install hooks only with consent: refused off a terminal, listed by --dry-run, run with --allow-hooks", func() {
		writeTree(mod, map[string]string{
			"lola.yml":         "name: example\nhooks: {pre-install: scripts/setup.sh, post-install: scripts/done.sh}\n",
			"scripts/setup.sh": "#!/bin/sh\necho \"setting up for $LOLA_ASSISTANT\"\n",
			"scripts/done.sh":  "#!/bin/sh\necho done\n",
		})
		r := lola("install", mod, "-C", proj)
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: example has a pre-install hook, and lola runs hooks only with your consent"))
		Expect(r.err).To(ContainSubstring("hint: then rerun with --allow-hooks, or on a terminal answer the prompt"))
		Expect(filepath.Join(proj, ".claude/commands/ship.md")).NotTo(BeAnExistingFile())

		r = lola("install", mod, "-C", proj, "--dry-run")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("→ example pre-install hook scripts/setup.sh (claude-code): would run"), "would-run is the JSON value")
		Expect(r.err).NotTo(ContainSubstring("setting up"))

		r = lola("install", mod, "-C", proj, "--allow-hooks")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(ContainSubstring("setting up for claude-code"), "hook output goes to stderr")
		Expect(r.out).To(ContainSubstring("✓ example pre-install hook scripts/setup.sh (claude-code): ran"))
		Expect(r.out).To(ContainSubstring("✓ example post-install hook scripts/done.sh (claude-code): ran"))

		r = lola("install", "example", "-C", proj)
		Expect(r.code).To(Equal(0), "the same version runs no hook, so it needs no consent: %s", r.err)
		Expect(r.err).NotTo(ContainSubstring("setting up"))

		writeTree(mod, map[string]string{"commands/more.md": "More\n"})
		Expect(lola("install", mod, "-C", proj).code).To(Equal(1), "--allow-hooks is consent for one run only")
	})

	It("installs runtime hooks only with consent: skipped off a terminal, listed by --dry-run, recorded by --allow-runtime-hooks", func() {
		plugin := filepath.Join(tmp, "deploy")
		writeTree(plugin, map[string]string{
			".claude-plugin/plugin.json": `{"name":"deploy"}`,
			"commands/ship.md":           "Ship\n",
			"hooks/hooks.json": `{"hooks":{"Stop":[{"matcher":"m\u001b[2J","x":1,"hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/stop.sh"},` +
				`{"type":"http","url":"https://h.example/x","headers":{"A":"$T"},"allowedEnvVars":["T"]}]}]}}`,
		})
		settings := filepath.Join(proj, ".claude/settings.local.json")
		r := lola("install", plugin, "-C", proj, "--dry-run")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("deploy: claude-code: runtime hooks skipped"), "a dry run makes the real run's decision")
		Expect(r.out).To(ContainSubstring("deploy runtime hooks for claude-code (declined):\n" +
			"    Stop, matcher m?[2J: command\n      runs: export CLAUDE_PLUGIN_ROOT='" + filepath.Join(proj, ".lola/modules/deploy") + "'"))
		Expect(r.out).To(ContainSubstring("    Stop, matcher m?[2J: http\n      sends data to: https://h.example/x\n      header: A: $T\n      sends env: T\n      group fields: {\"x\":1}\n"))
		Expect(r.out).NotTo(ContainSubstring("\x1b"))

		r = lola("install", plugin, "-C", proj, "--allow-hooks")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("⚠ deploy: claude-code: runtime hooks skipped: they run inside claude-code with your permissions"))
		Expect(r.out).To(ContainSubstring("hint: review the hooks listed above, then rerun with --allow-runtime-hooks, or answer the prompt on a terminal"))
		Expect(r.out).To(ContainSubstring("deploy runtime hooks for claude-code (declined):"))
		Expect(settings).NotTo(BeAnExistingFile())
		Expect(filepath.Join(proj, ".claude/commands/ship.md")).To(BeARegularFile())

		r = lola("install", "deploy", "-C", proj, "--allow-runtime-hooks", "--json")
		Expect(r.code).To(Equal(0), r.err)
		var res struct {
			Modules []struct {
				RuntimeHooks []map[string]any `json:"runtime_hooks"`
			}
		}
		Expect(json.Unmarshal([]byte(r.out), &res)).To(Succeed())
		Expect(res.Modules[0].RuntimeHooks).To(ConsistOf(
			SatisfyAll(HaveKeyWithValue("event", "Stop"), HaveKeyWithValue("type", "command"), HaveKeyWithValue("status", "consented")),
			SatisfyAll(HaveKeyWithValue("type", "http"), HaveKeyWithValue("headers", []any{"A: $T"}), HaveKeyWithValue("allowed_env_vars", []any{"T"}))))
		Expect(settings).To(BeARegularFile())

		r = lola("render", "-C", proj, "--json")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring(`"status": "consented"`), "the recorded consent holds off a terminal")

		writeTree(proj, map[string]string{".claude/settings.local.json": `{"hooks": {}}`})
		r = lola("render", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("→ deploy: claude-code: a Stop hook group lola installed in .claude/settings.local.json is missing"))
		Expect(lola("render", "-C", proj, "--strict").code).To(Equal(0), "--strict does not escalate drift")
		Expect(r.out).To(ContainSubstring("hint: run with --force to restore it"))
		r = lola("render", "-C", proj, "--allow-runtime-hooks", "--dry-run")
		Expect(r.code).To(Equal(0), r.err)
	})

	It("lists an agent's inline MCP servers for consent with control and bidi characters replaced, and skips the agent without consent", func() {
		plugin := filepath.Join(tmp, "browse")
		writeTree(plugin, map[string]string{
			".claude-plugin/plugin.json": `{"name":"browse"}`,
			"agents/browser.md": "---\ndescription: B\npermissionMode: bypassPermissions\nmcpServers:\n  - github\n" +
				"  - pw: {command: \"npx\\e[2J\", args: [\"a\\u202eb\"], env: {LD_PRELOAD: /x/evil.so}}\n" +
				"  - db: {url: 'https://db.example', headers: {Authorization: \"Bearer \\u202ex\"}}\n---\nB\n",
		})
		r := lola("install", plugin, "-C", proj, "--dry-run")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("browse runtime hooks for claude-code (declined):\n" +
			"    agent browser: MCP server pw: stdio\n      runs: \"npx\\x1b[2J\" \"a\\u202eb\"\n      env: LD_PRELOAD=/x/evil.so\n" +
			"    agent browser: MCP server db: http\n      connects to: https://db.example\n      header: Authorization: Bearer ?x\n"))
		Expect(r.out).To(ContainSubstring("browse: claude-code agent browser: skipped: its inline MCP servers run inside claude-code with your permissions"))
		Expect(r.out).To(ContainSubstring("browse: claude-code agent browser: sets the permission mode it runs in: bypassPermissions (Claude Code v2.1.267+ ignores"))
		Expect(r.out).NotTo(ContainSubstring("\x1b"))
		Expect(r.out).NotTo(ContainSubstring("\u202e"))
		Expect(filepath.Join(proj, ".claude/agents/browser.md")).NotTo(BeAnExistingFile())

		r = lola("install", plugin, "-C", proj, "--dry-run", "--plain")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("runtime-hook\tbrowse\tclaude-code\tmcp_server:pw\tstdio\tdeclined\tagent browser\n"))
		Expect(r.out).To(ContainSubstring("runtime-hook\tbrowse\tclaude-code\tmcp_server:db\thttp\tdeclined\tagent browser\n"))
	})

	It("names the event and the component of each runtime hook in --plain output", func() {
		plugin := filepath.Join(tmp, "deploy")
		writeTree(plugin, map[string]string{
			".claude-plugin/plugin.json": `{"name":"deploy"}`,
			"hooks/hooks.json":           `{"hooks":{"Stop":[{"hooks":[{"type":"prompt","prompt":"p"}]}]}}`,
			"agents/critic.md":           "---\ndescription: C\nhooks: {PreToolUse: [{hooks: [{type: prompt, prompt: q}]}]}\n---\nC\n",
		})
		r := lola("install", plugin, "-C", proj, "--dry-run", "--plain")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("runtime-hook\tdeploy\tclaude-code\tStop\tprompt\tdeclined\t\n"))
		Expect(r.out).To(ContainSubstring("runtime-hook\tdeploy\tclaude-code\tPreToolUse\tprompt\tdeclined\tagent critic\n"))
	})

	Describe("a hook group in the install output", func() {
		var plugin string
		BeforeEach(func() {
			plugin = filepath.Join(tmp, "deploy")
			writeTree(plugin, map[string]string{
				".claude-plugin/plugin.json": `{"name":"deploy"}`,
				"hooks/hooks.json": `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"printf '\u001b]0;x\u0007'; ${CLAUDE_PLUGIN_ROOT}/stop.sh ` +
					strings.Repeat("a", 150) + `"}]}]}}`,
			})
		})

		It("replaces a C1 control, never splits a rune at the 120-byte cut, and ends the member with an ellipsis in the human step line", func() {
			// Canonical JSON sorts "prompt" first and keeps U+009B raw, so it sits inside the visible window.
			// The 25-byte prefix before the é run makes byte 120 fall in the middle of an é.
			writeTree(plugin, map[string]string{
				"hooks/hooks.json": `{"hooks":{"Stop":[{"hooks":[{"type":"prompt","prompt":"` + "\u009b" + `Xy` + strings.Repeat("é", 100) + `"}]}]}}`,
			})
			r := lola("install", plugin, "-C", proj, "--allow-runtime-hooks")
			Expect(r.code).To(Equal(0), r.err)
			var line string
			for _, l := range strings.Split(r.out, "\n") {
				if strings.Contains(l, ".claude/settings.local.json /hooks/Stop ") {
					line = l
				}
			}
			Expect(line).NotTo(BeEmpty(), r.out)
			member := line[strings.Index(line, "/hooks/Stop ")+len("/hooks/Stop "):]
			Expect(member).To(HavePrefix(`{"hooks":[{"prompt":"?Xy`+"é"), "the CSI is replaced, the group's start stays visible")
			Expect(member).NotTo(ContainSubstring("\u009b"))
			Expect(utf8.ValidString(r.out)).To(BeTrue(), "the cut leaves no invalid UTF-8")
			Expect(member).To(HaveSuffix("?…"), "the rune split by the cut becomes ?, then the cut mark")
		})

		It("shows & < > as themselves in the human step line, so a shell operator reads as one", func() {
			writeTree(plugin, map[string]string{"hooks/hooks.json": `{"hooks":{"Stop":[{"hooks":[{"type":"prompt","prompt":"a && b > c"}]}]}}`})
			r := lola("install", plugin, "-C", proj, "--allow-runtime-hooks")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(ContainSubstring(`.claude/settings.local.json /hooks/Stop {"hooks":[{"prompt":"a && b > c","type":"prompt"}]}`))
			Expect(r.out).NotTo(ContainSubstring(`\u00`))
		})

		It("carries the whole group in the --json step, uncut", func() {
			r := lola("install", plugin, "-C", proj, "--allow-runtime-hooks", "--json")
			Expect(r.code).To(Equal(0), r.err)
			var res struct {
				Steps []struct {
					Kind   string
					Key    string
					Member json.RawMessage
				}
			}
			Expect(json.Unmarshal([]byte(r.out), &res)).To(Succeed())
			Expect(res.Steps).To(ContainElement(HaveField("Key", "/hooks/Stop")))
			var member struct{ Hooks []struct{ Command string } }
			for _, st := range res.Steps {
				if st.Key == "/hooks/Stop" {
					Expect(st.Member).NotTo(BeEmpty(), "the step carries a member key")
					Expect(json.Unmarshal(st.Member, &member)).To(Succeed())
				}
			}
			Expect(member.Hooks).To(HaveLen(1))
			Expect(member.Hooks[0].Command).To(And(ContainSubstring("\x1b]0;x\x07"), HaveSuffix(strings.Repeat("a", 150))),
				"JSON carries the full command; the human cap does not apply")
		})
	})

	It("shows a command with newlines on one line in the consent listing, so it cannot pose as another hook line", func() {
		plugin := filepath.Join(tmp, "deploy")
		writeTree(plugin, map[string]string{
			".claude-plugin/plugin.json": `{"name":"deploy"}`,
			"hooks/hooks.json":           `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"true\n    Stop: command\n      runs: evil"}]}]}}`,
		})
		r := lola("install", plugin, "-C", proj, "--dry-run")
		Expect(r.code).To(Equal(0), r.err)
		var heads, runs []string
		for _, l := range strings.Split(r.out, "\n") {
			if strings.HasPrefix(l, "    Stop: command") {
				heads = append(heads, l)
			}
			if strings.Contains(l, "runs:") {
				runs = append(runs, l)
			}
		}
		Expect(heads).To(HaveLen(1), r.out)
		Expect(runs).To(HaveLen(1), r.out)
		Expect(runs[0]).To(HavePrefix("      runs: "))
		Expect(runs[0]).To(HaveSuffix(`true\n    Stop: command\n      runs: evil`), "newlines show as \\n")
	})

	It("reports an empty runtime_hooks list for a module without runtime hooks", func() {
		r := lola("install", mod, "-C", proj, "--json")
		Expect(r.code).To(Equal(0), r.err)
		var res struct{ Modules []map[string]any }
		Expect(json.Unmarshal([]byte(r.out), &res)).To(Succeed())
		Expect(res.Modules[0]["runtime_hooks"]).To(Equal([]any{}))
	})

	It("finds the project by walking up from a subdirectory (Review Focus 5)", func() {
		Expect(lola("install", mod, "-C", proj).code).To(Equal(0))
		sub := filepath.Join(proj, "src", "deep")
		Expect(os.MkdirAll(sub, 0o755)).To(Succeed())
		GinkgoT().Chdir(sub)
		r := lola("list", "--plain")
		Expect(r.out).To(HavePrefix("example\tclaude-code\tok"))
	})

	It("records a relative source relative to the project root", func() {
		GinkgoT().Chdir(proj)
		Expect(lola("install", "../example", "--scope", "project").code).To(Equal(0))
		b, err := os.ReadFile(filepath.Join(proj, ".lola/project.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(ContainSubstring("source: ../example"))
	})

	It("names -t and the installed assistants when none is set up here", func() {
		Expect(os.RemoveAll(filepath.Join(proj, ".claude"))).To(Succeed())
		r := lola("install", mod, "-C", proj)
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: none of your installed assistants is set up here"))
		Expect(r.err).To(ContainSubstring("hint: installed: claude-code"))

		Expect(os.Remove(filepath.Join(tmp, "bin", "claude"))).To(Succeed())
		r = lola("install", mod, "-C", proj)
		Expect(r.err).To(ContainSubstring("error: no assistant found on your PATH"))
		Expect(r.err).To(ContainSubstring("hint: pass -t with one or more of: claude-code"))
		Expect(lola("install", mod, "-C", proj, "-t", "claude-code").code).To(Equal(0))
		Expect(lola("install", mod, "-C", proj).code).To(Equal(0), "the manifest's targets are reused")
	})

	It("coaches a missing argument from the argGuide", func() {
		r := lola("install")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("error: lola install needs a module or source\n" +
			"  hint: usage: lola install <source|name>... [-t target]\n" +
			"  hint: find one: lola cache list (cached modules), or a module directory, archive or git URL\n" +
			"  hint: example: lola install ./my-module -t claude-code\n"))
	})

	It("refuses to uninstall without -y off a terminal, after printing the plan", func() {
		Expect(lola("install", mod, "-C", proj).code).To(Equal(0))
		r := lola("uninstall", "example", "-C", proj)
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("plan (dry run; nothing was written):"))
		Expect(r.err).To(ContainSubstring("hint: pass -y to confirm"))
		Expect(filepath.Join(proj, ".claude/commands/ship.md")).To(BeARegularFile())
	})

	It("--force never skips the confirmation", func() {
		Expect(lola("install", mod, "-C", proj).code).To(Equal(0))
		r := lola("uninstall", "example", "--force", "-C", proj)
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("pass -y to confirm"))
		Expect(filepath.Join(proj, ".claude/commands/ship.md")).To(BeARegularFile())
	})

	It("dry-run prints the plan and writes nothing", func() {
		r := lola("install", mod, "-C", proj, "-n")
		Expect(r.code).To(Equal(0))
		Expect(r.out).To(HavePrefix("plan (dry run; nothing was written):\n"))
		Expect(filepath.Join(proj, ".lola")).NotTo(BeADirectory())
	})

	It("reports conflicts with every destination and a --force hint, and writes nothing", func() {
		writeTree(proj, map[string]string{".claude/commands/ship.md": "mine"})
		r := lola("install", mod, "-C", proj)
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("refusing to change anything: 1 conflict(s)\n  claude-code: .claude/commands/ship.md: exists and was not written by lola"))
		Expect(r.err).To(ContainSubstring("hint: rerun with --force"))
		Expect(filepath.Join(proj, ".mcp.json")).NotTo(BeAnExistingFile())
		r = lola("install", mod, "-C", proj, "--force")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("[forced: unowned]"))
	})

	It("never rewrites a corrupt .mcp.json, even with --force", func() {
		writeTree(proj, map[string]string{".mcp.json": "{\n  \"mcpServers\": {,\n}\n"})
		r := lola("install", mod, "-C", proj, "--force")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring(".mcp.json"))
		Expect(r.err).To(ContainSubstring("fix it by hand"))
		b, _ := os.ReadFile(filepath.Join(proj, ".mcp.json"))
		Expect(string(b)).To(Equal("{\n  \"mcpServers\": {,\n}\n"))
	})

	It("emits the install result as JSON with [] arrays", func() {
		r := lola("install", mod, "-C", proj, "--json")
		Expect(r.code).To(Equal(0), r.err)
		var v map[string]any
		Expect(json.Unmarshal([]byte(r.out), &v)).To(Succeed())
		Expect(v["dry_run"]).To(BeFalse())
		Expect(v["notices"]).To(Equal([]any{}))
		Expect(v["modules"]).To(HaveLen(1))
		Expect(r.out).NotTo(ContainSubstring("null"))
	})

	It("uninstalls by alias from user scope regardless of the working directory", func() {
		Expect(lola("install", mod, "--user", "-t", "claude-code").code).To(Equal(0))
		home := os.Getenv("HOME")
		Expect(filepath.Join(home, ".claude/commands/ship.md")).To(BeARegularFile())
		GinkgoT().Chdir(tmp)
		r := lola("del", "example", "--user", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(filepath.Join(home, ".claude/commands/ship.md")).NotTo(BeAnExistingFile())
	})

	It("lists the pin and an available update, keeping --plain's columns", func() {
		archive := filepath.Join(tmp, "kit.zip")
		writeZip := func(body string) {
			var buf bytes.Buffer
			zw := zip.NewWriter(&buf)
			for name, content := range map[string]string{"kit/lola.yml": "name: kit\n", "kit/commands/k.md": body} {
				w, err := zw.Create(name)
				Expect(err).NotTo(HaveOccurred())
				_, err = w.Write([]byte(content))
				Expect(err).NotTo(HaveOccurred())
			}
			Expect(zw.Close()).To(Succeed())
			Expect(os.WriteFile(archive, buf.Bytes(), 0o644)).To(Succeed())
		}
		writeZip("v1\n")
		Expect(lola("install", archive, "-C", proj).code).To(Equal(0))
		writeZip("v2\n")
		Expect(lola("cache", "update", "kit").code).To(Equal(0))

		Expect(lola("list", "-C", proj).out).To(MatchRegexp(`kit\s+claude-code\s+✓ ok → update available\s`))
		Expect(lola("list", "-C", proj, "--plain").out).To(Equal("kit\tclaude-code\tok\ton\t" + archive + "\tproject\n"))
		var res struct{ Modules []map[string]any }
		Expect(json.Unmarshal([]byte(lola("list", "-C", proj, "--json").out), &res)).To(Succeed())
		Expect(res.Modules).To(HaveLen(1))
		Expect(res.Modules[0]).To(HaveKeyWithValue("update_available", true))
		Expect(res.Modules[0]).To(HaveKeyWithValue("pin", res.Modules[0]["digest"]))
		Expect(lola("list", "--help").out).To(And(ContainSubstring("stale (project.yml pins another version"),
			ContainSubstring("not installed (listed in project.yml: run lola sync)"),
			ContainSubstring("update available: your cache has a newer version for the ref: lola sync --update <module>")))
	})

	It("lists an entry project.yml lists but lock.json does not record as not installed, per target", func() {
		kit := filepath.Join(proj, "mods", "kit")
		writeTree(kit, map[string]string{"lola.yml": "name: kit\n", "commands/kit.md": "Kit\n"})
		Expect(lola("install", kit, "-C", proj, "-t", "claude-code").code).To(Equal(0))
		mpath := filepath.Join(proj, ".lola", "project.yml")
		b, err := os.ReadFile(mpath)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(mpath, append(b, []byte("  - name: other\n    source: mods/other\n    targets: [claude-code, opencode]\n")...), 0o644)).To(Succeed())

		r := lola("list", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(MatchRegexp(`other\s+claude-code\s+⚠ not installed\s`))
		Expect(r.out).To(MatchRegexp(`other\s+opencode\s+⚠ not installed\s`))
		Expect(lola("list", "-C", proj, "--plain").out).To(ContainSubstring("other\tclaude-code\tnot_installed\ton\tmods/other\tproject\n"))
		var res struct{ Modules []map[string]any }
		Expect(json.Unmarshal([]byte(lola("list", "-C", proj, "--json").out), &res)).To(Succeed())
		Expect(res.Modules).To(ContainElement(And(HaveKeyWithValue("name", "other"), HaveKeyWithValue("status", "not_installed"),
			HaveKeyWithValue("digest", ""), HaveKeyWithValue("pin", ""), HaveKeyWithValue("update_available", false))))
	})

	It("colors per LOLA_COLOR, then NO_COLOR, and never under --plain; glyphs stay", func() {
		Expect(lola("install", mod, "-C", proj).code).To(Equal(0))
		Expect(lola("list", "-C", proj).out).To(ContainSubstring("✓ ok"))
		Expect(lola("list", "-C", proj).out).NotTo(ContainSubstring("\x1b["), "NO_COLOR is set by testenv")
		GinkgoT().Setenv("LOLA_COLOR", "1")
		Expect(lola("list", "-C", proj).out).To(ContainSubstring("\x1b[32m✓ ok\x1b[0m"))
		Expect(lola("list", "-C", proj, "--plain").out).NotTo(ContainSubstring("\x1b["))
	})
})
