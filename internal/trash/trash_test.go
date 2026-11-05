package trash_test

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
	"github.com/trevor-vaughan/lola-go/internal/trash"
)

var _ = Describe("the trash behaves as before its move out of migrate", func() {
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
	open := func() *trash.Trash {
		t, err := trash.OpenTrash(base, now, trash.RunRecord{Command: "lola tidy --user", Scope: "user"})
		Expect(err).NotTo(HaveOccurred())
		return t
	}
	move := func(t *trash.Trash, dest, base, orig string) (string, error) {
		parent, err := os.OpenRoot(filepath.Dir(orig))
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(parent.Close()).To(Succeed()) }()
		return t.Move(dest, base, parent, filepath.Base(orig))
	}
	manifest := func(t *trash.Trash) string {
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
		_, err := trash.OpenTrash(base, now, trash.RunRecord{})
		Expect(err).To(MatchError(ContainSubstring("cannot create the trash directory")))
		Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("nothing was changed")))
	})

	It("moves files and directories into the destination's subdirectory, recording each in manifest.txt", func() {
		t := open()
		to, err := move(t, "abc", proj, filepath.Join(proj, ".claude/commands/ship.md"))
		Expect(err).NotTo(HaveOccurred())
		Expect(to).To(Equal(filepath.Join(t.Dir, "abc", ".claude", "commands", "ship.md")))
		Expect(os.ReadFile(to)).To(Equal([]byte("ship\n")))
		Expect(filepath.Join(proj, ".claude/commands/ship.md")).NotTo(BeAnExistingFile())

		dir, err := move(t, "abc", proj, filepath.Join(proj, ".claude/skills/review"))
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
		to, err := move(t, "user", proj, other)
		Expect(err).NotTo(HaveOccurred())
		Expect(to).To(Equal(filepath.Join(t.Dir, "user", "_outside", other)))
	})

	It("refuses a path manifest.txt cannot record unambiguously", func() {
		bad := filepath.Join(proj, "a\tb")
		writeFile(bad, "x")
		t := open()
		_, err := move(t, "abc", proj, bad)
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

		_, err = move(t, "abc", other, filepath.Join(other, "x", "ref", "a.md"))
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

		gone, err := trash.UnlinkIfBackedUp(dir, bak, "SKILL.md")
		Expect(err).NotTo(HaveOccurred())
		Expect(gone).To(BeTrue())
		Expect(filepath.Join(proj, ".claude/skills/review/SKILL.md")).NotTo(BeAnExistingFile())
		gone, err = trash.UnlinkIfBackedUp(dir, bak, "ref/a.md")
		Expect(err).NotTo(HaveOccurred())
		Expect(gone).To(BeFalse(), "an edit after the backup is kept")
		Expect(filepath.Join(proj, ".claude/skills/review/ref/a.md")).To(BeAnExistingFile())
		gone, err = trash.UnlinkIfBackedUp(dir, bak, "ref/b.md")
		Expect(err).NotTo(HaveOccurred())
		Expect(gone).To(BeFalse(), "a file the backup lacks is kept")
	})
})

var _ = Describe("Trash.CopyBytes", func() {
	var tmp, base, proj string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		base, proj = filepath.Join(tmp, "state", "lola", "trash"), filepath.Join(tmp, "proj")
	})
	open := func() *trash.Trash {
		t, err := trash.OpenTrash(base, time.Now(), trash.RunRecord{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(t.Close)
		return t
	}
	manifest := func(t *trash.Trash) string {
		b, err := os.ReadFile(filepath.Join(t.Dir, "manifest.txt"))
		Expect(err).NotTo(HaveOccurred())
		return string(b)
	}

	It("CopyBytes writes exactly the given bytes at 0600 and never over an existing path", func() {
		orig := filepath.Join(proj, ".mcp.json")
		writeFile(orig, "on disk\n")
		Expect(os.Chmod(orig, 0o644)).To(Succeed())
		t := open()
		to, err := t.CopyBytes("abc", proj, orig, []byte("verified\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(to).To(Equal(filepath.Join(t.Dir, "abc", ".mcp.json")))
		Expect(os.ReadFile(to)).To(Equal([]byte("verified\n")), "the given bytes, never orig's")
		fi, err := os.Lstat(to)
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode()).To(Equal(os.FileMode(0o600)), "a regular file at 0600 whatever orig's mode")
		Expect(manifest(t)).To(Equal(orig + "\t" + to + "\n"))
		Expect(os.ReadFile(orig)).To(Equal([]byte("on disk\n")))

		_, err = t.CopyBytes("abc", proj, orig, []byte("again\n"))
		Expect(err).To(MatchError(ContainSubstring("already exists")))
		Expect(os.ReadFile(to)).To(Equal([]byte("verified\n")))

		By("a link where the copy goes is not written through")
		elsewhere := filepath.Join(tmp, "elsewhere")
		Expect(os.Symlink(elsewhere, filepath.Join(t.Dir, "abc", "linked.json"))).To(Succeed())
		_, err = t.CopyBytes("abc", proj, filepath.Join(proj, "linked.json"), []byte("x"))
		Expect(err).To(MatchError(ContainSubstring("already exists")))
		Expect(elsewhere).NotTo(BeAnExistingFile())
	})
})

var _ = Describe("a trash run's record", func() {
	var tmp, base, proj string
	now := time.Date(2026, 10, 12, 15, 4, 5, 0, time.FixedZone("CEST", 2*3600))
	rec := trash.RunRecord{Command: "lola tidy --project", Scope: "project", Root: "/work/proj"}
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		base, proj = filepath.Join(tmp, "state", "lola", "trash"), filepath.Join(tmp, "proj")
		writeTree(proj, map[string]string{".mcp.json": "{}\n", ".claude/commands/ship.md": "ship\n"})
	})
	// filled is a closed run holding proj's .mcp.json, marked a credential copy, and ship.md.
	filled := func() (dir, mcp, ship string) {
		t, err := trash.OpenTrash(base, now, rec)
		Expect(err).NotTo(HaveOccurred())
		mcp, err = t.CopyBytes("abc", proj, filepath.Join(proj, ".mcp.json"), []byte("{}\n"))
		Expect(err).NotTo(HaveOccurred())
		t.MarkCredential(mcp)
		ship, err = t.Copy("abc", proj, filepath.Join(proj, ".claude/commands/ship.md"))
		Expect(err).NotTo(HaveOccurred())
		Expect(t.Close()).To(Succeed())
		return t.Dir, mcp, ship
	}

	It("writes run.yaml at open, private, naming the command, scope, root and time", func() {
		t, err := trash.OpenTrash(base, now, rec)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(t.Close)
		fi, err := os.Stat(filepath.Join(t.Dir, "run.yaml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o600)))
		run, err := trash.ReadRun(t.Dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(run).To(Equal(trash.Run{ID: "20261012T130405Z", Time: now.UTC(), Command: "lola tidy --project", Scope: "project",
			Root: "/work/proj", Credentials: []string{}, Entries: []trash.Entry{}, Notices: []string{}}))
	})

	It("rewrites run.yaml at close, atomically, listing the copies that may hold credentials", func() {
		dir, mcp, ship := filled()
		run, err := trash.ReadRun(dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(run.Credentials).To(Equal([]string{mcp}))
		Expect(run.Entries).To(Equal([]trash.Entry{
			{Original: filepath.Join(proj, ".mcp.json"), Trash: mcp, Credential: true},
			{Original: filepath.Join(proj, ".claude/commands/ship.md"), Trash: ship},
		}))
		Expect(run.Command).To(Equal("lola tidy --project"))
		Expect(run.Notices).To(BeEmpty())
		names, err := os.ReadDir(dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(names).To(HaveLen(3), "abc, manifest.txt and run.yaml: no temporary file is left")
		fi, err := os.Stat(filepath.Join(dir, "run.yaml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o600)))
	})

	It("reads a run written before run.yaml as made by an unknown command at the time its name says", func() {
		dir, _, ship := filled()
		Expect(os.Remove(filepath.Join(dir, "run.yaml"))).To(Succeed())
		run, err := trash.ReadRun(dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(run.Command).To(Equal("unknown"))
		Expect(run.Time).To(Equal(now.UTC()))
		Expect(run.Scope + run.Root).To(BeEmpty())
		Expect(run.Notices).To(BeEmpty(), "an old run is not a fault")
		Expect(run.Entries).To(HaveLen(2))
		Expect(run.Entries[1]).To(Equal(trash.Entry{Original: filepath.Join(proj, ".claude/commands/ship.md"), Trash: ship}))
	})

	It("treats a malformed run.yaml as absent, with a notice", func() {
		dir, _, _ := filled()
		writeFile(filepath.Join(dir, "run.yaml"), "command: [lola\n")
		run, err := trash.ReadRun(dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(run.Command).To(Equal("unknown"))
		Expect(run.Credentials).To(BeEmpty())
		Expect(run.Time).To(Equal(now.UTC()))
		Expect(run.Notices).To(ConsistOf(ContainSubstring("run.yaml")))

		writeFile(filepath.Join(dir, "run.yaml"), "command: lola tidy\ntime: yesterday\n")
		run, err = trash.ReadRun(dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(run.Command).To(Equal("unknown"), "a time that does not parse makes the file malformed")
		Expect(run.Notices).To(HaveLen(1))
	})

	It("ignores keys run.yaml does not define", func() {
		dir, _, _ := filled()
		writeFile(filepath.Join(dir, "run.yaml"), "command: lola migrate\nscope: user\nfuture: [1, 2]\n")
		run, err := trash.ReadRun(dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(run.Command).To(Equal("lola migrate"))
		Expect(run.Scope).To(Equal("user"))
		Expect(run.Notices).To(BeEmpty())
	})

	It("refuses a manifest line whose trash path lies outside the run, or whose original is not absolute", func() {
		dir, mcp, _ := filled()
		outside := filepath.Join(tmp, "outside")
		Expect(os.MkdirAll(outside, 0o755)).To(Succeed())
		Expect(os.Symlink(outside, filepath.Join(dir, "linked"))).To(Succeed())
		f, err := os.OpenFile(filepath.Join(dir, "manifest.txt"), os.O_WRONLY|os.O_APPEND, 0)
		Expect(err).NotTo(HaveOccurred())
		_, err = f.WriteString("/etc/passwd\t" + filepath.Join(tmp, "elsewhere") + "\n" +
			"/x\t" + dir + "/../other-run/x\n" +
			"/x\t" + dir + "\n" +
			"/x\t" + filepath.Join(dir, "linked", "x") + "\n" +
			"relative/x\t" + filepath.Join(dir, "abc", "x") + "\n" +
			"no tab \x1b[2J\n")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Close()).To(Succeed())
		writeFile(filepath.Join(dir, "run.yaml"), "command: lola tidy\ncredentials: ["+mcp+", /etc/shadow]\n")

		run, err := trash.ReadRun(dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(run.Entries).To(HaveLen(2), "only the lines lola wrote")
		Expect(run.Credentials).To(Equal([]string{mcp}))
		Expect(run.Notices).To(HaveLen(7))
		Expect(run.Notices).To(HaveEach(Not(ContainSubstring("\x1b"))))
		Expect(run.Notices).To(ContainElement(ContainSubstring("/etc/shadow")))
		Expect(run.Notices).To(ContainElement(ContainSubstring("relative/x")))
	})

	It("fails on a run without manifest.txt", func() {
		Expect(os.MkdirAll(filepath.Join(base, "20261012T130405Z"), 0o700)).To(Succeed())
		_, err := trash.ReadRun(filepath.Join(base, "20261012T130405Z"))
		Expect(err).To(MatchError(ContainSubstring("manifest.txt")))
	})
})

var _ = Describe("reading a run back for lola trash", func() {
	var tmp, base, proj string
	now := time.Date(2026, 10, 12, 15, 4, 5, 0, time.UTC)
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		base, proj = filepath.Join(tmp, "state", "lola", "trash"), filepath.Join(tmp, "proj")
		writeTree(proj, map[string]string{"skills/review/SKILL.md": "skill\n", "skills/review/ref/a.md": "a\n", "ship.md": "ship\n"})
	})
	// moved is a closed run holding proj's review skill and ship.md, moved.
	moved := func() (dir, skill, ship string) {
		t, err := trash.OpenTrash(base, now, trash.RunRecord{Command: "lola tidy", Scope: "project", Root: proj})
		Expect(err).NotTo(HaveOccurred())
		root, err := os.OpenRoot(filepath.Join(proj, "skills"))
		Expect(err).NotTo(HaveOccurred())
		skill, err = t.Move("abc", proj, root, "review")
		Expect(err).NotTo(HaveOccurred())
		Expect(root.Close()).To(Succeed())
		root, err = os.OpenRoot(proj)
		Expect(err).NotTo(HaveOccurred())
		ship, err = t.Move("abc", proj, root, "ship.md")
		Expect(err).NotTo(HaveOccurred())
		Expect(root.Close()).To(Succeed())
		Expect(t.Close()).To(Succeed())
		return t.Dir, skill, ship
	}

	It("keeps a manifest line whose trash path runs through a directory that is gone", func() {
		dir, skill, _ := moved()
		Expect(os.RemoveAll(skill)).To(Succeed())
		f, err := os.OpenFile(filepath.Join(dir, "manifest.txt"), os.O_WRONLY|os.O_APPEND, 0)
		Expect(err).NotTo(HaveOccurred())
		_, err = f.WriteString("/x/a.md\t" + filepath.Join(skill, "gone", "a.md") + "\n")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Close()).To(Succeed())
		run, err := trash.ReadRun(dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(run.Notices).To(BeEmpty())
		Expect(run.Entries).To(ContainElement(trash.Entry{Original: "/x/a.md", Trash: filepath.Join(skill, "gone", "a.md")}))
	})

	It("keeps a manifest line whose trash path is itself a link", func() {
		dir, _, ship := moved()
		Expect(os.Remove(ship)).To(Succeed())
		Expect(os.Symlink(filepath.Join(tmp, "elsewhere"), ship)).To(Succeed())
		run, err := trash.ReadRun(dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(run.Notices).To(BeEmpty())
		Expect(run.Entries).To(ContainElement(trash.Entry{Original: filepath.Join(proj, "ship.md"), Trash: ship}))
	})

	It("reads at most 1 MiB of run.yaml and of manifest.txt", func() {
		dir, _, _ := moved()
		big := "command: lola tidy\n#" + strings.Repeat("x", 1<<20) + "\n"
		writeFile(filepath.Join(dir, "run.yaml"), big)
		run, err := trash.ReadRun(dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(run.Command).To(Equal("unknown"))
		Expect(run.Notices).To(ConsistOf(And(ContainSubstring("run.yaml"), ContainSubstring("1 MiB"))))

		writeFile(filepath.Join(dir, "manifest.txt"), strings.Repeat("/x\t"+dir+"/abc/x\n", 1<<16))
		_, err = trash.ReadRun(dir)
		Expect(err).To(MatchError(And(ContainSubstring("manifest.txt"), ContainSubstring("1 MiB"))))
	})

	It("measures an entry's files and bytes without following a link", func() {
		dir, skill, ship := moved()
		Expect(os.Symlink("/etc", filepath.Join(skill, "etc"))).To(Succeed())
		files, size, err := trash.Measure(dir, trash.Entry{Trash: skill})
		Expect(err).NotTo(HaveOccurred())
		Expect(files).To(Equal(3), "two files and a link")
		Expect(size).To(Equal(int64(len("skill\na\n"))))
		files, size, err = trash.Measure(dir, trash.Entry{Trash: ship})
		Expect(err).NotTo(HaveOccurred())
		Expect([]any{files, size}).To(Equal([]any{1, int64(5)}))
		_, _, err = trash.Measure(dir, trash.Entry{Trash: filepath.Join(dir, "abc", "gone")})
		Expect(err).To(MatchError(os.ErrNotExist))
	})
})

var _ = Describe("restoring a trash entry", func() {
	var tmp, base, proj string
	now := time.Date(2026, 10, 12, 15, 4, 5, 0, time.UTC)
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		base, proj = filepath.Join(tmp, "state", "lola", "trash"), filepath.Join(tmp, "proj")
		writeTree(proj, map[string]string{"skills/review/SKILL.md": "skill\n", "skills/review/.env": "TOKEN=x\n", "ship.md": "ship\n"})
	})
	// run moves names (paths relative to proj) into a closed run, marking marked (trash paths
	// relative to the run's abc subdirectory) as credential copies, and reads it back.
	run := func(marked []string, names ...string) trash.Run {
		GinkgoHelper()
		t, err := trash.OpenTrash(base, now, trash.RunRecord{Command: "lola migrate", Scope: "user"})
		Expect(err).NotTo(HaveOccurred())
		for _, n := range names {
			root, err := os.OpenRoot(filepath.Join(proj, filepath.Dir(n)))
			Expect(err).NotTo(HaveOccurred())
			_, err = t.Move("abc", proj, root, filepath.Base(n))
			Expect(err).NotTo(HaveOccurred())
			Expect(root.Close()).To(Succeed())
		}
		for _, m := range marked {
			t.MarkCredential(filepath.Join(t.Dir, "abc", m))
		}
		Expect(t.Close()).To(Succeed())
		r, err := trash.ReadRun(t.Dir)
		Expect(err).NotTo(HaveOccurred())
		return r
	}
	dirOf := func(r trash.Run) string { return filepath.Join(base, r.ID) }

	It("moves a file back, making the directories it was in", func() {
		r := run(nil, "ship.md")
		Expect(os.RemoveAll(proj)).To(Succeed())
		Expect(trash.Restore(dirOf(r), r.Entries[0], r.Credentials)).To(Succeed())
		Expect(os.ReadFile(filepath.Join(proj, "ship.md"))).To(Equal([]byte("ship\n")))
		_, err := os.Lstat(r.Entries[0].Trash)
		Expect(err).To(MatchError(os.ErrNotExist))
	})

	It("moves a directory back whole, a credential copy inside it at 0600", func() {
		r := run([]string{"skills/review/.env"}, "skills/review")
		Expect(r.Credentials).To(HaveLen(1))
		Expect(r.Entries[0].Credential).To(BeFalse(), "the mark names a path inside the directory entry")
		Expect(trash.Restore(dirOf(r), r.Entries[0], r.Credentials)).To(Succeed())
		Expect(os.ReadFile(filepath.Join(proj, "skills/review/SKILL.md"))).To(Equal([]byte("skill\n")))
		fi, err := os.Stat(filepath.Join(proj, "skills/review/.env"))
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o600)))
		fi, err = os.Stat(filepath.Join(proj, "skills/review/SKILL.md"))
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o644)))
	})

	It("restores a credential copy at 0600", func() {
		r := run([]string{"ship.md"}, "ship.md")
		Expect(r.Entries[0].Credential).To(BeTrue())
		Expect(trash.Restore(dirOf(r), r.Entries[0], r.Credentials)).To(Succeed())
		fi, err := os.Stat(filepath.Join(proj, "ship.md"))
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o600)))
	})

	It("moves a link back as a link and never changes its target's mode, though marked a credential copy", func() {
		target := filepath.Join(tmp, "target.md")
		writeFile(target, "t\n")
		Expect(os.Symlink(target, filepath.Join(proj, "ln.md"))).To(Succeed())
		r := run([]string{"ln.md"}, "ln.md")
		Expect(trash.Restore(dirOf(r), r.Entries[0], r.Credentials)).To(Succeed())
		Expect(os.Readlink(filepath.Join(proj, "ln.md"))).To(Equal(target))
		fi, err := os.Stat(target)
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o644)))
	})

	It("refuses to restore over an original that exists", func() {
		r := run(nil, "ship.md")
		writeFile(filepath.Join(proj, "ship.md"), "new\n")
		Expect(trash.Restore(dirOf(r), r.Entries[0], r.Credentials)).To(MatchError(os.ErrExist))
		Expect(os.ReadFile(filepath.Join(proj, "ship.md"))).To(Equal([]byte("new\n")))
		Expect(r.Entries[0].Trash).To(BeARegularFile())
	})

	It("refuses an entry whose directory became a link out of the run after it was read", func() {
		r := run(nil, "skills/review/SKILL.md")
		outside := filepath.Join(tmp, "outside")
		writeFile(filepath.Join(outside, "SKILL.md"), "outside\n")
		held := filepath.Dir(r.Entries[0].Trash)
		Expect(os.RemoveAll(held)).To(Succeed())
		Expect(os.Symlink(outside, held)).To(Succeed())
		Expect(trash.Restore(dirOf(r), r.Entries[0], r.Credentials)).NotTo(Succeed())
		Expect(filepath.Join(outside, "SKILL.md")).To(BeARegularFile(), "nothing outside the run is moved")
		_, err := os.Lstat(filepath.Join(proj, "skills/review/SKILL.md"))
		Expect(err).To(MatchError(os.ErrNotExist))
	})

	It("forgets restored lines and removes the run once it lists nothing", func() {
		r := run(nil, "ship.md", "skills/review")
		Expect(trash.Restore(dirOf(r), r.Entries[0], r.Credentials)).To(Succeed())
		gone, err := trash.Forget(dirOf(r), r.Entries[:1])
		Expect(err).NotTo(HaveOccurred())
		Expect(gone).To(BeFalse())
		left, err := trash.ReadRun(dirOf(r))
		Expect(err).NotTo(HaveOccurred())
		Expect(left.Entries).To(Equal(r.Entries[1:]))
		Expect(left.Command).To(Equal("lola migrate"))

		Expect(trash.Restore(dirOf(r), r.Entries[1], r.Credentials)).To(Succeed())
		gone, err = trash.Forget(dirOf(r), r.Entries[1:])
		Expect(err).NotTo(HaveOccurred())
		Expect(gone).To(BeTrue())
		Expect(dirOf(r)).NotTo(BeADirectory())
	})

	It("keeps a run that lists nothing but still holds a file", func() {
		r := run(nil, "ship.md")
		writeFile(filepath.Join(dirOf(r), "abc", "stray"), "x\n")
		Expect(trash.Restore(dirOf(r), r.Entries[0], r.Credentials)).To(Succeed())
		gone, err := trash.Forget(dirOf(r), r.Entries)
		Expect(err).NotTo(HaveOccurred())
		Expect(gone).To(BeFalse())
		Expect(filepath.Join(dirOf(r), "abc", "stray")).To(BeARegularFile())
	})
})
