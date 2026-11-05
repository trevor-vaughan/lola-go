package engine

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
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

// droppedTarget is a target an install's -t leaves out of module, installed on it at the scope
// whose root is root.
type droppedTarget struct{ root, module, target string }

// leftCopy is a skill's own copy that a target no longer writes because the agents target
// delivers the skill: dir, relative to the target's root, of module's skill for target, at the
// scope whose root is root.
type leftCopy struct{ root, module, target, dir string }

// leftToAgents reads module's findings at sc for skill-via-agents (planModule's agents pre-pass):
// for each, the skill's directory in each skill destination of the target that gave it. A plan
// that deletes files there deletes the copy that target wrote before agents delivered the skill.
func (e *Engine) leftToAgents(sc Scope, module string, finds []target.Finding) ([]leftCopy, error) {
	var out []leftCopy
	for _, f := range finds {
		if f.Code != target.CodeSkillViaAgents {
			continue
		}
		t, err := e.target(f.Target)
		if err != nil {
			return nil, err
		}
		d, err := t.Destinations(sc.Kind, t.Root(sc.Kind, sc.ProjectDir, e.Paths))
		if err != nil {
			return nil, err
		}
		for _, dir := range d.Dirs {
			if dir.Kind == "skill" {
				out = append(out, leftCopy{root(sc), module, f.Target, dir.Path + "/" + f.Artifact})
			}
		}
	}
	return out, nil
}

// Why a deleted file goes, as a removal names it.
const (
	whyPicks     = "dropped by your pick change"
	whyTargets   = "target dropped by -t"
	whyDropped   = "not in the version this run installs"
	whyViaAgents = "delivered through .agents/skills"
)

// deletedBy lists the files steps delete (not the emptied directories they leave), per module,
// scope root and reason: those of a target in dropped as whyTargets, those under a copy in left as
// whyViaAgents, the rest as other, or not at all when other is "". With removals at two scopes,
// each names its own (Where; home is user scope's).
func deletedBy(steps []plan.Step, dropped []droppedTarget, left []leftCopy, other, home string) []Removal {
	type group struct{ module, root, why string }
	var out []Removal
	at := map[group]int{}
	wheres := map[int]string{}
	for _, st := range steps {
		if st.Action != plan.ActionDelete || st.Kind == plan.KindDirectory {
			continue
		}
		why := other
		switch {
		case slices.Contains(dropped, droppedTarget{st.Root, st.Module, st.Target}):
			why = whyTargets
		case slices.ContainsFunc(left, func(c leftCopy) bool {
			return c.root == st.Root && c.module == st.Module && c.target == st.Target && (st.Path == c.dir || strings.HasPrefix(st.Path, c.dir+"/"))
		}):
			why = whyViaAgents
		case other == "":
			continue
		}
		g := group{st.Module, st.Root, why}
		i, ok := at[g]
		if !ok {
			i = len(out)
			at[g] = i
			out = append(out, Removal{Module: shown(st.Module), Targets: []string{}, Paths: []RemovedPath{}, Why: why, Scope: st.Scope})
		}
		if !slices.Contains(out[i].Targets, st.Target) {
			out[i].Targets = append(out[i].Targets, st.Target)
		}
		out[i].Paths = append(out[i].Paths, RemovedPath{Target: st.Target, Path: shown(st.Path)})
		if st.Scope == target.User.String() {
			wheres[i] = "user scope (" + shown(home) + ")"
		} else {
			wheres[i] = "project " + shown(st.Root)
		}
	}
	// Paths are relative to their scope's root: with two scopes listed, each says which.
	if len(slices.Compact(slices.Sorted(maps.Values(wheres)))) > 1 {
		for i, w := range wheres {
			out[i].Where = w
		}
	}
	return out
}

// PickDeletes is the files res, the (dry) run of spec, deletes because the run changes picks (a
// scope has Edits: every file it deletes), its -t leaves out a target a module is installed on
// (that target's files) or a target leaves a skill it wrote to the agents target (that copy's
// files); none for a run that does none of these. The review lists them before its Apply.
func PickDeletes(spec InstallSpec, res *InstallResult) []Removal {
	other := ""
	if slices.ContainsFunc(spec.Scopes, func(si ScopeInstall) bool { return len(si.Edits) > 0 }) {
		other = whyPicks
	}
	if other == "" && len(res.dropped) == 0 && len(res.viaAgents) == 0 {
		return nil
	}
	return deletedBy(res.Steps, res.dropped, res.viaAgents, other, res.home)
}

// confirmDeletes asks, before anything is written, about the files the install's plan deletes, as
// uninstall does, when the run changes picks (a scope has Edits: a narrowing --skip or --only, a
// ticked-off component), its -t drops a target a module is installed on, or a target now leaves a
// skill it wrote to the agents target (PickDeletes), and the
// user did not already answer yes to the plan
// (spec.DeletesConfirmed). spec.ConfirmRemovals answers (-y answers yes, a terminal asks); --force
// needs no answer; a dry run never asks. Without a way to ask it refuses, hinting the run with
// -y. A plan that deletes nothing asks nothing, and a run that changes no picks leaves its other
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
