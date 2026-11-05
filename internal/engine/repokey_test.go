package engine_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("Pick-P2-a: a project's base picks are keyed by its repository", func() {
	const repoKey = "git:example.com/o/r#."
	var (
		tmp, src string
		p        paths.Paths
		e        *engine.Engine
		ctx      = context.Background()
		claude   = engine.Options{Targets: []string{"claude-code"}}
	)
	git := func(at string, args ...string) {
		GinkgoHelper()
		out, err := exec.Command("git", append([]string{"-C", at}, args...)...).CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))
	}
	// clone makes a work tree at dir whose origin is remote.
	clone := func(dir, remote string) string {
		GinkgoHelper()
		Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
		git(dir, "init", "-q")
		git(dir, "remote", "add", "origin", remote)
		return dir
	}
	only := func(s string) engine.SelectionEdit {
		GinkgoHelper()
		sel, err := config.ParseSelector(s)
		Expect(err).NotTo(HaveOccurred())
		return engine.SelectionEdit{Op: engine.EditOnly, Selector: sel, Exact: true}
	}
	install := func(proj string, edits ...engine.SelectionEdit) *engine.InstallResult {
		GinkgoHelper()
		sc := engine.Scope{Kind: target.Project, ProjectDir: proj}
		spec := engine.InstallSpec{ConfirmRemovals: confirmAll, Cwd: proj, Options: claude, Modules: []engine.InstallRequest{{Arg: src}},
			Scopes: []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: sc, Source: engine.SourceFlag}, Targets: claude.Targets}}}
		Expect(spec.AddFlagEdits(edits, false)).To(Succeed())
		res, err := e.InstallAll(ctx, spec)
		Expect(err).NotTo(HaveOccurred())
		return res
	}
	exists := func(proj, rel string) bool {
		_, err := os.Stat(filepath.Join(proj, rel))
		return err == nil
	}
	selections := func() string {
		GinkgoHelper()
		b, err := os.ReadFile(p.Selections)
		Expect(err).NotTo(HaveOccurred())
		return string(b)
	}

	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		src = filepath.Join(tmp, "src", "example")
		writeTree(src, exampleModule)
	})

	It("Pick-P2-a: a fresh clone of the repository at another path installs only the pick", func() {
		a := clone(filepath.Join(tmp, "a", "r"), "https://user:tok@example.com/o/r.git")
		res := install(a, only("command/ship"))
		Expect(selections()).To(ContainSubstring("projects:\n  " + repoKey + ":\n    example:\n"))
		Expect(selections()).NotTo(ContainSubstring("tok"))
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo,
			Text: "picks for example saved in selections.yaml projects[" + repoKey + "]: they are yours, not your teammates'; a project profile shares picks with your team"}))

		b := clone(filepath.Join(tmp, "b", "elsewhere"), "git@example.com:o/r")
		install(b)
		Expect(exists(b, ".claude/commands/ship.md")).To(BeTrue())
		Expect(exists(b, ".claude/skills/review/SKILL.md")).To(BeFalse(), "the pick follows the repository")
	})

	It("Pick-P2-a: an scp-style origin carrying a token keeps the path key, and the token reaches neither the file nor the output", func() {
		a := clone(filepath.Join(tmp, "a", "r"), "x-access-token:tok@example.com:o/r")
		res := install(a, only("command/ship"))
		Expect(selections()).To(ContainSubstring("projects:\n  " + a + ":\n"))
		Expect(selections()).NotTo(ContainSubstring("tok"))
		for _, n := range res.Notices {
			Expect(n.Text).NotTo(ContainSubstring("tok@"))
		}
	})

	It("Pick-P2-a: a section under the project's path still applies, and the next pick write rewrites it to the repository key", func() {
		a := clone(filepath.Join(tmp, "a", "r"), "https://example.com/o/r")
		other := "  /elsewhere: # theirs\n    zeta:\n      skip: [agent/x]\n"
		Expect(os.MkdirAll(filepath.Dir(p.Selections), 0o700)).To(Succeed())
		Expect(os.WriteFile(p.Selections, []byte("version: 1\nprojects:\n"+other+"  "+a+":\n    example:\n      only: [command/ship]\n"), 0o600)).To(Succeed())
		install(a)
		Expect(exists(a, ".claude/skills/review/SKILL.md")).To(BeFalse(), "the path-keyed only: applies")
		Expect(exists(a, ".claude/commands/ship.md")).To(BeTrue())

		install(a, only("skill/review"))
		Expect(selections()).To(Equal("version: 1\nprojects:\n" + other + "  " + repoKey + ":\n    example:\n      only: [skill/review]\n"))
		Expect(exists(a, ".claude/skills/review/SKILL.md")).To(BeTrue())
	})

	It("Pick-P2-a: the ignored-section warning names the path key as the file writes it while the entry is still under it", func() {
		a := clone(filepath.Join(tmp, "a", "r"), "https://example.com/o/r")
		Expect(os.Symlink(filepath.Join(tmp, "a"), filepath.Join(tmp, "link"))).To(Succeed())
		linked := filepath.Join(tmp, "link", "r")
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: example, source: " + src + "}\n"})
		writeTree(filepath.Dir(p.Selections), map[string]string{"selections.yaml": "version: 1\nprojects:\n  " + linked + ":\n    example:\n      skip: [skill/review]\n"})
		led := ledger.New()
		led.Profile = &ledger.Active{Name: "user:go", By: ledger.ByOn}
		Expect(led.Save(filepath.Join(a, ".lola", "lock.json"))).To(Succeed())
		e.Getwd = func() (string, error) { return a, nil }
		res, err := e.Sync(ctx, engine.SyncRequest{Scope: engine.Scope{Kind: target.Project, ProjectDir: a},
			ConfirmRemovals: func([]engine.Removal) (bool, error) { return true, nil }})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(HaveField("Text", HavePrefix("selections.yaml projects["+linked+"] example is ignored: profile go provides it; delete it from "))))
	})

	It("Pick-P2-a: lola check in a clone git cannot read applies the path key's picks instead of refusing", func() {
		a := filepath.Join(tmp, "a", "r")
		writeTree(a, map[string]string{".git": "gitdir: nowhere\n"})
		writeTree(filepath.Dir(p.Selections), map[string]string{"selections.yaml": "version: 1\nprojects:\n  " + a + ":\n    example:\n      only: [command/ship]\n"})
		rep, err := e.Check(ctx, engine.Scope{Kind: target.Project, ProjectDir: a}, a, []string{src}, []string{"claude-code"}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(rep.Modules).To(HaveLen(1))
		Expect(rep.Modules[0].Skipped).To(ContainElement("skill/review"))
		Expect(rep.Findings).To(ContainElement(And(HaveField("Severity", target.SeverityWarn), HaveField("Code", "picks-keyed-by-path"),
			HaveField("Text", "git could not read the repository of "+a+": fatal: not a git repository: "+filepath.Join(a, "nowhere")+"; picks for this project are keyed by its path"),
			HaveField("Hint", ""))))
	})

	It("Pick-P2-a: lola info in a clone git cannot read says its picks are keyed by path", func() {
		a := filepath.Join(tmp, "a", "r")
		writeTree(a, map[string]string{".git": "gitdir: nowhere\n"})
		info, err := e.Info(ctx, engine.InfoRequest{Cwd: a, Arg: src, Scope: engine.ScopeRequest{Project: true, Cwd: a}, Targets: claude.Targets,
			Rerun: func(...string) string { return "" }, Install: func(string, string, string, engine.TrustKinds) string { return "" }})
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Install).To(HaveLen(1))
		Expect(info.Install[0].Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn, Text: "git could not read the repository of " + a +
			": fatal: not a git repository: " + filepath.Join(a, "nowhere") + "; picks for this project are keyed by its path"}))
	})

	It("Pick-P2-a: lola list labels a pick by the key the file writes while the entry is still under the path", func() {
		a := clone(filepath.Join(tmp, "a", "r"), "https://example.com/o/r")
		Expect(os.Symlink(filepath.Join(tmp, "a"), filepath.Join(tmp, "link"))).To(Succeed())
		linked := filepath.Join(tmp, "link", "r")
		writeTree(filepath.Dir(p.Selections), map[string]string{"selections.yaml": "version: 1\nprojects:\n  " + linked + ":\n    example:\n      only: [command/ship]\n"})
		install(a)
		rows, _, err := e.Status(ctx, engine.Scope{Kind: target.Project, ProjectDir: a}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(ContainElement(And(HaveField("Name", "example"), HaveField("Only", []string{"command/ship"}),
			HaveField("OnlyFrom", "selections.yaml projects["+linked+"]"))))
	})

	It("Pick-P2-a: says picks are personal only when the run's flags write them, not for picker answers or a plain install", func() {
		a := clone(filepath.Join(tmp, "a", "r"), "https://example.com/o/r")
		personal := HaveField("Text", ContainSubstring("a project profile shares picks"))
		sc := engine.Scope{Kind: target.Project, ProjectDir: a}
		sel, err := config.ParseSelector("command/ship")
		Expect(err).NotTo(HaveOccurred())
		picker := engine.InstallSpec{ConfirmRemovals: confirmAll, Cwd: a, Options: claude, Modules: []engine.InstallRequest{{Arg: src}},
			Scopes: []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: sc, Source: engine.SourceFlag}, Targets: claude.Targets,
				Edits: []engine.SelectionEdit{{Op: engine.EditSkip, Selector: sel}}}}}
		res, err := e.InstallAll(ctx, picker)
		Expect(err).NotTo(HaveOccurred())
		Expect(selections()).To(ContainSubstring("skip: [command/ship]"), "the picker's answer is saved")
		Expect(res.Notices).NotTo(ContainElement(personal))
		Expect(install(a).Notices).NotTo(ContainElement(personal))
		Expect(install(a, only("skill/review")).Notices).To(ContainElement(personal))
	})

	It("Pick-P2-a: a project with no origin keeps its path key, printed through untrusted.Text", func() {
		proj := filepath.Join(tmp, "my\u202eproj")
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		res := install(proj, only("command/ship"))
		Expect(selections()).To(ContainSubstring("projects:\n  "))
		Expect(selections()).NotTo(ContainSubstring("\n  git:"))
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo,
			Text: "picks for example saved in selections.yaml projects[" + filepath.Join(tmp, "my?proj") + "]: they are yours, not your teammates'; a project profile shares picks with your team"}))
	})
})
