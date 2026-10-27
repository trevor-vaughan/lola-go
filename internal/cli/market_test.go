package cli_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/cli"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// marketCatalog is a Claude catalog with a relative entry, a remote one and an npm one.
const marketCatalog = `{"name": "acme", "owner": {"name": "Acme"}, "description": "Acme tools", "plugins": [
  {"name": "formatter", "source": "./tools/formatter", "description": "Formats code", "version": "1.0.0"},
  {"name": "linter", "source": {"source": "url", "url": "https://lola.test/linter.git", "ref": "v1"}},
  {"name": "npmx", "source": {"source": "npm", "package": "x"}}
]}`

var _ = Describe("lola market", func() {
	var (
		tmp, url, commit string
		p                paths.Paths
	)
	gitIn := func(dir string, args ...string) string {
		GinkgoHelper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))
		return strings.TrimSpace(string(out))
	}
	// publish commits files to the marketplace repository and serves it again.
	publish := func(files map[string]string) {
		GinkgoHelper()
		work := filepath.Join(tmp, "work")
		writeTree(work, files)
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "change")
		bare := filepath.Join(tmp, "srv", "plugins.git")
		Expect(os.RemoveAll(bare)).To(Succeed())
		gitIn(tmp, "clone", "-q", "--bare", work, bare)
		commit = gitIn(work, "rev-parse", "HEAD")
	}
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		GinkgoT().Chdir(tmp)
		Expect(os.MkdirAll(filepath.Join(tmp, "work"), 0o755)).To(Succeed())
		gitIn(filepath.Join(tmp, "work"), "init", "-q", "-b", "main")
		publish(map[string]string{".claude-plugin/marketplace.json": marketCatalog, "tools/formatter/skills/f/SKILL.md": "---\nname: f\n---\nF\n"})
		// You type a marketplace's own source, so a loopback http server may serve it (Plan 6c §12).
		url = testenv.ServeGit(filepath.Join(tmp, "srv")) + "/plugins.git"
	})

	It("adds, lists, shows and updates a marketplace, for people, --plain and --json", func() {
		r := lola("market", "--add", url)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(Equal("✓ marketplace acme: 2 entries (Claude marketplace.json) from " + url + " at " + commit[:7] + "\n" +
			"⚠ 1 entry skipped: 1 npm source → lola market acme\n→ browse: lola install @acme\n"))

		r = lola("market")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(MatchRegexp(`^NAME +FORMAT +ENTRIES +SKIPPED +FETCHED +SOURCE\nacme +claude +2 +1 +just now +` + regexpQuote(url) + "\n$"))
		r = lola("markets", "--plain")
		Expect(r.out).To(MatchRegexp(`^acme\tclaude\t2\t1\t\d{4}-\d\d-\d\dT[0-9:]+Z\t` + regexpQuote(url) + "\t\t.claude-plugin/marketplace.json\n$"))
		r = lola("market", "--json")
		var list struct {
			Markets []map[string]any `json:"markets"`
			Notices []any            `json:"notices"`
		}
		Expect(json.Unmarshal([]byte(r.out), &list)).To(Succeed())
		Expect(list.Markets).To(HaveLen(1))
		Expect(list.Markets[0]).To(HaveKeyWithValue("entries", BeNumerically("==", 2)))
		Expect(list.Markets[0]).To(HaveKeyWithValue("commit", commit))
		Expect(list.Notices).NotTo(BeNil())

		r = lola("marketplace", "acme")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("Marketplace acme (Claude marketplace.json)\n  source       " + url + "\n  commit       " + commit + "\n" +
			"  file         .claude-plugin/marketplace.json\n  fetched      just now\n  owner        Acme\n  description  Acme tools\n"))
		Expect(r.out).To(ContainSubstring("Entries (2):\n  formatter  1.0.0  git  " + url + " path tools/formatter @ " + commit[:7] + "  Formats code\n" +
			"  linter     -      git  https://lola.test/linter.git ref v1"))
		Expect(r.out).To(HaveSuffix("Skipped (1):\n  npm sources are not supported: installing one runs a package manager: npmx\n"))
		r = lola("market", "acme", "--json")
		var show struct {
			Name    string           `json:"name"`
			Entries []map[string]any `json:"entries"`
			Skipped []map[string]any `json:"skipped"`
		}
		Expect(json.Unmarshal([]byte(r.out), &show)).To(Succeed())
		Expect(show.Name).To(Equal("acme"))
		Expect(show.Entries[0]).To(And(HaveKeyWithValue("kind", "git"), HaveKeyWithValue("tags", BeEmpty()), HaveKeyWithValue("notices", BeEmpty())))
		Expect(show.Skipped).To(Equal([]map[string]any{{"name": "npmx", "reason": "npm sources are not supported: installing one runs a package manager"}}))

		publish(map[string]string{".claude-plugin/marketplace.json": strings.Replace(marketCatalog, `"1.0.0"`, `"1.1.0"`, 1)})
		r = lola("market", "acme", "--refresh", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(Equal("→ would update marketplace acme: 2 entries (Claude marketplace.json) from "+url+" at "+commit[:7]+"\n"+
			"  ~ formatter (commit, version)\n"), "the skipped entries have not changed since the add reported them")
		r = lola("market", "--upgrade", "--all", "--plain")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(Equal("updated\tacme\nchange\tchanged\tacme\tformatter\tcommit,version\n"))
	})

	It("installs @market/module, showing where it resolved before anything is asked, and records where it came from", func() {
		Expect(lola("market", "--add", url).code).To(Equal(0))
		r := lola("install", "@acme/formatter", "--user", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(HavePrefix("→ @acme/formatter → " + url + " path tools/formatter @ " + commit + "\n"))
		user, err := os.ReadFile(filepath.Join(p.UserManifest))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(user)).To(ContainSubstring("    market:\n      name: acme\n      source: " + url + "\n"))
		r = lola("install", "@acme/formatter", "--user", "-t", "claude-code", "--json", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(BeEmpty(), "under --json, stderr holds nothing but an error")
		var out struct {
			Modules []map[string]any `json:"modules"`
		}
		Expect(json.Unmarshal([]byte(r.out), &out)).To(Succeed())
		Expect(out.Modules[0]).To(HaveKeyWithValue("market", map[string]any{"name": "acme", "source": url}))

		// On a terminal, the summary shows it before Apply, and only there.
		sp := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes("project")}, review: []string{cli.ReviewApply}}
		r = lolaP(sp, "install", "@acme/formatter", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(ContainSubstring("Summary:\n  @acme/formatter → " + url + " path tools/formatter @ " + commit + "\n"))
		Expect(strings.Count(r.err, "@acme/formatter → ")).To(Equal(1))
	})

	It("refuses an entry whose module names itself otherwise, hinting its install by source under that name with the run's flags", func() {
		named := filepath.Join(tmp, "named")
		writeTree(named, map[string]string{".claude-plugin/marketplace.json": `{"name": "named", "owner": {"name": "N"},
  "plugins": [{"name": "tool", "source": "./tool"}]}`, "tool/lola.yml": "name: other\n", "tool/commands/o.md": "O\n"})
		Expect(lola("market", "--add", named).code).To(Equal(0))
		r := lola("install", "@named/tool", "--user", "-t", "claude-code", "-y")
		Expect(r.code).To(Equal(1))
		hint := "lola install " + named + " --target claude-code --user --yes --path tool"
		Expect(r.err).To(Equal("error: marketplace named lists tool, and the module at its source names itself other; lola installs a " +
			"catalog entry only under the entry's name\n  hint: " + hint + "\n"))
		again := lola(shellWords(hint)[1:]...)
		Expect(again.code).To(Equal(0), again.err)
		Expect(filepath.Join(os.Getenv("HOME"), ".claude", "commands", "o.md")).To(BeARegularFile())
	})

	It("market flags: one action, --ref/--path only with --add, names xor --all, -n writes nothing", func() {
		for args, want := range map[string][2]string{
			"acme --add " + url + " --update": {"--add and --update cannot be combined; lola market takes one action per run",
				"  hint: lola market acme --add " + url + "\n  hint: lola market acme --update\n"},
			"--update --ref main":       {"--ref applies to --add only", "  hint: lola market --update\n"},
			"acme --path x":             {"--path applies to --add only", "  hint: lola market acme\n"},
			"--add " + url + " --all":   {"--all applies to --update only", "  hint: lola market --add " + url + "\n"},
			"--all":                     {"--all applies to --update only", "  hint: lola market\n"},
			"acme --path x --all":       {"--path applies to --add only; --all applies to --update only", "  hint: lola market acme\n"},
			"acme --update --all":       {"--all updates every marketplace; drop the names or --all", "  hint: lola market acme --update\n  hint: lola market --update --all\n"},
			"--update":                  {"name the marketplaces to update, or update every one with --all", "  hint: lola market --update --all\n"},
			"a b --add " + url:          {"--add registers one marketplace; give it one name", "  hint: lola market a --add " + url + "\n  hint: lola market b --add " + url + "\n"},
			"a b":                       {"name one marketplace to show", "  hint: lola market\n"},
			"acme --remove --update":    {"--update and --remove cannot be combined; lola market takes one action per run", "  hint: lola market acme --update\n  hint: lola market acme --remove\n"},
			"acme -y":                   {"--yes applies to --remove only", "  hint: lola market acme\n"},
			"acme --add " + url + " -y": {"--yes applies to --remove only", "  hint: lola market acme --add " + url + "\n"},
			"a b --remove":              {"--remove forgets one marketplace; name it", ""},
			"--enable acme":             {"unknown flag: --enable", ""},
			"acme --add " + url + " --update --remove": {"--add, --update and --remove cannot be combined; lola market takes one action per run",
				"  hint: lola market acme --add " + url + "\n  hint: lola market acme --update\n  hint: lola market acme --remove\n"},
		} {
			r := lola(append([]string{"market"}, strings.Fields(args)...)...)
			Expect(r.code).To(Equal(1), args)
			Expect(r.err).To(Equal("error: "+want[0]+"\n"+want[1]), args)
		}
		r := lola("market", "--add", url, "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("→ would add marketplace acme: 2 entries"))
		for _, f := range []string{p.Markets, p.MarketCache, p.MarketsLock} {
			Expect(f).NotTo(BeAnExistingFile(), "-n writes nothing")
		}
		Expect(lola("market", "--add", url, "--scope", "both").code).To(Equal(0), "scope flags change nothing")
		Expect(lola("market", "--user").out).To(HavePrefix("NAME"))
		r = lola("market", "-C", filepath.Join(tmp, "nope"))
		Expect(r.err).To(ContainSubstring("-C " + filepath.Join(tmp, "nope") + ": not a directory"))
	})

	It("asks another name on a terminal when the name is another source's, pre-filled with a free one", func() {
		Expect(lola("market", "--add", url).code).To(Equal(0))
		other := filepath.Join(tmp, "authoring")
		writeTree(other, map[string]string{".claude-plugin/marketplace.json": marketCatalog, "tools/formatter/x.md": "x"})
		r := lola("market", "--add", other)
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("error: acme is already a marketplace, from " + url + "\n  hint: lola market acme-2 --add " + other + " --path .claude-plugin/marketplace.json\n"))
		sp := &scripted{names: []string{"acme", "mine"}}
		r = lolaP(sp, "market", "--add", other)
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.nameSteps).To(Equal([]cli.MarketNameStep{{Why: "acme is already a marketplace, from " + url, Value: "acme-2"},
			{Why: "acme is already a marketplace, from " + url, Value: "acme-2", Problem: "acme is already a marketplace, from " + url}}))
		Expect(r.out).To(HavePrefix("✓ marketplace mine: 2 entries (Claude marketplace.json) from " + other + "\n"))
		Expect(r.out).To(ContainSubstring("→ the catalog calls itself acme; it is your marketplace mine\n"))
		r = lola("market", "acme", "--add", url)
		Expect(r.err).To(Equal("error: acme is already this marketplace\n  hint: lola market acme --update\n"))
		r = lola("market", "acme", "--add", url, "--json")
		Expect(r.err).To(MatchJSON(`{"error": "acme is already this marketplace", "hints": ["lola market acme --update --json"]}`),
			"a hint keeps the run's output flags")
	})

	It("asks a name on a terminal for a catalog whose own name no marketplace can have, pre-filled with its slug", func() {
		py := filepath.Join(tmp, "py")
		writeTree(py, map[string]string{"marketplace.yml": "name: Lola General Marketplace\nmodules: []\n"})
		r := lola("market", "--add", py)
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("error: the catalog names itself Lola General Marketplace, which cannot name a marketplace; give a name before --add\n"+
			"  hint: lola market lola-general-marketplace --add "+py+" --path marketplace.yml\n"), "off a terminal, refused with the slug's --add")
		sp := &scripted{names: []string{"general"}}
		r = lolaP(sp, "market", "--add", py)
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.nameSteps).To(Equal([]cli.MarketNameStep{{Why: "the catalog names itself Lola General Marketplace, which cannot name a marketplace",
			Value: "lola-general-marketplace"}}))
		Expect(r.out).To(HavePrefix("✓ marketplace general: 0 entries (Python lola YAML) from " + py + "\n"))
	})

	It("prints notices as [] under --json, an entry's notices by name, and a failed update's reason once", func() {
		py := filepath.Join(tmp, "py")
		writeTree(py, map[string]string{"marketplace.yml": "name: py\nmodules:\n  - name: hooked\n    repository: https://lola.test/h.git\n" +
			"    hooks: {pre-install: scripts/pre.sh}\n"})
		r := lola("market", "--add", py, "--json", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).NotTo(ContainSubstring("null"))
		Expect(r.out).To(ContainSubstring(`"notices": []`))
		r = lola("market", "--add", py, "--json")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring(`"text": "browse: lola install @py --json"`), "the browse line keeps the run's output flags")
		Expect(lola("market", "py").out).To(ContainSubstring("→ hooked: catalog install scripts are ignored\n"))

		writeTree(py, map[string]string{"marketplace.yml": "name: py\n"})
		r = lola("market", "py", "--update")
		Expect(r.code).To(Equal(1))
		Expect(r.out).To(Equal("✗ marketplace py was not updated (the error says why)\n"))
		Expect(r.err).To(Equal("error: the catalog marketplace.yml has no modules\n"))
	})

	It("reports every marketplace an --update --all could not update, in one error", func() {
		for _, n := range []string{"a", "b", "c"} {
			writeTree(filepath.Join(tmp, n), map[string]string{"marketplace.yml": "name: " + n + "\nmodules: []\n"})
			r := lola("market", "--add", filepath.Join(tmp, n))
			Expect(r.code).To(Equal(0), r.err)
		}
		for _, n := range []string{"b", "c"} {
			writeTree(filepath.Join(tmp, n), map[string]string{"marketplace.yml": "name: " + n + "\n"})
		}
		r := lola("market", "--update", "--all")
		Expect(r.code).To(Equal(1))
		Expect(r.out).To(ContainSubstring("✗ marketplace b was not updated (the error says why)\n"))
		Expect(r.out).To(ContainSubstring("✗ marketplace c was not updated (the error says why)\n"))
		Expect(r.err).To(Equal("error: 2 of 3 marketplaces were not updated: b: the catalog marketplace.yml has no modules; " +
			"c: the catalog marketplace.yml has no modules\n"))

		writeTree(filepath.Join(tmp, "c"), map[string]string{"marketplace.yml": "name: c\nmodules: []\n"})
		r = lola("market", "--update", "--all")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("error: 1 of 3 marketplaces was not updated: b: the catalog marketplace.yml has no modules\n"))

		writeTree(filepath.Join(tmp, "c"), map[string]string{"marketplace.yml": "name: c\n"})
		r = lola("market", "--update", "--all", "--json")
		Expect(r.code).To(Equal(1))
		var doc struct {
			Error   string `json:"error"`
			Markets []struct {
				Name string `json:"name"`
			} `json:"markets"`
			Updates []struct {
				Market, Outcome, Error string
			} `json:"updates"`
		}
		Expect(r.out).To(BeEmpty(), "under --json the failure is the document")
		Expect(json.Unmarshal([]byte(r.err), &doc)).To(Succeed(), r.err)
		Expect(doc.Error).To(HavePrefix("2 of 3 marketplaces were not updated: "))
		Expect(doc.Markets).To(HaveLen(1), "the marketplaces the run read")
		Expect(doc.Markets[0].Name).To(Equal("a"))
		Expect(doc.Updates).To(HaveLen(3))
		for i, want := range [][2]string{{"a", "up_to_date"}, {"b", "failed"}, {"c", "failed"}} {
			Expect([2]string{doc.Updates[i].Market, doc.Updates[i].Outcome}).To(Equal(want))
		}
		Expect(doc.Updates[1].Error).To(Equal("the catalog marketplace.yml has no modules"))
	})

	It("shows a long catalog by name, its descriptions cut to the width, its skips by reason, and pages it on a terminal", func() {
		long := "Formats code in every language you have ever heard of, and a few more besides"
		publish(map[string]string{".claude-plugin/marketplace.json": `{"name": "acme", "owner": {"name": "Acme"}, "plugins": [
  {"name": "zeta", "source": {"source": "url", "url": "https://lola.test/z.git"}, "description": "Short"},
  {"name": "npmz", "source": {"source": "npm", "package": "z"}},
  {"name": "alpha", "source": {"source": "url", "url": "https://lola.test/alpha.git"}, "description": "` + long + `"},
  {"name": "npma", "source": {"source": "npm", "package": "a"}},
  {"name": "cmd", "source": {"source": "command", "command": "x"}}
]}`})
		r := lola("market", "--add", url)
		Expect(r.code).To(Equal(0), r.err)
		GinkgoT().Setenv("COLUMNS", "66")
		r = lola("market", "acme")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("Entries (2):\n" +
			"  alpha  -  git  https://lola.test/alpha.git  Formats code in eve…\n" + // 66 columns
			"  zeta   -  git  https://lola.test/z.git      Short\n"))
		GinkgoT().Setenv("COLUMNS", "60")
		Expect(lola("market", "acme").out).To(ContainSubstring("Entries (2):\n"+
			"  alpha  -  git  https://lola.test/alpha.git\n    Formats code in every language you have ever heard of, …\n"+
			"  zeta   -  git  https://lola.test/z.git\n    Short\n"), "under 20 columns left: each description on its own line")
		GinkgoT().Setenv("COLUMNS", "90")
		r = lola("market", "acme")
		Expect(r.out).To(HaveSuffix("Skipped (3):\n" +
			"  npm sources are not supported: installing one runs a package manager (2): npma, npmz\n" +
			"  command sources run a program; lola never does: cmd\n"))
		for _, args := range [][]string{{"--plain"}, {"--json"}} {
			r = lola(append([]string{"market", "acme"}, args...)...)
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(ContainSubstring("in every language you have ever heard of,"), "%v keeps the full text", args)
		}
		GinkgoT().Setenv("COLUMNS", "")
		Expect(lola("market", "acme").out).To(ContainSubstring("  "+long+"\n"),
			"off a terminal with no COLUMNS, nothing is cut")

		paged := filepath.Join(tmp, "paged")
		Expect(os.WriteFile(filepath.Join(tmp, "bin", "fakepager"), []byte("#!/bin/sh\ncat > \"$PAGED\"\n"), 0o755)).To(Succeed())
		GinkgoT().Setenv("PAGED", paged)
		GinkgoT().Setenv("LOLA_PAGER", "fakepager")
		onTerminal := func(args ...string) result {
			root := cli.NewRootCmd(os.Getenv, cli.WithTerminals(true, true))
			var out, errb bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&errb)
			root.SetIn(strings.NewReader(""))
			root.SetArgs(args)
			code := cli.Execute(root)
			return result{out: out.String(), err: errb.String(), code: code}
		}
		r = onTerminal("market", "acme")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(BeEmpty(), "the listing went to the pager")
		Expect(os.ReadFile(paged)).To(ContainSubstring("Entries (2):\n  alpha "))
		Expect(os.Remove(paged)).To(Succeed())
		r = onTerminal("market", "acme", "--no-pager")
		Expect(r.out).To(ContainSubstring("Entries (2):\n  alpha "))
		Expect(paged).NotTo(BeAnExistingFile())
	})

	It("cuts a long version in the table so it does not widen every row, and keeps it whole elsewhere", func() {
		version := "1.0.0-rc.1+build.20261022.abcdef0" // 33 columns
		publish(map[string]string{".claude-plugin/marketplace.json": `{"name": "acme", "owner": {"name": "Acme"}, "plugins": [
  {"name": "alpha", "version": "1.0.0", "source": {"source": "url", "url": "https://lola.test/a.git"}, "description": "Short"},
  {"name": "beta", "version": "` + version + `", "source": {"source": "url", "url": "https://lola.test/b.git"}, "description": "Short"}
]}`})
		r := lola("market", "--add", url)
		Expect(r.code).To(Equal(0), r.err)
		GinkgoT().Setenv("COLUMNS", "80")
		r = lola("market", "acme")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("Entries (2):\n" +
			"  alpha  1.0.0             git  https://lola.test/a.git  Short\n" +
			"  beta   1.0.0-rc.1+buil…  git  https://lola.test/b.git  Short\n"))
		for _, args := range [][]string{{"--plain"}, {"--json"}} {
			r = lola(append([]string{"market", "acme"}, args...)...)
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(ContainSubstring(version), "%v keeps the version whole", args)
		}
		GinkgoT().Setenv("COLUMNS", "")
		Expect(lola("market", "acme").out).To(ContainSubstring("  beta   "+version+"  git"), "off a terminal with no COLUMNS, nothing is cut")
	})

	It("fits every line of a catalog shaped like claude-plugins-official to a narrow terminal", func() {
		long := strings.Repeat("Does many useful things for your code base, ", 6)
		var plugins []string
		// The longest names claude-plugins-official lists (53, 36 and 31 columns) and short ones.
		names := []string{"oracle-ai-data-platform-workbench-databricks-migrator", "unreal-engine-skills-for-claude-code",
			"blackrock-advisor-center-plugin", "zed", "rc"}
		for _, n := range names {
			plugins = append(plugins, `{"name": "`+n+`", "description": "`+long+`", "source": {"source": "git-subdir", "url": `+
				`"https://lola.test/some-very-long-organisation-name/a-very-long-repository-name-indeed.git", "path": "plugins/`+n+`", "ref": "v1.2.3"}}`)
		}
		for _, n := range []string{"clangd-lsp", "gopls-lsp", "pyright-lsp"} {
			plugins = append(plugins, `{"name": "`+n+`", "source": {"source": "url", "url": "https://lola.test/`+n+`.git"}, "lspServers": {"x": {}}}`)
		}
		publish(map[string]string{".claude-plugin/marketplace.json": `{"name": "acme", "owner": {"name": "Acme"}, "description": "` + long +
			`", "plugins": [` + strings.Join(plugins, ",\n") + `]}`})
		r := lola("market", "--add", url)
		Expect(r.code).To(Equal(0), r.err)
		for _, cols := range []int{60, 80, 120} {
			GinkgoT().Setenv("COLUMNS", strconv.Itoa(cols))
			r = lola("market", "acme")
			Expect(r.code).To(Equal(0), r.err)
			for _, line := range strings.Split(strings.TrimSuffix(r.out, "\n"), "\n") {
				Expect(utf8.RuneCountInString(line)).To(BeNumerically("<=", cols), "%d columns: %q", cols, line)
			}
			Expect(r.out).To(MatchRegexp(`\n  description  Does many[^\n]*\n {15}\S`), "%d columns: a wrapped field keeps to its column", cols)
			for _, n := range names[2:] {
				Expect(r.out).To(MatchRegexp(`\n  `+n+` +- +git +https://lola\.te`), "%d columns: a short name's row keeps its version, kind and source", cols)
			}
			for _, n := range names[:2] {
				Expect(r.out).To(MatchRegexp(`\n  `+n+`\n    - +git +https://lola\.te`), "%d columns: a long name whole, its row under it", cols)
			}
			Expect(r.out).To(ContainSubstring("https://lola.te"), "a source is cut")
			Expect(r.out).NotTo(ContainSubstring("a-very-long-repository-name"), "a source is cut")
			for _, name := range []string{"clangd-lsp", "gopls-lsp", "pyright-lsp"} {
				Expect(r.out).To(MatchRegexp(`[ ,]`+name+`[,\n]`), "a skip's names are wrapped at spaces, never cut")
			}
		}
		GinkgoT().Setenv("COLUMNS", "40")
		r = lola("market", "acme")
		Expect(r.code).To(Equal(0), r.err)
		lines := strings.Split(strings.TrimSuffix(r.out, "\n"), "\n")
		Expect(lines).To(ContainElement("  "+names[0]), "a name wider than the terminal prints whole on its own line, never cut")
		Expect(lines).To(ContainElement(MatchRegexp(`^  source +`+regexp.QuoteMeta(url)+`$`)), "a source wider than its column is not cut either")
		for _, line := range lines {
			if utf8.RuneCountInString(line) > 40 {
				Expect(len(strings.Fields(line))).To(BeNumerically("<=", 2), "only one word too wide to fit, after its label, passes the width: %q", line)
			}
		}
		GinkgoT().Setenv("COLUMNS", "120")
		Expect(lola("market", "acme").out).To(ContainSubstring(" https://lola.test/some-very-long-organi…  Does"), "at most 40 columns of a source")
		GinkgoT().Setenv("COLUMNS", "80")
		Expect(lola("market", "acme").out).To(MatchRegexp(`\n  zed +- +git +https://\S+…\n    Does many useful things`),
			"too little room left: the description goes on its own line")
		GinkgoT().Setenv("COLUMNS", "200")
		Expect(lola("market", "acme").out).To(MatchRegexp(`\n  zed +- +git +https://lola.test/some-very-long-organi… +Does many useful things for`),
			"room enough: the description fills the row")
		GinkgoT().Setenv("COLUMNS", "")
		Expect(lola("market", "acme").out).To(MatchRegexp(`a-very-long-repository-name-indeed\.git path plugins/zed ref v1\.2\.3 +`+
			regexp.QuoteMeta(strings.TrimSpace(long))+"\n"), "a pipe keeps it whole")
	})

	It("lists a registration not fetched yet, and one whose cache is unreadable, with the --update that fetches it", func() {
		Expect(os.MkdirAll(filepath.Dir(p.Markets), 0o700)).To(Succeed())
		Expect(os.WriteFile(p.Markets, []byte("version: 1\nmarkets:\n  - {name: acme, source: \""+url+"\", path: .claude-plugin/marketplace.json}\n"+
			"  - {name: broken, source: \""+url+"\"}\n"), 0o600)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(p.MarketCache, "broken"), 0o700)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(p.MarketCache, "broken", "source.json"), []byte("{}"), 0o600)).To(Succeed())
		r := lola("market")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(MatchRegexp(`acme +- +- +- +not fetched +` + regexpQuote(url)))
		Expect(r.out).To(ContainSubstring("→ acme is not fetched → lola market acme --update\n"))
		Expect(r.out).To(ContainSubstring("⚠ broken: " + filepath.Join(p.MarketCache, "broken", "source.json") + ": version 0 is not 1 → lola market broken --update\n"))
		r = lola("market", "acme")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HaveSuffix("→ not fetched → lola market acme --update\n"))
		Expect(lola("market", "acme", "--update").code).To(Equal(0))
		Expect(lola("market", "broken", "--update").code).To(Equal(0))
		Expect(lola("market").out).NotTo(ContainSubstring("not fetched"))
	})
})

var _ = Describe("lola market --remove, the Python verbs and every hint", func() {
	var tmp, url string
	var p paths.Paths
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		GinkgoT().Chdir(tmp)
		url = filepath.Join(tmp, "acme")
		writeTree(url, map[string]string{".claude-plugin/marketplace.json": marketCatalog, "tools/formatter/x.md": "x"})
		Expect(lola("market", "--add", url).code).To(Equal(0))
	})

	It("market --remove asks, refuses off a terminal without -y, and drops cache and registration", func() {
		r := lola("market", "acme", "--remove")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("error: lola market --remove asks before it forgets a marketplace, and there is no terminal to ask on\n" +
			"  hint: lola market acme --remove -y\n"))
		sp := &scripted{confirm: []bool{false}}
		r = lolaP(sp, "market", "acme", "--del")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(Equal("aborted; nothing was removed.\n"))
		Expect(sp.calls).To(Equal([]string{"confirm:Forget marketplace acme (" + url + ") and drop its cached catalog?"}))
		r = lola("market", "acme", "--erase", "-n")
		Expect(r.out).To(Equal("→ would forget marketplace acme and drop its cached catalog\n"))
		r = lola("market", "acme", "--rm", "-y", "--json")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(MatchJSON(`{"removed": ["acme"], "notices": [], "dry_run": false}`))
		Expect(filepath.Join(p.MarketCache, "acme")).NotTo(BeAnExistingFile())
		Expect(lola("market").out).To(HavePrefix("no marketplaces yet"))
	})

	It("python market verbs are refused with the translated command", func() {
		for args, want := range map[string]string{
			"add general " + url: "error: lola market add is the Python lola's form; in lola: lola market <name> --add <source>\n" +
				"  hint: lola market general --add " + url + "\n",
			"update --all": "error: lola market update is the Python lola's form; in lola: lola market --update --all\n" +
				"  hint: lola market --update --all\n",
			"ls acme":    "error: lola market ls is the Python lola's form; in lola: lola market <name>\n  hint: lola market acme\n",
			"rm acme -y": "error: lola market rm is the Python lola's form; in lola: lola market <name> --remove\n  hint: lola market acme --remove -y\n",
			"set --disable acme -y": "error: lola market set is the Python lola's form; in lola: lola market <name> --remove; lola has no disabled state\n" +
				"  hint: lola market acme --remove\n",
			"set --enable acme": "error: acme is already active; lola has no disabled state\n  hint: lola market acme\n",
			"add --add " + url:  "error: add cannot name a marketplace: it is how the Python lola spelled lola market add\n",
			"search":            "error: lola market search takes one query; in lola: lola search <query>\n",
		} {
			r := lola(append([]string{"market"}, strings.Fields(args)...)...)
			Expect(r.code).To(Equal(1), args)
			Expect(r.err).To(Equal(want), args)
		}
	})

	It("nothing in a catalog runs a command", func() {
		// The install summary says what the catalog asked that lola does not do (Plan 6c §12 spec 6).
		marker := filepath.Join(tmp, "MARKER")
		hooked := filepath.Join(tmp, "hooked")
		writeTree(hooked, map[string]string{"commands/h.md": "H\n"})
		for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"commit", "-q", "-m", "init"}} {
			out, err := exec.Command("git", append([]string{"-C", hooked}, args...)...).CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), string(out))
		}
		out, err := exec.Command("git", "clone", "-q", "--bare", hooked, filepath.Join(tmp, "mods", "h.git")).CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))
		mods := testenv.ServeGitTLS(filepath.Join(tmp, "mods"), nil)
		py := filepath.Join(tmp, "py")
		writeTree(py, map[string]string{"marketplace.yml": "name: py\nmodules:\n  - name: hooked\n    repository: " + mods + "/h.git\n" +
			"    hooks: {pre-install: touch " + marker + ", post-install: touch " + marker + "}\n"})
		Expect(lola("market", "--add", py).code).To(Equal(0))
		r := lola("install", "@py/hooked", "--user", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("→ hooked: catalog install scripts are ignored\n"))
		Expect(marker).NotTo(BeAnExistingFile())
	})

	It("every marketplace hint is a complete command that runs, and no hint adds -y for a different question", func() {
		other := filepath.Join(tmp, "other")
		writeTree(other, map[string]string{".claude-plugin/marketplace.json": marketCatalog, "tools/formatter/x.md": "x"})
		general := filepath.Join(tmp, "general")
		writeTree(general, map[string]string{"marketplace.yml": "name: Lola General Marketplace\nmodules: []\n",
			"two/.claude-plugin/marketplace.json": marketCatalog, "two/marketplace.yml": "name: two\nmodules: []\n"})
		writeTree(filepath.Join(tmp, "work"), map[string]string{"marketplace.json": marketCatalog})
		slugged := filepath.Join(tmp, "slugged")
		writeTree(slugged, map[string]string{"marketplace.yml": "name: Lola General Marketplace\nmodules: []\n",
			"lola-general-marketplace.yml": "name: other\nmodules: []\n"})
		files := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(marketCatalog)) }))
		DeferCleanup(files.Close)
		// 6c.2: a marketplace whose entries come from a host by name (ServeGitTLS, Plan 6c §12), for
		// the pin, renamed-module, dependency and catalog-move hints.
		git := func(dir string, args ...string) string {
			GinkgoHelper()
			out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), string(out))
			return strings.TrimSpace(string(out))
		}
		kit := filepath.Join(tmp, "kitwork")
		writeTree(kit, map[string]string{"commands/k.md": "K\n"})
		git(kit, "init", "-q", "-b", "main")
		git(kit, "add", "-A")
		git(kit, "commit", "-q", "-m", "init")
		git(kit, "checkout", "-q", "-b", "fork")
		writeTree(kit, map[string]string{"commands/k.md": "Fork\n"})
		git(kit, "commit", "-q", "-am", "fork")
		fork := git(kit, "rev-parse", "HEAD")
		git(kit, "checkout", "-q", "main")
		git(tmp, "clone", "-q", "--bare", kit, filepath.Join(tmp, "mods", "kit.git"))
		mods := testenv.ServeGitTLS(filepath.Join(tmp, "mods"), nil)
		pins := filepath.Join(tmp, "pins")
		pinsCatalog := func(movedRef string) {
			GinkgoHelper()
			writeTree(pins, map[string]string{".claude-plugin/marketplace.json": `{"name": "pins", "owner": {"name": "P"}, "plugins": [
  {"name": "far", "source": {"source": "url", "url": "` + mods + `/kit.git", "ref": "main", "sha": "` + fork + `"}},
  {"name": "moved", "source": {"source": "url", "url": "` + mods + `/kit.git", "ref": "` + movedRef + `"}},
  {"name": "near", "source": "./near"}, {"name": "deps", "source": "./deps", "dependencies": ["near"]}, {"name": "ren", "source": "./ren"}]}`,
				"near/commands/n.md": "N\n", "deps/commands/d.md": "D\n", "ren/lola.yml": "name: renamed-other\n", "ren/commands/r.md": "R\n"})
		}
		pinsCatalog("main")
		Expect(lola("market", "--add", pins).code).To(Equal(0))
		for _, args := range [][]string{
			{"market", "x", "--add", files.URL + "/m.json", "--ref", "main", "--path", "a"}, // a catalog file: neither flag
			{"market", "--add", slugged},                  // a slug with a catalog of its own name: --path names the one found
			{"market", "update", "acme", "--all"},         // names and --all: both forms
			{"market", "acme", "--add", url, "-n"},        // the same marketplace: its --update
			{"market", "acme", "--add", other},            // another source: a free name's --add
			{"market", "--add", general, "-n"},            // a catalog name lola cannot use: its slug
			{"market", "--add", general, "--path", "two"}, // two catalogs: one --path per file
			{"market", "--add", "work/marketplace.json"},  // a local catalog file: the folder that holds it
			{"market", "--add", general, "--ref", "main"}, // --ref of a folder: the run without it
			{"market", "--update"},                        // neither names nor --all
			{"market", "acme", "--update", "--all", "-n"}, // both
			{"market", "nope"},                            // no such marketplace: the listing
			{"market", "nope", "--update"},                // the same, before any fetch
			{"market", "acmee"},                           // a typo: its close name, then the listing
			{"market", "acmee", "--update", "-n"},         // the same, keeping -n
			{"market", "acmee", "--remove", "-y"},         // the same, never with -y
			{"market", "Acme_X", "--add", other, "-n"},    // a name with uppercase: its lowercase form
			{"market", "add", "x", other, "-n"},           // the Python verbs
			{"market", "ls", "acme"},
			{"market", "show", "nope"},
			{"market", "rm", "acme"},
			{"market", "update"},
			{"market", "update", "acme"},
			{"market", "search", "fmt"},
			{"market", "set", "--disable", "acme"},
			{"market", "set", "--enable", "acme"},
			{"market", "acme", "--remove", "-y", "--update"},                   // two actions: its --remove -y runs
			{"market", "acme", "--remove"},                                     // off a terminal: its own --remove -y
			{"install", "formatter", "--user", "-t", "claude-code"},            // a bare name only a catalog lists: its address
			{"install", "@nope/formatter", "--user", "-t", "claude-code"},      // no such marketplace: the listing
			{"install", "@acme/formater", "--user", "-t", "claude-code", "-n"}, // no such entry: the marketplace, naming close ones
			{"install", "@acme/npmx", "--user", "-t", "claude-code"},           // a skipped entry: the marketplace, which says why
			{"install", "@acme", "--user", "-t", "claude-code"},                // a marketplace alone, off a terminal: the marketplace
			{"install", "@acme/formatter", "--path", "tools", "--user", "-t", "claude-code", "-n", "-y"}, // the catalog gives the path: the run without it
			{"cache", "add", "@acme/formatter", "--path", "tools"},                                       // the same for cache add
			{"check", "@nope/formatter"},                            // and for check
			{"install", "@pins/far", "--user", "-t", "claude-code"}, // a pin its ref does not reach: the marketplace's --update
			{"install", "@pins/ren", "--user", "-t", "claude-code"}, // a module that names itself otherwise: its install by source
		} {
			// acme is added again when a hint run before forgot it.
			if lola("market", "acme").code != 0 {
				Expect(lola("market", "--add", url).code).To(Equal(0))
			}
			r := lola(args...)
			Expect(r.code).To(Equal(1), strings.Join(args, " "))
			// The one hint that may add -y approves exactly the --remove refused off a terminal.
			ownY := slices.Contains(args, "--remove") && strings.Contains(r.err, "there is no terminal to ask on")
			for _, asJSON := range []bool{false, true} {
				run, hints, refused := args, []string{}, r.err
				if asJSON {
					run = append(slices.Clone(args), "--json")
					var doc struct {
						Hints []string `json:"hints"`
					}
					j := lola(run...)
					Expect(json.Unmarshal([]byte(strings.Split(strings.TrimSpace(j.err), "\n")[0]), &doc)).To(Succeed(), j.err)
					hints, refused = doc.Hints, j.err
				} else {
					for _, line := range strings.Split(r.err, "\n") {
						if h, ok := strings.CutPrefix(line, "  hint: "); ok {
							hints = append(hints, h)
						}
					}
				}
				Expect(hints).NotTo(BeEmpty(), strings.Join(run, " "))
				for _, h := range hints {
					words := shellWords(h)
					Expect(words[0]).To(Equal("lola"), h)
					Expect(slices.Contains(words, "--json")).To(Equal(asJSON), "a hint keeps the run's output flags: "+h)
					switch {
					case ownY:
						Expect(words).To(ContainElement("-y"), h)
					case !slices.Contains(args, "-y"):
						Expect(words).NotTo(ContainElement("-y"), h)
					}
					if slices.Contains(args, "-n") && slices.Contains(words, "--add") {
						Expect(words).To(ContainElement("-n"), h)
					}
					again := lola(words[1:]...)
					Expect(again.err).NotTo(Equal(refused), h)
					// It runs, or fails for a reason of its own, never for how it is written.
					Expect(again.err).NotTo(Or(ContainSubstring("unknown flag"), ContainSubstring("applies to"),
						ContainSubstring("cannot be combined"), ContainSubstring("Python lola's form")), h)
					if lola("market", "acme").code != 0 {
						Expect(lola("market", "--add", url).code).To(Equal(0))
					}
					// What a hint fetched leaves the cache, so a bare name is refused again.
					lola("cache", "rm", "formatter", "-y")
				}
			}
		}

		// The hints of these refusals run verbatim and succeed, and then the run does too when the
		// hint was what it lacked. userYML is user.yml before the cases that write it.
		var userYML []byte
		for _, c := range []struct {
			prepare func()
			args    []string
			keeps   []string // the run's flags the hint carries, beside the output flags
			then    bool
		}{
			{nil, []string{"install", "@pins/ren", "@pins/near", "--user", "-t", "claude-code"}, // a renamed entry among others: its install by source alone
				[]string{"--user", "--target", "claude-code"}, false},
			{func() { Expect(os.RemoveAll(filepath.Join(p.MarketCache, "pins"))).To(Succeed()) }, // a marketplace not fetched: its --update
				[]string{"install", "@pins/near", "--user", "-t", "claude-code"}, []string{}, true},
			{func() { // an @ source in user.yml: the install of its address, which replaces it
				if userYML == nil {
					var err error
					userYML, err = os.ReadFile(p.UserManifest)
					Expect(err).NotTo(HaveOccurred())
				}
				lola("uninstall", "near", "--user", "-y")
				b, err := os.ReadFile(p.UserManifest)
				Expect(err).NotTo(HaveOccurred())
				Expect(os.WriteFile(p.UserManifest, append(b, "  - {name: near, source: '@pins/near'}\n"...), 0o600)).To(Succeed())
			}, []string{"sync", "--user"}, []string{"--user"}, true},
			{func() { // an @ source in a profile: the marketplace, which shows the source to copy
				Expect(os.Remove(p.UserManifest)).To(Or(Succeed(), MatchError(os.ErrNotExist)))
				writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
				Expect(lola("profile", "go", "--on", "--user").code).To(Equal(0))
				writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: near, source: '@pins/near'}\n"})
			}, []string{"sync", "--user"}, []string{}, false},
		} {
			args := c.args
			for _, asJSON := range []bool{false, true} {
				if c.prepare != nil {
					c.prepare()
				}
				run, hints := slices.Clone(args), []string{}
				if asJSON {
					run = append(run, "--json")
				}
				r := lola(run...)
				Expect(r.code).To(Equal(1), strings.Join(run, " "))
				if asJSON {
					var doc struct {
						Hints []string `json:"hints"`
					}
					Expect(json.Unmarshal([]byte(strings.Split(strings.TrimSpace(r.err), "\n")[0]), &doc)).To(Succeed(), r.err)
					hints = doc.Hints
				} else {
					for _, line := range strings.Split(r.err, "\n") {
						if h, ok := strings.CutPrefix(line, "  hint: "); ok {
							hints = append(hints, h)
						}
					}
				}
				Expect(hints).To(HaveLen(1), strings.Join(run, " ")+": "+r.err)
				words := shellWords(hints[0])
				Expect(slices.Contains(words, "--json")).To(Equal(asJSON), "a hint keeps the run's output flags: "+hints[0])
				Expect(words).To(ContainElements(c.keeps), "a hint keeps the run's flags: "+hints[0])
				again := lola(words[1:]...)
				Expect(again.code).To(Equal(0), hints[0]+": "+again.err)
				if c.then {
					r = lola(run...)
					Expect(r.code).To(Equal(0), strings.Join(run, " ")+": "+r.err)
				}
			}
		}

		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		Expect(lola("profile", "go", "--off", "--user").code).To(Equal(0))
		Expect(os.WriteFile(p.UserManifest, userYML, 0o600)).To(Succeed())

		// Hints a run that succeeds gives in its notices (text → command) run too.
		for _, c := range []struct {
			prepare func()
			args    []string
		}{
			{nil, []string{"install", "@pins/deps", "--user", "-t", "claude-code"}}, // a dependency the marketplace offers: its install
			{func() { // a ref the catalog moved: sync --update's install of the address
				pinsCatalog("main")
				Expect(lola("market", "pins", "--update").code).To(Equal(0))
				Expect(lola("install", "@pins/moved", "--user", "-t", "claude-code").code).To(Equal(0))
				pinsCatalog("fork")
				Expect(lola("market", "pins", "--update").code).To(Equal(0))
			}, []string{"sync", "--update", "moved", "--user"}},
		} {
			for _, asJSON := range []bool{false, true} {
				if c.prepare != nil {
					c.prepare()
				}
				run := slices.Clone(c.args)
				if asJSON {
					run = append(run, "--json")
				}
				r := lola(run...)
				Expect(r.code).To(Equal(0), r.err)
				var cmds []string
				if asJSON {
					var doc struct {
						Notices []struct {
							Text string `json:"text"`
						} `json:"notices"`
					}
					Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed(), r.out)
					for _, n := range doc.Notices {
						if _, cmd, ok := strings.Cut(n.Text, " → lola "); ok {
							cmds = append(cmds, "lola "+cmd)
						}
					}
				} else {
					for _, line := range strings.Split(r.out+r.err, "\n") {
						if _, cmd, ok := strings.Cut(line, " → lola "); ok {
							cmds = append(cmds, "lola "+cmd)
						}
					}
				}
				Expect(cmds).NotTo(BeEmpty(), strings.Join(run, " "))
				for _, h := range cmds {
					words := shellWords(h)
					Expect(slices.Contains(words, "--json")).To(Equal(asJSON), "a hint keeps the run's output flags: "+h)
					again := lola(words[1:]...)
					Expect(again.code).To(Equal(0), h+": "+again.err)
				}
			}
		}
		// A catalog that lists an entry from another source says what to do, and names no command
		// that the cache would refuse.
		catalog, err := os.ReadFile(filepath.Join(pins, ".claude-plugin", "marketplace.json"))
		Expect(err).NotTo(HaveOccurred())
		writeTree(pins, map[string]string{".claude-plugin/marketplace.json": strings.Replace(string(catalog),
			`"url": "`+mods+`/kit.git", "ref": "fork"`, `"url": "`+mods+`/other.git", "ref": "fork"`, 1)})
		Expect(lola("market", "pins", "--update").code).To(Equal(0))
		r := lola("sync", "--update", "moved", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("your marketplace pins lists moved from " + mods + "/other.git; moved updates from its own source (lola caches one moved per machine"))
		Expect(r.out).NotTo(ContainSubstring("→ lola install"))
	})
})
