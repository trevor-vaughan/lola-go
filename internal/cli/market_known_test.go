package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/cli"
	"github.com/trevor-vaughan/lola-go/internal/market"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("lola market --known and --enable", func() {
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
		opts := []cli.Option{cli.WithKnownSeed(func() ([]market.KnownMarket, error) { return seed, nil })}
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

	Describe("--known", func() {
		It("says so when this lola ships none", func() {
			seed = nil
			r := run(nil, "market", "--known")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(Equal("no known marketplaces ship with this lola; lola market --add <source> adds one of your own\n"))
			r = run(nil, "market", "--known", "--json")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(MatchJSON(`{"known": [], "hints": []}`))
			Expect(run(nil, "market", "--known", "--plain").out).To(BeEmpty())
		})

		It("lists them with their pin, and the one that shows each", func() {
			r := run(nil, "market", "--known")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(MatchRegexp(`ID +ENABLED +CURATED +PIN +SOURCE +REASON\nacme +no +2026-11-22 +` + pin[:7] + ` +` + regexpQuote(url) + ` +Acme's reviewed plugins\n`))
			Expect(r.out).To(HaveSuffix("→ lola market --known acme\n"))
			r = run(nil, "market", "--known", "--plain")
			Expect(r.out).To(Equal("known\tacme\tfalse\t2026-11-22\t" + pin + "\t" + url + "\t\tAcme's reviewed plugins\nhint\tlola market --known acme --plain\n"))
			d := doc(run(nil, "market", "--known", "--json").out)
			Expect(d["known"]).To(Equal([]any{map[string]any{"id": "acme", "source": url, "path": "", "commit": pin, "curated": "2026-11-22",
				"reason": "Acme's reviewed plugins", "enabled": false}}))
			Expect(d["hints"]).To(Equal([]any{"lola market --known acme --json"}))
		})

		It("shows one, hinting the --enable that registers it", func() {
			r := run(nil, "market", "--known", "acme")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(MatchRegexp(`Known marketplace acme\n +source +` + regexpQuote(url) + `\n +pin +` + pin + `\n +curated +2026-11-22\n +reason +Acme's reviewed plugins\n`))
			Expect(r.out).To(HaveSuffix("→ lola market --enable acme\n"))
			d := doc(run(nil, "market", "--known", "acme", "--json").out)
			Expect(d["known"]).To(HaveLen(1))
			Expect(d["hints"]).To(Equal([]any{"lola market --enable acme --json"}))
		})

		It("lets no control character of the seed reach the JSON", func() {
			seed[0].Reason = "fine \x1b[31mred"
			r := run(nil, "market", "--known", "--json")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).NotTo(ContainSubstring("\\u001b"))
			Expect(doc(r.out)["known"].([]any)[0].(map[string]any)["reason"]).NotTo(ContainSubstring("\x1b"))
		})

		It("refuses an id it does not ship, naming the ones it does", func() {
			r := run(nil, "market", "--known", "nope")
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(Equal("error: nope is not a known marketplace; the known ones are acme\n  hint: lola market --known\n"))
		})

		It("shows one id at most, each hint a run that works", func() {
			r := run(nil, "market", "--known", "acme", "other")
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(Equal("error: --known shows one marketplace; give it one id\n  hint: lola market acme --known\n  hint: lola market other --known\n"))
		})

		It("is never combined with another action, nor takes a flag only an action does", func() {
			for args, want := range map[string]string{
				"--known --enable acme": "error: --enable and --known cannot be combined; lola market takes one action per run\n" +
					"  hint: lola market --enable acme\n  hint: lola market --known\n",
				"--known --remove": "error: --remove and --known cannot be combined; lola market takes one action per run\n" +
					"  hint: lola market --remove\n  hint: lola market --known\n",
				"--known -n": "error: --dry-run applies to --add, --update, --remove and --enable only\n  hint: lola market --known\n",
			} {
				r := run(nil, append([]string{"market"}, strings.Fields(args)...)...)
				Expect(r.code).To(Equal(1), args)
				Expect(r.err).To(Equal(want), args)
			}
		})
	})

	Describe("--enable", func() {
		registered := func() string {
			b, err := os.ReadFile(p.Markets)
			if os.IsNotExist(err) {
				return ""
			}
			Expect(err).NotTo(HaveOccurred())
			return string(b)
		}

		It("registers the pinned commit under the id with -y, and --known then lists it enabled", func() {
			r := run(nil, "market", "--enable", "acme", "-y")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(MatchRegexp(`✓ marketplace acme: 1 entry \(.*\) from ` + regexpQuote(url) + ` \(ref ` + pin + `\) at ` + pin[:7] + "\n"))
			Expect(r.out).To(ContainSubstring("→ pinned to " + pin[:7] + ", curated 2026-11-22; lola market acme --update stays on it\n"))
			Expect(registered()).To(And(ContainSubstring("name: acme"), ContainSubstring(pin)))
			Expect(run(nil, "market", "--known", "--plain").out).To(HavePrefix("known\tacme\ttrue\t"))
			Expect(run(nil, "market", "--known", "acme").out).To(HaveSuffix("→ lola market acme\n"))
		})

		It("registers under the name given", func() {
			Expect(run(nil, "market", "mine", "--enable", "acme", "-y").code).To(Equal(0))
			Expect(registered()).To(ContainSubstring("name: mine"))
		})

		It("refuses without -y off a terminal, hinting the run with -y, and writes nothing", func() {
			r := run(nil, "market", "--enable", "acme")
			Expect(r.code).To(Equal(3))
			Expect(r.err).To(HavePrefix("Known marketplace acme\n"))
			Expect(r.err).To(ContainSubstring("pin      " + pin))
			Expect(r.err).To(HaveSuffix("error: lola market needs confirmation, and there is no terminal to ask on\n  hint: lola market --enable acme -y\n"))
			Expect(registered()).To(BeEmpty())
			Expect(p.MarketCache).NotTo(BeADirectory())
		})

		It("refuses under --json with the plan it would have asked about, the one -n --json prints", func() {
			r := run(nil, "market", "--enable", "acme", "--json")
			Expect(r.code).To(Equal(3))
			refusal := doc(r.err)
			Expect(refusal["hints"]).To(Equal([]any{"lola market --enable acme --json -y"}))
			plan := doc(run(nil, "market", "--enable", "acme", "--json", "-n").out)
			Expect(plan["dry_run"]).To(BeTrue())
			Expect(plan["known"]).To(HaveKeyWithValue("commit", pin))
			for k, v := range plan {
				Expect(refusal).To(HaveKeyWithValue(k, v), k)
			}
			Expect(registered()).To(BeEmpty())
		})

		It("asks on a terminal with the pin and the catalog it holds, then registers on yes", func() {
			c := &confirming{answers: []bool{true}, stderr: &bytes.Buffer{}}
			r := run(c, "market", "--enable", "acme")
			Expect(r.code).To(Equal(0), r.err)
			Expect(c.questions).To(Equal([]string{"Enable known marketplace acme?"}))
			Expect(c.shows[0]).To(And(ContainSubstring("source   "+url), ContainSubstring("pin      "+pin), ContainSubstring("curated  2026-11-22"),
				ContainSubstring("reason   Acme's reviewed plugins"), ContainSubstring("would add marketplace acme: 1 entry"),
				ContainSubstring("1 entry skipped: 1 npm source")))
			Expect(registered()).To(ContainSubstring("name: acme"))
		})

		It("writes nothing when the answer is no", func() {
			c := &confirming{answers: []bool{false}, stderr: &bytes.Buffer{}}
			r := run(c, "market", "--enable", "acme")
			Expect(r.code).To(Equal(4))
			Expect(r.err).To(Equal("✗ cancelled — nothing was changed\n"))
			Expect(registered()).To(BeEmpty())
			Expect(p.MarketCache).NotTo(BeADirectory())
		})

		It("previews under -n without asking, writing nothing", func() {
			c := &confirming{stderr: &bytes.Buffer{}}
			r := run(c, "market", "--enable", "acme", "-n")
			Expect(r.code).To(Equal(0), r.err)
			Expect(c.questions).To(BeEmpty())
			Expect(r.out).To(ContainSubstring("would add marketplace acme"))
			Expect(registered()).To(BeEmpty())
			Expect(p.MarketCache).NotTo(BeADirectory())
		})

		It("refuses an id it does not ship, a name another marketplace holds, and more names than one", func() {
			r := run(nil, "market", "--enable", "nope", "-y")
			Expect(r.err).To(Equal("error: nope is not a known marketplace; the known ones are acme\n  hint: lola market --known\n"))
			r = run(nil, "market", "a", "b", "--enable", "acme", "-y")
			Expect(r.err).To(Equal("error: --enable registers one marketplace; give it one name\n  hint: lola market a --enable acme --yes\n  hint: lola market b --enable acme --yes\n"))

			Expect(run(nil, "market", "taken", "--enable", "acme", "-y").code).To(Equal(0))
			seed = append(seed, market.KnownMarket{ID: "other", Source: url, Commit: strings.Repeat("a", 40), Curated: "2026-11-22", Reason: "r"})
			r = run(nil, "market", "taken", "--enable", "other", "-y")
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(ContainSubstring("taken is already a marketplace, from " + url))
			Expect(r.err).To(ContainSubstring("hint: lola market taken-2 --enable other\n"))

			seed = nil
			r = run(nil, "market", "x", "--enable", "nope")
			Expect(r.err).To(Equal("error: nope is not a known marketplace; this lola ships none\n  hint: lola market --known\n"))
		})

		It("is never combined with --add, nor takes --ref or --path", func() {
			r := run(nil, "market", "--enable", "acme", "--add", url)
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(HavePrefix("error: --add and --enable cannot be combined; lola market takes one action per run\n"))
			r = run(nil, "market", "--enable", "acme", "--ref", "main")
			Expect(r.err).To(Equal("error: --ref applies to --add only\n  hint: lola market --enable acme\n"))
		})

		It("answers the Python lola's set <name> --enable, flag last, as set --enable <name>", func() {
			for _, args := range [][]string{{"market", "set", "acme", "--enable"}, {"market", "set", "acme", "--enable", "-y"}} {
				r := run(nil, args...)
				Expect(r.code).To(Equal(1), strings.Join(args, " "))
				Expect(r.err).To(Equal("error: lola has no disabled state, and you have no marketplace acme\n"), strings.Join(args, " "))
			}
			Expect(run(nil, "market", "acme", "--enable").err).To(ContainSubstring("flag needs an argument: --enable"))
		})

		It("still answers the Python lola's set --enable with the lola command", func() {
			r := run(nil, "market", "set", "--enable", "acme")
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(HavePrefix("error: lola has no disabled state, and you have no marketplace acme"))
		})
	})
})
