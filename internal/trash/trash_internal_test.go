package trash

import (
	"errors"
	"io/fs"
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
	move := func(orig string) (string, error) {
		parent, err := os.OpenRoot(filepath.Dir(orig))
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(parent.Close()).To(Succeed()) }()
		return t.Move("d", proj, parent, filepath.Base(orig))
	}
	crossDevice := func(oldpath, newpath string) error {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
	}

	It("records the move in manifest.txt, synced, before it moves anything", func() {
		var seen string
		t.rename = func(oldpath, newpath string) error {
			b, err := os.ReadFile(filepath.Join(t.Dir, "manifest.txt"))
			Expect(err).NotTo(HaveOccurred())
			seen = string(b)
			return os.Rename(oldpath, newpath)
		}
		to, err := move(filepath.Join(proj, "skill"))
		Expect(err).NotTo(HaveOccurred())
		Expect(seen).To(Equal(filepath.Join(proj, "skill") + "\t" + to + "\n"))
	})

	It("moves across file systems by copying, syncing, then removing the original", func() {
		t.rename = func(oldpath, newpath string) error {
			return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
		}
		to, err := move(filepath.Join(proj, "skill"))
		Expect(err).NotTo(HaveOccurred())
		Expect(os.ReadFile(filepath.Join(to, "ref", "a.md"))).To(Equal([]byte("a")))
		Expect(filepath.Join(proj, "skill")).NotTo(BeAnExistingFile())
	})

	It("fails, keeping the original, on any other rename error", func() {
		t.rename = func(oldpath, newpath string) error {
			return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EACCES}
		}
		_, err := move(filepath.Join(proj, "skill"))
		Expect(err).To(HaveOccurred())
		Expect(filepath.Join(proj, "skill", "SKILL.md")).To(BeAnExistingFile())
	})

	It("moves a whole tree across file systems, links and empty directories included", func() {
		Expect(os.Symlink("SKILL.md", filepath.Join(proj, "skill", "link.md"))).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(proj, "skill", "empty", "deeper"), 0o755)).To(Succeed())
		t.rename = func(oldpath, newpath string) error {
			return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
		}
		to, err := move(filepath.Join(proj, "skill"))
		Expect(err).NotTo(HaveOccurred())
		Expect(os.Readlink(filepath.Join(to, "link.md"))).To(Equal("SKILL.md"))
		Expect(filepath.Join(to, "empty", "deeper")).To(BeADirectory())
		Expect(os.ReadFile(filepath.Join(to, "SKILL.md"))).To(Equal([]byte("s")))
		Expect(filepath.Join(proj, "skill")).NotTo(BeAnExistingFile())
	})

	It("a move across file systems copies, syncs, then removes only what matches the copy", func() {
		Expect(os.Symlink("SKILL.md", filepath.Join(proj, "skill", "link.md"))).To(Succeed())
		Expect(os.WriteFile(filepath.Join(proj, "skill", "ref", "b.md"), []byte("b"), 0o644)).To(Succeed())
		t.rename = crossDevice
		t.copyTree = func(parent *os.Root, name, to string) error {
			Expect(copyFrom(parent, name, to)).To(Succeed())
			By("a file added and a file edited after the copy walk")
			Expect(os.WriteFile(filepath.Join(proj, "skill", "ref", "late.md"), []byte("late"), 0o644)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(proj, "skill", "ref", "b.md"), []byte("edited"), 0o644)).To(Succeed())
			return nil
		}
		to, err := move(filepath.Join(proj, "skill"))
		var changed *ChangedError
		Expect(errors.As(err, &changed)).To(BeTrue(), "%v", err)
		Expect(changed.Left).To(Equal([]string{filepath.Join(proj, "skill", "ref", "b.md"), filepath.Join(proj, "skill", "ref", "late.md")}))
		Expect(err).To(MatchError(ContainSubstring("changed while lola moved it")))
		Expect(to).To(Equal(filepath.Join(t.Dir, "d", "skill")))

		By("the copy is whole and what matched it is gone")
		Expect(os.ReadFile(filepath.Join(to, "ref", "b.md"))).To(Equal([]byte("b")))
		Expect(os.Readlink(filepath.Join(to, "link.md"))).To(Equal("SKILL.md"))
		Expect(filepath.Join(to, "ref", "late.md")).NotTo(BeAnExistingFile())
		for _, gone := range []string{"SKILL.md", "link.md", "ref/a.md"} {
			_, err := os.Lstat(filepath.Join(proj, "skill", gone))
			Expect(err).To(MatchError(fs.ErrNotExist), gone)
		}
		Expect(os.ReadFile(filepath.Join(proj, "skill", "ref", "b.md"))).To(Equal([]byte("edited")))
		Expect(os.ReadFile(filepath.Join(proj, "skill", "ref", "late.md"))).To(Equal([]byte("late")))
	})

	It("a directory refilled during the move is reported in Left", func() {
		t.rename = crossDevice
		t.walk = func(parent *os.Root, name string) ([]string, map[string]bool, error) {
			walked, dirs, err := walkEntry(parent, name)
			Expect(os.WriteFile(filepath.Join(proj, "skill", "ref", "late.md"), []byte("late"), 0o644)).To(Succeed())
			return walked, dirs, err
		}
		_, err := move(filepath.Join(proj, "skill"))
		var changed *ChangedError
		Expect(errors.As(err, &changed)).To(BeTrue(), "%v", err)
		Expect(changed.Left).To(Equal([]string{filepath.Join(proj, "skill", "ref")}))
		Expect(filepath.Join(proj, "skill", "ref", "late.md")).To(BeAnExistingFile())
	})

	It("a directory swapped for a link during the move is reported in Left, and the link stays", func() {
		Expect(os.MkdirAll(filepath.Join(proj, "skill", "empty"), 0o755)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(proj, "spare"), 0o755)).To(Succeed())
		t.rename = crossDevice
		t.walk = func(parent *os.Root, name string) ([]string, map[string]bool, error) {
			walked, dirs, err := walkEntry(parent, name)
			Expect(os.Remove(filepath.Join(proj, "skill", "empty"))).To(Succeed())
			Expect(os.Symlink(filepath.Join("..", "spare"), filepath.Join(proj, "skill", "empty"))).To(Succeed())
			return walked, dirs, err
		}
		_, err := move(filepath.Join(proj, "skill"))
		var changed *ChangedError
		Expect(errors.As(err, &changed)).To(BeTrue(), "%v", err)
		Expect(changed.Left).To(Equal([]string{filepath.Join(proj, "skill", "empty")}))
		fi, err := os.Lstat(filepath.Join(proj, "skill", "empty"))
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode() & fs.ModeSymlink).NotTo(BeZero())
		Expect(filepath.Join(proj, "spare")).To(BeADirectory())
	})

	It("a failed cross-device removal names the copy and carries no control byte", func() {
		sub := filepath.Join(proj, "skill", "sub")
		Expect(os.MkdirAll(sub, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(sub, "\x1b[31mRED"), []byte("x"), 0o644)).To(Succeed())
		Expect(os.Chmod(sub, 0o555)).To(Succeed())
		DeferCleanup(os.Chmod, sub, os.FileMode(0o755))
		t.rename = crossDevice
		to, err := move(filepath.Join(proj, "skill"))
		Expect(err).To(MatchError(ContainSubstring("cannot remove " + filepath.Join(proj, "skill") + " after copying it into the trash")))
		Expect(err).To(MatchError(ContainSubstring("permission denied")))
		Expect(err.Error()).NotTo(ContainSubstring("\x1b"))
		Expect(to).NotTo(BeEmpty())
		Expect(filepath.Join(to, "sub", "\x1b[31mRED")).To(BeAnExistingFile(), "the copy is whole")
	})

	It("removes a moved directory whole across file systems when nothing changed", func() {
		Expect(os.MkdirAll(filepath.Join(proj, "skill", "empty"), 0o755)).To(Succeed())
		t.rename = crossDevice
		_, err := move(filepath.Join(proj, "skill"))
		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.Join(proj, "skill")).NotTo(BeAnExistingFile())
	})

	It("keeps a file across file systems that changed after the copy", func() {
		t.rename = crossDevice
		t.copyTree = func(parent *os.Root, name, to string) error {
			Expect(copyFrom(parent, name, to)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(proj, "skill", "SKILL.md"), []byte("edited"), 0o644)).To(Succeed())
			return nil
		}
		parent, err := os.OpenRoot(filepath.Join(proj, "skill"))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(parent.Close)
		_, err = t.Move("d", proj, parent, "SKILL.md")
		var changed *ChangedError
		Expect(errors.As(err, &changed)).To(BeTrue(), "%v", err)
		Expect(changed.Left).To(Equal([]string{filepath.Join(proj, "skill", "SKILL.md")}))
		Expect(os.ReadFile(filepath.Join(proj, "skill", "SKILL.md"))).To(Equal([]byte("edited")))
	})

	It("reads a move across file systems through the parent, never following a link out of it", func() {
		outside := filepath.Join(filepath.Dir(proj), "outside")
		Expect(os.MkdirAll(outside, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(outside, "secret"), []byte("s"), 0o644)).To(Succeed())
		Expect(os.Symlink(outside, filepath.Join(proj, "skill", "out"))).To(Succeed())
		t.rename = crossDevice
		to, err := move(filepath.Join(proj, "skill"))
		Expect(err).NotTo(HaveOccurred())
		Expect(os.Readlink(filepath.Join(to, "out"))).To(Equal(outside), "the link is copied as a link")
		Expect(filepath.Join(outside, "secret")).To(BeAnExistingFile())
	})

	It("refuses an entry name that is not one path component", func() {
		parent, err := os.OpenRoot(proj)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(parent.Close)
		for _, name := range []string{"skill/ref", "..", ".", ""} {
			_, err := t.Move("d", proj, parent, name)
			Expect(err).To(MatchError(ContainSubstring("not one path component")), name)
		}
		Expect(filepath.Join(proj, "skill", "ref", "a.md")).To(BeAnExistingFile())
	})

	It("prints a path from a failed rename only escaped", func() {
		esc := filepath.Join(proj, "a\x1bb")
		Expect(os.WriteFile(esc, []byte("x"), 0o644)).To(Succeed())
		t.rename = func(oldpath, newpath string) error {
			return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EACCES}
		}
		_, err := move(esc)
		Expect(err).To(MatchError(ContainSubstring("permission denied")))
		Expect(err.Error()).NotTo(ContainSubstring("\x1b"))
	})
})
