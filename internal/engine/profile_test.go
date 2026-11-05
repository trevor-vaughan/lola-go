package engine_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	// activate turns name on the way lola profile --on records it, in lock.json; "" turns off
	// whatever is on.
	activate := func(s engine.Scope, name string) {
		GinkgoHelper()
		led, err := e.Ledger(s)
		Expect(err).NotTo(HaveOccurred())
		led.Profile = nil
		if name != "" {
			led.Profile = &ledger.Active{Name: name, By: ledger.ByOn}
		}
		Expect(led.Save(ledgerPath(s))).To(Succeed())
	}
	sync := func(s engine.Scope) (*engine.SyncResult, error) {
		return e.Sync(ctx, engine.SyncRequest{Scope: s, ConfirmRemovals: func(r []engine.Removal) (bool, error) {
			removals = append(removals, r)
			return true, nil
		}, Trust: func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }})
	}
	// turn runs lola profile's action on name in s, confirming removals as -y does.
	turn := func(s engine.Scope, action engine.ProfileAction, name string) (*engine.SyncResult, error) {
		return e.Sync(ctx, engine.SyncRequest{Scope: s, Profiles: &engine.ProfileChange{Action: action, Name: name},
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
	activeProfile := func(s engine.Scope) *ledger.Active {
		GinkgoHelper()
		led, err := e.Ledger(s)
		Expect(err).NotTo(HaveOccurred())
		return led.Profile
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

	It("the winning entry's picks replace a lower layer's whole; the notice says which picks were dropped", func() {
		kit := filepath.Join(mods, "kit")
		writeTree(kit, map[string]string{"lola.yml": "name: kit\n", "skills/a/SKILL.md": "---\ndescription: A\n---\nA\n",
			"skills/b/SKILL.md": "---\ndescription: B\n---\nB\n"})
		skillB := filepath.Join(p.Home, ".claude", "skills", "b", "SKILL.md")
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + kit + ", skip: [skill/b]}\n"})
		_, err := sync(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(skillB).NotTo(BeAnExistingFile(), "default's entry skips it")

		writeProfile("work", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: "+kit+"}\n")
		activate(user, "user:work")
		res, err := sync(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(skillB).To(BeAnExistingFile(), "the winner's picks replace default's whole")
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
			Text: "kit: profile work overrides default (base); kit's picks from default (base) are dropped: the entry from profile work sets none"}))

		writeProfile("work", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: "+kit+", only: [skill/a]}\n")
		res, err = sync(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(skillB).NotTo(BeAnExistingFile())
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
			Text: "kit: profile work overrides default (base); kit's picks from default (base) are dropped: the entry from profile work sets its own"}))

		writeProfile("work", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: "+kit+", skip: [skill/b]}\n")
		res, err = sync(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).NotTo(ContainElement(HaveField("Text", HavePrefix("kit:"))), "the same picks are not dropped: identical entries are silent")
	})

	It("the same picks in another order are not said to be dropped", func() {
		kit := filepath.Join(mods, "kit")
		writeTree(kit, map[string]string{"lola.yml": "name: kit\n", "skills/a/SKILL.md": "---\ndescription: A\n---\nA\n",
			"skills/b/SKILL.md": "---\ndescription: B\n---\nB\n", "skills/c/SKILL.md": "---\ndescription: C\n---\nC\n"})
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + kit + ", only: [skill/a, skill/b]}\n"})
		writeProfile("work", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: "+kit+", only: [skill/b, skill/a]}\n")
		activate(user, "user:work")
		res, err := sync(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(HaveField("Text", HavePrefix("kit: profile work overrides default (base)"))))
		Expect(res.Notices).NotTo(ContainElement(HaveField("Text", ContainSubstring("picks"))))
	})

	It("says the picks dropped when the entry replaced holds only an only: list", func() {
		kit := filepath.Join(mods, "kit")
		writeTree(kit, map[string]string{"lola.yml": "name: kit\n", "skills/a/SKILL.md": "---\ndescription: A\n---\nA\n",
			"skills/b/SKILL.md": "---\ndescription: B\n---\nB\n"})
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + kit + ", only: [skill/a]}\n"})
		writeProfile("work", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: "+kit+"}\n")
		activate(user, "user:work")
		res, err := sync(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
			Text: "kit: profile work overrides default (base); kit's picks from default (base) are dropped: the entry from profile work sets none"}))

		writeProfile("work", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: "+kit+", only: [skill/b]}\n")
		res, err = sync(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
			Text: "kit: profile work overrides default (base); kit's picks from default (base) are dropped: the entry from profile work sets its own"}))
	})

	It("the winning entry's vars replace a lower layer's whole; a winner without vars renders defaults; the notice says which vars were dropped", func() {
		kit := filepath.Join(mods, "kit")
		writeTree(kit, map[string]string{"lola.yml": "name: kit\nvars:\n  language: go\n  strict: false\n",
			"commands/kit.md.liquid": "{{ vars.language }} {{ vars.strict }}\n"})
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + kit + ", vars: {language: rust, strict: true}}\n"})
		_, err := sync(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(command(p.Home, "kit")).To(Equal("rust true\n"), "default's entry sets both")

		writeProfile("work", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: "+kit+", vars: {strict: true}}\n")
		activate(user, "user:work")
		res, err := sync(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(command(p.Home, "kit")).To(Equal("go true\n"), "the winner's vars replace default's whole: language is the default again")
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
			Text: "kit: profile work overrides default (base); kit's vars from default (base) are dropped: the entry from profile work sets its own"}))

		writeProfile("work", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: "+kit+"}\n")
		res, err = sync(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(command(p.Home, "kit")).To(Equal("go false\n"), "a winner without vars renders the defaults")
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
			Text: "kit: profile work overrides default (base); kit's vars from default (base) are dropped: the entry from profile work sets none"}))

		writeProfile("work", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: "+kit+", vars: {language: rust, strict: true}}\n")
		res, err = sync(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).NotTo(ContainElement(HaveField("Text", HavePrefix("kit:"))),
			"the same vars are not dropped: identical entries are silent")
	})

	It("an override refusal of a profile's entry names the profile's layer", func() {
		kit := filepath.Join(mods, "kit")
		writeTree(kit, map[string]string{"lola.yml": "name: kit\nvars:\n  language: go\n", "commands/kit.md.liquid": "{{ vars.language }}\n"})
		writeProfile("work", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: "+kit+", vars: {lang: rust}}\n")
		activate(user, "user:work")
		_, err := sync(user)
		Expect(err).To(MatchError("profile:user:work: modules[kit].vars.lang: kit declares no var lang; it declares language"))
	})

	It("a later layer replaces an earlier entry whole, with a notice", func() {
		a, b, b2, c := mod("a", "a", "A"), mod("b", "b", "B from go"), mod("b2", "b", "B from default"), mod("c", "c", "C")
		writeTree(a, map[string]string{"rules/style.md": "Prefer small diffs.\n"})
		writeProfile("default", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: "+a+", rules: false}\n  - {name: c, source: "+c+"}\n"+
			"  - {name: b, source: "+b2+"}\n")
		writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: "+a+"}\n  - {name: b, source: "+b+"}\n  - {name: c, source: "+c+"}\n")
		activate(user, "user:go")

		res, err := sync(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElements(
			engine.Notice{Level: engine.LevelWarn, Text: "a: profile go overrides default (base)"},
			engine.Notice{Level: engine.LevelWarn, Text: "b: profile go overrides default (base) (" + b + " over " + b2 + ")"},
		))
		Expect(res.Notices).NotTo(ContainElement(HaveField("Text", HavePrefix("c:"))), "identical entries are silent")
		Expect(installed(user)).To(Equal(map[string]string{"a": "profile:user:go", "b": "profile:user:go", "c": "profile:user:go"}), "an identical entry still wins from the later layer")
		Expect(command(p.Home, "b")).To(Equal("B from go\n"))
		Expect(filepath.Join(p.Home, ".claude", "rules", "lola", "a", "style.md")).To(BeARegularFile(),
			"go's entry replaces default's whole: default's rules: false is not merged in")
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "default.yml"))).To(Equal([]byte("version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: "+a+
			", rules: false}\n  - {name: c, source: "+c+"}\n  - {name: b, source: "+b2+"}\n")), "a sync never writes the active profile's entries into default.yml")
	})

	It("the active profile's entry wins over the scope's manifest, at both scopes", func() {
		b, b2 := mod("b", "b", "B from go"), mod("b2", "b", "B from default")
		writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: b, source: "+b+"}\n")
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: b, source: " + b2 + "}\n"})
		writeTree(proj, map[string]string{"b/lola.yml": "name: b\n", "b/commands/b.md": "B from project.yml\n",
			".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: b, source: b}\n"})
		activate(user, "user:go")
		activate(sc, "user:go")

		res, err := sync(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn, Text: "b: profile go overrides default (base) (" + b + " over " + b2 + ")"}))
		Expect(installed(user)).To(Equal(map[string]string{"b": "profile:user:go"}))
		Expect(command(p.Home, "b")).To(Equal("B from go\n"))

		res, err = sync(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn, Text: "b: profile go overrides project.yml (base) (" + b + " over b)"}))
		Expect(installed(sc)).To(Equal(map[string]string{"b": "profile:user:go"}))
		Expect(command(proj, "b")).To(Equal("B from go\n"))
	})

	It("a sync of a name no layer lists names the manifest as the base and the other layers as profiles", func() {
		a, b, c := mod("a", "a", "A"), mod("b", "b", "B"), mod("c", "c", "C")
		writeProfile("default", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: c, source: "+c+"}\n  - {name: b, source: "+b+"}\n")
		writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: "+a+"}\n")
		activate(user, "user:go")

		_, err := e.Sync(ctx, engine.SyncRequest{Scope: user, Modules: []string{"nope"}})
		Expect(err).To(MatchError("nope is neither in profile:default or a profile (profile:user:go) nor installed here"))
		Expect(errs.Hints(err)).To(Equal([]string{"modules in profile:default or a profile (profile:user:go): a, b, c"}))
	})

	It("an install with --profile base of a module the active profile lists writes default.yml and leaves the profile's entry installed", func() {
		// One source at two tags: the cache holds one source per module name, so both entries name it.
		work, bare := filepath.Join(tmp, "work"), filepath.Join(tmp, "srv", "a.git")
		writeTree(work, map[string]string{"lola.yml": "name: a\n", "commands/a.md": "A from go\n"})
		gitIn(work, "init", "-q", "-b", "main")
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "go")
		gitIn(work, "tag", "v1")
		writeTree(work, map[string]string{"commands/a.md": "A from the install\n"})
		gitIn(work, "commit", "-q", "-am", "install")
		gitIn(work, "tag", "v2")
		gitIn(tmp, "clone", "-q", "--bare", work, bare)
		url := testenv.ServeGit(filepath.Join(tmp, "srv")) + "/a.git"
		writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: "+url+", ref: v1}\n")
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules: []\n"})
		activate(user, "user:go")

		res, err := e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: url, Ref: "v2"}}, engine.Options{Targets: []string{"claude-code"},
			Target: engine.TargetRequest{Profile: "base"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
			Text: "a: profile go's entry wins; this change to default (base) applies when it is off"}))
		Expect(installed(user)).To(BeEmpty(), "nothing of default's entry is installed while go's wins")
		man, err := os.ReadFile(filepath.Join(p.UserProfiles, "default.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(man)).To(ContainSubstring("ref: v2"), "the change is written")

		res2, err := sync(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(installed(user)).To(Equal(map[string]string{"a": "profile:user:go"}))
		Expect(command(p.Home, "a")).To(Equal("A from go\n"))
		Expect(res2.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn, Text: "a: profile go overrides default (base) (v1 over v2)"}))

		_, err = turn(user, engine.ProfileOff, "go")
		Expect(err).NotTo(HaveOccurred())
		Expect(installed(user)).To(Equal(map[string]string{"a": "profile:default"}))
		Expect(command(p.Home, "a")).To(Equal("A from the install\n"), "the change applies once go is off")
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
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: c, source: " + c + "}\n"})
		activate(user, "user:go")
		_, err := sync(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(installed(user)).To(HaveKeyWithValue("a", "profile:user:go"))

		Expect(os.Remove(gone)).To(Succeed())
		missing := engine.Notice{Level: engine.LevelWarn, Text: "profile user:go is on but no longer exists → lola profile --user user:go --off"}
		rows, notices, err := e.Status(ctx, user, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(notices).To(ConsistOf(missing))
		Expect(rows).To(ContainElement(And(HaveField("Name", "a"), HaveField("From", "profile:user:go"), HaveField("Status", "ok"))),
			"list keeps the layer lock.json recorded for a module no layer wants any more")
		res, err := sync(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(missing))
		Expect(removals).To(ConsistOf(ConsistOf(And(HaveField("Module", "a"), HaveField("WholeModule", true)))),
			"its modules go as any unwanted module does, with the usual confirmation")
		Expect(installed(user)).To(Equal(map[string]string{"c": "profile:default"}))
		led, err := e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Profile).To(Equal(&ledger.Active{Name: "user:go", By: ledger.ByOn}), "a sync never changes the profile on")
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
		writeProfile("default", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: c, source: "+c+"}\n  - {name: a, source: "+a+"}\n")
		rows, notices, err := e.Status(ctx, user, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(notices).To(Equal([]engine.Notice{{Level: engine.LevelInfo, Text: "2 modules in profile:default are not installed → lola sync --user"}}))
		Expect(rows).To(ConsistOf(
			And(HaveField("Name", "a"), HaveField("From", "profile:default"), HaveField("Status", "not_installed"), HaveField("Target", "claude-code")),
			And(HaveField("Name", "c"), HaveField("From", "profile:default"), HaveField("Status", "not_installed"), HaveField("Target", "claude-code")),
		))
		_, err = sync(user)
		Expect(err).NotTo(HaveOccurred())
		rows, _, err = e.Status(ctx, user, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(ConsistOf(
			And(HaveField("Name", "a"), HaveField("From", "profile:default"), HaveField("Status", "ok")),
			And(HaveField("Name", "c"), HaveField("From", "profile:default"), HaveField("Status", "ok")),
		))
	})

	It("sync installs a profile's module at project scope as installed, and asks before one no layer lists goes", func() {
		a := mod("a", "a", "A")
		writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: "+a+"}\n")
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		activate(sc, "user:go")
		_, err := sync(sc)
		Expect(err).NotTo(HaveOccurred())
		shipped := filepath.Join(proj, ".claude", "commands", "a.md")
		Expect(os.Remove(shipped)).To(Succeed())

		res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc})
		Expect(err).NotTo(HaveOccurred(), "a profile's entry needs no trust record")
		Expect(res.Findings).NotTo(ContainElement(HaveField("Severity", target.SeverityWarn)))
		Expect(shipped).To(BeARegularFile())
		Expect(installed(sc)).To(Equal(map[string]string{"a": "profile:user:go"}))

		// The profile names no targets: its entry installs to project.yml's, which a pull changes.
		writeProfile("go", "version: 1\nmodules:\n  - {name: a, source: "+a+"}\n")
		writeTree(filepath.Join(proj, ".lola"), map[string]string{"project.yml": "version: 1\ntargets: [claude-code]\n"})
		_, err = sync(sc)
		Expect(err).NotTo(HaveOccurred())
		writeTree(filepath.Join(proj, ".lola"), map[string]string{"project.yml": "version: 1\ntargets: [claude-code, opencode]\n"})
		Expect(os.Remove(shipped)).To(Succeed())
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc})
		refusal, ok := errors.AsType[*engine.TrustRefusalError](err)
		Expect(ok).To(BeTrue(), "%v", err)
		Expect(refusal.Report.Targets).NotTo(BeNil(), "the targets a's entry installs to changed: %v", err)
		Expect(err).To(MatchError("lola sync needs your trust for changes to .lola/project.yml, and there is no terminal to ask on"))
		Expect(shipped).NotTo(BeAnExistingFile(), "left as installed")

		activate(sc, "")
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }})
		refusal, ok = errors.AsType[*engine.TrustRefusalError](err)
		Expect(ok).To(BeTrue(), "%v", err)
		Expect(refusal.Removals).To(ConsistOf(And(HaveField("Module", "a"), HaveField("WholeModule", true))))
		Expect(shipped).NotTo(BeAnExistingFile())
		Expect(installed(sc)).To(Equal(map[string]string{"a": "profile:user:go"}), "nothing removed unasked")
	})

	It("uninstall refuses a module an active profile wants", func() {
		a, c := mod("a", "a", "A"), mod("c", "c", "C")
		writeProfile("default", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: "+a+"}\n  - {name: c, source: "+c+"}\n")
		writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: "+a+"}\n")
		activate(user, "user:go")
		_, err := sync(user)
		Expect(err).NotTo(HaveOccurred())
		before, err := os.ReadFile(p.UserLedger)
		Expect(err).NotTo(HaveOccurred())

		_, err = e.Uninstall(ctx, user, []string{"c"}, engine.Options{})
		Expect(err).To(MatchError(HavePrefix("c comes from default (base); lola uninstall needs to know whether to remove it there")),
			"default's module is not the profile on's to remove: the run asks, and off a terminal refuses")
		Expect(os.ReadFile(p.UserLedger)).To(Equal(before), "a refusal changes nothing")

		res, err := e.Uninstall(ctx, user, []string{"a"}, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(HaveField("Text", HavePrefix("a stays: default (base) lists it too"))),
			"the profile on drops its entry; default still wants a, so it stays installed")
		Expect(installed(user)).To(Equal(map[string]string{"a": "profile:user:go", "c": "profile:default"}))
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
		Expect(res.Notices).NotTo(ContainElement(HaveField("Text", ContainSubstring("overrides"))), "the install writes default's own entry")
		Expect(installed(user)).To(Equal(map[string]string{"a": "profile:default"}))

		writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: "+a+"}\n  - {name: b, source: ../b}\n")
		activate(user, "user:go")
		notice, hint, err = e.InstallCoaching(ctx, user)
		Expect(err).NotTo(HaveOccurred())
		Expect(notice).To(Equal("2 modules in profile:default and profile:user:go are not installed → lola sync --user"))
		Expect(hint).To(Equal("install what profile:default and profile:user:go list: lola sync --user"))
		res, err = e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: a}}, engine.Options{Targets: []string{"claude-code"},
			Target: engine.TargetRequest{Profile: "base"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
			Text: "a: profile go's entry wins; this change to default (base) applies when it is off"}))

		bad := writeProfile("go", "version: 1\nmodules: oops\n")
		_, err = e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: c}}, engine.Options{Targets: []string{"claude-code"}})
		Expect(err).To(MatchError(HavePrefix(bad+":2: ")), "an active profile that does not load refuses the install")
		Expect(e.Store.Has("c")).To(BeFalse(), "before anything is fetched")
	})

	It("refuses a run whose active profile does not load, naming the file and line", func() {
		bad := writeProfile("go", "version: 1\nmodules:\n  - {name: a, source: ../a, colour: red}\n")
		activate(user, "user:go")
		_, err := sync(user)
		Expect(err).To(MatchError(HavePrefix(bad + `:3: unknown key "colour" (a profile entry has: `)))
	})

	It("Prof-P2-d: an unknown target in a profile's targets: names the profile file and the line", func() {
		a := mod("a", "a", "A")
		bad := writeProfile("go", "version: 1\ntargets: [nope]\nmodules:\n  - {name: a, source: "+a+"}\n")
		activate(user, "user:go")
		_, err := sync(user)
		Expect(err).To(MatchError(HavePrefix(bad + `:2: `)))
		Expect(err).To(MatchError(ContainSubstring(`unknown target "nope"`)))
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
		Expect(err).To(MatchError(HavePrefix("no default.yml at ")), "nothing any layer wants: still refused")

		writeProfile("work", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: c, source: "+c+"}\n")
		activate(user, "user:work")
		_, err = sync(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(installed(user)).To(Equal(map[string]string{"c": "profile:user:work"}))
		Expect(filepath.Join(p.UserProfiles, "default.yml")).NotTo(BeAnExistingFile(), "a sync creates no default.yml")

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
		Expect(err).To(MatchError("example: its pin is in " + def + ", which lola never writes pins into"))
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
		b, err := os.ReadFile(filepath.Join(proj, ".claude", "commands", "ship.md"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).NotTo(ContainSubstring("attacker"))
	})

	It("never installs the commit a project's lock.json names for a project profile's entry recorded unpinned", func() {
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
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, ConfirmRemovals: func([]engine.Removal) (bool, error) { return true, nil }})
		refusal, ok := errors.AsType[*engine.TrustRefusalError](err)
		Expect(ok).To(BeTrue(), "%v", err)
		Expect(refusal.Report.Changes).To(ConsistOf(HaveField("Module", "example")), "the pin it would write needs trust")
		Expect(filepath.Join(proj, ".claude", "commands", "ship.md")).NotTo(BeAnExistingFile(), "nothing rendered unasked")
		res, err := sync(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Pins).To(ConsistOf(And(HaveField("Module", "example"), HaveField("Commit", gitIn(work, "rev-parse", "main")))),
			"sync resolves the entry's ref, never the commit lock.json names")
		b, err := os.ReadFile(filepath.Join(proj, ".claude", "commands", "ship.md"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).NotTo(ContainSubstring("attacker"), "the fork's ship.md is never rendered")
	})

	It("lists a base.yml in a profiles directory as reserved, never as a profile", func() {
		writeProfile("go", "version: 1\n")
		mine := writeProfile("base", "version: 1\n")
		writeTree(proj, map[string]string{".lola/profiles/base.yml": "version: 1\n"})
		theirs := filepath.Join(proj, ".lola", "profiles", "base.yml")

		rows, notices, err := e.Profiles(ctx, sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(ConsistOf(HaveField("Name", "go")))
		Expect(notices).To(Equal([]engine.Notice{
			{Level: engine.LevelWarn, Text: "base is reserved for the scope's base; rename " + mine},
			{Level: engine.LevelWarn, Text: "base is reserved for the scope's base; rename " + theirs},
		}))
		_, err = turn(user, engine.ProfileSwitch, "user:base")
		Expect(err).To(MatchError("user:base is no profile: base is reserved for the scope's base, so " + mine + " is not a profile"))
		renamed := filepath.Join(filepath.Dir(mine), "base-old.yml")
		Expect(errs.Hints(err)).To(Equal([]string{"rename " + mine + " to " + renamed + " to use it as the profile user:base-old"}))
		_, err = turn(sc, engine.ProfileSwitch, "project:base")
		Expect(err).To(MatchError("project:base is no profile: base is reserved for the scope's base, so " + theirs + " is not a profile"))
		_, err = turn(user, engine.ProfileSwitch, "remote:base")
		Expect(err).To(MatchError("remote:base is no profile: base is reserved for the scope's base"))
		Expect(errs.Hints(err)).To(Equal([]string{"base names the scope's base: default.yml at user scope, project.yml in a project"}))

		kept := writeProfile("base-old", "version: 1\ndescription: mine already\n")
		_, err = turn(user, engine.ProfileSwitch, "user:base")
		renamed = filepath.Join(filepath.Dir(mine), "base-old-2.yml")
		Expect(errs.Hints(err)).To(Equal([]string{"rename " + mine + " to " + renamed + " to use it as the profile user:base-old-2"}), "base-old is taken")
		Expect(os.Rename(mine, renamed)).To(Succeed(), "the hint, as printed")
		Expect(turn(user, engine.ProfileSwitch, "user:base-old-2")).Error().NotTo(HaveOccurred())
		Expect(activeProfile(user)).To(HaveField("Name", "user:base-old-2"))
		Expect(os.ReadFile(kept)).To(Equal([]byte("version: 1\ndescription: mine already\n")), "nothing is lost")

		for _, d := range []string{"base", "base-old"} {
			Expect(os.MkdirAll(filepath.Join(p.RemoteProfiles, d), 0o700)).To(Succeed())
		}
		_, err = turn(user, engine.ProfileSwitch, "remote:base")
		Expect(errs.Hints(err)).To(Equal([]string{"rename " + filepath.Join(p.RemoteProfiles, "base") + " to " +
			filepath.Join(p.RemoteProfiles, "base-old-3") + " to use it as the profile remote:base-old-3"}),
			"never into a directory that is there, nor a name a profile of yours has")
	})

	It("lists the profiles a scope can use, with the state of each", func() {
		writeProfile("default", "version: 1\n")
		writeProfile("go", "version: 1\n")
		writeProfile("baseline", "version: 1\n")
		writeTree(p.UserProfiles, map[string]string{"Bad Name.yml": "", "notes.txt": ""})
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		activate(sc, "user:go")
		activate(user, "user:gone")

		rows, notices, err := e.Profiles(ctx, sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(2), "default applies to user scope only")
		Expect(rows[0]).To(And(HaveField("Name", "baseline"), HaveField("Kind", "user"), HaveField("Active", false), HaveField("State", "off"),
			HaveField("Scope", "project"), HaveField("Path", filepath.Join(p.UserProfiles, "baseline.yml")), HaveField("Members", BeEmpty()),
			HaveField("Bundle", false), HaveField("ShadowedBy", BeNil())))
		Expect(rows[1]).To(And(HaveField("Name", "go"), HaveField("Active", true), HaveField("State", "on")))
		Expect(notices).To(ConsistOf(engine.Notice{Level: engine.LevelWarn, Text: "1 file with an invalid name in " + p.UserProfiles + " is not a profile"}))

		rows, notices, err = e.Profiles(ctx, user)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(3))
		Expect(rows[1]).To(And(HaveField("Name", "default"), HaveField("Kind", "default"), HaveField("Active", true), HaveField("State", "base")))
		Expect(rows[2]).To(And(HaveField("Name", "go"), HaveField("Active", false), HaveField("State", "off")))
		Expect(notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
			Text: "profile user:gone is on but no longer exists → lola profile --user user:gone --off"}))
	})

	It("lists the bundle on, the profiles and nested bundles it reaches as members, each bundle's members, and a file that does not load with a notice", func() {
		writeProfile("default", "version: 1\n")
		writeProfile("go", "version: 1\n")
		writeProfile("lint", "version: 1\n")
		writeProfile("docs", "version: 1\n")
		writeProfile("go-all", "version: 1\nbundle: [go, lint]\n")
		writeProfile("work", "version: 1\nbundle: [go-all, docs]\n")
		writeProfile("idle", "version: 1\nbundle: [docs]\n")
		writeProfile("loop", "version: 1\nbundle: [loop2]\n")
		writeProfile("loop2", "version: 1\nbundle: [loop]\n")
		broken := writeProfile("broken", "version: 1\nbundle: [go]\nmodules:\n  - {name: n, source: "+mod("n", "n", "N")+"}\n")
		activate(user, "user:work")

		rows, notices, err := e.Profiles(ctx, user)
		Expect(err).NotTo(HaveOccurred(), "a file that does not load never refuses the listing")
		states := map[string]string{}
		members := map[string][]string{}
		for _, r := range rows {
			states[r.Name] = r.State
			if r.Bundle {
				members[r.Name] = r.Members
			}
		}
		Expect(states).To(Equal(map[string]string{"broken": "off", "default": "base", "docs": "member", "go": "member", "go-all": "member",
			"idle": "off", "lint": "member", "loop": "off", "loop2": "off", "work": "on"}))
		Expect(members).To(Equal(map[string][]string{"go-all": {"user:go", "user:lint"}, "idle": {"user:docs"}, "loop": {}, "loop2": {},
			"work": {"user:go", "user:lint", "user:docs"}}))
		Expect(rows).To(ContainElement(And(HaveField("Name", "go"), HaveField("In", "work"), HaveField("Active", true))))
		Expect(notices).To(ContainElements(
			engine.Notice{Level: engine.LevelWarn, Text: "user:broken does not load: " + broken + ":2: broken has both bundle: and modules:; " +
				"a bundle combines profiles and a profile lists modules → lola profile --user broken --split"},
			engine.Notice{Level: engine.LevelWarn, Text: "user:loop does not expand: " + filepath.Join(p.UserProfiles, "loop2.yml") +
				":2: bundle: loop closes a cycle: loop → loop2 → loop"}))

		states2, _, err := e.ProfileState(ctx, []engine.Scope{user}, "go-all")
		Expect(err).NotTo(HaveOccurred())
		Expect(states2[0].State).To(Equal("member"), "a member shows what it gives in the bundle on")
		Expect(states2[0].Members).To(Equal([]string{"user:go", "user:lint"}))
	})

	It("qualifies a bundle and its members in a listing when a user and a project profile share a name", func() {
		writeProfile("go", "version: 1\n")
		writeProfile("work", "version: 1\nbundle: [go]\n")
		writeTree(proj, map[string]string{".lola/profiles/go.yml": "version: 1\n", ".lola/profiles/work.yml": "version: 1\n"})
		activate(sc, "user:work")

		rows, _, err := e.Profiles(ctx, sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(ConsistOf(
			And(HaveField("Name", "go"), HaveField("Kind", "user"), HaveField("State", "member"), HaveField("In", "user:work")),
			And(HaveField("Name", "go"), HaveField("Kind", "project"), HaveField("State", "off"), HaveField("In", "")),
			And(HaveField("Name", "work"), HaveField("Kind", "user"), HaveField("State", "on"), HaveField("Bundle", true), HaveField("Members", Equal([]string{"user:go"}))),
			And(HaveField("Name", "work"), HaveField("Kind", "project"), HaveField("State", "off"), HaveField("Bundle", false))))
	})

	It("shows a profile's modules with the layer that wins in each scope", func() {
		a, b, b2 := mod("a", "a", "A"), mod("b", "b", "B"), mod("b2", "b", "B2")
		writeProfile("default", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: "+a+"}\n  - {name: b, source: "+b2+"}\n")
		writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: b, source: "+b+"}\n  - {name: a, source: "+a+"}\n")
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules: []\n"})
		activate(sc, "user:go")

		states, _, err := e.ProfileState(ctx, []engine.Scope{sc, user}, "go")
		Expect(err).NotTo(HaveOccurred())
		Expect(states).To(HaveLen(2))
		Expect(states[0].Scope).To(Equal("project"))
		Expect(states[0].Active).To(BeTrue())
		Expect(states[0].Modules).To(Equal([]engine.ProfileModule{
			{Name: "a", From: "profile:user:go", Overrides: []string{}, Only: []string{}, Skip: []string{}},
			{Name: "b", From: "profile:user:go", Overrides: []string{}, Only: []string{}, Skip: []string{}}}))
		Expect(states[1].Active).To(BeFalse())
		Expect(states[1].Modules).To(Equal([]engine.ProfileModule{
			{Name: "a", From: "profile:user:go", Overrides: []string{"profile:default"}, Only: []string{}, Skip: []string{}},
			{Name: "b", From: "profile:user:go", Overrides: []string{"profile:default"}, Only: []string{}, Skip: []string{}}}), "an inactive profile shows what switching to it would do")
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
		Expect(err).To(MatchError(ContainSubstring(`:3: unknown key "nope"`)))
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
		tracked := ".lola/lock.json is tracked by git; lola keeps it private to your checkout, since a committed lock.json lets a pull decide what lola deletes; if you never installed with lola in this checkout, untrack and delete it, then run lola sync --project"
		_, _, err := e.Profiles(ctx, sc)
		Expect(err).To(MatchError(tracked))
		_, _, err = e.ProfileState(ctx, []engine.Scope{sc}, "go")
		Expect(err).To(MatchError(tracked))
		for _, action := range []engine.ProfileAction{engine.ProfileOn, engine.ProfileOff, engine.ProfileSwitch} {
			_, err = turn(sc, action, "go")
			Expect(err).To(MatchError(tracked), string(action))
		}
		Expect(activeProfile(sc)).To(Equal(&ledger.Active{Name: "user:go", By: ledger.ByOn}))
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
		Expect(res.Activation.Profile).To(HaveValue(Equal("user:go")))
		Expect(res.Activation.Named).To(Equal("user:go"))
		Expect(exported(res.Activation.Changes)).To(Equal([]engine.LayerChange{
			{Module: "a", Action: "load", From: "profile:user:go", Over: []string{}, Version: a},
			{Module: "c", Action: "replace", From: "profile:user:go", Over: []string{"profile:default"}, Version: c, Was: c}}))
		Expect(activeProfile(user)).To(Equal(&ledger.Active{Name: "user:go", By: ledger.ByOn}))
		Expect(installed(user)).To(Equal(map[string]string{"a": "profile:user:go", "c": "profile:user:go"}))
		Expect(removals).To(BeEmpty())

		res, err = turn(user, engine.ProfileOff, "go")
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Activation.Profile).To(BeNil())
		Expect(exported(res.Activation.Changes)).To(Equal([]engine.LayerChange{
			{Module: "a", Action: "unload", From: "profile:user:go", Over: []string{}, Version: a},
			{Module: "c", Action: "replace", From: "profile:default", Over: []string{}, Version: c, Was: c}}))
		Expect(removals).To(ConsistOf(ConsistOf(And(HaveField("Module", "a"), HaveField("WholeModule", true)))), "only a goes, and it is asked about")
		Expect(installed(user)).To(Equal(map[string]string{"c": "profile:default"}))
		Expect(command(p.Home, "c")).To(Equal("C\n"))
		Expect(filepath.Join(p.Home, ".claude", "commands", "a.md")).NotTo(BeAnExistingFile())
		Expect(activeProfile(user)).To(BeNil())
	})

	It("keeps one profile on: --switch replaces it, and --on refuses while another is on", func() {
		a, b := mod("a", "a", "A"), mod("b", "b", "B")
		writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: "+a+"}\n")
		writeProfile("docs", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: b, source: "+b+"}\n")
		res, err := turn(user, engine.ProfileOn, "go")
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Activation.Profile).To(HaveValue(Equal("user:go")))
		before, err := os.ReadFile(p.UserLedger)
		Expect(err).NotTo(HaveOccurred())

		_, err = turn(user, engine.ProfileOn, "docs")
		other, ok := errors.AsType[*engine.OtherProfileOnError](err)
		Expect(ok).To(BeTrue(), "%v", err)
		Expect(other.On).To(Equal("user:go"))
		Expect(other.Name).To(Equal("user:docs"))
		Expect(err).To(MatchError("user:go is on, and --on does not replace it without asking; --switch does"))
		Expect(errs.Hints(err)).To(Equal([]string{"lola profile --user user:docs --switch", "lola profile --user go-docs --switch --new --bundle go,docs"}))
		Expect(os.ReadFile(p.UserLedger)).To(Equal(before), "a refusal changes nothing")

		res, err = turn(user, engine.ProfileSwitch, "docs")
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Activation.Profile).To(HaveValue(Equal("user:docs")))
		Expect(res.Activation.Named).To(Equal("user:docs"))
		Expect(res.Activation.Turned).To(BeTrue())
		Expect(activeProfile(user)).To(Equal(&ledger.Active{Name: "user:docs", By: ledger.BySwitch}))
		Expect(installed(user)).To(Equal(map[string]string{"b": "profile:user:docs"}))
		Expect(removals).To(ConsistOf(ConsistOf(HaveField("Module", "a"))), "go's module goes, asked about")
	})

	It("turns off the profile on with --off and no name, and says when none is", func() {
		a := mod("a", "a", "A")
		writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: "+a+"}\n")
		Expect(turn(user, engine.ProfileOn, "go")).Error().NotTo(HaveOccurred())
		res, err := turn(user, engine.ProfileOff, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Activation.Named).To(Equal("user:go"))
		Expect(res.Activation.Profile).To(BeNil())
		Expect(activeProfile(user)).To(BeNil())
		Expect(installed(user)).To(BeEmpty())

		res, err = turn(user, engine.ProfileOff, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(res.InSync).To(BeTrue())
		Expect(res.Notices).To(ConsistOf(engine.Notice{Level: engine.LevelInfo, Text: "no profile is on"}))
	})

	It("refuses a lock.json with the profiles list of an older lola, hinting the switch to its last profile", func() {
		writeTree(filepath.Dir(p.UserLedger), map[string]string{filepath.Base(p.UserLedger): `{"version":1,` +
			`"profiles":[{"name":"user:go","by":"on"},{"name":"user:docs","by":"on"}],"modules":{},"created":[]}`})
		coach := []string{"remove the profiles key from " + p.UserLedger + ", then turn your profile on again: lola profile --user user:docs --switch"}
		_, err := sync(user)
		Expect(err).To(MatchError(p.UserLedger + ": profiles is not a key lola reads any more; lock.json records the one profile on as profile"))
		Expect(errs.Hints(err)).To(Equal(coach))
		_, err = e.PlanRepair(engine.RepairRequest{Scope: user})
		Expect(errs.Hints(err)).To(Equal(coach), "lola repair coaches it too")
	})

	It("refuses a lock.json naming a profile base, which an older lola recorded, hinting the rename that fixes it", func() {
		mine := writeProfile("base", "version: 1\n")
		kept := writeProfile("base-old", "version: 1\ndescription: mine already\n")
		writeTree(filepath.Dir(p.UserLedger), map[string]string{filepath.Base(p.UserLedger): `{"version":1,` +
			`"profile":{"name":"user:base","by":"switch"},"modules":{},"created":[]}`})
		renamed := filepath.Join(filepath.Dir(mine), "base-old-2.yml")
		coach := []string{"rename " + mine + " to " + renamed + ", then replace user:base with user:base-old-2 in " + p.UserLedger}
		_, err := sync(user)
		Expect(err).To(MatchError(p.UserLedger + ": it records the profile user:base, and base is now reserved for the scope's base, so no profile has that name"))
		Expect(errs.Hints(err)).To(Equal(coach))
		_, err = e.PlanRepair(engine.RepairRequest{Scope: user})
		Expect(errs.Hints(err)).To(Equal(coach), "lola repair coaches it too, never hinting itself")

		Expect(os.Rename(mine, renamed)).To(Succeed(), "the hint, as printed")
		b, err := os.ReadFile(p.UserLedger)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(p.UserLedger, []byte(strings.ReplaceAll(string(b), "user:base", "user:base-old-2")), 0o600)).To(Succeed())
		Expect(sync(user)).Error().NotTo(HaveOccurred())
		Expect(activeProfile(user)).To(HaveField("Name", "user:base-old-2"))
		Expect(os.ReadFile(kept)).To(Equal([]byte("version: 1\ndescription: mine already\n")), "nothing is lost")

		Expect(os.Remove(renamed)).To(Succeed())
		Expect(os.Remove(kept)).To(Succeed())
		writeTree(filepath.Dir(p.UserLedger), map[string]string{filepath.Base(p.UserLedger): `{"version":1,` +
			`"modules":{"a":{"source":"` + filepath.ToSlash(mods) + `/a","from":"profile:user:base","targets":[]},` +
			`"b":{"source":"` + filepath.ToSlash(mods) + `/b","from":"profile:remote:base-old","targets":[]}},"created":[]}`})
		_, err = sync(user)
		Expect(err).To(MatchError(ContainSubstring("it records the profile user:base")), "a module's from: names it too")
		Expect(errs.Hints(err)).To(Equal([]string{"replace user:base with user:base-old-2 in " + p.UserLedger}),
			"no file to rename; base-old is a name lock.json records")
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
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: user, Profiles: &engine.ProfileChange{Action: engine.ProfileOff, Name: "go"},
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
		_, err = turn(user, engine.ProfileOff, "nope")
		Expect(err).To(MatchError("no profile nope for user scope"))
		unchanged("an unknown name")

		// A partial trust answer would commit the new list with planned modules left out.
		writeProfile("bare", "version: 1\nmodules:\n  - {name: a, source: "+a+"}\n")
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules: []\n"})
		asked := false
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOn, Name: "bare"},
			Trust: func(engine.TrustReport) (engine.TrustAnswer, error) {
				asked = true
				return engine.TrustAnswer{Modules: []string{"a"}}, nil
			}})
		Expect(asked).To(BeTrue(), "the profile's entry installs to project.yml's untrusted targets")
		Expect(err).To(MatchError("lola profile applies all of its changes or none; trust them all, or cancel"))
		Expect(activeProfile(sc)).To(BeNil(), "a partial trust answer")
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

		res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOn, Name: "go"}})
		Expect(err).NotTo(HaveOccurred(), "no trust question about project.yml's pulled entry, which the change does not move")
		Expect(exported(res.Activation.Changes)).To(Equal([]engine.LayerChange{{Module: "c", Action: "load", From: "profile:user:go", Over: []string{}, Version: c}}))
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "1 other change is pending → lola sync --project"}))
		Expect(installed(sc)).To(Equal(map[string]string{"a": "", "c": "profile:user:go"}))
		Expect(command(proj, "a")).To(Equal("A\n"), "a's edited folder waits for lola sync")
		Expect(filepath.Join(proj, ".claude", "commands", "b.md")).NotTo(BeAnExistingFile())
	})

	It("holds at most 64 profiles in an active list", func() {
		// One profile is on at a time (spec 2026-10-29 §3.1): switching through 65 leaves the last on alone.
		var names []string
		for i := range 65 {
			names = append(names, fmt.Sprintf("p%d", i))
			writeProfile(names[i], "version: 1\n")
		}
		for _, n := range names {
			Expect(turn(user, engine.ProfileSwitch, n)).Error().NotTo(HaveOccurred(), n)
		}
		Expect(activeProfile(user)).To(Equal(&ledger.Active{Name: "user:p64", By: ledger.BySwitch}))
		_, err := turn(user, engine.ProfileOn, names[0])
		Expect(err).To(MatchError("user:p64 is on, and --on does not replace it without asking; --switch does"))
		Expect(errs.Hints(err)).To(Equal([]string{"lola profile --user user:p0 --switch", "lola profile --user p64-p0 --switch --new --bundle p64,p0"}))
		Expect(activeProfile(user)).To(Equal(&ledger.Active{Name: "user:p64", By: ledger.BySwitch}))
	})

	It("--switch keeps default on and unloads only what nothing wants", func() {
		a, b, c := mod("a", "a", "A"), mod("b", "b", "B"), mod("c", "c", "C")
		writeProfile("default", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: c, source: "+c+"}\n")
		writeProfile("baseline", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: "+a+"}\n  - {name: c, source: "+c+"}\n")
		writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: b, source: "+b+"}\n")
		_, err := turn(user, engine.ProfileOn, "baseline")
		Expect(err).NotTo(HaveOccurred())
		Expect(installed(user)).To(Equal(map[string]string{"a": "profile:user:baseline", "c": "profile:user:baseline"}))

		res, err := turn(user, engine.ProfileSwitch, "go")
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Activation.Profile).To(HaveValue(Equal("user:go")))
		Expect(activeProfile(user)).To(Equal(&ledger.Active{Name: "user:go", By: ledger.BySwitch}))
		Expect(removals).To(ConsistOf(ConsistOf(HaveField("Module", "a"))))
		Expect(installed(user)).To(Equal(map[string]string{"b": "profile:user:go", "c": "profile:default"}))
		Expect(command(p.Home, "c")).To(Equal("C\n"))

		res, err = turn(user, engine.ProfileSwitch, "go")
		Expect(err).NotTo(HaveOccurred())
		Expect(res.InSync).To(BeTrue())
		Expect(res.Notices).To(ConsistOf(engine.Notice{Level: engine.LevelInfo, Text: "user:go is already on"}))
		Expect(res.Activation.Changes).To(BeEmpty())
	})

	It("refuses the base's actions it cannot take, and notes what is already so", func() {
		writeProfile("default", "version: 1\n")
		writeProfile("go", "version: 1\n")
		for _, name := range []string{"base", "default", "user:default"} {
			res, err := turn(user, engine.ProfileOn, name)
			Expect(err).NotTo(HaveOccurred(), name)
			Expect(res.Notices).To(ConsistOf(engine.Notice{Level: engine.LevelInfo, Text: "the base is always loaded"}), name)
			res, err = turn(user, engine.ProfileSwitch, name)
			Expect(err).NotTo(HaveOccurred(), name)
			Expect(res.Notices).To(ConsistOf(engine.Notice{Level: engine.LevelInfo, Text: "already on the base"}), name)
			_, err = turn(user, engine.ProfileOff, name)
			Expect(err).To(MatchError("the base is always loaded; lola profile --off turns off the profile over it"), name)
			Expect(errs.Hints(err)).To(BeEmpty(), "%s: no profile is on to turn off", name)
		}
		Expect(turn(user, engine.ProfileOn, "go")).Error().NotTo(HaveOccurred())
		_, err := turn(user, engine.ProfileOn, "base")
		Expect(err).To(MatchError("the base is always loaded; user:go is on over it"))
		Expect(errs.Hints(err)).To(Equal([]string{"lola profile --user base --switch"}))
		_, err = turn(user, engine.ProfileOff, "default")
		Expect(errs.Hints(err)).To(Equal([]string{"lola profile --user --off"}))

		res, err := turn(user, engine.ProfileOn, "go")
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ConsistOf(engine.Notice{Level: engine.LevelInfo, Text: "user:go is already on"}))
		Expect(res.InSync).To(BeTrue())
		Expect(turn(user, engine.ProfileOff, "go")).Error().NotTo(HaveOccurred())
		res, err = turn(user, engine.ProfileOff, "go")
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ConsistOf(engine.Notice{Level: engine.LevelInfo, Text: "user:go is not on"}))
	})

	It("base --switch and default --switch turn the profile on off", func() {
		a, c := mod("a", "a", "A"), mod("c", "c", "C")
		writeProfile("default", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: c, source: "+c+"}\n")
		writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: "+a+"}\n")
		Expect(e.Sync(ctx, engine.SyncRequest{Scope: user})).Error().NotTo(HaveOccurred(), "installs the base: c")
		for _, name := range []string{"base", "default"} {
			Expect(turn(user, engine.ProfileOn, "go")).Error().NotTo(HaveOccurred(), name)
			dry, err := e.Sync(ctx, engine.SyncRequest{Scope: user, Options: engine.Options{DryRun: true},
				Profiles: &engine.ProfileChange{Action: engine.ProfileSwitch, Name: name}})
			Expect(err).NotTo(HaveOccurred(), name)
			Expect(dry.ToApply).To(Equal("lola profile --user user:go --off"), "%s: the run that applies it turns go off", name)
			res, err := turn(user, engine.ProfileSwitch, name)
			Expect(err).NotTo(HaveOccurred(), name)
			Expect(res.Activation.Named).To(Equal("user:go"), name)
			Expect(res.Activation.Profile).To(BeNil(), name)
			Expect(activeProfile(user)).To(BeNil(), name)
			Expect(installed(user)).To(Equal(map[string]string{"c": "profile:default"}), name)
		}
	})

	It("refuses default at project scope, naming the project's base", func() {
		writeProfile("go", "version: 1\n")
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		// default --switch hints each run only where it would change something: the project's base
		// while a profile is on in the project, user scope's default while one is on there.
		_, err := turn(sc, engine.ProfileSwitch, "default")
		Expect(err).To(MatchError("default is user scope's base; this project's base is project.yml"))
		Expect(errs.Hints(err)).To(BeEmpty(), "no profile is on in either scope")
		Expect(turn(user, engine.ProfileOn, "go")).Error().NotTo(HaveOccurred())
		_, err = turn(sc, engine.ProfileSwitch, "default")
		Expect(errs.Hints(err)).To(Equal([]string{"lola profile --user default --switch"}), "a profile is on at user scope only")
		Expect(turn(sc, engine.ProfileOn, "go")).Error().NotTo(HaveOccurred())
		_, err = turn(sc, engine.ProfileSwitch, "default")
		Expect(errs.Hints(err)).To(Equal([]string{"lola profile --project base --switch", "lola profile --user default --switch"}), "on in both")
		Expect(turn(user, engine.ProfileOff, "")).Error().NotTo(HaveOccurred())
		_, err = turn(sc, engine.ProfileSwitch, "default")
		Expect(errs.Hints(err)).To(Equal([]string{"lola profile --project base --switch"}), "a profile is on in the project only")
		Expect(turn(sc, engine.ProfileOff, "")).Error().NotTo(HaveOccurred())

		_, err = turn(sc, engine.ProfileOn, "user:default")
		Expect(err).To(MatchError("default is user scope's base; this project's base is project.yml"))
		Expect(errs.Hints(err)).To(BeEmpty(), "a base --on would do nothing")
		_, err = turn(sc, engine.ProfileOff, "default")
		Expect(errs.Hints(err)).To(BeEmpty(), "no profile is on, and the base cannot be turned off")

		Expect(turn(sc, engine.ProfileOn, "go")).Error().NotTo(HaveOccurred())
		_, err = turn(sc, engine.ProfileOn, "default")
		Expect(errs.Hints(err)).To(Equal([]string{"lola profile --project base --switch"}))
		_, err = turn(sc, engine.ProfileOff, "default")
		Expect(errs.Hints(err)).To(Equal([]string{"lola profile --project --off"}))
		res, err := turn(sc, engine.ProfileSwitch, "base")
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Activation.Named).To(Equal("user:go"))
		Expect(activeProfile(sc)).To(BeNil())
	})

	It("default --switch at project scope coaches user scope's lock.json with the profiles list of an older lola", func() {
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		writeTree(filepath.Dir(p.UserLedger), map[string]string{filepath.Base(p.UserLedger): `{"version":1,` +
			`"profiles":[{"name":"user:go","by":"on"}],"modules":{},"created":[]}`})
		_, err := turn(sc, engine.ProfileSwitch, "default")
		Expect(err).To(MatchError(p.UserLedger + ": profiles is not a key lola reads any more; lock.json records the one profile on as profile"))
		Expect(errs.Hints(err)).To(Equal([]string{"remove the profiles key from " + p.UserLedger +
			", then turn your profile on again: lola profile --user user:go --switch"}))
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
			Expect(activeProfile(user)).To(BeNil())
			Expect(installed(user)).To(BeEmpty())
			writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: "+a+"}\n")
		}
	})

	It("turns a profile on in a project without .lola, writing the list with the apply", func() {
		a := mod("a", "a", "A")
		writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: "+a+"}\n")
		res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{DryRun: true},
			Profiles: &engine.ProfileChange{Action: engine.ProfileOn, Name: "go"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.ToApply).To(Equal("lola profile --project user:go --on"))
		Expect(res.Activation.Changes).To(ConsistOf(HaveField("Action", "load")))
		Expect(filepath.Join(proj, ".lola")).NotTo(BeADirectory(), "a dry run writes nothing")

		_, err = turn(sc, engine.ProfileOn, "go")
		Expect(err).NotTo(HaveOccurred())
		Expect(activeProfile(sc)).To(Equal(&ledger.Active{Name: "user:go", By: ledger.ByOn}))
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
