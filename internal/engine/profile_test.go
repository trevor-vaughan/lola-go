package engine_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("profiles", func() {
	var (
		tmp, proj, mods string
		p               paths.Paths
		e               *engine.Engine
		user, sc        engine.Scope
		removals        [][]engine.Removal
		ctx             = context.Background()
	)
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		proj, mods = filepath.Join(tmp, "proj"), filepath.Join(tmp, "mods")
		e.Getwd = func() (string, error) { return proj, nil }
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		user, sc = engine.Scope{Kind: target.User}, engine.Scope{Kind: target.Project, ProjectDir: proj}
		removals = nil
	})
	// mod writes a folder module named name under mods/dir whose command says text.
	mod := func(dir, name, text string) string {
		GinkgoHelper()
		path := filepath.Join(mods, dir)
		writeTree(path, map[string]string{"lola.yml": "name: " + name + "\n", "commands/" + name + ".md": text + "\n"})
		return path
	}
	writeProfile := func(name, content string) string {
		GinkgoHelper()
		writeTree(p.UserProfiles, map[string]string{name + ".yml": content})
		return filepath.Join(p.UserProfiles, name+".yml")
	}
	ledgerPath := func(s engine.Scope) string {
		if s.Kind == target.User {
			return p.UserLedger
		}
		return filepath.Join(s.ProjectDir, ".lola", "lock.json")
	}
	// activate turns profiles on the way lola profile --on records it, in lock.json.
	activate := func(s engine.Scope, names ...string) {
		GinkgoHelper()
		led, err := e.Ledger(s)
		Expect(err).NotTo(HaveOccurred())
		led.Profiles = nil
		for _, n := range names {
			led.Profiles = append(led.Profiles, ledger.Active{Name: n, By: ledger.ByOn})
		}
		Expect(led.Save(ledgerPath(s))).To(Succeed())
	}
	sync := func(s engine.Scope) (*engine.SyncResult, error) {
		return e.Sync(ctx, engine.SyncRequest{Scope: s, ConfirmRemovals: func(r []engine.Removal) (bool, error) {
			removals = append(removals, r)
			return true, nil
		}, Trust: func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }})
	}
	// turn runs lola profile's action on names in s, confirming removals as -y does.
	turn := func(s engine.Scope, action engine.ProfileAction, names ...string) (*engine.SyncResult, error) {
		return e.Sync(ctx, engine.SyncRequest{Scope: s, Profiles: &engine.ProfileChange{Action: action, Names: names},
			ConfirmRemovals: func(r []engine.Removal) (bool, error) {
				removals = append(removals, r)
				return true, nil
			}})
	}
	// exported is changes as JSON and human output see them.
	exported := func(cs []engine.LayerChange) []engine.LayerChange {
		out := []engine.LayerChange{}
		for _, c := range cs {
			out = append(out, engine.LayerChange{Module: c.Module, Action: c.Action, From: c.From, Over: c.Over, Version: c.Version, Was: c.Was})
		}
		return out
	}
	activeList := func(s engine.Scope) []ledger.Active {
		GinkgoHelper()
		led, err := e.Ledger(s)
		Expect(err).NotTo(HaveOccurred())
		return led.Profiles
	}
	installed := func(s engine.Scope) map[string]string {
		GinkgoHelper()
		led, err := e.Ledger(s)
		Expect(err).NotTo(HaveOccurred())
		out := map[string]string{}
		for name, ent := range led.Modules {
			out[name] = ent.From
		}
		return out
	}
	command := func(home, name string) string {
		GinkgoHelper()
		b, err := os.ReadFile(filepath.Join(home, ".claude", "commands", name+".md"))
		Expect(err).NotTo(HaveOccurred())
		return string(b)
	}

	It("a later layer replaces an earlier entry whole, with a notice", func() {
		a, b, b2, c := mod("a", "a", "A"), mod("b", "b", "B from go"), mod("b2", "b", "B from user.yml"), mod("c", "c", "C")
		writeTree(a, map[string]string{"rules/style.md": "Prefer small diffs.\n"})
		writeProfile("default", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: "+a+", rules: false}\n  - {name: c, source: "+c+"}\n")
		writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: "+a+"}\n  - {name: b, source: "+b+"}\n  - {name: c, source: "+c+"}\n")
		writeTree(filepath.Dir(p.UserManifest), map[string]string{"user.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: b, source: " + b2 + "}\n"})
		activate(user, "user:go")

		res, err := sync(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElements(
			engine.Notice{Level: engine.LevelWarn, Text: "a: profile:user:go overrides profile:default"},
			engine.Notice{Level: engine.LevelWarn, Text: "b: user.yml overrides profile:user:go (" + b2 + " over " + b + ")"},
		))
		Expect(res.Notices).NotTo(ContainElement(HaveField("Text", HavePrefix("c:"))), "identical entries are silent")
		Expect(installed(user)).To(Equal(map[string]string{"a": "profile:user:go", "b": "", "c": "profile:user:go"}), "an identical entry still wins from the later layer")
		Expect(command(p.Home, "b")).To(Equal("B from user.yml\n"))
		Expect(filepath.Join(p.Home, ".claude", "rules", "lola", "a", "style.md")).To(BeARegularFile(),
			"go's entry replaces default's whole: default's rules: false is not merged in")
		Expect(p.UserManifest).To(BeAnExistingFile())
		b0, err := os.ReadFile(p.UserManifest)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b0)).NotTo(ContainSubstring("name: a"), "a profile's entry never reaches user.yml")
	})

	It("default is on in user scope and never in project scope", func() {
		c := mod("c", "c", "C")
		writeProfile("default", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: c, source: "+c+"}\n")
		writeTree(proj, map[string]string{"a/lola.yml": "name: a\n", "a/commands/a.md": "A\n",
			".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: a}\n"})

		_, err := sync(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(installed(sc)).To(Equal(map[string]string{"a": ""}))
		Expect(filepath.Join(proj, ".claude", "commands", "c.md")).NotTo(BeAnExistingFile())

		_, err = sync(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(installed(user)).To(Equal(map[string]string{"c": "profile:default"}))
		Expect(command(p.Home, "c")).To(Equal("C\n"))
	})

	It("an active profile that no longer exists warns and counts as off", func() {
		a, c := mod("a", "a", "A"), mod("c", "c", "C")
		gone := writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: "+a+"}\n")
		writeTree(filepath.Dir(p.UserManifest), map[string]string{"user.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: c, source: " + c + "}\n"})
		activate(user, "user:go")
		_, err := sync(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(installed(user)).To(HaveKeyWithValue("a", "profile:user:go"))

		Expect(os.Remove(gone)).To(Succeed())
		missing := engine.Notice{Level: engine.LevelWarn, Text: "profile user:go is on but no longer exists → lola profile --user user:go --off"}
		rows, notices, err := e.Status(ctx, user)
		Expect(err).NotTo(HaveOccurred())
		Expect(notices).To(ConsistOf(missing))
		Expect(rows).To(ContainElement(And(HaveField("Name", "a"), HaveField("From", "profile:user:go"), HaveField("Status", "ok"))),
			"list keeps the layer lock.json recorded for a module no layer wants any more")
		rendered, err := e.Render(ctx, user, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(rendered.Notices).To(ContainElement(missing))
		Expect(installed(user)).To(HaveKeyWithValue("a", "profile:user:go"), "render removes nothing")
		Expect(command(p.Home, "a")).To(Equal("A\n"))
		res, err := sync(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(missing))
		Expect(removals).To(ConsistOf(ConsistOf(And(HaveField("Module", "a"), HaveField("WholeModule", true)))),
			"its modules go as any unwanted module does, with the usual confirmation")
		Expect(installed(user)).To(Equal(map[string]string{"c": ""}))
		led, err := e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Profiles).To(Equal([]ledger.Active{{Name: "user:go", By: ledger.ByOn}}), "the active list is not changed by a sync")
	})

	It("removes a gone profile's modules from a scope with no manifest", func() {
		a, c := mod("a", "a", "A"), mod("c", "c", "C")
		def := writeProfile("default", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: c, source: "+c+"}\n")
		_, err := sync(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.Remove(def)).To(Succeed())
		_, err = sync(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(installed(user)).To(BeEmpty())
		Expect(filepath.Join(p.Home, ".claude", "commands", "c.md")).NotTo(BeAnExistingFile())

		goProfile := writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: "+a+"}\n")
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		activate(sc, "user:go")
		_, err = sync(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.Remove(goProfile)).To(Succeed())
		res, err := sync(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Removals).To(ConsistOf(HaveField("Module", "a")))
		Expect(installed(sc)).To(BeEmpty())
		Expect(filepath.Join(proj, ".lola", "project.yml")).NotTo(BeAnExistingFile())
	})

	It("lists each module with the layer it comes from, and a profile's entries not installed yet", func() {
		a, c := mod("a", "a", "A"), mod("c", "c", "C")
		writeProfile("default", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: c, source: "+c+"}\n")
		writeTree(filepath.Dir(p.UserManifest), map[string]string{"user.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: " + a + "}\n"})
		rows, notices, err := e.Status(ctx, user)
		Expect(err).NotTo(HaveOccurred())
		Expect(notices).To(BeEmpty())
		Expect(rows).To(ConsistOf(
			And(HaveField("Name", "a"), HaveField("From", "user.yml"), HaveField("Status", "not_installed"), HaveField("Target", "claude-code")),
			And(HaveField("Name", "c"), HaveField("From", "profile:default"), HaveField("Status", "not_installed"), HaveField("Target", "claude-code")),
		))
		_, err = sync(user)
		Expect(err).NotTo(HaveOccurred())
		rows, _, err = e.Status(ctx, user)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(ConsistOf(
			And(HaveField("Name", "a"), HaveField("From", "user.yml"), HaveField("Status", "ok")),
			And(HaveField("Name", "c"), HaveField("From", "profile:default"), HaveField("Status", "ok")),
		))
	})

	It("render installs a profile's module at project scope as installed, and leaves out one no layer lists", func() {
		a := mod("a", "a", "A")
		writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: "+a+"}\n")
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		activate(sc, "user:go")
		_, err := sync(sc)
		Expect(err).NotTo(HaveOccurred())
		shipped := filepath.Join(proj, ".claude", "commands", "a.md")
		Expect(os.Remove(shipped)).To(Succeed())

		res, err := e.Render(ctx, sc, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Findings).NotTo(ContainElement(HaveField("Severity", target.SeverityWarn)), "a profile's entry needs no trust record")
		Expect(shipped).To(BeARegularFile())
		Expect(installed(sc)).To(Equal(map[string]string{"a": "profile:user:go"}))

		// The profile names no targets: its entry installs to project.yml's, which a pull changes.
		writeProfile("go", "version: 1\nmodules:\n  - {name: a, source: "+a+"}\n")
		writeTree(filepath.Join(proj, ".lola"), map[string]string{"project.yml": "version: 1\ntargets: [claude-code]\n"})
		_, err = sync(sc)
		Expect(err).NotTo(HaveOccurred())
		writeTree(filepath.Join(proj, ".lola"), map[string]string{"project.yml": "version: 1\ntargets: [claude-code, opencode]\n"})
		Expect(os.Remove(shipped)).To(Succeed())
		res, err = e.Render(ctx, sc, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Findings).To(ContainElement(And(HaveField("Module", "a"), HaveField("Code", engine.CodeNotTrusted),
			HaveField("Text", "it installs to the targets of project.yml, which changed since you last trusted them"))))
		Expect(shipped).NotTo(BeAnExistingFile(), "left as installed")

		activate(sc)
		res, err = e.Render(ctx, sc, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Findings).To(ContainElement(And(HaveField("Module", "a"), HaveField("Code", engine.CodeNotInManifest),
			HaveField("Text", "profile:user:go no longer lists it"))))
		Expect(shipped).NotTo(BeAnExistingFile())
	})

	It("uninstall refuses a module an active profile wants", func() {
		a, b, c := mod("a", "a", "A"), mod("b", "b", "B"), mod("c", "c", "C")
		writeProfile("default", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: c, source: "+c+"}\n")
		def := filepath.Join(p.UserProfiles, "default.yml")
		writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: "+a+"}\n")
		writeTree(filepath.Dir(p.UserManifest), map[string]string{"user.yml": "version: 1\ntargets: [claude-code]\nmodules:\n" +
			"  - {name: a, source: " + a + "}\n  - {name: b, source: " + b + "}\n"})
		activate(user, "user:go")
		_, err := sync(user)
		Expect(err).NotTo(HaveOccurred())
		before, err := os.ReadFile(p.UserLedger)
		Expect(err).NotTo(HaveOccurred())

		_, err = e.Uninstall(ctx, user, []string{"a"}, engine.Options{})
		Expect(err).To(MatchError("a is wanted by profile user:go"), "user.yml lists it too: uninstalling would leave it installed")
		Expect(errs.Hints(err)).To(Equal([]string{"turn user:go off: lola profile --user user:go --off"}))
		_, err = e.Uninstall(ctx, user, []string{"b", "c"}, engine.Options{})
		Expect(err).To(MatchError("c is wanted by profile default"))
		Expect(errs.Hints(err)).To(Equal([]string{"remove it from " + def + ", which is always on at user scope"}))
		Expect(os.ReadFile(p.UserLedger)).To(Equal(before), "a refusal changes nothing")
		Expect(command(p.Home, "b")).To(Equal("B\n"))

		_, err = e.Uninstall(ctx, user, []string{"b"}, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(installed(user)).To(Equal(map[string]string{"a": "", "c": "profile:default"}))
	})

	It("install coaches about every layer's modules and says when its entry overrides a profile's", func() {
		a, c := mod("a", "a", "A"), mod("c", "c", "C")
		writeProfile("default", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: "+a+"}\n  - {name: c, source: "+c+"}\n")
		notice, hint, err := e.InstallCoaching(ctx, user)
		Expect(err).NotTo(HaveOccurred())
		Expect(notice).To(Equal("2 modules in profile:default are not installed → lola sync --user"))
		Expect(hint).To(Equal("install what profile:default lists: lola sync --user"))

		res, err := e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: a}}, engine.Options{Targets: []string{"claude-code", "opencode"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn, Text: "a: user.yml overrides profile:default"}))
		Expect(installed(user)).To(Equal(map[string]string{"a": ""}))

		writeTree(filepath.Dir(p.UserManifest), map[string]string{"user.yml": "version: 1\ntargets: [claude-code]\nmodules:\n" +
			"  - {name: a, source: " + a + ", targets: [claude-code, opencode]}\n  - {name: b, source: ../b}\n"})
		notice, hint, err = e.InstallCoaching(ctx, user)
		Expect(err).NotTo(HaveOccurred())
		Expect(notice).To(Equal("2 modules in profile:default and user.yml are not installed → lola sync --user"))
		Expect(hint).To(Equal("install what profile:default and user.yml list: lola sync --user"))

		bad := writeProfile("go", "version: 1\nmodules: oops\n")
		activate(user, "user:go")
		_, err = e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: c}}, engine.Options{Targets: []string{"claude-code"}})
		Expect(err).To(MatchError(HavePrefix(bad+": ")), "an active profile that does not load refuses the install")
		Expect(e.Store.Has("c")).To(BeFalse(), "before anything is fetched")
	})

	It("refuses a run whose active profile does not load, naming the file and line", func() {
		bad := writeProfile("go", "version: 1\nmodules:\n  - {name: a, source: ../a, colour: red}\n")
		activate(user, "user:go")
		_, err := sync(user)
		Expect(err).To(MatchError(And(HavePrefix(bad+": "), ContainSubstring("line 3: field colour not found"))))
	})

	It("a project with no active profile never lists the user's profiles", func() {
		Expect(os.MkdirAll(filepath.Dir(p.UserProfiles), 0o755)).To(Succeed())
		Expect(os.WriteFile(p.UserProfiles, []byte("not a directory\n"), 0o644)).To(Succeed())
		writeTree(proj, map[string]string{"a/lola.yml": "name: a\n", "a/commands/a.md": "A\n",
			".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: a}\n"})

		_, err := sync(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(installed(sc)).To(Equal(map[string]string{"a": ""}))

		activate(sc, "user:go")
		_, err = sync(sc)
		Expect(err).To(MatchError(HavePrefix("cannot list " + p.UserProfiles + ": ")))
	})

	It("a scope with only a profile syncs without a manifest", func() {
		a, c := mod("a", "a", "A"), mod("c", "c", "C")
		_, err := sync(user)
		Expect(err).To(MatchError(HavePrefix("no user.yml at ")), "nothing any layer wants: still refused")

		writeProfile("default", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: c, source: "+c+"}\n")
		_, err = sync(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(installed(user)).To(Equal(map[string]string{"c": "profile:default"}))
		Expect(p.UserManifest).NotTo(BeAnExistingFile())

		writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: "+a+"}\n")
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		activate(sc, "user:go")
		res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc}) // no Trust: a user profile needs none
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Trust.Status).To(Equal("trusted"))
		Expect(installed(sc)).To(Equal(map[string]string{"a": "profile:user:go"}))
		Expect(filepath.Join(proj, ".lola", "project.yml")).NotTo(BeAnExistingFile())
	})

	It("lola writes pins only into project.yml and project profiles", func() {
		work, bare := filepath.Join(tmp, "work"), filepath.Join(tmp, "srv", "example.git")
		writeTree(work, exampleModule)
		gitIn(work, "init", "-q", "-b", "main")
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "init")
		gitIn(tmp, "clone", "-q", "--bare", work, bare)
		url := testenv.ServeGit(filepath.Join(tmp, "srv")) + "/example.git"
		push := func(file, text string) string {
			writeTree(work, map[string]string{file: text})
			gitIn(work, "add", "-A")
			gitIn(work, "commit", "-q", "-m", "change")
			gitIn(work, "push", "-q", "-f", "--all", bare)
			return gitIn(work, "rev-parse", "HEAD")
		}
		first := gitIn(work, "rev-parse", "HEAD")
		c := mod("c", "c", "C")
		def := writeProfile("default", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: example, source: "+url+"}\n")
		goProfile := writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: c, source: "+c+"}\n")
		activate(user, "user:go")
		files := func() []string {
			GinkgoHelper()
			var out []string
			for _, f := range []string{def, goProfile} {
				b, err := os.ReadFile(f)
				Expect(err).NotTo(HaveOccurred())
				out = append(out, string(b))
			}
			return out
		}
		before := files()
		pinned := func() *ledger.Entry {
			GinkgoHelper()
			led, err := e.Ledger(user)
			Expect(err).NotTo(HaveOccurred())
			return led.Modules["example"]
		}

		res, err := sync(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Pins).To(BeEmpty(), "no pin is written to any file")
		Expect(files()).To(Equal(before), "default.yml and the user's profiles are never written")
		Expect(p.UserManifest).NotTo(BeAnExistingFile())
		Expect(pinned()).To(And(HaveField("Commit", first), HaveField("Digest", MatchRegexp(`^sha256:`)), HaveField("From", "profile:default")))
		v1 := pinned().Digest

		// The cache head moves (another project, lola cache update): the pin lock.json records holds.
		second := push("commands/ship.md", "Ship it faster\n")
		_, err = e.Fetch(ctx, sourceSpecGit(url))
		Expect(err).NotTo(HaveOccurred())
		res, err = sync(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.InSync).To(BeTrue())
		Expect(pinned()).To(HaveField("Digest", v1))

		// The winning layer changes between identical unpinned entries: the pin holds.
		Expect(os.WriteFile(goProfile, []byte("version: 1\ntargets: [claude-code]\nmodules:\n  - {name: c, source: "+c+
			"}\n  - {name: example, source: "+url+"}\n"), 0o600)).To(Succeed())
		_, err = sync(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(pinned()).To(And(HaveField("From", "profile:user:go"), HaveField("Commit", first), HaveField("Digest", v1)))
		Expect(os.WriteFile(goProfile, []byte(before[1]), 0o600)).To(Succeed())

		res, err = e.Sync(ctx, engine.SyncRequest{Scope: user, Modules: []string{"example"}, Update: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Pins).To(BeEmpty())
		Expect(pinned()).To(And(HaveField("Commit", second), Not(HaveField("Digest", v1))), "--update resolves an unpinned profile entry again")
		Expect(files()).To(Equal(before))

		Expect(os.WriteFile(def, []byte("version: 1\ntargets: [claude-code]\nmodules:\n  - {name: example, source: "+url+
			", commit: "+first+", digest: \""+v1+"\"}\n"), 0o600)).To(Succeed())
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: user, Modules: []string{"example"}, Update: true})
		Expect(err).To(MatchError("example: its pin is in " + def + ", which lola never writes"))
		Expect(errs.Hints(err)).To(Equal([]string{"edit the pin in " + def + ", then: lola sync --user example"}))

		res, err = e.Sync(ctx, engine.SyncRequest{Scope: user, Update: true, ConfirmRemovals: func([]engine.Removal) (bool, error) { return true, nil }})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo,
			Text: "example: --update leaves the pin " + def + " sets; edit the pin in " + def}))
		Expect(pinned()).To(And(HaveField("Commit", first), HaveField("Digest", v1)), "the profile's pin is installed")

		// A project profile is the project's: its unpinned entry is pinned, with the user's trust,
		// in the profile itself, as project.yml's are (§19 R1).
		writeTree(proj, map[string]string{".lola/profiles/team.yml": "version: 1\ntargets: [claude-code]\nmodules:\n" +
			"  # the team's review tooling\n  - {name: example, source: " + url + "}\n"})
		team := filepath.Join(proj, ".lola", "profiles", "team.yml")
		own := files()
		activate(sc, "project:team")
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc})
		Expect(err).To(MatchError(ContainSubstring("the pins it would write for example")), "pinning needs trust")
		res, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: func(engine.TrustReport) (engine.TrustAnswer, error) {
			return engine.TrustAnswer{All: true}, nil
		}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Pins).To(ConsistOf(And(HaveField("Module", "example"), HaveField("Profile", "project:team"), HaveField("Commit", second),
			HaveField("Written", true))))
		b, err := os.ReadFile(team)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(And(ContainSubstring("# the team's review tooling"), ContainSubstring("commit: "+second)), "written in place, comments kept")
		res, err = e.Sync(ctx, engine.SyncRequest{Scope: sc})
		Expect(err).NotTo(HaveOccurred(), "lola's own pin is trusted")
		Expect(res.InSync).To(BeTrue())

		third := push("commands/ship.md", "Ship it fastest\n")
		res, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Modules: []string{"example"}, Update: true,
			Trust: func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Pins).To(ConsistOf(And(HaveField("Profile", "project:team"), HaveField("Commit", third), HaveField("Reason", engine.PinUpdate))))
		Expect(os.ReadFile(team)).To(ContainSubstring("commit: " + third))
		Expect(files()).To(Equal(own), "the user's profiles are still never written")
	})

	It("writes a project profile's pins in the apply's transaction, never over a file that moved", func() {
		if os.Geteuid() == 0 {
			Skip("root ignores directory permissions")
		}
		work, bare := filepath.Join(tmp, "work"), filepath.Join(tmp, "srv", "example.git")
		writeTree(work, exampleModule)
		gitIn(work, "init", "-q", "-b", "main")
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "init")
		gitIn(tmp, "clone", "-q", "--bare", work, bare)
		url := testenv.ServeGit(filepath.Join(tmp, "srv")) + "/example.git"
		content := "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: example, source: " + url + "}\n"
		writeTree(proj, map[string]string{".lola/profiles/team.yml": content})
		team, profiles := filepath.Join(proj, ".lola", "profiles", "team.yml"), filepath.Join(proj, ".lola", "profiles")
		activate(sc, "project:team")
		before, err := os.ReadFile(ledgerPath(sc))
		Expect(err).NotTo(HaveOccurred())
		unchanged := func() {
			GinkgoHelper()
			Expect(os.ReadFile(ledgerPath(sc))).To(Equal(before), "lock.json")
			r, err := e.Trust.Load(proj)
			Expect(err).NotTo(HaveOccurred())
			Expect(r.View.Profiles).To(BeEmpty(), "no trust recorded")
			Expect(filepath.Join(proj, ".claude", "commands", "ship.md")).NotTo(BeAnExistingFile())
		}

		// The profile changes while the user answers: what was checked is not what would be written.
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: func(engine.TrustReport) (engine.TrustAnswer, error) {
			Expect(os.WriteFile(team, []byte(content+"# edited\n"), 0o644)).To(Succeed())
			return engine.TrustAnswer{All: true}, nil
		}})
		Expect(err).To(MatchError(ContainSubstring(team + " changed while lola was running; not writing its pins")))
		unchanged()
		Expect(os.WriteFile(team, []byte(content), 0o644)).To(Succeed())

		// The profile cannot be written: nothing of the apply stays.
		Expect(os.Chmod(profiles, 0o555)).To(Succeed())
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }})
		Expect(err).To(HaveOccurred())
		Expect(os.Chmod(profiles, 0o755)).To(Succeed())
		unchanged()
		Expect(os.ReadFile(team)).To(Equal([]byte(content)))

		// The record cannot be saved after the apply: the error names the profile lola wrote.
		Expect(os.RemoveAll(p.TrustLock)).To(Succeed())
		Expect(os.MkdirAll(p.TrustLock, 0o700)).To(Succeed()) // a lock that cannot be taken
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }})
		Expect(err).To(MatchError(ContainSubstring("lock.json and " + filepath.Join(".lola", "profiles", "team.yml") +
			" were updated, but lola could not record that you trust what it wrote to " + filepath.Join(".lola", "profiles", "team.yml") + ": ")))
		Expect(os.ReadFile(team)).To(ContainSubstring("commit: "), "the apply stands")
	})

	It("checks that a profile's pin read from a project's lock.json is on its ref", func() {
		work, bare := filepath.Join(tmp, "work"), filepath.Join(tmp, "srv", "example.git")
		writeTree(work, exampleModule)
		gitIn(work, "init", "-q", "-b", "main")
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "init")
		gitIn(work, "checkout", "-q", "-b", "fork")
		writeTree(work, map[string]string{"commands/ship.md": "Ship to the attacker\n"})
		gitIn(work, "commit", "-q", "-am", "fork")
		forked := gitIn(work, "rev-parse", "HEAD")
		gitIn(tmp, "clone", "-q", "--bare", work, bare)
		url := testenv.ServeGit(filepath.Join(tmp, "srv")) + "/example.git"
		writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: example, source: "+url+", ref: main}\n")
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		activate(sc, "user:go")
		_, err := sync(sc)
		Expect(err).NotTo(HaveOccurred())

		// The fork's commit is cached (another project), and a planted lock.json names it.
		v, err := e.Fetch(ctx, source.Spec{Kind: source.KindGit, Location: url, Ref: "fork"})
		Expect(err).NotTo(HaveOccurred())
		led, err := e.Ledger(sc)
		Expect(err).NotTo(HaveOccurred())
		led.Modules["example"].Commit, led.Modules["example"].Digest = forked, v.Meta.Digest
		Expect(led.Save(ledgerPath(sc))).To(Succeed())
		_, err = sync(sc)
		Expect(err).To(MatchError(ContainSubstring(forked)))
		res, err := e.Render(ctx, sc, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Findings).To(ContainElement(And(HaveField("Module", "example"), HaveField("Code", engine.CodeNotTrusted),
			HaveField("Text", ContainSubstring("lock.json pins it to "+forked)),
			HaveField("Hint", "re-pin it: lola sync --project --update example"))), "render leaves it as installed")
		b, err := os.ReadFile(filepath.Join(proj, ".claude", "commands", "ship.md"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).NotTo(ContainSubstring("attacker"))
	})

	It("checks that a project profile's entry recorded unpinned, read from a project's lock.json, is on its ref", func() {
		work, bare := filepath.Join(tmp, "work"), filepath.Join(tmp, "srv", "example.git")
		writeTree(work, exampleModule)
		gitIn(work, "init", "-q", "-b", "main")
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "init")
		gitIn(work, "checkout", "-q", "-b", "fork")
		writeTree(work, map[string]string{"commands/ship.md": "Ship to the attacker\n"})
		gitIn(work, "commit", "-q", "-am", "fork")
		forked := gitIn(work, "rev-parse", "HEAD")
		gitIn(tmp, "clone", "-q", "--bare", work, bare)
		url := testenv.ServeGit(filepath.Join(tmp, "srv")) + "/example.git"
		writeTree(proj, map[string]string{"mods/gone/lola.yml": "name: gone\n", "mods/gone/commands/gone.md": "Gone\n",
			".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: gone, source: mods/gone}\n"})
		_, err := sync(sc)
		Expect(err).NotTo(HaveOccurred())

		// A pull drops gone and adds project:team, turned on; declining the removal records team's
		// entry unpinned, since lola wrote no pin for it.
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules: []\n",
			".lola/profiles/team.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: example, source: " + url + ", ref: main}\n"})
		activate(sc, "project:team")
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, ConfirmRemovals: func([]engine.Removal) (bool, error) { return false, nil },
			Trust: func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }})
		Expect(errors.As(err, new(*engine.RemovalsDeclinedError))).To(BeTrue(), "%v", err)

		// The fork's commit is cached (another project), and a planted lock.json names it.
		v, err := e.Fetch(ctx, source.Spec{Kind: source.KindGit, Location: url, Ref: "fork"})
		Expect(err).NotTo(HaveOccurred())
		led, err := e.Ledger(sc)
		Expect(err).NotTo(HaveOccurred())
		led.Modules["example"] = &ledger.Entry{From: "profile:project:team", Source: url, Ref: "main", Commit: forked, Digest: v.Meta.Digest,
			Targets: []string{"claude-code"}}
		Expect(led.Save(ledgerPath(sc))).To(Succeed())
		res, err := e.Render(ctx, sc, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Findings).To(ContainElement(And(HaveField("Module", "example"), HaveField("Code", engine.CodeNotTrusted),
			HaveField("Text", ContainSubstring("lock.json pins it to "+forked)),
			HaveField("Hint", "re-pin it: lola sync --project --update example"))), "render leaves it as installed")
		Expect(filepath.Join(proj, ".claude", "commands", "ship.md")).NotTo(BeAnExistingFile(), "the fork's ship.md is never rendered")
	})

	It("lists the profiles a scope can use, with their place in the active list", func() {
		writeProfile("default", "version: 1\n")
		writeProfile("go", "version: 1\n")
		writeProfile("base", "version: 1\n")
		writeTree(p.UserProfiles, map[string]string{"Bad Name.yml": "", "notes.txt": ""})
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		activate(sc, "user:go", "user:base", "user:gone")
		activate(user, "user:base")

		rows, notices, err := e.Profiles(ctx, sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(2), "default applies to user scope only")
		Expect(rows[0]).To(And(HaveField("Name", "base"), HaveField("Kind", "user"), HaveField("Active", true), HaveField("Position", HaveValue(Equal(2))),
			HaveField("Scope", "project"), HaveField("Path", filepath.Join(p.UserProfiles, "base.yml")), HaveField("Extends", BeEmpty()),
			HaveField("ShadowedBy", BeNil()), HaveField("Of", 3)))
		Expect(rows[1]).To(And(HaveField("Name", "go"), HaveField("Position", HaveValue(Equal(1)))))
		Expect(notices).To(ConsistOf(
			engine.Notice{Level: engine.LevelWarn, Text: "profile user:gone is on but no longer exists → lola profile --project user:gone --off"},
			engine.Notice{Level: engine.LevelWarn, Text: "1 file with an invalid name in " + p.UserProfiles + " is not a profile"}))

		rows, _, err = e.Profiles(ctx, user)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(3))
		Expect(rows[1]).To(And(HaveField("Name", "default"), HaveField("Kind", "default"), HaveField("Active", true), HaveField("Position", BeNil())))
		Expect(rows[2]).To(And(HaveField("Name", "go"), HaveField("Active", false), HaveField("Position", BeNil())))
	})

	It("shows a profile's modules with the layer that wins in each scope", func() {
		a, b, b2 := mod("a", "a", "A"), mod("b", "b", "B"), mod("b2", "b", "B2")
		writeProfile("default", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: "+a+"}\n")
		writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: b, source: "+b+"}\n  - {name: a, source: "+a+"}\n")
		writeTree(filepath.Dir(p.UserManifest), map[string]string{"user.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: b, source: " + b2 + "}\n"})
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules: []\n"})
		activate(sc, "user:go")

		states, _, err := e.ProfileState(ctx, []engine.Scope{sc, user}, "go")
		Expect(err).NotTo(HaveOccurred())
		Expect(states).To(HaveLen(2))
		Expect(states[0].Scope).To(Equal("project"))
		Expect(states[0].Active).To(BeTrue())
		Expect(states[0].Modules).To(Equal([]engine.ProfileModule{
			{Name: "a", From: "profile:user:go", Overrides: []string{}},
			{Name: "b", From: "profile:user:go", Overrides: []string{}}}))
		Expect(states[1].Active).To(BeFalse())
		Expect(states[1].Modules).To(Equal([]engine.ProfileModule{
			{Name: "a", From: "profile:user:go", Overrides: []string{"profile:default"}},
			{Name: "b", From: "user.yml", Overrides: []string{"profile:user:go"}}}), "an inactive profile shows what turning it on would do")
		Expect(installed(user)).To(BeEmpty(), "nothing is written")

		states, _, err = e.ProfileState(ctx, []engine.Scope{sc, user}, "default")
		Expect(err).NotTo(HaveOccurred())
		Expect(states).To(ConsistOf(And(HaveField("ProfileRow.Scope", "user"), HaveField("ProfileRow.Qualified", "default"))))
		_, _, err = e.ProfileState(ctx, []engine.Scope{sc}, "default")
		Expect(err).To(MatchError("default applies to user scope only"))
		Expect(errs.Hints(err)).To(Equal([]string{"see it: lola profile --user default"}))
	})

	It("refuses a profile name that names none, listing those the scope can use", func() {
		writeProfile("go", "version: 1\n")
		writeProfile("default", "version: 1\n")
		for _, name := range []string{"nope", "project:go", "remote:go", "user:nope", "../go", "user:"} {
			_, _, err := e.ProfileState(ctx, []engine.Scope{user}, name)
			Expect(err).To(MatchError("no profile "+name+" for user scope"), name)
			Expect(errs.Hints(err)).To(Equal([]string{"profiles user scope can use: default, user:go"}), name)
		}
		_, _, err := e.ProfileState(ctx, []engine.Scope{sc}, "x\x1b[31m")
		Expect(err).To(MatchError("no profile x?[31m for the project at "+proj), "the name goes through untrusted.Text")
		Expect(os.RemoveAll(p.UserProfiles)).To(Succeed())
		_, _, err = e.ProfileState(ctx, []engine.Scope{user}, "go")
		Expect(errs.Hints(err)).To(Equal([]string{"write one: " + filepath.Join(p.UserProfiles, "<name>.yml") +
			"; lola profile --add fetches a remote one"}))
	})

	It("refuses a profile that does not load, naming the file and line", func() {
		bad := writeProfile("go", "version: 1\nmodules:\n  - {name: a, nope: 1}\n")
		_, _, err := e.ProfileState(ctx, []engine.Scope{user}, "go")
		Expect(err).To(MatchError(ContainSubstring(bad)))
		Expect(err).To(MatchError(ContainSubstring("line 3")))
	})

	It("refuses a profile listed but gone when read, without a panic", func() {
		Expect(os.MkdirAll(p.UserProfiles, 0o755)).To(Succeed())
		gone := filepath.Join(p.UserProfiles, "go.yml")
		Expect(os.Symlink(filepath.Join(tmp, "nowhere.yml"), gone)).To(Succeed())
		var err error
		Expect(func() { _, _, err = e.ProfileState(ctx, []engine.Scope{user}, "go") }).NotTo(Panic())
		Expect(err).To(MatchError(ContainSubstring(gone)))
	})

	It("profile commands refuse a tracked lock.json", func() {
		writeProfile("go", "version: 1\n")
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		activate(sc, "user:go")
		gitIn(proj, "init", "-q")
		gitIn(proj, "add", "-f", ".lola/lock.json")
		tracked := ".lola/lock.json is tracked by git; lola keeps it private to your checkout, since a committed lock.json lets a pull decide what lola deletes"
		_, _, err := e.Profiles(ctx, sc)
		Expect(err).To(MatchError(tracked))
		_, _, err = e.ProfileState(ctx, []engine.Scope{sc}, "go")
		Expect(err).To(MatchError(tracked))
		for _, action := range []engine.ProfileAction{engine.ProfileOn, engine.ProfileOff, engine.ProfileSwitch} {
			_, err = turn(sc, action, "go")
			Expect(err).To(MatchError(tracked), string(action))
		}
		Expect(activeList(sc)).To(Equal([]ledger.Active{{Name: "user:go", By: ledger.ByOn}}))
		gitIn(proj, "rm", "-q", "--cached", ".lola/lock.json")
		_, _, err = e.Profiles(ctx, sc)
		Expect(err).NotTo(HaveOccurred())
	})

	It("turning a profile off never removes a module another layer wants", func() {
		a, c := mod("a", "a", "A"), mod("c", "c", "C")
		writeProfile("default", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: c, source: "+c+"}\n")
		writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: "+a+"}\n  - {name: c, source: "+c+"}\n")
		_, err := sync(user)
		Expect(err).NotTo(HaveOccurred())

		res, err := turn(user, engine.ProfileOn, "go")
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Activation.Profiles).To(Equal([]string{"user:go"}))
		Expect(res.Activation.Named).To(Equal([]string{"user:go"}))
		Expect(exported(res.Activation.Changes)).To(Equal([]engine.LayerChange{
			{Module: "a", Action: "load", From: "profile:user:go", Over: []string{}, Version: a},
			{Module: "c", Action: "replace", From: "profile:user:go", Over: []string{"profile:default"}, Version: c, Was: c}}))
		Expect(activeList(user)).To(Equal([]ledger.Active{{Name: "user:go", By: ledger.ByOn}}))
		Expect(installed(user)).To(Equal(map[string]string{"a": "profile:user:go", "c": "profile:user:go"}))
		Expect(removals).To(BeEmpty())

		res, err = turn(user, engine.ProfileOff, "go")
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Activation.Profiles).To(BeEmpty())
		Expect(exported(res.Activation.Changes)).To(Equal([]engine.LayerChange{
			{Module: "a", Action: "unload", From: "profile:user:go", Over: []string{}, Version: a},
			{Module: "c", Action: "replace", From: "profile:default", Over: []string{}, Version: c, Was: c}}))
		Expect(removals).To(ConsistOf(ConsistOf(And(HaveField("Module", "a"), HaveField("WholeModule", true)))), "only a goes, and it is asked about")
		Expect(installed(user)).To(Equal(map[string]string{"c": "profile:default"}))
		Expect(command(p.Home, "c")).To(Equal("C\n"))
		Expect(filepath.Join(p.Home, ".claude", "commands", "a.md")).NotTo(BeAnExistingFile())
		Expect(activeList(user)).To(BeEmpty())
	})

	It("a refused activation leaves the active list and files unchanged", func() {
		a := mod("a", "a", "A")
		writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: "+a+"}\n")
		writeProfile("bad", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: b, source: "+filepath.Join(mods, "nope")+"}\n")
		_, err := turn(user, engine.ProfileOn, "go")
		Expect(err).NotTo(HaveOccurred())
		before, err := os.ReadFile(p.UserLedger)
		Expect(err).NotTo(HaveOccurred())
		unchanged := func(why string) {
			GinkgoHelper()
			after, err := os.ReadFile(p.UserLedger)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(after)).To(Equal(string(before)), why)
			Expect(command(p.Home, "a")).To(Equal("A\n"), why)
		}
		off := func(confirm func([]engine.Removal) (bool, error)) error {
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: user, Profiles: &engine.ProfileChange{Action: engine.ProfileOff, Names: []string{"go"}},
				ConfirmRemovals: confirm})
			return err
		}

		err = off(func([]engine.Removal) (bool, error) { return false, nil })
		Expect(errors.As(err, new(*engine.RemovalsDeclinedError))).To(BeTrue(), "%v", err)
		unchanged("a No")
		err = off(nil)
		Expect(err).To(MatchError("lola profile needs your confirmation to remove a, and there is no terminal to ask on"))
		Expect(errs.Hints(err)).To(Equal([]string{"see them: lola profile --user user:go --off -n", "then confirm them: lola profile --user user:go --off -y"}),
			"hints name the profile as resolved")
		unchanged("a refusal off a terminal")
		Expect(off(func([]engine.Removal) (bool, error) { return false, context.Canceled })).To(MatchError(context.Canceled))
		unchanged("an interrupt between staging and commit")
		_, err = turn(user, engine.ProfileSwitch, "bad")
		Expect(err).To(MatchError(ContainSubstring("nope")))
		unchanged("an error")
		_, err = turn(user, engine.ProfileOff, "go", "nope")
		Expect(err).To(MatchError("no profile nope for user scope"))
		unchanged("an unknown name")

		// A partial trust answer would commit the new list with planned modules left out.
		writeProfile("bare", "version: 1\nmodules:\n  - {name: a, source: "+a+"}\n")
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules: []\n"})
		asked := false
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOn, Names: []string{"bare"}},
			Trust: func(engine.TrustReport) (engine.TrustAnswer, error) {
				asked = true
				return engine.TrustAnswer{Modules: []string{"a"}}, nil
			}})
		Expect(asked).To(BeTrue(), "the profile's entry installs to project.yml's untrusted targets")
		Expect(err).To(MatchError("lola profile applies all of its changes or none; trust them all, or cancel"))
		Expect(activeList(sc)).To(BeEmpty(), "a partial trust answer")
		Expect(filepath.Join(proj, ".claude", "commands", "a.md")).NotTo(BeAnExistingFile())
	})

	It("an activation applies only what the change moves", func() {
		c := mod("c", "c", "C")
		writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: c, source: "+c+"}\n")
		writeTree(proj, map[string]string{"a/lola.yml": "name: a\n", "a/commands/a.md": "A\n", "b/lola.yml": "name: b\n", "b/commands/b.md": "B\n",
			".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: a}\n"})
		_, err := sync(sc)
		Expect(err).NotTo(HaveOccurred())
		// Pending since: a pulled entry (untrusted) and an edit of a's folder.
		writeTree(proj, map[string]string{"a/commands/a.md": "A edited\n",
			".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: a}\n  - {name: b, source: b}\n"})

		res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOn, Names: []string{"go"}}})
		Expect(err).NotTo(HaveOccurred(), "no trust question about project.yml's pulled entry, which the change does not move")
		Expect(exported(res.Activation.Changes)).To(Equal([]engine.LayerChange{{Module: "c", Action: "load", From: "profile:user:go", Over: []string{}, Version: c}}))
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "1 other change is pending → lola sync --project"}))
		Expect(installed(sc)).To(Equal(map[string]string{"a": "", "c": "profile:user:go"}))
		Expect(command(proj, "a")).To(Equal("A\n"), "a's edited folder waits for lola sync")
		Expect(filepath.Join(proj, ".claude", "commands", "b.md")).NotTo(BeAnExistingFile())
	})

	It("holds at most 64 profiles in an active list", func() {
		var names []string
		for i := range 65 {
			names = append(names, fmt.Sprintf("p%d", i))
			writeProfile(names[i], "version: 1\n")
		}
		_, err := turn(user, engine.ProfileSwitch, names...)
		Expect(err).To(MatchError("at most 64 profiles can be on in a scope"))
		Expect(errs.Hints(err)).To(Equal([]string{"turn some off: lola profile --user <name>… --off"}))
		Expect(turn(user, engine.ProfileSwitch, names[:64]...)).Error().NotTo(HaveOccurred())
		Expect(activeList(user)).To(HaveLen(64))
		_, err = turn(user, engine.ProfileOn, names[64])
		Expect(err).To(MatchError("at most 64 profiles can be on in a scope"))
		Expect(activeList(user)).To(HaveLen(64))
	})

	It("--switch keeps default on and unloads only what nothing wants", func() {
		a, b, c := mod("a", "a", "A"), mod("b", "b", "B"), mod("c", "c", "C")
		writeProfile("default", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: c, source: "+c+"}\n")
		writeProfile("base", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: "+a+"}\n  - {name: c, source: "+c+"}\n")
		writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: b, source: "+b+"}\n")
		writeProfile("docs", "version: 1\n")
		_, err := turn(user, engine.ProfileOn, "base")
		Expect(err).NotTo(HaveOccurred())
		Expect(installed(user)).To(Equal(map[string]string{"a": "profile:user:base", "c": "profile:user:base"}))

		res, err := turn(user, engine.ProfileSwitch, "go", "docs")
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Activation.Profiles).To(Equal([]string{"user:go", "user:docs"}), "in the order typed")
		Expect(activeList(user)).To(Equal([]ledger.Active{{Name: "user:go", By: ledger.BySwitch}, {Name: "user:docs", By: ledger.BySwitch}}))
		Expect(removals).To(ConsistOf(ConsistOf(HaveField("Module", "a"))))
		Expect(installed(user)).To(Equal(map[string]string{"b": "profile:user:go", "c": "profile:default"}))
		Expect(command(p.Home, "c")).To(Equal("C\n"))

		res, err = turn(user, engine.ProfileSwitch, "go", "docs")
		Expect(err).NotTo(HaveOccurred())
		Expect(res.InSync).To(BeTrue())
		Expect(res.Notices).To(ConsistOf(engine.Notice{Level: engine.LevelInfo, Text: "the active profiles are already user:go, user:docs"}))
		Expect(res.Activation.Changes).To(BeEmpty())
	})

	It("refuses default's actions it cannot take, a name given twice, and notes what is already so", func() {
		writeProfile("default", "version: 1\n")
		writeProfile("go", "version: 1\n")
		res, err := turn(user, engine.ProfileOn, "default")
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ConsistOf(engine.Notice{Level: engine.LevelInfo, Text: "default is always on in user scope"}))
		for _, action := range []engine.ProfileAction{engine.ProfileOff, engine.ProfileSwitch} {
			_, err = turn(user, action, "default")
			Expect(err).To(MatchError("default is always on in user scope; lola profile --" + string(action) + " cannot change that"))
			Expect(errs.Hints(err)).To(Equal([]string{"rename or delete " + filepath.Join(p.UserProfiles, "default.yml") + " to turn it off"}))
		}
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		_, err = turn(sc, engine.ProfileOn, "default")
		Expect(err).To(MatchError("default applies to user scope only"))

		_, err = turn(user, engine.ProfileOn, "go", "user:go")
		Expect(err).To(MatchError("user:go is named twice"))
		Expect(errs.Hints(err)).To(Equal([]string{"name each profile once"}))

		Expect(turn(user, engine.ProfileOn, "go")).Error().NotTo(HaveOccurred())
		res, err = turn(user, engine.ProfileOn, "go")
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ConsistOf(engine.Notice{Level: engine.LevelInfo, Text: "user:go is already on (1st of 1)"}))
		Expect(res.InSync).To(BeTrue())
		Expect(turn(user, engine.ProfileOff, "go")).Error().NotTo(HaveOccurred())
		res, err = turn(user, engine.ProfileOff, "go")
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ConsistOf(engine.Notice{Level: engine.LevelInfo, Text: "user:go is not on"}))
	})

	It("turns off an active profile whose file is gone, by either name", func() {
		a := mod("a", "a", "A")
		gone := writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: "+a+"}\n")
		for _, name := range []string{"user:go", "go"} {
			Expect(turn(user, engine.ProfileOn, "go")).Error().NotTo(HaveOccurred())
			Expect(os.Remove(gone)).To(Succeed())
			res, err := turn(user, engine.ProfileOff, name)
			Expect(err).NotTo(HaveOccurred(), name)
			Expect(res.Activation.Changes).To(ConsistOf(HaveField("Action", "unload")))
			Expect(activeList(user)).To(BeEmpty())
			Expect(installed(user)).To(BeEmpty())
			writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: "+a+"}\n")
		}
	})

	It("turns a profile on in a project without .lola, writing the list with the apply", func() {
		a := mod("a", "a", "A")
		writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: "+a+"}\n")
		res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{DryRun: true},
			Profiles: &engine.ProfileChange{Action: engine.ProfileOn, Names: []string{"go"}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.ToApply).To(Equal("lola profile --project user:go --on"))
		Expect(res.Activation.Changes).To(ConsistOf(HaveField("Action", "load")))
		Expect(filepath.Join(proj, ".lola")).NotTo(BeADirectory(), "a dry run writes nothing")

		_, err = turn(sc, engine.ProfileOn, "go")
		Expect(err).NotTo(HaveOccurred())
		Expect(activeList(sc)).To(Equal([]ledger.Active{{Name: "user:go", By: ledger.ByOn}}))
		Expect(filepath.Join(proj, ".lola", ".gitignore")).To(BeARegularFile())
		Expect(command(proj, "a")).To(Equal("A\n"))
		Expect(filepath.Join(proj, ".lola", "project.yml")).NotTo(BeAnExistingFile())
	})

	It("names every module a sync takes, from every layer, without asking git", func() {
		c := mod("c", "c", "C")
		writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: c, source: "+c+"}\n")
		writeTree(proj, map[string]string{"a/lola.yml": "name: a\n", "a/commands/a.md": "A\n",
			".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: a}\n"})
		activate(sc, "user:go")
		names, err := e.SyncNames(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(names).To(Equal([]string{"a", "c"}), "not installed yet, from project.yml and from a profile")

		_, err = sync(sc)
		Expect(err).NotTo(HaveOccurred())
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules: []\n"})
		gitIn(proj, "init", "-q")
		gitIn(proj, "add", "-f", ".lola/lock.json")
		names, err = e.SyncNames(sc)
		Expect(err).NotTo(HaveOccurred(), "no git check: a tracked lock.json is the completed command's to refuse")
		Expect(names).To(Equal([]string{"a", "c"}), "a module installed but no longer listed")
	})

	It("resolves a profile's relative folder source against the profile's directory", func() {
		mod("a", "a", "A")
		rel, err := filepath.Rel(p.UserProfiles, filepath.Join(mods, "a"))
		Expect(err).NotTo(HaveOccurred())
		writeProfile("default", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: "+rel+"}\n")
		_, err = sync(user)
		Expect(err).NotTo(HaveOccurred())
		led, err := e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Modules["a"].Source).To(Equal(filepath.Join(mods, "a")))
		Expect(command(p.Home, "a")).To(Equal("A\n"))
	})
})

func sourceSpecGit(url string) source.Spec { return source.Spec{Kind: source.KindGit, Location: url} }
