package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
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
	install := func(scs ...engine.Scope) (*engine.InstallResult, error) {
		GinkgoHelper()
		reg, err := targets.Load(p)
		Expect(err).NotTo(HaveOccurred())
		var in []engine.ScopeInstall
		for _, sc := range scs {
			in = append(in, engine.ScopeInstall{ScopeChoice: engine.ScopeChoice{Scope: sc, Source: engine.SourceFlag}, Targets: []string{"quill"}})
		}
		return engine.New(p, reg).InstallAll(ctx, engine.InstallSpec{Cwd: proj, Modules: []engine.InstallRequest{{Arg: mod}},
			Options: engine.Options{Targets: []string{"quill"}}, Scopes: in})
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
			`set target_versions.quill to "2.1" in .lola/project.yml, or install quill 1.2`)))
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
		_, err = engine.New(q, reg).Check(ctx, project(), proj, []string{mod}, []string{"quill"}, nil)
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
			res, err = install(user, project())
			Expect(err).NotTo(HaveOccurred())
			Expect(eraFindings(res)).To(ConsistOf(warn("version/era-mismatch",
				p.Config+" declares quill 1.2, but quill 2.1 is installed; lola writes for 1.2",
				`set target_versions.quill to "2.1" in `+p.Config+", or install quill 1.2")))
		})
	})

	It("lola check warns when the installed version is of another era than the one declared", func() {
		declare(`quill: "1.2"`)
		installed("2.1")
		reg, err := targets.Load(p)
		Expect(err).NotTo(HaveOccurred())
		rep, err := engine.New(p, reg).Check(ctx, project(), proj, []string{mod}, []string{"quill"}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(rep.Findings).To(ContainElement(HaveField("Code", "version/era-mismatch")))
	})

	Describe("lola check at user scope", func() {
		check := func() (*engine.CheckReport, error) {
			reg, err := targets.Load(p)
			Expect(err).NotTo(HaveOccurred())
			return engine.New(p, reg).Check(ctx, user, proj, []string{mod}, []string{"quill"}, nil)
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
