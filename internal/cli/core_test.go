package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

func writeTree(root string, files map[string]string) {
	for p, c := range files {
		full := filepath.Join(root, p)
		Expect(os.MkdirAll(filepath.Dir(full), 0o755)).To(Succeed())
		Expect(os.WriteFile(full, []byte(c), 0o644)).To(Succeed())
	}
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
			"lola.yml":         "name: example\nhooks: {pre-install: scripts/setup.sh}\n",
		})
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
	})

	It("installs into detected targets off a terminal, lists, renders and uninstalls", func() {
		r := lola("install", mod, "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("✓ create claude-code .claude/commands/ship.md"))
		Expect(r.out).To(ContainSubstring("lola-gh (stdio) runs: gh-mcp --stdio"))
		Expect(r.out).To(ContainSubstring("⚠ example declares install hooks that lola does not run:\n    pre-install: scripts/setup.sh\n  Review the script and run it yourself if you trust it."))
		Expect(r.out).To(ContainSubstring("✓ installed example (claude-code)"))

		r = lola("ls", "-C", proj)
		Expect(r.out).To(MatchRegexp(`example\s+claude-code\s+✓ ok`))
		Expect(lola("ls", "-C", proj, "--plain").out).To(Equal("example\tclaude-code\tok\t" + mod + "\n"))

		r = lola("update", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("✓ rendered example (claude-code)"))

		r = lola("rm", "example", "-y", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("✗ delete claude-code .claude/commands/ship.md"))
		Expect(r.out).To(ContainSubstring("→ still in your cache: lola cache rm example"))
		Expect(filepath.Join(proj, ".mcp.json")).NotTo(BeAnExistingFile())
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

	It("names -t and the valid targets when nothing is detected", func() {
		Expect(os.RemoveAll(filepath.Join(proj, ".claude"))).To(Succeed())
		r := lola("install", mod, "-C", proj)
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: no assistant detected here"))
		Expect(r.err).To(ContainSubstring("hint: pass -t with one or more of: claude-code"))
		Expect(lola("install", mod, "-C", proj, "-t", "claude-code").code).To(Equal(0))
		Expect(lola("install", mod, "-C", proj).code).To(Equal(0), "the manifest's targets are reused")
	})

	It("coaches a missing argument from the argGuide", func() {
		r := lola("install")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("error: lola install needs a module or source\n" +
			"  hint: usage: lola install <source|name>... [-t target]\n" +
			"  hint: find one: lola cache list (cached modules), or a module directory\n" +
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
