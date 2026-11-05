package engine_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/target"
)

var _ = Describe("lola tidy: refusals and hints", func() {
	var t *tidyEnv
	// flags are the run flags every hint spec runs with (§12): -C comes from a working directory
	// outside the project.
	flags := func(req engine.TidyRequest) engine.TidyRequest {
		req.Targets, req.IncludeTracked, req.Keep = []string{"claude-code"}, true, pats("skill/k")
		return req
	}
	BeforeEach(func() {
		t = newTidyEnv()
		t.install(t.sc)
		t.e.Getwd = func() (string, error) { return t.tmp, nil }
		t.files(t.proj, map[string]string{".claude/commands/tmp-1.md": "x\n", ".claude/skills/a b'c/SKILL.md": "x\n"})
	})
	run := func(flags string) string { return "lola tidy -C " + t.proj + flags }
	const rest = " -t claude-code --include-tracked --keep skill/k"

	It("a dry run takes no lock, writes nothing, ends with the command that applies it, and wins over -y and --force", func() {
		fresh := filepath.Join(t.tmp, "fresh")
		t.files(fresh, map[string]string{".claude/commands/old.md": "x\n"})
		locks, _ := os.ReadDir(t.p.Locks)
		for _, req := range []engine.TidyRequest{{Dry: true}, {Dry: true, Force: true}, {Dry: true, Yes: true}, {Dry: true, Select: pats("command/old")}} {
			req.Scope, req.Targets = engine.Scope{Kind: target.Project, ProjectDir: fresh}, []string{"claude-code"}
			res, err := t.e.Tidy(tidyCtx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.DryRun).To(BeTrue())
			Expect(listed(res.Candidates)).To(Equal([]string{"claude-code command/old"}))
			want := "lola tidy -C " + fresh + " --all -t claude-code -y"
			switch {
			case req.Force:
				want = "lola tidy -C " + fresh + " --all -t claude-code --force"
			case len(req.Select) > 0:
				want = "lola tidy -C " + fresh + " command/old -t claude-code -y"
			}
			Expect(res.Hints).To(Equal([]string{want}))
			Expect(filepath.Join(fresh, ".lola")).NotTo(BeAnExistingFile())
			Expect(filepath.Join(fresh, ".claude", "commands", "old.md")).To(BeAnExistingFile())
			after, _ := os.ReadDir(t.p.Locks)
			Expect(after).To(HaveLen(len(locks)), "no lock file was made")
			Expect(trashRuns(t)).To(BeEmpty())
		}
		By("the output flags as given, never -n")
		res, err := t.e.Tidy(tidyCtx, engine.TidyRequest{Scope: engine.Scope{Kind: target.Project, ProjectDir: fresh}, Targets: []string{"claude-code"},
			Dry: true, Display: []string{"--json", "--no-pager"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Hints).To(Equal([]string{"lola tidy -C " + fresh + " --all -t claude-code -y --json --no-pager"}))

		By("on a terminal, the run without -n, which asks the review: no -y; without names it runs the picker, but --force still needs --all")
		for req, want := range map[*engine.TidyRequest]string{
			{Dry: true, Force: true}:                 "lola tidy -C " + fresh + " --all -t claude-code --force",
			{Dry: true}:                              "lola tidy -C " + fresh + " -t claude-code",
			{Dry: true, Yes: true}:                   "lola tidy -C " + fresh + " -t claude-code -y",
			{Dry: true, Select: pats("command/old")}: "lola tidy -C " + fresh + " command/old -t claude-code",
		} {
			req.Scope, req.Targets, req.Interactive = engine.Scope{Kind: target.Project, ProjectDir: fresh}, []string{"claude-code"}, true
			res, err := t.e.Tidy(tidyCtx, *req)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Hints).To(Equal([]string{want}))
		}
	})

	It("off a terminal without names, tidy lists every candidate and hints --all -y with the run's flags; with names and without -y it hints -y", func() {
		_, err := t.tidy(engine.TidyRequest{Targets: []string{"claude-code"}, IncludeTracked: true})
		Expect(err).To(MatchError("lola tidy needs names or --all when it cannot ask (off a terminal or with --json): 2 items can move"))
		Expect(listed(refusal(err).Candidates)).To(Equal([]string{"claude-code skill/a b'c", "claude-code command/tmp-1"}))
		Expect(errs.Hints(err)).To(Equal([]string{run(" --all -t claude-code --include-tracked -y")}))
		res, err := t.runHint(errs.Hints(err)[0])
		Expect(err).NotTo(HaveOccurred())
		Expect(listed(res.Moved)).To(Equal([]string{"claude-code skill/a b'c", "claude-code command/tmp-1"}))

		t.files(t.proj, map[string]string{".claude/commands/tmp-1.md": "x\n", ".claude/skills/a b'c/SKILL.md": "x\n", ".claude/agents/z.md": "x\n"})
		_, err = t.tidy(flags(engine.TidyRequest{Select: pats("command/tmp-*", "skill/a b'c")}))
		Expect(err).To(MatchError("lola tidy would move 2 items: skill/a b'c (.claude/skills/a b'c), command/tmp-1 (.claude/commands/tmp-1.md); " +
			"it cannot ask (off a terminal or with --json), so it needs -y to move them"))
		Expect(errs.Hints(err)).To(Equal([]string{run(` 'command/tmp-*' 'skill/a b'\''c'` + rest + " -y")}))
		res, err = t.runHint(errs.Hints(err)[0])
		Expect(err).NotTo(HaveOccurred())
		Expect(listed(res.Moved)).To(Equal([]string{"claude-code skill/a b'c", "claude-code command/tmp-1"}))
		Expect(filepath.Join(t.proj, ".claude", "agents", "z.md")).To(BeAnExistingFile(), "the hint moves nothing outside the selection")
	})

	It("--force without names or --all refuses on any terminal, listing every candidate, hinting --all --force, and never asks", func() {
		choose, confirm := never()
		for _, req := range []engine.TidyRequest{{Force: true, Choose: choose, Confirm: confirm}, {Force: true}} {
			req.Targets = []string{"claude-code"}
			_, err := t.tidy(req)
			Expect(err).To(MatchError("--force needs names or --all: 2 items can move"))
			Expect(listed(refusal(err).Candidates)).To(HaveLen(2))
			Expect(errs.Hints(err)).To(Equal([]string{run(" --all -t claude-code --force")}))
		}
		_, err := t.tidy(engine.TidyRequest{Force: true, Targets: []string{"claude-code"}})
		res, err := t.runHint(errs.Hints(err)[0])
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Moved).To(HaveLen(2))
	})

	DescribeTable("--force moves tracked units without asking but never a kept or hard-locked one",
		func(setup func(t *tidyEnv), selector, code string) {
			t.files(t.proj, map[string]string{".claude/agents/tracked.md": "x\n"})
			t.commit(t.proj, ".claude/agents/tracked.md")
			t.files(filepath.Dir(t.p.Keep), map[string]string{"keep.yaml": "version: 1\nprojects:\n  " + t.proj + ":\n    - skill/kept\n"})
			setup(t)
			choose, confirm := never()
			res, err := t.tidy(engine.TidyRequest{All: true, Force: true, Choose: choose, Confirm: confirm})
			Expect(err).NotTo(HaveOccurred())
			Expect(listed(res.Moved)).To(ContainElement("claude-code agent/tracked"))
			Expect(codes(res.Protected)).To(ContainElement(HaveSuffix(" " + code)))
			Expect(listed(res.Moved)).NotTo(ContainElement(HavePrefix("claude-code " + selector[:len(selector)-1])))

			_, err = t.tidy(engine.TidyRequest{Select: pats(selector), Force: true, Choose: choose, Confirm: confirm})
			Expect(err).To(HaveOccurred())
			if code == "kept" {
				Expect(err).To(MatchError(ContainSubstring("is kept by your keep-list entry skill/kept")))
				return
			}
			Expect(err).To(MatchError(HaveSuffix("; lola never moves it")))
			Expect(errs.Hints(err)).To(BeEmpty())
		},
		Entry("instruction_file", func(t *tidyEnv) { t.files(t.proj, map[string]string{".claude/commands/CLAUDE.md": "x\n"}) }, "command/CLAUDE", "instruction_file"),
		Entry("reserved_name", func(t *tidyEnv) { t.files(t.proj, map[string]string{".claude/skills/.git/HEAD": "x\n"}) }, "skill/.git", "reserved_name"),
		Entry("not_a_file", func(t *tidyEnv) { mkfifo(filepath.Join(t.proj, ".claude", "commands", "pipe")) }, "command/pipe", "not_a_file"),
		Entry("unreadable", func(t *tidyEnv) {
			t.files(t.proj, map[string]string{".claude/skills/closed/sub/x.md": "x\n"})
			Expect(os.Chmod(filepath.Join(t.proj, ".claude", "skills", "closed", "sub"), 0)).To(Succeed())
			DeferCleanup(os.Chmod, filepath.Join(t.proj, ".claude", "skills", "closed", "sub"), os.FileMode(0o755))
		}, "skill/closed", "unreadable"),
		Entry("unrecordable_name", func(t *tidyEnv) { t.files(t.proj, map[string]string{".claude/commands/tab\tx.md": "x\n"}) }, "command/tab?x", "unrecordable_name"),
		Entry("unselectable_name", func(t *tidyEnv) { t.files(t.proj, map[string]string{".claude/skills/esc\x1b[31m/SKILL.md": "x\n"}) }, `skill/esc?\[31m`, "unselectable_name"),
		Entry("kept", func(t *tidyEnv) { t.files(t.proj, map[string]string{".claude/skills/kept/SKILL.md": "x\n"}) }, "skill/kept", "kept"),
	)

	Describe("a name that matches nothing tidy can move says exactly why, with a hint that works", func() {
		BeforeEach(func() {
			Expect(os.RemoveAll(filepath.Join(t.proj, ".claude", "skills", "a b'c"))).To(Succeed())
			Expect(os.Remove(filepath.Join(t.proj, ".claude", "commands", "tmp-1.md"))).To(Succeed())
		})
		plain := func(req engine.TidyRequest) engine.TidyRequest {
			req.Targets, req.Keep = []string{"claude-code"}, pats("skill/k")
			return req
		}
		const plainRest = " -t claude-code --keep skill/k"

		It("owned", func() {
			_, err := t.tidy(plain(engine.TidyRequest{Select: pats("skill/review")}))
			Expect(err).To(MatchError("skill/review is installed by module example; lola tidy only moves what lola does not manage"))
			Expect(errs.Hints(err)).To(Equal([]string{"lola uninstall -C " + t.proj + " example"}))
			_, display := t.tidy(plain(engine.TidyRequest{Select: pats("skill/review"), Display: []string{"--json", "--no-pager"}}))
			Expect(errs.Hints(display)).To(Equal([]string{"lola uninstall -C " + t.proj + " example --json --no-pager"}), "the output flags as given")
			_, err = t.runHint(errs.Hints(err)[0])
			Expect(err).NotTo(HaveOccurred())
			Expect(filepath.Join(t.proj, ".claude", "skills", "review")).NotTo(BeAnExistingFile())
		})

		It("owned by the user ledger, at a project at $HOME", func() {
			t.install(t.user)
			home := engine.Scope{Kind: target.Project, ProjectDir: t.home}
			_, err := t.e.Tidy(tidyCtx, engine.TidyRequest{Scope: home, Select: pats("skill/review"), Targets: []string{"claude-code"}})
			Expect(err).To(MatchError("skill/review is installed by module example; lola tidy only moves what lola does not manage"))
			Expect(errs.Hints(err)).To(Equal([]string{"lola uninstall --user example"}))
			_, display := t.e.Tidy(tidyCtx, engine.TidyRequest{Scope: home, Select: pats("skill/review"), Targets: []string{"claude-code"},
				Display: []string{"--plain"}})
			Expect(errs.Hints(display)).To(Equal([]string{"lola uninstall --user example --plain"}))
			_, err = t.runHint(errs.Hints(err)[0])
			Expect(err).NotTo(HaveOccurred())
			Expect(filepath.Join(t.home, ".claude", "skills", "review")).NotTo(BeAnExistingFile())
		})

		It("tracked", func() {
			t.files(t.proj, map[string]string{".claude/skills/notes/SKILL.md": "x\n"})
			t.commit(t.proj, ".claude/skills/notes/SKILL.md")
			_, err := t.tidy(plain(engine.TidyRequest{Select: pats("skill/notes")}))
			Expect(err).To(MatchError("skill/notes is tracked by git"))
			Expect(errs.Hints(err)).To(Equal([]string{run(" skill/notes -t claude-code --include-tracked --keep skill/k")}))
			_, err = t.runHint(errs.Hints(err)[0])
			Expect(err).To(MatchError(HaveSuffix("it cannot ask (off a terminal or with --json), so it needs -y to move them")), "the next row")
			res, err := t.runHint(errs.Hints(err)[0])
			Expect(err).NotTo(HaveOccurred())
			Expect(listed(res.Moved)).To(Equal([]string{"claude-code skill/notes"}))
		})

		It("git-unanswerable", func() {
			claude := filepath.Join(t.proj, ".claude")
			t.files(claude, map[string]string{"skills/notes/SKILL.md": "x\n"})
			t.git(claude, "init", "-q")
			t.noGit()
			_, err := t.tidy(plain(engine.TidyRequest{Select: pats("skill/notes")}))
			Expect(err).To(MatchError("git could not say whether skill/notes is tracked: git is not on PATH"))
			Expect(errs.Hints(err)).To(Equal([]string{run(" skill/notes -t claude-code --include-tracked --keep skill/k")}))
			_, err = t.runHint(errs.Hints(err)[0])
			Expect(err).To(MatchError(HaveSuffix("it cannot ask (off a terminal or with --json), so it needs -y to move them")))
		})

		It("kept by a glob entry that also keeps another candidate", func() {
			t.files(filepath.Dir(t.p.Keep), map[string]string{"keep.yaml": "version: 1\nprojects:\n  " + t.proj + ":\n    - skill/n*\n"})
			t.files(t.proj, map[string]string{".claude/skills/notes/SKILL.md": "x\n", ".claude/skills/nn/SKILL.md": "x\n"})
			_, err := t.tidy(plain(engine.TidyRequest{Select: pats("skill/notes")}))
			Expect(err).To(MatchError("skill/notes is kept by your keep-list entry skill/n* (keep.yaml, project " + t.proj +
				"); removing it also stops keeping: skill/nn"))
			Expect(errs.Hints(err)).To(Equal([]string{run(" skill/notes" + plainRest + " --unkeep 'skill/n*'")}))
			_, err = t.runHint(errs.Hints(err)[0])
			Expect(err).To(MatchError(HaveSuffix("it cannot ask (off a terminal or with --json), so it needs -y to move them")))
			res, err := t.runHint(errs.Hints(err)[0])
			Expect(err).NotTo(HaveOccurred())
			Expect(listed(res.Moved)).To(Equal([]string{"claude-code skill/notes"}))
			Expect(filepath.Join(t.proj, ".claude", "skills", "nn")).To(BeADirectory())
		})

		It("hard-locked", func() {
			t.files(t.proj, map[string]string{".claude/commands/CLAUDE.md": "x\n"})
			_, err := t.tidy(plain(engine.TidyRequest{Select: pats("command/CLAUDE")}))
			Expect(err).To(MatchError("command/CLAUDE is or holds an instruction file (CLAUDE.md, AGENTS.md or GEMINI.md); lola never moves it"))
			Expect(errs.Hints(err)).To(BeEmpty())
		})

		It("nothing", func() {
			_, err := t.tidy(plain(engine.TidyRequest{Select: pats("skill/x")}))
			Expect(err).To(MatchError("skill/x matches nothing lola can tidy at project " + t.proj + " (claude-code)"))
			Expect(errs.Hints(err)).To(Equal([]string{run(" -n" + plainRest)}))
			res, err := t.runHint(errs.Hints(err)[0])
			Expect(err).NotTo(HaveOccurred())
			Expect(res.DryRun).To(BeTrue())
		})
	})

	It("a refusal no hint can fix prints no hint line and hints []", func() {
		t.files(t.proj, map[string]string{".claude/commands/CLAUDE.md": "x\n"})
		_, err := t.tidy(engine.TidyRequest{Select: pats("command/CLAUDE")})
		Expect(err).To(HaveOccurred())
		Expect(errs.Hints(err)).To(BeEmpty(), "hard-locked")

		_, err = t.tidy(engine.TidyRequest{Unkeep: pats("skill/x")})
		Expect(err).To(MatchError("skill/x is not on the keep-list of project " + t.proj + "; it holds nothing"))
		Expect(errs.Hints(err)).To(BeEmpty(), "--unkeep of an absent entry")

		fresh := filepath.Join(t.tmp, "fresh")
		Expect(os.MkdirAll(fresh, 0o755)).To(Succeed())
		_, err = t.e.Tidy(tidyCtx, engine.TidyRequest{Scope: engine.Scope{Kind: target.Project, ProjectDir: fresh}})
		Expect(err).To(MatchError("project " + fresh + " lists no targets and lola installed nothing there"))
		Expect(errs.Hints(err)).To(BeEmpty(), "no assistant detected")

		t.git(t.proj, "init", "-q")
		t.noGit()
		_, err = t.tidy(engine.TidyRequest{All: true, Yes: true})
		Expect(err).To(MatchError(HavePrefix("lola could not check whether git tracks .lola/lock.json")))
		Expect(errs.Hints(err)).To(BeEmpty(), "a lock.json git cannot check")
	})

	It("a hint that would carry a root untrusted.Text alters is not given: no hint line, hints []", func() {
		odd := engine.Scope{Kind: target.Project, ProjectDir: filepath.Join(t.tmp, "q\x1b]0;x\a")}
		t.files(odd.ProjectDir, map[string]string{".claude/commands/old.md": "x\n"})
		req := engine.TidyRequest{Scope: odd, Targets: []string{"claude-code"}}
		_, err := t.e.Tidy(tidyCtx, req)
		Expect(err).To(MatchError("lola tidy needs names or --all when it cannot ask (off a terminal or with --json): 1 item can move"))
		Expect(errs.Hints(err)).To(BeEmpty())
		Expect(refusal(err).Hints).To(BeEmpty())

		dry := req
		dry.Dry = true
		res, err := t.e.Tidy(tidyCtx, dry)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Hints).To(BeEmpty())

		both := req
		both.All, both.Select = true, pats("command/old")
		_, err = t.e.Tidy(tidyCtx, both)
		Expect(err).To(MatchError("name items or pass --all, not both"))
		Expect(errs.Hints(err)).To(BeEmpty())

		err = t.e.TidyOneScope(req)
		Expect(errs.Hints(err)).To(Equal([]string{"lola tidy --user -t claude-code"}), "the user-scope run still works")
		Expect(filepath.Join(odd.ProjectDir, ".claude", "commands", "old.md")).To(BeAnExistingFile())
	})

	It("--scope both, --all with names, and a name both moved and kept are refused with hints that work", func() {
		err := t.e.TidyOneScope(flags(engine.TidyRequest{Scope: t.sc, Select: pats("command/tmp-*")}))
		Expect(err).To(MatchError("lola tidy acts on one scope per run"))
		Expect(errs.Hints(err)).To(Equal([]string{run(" 'command/tmp-*'" + rest),
			"lola tidy --user 'command/tmp-*'" + rest}))
		_, err = t.runHint(errs.Hints(err)[0])
		Expect(err).To(MatchError(HaveSuffix("it cannot ask (off a terminal or with --json), so it needs -y to move them")))

		_, err = t.tidy(flags(engine.TidyRequest{All: true, Select: pats("command/tmp-*")}))
		Expect(err).To(MatchError("name items or pass --all, not both"))
		Expect(errs.Hints(err)).To(Equal([]string{run(" 'command/tmp-*'" + rest), run(" --all" + rest)}))
		for _, h := range errs.Hints(err) {
			_, err := t.runHint(h)
			Expect(err).To(MatchError(Or(HaveSuffix("it cannot ask (off a terminal or with --json), so it needs -y to move them"), HavePrefix("lola tidy needs names or --all when it cannot ask"))))
		}

		err = t.e.TidyOneScope(engine.TidyRequest{Scope: t.sc, All: true, Yes: true, Targets: []string{"claude-code"}})
		Expect(errs.Hints(err)).To(Equal([]string{run(" --all -t claude-code -y"), "lola tidy --user --all -t claude-code -y"}))
		res, err := t.runHint(errs.Hints(err)[0])
		Expect(err).NotTo(HaveOccurred(), "--scope both --all -y: the hint keeps --all")
		Expect(res.Moved).To(HaveLen(2))
		t.files(t.proj, map[string]string{".claude/commands/tmp-1.md": "x\n", ".claude/skills/a b'c/SKILL.md": "x\n"})

		both := flags(engine.TidyRequest{Select: pats("command/tmp-*", "skill/a b'c")})
		both.Keep = append(both.Keep, pats("command/tmp-*")...)
		_, err = t.tidy(both)
		Expect(err).To(MatchError("command/tmp-* is named to move and to keep"))
		Expect(errs.Hints(err)).To(Equal([]string{
			run(` 'skill/a b'\''c' -t claude-code --include-tracked --keep skill/k --keep 'command/tmp-*'`),
			run(` 'command/tmp-*' 'skill/a b'\''c'` + rest)}))
		for _, h := range errs.Hints(err) {
			_, err := t.runHint(h)
			Expect(err).To(MatchError(HaveSuffix("it cannot ask (off a terminal or with --json), so it needs -y to move them")), h)
		}
	})

	It("with no targets, hints name each detected assistant; -t naming an unlisted target scans it", func() {
		fresh := filepath.Join(t.tmp, "fresh")
		t.files(fresh, map[string]string{".claude/commands/old.md": "x\n", ".gemini/commands/old.toml": "x\n"})
		Expect(os.WriteFile(filepath.Join(t.tmp, "bin", "opencode"), []byte("#!/bin/sh\n"), 0o755)).To(Succeed())
		sc := engine.Scope{Kind: target.Project, ProjectDir: fresh}
		_, err := t.e.Tidy(tidyCtx, engine.TidyRequest{Scope: sc, Keep: pats("skill/k")})
		Expect(err).To(MatchError("project " + fresh + " lists no targets and lola installed nothing there"))
		Expect(errs.Hints(err)).To(Equal([]string{"lola tidy -C " + fresh + " -t claude-code --keep skill/k",
			"lola tidy -C " + fresh + " -t gemini-cli --keep skill/k", "lola tidy -C " + fresh + " -t opencode --keep skill/k"}))

		t.files(t.proj, map[string]string{".opencode/agents/old.md": "x\n"})
		res := t.dry(t.sc, "opencode")
		Expect(res.Targets).To(Equal([]string{"opencode"}))
		Expect(listed(res.Candidates)).To(Equal([]string{"opencode agent/old"}))
	})

	It("a run with --keep and --force but no names or --all is keep-only", func() {
		choose, confirm := never()
		res, err := t.tidy(engine.TidyRequest{Keep: pats("command/tmp-*"), Force: true, Choose: choose, Confirm: confirm})
		Expect(err).NotTo(HaveOccurred(), "rule 4 comes before rule 5's refusal")
		Expect(res.Moved).To(BeEmpty())
		Expect(res.KeepAdded).To(Equal([]string{"command/tmp-*"}))
		Expect(filepath.Join(t.proj, ".claude", "skills", "a b'c")).To(BeADirectory())
		Expect(trashRuns(t)).To(BeEmpty())
	})

	It("--keep or --unkeep alone moves nothing and asks nothing", func() {
		choose, confirm := never()
		res, err := t.tidy(engine.TidyRequest{Keep: pats("command/tmp-*"), Choose: choose, Confirm: confirm})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Moved).To(BeEmpty())
		Expect(res.KeepAdded).To(Equal([]string{"command/tmp-*"}))
		Expect(codes(res.Protected)).To(Equal([]string{"claude-code command/tmp-1 kept"}))
		Expect(filepath.Join(t.proj, ".claude", "commands", "tmp-1.md")).To(BeAnExistingFile())

		res, err = t.tidy(engine.TidyRequest{Unkeep: pats("command/tmp-*"), Force: true, Choose: choose, Confirm: confirm})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Moved).To(BeEmpty())
		Expect(res.KeepRemoved).To(Equal([]string{"command/tmp-*"}))
		Expect(os.ReadFile(t.p.Keep)).NotTo(ContainSubstring("tmp-*"))
	})

	It("an empty scan exits 0 with \"nothing to tidy\", also with --all and --force --all", func() {
		Expect(os.RemoveAll(filepath.Join(t.proj, ".claude", "skills", "a b'c"))).To(Succeed())
		Expect(os.Remove(filepath.Join(t.proj, ".claude", "commands", "tmp-1.md"))).To(Succeed())
		t.files(t.proj, map[string]string{".claude/commands/CLAUDE.md": "x\n"})
		for _, req := range []engine.TidyRequest{{}, {All: true}, {All: true, Force: true}, {Force: true}} {
			res, err := t.tidy(req)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Moved).To(BeEmpty())
			Expect(res.Trash).To(BeEmpty())
			Expect(noticeTexts(res.Notices)).To(ContainElement("nothing to tidy at project " + t.proj + " (1 locked)"))
		}
		Expect(trashRuns(t)).To(BeEmpty())
	})
})
