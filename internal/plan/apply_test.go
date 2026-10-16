package plan_test

import (
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("Apply", func() {
	var root, ledgerPath string
	var stops []string

	write := func(rel, content string) {
		Expect(os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644)).To(Succeed())
	}
	change := func(rel, data string) plan.FileChange {
		return plan.FileChange{Target: "claude-code", Logical: rel, Abs: filepath.Join(root, rel), Root: root, Data: []byte(data), Mode: 0o644, StopDirs: stops}
	}
	del := func(rel string) plan.FileChange {
		return plan.FileChange{Target: "claude-code", Logical: rel, Abs: filepath.Join(root, rel), Root: root, Delete: true, StopDirs: stops}
	}
	// leftovers lists lola's temp and backup files anywhere under root.
	leftovers := func() []string {
		var out []string
		Expect(filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err == nil && (strings.Contains(d.Name(), ".lola-new-") || strings.Contains(d.Name(), ".lola-bak-")) {
				out = append(out, p)
			}
			return err
		})).To(Succeed())
		return out
	}

	BeforeEach(func() {
		var err error
		root, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		ledgerPath = filepath.Join(root, ".lola", "lock.json")
		stops = []string{root, filepath.Join(root, ".claude/skills")}
	})

	It("creates, replaces and deletes files, then writes the ledger, then runs after", func() {
		write("keep.md", "old")
		write(".claude/skills/s/ref/gone.md", "bye")
		l := ledger.New()
		l.Modules["a"] = &ledger.Entry{Digest: "sha256:1"}
		p := &plan.Plan{Ledger: l, Files: []plan.FileChange{
			change(".claude/commands/new.md", "N"), change("keep.md", "new"), del(".claude/skills/s/ref/gone.md"),
		}}
		p.Files[0].Mode = 0o755
		ran := false
		Expect(plan.Apply(p, ledgerPath, func() error { ran = true; return nil })).To(Succeed())
		Expect(os.ReadFile(filepath.Join(root, ".claude/commands/new.md"))).To(Equal([]byte("N")))
		fi, err := os.Stat(filepath.Join(root, ".claude/commands/new.md"))
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o755)))
		Expect(os.ReadFile(filepath.Join(root, "keep.md"))).To(Equal([]byte("new")))
		Expect(filepath.Join(root, ".claude/skills/s")).NotTo(BeADirectory(), "emptied dirs are removed")
		Expect(filepath.Join(root, ".claude/skills")).To(BeADirectory(), "stop dirs are kept")
		Expect(ledgerPath).To(BeARegularFile())
		Expect(ran).To(BeTrue())
		Expect(leftovers()).To(BeEmpty())
	})

	It("keeps a directory that still has other files", func() {
		write(".claude/skills/s/a.md", "a")
		write(".claude/skills/s/b.md", "b")
		Expect(plan.Apply(&plan.Plan{Ledger: ledger.New(), Files: []plan.FileChange{del(".claude/skills/s/a.md")}}, ledgerPath, nil)).To(Succeed())
		Expect(filepath.Join(root, ".claude/skills/s/b.md")).To(BeARegularFile())
	})

	It("restores every file and removes staged state when the ledger cannot be written (Review Focus 4)", func() {
		if os.Geteuid() == 0 {
			Skip("root ignores directory permissions")
		}
		write("keep.md", "original")
		write("gone.md", "still here")
		Expect(os.MkdirAll(filepath.Dir(ledgerPath), 0o755)).To(Succeed())
		Expect(os.Chmod(filepath.Dir(ledgerPath), 0o555)).To(Succeed())
		DeferCleanup(os.Chmod, filepath.Dir(ledgerPath), os.FileMode(0o755))

		p := &plan.Plan{Ledger: ledger.New(), Files: []plan.FileChange{
			change("deep/new/dir/x.md", "X"), change("keep.md", "replaced"), del("gone.md"),
		}}
		err := plan.Apply(p, ledgerPath, func() error { Fail("after must not run"); return nil })
		Expect(err).To(HaveOccurred())
		Expect(os.ReadFile(filepath.Join(root, "keep.md"))).To(Equal([]byte("original")))
		Expect(os.ReadFile(filepath.Join(root, "gone.md"))).To(Equal([]byte("still here")))
		Expect(filepath.Join(root, "deep")).NotTo(BeADirectory())
		Expect(leftovers()).To(BeEmpty())
		Expect(ledgerPath).NotTo(BeAnExistingFile())
	})

	It("rolls back when staging fails partway through", func() {
		if os.Geteuid() == 0 {
			Skip("root ignores directory permissions")
		}
		write("a.md", "A")
		Expect(os.MkdirAll(filepath.Join(root, "locked"), 0o555)).To(Succeed())
		DeferCleanup(os.Chmod, filepath.Join(root, "locked"), os.FileMode(0o755))
		p := &plan.Plan{Ledger: ledger.New(), Files: []plan.FileChange{change("a.md", "changed"), change("locked/x.md", "X")}}
		Expect(plan.Apply(p, ledgerPath, nil)).NotTo(Succeed())
		Expect(os.ReadFile(filepath.Join(root, "a.md"))).To(Equal([]byte("A")))
		Expect(leftovers()).To(BeEmpty())
	})

	It("returns the after error with the changes applied", func() {
		p := &plan.Plan{Ledger: ledger.New(), Files: []plan.FileChange{change("x.md", "X")}}
		err := plan.Apply(p, ledgerPath, func() error { return os.ErrPermission })
		Expect(err).To(MatchError(os.ErrPermission))
		Expect(filepath.Join(root, "x.md")).To(BeARegularFile())
	})

	It("deletes through a symlink and removes the link once it dangles", func() {
		outside, err := filepath.EvalSymlinks(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(outside, "CLAUDE.md"), []byte("x"), 0o644)).To(Succeed())
		Expect(os.Symlink(filepath.Join(outside, "CLAUDE.md"), filepath.Join(root, "CLAUDE.md"))).To(Succeed())
		fc := del("CLAUDE.md")
		fc.Abs, fc.Outside = filepath.Join(outside, "CLAUDE.md"), true
		Expect(plan.Apply(&plan.Plan{Ledger: ledger.New(), Files: []plan.FileChange{fc}}, ledgerPath, nil)).To(Succeed())
		Expect(filepath.Join(outside, "CLAUDE.md")).NotTo(BeAnExistingFile())
		_, err = os.Lstat(filepath.Join(root, "CLAUDE.md"))
		Expect(os.IsNotExist(err)).To(BeTrue())
		Expect(outside).To(BeADirectory())
	})

	It("leaves an existing ledger byte-identical, and nothing staged, when the ledger directory is unwritable", func() {
		if os.Geteuid() == 0 {
			Skip("root ignores directory permissions")
		}
		// Create and save an old ledger.
		oldLedger := ledger.New()
		oldLedger.Modules["old"] = &ledger.Entry{Digest: "sha256:old"}
		Expect(os.MkdirAll(filepath.Dir(ledgerPath), 0o755)).To(Succeed())
		Expect(oldLedger.Save(ledgerPath)).To(Succeed())
		oldContent, err := os.ReadFile(ledgerPath)
		Expect(err).NotTo(HaveOccurred())

		// Make the ledger dir read-only so Save fails.
		Expect(os.Chmod(filepath.Dir(ledgerPath), 0o555)).To(Succeed())
		DeferCleanup(os.Chmod, filepath.Dir(ledgerPath), os.FileMode(0o755))

		// Try to apply a plan; it should fail when creating temp file for the new ledger.
		p := &plan.Plan{Ledger: ledger.New(), Files: []plan.FileChange{change("x.md", "X")}}
		err = plan.Apply(p, ledgerPath, nil)
		Expect(err).To(HaveOccurred())

		// The old ledger should still exist with its original content unchanged.
		restoredContent, err := os.ReadFile(ledgerPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(restoredContent).To(Equal(oldContent))

		// The file project files were not staged or placed.
		Expect(filepath.Join(root, "x.md")).NotTo(BeAnExistingFile())

		// No temp or backup files should be left.
		Expect(leftovers()).To(BeEmpty())
	})
})
