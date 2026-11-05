package engine

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// R-9-9: a live run's scratch is held by an exclusive lock on the file of its name plus .lock.
var _ = Describe("scratch locks (R-9-9)", func() {
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

	DescribeTable("sweepRuns removes a stale run directory only when its lock is free (A28)",
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
			e.Now = func() time.Time { return time.Now().Add(25 * time.Hour) }
			Expect(e.sweepRuns()).To(BeEmpty())
			if gone {
				Expect(dir).NotTo(BeAnExistingFile())
			} else {
				Expect(dir).To(BeADirectory(), "a live run waiting longer than a day keeps its scratch")
			}
		},
		Entry("a lock a live run holds: kept", true, false),
		Entry("a lock nobody holds: removed", false, true),
	)
})
