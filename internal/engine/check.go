package engine

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/target"
)

// CheckedModule is what lola check found for one module.
type CheckedModule struct {
	Name     string           `json:"name"`
	Targets  []string         `json:"targets"`
	Findings []target.Finding `json:"findings"`
}

// CheckReport is the result of lola check: per-module findings, plus findings about the
// installed assistants themselves.
type CheckReport struct {
	Modules  []CheckedModule  `json:"modules"`
	Findings []target.Finding `json:"findings"`
}

// Check reports, for each argument (a folder source, or a module in the cache), everything
// the targets ids (every registered target when empty) would lose, change or skip. It renders
// nothing and changes no state: a folder is staged in a temporary directory, never added to the
// cache.
func (e *Engine) Check(ctx context.Context, sc Scope, cwd string, args, ids []string) (*CheckReport, error) {
	if len(ids) == 0 {
		for _, t := range e.Targets {
			ids = append(ids, t.ID())
		}
	}
	var ts []target.Target
	for _, id := range ids {
		t, err := e.target(id)
		if err != nil {
			return nil, err
		}
		ts = append(ts, t)
	}
	probes := e.newProbeRun(ctx)
	rep := &CheckReport{Modules: []CheckedModule{}, Findings: []target.Finding{}}
	for _, arg := range args {
		cm, err := e.checkOne(ctx, sc, cwd, arg, ts, probes)
		if err != nil {
			return nil, err
		}
		rep.Modules = append(rep.Modules, cm)
	}
	rep.Findings = append(rep.Findings, probes.findings...)
	return rep, nil
}

func (e *Engine) checkOne(ctx context.Context, sc Scope, cwd, arg string, ts []target.Target, probes *probeRun) (cm CheckedModule, err error) {
	m, contentDir, cleanup, err := e.loadForCheck(ctx, cwd, arg)
	if err != nil {
		return cm, err
	}
	defer func() { err = errors.Join(err, cleanup()) }()
	cm = CheckedModule{Name: m.Name, Targets: []string{}, Findings: warningFindings(m)}
	for _, t := range ts {
		if !module.Supports(m.Targets, t.ID()) {
			continue
		}
		pctx := target.PlanContext{Scope: sc.Kind, ContentDir: contentDir, PluginRoot: contentDir, PluginData: contentDir,
			Version: probes.version(t)}
		_, finds, err := t.Plan(m, pctx)
		if err != nil {
			return cm, fmt.Errorf("%s for %s: %w", m.Name, t.ID(), err)
		}
		for i := range finds {
			finds[i].Module = m.Name
		}
		cm.Targets, cm.Findings = append(cm.Targets, t.ID()), append(cm.Findings, finds...)
	}
	return cm, nil
}

// loadForCheck loads arg's module: a folder source is staged the way lola cache add stages
// it, in a temporary directory that cleanup removes; any other argument names a module in
// the cache.
func (e *Engine) loadForCheck(ctx context.Context, cwd, arg string) (*module.Module, string, func() error, error) {
	none := func() error { return nil }
	spec, isSrc, err := source.Parse(arg, cwd)
	if err != nil {
		return nil, "", none, err
	}
	if !isSrc {
		v, ok, err := e.Store.Head(arg, "")
		if err != nil {
			return nil, "", none, err
		}
		if !ok {
			return nil, "", none, errs.WithHints(fmt.Errorf("%q is neither a folder nor a module in your cache", arg),
				"pass a module folder, e.g. lola check ./my-module", "cached modules: lola cache list")
		}
		return v.Module, v.ContentDir, none, nil
	}
	if err := os.MkdirAll(e.Paths.Tmp, 0o700); err != nil {
		return nil, "", none, err
	}
	parent, err := os.MkdirTemp(e.Paths.Tmp, "check-")
	if err != nil {
		return nil, "", none, err
	}
	cleanup := func() error { return os.RemoveAll(parent) }
	staged, err := source.FetchFolder(ctx, spec, parent)
	if err != nil {
		return nil, "", none, errors.Join(err, cleanup())
	}
	m, err := module.Load(staged.Dir, staged.Name)
	if err != nil {
		return nil, "", none, errors.Join(err, cleanup())
	}
	return m, staged.Dir, cleanup, nil
}
