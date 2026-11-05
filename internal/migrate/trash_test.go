package migrate_test

import (
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/migrate"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("Trash", func() {
	var tmp, base, proj string
	now := time.Date(2026, 10, 12, 15, 4, 5, 0, time.FixedZone("CEST", 2*3600))
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		base, proj = filepath.Join(tmp, "state", "lola", "trash"), filepath.Join(tmp, "proj")
		writeTree(proj, map[string]string{
			".claude/commands/ship.md":       "ship\n",
			".claude/skills/review/SKILL.md": "skill\n",
			".claude/skills/review/ref/a.md": "a\n",
		})
	})
	open := func() *migrate.Trash {
		t, err := migrate.OpenTrash(base, now)
		Expect(err).NotTo(HaveOccurred())
		return t
	}
	manifest := func(t *migrate.Trash) string {
		b, err := os.ReadFile(filepath.Join(t.Dir, "manifest.txt"))
		Expect(err).NotTo(HaveOccurred())
		return string(b)
	}

	It("creates a private directory named after the UTC time, exclusively, with a suffix on a clash", func() {
		t := open()
		Expect(t.Dir).To(Equal(filepath.Join(base, "20261012T130405Z")))
		fi, err := os.Stat(t.Dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o700)))
		t2 := open()
		Expect(t2.Dir).To(Equal(filepath.Join(base, "20261012T130405Z-2")))
		Expect(t.Close()).To(Succeed())
		Expect(t2.Close()).To(Succeed())
	})

	It("coaches when the trash directory cannot be created", func() {
		writeFile(filepath.Join(tmp, "state", "lola"), "a file where a directory belongs")
		_, err := migrate.OpenTrash(base, now)
		Expect(err).To(MatchError(ContainSubstring("cannot create the trash directory")))
		Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("nothing was changed")))
	})

	It("moves files and directories into the destination's subdirectory, recording each in manifest.txt", func() {
		t := open()
		to, err := t.Move("abc", proj, filepath.Join(proj, ".claude/commands/ship.md"))
		Expect(err).NotTo(HaveOccurred())
		Expect(to).To(Equal(filepath.Join(t.Dir, "abc", ".claude", "commands", "ship.md")))
		Expect(os.ReadFile(to)).To(Equal([]byte("ship\n")))
		Expect(filepath.Join(proj, ".claude/commands/ship.md")).NotTo(BeAnExistingFile())

		dir, err := t.Move("abc", proj, filepath.Join(proj, ".claude/skills/review"))
		Expect(err).NotTo(HaveOccurred())
		Expect(os.ReadFile(filepath.Join(dir, "ref", "a.md"))).To(Equal([]byte("a\n")))
		Expect(manifest(t)).To(Equal(filepath.Join(proj, ".claude/commands/ship.md") + "\t" + to + "\n" +
			filepath.Join(proj, ".claude/skills/review") + "\t" + dir + "\n"))
		Expect(t.Close()).To(Succeed())
		Expect(t.Dir).To(BeADirectory())
	})

	It("copies backups, symbolic links as links, and leaves the original in place", func() {
		Expect(os.Symlink("SKILL.md", filepath.Join(proj, ".claude/skills/review/link.md"))).To(Succeed())
		Expect(os.Chmod(filepath.Join(proj, ".claude/skills/review/ref/a.md"), 0o755)).To(Succeed())
		t := open()
		to, err := t.Copy("abc", proj, filepath.Join(proj, ".claude/skills/review"))
		Expect(err).NotTo(HaveOccurred())
		Expect(os.ReadFile(filepath.Join(to, "SKILL.md"))).To(Equal([]byte("skill\n")))
		Expect(os.Readlink(filepath.Join(to, "link.md"))).To(Equal("SKILL.md"))
		fi, err := os.Stat(filepath.Join(to, "ref", "a.md"))
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o755)))
		Expect(filepath.Join(proj, ".claude/skills/review/SKILL.md")).To(BeAnExistingFile())
		Expect(manifest(t)).To(Equal(filepath.Join(proj, ".claude/skills/review") + "\t" + to + "\n"))

		_, err = t.Copy("abc", proj, filepath.Join(proj, ".claude/skills/review"))
		Expect(err).To(MatchError(ContainSubstring("already exists")), "a trash path is never overwritten")
	})

	It("refuses to copy what is neither a file, a directory nor a symbolic link", func() {
		mkfifo(filepath.Join(proj, ".claude/skills/review/pipe"))
		t := open()
		_, err := t.Copy("abc", proj, filepath.Join(proj, ".claude/skills/review"))
		Expect(err).To(MatchError(ContainSubstring("pipe is neither a file, a directory nor a symbolic link")))
	})

	It("keeps a path outside the destination's root under _outside", func() {
		other := filepath.Join(tmp, "elsewhere", "x.md")
		writeFile(other, "x")
		t := open()
		to, err := t.Move("user", proj, other)
		Expect(err).NotTo(HaveOccurred())
		Expect(to).To(Equal(filepath.Join(t.Dir, "user", "_outside", other)))
	})

	It("refuses a path manifest.txt cannot record unambiguously", func() {
		bad := filepath.Join(proj, "a\tb")
		writeFile(bad, "x")
		t := open()
		_, err := t.Move("abc", proj, bad)
		Expect(err).To(MatchError(ContainSubstring("cannot be recorded in manifest.txt")))
		Expect(bad).To(BeAnExistingFile())
	})

	It("refuses to write through a link copied into the trash", func() {
		outside := filepath.Join(tmp, "outside")
		writeFile(filepath.Join(outside, "keep.md"), "outside\n")
		Expect(os.MkdirAll(filepath.Join(proj, "x"), 0o755)).To(Succeed())
		Expect(os.Symlink(outside, filepath.Join(proj, "x", "ref"))).To(Succeed())
		By("a second base whose x/ref is a real directory, so its path maps onto the copied link")
		other := filepath.Join(tmp, "other")
		writeFile(filepath.Join(other, "x", "ref", "a.md"), "a\n")
		t := open()
		_, err := t.Copy("abc", proj, filepath.Join(proj, "x"))
		Expect(err).NotTo(HaveOccurred())
		Expect(os.Readlink(filepath.Join(t.Dir, "abc", "x", "ref"))).To(Equal(outside))

		_, err = t.Move("abc", other, filepath.Join(other, "x", "ref", "a.md"))
		Expect(err).To(MatchError(ContainSubstring("is not a directory")))
		Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("nothing was moved")))
		Expect(filepath.Join(other, "x", "ref", "a.md")).To(BeAnExistingFile())
		entries, err := os.ReadDir(outside)
		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(HaveLen(1), "nothing was written through the link")
	})

	It("prints an untrusted path in an error only escaped", func() {
		esc := filepath.Join(proj, "a\x1b[31mb")
		writeFile(esc, "x")
		t := open()
		_, err := t.Copy("abc", proj, esc)
		Expect(err).NotTo(HaveOccurred())
		_, err = t.Copy("abc", proj, esc)
		Expect(err).To(MatchError(ContainSubstring("already exists")))
		Expect(err.Error()).NotTo(ContainSubstring("\x1b"))
	})

	It("removes a trash directory nothing was put in when it is closed", func() {
		t := open()
		Expect(t.Close()).To(Succeed())
		Expect(t.Dir).NotTo(BeADirectory())
		Expect(t.Used()).To(BeFalse())
	})

	It("unlinks a leftover only when it matches its backup", func() {
		t := open()
		backup, err := t.Copy("abc", proj, filepath.Join(proj, ".claude/skills/review"))
		Expect(err).NotTo(HaveOccurred())
		writeFile(filepath.Join(proj, ".claude/skills/review/ref/a.md"), "edited\n")
		dir, err := os.OpenRoot(filepath.Join(proj, ".claude/skills/review"))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(dir.Close)
		bak, err := os.OpenRoot(backup)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(bak.Close)
		Expect(os.Remove(filepath.Join(backup, "ref", "b.md"))).To(MatchError(os.ErrNotExist))
		writeFile(filepath.Join(proj, ".claude/skills/review/ref/b.md"), "new\n")

		gone, err := migrate.UnlinkIfBackedUp(dir, bak, "SKILL.md")
		Expect(err).NotTo(HaveOccurred())
		Expect(gone).To(BeTrue())
		Expect(filepath.Join(proj, ".claude/skills/review/SKILL.md")).NotTo(BeAnExistingFile())
		gone, err = migrate.UnlinkIfBackedUp(dir, bak, "ref/a.md")
		Expect(err).NotTo(HaveOccurred())
		Expect(gone).To(BeFalse(), "an edit after the backup is kept")
		Expect(filepath.Join(proj, ".claude/skills/review/ref/a.md")).To(BeAnExistingFile())
		gone, err = migrate.UnlinkIfBackedUp(dir, bak, "ref/b.md")
		Expect(err).NotTo(HaveOccurred())
		Expect(gone).To(BeFalse(), "a file the backup lacks is kept")
	})
})
