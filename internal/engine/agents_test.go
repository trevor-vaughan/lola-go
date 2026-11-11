package engine_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/types"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("Skills the agents target delivers to gemini-cli and opencode", func() {
	var (
		proj, mod string
		e         *engine.Engine
		sc        engine.Scope
		ctx       = context.Background()
	)
	BeforeEach(func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p, targets.Builtin())
		proj, mod = filepath.Join(tmp, "proj"), filepath.Join(tmp, "m")
		writeTree(mod, map[string]string{"lola.yml": "name: m\n",
			"skills/review/SKILL.md":   "---\ndescription: Review code\n---\nReview it.\n",
			"skills/bad_name/SKILL.md": "---\ndescription: Breaks the agentskills name rules\n---\nBad.\n",
			"commands/hi.md":           "Hi\n"})
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		sc = engine.Scope{Kind: target.Project, ProjectDir: proj}
	})
	spec := func(arg string, ts ...string) engine.InstallSpec {
		return engine.InstallSpec{Cwd: proj, Modules: []engine.InstallRequest{{Arg: arg}}, Options: engine.Options{Targets: ts},
			Scopes: []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: sc, Source: engine.SourceFlag}, Targets: ts}}}
	}
	at := func(rel string) string { return filepath.Join(proj, rel) }
	via := func(t, skill string) types.GomegaMatcher {
		return And(HaveField("Code", "skill-via-agents"), HaveField("Target", t), HaveField("Artifact", skill),
			HaveField("Severity", target.SeverityInfo), HaveField("Class", target.ClassDetail),
			HaveField("Text", "delivered through .agents/skills, which "+t+" reads"))
	}

	It("writes a skill the agents target admits only to .agents/skills, with an info finding on gemini-cli and opencode", func() {
		res, err := e.InstallAll(ctx, spec(mod, "gemini-cli", "opencode", "agents"))
		Expect(err).NotTo(HaveOccurred())
		Expect(at(".agents/skills/review/SKILL.md")).To(BeAnExistingFile())
		Expect(at(".gemini/skills/review")).NotTo(BeAnExistingFile())
		Expect(at(".opencode/skills/review")).NotTo(BeAnExistingFile())
		Expect(res.Findings).To(ContainElements(via("gemini-cli", "review"), via("opencode", "review")))
		Expect(at(".gemini/skills/bad_name/SKILL.md")).To(BeAnExistingFile(), "agents refuses it: gemini keeps its copy")
		Expect(at(".gemini/commands/hi.toml")).To(BeAnExistingFile(), "other kinds are unaffected")
	})

	It("keeps native copies of a skill the agents target only forces", func() {
		s := spec(mod, "gemini-cli", "agents")
		s.Options.Force = true
		res, err := e.InstallAll(ctx, s)
		Expect(err).NotTo(HaveOccurred())
		Expect(at(".agents/skills/bad_name/SKILL.md")).To(BeAnExistingFile(), "forced")
		Expect(at(".gemini/skills/bad_name/SKILL.md")).To(BeAnExistingFile())
		Expect(res.Findings).NotTo(ContainElement(via("gemini-cli", "bad_name")))
		Expect(res.Findings).To(ContainElement(via("gemini-cli", "review")))
	})

	It("keeps native copies for a module whose entry's targets leave agents out", func() {
		s := spec(mod, "gemini-cli", "agents")
		s.Modules[0].Targets = []string{"gemini-cli"}
		res, err := e.InstallAll(ctx, s)
		Expect(err).NotTo(HaveOccurred())
		Expect(at(".gemini/skills/review/SKILL.md")).To(BeAnExistingFile())
		Expect(at(".agents/skills/review")).NotTo(BeAnExistingFile())
		Expect(res.Findings).NotTo(ContainElement(HaveField("Code", "skill-via-agents")))
	})

	It("asks before adding agents removes gemini-cli's copies, and writes them back when agents is dropped", func() {
		_, err := e.InstallAll(ctx, spec(mod, "gemini-cli"))
		Expect(err).NotTo(HaveOccurred())
		native := at(".gemini/skills/review/SKILL.md")
		Expect(native).To(BeAnExistingFile())
		viaAgents := engine.Removal{Module: "m", Targets: []string{"gemini-cli"}, Paths: []engine.RemovedPath{{Target: "gemini-cli", Path: ".gemini/skills/review/SKILL.md"}},
			Why: "delivered through .agents/skills", Scope: "project"}

		_, err = e.InstallAll(ctx, spec("m", "gemini-cli", "agents"))
		refused, ok := errors.AsType[*engine.DeletesRefusedError](err)
		Expect(ok).To(BeTrue(), "%v", err)
		Expect(refused.Removals).To(Equal([]engine.Removal{viaAgents}))
		Expect(native).To(BeAnExistingFile(), "nothing was written")
		Expect(at(".agents/skills/review")).NotTo(BeAnExistingFile())

		add := spec("m", "gemini-cli", "agents")
		var asked [][]engine.Removal
		add.ConfirmRemovals = func(rs []engine.Removal) (bool, error) { asked = append(asked, rs); return true, nil }
		_, err = e.InstallAll(ctx, add)
		Expect(err).NotTo(HaveOccurred())
		Expect(asked).To(Equal([][]engine.Removal{{viaAgents}}))
		Expect(native).NotTo(BeAnExistingFile())
		Expect(at(".agents/skills/review/SKILL.md")).To(BeAnExistingFile())
		Expect(at(".gemini/skills/bad_name/SKILL.md")).To(BeAnExistingFile())

		drop := spec("m", "gemini-cli")
		drop.ConfirmRemovals = func([]engine.Removal) (bool, error) { return true, nil }
		_, err = e.InstallAll(ctx, drop)
		Expect(err).NotTo(HaveOccurred())
		Expect(native).To(BeAnExistingFile(), "written back")
		Expect(at(".agents/skills/review")).NotTo(BeAnExistingFile())
	})

	It("renders agents once: a template that fails only for agents refuses the run for agents, charged once to the run budget", func() {
		writeTree(mod, map[string]string{"skills/review/SKILL.md.liquid": "---\ndescription: Review code\n---\n{% if target == 'agents' %}{{ nope }}{% endif %}Review it.\n"})
		Expect(os.Remove(filepath.Join(mod, "skills/review/SKILL.md"))).To(Succeed())
		clock := time.Unix(0, 0)
		e.Now = func() time.Time { clock = clock.Add(time.Second); return clock } // every render takes 1 s
		e.TemplateBudget.RunTime = 2500 * time.Millisecond                        // agents' failed render and gemini-cli's fit; a third does not
		_, err := e.InstallAll(ctx, spec(mod, "gemini-cli", "agents"))
		te, ok := errors.AsType[*engine.TemplateError](err)
		Expect(ok).To(BeTrue(), "%v", err)
		Expect(te.Code).To(Equal("template-undefined-variable"))
		Expect(te.Target).To(Equal("agents"))
		Expect(at(".gemini")).NotTo(BeAnExistingFile(), "nothing was written")

		_, err = e.Check(ctx, sc, proj, []string{mod}, []string{"gemini-cli", "agents"}, nil, nil)
		Expect(err).NotTo(HaveOccurred())
	})

	It("lola check agrees with install", func() {
		checked := func(ids ...string) []target.Finding {
			rep, err := e.Check(ctx, sc, proj, []string{mod}, ids, nil, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(rep.Modules).To(HaveLen(1))
			return rep.Modules[0].Findings
		}
		Expect(checked("gemini-cli", "opencode", "agents")).To(SatisfyAll(ContainElements(via("gemini-cli", "review"), via("opencode", "review")),
			Not(ContainElement(via("gemini-cli", "bad_name")))))
		Expect(checked("gemini-cli")).NotTo(ContainElement(HaveField("Code", "skill-via-agents")))

		s := spec(mod, "gemini-cli", "agents")
		s.Modules[0].Targets = []string{"gemini-cli"}
		_, err := e.InstallAll(ctx, s)
		Expect(err).NotTo(HaveOccurred())
		Expect(checked()).NotTo(ContainElement(HaveField("Code", "skill-via-agents")), "the entry's targets leave agents out")

		other := filepath.Join(filepath.Dir(mod), "other-m")
		writeTree(other, map[string]string{"lola.yml": "name: m\n", "skills/review/SKILL.md": "---\ndescription: Review code\n---\nReview it.\n"})
		rep, err := e.Check(ctx, sc, proj, []string{other}, []string{"gemini-cli", "agents"}, nil, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(rep.Modules[0].Findings).To(ContainElement(via("gemini-cli", "review")), "another source named m is not the entry's")
	})

	It("lola check with no -t leaves no skill to agents where .agents/skills is only set up, not among the module's targets", func() {
		Expect(os.MkdirAll(at(".agents/skills"), 0o755)).To(Succeed())
		_, err := e.InstallAll(ctx, spec(mod, "opencode"))
		Expect(err).NotTo(HaveOccurred())
		Expect(at(".opencode/skills/review/SKILL.md")).To(BeAnExistingFile())
		rep, err := e.Check(ctx, sc, proj, []string{mod}, nil, nil, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(rep.Modules[0].Targets).To(ContainElement("agents"), "agents is checked where it is set up")
		Expect(rep.Modules[0].Findings).NotTo(ContainElement(HaveField("Code", "skill-via-agents")), "install wrote opencode's own copy")

		_, err = e.InstallAll(ctx, spec("m", "opencode", "agents"))
		Expect(err).To(HaveOccurred(), "deleting opencode's copy asks first")
		s := spec("m", "opencode", "agents")
		s.ConfirmRemovals = func([]engine.Removal) (bool, error) { return true, nil }
		_, err = e.InstallAll(ctx, s)
		Expect(err).NotTo(HaveOccurred())
		rep, err = e.Check(ctx, sc, proj, []string{mod}, nil, nil, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(rep.Modules[0].Findings).To(ContainElement(via("opencode", "review")), "the scope's targets now include agents")
	})
})

var _ = Describe("The shared .agents/skills directory", func() {
	var (
		proj, mod string
		e         *engine.Engine
		sc        engine.Scope
		ctx       = context.Background()
	)
	BeforeEach(func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p, targets.Builtin())
		proj, mod = filepath.Join(tmp, "proj"), filepath.Join(tmp, "m")
		writeTree(mod, map[string]string{"lola.yml": "name: m\n", "skills/review/SKILL.md": "---\ndescription: Review code\n---\nReview it.\n"})
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		sc = engine.Scope{Kind: target.Project, ProjectDir: proj}
	})
	spec := func(opts engine.Options) engine.InstallSpec {
		return engine.InstallSpec{Cwd: proj, Modules: []engine.InstallRequest{{Arg: mod}}, Options: opts,
			Scopes: []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: sc, Source: engine.SourceFlag}, Targets: opts.Targets}}}
	}
	at := func(rel string) string { return filepath.Join(proj, rel) }

	It("a skill another tool wrote at the same name is the unowned-file conflict, never overwritten", func() {
		writeTree(proj, map[string]string{".agents/skills/review/SKILL.md": "theirs\n"})
		_, err := e.InstallAll(ctx, spec(engine.Options{Targets: []string{"agents"}, ForceRun: "lola install m -t agents --force"}))
		ce, ok := errors.AsType[*plan.ConflictError](err)
		Expect(ok).To(BeTrue(), "%v", err)
		Expect(ce.Conflicts).To(ConsistOf(And(HaveField("Target", "agents"), HaveField("Path", ".agents/skills/review/SKILL.md"),
			HaveField("Reason", "exists and was not written by lola"), HaveField("Forceable", true))))
		Expect(errs.Hints(err)).To(Equal([]string{"lola install m -t agents --force"}))
		Expect(os.ReadFile(at(".agents/skills/review/SKILL.md"))).To(BeEquivalentTo("theirs\n"))
	})

	It("a .claude/skills/<n> linked to .agents/skills/<n> refuses claude-code with agents, hinting the run for each one alone", func() {
		Expect(os.MkdirAll(at(".agents/skills/review"), 0o755)).To(Succeed())
		Expect(os.MkdirAll(at(".claude/skills"), 0o755)).To(Succeed())
		Expect(os.Symlink(filepath.Join("..", "..", ".agents", "skills", "review"), at(".claude/skills/review"))).To(Succeed())
		run := func(sc engine.Scope, id string) string { return "lola install m -C " + sc.ProjectDir + " -t " + id }
		_, err := e.InstallAll(ctx, spec(engine.Options{Targets: []string{"claude-code", "agents"}, TargetRun: run}))
		ce, ok := errors.AsType[*plan.ConflictError](err)
		Expect(ok).To(BeTrue(), "%v", err)
		Expect(ce.Conflicts).To(ConsistOf(And(HaveField("Target", "agents"), HaveField("Path", ".agents/skills/review/SKILL.md"),
			HaveField("Reason", "resolves to the same file as claude-code .claude/skills/review/SKILL.md (via symlink); refusing to write it twice"),
			HaveField("Forceable", false))))
		Expect(errs.Hints(err)).To(Equal([]string{run(sc, "claude-code"), run(sc, "agents")}), "each at the scope refused")
		Expect(at(".agents/skills/review/SKILL.md")).NotTo(BeAnExistingFile(), "nothing was written")

		_, err = e.InstallAll(ctx, spec(engine.Options{Targets: []string{"claude-code", "agents"}}))
		Expect(errs.Hints(err)).To(BeEmpty(), "a run with no command to name hints none")
		_, err = e.InstallAll(ctx, spec(engine.Options{Targets: []string{"claude-code"}, TargetRun: run}))
		Expect(err).NotTo(HaveOccurred())
		Expect(at(".agents/skills/review/SKILL.md")).To(BeAnExistingFile(), "written through the link")
	})

	It("a sync of targets that write one real file through such a link hints which file's targets: to edit", func() {
		_, err := e.InstallAll(ctx, spec(engine.Options{Targets: []string{"claude-code", "agents"}}))
		Expect(err).NotTo(HaveOccurred())
		Expect(os.ReadFile(at(".lola/project.yml"))).To(ContainSubstring("targets:"))
		for _, rel := range []string{".claude/skills/review", ".agents/skills/review/SKILL.md", ".lola/lock.json"} { // a fresh clone
			Expect(os.RemoveAll(at(rel))).To(Succeed())
		}
		Expect(os.Symlink(filepath.Join("..", "..", ".agents", "skills", "review"), at(".claude/skills/review"))).To(Succeed())
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }})
		ce, ok := errors.AsType[*plan.ConflictError](err)
		Expect(ok).To(BeTrue(), "%v", err)
		Expect(ce.Conflicts).To(ContainElement(HaveField("SameAs", Not(BeEmpty()))))
		Expect(errs.Hints(err)).To(Equal([]string{"remove claude-code or agents from targets: in project.yml:" + targetsLine(at(".lola/project.yml"))}))
	})

	It("a profile whose targets write one real file through such a link hints its file's targets: line", func() {
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		goYML := filepath.Join(p.UserProfiles, "go.yml")
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\n",
			"go.yml": "version: 1\ntargets: [claude-code, agents]\nmodules:\n  - {name: m, source: " + mod + "}\n"})
		Expect(os.MkdirAll(filepath.Join(p.Home, ".agents", "skills", "review"), 0o755)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(p.Home, ".claude", "skills"), 0o755)).To(Succeed())
		Expect(os.Symlink(filepath.Join(p.Home, ".agents", "skills", "review"), filepath.Join(p.Home, ".claude", "skills", "review"))).To(Succeed())
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: engine.Scope{Kind: target.User}, Profiles: &engine.ProfileChange{Action: engine.ProfileOn, Name: "go"}})
		_, ok := errors.AsType[*plan.ConflictError](err)
		Expect(ok).To(BeTrue(), "%v", err)
		Expect(errs.Hints(err)).To(Equal([]string{"remove claude-code or agents from targets: in " + goYML + ":2"}))
	})
})

// targetsLine is the line of the top-level targets: key of the YAML file at path.
func targetsLine(path string) string {
	b, err := os.ReadFile(path)
	Expect(err).NotTo(HaveOccurred())
	for i, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "targets:") {
			return strconv.Itoa(i + 1)
		}
	}
	Fail("no targets: in " + path)
	return ""
}
