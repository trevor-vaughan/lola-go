package engine_test

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

const quillTarget = `id: quill
readers: "the quill editor"
roots: {project: "{project}", user: "{home}"}
detect: {project: {any: [.quill/]}, user: {any: [.quill/]}}
eras:
  - since: "1.0"
    layout:
      agent: {mech: markdown-file, path: .quill/agents, ext: .md}
`

const mineTarget = `id: opencode
readers: "my opencode"
roots: {project: "{project}", user: "{home}"}
detect: {project: {any: [.mine/]}, user: {any: [.mine/]}}
eras:
  - since: "1.0"
    layout:
      agent: {mech: markdown-file, path: .mine/agents, ext: .md}
`

// restoreHint is the refusal's coaching for target id, missing from the user's target specs at p.
func restoreHint(p paths.Paths, id string) string {
	return "restore your target spec for " + id + " in " + p.Targets +
		", then run this again to remove what lola wrote for it; lola repair forgets those files and leaves them"
}

var _ = Describe("user target specs in the engine", func() {
	var (
		p                paths.Paths
		specs, proj, mod string
		ctx              = context.Background()
	)
	BeforeEach(func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		specs = p.Targets
		Expect(os.MkdirAll(specs, 0o755)).To(Succeed())
		proj, mod = filepath.Join(tmp, "proj"), filepath.Join(tmp, "m")
		writeTree(mod, map[string]string{"lola.yml": "name: m\n", "agents/helper.md": "---\ndescription: Helps.\n---\nHelp.\n"})
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
	})
	engineFor := func() *engine.Engine {
		GinkgoHelper()
		reg, err := targets.Load(p)
		Expect(err).NotTo(HaveOccurred())
		return engine.New(p, reg)
	}
	scope := func() engine.Scope { return engine.Scope{Kind: target.Project, ProjectDir: proj} }
	install := func(e *engine.Engine, dry bool, ts ...string) (*engine.InstallResult, error) {
		return e.InstallAll(ctx, engine.InstallSpec{Cwd: proj, Modules: []engine.InstallRequest{{Arg: mod}},
			Options: engine.Options{Targets: ts, DryRun: dry},
			Scopes:  []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: scope(), Source: engine.SourceFlag}, Targets: ts}}})
	}

	Describe("a user spec that replaces a built-in", func() {
		BeforeEach(func() {
			Expect(os.WriteFile(filepath.Join(specs, "mine.yaml"), []byte(mineTarget), 0o600)).To(Succeed())
		})

		It("reports an info finding naming the file when the replaced target takes part", func() {
			res, err := install(engineFor(), true, "opencode")
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Findings).To(ContainElement(And(HaveField("Code", "target/user-spec"), HaveField("Target", "opencode"),
				HaveField("Severity", target.SeverityInfo), HaveField("Kind", "assistant"),
				HaveField("Text", And(HavePrefix("uses your target spec "), HaveSuffix(filepath.Join("targets", "mine.yaml")+" in place of lola's built-in"))))))
		})

		It("reports nothing for a target the user spec does not replace", func() {
			res, err := install(engineFor(), true, "claude-code")
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Findings).NotTo(ContainElement(HaveField("Code", "target/user-spec")))
		})
	})

	Describe("a user spec removed after it was used", func() {
		It("refuses the uninstall, naming the vanished target and coaching its spec's return before a repair, and leaves the files", func() {
			quill := filepath.Join(specs, "quill.yaml")
			Expect(os.WriteFile(quill, []byte(quillTarget), 0o600)).To(Succeed())
			_, err := install(engineFor(), false, "quill")
			Expect(err).NotTo(HaveOccurred())
			written := filepath.Join(proj, ".quill", "agents", "helper.md")
			Expect(written).To(BeAnExistingFile())

			Expect(os.Remove(quill)).To(Succeed())
			res, err := engineFor().Uninstall(ctx, scope(), []string{"m"}, engine.Options{})
			Expect(res).To(BeNil())
			Expect(err).To(MatchError(ContainSubstring(`target "quill" is not a target lola knows`)))
			hints := errs.Hints(err)
			Expect(hints).To(HaveLen(2))
			Expect(hints[0]).To(Equal(restoreHint(p, "quill")))
			Expect(hints[1]).To(HavePrefix("lola repair"))
			Expect(written).To(BeAnExistingFile())
		})
	})
})
