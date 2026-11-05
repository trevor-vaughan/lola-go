package engine_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// Spec 2026-10-29 §16.2: a local entry inside a project never becomes yours unreviewed.
var _ = Describe("local entries inside a project", func() {
	var (
		tmp, proj, mods string
		p               paths.Paths
		e               *engine.Engine
		user, sc        engine.Scope
		ctx             = context.Background()
		yes             = func([]engine.Removal) (bool, error) { return true, nil }
		trustAll        = func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }
	)
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		proj = filepath.Join(tmp, "proj")
		mods = filepath.Join(proj, "mods")
		e.Getwd = func() (string, error) { return proj, nil }
		user, sc = engine.Scope{Kind: target.User}, engine.Scope{Kind: target.Project, ProjectDir: proj}
		writeTree(mods, map[string]string{"notes/lola.yml": "name: notes\n", "notes/commands/notes.md": "one\n"})
		writeTree(p.UserProfiles, map[string]string{"me.yml": "version: 1\n"})
	})
	// projectWith writes project.yml listing module from source, syncs it as trusted and turns me on
	// there; project lists notes.
	projectWith := func(module, source string) {
		GinkgoHelper()
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: " + module + ", source: " + source + "}\n"})
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll})
		Expect(err).NotTo(HaveOccurred())
		led, err := e.Ledger(sc)
		Expect(err).NotTo(HaveOccurred())
		led.Profile = &ledger.Active{Name: "user:me", By: ledger.ByOn}
		Expect(led.Save(filepath.Join(proj, ".lola", "lock.json"))).To(Succeed())
	}
	project := func(source string) { GinkgoHelper(); projectWith("notes", source) }
	move := func(req engine.MoveRequest) (*engine.MoveResult, error) {
		req.ConfirmRemovals, req.Trust = yes, trustAll
		return e.Move(ctx, req)
	}
	read := func(path string) string {
		GinkgoHelper()
		b, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			return ""
		}
		Expect(err).NotTo(HaveOccurred())
		return string(b)
	}
	insideAs := func(module, file, path, root string) string {
		return module + " (" + file + ") installs from " + path + ", inside project " + root + ": changes there install without review wherever that file is on"
	}
	inside := func(file, path, root string) string { return insideAs("notes", file, path, root) }
	refused := func(file, path, root string) string {
		return inside(file, path, root) + "; lola keeps a project's local modules out of your files"
	}
	me := func() string { return filepath.Join(p.UserProfiles, "me.yml") }
	projectYML := func() string { return filepath.Join(proj, ".lola", "project.yml") }
	Describe("lola profile --move", func() {
		It("refuses moving a project.yml folder entry into a profile of yours, writing nothing", func() {
			project("mods/notes")
			before := read(projectYML())
			_, err := move(engine.MoveRequest{Scope: sc, Modules: []string{"notes"}, To: "me"})
			Expect(err).To(MatchError(refused(me(), filepath.Join(mods, "notes"), proj)))
			Expect(errs.Hints(err)).To(BeEmpty())
			Expect(read(projectYML())).To(Equal(before))
			Expect(read(me())).To(Equal("version: 1\n"))
		})

		It("refuses moving a project.yml folder entry to user scope, writing nothing in either scope", func() {
			project("mods/notes")
			before := read(projectYML())
			_, err := move(engine.MoveRequest{Scope: user, Other: &sc, Across: true, Modules: []string{"notes"}, To: "base"})
			def := filepath.Join(p.UserProfiles, "default.yml")
			Expect(err).To(MatchError(refused(def, filepath.Join(mods, "notes"), proj)))
			Expect(read(projectYML())).To(Equal(before))
			Expect(read(def)).To(BeEmpty())
			led, err := e.Ledger(user)
			Expect(err).NotTo(HaveOccurred())
			Expect(led.Modules).NotTo(HaveKey("notes"))
		})

		It("detects a path that reaches the project through a symbolic link", func() {
			link := filepath.Join(tmp, "link")
			Expect(os.Symlink(mods, link)).To(Succeed())
			project(filepath.Join(link, "notes"))
			_, err := move(engine.MoveRequest{Scope: sc, Modules: []string{"notes"}, To: "me"})
			Expect(err).To(MatchError(refused(me(), filepath.Join(link, "notes"), proj)))
		})

		It("refuses moving a project profile's folder entry into a profile of yours", func() {
			writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\n",
				".lola/profiles/team.yml": "version: 1\nmodules:\n  - {name: notes, source: ../../mods/notes}\n"})
			led, err := e.Ledger(sc)
			Expect(err).NotTo(HaveOccurred())
			led.Profile = &ledger.Active{Name: "project:team", By: ledger.ByOn}
			Expect(led.Save(filepath.Join(proj, ".lola", "lock.json"))).To(Succeed())
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll})
			Expect(err).NotTo(HaveOccurred())
			before := read(filepath.Join(proj, ".lola", "profiles", "team.yml"))
			_, err = move(engine.MoveRequest{Scope: sc, From: "team", Modules: []string{"notes"}, To: "me", KeepOff: true, Yes: true})
			Expect(err).To(MatchError(refused(me(), filepath.Join(mods, "notes"), proj)))
			Expect(read(filepath.Join(proj, ".lola", "profiles", "team.yml"))).To(Equal(before))
			Expect(read(me())).To(Equal("version: 1\n"))
		})

		It("moves between a project's own files", func() {
			project("mods/notes")
			writeTree(proj, map[string]string{".lola/profiles/team.yml": "version: 1\n"})
			_, err := move(engine.MoveRequest{Scope: sc, Modules: []string{"notes"}, To: "project:team", Switch: true})
			Expect(err).NotTo(HaveOccurred())
			Expect(read(filepath.Join(proj, ".lola", "profiles", "team.yml"))).To(ContainSubstring("source: ../../mods/notes"))
		})

		It("moves an entry of yours whose folder is in no project between your files", func() {
			outside := filepath.Join(tmp, "notes")
			writeTree(outside, map[string]string{"lola.yml": "name: notes\n", "commands/notes.md": "one\n"})
			writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + outside + "}\n"})
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: user})
			Expect(err).NotTo(HaveOccurred())
			_, err = move(engine.MoveRequest{Scope: user, Modules: []string{"notes"}, To: "me", KeepOff: true, Yes: true})
			Expect(err).NotTo(HaveOccurred())
			Expect(read(me())).To(ContainSubstring("source: " + outside))
		})
	})

	// Ruling P7: an override copies a project file's entry into a file of yours as a move carries it.
	Describe("lola install --override and lola profile --edit --override into a profile of yours", func() {
		// override edits notes at project scope into me with --only commands/notes, as lola install
		// notes --profile me --override, or as lola profile me notes --edit --override (edit); notes is
		// named by the arguments given, else by its name.
		override := func(edit bool, arg ...string) error {
			GinkgoHelper()
			t := engine.TargetRequest{Profile: "me", Override: true}
			if edit {
				t.Edit, t.Rerun = true, func(extra ...string) string {
					return e.Command(sc, "profile", append([]string{"me", "notes", "--edit"}, extra...)...)
				}
			}
			spec := engine.InstallSpec{Cwd: proj, Modules: []engine.InstallRequest{{Arg: append(arg, "notes")[0]}}, Options: engine.Options{Target: t},
				Scopes: []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: sc, Source: engine.SourceFlag}}}}
			sel, err := config.ParseSelector("command/notes")
			Expect(err).NotTo(HaveOccurred())
			Expect(spec.AddFlagEdits([]engine.SelectionEdit{{Op: engine.EditOnly, Selector: sel, Exact: true}}, false)).To(Succeed())
			_, err = e.InstallAll(ctx, spec)
			return err
		}
		// served is a git module notes over https, as lola fetches one.
		served := func() string {
			GinkgoHelper()
			work := filepath.Join(tmp, "work")
			writeTree(work, map[string]string{"lola.yml": "name: notes\n", "commands/notes.md": "one\n"})
			gitIn(work, "init", "-q", "-b", "main")
			gitIn(work, "add", "-A")
			gitIn(work, "commit", "-q", "-m", "one")
			gitIn(tmp, "clone", "-q", "--bare", work, filepath.Join(tmp, "srv", "notes.git"))
			return testenv.ServeGit(filepath.Join(tmp, "srv")) + "/notes.git"
		}
		for _, edit := range []bool{false, true} {
			It(fmt.Sprintf("refuses a project.yml folder entry, writing nothing (--edit: %v)", edit), func() {
				project("mods/notes")
				before := read(projectYML())
				Expect(override(edit)).To(MatchError(refused(me(), filepath.Join(mods, "notes"), proj)))
				Expect(read(me())).To(Equal("version: 1\n"))
				Expect(read(projectYML())).To(Equal(before))
			})

			It(fmt.Sprintf("refuses an entry the project has not installed as it lists it now, writing nothing, until its sync (--edit: %v)", edit), func() {
				url := served()
				project(url)
				writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + url + ", ref: main}\n"}) // as a pull would
				err := override(edit)
				Expect(err).To(HaveOccurred())
				Expect(read(me())).To(Equal("version: 1\n"), "%v", err)
				Expect(filepath.Join(proj, ".claude", "commands", "notes.md")).To(BeAnExistingFile(), "what the project installed stays")

				_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll})
				Expect(err).NotTo(HaveOccurred())
				Expect(override(edit)).To(Succeed(), "reviewed, it copies")
				Expect(read(me())).To(ContainSubstring("source: " + url))
			})

			It(fmt.Sprintf("copies an https entry the project installed as trusted (--edit: %v)", edit), func() {
				url := served()
				project(url)
				Expect(override(edit)).To(Succeed())
				Expect(read(me())).To(And(ContainSubstring("source: "+url), ContainSubstring("only: [command/notes]")))
			})
		}

		It("refuses a project profile's entry the project has not installed as trusted into yours, hinting the sync that reviews it", func() {
			url := served()
			writeTree(proj, map[string]string{".lola/project.yml": "version: 1\n",
				".lola/profiles/team.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + url + "}\n"})
			writeTree(p.UserProfiles, map[string]string{"both.yml": "version: 1\nbundle: ['project:team', me]\n", "me.yml": "version: 1\ntargets: [claude-code]\n"})
			led, err := e.Ledger(sc)
			Expect(err).NotTo(HaveOccurred())
			led.Profile = &ledger.Active{Name: "user:both", By: ledger.ByOn}
			Expect(led.Save(filepath.Join(proj, ".lola", "lock.json"))).To(Succeed())
			err = override(true, url)
			Expect(err).To(MatchError("notes is not installed in this project as its files list it now; lola copies or moves out of a project only what it installed as you trusted it"))
			Expect(errs.Hints(err)).To(Equal([]string{"review the changes first: lola sync --project notes"}), "a retry refuses again until then")
			Expect(read(me())).To(Equal("version: 1\ntargets: [claude-code]\n"))
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Modules: []string{"notes"}, Trust: trustAll})
			Expect(err).NotTo(HaveOccurred(), "the hint runs as printed")
			Expect(override(true, url)).To(Succeed())
			Expect(read(me())).To(ContainSubstring("source: " + url))
		})
	})

	Describe("lola install", func() {
		claude := engine.Options{Targets: []string{"claude-code"}}
		warning := func(ns []engine.Notice) []string {
			var out []string
			for _, n := range ns {
				if n.Level == engine.LevelWarn {
					out = append(out, n.Text)
				}
			}
			return out
		}

		It("warns once, at the write, when it adds a folder inside a project to a file of yours", func() {
			writeTree(proj, map[string]string{".lola/project.yml": "version: 1\n"})
			res, err := e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: filepath.Join(mods, "notes")}}, claude)
			Expect(err).NotTo(HaveOccurred())
			Expect(warning(res.Notices)).To(Equal([]string{inside(filepath.Join(p.UserProfiles, "default.yml"), filepath.Join(mods, "notes"), proj)}))

			again, err := e.Sync(ctx, engine.SyncRequest{Scope: user})
			Expect(err).NotTo(HaveOccurred())
			Expect(warning(again.Notices)).To(BeEmpty(), "a sync of what you have says nothing")
		})

		It("warns once when an install at project scope adds a folder of the project to a profile of yours", func() {
			writeTree(proj, map[string]string{".lola/project.yml": "version: 1\n"})
			opts := claude
			opts.Target.Profile = "me"
			res, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: filepath.Join(mods, "notes")}}, opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(warning(res.Notices)).To(Equal([]string{inside(me(), filepath.Join(mods, "notes"), proj)}))
		})

		It("says nothing of a folder in no project, or in your home directory", func() {
			outside := filepath.Join(tmp, "notes")
			writeTree(outside, map[string]string{"lola.yml": "name: notes\n", "commands/notes.md": "one\n"})
			res, err := e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: outside}}, claude)
			Expect(err).NotTo(HaveOccurred())
			Expect(warning(res.Notices)).To(BeEmpty())

			writeTree(p.Home, map[string]string{".lola/project.yml": "version: 1\n", "kit/lola.yml": "name: kit\n", "kit/commands/kit.md": "one\n"})
			res, err = e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: filepath.Join(p.Home, "kit")}}, claude)
			Expect(err).NotTo(HaveOccurred())
			Expect(warning(res.Notices)).To(BeEmpty())
		})
	})
})
