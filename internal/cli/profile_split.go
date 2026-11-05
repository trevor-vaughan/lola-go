package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// profileSplit runs lola profile <name> --split (spec 2026-10-29 §5.2): one name, the scope as
// --switch resolves it, the plan shown and "Split <name>?" asked on a terminal (-y and --force
// answer it), then, when the bundle is on there, the scope's apply with sync's questions.
func (a *app) profileSplit(cmd *cobra.Command, eng *engine.Engine, args []string, to string, f syncFlags) error {
	if len(args) != 1 {
		return errs.WithHints(errors.New("--split takes the name of one profile"), "see them: lola profile")
	}
	scs, err := a.scopes(cmd, eng, args, "Split where?", false, true)
	if err != nil {
		return err
	}
	sc := scs[0].Scope
	a.printContext(cmd, eng, []engine.Scope{sc}, nil)
	p, stderr := a.prompter(cmd), style{color: a.color(cmd.ErrOrStderr())}
	opts := engine.Options{DryRun: f.dry, Force: f.force, Display: outputFlags(cmd), ForceRun: forceRun(cmd, args), RenderRun: renderRun(cmd, args)}
	opts.Trust, opts.Receipt = f.trust, a.receipt(cmd, f.trust, f.yes)
	a.hookOptions(cmd, &opts)
	req := engine.SplitRequest{Scope: sc, Name: args[0], To: to, Yes: f.yes, Options: opts}
	if !a.json && p.Interactive() {
		req.Confirm = func(plan engine.SplitPlan) (bool, error) {
			printSplitPlan(cmd.ErrOrStderr(), stderr, plan)
			return p.Confirm("Split "+untrusted.Text(args[0], untrusted.MaxName)+"?", Nav{})
		}
	}
	name := manifestName(sc)
	diff := func(rep engine.TrustReport) func(io.Writer, style) {
		return func(w io.Writer, s style) { printTrustDiff(w, s, name, "lola profile", rep, "") }
	}
	var asked bool
	req.Show, req.Trust, req.ConfirmRemovals = a.syncQuestions(cmd, f, false, "Remove these and split? No applies nothing", diff, &asked)
	res, err := eng.Split(cmd.Context(), req)
	if errors.Is(err, engine.ErrDeclined) {
		return ErrAborted
	}
	if err != nil {
		return err
	}
	return a.emit(cmd, func(w io.Writer, s style) {
		if res.DryRun || req.Confirm == nil {
			printSplitPlan(w, s, res.Plan)
		}
		if res.DryRun {
			if res.Sync != nil {
				printSync(w, s, name, res.Sync, nil, "")
			}
			fmt.Fprintln(w, s.info("nothing written (-n)"))
			return
		}
		fmt.Fprintln(w, s.ok("split "+res.Plan.Bundle.Name+" into a bundle and "+res.Plan.Profile.Name))
		if res.Sync != nil {
			printSync(w, s, name, res.Sync, nil, "")
		}
	}, res)
}

// printSplitPlan prints what a split writes (spec 2026-10-29 §5.2): the file that becomes a bundle,
// with its members, and the new profile, with its targets and modules. --plain rows: split, the
// profile, its file, and its members or modules, comma-separated.
func printSplitPlan(w io.Writer, s style, plan engine.SplitPlan) {
	if s.plain {
		fmt.Fprintf(w, "split\t%s\t%s\t%s\n", plan.Bundle.Name, plan.Bundle.Path, strings.Join(plan.Bundle.Members, ","))
		fmt.Fprintf(w, "split\t%s\t%s\t%s\n", plan.Profile.Name, plan.Profile.Path, strings.Join(plan.Profile.Modules, ","))
		return
	}
	fmt.Fprintln(w, "  ~ "+plan.Bundle.Name+"  bundle: "+strings.Join(plan.Bundle.Members, ", ")+"  ("+plan.Bundle.Path+")")
	line := "  + " + plan.Profile.Name + "  modules: " + strings.Join(plan.Profile.Modules, ", ")
	if len(plan.Profile.Targets) > 0 {
		line += "; targets: " + strings.Join(plan.Profile.Targets, ", ")
	}
	fmt.Fprintln(w, line+"  ("+plan.Profile.Path+", new)")
}

// offerSplit wraps run, a command's RunE, with the offer of spec 2026-10-29 §5.1: a run refused by
// a profile with both bundle: and modules: shows, on a terminal, the split's plan and asks "Split
// <name> now?"; yes writes the two files and runs the command again, once. Off a terminal, under
// --json or -n, and when the answer is no, the refusal stands, with its --split hint. The other
// command's -y and --force never answer it.
func (a *app) offerSplit(run func(*cobra.Command, []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		err := run(cmd, args)
		offer, ok := errors.AsType[*engine.SplitOfferError](err)
		dry, _ := cmd.Flags().GetBool("dry-run")
		if !ok || a.json || dry || !a.prompter(cmd).Interactive() {
			return err
		}
		eng, eerr := a.engine()
		if eerr != nil {
			return err
		}
		stderr := style{color: a.color(cmd.ErrOrStderr())}
		// The plan alone is shown before the question; a No prints the refusal once (ruling P11).
		var yes bool
		var asking error
		req := engine.SplitRequest{Scope: offer.Scope, Name: offer.Name, WriteOnly: true, Options: engine.Options{Display: outputFlags(cmd)},
			Confirm: func(plan engine.SplitPlan) (bool, error) {
				printSplitPlan(cmd.ErrOrStderr(), stderr, plan)
				ok, cerr := a.prompter(cmd).Confirm("Split "+untrusted.Text(offer.Name, untrusted.MaxName)+" now?", Nav{})
				yes, asking = ok, cerr
				return ok, cerr
			}}
		// The split's own apply asks on this terminal what lola profile --split would (trust in a
		// project's member list, say); the other command's -y never answers it.
		name := manifestName(offer.Scope)
		diff := func(rep engine.TrustReport) func(io.Writer, style) {
			return func(w io.Writer, s style) { printTrustDiff(w, s, name, "lola profile", rep, "") }
		}
		var trusted bool
		req.Show, req.Trust, req.ConfirmRemovals = a.syncQuestions(cmd, syncFlags{}, false, "Remove these and split? No applies nothing", diff, &trusted)
		if _, serr := eng.Split(cmd.Context(), req); serr != nil {
			switch {
			case asking != nil: // the question itself failed (an interrupt, say)
				return asking
			case !yes: // declined, or refused before asking: the run's refusal, with its --split hint
				return err
			}
			return serr
		}
		fmt.Fprintln(cmd.ErrOrStderr(), stderr.ok("split "+untrusted.Text(offer.Name, untrusted.MaxName)+"; running it again"))
		a.contexts = nil // the run prints its context again
		return run(cmd, args)
	}
}

// offerSplits puts offerSplit around the RunE of cmd and of every command under it.
func (a *app) offerSplits(cmd *cobra.Command) {
	if cmd.RunE != nil {
		cmd.RunE = a.offerSplit(cmd.RunE)
	}
	for _, sub := range cmd.Commands() {
		a.offerSplits(sub)
	}
}
