package cli_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// installJSON is the part of lola install --json the install flow specs read.
type installJSON struct {
	Scopes []struct {
		Scope       string `json:"scope"`
		Root        string `json:"root"`
		ScopeSource string `json:"scope_source"`
	} `json:"scopes"`
	Modules []struct {
		Name    string   `json:"name"`
		Scope   string   `json:"scope"`
		Skipped []string `json:"skipped"`
		SavedTo *string  `json:"saved_to"`
	} `json:"modules"`
	EquivalentCommands []string `json:"equivalent_commands"`
}

func parseInstall(out string) installJSON {
	var v installJSON
	Expect(json.Unmarshal([]byte(out), &v)).To(Succeed(), out)
	return v
}

var _ = Describe("install scope (Plan 5c §5.1)", func() {
	var tmp, proj, bare, mod, home string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, bare, mod = filepath.Join(tmp, "proj"), filepath.Join(tmp, "bare"), filepath.Join(tmp, "example")
		home = os.Getenv("HOME")
		writeTree(mod, map[string]string{"commands/ship.md": "Ship $ARGUMENTS\n"})
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		Expect(os.MkdirAll(bare, 0o755)).To(Succeed())
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
	})

	It("refuses to guess a scope off a terminal where no .lola/ project is found, and writes nothing (breaking change)", func() {
		GinkgoT().Chdir(bare)
		r := lola("install", mod, "-t", "claude-code")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(HavePrefix("error: lola install needs a scope: no .lola/ project was found in " + bare + " or above it, and " +
			filepath.Join(tmp, "config", "lola", "config.yaml") + " sets no default_scope (default_scope: project, user or both)\n"))
		Expect(filepath.Join(bare, ".lola")).NotTo(BeADirectory())
		Expect(filepath.Join(bare, ".claude")).NotTo(BeADirectory())

		r = lola("install", mod, "-t", "claude-code", "--strict", "--no-pager")
		run := "lola install " + mod + " --no-pager --strict --target claude-code"
		Expect(r.err).To(HaveSuffix("\n  hint: "+run+" --project\n  hint: "+run+" --user\n  hint: "+run+" --scope both\n"),
			"one hint per scope, each the run with its source and every flag")
		for _, h := range []string{run + " --project", run + " --user", run + " --scope both"} {
			again := lola(shellWords(h)[1:]...)
			Expect(again.code).To(Equal(0), h+": "+again.err)
		}
		Expect(filepath.Join(bare, ".claude", "commands", "ship.md")).To(BeARegularFile())
	})

	It("uses a project found walking up, says so on stderr, and reports it as discovered", func() {
		Expect(lola("install", mod, "-C", proj).code).To(Equal(0))
		sub := filepath.Join(proj, "src")
		Expect(os.MkdirAll(sub, 0o755)).To(Succeed())
		GinkgoT().Chdir(sub)
		r := lola("install", mod, "--json")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(Equal("scope: project (found .lola/ at " + proj + ")\n"))
		v := parseInstall(r.out)
		Expect(v.Scopes).To(HaveLen(1))
		Expect(v.Scopes[0].Scope).To(Equal("project"))
		Expect(v.Scopes[0].Root).To(Equal(proj))
		Expect(v.Scopes[0].ScopeSource).To(Equal("discovered"))
	})

	It("uses default_scope from config.yaml", func() {
		Expect(os.MkdirAll(filepath.Join(tmp, "config", "lola"), 0o700)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(tmp, "config", "lola", "config.yaml"), []byte("version: 1\ndefault_scope: user\n"), 0o600)).To(Succeed())
		GinkgoT().Chdir(bare)
		r := lola("install", mod, "-t", "claude-code", "--json")
		Expect(r.code).To(Equal(0), r.err)
		Expect(parseInstall(r.out).Scopes[0].ScopeSource).To(Equal("config"))
		Expect(filepath.Join(home, ".claude/commands/ship.md")).To(BeARegularFile())
	})

	It("--scope both installs into the project and for the user in one run", func() {
		r := lola("install", mod, "--scope", "both", "-C", proj, "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("project (" + proj + "):\n  ✓ create claude-code .claude/commands/ship.md"))
		Expect(r.out).To(ContainSubstring("user:\n  ✓ create claude-code .claude/commands/ship.md"))
		Expect(r.out).To(ContainSubstring("✓ installed example at project scope (claude-code)"))
		Expect(r.out).To(ContainSubstring("✓ installed example at user scope (claude-code)"))
		Expect(filepath.Join(proj, ".claude/commands/ship.md")).To(BeARegularFile())
		Expect(filepath.Join(home, ".claude/commands/ship.md")).To(BeARegularFile())

		v := parseInstall(lola("install", mod, "--scope", "project,user", "-C", proj, "--json").out)
		Expect(v.Scopes).To(HaveLen(2))
		Expect(v.Scopes[1].Scope).To(Equal("user"))
		Expect(v.Scopes[1].ScopeSource).To(Equal("flag"))
		Expect(v.Modules[0].Scope).To(Equal("project"))
		Expect(v.Modules[1].Scope).To(Equal("user"))
	})

	It("prints no raw control characters from the project directory in the --scope both header", func() {
		evil := filepath.Join(tmp, "evil\x1b[31mproj")
		Expect(os.MkdirAll(evil, 0o755)).To(Succeed())
		r := lola("install", mod, "--scope", "both", "-C", evil, "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("project ("))
		Expect(r.out).NotTo(ContainSubstring("\x1b"))
	})

	It("refuses --scope that conflicts with --user or -C", func() {
		r := lola("install", mod, "--scope", "user", "-C", proj)
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: --scope user cannot take -C: user scope has no project directory\n  hint: drop -C"))
		r = lola("install", mod, "--scope", "project", "--user")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: --user and --scope project disagree"))
	})

	It("refuses a project at the home directory off a terminal: found there, -C ~, or --scope both from ~", func() {
		Expect(os.MkdirAll(filepath.Join(home, ".lola"), 0o755)).To(Succeed())
		GinkgoT().Chdir(home)
		for _, args := range [][]string{{"install", mod, "-t", "claude-code"}, {"-C", home, "install", mod, "-t", "claude-code"},
			{"install", mod, "--scope", "both", "-t", "claude-code"}} {
			r := lola(args...)
			Expect(r.code).To(Equal(1), fmt.Sprint(args))
			Expect(r.err).To(ContainSubstring("error: project scope at " + home + " is your home directory, where the user scope installs"))
			Expect(r.err).To(HaveSuffix("hint: lola install "+mod+" --target claude-code --user\n"), "the run, every flag kept, at user scope")
			Expect(filepath.Join(home, ".claude")).NotTo(BeADirectory())
			Expect(filepath.Join(home, ".lola", "lock.json")).NotTo(BeAnExistingFile())
		}
		r := lola("install", mod, "--target", "claude-code", "--user")
		Expect(r.code).To(Equal(0), "the hint runs: "+r.err)
	})
})

var _ = Describe("selection flags (Plan 5c §5.2)", func() {
	var tmp, proj, mod, selections string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, mod = filepath.Join(tmp, "proj"), filepath.Join(tmp, "example")
		writeTree(mod, map[string]string{"commands/ship.md": "Ship\n", "commands/test.md": "Test\n", "skills/review/SKILL.md": "---\ndescription: Review\n---\nR\n"})
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
		selections = filepath.Join(tmp, "config", "lola", "selections.yaml")
	})

	It("--skip saves to selections.yaml, says so on stderr, and reports skipped and saved_to in JSON", func() {
		r := lola("install", mod, "-C", proj, "--skip", "command/test", "--json")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(Equal("→ saved to " + selections + "\n"))
		v := parseInstall(r.out)
		Expect(v.Modules[0].Skipped).To(Equal([]string{"command/test"}))
		Expect(*v.Modules[0].SavedTo).To(Equal(selections))
		Expect(v.EquivalentCommands).To(Equal([]string{}), "no picker ran")
		Expect(r.out).NotTo(ContainSubstring("null"))
		Expect(filepath.Join(proj, ".claude/commands/test.md")).NotTo(BeAnExistingFile())

		r = lola("install", "example", "-C", proj, "--json")
		v = parseInstall(r.out)
		Expect(v.Modules[0].Skipped).To(Equal([]string{"command/test"}), "the saved selection holds for the next install")
		Expect(*v.Modules[0].SavedTo).To(BeEmpty())
		Expect(r.err).To(BeEmpty())

		r = lola("install", "example", "-C", proj, "--unskip", "command/test")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("✓ create claude-code .claude/commands/test.md"))
	})

	It("coaches a bad selector and one that matches nothing, module-qualified", func() {
		r := lola("install", mod, "-C", proj, "--skip", "skills/review")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring(`error: --skip: selector "skills/review" has an unknown kind "skills"`))
		Expect(r.err).To(ContainSubstring("hint: kinds: skill, command, agent, mcp, rule"))

		r = lola("install", mod, "-C", proj, "--only", "command/deploy")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: --only command/deploy matches no component of the modules being installed\n"))
		Expect(r.err).To(ContainSubstring("  hint: commands here: example:command/ship, example:command/test\n"))
		Expect(selections).NotTo(BeAnExistingFile())
	})

	It("refuses component flags with both scopes unless --global, and --global alone", func() {
		r := lola("install", mod, "-C", proj, "--scope", "both", "-t", "claude-code", "--skip", "command/test")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: --skip command/test with more than one scope does not say which scope's selections it changes"))
		Expect(r.err).To(ContainSubstring("hint: add --global to change the selections of every scope"))

		r = lola("install", mod, "-C", proj, "--global")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: --global saves --skip, --only and --unskip for every scope, and none was given"))

		r = lola("install", mod, "-C", proj, "--scope", "both", "-t", "claude-code", "--skip", "command/test", "--global")
		Expect(r.code).To(Equal(0), r.err)
		b, err := os.ReadFile(selections)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(HaveSuffix("version: 1\nglobal:\n  example:\n    skip: [command/test]\n"))
	})

	It("--dry-run writes neither the files nor selections.yaml", func() {
		r := lola("install", mod, "-C", proj, "--skip", "command/test", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).NotTo(ContainSubstring("commands/test.md"))
		Expect(r.err).To(BeEmpty())
		Expect(selections).NotTo(BeAnExistingFile())
	})

	It("--strict refusal writes nothing", func() {
		writeTree(mod, map[string]string{"agents/bare.md": "No description.\n"})
		r := lola("install", mod, "-C", proj, "--skip", "command/test", "--strict")
		Expect(r.code).To(Equal(1))
		Expect(selections).NotTo(BeAnExistingFile())
	})
})
