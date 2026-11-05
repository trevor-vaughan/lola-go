package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
	"github.com/trevor-vaughan/lola-go/internal/trust"
)

var _ = Describe("lola profile --edit", func() {
	var (
		tmp, proj, mods string
		p               paths.Paths
		e               *engine.Engine
		user, sc        engine.Scope
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
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		user, sc = engine.Scope{Kind: target.User}, engine.Scope{Kind: target.Project, ProjectDir: proj}
	})
	// mod writes a folder module named name with two skills, a and b.
	mod := func(name string) string {
		GinkgoHelper()
		path := filepath.Join(mods, name)
		writeTree(path, map[string]string{"lola.yml": "name: " + name + "\n", "skills/a/SKILL.md": "---\ndescription: A\n---\nA\n",
			"skills/b/SKILL.md": "---\ndescription: B\n---\nB\n"})
		return path
	}
	writeProfile := func(name, content string) string {
		GinkgoHelper()
		writeTree(p.UserProfiles, map[string]string{name + ".yml": content})
		return filepath.Join(p.UserProfiles, name+".yml")
	}
	activate := func(s engine.Scope, name string) {
		GinkgoHelper()
		led, err := e.Ledger(s)
		Expect(err).NotTo(HaveOccurred())
		led.Profile = &ledger.Active{Name: name, By: ledger.ByOn}
		path := p.UserLedger
		if s.Kind == target.Project {
			path = filepath.Join(s.ProjectDir, ".lola", "lock.json")
		}
		Expect(led.Save(path)).To(Succeed())
	}
	// remote keeps a remote profile name holding content, as lola profile --add writes it.
	remote := func(name, content string) {
		GinkgoHelper()
		o, err := json.Marshal(profile.Origin{Version: profile.OriginVersion, Source: "https://example.com/profiles.git", Commit: strings.Repeat("a", 40),
			Digest: profile.Digest([]byte(content))})
		Expect(err).NotTo(HaveOccurred())
		writeTree(filepath.Join(p.RemoteProfiles, name), map[string]string{profile.RemoteFile: content, profile.OriginFile: string(o)})
	}
	// edit is the TargetRequest of lola profile <name> --edit <args>, its hints worded as the CLI's:
	// the run with "--profile <x>" taken as the profile it names.
	edit := func(s engine.Scope, name string, args ...string) engine.TargetRequest {
		return engine.TargetRequest{Edit: true, Profile: name, Rerun: func(extra ...string) string {
			named, rest := name, []string{}
			for k := 0; k < len(extra); k++ {
				if extra[k] == "--profile" {
					named, k = extra[k+1], k+1
					continue
				}
				rest = append(rest, extra[k])
			}
			return e.Command(s, "profile", append(append([]string{named}, args...), append([]string{"--edit"}, rest...)...)...)
		}}
	}

	// only installs kit at s, its --only skill/a given, with the edit's TargetRequest t.
	only := func(s engine.Scope, t engine.TargetRequest) (*engine.InstallResult, error) {
		spec := engine.InstallSpec{ConfirmRemovals: confirmAll, Cwd: tmp, Modules: []engine.InstallRequest{{Arg: "kit"}}, Options: engine.Options{Target: t},
			Scopes: []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: s, Source: engine.SourceFlag}}}}
		sel, err := config.ParseSelector("skill/a")
		Expect(err).NotTo(HaveOccurred())
		Expect(spec.AddFlagEdits([]engine.SelectionEdit{{Op: engine.EditOnly, Selector: sel, Exact: true}}, false)).To(Succeed())
		return e.InstallAll(ctx, spec)
	}

	It("splits its arguments into the profile and the modules, by what each names", func() {
		kit := mod("kit")
		writeProfile("go", "version: 1\n")
		_, err := e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: kit}}, engine.Options{Targets: []string{"claude-code"}})
		Expect(err).NotTo(HaveOccurred())
		for _, c := range []struct {
			args    []string
			made    bool
			name    string
			modules []string
		}{
			{[]string{"go", "kit"}, false, "go", []string{"kit"}},
			{[]string{"kit"}, false, "", []string{"kit"}},
			{[]string{"base", "kit"}, false, "base", []string{"kit"}},
			{[]string{"default"}, false, "default", []string{}},
			{[]string{"user:new", "kit"}, false, "user:new", []string{"kit"}},
			{[]string{kit}, false, "", []string{kit}},
			{[]string{"nope", "kit"}, false, "nope", []string{"kit"}},
			{[]string{"kit"}, true, "kit", []string{}},
		} {
			name, modules, err := e.EditArgs(engine.EditArgsRequest{Scope: user, Cwd: tmp, Args: c.args, Made: c.made})
			Expect(err).NotTo(HaveOccurred())
			Expect(name).To(Equal(c.name), "%v", c.args)
			Expect(modules).To(ConsistOf(c.modules), "%v", c.args)
		}
	})

	It("edits a named profile's entry, writing only: for --only, and saves one that is off without applying it", func() {
		kit := mod("kit")
		goYml := writeProfile("go", "version: 1\n# mine\n")
		res, err := e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: kit}}, engine.Options{Targets: []string{"claude-code"},
			Target: edit(user, "go", "kit")})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Edits).To(ContainElement(And(HaveField("Module", "kit"), HaveField("Action", engine.EditAdded), HaveField("Profile", "user:go"))))
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo,
			Text: "saved to profile go; it is not on at user scope → lola profile --user go --switch"}))
		Expect(os.ReadFile(goYml)).To(ContainSubstring("# mine\n"), "comments kept")
		led, err := e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Modules).To(BeEmpty(), "go is off: nothing is applied")
	})

	It("asks where an inherited edit goes, off a terminal hinting lola profile base --edit and lola profile <target> --edit --override, which run as printed", func() {
		kit := mod("kit")
		writeProfile("go", "version: 1\n")
		_, err := e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: kit}}, engine.Options{Targets: []string{"claude-code"}})
		Expect(err).NotTo(HaveOccurred())
		activate(user, "user:go")
		_, err = only(user, edit(user, "", "kit"))
		Expect(err).To(MatchError("kit comes from default (base); lola profile --edit needs to know whether to change it there or override it in profile go, " +
			"and there is no terminal to ask on"))
		Expect(errs.Hints(err)).To(Equal([]string{"lola profile --user base kit --edit", "lola profile --user go kit --edit --override"}))

		req := edit(user, "go", "kit")
		req.Override = true
		res, err := only(user, req)
		Expect(err).NotTo(HaveOccurred(), "the --override hint runs as printed")
		Expect(res.Edits).To(ContainElement(And(HaveField("Module", "kit"), HaveField("Profile", "user:go"))))
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "go.yml"))).To(ContainSubstring("only: [skill/a]"))
		_, err = only(user, edit(user, "base", "kit"))
		Expect(err).NotTo(HaveOccurred(), "the base hint runs as printed")
	})

	It("makes the profile --new names in the install's transaction, refuses one that exists, and writes nothing under -n", func() {
		kit := mod("kit")
		writeProfile("go", "version: 1\n")
		req := edit(user, "try", "kit")
		req.New = true
		res, err := e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: kit}}, engine.Options{Targets: []string{"claude-code"}, Target: req, DryRun: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.Join(p.UserProfiles, "try.yml")).NotTo(BeAnExistingFile(), "-n writes nothing")
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "would be saved to profile try; it is not on at user scope"}),
			"-n says would, and hints no switch to a profile it did not make")
		res, err = e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: kit}}, engine.Options{Targets: []string{"claude-code"}, Target: req})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Edits).To(ContainElement(And(HaveField("Module", "kit"), HaveField("Profile", "user:try"), HaveField("Created", true))))
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "try.yml"))).To(ContainSubstring("name: kit"))

		req = edit(user, "go", "kit")
		req.New = true
		_, err = e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: "kit"}}, engine.Options{Target: req})
		Expect(err).To(MatchError("go names user:go (" + filepath.Join(p.UserProfiles, "go.yml") + "); --new makes a profile that does not exist yet"))
		Expect(errs.Hints(err)).To(Equal([]string{"edit it: lola profile --user user:go kit --edit", "or make another: lola profile --user go-2 kit --edit --new"}))
	})

	It("names what it edits: a profile's modules, a bundle's members, the write target, and refuses a remote profile with a copy that runs as printed", func() {
		kit := mod("kit")
		writeProfile("go", "version: 1\nmodules:\n  - {name: kit, source: "+kit+"}\n")
		writeProfile("docs", "version: 1\n")
		writeProfile("work", "version: 1\nbundle: [go, docs]\n")
		t, err := e.EditTarget(user, edit(user, "go"), nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(*t).To(Equal(engine.EditTarget{Name: "go", Label: "profile go", Path: filepath.Join(p.UserProfiles, "go.yml"), Kind: "user",
			Modules: []string{"kit"}, Members: []string{}, CacheAdd: map[string]string{"kit": "lola cache add " + kit}}))
		t, err = e.EditTarget(user, edit(user, "work"), nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(t.Bundle).To(BeTrue())
		Expect(t.Members).To(Equal([]string{"go", "docs"}))
		t, err = e.EditTarget(user, edit(user, ""), nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(t.Label).To(Equal("default (base)"), "no profile on: the base")
		activate(user, "user:work")
		_, err = e.EditTarget(user, edit(user, ""), nil)
		Expect(err).To(MatchError("lola profile --edit needs to know which profile of bundle work gets this change, and there is no terminal to ask on"))
		Expect(errs.Hints(err)).To(Equal([]string{"lola profile --user docs --edit", "lola profile --user go --edit"}))
		_, err = e.EditTarget(user, edit(user, "nope"), nil)
		Expect(engine.IsUnknownProfile(err)).To(BeTrue(), "%v", err)

		remote("team", "version: 1\n")
		_, err = e.EditTarget(user, edit(user, "remote:team"), nil)
		Expect(err).To(MatchError("remote:team is read-only; lola does not edit remote profiles"))
		Expect(errs.Hints(err)).To(Equal([]string{"switch to a copy of yours, which you can edit: lola profile --user team-mine --switch --new --from remote:team"}))
	})

	It("makes an empty profile with --new and no module, applying nothing, its hint running as printed; -n makes nothing", func() {
		res, err := e.MakeProfile(ctx, engine.MakeRequest{Scope: user, Name: "try", Target: edit(user, "try"), Options: engine.Options{DryRun: true}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Created).To(ConsistOf(HaveField("Profile", "user:try")))
		Expect(res.Notices).NotTo(ContainElement(HaveField("Text", ContainSubstring("--switch"))), "nothing was made: no run turns it on")
		Expect(filepath.Join(p.UserProfiles, "try.yml")).NotTo(BeAnExistingFile())
		res, err = e.MakeProfile(ctx, engine.MakeRequest{Scope: sc, Name: "try", Target: edit(sc, "try")})
		Expect(err).NotTo(HaveOccurred())
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "try.yml"))).To(Equal([]byte("version: 1\n")))
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "try is not on in this project → lola profile --project try --switch"}))
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileSwitch, Name: "try"}})
		Expect(err).NotTo(HaveOccurred(), "the hint runs as printed")
		_, err = e.MakeProfile(ctx, engine.MakeRequest{Scope: sc, Name: "remote:x", Target: edit(sc, "remote:x")})
		Expect(err).To(MatchError("--new makes a profile of yours or the project's; lola profile --add fetches a remote one"))
		Expect(errs.Hints(err)).To(Equal([]string{"make it yours: lola profile --project x --edit --new"}))
		_, err = e.MakeProfile(ctx, engine.MakeRequest{Scope: user, Name: "try", Target: edit(user, "try")})
		Expect(err).To(MatchError("try names user:try (" + filepath.Join(p.UserProfiles, "try.yml") + "); --new makes a profile that does not exist yet"))
		Expect(errs.Hints(err)).To(Equal([]string{"see it: lola profile --user user:try", "or make another: lola profile --user try-2 --edit --new"}),
			"without a module there is nothing to edit it by: the hint shows it")
	})

	It("hints a free name once with the run's own --bundle, and that hint runs as printed", func() {
		_, err := e.MakeProfile(ctx, engine.MakeRequest{Scope: user, Name: "try", Target: edit(user, "try")})
		Expect(err).NotTo(HaveOccurred())
		_, err = e.MakeProfile(ctx, engine.MakeRequest{Scope: user, Name: "try", Bundle: []string{"a", "b"}, Target: edit(user, "try", "--bundle", "a,b")})
		Expect(err).To(MatchError(ContainSubstring("--new makes a profile that does not exist yet")))
		Expect(errs.Hints(err)).To(ContainElement("or make another: lola profile --user try-2 --bundle a,b --edit --new"))
		_, err = e.MakeProfile(ctx, engine.MakeRequest{Scope: user, Name: "try-2", Bundle: []string{"a", "b"}, Target: edit(user, "try-2", "--bundle", "a,b")})
		Expect(err).NotTo(HaveOccurred(), "the hint runs as printed")
	})

	It("asks which a name means when it is a profile and a module, and off a terminal refuses with the run for each, the names qualified", func() {
		kit := mod("kit")
		_, err := e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: kit}}, engine.Options{Targets: []string{"claude-code"}})
		Expect(err).NotTo(HaveOccurred())
		kitYml := writeProfile("kit", "version: 1\n")
		rerun := func(names ...string) string {
			return "lola profile --user " + strings.Join(names, " ") + " --edit --only skill/a"
		}
		req := engine.EditArgsRequest{Scope: user, Cwd: tmp, Args: []string{"kit"}, Rerun: rerun}
		_, _, err = e.EditArgs(req)
		Expect(err).To(MatchError("kit names profile user:kit (" + kitYml + ") and a module; lola profile --edit needs to know which to edit, and there is no terminal to ask on"))
		Expect(errs.Hints(err)).To(Equal([]string{"see profile user:kit, then name a module after it: lola profile --user user:kit",
			"edit module kit in default (base): lola profile --user base kit --edit --only skill/a"}), "no other module: an edit of the profile names one")
		for _, args := range [][]string{{"user:kit"}, {"base", "kit"}} {
			name, modules, err := e.EditArgs(engine.EditArgsRequest{Scope: user, Cwd: tmp, Args: args, Rerun: rerun})
			Expect(err).NotTo(HaveOccurred(), "%v: the hint is read as it says", args)
			Expect(append([]string{name}, modules...)).To(Equal(args))
		}

		activate(user, "user:kit")
		_, _, err = e.EditArgs(engine.EditArgsRequest{Scope: user, Cwd: tmp, Args: []string{"kit", "other"}, Rerun: rerun})
		Expect(errs.Hints(err)).To(Equal([]string{"edit profile user:kit: lola profile --user user:kit other --edit --only skill/a",
			"edit module kit in profile kit: lola profile --user user:kit kit other --edit --only skill/a"}), "the profile on, named first, is qualified too")

		var asked engine.WhereQuestion
		for answer, want := range map[int][]string{0: {"user:kit"}, 1: {"", "kit"}} {
			req.Ask = func(q engine.WhereQuestion) (engine.WhereChoice, error) { asked = q; return q.Choices[answer], nil }
			name, modules, err := e.EditArgs(req)
			Expect(err).NotTo(HaveOccurred())
			Expect(append([]string{name}, modules...)).To(Equal(want))
		}
		Expect(asked.Title).To(Equal("kit names profile user:kit (" + kitYml + ") and a module."))
		Expect(asked.Choices).To(HaveExactElements(HaveField("Label", "Edit profile user:kit"), HaveField("Label", "Edit module kit in profile kit")))
	})

	It("applies a named profile of yours in each scope it is on when no scope flag names one, and in the scope named alone otherwise", func() {
		writeProfile("go", "version: 1\n")
		writeTree(proj, map[string]string{".lola/profiles/team.yml": "version: 1\n"})
		found := engine.ScopeChoice{Scope: sc, Source: engine.SourceDiscovered}
		scopes := func(read engine.ScopeChoice, name string) []engine.ScopeChoice {
			GinkgoHelper()
			out, err := e.EditScopes(read, name)
			Expect(err).NotTo(HaveOccurred())
			return out
		}
		Expect(scopes(found, "go")).To(Equal([]engine.ScopeChoice{found}), "off at user scope")
		activate(user, "user:go")
		Expect(scopes(found, "go")).To(Equal([]engine.ScopeChoice{found, {Scope: user, Source: engine.SourceDiscovered}}))
		Expect(scopes(found, "user:go")).To(HaveLen(2))
		for _, name := range []string{"", "base", "project:team", "nope"} {
			Expect(scopes(found, name)).To(Equal([]engine.ScopeChoice{found}), name)
		}
		flagged := engine.ScopeChoice{Scope: sc, Source: engine.SourceFlag}
		Expect(scopes(flagged, "go")).To(Equal([]engine.ScopeChoice{flagged}), "--project names the scope")
		atUser := engine.ScopeChoice{Scope: user, Source: engine.SourceDiscovered}
		Expect(scopes(atUser, "go")).To(Equal([]engine.ScopeChoice{atUser}))
	})

	It("qualifies, in a flag form, a profile name a catalog entry has too, which the run then reads as the profile", func() {
		kit := mod("kit")
		writeProfile("go", "version: 1\n")
		_, err := e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: kit}}, engine.Options{Targets: []string{"claude-code"}})
		Expect(err).NotTo(HaveOccurred())
		activate(user, "user:go")
		names, complete := e.CatalogNames(nil)
		Expect(names).To(BeEmpty(), "no marketplace, no names")
		Expect(complete).To(BeTrue())
		req := edit(user, "", "kit")
		req.Catalog = []string{"go"}
		_, err = only(user, req)
		Expect(errs.Hints(err)).To(Equal([]string{"lola profile --user base kit --edit", "lola profile --user user:go kit --edit --override"}))
		name, modules, err := e.EditArgs(engine.EditArgsRequest{Scope: user, Cwd: tmp, Args: []string{"user:go", "kit"}, Catalog: req.Catalog})
		Expect(err).NotTo(HaveOccurred())
		Expect(append([]string{name}, modules...)).To(Equal([]string{"user:go", "kit"}), "the hint reads as it says")
		_, _, err = e.EditArgs(engine.EditArgsRequest{Scope: user, Cwd: tmp, Args: []string{"go", "kit"}, Catalog: req.Catalog,
			Rerun: func(names ...string) string { return "lola profile " + strings.Join(names, " ") + " --edit" }})
		Expect(err).To(MatchError(ContainSubstring("go names profile user:go")), "bare, it would be asked about")
		over := edit(user, "user:go", "kit")
		over.Override, over.Catalog = true, req.Catalog
		_, err = only(user, over)
		Expect(err).NotTo(HaveOccurred(), "the hint runs as printed")
	})

	It("keeps the run's --override when a fetched module's install fails, its retry running as printed", func() {
		kit := mod("kit")
		writeTree(kit, map[string]string{"lola.yml": "name: kit\nhooks: {pre-install: hooks/pre.sh}\n", "hooks/pre.sh": "#!/bin/sh\n"})
		Expect(os.Chmod(filepath.Join(kit, "hooks/pre.sh"), 0o755)).To(Succeed())
		writeProfile("go", "version: 1\n")
		activate(user, "user:go")
		req := edit(user, "go", kit)
		req.Override = true
		opts := engine.Options{Targets: []string{"claude-code"}, Target: req,
			Ask: func(string, []engine.HookScript) (bool, error) { return false, errors.New("cancelled") }}
		_, err := e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: kit}}, opts)
		Expect(err).To(MatchError("cancelled"))
		Expect(errs.Hints(err)).To(ContainElement("fetched modules stay in your cache; retry with: lola profile --user go " + kit + " --edit --override"))
		opts.Ask = func(string, []engine.HookScript) (bool, error) { return true, nil }
		_, err = e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: kit}}, opts)
		Expect(err).NotTo(HaveOccurred(), "the retry runs as printed")
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "go.yml"))).To(ContainSubstring("name: kit"))
	})

	It("syncs the other scope a profile of yours is on in only for what the edit wrote into it", func() {
		work := mod("kit")
		gitIn(work, "init", "-q", "-b", "main")
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "one")
		gitIn(tmp, "clone", "-q", "--bare", work, filepath.Join(tmp, "srv", "kit.git"))
		kit := testenv.ServeGit(filepath.Join(tmp, "srv")) + "/kit.git"
		writeProfile("go", "version: 1\n")
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + kit + "}\n"})
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }})
		Expect(err).NotTo(HaveOccurred())
		activate(sc, "user:go")
		activate(user, "user:go")
		// both edits kit at the project and at user scope, go's run, its question answered with
		// the choice labeled answer, its picks --only of pick when given, the project's assistants
		// those of targets when given.
		both := func(answer string, override bool, pick string, targets ...string) (*engine.InstallResult, error) {
			GinkgoHelper()
			t := edit(sc, "go", "kit")
			t.Override = override
			spec := engine.InstallSpec{ConfirmRemovals: confirmAll, Cwd: proj, Modules: []engine.InstallRequest{{Arg: "kit"}},
				Options: engine.Options{Target: t, AskWhere: func(q engine.WhereQuestion) (engine.WhereChoice, error) {
					k := slices.IndexFunc(q.Choices, func(c engine.WhereChoice) bool { return strings.HasPrefix(c.Label, answer) })
					Expect(k).To(BeNumerically(">=", 0), "%v", q.Choices)
					return q.Choices[k], nil
				}},
				Scopes: []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: sc, Source: engine.SourceDiscovered}, Targets: targets},
					{ScopeChoice: engine.ScopeChoice{Scope: user, Source: engine.SourceDiscovered}}}}
			if pick != "" {
				sel, err := config.ParseSelector(pick)
				Expect(err).NotTo(HaveOccurred())
				Expect(spec.AddFlagEdits([]engine.SelectionEdit{{Op: engine.EditOnly, Selector: sel, Exact: true}}, false)).To(Succeed())
			}
			return e.InstallAll(ctx, spec)
		}
		scopesOf := func(res *engine.InstallResult) []string {
			var out []string
			for _, s := range res.Scopes {
				out = append(out, s.Scope)
			}
			return out
		}
		res, err := both("Change your picks", false, "skill/a")
		Expect(err).NotTo(HaveOccurred(), "picks of project.yml's module: selections.yaml, nothing of user scope's")
		Expect(scopesOf(res)).To(Equal([]string{"project"}))
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "go.yml"))).To(Equal([]byte("version: 1\n")))

		res, err = both("Change it in", false, "", "claude-code", "opencode")
		Expect(err).NotTo(HaveOccurred(), "project.yml's entry: nothing of user scope's")
		Expect(scopesOf(res)).To(Equal([]string{"project"}))
		Expect(os.ReadFile(filepath.Join(proj, ".lola", "project.yml"))).To(ContainSubstring("opencode"))

		res, err = both("Override it", true, "skill/b")
		Expect(err).NotTo(HaveOccurred())
		Expect(scopesOf(res)).To(Equal([]string{"project", "user"}), "go's entry: user scope syncs it")
		Expect(res.Modules).To(ContainElement(And(HaveField("Name", "kit"), HaveField("Scope", "user"))))
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "go.yml"))).To(ContainSubstring("only: [skill/b]"))
		Expect(filepath.Join(p.Home, ".claude", "skills", "b", "SKILL.md")).To(BeAnExistingFile())
		Expect(filepath.Join(p.Home, ".claude", "skills", "a", "SKILL.md")).NotTo(BeAnExistingFile())
	})

	It("asks, before anything is written, about what syncing the other scope removes; No writes nothing", func() {
		kit := mod("kit")
		writeProfile("default", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: "+kit+", targets: [claude-code, opencode]}\n")
		goYml := writeProfile("go", "version: 1\n")
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: user})
		Expect(err).NotTo(HaveOccurred())
		activate(sc, "user:go")
		activate(user, "user:go")
		var asked [][]engine.Removal
		edit := func(answer bool) (*engine.InstallResult, error) {
			spec := engine.InstallSpec{Cwd: proj, Modules: []engine.InstallRequest{{Arg: kit}}, Options: engine.Options{Target: edit(sc, "go", kit)},
				Scopes: []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: sc, Source: engine.SourceDiscovered}, Targets: []string{"claude-code"}},
					{ScopeChoice: engine.ScopeChoice{Scope: user, Source: engine.SourceDiscovered}}},
				ConfirmRemovals: func(rs []engine.Removal) (bool, error) { asked = append(asked, rs); return answer, nil }}
			return e.InstallAll(ctx, spec)
		}
		_, err = edit(false)
		Expect(err).To(MatchError(engine.ErrDeclined))
		Expect(asked).To(HaveLen(1))
		Expect(asked[0]).To(ConsistOf(HaveField("Module", "kit")))
		Expect(os.ReadFile(goYml)).To(Equal([]byte("version: 1\n")), "No writes nothing")
		Expect(filepath.Join(proj, ".claude", "skills", "a", "SKILL.md")).NotTo(BeAnExistingFile())

		res, err := edit(true)
		Expect(err).NotTo(HaveOccurred())
		Expect(asked).To(HaveLen(2), "asked once")
		Expect(res.Scopes).To(HaveLen(2))
		Expect(os.ReadFile(goYml)).To(ContainSubstring("name: kit"))
		led, err := e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Modules["kit"].InstalledTargets()).To(Equal([]string{"claude-code"}), "the removal confirmed")
	})

	Describe("--bundle", func() {
		yes := func([]engine.Removal) (bool, error) { return true, nil }
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

		It("sets a bundle's members, making a bare one that names no profile, and applies it where the bundle is on", func() {
			kit := mod("kit")
			writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: "+kit+"}\n")
			writeProfile("docs", "version: 1\n")
			work := writeProfile("work", "version: 1\n# mine\nbundle: [docs]\n")
			activate(user, "user:work")
			res, err := e.EditBundle(ctx, engine.BundleEditRequest{Scope: user, Name: "work", Members: []string{"docs", "go", "notes"}, Target: edit(user, "work"),
				ConfirmRemovals: yes})
			Expect(err).NotTo(HaveOccurred())
			Expect(res).To(And(HaveField("Profile", "user:work"), HaveField("Members", []string{"docs", "go", "notes"}), HaveField("Was", []string{"docs"})))
			Expect(res.Created).To(ConsistOf(HaveField("Profile", "user:notes")))
			Expect(res.Sync).NotTo(BeNil(), "work is on: the scope applies its new members")
			Expect(os.ReadFile(work)).To(Equal([]byte("version: 1\n# mine\nbundle: [docs, go, notes]\n")))
			Expect(os.ReadFile(filepath.Join(p.UserProfiles, "notes.yml"))).To(Equal([]byte("version: 1\n")))
			Expect(installed(user)).To(Equal(map[string]string{"kit": "profile:user:go"}))

			_, err = e.EditBundle(ctx, engine.BundleEditRequest{Scope: user, Name: "work", Members: []string{"docs"}, Target: edit(user, "work")})
			Expect(err).To(MatchError(ContainSubstring("lola profile needs your confirmation to remove kit, and there is no terminal to ask on")),
				"taking go out removes kit, which asks")
			Expect(errs.Hints(err)).To(ContainElement("then confirm them: lola profile --user work --edit --bundle docs -y"))
			Expect(os.ReadFile(work)).To(ContainSubstring("bundle: [docs, go, notes]"), "a refused run writes nothing")
			res, err = e.EditBundle(ctx, engine.BundleEditRequest{Scope: user, Name: "work", Members: []string{"docs", "go", "notes"}, Target: edit(user, "work")})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "bundle work already lists docs, go, notes"}))
		})

		It("saves a bundle that is off without applying it, applies one the bundle on nests, and writes nothing under -n", func() {
			kit := mod("kit")
			writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: "+kit+"}\n")
			idle := writeProfile("idle", "version: 1\nbundle: []\n")
			res, err := e.EditBundle(ctx, engine.BundleEditRequest{Scope: user, Name: "idle", Members: []string{"go"}, Target: edit(user, "idle"),
				Options: engine.Options{DryRun: true}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.DryRun).To(BeTrue())
			Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "would be saved to bundle idle; it is not on at user scope"}),
				"-n never says it saved (ruling P5)")
			Expect(os.ReadFile(idle)).To(Equal([]byte("version: 1\nbundle: []\n")), "-n writes nothing")
			res, err = e.EditBundle(ctx, engine.BundleEditRequest{Scope: user, Name: "idle", Members: []string{"go"}, Target: edit(user, "idle")})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Sync).To(BeNil())
			Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "saved to bundle idle; it is not on at user scope → lola profile --user idle --switch"}))
			Expect(installed(user)).To(BeEmpty())

			writeProfile("all", "version: 1\nbundle: [idle]\n")
			activate(user, "user:all")
			Expect(os.WriteFile(idle, []byte("version: 1\nbundle: []\n"), 0o600)).To(Succeed())
			res, err = e.EditBundle(ctx, engine.BundleEditRequest{Scope: user, Name: "idle", Members: []string{"go"}, Target: edit(user, "idle"), ConfirmRemovals: yes})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Sync).NotTo(BeNil(), "all nests idle: the edit changes what the scope loads")
			Expect(installed(user)).To(Equal(map[string]string{"kit": "profile:user:go"}))
			res, err = e.EditBundle(ctx, engine.BundleEditRequest{Scope: user, Name: "idle", Members: []string{}, Target: edit(user, "idle"), ConfirmRemovals: yes})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Sync).NotTo(BeNil(), "all still nests idle, though no member of idle gives a layer now")
			Expect(installed(user)).To(BeEmpty())
		})

		It("refuses a profile that lists modules, the base and a remote profile, each hinting what does it, which run as printed", func() {
			kit := mod("kit")
			writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: "+kit+"}\n")
			writeProfile("docs", "version: 1\n")
			_, err := e.EditBundle(ctx, engine.BundleEditRequest{Scope: user, Name: "go", Members: []string{"docs"}, Target: edit(user, "go")})
			Expect(err).To(MatchError("go lists modules; a bundle is a file of its own"))
			Expect(errs.Hints(err)).To(Equal([]string{"bundle it with these instead: lola profile --user go-docs --switch --new --bundle go,docs"}))
			for _, n := range []string{"base", "default"} {
				_, err = e.EditBundle(ctx, engine.BundleEditRequest{Scope: user, Name: n, Members: []string{"docs", "go"}, Target: edit(user, n)})
				Expect(err).To(MatchError("default is the base and lists modules; a bundle is a profile of its own"), n)
				Expect(errs.Hints(err)).To(Equal([]string{"make one of these and switch to it: lola profile --user docs-go --switch --new --bundle docs,go"}), n)
			}
			remote("team", "version: 1\n")
			_, err = e.EditBundle(ctx, engine.BundleEditRequest{Scope: user, Name: "team", Members: []string{"docs"}, Target: edit(user, "team")})
			Expect(err).To(MatchError("remote:team is read-only; lola does not edit remote profiles"))
			Expect(errs.Hints(err)).To(Equal([]string{"switch to a copy of yours, which you can edit: lola profile --user team-mine --switch --new --from remote:team"}))
			for _, ch := range []engine.ProfileChange{{Name: "go-docs", Bundle: []string{"go", "docs"}}, {Name: "docs-go", Bundle: []string{"docs", "go"}},
				{Name: "team-mine", From: "remote:team"}} {
				ch.Action, ch.New = engine.ProfileSwitch, true
				_, err = e.Sync(ctx, engine.SyncRequest{Scope: user, ConfirmRemovals: yes, Profiles: &ch})
				Expect(err).NotTo(HaveOccurred(), "the hints run as printed: %s", ch.Name)
			}
		})

		It("trusts a project bundle's members as you name them, and asks about what a new member brings", func() {
			writeTree(proj, map[string]string{"mods/r/lola.yml": "name: r\n", "mods/r/commands/r.md": "R\n",
				".lola/profiles/lint.yml": "version: 1\n", ".lola/profiles/team.yml": "version: 1\nbundle: [lint]\n",
				".lola/profiles/review.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: r, source: ../../mods/r}\n"})
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileSwitch, Name: "project:team"},
				Trust: func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }, ConfirmRemovals: yes})
			Expect(err).NotTo(HaveOccurred())
			_, err = e.EditBundle(ctx, engine.BundleEditRequest{Scope: sc, Name: "project:team", Members: []string{"lint", "review"}, Target: edit(sc, "project:team"),
				ConfirmRemovals: yes})
			refusal, ok := errors.AsType[*engine.TrustRefusalError](err)
			Expect(ok).To(BeTrue(), "review's entry is asked about: %v", err)
			_, err = e.EditBundle(ctx, engine.BundleEditRequest{Scope: sc, Name: "project:team", Members: []string{"lint", "review"}, Target: edit(sc, "project:team"),
				ConfirmRemovals: yes, Options: engine.Options{Force: true}})
			_, forced := errors.AsType[*engine.TrustRefusalError](err)
			Expect(forced).To(BeTrue(), "-y and --force never answer the trust question: %v", err)
			Expect(os.ReadFile(filepath.Join(proj, ".lola", "profiles", "team.yml"))).To(Equal([]byte("version: 1\nbundle: [lint]\n")), "a refused run writes nothing")
			Expect(refusal.Report.ProfileTargets).NotTo(ContainElement(HaveField("Field", "bundle")), "the members you named are not asked about")
			Expect(refusal.Report.Changes).To(ContainElement(HaveField("Module", "r")))
			_, err = e.EditBundle(ctx, engine.BundleEditRequest{Scope: sc, Name: "project:team", Members: []string{"lint", "review"}, Target: edit(sc, "project:team"),
				ConfirmRemovals: yes, Options: engine.Options{Trust: engine.TrustEntries | engine.TrustProfiles}})
			Expect(err).NotTo(HaveOccurred())
			Expect(installed(sc)).To(Equal(map[string]string{"r": "profile:project:review"}))
		})

		It("refuses no name, saying to name the bundle, hinting the bundle on and a bundle of them, which run as printed (ruling P4)", func() {
			writeProfile("go", "version: 1\n")
			_, err := e.EditBundle(ctx, engine.BundleEditRequest{Scope: user, Members: []string{}, Target: edit(user, ""), Options: engine.Options{Display: []string{"--plain"}}})
			Expect(err).To(MatchError("--bundle sets the members of the bundle --edit names, and none was named: name the bundle to edit, as in " +
				"lola profile <bundle> --edit --bundle <profile>,<profile>"))
			Expect(errs.Hints(err)).To(Equal([]string{"see the profiles: lola profile --user --plain"}))
			_, err = e.EditBundle(ctx, engine.BundleEditRequest{Scope: user, Members: []string{"notes"}, Target: edit(user, "")})
			Expect(errs.Hints(err)).To(Equal([]string{"make a bundle of them: lola profile --user notes-2 --edit --new --bundle notes"}),
				"never named after its only member, which it cannot name itself; no \"or\" when it is the only hint")
			_, err = e.MakeProfile(ctx, engine.MakeRequest{Scope: user, Name: "notes-2", Bundle: []string{"notes"}, Target: edit(user, "notes-2")})
			Expect(err).NotTo(HaveOccurred(), "the hint runs as printed")

			writeProfile("docs", "version: 1\n")
			writeProfile("work", "version: 1\nbundle: [docs]\n")
			activate(user, "user:work")
			_, err = e.EditBundle(ctx, engine.BundleEditRequest{Scope: user, Members: []string{"go"}, Target: edit(user, "")})
			Expect(errs.Hints(err)).To(Equal([]string{"add them to work, the bundle on: lola profile --user work --edit --bundle docs,go",
				"or make a bundle of them: lola profile --user go-2 --edit --new --bundle go"}))
			_, err = e.MakeProfile(ctx, engine.MakeRequest{Scope: user, Name: "go-2", Bundle: []string{"go"}, Target: edit(user, "go-2")})
			Expect(err).NotTo(HaveOccurred(), "the second hint runs as printed")
			_, err = e.EditBundle(ctx, engine.BundleEditRequest{Scope: user, Name: "work", Members: []string{"docs", "go"}, Target: edit(user, "work"), ConfirmRemovals: yes})
			Expect(err).NotTo(HaveOccurred(), "the first hint runs as printed")
		})

		It("refuses members that close a cycle, naming the line, and writes nothing", func() {
			a := writeProfile("a", "version: 1\nbundle: [work]\n")
			work := writeProfile("work", "version: 1\nbundle: []\n")
			_, err := e.EditBundle(ctx, engine.BundleEditRequest{Scope: user, Name: "work", Members: []string{"a"}, Target: edit(user, "work")})
			Expect(err).To(MatchError(a+":2: bundle: work closes a cycle: work → a → work"), "work is off: the edit's result is checked all the same")
			Expect(os.ReadFile(work)).To(Equal([]byte("version: 1\nbundle: []\n")))
		})

		It("applies a bundle of yours in each scope it reaches, asking first what each removes (ruling P1)", func() {
			kit := mod("kit")
			writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: "+kit+"}\n")
			work := writeProfile("work", "version: 1\nbundle: []\n")
			activate(user, "user:work")
			scopes, err := e.EditScopes(engine.ScopeChoice{Scope: sc, Source: engine.SourceDiscovered}, "work")
			Expect(err).NotTo(HaveOccurred())
			Expect(scopes).To(HaveLen(2), "work is on at user scope: an edit found from the project applies there too")
			Expect(scopes[1].Scope).To(Equal(user))
			scopes, err = e.EditScopes(engine.ScopeChoice{Scope: sc, Source: engine.SourceFlag}, "work")
			Expect(err).NotTo(HaveOccurred())
			Expect(scopes).To(HaveLen(1), "--project names the one scope")

			res, err := e.EditBundle(ctx, engine.BundleEditRequest{Scope: sc, Also: []engine.Scope{user}, Name: "work", Members: []string{"go"}, Target: edit(sc, "work")})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Sync).To(BeNil(), "work is off in the project")
			Expect(res.Also).To(ConsistOf(HaveField("Scope", user)))
			Expect(installed(user)).To(Equal(map[string]string{"kit": "profile:user:go"}))
			Expect(installed(sc)).To(BeEmpty())

			_, err = e.EditBundle(ctx, engine.BundleEditRequest{Scope: sc, Also: []engine.Scope{user}, Name: "work", Members: []string{}, Target: edit(sc, "work")})
			Expect(err).To(MatchError("lola profile needs your confirmation to remove kit (claude-code) at user scope, and there is no terminal to ask on; nothing was written"))
			Expect(errs.Hints(err)).To(Equal([]string{"see what goes: lola profile --project work --edit --bundle '' -n", "then confirm them: lola profile --project work --edit --bundle '' -y"}))
			_, err = e.EditBundle(ctx, engine.BundleEditRequest{Scope: sc, Also: []engine.Scope{user}, Name: "work", Members: []string{}, Target: edit(sc, "work"),
				ConfirmRemovals: func([]engine.Removal) (bool, error) { return false, nil }})
			Expect(err).To(MatchError(engine.ErrDeclined))
			Expect(os.ReadFile(work)).To(Equal([]byte("version: 1\nbundle: [go]\n")), "a refused or declined run writes nothing")
			Expect(installed(user)).To(Equal(map[string]string{"kit": "profile:user:go"}))
			_, err = e.EditBundle(ctx, engine.BundleEditRequest{Scope: sc, Also: []engine.Scope{user}, Name: "work", Members: []string{}, Target: edit(sc, "work"),
				ConfirmRemovals: yes})
			Expect(err).NotTo(HaveOccurred())
			Expect(installed(user)).To(BeEmpty())
		})

		It("asks about a pulled change to a project bundle's members before an edit records the list you name, and hints never repeat it", func() {
			trustAll := func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }
			team := filepath.Join(proj, ".lola", "profiles", "team.yml")
			writeTree(proj, map[string]string{".lola/profiles/lint.yml": "version: 1\n", ".lola/profiles/deploy.yml": "version: 1\n",
				".lola/profiles/team.yml": "version: 1\nbundle: [lint]\n"})
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileSwitch, Name: "project:team"},
				Trust: trustAll, ConfirmRemovals: yes})
			Expect(err).NotTo(HaveOccurred())
			pulled := []byte("version: 1\nbundle: [lint, deploy]\n")
			Expect(os.WriteFile(team, pulled, 0o600)).To(Succeed()) // as a pull would

			_, err = e.EditBundle(ctx, engine.BundleEditRequest{Scope: sc, Name: "project:team", Members: []string{"lint", "extra"}, Target: edit(sc, "project:team"),
				ConfirmRemovals: yes})
			refusal, ok := errors.AsType[*engine.TrustRefusalError](err)
			Expect(ok).To(BeTrue(), "%v", err)
			Expect(err).To(MatchError("lola profile needs your trust for changes to the members of project:team, and there is no terminal to ask on; nothing was written"))
			Expect(refusal.Report.ProfileTargets).To(ConsistOf(engine.ProfileTargets{Profile: "project:team",
				FieldChange: trust.FieldChange{Field: "bundle", From: []string{"lint"}, To: []string{"lint", "deploy"}}}), "the pulled diff")
			Expect(errs.Hints(err)).To(Equal([]string{"see them: lola profile --project team --edit --bundle lint,extra -n",
				"then accept them: lola profile --project team --edit --bundle lint,extra --trust=profiles"}))
			Expect(os.ReadFile(team)).To(Equal(pulled), "a refused run writes nothing")

			_, err = e.EditBundle(ctx, engine.BundleEditRequest{Scope: sc, Members: []string{"x"}, Target: edit(sc, "")})
			Expect(errs.Hints(err)).To(Equal([]string{"make a bundle of them: lola profile --project x-2 --edit --new --bundle x"}),
				"no hint builds on the pulled list: running it would record it as trusted")

			var asked []engine.TrustReport
			_, err = e.EditBundle(ctx, engine.BundleEditRequest{Scope: sc, Name: "project:team", Members: []string{"lint", "extra"}, Target: edit(sc, "project:team"),
				ConfirmRemovals: yes, Trust: func(r engine.TrustReport) (engine.TrustAnswer, error) {
					asked = append(asked, r)
					return engine.TrustAnswer{}, nil
				}})
			Expect(err).To(MatchError(engine.ErrDeclined), "on a terminal the pulled diff is asked first; No writes nothing")
			Expect(asked).To(HaveLen(1))
			Expect(asked[0].ManifestChanged).To(BeTrue(), "the question says the file changed, never that it is unchanged")
			Expect(asked[0].ProfileTargets).To(ConsistOf(HaveField("FieldChange", trust.FieldChange{Field: "bundle", From: []string{"lint"}, To: []string{"lint", "deploy"}})))
			Expect(os.ReadFile(team)).To(Equal(pulled))

			_, err = e.EditBundle(ctx, engine.BundleEditRequest{Scope: sc, Name: "project:team", Members: []string{"lint", "extra"}, Target: edit(sc, "project:team"),
				ConfirmRemovals: yes, Options: engine.Options{Trust: engine.TrustProfiles}})
			Expect(err).NotTo(HaveOccurred(), "the accepting hint runs as printed")
			Expect(os.ReadFile(team)).To(Equal([]byte("version: 1\nbundle: [lint, extra]\n")))
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, ConfirmRemovals: yes})
			Expect(err).NotTo(HaveOccurred(), "the list you named is trusted: nothing is left to ask")
		})

		It("copies with --edit --new --from a project profile only what a move would carry, writing nothing it refuses", func() {
			trustAll := func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }
			try := filepath.Join(p.UserProfiles, "try.yml")
			writeTree(proj, map[string]string{"mods/evil/lola.yml": "name: evil\n", "mods/evil/commands/evil.md": "E\n",
				".lola/profiles/local.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: evil, source: ../../mods/evil}\n"})
			_, err := e.MakeProfile(ctx, engine.MakeRequest{Scope: sc, Name: "try", From: "project:local", Target: edit(sc, "try")})
			Expect(err).To(MatchError("evil (" + try + ") installs from " + filepath.Join(proj, "mods", "evil") + ", inside project " + proj +
				": changes there install without review wherever that file is on; lola keeps a project's local modules out of your files"))
			Expect(try).NotTo(BeAnExistingFile())

			for _, n := range []string{"k", "k2"} {
				work := filepath.Join(tmp, "work", n)
				writeTree(work, map[string]string{"lola.yml": "name: " + n + "\n", "commands/" + n + ".md": "one\n"})
				gitIn(work, "init", "-q", "-b", "main")
				gitIn(work, "add", "-A")
				gitIn(work, "commit", "-q", "-m", "one")
				gitIn(tmp, "clone", "-q", "--bare", work, filepath.Join(tmp, "srv", n+".git"))
			}
			srv := testenv.ServeGit(filepath.Join(tmp, "srv"))
			other := filepath.Join(proj, ".lola", "profiles", "other.yml")
			writeTree(proj, map[string]string{".lola/profiles/other.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: k, source: " + srv + "/k.git}\n"})
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileSwitch, Name: "project:other"},
				Trust: trustAll, ConfirmRemovals: yes})
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(other, []byte("version: 1\ntargets: [claude-code]\nmodules:\n  - {name: k, source: "+srv+"/k.git}\n  - {name: k2, source: "+srv+
				"/k2.git}\n"), 0o600)).To(Succeed()) // as a pull would
			_, err = e.MakeProfile(ctx, engine.MakeRequest{Scope: sc, Name: "try", From: "project:other", Target: edit(sc, "try")})
			Expect(err).To(MatchError(ContainSubstring("lola copies or moves out of a project only what it installed as you trusted it")))
			Expect(errs.Hints(err)).To(Equal([]string{"review the changes first: lola sync --project k k2"}))
			Expect(try).NotTo(BeAnExistingFile())
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Modules: []string{"k", "k2"}, Trust: trustAll, ConfirmRemovals: yes})
			Expect(err).NotTo(HaveOccurred(), "the hint runs as printed")
			_, err = e.MakeProfile(ctx, engine.MakeRequest{Scope: sc, Name: "try", From: "project:other", Target: edit(sc, "try")})
			Expect(err).NotTo(HaveOccurred(), "then the copy goes")
			Expect(try).To(BeAnExistingFile())
		})

		It("says truthfully that the edit was written when removals the check before it did not see are declined at the other scope", func() {
			kit := mod("kit")
			writeProfile("default", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: "+kit+"}\n")
			writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: "+kit+"}\n")
			work := writeProfile("work", "version: 1\nbundle: [go]\n")
			writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\n"})
			activate(user, "user:work")
			activate(sc, "user:work")
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: user, ConfirmRemovals: yes})
			Expect(err).NotTo(HaveOccurred())
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, ConfirmRemovals: yes, Trust: func(engine.TrustReport) (engine.TrustAnswer, error) {
				return engine.TrustAnswer{All: true}, nil
			}})
			Expect(err).NotTo(HaveOccurred())
			calls := 0
			confirm := func([]engine.Removal) (bool, error) {
				calls++
				if calls == 1 { // the project's removal of kit; meanwhile user scope gains a module no layer wants
					led, err := e.Ledger(user)
					Expect(err).NotTo(HaveOccurred())
					ghost := *led.Modules["kit"]
					ghost.Artifacts = nil
					led.Modules["ghost"] = &ghost
					Expect(led.Save(p.UserLedger)).To(Succeed())
					return true, nil
				}
				return false, nil
			}
			_, err = e.EditBundle(ctx, engine.BundleEditRequest{Scope: sc, Also: []engine.Scope{user}, Name: "work", Members: []string{}, Target: edit(sc, "work"),
				ConfirmRemovals: confirm})
			Expect(err).To(MatchError("you declined its removals there; the edit was saved and applied in this project, not at user scope"))
			Expect(errors.Is(err, engine.ErrDeclined)).To(BeFalse(), "the file was written: no run that changed nothing")
			Expect(errs.Hints(err)).To(Equal([]string{"apply it there: lola sync --user"}))
			Expect(os.ReadFile(work)).To(Equal([]byte("version: 1\nbundle: []\n")))
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: user, ConfirmRemovals: yes})
			Expect(err).NotTo(HaveOccurred(), "the hint runs as printed")
		})
	})

	Describe("hints that add or take out a member", func() {
		yes := func([]engine.Removal) (bool, error) { return true, nil }

		It("offers --on of a profile while a bundle of yours is on its addition to that bundle, which runs as printed and keeps the bundle on", func() {
			writeProfile("go", "version: 1\n")
			writeProfile("docs", "version: 1\n")
			writeProfile("lint", "version: 1\n")
			writeProfile("work", "version: 1\nbundle: [go, docs]\n")
			activate(user, "user:work")
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: user, Profiles: &engine.ProfileChange{Action: engine.ProfileOn, Name: "lint"}})
			other, ok := errors.AsType[*engine.OtherProfileOnError](err)
			Expect(ok).To(BeTrue(), "%v", err)
			Expect(other).To(And(HaveField("Into", "work"), HaveField("Joined", []string{"go", "docs", "lint"})))
			Expect(errs.Hints(err)).To(Equal([]string{"lola profile --user user:lint --switch", "lola profile --user work-lint --switch --new --bundle work,lint",
				"or add it to bundle work: lola profile --user work --edit --bundle go,docs,lint"}))
			_, err = e.EditBundle(ctx, engine.BundleEditRequest{Scope: user, Name: "work", Members: other.Joined, ConfirmRemovals: yes})
			Expect(err).NotTo(HaveOccurred(), "the hint runs as printed")
			now := e.Context(user, nil)
			Expect(now.Profile.Name).To(Equal("user:work"), "the bundle stays on")
			Expect(now.Profile.Members).To(HaveLen(3))

			writeProfile("work", "version: 1\nbundle: [go, 'user:docs']\n")
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: user, Profiles: &engine.ProfileChange{Action: engine.ProfileOn, Name: "docs"}})
			other, ok = errors.AsType[*engine.OtherProfileOnError](err)
			Expect(ok).To(BeTrue(), "%v", err)
			Expect(other.Into).To(BeEmpty(), "a member in either spelling is not added twice")
			Expect(errs.Hints(err)).NotTo(ContainElement(HavePrefix("or add it to bundle ")))
		})

		It("offers --on of a profile while a project bundle is on its addition to that bundle, never while a pulled member list is pending", func() {
			trustAll := func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }
			team := filepath.Join(proj, ".lola", "profiles", "team.yml")
			writeTree(proj, map[string]string{".lola/profiles/lint.yml": "version: 1\n", ".lola/profiles/docs.yml": "version: 1\n",
				".lola/profiles/deploy.yml": "version: 1\n", ".lola/profiles/team.yml": "version: 1\nbundle: [lint]\n"})
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileSwitch, Name: "project:team"},
				Trust: trustAll, ConfirmRemovals: yes})
			Expect(err).NotTo(HaveOccurred())
			on := func() (*engine.OtherProfileOnError, error) {
				_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOn, Name: "docs"}})
				other, ok := errors.AsType[*engine.OtherProfileOnError](err)
				Expect(ok).To(BeTrue(), "%v", err)
				return other, err
			}

			Expect(os.WriteFile(team, []byte("version: 1\nbundle: [lint, deploy]\n"), 0o600)).To(Succeed()) // as a pull would
			other, err := on()
			Expect(other).To(And(HaveField("Into", ""), HaveField("Joined", BeNil())), "the edit would record the pulled list as trusted")
			Expect(errs.Hints(err)).NotTo(ContainElement(HavePrefix("or add it to bundle ")))

			Expect(os.WriteFile(team, []byte("version: 1\nbundle: [lint]\n"), 0o600)).To(Succeed())
			other, err = on()
			Expect(other).To(And(HaveField("Into", "team"), HaveField("Joined", []string{"lint", "docs"})))
			Expect(errs.Hints(err)).To(ContainElement("or add it to bundle team: lola profile --project team --edit --bundle lint,docs"))
			_, err = e.EditBundle(ctx, engine.BundleEditRequest{Scope: sc, Name: "team", Members: other.Joined, ConfirmRemovals: yes})
			Expect(err).NotTo(HaveOccurred(), "the hint runs as printed")
			Expect(e.Context(sc, nil).Profile.Name).To(Equal("project:team"), "the bundle stays on")
			Expect(os.ReadFile(team)).To(Equal([]byte("version: 1\nbundle: [lint, docs]\n")))
		})

		It("judges in full the bundles an edit's new member list brings in: a broken one is refused, and nothing is written", func() {
			writeProfile("go", "version: 1\n")
			mine := writeProfile("mine", "version: 1\nbundle: [go]\n")
			team := writeProfile("team", "version: 1\nbundle: [remote:nope]\n")
			activate(user, "user:mine")
			_, err := e.EditBundle(ctx, engine.BundleEditRequest{Scope: user, Name: "mine", Members: []string{"go", "team"}, ConfirmRemovals: yes})
			Expect(err).To(MatchError(team+":2: bundle: remote:nope is not a remote profile you have"), "the edit would create the breakage")
			Expect(os.ReadFile(mine)).To(Equal([]byte("version: 1\nbundle: [go]\n")), "a refused run writes nothing")
			_, err = e.EditBundle(ctx, engine.BundleEditRequest{Scope: user, Name: "go", Members: []string{"team"}, ConfirmRemovals: yes})
			Expect(err).To(MatchError(team+":2: bundle: remote:nope is not a remote profile you have"), "so does a member the bundle on nests")
		})

		It("gives a bundle on with no member lola can write a profile of yours, made empty, which runs as printed", func() {
			kit := mod("kit")
			remote("x", "version: 1\n")
			writeProfile("work", "version: 1\nbundle: [remote:x]\n")
			activate(user, "user:work")
			_, err := e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: kit}}, engine.Options{Targets: []string{"claude-code"}})
			Expect(errs.Hints(err)).To(ContainElement("or add a profile of yours to it, made empty: lola profile --user work --edit --bundle remote:x,work-extras"))
			_, err = e.EditBundle(ctx, engine.BundleEditRequest{Scope: user, Name: "work", Members: []string{"remote:x", "work-extras"}, ConfirmRemovals: yes})
			Expect(err).NotTo(HaveOccurred(), "the hint runs as printed")
			rerun := func(extra ...string) string { return e.Command(user, "install", append([]string{kit}, extra...)...) }
			_, err = e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: kit}}, engine.Options{Targets: []string{"claude-code"},
				Target: engine.TargetRequest{Rerun: rerun}})
			Expect(errs.Hints(err)).To(Equal([]string{"lola install --user " + kit + " --profile work-extras"}), "work-extras takes the edit, once named")
		})
	})
})
