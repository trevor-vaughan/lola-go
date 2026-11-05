package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// A live run's scratch is held by an exclusive lock on the file of its name plus .lock.
var _ = Describe("scratch locks", func() {
	var e *Engine
	var tmp string
	BeforeEach(func() {
		tmp = testenv.Isolate()
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = New(p)
	})
	// held reports whether another holder has lock: a lock on a new descriptor, which flock(2)
	// treats as another holder, cannot be taken.
	held := func(lock string) bool {
		GinkgoHelper()
		other := flock.New(lock)
		ok, err := other.TryLock()
		Expect(err).NotTo(HaveOccurred())
		if ok {
			Expect(other.Unlock()).To(Succeed())
		}
		return !ok
	}

	DescribeTable("a run holds the lock beside its scratch while it lives, and removes both when it ends",
		func(start func() (string, func() error), parent func() string, prefix string) {
			dir, done := start()
			Expect(filepath.Dir(dir)).To(Equal(parent()))
			Expect(filepath.Base(dir)).To(HavePrefix(prefix))
			Expect(dir).To(BeADirectory())
			Expect(held(dir+".lock")).To(BeTrue(), "the run holds it")
			Expect(done()).To(Succeed())
			Expect(dir).NotTo(BeAnExistingFile())
			Expect(dir + ".lock").NotTo(BeAnExistingFile())
		},
		Entry("a staging run: .staging/run-*", func() (string, func() error) {
			dir, done, err := e.runDir()
			Expect(err).NotTo(HaveOccurred())
			return dir, done
		}, func() string { return e.Paths.Staging }, "run-"),
		Entry("lola check of a source: tmp/check-*", func() (string, func() error) {
			src := filepath.Join(tmp, "kit")
			Expect(os.MkdirAll(filepath.Join(src, "commands"), 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(src, "lola.yml"), []byte("name: kit\n"), 0o644)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(src, "commands", "hi.md"), []byte("Hi\n"), 0o644)).To(Succeed())
			_, content, done, err := e.loadForCheck(context.Background(), tmp, src, nil)
			Expect(err).NotTo(HaveOccurred())
			es, err := os.ReadDir(e.Paths.Tmp)
			Expect(err).NotTo(HaveOccurred())
			for _, d := range es {
				if d.IsDir() {
					dir := filepath.Join(e.Paths.Tmp, d.Name())
					Expect(content).To(HavePrefix(dir + string(filepath.Separator)))
					return dir, done
				}
			}
			Fail("no scratch directory in " + e.Paths.Tmp)
			return "", nil
		}, func() string { return e.Paths.Tmp }, "check-"),
	)

	It("an install's staged run holds its lock until Close: the sweep of stale runs leaves it, and it blocks no other run", func() {
		run, err := e.StageInstall()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(run.Close)
		Expect(held(run.dir + ".lock")).To(BeTrue())
		other, err := e.StageInstall()
		Expect(err).NotTo(HaveOccurred(), "another lola's run opens beside it, never waiting")
		Expect(other.dir).NotTo(Equal(run.dir))
		Expect(other.Close()).To(Succeed())

		old := time.Now().Add(-2 * time.Minute)
		Expect(os.Chtimes(run.dir, old, old)).To(Succeed())
		Expect(e.sweepRuns()).To(BeEmpty())
		Expect(run.dir).To(BeADirectory(), "a picker left open for more than a minute keeps its run")

		Expect(run.Close()).To(Succeed())
		Expect(run.dir).NotTo(BeAnExistingFile())
		Expect(run.dir + ".lock").NotTo(BeAnExistingFile())
	})

	It("a lock held on a file removed from its path, as a clean removes a free one, is not held at that path", func() {
		lock := filepath.Join(tmp, "run-x.lock")
		Expect(os.WriteFile(lock, nil, 0o600)).To(Succeed())
		fl := flock.New(lock)
		Expect(fl.TryLock()).To(BeTrue())
		DeferCleanup(fl.Unlock)
		Expect(lockedAt(fl, lock)).To(BeTrue())
		Expect(os.Remove(lock)).To(Succeed())
		Expect(lockedAt(fl, lock)).To(BeFalse(), "gone")
		Expect(os.WriteFile(lock, nil, 0o600)).To(Succeed())
		Expect(lockedAt(fl, lock)).To(BeFalse(), "another file at the same path")
	})

	// deadRun makes the directory and lock a killed lola leaves, last modified age ago.
	deadRun := func(age time.Duration) string {
		dir := filepath.Join(e.Paths.Staging, runPrefix+"dead")
		Expect(os.MkdirAll(dir, 0o700)).To(Succeed())
		Expect(os.WriteFile(dir+lockSuffix, nil, 0o600)).To(Succeed())
		mtime := time.Now().Add(-age)
		Expect(os.Chtimes(dir, mtime, mtime)).To(Succeed())
		return dir
	}

	It("sweepRuns reaps a dead run older than a minute", func() {
		dir := deadRun(2 * time.Minute)
		Expect(e.sweepRuns()).To(BeEmpty())
		Expect(dir).NotTo(BeAnExistingFile())
	})

	It("sweepRuns keeps a dead run created seconds ago: its lock may not be opened yet", func() {
		dir := deadRun(5 * time.Second)
		Expect(e.sweepRuns()).To(BeEmpty())
		Expect(dir).To(BeADirectory())
	})

	It("sweepRuns keeps a run a live process holds, however old", func() {
		dir, done, err := scratch(e.Paths.Staging, runPrefix)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(done)
		old := time.Now().Add(-2 * time.Minute)
		Expect(os.Chtimes(dir, old, old)).To(Succeed())
		Expect(e.sweepRuns()).To(BeEmpty())
		Expect(dir).To(BeADirectory())
	})

	DescribeTable("sweepRuns removes a stale run directory only when its lock is free",
		func(held, gone bool) {
			dir, done, err := e.runDir()
			Expect(err).NotTo(HaveOccurred())
			if !held {
				Expect(done()).To(Succeed())
				Expect(os.MkdirAll(dir, 0o700)).To(Succeed())
				Expect(os.WriteFile(dir+".lock", nil, 0o600)).To(Succeed())
			} else {
				DeferCleanup(done)
			}
			e.Now = func() time.Time { return time.Now().Add(2 * time.Minute) }
			Expect(e.sweepRuns()).To(BeEmpty())
			if gone {
				Expect(dir).NotTo(BeAnExistingFile())
				Expect(dir+".lock").NotTo(BeAnExistingFile(), "its lock goes with it")
			} else {
				Expect(dir).To(BeADirectory(), "a live run waiting longer than a minute keeps its scratch")
				Expect(dir + ".lock").To(BeARegularFile())
			}
		},
		Entry("a lock a live run holds: kept", true, false),
		Entry("a lock nobody holds: removed", false, true),
	)

	// orphanLock makes a run-*.lock with no directory beside it, last modified age ago.
	orphanLock := func(age time.Duration) string {
		GinkgoHelper()
		Expect(os.MkdirAll(e.Paths.Staging, 0o700)).To(Succeed())
		lock := filepath.Join(e.Paths.Staging, runPrefix+"orphan"+lockSuffix)
		Expect(os.WriteFile(lock, nil, 0o600)).To(Succeed())
		mtime := time.Now().Add(-age)
		Expect(os.Chtimes(lock, mtime, mtime)).To(Succeed())
		return lock
	}

	It("sweepRuns removes a run's lock left without its directory once it is a minute old and free", func() {
		lock := orphanLock(2 * time.Minute)
		Expect(e.sweepRuns()).To(BeEmpty())
		Expect(lock).NotTo(BeAnExistingFile())
	})

	It("sweepRuns keeps a lock without its directory created seconds ago: its run may be about to make it", func() {
		lock := orphanLock(5 * time.Second)
		Expect(e.sweepRuns()).To(BeEmpty())
		Expect(lock).To(BeARegularFile())
	})

	It("sweepRuns keeps a lock without its directory that a live process holds, however old", func() {
		lock := orphanLock(2 * time.Minute)
		fl := flock.New(lock)
		Expect(fl.TryLock()).To(BeTrue())
		DeferCleanup(fl.Unlock)
		Expect(e.sweepRuns()).To(BeEmpty())
		Expect(lock).To(BeARegularFile())
		Expect(lockedAt(fl, lock)).To(BeTrue(), "the live holder still holds the file at its path")
	})
})

var _ = Describe("reviewOf a version an install's staged run holds", func() {
	var e *Engine
	BeforeEach(func() {
		testenv.Isolate()
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = New(p)
	})
	commit := strings.Repeat("a", 40)
	// staged prepares kit, a git module, in run as a fetch would, selecting comps.
	staged := func(run *StagedRun, comps *source.Components) store.Version {
		GinkgoHelper()
		dir := filepath.Join(run.dir, "fetch-kit")
		Expect(os.MkdirAll(filepath.Join(dir, "skills", "a"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dir, "skills", "a", "SKILL.md"), []byte("---\ndescription: A\n---\nA\n"), 0o644)).To(Succeed())
		st, err := e.Store.Prepare(store.PutRequest{Staged: dir, FallbackName: "kit", Name: "kit", Source: "https://example.invalid/kit.git",
			Type: string(source.KindGit), Commit: commit, Components: comps, Now: e.Now()})
		Expect(err).NotTo(HaveOccurred())
		v, err := run.add(e.Store, st, false)
		Expect(err).NotTo(HaveOccurred())
		return v
	}

	It("names a git version without a selection by its source at its commit, which lola info fetches, before the cache holds it", func() {
		run, err := e.StageInstall()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(run.Close)
		review, err := e.reviewOf(run, staged(run, nil))
		Expect(err).NotTo(HaveOccurred())
		Expect(review).To(Equal([]string{"https://example.invalid/kit.git", "--ref", commit}))
	})

	It("names no lola info by name until the run commits the version: the cache does not hold the name yet", func() {
		run, err := e.StageInstall()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(run.Close)
		v := staged(run, &source.Components{Skills: []string{"skills/a"}})
		review, err := e.reviewOf(run, v)
		Expect(err).NotTo(HaveOccurred())
		Expect(review).To(BeNil())
		committed, err := run.commit(e.Store, []plan.ModuleChange{{Name: "kit", Entry: &ledger.Entry{Digest: v.Meta.Digest}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(committed).To(ConsistOf(HaveField("ID", v.ID)))
		review, err = e.reviewOf(run, v)
		Expect(err).NotTo(HaveOccurred())
		Expect(review).To(Equal([]string{"kit"}))
	})
})
