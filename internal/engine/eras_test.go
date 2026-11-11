package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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

// quillEras is a user target with two eras, whose installed version is read from a file.
const quillEras = `id: quill
readers: "the quill editor"
probe:
  - file: {path: "{home}/.quill/version.json", pointer: /version}
roots: {project: "{project}", user: "{home}"}
detect: {project: {any: [.quill/]}, user: {any: [.quill/]}}
eras:
  - since: "1.0"
    summary: commands in old/
    layout:
      command: {mech: markdown-file, path: old/commands, ext: .md, sidecar: drop}
  - since: "2.0"
    layout:
      command: {mech: markdown-file, path: new/commands, ext: .md, sidecar: drop}
`

var _ = Describe("the era each scope declares", func() {
	var (
		p         paths.Paths
		home, mod string
		proj      string
		ctx       = context.Background()
	)
	BeforeEach(func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		home = p.Home
		Expect(os.MkdirAll(p.Targets, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(p.Targets, "quill.yaml"), []byte(quillEras), 0o600)).To(Succeed())
		proj, mod = filepath.Join(tmp, "proj"), filepath.Join(tmp, "m")
		writeTree(mod, map[string]string{"lola.yml": "name: m\n", "commands/c.md": "Go.\n"})
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
	})
	project := func() engine.Scope { return engine.Scope{Kind: target.Project, ProjectDir: proj} }
	user := engine.Scope{Kind: target.User}
	installed := func(v string) {
		writeTree(filepath.Join(home, ".quill"), map[string]string{"version.json": `{"version": "` + v + `"}`})
	}
	declare := func(versions string) {
		writeTree(filepath.Join(proj, ".lola"), map[string]string{"project.yml": "version: 1\ntarget_versions: {" + versions + "}\n"})
	}
	installWith := func(opts engine.Options, scs ...engine.Scope) (*engine.InstallResult, error) {
		GinkgoHelper()
		reg, err := targets.Load(p)
		Expect(err).NotTo(HaveOccurred())
		var in []engine.ScopeInstall
		for _, sc := range scs {
			in = append(in, engine.ScopeInstall{ScopeChoice: engine.ScopeChoice{Scope: sc, Source: engine.SourceFlag}, Targets: []string{"quill"}})
		}
		e := engine.New(p, reg)
		e.Getwd = func() (string, error) { return proj, nil } // the project lola runs in, whose version findings it raises
		return e.InstallAll(ctx, engine.InstallSpec{Cwd: proj, Modules: []engine.InstallRequest{{Arg: mod}},
			Options: opts, Scopes: in})
	}
	install := func(scs ...engine.Scope) (*engine.InstallResult, error) {
		GinkgoHelper()
		return installWith(engine.Options{Targets: []string{"quill"}}, scs...)
	}
	eraFindings := func(res *engine.InstallResult) []target.Finding {
		var out []target.Finding
		for _, f := range res.Findings {
			if strings.HasPrefix(f.Code, "version/") {
				out = append(out, f)
			}
		}
		return out
	}
	warn := func(code, text, hint string) any {
		return And(HaveField("Code", code), HaveField("Target", "quill"), HaveField("Kind", "assistant"),
			HaveField("Severity", target.SeverityWarn), HaveField("Text", text), HaveField("Hint", hint))
	}

	It("renders the newest era when nothing is declared and nothing is installed, and says nothing of versions", func() {
		res, err := install(project())
		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.Join(proj, "new", "commands", "c.md")).To(BeAnExistingFile())
		Expect(filepath.Join(proj, "old")).NotTo(BeAnExistingFile())
		Expect(eraFindings(res)).To(BeEmpty())
	})

	It("renders the era project.yml declares, quietly while the installed version is of that era", func() {
		declare(`quill: "1.2"`)
		installed("1.9")
		res, err := install(project())
		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.Join(proj, "old", "commands", "c.md")).To(BeAnExistingFile())
		Expect(filepath.Join(proj, "new")).NotTo(BeAnExistingFile())
		Expect(eraFindings(res)).To(BeEmpty())
	})

	It("warns when the installed version is of another era than the one declared, and renders the declared one", func() {
		declare(`quill: "1.2"`)
		installed("2.1")
		res, err := install(project())
		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.Join(proj, "old", "commands", "c.md")).To(BeAnExistingFile())
		Expect(eraFindings(res)).To(ConsistOf(warn("version/era-mismatch",
			".lola/project.yml declares quill 1.2, but quill 2.1 is installed; lola writes for 1.2",
			`set target_versions.quill to "2.1" in .lola/project.yml, then: lola sync --project; or install a release of quill >=1.0 <2.0`)))
	})

	It("--strict refuses an installed version of another era than the one declared, writing nothing, when the install writes files", func() {
		declare(`quill: "1.2"`)
		installed("2.1")
		strict := engine.Options{Targets: []string{"quill"}, Strict: true}
		_, err := installWith(strict, project())
		Expect(err).To(MatchError(ContainSubstring("--strict: 1 finding(s); nothing was changed")))
		Expect(err).To(MatchError(ContainSubstring("declares quill 1.2, but quill 2.1 is installed")))
		Expect(errs.Hints(err)).To(Equal([]string{"fetched modules stay in your cache; retry with: lola install --project m",
			`set target_versions.quill to "2.1" in .lola/project.yml, then: lola sync --project; ` +
				"or install a release of quill >=1.0 <2.0", "or rerun without --strict to write for the declared version"}))
		Expect(filepath.Join(proj, "old")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(proj, ".lola", "lock.json")).NotTo(BeAnExistingFile())

		installed("1.9")
		_, err = install(project())
		Expect(err).NotTo(HaveOccurred())
		installed("2.1")
		_, err = installWith(strict, project())
		Expect(err).NotTo(HaveOccurred(), "a re-run that writes nothing")
	})

	It("warns when nothing is declared and the installed version is of an older era than lola writes for", func() {
		installed("1.0")
		res, err := install(project())
		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.Join(proj, "new", "commands", "c.md")).To(BeAnExistingFile())
		Expect(eraFindings(res)).To(ConsistOf(warn("version/undeclared",
			"quill 1.0 is installed, but .lola/project.yml declares no version for it, so lola writes for quill 2.0",
			`add quill: "1.0" under target_versions: in .lola/project.yml`)))
	})

	It("renders the first era for a version older than any, and warns", func() {
		declare(`quill: "0.5"`)
		res, err := install(project())
		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.Join(proj, "old", "commands", "c.md")).To(BeAnExistingFile())
		Expect(eraFindings(res)).To(ConsistOf(warn("version/before-first-era",
			".lola/project.yml declares quill 0.5, older than any quill lola knows; lola writes for quill 1.0",
			"upgrade quill to 1.0 or later, then set target_versions.quill to match in .lola/project.yml")))
	})

	It("refuses a project.yml declaring a version for a target lola has none for, naming the file and key", func() {
		declare(`nope: "1.0"`)
		res, err := install(project())
		Expect(res).To(BeNil())
		Expect(err).To(MatchError(".lola/project.yml: target_versions.nope: lola has no target nope"))
		Expect(errs.Hints(err)).To(ContainElements("valid targets: claude-code, gemini-cli, opencode, agents, quill",
			"remove it, or add a target spec for it to "+p.Targets))
		Expect(filepath.Join(proj, "old")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(proj, "new")).NotTo(BeAnExistingFile())
	})

	It("shows the targets directory in its hint without control or format characters", func() {
		declare(`nope: "1.0"`)
		reg, err := targets.Load(p)
		Expect(err).NotTo(HaveOccurred())
		q := p
		q.Targets = p.Targets + "\u202e"
		_, err = engine.New(q, reg).Check(ctx, project(), proj, []string{mod}, []string{"quill"}, nil, nil)
		Expect(errs.Hints(err)).NotTo(ContainElement(ContainSubstring("\u202e")))
	})

	Describe("one install at both scopes", func() {
		BeforeEach(func() {
			writeTree(filepath.Dir(p.Config), map[string]string{filepath.Base(p.Config): "version: 1\ntarget_versions:\n  quill: \"1.2\"\n"})
		})

		It("renders each scope for its own declaration and warns of each once, naming its file", func() {
			installed("1.5")
			res, err := install(user, project())
			Expect(err).NotTo(HaveOccurred())
			Expect(filepath.Join(home, "old", "commands", "c.md")).To(BeAnExistingFile())
			Expect(filepath.Join(home, "new")).NotTo(BeAnExistingFile())
			Expect(filepath.Join(proj, "new", "commands", "c.md")).To(BeAnExistingFile())
			Expect(filepath.Join(proj, "old")).NotTo(BeAnExistingFile())
			Expect(eraFindings(res)).To(ConsistOf(warn("version/undeclared",
				"quill 1.5 is installed, but .lola/project.yml declares no version for it, so lola writes for quill 2.0",
				`add quill: "1.5" under target_versions: in .lola/project.yml`)))

			installed("2.1")
			writeTree(mod, map[string]string{"commands/d.md": "Do.\n"}) // the mismatch matters to an install that writes files
			res, err = install(user, project())
			Expect(err).NotTo(HaveOccurred())
			Expect(eraFindings(res)).To(ConsistOf(warn("version/era-mismatch",
				p.Config+" declares quill 1.2, but quill 2.1 is installed; lola writes for 1.2",
				`set target_versions.quill to "2.1" in `+p.Config+", then: lola sync --user; or install a release of quill >=1.0 <2.0")))
		})
	})

	It("lola check warns when the installed version is of another era than the one declared", func() {
		declare(`quill: "1.2"`)
		installed("2.1")
		reg, err := targets.Load(p)
		Expect(err).NotTo(HaveOccurred())
		e := engine.New(p, reg)
		e.Getwd = func() (string, error) { return proj, nil }
		rep, err := e.Check(ctx, project(), proj, []string{mod}, []string{"quill"}, nil, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(rep.Findings).To(ContainElement(HaveField("Code", "version/era-mismatch")))
	})

	Describe("lola check at user scope", func() {
		check := func() (*engine.CheckReport, error) {
			reg, err := targets.Load(p)
			Expect(err).NotTo(HaveOccurred())
			return engine.New(p, reg).Check(ctx, user, proj, []string{mod}, []string{"quill"}, nil, nil)
		}

		It("checks the newest era when config.yaml cannot be read, as it does when project.yml cannot", func() {
			writeTree(filepath.Dir(p.Config), map[string]string{filepath.Base(p.Config): "version: [\n"})
			rep, err := check()
			Expect(err).NotTo(HaveOccurred())
			Expect(rep.Modules).To(HaveLen(1))
		})

		It("refuses a config.yaml declaring a version for a target lola has none for", func() {
			writeTree(filepath.Dir(p.Config), map[string]string{filepath.Base(p.Config): "version: 1\ntarget_versions:\n  nope: \"1.0\"\n"})
			_, err := check()
			Expect(err).To(MatchError(p.Config + ": target_versions.nope: lola has no target nope"))
		})
	})

	Describe("lock.json", func() {
		lockPath := func() string { return filepath.Join(proj, ".lola", "lock.json") }
		eras := func() map[string]string {
			GinkgoHelper()
			led, err := ledger.Load(lockPath())
			Expect(err).NotTo(HaveOccurred())
			Expect(led.Modules).To(HaveKey("m"))
			return led.Modules["m"].Eras
		}
		// redeclare sets project.yml's target_versions, keeping the entries the install wrote.
		redeclare := func(versions string) {
			GinkgoHelper()
			f := filepath.Join(proj, ".lola", "project.yml")
			b, err := os.ReadFile(f)
			Expect(err).NotTo(HaveOccurred())
			kept := regexp.MustCompile(`(?m)^target_versions:.*\n`).ReplaceAllString(string(b), "")
			Expect(os.WriteFile(f, []byte(kept+"target_versions: {"+versions+"}\n"), 0o600)).To(Succeed())
		}
		syncAs := func(dry bool) (*engine.SyncResult, error) {
			GinkgoHelper()
			reg, err := targets.Load(p)
			Expect(err).NotTo(HaveOccurred())
			e := engine.New(p, reg)
			e.Getwd = func() (string, error) { return proj, nil }
			return e.Sync(ctx, engine.SyncRequest{Scope: project(), Options: engine.Options{DryRun: dry},
				Trust:           func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil },
				ConfirmRemovals: func([]engine.Removal) (bool, error) { return true, nil }})
		}
		sync := func() (*engine.SyncResult, error) { return syncAs(false) }
		eraNotices := func(res *engine.SyncResult) []string {
			var out []string
			for _, n := range res.Notices {
				if strings.Contains(n.Text, " rewrote ") || strings.Contains(n.Text, " would rewrite ") {
					Expect(n.Level).To(Equal(engine.LevelInfo))
					Expect(n.Class).To(Equal(target.ClassDetail))
					out = append(out, n.Text)
				}
			}
			return out
		}

		It("records the era each target's files were written for", func() {
			declare(`quill: "1.2"`)
			_, err := install(project())
			Expect(err).NotTo(HaveOccurred())
			Expect(eras()).To(Equal(map[string]string{"quill": "1.0"}))
		})

		It("sync warns of an installed version of another era only when it writes files", func() {
			declare(`quill: "1.2"`)
			_, err := install(project())
			Expect(err).NotTo(HaveOccurred())
			installed("2.1")
			mismatch := func(res *engine.SyncResult) []target.Finding {
				return slices.DeleteFunc(slices.Clone(res.Findings), func(f target.Finding) bool { return f.Code != "version/era-mismatch" })
			}
			res, err := sync()
			Expect(err).NotTo(HaveOccurred())
			Expect(res.InSync).To(BeTrue())
			Expect(mismatch(res)).To(BeEmpty(), "already in sync")
			Expect(os.Remove(filepath.Join(proj, "old", "commands", "c.md"))).To(Succeed())
			res, err = sync()
			Expect(err).NotTo(HaveOccurred())
			Expect(mismatch(res)).To(HaveLen(1), "it restores a file")
		})

		It("records the newest era when nothing is declared", func() {
			_, err := install(project())
			Expect(err).NotTo(HaveOccurred())
			Expect(eras()).To(Equal(map[string]string{"quill": "2.0"}))
		})

		It("sync rewrites a module's files for the era project.yml now declares, removes the old ones and says why", func() {
			_, err := install(project())
			Expect(err).NotTo(HaveOccurred())
			Expect(filepath.Join(proj, "new", "commands", "c.md")).To(BeAnExistingFile())
			redeclare(`quill: "1.2"`)
			res, err := syncAs(true)
			Expect(err).NotTo(HaveOccurred())
			Expect(eraNotices(res)).To(ConsistOf("m: would rewrite quill files from >=2.0 to >=1.0 <2.0"))
			Expect(res.Rewrites).To(Equal([]engine.Rewrite{{Module: "m", Target: "quill", From: "2.0", To: "1.0"}}))
			res, err = sync()
			Expect(err).NotTo(HaveOccurred())
			Expect(filepath.Join(proj, "old", "commands", "c.md")).To(BeAnExistingFile())
			Expect(filepath.Join(proj, "new", "commands", "c.md")).NotTo(BeAnExistingFile())
			Expect(eraNotices(res)).To(ConsistOf("m: rewrote quill files from >=2.0 to >=1.0 <2.0"))
			Expect(eras()).To(Equal(map[string]string{"quill": "1.0"}))

			res, err = sync()
			Expect(err).NotTo(HaveOccurred())
			Expect(res.InSync).To(BeTrue())
			Expect(eraNotices(res)).To(BeEmpty())
			Expect(res.Rewrites).To(BeEmpty())
			Expect(res.Rewrites).NotTo(BeNil())
		})

		It("reads an entry without eras as written for the newest era, so sync leaves it as it is", func() {
			_, err := install(project())
			Expect(err).NotTo(HaveOccurred())
			b, err := os.ReadFile(lockPath())
			Expect(err).NotTo(HaveOccurred())
			bare := regexp.MustCompile(`(?s),\s*"eras": \{[^}]*\}`).ReplaceAll(b, nil)
			Expect(string(bare)).NotTo(ContainSubstring(`"eras"`))
			Expect(os.WriteFile(lockPath(), bare, 0o600)).To(Succeed())
			redeclare(`quill: "2.4"`)
			res, err := sync()
			Expect(err).NotTo(HaveOccurred())
			Expect(eraNotices(res)).To(BeEmpty())
			Expect(res.InSync).To(BeTrue())
			Expect(filepath.Join(proj, "new", "commands", "c.md")).To(BeAnExistingFile())
		})
	})

	It("tidies the declared era's destinations, owning what the install wrote there", func() {
		declare(`quill: "1.2"`)
		_, err := install(project())
		Expect(err).NotTo(HaveOccurred())
		writeTree(filepath.Join(proj, "old", "commands"), map[string]string{"stray.md": "Mine.\n"})
		reg, err := targets.Load(p)
		Expect(err).NotTo(HaveOccurred())
		res, err := engine.New(p, reg).Tidy(ctx, engine.TidyRequest{Scope: project(), Dry: true, Targets: []string{"quill"}})
		Expect(err).NotTo(HaveOccurred())
		var paths []string
		for _, u := range res.Candidates {
			paths = append(paths, u.Path)
		}
		Expect(paths).To(ConsistOf(filepath.Join(proj, "old", "commands", "stray.md")))
	})
})
