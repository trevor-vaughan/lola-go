package engine_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("profile entry picks", func() {
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
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		sc = engine.Scope{Kind: target.Project, ProjectDir: proj}
	})
	// kit writes a folder module with skills a, b and c, in the project, where a project profile's
	// folder source must resolve.
	kit := func() string {
		GinkgoHelper()
		dir := filepath.Join(proj, "mods", "kit")
		writeTree(dir, map[string]string{"lola.yml": "name: kit\n", "skills/a/SKILL.md": "---\nname: a\ndescription: A\n---\nA\n",
			"skills/b/SKILL.md": "---\nname: b\ndescription: B\n---\nB\n", "skills/c/SKILL.md": "---\nname: c\ndescription: C\n---\nC\n"})
		return dir
	}
	skills := func(home string) []string {
		GinkgoHelper()
		var out []string
		for _, s := range []string{"a", "b", "c"} {
			if _, err := os.Stat(filepath.Join(home, ".claude", "skills", s, "SKILL.md")); err == nil {
				out = append(out, s)
			}
		}
		return out
	}
	sync := func(s engine.Scope) {
		GinkgoHelper()
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: s, ConfirmRemovals: func([]engine.Removal) (bool, error) { return true, nil },
			Trust: func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }})
		Expect(err).NotTo(HaveOccurred())
	}

	It("a profile entry's only: and skip: decide what of its module installs, with the global skip, and selections.yaml's scope section does not", func() {
		dir := kit()
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n" +
			"  - {name: kit, source: " + dir + ", only: [skill/a, skill/b, skill/c], skip: [skill/c]}\n"})
		writeTree(filepath.Dir(p.Selections), map[string]string{"selections.yaml": "version: 1\nglobal:\n  kit:\n    skip: [skill/b]\n" +
			"projects:\n  " + proj + ":\n    kit:\n      skip: [skill/a]\n"})
		led := ledger.New()
		led.Profile = &ledger.Active{Name: "user:go", By: ledger.ByOn}
		Expect(led.Save(filepath.Join(proj, ".lola", "lock.json"))).To(Succeed())
		sync(sc)
		Expect(skills(proj)).To(Equal([]string{"a"}), "c by the entry's skip, b by the global skip; the project section's skip of a is not go's")
		rows, _, err := e.Status(ctx, sc, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(ContainElement(And(HaveField("Name", "kit"), HaveField("Skipped", ConsistOf("skill/b", "skill/c")),
			HaveField("Only", []string{"skill/a", "skill/b", "skill/c"}), HaveField("OnlyFrom", "profile go"))), "list names the only: list in force and its layer")

		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + dir + "}\n"})
		writeTree(filepath.Dir(p.Selections), map[string]string{"selections.yaml": "version: 1\nglobal:\n  kit:\n    only: [skill/a]\n"})
		rows, _, err = e.Status(ctx, sc, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(ContainElement(And(HaveField("Name", "kit"), HaveField("Only", []string{}), HaveField("OnlyFrom", ""))),
			"the global only: does not apply to a profile's module")
	})

	It("lists a wanted, not installed module with the only: list in force and its layer", func() {
		dir := kit()
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n" +
			"  - {name: kit, source: " + dir + ", only: [skill/a]}\n"})
		led := ledger.New()
		led.Profile = &ledger.Active{Name: "user:go", By: ledger.ByOn}
		Expect(led.Save(filepath.Join(proj, ".lola", "lock.json"))).To(Succeed())
		rows, _, err := e.Status(ctx, sc, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(ContainElement(And(HaveField("Name", "kit"), HaveField("Status", "not_installed"),
			HaveField("Only", []string{"skill/a"}), HaveField("OnlyFrom", "profile go"))))
	})

	It("warns, at sync and install, that selections.yaml's scope section is ignored for a module a profile provides, hinting that profile's edit", func() {
		dir := kit()
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + dir + ", skip: [skill/c]}\n"})
		writeTree(filepath.Dir(p.Selections), map[string]string{"selections.yaml": "version: 1\nprojects:\n  " + proj + ":\n    kit:\n      skip: [skill/a]\n"})
		led := ledger.New()
		led.Profile = &ledger.Active{Name: "user:go", By: ledger.ByOn}
		Expect(led.Save(filepath.Join(proj, ".lola", "lock.json"))).To(Succeed())
		ignored := engine.Notice{Level: engine.LevelWarn, Text: "selections.yaml projects[" + proj + "] kit is ignored: profile go provides it; delete it from " +
			p.Selections + ", and pick kit's components in profile go → lola profile --project go --edit kit"}
		isIgnored := HaveField("Text", ContainSubstring(" is ignored: "))
		res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, ConfirmRemovals: func([]engine.Removal) (bool, error) { return true, nil }})
		Expect(err).NotTo(HaveOccurred())
		var found []engine.Notice
		Expect(res.Notices).To(ContainElement(isIgnored, &found))
		Expect(found).To(HaveLen(1), "once per sync")
		Expect(found[0]).To(Equal(ignored))
		Expect(skills(proj)).To(Equal([]string{"a", "b"}), "go's skip applies, the section's does not")

		ires, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: "kit"}}, engine.Options{Targets: []string{"claude-code"}})
		Expect(err).NotTo(HaveOccurred())
		found = nil
		Expect(ires.Notices).To(ContainElement(isIgnored, &found))
		Expect(found).To(HaveLen(1), "once per install")
		Expect(found[0]).To(Equal(ignored))

		writeTree(filepath.Dir(p.Selections), map[string]string{"selections.yaml": "version: 1\nprojects:\n  " + proj + ":\n    other:\n      skip: [skill/a]\n"})
		res, err = e.Sync(ctx, engine.SyncRequest{Scope: sc})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).NotTo(ContainElement(isIgnored), "a section naming another module says nothing")
	})

	It("a profile's state lists each entry's only: and skip: as written, [] for none", func() {
		dir := kit()
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\nmodules:\n  - {name: kit, source: " + dir + ", only: ['skill/*'], skip: [skill/c]}\n" +
			"  - {name: other, source: " + dir + "}\n"})
		states, _, err := e.ProfileState(ctx, []engine.Scope{sc}, "go")
		Expect(err).NotTo(HaveOccurred())
		Expect(states[0].Modules).To(HaveExactElements(
			And(HaveField("Name", "kit"), HaveField("Only", []string{"skill/*"}), HaveField("Skip", []string{"skill/c"})),
			And(HaveField("Name", "other"), HaveField("Only", []string{}), HaveField("Skip", []string{}))))
	})

	It("lola info names, per scope, the layer that provides the module, each component's state, its origin, what is new and what it mentions, and the re-pick", func() {
		dir := filepath.Join(tmp, "src", "kit")
		writeTree(dir, map[string]string{"lola.yml": "name: kit\n", "skills/alpha/SKILL.md": "---\ndescription: A\n---\nSee bravo.\n",
			"skills/bravo/SKILL.md": "---\ndescription: B\n---\nB\n", "skills/charlie/SKILL.md": "---\ndescription: C\n---\nC\n",
			"rules/style.md": "Small diffs.\n"})
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n" +
			"  - {name: kit, source: " + dir + ", only: [skill/alpha, skill/bravo, 'rule/*'], skip: [skill/bravo], rules: false}\n"})
		user := engine.Scope{Kind: target.User}
		sync(user)
		writeTree(dir, map[string]string{"skills/delta/SKILL.md": "---\ndescription: D\n---\nD\n"})
		info, err := e.Info(ctx, engine.InfoRequest{Cwd: proj, Arg: dir, Scope: engine.ScopeRequest{Cwd: proj, User: true}, Targets: []string{"claude-code"},
			Rerun: func(extra ...string) string { return "lola info" }, UserRun: "lola info --user",
			Install: func(arg, ref, path string, k engine.TrustKinds) string { return "lola install " + arg }})
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Install).To(HaveLen(1))
		in := info.Install[0]
		Expect(in.From).To(Equal("profile:default"))
		Expect(in.Change).To(Equal("lola profile --user default --edit kit"))
		Expect(in.Components).To(Equal([]engine.InfoComponent{
			{Kind: "skill", Name: "alpha", State: engine.ComponentInstalled, Mentions: []string{"skill/bravo"}},
			{Kind: "skill", Name: "bravo", State: engine.ComponentSkipped, Origin: "default (base)", Mentions: []string{}},
			{Kind: "skill", Name: "charlie", State: engine.ComponentNotInOnly, Origin: "default (base)", Mentions: []string{}},
			{Kind: "skill", Name: "delta", State: engine.ComponentNotInOnly, Origin: "default (base)", New: true, Mentions: []string{}},
			{Kind: "rule", Name: "style", State: engine.ComponentRulesOff, Mentions: []string{}}}))

		info, err = e.Info(ctx, engine.InfoRequest{Cwd: proj, Arg: dir, Scope: engine.ScopeRequest{Cwd: proj, Project: true}, Targets: []string{"claude-code"},
			Rerun: func(extra ...string) string { return "lola info" }, UserRun: "lola info --user",
			Install: func(arg, ref, path string, k engine.TrustKinds) string { return "lola install " + arg }})
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Install[0]).To(And(HaveField("From", ""), HaveField("Components", []engine.InfoComponent{}), HaveField("Change", "")),
			"no layer of the project provides it: the plan says what an install takes")
	})

	It("lola info calls no component new when the cache no longer holds the installed version", func() {
		dir := filepath.Join(tmp, "src", "kit")
		writeTree(dir, map[string]string{"lola.yml": "name: kit\n", "skills/alpha/SKILL.md": "---\ndescription: A\n---\nA\n"})
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n" +
			"  - {name: kit, source: " + dir + ", only: [skill/alpha]}\n"})
		user := engine.Scope{Kind: target.User}
		sync(user)
		led, err := e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.RemoveAll(filepath.Join(p.Store, "kit", "versions", strings.TrimPrefix(led.Modules["kit"].Digest, "sha256:")[:12]))).To(Succeed())
		writeTree(dir, map[string]string{"skills/delta/SKILL.md": "---\ndescription: D\n---\nD\n"})
		info, err := e.Info(ctx, engine.InfoRequest{Cwd: proj, Arg: dir, Scope: engine.ScopeRequest{Cwd: proj, User: true}, Targets: []string{"claude-code"},
			Rerun: func(extra ...string) string { return "lola info" }, UserRun: "lola info --user",
			Install: func(arg, ref, path string, k engine.TrustKinds) string { return "lola install " + arg }})
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Install[0].Components).To(HaveLen(2))
		Expect(info.Install[0].Components).To(HaveEach(HaveField("New", false)), "what the installed version held is unknown")
	})

	It("lola info warns that it cannot tell what is new when the cache holds the installed version but cannot read it, and calls nothing new", func() {
		dir := filepath.Join(tmp, "src", "kit")
		writeTree(dir, map[string]string{"lola.yml": "name: kit\n", "skills/alpha/SKILL.md": "---\ndescription: A\n---\nA\n"})
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + dir + "}\n"})
		user := engine.Scope{Kind: target.User}
		sync(user)
		led, err := e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		origin := filepath.Join(p.Store, "kit", "versions", strings.TrimPrefix(led.Modules["kit"].Digest, "sha256:")[:12], ".lola-origin")
		Expect(os.WriteFile(origin, []byte("{\n"), 0o644)).To(Succeed())
		writeTree(dir, map[string]string{"skills/delta/SKILL.md": "---\ndescription: D\n---\nD\n"})
		info, err := e.Info(ctx, engine.InfoRequest{Cwd: proj, Arg: dir, Scope: engine.ScopeRequest{Cwd: proj, User: true}, Targets: []string{"claude-code"},
			Rerun: func(extra ...string) string { return "lola info" }, UserRun: "lola info --user",
			Install: func(arg, ref, path string, k engine.TrustKinds) string { return "lola install " + arg }})
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Install[0].Refusal).To(BeNil())
		Expect(info.Install[0].Notices).To(ContainElement(And(HaveField("Level", engine.LevelWarn), HaveField("Text", HavePrefix("kit: cannot tell which components are new: ")))))
		Expect(info.Install[0].Components).To(HaveLen(2))
		Expect(info.Install[0].Components).To(HaveEach(HaveField("New", false)))
	})

	It("a profile entry's picks apply through check, the install choices and their preview", func() {
		dir := kit()
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n" +
			"  - {name: kit, source: " + dir + ", only: [skill/a, skill/b, skill/c], skip: [skill/c]}\n"})
		writeTree(filepath.Dir(p.Selections), map[string]string{"selections.yaml": "version: 1\nglobal:\n  kit:\n    skip: [skill/b]\n" +
			"projects:\n  " + proj + ":\n    kit:\n      skip: [skill/a]\n"})
		led := ledger.New()
		led.Profile = &ledger.Active{Name: "user:go", By: ledger.ByOn}
		Expect(led.Save(filepath.Join(proj, ".lola", "lock.json"))).To(Succeed())
		sync(sc)

		rep, err := e.Check(ctx, sc, proj, []string{dir}, []string{"claude-code"}, nil, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(rep.Modules).To(HaveExactElements(And(HaveField("Skipped", []string{"skill/b", "skill/c"}), HaveField("SkippedIn", "profile go and selections.yaml"))))

		ch, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: []engine.ScopeChoice{{Scope: sc, Source: engine.SourceFlag}},
			Modules: []engine.InstallRequest{{Arg: dir}}})
		Expect(err).NotTo(HaveOccurred())
		ms := ch.Module("kit").Scope(sc.Key())
		Expect(ms).To(And(HaveField("Excluded", []string{"skill/b", "skill/c"}), HaveField("ExcludedIn", "profile go and selections.yaml"), HaveField("OnlyFrom", "profile go"),
			HaveField("Current", []string{"skill/a"}), HaveField("NotInOnly", []string{}), HaveField("New", []string{}), HaveField("Provider", "profile go")))

		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + dir + ", only: [skill/a]}\n"})
		writeTree(dir, map[string]string{"skills/d/SKILL.md": "---\nname: d\ndescription: D\n---\nD\n"})
		ch, err = e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: []engine.ScopeChoice{{Scope: sc, Source: engine.SourceFlag}},
			Modules: []engine.InstallRequest{{Arg: dir}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(ch.Module("kit").Scope(sc.Key())).To(And(HaveField("Excluded", []string{"skill/b", "skill/c", "skill/d"}),
			HaveField("NotInOnly", []string{"skill/c", "skill/d"}), HaveField("ExcludedIn", "profile go and selections.yaml"), HaveField("New", []string{"skill/d"})),
			"b by the global skip and go's only: list; c and d by go's list alone; d is what the fetched version adds")

		led, err = e.Ledger(sc)
		Expect(err).NotTo(HaveOccurred())
		installed := filepath.Join(p.Store, "kit", "versions", strings.TrimPrefix(led.Modules["kit"].Digest, "sha256:")[:12])
		Expect(os.WriteFile(filepath.Join(installed, ".lola-origin"), []byte("{\n"), 0o644)).To(Succeed())
		ch, err = e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: []engine.ScopeChoice{{Scope: sc, Source: engine.SourceFlag}},
			Modules: []engine.InstallRequest{{Arg: dir}}})
		Expect(err).NotTo(HaveOccurred(), "an installed version the cache holds but cannot read does not stop the choices")
		Expect(ch.Module("kit").Scope(sc.Key())).To(And(HaveField("NotInOnly", []string{"skill/c", "skill/d"}), HaveField("New", []string{})))
		Expect(os.RemoveAll(installed)).To(Succeed())
		ch, err = e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: []engine.ScopeChoice{{Scope: sc, Source: engine.SourceFlag}},
			Modules: []engine.InstallRequest{{Arg: dir}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(ch.Module("kit").Scope(sc.Key())).To(And(HaveField("NotInOnly", []string{"skill/c", "skill/d"}), HaveField("New", []string{})),
			"with the installed version no longer cached, nothing is called new")

		pv, err := ch.Preview(engine.InstallSpec{Cwd: proj, Modules: []engine.InstallRequest{{Arg: dir}},
			Scopes: []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: sc, Source: engine.SourceFlag}}}}, []string{"kit"})
		Expect(err).NotTo(HaveOccurred())
		Expect(pv.Selected[sc.Key()]["kit"]).To(Equal([]string{"skill/a"}))
	})

	It("names on the install choices your default as the layer that provides a module it lists", func() {
		dir := kit()
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + dir + "}\n"})
		user := engine.Scope{Kind: target.User}
		sync(user)
		ch, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: []engine.ScopeChoice{{Scope: user, Source: engine.SourceFlag}},
			Modules: []engine.InstallRequest{{Arg: dir}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(ch.Module("kit").Scope(user.Key())).To(HaveField("Provider", "default (base)"))
	})

	It("a pulled change to a project profile entry's picks is a trust change, shown and asked", func() {
		kit()
		writeTree(proj, map[string]string{".lola/profiles/team.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: ../../mods/kit}\n"})
		led := ledger.New()
		led.Profile = &ledger.Active{Name: "project:team", By: ledger.ByOn}
		Expect(led.Save(filepath.Join(proj, ".lola", "lock.json"))).To(Succeed())
		sync(sc)
		writeTree(proj, map[string]string{".lola/profiles/team.yml": "version: 1\ntargets: [claude-code]\nmodules:\n" +
			"  - {name: kit, source: ../../mods/kit, only: [skill/a]}\n"})
		var asked engine.TrustReport
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, ConfirmRemovals: func([]engine.Removal) (bool, error) { return true, nil },
			Trust: func(r engine.TrustReport) (engine.TrustAnswer, error) {
				asked = r
				return engine.TrustAnswer{All: true}, nil
			}})
		Expect(err).NotTo(HaveOccurred())
		Expect(asked.Changes).To(ContainElement(And(HaveField("Module", "kit"), HaveField("Profile", "project:team"))))
		Expect(skills(proj)).To(Equal([]string{"a"}))
	})
})

var _ = Describe("Target-narrowing: an install whose -t leaves out a target the module is installed on", func() {
	It("Target-narrowing: plans the dropped target's deletions, asks about them as a pick change's, and leaves nothing for sync", func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e := engine.New(p, targets.Builtin())
		ctx := context.Background()
		proj, mod := filepath.Join(tmp, "proj"), filepath.Join(tmp, "m")
		writeTree(mod, map[string]string{"lola.yml": "name: m\n", "commands/hi.md": "Hi\n"})
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		sc := engine.Scope{Kind: target.Project, ProjectDir: proj}
		spec := func(arg string, ts ...string) engine.InstallSpec {
			return engine.InstallSpec{Cwd: proj, Modules: []engine.InstallRequest{{Arg: arg}}, Options: engine.Options{Targets: ts},
				Scopes: []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: sc, Source: engine.SourceFlag}, Targets: ts}}}
		}
		_, err = e.InstallAll(ctx, spec(mod, "claude-code", "gemini-cli"))
		Expect(err).NotTo(HaveOccurred())
		gemini := filepath.Join(proj, ".gemini", "commands", "hi.toml")
		Expect(gemini).To(BeAnExistingFile())
		dropped := engine.Removal{Module: "m", Targets: []string{"gemini-cli"}, Paths: []engine.RemovedPath{{Target: "gemini-cli", Path: ".gemini/commands/hi.toml"}},
			Why: "target dropped by -t", Scope: "project"}

		_, err = e.InstallAll(ctx, spec("m", "claude-code"))
		refused, ok := errors.AsType[*engine.DeletesRefusedError](err)
		Expect(ok).To(BeTrue(), "%v", err)
		Expect(refused.Removals).To(Equal([]engine.Removal{dropped}))
		Expect(gemini).To(BeAnExistingFile(), "nothing was written")

		narrow := spec("m", "claude-code")
		var asked [][]engine.Removal
		narrow.ConfirmRemovals = func(rs []engine.Removal) (bool, error) { asked = append(asked, rs); return true, nil }
		res, err := e.InstallAll(ctx, narrow)
		Expect(err).NotTo(HaveOccurred())
		Expect(asked).To(Equal([][]engine.Removal{{dropped}}))
		Expect(res.Steps).To(ContainElement(And(HaveField("Action", plan.ActionDelete), HaveField("Target", "gemini-cli"))))
		Expect(gemini).NotTo(BeAnExistingFile())
		led, err := e.Ledger(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Modules["m"].Targets).To(Equal([]string{"claude-code"}))
		synced, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{DryRun: true}})
		Expect(err).NotTo(HaveOccurred())
		Expect(synced.InSync).To(BeTrue(), "the next sync changes nothing")

		_, err = e.InstallAll(ctx, spec("m", "claude-code", "gemini-cli"))
		Expect(err).NotTo(HaveOccurred(), "a widening -t asks nothing")
		Expect(gemini).To(BeAnExistingFile())
	})
})
