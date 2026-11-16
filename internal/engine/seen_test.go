package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("what a run has shown before", func() {
	var (
		proj, mod string
		e         *engine.Engine
		sc        engine.Scope
		ctx       = context.Background()
		claude    = engine.Options{Targets: []string{"claude-code"}}
	)
	skill := func(tools string) string {
		return "---\nname: lint\ndescription: Lint\nallowed-tools: " + tools + "\n---\nLint\n"
	}
	BeforeEach(func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p, targets.Builtin())
		proj, mod = filepath.Join(tmp, "proj"), filepath.Join(tmp, "src", "kit")
		writeTree(mod, map[string]string{
			".claude-plugin/plugin.json": `{"name":"kit"}`,
			"skills/lint/SKILL.md":       skill("Bash"),
			"hooks/hooks.json":           `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/stop.sh"}]}]}}`,
		})
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		sc = engine.Scope{Kind: target.Project, ProjectDir: proj}
	})
	install := func(opts engine.Options) *engine.Result {
		GinkgoHelper()
		res, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: mod}}, opts)
		Expect(err).NotTo(HaveOccurred())
		return res
	}
	code := func(res *engine.Result, c string) target.Finding {
		GinkgoHelper()
		for _, f := range res.Findings {
			if f.Code == c {
				return f
			}
		}
		Fail("no finding " + c)
		return target.Finding{}
	}
	recorded := func() []string {
		GinkgoHelper()
		led, err := e.Ledger(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Modules).To(HaveKey("kit"))
		return led.Modules["kit"].Safety
	}

	It("records the state findings a run shows, and the next run of the unchanged module says seen", func() {
		res := install(claude)
		Expect(*code(res, "claude-code/allowed-tools").New).To(BeTrue())
		Expect(recorded()).To(HaveLen(2), "the pre-approved tools and the declined runtime hooks")
		Expect(res.Modules[0].Info).To(And(HavePrefix("lola info"), ContainSubstring("kit")))

		res = install(claude)
		Expect(res.Modules[0].Kept).To(BeTrue())
		Expect(*code(res, "claude-code/allowed-tools").New).To(BeFalse())
	})

	It("reports a changed tool list as new again", func() {
		install(claude)
		before := recorded()
		writeTree(mod, map[string]string{"skills/lint/SKILL.md": skill("Bash, Read")})
		res := install(claude)
		Expect(*code(res, "claude-code/allowed-tools").New).To(BeTrue())
		Expect(recorded()).NotTo(Equal(before))
	})

	It("compares in a dry run and records nothing", func() {
		dry := claude
		dry.DryRun = true
		res := install(dry)
		Expect(*code(res, "claude-code/allowed-tools").New).To(BeTrue())
		Expect(filepath.Join(proj, ".lola", "lock.json")).NotTo(BeAnExistingFile())

		install(claude)
		before := recorded()
		writeTree(mod, map[string]string{"skills/lint/SKILL.md": skill("Read")})
		res = install(dry)
		Expect(*code(res, "claude-code/allowed-tools").New).To(BeTrue())
		Expect(recorded()).To(Equal(before))
	})

	It("records on the first run of a module installed before it was recorded, even in sync", func() {
		install(claude)
		led, err := e.Ledger(sc)
		Expect(err).NotTo(HaveOccurred())
		led.Modules["kit"].Safety = nil
		Expect(led.Save(filepath.Join(proj, ".lola", "lock.json"))).To(Succeed())

		res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: claude})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.InSync).To(BeTrue())
		Expect(*code(res.Result, "claude-code/allowed-tools").New).To(BeTrue())
		Expect(recorded()).To(HaveLen(2), "the pre-approved tools and the declined runtime hooks")

		res, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: claude})
		Expect(err).NotTo(HaveOccurred())
		Expect(*code(res.Result, "claude-code/allowed-tools").New).To(BeFalse())
	})

	It("records on an unchanged reinstall of a module installed before it was recorded", func() {
		install(claude)
		led, err := e.Ledger(sc)
		Expect(err).NotTo(HaveOccurred())
		led.Modules["kit"].Safety = nil
		Expect(led.Save(filepath.Join(proj, ".lola", "lock.json"))).To(Succeed())

		res := install(claude)
		Expect(res.Modules[0].Kept).To(BeTrue())
		Expect(*code(res, "claude-code/allowed-tools").New).To(BeTrue())
		Expect(recorded()).To(HaveLen(2), "the pre-approved tools and the declined runtime hooks")

		res = install(claude)
		Expect(*code(res, "claude-code/allowed-tools").New).To(BeFalse())
	})

	It("records each state finding's key once, sorted", func() {
		writeTree(mod, map[string]string{
			"skills/fmt/SKILL.md":  "---\nname: fmt\ndescription: Fmt\nallowed-tools: Read\n---\nFmt\n",
			"skills/vet/SKILL.md":  "---\nname: vet\ndescription: Vet\nallowed-tools: Grep\n---\nVet\n",
			"skills/lint/SKILL.md": skill("Bash"),
		})
		install(claude)
		keys := recorded()
		Expect(keys).To(HaveLen(4), "three tool lists and the declined runtime hooks")
		Expect(slices.IsSorted(keys)).To(BeTrue())
		Expect(slices.Compact(slices.Clone(keys))).To(Equal(keys))
	})

	It("reports declined runtime hooks as new once, then seen until they change or are granted", func() {
		declined := func(res *engine.Result) (hook, warning bool) {
			GinkgoHelper()
			Expect(res.Modules[0].RuntimeHooks).To(HaveLen(1))
			h := res.Modules[0].RuntimeHooks[0]
			Expect(h.Status).To(Equal(engine.RuntimeHooksDeclined))
			return *h.New, *code(res, "claude-code/runtime-hooks-declined").New
		}
		hook, warning := declined(install(claude))
		Expect([]bool{hook, warning}).To(Equal([]bool{true, true}))
		for range 2 {
			hook, warning = declined(install(claude))
			Expect([]bool{hook, warning}).To(Equal([]bool{false, false}))
		}
		res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: claude})
		Expect(err).NotTo(HaveOccurred())
		hook, warning = declined(res.Result)
		Expect([]bool{hook, warning}).To(Equal([]bool{false, false}))
		Expect(recorded()).To(HaveLen(2))

		writeTree(mod, map[string]string{"hooks/hooks.json": `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/other.sh"}]}]}}`})
		hook, warning = declined(install(claude))
		Expect([]bool{hook, warning}).To(Equal([]bool{true, true}), "a changed hook is new again")

		granted := claude
		granted.Trust = engine.TrustRuntimeHooks
		r := install(granted)
		Expect(r.Modules[0].RuntimeHooks[0].New).To(HaveValue(BeTrue()))
		Expect(recorded()).To(HaveLen(1), "a granted hook is consent's record, not lock.json's")
		r = install(claude)
		Expect(r.Modules[0].RuntimeHooks[0].Status).To(Equal(engine.RuntimeHooksConsented))
		Expect(r.Modules[0].RuntimeHooks[0].New).To(HaveValue(BeFalse()))
	})

	It("compares each scope's findings with that scope's lock.json", func() {
		install(claude)
		user := engine.Scope{Kind: target.User}
		out, err := e.InstallAll(ctx, engine.InstallSpec{Cwd: proj, Modules: []engine.InstallRequest{{Arg: mod}}, Options: claude,
			Scopes: []engine.ScopeInstall{
				{ScopeChoice: engine.ScopeChoice{Scope: sc, Source: engine.SourceFlag}, Targets: claude.Targets},
				{ScopeChoice: engine.ScopeChoice{Scope: user, Source: engine.SourceFlag}, Targets: claude.Targets}}})
		Expect(err).NotTo(HaveOccurred())
		seen := map[string]bool{}
		for _, f := range out.Findings {
			if f.Code == "claude-code/allowed-tools" {
				seen[f.Scope] = !*f.New
			}
		}
		Expect(seen).To(Equal(map[string]bool{"project": true, "user": false}))
	})

	It("leaves new out of lola info", func() {
		install(claude)
		info, err := e.Info(ctx, engine.InfoRequest{Cwd: proj, Arg: mod, Scope: engine.ScopeRequest{Project: true, Cwd: proj}, Targets: claude.Targets,
			Rerun: func(...string) string { return "" }, Install: func(string, string, string, engine.TrustKinds) string { return "" }})
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Install).NotTo(BeEmpty())
		for _, in := range info.Install {
			Expect(in.Findings).NotTo(BeEmpty())
			for _, f := range in.Findings {
				Expect(f.New).To(BeNil())
			}
		}
	})
})
