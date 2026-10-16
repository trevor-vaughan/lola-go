package engine

import (
	"fmt"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/module"
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

// SelectionEdit is one change to selections.yaml an install makes before it plans.
type SelectionEdit struct {
	Op       string
	Selector config.Selector
}

func (e SelectionEdit) String() string { return "--" + e.Op + " " + e.Selector.String() }

// AddFlagEdits adds edits given as flags: to the global section with global, else to the
// one scope's section. With more than one scope and no --global it refuses, since one flag
// cannot say which scope it means (Plan 5c §5.2).
func (spec *InstallSpec) AddFlagEdits(edits []SelectionEdit, global bool) error {
	if len(edits) == 0 {
		return nil
	}
	if global {
		spec.GlobalEdits = append(spec.GlobalEdits, edits...)
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

// applyEdits applies edits to section w of sel for the chosen modules, in memory, and returns
// the changes to save and warnings. --only runs first, then --skip, then --unskip. Every
// selector must match a component of some chosen module; a module-qualified selector must name
// a chosen module. scopes are the scope sections an --unskip --global can leave an item
// skipped in.
func applyEdits(sel *config.Selections, w config.Where, edits []SelectionEdit, mods []editTarget, scopes []config.Where) ([]config.Change, []Notice, error) {
	if err := checkEdits(edits, mods); err != nil {
		return nil, nil, err
	}
	var changes []config.Change
	var notes []Notice
	for _, m := range mods {
		g, cur := sel.Entry(config.Global(), m.name), sel.Entry(w, m.name)
		e := config.Entry{Skip: slices.Clone(cur.Skip), Include: slices.Clone(cur.Include)}
		excluded := func(c Component) bool {
			if w.Kind == config.SectionGlobal {
				return config.Exclusions{Skip: e.Skip}.Excludes(c.Kind, c.Name)
			}
			return config.Exclusions{Skip: append(slices.Clone(g.Skip), e.Skip...), Include: e.Include}.Excludes(c.Kind, c.Name)
		}
		var only []Component
		for _, ed := range edits {
			if ed.Op == EditOnly {
				only = append(only, matching(ed.Selector, m)...)
			}
		}
		if len(only) > 0 {
			for _, c := range m.comps {
				lit := config.Pattern{Kind: c.Kind, Name: c.Name}
				if slices.Contains(only, c) {
					unskip(&e, w, lit, []Component{c}, excluded)
				} else {
					skip(&e, lit, []Component{c})
					if !excluded(c) {
						notes = append(notes, Notice{Level: LevelWarn, Text: fmt.Sprintf("%s:%s stays installed: an include in %s overrides --only; edit %s",
							m.name, untrusted.Text(c.String(), untrusted.MaxName), label(w), untrusted.Text(sel.Path, untrusted.MaxName))})
					}
				}
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
							m.name, untrusted.Text(c.String(), untrusted.MaxName), label(w), ed, untrusted.Text(sel.Path, untrusted.MaxName))})
					case op == EditUnskip && excluded(c):
						notes = append(notes, Notice{Level: LevelWarn, Text: fmt.Sprintf("%s:%s stays skipped by a glob in %s; edit %s",
							m.name, untrusted.Text(c.String(), untrusted.MaxName), label(w), untrusted.Text(sel.Path, untrusted.MaxName))})
					case op == EditUnskip && w.Kind == config.SectionGlobal:
						for _, sw := range scopes {
							se := sel.Entry(sw, m.name)
							if (config.Exclusions{Skip: append(slices.Clone(e.Skip), se.Skip...), Include: se.Include}).Excludes(c.Kind, c.Name) {
								notes = append(notes, Notice{Level: LevelWarn, Text: fmt.Sprintf("%s:%s stays skipped in the %s section; --unskip without --global removes it there",
									m.name, untrusted.Text(c.String(), untrusted.MaxName), label(sw))})
							}
						}
					}
				}
			}
		}
		if !slices.Equal(e.Skip, cur.Skip) || !slices.Equal(e.Include, cur.Include) {
			sel.Set(w, m.name, e)
			changes = append(changes, config.Change{Section: w, Module: m.name, Entry: e})
		}
	}
	return changes, notes, nil
}

// label is a selections.yaml section for a notice; a project path is untrusted.
func label(w config.Where) string { return untrusted.Text(w.String(), untrusted.MaxName) }

// skip adds p to the entry's skip list and removes the includes that name what it hits: one
// equal to p, or a literal include of a hit component.
func skip(e *config.Entry, p config.Pattern, hit []Component) {
	if !slices.Contains(e.Skip, p) {
		e.Skip = append(e.Skip, p)
	}
	e.Include = slices.DeleteFunc(e.Include, func(i config.Pattern) bool { return i == p || names(i, hit) })
}

// unskip removes the skips that name what p hits (one equal to p, or a literal skip of a hit
// component); where a hit component stays excluded (a global skip, or a glob) and w is a
// scope's section, it adds p as an include, which overrides them.
func unskip(e *config.Entry, w config.Where, p config.Pattern, hit []Component, excluded func(Component) bool) {
	e.Skip = slices.DeleteFunc(e.Skip, func(s config.Pattern) bool { return s == p || names(s, hit) })
	if w.Kind == config.SectionGlobal || !slices.ContainsFunc(hit, excluded) {
		return
	}
	if !slices.Contains(e.Include, p) {
		e.Include = append(e.Include, p)
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
// next render converges on the saved selections.
func errApplied(err error, path string) error {
	return errs.WithHints(err, "your selections were saved to "+untrusted.Text(path, untrusted.MaxName)+"; lola render applies them once the problem is fixed")
}
