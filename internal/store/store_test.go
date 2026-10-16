package store_test

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("Store", func() {
	var (
		p   paths.Paths
		s   *store.Store
		now = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	)

	// stage builds a module content dir under paths.Staging, as a fetcher would.
	stage := func(files map[string]string) string {
		Expect(os.MkdirAll(p.Staging, 0o700)).To(Succeed())
		dir, err := os.MkdirTemp(p.Staging, "fetch-*")
		Expect(err).NotTo(HaveOccurred())
		for rel, c := range files {
			Expect(os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(dir, rel), []byte(c), 0o644)).To(Succeed())
		}
		return dir
	}
	put := func(files map[string]string, ref string) (store.Version, error) {
		return s.Put(store.PutRequest{Staged: stage(files), FallbackName: "example", Source: "/src/example", Type: "folder", Ref: ref, Now: now})
	}

	BeforeEach(func() {
		testenv.Isolate()
		var err error
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		s = store.New(p)
	})

	It("stores a version, moves the staged dir, and records origin and head", func() {
		staged := stage(map[string]string{"commands/a.md": "A"})
		v, err := s.Put(store.PutRequest{Staged: staged, FallbackName: "example", Source: "/src/example", Type: "folder", Now: now})
		Expect(err).NotTo(HaveOccurred())
		Expect(v.Name).To(Equal("example"))
		Expect(v.ID).To(HaveLen(12))
		Expect(v.Meta.Digest).To(HavePrefix("sha256:" + v.ID))
		Expect(v.Meta.FetchedAt).To(Equal("2026-10-08T12:00:00Z"))
		Expect(v.Module.Commands).To(HaveLen(1))
		Expect(filepath.Join(v.ContentDir, "commands/a.md")).To(BeARegularFile())
		Expect(staged).NotTo(BeADirectory())
		o, found, err := s.Origin("example")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(o).To(Equal(store.Origin{Source: "/src/example", Type: "folder", Format: "native", Heads: []store.Head{{Ref: "", Digest: v.ID}}}))
		fi, err := os.Stat(filepath.Join(p.Store, "example", "origin.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o600)))
	})

	It("keeps one head per ref so projects can pin different refs", func() {
		v1, err := put(map[string]string{"commands/a.md": "v1"}, "v1")
		Expect(err).NotTo(HaveOccurred())
		v2, err := put(map[string]string{"commands/a.md": "v2"}, "v2")
		Expect(err).NotTo(HaveOccurred())
		h1, ok, err := s.Head("example", "v1")
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(h1.ID).To(Equal(v1.ID))
		h2, _, _ := s.Head("example", "v2")
		Expect(h2.ID).To(Equal(v2.ID))
		_, ok, err = s.Head("example", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeFalse())
	})

	It("moves a head on new content and keeps the old version", func() {
		v1, _ := put(map[string]string{"commands/a.md": "old"}, "")
		v2, err := put(map[string]string{"commands/a.md": "new"}, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(v2.ID).NotTo(Equal(v1.ID))
		h, _, _ := s.Head("example", "")
		Expect(h.ID).To(Equal(v2.ID))
		Expect(v1.ContentDir).To(BeADirectory())
	})

	It("reuses the version directory for identical content", func() {
		v1, _ := put(map[string]string{"commands/a.md": "same"}, "")
		v2, err := put(map[string]string{"commands/a.md": "same"}, "main")
		Expect(err).NotTo(HaveOccurred())
		Expect(v2.ID).To(Equal(v1.ID))
		entries, _ := os.ReadDir(filepath.Join(p.Store, "example", "versions"))
		Expect(entries).To(HaveLen(1))
		staging, _ := os.ReadDir(p.Staging)
		Expect(staging).To(BeEmpty())
	})

	It("refuses a same-named module from a different origin", func() {
		_, err := put(map[string]string{"commands/a.md": "x"}, "")
		Expect(err).NotTo(HaveOccurred())
		_, err = s.Put(store.PutRequest{Staged: stage(map[string]string{"commands/a.md": "y"}), FallbackName: "example", Source: "/elsewhere/example", Type: "folder", Now: now})
		Expect(err).To(MatchError(ContainSubstring("different module named \"example\"")))
		Expect(errs.Hints(err)).To(ContainElement("remove the cached one first: lola cache rm example"))
	})

	It("stores nothing when the staged dir is not a module", func() {
		_, err := put(map[string]string{"README.md": "x"}, "")
		Expect(err).To(MatchError(ContainSubstring("no module found")))
		names, err := s.Names()
		Expect(err).NotTo(HaveOccurred())
		Expect(names).To(BeEmpty())
	})

	It("serializes concurrent puts of the same content", func() {
		var wg sync.WaitGroup
		errsCh := make(chan error, 4)
		dirs := make([]string, 4)
		for i := range dirs {
			dirs[i] = stage(map[string]string{"commands/a.md": "same"})
		}
		for _, d := range dirs {
			wg.Add(1)
			go func(d string) {
				defer wg.Done()
				defer GinkgoRecover()
				_, err := s.Put(store.PutRequest{Staged: d, FallbackName: "example", Source: "/src/example", Type: "folder", Now: now})
				errsCh <- err
			}(d)
		}
		wg.Wait()
		close(errsCh)
		for err := range errsCh {
			Expect(err).NotTo(HaveOccurred())
		}
		entries, _ := os.ReadDir(filepath.Join(p.Store, "example", "versions"))
		Expect(entries).To(HaveLen(1))
	})

	It("lists names sorted and removes every version", func() {
		_, _ = put(map[string]string{"commands/a.md": "x"}, "")
		_, err := s.Put(store.PutRequest{Staged: stage(map[string]string{"lola.yml": "name: alpha\n", "commands/a.md": "x"}), Source: "/src/alpha", Type: "folder", Now: now})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Names()).To(Equal([]string{"alpha", "example"}))
		Expect(s.Remove("example")).To(BeTrue())
		Expect(s.Remove("example")).To(BeFalse())
		Expect(s.Names()).To(Equal([]string{"alpha"}))
	})
})

var _ = Describe("Digest", func() {
	var dir string
	BeforeEach(func() {
		dir = testenv.Isolate()
		Expect(os.WriteFile(filepath.Join(dir, "a"), []byte("x"), 0o644)).To(Succeed())
	})

	It("is stable and sensitive to content, path and executability", func() {
		d1, err := store.Digest(dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(d1).To(MatchRegexp(`^sha256:[0-9a-f]{64}$`))
		Expect(store.Digest(dir)).To(Equal(d1))
		Expect(os.Chmod(filepath.Join(dir, "a"), 0o600)).To(Succeed())
		Expect(store.Digest(dir)).To(Equal(d1), "owner-only mode changes are normalized away")
		Expect(os.Chmod(filepath.Join(dir, "a"), 0o755)).To(Succeed())
		Expect(store.Digest(dir)).NotTo(Equal(d1))
		Expect(os.Chmod(filepath.Join(dir, "a"), 0o644)).To(Succeed())
		Expect(os.Rename(filepath.Join(dir, "a"), filepath.Join(dir, "b"))).To(Succeed())
		Expect(store.Digest(dir)).NotTo(Equal(d1))
	})

	It("refuses symlinks", func() {
		Expect(os.Symlink("a", filepath.Join(dir, "l"))).To(Succeed())
		_, err := store.Digest(dir)
		Expect(err).To(MatchError(ContainSubstring("symbolic link")))
	})
})
