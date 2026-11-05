package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/market"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/templating"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// CheckedModule is what lola check found for one module.
type CheckedModule struct {
	Name      string           `json:"name"`
	Targets   []string         `json:"targets"`
	Findings  []target.Finding `json:"findings"`
	Skipped   []string         `json:"skipped"`              // components your picks exclude at the scope sync uses; not checked
	SkippedIn string           `json:"skipped_in,omitempty"` // where the picks that exclude them live (skippedIn)
	NotInOnly []string         `json:"not_in_only"`          // those of Skipped the only: list in force alone leaves out
	OnlyIn    string           `json:"only_in,omitempty"`    // where that only: list lives (excludedWhere)
	Summary   *PicksSummary    `json:"-"`                    // Skipped shown as the picks they leave, when they are that (summarizePicks)
	// Notices is what the picks leave out that the module's components mention, with the install
	// that adds it (mentionNotice), or why its mentions could not be read; [] for none.
	Notices []Notice `json:"notices"`
}

// CheckReport is the result of lola check: per-module findings, plus findings about the
// installed assistants themselves.
type CheckReport struct {
	Modules  []CheckedModule  `json:"modules"`
	Findings []target.Finding `json:"findings"`
	// Read says check read the scope's module entries, to render a module the scope installs with
	// its entry's vars: its layers loaded and list one module or more.
	Read bool `json:"-"`
}

// Check reports, for each argument (a source, or a module in the cache), everything the
// targets ids (every registered target when empty) would lose, change or skip. It renders
// nothing and changes no state: a source is staged in a temporary directory, never added to
// the cache. Components that selections.yaml excludes at sc are reported as skipped, not
// checked, since sync would not install them. A marketplace address is checked as install
// would fetch it; display is the run's output flags, which its hints keep.
func (e *Engine) Check(ctx context.Context, sc Scope, cwd string, args, ids, display []string) (*CheckReport, error) {
	given := ids
	var ts []target.Target
	for _, id := range ids {
		t, err := e.target(id)
		if err != nil {
			return nil, err
		}
		ts = append(ts, t)
	}
	// The profiles lock comes before anything is read, as a sync's does.
	unlock, err := e.readProfiles(true)
	if err != nil {
		return nil, err
	}
	defer unlock()
	sel, err := e.selections()
	if err != nil {
		return nil, err
	}
	probes := e.newProbeRun(ctx)
	led, err := e.Ledger(sc)
	if err != nil {
		return nil, err
	}
	templates, err := e.newTemplateRun(sc, led.Profile) // templates render with the scope's active profile, as sync would
	if err != nil {
		return nil, err
	}
	rep := &CheckReport{Modules: []CheckedModule{}, Findings: []target.Finding{}}
	// A module the scope installs from the source checked renders with its winning entry's vars.
	// A manifest or profile that cannot be read leaves every module its defaults, and says so:
	// check is the author's tool, and an unrelated broken file must not stop it.
	var want []Wanted
	man, err := e.Manifest(sc)
	var layers []Layer
	if err == nil {
		layers, _, err = e.layers(sc, man, led)
	}
	if err != nil {
		rep.Findings = append(rep.Findings, target.Finding{Severity: target.SeverityInfo, Kind: "module", Code: "vars-defaults",
			Text: untrusted.CleanErr(err).Error() + "; lola check renders every module with its default vars", Hint: strings.Join(errs.Hints(err), "; ")})
	} else {
		want, _ = desired(layers)
		rep.Read = len(want) > 0
		if sel, err = withPicks(sel, layers); err != nil {
			return nil, err
		}
	}
	if len(given) == 0 {
		ts = e.checkedTargets(sc, man, layers, want)
	}
	profiles := e.visibleOnce(sc)
	for _, arg := range args {
		cm, err := e.checkOne(ctx, sc, cwd, arg, sel, ts, given, scopeNames(man, layers, agentsTarget), probes, templates, want, profiles, display)
		if err != nil {
			return nil, err
		}
		rep.Modules = append(rep.Modules, cm)
	}
	rep.Findings = append(rep.Findings, probes.findings...)
	if failed := e.keyFailure(sc); failed != nil {
		rep.Findings = append(rep.Findings, target.Finding{Severity: target.SeverityWarn, Kind: "module", Code: "picks-keyed-by-path",
			Text: keyedByPath(failed), Hint: strings.Join(errs.Hints(failed), "; ")})
	}
	return rep, nil
}

// checkedTargets is what lola check covers with no -t: every target with a binary, and a target
// without one (agents) only where it is set up at sc or the scope's targets name it: its manifest's
// (man, nil when it could not be read), a profile layer's own, as fileTargets reads them, or an
// entry's (want).
func (e *Engine) checkedTargets(sc Scope, man *manifest.Manifest, layers []Layer, want []Wanted) []target.Target {
	named := func(id string) bool {
		return scopeNames(man, layers, id) || slices.ContainsFunc(want, func(w Wanted) bool { return slices.Contains(w.Targets, id) })
	}
	var ts []target.Target
	for _, t := range e.Targets {
		if !target.NoBinary(t) || named(t.ID()) || t.Detect(sc.Kind, t.Root(sc.Kind, sc.ProjectDir, e.Paths)) {
			ts = append(ts, t)
		}
	}
	return ts
}

// scopeNames reports whether the scope's own targets name id: its manifest's (man, nil when it
// could not be read) or a profile layer's, as fileTargets reads them.
func scopeNames(man *manifest.Manifest, layers []Layer, id string) bool {
	return man != nil && slices.Contains(man.Targets, id) ||
		slices.ContainsFunc(layers, func(l Layer) bool { return l.read != nil && slices.Contains(l.read.Targets, id) })
}

func (e *Engine) checkOne(ctx context.Context, sc Scope, cwd, arg string, sel *config.Selections, ts []target.Target, given []string, scopeAgents bool, probes *probeRun,
	templates *templateRun, want []Wanted, profiles func() ([]profile.Info, error), display []string) (cm CheckedModule, err error) {
	full, contentDir, cleanup, err := e.loadForCheck(ctx, cwd, arg, display)
	if err != nil {
		return cm, err
	}
	defer func() { err = errors.Join(err, cleanup()) }()
	provider := ""
	if w := find(want, full.Name); w != nil {
		provider = w.From
	}
	x, err := e.exclusions(sel, sc, provider, full.Name)
	if err != nil {
		return cm, err
	}
	r := resolveComponents(full, x, true)
	m := r.module
	cm = CheckedModule{Name: m.Name, Targets: []string{}, Findings: warningFindings(m), Skipped: r.skipped, NotInOnly: []string{}, Notices: []Notice{}}
	nf, err := nativeFindings(contentDir, m, arg, display)
	if err != nil {
		return cm, err
	}
	cm.Findings = append(cm.Findings, nf...)
	if cm.SkippedIn, cm.NotInOnly, cm.OnlyIn, err = excludedWhere(profiles, x, r.skipped, provider); err != nil {
		return cm, err
	}
	cm.Summary = resolvedSummary(x, full, true, r.skipped)
	var ment, entry *manifest.Entry // entry is the one checked, kept when its vars are refused
	from := ""
	if w := find(want, m.Name); w != nil && e.checksEntry(sc, cwd, arg, w.Entry) {
		ment, entry, from = &w.Entry, &w.Entry, w.From
	}
	// A bad override is the module's skip finding, as a template error is, and it renders with
	// its defaults; a staged source is no cached version, so the refusal carries no hint.
	if err := e.checkVars(sc, m, ment, from, "", display); err != nil {
		f := target.Finding{Severity: target.SeveritySkip, Module: m.Name, Kind: "module", Code: "vars-override", Text: err.Error()}
		if vr, ok := errors.AsType[*varsRefusal](err); ok {
			f.Hint = vr.fix
		}
		cm.Findings = append(cm.Findings, f)
		ment = nil
	}
	cm.Findings = append(cm.Findings, attrsUndefined(m)...)
	// What the picks leave out that a component mentions, named as install names it, with the
	// install of arg that adds it at the assistants given (-t).
	if ms, err := scanMentions(store.Version{Name: full.Name, ContentDir: contentDir, Module: full}); err != nil {
		cm.Notices = append(cm.Notices, Notice{Level: LevelWarn, Text: err.Error()})
	} else {
		gaps := mentionGaps(components(full), ms, x, ment.RulesOn())
		cm.Notices = append(cm.Notices, e.mentionNotice(sc, m.Name, []string{arg}, nil, gaps, arg, Options{Targets: given, Display: display})...)
	}
	vars := resolvedVars(m, ment)
	var shared map[string]bool
	// checkTarget is t's findings of the module: its template refusals, then its plan's.
	checkTarget := func(t target.Target) ([]target.Finding, targetPlan, error) {
		pctx := target.PlanContext{Scope: sc.Kind, Root: t.Root(sc.Kind, sc.ProjectDir, e.Paths), ContentDir: contentDir,
			PluginRoot: contentDir, PluginData: contentDir, Version: probes.version(t), SharedSkills: shared}
		if sc.Kind == target.Project && len(m.RuntimeHooks) > 0 {
			pctx.LocalSettingsExposed = probes.localSettings(sc.ProjectDir)
		}
		rendered, refused, err := e.checkTemplates(ctx, m, templates.data(t.ID(), sc, vars), templates)
		if err != nil {
			return nil, targetPlan{}, err
		}
		var out []target.Finding
		for _, te := range refused {
			out = append(out, te.finding())
		}
		_, finds, err := t.Plan(rendered, pctx)
		if err != nil {
			return nil, targetPlan{}, fmt.Errorf("%s for %s: %w", m.Name, t.ID(), err)
		}
		for i := range finds {
			finds[i].Module = m.Name
		}
		return append(out, finds...), targetPlan{rendered: rendered, finds: finds}, nil
	}
	// As install plans it (planModule): the agents target first, where the module's targets
	// include it: the checked entry's own targets:, else -t, else the scope's (scopeAgents), never
	// agents only set up here. Its error is returned at its place in ts, so a check refused is
	// refused for the target it names first.
	var (
		viaAgents []target.Finding
		agentsErr error
		agentsRan bool
	)
	shares := slices.Contains(given, agentsTarget) || len(given) == 0 && scopeAgents
	if entry != nil && len(entry.Targets) > 0 {
		shares = slices.Contains(entry.Targets, agentsTarget) && (len(given) == 0 || slices.Contains(given, agentsTarget))
	}
	i := slices.IndexFunc(ts, func(t target.Target) bool { return t.ID() == agentsTarget })
	if i >= 0 && shares && module.Supports(m.Targets, agentsTarget) {
		var tp targetPlan
		viaAgents, tp, agentsErr = checkTarget(ts[i])
		if agentsErr == nil {
			shared = sharedSkills(tp)
		}
		agentsRan = true
	}
	for _, t := range ts {
		if !module.Supports(m.Targets, t.ID()) {
			continue
		}
		finds := viaAgents
		switch {
		case t.ID() == agentsTarget && agentsRan && agentsErr != nil:
			return cm, agentsErr
		case t.ID() != agentsTarget || !agentsRan:
			var err error
			if finds, _, err = checkTarget(t); err != nil {
				return cm, err
			}
		}
		cm.Targets, cm.Findings = append(cm.Targets, t.ID()), append(cm.Findings, finds...)
	}
	return cm, nil
}

// loadForCheck loads arg's module: a source, or a marketplace address, is staged the way lola
// cache add stages it, in a temporary directory that cleanup removes; any other argument names a
// module in the cache.
func (e *Engine) loadForCheck(ctx context.Context, cwd, arg string, display []string) (_ *module.Module, _ string, _ func() error, err error) {
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
	parent, cleanup, err := scratch(e.Paths.Tmp, checkPrefix)
	if err != nil {
		return nil, "", none, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, cleanup())
		}
	}()
	if isAddr {
		m, dir, err := e.checkEntry(ctx, arg, parent, display)
		if err != nil {
			return nil, "", none, err
		}
		return m, dir, cleanup, nil
	}
	staged, err := source.Fetch(ctx, spec, parent)
	if err != nil {
		return nil, "", none, err
	}
	m, err := module.Detect(staged.Dir, staged.Name, targets.IDs())
	if errors.Is(err, module.ErrNoModule) {
		err = store.NoModuleAt(spec.Location, spec.Path, staged.Dir, err)
	}
	if err != nil {
		return nil, "", none, err
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
	origins, err := e.Store.Origins(arg)
	return err == nil && slices.ContainsFunc(origins, func(o store.Origin) bool { return o.Source == want.Location && o.Path == ent.Path })
}

// attrsUndefined warns on each literal attrs path that m's templates and partials read and no
// attribute file of m defines for any target: attrs reads a missing key as nil,
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
		key, file := untrusted.Text(strings.Join(ref.Path, "."), untrusted.MaxName), untrusted.Text(ref.File, untrusted.MaxName)
		text := fmt.Sprintf("%s:%d: attrs.%s is defined by no attribute file of %s, for any target; it always reads as nil",
			file, ref.Line, key, untrusted.Text(m.Name, untrusted.MaxName))
		if !seen[text] {
			seen[text] = true
			finds = append(finds, target.Finding{Severity: target.SeverityWarn, Module: m.Name, Kind: "module", Code: "attrs-undefined", Text: text,
				Hint: fmt.Sprintf("correct attrs.%s at %s:%d, or define it for an assistant in the .targets.yaml beside the component that reads it", key, file, ref.Line)})
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
