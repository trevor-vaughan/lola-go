package engine

import (
	"fmt"
	"slices"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// RepairRequest is one lola repair: the scope whose lock.json it repairs and the run's output
// flags (--json, --plain, --no-pager as given), which the commands its notice and refusals name
// keep.
type RepairRequest struct {
	Scope   Scope
	Display []string
}

// RepairItem is one entry of lock.json lola repair forgets, or under -n would: Entry as the list
// and every refusal name it (ledger.Stray.String), then its kind (artifact, target or created),
// module ("" for an item of the created list), target, path ("" for a target) and key. Entry,
// Target, Path and Key pass through untrusted.Text; Kind is a constant and Module a validated
// name, so neither does.
type RepairItem struct {
	Entry  string `json:"entry"`
	Kind   string `json:"kind"`
	Module string `json:"module"`
	Target string `json:"target"`
	Path   string `json:"path"`
	Key    string `json:"key"`
}

// RepairResult is what lola repair forgets, or under -n (DryRun) would: the entries, in
// lock.json's order (ledger.Strays), and the modules then left installed on no target, which
// lock.json no longer records. After a run that forgot a path on a target lola knows or a whole
// module, Notices names the sync that puts back any file a module now lacks. File is the
// lock.json, through untrusted.Text.
type RepairResult struct {
	DryRun           bool         `json:"dry_run"`
	File             string       `json:"file"`
	Forgotten        []RepairItem `json:"forgotten"`
	ForgottenModules []string     `json:"forgotten_modules"`
	Notices          []Notice     `json:"notices"`
	req              RepairRequest
	strays           []ledger.Stray // a plan's: what ApplyRepair may forget
}

// PlanRepair plans lola repair and writes nothing: it reads the scope's lock.json keeping what
// lola could not have written (ledger.LoadKeepingStrays), which every other command refuses, and
// lists it, with the modules forgetting it leaves on no target. A lock.json refused for anything
// else (malformed, newer, a source, profile or member lola never writes) keeps that refusal. The
// plan is what -n prints and what ApplyRepair applies.
func (e *Engine) PlanRepair(req RepairRequest) (*RepairResult, error) {
	if err := e.refuseSymlinkedState(req.Scope); err != nil {
		return nil, err
	}
	path := e.files(req.Scope).ledger
	led, err := ledger.LoadKeepingStrays(path)
	if err != nil {
		return nil, err
	}
	strays := led.Strays(e.knows)
	return e.repaired(req, path, strays, led.Forget(strays), true), nil
}

// ApplyRepair forgets what plan lists, under the scope's lock: it reads lock.json again and
// forgets every entry lola could not have written, as long as plan listed each one. A run that
// waited on the lock may find fewer, never more: an entry plan did not list refuses the run,
// which writes nothing. It saves lock.json only when it forgets something, and deletes no file.
func (e *Engine) ApplyRepair(plan *RepairResult) (*RepairResult, error) {
	req := plan.req
	path := e.files(req.Scope).ledger
	if len(plan.strays) == 0 {
		return e.repaired(req, path, nil, nil, false), nil
	}
	if err := e.refuseSymlinkedState(req.Scope); err != nil {
		return nil, err
	}
	unlock, err := e.lock(req.Scope)
	if err != nil {
		return nil, err
	}
	defer unlock()
	led, err := ledger.LoadKeepingStrays(path)
	if err != nil {
		return nil, err
	}
	strays := led.Strays(e.knows)
	for _, s := range strays {
		if !slices.Contains(plan.strays, s) {
			return nil, errs.WithHints(fmt.Errorf("%s changed since lola listed what to forget: %s is new", path, s),
				e.Command(req.Scope, "repair", req.Display...))
		}
	}
	gone := led.Forget(strays)
	if len(strays) > 0 {
		if err := led.Save(path); err != nil {
			return nil, err
		}
	}
	return e.repaired(req, path, strays, gone, false), nil
}

// repaired is the result of a repair of req's scope at path that forgets strays and leaves the
// modules gone on no target. A run (not dry) that forgot a path on a target lola knows, or a
// whole module, names the sync that puts back any file a module now lacks; one that forgot only
// entries on targets lola does not know names none, since nothing there can be put back.
func (e *Engine) repaired(req RepairRequest, path string, strays []ledger.Stray, gone []string, dry bool) *RepairResult {
	res := &RepairResult{DryRun: dry, File: untrusted.Text(path, len(path)), Forgotten: []RepairItem{},
		ForgottenModules: append([]string{}, gone...), Notices: []Notice{}, req: req, strays: strays}
	for _, s := range strays {
		entry := s.String()
		res.Forgotten = append(res.Forgotten, RepairItem{Entry: untrusted.Text(entry, len(entry)), Kind: s.Kind, Module: s.Module,
			Target: untrusted.Text(s.Target, len(s.Target)), Path: untrusted.Text(s.Path, len(s.Path)), Key: untrusted.Text(s.Key, len(s.Key))})
	}
	lacks := len(gone) > 0 || slices.ContainsFunc(strays, func(s ledger.Stray) bool { return s.Kind == ledger.StrayArtifact && !s.Unknown })
	if !dry && lacks {
		res.Notices = append(res.Notices, Notice{Level: LevelInfo,
			Text: "lola sync puts back any file a module now lacks → " + e.SyncCommand(req.Scope, req.Display...)})
	}
	return res
}
