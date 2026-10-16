package engine_test

import (
	"context"
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
		notice, hint, err := e.InstallCoaching(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(notice).To(Equal("2 modules in profile:default are not installed → lola sync --user"))
		Expect(hint).To(Equal("install what profile:default lists: lola sync --user"))

		res, err := e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: a}}, engine.Options{Targets: []string{"claude-code", "opencode"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn, Text: "a: user.yml overrides profile:default"}))
		Expect(installed(user)).To(Equal(map[string]string{"a": ""}))

		writeTree(filepath.Dir(p.UserManifest), map[string]string{"user.yml": "version: 1\ntargets: [claude-code]\nmodules:\n" +
			"  - {name: a, source: " + a + ", targets: [claude-code, opencode]}\n  - {name: b, source: ../b}\n"})
		notice, hint, err = e.InstallCoaching(user)
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
			Text: "example: --update leaves the pin profile:default sets; edit the pin in " + def}))
		Expect(pinned()).To(And(HaveField("Commit", first), HaveField("Digest", v1)), "the profile's pin is installed")
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
