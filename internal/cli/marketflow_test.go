package cli_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/cli"
	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/market"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("installing from marketplaces on and off a terminal", func() {
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
		Expect(lola("market", "add", acme).code).To(Equal(0))
		Expect(lola("market", "add", other).code).To(Equal(0))
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
			Expect(lola("market", "add", filepath.Join(tmp, m)).code).To(Equal(0))
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

	It("says an entry the marketplace does not list before it asks which assistant to install for", func() {
		r := lola("install", "@acme/nope", "-y", "--scope", "project")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: marketplace acme lists no nope"))
		Expect(r.err).NotTo(ContainSubstring("assistant"))
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
		Expect(r.err).To(Equal(projectContext(proj) + "error: @acme is a marketplace; name the entry to install: @acme/formatter, @acme/linter\n  hint: lola market acme\n"))

		p := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("@acme/formatter")}}
		r = lolaP(p, "install", "@acme", "--scope", "project", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.moduleSteps).To(HaveLen(1))
		Expect(p.moduleSteps[0].Browse).To(BeTrue())
		Expect(p.moduleSteps[0].Options).To(BeEmpty())
		Expect(p.moduleSteps[0].Markets).To(HaveLen(1))
		Expect(filepath.Join(proj, ".claude", "commands", "fmt.md")).To(BeARegularFile())
		Expect(manifest()).To(ContainSubstring("    market:\n      name: acme\n"))
	})

	It("asks again when what was picked from a marketplace cannot be fetched, and stops when nothing can be listed", func() {
		third := filepath.Join(tmp, "third")
		writeTree(third, map[string]string{".claude-plugin/marketplace.json": `{"name": "third", "owner": {"name": "T"},
  "plugins": [{"name": "good", "source": "./good"}, {"name": "broken", "source": "./broken"}]}`,
			"good/commands/good.md": "Good\n", "broken/lola.yml": "name: broken\nunknown: key\n", "broken/commands/b.md": "B\n"})
		Expect(lola("market", "add", third).code).To(Equal(0))
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

// offTerminal is a scripted Prompter that cannot ask, as the huh adapter off a terminal.
type offTerminal struct{ *scripted }

func (offTerminal) Interactive() bool { return false }

var _ = Describe("the marketplace picker of a bare lola market on a terminal", func() {
	var tmp, url, pin, acme, markets string
	var requests atomic.Int32
	var seed []market.KnownMarket
	// publish commits files to the known marketplace's repository and serves it again, answering
	// the new commit.
	publish := func(files map[string]string) string {
		GinkgoHelper()
		work := filepath.Join(tmp, "work")
		writeTree(work, files)
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "change")
		bare := filepath.Join(tmp, "srv", "kn.git")
		Expect(os.RemoveAll(bare)).To(Succeed())
		gitIn(tmp, "clone", "-q", "--bare", work, bare)
		return gitIn(work, "rev-parse", "HEAD")
	}
	// run is lola with the seed above and, when pr is not nil, pr answering its questions.
	run := func(pr cli.Prompter, args ...string) result {
		GinkgoHelper()
		opts := []cli.Option{cli.WithKnownSeed(func() ([]market.KnownMarket, error) { return seed, nil })}
		if pr != nil {
			opts = append(opts, cli.WithPrompter(pr))
		}
		root, err := cli.NewRootCmd(os.Getenv, opts...)
		Expect(err).NotTo(HaveOccurred())
		var out, errb bytes.Buffer
		if s, ok := pr.(*scripted); ok {
			s.summaryTo = &errb
		}
		root.SetOut(&out)
		root.SetErr(&errb)
		root.SetIn(strings.NewReader(""))
		root.SetArgs(args)
		code := cli.Execute(root)
		return result{out: out.String(), err: errb.String(), code: code}
	}
	stored := func() string {
		GinkgoHelper()
		b, err := os.ReadFile(markets)
		Expect(err).NotTo(HaveOccurred())
		return string(b)
	}
	sum := func() [32]byte { return sha256.Sum256([]byte(stored())) }
	ids := func(rows []engine.PickerRow) []string {
		out := []string{}
		for _, r := range rows {
			out = append(out, r.ID)
		}
		return out
	}
	answer := func(a cli.MarketPickerAnswer) func(cli.MarketPickerStep) (cli.MarketPickerAnswer, error) {
		return func(cli.MarketPickerStep) (cli.MarketPickerAnswer, error) { return a, nil }
	}
	leave := answer(cli.MarketPickerAnswer{Action: cli.PickerCancel})
	type picks = []func(cli.MarketPickerStep) (cli.MarketPickerAnswer, error)
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		srv := filepath.Join(tmp, "srv")
		Expect(os.MkdirAll(srv, 0o755)).To(Succeed())
		base := testenv.ServeGitWith(srv, func(*http.Request) { requests.Add(1) })
		work := filepath.Join(tmp, "work")
		Expect(os.MkdirAll(work, 0o755)).To(Succeed())
		gitIn(work, "init", "-q", "-b", "main")
		pin = publish(map[string]string{".claude-plugin/marketplace.json": `{"name": "kn", "owner": {"name": "K"},
  "plugins": [{"name": "formatter", "source": "./formatter", "description": "Formats code"}]}`, "formatter/commands/fmt.md": "Fmt\n"})
		url = base + "/kn.git"
		seed = []market.KnownMarket{{ID: "kn", Source: url, Commit: pin, Curated: "2026-11-22", Reason: "Reviewed plugins"}}
		acme = filepath.Join(tmp, "acme")
		writeTree(acme, map[string]string{".claude-plugin/marketplace.json": `{"name": "acme", "owner": {"name": "Acme"},
  "plugins": [{"name": "linter", "source": "./linter", "description": "Lints code"}]}`, "linter/commands/lint.md": "Lint\n"})
		Expect(run(nil, "market", "add", acme).code).To(Equal(0))
		markets = filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "lola", "markets.yaml")
		requests.Store(0)
	})

	It("opens without contacting a host, and leaving it changes nothing", func() {
		before := sum()
		p := &scripted{pickers: picks{leave}}
		r := run(p, "market")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"market-picker"}))
		Expect(ids(p.pickerSteps[0].Own)).To(Equal([]string{"acme"}))
		Expect(ids(p.pickerSteps[0].Known)).To(Equal([]string{"kn"}))
		Expect(requests.Load()).To(BeZero())
		Expect(sum()).To(Equal(before))
		Expect(r.out).To(BeEmpty())
	})

	It("checks the remotes of the rows the picker gives only when it starts a check, as u does", func() {
		var got []engine.Check
		p := &scripted{pickers: picks{func(st cli.MarketPickerStep) (cli.MarketPickerAnswer, error) {
			Expect(requests.Load()).To(BeZero(), "nothing is checked before u")
			for c := range st.Check(context.Background(), slices.Concat(st.Own, st.Known)) {
				got = append(got, c)
			}
			return cli.MarketPickerAnswer{Action: cli.PickerCancel}, nil
		}}}
		Expect(run(p, "market").code).To(Equal(0))
		Expect(requests.Load()).NotTo(BeZero())
		Expect(got).To(Equal([]engine.Check{{Index: 1, Status: engine.StatusCurrent}}), "the folder marketplace has no remote; the known one is at its pin")
	})

	It("enables a ticked known marketplace at its pin after the --enable question, and opens again with it among yours", func() {
		before := sum()
		p := &scripted{pickers: picks{answer(cli.MarketPickerAnswer{Action: cli.PickerEnter, Known: []string{"kn"}}), leave}, confirm: []bool{false}}
		r := run(p, "market")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"market-picker", "confirm:Enable known marketplace kn?", "market-picker"}))
		Expect(r.err).To(ContainSubstring("cancelled — nothing was changed"))
		Expect(sum()).To(Equal(before), "a No changes nothing")

		p = &scripted{pickers: picks{answer(cli.MarketPickerAnswer{Action: cli.PickerEnter, Known: []string{"kn"}}), leave}, confirm: []bool{true}}
		r = run(p, "market")
		Expect(r.code).To(Equal(0), r.err)
		Expect(stored()).To(ContainSubstring("  - name: kn\n    source: " + url + "\n    ref: " + pin + "\n"))
		Expect(ids(p.pickerSteps[1].Own)).To(Equal([]string{"acme", "kn"}))
		Expect(p.pickerSteps[1].Own[1].Enabled).To(BeTrue())
		Expect(p.pickerSteps[1].Known).To(BeEmpty())
	})

	It("removes a ticked marketplace of yours only when the --remove question is answered yes", func() {
		before := sum()
		enter := answer(cli.MarketPickerAnswer{Action: cli.PickerEnter, Own: []string{"acme"}})
		p := &scripted{pickers: picks{enter, func(st cli.MarketPickerStep) (cli.MarketPickerAnswer, error) {
			Expect(sum()).To(Equal(before), "a No changes nothing")
			Expect(ids(st.Own)).To(Equal([]string{"acme"}))
			return enter(st)
		}, leave}, where: []int{2, 2}, confirm: []bool{false, true}}
		r := run(p, "market")
		Expect(r.code).To(Equal(0), r.err)
		question := "confirm:Forget marketplace acme (" + acme + ") and drop its cached catalog?"
		Expect(p.calls).To(Equal([]string{"market-picker", "where", question, "market-picker", "where", question, "market-picker"}))
		Expect(p.whereSteps[0].Title).To(Equal("Do what with these?"))
		Expect(p.whereSteps[0].Options).To(Equal([]string{"Browse entries", "Update", "Remove"}))
		Expect(p.pickerSteps[2].Own).To(BeEmpty())
		Expect(stored()).NotTo(ContainSubstring("acme"))
		Expect(r.out).To(ContainSubstring("✓ forgot marketplace acme and dropped its cached catalog\n"))
	})

	It("updates a known marketplace past lola's pin on u after asking, registering the remote's tip", func() {
		Expect(run(nil, "market", "enable", "kn", "-y").code).To(Equal(0))
		tip := publish(map[string]string{".claude-plugin/marketplace.json": `{"name": "kn", "owner": {"name": "K"},
  "plugins": [{"name": "formatter", "source": "./formatter", "description": "Formats code"},
    {"name": "tidier", "source": "./formatter", "description": "Tidies code"}]}`})
		Expect(tip).To(MatchRegexp(`^[0-9a-f]{40}$`))
		before := sum()
		update := func(st cli.MarketPickerStep) (cli.MarketPickerAnswer, error) {
			Expect(st.Own[1]).To(And(HaveField("ID", "kn"), HaveField("AheadOfPin", false)))
			return cli.MarketPickerAnswer{Action: cli.PickerUpdate, Own: []string{"kn"}}, nil
		}
		p := &scripted{pickers: picks{update, leave}, confirm: []bool{false}}
		r := run(p, "market")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"market-picker", "confirm:Update past the commit it is registered at?", "market-picker"}))
		Expect(r.err).To(ContainSubstring("tidier"), "the question shows the catalog's changes")
		Expect(sum()).To(Equal(before), "a No changes nothing")

		p = &scripted{pickers: picks{update, leave}, confirm: []bool{true}}
		r = run(p, "market")
		Expect(r.code).To(Equal(0), r.err)
		Expect(stored()).To(ContainSubstring("    ref: " + tip + "\n"))
		Expect(stored()).NotTo(ContainSubstring(pin))
		Expect(p.pickerSteps[1].Own[1].AheadOfPin).To(BeTrue())
		Expect(r.out).To(ContainSubstring("tidier"))
	})

	Describe("your own marketplace registered at a commit", func() {
		BeforeEach(func() {
			seed = nil
			Expect(run(nil, "market", "add", url, "pinned", "--ref", pin).code).To(Equal(0))
		})
		// status is the check of the picker's row of pinned.
		status := func(st cli.MarketPickerStep) string {
			GinkgoHelper()
			Expect(ids(st.Own)).To(Equal([]string{"acme", "pinned"}))
			got := ""
			for c := range st.Check(context.Background(), st.Own[1:]) {
				got = c.Status
			}
			return got
		}

		It("moves past the commit on u after asking, after which it reads up to date", func() {
			tip := publish(map[string]string{".claude-plugin/marketplace.json": `{"name": "kn", "owner": {"name": "K"},
  "plugins": [{"name": "formatter", "source": "./formatter", "description": "Formats code"},
    {"name": "tidier", "source": "./formatter", "description": "Tidies code"}]}`})
			before := sum()
			update := func(st cli.MarketPickerStep) (cli.MarketPickerAnswer, error) {
				Expect(status(st)).To(Equal(engine.StatusMoved))
				return cli.MarketPickerAnswer{Action: cli.PickerUpdate, Own: []string{"pinned"}}, nil
			}
			p := &scripted{pickers: picks{update, leave}, confirm: []bool{false}}
			r := run(p, "market")
			Expect(r.code).To(Equal(0), r.err)
			Expect(p.calls).To(Equal([]string{"market-picker", "confirm:Update past the commit it is registered at?", "market-picker"}))
			Expect(r.err).To(ContainSubstring("tidier"), "the question shows the catalog's changes")
			Expect(sum()).To(Equal(before), "a No changes nothing")

			current := func(st cli.MarketPickerStep) (cli.MarketPickerAnswer, error) {
				Expect(status(st)).To(Equal(engine.StatusCurrent), "the next u has nothing to offer")
				return cli.MarketPickerAnswer{Action: cli.PickerCancel}, nil
			}
			p = &scripted{pickers: picks{update, current}, confirm: []bool{true}}
			r = run(p, "market")
			Expect(r.code).To(Equal(0), r.err)
			Expect(p.calls).To(Equal([]string{"market-picker", "confirm:Update past the commit it is registered at?", "market-picker"}))
			Expect(stored()).To(ContainSubstring("  - name: pinned\n    source: " + url + "\n    ref: " + tip + "\n"))
			Expect(stored()).NotTo(ContainSubstring(pin))
			Expect(r.out).To(ContainSubstring("tidier"))
		})

		It("on u with a marketplace of yours on no commit, updates that one too when the move fails", func() {
			Expect(os.RemoveAll(filepath.Join(tmp, "srv", "kn.git"))).To(Succeed())
			p := &scripted{pickers: picks{answer(cli.MarketPickerAnswer{Action: cli.PickerUpdate, Own: []string{"acme", "pinned"}}), leave}}
			r := run(p, "market")
			Expect(r.code).To(Equal(0), r.err)
			Expect(p.calls).To(Equal([]string{"market-picker", "market-picker"}))
			Expect(r.err).To(ContainSubstring("error:"))
			Expect(r.out).To(ContainSubstring("marketplace acme"))
		})
	})

	It("updates your own marketplaces on u, as --update does, and opens again", func() {
		p := &scripted{pickers: picks{answer(cli.MarketPickerAnswer{Action: cli.PickerUpdate, Own: []string{"acme"}}), leave}}
		r := run(p, "market")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"market-picker", "market-picker"}))
		Expect(r.err).NotTo(ContainSubstring("error:"))
		Expect(r.out).To(ContainSubstring("marketplace acme"))
		Expect(stored()).NotTo(ContainSubstring("kn"))
	})

	It("adds a source on a, asking --add's questions, and opens again with the new marketplace ticked", func() {
		priv := filepath.Join(tmp, "priv")
		writeTree(priv, map[string]string{".claude-plugin/marketplace.json": `{"name": "acme", "owner": {"name": "P"}, "plugins": [
  {"name": "a", "source": {"source": "url", "url": "ssh://git@git.corp.test/a.git"}}]}`})
		add := answer(cli.MarketPickerAnswer{Action: cli.PickerAdd})
		p := &scripted{pickers: picks{add, leave, add, leave}, sources: [][2]string{{goBack, ""}, {priv, ""}}, names: []string{"mine"},
			sshHosts: []func(cli.SSHHostStep) ([]string, error){func(cli.SSHHostStep) ([]string, error) { return []string{"git.corp.test"}, nil }}}
		before := sum()
		r := run(p, "market")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"market-picker", "market-source", "market-picker"}), "Esc goes back to the picker")
		Expect(sum()).To(Equal(before))

		p.calls = nil
		r = run(p, "market")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"market-picker", "market-source", "market-name", "ssh-hosts", "market-picker"}))
		Expect(p.nameSteps[0].Value).To(Equal("acme-2"))
		Expect(p.pickerSteps[3].PreselectOwn).To(Equal([]string{"mine"}))
		Expect(ids(p.pickerSteps[3].Own)).To(Equal([]string{"acme", "mine"}))
		Expect(stored()).To(ContainSubstring("ssh_hosts:\n      - git.corp.test\n"))
		Expect(r.out).To(ContainSubstring("✓ marketplace mine: 1 entry"))
	})

	It("on mixed ticks enables the known ones first, then asks what to do with yours", func() {
		p := &scripted{pickers: picks{answer(cli.MarketPickerAnswer{Action: cli.PickerEnter, Own: []string{"acme"}, Known: []string{"kn"}}), leave},
			confirm: []bool{true}, where: []int{1}}
		r := run(p, "market")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"market-picker", "confirm:Enable known marketplace kn?", "where", "market-picker"}))
		Expect(stored()).To(ContainSubstring("ref: " + pin))
		Expect(r.out).To(MatchRegexp(`(?s)marketplace kn.*marketplace acme`))
	})

	It("browses the entries of the ticked marketplaces of yours through lola install's picker", func() {
		proj := filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
		GinkgoT().Chdir(proj)
		p := &scripted{pickers: picks{answer(cli.MarketPickerAnswer{Action: cli.PickerEnter, Own: []string{"acme"}}), leave},
			where: []int{0}, modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("@acme/linter")},
			scope: []func(cli.ScopeStep) ([]string, error){scopes(cli.ScopeProject)}, review: []string{cli.ReviewApply}}
		r := run(p, "market")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"market-picker", "where", "modules", "scope", "review", "market-picker"}), r.err)
		Expect(p.moduleSteps[0].Browse).To(BeTrue())
		Expect(p.moduleSteps[0].Markets).To(HaveLen(1))
		Expect(filepath.Join(proj, ".claude", "commands", "lint.md")).To(BeARegularFile(), r.err)
		Expect(equivalents(r.err)).To(Equal([]string{"lola install @acme/linter --scope project -t claude-code"}))
	})

	It("lists as text under --json, --plain, --no-pager and market_picker: false, never asking", func() {
		for _, args := range [][]string{{"--json"}, {"--plain"}, {"--no-pager"}} {
			p := &scripted{}
			r := run(p, append([]string{"market"}, args...)...)
			Expect(r.code).To(Equal(0), r.err)
			Expect(p.calls).To(BeEmpty(), strings.Join(args, " "))
			Expect(r.out).To(ContainSubstring("acme"), strings.Join(args, " "))
		}
		Expect(os.WriteFile(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "lola", "config.yaml"), []byte("version: 1\nmarket_picker: false\n"), 0o600)).To(Succeed())
		p := &scripted{}
		r := run(p, "market")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(BeEmpty())
		Expect(r.out).To(MatchRegexp(`^NAME +FORMAT`))
	})

	It("ends the run on Ctrl+C in the picker, changing nothing", func() {
		before := sum()
		p := &scripted{pickers: picks{aborted[cli.MarketPickerStep, cli.MarketPickerAnswer]}}
		r := run(p, "market")
		Expect(r.code).To(Equal(4))
		Expect(r.err).To(ContainSubstring("cancelled — nothing was changed"))
		Expect(p.calls).To(Equal([]string{"market-picker"}))
		Expect(sum()).To(Equal(before))
	})

	It("ends the run on Ctrl+C in Do what with these?, and goes back to the picker on its Cancel", func() {
		before := sum()
		enter := answer(cli.MarketPickerAnswer{Action: cli.PickerEnter, Own: []string{"acme"}})
		p := &scripted{pickers: picks{enter}, where: []int{whereCtrlC}}
		r := run(p, "market")
		Expect(r.code).To(Equal(4))
		Expect(r.err).To(ContainSubstring("cancelled — nothing was changed"))
		Expect(p.calls).To(Equal([]string{"market-picker", "where"}))
		Expect(sum()).To(Equal(before))

		p = &scripted{pickers: picks{enter, enter, leave}, where: []int{-1, whereBack}}
		r = run(p, "market")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"market-picker", "where", "market-picker", "where", "market-picker"}))
		Expect(sum()).To(Equal(before))
	})

	It("ends the run on Ctrl+C in Add a source…'s source and in --add's name question", func() {
		priv := filepath.Join(tmp, "priv")
		writeTree(priv, map[string]string{".claude-plugin/marketplace.json": `{"name": "acme", "owner": {"name": "P"}, "plugins": []}`})
		before := sum()
		add := answer(cli.MarketPickerAnswer{Action: cli.PickerAdd})
		p := &scripted{pickers: picks{add}, sources: [][2]string{{cancelRun, ""}}}
		r := run(p, "market")
		Expect(r.code).To(Equal(4))
		Expect(p.calls).To(Equal([]string{"market-picker", "market-source"}))

		p = &scripted{pickers: picks{add}, sources: [][2]string{{priv, ""}}, names: []string{cancelRun}}
		r = run(p, "market")
		Expect(r.code).To(Equal(4))
		Expect(p.calls).To(Equal([]string{"market-picker", "market-source", "market-name"}))
		Expect(sum()).To(Equal(before))
	})

	It("ends the run on Ctrl+C in the install flow that browses entries", func() {
		before := sum()
		p := &scripted{pickers: picks{answer(cli.MarketPickerAnswer{Action: cli.PickerEnter, Own: []string{"acme"}})},
			where: []int{0}, modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){aborted[cli.ModuleStep, cli.ModuleAnswer]}}
		r := run(p, "market")
		Expect(r.code).To(Equal(4))
		Expect(p.calls).To(Equal([]string{"market-picker", "where", "modules"}))
		Expect(sum()).To(Equal(before))
	})

	Describe("installing and uninstalling an entry from its detail page", func() {
		var proj string
		linter := func(st cli.MarketPickerStep) engine.PickerEntry {
			GinkgoHelper()
			Expect(st.Entries).To(HaveKey("acme"))
			Expect(st.Entries["acme"]).To(HaveLen(1))
			return st.Entries["acme"][0]
		}
		install := answer(cli.MarketPickerAnswer{Action: cli.PickerInstall, Market: "acme", Entry: "linter", Entries: []string{"linter"}})
		uninstall := answer(cli.MarketPickerAnswer{Action: cli.PickerUninstall, Market: "acme", Entry: "linter", Entries: []string{"linter"}})
		BeforeEach(func() {
			proj = filepath.Join(tmp, "proj")
			Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
			fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
			GinkgoT().Chdir(proj)
		})

		It("installs where the install flow's own scope question is answered, and opens again at the entry, installed", func() {
			p := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes(cli.ScopeProject)}, pickers: picks{install, leave}, review: []string{cli.ReviewApply}}
			r := run(p, "market")
			Expect(r.code).To(Equal(0), r.err)
			Expect(p.calls).To(Equal([]string{"market-picker", "scope", "review", "market-picker"}), r.err)
			Expect(filepath.Join(proj, ".claude", "commands", "lint.md")).To(BeARegularFile(), r.err)
			Expect(p.pickerSteps[0].Resume).To(BeZero())
			Expect(linter(p.pickerSteps[0]).Installed).To(BeFalse())
			Expect(p.pickerSteps[1].Resume).To(Equal(cli.PickerResume{Market: "acme", Entry: "linter"}))
			Expect(linter(p.pickerSteps[1]).Installed).To(BeTrue())
			Expect(p.pickerSteps[1].Context).To(HavePrefix("project " + proj))
		})

		It("opens again on the entries page when the install began there", func() {
			p := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes(cli.ScopeProject)}, pickers: picks{answer(cli.MarketPickerAnswer{Action: cli.PickerInstall, Market: "acme", Entry: "linter", Entries: []string{"linter"}, FromEntries: true}), leave},
				review: []string{cli.ReviewApply}}
			r := run(p, "market")
			Expect(r.code).To(Equal(0), r.err)
			Expect(filepath.Join(proj, ".claude", "commands", "lint.md")).To(BeARegularFile(), r.err)
			Expect(p.pickerSteps[1].Resume).To(Equal(cli.PickerResume{Market: "acme", Entry: "linter", OnEntries: true}))
		})

		It("prints a failed install's error and opens again on the entries page when it began there", func() {
			Expect(os.RemoveAll(filepath.Join(acme, "linter"))).To(Succeed())
			p := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes(cli.ScopeProject)}, pickers: picks{answer(cli.MarketPickerAnswer{Action: cli.PickerInstall, Market: "acme", Entry: "linter", Entries: []string{"linter"}, FromEntries: true}), leave}}
			r := run(p, "market")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.err).To(ContainSubstring("error: "))
			Expect(p.pickerSteps[1].Resume).To(Equal(cli.PickerResume{Market: "acme", Entry: "linter", OnEntries: true}))
		})

		It("uninstalls an installed entry and opens again at it, not installed", func() {
			p := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes(cli.ScopeProject)}, pickers: picks{install, uninstall, leave}, review: []string{cli.ReviewApply}, confirm: []bool{true}}
			r := run(p, "market")
			Expect(r.code).To(Equal(0), r.err)
			Expect(filepath.Join(proj, ".claude", "commands", "lint.md")).NotTo(BeAnExistingFile())
			Expect(p.pickerSteps[2].Resume).To(Equal(cli.PickerResume{Market: "acme", Entry: "linter"}))
			Expect(linter(p.pickerSteps[2]).Installed).To(BeFalse())
		})

		Describe("with several entries", func() {
			both := func(action cli.MarketPickerAction) cli.MarketPickerAnswer {
				return cli.MarketPickerAnswer{Action: action, Market: "acme", Entry: "formatter", Entries: []string{"formatter", "linter"}, FromEntries: true}
			}
			BeforeEach(func() {
				writeTree(acme, map[string]string{".claude-plugin/marketplace.json": `{"name": "acme", "owner": {"name": "Acme"},
  "plugins": [{"name": "formatter", "source": "./formatter", "description": "Formats code"},
    {"name": "linter", "source": "./linter", "description": "Lints code"}]}`, "formatter/commands/fmt.md": "Fmt\n"})
				Expect(run(nil, "market", "update", "acme").code).To(Equal(0))
			})

			It("installs them in one install, asking its questions once, and opens again on the first", func() {
				p := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes(cli.ScopeProject)}, pickers: picks{answer(both(cli.PickerInstall)), leave}, review: []string{cli.ReviewApply}}
				r := run(p, "market")
				Expect(r.code).To(Equal(0), r.err)
				Expect(p.calls).To(Equal([]string{"market-picker", "scope", "review", "market-picker"}), r.err)
				Expect(filepath.Join(proj, ".claude", "commands", "fmt.md")).To(BeARegularFile(), r.err)
				Expect(filepath.Join(proj, ".claude", "commands", "lint.md")).To(BeARegularFile(), r.err)
				Expect(p.pickerSteps[1].Resume).To(Equal(cli.PickerResume{Market: "acme", Entry: "formatter", OnEntries: true}))
			})

			It("uninstalls them in one uninstall", func() {
				p := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes(cli.ScopeProject)}, pickers: picks{answer(both(cli.PickerInstall)), answer(both(cli.PickerUninstall)), leave},
					review: []string{cli.ReviewApply}, confirm: []bool{true}}
				r := run(p, "market")
				Expect(r.code).To(Equal(0), r.err)
				Expect(filepath.Join(proj, ".claude", "commands", "fmt.md")).NotTo(BeAnExistingFile())
				Expect(filepath.Join(proj, ".claude", "commands", "lint.md")).NotTo(BeAnExistingFile())
			})

			It("names an entry the marketplace no longer offers, and installs none", func() {
				ghost := both(cli.PickerInstall)
				ghost.Entries = []string{"formatter", "ghost"}
				p := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes(cli.ScopeProject)}, pickers: picks{answer(ghost), leave}}
				r := run(p, "market")
				Expect(r.code).To(Equal(0), r.err)
				Expect(r.err).To(ContainSubstring("error: acme no longer offers ghost"))
				Expect(r.err).To(ContainSubstring("hint: lola market update acme"))
				Expect(filepath.Join(proj, ".claude", "commands", "fmt.md")).NotTo(BeAnExistingFile())
			})
		})

		It("prints a failed install's error with its hints and opens again at the same entry", func() {
			Expect(os.RemoveAll(filepath.Join(acme, "linter"))).To(Succeed())
			p := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes(cli.ScopeProject)}, pickers: picks{install, leave}}
			r := run(p, "market")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.err).To(ContainSubstring("error: "))
			Expect(r.err).To(ContainSubstring("hint: "))
			Expect(p.pickerSteps[1].Resume).To(Equal(cli.PickerResume{Market: "acme", Entry: "linter"}))
			Expect(linter(p.pickerSteps[1]).Installed).To(BeFalse())
		})

		It("ends the run on Ctrl+C in the install's own question", func() {
			p := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes(cli.ScopeProject)}, pickers: picks{install}, review: []string{cli.ReviewQuit}}
			r := run(p, "market")
			Expect(r.code).To(Equal(4))
			Expect(p.calls).To(Equal([]string{"market-picker", "scope", "review"}))
			Expect(filepath.Join(proj, ".claude", "commands", "lint.md")).NotTo(BeAnExistingFile())
		})

		It("opens again with no entry for Enter, update and Add", func() {
			p := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes(cli.ScopeProject)}, pickers: picks{install, answer(cli.MarketPickerAnswer{Action: cli.PickerUpdate, Own: []string{"acme"}}),
				answer(cli.MarketPickerAnswer{Action: cli.PickerAdd}), leave}, review: []string{cli.ReviewApply}, sources: [][2]string{{"", ""}}}
			r := run(p, "market")
			Expect(r.code).To(Equal(0), r.err)
			Expect(p.pickerSteps).To(HaveLen(4))
			Expect(p.pickerSteps[1].Resume).NotTo(BeZero())
			Expect(p.pickerSteps[2].Resume).To(BeZero())
			Expect(p.pickerSteps[3].Resume).To(BeZero())
		})

		It("opens again with no entry after Enter's follow-up question", func() {
			p := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes(cli.ScopeProject)}, pickers: picks{install, answer(cli.MarketPickerAnswer{Action: cli.PickerEnter, Own: []string{"acme"}}), leave},
				review: []string{cli.ReviewApply}, where: []int{1}}
			r := run(p, "market")
			Expect(r.code).To(Equal(0), r.err)
			Expect(p.pickerSteps).To(HaveLen(3))
			Expect(p.pickerSteps[1].Resume).NotTo(BeZero())
			Expect(p.pickerSteps[2].Resume).To(BeZero())
		})

		It("refuses --scope both before opening, since it acts on one scope", func() {
			p := &scripted{pickers: picks{leave}}
			r := run(p, "market", "--scope", "both")
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(ContainSubstring("error: the market picker acts on one scope, and --scope both names two"))
			Expect(r.err).To(ContainSubstring("hint: lola market --scope project"))
			Expect(r.err).To(ContainSubstring("hint: lola market --scope user"))
			Expect(p.calls).To(BeEmpty())
		})

		It("acts on the scope --scope user names, and says so in the context line", func() {
			p := &scripted{pickers: picks{install, leave}, review: []string{cli.ReviewApply}}
			r := run(p, "market", "--scope", "user")
			Expect(r.code).To(Equal(0), r.err)
			Expect(p.pickerSteps[0].Context).To(HavePrefix("user scope"))
			Expect(filepath.Join(os.Getenv("HOME"), ".claude", "commands", "lint.md")).To(BeARegularFile(), r.err)
			Expect(filepath.Join(proj, ".claude", "commands", "lint.md")).NotTo(BeAnExistingFile())
			Expect(linter(p.pickerSteps[1]).Installed).To(BeTrue())
		})

		It("leaves an install's scope to the install flow, which resolves it as lola install does, not as the picker's flags", func() {
			bare := filepath.Join(tmp, "bare")
			Expect(os.MkdirAll(bare, 0o755)).To(Succeed())
			GinkgoT().Chdir(bare)
			p := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes(cli.ScopeUser)}, pickers: picks{install, leave}, review: []string{cli.ReviewApply}}
			r := run(p, "market")
			Expect(r.code).To(Equal(0), r.err)
			Expect(p.pickerSteps[0].Context).To(HavePrefix("user scope"))
			Expect(filepath.Join(os.Getenv("HOME"), ".claude", "commands", "lint.md")).To(BeARegularFile(), r.err)
			Expect(p.reviewSteps[0].Summary.Folded()).NotTo(ContainSubstring("flag:"), "the picker's scope is not passed on as a flag")
			Expect(p.pickerSteps[1].Resume).To(Equal(cli.PickerResume{Market: "acme", Entry: "linter"}))
		})

		It("passes the scope flags given to lola market on to the install", func() {
			p := &scripted{pickers: picks{install, leave}, review: []string{cli.ReviewApply}}
			r := run(p, "market", "--user")
			Expect(r.code).To(Equal(0), r.err)
			Expect(p.reviewSteps[0].Summary.Folded()).To(ContainSubstring("flag:"))
			Expect(filepath.Join(os.Getenv("HOME"), ".claude", "commands", "lint.md")).To(BeARegularFile(), r.err)
			Expect(filepath.Join(proj, ".claude", "commands", "lint.md")).NotTo(BeAnExistingFile())
		})

		It("keeps uninstall pinned to the scope the picker reads", func() {
			p := &scripted{pickers: picks{install, uninstall, leave}, review: []string{cli.ReviewApply}, confirm: []bool{true}}
			r := run(p, "market", "--scope", "user")
			Expect(r.code).To(Equal(0), r.err)
			Expect(p.calls).To(Equal([]string{"market-picker", "review", "market-picker", "confirm:Uninstall linter?", "market-picker"}))
			Expect(filepath.Join(os.Getenv("HOME"), ".claude", "commands", "lint.md")).NotTo(BeAnExistingFile())
		})

		It("keeps the picker open on a lock.json lola cannot read, with the error and its hints as a warning", func() {
			Expect(os.WriteFile(filepath.Join(proj, ".lola", "lock.json"), []byte("{oops"), 0o600)).To(Succeed())
			p := &scripted{pickers: picks{leave}}
			r := run(p, "market")
			Expect(r.code).To(Equal(0), r.err)
			Expect(p.calls).To(Equal([]string{"market-picker"}))
			Expect(p.pickerSteps[0].Entries).To(BeEmpty())
			Expect(r.err).To(ContainSubstring("lock.json"))
			Expect(r.err).To(ContainSubstring("hint: "))
		})
	})

	It("lists as text off a terminal", func() {
		p := &scripted{}
		r := run(offTerminal{p}, "market")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(BeEmpty())
		Expect(r.out).To(MatchRegexp(`^NAME +FORMAT`))
	})
})
