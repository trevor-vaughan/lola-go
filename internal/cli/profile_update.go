package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// profileUpdate runs lola profile <name>... --update, or --update --all (Plan 6b §3.2, §6.5, §19
// P21): it asks no scope (§19 P20); each profile that changed is shown, paged, and asked about on
// its own, or accepted by --trust off a terminal, which the engine checks before any network
// contact; a No keeps that profile and goes on, Ctrl+C stops the run keeping what it updated. Then
// a sync of the modules the updates move in each scope they apply in (every known scope, or the
// one a scope flag names), in order, with sync's own questions and answers (-y, --trust),
// stopping at the first that fails or is declined, as lola sync --scope both does. A profile that
// failed makes the run fail once the others are applied.
func (a *app) profileUpdate(cmd *cobra.Command, eng *engine.Engine, args []string, all bool, f syncFlags) error {
	// The scope flags first: a hint of the names' refusal must name scopes that work.
	given, err := a.flagScope(cmd, eng, args, "")
	if err != nil {
		return err
	}
	if err := updateNamesRefusal(cmd, eng, args, all); err != nil {
		return err
	}
	// -y as given, which every command repeating the run keeps.
	var flags []string
	if f.yes {
		flags = append(flags, "-y")
	}
	// Without a scope flag, --trust answered only the profiles' consent: a scope's own trust
	// question, its entries and profiles, is asked, never answered by it (ruling R-6b3c-7; off a
	// terminal the engine refused before any network contact when the found project had changes to
	// trust). Its other kinds answer each scope's sync (Plan 9 §3.2), and the syncs a hint names
	// keep them.
	scoped := f
	if len(given) == 0 {
		scoped.trust &^= engine.TrustBare
	}
	later := slices.Clone(flags)
	if scoped.trust != 0 {
		later = append([]string{scoped.trust.Arg()}, later...)
	}
	p := a.prompter(cmd) // never asks under --json
	req := engine.ProfileUpdateRequest{Scopes: given, Names: args, All: all, DryRun: f.dry, Flags: flags, Rerun: rerunFlags(cmd, f.trust), Trust: f.trust, Receipt: a.receipt(cmd, f.trust, f.yes),
		CannotAsk: !p.Interactive()}
	stderr := style{color: a.color(cmd.ErrOrStderr())}
	// Unpaged, the content is capped, naming the command that lists all of a profile's; paged, it
	// is not.
	printer := func(rps []engine.RemoteProfile) func(io.Writer, style) {
		return func(w io.Writer, s style) {
			printRemoteProfiles(w, rps, func(rp engine.RemoteProfile) string {
				return s.more(eng.ProfileRun(given, rp.Profile, "--update", "-n", "--json"))
			})
		}
	}
	show := func(rps []engine.RemoteProfile) { a.page(cmd, cmd.ErrOrStderr(), stderr, printer(rps)) }
	req.Show = func(rps []engine.RemoteProfile) {
		if !a.json {
			show(rps)
		}
	}
	if p.Interactive() {
		req.Consent = func(rps []engine.RemoteProfile) (bool, error) {
			show(rps)
			return p.Confirm("Update " + rps[0].Profile + "?")
		}
	}
	// With --trust the command shows one receipt (Plan 9 R-9-5, CR3): the update stages the
	// profiles it keeps, each scope's sync runs up to its receipt against them, and only the yes to
	// the receipt of all of them writes the profiles, records the grants and applies each scope.
	batch := f.trust != 0 && !f.dry
	if batch {
		req.Batch = &engine.TrustBatch{}
		defer eng.DropStaged()
	}
	res, err := eng.UpdateProfile(cmd.Context(), req)
	if stopped, ok := errors.AsType[*engine.UpdateStoppedError](err); ok && batch {
		if errors.Is(stopped.Err, ErrAborted) || errors.Is(stopped.Err, context.Canceled) {
			return &abortedError{"aborted; nothing was updated."}
		}
		return stopped.Err // nothing was written
	} else if ok {
		return a.updateStopped(cmd, eng, stopped, later)
	}
	if err != nil {
		return err
	}
	var passes []*syncPass
	if batch && slices.ContainsFunc(res.Updates, func(u engine.ProfileOutcome) bool { return u.Outcome == engine.UpdateUpdated }) {
		var scopes []engine.Scope
		var applies [][]string
		for _, u := range res.Scopes {
			if len(u.Apply) > 0 {
				scopes, applies = append(scopes, u.Of), append(applies, u.Apply)
			}
		}
		var failed int
		passes, failed, err = a.collectSyncs(cmd, eng, scopes, applies, nil, scoped, f.trust, req.Batch.Items, runWithout(cmd, args))
		aborted := errors.Is(err, ErrAborted) || errors.Is(err, context.Canceled)
		if failed >= 0 && aborted {
			return &abortedError{"aborted; nothing was updated."} // a Cancel, Ctrl+C or a No to the removals (M1)
		}
		if failed >= 0 {
			// A scope's sync refused before the receipt (R-9-15): the update is kept, after the
			// receipt of its own items, and that sync refuses again below with the hints that apply
			// it later.
			passes, err = nil, nil
			if len(req.Batch.Items) > 0 {
				_, err = a.receipt(cmd, f.trust, f.yes)(req.Batch.Items)
			}
		}
		if errors.Is(err, ErrAborted) {
			return &abortedError{"aborted; nothing was updated."}
		}
		if err != nil {
			return err
		}
		if err := eng.CommitStaged(req, res); err != nil {
			if stopped, ok := errors.AsType[*engine.UpdateStoppedError](err); ok {
				return a.updateStopped(cmd, eng, stopped, later) // a later profile changed: what was written is said
			}
			return err
		}
	}
	if !slices.ContainsFunc(res.Updates, func(u engine.ProfileOutcome) bool { return u.Outcome != engine.UpdateDeclined }) && len(res.Updates) > 0 {
		// Every profile declined: the run's notices are still said.
		if !a.json {
			for _, n := range res.Notices {
				fmt.Fprintln(cmd.ErrOrStderr(), noticeLine(stderr, n))
			}
		}
		return &abortedError{"aborted; nothing was updated."}
	}
	doc := profileUpdateDoc{ProfileUpdateResult: res, Scopes: []updateScopeDoc{}}
	if !a.json {
		// The updates' own lines first; each scope's sync follows under its label.
		if err := a.emit(cmd, func(w io.Writer, s style) {
			if res.DryRun && len(res.Profiles) > 0 {
				printer(res.Profiles)(w, s)
			}
			printUpdateResult(w, s, res)
		}, nil); err != nil {
			return err
		}
	}
	// Each scope's sync is labelled when more than one runs.
	several := 0
	for _, u := range res.Scopes {
		if len(u.Apply) > 0 {
			several++
		}
	}
	for i, u := range res.Scopes {
		d := updateScopeDoc{UpdateScope: u}
		if len(u.Apply) > 0 {
			fi := scoped
			if passes != nil {
				fi.pass, passes = passes[0], passes[1:]
			}
			synced, more, err := a.syncScope(cmd, eng, u.Of, u.Apply, nil, fi)
			if err != nil {
				return updateNotApplied(eng, doc, u, res.Scopes[i+1:], later, outputFlags(cmd), err)
			}
			d.Sync = synced
			if !a.json {
				if err := a.emit(cmd, func(w io.Writer, s style) {
					if several > 1 && !s.plain {
						fmt.Fprintln(w, destLabel(u.Of)+":")
					}
					printSync(w, s, manifestName(u.Of), synced, u.Apply, s.more(more))
				}, nil); err != nil {
					return err
				}
			}
		}
		doc.Scopes = append(doc.Scopes, d)
	}
	if failed := failedErr(res); failed != nil {
		return &updateError{err: failed, doc: doc}
	}
	if a.json {
		return a.emit(cmd, nil, doc)
	}
	return nil
}

// failedErr is the error of the profiles of the run that failed, nil for none: the profile's own
// when it was the run's only one; otherwise "1 of 2 remote profiles was not updated: remote:go:
// <why>", with every failure's hints. The profiles a stop (Ctrl+C) left are not updated either,
// and are counted and named with them ("remote:docs: stopped before it was updated").
func failedErr(res *engine.ProfileUpdateResult) error {
	var whys, hints []string
	failed := false
	for _, u := range res.Updates {
		switch u.Outcome {
		case engine.UpdateFailed:
			if len(res.Updates) == 1 {
				return u.Err
			}
			failed = true
			whys = append(whys, u.Profile+": "+u.Error)
			for _, h := range u.Hints {
				if !slices.Contains(hints, h) {
					hints = append(hints, h)
				}
			}
		case engine.UpdateSkipped:
			whys = append(whys, u.Profile+": stopped before it was updated")
		}
	}
	if !failed {
		return nil
	}
	verb := " was"
	if len(whys) > 1 {
		verb = " were"
	}
	return errs.WithHints(fmt.Errorf("%d of %d remote profiles%s not updated: %s", len(whys), len(res.Updates), verb, strings.Join(whys, "; ")), hints...)
}

// applyLater is the lola sync of each of scopes that applies what the updates move there,
// keeping the run's --trust for a sync and -y, and its output flags (flags).
func applyLater(eng *engine.Engine, scopes []engine.UpdateScope, flags []string) []string {
	var out []string
	for _, u := range scopes {
		if len(u.Apply) > 0 {
			out = append(out, eng.SyncCommand(u.Of, append(slices.Clone(u.Apply), flags...)...))
		}
	}
	return out
}

// updateNamesRefusal refuses the names of an --update the run cannot take (Plan 6b §19 P21): names
// with --all; neither, listing your remote profiles with both forms; and a name given twice (go
// and remote:go are one). Each hint is the run, flags kept (runWithout), but one that names
// profiles you did not name drops the answers: --trust and -y (ruling R-6b3c-8).
func updateNamesRefusal(cmd *cobra.Command, eng *engine.Engine, args []string, all bool) error {
	switch {
	case all && len(args) > 0:
		// A hint for more profiles than were named answers no question for them (ruling R-6b3c-8).
		return errs.WithHints(errors.New("--all updates every remote profile you have, and takes no names"),
			"update every one: "+runWithout(cmd, nil, "trust", "yes"),
			"or only those named: "+runWithout(cmd, args, "all"))
	case len(args) == 0 && !all:
		names, err := eng.RemoteProfileNames()
		if err != nil {
			return err
		}
		if len(names) == 0 {
			return errs.WithHints(errors.New("you have no remote profiles to update"),
				"see the profiles you have: "+runWithout(cmd, nil, "update", "yes", "trust", "dry-run"))
		}
		var qs []string
		for _, n := range names {
			qs = append(qs, "remote:"+n)
		}
		listed := make([]string, len(qs))
		for i, q := range qs {
			listed[i] = untrusted.Text(q, untrusted.MaxName)
		}
		return errs.WithHints(fmt.Errorf("--update needs the remote profiles to update, or --all; you have %s", strings.Join(listed, ", ")),
			"name them: "+runWithout(cmd, qs, "trust", "yes"),
			"or every one: "+runWithout(cmd, nil, "trust", "yes")+" --all")
	}
	var once, seen []string
	twice := ""
	for _, n := range args {
		bare := strings.TrimPrefix(n, "remote:")
		if strings.Contains(bare, ":") {
			once = append(once, n) // another kind: the engine refuses it
			continue
		}
		if slices.Contains(seen, bare) {
			twice = cmp.Or(twice, bare)
			continue
		}
		seen, once = append(seen, bare), append(once, n)
	}
	if twice != "" {
		return errs.WithHints(fmt.Errorf("remote:%s is named twice", untrusted.Text(twice, untrusted.MaxName)), "name it once: "+runWithout(cmd, once))
	}
	return nil
}

// printUpdateResult is lola profile --update's report of each profile it was asked about, in
// order, then the notices: updated (✓), would update (a dry run), declined, and failed (✗; the
// run's error says why, with the hints); a profile up to date is its notice, and one skipped
// has no line of its own, since the run's stop message says it. --plain rows are the outcome
// (updated, would-update, up-to-date, declined, failed, skipped), the profile and its file.
func printUpdateResult(w io.Writer, s style, res *engine.ProfileUpdateResult) {
	for _, u := range res.Updates {
		switch {
		case s.plain:
			fmt.Fprintf(w, "%s\t%s\t%s\n", strings.ReplaceAll(u.Outcome, "_", "-"), u.Profile, u.File)
		case u.Outcome == engine.UpdateUpdated:
			fmt.Fprintln(w, s.ok("updated "+u.Profile+" ("+u.File+")"))
		case u.Outcome == engine.UpdateWould:
			fmt.Fprintln(w, s.info("would update "+u.Profile+" ("+u.File+")"))
		case u.Outcome == engine.UpdateDeclined:
			fmt.Fprintln(w, s.info("declined "+u.Profile+"; it is kept as it was"))
		case u.Outcome == engine.UpdateFailed:
			fmt.Fprintln(w, s.bad(u.Profile+" was not updated (the error says why)"))
		}
	}
	for _, n := range res.Notices {
		fmt.Fprintln(w, noticeLine(s, n))
	}
}

// updateStopped is the end of an update stopped at a question (Ctrl+C) or by a lock it could not
// take again (Plan 6b §19 P21): the report of each profile handled so far, then what it updated,
// kept and not applied, with the lola sync of each scope that applies it (keeping the run's hook
// flags and -y). Ctrl+C alone, with no profile failed, is an abort; a failed profile, or any
// other error, Ctrl+C's companion included, is the run's error, with its hints and those
// commands.
func (a *app) updateStopped(cmd *cobra.Command, eng *engine.Engine, stopped *engine.UpdateStoppedError, flags []string) error {
	res := stopped.Result
	if !a.json {
		if err := a.emit(cmd, func(w io.Writer, s style) { printUpdateResult(w, s, res) }, nil); err != nil {
			return err
		}
	}
	syncs := applyLater(eng, res.Scopes, slices.Concat(flags, outputFlags(cmd)))
	done := res.UpdatedText()
	failed := failedErr(res)
	isAbort := func(err error) bool { return errors.Is(err, ErrAborted) || errors.Is(err, context.Canceled) }
	// The stop's errors: Ctrl+C, and what else stopped the run with it (a scope lola could not
	// read once the profiles were written), which is reported, never dropped.
	causes := []error{stopped.Err}
	if j, ok := stopped.Err.(interface{ Unwrap() []error }); ok {
		causes = j.Unwrap()
	}
	ctrlC := slices.ContainsFunc(causes, isAbort)
	others := errors.Join(slices.DeleteFunc(slices.Clone(causes), isAbort)...)
	if ctrlC && others == nil && failed == nil {
		switch {
		case done == "":
			return &abortedError{"aborted; nothing was updated."}
		case len(syncs) > 0:
			done += ", but not applied (" + strings.Join(syncs, ", then ") + ")"
		}
		return &abortedError{"aborted; " + done + "; nothing else was updated."}
	}
	msg := "the run stopped: " + untrusted.CleanErr(stopped.Err).Error()
	if ctrlC {
		msg = "the run stopped at Ctrl+C"
		if others != nil {
			msg += ", and: " + untrusted.CleanErr(others).Error()
		}
	}
	var hints []string
	if done != "" {
		msg = done + ", but not applied; " + msg
	}
	if failed != nil {
		msg = failed.Error() + "; " + msg
		hints = errs.Hints(failed)
	}
	for _, c := range syncs {
		hints = append(hints, "apply it later: "+c)
	}
	hints = append(hints, errs.Hints(stopped.Err)...)
	doc := profileUpdateDoc{ProfileUpdateResult: res, Scopes: []updateScopeDoc{}}
	for _, u := range res.Scopes {
		doc.Scopes = append(doc.Scopes, updateScopeDoc{UpdateScope: u})
	}
	return &updateError{err: errs.WithHints(errors.New(msg), hints...), doc: doc}
}

// updateNotApplied is the end of updates kept but not applied in scope u, whose sync failed or
// was declined with err, before the scopes in rest were reached (Plan 6b §19 P20): it says so,
// with the lola sync that applies them later in u and in each of rest that they move something in
// (keeping the run's --trust for a sync and -y, and display, its output flags), but for a conflict in u, which that sync would meet again:
// u's is then the sync with --force, without the run's answers, when --force overrides every
// conflict, and none otherwise. A No at the sync's question is an abort, which says those
// commands in its message (an abort prints no hints), unless a profile failed; the --json error
// document is the run's, with the scopes done before (doc).
func updateNotApplied(eng *engine.Engine, doc profileUpdateDoc, u engine.UpdateScope, rest []engine.UpdateScope, flags, display []string, err error) error {
	syncs := applyLater(eng, append([]engine.UpdateScope{u}, rest...), slices.Concat(flags, display))
	head := doc.UpdatedText() + ", but not applied to " + unchanged(u.Of)
	failed := failedErr(doc.ProfileUpdateResult)
	// The scopes not reached, which lola sync --scope both names the same way.
	var skipped []string
	for _, r := range rest {
		if len(r.Apply) > 0 {
			skipped = append(skipped, unchanged(r.Of))
		}
	}
	tail := ""
	if len(skipped) > 0 {
		tail = "; " + strings.Join(skipped, ", ") + " was not synced"
	}
	if _, declined := errors.AsType[*abortedError](err); declined {
		msg := head + ": " + strings.TrimSuffix(err.Error(), ".") + tail + ". Apply it later: " + strings.Join(syncs, ", then ") + "."
		if failed == nil {
			return &abortedError{msg}
		}
		err = errors.New(failed.Error() + "; " + msg)
	} else {
		err = fmt.Errorf("%s: %w", head, &suffixed{err, tail})
		if failed != nil {
			err = fmt.Errorf("%s; %w", failed.Error(), err)
		}
	}
	// A conflict refuses the same sync again (ruling R-6cux-4 drops the run's answers from a
	// --force run): u's sync is hinted with --force when it overrides every conflict, else not at
	// all, the conflicts saying what to remove.
	var hints []string
	if ce, ok := errors.AsType[*plan.ConflictError](err); ok {
		syncs = syncs[1:]
		if !slices.ContainsFunc(ce.Conflicts, func(c plan.Conflict) bool { return !c.Forceable }) {
			hints = append(hints, "apply it later, overwriting what the conflicts list: "+
				eng.SyncCommand(u.Of, slices.Concat(u.Apply, []string{"--force"}, display)...))
		}
	}
	for _, c := range syncs {
		hints = append(hints, "apply it later: "+c)
	}
	hints = append(hints, errs.Hints(failed)...)
	return &updateError{err: errs.WithHints(err, hints...), doc: doc}
}

// updateError is the error of an update run that did not finish as asked (Plan 6b §19 P20, P21):
// profiles that failed, a scope not applied, a stop. Its --json document is the run's (doc:
// remote_profiles, updates, notices, dry_run, and the scopes done), beside the fields of the
// error it wraps, if any (a sync refused for want of answers carries the changes it needs them
// for).
type updateError struct {
	err error
	doc profileUpdateDoc
}

func (e *updateError) Error() string { return e.err.Error() }
func (e *updateError) Unwrap() error { return e.err }

// JSONFields carries the run's document beside error and hints, and trusted: the profiles' grants
// and what each scope's sync recorded, the failed one's included (R-9-16).
func (e *updateError) JSONFields() map[string]any {
	fields := map[string]any{}
	if f, ok := errors.AsType[jsonFielder](e.err); ok {
		maps.Copy(fields, f.JSONFields())
	}
	maps.Copy(fields, map[string]any{"remote_profiles": e.doc.Profiles, "notices": e.doc.Notices, "dry_run": e.doc.DryRun,
		"updates": e.doc.Updates, "scopes": e.doc.Scopes})
	var done []*engine.SyncResult
	for _, s := range e.doc.Scopes {
		done = append(done, s.Sync)
	}
	trusted := slices.Clone(e.doc.Trusted)
	for _, c := range written(done, e.err) {
		if !slices.Contains(trusted, c) {
			trusted = append(trusted, c)
		}
	}
	fields["trusted"] = append([]engine.Consent{}, trusted...)
	return fields
}

// profileUpdateDoc is lola profile --update's JSON document: the update's result (remote_profiles,
// updates, notices, dry_run) and each scope it applies in, with the sync that applied it there
// (Scopes shadows the result's own, adding the sync).
type profileUpdateDoc struct {
	*engine.ProfileUpdateResult
	Scopes []updateScopeDoc `json:"scopes"`
}

// updateScopeDoc is one scope of lola profile --update's JSON document: the scope, its root, the
// modules the updates move there (apply) and the sync that applied them, null when none ran.
type updateScopeDoc struct {
	engine.UpdateScope
	Sync *engine.SyncResult `json:"sync"`
}

// suffixed is err with tail after its message, its trailing period kept last; it unwraps to err,
// so its hints and --json fields stay.
type suffixed struct {
	err  error
	tail string
}

func (e *suffixed) Error() string {
	msg, period := strings.CutSuffix(e.err.Error(), ".")
	if period {
		return msg + e.tail + "."
	}
	return msg + e.tail
}
func (e *suffixed) Unwrap() error { return e.err }
