package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("user scope's base", func() {
	var (
		tmp      string
		p        paths.Paths
		e        *engine.Engine
		user     engine.Scope
		def      string
		ctx      = context.Background()
		claude   = engine.Options{Targets: []string{"claude-code"}}
		removals [][]engine.Removal
	)
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		e.Getwd = func() (string, error) { return tmp, nil }
		user, def = engine.Scope{Kind: target.User}, filepath.Join(p.UserProfiles, "default.yml")
		removals = nil
	})
	mod := func(name string) string {
		GinkgoHelper()
		dir := filepath.Join(tmp, "mods", name)
		writeTree(dir, map[string]string{"lola.yml": "name: " + name + "\n", "commands/" + name + ".md": name + "\n",
			"skills/a/SKILL.md": "---\nname: a\ndescription: A\n---\nA\n", "skills/b/SKILL.md": "---\nname: b\ndescription: B\n---\nB\n"})
		return dir
	}
	// served is a git module served over loopback HTTP at tag v1: its URL.
	served := func(name string) string {
		GinkgoHelper()
		work, bare := filepath.Join(tmp, "work", name), filepath.Join(tmp, "srv", name+".git")
		writeTree(work, map[string]string{"lola.yml": "name: " + name + "\n", "commands/" + name + ".md": name + "\n"})
		gitIn(work, "init", "-q", "-b", "main")
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "v1")
		gitIn(work, "tag", "v1")
		gitIn(tmp, "clone", "-q", "--bare", work, bare)
		return testenv.ServeGit(filepath.Join(tmp, "srv")) + "/" + name + ".git"
	}
	sync := func() (*engine.SyncResult, error) {
		return e.Sync(ctx, engine.SyncRequest{Scope: user, ConfirmRemovals: func(r []engine.Removal) (bool, error) {
			removals = append(removals, r)
			return true, nil
		}})
	}
	installed := func() map[string]string {
		GinkgoHelper()
		led, err := e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		out := map[string]string{}
		for name, ent := range led.Modules {
			out[name] = ent.From
		}
		return out
	}

	It("an install at user scope writes default.yml through its link, never a pin", func() {
		if runtime.GOOS == "windows" {
			Skip("symbolic links need privileges on Windows")
		}
		url := served("kit")
		dotfiles := filepath.Join(tmp, "dotfiles", "default.yml")
		writeTree(filepath.Dir(dotfiles), map[string]string{"default.yml": "version: 1 # synced by stow\ntargets: [claude-code]\n"})
		Expect(os.MkdirAll(p.UserProfiles, 0o700)).To(Succeed())
		Expect(os.Symlink(dotfiles, def)).To(Succeed())

		_, err := e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: url, Ref: "v1"}}, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		fi, err := os.Lstat(def)
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode()&os.ModeSymlink).NotTo(BeZero(), "the link stays a link")
		Expect(os.ReadFile(dotfiles)).To(Equal([]byte("version: 1 # synced by stow\ntargets: [claude-code]\nmodules:\n"+
			"  - name: kit\n    source: "+url+"\n    ref: v1\n")), "no commit or digest is written into your profile")
		led, err := e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Modules["kit"].From).To(Equal("profile:default"))
		Expect(led.Modules["kit"].Commit).NotTo(BeEmpty(), "lock.json records the version installed")

		res, err := sync()
		Expect(err).NotTo(HaveOccurred())
		Expect(res.InSync).To(BeTrue(), "the next sync installs the version lock.json records")
	})

	It("install at user scope saves its picks in default's entry", func() {
		kit := mod("kit")
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\n"})
		spec := engine.InstallSpec{ConfirmRemovals: confirmAll, Cwd: tmp, Modules: []engine.InstallRequest{{Arg: kit}},
			Scopes: []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: user, Source: engine.SourceFlag}}}}
		Expect(spec.AddFlagEdits([]engine.SelectionEdit{skipEdit("skill/b")}, false)).To(Succeed())
		res, err := e.InstallAll(ctx, spec)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.ReadFile(def)).To(ContainSubstring("    skip: [skill/b]\n"))
		Expect(res.Modules).To(ConsistOf(HaveField("SavedTo", def)))
		Expect(p.Selections).NotTo(BeAnExistingFile(), "selections.yaml holds no user-scope picks")
		Expect(filepath.Join(p.Home, ".claude", "skills", "b")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(p.Home, ".claude", "skills", "a", "SKILL.md")).To(BeARegularFile())
	})

	It("an install at user scope keeps default.yml's relative source as written", func() {
		mod("kit")
		Expect(filepath.Join(p.UserProfiles, "..", "..", "..", "mods", "kit")).To(BeADirectory(), "the fixture's relative source names the module")
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - name: kit\n    source: ../../../mods/kit\n"})
		_, err := sync()
		Expect(err).NotTo(HaveOccurred())
		spec := engine.InstallSpec{ConfirmRemovals: confirmAll, Cwd: tmp, Modules: []engine.InstallRequest{{Arg: "kit"}},
			Scopes: []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: user, Source: engine.SourceFlag}}}}
		Expect(spec.AddFlagEdits([]engine.SelectionEdit{skipEdit("skill/b")}, false)).To(Succeed())
		_, err = e.InstallAll(ctx, spec)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.ReadFile(def)).To(Equal([]byte("version: 1\ntargets: [claude-code]\nmodules:\n  - name: kit\n    source: ../../../mods/kit\n"+
			"    skip: [skill/b]\n")), "the source as the file holds it, never made absolute")
	})

	It("a user.yml on disk changes nothing", func() {
		kit := mod("kit")
		writeTree(filepath.Join(p.ConfigHome, "lola"), map[string]string{"user.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + kit + "}\n"})
		_, err := sync()
		Expect(err).To(MatchError(HavePrefix("no default.yml at "+def)), "nothing to sync: user.yml is not read")
		rows, _, err := e.Status(ctx, user, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(BeEmpty())
	})

	It("an old lock.json's entries from user.yml are offered for removal by the next sync", func() {
		kit := mod("kit")
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + kit + "}\n"})
		_, err := sync()
		Expect(err).NotTo(HaveOccurred())
		led, err := e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		led.Modules["kit"].From = "" // what an earlier build recorded for user.yml's entry
		Expect(led.Save(p.UserLedger)).To(Succeed())
		Expect(os.Remove(def)).To(Succeed())
		_, err = sync()
		Expect(err).NotTo(HaveOccurred())
		Expect(removals).To(ConsistOf(ConsistOf(And(HaveField("Module", "kit"), HaveField("WholeModule", true)))))
		Expect(installed()).To(BeEmpty())
	})

	It("uninstall at user scope removes the entry from default.yml", func() {
		kit, other := mod("kit"), filepath.Join(tmp, "mods", "other")
		writeTree(other, map[string]string{"lola.yml": "name: other\n", "commands/other.md": "other\n"}) // no skills of kit's names
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n" +
			"  # mine\n  - {name: kit, source: " + kit + "}\n  - {name: other, source: " + other + "}\n"})
		_, err := sync()
		Expect(err).NotTo(HaveOccurred())
		_, err = e.Uninstall(ctx, user, []string{"kit"}, claude)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.ReadFile(def)).To(Equal([]byte("version: 1\ntargets: [claude-code]\nmodules:\n  - {name: other, source: " + other + "}\n")))
		Expect(installed()).To(Equal(map[string]string{"other": "profile:default"}))
		Expect(filepath.Join(p.Home, ".claude", "commands", "kit.md")).NotTo(BeAnExistingFile())
	})

	It("names user scope's base default in its context", func() {
		base := engine.ContextLayer{Name: "default", Kind: "base", Path: def, Label: "default"}
		Expect(e.Context(user, nil).Base).To(Equal(base))
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		led := ledger.New()
		led.Profile = &ledger.Active{Name: "user:go", By: ledger.ByOn}
		Expect(led.Save(p.UserLedger)).To(Succeed())
		Expect(e.Context(user, nil).Base).To(Equal(base), "the base stays default with a profile on")
	})
})

// skipEdit is the --skip flag edit of selector s.
func skipEdit(s string) engine.SelectionEdit {
	GinkgoHelper()
	sel, err := config.ParseSelector(s)
	Expect(err).NotTo(HaveOccurred())
	return engine.SelectionEdit{Op: engine.EditSkip, Selector: sel}
}
