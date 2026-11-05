package migrate

import (
	"os"
	"path/filepath"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("Trash moves", func() {
	var t *Trash
	var proj string
	BeforeEach(func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj = filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(filepath.Join(proj, "skill", "ref"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(proj, "skill", "SKILL.md"), []byte("s"), 0o644)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(proj, "skill", "ref", "a.md"), []byte("a"), 0o644)).To(Succeed())
		t, err = OpenTrash(filepath.Join(tmp, "trash"), time.Now())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(t.Close)
	})

	It("records the move in manifest.txt, synced, before it moves anything", func() {
		var seen string
		t.rename = func(oldpath, newpath string) error {
			b, err := os.ReadFile(filepath.Join(t.Dir, "manifest.txt"))
			Expect(err).NotTo(HaveOccurred())
			seen = string(b)
			return os.Rename(oldpath, newpath)
		}
		to, err := t.Move("d", proj, filepath.Join(proj, "skill"))
		Expect(err).NotTo(HaveOccurred())
		Expect(seen).To(Equal(filepath.Join(proj, "skill") + "\t" + to + "\n"))
	})

	It("moves across file systems by copying, syncing, then removing the original", func() {
		t.rename = func(oldpath, newpath string) error {
			return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
		}
		to, err := t.Move("d", proj, filepath.Join(proj, "skill"))
		Expect(err).NotTo(HaveOccurred())
		Expect(os.ReadFile(filepath.Join(to, "ref", "a.md"))).To(Equal([]byte("a")))
		Expect(filepath.Join(proj, "skill")).NotTo(BeAnExistingFile())
	})

	It("fails, keeping the original, on any other rename error", func() {
		t.rename = func(oldpath, newpath string) error {
			return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EACCES}
		}
		_, err := t.Move("d", proj, filepath.Join(proj, "skill"))
		Expect(err).To(HaveOccurred())
		Expect(filepath.Join(proj, "skill", "SKILL.md")).To(BeAnExistingFile())
	})

	It("moves a whole tree across file systems, links and empty directories included", func() {
		Expect(os.Symlink("SKILL.md", filepath.Join(proj, "skill", "link.md"))).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(proj, "skill", "empty", "deeper"), 0o755)).To(Succeed())
		t.rename = func(oldpath, newpath string) error {
			return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
		}
		to, err := t.Move("d", proj, filepath.Join(proj, "skill"))
		Expect(err).NotTo(HaveOccurred())
		Expect(os.Readlink(filepath.Join(to, "link.md"))).To(Equal("SKILL.md"))
		Expect(filepath.Join(to, "empty", "deeper")).To(BeADirectory())
		Expect(os.ReadFile(filepath.Join(to, "SKILL.md"))).To(Equal([]byte("s")))
		Expect(filepath.Join(proj, "skill")).NotTo(BeAnExistingFile())
	})

	It("prints a path from a failed rename only escaped", func() {
		esc := filepath.Join(proj, "a\x1bb")
		Expect(os.WriteFile(esc, []byte("x"), 0o644)).To(Succeed())
		t.rename = func(oldpath, newpath string) error {
			return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EACCES}
		}
		_, err := t.Move("d", proj, esc)
		Expect(err).To(MatchError(ContainSubstring("permission denied")))
		Expect(err.Error()).NotTo(ContainSubstring("\x1b"))
	})
})
