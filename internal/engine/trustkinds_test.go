package engine_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

func ptr(k engine.TrustKinds) *engine.TrustKinds { return &k }

// answer is a receipt that answers ok, or err.
func answer(ok bool, err error) func([]engine.Consent) (bool, error) {
	return func([]engine.Consent) (bool, error) { return ok, err }
}

// askYes is a terminal's hook question answered yes for the modules named, and No for the rest.
func askYes(modules ...string) func(string, []engine.HookScript) (bool, error) {
	return func(m string, _ []engine.HookScript) (bool, error) { return slices.Contains(modules, m), nil }
}

var _ = Describe("typed trust (Plan 9 §3)", func() {
	var (
		tmp, proj, log string
		p              paths.Paths
		e              *engine.Engine
		sc             engine.Scope
		ctx            = context.Background()
	)
	// script appends the hook's event to the log, so a spec sees whether it ran.
	const script = "#!/bin/sh\necho \"$LOLA_HOOK $LOLA_MODULE_NAME\" >> \"$HOOK_LOG\"\n"
	// kit writes the module kit into the project at mods/kit, its hooks as yaml declares them.
	kit := func(hooks string) {
		GinkgoHelper()
		files := map[string]string{"mods/kit/lola.yml": "name: kit\n", "mods/kit/commands/kit.md": "Kit\n",
			"mods/kit/hooks/pre.sh": script, "mods/kit/hooks/post.sh": script}
		if hooks != "" {
			files["mods/kit/lola.yml"] = "name: kit\nhooks: " + hooks + "\n"
		}
		writeTree(proj, files)
		for _, s := range []string{"pre.sh", "post.sh"} {
			Expect(os.Chmod(filepath.Join(proj, "mods", "kit", "hooks", s), 0o755)).To(Succeed())
		}
	}
	// deploy writes the plugin deploy into the project at mods/deploy, with a runtime hook that runs
	// command.
	deploy := func(command string) {
		GinkgoHelper()
		writeTree(proj, map[string]string{"mods/deploy/.claude-plugin/plugin.json": `{"name":"deploy"}`, "mods/deploy/commands/ship.md": "Ship\n",
			"mods/deploy/hooks/hooks.json": `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"` + command + `"}]}]}}`})
	}
	// pull writes project.yml behind lola's back, as a git pull would: its entries are untrusted.
	pull := func(modules ...string) {
		GinkgoHelper()
		content := "version: 1\ntargets: [claude-code]\nmodules:\n"
		for _, m := range modules {
			content += "  - {name: " + m + ", source: ./mods/" + m + "}\n"
		}
		writeTree(proj, map[string]string{".lola/project.yml": content})
	}
	ran := func() []string {
		GinkgoHelper()
		b, err := os.ReadFile(log)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		Expect(err).NotTo(HaveOccurred())
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
	consented := func() string {
		GinkgoHelper()
		b, err := os.ReadFile(p.Consent)
		if errors.Is(err, os.ErrNotExist) {
			return ""
		}
		Expect(err).NotTo(HaveOccurred())
		return string(b)
	}
	trusts := func(module string) bool {
		GinkgoHelper()
		r, err := e.Trust.Load(proj)
		Expect(err).NotTo(HaveOccurred())
		return r.View.Find(module) != nil
	}
	receipt := func(answer bool, got *[]engine.Consent) func([]engine.Consent) (bool, error) {
		return func(cs []engine.Consent) (bool, error) {
			*got = append(*got, cs...)
			return answer, nil
		}
	}
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		proj, log = filepath.Join(tmp, "proj"), filepath.Join(tmp, "hooks.log")
		e.Getwd = func() (string, error) { return proj, nil }
		e.Environ = func() []string { return append(os.Environ(), "HOOK_LOG="+log) }
		sc = engine.Scope{Kind: target.Project, ProjectDir: proj}
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
	})

	It("--trust with no value means entries and profiles, and covers no hook, script or runtime hook", func() {
		k, err := engine.ParseTrust("sync", []string{engine.TrustBareValue})
		Expect(err).NotTo(HaveOccurred())
		Expect(k).To(Equal(engine.TrustEntries | engine.TrustProfiles))
		Expect(k.Covers(engine.TrustHooks) || k.Covers(engine.TrustScripts) || k.Covers(engine.TrustRuntimeHooks)).To(BeFalse())

		kit("{pre-install: hooks/pre.sh}")
		pull("kit")
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: k}})
		Expect(err).To(MatchError("kit has a pre-install hook, and lola runs hooks only with your consent"))
		Expect(errs.Hints(err)).To(Equal([]string{"review it: lola cache info kit",
			"then consent to it: lola sync --project --trust=entries,profiles,hooks"}))
		Expect(ran()).To(BeNil())
		Expect(trusts("kit")).To(BeFalse())
	})

	DescribeTable("the --trust value is parsed as §3.3 says, a bare --trust told apart by a value no argument can hold",
		func(command string, values []string, want engine.TrustKinds, msg string, fix *engine.TrustKinds) {
			k, err := engine.ParseTrust(command, values)
			if msg == "" {
				Expect(err).NotTo(HaveOccurred())
				Expect(k).To(Equal(want))
				return
			}
			Expect(err).To(MatchError(msg))
			fe, ok := errors.AsType[*engine.TrustFlagError](err)
			Expect(ok).To(BeTrue())
			Expect(fe.HasFix).To(Equal(fix != nil))
			if fix != nil {
				Expect(fe.Fix).To(Equal(*fix))
			}
		},
		Entry("bare --trust is a value no typed --trust= gives", "sync", []string{engine.TrustBareValue}, engine.TrustBare, "", nil),
		Entry("--trust=entries,profiles written on profile --add is refused (A21)", "profile --add", []string{"entries,profiles"}, engine.TrustKinds(0),
			"lola profile --add never asks for entries trust; it takes --trust=profiles", ptr(engine.TrustProfiles)),
		Entry("bare --trust beside a kind install takes is still refused", "install", []string{engine.TrustBareValue, "hooks"}, engine.TrustKinds(0),
			"lola install never asks for entries or profiles trust; it takes --trust=scripts,hooks,runtime-hooks", ptr(engine.TrustHooks)),
		Entry("three refused kinds read as a list", "profile --add", []string{"entries,scripts,hooks"}, engine.TrustKinds(0),
			"lola profile --add never asks for entries, scripts or hooks trust; it takes --trust=profiles", ptr(engine.TrustKinds(0))),
		Entry("a trailing comma hints the value without it", "sync", []string{"hooks,"}, engine.TrustKinds(0),
			`--trust: "" is not a kind of trust; kinds are entries, profiles, scripts, hooks, runtime-hooks, all`, ptr(engine.TrustHooks)),
		Entry("--trust= hints bare --trust only where the command takes entries", "profile --add", []string{""}, engine.TrustKinds(0),
			"--trust= names no kind; kinds are entries, profiles, scripts, hooks, runtime-hooks, all", nil),
	)

	DescribeTable("an argument after a bare --trust is a kind when every part of it names one, in any case",
		func(arg string, want engine.TrustKinds, is bool) {
			k, ok := engine.TrustArgument("sync", arg)
			Expect(ok).To(Equal(is))
			Expect(k).To(Equal(want))
		},
		Entry("a kind", "hooks", engine.TrustHooks, true),
		Entry("in another case", "Hooks", engine.TrustHooks, true),
		Entry("a list", "hooks,scripts", engine.TrustHooks|engine.TrustScripts, true),
		Entry("all", "ALL", engine.TrustAll, true),
		Entry("a module", "kit", engine.TrustKinds(0), false),
		Entry("a list with a module", "hooks,kit", engine.TrustKinds(0), false),
	)

	DescribeTable("--trust=all covers exactly the kinds the command takes, and no hint ever writes all",
		func(command string, want engine.TrustKinds, arg string) {
			k, err := engine.ParseTrust(command, []string{"all"})
			Expect(err).NotTo(HaveOccurred())
			Expect(k).To(Equal(want))
			Expect(k.Arg()).To(Equal(arg))
			for sub := range engine.TrustAll + 1 {
				Expect(sub.Arg()).NotTo(ContainSubstring("all"), "a hint names exact kinds")
			}
		},
		Entry("sync", "sync", engine.TrustAll, "--trust=entries,profiles,scripts,hooks,runtime-hooks"),
		Entry("profile", "profile", engine.TrustAll, "--trust=entries,profiles,scripts,hooks,runtime-hooks"),
		Entry("profile --add", "profile --add", engine.TrustProfiles, "--trust=profiles"),
		Entry("install", "install", engine.TrustScripts|engine.TrustHooks|engine.TrustRuntimeHooks, "--trust=scripts,hooks,runtime-hooks"),
		Entry("render", "render", engine.TrustScripts|engine.TrustHooks|engine.TrustRuntimeHooks, "--trust=scripts,hooks,runtime-hooks"),
		Entry("migrate", "migrate", engine.TrustScripts|engine.TrustHooks|engine.TrustRuntimeHooks, "--trust=scripts,hooks,runtime-hooks"),
	)

	DescribeTable("off a terminal an uncovered run refuses, hinting the run with exactly the missing kinds and its own flags",
		func(setup func() error, msg string, hints []string) {
			err := setup()
			Expect(err).To(MatchError(ContainSubstring(msg)))
			Expect(errs.Hints(err)).To(Equal(hints))
			Expect(consented()).To(BeEmpty())
		},
		Entry("entries", func() error {
			kit("")
			pull("kit")
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Display: []string{"--json"}}, Display: []string{"--json"}})
			return err
		}, "lola sync needs your trust for .lola/project.yml, which has not been trusted on this machine, and there is no terminal to ask on",
			[]string{"see them: lola sync --project --json -n", "then accept them: lola sync --project --trust=entries --json"}),
		Entry("profiles", func() error {
			kit("")
			writeTree(proj, map[string]string{".lola/profiles/go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: ../../mods/kit}\n"})
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOn, Names: []string{"project:go"}},
				Options: engine.Options{Trust: engine.TrustEntries}})
			return err
		}, "lola profile needs your trust for", []string{"see them: lola profile --project project:go --on --trust=entries -n",
			"then accept them: lola profile --project project:go --on --trust=entries,profiles"}),
		Entry("hooks", func() error {
			kit("{pre-install: hooks/pre.sh}")
			pull("kit")
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: engine.TrustEntries, Display: []string{"--plain"}},
				Display: []string{"--plain"}})
			return err
		}, "kit has a pre-install hook, and lola runs hooks only with your consent",
			[]string{"review it: lola cache info kit", "then consent to it: lola sync --project --trust=entries,hooks --plain"}),
		Entry("entries+hooks", func() error {
			kit("{pre-install: hooks/pre.sh}")
			pull("kit")
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc})
			return err
		}, "lola sync needs your trust for .lola/project.yml",
			[]string{"see them: lola sync --project -n", "then accept them: lola sync --project --trust=entries,hooks"}),
		Entry("remote --add", func() error {
			_, err := e.AddProfile(ctx, engine.ProfileAddRequest{Name: "go", Source: "https://example.com/profiles.git", Rerun: []string{"--json"}})
			return err
		}, "lola profile --add shows the profile and asks before adding it, and there is no terminal to ask on",
			[]string{"see it: lola profile go --add https://example.com/profiles.git -n --json",
				"then add it unasked: lola profile go --add https://example.com/profiles.git --trust=profiles --json"}),
	)

	It("a refusal for removals alone adds the hook kind the next run would be refused without", func() {
		kit("{pre-install: hooks/pre.sh}")
		user := engine.Scope{Kind: target.User}
		writeTree(tmp, map[string]string{"src/plain/lola.yml": "name: plain\n", "src/plain/commands/plain.md": "Plain\n"})
		_, err := e.Install(ctx, user, proj, []engine.InstallRequest{{Arg: filepath.Join(tmp, "src", "plain")}}, engine.Options{Targets: []string{"claude-code"}})
		Expect(err).NotTo(HaveOccurred())
		writeTree(filepath.Dir(p.UserManifest), map[string]string{filepath.Base(p.UserManifest): "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " +
			filepath.Join(proj, "mods", "kit") + "}\n"})
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: user})
		Expect(err).To(MatchError("lola sync needs your confirmation to remove plain, and there is no terminal to ask on"))
		Expect(errs.Hints(err)).To(Equal([]string{"see them: lola sync --user -n", "then confirm them: lola sync --user --trust=hooks -y"}))
	})

	It("a No to the removals records no trust --trust gave, and keeps what a terminal answer accepted", func() {
		kit("")
		writeTree(proj, map[string]string{"mods/plain/lola.yml": "name: plain\n", "mods/plain/commands/plain.md": "Plain\n"})
		_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: filepath.Join(proj, "mods", "plain")}}, engine.Options{Targets: []string{"claude-code"}})
		Expect(err).NotTo(HaveOccurred())
		pull("kit")
		no := func([]engine.Removal) (bool, error) { return false, nil }
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, ConfirmRemovals: no, Options: engine.Options{Trust: engine.TrustEntries}})
		Expect(errors.Is(err, engine.ErrDeclined)).To(BeTrue(), "%v", err)
		Expect(trusts("kit")).To(BeFalse(), "the flag's grant waits for the receipt, which a No never reaches")

		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, ConfirmRemovals: no,
			Trust: func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }})
		Expect(errors.Is(err, engine.ErrDeclined)).To(BeTrue(), "%v", err)
		Expect(trusts("kit")).To(BeTrue(), "a terminal answer is recorded on a No to the removals (Plan 6a X6)")
	})

	It("-y and --force grant no kind of trust", func() {
		kit("{pre-install: hooks/pre.sh}")
		pull("kit")
		yes := func([]engine.Removal) (bool, error) { return true, nil }
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, ConfirmRemovals: yes, Options: engine.Options{Force: true}})
		Expect(err).To(MatchError(ContainSubstring("lola sync needs your trust for .lola/project.yml")))
		Expect(err).To(MatchError(ContainSubstring("-y confirms removals only; accept them with --trust=entries,hooks")))

		_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: filepath.Join(proj, "mods", "kit")}},
			engine.Options{Targets: []string{"claude-code"}, Force: true})
		Expect(err).To(MatchError("kit has a pre-install hook, and lola runs hooks only with your consent"))
		Expect(ran()).To(BeNil())
		Expect(consented()).To(BeEmpty())
	})

	It("a post-install hook or runtime hook not covered is declined with a warning naming --trust=<kind>", func() {
		kit("{post-install: hooks/post.sh}")
		deploy("true")
		pull("kit", "deploy")
		res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: engine.TrustEntries}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(HaveField("Text", "kit: post-install hook skipped without your consent; "+
			"review and run it: lola sync --project kit, or consent without asking: lola sync --project kit --trust=hooks")))
		Expect(res.Findings).To(ContainElement(And(HaveField("Code", "claude-code/runtime-hooks-declined"),
			HaveField("Hint", "review the hooks listed above, then consent to them: lola sync --project --trust=entries,runtime-hooks"))))
		Expect(ran()).To(BeNil())
	})

	It("a terminal yes in a batch's collect pass is the consent its apply pass runs on, recorded then", func() {
		kit("{pre-install: hooks/pre.sh}")
		deploy("true")
		pull("kit", "deploy")
		asked := 0
		collect := func(batch *engine.TrustBatch) {
			GinkgoHelper()
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: engine.TrustEntries, Batch: batch,
				Ask:             func(string, []engine.HookScript) (bool, error) { asked++; return true, nil },
				AskRuntimeHooks: func(string, []engine.RuntimeHook) (bool, error) { asked++; return true, nil }}})
			Expect(err).To(MatchError(engine.ErrBatched))
		}
		batch := &engine.TrustBatch{}
		collect(batch)
		Expect(asked).To(Equal(2), "the hook and the runtime hooks, each asked once")
		Expect(consented()).To(BeEmpty(), "nothing is recorded before the command's receipt")

		// The apply pass asks nothing: the batch holds the yes answers, by key and digest.
		res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: engine.TrustEntries, Expect: batch}})
		Expect(err).NotTo(HaveOccurred())
		Expect(ran()).To(Equal([]string{"pre-install kit"}))
		Expect(res.Findings).NotTo(ContainElement(HaveField("Code", "claude-code/runtime-hooks-declined")))
		Expect(consented()).To(And(ContainSubstring(`"kit"`), ContainSubstring("deploy/runtime-hooks/")), "recorded just before the apply")

		// A hook changed between the passes is not the one answered: it is refused as without consent.
		kit("{pre-install: hooks/pre.sh}")
		writeTree(proj, map[string]string{"mods/kit/hooks/pre.sh": script + "true\n"})
		batch = &engine.TrustBatch{}
		collect(batch)
		writeTree(proj, map[string]string{"mods/kit/hooks/pre.sh": script + "true # changed after the answer\n"})
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: engine.TrustEntries, Expect: batch}})
		Expect(err).To(MatchError("kit has a pre-install hook, and lola runs hooks only with your consent"))
		Expect(ran()).To(Equal([]string{"pre-install kit"}), "it did not run again")
	})

	It("a dry run's command adds exactly the trust kinds the run needs", func() {
		kit("{pre-install: hooks/pre.sh}")
		pull("kit")
		for _, given := range []engine.TrustKinds{0, engine.TrustEntries, engine.TrustHooks, engine.TrustRuntimeHooks} {
			res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{DryRun: true, Trust: given}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.OffTerminal).To(Equal("lola sync --project " + (given | engine.TrustEntries | engine.TrustHooks).Arg()))
			Expect(res.EquivalentCommands).To(Equal([]string{res.OffTerminal}))
		}
		Expect(consented()).To(BeEmpty())
	})

	DescribeTable("consent given by --trust is recorded as a yes at the prompt is, for every kind, and a changed digest or view asks again",
		func(given engine.TrustKinds, setup func(), change func(), asked func(*int) engine.SyncRequest) {
			setup()
			res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: given}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Trusted).NotTo(BeEmpty())

			n := 0
			_, err = e.Sync(ctx, asked(&n))
			Expect(err).NotTo(HaveOccurred())
			Expect(n).To(BeZero(), "the grant is recorded: nothing asks again")

			change()
			_, err = e.Sync(ctx, asked(&n))
			Expect(err).NotTo(HaveOccurred())
			Expect(n).To(Equal(1), "a changed digest or view asks again")
		},
		Entry("entries", engine.TrustEntries, func() { kit(""); pull("kit") }, func() {
			writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: ./mods/kit, rules: false}\n"})
		}, func(n *int) engine.SyncRequest {
			return engine.SyncRequest{Scope: sc, Trust: func(engine.TrustReport) (engine.TrustAnswer, error) { *n++; return engine.TrustAnswer{All: true}, nil }}
		}),
		Entry("profiles", engine.TrustEntries|engine.TrustProfiles, func() {
			kit("")
			writeTree(proj, map[string]string{".lola/profiles/go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: ../../mods/kit}\n"})
			led := &ledger.Ledger{Version: ledger.Version, Modules: map[string]*ledger.Entry{}, Profiles: []ledger.Active{{Name: "project:go", By: ledger.ByOn}}}
			Expect(led.Save(filepath.Join(proj, ".lola", "lock.json"))).To(Succeed())
		}, func() {
			writeTree(proj, map[string]string{".lola/profiles/go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: ../../mods/kit, rules: false}\n"})
		}, func(n *int) engine.SyncRequest {
			return engine.SyncRequest{Scope: sc, Trust: func(engine.TrustReport) (engine.TrustAnswer, error) { *n++; return engine.TrustAnswer{All: true}, nil }}
		}),
		Entry("hooks", engine.TrustEntries|engine.TrustHooks, func() { kit("{pre-install: hooks/pre.sh}"); pull("kit") }, func() {
			writeTree(proj, map[string]string{"mods/kit/hooks/pre.sh": script + "true\n"})
		}, func(n *int) engine.SyncRequest {
			return engine.SyncRequest{Scope: sc, Trust: func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil },
				Options: engine.Options{Ask: func(string, []engine.HookScript) (bool, error) { *n++; return true, nil }}}
		}),
		Entry("runtime-hooks", engine.TrustEntries|engine.TrustRuntimeHooks, func() {
			deploy("true")
			pull("deploy")
		}, func() { deploy("false") }, func(n *int) engine.SyncRequest {
			return engine.SyncRequest{Scope: sc, Trust: func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil },
				Options: engine.Options{AskRuntimeHooks: func(string, []engine.RuntimeHook) (bool, error) { *n++; return true, nil }}}
		}),
	)

	It("a dry run with --trust records no consent", func() {
		kit("{pre-install: hooks/pre.sh}")
		pull("kit")
		res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{DryRun: true, Trust: engine.TrustEntries | engine.TrustHooks}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Trusted).To(ConsistOf(engine.Consent{Kind: "entries", Item: "targets", Change: engine.ConsentNew},
			engine.Consent{Kind: "entries", Item: "kit", Change: engine.ConsentNew},
			engine.Consent{Kind: "hooks", Item: "kit pre-install", Change: engine.ConsentNew}), "trusted lists what the flags would cover")
		Expect(consented()).To(BeEmpty())
		Expect(filepath.Join(proj, ".lola", "trust.json")).NotTo(BeAnExistingFile())
		Expect(ran()).To(BeNil())
	})

	DescribeTable("a receipt answered No, a refusal or an abort records no consent given by --trust",
		func(opts engine.Options, want any) {
			kit("{pre-install: hooks/pre.sh}")
			writeTree(proj, map[string]string{"mods/kit2/lola.yml": "name: kit2\nhooks: {pre-install: hooks/pre.sh}\n", "mods/kit2/commands/kit2.md": "Kit2\n",
				"mods/kit2/hooks/pre.sh": script})
			Expect(os.Chmod(filepath.Join(proj, "mods", "kit2", "hooks", "pre.sh"), 0o755)).To(Succeed())
			pull("kit", "kit2")
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: opts})
			Expect(err).To(MatchError(want))
			Expect(consented()).To(BeEmpty())
			Expect(trusts("kit")).To(BeFalse())
			Expect(ran()).To(BeNil())
			Expect(filepath.Join(proj, ".claude")).NotTo(BeADirectory())
		},
		Entry("No", engine.Options{Trust: engine.TrustEntries | engine.TrustHooks, Receipt: answer(false, nil)}, engine.ErrDeclined),
		Entry("a refusal for another kind", engine.Options{Trust: engine.TrustHooks, Receipt: answer(true, nil)},
			"lola sync needs your trust for .lola/project.yml, which has not been trusted on this machine, and there is no terminal to ask on"),
		Entry("Ctrl+C", engine.Options{Trust: engine.TrustEntries | engine.TrustHooks, Receipt: answer(false, context.Canceled)}, context.Canceled),
		Entry("a terminal yes, then No at the receipt", engine.Options{Trust: engine.TrustEntries, Ask: askYes("kit", "kit2"),
			Receipt: answer(false, nil)}, engine.ErrDeclined),
		Entry("a terminal yes, then Ctrl+C at the receipt", engine.Options{Trust: engine.TrustEntries, Ask: askYes("kit", "kit2"),
			Receipt: answer(false, context.Canceled)}, context.Canceled),
		Entry("a terminal yes, then a refusal of another module's hook", engine.Options{Trust: engine.TrustEntries, Ask: askYes("kit"),
			Receipt: answer(true, nil)}, "kit2 has a pre-install hook, and lola runs hooks only with your consent"),
	)

	It("covered items reach the receipt once, grouped in canonical order, before anything is written", func() {
		kit("{pre-install: hooks/pre.sh}")
		pull("kit")
		var got []engine.Consent
		res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: engine.TrustAll, Receipt: receipt(true, &got)}})
		Expect(err).NotTo(HaveOccurred())
		want := []engine.Consent{{Kind: "entries", Item: "targets", Change: engine.ConsentNew}, {Kind: "entries", Item: "kit", Change: engine.ConsentNew},
			{Kind: "hooks", Item: "kit pre-install", Change: engine.ConsentNew}}
		Expect(got).To(Equal(want))
		Expect(res.Trusted).To(Equal(want))
		Expect(ran()).To(Equal([]string{"pre-install kit"}))
		Expect(trusts("kit")).To(BeTrue())
	})

	Describe("profile --update", func() {
		It("profile --update without a scope flag applies profiles trust only", func() {
			kit("")
			pull("kit")
			dir := filepath.Join(p.RemoteProfiles, "go")
			writeTree(dir, map[string]string{"profile.yml": "version: 1\nmodules: []\n",
				"source.json": `{"version":1,"source":"https://example.com/profiles.git","commit":"0000000000000000000000000000000000000000","digest":"sha256:00"}`})
			_, err := e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Names: []string{"go"}, CannotAsk: true, Trust: engine.TrustAll,
				Rerun: []string{engine.TrustAll.Arg()}})
			Expect(err).To(MatchError(ContainSubstring("project " + proj + " has changes you have not trusted (kit and project.yml's targets), which its sync asks about, and no flag named it")))
			Expect(errs.Hints(err)).To(Equal([]string{
				"name it, and trust them: lola profile --project remote:go --update --trust=entries,profiles,scripts,hooks,runtime-hooks",
				"or with user scope too: lola profile --scope both remote:go --update --trust=entries,profiles,scripts,hooks,runtime-hooks"}))
		})
	})

	DescribeTable("a narrowed profile --update hint drops --trust in any form",
		func(flag string) {
			kit("")
			led := &ledger.Ledger{Version: ledger.Version, Modules: map[string]*ledger.Entry{}}
			Expect(led.Save(filepath.Join(proj, ".lola", "lock.json"))).To(Succeed())
			writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules: []\n"})
			Expect(os.WriteFile(filepath.Join(proj, ".lola", "lock.json"), []byte("{not json"), 0o644)).To(Succeed())
			dir := filepath.Join(p.RemoteProfiles, "go")
			writeTree(dir, map[string]string{"profile.yml": "version: 1\nmodules: []\n",
				"source.json": `{"version":1,"source":"https://example.com/profiles.git","commit":"0000000000000000000000000000000000000000","digest":"sha256:00"}`})
			_, err := e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Names: []string{"go"}, Consent: func([]engine.RemoteProfile) (bool, error) { return true, nil },
				Rerun: []string{flag, "--plain"}, Flags: []string{"-y"}})
			Expect(err).To(HaveOccurred())
			Expect(errs.Hints(err)).To(ContainElement("or update it at user scope only: lola profile --user remote:go --update --plain"))
		},
		Entry("--trust", "--trust"),
		Entry("--trust=hooks", "--trust=hooks"),
		Entry("--trust=entries,profiles", "--trust=entries,profiles"),
	)
})

var _ = Describe("typed trust after the receipt (Plan 9 A22, A23)", func() {
	var (
		tmp, proj string
		p         paths.Paths
		e         *engine.Engine
		sc        engine.Scope
		ctx       = context.Background()
	)
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		proj = filepath.Join(tmp, "proj")
		e.Getwd = func() (string, error) { return proj, nil }
		sc = engine.Scope{Kind: target.Project, ProjectDir: proj}
		writeTree(proj, map[string]string{"mods/kit/lola.yml": "name: kit\nhooks: {pre-install: hooks/pre.sh}\n", "mods/kit/commands/kit.md": "Kit\n",
			"mods/kit/hooks/pre.sh": "#!/bin/sh\nexit 3\n", ".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: ./mods/kit}\n"})
		Expect(os.Chmod(filepath.Join(proj, "mods", "kit", "hooks", "pre.sh"), 0o755)).To(Succeed())
	})

	It("a failure after the grants were written carries them as trusted", func() {
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: engine.TrustEntries | engine.TrustHooks}})
		Expect(err).To(MatchError(ContainSubstring("pre-install hook hooks/pre.sh of kit failed")))
		te, ok := errors.AsType[*engine.TrustedError](err)
		Expect(ok).To(BeTrue())
		Expect(te.JSONFields()).To(HaveKeyWithValue("trusted", []engine.Consent{{Kind: "hooks", Item: "kit pre-install", Change: engine.ConsentNew}}),
			"the grants written, which a refusal or an abort never has")
		Expect(p.Consent).To(BeAnExistingFile())
	})

	It("a batch collects every run's covered items and grants, which each run records only when applied", func() {
		batch := &engine.TrustBatch{}
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: engine.TrustEntries | engine.TrustHooks, Batch: batch}})
		Expect(err).To(MatchError(engine.ErrBatched))
		Expect(batch.Items).To(Equal([]engine.Consent{{Kind: "entries", Item: "targets", Change: engine.ConsentNew},
			{Kind: "entries", Item: "kit", Change: engine.ConsentNew}, {Kind: "hooks", Item: "kit pre-install", Change: engine.ConsentNew}}))
		Expect(p.Consent).NotTo(BeAnExistingFile())
		Expect(filepath.Join(proj, ".claude")).NotTo(BeADirectory())
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: engine.TrustEntries | engine.TrustHooks, Expect: batch}})
		Expect(err).To(MatchError(ContainSubstring("pre-install hook hooks/pre.sh of kit failed")), "the run applied after the receipt")
		Expect(p.Consent).To(BeAnExistingFile(), "it recorded its grants, which the batch held, just before its apply")
	})
})

var _ = Describe("a batch applied after its one receipt (Plan 9 R-9-16)", func() {
	var (
		tmp, proj string
		p         paths.Paths
		e         *engine.Engine
		sc, user  engine.Scope
		ctx       = context.Background()
	)
	hooked := func(dir, body string) {
		writeTree(dir, map[string]string{"lola.yml": "name: kit\nhooks: {pre-install: pre.sh}\n", "commands/kit.md": "Kit\n", "pre.sh": "#!/bin/sh\n" + body + "\n"})
		Expect(os.Chmod(filepath.Join(dir, "pre.sh"), 0o755)).To(Succeed())
	}
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		proj = filepath.Join(tmp, "proj")
		e.Getwd = func() (string, error) { return proj, nil }
		sc, user = engine.Scope{Kind: target.Project, ProjectDir: proj}, engine.Scope{Kind: target.User}
		hooked(filepath.Join(proj, "mods", "kit"), "true")
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: ./mods/kit}\n"})
	})
	run := func(s engine.Scope, opts engine.Options) error {
		opts.Trust = engine.TrustEntries | engine.TrustHooks
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: s, Options: opts})
		return err
	}

	It("refuses a run whose grants moved after the receipt, by key and digest, writing nothing", func() {
		batch := &engine.TrustBatch{}
		Expect(run(sc, engine.Options{Batch: batch})).To(MatchError(engine.ErrBatched))
		hooked(filepath.Join(proj, "mods", "kit"), "true # changed after the receipt")
		err := run(sc, engine.Options{Expect: batch})
		Expect(err).To(MatchError("what --trust covers changed after you answered its receipt; nothing more was written"))
		Expect(p.Consent).NotTo(BeAnExistingFile())
		Expect(filepath.Join(proj, ".claude")).NotTo(BeADirectory())
	})

	It("applies one module at two versions in two scopes, each recording its own digest before its own apply", func() {
		work := filepath.Join(tmp, "kit-work")
		hooked(work, "true # v1")
		gitIn(work, "init", "-q", "-b", "main")
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "v1")
		gitIn(work, "tag", "v1")
		hooked(work, "true # v2")
		gitIn(work, "commit", "-q", "-am", "v2")
		gitIn(work, "tag", "v2")
		gitIn(tmp, "clone", "-q", "--bare", work, filepath.Join(tmp, "srv", "kit.git"))
		url := testenv.ServeGit(filepath.Join(tmp, "srv")) + "/kit.git"
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + url + ", ref: v1}\n"})
		writeTree(filepath.Dir(p.UserManifest), map[string]string{filepath.Base(p.UserManifest): "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " +
			url + ", ref: v2}\n"})
		batch := &engine.TrustBatch{}
		Expect(run(sc, engine.Options{Batch: batch})).To(MatchError(engine.ErrBatched))
		Expect(run(user, engine.Options{Batch: batch})).To(MatchError(engine.ErrBatched))
		Expect(run(sc, engine.Options{Expect: batch})).To(Succeed())
		Expect(run(user, engine.Options{Expect: batch})).To(Succeed())
		Expect(filepath.Join(proj, ".claude", "commands", "kit.md")).To(BeAnExistingFile())
		Expect(filepath.Join(p.Home, ".claude", "commands", "kit.md")).To(BeAnExistingFile())
	})
})
