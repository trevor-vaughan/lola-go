package trash

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = ginkgo.Describe("Trash moves", func() {
	var t *Trash
	var proj string
	ginkgo.BeforeEach(func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj = filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(filepath.Join(proj, "skill", "ref"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(proj, "skill", "SKILL.md"), []byte("s"), 0o644)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(proj, "skill", "ref", "a.md"), []byte("a"), 0o644)).To(Succeed())
		t, err = OpenTrash(filepath.Join(tmp, "trash"), time.Now(), RunRecord{})
		Expect(err).NotTo(HaveOccurred())
		ginkgo.DeferCleanup(t.Close)
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

	ginkgo.It("records the move in manifest.txt, synced, before it moves anything", func() {
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

	ginkgo.It("moves across file systems by copying, syncing, then removing the original", func() {
		t.rename = func(oldpath, newpath string) error {
			return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
		}
		to, err := move(filepath.Join(proj, "skill"))
		Expect(err).NotTo(HaveOccurred())
		Expect(os.ReadFile(filepath.Join(to, "ref", "a.md"))).To(Equal([]byte("a")))
		Expect(filepath.Join(proj, "skill")).NotTo(BeAnExistingFile())
	})

	ginkgo.It("fails, keeping the original, on any other rename error", func() {
		t.rename = func(oldpath, newpath string) error {
			return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EACCES}
		}
		_, err := move(filepath.Join(proj, "skill"))
		Expect(err).To(HaveOccurred())
		Expect(filepath.Join(proj, "skill", "SKILL.md")).To(BeAnExistingFile())
	})

	ginkgo.It("moves a whole tree across file systems, links and empty directories included", func() {
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

	ginkgo.It("a move across file systems copies, syncs, then removes only what matches the copy", func() {
		Expect(os.Symlink("SKILL.md", filepath.Join(proj, "skill", "link.md"))).To(Succeed())
		Expect(os.WriteFile(filepath.Join(proj, "skill", "ref", "b.md"), []byte("b"), 0o644)).To(Succeed())
		t.rename = crossDevice
		t.copyTree = func(parent *os.Root, name, to string) error {
			Expect(copyFrom(parent, name, to)).To(Succeed())
			ginkgo.By("a file added and a file edited after the copy walk")
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

		ginkgo.By("the copy is whole and what matched it is gone")
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

	ginkgo.It("a directory refilled during the move is reported in Left", func() {
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

	ginkgo.It("a directory swapped for a link during the move is reported in Left, and the link stays", func() {
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

	ginkgo.It("a failed cross-device removal names the copy and carries no control byte", func() {
		sub := filepath.Join(proj, "skill", "sub")
		Expect(os.MkdirAll(sub, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(sub, "\x1b[31mRED"), []byte("x"), 0o644)).To(Succeed())
		Expect(os.Chmod(sub, 0o555)).To(Succeed())
		ginkgo.DeferCleanup(os.Chmod, sub, os.FileMode(0o755))
		t.rename = crossDevice
		to, err := move(filepath.Join(proj, "skill"))
		Expect(err).To(MatchError(ContainSubstring("cannot remove " + filepath.Join(proj, "skill") + " after copying it into the trash")))
		Expect(err).To(MatchError(ContainSubstring("permission denied")))
		Expect(err.Error()).NotTo(ContainSubstring("\x1b"))
		Expect(to).NotTo(BeEmpty())
		Expect(filepath.Join(to, "sub", "\x1b[31mRED")).To(BeAnExistingFile(), "the copy is whole")
	})

	ginkgo.It("removes a moved directory whole across file systems when nothing changed", func() {
		Expect(os.MkdirAll(filepath.Join(proj, "skill", "empty"), 0o755)).To(Succeed())
		t.rename = crossDevice
		_, err := move(filepath.Join(proj, "skill"))
		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.Join(proj, "skill")).NotTo(BeAnExistingFile())
	})

	ginkgo.It("keeps a file across file systems that changed after the copy", func() {
		t.rename = crossDevice
		t.copyTree = func(parent *os.Root, name, to string) error {
			Expect(copyFrom(parent, name, to)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(proj, "skill", "SKILL.md"), []byte("edited"), 0o644)).To(Succeed())
			return nil
		}
		parent, err := os.OpenRoot(filepath.Join(proj, "skill"))
		Expect(err).NotTo(HaveOccurred())
		ginkgo.DeferCleanup(parent.Close)
		_, err = t.Move("d", proj, parent, "SKILL.md")
		var changed *ChangedError
		Expect(errors.As(err, &changed)).To(BeTrue(), "%v", err)
		Expect(changed.Left).To(Equal([]string{filepath.Join(proj, "skill", "SKILL.md")}))
		Expect(os.ReadFile(filepath.Join(proj, "skill", "SKILL.md"))).To(Equal([]byte("edited")))
	})

	ginkgo.It("reads a move across file systems through the parent, never following a link out of it", func() {
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

	ginkgo.It("refuses an entry name that is not one path component", func() {
		parent, err := os.OpenRoot(proj)
		Expect(err).NotTo(HaveOccurred())
		ginkgo.DeferCleanup(parent.Close)
		for _, name := range []string{"skill/ref", "..", ".", ""} {
			_, err := t.Move("d", proj, parent, name)
			Expect(err).To(MatchError(ContainSubstring("not one path component")), name)
		}
		Expect(filepath.Join(proj, "skill", "ref", "a.md")).To(BeAnExistingFile())
	})

	ginkgo.It("prints a path from a failed rename only escaped", func() {
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

var _ = ginkgo.Describe("restoring a trash entry across file systems", func() {
	var proj, dir string
	var run Run
	ginkgo.BeforeEach(func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj = filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(filepath.Join(proj, "skill", "ref"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(proj, "skill", "SKILL.md"), []byte("s"), 0o644)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(proj, "skill", "ref", "a.md"), []byte("a"), 0o644)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(proj, "ship.md"), []byte("ship"), 0o644)).To(Succeed())
		t, err := OpenTrash(filepath.Join(tmp, "trash"), time.Now(), RunRecord{})
		Expect(err).NotTo(HaveOccurred())
		root, err := os.OpenRoot(proj)
		Expect(err).NotTo(HaveOccurred())
		for _, n := range []string{"skill", "ship.md"} {
			_, err = t.Move("d", proj, root, n)
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(root.Close()).To(Succeed())
		Expect(t.Close()).To(Succeed())
		dir = t.Dir
		run, err = ReadRun(dir)
		Expect(err).NotTo(HaveOccurred())
	})
	crossDevice := func(oldpath, newpath string) error {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
	}

	ginkgo.It("copies a file back, then removes it from the run", func() {
		Expect(restore(dir, run.Entries[1], nil, crossDevice)).To(Succeed())
		Expect(os.ReadFile(filepath.Join(proj, "ship.md"))).To(Equal([]byte("ship")))
		_, err := os.Lstat(run.Entries[1].Trash)
		Expect(err).To(MatchError(fs.ErrNotExist))
	})

	ginkgo.It("copies a directory back whole, then removes it from the run", func() {
		Expect(restore(dir, run.Entries[0], nil, crossDevice)).To(Succeed())
		Expect(os.ReadFile(filepath.Join(proj, "skill", "ref", "a.md"))).To(Equal([]byte("a")))
		_, err := os.Lstat(run.Entries[0].Trash)
		Expect(err).To(MatchError(fs.ErrNotExist))
	})

	ginkgo.It("removes only its own partial copy when the copy fails, keeping the trash copy", func() {
		if os.Geteuid() == 0 {
			ginkgo.Skip("root reads a file of mode 000")
		}
		locked := filepath.Join(run.Entries[0].Trash, "ref", "a.md")
		Expect(os.Chmod(locked, 0)).To(Succeed())
		ginkgo.DeferCleanup(os.Chmod, locked, os.FileMode(0o644))
		Expect(restore(dir, run.Entries[0], nil, crossDevice)).NotTo(Succeed())
		_, err := os.Lstat(filepath.Join(proj, "skill"))
		Expect(err).To(MatchError(fs.ErrNotExist), "the partial copy is gone")
		Expect(filepath.Join(run.Entries[0].Trash, "SKILL.md")).To(BeARegularFile(), "the trash copy is kept")
	})

	ginkgo.It("refuses an original created while it copied, touching neither copy", func() {
		made := func(oldpath, newpath string) error {
			Expect(os.WriteFile(filepath.Join(proj, "ship.md"), []byte("theirs"), 0o644)).To(Succeed())
			return crossDevice(oldpath, newpath)
		}
		Expect(restore(dir, run.Entries[1], nil, made)).To(MatchError(fs.ErrExist))
		Expect(os.ReadFile(filepath.Join(proj, "ship.md"))).To(Equal([]byte("theirs")))
		Expect(os.ReadFile(run.Entries[1].Trash)).To(Equal([]byte("ship")))
	})

	ginkgo.It("never removes an original it did not create when the copy fails before creating it", func() {
		// The trash copy vanishes and another process makes the original, both before the copy
		// creates anything: the copy fails, but not with fs.ErrExist.
		raced := func(oldpath, newpath string) error {
			Expect(os.Remove(run.Entries[1].Trash)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(proj, "ship.md"), []byte("theirs"), 0o644)).To(Succeed())
			return crossDevice(oldpath, newpath)
		}
		Expect(restore(dir, run.Entries[1], nil, raced)).To(MatchError(fs.ErrNotExist))
		Expect(os.ReadFile(filepath.Join(proj, "ship.md"))).To(Equal([]byte("theirs")))
	})

	ginkgo.It("restores an entry whose mode it cannot make private, saying so in a ModeError", func() {
		if os.Geteuid() == 0 {
			ginkgo.Skip("root opens a file of mode 000")
		}
		Expect(os.Chmod(run.Entries[1].Trash, 0)).To(Succeed())
		err := restore(dir, run.Entries[1], []string{run.Entries[1].Trash}, os.Rename)
		me, ok := errors.AsType[*ModeError](err)
		Expect(ok).To(BeTrue(), "%v", err)
		Expect(me.Paths).To(Equal([]string{filepath.Join(proj, "ship.md")}))
		_, err = os.Lstat(filepath.Join(proj, "ship.md"))
		Expect(err).NotTo(HaveOccurred(), "the move happened")
	})
})

var _ = ginkgo.Describe("making a restored directory's credential copies private", func() {
	ginkgo.It("tries every credential copy, reporting each one that failed", func() {
		if os.Geteuid() == 0 {
			ginkgo.Skip("root opens a file of mode 000")
		}
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj := filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(filepath.Join(proj, "cfg"), 0o755)).To(Succeed())
		for _, n := range []string{"a.json", "b.json"} {
			Expect(os.WriteFile(filepath.Join(proj, "cfg", n), []byte("{}"), 0o644)).To(Succeed())
		}
		t, err := OpenTrash(filepath.Join(tmp, "trash"), time.Now(), RunRecord{})
		Expect(err).NotTo(HaveOccurred())
		root, err := os.OpenRoot(proj)
		Expect(err).NotTo(HaveOccurred())
		to, err := t.Move("d", proj, root, "cfg")
		Expect(err).NotTo(HaveOccurred())
		Expect(root.Close()).To(Succeed())
		creds := []string{filepath.Join(to, "a.json"), filepath.Join(to, "b.json")}
		Expect(t.Close()).To(Succeed())
		run, err := ReadRun(t.Dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.Chmod(creds[0], 0)).To(Succeed(), "the first copy cannot be opened")

		err = restore(t.Dir, run.Entries[0], creds, os.Rename)
		me, ok := errors.AsType[*ModeError](err)
		Expect(ok).To(BeTrue(), "%v", err)
		Expect(me.Paths).To(Equal([]string{filepath.Join(proj, "cfg", "a.json")}))
		fi, err := os.Stat(filepath.Join(proj, "cfg", "b.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o600)), "the second copy was still made private")
	})

	ginkgo.It("says the restored copy stays at the original when the trash copy cannot be removed after copying", func() {
		if os.Geteuid() == 0 {
			ginkgo.Skip("root removes from a read-only directory")
		}
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj := filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(proj, "ship.md"), []byte("ship"), 0o644)).To(Succeed())
		t, err := OpenTrash(filepath.Join(tmp, "trash"), time.Now(), RunRecord{})
		Expect(err).NotTo(HaveOccurred())
		root, err := os.OpenRoot(proj)
		Expect(err).NotTo(HaveOccurred())
		to, err := t.Move("d", proj, root, "ship.md")
		Expect(err).NotTo(HaveOccurred())
		Expect(root.Close()).To(Succeed())
		Expect(t.Close()).To(Succeed())
		run, err := ReadRun(t.Dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.Chmod(filepath.Dir(to), 0o500)).To(Succeed())
		ginkgo.DeferCleanup(os.Chmod, filepath.Dir(to), os.FileMode(0o700))
		crossDevice := func(oldpath, newpath string) error {
			return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
		}

		err = restore(t.Dir, run.Entries[0], nil, crossDevice)
		Expect(err).To(HaveOccurred())
		Expect(errs.Hints(err)).To(ConsistOf(And(ContainSubstring("stays at "+filepath.Join(proj, "ship.md")), ContainSubstring("--force"))))
		Expect(os.ReadFile(filepath.Join(proj, "ship.md"))).To(Equal([]byte("ship")))
		Expect(to).To(BeARegularFile(), "the trash keeps its copy")
	})
})
