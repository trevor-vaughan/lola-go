package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("an install's mention scan that fails", func() {
	It("degrades to one warn notice for the module, without the file's path, and names no gap", func() {
		tmp := testenv.Isolate()
		v := store.Version{Name: "pack", ID: "v1", ContentDir: filepath.Join(tmp, "gone"), Module: &module.Module{Name: "pack", Skills: []module.Skill{
			{Name: "alpha-one", Dir: "skills/alpha-one"}, {Name: "alpha-two", Dir: "skills/alpha-two"}}}}
		spec := InstallSpec{NameMentions: true}
		scans := mentionReads{}
		targets := editTargets([]*module.Module{v.Module})
		e := &Engine{}
		var notes []Notice
		for range 2 { // two scopes resolving the same version
			sp := &scopePlan{man: &manifest.Manifest{}, resolved: []resolvedRequest{{req: InstallRequest{Arg: "pack"}, v: v}}}
			mods := resolvedMentions(sp, spec, scans)
			gaps := map[string][]mentionGap{"pack": mentionGaps(targets[0].comps, mods["pack"].mentions, config.Exclusions{}, true)}
			notes = e.mentionNotes(notes, ScopeInstall{}, scopeEdits{gaps: gaps}, targets, mods, spec)
		}
		Expect(notes).To(HaveLen(1))
		Expect(notes[0].Level).To(Equal(LevelWarn))
		Expect(notes[0].Text).To(And(HavePrefix("pack: cannot read what its components mention: "), Not(ContainSubstring(tmp))))
	})
})

var _ = Describe("the review's preview when the mention scan fails", func() {
	It("degrades to one warn notice, not an error, and keeps the failure rather than reading again", func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e := New(p)
		proj, src := filepath.Join(tmp, "proj"), filepath.Join(tmp, "src", "pack")
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(src, "skills", "alpha-one"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(src, "lola.yml"), []byte("name: pack\n"), 0o644)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(src, "skills", "alpha-one", "SKILL.md"), []byte("---\ndescription: a\n---\nx\n"), 0o644)).To(Succeed())
		e.Getwd = func() (string, error) { return proj, nil }
		choice := ScopeChoice{Scope: Scope{Kind: target.Project, ProjectDir: proj}, Source: SourceFlag}
		ch, err := e.InstallChoices(context.Background(), ChoicesRequest{Cwd: proj, Scopes: []ScopeChoice{choice}, Modules: []InstallRequest{{Arg: src}}})
		Expect(err).NotTo(HaveOccurred())
		v := ch.versions["pack"]
		good := v.ContentDir
		v.ContentDir = filepath.Join(tmp, "gone")
		ch.versions["pack"] = v
		spec := InstallSpec{Cwd: proj, NameMentions: true, Options: Options{Targets: []string{"claude-code"}},
			Scopes: []ScopeInstall{{ScopeChoice: choice, Targets: []string{"claude-code"}}}}
		pv, err := ch.Preview(spec, []string{"pack"})
		Expect(err).NotTo(HaveOccurred())
		Expect(pv.Selected[choice.Scope.Key()]["pack"]).To(Equal([]string{"skill/alpha-one"}))
		Expect(pv.Notices).To(HaveLen(1))
		Expect(pv.Notices[0].Level).To(Equal(LevelWarn))
		Expect(pv.Notices[0].Text).To(And(HavePrefix("pack: cannot read what its components mention: "), Not(ContainSubstring(tmp))))
		v.ContentDir = good
		ch.versions["pack"] = v
		_, err = ch.Mentions("pack")
		Expect(err).To(MatchError(pv.Notices[0].Text))
	})
})

var _ = Describe("the mention scan's 16 MiB budget", func() {
	It("opens no file once the budget is spent: one that cannot be read is never reached", func() {
		dir := filepath.Join(testenv.Isolate(), "content")
		for _, d := range []string{"skills/aaaa", "skills/zzzz"} {
			Expect(os.MkdirAll(filepath.Join(dir, d), 0o755)).To(Succeed())
		}
		Expect(os.WriteFile(filepath.Join(dir, "skills/aaaa/SKILL.md"), []byte("See zzzz.\n"), 0o644)).To(Succeed())
		for i := range 16 {
			Expect(os.WriteFile(filepath.Join(dir, fmt.Sprintf("skills/aaaa/f%02d.txt", i)), []byte(strings.Repeat("x", 1<<20)), 0o644)).To(Succeed())
		}
		Expect(os.WriteFile(filepath.Join(dir, "skills/zzzz/SKILL.md"), []byte("See aaaa.\n"), 0o644)).To(Succeed())
		locked := filepath.Join(dir, "skills/zzzz/locked.md")
		Expect(os.WriteFile(locked, []byte("aaaa\n"), 0o000)).To(Succeed())
		DeferCleanup(os.Chmod, locked, os.FileMode(0o644))
		v := store.Version{Name: "big", ID: "v1", ContentDir: dir, Module: &module.Module{Name: "big", Skills: []module.Skill{
			{Name: "aaaa", Dir: "skills/aaaa"}, {Name: "zzzz", Dir: "skills/zzzz"}}}}
		Expect(scanMentions(v)).To(Equal(Mentions{{config.KindSkill, "aaaa"}: {{config.KindSkill, "zzzz"}}}))
	})
})

var _ = Describe("an interactive install's mention scans", func() {
	It("read each version once per staged run: the install reuses what the pickers read", func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e := New(p)
		proj, src := filepath.Join(tmp, "proj"), filepath.Join(tmp, "src", "pack")
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		for _, d := range []string{"alpha-one", "alpha-two"} {
			Expect(os.MkdirAll(filepath.Join(src, "skills", d), 0o755)).To(Succeed())
		}
		Expect(os.WriteFile(filepath.Join(src, "lola.yml"), []byte("name: pack\n"), 0o644)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(src, "skills", "alpha-one", "SKILL.md"), []byte("---\ndescription: a\n---\nx\n"), 0o644)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(src, "skills", "alpha-two", "SKILL.md"), []byte("---\ndescription: b\n---\nx\n"), 0o644)).To(Succeed())
		e.Getwd = func() (string, error) { return proj, nil }
		run, err := e.StageInstall()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(run.Close)
		choice := ScopeChoice{Scope: Scope{Kind: target.Project, ProjectDir: proj}, Source: SourceFlag}
		ch, err := e.InstallChoices(context.Background(), ChoicesRequest{Cwd: proj, Scopes: []ScopeChoice{choice}, Run: run, Modules: []InstallRequest{{Arg: src}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(ch.Mentions("pack")).To(Equal(Mentions{}))
		// Were the install to read the version again, it would find this mention.
		Expect(os.WriteFile(filepath.Join(ch.versions["pack"].ContentDir, "skills", "alpha-one", "SKILL.md"), []byte("---\ndescription: a\n---\nalpha-two\n"), 0o644)).To(Succeed())
		sel, err := config.ParseSelector("skill/alpha-one")
		Expect(err).NotTo(HaveOccurred())
		spec := InstallSpec{Cwd: proj, Staged: run, NameMentions: true, Options: Options{Targets: []string{"claude-code"}, DryRun: true},
			Modules: []InstallRequest{{Arg: src, Version: &ch.Versions[0]}},
			Scopes:  []ScopeInstall{{ScopeChoice: choice, Targets: []string{"claude-code"}}}}
		Expect(spec.AddFlagEdits([]SelectionEdit{{Op: EditOnly, Selector: sel}}, false)).To(Succeed())
		res, err := e.InstallAll(context.Background(), spec)
		Expect(err).NotTo(HaveOccurred())
		for _, n := range res.Notices {
			Expect(n.Text).NotTo(ContainSubstring("you did not pick"))
		}
	})
})
