package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// UnitID identifies one unit lola tidy offers (Plan 6d §6.1): its target and kind, its
// resolved absolute path (a link unit's through its parent, never through the link), and for an
// MCP unit the member's JSON Pointer.
type UnitID struct{ Target, Kind, Path, Key string }

// TidyUnit is one item a tidy run lists: a unit (candidate, moved or protected), or in Skipped
// a whole destination. Path and Trash are raw, but for JSON's escaping (R-6d-9); Name, Key,
// Reason and every json:"-" string have been through untrusted.Text.
type TidyUnit struct {
	Target string `json:"target"`
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Path   string `json:"path"`
	Key    string `json:"key,omitempty"`
	Trash  string `json:"trash,omitempty"`
	Code   string `json:"code,omitempty"`
	Reason string `json:"reason,omitempty"`

	ID    UnitID `json:"-"`
	Rel   string `json:"-"` // root-relative and slash-separated: what human output shows
	Link  string `json:"-"` // a link unit's target
	Label string `json:"-"` // an MCP unit's server label (§11.1), "" for none
	Size  int64  `json:"-"` // bytes in the unit's files
	Dir   bool   `json:"-"` // the unit is a directory
}

// TidyScan is what a scan found (§6.2): the candidates, the units locked against tidy, and the
// destinations it skipped whole, each sorted by target, kind, name and path.
type TidyScan struct {
	Candidates, Protected, Skipped []TidyUnit
}

// TidyChoice is what moves, by unit identity, and what joins the keep-list (escaped literal
// selectors, §6.3), with the equivalent command when one selects exactly Move ("" otherwise).
// Items are the units Move names, for the review to list. KeepAdd and KeepRemove are the
// keep-list edits the run makes, through untrusted.Text: each entry once, with KeepAdd leaving
// out what keep.yaml already holds. Hidden says an equivalent command exists but untrusted.Text
// would alter it (a project root with characters lola does not print, or a word over
// untrusted.MaxName), so Command is "".
type TidyChoice struct {
	Move                []UnitID
	Keep                []config.Pattern
	Command             string
	Hidden              bool
	Items               []TidyUnit
	KeepAdd, KeepRemove []string
}

// TidyRequest is one lola tidy run (Plan 6d §6.1). Choose and Confirm are nil when lola cannot
// ask: off a terminal; on one the CLI sets both, and the engine calls Choose only for a run
// without names, --all, -n, --force, --keep or --unkeep.
type TidyRequest struct {
	Scope          Scope
	Targets        []string         // -t; nil: the scope's targets (§4.3)
	Select         []config.Pattern // names; nil with All false: Choose, or refuse (§6.3)
	All            bool
	IncludeTracked bool // --include-tracked as given; Force implies it
	Force          bool // names or All required; Confirm is never called
	Yes            bool // -y: Confirm is never called
	Keep, Unkeep   []config.Pattern
	Dry            bool
	// Interactive says lola runs on a terminal: a hint repeating the run leaves -y out, since
	// the run it names asks the review there, and a dry run without names hints the run without
	// --all, which runs the picker there.
	Interactive bool
	// Display is the run's output flags as given (--json, --plain, --no-pager), which every hint
	// keeps (6b §12.3, R-6d2-5); never -n, since a dry run's hint is the real run.
	Display []string
	// Choose asks which units to move and which to keep; nil means it cannot ask.
	Choose func(TidyScan) (TidyChoice, error)
	// Confirm asks the review; nil means it cannot ask.
	Confirm func(TidyChoice) (bool, error)
}

// includeTracked reports whether tracked units are offered: --include-tracked or --force.
func (req TidyRequest) includeTracked() bool { return req.IncludeTracked || req.Force }

// TidyResult is what lola tidy reports (§11.2). Arrays are never nil.
type TidyResult struct {
	Scope            string     `json:"scope"`
	Root             string     `json:"root"`
	DryRun           bool       `json:"dry_run"`
	Targets          []string   `json:"targets"`
	Trash            string     `json:"trash"`
	SizeBytes        int64      `json:"size_bytes"`
	Candidates       []TidyUnit `json:"candidates"`
	Moved            []TidyUnit `json:"moved"`
	Protected        []TidyUnit `json:"protected"`
	Skipped          []TidyUnit `json:"skipped"`
	KeepAdded        []string   `json:"keep_added"`
	KeepRemoved      []string   `json:"keep_removed"`
	TrashCredentials []string   `json:"trash_credentials"`
	Notices          []Notice   `json:"notices"`
	Hints            []string   `json:"hints"`
}

func newTidyResult(req TidyRequest) *TidyResult {
	res := &TidyResult{Scope: req.Scope.Kind.String(), DryRun: req.Dry, Targets: []string{}, Candidates: []TidyUnit{}, Moved: []TidyUnit{},
		Protected: []TidyUnit{}, Skipped: []TidyUnit{}, KeepAdded: []string{}, KeepRemoved: []string{}, TrashCredentials: []string{},
		Notices: []Notice{}, Hints: []string{}}
	if req.Scope.Kind == target.Project {
		res.Root = req.Scope.ProjectDir
	}
	return res
}

// TidyError is a tidy run that stopped: a refusal, which lists the candidates it was given, or
// a failed move, which reports what moved before it. JSON output carries Result's lists.
type TidyError struct {
	Result *TidyResult
	err    error
}

func (e *TidyError) Error() string { return e.err.Error() }
func (e *TidyError) Unwrap() error { return e.err }

// JSONFields are the keys a --json refusal carries beside error and hints.
func (e *TidyError) JSONFields() map[string]any {
	r := e.Result
	return map[string]any{"scope": r.Scope, "root": r.Root, "dry_run": r.DryRun, "targets": r.Targets, "trash": r.Trash,
		"size_bytes": r.SizeBytes, "candidates": r.Candidates, "moved": r.Moved, "protected": r.Protected, "skipped": r.Skipped,
		"keep_added": r.KeepAdded, "keep_removed": r.KeepRemoved, "trash_credentials": r.TrashCredentials, "notices": r.Notices}
}

// errUnoffered refuses a picker answer that names what the scan did not offer (R-6d-N3).
var errUnoffered = errors.New("the picker answered with an item lola did not offer")

// TidyOneScope refuses a run given two scopes (--scope both, §3.3): each run is one scope's
// consent. The hints are the run at req.Scope, the project, and at user scope.
func (e *Engine) TidyOneScope(req TidyRequest) error {
	user := req
	user.Scope = Scope{Kind: target.User}
	return errs.WithHints(errors.New("lola tidy acts on one scope per run"), tidyHints(e.tidyRerun(req), e.tidyRerun(user))...)
}

// Tidy moves what lola does not manage out of the scope's destinations into the trash (Plan
// 6d): it scans, selects by names, --all or Choose, asks Confirm, moves, then writes the
// keep-list edits.
func (e *Engine) Tidy(ctx context.Context, req TidyRequest) (*TidyResult, error) {
	if err := e.tidyArgs(req); err != nil {
		return nil, err
	}
	res := newTidyResult(req)
	if err := e.refuseSymlinkedState(req.Scope); err != nil {
		return nil, err
	}
	unlock := func() {}
	if !req.Dry {
		u, err := e.lock(req.Scope)
		if err != nil {
			return nil, err
		}
		unlock = u
	}
	done, err := e.tidyLocked(ctx, req, res, &unlock)
	if err == nil && !req.Dry {
		// The keep-list is written under the scope lock (scope, then keep), so a run waiting on
		// the scope lock reads it as this run left it.
		done.keepErr = done.keep.write(e, req.Scope, res)
		if done.keepErr != nil {
			keepOnly := TidyRequest{Scope: req.Scope, Targets: req.Targets, IncludeTracked: req.IncludeTracked,
				Keep: done.keep.add, Unkeep: done.keep.remove, Display: req.Display}
			done.keepCmd = e.tidyRun(keepOnly, nil)
		}
	}
	unlock()
	if err != nil {
		return nil, err
	}
	return done.finish(req, res)
}

// tidyArgs refuses what the arguments alone decide (§3.1 rules 2 and 3).
func (e *Engine) tidyArgs(req TidyRequest) error {
	if req.All && len(req.Select) > 0 {
		names := req
		names.All = false
		all := req
		all.Select = nil
		return errs.WithHints(errors.New("name items or pass --all, not both"),
			tidyHints(e.tidyRun(names, tidyNames(req.Select)), e.tidyRun(all, nil, "--all"))...)
	}
	for _, s := range req.Select {
		if !slices.Contains(req.Keep, s) {
			continue
		}
		without := req
		without.Select = slices.DeleteFunc(slices.Clone(req.Select), func(p config.Pattern) bool { return p == s })
		unkept := req
		unkept.Keep = slices.DeleteFunc(slices.Clone(req.Keep), func(p config.Pattern) bool { return p == s })
		return errs.WithHints(fmt.Errorf("%s is named to move and to keep", untrusted.Text(s.String(), untrusted.MaxName)),
			tidyHints(e.tidyRun(without, tidyNames(without.Select)), e.tidyRun(unkept, tidyNames(req.Select)))...)
	}
	return nil
}

// tidyNames are selectors as a hint writes them: as given.
func tidyNames(ps []config.Pattern) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.String()
	}
	return out
}

// tidyRun is the lola tidy command a hint names (§3.1): the scope's flags, names, extra, then
// the run flags as given (-t, --include-tracked, --keep, --unkeep), then -y or --force and the
// output flags as given. --all goes in extra; never -n unless extra says so. "" when no such command
// works (tidyCommand).
func (e *Engine) tidyRun(req TidyRequest, names []string, extra ...string) string {
	args := append(slices.Clone(names), extra...)
	for _, t := range req.Targets {
		args = append(args, "-t", t)
	}
	if req.IncludeTracked {
		args = append(args, "--include-tracked")
	}
	for _, k := range req.Keep {
		args = append(args, "--keep", k.String())
	}
	for _, k := range req.Unkeep {
		args = append(args, "--unkeep", k.String())
	}
	switch {
	case req.Force:
		args = append(args, "--force")
	case req.Yes:
		args = append(args, "-y")
	}
	args = append(args, req.Display...)
	return e.tidyCommand(req.Scope, "tidy", args...)
}

// tidyCommand is e.Command(sc, sub, args...), or "" when untrusted.Text would alter the root it
// names or any of args: the command would name a path or item that does not exist.
func (e *Engine) tidyCommand(sc Scope, sub string, args ...string) string {
	words := slices.Clone(args)
	if slices.Contains(e.RootArgs(sc), "-C") {
		words = append(words, sc.ProjectDir)
	}
	for _, w := range words {
		if untrusted.Text(w, untrusted.MaxName) != w {
			return ""
		}
	}
	return e.Command(sc, sub, args...)
}

// tidyHints are hs without the "" of a command that does not work: a refusal no hint can fix
// carries none (§3.1).
func tidyHints(hs ...string) []string {
	return slices.DeleteFunc(hs, func(h string) bool { return h == "" })
}

// tidyRerun is the user's run as a hint repeats it: its names or --all, its flags, and -y or
// --force as given.
func (e *Engine) tidyRerun(req TidyRequest) string {
	if req.All {
		return e.tidyRun(req, nil, "--all")
	}
	return e.tidyRun(req, tidyNames(req.Select))
}

// tidyDone is a run that got past every refusal: the keep edits Tidy writes before it releases
// the scope lock, and the outcome finish reports.
type tidyDone struct {
	keep    *tidyKeep
	empty   bool  // the scan found no candidate
	failure error // the first failed move, which stops the run (§6.4)
	keepErr error // writing the keep-list failed
	// keepCmd is the keep-only run that makes this run's keep edits; a keep-only run needs no -y.
	keepCmd string
}

// tidyLocked runs the scan, the selection and the moves; *unlock releases what it locked, which
// Tidy calls after the keep-list is written.
func (e *Engine) tidyLocked(ctx context.Context, req TidyRequest, res *TidyResult, unlock *func()) (*tidyDone, error) {
	sc := req.Scope
	if err := e.tidyLedgerCheck(ctx, sc); err != nil {
		return nil, err
	}
	led, err := e.Ledger(sc)
	if err != nil {
		return nil, err
	}
	keep, err := e.tidyKeepList(req)
	if err != nil {
		return nil, err
	}
	targets, err := e.tidyTargets(sc, led, req, res)
	if err != nil {
		return nil, err
	}
	s, err := e.newTidyScanner(ctx, req, targets, keep)
	if err != nil {
		return nil, err
	}
	ledgers := []tidyLedger{{sc: sc, led: led}}
	overlap := false
	if sc.Kind == target.Project {
		if overlap, err = s.overlapsUser(); err != nil {
			return nil, err
		}
	}
	if overlap {
		user := Scope{Kind: target.User}
		if !req.Dry {
			u, err := e.lock(user)
			if err != nil {
				return nil, err
			}
			prev := *unlock
			*unlock = func() { u(); prev() }
		}
		uled, err := e.Ledger(user)
		if err != nil {
			return nil, err
		}
		ledgers = append(ledgers, tidyLedger{sc: user, led: uled})
	}
	scan := s.scan(ledgers)
	res.Candidates, res.Protected, res.Skipped = scan.Candidates, scan.Protected, scan.Skipped
	res.Notices = append(res.Notices, s.notices...)
	res.Hints = append(res.Hints, tidyHints(s.hints...)...)

	chosen, choice, err := e.tidySelect(req, s, scan, res)
	if err != nil {
		return nil, err
	}
	done := &tidyDone{keep: keep, empty: len(scan.Candidates) == 0}
	if !req.Dry {
		res.Candidates = []TidyUnit{}
	}
	keep.add = append(keep.add, choice.Keep...)
	if chosen == nil {
		return done, nil
	}
	if req.Dry {
		res.Candidates = chosen
		return done, nil
	}
	done.failure = s.apply(chosen, res)
	return done, nil
}

// finish returns the result, or the first failed move and a failed keep-list write with what
// moved before them.
func (d *tidyDone) finish(req TidyRequest, res *TidyResult) (*TidyResult, error) {
	if d.failure != nil || d.keepErr != nil {
		if res.Trash != "" {
			res.Notices = append(res.Notices, Notice{Level: LevelInfo,
				Text: "what moved is in " + untrusted.Text(res.Trash, untrusted.MaxName) + "; its manifest.txt lists each item's original path"})
		}
		if d.keepErr != nil {
			// The write's own hints say to rerun, which now refuses: what moved is gone.
			res.Notices = append(res.Notices, Notice{Level: LevelWarn,
				Text: "nothing was written to the keep-list: close any program that holds it open and check its directory's permissions"})
		}
		// The joined message is rebuilt so the hints below are the error's only ones.
		err := errors.New(errors.Join(d.failure, d.keepErr).Error())
		return nil, &TidyError{Result: res, err: errs.WithHints(err, tidyHints(append(errs.Hints(d.failure), d.keepCmd)...)...)}
	}
	if d.empty && len(req.Select) == 0 {
		res.Notices = append(res.Notices, nothingToTidy(req.Scope, res))
	}
	return res, nil
}

// hasSelection reports whether the run names what moves.
func hasSelection(req TidyRequest) bool { return req.All || len(req.Select) > 0 }

// nothingToTidy is the notice of an empty scan (§9), with the count of what was locked.
func nothingToTidy(sc Scope, res *TidyResult) Notice {
	text := "nothing to tidy at " + tidyWhere(sc)
	if n := len(res.Protected); n > 0 {
		text += fmt.Sprintf(" (%d locked)", n)
	}
	return Notice{Level: LevelInfo, Text: text}
}

// tidyWhere names the scope in messages: "project <root>" or "user scope".
func tidyWhere(sc Scope) string {
	if sc.Kind == target.User {
		return "user scope"
	}
	return "project " + untrusted.Text(sc.ProjectDir, untrusted.MaxName)
}

// tidyLedgerCheck refuses a project whose lock.json git tracks, or about which git cannot say
// (§4.3, R-6d-S5): unlike render, tidy treats "cannot check" as tracked.
func (e *Engine) tidyLedgerCheck(ctx context.Context, sc Scope) error {
	if sc.Kind != target.Project {
		return nil
	}
	tracked, err := ledgerTracked(ctx, sc.ProjectDir)
	if err != nil {
		return fmt.Errorf("lola could not check whether git tracks %s: %s; lola tidy needs to know", projectLedger, gitCause(err))
	}
	if tracked {
		return e.trackedLedger(sc)
	}
	return nil
}

// tidyTargets are the targets a run scans (§4.3): -t when given; else the manifest's targets as
// far as trust covers them, and every target the ledger records.
func (e *Engine) tidyTargets(sc Scope, led *ledger.Ledger, req TidyRequest, res *TidyResult) ([]string, error) {
	var want []string
	if len(req.Targets) > 0 {
		for _, id := range req.Targets {
			if _, err := e.target(id); err != nil {
				return nil, err
			}
			want = append(want, id)
		}
	} else {
		man, err := e.Manifest(sc)
		if ue, ok := errors.AsType[*manifest.UnresolvedError](err); ok {
			man, err = ue.Manifest, nil
		}
		if err != nil {
			return nil, err
		}
		fixed := man.Targets
		if sc.Kind == target.Project && len(fixed) > 0 {
			rec, err := e.trustRecord(sc)
			if err != nil {
				return nil, err
			}
			if !slices.Equal(rec.View.Targets, fixed) {
				fixed = nil
				res.Notices = append(res.Notices, Notice{Level: LevelWarn, Text: "project.yml's targets changed since you trusted them; " +
					e.SyncCommand(sc) + " reviews the change"})
			}
		}
		want = append(want, fixed...)
		for _, ent := range led.Modules {
			want = append(want, ent.InstalledTargets()...)
		}
	}
	var out []string
	for _, t := range e.Targets {
		if slices.Contains(want, t.ID()) {
			out = append(out, t.ID())
		}
	}
	if len(out) == 0 {
		var hints []string
		for _, a := range e.Detect(sc) {
			if a.Binary != "" || a.Marked {
				r := req
				r.Targets = []string{a.Target}
				hints = append(hints, e.tidyRerun(r))
			}
		}
		where := "project " + untrusted.Text(sc.ProjectDir, untrusted.MaxName) + " lists no targets and lola installed nothing there"
		if sc.Kind == target.User {
			where = "user scope lists no targets and lola installed nothing there"
		}
		return nil, errs.WithHints(errors.New(where), tidyHints(hints...)...)
	}
	res.Targets = out
	return out, nil
}

// tidyKeep is the keep-list as this run reads it and the edits it writes (§5).
type tidyKeep struct {
	where   config.Where
	held    []config.Pattern // the section as the file holds it
	entries []config.Pattern // with the run's --keep added and --unkeep removed: what the scan honors
	add     []config.Pattern
	remove  []config.Pattern
}

// tidyKeepList reads keep.yaml, refusing a malformed file and an --unkeep of an entry the
// scope's section does not hold.
func (e *Engine) tidyKeepList(req TidyRequest) (*tidyKeep, error) {
	w, err := where(req.Scope)
	if err != nil {
		return nil, err
	}
	k, err := config.LoadKeep(e.Paths.Keep)
	if err != nil {
		return nil, err
	}
	held := k.Entries(w)
	for _, u := range req.Unkeep {
		if slices.Contains(held, u) {
			continue
		}
		holds := " nothing"
		if len(held) > 0 {
			holds = ": " + untrusted.Text(strings.Join(tidyNames(held), ", "), 4*untrusted.MaxName)
		}
		return nil, fmt.Errorf("%s is not on the keep-list of %s; it holds%s", untrusted.Text(u.String(), untrusted.MaxName),
			tidyWhere(req.Scope), holds)
	}
	entries := slices.DeleteFunc(append(slices.Clone(held), req.Keep...), func(p config.Pattern) bool { return slices.Contains(req.Unkeep, p) })
	return &tidyKeep{where: w, held: held, entries: entries, add: slices.Clone(req.Keep), remove: slices.Clone(req.Unkeep)}, nil
}

// write applies the keep edits under keep.lock, taken after the scope lock, and reports what
// changed.
func (k *tidyKeep) write(e *Engine, sc Scope, res *TidyResult) error {
	var added, removed []string
	for _, p := range k.add {
		if !slices.Contains(k.held, p) && !slices.Contains(added, p.String()) {
			added = append(added, p.String())
		}
	}
	for _, p := range k.remove {
		if slices.Contains(k.held, p) {
			removed = append(removed, p.String())
		}
	}
	if len(added) == 0 && len(removed) == 0 {
		return nil
	}
	unlock, err := config.Lock(e.Paths.KeepLock)
	if err != nil {
		return err
	}
	defer unlock()
	if err := config.SaveKeep(e.Paths.Keep, k.where, k.add, k.remove); err != nil {
		return err
	}
	for _, a := range added {
		res.KeepAdded = append(res.KeepAdded, untrusted.Text(a, untrusted.MaxName))
	}
	for _, r := range removed {
		res.KeepRemoved = append(res.KeepRemoved, untrusted.Text(r, untrusted.MaxName))
	}
	return nil
}
