package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("selections.yaml in list, check and sync", func() {
	var tmp, proj, mod, selections string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, mod = filepath.Join(tmp, "proj"), filepath.Join(tmp, "example")
		writeTree(mod, map[string]string{
			"commands/ship.md": "Ship $ARGUMENTS\n",
			"commands/test.md": "Test\n",
			"agents/bare.md":   "No description.\n",
		})
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
		selections = filepath.Join(tmp, "config", "lola", "selections.yaml")
		Expect(os.MkdirAll(filepath.Dir(selections), 0o700)).To(Succeed())
	})
	skip := func(items string) {
		Expect(os.WriteFile(selections, []byte("version: 1\nprojects:\n  "+proj+":\n    example:\n      skip: ["+items+"]\n"), 0o600)).To(Succeed())
	}

	It("lists a SKIPPED count only when a module has skipped components, with names in JSON", func() {
		Expect(lola("install", mod, "-C", proj).code).To(Equal(0))
		Expect(lola("ls", "-C", proj).out).NotTo(ContainSubstring("SKIPPED"))

		skip("command/test")
		r := lola("sync", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("✗ delete claude-code .claude/commands/test.md"))
		Expect(r.out).To(ContainSubstring("→ example: skipped in selections.yaml: command/test"))

		r = lola("ls", "-C", proj)
		Expect(r.out).To(MatchRegexp(`SCOPE\s+MODULE\s+TARGET\s+STATUS\s+RULES\s+SKIPPED\s+SOURCE`))
		Expect(r.out).To(MatchRegexp(`example\s+claude-code\s+✓ ok\s+on\s+1\s+` + mod))
		var v struct {
			Modules []struct {
				Skipped []string `json:"skipped"`
			} `json:"modules"`
		}
		Expect(json.Unmarshal([]byte(lola("ls", "-C", proj, "--json").out), &v)).To(Succeed())
		Expect(v.Modules[0].Skipped).To(Equal([]string{"command/test"}))
		Expect(lola("ls", "-C", proj, "--plain").out).To(Equal("example\tclaude-code\tok\ton\t"+mod+"\tproject\tproject.yml\n"), "--plain rows keep their columns, scope and from last")
	})

	It("says a dry run would skip, since it saves nothing", func() {
		r := lola("install", mod, "-C", proj, "--skip", "command/test", "--dry-run")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("→ example: would be skipped in selections.yaml: command/test"))
		Expect(selections).NotTo(BeAnExistingFile())

		r = lola("install", mod, "-C", proj, "--skip", "command/test")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("→ example: skipped in selections.yaml: command/test"))
	})

	It("check names skipped components and reports nothing lost for them", func() {
		GinkgoT().Chdir(proj)
		r := lola("check", mod, "-t", "claude-code")
		Expect(r.out).To(ContainSubstring("claude-code agent bare"))

		skip("agent/bare")
		r = lola("check", mod, "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("→ example: skipped in selections.yaml: agent/bare"))
		Expect(r.out).NotTo(ContainSubstring("agent bare"))
		Expect(lola("check", mod, "-t", "claude-code", "--plain").out).To(ContainSubstring("skipped\texample\tagent/bare\n"))
		Expect(lola("check", mod, "-t", "claude-code", "--strict").code).To(Equal(0))
	})

	It("check reads the selections of the scope --user or -C names", func() {
		GinkgoT().Chdir(tmp)
		skip("agent/bare")
		r := lola("check", mod, "-t", "claude-code", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("→ example: skipped in selections.yaml: agent/bare"))
		r = lola("check", mod, "-t", "claude-code", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).NotTo(ContainSubstring("skipped in selections.yaml"), "the project's selections are not user scope's")

		Expect(os.Remove(selections)).To(Succeed())
		writeTree(filepath.Join(tmp, "config", "lola", "profiles"), map[string]string{"default.yml": "version: 1\nmodules:\n" +
			"  - {name: example, source: " + mod + ", skip: [command/test]}\n"})
		r = lola("check", mod, "-t", "claude-code", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("→ example: skipped in default (base): command/test"), "default's entry picks it at user scope")
		r = lola("check", mod, "-t", "claude-code", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).NotTo(ContainSubstring("skipped in"), "default's entry picks are not the project's")
	})

	It("reports an invalid selections.yaml with its key path and a hint", func() {
		Expect(os.WriteFile(selections, []byte("version: 1\nglobal: [x]\n"), 0o600)).To(Succeed())
		r := lola("ls", "-C", proj)
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: " + selections + ":2: global: must be a mapping of module names, not a list"))
		Expect(r.err).To(ContainSubstring("hint: write module-name: followed by skip:, include: and only: lists"))
	})
})

var _ = Describe("lola install --exact (cherry-pick design §3.2, §3.4)", func() {
	var tmp, proj, mod, selections string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, mod = filepath.Join(tmp, "proj"), filepath.Join(tmp, "example")
		writeTree(mod, map[string]string{"commands/ship.md": "Ship\n", "commands/test.md": "Test\n"})
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
		selections = filepath.Join(tmp, "config", "lola", "selections.yaml")
	})

	It("refuses --exact without --only, hinting the run without it", func() {
		r := lola("install", mod, "-C", proj, "--skip", "command/test", "--exact")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: --exact applies to --only\n"))
		var hint string
		for _, l := range strings.Split(r.err, "\n") {
			if strings.HasPrefix(strings.TrimSpace(l), "hint: lola install ") {
				hint = l
			}
		}
		Expect(hint).To(ContainSubstring("--skip command/test"))
		Expect(hint).NotTo(ContainSubstring("--exact"))
		Expect(selections).NotTo(BeAnExistingFile())
		Expect(runHint(strings.TrimPrefix(strings.TrimSpace(hint), "hint: ")).code).To(Equal(0), "the hint runs as printed")
		Expect(os.ReadFile(selections)).To(ContainSubstring("command/test"), "and saves the skip it kept")
	})

	It("a dry run writes no selections", func() {
		r := lola("install", mod, "-C", proj, "--only", "command/ship", "--exact", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(selections).NotTo(BeAnExistingFile())
	})

	It("saves only:, and names a later component in JSON notices and in sync's output with the scope flag", func() {
		r := lola("install", mod, "-C", proj, "--only", "command/ship", "--exact")
		Expect(r.code).To(Equal(0), r.err)
		b, err := os.ReadFile(selections)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(HaveSuffix("projects:\n  " + proj + ":\n    example:\n      only: [command/ship]\n"))
		Expect(filepath.Join(proj, ".claude/commands/test.md")).NotTo(BeAnExistingFile())

		writeTree(mod, map[string]string{"commands/lint.md": "Lint\n"})
		// -n: the pin stays on v1, so the sync below names both lint and lint2.
		r = lola("install", mod, "-C", proj, "--json", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(withoutContext(r.out)).NotTo(ContainSubstring("null"))
		var v struct {
			Notices []struct{ Level, Text string } `json:"notices"`
		}
		Expect(json.Unmarshal([]byte(r.out), &v)).To(Succeed())
		Expect(v.Notices).To(ContainElement(SatisfyAll(HaveField("Level", "info"),
			HaveField("Text", HavePrefix("example adds command/lint, not installed (only: in selections.yaml projects["+proj+"]) → lola install ")),
			HaveField("Text", HaveSuffix(" example --unskip command/lint")))))
		Expect(filepath.Join(proj, ".claude/commands/lint.md")).NotTo(BeAnExistingFile())

		writeTree(mod, map[string]string{"commands/lint2.md": "Lint 2\n"})
		r = lola("sync", "-C", proj, "--trust")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out + r.err).To(ContainSubstring("→ example adds command/lint, command/lint2, not installed (only: in selections.yaml projects[" + proj + "])"))
		Expect(r.out + r.err).To(ContainSubstring("example --unskip command/lint --unskip command/lint2"))
	})
})
