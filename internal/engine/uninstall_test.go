package engine_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("uninstall from the layer that provides a module", func() {
	var (
		tmp  string
		p    paths.Paths
		e    *engine.Engine
		user engine.Scope
		ctx  = context.Background()
	)
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		e.Getwd = func() (string, error) { return tmp, nil }
		user = engine.Scope{Kind: target.User}
	})
	mod := func(name string) string {
		GinkgoHelper()
		dir := filepath.Join(tmp, "mods", name)
		writeTree(dir, map[string]string{"lola.yml": "name: " + name + "\n", "commands/" + name + ".md": name + "\n"})
		return dir
	}
	on := func(name string) {
		GinkgoHelper()
		led, err := e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		led.Profile = &ledger.Active{Name: name, By: ledger.ByOn}
		Expect(led.Save(p.UserLedger)).To(Succeed())
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: user, ConfirmRemovals: func([]engine.Removal) (bool, error) { return true, nil }})
		Expect(err).NotTo(HaveOccurred())
	}
	read := func(name string) string {
		GinkgoHelper()
		b, err := os.ReadFile(filepath.Join(p.UserProfiles, name))
		Expect(err).NotTo(HaveOccurred())
		return string(b)
	}
	command := func(name string) string { return filepath.Join(p.Home, ".claude", "commands", name+".md") }

	It("removes a module from the profile on, and asks before removing one the base provides", func() {
		a, c := mod("a"), mod("c")
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: c, source: " + c + "}\n",
			"go.yml": "version: 1\nmodules:\n  - {name: a, source: " + a + "}\n"})
		on("user:go")
		_, err := e.Uninstall(ctx, user, []string{"a"}, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(read("go.yml")).To(Equal("version: 1\n"))
		Expect(command("a")).NotTo(BeAnExistingFile())

		_, err = e.Uninstall(ctx, user, []string{"c"}, engine.Options{Force: true})
		Expect(err).To(MatchError("c comes from default (base); lola uninstall needs to know whether to remove it there, and there is no terminal to ask on"))
		Expect(errs.Hints(err)).To(Equal([]string{"lola uninstall --user c --profile base"}))
		Expect(command("c")).To(BeARegularFile())

		var asked []engine.WhereQuestion
		_, err = e.Uninstall(ctx, user, []string{"c"}, engine.Options{AskWhere: func(q engine.WhereQuestion) (engine.WhereChoice, error) {
			asked = append(asked, q)
			return q.Choices[0], nil
		}})
		Expect(err).NotTo(HaveOccurred())
		Expect(asked).To(Equal([]engine.WhereQuestion{{Scope: user, Module: "c", Title: "c comes from default (base), loaded under every profile at user scope.",
			Choices: []engine.WhereChoice{{Label: "Remove it from default (base)", Profile: "base"}}}}))
		Expect(read("default.yml")).To(Equal("version: 1\ntargets: [claude-code]\n"))
		Expect(command("c")).NotTo(BeAnExistingFile())
	})

	It("leaves a module the base also lists, saying so, with the run that removes it there", func() {
		a := mod("a")
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: " + a + "}\n",
			"go.yml": "version: 1\nmodules:\n  - {name: a, source: " + a + ", rules: false}\n"})
		on("user:go")
		res, err := e.Uninstall(ctx, user, []string{"a"}, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(read("go.yml")).To(Equal("version: 1\n"))
		Expect(command("a")).To(BeARegularFile(), "default (base) still lists it")
		Expect(res.Notices).To(ContainElements(
			engine.Notice{Level: engine.LevelInfo, Text: "a stays: default (base) lists it too → also remove it there: lola uninstall --user a --profile base"},
			engine.Notice{Level: engine.LevelInfo, Text: "a: default (base)'s entry applies at the next sync → lola sync --user a"}))
	})

	It("refuses a module a remote profile provides, hinting --off", func() {
		writeRemoteKit(p, "team")
		on("remote:team")
		_, err := e.Uninstall(ctx, user, []string{"kit"}, engine.Options{})
		Expect(err).To(MatchError("kit comes from remote:team, which is read-only; lola does not edit remote profiles"))
		Expect(errs.Hints(err)).To(Equal([]string{"turn it off: lola profile --user --off",
			"or switch to a copy of yours: lola profile --user team-mine --switch --new --from remote:team"}))

		writeTree(p.UserProfiles, map[string]string{"work.yml": "version: 1\nbundle: [remote:team]\n"})
		on("user:work")
		_, err = e.Uninstall(ctx, user, []string{"kit"}, engine.Options{})
		Expect(err).To(MatchError("kit comes from remote:team, which is read-only; lola does not edit remote profiles"))
		Expect(errs.Hints(err)).To(Equal([]string{"turn bundle work off: lola profile --user --off",
			"or take remote:team out of bundle work: lola profile --user work --edit --bundle ''"}), "a bundle's member goes off with its bundle, or out of it")
		_, err = e.EditBundle(ctx, engine.BundleEditRequest{Scope: user, Name: "work", Members: []string{},
			ConfirmRemovals: func([]engine.Removal) (bool, error) { return true, nil }})
		Expect(err).NotTo(HaveOccurred(), "the hint runs as printed")
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "work.yml"))).To(Equal([]byte("version: 1\nbundle: []\n")))
	})

	It("-t edits the targets of the entry in the layer that provides it", func() {
		a := mod("a")
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code, opencode]\nmodules:\n  - {name: a, source: " + a + "}\n"})
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: user})
		Expect(err).NotTo(HaveOccurred())
		_, err = e.Uninstall(ctx, user, []string{"a"}, engine.Options{Targets: []string{"opencode"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(read("default.yml")).To(Equal("version: 1\ntargets: [claude-code, opencode]\nmodules:\n  - {name: a, source: " + a + ", targets: [claude-code]}\n"))
		Expect(command("a")).To(BeARegularFile())
	})

	It("-t keeps the entry, and so the module, when the base lists it too", func() {
		a := mod("a")
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: " + a + "}\n",
			"go.yml": "version: 1\ntargets: [claude-code, opencode]\nmodules:\n  - {name: a, source: " + a + "}\n"})
		on("user:go")
		res, err := e.Uninstall(ctx, user, []string{"a"}, engine.Options{Targets: []string{"opencode"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).NotTo(ContainElement(HaveField("Text", ContainSubstring("stays"))), "the profile on still lists it")
		Expect(read("go.yml")).To(Equal("version: 1\ntargets: [claude-code, opencode]\nmodules:\n  - {name: a, source: " + a + ", targets: [claude-code]}\n"))
		Expect(read("default.yml")).To(ContainSubstring("name: a"), "the base is not touched")
		Expect(command("a")).To(BeARegularFile())
	})

	It("--profile removes the entry from the profile it names, on or not, and refuses one that does not list it", func() {
		a := mod("a")
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\n",
			"go.yml": "version: 1\n", "work.yml": "version: 1\nmodules:\n  - {name: a, source: " + a + "}\n"})
		on("user:go")
		_, err := e.Uninstall(ctx, user, []string{"a"}, engine.Options{Target: engine.TargetRequest{Profile: "base"}})
		Expect(err).To(MatchError(`lola uninstall: "a" is not installed at user scope`), "no layer on lists it, and the base is on")

		res, err := e.Uninstall(ctx, user, []string{"a"}, engine.Options{Target: engine.TargetRequest{Profile: "work"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(read("work.yml")).To(Equal("version: 1\n"), "work is not on: its file is edited, and nothing is installed or removed")
		Expect(res.Modules).To(BeEmpty())
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "saved to profile work; it is not on at user scope → lola profile --user work --switch"}))

		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\nmodules:\n  - {name: a, source: " + a + "}\n"})
		on("user:go")
		_, err = e.Uninstall(ctx, user, []string{"a"}, engine.Options{Target: engine.TargetRequest{Profile: "base"}, Display: []string{"--json"}})
		Expect(err).To(MatchError("default (base) does not list a"))
		Expect(errs.Hints(err)).To(Equal([]string{"see where it comes from: lola list --user --json"}))
		Expect(command("a")).To(BeARegularFile())
	})

	It("--profile naming a profile that is not on edits its file and uninstalls a leftover no layer on lists", func() {
		a := mod("a")
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\n",
			"go.yml": "version: 1\nmodules:\n  - {name: a, source: " + a + "}\n", "work.yml": "version: 1\nmodules:\n  - {name: a, source: " + a + "}\n"})
		on("user:go")
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"}) // a hand edit leaves a installed, wanted by no layer on
		_, err := e.Uninstall(ctx, user, []string{"a"}, engine.Options{Targets: []string{"opencode"}, Target: engine.TargetRequest{Profile: "work"}})
		Expect(err).To(MatchError("a is not installed on opencode, nor does profile work list it for them; it is installed on claude-code; profile work lists it for claude-code"))
		Expect(errs.Hints(err)).To(Equal([]string{"uninstall it there: lola uninstall --user a -t claude-code --profile work"}))
		res, err := e.Uninstall(ctx, user, []string{"a"}, engine.Options{Target: engine.TargetRequest{Profile: "work"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(read("work.yml")).To(Equal("version: 1\n"))
		Expect(command("a")).NotTo(BeAnExistingFile(), "the leftover is uninstalled, as without --profile")
		Expect(res.Modules).To(HaveLen(1))
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "saved to profile work; it is not on at user scope → lola profile --user work --switch"}))
	})

	It("-t with --profile naming a profile that is not on narrows its entry of a module that is not installed", func() {
		b := mod("b")
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\n", "go.yml": "version: 1\n",
			"work.yml": "version: 1\ntargets: [claude-code, opencode]\nmodules:\n  - {name: b, source: " + b + "}\n"})
		on("user:go")
		res, err := e.Uninstall(ctx, user, []string{"b"}, engine.Options{Targets: []string{"opencode"}, Target: engine.TargetRequest{Profile: "work"}, DryRun: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "would be saved to profile work; it is not on at user scope → lola profile --user work --switch"}),
			"a dry run saves nothing")
		Expect(read("work.yml")).To(Equal("version: 1\ntargets: [claude-code, opencode]\nmodules:\n  - {name: b, source: " + b + "}\n"))

		_, err = e.Uninstall(ctx, user, []string{"b"}, engine.Options{Targets: []string{"opencode"}, Target: engine.TargetRequest{Profile: "work"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(read("work.yml")).To(Equal("version: 1\ntargets: [claude-code, opencode]\nmodules:\n  - {name: b, source: " + b + ", targets: [claude-code]}\n"))
	})

	It("-t with --profile naming a layer below the winner narrows that entry's targets and removes nothing", func() {
		a := mod("a")
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code, opencode]\nmodules:\n  - {name: a, source: " + a + "}\n",
			"go.yml": "version: 1\ntargets: [claude-code, opencode]\nmodules:\n  - {name: a, source: " + a + "}\n"})
		on("user:go")
		res, err := e.Uninstall(ctx, user, []string{"a"}, engine.Options{Targets: []string{"opencode"}, Target: engine.TargetRequest{Profile: "base"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Modules).To(BeEmpty(), "profile go still wants a on both")
		led, err := e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Modules["a"].InstalledTargets()).To(ConsistOf("claude-code", "opencode"))
		Expect(read("default.yml")).To(Equal("version: 1\ntargets: [claude-code, opencode]\nmodules:\n  - {name: a, source: " + a + ", targets: [claude-code]}\n"))
		Expect(read("go.yml")).To(ContainSubstring("{name: a, source: "+a+"}\n"), "the winner is not touched")
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn, Text: "a: profile go's entry wins; this change to default (base) applies when it is off"}))

		_, err = e.Uninstall(ctx, user, []string{"a"}, engine.Options{Targets: []string{"opencode"}, Target: engine.TargetRequest{Profile: "base"}})
		Expect(err).To(MatchError("default (base) lists a for claude-code, not for opencode"), "-t is judged by the entry it edits")
		Expect(errs.Hints(err)).To(Equal([]string{"uninstall it there: lola uninstall --user a -t claude-code --profile base"}))
	})

	It("-t says where a module is listed but not installed, never that it is installed there", func() {
		zed := mod("zed")
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: zed, source: " + zed + "}\n"})
		for _, t := range []string{"opencode", "claude-code"} {
			_, err := e.Uninstall(ctx, user, []string{"zed"}, engine.Options{Targets: []string{t}})
			Expect(err).To(MatchError("zed is not installed on "+t+"; it is listed for claude-code but not installed there"), "a hand-added entry never synced")
			Expect(errs.Hints(err)).To(Equal([]string{"remove its entry instead: lola uninstall --user zed"}))
		}
		res, err := e.Uninstall(ctx, user, []string{"zed"}, engine.Options{})
		Expect(err).NotTo(HaveOccurred(), "the hint runs")
		Expect(res.Modules).To(BeEmpty(), "nothing was installed, so nothing is said to be uninstalled")
		Expect(read("default.yml")).To(Equal("version: 1\ntargets: [claude-code]\n"))

		a := mod("a")
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: " + a + "}\n"})
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: user})
		Expect(err).NotTo(HaveOccurred())
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code, opencode]\nmodules:\n  - {name: a, source: " + a + "}\n"})
		_, err = e.Uninstall(ctx, user, []string{"a"}, engine.Options{Targets: []string{"opencode"}})
		Expect(err).To(MatchError("a is not installed on opencode; it is listed for claude-code, opencode but installed only on claude-code"))
		Expect(errs.Hints(err)).To(Equal([]string{"uninstall it there: lola uninstall --user a -t claude-code"}))
	})

	It("-t asks to change, not remove, the entry of the base under a profile on", func() {
		a := mod("a")
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code, opencode]\nmodules:\n  - {name: a, source: " + a + "}\n",
			"go.yml": "version: 1\n"})
		on("user:go")
		_, err := e.Uninstall(ctx, user, []string{"a"}, engine.Options{Targets: []string{"opencode"}, Force: true})
		Expect(err).To(MatchError("a comes from default (base); lola uninstall needs to know whether to change it there, and there is no terminal to ask on"))
		Expect(errs.Hints(err)).To(Equal([]string{"lola uninstall --user a --profile base"}))

		var asked []engine.WhereQuestion
		_, err = e.Uninstall(ctx, user, []string{"a"}, engine.Options{Targets: []string{"opencode"}, AskWhere: func(q engine.WhereQuestion) (engine.WhereChoice, error) {
			asked = append(asked, q)
			return q.Choices[0], nil
		}})
		Expect(err).NotTo(HaveOccurred())
		Expect(asked).To(Equal([]engine.WhereQuestion{{Scope: user, Module: "a", Title: "a comes from default (base), loaded under every profile at user scope.",
			Choices: []engine.WhereChoice{{Label: "Change it in default (base)", Profile: "base"}}}}))
		Expect(read("default.yml")).To(Equal("version: 1\ntargets: [claude-code, opencode]\nmodules:\n  - {name: a, source: " + a + ", targets: [claude-code]}\n"))
	})

	Describe("-t at project scope", func() {
		var (
			proj, notes, yml string
			sc               engine.Scope
		)
		BeforeEach(func() {
			proj = filepath.Join(tmp, "proj")
			notes = filepath.Join(tmp, "mods", "notes")
			writeTree(notes, map[string]string{"lola.yml": "name: notes\n", "commands/notes.md": "notes\n"})
			sc = engine.Scope{Kind: target.Project, ProjectDir: proj}
			yml = filepath.Join(proj, ".lola", "project.yml")
			writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code, opencode]\n"})
			// Nothing installs to the targets yet: only --trust=entries records them (spec 2026-10-29 §16.1).
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: engine.TrustEntries}})
			Expect(err).NotTo(HaveOccurred())
		})
		readYML := func() string {
			GinkgoHelper()
			b, err := os.ReadFile(yml)
			Expect(err).NotTo(HaveOccurred())
			return string(b)
		}
		install := func(ts ...string) {
			GinkgoHelper()
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: notes}}, engine.Options{Targets: ts})
			Expect(err).NotTo(HaveOccurred())
		}

		It("refuses an unknown target, changing nothing", func() {
			install()
			before := readYML()
			_, err := e.Uninstall(ctx, sc, []string{"notes"}, engine.Options{Targets: []string{"nosuch"}})
			Expect(err).To(MatchError(`unknown target "nosuch"`))
			Expect(errs.Hints(err)).To(HaveExactElements(HavePrefix("valid targets: ")))
			Expect(readYML()).To(Equal(before))
		})

		It("refuses targets the module is not installed on, naming where it is, with the run on those", func() {
			install()
			before := readYML()
			_, err := e.Uninstall(ctx, sc, []string{"notes"}, engine.Options{Targets: []string{"gemini-cli"}, Display: []string{"--json"}})
			Expect(err).To(MatchError("notes is not installed on gemini-cli; it is installed on claude-code, opencode"))
			Expect(errs.Hints(err)).To(Equal([]string{"uninstall it there: lola uninstall -C " + proj + " notes -t claude-code,opencode --json"}))
			Expect(readYML()).To(Equal(before))

			_, err = e.Uninstall(ctx, sc, []string{"notes"}, engine.Options{Targets: []string{"gemini-cli"},
				Retarget: func(m string, ts []string) string { return "lola uninstall " + m + " -y -t " + strings.Join(ts, ",") }})
			Expect(errs.Hints(err)).To(Equal([]string{"uninstall it there: lola uninstall notes -y -t claude-code,opencode"}), "the run's own, when given")
		})

		It("refuses -t for an entry of an untrusted project.yml never synced, saying it is installed nowhere", func() {
			writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code, opencode]\nmodules:\n  - {name: notes, source: " + notes + "}\n"})
			before := readYML()
			_, err := e.Uninstall(ctx, sc, []string{"notes"}, engine.Options{Targets: []string{"gemini-cli"}})
			Expect(err).To(MatchError("notes is not installed on gemini-cli; it is listed for claude-code, opencode but not installed there"))
			Expect(errs.Hints(err)).To(Equal([]string{"remove its entry instead: lola uninstall -C " + proj + " notes"}))
			Expect(readYML()).To(Equal(before))

			res, err := e.Uninstall(ctx, sc, []string{"notes"}, engine.Options{})
			Expect(err).NotTo(HaveOccurred(), "the hint runs")
			Expect(res.Modules).To(BeEmpty(), "nothing was installed")
			Expect(readYML()).To(Equal("version: 1\ntargets: [claude-code, opencode]\n"))
		})

		It("writes targets: into the entry only when what it keeps differs from the file's targets", func() {
			install("claude-code", "opencode", "gemini-cli")
			head := "version: 1\ntargets: [claude-code, opencode]\nmodules:\n  - name: notes\n    source: " + notes + "\n"
			Expect(readYML()).To(Equal(head + "    targets: [claude-code, opencode, gemini-cli]\n"))
			res, err := e.Uninstall(ctx, sc, []string{"notes"}, engine.Options{Targets: []string{"gemini-cli"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Modules).To(HaveExactElements(HaveField("Targets", []string{"gemini-cli"})))
			Expect(readYML()).To(Equal(head), "what it keeps is the file's targets: the entry follows them again")

			install("claude-code", "opencode", "gemini-cli")
			res, err = e.Uninstall(ctx, sc, []string{"notes"}, engine.Options{Targets: []string{"opencode"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Modules).To(HaveExactElements(HaveField("Targets", []string{"opencode"})))
			Expect(readYML()).To(Equal(head + "    targets: [claude-code, gemini-cli]\n"))

			res, err = e.Uninstall(ctx, sc, []string{"notes"}, engine.Options{Targets: []string{"gemini-cli", "opencode"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Modules).To(HaveExactElements(HaveField("Targets", []string{"gemini-cli"})), "only what it is installed on")
			Expect(readYML()).To(Equal(head + "    targets: [claude-code]\n"))
		})
	})

	Describe("the directories it empties", func() {
		var (
			proj, notes string
			sc          engine.Scope
		)
		BeforeEach(func() {
			proj = filepath.Join(tmp, "proj")
			notes = filepath.Join(tmp, "mods", "notes")
			writeTree(notes, map[string]string{"lola.yml": "name: notes\n", "commands/notes.md": "notes\n",
				"skills/notes/SKILL.md": "---\nname: notes\ndescription: take notes\n---\nTake notes.\n"})
			sc = engine.Scope{Kind: target.Project, ProjectDir: proj}
			writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code, opencode]\n"})
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: engine.TrustEntries}})
			Expect(err).NotTo(HaveOccurred())
		})
		uninstall := func() *engine.Result {
			GinkgoHelper()
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: notes}}, engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			res, err := e.Uninstall(ctx, sc, []string{"notes"}, engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			return res
		}
		dirs := func(res *engine.Result) []string {
			var out []string
			for _, st := range res.Steps {
				if st.Kind == plan.KindDirectory {
					Expect(st.Action).To(Equal(plan.ActionDelete))
					Expect(st.Module).To(Equal("notes"))
					out = append(out, st.Target+" "+st.Path)
				}
			}
			return out
		}

		It("removes the directories lola's files leave empty, and says so, below the project's own directories", func() {
			res := uninstall()
			Expect(dirs(res)).To(Equal([]string{"claude-code .claude/commands", "claude-code .claude/skills/notes", "claude-code .claude/skills",
				"opencode .opencode/commands", "opencode .opencode/skills/notes", "opencode .opencode/skills"}))
			for _, top := range []string{".claude", ".opencode"} {
				left, err := os.ReadDir(filepath.Join(proj, top))
				Expect(err).NotTo(HaveOccurred(), "%s stays: it tells lola the assistant is set up here", top)
				Expect(left).To(BeEmpty())
			}
			Expect(filepath.Join(proj, ".lola", "project.yml")).To(BeARegularFile())
		})

		It("keeps a directory holding anything of yours, a symlinked directory and what it leads to, in or out of the project", func() {
			outside := filepath.Join(tmp, "elsewhere")
			writeTree(proj, map[string]string{".claude/settings.local.json": "{}\n"})
			Expect(os.MkdirAll(filepath.Join(proj, ".claude/skills/yours"), 0o755)).To(Succeed())
			Expect(os.MkdirAll(filepath.Join(proj, "shared"), 0o755)).To(Succeed())
			Expect(os.MkdirAll(filepath.Join(proj, ".opencode"), 0o755)).To(Succeed())
			Expect(os.Symlink(filepath.Join(proj, "shared"), filepath.Join(proj, ".opencode/commands"))).To(Succeed())
			Expect(os.MkdirAll(outside, 0o755)).To(Succeed())
			Expect(os.Symlink(outside, filepath.Join(proj, ".opencode/skills"))).To(Succeed())

			res := uninstall()
			Expect(dirs(res)).To(Equal([]string{"claude-code .claude/commands", "claude-code .claude/skills/notes"}))
			Expect(filepath.Join(proj, ".claude/settings.local.json")).To(BeARegularFile())
			Expect(filepath.Join(proj, ".claude/skills/yours")).To(BeADirectory())
			Expect(filepath.Join(proj, ".opencode/commands")).To(BeADirectory(), "the link is kept")
			Expect(filepath.Join(proj, "shared")).To(BeADirectory())
			Expect(filepath.Join(outside, "notes")).To(BeADirectory(), "a directory out of the project is never removed")
			Expect(filepath.Join(outside, "notes", "SKILL.md")).NotTo(BeAnExistingFile())
		})
	})

	It("-t checks trust for a project profile's entry, as for project.yml's", func() {
		proj := filepath.Join(tmp, "proj")
		writeTree(filepath.Join(proj, "mods", "a"), map[string]string{"lola.yml": "name: a\n", "commands/a.md": "a\n"})
		a := "../../mods/a" // a project profile's local source, beside .lola/profiles
		sc := engine.Scope{Kind: target.Project, ProjectDir: proj}
		team := filepath.Join(proj, ".lola", "profiles", "team.yml")
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\n",
			".lola/profiles/team.yml": "version: 1\ntargets: [claude-code, opencode]\nmodules:\n  - {name: a, source: " + a + "}\n"})
		trustAll := func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOn, Name: "team"}, Trust: trustAll})
		Expect(err).NotTo(HaveOccurred())
		// A pull changes the entry behind lola's back.
		writeTree(proj, map[string]string{".lola/profiles/team.yml": "version: 1\ntargets: [claude-code, opencode]\nmodules:\n  - {name: a, source: " + a + ", rules: false}\n"})
		_, err = e.Uninstall(ctx, sc, []string{"a"}, engine.Options{Targets: []string{"opencode"}})
		Expect(err).To(MatchError("a: .lola/profiles/team.yml changed for it since you last trusted it"))
		Expect(errs.Hints(err)).To(Equal([]string{"review and apply it: lola sync -C " + proj + " a"}))

		writeTree(proj, map[string]string{".lola/profiles/team.yml": "version: 1\ntargets: [claude-code, opencode]\nmodules:\n  - {name: a, source: " + a + "}\n"})
		_, err = e.Uninstall(ctx, sc, []string{"a"}, engine.Options{Targets: []string{"opencode"}})
		Expect(err).NotTo(HaveOccurred(), "the record covers the entry as lola wrote it")
		b, err := os.ReadFile(team)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal("version: 1\ntargets: [claude-code, opencode]\nmodules:\n  - {name: a, source: " + a + ", targets: [claude-code]}\n"))
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc})
		Expect(err).NotTo(HaveOccurred(), "lola's own edit is trusted")
	})
})

// writeRemoteKit serves a git module kit, installs it once at user scope to learn its pin, and
// keeps a remote profile name whose one entry is kit at that pin (profile.yml and source.json, as
// lola profile --add writes them); default.yml is left with no module.
func writeRemoteKit(p paths.Paths, name string) {
	GinkgoHelper()
	tmp := filepath.Dir(p.Home)
	work, bare := filepath.Join(tmp, "work-kit"), filepath.Join(tmp, "srv-kit", "kit.git")
	writeTree(work, map[string]string{"lola.yml": "name: kit\n", "commands/kit.md": "kit\n"})
	gitIn(work, "init", "-q", "-b", "main")
	gitIn(work, "add", "-A")
	gitIn(work, "commit", "-q", "-m", "one")
	gitIn(work, "tag", "v1")
	gitIn(tmp, "clone", "-q", "--bare", work, bare)
	url := testenv.ServeGitTLS(filepath.Dir(bare), nil) + "/kit.git" // a remote profile names https sources only
	e := engine.New(p)
	user := engine.Scope{Kind: target.User}
	_, err := e.Install(context.Background(), user, tmp, []engine.InstallRequest{{Arg: url, Ref: "v1"}}, engine.Options{Targets: []string{"claude-code"}})
	Expect(err).NotTo(HaveOccurred())
	led, err := e.Ledger(user)
	Expect(err).NotTo(HaveOccurred())
	in := led.Modules["kit"]
	body := "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + url + ", ref: v1, commit: " + in.Commit + ", digest: " + in.Digest + "}\n"
	o, err := json.Marshal(profile.Origin{Version: profile.OriginVersion, Source: "https://example.com/profiles.git", Commit: strings.Repeat("a", 40),
		Digest: profile.Digest([]byte(body))})
	Expect(err).NotTo(HaveOccurred())
	writeTree(filepath.Join(p.RemoteProfiles, name), map[string]string{profile.RemoteFile: body, profile.OriginFile: string(o)})
	writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\n"})
}
