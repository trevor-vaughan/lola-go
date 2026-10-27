package engine_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// The catalog of the marketplace acme that the installs below resolve against: entries from the
// module repositories (served at lola.test, as a stranger's catalog may name only a host by
// name), a relative one from the marketplace's own tree, and the ones lola refuses to install.
const installCatalog = `{"name": "acme", "owner": {"name": "Acme"}, "plugins": [
  {"name": "secdevai", "source": {"source": "git-subdir", "url": "MODS/tools.git", "path": "module", "ref": "main", "sha": "HEAD"},
   "description": "Security review"},
  {"name": "forked", "source": {"source": "url", "url": "MODS/tools.git", "ref": "main", "sha": "FORK"}},
  {"name": "pinned-ref", "source": {"source": "url", "url": "MODS/tools.git", "ref": "FORK"}},
  {"name": "renamed", "source": {"source": "url", "url": "MODS/named.git"}},
  {"name": "kit", "source": {"source": "url", "url": "MODS/kit.git", "ref": "main"}},
  {"name": "formatter", "source": "./tools/formatter"},
  {"name": "deps", "source": "./tools/deps", "dependencies": ["formatter", "lint@other", "missing"]},
  {"name": "document-skills", "source": "./", "strict": false, "skills": ["./skills/xlsx"]},
  {"name": "npmx", "source": {"source": "npm", "package": "x"}}
]}`

var _ = Describe("installing from a marketplace", func() {
	var (
		tmp, proj, mods, market, head, fork string
		msrv, catalog                       string
		p                                   paths.Paths
		e                                   *engine.Engine
		sc                                  engine.Scope
		requests, catalogFetches            atomic.Int32
		ctx                                 = context.Background()
		claude                              = engine.Options{Targets: []string{"claude-code"}}
	)
	// bare commits files to the work tree <name> and publishes it as srv/<name>.git.
	bare := func(srv, name string, files map[string]string) string {
		GinkgoHelper()
		work := filepath.Join(tmp, "work", name)
		if _, err := os.Stat(work); errors.Is(err, os.ErrNotExist) {
			Expect(os.MkdirAll(work, 0o755)).To(Succeed())
			gitIn(work, "init", "-q", "-b", "main")
		}
		writeTree(work, files)
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "change")
		dst := filepath.Join(srv, name+".git")
		Expect(os.RemoveAll(dst)).To(Succeed())
		gitIn(tmp, "clone", "-q", "--bare", work, dst)
		return gitIn(work, "rev-parse", "HEAD")
	}
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		proj = filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		e.Getwd = func() (string, error) { return proj, nil }
		sc = engine.Scope{Kind: target.Project, ProjectDir: proj}

		// The module repositories: tools has no lola.yml in module/, so it takes its entry's name.
		srv := filepath.Join(tmp, "mods")
		bare(srv, "tools", map[string]string{"module/skills/sec/SKILL.md": "---\ndescription: Sec\n---\nSec\n", "README.md": "tools\n"})
		work := filepath.Join(tmp, "work", "tools")
		gitIn(work, "checkout", "-q", "-b", "fork")
		writeTree(work, map[string]string{"module/skills/sec/SKILL.md": "---\ndescription: Forked\n---\nForked\n"})
		gitIn(work, "commit", "-q", "-am", "fork")
		fork = gitIn(work, "rev-parse", "HEAD")
		gitIn(work, "checkout", "-q", "main")
		head = gitIn(work, "rev-parse", "main")
		Expect(os.RemoveAll(filepath.Join(srv, "tools.git"))).To(Succeed())
		gitIn(tmp, "clone", "-q", "--bare", "--no-single-branch", work, filepath.Join(srv, "tools.git"))
		gitIn(filepath.Join(srv, "tools.git"), "fetch", "-q", work, "fork:fork")
		bare(srv, "named", map[string]string{"lola.yml": "name: other\n", "commands/o.md": "O\n"})
		bare(srv, "kit", map[string]string{"lola.yml": "name: kit\nvars:\n  tools: [rg]\n", "commands/k.md": "K\n"})
		requests.Store(0)
		mods = testenv.ServeGitTLS(srv, func(*http.Request) { requests.Add(1) })

		// The marketplace itself, a source you type, served on loopback.
		catalog = strings.NewReplacer("MODS", mods, "HEAD", head, "FORK", fork).Replace(installCatalog)
		msrv = filepath.Join(tmp, "msrv")
		bare(msrv, "plugins", map[string]string{".claude-plugin/marketplace.json": catalog,
			"tools/formatter/commands/fmt.md": "Fmt\n", "tools/deps/commands/deps.md": "Deps\n", "skills/xlsx/SKILL.md": "---\ndescription: X\n---\nX\n",
			"py/marketplace.yml": "name: py\nmodules:\n  - name: pyfork\n    repository: " + mods + "/tools.git\n    ref: " + fork + "\n"})
		market = testenv.ServeGitWith(msrv, func(r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/plugins.git/info/refs") {
				catalogFetches.Add(1)
			}
		}) + "/plugins.git"
		_, err = e.AddMarket(ctx, engine.MarketAddRequest{Source: market})
		Expect(err).NotTo(HaveOccurred())
		_, err = e.AddMarket(ctx, engine.MarketAddRequest{Name: "py", Source: market, Path: "py"})
		Expect(err).NotTo(HaveOccurred())
	})
	install := func(opts engine.Options, reqs ...engine.InstallRequest) (*engine.Result, error) {
		opts.Targets = claude.Targets
		return e.Install(ctx, sc, proj, reqs, opts)
	}
	entry := func(name string) manifest.Entry {
		GinkgoHelper()
		m, err := e.Manifest(sc)
		Expect(err).NotTo(HaveOccurred())
		ent := m.Find(name)
		Expect(ent).NotTo(BeNil())
		return *ent
	}
	cached := func() []string {
		GinkgoHelper()
		names, err := e.Store.Names()
		Expect(err).NotTo(HaveOccurred())
		return names
	}

	It("a wrapped archive marketplace's relative entries install exactly their files, and a teammate's sync reproduces them", func() {
		// GitHub's archive shape: every file under one repo-<sha>/ directory.
		arch := tarGz("repo-abc", map[string]string{
			".claude-plugin/marketplace.json": `{"name": "arch", "owner": {"name": "Arch"}, "plugins": [{"name": "aplain", "source": "./plain"},
  {"name": "asel", "source": "./sel", "skills": ["./skills/s1"]}, {"name": "aroot", "source": "./", "skills": ["./other/skills/r1"]}]}`,
			"plain/commands/p.md":      "P\n",
			"sel/skills/s1/SKILL.md":   "---\ndescription: S1\n---\nS1\n",
			"sel/skills/s2/SKILL.md":   "---\ndescription: S2\n---\nS2\n",
			"other/skills/r1/SKILL.md": "---\ndescription: R1\n---\nR1\n",
			"other/skills/r2/SKILL.md": "---\ndescription: R2\n---\nR2\n",
		})
		files := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(arch) }))
		DeferCleanup(files.Close)
		archURL := files.URL + "/m.tar.gz"
		_, err := e.AddMarket(ctx, engine.MarketAddRequest{Source: archURL})
		Expect(err).NotTo(HaveOccurred())
		_, err = install(engine.Options{}, engine.InstallRequest{Arg: "@arch/aplain"}, engine.InstallRequest{Arg: "@arch/asel"},
			engine.InstallRequest{Arg: "@arch/aroot"})
		Expect(err).NotTo(HaveOccurred())
		Expect(entry("aplain")).To(And(HaveField("Source", archURL), HaveField("Path", "repo-abc/plain")))
		Expect(entry("asel")).To(And(HaveField("Path", "repo-abc/sel"), HaveField("Components", &source.Components{Skills: []string{"skills/s1"}})))
		Expect(entry("aroot")).To(And(HaveField("Path", "repo-abc"), HaveField("Components", &source.Components{Skills: []string{"other/skills/r1"}})))
		rendered := func(dir string) {
			GinkgoHelper()
			Expect(filepath.Join(dir, ".claude", "commands", "p.md")).To(BeARegularFile())
			Expect(filepath.Join(dir, ".claude", "skills", "s1", "SKILL.md")).To(BeARegularFile())
			Expect(filepath.Join(dir, ".claude", "skills", "r1", "SKILL.md")).To(BeARegularFile())
			Expect(filepath.Join(dir, ".claude", "skills", "s2")).NotTo(BeAnExistingFile(), "asel selects s1 alone")
			Expect(filepath.Join(dir, ".claude", "skills", "r2")).NotTo(BeAnExistingFile(), "aroot selects r1 alone")
		}
		rendered(proj)

		teammate := filepath.Join(tmp, "teammate")
		Expect(os.MkdirAll(filepath.Join(teammate, ".lola"), 0o755)).To(Succeed())
		b, err := os.ReadFile(filepath.Join(proj, ".lola", "project.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(teammate, ".lola", "project.yml"), b, 0o644)).To(Succeed())
		for _, dir := range []string{p.Markets, p.MarketCache, p.Store} {
			Expect(os.RemoveAll(dir)).To(Succeed())
		}
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: engine.Scope{Kind: target.Project, ProjectDir: teammate}, Trust: func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }})
		Expect(err).NotTo(HaveOccurred())
		rendered(teammate)
	})

	It("install @market/module records the real source, its pins and market name and source", func() {
		var shown []string
		res, err := install(engine.Options{ShowResolved: func(lines []string) { shown = append(shown, lines...) }},
			engine.InstallRequest{Arg: "@acme/secdevai"}, engine.InstallRequest{Arg: "@acme/formatter"})
		Expect(err).NotTo(HaveOccurred())
		marketRef := &manifest.MarketRef{Name: "acme", Source: market}
		sec := entry("secdevai")
		Expect(sec).To(And(HaveField("Source", mods+"/tools.git"), HaveField("Path", "module"), HaveField("Ref", "main"),
			HaveField("Commit", head), HaveField("Digest", HavePrefix("sha256:")), HaveField("Market", marketRef)))
		Expect(filepath.Join(proj, ".claude", "skills", "sec", "SKILL.md")).To(BeARegularFile())
		Expect(cached()).To(ConsistOf("secdevai", "formatter"), "a module without a lola.yml takes its entry's name, not its directory's")
		fmtEntry := entry("formatter")
		marketCommit := gitIn(filepath.Join(tmp, "work", "plugins"), "rev-parse", "HEAD")
		Expect(fmtEntry).To(And(HaveField("Source", market), HaveField("Path", "tools/formatter"), HaveField("Ref", ""),
			HaveField("Commit", marketCommit), HaveField("Market", marketRef)), "a relative entry pins the marketplace's own commit")
		Expect(res.Modules).To(ContainElement(And(HaveField("Name", "secdevai"), HaveField("Market", marketRef))))
		Expect(shown).To(Equal([]string{"@acme/secdevai → " + mods + "/tools.git path module @ main (" + head + ")",
			"@acme/formatter → " + market + " path tools/formatter @ " + marketCommit}))

		// A teammate with the file and none of the marketplace, or the cache, syncs it.
		teammate := filepath.Join(tmp, "teammate")
		Expect(os.MkdirAll(filepath.Join(teammate, ".lola"), 0o755)).To(Succeed())
		b, err := os.ReadFile(filepath.Join(proj, ".lola", "project.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(teammate, ".lola", "project.yml"), b, 0o644)).To(Succeed())
		for _, dir := range []string{p.Markets, p.MarketCache, p.Store} {
			Expect(os.RemoveAll(dir)).To(Succeed())
		}
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: engine.Scope{Kind: target.Project, ProjectDir: teammate},
			Trust: func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }})
		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.Join(teammate, ".claude", "skills", "sec", "SKILL.md")).To(BeARegularFile())
		Expect(cached()).To(ConsistOf("secdevai", "formatter"))
		up, err := e.Update(ctx, []string{"secdevai"})
		Expect(err).NotTo(HaveOccurred())
		Expect(up.Updates).To(HaveLen(1), "cache update fetches it under its name again")
	})

	It("installing an entry again by name keeps its market and ref, unless another ref is given", func() {
		_, err := install(engine.Options{}, engine.InstallRequest{Arg: "@acme/secdevai"})
		Expect(err).NotTo(HaveOccurred())
		marketRef := &manifest.MarketRef{Name: "acme", Source: market}
		_, err = install(engine.Options{}, engine.InstallRequest{Arg: "secdevai"})
		Expect(err).NotTo(HaveOccurred())
		Expect(entry("secdevai")).To(And(HaveField("Market", marketRef), HaveField("Ref", "main")), "by name alone")
		_, err = install(engine.Options{}, engine.InstallRequest{Arg: "secdevai", Ref: "main"})
		Expect(err).NotTo(HaveOccurred())
		Expect(entry("secdevai")).To(And(HaveField("Market", marketRef), HaveField("Ref", "main")), "with the ref it records")
		_, err = install(engine.Options{}, engine.InstallRequest{Arg: "secdevai", Ref: "fork"})
		Expect(err).NotTo(HaveOccurred())
		Expect(entry("secdevai")).To(And(HaveField("Market", BeNil()), HaveField("Ref", "fork")), "another ref is yours, not the catalog's")
	})

	It("install @m/x --ref records the ref and no market, and the notice says so", func() {
		res, err := install(engine.Options{}, engine.InstallRequest{Arg: "@acme/secdevai", Ref: "fork"})
		Expect(err).NotTo(HaveOccurred())
		sec := entry("secdevai")
		Expect(sec).To(And(HaveField("Ref", "fork"), HaveField("Commit", fork), HaveField("Market", BeNil())))
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "secdevai: pinned to your ref fork; not following acme"}))
		// Installing it again by name keeps it as it is: no market comes back.
		_, err = install(engine.Options{}, engine.InstallRequest{Arg: "secdevai"})
		Expect(err).NotTo(HaveOccurred())
		Expect(entry("secdevai").Market).To(BeNil())
	})

	It("refuses a version listed for an address that its catalog now lists from elsewhere", func() {
		ch, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: []engine.ScopeChoice{{Scope: sc, Source: engine.SourceFlag}},
			Modules: []engine.InstallRequest{{Arg: "@acme/secdevai"}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(ch.Resolved).To(Equal([]string{"@acme/secdevai → " + mods + "/tools.git path module @ main (" + head + ")"}))
		listed := ch.Versions[0]
		// The catalog moves the entry to another repository before the install is confirmed.
		msrv := filepath.Join(tmp, "msrv")
		bare(msrv, "plugins", map[string]string{".claude-plugin/marketplace.json": strings.Replace(
			strings.NewReplacer("MODS", mods, "HEAD", head, "FORK", fork).Replace(installCatalog),
			`"url": "`+mods+`/tools.git", "path": "module"`, `"url": "`+mods+`/named.git", "path": "module"`, 1)})
		_, err = e.UpdateMarkets(ctx, engine.MarketUpdateRequest{Names: []string{"acme"}})
		Expect(err).NotTo(HaveOccurred())
		_, err = install(engine.Options{}, engine.InstallRequest{Arg: "@acme/secdevai", Version: &listed})
		Expect(err).To(MatchError(ContainSubstring("is not the module or source version")))
		Expect(filepath.Join(proj, ".lola", "project.yml")).NotTo(BeAnExistingFile())
	})

	It("a catalog commit pin its ref does not reach refuses before anything is cached", func() {
		for arg, want := range map[string]string{
			"@acme/forked":     "marketplace acme lists forked at " + fork + ", which main does not reach",
			"@acme/pinned-ref": "marketplace acme lists pinned-ref at " + fork + ", which the default branch does not reach",
			"@py/pyfork":       "marketplace py lists pyfork at " + fork + ", which the default branch does not reach",
		} {
			_, err := install(engine.Options{Display: []string{"--json"}}, engine.InstallRequest{Arg: arg})
			Expect(err).To(MatchError(want), arg)
			mkt := strings.TrimPrefix(strings.Split(arg, "/")[0], "@")
			Expect(errs.Hints(err)).To(Equal([]string{"lola market " + mkt + " --update --json"}), arg)
		}
		Expect(cached()).To(BeEmpty())
		Expect(filepath.Join(proj, ".lola", "project.yml")).NotTo(BeAnExistingFile())
	})

	It("refuses an address it cannot resolve, naming what there is, before anything is fetched", func() {
		requests.Store(0)
		display := []string{"--plain"}
		for arg, want := range map[string][2]string{
			"@nope/x":        {"you have no marketplace nope", "lola market --plain"},
			"@acme/secdevay": {"marketplace acme lists no secdevay; close names: secdevai", "lola market acme --plain"},
			"@acme/npmx":     {"marketplace acme skips npmx: npm sources are not supported: installing one runs a package manager", "lola market acme --plain"},
			"@acme":          {"@acme is a marketplace; name the entry to install: @acme/secdevai, @acme/forked, @acme/pinned-ref, @acme/renamed, @acme/kit, @acme/formatter, @acme/deps, @acme/document-skills", "lola market acme --plain"},
		} {
			_, err := install(engine.Options{Display: display}, engine.InstallRequest{Arg: arg})
			Expect(err).To(MatchError(want[0]), arg)
			hints := []string{}
			if want[1] != "" {
				hints = []string{want[1]}
			}
			Expect(append([]string{}, errs.Hints(err)...)).To(Equal(hints), arg)
		}
		_, err := install(engine.Options{}, engine.InstallRequest{Arg: "@acme/formatter", Path: "x"})
		Expect(err).To(MatchError("--path does not apply to @acme/formatter: its catalog gives the path"))
		_, err = install(engine.Options{}, engine.InstallRequest{Arg: "@Acme/x"})
		Expect(err).To(MatchError(ContainSubstring(`marketplace name "Acme" is invalid`)))
		Expect(requests.Load()).To(BeZero(), "no module repository was asked")

		// A source the catalog names keeps rules a source you type does not (the redirect rule),
		// so no command to install it by hand is offered, and the message says why.
		why := "marketplace acme lists renamed, and the module at its source names itself other; lola installs a catalog entry only under " +
			"the entry's name, and offers no install of its source by hand, which would not keep the catalog's redirect rule"
		_, err = install(engine.Options{DryRun: true, Display: []string{"--json"}}, engine.InstallRequest{Arg: "@acme/renamed"})
		Expect(err).To(MatchError(why))
		Expect(errs.Hints(err)).To(BeEmpty())
		_, err = e.AddEntry(ctx, "@acme/renamed", "", "", []string{"--plain"})
		Expect(err).To(MatchError(why))
		Expect(errs.Hints(err)).To(BeEmpty())
		_, err = e.Check(ctx, sc, proj, []string{"@acme/renamed"}, nil, nil)
		Expect(errs.Hints(err)).To(BeEmpty())
		Expect(cached()).To(BeEmpty())
	})

	It("names the dependencies lola does not install, with an install for each one the marketplace offers", func() {
		res, err := install(engine.Options{DryRun: true, Display: []string{"--json"}}, engine.InstallRequest{Arg: "@acme/deps"})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElements(
			engine.Notice{Level: engine.LevelInfo, Text: "deps: lists dependencies lola does not install: formatter, lint@other, missing"},
			engine.Notice{Level: engine.LevelInfo, Text: "install deps's dependency formatter → lola install --project @acme/formatter -n --json"}))
		Expect(res.Notices).NotTo(ContainElement(HaveField("Text", ContainSubstring("@acme/missing"))))
	})

	It("search finds catalog entries and labels them with their marketplace", func() {
		hits, warnings, err := e.Search("SECURITY", true, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(warnings).To(BeEmpty())
		Expect(hits).To(Equal([]engine.SearchHit{{Name: "secdevai", Description: "Security review", Keywords: []string{}, Where: "@acme",
			Install: "@acme/secdevai"}}))
		for _, q := range []string{"npmx"} {
			hits, _, err = e.Search(q, true, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(hits).To(BeEmpty(), "%s: an entry lola skips is no hit", q)
		}
		Expect(requests.Load()).To(BeZero(), "search never fetches")

		_, err = install(engine.Options{}, engine.InstallRequest{Arg: "@acme/secdevai"})
		Expect(err).NotTo(HaveOccurred())
		hits, _, err = e.Search("sec", true, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(hits).To(HaveLen(1))
		Expect(hits[0]).To(And(HaveField("Where", "cache"), HaveField("Install", "secdevai")), "the entry the cache holds from its source is its cache hit")
		hits, _, err = e.Search("forked", false, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(hits).To(BeEmpty(), "the cache alone")

		// One cache entry lola cannot read costs only that entry, with the command that clears it.
		Expect(os.WriteFile(filepath.Join(p.Store, "secdevai", "index.yml"), []byte("heads: [oops\n"), 0o600)).To(Succeed())
		hits, warnings, err = e.Search("security", true, []string{"--json"})
		Expect(err).NotTo(HaveOccurred())
		Expect(hits).To(ContainElement(HaveField("Install", "@acme/secdevai")))
		Expect(warnings).To(ContainElement(And(HavePrefix("secdevai: your cache's record of it cannot be read"), HaveSuffix("→ lola cache rm secdevai --json"))))
		groups, _, err := e.MarketGroups(nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(groups).NotTo(BeEmpty())
	})

	It("offers an entry with a selection though your cache holds its name whole from the same source", func() {
		whole := filepath.Join(tmp, "whole")
		writeTree(whole, map[string]string{"lola.yml": "name: whole\n", "skills/a/SKILL.md": "---\ndescription: A\n---\nA\n",
			"skills/b/SKILL.md": "---\ndescription: B\n---\nB\n",
			".claude-plugin/marketplace.json": `{"name": "wm", "owner": {"name": "W"}, "plugins": [{"name": "whole", "source": "./", "description": "Whole",
  "skills": ["./skills/a"]}]}`})
		_, err := e.AddMarket(ctx, engine.MarketAddRequest{Source: whole})
		Expect(err).NotTo(HaveOccurred())
		_, err = e.AddSource(ctx, proj, whole, "", "")
		Expect(err).NotTo(HaveOccurred())
		groups, _, err := e.MarketGroups(nil)
		Expect(err).NotTo(HaveOccurred())
		k := slices.IndexFunc(groups, func(g engine.MarketGroup) bool { return g.Name == "wm" })
		Expect(k).NotTo(BeNumerically("<", 0))
		Expect(groups[k].Entries).To(ConsistOf(And(HaveField("Install", "@wm/whole"), HaveField("Cached", false))),
			"the cache's whole is not what the entry installs")
		hits, _, err := e.Search("whole", true, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(hits).To(ContainElement(HaveField("Install", "@wm/whole")))
	})

	It("takes a bare name for a marketplace entry only when your cache lacks it and it is no source here", func() {
		offers, err := e.MarketMeaning("secdevai", proj, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(offers).To(Equal([]engine.MarketOffer{{Install: "@acme/secdevai", Name: "secdevai", Description: "Security review", Tags: []string{},
			Host: "https://lola.test:" + strings.Split(mods, ":")[2]}}))
		for _, name := range []string{"Secdevai", "npmx", "nope"} {
			offers, err = e.MarketMeaning(name, proj, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(offers).To(BeEmpty(), name)
		}
		Expect(os.MkdirAll(filepath.Join(proj, "secdevai"), 0o755)).To(Succeed())
		offers, err = e.MarketMeaning("secdevai", proj, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(offers).To(BeEmpty(), "a folder here is that source")
		_, err = e.AddEntry(ctx, "@acme/secdevai", "", "", nil)
		Expect(err).NotTo(HaveOccurred())
		offers, err = e.MarketMeaning("secdevai", tmp, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(offers).To(BeEmpty(), "a name your cache holds is that module")
	})

	It("market --remove counts market entries in known scopes and installed modules keep rendering", func() {
		_, err := install(engine.Options{}, engine.InstallRequest{Arg: "@acme/secdevai"}, engine.InstallRequest{Arg: "@acme/formatter"})
		Expect(err).NotTo(HaveOccurred())
		_, err = e.Install(ctx, engine.Scope{Kind: target.User}, proj, []engine.InstallRequest{{Arg: "@acme/formatter"}}, claude)
		Expect(err).NotTo(HaveOccurred())
		yes := func(engine.MarketRow) (bool, error) { return true, nil }
		res, err := e.RemoveMarket(engine.MarketRemoveRequest{Name: "acme", DryRun: true, Display: []string{"--json"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(Equal([]engine.Notice{{Level: engine.LevelInfo,
			Text: "3 entries in this project and your user scope name acme as their marketplace; installed from their real sources, they stay installed"}}))
		// An entry whose market names acme from another source is not this marketplace's.
		b, err := os.ReadFile(filepath.Join(proj, ".lola", "project.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(proj, ".lola", "project.yml"), []byte(strings.Replace(string(b), "source: "+market+"\n",
			"source: https://example.com/other.git\n", 1)), 0o644)).To(Succeed())
		res, err = e.RemoveMarket(engine.MarketRemoveRequest{Name: "acme", Confirm: yes, Display: []string{"--json"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(Equal([]engine.Notice{{Level: engine.LevelInfo,
			Text: "2 entries in this project and your user scope name acme as their marketplace; installed from their real sources, they stay " +
				"installed, and lola sync --update follows acme again once you add it back → lola market acme --add " + market +
				" --path .claude-plugin/marketplace.json --json"}}))
		_, err = e.Render(ctx, sc, claude)
		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.Join(proj, ".claude", "skills", "sec", "SKILL.md")).To(BeARegularFile())
		Expect(filepath.Join(proj, ".claude", "commands", "fmt.md")).To(BeARegularFile())
		res, err = e.RemoveMarket(engine.MarketRemoveRequest{Name: "py", Confirm: yes})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(BeEmpty(), "no entry came from py")
	})

	// publish replaces the catalog's text old with new and publishes the marketplace again,
	// returning its new commit.
	publish := func(old, new string) string {
		GinkgoHelper()
		catalog = strings.Replace(catalog, old, new, 1)
		return bare(msrv, "plugins", map[string]string{".claude-plugin/marketplace.json": catalog})
	}
	trustAll := func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }
	update := func(names ...string) *engine.SyncResult {
		GinkgoHelper()
		res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Modules: names, Update: true, Trust: trustAll, Display: []string{"--plain"}})
		Expect(err).NotTo(HaveOccurred())
		return res
	}
	secLine := `{"name": "secdevai", "source": {"source": "git-subdir", "url": "` + "MODS" + `/tools.git", "path": "module", "ref": "main", "sha": "`

	It("sync --update follows the catalog only within the same source, path and ref", func() {
		_, err := install(engine.Options{}, engine.InstallRequest{Arg: "@acme/secdevai"}, engine.InstallRequest{Arg: "@acme/kit"},
			engine.InstallRequest{Arg: "@acme/formatter"})
		Expect(err).NotTo(HaveOccurred())
		line := strings.Replace(secLine, "MODS", mods, 1)
		// The tools repository moves on; the catalog pins its new head, and the marketplace itself
		// moves, which moves its relative entry.
		work := filepath.Join(tmp, "work", "tools")
		writeTree(work, map[string]string{"module/skills/sec/SKILL.md": "---\ndescription: Sec 2\n---\nSec 2\n"})
		gitIn(work, "commit", "-q", "-am", "two")
		next := gitIn(work, "rev-parse", "HEAD")
		writeTree(work, map[string]string{"module/skills/sec/SKILL.md": "---\ndescription: Sec 3\n---\nSec 3\n"})
		gitIn(work, "commit", "-q", "-am", "three") // main's head, which the catalog does not pin
		gitIn(work, "push", "-q", filepath.Join(tmp, "mods", "tools.git"), "main")
		marketCommit := publish(line+head, line+next)
		catalogFetches.Store(0)
		update("secdevai", "kit")
		Expect(entry("secdevai")).To(And(HaveField("Commit", next), HaveField("Ref", "main")), "the catalog's pin, not the head of main")
		Expect(catalogFetches.Load()).To(BeEquivalentTo(1), "two entries of one marketplace: one catalog fetch")
		update("formatter")
		Expect(entry("formatter").Commit).To(Equal(marketCommit), "a relative entry follows the marketplace's own commit")

		catalogFetches.Store(0)
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll})
		Expect(err).NotTo(HaveOccurred())
		Expect(catalogFetches.Load()).To(BeZero(), "lola sync without --update reads no catalog")

		// A commit pin the entry's ref does not reach is refused, as at install.
		publish(line+next, line+fork)
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Modules: []string{"secdevai"}, Update: true, Trust: trustAll})
		Expect(err).To(MatchError("secdevai: marketplace acme lists secdevai at " + fork + ", which main does not reach"))

		// Another ref, or another source: a notice with the install that takes it there; the entry
		// keeps its source and ref, and updates from them.
		publish(`"ref": "main", "sha": "`+fork, `"ref": "fork", "sha": "`+fork)
		res := update("secdevai")
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "your marketplace acme moved secdevai to ref fork; " +
			"secdevai updates from its own source → lola install --project @acme/secdevai --plain"}))
		three := gitIn(work, "rev-parse", "HEAD")
		Expect(entry("secdevai")).To(And(HaveField("Ref", "main"), HaveField("Commit", three)), "a plain update: the head of its own ref")
		publish(`"url": "`+mods+`/tools.git", "path": "module", "ref": "fork"`, `"url": "`+mods+`/named.git", "path": "module", "ref": "main"`)
		res = update("secdevai")
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "your marketplace acme lists secdevai from " + mods +
			"/named.git path module; secdevai updates from its own source (lola caches one secdevai per machine: to install the catalog's, " +
			"uninstall it and remove it from your cache first)"}))
		// Another selection is the same: a notice that says what to do, and no command.
		_, err = install(engine.Options{}, engine.InstallRequest{Arg: "@acme/document-skills"})
		Expect(err).NotTo(HaveOccurred())
		catalog = strings.Replace(catalog, `"skills": ["./skills/xlsx"]`, `"skills": ["./skills/docx"]`, 1)
		bare(msrv, "plugins", map[string]string{".claude-plugin/marketplace.json": catalog, "skills/docx/SKILL.md": "---\ndescription: D\n---\nD\n"})
		res = update("document-skills")
		moved := "your marketplace acme selects other components for document-skills; document-skills updates from its own source (lola " +
			"caches one document-skills per machine: to install the catalog's, uninstall it and remove it from your cache first)"
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: moved}))
		Expect(moved).NotTo(ContainSubstring("→"))
		Expect(entry("document-skills").Components).To(Equal(&source.Components{Skills: []string{"skills/xlsx"}}), "the entry keeps its selection")

		// A catalog that cannot be fetched: a warning, a plain update, and the cached catalog kept.
		Expect(os.RemoveAll(filepath.Join(msrv, "plugins.git"))).To(Succeed())
		res = update("secdevai")
		Expect(res.Notices).To(ContainElement(And(HaveField("Level", engine.LevelWarn),
			HaveField("Text", HavePrefix("could not fetch your marketplace acme, so its entries update from their own sources: ")))))
		v, err := e.ShowMarket("acme", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(v.Entries).NotTo(BeEmpty())

		// Your marketplace of that name is from another source: its catalog is not fetched.
		b, err := os.ReadFile(filepath.Join(proj, ".lola", "project.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(proj, ".lola", "project.yml"), []byte(strings.Replace(string(b), "source: "+market+"\n",
			"source: https://example.com/other.git\n", 2)), 0o644)).To(Succeed())
		catalogFetches.Store(0)
		update("secdevai") // the hand edit is trusted here
		res = update("secdevai")
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "secdevai: your marketplace acme is not the one " +
			"it was installed through, from https://example.com/other.git; it updates from its own source"}))
		Expect(catalogFetches.Load()).To(BeZero())

		_, err = e.RemoveMarket(engine.MarketRemoveRequest{Name: "acme", Confirm: func(engine.MarketRow) (bool, error) { return true, nil }})
		Expect(err).NotTo(HaveOccurred())
		res = update("secdevai")
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "secdevai: you have no marketplace acme; it updates from its own source"}))
	})

	It("keeps the redirect rule of a source a catalog named in every sync of the entry", func() {
		away := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, mods+r.URL.RequestURI(), http.StatusFound)
		}))
		DeferCleanup(away.Close)
		user := engine.Scope{Kind: target.User}
		write := func(market string) {
			GinkgoHelper()
			Expect(os.MkdirAll(filepath.Dir(p.UserManifest), 0o700)).To(Succeed())
			Expect(os.WriteFile(p.UserManifest, []byte("version: 1\ntargets: [claude-code]\nmodules:\n  - name: kit\n    source: "+away.URL+
				"/kit.git\n"+market), 0o600)).To(Succeed())
		}
		write("    market: {name: acme, source: https://example.com/plugins.git}\n")
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: user})
		Expect(err).To(MatchError(ContainSubstring("redirects, and lola follows no redirect of a source a catalog names")), "a catalog named the source: git follows no redirect")
		write("    market: {name: acme, source: " + away.URL + "/kit.git}\n")
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: user})
		Expect(err).NotTo(HaveOccurred(), "the marketplace's own source, which you typed, keeps today's rule")
	})

	It("sync --update fetches no catalog for an entry of a file it has not trusted", func() {
		_, err := install(engine.Options{}, engine.InstallRequest{Arg: "@acme/kit"})
		Expect(err).NotTo(HaveOccurred())
		b, err := os.ReadFile(filepath.Join(proj, ".lola", "project.yml"))
		Expect(err).NotTo(HaveOccurred())
		pulled := engine.Scope{Kind: target.Project, ProjectDir: filepath.Join(tmp, "pulled")}
		Expect(os.MkdirAll(filepath.Join(pulled.ProjectDir, ".lola"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(pulled.ProjectDir, ".lola", "project.yml"), b, 0o644)).To(Succeed())
		catalogFetches.Store(0)
		// A dry run records no trust, so it promises nothing of the next run.
		dry, err := e.Sync(ctx, engine.SyncRequest{Scope: pulled, Update: true, Options: engine.Options{DryRun: true}})
		Expect(err).NotTo(HaveOccurred())
		Expect(dry.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo,
			Text: "kit: lola follows a marketplace only for an entry you trusted before the run; it updates from its own source"}))
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: pulled, Update: true})
		Expect(err).To(MatchError(ContainSubstring("needs your trust")))
		res, err := e.Sync(ctx, engine.SyncRequest{Scope: pulled, Update: true, Trust: trustAll})
		Expect(err).NotTo(HaveOccurred())
		Expect(catalogFetches.Load()).To(BeZero(), "a file not trusted before the run makes no catalog request")
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo,
			Text: "kit: lola follows a marketplace only for an entry you trusted before the run; it updates from its own source, and the " +
				"next update follows acme → lola sync -C " + pulled.ProjectDir + " --update kit"}))
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: pulled, Update: true, Trust: trustAll})
		Expect(err).NotTo(HaveOccurred())
		Expect(catalogFetches.Load()).To(BeEquivalentTo(1), "once trusted, the catalog is followed")

		// An entry no next run would follow says why, with no command to run.
		for i, c := range []struct{ old, new, want string }{
			{"      name: acme\n", "      name: ghost\n", "kit: you have no marketplace ghost; it updates from its own source"},
			{"      source: " + market + "\n", "      source: https://example.com/other.git\n",
				"kit: your marketplace acme is not the one it was installed through, from https://example.com/other.git; it updates from its own source"},
		} {
			Expect(string(b)).To(ContainSubstring(c.old))
			other := engine.Scope{Kind: target.Project, ProjectDir: filepath.Join(tmp, "pulled-"+string(rune('a'+i)))}
			Expect(os.MkdirAll(filepath.Join(other.ProjectDir, ".lola"), 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(other.ProjectDir, ".lola", "project.yml"), []byte(strings.Replace(string(b), c.old, c.new, 1)), 0o644)).To(Succeed())
			res, err := e.Sync(ctx, engine.SyncRequest{Scope: other, Update: true, Trust: trustAll})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: c.want}))
			Expect(res.Notices).NotTo(ContainElement(HaveField("Text", ContainSubstring("→"))), c.want)
		}
	})

	It("install @m/x --ref records no market and sync --update does not follow the catalog", func() {
		_, err := install(engine.Options{}, engine.InstallRequest{Arg: "@acme/secdevai", Ref: "main"})
		Expect(err).NotTo(HaveOccurred())
		Expect(entry("secdevai")).To(And(HaveField("Market", BeNil()), HaveField("Ref", "main"), HaveField("Commit", head)))
		line := strings.Replace(secLine, "MODS", mods, 1)
		publish(line+head, line+fork) // a pin the catalog would be refused for
		catalogFetches.Store(0)
		res := update()
		Expect(catalogFetches.Load()).To(BeZero(), "no catalog is read for an entry without market")
		Expect(res.Notices).NotTo(ContainElement(HaveField("Text", ContainSubstring("marketplace"))))
		Expect(entry("secdevai").Commit).To(Equal(head), "the head of its ref")
	})

	It("an @ source in any file refuses before trust and network, with a working hint only when one exists", func() {
		// trust.json holds another project's record, which no refusal below may touch.
		other := filepath.Join(tmp, "other")
		Expect(os.MkdirAll(filepath.Join(other, ".lola"), 0o755)).To(Succeed())
		_, err := e.Install(ctx, engine.Scope{Kind: target.Project, ProjectDir: other}, other, []engine.InstallRequest{{Arg: mods + "/kit.git"}}, claude)
		Expect(err).NotTo(HaveOccurred())
		trustBefore, err := os.ReadFile(p.Trust)
		Expect(err).NotTo(HaveOccurred())
		const addresses = "  - name: formatter\n    source: '@acme/formatter'\n  - name: kit\n    source: '@acme/kit'\n" +
			"  - {name: lint, source: '@gone/lint'}\n  - {name: fmt2, source: '@acme/formatter'}\n"
		refusal := func(file string) string {
			return file + " records 4 marketplace addresses as sources; a file must record each module's real source\n" +
				"  " + file + ":5: formatter's source is @acme/formatter\n  " + file + ":7: kit's source is @acme/kit\n" +
				"  " + file + ":8: lint's source is @gone/lint; edit the entry to name the module's source, ref and path\n" +
				"  " + file + ":9: fmt2's source is @acme/formatter"
		}
		projectYML := filepath.Join(proj, ".lola", "project.yml")
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n" + addresses})
		user := engine.Scope{Kind: target.User}
		writeTree(filepath.Dir(p.UserManifest), map[string]string{"user.yml": "version: 1\ntargets: [claude-code]\nmodules:\n" + addresses})
		requests.Store(0)
		catalogFetches.Store(0)

		By("project.yml and user.yml: one install per entry it replaces, a marketplace view for one named otherwise, none without the marketplace")
		display := []string{"--json"}
		runs := func(s engine.Scope, dry bool) map[string]func() error {
			opts := engine.Options{Targets: claude.Targets, DryRun: dry, Display: display}
			return map[string]func() error{
				"install": func() error {
					_, err := e.Install(ctx, s, proj, []engine.InstallRequest{{Arg: mods + "/kit.git"}}, opts)
					return err
				},
				"sync": func() error {
					_, err := e.Sync(ctx, engine.SyncRequest{Scope: s, Options: opts, Display: display})
					return err
				},
				"sync --update": func() error {
					_, err := e.Sync(ctx, engine.SyncRequest{Scope: s, Update: true, Options: opts, Display: display})
					return err
				},
				"render":    func() error { _, err := e.Render(ctx, s, opts); return err },
				"uninstall": func() error { _, err := e.Uninstall(ctx, s, []string{"formatter"}, opts); return err },
				"list":      func() error { _, _, err := e.Status(ctx, s, display); return err },
			}
		}
		for _, c := range []struct {
			s          engine.Scope
			file, flag string
			dry        bool
		}{{sc, projectYML, "--project", false}, {sc, projectYML, "--project", true}, {user, p.UserManifest, "--user", false}} {
			for name, run := range runs(c.s, c.dry) {
				err := run()
				Expect(err).To(MatchError(refusal(c.file)), name)
				dry := []string{}
				if c.dry && name != "list" { // lola list writes nothing, and takes no -n
					dry = []string{"-n"}
				}
				Expect(errs.Hints(err)).To(Equal([]string{
					strings.Join(slices.Concat([]string{"lola install", c.flag, "@acme/formatter"}, dry, display), " "),
					strings.Join(slices.Concat([]string{"lola install", c.flag, "@acme/kit"}, dry, display), " "),
					"lola market acme --json"}), "%s %s", c.flag, name)
			}
		}
		Expect(requests.Load()).To(BeZero(), "the module server saw no request")
		Expect(catalogFetches.Load()).To(BeZero(), "no catalog was fetched")
		Expect(os.ReadFile(p.Trust)).To(Equal(trustBefore), "the project has no trust record, and none was written")

		By("a project found elsewhere is named with -C")
		e.Getwd = func() (string, error) { return tmp, nil }
		err = runs(sc, false)["sync"]()
		Expect(errs.Hints(err)).To(HaveExactElements(HavePrefix("lola install -C "+proj+" @acme/formatter"), HavePrefix("lola install -C "+proj+" @acme/kit"),
			Equal("lola market acme --json")))
		e.Getwd = func() (string, error) { return proj, nil }

		By("each kind of profile: the marketplace view, which shows what to copy into it")
		Expect(os.Remove(p.UserManifest)).To(Succeed())
		Expect(os.Remove(projectYML)).To(Succeed())
		const profileBody = "version: 1\nmodules:\n  - name: formatter\n    source: '@acme/formatter'\n  - {name: lint, source: '@gone/lint'}\n"
		profileRefusal := func(file string) string {
			return file + " records 2 marketplace addresses as sources; a file must record each module's real source\n" +
				"  " + file + ":4: formatter's source is @acme/formatter\n" +
				"  " + file + ":5: lint's source is @gone/lint; edit the entry to name the module's source, ref and path"
		}
		yes := func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }
		def := filepath.Join(p.UserProfiles, "default.yml")
		writeTree(p.UserProfiles, map[string]string{"default.yml": profileBody})
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: user, Display: display})
		Expect(err).To(MatchError(profileRefusal(def)))
		Expect(errs.Hints(err)).To(Equal([]string{"lola market acme --json"}))
		Expect(os.Remove(def)).To(Succeed())

		goYML := filepath.Join(p.UserProfiles, "go.yml")
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: user, Profiles: &engine.ProfileChange{Action: engine.ProfileOn, Names: []string{"go"}}})
		Expect(err).NotTo(HaveOccurred())
		writeTree(p.UserProfiles, map[string]string{"go.yml": profileBody})
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: user})
		Expect(err).To(MatchError(profileRefusal(goYML)))
		Expect(errs.Hints(err)).To(Equal([]string{"lola market acme"}))

		team := filepath.Join(proj, ".lola", "profiles", "team.yml")
		writeTree(proj, map[string]string{".lola/profiles/team.yml": "version: 1\n"})
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: yes, Profiles: &engine.ProfileChange{Action: engine.ProfileOn, Names: []string{"team"}}})
		Expect(err).NotTo(HaveOccurred())
		writeTree(proj, map[string]string{".lola/profiles/team.yml": profileBody})
		trustBefore, err = os.ReadFile(p.Trust)
		Expect(err).NotTo(HaveOccurred())
		requests.Store(0)
		for name, run := range runs(sc, false) {
			err := run()
			Expect(err).To(MatchError(profileRefusal(team)), name)
			Expect(errs.Hints(err)).To(Equal([]string{"lola market acme --json"}), name)
		}
		Expect(requests.Load()).To(BeZero())
		Expect(os.ReadFile(p.Trust)).To(Equal(trustBefore))

		bare(filepath.Join(tmp, "mods"), "profiles", map[string]string{"profiles/go.yml": profileBody})
		_, err = e.AddProfile(ctx, engine.ProfileAddRequest{Name: "shared", Source: mods + "/profiles.git", Rerun: []string{"--trust", "--plain"},
			Consent: func([]engine.RemoteProfile) (bool, error) { return true, nil }})
		Expect(err).To(MatchError(ContainSubstring("the source has no profile shared")))
		_, err = e.AddProfile(ctx, engine.ProfileAddRequest{Name: "go", Source: mods + "/profiles.git", Rerun: []string{"--trust", "--plain"},
			Consent: func([]engine.RemoteProfile) (bool, error) { return true, nil }})
		Expect(err).To(MatchError(profileRefusal("profiles/go.yml")))
		Expect(errs.Hints(err)).To(Equal([]string{"lola market acme --plain"}), "a remote profile refuses at --add")
		Expect(filepath.Join(p.RemoteProfiles, "go")).NotTo(BeAnExistingFile())

		By("sync --update refuses a profile before it fetches the catalog a market: entry names")
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		_, err = e.Install(ctx, user, proj, []engine.InstallRequest{{Arg: "@acme/formatter"}}, claude)
		Expect(err).NotTo(HaveOccurred())
		writeTree(p.UserProfiles, map[string]string{"default.yml": profileBody})
		catalogFetches.Store(0)
		requests.Store(0)
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: user, Update: true, Display: display})
		Expect(err).To(MatchError(profileRefusal(def)))
		Expect(errs.Hints(err)).To(Equal([]string{"lola market acme --json"}))
		Expect(catalogFetches.Load()).To(BeZero(), "user.yml's formatter names acme, whose catalog is not fetched")
		Expect(requests.Load()).To(BeZero())

		By("a tracked lock.json is refused before the profiles it names, sync --update included (Plan 6b §19 S5)")
		userYML, err := os.ReadFile(p.UserManifest)
		Expect(err).NotTo(HaveOccurred())
		writeTree(proj, map[string]string{".lola/project.yml": string(userYML)}) // formatter, installed from @acme
		gitIn(proj, "init", "-q")
		gitIn(proj, "add", "-f", ".lola/lock.json")
		catalogFetches.Store(0)
		for _, update := range []bool{false, true} {
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Update: update, Trust: yes, Display: display})
			Expect(err).To(MatchError(ContainSubstring(".lola/lock.json is tracked by git")), "update: %v", update)
		}
		Expect(catalogFetches.Load()).To(BeZero())
	})

	It("every reader of a file with an @ source gives the hints the commands give", func() {
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - name: formatter\n    source: '@acme/formatter'\n"})
		hint := "lola install --project @acme/formatter --json"
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		_, _, err := e.ProfileState(ctx, []engine.Scope{sc}, "go", "--json")
		Expect(err).To(MatchError(HaveSuffix(":5: formatter's source is @acme/formatter, a marketplace address; a file must record the module's real source")))
		Expect(errs.Hints(err)).To(Equal([]string{hint}), "lola profile go")

		res, err := e.RemoveMarket(engine.MarketRemoveRequest{Name: "py", DryRun: true, Display: []string{"--json"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(And(HaveField("Level", engine.LevelWarn),
			HaveField("Text", HaveSuffix(" → lola install --project @acme/formatter -n --json")))), "lola market --remove -n's count keeps -n")

		res, err = e.RemoveMarket(engine.MarketRemoveRequest{Name: "py", Display: []string{"--json"},
			Confirm: func(engine.MarketRow) (bool, error) { return true, nil }})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(And(HaveField("Level", engine.LevelWarn), HaveField("Text", HaveSuffix(" → "+hint)))),
			"lola market --remove's count")
	})

	It("an unresolved @ entry's vars are read by tag: a date, a map or a non-finite number refuses at load, and a list installs", func() {
		user := engine.Scope{Kind: target.User}
		write := func(vars string) {
			writeTree(filepath.Dir(p.UserManifest), map[string]string{"user.yml": "version: 1\ntargets: [claude-code]\nmodules:\n" +
				"  - name: kit\n    source: '@acme/kit'\n    vars: " + vars + "\n"})
		}
		for vars, want := range map[string]string{
			"{tools: 2024-01-02}": `:6: modules[0].vars.tools: dates are not supported; quote it to make it a string: "2024-01-02"`,
			"{tools: {a: b}}":     ":6: modules[0].vars.tools: a var is a string, true or false, a number, or a list of strings",
			"{tools: .nan}":       ":6: modules[0].vars.tools: NaN and infinity are not supported",
		} {
			write(vars)
			_, err := e.Install(ctx, user, proj, []engine.InstallRequest{{Arg: "@acme/kit"}}, engine.Options{})
			Expect(err).To(MatchError(p.UserManifest+want), vars)
		}
		write("{tools: [rg, jq]}")
		_, err := e.Install(ctx, user, proj, []engine.InstallRequest{{Arg: "@acme/kit"}}, engine.Options{})
		Expect(err).NotTo(HaveOccurred(), "a list is a list, as the module declares")
		m, err := e.Manifest(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Find("kit").Vars).To(Equal(map[string]any{"tools": []string{"rg", "jq"}}), "kept as written when the entry is resolved")
	})

	It("install @m/x replaces an unresolved @ entry and records it resolved", func() {
		projectYML := filepath.Join(proj, ".lola", "project.yml")
		const rest = "  - name: kit\n    source: '@acme/kit'\n  - {name: lint, source: '@gone/lint'}\n  - {name: fmt2, source: '@acme/formatter'}\n"
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  # the formatter\n" +
			"  - name: formatter\n    source: '@acme/formatter'\n" + rest})
		display := []string{"--json"}
		res, err := install(engine.Options{Display: display}, engine.InstallRequest{Arg: "@acme/formatter"})
		Expect(err).NotTo(HaveOccurred())
		marketCommit := gitIn(filepath.Join(tmp, "work", "plugins"), "rev-parse", "HEAD")
		b, err := os.ReadFile(projectYML)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(HavePrefix("version: 1\ntargets: [claude-code]\nmodules:\n  # the formatter\n  - name: formatter\n    source: '"+market+
			"'\n    path: tools/formatter\n    commit: "+marketCommit+"\n"), "the entry, resolved in place")
		Expect(string(b)).To(HaveSuffix(rest), "every other unresolved entry as written")
		Expect(filepath.Join(proj, ".claude", "commands", "fmt.md")).To(BeARegularFile())
		Expect(res.Notices).To(ContainElements(
			engine.Notice{Level: engine.LevelWarn, Text: projectYML + ":8: kit's source is @acme/kit, a marketplace address; a file must record the " +
				"module's real source → lola install --project @acme/kit --json"},
			engine.Notice{Level: engine.LevelWarn, Text: projectYML + ":9: lint's source is @gone/lint, a marketplace address; a file must record " +
				"the module's real source; edit the entry to name the module's source, ref and path"},
			engine.Notice{Level: engine.LevelWarn, Text: projectYML + ":10: fmt2's source is @acme/formatter, a marketplace address; a file must " +
				"record the module's real source → lola market acme --json"}))

		pick := func(arg string) error {
			_, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: []engine.ScopeChoice{{Scope: sc}}, Modules: []engine.InstallRequest{{Arg: arg}},
				DryRun: true, Display: display})
			return err
		}
		Expect(pick("@acme/kit")).To(Succeed(), "the pickers read the file as the install of an address that replaces an entry does")
		err = pick(mods + "/kit.git")
		Expect(err).To(MatchError(HavePrefix(projectYML + " records 3 marketplace addresses as sources")))
		Expect(errs.Hints(err)).To(Equal([]string{"lola install --project @acme/kit -n --json", "lola market acme --json"}))

		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc})
		Expect(err).To(MatchError(HavePrefix(projectYML+" records 3 marketplace addresses as sources")), "the file is refused while one remains")
		_, err = install(engine.Options{}, engine.InstallRequest{Arg: "formatter"})
		Expect(err).To(MatchError(HavePrefix(projectYML+" records 3")), "and so is an install of anything else")

		By("its own file's entry keeps its targets and rules")
		user := engine.Scope{Kind: target.User}
		writeTree(filepath.Dir(p.UserManifest), map[string]string{"user.yml": "version: 1\ntargets: [claude-code]\nmodules:\n" +
			"  - {name: kit, source: '@acme/kit', targets: [claude-code], rules: false}\n"})
		res, err = e.Install(ctx, user, proj, []engine.InstallRequest{{Arg: "@acme/kit"}}, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).NotTo(ContainElement(HaveField("Level", engine.LevelWarn)))
		m, err := e.Manifest(user)
		Expect(err).NotTo(HaveOccurred())
		off := false
		Expect(m.Modules).To(HaveExactElements(And(HaveField("Name", "kit"), HaveField("Source", mods+"/kit.git"), HaveField("Ref", "main"),
			HaveField("Targets", []string{"claude-code"}), HaveField("Rules", &off), HaveField("Market", &manifest.MarketRef{Name: "acme", Source: market}))))
	})

	It("install @m/x over a pulled file's untrusted targets hints only -t while the file holds an @ source, since sync refuses it", func() {
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n" +
			"  - name: formatter\n    source: '@acme/formatter'\n  - name: kit\n    source: '@acme/kit'\n"})
		for _, arg := range []string{"@acme/formatter", "@acme/kit"} {
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: arg}}, engine.Options{Display: []string{"--json"}})
			Expect(err).To(MatchError("project.yml's top-level targets have not been trusted on this machine"), arg)
			Expect(errs.Hints(err)).To(HaveExactElements(HaveSuffix("-t <id>")), arg)
		}
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc})
		Expect(err).To(MatchError(ContainSubstring("records 2 marketplace addresses as sources")), "the sync the hint would name refuses")
	})

	It("an entry changed since trusted, beside an @ source, is refused with no sync hint, since sync refuses the file", func() {
		_, err := install(engine.Options{}, engine.InstallRequest{Arg: mods + "/kit.git"})
		Expect(err).NotTo(HaveOccurred())
		projectYML := filepath.Join(proj, ".lola", "project.yml")
		b, err := os.ReadFile(projectYML)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(projectYML, append([]byte(strings.Replace(string(b), "  - name: kit\n", "  - name: kit\n    rules: false\n", 1)),
			"  - {name: formatter, source: '@acme/formatter'}\n"...), 0o644)).To(Succeed())
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc})
		Expect(err).To(MatchError(ContainSubstring("formatter's source is @acme/formatter")), "the sync a hint would name refuses")

		both := []engine.InstallRequest{{Arg: "@acme/formatter"}, {Arg: "kit"}}
		_, err = install(engine.Options{}, both...)
		Expect(err).To(MatchError("kit: project.yml changed for it since you last trusted it"))
		Expect(errs.Hints(err)).To(BeEmpty(), "install")
		choices := func(reqs ...engine.InstallRequest) (*engine.Choices, error) {
			return e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: []engine.ScopeChoice{{Scope: sc}}, Modules: reqs, DryRun: true})
		}
		_, err = choices(both...)
		Expect(err).To(MatchError("kit: project.yml changed for it since you last trusted it"))
		Expect(errs.Hints(err)).To(BeEmpty(), "the pickers")
		ch, err := choices(both[0])
		Expect(err).NotTo(HaveOccurred())
		Expect(ch.Warnings).To(ContainElement("1 module in project.yml changed since you last trusted it: kit"), "the pickers' notice, without → lola sync")
	})

	It("checks and caches an address as install fetches it", func() {
		rep, err := e.Check(ctx, sc, proj, []string{"@acme/secdevai"}, []string{"claude-code"}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(rep.Modules).To(HaveLen(1))
		Expect(rep.Modules[0].Name).To(Equal("secdevai"))
		Expect(cached()).To(BeEmpty(), "check caches nothing")
		_, err = e.Check(ctx, sc, proj, []string{"@acme/forked"}, nil, nil)
		Expect(err).To(MatchError(ContainSubstring("which main does not reach")))
		_, err = e.Check(ctx, sc, proj, []string{"@acme/renamed"}, nil, nil)
		Expect(err).To(MatchError(ContainSubstring("the module at its source names itself other")), "check refuses what install refuses")

		v, err := e.AddEntry(ctx, "@acme/secdevai", "", "", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(v.Name).To(Equal("secdevai"))
		Expect(v.Commit).To(Equal(head))
		_, err = e.AddEntry(ctx, "@acme/secdevai", "", "module", nil)
		Expect(err).To(MatchError("--path does not apply to @acme/secdevai: its catalog gives the path"))
	})
})
