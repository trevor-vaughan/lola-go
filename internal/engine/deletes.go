package engine

import (
	"fmt"
	"slices"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/plan"
)

// DeletesRefusedError is an install, or lola profile --edit, whose plan deletes files, refused
// because it cannot ask (spec 2026-11-03 Pick-P2-b): there is no terminal and neither -y nor
// --force answered. Removals is what the plan deletes, for the CLI to print; Plan is the dry
// run's result, the plan a --json refusal carries.
type DeletesRefusedError struct {
	Removals []Removal
	Plan     *InstallResult
	verb     string
}

func (e *DeletesRefusedError) Error() string {
	return fmt.Sprintf("lola %s needs your confirmation to remove files, and there is no terminal to ask on; nothing was written", e.verb)
}

// deletedBy lists the files steps delete (not the emptied directories they leave), per module and
// scope root: what an install that narrows picks removes.
func deletedBy(steps []plan.Step) []Removal {
	type group struct{ module, root string }
	var out []Removal
	at := map[group]int{}
	for _, st := range steps {
		if st.Action != plan.ActionDelete || st.Kind == plan.KindDirectory {
			continue
		}
		g := group{st.Module, st.Root}
		i, ok := at[g]
		if !ok {
			i = len(out)
			at[g] = i
			out = append(out, Removal{Module: shown(st.Module), Targets: []string{}, Paths: []RemovedPath{}, Why: "dropped by your pick change"})
		}
		if !slices.Contains(out[i].Targets, st.Target) {
			out[i].Targets = append(out[i].Targets, st.Target)
		}
		out[i].Paths = append(out[i].Paths, RemovedPath{Target: st.Target, Path: shown(st.Path)})
	}
	return out
}

// PickDeletes is the files res, the (dry) run of spec, deletes because the run changes picks (a
// scope has Edits); none for a run that changes no picks. The review lists them before its Apply.
func PickDeletes(spec InstallSpec, res *InstallResult) []Removal {
	if !slices.ContainsFunc(spec.Scopes, func(si ScopeInstall) bool { return len(si.Edits) > 0 }) {
		return nil
	}
	return deletedBy(res.Steps)
}

// confirmDeletes asks, before anything is written, about the files the install's plan deletes, as
// uninstall does, when the run changes picks (a scope has Edits: a narrowing --skip or --only, a
// ticked-off component) and the user did not already answer yes to the plan
// (spec.DeletesConfirmed). spec.ConfirmRemovals answers (-y answers yes, a terminal asks); --force
// needs no answer; a dry run never asks. Without a way to ask it refuses, hinting the run with
// -y. A plan that deletes nothing asks nothing, and a run that changes no picks leaves its
// deletions (a rule dropped, a declined hook) to the questions that own them.
func (e *Engine) confirmDeletes(spec InstallSpec, out *InstallResult) error {
	opts := spec.Options
	if opts.DryRun || opts.Force || spec.DeletesConfirmed {
		return nil
	}
	rs := PickDeletes(spec, out)
	if len(rs) == 0 {
		return nil
	}
	if spec.ConfirmRemovals == nil {
		err := &DeletesRefusedError{Removals: rs, Plan: out, verb: opts.Target.verb()}
		if t := opts.Target; t.Rerun != nil {
			return errs.WithHints(err, t.Rerun(append(t.ownFlags(), "-y")...))
		}
		return err
	}
	yes, err := spec.ConfirmRemovals(slices.Clone(rs))
	switch {
	case err != nil:
		return err
	case !yes:
		return ErrDeclined
	}
	return nil
}
