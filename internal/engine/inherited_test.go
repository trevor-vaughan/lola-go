package engine_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

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

var _ = Describe("an edit of an inherited module", func() {
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
		e = engine.New(p)
		proj = filepath.Join(tmp, "proj")
		e.Getwd = func() (string, error) { return proj, nil }
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		user, sc = engine.Scope{Kind: target.User}, engine.Scope{Kind: target.Project, ProjectDir: proj}
	})
	kitFiles := map[string]string{"lola.yml": "name: kit\n", "skills/a/SKILL.md": "---\nname: a\ndescription: A\n---\nA\n",
		"skills/b/SKILL.md": "---\nname: b\ndescription: B\n---\nB\n"}
	folder := func() string {
		GinkgoHelper()
		dir := filepath.Join(tmp, "mods", "kit")
		writeTree(dir, kitFiles)
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
		led.Profile = nil
		if name != "" {
			led.Profile = &ledger.Active{Name: name, By: ledger.ByOn}
		}
		Expect(led.Save(path)).To(Succeed())
	}
	sync := func(s engine.Scope) {
		GinkgoHelper()
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: s, ConfirmRemovals: func([]engine.Removal) (bool, error) { return true, nil },
			Trust: func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }})
		Expect(err).NotTo(HaveOccurred())
	}
	// install installs req at s with a --skip of each of skips, hints repeating the run as the CLI's do.
	install := func(s engine.Scope, req engine.InstallRequest, opts engine.Options, skips ...string) (*engine.InstallResult, error) {
		spec := engine.InstallSpec{ConfirmRemovals: confirmAll, Cwd: tmp, Modules: []engine.InstallRequest{req},
			Scopes: []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: s, Source: engine.SourceFlag}}}}
		var edits []engine.SelectionEdit
		for _, k := range skips {
			sel, err := config.ParseSelector(k)
			Expect(err).NotTo(HaveOccurred())
			edits = append(edits, engine.SelectionEdit{Op: engine.EditSkip, Selector: sel})
		}
		Expect(spec.AddFlagEdits(edits, false)).To(Succeed())
		opts.Target.Rerun = func(extra ...string) string { return e.Command(s, "install", append([]string{req.Arg}, extra...)...) }
		spec.Options = opts
		return e.InstallAll(ctx, spec)
	}
	// served is a git source of kit at tmp/srv/kit.git, its work tree at tmp/work, with commit one
	// tagged v1; move commits to main again, so its head moves on.
	served := func() (url string, move func()) {
		GinkgoHelper()
		work, bare := filepath.Join(tmp, "work"), filepath.Join(tmp, "srv", "kit.git")
		writeTree(work, kitFiles)
		gitIn(work, "init", "-q", "-b", "main")
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "one")
		gitIn(work, "tag", "v1")
		gitIn(tmp, "clone", "-q", "--bare", work, bare)
		return testenv.ServeGit(filepath.Join(tmp, "srv")) + "/kit.git", func() {
			GinkgoHelper()
			writeTree(work, map[string]string{"skills/a/SKILL.md": "---\nname: a\ndescription: A\n---\nA moved\n"})
			gitIn(work, "commit", "-q", "-am", "two")
			gitIn(work, "push", "-q", bare, "main")
		}
	}
	installedA := func() string {
		GinkgoHelper()
		b, err := os.ReadFile(filepath.Join(p.Home, ".claude", "skills", "a", "SKILL.md"))
		Expect(err).NotTo(HaveOccurred())
		return string(b)
	}
	read := func(name string) string {
		GinkgoHelper()
		b, err := os.ReadFile(filepath.Join(p.UserProfiles, name))
		Expect(err).NotTo(HaveOccurred())
		return string(b)
	}

	It("asks where an edit of a module default provides goes, and writes the file of the answer", func() {
		kit := folder()
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + kit + "}\n",
			"go.yml": "version: 1\n"})
		activate(user, "user:go")
		sync(user)
		var asked []engine.WhereQuestion
		override := func(q engine.WhereQuestion) (engine.WhereChoice, error) {
			asked = append(asked, q)
			return q.Choices[1], nil
		}
		_, err := install(user, engine.InstallRequest{Arg: "kit"}, engine.Options{AskWhere: override}, "skill/b")
		Expect(err).NotTo(HaveOccurred())
		Expect(asked).To(Equal([]engine.WhereQuestion{{Scope: user, Module: "kit",
			Title: "kit comes from default (base), loaded under every profile at user scope.",
			Choices: []engine.WhereChoice{{Label: "Change it in default  (every profile sees it)", Profile: "base"},
				{Label: "Override it in profile go  (go gets its own entry, starting from default's)", Override: true}}}}))
		Expect(read("go.yml")).To(Equal("version: 1\nmodules:\n  - name: kit\n    source: " + kit + "\n    skip: [skill/b]\n"))
		Expect(read("default.yml")).NotTo(ContainSubstring("skip"))
		led, err := e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Modules["kit"].From).To(Equal("profile:user:go"))
		Expect(filepath.Join(p.Home, ".claude", "skills", "b")).NotTo(BeAnExistingFile())

		activate(user, "") // nothing on: the base is the target, and its own entry asks nothing
		_, err = install(user, engine.InstallRequest{Arg: "kit"}, engine.Options{AskWhere: override}, "skill/a")
		Expect(err).NotTo(HaveOccurred())
		Expect(asked).To(HaveLen(1))
		Expect(read("default.yml")).To(ContainSubstring("skip: [skill/a]"))
	})

	It("refuses an inherited edit off a terminal with both flag forms; -y and --force answer neither", func() {
		kit := folder()
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + kit + "}\n",
			"go.yml": "version: 1\n"})
		activate(user, "user:go")
		sync(user)
		before := read("default.yml")
		_, err := install(user, engine.InstallRequest{Arg: "kit"}, engine.Options{Force: true}, "skill/b")
		Expect(err).To(MatchError("kit comes from default (base); lola install needs to know whether to change it there or override it in profile go, " +
			"and there is no terminal to ask on"))
		Expect(errs.Hints(err)).To(Equal([]string{"lola install --user kit --profile base", "lola install --user kit --override"}))
		Expect(read("default.yml")).To(Equal(before))
		Expect(read("go.yml")).To(Equal("version: 1\n"))

		_, err = install(user, engine.InstallRequest{Arg: "kit"}, engine.Options{Target: engine.TargetRequest{Override: true}}, "skill/b")
		Expect(err).NotTo(HaveOccurred())
		Expect(read("go.yml")).To(ContainSubstring("skip: [skill/b]"))
	})

	It("Prof-P2-b: --skip --override of a project.yml module names no selections.yaml the file does not have", func() {
		writeTree(proj, map[string]string{"kit/lola.yml": "name: kit\n", "kit/commands/a.md": "A\n", "kit/commands/b.md": "B\n",
			".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: kit}\n", ".lola/profiles/team.yml": "version: 1\n"})
		activate(sc, "project:team")
		sync(sc)
		res, err := install(sc, engine.InstallRequest{Arg: "kit"}, engine.Options{Target: engine.TargetRequest{Override: true}}, "command/b")
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).NotTo(ContainElement(HaveField("Text", ContainSubstring("selections.yaml"))))
		Expect(p.Selections).NotTo(BeAnExistingFile())
	})

	It("an install asking for what the inherited entry says is a notice, not a question", func() {
		kit := folder()
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + kit + "}\n",
			"go.yml": "version: 1\n"})
		activate(user, "user:go")
		sync(user)
		res, err := install(user, engine.InstallRequest{Arg: "kit"}, engine.Options{AskWhere: func(engine.WhereQuestion) (engine.WhereChoice, error) {
			Fail("asked")
			return engine.WhereChoice{}, nil
		}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "kit is already installed from default (base)"}))
		Expect(read("go.yml")).To(Equal("version: 1\n"))
		Expect(res.Steps).To(BeEmpty())
	})

	It("--override copies the entry without its pin and keeps the installed commit, though its ref moved", func() {
		work, bare := filepath.Join(tmp, "work"), filepath.Join(tmp, "srv", "kit.git")
		writeTree(work, kitFiles)
		gitIn(work, "init", "-q", "-b", "main")
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "one")
		gitIn(tmp, "clone", "-q", "--bare", work, bare)
		url := testenv.ServeGit(filepath.Join(tmp, "srv")) + "/kit.git"
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + url + ", ref: main}\n",
			"go.yml": "version: 1\n"})
		sync(user)
		led, err := e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		was := *led.Modules["kit"]

		writeTree(work, map[string]string{"skills/a/SKILL.md": "---\nname: a\ndescription: A\n---\nA moved\n"})
		gitIn(work, "commit", "-q", "-am", "two")
		gitIn(work, "push", "-q", bare, "main")
		activate(user, "user:go")

		// By its source and ref, the install fetches what main serves now; the override still
		// installs the version installed.
		_, err = install(user, engine.InstallRequest{Arg: url, Ref: "main"}, engine.Options{Target: engine.TargetRequest{Override: true}}, "skill/b")
		Expect(err).NotTo(HaveOccurred())
		Expect(read("go.yml")).To(Equal("version: 1\nmodules:\n  - name: kit\n    source: " + url + "\n    ref: main\n    skip: [skill/b]\n"))
		led, err = e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Modules["kit"].Commit).To(Equal(was.Commit), "nothing changes but the edit")
		Expect(led.Modules["kit"].From).To(Equal("profile:user:go"))
		b, err := os.ReadFile(filepath.Join(p.Home, ".claude", "skills", "a", "SKILL.md"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).NotTo(ContainSubstring("moved"))
	})

	It("an entry a bundle's remote member gives is read-only: only Override is offered", func() {
		work, bare := filepath.Join(tmp, "work"), filepath.Join(tmp, "srv", "kit.git")
		writeTree(work, kitFiles)
		gitIn(work, "init", "-q", "-b", "main")
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "one")
		gitIn(work, "tag", "v1")
		gitIn(tmp, "clone", "-q", "--bare", work, bare)
		url := testenv.ServeGitTLS(filepath.Join(tmp, "srv"), nil) + "/kit.git" // a remote profile names https sources only
		_, err := e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: url, Ref: "v1"}}, engine.Options{Targets: []string{"claude-code"}})
		Expect(err).NotTo(HaveOccurred())
		led, err := e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		in := led.Modules["kit"]
		body := "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + url + ", ref: v1, commit: " + in.Commit + ", digest: " + in.Digest + "}\n"
		o, err := json.Marshal(profile.Origin{Version: profile.OriginVersion, Source: "https://example.com/profiles.git", Commit: strings.Repeat("a", 40),
			Digest: profile.Digest([]byte(body))})
		Expect(err).NotTo(HaveOccurred())
		writeTree(filepath.Join(p.RemoteProfiles, "team"), map[string]string{profile.RemoteFile: body, profile.OriginFile: string(o)})
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\n", "go.yml": "version: 1\nbundle: [remote:team, mine]\n",
			"mine.yml": "version: 1\n"})
		activate(user, "user:go")
		sync(user)

		off := false
		_, err = install(user, engine.InstallRequest{Arg: "kit"}, engine.Options{Rules: &off, Target: engine.TargetRequest{Profile: "mine"}})
		Expect(err).To(MatchError("kit comes from remote:team, which is read-only; lola install needs to know whether to override it in profile mine, " +
			"and there is no terminal to ask on"))
		Expect(errs.Hints(err)).To(Equal([]string{"lola install --user kit --profile mine --override"}))
		_, err = install(user, engine.InstallRequest{Arg: "kit"}, engine.Options{Rules: &off, DryRun: true, Target: engine.TargetRequest{Profile: "mine", Override: true}})
		Expect(err).NotTo(HaveOccurred(), "the hint runs as printed")
		var asked []engine.WhereQuestion
		_, err = install(user, engine.InstallRequest{Arg: "kit"}, engine.Options{Rules: &off, AskWhere: func(q engine.WhereQuestion) (engine.WhereChoice, error) {
			asked = append(asked, q)
			return q.Choices[0], nil
		}})
		Expect(err).NotTo(HaveOccurred())
		Expect(asked).To(HaveLen(2))
		Expect(asked[0].Choices).To(ConsistOf(HaveField("Profile", "mine")), "the member question: mine, the bundle's one writable member")
		Expect(asked[1]).To(Equal(engine.WhereQuestion{Scope: user, Module: "kit", Title: "kit comes from remote:team, which is read-only.",
			Choices: []engine.WhereChoice{{Label: "Override it in profile mine  (mine gets its own entry, starting from remote:team's)", Override: true,
				Flags: []string{"--profile", "mine", "--override"}}}}))
	})

	It("an override's write line names the layer it copied only when no override notice does", func() {
		kit := folder()
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + kit + "}\n",
			"go.yml": "version: 1\n"})
		activate(user, "user:go")
		sync(user)
		res, err := install(user, engine.InstallRequest{Arg: "kit"}, engine.Options{Target: engine.TargetRequest{Override: true}}, "skill/b")
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn, Text: "kit: profile go overrides default (base)"}))
		Expect(res.Edits).To(ConsistOf(And(HaveField("Profile", "user:go"), HaveField("Action", "added"), HaveField("Overrides", ""))),
			"the override notice says it")
	})

	It("an override of a remote member's entry names the remote profile in its override notice", func() {
		work, bare := filepath.Join(tmp, "work"), filepath.Join(tmp, "srv", "kit.git")
		writeTree(work, kitFiles)
		gitIn(work, "init", "-q", "-b", "main")
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "one")
		gitIn(work, "tag", "v1")
		gitIn(tmp, "clone", "-q", "--bare", work, bare)
		url := testenv.ServeGitTLS(filepath.Join(tmp, "srv"), nil) + "/kit.git" // a remote profile names https sources only
		_, err := e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: url, Ref: "v1"}}, engine.Options{Targets: []string{"claude-code"}})
		Expect(err).NotTo(HaveOccurred())
		led, err := e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		in := led.Modules["kit"]
		body := "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + url + ", ref: v1, commit: " + in.Commit + ", digest: " + in.Digest + "}\n"
		o, err := json.Marshal(profile.Origin{Version: profile.OriginVersion, Source: "https://example.com/profiles.git", Commit: strings.Repeat("a", 40),
			Digest: profile.Digest([]byte(body))})
		Expect(err).NotTo(HaveOccurred())
		writeTree(filepath.Join(p.RemoteProfiles, "team"), map[string]string{profile.RemoteFile: body, profile.OriginFile: string(o)})
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\n", "go.yml": "version: 1\nbundle: [remote:team, mine]\n",
			"mine.yml": "version: 1\n"})
		activate(user, "user:go")
		sync(user)

		off := false
		res, err := install(user, engine.InstallRequest{Arg: "kit"}, engine.Options{Rules: &off, Target: engine.TargetRequest{Profile: "mine", Override: true}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn, Text: "kit: profile mine overrides remote:team"}))
		Expect(res.Edits).To(ConsistOf(And(HaveField("Profile", "user:mine"), HaveField("Action", "added"), HaveField("Overrides", ""))),
			"the override notice names the remote member, so the write line does not")
	})

	It("an override never copies a pulled project.yml entry you have not trusted", func() {
		writeTree(proj, map[string]string{"kit-a/lola.yml": "name: kit\n", "kit-a/commands/kit.md": "A\n",
			"kit-b/lola.yml": "name: kit\n", "kit-b/commands/kit.md": "B\n",
			".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: kit-a}\n"})
		sync(sc)
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: kit-b}\n"})
		writeTree(p.UserProfiles, map[string]string{"me.yml": "version: 1\n"})
		activate(sc, "user:me")
		_, err := install(sc, engine.InstallRequest{Arg: "kit"}, engine.Options{Target: engine.TargetRequest{Override: true}}, "command/kit")
		Expect(err).To(MatchError("kit: project.yml changed for it since you last trusted it"))
		Expect(errs.Hints(err)).To(Equal([]string{"review and apply it: lola sync --project kit"}))
		b, err := os.ReadFile(filepath.Join(p.UserProfiles, "me.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal("version: 1\n"))
	})

	It("an override or picks edit of a folder module in a project profile writes no pin, and the picks leave selections.yaml", func() {
		writeTree(proj, map[string]string{"kit/lola.yml": "name: kit\n", "kit/commands/a.md": "A\n", "kit/commands/b.md": "B\n",
			".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: kit}\n", ".lola/profiles/team.yml": "version: 1\n"})
		activate(sc, "project:team")
		sync(sc)
		team := filepath.Join(proj, ".lola", "profiles", "team.yml")
		_, err := install(sc, engine.InstallRequest{Arg: "kit"}, engine.Options{Target: engine.TargetRequest{Override: true}}, "command/b")
		Expect(err).NotTo(HaveOccurred())
		b, err := os.ReadFile(team)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal("version: 1\nmodules:\n  - name: kit\n    source: ../../kit\n    skip: [command/b]\n"))
		Expect(p.Selections).NotTo(BeAnExistingFile(), "the override's picks are its entry's")

		// The entry is the profile's own now: a picks edit of it asks nothing and pins nothing.
		_, err = install(sc, engine.InstallRequest{Arg: "kit"}, engine.Options{}, "command/a")
		Expect(err).NotTo(HaveOccurred())
		b, err = os.ReadFile(team)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal("version: 1\nmodules:\n  - name: kit\n    source: ../../kit\n    skip: [command/b, command/a]\n"))
		sync(sc)
		Expect(filepath.Join(proj, ".claude", "commands", "a.md")).NotTo(BeAnExistingFile())
	})

	It("a picks edit of an installed folder module installs the version a plain sync would, never holding the one installed (folders are never pinned)", func() {
		kit := folder()
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + kit + "}\n"})
		sync(user)
		writeTree(kit, map[string]string{"skills/c/SKILL.md": "---\nname: c\ndescription: C\n---\nC\n"})
		v2, err := e.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: kit})
		Expect(err).NotTo(HaveOccurred())
		res, err := install(user, engine.InstallRequest{Arg: "kit"}, engine.Options{}, "skill/b")
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).NotTo(ContainElement(HaveField("Text", ContainSubstring("has no component"))))
		led, err := e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Modules["kit"].Digest).To(Equal(v2.Meta.Digest))
		Expect(filepath.Join(p.Home, ".claude", "skills", "c", "SKILL.md")).To(BeAnExistingFile())
		Expect(filepath.Join(p.Home, ".claude", "skills", "b")).NotTo(BeADirectory())
		next, err := e.Sync(ctx, engine.SyncRequest{Scope: user, Options: engine.Options{DryRun: true}})
		Expect(err).NotTo(HaveOccurred())
		Expect(next.InSync).To(BeTrue(), "the next plain sync contradicts nothing the edit installed")
	})

	It("an override that adds assistants to an inherited folder module writes no pin for the folder", func() {
		writeTree(proj, map[string]string{"kit/lola.yml": "name: kit\n", "kit/commands/a.md": "A\n",
			".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: kit}\n", ".lola/profiles/team.yml": "version: 1\n"})
		activate(sc, "project:team")
		sync(sc)
		_, err := install(sc, engine.InstallRequest{Arg: "kit", Targets: []string{"gemini-cli"}}, engine.Options{Target: engine.TargetRequest{Override: true}})
		Expect(err).NotTo(HaveOccurred())
		b, err := os.ReadFile(filepath.Join(proj, ".lola", "profiles", "team.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal("version: 1\nmodules:\n  - name: kit\n    source: ../../kit\n    targets: [claude-code, gemini-cli]\n"), "a folder is never pinned")
		sync(sc)
	})

	It("a picks edit of a project.yml module under a project profile changes your picks in selections.yaml, writing nothing shared", func() {
		writeTree(proj, map[string]string{"kit/lola.yml": "name: kit\n", "kit/commands/a.md": "A\n", "kit/commands/b.md": "B\n",
			".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: kit}\n", ".lola/profiles/team.yml": "version: 1\n"})
		activate(sc, "project:team")
		sync(sc)
		manifest, team := filepath.Join(proj, ".lola", "project.yml"), filepath.Join(proj, ".lola", "profiles", "team.yml")
		before, err := os.ReadFile(manifest)
		Expect(err).NotTo(HaveOccurred())
		var asked []engine.WhereQuestion
		change := func(q engine.WhereQuestion) (engine.WhereChoice, error) {
			asked = append(asked, q)
			return q.Choices[0], nil
		}
		_, err = install(sc, engine.InstallRequest{Arg: "kit"}, engine.Options{AskWhere: change}, "command/b")
		Expect(err).NotTo(HaveOccurred())
		Expect(asked).To(HaveLen(1))
		Expect(asked[0].Title).To(Equal("kit comes from project.yml (base), loaded under every profile in this project."))
		Expect(asked[0].Choices).To(Equal([]engine.WhereChoice{
			{Label: "Change your picks for project.yml's module  (selections.yaml, not shared)", Profile: "base"},
			{Label: "Override it in profile team  (team gets its own entry, starting from project.yml's)", Override: true}}))
		after, err := os.ReadFile(manifest)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(after)).To(Equal(string(before)))
		b, err := os.ReadFile(team)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal("version: 1\n"))
		b, err = os.ReadFile(p.Selections)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(ContainSubstring("    kit:\n      skip: [command/b]\n"))
		Expect(filepath.Join(proj, ".claude", "commands", "b.md")).NotTo(BeAnExistingFile())
	})

	It("a first install of a module default lists at a ref installs that ref, though the cache holds main's head", func() {
		url, move := served()
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + url + ", ref: v1}\n",
			"go.yml": "version: 1\n"})
		move()
		_, err := e.AddSource(ctx, tmp, url, "", "", nil)
		Expect(err).NotTo(HaveOccurred())
		activate(user, "user:go")
		res, err := install(user, engine.InstallRequest{Arg: "kit"}, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "kit is listed in default (base); installing it from there"}))
		led, err := e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Modules["kit"].Ref).To(Equal("v1"))
		Expect(led.Modules["kit"].Commit).To(Equal(strings.TrimSpace(gitIn(filepath.Join(tmp, "work"), "rev-parse", "v1"))))
		Expect(installedA()).NotTo(ContainSubstring("moved"))
		Expect(read("go.yml")).To(Equal("version: 1\n"))
	})

	It("a not-installed module default lists at a ref, overridden or changed there, installs that ref", func() {
		url, move := served()
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + url + ", ref: v1}\n",
			"go.yml": "version: 1\n"})
		move()
		_, err := e.AddSource(ctx, tmp, url, "", "", nil)
		Expect(err).NotTo(HaveOccurred())
		activate(user, "user:go")
		answer := 1
		where := func(q engine.WhereQuestion) (engine.WhereChoice, error) { return q.Choices[answer], nil }
		_, err = install(user, engine.InstallRequest{Arg: "kit"}, engine.Options{AskWhere: where}, "skill/b")
		Expect(err).NotTo(HaveOccurred())
		Expect(read("go.yml")).To(Equal("version: 1\nmodules:\n  - name: kit\n    source: " + url + "\n    ref: v1\n    skip: [skill/b]\n"))
		Expect(installedA()).NotTo(ContainSubstring("moved"))

		Expect(os.RemoveAll(filepath.Join(p.Home, ".claude"))).To(Succeed())
		led, err := e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		delete(led.Modules, "kit")
		Expect(led.Save(p.UserLedger)).To(Succeed())
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		answer = 0
		_, err = install(user, engine.InstallRequest{Arg: "kit"}, engine.Options{AskWhere: where}, "skill/b")
		Expect(err).NotTo(HaveOccurred())
		Expect(read("default.yml")).To(ContainSubstring("ref: v1"))
		Expect(read("default.yml")).To(ContainSubstring("skip: [skill/b]"))
		Expect(installedA()).NotTo(ContainSubstring("moved"))
	})

	It("a first install of a module project.yml pins installs the pin under a project profile, and refuses one the cache lacks", func() {
		url, move := served()
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + url + "}\n"})
		sync(sc) // pins the entry at commit one
		pinned, err := os.ReadFile(filepath.Join(proj, ".lola", "project.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(pinned)).To(ContainSubstring("commit: "))
		rec, err := e.Trust.Load(proj)
		Expect(err).NotTo(HaveOccurred())
		move()
		_, err = e.AddSource(ctx, tmp, url, "", "", nil)
		Expect(err).NotTo(HaveOccurred())

		other := filepath.Join(tmp, "other")
		sc2 := engine.Scope{Kind: target.Project, ProjectDir: other}
		writeTree(other, map[string]string{".lola/project.yml": string(pinned), ".lola/profiles/team.yml": "version: 1\n"})
		view := rec.View
		view.Profiles = map[string]trust.ProfileView{"team": {}}
		Expect(e.Trust.Save(other, view)).To(Succeed())
		activate(sc2, "project:team")
		_, err = install(sc2, engine.InstallRequest{Arg: "kit"}, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		led, err := e.Ledger(sc)
		Expect(err).NotTo(HaveOccurred())
		led2, err := e.Ledger(sc2)
		Expect(err).NotTo(HaveOccurred())
		Expect(led2.Modules["kit"].Commit).To(Equal(led.Modules["kit"].Commit))
		b, err := os.ReadFile(filepath.Join(other, ".claude", "skills", "a", "SKILL.md"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).NotTo(ContainSubstring("moved"))

		third := filepath.Join(tmp, "third")
		sc3 := engine.Scope{Kind: target.Project, ProjectDir: third}
		gone := strings.Replace(string(pinned), led.Modules["kit"].Digest, "sha256:"+strings.Repeat("b", 64), 1)
		writeTree(third, map[string]string{".lola/project.yml": gone, ".lola/profiles/team.yml": "version: 1\n"})
		for i := range view.Modules {
			view.Modules[i].Digest = "sha256:" + strings.Repeat("b", 64)
		}
		Expect(e.Trust.Save(third, view)).To(Succeed())
		activate(sc3, "project:team")
		_, err = install(sc3, engine.InstallRequest{Arg: "kit"}, engine.Options{})
		Expect(err).To(MatchError("the version of kit project.yml (base) pins (bbbbbbbbbbbb) is not in your cache"))
		Expect(errs.Hints(err)).To(Equal([]string{"lola sync -C " + third + " kit fetches it and installs it here"}))
	})

	It("a first install of a pinned module the run fetched itself, at the pin, installs it though the cache lacks it", func() {
		url, _ := served()
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + url + "}\n"})
		sync(sc)
		pinned, err := os.ReadFile(filepath.Join(proj, ".lola", "project.yml"))
		Expect(err).NotTo(HaveOccurred())
		rec, err := e.Trust.Load(proj)
		Expect(err).NotTo(HaveOccurred())
		led, err := e.Ledger(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.RemoveAll(filepath.Join(p.Store, "kit"))).To(Succeed())

		other := filepath.Join(tmp, "other")
		sc2 := engine.Scope{Kind: target.Project, ProjectDir: other}
		writeTree(other, map[string]string{".lola/project.yml": string(pinned), ".lola/profiles/team.yml": "version: 1\n"})
		view := rec.View
		view.Profiles = map[string]trust.ProfileView{"team": {}}
		Expect(e.Trust.Save(other, view)).To(Succeed())
		activate(sc2, "project:team")
		run, err := e.StageInstall()
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(run.Close()).To(Succeed()) }()
		_, err = e.InstallAll(ctx, engine.InstallSpec{Cwd: tmp, Staged: run, Modules: []engine.InstallRequest{{Arg: url}},
			Scopes: []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: sc2, Source: engine.SourceFlag}}}})
		Expect(err).NotTo(HaveOccurred())
		led2, err := e.Ledger(sc2)
		Expect(err).NotTo(HaveOccurred())
		Expect(led2.Modules["kit"].Digest).To(Equal(led.Modules["kit"].Digest))
		Expect(led2.Modules["kit"].Commit).To(Equal(led.Modules["kit"].Commit))
	})

	It("a first install of a pinned module whose cached version was tampered with is refused and records nothing", func() {
		url, move := served()
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + url + "}\n"})
		sync(sc)
		pinned, err := os.ReadFile(filepath.Join(proj, ".lola", "project.yml"))
		Expect(err).NotTo(HaveOccurred())
		rec, err := e.Trust.Load(proj)
		Expect(err).NotTo(HaveOccurred())
		led, err := e.Ledger(sc)
		Expect(err).NotTo(HaveOccurred())
		digest := led.Modules["kit"].Digest
		move()
		_, err = e.AddSource(ctx, tmp, url, "", "", nil)
		Expect(err).NotTo(HaveOccurred())
		id := strings.TrimPrefix(digest, "sha256:")[:12]
		writeTree(filepath.Join(p.Store, "kit", "versions", id, "content"), map[string]string{"skills/a/SKILL.md": "---\nname: a\ndescription: A\n---\nA planted\n"})

		other := filepath.Join(tmp, "other")
		sc2 := engine.Scope{Kind: target.Project, ProjectDir: other}
		writeTree(other, map[string]string{".lola/project.yml": string(pinned), ".lola/profiles/team.yml": "version: 1\n"})
		view := rec.View
		view.Profiles = map[string]trust.ProfileView{"team": {}}
		Expect(e.Trust.Save(other, view)).To(Succeed())
		activate(sc2, "project:team")
		_, err = install(sc2, engine.InstallRequest{Arg: "kit"}, engine.Options{})
		Expect(err).To(MatchError("cached version kit/" + id + " does not match its digest " + digest))
		Expect(errs.Hints(err)).To(Equal([]string{"remove it from your cache, then install it again from its source: lola cache rm kit"}))
		led2, err := e.Ledger(sc2)
		Expect(err).NotTo(HaveOccurred())
		Expect(led2.Modules).NotTo(HaveKey("kit"))
		Expect(filepath.Join(other, ".claude", "skills", "a")).NotTo(BeAnExistingFile())
	})

	It("a first install of a pinned module a publishing run fetched into a tampered cached version is refused and records nothing", func() {
		url, _ := served()
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + url + "}\n"})
		sync(sc)
		pinned, err := os.ReadFile(filepath.Join(proj, ".lola", "project.yml"))
		Expect(err).NotTo(HaveOccurred())
		rec, err := e.Trust.Load(proj)
		Expect(err).NotTo(HaveOccurred())
		led, err := e.Ledger(sc)
		Expect(err).NotTo(HaveOccurred())
		digest := led.Modules["kit"].Digest
		id := strings.TrimPrefix(digest, "sha256:")[:12]
		writeTree(filepath.Join(p.Store, "kit", "versions", id, "content"), map[string]string{"skills/a/SKILL.md": "---\nname: a\ndescription: A\n---\nA planted\n"})

		other := filepath.Join(tmp, "other")
		sc2 := engine.Scope{Kind: target.Project, ProjectDir: other}
		writeTree(other, map[string]string{".lola/project.yml": string(pinned), ".lola/profiles/team.yml": "version: 1\n"})
		view := rec.View
		view.Profiles = map[string]trust.ProfileView{"team": {}}
		Expect(e.Trust.Save(other, view)).To(Succeed())
		activate(sc2, "project:team")
		_, err = e.InstallAll(ctx, engine.InstallSpec{Cwd: tmp, Modules: []engine.InstallRequest{{Arg: url}},
			Scopes: []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: sc2, Source: engine.SourceFlag}}}})
		Expect(err).To(MatchError("cached version kit/" + id + " does not match its digest " + digest))
		Expect(errs.Hints(err)).To(Equal([]string{"remove it from your cache, then install it again from its source: lola cache rm kit"}))
		led2, err := e.Ledger(sc2)
		Expect(err).NotTo(HaveOccurred())
		Expect(led2.Modules).NotTo(HaveKey("kit"))
		Expect(filepath.Join(other, ".claude", "skills", "a")).NotTo(BeAnExistingFile())
		Expect(os.ReadFile(filepath.Join(other, ".lola", "project.yml"))).To(Equal(pinned))
	})

	It("--override under a layer whose entry wins does not say there is nothing to override", func() {
		kit := folder()
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\n",
			"go.yml": "version: 1\nmodules:\n  - {name: kit, source: " + kit + "}\n"})
		activate(user, "user:go")
		sync(user)
		res, err := install(user, engine.InstallRequest{Arg: "kit"}, engine.Options{Target: engine.TargetRequest{Profile: "base", Override: true}}, "skill/b")
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).NotTo(ContainElement(HaveField("Text", ContainSubstring("nothing to override"))))
	})

	It("an override into a project profile writes the installed pin into its entry", func() {
		url, move := served()
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + url + "}\n",
			".lola/profiles/team.yml": "version: 1\n"})
		activate(sc, "project:team")
		sync(sc)
		led, err := e.Ledger(sc)
		Expect(err).NotTo(HaveOccurred())
		was := *led.Modules["kit"]
		move()
		_, err = install(sc, engine.InstallRequest{Arg: "kit"}, engine.Options{Target: engine.TargetRequest{Override: true}}, "skill/b")
		Expect(err).NotTo(HaveOccurred())
		b, err := os.ReadFile(filepath.Join(proj, ".lola", "profiles", "team.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal("version: 1\nmodules:\n  - name: kit\n    source: " + url + "\n    commit: " + was.Commit + "\n    digest: " + was.Digest +
			"\n    skip: [skill/b]\n"))
	})

	It("an override of an entry without targets into a profile with other top-level targets keeps the module's assistants", func() {
		kit := folder()
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + kit + "}\n",
			"go.yml": "version: 1\ntargets: [opencode]\n"})
		activate(user, "user:go")
		sync(user)
		_, err := install(user, engine.InstallRequest{Arg: "kit"}, engine.Options{Target: engine.TargetRequest{Override: true}}, "skill/b")
		Expect(err).NotTo(HaveOccurred())
		Expect(read("go.yml")).To(Equal("version: 1\ntargets: [opencode]\nmodules:\n  - name: kit\n    source: " + kit + "\n    targets: [claude-code]\n    skip: [skill/b]\n"))
		led, err := e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Modules["kit"].InstalledTargets()).To(Equal([]string{"claude-code"}))
	})

	It("--override with no other layer to override says so", func() {
		kit := folder()
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + kit + "}\n"})
		sync(user)
		res, err := install(user, engine.InstallRequest{Arg: "kit"}, engine.Options{Target: engine.TargetRequest{Override: true}}, "skill/b")
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo,
			Text: "kit: --override has nothing to override; no other layer provides it, and this install writes default (base)"}))
		Expect(read("default.yml")).To(ContainSubstring("skip: [skill/b]"))
	})

	It("names a profile whose bare name a profile of another kind has by its qualified name", func() {
		kit := folder()
		writeTree(p.UserProfiles, map[string]string{"team.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + kit + ", skip: [skill/*]}\n"})
		writeTree(proj, map[string]string{".lola/profiles/team.yml": "version: 1\n"})
		activate(sc, "user:team")
		_, err := e.AddSource(ctx, tmp, kit, "", "", nil)
		Expect(err).NotTo(HaveOccurred())
		sel, err := config.ParseSelector("skill/a")
		Expect(err).NotTo(HaveOccurred())
		spec := engine.InstallSpec{ConfirmRemovals: confirmAll, Cwd: tmp, Modules: []engine.InstallRequest{{Arg: "kit"}},
			Scopes: []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: sc, Source: engine.SourceFlag}}}}
		Expect(spec.AddFlagEdits([]engine.SelectionEdit{{Op: engine.EditUnskip, Selector: sel}}, false)).To(Succeed())
		res, err := e.InstallAll(ctx, spec)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
			Text: "kit:skill/a stays skipped by skill/* in profile user:team; edit " + filepath.Join(p.UserProfiles, "team.yml")}))
		Expect(res.Modules).To(HaveLen(1))
		Expect(res.Modules[0].SkippedIn).To(Equal("profile user:team"))
	})

	It("names the profile file of a skip an --unskip leaves in place", func() {
		kit := folder()
		def := filepath.Join(p.UserProfiles, "default.yml")
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + kit + ", skip: [skill/*]}\n"})
		sync(user)
		sel, err := config.ParseSelector("skill/a")
		Expect(err).NotTo(HaveOccurred())
		spec := engine.InstallSpec{ConfirmRemovals: confirmAll, Cwd: tmp, Modules: []engine.InstallRequest{{Arg: "kit"}},
			Scopes: []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: user, Source: engine.SourceFlag}}}}
		Expect(spec.AddFlagEdits([]engine.SelectionEdit{{Op: engine.EditUnskip, Selector: sel}}, false)).To(Succeed())
		res, err := e.InstallAll(ctx, spec)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
			Text: "kit:skill/a stays skipped by skill/* in default (base); edit " + def}))
		Expect(read("default.yml")).To(ContainSubstring("skip: [skill/*]"))
	})
})
