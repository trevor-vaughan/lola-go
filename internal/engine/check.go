package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/market"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/templating"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// CheckedModule is what lola check found for one module.
type CheckedModule struct {
	Name     string           `json:"name"`
	Targets  []string         `json:"targets"`
	Findings []target.Finding `json:"findings"`
	Skipped  []string         `json:"skipped"` // components selections.yaml excludes at the scope render uses; not checked
}

// CheckReport is the result of lola check: per-module findings, plus findings about the
// installed assistants themselves.
type CheckReport struct {
	Modules  []CheckedModule  `json:"modules"`
	Findings []target.Finding `json:"findings"`
}

// Check reports, for each argument (a source, or a module in the cache), everything the
// targets ids (every registered target when empty) would lose, change or skip. It renders
// nothing and changes no state: a source is staged in a temporary directory, never added to
// the cache. Components that selections.yaml excludes at sc are reported as skipped, not
// checked, since render would not install them. A marketplace address is checked as install
// would fetch it (Plan 6c §3.3); display is the run's output flags, which its hints keep.
func (e *Engine) Check(ctx context.Context, sc Scope, cwd string, args, ids, display []string) (*CheckReport, error) {
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
	sel, err := e.selections()
	if err != nil {
		return nil, err
	}
	probes := e.newProbeRun(ctx)
	led, err := ledger.Load(e.files(sc).ledger)
	if err != nil {
		return nil, err
	}
	templates, err := e.newTemplateRun(sc, led.Profiles) // templates render with the scope's active profiles, as render would
	if err != nil {
		return nil, err
	}
	rep := &CheckReport{Modules: []CheckedModule{}, Findings: []target.Finding{}}
	// A module the scope installs from the source checked renders with its winning entry's vars.
	// A manifest or profile that cannot be read leaves every module its defaults, and says so:
	// check is the author's tool, and an unrelated broken file must not stop it.
	var want []Wanted
	man, err := manifest.Load(e.files(sc).manifest)
	var layers []Layer
	if err == nil {
		layers, _, err = e.layers(sc, man, led)
	}
	if err != nil {
		rep.Findings = append(rep.Findings, target.Finding{Severity: target.SeverityInfo, Kind: "module", Code: "vars-defaults",
			Text: untrusted.CleanErr(err).Error() + "; lola check renders every module with its default vars"})
	} else {
		want, _ = desired(layers)
	}
	for _, arg := range args {
		cm, err := e.checkOne(ctx, sc, cwd, arg, sel, ts, probes, templates, want, display)
		if err != nil {
			return nil, err
		}
		rep.Modules = append(rep.Modules, cm)
	}
	rep.Findings = append(rep.Findings, probes.findings...)
	return rep, nil
}

func (e *Engine) checkOne(ctx context.Context, sc Scope, cwd, arg string, sel *config.Selections, ts []target.Target, probes *probeRun,
	templates *templateRun, want []Wanted, display []string) (cm CheckedModule, err error) {
	full, contentDir, cleanup, err := e.loadForCheck(ctx, cwd, arg, display)
	if err != nil {
		return cm, err
	}
	defer func() { err = errors.Join(err, cleanup()) }()
	x, err := exclusions(sel, sc, full.Name)
	if err != nil {
		return cm, err
	}
	r := resolveComponents(full, x, true)
	m := r.module
	cm = CheckedModule{Name: m.Name, Targets: []string{}, Findings: warningFindings(m), Skipped: r.skipped}
	var ment *manifest.Entry
	from := ""
	if w := find(want, m.Name); w != nil && e.checksEntry(sc, cwd, arg, w.Entry) {
		ment, from = &w.Entry, w.From
	}
	// A bad override is the module's skip finding, as a template error is, and it renders with
	// its defaults; a staged source is no cached version, so the refusal carries no hint.
	if err := e.checkVars(sc, m, ment, from, "", display); err != nil {
		cm.Findings = append(cm.Findings, target.Finding{Severity: target.SeveritySkip, Module: m.Name, Kind: "module", Code: "vars-override",
			Text: err.Error()})
		ment = nil
	}
	cm.Findings = append(cm.Findings, attrsUndefined(m)...)
	vars := resolvedVars(m, ment)
	for _, t := range ts {
		if !module.Supports(m.Targets, t.ID()) {
			continue
		}
		pctx := target.PlanContext{Scope: sc.Kind, Root: t.Root(sc.Kind, sc.ProjectDir, e.Paths), ContentDir: contentDir,
			PluginRoot: contentDir, PluginData: contentDir, Version: probes.version(t)}
		if sc.Kind == target.Project && len(m.RuntimeHooks) > 0 {
			pctx.LocalSettingsExposed = probes.localSettings(sc.ProjectDir)
		}
		rendered, refused, err := e.checkTemplates(ctx, m, templates.data(t.ID(), sc, vars), templates)
		if err != nil {
			return cm, err
		}
		for _, te := range refused {
			cm.Findings = append(cm.Findings, te.finding())
		}
		_, finds, err := t.Plan(rendered, pctx)
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

// loadForCheck loads arg's module: a source, or a marketplace address, is staged the way lola
// cache add stages it, in a temporary directory that cleanup removes; any other argument names a
// module in the cache.
func (e *Engine) loadForCheck(ctx context.Context, cwd, arg string, display []string) (*module.Module, string, func() error, error) {
	none := func() error { return nil }
	_, isAddr, err := market.ParseAddress(arg)
	if err != nil {
		return nil, "", none, err
	}
	var spec source.Spec
	isSrc := false
	if !isAddr {
		if spec, isSrc, err = source.Parse(arg, cwd); err != nil {
			return nil, "", none, err
		}
	}
	if !isSrc && !isAddr {
		v, ok, err := e.Store.Head(arg, "")
		if err != nil {
			return nil, "", none, err
		}
		if !ok {
			return nil, "", none, errs.WithHints(fmt.Errorf("%q is neither a source nor a module in your cache; "+
				"pass a module folder or another source, e.g. lola check ./my-module; sources are %s", arg, source.AcceptedForms),
				"cached modules: "+errs.ShellJoin(append([]string{"lola", "cache", "list"}, display...)))
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
	if isAddr {
		m, dir, err := e.checkEntry(ctx, arg, parent, display)
		if err != nil {
			return nil, "", none, errors.Join(err, cleanup())
		}
		return m, dir, cleanup, nil
	}
	staged, err := source.Fetch(ctx, spec, parent)
	if err != nil {
		return nil, "", none, errors.Join(err, cleanup())
	}
	m, err := module.Detect(staged.Dir, staged.Name, targets.IDs())
	if err != nil {
		return nil, "", none, errors.Join(err, cleanup())
	}
	return m, staged.Dir, cleanup, nil
}

// checksEntry reports whether ent installs the source lola check of arg (from cwd) checks: the
// same location and path, the same marketplace address, or, for a module named from the cache,
// the source and path the cache recorded for it. One it cannot tell is not the same.
func (e *Engine) checksEntry(sc Scope, cwd, arg string, ent manifest.Entry) bool {
	want, isSrc, err := source.Parse(ent.Source, sourceRoot(sc))
	if err != nil || !isSrc {
		return false
	}
	if mkt, mod, isAddr := manifest.Address(arg); isAddr {
		return ent.Market != nil && ent.Market.Name == mkt && ent.Name == mod
	}
	if got, isSrc, err := source.Parse(arg, cwd); err == nil && isSrc {
		return got.Location == want.Location && got.Path == ent.Path
	}
	o, found, err := e.Store.Origin(arg)
	return err == nil && found && o.Source == want.Location && o.Path == ent.Path
}

// attrsUndefined warns on each literal attrs path that m's templates and partials read and no
// attribute file of m defines for any target (Plan 7 §5.7.3): attrs reads a missing key as nil,
// so a typo there would flip a condition silently. Findings are sorted by their text.
func attrsUndefined(m *module.Module) []target.Finding {
	var files []map[string]any
	add := func(attrs map[string]map[string]any) {
		for _, forTarget := range attrs {
			files = append(files, forTarget)
		}
	}
	for _, sk := range m.Skills {
		add(sk.Attrs)
	}
	for _, c := range m.Commands {
		add(c.Attrs)
	}
	for _, a := range m.Agents {
		add(a.Attrs)
	}
	seen := map[string]bool{}
	finds := []target.Finding{}
	for _, ref := range templating.AttrRefs(templateSources(m), templating.Partials{Files: m.Partials}) {
		if slices.ContainsFunc(files, func(a map[string]any) bool { return definesPath(a, ref.Path) }) {
			continue
		}
		text := fmt.Sprintf("%s:%d: attrs.%s is defined by no attribute file of %s, for any target; it always reads as nil",
			untrusted.Text(ref.File, untrusted.MaxName), ref.Line, untrusted.Text(strings.Join(ref.Path, "."), untrusted.MaxName), untrusted.Text(m.Name, untrusted.MaxName))
		if !seen[text] {
			seen[text] = true
			finds = append(finds, target.Finding{Severity: target.SeverityWarn, Module: m.Name, Kind: "module", Code: "attrs-undefined", Text: text})
		}
	}
	slices.SortFunc(finds, func(a, b target.Finding) int { return strings.Compare(a.Text, b.Text) })
	return finds
}

// definesPath reports whether attrs, one attribute file's keys for one target, gives path a value
// a template can read: each key present in its map, or a property liquid computes, as a map's or
// a string's size and a list's size, first and last.
func definesPath(attrs map[string]any, path []string) bool {
	var cur any = attrs
	for _, k := range path {
		switch v := cur.(type) {
		case map[string]any:
			next, ok := v[k]
			if !ok {
				return k == "size"
			}
			cur = next
		case []any:
			return k == "size" || k == "first" || k == "last"
		case string:
			return k == "size"
		default:
			return false
		}
	}
	return true
}
