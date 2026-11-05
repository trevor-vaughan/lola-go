package migrate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
	"github.com/trevor-vaughan/lola-go/internal/trash"
)

var _ = Describe("Cleaning up a replaced directory", func() {
	var proj, backup string
	BeforeEach(func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, backup = filepath.Join(tmp, "proj"), filepath.Join(tmp, "trash", "skill")
		for _, d := range []string{filepath.Join(proj, ".claude", "skills", "r"), backup} {
			Expect(os.MkdirAll(d, 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(d, "old.md"), []byte("python\n"), 0o644)).To(Succeed())
		}
	})

	It("keeps a file the backup lacks as one that appeared after it, its name made safe to print", func() {
		Expect(os.WriteFile(filepath.Join(proj, ".claude", "skills", "r", "late\x1b[2J.md"), []byte("new\n"), 0o644)).To(Succeed())
		dc := decision{rec: Recorded{Target: "claude-code", Kind: KindSkill, Root: proj, Rel: ".claude/skills/r", Dir: true}, act: actReplace, backup: backup}
		dr := &DestResult{}
		Expect((&runner{}).leftovers(dc, &ledger.Ledger{Modules: map[string]*ledger.Entry{}}, dr)).To(Succeed())
		Expect(dr.Kept).To(ConsistOf(Kept{Path: filepath.Join(proj, ".claude", "skills", "r", "late?[2J.md"),
			Reason: "appeared after the backup, so lola did not remove it"}))
		Expect(filepath.Join(proj, ".claude", "skills", "r", "old.md")).NotTo(BeAnExistingFile(), "a file matching its backup is unlinked")
	})
})

var _ = Describe("Moving a recorded path after the install", func() {
	It("keeps a path a lola-go module came to own since the plan, checking the ledger after the install", func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj := filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(filepath.Join(proj, ".claude", "agents"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(proj, ".claude", "agents", "critic.md"), []byte("x\n"), 0o644)).To(Succeed())
		t, err := trash.OpenTrash(filepath.Join(tmp, "trash"), time.Now())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(t.Close)
		rec := Recorded{Target: "claude-code", Kind: KindAgent, Root: proj, Rel: ".claude/agents/critic.md"}
		post := &ledger.Ledger{Modules: map[string]*ledger.Entry{"other": {Artifacts: []ledger.Artifact{
			{Target: "claude-code", Kind: "agent", Path: ".claude/agents/critic.md"}}}}}
		dr := &DestResult{}
		Expect((&runner{trash: t}).cleanup(&Destination{ID: "d", Root: proj}, decision{rec: rec, act: actMove}, post, dr)).To(Succeed())
		Expect(dr.Moved).To(BeEmpty())
		Expect(dr.Kept).To(ConsistOf(Kept{Path: rec.Abs(), Reason: "owned by lola-go module other"}))
		Expect(rec.Abs()).To(BeAnExistingFile())
	})
})

var _ = Describe("Asking git whether it tracks a path", func() {
	It("treats a tracked file named like git pathspec magic as tracked", func() {
		proj, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		file := filepath.Join(proj, ":(top)critic.md")
		Expect(os.WriteFile(file, []byte("x\n"), 0o644)).To(Succeed())
		for _, args := range [][]string{{"init", "-q"}, {"--literal-pathspecs", "add", ":(top)critic.md"}, {"commit", "-q", "-m", "x"}} {
			out, err := exec.Command("git", append([]string{"-C", proj}, args...)...).CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), string(out))
		}
		Expect(tracked(context.Background(), file)).To(Equal("tracked by git; --force moves it anyway"))
	})
})
