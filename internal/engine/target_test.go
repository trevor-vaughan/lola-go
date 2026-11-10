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
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
	"github.com/trevor-vaughan/lola-go/internal/trust"
)

var _ = Describe("the write target", func() {
	var (
		tmp, proj string
		p         paths.Paths
		e         *engine.Engine
		user, sc  engine.Scope
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
		user, sc = engine.Scope{Kind: target.User}, engine.Scope{Kind: target.Project, ProjectDir: proj}
	})
	mod := func(name string) string {
		GinkgoHelper()
		dir := filepath.Join(tmp, "mods", name)
		writeTree(dir, map[string]string{"lola.yml": "name: " + name + "\n", "commands/" + name + ".md": name + "\n"})
		return dir
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
		Expect(led.Save(path)).To(Succeed())
	}
	// install installs args at s for claude-code, its hints repeating the run as the CLI's do.
	install := func(s engine.Scope, req engine.TargetRequest, args ...engine.InstallRequest) (*engine.Result, error) {
		req.Rerun = func(extra ...string) string {
			return e.Command(s, "install", append([]string{args[0].Arg}, extra...)...)
		}
		return e.Install(ctx, s, tmp, args, engine.Options{Targets: []string{"claude-code"}, Target: req})
	}
	from := func(s engine.Scope, name string) string {
		GinkgoHelper()
		led, err := e.Ledger(s)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Modules).To(HaveKey(name))
		return led.Modules[name].From
	}
	read := func(path string) string {
		GinkgoHelper()
		b, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		return string(b)
	}
	// keepRemote writes a remote profile as lola profile --add does: profile.yml and source.json.
	keepRemote := func(name, content string) {
		GinkgoHelper()
		o := profile.Origin{Version: profile.OriginVersion, Source: "https://example.com/profiles.git", Commit: strings.Repeat("a", 40),
			Digest: profile.Digest([]byte(content))}
		b, err := json.Marshal(o)
		Expect(err).NotTo(HaveOccurred())
		writeTree(filepath.Join(p.RemoteProfiles, name), map[string]string{profile.RemoteFile: content, profile.OriginFile: string(b)})
	}

	It("writes the profile on, yours at user scope and in a project, and the base with none on", func() {
		kit := mod("kit")
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\n", "go.yml": "version: 1\n"})
		activate(user, "user:go")
		_, err := install(user, engine.TargetRequest{}, engine.InstallRequest{Arg: kit})
		Expect(err).NotTo(HaveOccurred())
		Expect(read(filepath.Join(p.UserProfiles, "go.yml"))).To(Equal("version: 1\nmodules:\n  - name: kit\n    source: "+kit+"\n"),
			"go has no targets of its own: default's apply, so none is written")
		Expect(from(user, "kit")).To(Equal("profile:user:go"))

		other := mod("other")
		_, err = install(sc, engine.TargetRequest{}, engine.InstallRequest{Arg: other})
		Expect(err).NotTo(HaveOccurred())
		Expect(read(filepath.Join(proj, ".lola", "project.yml"))).To(ContainSubstring("name: other"), "nothing on in the project: its base")
		activate(sc, "user:go")
		third := mod("third")
		_, err = install(sc, engine.TargetRequest{}, engine.InstallRequest{Arg: third})
		Expect(err).NotTo(HaveOccurred())
		Expect(read(filepath.Join(p.UserProfiles, "go.yml"))).To(ContainSubstring("name: third"))
		Expect(read(filepath.Join(proj, ".lola", "project.yml"))).NotTo(ContainSubstring("third"))
		Expect(from(sc, "third")).To(Equal("profile:user:go"))
	})

	It("--profile of a profile that is not on writes it, applies nothing, and says how to switch", func() {
		kit := mod("kit")
		writeTree(p.UserProfiles, map[string]string{"review.yml": "version: 1\ntargets: [claude-code]\n"})
		res, err := install(sc, engine.TargetRequest{Profile: "review"}, engine.InstallRequest{Arg: kit})
		Expect(err).NotTo(HaveOccurred())
		Expect(read(filepath.Join(p.UserProfiles, "review.yml"))).To(ContainSubstring("name: kit"))
		Expect(res.Steps).To(BeEmpty())
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo,
			Text: "saved to profile review; it is not on in this project → lola profile --project review --switch"}))
		led, err := e.Ledger(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Modules).NotTo(HaveKey("kit"))
	})

	It("--profile base writes the base under the profile on, and a remote profile is refused with the writable ones", func() {
		kit := mod("kit")
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n", "mine.yml": "version: 1\n"})
		keepRemote("team", "version: 1\n")
		activate(sc, "user:go")
		_, err := install(sc, engine.TargetRequest{Profile: "base"}, engine.InstallRequest{Arg: kit})
		Expect(err).NotTo(HaveOccurred())
		Expect(read(filepath.Join(proj, ".lola", "project.yml"))).To(ContainSubstring("name: kit"))
		Expect(from(sc, "kit")).To(Equal(""))

		_, err = install(sc, engine.TargetRequest{Profile: "remote:team"}, engine.InstallRequest{Arg: kit})
		Expect(err).To(MatchError("remote:team is read-only; lola does not edit remote profiles"))
		Expect(errs.Hints(err)).To(Equal([]string{
			"lola install --project " + kit + " --profile base", "lola install --project " + kit + " --profile user:go",
			"lola install --project " + kit + " --profile user:mine"}))

		_, err = install(sc, engine.TargetRequest{Profile: "default"}, engine.InstallRequest{Arg: kit})
		Expect(err).To(MatchError("default is user scope's base; this project's base is project.yml"))
		Expect(errs.Hints(err)).To(Equal([]string{"lola install --project " + kit + " --profile base"}))
	})

	It("asks before writing the base while a remote profile is on, and refuses off a terminal", func() {
		kit := mod("kit")
		keepRemote("team", "version: 1\n")
		activate(sc, "remote:team")
		_, err := install(sc, engine.TargetRequest{}, engine.InstallRequest{Arg: kit})
		Expect(err).To(MatchError("remote:team is read-only; lola install needs to know whether to write to the base (project.yml), and there is no terminal to ask on"))
		Expect(errs.Hints(err)).To(Equal([]string{"lola install --project " + kit + " --profile base",
			"or bundle it with a profile of yours: lola profile --project team-work --switch --new --bundle remote:team,team-extras"}))

		var asked []engine.WhereQuestion
		_, err = e.Install(ctx, sc, tmp, []engine.InstallRequest{{Arg: kit}}, engine.Options{Targets: []string{"claude-code"}, AskWhere: func(q engine.WhereQuestion) (engine.WhereChoice, error) {
			asked = append(asked, q)
			return q.Choices[0], nil
		}})
		Expect(err).NotTo(HaveOccurred())
		Expect(asked).To(Equal([]engine.WhereQuestion{{Scope: sc, Title: "remote:team is read-only.",
			Choices: []engine.WhereChoice{{Label: "Write to the base (project.yml)", Profile: "base"}}}}))
		Expect(read(filepath.Join(proj, ".lola", "project.yml"))).To(ContainSubstring("name: kit"))

		other := mod("other")
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileSwitch, Name: "team-work", New: true,
			Bundle: []string{"remote:team", "team-extras"}}})
		Expect(err).NotTo(HaveOccurred(), "the bundle hint runs as printed")
		_, err = install(sc, engine.TargetRequest{}, engine.InstallRequest{Arg: other})
		Expect(err).To(MatchError("lola install needs to know which profile of bundle team-work gets " + other + ", and there is no terminal to ask on"))
		Expect(errs.Hints(err)).To(Equal([]string{"lola install --project " + other + " --profile team-extras"}))
		_, err = install(sc, engine.TargetRequest{Profile: "team-extras"}, engine.InstallRequest{Arg: other})
		Expect(err).NotTo(HaveOccurred())
		Expect(read(filepath.Join(p.UserProfiles, "team-extras.yml"))).To(ContainSubstring("name: other"))
	})

	It("writes a project-relative local source into your profile as an absolute path, and into a project profile relative to .lola/profiles", func() {
		writeTree(proj, map[string]string{"tools/lola.yml": "name: tools\n", "tools/commands/tools.md": "T\n",
			".lola/profiles/team.yml": "version: 1\ntargets: [claude-code]\n"})
		writeTree(p.UserProfiles, map[string]string{"me.yml": "version: 1\ntargets: [claude-code]\n"})
		activate(sc, "user:me")
		_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: "./tools"}}, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(read(filepath.Join(p.UserProfiles, "me.yml"))).To(ContainSubstring("source: " + filepath.Join(proj, "tools") + "\n"))

		activate(sc, "project:team")
		// team's top-level targets are not trusted yet (nothing that installs to them is), so the
		// install names its assistant.
		_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: "./tools"}}, engine.Options{Targets: []string{"claude-code"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(read(filepath.Join(proj, ".lola", "profiles", "team.yml"))).To(ContainSubstring("source: " + filepath.Join("..", "..", "tools") + "\n"))
		rec, err := e.Trust.Load(proj)
		Expect(err).NotTo(HaveOccurred())
		Expect(rec.View.Part("team").Find("tools")).NotTo(BeNil(), "an entry lola writes into a project profile is trusted (line 47)")
	})

	It("refuses to rewrite a project profile entry the trust record does not cover", func() {
		kit := mod("kit")
		writeTree(proj, map[string]string{".lola/profiles/team.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + kit + "}\n"})
		activate(sc, "project:team")
		_, err := install(sc, engine.TargetRequest{}, engine.InstallRequest{Arg: "kit"})
		Expect(err).To(MatchError("kit: .lola/profiles/team.yml has not been trusted on this machine"))
		Expect(errs.Hints(err)).To(Equal([]string{"review and apply it: lola sync --project kit"}))

		_, err = install(sc, engine.TargetRequest{}, engine.InstallRequest{Arg: kit, Targets: []string{"claude-code"}})
		Expect(err).To(MatchError("kit: .lola/profiles/team.yml has not been trusted on this machine"),
			"adding assistants keeps the entry's other fields, so it is refused too, naming the profile's file")
	})

	It("a trusted project-profile entry without its own targets is covered", func() {
		kit := mod("kit")
		team := filepath.Join(proj, ".lola", "profiles", "team.yml")
		writeTree(proj, map[string]string{".lola/profiles/team.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + kit + "}\n"})
		activate(sc, "project:team")
		// The record holds the entry as sync records it: with the profile's targets, which it installs to.
		Expect(e.Trust.Save(proj, trust.View{Profiles: map[string]trust.ProfileView{
			"team": {Modules: []trust.Entry{{Name: "kit", Source: kit, Targets: []string{"claude-code"}}}}}})).To(Succeed())
		_, err := e.AddSource(ctx, tmp, kit, "", "", nil)
		Expect(err).NotTo(HaveOccurred())
		_, err = install(sc, engine.TargetRequest{}, engine.InstallRequest{Arg: "kit"})
		Expect(err).NotTo(HaveOccurred())
		Expect(from(sc, "kit")).To(Equal("profile:project:team"))
		Expect(read(team)).To(ContainSubstring("name: kit"))
		Expect(read(team)).NotTo(ContainSubstring("targets: [claude-code]\n    "), "the entry keeps taking the profile's targets")
	})

	It("refuses an install that would take a project profile's top-level targets the trust record does not cover", func() {
		kit, other, third := mod("kit"), mod("other"), mod("third")
		team := filepath.Join(proj, ".lola", "profiles", "team.yml")
		writeTree(proj, map[string]string{".lola/profiles/team.yml": "version: 1\ntargets: [claude-code]\n"})
		activate(sc, "project:team")
		_, err := e.Install(ctx, sc, tmp, []engine.InstallRequest{{Arg: kit}}, engine.Options{})
		Expect(err).To(MatchError(".lola/profiles/team.yml's top-level targets have not been trusted on this machine"))
		Expect(errs.Hints(err)).To(Equal([]string{"review and apply them: lola sync --project",
			"or name the assistants: lola install --project " + kit + " -t <id>"}))

		// The record holds the profile's entries, as a sync before its targets were pulled left it.
		Expect(e.Trust.Save(proj, trust.View{}.WithPart("team", trust.View{Modules: []trust.Entry{{Name: "old", Source: "https://example.com/old.git"}}}))).To(Succeed())
		_, err = e.Install(ctx, sc, tmp, []engine.InstallRequest{{Arg: kit}}, engine.Options{})
		Expect(err).To(MatchError(".lola/profiles/team.yml's top-level targets have not been trusted yet"),
			"the record never held them: nothing changed since they were trusted")

		Expect(e.Trust.Save(proj, trust.View{}.WithPart("team", trust.View{Targets: []string{"claude-code"}}))).To(Succeed())
		_, err = e.Install(ctx, sc, tmp, []engine.InstallRequest{{Arg: kit}}, engine.Options{})
		Expect(err).NotTo(HaveOccurred(), "the record covers them")
		rec, err := e.Trust.Load(proj)
		Expect(err).NotTo(HaveOccurred())
		Expect(rec.View.Part("team").Targets).To(Equal([]string{"claude-code"}), "the install's refresh keeps them")

		// A pull changes the profile's targets.
		writeTree(proj, map[string]string{".lola/profiles/team.yml": strings.Replace(read(team), "targets: [claude-code]", "targets: [cursor]", 1)})
		_, err = e.Install(ctx, sc, tmp, []engine.InstallRequest{{Arg: other}}, engine.Options{})
		Expect(err).To(MatchError(".lola/profiles/team.yml's top-level targets changed since you last trusted them"))
		Expect(errs.Hints(err)).To(Equal([]string{"review and apply them: lola sync --project",
			"or name the assistants: lola install --project " + other + " -t <id>"}))

		// Targets lola writes into a profile without them are recorded.
		writeTree(proj, map[string]string{".lola/profiles/team.yml": "version: 1\n"})
		_, err = e.Install(ctx, sc, tmp, []engine.InstallRequest{{Arg: other}}, engine.Options{Targets: []string{"claude-code"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(read(team)).To(HavePrefix("version: 1\ntargets:"))
		_, err = e.Install(ctx, sc, tmp, []engine.InstallRequest{{Arg: third}}, engine.Options{})
		Expect(err).NotTo(HaveOccurred(), "lola wrote them, so the record covers them")
	})

	It("an entry of --profile's project profile that is not on is reviewed by turning it on", func() {
		kit := mod("kit")
		writeTree(proj, map[string]string{".lola/profiles/team.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + kit + "}\n"})
		_, err := install(sc, engine.TargetRequest{Profile: "team"}, engine.InstallRequest{Arg: "kit"})
		Expect(err).To(MatchError("kit: .lola/profiles/team.yml has not been trusted on this machine"))
		Expect(errs.Hints(err)).To(Equal([]string{"review it by turning the profile on: lola profile --project team --switch"}),
			"a sync reviews only the profile on")
	})

	It("the top-level targets of --profile's project profile that is not on are reviewed by turning it on, or named with -t", func() {
		kit := mod("kit")
		writeTree(proj, map[string]string{".lola/profiles/team.yml": "version: 1\ntargets: [claude-code]\n"})
		_, err := e.Install(ctx, sc, tmp, []engine.InstallRequest{{Arg: kit}}, engine.Options{Target: engine.TargetRequest{Profile: "team"}})
		Expect(err).To(MatchError(".lola/profiles/team.yml's top-level targets have not been trusted on this machine"))
		Expect(errs.Hints(err)).To(Equal([]string{"review them by turning the profile on: lola profile --project team --switch",
			"or name the assistants: lola install --project " + kit + " --profile team -t <id>"}))
	})

	It("refuses a profile two scopes would write differently, hinting each scope's run with the run's own arguments", func() {
		kit := mod("kit")
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\n"})
		activate(user, "user:go")
		activate(sc, "user:go")
		_, err := e.InstallAll(ctx, engine.InstallSpec{Cwd: tmp, Modules: []engine.InstallRequest{{Arg: kit}},
			Scopes: []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: user}, Targets: []string{"claude-code"}},
				{ScopeChoice: engine.ScopeChoice{Scope: sc}, Targets: []string{"claude-code", "opencode"}}},
			Options: engine.Options{Target: engine.TargetRequest{Profile: "go"}}})
		Expect(err).To(MatchError("profile go would be written differently for each scope"))
		Expect(errs.Hints(err)).To(Equal([]string{"install into one scope at a time: lola install --user " + kit + " --profile go -t claude-code" +
			", then lola install --project " + kit + " --profile go -t claude-code,opencode"}))
	})

	It("keeps --override in each scope's run a refusal hints", func() {
		kit := mod("kit")
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\n"})
		activate(user, "user:go")
		activate(sc, "user:go")
		_, err := e.InstallAll(ctx, engine.InstallSpec{Cwd: tmp, Modules: []engine.InstallRequest{{Arg: kit}},
			Scopes: []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: user}, Targets: []string{"claude-code"}},
				{ScopeChoice: engine.ScopeChoice{Scope: sc}, Targets: []string{"opencode"}}},
			Options: engine.Options{Target: engine.TargetRequest{Override: true}}})
		Expect(err).To(MatchError("profile go would be written differently for each scope"))
		Expect(errs.Hints(err)).To(Equal([]string{"install into one scope at a time: lola install --user " + kit + " --override -t claude-code" +
			", then lola install --project " + kit + " --override -t opencode"}))
	})

	It("records a pulled entry an install by source replaces, after another of its modules, as trusted", func() {
		kit, other := mod("kit"), mod("other")
		writeTree(proj, map[string]string{".lola/profiles/team.yml": "version: 1\ntargets: [claude-code]\nmodules:\n" +
			"  - {name: kit, source: " + kit + "}\n  - {name: other, source: https://example.com/pulled.git}\n"})
		activate(sc, "project:team")
		Expect(e.Trust.Save(proj, trust.View{}.WithPart("team", trust.View{Targets: []string{"claude-code"},
			Modules: []trust.Entry{{Name: "kit", Source: kit, Targets: []string{"claude-code"}}}}))).To(Succeed())
		_, err := e.Install(ctx, sc, tmp, []engine.InstallRequest{{Arg: kit}, {Arg: other}}, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		_, err = e.Install(ctx, sc, tmp, []engine.InstallRequest{{Arg: "other"}}, engine.Options{})
		Expect(err).NotTo(HaveOccurred(), "lola wrote other's entry, so the record covers it")
	})

	It("in a project, -t for a profile of yours without targets is written into the entry, never the profile's targets", func() {
		kit := mod("kit")
		me := filepath.Join(p.UserProfiles, "me.yml")
		writeTree(p.UserProfiles, map[string]string{"me.yml": "version: 1\n"})
		activate(sc, "user:me")
		_, err := e.Install(ctx, sc, tmp, []engine.InstallRequest{{Arg: kit}}, engine.Options{Targets: []string{"claude-code"}})
		Expect(err).NotTo(HaveOccurred())
		got, err := profile.Load(me)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Targets).To(BeEmpty(), "your profile is used in other projects too")
		Expect(got.Modules).To(HaveLen(1))
		Expect(got.Modules[0].Targets).To(Equal([]string{"claude-code"}))
	})
})
