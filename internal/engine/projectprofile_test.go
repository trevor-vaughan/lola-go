package engine_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
	"github.com/trevor-vaughan/lola-go/internal/trust"
)

var _ = Describe("project profiles", func() {
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
		// Two folder modules inside the project, which a profile in .lola/profiles/ names relative
		// to itself.
		writeTree(proj, map[string]string{"mods/a/lola.yml": "name: a\n", "mods/a/commands/a.md": "A\n",
			"mods/b/lola.yml": "name: b\n", "mods/b/commands/b.md": "B\n"})
	})
	profile := func(name, content string) string {
		GinkgoHelper()
		writeTree(proj, map[string]string{".lola/profiles/" + name + ".yml": content})
		return filepath.Join(proj, ".lola", "profiles", name+".yml")
	}
	// activate turns profiles on the way lola profile --on records it, in lock.json.
	activate := func(names ...string) {
		GinkgoHelper()
		led, err := e.Ledger(sc)
		Expect(err).NotTo(HaveOccurred())
		led.Profiles = nil
		for _, n := range names {
			led.Profiles = append(led.Profiles, ledger.Active{Name: n, By: ledger.ByOn})
		}
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		Expect(led.Save(filepath.Join(proj, ".lola", "lock.json"))).To(Succeed())
	}
	activeNames := func() []string {
		GinkgoHelper()
		led, err := e.Ledger(sc)
		Expect(err).NotTo(HaveOccurred())
		out := []string{}
		for _, a := range led.Profiles {
			out = append(out, a.Name)
		}
		return out
	}
	trustAll := func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }
	yes := func([]engine.Removal) (bool, error) { return true, nil }
	record := func() trust.View {
		GinkgoHelper()
		r, err := e.Trust.Load(proj)
		Expect(err).NotTo(HaveOccurred())
		return r.View
	}

	It("a pulled change to an active project profile needs trust; an inactive one does not", func() {
		profile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: ../../mods/a}\n")
		docs := profile("docs", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: b, source: ../../mods/b}\n")
		activate("project:go")

		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc})
		refusal, ok := errors.AsType[*engine.TrustRefusalError](err)
		Expect(ok).To(BeTrue(), "%v", err)
		Expect(err).To(MatchError(ContainSubstring(filepath.Join(".lola", "profiles", "go.yml") + ", which has not been trusted on this machine")))
		Expect(refusal.Report.Changes).To(ConsistOf(And(HaveField("Module", "a"), HaveField("Profile", "project:go"),
			HaveField("Kind", trust.ChangeAdded))))
		Expect(filepath.Join(proj, ".claude", "commands", "a.md")).NotTo(BeAnExistingFile())

		res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Trust.Recorded).To(BeTrue())
		Expect(os.ReadFile(filepath.Join(proj, ".claude", "commands", "a.md"))).To(Equal([]byte("A\n")))
		Expect(record().Part("go").Modules).To(Equal([]trust.Entry{{Name: "a", Source: filepath.Join("mods", "a"), Targets: []string{"claude-code"}}}),
			"the entry as it installs: its source relative to the project, the profile's targets")
		Expect(record().Part("").Modules).To(BeEmpty(), "project.yml's part is not touched")
		led, err := e.Ledger(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Modules["a"].From).To(Equal("profile:project:go"))

		// A pull changes the profile that is off: nothing to review until it is turned on.
		Expect(os.WriteFile(docs, []byte("version: 1\ntargets: [claude-code]\nmodules:\n  - {name: b, source: ../../mods/b, rules: false}\n"), 0o644)).To(Succeed())
		res, err = e.Sync(ctx, engine.SyncRequest{Scope: sc})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.InSync).To(BeTrue())

		// A pull changes the active one.
		profile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: ../../mods/a, rules: false}\n")
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc})
		refusal, ok = errors.AsType[*engine.TrustRefusalError](err)
		Expect(ok).To(BeTrue(), "%v", err)
		Expect(err).To(MatchError(ContainSubstring("needs your trust for changes to " + filepath.Join(".lola", "profiles", "go.yml"))))
		Expect(refusal.Report.Changes).To(ConsistOf(And(HaveField("Module", "a"), HaveField("Profile", "project:go"),
			HaveField("Kind", trust.ChangeChanged), HaveField("Fields", ConsistOf(trust.FieldChange{Field: "rules", From: nil, To: false})))))
		rres, err := e.Render(ctx, sc, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(rres.Findings).To(ContainElement(And(HaveField("Module", "a"), HaveField("Code", engine.CodeNotTrusted),
			HaveField("Text", "profile:project:go changed for it since you last trusted it"))), "render leaves it as installed")
	})

	It("a profile with a hostile ref or source refuses before git runs", func() {
		for _, c := range []struct{ ent, why string }{
			{"{name: a, source: 'https://example.com/a.git', ref: '--upload-pack=touch PWNED'}", `module a: ref "--upload-pack=touch PWNED" starts with '-'`},
			{"{name: a, source: 'ext::sh -c touch% PWNED'}", `module a: source "ext::sh -c touch% PWNED" names a git remote helper`},
			{"{name: a, source: 'fd::0'}", `module a: source "fd::0" names a git remote helper`},
			{"{name: a, source: 'git://example.com/a.git'}", "module a: a git:// source is refused"},
		} {
			ent, why := c.ent, c.why
			path := profile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - "+ent+"\n")
			activate("project:go")
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll, ConfirmRemovals: yes})
			Expect(err).To(MatchError(And(HavePrefix(path+":4: "), ContainSubstring(why))), "refused as the profile loads, before git")
			_, err = e.Render(ctx, sc, engine.Options{})
			Expect(err).To(MatchError(ContainSubstring(why)), ent)
			for _, dir := range []string{proj, tmp} {
				Expect(filepath.Join(dir, "PWNED")).NotTo(BeAnExistingFile(), ent)
			}
		}
	})

	It("checks a project profile's local sources against the project, as project.yml's", func() {
		outside := filepath.Join(tmp, "outside")
		writeTree(outside, map[string]string{"lola.yml": "name: a\n", "commands/a.md": "Read the user's files\n"})
		cases := map[string]string{"climbing out": "../../../outside", "absolute": outside}
		if runtime.GOOS != "windows" {
			Expect(os.Symlink(outside, filepath.Join(proj, "link"))).To(Succeed())
			cases["through a link in the project"] = "../../link"
		}
		for what, src := range cases {
			profile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: '"+src+"'}\n")
			activate("project:go")
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll, ConfirmRemovals: yes})
			Expect(err).To(MatchError(ContainSubstring("resolves outside the project")), what)
			Expect(filepath.Join(proj, ".claude", "commands", "a.md")).NotTo(BeAnExistingFile(), what)
		}
	})

	It("names the profile whose entry asks for another source than the cache holds", func() {
		other := filepath.Join(tmp, "other", "a")
		writeTree(other, map[string]string{"lola.yml": "name: a\n", "commands/a.md": "other A\n"})
		_, err := e.Install(ctx, engine.Scope{Kind: target.User}, proj, []engine.InstallRequest{{Arg: other}}, engine.Options{Targets: []string{"claude-code"}})
		Expect(err).NotTo(HaveOccurred())
		profile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: ../../mods/a}\n")
		activate("project:go")
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll})
		Expect(err).To(MatchError(ContainSubstring("a in your cache comes from " + other + ", and " + filepath.Join(".lola", "profiles", "go.yml") + " asks for ")))
	})

	It("refuses a .lola/profiles/ that leads out of the project before reading a profile", func() {
		if runtime.GOOS == "windows" {
			Skip("symbolic links need privileges on Windows")
		}
		elsewhere := filepath.Join(tmp, "elsewhere")
		writeTree(elsewhere, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: ../../mods/a}\n"})
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		Expect(os.Symlink(elsewhere, filepath.Join(proj, ".lola", "profiles"))).To(Succeed())
		activate("project:go")
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll})
		Expect(err).To(MatchError(ContainSubstring(filepath.Join(proj, ".lola", "profiles") + " is not a directory of the project's own")))
		_, _, err = e.Status(ctx, sc, nil)
		Expect(err).To(MatchError(ContainSubstring("is not a directory of the project's own")))
	})

	It("records trust for the entries accepted, in each file's part, and carries the profiles the run does not check", func() {
		profile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: ../../mods/a}\n")
		profile("docs", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: b, source: ../../mods/b}\n")
		activate("project:docs")
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll})
		Expect(err).NotTo(HaveOccurred())
		docsPart := record().Part("docs")
		Expect(docsPart.Modules).To(HaveLen(1))

		// docs goes off and go comes on: only go's part is checked and recorded.
		activate("project:go")
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll, ConfirmRemovals: yes})
		Expect(err).NotTo(HaveOccurred())
		Expect(record().Part("go").Modules).To(HaveLen(1))
		Expect(record().Part("docs")).To(Equal(docsPart), "an inactive profile's record is carried, never reported removed")
	})

	It("a shadowed project-profile entry is reviewed with its content when project.yml drops it", func() {
		writeTree(proj, map[string]string{"mods/a/mcps.json": `{"mcpServers":{"gh":{"command":"gh-mcp","args":["--stdio"]}}}`,
			".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: mods/a}\n"})
		profile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: ../../mods/a, rules: false}\n")
		activate("project:go")
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll})
		Expect(err).NotTo(HaveOccurred())
		Expect(record().Part("go").Modules).To(BeEmpty(), "the shadowed entry was neither shown nor accepted")

		// A pull drops a from project.yml: the profile's entry wins now, and is asked about with its content.
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\n"})
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, ConfirmRemovals: yes})
		refusal, ok := errors.AsType[*engine.TrustRefusalError](err)
		Expect(ok).To(BeTrue(), "%v", err)
		Expect(refusal.Report.Changes).To(ContainElement(And(HaveField("Module", "a"), HaveField("Profile", "project:go"),
			HaveField("Kind", trust.ChangeAdded), HaveField("MCPServers", ContainElement(HaveField("Name", "gh"))))))
	})

	It("a shadowed project-profile entry is reviewed with its content when a higher profile goes off", func() {
		writeTree(proj, map[string]string{"mods/a/mcps.json": `{"mcpServers":{"gh":{"command":"gh-mcp","args":["--stdio"]}}}`})
		writeTree(p.UserProfiles, map[string]string{"hi.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: " +
			filepath.Join(proj, "mods", "a") + "}\n"})
		profile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: ../../mods/a, rules: false}\n")
		activate("project:go", "user:hi")
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll})
		Expect(err).NotTo(HaveOccurred())
		Expect(record().Part("go").Modules).To(BeEmpty())

		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOff, Names: []string{"user:hi"}},
			ConfirmRemovals: yes})
		refusal, ok := errors.AsType[*engine.TrustRefusalError](err)
		Expect(ok).To(BeTrue(), "%v", err)
		Expect(refusal.Report.Changes).To(ConsistOf(And(HaveField("Module", "a"), HaveField("Profile", "project:go"),
			HaveField("MCPServers", ContainElement(HaveField("Name", "gh"))))))
		led, err := e.Ledger(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Profiles).To(HaveLen(2), "nothing changes unasked")

		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOff, Names: []string{"user:hi"}},
			Trust: trustAll, ConfirmRemovals: yes})
		Expect(err).NotTo(HaveOccurred())
		Expect(record().Part("go").Modules).To(ConsistOf(HaveField("Name", "a")))
	})

	It("drops a deleted profile's part from the record when the project's record is saved", func() {
		profile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: ../../mods/a}\n")
		docs := profile("docs", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: b, source: ../../mods/b}\n")
		activate("project:docs")
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll})
		Expect(err).NotTo(HaveOccurred())
		Expect(record().Profiles).To(HaveKey("docs"))

		Expect(os.Remove(docs)).To(Succeed())
		activate("project:go")
		res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll, ConfirmRemovals: yes})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Trust.Changes).NotTo(ContainElement(HaveField("Profile", "project:docs")), "never reported removed")
		Expect(record().Profiles).To(HaveKey("go"))
		Expect(record().Profiles).NotTo(HaveKey("docs"))
	})

	It("names no module of a project profile for completion, and never reads one", func() {
		profile("go", "version: 9\n")
		activate("project:go")
		names, err := e.SyncNames(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(names).To(BeEmpty())
	})

	It("coaches install without arguments only once lock.json is known untracked", func() {
		profile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: ../../mods/a}\n")
		activate("project:go")
		notice, _, err := e.InstallCoaching(ctx, sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(notice).To(Equal("1 module in profile:project:go is not installed → lola sync --project"))
		gitIn(proj, "init", "-q")
		gitIn(proj, "add", "-f", ".lola/lock.json")
		_, _, err = e.InstallCoaching(ctx, sc)
		Expect(err).To(MatchError(ContainSubstring("lock.json")))
	})

	It("an ambiguous bare profile name refuses with both qualified names", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		theirs := profile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: ../../mods/a}\n")
		ambiguous := "go is ambiguous: your profile user:go (" + filepath.Join(p.UserProfiles, "go.yml") +
			") and the project's project:go (" + theirs + ") are both named go"
		turn := func(action engine.ProfileAction, names ...string) (*engine.SyncResult, error) {
			return e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: action, Names: names}, Trust: trustAll,
				ConfirmRemovals: yes})
		}
		_, err := turn(engine.ProfileOn, "go")
		Expect(err).To(MatchError(ambiguous))
		Expect(errs.Hints(err)).To(Equal([]string{"yours: lola profile --project user:go --on",
			"the project's: lola profile --project project:go --on"}))
		_, err = turn(engine.ProfileSwitch, "go", "docs")
		Expect(err).To(MatchError(ambiguous))
		Expect(errs.Hints(err)).To(Equal([]string{"yours: lola profile --project user:go docs --switch",
			"the project's: lola profile --project project:go docs --switch"}), "the other names typed are kept")
		_, _, err = e.ProfileState(ctx, []engine.Scope{sc}, "go")
		Expect(err).To(MatchError(ambiguous), "never one scope's answer for the other's")
		Expect(errs.Hints(err)).To(Equal([]string{"yours: lola profile --project user:go",
			"the project's: lola profile --project project:go"}))

		states, _, err := e.ProfileState(ctx, []engine.Scope{{Kind: target.User}}, "go")
		Expect(err).NotTo(HaveOccurred())
		Expect(states).To(ConsistOf(HaveField("Kind", "user")), "user scope sees no project profile")

		res, err := turn(engine.ProfileOn, "project:go")
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Activation.Profiles).To(Equal([]string{"project:go"}))
		Expect(os.ReadFile(filepath.Join(proj, ".claude", "commands", "a.md"))).To(Equal([]byte("A\n")))
		_, err = turn(engine.ProfileOn, "user:go")
		Expect(err).NotTo(HaveOccurred())
		Expect(activeNames()).To(Equal([]string{"project:go", "user:go"}))

		// Both gone: --off by the bare name cannot tell them apart either.
		Expect(os.Remove(filepath.Join(p.UserProfiles, "go.yml"))).To(Succeed())
		Expect(os.Remove(filepath.Join(proj, ".lola", "profiles", "go.yml"))).To(Succeed())
		_, err = turn(engine.ProfileOff, "go")
		Expect(err).To(MatchError("go is ambiguous: user:go and project:go are both on, and neither file exists any more"))
		Expect(errs.Hints(err)).To(Equal([]string{"yours: lola profile --project user:go --off",
			"the project's: lola profile --project project:go --off"}))
		_, err = turn(engine.ProfileOff, "project:go")
		Expect(err).NotTo(HaveOccurred())
		_, err = turn(engine.ProfileOff, "go")
		Expect(err).NotTo(HaveOccurred(), "a gone profile goes off by its bare name once it is the only one")
		Expect(activeNames()).To(BeEmpty())
	})

	It("turning on a project profile leaves nothing for the next sync to ask about", func() {
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: mods/a}\n"})
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll})
		Expect(err).NotTo(HaveOccurred())
		profile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: ../../mods/a, rules: false}\n  - {name: b, source: ../../mods/b}\n")
		res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOn, Names: []string{"go"}}, Trust: trustAll})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Trust.Changes).To(ConsistOf(HaveField("Module", "b")), "a, which project.yml overrides, is not asked about")
		res, err = e.Sync(ctx, engine.SyncRequest{Scope: sc})
		Expect(err).NotTo(HaveOccurred(), "off a terminal: nothing left to trust")
		Expect(res.InSync).To(BeTrue())
	})

	It("refuses --off of a bare name both kinds define, even when only one of them is on", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		theirs := profile("go", "version: 1\n")
		activate("project:go")
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOff, Names: []string{"go"}}, ConfirmRemovals: yes})
		Expect(err).To(MatchError("go is ambiguous: the project's project:go (" + theirs + ") is on and your profile user:go (" +
			filepath.Join(p.UserProfiles, "go.yml") + ") is off"))
		Expect(errs.Hints(err)).To(Equal([]string{"turn it off: lola profile --project project:go --off"}), "never a no-op")
		Expect(activeNames()).To(Equal([]string{"project:go"}))
	})

	It("refuses --off of a bare name both kinds define when yours is on, hinting only its --off with the rest of the command", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		theirs := profile("go", "version: 1\n")
		activate("user:go")
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOff, Names: []string{"go", "docs"}}, ConfirmRemovals: yes})
		Expect(err).To(MatchError("go is ambiguous: your profile user:go (" + filepath.Join(p.UserProfiles, "go.yml") +
			") is on and the project's project:go (" + theirs + ") is off"))
		Expect(errs.Hints(err)).To(Equal([]string{"turn it off: lola profile --project user:go docs --off"}))
		Expect(activeNames()).To(Equal([]string{"user:go"}))
	})

	It("refuses --off of a bare name both kinds define and both have on, with a hint for each", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		theirs := profile("go", "version: 1\n")
		activate("user:go", "project:go")
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOff, Names: []string{"go"}}, ConfirmRemovals: yes})
		Expect(err).To(MatchError("go is ambiguous: your profile user:go (" + filepath.Join(p.UserProfiles, "go.yml") +
			") and the project's project:go (" + theirs + ") are both named go"))
		Expect(errs.Hints(err)).To(Equal([]string{"yours: lola profile --project user:go --off",
			"the project's: lola profile --project project:go --off"}))
		Expect(activeNames()).To(Equal([]string{"user:go", "project:go"}))
	})

	It("--off of a bare name both kinds define with neither on turns the other names off, its notice in typed order", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		profile("go", "version: 1\n")
		profile("docs", "version: 1\n")
		profile("api", "version: 1\n")
		activate("project:docs")
		off := func(names ...string) *engine.SyncResult {
			res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOff, Names: names}, ConfirmRemovals: yes})
			Expect(err).NotTo(HaveOccurred())
			return res
		}
		res := off("go", "docs")
		Expect(res.Notices).To(ConsistOf(engine.Notice{Level: engine.LevelInfo, Text: "neither user:go nor project:go is on"}))
		Expect(activeNames()).To(BeEmpty())
		Expect(off("api", "go").Notices).To(Equal([]engine.Notice{{Level: engine.LevelInfo, Text: "project:api is not on"},
			{Level: engine.LevelInfo, Text: "neither user:go nor project:go is on"}}))
		Expect(off("go", "api").Notices).To(Equal([]engine.Notice{{Level: engine.LevelInfo, Text: "neither user:go nor project:go is on"},
			{Level: engine.LevelInfo, Text: "project:api is not on"}}))
	})

	It("refuses a bare name typed twice before resolving it, and never hints a name twice", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		profile("go", "version: 1\n")
		activate("user:go")
		turn := func(action engine.ProfileAction, names ...string) error {
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: action, Names: names}, ConfirmRemovals: yes})
			return err
		}
		err := turn(engine.ProfileOff, "go", "go")
		Expect(err).To(MatchError("go is named twice"))
		Expect(errs.Hints(err)).To(Equal([]string{"name each profile once"}))
		Expect(turn(engine.ProfileOff, "user:go", "user:go")).To(MatchError("user:go is named twice"))
		err = turn(engine.ProfileOn, "go", "user:go")
		Expect(err).To(MatchError(ContainSubstring("go is ambiguous")))
		Expect(errs.Hints(err)).To(Equal([]string{"yours: lola profile --project user:go --on",
			"the project's: lola profile --project project:go user:go --on"}))
		Expect(activeNames()).To(Equal([]string{"user:go"}))
	})

	It("keeps the run's flags in an ambiguous name's hints", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		profile("go", "version: 1\n")
		activate("user:go")
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOff, Names: []string{"go"}},
			Options: engine.Options{DryRun: true, Trust: engine.TrustHooks}, ConfirmRemovals: yes})
		Expect(errs.Hints(err)).To(Equal([]string{"turn it off: lola profile --project user:go --off --trust=hooks -n"}))
	})

	It("--off of a bare name both kinds define with neither on is a notice, as --off of any profile that is off", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		profile("go", "version: 1\n")
		res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOff, Names: []string{"go"}}, ConfirmRemovals: yes})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ConsistOf(engine.Notice{Level: engine.LevelInfo, Text: "neither user:go nor project:go is on"}))
		Expect(res.InSync).To(BeTrue())
		Expect(activeNames()).To(BeEmpty())
	})

	It("refuses --off of a bare name whose active profile is gone while the other kind's exists", func() {
		mine := filepath.Join(p.UserProfiles, "go.yml")
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		theirs := profile("go", "version: 1\n")
		activate("user:go")
		Expect(os.Remove(mine)).To(Succeed())
		off := func(name string) error {
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOff, Names: []string{name}}, ConfirmRemovals: yes})
			return err
		}
		err := off("go")
		Expect(err).To(MatchError("go is ambiguous: user:go is on but its file " + mine + " is gone, and project:go (" + theirs + ") is off"))
		Expect(errs.Hints(err)).To(Equal([]string{"turn it off: lola profile --project user:go --off",
			"to use the project's instead: lola profile --project project:go --on"}))
		Expect(activeNames()).To(Equal([]string{"user:go"}))
		profile("docs", "version: 1\n")
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOff, Names: []string{"docs", "go"}}, ConfirmRemovals: yes})
		Expect(errs.Hints(err)).To(Equal([]string{"turn it off: lola profile --project docs user:go --off",
			"to use the project's instead: lola profile --project project:go --on"}), "the rest of the command is kept")
		Expect(off("user:go")).To(Succeed())
		Expect(activeNames()).To(BeEmpty())
	})

	It("refuses --off of a bare name whose active project profile is gone while yours exists, hinting only its --off", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		theirs := profile("go", "version: 1\n")
		activate("project:go")
		Expect(os.Remove(theirs)).To(Succeed())
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOff, Names: []string{"go"}}, ConfirmRemovals: yes})
		Expect(err).To(MatchError("go is ambiguous: project:go is on but its file " + theirs + " is gone, and user:go (" +
			filepath.Join(p.UserProfiles, "go.yml") + ") is off"))
		Expect(errs.Hints(err)).To(Equal([]string{"turn it off: lola profile --project project:go --off"}),
			"turning yours on would leave the project's on, so it is not offered as an instead")
		Expect(activeNames()).To(Equal([]string{"project:go"}))
	})

	It("refuses --off of a bare name whose two active profiles are both gone, with a hint for each", func() {
		activate("user:go", "project:go")
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOff, Names: []string{"go", "docs"}}, ConfirmRemovals: yes})
		Expect(err).To(MatchError("go is ambiguous: user:go and project:go are both on, and neither file exists any more"))
		Expect(errs.Hints(err)).To(Equal([]string{"yours: lola profile --project user:go docs --off",
			"the project's: lola profile --project project:go docs --off"}))
		Expect(activeNames()).To(Equal([]string{"user:go", "project:go"}))
	})

	It("names a project profile's file for output without control characters", func() {
		Expect(engine.ProfileFile("project:a\x1b[31mb")).To(Equal(filepath.Join(".lola", "profiles", "a?[31mb.yml")))
	})

	It("a project cannot define default", func() {
		def := profile("default", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: ../../mods/a}\n")
		profile("go", "version: 1\n")
		rows, notices, err := e.Profiles(ctx, sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(ConsistOf(And(HaveField("Name", "go"), HaveField("Kind", "project"), HaveField("Path", filepath.Join(proj, ".lola", "profiles", "go.yml")))))
		Expect(notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn, Text: "a project cannot define default; rename " + def}))
		on := func(name string) error {
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOn, Names: []string{name}}, Trust: trustAll})
			return err
		}
		Expect(on("default")).To(MatchError("default applies to user scope only"))
		Expect(on("project:default")).To(MatchError(ContainSubstring("no profile project:default for the project at " + proj)))
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: engine.Scope{Kind: target.User}, ConfirmRemovals: yes})
		Expect(err).To(MatchError(ContainSubstring("no user.yml at")), "nothing to sync at user scope: a project's default.yml is not the user's")
		Expect(filepath.Join(proj, ".claude", "commands", "a.md")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(p.Home, ".claude", "commands", "a.md")).NotTo(BeAnExistingFile())
	})

	It("lists, shows and turns a project's profiles on and off by name", func() {
		profile("go", "version: 1\ndescription: Go\ntargets: [claude-code]\nmodules:\n  - {name: a, source: ../../mods/a}\n")
		writeTree(proj, map[string]string{".lola/profiles/Bad Name.yml": ""})
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOn, Names: []string{"go"}}})
		Expect(err).To(MatchError(ContainSubstring("lola profile needs your trust for "+filepath.Join(".lola", "profiles", "go.yml"))),
			"turning a project profile on needs trust, before anything is fetched")
		Expect(activeNames()).To(BeEmpty())
		res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOn, Names: []string{"go"}}, Trust: trustAll})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Activation.Named).To(Equal([]string{"project:go"}))
		rows, notices, err := e.Profiles(ctx, sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(ConsistOf(And(HaveField("Name", "go"), HaveField("Kind", "project"), HaveField("Active", true), HaveField("Qualified", "project:go"))))
		Expect(notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
			Text: "1 file with an invalid name in " + filepath.Join(proj, ".lola", "profiles") + " is not a profile"}))
		states, _, err := e.ProfileState(ctx, []engine.Scope{sc}, "project:go")
		Expect(err).NotTo(HaveOccurred())
		Expect(states[0].Modules).To(Equal([]engine.ProfileModule{{Name: "a", From: "profile:project:go", Overrides: []string{}}}))

		Expect(os.Remove(filepath.Join(proj, ".lola", "profiles", "go.yml"))).To(Succeed())
		_, notices, err = e.Profiles(ctx, sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
			Text: "profile project:go is on but no longer exists → lola profile --project project:go --off"}))
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOff, Names: []string{"go"}}, ConfirmRemovals: yes})
		Expect(err).NotTo(HaveOccurred(), "a gone project profile goes off by its bare name")
		Expect(activeNames()).To(BeEmpty())
		Expect(filepath.Join(proj, ".claude", "commands", "a.md")).NotTo(BeAnExistingFile())
	})

	It("a project profile replacing an active profile is a trust change", func() {
		mine := filepath.Join(tmp, "mine", "c")
		writeTree(mine, map[string]string{"lola.yml": "name: c\n", "commands/c.md": "my C\n"})
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: c, source: " + mine + "}\n",
			"base.yml": "version: 1\n"})
		activate("user:go", "user:base")
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc})
		Expect(err).NotTo(HaveOccurred())
		installed := func(name string) bool {
			_, err := os.Stat(filepath.Join(proj, ".claude", "commands", name+".md"))
			return err == nil
		}
		Expect(installed("c")).To(BeTrue())

		// A pull adds the project's own go: lola sync never swaps it in, even with --trust, and says so.
		profile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: ../../mods/a}\n  - {name: b, source: ../../mods/b}\n")
		notice := engine.Notice{Level: engine.LevelWarn, Text: "project:go also exists → lola profile --project project:go --on"}
		for _, req := range []engine.SyncRequest{{Scope: sc}, {Scope: sc, Trust: trustAll, ConfirmRemovals: yes}, {Scope: sc, Modules: []string{"c"}}} {
			res, err := e.Sync(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.InSync).To(BeTrue())
			Expect(res.Notices).To(ContainElement(notice))
		}
		Expect(activeNames()).To(Equal([]string{"user:go", "user:base"}))
		_, notices, err := e.Profiles(ctx, sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(notices).To(ContainElement(notice))
		turn := func(trust func(engine.TrustReport) (engine.TrustAnswer, error)) (*engine.SyncResult, error) {
			return e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOn, Names: []string{"project:go"}},
				Trust: trust, ConfirmRemovals: yes})
		}

		// Turning it on is the replacement, shown and asked about as a change of its own.
		_, err = turn(nil)
		refusal, ok := errors.AsType[*engine.TrustRefusalError](err)
		Expect(ok).To(BeTrue(), "%v", err)
		Expect(err).To(MatchError(ContainSubstring("project:go replacing user:go")))
		Expect(refusal.Report.Replacements).To(Equal([]engine.Replacement{{Profile: "project:go", Replaces: "user:go", Modules: []string{"a", "b", "c"}}}))

		// Declined: the active profile stays, as do its files.
		_, err = turn(func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{}, nil })
		Expect(err).To(MatchError(engine.ErrDeclined))
		Expect(activeNames()).To(Equal([]string{"user:go", "user:base"}))
		Expect(installed("c")).To(BeTrue())
		Expect(installed("a") || installed("b")).To(BeFalse())

		// Accepted: project:go takes user:go's place in the list, and its entries win.
		res, err := turn(trustAll)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Trust.Replacements).To(HaveLen(1))
		Expect(activeNames()).To(Equal([]string{"project:go", "user:base"}), "in user:go's position")
		Expect(installed("a") && installed("b")).To(BeTrue())
		Expect(installed("c")).To(BeFalse(), "user:go's module is unloaded with it")
		res, err = e.Sync(ctx, engine.SyncRequest{Scope: sc})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.InSync).To(BeTrue())
		Expect(res.Notices).NotTo(ContainElement(notice), "nothing to say again")

		// --switch naming it replaces too.
		activate("user:go")
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, ConfirmRemovals: yes})
		Expect(err).NotTo(HaveOccurred())
		res, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileSwitch, Names: []string{"project:go"}},
			Trust: trustAll, ConfirmRemovals: yes})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Trust.Replacements).To(ConsistOf(HaveField("Replaces", "user:go")))
	})
})
