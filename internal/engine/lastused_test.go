package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// Plan 9 §6.3 (A14): which commands record a use of a cached version in index.yml's last_used.
var _ = Describe("last_used (Plan 9 §6.3)", func() {
	var (
		e        *engine.Engine
		p        paths.Paths
		tmp, src string
		sc       engine.Scope
		day      time.Time
		ctx      = context.Background()
		claude   = engine.Options{Targets: []string{"claude-code"}}
		user     = engine.Scope{Kind: target.User}
		archive  source.Spec
	)
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		day = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
		e.Now = func() time.Time { return day }
		// An archive, which install pins, so render and sync read the version lock.json records.
		src = filepath.Join(tmp, "kit.tar.gz")
		Expect(os.WriteFile(src, tarGz("kit", map[string]string{"lola.yml": "name: kit\n", "commands/hi.md": "Hi\n"}), 0o644)).To(Succeed())
		archive = source.Spec{Kind: source.KindArchive, Location: src}
		proj := filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		sc = engine.Scope{Kind: target.Project, ProjectDir: proj}
		_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: src}}, claude)
		Expect(err).NotTo(HaveOccurred())
		writeTree(p.UserProfiles, map[string]string{"kits.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + src + "}\n"})
	})
	// lastUsed is kit's only version's last_used day.
	lastUsed := func() string {
		GinkgoHelper()
		o, found, err := e.Store.Origin("kit")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(o.Heads).To(HaveLen(1))
		return o.LastUsed[o.Heads[0].Digest]
	}
	ok := func(_ any, err error) {
		GinkgoHelper()
		Expect(err).NotTo(HaveOccurred())
	}
	info := func(arg string) engine.InfoRequest {
		return engine.InfoRequest{Cwd: tmp, Arg: arg, Scope: engine.ScopeRequest{Cwd: tmp, User: true}, Targets: claude.Targets,
			Install: func(string, string, string, engine.TrustKinds) string { return "" }}
	}
	profile := func(action engine.ProfileAction) {
		GinkgoHelper()
		ok(e.Sync(ctx, engine.SyncRequest{Scope: user, Profiles: &engine.ProfileChange{Action: action, Names: []string{"kits"}},
			ConfirmRemovals: func([]engine.Removal) (bool, error) { return true, nil }}))
	}
	// step is one command of a row, run on the next fake day.
	type step func()

	DescribeTable("the commands that write last_used, and those that do not",
		func(writes bool, steps ...step) {
			Expect(lastUsed()).To(Equal("2026-10-01"), "the install's fetch")
			for _, run := range steps {
				day = day.AddDate(0, 0, 1)
				run()
				if writes {
					Expect(lastUsed()).To(Equal(day.Format("2006-01-02")))
				} else {
					Expect(lastUsed()).To(Equal("2026-10-01"), "no use recorded on "+day.Format("2006-01-02"))
				}
			}
		},
		Entry("Store.Commit: every fetch committed (cache add, info of a source, install of a source, cache update, sync --update, render's re-fetch)", true,
			step(func() { ok(e.Fetch(ctx, archive)) }),
			step(func() { ok(e.Info(ctx, info(src))) }),
			step(func() { ok(e.Install(ctx, sc, tmp, []engine.InstallRequest{{Arg: src}}, claude)) }),
			step(func() { ok(e.Update(ctx, nil)) }),
			step(func() {
				ok(e.Sync(ctx, engine.SyncRequest{Scope: sc, Update: true,
					Trust: func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }}))
			}),
			step(func() {
				Expect(e.Store.Remove("kit")).To(BeTrue())
				ok(e.Render(ctx, sc, engine.Options{}))
			})),
		Entry("install of a cached name or a source whose head it reads", true,
			step(func() { ok(e.Install(ctx, sc, tmp, []engine.InstallRequest{{Arg: "kit"}}, claude)) })),
		Entry("Store.Pinned: render, sync and profile --on/--switch reading the recorded version", true,
			step(func() { ok(e.Render(ctx, sc, engine.Options{})) }),
			step(func() {
				Expect(os.RemoveAll(filepath.Join(sc.ProjectDir, ".claude"))).To(Succeed())
				ok(e.Sync(ctx, engine.SyncRequest{Scope: sc}))
			}),
			step(func() { profile(engine.ProfileOn) }),
			step(func() {
				profile(engine.ProfileOff)
				day = day.AddDate(0, 0, 1)
				profile(engine.ProfileSwitch)
			})),
		Entry("none: info and install -n of a cached name, cache list, check, list and cache clean -n", false,
			step(func() { ok(e.Info(ctx, info("kit"))) }),
			step(func() {
				ok(e.Install(ctx, sc, tmp, []engine.InstallRequest{{Arg: "kit"}}, engine.Options{Targets: claude.Targets, DryRun: true}))
			}),
			step(func() {
				_, _, err := e.CachedModules()
				Expect(err).NotTo(HaveOccurred())
			}),
			step(func() { ok(e.Check(ctx, sc, tmp, []string{"kit"}, nil, nil)) }),
			step(func() {
				_, _, err := e.Status(ctx, sc, nil)
				Expect(err).NotTo(HaveOccurred())
			}),
			step(func() { ok(e.PlanClean(engine.CleanRequest{UnusedFor: "1d"})) })),
	)

	It("a use lola cannot record never fails the read: render and install go on as before", func() {
		if os.Geteuid() == 0 {
			Skip("root writes in a read-only directory")
		}
		mod := filepath.Join(p.Store, "kit")
		Expect(os.Chmod(mod, 0o500)).To(Succeed())
		DeferCleanup(os.Chmod, mod, os.FileMode(0o700))
		day = day.AddDate(0, 0, 1)
		ok(e.Render(ctx, sc, engine.Options{}))
		ok(e.Install(ctx, sc, tmp, []engine.InstallRequest{{Arg: "kit"}}, claude))
		Expect(lastUsed()).To(Equal("2026-10-01"), "index.yml could not be written")
	})
})
