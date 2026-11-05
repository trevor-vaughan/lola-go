package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"

	"github.com/gofrs/flock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
	"github.com/trevor-vaughan/lola-go/internal/trust"
)

var _ = Describe("remote profiles", func() {
	var (
		tmp, proj, work, url string
		commit, digest       string
		p                    paths.Paths
		e                    *engine.Engine
		user, sc             engine.Scope
		requests             atomic.Int32
		ctx                  = context.Background()
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
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		user, sc = engine.Scope{Kind: target.User}, engine.Scope{Kind: target.Project, ProjectDir: proj}
		// The module example, served over loopback git with every request counted, and its pin.
		work = filepath.Join(tmp, "work")
		writeTree(work, exampleModule)
		gitIn(work, "init", "-q", "-b", "main")
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "init")
		gitIn(tmp, "clone", "-q", "--bare", work, filepath.Join(tmp, "srv", "example.git"))
		url = testenv.ServeGitTLS(filepath.Join(tmp, "srv"), func(*http.Request) { requests.Add(1) }) + "/example.git"
		commit = gitIn(work, "rev-parse", "HEAD")
		// The pin is computed with a cache of its own, so the specs start with an empty one.
		other := p
		other.Store, other.StoreLock, other.Staging = filepath.Join(tmp, "pin"), filepath.Join(tmp, "pin.lock"), filepath.Join(tmp, "pin-staging")
		v, err := engine.New(other).Fetch(ctx, source.Spec{Kind: source.KindGit, Location: url})
		Expect(err).NotTo(HaveOccurred())
		digest = v.Meta.Digest
		requests.Store(0)
	})
	// pinned is a profile whose one entry is example at its pin.
	pinned := func() string {
		return "version: 1\ntargets: [claude-code]\nmodules:\n  - name: example\n    source: " + url + "\n    commit: " + commit +
			"\n    digest: " + digest + "\n"
	}
	// keep writes a remote profile as lola profile --add does: profile.yml and its source.json.
	keep := func(name, content string) {
		GinkgoHelper()
		dir := filepath.Join(p.RemoteProfiles, name)
		Expect(os.MkdirAll(dir, 0o700)).To(Succeed())
		b, err := json.Marshal(profile.Origin{Version: 1, Source: "https://example.com/profiles.git", Commit: commit,
			Digest: profile.Digest([]byte(content))})
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(dir, "source.json"), b, 0o600)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dir, "profile.yml"), []byte(content), 0o600)).To(Succeed())
	}
	ledgerPath := func(s engine.Scope) string {
		if s.Kind == target.User {
			return p.UserLedger
		}
		return filepath.Join(s.ProjectDir, ".lola", "lock.json")
	}
	activate := func(s engine.Scope, name string) {
		GinkgoHelper()
		led, err := e.Ledger(s)
		Expect(err).NotTo(HaveOccurred())
		led.Profile = nil
		if name != "" {
			led.Profile = &ledger.Active{Name: name, By: ledger.ByOn}
		}
		Expect(os.MkdirAll(filepath.Dir(ledgerPath(s)), 0o755)).To(Succeed())
		Expect(led.Save(ledgerPath(s))).To(Succeed())
	}
	active := func(s engine.Scope) string {
		GinkgoHelper()
		led, err := e.Ledger(s)
		Expect(err).NotTo(HaveOccurred())
		if led.Profile == nil {
			return ""
		}
		return led.Profile.Name
	}
	yes := func([]engine.Removal) (bool, error) { return true, nil }
	turn := func(s engine.Scope, action engine.ProfileAction, name string) (*engine.SyncResult, error) {
		return e.Sync(ctx, engine.SyncRequest{Scope: s, Profiles: &engine.ProfileChange{Action: action, Name: name}, ConfirmRemovals: yes})
	}
	// failed is the error of an update of one profile: the run's, else its profile's (§19 P21).
	failed := func(res *engine.ProfileUpdateResult, err error) error {
		if err != nil {
			return err
		}
		for _, u := range res.Updates {
			if u.Err != nil {
				return u.Err
			}
		}
		return nil
	}
	shipped := func(root string) string { return filepath.Join(root, ".claude", "commands", "ship.md") }
	userRoot := func() string { return p.Home }

	It("installs a remote profile's pinned entries at user scope and in a project, as your own", func() {
		keep("go", pinned())
		res, err := turn(user, engine.ProfileOn, "go")
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Activation.Profile).To(HaveValue(Equal("remote:go")))
		Expect(shipped(userRoot())).To(BeAnExistingFile())
		led, err := e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Modules["example"].From).To(Equal("profile:remote:go"))
		Expect(led.Modules["example"].Commit).To(Equal(commit))

		// At project scope it needs no trust: the consent was given when it was added (D5).
		res, err = turn(sc, engine.ProfileOn, "remote:go")
		Expect(err).NotTo(HaveOccurred(), "no Trust callback, and none needed")
		Expect(res.Trust.Changes).To(BeEmpty())
		Expect(shipped(proj)).To(BeAnExistingFile())
		Expect(active(sc)).To(Equal("remote:go"))

		// Its pin is its own: sync --update of its module points at --update of the profile.
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: user, Modules: []string{"example"}, Update: true})
		Expect(err).To(MatchError("example: its pin is in remote:go, which only lola profile --update changes"))
		Expect(errs.Hints(err)).To(Equal([]string{"update it: lola profile --user remote:go --update"}), "the update applies what it moves")
	})

	It("lola update leaves a remote profile's module at its pin, pointing at lola profile --update", func() {
		keep("go", pinned())
		_, err := turn(user, engine.ProfileOn, "go")
		Expect(err).NotTo(HaveOccurred())
		requests.Store(0)
		res, err := e.Sync(ctx, engine.SyncRequest{Scope: user, Modules: []string{"example"}, Update: true, Installed: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo,
			Text: "example: its pin is in remote:go; update it: lola profile --user remote:go --update"}))
		Expect(res.Pins).To(BeEmpty())
		Expect(requests.Load()).To(BeZero())
		led, err := e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Modules["example"].Commit).To(Equal(commit))
	})

	It("a local profile shadows a remote one of the same name", func() {
		keep("go", pinned())
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		rows, _, err := e.Profiles(ctx, user)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(ContainElement(And(HaveField("Kind", "remote"), HaveField("ShadowedBy", HaveValue(Equal("user:go"))),
			HaveField("Source", "https://example.com/profiles.git"), HaveField("Commit", commit))))
		res, err := turn(user, engine.ProfileOn, "go")
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Activation.Profile).To(HaveValue(Equal("user:go")), "a bare name is the local one")
		_, err = turn(user, engine.ProfileOn, "remote:go")
		Expect(err).To(MatchError("remote:go is shadowed by your profile user:go (" + filepath.Join(p.UserProfiles, "go.yml") +
			"): a local profile always wins over a remote one of its name"))
		Expect(errs.Hints(err)).To(Equal([]string{"use yours: lola profile --user user:go --on",
			"or use both, bundled: lola profile --user go-both --switch --new --bundle user:go,remote:go",
			"or rename or delete " + filepath.Join(p.UserProfiles, "go.yml")}))
		res, err = e.Sync(ctx, engine.SyncRequest{Scope: user, ConfirmRemovals: yes, Profiles: &engine.ProfileChange{Action: engine.ProfileSwitch, New: true,
			Name: "go-both", Bundle: []string{"user:go", "remote:go"}}}) // the hint as printed
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Activation.Profile).To(HaveValue(Equal("user:go-both")))
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "go-both.yml"))).To(Equal([]byte("version: 1\nbundle: ['user:go', 'remote:go']\n")))
		Expect(shipped(userRoot())).To(BeAnExistingFile(), "the remote one's module installs beside yours")
		Expect(os.Remove(filepath.Join(p.UserProfiles, "go-both.yml"))).To(Succeed())

		// A user:go that appears after remote:go was turned on shadows it, with a notice.
		Expect(os.Remove(filepath.Join(p.UserProfiles, "go.yml"))).To(Succeed())
		activate(user, "remote:go")
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: user})
		Expect(err).NotTo(HaveOccurred())
		Expect(shipped(userRoot())).To(BeAnExistingFile())
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		res, err = e.Sync(ctx, engine.SyncRequest{Scope: user, ConfirmRemovals: yes})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn, Text: "user:go now replaces remote:go"}))
		Expect(shipped(userRoot())).NotTo(BeAnExistingFile(), "yours lists nothing, so the remote's module goes")
		Expect(active(user)).To(Equal("remote:go"), "a sync never rewrites the list")
		// --on of another profile never replaces it unasked, and the stored name stays (ruling R-6b3b-8, M13).
		writeTree(p.UserProfiles, map[string]string{"docs.yml": "version: 1\n"})
		_, err = turn(user, engine.ProfileOn, "docs")
		Expect(err).To(MatchError("remote:go is on, and --on does not replace it without asking; --switch does"))
		Expect(active(user)).To(Equal("remote:go"))
		// A bare --off of go turns off the profile on: remote:go, read as yours.
		res, err = turn(user, engine.ProfileOff, "go")
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Activation.Named).To(Equal("remote:go"))
		Expect(active(user)).To(BeEmpty())
	})

	It("an ambiguous bare profile name refuses with every qualified name", func() {
		keep("go", pinned())
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		writeTree(proj, map[string]string{".lola/profiles/go.yml": "version: 1\n"})
		_, err := turn(sc, engine.ProfileOn, "go")
		Expect(err).To(MatchError("go is ambiguous: your profile user:go (" + filepath.Join(p.UserProfiles, "go.yml") +
			") and the project's project:go (" + filepath.Join(proj, ".lola", "profiles", "go.yml") + ") are both named go"))
		Expect(errs.Hints(err)).To(Equal([]string{"yours: lola profile --project user:go --on", "the project's: lola profile --project project:go --on"}))
		_, err = turn(sc, engine.ProfileOn, "remote:go")
		Expect(err).To(MatchError(ContainSubstring("remote:go is shadowed by your profile user:go")))
		Expect(os.Remove(filepath.Join(p.UserProfiles, "go.yml"))).To(Succeed())
		_, err = turn(sc, engine.ProfileOn, "remote:go")
		Expect(err).To(MatchError(ContainSubstring("remote:go is shadowed by the project's project:go")))
		Expect(errs.Hints(err)).To(ContainElement("or rename or delete " + filepath.Join(proj, ".lola", "profiles", "go.yml")))
	})

	It("never swaps an active remote profile for a project's except when you turn that on", func() {
		keep("go", pinned())
		activate(sc, "remote:go")
		writeTree(proj, map[string]string{".lola/profiles/go.yml": "version: 1\n"})
		res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn, Text: "project:go also exists → lola profile --project project:go --on"}))
		Expect(shipped(proj)).To(BeAnExistingFile())

		// A bare --off of go means the profile on, the remote one: a dry run says it would turn it off.
		res, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOff, Name: "go"},
			Options: engine.Options{DryRun: true}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Activation.Named).To(Equal("remote:go"))
		Expect(active(sc)).To(Equal("remote:go"), "a dry run")

		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOn, Name: "project:go"},
			ConfirmRemovals: yes})
		var refusal *engine.TrustRefusalError
		Expect(errors.As(err, &refusal)).To(BeTrue(), "%v", err)
		Expect(refusal.Report.Replacements).To(Equal([]engine.Replacement{{Profile: "project:go", Replaces: "remote:go", Modules: []string{"example"}}}))
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOn, Name: "project:go"},
			ConfirmRemovals: yes, Trust: func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }})
		Expect(err).NotTo(HaveOccurred())
		Expect(active(sc)).To(Equal("project:go"))
	})

	It("shows an active remote profile a profile of yours shadows with its own modules", func() {
		keep("go", pinned())
		activate(user, "remote:go")
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		states, _, err := e.ProfileState(ctx, []engine.Scope{user}, "remote:go")
		Expect(err).NotTo(HaveOccurred())
		Expect(states).To(HaveLen(1))
		Expect(states[0].Active).To(BeTrue())
		Expect(states[0].ShadowedBy).To(HaveValue(Equal("user:go")))
		Expect(states[0].Modules).To(Equal([]engine.ProfileModule{{Name: "example", Overrides: []string{}, Only: []string{}, Skip: []string{}}}),
			"remote:go's own entries, which user:go, read in its place, does not want")
		Expect(states[0].ReadAs).To(HaveValue(Equal("user:go")))

		// A project profile of its name shadows it too, but is never read in its place.
		Expect(os.Remove(filepath.Join(p.UserProfiles, "go.yml"))).To(Succeed())
		writeTree(proj, map[string]string{".lola/profiles/go.yml": "version: 1\n"})
		activate(sc, "remote:go")
		states, notices, err := e.ProfileState(ctx, []engine.Scope{sc}, "remote:go")
		Expect(err).NotTo(HaveOccurred())
		Expect(states[0].ShadowedBy).To(HaveValue(Equal("project:go")))
		Expect(states[0].ReadAs).To(BeNil())
		Expect(states[0].Modules).To(Equal([]engine.ProfileModule{{Name: "example", From: "profile:remote:go", Overrides: []string{}, Only: []string{}, Skip: []string{}}}))
		Expect(notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "to use project:go instead: lola profile --project project:go --on"}))

		// project:go on in its place (one profile on at a time): remote:go is off, and its module
		// comes from the project's profile.
		writeTree(proj, map[string]string{".lola/profiles/go.yml": "version: 1\nmodules:\n  - {name: example, source: " + url +
			", commit: " + commit + ", digest: " + digest + "}\n"})
		activate(sc, "project:go")
		states, notices, err = e.ProfileState(ctx, []engine.Scope{sc}, "remote:go")
		Expect(err).NotTo(HaveOccurred())
		Expect(states[0].Active).To(BeFalse())
		Expect(states[0].ReadAs).To(BeNil())
		Expect(states[0].Modules).To(Equal([]engine.ProfileModule{{Name: "example", From: "profile:project:go", Overrides: []string{}, Only: []string{}, Skip: []string{}}}))
		Expect(notices).NotTo(ContainElement(HaveField("Text", HavePrefix("to use project:go instead"))))
	})

	It("lola info's re-pick of a module a remote profile provides is the switch to a copy of yours, which lola can edit", func() {
		keep("go", pinned())
		_, err := turn(user, engine.ProfileOn, "remote:go")
		Expect(err).NotTo(HaveOccurred())
		info, err := e.Info(ctx, engine.InfoRequest{Cwd: proj, Arg: "example", Scope: engine.ScopeRequest{Cwd: proj, User: true}, Targets: []string{"claude-code"},
			Rerun: func(extra ...string) string { return "lola info" }, UserRun: "lola info --user",
			Install: func(arg, ref, path string, k engine.TrustKinds) string { return "lola install " + arg }})
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Install[0]).To(And(HaveField("From", "profile:remote:go"), HaveField("Change", "lola profile --user go-mine --switch --new --from remote:go")))
		Expect(info.Install[0].Components).To(ContainElement(And(HaveField("Name", "review"), HaveField("State", engine.ComponentInstalled))))
	})

	It("lola info keeps the refusal of a scope whose active profile does not load, with its hint, and names no components or re-pick there", func() {
		keep("go", pinned())
		_, err := turn(user, engine.ProfileOn, "remote:go")
		Expect(err).NotTo(HaveOccurred())
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\nbogus: x\n"})
		activate(user, "user:go")
		info, err := e.Info(ctx, engine.InfoRequest{Cwd: proj, Arg: "example", Scope: engine.ScopeRequest{Cwd: proj, User: true}, Targets: []string{"claude-code"},
			Rerun: func(extra ...string) string { return "lola info" }, UserRun: "lola info --user",
			Install: func(arg, ref, path string, k engine.TrustKinds) string { return "lola install " + arg }})
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Install).To(HaveLen(1))
		Expect(info.Install[0].Refusal).NotTo(BeNil())
		Expect(info.Install[0].Refusal.Hints).To(ContainElement("fix it by hand"))
		Expect(info.Install[0]).To(And(HaveField("From", ""), HaveField("Components", []engine.InfoComponent{}), HaveField("Change", "")))
	})

	It("turning on the profile of yours that already stands in for an active remote one changes nothing", func() {
		keep("go", pinned())
		activate(user, "remote:go")
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		res, err := turn(user, engine.ProfileOn, "go")
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "user:go is already on, in place of remote:go"}))
		Expect(active(user)).To(Equal("remote:go"), "the stored name stays")
	})

	It("turning on a project profile replaces every active namesake of yours and every remote one", func() {
		keep("go", pinned())
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		writeTree(proj, map[string]string{".lola/profiles/go.yml": "version: 1\n"})
		for _, on := range []string{"user:go", "remote:go"} {
			activate(sc, on)
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOn, Name: "project:go"},
				ConfirmRemovals: yes, Trust: func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }})
			Expect(err).NotTo(HaveOccurred(), on)
			Expect(active(sc)).To(Equal("project:go"), on)
		}
	})

	It("refuses a remote profile that no longer has the bytes its pin records", func() {
		keep("go", pinned())
		activate(user, "remote:go")
		Expect(os.WriteFile(filepath.Join(p.RemoteProfiles, "go", "profile.yml"), []byte(pinned()+"description: edited\n"), 0o600)).To(Succeed())
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: user})
		Expect(err).To(MatchError(ContainSubstring("changed since you added it")))
		readd := []string{"remove it: lola profile --user remote:go --remove --force",
			"then add it again: lola profile --user go --add https://example.com/profiles.git",
			"lola wrote it when you added the profile; remove the profile and add it again"}
		Expect(errs.Hints(err)).To(Equal(readd))
		Expect(requests.Load()).To(BeZero())
		err = failed(e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Scopes: []engine.Scope{user}, Names: []string{"go"}, DryRun: true}))
		Expect(err).To(MatchError(ContainSubstring("changed since you added it")))
		Expect(errs.Hints(err)).To(Equal(readd))
		err = failed(e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Scopes: []engine.Scope{user}, Names: []string{"go"}, DryRun: true, Flags: []string{"-y"}, Trust: engine.TrustHooks, Rerun: []string{"--trust=hooks"}}))
		Expect(errs.Hints(err)).To(ContainElement("remove it: lola profile --user remote:go --remove --force"), "an update's -y never answers a removal")

		// Shadowed by a profile of yours, its state reads it alone, and coaches the same.
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		_, _, err = e.ProfileState(ctx, []engine.Scope{user}, "remote:go")
		Expect(err).To(MatchError(ContainSubstring("changed since you added it")))
		Expect(errs.Hints(err)).To(Equal(readd))
		Expect(os.Remove(filepath.Join(p.UserProfiles, "go.yml"))).To(Succeed())

		// --remove of it cannot tell whether it is in use, and never hints itself; the hinted
		// command removes it.
		yes := func(engine.RemoteProfile) (bool, error) { return true, nil }
		_, err = e.RemoveProfile(ctx, engine.ProfileRemoveRequest{Scope: user, Name: "go", Confirm: yes})
		Expect(err).To(MatchError(HavePrefix("lola cannot tell whether remote:go is in use at user scope: remote:go itself does not load: ")))
		Expect(errs.Hints(err)).To(Equal([]string{"or remove it anyway: lola profile --user remote:go --remove --force"}))
		Expect(os.WriteFile(filepath.Join(p.RemoteProfiles, "go", "source.json"), []byte("{}"), 0o600)).To(Succeed())
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: user})
		Expect(errs.Hints(err)).To(Equal([]string{"remove it: lola profile --user remote:go --remove --force",
			"lola wrote it when you added the profile; remove the profile and add it again"}), "no source to add it again from")
		res, err := e.RemoveProfile(ctx, engine.ProfileRemoveRequest{Scope: user, Name: "go", Force: true, Confirm: yes})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Removed).To(BeTrue())
	})

	It("never hints adding a remote profile again from a recorded source --add refuses", func() {
		keep("go", pinned())
		b, err := json.Marshal(profile.Origin{Version: 1, Source: "https://example.com/profiles.tar.gz", Ref: "v1",
			Digest: profile.Digest([]byte(pinned()))})
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(p.RemoteProfiles, "go", "source.json"), b, 0o600)).To(Succeed())
		err = failed(e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Scopes: []engine.Scope{user}, Names: []string{"go"}, DryRun: true}))
		Expect(err).To(MatchError(ContainSubstring("--ref applies to git sources only")))
		Expect(errs.Hints(err)).To(Equal([]string{"remove it: lola profile --user remote:go --remove --force",
			"its recorded source cannot be added again: --ref applies to git sources only, not to archive https://example.com/profiles.tar.gz; " +
				"add go from a source lola profile --add accepts"}))
		Expect(requests.Load()).To(BeZero())
	})

	It("a bundle of a remote profile and yours loads the remote one's entries first and yours last", func() {
		keep("go", pinned())
		mods := filepath.Join(tmp, "mods")
		writeTree(filepath.Join(mods, "a"), map[string]string{"lola.yml": "name: a\n", "commands/a.md": "A\n"})
		own := filepath.Join(p.UserProfiles, "mine-own.yml")
		writeTree(p.UserProfiles, map[string]string{"mine.yml": "version: 1\nbundle: [remote:go, mine-own]\n",
			"mine-own.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: " + filepath.Join(mods, "a") + "}\n"})
		res, err := turn(user, engine.ProfileOn, "mine")
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Activation.Changes).To(ConsistOf(HaveField("Module", "a"), HaveField("Module", "example")))
		Expect(shipped(userRoot())).To(BeAnExistingFile())
		led, err := e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Modules["example"].From).To(Equal("profile:remote:go"), "the layer is the member that gives it")
		states, _, err := e.ProfileState(ctx, []engine.Scope{user}, "mine")
		Expect(err).NotTo(HaveOccurred())
		Expect(states[0].Members).To(Equal([]string{"remote:go", "user:mine-own"}))
		Expect(states[0].Modules).To(ConsistOf(HaveField("Name", "a"), HaveField("Name", "example")))

		// Its pin is the remote profile's.
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: user, Modules: []string{"example"}, Update: true})
		Expect(err).To(MatchError("example: its pin is in remote:go, which only lola profile --update changes"))
		res, err = e.Sync(ctx, engine.SyncRequest{Scope: user, Update: true, ConfirmRemovals: yes})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo,
			Text: "example: --update leaves the pin remote:go sets; update it: lola profile --user remote:go --update"}), "the file that sets it")

		// The later member's entry of a module replaces the remote one whole, and says so.
		Expect(os.WriteFile(own, []byte("version: 1\ntargets: [claude-code]\nmodules:\n  - {name: example, source: "+url+
			", commit: "+commit+", digest: "+digest+", targets: [gemini-cli]}\n"), 0o600)).To(Succeed())
		res, err = e.Sync(ctx, engine.SyncRequest{Scope: user, ConfirmRemovals: yes})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(HaveField("Text", HavePrefix("example: profile mine-own overrides remote:go in bundle mine"))))
		Expect(shipped(userRoot())).NotTo(BeAnExistingFile(), "a is gone too, and example installs to gemini-cli only")
		led, err = e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Modules["example"].Targets).To(Equal([]string{"gemini-cli"}))
		Expect(led.Modules).NotTo(HaveKey("a"))
		states, _, err = e.ProfileState(ctx, []engine.Scope{user}, "mine")
		Expect(err).NotTo(HaveOccurred())
		Expect(states[0].Modules).To(Equal([]engine.ProfileModule{{Name: "example", From: "profile:user:mine-own", Overrides: []string{"profile:remote:go"}, Only: []string{}, Skip: []string{}}}))
	})

	It("a later remote member's entry replaces an earlier one's, and says so", func() {
		keep("go", pinned())
		keep("docs", strings.Replace(pinned(), "targets: [claude-code]", "targets: [gemini-cli]", 1))
		writeTree(p.UserProfiles, map[string]string{"mine.yml": "version: 1\nbundle: [remote:go, remote:docs]\n"})
		res, err := turn(user, engine.ProfileOn, "mine")
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Activation.Changes).To(ConsistOf(And(HaveField("Module", "example"), HaveField("From", "profile:remote:docs"),
			HaveField("Over", Equal([]string{"profile:remote:go"})))))
		Expect(res.Notices).To(ContainElement(HaveField("Text", HavePrefix("example: remote:docs overrides remote:go in bundle mine"))))
		states, _, err := e.ProfileState(ctx, []engine.Scope{user}, "mine")
		Expect(err).NotTo(HaveOccurred())
		Expect(states[0].Modules).To(Equal([]engine.ProfileModule{{Name: "example", From: "profile:remote:docs", Overrides: []string{"profile:remote:go"}, Only: []string{}, Skip: []string{}}}))
	})

	It("a bundle refuses a remote member it cannot resolve, and a remote profile that bundles", func() {
		keep("go", pinned())
		path := filepath.Join(p.UserProfiles, "mine.yml")
		writeTree(p.UserProfiles, map[string]string{"mine.yml": "version: 1\nbundle: [remote:nope]\n"})
		_, err := turn(user, engine.ProfileOn, "mine")
		Expect(err).To(MatchError(path + ":2: bundle: remote:nope is not a remote profile you have"))
		Expect(errs.Hints(err)).To(Equal([]string{"see the profiles: lola profile --user", "or take it out: lola profile --user mine --edit --bundle ''"}))
		keep("go", "version: 1\nbundle: [docs]\n")
		writeTree(p.UserProfiles, map[string]string{"mine.yml": "version: 1\nbundle: [remote:go]\n"})
		_, err = turn(user, engine.ProfileOn, "mine")
		Expect(err).To(MatchError(ContainSubstring("profile.yml:2: bundle: a remote profile cannot combine profiles; its names would resolve on each reader's machine")))
		Expect(active(user)).To(BeEmpty())
	})

	It("records a project bundle's remote member's entries in the bundle's part, and asks again when its pin changes", func() {
		keep("go", pinned())
		writeTree(proj, map[string]string{".lola/profiles/team.yml": "version: 1\nbundle: [remote:go]\n"})
		trustAll := func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }
		_, err := turn(sc, engine.ProfileOn, "team")
		var refusal *engine.TrustRefusalError
		Expect(errors.As(err, &refusal)).To(BeTrue(), "%v", err)
		Expect(refusal.Report.Changes).To(ConsistOf(And(HaveField("Module", "example"), HaveField("Profile", "project:team"),
			HaveField("Fields", ContainElement(HaveField("Field", "member"))))))
		Expect(refusal.Report.ProfileTargets).To(ConsistOf(engine.ProfileTargets{Profile: "project:team",
			FieldChange: trust.FieldChange{Field: "bundle", To: []string{"remote:go"}}}))
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOn, Name: "team"}, Trust: trustAll})
		Expect(err).NotTo(HaveOccurred())
		Expect(shipped(proj)).To(BeAnExistingFile())
		led, err := e.Ledger(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Modules["example"].From).To(Equal("profile:remote:go"), "lock.json's from is the member")
		rec, err := e.Trust.Load(proj)
		Expect(err).NotTo(HaveOccurred())
		Expect(rec.View.Part("team").Bundle).To(Equal([]string{"remote:go"}))
		Expect(rec.View.Part("team").Modules[0].Member).To(HaveValue(HaveField("Name", "remote:go")))
		res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.InSync).To(BeTrue())

		// The remote profile now has other bytes (an update): same entries, another pin.
		keep("go", pinned()+"description: updated\n")
		requests.Store(0)
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc})
		Expect(errors.As(err, &refusal)).To(BeTrue(), "%v", err)
		Expect(refusal.Report.Changes).To(ConsistOf(And(HaveField("Module", "example"), HaveField("Kind", "changed"),
			HaveField("Fields", ConsistOf(HaveField("Field", "member"))))))
		Expect(requests.Load()).To(BeZero(), "refused before any network contact")
	})

	It("asks about a remote member's entry only while it wins, and refuses its --update in a project sync", func() {
		keep("go", pinned())
		entry := "  - {name: example, source: " + url + ", commit: " + commit + ", digest: " + digest + "}\n"
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n" + entry,
			".lola/profiles/team.yml": "version: 1\nbundle: [remote:go]\n"})
		var reports []engine.TrustReport
		trustAll := func(r engine.TrustReport) (engine.TrustAnswer, error) {
			reports = append(reports, r)
			return engine.TrustAnswer{All: true}, nil
		}
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll, ConfirmRemovals: yes})
		Expect(err).NotTo(HaveOccurred())
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOn, Name: "team"},
			Trust: trustAll, ConfirmRemovals: yes})
		Expect(err).NotTo(HaveOccurred())
		Expect(reports).NotTo(BeEmpty())
		Expect(reports[len(reports)-1].Changes).To(ContainElement(And(HaveField("Module", "example"), HaveField("Profile", "project:team"),
			HaveField("Fields", ContainElement(HaveField("Field", "member"))))), "team's entry wins over project.yml's, and is asked about")
		led, err := e.Ledger(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Modules["example"].From).To(Equal("profile:remote:go"))

		// The member's entry's pin is the remote profile's, at project scope too.
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Modules: []string{"example"}, Update: true, Trust: trustAll, ConfirmRemovals: yes})
		Expect(err).To(MatchError("example: its pin is in remote:go, which only lola profile --update changes"))
		Expect(errs.Hints(err)).To(Equal([]string{"update it: lola profile --project remote:go --update"}))

		// Once team is off, project.yml's entry wins again, and nothing is asked about it: it is trusted.
		reports = nil
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOff, Name: "team"},
			Trust: trustAll, ConfirmRemovals: yes})
		Expect(err).NotTo(HaveOccurred())
		for _, r := range reports {
			Expect(r.Changes).NotTo(ContainElement(HaveField("Module", "example")))
		}
		led, err = e.Ledger(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Modules["example"].From).To(Equal(""), "project.yml's")
	})

	It("takes the profiles lock first: shared to read the remote profiles, exclusive to write them", func() {
		keep("go", pinned())
		activate(user, "remote:go")
		Expect(os.MkdirAll(filepath.Dir(p.ProfilesLock), 0o700)).To(Succeed())
		other := flock.New(p.ProfilesLock)
		Expect(other.RLock()).To(Succeed())
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: user})
		Expect(err).NotTo(HaveOccurred(), "readers share it")
		Expect(other.Unlock()).To(Succeed())

		Expect(other.Lock()).To(Succeed())
		done := make(chan error, 1)
		go func() {
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: user})
			done <- err
		}()
		dry := make(chan error, 1)
		go func() {
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: user, Options: engine.Options{DryRun: true}})
			dry <- err
		}()
		// Every other run that reads the profiles waits too: install and uninstall, and the
		// read-only listing, a profile's state and lola list.
		readers := map[string]chan error{}
		run := func(name string, f func() error) {
			ch := make(chan error, 1)
			readers[name] = ch
			go func() { ch <- f() }()
		}
		run("install", func() error {
			_, err := e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: work}}, engine.Options{})
			return err
		})
		run("uninstall", func() error {
			_, err := e.Uninstall(ctx, user, []string{"example"}, engine.Options{})
			return err
		})
		run("profiles", func() error {
			_, _, err := e.Profiles(ctx, user)
			return err
		})
		run("state", func() error {
			_, _, err := e.ProfileState(ctx, []engine.Scope{user}, "remote:go")
			return err
		})
		run("status", func() error {
			_, _, err := e.Status(ctx, user, nil)
			return err
		})
		Consistently(done, "300ms").ShouldNot(Receive(), "a writer holds it")
		Consistently(dry, "100ms").ShouldNot(Receive(), "a dry run reads the profiles under the lock too")
		for name, ch := range readers {
			Consistently(ch, "10ms").ShouldNot(Receive(), name)
		}
		Expect(other.Unlock()).To(Succeed())
		Eventually(done, "10s").Should(Receive(BeNil()))
		Eventually(dry, "10s").Should(Receive(BeNil()))
		for name, ch := range readers {
			Eventually(ch, "10s").Should(Receive(), name)
		}
	})

	Describe("--add", func() {
		var src string // the profile source's URL
		// publish makes the profile source: a git repository of files, served beside example.
		publish := func(files map[string]string) {
			GinkgoHelper()
			dir := filepath.Join(tmp, "profiles-work")
			Expect(os.RemoveAll(dir)).To(Succeed())
			writeTree(dir, files)
			gitIn(dir, "init", "-q", "-b", "main")
			gitIn(dir, "add", "-A")
			gitIn(dir, "commit", "-q", "-m", "profiles")
			bare := filepath.Join(tmp, "srv", "profiles.git")
			Expect(os.RemoveAll(bare)).To(Succeed())
			gitIn(tmp, "clone", "-q", "--bare", dir, bare)
		}
		add := func(req engine.ProfileAddRequest) (*engine.RemoteResult, error) {
			if req.Scope == nil {
				req.Scope = &user
			}
			if req.Source == "" {
				req.Source = src
			}
			return e.AddProfile(ctx, req)
		}
		accept := func(seen *[]engine.RemoteProfile) func([]engine.RemoteProfile) (bool, error) {
			return func(rps []engine.RemoteProfile) (bool, error) {
				*seen = rps
				return true, nil
			}
		}
		// kept is what lola keeps of remote profiles and stages, which a refused --add leaves as it was.
		kept := func() []string {
			GinkgoHelper()
			out := []string{}
			for _, dir := range []string{p.RemoteProfiles, p.Staging, p.Store} {
				es, err := os.ReadDir(dir)
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				Expect(err).NotTo(HaveOccurred())
				for _, d := range es {
					out = append(out, filepath.Join(dir, d.Name()))
				}
			}
			return out
		}
		BeforeEach(func() {
			src = url[:len(url)-len("example.git")] + "profiles.git"
			publish(map[string]string{"profiles/go.yml": pinned(), "profiles/docs.yml": "version: 1\ndescription: docs\n", "README.md": "hi\n"})
			requests.Store(0)
		})

		It("warns that a profile of yours shadows a remote one it adds, hinting a bundle of both", func() {
			writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
			var seen []engine.RemoteProfile
			res, err := add(engine.ProfileAddRequest{Name: "go", Consent: accept(&seen)})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
				Text: "remote:go is shadowed by user:go, which is used instead; to use both, bundled: lola profile --user go-both --switch --new --bundle user:go,remote:go"}))
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: user, ConfirmRemovals: yes, Profiles: &engine.ProfileChange{Action: engine.ProfileSwitch, New: true,
				Name: "go-both", Bundle: []string{"user:go", "remote:go"}}}) // the hint as printed
			Expect(err).NotTo(HaveOccurred())
			Expect(active(user)).To(Equal("user:go-both"))
		})

		It("records each remote profile it adds in pins.yaml, --update rewrites the record, --remove drops it, and a sync records one kept by hand", func() {
			record := func(name string) config.Remote {
				GinkgoHelper()
				o, err := profile.ReadOrigin(filepath.Join(p.RemoteProfiles, name))
				Expect(err).NotTo(HaveOccurred())
				return config.Remote{Source: o.Source, Ref: o.Ref, Path: o.Path, Commit: o.Commit, Digest: o.Digest}
			}
			remotes := func() map[string]config.Remote {
				GinkgoHelper()
				pins, err := config.LoadPins(p.Pins)
				Expect(err).NotTo(HaveOccurred())
				return pins.Remotes
			}
			stale := "stale:\n    - {source: 'https://example.com/x.git', commit: " + strings.Repeat("a", 40) + ", digest: 'sha256:" + strings.Repeat("b", 64) + "'}\n"
			Expect(os.MkdirAll(filepath.Dir(p.Pins), 0o700)).To(Succeed())
			Expect(os.WriteFile(p.Pins, []byte("version: 1\nmodules:\n  "+stale), 0o600)).To(Succeed())
			var seen []engine.RemoteProfile
			_, err := add(engine.ProfileAddRequest{Name: "go", Consent: accept(&seen)})
			Expect(err).NotTo(HaveOccurred())
			Expect(remotes()).To(Equal(map[string]config.Remote{"go": record("go")}))
			pins, err := config.LoadPins(p.Pins)
			Expect(err).NotTo(HaveOccurred())
			Expect(pins.Modules).To(HaveKey("stale"), "only remotes: changes outside an apply; modules: is kept as read")

			publish(map[string]string{"profiles/go.yml": pinned() + "description: moved\n", "profiles/docs.yml": "version: 1\n"})
			err = failed(e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Scopes: []engine.Scope{user}, Names: []string{"go"},
				Consent: func([]engine.RemoteProfile) (bool, error) { return true, nil }}))
			Expect(err).NotTo(HaveOccurred())
			Expect(remotes()).To(Equal(map[string]config.Remote{"go": record("go")}), "the record follows the update")

			_, err = e.RemoveProfile(ctx, engine.ProfileRemoveRequest{Scope: user, Name: "go", Confirm: func(engine.RemoteProfile) (bool, error) { return true, nil }})
			Expect(err).NotTo(HaveOccurred())
			Expect(remotes()).To(BeEmpty())

			keep("docs", "version: 1\n")
			writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: example, source: " + url + "}\n"})
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: user})
			Expect(err).NotTo(HaveOccurred())
			Expect(remotes()).To(Equal(map[string]config.Remote{"docs": record("docs")}), "an unrecorded remote profile is recorded by the next write")
		})

		It("says why pins.yaml did not record the profiles an --add or --update wrote, naming a pins.yaml that does not load, and what records them", func() {
			Expect(os.MkdirAll(filepath.Dir(p.Pins), 0o700)).To(Succeed())
			Expect(os.WriteFile(p.Pins, []byte("version: 1\nmodules: [oops]\n"), 0o600)).To(Succeed())
			both := func(names []string, _ int) ([]string, error) { return names, nil }
			yes := func([]engine.RemoteProfile) (bool, error) { return true, nil }
			res, err := add(engine.ProfileAddRequest{Pick: both, Consent: yes})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn, Text: "added remote:docs and remote:go, but pins.yaml does not load, " +
				"so it does not record them: " + p.Pins + ":2: modules: must be a mapping of module names, not a list → fix it by hand, then lola sync --user records them"}))

			Expect(os.WriteFile(p.Pins, []byte("version: 1\n"), 0o600)).To(Succeed())
			Expect(os.RemoveAll(p.PinsLock)).To(Succeed())
			Expect(os.MkdirAll(p.PinsLock, 0o700)).To(Succeed()) // a pins.lock that cannot be taken
			publish(map[string]string{"profiles/go.yml": pinned() + "description: moved\n", "profiles/docs.yml": "version: 1\ndescription: docs\n"})
			up, err := e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Scopes: []engine.Scope{user}, Names: []string{"go"}, Consent: yes})
			Expect(failed(up, err)).NotTo(HaveOccurred())
			Expect(up.Notices).To(ContainElement(And(HaveField("Level", engine.LevelWarn),
				HaveField("Text", And(HavePrefix("remote:go is written, but pins.yaml could not record it: "),
					HaveSuffix(" → lola sync --user records it once pins.yaml can be written"))))))
		})

		It("keeps the mode of a pins.yaml, or of the file its link reaches, when --add records a profile", func() {
			Expect(os.MkdirAll(filepath.Dir(p.Pins), 0o700)).To(Succeed())
			shared := filepath.Join(tmp, "dotfiles-pins.yaml")
			Expect(os.WriteFile(shared, []byte("version: 1\n"), 0o644)).To(Succeed())
			Expect(os.Chmod(shared, 0o644)).To(Succeed())
			modeOf := func(path string) os.FileMode {
				GinkgoHelper()
				fi, err := os.Stat(path)
				Expect(err).NotTo(HaveOccurred())
				return fi.Mode().Perm()
			}
			Expect(os.WriteFile(p.Pins, []byte("version: 1\n"), 0o644)).To(Succeed())
			Expect(os.Chmod(p.Pins, 0o644)).To(Succeed())
			var seen []engine.RemoteProfile
			_, err := add(engine.ProfileAddRequest{Name: "go", Consent: accept(&seen)})
			Expect(err).NotTo(HaveOccurred())
			Expect(modeOf(p.Pins)).To(Equal(os.FileMode(0o644)))

			Expect(os.Remove(p.Pins)).To(Succeed())
			Expect(os.Symlink(shared, p.Pins)).To(Succeed())
			_, err = add(engine.ProfileAddRequest{Name: "docs", Consent: accept(&seen)})
			Expect(err).NotTo(HaveOccurred())
			Expect(modeOf(shared)).To(Equal(os.FileMode(0o644)))
			fi, err := os.Lstat(p.Pins)
			Expect(err).NotTo(HaveOccurred())
			Expect(fi.Mode()&os.ModeSymlink).NotTo(BeZero(), "the link stays a link")
			b, err := os.ReadFile(shared)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(b)).To(ContainSubstring("docs"))
		})

		It("remoteContent renders all its entries under one run", func() {
			// Two pinned templated modules, 4 rendered bytes each for their one target: the diff of
			// the profile renders both under one budget, which 6 bytes cannot hold.
			entries := ""
			for _, name := range []string{"ta", "tb"} {
				w := filepath.Join(tmp, "work-"+name)
				writeTree(w, map[string]string{"lola.yml": "name: " + name + "\ntargets: [claude-code]\n", "commands/" + name + ".md.liquid": "xxxx"})
				gitIn(w, "init", "-q", "-b", "main")
				gitIn(w, "add", "-A")
				gitIn(w, "commit", "-q", "-m", "init")
				gitIn(tmp, "clone", "-q", "--bare", w, filepath.Join(tmp, "srv", name+".git"))
				u := url[:len(url)-len("example.git")] + name + ".git"
				other := p
				other.Store, other.StoreLock, other.Staging = filepath.Join(tmp, "pin-"+name), filepath.Join(tmp, "pin-"+name+".lock"), filepath.Join(tmp, "pin-"+name+"-staging")
				v, err := engine.New(other).Fetch(ctx, source.Spec{Kind: source.KindGit, Location: u})
				Expect(err).NotTo(HaveOccurred())
				entries += "  - name: " + name + "\n    source: " + u + "\n    commit: " + gitIn(w, "rev-parse", "HEAD") + "\n    digest: " + v.Meta.Digest + "\n"
			}
			publish(map[string]string{"profiles/two.yml": "version: 1\ntargets: [claude-code]\nmodules:\n" + entries})
			e.TemplateBudget.RunOutput = 6
			_, err := add(engine.ProfileAddRequest{Name: "two", DryRun: true})
			te, ok := errors.AsType[*engine.TemplateError](err)
			Expect(ok).To(BeTrue(), "both entries' renders count against one budget: %v", err)
			Expect(te.Code).To(Equal("template-run-budget"))
			e.TemplateBudget.RunOutput = 8
			res, err := add(engine.ProfileAddRequest{Name: "two", DryRun: true})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Profiles[0].Changes).To(HaveLen(2))
		})

		It("a profile command renders all its profiles under one run", func() {
			// Two profiles of one pinned templated module each, 4 rendered bytes for its one target:
			// one lola profile --add or --update of both renders them under one budget, which 6
			// bytes cannot hold.
			entries := map[string]string{}
			for _, name := range []string{"ta", "tb"} {
				w := filepath.Join(tmp, "work-"+name)
				writeTree(w, map[string]string{"lola.yml": "name: " + name + "\ntargets: [claude-code]\n", "commands/" + name + ".md.liquid": "xxxx"})
				gitIn(w, "init", "-q", "-b", "main")
				gitIn(w, "add", "-A")
				gitIn(w, "commit", "-q", "-m", "init")
				gitIn(tmp, "clone", "-q", "--bare", w, filepath.Join(tmp, "srv", name+".git"))
				u := url[:len(url)-len("example.git")] + name + ".git"
				other := p
				other.Store, other.StoreLock, other.Staging = filepath.Join(tmp, "pin-"+name), filepath.Join(tmp, "pin-"+name+".lock"), filepath.Join(tmp, "pin-"+name+"-staging")
				v, err := engine.New(other).Fetch(ctx, source.Spec{Kind: source.KindGit, Location: u})
				Expect(err).NotTo(HaveOccurred())
				entries[name] = "  - name: " + name + "\n    source: " + u + "\n    commit: " + gitIn(w, "rev-parse", "HEAD") + "\n    digest: " + v.Meta.Digest + "\n"
			}
			profiles := func(extra string) map[string]string {
				return map[string]string{
					"profiles/pa.yml": "version: 1\ntargets: [claude-code]\nmodules:\n" + entries["ta"] + extra,
					"profiles/pb.yml": "version: 1\ntargets: [claude-code]\nmodules:\n" + entries["tb"] + extra,
				}
			}
			both := func(names []string, _ int) ([]string, error) { return names, nil }
			yes := func([]engine.RemoteProfile) (bool, error) { return true, nil }
			publish(profiles(""))
			e.TemplateBudget.RunOutput = 6
			_, err := add(engine.ProfileAddRequest{Pick: both, DryRun: true})
			te, ok := errors.AsType[*engine.TemplateError](err)
			Expect(ok).To(BeTrue(), "both profiles' renders count against one budget: %v", err)
			Expect(te.Code).To(Equal("template-run-budget"))
			e.TemplateBudget.RunOutput = 8
			res, err := add(engine.ProfileAddRequest{Pick: both, Consent: yes})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Profiles).To(HaveLen(2))

			publish(profiles("    rules: false\n"))
			e.TemplateBudget.RunOutput = 6
			up, err := e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Scopes: []engine.Scope{user}, All: true, Consent: yes})
			Expect(err).NotTo(HaveOccurred())
			Expect(up.Updates).To(HaveExactElements(
				And(HaveField("Profile", "remote:pa"), HaveField("Outcome", engine.UpdateUpdated)),
				And(HaveField("Profile", "remote:pb"), HaveField("Outcome", engine.UpdateFailed),
					HaveField("Error", ContainSubstring("rendering templates held more than 6 bytes of rendered templates")))))
		})

		It("adds a remote profile after showing its entries and the content they bring", func() {
			var seen []engine.RemoteProfile
			res, err := add(engine.ProfileAddRequest{Name: "go", Consent: accept(&seen)})
			Expect(err).NotTo(HaveOccurred())
			Expect(seen).To(HaveLen(1))
			Expect(seen[0]).To(And(HaveField("Profile", "remote:go"), HaveField("Source", src), HaveField("Written", false)))
			Expect(seen[0].Changes).To(ConsistOf(And(HaveField("Module", "example"), HaveField("Kind", "added"),
				HaveField("Files", ContainElement(HaveField("Path", "commands/ship.md"))), HaveField("MCPServers", HaveLen(1)))))
			Expect(res.Profiles[0].Written).To(BeTrue())
			pr, o, err := profile.LoadRemote(filepath.Join(p.RemoteProfiles, "go", "profile.yml"))
			Expect(err).NotTo(HaveOccurred())
			Expect(pr.Modules).To(HaveLen(1))
			Expect(o).To(Equal(profile.Origin{Version: 1, Source: src, Commit: gitIn(filepath.Join(tmp, "profiles-work"), "rev-parse", "HEAD"),
				Digest: profile.Digest([]byte(pinned()))}))
			es, err := os.ReadDir(p.Store)
			Expect(err == nil && len(es) > 0).To(BeFalse(), "the modules it showed are not cached")

			_, err = add(engine.ProfileAddRequest{Name: "go", Consent: accept(&seen)})
			Expect(err).To(MatchError("you already have a remote profile remote:go"))
			Expect(errs.Hints(err)).To(Equal([]string{"fetch it again: lola profile --user remote:go --update"}))

			_, err = turn(user, engine.ProfileOn, "go")
			Expect(err).NotTo(HaveOccurred())
			Expect(shipped(userRoot())).To(BeAnExistingFile())
		})

		It("profile --add off a terminal without --trust refuses before network contact", func() {
			_, err := add(engine.ProfileAddRequest{Name: "go", Ref: "main"})
			Expect(err).To(MatchError("lola profile --add shows the profile and asks before adding it, and there is no terminal to ask on"))
			Expect(errs.Hints(err)).To(Equal([]string{"see it: lola profile --user go --add " + src + " --ref main -n",
				"then add it unasked: lola profile --user go --add " + src + " --ref main --trust=profiles"}))
			Expect(requests.Load()).To(BeZero())
			Expect(kept()).To(BeEmpty())

			// A dry run asks nothing and writes nothing.
			res, err := add(engine.ProfileAddRequest{Name: "go", DryRun: true})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Profiles[0].Changes).To(HaveLen(1))
			Expect(res.Profiles[0].ComparedWith).To(Equal("user scope"))
			// The scope it compares with is named, its root printed safely.
			odd := filepath.Join(tmp, "odd\x1b[2Jproj")
			Expect(os.MkdirAll(odd, 0o755)).To(Succeed())
			res, err = add(engine.ProfileAddRequest{Scope: &engine.Scope{Kind: target.Project, ProjectDir: odd}, Name: "go", DryRun: true})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Profiles[0].ComparedWith).To(HavePrefix("project " + filepath.Join(tmp, "odd")))
			Expect(res.Profiles[0].ComparedWith).NotTo(ContainSubstring("\x1b"))
			Expect(filepath.Join(p.RemoteProfiles, "go")).NotTo(BeADirectory())
		})

		It("compares with the project found from here, else user scope, and repeats a run without a scope flag", func() {
			req := engine.ProfileAddRequest{Source: src, Name: "go", DryRun: true}
			res, err := e.AddProfile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Profiles[0].ComparedWith).To(Equal("user scope"), "no project is found from here")
			Expect(res.Notices).To(ContainElement(HaveField("Text", "to add remote:go: lola profile go --add "+src+" (off a terminal: --trust=profiles)")))
			req.DryRun = false
			_, err = e.AddProfile(ctx, req)
			Expect(errs.Hints(err)).To(Equal([]string{"see it: lola profile go --add " + src + " -n",
				"then add it unasked: lola profile go --add " + src + " --trust=profiles"}), "no scope flag was given, and none is needed")

			Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
			req.Consent = accept(new([]engine.RemoteProfile))
			res, err = e.AddProfile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Profiles[0].ComparedWith).To(Equal("project " + proj))
			Expect(res.Notices).To(ContainElement(HaveField("Text", "turn it on: lola profile --project remote:go --on")),
				"turning it on is a scope's choice")
			_, err = e.AddProfile(ctx, req)
			Expect(errs.Hints(err)).To(Equal([]string{"fetch it again: lola profile remote:go --update"}))
		})

		It("offers user scope when the found project it compares with cannot be read", func() {
			Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(proj, ".lola", "lock.json"), []byte("{}\n"), 0o644)).To(Succeed())
			gitIn(proj, "init", "-q")
			gitIn(proj, "add", "-f", ".lola/lock.json")
			_, err := e.AddProfile(ctx, engine.ProfileAddRequest{Source: src, Name: "go", Consent: accept(new([]engine.RemoteProfile)),
				Rerun: []string{"--trust=profiles"}})
			Expect(err).To(MatchError(HavePrefix(".lola/lock.json is tracked by git")))
			Expect(errs.Hints(err)).To(Equal([]string{"git rm --cached .lola/lock.json",
				"or compare with user scope: lola profile --user go --add " + src}), "each hint once, the run's flags kept but --trust")
			Expect(requests.Load()).To(BeZero())
		})

		It("never lets a directory named like an entry's URL stand in for it", func() {
			// A checkout plants a folder module at <proj>/https:/lola.test:<port>/example.git, the
			// path a remote entry's URL would name if it were looked up on disk (ruling R-6b3b-5).
			planted := filepath.Join(proj, strings.Replace(url, "://", ":/", 1))
			writeTree(planted, map[string]string{"lola.yml": "name: example\n", "commands/planted.md": "Planted\n"})
			var seen []engine.RemoteProfile
			_, err := add(engine.ProfileAddRequest{Scope: &sc, Name: "go", Consent: accept(&seen)})
			Expect(err).NotTo(HaveOccurred())
			Expect(seen[0].Changes[0].Files).To(ConsistOf(HaveField("Path", "commands/ship.md"), HaveField("Path", "agents/critic.md"),
				HaveField("Path", "skills/review/SKILL.md"), HaveField("Path", "skills/review/ref/checks.md"), HaveField("Path", "mcps.json"),
				HaveField("Path", "rules/style.md")), "the consent shows what the URL serves")
			_, err = turn(sc, engine.ProfileOn, "remote:go")
			Expect(err).NotTo(HaveOccurred())
			Expect(shipped(proj)).To(BeAnExistingFile())
			Expect(filepath.Join(proj, ".claude", "commands", "planted.md")).NotTo(BeAnExistingFile(), "and so is what installs")

			// The same holds for an entry of your own profile at project scope.
			writeTree(p.UserProfiles, map[string]string{"mine.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: example, source: " + url + "}\n"})
			_, err = turn(sc, engine.ProfileSwitch, "user:mine")
			Expect(err).NotTo(HaveOccurred())
			Expect(filepath.Join(proj, ".claude", "commands", "planted.md")).NotTo(BeAnExistingFile())
		})

		It("refuses a source at an address inside your network, or over plain http, before any network contact", func() {
			GinkgoT().Chdir(tmp)
			port, _, _ := strings.Cut(strings.TrimPrefix(src, "https://lola.test:"), "/")
			for source, want := range map[string]string{
				"https://127.0.0.1:" + port + "/profiles.git":  "names the loopback address 127.0.0.1",
				"https://[::1]:" + port + "/profiles.git":      "names the loopback address ::1",
				"https://169.254.169.254/profiles.git":         "names the link-local address 169.254.169.254",
				"https://10.0.0.7/profiles.git":                "names the private address 10.0.0.7",
				"http://lola.test:" + port + "/profiles.git":   "uses http://; a remote profile's sources must use https:// or ssh://",
				"https://localhost:" + port + "/profiles.git":  "names localhost, this machine",
				"https://2130706433:" + port + "/profiles.git": "names the host 2130706433, which git and ssh read as an IP address",
			} {
				_, err := add(engine.ProfileAddRequest{Name: "go", Source: source, Consent: accept(new([]engine.RemoteProfile))})
				Expect(err).To(MatchError(ContainSubstring(want)), source)
				Expect(err).To(MatchError(HaveSuffix(" (or user@host:path), with its host name")), source)
				Expect(errs.Hints(err)).To(BeEmpty(), source)
			}
			Expect(requests.Load()).To(BeZero())
			// An entry at such an address is refused when the profile is read, before any module is fetched.
			publish(map[string]string{"profiles/go.yml": "version: 1\nmodules:\n  - {name: example, source: \"https://192.168.0.2/example.git\", commit: " +
				commit + ", digest: " + digest + "}\n"})
			_, err := add(engine.ProfileAddRequest{Name: "go", Consent: accept(new([]engine.RemoteProfile))})
			Expect(err).To(MatchError(ContainSubstring("profiles/go.yml:3: module example: https://192.168.0.2/example.git names the private address")))
			Expect(kept()).To(BeEmpty())
		})

		It("shows the whole content whatever a scope's lock.json says", func() {
			// A planted lock.json claims the module is installed at its pin (ruling R-6b3b-7).
			led := ledger.New()
			led.Modules["example"] = &ledger.Entry{Source: url, Commit: commit, Digest: digest, Targets: []string{"claude-code"}}
			Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
			Expect(led.Save(filepath.Join(proj, ".lola", "lock.json"))).To(Succeed())
			res, err := add(engine.ProfileAddRequest{Scope: &sc, Name: "go", DryRun: true})
			Expect(err).NotTo(HaveOccurred())
			c := res.Profiles[0].Changes[0]
			Expect(c.Files).To(HaveLen(6))
			Expect(c.Files).To(HaveEach(HaveField("Change", "added")))
			Expect(c.MCPServers).To(ConsistOf(HaveField("Change", "added")))
			Expect(c.FilesNote).To(Equal("this version is installed in project " + proj))
		})

		It("takes a remote: name, refuses another kind, and never a file in its place", func() {
			yes := func([]engine.RemoteProfile) (bool, error) { return true, nil }
			_, err := add(engine.ProfileAddRequest{Name: "user:go", Consent: yes})
			Expect(err).To(MatchError("--add keeps remote profiles; user:go names another kind"))
			Expect(errs.Hints(err)).To(Equal([]string{"add it as a remote profile: lola profile --user go --add " + src}))
			_, err = add(engine.ProfileAddRequest{Name: "project:go", Ref: "main", Path: "sub", DryRun: true, Rerun: []string{"-n"}})
			Expect(errs.Hints(err)).To(Equal([]string{"add it as a remote profile: lola profile --user go --add " + src + " --ref main --path sub -n"}),
				"the hint keeps the run's flags")
			for _, name := range []string{"default", "remote:default"} {
				_, err = add(engine.ProfileAddRequest{Name: name, Consent: yes})
				Expect(err).To(MatchError("a remote profile cannot be named default"), name)
			}
			for _, name := range []string{"base", "remote:base"} {
				_, err = add(engine.ProfileAddRequest{Name: name, Consent: yes})
				Expect(err).To(MatchError("a remote profile cannot be named base"), name)
				Expect(errs.Hints(err)).To(Equal([]string{"base names the scope's base: default.yml at user scope, project.yml in a project"}))
			}
			_, err = add(engine.ProfileAddRequest{Name: "remote:", Consent: yes, Pick: func([]string, int) ([]string, error) {
				Fail("remote: names no profile; it never starts the picker")
				return nil, nil
			}})
			Expect(err).To(HaveOccurred())
			Expect(requests.Load()).To(BeZero(), "all refused before any network contact")
			Expect(os.MkdirAll(p.RemoteProfiles, 0o700)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(p.RemoteProfiles, "go"), nil, 0o600)).To(Succeed())
			_, err = add(engine.ProfileAddRequest{Name: "remote:go", Consent: yes})
			Expect(err).To(MatchError(filepath.Join(p.RemoteProfiles, "go") + " is not a profile lola wrote, and is in the way of remote:go"))
			Expect(os.Remove(filepath.Join(p.RemoteProfiles, "go"))).To(Succeed())
			res, err := add(engine.ProfileAddRequest{Name: "remote:go", Consent: yes})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Profiles[0].Profile).To(Equal("remote:go"))
		})

		It("never offers a source's default.yml, which no remote profile may be", func() {
			publish(map[string]string{"profiles/go.yml": pinned(), "profiles/default.yml": "version: 1\n"})
			var offered []string
			var invalid int
			_, err := add(engine.ProfileAddRequest{DryRun: true, Pick: func(names []string, n int) ([]string, error) {
				offered, invalid = names, n
				return names, nil
			}})
			Expect(err).NotTo(HaveOccurred())
			Expect(offered).To(Equal([]string{"go"}))
			Expect(invalid).To(Equal(1))
		})

		It("asks under a shared lock, so syncs go on, and writes only what is still free", func() {
			writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\n"})
			res, err := add(engine.ProfileAddRequest{Name: "go", Consent: func([]engine.RemoteProfile) (bool, error) {
				done := make(chan error, 1)
				go func() {
					_, err := e.Sync(ctx, engine.SyncRequest{Scope: user})
					done <- err
				}()
				Eventually(done, "10s").Should(Receive(BeNil()), "a sync while you are asked does not wait")
				keep("go", pinned()) // another --add of go while you were asked
				return true, nil
			}})
			Expect(err).To(MatchError("you already have a remote profile remote:go"))
			Expect(res).To(BeNil())
		})

		It("reports what it added when it cannot check for a local profile of the name", func() {
			if runtime.GOOS == "windows" || os.Geteuid() == 0 {
				Skip("needs a directory its owner cannot read")
			}
			writeTree(p.UserProfiles, map[string]string{"docs.yml": "version: 1\n"})
			DeferCleanup(func() { Expect(os.Chmod(p.UserProfiles, 0o700)).To(Succeed()) })
			res, err := add(engine.ProfileAddRequest{Name: "go", Consent: func([]engine.RemoteProfile) (bool, error) {
				Expect(os.Chmod(p.UserProfiles, 0)).To(Succeed())
				return true, nil
			}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Profiles[0].Written).To(BeTrue())
			Expect(res.Notices).To(ContainElement(And(HaveField("Level", engine.LevelWarn),
				HaveField("Text", HavePrefix("added remote:go; lola could not check whether a local profile shadows them: ")))))
			Expect(filepath.Join(p.RemoteProfiles, "go", "profile.yml")).To(BeARegularFile())
		})

		It("writes under the exclusive lock, waiting for the runs that hold it shared", func() {
			publish(map[string]string{"profiles/go.yml": pinned()})
			Expect(os.MkdirAll(filepath.Dir(p.ProfilesLock), 0o700)).To(Succeed())
			other := flock.New(p.ProfilesLock)
			Expect(other.RLock()).To(Succeed())
			done := make(chan error, 1)
			go func() {
				_, err := add(engine.ProfileAddRequest{Name: "go", Consent: func([]engine.RemoteProfile) (bool, error) { return true, nil }})
				done <- err
			}()
			Consistently(done, "300ms").ShouldNot(Receive(), "a reader holds it")
			Expect(other.Unlock()).To(Succeed())
			Eventually(done, "10s").Should(Receive(BeNil()))
			Expect(filepath.Join(p.RemoteProfiles, "go", "profile.yml")).To(BeARegularFile())
		})

		It("says when --path is not in the source, or has no profiles/", func() {
			publish(map[string]string{"README.md": "hi\n", "team/README.md": "hi\n"})
			yes := func([]engine.RemoteProfile) (bool, error) { return true, nil }
			_, err := add(engine.ProfileAddRequest{Name: "go", Path: "nope", Consent: yes})
			Expect(err).To(MatchError("--path nope is not a directory in the source"))
			_, err = add(engine.ProfileAddRequest{Name: "go", Path: "team", Consent: yes})
			Expect(err).To(MatchError("the source has no profiles/ directory under --path team"))
		})

		It("nothing of a remote profile is kept before consent", func() {
			for _, answer := range []func([]engine.RemoteProfile) (bool, error){
				func([]engine.RemoteProfile) (bool, error) { return false, nil },
				func([]engine.RemoteProfile) (bool, error) { return false, context.Canceled }, // Ctrl+C at the question
			} {
				_, err := add(engine.ProfileAddRequest{Name: "go", Consent: answer})
				Expect(err).To(HaveOccurred())
				Expect(requests.Load()).NotTo(BeZero(), "it fetched to show the profile")
				Expect(kept()).To(BeEmpty(), "no profile, no staging, no cache")
			}
		})

		It("names the source's profiles, adding those picked, or refusing without a picker", func() {
			var seen []engine.RemoteProfile
			_, err := add(engine.ProfileAddRequest{Consent: accept(&seen)})
			Expect(err).To(MatchError("name the profile to add; the source has docs and go"))
			Expect(errs.Hints(err)).To(Equal([]string{"add docs: lola profile --user docs --add " + src + " --trust=profiles",
				"add go: lola profile --user go --add " + src + " --trust=profiles"}))
			var offered []string
			_, err = add(engine.ProfileAddRequest{Consent: accept(&seen), Pick: func(names []string, invalid int) ([]string, error) {
				offered = names
				return []string{"docs"}, nil
			}})
			Expect(err).NotTo(HaveOccurred())
			Expect(offered).To(Equal([]string{"docs", "go"}))
			Expect(seen).To(ConsistOf(HaveField("Profile", "remote:docs")))
			_, err = add(engine.ProfileAddRequest{DryRun: true})
			Expect(errs.Hints(err)).To(Equal([]string{"see docs: lola profile --user docs --add " + src + " -n",
				"see go: lola profile --user go --add " + src + " -n"}), "a look never turns into a write")
			Expect(filepath.Join(p.RemoteProfiles, "docs", "profile.yml")).To(BeAnExistingFile())
			Expect(filepath.Join(p.RemoteProfiles, "go")).NotTo(BeADirectory())
		})

		It("keeps the run's flags in every hint without a scope flag, and never an add's --trust in an update's", func() {
			yes := func([]engine.RemoteProfile) (bool, error) { return true, nil }
			rerun := []string{"--trust=profiles", "--json", "--no-pager"}
			_, err := e.AddProfile(ctx, engine.ProfileAddRequest{Source: src, Name: "user:go", Consent: yes, Rerun: rerun})
			Expect(errs.Hints(err)).To(Equal([]string{"add it as a remote profile: lola profile go --add " + src + " --trust=profiles --json --no-pager"}))
			_, err = e.AddProfile(ctx, engine.ProfileAddRequest{Source: src, Name: "go", Rerun: []string{"--json", "--no-pager"}})
			Expect(errs.Hints(err)).To(Equal([]string{"see it: lola profile go --add " + src + " -n --json --no-pager",
				"then add it unasked: lola profile go --add " + src + " --trust=profiles --json --no-pager"}))
			_, err = e.AddProfile(ctx, engine.ProfileAddRequest{Source: src, Consent: yes, Rerun: rerun})
			Expect(errs.Hints(err)).To(Equal([]string{"add docs: lola profile docs --add " + src + " --trust=profiles --json --no-pager",
				"add go: lola profile go --add " + src + " --trust=profiles --json --no-pager"}))
			dry := []string{"-n", "--json", "--no-pager"}
			_, err = e.AddProfile(ctx, engine.ProfileAddRequest{Source: src, DryRun: true, Rerun: dry})
			Expect(errs.Hints(err)).To(Equal([]string{"see docs: lola profile docs --add " + src + " -n --json --no-pager",
				"see go: lola profile go --add " + src + " -n --json --no-pager"}))
			res, err := e.AddProfile(ctx, engine.ProfileAddRequest{Source: src, Name: "go", DryRun: true, Rerun: dry})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Notices).To(ContainElement(HaveField("Text", "to add remote:go: lola profile go --add "+src+" --json --no-pager (off a terminal: --trust=profiles)")))
			res, err = e.AddProfile(ctx, engine.ProfileAddRequest{Source: src, Name: "go", DryRun: true, Trust: engine.TrustProfiles,
				Rerun: []string{"--trust=profiles", "-n", "--json"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Notices).To(ContainElement(HaveField("Text", "to add remote:go: lola profile go --add "+src+" --trust=profiles --json")),
				"a command that has --trust is not told to add it")
			Expect(requests.Load()).NotTo(BeZero())

			_, err = e.AddProfile(ctx, engine.ProfileAddRequest{Source: src, Name: "go", Consent: yes, Rerun: rerun})
			Expect(err).NotTo(HaveOccurred())
			_, err = e.AddProfile(ctx, engine.ProfileAddRequest{Source: src, Name: "go", Consent: yes, Rerun: rerun})
			Expect(err).To(MatchError("you already have a remote profile remote:go"))
			Expect(errs.Hints(err)).To(Equal([]string{"fetch it again: lola profile remote:go --update --json --no-pager"}),
				"the add's --trust answered its content, never an update's change")
		})

		It("a profile with a hostile ref or source refuses before git runs", func() {
			GinkgoT().Chdir(tmp)
			for _, req := range []engine.ProfileAddRequest{
				{Name: "go", Source: src, Ref: "--upload-pack=touch PWNED"},
				{Name: "go", Source: "ext::sh -c touch% PWNED"},
				{Name: "go", Source: "fd::0"},
				{Name: "go", Source: "git://127.0.0.1/profiles.git"},
				{Name: "go", Source: "https://tok3n@example.com/p.git"},
				{Name: "go", Source: src, Path: "../outside"},
			} {
				var seen []engine.RemoteProfile
				req.Consent = accept(&seen)
				_, err := add(req)
				Expect(err).To(HaveOccurred(), "%+v", req)
				Expect(requests.Load()).To(BeZero(), "%+v", req)
			}
			// A profile whose entry carries a hostile ref is refused when it is read, before any of
			// its modules is fetched.
			publish(map[string]string{"profiles/go.yml": "version: 1\nmodules:\n  - name: example\n    source: " + url +
				"\n    ref: --upload-pack=touch PWNED\n    commit: " + commit + "\n    digest: " + digest + "\n"})
			var seen []engine.RemoteProfile
			_, err := add(engine.ProfileAddRequest{Name: "go", Consent: accept(&seen)})
			Expect(err).To(MatchError(ContainSubstring("profiles/go.yml:3: module example: ref \"--upload-pack=touch PWNED\" starts with '-'")))
			for _, hostile := range [][2]string{{"ext::sh -c touch% PWNED", "names a git remote helper"}, {"fd::0", "names a git remote helper"},
				{"git://localhost/example.git", "a git:// source is refused"}} {
				entry, want := hostile[0], hostile[1]
				publish(map[string]string{"profiles/go.yml": "version: 1\nmodules:\n  - name: example\n    source: \"" + entry + "\"\n    commit: " +
					commit + "\n    digest: " + digest + "\n"})
				_, err := add(engine.ProfileAddRequest{Name: "go", Consent: accept(&seen)})
				Expect(err).To(MatchError(ContainSubstring(want)), entry)
			}
			Expect(filepath.Join(tmp, "PWNED")).NotTo(BeAnExistingFile())
			Expect(seen).To(BeNil())
			Expect(kept()).To(BeEmpty())
		})

		It("a remote profile with a local source or an unpinned entry refuses", func() {
			writeTree(filepath.Join(proj, "local", "profiles"), map[string]string{"go.yml": pinned()})
			for _, local := range []string{filepath.Join(proj, "local"), "./local", "file://" + filepath.Join(proj, "local")} {
				_, err := add(engine.ProfileAddRequest{Name: "go", Source: local, Consent: func([]engine.RemoteProfile) (bool, error) { return true, nil }})
				Expect(err).To(MatchError(ContainSubstring("is on this machine; a remote profile names only git repositories and https archives")), local)
			}
			publish(map[string]string{"profiles/go.yml": "version: 1\nmodules:\n  - {name: example, source: " + url + "}\n",
				"profiles/near.yml": "version: 1\nmodules:\n  - {name: example, source: ../example}\n"})
			_, err := add(engine.ProfileAddRequest{Name: "go", Consent: func([]engine.RemoteProfile) (bool, error) { return true, nil }})
			Expect(err).To(MatchError(ContainSubstring("profiles/go.yml:3: module example: it is not pinned")))
			_, err = add(engine.ProfileAddRequest{Name: "near", Consent: func([]engine.RemoteProfile) (bool, error) { return true, nil }})
			Expect(err).To(MatchError(ContainSubstring("profiles/near.yml:3: module example: ../example is on this machine")))
			Expect(kept()).To(BeEmpty())
		})

		It("a crafted profile file refuses at load", func() {
			if runtime.GOOS == "windows" {
				Skip("symbolic links need privileges on Windows")
			}
			dir := filepath.Join(tmp, "profiles-work")
			publish(map[string]string{"elsewhere/profiles/go.yml": pinned(), "profiles/docs.yml": "version: 1\n", "notes.md": "hi\n"})
			commitLinks := func(links map[string]string) {
				GinkgoHelper()
				for link, to := range links {
					Expect(os.Symlink(to, filepath.Join(dir, link))).To(Succeed())
				}
				gitIn(dir, "add", "-A")
				gitIn(dir, "commit", "-q", "-m", "links")
				gitIn(dir, "push", "-q", filepath.Join(tmp, "srv", "profiles.git"), "main")
			}
			yes := func([]engine.RemoteProfile) (bool, error) { return true, nil }
			// A link outside --path and profiles/ is never read, and allowed.
			commitLinks(map[string]string{"README.md": "notes.md", "team": "elsewhere"})
			_, err := add(engine.ProfileAddRequest{Name: "docs", Consent: yes})
			Expect(err).NotTo(HaveOccurred())
			_, err = add(engine.ProfileAddRequest{Name: "go", Path: "team", Consent: yes})
			Expect(err).To(MatchError(ContainSubstring("team: symbolic link refused")), "a link in --path")
			commitLinks(map[string]string{"profiles/go.yml": "/etc/hostname"})
			_, err = add(engine.ProfileAddRequest{Name: "go", Consent: yes})
			Expect(err).To(MatchError(ContainSubstring("profiles/go.yml: symbolic link refused: it points to /etc/hostname, an absolute path")), "an absolute link in profiles/")
			Expect(filepath.Join(p.RemoteProfiles, "go")).NotTo(BeADirectory())
		})

		It("puts back a profile an interrupted write left aside, and never one an interrupted removal left", func() {
			keep("go", pinned())
			keep("docs", "version: 1\n")
			activate(user, "remote:go")
			Expect(os.Rename(filepath.Join(p.RemoteProfiles, "go"), filepath.Join(p.RemoteProfiles, ".old-go-123"))).To(Succeed())
			Expect(os.Rename(filepath.Join(p.RemoteProfiles, "docs"), filepath.Join(p.RemoteProfiles, ".del-docs-456"))).To(Succeed())
			Expect(os.MkdirAll(filepath.Join(p.RemoteProfiles, ".new-go-789"), 0o700)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(p.RemoteProfiles, ".DS_Store"), nil, 0o600)).To(Succeed()) // not lola's: left alone
			// Any run that reads the remote profiles finishes the interrupted work first: the profile
			// that was being replaced is not read as gone.
			res, err := e.Sync(ctx, engine.SyncRequest{Scope: user})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Notices).NotTo(ContainElement(HaveField("Text", ContainSubstring("no longer exists"))))
			Expect(shipped(userRoot())).To(BeAnExistingFile())
			es, err := os.ReadDir(p.RemoteProfiles)
			Expect(err).NotTo(HaveOccurred())
			var names []string
			for _, d := range es {
				names = append(names, d.Name())
			}
			Expect(names).To(ConsistOf("go", ".DS_Store"))
		})

		Describe("restore", func() {
			var record config.Remote
			BeforeEach(func() {
				var seen []engine.RemoteProfile
				_, err := add(engine.ProfileAddRequest{Name: "go", Consent: accept(&seen)})
				Expect(err).NotTo(HaveOccurred())
				pins, err := config.LoadPins(p.Pins)
				Expect(err).NotTo(HaveOccurred())
				record = pins.Remotes["go"]
				activate(user, "remote:go")
				_, err = e.Sync(ctx, engine.SyncRequest{Scope: user, Trust: func(engine.TrustReport) (engine.TrustAnswer, error) {
					return engine.TrustAnswer{All: true}, nil
				}})
				Expect(err).NotTo(HaveOccurred())
				Expect(os.RemoveAll(filepath.Join(p.RemoteProfiles, "go"))).To(Succeed()) // a machine that has only the config dir
				// The ref serves another version now; the recorded commit stays on it.
				pw := filepath.Join(tmp, "profiles-work")
				writeTree(pw, map[string]string{"profiles/go.yml": pinned() + "description: moved on\n"})
				gitIn(pw, "commit", "-q", "-am", "moved on")
				gitIn(pw, "push", "-q", filepath.Join(tmp, "srv", "profiles.git"), "main")
				requests.Store(0)
			})

			It("restores it before a move between scopes plans, whose syncs run under the move's locks", func() {
				// A git source: a project's folder entry never moves into your files (spec 2026-10-29 §16.2).
				nw := filepath.Join(tmp, "notes-work")
				writeTree(nw, map[string]string{"lola.yml": "name: notes\n", "commands/notes.md": "N\n"})
				gitIn(nw, "init", "-q", "-b", "main")
				gitIn(nw, "add", "-A")
				gitIn(nw, "commit", "-q", "-m", "one")
				gitIn(tmp, "clone", "-q", "--bare", nw, filepath.Join(tmp, "srv", "notes.git"))
				notes := strings.TrimSuffix(url, "/example.git") + "/notes.git"
				writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + notes + "}\n"})
				all := func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }
				_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: all})
				Expect(err).NotTo(HaveOccurred())
				res, err := e.Move(ctx, engine.MoveRequest{Scope: user, Other: &sc, Modules: []string{"notes"}, To: "base", Trust: all,
					ConfirmRemovals: func([]engine.Removal) (bool, error) { return true, nil }})
				Expect(err).NotTo(HaveOccurred())
				Expect(res.Syncs[0].Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo,
					Text: "restored remote profile go from pins.yaml (" + src + " @ " + record.Commit[:12] + ")"}))
				Expect(res.Syncs[0].Notices).NotTo(ContainElement(HaveField("Text", ContainSubstring("no longer exists"))))
				Expect(shipped(userRoot())).To(BeARegularFile(), "the remote profile's module stays installed")
				Expect(filepath.Join(userRoot(), ".claude", "commands", "notes.md")).To(BeARegularFile())
			})

			It("restores the profile on at its recorded version before the sync plans", func() {
				res, err := e.Sync(ctx, engine.SyncRequest{Scope: user})
				Expect(err).NotTo(HaveOccurred())
				Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo,
					Text: "restored remote profile go from pins.yaml (" + src + " @ " + record.Commit[:12] + ")"}))
				b, err := os.ReadFile(filepath.Join(p.RemoteProfiles, "go", "profile.yml"))
				Expect(err).NotTo(HaveOccurred())
				Expect(profile.Digest(b)).To(Equal(record.Digest), "the bytes you accepted, not what the ref serves now")
				Expect(shipped(userRoot())).To(BeARegularFile())
			})

			It("refuses bytes whose digest is not the recorded one, hinting --add, and a dry run fetches nothing", func() {
				res, err := e.Sync(ctx, engine.SyncRequest{Scope: user, Options: engine.Options{DryRun: true}})
				Expect(err).NotTo(HaveOccurred())
				Expect(requests.Load()).To(BeZero(), "-n restores nothing")
				Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
					Text: "remote:go is not on this machine yet; run this again without -n to restore it from pins.yaml"}))
				Expect(filepath.Join(p.RemoteProfiles, "go")).NotTo(BeADirectory())

				pins, err := config.LoadPins(p.Pins)
				Expect(err).NotTo(HaveOccurred())
				r := pins.Remotes["go"]
				r.Digest = "sha256:" + strings.Repeat("c", 64)
				out, err := pins.Bytes(pins.Modules, map[string]config.Remote{"go": r})
				Expect(err).NotTo(HaveOccurred())
				Expect(os.WriteFile(p.Pins, out, 0o600)).To(Succeed())
				_, err = e.Sync(ctx, engine.SyncRequest{Scope: user})
				Expect(err).To(MatchError("go: the source no longer serves the recorded version"))
				Expect(errs.Hints(err)).To(Equal([]string{"review and take what it serves now: lola profile go --add " + src}))
				Expect(filepath.Join(p.RemoteProfiles, "go")).NotTo(BeADirectory())
			})

			It("checks the host rules before any network contact", func() {
				pins, err := config.LoadPins(p.Pins)
				Expect(err).NotTo(HaveOccurred())
				out, err := pins.Bytes(pins.Modules, nil)
				Expect(err).NotTo(HaveOccurred())
				hostile := strings.Replace(string(out), "version: 1\n", "version: 1\nremotes:\n  go: {source: \"http://127.0.0.1/profiles.git\", commit: "+
					record.Commit+", digest: "+record.Digest+"}\n", 1)
				Expect(os.WriteFile(p.Pins, []byte(hostile), 0o600)).To(Succeed())
				_, err = e.Sync(ctx, engine.SyncRequest{Scope: user})
				Expect(err).To(MatchError(ContainSubstring(p.Pins + ":")))
				Expect(requests.Load()).To(BeZero())
			})

			It("read-only commands fetch nothing, and the context says how to restore it", func() {
				_, _, err := e.Status(ctx, user, nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(requests.Load()).To(BeZero())
				c := e.Context(user, nil)
				Expect(c.Restore).To(Equal("lola sync --user"))
				Expect(c.Error).To(BeEmpty())
			})

			It("says what is in the way of a recorded profile it cannot restore, naming it, and plans no removal of its modules", func() {
				stray := filepath.Join(p.RemoteProfiles, "go")
				writeTree(p.RemoteProfiles, map[string]string{"go": "not a profile\n"})
				for _, dry := range []bool{true, false} {
					_, err := e.Sync(ctx, engine.SyncRequest{Scope: user, Options: engine.Options{DryRun: dry}, ConfirmRemovals: func([]engine.Removal) (bool, error) { return true, nil }})
					Expect(err).To(MatchError(stray+" is in the way of remote:go, which pins.yaml records; lola never writes over it"), "dry run: %v", dry)
					Expect(errs.Hints(err)).To(Equal([]string{"move it aside, then run it again: lola sync --user"}))
				}
				Expect(requests.Load()).To(BeZero())
				Expect(shipped(userRoot())).To(BeARegularFile(), "its modules stay installed")
				Expect(os.ReadFile(stray)).To(Equal([]byte("not a profile\n")))

				c := e.Context(user, nil)
				Expect(c.Restore).To(BeEmpty(), "no run restores it while that is there")
				Expect(c.Error).To(Equal(stray + " is in the way of it; move it aside"))
				Expect(os.Remove(stray)).To(Succeed())
				Expect(os.Symlink(tmp, stray)).To(Succeed()) // a link is in its way too: lola never follows one there
				_, err := e.Sync(ctx, engine.SyncRequest{Scope: user})
				Expect(err).To(MatchError(HavePrefix(stray + " is in the way of remote:go")))

				Expect(os.Remove(stray)).To(Succeed()) // the hint
				_, err = e.Sync(ctx, engine.SyncRequest{Scope: user})
				Expect(err).NotTo(HaveOccurred())
				Expect(filepath.Join(stray, "profile.yml")).To(BeARegularFile())
			})

			It("a fresh home given only the config dir installs the recorded versions and restores the remote profile", func() {
				for _, dir := range []string{p.StateHome, p.DataHome, p.CacheHome, filepath.Join(p.Home, ".claude")} {
					Expect(os.RemoveAll(dir)).To(Succeed())
				}
				activate(user, "remote:go") // lock.json is per machine: switch on the new one
				_, err := e.Sync(ctx, engine.SyncRequest{Scope: user})
				Expect(err).NotTo(HaveOccurred())
				b, err := os.ReadFile(filepath.Join(p.RemoteProfiles, "go", "profile.yml"))
				Expect(err).NotTo(HaveOccurred())
				Expect(profile.Digest(b)).To(Equal(record.Digest))
				Expect(shipped(userRoot())).To(BeARegularFile())
			})

			It("--switch restores the profile it names, and install the remote member of the bundle on", func() {
				restored := engine.Notice{Level: engine.LevelInfo, Text: "restored remote profile go from pins.yaml (" + src + " @ " + record.Commit[:12] + ")"}
				activate(user, "")
				res, err := turn(user, engine.ProfileSwitch, "go")
				Expect(err).NotTo(HaveOccurred())
				Expect(res.Notices).To(ContainElement(restored))
				Expect(active(user)).To(Equal("remote:go"))

				Expect(os.RemoveAll(filepath.Join(p.RemoteProfiles, "go"))).To(Succeed())
				writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\n", "work.yml": "version: 1\nbundle: [remote:go]\n"})
				activate(user, "user:work")
				out, err := e.InstallAll(ctx, engine.InstallSpec{Cwd: tmp, Modules: []engine.InstallRequest{{Arg: url}},
					Scopes:  []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: user, Source: engine.SourceFlag}}},
					Options: engine.Options{Target: engine.TargetRequest{Profile: "base"}}})
				Expect(err).NotTo(HaveOccurred())
				Expect(out.Notices).To(ContainElement(restored))
				Expect(filepath.Join(p.RemoteProfiles, "go", "profile.yml")).To(BeARegularFile())
			})

			It("--switch restores a remote member of the bundle it names", func() {
				activate(user, "")
				writeTree(p.UserProfiles, map[string]string{"work.yml": "version: 1\nbundle: [remote:go]\n"})
				res, err := turn(user, engine.ProfileSwitch, "work")
				Expect(err).NotTo(HaveOccurred())
				Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo,
					Text: "restored remote profile go from pins.yaml (" + src + " @ " + record.Commit[:12] + ")"}))
				Expect(active(user)).To(Equal("user:work"))
				Expect(shipped(userRoot())).To(BeARegularFile())
			})

			It("restores a project bundle's remote member only once the trust record covers its member list", func() {
				restored := engine.Notice{Level: engine.LevelInfo, Text: "restored remote profile go from pins.yaml (" + src + " @ " + record.Commit[:12] + ")"}
				writeTree(proj, map[string]string{".lola/profiles/team.yml": "version: 1\nbundle: [remote:go]\n"})
				activate(sc, "project:team")
				var refusal *engine.TrustRefusalError
				_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc})
				Expect(errors.As(err, &refusal)).To(BeTrue(), "%v", err)
				Expect(refusal.Report.ProfileTargets).To(ConsistOf(engine.ProfileTargets{Profile: "project:team",
					FieldChange: trust.FieldChange{Field: "bundle", To: []string{"remote:go"}}}), "the pulled member list is asked about first")
				Expect(requests.Load()).To(BeZero(), "nothing fetched before it is trusted")
				Expect(filepath.Join(p.RemoteProfiles, "go")).NotTo(BeADirectory())

				all := func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }
				res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: all})
				Expect(err).NotTo(HaveOccurred())
				Expect(res.Notices).NotTo(ContainElement(restored), "the run that trusts the list plans the member missing")
				Expect(requests.Load()).To(BeZero())
				rec, err := e.Trust.Load(proj)
				Expect(err).NotTo(HaveOccurred())
				Expect(rec.View.Part("team").Bundle).To(Equal([]string{"remote:go"}))

				res, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: all})
				Expect(err).NotTo(HaveOccurred())
				Expect(res.Notices).To(ContainElement(restored), "the next run restores it")
				Expect(shipped(proj)).To(BeARegularFile())
			})

			// gated runs the project syncs a P9 restore takes once the scope's bundle on reaches
			// remote:go through project bundles: refused before any fetch, then trusted (the member
			// still planned missing), then restored.
			gated := func() {
				GinkgoHelper()
				restored := engine.Notice{Level: engine.LevelInfo, Text: "restored remote profile go from pins.yaml (" + src + " @ " + record.Commit[:12] + ")"}
				var refusal *engine.TrustRefusalError
				_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc})
				Expect(errors.As(err, &refusal)).To(BeTrue(), "%v", err)
				Expect(refusal.Report.ProfileTargets).NotTo(BeEmpty(), "a member list is asked about")
				Expect(requests.Load()).To(BeZero(), "nothing fetched before it is trusted")
				Expect(filepath.Join(p.RemoteProfiles, "go")).NotTo(BeADirectory())
				all := func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }
				res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: all})
				Expect(err).NotTo(HaveOccurred())
				Expect(res.Notices).NotTo(ContainElement(restored))
				Expect(requests.Load()).To(BeZero())
				res, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: all})
				Expect(err).NotTo(HaveOccurred())
				Expect(res.Notices).To(ContainElement(restored))
			}

			It("restores through nested project bundles only once each one's member list is trusted", func() {
				writeTree(proj, map[string]string{".lola/profiles/team.yml": "version: 1\nbundle: [inner]\n", ".lola/profiles/inner.yml": "version: 1\nbundle: [remote:go]\n"})
				activate(sc, "project:team")
				gated()
				rec, err := e.Trust.Load(proj)
				Expect(err).NotTo(HaveOccurred())
				Expect(rec.View.Part("team").Bundle).To(Equal([]string{"inner"}))
				Expect(rec.View.Part("inner").Bundle).To(Equal([]string{"remote:go"}))
			})

			It("restores through nested project bundles while only the outer one's member list is untrusted", func() {
				writeTree(proj, map[string]string{".lola/profiles/team.yml": "version: 1\nbundle: [inner]\n", ".lola/profiles/inner.yml": "version: 1\nbundle: [remote:go]\n"})
				Expect(e.Trust.Save(proj, trust.View{}.WithPart("inner", trust.View{Bundle: []string{"remote:go"}}))).To(Succeed())
				activate(sc, "project:team")
				gated() // refuses on team's list alone, though the last bundle on the way is trusted
				rec, err := e.Trust.Load(proj)
				Expect(err).NotTo(HaveOccurred())
				Expect(rec.View.Part("team").Bundle).To(Equal([]string{"inner"}))
			})

			It("restores through a project bundle that a bundle of yours names only once its member list is trusted", func() {
				writeTree(p.UserProfiles, map[string]string{"work.yml": "version: 1\nbundle: [project:team]\n"})
				writeTree(proj, map[string]string{".lola/profiles/team.yml": "version: 1\nbundle: [remote:go]\n"})
				activate(sc, "user:work")
				gated()
			})

			It("fetches a member a pulled change adds to a trusted member list only once the new list is trusted", func() {
				writeTree(proj, map[string]string{".lola/profiles/team.yml": "version: 1\nbundle: [remote:go]\n"})
				activate(sc, "project:team")
				gated()
				pins, err := config.LoadPins(p.Pins)
				Expect(err).NotTo(HaveOccurred())
				out, err := pins.Bytes(pins.Modules, map[string]config.Remote{"go": pins.Remotes["go"], "x": pins.Remotes["go"]})
				Expect(err).NotTo(HaveOccurred())
				Expect(os.WriteFile(p.Pins, out, 0o600)).To(Succeed())
				writeTree(proj, map[string]string{".lola/profiles/team.yml": "version: 1\nbundle: [remote:go, remote:x]\n"}) // a git pull
				requests.Store(0)
				var refusal *engine.TrustRefusalError
				_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc})
				Expect(errors.As(err, &refusal)).To(BeTrue(), "%v", err)
				Expect(refusal.Report.ProfileTargets).To(ConsistOf(engine.ProfileTargets{Profile: "project:team",
					FieldChange: trust.FieldChange{Field: "bundle", From: []string{"remote:go"}, To: []string{"remote:go", "remote:x"}}}))
				Expect(requests.Load()).To(BeZero(), "x is not fetched before the trust question")
				Expect(filepath.Join(p.RemoteProfiles, "x")).NotTo(BeADirectory())
			})

			Describe("a remote member of the bundle on", func() {
				left := engine.Notice{Level: engine.LevelWarn,
					Text: "user:work's member remote:go, which is not on this machine yet; it is left out until a run that needs it restores it from pins.yaml"}
				warned := engine.Notice{Level: engine.LevelWarn, Text: "remote:go is not on this machine yet; run this again without -n to restore it from pins.yaml"}
				BeforeEach(func() {
					writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\n", "work.yml": "version: 1\nbundle: [remote:go]\n"})
					activate(user, "user:work")
				})

				It("-n plans it as missing, warning, and fetches nothing", func() {
					res, err := e.Sync(ctx, engine.SyncRequest{Scope: user, Options: engine.Options{DryRun: true}})
					Expect(err).NotTo(HaveOccurred())
					Expect(requests.Load()).To(BeZero())
					Expect(res.Notices).To(ContainElements(warned, left))
					Expect(res.Removals).To(ContainElement(HaveField("Module", "example")), "planned without go's entries")
					Expect(filepath.Join(p.RemoteProfiles, "go")).NotTo(BeADirectory())
				})

				It("names what is in its way, which a sync refuses to plan around", func() {
					stray := filepath.Join(p.RemoteProfiles, "go")
					writeTree(p.RemoteProfiles, map[string]string{"go": "x\n"})
					_, notices, err := e.Status(ctx, user, nil)
					Expect(err).NotTo(HaveOccurred())
					Expect(notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn, Text: "user:work's member remote:go, which pins.yaml records but " + stray +
						" is in the way of; it is left out until that is moved aside and a run that needs it restores it"}))
					_, err = e.Sync(ctx, engine.SyncRequest{Scope: user, Options: engine.Options{DryRun: true}})
					Expect(err).To(MatchError(HavePrefix(stray + " is in the way of remote:go")))
				})

				It("an install's preview and its dry run plan it as missing", func() {
					at := []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: user, Source: engine.SourceFlag}}}
					base := engine.Options{Target: engine.TargetRequest{Profile: "base"}}
					ch, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: tmp, Scopes: []engine.ScopeChoice{at[0].ScopeChoice},
						Modules: []engine.InstallRequest{{Arg: url}}})
					Expect(err).NotTo(HaveOccurred())
					_, err = ch.Preview(engine.InstallSpec{Scopes: at, Options: base}, []string{"example"})
					Expect(err).NotTo(HaveOccurred())
					dry := base
					dry.DryRun = true
					out, err := e.InstallAll(ctx, engine.InstallSpec{Cwd: tmp, Modules: []engine.InstallRequest{{Arg: url}}, Scopes: at, Options: dry})
					Expect(err).NotTo(HaveOccurred())
					Expect(out.Notices).To(ContainElement(warned))
					Expect(filepath.Join(p.RemoteProfiles, "go")).NotTo(BeADirectory())
				})
			})

			It("writes nothing over a profile that took its place while it fetched", func() {
				Expect(os.MkdirAll(filepath.Dir(p.ProfilesLock), 0o700)).To(Succeed())
				other := flock.New(p.ProfilesLock)
				Expect(other.RLock()).To(Succeed())
				done := make(chan error, 1)
				var res *engine.SyncResult
				go func() {
					defer GinkgoRecover()
					var err error
					res, err = e.Sync(ctx, engine.SyncRequest{Scope: user})
					done <- err
				}()
				Eventually(requests.Load, "10s").ShouldNot(BeZero(), "it fetches before it takes the lock to write")
				Consistently(done, "300ms").ShouldNot(Receive(), "a reader holds the lock")
				newer := pinned() + "description: added meanwhile\n"
				keep("go", newer) // an --add or --update that wrote while the restore fetched
				Expect(other.Unlock()).To(Succeed())
				Eventually(done, "10s").Should(Receive(BeNil()))
				b, err := os.ReadFile(filepath.Join(p.RemoteProfiles, "go", "profile.yml"))
				Expect(err).NotTo(HaveOccurred())
				Expect(string(b)).To(Equal(newer), "the restore never writes over it")
				Expect(res.Notices).NotTo(ContainElement(HaveField("Text", HavePrefix("restored remote profile"))))
			})

			It("keeps the coaching of a source it cannot reach, hinting the same run again", func() {
				Expect(os.RemoveAll(filepath.Join(tmp, "srv", "profiles.git"))).To(Succeed())
				_, err := e.Sync(ctx, engine.SyncRequest{Scope: user, Display: []string{"--plain"}})
				Expect(err).To(MatchError(HavePrefix("go: ")))
				Expect(errs.Hints(err)).To(ContainElement("run it again: lola sync --user --plain"))
				Expect(errs.Hints(err)).NotTo(ContainElement(HavePrefix("review and take")))
				Expect(filepath.Join(p.RemoteProfiles, "go")).NotTo(BeADirectory())
			})
		})
	})

	It("finds a profile an interrupted update left aside before it names one for --update or --remove", func() {
		keep("go", pinned())
		aside := func() {
			GinkgoHelper()
			Expect(os.Rename(filepath.Join(p.RemoteProfiles, "go"), filepath.Join(p.RemoteProfiles, ".old-go-123"))).To(Succeed())
		}
		aside()
		_, err := e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Scopes: []engine.Scope{user}, Names: []string{"go"}, DryRun: true})
		Expect(err).To(MatchError("you have no remote profile go"), "a dry run never sweeps, so it does not find it")
		_, err = e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Scopes: []engine.Scope{user}, Names: []string{"go"}})
		Expect(err).To(MatchError(ContainSubstring("there is no terminal to ask on")), "found, then refused before any network contact")
		Expect(filepath.Join(p.RemoteProfiles, "go")).To(BeADirectory())
		aside()
		rm, err := e.RemoveProfile(ctx, engine.ProfileRemoveRequest{Scope: user, Name: "go", Confirm: func(engine.RemoteProfile) (bool, error) { return true, nil }})
		Expect(err).NotTo(HaveOccurred())
		Expect(rm.Removed).To(BeTrue())
	})

	It("profile commands refuse a tracked lock.json", func() {
		keep("go", pinned())
		activate(sc, "remote:go")
		gitIn(proj, "init", "-q")
		gitIn(proj, "add", "-f", ".lola/lock.json")
		tracked := ".lola/lock.json is tracked by git; lola keeps it private to your checkout, since a committed lock.json lets a pull decide what lola deletes; if you never installed with lola in this checkout, untrack and delete it, then run lola sync --project"
		yes := func([]engine.RemoteProfile) (bool, error) { return true, nil }
		_, err := e.AddProfile(ctx, engine.ProfileAddRequest{Scope: &sc, Name: "docs", Source: url, Consent: yes})
		Expect(err).To(MatchError(tracked))
		_, err = e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Scopes: []engine.Scope{sc}, Names: []string{"go"}, Consent: yes})
		Expect(err).To(MatchError(tracked))
		_, err = e.RemoveProfile(ctx, engine.ProfileRemoveRequest{Scope: sc, Name: "go", Force: true,
			Confirm: func(engine.RemoteProfile) (bool, error) { return true, nil }})
		Expect(err).To(MatchError(tracked), "--force overrides the check of use, not this one")
		Expect(requests.Load()).To(BeZero())
		Expect(filepath.Join(p.RemoteProfiles, "go")).To(BeADirectory())
	})

	Describe("--update", func() {
		var src string
		work := func() string { return filepath.Join(tmp, "profiles-work") }
		publish := func(files map[string]string) {
			GinkgoHelper()
			writeTree(work(), files)
			gitIn(work(), "add", "-A")
			gitIn(work(), "commit", "-q", "-m", "change")
			gitIn(work(), "push", "-q", filepath.Join(tmp, "srv", "profiles.git"), "main")
		}
		yes := func([]engine.RemoteProfile) (bool, error) { return true, nil }
		BeforeEach(func() {
			src = url[:len(url)-len("example.git")] + "profiles.git"
			writeTree(work(), map[string]string{"profiles/go.yml": pinned()})
			gitIn(work(), "init", "-q", "-b", "main")
			gitIn(work(), "add", "-A")
			gitIn(work(), "commit", "-q", "-m", "profiles")
			gitIn(tmp, "clone", "-q", "--bare", work(), filepath.Join(tmp, "srv", "profiles.git"))
			_, err := e.AddProfile(ctx, engine.ProfileAddRequest{Scope: &user, Name: "go", Source: src, Consent: yes})
			Expect(err).NotTo(HaveOccurred())
			requests.Store(0)
		})
		update := func(s engine.Scope, name string, consent func([]engine.RemoteProfile) (bool, error)) (*engine.ProfileUpdateResult, error) {
			return e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Scopes: []engine.Scope{s}, Names: []string{name}, Consent: consent})
		}
		// applied is what a run narrowed to one scope applies there.
		applied := func(res *engine.ProfileUpdateResult) []string {
			GinkgoHelper()
			Expect(res.Scopes).To(HaveLen(1))
			return res.Scopes[0].Apply
		}
		file := func() string {
			GinkgoHelper()
			b, err := os.ReadFile(filepath.Join(p.RemoteProfiles, "go", "profile.yml"))
			Expect(err).NotTo(HaveOccurred())
			return string(b)
		}

		It("an update in a batch stages the profiles, which each scope's sync reads, and writes them only when committed", func() {
			_, err := turn(user, engine.ProfileOn, "go")
			Expect(err).NotTo(HaveOccurred())
			publish(map[string]string{"profiles/go.yml": pinned() + "    rules: false\n"})
			batch := &engine.TrustBatch{}
			res, err := e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Scopes: []engine.Scope{user}, Names: []string{"go"}, Trust: engine.TrustProfiles,
				Batch: batch})
			Expect(err).NotTo(HaveOccurred())
			Expect(batch.Items).To(Equal([]engine.Consent{{Kind: "profiles", Item: "remote:go", Change: engine.ConsentChanged}}))
			Expect(applied(res)).To(Equal([]string{"example"}), "the staged profile moves example")
			Expect(file()).To(Equal(pinned()), "nothing is written before the command's one receipt")

			scope := &engine.TrustBatch{}
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: user, Modules: applied(res), Options: engine.Options{Batch: scope}})
			Expect(err).To(MatchError(engine.ErrBatched), "the sync reads the staged profile and stops at its receipt")
			Expect(e.CommitStaged(engine.ProfileUpdateRequest{Scopes: []engine.Scope{user}}, res)).To(Succeed())
			Expect(file()).To(Equal(pinned() + "    rules: false\n"))
			res2, err := e.Sync(ctx, engine.SyncRequest{Scope: user, Modules: applied(res)})
			Expect(err).NotTo(HaveOccurred())
			Expect(res2.Changed).To(Equal([]string{"example"}))
		})

		It("commits the staged profiles with the run's hints, and names what it wrote when a later one changed", func() {
			publish(map[string]string{"profiles/go.yml": pinned(), "profiles/docs.yml": "version: 1\ndescription: docs\n"})
			_, err := e.AddProfile(ctx, engine.ProfileAddRequest{Scope: &user, Name: "docs", Source: src, Consent: yes})
			Expect(err).NotTo(HaveOccurred())
			docs := func() string {
				GinkgoHelper()
				b, err := os.ReadFile(filepath.Join(p.RemoteProfiles, "docs", "profile.yml"))
				Expect(err).NotTo(HaveOccurred())
				return string(b)
			}
			req := engine.ProfileUpdateRequest{Scopes: []engine.Scope{user}, Names: []string{"go", "docs"}, Flags: []string{"-y"},
				Trust: engine.TrustProfiles | engine.TrustHooks, Rerun: []string{"--trust=profiles,hooks", "--json"}}
			stage := func(n int) *engine.ProfileUpdateResult {
				GinkgoHelper()
				publish(map[string]string{"profiles/go.yml": pinned() + fmt.Sprintf("description: go %d\n", n),
					"profiles/docs.yml": fmt.Sprintf("version: 1\ndescription: docs %d\n", n)})
				req.Batch = &engine.TrustBatch{}
				res, err := e.UpdateProfile(ctx, req)
				Expect(err).NotTo(HaveOccurred())
				return res
			}
			// touch changes the pin a profile's source.json records, as another lola run would.
			touch := func(name string) {
				GinkgoHelper()
				o, err := profile.ReadOrigin(filepath.Join(p.RemoteProfiles, name))
				Expect(err).NotTo(HaveOccurred())
				o.Commit = strings.Repeat("0", 40)
				b, err := json.Marshal(o)
				Expect(err).NotTo(HaveOccurred())
				Expect(os.WriteFile(filepath.Join(p.RemoteProfiles, name, "source.json"), b, 0o600)).To(Succeed())
			}

			// The first profile changed since it was read: nothing was written, and the hint is the run.
			res := stage(1)
			touch("go")
			err = e.CommitStaged(req, res)
			Expect(err).To(MatchError("remote:go changed while you were asked; it was not updated"))
			_, written := errors.AsType[*engine.UpdateStoppedError](err)
			Expect(written).To(BeFalse(), "nothing was written")
			Expect(errs.Hints(err)).To(Equal([]string{"run it again: lola profile --user remote:go remote:docs --update -y --trust=profiles,hooks --json"}))
			Expect(docs()).To(Equal("version: 1\ndescription: docs\n"))

			// A later one changed: what was written is kept and named, and nothing is applied.
			res = stage(2)
			touch("docs")
			err = e.CommitStaged(req, res)
			stopped, ok := errors.AsType[*engine.UpdateStoppedError](err)
			Expect(ok).To(BeTrue(), "%v", err)
			Expect(stopped).To(MatchError("remote:docs changed while you were asked; it was not updated"))
			Expect(errs.Hints(stopped)).To(Equal([]string{"apply what was updated at user scope: lola sync --user --json",
				"run it again: lola profile --user remote:docs --update -y --trust=profiles,hooks --json"}))
			Expect(stopped.Result.Updated()).To(Equal([]string{"remote:go"}))
			Expect(stopped.Result.Updates[1]).To(And(HaveField("Profile", "remote:docs"), HaveField("Outcome", engine.UpdateSkipped)))
			Expect(stopped.Result.Scopes).To(BeEmpty(), "nothing is applied")
			Expect(file()).To(Equal(pinned() + "description: go 2\n"))
			Expect(docs()).To(Equal("version: 1\ndescription: docs\n"))
		})

		It("a staged update stopped at a refused profile hints the run of every profile it did not write, with the run's flags", func() {
			publish(map[string]string{"profiles/go.yml": pinned(), "profiles/docs.yml": "version: 1\ndescription: docs\n",
				"profiles/web.yml": "version: 1\ndescription: web\n"})
			for _, name := range []string{"docs", "web"} {
				_, err := e.AddProfile(ctx, engine.ProfileAddRequest{Scope: &user, Name: name, Source: src, Consent: yes})
				Expect(err).NotTo(HaveOccurred())
			}
			req := engine.ProfileUpdateRequest{Scopes: []engine.Scope{user}, Names: []string{"go", "docs", "web"}, Flags: []string{"-y"},
				Trust: engine.TrustProfiles, Rerun: []string{"--trust=profiles", "--plain"}}
			stage := func(n int) *engine.ProfileUpdateResult {
				GinkgoHelper()
				publish(map[string]string{"profiles/go.yml": pinned() + fmt.Sprintf("description: go %d\n", n),
					"profiles/docs.yml": fmt.Sprintf("version: 1\ndescription: docs %d\n", n), "profiles/web.yml": fmt.Sprintf("version: 1\ndescription: web %d\n", n)})
				req.Batch = &engine.TrustBatch{}
				res, err := e.UpdateProfile(ctx, req)
				Expect(err).NotTo(HaveOccurred())
				return res
			}
			touch := func(name string) {
				GinkgoHelper()
				o, err := profile.ReadOrigin(filepath.Join(p.RemoteProfiles, name))
				Expect(err).NotTo(HaveOccurred())
				o.Commit = strings.Repeat("0", 40)
				b, err := json.Marshal(o)
				Expect(err).NotTo(HaveOccurred())
				Expect(os.WriteFile(filepath.Join(p.RemoteProfiles, name, "source.json"), b, 0o600)).To(Succeed())
			}

			res := stage(1)
			touch("go")
			err := e.CommitStaged(req, res)
			Expect(err).To(MatchError("remote:go changed while you were asked; it was not updated"))
			Expect(errs.Hints(err)).To(Equal([]string{"run it again: lola profile --user remote:go remote:docs remote:web --update -y --trust=profiles --plain"}),
				"the profiles after the refused one were not written either")

			res = stage(2)
			touch("docs")
			err = e.CommitStaged(req, res)
			Expect(err).To(MatchError("remote:docs changed while you were asked; it was not updated"))
			Expect(errs.Hints(err)).To(Equal([]string{"apply what was updated at user scope: lola sync --user --plain",
				"run it again: lola profile --user remote:docs remote:web --update -y --trust=profiles --plain"}))
			stopped, _ := errors.AsType[*engine.UpdateStoppedError](err)
			Expect(stopped.Result.Updated()).To(Equal([]string{"remote:go"}))
		})

		It("commits a staged profile it wrote as written when what follows the write fails, with a warning", func() {
			if runtime.GOOS == "windows" || os.Geteuid() == 0 {
				Skip("needs a directory its owner cannot delete from")
			}
			stuck := filepath.Join(p.RemoteProfiles, "go", "stuck")
			writeTree(stuck, map[string]string{"f": ""})
			Expect(os.Chmod(stuck, 0o500)).To(Succeed())
			DeferCleanup(func() {
				left, err := filepath.Glob(filepath.Join(p.RemoteProfiles, "*", "stuck"))
				Expect(err).NotTo(HaveOccurred())
				for _, d := range left {
					Expect(os.Chmod(d, 0o700)).To(Succeed())
				}
			})
			publish(map[string]string{"profiles/go.yml": pinned() + "description: new\n"})
			req := engine.ProfileUpdateRequest{Scopes: []engine.Scope{user}, Names: []string{"go"}, Trust: engine.TrustProfiles, Batch: &engine.TrustBatch{}}
			res, err := e.UpdateProfile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(e.CommitStaged(req, res)).To(Succeed())
			Expect(res.Notices).To(ContainElement(And(HaveField("Level", engine.LevelWarn), HaveField("Text", HavePrefix("remote:go is written, but: ")))))
			Expect(file()).To(Equal(pinned() + "description: new\n"))
		})

		It("profile --add off a terminal without --trust refuses before network contact", func() {
			_, err := update(user, "go", nil)
			Expect(err).To(MatchError("lola profile --update shows how each profile changes and asks before keeping it, and there is no terminal to ask on"))
			Expect(errs.Hints(err)).To(Equal([]string{"see it: lola profile --user remote:go --update -n",
				"then update it unasked: lola profile --user remote:go --update --trust=profiles"}))
			_, err = e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Scopes: []engine.Scope{user}, Names: []string{"go"}, Flags: []string{"-y"},
				Trust: engine.TrustRuntimeHooks, Rerun: []string{"--trust=runtime-hooks"}})
			Expect(errs.Hints(err)).To(Equal([]string{"see it: lola profile --user remote:go --update -n -y --trust=runtime-hooks",
				"then update it unasked: lola profile --user remote:go --update --trust=profiles,runtime-hooks -y"}), "hints keep the run's flags")
			Expect(requests.Load()).To(BeZero())

			publish(map[string]string{"profiles/go.yml": pinned() + "description: new\n"})
			res, err := e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Scopes: []engine.Scope{user}, Names: []string{"go"}, DryRun: true, Trust: engine.TrustHooks,
				Rerun: []string{"--trust=hooks"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo,
				Text: "to update remote:go: lola profile --user remote:go --update --trust=hooks (off a terminal: --trust=profiles)"}))
			res, err = e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Scopes: []engine.Scope{user}, Names: []string{"go"}, DryRun: true,
				Trust: engine.TrustProfiles, Rerun: []string{"--trust=profiles", "-n"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo,
				Text: "to update remote:go: lola profile --user remote:go --update --trust=profiles"}), "a command that has --trust is not told to add it")
		})

		It("shows how the entries change, replaces the profile, and names what to apply where it is on", func() {
			res, err := update(user, "remote:go", yes)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Profiles).To(BeEmpty())
			Expect(res.Notices).To(ConsistOf(HaveField("Text", HavePrefix("remote:go is up to date with "+src+" (commit "))))

			_, err = turn(user, engine.ProfileOn, "go")
			Expect(err).NotTo(HaveOccurred())
			before := file()
			changed := strings.Replace(pinned(), "targets: [claude-code]", "targets: [claude-code]\ndescription: now with rules off", 1) +
				"    rules: false\n"
			publish(map[string]string{"profiles/go.yml": changed})

			// Declined: the old profile stays, and nothing is staged.
			res, err = update(user, "go", func([]engine.RemoteProfile) (bool, error) { return false, nil })
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Updates).To(ConsistOf(HaveField("Outcome", engine.UpdateDeclined)))
			Expect(res.Scopes).To(BeEmpty())
			Expect(file()).To(Equal(before))
			_, err = update(user, "go", func([]engine.RemoteProfile) (bool, error) { return false, context.Canceled })
			Expect(err).To(MatchError(context.Canceled))
			Expect(file()).To(Equal(before))
			es, _ := os.ReadDir(p.Staging)
			Expect(es).To(BeEmpty())

			var seen []engine.RemoteProfile
			res, err = update(user, "go", func(rps []engine.RemoteProfile) (bool, error) {
				seen = rps
				return true, nil
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(seen).To(HaveLen(1))
			Expect(seen[0].PreviousDigest).To(Equal(profile.Digest([]byte(before))))
			Expect(seen[0].Digest).To(Equal(profile.Digest([]byte(changed))))
			Expect(seen[0].Changes).To(ConsistOf(And(HaveField("Module", "example"), HaveField("Kind", "changed"),
				HaveField("Fields", ConsistOf(HaveField("Field", "rules"))))))
			Expect(file()).To(Equal(changed))
			Expect(applied(res)).To(Equal([]string{"example"}))
			es, err = os.ReadDir(p.RemoteProfiles)
			Expect(err).NotTo(HaveOccurred())
			Expect(es).To(HaveLen(1), "nothing left aside")

			// Fetched again with nothing new, it is up to date.
			res, err = update(sc, "go", yes)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Notices).To(ConsistOf(HaveField("Text", HavePrefix("remote:go is up to date"))))
		})

		It("a vars-only update of a remote profile asks again, showing the vars change", func() {
			kitWork := filepath.Join(tmp, "kit-work")
			writeTree(kitWork, map[string]string{"lola.yml": "name: kit\nvars:\n  language: go\n", "commands/k.md.liquid": "{{ vars.language }}\n"})
			gitIn(kitWork, "init", "-q", "-b", "main")
			gitIn(kitWork, "add", "-A")
			gitIn(kitWork, "commit", "-q", "-m", "init")
			gitIn(tmp, "clone", "-q", "--bare", kitWork, filepath.Join(tmp, "srv", "kit.git"))
			kitURL, kitCommit := url[:len(url)-len("example.git")]+"kit.git", gitIn(kitWork, "rev-parse", "HEAD")
			other := p
			other.Store, other.StoreLock, other.Staging = filepath.Join(tmp, "pin"), filepath.Join(tmp, "pin.lock"), filepath.Join(tmp, "pin-staging")
			v, err := engine.New(other).Fetch(ctx, source.Spec{Kind: source.KindGit, Location: kitURL})
			Expect(err).NotTo(HaveOccurred())
			entry := "version: 1\ntargets: [claude-code]\nmodules:\n  - name: kit\n    source: " + kitURL + "\n    commit: " + kitCommit +
				"\n    digest: " + v.Meta.Digest + "\n"
			publish(map[string]string{"profiles/go.yml": entry})
			_, err = update(user, "go", yes)
			Expect(err).NotTo(HaveOccurred())

			publish(map[string]string{"profiles/go.yml": entry + "    vars: {language: rust}\n"})
			var seen []engine.RemoteProfile
			_, err = update(user, "go", func(rps []engine.RemoteProfile) (bool, error) {
				seen = rps
				return true, nil
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(seen).To(HaveLen(1), "a vars-only edit is a change the user is asked about")
			Expect(seen[0].Changes).To(ConsistOf(And(HaveField("Module", "kit"), HaveField("Kind", "changed"),
				HaveField("Fields", ConsistOf(trust.FieldChange{Field: "vars", To: `language = "rust"`})))))
			Expect(file()).To(HaveSuffix("    vars: {language: rust}\n"))
		})

		It("an override refusal of a remote profile's entry names the remote profile's layer", func() {
			kitWork := filepath.Join(tmp, "kit-work")
			writeTree(kitWork, map[string]string{"lola.yml": "name: kit\nvars:\n  language: go\n", "commands/k.md.liquid": "{{ vars.language }}\n"})
			gitIn(kitWork, "init", "-q", "-b", "main")
			gitIn(kitWork, "add", "-A")
			gitIn(kitWork, "commit", "-q", "-m", "init")
			gitIn(tmp, "clone", "-q", "--bare", kitWork, filepath.Join(tmp, "srv", "kit.git"))
			kitURL := url[:len(url)-len("example.git")] + "kit.git"
			other := p
			other.Store, other.StoreLock, other.Staging = filepath.Join(tmp, "pin"), filepath.Join(tmp, "pin.lock"), filepath.Join(tmp, "pin-staging")
			v, err := engine.New(other).Fetch(ctx, source.Spec{Kind: source.KindGit, Location: kitURL})
			Expect(err).NotTo(HaveOccurred())
			publish(map[string]string{"profiles/go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - name: kit\n    source: " + kitURL +
				"\n    commit: " + gitIn(kitWork, "rev-parse", "HEAD") + "\n    digest: " + v.Meta.Digest + "\n    vars: {lang: rust}\n"})
			res, err := update(user, "go", yes)
			Expect(err).NotTo(HaveOccurred(), "a profile's failure is its own")
			Expect(res.Updates).To(ConsistOf(And(HaveField("Outcome", "failed"),
				HaveField("Error", "profile:remote:go: modules[kit].vars.lang: kit declares no var lang; it declares language"))),
				"not default.yml, which holds no such entry")
		})

		It("applies only what the update moves, counting what else is pending", func() {
			_, err := turn(user, engine.ProfileOn, "go")
			Expect(err).NotTo(HaveOccurred())
			mods := filepath.Join(tmp, "mods")
			writeTree(filepath.Join(mods, "a"), map[string]string{"lola.yml": "name: a\n", "commands/a.md": "A\n"})
			writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: " +
				filepath.Join(mods, "a") + "}\n"})
			publish(map[string]string{"profiles/go.yml": pinned() + "    rules: false\n"})
			res, err := update(user, "go", yes)
			Expect(err).NotTo(HaveOccurred())
			Expect(applied(res)).To(Equal([]string{"example"}), "a, pending from default.yml, is not the update's")
			Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "1 other change is pending → lola sync --user"}))

			// An entry another overrides moves nothing: in bundle mine, own's entry wins over the one
			// remote:go, the member before it, gives (spec 2026-10-29 §3.3).
			writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\n", "mine.yml": "version: 1\nbundle: [remote:go, own]\n",
				"own.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: example, source: " + url + ", commit: " + commit + ", digest: " + digest + "}\n"})
			_, err = turn(user, engine.ProfileSwitch, "mine")
			Expect(err).NotTo(HaveOccurred())
			publish(map[string]string{"profiles/go.yml": pinned() + "description: only the description\n"})
			res, err = update(user, "go", yes)
			Expect(err).NotTo(HaveOccurred())
			Expect(applied(res)).To(BeEmpty())
			Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "the update changes nothing installed at user scope"}))
		})

		It("says how to apply the update in the other scope it is used in, and when it cannot tell", func() {
			_, err := turn(user, engine.ProfileOn, "go")
			Expect(err).NotTo(HaveOccurred())
			publish(map[string]string{"profiles/go.yml": pinned() + "    rules: false\n"})
			res, err := update(sc, "go", yes)
			Expect(err).NotTo(HaveOccurred())
			Expect(applied(res)).To(BeEmpty(), "applied only in the run's scope")
			Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
				Text: "remote:go is on at user scope; apply it there: lola sync --user example"}))
			Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "remote:go is not in use in project " + proj}),
				"not that every scope picks it up at its next sync: the notice above says how to apply it")

			// From user scope, the project found from the working directory is the other scope.
			activate(sc, "remote:go")
			publish(map[string]string{"profiles/go.yml": pinned()})
			res, err = update(user, "go", yes)
			Expect(err).NotTo(HaveOccurred())
			Expect(applied(res)).To(Equal([]string{"example"}))
			Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
				Text: "remote:go is also on in project " + proj + "; apply it there: lola sync --project example"}))

			// As a member of the bundle on, too; and a scope lola cannot read is said so.
			activate(sc, "")
			activate(user, "")
			writeTree(p.UserProfiles, map[string]string{"mine.yml": "version: 1\nbundle: [remote:go]\n"})
			activate(user, "user:mine")
			publish(map[string]string{"profiles/go.yml": pinned() + "    rules: false\n"})
			res, err = update(sc, "go", yes)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
				Text: "remote:go is in bundle user:mine, at user scope; apply it there: lola sync --user example"}))
			gitIn(proj, "init", "-q")
			gitIn(proj, "add", "-f", ".lola/lock.json")
			publish(map[string]string{"profiles/go.yml": pinned()})
			res, err = update(user, "go", yes)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Notices).To(ContainElement(HaveField("Text", HavePrefix("lola could not check whether remote:go is in use in project "+proj+
				": .lola/lock.json is tracked by git"))))
		})

		It("names only the profiles the other scope uses when the run updates several, and how it uses them", func() {
			publish(map[string]string{"profiles/go.yml": pinned(), "profiles/docs.yml": "version: 1\ndescription: docs\n"})
			_, err := e.AddProfile(ctx, engine.ProfileAddRequest{Scope: &user, Name: "docs", Source: src, Consent: yes})
			Expect(err).NotTo(HaveOccurred())
			writeTree(p.UserProfiles, map[string]string{"mine.yml": "version: 1\nbundle: [remote:go]\n", "a.yml": "version: 1\nbundle: [remote:go, remote:docs]\n",
				"b.yml": "version: 1\nbundle: [remote:docs]\n"})
			n, rules := 0, "" // rules is go's entry's last rules line
			// update changes both profiles (go's entry, which the scopes install, only when entry is set), narrowed to the project,
			// and returns its notice about user scope.
			update := func(entry bool) engine.Notice {
				GinkgoHelper()
				n++
				if entry && rules == "" {
					rules = "    rules: false\n"
				} else if entry {
					rules = ""
				}
				goYml := pinned() + rules + fmt.Sprintf("description: go %d\n", n)
				publish(map[string]string{"profiles/go.yml": goYml, "profiles/docs.yml": fmt.Sprintf("version: 1\ndescription: docs %d\n", n)})
				res, err := e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Scopes: []engine.Scope{sc}, Names: []string{"go", "docs"}, Consent: yes})
				Expect(err).NotTo(HaveOccurred())
				var at []engine.Notice
				for _, nt := range res.Notices {
					if strings.Contains(nt.Text, " at user scope") {
						at = append(at, nt)
					}
				}
				Expect(at).To(HaveLen(1), "%v", res.Notices)
				return at[0]
			}
			apply := "; apply the update there: lola sync --user example"

			activate(user, "remote:go")
			Expect(update(true)).To(Equal(engine.Notice{Level: engine.LevelWarn, Text: "remote:go is on at user scope" + apply}),
				"remote:docs is off there")
			activate(user, "user:mine")
			Expect(update(true)).To(Equal(engine.Notice{Level: engine.LevelWarn,
				Text: "user:mine is on at user scope and bundles one of the updated profiles" + apply}))
			activate(user, "user:a")
			Expect(update(true)).To(Equal(engine.Notice{Level: engine.LevelWarn,
				Text: "user:a is on at user scope and bundles 2 of the updated profiles" + apply}), "named once, with how many it bundles")
			activate(user, "user:b")
			Expect(update(true)).To(Equal(engine.Notice{Level: engine.LevelInfo,
				Text: "user:b is on at user scope and bundles one of the updated profiles; the update changes nothing installed there"}),
				"remote:docs, its member, lists no module")
			activate(user, "remote:go")
			Expect(update(false)).To(Equal(engine.Notice{Level: engine.LevelInfo,
				Text: "remote:go is on at user scope; the update changes nothing installed there"}))
			activate(sc, "remote:go")
			Expect(update(true)).To(Equal(engine.Notice{Level: engine.LevelWarn, Text: "remote:go is also on at user scope" + apply}))
			activate(user, "user:mine")
			Expect(update(true)).To(Equal(engine.Notice{Level: engine.LevelWarn,
				Text: "user:mine is also on at user scope and bundles one of the updated profiles" + apply}))
			activate(user, "user:a")
			Expect(update(true)).To(Equal(engine.Notice{Level: engine.LevelWarn,
				Text: "user:a is also on at user scope and bundles 2 of the updated profiles" + apply}))
		})

		It("without a scope flag, applies in the project found from here, then user scope, and notes other checkouts", func() {
			everywhere := func(consent func([]engine.RemoteProfile) (bool, error)) (*engine.ProfileUpdateResult, error) {
				return e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Names: []string{"go"}, Consent: consent, Trust: engine.TrustHooks,
					Rerun: []string{"--trust=hooks"}})
			}
			_, err := turn(user, engine.ProfileOn, "go")
			Expect(err).NotTo(HaveOccurred())
			publish(map[string]string{"profiles/go.yml": pinned() + "    rules: false\n"})
			res, err := everywhere(yes)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Scopes).To(Equal([]engine.UpdateScope{{Scope: "user", Root: "", Apply: []string{"example"}, Of: user}}),
				"no project is found from here")
			Expect(res.Profiles[0].ComparedWith).To(Equal("user scope"))

			activate(sc, "remote:go")
			publish(map[string]string{"profiles/go.yml": pinned()})
			_, err = everywhere(nil)
			Expect(errs.Hints(err)).To(Equal([]string{"see it: lola profile remote:go --update -n --trust=hooks",
				"then update it unasked: lola profile remote:go --update --trust=profiles,hooks"}), "no scope flag was given, and none is needed")
			res, err = everywhere(yes)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Profiles[0].ComparedWith).To(Equal("project " + proj))
			Expect(res.Scopes).To(Equal([]engine.UpdateScope{
				{Scope: "project", Root: proj, Apply: []string{"example"}, Of: sc},
				{Scope: "user", Root: "", Apply: []string{"example"}, Of: user}}))
			Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo,
				Text: "every other checkout that uses remote:go picks the update up at its next lola sync"}))
			Expect(res.Notices).NotTo(ContainElement(HaveField("Text", ContainSubstring("apply it there"))), "both scopes are applied")

			// A known scope that does not use it is said so, and applies nothing.
			activate(sc, "")
			publish(map[string]string{"profiles/go.yml": pinned() + "    rules: false\n"})
			res, err = everywhere(yes)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Scopes[0].Apply).To(BeEmpty())
			Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "remote:go is not in use in project " + proj}))
		})

		It("without a scope flag, refuses before the question a known scope it cannot read, naming the others", func() {
			_, err := turn(user, engine.ProfileOn, "go")
			Expect(err).NotTo(HaveOccurred())
			writeTree(p.UserProfiles, map[string]string{"broken.yml": "version: 1\nshell: x\n"})
			activate(sc, "user:broken")
			publish(map[string]string{"profiles/go.yml": pinned() + "    rules: false\n"})
			asked := false
			_, err = e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Names: []string{"go"}, Flags: []string{"-y"}, Consent: func([]engine.RemoteProfile) (bool, error) {
				asked = true
				return true, nil
			}})
			Expect(err).To(MatchError(HavePrefix("lola cannot tell whether remote:go is in use in project " + proj + ", so it did not ask to update it: ")))
			Expect(errs.Hints(err)).To(Equal([]string{"fix it by hand",
				"or update it at user scope only: lola profile --user remote:go --update"}), "each hint once, and another scope is another question: no -y")
			Expect(asked).To(BeFalse())
			Expect(file()).To(Equal(pinned()))

			activate(sc, "")
			gitIn(proj, "init", "-q")
			gitIn(proj, "add", "-f", ".lola/lock.json")
			requests.Store(0)
			_, err = e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Names: []string{"go"}, Consent: yes, Rerun: []string{"--trust", "--json"}})
			Expect(err).To(MatchError(HavePrefix(".lola/lock.json is tracked by git")))
			Expect(errs.Hints(err)).To(Equal([]string{"git rm --cached .lola/lock.json",
				"or update it at user scope only: lola profile --user remote:go --update --json"}), "the hint keeps the run's output flags, never its --trust")
			Expect(requests.Load()).To(BeZero(), "refused before any network contact")
		})

		It("without a scope flag, never carries --trust into a hint that names the project, and says what it has not trusted", func() {
			_, err := turn(user, engine.ProfileOn, "go")
			Expect(err).NotTo(HaveOccurred())
			writeTree(p.UserProfiles, map[string]string{"broken.yml": "version: 1\nshell: x\n"})
			activate(user, "user:broken")
			writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: planted, source: " + url + "}\n"})
			activate(sc, "remote:go")
			publish(map[string]string{"profiles/go.yml": pinned() + "    rules: false\n"})
			_, err = e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Names: []string{"go"}, Consent: yes, Rerun: []string{"--trust"}})
			Expect(err).To(MatchError(And(HavePrefix("lola cannot tell whether remote:go is in use at user scope, so it did not ask to update it: "),
				HaveSuffix("; project "+proj+" has changes you have not trusted (planted and project.yml's targets), which its sync asks about: "+
					"the command for it leaves --trust out, which you add once you have reviewed them"))))
			Expect(errs.Hints(err)).To(ContainElement("or update it in project " + proj + " only: lola profile --project remote:go --update"))
			Expect(errs.Hints(err)).NotTo(ContainElement(ContainSubstring("--trust")))

			// With the project named (--scope both), the run's answers were given for both scopes
			// together: a run narrowed to one is another question, so they do not travel into it.
			_, err = e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Scopes: []engine.Scope{sc, user}, Names: []string{"go"}, Consent: yes,
				Flags: []string{"-y"}, Trust: engine.TrustProfiles | engine.TrustHooks | engine.TrustRuntimeHooks,
				Rerun: []string{"--trust=profiles,hooks,runtime-hooks", "--plain"}})
			Expect(errs.Hints(err)).To(ContainElement("or update it in project " + proj + " only: lola profile --project remote:go --update --plain"))
			Expect(err.Error()).NotTo(ContainSubstring("has changes you have not trusted"))
		})

		It("without a scope flag and no way to ask, refuses a found project with changes to trust, which --trust does not answer", func() {
			// A hostile, untracked checkout: its project.yml was never trusted, and it uses the profile.
			writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: planted, source: " + url + "}\n"})
			activate(sc, "remote:go")
			publish(map[string]string{"profiles/go.yml": pinned() + "    rules: false\n"})
			_, err := e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Names: []string{"go"}, CannotAsk: true, Consent: yes,
				Trust: engine.TrustProfiles | engine.TrustHooks, Rerun: []string{"--trust=profiles,hooks"}})
			Expect(err).To(MatchError("project " + proj + " has changes you have not trusted (planted and project.yml's targets), which its sync asks about, and no flag named it: " +
				"--trust then answers only for the remote profiles"))
			Expect(errs.Hints(err)).To(Equal([]string{"name it, and trust them: lola profile --project remote:go --update --trust=entries,profiles,hooks",
				"or with user scope too: lola profile --scope both remote:go --update --trust=entries,profiles,hooks"}))
			Expect(requests.Load()).To(BeZero(), "refused before any network contact")
			Expect(file()).To(Equal(pinned()))

			// Named, the project's trust is the run's to give; on a terminal it is asked there.
			res, err := e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Scopes: []engine.Scope{sc}, Names: []string{"go"}, CannotAsk: true, Consent: yes})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Updates).To(ConsistOf(HaveField("Outcome", engine.UpdateUpdated)))
			publish(map[string]string{"profiles/go.yml": pinned()})
			res, err = e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Names: []string{"go"}, Consent: yes})
			Expect(err).NotTo(HaveOccurred(), "a terminal asks the project's question at its sync")
			Expect(res.Scopes).To(HaveLen(2))
		})

		It("never takes your home directory for a found project", func() {
			writeTree(p.Home, map[string]string{".lola/lock.json": "{}\n"})
			e.Getwd = func() (string, error) { return p.Home, nil }
			_, err := turn(user, engine.ProfileOn, "go")
			Expect(err).NotTo(HaveOccurred())
			publish(map[string]string{"profiles/go.yml": pinned() + "    rules: false\n"})
			skipped := engine.Notice{Level: engine.LevelInfo, Text: p.Home + " has .lola/, but it is your home directory, where user scope installs; " +
				"it is not taken as a project"}
			res, err := e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Names: []string{"go"}, Consent: yes})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Scopes).To(Equal([]engine.UpdateScope{{Scope: "user", Root: "", Apply: []string{"example"}, Of: user}}))
			Expect(res.Notices).To(ContainElement(skipped))
			add, err := e.AddProfile(ctx, engine.ProfileAddRequest{Name: "go", Source: src, DryRun: true})
			Expect(err).To(MatchError("you already have a remote profile remote:go"), "%v", add)
			Expect(e.RemoveProfile(ctx, engine.ProfileRemoveRequest{Scope: user, Name: "go", Force: true,
				Confirm: func(engine.RemoteProfile) (bool, error) { return true, nil }})).Error().NotTo(HaveOccurred())
			add, err = e.AddProfile(ctx, engine.ProfileAddRequest{Name: "go", Source: src, DryRun: true})
			Expect(err).NotTo(HaveOccurred())
			Expect(add.Profiles[0].ComparedWith).To(Equal("user scope"))
			Expect(add.Notices).To(ContainElement(skipped))
		})

		It("refuses an update whose one project is your home directory, before any network contact", func() {
			writeTree(p.Home, map[string]string{".lola/lock.json": "{}\n"})
			home := engine.Scope{Kind: target.Project, ProjectDir: p.Home}
			publish(map[string]string{"profiles/go.yml": pinned() + "    rules: false\n"})
			requests.Store(0)
			refused := "project scope at " + p.Home + " is your home directory, where user scope installs"
			_, err := e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Scopes: []engine.Scope{home}, Names: []string{"go"}, Consent: yes,
				Flags: []string{"-y"}, Trust: engine.TrustProfiles | engine.TrustHooks, Rerun: []string{"--trust=profiles,hooks", "--json"}})
			Expect(err).To(MatchError(refused))
			Expect(errs.Hints(err)).To(Equal([]string{"update at user scope only: lola profile --user remote:go --update --json"}),
				"another scope is another question: no answer travels into it")
			_, err = e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Scopes: []engine.Scope{home}, All: true, DryRun: true, Rerun: []string{"-n"}})
			Expect(err).To(MatchError(refused))
			Expect(errs.Hints(err)).To(Equal([]string{"update at user scope only: lola profile --user --update --all -n"}))
			_, err = e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Scopes: []engine.Scope{home}, Consent: yes})
			Expect(err).To(MatchError(refused))
			Expect(errs.Hints(err)).To(BeEmpty(), "with no names and no --all, a hint would itself be refused")
			Expect(requests.Load()).To(BeZero())
			_, o, err := profile.LoadRemote(filepath.Join(p.RemoteProfiles, "go", "profile.yml"))
			Expect(err).NotTo(HaveOccurred())
			Expect(o.Digest).To(Equal(profile.Digest([]byte(pinned()))), "nothing was written")
		})

		It("says which profiles a run updated in one phrase", func() {
			res := &engine.ProfileUpdateResult{Updates: []engine.ProfileOutcome{{Profile: "remote:a", Outcome: engine.UpdateUpdated},
				{Profile: "remote:b", Outcome: engine.UpdateDeclined}, {Profile: "remote:c", Outcome: engine.UpdateUpdated},
				{Profile: "remote:d", Outcome: engine.UpdateUpdated}}}
			Expect(res.UpdatedText()).To(Equal("remote:a, remote:c and remote:d are updated"))
			res.Updates = res.Updates[:2]
			Expect(res.UpdatedText()).To(Equal("remote:a is updated"))
			res.Updates = res.Updates[1:]
			Expect(res.UpdatedText()).To(BeEmpty(), "none was updated")
		})

		It("refuses an engine call that names no profile and no --all", func() {
			_, err := e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Scopes: []engine.Scope{user}, Consent: yes,
				Flags: []string{"-y"}, Trust: engine.TrustAll, Rerun: []string{"--trust=entries,profiles,scripts,hooks,runtime-hooks", "--json", "--plain"}})
			Expect(err).To(MatchError("--update needs the remote profiles to update, or --all"))
			Expect(errs.Hints(err)).To(Equal([]string{"update every one: lola profile --user --update --all --json --plain"}),
				"no answer is given for profiles never named")
		})

		It("asks about each named profile on its own: a No or a failure is that profile's, and Ctrl+C stops the run", func() {
			publish(map[string]string{"profiles/go.yml": pinned(), "profiles/docs.yml": "version: 1\ndescription: docs\n"})
			_, err := e.AddProfile(ctx, engine.ProfileAddRequest{Scope: &user, Name: "docs", Source: src, Consent: yes})
			Expect(err).NotTo(HaveOccurred())
			docs := func() string {
				GinkgoHelper()
				b, err := os.ReadFile(filepath.Join(p.RemoteProfiles, "docs", "profile.yml"))
				Expect(err).NotTo(HaveOccurred())
				return string(b)
			}
			publish(map[string]string{"profiles/go.yml": pinned() + "description: new\n", "profiles/docs.yml": "version: 1\ndescription: docs 2\n"})
			var asked []string
			// answers answers each question in turn: nil says yes, context.Canceled is Ctrl+C, and
			// any other error says No.
			answers := func(say ...error) func([]engine.RemoteProfile) (bool, error) {
				return func(rps []engine.RemoteProfile) (bool, error) {
					asked = append(asked, rps[0].Profile)
					switch a := say[len(asked)-1]; {
					case a == nil:
						return true, nil
					case errors.Is(a, context.Canceled):
						return false, a
					}
					return false, nil
				}
			}
			no := errors.New("no")
			res, err := e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Scopes: []engine.Scope{user}, Names: []string{"go", "remote:docs", "remote:go"}, Consent: answers(no, nil)})
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(Equal([]string{"remote:go", "remote:docs"}), "one question each, in order; a name given twice is one")
			Expect(res.Updates).To(HaveExactElements(
				And(HaveField("Profile", "remote:go"), HaveField("Outcome", engine.UpdateDeclined)),
				And(HaveField("Profile", "remote:docs"), HaveField("Outcome", engine.UpdateUpdated), HaveField("Hints", BeEmpty()))))
			Expect(file()).To(Equal(pinned()), "declined, it is kept as it was")
			Expect(docs()).To(Equal("version: 1\ndescription: docs 2\n"))

			// --all is every remote profile you have, by name; a failure is its outcome, and the run goes on.
			o, err := profile.ReadOrigin(filepath.Join(p.RemoteProfiles, "docs"))
			Expect(err).NotTo(HaveOccurred())
			o.Ref = "--upload-pack=touch PWNED"
			b, err := json.Marshal(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(filepath.Join(p.RemoteProfiles, "docs", "source.json"), b, 0o600)).To(Succeed())
			_, err = e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Scopes: []engine.Scope{user}, All: true, Flags: []string{"-y"}})
			Expect(errs.Hints(err)).To(Equal([]string{"see it: lola profile --user --update --all -n -y",
				"then update it unasked: lola profile --user --update --all --trust=profiles -y"}))
			asked = nil
			res, err = e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Scopes: []engine.Scope{user}, All: true, Consent: answers(nil)})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Updates).To(HaveExactElements(
				And(HaveField("Profile", "remote:docs"), HaveField("Outcome", engine.UpdateFailed), HaveField("Error", ContainSubstring("starts with '-'")),
					HaveField("Hints", ContainElement("remove it: lola profile --user remote:docs --remove --force"))),
				And(HaveField("Profile", "remote:go"), HaveField("Outcome", engine.UpdateUpdated))))
			Expect(asked).To(Equal([]string{"remote:go"}))
			Expect(filepath.Join(tmp, "PWNED")).NotTo(BeAnExistingFile())

			// Ctrl+C at a question stops the run: what it updated is kept, and nothing after is asked.
			o.Ref = ""
			b, err = json.Marshal(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(filepath.Join(p.RemoteProfiles, "docs", "source.json"), b, 0o600)).To(Succeed())
			publish(map[string]string{"profiles/go.yml": pinned(), "profiles/docs.yml": "version: 1\ndescription: docs 3\n"})
			asked = nil
			_, err = e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Scopes: []engine.Scope{user}, All: true, Consent: answers(context.Canceled)})
			Expect(err).To(MatchError(context.Canceled), "nothing was updated")
			asked = nil
			_, err = e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Scopes: []engine.Scope{user}, All: true, Consent: answers(nil, context.Canceled)})
			stopped, ok := errors.AsType[*engine.UpdateStoppedError](err)
			Expect(ok).To(BeTrue(), "%v", err)
			Expect(stopped).To(MatchError(context.Canceled))
			Expect(stopped.Result.Updated()).To(Equal([]string{"remote:docs"}))
			Expect(stopped.Result.Updates[1]).To(And(HaveField("Profile", "remote:go"), HaveField("Outcome", engine.UpdateSkipped)))
			Expect(docs()).To(Equal("version: 1\ndescription: docs 3\n"))
			Expect(file()).To(Equal(pinned() + "description: new\n"))

			// --all without remote profiles says so, and contacts nothing.
			Expect(os.RemoveAll(p.RemoteProfiles)).To(Succeed())
			requests.Store(0)
			res, err = e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Scopes: []engine.Scope{user}, All: true})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Notices).To(Equal([]engine.Notice{{Level: engine.LevelInfo, Text: "you have no remote profiles to update"}}))
			Expect(requests.Load()).To(BeZero())
		})

		It("says when it cannot read a scope once the profile is written, naming how to apply it in each", func() {
			_, err := turn(user, engine.ProfileOn, "go")
			Expect(err).NotTo(HaveOccurred())
			activate(sc, "remote:go")
			publish(map[string]string{"profiles/go.yml": pinned() + "    rules: false\n"})
			_, err = e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Names: []string{"go"}, Trust: engine.TrustHooks, Rerun: []string{"--trust=hooks"},
				Consent: func([]engine.RemoteProfile) (bool, error) {
					writeTree(p.UserProfiles, map[string]string{"broken.yml": "version: 1\nshell: x\n"})
					activate(user, "user:broken")
					return true, nil
				}})
			Expect(err).To(MatchError(HavePrefix("lola could not tell whether remote:go is in use at user scope: ")))
			Expect(errs.Hints(err)).To(Equal([]string{"fix it by hand",
				"apply it in project " + proj + " once that is fixed: lola sync --project",
				"apply it at user scope once that is fixed: lola sync --user"}), "a whole scope's sync carries none of the run's answers")
			stopped, ok := errors.AsType[*engine.UpdateStoppedError](err)
			Expect(ok).To(BeTrue())
			Expect(stopped.Result.Updates).To(ConsistOf(HaveField("Outcome", engine.UpdateUpdated)), "every profile's outcome is kept")
			Expect(stopped.Result.Scopes).To(BeEmpty(), "nothing is applied anywhere")
			Expect(file()).To(Equal(pinned() + "    rules: false\n"))
		})

		It("a remote profile with a local source or an unpinned entry refuses", func() {
			before := file()
			for content, want := range map[string]string{
				"version: 1\nmodules:\n  - {name: example, source: " + url + "}\n":                                                                ":3: module example: it is not pinned",
				"version: 1\nmodules:\n  - {name: example, source: ../example}\n":                                                                 ":3: module example: ../example is on this machine",
				"version: 1\nmodules:\n  - {name: example, source: \"http://localhost/x.git\", commit: " + commit + ", digest: " + digest + "}\n": "uses http://",
			} {
				publish(map[string]string{"profiles/go.yml": content})
				err := failed(update(user, "go", yes))
				Expect(err).To(MatchError(ContainSubstring(want)), content)
				Expect(file()).To(Equal(before))
			}
		})

		It("refuses before the question when it cannot read the scope it would apply to, and keeps the profile it showed", func() {
			_, err := turn(user, engine.ProfileOn, "go")
			Expect(err).NotTo(HaveOccurred())
			writeTree(p.UserProfiles, map[string]string{"broken.yml": "version: 1\nshell: x\n"})
			activate(user, "user:broken")
			publish(map[string]string{"profiles/go.yml": pinned() + "    rules: false\n"})
			asked := false
			_, err = update(user, "go", func([]engine.RemoteProfile) (bool, error) {
				asked = true
				return true, nil
			})
			Expect(err).To(MatchError(HavePrefix("lola cannot tell whether remote:go is in use at user scope, so it did not ask to update it: ")))
			Expect(asked).To(BeFalse())
			Expect(file()).To(Equal(pinned()))

			// A profile changed while you were asked is not replaced; the hint keeps the run's flags.
			activate(user, "remote:go")
			err = failed(e.UpdateProfile(ctx, engine.ProfileUpdateRequest{Scopes: []engine.Scope{user}, Names: []string{"go"}, Flags: []string{"-y"}, Trust: engine.TrustHooks,
				Rerun: []string{"--trust=hooks"}, Consent: func([]engine.RemoteProfile) (bool, error) {
					keep("go", pinned()+"description: another update\n")
					return true, nil
				}}))
			Expect(err).To(MatchError("remote:go changed while you were asked; it was not updated"))
			Expect(errs.Hints(err)).To(Equal([]string{"run it again: lola profile --user remote:go --update -y --trust=hooks"}))
			Expect(file()).To(Equal(pinned() + "description: another update\n"))
		})

		It("reports a profile it wrote as written when what follows the write fails", func() {
			if runtime.GOOS == "windows" || os.Geteuid() == 0 {
				Skip("needs a directory its owner cannot delete from")
			}
			stuck := filepath.Join(p.RemoteProfiles, "go", "stuck")
			writeTree(stuck, map[string]string{"f": ""})
			Expect(os.Chmod(stuck, 0o500)).To(Succeed())
			DeferCleanup(func() {
				left, err := filepath.Glob(filepath.Join(p.RemoteProfiles, "*", "stuck"))
				Expect(err).NotTo(HaveOccurred())
				for _, d := range left {
					Expect(os.Chmod(d, 0o700)).To(Succeed())
				}
			})
			publish(map[string]string{"profiles/go.yml": pinned() + "description: new\n"})
			res, err := update(user, "go", yes)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Profiles[0].Written).To(BeTrue())
			Expect(res.Notices).To(ContainElement(And(HaveField("Level", engine.LevelWarn), HaveField("Text", HavePrefix("remote:go is written, but: ")))))
			Expect(file()).To(Equal(pinned() + "description: new\n"))
		})

		It("names nothing to apply where the profile is not used, and refuses a profile of another kind or none", func() {
			publish(map[string]string{"profiles/go.yml": pinned() + "description: new\n"})
			res, err := update(sc, "go", yes)
			Expect(err).NotTo(HaveOccurred())
			Expect(applied(res)).To(BeEmpty())
			Expect(res.Notices).To(ContainElement(HaveField("Text",
				"remote:go is not in use in project "+proj+"; every scope that uses it picks the update up at its next lola sync")))

			_, err = update(user, "user:go", yes)
			Expect(err).To(MatchError("--update acts on remote profiles; user:go is not one"))
			Expect(errs.Hints(err)).To(Equal([]string{"edit " + filepath.Join(p.UserProfiles, "go.yml") + " by hand: lola updates only remote profiles",
				"see the remote ones: lola profile --user"}))
			_, err = update(user, "docs", yes)
			Expect(err).To(MatchError("you have no remote profile docs"))
			Expect(errs.Hints(err)).To(Equal([]string{"--update remote:go: lola profile --user remote:go --update"}))
		})

		It("a profile with a hostile ref or source refuses before git runs", func() {
			// source.json is lola's, but a tampered one is refused before anything is fetched.
			o, err := profile.ReadOrigin(filepath.Join(p.RemoteProfiles, "go"))
			Expect(err).NotTo(HaveOccurred())
			o.Ref = "--upload-pack=touch PWNED"
			b, err := json.Marshal(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(filepath.Join(p.RemoteProfiles, "go", "source.json"), b, 0o600)).To(Succeed())
			err = failed(update(user, "go", yes))
			Expect(err).To(MatchError(ContainSubstring("starts with '-'")))
			Expect(requests.Load()).To(BeZero())
			Expect(filepath.Join(tmp, "PWNED")).NotTo(BeAnExistingFile())
		})
	})

	Describe("--remove", func() {
		asked := 0
		yes := func(engine.RemoteProfile) (bool, error) {
			asked++
			return true, nil
		}
		remove := func(s engine.Scope, name string, force bool, confirm func(engine.RemoteProfile) (bool, error)) (*engine.ProfileRemoveResult, error) {
			return e.RemoveProfile(ctx, engine.ProfileRemoveRequest{Scope: s, Name: name, Force: force, Confirm: confirm})
		}
		BeforeEach(func() { asked = 0 })

		DescribeTable("a hint that runs another command keeps --json, --plain, --no-pager and the scope flags, repeats the flags the user gave, and adds a consent flag or --force only when it answers its own refusal",
			func(display []string, want func() []string) {
				keep("go", pinned())
				activate(sc, "remote:go")
				_, err := e.RemoveProfile(ctx, engine.ProfileRemoveRequest{Scope: user, Name: "go", Confirm: yes, Display: display})
				Expect(err).To(MatchError("remote:go is on in project " + proj))
				Expect(errs.Hints(err)).To(Equal(want()))
			},
			Entry("remote profile --remove of one that is on, under --json", []string{"--json"}, func() []string {
				return []string{"turn the profile off first: lola profile --project remote:go --off --json",
					"or remove it anyway: lola profile --user remote:go --remove --force --json"}
			}),
		)

		It("removes a remote profile after asking, refused while it is in use unless --force", func() {
			keep("go", pinned())
			_, err := turn(user, engine.ProfileOn, "go")
			Expect(err).NotTo(HaveOccurred())
			for _, s := range []engine.Scope{user, sc} {
				_, err = remove(s, "go", false, yes)
				Expect(err).To(MatchError("remote:go is on at user scope"), "user scope is checked from a project too")
				Expect(errs.Hints(err)).To(HaveExactElements("turn the profile off first: lola profile --user remote:go --off",
					And(HavePrefix("or remove it anyway: lola profile "), HaveSuffix(" remote:go --remove --force"))))
			}
			Expect(asked).To(BeZero())
			// --force answers the question too, off a terminal and on one (user decision, 2026-10-22).
			res, err := remove(user, "go", true, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(BeZero(), "nothing was asked")
			Expect(res.Removed).To(BeTrue())
			Expect(res.Notices).To(ConsistOf(engine.Notice{Level: engine.LevelWarn,
				Text: "remote:go is on at user scope; its modules stay installed there until the next lola sync --user, which removes them"}))
			es, err := os.ReadDir(p.RemoteProfiles)
			Expect(err).NotTo(HaveOccurred())
			Expect(es).To(BeEmpty())
			Expect(shipped(userRoot())).To(BeAnExistingFile())

			sres, err := e.Sync(ctx, engine.SyncRequest{Scope: user, ConfirmRemovals: func([]engine.Removal) (bool, error) { return true, nil }})
			Expect(err).NotTo(HaveOccurred())
			Expect(sres.Notices).To(ContainElement(HaveField("Text", "profile remote:go is on but no longer exists → lola profile --user remote:go --off")))
			Expect(shipped(userRoot())).NotTo(BeAnExistingFile())
		})

		It("checks the project found from the working directory too, naming the scope that uses it", func() {
			keep("go", pinned())
			activate(sc, "remote:go") // proj, found from the working directory
			_, err := remove(user, "go", false, yes)
			Expect(err).To(MatchError("remote:go is on in project " + proj))
			Expect(errs.Hints(err)).To(Equal([]string{"turn the profile off first: lola profile --project remote:go --off",
				"or remove it anyway: lola profile --user remote:go --remove --force"}))
			Expect(asked).To(BeZero())

			// Another project is not found from here: lola keeps no list of every checkout.
			e.Getwd = func() (string, error) { return tmp, nil }
			res, err := remove(user, "go", false, yes)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Removed).To(BeTrue())
		})

		It("keeps --force in its dry run's command, and under it warns of a scope it cannot read", func() {
			keep("go", pinned())
			activate(user, "remote:go")
			res, err := e.RemoveProfile(ctx, engine.ProfileRemoveRequest{Scope: user, Name: "go", Force: true, DryRun: true})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo,
				Text: "to remove it: lola profile --user remote:go --remove --force"}), "--force answers the question: no -y off a terminal")

			// The project found from here has a tracked lock.json: it cannot be read.
			activate(user, "")
			activate(sc, "")
			gitIn(proj, "init", "-q")
			gitIn(proj, "add", "-f", ".lola/lock.json")
			_, err = remove(user, "go", false, yes)
			Expect(err).To(MatchError(HavePrefix("lola cannot tell whether remote:go is in use in project " + proj + ": .lola/lock.json is tracked by git")))
			Expect(errs.Hints(err)).To(HaveExactElements(HavePrefix("git rm --cached"),
				"or remove it anyway: lola profile --user remote:go --remove --force"))
			res, err = remove(user, "go", true, yes)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Removed).To(BeTrue())
			Expect(res.Notices).To(ContainElement(HaveField("Text", HavePrefix("could not check whether remote:go is in use in project "+proj))))
		})

		It("carries -y only in the hint that asks for it, keeps -n, and refuses a profile that changed while you were asked", func() {
			keep("go", pinned())
			activate(user, "remote:go")
			_, err := e.RemoveProfile(ctx, engine.ProfileRemoveRequest{Scope: user, Name: "go", Yes: true, Confirm: yes})
			Expect(err).To(MatchError("remote:go is on at user scope"))
			Expect(errs.Hints(err)).To(Equal([]string{"turn the profile off first: lola profile --user remote:go --off",
				"or remove it anyway: lola profile --user remote:go --remove --force"}), "a -y answers only the question it was given for")
			_, err = e.RemoveProfile(ctx, engine.ProfileRemoveRequest{Scope: user, Name: "go", Yes: true, DryRun: true})
			Expect(errs.Hints(err)).To(ContainElement("or remove it anyway: lola profile --user remote:go --remove --force -n"))
			_, err = e.RemoveProfile(ctx, engine.ProfileRemoveRequest{Scope: user, Name: "go", Yes: true})
			Expect(err).To(MatchError("remote:go is on at user scope"), "off a terminal, in use is said before -y is offered")
			Expect(errs.Hints(err)).To(Equal([]string{"turn the profile off first: lola profile --user remote:go --off",
				"or remove it anyway: lola profile --user remote:go --remove --force"}), "no -y hint that then refuses")
			activate(user, "")
			_, err = e.RemoveProfile(ctx, engine.ProfileRemoveRequest{Scope: user, Name: "go", Yes: true})
			Expect(errs.Hints(err)).To(Equal([]string{"confirm it: lola profile --user remote:go --remove -y"}), "-y once, once it would remove it")
			res, err := e.RemoveProfile(ctx, engine.ProfileRemoveRequest{Scope: user, Name: "go", Yes: true, DryRun: true})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "to remove it: lola profile --user remote:go --remove (off a terminal: -y)"}))

			_, err = e.RemoveProfile(ctx, engine.ProfileRemoveRequest{Scope: user, Name: "go", Confirm: func(engine.RemoteProfile) (bool, error) {
				keep("go", pinned()+"description: replaced\n")
				return true, nil
			}})
			Expect(err).To(MatchError("remote:go changed while you were asked; nothing was removed"))
			Expect(errs.Hints(err)).To(Equal([]string{"run it again: lola profile --user remote:go --remove"}))
			Expect(filepath.Join(p.RemoteProfiles, "go", "profile.yml")).To(BeAnExistingFile())
		})

		It("reports a profile it removed but could not delete as removed, with a warning", func() {
			if runtime.GOOS == "windows" || os.Geteuid() == 0 {
				Skip("needs a directory its owner cannot delete from")
			}
			keep("go", pinned())
			stuck := filepath.Join(p.RemoteProfiles, "go", "stuck")
			writeTree(stuck, map[string]string{"f": ""})
			Expect(os.Chmod(stuck, 0o500)).To(Succeed())
			DeferCleanup(func() {
				left, err := filepath.Glob(filepath.Join(p.RemoteProfiles, "*", "stuck"))
				Expect(err).NotTo(HaveOccurred())
				for _, d := range left {
					Expect(os.Chmod(d, 0o700)).To(Succeed())
				}
			})
			res, err := remove(user, "go", false, yes)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Removed).To(BeTrue())
			Expect(res.Notices).To(ContainElement(And(HaveField("Level", engine.LevelWarn), HaveField("Text",
				And(HavePrefix("removed remote:go, but could not delete "+filepath.Join(p.RemoteProfiles, ".del-go-")),
					HaveSuffix("; the next lola run removes it"))))))
			left, err := filepath.Glob(filepath.Join(p.RemoteProfiles, ".del-go-*", "stuck"))
			Expect(err).NotTo(HaveOccurred())
			Expect(left).To(HaveLen(1))
			Expect(os.Chmod(left[0], 0o700)).To(Succeed())
			writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\n"})
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: user})
			Expect(err).NotTo(HaveOccurred())
			Expect(os.ReadDir(p.RemoteProfiles)).To(BeEmpty(), "the next run removed it")
		})

		It("names every bundle file that lists a member it removes, nested ones too, and the sync that then refuses says how to fix it", func() {
			keep("go", pinned())
			mine, team := filepath.Join(p.UserProfiles, "mine.yml"), filepath.Join(p.UserProfiles, "team.yml")
			writeTree(p.UserProfiles, map[string]string{"mine.yml": "version: 1\nbundle: [team, go]\n", "team.yml": "version: 1\nbundle: [remote:go]\n"})
			activate(user, "user:mine")
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: user, ConfirmRemovals: func([]engine.Removal) (bool, error) { return true, nil }})
			Expect(err).NotTo(HaveOccurred())
			_, err = remove(user, "go", false, yes)
			Expect(err).To(MatchError("remote:go is in bundle user:mine, on at user scope"))
			Expect(errs.Hints(err)).To(Equal([]string{"turn the bundle off first: lola profile --user user:mine --off",
				"or take remote:go out of bundle team: lola profile --user team --edit --bundle ''",
				"or take go out of bundle mine: lola profile --user mine --edit --bundle team",
				"or remove it anyway: lola profile --user remote:go --remove --force"}))

			res, err := remove(user, "go", true, yes)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn, Text: "remote:go is in bundle user:mine, on at user scope; " +
				"its modules stay installed there, and lola sync --user refuses bundle user:mine until you take remote:go out of bundle team: " +
				"lola profile --user team --edit --bundle ''; and take go out of bundle mine: lola profile --user mine --edit --bundle team"}))
			Expect(shipped(userRoot())).To(BeAnExistingFile())
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: user, ConfirmRemovals: func([]engine.Removal) (bool, error) { return true, nil }})
			Expect(err).To(MatchError(team + ":2: bundle: remote:go is not a remote profile you have"))
			Expect(errs.Hints(err)).To(Equal([]string{"see the profiles: lola profile --user", "or take it out: lola profile --user team --edit --bundle ''"}))

			// --off mends it: the bundle goes off, each broken member named with its take-out.
			removals := func([]engine.Removal) (bool, error) { return true, nil }
			offRes, err := e.Sync(ctx, engine.SyncRequest{Scope: user, Profiles: &engine.ProfileChange{Action: engine.ProfileOff}, ConfirmRemovals: removals,
				Options: engine.Options{DryRun: true}})
			Expect(err).NotTo(HaveOccurred(), "lola profile --user --off -n works")
			Expect(offRes.Notices).To(ContainElements(
				engine.Notice{Level: engine.LevelWarn, Text: team + ":2: bundle: remote:go is not a remote profile you have; it is left out until you " +
					"take it out: lola profile --user team --edit --bundle ''"},
				engine.Notice{Level: engine.LevelWarn, Text: mine + ":2: bundle: go is not a profile of yours or a remote profile you have; it is left out until you " +
					"take it out: lola profile --user mine --edit --bundle team"}))

			// The edits, as the notice printed them, in its order: each succeeds.
			res2, err := e.EditBundle(ctx, engine.BundleEditRequest{Scope: user, Name: "team", Members: []string{}, ConfirmRemovals: removals})
			Expect(err).NotTo(HaveOccurred(), "team's take-out is not refused for mine's broken member")
			Expect(res2.Sync).NotTo(BeNil())
			Expect(res2.Sync.Notices).To(ContainElement(HaveField("Text", HavePrefix(mine+":2: bundle: go is not a profile of yours"))), "mine's member is named, not refused")
			Expect(os.ReadFile(team)).To(Equal([]byte("version: 1\nbundle: []\n")))
			_, err = e.EditBundle(ctx, engine.BundleEditRequest{Scope: user, Name: "mine", Members: []string{"team"}, ConfirmRemovals: removals})
			Expect(err).NotTo(HaveOccurred())
			Expect(os.ReadFile(mine)).To(Equal([]byte("version: 1\nbundle: [team]\n")))
			Expect(shipped(userRoot())).NotTo(BeAnExistingFile(), "the sync removes its modules")
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: user})
			Expect(err).NotTo(HaveOccurred(), "the bundle on loads again")

			// The edited bundle's own result is judged in full.
			_, err = e.EditBundle(ctx, engine.BundleEditRequest{Scope: user, Name: "mine", Members: []string{"team", "remote:gone"}, ConfirmRemovals: removals})
			Expect(err).To(HaveOccurred(), "a member the edit names that resolves to nothing is refused")
			Expect(os.ReadFile(mine)).To(Equal([]byte("version: 1\nbundle: [team]\n")), "a refused run writes nothing")
		})

		It("refuses a remote profile that is a member of the bundle on, and a profile of yours that is on", func() {
			keep("go", pinned())
			writeTree(p.UserProfiles, map[string]string{"mine.yml": "version: 1\nbundle: [remote:go]\n"})
			activate(user, "user:mine")
			_, err := remove(sc, "remote:go", false, yes)
			Expect(err).To(MatchError("remote:go is in bundle user:mine, on at user scope"))
			Expect(errs.Hints(err)).To(HaveExactElements("turn the bundle off first: lola profile --user user:mine --off",
				"or take remote:go out of bundle mine: lola profile --user mine --edit --bundle ''", HavePrefix("or remove it anyway: ")))
			activate(user, "user:mine")
			_, err = remove(user, "user:mine", false, yes)
			Expect(err).To(MatchError("user:mine is on at user scope"), "a profile of yours is removed too, once off (spec 2026-10-29 §6.2)")
			Expect(os.WriteFile(filepath.Join(p.RemoteProfiles, "stray"), nil, 0o600)).To(Succeed())
			_, err = remove(user, "stray", true, yes)
			Expect(err).To(MatchError("you have no remote profile stray"), "a file there is no profile")
			Expect(filepath.Join(p.RemoteProfiles, "stray")).To(BeAnExistingFile())
			activate(user, "")
			_, err = remove(sc, "go", false, yes)
			Expect(err).NotTo(HaveOccurred())
		})

		Describe("of a profile of yours", func() {
			mine := func(name, content string) string {
				GinkgoHelper()
				writeTree(p.UserProfiles, map[string]string{name + ".yml": content})
				return filepath.Join(p.UserProfiles, name+".yml")
			}

			It("moves it to the trash once asked, refused off a terminal without -y, and -n writes nothing", func() {
				try := mine("try", "version: 1\n# an experiment\n")
				_, err := remove(user, "user:try", false, nil)
				Expect(err).To(MatchError("lola profile --remove asks before it moves " + try + " to the trash, and there is no terminal to ask on"))
				Expect(errs.Hints(err)).To(Equal([]string{"move " + try + " to the trash: lola profile --user user:try --remove -y"}))
				res, err := e.RemoveProfile(ctx, engine.ProfileRemoveRequest{Scope: user, Name: "try", DryRun: true})
				Expect(err).NotTo(HaveOccurred(), "a bare name only yours has is yours")
				Expect(res).To(And(HaveField("Removed", false), HaveField("Trash", ""), HaveField("DryRun", true)))
				Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "to remove it: lola profile --user user:try --remove (off a terminal: -y)"}))
				Expect(try).To(BeARegularFile(), "-n writes nothing")
				_, err = remove(user, "user:try", false, func(engine.RemoteProfile) (bool, error) { return false, nil })
				Expect(err).To(MatchError(engine.ErrDeclined))
				Expect(try).To(BeARegularFile())

				res, err = remove(user, "user:try", false, yes)
				Expect(err).NotTo(HaveOccurred(), "the hint's -y answers as Confirm does")
				Expect(asked).To(Equal(1))
				Expect(res).To(And(HaveField("Profile", "user:try"), HaveField("File", try), HaveField("Removed", true)))
				Expect(try).NotTo(BeAnExistingFile())
				Expect(res.Trash).To(And(HavePrefix(p.Trash), HaveSuffix(filepath.Join("profiles", "try.yml"))))
				Expect(os.ReadFile(res.Trash)).To(Equal([]byte("version: 1\n# an experiment\n")), "restoring is a move back")
			})

			It("refuses a bare name both a profile of yours and a remote one have, hinting each qualified, which run as printed", func() {
				keep("go", pinned())
				mine("go", "version: 1\n")
				_, err := e.RemoveProfile(ctx, engine.ProfileRemoveRequest{Scope: user, Name: "go", Confirm: yes, Force: true, DryRun: true, Display: []string{"--json"}})
				Expect(err).To(MatchError("go names your profile user:go and the remote profile remote:go"))
				Expect(errs.Hints(err)).To(Equal([]string{"lola profile --user user:go --remove -n --json", "lola profile --user remote:go --remove -n --json"}),
					"the hints keep -n and the output flags, never --force or -y")
				for _, q := range []string{"user:go", "remote:go"} {
					_, err = remove(user, q, false, yes)
					Expect(err).NotTo(HaveOccurred(), q)
				}
				Expect(filepath.Join(p.UserProfiles, "go.yml")).NotTo(BeAnExistingFile())
				Expect(filepath.Join(p.RemoteProfiles, "go")).NotTo(BeAnExistingFile())
			})

			It("refuses one that is on, or in the bundle on, unless --force, and names the bundles of yours it leaves broken", func() {
				mine("try", "version: 1\n")
				work := mine("work", "version: 1\nbundle: [try]\n")
				idle := mine("idle", "version: 1\nbundle: [user:try]\n")
				activate(sc, "user:try")
				_, err := remove(user, "try", false, yes)
				Expect(err).To(MatchError("user:try is on in project "+proj), "the project found from here is checked")
				Expect(errs.Hints(err)).To(Equal([]string{"turn the profile off first: lola profile --project user:try --off",
					"or remove it anyway: lola profile --user user:try --remove --force"}))
				activate(sc, "")
				activate(user, "user:work")
				_, err = remove(user, "try", false, yes)
				Expect(err).To(MatchError("user:try is in bundle user:work, on at user scope"))
				Expect(asked).To(BeZero())
				for n, want := range map[string]string{"try": "user:try is in bundle user:work, on at user scope", "work": "user:work is on at user scope"} {
					_, err = remove(user, n, false, nil)
					Expect(err).To(MatchError(want), "off a terminal, in use is said before -y is offered: no -y hint then refuses")
					Expect(errs.Hints(err)).NotTo(ContainElement(ContainSubstring("-y")), n)
				}
				mine("outer", "version: 1\nbundle: [work]\n")
				activate(user, "user:outer")
				_, err = remove(user, "work", false, yes)
				Expect(err).To(MatchError("user:work is in bundle user:outer, on at user scope"), "a bundle the one on nests is in use")
				activate(user, "user:work")

				res, err := e.RemoveProfile(ctx, engine.ProfileRemoveRequest{Scope: user, Name: "try", Force: true, DryRun: true})
				Expect(err).NotTo(HaveOccurred())
				Expect(res.Notices).To(ContainElements(
					HaveField("Text", HavePrefix("user:try is in bundle user:work, on at user scope; its modules would stay installed there, and lola sync --user would refuse bundle user:work until you ")),
					engine.Notice{Level: engine.LevelWarn, Text: "bundle idle (" + idle + ") names user:try; it would refuse to load until you take it out → " +
						"lola profile --user idle --edit --bundle ''"}),
					"-n says what would happen")

				res, err = remove(user, "try", true, nil)
				Expect(err).NotTo(HaveOccurred(), "--force removes it, asking nothing")
				Expect(res.Notices).To(ContainElements(
					engine.Notice{Level: engine.LevelWarn, Text: "user:try is in bundle user:work, on at user scope; its modules stay installed there, and lola sync --user refuses bundle user:work until you " +
						"take try out of bundle work: lola profile --user work --edit --bundle ''"},
					engine.Notice{Level: engine.LevelWarn, Text: "bundle idle (" + idle + ") names user:try; it refuses to load until you take it out → " +
						"lola profile --user idle --edit --bundle ''"}))
				Expect(res.Notices).NotTo(ContainElement(HaveField("Text", ContainSubstring("("+work+") names"))), "the bundle on is said once, above")
				_, err = e.EditBundle(ctx, engine.BundleEditRequest{Scope: user, Name: "idle", Members: []string{}})
				Expect(err).NotTo(HaveOccurred(), "the notice's run runs as printed, whatever the bundle on names")
				sw, err := turn(user, engine.ProfileSwitch, "base")
				Expect(err).NotTo(HaveOccurred(), "lola profile base --switch, which is --off, works with a member gone")
				Expect(sw.Notices).To(ContainElement(HaveField("Text", HavePrefix(work+":2: bundle: try is not a profile of yours"))))
				Expect(e.Context(user, nil).Profile).To(BeNil(), "the bundle is off")
				activate(user, "user:work")
				off, err := e.Sync(ctx, engine.SyncRequest{Scope: user, Profiles: &engine.ProfileChange{Action: engine.ProfileOff},
					ConfirmRemovals: func([]engine.Removal) (bool, error) { return true, nil }})
				Expect(err).NotTo(HaveOccurred(), "lola profile --user --off works with a member gone")
				Expect(off.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn, Text: work + ":2: bundle: try is not a profile of yours or a remote profile you have; " +
					"it is left out until you take it out: lola profile --user work --edit --bundle ''"}))
				Expect(e.Context(user, nil).Profile).To(BeNil(), "the bundle is off")
				_, err = e.EditBundle(ctx, engine.BundleEditRequest{Scope: user, Name: "work", Members: []string{}, ConfirmRemovals: func([]engine.Removal) (bool, error) { return true, nil }})
				Expect(err).NotTo(HaveOccurred(), "the notice's run runs as printed")
				Expect(os.ReadFile(idle)).To(Equal([]byte("version: 1\nbundle: []\n")))
			})

			It("refuses the base, a project's profile, which git removes, and a name nothing has", func() {
				mine("default", "version: 1\n")
				writeTree(proj, map[string]string{".lola/profiles/review.yml": "version: 1\n"})
				for _, n := range []string{"default", "base", "user:default"} {
					_, err := remove(user, n, true, yes)
					Expect(err).To(MatchError("the base is always loaded and lola keeps it; --remove deletes a profile of yours or a remote one"), n)
				}
				for _, n := range []string{"review", "project:review"} {
					_, err := remove(sc, n, true, yes)
					Expect(err).To(MatchError("project:review is committed with the project ("+filepath.Join(proj, ".lola", "profiles", "review.yml")+
						"); lola does not delete it"), n)
					Expect(errs.Hints(err)).To(Equal([]string{"remove it from the project: git -C " + proj + " rm .lola/profiles/review.yml"}))
				}
				_, err := remove(user, "nope", true, yes)
				Expect(err).To(MatchError("you have no profile nope and no remote profile nope"))
				Expect(errs.Hints(err)).To(Equal([]string{"see the profiles: lola profile --user"}))
				_, err = remove(user, "user:nope", true, yes)
				Expect(err).To(MatchError("you have no profile user:nope"))

				try := mine("try", "version: 1\n")
				for _, n := range []string{"foo:try", "base:try", "default:try"} {
					_, err = remove(user, n, true, yes)
					Expect(err).To(MatchError(n+" is not user:, project: or remote: followed by a name"), "a kind lola has not is never read as user:")
					Expect(errs.Hints(err)).To(Equal([]string{"see the profiles: lola profile --user"}))
				}
				Expect(try).To(BeARegularFile(), "nothing went to the trash")
				Expect(p.Trash).NotTo(BeAnExistingFile())
			})

			It("refuses a bundle the one on nests that gives no layer: an empty one, or one whose members are placed already", func() {
				try := mine("try", "version: 1\n")
				empty := mine("empty", "version: 1\nbundle: []\n")
				dup := mine("dup", "version: 1\nbundle: [try]\n")
				mine("outer", "version: 1\nbundle: [try, empty, dup]\n")
				activate(user, "user:outer")
				for n, path := range map[string]string{"empty": empty, "dup": dup, "try": try} {
					_, err := remove(user, n, false, yes)
					Expect(err).To(MatchError("user:"+n+" is in bundle user:outer, on at user scope"), n)
					Expect(path).To(BeARegularFile(), n)
				}
				Expect(asked).To(BeZero())
				Expect(p.Trash).NotTo(BeAnExistingFile(), "nothing went to the trash")
			})

			It("moves a link of yours that points at nothing to the trash as a link", func() {
				try := filepath.Join(p.UserProfiles, "try.yml")
				gone := filepath.Join(tmp, "dots", "gone.yml")
				Expect(os.MkdirAll(p.UserProfiles, 0o755)).To(Succeed())
				Expect(os.Symlink(gone, try)).To(Succeed())
				_, err := remove(user, "try", false, nil)
				Expect(err).To(MatchError("lola profile --remove asks before it moves " + try + " to the trash, and there is no terminal to ask on"))
				Expect(errs.Hints(err)).To(Equal([]string{"move " + try + " to the trash: lola profile --user user:try --remove -y"}))
				res, err := remove(user, "user:try", false, yes)
				Expect(err).NotTo(HaveOccurred(), "the hint's -y answers as Confirm does")
				Expect(res.Removed).To(BeTrue())
				Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: try + " was a link to " + gone + ", which does not exist"}))
				Expect(os.Readlink(res.Trash)).To(Equal(gone))
				_, err = os.Lstat(try)
				Expect(err).To(MatchError(fs.ErrNotExist))
			})

			It("keeps a file changed while you were asked, moves a link as a link, and leaves its pins for the next apply to drop", func() {
				try := mine("try", "version: 1\n")
				_, err := remove(user, "try", false, func(engine.RemoteProfile) (bool, error) {
					return true, os.WriteFile(try, []byte("version: 1\n# edited\n"), 0o600)
				})
				Expect(err).To(MatchError(try + " changed while you were asked; nothing was removed"))
				Expect(errs.Hints(err)).To(Equal([]string{"run it again: lola profile --user user:try --remove"}))
				Expect(try).To(BeARegularFile())
				_, err = e.RemoveProfile(ctx, engine.ProfileRemoveRequest{Scope: user, Name: "try", Display: []string{"--json"}, Confirm: func(engine.RemoteProfile) (bool, error) {
					return true, os.WriteFile(try, []byte("version: 1\n# edited again\n"), 0o600)
				}})
				Expect(errs.Hints(err)).To(Equal([]string{"run it again: lola profile --user user:try --remove --json"}), "the output flags stay")
				_, err = remove(user, "try", false, func(engine.RemoteProfile) (bool, error) {
					activate(user, "user:try")
					return true, nil
				})
				Expect(err).To(MatchError("user:try is on at user scope"), "one switched on while you were asked is checked again")
				Expect(try).To(BeARegularFile())
				activate(user, "")
				Expect(os.Remove(try)).To(Succeed())

				dots := filepath.Join(tmp, "dots", "try.yml")
				writeTree(filepath.Dir(dots), map[string]string{"try.yml": pinned()})
				Expect(os.Symlink(dots, try)).To(Succeed())
				mine("default", "version: 1\n")
				_, err = e.Sync(ctx, engine.SyncRequest{Scope: user, Profiles: &engine.ProfileChange{Action: engine.ProfileSwitch, Name: "try"},
					ConfirmRemovals: func([]engine.Removal) (bool, error) { return true, nil }})
				Expect(err).NotTo(HaveOccurred())
				Expect(os.ReadFile(p.Pins)).To(ContainSubstring(commit), "try's entry is pinned in pins.yaml")
				_, err = turn(user, engine.ProfileOff, "")
				Expect(err).NotTo(HaveOccurred())

				res, err := remove(user, "try", false, yes)
				Expect(err).NotTo(HaveOccurred())
				fi, err := os.Lstat(res.Trash)
				Expect(err).NotTo(HaveOccurred())
				Expect(fi.Mode()&fs.ModeSymlink).NotTo(BeZero(), "a stow-style link goes to the trash as a link")
				Expect(dots).To(BeARegularFile(), "the file it points to stays")
				Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: try + " was a link to " + dots + ", which stays where it is"}))
				Expect(os.ReadFile(p.Pins)).To(ContainSubstring(commit), "only remotes: changes outside an apply (12.2b)")
				_, err = e.Sync(ctx, engine.SyncRequest{Scope: user})
				Expect(err).NotTo(HaveOccurred())
				Expect(os.ReadFile(p.Pins)).NotTo(ContainSubstring(commit), "the next apply drops the pins no file of yours lists")
			})
		})
	})
})
