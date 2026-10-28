package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/engine"
)

func (a *app) repairCmd() *cobra.Command {
	var dry, yes bool
	cmd := &cobra.Command{
		Use:   "repair",
		Short: "Forget lock.json entries lola could not have written",
		Long: "Forget what lock.json records that lola could not have written: an artifact or created path\n" +
			"that leaves its target's root (../, an absolute path), and a target lola does not know. Every\n" +
			"other command refuses such a lock.json and hints this one. lola repair lists the entries, asks\n" +
			"(default No), then forgets them; a module left installed on no target lola knows is forgotten\n" +
			"with them. It deletes and moves no file; lola sync then puts back any file a module lacks. -y\n" +
			"answers the question, and off a terminal it is needed; -n lists the entries and writes nothing.\n" +
			"A lock.json lola refuses for another reason keeps that refusal.\n\n" + scopeHelp,
		Example: "  lola repair\n  lola repair -n\n  lola repair --user -y",
		GroupID: groupAdditional,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			eng, err := a.engine()
			if err != nil {
				return err
			}
			scs, err := a.scopes(cmd, eng, nil, "Repair where?", false, false)
			if err != nil {
				return err
			}
			plan, err := eng.PlanRepair(engine.RepairRequest{Scope: scs[0].Scope, Display: outputFlags(cmd)})
			if err != nil {
				return err
			}
			if dry {
				return a.emit(cmd, func(w io.Writer, s style) { printRepair(w, s, plan) }, plan)
			}
			if n := len(plan.Forgotten); n > 0 && !yes {
				show := func(w io.Writer) { printRepair(w, style{}, plan) }
				question := "Forget " + countEntries(n) + " lola could not have written? Files are not touched."
				if err := a.confirm(cmd, nil, question, Nav{}, plan, show); err != nil {
					return err
				}
			}
			res, err := eng.ApplyRepair(plan)
			if err != nil {
				return err
			}
			return a.emit(cmd, func(w io.Writer, s style) { printRepair(w, s, res) }, res)
		},
	}
	cmd.Flags().BoolVarP(&dry, "dry-run", "n", false, "list what lola would forget and write nothing")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

// countEntries is n lock.json entries, as lola repair's list and question say it.
func countEntries(n int) string {
	if n == 1 {
		return "1 entry"
	}
	return fmt.Sprintf("%d entries", n)
}

// printRepair prints a repair's plan (dry run) or its result: the entries it forgets or forgot,
// the modules left on no target lola knows, and its notices; --plain rows are the action (forget
// or forgot) and the entry, or "module <name>".
func printRepair(w io.Writer, s style, r *engine.RepairResult) {
	act := "forgot"
	if r.DryRun {
		act = "forget"
	}
	if s.plain {
		for _, it := range r.Forgotten {
			fmt.Fprintf(w, "%s\t%s\n", act, it.Entry)
		}
		for _, m := range r.ForgottenModules {
			fmt.Fprintf(w, "%s\tmodule %s\n", act, m)
		}
		for _, n := range r.Notices {
			fmt.Fprintln(w, noticeLine(s, n))
		}
		return
	}
	if len(r.Forgotten) == 0 {
		fmt.Fprintln(w, s.ok("nothing to repair in "+r.File))
		return
	}
	if r.DryRun {
		fmt.Fprintf(w, "will forget %s in %s; files are not touched:\n", countEntries(len(r.Forgotten)), r.File)
	} else {
		fmt.Fprintln(w, s.ok(fmt.Sprintf("forgot %s in %s; no file was touched:", countEntries(len(r.Forgotten)), r.File)))
	}
	for _, it := range r.Forgotten {
		fmt.Fprintln(w, "    "+it.Entry)
	}
	for _, m := range r.ForgottenModules {
		fmt.Fprintln(w, "    module "+m+": no target lola knows is left")
	}
	for _, n := range r.Notices {
		fmt.Fprintln(w, noticeLine(s, n))
	}
}
