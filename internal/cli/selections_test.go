package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("selections.yaml in list, check and render", func() {
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
		r := lola("render", "-C", proj)
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

		Expect(os.WriteFile(selections, []byte("version: 1\nuser:\n  example:\n    skip: [command/test]\n"), 0o600)).To(Succeed())
		r = lola("check", mod, "-t", "claude-code", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("→ example: skipped in selections.yaml: command/test"))
		r = lola("check", mod, "-t", "claude-code", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).NotTo(ContainSubstring("skipped in selections.yaml"), "user scope's selections are not the project's")
	})

	It("reports an invalid selections.yaml with its key path and a hint", func() {
		Expect(os.WriteFile(selections, []byte("version: 1\nuser: [x]\n"), 0o600)).To(Succeed())
		r := lola("ls", "-C", proj)
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: " + selections + ":2: user: must be a mapping of module names, not a list"))
		Expect(r.err).To(ContainSubstring("hint: write module-name: followed by skip: and include: lists"))
	})
})
