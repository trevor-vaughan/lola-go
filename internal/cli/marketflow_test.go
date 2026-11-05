package cli_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/cli"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("installing from marketplaces on and off a terminal (Plan 6c §3.3, §6.5)", func() {
	var tmp, proj, acme string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj = filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		acme = filepath.Join(tmp, "acme")
		writeTree(acme, map[string]string{".claude-plugin/marketplace.json": `{"name": "acme", "owner": {"name": "Acme"}, "description": "Acme tools",
  "plugins": [{"name": "formatter", "source": "./formatter", "version": "1.0.0", "description": "Formats code", "tags": ["style"]},
    {"name": "linter", "source": "./linter", "description": "Lints code"}]}`,
			"formatter/commands/fmt.md": "Fmt\n", "linter/commands/lint.md": "Lint\n"})
		other := filepath.Join(tmp, "other")
		writeTree(other, map[string]string{".claude-plugin/marketplace.json": `{"name": "other", "owner": {"name": "O"},
  "plugins": [{"name": "formatter", "source": "./f", "description": "Another formatter"}]}`, "f/commands/other.md": "Other\n"})
		Expect(lola("market", "--add", acme).code).To(Equal(0))
		Expect(lola("market", "--add", other).code).To(Equal(0))
		GinkgoT().Chdir(proj)
	})
	manifest := func() string {
		GinkgoHelper()
		b, err := os.ReadFile(filepath.Join(proj, ".lola", "project.yml"))
		Expect(err).NotTo(HaveOccurred())
		return string(b)
	}

	It("a bare name listed only by marketplaces asks on a terminal and refuses with @ hints off one", func() {
		r := lola("install", "formatter", "--scope", "project", "-t", "claude-code", "--ref", "main")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("error: formatter is not in your cache, and your marketplaces list it: @acme/formatter, @other/formatter; " +
			"name the one to install\n  hint: lola install @acme/formatter --ref main --scope project --target claude-code\n" +
			"  hint: lola install @other/formatter --ref main --scope project --target claude-code\n"))
		r = lola("install", "formatter", "--scope", "project", "-t", "claude-code", "--json")
		Expect(r.code).To(Equal(1))
		var refusal struct {
			Hints []string `json:"hints"`
		}
		Expect(json.Unmarshal([]byte(r.err), &refusal)).To(Succeed(), r.err)
		Expect(refusal.Hints).To(Equal([]string{"lola install @acme/formatter --json --scope project --target claude-code",
			"lola install @other/formatter --json --scope project --target claude-code"}))

		p := &scripted{entries: []string{"@other/formatter"}}
		r = lolaP(p, "install", "formatter", "--scope", "project", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"market-entry"}))
		Expect(p.entrySteps[0]).To(Equal(cli.MarketEntryStep{Name: "formatter", Options: []cli.ModuleOption{
			{Name: "@acme/formatter", Label: "@acme/formatter 1.0.0 — Formats code [style]"},
			{Name: "@other/formatter", Label: "@other/formatter — Another formatter"}}}))
		Expect(filepath.Join(proj, ".claude", "commands", "other.md")).To(BeARegularFile())
		Expect(manifest()).To(ContainSubstring("    market:\n      name: other\n"))

		r = lola("install", "formatter", "--scope", "project", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err+": a name your cache holds is that module, never a marketplace's")
	})

	It("refuses bare names with too many combinations to hint without building them", func() {
		var plugins, names []string
		for i := range 20 {
			n := fmt.Sprintf("n%02d", i)
			names = append(names, n)
			plugins = append(plugins, `{"name": "`+n+`", "source": "./x"}`)
		}
		for _, m := range []string{"m1", "m2"} {
			writeTree(filepath.Join(tmp, m), map[string]string{".claude-plugin/marketplace.json": `{"name": "` + m + `", "owner": {"name": "M"}, "plugins": [` +
				strings.Join(plugins, ", ") + `]}`, "x/commands/x.md": "X\n"})
			Expect(lola("market", "--add", filepath.Join(tmp, m)).code).To(Equal(0))
		}
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		r := lola(append([]string{"install", "--scope", "project", "-t", "claude-code"}, names...)...)
		runtime.ReadMemStats(&after)
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(HaveSuffix("name the ones to install as @<marketplace>/<name>\n"))
		Expect(r.err).NotTo(ContainSubstring("hint:"))
		Expect(after.TotalAlloc-before.TotalAlloc).To(BeNumerically("<", 64<<20), "2^20 combinations are counted, never built")
	})

	It("refuses every bare name of a run at once, each hint naming them all, and --path with what a name resolved to", func() {
		r := lola("install", "formatter", "linter", "--scope", "project", "-t", "claude-code")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("error: formatter (@acme/formatter, @other/formatter), linter (@acme/linter) are not in your cache, and your " +
			"marketplaces list them; name the ones to install\n" +
			"  hint: lola install @acme/formatter @acme/linter --scope project --target claude-code\n" +
			"  hint: lola install @other/formatter @acme/linter --scope project --target claude-code\n"))
		r = lola("install", "@other/formatter", "@acme/linter", "--scope", "project", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)

		p := &scripted{entries: []string{"@acme/formatter"}}
		r = lolaP(p, "install", "fmt-missing", "--scope", "project", "-t", "claude-code")
		Expect(r.code).To(Equal(1), "a name no catalog lists is the engine's to refuse")
		Expect(p.calls).To(BeEmpty())
		Expect(lola("cache", "rm", "formatter", "-y").code).To(Equal(0))
		r = lolaP(p, "install", "formatter", "--path", "tools", "--scope", "project", "-t", "claude-code")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("error: --path does not apply to a marketplace entry: its catalog gives the path\n" +
			"  hint: lola install @acme/formatter --scope project --target claude-code\n"))
	})

	It("offers each marketplace as a group after the cache, and installs a pick with where it came from", func() {
		p := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("@acme/linter")}, review: []string{cli.ReviewApply}}
		r := lolaP(p, "install", "--scope", "project", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.moduleSteps[0].Options).To(BeEmpty())
		Expect(p.moduleSteps[0].Markets).To(Equal([]cli.ModuleGroup{
			{Title: "@acme — Acme tools", Options: []cli.ModuleOption{{Name: "@acme/formatter", Label: "formatter 1.0.0 — Formats code [style]"},
				{Name: "@acme/linter", Label: "linter — Lints code"}}},
			{Title: "@other", Options: []cli.ModuleOption{{Name: "@other/formatter", Label: "formatter — Another formatter"}}}}))
		Expect(equivalents(r.err)).To(Equal([]string{"lola install @acme/linter --scope project -t claude-code"}))
		Expect(filepath.Join(proj, ".claude", "commands", "lint.md")).To(BeARegularFile())
		Expect(manifest()).To(ContainSubstring("  - name: linter\n    source: " + acme + "\n    path: linter\n    market:\n      name: acme\n"))

		// Now in the cache from the same source, it is offered there, once.
		p = &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("linter")}, review: []string{cli.ReviewQuit}}
		r = lolaP(p, "install", "--scope", "project", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.moduleSteps[0].Options).To(ConsistOf(HaveField("Name", "linter")))
		Expect(p.moduleSteps[0].Markets[0].Options).To(Equal([]cli.ModuleOption{{Name: "@acme/formatter", Label: "formatter 1.0.0 — Formats code [style]"}}))
	})

	It("lola install @acme offers its group alone on a terminal and names its first entries off one", func() {
		r := lola("install", "@acme", "--scope", "project", "-t", "claude-code")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("error: @acme is a marketplace; name the entry to install: @acme/formatter, @acme/linter\n  hint: lola market acme\n"))

		p := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods(), mods("@acme/formatter")}}
		r = lolaP(p, "install", "@acme", "--scope", "project", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.moduleSteps[0].Browse).To(BeTrue())
		Expect(p.moduleSteps[0].Options).To(BeEmpty())
		Expect(p.moduleSteps[0].Markets).To(HaveLen(1))
		Expect(p.moduleSteps[1].Problem).To(Equal("pick at least one module"))
		Expect(filepath.Join(proj, ".claude", "commands", "fmt.md")).To(BeARegularFile())
		Expect(manifest()).To(ContainSubstring("    market:\n      name: acme\n"))
	})

	It("asks again when what was picked from a marketplace cannot be fetched, and stops when nothing can be listed", func() {
		third := filepath.Join(tmp, "third")
		writeTree(third, map[string]string{".claude-plugin/marketplace.json": `{"name": "third", "owner": {"name": "T"},
  "plugins": [{"name": "good", "source": "./good"}, {"name": "broken", "source": "./broken"}]}`,
			"good/commands/good.md": "Good\n", "broken/lola.yml": "name: broken\nunknown: key\n", "broken/commands/b.md": "B\n"})
		Expect(lola("market", "--add", third).code).To(Equal(0))
		p := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("@third/broken"), mods("@third/good")},
			review: []string{cli.ReviewApply}}
		r := lolaP(p, "install", "--scope", "project", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.moduleSteps[1].Problem).To(Equal("could not fetch what you picked from your marketplaces; pick again"))
		Expect(filepath.Join(proj, ".claude", "commands", "good.md")).To(BeARegularFile())

		// The project cannot be read any more by the time the pick is fetched: nothing can be
		// listed again, so the run stops with why.
		p = &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){func(cli.ModuleStep) (cli.ModuleAnswer, error) {
			Expect(os.WriteFile(filepath.Join(proj, ".lola", "project.yml"), []byte("version: 1\nmodules: [oops\n"), 0o600)).To(Succeed())
			return cli.ModuleAnswer{Names: []string{"@acme/linter"}}, nil
		}}}
		r = lolaP(p, "install", "--scope", "project", "-t", "claude-code")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("project.yml"))
	})

	It("asks again when two picks are one module name, and installs the answer", func() {
		Expect(lola("cache", "add", "@other/formatter").code).To(Equal(0))
		p := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("formatter", "@acme/formatter"), mods("formatter")},
			review: []string{cli.ReviewApply}}
		r := lolaP(p, "install", "--scope", "project", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.moduleSteps[1].Problem).To(Equal("formatter and @acme/formatter are both the module formatter; lola installs one module of a name: pick one"))
		Expect(p.moduleSteps[1].Preselect).To(Equal([]string{"formatter", "@acme/formatter"}), "the picks stay as they were")
		Expect(filepath.Join(proj, ".claude", "commands", "other.md")).To(BeARegularFile())

		p = &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("@acme/formatter", "@other/formatter"), mods("@acme/linter")},
			review: []string{cli.ReviewApply}}
		r = lolaP(p, "install", "--scope", "project", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.moduleSteps[1].Problem).To(Equal("@acme/formatter and @other/formatter are both the module formatter; lola installs one module of a name: pick one"))
		Expect(filepath.Join(proj, ".claude", "commands", "lint.md")).To(BeARegularFile())
	})

	It("an unreadable markets.yaml leaves the pickers and search to your cache, with a warning", func() {
		Expect(lola("cache", "add", "@acme/linter").code).To(Equal(0))
		markets := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "lola", "markets.yaml")
		Expect(os.WriteFile(markets, []byte("version: 1\nmarkets: [oops\n"), 0o600)).To(Succeed())
		warning := "your marketplaces cannot be read, so the pickers offer none: "
		r := lolaP(&scripted{review: []string{cli.ReviewApply}}, "install", "linter", "--scope", "project", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(ContainSubstring(warning + markets + ": does not parse"))
		Expect(filepath.Join(proj, ".claude", "commands", "lint.md")).To(BeARegularFile())
		p := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("linter")}, review: []string{cli.ReviewApply}}
		r = lolaP(p, "install", "--scope", "project", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.moduleSteps[0].Markets).To(BeEmpty())
		Expect(p.moduleSteps[0].Options).To(ConsistOf(HaveField("Name", "linter")))

		r = lola("search", "lint")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(MatchRegexp(`^linter +cache *\n$`))
		Expect(r.err).To(ContainSubstring("your marketplaces cannot be read, so search lists your cache alone: "))
		r = lola("search", "lint", "--json")
		Expect(r.code).To(Equal(0), r.err)
		var doc struct {
			Results  []map[string]any `json:"results"`
			Warnings []string         `json:"warnings"`
		}
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed(), r.out)
		Expect(doc.Results).To(ConsistOf(HaveKeyWithValue("install", "linter")))
		Expect(doc.Warnings).To(ConsistOf(HavePrefix("your marketplaces cannot be read, so search lists your cache alone: ")))

		r = lola("install", "@acme/linter", "--scope", "project", "-t", "claude-code")
		Expect(r.code).To(Equal(1), "an address still needs your marketplaces")
		Expect(r.err).To(ContainSubstring("markets.yaml"))
	})

	It("refuses --ref with more than one entry picked from a marketplace", func() {
		p := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("@acme/formatter", "@acme/linter")}}
		r := lolaP(p, "install", "@acme", "--ref", "main", "--scope", "project", "-t", "claude-code")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("error: --ref and --path apply to a single source, and you picked more than one entry\n"))
	})

	It("search finds catalog entries and labels them with their marketplace", func() {
		r := lola("search", "format")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(MatchRegexp(`^@acme/formatter +1\.0\.0 +@acme +Formats code\n@other/formatter +@other +Another formatter\n$`))
		r = lola("search", "STYLE", "--json")
		Expect(r.code).To(Equal(0), r.err)
		var hits struct {
			Results []map[string]any `json:"results"`
		}
		Expect(json.Unmarshal([]byte(r.out), &hits)).To(Succeed())
		Expect(hits.Results).To(Equal([]map[string]any{{"name": "formatter", "description": "Formats code", "version": "1.0.0",
			"keywords": []any{"style"}, "where": "@acme", "install": "@acme/formatter"}}))
		Expect(lola("cache", "search", "format").out).To(Equal("no matches\n"), "lola cache search searches the cache alone")
		r = lola("__complete", "install", "@a")
		Expect(r.out).To(HavePrefix("@acme\n@acme/formatter\n@acme/linter\n:4\n"))
	})
})
