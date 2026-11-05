package cli

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/trust"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// syncAborted is a sync the user cancelled at a question: exit 0. The cache may already hold
// content of entries the user trusted, so "nothing changed" would be untrue (Plan 6a §3.4).
func syncAborted(sc engine.Scope) error {
	return &abortedError{"aborted; " + unchanged(sc) + " was not changed."}
}

// unchanged names the scope a refused or declined sync left as it was.
func unchanged(sc engine.Scope) string {
	if sc.Kind == target.User {
		return "user scope"
	}
	return "the project"
}

// removalsDeclined is a No to a sync's removals (Plan 6a §9.3 step 9): nothing was applied, but
// the trust the user accepted before was recorded, which the message says, with what the next
// run (the sync, or the activation, repeated) asks about again and what render would now install,
// each command carrying sc's flag.
func removalsDeclined(eng *engine.Engine, sc engine.Scope, d *engine.RemovalsDeclinedError) error {
	msg := "removals declined; " + unchanged(sc) + " was not changed."
	again := ""
	if len(d.Again) > 0 {
		again = "about the removals and about " + strings.Join(d.Again, ", ") +
			" again: lola records a pin or a folder's content only once it writes it."
	}
	switch {
	case d.Recorded && again == "":
		msg += " The changes you trusted are recorded; the next " + d.Retry + " asks only about the removals."
	case d.Recorded:
		msg += " The changes you trusted are recorded; the next " + d.Retry + " asks " + again
	case again != "":
		msg += " The next " + d.Retry + " asks " + again
	}
	if d.Render {
		msg += " " + eng.Command(sc, "render") + " would install what you trusted."
	}
	return &abortedError{msg}
}

// syncFlags are lola sync's own flags.
type syncFlags struct{ dry, yes, trust, force, update, allowHooks, allowRuntime bool }

// syncedScopesError is a sync over several scopes (--scope both) that stopped at err after the
// scopes in done (none, when the first failed) were synced: their results stay in the --json
// error document. skipped names the scopes it never reached, for the human message only.
type syncedScopesError struct {
	err     error
	done    []*engine.SyncResult
	skipped []engine.Scope
}

func (e *syncedScopesError) Error() string {
	msg := e.err.Error()
	if len(e.skipped) == 0 {
		return msg
	}
	var names []string
	for _, sc := range e.skipped {
		if sc.Kind == target.User {
			names = append(names, "user scope")
		} else {
			names = append(names, "project scope at "+untrusted.Text(sc.ProjectDir, untrusted.MaxName))
		}
	}
	verb := " was not synced"
	if len(names) > 1 {
		verb = " were not synced"
	}
	msg, period := strings.CutSuffix(msg, ".")
	msg += "; " + strings.Join(names, ", ") + verb
	if period {
		msg += "."
	}
	return msg
}
func (e *syncedScopesError) Unwrap() error { return e.err }

// JSONFields carries the synced scopes beside the fields of err's own document, if any.
func (e *syncedScopesError) JSONFields() map[string]any {
	fields := map[string]any{}
	if f, ok := errors.AsType[jsonFielder](e.err); ok {
		maps.Copy(fields, f.JSONFields())
	}
	fields["scopes"] = e.done
	return fields
}

func (a *app) syncCmd() *cobra.Command {
	var f syncFlags
	cmd := &cobra.Command{
		Use:   "sync [<module>...]",
		Short: "Make this project (or --user) match its manifest",
		Long: "Install every module project.yml lists at the version it pins, render the modules already\n" +
			"installed, and remove the ones project.yml no longer lists (asking first; -y confirms).\n" +
			"Name modules to sync only those. An entry without a pin is installed from your cache's\n" +
			"version for its ref, and lola writes the pin into project.yml. --update fetches the refs\n" +
			"again and re-pins; an entry installed through a marketplace (market:) follows its catalog,\n" +
			"fetched once, at the same source, path and ref only, and says why when it cannot.\n" +
			"When project.yml changed since lola last wrote it or you last trusted it (after a git pull,\n" +
			"say), sync shows each change with the files, MCP servers and hooks it brings, and asks before\n" +
			"anything is cached or installed. Off a terminal, --trust accepts the changes (of the modules\n" +
			"named, if any); -y does not.\n" +
			"On a terminal, the diff (and a dry run's plan) is paged, in full, with $LOLA_PAGER, else $PAGER,\n" +
			"else less; the question comes after the pager exits. --no-pager, or pager: false in\n" +
			"$XDG_CONFIG_HOME/lola/config.yaml, prints it instead.\n" +
			"Install hooks, runtime hooks and agent MCP servers still need their own consent: neither -y\n" +
			"nor --trust gives it.\n\n" + scopeHelp + "\n" +
			"--scope both syncs the project, then user scope, and stops at the first that fails or is\n" +
			"declined.",
		Example: "  lola sync\n  lola sync --dry-run\n  lola sync review-kit\n  lola sync --update review-kit\n" +
			"  lola sync --trust -y\n  lola sync --user -y\n  lola sync --scope both",
		GroupID:           groupCore,
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: a.completeSync,
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := a.engine()
			if err != nil {
				return err
			}
			scs, err := a.scopes(cmd, eng, args, "Sync where?", true, true)
			if err != nil {
				return err
			}
			pg, err := a.pager(cmd, eng)
			if err != nil {
				return err
			}
			// Several scopes (Plan 6b §19 R13): in order, each reported under its label as it is
			// synced, stopping at the first that fails or is declined; --json prints one document
			// once every scope is synced.
			done := []*engine.SyncResult{}
			for i, c := range scs {
				res, more, err := a.syncScope(cmd, eng, pg, c.Scope, args, nil, f)
				if err != nil && len(scs) > 1 {
					serr := &syncedScopesError{err: err, done: done}
					if !a.json {
						for _, rest := range scs[i+1:] {
							serr.skipped = append(serr.skipped, rest.Scope)
						}
					}
					return serr
				}
				if err != nil {
					return err
				}
				done = append(done, res)
				name := manifestName(c.Scope)
				human := func(w io.Writer, s style, more string) {
					if len(scs) > 1 && !s.plain {
						fmt.Fprintln(w, destLabel(c.Scope)+":")
					}
					printSync(w, s, name, res, args, more)
				}
				// A dry run's diff and plan are paged, uncapped; a run's result lines are not.
				out := func(w io.Writer, s style) {
					if res.DryRun {
						pg.show(w, s, more, human)
					} else {
						human(w, s, more)
					}
				}
				switch {
				case len(scs) == 1:
					return a.emit(cmd, out, res)
				case !a.json:
					if err := a.emit(cmd, out, nil); err != nil {
						return err
					}
				}
			}
			if a.json {
				return a.emit(cmd, nil, map[string]any{"scopes": done})
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&f.dry, "dry-run", "n", false, "print the plan and the trust diff, ask nothing and write nothing")
	cmd.Flags().BoolVarP(&f.yes, "yes", "y", false, "confirm removals (only removals: it never trusts changes)")
	cmd.Flags().BoolVar(&f.trust, "trust", false, "accept the changes to project.yml and its active profiles without asking (never bypasses a refusal or grants hook consent)")
	cmd.Flags().BoolVar(&f.force, "force", false, "overwrite unowned or hand-edited files, render skipped artifacts that can be, and confirm removals (never answers a consent question)")
	cmd.Flags().BoolVar(&f.update, "update", false, "fetch the refs again and re-pin the modules of the run")
	cmd.Flags().BoolVar(&f.allowHooks, "allow-hooks", false, "run the modules' install hooks without asking (consent for this run)")
	cmd.Flags().BoolVar(&f.allowRuntime, "allow-runtime-hooks", false, "install the modules' Claude Code runtime hooks without asking (consent is recorded)")
	return cmd
}

// syncScope syncs one scope, or applies the activation prof to it (lola profile): the trust
// question and the removals' confirmation on a terminal, or the answers --trust and -y give; a
// cancel or a No is an aborted error naming the scope. The diff and the removals a question is
// about go through pg before it is asked. more is the command that lists what the human output
// caps.
func (a *app) syncScope(cmd *cobra.Command, eng *engine.Engine, pg *pager, sc engine.Scope, args []string, prof *engine.ProfileChange,
	f syncFlags) (res *engine.SyncResult, more string, err error) {
	opts := engine.Options{DryRun: f.dry, Force: f.force, Display: outputFlags(cmd)}
	if cmd.Name() == "sync" { // lola profile takes no --force: its conflicts hint nothing
		opts.ForceRun = forceRun(cmd, args)
	}
	a.hookOptions(cmd, &opts, f.allowHooks, f.allowRuntime)
	req := engine.SyncRequest{Scope: sc, Modules: args, Update: f.update, Options: opts, Profiles: prof, Display: outputFlags(cmd)}
	question := "Remove these and sync? No applies nothing"
	if prof != nil {
		question = "Remove these and apply? No applies nothing"
	}
	name, more := manifestName(sc), eng.ListAll(req)
	p := a.prompter(cmd) // never asks under --json
	stderr := style{color: a.color(cmd.ErrOrStderr())}
	diff := func(rep engine.TrustReport) func(io.Writer, style, string) {
		return func(w io.Writer, s style, more string) { printTrustDiff(w, s, name, rep, more) }
	}
	// asked is a trust answer given at the picker, whose equivalent command is printed once
	// the removals, if any, are answered.
	var asked bool
	switch {
	case f.trust:
		// --trust answers the question; the diff it accepted is still shown, as a record.
		req.Trust = func(rep engine.TrustReport) (engine.TrustAnswer, error) {
			if !a.json {
				pg.show(cmd.ErrOrStderr(), stderr, more, diff(rep))
			}
			return engine.TrustAnswer{All: true}, nil
		}
	case p.Interactive():
		req.Trust = func(rep engine.TrustReport) (engine.TrustAnswer, error) {
			pg.show(cmd.ErrOrStderr(), stderr, more, diff(rep))
			step := trustStep(rep)
			if prof != nil {
				step.Groups = nil // an activation applies all it planned or nothing: no Choose…
			}
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
	case f.yes:
		req.ConfirmRemovals = func([]engine.Removal) (bool, error) { return true, nil }
	case p.Interactive():
		req.ConfirmRemovals = func(rs []engine.Removal) (bool, error) {
			pg.show(cmd.ErrOrStderr(), stderr, more, func(w io.Writer, s style, _ string) { printRemovals(w, s, rs) })
			ok, err := p.Confirm(question)
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
	res, err = eng.Sync(cmd.Context(), req)
	if refusal, ok := errors.AsType[*engine.TrustRefusalError](err); ok && !a.json {
		printTrustDiff(cmd.ErrOrStderr(), stderr, name, refusal.Report, more)
		printRemovals(cmd.ErrOrStderr(), stderr, refusal.Removals)
	}
	if declined, ok := errors.AsType[*engine.RemovalsDeclinedError](err); ok {
		return nil, more, removalsDeclined(eng, sc, declined)
	}
	if errors.Is(err, engine.ErrDeclined) || errors.Is(err, ErrAborted) {
		return nil, more, syncAborted(sc)
	}
	if err != nil {
		return nil, more, err
	}
	if asked && res.Equivalent != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), stderr.info("the same off a terminal: "+res.Equivalent))
	}
	return res, more, nil
}

// completeSync offers the names a sync takes: what the engine says the current scope's layers
// want and has installed (SyncNames), minus the names already given.
func (a *app) completeSync(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	eng, sc, err := a.session(cmd)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	names, err := eng.SyncNames(sc)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []cobra.Completion
	for _, name := range names {
		if strings.HasPrefix(name, toComplete) && !slices.Contains(args, name) {
			out = append(out, name)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// trustStep is the trust picker for rep (Plan 6a §7.5): its change count, and an option per
// changed module, grouped by kind in the diff's order (! source host changed, ~ changed, + added,
// - removed), each "<glyph> <module> <one-line summary>", the module followed by the files it
// changed in when a project profile is one of them (Plan 6b §8.1). A module whose entries changed
// in several files is offered once, summarized by its first change: choosing it trusts all of
// them. The top-level targets have no option of their own: accepting an entry that installs to
// them accepts them. A project profile an activation puts in place of an active one counts as a
// change; an activation offers no Choose… (§4.4, P10).
func trustStep(rep engine.TrustReport) TrustStep {
	st := TrustStep{Changes: len(rep.Changes) + len(rep.Replacements)}
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
			if !g.of(c) || slices.ContainsFunc(st.Groups, func(tg TrustGroup) bool {
				return slices.ContainsFunc(tg.Options, func(o TrustOption) bool { return o.Module == c.Module })
			}) || slices.ContainsFunc(opts, func(o TrustOption) bool { return o.Module == c.Module }) {
				continue
			}
			// The files whose changes choosing it trusts, when a project profile is one of them.
			var files []string
			for _, o := range rep.Changes {
				file := cmp.Or(o.Profile, "project.yml")
				if o.Module == c.Module && !slices.Contains(files, file) {
					files = append(files, file)
				}
			}
			module := c.Module
			if len(files) > 1 || c.Profile != "" {
				module += " (" + strings.Join(files, ", ") + ")"
			}
			opts = append(opts, TrustOption{Module: c.Module, Label: g.glyph + " " + module + " " + trustSummary(c, rep.Targets)})
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
		if !slices.Contains(ans.Modules, c.Module) && !slices.Contains(left, c.Module) {
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
