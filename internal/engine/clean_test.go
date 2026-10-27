package engine_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.yaml.in/yaml/v3"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// Plan 9 §6: lola cache clean.
var _ = Describe("lola cache clean (Plan 9 §6)", func() {
	var (
		e   *engine.Engine
		tmp string
		day time.Time
		ctx = context.Background()
	)
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		day = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
		e.Now = func() time.Time { return day }
	})
	// fetch caches kit with hi.md holding content, as lola cache add does, and returns the
	// version's id; the default head moves to it.
	fetch := func(name, content string) string {
		GinkgoHelper()
		dir := filepath.Join(tmp, name)
		writeTree(dir, map[string]string{"lola.yml": "name: " + name + "\n", "commands/hi.md": content})
		v, err := e.Fetch(ctx, sourceSpec(dir))
		Expect(err).NotTo(HaveOccurred())
		return v.ID
	}
	version := func(name, id string) string { return filepath.Join(e.Paths.Store, name, "versions", id) }
	rel := func(abs string) string {
		r, err := filepath.Rel(filepath.Dir(e.Paths.Store), abs) // $XDG_CACHE_HOME/lola
		Expect(err).NotTo(HaveOccurred())
		return filepath.ToSlash(r)
	}
	// scratch makes a run's scratch directory holding a 10-byte file, with the lock file beside it
	// when lock is set, as a killed run leaves it, and returns the directory.
	scratch := func(parent, name string, lock bool) string {
		GinkgoHelper()
		dir := filepath.Join(parent, name)
		writeTree(dir, map[string]string{"fetched/x": "0123456789"})
		if lock {
			Expect(os.WriteFile(dir+".lock", nil, 0o600)).To(Succeed())
		}
		return dir
	}
	// hold holds the lock at path on a descriptor of the spec's own, which flock(2) treats as
	// another holder, as a live run does.
	hold := func(path string) {
		GinkgoHelper()
		fl := flock.New(path)
		Expect(fl.Lock()).To(Succeed())
		DeferCleanup(fl.Unlock)
	}
	plan := func(req engine.CleanRequest) *engine.CleanResult {
		GinkgoHelper()
		res, err := e.PlanClean(req)
		Expect(err).NotTo(HaveOccurred())
		return res
	}
	apply := func(p *engine.CleanResult) *engine.CleanResult {
		GinkgoHelper()
		res, err := e.ApplyClean(p)
		Expect(err).NotTo(HaveOccurred())
		return res
	}
	removed := func(res *engine.CleanResult) map[string]string {
		out := map[string]string{}
		for _, it := range res.Removed {
			out[it.Path] = it.Reason
		}
		return out
	}
	heads := func(name string) []string {
		GinkgoHelper()
		o, found, err := e.Store.Origin(name)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		var out []string
		for _, h := range o.Heads {
			out = append(out, h.Digest)
		}
		return out
	}

	It("bare clean removes superseded versions and dead scratch, and keeps every head", func() {
		old := fetch("kit", "v1\n")
		head := fetch("kit", "v2\n")
		other := fetch("other", "o\n")
		run := scratch(e.Paths.Staging, "run-1", true)
		bare := scratch(e.Paths.Staging, "run-2", false)
		orphan := filepath.Join(e.Paths.Staging, "run-3.lock")
		Expect(os.WriteFile(orphan, nil, 0o600)).To(Succeed())
		check := scratch(e.Paths.Tmp, "check-4", true)
		p := plan(engine.CleanRequest{})
		Expect(p.DryRun).To(BeTrue())
		Expect(removed(p)).To(Equal(map[string]string{rel(version("kit", old)): "superseded", rel(run): "scratch", rel(bare): "scratch",
			rel(orphan): "scratch", rel(check): "scratch"}))
		Expect(p.Kept).To(BeEmpty())
		Expect(p.BytesFreed).To(BeNumerically(">=", 30), "three 10-byte scratch files and the version")
		Expect(version("kit", old)).To(BeADirectory(), "a plan writes nothing")

		res := apply(p)
		Expect(res.DryRun).To(BeFalse())
		Expect(res.Removed).To(Equal(p.Removed))
		Expect(res.BytesFreed).To(Equal(p.BytesFreed))
		Expect(version("kit", old)).NotTo(BeAnExistingFile())
		for _, gone := range []string{run, run + ".lock", bare, orphan, check, check + ".lock"} {
			Expect(gone).NotTo(BeAnExistingFile())
		}
		Expect(version("kit", head)).To(BeADirectory())
		Expect(version("other", other)).To(BeADirectory())
		Expect(heads("kit")).To(Equal([]string{head}))
		Expect(heads("other")).To(Equal([]string{other}))
		staged, err := os.ReadDir(e.Paths.Staging)
		Expect(err).NotTo(HaveOccurred())
		Expect(staged).To(BeEmpty(), "nothing set aside is left")
	})

	DescribeTable("clean keeps scratch a live run holds",
		func(parent func() string, name string, req engine.CleanRequest) {
			dir := scratch(parent(), name, true)
			hold(dir + ".lock")
			p := plan(req)
			Expect(p.Kept).To(Equal([]engine.CleanKept{{Reason: "in use", Path: rel(dir)}}))
			Expect(removed(p)).NotTo(HaveKey(rel(dir)))
			Expect(removed(p)).NotTo(HaveKey(rel(dir + ".lock")))
			res := apply(p)
			Expect(res.Kept).To(Equal(p.Kept))
			Expect(filepath.Join(dir, "fetched", "x")).To(BeARegularFile())
			Expect(dir + ".lock").To(BeARegularFile())
		},
		Entry(".staging/run-*", func() string { return e.Paths.Staging }, "run-1", engine.CleanRequest{}),
		Entry("tmp/check-*", func() string { return e.Paths.Tmp }, "check-1", engine.CleanRequest{}),
		Entry("--all", func() string { return e.Paths.Staging }, "run-1", engine.CleanRequest{All: true}),
	)

	DescribeTable("--unused-for removes versions unused that long, heads included, and a module whose last version goes",
		func(age string, days int, gone bool) {
			id := fetch("kit", "v1\n")
			keep := fetch("other", "o\n")
			day = day.AddDate(0, 0, days)
			Expect(e.Store.Used("other", keep)).To(Succeed())
			res := apply(plan(engine.CleanRequest{UnusedFor: age}))
			if !gone {
				Expect(res.Removed).To(BeEmpty())
				Expect(heads("kit")).To(Equal([]string{id}))
				return
			}
			Expect(removed(res)).To(Equal(map[string]string{rel(version("kit", id)): "unused"}))
			Expect(filepath.Join(e.Paths.Store, "kit")).NotTo(BeAnExistingFile(), "its last version went: the module goes whole")
			Expect(e.Store.Names()).To(Equal([]string{"other"}))
			Expect(version("other", keep)).To(BeADirectory(), "used today")
		},
		Entry("30d: unused 30 days", "30d", 30, true),
		Entry("30d: unused 29 days", "30d", 29, false),
		Entry("1w = 7 days: unused 7 days", "1w", 7, true),
		Entry("1w = 7 days: unused 6 days", "1w", 6, false),
	)

	It("--unused-for drops the head of a version it removes and keeps the module's others", func() {
		old := fetch("kit", "v1\n")
		day = day.AddDate(0, 0, 10)
		head := fetch("kit", "v2\n")
		index := filepath.Join(e.Paths.Store, "kit", "index.yml")
		var o store.Origin
		b, err := os.ReadFile(index)
		Expect(err).NotTo(HaveOccurred())
		Expect(yaml.Unmarshal(b, &o)).To(Succeed())
		o.Heads = append(o.Heads, store.Head{Ref: "v1", Digest: old})
		b, err = yaml.Marshal(o)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(index, b, 0o600)).To(Succeed())
		res := apply(plan(engine.CleanRequest{UnusedFor: "1w"}))
		Expect(removed(res)).To(Equal(map[string]string{rel(version("kit", old)): "unused"}))
		Expect(heads("kit")).To(Equal([]string{head}))
		o, _, err = e.Store.Origin("kit")
		Expect(err).NotTo(HaveOccurred())
		Expect(o.LastUsed).To(Equal(map[string]string{head: "2026-10-11"}), "the removed version's last_used goes with it")
	})

	It("a version without last_used counts as used the day clean first sees it; -n writes nothing", func() {
		id := fetch("kit", "v1\n")
		index := filepath.Join(e.Paths.Store, "kit", "index.yml")
		b, err := os.ReadFile(index)
		Expect(err).NotTo(HaveOccurred())
		before := strings.Split(string(b), "last_used:")[0]
		Expect(os.WriteFile(index, []byte(before), 0o600)).To(Succeed())
		day = day.AddDate(0, 0, 100)
		p := plan(engine.CleanRequest{UnusedFor: "1d"})
		Expect(p.Removed).To(BeEmpty(), "first seen today")
		Expect(os.ReadFile(index)).To(Equal([]byte(before)), "-n writes nothing")
		Expect(apply(p).Removed).To(BeEmpty())
		o, _, err := e.Store.Origin("kit")
		Expect(err).NotTo(HaveOccurred())
		Expect(o.LastUsed).To(Equal(map[string]string{id: "2027-01-09"}), "a real clean writes the day it first saw it")
		day = day.AddDate(0, 0, 1)
		Expect(removed(plan(engine.CleanRequest{UnusedFor: "1d"}))).To(Equal(map[string]string{rel(version("kit", id)): "unused"}))
	})

	It("cache_unused_for sets the bare default; --unused-for never turns it off", func() {
		id := fetch("kit", "v1\n")
		writeTree(filepath.Dir(e.Paths.Config), map[string]string{"config.yaml": "version: 1\ncache_unused_for: 1w\n"})
		day = day.AddDate(0, 0, 8)
		Expect(removed(plan(engine.CleanRequest{}))).To(Equal(map[string]string{rel(version("kit", id)): "unused"}))
		Expect(plan(engine.CleanRequest{UnusedFor: "never"}).Removed).To(BeEmpty())
		Expect(plan(engine.CleanRequest{UnusedFor: "9d"}).Removed).To(BeEmpty(), "the flag replaces it")
	})

	It("--all removes modules, catalogs and scratch, and keeps the lock file, probes and held scratch", func() {
		fetch("kit", "v1\n")
		writeTree(filepath.Join(e.Paths.MarketCache, "acme"), map[string]string{"catalog.json": "{}", "source.json": "{}"})
		dead := scratch(e.Paths.Staging, "run-1", true)
		live := scratch(e.Paths.Staging, "run-2", true)
		hold(live + ".lock")
		check := scratch(e.Paths.Tmp, "check-3", false)
		writeTree(filepath.Dir(e.Paths.Probes), map[string]string{"probes.json": "{}"})
		Expect(os.WriteFile(e.Paths.StoreLock, nil, 0o600)).To(Succeed())

		res := apply(plan(engine.CleanRequest{All: true}))
		Expect(removed(res)).To(Equal(map[string]string{"modules/kit": "all", "markets/acme": "all", rel(dead): "all", rel(check): "all"}))
		Expect(res.Removed[0].Module).To(Equal("kit"))
		Expect(res.Kept).To(Equal([]engine.CleanKept{{Reason: "in use", Path: rel(live)}}))
		for _, dir := range []string{e.Paths.Store, e.Paths.MarketCache, e.Paths.Tmp} {
			es, err := os.ReadDir(dir)
			Expect(err).NotTo(HaveOccurred())
			Expect(es).To(BeEmpty(), dir)
		}
		staged, err := os.ReadDir(e.Paths.Staging)
		Expect(err).NotTo(HaveOccurred())
		var names []string
		for _, d := range staged {
			names = append(names, d.Name())
		}
		Expect(names).To(ConsistOf("run-2", "run-2.lock"))
		Expect(e.Paths.StoreLock).To(BeARegularFile())
		Expect(e.Paths.Probes).To(BeARegularFile())
	})

	DescribeTable("removed carries the reason of each item",
		func(setup func() (engine.CleanRequest, string), reason string) {
			req, path := setup()
			res := apply(plan(req))
			Expect(removed(res)).To(HaveKeyWithValue(path, reason))
		},
		Entry("superseded", func() (engine.CleanRequest, string) {
			old := fetch("kit", "v1\n")
			fetch("kit", "v2\n")
			return engine.CleanRequest{}, rel(version("kit", old))
		}, "superseded"),
		Entry("unused", func() (engine.CleanRequest, string) {
			id := fetch("kit", "v1\n")
			day = day.AddDate(0, 0, 3)
			return engine.CleanRequest{UnusedFor: "3d"}, rel(version("kit", id))
		}, "unused"),
		Entry("scratch", func() (engine.CleanRequest, string) {
			return engine.CleanRequest{}, rel(scratch(e.Paths.Tmp, "check-1", true))
		}, "scratch"),
		Entry("all", func() (engine.CleanRequest, string) {
			fetch("kit", "v1\n")
			return engine.CleanRequest{All: true}, "modules/kit"
		}, "all"),
	)

	It("clean never follows a symbolic link out of the cache", func() {
		outside := filepath.Join(tmp, "outside")
		writeTree(outside, map[string]string{"keep.txt": "precious"})
		fetch("kit", "v1\n")
		links := []string{version("kit", "0123456789ab"), filepath.Join(e.Paths.Staging, "run-1"), filepath.Join(e.Paths.Store, "evil"),
			filepath.Join(e.Paths.Store, "kit", "versions", "notanid")}
		for _, l := range links {
			Expect(os.MkdirAll(filepath.Dir(l), 0o700)).To(Succeed())
			Expect(os.Symlink(outside, l)).To(Succeed())
		}
		res := apply(plan(engine.CleanRequest{}))
		Expect(removed(res)).To(Equal(map[string]string{rel(links[0]): "superseded", rel(links[1]): "scratch"}),
			"bare clean leaves what is not a module or a version id")
		for _, it := range res.Removed {
			Expect(it.Bytes).To(BeZero(), "a link holds no bytes of the cache's")
		}
		res = apply(plan(engine.CleanRequest{All: true}))
		Expect(removed(res)).To(HaveKeyWithValue("modules/evil", "all"))
		for _, l := range links {
			Expect(l).NotTo(BeAnExistingFile())
		}
		Expect(os.ReadFile(filepath.Join(outside, "keep.txt"))).To(Equal([]byte("precious")))
	})

	DescribeTable("clean renames each item aside before removing it, and skips and reports an item that changed after the plan",
		func(age string, change func(old string)) {
			old := fetch("kit", "v1\n")
			if age == "" {
				fetch("kit", "v2\n")
			}
			day = day.AddDate(0, 0, 10)
			p := plan(engine.CleanRequest{UnusedFor: age})
			Expect(removed(p)).To(HaveKey(rel(version("kit", old))))
			change(old)
			res := apply(p)
			Expect(res.Kept).To(Equal([]engine.CleanKept{{Module: "kit", Version: old, Reason: "changed", Path: rel(version("kit", old))}}))
			Expect(res.Removed).To(BeEmpty())
		},
		Entry("a head moved onto the version", "", func(string) { fetch("kit", "v1\n") }),
		Entry("last_used refreshed", "1w", func(old string) { Expect(e.Store.Used("kit", old)).To(Succeed()) }),
		Entry("the version gone", "", func(old string) { Expect(os.RemoveAll(version("kit", old))).To(Succeed()) }),
	)

	// writeHead adds a head for ref to name's index.yml pointing at the version id, as a fetch of
	// another ref of the same content would.
	writeHead := func(name, ref, id string) {
		GinkgoHelper()
		index := filepath.Join(e.Paths.Store, name, "index.yml")
		var o store.Origin
		b, err := os.ReadFile(index)
		Expect(err).NotTo(HaveOccurred())
		Expect(yaml.Unmarshal(b, &o)).To(Succeed())
		o.Heads = append(o.Heads, store.Head{Ref: ref, Digest: id})
		b, err = yaml.Marshal(o)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(index, b, 0o600)).To(Succeed())
	}
	// unwritable makes dir read-only for the spec, as a file system that refuses a rename out of
	// it does; root ignores the mode, so the spec cannot run as root.
	unwritable := func(dir string) {
		GinkgoHelper()
		if os.Geteuid() == 0 {
			Skip("root renames and deletes in a read-only directory")
		}
		Expect(os.Chmod(dir, 0o500)).To(Succeed())
		DeferCleanup(func() error {
			if err := os.Chmod(dir, 0o700); !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			return nil // a clean moved it aside
		})
	}

	It("a clean whose second rename fails leaves every head loadable (A28)", func() {
		old := fetch("a", "v1\n")
		day = day.AddDate(0, 0, 10)
		head := fetch("a", "v2\n")
		writeHead("a", "old", old)
		superseded := fetch("b", "b1\n")
		fetch("b", "b2\n")
		p := plan(engine.CleanRequest{UnusedFor: "1w"})
		Expect(removed(p)).To(Equal(map[string]string{rel(version("a", old)): "unused", rel(version("b", superseded)): "superseded"}))
		unwritable(filepath.Join(e.Paths.Store, "b", "versions"))
		_, err := e.ApplyClean(p)
		Expect(err).To(HaveOccurred(), "the second rename fails")
		Expect(version("a", old)).NotTo(BeAnExistingFile(), "the first went")
		_, found, err := e.Store.Head("a", "old")
		Expect(err).NotTo(HaveOccurred(), "no head names the removed version")
		Expect(found).To(BeFalse())
		v, found, err := e.Store.Head("a", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(v.ID).To(Equal(head))
		Expect(e.Store.Head("b", "")).Error().NotTo(HaveOccurred())
	})

	It("a module directory swapped for a link after the plan is kept as changed, and nothing outside is removed (A28)", func() {
		old := fetch("kit", "v1\n")
		fetch("kit", "v2\n")
		p := plan(engine.CleanRequest{})
		outside := filepath.Join(tmp, "outside-kit")
		Expect(os.Rename(filepath.Join(e.Paths.Store, "kit"), outside)).To(Succeed())
		Expect(os.Symlink(outside, filepath.Join(e.Paths.Store, "kit"))).To(Succeed())
		res := apply(p)
		Expect(res.Removed).To(BeEmpty())
		Expect(res.Kept).To(Equal([]engine.CleanKept{{Module: "kit", Version: old, Reason: "changed", Path: rel(version("kit", old))}}))
		Expect(filepath.Join(outside, "versions", old)).To(BeADirectory())
	})

	It("--all shows a cache entry name that is not plain text cleaned in path (A28)", func() {
		writeTree(filepath.Join(e.Paths.Store, "x\x1b[31m"), map[string]string{"f": "x"})
		res := plan(engine.CleanRequest{All: true})
		Expect(res.Removed).To(HaveLen(1))
		Expect(res.Removed[0].Path).To(Equal("modules/x?[31m"))
		Expect(res.Removed[0].Module).To(BeEmpty())
	})

	It("a module whose index.yml cannot be read is kept as unreadable, and the clean goes on (A28)", func() {
		old := fetch("kit", "v1\n")
		fetch("kit", "v2\n")
		writeTree(filepath.Join(e.Paths.Store, "bad"), map[string]string{"index.yml": "heads: [\n"})
		res := apply(plan(engine.CleanRequest{}))
		Expect(res.Kept).To(Equal([]engine.CleanKept{{Module: "bad", Reason: "unreadable", Path: "modules/bad"}}))
		Expect(removed(res)).To(Equal(map[string]string{rel(version("kit", old)): "superseded"}))
		Expect(filepath.Join(e.Paths.Store, "bad", "index.yml")).To(BeARegularFile())
	})

	It("a clean that cannot delete what it set aside says so, and the next clean removes it", func() {
		old := fetch("kit", "v1\n")
		fetch("kit", "v2\n")
		unwritable(filepath.Join(version("kit", old), "content", "commands"))
		p := plan(engine.CleanRequest{})
		res := apply(p)
		Expect(removed(res)).To(Equal(map[string]string{rel(version("kit", old)): "superseded"}), "what it removed from the cache")
		Expect(res.BytesFreed).To(Equal(p.BytesFreed-int64(len("v1\n"))), "bytes_freed counts only what was deleted: hi.md stays")
		Expect(res.Notices).To(HaveLen(1))
		Expect(res.Notices[0].Level).To(Equal(engine.LevelWarn))
		Expect(res.Notices[0].Text).To(MatchRegexp(`^could not delete what this clean set aside in \.staging/clean-[0-9]+: .*; lola cache clean retries it$`))
		Expect(version("kit", old)).NotTo(BeAnExistingFile(), "renamed aside first")
		aside, err := filepath.Glob(filepath.Join(e.Paths.Staging, "clean-*", "*", "content", "commands"))
		Expect(err).NotTo(HaveOccurred())
		Expect(aside).To(HaveLen(1))
		Expect(os.Chmod(aside[0], 0o700)).To(Succeed())
		res = apply(plan(engine.CleanRequest{}))
		Expect(removed(res)).To(HaveKeyWithValue(rel(filepath.Dir(filepath.Dir(filepath.Dir(aside[0])))), "scratch"))
	})

	It("a clean renames into .staging only while it is a real directory (A28)", func() {
		old := fetch("kit", "v1\n")
		fetch("kit", "v2\n")
		p := plan(engine.CleanRequest{})
		Expect(p.Removed).To(HaveLen(1), "versions only")
		outside := filepath.Join(tmp, "outside-staging")
		Expect(os.MkdirAll(outside, 0o700)).To(Succeed())
		Expect(os.RemoveAll(e.Paths.Staging)).To(Succeed())
		Expect(os.Symlink(outside, e.Paths.Staging)).To(Succeed())
		_, err := e.ApplyClean(p)
		Expect(err).To(MatchError(e.Paths.Staging + " is not a directory, or not one lola made: lola cache clean sets what it removes aside only in a real directory of your cache"))
		Expect(version("kit", old)).To(BeADirectory(), "nothing renamed out of the cache")
		o, _, err := e.Store.Origin("kit")
		Expect(err).NotTo(HaveOccurred())
		Expect(o.LastUsed).To(HaveKey(old), "index.yml untouched")
	})

	It("a rename that fails puts back the heads and last_used of the versions that did not go (A28)", func() {
		old := fetch("a", "v1\n")
		day = day.AddDate(0, 0, 10)
		fetch("a", "v2\n")
		writeHead("a", "old", old)
		p := plan(engine.CleanRequest{UnusedFor: "1w"})
		Expect(removed(p)).To(Equal(map[string]string{rel(version("a", old)): "unused"}))
		unwritable(filepath.Join(e.Paths.Store, "a", "versions"))
		_, err := e.ApplyClean(p)
		Expect(err).To(HaveOccurred())
		_, isClean := errors.AsType[*engine.CleanError](err)
		Expect(isClean).To(BeTrue(), "a CleanError, whose JSON says what was removed")
		v, found, err := e.Store.Head("a", "old")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue(), "the head of the version that stayed is back")
		Expect(v.ID).To(Equal(old))
		o, _, err := e.Store.Origin("a")
		Expect(err).NotTo(HaveOccurred())
		Expect(o.LastUsed).To(HaveKeyWithValue(old, "2026-10-01"))
	})

	It("a clean that cannot write a later module's index.yml puts back the heads of the modules before it (A28)", func() {
		old := fetch("a", "v1\n")
		day = day.AddDate(0, 0, 10)
		fetch("a", "v2\n")
		writeHead("a", "old", old)
		superseded := fetch("b", "b1\n")
		fetch("b", "b2\n")
		p := plan(engine.CleanRequest{UnusedFor: "1w"})
		Expect(removed(p)).To(Equal(map[string]string{rel(version("a", old)): "unused", rel(version("b", superseded)): "superseded"}))
		unwritable(filepath.Join(e.Paths.Store, "b"))
		_, err := e.ApplyClean(p)
		Expect(err).To(HaveOccurred(), "b's index.yml cannot be written")
		v, found, err := e.Store.Head("a", "old")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue(), "a's head is back, so the next bare clean does not remove its version as superseded")
		Expect(v.ID).To(Equal(old))
		Expect(version("a", old)).To(BeADirectory())
		Expect(version("b", superseded)).To(BeADirectory())
	})

	DescribeTable("a module clean cannot read again under the lock is kept as unreadable, and the clean goes on (A28)",
		func(breakModule func(dir string)) {
			old := fetch("a", "v1\n")
			fetch("a", "v2\n")
			superseded := fetch("b", "b1\n")
			fetch("b", "b2\n")
			p := plan(engine.CleanRequest{})
			Expect(removed(p)).To(Equal(map[string]string{rel(version("a", old)): "superseded", rel(version("b", superseded)): "superseded"}))
			breakModule(filepath.Join(e.Paths.Store, "b"))
			res := apply(p)
			Expect(removed(res)).To(Equal(map[string]string{rel(version("a", old)): "superseded"}))
			Expect(res.Kept).To(Equal([]engine.CleanKept{{Module: "b", Reason: "unreadable", Path: "modules/b"}}))
			Expect(version("a", old)).NotTo(BeAnExistingFile())
		},
		Entry("its index.yml", func(dir string) {
			Expect(os.WriteFile(filepath.Join(dir, "index.yml"), []byte("heads: [\n"), 0o600)).To(Succeed())
		}),
		Entry("a version it cannot examine", func(dir string) {
			if os.Geteuid() == 0 {
				Skip("root examines entries of a directory it may not search")
			}
			versions := filepath.Join(dir, "versions")
			Expect(os.Chmod(versions, 0o600)).To(Succeed())
			DeferCleanup(os.Chmod, versions, os.FileMode(0o700))
		}),
	)
})
