package engine

import (
	"fmt"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Component is one component of a module, as selectors name it.
type Component struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

func (c Component) String() string { return c.Kind + "/" + c.Name }

// components lists a module's components in the order pickers show them: skills, commands,
// agents, MCP servers, rules.
func components(m *module.Module) []Component {
	var out []Component
	for _, s := range m.Skills {
		out = append(out, Component{config.KindSkill, s.Name})
	}
	for _, c := range m.Commands {
		out = append(out, Component{config.KindCommand, c.Name})
	}
	for _, a := range m.Agents {
		out = append(out, Component{config.KindAgent, a.Name})
	}
	for _, s := range m.MCPServers {
		out = append(out, Component{config.KindMCP, s.Name})
	}
	for _, r := range m.Rules {
		out = append(out, Component{config.KindRule, r.Name})
	}
	return out
}

// resolved is a module narrowed to the components a scope installs.
type resolved struct {
	module  *module.Module
	skipped []string // kind/name of every component the exclusions remove
}

// resolveComponents is the one resolver: the cached module minus the effective
// exclusions, and without rules when they are off. install, sync and check all plan what it
// returns, so a component excluded after installation is an orphan the planner removes.
func resolveComponents(m *module.Module, x config.Exclusions, rulesOn bool) resolved {
	out := *m
	r := resolved{module: &out, skipped: []string{}}
	keep := func(kind, name string) bool {
		if x.Excludes(kind, name) {
			r.skipped = append(r.skipped, kind+"/"+name)
			return false
		}
		return true
	}
	out.Skills = slices.DeleteFunc(slices.Clone(m.Skills), func(s module.Skill) bool { return !keep(config.KindSkill, s.Name) })
	out.Commands = slices.DeleteFunc(slices.Clone(m.Commands), func(c module.Command) bool { return !keep(config.KindCommand, c.Name) })
	out.Agents = slices.DeleteFunc(slices.Clone(m.Agents), func(a module.Agent) bool { return !keep(config.KindAgent, a.Name) })
	out.MCPServers = slices.DeleteFunc(slices.Clone(m.MCPServers), func(s module.MCPServer) bool { return !keep(config.KindMCP, s.Name) })
	out.Rules = slices.DeleteFunc(slices.Clone(m.Rules), func(ru module.Rule) bool { return !keep(config.KindRule, ru.Name) })
	if !rulesOn {
		out.Rules = nil
	}
	return r
}

// narrows reports whether a skip or only: selector of x names a component of m: a stale one narrows
// nothing, and an include only widens.
func narrows(x config.Exclusions, m *module.Module) bool {
	all := components(m)
	for _, l := range []config.Layer{x.Global, x.Scope} {
		for _, ps := range [][]config.Pattern{l.Skip, l.Only} {
			for _, p := range ps {
				if slices.ContainsFunc(all, func(c Component) bool { return p.Matches(c.Kind, c.Name) }) {
					return true
				}
			}
		}
	}
	return false
}

// staleNotices warns, once per selector and origin, of each selector in x that names no component
// of m, with the run that removes it: --unskip of it in the section that holds it, which removes a
// stale selector (applyEdits) and, in the global section, needs --global. repick gives the run
// that changes m's picks where it is provided (args: the module, then the flags); it is asked
// only for a selector that is in a scope's section or a profile entry.
func (e *Engine) staleNotices(sc Scope, m *module.Module, x config.Exclusions, repick func(args ...string) (string, error)) ([]Notice, error) {
	all := components(m)
	var out []Notice
	var seen []config.Sourced
	for _, p := range x.Patterns() {
		if slices.ContainsFunc(all, func(c Component) bool { return p.Matches(c.Kind, c.Name) }) || slices.Contains(seen, p) {
			continue
		}
		seen = append(seen, p)
		var run string
		if p.Origin == x.Global.Origin {
			run = e.Command(sc, "install", m.Name, "--global", "--unskip", p.String())
		} else {
			var err error
			if run, err = repick(m.Name, "--unskip", p.String()); err != nil {
				return nil, err
			}
		}
		out = append(out, Notice{Level: LevelWarn, Text: fmt.Sprintf("%s: %s has no component %s → %s",
			untrusted.Text(p.Origin, untrusted.MaxName), m.Name, untrusted.Text(p.String(), untrusted.MaxName), run)})
	}
	return out, nil
}

// selections loads selections.yaml for one command.
func (e *Engine) selections() (*config.Selections, error) {
	return config.LoadSelections(e.Paths.Selections)
}

// where is keep.yaml's section of a scope: user, or the project by its path key.
func where(sc Scope) (config.Where, error) {
	if sc.Kind == target.User {
		return config.User(), nil
	}
	key, err := paths.ProjectKey(sc.ProjectDir)
	if err != nil {
		return config.Where{}, err
	}
	return config.Project(key), nil
}

// scopeSection is the selections.yaml section of a scope: user, or the project by its repository
// key (config.RepoKey), which also matches a section under its path key, else by its path key.
// Every read and write of a project's section goes through it; it
// asks git once per project in a run (Engine.sections). A clone git cannot read is keyed by its
// path, never refused: keyNotices says why, once.
func (e *Engine) scopeSection(sc Scope) (config.Where, error) {
	w, err := where(sc)
	if err != nil || sc.Kind != target.Project {
		return w, err
	}
	key := w.Project
	if known, ok := e.sections[key]; ok {
		return known.w, nil
	}
	known := projectSection{w: w}
	repo, ok, err := config.RepoKey(sc.ProjectDir, source.GitCommand)
	switch {
	case err != nil:
		known.failed = err
	case ok:
		known.w = config.RepoProject(repo, key)
	}
	if e.sections == nil {
		e.sections = map[string]*projectSection{}
	}
	e.sections[key] = &known
	return known.w, nil
}

// projectSection is a project's selections.yaml section as scopeSection resolved it, and failed,
// why git could not read the clone, which keys it by path, until keyFailure takes it.
type projectSection struct {
	w      config.Where
	failed error
}

// keyFailure is why git could not read sc's clone, so its picks are keyed by its path
// (scopeSection), the first time a run asks; nil after, and for any other scope.
func (e *Engine) keyFailure(sc Scope) error {
	if sc.Kind != target.Project {
		return nil
	}
	w, err := where(sc)
	if err != nil {
		return nil
	}
	known := e.sections[w.Project]
	if known == nil {
		return nil
	}
	failed := known.failed
	known.failed = nil
	return failed
}

// keyedByPath is the text that says sc's picks are keyed by its path because git failed (failed,
// keyFailure's), without the hints.
func keyedByPath(failed error) string {
	return untrusted.CleanErr(failed).Error() + "; picks for this project are keyed by its path"
}

// keyNotices is keyFailure as the run's warn notice, its hint after →; nil when there is none.
func (e *Engine) keyNotices(sc Scope) []Notice {
	failed := e.keyFailure(sc)
	if failed == nil {
		return nil
	}
	text := keyedByPath(failed)
	for _, h := range errs.Hints(failed) {
		text += " → " + h
	}
	return []Notice{{Level: LevelWarn, Text: text}}
}

// picksAt is the section of selections whose picks apply to a module that from provides at sc:
// the entry of from when it is a profile layer (profile:<qualified>),
// else selections.yaml's section of sc. from is a layer's or lock.json's name: "" or project.yml for
// the scope's manifest.
func (e *Engine) picksAt(sc Scope, from string) (config.Where, error) {
	if strings.HasPrefix(from, ledger.FromProfile("")) {
		return config.Picks(from), nil
	}
	return e.scopeSection(sc)
}

// exclusions returns module's effective exclusions at sc, provided by from (picksAt). It is the
// one place a planner, the choices and the preview get them (install, list, sync, check,
// moduleScope, Preview), so a layer added to the precedence reaches every caller here.
func (e *Engine) exclusions(sel *config.Selections, sc Scope, from, module string) (config.Exclusions, error) {
	w, err := e.picksAt(sc, from)
	if err != nil {
		return config.Exclusions{}, err
	}
	return sel.Exclusions(w, module), nil
}

// originLabel names origin, the origin of one of x's layers (x: the exclusions of a module that
// from provides), as output names it: a profile entry's as layerLabel names the profile among
// profiles (listed only then), a selections.yaml section's as its origin, through untrusted.Text;
// "" for "".
func originLabel(x config.Exclusions, origin, from string, profiles func() ([]profile.Info, error)) (string, error) {
	if origin == "" {
		return "", nil
	}
	if origin == x.Scope.Origin && strings.HasPrefix(from, ledger.FromProfile("")) {
		infos, err := profiles()
		if err != nil {
			return "", err
		}
		return layerLabel(infos, from), nil
	}
	return untrusted.Text(origin, untrusted.MaxName), nil
}

// ignoredSection is the warning that sel's section of sc names module, which from, a profile
// layer, provides at sc: that profile entry's picks apply instead, so the
// section is never read for it. It names the profile as layerLabel does among profiles (the
// profiles sc can use, listed only for a warning), the file to delete the entry from, and, as where
// module's picks live instead, the edit of the profile's picks (repickRun, with layers and on,
// whether a profile is on at sc once the run applies). It is nil when from is the scope's manifest
// or the section has no picks for module.
func (e *Engine) ignoredSection(sel *config.Selections, sc Scope, layers []Layer, profiles func() ([]profile.Info, error), on bool,
	from, module string) (*Notice, error) {
	if !strings.HasPrefix(from, ledger.FromProfile("")) {
		return nil, nil
	}
	w, err := e.scopeSection(sc)
	if err != nil {
		return nil, err
	}
	if sel.Entry(w, module).Empty() {
		return nil, nil
	}
	infos, err := profiles()
	if err != nil {
		return nil, err
	}
	label := layerLabel(infos, from)
	return &Notice{Level: LevelWarn, Text: fmt.Sprintf("%s %s is ignored: %s provides it; delete it from %s, and pick %s's components in %s → %s",
		untrusted.Text(sel.Origin(w, module), untrusted.MaxName), shown(module), label, untrusted.Text(sel.Path, untrusted.MaxName), shown(module), label,
		e.repickRun(sc, layers, infos, on, from, module))}, nil
}

// skippedIn names where the picks that leave skipped out (kind/name each, by x) live, as output
// says it: "" for none; selections.yaml for a module project.yml provides
// (from, picksAt's, not a profile's); else the profile whose entry holds them, and selections.yaml
// too where its global skip leaves one out. A profile is named as layerLabel names it among
// profiles, the profiles the scope can use (visibleOnce).
func skippedIn(profiles func() ([]profile.Info, error), x config.Exclusions, skipped []string, from string) (string, error) {
	if len(skipped) == 0 {
		return "", nil
	}
	if !strings.HasPrefix(from, ledger.FromProfile("")) {
		return "selections.yaml", nil
	}
	infos, err := profiles()
	if err != nil {
		return "", err
	}
	label := layerLabel(infos, from)
	entry, global := false, false
	for _, c := range skipped {
		kind, name, _ := strings.Cut(c, "/")
		entry = entry || config.Exclusions{Scope: x.Scope}.Excludes(kind, name)
		global = global || config.Exclusions{Global: x.Global}.Excludes(kind, name)
	}
	switch {
	case entry && global:
		return label + " and selections.yaml", nil
	case global:
		return "selections.yaml", nil
	}
	return label, nil
}

// excludedWhere says where the picks that exclude skipped (kind/name each, by x) live, as output
// names it: in, for those a skip leaves out (skippedIn), and for those only the only: list in force
// leaves out, notInOnly and onlyIn, the list's origin as originLabel names it; "" and [] for none.
func excludedWhere(profiles func() ([]profile.Info, error), x config.Exclusions, skipped []string, from string) (in string, notInOnly []string, onlyIn string, err error) {
	notInOnly = []string{}
	var rest []string
	for _, c := range skipped {
		kind, name, _ := strings.Cut(c, "/")
		if x.LeftOutByOnly(kind, name) {
			notInOnly = append(notInOnly, c)
		} else {
			rest = append(rest, c)
		}
	}
	if in, err = skippedIn(profiles, x, rest, from); err != nil || len(notInOnly) == 0 {
		return in, notInOnly, "", err
	}
	_, origin := x.Only()
	onlyIn, err = originLabel(x, origin, from, profiles)
	return in, notInOnly, onlyIn, err
}

// visibleOnce is the profiles sc can use (visible), listed when first asked for and then kept, so
// a run that labels many modules lists them once per scope, and one that labels none never does.
func (e *Engine) visibleOnce(sc Scope) func() ([]profile.Info, error) {
	var infos []profile.Info
	var err error
	listed := false
	return func() ([]profile.Info, error) {
		if !listed {
			infos, _, err = e.visible(sc)
			listed = true
		}
		return infos, err
	}
}

// withPicks is a copy of sel holding the picks of every entry of layers' profiles, each in its
// layer's section (config.Picks), parsed as the profile's load checked them.
func withPicks(sel *config.Selections, layers []Layer) (*config.Selections, error) {
	out := sel.Clone()
	for _, l := range layers {
		if !l.Profile {
			continue
		}
		for _, ent := range l.Entries {
			if len(ent.Only)+len(ent.Skip) == 0 {
				continue
			}
			var picks config.Entry
			for _, x := range []struct {
				in  []string
				out *[]config.Pattern
			}{{ent.Only, &picks.Only}, {ent.Skip, &picks.Skip}} {
				for _, s := range x.in {
					pat, err := config.ParsePattern(s)
					if err != nil {
						return nil, fmt.Errorf("%s: module %s: %w", shown(l.Path), shown(ent.Name), err)
					}
					*x.out = append(*x.out, pat)
				}
			}
			out.Set(config.Picks(l.Name), ent.Name, picks)
		}
	}
	return out, nil
}
