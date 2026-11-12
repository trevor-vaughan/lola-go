package cli_test

// One spec per row of the tidy, trash and cache clean transition tables in
// docs/architecture/tui-flows.md, named "tidy: <from> --<input>--> <to>", "trash: …" and
// "cache clean: …", the input followed by the row's guard in parentheses where rows share a from
// and an input. Rows an existing spec pins already:
//
// also pinned (tidy: start --run--> scope): scope_paths_test.go "scope picker: start --run (lola tidy)--> scope"
// also pinned (tidy: start --run (--json or no terminal)--> refused): tidy_test.go "--json on a terminal never opens the picker: it refuses as off one, its hint keeping --json"
// also pinned (tidy: start --names--> confirm): tidy_test.go "asks the review on a terminal when names are given, and declining moves nothing"
// also pinned (tidy: start --run (--force, no names)--> refused; tidy: keep --answer (-y)--> done): tidy_test.go "-y on a terminal without names still runs the picker"
// also pinned (tidy: start --run (--scope both)--> refused): tidy_test.go "--scope both is refused with the run at each scope"
// also pinned (tidy: keep --empty submit (nothing moves)--> keep): huh_internal_test.go "a picker that needs a pick refuses none, naming the keys, and still cancels at end of input" Entry("tidy's keep-list when nothing moves")
// also pinned (tidy: keep --esc--> move): tidy_test.go "goes back from the keep picker to the move picker on Esc, its answer preselected"
// also pinned (trash: start --run (runs in the trash, on a terminal)--> pick): trash_test.go "on a terminal, bare lola trash opens a picker: runs newest first as [whole run] rows, each followed by its files, credential copies marked first"
// also pinned (trash: start --run (--json, --plain or no terminal)--> done): trash_test.go "opens no picker off a terminal, or under --json or --plain, and lists the runs"
// also pinned (trash: start --run (--restore)--> done): trash_test.go "restores a run's entries, a relative original resolved against the working directory"
// also pinned (trash: start --run (--restore, an original in the way)--> refused): trash_test.go "refuses a restore over what is there, hinting --force, and -n prints the plan"
// also pinned (trash: start --run (--empty)--> empty): confirm_test.go "c4: on a terminal without --json, <cmd> asks after the plan and prints the human result" Entry("trash --empty")
// also pinned (trash: start --run (--empty -n)--> done): trash_test.go "empties runs older than an age, -n listing them and deleting nothing"
// also pinned (trash: start --run (--empty, no terminal)--> refused): confirm_test.go "c2: off a terminal without --json, <cmd> prints the plan, then the error and its hint" Entry("trash --empty")
// also pinned (trash: pick --Restore ticked--> restore): trash_test.go "restores what the picker ticks with the plan lola trash <run> [<original>...] --restore makes, shown before it asks"
// also pinned (trash: pick --Restore ticked (an original in the way)--> restore; trash: restore --Restore with --force--> force; trash: force --yes--> done; trash: force --no--> cancelled): trash_test.go "offers Restore with --force as a choice of its own when an original exists, and asks before moving what is there"
// also pinned (trash: pick --Restore ticked (two runs hold one original)--> pick): trash_test.go "asks the picker again when two ticked runs would restore the same original, naming it and both runs"
// also pinned (trash: pick --Restore ticked (nothing left to restore)--> done): trash_test.go "says there is nothing to restore when what was ticked is gone from the trash"
// also pinned (trash: pick --Empty ticked runs--> empty; trash: empty --yes--> done; trash: empty --no--> cancelled): trash_test.go "empties the runs ticked whole after asking, and asks the picker again when nothing or only a run's files are ticked"
// also pinned (cache clean: start --run (something to remove, on a terminal)--> confirm; cache clean: confirm --yes--> done): confirm_test.go "c4: on a terminal without --json, <cmd> asks after the plan and prints the human result" Entry("cache clean")
// also pinned (cache clean: start --run (the plan taller than the terminal)--> plan; cache clean: plan --q--> confirm): confirm_test.go "c8: on a terminal, <cmd>'s plan taller than the terminal opens in the viewer, in full, before the question" Entry("cache clean")
// also pinned (cache clean: plan --ctrl+c--> cancelled): confirm_test.go "viewer: viewer --ctrl+c (a question follows)--> cancelled"
// also pinned (cache clean: start --run (--json, on a terminal)--> confirm): confirm_test.go "c3: on a terminal under --json, <cmd> prints the plan and the question on stderr and one JSON document on stdout" Entry("cache clean")
// also pinned (cache clean: start --run (no terminal)--> refused): confirm_test.go "c2: off a terminal without --json, <cmd> prints the plan, then the error and its hint" Entry("cache clean")
// also pinned (cache clean: confirm --yes (an item changed since the plan)--> done): clean_test.go "a confirmed clean whose every item changed says nothing was removed"
// also pinned (cache clean: confirm --no--> cancelled): confirm_test.go "c5: declining <cmd> on a terminal writes nothing and exits 0" Entry("cache clean")

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/cli"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// tidyAgain answers a tidy picker asked again with what it preselects, as Enter does.
func tidyAgain(st cli.TidyPickStep) ([]string, error) { return st.Preselect, nil }

// keepAgain answers the keep picker asked again with what it preselects, as Enter does.
func keepAgain(st cli.TidyKeepStep) ([]string, error) { return st.Preselect, nil }

var _ = Describe("tidy paths", func() {
	var tmp, proj, keepYAML, old, tmpMd string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj = filepath.Join(tmp, "proj")
		mod := filepath.Join(tmp, "example")
		writeTree(mod, map[string]string{"commands/ship.md": "Ship $ARGUMENTS\n"})
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		r := lola("install", mod, "-C", proj, "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		writeTree(proj, map[string]string{".claude/skills/old/SKILL.md": "x\n", ".claude/commands/tmp.md": "x\n"})
		keepYAML = filepath.Join(tmp, "config", "lola", "keep.yaml")
		old, tmpMd = filepath.Join(proj, ".claude", "skills", "old"), filepath.Join(proj, ".claude", "commands", "tmp.md")
		GinkgoT().Chdir(tmp)
	})

	// untouched asserts the run moved nothing and wrote no keep-list.
	untouched := func() {
		GinkgoHelper()
		Expect(old).To(BeADirectory())
		Expect(tmpMd).To(BeARegularFile())
		Expect(keepYAML).NotTo(BeAnExistingFile())
	}

	It("tidy: start --scope flag--> move", func() {
		p := &scripted{tidyPick: []func(cli.TidyPickStep) ([]string, error){aborted[cli.TidyPickStep, []string]}}
		r := lolaP(p, "tidy", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"tidy-pick"}), "no scope picker")
		Expect(tidyLabels(p.tidyPickSteps[0].Options)).To(Equal([]string{"claude-code  skill/old  .claude/skills/old/",
			"claude-code  command/tmp  .claude/commands/tmp.md"}))
		Expect(p.tidyPickSteps[0].Preselect).To(BeEmpty())
		untouched()
	})

	It("tidy: start --scope flag (nothing to tidy)--> done", func() {
		Expect(os.RemoveAll(old)).To(Succeed())
		Expect(os.Remove(tmpMd)).To(Succeed())
		p := &scripted{}
		r := lolaP(p, "tidy", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(BeEmpty())
		Expect(r.out).To(ContainSubstring("nothing to tidy at project " + proj))
		Expect(keepYAML).NotTo(BeAnExistingFile())
	})

	It("tidy: start --run (--all)--> confirm", func() {
		p := &scripted{confirm: []bool{false}}
		r := lolaP(p, "tidy", "--all", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"confirm:Move 2 items to the trash?"}), "no picker")
		Expect(p.confirmNavs[0].Back).To(BeFalse(), "a first screen: Esc stays")
		untouched()
	})

	It("tidy: start --names (-y)--> done", func() {
		p := &scripted{}
		r := lolaP(p, "tidy", "skill/old", "-y", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(BeEmpty())
		Expect(r.out).To(ContainSubstring("✓ moved 1 item"))
		Expect(old).NotTo(BeAnExistingFile())
		Expect(tmpMd).To(BeARegularFile())
	})

	It("tidy: start --names (--force)--> done", func() {
		p := &scripted{}
		r := lolaP(p, "tidy", "skill/old", "--force", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(BeEmpty(), "--force never asks")
		Expect(old).NotTo(BeAnExistingFile())
	})

	It("tidy: start --run (-n)--> dry run", func() {
		p := &scripted{}
		r := lolaP(p, "tidy", "-n", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(BeEmpty(), "a dry run asks nothing")
		Expect(r.out).To(ContainSubstring("  → claude-code  skill/old  .claude/skills/old/\n"))
		Expect(r.out).To(ContainSubstring("  → claude-code  command/tmp  .claude/commands/tmp.md\n"))
		untouched()
	})

	It("tidy: start --run (--keep or --unkeep)--> done", func() {
		p := &scripted{}
		r := lolaP(p, "tidy", "--keep", "command/tmp", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(BeEmpty(), "the keep-list is edited with no question, on a terminal too")
		Expect(os.ReadFile(keepYAML)).To(ContainSubstring("- command/tmp\n"))
		Expect(old).To(BeADirectory(), "nothing moves")
		r = lolaP(p, "tidy", "--unkeep", "command/tmp", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(BeEmpty())
		Expect(os.ReadFile(keepYAML)).NotTo(ContainSubstring("command/tmp"))
	})

	It("tidy: move --answer--> keep", func() {
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		held := false
		sp := &scripted{tidyPick: []func(cli.TidyPickStep) ([]string, error){func(st cli.TidyPickStep) ([]string, error) {
			locks, err := filepath.Glob(filepath.Join(p.Locks, "*.lock"))
			Expect(err).NotTo(HaveOccurred())
			for _, l := range locks {
				ok, err := flock.New(l).TryLock()
				Expect(err).NotTo(HaveOccurred())
				held = held || !ok
			}
			return tidyIDs(st.Options, "skill/old"), nil
		}}, tidyKeep: []func(cli.TidyKeepStep) ([]string, error){aborted[cli.TidyKeepStep, []string]}}
		r := lolaP(sp, "tidy", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(held).To(BeTrue(), "the scope's lock is held while the picker waits")
		Expect(sp.calls).To(Equal([]string{"tidy-pick", "tidy-keep"}))
		Expect(tidyLabels(sp.tidyKeepSteps[0].Options)).To(Equal([]string{"command/tmp  claude-code"}), "the rest")
		Expect(sp.tidyKeepSteps[0].Required).To(BeFalse())
		untouched()
	})

	It("tidy: move --answer (every name ticked)--> confirm", func() {
		p := &scripted{tidyPick: tidyPicks("claude-code"), confirm: []bool{false}}
		r := lolaP(p, "tidy", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"tidy-pick", "confirm:Move 2 items to the trash?"}), "no keep picker")
		untouched()
	})

	It("tidy: move --answer (-y, every name ticked)--> done", func() {
		p := &scripted{tidyPick: tidyPicks("claude-code")}
		r := lolaP(p, "tidy", "-y", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"tidy-pick"}), "the move follows the pick")
		Expect(old).NotTo(BeAnExistingFile())
		Expect(tmpMd).NotTo(BeAnExistingFile())
	})

	It("tidy: move --empty submit--> keep", func() {
		p := &scripted{tidyPick: tidyPicks(), tidyKeep: tidyKeeps("command/tmp"), confirm: []bool{true}}
		r := lolaP(p, "tidy", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"tidy-pick", "tidy-keep", "confirm:Keep 1?"}))
		Expect(p.tidyKeepSteps[0].Required).To(BeTrue(), "nothing moves: the keep picker needs a pick")
		Expect(old).To(BeADirectory())
		Expect(os.ReadFile(keepYAML)).To(ContainSubstring("- command/tmp\n"))
	})

	It("tidy: move --ctrl+c--> cancelled", func() {
		p := &scripted{tidyPick: []func(cli.TidyPickStep) ([]string, error){aborted[cli.TidyPickStep, []string]}}
		r := lolaP(p, "tidy", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(ContainSubstring("✗ cancelled — nothing was changed"))
		untouched()
	})

	It("tidy: keep --answer--> confirm", func() {
		p := &scripted{tidyPick: tidyPicks("skill/old"), tidyKeep: tidyKeeps("command/tmp"), confirm: []bool{false}}
		r := lolaP(p, "tidy", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"tidy-pick", "tidy-keep", "confirm:Move 1 item to the trash and keep 1?"}))
		Expect(r.err).To(ContainSubstring("  → keep command/tmp\n"))
		Expect(r.err).To(ContainSubstring("Equivalent command:\n  lola tidy -C " + proj + " skill/old --keep command/tmp\n"))
		untouched()
	})

	It("tidy: keep --empty submit (something moves)--> confirm", func() {
		p := &scripted{tidyPick: tidyPicks("skill/old"), tidyKeep: tidyKeeps(), confirm: []bool{true}}
		r := lolaP(p, "tidy", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"tidy-pick", "tidy-keep", "confirm:Move 1 item to the trash?"}))
		Expect(old).NotTo(BeAnExistingFile())
		Expect(tmpMd).To(BeARegularFile())
		Expect(keepYAML).NotTo(BeAnExistingFile())
	})

	It("tidy: keep --ctrl+c--> cancelled", func() {
		p := &scripted{tidyPick: tidyPicks("skill/old"), tidyKeep: []func(cli.TidyKeepStep) ([]string, error){aborted[cli.TidyKeepStep, []string]}}
		r := lolaP(p, "tidy", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"tidy-pick", "tidy-keep"}))
		untouched()
	})

	It("tidy: confirm --yes--> done", func() {
		p := &scripted{tidyPick: tidyPicks("skill/old"), tidyKeep: tidyKeeps("command/tmp"), confirm: []bool{true}}
		r := lolaP(p, "tidy", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("✓ moved 1 item"))
		Expect(r.out).To(ContainSubstring("keep-list: added command/tmp (project " + proj + ")"))
		Expect(old).NotTo(BeAnExistingFile())
		Expect(tmpMd).To(BeARegularFile())
		Expect(os.ReadFile(keepYAML)).To(ContainSubstring("- command/tmp\n"))
		trash, err := os.ReadDir(filepath.Join(tmp, "state", "lola", "trash"))
		Expect(err).NotTo(HaveOccurred())
		Expect(trash).To(HaveLen(1), "one trash run")
	})

	It("tidy: confirm --yes (a move fails)--> failed", func() {
		if os.Geteuid() == 0 {
			Skip("root moves out of a read-only directory")
		}
		commands := filepath.Dir(tmpMd)
		Expect(os.Chmod(commands, 0o555)).To(Succeed())
		DeferCleanup(os.Chmod, commands, os.FileMode(0o755))
		p := &scripted{confirm: []bool{true}}
		r := lolaP(p, "tidy", "skill/old", "command/tmp", "--keep", "command/zz", "-C", proj)
		Expect(r.code).To(Equal(1))
		Expect(p.calls).To(Equal([]string{"confirm:Move 2 items to the trash and keep 1?"}))
		Expect(r.err).To(ContainSubstring("permission denied"))
		Expect(old).NotTo(BeAnExistingFile(), "what moved before the failure stays in the trash")
		Expect(tmpMd).To(BeARegularFile())
		Expect(os.ReadFile(keepYAML)).To(ContainSubstring("- command/zz\n"), "the keep-list is written all the same")
	})

	It("tidy: confirm --no--> cancelled", func() {
		p := &scripted{tidyPick: tidyPicks("skill/old"), tidyKeep: tidyKeeps("command/tmp"), confirm: []bool{false}}
		r := lolaP(p, "tidy", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(ContainSubstring("✗ cancelled — nothing was changed"))
		untouched()
	})

	It("tidy: confirm --ctrl+c--> cancelled", func() {
		p := &scripted{tidyPick: tidyPicks("skill/old"), tidyKeep: tidyKeeps("command/tmp"), confirmCancels: 1}
		r := lolaP(p, "tidy", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"tidy-pick", "tidy-keep", "confirm:Move 1 item to the trash and keep 1?"}))
		untouched()
	})

	It("tidy: confirm --esc (after the keep picker)--> keep", func() {
		again := func(st cli.TidyKeepStep) ([]string, error) {
			untouched() // nothing moved or kept before the question is answered
			return keepAgain(st)
		}
		p := &scripted{tidyPick: tidyPicks("skill/old"), tidyKeep: append(tidyKeeps("command/tmp"), again), confirmBacks: 1, confirm: []bool{true}}
		r := lolaP(p, "tidy", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		q := "confirm:Move 1 item to the trash and keep 1?"
		Expect(p.calls).To(Equal([]string{"tidy-pick", "tidy-keep", q, "tidy-keep", q}), "the keep picker again, not the move picker")
		Expect(p.tidyKeepSteps[1].Preselect).To(Equal([]string{"command/tmp"}), "the keep answer is kept")
		Expect(p.tidyKeepSteps[1].Nav).To(Equal(cli.Nav{Back: true, Answered: 1}), "Esc there still goes to the move picker")
		Expect(p.confirmNavs).To(Equal([]cli.Nav{{Back: true, Answered: 2}, {Back: true, Answered: 2}}))
		Expect(old).NotTo(BeAnExistingFile())
		Expect(os.ReadFile(keepYAML)).To(ContainSubstring("- command/tmp\n"))
	})

	It("tidy: confirm --esc (after the move picker only)--> move", func() {
		again := func(st cli.TidyPickStep) ([]string, error) {
			untouched() // nothing moved before the question is answered
			return tidyAgain(st)
		}
		p := &scripted{tidyPick: append(tidyPicks("claude-code"), again), confirmBacks: 1, confirm: []bool{true}}
		r := lolaP(p, "tidy", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		q := "confirm:Move 2 items to the trash?"
		Expect(p.calls).To(Equal([]string{"tidy-pick", q, "tidy-pick", q}))
		Expect(p.tidyPickSteps[1].Preselect).To(Equal(tidyIDs(p.tidyPickSteps[0].Options, "claude-code")), "the move answer is kept")
		Expect(p.confirmNavs).To(Equal([]cli.Nav{{Back: true, Answered: 1}, {Back: true, Answered: 1}}))
		Expect(old).NotTo(BeAnExistingFile())
		Expect(tmpMd).NotTo(BeAnExistingFile())
		Expect(keepYAML).NotTo(BeAnExistingFile())
	})

	It("tidy: confirm --esc (names)--> confirm", func() {
		// huh's Esc on a first screen is pinned in keys_internal_test.go "on the first screen, Esc says q quits in the key help, not as an error, and stays".
		p := &scripted{confirm: []bool{false}}
		r := lolaP(p, "tidy", "skill/old", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"confirm:Move 1 item to the trash?"}))
		Expect(p.confirmNavs[0].Back).To(BeFalse(), "a first screen: Esc stays")
		Expect(old).To(BeADirectory())
	})
})

var _ = Describe("trash paths", func() {
	var proj, trashDir, ship string
	at := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	BeforeEach(func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj = filepath.Join(tmp, "proj")
		writeTree(proj, map[string]string{".claude/commands/ship.md": "ship\n", ".claude/commands/lint.md": "lint\n"})
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		trashDir = p.Trash
		ship = filepath.Join(proj, ".claude/commands/ship.md")
	})
	restoreQ := cli.WhereStep{Title: "Restore 1 item from the trash?", Options: []string{"Restore"}, Nav: cli.Nav{Back: true, Answered: 1}}

	It("trash: start --run (the trash is empty)--> done", func() {
		p := &scripted{}
		r := lolaP(p, "trash")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(BeEmpty(), "no picker of nothing")
		Expect(r.out).To(Equal("✓ the trash is empty\n"))
	})

	It("trash: start --run (--empty -y)--> done", func() {
		id := trashRunOf(proj, at, "lola tidy", nil, ".claude/commands/ship.md")
		p := &scripted{}
		r := lolaP(p, "trash", "--empty", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(BeEmpty())
		Expect(r.out).To(ContainSubstring("✓ deleted 1 trash run"))
		Expect(filepath.Join(trashDir, id)).NotTo(BeAnExistingFile())
	})

	It("trash: pick --empty submit--> pick", func() {
		id := trashRunOf(proj, at, "lola tidy", nil, ".claude/commands/ship.md")
		p := &scripted{trashPick: []func(cli.TrashPickStep) (cli.TrashAnswer, error){trashTicks(cli.TrashEmpty), trashQuit}}
		r := lolaP(p, "trash")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"trash-pick", "trash-pick"}))
		Expect(p.trashPickSteps[1].Problem).To(Equal("nothing ticked: tick files or runs, then pick what to do with them"))
		Expect(p.trashPickSteps[1].Action).To(Equal(cli.TrashEmpty), "the action is kept")
		Expect(filepath.Join(trashDir, id)).To(BeADirectory())
	})

	It("trash: pick --ctrl+c--> cancelled", func() {
		id := trashRunOf(proj, at, "lola tidy", nil, ".claude/commands/ship.md")
		p := &scripted{trashPick: []func(cli.TrashPickStep) (cli.TrashAnswer, error){trashQuit}}
		r := lolaP(p, "trash")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(ContainSubstring("✗ cancelled — nothing was changed"))
		Expect(ship).NotTo(BeAnExistingFile())
		Expect(filepath.Join(trashDir, id)).To(BeADirectory())
	})

	It("ends the restore on Ctrl+C in its plan's viewer, before the question", func() {
		id := trashRunOf(proj, at, "lola tidy", nil, ".claude/commands/ship.md")
		GinkgoT().Setenv("LOLA_PAGER", "")
		GinkgoT().Setenv("COLUMNS", "50")
		GinkgoT().Setenv("LINES", "3")
		in, typed, err := os.Pipe()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			Expect(typed.Close()).To(Succeed())
			Expect(in.Close()).To(Succeed())
		})
		_, err = typed.WriteString("\x03")
		Expect(err).NotTo(HaveOccurred())
		p := &scripted{trashPick: []func(cli.TrashPickStep) (cli.TrashAnswer, error){trashTicks(cli.TrashRestore, "lola tidy")}}
		root, err := cli.NewRootCmd(os.Getenv, cli.WithPrompter(p), cli.WithTerminals(true, true))
		Expect(err).NotTo(HaveOccurred())
		var out, errb bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&errb)
		root.SetIn(in)
		root.SetArgs([]string{"trash"})
		Expect(cli.Execute(root)).To(Equal(0), errb.String())
		Expect(out.String()).To(ContainSubstring("q  continue to: Restore 1 item from the trash?"))
		Expect(p.calls).To(Equal([]string{"trash-pick"}), "Ctrl+C ended the run: the restore question is not asked")
		Expect(errb.String()).To(HaveSuffix("✗ cancelled — nothing was changed\n"))
		Expect(ship).NotTo(BeAnExistingFile())
		Expect(filepath.Join(trashDir, id)).To(BeADirectory())
	})

	It("trash: restore --Restore--> done", func() {
		id := trashRunOf(proj, at, "lola tidy", nil, ".claude/commands/ship.md")
		p := &scripted{trashPick: []func(cli.TrashPickStep) (cli.TrashAnswer, error){trashTicks(cli.TrashRestore, "lola tidy")}, where: []int{0}}
		r := lolaP(p, "trash")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"trash-pick", "where"}))
		Expect(r.out).To(ContainSubstring("✓ restored 1 item from trash run " + id))
		Expect(read(ship)).To(Equal("ship\n"))
		Expect(filepath.Join(trashDir, id)).NotTo(BeAnExistingFile(), "an emptied run is removed")
	})

	It("trash: restore --Restore (several runs, one fails)--> failed", func() {
		if os.Geteuid() == 0 {
			Skip("root writes in a read-only directory")
		}
		writeTree(proj, map[string]string{"a/one.md": "one\n", "b/two.md": "two\n"})
		older := trashRunOf(proj, at, "lola tidy", nil, "a/one.md")
		newer := trashRunOf(proj, at.Add(time.Hour), "lola tidy", nil, "b/two.md")
		Expect(os.Chmod(filepath.Join(proj, "a"), 0o555)).To(Succeed())
		DeferCleanup(os.Chmod, filepath.Join(proj, "a"), os.FileMode(0o755))
		p := &scripted{trashPick: []func(cli.TrashPickStep) (cli.TrashAnswer, error){trashTicks(cli.TrashRestore, "lola tidy")}, where: []int{0}}
		r := lolaP(p, "trash")
		Expect(r.code).To(Equal(1))
		Expect(p.whereSteps[0].Title).To(Equal("Restore 2 items from the trash?"))
		Expect(r.err).To(ContainSubstring("permission denied"))
		Expect(r.out).To(ContainSubstring("✓ restored 1 item from trash run "+newer), "the newer run, first, stays restored")
		Expect(read(filepath.Join(proj, "b/two.md"))).To(Equal("two\n"))
		Expect(filepath.Join(proj, "a/one.md")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(trashDir, older)).To(BeADirectory())
	})

	It("trash: restore --esc--> pick", func() {
		trashRunOf(proj, at, "lola tidy", nil, ".claude/commands/ship.md")
		again := func(st cli.TrashPickStep) (cli.TrashAnswer, error) {
			Expect(ship).NotTo(BeAnExistingFile(), "nothing restored before the question is answered")
			return trashTicks(cli.TrashRestore, "ship.md")(st)
		}
		p := &scripted{trashPick: []func(cli.TrashPickStep) (cli.TrashAnswer, error){trashTicks(cli.TrashRestore, "ship.md"), again},
			where: []int{whereBack, 0}}
		r := lolaP(p, "trash")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"trash-pick", "where", "trash-pick", "where"}))
		Expect(p.whereSteps[0]).To(Equal(restoreQ))
		Expect(p.trashPickSteps[1].Preselect).To(Equal(tidyIDs(p.trashPickSteps[0].Options, "ship.md")), "the ticks are kept")
		Expect(p.trashPickSteps[1].Problem).To(BeEmpty())
		Expect(read(ship)).To(Equal("ship\n"))
	})

	It("trash: restore --Cancel--> pick", func() {
		id := trashRunOf(proj, at, "lola tidy", nil, ".claude/commands/ship.md")
		p := &scripted{trashPick: []func(cli.TrashPickStep) (cli.TrashAnswer, error){trashTicks(cli.TrashRestore, "ship.md"), trashQuit}, where: []int{-1}}
		r := lolaP(p, "trash")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"trash-pick", "where", "trash-pick"}))
		Expect(p.trashPickSteps[1].Preselect).To(Equal(tidyIDs(p.trashPickSteps[0].Options, "ship.md")), "the ticks are kept")
		Expect(ship).NotTo(BeAnExistingFile())
		Expect(filepath.Join(trashDir, id)).To(BeADirectory())
	})

	It("trash: restore --ctrl+c--> cancelled", func() {
		id := trashRunOf(proj, at, "lola tidy", nil, ".claude/commands/ship.md")
		p := &scripted{trashPick: []func(cli.TrashPickStep) (cli.TrashAnswer, error){trashTicks(cli.TrashRestore, "ship.md")}, where: []int{whereCtrlC}}
		r := lolaP(p, "trash")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"trash-pick", "where"}), "the run ends: no picker again")
		Expect(r.out).To(BeEmpty())
		Expect(ship).NotTo(BeAnExistingFile())
		Expect(filepath.Join(trashDir, id)).To(BeADirectory())
	})

	It("trash: force --esc--> pick", func() {
		trashRunOf(proj, at, "lola tidy", nil, ".claude/commands/ship.md")
		writeTree(proj, map[string]string{".claude/commands/ship.md": "new\n"})
		again := func(st cli.TrashPickStep) (cli.TrashAnswer, error) {
			Expect(read(ship)).To(Equal("new\n"), "nothing moved or restored before the question is answered")
			runs, err := os.ReadDir(trashDir)
			Expect(err).NotTo(HaveOccurred())
			Expect(runs).To(HaveLen(1), "no new trash run yet")
			return trashTicks(cli.TrashRestore, "ship.md")(st)
		}
		p := &scripted{trashPick: []func(cli.TrashPickStep) (cli.TrashAnswer, error){trashTicks(cli.TrashRestore, "ship.md"), again},
			where: []int{0, 0}, confirmBacks: 1, confirm: []bool{true}}
		r := lolaP(p, "trash")
		Expect(r.code).To(Equal(0), r.err)
		q := "confirm:Move 1 item in the way into a new trash run, then restore 1 item?"
		Expect(p.calls).To(Equal([]string{"trash-pick", "where", q, "trash-pick", "where", q}), "back to the run picker, not to Restore 1 item…?")
		Expect(p.confirmNavs[0]).To(Equal(cli.Nav{Back: true, Answered: 2}))
		Expect(p.trashPickSteps[1].Preselect).To(Equal(tidyIDs(p.trashPickSteps[0].Options, "ship.md")))
		Expect(read(ship)).To(Equal("ship\n"))
	})

	It("trash: force --ctrl+c--> cancelled", func() {
		id := trashRunOf(proj, at, "lola tidy", nil, ".claude/commands/ship.md")
		writeTree(proj, map[string]string{".claude/commands/ship.md": "new\n"})
		p := &scripted{trashPick: []func(cli.TrashPickStep) (cli.TrashAnswer, error){trashTicks(cli.TrashRestore, "ship.md")}, where: []int{0}, confirmCancels: 1}
		r := lolaP(p, "trash")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"trash-pick", "where", "confirm:Move 1 item in the way into a new trash run, then restore 1 item?"}))
		Expect(read(ship)).To(Equal("new\n"))
		runs, err := os.ReadDir(trashDir)
		Expect(err).NotTo(HaveOccurred())
		Expect(runs).To(HaveLen(1), "no new trash run")
		Expect(filepath.Join(trashDir, id)).To(BeADirectory())
	})

	It("trash: empty --ctrl+c--> cancelled", func() {
		id := trashRunOf(proj, at, "lola tidy", nil, ".claude/commands/ship.md")
		p := &scripted{trashPick: []func(cli.TrashPickStep) (cli.TrashAnswer, error){trashTicks(cli.TrashEmpty, "lola tidy")}, confirmCancels: 1}
		r := lolaP(p, "trash")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"trash-pick", "confirm:Delete 1 trash run? This cannot be undone."}), "the run ends")
		Expect(filepath.Join(trashDir, id)).To(BeADirectory())
	})

	It("trash: empty --esc (from the picker)--> pick", func() {
		id := trashRunOf(proj, at, "lola tidy", nil, ".claude/commands/ship.md")
		again := func(st cli.TrashPickStep) (cli.TrashAnswer, error) {
			Expect(filepath.Join(trashDir, id)).To(BeADirectory(), "nothing deleted before the question is answered")
			return trashTicks(cli.TrashEmpty, "lola tidy")(st)
		}
		p := &scripted{trashPick: []func(cli.TrashPickStep) (cli.TrashAnswer, error){trashTicks(cli.TrashEmpty), trashTicks(cli.TrashEmpty, "lola tidy"), again},
			confirmBacks: 1, confirm: []bool{true}}
		r := lolaP(p, "trash")
		Expect(r.code).To(Equal(0), r.err)
		q := "confirm:Delete 1 trash run? This cannot be undone."
		Expect(p.calls).To(Equal([]string{"trash-pick", "trash-pick", q, "trash-pick", q}), "an empty submit first, then the question")
		Expect(p.confirmNavs[0]).To(Equal(cli.Nav{Back: true, Answered: 1}))
		Expect(p.trashPickSteps[2].Preselect).To(Equal(tidyIDs(p.trashPickSteps[1].Options, "lola tidy")))
		Expect(p.trashPickSteps[2].Problem).To(BeEmpty(), "the empty submit's problem no longer stands")
		Expect(filepath.Join(trashDir, id)).NotTo(BeAnExistingFile())
	})

	It("trash: pick --Empty ticked runs (only files ticked)--> pick", func() {
		id := trashRunOf(proj, at, "lola tidy", nil, ".claude/commands/ship.md")
		p := &scripted{trashPick: []func(cli.TrashPickStep) (cli.TrashAnswer, error){trashTicks(cli.TrashEmpty, "ship.md"), trashQuit}}
		r := lolaP(p, "trash")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"trash-pick", "trash-pick"}))
		Expect(p.trashPickSteps[1].Problem).To(Equal("Empty ticked runs deletes whole runs: tick a [whole run] row, not only files"))
		Expect(p.trashPickSteps[1].Preselect).To(Equal(tidyIDs(p.trashPickSteps[0].Options, "ship.md")), "the ticks are kept")
		Expect(p.trashPickSteps[1].Action).To(Equal(cli.TrashEmpty), "and so is the action")
		Expect(filepath.Join(trashDir, id)).To(BeADirectory())
	})

	It("trash: empty --esc (--empty)--> empty", func() {
		// huh's Esc on a first screen is pinned in keys_internal_test.go "on the first screen, Esc says q quits in the key help, not as an error, and stays".
		id := trashRunOf(proj, at, "lola tidy", nil, ".claude/commands/ship.md")
		p := &scripted{confirm: []bool{false}}
		r := lolaP(p, "trash", "--empty")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"confirm:Delete 1 trash run? This cannot be undone."}))
		Expect(p.confirmNavs[0].Back).To(BeFalse(), "a first screen: Esc stays")
		Expect(filepath.Join(trashDir, id)).To(BeADirectory())
	})
})

var _ = Describe("cache clean paths", func() {
	var p paths.Paths
	BeforeEach(func() {
		testenv.Isolate()
		var err error
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
	})
	const removeQ = "confirm:Remove these from your cache?"

	It("cache clean: start --run (nothing to remove)--> done", func() {
		src := filepath.Join(filepath.Dir(p.Config), "src", "a")
		writeTree(src, map[string]string{"lola.yml": "name: a\n", "commands/hi.md": "v1\n"})
		Expect(lola("cache", "add", src).code).To(Equal(0))
		index := filepath.Join(p.Store, "a", "index.yml")
		doc := read(index)
		Expect(doc).To(ContainSubstring("last_used:"))
		Expect(os.WriteFile(index, []byte(doc[:strings.Index(doc, "last_used:")]), 0o644)).To(Succeed())
		sp := &scripted{}
		r := lolaP(sp, "cache", "clean")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(BeEmpty(), "nothing to ask about")
		Expect(r.out).To(Equal("✓ nothing to clean\n"))
		Expect(read(index)).To(ContainSubstring("last_used:"), "the clean still runs: it dates a version that has no date")
	})

	It("cache clean: start --run (-y)--> done", func() {
		old := cacheTwice(p)
		sp := &scripted{}
		r := lolaP(sp, "cache", "clean", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(BeEmpty())
		Expect(r.out).To(ContainSubstring("freed"))
		Expect(old).NotTo(BeAnExistingFile())
	})

	It("cache clean: start --run (-n)--> done", func() {
		old := cacheTwice(p)
		sp := &scripted{}
		r := lolaP(sp, "cache", "clean", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(BeEmpty(), "a dry run asks nothing")
		Expect(r.out).To(ContainSubstring("will remove from your cache:"))
		Expect(old).To(BeADirectory())
	})

	It("cache clean: confirm --esc--> confirm", func() {
		// huh's Esc on a first screen is pinned in keys_internal_test.go "on the first screen, Esc says q quits in the key help, not as an error, and stays".
		old := cacheTwice(p)
		sp := &scripted{confirm: []bool{false}}
		r := lolaP(sp, "cache", "clean")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{removeQ}))
		Expect(sp.confirmNavs[0].Back).To(BeFalse(), "a first screen: Esc stays")
		Expect(old).To(BeADirectory())
	})

	It("cache clean: confirm --ctrl+c--> cancelled", func() {
		old := cacheTwice(p)
		sp := &scripted{confirmCancels: 1}
		r := lolaP(sp, "cache", "clean")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{removeQ}))
		Expect(r.out).To(BeEmpty())
		Expect(old).To(BeADirectory())
	})
})
