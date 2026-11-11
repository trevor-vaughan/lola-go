// Package spec is the target.Target lola builds from a target spec: the spec's facts read
// through lola's policy (internal/target/policy) and rendered by its mechanisms
// (internal/target/mech).
package spec

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/target/mech"
	"github.com/trevor-vaughan/lola-go/internal/target/policy"
	"github.com/trevor-vaughan/lola-go/internal/targetspec"
)

// Target renders for one era of a spec.
type Target struct {
	s    targetspec.Spec
	era  targetspec.Era
	caps target.Capabilities
}

// New is s's target for its newest era.
func New(s targetspec.Spec) Target { return At(s, len(s.Eras)-1) }

// At is s's target for its era i (0 is the oldest).
func At(s targetspec.Spec, i int) Target {
	era := s.Eras[i]
	return Target{s: s, era: era, caps: policy.Capabilities(s.ID, era, mech.Rules(s.ID, era.Layout(false)))}
}

// ID implements target.Target.
func (t Target) ID() string { return t.s.ID }

// Root implements target.Target.
func (t Target) Root(scope target.Scope, projectDir string, p paths.Paths) string {
	return t.s.Root(scope == target.User, targetspec.RootVars{Project: projectDir, Home: p.Home, ConfigHome: p.ConfigHome, DataHome: p.DataHome})
}

// Detect implements target.Target.
func (t Target) Detect(scope target.Scope, root string) bool {
	d := t.s.Detect.Project
	if scope == target.User {
		d = t.s.Detect.User
	}
	if d.Dir != "" {
		fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(d.Dir)))
		return err == nil && fi.IsDir()
	}
	for _, m := range d.Any {
		if dir, isDir := strings.CutSuffix(m, "/"); isDir {
			if target.HasAny(root, filepath.FromSlash(dir)) {
				return true
			}
		} else if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(m))); err == nil {
			return true
		}
	}
	return false
}

// Capabilities implements target.Target.
func (t Target) Capabilities() target.Capabilities { return t.caps }

// Probe implements target.Target.
func (t Target) Probe() target.Probe {
	p := target.Probe{Readers: t.s.Readers}
	for _, q := range t.s.Probe {
		if x := q.Exec; x != nil {
			p.Methods = append(p.Methods, target.ProbeMethod{Binaries: x.Binaries, Args: x.Args, Match: x.Match.RE()})
			continue
		}
		f := *q.File
		m := target.ProbeMethod{Pointer: f.Pointer, File: func(p paths.Paths) string {
			return f.Expand(targetspec.RootVars{Home: p.Home, ConfigHome: p.ConfigHome, DataHome: p.DataHome})
		}}
		if f.Match != nil {
			m.Match = f.Match.RE()
		}
		p.Methods = append(p.Methods, m)
	}
	return p
}

// Destinations implements target.Target: the directories of the era's layout, in kind order,
// and the server maps of its json-servers entries.
func (t Target) Destinations(scope target.Scope, root string) (target.Destinations, error) {
	var d target.Destinations
	layout := t.era.Layout(scope == target.User)
	for _, kind := range targetspec.Kinds() {
		e, ok := layout[kind]
		switch {
		case !ok || e.Mech == "claude-hooks":
		case e.Mech == "json-servers":
			file, err := mech.Resolve(t.s.ID, e.File, root)
			if err != nil {
				return target.Destinations{}, err
			}
			d.Servers = append(d.Servers, target.ServerMap{File: file, Pointer: e.Pointer})
		default:
			d.Dirs = append(d.Dirs, target.DestDir{Kind: kind, Path: e.Path, Ext: e.Ext, Shared: e.Shared})
		}
	}
	return d, nil
}

// Plan implements target.Target: each kind, in order, through its mechanism, or reported as
// one the target does not place.
func (t Target) Plan(m *module.Module, ctx target.PlanContext) ([]plan.Intent, []target.Finding, error) {
	layout := t.era.Layout(ctx.Scope == target.User)
	var out []plan.Intent
	var finds []target.Finding
	for _, kind := range targetspec.Kinds() {
		if kind == "rule" && ctx.SkipRules {
			continue
		}
		e, ok := layout[kind]
		if !ok {
			finds = append(finds, t.unplaced(kind, m, ctx)...)
			continue
		}
		ins, f, err := mech.Plan(kind, mech.Env{ID: t.s.ID, Caps: t.caps, Ctx: ctx, Entry: e}, m)
		if err != nil {
			return nil, nil, err
		}
		out, finds = append(out, ins...), append(finds, f...)
	}
	return out, finds, nil
}

// unplaced is the kind-unsupported finding of each of m's artifacts of kind that names t.
func (t Target) unplaced(kind string, m *module.Module, ctx target.PlanContext) []target.Finding {
	var finds []target.Finding
	for _, a := range artifacts(m, kind) {
		if !module.Supports(a.targets, t.s.ID) {
			continue
		}
		_, f, _ := target.Admit(t.s.ID, t.caps, ctx, kind, a.name, module.Doc{})
		for i := range f {
			if f[i].Code == "kind-unsupported" && t.s.KindUnsupportedNote != "" {
				f[i].Text += ": " + t.s.KindUnsupportedNote
			}
		}
		finds = append(finds, f...)
	}
	return finds
}

type artifact struct {
	name    string
	targets []string
}

// artifacts are m's artifacts of kind, by name and the targets they name.
func artifacts(m *module.Module, kind string) []artifact {
	var out []artifact
	switch kind {
	case "skill":
		for _, s := range m.Skills {
			out = append(out, artifact{s.Name, s.Targets})
		}
	case "command":
		for _, c := range m.Commands {
			out = append(out, artifact{c.Name, c.Targets})
		}
	case "agent":
		for _, a := range m.Agents {
			out = append(out, artifact{a.Name, a.Targets})
		}
	case "mcp":
		for _, s := range m.MCPServers {
			out = append(out, artifact{s.Name, s.Targets})
		}
	case "rule":
		for _, r := range m.Rules {
			out = append(out, artifact{r.Name, r.Targets})
		}
	case target.KindRuntimeHook:
		for _, h := range m.RuntimeHooks {
			out = append(out, artifact{h.Name(), h.Targets})
		}
	}
	return out
}
