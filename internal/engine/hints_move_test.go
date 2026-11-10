package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("the move hint", func() {
	var (
		tmp, proj, notes string
		p                paths.Paths
		e                *engine.Engine
		user, sc         engine.Scope
		ctx              = context.Background()
		claude           = engine.Options{Targets: []string{"claude-code"}}
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
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		user, sc = engine.Scope{Kind: target.User}, engine.Scope{Kind: target.Project, ProjectDir: proj}
		notes = filepath.Join(tmp, "notes")
		writeTree(notes, map[string]string{"lola.yml": "name: notes\n", "commands/notes.md": "N\n"})
	})
	hints := func(res *engine.Result) []string {
		out := []string{}
		for _, n := range res.Notices {
			if strings.HasPrefix(n.Text, "meant for ") || strings.HasPrefix(n.Text, "keep it to yourself") || strings.HasPrefix(n.Text, "other profiles") {
				Expect(n.Level).To(Equal(engine.LevelInfo))
				out = append(out, n.Text)
			}
		}
		return out
	}
	install := func(s engine.Scope, opts engine.Options, module string) []string {
		GinkgoHelper()
		res, err := e.Install(ctx, s, tmp, []engine.InstallRequest{{Arg: module}}, opts)
		Expect(err).NotTo(HaveOccurred())
		return hints(res)
	}
	activate := func(s engine.Scope, name string) {
		GinkgoHelper()
		path := p.UserLedger
		if s.Kind == target.Project {
			path = filepath.Join(s.ProjectDir, ".lola", "lock.json")
		}
		led, err := e.Ledger(s)
		Expect(err).NotTo(HaveOccurred())
		led.Profile = &ledger.Active{Name: name, By: ledger.ByOn}
		Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
		Expect(led.Save(path)).To(Succeed())
	}

	It("after a project.yml write, hints the move into a profile of yours and into every project", func() {
		writeTree(p.UserProfiles, map[string]string{"me.yml": "version: 1\n"})
		Expect(install(sc, claude, notes)).To(Equal([]string{
			"meant for another profile? lola profile --project --move notes --to me --switch",
			"meant for every project? lola profile --user --move notes --to base"}))
	})

	It("with no profile of yours, after a shared write, hints a new one, never a shared file", func() {
		Expect(install(sc, claude, notes)).To(Equal([]string{
			"keep it to yourself: lola profile --project --move notes --to mine --switch",
			"meant for every project? lola profile --user --move notes --to base"}))

		// A profile of another kind named mine would be what --to mine names: the hint picks mine-2.
		writeTree(proj, map[string]string{".lola/profiles/mine.yml": "version: 1\n"})
		other := filepath.Join(tmp, "other")
		writeTree(other, map[string]string{"lola.yml": "name: other\n", "commands/other.md": "O\n"})
		Expect(install(sc, claude, other)).To(ContainElement("keep it to yourself: lola profile --project --move other --to mine-2 --switch"))
	})

	It("after a user-scope write in a found project, hints this project only; at most two profiles, then the listing", func() {
		writeTree(p.UserProfiles, map[string]string{"a.yml": "version: 1\n", "b.yml": "version: 1\n", "c.yml": "version: 1\n"})
		Expect(install(user, claude, notes)).To(Equal([]string{
			"meant for another profile? lola profile --user --move notes --to a --switch",
			"meant for another profile? lola profile --user --move notes --to b --switch",
			"other profiles: lola profile --user",
			"meant for this project only? lola profile --project --move notes --to user:a --switch"}))
	})

	It("names the profile on without --switch, then the base, and carries no -y", func() {
		writeTree(p.UserProfiles, map[string]string{"a.yml": "version: 1\n", "b.yml": "version: 1\n"})
		activate(user, "user:b")
		activate(sc, "user:b")
		Expect(install(user, claude, notes)).To(Equal([]string{
			"meant for another profile? lola profile --user --move notes --to a --switch",
			"meant for another profile? lola profile --user --move notes --to base"}),
			"the target is b, on in the project too, which then lists notes: no move into it")

		other := filepath.Join(tmp, "other")
		writeTree(other, map[string]string{"lola.yml": "name: other\n", "commands/other.md": "O\n"})
		activate(user, "user:a")
		activate(sc, "user:b")
		asks := claude
		asks.AskWhere = func(engine.WhereQuestion) (engine.WhereChoice, error) {
			Fail("nothing to ask")
			return engine.WhereChoice{}, nil
		}
		Expect(install(user, asks, other)).To(Equal([]string{
			"meant for another profile? lola profile --user --move other --to b --switch",
			"meant for another profile? lola profile --user --move other --to base",
			"meant for this project only? lola profile --project --move other --to user:b"}),
			"the profile of yours on in the project, which takes it without a switch")
	})

	It("qualifies a profile of yours a project profile has the name of", func() {
		writeTree(p.UserProfiles, map[string]string{"me.yml": "version: 1\n"})
		writeTree(proj, map[string]string{".lola/profiles/me.yml": "version: 1\n"})
		Expect(install(sc, claude, notes)).To(HaveExactElements(
			"meant for another profile? lola profile --project --move notes --to user:me --switch", HavePrefix("meant for every project? ")))
	})

	It("passes over a profile that lists the module already", func() {
		writeTree(p.UserProfiles, map[string]string{"me.yml": "version: 1\nmodules:\n  - {name: notes, source: " + notes + "}\n"})
		Expect(install(sc, claude, notes)).To(Equal([]string{
			"keep it to yourself: lola profile --project --move notes --to mine --switch",
			"meant for every project? lola profile --user --move notes --to base"}))
	})

	It("names -C for a project not found from here, and the two-step without a profile of yours", func() {
		e.Getwd = func() (string, error) { return tmp, nil }
		Expect(install(sc, claude, notes)).To(ContainElement("meant for every project? lola profile --user --move notes --to base -C " + proj))

		other := filepath.Join(tmp, "other")
		writeTree(other, map[string]string{"lola.yml": "name: other\n", "commands/other.md": "O\n"})
		Expect(install(user, claude, other)).To(BeEmpty(), "no project is found from here")
		e.Getwd = func() (string, error) { return proj, nil }
		third := filepath.Join(tmp, "third")
		writeTree(third, map[string]string{"lola.yml": "name: third\n", "commands/third.md": "T\n"})
		Expect(install(user, claude, third)).To(Equal([]string{
			"meant for this project only? lola uninstall --user third, then lola install --project third"}), "the project names its targets, from the install above")
	})

	It("follows only what an install adds: nothing for a dry run, a changed entry, or a scope the module is listed in", func() {
		dry := claude
		dry.DryRun = true
		Expect(install(sc, dry, notes)).To(BeEmpty())
		Expect(install(sc, claude, notes)).NotTo(BeEmpty())
		again := claude
		again.Targets = []string{"claude-code", "gemini-cli"}
		Expect(install(sc, again, notes)).To(BeEmpty(), "the entry changed")
		Expect(install(user, claude, notes)).To(BeEmpty(), "user scope's only profile is default; the project lists notes")
	})

	It("never hints a switch into a profile whose plan asks for consent, as one listing an installed module at another digest", func() {
		hooked := func(dir, body string) {
			// The hook differs with the body: a hook the user consented to at another digest asks again.
			writeTree(dir, map[string]string{"lola.yml": "name: kit\nhooks: {pre-install: setup.sh}\n", "setup.sh": "#!/bin/sh\necho " + strings.TrimSpace(body) + "\n", "commands/kit.md": body})
			Expect(os.Chmod(filepath.Join(dir, "setup.sh"), 0o755)).To(Succeed())
		}
		kit, kit2, ghost := filepath.Join(tmp, "kit"), filepath.Join(tmp, "kit2"), filepath.Join(tmp, "ghost")
		hooked(kit, "one\n")
		hooked(kit2, "two\n")
		writeTree(ghost, map[string]string{"lola.yml": "name: ghost\n", "commands/ghost.md": "G\n"})
		trusted := claude
		trusted.Trust = engine.TrustHooks
		e.Getwd = func() (string, error) { return tmp, nil }
		install(user, trusted, kit) // installed, its hook consented to at this digest
		writeTree(p.UserProfiles, map[string]string{
			"a.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + kit2 + "}\n",
			"b.yml": "version: 1\nbundle: [c]\n",
			"c.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: ghost, source: " + ghost + "}\n",
			"d.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + kit + "}\n"})
		Expect(install(user, claude, notes)).To(Equal([]string{
			"meant for another profile? lola profile --user --move notes --to c --switch",
			"meant for another profile? lola profile --user --move notes --to d --switch"}),
			"a's kit would replace the one installed and ask about its hook; b is a bundle; "+
				"c installs a module without hooks, d kit as it is installed")
	})

	It("moves into this project only through a profile of yours that does not list the module", func() {
		writeTree(p.UserProfiles, map[string]string{"a.yml": "version: 1\nmodules:\n  - {name: notes, source: " + notes + "}\n", "b.yml": "version: 1\n"})
		Expect(install(user, claude, notes)).To(Equal([]string{
			"meant for another profile? lola profile --user --move notes --to b --switch",
			"meant for this project only? lola profile --project --move notes --to user:b --switch"}))
	})

	It("names the profile a switch turns off only when the switch removes its modules", func() {
		kept := filepath.Join(tmp, "kept")
		writeTree(kept, map[string]string{"lola.yml": "name: kept\n", "commands/kept.md": "K\n"})
		writeTree(p.UserProfiles, map[string]string{"a.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kept, source: " + kept + "}\n",
			"b.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kept, source: " + kept + "}\n", "c.yml": "version: 1\n"})
		activate(user, "user:a")
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: user})
		Expect(err).NotTo(HaveOccurred())
		e.Getwd = func() (string, error) { return tmp, nil }
		Expect(install(user, claude, notes)).To(Equal([]string{
			"meant for another profile? lola profile --user --move notes --to b --switch",
			"meant for another profile (switches a off, removing its modules)? lola profile --user --move notes --to c --switch",
			"other profiles: lola profile --user"}), "b lists kept too: switching to it removes nothing")
	})

	It("never offers a bundle, and names the bundle on that a switch turns off", func() {
		kept := filepath.Join(tmp, "kept")
		writeTree(kept, map[string]string{"lola.yml": "name: kept\n", "commands/kept.md": "K\n"})
		writeTree(p.UserProfiles, map[string]string{"a.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kept, source: " + kept + "}\n",
			"c.yml": "version: 1\n", "d.yml": "version: 1\n", "other.yml": "version: 1\nbundle: [c]\n", "work.yml": "version: 1\nbundle: [a, d]\n"})
		activate(user, "user:work")
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: user})
		Expect(err).NotTo(HaveOccurred())
		e.Getwd = func() (string, error) { return tmp, nil }
		opts := claude
		opts.AskWhere = func(q engine.WhereQuestion) (engine.WhereChoice, error) { return q.Choices[0], nil }
		Expect(install(user, opts, notes)).To(Equal([]string{
			"meant for another profile? lola profile --user --move notes --to a",
			"meant for another profile (switches bundle work off, removing its modules)? lola profile --user --move notes --to c --switch",
			"other profiles: lola profile --user"}), "neither bundle, other nor work, is offered")
		_, err = e.Move(ctx, engine.MoveRequest{Scope: user, Modules: []string{"notes"}, To: "a"})
		Expect(err).NotTo(HaveOccurred(), "the hint runs as printed")
		var removed []engine.Removal
		_, err = e.Move(ctx, engine.MoveRequest{Scope: user, Modules: []string{"notes"}, To: "c", Switch: true,
			ConfirmRemovals: func(r []engine.Removal) (bool, error) { removed = r; return true, nil }})
		Expect(err).NotTo(HaveOccurred(), "the hint runs as printed")
		Expect(removed).To(ContainElement(HaveField("Module", "kept")), "the switch turns the whole bundle off")
		led, err := e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Profile.Name).To(Equal("user:c"))
	})

	It("names no other scope a run installs into too", func() {
		res, err := e.InstallAll(ctx, engine.InstallSpec{Cwd: tmp, Modules: []engine.InstallRequest{{Arg: notes}}, Options: claude,
			Scopes: []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: sc}, Targets: claude.Targets},
				{ScopeChoice: engine.ScopeChoice{Scope: user}, Targets: claude.Targets}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(hints(res.Result)).To(Equal([]string{"keep it to yourself: lola profile --project --move notes --to mine --switch"}))
	})
})
