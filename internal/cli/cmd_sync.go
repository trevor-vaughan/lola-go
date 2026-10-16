package cli

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/trust"
)

// errSyncAborted is a sync the user cancelled at a question: exit 0. The cache may already hold
// content of entries the user trusted, so "nothing changed" would be untrue (Plan 6a §3.4).
var errSyncAborted = &abortedError{"aborted; the project was not changed."}

// removalsDeclined is a No to a sync's removals (Plan 6a §9.3 step 9): nothing was applied, but
// the trust the user accepted before was recorded, which the message says, with what the next
// sync asks about again and what render would now install.
func removalsDeclined(d *engine.RemovalsDeclinedError) error {
	msg := "removals declined; the project was not changed."
	again := ""
	if len(d.Again) > 0 {
		again = "about the removals and about " + strings.Join(d.Again, ", ") +
			" again: lola records a pin or a folder's content only once it writes it."
	}
	switch {
	case d.Recorded && again == "":
		msg += " The changes you trusted are recorded; the next lola sync asks only about the removals."
	case d.Recorded:
		msg += " The changes you trusted are recorded; the next lola sync asks " + again
	case again != "":
		msg += " The next lola sync asks " + again
	}
	if d.Render {
		msg += " lola render would install what you trusted."
	}
	return &abortedError{msg}
}

func (a *app) syncCmd() *cobra.Command {
	var dry, yes, trustIt, force, update, allowHooks, allowRuntime bool
	cmd := &cobra.Command{
		Use:   "sync [<module>...]",
		Short: "Make this project (or --user) match its manifest",
		Long: "Install every module project.yml lists at the version it pins, render the modules already\n" +
			"installed, and remove the ones project.yml no longer lists (asking first; -y confirms).\n" +
			"Name modules to sync only those. An entry without a pin is installed from your cache's\n" +
			"version for its ref, and lola writes the pin into project.yml. --update fetches the refs\n" +
			"again and re-pins.\n" +
			"When project.yml changed since lola last wrote it or you last trusted it (after a git pull,\n" +
			"say), sync shows each change with the files, MCP servers and hooks it brings, and asks before\n" +
			"anything is cached or installed. Off a terminal, --trust accepts the changes (of the modules\n" +
			"named, if any); -y does not.\n" +
			"Install hooks, runtime hooks and agent MCP servers still need their own consent: neither -y\n" +
			"nor --trust gives it.",
		Example: "  lola sync\n  lola sync --dry-run\n  lola sync review-kit\n  lola sync --update review-kit\n" +
			"  lola sync --trust -y\n  lola sync --user -y",
		GroupID:           groupCore,
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: a.completeSync,
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, sc, err := a.session()
			if err != nil {
				return err
			}
			opts := engine.Options{DryRun: dry, Force: force}
			a.hookOptions(cmd, &opts, allowHooks, allowRuntime)
			req := engine.SyncRequest{Scope: sc, Modules: args, Update: update, Options: opts}
			name, more := manifestName(sc), eng.ListAll(req)
			p := a.prompter(cmd) // never asks under --json
			stderr := style{color: a.color(cmd.ErrOrStderr())}
			// asked is a trust answer given at the picker, whose equivalent command is printed once
			// the removals, if any, are answered.
			var asked bool
			switch {
			case trustIt:
				// --trust answers the question; the diff it accepted is still shown, as a record.
				req.Trust = func(rep engine.TrustReport) (engine.TrustAnswer, error) {
					if !a.json {
						printTrustDiff(cmd.ErrOrStderr(), stderr, name, rep, more)
					}
					return engine.TrustAnswer{All: true}, nil
				}
			case p.Interactive():
				req.Trust = func(rep engine.TrustReport) (engine.TrustAnswer, error) {
					printTrustDiff(cmd.ErrOrStderr(), stderr, name, rep, more)
					step := trustStep(rep)
					ans, err := p.Trust(step)
					if err != nil {
						return engine.TrustAnswer{}, err
					}
					asked = true
					fmt.Fprintln(cmd.ErrOrStderr(), stderr.info(trustEcho(step, rep, ans)))
					return ans, nil
				}
			}
			switch {
			case yes:
				req.ConfirmRemovals = func([]engine.Removal) (bool, error) { return true, nil }
			case p.Interactive():
				req.ConfirmRemovals = func(rs []engine.Removal) (bool, error) {
					printRemovals(cmd.ErrOrStderr(), stderr, rs)
					ok, err := p.Confirm("Remove these and sync? No applies nothing")
					if err == nil && ok {
						names := make([]string, len(rs))
						for i, r := range rs {
							names[i] = r.Module
						}
						fmt.Fprintln(cmd.ErrOrStderr(), stderr.info("removing "+strings.Join(names, ", ")))
					}
					return ok, err
				}
			}
			res, err := eng.Sync(cmd.Context(), req)
			if refusal, ok := errors.AsType[*engine.TrustRefusalError](err); ok && !a.json {
				printTrustDiff(cmd.ErrOrStderr(), stderr, name, refusal.Report, more)
				printRemovals(cmd.ErrOrStderr(), stderr, refusal.Removals)
			}
			if declined, ok := errors.AsType[*engine.RemovalsDeclinedError](err); ok {
				return removalsDeclined(declined)
			}
			if errors.Is(err, engine.ErrDeclined) || errors.Is(err, ErrAborted) {
				return errSyncAborted
			}
			if err != nil {
				return err
			}
			if asked && res.Equivalent != "" {
				fmt.Fprintln(cmd.ErrOrStderr(), stderr.info("the same off a terminal: "+res.Equivalent))
			}
			return a.emit(cmd, func(w io.Writer, s style) { printSync(w, s, name, res, args, more) }, res)
		},
	}
	cmd.Flags().BoolVarP(&dry, "dry-run", "n", false, "print the plan and the trust diff, ask nothing and write nothing")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "confirm removals (only removals: it never trusts changes)")
	cmd.Flags().BoolVar(&trustIt, "trust", false, "accept the changes to project.yml without asking (never bypasses a refusal or grants hook consent)")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite unowned or hand-edited files and render skipped artifacts that can be (never skips a prompt)")
	cmd.Flags().BoolVar(&update, "update", false, "fetch the refs again and re-pin the modules of the run")
	cmd.Flags().BoolVar(&allowHooks, "allow-hooks", false, "run the modules' install hooks without asking (consent for this run)")
	cmd.Flags().BoolVar(&allowRuntime, "allow-runtime-hooks", false, "install the modules' Claude Code runtime hooks without asking (consent is recorded)")
	return cmd
}

// completeSync offers the names a sync takes: the entries of the current scope's manifest and the
// modules installed there that it no longer lists, minus the names already given.
func (a *app) completeSync(_ *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	eng, sc, err := a.session()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	man, err := eng.Manifest(sc)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	led, err := eng.Ledger(sc)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	names := []string{}
	for _, ent := range man.Modules {
		names = append(names, ent.Name)
	}
	for name := range led.Modules {
		if man.Find(name) == nil {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	var out []cobra.Completion
	for _, name := range names {
		if strings.HasPrefix(name, toComplete) && !slices.Contains(args, name) {
			out = append(out, name)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// trustStep is the trust picker for rep (Plan 6a §7.5): its change count, and an option per
// changed entry, grouped by kind in the diff's order (! source host changed, ~ changed, + added,
// - removed), each "<glyph> <module> <one-line summary>". The top-level targets have no option
// of their own: accepting an entry that installs to them accepts them.
func trustStep(rep engine.TrustReport) TrustStep {
	st := TrustStep{Changes: len(rep.Changes)}
	if rep.Targets != nil {
		st.Changes++ // the diff's targets row
	}
	for _, g := range []struct {
		title, glyph string
		of           func(engine.TrustChange) bool
	}{
		{"Source host changed", "!", func(c engine.TrustChange) bool { return c.HostChanged }},
		{"Changed", changeGlyphs[trust.ChangeChanged], func(c engine.TrustChange) bool { return !c.HostChanged && c.Kind == trust.ChangeChanged }},
		{"Added", changeGlyphs[trust.ChangeAdded], func(c engine.TrustChange) bool { return !c.HostChanged && c.Kind == trust.ChangeAdded }},
		{"Removed", changeGlyphs[trust.ChangeRemoved], func(c engine.TrustChange) bool { return c.Kind == trust.ChangeRemoved }},
	} {
		var opts []TrustOption
		for _, c := range rep.Changes {
			if g.of(c) {
				opts = append(opts, TrustOption{Module: c.Module, Label: g.glyph + " " + c.Module + " " + trustSummary(c, rep.Targets)})
			}
		}
		if len(opts) > 0 {
			st.Groups = append(st.Groups, TrustGroup{Title: fmt.Sprintf("%s (%d)", g.title, len(opts)), Options: opts})
		}
	}
	return st
}

// trustSummary is one change on one line for the picker: what moves, why lola asks, and what
// the content changes; every value has been through untrusted.Text.
func trustSummary(c engine.TrustChange, targets *trust.FieldChange) string {
	if c.Kind == trust.ChangeRemoved {
		return "stop managing it and remove its files"
	}
	var parts []string
	for _, f := range c.Fields {
		switch {
		case f.Field == "commit" || f.Field == "digest":
		case c.Kind == trust.ChangeAdded:
			parts = append(parts, f.Field+" "+shownValue(f.To))
		case c.HostChanged && f.Field == "source":
			parts = append(parts, "source host changed: "+shownValue(f.From)+" → "+shownValue(f.To))
		default:
			parts = append(parts, f.Field+" "+shownValue(f.From)+" → "+shownValue(f.To))
		}
	}
	if c.Reason != "" {
		parts = append(parts, c.Reason)
	} else if slices.ContainsFunc(c.Fields, func(f trust.FieldChange) bool { return f.Field == "digest" }) && c.Kind != trust.ChangeAdded {
		parts = append(parts, "new pin")
	}
	for _, m := range c.MCPServers {
		if m.Change != engine.ChangeUnchanged {
			parts = append(parts, "MCP server "+m.Name+" "+m.Change)
		}
	}
	for _, kind := range []string{trust.ChangeAdded, trust.ChangeChanged, trust.ChangeRemoved} {
		switch n := len(slices.DeleteFunc(slices.Clone(c.Hooks), func(h engine.ContentHook) bool { return h.Change != kind })); {
		case n == 1:
			parts = append(parts, "1 hook "+kind)
		case n > 1:
			parts = append(parts, fmt.Sprintf("%d hooks %s", n, kind))
		}
	}
	switch {
	case !c.InstallsToTargets || targets == nil:
	case targets.From == nil: // the diff's + targets row: there were none to change from
		parts = append(parts, "accepts the top-level targets "+shownValue(targets.To))
	default:
		parts = append(parts, "accepts the top-level targets "+shownValue(targets.From)+" → "+shownValue(targets.To))
	}
	return strings.Join(parts, "; ")
}

// trustEcho says the trust answer on stderr, since huh leaves it in the scrollback only as the
// question (Plan 6a §3.4): "trusting all 4 changes", or the entries trusted and those left as
// installed.
func trustEcho(st TrustStep, rep engine.TrustReport, ans engine.TrustAnswer) string {
	if ans.All {
		if st.Changes == 1 {
			return "trusting this change"
		}
		return fmt.Sprintf("trusting all %d changes", st.Changes)
	}
	var left []string
	for _, c := range rep.Changes {
		if !slices.Contains(ans.Modules, c.Module) {
			left = append(left, c.Module)
		}
	}
	echo := "trusting " + strings.Join(ans.Modules, ", ")
	if len(left) > 0 {
		echo += "; leaving " + strings.Join(left, ", ") + " as installed"
	}
	return echo
}

// manifestName is how output names the scope's manifest.
func manifestName(sc engine.Scope) string {
	if sc.Kind == target.User {
		return "user.yml"
	}
	return filepath.Join(".lola", "project.yml")
}
