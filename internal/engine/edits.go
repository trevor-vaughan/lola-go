package engine

import (
	"fmt"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Selection edit operations: the --skip, --only and --unskip flags, and the component picker's
// answers.
const (
	EditSkip   = "skip"
	EditOnly   = "only"
	EditUnskip = "unskip"
)

// SelectionEdit is one change to selections.yaml an install makes before it plans. Exact, on an
// EditOnly edit (--only --exact), saves the selector's pattern as the section's only: list
// instead of skips of the components that exist now (cherry-pick design §3.2). Flag marks an
// edit given as a flag (AddFlagEdits sets it), not a pick on the component page: a --skip flag
// keeps out what --with-mentions would add, an untick on the page does not.
type SelectionEdit struct {
	Op       string
	Selector config.Selector
	Exact    bool
	Flag     bool
}

func (e SelectionEdit) String() string { return "--" + e.Op + " " + e.Selector.String() }

// AddFlagEdits adds edits given as flags: to the global section with global, else to the
// one scope's section. With more than one scope and no --global it refuses, since one flag
// cannot say which scope it means (Plan 5c §5.2), except for lola profile <name> --edit, whose
// scopes all write the one profile it names (ruling P1): each scope gets them.
func (spec *InstallSpec) AddFlagEdits(edits []SelectionEdit, global bool) error {
	if len(edits) == 0 {
		return nil
	}
	edits = slices.Clone(edits)
	for i := range edits {
		edits[i].Flag = true
	}
	if global {
		spec.GlobalEdits = append(spec.GlobalEdits, edits...)
		return nil
	}
	if spec.Options.Target.Edit {
		for i := range spec.Scopes {
			spec.Scopes[i].Edits = append(spec.Scopes[i].Edits, edits...)
		}
		return nil
	}
	if len(spec.Scopes) != 1 {
		var each []string
		for _, si := range spec.Scopes {
			if si.Scope.Kind == target.User {
				each = append(each, errs.ShellJoin([]string{"--scope", "user"}))
			} else {
				each = append(each, errs.ShellJoin([]string{"--scope", "project", "-C", untrusted.Text(si.Scope.ProjectDir, untrusted.MaxName)}))
			}
		}
		return errs.WithHints(fmt.Errorf("%s with more than one scope does not say which scope's selections it changes", edits[0]),
			"add --global to change the selections of every scope", "or install one destination at a time: "+strings.Join(each, ", then "))
	}
	spec.Scopes[0].Edits = append(spec.Scopes[0].Edits, edits...)
	return nil
}

// editTarget is one module an edit can apply to, with its components.
type editTarget struct {
	name  string
	comps []Component
}

// applyEdits applies edits to each chosen module's section of sel, at (the module's name) names
// it, in memory, and returns the changes to save and warnings; a warning names a section and the
// file to edit as pl does. An exact --only replaces the section's only: list with its patterns,
// removes the skips naming a match and leaves other skips and includes; with --global it warns
// about each scope section whose own only: list shadows the new one. --only runs first, then
// --skip, then --unskip. Every selector must match a component of some chosen module; a
// module-qualified selector must name a chosen module. scopes name each scope's section of a
// module, those an --unskip --global can leave an item skipped in.
func applyEdits(sel *config.Selections, at func(module string) config.Where, pl places, edits []SelectionEdit, mods []editTarget,
	scopes []func(module string) config.Where) ([]config.Change, []Notice, error) {
	if err := checkEdits(edits, mods); err != nil {
		return nil, nil, err
	}
	var changes []config.Change
	var notes []Notice
	for _, m := range mods {
		w := at(m.name)
		g, cur := sel.Entry(config.Global(), m.name), sel.Entry(w, m.name)
		e := config.Entry{Skip: slices.Clone(cur.Skip), Include: slices.Clone(cur.Include), Only: slices.Clone(cur.Only)}
		// excluded is the module's exclusions at w as edited so far: the global section alone when
		// w is global, else the global entry under w's as the section takes it (config.Layered).
		excluded := func(c Component) bool {
			x := config.Exclusions{Global: config.Layer{Origin: config.Global().Origin(), Entry: e}}
			if w.Kind != config.SectionGlobal {
				x = config.Layered(w, g, e)
			}
			return x.Excludes(c.Kind, c.Name)
		}
		var only []Component
		var exact []config.Pattern
		for _, ed := range edits {
			if ed.Op != EditOnly {
				continue
			}
			hit := matching(ed.Selector, m)
			only = append(only, hit...)
			if ed.Exact && len(hit) > 0 && !slices.Contains(exact, ed.Selector.Pattern) {
				exact = append(exact, ed.Selector.Pattern)
			}
		}
		staysWarn := func(c Component) Notice {
			return Notice{Level: LevelWarn, Text: fmt.Sprintf("%s:%s stays installed: an include in %s overrides --only; edit %s",
				m.name, untrusted.Text(c.String(), untrusted.MaxName), pl.name(w), pl.file(w))}
		}
		// staysSkipped names the skip that still excludes c after an edit of w, where it is and the
		// file that removes it. The skip is in w's entry or, for a scope, the global one.
		staysSkipped := func(c Component) Notice {
			by, in := config.Pattern{}, w
			if k := slices.IndexFunc(e.Skip, func(p config.Pattern) bool { return p.Matches(c.Kind, c.Name) }); k >= 0 {
				by = e.Skip[k]
			} else if k := slices.IndexFunc(g.Skip, func(p config.Pattern) bool { return p.Matches(c.Kind, c.Name) }); k >= 0 {
				by, in = g.Skip[k], config.Global()
			}
			return Notice{Level: LevelWarn, Text: fmt.Sprintf("%s:%s stays skipped by %s in %s; edit %s", m.name,
				untrusted.Text(c.String(), untrusted.MaxName), untrusted.Text(by.String(), untrusted.MaxName), pl.name(in), pl.file(in))}
		}
		switch {
		case len(exact) > 0:
			e.Only = exact
			for _, p := range exact {
				unskip(&e, w, p, matching(config.Selector{Pattern: p}, m), excluded)
			}
			for _, c := range m.comps {
				if !slices.Contains(only, c) && !excluded(c) {
					notes = append(notes, staysWarn(c))
				}
			}
			if w.Kind == config.SectionGlobal {
				for _, scope := range scopes {
					if sw := scope(m.name); len(sel.Entry(sw, m.name).Only) > 0 {
						notes = append(notes, Notice{Level: LevelWarn, Text: fmt.Sprintf("%s: only: in %s shadows the global only: list there; edit %s",
							m.name, untrusted.Text(sw.Origin(), untrusted.MaxName), pl.file(sw))})
					}
				}
			}
		case len(only) > 0:
			for _, c := range m.comps {
				lit := config.Pattern{Kind: c.Kind, Name: c.Name}
				if slices.Contains(only, c) {
					unskip(&e, w, lit, []Component{c}, excluded)
				} else {
					skip(&e, lit, []Component{c})
					if !excluded(c) {
						notes = append(notes, staysWarn(c))
					}
				}
			}
		}
		for _, c := range only {
			if excluded(c) {
				notes = append(notes, staysSkipped(c))
			}
		}
		for _, op := range []string{EditSkip, EditUnskip} {
			for _, ed := range edits {
				hit := matching(ed.Selector, m)
				if ed.Op != op || len(hit) == 0 {
					continue
				}
				if op == EditSkip {
					skip(&e, ed.Selector.Pattern, hit)
				} else {
					unskip(&e, w, ed.Selector.Pattern, hit, excluded)
				}
				for _, c := range hit {
					switch {
					case op == EditSkip && !excluded(c):
						notes = append(notes, Notice{Level: LevelWarn, Text: fmt.Sprintf("%s:%s stays installed: an include in %s overrides %s; edit %s",
							m.name, untrusted.Text(c.String(), untrusted.MaxName), pl.name(w), ed, pl.file(w))})
					case op == EditUnskip && excluded(c):
						notes = append(notes, staysSkipped(c))
					case op == EditUnskip && w.Kind == config.SectionGlobal:
						for _, scope := range scopes {
							sw := scope(m.name)
							if config.Layered(sw, e, sel.Entry(sw, m.name)).Excludes(c.Kind, c.Name) {
								notes = append(notes, Notice{Level: LevelWarn, Text: fmt.Sprintf("%s:%s stays skipped in the %s section; --unskip without --global removes it there",
									m.name, untrusted.Text(c.String(), untrusted.MaxName), pl.name(sw))})
							}
						}
					}
				}
			}
		}
		if !slices.Equal(e.Skip, cur.Skip) || !slices.Equal(e.Include, cur.Include) || !slices.Equal(e.Only, cur.Only) {
			sel.Set(w, m.name, e)
			changes = append(changes, config.Change{Section: w, Module: m.name, Entry: e})
		}
	}
	return changes, notes, nil
}

// scopeEdits is what editScope did to one scope's section: the changes to save, applyEdits'
// warnings, what --with-mentions said per module (the plan line of each component it added, each
// mention a --skip flag kept out, or that nothing the picks mention is left out), and per module of
// its mods what the picks mention and leave out, but for what a --skip flag kept out.
type scopeEdits struct {
	changes []config.Change
	notes   []Notice
	added   []Notice
	gaps    map[string][]mentionGap
}

// editScope applies edits to each module's section of sel at sc, at names it, (applyEdits) for
// targets; pl names a section and its file in a notice. With withMentions it first adds an --unskip of each component the picks mention and leave
// out (mentionEdits; one hop: what they mention is not followed), computed over a copy of sel with
// the edits applied, so the picks of a component page and of the flags count alike; a component
// a --skip flag of edits or of global (the run's --global edits) matches stays out. Then it
// computes over sel as edited, for each module of mods, what the picks mention and still leave out
// (mentionGaps) but a --skip flag did not keep out (unheld), and says of a module --with-mentions
// added nothing to whose picks leave no mention out that nothing is left out. A module whose
// mentions could not be read (scanErr) gets neither. It is the one place install and the review's
// preview edit a scope.
func editScope(sel *config.Selections, at func(module string) config.Where, pl places, edits, global []SelectionEdit,
	targets []editTarget, mods map[string]mentionModule, withMentions bool) (scopeEdits, error) {
	var se scopeEdits
	none := map[string]bool{}
	var held []SelectionEdit
	for _, ed := range slices.Concat(global, edits) {
		if ed.Flag && ed.Op == EditSkip {
			held = append(held, ed)
		}
	}
	if withMentions && len(mods) > 0 {
		trial := sel.Clone()
		if _, _, err := applyEdits(trial, at, pl, edits, targets, nil); err != nil {
			return scopeEdits{}, err
		}
		edits = slices.Clone(edits)
		for _, t := range targets {
			mm, ok := mods[t.name]
			if !ok || mm.scanErr != nil {
				continue
			}
			add, lines := mentionEdits(t, mentionGaps(t.comps, mm.mentions, trial.Exclusions(at(t.name), t.name), mm.rulesOn), held)
			edits, se.added = append(edits, add...), append(se.added, lines...)
			none[t.name] = len(add) == 0
		}
	}
	changes, notes, err := applyEdits(sel, at, pl, edits, targets, nil)
	if err != nil {
		return scopeEdits{}, err
	}
	se.changes, se.notes, se.gaps = changes, notes, map[string][]mentionGap{}
	for _, t := range targets {
		mm, ok := mods[t.name]
		if !ok {
			continue
		}
		gaps := mentionGaps(t.comps, mm.mentions, sel.Exclusions(at(t.name), t.name), mm.rulesOn)
		if none[t.name] && len(gaps) == 0 {
			se.added = append(se.added, Notice{Level: LevelInfo, Text: untrusted.Text(t.name, untrusted.MaxName) + ": nothing the picked components mention is left out"})
		}
		se.gaps[t.name] = unheld(t, gaps, held)
	}
	return se, nil
}

// unheld is gaps without the components a --skip flag of held matches: the user just kept those
// out, so no hint asks to add them. A gap left with nothing is dropped.
func unheld(t editTarget, gaps []mentionGap, held []SelectionEdit) []mentionGap {
	var out []mentionGap
	for _, g := range gaps {
		g.left = slices.DeleteFunc(slices.Clone(g.left), func(c Component) bool { return heldBy(held, t, c) >= 0 })
		if len(g.left) > 0 {
			out = append(out, g)
		}
	}
	return out
}

// heldBy is the index of the first --skip flag of held that matches c of module t, or -1.
func heldBy(held []SelectionEdit, t editTarget, c Component) int {
	return slices.IndexFunc(held, func(ed SelectionEdit) bool { return slices.Contains(matching(ed.Selector, t), c) })
}

// mentionEdits is, for module t, an --unskip of each component gaps leave out, in t's order, and
// the plan line of each: "<module>: + kind/name (mentioned by kind/name, …)". A component a
// --skip flag of held matches is not added; its line says the flag keeps it out.
func mentionEdits(t editTarget, gaps []mentionGap, held []SelectionEdit) ([]SelectionEdit, []Notice) {
	var edits []SelectionEdit
	var lines []Notice
	for _, c := range t.comps {
		var by []Component
		for _, g := range gaps {
			if slices.Contains(g.left, c) {
				by = append(by, g.by)
			}
		}
		if len(by) == 0 {
			continue
		}
		mod, comp := untrusted.Text(t.name, untrusted.MaxName), untrusted.Text(c.String(), untrusted.MaxName)
		if i := heldBy(held, t, c); i >= 0 {
			lines = append(lines, Notice{Level: LevelInfo, Text: fmt.Sprintf("%s: %s is mentioned by %s but kept out by %s", mod, comp, namedList(by), held[i])})
			continue
		}
		edits = append(edits, SelectionEdit{Op: EditUnskip, Selector: config.Selector{Module: t.name, Pattern: config.Pattern{Kind: c.Kind, Name: c.Name}}})
		lines = append(lines, Notice{Level: LevelInfo, Text: fmt.Sprintf("%s: + %s (mentioned by %s)", mod, comp, namedList(by))})
	}
	return edits, lines
}

// places names where each section's picks are, for a notice, made safe to print: path is
// selections.yaml's, layers the layers whose profile entries hold picks, and infos the profiles
// their scopes can use, for layerLabel.
type places struct {
	path   string
	layers []Layer
	infos  []profile.Info
}

// name is w: selections.yaml's section, or the profile whose entry holds the picks (layerLabel).
func (pl places) name(w config.Where) string {
	if w.Kind == config.SectionProfile {
		return layerLabel(pl.infos, w.Profile)
	}
	return untrusted.Text(w.String(), untrusted.MaxName)
}

// file is the file that holds w's picks: the profile's of a profile entry's, else selections.yaml.
func (pl places) file(w config.Where) string {
	file := pl.path
	if k := slices.IndexFunc(pl.layers, func(l Layer) bool { return l.Name == w.Profile }); w.Kind == config.SectionProfile && k >= 0 {
		file = pl.layers[k].Path
	}
	return untrusted.Text(file, untrusted.MaxName)
}

// skip adds p to the entry's skip list and removes the includes that name what it hits: one
// equal to p, or a literal include of a hit component.
func skip(e *config.Entry, p config.Pattern, hit []Component) {
	if !slices.Contains(e.Skip, p) {
		e.Skip = append(e.Skip, p)
	}
	e.Include = slices.DeleteFunc(e.Include, func(i config.Pattern) bool { return i == p || names(i, hit) })
}

// unskip removes the skips that name what p hits (one equal to p, or a literal skip of a hit
// component). Where a hit component stays excluded (a global skip, a glob, or an only: list that
// misses it), a scope's section gets p as an include, which overrides them; the global section and
// a profile entry, which take no include (cherry-pick design §4.1: an entry's picks are only: and
// skip:), get each hit component's kind/name added to their only: list when they have one and the
// list misses it (§3.2). A section without an only: list never gets one here.
func unskip(e *config.Entry, w config.Where, p config.Pattern, hit []Component, excluded func(Component) bool) {
	e.Skip = slices.DeleteFunc(e.Skip, func(s config.Pattern) bool { return s == p || names(s, hit) })
	if w.Kind != config.SectionGlobal && w.Kind != config.SectionProfile {
		if slices.ContainsFunc(hit, excluded) && !slices.Contains(e.Include, p) {
			e.Include = append(e.Include, p)
		}
		return
	}
	if len(e.Only) == 0 {
		return
	}
	for _, c := range hit {
		if !slices.ContainsFunc(e.Only, func(o config.Pattern) bool { return o.Matches(c.Kind, c.Name) }) {
			e.Only = append(e.Only, config.Pattern{Kind: c.Kind, Name: c.Name})
		}
	}
}

// names reports whether p is a literal pattern naming one of cs.
func names(p config.Pattern, cs []Component) bool {
	return p.Literal() && slices.ContainsFunc(cs, func(c Component) bool { return p.Matches(c.Kind, c.Name) })
}

// matching returns the components of m that s selects.
func matching(s config.Selector, m editTarget) []Component {
	if s.Module != "" && s.Module != m.name {
		return nil
	}
	var out []Component
	for _, c := range m.comps {
		if s.Matches(c.Kind, c.Name) {
			out = append(out, c)
		}
	}
	return out
}

// checkEdits refuses a selector that names a module not being installed, or that matches no
// component of any chosen module; the hint lists what it could have named.
func checkEdits(edits []SelectionEdit, mods []editTarget) error {
	var chosen []string
	for _, m := range mods {
		chosen = append(chosen, m.name)
	}
	for _, ed := range edits {
		if ed.Selector.Module != "" && !slices.Contains(chosen, ed.Selector.Module) {
			return errs.WithHints(fmt.Errorf("%s: module %s is not part of this install", ed, ed.Selector.Module),
				"modules in this install: "+strings.Join(chosen, ", "))
		}
		if slices.ContainsFunc(mods, func(m editTarget) bool { return len(matching(ed.Selector, m)) > 0 }) {
			continue
		}
		var have []string
		for _, m := range mods {
			if ed.Selector.Module != "" && ed.Selector.Module != m.name {
				continue
			}
			for _, c := range m.comps {
				if c.Kind == ed.Selector.Kind {
					have = append(have, m.name+":"+untrusted.Text(c.String(), untrusted.MaxName))
				}
			}
		}
		hint := "no chosen module has a " + ed.Selector.Kind
		if len(have) > 0 {
			hint = ed.Selector.Kind + "s here: " + strings.Join(have, ", ")
		}
		return errs.WithHints(fmt.Errorf("%s matches no component of the modules being installed", ed), hint)
	}
	return nil
}

// editTargets lists the chosen modules' components for edits.
func editTargets(mods []*module.Module) []editTarget {
	var out []editTarget
	for _, m := range mods {
		if !slices.ContainsFunc(out, func(t editTarget) bool { return t.name == m.Name }) {
			out = append(out, editTarget{name: m.Name, comps: components(m)})
		}
	}
	return out
}

// errApplied adds to a failure during apply, after selections.yaml was written, that the
// next sync of sc converges on the saved selections.
func (e *Engine) errApplied(err error, path string, sc Scope) error {
	return errs.WithHints(err, "your selections were saved to "+untrusted.Text(path, untrusted.MaxName)+"; "+e.SyncCommand(sc)+
		" applies them once the problem is fixed")
}
