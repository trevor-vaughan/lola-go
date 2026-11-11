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
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("edits", func() {
	var (
		tmp, proj string
		p         paths.Paths
		e         *engine.Engine
		user, sc  engine.Scope
		ctx       = context.Background()
		claude    = engine.Options{Targets: []string{"claude-code"}}
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
	// repo serves a git repository holding the module kit and returns its URL and work tree;
	// advance commits a change there and pushes it, moving main.
	repo := func() (string, string) {
		GinkgoHelper()
		work := filepath.Join(tmp, "work")
		writeTree(work, map[string]string{"lola.yml": "name: kit\n", "commands/kit.md": "kit\n"})
		gitIn(work, "init", "-q", "-b", "main")
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "one")
		gitIn(tmp, "clone", "-q", "--bare", work, filepath.Join(tmp, "srv", "kit.git"))
		return testenv.ServeGit(filepath.Join(tmp, "srv")) + "/kit.git", work
	}
	advance := func(work string) {
		GinkgoHelper()
		writeTree(work, map[string]string{"commands/kit.md": "kit 2\n"})
		gitIn(work, "commit", "-q", "-am", "two")
		gitIn(work, "push", "-q", filepath.Join(tmp, "srv", "kit.git"), "main")
	}
	mod := func(name string) string {
		GinkgoHelper()
		dir := filepath.Join(tmp, "mods", name)
		writeTree(dir, map[string]string{"lola.yml": "name: " + name + "\n", "commands/" + name + ".md": name + "\n"})
		return dir
	}

	It("keeps the mode of a file of yours it rewrites, through a symlink too", func() {
		kit := mod("kit")
		dotfiles := filepath.Join(tmp, "dotfiles")
		writeTree(dotfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\n", "go.yml": "version: 1\n"})
		Expect(os.Chmod(filepath.Join(dotfiles, "default.yml"), 0o644)).To(Succeed())
		Expect(os.Chmod(filepath.Join(dotfiles, "go.yml"), 0o640)).To(Succeed())
		Expect(os.MkdirAll(p.UserProfiles, 0o700)).To(Succeed())
		Expect(os.Symlink(filepath.Join(dotfiles, "default.yml"), filepath.Join(p.UserProfiles, "default.yml"))).To(Succeed())
		Expect(os.Symlink(filepath.Join(dotfiles, "go.yml"), filepath.Join(p.UserProfiles, "go.yml"))).To(Succeed())
		mode := func(path string) os.FileMode {
			GinkgoHelper()
			fi, err := os.Stat(path)
			Expect(err).NotTo(HaveOccurred())
			return fi.Mode().Perm()
		}

		_, err := e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: kit}}, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(os.ReadFile(filepath.Join(dotfiles, "default.yml"))).To(ContainSubstring("name: kit"))
		Expect(mode(filepath.Join(dotfiles, "default.yml"))).To(Equal(os.FileMode(0o644)))
		_, err = e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: "kit"}}, engine.Options{Target: engine.TargetRequest{Profile: "go"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(os.ReadFile(filepath.Join(dotfiles, "go.yml"))).To(ContainSubstring("name: kit"))
		Expect(mode(filepath.Join(dotfiles, "go.yml"))).To(Equal(os.FileMode(0o640)))
	})

	It("refuses the write of a profile edited by hand while the run applies, rolling back lock.json and the files it rendered", func() {
		a, kit := mod("a"), mod("kit")
		goYML := filepath.Join(p.UserProfiles, "go.yml")
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\n", "go.yml": "version: 1\nmodules:\n  - {name: a, source: " + a + "}\n"})
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: user, Profiles: &engine.ProfileChange{Action: engine.ProfileOn, Name: "go"}})
		Expect(err).NotTo(HaveOccurred())
		lock, err := os.ReadFile(p.UserLedger)
		Expect(err).NotTo(HaveOccurred())
		command := func(name string) string { return filepath.Join(p.Home, ".claude", "commands", name+".md") }
		Expect(command("a")).To(BeARegularFile())

		// A --pre-install script runs after the run edited the profile in memory and before it applies.
		edit := filepath.Join(tmp, "bin", "edit.sh")
		writeTree(filepath.Dir(edit), map[string]string{"edit.sh": "#!/bin/sh\nprintf '# mine\\n' >> '" + goYML + "'\n"})
		Expect(os.Chmod(edit, 0o755)).To(Succeed())
		edited := "version: 1\nmodules:\n  - {name: a, source: " + a + "}\n# mine\n"
		_, err = e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: kit}}, engine.Options{PreInstall: engine.FlagScript{Given: edit, Path: edit}})
		Expect(err).To(MatchError(ContainSubstring(goYML + " changed while lola was reading it; nothing was written")))
		Expect(errs.Hints(err)).To(ContainElement("run the same command again"))
		Expect(os.ReadFile(goYML)).To(Equal([]byte(edited)), "the hand edit stays")
		Expect(os.ReadFile(p.UserLedger)).To(Equal(lock))
		Expect(command("kit")).NotTo(BeAnExistingFile())
		Expect(command("a")).To(BeARegularFile())
	})

	It("names each write: the file, its kind and path, whether lola created it, and whether git tracks it", func() {
		kit := mod("kit")
		def := filepath.Join(p.UserProfiles, "default.yml")
		res, err := e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: kit}}, claude)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Edits).To(Equal([]engine.Edit{{Module: "kit", Action: "added", Profile: "default", Kind: "base", Path: def, Scope: "user",
			Label: "default (base)", Created: true}}))
		res, err = e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: "kit"}}, engine.Options{Targets: []string{"claude-code", "opencode"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Edits).To(ConsistOf(HaveField("Action", "changed")))

		gitIn(proj, "init", "-q", "-b", "main")
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\n"})
		gitIn(proj, "add", ".lola/project.yml")
		gitIn(proj, "commit", "-q", "-m", "lola")
		res, err = e.Install(ctx, sc, tmp, []engine.InstallRequest{{Arg: kit}}, claude)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Edits).To(Equal([]engine.Edit{{Module: "kit", Action: "added", Profile: "project.yml", Kind: "base",
			Path: filepath.Join(proj, ".lola", "project.yml"), Scope: "project", Label: "project.yml (base)", Shared: true}}))

		un, err := e.Uninstall(ctx, sc, []string{"kit"}, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(un.Edits).To(ConsistOf(And(HaveField("Action", "removed"), HaveField("Shared", true))))

		dry, err := e.Install(ctx, sc, tmp, []engine.InstallRequest{{Arg: kit}}, engine.Options{Targets: []string{"claude-code"}, DryRun: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(dry.Edits).To(ConsistOf(HaveField("Action", "added")), "-n names what it would write")
	})

	It("says a profile of yours written in one scope is also on in the other", func() {
		kit := mod("kit")
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\n"})
		for _, s := range []struct {
			sc   engine.Scope
			path string
		}{{user, p.UserLedger}, {sc, filepath.Join(proj, ".lola", "lock.json")}} {
			led := ledger.New()
			led.Profile = &ledger.Active{Name: "user:go", By: ledger.ByOn}
			Expect(led.Save(s.path)).To(Succeed())
		}
		res, err := e.Install(ctx, sc, tmp, []engine.InstallRequest{{Arg: kit}}, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "go is also on at user scope → lola sync --user"}))
		res, err = e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: "kit"}}, engine.Options{Targets: []string{"claude-code", "opencode"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "go is also on in this project → lola sync --project"}))
	})

	It("names no write for an entry the file already holds as written", func() {
		kit := mod("kit")
		def := filepath.Join(p.UserProfiles, "default.yml")
		_, err := e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: kit}}, claude)
		Expect(err).NotTo(HaveOccurred())
		was, err := os.ReadFile(def)
		Expect(err).NotTo(HaveOccurred())
		res, err := e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: "kit"}}, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Edits).To(Equal([]engine.Edit{}))
		Expect(os.ReadFile(def)).To(Equal(was), "the file is as it was")

		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - name: kit\n    source: " + kit +
			"\n    targets: [claude-code]\n"})
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: user})
		Expect(err).NotTo(HaveOccurred())
		was, err = os.ReadFile(def)
		Expect(err).NotTo(HaveOccurred())
		_, err = e.Uninstall(ctx, user, []string{"kit"}, engine.Options{Targets: []string{"opencode"}})
		Expect(err).To(MatchError("kit is not installed on opencode; it is installed on claude-code"), "-t naming a target the entry does not have leaves it as it was")
		Expect(os.ReadFile(def)).To(Equal(was))
	})

	It("says changed for a project reinstall that moves the pin", func() {
		url, work := repo()
		_, err := e.Install(ctx, sc, tmp, []engine.InstallRequest{{Arg: url, Ref: "main"}}, claude)
		Expect(err).NotTo(HaveOccurred())
		advance(work)
		res, err := e.Install(ctx, sc, tmp, []engine.InstallRequest{{Arg: url, Ref: "main"}}, claude)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Edits).To(Equal([]engine.Edit{{Module: "kit", Action: "changed", Profile: "project.yml", Kind: "base",
			Path: filepath.Join(proj, ".lola", "project.yml"), Scope: "project", Label: "project.yml (base)"}}))
	})

	It("names the re-pins a sync writes into project.yml, and none for a sync that writes nothing", func() {
		url, work := repo()
		_, err := e.Install(ctx, sc, tmp, []engine.InstallRequest{{Arg: url, Ref: "main"}}, claude)
		Expect(err).NotTo(HaveOccurred())
		trustAll := func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }
		res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Edits).To(Equal([]engine.Edit{}), "in sync: nothing written")
		advance(work)
		res, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Update: true, Refetch: true, Trust: trustAll})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Edits).To(Equal([]engine.Edit{{Module: "kit", Action: "changed", Profile: "project.yml", Kind: "base",
			Path: filepath.Join(proj, ".lola", "project.yml"), Scope: "project", Label: "project.yml (base)"}}))
	})

	It("names the re-pin a sync writes into a project profile as changed", func() {
		url, work := repo()
		team := filepath.Join(proj, ".lola", "profiles", "team.yml")
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\n",
			".lola/profiles/team.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + url + ", ref: main}\n"})
		trustAll := func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOn, Name: "team"}, Trust: trustAll})
		Expect(err).NotTo(HaveOccurred())
		advance(work)
		res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Update: true, Refetch: true, Trust: trustAll})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Edits).To(Equal([]engine.Edit{{Module: "kit", Action: "changed", Profile: "project:team", Kind: "project",
			Path: team, Scope: "project", Label: "profile team"}}))
	})

	It("says a file git does not track is not shared, in a git work tree", func() {
		gitIn(proj, "init", "-q", "-b", "main")
		res, err := e.Install(ctx, sc, tmp, []engine.InstallRequest{{Arg: mod("kit")}}, claude)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Edits).To(ConsistOf(And(HaveField("Profile", "project.yml"), HaveField("Shared", false))))
	})
})
