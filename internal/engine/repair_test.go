package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("Repair", func() {
	var (
		tmp, proj, lp string
		p             paths.Paths
		e             *engine.Engine
		sc            engine.Scope
		ctx           = context.Background()
		user          = engine.Scope{Kind: target.User}
	)
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		proj = filepath.Join(tmp, "proj")
		e.Getwd = func() (string, error) { return proj, nil } // hints are written for a user in the project
		sc = engine.Scope{Kind: target.Project, ProjectDir: proj}
		lp = filepath.Join(proj, ".lola", "lock.json")
		kit := filepath.Join(tmp, "kit")
		writeTree(kit, map[string]string{"lola.yml": "name: kit\n", "commands/hi.md": "Hi\n"})
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: kit}}, engine.Options{Targets: []string{"claude-code"}})
		Expect(err).NotTo(HaveOccurred())
	})
	// edit changes lock.json behind lola's back, as a hand edit would.
	edit := func(f func(*ledger.Ledger)) {
		GinkgoHelper()
		l, err := ledger.LoadKeepingStrays(lp)
		Expect(err).NotTo(HaveOccurred())
		f(l)
		Expect(l.Save(lp)).To(Succeed())
	}
	read := func() []byte {
		GinkgoHelper()
		b, err := os.ReadFile(lp)
		Expect(err).NotTo(HaveOccurred())
		return b
	}
	entries := func(r *engine.RepairResult) []string {
		out := []string{}
		for _, it := range r.Forgotten {
			out = append(out, it.Entry)
		}
		return out
	}
	syncNotice := []engine.Notice{{Level: engine.LevelInfo, Text: "lola sync puts back any file a module now lacks → lola sync --project"}}

	It("lists every entry lola could not have written, then forgets exactly those and touches no file", func() {
		writeTree(tmp, map[string]string{"outside.md": "mine\n"})
		edit(func(l *ledger.Ledger) {
			k := l.Modules["kit"]
			k.Targets = append(k.Targets, "bogus")
			k.Artifacts = append(k.Artifacts,
				ledger.Artifact{Target: "claude-code", Kind: "file", Path: "../outside.md"},
				ledger.Artifact{Target: "bogus", Kind: "file", Path: "x.md"})
			l.Created = append(l.Created, ledger.FileRef{Target: "claude-code", Path: "../settings.json", Key: "/hooks"})
		})
		before := read()
		want := []string{
			`module kit: target "bogus"`,
			`module kit: artifact "x.md" on target "bogus"`,
			`module kit: artifact "../outside.md" on target "claude-code"`,
			`created "../settings.json" key "/hooks" on target "claude-code"`,
		}
		plan, err := e.PlanRepair(engine.RepairRequest{Scope: sc})
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.DryRun).To(BeTrue())
		Expect(plan.File).To(Equal(lp))
		Expect(entries(plan)).To(Equal(want))
		Expect(plan.Forgotten[2]).To(Equal(engine.RepairItem{Entry: want[2], Kind: ledger.StrayArtifact, Module: "kit",
			Target: "claude-code", Path: "../outside.md", Key: ""}))
		Expect(plan.ForgottenModules).To(BeEmpty())
		Expect(plan.Notices).To(BeEmpty(), "a plan names no next step")
		Expect(read()).To(Equal(before), "a plan writes nothing")

		res, err := e.ApplyRepair(plan)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.DryRun).To(BeFalse())
		Expect(entries(res)).To(Equal(want))
		Expect(res.Notices).To(Equal(syncNotice), "a path on a target lola knows was forgotten")
		led, err := e.Ledger(sc)
		Expect(err).NotTo(HaveOccurred(), "every command reads lock.json again")
		Expect(led.Modules["kit"].Targets).To(Equal([]string{"claude-code"}))
		Expect(led.Modules["kit"].Artifacts).To(HaveExactElements(HaveField("Path", ".claude/commands/hi.md")))
		Expect(led.Created).To(BeEmpty())
		Expect(os.ReadFile(filepath.Join(tmp, "outside.md"))).To(Equal([]byte("mine\n")), "no file is touched")
		Expect(filepath.Join(proj, ".claude", "commands", "hi.md")).To(BeARegularFile())
	})

	It("forgets a module left on no target lola knows, and keeps the targets of an entry from before lock.json recorded them", func() {
		edit(func(l *ledger.Ledger) {
			ghost := *l.Modules["kit"]
			ghost.Targets, ghost.Artifacts = []string{"bogus"}, []ledger.Artifact{{Target: "bogus", Kind: "file", Path: "x.md"}}
			l.Modules["ghost"] = &ghost
			k := l.Modules["kit"]
			k.Targets = nil
			k.Artifacts = append(k.Artifacts, ledger.Artifact{Target: "claude-code", Kind: "file", Path: "../y.md"})
		})
		plan, err := e.PlanRepair(engine.RepairRequest{Scope: sc})
		Expect(err).NotTo(HaveOccurred())
		Expect(entries(plan)).To(Equal([]string{`module ghost: target "bogus"`, `module ghost: artifact "x.md" on target "bogus"`,
			`module kit: artifact "../y.md" on target "claude-code"`}))
		Expect(plan.ForgottenModules).To(Equal([]string{"ghost"}))
		res, err := e.ApplyRepair(plan)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.ForgottenModules).To(Equal([]string{"ghost"}))
		Expect(res.Notices).To(Equal(syncNotice))
		led, err := e.Ledger(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Modules).NotTo(HaveKey("ghost"))
		Expect(led.Modules["kit"].Targets).To(Equal([]string{"claude-code"}), "forgetting a path does not uninstall kit from its target")
	})

	It("names no next step when it forgot only entries on a target lola does not know", func() {
		edit(func(l *ledger.Ledger) {
			k := l.Modules["kit"]
			k.Targets = append(k.Targets, "bogus")
			k.Artifacts = append(k.Artifacts, ledger.Artifact{Target: "bogus", Kind: "file", Path: "x.md"})
		})
		plan, err := e.PlanRepair(engine.RepairRequest{Scope: sc})
		Expect(err).NotTo(HaveOccurred())
		res, err := e.ApplyRepair(plan)
		Expect(err).NotTo(HaveOccurred())
		Expect(entries(res)).To(HaveLen(2))
		Expect(res.Notices).To(BeEmpty(), "there is nothing lola could put back on a target it does not know")
	})

	It("says there is nothing to repair, writing nothing, for a good lock.json and for none", func() {
		old := time.Now().Add(-time.Hour).Truncate(time.Second)
		Expect(os.Chtimes(lp, old, old)).To(Succeed())
		plan, err := e.PlanRepair(engine.RepairRequest{Scope: sc})
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Forgotten).To(BeEmpty())
		res, err := e.ApplyRepair(plan)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Forgotten).To(BeEmpty())
		Expect(res.ForgottenModules).To(BeEmpty())
		Expect(res.Notices).To(BeEmpty())
		fi, err := os.Stat(lp)
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.ModTime()).To(BeTemporally("==", old), "lock.json was not written")

		plan, err = e.PlanRepair(engine.RepairRequest{Scope: user})
		Expect(err).NotTo(HaveOccurred())
		res, err = e.ApplyRepair(plan)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Forgotten).To(BeEmpty())
		Expect(p.UserLedger).NotTo(BeAnExistingFile(), "no lock.json is created")
	})

	It("keeps the refusal of a defect it cannot repair, even beside one it can, and writes nothing", func() {
		edit(func(l *ledger.Ledger) {
			k := l.Modules["kit"]
			k.Artifacts = append(k.Artifacts, ledger.Artifact{Target: "claude-code", Kind: "file", Path: "../outside.md"})
			k.From = "nope"
		})
		before := read()
		for _, run := range []func() error{
			func() error { _, err := e.PlanRepair(engine.RepairRequest{Scope: sc}); return err },
			func() error { _, err := e.Ledger(sc); return err },
		} {
			err := run()
			Expect(err).To(MatchError(lp + `: module kit: from "nope" is not a profile lola records`))
			Expect(errs.Hints(err)).To(Equal([]string{"restore lock.json from a backup, or remove the module's entry from it and reinstall the module"}))
		}
		Expect(read()).To(Equal(before))
	})

	It("forgets only what it listed: an entry added since its plan refuses the run, which writes nothing", func() {
		edit(func(l *ledger.Ledger) {
			l.Created = append(l.Created, ledger.FileRef{Target: "claude-code", Path: "../a.json"})
		})
		plan, err := e.PlanRepair(engine.RepairRequest{Scope: sc, Display: []string{"--json"}})
		Expect(err).NotTo(HaveOccurred())
		edit(func(l *ledger.Ledger) {
			l.Created = append(l.Created, ledger.FileRef{Target: "claude-code", Path: "../b.json"})
		})
		before := read()
		_, err = e.ApplyRepair(plan)
		Expect(err).To(MatchError(lp + ` changed since lola listed what to forget: created "../b.json" on target "claude-code" is new`))
		Expect(errs.Hints(err)).To(Equal([]string{"lola repair --project --json"}))
		Expect(read()).To(Equal(before))
	})

	It("does not report an entry another run forgot since its plan", func() {
		edit(func(l *ledger.Ledger) {
			l.Created = append(l.Created, ledger.FileRef{Target: "claude-code", Path: "../a.json"})
		})
		plan, err := e.PlanRepair(engine.RepairRequest{Scope: sc})
		Expect(err).NotTo(HaveOccurred())
		other, err := e.PlanRepair(engine.RepairRequest{Scope: sc})
		Expect(err).NotTo(HaveOccurred())
		_, err = e.ApplyRepair(other)
		Expect(err).NotTo(HaveOccurred())
		res, err := e.ApplyRepair(plan)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Forgotten).To(BeEmpty())
	})

	It("waits for a run holding the scope's lock, then forgets what it listed", func() {
		l := ledger.New()
		l.Created = []ledger.FileRef{{Target: "claude-code", Path: "../c.json"}}
		Expect(l.Save(p.UserLedger)).To(Succeed())
		plan, err := e.PlanRepair(engine.RepairRequest{Scope: user})
		Expect(err).NotTo(HaveOccurred())
		Expect(entries(plan)).To(Equal([]string{`created "../c.json" on target "claude-code"`}))
		held := flock.New(p.UserLock)
		Expect(held.Lock()).To(Succeed())
		done := make(chan error, 1)
		go func() {
			_, err := e.ApplyRepair(plan)
			done <- err
		}()
		Consistently(done, "300ms").ShouldNot(Receive(), "another run holds the scope's lock")
		Expect(held.Unlock()).To(Succeed())
		Eventually(done, "10s").Should(Receive(BeNil()))
		back, err := ledger.Load(p.UserLedger)
		Expect(err).NotTo(HaveOccurred())
		Expect(back.Created).To(BeEmpty())
	})
})
