package cli_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/cli"
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

var _ = Describe("install scope", func() {
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
		r := lola("install", mod)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(Equal(projectContext(proj)), "the context line, once the scope is found")
		r = lola("install", mod, "--json")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(BeEmpty(), "--json says it in the document")
		var doc map[string]any
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed())
		Expect(doc["context"]).To(ConsistOf(HaveKeyWithValue("writes_to",
			map[string]any{"name": "project.yml", "kind": "base", "path": filepath.Join(proj, ".lola", "project.yml")})))
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
		r := lola("install", mod, "--scope", "both", "-C", proj, "-t", "claude-code", "-v")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("project (" + proj + "):\nexample: 1 change (claude-code 1)\n    ✓ create claude-code .claude/commands/ship.md"))
		Expect(r.out).To(ContainSubstring("user:\nexample: 1 change (claude-code 1)\n    ✓ create claude-code .claude/commands/ship.md"))
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

var _ = Describe("selection flags", func() {
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
		Expect(withoutContext(r.out)).NotTo(ContainSubstring("null"))
		Expect(filepath.Join(proj, ".claude/commands/test.md")).NotTo(BeAnExistingFile())

		r = lola("install", "example", "-C", proj, "--json")
		v = parseInstall(r.out)
		Expect(v.Modules[0].Skipped).To(Equal([]string{"command/test"}), "the saved selection holds for the next install")
		Expect(*v.Modules[0].SavedTo).To(BeEmpty())
		Expect(r.err).To(BeEmpty())

		r = lola("install", "example", "-C", proj, "--unskip", "command/test", "-v")
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
		Expect(r.err).To(Equal(projectContext(proj)))
		Expect(selections).NotTo(BeAnExistingFile())
	})

	It("--strict refusal writes nothing", func() {
		writeTree(mod, map[string]string{"agents/bare.md": "No description.\n"})
		r := lola("install", mod, "-C", proj, "--skip", "command/test", "--strict")
		Expect(r.code).To(Equal(1))
		Expect(selections).NotTo(BeAnExistingFile())
	})
})

var _ = Describe("an install's report", func() {
	var tmp, proj, kit string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, kit = filepath.Join(tmp, "proj"), filepath.Join(tmp, "kit")
		writeTree(kit, map[string]string{"lola.yml": "name: kit\n",
			"skills/lint/SKILL.md": "---\nname: lint\ndescription: Lints.\nallowed-tools: Bash\n---\nLint.\n"})
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
	})

	It("says pre-approved tools are new the first time, and folds them under Unchanged since last run when the module is unchanged", func() {
		r := lola("install", kit, "-C", proj, "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("Warnings — new:\n⚠ kit: claude-code skill lint: pre-approves tools Claude Code then runs without asking: Bash\n"))
		Expect(r.out).NotTo(ContainSubstring("Unchanged since last run:"))

		r = lola("install", kit, "-C", proj, "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).NotTo(ContainSubstring("Warnings — new:"))
		Expect(r.out).To(ContainSubstring("Unchanged since last run:\n▸ 1 warning seen before\n"))
		Expect(r.out).NotTo(ContainSubstring("pre-approves tools"), "folded")
		Expect(r.out).To(HaveSuffix("→ ▸ lines are folded; -v prints them in full\n"))

		r = lola("install", kit, "-C", proj, "-t", "claude-code", "-v")
		Expect(r.out).To(ContainSubstring("Unchanged since last run:\n1 warning seen before\n    kit: 1 warning — "))
		Expect(r.out).To(ContainSubstring("pre-approves tools Claude Code then runs without asking: Bash"))
		Expect(r.out).NotTo(ContainSubstring("lines are folded"))
	})

	It("prints only the outcome of a dry run that would change nothing, as the real run words it", func() {
		inside := filepath.Join(proj, "mods", "kit") // a folder inside the project draws no warning
		writeTree(inside, map[string]string{"lola.yml": "name: kit\n",
			"skills/lint/SKILL.md": "---\nname: lint\ndescription: Lints.\nallowed-tools: Bash\n---\nLint.\n"})
		Expect(lola("install", inside, "-C", proj, "-t", "claude-code").code).To(Equal(0))
		r := lola("install", inside, "-C", proj, "-t", "claude-code")
		Expect(r.out).To(ContainSubstring("✓ kit already up to date (claude-code)\n"))
		r = lola("install", inside, "-C", proj, "-t", "claude-code", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(Equal("→ kit already up to date (claude-code)\n"))
		r = lola("sync", "-C", proj, "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(Equal("→ already in sync\n"))

		for _, run := range [][]string{{"install", inside, "-C", proj, "-t", "claude-code", "-n", "-v"}, {"sync", "-C", proj, "-n", "-v"}} {
			r = lola(run...)
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(ContainSubstring("Unchanged since last run:\n1 warning seen before\n"), "-v prints what it would fold")
			Expect(r.out).To(ContainSubstring("pre-approves tools Claude Code then runs without asking: Bash"))
		}
		plain := filepath.Join(proj, "mods", "plain")
		writeTree(plain, map[string]string{"lola.yml": "name: plain\n", "commands/ship.md": "Ship.\n"})
		Expect(lola("install", plain, "-C", proj, "-t", "claude-code").code).To(Equal(0))
		r = lola("install", plain, "-C", proj, "-t", "claude-code", "-n", "-v")
		Expect(r.out).To(Equal("→ plain already up to date (claude-code)\n"), "-v has nothing more to print")

		writeTree(inside, map[string]string{"skills/lint/SKILL.md": "---\nname: lint\ndescription: Lints.\nallowed-tools: Bash\n---\nLint more.\n"})
		r = lola("install", inside, "-C", proj, "-t", "claude-code", "-n")
		Expect(r.out).To(HavePrefix("plan (dry run; nothing was written):\n"), "a changed module is something to do")
	})

	It("says a module --force would leave as it is is already up to date, unless its install hooks run again", func() {
		inside := filepath.Join(proj, "mods", "kit")
		writeTree(inside, map[string]string{"lola.yml": "name: kit\n", "commands/ship.md": "Ship.\n"})
		Expect(lola("install", inside, "-C", proj, "-t", "claude-code").code).To(Equal(0))
		r := lola("install", inside, "-C", proj, "-t", "claude-code", "--force", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(Equal("→ kit already up to date (claude-code)\n"))
		r = lola("install", inside, "-C", proj, "-t", "claude-code", "--force")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("✓ kit already up to date (claude-code)\n"))
		Expect(r.out).NotTo(ContainSubstring("✓ installed kit"))

		hooked := filepath.Join(proj, "mods", "hooked")
		writeTree(hooked, map[string]string{"lola.yml": "name: hooked\nhooks: {post-install: hooks/p.sh}\n", "hooks/p.sh": "#!/bin/sh\nexit 0\n",
			"commands/land.md": "Land.\n"})
		r = lola("install", hooked, "-C", proj, "-t", "claude-code", "--trust=hooks")
		Expect(r.code).To(Equal(0), r.err)
		r = lola("install", hooked, "-C", proj, "-t", "claude-code", "--trust=hooks", "--force", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(MatchRegexp(`post-install hook hooks/p\.sh \(sha256 [0-9a-f]{64}, claude-code\): would run`))
		Expect(r.out).NotTo(ContainSubstring("already up to date"), "its hooks run again in a new copy")
	})

	It("keeps a dry run's warning notices, as the real run prints them", func() {
		Expect(lola("install", kit, "-C", proj, "-t", "claude-code").code).To(Equal(0))
		outside := "⚠ kit comes from " + kit + ", outside the project"
		r := lola("install", kit, "-C", proj, "-t", "claude-code")
		Expect(r.out).To(ContainSubstring(outside))
		r = lola("install", kit, "-C", proj, "-t", "claude-code", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring(outside), "a warning is not something to hide")
		real := lola("install", kit, "-C", proj, "-t", "claude-code").out
		Expect(real).To(ContainSubstring("Unchanged since last run:\n▸ 1 warning seen before\n"))
		Expect(r.out).To(Equal("plan (dry run; nothing was written):\n"+strings.Replace(real, "✓ kit already up to date", "→ kit already up to date", 1)),
			"the dry run's report is the real run's: its seen findings folded and the module up to date")

		writeTree(filepath.Join(proj, "mods", "other"), map[string]string{"lola.yml": "name: other\n", "commands/o.md": "O\n"})
		manifest := filepath.Join(proj, ".lola", "project.yml")
		b, err := os.ReadFile(manifest)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(manifest, append(b, "  - name: other\n    source: mods/other\n"...), 0o644)).To(Succeed())
		r = lola("sync", "kit", "-C", proj, "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("⚠ 1 other module is not in sync: other → lola sync -C " + proj + "\n"))
		Expect(r.out).To(ContainSubstring("→ kit already in sync\n"))
	})

	It("shows a declined runtime hook in full once, with how to consent, then folds it as seen", func() {
		plugin := filepath.Join(proj, "mods", "deploy")
		writeTree(plugin, map[string]string{".claude-plugin/plugin.json": `{"name":"deploy"}`, "commands/ship.md": "Ship\n",
			"hooks/hooks.json": `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo hi"}]}]}}`})
		r := lola("install", plugin, "-C", proj, "-t", "claude-code", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("Runtime hooks — new:\ndeploy: 1 runtime hook for claude-code in .claude/settings.local.json (declined)\n"))
		Expect(r.out).To(ContainSubstring("; echo hi\n    hint: review the hooks listed above, then"))
		Expect(r.out).NotTo(ContainSubstring("runtime hooks skipped"), "the hooks above are what was skipped")

		r = lola("install", plugin, "-C", proj, "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("(declined)\n"))
		r = lola("install", plugin, "-C", proj, "-t", "claude-code")
		Expect(r.out).NotTo(ContainSubstring("Runtime hooks — new:"))
		Expect(r.out).To(ContainSubstring("Unchanged since last run:\n▸ 1 runtime hook seen before\n"), "declined where nothing asked: a terminal run would ask")
		r = lola("install", plugin, "-C", proj, "-t", "claude-code", "-n")
		Expect(r.out).To(Equal("→ deploy already up to date (claude-code)\n"), "declined as before: nothing to do")
		r = lola("sync", "-C", proj, "-n")
		Expect(r.out).To(Equal("→ already in sync\n"))
		r = lola("install", plugin, "-C", proj, "-t", "claude-code", "-n", "--plain")
		Expect(r.out).To(MatchRegexp(`runtime-hook\tdeploy\tclaude-code\tStop\tcommand\tdeclined\t-\tseen\tStop: command; runs: .*; echo hi\n`))

		writeTree(plugin, map[string]string{"hooks/hooks.json": `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo bye"}]}]}}`})
		r = lola("install", plugin, "-C", proj, "-t", "claude-code", "-n")
		Expect(r.out).To(ContainSubstring("Runtime hooks — new:\n"), "a changed hook is new again")
		Expect(lola("install", plugin, "-C", proj, "-t", "claude-code", "--trust=runtime-hooks").code).To(Equal(0))
		r = lola("install", plugin, "-C", proj, "-t", "claude-code", "-n")
		Expect(r.out).To(Equal("→ deploy already up to date (claude-code)\n"), "consented: nothing to do")
	})

	It("hands the pager its folds closed with the footer, and under -v every line", func() {
		paged := filepath.Join(tmp, "paged")
		Expect(os.WriteFile(filepath.Join(tmp, "bin", "fakepager"), []byte("#!/bin/sh\ncat > \"$PAGED\"\n"), 0o755)).To(Succeed())
		GinkgoT().Setenv("PAGED", paged)
		GinkgoT().Setenv("LOLA_PAGER", "fakepager")
		GinkgoT().Setenv("LOLA_COLOR", "0")
		onTerminal := func(args ...string) string {
			root, err := cli.NewRootCmd(os.Getenv, cli.WithPrompter(&scripted{}), cli.WithTerminals(true, true))
			Expect(err).NotTo(HaveOccurred())
			var out, errb bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&errb)
			root.SetIn(strings.NewReader(""))
			root.SetArgs(args)
			Expect(cli.Execute(root)).To(Equal(0), errb.String())
			Expect(out.String()).To(BeEmpty(), "the report went to the pager")
			b, err := os.ReadFile(paged)
			Expect(err).NotTo(HaveOccurred())
			return string(b)
		}
		got := onTerminal("install", kit, "-C", proj, "-t", "claude-code", "-y")
		Expect(got).To(ContainSubstring("▸ kit: 1 change (claude-code 1)\n"))
		Expect(got).NotTo(ContainSubstring(".claude/skills/lint/SKILL.md"))
		Expect(got).To(HaveSuffix("→ ▸ lines are folded; -v prints them in full\n"))

		Expect(lola("uninstall", "kit", "-C", proj, "-y").code).To(Equal(0))
		got = onTerminal("install", kit, "-C", proj, "-t", "claude-code", "-y", "-v")
		Expect(got).To(ContainSubstring("kit: 1 change (claude-code 1)\n    ✓ create claude-code .claude/skills/lint/SKILL.md\n"))
		Expect(got).NotTo(ContainSubstring("▸"))
	})
})
