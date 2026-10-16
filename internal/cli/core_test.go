package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"

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
		Expect(lola("ls", "-C", proj, "--plain").out).To(Equal("example\tclaude-code\tok\ton\t" + mod + "\n"))

		r = lola("update", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("✓ rendered example (claude-code)"))

		r = lola("rm", "example", "-y", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("✗ delete claude-code .claude/commands/ship.md"))
		Expect(r.out).To(ContainSubstring("→ still in your cache: lola cache rm example"))
		Expect(filepath.Join(proj, ".mcp.json")).NotTo(BeAnExistingFile())
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
		Expect(r.out).To(ContainSubstring("→ example pre-install hook scripts/setup.sh (claude-code): would-run"))
		Expect(r.err).NotTo(ContainSubstring("setting up"))

		r = lola("install", mod, "-C", proj, "--allow-hooks")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(ContainSubstring("setting up for claude-code"), "hook output goes to stderr")
		Expect(r.out).To(ContainSubstring("✓ example pre-install hook scripts/setup.sh (claude-code): ran"))
		Expect(r.out).To(ContainSubstring("✓ example post-install hook scripts/done.sh (claude-code): ran"))

		Expect(lola("install", "example", "-C", proj).code).To(Equal(1), "--allow-hooks is consent for one run only")
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
		Expect(lola("install", "../example").code).To(Equal(0))
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

	It("colors per LOLA_COLOR, then NO_COLOR, and never under --plain; glyphs stay", func() {
		Expect(lola("install", mod, "-C", proj).code).To(Equal(0))
		Expect(lola("list", "-C", proj).out).To(ContainSubstring("✓ ok"))
		Expect(lola("list", "-C", proj).out).NotTo(ContainSubstring("\x1b["), "NO_COLOR is set by testenv")
		GinkgoT().Setenv("LOLA_COLOR", "1")
		Expect(lola("list", "-C", proj).out).To(ContainSubstring("\x1b[32m✓ ok\x1b[0m"))
		Expect(lola("list", "-C", proj, "--plain").out).NotTo(ContainSubstring("\x1b["))
	})
})
