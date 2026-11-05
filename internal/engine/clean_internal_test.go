package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"

	"github.com/gofrs/flock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("moveAside (R-9-42)", func() {
	It("holds a lock alone across its own rename, but on Windows, where an open file cannot be renamed", func() {
		tmp := testenv.Isolate()
		lock := filepath.Join(tmp, "run-1.lock")
		Expect(os.WriteFile(lock, nil, 0o600)).To(Succeed())
		held := false
		rename := func(oldpath, newpath string) error {
			other := flock.New(oldpath)
			ok, err := other.TryLock()
			Expect(err).NotTo(HaveOccurred())
			if ok {
				Expect(other.Unlock()).To(Succeed())
			}
			held = !ok
			return os.Rename(oldpath, newpath)
		}
		ok, err := moveAside(CleanItem{abs: lock, lock: lock}, filepath.Join(tmp, "aside"), rename)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(held).To(Equal(runtime.GOOS != "windows"), "no run can take the lock between its check and its rename")
		Expect(filepath.Join(tmp, "aside")).To(BeARegularFile())
	})
})

var _ = Describe("restore (A28)", func() {
	It("puts back the head of a version it cannot examine, which may still be cached", func() {
		if os.Geteuid() == 0 {
			Skip("root examines entries of a directory it may not search")
		}
		tmp := testenv.Isolate()
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e := New(p)
		src := filepath.Join(tmp, "kit")
		Expect(os.MkdirAll(filepath.Join(src, "commands"), 0o700)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(src, "lola.yml"), []byte("name: kit\n"), 0o600)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(src, "commands", "hi.md"), []byte("Hi\n"), 0o600)).To(Succeed())
		v, err := e.Fetch(context.Background(), source.Spec{Kind: source.KindFolder, Location: src})
		Expect(err).NotTo(HaveOccurred())
		before, _, err := e.Store.Origin("kit")
		Expect(err).NotTo(HaveOccurred())
		Expect(e.Store.Cleaned("kit", []string{v.ID})).To(Succeed())
		versions := filepath.Join(p.Store, "kit", "versions")
		Expect(os.Chmod(versions, 0o600)).To(Succeed())
		DeferCleanup(os.Chmod, versions, os.FileMode(0o700))
		failed := errors.New("the rename failed")
		Expect(e.restore([]string{"kit"}, map[string]store.Origin{"kit": before}, map[string][]string{"kit": {v.ID}}, nil, failed)).To(MatchError(failed))
		o, _, err := e.Store.Origin("kit")
		Expect(err).NotTo(HaveOccurred())
		Expect(o.Heads).To(Equal(before.Heads))
		Expect(o.LastUsed).To(Equal(before.LastUsed))
	})
})
