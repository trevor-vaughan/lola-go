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
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// acmeCatalog is a Claude catalog with one relative entry, one remote entry and one npm entry.
const acmeCatalog = `{"name": "acme", "owner": {"name": "Acme"}, "description": "Acme tools", "plugins": [
  {"name": "formatter", "source": "./tools/formatter", "description": "Formats code", "version": "1.0.0"},
  {"name": "linter", "source": {"source": "url", "url": "https://lola.test/linter.git", "ref": "v1"}},
  {"name": "npmx", "source": {"source": "npm", "package": "x"}}
]}`

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
		e = engine.New(p)
		e.Getwd = func() (string, error) { return tmp, nil }
		srv = filepath.Join(tmp, "srv")
		Expect(os.MkdirAll(srv, 0o755)).To(Succeed())
		// A marketplace's own source is one you type, so a loopback http server may serve it (§12).
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

		// One source and ref at another catalog file is another marketplace (§3.1: source, ref and path).
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
			"marketplace.yml, marketplace.yaml"))
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
			Expect(err).To(MatchError(ContainSubstring("symbolic links are not allowed")), dir)
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
		Expect(res.Updates[0].Error).To(ContainSubstring("symbolic links are not allowed"), "a link anywhere in the tree refuses the fetch")
	})

	It("installs, renders, updates and trusts a selection of components as its entry records it", func() {
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
		res, err := e.Render(ctx, sc, claude)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Findings).NotTo(ContainElement(HaveField("Code", engine.CodeContentChanged)), "render stages the selection, not the folder")

		writeTree(mine, map[string]string{"skills/a/SKILL.md": skill("a2")})
		res, err = e.Render(ctx, sc, claude)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Findings).To(ContainElement(And(HaveField("Code", engine.CodeContentChanged), HaveField("Hint", "review it: lola sync -C "+proj+" sel"))),
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

		// A pulled change of the selection: refused while the cache holds the other one, naming
		// both, then a trust question of its own.
		b, err := os.ReadFile(filepath.Join(proj, ".lola", "project.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(proj, ".lola", "project.yml"), []byte(strings.Replace(string(b), "skills: [skills/a]", "skills: [skills/b]", 1)),
			0o644)).To(Succeed())
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc})
		Expect(err).To(MatchError("sel in your cache comes from " + mine + " selecting skills skills/a, and project.yml asks for " + mine +
			" selecting skills skills/b; the cache is shared by every project on this machine"))
		Expect(e.Store.Remove("sel")).To(BeTrue())
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
			engine.Options{Targets: []string{"claude-code"}, AllowHooks: true})
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
		e = engine.New(p)
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
		for args, w := range map[string]want{
			"add general https://example.com/m.git": {"lola market add is the Python lola's form; in lola: lola market <name> --add <source>",
				"lola market general --add https://example.com/m.git"},
			"add general": {"lola market add takes a name and a source; in lola: lola market <name> --add <source>", ""},
			"add Bad x":   {"lola market add is the Python lola's form; in lola: lola market <name> --add <source>; Bad cannot name a marketplace", ""},
			"ls":          {"lola market ls is the Python lola's form; in lola: lola market", "lola market"},
			"list":        {"lola market list is the Python lola's form; in lola: lola market", "lola market"},
			"ls acme":     {"lola market ls is the Python lola's form; in lola: lola market <name>", "lola market acme"},
			"show acme":   {"lola market show is the Python lola's form; in lola: lola market <name>", "lola market acme"},
			"show nope":   {"lola market show is the Python lola's form; in lola: lola market <name>, and you have no marketplace nope", "lola market"},
			"show":        {"lola market show takes one name; in lola: lola market <name>", ""},
			"rm acme":     {"lola market rm is the Python lola's form; in lola: lola market <name> --remove", "lola market acme --remove"},
			"remove":      {"lola market remove takes one name; in lola: lola market <name> --remove", ""},
			"rm":          {"lola market rm takes one name; in lola: lola market <name> --remove", ""},
			"update":      {"lola market update is the Python lola's form; in lola: lola market --update --all", "lola market --update --all"},
			"update acme other": {"lola market update is the Python lola's form; in lola: lola market <name>... --update",
				"lola market acme other --update"},
			"search fmt": {"lola market search is the Python lola's form; in lola: lola search <query>", "lola search fmt"},
			"search":     {"lola market search takes one query; in lola: lola search <query>", ""},
			"set acme":   {"lola market set takes --enable or --disable and one name; in lola: lola market <name> --remove, as lola has no disabled state", ""},
			"set":        {"lola market set takes --enable or --disable and one name; in lola: lola market <name> --remove, as lola has no disabled state", ""},
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
			Expect(err).To(MatchError("lola market search is the Python lola's form; in lola: lola search <query>"), query)
			Expect(errs.Hints(err)).To(Equal(hint), "a hint only when it is the query typed, whole: "+query)
		}
		for args, w := range map[string][2]string{
			"show acmee": {"lola market show is the Python lola's form; in lola: lola market <name>, and you have no marketplace acmee; close names: acme",
				"lola market acme"},
			"rm acmee": {"lola market rm is the Python lola's form; in lola: lola market <name> --remove, and you have no marketplace acmee; close names: acme",
				"lola market acme --remove"},
			"update acme acmee": {"lola market update is the Python lola's form; in lola: lola market <name>... --update, and you have no marketplace acmee; " +
				"close names: acme", "lola market acme --update"},
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
		err = e.CoachMarketVerb(engine.MarketVerb{Verb: "update", All: true})
		Expect(errs.Hints(err)).To(Equal([]string{"lola market --update --all"}))
		err = e.CoachMarketVerb(engine.MarketVerb{Verb: "update", Operands: []string{"acme"}, All: true})
		Expect(err).To(MatchError("lola market update takes names or --all, not both; in lola: lola market <name>... --update, " +
			"or lola market --update --all"))
		Expect(errs.Hints(err)).To(Equal([]string{"lola market acme --update", "lola market --update --all"}))
		err = e.CoachMarketVerb(engine.MarketVerb{Verb: "set", Operands: []string{"acme"}, Disable: true, Yes: true})
		Expect(err).To(MatchError("lola market set is the Python lola's form; in lola: lola market <name> --remove; lola has no disabled state"))
		Expect(errs.Hints(err)).To(Equal([]string{"lola market acme --remove"}), "never with -y")
		err = e.CoachMarketVerb(engine.MarketVerb{Verb: "set", Operands: []string{"acme"}, Enable: true})
		Expect(err).To(MatchError("acme is already active; lola has no disabled state"))
		Expect(errs.Hints(err)).To(Equal([]string{"lola market acme"}))
		err = e.CoachMarketVerb(engine.MarketVerb{Verb: "set", Operands: []string{"nope"}, Enable: true})
		Expect(err).To(MatchError("lola has no disabled state, and you have no marketplace nope"))
		Expect(errs.Hints(err)).To(BeEmpty())
	})
})
