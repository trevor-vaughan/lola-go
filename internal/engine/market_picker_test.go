package engine_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/market"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("Engine.PickerRows", func() {
	const (
		pinA     = "1111111111111111111111111111111111111111"
		pinB     = "2222222222222222222222222222222222222222"
		alphaURL = "https://example.com/alpha.git"
		betaURL  = "https://example.com/beta.git"
		gammaURL = "https://example.com/gamma.git"
	)
	type reg struct{ name, source, ref, path string }
	var (
		p paths.Paths
		e *engine.Engine
	)
	BeforeEach(func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p, targets.Builtin())
		e.Getwd = func() (string, error) { return tmp, nil }
		e.KnownSeed = func() ([]market.KnownMarket, error) { return []market.KnownMarket{}, nil }
	})
	// register writes markets.yaml with rs, in order; nothing is fetched.
	register := func(rs ...reg) {
		GinkgoHelper()
		var b strings.Builder
		b.WriteString("version: 1\nmarkets:\n")
		for _, r := range rs {
			b.WriteString("  - name: " + r.name + "\n    source: " + r.source + "\n")
			if r.ref != "" {
				b.WriteString("    ref: " + r.ref + "\n")
			}
			if r.path != "" {
				b.WriteString("    path: " + r.path + "\n")
			}
		}
		Expect(os.MkdirAll(filepath.Dir(p.Markets), 0o755)).To(Succeed())
		Expect(os.WriteFile(p.Markets, []byte(b.String()), 0o600)).To(Succeed())
	}
	seedOf := func(ks ...market.KnownMarket) {
		e.KnownSeed = func() ([]market.KnownMarket, error) { return ks, nil }
	}
	known := func(id, src string) market.KnownMarket {
		return market.KnownMarket{ID: id, Source: src, Commit: pinA, Curated: "2026-11-22", Reason: "a fixture"}
	}

	It("returns empty non-nil slices with no markets and an empty seed", func() {
		own, unenabled, notices, err := e.PickerRows(nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(own).NotTo(BeNil())
		Expect(own).To(BeEmpty())
		Expect(unenabled).NotTo(BeNil())
		Expect(unenabled).To(BeEmpty())
		Expect(notices).NotTo(BeNil())
	})

	It("lists your marketplaces in markets.yaml order, none known", func() {
		register(reg{name: "zed", source: alphaURL}, reg{name: "alpha", source: betaURL})
		own, unenabled, _, err := e.PickerRows(nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(own).To(HaveExactElements(
			And(HaveField("ID", "zed"), HaveField("Name", "zed"), HaveField("Known", false), HaveField("Enabled", false), HaveField("Reason", "")),
			And(HaveField("ID", "alpha"), HaveField("Known", false))))
		Expect(unenabled).To(BeEmpty())
	})

	It("folds an enabled known marketplace into own and leaves the others, in seed order", func() {
		register(reg{name: "beta", source: betaURL, ref: pinA})
		seedOf(known("alpha", alphaURL), known("beta", betaURL), known("gamma", gammaURL))
		own, unenabled, _, err := e.PickerRows(nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(own).To(HaveExactElements(And(
			HaveField("ID", "beta"), HaveField("Known", true), HaveField("Enabled", true),
			HaveField("Reason", "a fixture"), HaveField("Pin", pinA), HaveField("Source", betaURL))))
		Expect(unenabled).To(HaveExactElements(
			And(HaveField("ID", "alpha"), HaveField("Known", true), HaveField("Enabled", false), HaveField("Pin", pinA), HaveField("Reason", "a fixture"), HaveField("Name", ""), HaveField("Source", alphaURL)),
			And(HaveField("ID", "gamma"), HaveField("Known", true), HaveField("Enabled", false))))
	})

	It("does not count a marketplace of the same name from another source as the known one", func() {
		register(reg{name: "alpha", source: betaURL})
		seedOf(known("alpha", alphaURL))
		own, unenabled, _, err := e.PickerRows(nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(own).To(ConsistOf(And(HaveField("ID", "alpha"), HaveField("Known", false), HaveField("Enabled", false), HaveField("Reason", ""), HaveField("Pin", ""))))
		Expect(unenabled).To(ConsistOf(And(HaveField("ID", "alpha"), HaveField("Known", true), HaveField("Enabled", false))))
	})

	It("is not known when its source is an archive or a folder", func() {
		register(reg{name: "alpha", source: "https://example.com/alpha.tar.gz"}, reg{name: "beta", source: "/srv/beta"})
		seedOf(known("alpha", alphaURL), known("beta", betaURL))
		own, unenabled, _, err := e.PickerRows(nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(own).To(HaveEach(And(HaveField("Known", false), HaveField("Enabled", false))))
		Expect(unenabled).To(HaveLen(2))
	})

	DescribeTable("WebURL",
		func(r reg, want string) {
			r.name = "alpha"
			register(r)
			seedOf(known("beta", "git@github.com:acme/beta.git"), known("gamma", "https://exa_mple.com/acme/gamma.git"))
			own, unenabled, _, err := e.PickerRows(nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(own).To(ConsistOf(HaveField("WebURL", want)))
			Expect(unenabled).To(HaveExactElements(HaveField("WebURL", "https://github.com/acme/beta"), HaveField("WebURL", "")))
		},
		Entry("an https GitHub source", reg{source: "https://github.com/acme/alpha.git"}, "https://github.com/acme/alpha"),
		Entry("an ssh scp form of a known host", reg{source: "git@github.com:acme/alpha.git"}, "https://github.com/acme/alpha"),
		Entry("its ref and path", reg{source: "https://github.com/acme/alpha.git", ref: "v1", path: "cat/market"}, "https://github.com/acme/alpha/tree/v1/cat/market"),
		Entry("its path at the default ref", reg{source: "https://github.com/acme/alpha.git", path: "cat"}, "https://github.com/acme/alpha/tree/HEAD/cat"),
		Entry("a local folder", reg{source: "/srv/alpha"}, ""),
		Entry("an archive", reg{source: "https://example.com/alpha.tar.gz"}, ""),
		Entry("a catalog file", reg{source: "https://example.com/acme/alpha/marketplace.json"}, ""),
		Entry("a host a web address may not name", reg{source: "https://exa_mple.com/acme/alpha.git"}, ""),
		Entry("an ssh remote of an unknown host", reg{source: "git@internal.example.com:acme/alpha.git"}, ""),
	)

	DescribeTable("AheadOfPin",
		func(ref string, ahead bool) {
			register(reg{name: "beta", source: betaURL, ref: ref})
			seedOf(known("beta", betaURL))
			own, _, _, err := e.PickerRows(nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(own).To(ConsistOf(And(HaveField("Enabled", true), HaveField("AheadOfPin", ahead))))
		},
		Entry("a commit other than the pin", pinB, true),
		Entry("a commit other than the pin, written in upper case", "ABCDEF"+strings.Repeat("0", 34), true),
		Entry("the pin itself", pinA, false),
		Entry("a branch name", "main", false),
		Entry("no ref", "", false),
		Entry("a short hex that is not a full commit", "2222222", false),
	)

	It("strips control characters from a known entry's id, source, reason and pin", func() {
		k := known("al\x1b[31mpha", "https://example.com/\x1b[2Jalpha.git")
		k.Reason, k.Commit = "bad\x1b[2Jreason", "ab\x07cd"
		seedOf(k)
		_, unenabled, _, err := e.PickerRows(nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(unenabled).To(HaveLen(1))
		for _, s := range []string{unenabled[0].ID, unenabled[0].Source, unenabled[0].Reason, unenabled[0].Pin} {
			Expect(s).NotTo(MatchRegexp(`[\x00-\x1f\x7f]`))
		}
	})
})

var _ = Describe("Classify", func() {
	DescribeTable("raises the banner when most checks fail, and at least two",
		func(failed, checked int, want bool) {
			checks := make([]engine.Check, checked)
			for i := range checks {
				checks[i] = engine.Check{Index: i, Status: engine.StatusCurrent}
			}
			for i := range failed {
				checks[i].Status = engine.StatusUnreachable
			}
			Expect(engine.Classify(checks)).To(Equal(want))
		},
		Entry("0 of 0", 0, 0, false),
		Entry("1 of 1", 1, 1, false),
		Entry("1 of 5", 1, 5, false),
		Entry("2 of 5", 2, 5, false),
		Entry("3 of 5", 3, 5, true),
		Entry("2 of 2", 2, 2, true),
		Entry("2 of 3", 2, 3, true),
		Entry("1 of 2", 1, 2, false),
	)
})

var _ = Describe("Engine.CheckMarkets", func() {
	const (
		pinSeed = "1111111111111111111111111111111111111111"
	)
	var (
		tmp, work, bare string
		repoURL, first  string
		e               *engine.Engine
	)
	commit := func(name string) string {
		GinkgoHelper()
		Expect(os.WriteFile(filepath.Join(work, name), []byte(name), 0o600)).To(Succeed())
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", name)
		gitIn(work, "push", "-q", bare, "HEAD:refs/heads/main")
		return gitIn(work, "rev-parse", "HEAD")
	}
	// collect drains the channel, by row index; it fails if the channel does not close in time.
	collect := func(ch <-chan engine.Check) map[int]engine.Check {
		GinkgoHelper()
		got := map[int]engine.Check{}
		timeout := time.After(30 * time.Second)
		for {
			select {
			case c, ok := <-ch:
				if !ok {
					return got
				}
				got[c.Index] = c
			case <-timeout:
				Fail("the channel did not close")
			}
		}
	}
	// own is a fetched marketplace of yours at cached.
	own := func(cached string) engine.PickerRow {
		return engine.PickerRow{MarketRow: engine.MarketRow{Name: "m", Source: repoURL, Commit: cached}, ID: "m"}
	}
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		work, bare = filepath.Join(tmp, "work"), filepath.Join(tmp, "srv", "repo.git")
		Expect(os.MkdirAll(work, 0o755)).To(Succeed())
		Expect(os.MkdirAll(filepath.Dir(bare), 0o755)).To(Succeed())
		gitIn(work, "init", "-q", "-b", "main")
		gitIn(tmp, "init", "-q", "--bare", "-b", "main", bare)
		first = commit("one")
		repoURL = testenv.ServeGit(filepath.Join(tmp, "srv")) + "/repo.git"
		e = engine.New(paths.Paths{}, targets.Builtin())
	})

	It("says current when the remote is at the cached commit", func() {
		got := collect(e.CheckMarkets(context.Background(), []engine.PickerRow{own(first)}))
		Expect(got).To(HaveLen(1))
		Expect(got[0]).To(Equal(engine.Check{Index: 0, Status: engine.StatusCurrent}))
	})

	It("says an update is available when the remote has moved past your own marketplace", func() {
		commit("two")
		got := collect(e.CheckMarkets(context.Background(), []engine.PickerRow{own(first)}))
		Expect(got[0]).To(Equal(engine.Check{Index: 0, Status: engine.StatusUpdate}))
	})

	It("says an update is available when the branch your own marketplace is registered on has moved", func() {
		commit("two")
		row := own(first)
		row.Ref = "main"
		Expect(collect(e.CheckMarkets(context.Background(), []engine.PickerRow{row}))[0].Status).To(Equal(engine.StatusUpdate))
	})

	It("says upstream moved when the remote has moved past the commit your own marketplace is registered at", func() {
		commit("two")
		row := own(first)
		row.Ref = first
		Expect(collect(e.CheckMarkets(context.Background(), []engine.PickerRow{row}))[0].Status).To(Equal(engine.StatusMoved))
	})

	It("says upstream moved for a marketplace registered at a commit written in upper case", func() {
		commit("two")
		row := own(first)
		row.Ref = strings.ToUpper(first)
		Expect(collect(e.CheckMarkets(context.Background(), []engine.PickerRow{row}))[0].Status).To(Equal(engine.StatusMoved))
	})

	It("says current for a marketplace registered at the commit the remote is at", func() {
		row := own(first)
		row.Ref = first
		Expect(collect(e.CheckMarkets(context.Background(), []engine.PickerRow{row}))[0].Status).To(Equal(engine.StatusCurrent))
	})

	It("says upstream moved for a known marketplace you moved off its pin to a commit", func() {
		commit("two")
		row := own(first)
		row.Ref, row.Known, row.Enabled, row.AheadOfPin, row.Pin = first, true, true, true, pinSeed
		Expect(collect(e.CheckMarkets(context.Background(), []engine.PickerRow{row}))[0].Status).To(Equal(engine.StatusMoved))
	})

	It("says upstream moved for a known marketplace still on its pin", func() {
		commit("two")
		row := own(first)
		row.Ref, row.Known, row.Enabled, row.Pin = first, true, true, first
		Expect(collect(e.CheckMarkets(context.Background(), []engine.PickerRow{row}))[0].Status).To(Equal(engine.StatusMoved))
	})

	It("says an update is available for a known marketplace you registered on a branch", func() {
		commit("two")
		row := own(first)
		row.Ref, row.Known, row.Enabled, row.Pin = "main", true, true, first
		Expect(collect(e.CheckMarkets(context.Background(), []engine.PickerRow{row}))[0].Status).To(Equal(engine.StatusUpdate))
	})

	It("compares the seed pin to the tip for a known marketplace you have not enabled", func() {
		unenabled := func(pin string) engine.PickerRow {
			return engine.PickerRow{MarketRow: engine.MarketRow{Source: repoURL}, ID: "k", Known: true, Pin: pin}
		}
		got := collect(e.CheckMarkets(context.Background(), []engine.PickerRow{unenabled(first), unenabled(pinSeed)}))
		Expect(got[0].Status).To(Equal(engine.StatusCurrent))
		Expect(got[1].Status).To(Equal(engine.StatusMoved))
	})

	It("says unreachable with the reason when the remote is gone", func() {
		Expect(os.RemoveAll(bare)).To(Succeed())
		got := collect(e.CheckMarkets(context.Background(), []engine.PickerRow{own(first)}))
		Expect(got[0].Status).To(Equal(engine.StatusUnreachable))
		Expect(got[0].Reason).NotTo(BeEmpty())
	})

	It("gives a reason without the file names of a path error", func() {
		e.RemoteTip = func(context.Context, source.Spec) (string, error) {
			return "", &fs.PathError{Op: "open", Path: "/secret/place", Err: fs.ErrNotExist}
		}
		got := collect(e.CheckMarkets(context.Background(), []engine.PickerRow{own(first)}))
		Expect(got[0]).To(Equal(engine.Check{Index: 0, Status: engine.StatusUnreachable, Reason: fs.ErrNotExist.Error()}))
	})

	It("emits no check for an archive, a folder, a catalog file, or a row with no fetched catalog", func() {
		rows := []engine.PickerRow{
			{MarketRow: engine.MarketRow{Name: "a", Source: "https://example.com/a.tar.gz", Commit: first}},
			{MarketRow: engine.MarketRow{Name: "f", Source: "/srv/folder", Commit: first}},
			{MarketRow: engine.MarketRow{Name: "c", Source: "https://example.com/marketplace.json", Commit: first}},
			{MarketRow: engine.MarketRow{Name: "n", Source: repoURL}},
			own(first),
		}
		got := collect(e.CheckMarkets(context.Background(), rows))
		Expect(got).To(HaveLen(1))
		Expect(got).To(HaveKey(4))
	})

	It("closes the channel at once for no rows", func() {
		Expect(collect(e.CheckMarkets(context.Background(), nil))).To(BeEmpty())
	})

	It("never runs more than 8 checks at once", func() {
		var running, peak atomic.Int32
		e.RemoteTip = func(ctx context.Context, _ source.Spec) (string, error) {
			n := running.Add(1)
			defer running.Add(-1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(50 * time.Millisecond)
			return first, nil
		}
		rows := make([]engine.PickerRow, 40)
		for i := range rows {
			rows[i] = own(first)
		}
		Expect(collect(e.CheckMarkets(context.Background(), rows))).To(HaveLen(40))
		Expect(peak.Load()).To(BeNumerically("<=", 8))
		Expect(peak.Load()).To(BeNumerically(">", 1))
	})

	It("ends a check that hangs when the context is cancelled, and closes the channel", func() {
		e.RemoteTip = func(ctx context.Context, _ source.Spec) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		}
		ctx, cancel := context.WithCancel(context.Background())
		ch := e.CheckMarkets(ctx, []engine.PickerRow{own(first), own(first)})
		time.AfterFunc(100*time.Millisecond, cancel)
		got := collect(ch)
		Expect(got).To(HaveLen(2))
		for _, c := range got {
			Expect(c.Status).To(Equal(engine.StatusUnreachable))
		}
	})

	It("gives up on a check that hangs after 10 seconds", func() {
		e.RemoteTip = func(ctx context.Context, _ source.Spec) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		}
		start := time.Now()
		got := collect(e.CheckMarkets(context.Background(), []engine.PickerRow{own(first)}))
		Expect(got[0].Status).To(Equal(engine.StatusUnreachable))
		Expect(time.Since(start)).To(BeNumerically("~", 10*time.Second, 2*time.Second))
	})
})
