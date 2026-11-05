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
		Expect(r.out).To(ContainSubstring("→ delete claude-code .claude/commands/test.md"))
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
		Expect(lola("ls", "-C", proj, "--plain").out).To(Equal("example\tclaude-code\tok\ton\t"+mod+"\tproject\tproject.yml\t\n"), "--plain rows keep their columns, scope and from last")
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

var _ = Describe("Pick-P2-b: a pick change that deletes files is confirmed as uninstall is", func() {
	var tmp, proj, mod, selections, shipped, tested string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, mod = filepath.Join(tmp, "proj"), filepath.Join(tmp, "example")
		writeTree(mod, map[string]string{"commands/ship.md": "Ship $ARGUMENTS\n", "commands/test.md": "Test\n"})
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
		selections = filepath.Join(tmp, "config", "lola", "selections.yaml")
		shipped, tested = filepath.Join(proj, ".claude/commands/ship.md"), filepath.Join(proj, ".claude/commands/test.md")
		r := lola("install", mod, "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(tested).To(BeAnExistingFile())
	})

	It("refuses --skip of an installed component off a terminal, printing the plan and hinting the run with -y", func() {
		r := lola("install", "example", "-C", proj, "--skip", "command/test")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: lola install needs your confirmation to remove files, and there is no terminal to ask on; nothing was written\n"))
		Expect(r.err).To(ContainSubstring("removals:\n"))
		Expect(r.err).To(ContainSubstring("(dropped by your pick change)"))
		Expect(r.err).NotTo(ContainSubstring("no longer in its targets"))
		Expect(r.err).To(ContainSubstring(".claude/commands/test.md"))
		Expect(r.err).NotTo(ContainSubstring("commands/ship.md"), "the plan lists only what goes")
		Expect(tested).To(BeAnExistingFile())
		Expect(selections).NotTo(BeAnExistingFile(), "a refusal saves no picks")
		var hint string
		for _, l := range strings.Split(r.err, "\n") {
			if strings.HasPrefix(strings.TrimSpace(l), "hint: lola install ") {
				hint = strings.TrimPrefix(strings.TrimSpace(l), "hint: ")
			}
		}
		Expect(hint).To(SatisfyAll(ContainSubstring("--skip command/test"), HaveSuffix(" -y")))
		Expect(runHint(hint).code).To(Equal(0), "the hint runs as printed")
		Expect(tested).NotTo(BeAnExistingFile())
		Expect(shipped).To(BeAnExistingFile())
	})

	It("deletes with -y", func() {
		r := lola("install", "example", "-C", proj, "--skip", "command/test", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(tested).NotTo(BeAnExistingFile())
	})

	It("answers the question with --force, without asking", func() {
		r := lola("install", "example", "-C", proj, "--skip", "command/test", "--force")
		Expect(r.code).To(Equal(0), r.err)
		Expect(tested).NotTo(BeAnExistingFile())
	})

	It("never refuses -n and writes nothing", func() {
		r := lola("install", "example", "-C", proj, "--skip", "command/test", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(tested).To(BeAnExistingFile())
		Expect(selections).NotTo(BeAnExistingFile())
	})

	It("carries the plan in the refusal under --json", func() {
		r := lola("install", "example", "-C", proj, "--skip", "command/test", "--json")
		Expect(r.code).To(Equal(1))
		var v map[string]any
		Expect(json.Unmarshal([]byte(r.err), &v)).To(Succeed(), r.err)
		Expect(v).To(HaveKey("error"))
		Expect(v).To(HaveKey("hints"))
		Expect(v["steps"]).To(ContainElement(HaveKeyWithValue("action", "delete")))
		Expect(tested).To(BeAnExistingFile())
	})

	It("applies a widening change without -y", func() {
		Expect(lola("install", "example", "-C", proj, "--skip", "command/test", "-y").code).To(Equal(0))
		Expect(tested).NotTo(BeAnExistingFile())
		r := lola("install", "example", "-C", proj, "--unskip", "command/test")
		Expect(r.code).To(Equal(0), r.err)
		Expect(tested).To(BeAnExistingFile())
	})
})

var _ = Describe("Target-narrowing: install -t of fewer assistants removes the dropped ones' files now, after asking", func() {
	var tmp, proj, mod, claudeFile, geminiFile, projectYML string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, mod = filepath.Join(tmp, "proj"), filepath.Join(tmp, "m")
		writeTree(mod, map[string]string{"lola.yml": "name: m\n", "commands/hi.md": "Hi\n"})
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		claudeFile, geminiFile = filepath.Join(proj, ".claude/commands/hi.md"), filepath.Join(proj, ".gemini/commands/hi.toml")
		projectYML = filepath.Join(proj, ".lola", "project.yml")
		r := lola("install", mod, "-C", proj, "-t", "claude-code,gemini-cli")
		Expect(r.code).To(Equal(0), r.err)
		Expect(geminiFile).To(BeAnExistingFile())
	})
	agrees := func() {
		GinkgoHelper()
		r := lola("list", "-C", proj, "--plain")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("\tclaude-code\t"))
		Expect(r.out).NotTo(ContainSubstring("gemini-cli"), "lola list shows what is installed")
		r = lola("sync", "-C", proj, "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(Equal("→ already in sync\n"), "the next sync changes nothing")
	}

	It("Target-narrowing: off a terminal it refuses without -y, listing the dropped target's files, and writes nothing", func() {
		before, err := os.ReadFile(projectYML)
		Expect(err).NotTo(HaveOccurred())
		r := lola("install", "m", "-C", proj, "-t", "claude-code")
		Expect(r.code).To(Equal(1), r.out)
		Expect(r.err).To(ContainSubstring("removals:\n  ✗ m: gemini-cli (target dropped by -t)\n      .gemini/commands/hi.toml\n"))
		Expect(r.err).To(ContainSubstring("error: lola install needs your confirmation to remove files, and there is no terminal to ask on; nothing was written\n"))
		hints := hintLines(r.err)
		Expect(hints).To(HaveLen(1))
		Expect(hints[0]).To(SatisfyAll(HavePrefix("lola install m "), ContainSubstring("claude-code"), HaveSuffix(" -y")))
		Expect(geminiFile).To(BeAnExistingFile())
		Expect(os.ReadFile(projectYML)).To(Equal(before))

		r = lola("install", "m", "-C", proj, "-t", "claude-code", "--plain")
		Expect(r.code).To(Equal(1), r.out)
		Expect(r.err).To(ContainSubstring("remove\tm\tgemini-cli\t.gemini/commands/hi.toml\tproject\n"), "one scope has the scope column too")

		r = runHint(hints[0])
		Expect(r.code).To(Equal(0), r.err)
		Expect(geminiFile).NotTo(BeAnExistingFile())
		Expect(claudeFile).To(BeAnExistingFile())
		Expect(os.ReadFile(projectYML)).To(ContainSubstring("targets: [claude-code]"))
		agrees()
	})

	It("Target-narrowing: -n shows the removals and writes nothing; -y and --force remove them", func() {
		r := lola("install", "m", "-C", proj, "-t", "claude-code", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out + r.err).To(ContainSubstring("→ delete gemini-cli  .gemini/commands/hi.toml"))
		Expect(geminiFile).To(BeAnExistingFile())

		r = lola("install", "m", "-C", proj, "-t", "claude-code", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(geminiFile).NotTo(BeAnExistingFile())
		agrees()

		Expect(lola("install", "m", "-C", proj, "-t", "claude-code,gemini-cli").code).To(Equal(0), "widening asks nothing")
		Expect(geminiFile).To(BeAnExistingFile())
		r = lola("install", "m", "-C", proj, "-t", "claude-code", "--force")
		Expect(r.code).To(Equal(0), r.err)
		Expect(geminiFile).NotTo(BeAnExistingFile())
	})

	It("Target-narrowing: on a terminal it lists the removals and asks; No writes nothing, yes removes them", func() {
		sp := &scripted{confirm: []bool{false}}
		r := lolaP(sp, "install", "m", "-C", proj, "-t", "claude-code")
		Expect(r.err).To(ContainSubstring("removals:\n  ✗ m: gemini-cli (target dropped by -t)\n      .gemini/commands/hi.toml\n"))
		Expect(sp.calls).To(ContainElement(HavePrefix("confirm:Remove these and apply?")))
		Expect(geminiFile).To(BeAnExistingFile())
		Expect(os.ReadFile(projectYML)).NotTo(ContainSubstring("targets: [claude-code]\n"))

		sp = &scripted{confirm: []bool{true}}
		r = lolaP(sp, "install", "m", "-C", proj, "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(geminiFile).NotTo(BeAnExistingFile())
		agrees()
	})

	It("Target-narrowing: under --scope both each scope's removals name the scope and where it writes", func() {
		GinkgoT().Chdir(proj)
		Expect(lola("install", "m", "--user", "-t", "claude-code,gemini-cli").code).To(Equal(0))
		home := filepath.Join(tmp, "home")
		r := lola("install", "m", "--scope", "both", "-t", "claude-code")
		Expect(r.code).To(Equal(1), r.out)
		Expect(r.err).To(ContainSubstring("  ✗ m: gemini-cli (target dropped by -t) — project " + proj + "\n      .gemini/commands/hi.toml\n"))
		Expect(r.err).To(ContainSubstring("  ✗ m: gemini-cli (target dropped by -t) — user scope (" + home + ")\n      .gemini/commands/hi.toml\n"))
		r = lola("install", "m", "--scope", "both", "-t", "claude-code", "--plain")
		Expect(r.code).To(Equal(1), r.out)
		Expect(r.err).To(ContainSubstring("remove\tm\tgemini-cli\t.gemini/commands/hi.toml\tproject\n"))
		Expect(r.err).To(ContainSubstring("remove\tm\tgemini-cli\t.gemini/commands/hi.toml\tuser\n"))
	})

	It("Target-narrowing: install --help says a narrower -t removes the others' files, after asking", func() {
		r := lola("install", "--help")
		Expect(r.code).To(Equal(0), r.err)
		Expect(strings.Join(strings.Fields(r.out), " ")).To(ContainSubstring("naming fewer than a module is installed on removes the others' files, after asking"))
	})
})

var _ = Describe("install -t adding agents to a module installed on gemini-cli", func() {
	It("lists the gemini-cli skill copies .agents/skills now delivers, refuses off a terminal without -y, and -y removes them", func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, mod := filepath.Join(tmp, "proj"), filepath.Join(tmp, "m")
		writeTree(mod, map[string]string{"lola.yml": "name: m\n", "skills/review/SKILL.md": "---\ndescription: Review code\n---\nReview it.\n"})
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		native := filepath.Join(proj, ".gemini/skills/review/SKILL.md")
		r := lola("install", mod, "-C", proj, "-t", "gemini-cli")
		Expect(r.code).To(Equal(0), r.err)
		Expect(native).To(BeAnExistingFile())

		r = lola("install", "m", "-C", proj, "-t", "gemini-cli,agents")
		Expect(r.code).To(Equal(1), r.out)
		Expect(r.err).To(ContainSubstring("removals:\n  ✗ m: gemini-cli (delivered through .agents/skills)\n      .gemini/skills/review/SKILL.md\n"))
		Expect(r.err).To(ContainSubstring("error: lola install needs your confirmation to remove files"))
		hints := hintLines(r.err)
		Expect(hints).To(HaveLen(1))
		Expect(hints[0]).To(HaveSuffix(" -y"))
		Expect(native).To(BeAnExistingFile())

		r = runHint(hints[0])
		Expect(r.code).To(Equal(0), r.err)
		Expect(native).NotTo(BeAnExistingFile())
		Expect(filepath.Join(proj, ".agents/skills/review/SKILL.md")).To(BeAnExistingFile())
		r = lola("check", "m", "-C", proj, "--plain")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("delivered through .agents/skills, which gemini-cli reads"), "lola check agrees")
	})
})
