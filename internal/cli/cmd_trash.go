package cli

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
)

// trashActions are lola trash's action flags, in the order a refusal names them.
var trashActions = []string{"restore", "empty"}

// trashFlags are the flags each lola trash action takes; one the run's action does not take, or
// any of them without an action, is refused, since a flag that does nothing must not look
// accepted.
var trashFlags = map[string][]string{"restore": {"force", "dry-run"}, "empty": {"yes", "older-than", "dry-run"}}

func (a *app) trashCmd() *cobra.Command {
	var restore, empty, force, dry, yes bool
	var olderThan string
	cmd := &cobra.Command{
		Use:   "trash [<run> [<original>...] --restore | [<run>...] --empty [--older-than <age>]]",
		Short: "List, restore and empty lola's trash",
		Long: "lola tidy, lola migrate and lola profile --remove move what they take out of place into the\n" +
			"trash under $XDG_STATE_HOME/lola/trash, one run per command, never deleting it. Without a run,\n" +
			"list the runs, newest first: each one's id, time, the command that made it, its scope and\n" +
			"project, and its files and size. With a run, show its entries: each original path and where\n" +
			"the trash holds it; a copy that may hold credentials (an MCP or settings file) is marked.\n" +
			"On a terminal, without a run, --json or --plain, lola opens a picker instead: tick files, or a\n" +
			"run's [whole run] row, which takes the whole run whatever of its files is ticked, then restore\n" +
			"or empty them; lola shows the plan and asks before it writes.\n\n" +
			"--restore moves the run's entries back, or only the originals you name (a relative path is\n" +
			"taken from the working directory), making the directories they were in. It restores nothing\n" +
			"when an original exists; --force first moves what is there into a new trash run, never\n" +
			"deleting it. A credential copy comes back at mode 0600. Restored entries leave the run, and a\n" +
			"run left empty is removed. -n prints the plan.\n\n" +
			"--empty deletes the named runs, or every run; --older-than 30d (or 2w, weeks of 7 days) keeps\n" +
			"the newer ones. Deleting cannot be undone: lola asks first on a terminal, and off one it\n" +
			"needs -y. -n lists what would be deleted.",
		Example: "  lola trash\n  lola trash 20261103T120000Z\n  lola trash 20261103T120000Z --restore\n" +
			"  lola trash 20261103T120000Z .claude/commands/ship.md --restore -n\n  lola trash --empty --older-than 30d -y",
		GroupID:           groupAdditional,
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: a.completeTrash,
		RunE: func(cmd *cobra.Command, args []string) error {
			act, err := trashAction(cmd, args)
			if err != nil {
				return err
			}
			eng, err := a.engine()
			if err != nil {
				return err
			}
			switch act {
			case "restore":
				return a.restoreTrash(cmd, eng, args, force, dry)
			case "empty":
				return a.emptyTrash(cmd, eng, args, olderThan, dry, yes, Nav{})
			}
			if len(args) > 1 {
				return errs.WithHints(errors.New("lola trash shows one run at a time"), "show one: "+runWithout(cmd, args[:1]),
					"restore those from "+args[0]+": "+runWithout(cmd, args)+" --restore")
			}
			if len(args) == 1 {
				show, err := eng.ShowTrash(args[0], outputFlags(cmd))
				if err != nil {
					return err
				}
				again := runWithout(cmd, args) + " --restore"
				return a.emit(cmd, func(w io.Writer, s style) { printTrashShow(w, s, show, again) }, show)
			}
			list, err := eng.ListTrash()
			if err != nil {
				return err
			}
			if len(list.Runs) > 0 && !a.json && !a.plain {
				if p := a.prompter(cmd); p.Interactive() {
					return a.pickTrash(cmd, eng, p, list)
				}
			}
			return a.emit(cmd, func(w io.Writer, s style) { printTrashList(w, s, list) }, list)
		},
	}
	cmd.Flags().BoolVar(&restore, "restore", false, "move the run's entries, or the originals named, back where they were")
	cmd.Flags().BoolVar(&empty, "empty", false, "delete the named runs, or every run")
	cmd.Flags().BoolVar(&force, "force", false, "with --restore, first move what is where an original was into a new trash run")
	cmd.Flags().StringVar(&olderThan, "older-than", "", "with --empty, only runs older than this: <n>d or <n>w")
	_ = cmd.RegisterFlagCompletionFunc("older-than", cobra.FixedCompletions([]cobra.Completion{cobra.CompletionWithDesc("7d", "7 days"),
		cobra.CompletionWithDesc("30d", "30 days"), cobra.CompletionWithDesc("2w", "2 weeks")}, cobra.ShellCompDirectiveNoFileComp|cobra.ShellCompDirectiveKeepOrder))
	cmd.Flags().BoolVarP(&dry, "dry-run", "n", false, "print the plan and change nothing")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	// Aliases for muscle memory; additive and stable. The published table is pinned in store_test.go.
	aliasFlags(cmd, map[string][]string{"restore": {"undo", "recover"}, "empty": {"purge", "clear"}})
	return cmd
}

// trashAction is the run's action ("" to list or show), refusing two actions and a flag the action
// does not take, each with the run that drops it.
func trashAction(cmd *cobra.Command, args []string) (string, error) {
	var set []string
	for _, act := range trashActions {
		if cmd.Flags().Changed(act) {
			set = append(set, act)
		}
	}
	if len(set) > 1 {
		var hints []string
		for _, keep := range set {
			drop := slices.DeleteFunc(slices.Clone(set), func(s string) bool { return s == keep })
			for _, other := range drop {
				for _, f := range trashFlags[other] {
					if !slices.Contains(trashFlags[keep], f) {
						drop = append(drop, f)
					}
				}
			}
			hints = append(hints, runWithout(cmd, args, drop...))
		}
		return "", errs.WithHints(errors.New("--restore and --empty cannot be combined; lola trash takes one action per run"), hints...)
	}
	act := ""
	if len(set) == 1 {
		act = set[0]
	}
	for _, f := range []string{"force", "yes", "older-than", "dry-run"} {
		if !cmd.Flags().Changed(f) || slices.Contains(trashFlags[act], f) {
			continue
		}
		var takes []string
		for _, a := range trashActions {
			if slices.Contains(trashFlags[a], f) {
				takes = append(takes, "--"+a)
			}
		}
		return "", errs.WithHints(fmt.Errorf("--%s applies to %s only", f, strings.Join(takes, " and ")), "drop it: "+runWithout(cmd, args, f))
	}
	if act == "restore" && len(args) == 0 {
		return "", errs.WithHints(errors.New("--restore needs the run to restore from"), "see the runs: "+runWithout(cmd, nil, "restore", "force", "dry-run"))
	}
	return act, nil
}

// restoreTrash is lola trash <run> [<original>...] --restore: its plan with -n, else the restore.
func (a *app) restoreTrash(cmd *cobra.Command, eng *engine.Engine, args []string, force, dry bool) error {
	req := engine.TrashRestoreRequest{Run: args[0], Force: force, DryRun: dry, Display: outputFlags(cmd)}
	for _, o := range args[1:] {
		abs, err := filepath.Abs(o)
		if err != nil {
			return err
		}
		req.Originals = append(req.Originals, abs)
	}
	plan, err := eng.PlanTrashRestore(req)
	if err != nil {
		return err
	}
	res := plan
	if !dry {
		if res, err = eng.RestoreTrash(plan); err != nil {
			return err
		}
	}
	return a.emit(cmd, func(w io.Writer, s style) { printTrashRestore(w, s, res) }, res)
}

// emptyTrash is lola trash [<run>...] --empty: its plan with -n, else asked about at nav (-y
// answers), then deleted.
func (a *app) emptyTrash(cmd *cobra.Command, eng *engine.Engine, args []string, olderThan string, dry, yes bool, nav Nav) error {
	// Given empty, it must not read as not given. The hint corrects the age and keeps it: one
	// without it would delete every run. A corrected age is a new question, so -y is dropped.
	if cmd.Flags().Changed("older-than") {
		if _, err := engine.ParseOlderThan(olderThan); err != nil {
			return errs.WithHints(err, "use an age: "+runWithout(cmd, args, "older-than", "yes")+" --older-than 30d")
		}
	}
	plan, err := eng.PlanTrashEmpty(engine.TrashEmptyRequest{Runs: args, OlderThan: olderThan, Display: outputFlags(cmd)})
	if err != nil {
		return err
	}
	if dry {
		return a.emit(cmd, func(w io.Writer, s style) { printTrashEmpty(w, s, plan) }, plan)
	}
	if len(plan.Removed) > 0 && !yes {
		show := func(w io.Writer, s style) { printTrashEmpty(w, s, plan) }
		q := "Delete " + trashRuns(len(plan.Removed)) + "? This cannot be undone."
		if err := a.confirm(cmd, args, q, nav, plan, show); err != nil {
			return err
		}
	}
	res, err := eng.EmptyTrash(plan)
	if err != nil {
		return err
	}
	return a.emit(cmd, func(w io.Writer, s style) { printTrashEmpty(w, s, res) }, res)
}

// pickTrash is bare lola trash on a terminal: the picker of list's runs,
// newest first, each a [whole run] row followed by its files, then the plan of what was ticked,
// shown before anything is written. Restore ticked restores, from each run, what lola trash <run>
// [<original>...] --restore would (a run ticked whole, every entry), after asking; when an
// original exists, the only restore offered is --force's, and lola asks again before moving what
// is there. Empty ticked runs is lola trash <run>... --empty, which asks. A tick the action cannot
// take, or Cancel at the review, asks the picker again, saying why, its ticks and action kept; so
// does going back (Esc) from any question after it. Each file shows its credentials mark first,
// then its path relative to the run's project (whole when the run has none), so a cut to the
// terminal's width takes the path's start last.
func (a *app) pickTrash(cmd *cobra.Command, eng *engine.Engine, p Prompter, list *engine.TrashList) error {
	w, s := cmd.ErrOrStderr(), style{color: a.color(cmd.ErrOrStderr())}
	for _, n := range list.Notices {
		fmt.Fprintln(w, noticeLine(s, n))
	}
	shows := make([]*engine.TrashShow, len(list.Runs))
	var opts []TidyOption
	for i, r := range list.Runs {
		show, err := eng.ShowTrash(r.ID, outputFlags(cmd)) // its notices are list.Notices'
		if err != nil {
			return err
		}
		shows[i] = show
		opts = append(opts, TidyOption{ID: strconv.Itoa(i),
			Label: "[whole run] " + trashTime(r) + "  " + r.Command + "  " + trashWhere(r) + "  " + trashFiles(r.Files) + ", " + sizeText(r.SizeBytes)})
		for j, en := range show.Entries {
			label := "    "
			if en.Credentials {
				label += "(may hold credentials) "
			}
			path := en.Original
			if rel, err := filepath.Rel(r.Root, path); r.Root != "" && err == nil && filepath.IsLocal(rel) {
				path = rel
			}
			opts = append(opts, TidyOption{ID: strconv.Itoa(i) + "/" + strconv.Itoa(j), Label: label + path})
		}
	}
	st := TrashPickStep{Options: opts}
	for {
		ans, err := p.TrashPick(st)
		if err != nil {
			return err
		}
		st.Preselect, st.Action = ans.Picked, ans.Action
		whole, files, err := trashTicked(shows, ans.Picked)
		if err != nil {
			return err
		}
		switch {
		case len(whole) == 0 && len(files) == 0:
			st.Problem = "nothing ticked: tick files or runs, then pick what to do with them"
		case ans.Action == TrashEmpty && len(files) > 0:
			st.Problem = "Empty ticked runs deletes whole runs: tick a [whole run] row, not only files"
		case ans.Action == TrashEmpty:
			var ids []string
			for i, show := range shows {
				if whole[i] {
					ids = append(ids, show.ID)
				}
			}
			if err := a.emptyTrash(cmd, eng, ids, "", false, false, Nav{Back: true, Answered: 1}); !errors.Is(err, ErrBack) {
				return err
			}
			st.Problem = ""
		default:
			problem, err := a.restorePicked(cmd, eng, p, shows, whole, files)
			if !errors.Is(err, ErrBack) && (err != nil || problem == "") {
				return err
			}
			st.Problem = problem
		}
	}
}

// trashTicked splits the trash picker's ticks into the runs ticked whole and, for each other run,
// the entries ticked, both by index into shows.
func trashTicked(shows []*engine.TrashShow, picked []string) (map[int]bool, map[int][]int, error) {
	whole, files := map[int]bool{}, map[int][]int{}
	for _, id := range picked {
		run, entry, isFile := strings.Cut(id, "/")
		i, err := strconv.Atoi(run)
		if err != nil || i < 0 || i >= len(shows) {
			return nil, nil, fmt.Errorf("the trash picker answered a row it did not offer: %q", id)
		}
		if !isFile {
			whole[i] = true
			continue
		}
		j, err := strconv.Atoi(entry)
		if err != nil || j < 0 || j >= len(shows[i].Entries) {
			return nil, nil, fmt.Errorf("the trash picker answered a row it did not offer: %q", id)
		}
		files[i] = append(files[i], j)
	}
	for i := range whole {
		delete(files, i)
	}
	return whole, files, nil
}

// restorePicked is pickTrash's Restore ticked: each run's plan, as lola trash <run> [<original>...]
// --restore makes it (the files ticked named by identity, so two whose paths print alike stay
// apart), shown, then Restore, or, when an original exists, Restore with --force, which re-plans
// those runs with --force and asks before moving what is there. It answers why the picker asks
// again when two runs would restore one original, and ErrBack for Cancel at the review and for
// going back from either question; Ctrl+C ends the run. With nothing left to restore it says so.
func (a *app) restorePicked(cmd *cobra.Command, eng *engine.Engine, p Prompter, shows []*engine.TrashShow, whole map[int]bool, files map[int][]int) (string, error) {
	var reqs []engine.TrashRestoreRequest
	var plans []*engine.TrashRestoreResult
	for i, show := range shows {
		if !whole[i] && len(files[i]) == 0 {
			continue
		}
		req := engine.TrashRestoreRequest{Run: show.ID, Display: outputFlags(cmd)}
		for _, j := range files[i] {
			req.Entries = append(req.Entries, show.Ref(j))
		}
		plan, err := eng.PlanTrashRestore(req)
		if err != nil {
			return "", err
		}
		reqs, plans = append(reqs, req), append(plans, plan)
	}
	if err := engine.TrashRestoresOverlap(plans...); err != nil {
		return err.Error() + "; untick one", nil
	}
	show := func(w io.Writer, s style) {
		for _, plan := range plans {
			printTrashRestore(w, s, plan)
		}
	}
	restored, existing := trashCounts(plans)
	title := "Restore " + engine.CountItems(restored) + " from the trash?"
	if restored == 0 {
		title = "" // no question follows the plan
	}
	if err := a.pageThen(cmd, cmd.ErrOrStderr(), style{color: a.color(cmd.ErrOrStderr())}, title, show); err != nil {
		return "", err // Ctrl+C in the plan's viewer: the question is not asked
	}
	if restored == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), a.style(cmd).info("nothing to restore, so nothing changed"))
		return "", nil
	}
	choice := "Restore"
	if existing > 0 {
		choice = "Restore with --force: first move what is there into a new trash run"
	}
	_, err := p.Where(WhereStep{Title: title, Options: []string{choice},
		Nav: Nav{Back: true, Answered: 1}})
	if errors.Is(err, ErrCancelled) {
		return "", ErrBack
	}
	if err != nil {
		return "", err
	}
	if existing > 0 {
		for k, plan := range plans {
			if len(plan.Existing) == 0 {
				continue
			}
			reqs[k].Force = true
			if plans[k], err = eng.PlanTrashRestore(reqs[k]); err != nil {
				return "", err
			}
		}
		restored, existing = trashCounts(plans)
		q := "Move " + engine.CountItems(existing) + " in the way into a new trash run, then restore " + engine.CountItems(restored) + "?"
		if err := a.confirm(cmd, nil, q, Nav{Back: true, Answered: 2}, plans, show); err != nil {
			return "", err
		}
	}
	for _, plan := range plans {
		res, err := eng.RestoreTrash(plan)
		if err != nil {
			return "", err
		}
		if err := a.emit(cmd, func(w io.Writer, s style) { printTrashRestore(w, s, res) }, res); err != nil {
			return "", err
		}
	}
	return "", nil
}

// trashCounts is the entries plans restore and the originals in their way.
func trashCounts(plans []*engine.TrashRestoreResult) (restored, existing int) {
	for _, plan := range plans {
		restored, existing = restored+len(plan.Restored), existing+len(plan.Existing)
	}
	return restored, existing
}

// trashRuns is "1 trash run" or "n trash runs".
func trashRuns(n int) string {
	if n == 1 {
		return "1 trash run"
	}
	return strconv.Itoa(n) + " trash runs"
}

// trashWhere is a run's scope and project, as a listing shows them.
func trashWhere(r engine.TrashRun) string {
	return strings.TrimSpace(r.Scope + " " + r.Root)
}

// trashTime is a run's time as human output shows it.
func trashTime(r engine.TrashRun) string {
	if r.Time == "" {
		return "unknown time"
	}
	return strings.Replace(strings.TrimSuffix(r.Time, "Z"), "T", " ", 1) + " UTC"
}

// trashFiles is a run's file count, as human output shows it.
func trashFiles(n int) string {
	if n == 1 {
		return "1 file"
	}
	return strconv.Itoa(n) + " files"
}

// printTrashList prints lola trash: a row per run, newest first; --plain rows are run, id, time,
// command, scope, root, files and bytes. The engine escaped every text.
func printTrashList(w io.Writer, s style, list *engine.TrashList) {
	if s.plain {
		for _, r := range list.Runs {
			fmt.Fprintln(w, strings.Join([]string{"run", r.ID, r.Time, r.Command, r.Scope, r.Root, strconv.Itoa(r.Files),
				strconv.FormatInt(r.SizeBytes, 10)}, "\t"))
		}
	} else if len(list.Runs) == 0 {
		fmt.Fprintln(w, s.ok("the trash is empty"))
	} else {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, r := range list.Runs {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, trashTime(r), r.Command, trashWhere(r), trashFiles(r.Files), sizeText(r.SizeBytes))
		}
		_ = tw.Flush()
	}
	for _, n := range list.Notices {
		fmt.Fprintln(w, noticeLine(s, n))
	}
}

// printTrashShow prints lola trash <run>: the run, then each entry, original first, with the run
// that restores them all (again); --plain rows are the run's row, then entry, original, trash path
// and whether it may hold credentials.
func printTrashShow(w io.Writer, s style, show *engine.TrashShow, again string) {
	if s.plain {
		printTrashList(w, s, &engine.TrashList{Runs: []engine.TrashRun{show.TrashRun}})
		for _, en := range show.Entries {
			fmt.Fprintln(w, strings.Join([]string{"entry", en.Original, en.Trash, strconv.FormatBool(en.Credentials)}, "\t"))
		}
		for _, n := range show.Notices {
			fmt.Fprintln(w, noticeLine(s, n))
		}
		return
	}
	r := show.TrashRun
	fmt.Fprintf(w, "trash run %s: %s, %s (%s), %s, %s\n", r.ID, trashTime(r), r.Command, trashWhere(r), trashFiles(r.Files), sizeText(r.SizeBytes))
	for _, en := range show.Entries {
		mark := ""
		if en.Credentials {
			mark = " (may hold credentials)"
		}
		fmt.Fprintf(w, "  %s%s → %s\n", en.Original, mark, en.Trash)
	}
	for _, n := range show.Notices {
		fmt.Fprintln(w, noticeLine(s, n))
	}
	if len(show.Entries) > 0 {
		fmt.Fprintln(w, s.info(show.RestoreHint(again)))
	}
}

// printTrashRestore prints a restore's plan (-n) or what it did; --plain rows are restore or
// restored with the original and trash path, exists with an original in the way, displaced and
// removed with a run's id.
func printTrashRestore(w io.Writer, s style, res *engine.TrashRestoreResult) {
	if s.plain {
		act := "restored"
		if res.DryRun {
			act = "restore"
		}
		for _, en := range res.Restored {
			fmt.Fprintln(w, act+"\t"+en.Original+"\t"+en.Trash)
		}
		for _, o := range res.Existing {
			fmt.Fprintln(w, "exists\t"+o)
		}
		if res.Displaced != "" {
			fmt.Fprintln(w, "displaced\t"+res.Displaced)
		}
		if res.RunRemoved {
			fmt.Fprintln(w, "removed\t"+res.Run)
		}
		for _, n := range res.Notices {
			fmt.Fprintln(w, noticeLine(s, n))
		}
		return
	}
	for _, n := range res.Notices {
		fmt.Fprintln(w, noticeLine(s, n))
	}
	switch {
	case len(res.Restored) == 0:
		fmt.Fprintln(w, s.info("nothing to restore from trash run "+res.Run))
	case res.DryRun:
		fmt.Fprintln(w, "would restore from trash run "+res.Run+":")
	default:
		fmt.Fprintln(w, s.ok("restored "+engine.CountItems(len(res.Restored))+" from trash run "+res.Run))
	}
	for _, en := range res.Restored {
		fmt.Fprintln(w, "    "+en.Original)
	}
	if res.Displaced != "" {
		fmt.Fprintln(w, s.info("moved what was in their place to trash run "+res.Displaced))
	}
	if res.RunRemoved {
		fmt.Fprintln(w, s.info("trash run "+res.Run+" is empty now, and was removed"))
	}
}

// printTrashEmpty prints an empty's plan (-n, or what lola asks about) or what it deleted; --plain
// rows are delete or deleted, the run's id and its bytes.
func printTrashEmpty(w io.Writer, s style, res *engine.TrashEmptyResult) {
	if s.plain {
		act := "deleted"
		if res.DryRun {
			act = "delete"
		}
		for _, r := range res.Removed {
			fmt.Fprintf(w, "%s\t%s\t%d\n", act, r.ID, r.SizeBytes)
		}
		for _, n := range res.Notices {
			fmt.Fprintln(w, noticeLine(s, n))
		}
		return
	}
	for _, n := range res.Notices {
		fmt.Fprintln(w, noticeLine(s, n))
	}
	switch {
	case len(res.Removed) == 0:
		fmt.Fprintln(w, s.ok("nothing to delete"))
	case res.DryRun:
		fmt.Fprintln(w, "will delete from the trash:")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, r := range res.Removed {
			fmt.Fprintf(tw, "    %s\t%s\t%s\n", r.ID, r.Command, sizeText(r.SizeBytes))
		}
		_ = tw.Flush()
		fmt.Fprintf(w, "  total: %s\n", sizeText(res.BytesFreed))
	default:
		fmt.Fprintln(w, s.ok("deleted "+trashRuns(len(res.Removed))+", freeing "+sizeText(res.BytesFreed)))
	}
}
