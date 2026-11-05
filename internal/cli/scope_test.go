package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/cli"
	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("scope for every command (Plan 6b §3.1)", func() {
	var tmp, proj, bare, mod string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, bare, mod = filepath.Join(tmp, "proj"), filepath.Join(tmp, "bare"), filepath.Join(tmp, "example")
		writeTree(mod, map[string]string{"commands/ship.md": "Ship $ARGUMENTS\n"})
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		Expect(os.MkdirAll(bare, 0o755)).To(Succeed())
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
	})

	It("--project selects project scope and -C/--root picks the directory", func() {
		GinkgoT().Chdir(bare)
		v := parseInstall(lola("install", mod, "--project", "-t", "claude-code", "--json").out)
		Expect(v.Scopes).To(HaveLen(1))
		Expect(v.Scopes[0].Scope).To(Equal("project"))
		Expect(v.Scopes[0].Root).To(Equal(bare))
		Expect(v.Scopes[0].ScopeSource).To(Equal("flag"))
		Expect(filepath.Join(bare, ".claude/commands/ship.md")).To(BeARegularFile())

		v = parseInstall(lola("install", mod, "--root", proj, "-t", "claude-code", "--json").out)
		Expect(v.Scopes[0].Root).To(Equal(proj))
		Expect(filepath.Join(proj, ".claude/commands/ship.md")).To(BeARegularFile())

		r := lola("uninstall", "example", "-y", "--project", "--root", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(filepath.Join(proj, ".claude/commands/ship.md")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(bare, ".claude/commands/ship.md")).To(BeARegularFile())
	})

	It("every scoped command resolves its scope as install does", func() {
		home := os.Getenv("HOME")
		inProj, inHome := filepath.Join(proj, ".claude/commands/ship.md"), filepath.Join(home, ".claude/commands/ship.md")
		Expect(lola("install", mod, "--user", "-t", "claude-code").code).To(Equal(0))
		Expect(lola("install", mod, "-C", proj, "-t", "claude-code").code).To(Equal(0))
		sub := filepath.Join(proj, "src")
		Expect(os.MkdirAll(sub, 0o755)).To(Succeed())

		By("on a terminal without a scope flag, the picker asks, offering one scope with nothing preselected")
		GinkgoT().Chdir(sub)
		Expect(os.Remove(inProj)).To(Succeed())
		Expect(os.Remove(inHome)).To(Succeed())
		p := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes(cli.ScopeUser)}}
		r := lolaP(p, "render")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.scopeSteps).To(Equal([]cli.ScopeStep{{Question: "Render where?", ProjectRoot: proj, Home: home, One: true}}))
		Expect(inHome).To(BeARegularFile())
		Expect(inProj).NotTo(BeAnExistingFile())

		p = &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes(cli.ScopeProject, cli.ScopeUser), scopes(cli.ScopeUser)}}
		r = lolaP(p, "render")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.scopeSteps).To(HaveLen(2))
		Expect(p.scopeSteps[1].Problem).To(Equal("pick one scope"))

		p = &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes(cli.ScopeProject, cli.ScopeUser)}}
		r = lolaP(p, "sync")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.scopeSteps).To(Equal([]cli.ScopeStep{{Question: "Sync where?", ProjectRoot: proj, Home: home}}), "sync takes both scopes")
		Expect(r.out).To(ContainSubstring("project (" + proj + "):\n"))
		Expect(r.out).To(ContainSubstring("\nuser:\n"))

		p = &scripted{}
		r = lolaP(p, "render", "--project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(BeEmpty(), "a scope flag answers the picker")
		Expect(inProj).To(BeARegularFile())

		By("off a terminal, the project found walking up")
		Expect(os.Remove(inProj)).To(Succeed())
		r = lola("render")
		Expect(r.code).To(Equal(0), r.err)
		Expect(inProj).To(BeARegularFile())

		By("off a terminal and outside a project, default_scope")
		GinkgoT().Chdir(bare)
		r = lola("render")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("error: lola render needs a scope: no .lola/ project was found in " + bare + " or above it, and " +
			filepath.Join(tmp, "config", "lola", "config.yaml") + " sets no default_scope (default_scope: project or user)\n" +
			"  hint: lola render --project\n  hint: lola render --user\n"))
		for _, h := range []string{"lola render --project", "lola render --user"} {
			again := lola(shellWords(h)[1:]...)
			Expect(again.code).To(Equal(0), h+": "+again.err)
		}
		Expect(lola("sync").err).To(HavePrefix("error: lola sync needs a scope"))
		Expect(os.MkdirAll(filepath.Join(tmp, "config", "lola"), 0o700)).To(Succeed())
		cfg := filepath.Join(tmp, "config", "lola", "config.yaml")
		Expect(os.WriteFile(cfg, []byte("version: 1\ndefault_scope: user\n"), 0o600)).To(Succeed())
		r = lola("uninstall", "example", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(inHome).NotTo(BeAnExistingFile())
		Expect(inProj).To(BeARegularFile())

		Expect(os.WriteFile(cfg, []byte("version: 1\ndefault_scope: both\n"), 0o600)).To(Succeed())
		r = lola("render")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("error: lola render acts on one scope at a time, and default_scope: both in " + cfg + " names two\n" +
			"  hint: name one: --project or --user\n"))

		By("lola profile's actions resolve their scope the same way")
		writeTree(filepath.Join(tmp, "config", "lola", "profiles"), map[string]string{"go.yml": "version: 1\n"})
		active := func(ledgerPath string) string {
			GinkgoHelper()
			b, err := os.ReadFile(ledgerPath)
			if os.IsNotExist(err) {
				return ""
			}
			Expect(err).NotTo(HaveOccurred())
			return string(b)
		}
		projLedger, userLedger := filepath.Join(proj, ".lola", "lock.json"), filepath.Join(tmp, "state", "lola", "lock.json")
		r = lola("profile", "go", "--on")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(HavePrefix("error: lola profile acts on one scope at a time, and default_scope: both in " + cfg + " names two\n"))
		Expect(os.WriteFile(cfg, []byte("version: 1\ndefault_scope: user\n"), 0o600)).To(Succeed())
		r = lola("profile", "go", "--on")
		Expect(r.code).To(Equal(0), r.err)
		Expect(active(userLedger)).To(ContainSubstring(`"user:go"`), "off a terminal and outside a project, default_scope")
		Expect(active(projLedger)).NotTo(ContainSubstring(`"user:go"`))

		GinkgoT().Chdir(sub)
		p = &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes(cli.ScopeProject)}}
		r = lolaP(p, "profile", "go", "--on")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.scopeSteps).To(Equal([]cli.ScopeStep{{Question: "Turn them on where?", ProjectRoot: proj, Home: home, One: true}}))
		Expect(active(projLedger)).To(ContainSubstring(`"user:go"`), "on a terminal, the picker")
		r = lola("profile", "go", "--off", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(active(userLedger)).NotTo(ContainSubstring(`"user:go"`), "a scope flag")
		r = lola("profile", "go", "--off")
		Expect(r.code).To(Equal(0), r.err)
		Expect(active(projLedger)).NotTo(ContainSubstring(`"user:go"`), "off a terminal, the project found walking up")

		Expect(os.Remove(cfg)).To(Succeed())
		GinkgoT().Chdir(bare)
		Expect(lola("profile", "go", "--on").err).To(HavePrefix("error: lola profile needs a scope"))
	})

	It("no hint turns a flag given as false into one given as true, or carries an answer to another question", func() {
		GinkgoT().Chdir(bare)
		hintsOf := func(r result) []string {
			var out []string
			for _, line := range strings.Split(r.err, "\n") {
				if h, ok := strings.CutPrefix(line, "  hint: "); ok {
					out = append(out, h)
				}
			}
			return out
		}
		for _, run := range [][]string{
			{"install", mod, "--yes=false", "--json=false", "--no-rules=false", "--allow-hooks=false"},
			{"sync", "--trust=false", "--yes=false", "--json=false", "--allow-runtime-hooks=false"},
			{"install", mod, "-y", "--allow-hooks", "--allow-runtime-hooks", "-t", "claude-code"}, // answers for this run only
			{"sync", "-y", "--trust", "--allow-hooks"},
		} {
			r := lola(run...)
			Expect(r.code).To(Equal(1), strings.Join(run, " "))
			hints := hintsOf(r)
			Expect(hints).NotTo(BeEmpty(), strings.Join(run, " "))
			for _, h := range hints {
				words := shellWords(h)
				for _, consent := range []string{"--yes", "-y", "--trust", "--allow-hooks", "--allow-runtime-hooks", "--json", "--no-rules"} {
					Expect(words).NotTo(ContainElement(consent), h)
				}
			}
		}
		r := lola("market", "acmee", "--json=false")
		Expect(r.err).To(Equal("error: you have no marketplace acmee\n  hint: lola market\n"), "a false output flag is no output flag")
	})

	It("asks a one-scope picker again when the answer is empty", func() {
		Expect(lola("install", mod, "-C", proj, "-t", "claude-code").code).To(Equal(0))
		GinkgoT().Chdir(proj)
		p := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes(), scopes(cli.ScopeProject)}}
		r := lolaP(p, "render")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.scopeSteps).To(HaveLen(2))
		Expect(p.scopeSteps[1].Problem).To(Equal("pick at least one scope"))
	})

	It("uninstall and render take a project at the home directory an older lola installed; install and sync refuse it", func() {
		home := os.Getenv("HOME")
		Expect(lola("install", mod, "-C", proj, "-t", "claude-code").code).To(Equal(0))
		for _, d := range []string{".lola", ".claude"} {
			Expect(os.CopyFS(filepath.Join(home, d), os.DirFS(filepath.Join(proj, d)))).To(Succeed())
		}
		inHome := filepath.Join(home, ".claude/commands/ship.md")

		By("render, the home directory picked as Other directory…")
		GinkgoT().Chdir(bare)
		p := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes(cli.ScopeOther)}, dirs: []func(cli.DirectoryStep) (string, error){dir(home)}}
		r := lolaP(p, "render")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.dirSteps).To(HaveLen(1))

		By("uninstall, Project picked in the home directory")
		GinkgoT().Chdir(home)
		p = &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes(cli.ScopeProject)}}
		r = lolaP(p, "uninstall", "example", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.scopeSteps).To(HaveLen(1))
		Expect(inHome).NotTo(BeAnExistingFile())
		Expect(filepath.Join(proj, ".claude/commands/ship.md")).To(BeARegularFile())

		for _, args := range [][]string{{"install", mod}, {"sync"}} {
			p = &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes(cli.ScopeProject), func(cli.ScopeStep) ([]string, error) { return nil, cli.ErrAborted }}}
			lolaP(p, args...)
			Expect(p.scopeSteps).To(HaveLen(2), args[0])
			Expect(p.scopeSteps[1].Problem).To(Equal("the project here is your home directory; tick User instead"), args[0])
		}
	})

	It("sync --scope both syncs the project, then user scope, and stops at the first that fails", func() {
		home := os.Getenv("HOME")
		inProj, inHome := filepath.Join(proj, ".claude/commands/ship.md"), filepath.Join(home, ".claude/commands/ship.md")
		Expect(lola("install", mod, "--scope", "both", "-C", proj, "-t", "claude-code").code).To(Equal(0))
		Expect(os.Remove(inProj)).To(Succeed())
		Expect(os.Remove(inHome)).To(Succeed())

		r := lola("sync", "--scope", "both", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(MatchRegexp(`(?s)^project \(` + regexp.QuoteMeta(proj) + `\):\n.*\nuser:\n`))
		Expect(inProj).To(BeARegularFile())
		Expect(inHome).To(BeARegularFile())

		var doc struct {
			Scopes []struct{ Scope, Root string } `json:"scopes"`
		}
		r = lola("sync", "--scope", "both", "-C", proj, "--json")
		Expect(r.code).To(Equal(0), r.err)
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed())
		Expect(doc.Scopes).To(HaveLen(2))
		Expect(doc.Scopes[0].Scope).To(Equal("project"))
		Expect(doc.Scopes[0].Root).To(Equal(proj))
		Expect(doc.Scopes[1].Scope).To(Equal("user"))

		By("a failing project scope stops before user scope")
		Expect(os.Remove(inHome)).To(Succeed())
		r = lola("sync", "--scope", "both", "-C", bare)
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(HavePrefix("error: no project.yml in " + bare + "; user scope was not synced\n"))
		Expect(inHome).NotTo(BeAnExistingFile())

		r = lola("sync", "--scope", "both", "-C", bare, "--json")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring(`"scopes":[]`), "the error document always carries the synced scopes")
		Expect(r.err).NotTo(ContainSubstring("not synced"), "JSON names the synced scopes in scopes")

		By("a declined or refused project scope stops before user scope")
		manPath := filepath.Join(proj, ".lola", "project.yml")
		was, err := os.ReadFile(manPath)
		Expect(err).NotTo(HaveOccurred())
		writeTree(filepath.Join(proj, "mods", "kit"), map[string]string{"lola.yml": "name: kit\n", "commands/kit.md": "Kit\n"})
		Expect(os.WriteFile(manPath, append(slices.Clone(was), "  - name: kit\n    source: mods/kit\n"...), 0o644)).To(Succeed())
		c := &confirming{trusts: []engine.TrustAnswer{{}}}
		r = lolaAsking(c, "sync", "--scope", "both", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(c.questions).To(Equal([]string{"trust"}), "user scope was never reached")
		Expect(r.err).To(HaveSuffix("aborted; the project was not changed; user scope was not synced.\n"))
		Expect(inHome).NotTo(BeAnExistingFile())
		r = lola("sync", "--scope", "both", "-C", proj)
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: lola sync needs your trust"))
		Expect(r.err).To(ContainSubstring("; user scope was not synced\n"))
		Expect(r.out).NotTo(ContainSubstring("user:"))
		Expect(inHome).NotTo(BeAnExistingFile())
		Expect(os.WriteFile(manPath, was, 0o644)).To(Succeed())

		By("a failing user scope keeps the project's result")
		Expect(os.Remove(filepath.Join(tmp, "config", "lola", "user.yml"))).To(Succeed())
		r = lola("sync", "--scope", "both", "-C", proj, "--json")
		Expect(r.code).To(Equal(1))
		var failed struct {
			Error  string                   `json:"error"`
			Scopes []struct{ Scope string } `json:"scopes"`
		}
		Expect(json.Unmarshal([]byte(r.err), &failed)).To(Succeed(), r.err)
		Expect(failed.Error).To(HavePrefix("no user.yml at "))
		Expect(failed.Scopes).To(HaveLen(1))
		Expect(failed.Scopes[0].Scope).To(Equal("project"))
	})

	It("a declined sync names the scope it left unchanged", func() {
		Expect(lola("install", mod, "--user", "-t", "claude-code").code).To(Equal(0))
		Expect(os.WriteFile(filepath.Join(tmp, "config", "lola", "user.yml"), []byte("version: 1\nmodules: []\n"), 0o600)).To(Succeed())
		c := &confirming{answers: []bool{false}}
		r := lolaAsking(c, "sync", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(HaveSuffix("removals declined; user scope was not changed.\n"))
	})

	It("read-only commands show user scope and the found project", func() {
		Expect(lola("install", mod, "--user", "-t", "claude-code").code).To(Equal(0))
		Expect(lola("install", mod, "-C", proj, "-t", "claude-code").code).To(Equal(0))
		sub := filepath.Join(proj, "src")
		Expect(os.MkdirAll(sub, 0o755)).To(Succeed())
		cfg := filepath.Join(tmp, "config", "lola", "config.yaml")
		Expect(os.WriteFile(cfg, []byte("version: 1\ndefault_scope: project\n"), 0o600)).To(Succeed())
		scopesOf := func(r result) []string {
			GinkgoHelper()
			Expect(r.code).To(Equal(0), r.err)
			var doc struct {
				Modules []struct{ Scope, Name string } `json:"modules"`
			}
			Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed())
			out := []string{}
			for _, m := range doc.Modules {
				out = append(out, m.Scope+":"+m.Name)
			}
			return out
		}

		GinkgoT().Chdir(sub)
		Expect(scopesOf(lola("list", "--json"))).To(Equal([]string{"project:example", "user:example"}))
		r := lola("list")
		Expect(r.out).To(MatchRegexp(`SCOPE\s+MODULE\s+TARGET`))
		Expect(r.out).To(MatchRegexp(`\nproject\s+example\s+claude-code\s+✓ ok`))
		Expect(r.out).To(MatchRegexp(`\nuser\s+example\s+claude-code\s+✓ ok`))
		p := &scripted{}
		r = lolaP(p, "list", "--plain")
		Expect(r.out).To(Equal("example\tclaude-code\tok\ton\t" + mod + "\tproject\tproject.yml\nexample\tclaude-code\tok\ton\t" + mod + "\tuser\tuser.yml\n"))
		Expect(p.calls).To(BeEmpty(), "list never asks for a scope on a terminal")

		By("the flags decide, --scope both included")
		Expect(scopesOf(lola("list", "--project", "--json"))).To(Equal([]string{"project:example"}))
		Expect(scopesOf(lola("list", "--user", "--json"))).To(Equal([]string{"user:example"}))
		GinkgoT().Chdir(bare)
		Expect(scopesOf(lola("list", "--scope", "both", "-C", proj, "--json"))).To(Equal([]string{"project:example", "user:example"}))

		By("outside a project, user scope only: default_scope does not apply")
		Expect(scopesOf(lola("list", "--json"))).To(Equal([]string{"user:example"}))
		Expect(lola("uninstall", "example", "--user", "-y").code).To(Equal(0))
		Expect(lola("list").out).To(Equal("nothing installed at user scope (try: lola install --user <source>)\n"))
		GinkgoT().Chdir(sub)
		Expect(lola("list").out).To(HaveSuffix("\nnothing installed at user scope (try: lola install --user <source>)\n"))

		By("lola profile reads the same scopes and never asks")
		writeTree(filepath.Join(tmp, "config", "lola", "profiles"), map[string]string{"go.yml": "version: 1\n"})
		profiles := func(r result) []string {
			GinkgoHelper()
			Expect(r.code).To(Equal(0), r.err)
			var doc struct {
				Profiles []struct{ Scope, Name string } `json:"profiles"`
			}
			Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed())
			out := []string{}
			for _, m := range doc.Profiles {
				out = append(out, m.Scope+":"+m.Name)
			}
			return out
		}
		Expect(profiles(lola("profile", "--json"))).To(Equal([]string{"project:go", "user:go"}))
		Expect(profiles(lola("profile", "go", "--json"))).To(Equal([]string{"project:go", "user:go"}))
		Expect(profiles(lola("profile", "--user", "--json"))).To(Equal([]string{"user:go"}))
		p = &scripted{}
		r = lolaP(p, "profile")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(BeEmpty(), "lola profile never asks for a scope on a terminal")
		GinkgoT().Chdir(bare)
		Expect(profiles(lola("profile", "--json"))).To(Equal([]string{"user:go"}))
	})

	It("list without a scope flag names the other scope's list when one scope fails", func() {
		Expect(lola("install", mod, "-C", proj, "-t", "claude-code").code).To(Equal(0))
		Expect(os.MkdirAll(filepath.Join(tmp, "config", "lola"), 0o700)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(tmp, "config", "lola", "user.yml"), []byte("modules: [\n"), 0o600)).To(Succeed())
		GinkgoT().Chdir(proj)
		r := lola("list")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("  hint: list the other scope: lola list --project\n"))

		r = lola("list", "--scope", "both")
		Expect(r.code).To(Equal(1))
		Expect(r.err).NotTo(ContainSubstring("list the other scope"), "the flags named both scopes")
	})

	DescribeTable("refuses scope flags that disagree",
		func(args []string, msg, hint string) {
			r := lola(append([]string{"uninstall", "example", "-y"}, args...)...)
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(ContainSubstring("error: " + msg + "\n  hint: " + hint))
		},
		Entry("--user --project", []string{"--user", "--project"}, "--user and --project disagree", "name one scope: --project or --user"),
	)
})
