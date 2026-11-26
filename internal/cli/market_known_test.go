package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/cli"
	"github.com/trevor-vaughan/lola-go/internal/market"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("lola market known and enable", func() {
	var tmp, url, pin string
	var p paths.Paths
	var seed []market.KnownMarket
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		srv := filepath.Join(tmp, "srv")
		Expect(os.MkdirAll(srv, 0o755)).To(Succeed())
		base := testenv.ServeGit(srv)
		work := filepath.Join(tmp, "work")
		Expect(os.MkdirAll(work, 0o755)).To(Succeed())
		gitIn(work, "init", "-q", "-b", "main")
		writeTree(work, map[string]string{".claude-plugin/marketplace.json": `{"name": "acme", "owner": {"name": "Acme"},
  "plugins": [{"name": "formatter", "source": "./formatter", "description": "Formats code"},
    {"name": "npmx", "source": {"source": "npm", "package": "npmx"}}]}`, "formatter/commands/fmt.md": "Fmt\n"})
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "one")
		gitIn(tmp, "clone", "-q", "--bare", work, filepath.Join(srv, "acme.git"))
		url, pin = base+"/acme.git", gitIn(work, "rev-parse", "HEAD")
		seed = []market.KnownMarket{{ID: "acme", Source: url, Commit: pin, Curated: "2026-11-22", Reason: "Acme's reviewed plugins"}}
	})
	// run is lola with the seed above and, when pr is not nil, pr answering its questions.
	run := func(pr cli.Prompter, args ...string) result {
		GinkgoHelper()
		opts := []cli.Option{cli.WithArgs(args), cli.WithKnownSeed(func() ([]market.KnownMarket, error) { return seed, nil })}
		if pr != nil {
			opts = append(opts, cli.WithPrompter(pr))
		}
		root, err := cli.NewRootCmd(os.Getenv, opts...)
		Expect(err).NotTo(HaveOccurred())
		var out, errb bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&errb)
		root.SetIn(strings.NewReader(""))
		root.SetArgs(args)
		code := cli.Execute(root)
		return result{out: out.String(), err: errb.String(), code: code}
	}
	doc := func(s string) map[string]any {
		GinkgoHelper()
		var v map[string]any
		Expect(json.Unmarshal([]byte(s), &v)).To(Succeed(), s)
		checkJSON(v, "")
		return v
	}

	Describe("known", func() {
		It("says so when this lola ships none", func() {
			seed = nil
			r := run(nil, "market", "known")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(Equal("no known marketplaces ship with this lola; lola market add <source> adds one of your own\n"))
			r = run(nil, "market", "known", "--json")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(MatchJSON(`{"known": [], "hints": []}`))
			Expect(run(nil, "market", "known", "--plain").out).To(BeEmpty())
		})

		It("lists them with their pin, and the one that shows each", func() {
			r := run(nil, "market", "known")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(MatchRegexp(`ID +ENABLED +CURATED +PIN +SOURCE +REASON\nacme +no +2026-11-22 +` + pin[:7] + ` +` + regexpQuote(url) + ` +Acme's reviewed plugins\n`))
			Expect(r.out).To(HaveSuffix("→ lola market known acme\n"))
			r = run(nil, "market", "known", "--plain")
			Expect(r.out).To(Equal("known\tacme\tfalse\t2026-11-22\t" + pin + "\t" + url + "\t\tAcme's reviewed plugins\nhint\tlola market known acme --plain\n"))
			d := doc(run(nil, "market", "known", "--json").out)
			Expect(d["known"]).To(Equal([]any{map[string]any{"id": "acme", "source": url, "path": "", "commit": pin, "curated": "2026-11-22",
				"reason": "Acme's reviewed plugins", "enabled": false}}))
			Expect(d["hints"]).To(Equal([]any{"lola market known acme --json"}))
		})

		It("shows one, hinting the enable that registers it", func() {
			r := run(nil, "market", "known", "acme")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(MatchRegexp(`Known marketplace acme\n +source +` + regexpQuote(url) + `\n +pin +` + pin + `\n +curated +2026-11-22\n +reason +Acme's reviewed plugins\n`))
			Expect(r.out).To(HaveSuffix("→ lola market enable acme\n"))
			d := doc(run(nil, "market", "known", "acme", "--json").out)
			Expect(d["known"]).To(HaveLen(1))
			Expect(d["hints"]).To(Equal([]any{"lola market enable acme --json"}))
		})

		It("lets no control character of the seed reach the JSON", func() {
			seed[0].Reason = "fine \x1b[31mred"
			r := run(nil, "market", "known", "--json")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).NotTo(ContainSubstring("\\u001b"))
			Expect(doc(r.out)["known"].([]any)[0].(map[string]any)["reason"]).NotTo(ContainSubstring("\x1b"))
		})

		It("refuses an id it does not ship, naming the ones it does", func() {
			r := run(nil, "market", "known", "nope")
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(Equal("error: nope is not a known marketplace; the known ones are acme\n  hint: lola market known\n"))
			expectRunnable(lastHint(r.err))
		})

		It("shows one id at most, each hint a run that works", func() {
			r := run(nil, "market", "known", "acme", "other")
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(Equal("error: known shows one marketplace; give it one id\n  hint: lola market known acme\n  hint: lola market known other\n"))
			for line := range strings.SplitSeq(r.err, "\n") {
				if h, ok := strings.CutPrefix(line, "  hint: "); ok {
					expectRunnable(h)
				}
			}
		})

		It("takes no flag of another verb, and the old --known names the verb", func() {
			r := run(nil, "market", "known", "-n")
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(HavePrefix("error: --dry-run applies to lola market add, enable, update and rm\n"))
			r = run(nil, "market", "--known", "acme")
			Expect(r.err).To(Equal("error: --known is not a flag of lola market\n  hint: lola market known acme\n"))
			expectRunnable(lastHint(r.err))
		})
	})

	Describe("enable", func() {
		registered := func() string {
			b, err := os.ReadFile(p.Markets)
			if os.IsNotExist(err) {
				return ""
			}
			Expect(err).NotTo(HaveOccurred())
			return string(b)
		}

		It("registers the pinned commit under the id with -y, and --known then lists it enabled", func() {
			r := run(nil, "market", "enable", "acme", "-y")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(MatchRegexp(`✓ marketplace acme: 1 entry \(.*\) from ` + regexpQuote(url) + ` \(ref ` + pin + `\) at ` + pin[:7] + "\n"))
			Expect(r.out).To(ContainSubstring("→ pinned to " + pin[:7] + ", curated 2026-11-22; lola market update acme stays on it\n"))
			Expect(registered()).To(And(ContainSubstring("name: acme"), ContainSubstring(pin)))
			Expect(run(nil, "market", "known", "--plain").out).To(HavePrefix("known\tacme\ttrue\t"))
			Expect(run(nil, "market", "known", "acme").out).To(HaveSuffix("→ lola market acme\n"))
		})

		It("registers under the name given", func() {
			Expect(run(nil, "market", "enable", "acme", "mine", "-y").code).To(Equal(0))
			Expect(registered()).To(ContainSubstring("name: mine"))
		})

		It("refuses without -y off a terminal, hinting the run with -y, and writes nothing", func() {
			r := run(nil, "market", "enable", "acme")
			Expect(r.code).To(Equal(3))
			Expect(r.err).To(HavePrefix("Known marketplace acme\n"))
			Expect(r.err).To(ContainSubstring("pin      " + pin))
			Expect(r.err).To(HaveSuffix("error: lola market enable needs confirmation, and there is no terminal to ask on\n  hint: lola market enable acme -y\n"))
			expectRunnable(lastHint(r.err))
			Expect(registered()).To(BeEmpty())
			Expect(p.MarketCache).NotTo(BeADirectory())
		})

		It("refuses under --json with the plan it would have asked about, the one -n --json prints", func() {
			r := run(nil, "market", "enable", "acme", "--json")
			Expect(r.code).To(Equal(3))
			refusal := doc(r.err)
			Expect(refusal["hints"]).To(Equal([]any{"lola market enable acme --json -y"}))
			plan := doc(run(nil, "market", "enable", "acme", "--json", "-n").out)
			Expect(plan["dry_run"]).To(BeTrue())
			Expect(plan["known"]).To(HaveKeyWithValue("commit", pin))
			for k, v := range plan {
				Expect(refusal).To(HaveKeyWithValue(k, v), k)
			}
			Expect(registered()).To(BeEmpty())
		})

		It("asks on a terminal with the pin and the catalog it holds, then registers on yes", func() {
			c := &confirming{answers: []bool{true}, stderr: &bytes.Buffer{}}
			r := run(c, "market", "enable", "acme")
			Expect(r.code).To(Equal(0), r.err)
			Expect(c.questions).To(Equal([]string{"Enable known marketplace acme?"}))
			Expect(c.shows[0]).To(And(ContainSubstring("source   "+url), ContainSubstring("pin      "+pin), ContainSubstring("curated  2026-11-22"),
				ContainSubstring("reason   Acme's reviewed plugins"), ContainSubstring("would add marketplace acme: 1 entry"),
				ContainSubstring("1 entry skipped: 1 npm source")))
			Expect(registered()).To(ContainSubstring("name: acme"))
		})

		It("writes nothing when the answer is no", func() {
			c := &confirming{answers: []bool{false}, stderr: &bytes.Buffer{}}
			r := run(c, "market", "enable", "acme")
			Expect(r.code).To(Equal(4))
			Expect(r.err).To(Equal("✗ cancelled — nothing was changed\n"))
			Expect(registered()).To(BeEmpty())
			Expect(p.MarketCache).NotTo(BeADirectory())
		})

		It("previews under -n without asking, writing nothing", func() {
			c := &confirming{stderr: &bytes.Buffer{}}
			r := run(c, "market", "enable", "acme", "-n")
			Expect(r.code).To(Equal(0), r.err)
			Expect(c.questions).To(BeEmpty())
			Expect(r.out).To(ContainSubstring("would add marketplace acme"))
			Expect(registered()).To(BeEmpty())
			Expect(p.MarketCache).NotTo(BeADirectory())
		})

		It("refuses an id it does not ship, a name another marketplace holds, and more names than one", func() {
			r := run(nil, "market", "enable", "nope", "-y")
			Expect(r.err).To(Equal("error: nope is not a known marketplace; the known ones are acme\n  hint: lola market known\n"))
			expectRunnable(lastHint(r.err))
			r = run(nil, "market", "enable", "acme", "a", "b", "-y")
			Expect(r.err).To(Equal("error: enable registers one marketplace; give it one name\n  hint: lola market enable acme a --yes\n  hint: lola market enable acme b --yes\n"))

			Expect(run(nil, "market", "enable", "acme", "taken", "-y").code).To(Equal(0))
			seed = append(seed, market.KnownMarket{ID: "other", Source: url, Commit: strings.Repeat("a", 40), Curated: "2026-11-22", Reason: "r"})
			r = run(nil, "market", "enable", "other", "taken", "-y")
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(ContainSubstring("taken is already a marketplace, from " + url))
			Expect(r.err).To(ContainSubstring("hint: lola market enable other taken-2\n"))

			seed = nil
			r = run(nil, "market", "enable", "nope", "x")
			Expect(r.err).To(Equal("error: nope is not a known marketplace; this lola ships none\n  hint: lola market known\n"))
		})

		It("is the same as market add --enable, which takes no source, --ref, --path or --ssh-host", func() {
			a := run(nil, "market", "enable", "acme", "-n", "--json")
			Expect(a.code).To(Equal(0), a.err)
			b := run(nil, "market", "add", "--enable", "acme", "-n", "--json")
			// The two runs fetch at different seconds.
			fetchedAt := regexp.MustCompile(`"fetched_at": "[^"]*"`)
			Expect(fetchedAt.ReplaceAllString(b.out, "")).To(Equal(fetchedAt.ReplaceAllString(a.out, "")))
			Expect(b.code).To(Equal(a.code))
			Expect(b.err).To(Equal(a.err))
			Expect(run(nil, "market", "add", "--enable", "acme", "mine", "-n", "--json").code).To(Equal(0))
			for _, tc := range []struct {
				args  []string
				hints string
			}{
				{[]string{url}, "  hint: lola market enable acme\n  hint: lola market add " + url + "\n"},
				{[]string{"--ref", "main"}, "  hint: lola market enable acme\n"},
				{[]string{"--path", "x"}, "  hint: lola market enable acme\n"},
				{[]string{"--ssh-host", "git.example.com"}, "  hint: lola market enable acme --ssh-host git.example.com\n"},
				{[]string{"mine", "other"}, "  hint: lola market enable acme mine\n"},
			} {
				r := run(nil, append([]string{"market", "add", "--enable", "acme"}, tc.args...)...)
				Expect(r.code).To(Equal(1), strings.Join(tc.args, " "))
				Expect(r.err).To(Equal("error: add --enable registers a known marketplace: it takes an id and a name, no source, --ref, --path or --ssh-host\n"+tc.hints),
					strings.Join(tc.args, " "))
			}
			r := run(nil, "market", "enable", "acme", "--ref", "main")
			Expect(r.err).To(HavePrefix("error: --ref applies to lola market add\n"))
		})

		It("refuses a name that is a market verb or alias, coaching a free one", func() {
			for _, w := range []string{"known", "check", "enable", "get", "upgrade", "list", "ls", "set", "search"} {
				r := run(nil, "market", "enable", "acme", w, "-y")
				Expect(r.code).To(Equal(1), w)
				what := " is a lola market command"
				if w == "set" || w == "search" {
					what = " is a word lola market answers to"
				}
				Expect(r.err).To(Equal("error: "+w+what+", so no marketplace can be named "+w+"\n  hint: name it otherwise: lola market enable acme "+w+"-1 --yes\n"), w)
			}
			Expect(registered()).To(BeEmpty())
		})

		It("coaches the Python lola's set <name> --enable and set <name> --disable", func() {
			r := run(nil, "market", "set", "acme", "--enable")
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(Equal("error: lola has no disabled state, and you have no marketplace acme\n"))
			Expect(run(nil, "market", "enable", "-y", "acme").code).To(Equal(0))
			r = run(nil, "market", "set", "acme", "--enable")
			Expect(r.err).To(Equal("error: acme is already active; lola has no disabled state\n  hint: lola market acme\n"))
			r = run(nil, "market", "set", "acme", "--disable")
			Expect(r.err).To(Equal("error: set is not a lola market verb: lola market rm <name> forgets a marketplace; lola has no disabled state\n  hint: lola market rm acme\n"))
		})
	})
})
