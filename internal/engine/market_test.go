package engine_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/market"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// acmeCatalog is a Claude catalog with one relative entry, one remote entry and one npm entry.
const acmeCatalog = `{"name": "acme", "owner": {"name": "Acme"}, "description": "Acme tools", "plugins": [
  {"name": "formatter", "source": "./tools/formatter", "description": "Formats code", "version": "1.0.0"},
  {"name": "linter", "source": {"source": "url", "url": "https://lola.test/linter.git", "ref": "v1"}},
  {"name": "npmx", "source": {"source": "npm", "package": "x"}}
]}`

// sshCatalog names ssh sources at two hosts, a at a host named with a port and b in scp form, plus
// one at a private address no one can allow.
const sshCatalog = `{"name": "priv", "owner": {"name": "P"}, "plugins": [
  {"name": "a", "source": {"source": "url", "url": "ssh://git@git.corp.test:2222/a.git"}},
  {"name": "b", "source": {"source": "url", "url": "git@git.corp.test:b.git"}},
  {"name": "c", "source": {"source": "url", "url": "ssh://git@other.test/c.git"}},
  {"name": "d", "source": {"source": "url", "url": "ssh://git@10.0.0.5/d.git"}}]}`

var _ = Describe("marketplaces", func() {
	var (
		tmp, srv, base string
		p              paths.Paths
		e              *engine.Engine
		ctx            = context.Background()
	)
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p, targets.Builtin())
		e.Getwd = func() (string, error) { return tmp, nil }
		srv = filepath.Join(tmp, "srv")
		Expect(os.MkdirAll(srv, 0o755)).To(Succeed())
		// A marketplace's own source is one you type, so a loopback http server may serve it.
		base = testenv.ServeGit(srv)
	})
	// repo commits files to the bare repository <name>.git that base serves, creating it on first
	// use, and returns its URL and the new commit.
	repo := func(name string, files map[string]string) (string, string) {
		GinkgoHelper()
		work := filepath.Join(tmp, "work", name)
		if _, err := os.Stat(work); errors.Is(err, os.ErrNotExist) {
			Expect(os.MkdirAll(work, 0o755)).To(Succeed())
			gitIn(work, "init", "-q", "-b", "main")
		}
		writeTree(work, files)
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "change")
		bare := filepath.Join(srv, name+".git")
		Expect(os.RemoveAll(bare)).To(Succeed())
		gitIn(tmp, "clone", "-q", "--bare", work, bare)
		return base + "/" + name + ".git", gitIn(work, "rev-parse", "HEAD")
	}
	acme := func() map[string]string {
		return map[string]string{".claude-plugin/marketplace.json": acmeCatalog, "tools/formatter/skills/f/SKILL.md": "---\nname: f\n---\nF\n"}
	}
	add := func(req engine.MarketAddRequest) (*engine.MarketResult, error) { return e.AddMarket(ctx, req) }

	It("applies the registration's ssh hosts to the cached catalog on every read", func() {
		url, _ := repo("priv", map[string]string{".claude-plugin/marketplace.json": sshCatalog})
		_, err := add(engine.MarketAddRequest{Name: "priv", Source: url})
		Expect(err).NotTo(HaveOccurred())
		v, err := e.ShowMarket("priv", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(v.Entries).To(BeEmpty())
		b, err := os.ReadFile(p.Markets)
		Expect(err).NotTo(HaveOccurred())
		b, err = market.SetSSHHosts(b, "priv", []string{"git.corp.test"})
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(p.Markets, b, 0o600)).To(Succeed())
		v, err = e.ShowMarket("priv", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(v.Entries).To(HaveLen(2))
	})

	It("reads the catalog the Python lola read when lola migrate names it, and never otherwise", func() {
		const yml = "name: py\nmodules:\n  - name: tool\n    repository: https://example.com/tool.git\n"
		url, _ := repo("single", map[string]string{"catalog.yml": yml, ".pre-commit-config.yaml": "repos: []\n"})
		_, err := add(engine.MarketAddRequest{Name: "single", Source: url})
		Expect(err).To(MatchError(HavePrefix("the source has no catalog at the source's root")), "lola's own lookup is unchanged")
		_, err = add(engine.MarketAddRequest{Name: "single", Source: url, PythonName: "single"})
		Expect(err).NotTo(HaveOccurred(), "the only .yml at the root, as the Python lola reads it")
		rows, _, err := e.Markets(nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(ContainElement(And(HaveField("Name", "single"), HaveField("Path", "catalog.yml"), HaveField("Entries", 1))))

		url, _ = repo("named", map[string]string{"My Market.yml": yml, "marketplace.yml": yml, ".claude-plugin/marketplace.json": `{"name": "x", "plugins": []}`})
		_, err = add(engine.MarketAddRequest{Name: "named", Source: url, PythonName: "My Market"})
		Expect(err).NotTo(HaveOccurred())
		rows, _, err = e.Markets(nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(ContainElement(And(HaveField("Name", "named"), HaveField("Path", "My Market.yml"))), "its name first, never marketplace.json")
		file, err := e.PythonCatalog(ctx, url, "other")
		Expect(err).NotTo(HaveOccurred())
		Expect(file).To(Equal("marketplace.yml"), "then marketplace.yml")

		url, _ = repo("two", map[string]string{"a.yml": yml, "b.yaml": yml})
		_, err = add(engine.MarketAddRequest{Name: "two", Source: url, PythonName: "two"})
		Expect(err).To(MatchError("the source has 2 YAML files at its root, a.yml and b.yaml, and none the Python lola would pick: " +
			"two.yml, two.yaml, marketplace.yml or marketplace.yaml"))
		Expect(errors.Is(err, engine.ErrNoPythonCatalog)).To(BeTrue())
		Expect(errs.Hints(err)).To(BeEmpty(), "only naming the file in the Python lola's registration can tell")
		_, err = e.PythonCatalog(ctx, url, "two")
		Expect(errors.Is(err, engine.ErrNoPythonCatalog)).To(BeTrue())

		marketsBefore, err := os.ReadFile(p.Markets)
		Expect(err).NotTo(HaveOccurred())
		url, _ = repo("lone", map[string]string{"evil\nname: x\x1b[31m.yml": yml})
		_, err = add(engine.MarketAddRequest{Name: "lone", Source: url, PythonName: "lone"})
		Expect(err).To(MatchError("the only YAML file at the source's root has a name lola does not read"))
		Expect(errors.Is(err, engine.ErrNoPythonCatalog)).To(BeTrue())
		Expect(errs.Hints(err)).To(BeEmpty())
		Expect(os.ReadFile(p.Markets)).To(Equal(marketsBefore), "nothing registered")
		_, err = e.PythonCatalog(ctx, url, "lone")
		Expect(errors.Is(err, engine.ErrNoPythonCatalog)).To(BeTrue())
	})

	It("adds a marketplace, lists it, shows its entries and updates it, reporting what changed", func() {
		url, commit := repo("plugins", acme())
		res, err := add(engine.MarketAddRequest{Source: url})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Updates).To(Equal([]engine.MarketOutcome{{Market: "acme", Outcome: engine.MarketAdded, Hints: []string{}}}))
		Expect(res.Markets).To(HaveLen(1))
		Expect(res.Markets[0]).To(And(HaveField("Name", "acme"), HaveField("Source", url), HaveField("Format", "claude"),
			HaveField("Commit", commit), HaveField("Entries", 2), HaveField("Skipped", 1), HaveField("Path", ".claude-plugin/marketplace.json")))
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn, Text: "1 entry skipped: 1 npm source → lola market acme"}))
		Expect(os.ReadFile(p.Markets)).To(Equal([]byte("# lola marketplaces. Safe to edit by hand.\nversion: 1\nmarkets:\n  - name: acme\n" +
			"    source: " + url + "\n    path: .claude-plugin/marketplace.json\n")))
		Expect(os.ReadFile(filepath.Join(p.MarketCache, "acme", "catalog.json"))).To(Equal([]byte(acmeCatalog)), "cached byte for byte")

		rows, notices, err := e.Markets(nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(notices).To(BeEmpty())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].Fetched).To(Equal("just now"))

		v, err := e.ShowMarket("acme", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect([]string{v.File, v.Owner, v.Description}).To(Equal([]string{".claude-plugin/marketplace.json", "Acme", "Acme tools"}))
		Expect(v.Entries).To(HaveLen(2))
		Expect(v.Entries[0]).To(Equal(engine.MarketEntry{Name: "formatter", Description: "Formats code", Version: "1.0.0", Tags: []string{},
			Source: url, Path: "tools/formatter", Commit: commit, Kind: "git", Install: "@acme/formatter", Notices: []string{}}))
		Expect(v.Entries[1]).To(And(HaveField("Source", "https://lola.test/linter.git"), HaveField("Ref", "v1")))
		Expect(v.Skipped).To(Equal([]engine.MarketSkip{{Name: "npmx", Reason: "npm sources are not supported: installing one runs a package manager"}}))

		_, err = e.ShowMarket("nope", nil)
		Expect(err).To(MatchError("you have no marketplace nope"))
		Expect(errs.Hints(err)).To(Equal([]string{"lola market"}))

		changed := strings.Replace(strings.Replace(acmeCatalog, `"1.0.0"`, `"1.1.0"`, 1), `{"name": "npmx", "source": {"source": "npm", "package": "x"}}`,
			`{"name": "review", "source": {"source": "github", "repo": "acme/review"}}`, 1)
		changed = strings.Replace(changed, `{"name": "linter", "source": {"source": "url", "url": "https://lola.test/linter.git", "ref": "v1"}},`, "", 1)
		_, commit2 := repo("plugins", map[string]string{".claude-plugin/marketplace.json": changed})
		dry, err := e.UpdateMarkets(ctx, engine.MarketUpdateRequest{Names: []string{"acme"}, DryRun: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(dry.Updates[0].Outcome).To(Equal(engine.MarketWouldUpdate))
		Expect(os.ReadFile(filepath.Join(p.MarketCache, "acme", "catalog.json"))).To(Equal([]byte(acmeCatalog)), "a dry run writes nothing")

		up, err := e.UpdateMarkets(ctx, engine.MarketUpdateRequest{All: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(up.Updates).To(Equal([]engine.MarketOutcome{{Market: "acme", Outcome: engine.MarketUpdated, Hints: []string{}}}))
		Expect(up.Changes).To(ConsistOf(
			engine.MarketChange{Market: "acme", Entry: "formatter", Change: "changed", Fields: []string{"commit", "version"}},
			engine.MarketChange{Market: "acme", Entry: "review", Change: "added", Fields: []string{}},
			engine.MarketChange{Market: "acme", Entry: "linter", Change: "removed", Fields: []string{}}))
		Expect(up.Markets[0].Commit).To(Equal(commit2))
		again, err := e.UpdateMarkets(ctx, engine.MarketUpdateRequest{Names: []string{"acme"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(again.Updates[0].Outcome).To(Equal(engine.MarketUpToDate))

		_, err = e.UpdateMarkets(ctx, engine.MarketUpdateRequest{Names: []string{"acme", "nope"}})
		Expect(err).To(MatchError("you have no marketplace nope"), "every name is checked before any network contact")
	})

	It("adds a catalog file at a URL and a Python YAML catalog, whose relative entries a single file cannot have", func() {
		yml := "name: general\ndescription: General\nmodules:\n  - {name: gopls, repository: \"https://lola.test/gopls.git\", tags: [go]}\n"
		files := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/general-market.yml":
				_, _ = w.Write([]byte(yml))
			default:
				_, _ = w.Write([]byte(acmeCatalog))
			}
		}))
		DeferCleanup(files.Close)
		res, err := add(engine.MarketAddRequest{Source: files.URL + "/general-market.yml"})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Markets[0]).To(And(HaveField("Name", "general"), HaveField("Format", "lola-yaml"), HaveField("Entries", 1), HaveField("Path", "")))
		Expect(os.ReadFile(filepath.Join(p.MarketCache, "general", "catalog.yml"))).To(Equal([]byte(yml)))

		res, err = add(engine.MarketAddRequest{Name: "single", Source: files.URL + "/marketplace.json"})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Markets[0].Skipped).To(Equal(2))
		v, err := e.ShowMarket("single", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(v.Skipped).To(ContainElement(engine.MarketSkip{Name: "formatter",
			Reason: "a relative source needs the marketplace's files, and this marketplace is a single catalog file"}))
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "the catalog calls itself acme; it is your marketplace single"}))

		_, err = add(engine.MarketAddRequest{Name: "x", Source: files.URL + "/marketplace.json", Ref: "main", Path: "a"})
		Expect(err).To(MatchError("--ref and --path apply to a git repository, archive or folder, and " + files.URL +
			"/marketplace.json is a catalog file"))
		Expect(errs.Hints(err)).To(Equal([]string{"lola market x --add " + files.URL + "/marketplace.json"}), "one refusal, dropping both")
		for _, name := range []string{"%5Cm.json", "%07m.json"} {
			_, err = add(engine.MarketAddRequest{Name: "x", Source: files.URL + "/" + name})
			Expect(err).To(MatchError(HaveSuffix("cannot be recorded as a catalog file name")), name)
		}
	})

	It("market --add and --update ask nothing and write cache before registration", func() {
		if runtime.GOOS == "windows" {
			Skip("directory permissions are POSIX")
		}
		url, _ := repo("plugins", acme())
		config := filepath.Dir(p.Markets)
		Expect(os.MkdirAll(config, 0o700)).To(Succeed())
		Expect(os.Chmod(config, 0o500)).To(Succeed())
		DeferCleanup(func() { _ = os.Chmod(config, 0o700) })
		_, err := add(engine.MarketAddRequest{Source: url})
		Expect(err).To(MatchError(ContainSubstring("could not write " + p.Markets)))
		Expect(filepath.Join(p.MarketCache, "acme", "catalog.json")).To(BeARegularFile(), "the cache is written first")
		rows, _, err := e.Markets(nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(BeEmpty(), "a cache without a registration is no marketplace")

		Expect(os.Chmod(config, 0o700)).To(Succeed())
		res, err := add(engine.MarketAddRequest{Source: url})
		Expect(err).NotTo(HaveOccurred(), "the next --add of the name replaces the cache")
		Expect(res.Updates[0].Outcome).To(Equal(engine.MarketAdded))
		_, err = e.UpdateMarkets(ctx, engine.MarketUpdateRequest{All: true})
		Expect(err).NotTo(HaveOccurred())
	})

	It("market --add of a taken name never coaches an update of another source", func() {
		url, _ := repo("plugins", acme())
		other, _ := repo("others", acme())
		_, err := add(engine.MarketAddRequest{Source: url})
		Expect(err).NotTo(HaveOccurred())

		_, err = add(engine.MarketAddRequest{Name: "acme", Source: url, DryRun: true})
		Expect(err).To(MatchError("acme is already this marketplace"))
		Expect(errs.Hints(err)).To(Equal([]string{"lola market acme --update -n"}))
		_, err = add(engine.MarketAddRequest{Source: url})
		Expect(err).To(MatchError("acme is already this marketplace"), "the catalog's own name, once fetched")

		_, err = add(engine.MarketAddRequest{Name: "acme", Source: other, Ref: "main"})
		Expect(err).To(MatchError("acme is already a marketplace, from " + url))
		Expect(errs.Hints(err)).To(Equal([]string{"lola market acme-2 --add " + other + " --ref main --path .claude-plugin/marketplace.json"}),
			"the hint names the catalog file found, so another name looks for no other")

		var steps []engine.MarketRename
		answers := []string{"acme", "Bad Name", "beta"}
		res, err := add(engine.MarketAddRequest{Source: other, Rename: func(st engine.MarketRename) (string, error) {
			steps = append(steps, st)
			a := answers[0]
			answers = answers[1:]
			return a, nil
		}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Markets[0].Name).To(Equal("beta"))
		Expect(steps).To(HaveLen(3))
		Expect(steps[0]).To(Equal(engine.MarketRename{Why: "acme is already a marketplace, from " + url, Suggest: "acme-2"}))
		Expect(steps[1].Problem).To(Equal("acme is already a marketplace, from " + url))
		Expect(steps[2].Problem).To(ContainSubstring(`marketplace name "Bad Name" is invalid`))

		// The catalog is found by the name typed; a free name's hint names the file found.
		general, _ := repo("general", map[string]string{"acme.yml": "name: general\nmodules: []\n"})
		_, err = add(engine.MarketAddRequest{Name: "acme", Source: general})
		Expect(err).To(MatchError("acme is already a marketplace, from " + url))
		Expect(errs.Hints(err)).To(Equal([]string{"lola market acme-2 --add " + general + " --path acme.yml"}))
		res, err = add(engine.MarketAddRequest{Name: "acme-2", Source: general, Path: "acme.yml"})
		Expect(err).NotTo(HaveOccurred(), "the hint runs")
		Expect(res.Markets[0].Path).To(Equal("acme.yml"))

		// One source and ref at another catalog file is another marketplace (source, ref and path).
		nested, _ := repo("nested", map[string]string{"sub/.claude-plugin/marketplace.json": acmeCatalog, "marketplace.yml": "name: root\nmodules: []\n"})
		_, err = add(engine.MarketAddRequest{Name: "nest", Source: nested, Path: "sub"})
		Expect(err).NotTo(HaveOccurred())
		for _, typed := range []string{"sub", "sub/.claude-plugin/marketplace.json"} {
			_, err = add(engine.MarketAddRequest{Name: "nest", Source: nested, Path: typed})
			Expect(err).To(MatchError("nest is already this marketplace"), typed)
			Expect(errs.Hints(err)).To(Equal([]string{"lola market nest --update"}), typed)
		}
		for _, typed := range []string{"", "."} {
			_, err = add(engine.MarketAddRequest{Name: "nest", Source: nested, Path: typed})
			Expect(err).To(MatchError("nest is already a marketplace, from "+nested+" at sub/.claude-plugin/marketplace.json"), typed)
			Expect(errs.Hints(err)).To(Equal([]string{"lola market nest-2 --add " + nested + " --path marketplace.yml"}), typed)
		}
		res, err = add(engine.MarketAddRequest{Name: "nest-2", Source: nested, Path: "marketplace.yml"})
		Expect(err).NotTo(HaveOccurred(), "the hint runs")
		Expect(res.Markets[0].Path).To(Equal("marketplace.yml"))
	})

	It("--add asks a name for a catalog that names itself what no marketplace can be, pre-filled with its slug", func() {
		url, _ := repo("general", map[string]string{"marketplace.yml": "name: Lola General Marketplace\nmodules: []\n"})
		var steps []engine.MarketRename
		answers := []string{"Bad Name", "general"}
		ask := func(st engine.MarketRename) (string, error) {
			steps = append(steps, st)
			a := answers[0]
			answers = answers[1:]
			return a, nil
		}
		res, err := add(engine.MarketAddRequest{Source: url, Rename: ask})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Markets[0].Name).To(Equal("general"))
		Expect(steps).To(HaveLen(2))
		Expect(steps[0]).To(Equal(engine.MarketRename{Why: "the catalog names itself Lola General Marketplace, which cannot name a marketplace",
			Suggest: "lola-general-marketplace"}))
		Expect(steps[1].Problem).To(HavePrefix(`marketplace name "Bad Name" is invalid: B and N are uppercase, ' ' is not allowed; use`))
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo,
			Text: "the catalog calls itself Lola General Marketplace; it is your marketplace general"}))

		other, _ := repo("other", map[string]string{"marketplace.yml": "name: Lola General Marketplace\nmodules: []\n"})
		answers, steps = []string{"general", "lola-general-marketplace"}, nil
		_, err = add(engine.MarketAddRequest{Source: other, Rename: ask})
		Expect(err).NotTo(HaveOccurred())
		Expect(steps[1].Problem).To(Equal("general is already a marketplace, from " + url))

		third, _ := repo("third", map[string]string{"marketplace.yml": "name: Lola General Marketplace\nmodules: []\n"})
		_, err = add(engine.MarketAddRequest{Source: third, Rename: ask, DryRun: true})
		Expect(err).To(MatchError("the catalog names itself Lola General Marketplace, which cannot name a marketplace; give a name before --add"),
			"a dry run asks nothing")
		Expect(errs.Hints(err)).To(Equal([]string{"lola market lola-general-marketplace-2 --add " + third + " --path marketplace.yml -n"}))
	})

	It("--add of a name lola refuses says what is wrong with it, hinting its lowercase form when that is a name", func() {
		url, _ := repo("plugins", acme())
		_, err := add(engine.MarketAddRequest{Name: "Acme_Tools", Source: url, Ref: "main", DryRun: true, Display: []string{"--json"}})
		Expect(err).To(MatchError(`marketplace name "Acme_Tools" is invalid: A and T are uppercase; use 1-64 characters from a-z, 0-9, '.', '_' ` +
			`and '-', starting with a letter or digit, with no '..'`))
		Expect(errs.Hints(err)).To(Equal([]string{"lola market acme_tools --add " + url + " --ref main -n --json"}))
		_, err = add(engine.MarketAddRequest{Name: "acme_tools", Source: url})
		Expect(err).NotTo(HaveOccurred())
		for _, name := range []string{"My Tools", "Add", "Acme_Tools"} { // "my tools" is no name, add is reserved, acme_tools is taken
			_, err = add(engine.MarketAddRequest{Name: name, Source: url})
			Expect(err).To(HaveOccurred(), name)
			Expect(errs.Hints(err)).To(BeEmpty(), name)
		}
	})

	It("--add whose name looks like a source (--add <name> <source> typed for --add <source>) hints the order that works", func() {
		_, err := add(engine.MarketAddRequest{Name: "https://github.com/lolables/market", Source: "lolables", Ref: "main", DryRun: true, Display: []string{"--json"}})
		Expect(err).To(MatchError(ContainSubstring(`marketplace name "https://github.com/lolables/market" is invalid`)))
		Expect(errs.Hints(err)).To(Equal([]string{"lola market lolables --add https://github.com/lolables/market --ref main -n --json"}))
		_, err = add(engine.MarketAddRequest{Name: "git@host:acme/market", Source: "acme"})
		Expect(errs.Hints(err)).To(Equal([]string{"lola market acme --add git@host:acme/market"}))
		_, err = add(engine.MarketAddRequest{Name: "a b", Source: "x"})
		Expect(errs.Hints(err)).To(BeEmpty(), "a name without ':' or '/' is not a source")
	})

	It("names the marketplaces close to one you do not have, hinting the run with each", func() {
		url, _ := repo("plugins", acme())
		_, err := add(engine.MarketAddRequest{Source: url})
		Expect(err).NotTo(HaveOccurred())
		_, err = e.ShowMarket("acmee", []string{"--plain"})
		Expect(err).To(MatchError("you have no marketplace acmee; close names: acme"))
		Expect(errs.Hints(err)).To(Equal([]string{"lola market acme --plain", "lola market --plain"}))
		_, err = e.ShowMarket("ACME", nil)
		Expect(errs.Hints(err)).To(Equal([]string{"lola market acme", "lola market"}), "compared without case")
		_, err = e.ShowMarket("zzz", nil)
		Expect(err).To(MatchError("you have no marketplace zzz"))
		Expect(errs.Hints(err)).To(Equal([]string{"lola market"}))

		_, err = e.UpdateMarkets(ctx, engine.MarketUpdateRequest{Names: []string{"acme", "acmee"}, DryRun: true})
		Expect(err).To(MatchError("you have no marketplace acmee; close names: acme"))
		Expect(errs.Hints(err)).To(Equal([]string{"lola market acme --update -n", "lola market"}), "the typo's close name in its place, once")
		_, err = e.RemoveMarket(engine.MarketRemoveRequest{Name: "acmee", Confirm: func(engine.MarketRow) (bool, error) { return true, nil }})
		Expect(err).To(MatchError("you have no marketplace acmee; close names: acme"))
		Expect(errs.Hints(err)).To(Equal([]string{"lola market acme --remove", "lola market"}), "never with -y, which answered another question")
		_, err = e.AddEntry(ctx, "@acmee/formatter", "", "", nil)
		Expect(err).To(MatchError("you have no marketplace acmee; close names: acme"))
		Expect(errs.Hints(err)).To(Equal([]string{"lola market"}))
	})

	It("--add refuses an unusable catalog name or two catalog files with --add hints keeping --ref, --path and -n", func() {
		url, _ := repo("general", map[string]string{
			"sub/marketplace.yml":                   "name: Lola General Marketplace\nmodules: []\n",
			"two/.claude-plugin/marketplace.json":   acmeCatalog,
			"two/marketplace.yml":                   "name: two\nmodules: []\n",
			"two/tools/formatter/skills/f/SKILL.md": "F",
		})
		_, err := add(engine.MarketAddRequest{Source: url, Ref: "main", Path: "sub", DryRun: true})
		Expect(err).To(MatchError("the catalog names itself Lola General Marketplace, which cannot name a marketplace; give a name before --add"))
		Expect(errs.Hints(err)).To(Equal([]string{"lola market lola-general-marketplace --add " + url + " --ref main --path sub/marketplace.yml -n"}))

		// A slug that names a file of its own (<slug>.yml) would find two catalogs without --path.
		slugged := filepath.Join(tmp, "slugged")
		writeTree(slugged, map[string]string{"marketplace.yml": "name: Lola General Marketplace\nmodules: []\n",
			"lola-general-marketplace.yml": "name: other\nmodules: []\n"})
		_, err = add(engine.MarketAddRequest{Source: slugged})
		Expect(errs.Hints(err)).To(Equal([]string{"lola market lola-general-marketplace --add " + slugged + " --path marketplace.yml"}))
		res, err := add(engine.MarketAddRequest{Name: "lola-general-marketplace", Source: slugged, Path: "marketplace.yml"})
		Expect(err).NotTo(HaveOccurred(), "the hint runs")
		Expect(res.Markets[0].Path).To(Equal("marketplace.yml"))
		_, err = add(engine.MarketAddRequest{Source: slugged})
		Expect(err).To(MatchError("lola-general-marketplace is already this marketplace"), "its slug is this marketplace, never a second one")
		Expect(errs.Hints(err)).To(Equal([]string{"lola market lola-general-marketplace --update"}))
		_, err = add(engine.MarketAddRequest{Source: url, Path: "sub"})
		Expect(errs.Hints(err)).To(Equal([]string{"lola market lola-general-marketplace-2 --add " + url + " --path sub/marketplace.yml"}),
			"another source's slug is taken, so a free one is coached")

		_, err = add(engine.MarketAddRequest{Source: url, Ref: "main", Path: "two", DryRun: true})
		Expect(err).To(MatchError("the source has 2 catalogs under --path two, two/.claude-plugin/marketplace.json and two/marketplace.yml; " +
			"name the one to read with --path"))
		Expect(errs.Hints(err)).To(Equal([]string{"lola market --add " + url + " --ref main --path two/.claude-plugin/marketplace.json -n",
			"lola market --add " + url + " --ref main --path two/marketplace.yml -n"}))

		_, err = add(engine.MarketAddRequest{Name: "x", Source: url})
		Expect(err).To(MatchError("the source has no catalog at the source's root: lola looked for .claude-plugin/marketplace.json, x.yml, x.yaml, " +
			"marketplace.yml, marketplace.yaml, lola.yml, lola.yaml"))
		Expect(errs.Hints(err)).To(BeEmpty())
		_, err = add(engine.MarketAddRequest{Name: "x", Source: url, Path: "nope"})
		Expect(err).To(MatchError("--path nope is not in the source"))

		Expect(os.MkdirAll(filepath.Dir(p.Markets), 0o700)).To(Succeed())
		Expect(os.WriteFile(p.Markets, []byte("version: 1\nmarkets:\n  - {name: two, source: \""+url+"\", path: two}\n"), 0o600)).To(Succeed())
		res, err = e.UpdateMarkets(ctx, engine.MarketUpdateRequest{Names: []string{"two"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Updates[0].Error).To(Equal("the source has 2 catalogs under path two, two/.claude-plugin/marketplace.json and " +
			"two/marketplace.yml; set path: in markets.yaml to the one to read"))
		Expect(res.Updates[0].Hints).To(BeEmpty())
	})

	It("reads the original lola's lola.yml only when no other catalog is at the root", func() {
		const lolaYML = "name: M\nmodules:\n  - {name: a, repository: \"https://example.com/a.git\", version: main}\n"
		only := filepath.Join(tmp, "only")
		writeTree(only, map[string]string{"lola.yml": lolaYML})
		_, err := add(engine.MarketAddRequest{Name: "m", Source: only})
		Expect(err).NotTo(HaveOccurred())
		v, err := e.ShowMarket("m", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(v.File).To(Equal("lola.yml"))
		Expect(v.Entries).To(HaveLen(1))
		Expect(v.Entries[0].Name).To(Equal("a"))

		both := filepath.Join(tmp, "both")
		writeTree(both, map[string]string{".claude-plugin/marketplace.json": acmeCatalog, "lola.yml": lolaYML})
		_, err = add(engine.MarketAddRequest{Source: both})
		Expect(err).NotTo(HaveOccurred())
		v, err = e.ShowMarket("acme", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(v.File).To(Equal(".claude-plugin/marketplace.json"))

		broken := filepath.Join(tmp, "broken")
		writeTree(broken, map[string]string{"lola.yml": "modules: ["})
		_, err = add(engine.MarketAddRequest{Name: "broken", Source: broken})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("lola.yml"))

		empty := filepath.Join(tmp, "empty")
		writeTree(empty, map[string]string{"README.md": "x"})
		_, err = add(engine.MarketAddRequest{Name: "x", Source: empty})
		Expect(err).To(MatchError(ContainSubstring("marketplace.yml, marketplace.yaml, lola.yml, lola.yaml")))
	})

	It("a relative entry cannot leave the marketplace tree", func() {
		if runtime.GOOS == "windows" {
			Skip("symbolic links need privileges on Windows")
		}
		folder := filepath.Join(tmp, "market")
		writeTree(folder, map[string]string{".claude-plugin/marketplace.json": `{"name": "m", "owner": {"name": "M"}, "plugins": [
  {"name": "here", "source": "./here"}, {"name": "gone", "source": "./gone"}, {"name": "up", "source": "./../x"}]}`, "here/x.md": "x"})
		res, err := add(engine.MarketAddRequest{Source: folder})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Markets[0].Entries).To(Equal(1))
		v, err := e.ShowMarket("m", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(v.Skipped).To(ConsistOf(engine.MarketSkip{Name: "up", Reason: "source ./../x leaves the marketplace (..)"},
			engine.MarketSkip{Name: "gone", Reason: "gone is not in the marketplace"}))

		elsewhere := filepath.Join(tmp, "elsewhere")
		writeTree(elsewhere, map[string]string{"x.md": "x", "marketplace.json": `{"name": "m", "owner": {"name": "M"}, "plugins": []}`})
		inside := filepath.Join(tmp, "inside")
		writeTree(inside, map[string]string{".claude-plugin/marketplace.json": `{"name": "i", "owner": {"name": "M"}, "plugins": [{"name": "l", "source": "./l"}]}`})
		Expect(os.Symlink(elsewhere, filepath.Join(inside, "l"))).To(Succeed())
		linkedDir := filepath.Join(tmp, "linkeddir")
		Expect(os.MkdirAll(linkedDir, 0o755)).To(Succeed())
		Expect(os.Symlink(elsewhere, filepath.Join(linkedDir, ".claude-plugin"))).To(Succeed())
		linkedFile := filepath.Join(tmp, "linkedfile")
		Expect(os.MkdirAll(filepath.Join(linkedFile, ".claude-plugin"), 0o755)).To(Succeed())
		Expect(os.Symlink(filepath.Join(elsewhere, "marketplace.json"), filepath.Join(linkedFile, ".claude-plugin", "marketplace.json"))).To(Succeed())
		for _, dir := range []string{inside, linkedDir, linkedFile} {
			_, err := add(engine.MarketAddRequest{Name: "linked", Source: dir})
			Expect(err).To(MatchError(ContainSubstring("symbolic link refused")), dir)
		}
		rows, _, err := e.Markets(nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(1))
	})

	It("a catalog skills list selects the module's skills from the marketplace's own tree", func() {
		if runtime.GOOS == "windows" {
			Skip("symbolic links need privileges on Windows")
		}
		skill := func(n string) string { return "---\nname: " + n + "\ndescription: " + n + "\n---\n" + n + "\n" }
		folder := filepath.Join(tmp, "skills-market")
		writeTree(folder, map[string]string{".claude-plugin/marketplace.json": `{"name": "skills", "owner": {"name": "A"}, "plugins": [
  {"name": "document-skills", "source": "./", "strict": false, "skills": ["./skills/xlsx", "./skills/docx"]},
  {"name": "sheets", "source": "./", "strict": false, "skills": ["./skills/xlsx"], "commands": ["./commands/sum.md"]},
  {"name": "flat", "source": "./", "skills": ["./skills/notes.md"]}]}`,
			"skills/xlsx/SKILL.md": skill("xlsx"), "skills/notes.md": "x", "commands/sum.md": "Sum\n"})
		_, err := add(engine.MarketAddRequest{Source: folder})
		Expect(err).NotTo(HaveOccurred())
		v, err := e.ShowMarket("skills", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(v.Skipped).To(ConsistOf(engine.MarketSkip{Name: "document-skills", Reason: "skills/docx is not a directory in the marketplace"},
			engine.MarketSkip{Name: "flat", Reason: "skills/notes.md is not a directory in the marketplace"}))
		Expect(v.Entries).To(HaveLen(1))
		Expect(v.Entries[0].Components).To(Equal(&market.Components{Skills: []string{"skills/xlsx"}, Commands: []string{"commands/sum.md"}, Agents: []string{}}))

		// Installed from a git marketplace, the module is exactly what the lists select, and the
		// entry records the selection, so a fresh clone without the marketplace gets the same.
		url, _ := repo("gskills", map[string]string{".claude-plugin/marketplace.json": `{"name": "gskills", "owner": {"name": "A"}, "plugins": [
  {"name": "sheets", "source": "./", "strict": false, "skills": ["./skills/xlsx"], "commands": ["./commands/sum.md"]}]}`,
			"skills/xlsx/SKILL.md": skill("xlsx"), "skills/pptx/SKILL.md": skill("pptx"), "commands/sum.md": "Sum\n", "commands/other.md": "Other\n"})
		_, err = add(engine.MarketAddRequest{Source: url})
		Expect(err).NotTo(HaveOccurred())
		proj := filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		sc := engine.Scope{Kind: target.Project, ProjectDir: proj}
		_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: "@gskills/sheets"}}, engine.Options{Targets: []string{"claude-code"}})
		Expect(err).NotTo(HaveOccurred())
		exactly := func(root string) {
			GinkgoHelper()
			Expect(filepath.Join(root, ".claude", "skills", "xlsx", "SKILL.md")).To(BeARegularFile())
			Expect(filepath.Join(root, ".claude", "commands", "sum.md")).To(BeARegularFile())
			Expect(filepath.Join(root, ".claude", "skills", "pptx")).NotTo(BeAnExistingFile())
			Expect(filepath.Join(root, ".claude", "commands", "other.md")).NotTo(BeAnExistingFile())
		}
		exactly(proj)
		m, err := e.Manifest(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Find("sheets").Components).To(Equal(&source.Components{Skills: []string{"skills/xlsx"}, Commands: []string{"commands/sum.md"}}))
		led, err := e.Ledger(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Modules["sheets"].Components).To(Equal(m.Find("sheets").Components))
		b, err := os.ReadFile(filepath.Join(proj, ".lola", "project.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(ContainSubstring("    components:\n      skills: [skills/xlsx]\n      commands: [commands/sum.md]\n"))

		teammate := filepath.Join(tmp, "teammate")
		Expect(os.MkdirAll(filepath.Join(teammate, ".lola"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(teammate, ".lola", "project.yml"), b, 0o644)).To(Succeed())
		for _, dir := range []string{p.Markets, p.MarketCache, p.Store} {
			Expect(os.RemoveAll(dir)).To(Succeed())
		}
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: engine.Scope{Kind: target.Project, ProjectDir: teammate},
			Trust: func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }})
		Expect(err).NotTo(HaveOccurred())
		exactly(teammate)
		_, err = add(engine.MarketAddRequest{Source: folder})
		Expect(err).NotTo(HaveOccurred())

		Expect(os.Symlink(filepath.Join(folder, "skills", "xlsx"), filepath.Join(folder, "skills", "docx"))).To(Succeed())
		res, err := e.UpdateMarkets(ctx, engine.MarketUpdateRequest{Names: []string{"skills"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Updates[0].Error).To(ContainSubstring("skills/docx: symbolic link refused: it points to"), "an absolute link refuses the fetch")
	})

	It("installs, syncs, updates and trusts a selection of components as its entry records it", func() {
		proj := filepath.Join(tmp, "proj")
		sc := engine.Scope{Kind: target.Project, ProjectDir: proj}
		skill := func(n string) string { return "---\nname: " + n + "\ndescription: " + n + "\n---\n" + n + "\n" }
		mine := filepath.Join(proj, "mine")
		writeTree(mine, map[string]string{".claude-plugin/marketplace.json": `{"name": "mine", "owner": {"name": "M"}, "plugins": [
  {"name": "sel", "source": "./", "skills": ["./skills/a"]}]}`, "skills/a/SKILL.md": skill("a"), "skills/b/SKILL.md": skill("b")})
		_, err := add(engine.MarketAddRequest{Source: mine})
		Expect(err).NotTo(HaveOccurred())
		claude := engine.Options{Targets: []string{"claude-code"}}
		_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: "@mine/sel"}}, claude)
		Expect(err).NotTo(HaveOccurred())
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: claude})
		Expect(err).NotTo(HaveOccurred(), "sync stages the selection, not the folder, so nothing changed asks for trust")

		writeTree(mine, map[string]string{"skills/a/SKILL.md": skill("a2")})
		var changed []engine.TrustReport
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: claude, Trust: func(r engine.TrustReport) (engine.TrustAnswer, error) {
			changed = append(changed, r)
			return engine.TrustAnswer{}, engine.ErrDeclined
		}})
		Expect(err).To(HaveOccurred())
		Expect(changed).To(HaveLen(1))
		Expect(changed[0].Changes).To(ConsistOf(And(HaveField("Module", "sel"), HaveField("FolderContent", true), HaveField("InstallOwn", ""))),
			"no install by source, which would take the whole folder")

		_, err = e.Update(ctx, []string{"sel"})
		Expect(err).NotTo(HaveOccurred())
		v, _, err := e.Current("sel")
		Expect(err).NotTo(HaveOccurred())
		Expect(v.Module.Skills).To(HaveLen(1), "cache update fetches the selection again")
		_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: "sel"}}, claude)
		Expect(err).NotTo(HaveOccurred())
		m, err := e.Manifest(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Find("sel").Components).To(Equal(&source.Components{Skills: []string{"skills/a"}}), "an install by cached name keeps the selection")

		// A pulled change of the selection: the cache keeps an origin for each selection, so it is
		// a trust question of its own.
		b, err := os.ReadFile(filepath.Join(proj, ".lola", "project.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(proj, ".lola", "project.yml"), []byte(strings.Replace(string(b), "skills: [skills/a]", "skills: [skills/b]", 1)),
			0o644)).To(Succeed())
		var asked []engine.TrustReport
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: func(r engine.TrustReport) (engine.TrustAnswer, error) {
			asked = append(asked, r)
			return engine.TrustAnswer{}, engine.ErrDeclined
		}})
		Expect(err).To(HaveOccurred())
		Expect(asked).To(HaveLen(1))
		Expect(asked[0].Changes).To(ContainElement(HaveField("Fields", ContainElement(HaveField("Field", "components")))))
	})

	It("hints the install, cache add or check of a renamed module's own source only when the command takes what the catalog lists", func() {
		folder := filepath.Join(tmp, "renamed")
		writeTree(folder, map[string]string{".claude-plugin/marketplace.json": `{"name": "ren", "owner": {"name": "R"}, "plugins": [
  {"name": "root", "source": "./"}, {"name": "sub", "source": "./sub"}]}`, "lola.yml": "name: other\n", "commands/o.md": "O\n",
			"sub/lola.yml": "name: subby\n", "sub/commands/s.md": "S\n"})
		_, err := add(engine.MarketAddRequest{Source: folder})
		Expect(err).NotTo(HaveOccurred())
		sc := engine.Scope{Kind: target.User}
		_, err = e.Check(ctx, sc, tmp, []string{"@ren/root"}, nil, []string{"--json"})
		Expect(errs.Hints(err)).To(Equal([]string{"lola check " + folder + " --json"}))
		_, err = e.Check(ctx, sc, tmp, []string{"@ren/sub"}, nil, nil)
		Expect(err).To(MatchError(ContainSubstring("names itself subby")))
		Expect(errs.Hints(err)).To(BeEmpty(), "lola check takes no --path")
		_, err = e.AddEntry(ctx, "@ren/sub", "", "", nil)
		Expect(errs.Hints(err)).To(Equal([]string{"lola cache add " + folder + " --path sub"}))
	})

	It("nothing in a catalog runs a command", func() {
		marker := filepath.Join(tmp, "MARKER")
		catalog := `{"name": "m", "owner": {"name": "M"}, "plugins": [
  {"name": "c", "source": {"source": "command", "command": "touch ` + marker + `"}},
  {"name": "h", "source": "./h", "headersHelper": "touch ` + marker + `"}]}`
		// The Python entry's module, at a host by name, as a catalog may name only such a host.
		hooked := filepath.Join(tmp, "hooked")
		writeTree(hooked, map[string]string{"commands/h.md": "H\n"})
		gitIn(hooked, "init", "-q", "-b", "main")
		gitIn(hooked, "add", "-A")
		gitIn(hooked, "commit", "-q", "-m", "init")
		gitIn(tmp, "clone", "-q", "--bare", hooked, filepath.Join(tmp, "mods", "h.git"))
		mods := testenv.ServeGitTLS(filepath.Join(tmp, "mods"), nil)
		url, _ := repo("cmds", map[string]string{".claude-plugin/marketplace.json": catalog, "h/x.md": "x",
			"py/marketplace.yml": "name: py\nmodules:\n  - name: hooked\n    repository: " + mods + "/h.git\n" +
				"    hooks: {pre-install: touch " + marker + ", post-install: touch " + marker + "}\n"})
		res, err := add(engine.MarketAddRequest{Source: url})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Markets[0].Skipped).To(Equal(2))
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
			Text: "2 entries skipped: 1 command source, 1 entry with headersHelper → lola market m"}))
		res, err = add(engine.MarketAddRequest{Source: url, Path: "py"})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Markets[0].Entries).To(Equal(1))
		v, err := e.ShowMarket("py", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(v.Entries[0].Notices).To(Equal([]string{"catalog install scripts are ignored"}))
		_, err = e.UpdateMarkets(ctx, engine.MarketUpdateRequest{All: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(marker).NotTo(BeAnExistingFile())

		proj := filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		installed, err := e.Install(ctx, engine.Scope{Kind: target.Project, ProjectDir: proj}, proj, []engine.InstallRequest{{Arg: "@py/hooked"}},
			engine.Options{Targets: []string{"claude-code"}, Trust: engine.TrustHooks})
		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.Join(proj, ".claude", "commands", "h.md")).To(BeARegularFile())
		Expect(installed.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "hooked: catalog install scripts are ignored"}))
		Expect(marker).NotTo(BeAnExistingFile(), "the catalog's pre- and post-install scripts never ran")
	})

	It("shows entries sorted by name, and an --update reports the skipped entries only when they change", func() {
		unsorted := `{"name": "acme", "owner": {"name": "Acme"}, "plugins": [
  {"name": "zeta", "source": {"source": "url", "url": "https://lola.test/zeta.git"}},
  {"name": "npmz", "source": {"source": "npm", "package": "z"}},
  {"name": "alpha", "source": {"source": "url", "url": "https://lola.test/alpha.git"}},
  {"name": "npma", "source": {"source": "npm", "package": "a"}},
  {"name": "mid", "source": {"source": "url", "url": "https://lola.test/mid.git"}}
]}`
		url, _ := repo("plugins", map[string]string{".claude-plugin/marketplace.json": unsorted})
		res, err := add(engine.MarketAddRequest{Source: url})
		Expect(err).NotTo(HaveOccurred())
		summary := engine.Notice{Level: engine.LevelWarn, Text: "2 entries skipped: 2 npm sources → lola market acme"}
		Expect(res.Notices).To(ContainElement(summary), "an add reports what it skips")

		v, err := e.ShowMarket("acme", nil)
		Expect(err).NotTo(HaveOccurred())
		var names []string
		for _, en := range v.Entries {
			names = append(names, en.Name)
		}
		Expect(names).To(Equal([]string{"alpha", "mid", "zeta"}))
		Expect(v.Skipped).To(Equal([]engine.MarketSkip{
			{Name: "npma", Reason: "npm sources are not supported: installing one runs a package manager"},
			{Name: "npmz", Reason: "npm sources are not supported: installing one runs a package manager"}}))

		up, err := e.UpdateMarkets(ctx, engine.MarketUpdateRequest{Names: []string{"acme"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(up.Updates[0].Outcome).To(Equal(engine.MarketUpToDate))
		Expect(up.Notices).To(BeEmpty(), "the same skips are not reported again")
		repo("plugins", map[string]string{".claude-plugin/marketplace.json": strings.Replace(unsorted, `"zeta"`, `"zed"`, 1)})
		up, err = e.UpdateMarkets(ctx, engine.MarketUpdateRequest{Names: []string{"acme"}, DryRun: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(up.Notices).To(BeEmpty(), "an entry change is not a skip change")

		repo("plugins", map[string]string{".claude-plugin/marketplace.json": strings.Replace(unsorted, `"npmz"`, `"npmy"`, 1)})
		up, err = e.UpdateMarkets(ctx, engine.MarketUpdateRequest{Names: []string{"acme"}, DryRun: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(up.Notices).To(Equal([]engine.Notice{{Level: engine.LevelWarn, Text: "2 entries skipped: 2 npm sources"}}), "a dry run lists none")
		up, err = e.UpdateMarkets(ctx, engine.MarketUpdateRequest{All: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(up.Notices).To(Equal([]engine.Notice{summary}), "the skips changed")
		up, err = e.UpdateMarkets(ctx, engine.MarketUpdateRequest{All: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(up.Notices).To(BeEmpty())
	})

	It("a marketplace or module git may not read says it is missing or private, hinting git's own check", func() {
		missing := base + "/missing.git"
		why := missing + " was not found, or it is private and the credentials git has, if any, cannot read it"
		_, err := add(engine.MarketAddRequest{Source: missing})
		Expect(err).To(MatchError(why))
		Expect(errs.Hints(err)).To(Equal([]string{"git ls-remote -- " + missing}))

		Expect(os.MkdirAll(filepath.Dir(p.Markets), 0o700)).To(Succeed())
		Expect(os.WriteFile(p.Markets, []byte("version: 1\nmarkets:\n  - {name: gone, source: \""+missing+"\"}\n"), 0o600)).To(Succeed())
		res, err := e.UpdateMarkets(ctx, engine.MarketUpdateRequest{Names: []string{"gone"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Updates[0]).To(And(HaveField("Error", why), HaveField("Hints", []string{"git ls-remote -- " + missing})))

		_, err = e.AddSource(ctx, tmp, missing, "", "", nil)
		Expect(err).To(MatchError(why), "a module's fetch says the same")
		Expect(errs.Hints(err)).To(Equal([]string{"git ls-remote -- " + missing}))
	})

	It("a catalog that fails to parse at --update keeps the cached one", func() {
		url, _ := repo("plugins", acme())
		_, err := add(engine.MarketAddRequest{Source: url})
		Expect(err).NotTo(HaveOccurred())
		repo("plugins", map[string]string{".claude-plugin/marketplace.json": `{"name": "acme", "owner": {"name": "A"}}`})
		res, err := e.UpdateMarkets(ctx, engine.MarketUpdateRequest{Names: []string{"acme"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Updates).To(HaveLen(1))
		Expect(res.Updates[0]).To(And(HaveField("Outcome", engine.MarketFailed),
			HaveField("Error", "the catalog .claude-plugin/marketplace.json has no plugins")))
		v, err := e.ShowMarket("acme", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(v.Entries).To(HaveLen(2), "the cached catalog is kept")
	})

	It("a folder marketplace is recorded absolute and its edits need --update", func() {
		if runtime.GOOS == "windows" {
			Skip("symbolic links need privileges on Windows")
		}
		folder := filepath.Join(tmp, "authoring")
		writeTree(folder, acme())
		Expect(os.Symlink(folder, filepath.Join(tmp, "via"))).To(Succeed())
		res, err := add(engine.MarketAddRequest{Source: "via"})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Markets[0].Source).To(Equal(folder), "absolute, links resolved")
		v, err := e.ShowMarket("acme", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(v.Entries[0]).To(And(HaveField("Source", folder), HaveField("Path", "tools/formatter"), HaveField("Kind", "folder"),
			HaveField("Commit", "")))

		writeTree(folder, map[string]string{".claude-plugin/marketplace.json": strings.Replace(acmeCatalog, "Formats code", "Formats more", 1)})
		v, err = e.ShowMarket("acme", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(v.Entries[0].Description).To(Equal("Formats code"), "the cache, until --update")
		_, err = e.UpdateMarkets(ctx, engine.MarketUpdateRequest{Names: []string{"acme"}})
		Expect(err).NotTo(HaveOccurred())
		v, err = e.ShowMarket("acme", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(v.Entries[0].Description).To(Equal("Formats more"))

		Expect(os.WriteFile(p.Markets, []byte("version: 1\nmarkets:\n  - {name: rel, source: ./authoring}\n"), 0o600)).To(Succeed())
		_, _, err = e.Markets(nil)
		Expect(err).To(MatchError(p.Markets + ":3: marketplace rel: source ./authoring is a relative path; record the folder's absolute path"))
	})

	It("a dry run writes nothing, not even a lock file", func() {
		url, _ := repo("plugins", acme())
		res, err := add(engine.MarketAddRequest{Source: url, DryRun: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Updates[0].Outcome).To(Equal(engine.MarketWouldAdd))
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn, Text: "1 entry skipped: 1 npm source"}),
			"no command to list them: the marketplace is not registered")
		for _, f := range []string{p.Markets, p.MarketCache, p.MarketsLock} {
			Expect(f).NotTo(BeAnExistingFile())
		}
	})

	It("refuses a local catalog file or archive, coaching the folder that holds it", func() {
		writeTree(filepath.Join(tmp, "m"), map[string]string{".claude-plugin/marketplace.json": acmeCatalog, "general.yml": "name: g\nmodules: []\n"})
		_, err := add(engine.MarketAddRequest{Source: "m/.claude-plugin/marketplace.json", DryRun: true})
		Expect(err).To(MatchError("m/.claude-plugin/marketplace.json is a catalog file on this machine; lola reads a local marketplace from the folder that holds it"))
		Expect(errs.Hints(err)).To(Equal([]string{"lola market --add " + filepath.Join(tmp, "m") + " --path .claude-plugin/marketplace.json -n"}))
		_, err = add(engine.MarketAddRequest{Name: "g", Source: filepath.Join(tmp, "m", "general.yml")})
		Expect(errs.Hints(err)).To(Equal([]string{"lola market g --add " + filepath.Join(tmp, "m") + " --path general.yml"}))
		Expect(os.WriteFile(filepath.Join(tmp, "m.tar.gz"), nil, 0o644)).To(Succeed())
		_, err = add(engine.MarketAddRequest{Source: "m.tar.gz"})
		Expect(err).To(MatchError(ContainSubstring("is an archive on this machine; lola reads a local marketplace from a folder")))
		_, err = add(engine.MarketAddRequest{Name: "g", Source: filepath.Join(tmp, "m", "general.yml"), Ref: "v1"})
		Expect(errs.Hints(err)).To(Equal([]string{"lola market g --add " + filepath.Join(tmp, "m") + " --path general.yml"}), "a folder takes no --ref")
		_, err = add(engine.MarketAddRequest{Source: "nope"})
		Expect(err).To(MatchError(HavePrefix("nope is no folder here, and no URL: give a git repository")))
		_, err = add(engine.MarketAddRequest{Source: "http://example.com/m.git"})
		Expect(err).To(MatchError(ContainSubstring("plain http:// is allowed only for a loopback host")))
		Expect(errs.Hints(err)).To(BeEmpty(), "a marketplace's hints are commands only")
		_, err = add(engine.MarketAddRequest{Name: "f", Source: filepath.Join(tmp, "m"), Ref: "v1"})
		Expect(err).To(MatchError("--ref applies to git sources only, not to folder " + filepath.Join(tmp, "m")))
		Expect(errs.Hints(err)).To(Equal([]string{"lola market f --add " + filepath.Join(tmp, "m")}))
	})

	Describe("catalog ssh hosts", func() {
		var url string
		BeforeEach(func() { url, _ = repo("priv", map[string]string{".claude-plugin/marketplace.json": sshCatalog}) })
		// allowed is the ssh hosts markets.yaml records for name.
		allowed := func(name string) []string {
			GinkgoHelper()
			b, err := os.ReadFile(p.Markets)
			Expect(err).NotTo(HaveOccurred())
			reg, err := market.ParseRegistry(p.Markets, b)
			Expect(err).NotTo(HaveOccurred())
			return reg.Find(name).SSHHosts
		}
		names := func(v *engine.MarketView) []string {
			var out []string
			for _, en := range v.Entries {
				out = append(out, en.Name)
			}
			return out
		}
		show := func(name string) *engine.MarketView {
			GinkgoHelper()
			v, err := e.ShowMarket(name, nil)
			Expect(err).NotTo(HaveOccurred())
			return v
		}
		hint := func(hosts string) engine.Notice {
			return engine.Notice{Level: engine.LevelWarn, Text: "to allow ssh hosts → lola market priv --update " + hosts}
		}
		noPick := func(engine.MarketSSHStep) ([]string, error) {
			Fail("the host picker was asked")
			return nil, nil
		}
		// newer is sshCatalog with an entry at a host it did not name before.
		newer := strings.Replace(sshCatalog, "]}", `,
  {"name": "e", "source": {"source": "url", "url": "ssh://git@new.test/e.git"}}]}`, 1)

		It("skips every ssh entry of a catalog added without hosts off a terminal, hinting the --update that allows them", func() {
			res, err := add(engine.MarketAddRequest{Name: "priv", Source: url})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Notices).To(ContainElements(
				engine.Notice{Level: engine.LevelWarn, Text: "4 entries skipped: 3 ssh sources, 1 local or private source → lola market priv"},
				hint("--ssh-host git.corp.test --ssh-host other.test")))
			Expect(allowed("priv")).To(BeEmpty())
			v := show("priv")
			Expect(v.Entries).To(BeEmpty())
			Expect(v.SSHHosts).To(Equal([]string{}))
			Expect(v.Notices).To(ContainElement(hint("--ssh-host git.corp.test --ssh-host other.test")))
			v, err = e.ShowMarket("priv", []string{"--json"})
			Expect(err).NotTo(HaveOccurred())
			Expect(v.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
				Text: "to allow ssh hosts → lola market priv --update --ssh-host git.corp.test --ssh-host other.test --json"}))
		})

		It("--ssh-host on --add allows the host, stores it, and shows which entries it admits", func() {
			res, err := add(engine.MarketAddRequest{Name: "priv", Source: url, SSHHosts: []string{"Git.Corp.Test"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "allowed ssh hosts: git.corp.test"}))
			Expect(allowed("priv")).To(Equal([]string{"git.corp.test"}))
			v := show("priv")
			Expect(names(v)).To(Equal([]string{"a", "b"}))
			Expect(v.Entries[0]).To(And(HaveField("SSHHost", "git.corp.test"), HaveField("Source", "ssh://git@git.corp.test:2222/a.git")))
			Expect(v.Entries[1]).To(HaveField("SSHHost", "git.corp.test"))
			Expect(v.SSHHosts).To(Equal([]string{"git.corp.test"}))
			Expect(v.Notices).To(ContainElement(hint("--ssh-host other.test")))
			Expect(v.Notices).NotTo(ContainElement(hint("--ssh-host git.corp.test --ssh-host other.test")))
		})

		It("an entry that is not ssh has no ssh host", func() {
			u, _ := repo("plugins", acme())
			_, err := add(engine.MarketAddRequest{Source: u})
			Expect(err).NotTo(HaveOccurred())
			v := show("acme")
			Expect(v.Entries).NotTo(BeEmpty())
			for _, en := range v.Entries {
				Expect(en.SSHHost).To(BeEmpty())
			}
			Expect(v.SSHHosts).To(Equal([]string{}))
		})

		It("the picker on --add offers each host not allowed with its entry count, and allows only what it ticks", func() {
			var steps []engine.MarketSSHStep
			_, err := add(engine.MarketAddRequest{Name: "priv", Source: url, PickSSH: func(s engine.MarketSSHStep) ([]string, error) {
				steps = append(steps, s)
				return []string{"other.test"}, nil
			}})
			Expect(err).NotTo(HaveOccurred())
			Expect(steps).To(Equal([]engine.MarketSSHStep{{Market: "priv", Hosts: []engine.MarketSSHHost{{Host: "git.corp.test", Entries: 2},
				{Host: "other.test", Entries: 1}}}}), "the private address offers nothing")
			Expect(names(show("priv"))).To(Equal([]string{"c"}))
			Expect(allowed("priv")).To(Equal([]string{"other.test"}))

			_, err = add(engine.MarketAddRequest{Name: "bad", Source: url, PickSSH: func(engine.MarketSSHStep) ([]string, error) {
				return []string{"nope.test"}, nil
			}})
			Expect(err).To(MatchError("the host picker returned a host it was not offered"))
			_, err = e.ShowMarket("bad", nil)
			Expect(err).To(MatchError(HavePrefix("you have no marketplace bad")), "nothing registered")

			_, err = add(engine.MarketAddRequest{Name: "none", Source: url, PickSSH: func(engine.MarketSSHStep) ([]string, error) { return nil, nil }})
			Expect(err).NotTo(HaveOccurred())
			Expect(allowed("none")).To(BeEmpty())
			Expect(show("none").Entries).To(BeEmpty())
		})

		It("the picker offers only the hosts --ssh-host did not cover", func() {
			var steps []engine.MarketSSHStep
			_, err := add(engine.MarketAddRequest{Name: "priv", Source: url, SSHHosts: []string{"git.corp.test"}, PickSSH: func(s engine.MarketSSHStep) ([]string, error) {
				steps = append(steps, s)
				return nil, nil
			}})
			Expect(err).NotTo(HaveOccurred())
			Expect(steps).To(Equal([]engine.MarketSSHStep{{Market: "priv", Hosts: []engine.MarketSSHHost{{Host: "other.test", Entries: 1}}}}))
			Expect(allowed("priv")).To(Equal([]string{"git.corp.test"}))
		})

		It("a dry run asks nothing, previews the hosts --ssh-host allows, and writes nothing", func() {
			res, err := add(engine.MarketAddRequest{Name: "priv", Source: url, DryRun: true, SSHHosts: []string{"git.corp.test"}, PickSSH: noPick})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "would allow ssh hosts: git.corp.test"}))
			Expect(res.Markets).To(HaveLen(1))
			Expect(res.Markets[0].Entries).To(Equal(2))
			Expect(p.Markets).NotTo(BeAnExistingFile())
		})

		It("refuses an --ssh-host that is not a host name lola can allow, before anything is written", func() {
			for _, h := range []string{"ssh://git.corp.test", "git@x.test", "x.test:22", "10.0.0.5", "localhost", "x.test/a"} {
				_, err := add(engine.MarketAddRequest{Name: "priv", Source: url, SSHHosts: []string{"ok.test", h}})
				Expect(err).To(MatchError(HavePrefix("--ssh-host ")), h)
			}
			Expect(p.Markets).NotTo(BeAnExistingFile())
			_, err := add(engine.MarketAddRequest{Name: "priv", Source: url})
			Expect(err).NotTo(HaveOccurred())
			_, err = e.UpdateMarkets(ctx, engine.MarketUpdateRequest{Names: []string{"priv"}, SSHHosts: []string{"x.test:22"}})
			Expect(err).To(MatchError(HavePrefix("--ssh-host ")))
			Expect(allowed("priv")).To(BeEmpty())
		})

		It("--update never widens the stored hosts but by the picker", func() {
			_, err := add(engine.MarketAddRequest{Name: "priv", Source: url, SSHHosts: []string{"git.corp.test"}})
			Expect(err).NotTo(HaveOccurred())
			repo("priv", map[string]string{".claude-plugin/marketplace.json": newer})
			up, err := e.UpdateMarkets(ctx, engine.MarketUpdateRequest{Names: []string{"priv"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(up.Updates[0].Outcome).To(Equal(engine.MarketUpdated))
			Expect(allowed("priv")).To(Equal([]string{"git.corp.test"}))
			Expect(up.Notices).To(ContainElement(hint("--ssh-host new.test --ssh-host other.test")))
			Expect(show("priv").Skipped).To(ContainElement(HaveField("Name", "e")))

			var steps []engine.MarketSSHStep
			up, err = e.UpdateMarkets(ctx, engine.MarketUpdateRequest{Names: []string{"priv"}, PickSSH: func(s engine.MarketSSHStep) ([]string, error) {
				steps = append(steps, s)
				return []string{"new.test"}, nil
			}})
			Expect(err).NotTo(HaveOccurred())
			Expect(steps).To(Equal([]engine.MarketSSHStep{{Market: "priv", Hosts: []engine.MarketSSHHost{{Host: "new.test", Entries: 1},
				{Host: "other.test", Entries: 1}}}}))
			Expect(allowed("priv")).To(Equal([]string{"git.corp.test", "new.test"}))
			Expect(up.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "allowed ssh hosts: new.test"}))
			Expect(up.Changes).To(Equal([]engine.MarketChange{{Market: "priv", Entry: "e", Change: "added", Fields: []string{}}}))
			Expect(names(show("priv"))).To(Equal([]string{"a", "b", "e"}))

			up, err = e.UpdateMarkets(ctx, engine.MarketUpdateRequest{Names: []string{"priv"}, DryRun: true, PickSSH: noPick})
			Expect(err).NotTo(HaveOccurred())
			Expect(up.Updates[0].Outcome).To(Equal(engine.MarketWouldUpdate))
		})

		It("--ssh-host on --update stores the host for one marketplace and refuses more than one", func() {
			_, err := add(engine.MarketAddRequest{Name: "priv", Source: url})
			Expect(err).NotTo(HaveOccurred())
			u, _ := repo("plugins", acme())
			_, err = add(engine.MarketAddRequest{Source: u})
			Expect(err).NotTo(HaveOccurred())

			_, err = e.UpdateMarkets(ctx, engine.MarketUpdateRequest{All: true, SSHHosts: []string{"other.test"}})
			Expect(err).To(MatchError("--ssh-host applies to one marketplace"))
			Expect(errs.Hints(err)).To(BeEmpty(), "--all means no one marketplace")
			_, err = e.UpdateMarkets(ctx, engine.MarketUpdateRequest{Names: []string{"priv", "acme"}, SSHHosts: []string{"other.test"}, Display: []string{"--json"}})
			Expect(err).To(MatchError("--ssh-host applies to one marketplace"))
			Expect(errs.Hints(err)).To(Equal([]string{"lola market priv --update --ssh-host other.test --json",
				"lola market acme --update --ssh-host other.test --json"}))
			Expect(allowed("priv")).To(BeEmpty())

			up, err := e.UpdateMarkets(ctx, engine.MarketUpdateRequest{Names: []string{"priv"}, SSHHosts: []string{"other.test"}, DryRun: true})
			Expect(err).NotTo(HaveOccurred())
			Expect(up.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "would allow ssh hosts: other.test"}))
			Expect(allowed("priv")).To(BeEmpty())

			up, err = e.UpdateMarkets(ctx, engine.MarketUpdateRequest{Names: []string{"priv"}, SSHHosts: []string{"other.test"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(up.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "allowed ssh hosts: other.test"}))
			Expect(up.Changes).To(Equal([]engine.MarketChange{{Market: "priv", Entry: "c", Change: "added", Fields: []string{}}}))
			Expect(allowed("priv")).To(Equal([]string{"other.test"}))
			Expect(names(show("priv"))).To(Equal([]string{"c"}))
		})

		It("a stored host the catalog no longer names stays allowed", func() {
			_, err := add(engine.MarketAddRequest{Name: "priv", Source: url, SSHHosts: []string{"git.corp.test", "other.test"}})
			Expect(err).NotTo(HaveOccurred())
			repo("priv", map[string]string{".claude-plugin/marketplace.json": `{"name": "priv", "owner": {"name": "P"}, "plugins": [
  {"name": "c", "source": {"source": "url", "url": "ssh://git@other.test/c.git"}}]}`})
			_, err = e.UpdateMarkets(ctx, engine.MarketUpdateRequest{Names: []string{"priv"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(allowed("priv")).To(Equal([]string{"git.corp.test", "other.test"}))
		})

		It("revoking a host re-skips its entries and keeps the marketplace", func() {
			_, err := add(engine.MarketAddRequest{Name: "priv", Source: url, SSHHosts: []string{"git.corp.test"}})
			Expect(err).NotTo(HaveOccurred())
			removed := []engine.MarketChange{{Market: "priv", Entry: "a", Change: "removed", Fields: []string{}},
				{Market: "priv", Entry: "b", Change: "removed", Fields: []string{}}}

			_, err = e.RevokeSSHHosts(engine.MarketSSHRevokeRequest{Name: "priv", Hosts: []string{"other.test"}})
			Expect(err).To(MatchError("marketplace priv does not allow ssh host other.test; it allows: git.corp.test"))
			Expect(errs.Hints(err)).To(Equal([]string{"lola market priv --ssh-host git.corp.test --remove"}))
			_, err = e.RevokeSSHHosts(engine.MarketSSHRevokeRequest{Name: "priv", Hosts: []string{"git.corp.test", "other.test"}, DryRun: true, Display: []string{"--json"}})
			Expect(err).To(MatchError("marketplace priv does not allow ssh host other.test; it allows: git.corp.test"))
			Expect(errs.Hints(err)).To(Equal([]string{"lola market priv --ssh-host git.corp.test --remove -n --json"}), "the run with only the allowed ones")
			_, err = e.RevokeSSHHosts(engine.MarketSSHRevokeRequest{Name: "priv", Hosts: []string{"other..test"}})
			Expect(err).To(MatchError(HavePrefix("--ssh-host ")))
			_, err = e.RevokeSSHHosts(engine.MarketSSHRevokeRequest{Name: "prov", Hosts: []string{"git.corp.test"}})
			Expect(err).To(MatchError("you have no marketplace prov; close names: priv"))
			Expect(errs.Hints(err)).To(Equal([]string{"lola market priv --ssh-host git.corp.test --remove", "lola market"}))

			before, err := os.ReadFile(p.Markets)
			Expect(err).NotTo(HaveOccurred())
			dry, err := e.RevokeSSHHosts(engine.MarketSSHRevokeRequest{Name: "priv", Hosts: []string{"Git.Corp.Test"}, DryRun: true})
			Expect(err).NotTo(HaveOccurred())
			Expect(dry.DryRun).To(BeTrue())
			Expect(dry.Updates).To(Equal([]engine.MarketOutcome{{Market: "priv", Outcome: engine.MarketWouldRevoke, Hints: []string{}}}))
			Expect(dry.Changes).To(Equal(removed))
			Expect(os.ReadFile(p.Markets)).To(Equal(before), "a dry run writes nothing")

			res, err := e.RevokeSSHHosts(engine.MarketSSHRevokeRequest{Name: "priv", Hosts: []string{"git.corp.test"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Updates).To(Equal([]engine.MarketOutcome{{Market: "priv", Outcome: engine.MarketRevoked, Hints: []string{}}}))
			Expect(res.Changes).To(Equal(removed))
			Expect(allowed("priv")).To(BeEmpty())
			Expect(os.ReadFile(p.Markets)).NotTo(ContainSubstring("ssh_hosts"))
			v := show("priv")
			Expect(v.Entries).To(BeEmpty())
			Expect(v.SSHHosts).To(Equal([]string{}))

			_, err = e.RevokeSSHHosts(engine.MarketSSHRevokeRequest{Name: "priv", Hosts: []string{"git.corp.test"}})
			Expect(err).To(MatchError("marketplace priv does not allow ssh host git.corp.test; it allows none"))
			Expect(errs.Hints(err)).To(Equal([]string{"lola market priv"}))
		})

		It("revoking with no cached catalog writes the registration alone", func() {
			_, err := add(engine.MarketAddRequest{Name: "priv", Source: url, SSHHosts: []string{"git.corp.test", "other.test"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(os.RemoveAll(filepath.Join(p.MarketCache, "priv"))).To(Succeed())
			res, err := e.RevokeSSHHosts(engine.MarketSSHRevokeRequest{Name: "priv", Hosts: []string{"other.test"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Changes).To(BeEmpty())
			Expect(res.Updates).To(Equal([]engine.MarketOutcome{{Market: "priv", Outcome: engine.MarketRevoked, Hints: []string{}}}))
			Expect(allowed("priv")).To(Equal([]string{"git.corp.test"}))
		})

		It("a refused --add hints runs that keep its --ssh-host values", func() {
			_, err := add(engine.MarketAddRequest{Name: "Priv", Source: url, SSHHosts: []string{"x.test:22"}})
			Expect(err).To(MatchError(HavePrefix("--ssh-host ")), "the hosts are checked before any hint is built")
			_, err = add(engine.MarketAddRequest{Name: "Priv", Source: url, SSHHosts: []string{"Other.Test"}})
			Expect(errs.Hints(err)).To(Equal([]string{"lola market priv --add " + url + " --ssh-host other.test"}))
			folder := filepath.Join(tmp, "m")
			writeTree(folder, map[string]string{".claude-plugin/marketplace.json": sshCatalog})
			_, err = add(engine.MarketAddRequest{Name: "f", Source: folder, Ref: "v1", SSHHosts: []string{"other.test"}})
			Expect(errs.Hints(err)).To(Equal([]string{"lola market f --add " + folder + " --ssh-host other.test"}))

			_, err = add(engine.MarketAddRequest{Name: "priv", Source: url})
			Expect(err).NotTo(HaveOccurred())
			_, err = add(engine.MarketAddRequest{Name: "priv", Source: url, SSHHosts: []string{"other.test"}, Display: []string{"--json"}})
			Expect(err).To(MatchError("priv is already this marketplace"))
			Expect(errs.Hints(err)).To(Equal([]string{"lola market priv --update --ssh-host other.test --json"}))
			_, err = add(engine.MarketAddRequest{Source: url, SSHHosts: []string{"other.test"}, DryRun: true})
			Expect(errs.Hints(err)).To(Equal([]string{"lola market priv --update --ssh-host other.test -n"}))

			u, _ := repo("plugins", acme())
			_, err = add(engine.MarketAddRequest{Name: "priv", Source: u, SSHHosts: []string{"other.test"}})
			Expect(err).To(MatchError(HavePrefix("priv is already a marketplace")))
			Expect(errs.Hints(err)).To(Equal([]string{"lola market priv-2 --add " + u + " --path .claude-plugin/marketplace.json --ssh-host other.test"}))
		})

		It("a dry-run --add hints the --add itself with every host, and none when its --ssh-host covers them", func() {
			res, err := add(engine.MarketAddRequest{Name: "priv", Source: url, DryRun: true, Display: []string{"--json"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
				Text: "to allow ssh hosts → lola market priv --add " + url + " --ssh-host git.corp.test --ssh-host other.test --json"}))
			res, err = add(engine.MarketAddRequest{Name: "priv", Source: url, DryRun: true, SSHHosts: []string{"git.corp.test"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
				Text: "to allow ssh hosts → lola market priv --add " + url + " --ssh-host git.corp.test --ssh-host other.test"}))
			res, err = add(engine.MarketAddRequest{Name: "priv", Source: url, DryRun: true, SSHHosts: []string{"git.corp.test", "other.test"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Notices).NotTo(ContainElement(HaveField("Text", HavePrefix("to allow ssh hosts"))))
			Expect(p.Markets).NotTo(BeAnExistingFile())
		})

		It("a dry-run --update hints the --update with its --ssh-host values and the hosts still pending", func() {
			_, err := add(engine.MarketAddRequest{Name: "priv", Source: url})
			Expect(err).NotTo(HaveOccurred())
			repo("priv", map[string]string{".claude-plugin/marketplace.json": newer})
			up, err := e.UpdateMarkets(ctx, engine.MarketUpdateRequest{Names: []string{"priv"}, DryRun: true})
			Expect(err).NotTo(HaveOccurred())
			Expect(up.Notices).To(ContainElement(hint("--ssh-host git.corp.test --ssh-host new.test --ssh-host other.test")))
			up, err = e.UpdateMarkets(ctx, engine.MarketUpdateRequest{Names: []string{"priv"}, DryRun: true, SSHHosts: []string{"other.test"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(up.Notices).To(ContainElement(hint("--ssh-host git.corp.test --ssh-host new.test --ssh-host other.test")))
			up, err = e.UpdateMarkets(ctx, engine.MarketUpdateRequest{Names: []string{"priv"}, DryRun: true,
				SSHHosts: []string{"git.corp.test", "new.test", "other.test"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(up.Notices).NotTo(ContainElement(HaveField("Text", HavePrefix("to allow ssh hosts"))))
			Expect(allowed("priv")).To(BeEmpty())
		})

		It("--ssh-host on --update counts a marketplace named twice once", func() {
			_, err := add(engine.MarketAddRequest{Name: "priv", Source: url})
			Expect(err).NotTo(HaveOccurred())
			_, err = e.UpdateMarkets(ctx, engine.MarketUpdateRequest{Names: []string{"priv", "priv"}, SSHHosts: []string{"other.test"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(allowed("priv")).To(Equal([]string{"other.test"}))
		})

		It("revoking needs a host", func() {
			_, err := add(engine.MarketAddRequest{Name: "priv", Source: url, SSHHosts: []string{"git.corp.test"}})
			Expect(err).NotTo(HaveOccurred())
			before, err := os.ReadFile(p.Markets)
			Expect(err).NotTo(HaveOccurred())
			_, err = e.RevokeSSHHosts(engine.MarketSSHRevokeRequest{Name: "priv"})
			Expect(err).To(MatchError("--remove with --ssh-host needs a host"))
			Expect(os.ReadFile(p.Markets)).To(Equal(before))
		})

		It("an allowed host at a private address is still refused", func() {
			_, err := add(engine.MarketAddRequest{Name: "priv", Source: url, SSHHosts: []string{"git.corp.test"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(show("priv").Skipped).To(ContainElement(And(HaveField("Name", "d"), HaveField("Reason", ContainSubstring("private address 10.0.0.5")))))
		})
	})
})

var _ = Describe("removing marketplaces and the Python verbs", func() {
	var (
		tmp, url string
		p        paths.Paths
		e        *engine.Engine
	)
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p, targets.Builtin())
		e.Getwd = func() (string, error) { return tmp, nil }
		url = filepath.Join(tmp, "acme")
		writeTree(url, map[string]string{".claude-plugin/marketplace.json": acmeCatalog, "tools/formatter/x.md": "x"})
		_, err = e.AddMarket(context.Background(), engine.MarketAddRequest{Source: url})
		Expect(err).NotTo(HaveOccurred())
		writeTree(filepath.Join(tmp, "other"), map[string]string{".claude-plugin/marketplace.json": strings.Replace(acmeCatalog, `"acme"`, `"other"`, 1)})
		_, err = e.AddMarket(context.Background(), engine.MarketAddRequest{Source: filepath.Join(tmp, "other")})
		Expect(err).NotTo(HaveOccurred())
	})

	It("market --remove asks, refuses off a terminal without -y, and drops cache and registration", func() {
		_, err := e.RemoveMarket(engine.MarketRemoveRequest{Name: "acme"})
		Expect(err).To(MatchError("lola market --remove asks before it forgets a marketplace, and there is no terminal to ask on"))
		Expect(errs.Hints(err)).To(Equal([]string{"lola market acme --remove -y"}), "-y approves exactly the removal refused")
		_, err = e.RemoveMarket(engine.MarketRemoveRequest{Name: "acme", Display: []string{"--json"}})
		Expect(errs.Hints(err)).To(Equal([]string{"lola market acme --remove -y --json"}))
		_, err = e.RemoveMarket(engine.MarketRemoveRequest{Name: "nope", Confirm: func(engine.MarketRow) (bool, error) { return true, nil }})
		Expect(err).To(MatchError("you have no marketplace nope"))

		var asked []engine.MarketRow
		_, err = e.RemoveMarket(engine.MarketRemoveRequest{Name: "acme", Confirm: func(r engine.MarketRow) (bool, error) {
			asked = append(asked, r)
			return false, nil
		}})
		Expect(err).To(MatchError(engine.ErrDeclined))
		Expect(asked).To(HaveLen(1))
		Expect(asked[0]).To(And(HaveField("Name", "acme"), HaveField("Source", url)))
		res, err := e.RemoveMarket(engine.MarketRemoveRequest{Name: "acme", DryRun: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Removed).To(Equal([]string{"acme"}))
		Expect(filepath.Join(p.MarketCache, "acme")).To(BeADirectory(), "a dry run and a No keep it")

		res, err = e.RemoveMarket(engine.MarketRemoveRequest{Name: "acme", Confirm: func(engine.MarketRow) (bool, error) { return true, nil }})
		Expect(err).NotTo(HaveOccurred())
		Expect(res).To(Equal(&engine.MarketRemoveResult{Removed: []string{"acme"}, Notices: []engine.Notice{}}))
		Expect(filepath.Join(p.MarketCache, "acme")).NotTo(BeAnExistingFile())
		Expect(os.ReadFile(p.Markets)).To(Equal([]byte("# lola marketplaces. Safe to edit by hand.\nversion: 1\nmarkets:\n  - name: other\n" +
			"    source: " + filepath.Join(tmp, "other") + "\n    path: .claude-plugin/marketplace.json\n")))
		rows, _, err := e.Markets(nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(1))
	})

	It("python market verbs are refused with the translated command", func() {
		type want struct{ err, hint string }
		const (
			add    = "is not a lola market verb: lola market <name> --add <source> adds a marketplace"
			list   = "is not a lola market verb: lola market lists your marketplaces"
			show   = "is not a lola market verb: lola market <name> shows a marketplace"
			remove = "is not a lola market verb: lola market <name> --remove forgets a marketplace"
			search = "search is not a lola market verb: lola search <query> searches your cache and every fetched catalog"
			set    = "set is not a lola market verb: lola market <name> --remove forgets a marketplace; lola has no disabled state"
		)
		for args, w := range map[string]want{
			"add general https://example.com/m.git": {"add " + add, "lola market general --add https://example.com/m.git"},
			"add general":                           {"add " + add, ""},
			"add Bad x":                             {"add " + add + "; Bad cannot name a marketplace", ""},
			"ls":                                    {"ls " + list, "lola market"},
			"list":                                  {"list " + list, "lola market"},
			"ls acme":                               {"ls " + show, "lola market acme"},
			"ls acme other":                         {"ls " + show, ""},
			"show acme":                             {"show " + show, "lola market acme"},
			"show nope":                             {"show " + show + ", and you have no marketplace nope", "lola market"},
			"show":                                  {"show " + show, ""},
			"rm acme":                               {"rm " + remove, "lola market acme --remove"},
			"remove":                                {"remove " + remove, ""},
			"rm":                                    {"rm " + remove, ""},
			"update":                                {"update is not a lola market verb: lola market --update --all updates every marketplace", "lola market --update --all"},
			"update acme other":                     {"update is not a lola market verb: lola market <name>... --update updates the marketplaces named", "lola market acme other --update"},
			"search fmt":                            {search, "lola search fmt"},
			"search":                                {search, ""},
			"set acme":                              {set, ""},
			"set":                                   {set, ""},
		} {
			f := strings.Fields(args)
			err := e.CoachMarketVerb(engine.MarketVerb{Verb: f[0], Operands: f[1:]})
			Expect(err).To(MatchError(w.err), args)
			if w.hint == "" {
				Expect(errs.Hints(err)).To(BeEmpty(), args)
			} else {
				Expect(errs.Hints(err)).To(Equal([]string{w.hint}), args)
			}
		}
		long := strings.Repeat("q", 256)
		for query, hint := range map[string][]string{
			"a\tb":     nil,
			long + "q": nil,
			long:       {"lola search " + long + " --json"},
			"fmt":      {"lola search fmt --json"},
		} {
			err := e.CoachMarketVerb(engine.MarketVerb{Verb: "search", Operands: []string{query}, Display: []string{"--json"}})
			Expect(err).To(MatchError(search), query)
			Expect(errs.Hints(err)).To(Equal(hint), "a hint only when it is the query typed, whole: "+query)
		}
		for args, w := range map[string][2]string{
			"show acmee": {"show " + show + ", and you have no marketplace acmee; close names: acme", "lola market acme"},
			"rm acmee":   {"rm " + remove + ", and you have no marketplace acmee; close names: acme", "lola market acme --remove"},
			"update acme acmee": {"update is not a lola market verb: lola market <name>... --update updates the marketplaces named, " +
				"and you have no marketplace acmee; close names: acme", "lola market acme --update"},
		} {
			f := strings.Fields(args)
			err := e.CoachMarketVerb(engine.MarketVerb{Verb: f[0], Operands: f[1:], Yes: true})
			Expect(err).To(MatchError(w[0]), args)
			Expect(errs.Hints(err)).To(Equal([]string{w[1], "lola market"}), args+": the close name in its place, never with -y")
		}
		err := e.CoachMarketVerb(engine.MarketVerb{Verb: "rm", Operands: []string{"acme"}, Yes: true, DryRun: true})
		Expect(errs.Hints(err)).To(Equal([]string{"lola market acme --remove -n -y"}), "-y kept only when typed")
		err = e.CoachMarketVerb(engine.MarketVerb{Verb: "add", Operands: []string{"g", "https://e.example/m.git"}, Ref: "v1", Path: "sub", DryRun: true})
		Expect(errs.Hints(err)).To(Equal([]string{"lola market g --add https://e.example/m.git --ref v1 --path sub -n"}))
		err = e.CoachMarketVerb(engine.MarketVerb{Verb: "add", Operands: []string{"priv", "https://e.example/m.git"}, SSHHosts: []string{"x.test", "y.test"},
			Display: []string{"--json"}})
		Expect(errs.Hints(err)).To(Equal([]string{"lola market priv --add https://e.example/m.git --ssh-host x.test --ssh-host y.test --json"}))
		err = e.CoachMarketVerb(engine.MarketVerb{Verb: "update", Operands: []string{"acme"}, SSHHosts: []string{"x.test"}, DryRun: true})
		Expect(errs.Hints(err)).To(Equal([]string{"lola market acme --update -n --ssh-host x.test"}))
		err = e.CoachMarketVerb(engine.MarketVerb{Verb: "update", Operands: []string{"acme", "other"}, SSHHosts: []string{"x.test"}})
		Expect(errs.Hints(err)).To(Equal([]string{"lola market acme --update --ssh-host x.test", "lola market other --update --ssh-host x.test"}),
			"--ssh-host applies to one marketplace: a run for each")
		err = e.CoachMarketVerb(engine.MarketVerb{Verb: "update", Operands: []string{"acmee"}, SSHHosts: []string{"x.test", "y.test"}})
		Expect(errs.Hints(err)).To(Equal([]string{"lola market acme --update --ssh-host x.test --ssh-host y.test", "lola market"}),
			"the close name's run keeps every --ssh-host")
		err = e.CoachMarketVerb(engine.MarketVerb{Verb: "update", Operands: []string{"other", "acmee"}, SSHHosts: []string{"x.test"}, DryRun: true,
			Display: []string{"--json"}})
		Expect(errs.Hints(err)).To(Equal([]string{"lola market other --update -n --ssh-host x.test --json",
			"lola market acme --update -n --ssh-host x.test --json", "lola market --json"}),
			"--ssh-host applies to one marketplace: a run for each name, the close name in the missing one's place")
		for _, h := range errs.Hints(err)[:2] {
			f := strings.Fields(strings.TrimPrefix(h, "lola market "))
			_, err := e.UpdateMarkets(context.Background(), engine.MarketUpdateRequest{Names: f[:1], DryRun: true, SSHHosts: []string{"x.test"}, Display: []string{"--json"}})
			Expect(err).NotTo(HaveOccurred(), h)
		}
		err = e.CoachMarketVerb(engine.MarketVerb{Verb: "update", Operands: []string{"acme"}, All: true, SSHHosts: []string{"x.test"}})
		Expect(errs.Hints(err)).To(Equal([]string{"lola market acme --update --ssh-host x.test", "lola market --update --all"}),
			"--ssh-host applies to one marketplace, never --all")
		for _, f := range [][]string{{"update"}, {"rm", "acme"}, {"show", "acme"}, {"ls"}, {"search", "fmt"}} {
			with := e.CoachMarketVerb(engine.MarketVerb{Verb: f[0], Operands: f[1:], SSHHosts: []string{"x.test"}})
			without := e.CoachMarketVerb(engine.MarketVerb{Verb: f[0], Operands: f[1:]})
			Expect(errs.Hints(with)).To(Equal(errs.Hints(without)), "%v: --ssh-host is not carried where the command would not allow it", f)
		}
		err = e.CoachMarketVerb(engine.MarketVerb{Verb: "update", All: true})
		Expect(errs.Hints(err)).To(Equal([]string{"lola market --update --all"}))
		err = e.CoachMarketVerb(engine.MarketVerb{Verb: "update", Operands: []string{"acme"}, All: true})
		Expect(err).To(MatchError("update is not a lola market verb: lola market <name>... --update updates the marketplaces named, " +
			"and lola market --update --all updates every marketplace"))
		Expect(errs.Hints(err)).To(Equal([]string{"lola market acme --update", "lola market --update --all"}))
		err = e.CoachMarketVerb(engine.MarketVerb{Verb: "set", Operands: []string{"acme"}, Disable: true, Yes: true})
		Expect(err).To(MatchError(set))
		Expect(errs.Hints(err)).To(Equal([]string{"lola market acme --remove"}), "never with -y")
		err = e.CoachMarketVerb(engine.MarketVerb{Verb: "set", Operands: []string{"acme"}, Enable: true})
		Expect(err).To(MatchError("acme is already active; lola has no disabled state"))
		Expect(errs.Hints(err)).To(Equal([]string{"lola market acme"}))
		err = e.CoachMarketVerb(engine.MarketVerb{Verb: "set", Operands: []string{"nope"}, Enable: true})
		Expect(err).To(MatchError("lola has no disabled state, and you have no marketplace nope"))
		Expect(errs.Hints(err)).To(BeEmpty())
	})
})
