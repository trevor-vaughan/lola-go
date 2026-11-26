package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/cli"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
	"github.com/trevor-vaughan/lola-go/internal/trash"
)

// trashRunOf moves names (paths relative to proj) into a new trash run made at, recorded as made
// by command, copying copied as credential copies, and returns the run's id.
func trashRunOf(proj string, at time.Time, command string, copied []string, names ...string) string {
	GinkgoHelper()
	p, err := paths.Resolve(os.Getenv)
	Expect(err).NotTo(HaveOccurred())
	t, err := trash.OpenTrash(p.Trash, at, trash.RunRecord{Command: command, Scope: "project", Root: proj})
	Expect(err).NotTo(HaveOccurred())
	for _, c := range copied {
		to, err := t.CopyBytes("abc", proj, filepath.Join(proj, c), []byte(read(filepath.Join(proj, c))))
		Expect(err).NotTo(HaveOccurred())
		t.MarkCredential(to)
	}
	for _, n := range names {
		root, err := os.OpenRoot(filepath.Join(proj, filepath.Dir(n)))
		Expect(err).NotTo(HaveOccurred())
		_, err = t.Move("abc", proj, root, filepath.Base(n))
		Expect(err).NotTo(HaveOccurred())
		Expect(root.Close()).To(Succeed())
	}
	Expect(t.Close()).To(Succeed())
	return filepath.Base(t.Dir)
}

// trashTicks answers the trash picker with action and the options whose labels contain any of parts.
func trashTicks(action string, parts ...string) func(cli.TrashPickStep) (cli.TrashAnswer, error) {
	return func(st cli.TrashPickStep) (cli.TrashAnswer, error) {
		return cli.TrashAnswer{Picked: tidyIDs(st.Options, parts...), Action: action}, nil
	}
}

// trashQuit answers the trash picker as Ctrl+C does.
func trashQuit(cli.TrashPickStep) (cli.TrashAnswer, error) { return cli.TrashAnswer{}, cli.ErrAborted }

var _ = Describe("lola trash", func() {
	var tmp, proj, trashDir string
	at := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj = filepath.Join(tmp, "proj")
		writeTree(proj, map[string]string{".mcp.json": "{}\n", ".claude/commands/ship.md": "ship\n", ".claude/commands/lint.md": "lint\n"})
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		trashDir = p.Trash
	})
	doc := func(out string) map[string]any {
		GinkgoHelper()
		var d map[string]any
		Expect(json.Unmarshal([]byte(out), &d)).To(Succeed(), out)
		checkJSON(d, "")
		return d
	}

	It("lists an empty trash as runs: [] under --json, and says so", func() {
		r := lola("trash", "--json")
		Expect(r.code).To(Equal(0), r.err)
		Expect(doc(r.out)).To(Equal(map[string]any{"runs": []any{}, "notices": []any{}}))
		r = lola("trash")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(Equal("✓ the trash is empty\n"))
	})

	It("lists runs as JSON runs[] of id, time, command, scope, root, files and size_bytes, and --plain rows", func() {
		id := trashRunOf(proj, at, "lola tidy", nil, ".claude/commands/ship.md")
		r := lola("trash", "--json")
		Expect(r.code).To(Equal(0), r.err)
		Expect(doc(r.out)["runs"]).To(Equal([]any{map[string]any{"id": id, "time": "2026-11-03T12:00:00Z", "command": "lola tidy",
			"scope": "project", "root": proj, "files": 1.0, "size_bytes": 5.0}}))
		r = lola("trash", "--plain")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(Equal(strings.Join([]string{"run", id, "2026-11-03T12:00:00Z", "lola tidy", "project", proj, "1", "5"}, "\t") + "\n"))
	})

	It("shows a run as JSON entries[] of original, trash and credentials, and marks credential copies", func() {
		id := trashRunOf(proj, at, "lola tidy", []string{".mcp.json"}, ".claude/commands/ship.md")
		r := lola("trash", id, "--json")
		Expect(r.code).To(Equal(0), r.err)
		d := doc(r.out)
		Expect(d["id"]).To(Equal(id))
		run := filepath.Join(trashDir, id, "abc")
		Expect(d["entries"]).To(Equal([]any{
			map[string]any{"original": filepath.Join(proj, ".mcp.json"), "trash": filepath.Join(run, ".mcp.json"), "credentials": true},
			map[string]any{"original": filepath.Join(proj, ".claude/commands/ship.md"), "trash": filepath.Join(run, ".claude/commands/ship.md"), "credentials": false},
		}))
		r = lola("trash", id)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring(filepath.Join(proj, ".mcp.json") + " (may hold credentials)"))
		Expect(r.out).To(ContainSubstring("→ restore them (replaces "+filepath.Join(proj, ".mcp.json")+
			", which moves to a new trash run first): lola trash restore "+id+" --force\n"), "the copy's file is there: a plain restore is refused")
		r = lola("trash", id, "--plain")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("entry\t" + filepath.Join(proj, ".mcp.json") + "\t" + filepath.Join(run, ".mcp.json") + "\ttrue\n"))
	})

	It("I-3: a run whose original is there hints the --force restore that works, saying what it replaces", func() {
		id := trashRunOf(proj, at, "lola tidy", []string{".mcp.json"})
		writeTree(proj, map[string]string{".mcp.json": "{\"later\":true}\n"})
		r := lola("trash", id)
		Expect(r.code).To(Equal(0), r.err)
		_, hint, ok := strings.Cut(r.out, "→ restore them (replaces "+filepath.Join(proj, ".mcp.json")+", which moves to a new trash run first): ")
		Expect(ok).To(BeTrue(), r.out)
		hint = strings.TrimSuffix(hint, "\n")
		Expect(hint).To(Equal("lola trash restore " + id + " --force"))
		r = runHint(hint)
		Expect(r.code).To(Equal(0), r.err)
		Expect(read(filepath.Join(proj, ".mcp.json"))).To(Equal("{}\n"))

		id = trashRunOf(proj, at.Add(time.Hour), "lola tidy", nil, ".claude/commands/lint.md")
		r = lola("trash", id)
		Expect(r.out).To(HaveSuffix("→ restore them: lola trash restore "+id+"\n"), "nothing in the way: the plain restore works")
	})

	It("prints what a run says only escaped", func() {
		trashRunOf(proj, at, "lola tidy \x1b[2J\a", nil, ".claude/commands/ship.md")
		for _, format := range [][]string{nil, {"--plain"}, {"--json"}} {
			r := lola(append([]string{"trash"}, format...)...)
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).NotTo(ContainSubstring("\x1b"), format)
			Expect(r.out).To(ContainSubstring("lola tidy ?[2J?"), format)
		}
	})

	It("shows a run with trash show and with the trash <run> shorthand, identically", func() {
		id := trashRunOf(proj, at, "lola tidy", nil, ".mcp.json")
		long, short := lola("trash", "show", id, "--plain"), lola("trash", id, "--plain")
		Expect(long.code).To(Equal(0), long.err)
		Expect(short).To(Equal(long))
	})

	It("lists with trash list as bare trash does off a terminal", func() {
		trashRunOf(proj, at, "lola tidy", nil, ".mcp.json")
		Expect(lola("trash", "list", "--json")).To(Equal(lola("trash", "--json")))
		Expect(lola("trash", "ls", "--json")).To(Equal(lola("trash", "--json")))
	})

	It("opens no picker for trash list, even on a terminal", func() {
		trashRunOf(proj, at, "lola tidy", nil, ".mcp.json")
		p := &scripted{}
		r := lolaP(p, "trash", "list")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(BeEmpty())
	})

	It("coaches an old action flag to its subcommand, keeping the other flags", func() {
		id := trashRunOf(proj, at, "lola tidy", nil, ".mcp.json")
		r := lola("trash", id, "--restore", "-n")
		Expect(r.code).NotTo(Equal(0))
		Expect(r.err).To(ContainSubstring("--restore is not a flag of lola trash"))
		Expect(r.err).To(ContainSubstring("hint: lola trash restore " + id + " -n\n"))
		expectRunnable("lola trash restore " + id + " -n")
		Expect(filepath.Join(trashDir, id)).To(BeADirectory(), "the refused run changed nothing")
	})

	It("coaches an old action flag typed after a flag of a verb, keeping that flag", func() {
		id := trashRunOf(proj, at, "lola tidy", nil, ".mcp.json")
		r := lola("trash", id, "-n", "--restore")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("error: --restore is not a flag of lola trash\n  hint: lola trash restore " + id + " -n\n"))
		expectRunnable(lastHint(r.err))
		Expect(filepath.Join(trashDir, id)).To(BeADirectory(), "the refused run changed nothing")
	})

	It("keeps each flag of a shorthand cluster the verb takes, so -ny stays a dry run", func() {
		id := trashRunOf(proj, at, "lola tidy", nil, ".mcp.json")
		r := lola("trash", id, "--restore", "-ny")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("error: --restore is not a flag of lola trash\n  hint: lola trash restore "+id+" -n\n"),
			"restore takes -n and not -y")
		expectRunnable(lastHint(r.err))
		r = lola("trash", id, "--empty", "-yn")
		Expect(lastHint(r.err)).To(Equal("lola trash empty " + id + " -y -n"))
		expectRunnable(lastHint(r.err))
	})

	DescribeTable("coaches each old action flag and alias to its verb",
		func(flag, verb string) {
			id := trashRunOf(proj, at, "lola tidy", nil, ".mcp.json")
			r := lola("trash", id, flag)
			Expect(r.code).NotTo(Equal(0))
			Expect(r.err).To(ContainSubstring(flag + " is not a flag of lola trash"))
			Expect(r.err).To(ContainSubstring("hint: lola trash " + verb + " " + id + "\n"))
			expectRunnable("lola trash " + verb + " " + id)
		},
		Entry("--restore", "--restore", "restore"), Entry("--undo", "--undo", "restore"), Entry("--recover", "--recover", "restore"),
		Entry("--empty", "--empty", "empty"), Entry("--purge", "--purge", "empty"), Entry("--clear", "--clear", "empty"))

	It("coaches a sibling verb's flag with that verb's usage and the flag, never the run's operands", func() {
		id := trashRunOf(proj, at, "lola tidy", nil, ".mcp.json")
		r := lola("trash", "restore", id, "--older-than", "30d")
		Expect(r.code).NotTo(Equal(0))
		Expect(r.err).To(ContainSubstring("--older-than applies to lola trash empty"))
		Expect(r.err).To(ContainSubstring("hint: usage: lola trash empty [<run>...] --older-than 30d\n"))
		Expect(r.err).NotTo(ContainSubstring(id + " --older-than"))
		r = lola("trash", "empty", "--force")
		Expect(r.err).To(ContainSubstring("--force applies to lola trash restore"))
		Expect(r.err).To(ContainSubstring("hint: usage: lola trash restore <run> [<original>...] --force\n"))
	})

	It("reads a sibling flag's value given with = too", func() {
		id := trashRunOf(proj, at, "lola tidy", nil, ".mcp.json")
		r := lola("trash", "restore", id, "--older-than=30d")
		Expect(r.err).To(ContainSubstring("hint: usage: lola trash empty [<run>...] --older-than 30d\n"))
	})

	It("coaches an old flag with the words typed after it too, and a second old flag with its own hint", func() {
		id := trashRunOf(proj, at, "lola tidy", nil, ".mcp.json")
		r := lola("trash", "--restore", id, "-n")
		Expect(r.err).To(ContainSubstring("hint: lola trash restore " + id + " -n\n"))
		expectRunnable("lola trash restore " + id + " -n")
		r = lola("trash", id, "--restore", "--empty")
		Expect(r.err).To(ContainSubstring("hint: lola trash restore " + id + "\n"))
		Expect(r.err).To(ContainSubstring("hint: lola trash empty " + id + "\n"))
		r = lola("trash", "--restore")
		Expect(r.err).To(ContainSubstring("hint: usage: lola trash restore <run> [<original>...]\n"))
		Expect(r.err).To(ContainSubstring("hint: find one: lola trash list\n"))
	})

	It("keeps the run's output flags in the find one hint", func() {
		r := lola("trash", "restore", "--json")
		Expect(r.code).To(Equal(1))
		d := doc(r.out + r.err)
		Expect(d["hints"]).To(ContainElement("find one: lola trash list --json"))
		expectRunnable("find one: lola trash list --json")
		r = lola("trash", "--restore", "--plain")
		Expect(r.err).To(ContainSubstring("find one: lola trash list --plain"))
	})

	It("passes an unknown flag no verb takes to the root's refusal", func() {
		r := lola("trash", "list", "--nope")
		Expect(r.code).NotTo(Equal(0))
		Expect(r.err).To(ContainSubstring("unknown flag: --nope"))
		Expect(r.err).To(ContainSubstring("hint: see: lola trash list --help"))
	})

	It("suggests the verb a typo is near, keeping the operands", func() {
		id := trashRunOf(proj, at, "lola tidy", nil, ".mcp.json")
		r := lola("trash", "restor", id)
		Expect(r.code).NotTo(Equal(0))
		Expect(r.err).To(ContainSubstring(`unknown trash run or command "restor"`))
		Expect(r.err).To(ContainSubstring("hint: did you mean: lola trash restore " + id + "\n"))
		expectRunnable("lola trash restore " + id)
		Expect(filepath.Join(trashDir, id)).To(BeADirectory())
	})

	It("shows a word that names no verb as a run, refusing it as an unknown run", func() {
		r := lola("trash", "nonesuch")
		Expect(r.code).NotTo(Equal(0))
		Expect(r.err).To(ContainSubstring("there is no trash run nonesuch"))
		Expect(r.err).To(ContainSubstring("hint: see the runs: lola trash list"))
	})

	It("empties every run with trash empty after -y, and off a terminal without -y refuses with the plan", func() {
		trashRunOf(proj, at, "lola tidy", nil, ".mcp.json")
		r := lola("trash", "empty", "--json")
		Expect(r.code).NotTo(Equal(0))
		Expect(r.out + r.err).To(ContainSubstring(`"removed"`))
		Expect(r.err + r.out).To(ContainSubstring("lola trash empty --json -y"))
		Expect(lola("trash", "list", "--json").out).NotTo(ContainSubstring(`"runs": []`), "the refused run deleted nothing")
		Expect(lola("trash", "empty", "-y").code).To(Equal(0))
		Expect(lola("trash", "--json").out).To(ContainSubstring(`"runs": []`))
	})

	It("prints, in a coach refusal under --json, error and hints, though --json came after the unknown flag", func() {
		id := trashRunOf(proj, at, "lola tidy", nil, ".mcp.json")
		r := lola("trash", id, "--restore", "--json")
		d := doc(r.out + r.err)
		Expect(d["error"]).To(ContainSubstring("--restore is not a flag of lola trash"))
		Expect(d["hints"]).To(ContainElement("lola trash restore " + id + " --json"))
	})

	It("restores a run's entries, a relative original resolved against the working directory", func() {
		id := trashRunOf(proj, at, "lola tidy", nil, ".claude/commands/ship.md", ".claude/commands/lint.md")
		GinkgoT().Chdir(proj)
		r := lola("trash", "restore", id, ".claude/commands/ship.md", "--json")
		Expect(r.code).To(Equal(0), r.err)
		d := doc(r.out)
		Expect(d["restored"]).To(HaveLen(1))
		Expect(d["run_removed"]).To(BeFalse())
		Expect(read(filepath.Join(proj, ".claude/commands/ship.md"))).To(Equal("ship\n"))
		r = lola("trash", "restore", id)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("✓ restored 1 item from trash run " + id))
		Expect(r.out).To(ContainSubstring("trash run " + id + " is empty now, and was removed"))
	})

	It("asks the picker again when two ticked runs would restore the same original, naming it and both runs", func() {
		a := trashRunOf(proj, at, "lola tidy", nil, ".claude/commands/ship.md")
		writeTree(proj, map[string]string{".claude/commands/ship.md": "again\n"})
		b := trashRunOf(proj, at.Add(time.Hour), "lola tidy", nil, ".claude/commands/ship.md")
		p := &scripted{trashPick: []func(cli.TrashPickStep) (cli.TrashAnswer, error){trashTicks(cli.TrashRestore, "lola tidy"), trashQuit}}
		r := lolaP(p, "trash")
		Expect(r.code).To(Equal(4), r.err)
		Expect(p.calls).To(Equal([]string{"trash-pick", "trash-pick"}), "no review of a restore that would fail part way")
		Expect(p.trashPickSteps[1].Problem).To(Equal("trash runs " + b + " and " + a + " both hold " + filepath.Join(proj, ".claude/commands/ship.md") +
			", which one restore cannot put back twice; untick one"))
		Expect(p.trashPickSteps[1].Preselect).To(Equal(tidyIDs(p.trashPickSteps[0].Options, "lola tidy")), "the ticks are kept")
	})

	It("says there is nothing to restore when what was ticked is gone from the trash", func() {
		id := trashRunOf(proj, at, "lola tidy", nil, ".claude/commands/ship.md")
		Expect(os.Remove(filepath.Join(trashDir, id, "abc", ".claude/commands/ship.md"))).To(Succeed())
		p := &scripted{trashPick: []func(cli.TrashPickStep) (cli.TrashAnswer, error){trashTicks(cli.TrashRestore, "ship.md")}}
		r := lolaP(p, "trash")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"trash-pick"}))
		Expect(r.err).To(ContainSubstring("is gone from the trash"))
		Expect(r.out).To(Equal("→ nothing to restore, so nothing changed\n"))
	})

	It("refuses a restore over what is there, hinting --force, and -n prints the plan", func() {
		id := trashRunOf(proj, at, "lola tidy", nil, ".claude/commands/ship.md")
		writeTree(proj, map[string]string{".claude/commands/ship.md": "new\n"})
		r := lola("trash", "restore", id, "-n", "--plain")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("restore\t" + filepath.Join(proj, ".claude/commands/ship.md")))
		Expect(r.out).To(ContainSubstring("exists\t" + filepath.Join(proj, ".claude/commands/ship.md")))
		r = lola("trash", "restore", id)
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("hint: move what is there to a new trash run, then restore: lola trash restore " + id + " --force"))
		Expect(read(filepath.Join(proj, ".claude/commands/ship.md"))).To(Equal("new\n"))
		r = lola("trash", "restore", id, "--force")
		Expect(r.code).To(Equal(0), r.err)
		Expect(read(filepath.Join(proj, ".claude/commands/ship.md"))).To(Equal("ship\n"))
		Expect(r.out).To(ContainSubstring("moved what was in their place to trash run "))
	})

	DescribeTable("takes undo and recover for restore, purge and clear for empty, ls for list",
		func(verb string, restores bool) {
			id := trashRunOf(proj, at, "lola tidy", nil, ".claude/commands/ship.md")
			args := []string{"trash", verb, id}
			if !restores {
				args = append(args, "-y")
			}
			r := lola(args...)
			Expect(r.code).To(Equal(0), r.err)
			if restores {
				Expect(filepath.Join(proj, ".claude/commands/ship.md")).To(BeARegularFile())
			}
			Expect(filepath.Join(trashDir, id)).NotTo(BeADirectory())
		},
		Entry("undo", "undo", true), Entry("recover", "recover", true),
		Entry("purge", "purge", false), Entry("clear", "clear", false))

	DescribeTable("refuses what the verb cannot do, hinting the run that works",
		func(args []string, refusal, hint string) {
			r := lola(args...)
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(ContainSubstring("error: " + refusal + "\n"))
			Expect(r.err).To(ContainSubstring("hint: " + hint + "\n"))
			expectRunnable(hint)
		},
		Entry("two runs to show", []string{"trash", "a", "b"}, "lola trash shows one run at a time", "show one: lola trash show a"),
		Entry("two runs to show, saying how to restore them", []string{"trash", "show", "a", "b"}, "lola trash shows one run at a time",
			"restore those from a: lola trash restore a b"),
		Entry("restore with no run", []string{"trash", "restore"}, "lola trash restore needs a trash run", "find one: lola trash list"),
		Entry("show with no run", []string{"trash", "show"}, "lola trash show needs a trash run", "find one: lola trash list"),
	)

	It("names show's own usage, not restore's, when show has no run", func() {
		r := lola("trash", "show")
		Expect(r.err).To(ContainSubstring("hint: usage: lola trash show <run>\n"))
		Expect(r.err).NotTo(ContainSubstring("restore"))
	})

	DescribeTable("hints a bad --older-than corrected, never without it, and never with -y",
		func(args []string, hint string) {
			r := lola(args...)
			Expect(r.code).To(Equal(1))
			var d map[string]any
			Expect(json.Unmarshal([]byte(r.err), &d)).To(Succeed(), r.err)
			Expect(d["hints"]).To(Equal([]any{hint}))
			for _, h := range d["hints"].([]any) {
				Expect(h).To(ContainSubstring("--older-than "), "a hint never widens what is deleted")
				Expect(shellWords(h.(string))).NotTo(ContainElements("-y", "--yes"), "a corrected value is a new question")
			}
		},
		Entry("a word, with -y", []string{"trash", "empty", "--older-than", "xyz", "-y", "--json"},
			"use an age: lola trash empty --json --older-than 30d"),
		Entry("never, with runs", []string{"trash", "purge", "a", "b", "--older-than", "never", "--yes", "--json"},
			"use an age: lola trash empty a b --json --older-than 30d"),
		Entry("empty, with -n", []string{"trash", "empty", "--older-than", "", "-n", "--json"},
			"use an age: lola trash empty --dry-run --json --older-than 30d"),
	)

	It("empties runs older than an age, -n listing them and deleting nothing", func() {
		old := trashRunOf(proj, at.AddDate(0, 0, -40), "lola tidy", nil, ".claude/commands/ship.md")
		recent := trashRunOf(proj, time.Now(), "lola tidy", nil, ".claude/commands/lint.md")
		r := lola("trash", "empty", "--older-than", "30d", "-n", "--json")
		Expect(r.code).To(Equal(0), r.err)
		d := doc(r.out)
		Expect(d["dry_run"]).To(BeTrue())
		Expect(d["removed"]).To(ConsistOf(HaveKeyWithValue("id", old)))
		Expect(filepath.Join(trashDir, old)).To(BeADirectory())
		r = lola("trash", "empty", "--older-than", "30d", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("✓ deleted 1 trash run, freeing 5 B"))
		Expect(filepath.Join(trashDir, old)).NotTo(BeADirectory())
		Expect(filepath.Join(trashDir, recent)).To(BeADirectory())
	})

	It("on a terminal, bare lola trash opens a picker: runs newest first as [whole run] rows, each followed by its files, credential copies marked first", func() {
		trashRunOf(proj, at, "lola tidy", []string{".mcp.json"}, ".claude/commands/ship.md")
		trashRunOf(proj, at.Add(time.Hour), "lola migrate", nil, ".claude/commands/lint.md")
		p := &scripted{trashPick: []func(cli.TrashPickStep) (cli.TrashAnswer, error){trashQuit}}
		r := lolaP(p, "trash")
		Expect(r.code).To(Equal(4), r.err)
		Expect(r.err).To(ContainSubstring("✗ cancelled — nothing was changed"))
		Expect(r.out).To(BeEmpty())
		Expect(p.calls).To(Equal([]string{"trash-pick"}))
		Expect(tidyLabels(p.trashPickSteps[0].Options)).To(Equal([]string{
			"[whole run] 2026-11-03 13:00:00 UTC  lola migrate  project " + proj + "  1 file, 5 B",
			"    " + filepath.FromSlash(".claude/commands/lint.md"),
			"[whole run] 2026-11-03 12:00:00 UTC  lola tidy  project " + proj + "  2 files, 8 B",
			"    (may hold credentials) .mcp.json",
			"    " + filepath.FromSlash(".claude/commands/ship.md"),
		}), "a file shows relative to its run's project")
		Expect(p.trashPickSteps[0].Preselect).To(BeEmpty(), "nothing is ticked at first")
		Expect(p.trashPickSteps[0].Problem).To(BeEmpty())

		By("a run with no project shows its files' paths whole, and a run's notice prints once")
		pth, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		t, err := trash.OpenTrash(pth.Trash, at.Add(2*time.Hour), trash.RunRecord{Command: "lola migrate", Scope: "user"})
		Expect(err).NotTo(HaveOccurred())
		_, err = t.CopyBytes("abc", proj, filepath.Join(proj, ".mcp.json"), []byte("{}\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(t.Close()).To(Succeed())
		Expect(os.WriteFile(filepath.Join(t.Dir, "run.yaml"), []byte("command: [\n"), 0o600)).To(Succeed())
		p = &scripted{trashPick: []func(cli.TrashPickStep) (cli.TrashAnswer, error){trashQuit}}
		r = lolaP(p, "trash")
		Expect(r.code).To(Equal(4), r.err)
		Expect(p.trashPickSteps[0].Options[1].Label).To(Equal("    " + filepath.Join(proj, ".mcp.json")))
		Expect(strings.Count(r.err, "run.yaml")).To(Equal(1), r.err)
	})

	It("restores what the picker ticks with the plan lola trash <run> [<original>...] --restore makes, shown before it asks", func() {
		id := trashRunOf(proj, at, "lola tidy", nil, ".claude/commands/ship.md", ".claude/commands/lint.md")
		ship, lint := filepath.Join(proj, ".claude/commands/ship.md"), filepath.Join(proj, ".claude/commands/lint.md")
		flag := lola("trash", "restore", id, ship, "-n")
		Expect(flag.code).To(Equal(0), flag.err)

		By("Cancel at the review goes back to the picker, the ticks kept, and changes nothing")
		p := &scripted{trashPick: []func(cli.TrashPickStep) (cli.TrashAnswer, error){trashTicks(cli.TrashRestore, "ship.md"), trashQuit}, where: []int{-1}}
		r := lolaP(p, "trash")
		Expect(r.code).To(Equal(4), r.err)
		Expect(r.err).To(ContainSubstring(flag.out), "the plan the flag form prints")
		Expect(p.whereSteps[0]).To(Equal(cli.WhereStep{Title: "Restore 1 item from the trash?", Options: []string{"Restore"}, Nav: cli.Nav{Back: true, Answered: 1}}))
		Expect(p.calls).To(Equal([]string{"trash-pick", "where", "trash-pick"}))
		Expect(p.trashPickSteps[1].Preselect).To(Equal(tidyIDs(p.trashPickSteps[0].Options, "ship.md")))
		Expect(r.err).To(ContainSubstring("✗ cancelled — nothing was changed"))
		Expect(ship).NotTo(BeAnExistingFile())

		p = &scripted{trashPick: []func(cli.TrashPickStep) (cli.TrashAnswer, error){trashTicks(cli.TrashRestore, "ship.md")}, where: []int{0}}
		r = lolaP(p, "trash")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"trash-pick", "where"}))
		Expect(read(ship)).To(Equal("ship\n"))
		Expect(lint).NotTo(BeAnExistingFile())
		Expect(r.out).To(ContainSubstring("✓ restored 1 item from trash run " + id))

		By("ticking a run's own row restores all it holds, as the flag form naming no original does")
		flag = lola("trash", "restore", id, "-n")
		Expect(flag.code).To(Equal(0), flag.err)
		p = &scripted{trashPick: []func(cli.TrashPickStep) (cli.TrashAnswer, error){trashTicks(cli.TrashRestore, "lola tidy", "lint.md")}, where: []int{0}}
		r = lolaP(p, "trash")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(ContainSubstring(flag.out))
		Expect(read(lint)).To(Equal("lint\n"))
		Expect(r.out).To(ContainSubstring("trash run " + id + " is empty now, and was removed"))
	})

	It("offers Restore with --force as a choice of its own when an original exists, and asks before moving what is there", func() {
		id := trashRunOf(proj, at, "lola tidy", nil, ".claude/commands/ship.md")
		ship := filepath.Join(proj, ".claude/commands/ship.md")
		writeTree(proj, map[string]string{".claude/commands/ship.md": "new\n"})
		force := []string{"Restore with --force: first move what is there into a new trash run"}

		p := &scripted{trashPick: []func(cli.TrashPickStep) (cli.TrashAnswer, error){trashTicks(cli.TrashRestore, "ship.md"), trashQuit}, where: []int{-1}}
		r := lolaP(p, "trash")
		Expect(r.code).To(Equal(4), r.err)
		Expect(r.err).To(ContainSubstring(ship+" exists, so the restore is refused"), "the review names the original in the way")
		Expect(p.whereSteps[0].Options).To(Equal(force), "never a plain Restore, and --force is never implied")
		Expect(read(ship)).To(Equal("new\n"))

		p = &scripted{trashPick: []func(cli.TrashPickStep) (cli.TrashAnswer, error){trashTicks(cli.TrashRestore, "ship.md")}, where: []int{0}, confirm: []bool{false}}
		r = lolaP(p, "trash")
		Expect(r.code).To(Equal(4), r.err)
		Expect(p.calls).To(Equal([]string{"trash-pick", "where", "confirm:Move 1 item in the way into a new trash run, then restore 1 item?"}),
			"counted from the plan with --force")
		Expect(p.confirmNavs).To(Equal([]cli.Nav{{Back: true, Answered: 2}}))
		Expect(read(ship)).To(Equal("new\n"))

		p = &scripted{trashPick: []func(cli.TrashPickStep) (cli.TrashAnswer, error){trashTicks(cli.TrashRestore, "ship.md")}, where: []int{0}, confirm: []bool{true}}
		r = lolaP(p, "trash")
		Expect(r.code).To(Equal(0), r.err)
		Expect(read(ship)).To(Equal("ship\n"))
		Expect(r.out).To(ContainSubstring("✓ restored 1 item from trash run " + id))
		Expect(r.out).To(ContainSubstring("moved what was in their place to trash run "))
	})

	It("empties the runs ticked whole after asking, and asks the picker again when nothing or only a run's files are ticked", func() {
		old := trashRunOf(proj, at, "lola tidy", nil, ".claude/commands/ship.md")
		recent := trashRunOf(proj, at.Add(time.Hour), "lola migrate", nil, ".claude/commands/lint.md")
		p := &scripted{trashPick: []func(cli.TrashPickStep) (cli.TrashAnswer, error){trashTicks(cli.TrashEmpty), trashTicks(cli.TrashEmpty, "lint.md"),
			trashTicks(cli.TrashEmpty, "lola tidy")}, confirm: []bool{false}}
		r := lolaP(p, "trash")
		Expect(r.code).To(Equal(4), r.err)
		Expect(p.calls).To(Equal([]string{"trash-pick", "trash-pick", "trash-pick", "confirm:Delete 1 trash run? This cannot be undone."}))
		Expect(p.trashPickSteps[1].Problem).To(Equal("nothing ticked: tick files or runs, then pick what to do with them"))
		Expect(p.trashPickSteps[2].Problem).To(Equal("Empty ticked runs deletes whole runs: tick a [whole run] row, not only files"))
		Expect(p.trashPickSteps[2].Preselect).To(Equal(tidyIDs(p.trashPickSteps[1].Options, "lint.md")), "the last answer stays ticked")
		Expect(p.confirmNavs).To(Equal([]cli.Nav{{Back: true, Answered: 1}}))
		Expect(r.err).To(ContainSubstring("will delete from the trash:"), "the plan, before the question")
		Expect(filepath.Join(trashDir, old)).To(BeADirectory())

		p = &scripted{trashPick: []func(cli.TrashPickStep) (cli.TrashAnswer, error){trashTicks(cli.TrashEmpty, "lola tidy")}, confirm: []bool{true}}
		r = lolaP(p, "trash")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("✓ deleted 1 trash run, freeing 5 B"))
		Expect(filepath.Join(trashDir, old)).NotTo(BeADirectory())
		Expect(filepath.Join(trashDir, recent)).To(BeADirectory())
	})

	It("opens no picker off a terminal, or under --json or --plain, and lists the runs", func() {
		id := trashRunOf(proj, at, "lola tidy", nil, ".claude/commands/ship.md")
		for _, format := range []string{"--json", "--plain"} {
			p := &scripted{}
			r := lolaP(p, "trash", format)
			Expect(r.code).To(Equal(0), r.err)
			Expect(p.calls).To(BeEmpty(), format)
			Expect(r.out).To(ContainSubstring(id), format)
		}
		r := lola("trash")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring(id))
	})
})
