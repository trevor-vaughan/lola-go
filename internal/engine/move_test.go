package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("lola profile --move", func() {
	var (
		tmp, proj, url, work, bare string
		p                          paths.Paths
		e                          *engine.Engine
		user, sc                   engine.Scope
		ctx                        = context.Background()
		yes                        = func([]engine.Removal) (bool, error) { return true, nil }
		trustAll                   = func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }
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
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		user, sc = engine.Scope{Kind: target.User}, engine.Scope{Kind: target.Project, ProjectDir: proj}
		work, bare = filepath.Join(tmp, "work"), filepath.Join(tmp, "srv", "notes.git")
		writeTree(work, map[string]string{"lola.yml": "name: notes\n", "commands/notes.md": "one\n"})
		gitIn(work, "init", "-q", "-b", "main")
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "one")
		gitIn(tmp, "clone", "-q", "--bare", work, bare)
		url = testenv.ServeGit(filepath.Join(tmp, "srv")) + "/notes.git"
	})
	activate := func(s engine.Scope, name string) {
		GinkgoHelper()
		path := p.UserLedger
		if s.Kind == target.Project {
			path = filepath.Join(s.ProjectDir, ".lola", "lock.json")
		}
		led, err := e.Ledger(s)
		Expect(err).NotTo(HaveOccurred())
		led.Profile = nil
		if name != "" {
			led.Profile = &ledger.Active{Name: name, By: ledger.ByOn}
		}
		Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
		Expect(led.Save(path)).To(Succeed())
	}
	installed := func(s engine.Scope) *ledger.Entry {
		GinkgoHelper()
		led, err := e.Ledger(s)
		Expect(err).NotTo(HaveOccurred())
		return led.Modules["notes"]
	}
	read := func(path string) string {
		GinkgoHelper()
		b, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		return string(b)
	}
	move := func(req engine.MoveRequest) (*engine.MoveResult, error) {
		if req.ConfirmRemovals == nil {
			req.ConfirmRemovals = yes
		}
		req.Trust = trustAll
		// The run without --to and its consent flags, as the CLI gives it: each hint names its own --to.
		req.Options.Target.Rerun = func(extra ...string) string {
			return e.Command(req.Scope, "profile", append([]string{"--move", req.Modules[0]}, extra...)...)
		}
		return e.Move(ctx, req)
	}
	// keepRemote writes a remote profile as lola profile --add does: profile.yml and its source.json.
	keepRemote := func(name, content string) {
		GinkgoHelper()
		dir := filepath.Join(p.RemoteProfiles, name)
		Expect(os.MkdirAll(dir, 0o700)).To(Succeed())
		b, err := json.Marshal(profile.Origin{Version: 1, Source: "https://example.com/profiles.git", Commit: strings.Repeat("c", 40),
			Digest: profile.Digest([]byte(content))})
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(dir, "source.json"), b, 0o600)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dir, "profile.yml"), []byte(content), 0o600)).To(Succeed())
	}
	projectYML := func() string { return filepath.Join(proj, ".lola", "project.yml") }
	// serveOther serves a second module, other, beside notes, and is its url.
	serveOther := func() string {
		GinkgoHelper()
		dir := filepath.Join(tmp, "work-other")
		writeTree(dir, map[string]string{"lola.yml": "name: other\n", "commands/other.md": "O\n"})
		gitIn(dir, "init", "-q", "-b", "main")
		gitIn(dir, "add", "-A")
		gitIn(dir, "commit", "-q", "-m", "one")
		gitIn(tmp, "clone", "-q", "--bare", dir, filepath.Join(tmp, "srv", "other.git"))
		return strings.TrimSuffix(url, "/notes.git") + "/other.git"
	}

	It("moves an entry from project.yml into the profile on, in one transaction, keeping the installed commit though its ref moved", func() {
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + url + "}\n"})
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll})
		Expect(err).NotTo(HaveOccurred())
		was := installed(sc)
		writeTree(work, map[string]string{"commands/notes.md": "two\n"})
		gitIn(work, "commit", "-q", "-am", "two")
		gitIn(work, "push", "-q", bare, "main")
		writeTree(p.UserProfiles, map[string]string{"me.yml": "version: 1\n"})
		activate(sc, "user:me")

		res, err := move(engine.MoveRequest{Scope: sc, Modules: []string{"notes"}, To: "me"})
		Expect(err).NotTo(HaveOccurred())
		Expect(read(projectYML())).NotTo(ContainSubstring("notes"))
		Expect(read(filepath.Join(p.UserProfiles, "me.yml"))).To(Equal("version: 1\nmodules:\n  - name: notes\n    source: "+url+"\n"),
			"whole, without the pin project.yml held: your profile never holds one")
		now := installed(sc)
		Expect(now.Commit).To(Equal(was.Commit), "nothing changes but where the entry lives")
		Expect(now.From).To(Equal("profile:user:me"))
		Expect(res.Edits).To(HaveExactElements(HaveField("Action", "removed"), HaveField("Action", "added")))
		Expect(read(filepath.Join(proj, ".claude", "commands", "notes.md"))).To(Equal("one\n"))
	})

	It("moves into project.yml with the installed pin, and records it as trusted", func() {
		writeTree(p.UserProfiles, map[string]string{"me.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + url + "}\n"})
		activate(sc, "user:me")
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll})
		Expect(err).NotTo(HaveOccurred())
		was := installed(sc)
		_, err = move(engine.MoveRequest{Scope: sc, Modules: []string{"notes"}, To: "base"})
		Expect(err).NotTo(HaveOccurred())
		Expect(read(projectYML())).To(ContainSubstring("commit: " + was.Commit))
		rec, err := e.Trust.Load(proj)
		Expect(err).NotTo(HaveOccurred())
		Expect(rec.View.Find("notes")).NotTo(BeNil(), "lola wrote it, so it is trusted (decisions line 47)")
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc})
		Expect(err).NotTo(HaveOccurred(), "a sync without a trust answer asks nothing about what lola wrote")
	})

	It("moves an entry's trust with it, never more: one its file's record does not cover is asked about where it lands", func() {
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + url + "}\n",
			".lola/profiles/team.yml": "version: 1\n"})
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll})
		Expect(err).NotTo(HaveOccurred())
		pulled := "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + url + ", rules: false}\n"
		writeTree(proj, map[string]string{".lola/project.yml": pulled}) // a change no one trusted yet
		activate(sc, "project:team")

		_, err = e.Move(ctx, engine.MoveRequest{Scope: sc, Modules: []string{"notes"}, To: "team", ConfirmRemovals: yes})
		Expect(err).To(MatchError(HavePrefix("lola profile needs your trust for changes to "+filepath.Join(".lola", "profiles", "team.yml")+
			", and there is no terminal to ask on")), "the run is lola profile's, not lola sync's")
		Expect(errs.Hints(err)).To(Equal([]string{"see them: lola profile --project --move notes --to team -n",
			"then accept them: lola profile --project --move notes --to team --trust=profiles -y"}))
		Expect(read(projectYML())).To(Equal(pulled), "a refused move writes nothing")
		Expect(read(filepath.Join(proj, ".lola", "profiles", "team.yml"))).To(Equal("version: 1\n"))

		_, err = move(engine.MoveRequest{Scope: sc, Modules: []string{"notes"}, To: "team"})
		Expect(err).NotTo(HaveOccurred(), "trusted at the question")
		Expect(read(filepath.Join(proj, ".lola", "profiles", "team.yml"))).To(ContainSubstring("rules: false"))
	})

	It("never carries an entry of a project file its record does not cover into a profile of yours, where nothing would ask about it", func() {
		otherURL := serveOther()
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + url + "}\n"})
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll})
		Expect(err).NotTo(HaveOccurred())
		pulled := read(projectYML()) + "  - {name: other, source: " + otherURL + "}\n"
		writeTree(proj, map[string]string{".lola/project.yml": pulled}) // a pull adds other, never synced nor trusted
		writeTree(p.UserProfiles, map[string]string{"me.yml": "version: 1\n"})
		activate(sc, "user:me")
		me := filepath.Join(p.UserProfiles, "me.yml")
		other := filepath.Join(proj, ".claude", "commands", "other.md")

		for _, dry := range []bool{true, false} {
			_, err = e.Move(ctx, engine.MoveRequest{Scope: sc, Modules: []string{"other"}, To: "me", ConfirmRemovals: yes, Options: engine.Options{DryRun: dry}})
			Expect(err).To(MatchError("other is not installed in this project as its files list it now; lola copies or moves out of a project only what it installed "+
				"as you trusted it"), "dry run: %v", dry)
			Expect(errs.Hints(err)).To(Equal([]string{"review the changes first: lola sync --project other"}))
			Expect(read(me)).To(Equal("version: 1\n"))
			Expect(read(projectYML())).To(Equal(pulled))
			Expect(other).NotTo(BeAnExistingFile())
		}
		_, err = e.Move(ctx, engine.MoveRequest{Scope: sc, Modules: []string{"notes"}, To: "me", ConfirmRemovals: yes})
		Expect(err).NotTo(HaveOccurred(), "what it installed as you trusted it moves")
		Expect(read(me)).To(ContainSubstring("name: notes"))

		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Modules: []string{"other"}, Trust: trustAll}) // the hint's run, trusted at its question
		Expect(err).NotTo(HaveOccurred())
		_, err = e.Move(ctx, engine.MoveRequest{Scope: sc, Modules: []string{"other"}, To: "me", ConfirmRemovals: yes})
		Expect(err).NotTo(HaveOccurred())
		Expect(read(me)).To(ContainSubstring("name: other"))
		Expect(read(projectYML())).NotTo(ContainSubstring("other"))
	})

	It("never carries an entry of a project profile its record does not cover into a profile of yours", func() {
		otherURL := serveOther()
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\n",
			".lola/profiles/team.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + url + "}\n"})
		writeTree(p.UserProfiles, map[string]string{"me.yml": "version: 1\n"})
		activate(sc, "project:team")
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll})
		Expect(err).NotTo(HaveOccurred())
		pulled := read(filepath.Join(proj, ".lola", "profiles", "team.yml")) + "  - {name: other, source: " + otherURL + "}\n"
		writeTree(proj, map[string]string{".lola/profiles/team.yml": pulled})
		me := filepath.Join(p.UserProfiles, "me.yml")

		_, err = e.Move(ctx, engine.MoveRequest{Scope: sc, Modules: []string{"other"}, To: "me", KeepOff: true, ConfirmRemovals: yes})
		Expect(err).To(MatchError(HavePrefix("other is not installed in this project as its files list it now")))
		Expect(errs.Hints(err)).To(Equal([]string{"review the changes first: lola sync --project other"}))
		Expect(read(me)).To(Equal("version: 1\n"))
		Expect(read(filepath.Join(proj, ".lola", "profiles", "team.yml"))).To(Equal(pulled))

		// From a profile that is off, which installed none of its entries.
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileSwitch, Name: "user:me"}, ConfirmRemovals: yes})
		Expect(err).NotTo(HaveOccurred())
		Expect(installed(sc)).To(BeNil())
		_, err = e.Move(ctx, engine.MoveRequest{Scope: sc, From: "team", Modules: []string{"other"}, To: "me", ConfirmRemovals: yes})
		Expect(err).To(MatchError("other is in profile team, which is off in this project; lola copies or moves out of a project only what it installed as you trusted it"))
		Expect(errs.Hints(err)).To(Equal([]string{"review it first: lola profile --project team --switch"}))
		Expect(read(me)).To(Equal("version: 1\n"))

		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileSwitch, Name: "project:team"},
			ConfirmRemovals: yes, Trust: trustAll}) // the hint's run, trusted at its question
		Expect(err).NotTo(HaveOccurred())
		_, err = e.Move(ctx, engine.MoveRequest{Scope: sc, Modules: []string{"other"}, To: "me", KeepOff: true, ConfirmRemovals: yes})
		Expect(err).NotTo(HaveOccurred())
		Expect(read(me)).To(ContainSubstring("name: other"))
	})

	It("moves between two profiles that are off at user scope with no default.yml, writing no base and installing nothing", func() {
		writeTree(p.UserProfiles, map[string]string{"a.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + url + "}\n",
			"c.yml": "version: 1\n"})
		_, err := e.Move(ctx, engine.MoveRequest{Scope: user, From: "a", Modules: []string{"notes"}, To: "c", KeepOff: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(read(filepath.Join(p.UserProfiles, "c.yml"))).To(ContainSubstring("name: notes"))
		Expect(read(filepath.Join(p.UserProfiles, "a.yml"))).NotTo(ContainSubstring("notes"))
		Expect(filepath.Join(p.UserProfiles, "default.yml")).NotTo(BeAnExistingFile())
		Expect(installed(user)).To(BeNil())
	})

	It("pins an entry it moves into a project profile in the same write, when nothing installed it yet", func() {
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + url + "}\n",
			".lola/profiles/team.yml": "version: 1\n"})
		activate(sc, "project:team")
		_, err := move(engine.MoveRequest{Scope: sc, Modules: []string{"notes"}, To: "team"})
		Expect(err).NotTo(HaveOccurred())
		Expect(installed(sc)).NotTo(BeNil())
		Expect(read(filepath.Join(proj, ".lola", "profiles", "team.yml"))).To(And(ContainSubstring("name: notes"), ContainSubstring("commit: "+installed(sc).Commit)))
		Expect(read(projectYML())).NotTo(ContainSubstring("notes"))
	})

	It("writes the targets the entry installed to into it when the target file would give it others", func() {
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\n",
			".lola/profiles/team.yml": "version: 1\ntargets: [gemini-cli]\nmodules:\n  - {name: notes, source: " + url + "}\n"})
		activate(sc, "project:team")
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll})
		Expect(err).NotTo(HaveOccurred())
		Expect(installed(sc).Targets).To(Equal([]string{"gemini-cli"}))

		_, err = move(engine.MoveRequest{Scope: sc, Modules: []string{"notes"}, To: "project.yml"})
		Expect(err).NotTo(HaveOccurred())
		man, err := manifest.Load(projectYML())
		Expect(err).NotTo(HaveOccurred())
		Expect(man.Find("notes")).NotTo(BeNil())
		Expect(man.Find("notes").Targets).To(Equal([]string{"gemini-cli"}), "the move changes no installed file (§4 S4)")
		Expect(installed(sc).Targets).To(Equal([]string{"gemini-cli"}))
		Expect(filepath.Join(proj, ".claude", "commands", "notes.md")).NotTo(BeAnExistingFile())
	})

	It("asks before replacing an entry the target has, and refuses off a terminal without -y", func() {
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + url + "}\n",
			"me.yml": "version: 1\nmodules:\n  - {name: notes, source: " + url + ", rules: false}\n"})
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: user})
		Expect(err).NotTo(HaveOccurred())
		activate(user, "user:me")
		_, err = move(engine.MoveRequest{Scope: user, From: "base", Modules: []string{"notes"}, To: "me"})
		Expect(err).To(MatchError("profile me lists notes already; lola needs to know whether to replace its entry, and there is no terminal to ask on"))
		Expect(errs.Hints(err)).To(Equal([]string{"lola profile --user --move notes --to me -y"}))
		var asked []string
		_, err = move(engine.MoveRequest{Scope: user, From: "base", Modules: []string{"notes"}, To: "me",
			Replace: func(module, label string) (bool, error) {
				asked = append(asked, module+" in "+label)
				return false, nil
			}})
		Expect(err).To(MatchError(engine.ErrDeclined))
		Expect(asked).To(Equal([]string{"notes in profile me"}))
		Expect(read(filepath.Join(p.UserProfiles, "me.yml"))).To(ContainSubstring("rules: false"), "a No writes nothing")
		_, err = move(engine.MoveRequest{Scope: user, From: "base", Modules: []string{"notes"}, To: "me", Yes: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(read(filepath.Join(p.UserProfiles, "me.yml"))).NotTo(ContainSubstring("rules: false"))
		Expect(read(filepath.Join(p.UserProfiles, "default.yml"))).NotTo(ContainSubstring("notes"))
	})

	It("refuses a move from or to a remote profile, and project.yml as a target from user scope", func() {
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + url + "}\n",
			"me.yml": "version: 1\n"})
		_, err := move(engine.MoveRequest{Scope: user, Modules: []string{"notes"}, To: "remote:team"})
		Expect(err).To(MatchError(HavePrefix("no profile remote:team")))
		_, err = move(engine.MoveRequest{Scope: user, Modules: []string{"notes"}, To: "project.yml"})
		Expect(err).To(MatchError("project.yml is a project's base; lola profile --move never moves an entry into it from user scope"))
		_, err = move(engine.MoveRequest{Scope: user, Modules: []string{"gone"}, To: "base"})
		Expect(err).To(MatchError("gone: no layer at user scope lists it"))

		keepRemote("team", "version: 1\nmodules:\n  - {name: notes, source: https://example.com/notes.git, commit: "+strings.Repeat("a", 40)+", digest: sha256:"+strings.Repeat("b", 64)+"}\n")
		_, err = move(engine.MoveRequest{Scope: user, Modules: []string{"notes"}, To: "remote:team"})
		Expect(err).To(MatchError("remote:team is read-only; lola does not edit remote profiles"))
		Expect(errs.Hints(err)).To(BeEmpty(), "notes is in the base already, and me is off: no --to takes it")
		_, err = move(engine.MoveRequest{Scope: user, From: "remote:team", Modules: []string{"notes"}, To: "base"})
		Expect(err).To(MatchError("notes comes from remote:team, which is read-only; lola does not edit remote profiles"), "named, though it is off")
		Expect(errs.Hints(err)).To(BeEmpty(), "with it off, no command moves it")
		activate(user, "remote:team")
		for _, from := range []string{"", "remote:team"} {
			_, err = move(engine.MoveRequest{Scope: user, From: from, Modules: []string{"notes"}, To: "base"})
			Expect(err).To(MatchError("notes comes from remote:team, which is read-only; lola does not edit remote profiles"), from)
			Expect(errs.Hints(err)).To(Equal([]string{"turn it off: lola profile --user --off",
				"or switch to a copy of yours: lola profile --user team-mine --switch --new --from remote:team"}), from)
		}
		_, err = e.Move(ctx, engine.MoveRequest{Scope: user, Modules: []string{"notes"}, To: "base"})
		Expect(errs.Hints(err)).To(Equal([]string{"turn it off: lola profile --user --off",
			"or switch to a copy of yours: lola profile --user team-mine --switch --new --from remote:team"}), "R-8: a run that cannot ask hints no -y it was not given")
	})

	// hinted runs the move each hint names (its --to), as the hint would.
	hinted := func(req engine.MoveRequest, hints []string) {
		GinkgoHelper()
		for _, h := range hints {
			f := strings.Fields(h)
			r := req
			r.To = f[len(f)-1]
			_, err := move(r)
			Expect(err).NotTo(HaveOccurred(), h)
		}
	}

	It("hints only the --to a move takes as things stand, and each runs", func() {
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + url + "}\n",
			"me.yml": "version: 1\n"})
		keepRemote("team", "version: 1\n")
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: user})
		Expect(err).NotTo(HaveOccurred())
		_, err = move(engine.MoveRequest{Scope: user, Modules: []string{"notes"}})
		Expect(err).To(MatchError("--move needs --to, the profile the entries move to"))
		Expect(errs.Hints(err)).To(BeEmpty(), "the entry is in the base, and no profile is on")

		activate(user, "user:me")
		_, err = move(engine.MoveRequest{Scope: user, Modules: []string{"notes"}})
		Expect(errs.Hints(err)).To(Equal([]string{"lola profile --user --move notes --to me"}))
		_, err = move(engine.MoveRequest{Scope: user, Modules: []string{"notes"}, To: "remote:team"})
		Expect(err).To(MatchError("remote:team is read-only; lola does not edit remote profiles"))
		Expect(errs.Hints(err)).To(Equal([]string{"lola profile --user --move notes --to me"}))
		hinted(engine.MoveRequest{Scope: user, Modules: []string{"notes"}}, errs.Hints(err))

		_, err = move(engine.MoveRequest{Scope: user, Modules: []string{"notes"}})
		Expect(errs.Hints(err)).To(Equal([]string{"lola profile --user --move notes --to base"}), "now in me, the profile on")
		hinted(engine.MoveRequest{Scope: user, Modules: []string{"notes"}}, errs.Hints(err))
	})

	It("asks before moving into a profile that is off: Switch moves and switches in one plan", func() {
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + url + "}\n"})
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll})
		Expect(err).NotTo(HaveOccurred())
		was := installed(sc)
		writeTree(p.UserProfiles, map[string]string{"me.yml": "version: 1\n"})
		res, err := move(engine.MoveRequest{Scope: sc, Modules: []string{"notes"}, To: "me", Switch: true, Options: engine.Options{DryRun: true}})
		Expect(err).NotTo(HaveOccurred())
		dryAct := res.Syncs[0].Activation
		Expect(dryAct).NotTo(BeNil())
		Expect(dryAct.Turned).To(BeTrue())
		Expect(res.Syncs[0].Removals).To(BeEmpty(), "a dry run of the switch plans no removal: notes moves along")
		Expect(read(filepath.Join(p.UserProfiles, "me.yml"))).To(Equal("version: 1\n"), "and writes nothing")
		var asked []engine.WhereQuestion
		res, err = move(engine.MoveRequest{Scope: sc, Modules: []string{"notes"}, To: "me", Options: engine.Options{
			AskWhere: func(q engine.WhereQuestion) (engine.WhereChoice, error) {
				asked = append(asked, q)
				return q.Choices[0], nil
			}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(asked).To(ConsistOf(engine.WhereQuestion{Scope: sc, Title: "me is not on here.", Choices: []engine.WhereChoice{
			{Label: "Switch to me", Flags: []string{"--to", "me", "--switch"}},
			{Label: "Keep it off (notes is removed here)", Flags: []string{"--to", "me", "--keep-off", "-y"}}}}))
		led, err := e.Ledger(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Profile).To(Equal(&ledger.Active{Name: "user:me", By: ledger.BySwitch}))
		Expect(dryAct).To(Equal(res.Syncs[0].Activation), "the dry run showed the switch the run made")
		Expect(installed(sc).Commit).To(Equal(was.Commit))
		Expect(installed(sc).From).To(Equal("profile:user:me"))
	})

	It("--keep-off removes the module's files here, asked, and leaves the entry in the profile", func() {
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + url + "}\n"})
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll})
		Expect(err).NotTo(HaveOccurred())
		writeTree(p.UserProfiles, map[string]string{"me.yml": "version: 1\ntargets: [claude-code]\n"})
		var asked [][]engine.Removal
		_, err = move(engine.MoveRequest{Scope: sc, Modules: []string{"notes"}, To: "me", KeepOff: true,
			ConfirmRemovals: func(r []engine.Removal) (bool, error) { asked = append(asked, r); return true, nil }})
		Expect(err).NotTo(HaveOccurred())
		Expect(asked).To(HaveLen(1), "the removal is asked")
		Expect(installed(sc)).To(BeNil())
		Expect(read(filepath.Join(p.UserProfiles, "me.yml"))).To(ContainSubstring("name: notes"))
		led, err := e.Ledger(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Profile).To(BeNil(), "me stays off")
	})

	It("refuses a move into a profile that is off without --switch or --keep-off, off a terminal", func() {
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + url + "}\n"})
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll})
		Expect(err).NotTo(HaveOccurred())
		writeTree(p.UserProfiles, map[string]string{"me.yml": "version: 1\n"})
		_, err = move(engine.MoveRequest{Scope: sc, Modules: []string{"notes"}, To: "me", Options: engine.Options{Force: true}})
		Expect(err).To(MatchError("me is not on in this project; lola needs to know whether to switch to it, and there is no terminal to ask on"))
		Expect(errs.Hints(err)).To(Equal([]string{"lola profile --project --move notes --to me --switch",
			"lola profile --project --move notes --to me --keep-off -y"}))
		Expect(read(projectYML())).To(ContainSubstring("notes"), "nothing was written")
		Expect(installed(sc)).NotTo(BeNil())
	})

	It("--to a name no profile has creates your profile, and a failed move removes it again", func() {
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + url + "}\n"})
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll})
		Expect(err).NotTo(HaveOccurred())
		res, err := move(engine.MoveRequest{Scope: sc, Modules: []string{"notes"}, To: "mine", Switch: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Created).To(Equal(filepath.Join(p.UserProfiles, "mine.yml")))
		Expect(read(filepath.Join(p.UserProfiles, "mine.yml"))).To(Equal("version: 1\nmodules:\n  - name: notes\n    source: " + url + "\n"))

		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: other, source: " + serveOther() + "}\n"})
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll}) // installed as trusted, so it may move into a profile of yours
		Expect(err).NotTo(HaveOccurred())
		_, err = move(engine.MoveRequest{Scope: sc, Modules: []string{"other"}, To: "fresh", Switch: true,
			ConfirmRemovals: func([]engine.Removal) (bool, error) { return false, nil }})
		_, declined := errors.AsType[*engine.RemovalsDeclinedError](err)
		Expect(declined).To(BeTrue(), "switching from mine unloads notes, which was declined: %v", err)
		Expect(filepath.Join(p.UserProfiles, "fresh.yml")).NotTo(BeAnExistingFile(), "the profile it created goes with the failed move")
	})

	It("a dry run of a switch into a profile it would create plans what the switch does, and creates nothing", func() {
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + url + "}\n"})
		writeTree(p.UserProfiles, map[string]string{"me.yml": "version: 1\nmodules:\n  - {name: other, source: " + serveOther() + "}\n"})
		activate(sc, "user:me")
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll})
		Expect(err).NotTo(HaveOccurred())
		fresh := filepath.Join(p.UserProfiles, "fresh.yml")
		res, err := move(engine.MoveRequest{Scope: sc, Modules: []string{"notes"}, To: "fresh", Switch: true, Options: engine.Options{DryRun: true}})
		Expect(err).NotTo(HaveOccurred())
		Expect(fresh).NotTo(BeAnExistingFile(), "a dry run creates nothing")
		Expect(res.Created).To(BeEmpty())
		planned, dryAct, dryNotices := res.Syncs[0].Removals, res.Syncs[0].Activation, res.Syncs[0].Notices
		Expect(dryNotices).NotTo(ContainElement(HaveField("Text", ContainSubstring("fresh"))), "the profile it would create is not gone")
		Expect(dryAct).NotTo(BeNil(), "the dry run shows the switch, which is why other goes")
		Expect(res.Syncs[0].ToApply).To(Equal("lola profile --project --move notes --to fresh --switch"), "the run it previews keeps --switch")
		Expect(planned).To(ConsistOf(HaveField("Module", "other")), "the switch leaves me, whose other goes; notes moves along")

		var asked []engine.Removal
		res, err = move(engine.MoveRequest{Scope: sc, Modules: []string{"notes"}, To: "fresh", Switch: true,
			ConfirmRemovals: func(r []engine.Removal) (bool, error) { asked = r; return true, nil }})
		Expect(err).NotTo(HaveOccurred())
		Expect(asked).To(Equal(planned), "the run removes what the dry run planned")
		Expect(dryAct).To(Equal(res.Syncs[0].Activation), "and switches as the dry run showed")
		Expect(dryNotices).To(Equal(res.Syncs[0].Notices), "with the notices the dry run gave")
		Expect(res.Created).To(Equal(fresh))
		led, err := e.Ledger(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Profile).To(Equal(&ledger.Active{Name: "user:fresh", By: ledger.BySwitch}))
		Expect(installed(sc).From).To(Equal("profile:user:fresh"))
	})

	It("takes --switch to the profile on, saying it is already on, and refuses --keep-off with a target that is on", func() {
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + url + "}\n",
			"me.yml": "version: 1\n"})
		keepRemote("team", "version: 1\n")
		activate(user, "user:me")
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: user})
		Expect(err).NotTo(HaveOccurred())

		for _, to := range []string{"me", "base"} {
			_, err = move(engine.MoveRequest{Scope: user, Modules: []string{"notes"}, To: to, KeepOff: true})
			flagErr, ok := errors.AsType[*engine.MoveFlagError](err)
			Expect(ok).To(BeTrue(), "%s: %v", to, err)
			Expect(flagErr.Flag).To(Equal("keep-off"))
		}
		Expect(err).To(MatchError("the base is always on at user scope; --keep-off applies to a profile"))
		_, err = move(engine.MoveRequest{Scope: user, Modules: []string{"notes"}, To: "me", KeepOff: true})
		Expect(err).To(MatchError("me is on at user scope already; --keep-off applies to a profile that is off"))
		_, err = move(engine.MoveRequest{Scope: user, Modules: []string{"notes"}, To: "base", Switch: true})
		Expect(err).To(MatchError("the base is always on at user scope; --switch applies to a profile"))
		Expect(read(filepath.Join(p.UserProfiles, "default.yml"))).To(ContainSubstring("name: notes"), "nothing was written")

		res, err := move(engine.MoveRequest{Scope: user, Modules: []string{"notes"}, To: "me", Switch: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Syncs[0].Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "user:me is already on"}))
		Expect(read(filepath.Join(p.UserProfiles, "me.yml"))).To(ContainSubstring("name: notes"))

		// A bundle on is never a target, nor is its remote member.
		writeTree(p.UserProfiles, map[string]string{"work.yml": "version: 1\nbundle: [remote:team, me]\n"})
		activate(user, "user:work")
		_, err = move(engine.MoveRequest{Scope: user, Modules: []string{"notes"}, To: "work", Switch: true})
		Expect(err).To(MatchError("work is a bundle; move into one of its members"))
		_, err = move(engine.MoveRequest{Scope: user, Modules: []string{"notes"}, To: "remote:team", Switch: true})
		Expect(err).To(MatchError("remote:team is read-only; lola does not edit remote profiles"), "a bundle's remote member is remote, never a target")
		Expect(read(filepath.Join(p.UserProfiles, "me.yml"))).To(ContainSubstring("name: notes"), "nothing was written")
	})

	It("--keep-off without -y off a terminal refuses the removal and keeps every file", func() {
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + url + "}\n"})
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll})
		Expect(err).NotTo(HaveOccurred())
		writeTree(p.UserProfiles, map[string]string{"me.yml": "version: 1\n"})
		before := read(projectYML())
		_, err = e.Move(ctx, engine.MoveRequest{Scope: sc, Modules: []string{"notes"}, To: "me", KeepOff: true, Trust: trustAll})
		Expect(err).To(HaveOccurred())
		Expect(err).To(MatchError("lola profile needs your confirmation to remove notes, and there is no terminal to ask on"))
		Expect(errs.Hints(err)).To(Equal([]string{"see them: lola profile --project --move notes --to me --keep-off -n",
			"then confirm them: lola profile --project --move notes --to me --keep-off -y"}))
		Expect(read(projectYML())).To(Equal(before))
		Expect(read(filepath.Join(p.UserProfiles, "me.yml"))).To(Equal("version: 1\n"))
		Expect(installed(sc)).NotTo(BeNil())
		Expect(filepath.Join(proj, ".claude", "commands", "notes.md")).To(BeAnExistingFile())
	})

	It("refuses default as the target at project scope, hinting base and user scope where each takes the move", func() {
		writeTree(p.UserProfiles, map[string]string{"me.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + url + "}\n"})
		activate(user, "user:me")
		activate(sc, "user:me")
		req := engine.MoveRequest{Scope: sc, Modules: []string{"notes"}, To: "default", Options: engine.Options{Target: engine.TargetRequest{
			Rerun: func(extra ...string) string {
				return strings.Join(append([]string{"lola profile --move notes"}, extra...), " ")
			},
			UserRun: func() string { return "lola profile --move notes --to default --user" },
		}}}
		_, err := e.Move(ctx, req)
		Expect(err).To(MatchError("default is user scope's base; this project's base is project.yml"))
		Expect(errs.Hints(err)).To(Equal([]string{"lola profile --move notes --to base", "lola profile --move notes --to default --user"}))
		// The hints are alternatives (me.yml is one file at both scopes): each runs from where the refusal was.
		me := read(filepath.Join(p.UserProfiles, "me.yml"))
		_, err = move(engine.MoveRequest{Scope: sc, Modules: []string{"notes"}, To: "base"})
		Expect(err).NotTo(HaveOccurred())
		writeTree(p.UserProfiles, map[string]string{"me.yml": me})
		_, err = move(engine.MoveRequest{Scope: user, Modules: []string{"notes"}, To: "default"})
		Expect(err).NotTo(HaveOccurred())
		_, err = e.Move(ctx, req)
		Expect(errs.Hints(err)).To(Equal([]string{"lola profile --move notes --to me"}), "notes is in both bases now: only me takes it")
	})

	It("moves a folder entry project.yml trusts into a project profile without asking again", func() {
		writeTree(proj, map[string]string{"mods/notes/lola.yml": "name: notes\n", "mods/notes/commands/notes.md": "N\n",
			".lola/project.yml":       "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: ./mods/notes}\n",
			".lola/profiles/team.yml": "version: 1\n"})
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll})
		Expect(err).NotTo(HaveOccurred())
		activate(sc, "project:team")
		res, err := e.Move(ctx, engine.MoveRequest{Scope: sc, Modules: []string{"notes"}, To: "team", ConfirmRemovals: yes})
		Expect(err).NotTo(HaveOccurred(), "the record covers it where it came from")
		Expect(res.Syncs[0].Trust.Changes).To(BeEmpty())
		Expect(read(filepath.Join(proj, ".lola", "profiles", "team.yml"))).To(ContainSubstring("source: ../../mods/notes"))
		Expect(read(filepath.Join(proj, ".claude", "commands", "notes.md"))).To(Equal("N\n"))
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc})
		Expect(err).NotTo(HaveOccurred(), "and the next sync asks nothing")
	})

	It("moves between profiles of yours in a project without touching the trust record", func() {
		writeTree(p.UserProfiles, map[string]string{"me.yml": "version: 1\n",
			"other.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + url + "}\n"})
		activate(sc, "user:me")
		Expect(os.MkdirAll(p.TrustLock, 0o700)).To(Succeed()) // a trust lock that cannot be taken
		_, err := move(engine.MoveRequest{Scope: sc, From: "other", Modules: []string{"notes"}, To: "me"})
		Expect(err).NotTo(HaveOccurred(), "nothing of the project's was written, so there is nothing to record")
		Expect(read(filepath.Join(p.UserProfiles, "me.yml"))).To(ContainSubstring("name: notes"))
	})

	It("creates the profile on whose file is gone, and removes it again when the move fails", func() {
		if os.Geteuid() == 0 {
			Skip("root writes into a read-only directory")
		}
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + url + "}\n"})
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: user})
		Expect(err).NotTo(HaveOccurred())
		activate(user, "user:me")
		me := filepath.Join(p.UserProfiles, "me.yml")

		res, err := move(engine.MoveRequest{Scope: user, Modules: []string{"notes"}, To: "me", Options: engine.Options{DryRun: true}})
		Expect(err).NotTo(HaveOccurred())
		Expect(me).NotTo(BeAnExistingFile(), "a dry run creates nothing")
		Expect(res.Created).To(BeEmpty())
		Expect(res.Syncs[0].Removals).To(BeEmpty(), "the dry run reads the entry where it goes")
		Expect(res.Syncs[0].Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
			Text: "profile user:me is on but no longer exists → lola profile --user user:me --off"}), "lock.json has me on, and its file is gone")

		// lock.json's directory refuses the apply's write, after the profile was created.
		Expect(os.Chmod(filepath.Dir(p.UserLedger), 0o500)).To(Succeed())
		_, err = move(engine.MoveRequest{Scope: user, Modules: []string{"notes"}, To: "me"})
		Expect(os.Chmod(filepath.Dir(p.UserLedger), 0o700)).To(Succeed())
		Expect(err).To(HaveOccurred())
		Expect(me).NotTo(BeAnExistingFile(), "the profile the failed move created is gone")
		Expect(read(filepath.Join(p.UserProfiles, "default.yml"))).To(ContainSubstring("notes"))

		res, err = move(engine.MoveRequest{Scope: user, Modules: []string{"notes"}, To: "me"})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Created).To(Equal(me))
		Expect(read(me)).To(Equal("version: 1\nmodules:\n  - name: notes\n    source: " + url + "\n"))
		Expect(installed(user).From).To(Equal("profile:user:me"))
	})

	It("a failed apply leaves every file as it was, pins.yaml included", func() {
		if os.Geteuid() == 0 {
			Skip("root writes into a read-only directory")
		}
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + url + "}\n",
			"me.yml": "version: 1\n"})
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: user})
		Expect(err).NotTo(HaveOccurred())
		activate(user, "user:me")
		files := map[string]string{"default": filepath.Join(p.UserProfiles, "default.yml"), "me": filepath.Join(p.UserProfiles, "me.yml"),
			"pins": p.Pins, "lock": p.UserLedger}
		before := map[string]string{}
		for name, path := range files {
			before[name] = read(path)
		}
		// The apply writes lock.json, then the profiles, whose directory refuses the write: the
		// transaction restores lock.json and never reaches pins.yaml.
		Expect(os.Chmod(p.UserProfiles, 0o500)).To(Succeed())
		DeferCleanup(func() { Expect(os.Chmod(p.UserProfiles, 0o700)).To(Succeed()) })
		_, err = move(engine.MoveRequest{Scope: user, Modules: []string{"notes"}, To: "me"})
		Expect(err).To(HaveOccurred())
		for name, path := range files {
			Expect(read(path)).To(Equal(before[name]), name)
		}
	})

	Context("between user scope and a project", func() {
		defaultYML := func() string { return filepath.Join(p.UserProfiles, "default.yml") }
		inProject := func(modules string) {
			GinkgoHelper()
			writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n" + modules})
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll})
			Expect(err).NotTo(HaveOccurred())
		}

		It("moves a module from project.yml into default at user scope: added there first, removed here, the version kept", func() {
			inProject("  - {name: notes, source: " + url + "}\n")
			was := installed(sc)
			writeTree(work, map[string]string{"commands/notes.md": "two\n"})
			gitIn(work, "commit", "-q", "-am", "two")
			gitIn(work, "push", "-q", bare, "main")
			res, err := move(engine.MoveRequest{Scope: user, Other: &sc, Modules: []string{"notes"}, To: "base"})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Syncs).To(HaveLen(2))
			Expect(res.Syncs[0].Scope).To(Equal("user"), "the target scope's sync first")
			Expect(res.Edits).To(HaveExactElements(And(HaveField("Action", "removed"), HaveField("Scope", "project")),
				And(HaveField("Action", "added"), HaveField("Scope", "user"))))
			Expect(read(defaultYML())).To(ContainSubstring("name: notes"))
			Expect(read(projectYML())).NotTo(ContainSubstring("notes"))
			Expect(installed(user).Commit).To(Equal(was.Commit), "the version the project had")
			Expect(installed(sc)).To(BeNil())
			Expect(read(p.Pins)).To(ContainSubstring(was.Commit))
			Expect(filepath.Join(proj, ".claude", "commands", "notes.md")).NotTo(BeAnExistingFile())
			Expect(read(filepath.Join(p.Home, ".claude", "commands", "notes.md"))).To(Equal("one\n"))
		})

		It("keeps the version installed though pins.yaml holds a newer pin for the key, which the next plain sync applies", func() {
			inProject("  - {name: notes, source: " + url + "}\n")
			was := installed(sc)
			writeTree(work, map[string]string{"commands/notes.md": "two\n"})
			gitIn(work, "commit", "-q", "-am", "two")
			gitIn(work, "push", "-q", bare, "main")
			// An off profile of yours lists notes, and pins.yaml its newer version, as another machine synced it.
			writeTree(p.UserProfiles, map[string]string{"later.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + url + "}\n"})
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: user, Update: true, Profiles: &engine.ProfileChange{Action: engine.ProfileSwitch, Name: "user:later"}})
			Expect(err).NotTo(HaveOccurred())
			newer := installed(user).Commit
			Expect(newer).NotTo(Equal(was.Commit))
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: user, Profiles: &engine.ProfileChange{Action: engine.ProfileOff}, ConfirmRemovals: yes})
			Expect(err).NotTo(HaveOccurred())
			Expect(installed(user)).To(BeNil())
			Expect(read(p.Pins)).To(ContainSubstring(newer))

			_, err = move(engine.MoveRequest{Scope: user, Other: &sc, Modules: []string{"notes"}, To: "base"})
			Expect(err).NotTo(HaveOccurred())
			Expect(installed(user).Commit).To(Equal(was.Commit), "the version installed is kept")
			Expect(read(filepath.Join(p.Home, ".claude", "commands", "notes.md"))).To(Equal("one\n"))
			Expect(read(p.Pins)).To(ContainSubstring(newer), "the synced pin is kept")
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: user})
			Expect(err).NotTo(HaveOccurred())
			Expect(installed(user).Commit).To(Equal(newer))
		})

		It("moves from user scope into your profile used in a project", func() {
			writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + url + "}\n",
				"me.yml": "version: 1\ntargets: [claude-code]\n"})
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: user})
			Expect(err).NotTo(HaveOccurred())
			was := installed(user)
			activate(sc, "user:me")
			_, err = move(engine.MoveRequest{Scope: sc, Other: &user, Modules: []string{"notes"}, To: "user:me"})
			Expect(err).NotTo(HaveOccurred())
			Expect(installed(sc).From).To(Equal("profile:user:me"))
			Expect(installed(sc).Commit).To(Equal(was.Commit))
			Expect(installed(user)).To(BeNil())
			Expect(read(defaultYML())).NotTo(ContainSubstring("notes"))
			Expect(read(filepath.Join(p.UserProfiles, "me.yml"))).To(ContainSubstring("name: notes"))
		})

		It("moves within the target scope when its layers list the module, though the other scope's do too", func() {
			writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + url + "}\n",
				"me.yml": "version: 1\ntargets: [claude-code]\n"})
			inProject("  - {name: notes, source: " + url + "}\n")
			activate(sc, "user:me")
			before := read(defaultYML())
			res, err := move(engine.MoveRequest{Scope: sc, Other: &user, Modules: []string{"notes"}, To: "me"})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Syncs).To(HaveLen(1))
			Expect(read(projectYML())).NotTo(ContainSubstring("notes"))
			Expect(read(defaultYML())).To(Equal(before))
		})

		It("a module both scopes provide refuses, when the run names both, naming the layer to move from", func() {
			writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + url + "}\n"})
			writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + url + "}\n",
				"me.yml": "version: 1\n"})
			_, err := move(engine.MoveRequest{Scope: user, Other: &sc, Across: true, Modules: []string{"notes"}, To: "me"})
			Expect(err).To(MatchError("notes is in default (base) at user scope and in project.yml (base) in this project; name the layer it moves from"))
			Expect(errs.Hints(err)).To(Equal([]string{"lola profile --user --move notes --to me default", "lola profile --user --move notes --to me project.yml"}))
		})

		It("refuses modules of both scopes in one run, and those of two layers of the other scope, hinting a run for each", func() {
			otherURL := serveOther()
			writeTree(proj, map[string]string{".lola/profiles/team.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: other, source: " + otherURL + "}\n"})
			activate(sc, "project:team")
			inProject("  - {name: notes, source: " + url + "}\n")
			_, err := move(engine.MoveRequest{Scope: user, Other: &sc, Modules: []string{"notes", "other"}, To: "base"})
			Expect(err).To(MatchError("notes is in project.yml (base) and other in profile team in this project; " +
				"a move between scopes takes the modules of one layer"))
			Expect(errs.Hints(err)).To(Equal([]string{"lola profile --user project.yml --move notes --to base", "lola profile --user team --move other --to base"}))

			writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: other, source: " + otherURL + "}\n",
				"me.yml": "version: 1\n"})
			activate(user, "user:me")
			_, err = move(engine.MoveRequest{Scope: user, Other: &sc, Modules: []string{"notes", "other"}, To: "me"})
			Expect(err).To(MatchError("other is listed at user scope and notes in this project; a move takes the modules of one scope"))
			Expect(errs.Hints(err)).To(Equal([]string{"lola profile --user --move other --to me", "lola profile --user --move notes --to me"}))
		})

		It("refuses a typo as the target scope does, and a move from user scope into a file of the project", func() {
			inProject("  - {name: notes, source: " + url + "}\n")
			_, err := move(engine.MoveRequest{Scope: user, Other: &sc, Modules: []string{"nope"}, To: "base"})
			Expect(err).To(MatchError("nope: no layer at user scope lists it"))
			_, err = move(engine.MoveRequest{Scope: user, Other: &sc, From: "tem", Modules: []string{"notes"}, To: "base"})
			Expect(err).To(MatchError("no profile tem for user scope"))

			writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: other, source: " + serveOther() + "}\n"})
			writeTree(proj, map[string]string{".lola/profiles/team.yml": "version: 1\n"})
			for _, to := range []string{"base", "project.yml", "team"} {
				_, err = move(engine.MoveRequest{Scope: sc, Other: &user, Modules: []string{"other"}, To: to})
				Expect(err).To(MatchError("other comes from user scope; lola profile --move moves an entry from user scope into a profile of yours only"), to)
				Expect(errs.Hints(err)).To(BeEmpty(), "you have no profile to move it into")
			}

			// With a profile of yours, the hint is the move into it: with --switch while it is off, as is when it is on.
			writeTree(p.UserProfiles, map[string]string{"me.yml": "version: 1\n"})
			_, err = move(engine.MoveRequest{Scope: sc, Other: &user, Modules: []string{"other"}, To: "base"})
			Expect(errs.Hints(err)).To(Equal([]string{"lola profile --project --move other --to me --switch"}))
			activate(sc, "user:me")
			_, err = move(engine.MoveRequest{Scope: sc, Other: &user, Modules: []string{"other"}, To: "base"})
			Expect(errs.Hints(err)).To(Equal([]string{"lola profile --project --move other --to me"}))
			_, err = move(engine.MoveRequest{Scope: sc, Other: &user, Modules: []string{"other"}, To: "me"}) // the hint's run
			Expect(err).NotTo(HaveOccurred())
			Expect(read(filepath.Join(p.UserProfiles, "me.yml"))).To(ContainSubstring("name: other"))
		})

		It("names a profile of yours a project profile has the name of by its kind, so the hint runs as printed", func() {
			writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: other, source: " + serveOther() + "}\n",
				"me.yml": "version: 1\n"})
			writeTree(proj, map[string]string{".lola/profiles/me.yml": "version: 1\n"})
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: user})
			Expect(err).NotTo(HaveOccurred())
			_, err = move(engine.MoveRequest{Scope: sc, Other: &user, Modules: []string{"other"}, To: "base"})
			Expect(errs.Hints(err)).To(Equal([]string{"lola profile --project --move other --to user:me --switch"}))
			_, err = move(engine.MoveRequest{Scope: sc, Other: &user, Modules: []string{"other"}, To: "user:me", Switch: true}) // the hint's run
			Expect(err).NotTo(HaveOccurred())
			Expect(read(filepath.Join(p.UserProfiles, "me.yml"))).To(ContainSubstring("name: other"))
			Expect(read(filepath.Join(proj, ".lola", "profiles", "me.yml"))).To(Equal("version: 1\n"))
		})

		It("names what the hinted switch turns off, and hints no switch into a profile that could ask for consent", func() {
			writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: other, source: " + serveOther() + "}\n",
				"me.yml": "version: 1\n", "you.yml": "version: 1\nbundle: [me]\n"})
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: user})
			Expect(err).NotTo(HaveOccurred())
			writeTree(proj, map[string]string{".lola/profiles/team.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + url + "}\n"})
			activate(sc, "project:team")
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll})
			Expect(err).NotTo(HaveOccurred())
			_, err = move(engine.MoveRequest{Scope: sc, Other: &user, Modules: []string{"other"}, To: "base"})
			Expect(errs.Hints(err)).To(Equal([]string{"switches team off, removing its modules: lola profile --project --move other --to me --switch"}),
				"you is a bundle")
		})

		It("refuses a move into the file of yours the entry is in, which both scopes read", func() {
			writeTree(p.UserProfiles, map[string]string{"me.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + url + "}\n"})
			activate(sc, "user:me")
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll})
			Expect(err).NotTo(HaveOccurred())
			before := read(filepath.Join(p.UserProfiles, "me.yml"))
			_, err = move(engine.MoveRequest{Scope: user, Other: &sc, Modules: []string{"notes"}, To: "me", Switch: true, Yes: true})
			Expect(err).To(MatchError("notes is in profile me already"))
			Expect(read(filepath.Join(p.UserProfiles, "me.yml"))).To(Equal(before))
		})

		// untouched says nothing reached user scope.
		untouched := func() {
			GinkgoHelper()
			Expect(filepath.Join(p.UserProfiles, "default.yml")).NotTo(BeAnExistingFile())
			Expect(installed(user)).To(BeNil())
			Expect(filepath.Join(p.Home, ".claude", "commands", "notes.md")).NotTo(BeAnExistingFile())
		}
		notInstalled := "notes is not installed in this project as its files list it now; lola copies or moves out of a project only what it installed as you trusted it"

		It("refuses to carry an entry the project's trust record does not cover out of it", func() {
			writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + url + "}\n"})
			_, err := move(engine.MoveRequest{Scope: user, Other: &sc, Modules: []string{"notes"}, To: "base"})
			Expect(err).To(MatchError(notInstalled))
			Expect(errs.Hints(err)).To(Equal([]string{"review the changes first: lola sync --project notes"}))
			untouched()

			writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\n",
				".lola/profiles/team.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + url + "}\n"})
			_, err = move(engine.MoveRequest{Scope: user, Other: &sc, From: "team", Modules: []string{"notes"}, To: "base"})
			Expect(err).To(MatchError("notes is in profile team, which is off in this project; lola copies or moves out of a project only what it installed as you trusted it"))
			Expect(errs.Hints(err)).To(Equal([]string{"review it first: lola profile --project team --switch"}))
			untouched()
		})

		It("refuses carrying a project's folder out of it before judging its content, changed or as trusted", func() {
			writeTree(proj, map[string]string{"mods/notes/lola.yml": "name: notes\n", "mods/notes/commands/notes.md": "trusted\n"})
			inProject("  - {name: notes, source: ./mods/notes}\n")
			refused := "notes (" + defaultYML() + ") installs from " + filepath.Join(proj, "mods", "notes") + ", inside project " + proj +
				": changes there install without review wherever that file is on; lola keeps a project's local modules out of your files"
			writeTree(proj, map[string]string{"mods/notes/commands/notes.md": "pulled\n"}) // as a pull would
			before := read(projectYML())
			_, err := move(engine.MoveRequest{Scope: user, Other: &sc, Modules: []string{"notes"}, To: "base"})
			Expect(err).To(MatchError(refused))
			untouched()
			Expect(read(projectYML())).To(Equal(before))
			Expect(read(filepath.Join(proj, ".claude", "commands", "notes.md"))).To(Equal("trusted\n"))

			writeTree(proj, map[string]string{"mods/notes/commands/notes.md": "trusted\n"})
			_, err = move(engine.MoveRequest{Scope: user, Other: &sc, Modules: []string{"notes"}, To: "base"})
			Expect(err).To(MatchError(refused))
			untouched()
		})

		It("never carries targets a project lists but has not installed: refused, and from user scope the targets installed are written", func() {
			inProject("  - {name: notes, source: " + url + "}\n")
			writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code, opencode]\nmodules:\n  - {name: notes, source: " + url + "}\n"})
			_, err := move(engine.MoveRequest{Scope: user, Other: &sc, Modules: []string{"notes"}, To: "base"})
			Expect(err).To(MatchError(notInstalled), "an untrusted targets line")
			untouched()

			otherURL := serveOther()
			writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: other, source: " + otherURL + "}\n",
				"me.yml": "version: 1\n"})
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: user})
			Expect(err).NotTo(HaveOccurred())
			writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code, gemini-cli]\nmodules:\n  - {name: other, source: " + otherURL + "}\n"})
			writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + url + "}\n"})
			activate(sc, "user:me")
			_, err = move(engine.MoveRequest{Scope: sc, Other: &user, Modules: []string{"other"}, To: "me"})
			Expect(err).NotTo(HaveOccurred())
			Expect(read(filepath.Join(p.UserProfiles, "me.yml"))).NotTo(ContainSubstring("gemini-cli"), "other is installed to claude-code only")
			led, err := e.Ledger(sc)
			Expect(err).NotTo(HaveOccurred())
			Expect(led.Modules["other"].Targets).To(Equal([]string{"claude-code"}))
		})

		It("asks for the consent the source's run needs before the add, and the hint gives it", func() {
			writeTree(proj, map[string]string{"ci/check.sh": "#!/bin/sh\nexit 0\n"})
			Expect(os.Chmod(filepath.Join(proj, "ci", "check.sh"), 0o755)).To(Succeed())
			writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\npre_install: ci/check.sh\nmodules:\n  - {name: notes, source: " + url + "}\n"})
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll, Options: engine.Options{Trust: engine.TrustScripts}})
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(filepath.Join(proj, "ci", "check.sh"), []byte("#!/bin/sh\necho changed\n"), 0o755)).To(Succeed()) // its consent was for the old digest
			_, err = e.Move(ctx, engine.MoveRequest{Scope: user, Other: &sc, Modules: []string{"notes"}, To: "base", ConfirmRemovals: yes})
			Expect(err).To(MatchError("removing notes in this project needs your trust (--trust=scripts), and there is no terminal to ask on"))
			Expect(errs.Hints(err)).To(Equal([]string{"then accept it: lola profile --user --move notes --to base --trust=scripts"}))
			untouched()
			Expect(installed(sc)).NotTo(BeNil())

			_, err = e.Move(ctx, engine.MoveRequest{Scope: user, Other: &sc, Modules: []string{"notes"}, To: "base", ConfirmRemovals: yes,
				Options: engine.Options{Trust: engine.TrustScripts}}) // the hint's run
			Expect(err).NotTo(HaveOccurred())
			Expect(installed(sc)).To(BeNil())
			Expect(installed(user)).NotTo(BeNil())
		})

		It("asks about the removals before it writes anything: a No, or no terminal, leaves both scopes as they were", func() {
			inProject("  - {name: notes, source: " + url + "}\n")
			before := read(projectYML())
			_, err := move(engine.MoveRequest{Scope: user, Other: &sc, Modules: []string{"notes"}, To: "base",
				ConfirmRemovals: func(rs []engine.Removal) (bool, error) {
					Expect(rs).To(HaveExactElements(HaveField("Module", "notes")))
					return false, nil
				}})
			Expect(errors.As(err, new(*engine.RemovalsDeclinedError))).To(BeTrue(), "%v", err)
			_, err = e.Move(ctx, engine.MoveRequest{Scope: user, Other: &sc, Modules: []string{"notes"}, To: "base", Trust: trustAll})
			Expect(err).To(MatchError("lola profile needs your confirmation to remove notes, and there is no terminal to ask on"))
			Expect(errs.Hints(err)).To(Equal([]string{"see them: lola profile --user --move notes --to base -n", "then confirm them: lola profile --user --move notes --to base -y"}))
			Expect(read(projectYML())).To(Equal(before))
			Expect(filepath.Join(p.UserProfiles, "default.yml")).NotTo(BeAnExistingFile())
			Expect(installed(user)).To(BeNil())
			Expect(installed(sc)).NotTo(BeNil())
		})

		It("reads what it carries again under the scope locks, after every question: a change since the move read it refuses, writing nothing", func() {
			inProject("  - {name: notes, source: " + url + "}\n")
			edited := strings.Replace(read(projectYML()), "name: notes, ", "name: notes, rules: false, ", 1)
			Expect(edited).To(ContainSubstring("rules: false"))
			_, err := move(engine.MoveRequest{Scope: user, Other: &sc, Modules: []string{"notes"}, To: "base",
				ConfirmRemovals: func([]engine.Removal) (bool, error) {
					writeTree(proj, map[string]string{".lola/project.yml": edited}) // as a pull while you are asked would
					return true, nil
				}})
			Expect(err).To(MatchError("notes changed in this project while lola was moving it; nothing was written"))
			Expect(errs.Hints(err)).To(Equal([]string{"run it again: lola profile --user --move notes --to base"}))
			untouched()
			Expect(read(projectYML())).To(Equal(edited))
			Expect(installed(sc)).NotTo(BeNil())
		})

		It("a dry run writes nothing, and its run to apply answers both scopes' questions", func() {
			inProject("  - {name: notes, source: " + url + "}\n")
			before := read(projectYML())
			res, err := move(engine.MoveRequest{Scope: user, Other: &sc, Modules: []string{"notes"}, To: "base", Options: engine.Options{DryRun: true}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Syncs).To(HaveLen(2))
			Expect(res.Syncs[1].Removals).To(HaveExactElements(HaveField("Module", "notes")))
			Expect(res.Syncs[0].ToApply).To(Equal("lola profile --user --move notes --to base"))
			Expect(res.Syncs[0].OffTerminal).To(BeEmpty(), "R-8: a dry run's hint carries no -y you did not give")
			Expect(res.Syncs[1].ToApply).To(BeEmpty(), "one run applies both")
			Expect(read(projectYML())).To(Equal(before))
			Expect(filepath.Join(p.UserProfiles, "default.yml")).NotTo(BeAnExistingFile())
		})

		It("creates the profile --to names at the target scope before the move, and writes in the targets the entry installed to", func() {
			writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [gemini-cli]\nmodules:\n  - {name: notes, source: " + url + "}\n"})
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll})
			Expect(err).NotTo(HaveOccurred())
			was := installed(sc)
			res, err := move(engine.MoveRequest{Scope: user, Other: &sc, Modules: []string{"notes"}, To: "fresh", Switch: true})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Created).To(Equal(filepath.Join(p.UserProfiles, "fresh.yml")))
			Expect(res.Syncs[0].Activation.Changes).To(ConsistOf(And(HaveField("Module", "notes"), HaveField("Version", was.Commit[:12]))),
				"the line names the commit the move carries, not the source")
			Expect(read(filepath.Join(p.UserProfiles, "fresh.yml"))).To(ContainSubstring("gemini-cli"))
			Expect(installed(user)).To(And(HaveField("From", "profile:user:fresh"), HaveField("Commit", was.Commit), HaveField("Targets", []string{"gemini-cli"})))
			led, err := e.Ledger(user)
			Expect(err).NotTo(HaveOccurred())
			Expect(led.Profile).To(HaveField("Name", "user:fresh"))
		})

		It("adds before it removes: a failure between the two leaves a reported duplicate, never a loss, and the hint finishes the move", func() {
			if os.Geteuid() == 0 {
				Skip("root writes into a read-only directory")
			}
			inProject("  - {name: notes, source: " + url + "}\n")
			was := installed(sc)
			commands := filepath.Join(proj, ".claude", "commands")
			Expect(os.Chmod(commands, 0o500)).To(Succeed()) // the project's removal cannot delete notes.md
			DeferCleanup(func() { _ = os.Chmod(commands, 0o700) })
			_, err := move(engine.MoveRequest{Scope: user, Other: &sc, Modules: []string{"notes"}, To: "base"})
			Expect(err).To(MatchError(HavePrefix("notes was added to default (base) at user scope but is still in project.yml (base) in this project: ")))
			Expect(errs.Hints(err)).To(HaveExactElements("finish the move: lola profile --user project.yml --move notes --to base -y"))
			Expect(read(defaultYML())).To(ContainSubstring("name: notes"))
			Expect(read(projectYML())).To(ContainSubstring("name: notes"))
			Expect(installed(user)).NotTo(BeNil())
			Expect(installed(sc)).NotTo(BeNil())

			Expect(os.Chmod(commands, 0o700)).To(Succeed())
			_, err = move(engine.MoveRequest{Scope: user, Other: &sc, From: "project.yml", Modules: []string{"notes"}, To: "base", Yes: true})
			Expect(err).NotTo(HaveOccurred(), "the hint's run")
			Expect(read(projectYML())).NotTo(ContainSubstring("notes"))
			Expect(installed(sc)).To(BeNil())
			Expect(installed(user).Commit).To(Equal(was.Commit))
		})
	})
})
