package cli_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// A source's refusal hints the user's own command, with the user's flags.
var _ = Describe("hints of a refused source keep your command", func() {
	var tmp, proj, web string
	// serve publishes a bare clone of a repository holding files as o/r on a local server, and
	// points git's github.com at it, so a github.com URL reaches the fixture.
	serve := func(files map[string]string) {
		work := filepath.Join(tmp, "work")
		writeTree(work, files)
		for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"commit", "-q", "-m", "init"}} {
			Expect(exec.Command("git", append([]string{"-C", work}, args...)...).Run()).To(Succeed())
		}
		Expect(exec.Command("git", "clone", "-q", "--bare", work, filepath.Join(tmp, "srv", "o", "r")).Run()).To(Succeed())
		config := filepath.Join(tmp, "gitconfig")
		Expect(os.WriteFile(config, []byte("[url \""+testenv.ServeGit(filepath.Join(tmp, "srv"))+"/\"]\n\tinsteadOf = https://github.com/\n"), 0o600)).To(Succeed())
		GinkgoT().Setenv("GIT_CONFIG_GLOBAL", config)
	}
	hintOf := func(r result) string {
		GinkgoHelper()
		_, after, ok := strings.Cut(r.err, "  hint: ")
		Expect(ok).To(BeTrue(), r.err)
		line, _, _ := strings.Cut(after, "\n")
		return line
	}

	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj = filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
		serve(map[string]string{"skills/a/SKILL.md": "---\nname: a\ndescription: d\n---\nA\n", "docs/x/commands/ship.md": "Ship\n"})
		web = "https://github.com/o/r/tree/main/skills/"
	})

	It("info on a web URL says it is a web page and hints lola info at the repository, keeping its flags", func() {
		r := lola("info", web, "--plain", "-t", "claude-code")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(HavePrefix("error: " + web + " is a github.com web page, not a repository URL; "))
		Expect(r.err).NotTo(ContainSubstring("not found, or it is private"))
		Expect(hintOf(r)).To(Equal("lola info https://github.com/o/r --plain --target claude-code --ref main"))
	})

	It("install on a web URL keeps the verb, the scope, -t and -y, and the hint installs", func() {
		r := lola("install", web, "-C", proj, "-t", "claude-code", "-y", "--plain")
		Expect(r.code).To(Equal(1))
		hint := hintOf(r)
		Expect(hint).To(Equal("lola install https://github.com/o/r --plain --root " + proj + " --target claude-code --yes --ref main"))
		Expect(runHint(hint).code).To(Equal(0))
		Expect(filepath.Join(proj, ".claude/skills/a/SKILL.md")).To(BeARegularFile())
	})

	It("a web URL on a directory of skills hints the repository root and offers --path skills/<name> in the message, never --path skills", func() {
		r := lola("info", web, "--plain")
		Expect(r.err).To(ContainSubstring("to take one skill, use --path skills/<name>"))
		Expect(hintOf(r)).NotTo(ContainSubstring("--path"))

		r = lola("info", "https://github.com/o/r/tree/main/docs/x", "--plain")
		Expect(hintOf(r)).To(Equal("lola info https://github.com/o/r --plain --ref main --path docs/x"))
	})

	It("--path <dir-of-skills> hints the install itself with your flags, and that install works", func() {
		url := "https://github.com/o/r"
		r := lola("install", url, "--path", "skills", "-C", proj, "-t", "claude-code", "--plain")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("that directory holds skills, and a module root holds them under skills/"))
		hint := hintOf(r)
		Expect(hint).To(Equal("lola install " + url + " --plain --root " + proj + " --target claude-code"))
		Expect(runHint(hint).code).To(Equal(0))
		Expect(filepath.Join(proj, ".claude/skills/a/SKILL.md")).To(BeARegularFile())
	})
})

var _ = Describe("a dry run's hints name the run that does the work", func() {
	It("sync -n with a runtime-hook module hints --trust=runtime-hooks and a real --force run, never --dry-run --force", func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, plugin := filepath.Join(tmp, "proj"), filepath.Join(tmp, "deploy")
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
		writeTree(plugin, map[string]string{
			".claude-plugin/plugin.json": `{"name":"deploy"}`,
			"agents/critic.md":           "Criticize.\n",
			"hooks/hooks.json":           `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/stop.sh"}]}]}}`,
		})
		Expect(lola("install", plugin, "-C", proj, "-t", "claude-code", "--trust=hooks").code).To(Equal(0))
		Expect(os.RemoveAll(filepath.Join(proj, ".claude"))).To(Succeed())

		r := lola("sync", "-n", "-C", proj, "-v")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("consent to them: lola sync -C " + proj + " --trust=runtime-hooks"))
		Expect(r.out).To(ContainSubstring("render it anyway: lola sync --root " + proj + " --verbose --force"))
		Expect(r.out).NotTo(ContainSubstring("--dry-run"))
	})
})

var _ = Describe("a dry run's consent hint is the real run", func() {
	It("install -n with runtime hooks hints the real install with --trust=runtime-hooks, never --dry-run, and it installs them", func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, plugin := filepath.Join(tmp, "proj"), filepath.Join(tmp, "deploy")
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
		writeTree(plugin, map[string]string{
			".claude-plugin/plugin.json": `{"name":"deploy"}`,
			"commands/ship.md":           "Ship\n",
			"hooks/hooks.json":           `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/stop.sh"}]}]}}`,
		})
		r := lola("install", plugin, "-C", proj, "-t", "claude-code", "-n")
		Expect(r.code).To(Equal(0), r.err)
		_, after, ok := strings.Cut(r.out, "consent to them: ")
		Expect(ok).To(BeTrue(), r.out)
		hint, _, _ := strings.Cut(after, "\n")
		Expect(hint).To(Equal("lola install " + plugin + " --root " + proj + " --target claude-code --trust=runtime-hooks"))
		Expect(runHint(hint).code).To(Equal(0))
		Expect(filepath.Join(proj, ".claude/settings.local.json")).To(BeARegularFile(), "the hinted run installs the hooks")
	})
})

var _ = Describe("a forceable skip planned under -n hints a real run", func() {
	var tmp, proj, kit string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, kit = filepath.Join(tmp, "proj"), filepath.Join(tmp, "kit")
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
		writeTree(kit, map[string]string{"lola.yml": "name: kit\n", "agents/critic.md": "Criticize.\n"})
	})

	anyway := func(r result) string {
		GinkgoHelper()
		Expect(r.code).To(Equal(0), r.err)
		_, after, ok := strings.Cut(r.out, "render it anyway: ")
		Expect(ok).To(BeTrue(), r.out)
		line, _, _ := strings.Cut(after, "\n")
		Expect(line).NotTo(ContainSubstring("--dry-run"))
		Expect(strings.Fields(line)).NotTo(ContainElement("-n"))
		return line
	}

	It("install -n hints the real install, and it renders", func() {
		hint := anyway(lola("install", kit, "-C", proj, "-t", "claude-code", "-n", "-v"))
		Expect(runHint(hint).code).To(Equal(0))
		Expect(filepath.Join(proj, ".claude/agents/critic.md")).To(BeARegularFile())
	})

	It("profile --split -n hints the real split, and it renders", func() {
		writeTree(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "lola", "profiles"), map[string]string{"base1.yml": "version: 1\n",
			"mine.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + kit + "}\n"})
		sw := lola("profile", "mine", "--switch", "--user", "-y", "--trust=all")
		Expect(sw.code).To(Equal(0), sw.err)
		mixed := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "lola", "profiles", "mine.yml") // on, then edited by hand
		Expect(os.WriteFile(mixed, []byte("version: 1\ntargets: [claude-code]\nbundle: [base1]\nmodules:\n  - {name: kit, source: "+kit+"}\n"), 0o644)).To(Succeed())
		hint := anyway(lola("profile", "mine", "--split", "--user", "-n", "-v"))
		Expect(runHint(hint).code).To(Equal(0))
	})

	It("profile --edit <module> -n hints the real edit, and it renders", func() {
		hint := anyway(lola("profile", "--edit", kit, "-C", proj, "-t", "claude-code", "-n", "-v"))
		Expect(runHint(hint).code).To(Equal(0))
		Expect(filepath.Join(proj, ".claude/agents/critic.md")).To(BeARegularFile())
	})
})
