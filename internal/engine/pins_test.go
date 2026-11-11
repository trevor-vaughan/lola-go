package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"

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

var _ = Describe("pins.yaml", func() {
	var (
		tmp, url, work, bare string
		p                    paths.Paths
		e                    *engine.Engine
		user                 engine.Scope
		ctx                  = context.Background()
	)
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p, targets.Builtin())
		e.Getwd = func() (string, error) { return tmp, nil }
		user = engine.Scope{Kind: target.User}
		work, bare = filepath.Join(tmp, "work"), filepath.Join(tmp, "srv", "kit.git")
		writeTree(work, map[string]string{"lola.yml": "name: kit\n", "commands/kit.md": "one\n"})
		gitIn(work, "init", "-q", "-b", "main")
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "one")
		gitIn(tmp, "clone", "-q", "--bare", work, bare)
		url = testenv.ServeGit(filepath.Join(tmp, "srv")) + "/kit.git"
	})
	// advance commits a new version of kit and pushes it, so main serves it.
	advance := func(text string) {
		GinkgoHelper()
		writeTree(work, map[string]string{"commands/kit.md": text + "\n"})
		gitIn(work, "commit", "-q", "-am", text)
		gitIn(work, "push", "-q", bare, "main")
	}
	sync := func(update bool) *engine.SyncResult {
		GinkgoHelper()
		res, err := e.Sync(ctx, engine.SyncRequest{Scope: user, Update: update, ConfirmRemovals: func([]engine.Removal) (bool, error) { return true, nil }})
		Expect(err).NotTo(HaveOccurred())
		return res
	}
	pinsFile := func() string {
		GinkgoHelper()
		b, err := os.ReadFile(p.Pins)
		Expect(err).NotTo(HaveOccurred())
		return string(b)
	}
	installed := func() string {
		GinkgoHelper()
		b, err := os.ReadFile(filepath.Join(p.Home, ".claude", "commands", "kit.md"))
		Expect(err).NotTo(HaveOccurred())
		return string(b)
	}
	// pinLine is the line pins.yaml holds for kit's entry of ref at the version lock.json records.
	pinLine := func(ref string) string {
		GinkgoHelper()
		led, err := e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		in := led.Modules["kit"]
		return "    - {source: '" + url + "', ref: " + ref + ", commit: " + in.Commit + ", digest: '" + in.Digest + "'}\n"
	}
	const kitMain = "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: "

	It("records the version installed from your profiles, and a fresh lock.json installs it again", func() {
		writeTree(p.UserProfiles, map[string]string{"default.yml": kitMain + url + ", ref: main}\n"})
		sync(false)
		line := pinLine("main")
		Expect(pinsFile()).To(HaveSuffix("version: 1\nmodules:\n  kit:\n" + line))

		advance("two")
		Expect(os.Remove(p.UserLedger)).To(Succeed()) // a new machine: no lock.json
		Expect(os.RemoveAll(filepath.Join(p.Home, ".claude"))).To(Succeed())
		sync(false)
		Expect(installed()).To(Equal("one\n"), "pins.yaml's version, not what main serves now")

		sync(true)
		Expect(installed()).To(Equal("two\n"))
		Expect(pinsFile()).NotTo(ContainSubstring(line), "lola update moves the pin")
		Expect(pinsFile()).To(ContainSubstring(pinLine("main")))
	})

	// switchTo turns name on at user scope as lola profile --switch -y does.
	switchTo := func(name string) *engine.SyncResult {
		GinkgoHelper()
		res, err := e.Sync(ctx, engine.SyncRequest{Scope: user, Profiles: &engine.ProfileChange{Action: engine.ProfileSwitch, Name: name},
			ConfirmRemovals: func([]engine.Removal) (bool, error) { return true, nil }})
		Expect(err).NotTo(HaveOccurred())
		return res
	}
	short := func(commit string) string { return commit[:12] }

	It("names the version an entry of yours keeps through its pin when a switch moves it to another layer, never its source", func() {
		writeTree(p.UserProfiles, map[string]string{"default.yml": kitMain + url + "}\n", "go.yml": kitMain + url + "}\n"})
		sync(false)
		led, err := e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		was := short(led.Modules["kit"].Commit)
		advance("two")
		res := switchTo("go")
		Expect(res.Activation.Changes).To(ConsistOf(And(HaveField("Module", "kit"), HaveField("Action", engine.ChangeReplace),
			HaveField("Version", was), HaveField("Was", was))), "the pin keeps the installed commit; main's new one is not installed")
		Expect(installed()).To(Equal("one\n"))
	})

	It("names pins.yaml's newer pin as the version a switch installs", func() {
		writeTree(p.UserProfiles, map[string]string{"default.yml": kitMain + url + "}\n", "go.yml": kitMain + url + "}\n"})
		sync(false)
		older := pinsFile()
		led, err := e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		first := short(led.Modules["kit"].Commit)
		advance("two")
		sync(true)
		newer := pinsFile()
		led, err = e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		second := short(led.Modules["kit"].Commit)
		Expect(os.WriteFile(p.Pins, []byte(older), 0o600)).To(Succeed()) // another machine's file, synced over
		res := switchTo("go")
		Expect(res.Activation.Changes).To(ConsistOf(And(HaveField("Module", "kit"), HaveField("Version", first), HaveField("Was", second))))
		Expect(installed()).To(Equal("one\n"), "pins.yaml's version is the one installed")
		Expect(newer).NotTo(Equal(older))
	})

	It("names the installed commit through lock.json alone when pins.yaml is gone", func() {
		writeTree(p.UserProfiles, map[string]string{"default.yml": kitMain + url + "}\n", "go.yml": kitMain + url + "}\n"})
		sync(false)
		led, err := e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		was := short(led.Modules["kit"].Commit)
		advance("two")
		Expect(os.Remove(p.Pins)).To(Succeed())
		res := switchTo("go")
		Expect(res.Activation.Changes).To(ConsistOf(And(HaveField("Module", "kit"), HaveField("Version", was), HaveField("Was", was))))
		Expect(installed()).To(Equal("one\n"))
	})

	It("names the commit --off keeps, never the source", func() {
		writeTree(p.UserProfiles, map[string]string{"default.yml": kitMain + url + "}\n", "go.yml": kitMain + url + "}\n"})
		sync(false)
		led, err := e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		was := short(led.Modules["kit"].Commit)
		advance("two")
		switchTo("go")
		res, err := e.Sync(ctx, engine.SyncRequest{Scope: user, Profiles: &engine.ProfileChange{Action: engine.ProfileOff, Name: "go"},
			ConfirmRemovals: func([]engine.Removal) (bool, error) { return true, nil }})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Activation.Changes).To(ConsistOf(And(HaveField("Module", "kit"), HaveField("Version", was), HaveField("Was", was))))
		Expect(installed()).To(Equal("one\n"))
	})

	It("shows commits, not the unchanged ref, when pins.yaml's pin moves a ref'd entry", func() {
		writeTree(p.UserProfiles, map[string]string{"default.yml": kitMain + url + ", ref: main}\n", "go.yml": kitMain + url + ", ref: main}\n"})
		sync(false)
		older := pinsFile()
		led, err := e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		first := short(led.Modules["kit"].Commit)
		advance("two")
		sync(true)
		led, err = e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		second := short(led.Modules["kit"].Commit)
		Expect(os.WriteFile(p.Pins, []byte(older), 0o600)).To(Succeed())
		res := switchTo("go")
		Expect(res.Activation.Changes).To(ConsistOf(And(HaveField("Module", "kit"), HaveField("Version", first), HaveField("Was", second))))
	})

	It("prefers pins.yaml to lock.json, and a newer synced pin is a change the next sync applies", func() {
		writeTree(p.UserProfiles, map[string]string{"default.yml": kitMain + url + ", ref: main}\n"})
		sync(false)
		before := pinsFile()
		advance("two")
		sync(true)
		after := pinsFile()
		Expect(os.WriteFile(p.Pins, []byte(before), 0o600)).To(Succeed()) // an older machine's file, synced over
		res := sync(false)
		Expect(installed()).To(Equal("one\n"))
		Expect(res.Steps).NotTo(BeEmpty(), "the synced pin differs from what is installed")
		Expect(os.WriteFile(p.Pins, []byte(after), 0o600)).To(Succeed())
		sync(false)
		Expect(installed()).To(Equal("two\n"))
	})

	It("names pins.yaml when the version it pins is not what the source serves, and the hinted --update re-pins it", func() {
		writeTree(p.UserProfiles, map[string]string{"default.yml": kitMain + url + ", ref: main}\n"})
		sync(false)
		line := pinLine("main")
		led, err := e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		forged := "sha256:" + strings.Repeat("0", 64)
		Expect(os.WriteFile(p.Pins, []byte(strings.Replace(pinsFile(), led.Modules["kit"].Digest, forged, 1)), 0o600)).To(Succeed())
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: user})
		Expect(err).To(MatchError(HavePrefix("kit: the source now serves different content than pins.yaml pins (pinned " + forged + ", got ")))
		Expect(errs.Hints(err)).To(HaveExactElements("if you expect the change, re-pin it: lola sync --user --update kit",
			"otherwise the source changed under the same ref or commit: check it before re-pinning"))

		_, err = e.Sync(ctx, engine.SyncRequest{Scope: user, Modules: []string{"kit"}, Update: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(pinsFile()).To(HaveSuffix("  kit:\n" + line))
	})

	It("refuses to write over a pins.yaml another writer changed after the sync read it to plan, keeping that writer's bytes", func() {
		folder := filepath.Join(tmp, "mods", "local")
		writeTree(folder, map[string]string{"lola.yml": "name: local\n", "commands/local.md": "L\n"})
		writeTree(p.UserProfiles, map[string]string{"default.yml": kitMain + url + ", ref: main}\n  - {name: local, source: " + folder + "}\n"})
		sync(false)
		gitIn(work, "tag", "v2")
		gitIn(work, "push", "-q", bare, "v2")
		theirs := "# synced from the laptop\n" + pinsFile()
		writeTree(p.UserProfiles, map[string]string{"default.yml": kitMain + url + ", ref: v2}\n"}) // a pin to write, and a removal to confirm
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: user, ConfirmRemovals: func([]engine.Removal) (bool, error) {
			// A dotfile manager syncs pins.yaml in while the run is between its plan and its write.
			return true, os.WriteFile(p.Pins, []byte(theirs), 0o600)
		}})
		Expect(err).To(MatchError(p.Pins + " changed while lola was reading it; nothing was written"))
		Expect(errs.Hints(err)).To(HaveExactElements("run the same command again"))
		Expect(pinsFile()).To(Equal(theirs))
		Expect(filepath.Join(p.Home, ".claude", "commands", "local.md")).To(BeARegularFile(), "the apply is rolled back")
	})

	It("a newer synced pin survives lola install other --user", func() {
		writeTree(p.UserProfiles, map[string]string{"default.yml": kitMain + url + ", ref: main}\n"})
		sync(false)
		before := pinsFile()
		advance("two")
		sync(true)
		after := pinsFile()
		Expect(os.WriteFile(p.Pins, []byte(before), 0o600)).To(Succeed())
		sync(false)
		Expect(installed()).To(Equal("one\n"))
		Expect(os.WriteFile(p.Pins, []byte(after), 0o600)).To(Succeed()) // another machine's newer pin, synced over

		other := filepath.Join(tmp, "mods", "other")
		writeTree(other, map[string]string{"lola.yml": "name: other\n", "commands/other.md": "O\n"})
		_, err := e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: other}}, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(installed()).To(Equal("one\n"), "an install of another module leaves kit as installed")
		Expect(pinsFile()).To(Equal(after), "and keeps the synced pin, which the next sync applies")
		sync(false)
		Expect(installed()).To(Equal("two\n"))
	})

	It("a newer synced pin survives lola install kit -t <t> --user and lola uninstall kit -t <t> --user, which keep the version installed", func() {
		writeTree(p.UserProfiles, map[string]string{"default.yml": kitMain + url + ", ref: main}\n"})
		sync(false)
		before := pinsFile()
		advance("two")
		sync(true)
		after := pinsFile()
		Expect(os.WriteFile(p.Pins, []byte(before), 0o600)).To(Succeed())
		sync(false)
		Expect(installed()).To(Equal("one\n"))
		Expect(os.WriteFile(p.Pins, []byte(after), 0o600)).To(Succeed()) // another machine's newer pin, synced over

		_, err := e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: "kit", Targets: []string{"gemini-cli"}}}, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(installed()).To(Equal("one\n"), "adding an assistant never upgrades")
		Expect(pinsFile()).To(Equal(after), "so the synced pin is kept as read")
		_, err = e.Uninstall(ctx, user, []string{"kit"}, engine.Options{Targets: []string{"gemini-cli"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(pinsFile()).To(Equal(after), "nor does removing one")
		sync(false)
		Expect(installed()).To(Equal("two\n"), "the next sync applies it")
	})

	It("drops the pins of keys no file of yours lists, keeps two pins of one module, and never pins a folder", func() {
		folder := filepath.Join(tmp, "mods", "local")
		writeTree(folder, map[string]string{"lola.yml": "name: local\n", "commands/local.md": "L\n"})
		writeTree(p.UserProfiles, map[string]string{
			"default.yml": kitMain + url + ", ref: main}\n  - {name: local, source: " + folder + "}\n",
			"v2.yml":      kitMain + url + ", ref: v2}\n"})
		gitIn(work, "tag", "v2")
		gitIn(work, "push", "-q", bare, "v2")
		sync(false)
		main := pinLine("main")
		led, err := e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		led.Profile = &ledger.Active{Name: "user:v2", By: ledger.ByOn}
		Expect(led.Save(p.UserLedger)).To(Succeed())
		sync(false)
		Expect(pinsFile()).To(ContainSubstring("  kit:\n"+main+pinLine("v2")), "main's pin and v2's")
		Expect(pinsFile()).NotTo(ContainSubstring(folder))

		Expect(os.Remove(filepath.Join(p.UserProfiles, "v2.yml"))).To(Succeed())
		_, err = e.Uninstall(ctx, user, []string{"local"}, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.Count(pinsFile(), "source: '"+url+"'")).To(Equal(1), "v2's key went with v2.yml")
		Expect(pinsFile()).To(ContainSubstring("  kit:\n" + main))
	})

	It("keeps every pin while a profile of yours does not load, with a warning, and drops the stale one once it loads", func() {
		writeTree(p.UserProfiles, map[string]string{"default.yml": kitMain + url + ", ref: main}\n"})
		sync(false)
		main := pinLine("main")
		stale := strings.Replace(main, "ref: main", "ref: gone", 1)
		Expect(os.WriteFile(p.Pins, []byte(pinsFile()+stale), 0o600)).To(Succeed())
		writeTree(p.UserProfiles, map[string]string{"broken.yml": "version: 1\nmodules: [\n"})
		res := sync(false)
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn, Text: filepath.Join(p.UserProfiles, "broken.yml") + " does not load, so pins.yaml keeps every pin it holds"}))
		Expect(pinsFile()).To(HaveSuffix("  kit:\n" + main + stale))

		Expect(os.Remove(filepath.Join(p.UserProfiles, "broken.yml"))).To(Succeed())
		res = sync(false)
		Expect(res.Notices).NotTo(ContainElement(HaveField("Level", engine.LevelWarn)))
		Expect(pinsFile()).To(HaveSuffix("  kit:\n" + main))
	})

	It("writes pins.yaml in the user-scope apply, through its link, only when its bytes change, and never under -n", func() {
		if runtime.GOOS == "windows" {
			Skip("symbolic links need privileges on Windows")
		}
		writeTree(p.UserProfiles, map[string]string{"default.yml": kitMain + url + ", ref: main}\n"})
		dotfiles := filepath.Join(tmp, "dotfiles", "pins.yaml")
		writeTree(filepath.Dir(dotfiles), map[string]string{"pins.yaml": "# synced\n\n# by chezmoi\nversion: 1\n"})
		Expect(os.MkdirAll(filepath.Dir(p.Pins), 0o700)).To(Succeed())
		Expect(os.Symlink(dotfiles, p.Pins)).To(Succeed())
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: user, Options: engine.Options{DryRun: true}})
		Expect(err).NotTo(HaveOccurred())
		Expect(os.ReadFile(dotfiles)).To(Equal([]byte("# synced\n\n# by chezmoi\nversion: 1\n")), "-n writes nothing")
		sync(false)
		fi, err := os.Lstat(p.Pins)
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode() & os.ModeSymlink).NotTo(BeZero())
		b, err := os.ReadFile(dotfiles)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal("# synced\n\n# by chezmoi\nversion: 1\nmodules:\n  kit:\n" + pinLine("main")))
		st, err := os.Stat(dotfiles)
		Expect(err).NotTo(HaveOccurred())
		sync(false)
		again, err := os.Stat(dotfiles)
		Expect(err).NotTo(HaveOccurred())
		Expect(again.ModTime()).To(Equal(st.ModTime()), "an unchanged file is not rewritten")
	})

	It("a project-only run never writes pins.yaml, and a user-scope run that pins nothing creates none", func() {
		proj := filepath.Join(tmp, "proj")
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + url + "}\n"})
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: engine.Scope{Kind: target.Project, ProjectDir: proj},
			Trust: func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }})
		Expect(err).NotTo(HaveOccurred())
		Expect(p.Pins).NotTo(BeAnExistingFile())

		folder := filepath.Join(tmp, "mods", "local")
		writeTree(folder, map[string]string{"lola.yml": "name: local\n", "commands/local.md": "L\n"})
		archive := filepath.Join(tmp, "mods", "packed-1.0.tar.gz")
		Expect(os.WriteFile(archive, tarGz("packed", map[string]string{"lola.yml": "name: packed\n", "commands/packed.md": "P\n"}), 0o600)).To(Succeed())
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: local, source: " + folder + "}\n" +
			"  - {name: packed, source: " + archive + "}\n"})
		sync(false)
		led, err := e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Modules["packed"].Digest).NotTo(BeEmpty(), "lock.json records the archive's version on this machine")
		Expect(p.Pins).NotTo(BeAnExistingFile(), "a folder or an archive on this machine is never pinned, and an empty pins.yaml is never created")
	})
})
