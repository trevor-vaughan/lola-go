package engine_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("replaceable paths (migrate design §4, §12)", func() {
	var (
		proj, src string
		e         *engine.Engine
		ctx       = context.Background()
	)
	BeforeEach(func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, src = filepath.Join(tmp, "proj"), filepath.Join(tmp, "example")
		writeTree(src, exampleModule)
		writeTree(proj, map[string]string{".claude/commands/ship.md": "the Python lola's copy\n"})
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
	})
	install := func(dry bool, replace ...plan.Replaceable) (*engine.InstallResult, error) {
		si := engine.ScopeInstall{ScopeChoice: engine.ScopeChoice{Scope: engine.Scope{Kind: target.Project, ProjectDir: proj}, Source: engine.SourceFlag},
			Targets: []string{"claude-code"}, Replace: replace}
		return e.InstallAll(ctx, engine.InstallSpec{Cwd: proj, Scopes: []engine.ScopeInstall{si}, Modules: []engine.InstallRequest{{Arg: src}},
			Options: engine.Options{DryRun: dry}})
	}
	ship := plan.Replaceable{Target: "claude-code", Path: ".claude/commands/ship.md"}

	It("refuses an unowned file that is not replaceable, writing nothing", func() {
		_, err := install(false)
		Expect(err).To(MatchError(ContainSubstring(".claude/commands/ship.md: exists and was not written by lola")))
		Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("--force")))
		Expect(filepath.Join(proj, ".lola")).NotTo(BeADirectory())
		Expect(os.ReadFile(filepath.Join(proj, ".claude/commands/ship.md"))).To(Equal([]byte("the Python lola's copy\n")))
	})

	It("writes no .lola/.gitignore into a .lola/ that already existed when it refuses", func() {
		writeTree(proj, map[string]string{".lola/modules/x/a.md": "the Python lola's module copy\n"})
		_, err := install(false)
		Expect(err).To(HaveOccurred())
		Expect(filepath.Join(proj, ".lola/.gitignore")).NotTo(BeAnExistingFile())
	})

	It("plans a replaceable file as a replacement in a dry run and writes nothing", func() {
		res, err := install(true, ship)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Steps).To(ContainElement(And(HaveField("Path", ".claude/commands/ship.md"), HaveField("Replaced", true))))
		Expect(os.ReadFile(filepath.Join(proj, ".claude/commands/ship.md"))).To(Equal([]byte("the Python lola's copy\n")))
	})

	It("overwrites a replaceable file and owns it", func() {
		res, err := install(false, ship)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Steps).To(ContainElement(And(HaveField("Path", ".claude/commands/ship.md"), HaveField("Replaced", true))))
		Expect(os.ReadFile(filepath.Join(proj, ".claude/commands/ship.md"))).To(Equal([]byte("Ship $ARGUMENTS\n")))
		led, err := e.Ledger(engine.Scope{Kind: target.Project, ProjectDir: proj})
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Modules["example"].Artifacts).To(ContainElement(HaveField("Path", ".claude/commands/ship.md")))

		_, err = install(false, ship)
		Expect(err).NotTo(HaveOccurred(), "a second install updates its own file")
	})
})

var _ = Describe("per-module targets and the pre-write callback (migrate design §4)", func() {
	var (
		proj, src string
		e         *engine.Engine
		sc        engine.Scope
		ctx       = context.Background()
	)
	BeforeEach(func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, src = filepath.Join(tmp, "proj"), filepath.Join(tmp, "example")
		writeTree(src, exampleModule)
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		sc = engine.Scope{Kind: target.Project, ProjectDir: proj}
	})
	spec := func(scopeTargets []string, req engine.InstallRequest) engine.InstallSpec {
		return engine.InstallSpec{Cwd: proj, Modules: []engine.InstallRequest{req},
			Scopes: []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: sc, Source: engine.SourceFlag}, Targets: scopeTargets}}}
	}

	It("installs a module on its request's targets, recording them when they differ from the manifest's", func() {
		_, err := e.InstallAll(ctx, spec([]string{"claude-code", "gemini-cli"}, engine.InstallRequest{Arg: src, Targets: []string{"gemini-cli"}}))
		Expect(err).NotTo(HaveOccurred())
		man, err := e.Manifest(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(man.Targets).To(Equal([]string{"claude-code", "gemini-cli"}))
		Expect(man.Find("example").Targets).To(Equal([]string{"gemini-cli"}))
		Expect(filepath.Join(proj, ".claude")).NotTo(BeADirectory())
		Expect(filepath.Join(proj, ".gemini/commands/ship.toml")).To(BeAnExistingFile())
	})

	It("adds a request's targets to what the manifest already has for the module", func() {
		_, err := e.InstallAll(ctx, spec([]string{"claude-code"}, engine.InstallRequest{Arg: src}))
		Expect(err).NotTo(HaveOccurred())
		_, err = e.InstallAll(ctx, spec(nil, engine.InstallRequest{Arg: src, Targets: []string{"gemini-cli"}}))
		Expect(err).NotTo(HaveOccurred())
		man, err := e.Manifest(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(man.Targets).To(Equal([]string{"claude-code"}))
		Expect(man.Find("example").Targets).To(Equal([]string{"claude-code", "gemini-cli"}))
		Expect(filepath.Join(proj, ".claude/commands/ship.md")).To(BeAnExistingFile())
		Expect(filepath.Join(proj, ".gemini/commands/ship.toml")).To(BeAnExistingFile())

		_, err = e.InstallAll(ctx, spec(nil, engine.InstallRequest{Arg: src, Targets: []string{"claude-code"}}))
		Expect(err).NotTo(HaveOccurred())
		man, err = e.Manifest(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(man.Find("example").Targets).To(Equal([]string{"claude-code", "gemini-cli"}), "targets are only ever added")
	})

	It("adds targets to a module at the version installed here, never upgrading it", func() {
		_, err := e.InstallAll(ctx, spec([]string{"claude-code"}, engine.InstallRequest{Arg: src}))
		Expect(err).NotTo(HaveOccurred())
		led, err := e.Ledger(sc)
		Expect(err).NotTo(HaveOccurred())
		installed := led.Modules["example"].Digest
		writeTree(src, map[string]string{"commands/ship.md": "Ship something newer\n"})

		ch, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: []engine.ScopeChoice{{Scope: sc, Source: engine.SourceFlag}},
			Modules: []engine.InstallRequest{{Arg: src, Targets: []string{"gemini-cli"}}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(installed).To(HavePrefix("sha256:"+ch.Versions[0].ID), "the choices list the installed version")

		_, err = e.InstallAll(ctx, spec(nil, engine.InstallRequest{Arg: src, Targets: []string{"gemini-cli"}}))
		Expect(err).NotTo(HaveOccurred())
		for _, f := range []string{".claude/commands/ship.md", ".gemini/commands/ship.toml"} {
			b, err := os.ReadFile(filepath.Join(proj, f))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(b)).To(ContainSubstring("Ship"))
			Expect(string(b)).NotTo(ContainSubstring("newer"), f)
		}
		led, err = e.Ledger(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Modules["example"].Digest).To(Equal(installed))
	})

	It("refuses to add targets to a module whose installed version left the cache, writing nothing", func() {
		_, err := e.InstallAll(ctx, spec([]string{"claude-code"}, engine.InstallRequest{Arg: src}))
		Expect(err).NotTo(HaveOccurred())
		_, err = e.Store.Remove("example")
		Expect(err).NotTo(HaveOccurred())
		writeTree(src, map[string]string{"commands/ship.md": "Ship something newer\n"})
		_, err = e.InstallAll(ctx, spec(nil, engine.InstallRequest{Arg: src, Targets: []string{"gemini-cli"}}))
		Expect(err).To(MatchError(ContainSubstring("the version of example installed here")))
		Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("lola render")))
		Expect(filepath.Join(proj, ".gemini")).NotTo(BeADirectory())
	})

	It("refuses a request target it does not know", func() {
		_, err := e.InstallAll(ctx, spec([]string{"claude-code"}, engine.InstallRequest{Arg: src, Targets: []string{"cursor"}}))
		Expect(err).To(MatchError(`unknown target "cursor"`))
	})

	It("writes nothing when it refuses a request target it does not know", func() {
		_, err := e.InstallAll(ctx, spec([]string{"claude-code"}, engine.InstallRequest{Arg: src, Targets: []string{"cursor"}}))
		Expect(err).To(HaveOccurred())
		Expect(os.ReadDir(proj)).To(BeEmpty())
	})

	It("adds nothing to a .lola/ that already existed when it refuses a request target it does not know", func() {
		writeTree(proj, map[string]string{".lola/modules/x/a.md": "the Python lola's module copy\n"})
		_, err := e.InstallAll(ctx, spec([]string{"claude-code"}, engine.InstallRequest{Arg: src, Targets: []string{"cursor"}}))
		Expect(err).To(HaveOccurred())
		Expect(os.ReadDir(proj)).To(HaveLen(1))
		Expect(os.ReadDir(filepath.Join(proj, ".lola"))).To(ConsistOf(HaveField("Name()", "modules")))
		Expect(os.ReadDir(filepath.Join(proj, ".lola/modules"))).To(ConsistOf(HaveField("Name()", "x")))
	})

	It("calls BeforeWrite with the computed plan once every refusal and prompt has passed, before writing", func() {
		var seen []plan.Step
		s := spec([]string{"claude-code"}, engine.InstallRequest{Arg: src})
		s.BeforeWrite = func(r *engine.InstallResult) error {
			seen = r.Steps
			Expect(filepath.Join(proj, ".claude/commands/ship.md")).NotTo(BeAnExistingFile())
			return nil
		}
		_, err := e.InstallAll(ctx, s)
		Expect(err).NotTo(HaveOccurred())
		Expect(seen).To(ContainElement(HaveField("Path", ".claude/commands/ship.md")))
		Expect(filepath.Join(proj, ".claude/commands/ship.md")).To(BeAnExistingFile())
	})

	It("adds nothing to a .lola/ that already existed when BeforeWrite fails", func() {
		writeTree(proj, map[string]string{".lola/modules/x/a.md": "the Python lola's module copy\n"})
		s := spec([]string{"claude-code"}, engine.InstallRequest{Arg: src})
		s.BeforeWrite = func(*engine.InstallResult) error { return errors.New("backup failed") }
		_, err := e.InstallAll(ctx, s)
		Expect(err).To(MatchError("backup failed"))
		Expect(filepath.Join(proj, ".lola/.gitignore")).NotTo(BeAnExistingFile())
		Expect(os.ReadDir(proj)).To(HaveLen(1))
		Expect(os.ReadDir(filepath.Join(proj, ".lola"))).To(ConsistOf(HaveField("Name()", "modules")))
		Expect(os.ReadDir(filepath.Join(proj, ".lola/modules"))).To(ConsistOf(HaveField("Name()", "x")))
	})

	It("writes nothing when BeforeWrite fails, and never calls it on a dry run or a refusal", func() {
		s := spec([]string{"claude-code"}, engine.InstallRequest{Arg: src})
		s.BeforeWrite = func(*engine.InstallResult) error { return errors.New("backup failed") }
		_, err := e.InstallAll(ctx, s)
		Expect(err).To(MatchError("backup failed"))
		Expect(filepath.Join(proj, ".claude")).NotTo(BeADirectory())
		Expect(filepath.Join(proj, ".lola")).NotTo(BeADirectory())

		called := false
		s.BeforeWrite = func(*engine.InstallResult) error { called = true; return nil }
		s.Options.DryRun = true
		_, err = e.InstallAll(ctx, s)
		Expect(err).NotTo(HaveOccurred())
		writeTree(proj, map[string]string{".claude/commands/ship.md": "theirs\n"})
		s.Options.DryRun = false
		_, err = e.InstallAll(ctx, s)
		Expect(err).To(MatchError(ContainSubstring("conflict")))
		Expect(called).To(BeFalse())
	})
})
