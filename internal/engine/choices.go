package engine

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// maxLabel caps a description shown in a picker; pickers cut it again to the terminal width.
const maxLabel = 200

// ChoicesRequest asks for what the install pickers offer at scopes. Modules are the module
// arguments: each is resolved, and a source is fetched into the cache, so its components can
// be picked.
type ChoicesRequest struct {
	Cwd     string
	Scopes  []ScopeChoice
	Modules []InstallRequest
}

// Choices is everything the install pickers need (Plan 5c §7), computed by the engine. Every
// author-chosen string in it has gone through untrusted.Text.
type Choices struct {
	Scopes    []ScopeChoices `json:"scopes"`
	Modules   []ModuleChoice `json:"modules"`   // every cached module, by source then name
	Requested []string       `json:"requested"` // the module arguments, resolved to names
	// Versions are the cached versions the module arguments resolved to, in argument order;
	// InstallRequest.Version pins an install to them.
	Versions []VersionRef `json:"versions"`
	Warnings []string     `json:"warnings"` // cached modules that could not be loaded

	sel *config.Selections // as read, for Preview
}

// ScopeChoices is what one scope offers: its assistants and the targets its manifest fixes.
type ScopeChoices struct {
	ScopeChoice
	Name         string      `json:"scope"` // project or user; Scope.Key() tells two project roots apart
	FixedTargets []string    `json:"fixed_targets"`
	Assistants   []Assistant `json:"assistants"`
}

// ModuleChoice is one cached module, with its components and its state at each scope.
type ModuleChoice struct {
	Name         string            `json:"name"`
	Source       string            `json:"source"`
	Type         string            `json:"type"`
	Format       string            `json:"format"`
	Description  string            `json:"description"`
	Digest       string            `json:"digest"`
	ID           string            `json:"id"` // the cached version listed; InstallRequest.Version pins an install to it
	Components   []ComponentChoice `json:"components"`
	RuntimeHooks int               `json:"runtime_hooks"` // matcher groups the consent prompt will ask about
	PerScope     []ModuleScope     `json:"per_scope"`     // one per destination, in scope order
}

// ComponentChoice is one component with what its picker label shows.
type ComponentChoice struct {
	Component
	Description string `json:"description"` // the first line of its description
	RunsCommand bool   `json:"runs_command"`
}

// ModuleScope is a module's state at one destination.
type ModuleScope struct {
	Scope     string   `json:"scope"` // the destination: Scope.Key()
	Installed bool     `json:"installed"`
	Digest    string   `json:"digest"`  // the installed version, "" when not installed
	Targets   []string `json:"targets"` // the assistants it is installed for
	RulesOff  bool     `json:"rules_off"`
	Excluded  []string `json:"excluded"`           // kind/name that selections.yaml excludes here
	Current   []string `json:"current_components"` // kind/name installed now
}

// Scope returns m's state at the destination whose Scope.Key() is key.
func (m ModuleChoice) Scope(key string) ModuleScope {
	for _, s := range m.PerScope {
		if s.Scope == key {
			return s
		}
	}
	return ModuleScope{Scope: key}
}

// Module returns the choice for name, or nil.
func (c *Choices) Module(name string) *ModuleChoice {
	for i := range c.Modules {
		if c.Modules[i].Name == name {
			return &c.Modules[i]
		}
	}
	return nil
}

// InstallChoices computes what the install pickers offer: the scopes with their assistants and
// fixed targets, every cached module (and the module arguments, resolved) with its components,
// and per scope whether it is installed, which components selections.yaml excludes and which
// are installed now. It writes nothing but a source argument's fetch into the cache.
func (e *Engine) InstallChoices(ctx context.Context, req ChoicesRequest) (*Choices, error) {
	sel, err := e.selections()
	if err != nil {
		return nil, err
	}
	ch := &Choices{Scopes: []ScopeChoices{}, Modules: []ModuleChoice{}, Requested: []string{}, Versions: []VersionRef{}, Warnings: []string{}, sel: sel}
	// A version a module argument resolved to (its --ref, a fresh fetch, or the version pinned
	// by r.Version, which is not fetched again) replaces the default head.
	asked := map[string]store.Version{}
	for _, r := range req.Modules {
		sc := Scope{Kind: target.User}
		if len(req.Scopes) > 0 {
			sc = req.Scopes[0].Scope
		}
		var pinned store.Version
		if r.Version != nil {
			if pinned, err = e.pinnedVersion(r); err != nil {
				return nil, err
			}
		}
		_, v, _, err := e.resolve(ctx, sc, req.Cwd, r, pinned)
		if err != nil {
			return nil, err
		}
		if prev, dup := asked[v.Name]; dup && prev.ID != v.ID {
			return nil, errs.WithHints(fmt.Errorf("module arguments resolve to two versions of %s", untrusted.Text(v.Name, untrusted.MaxName)),
				"name each module once, with one --ref")
		}
		asked[v.Name] = v
		ch.Requested = append(ch.Requested, v.Name)
		ch.Versions = append(ch.Versions, VersionRef{Name: v.Name, ID: v.ID})
	}
	type state struct {
		sc  ScopeChoice
		man *manifest.Manifest
		led *ledger.Ledger
	}
	var states []state
	for _, s := range req.Scopes {
		man, led, _, err := e.load(s.Scope, true)
		if err != nil {
			return nil, err
		}
		fixed := slices.Clone(man.Targets)
		if fixed == nil {
			fixed = []string{}
		}
		detected := e.Detect(s.Scope)
		for i := range detected {
			detected[i].Binary = untrusted.Text(detected[i].Binary, untrusted.MaxName)
		}
		ch.Scopes = append(ch.Scopes, ScopeChoices{ScopeChoice: s, Name: s.Scope.Kind.String(), FixedTargets: fixed, Assistants: detected})
		states = append(states, state{sc: s, man: man, led: led})
	}
	cached, warnings, err := e.CachedModules()
	if err != nil {
		return nil, err
	}
	for _, w := range warnings {
		ch.Warnings = append(ch.Warnings, untrusted.Text(w, untrusted.MaxName))
	}
	// A module argument whose default head does not load is not in cached; the version it
	// resolved to is still offered.
	for name, v := range asked {
		if !slices.ContainsFunc(cached, func(c Cached) bool { return c.Version.Name == name }) {
			o, _, err := e.Store.Origin(name)
			if err != nil {
				return nil, err
			}
			cached = append(cached, Cached{Version: v, Origin: o})
		}
	}
	for _, c := range cached {
		v := c.Version
		if a, ok := asked[v.Name]; ok {
			v = a
		}
		mc := moduleChoice(v, c.Origin)
		for _, st := range states {
			ms, err := e.moduleScope(sel, st.sc.Scope, st.man, st.led, v)
			if err != nil {
				return nil, err
			}
			mc.PerScope = append(mc.PerScope, ms)
		}
		ch.Modules = append(ch.Modules, mc)
	}
	slices.SortFunc(ch.Modules, func(a, b ModuleChoice) int {
		return cmp.Or(cmp.Compare(a.Source, b.Source), cmp.Compare(a.Name, b.Name))
	})
	return ch, nil
}

// pinnedVersion loads the cached version r.Version names. A version removed from the cache
// since the pickers listed it, or an invalid reference, is refused with a way forward.
func (e *Engine) pinnedVersion(r InstallRequest) (store.Version, error) {
	v, err := e.Store.Load(r.Version.Name, r.Version.ID)
	if err == nil {
		return v, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		err = fmt.Errorf("version %s of %s was removed from the cache since it was listed",
			untrusted.Text(r.Version.ID, untrusted.MaxName), untrusted.Text(r.Version.Name, untrusted.MaxName))
	}
	return store.Version{}, errs.WithHints(untrusted.CleanErr(err), "run lola install again to list the current versions")
}

// firstLine is the first line of untrusted text, made safe and capped for a label.
func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return untrusted.Text(strings.TrimSpace(s), maxLabel)
}

// moduleChoice describes a cached version and its components.
func moduleChoice(v store.Version, o store.Origin) ModuleChoice {
	m := v.Module
	mc := ModuleChoice{Name: m.Name, Source: untrusted.Text(o.Source, untrusted.MaxName), Type: untrusted.Text(o.Type, untrusted.MaxName), Format: untrusted.Text(v.Meta.Format, untrusted.MaxName),
		Description: firstLine(m.Description), Digest: untrusted.Text(v.Meta.Digest, untrusted.MaxName), ID: v.ID, Components: []ComponentChoice{}, PerScope: []ModuleScope{}}
	for _, s := range m.Skills {
		mc.Components = append(mc.Components, ComponentChoice{Component: Component{config.KindSkill, s.Name}, Description: firstLine(s.Doc.String("description"))})
	}
	for _, c := range m.Commands {
		mc.Components = append(mc.Components, ComponentChoice{Component: Component{config.KindCommand, c.Name}, Description: firstLine(c.Doc.String("description"))})
	}
	for _, a := range m.Agents {
		mc.Components = append(mc.Components, ComponentChoice{Component: Component{config.KindAgent, a.Name}, Description: firstLine(a.Doc.String("description"))})
	}
	for _, s := range m.MCPServers {
		cc := ComponentChoice{Component: Component{config.KindMCP, s.Name}, RunsCommand: s.Transport == module.TransportStdio}
		if !cc.RunsCommand {
			cc.Description = firstLine(s.URL)
		}
		mc.Components = append(mc.Components, cc)
	}
	for _, r := range m.Rules {
		mc.Components = append(mc.Components, ComponentChoice{Component: Component{config.KindRule, r.Name}, Description: firstLine(r.Body)})
	}
	for i := range mc.Components {
		mc.Components[i].Name = untrusted.Text(mc.Components[i].Name, untrusted.MaxName)
	}
	for _, h := range m.RuntimeHooks {
		mc.RuntimeHooks += runtimeHookGroups(h)
	}
	return mc
}

// moduleScope is v's module at sc: installed or not, its exclusions, and the components
// installed now (the installed version, or v when that is no longer cached, through the
// resolver with today's selections).
func (e *Engine) moduleScope(sel *config.Selections, sc Scope, man *manifest.Manifest, led *ledger.Ledger, v store.Version) (ModuleScope, error) {
	x, err := exclusions(sel, sc, v.Name)
	if err != nil {
		return ModuleScope{}, err
	}
	ment := man.Find(v.Name)
	ms := ModuleScope{Scope: sc.Key(), Targets: []string{}, RulesOff: !ment.RulesOn(),
		Excluded: resolveComponents(v.Module, x, true).skipped, Current: []string{}}
	entry := led.Modules[v.Name]
	if entry == nil {
		return ms, nil
	}
	ms.Installed, ms.Digest = true, untrusted.Text(entry.Digest, untrusted.MaxName)
	for _, t := range allTargets(entry, ment, man) {
		ms.Targets = append(ms.Targets, untrusted.Text(t, untrusted.MaxName))
	}
	installed := v
	if entry.Digest != v.Meta.Digest {
		if id := strings.TrimPrefix(entry.Digest, "sha256:"); len(id) >= 12 {
			if iv, err := e.Store.Load(v.Name, id[:12]); err == nil {
				installed = iv
			}
		}
	}
	for _, c := range components(resolveComponents(installed.Module, x, ment.RulesOn()).module) {
		ms.Current = append(ms.Current, c.String())
	}
	return ms, nil
}

// runtimeHookGroups counts the matcher groups of a runtime hook set; a set that does not
// decode counts none (loading already validated it).
func runtimeHookGroups(h module.RuntimeHookSet) int {
	var events map[string][]json.RawMessage
	if json.Unmarshal(h.Hooks, &events) != nil {
		return 0
	}
	n := 0
	for _, groups := range events {
		n += len(groups)
	}
	return n
}

// ComponentDiff is what a run changes in one module's components for one assistant at one
// destination.
type ComponentDiff struct {
	Added     []string `json:"added"`
	Updated   []string `json:"updated"`
	Removed   []string `json:"removed"`
	Unchanged int      `json:"unchanged"`
}

// Diff compares the components a run selects (kind/name) for assistant id at the destination
// key with those installed for it there now. A component in both is updated when the cached
// version differs from the installed one.
func (m ModuleChoice) Diff(key, id string, selected []string) ComponentDiff {
	ms := m.Scope(key)
	var current []string
	if slices.Contains(ms.Targets, id) {
		current = ms.Current
	}
	d := ComponentDiff{Added: []string{}, Updated: []string{}, Removed: []string{}}
	for _, c := range selected {
		switch {
		case !slices.Contains(current, c):
			d.Added = append(d.Added, c)
		case ms.Digest != m.Digest:
			d.Updated = append(d.Updated, c)
		default:
			d.Unchanged++
		}
	}
	for _, c := range current {
		if !slices.Contains(selected, c) {
			d.Removed = append(d.Removed, c)
		}
	}
	return d
}

// Preview is what an install would select, without writing anything.
type Preview struct {
	Selected map[string]map[string][]string // destination (Scope.Key) → module → kind/name it installs
	Changes  []config.Change                // the selections.yaml entries it would write
	Notices  []Notice
}

// Preview applies spec's selection edits to a copy of selections.yaml and resolves, per scope,
// the components each of modules would install: the summary shows exactly what InstallAll
// will do. Rules are left out where they are off.
func (c *Choices) Preview(spec InstallSpec, modules []string) (*Preview, error) {
	sel := c.sel.Clone()
	var targets []editTarget
	for _, name := range modules {
		if m := c.Module(name); m != nil {
			t := editTarget{name: name}
			for _, cc := range m.Components {
				t.comps = append(t.comps, cc.Component)
			}
			targets = append(targets, t)
		}
	}
	var sections []config.Where
	for _, si := range spec.Scopes {
		w, err := where(si.Scope)
		if err != nil {
			return nil, err
		}
		sections = append(sections, w)
	}
	pv := &Preview{Selected: map[string]map[string][]string{}}
	changes, notes, err := applyEdits(sel, config.Global(), spec.GlobalEdits, targets, sections)
	if err != nil {
		return nil, err
	}
	pv.Changes, pv.Notices = changes, notes
	for i, si := range spec.Scopes {
		ch, n, err := applyEdits(sel, sections[i], si.Edits, targets, nil)
		if err != nil {
			return nil, err
		}
		pv.Changes, pv.Notices = append(pv.Changes, ch...), append(pv.Notices, n...)
		key := si.Scope.Key()
		pv.Selected[key] = map[string][]string{}
		for _, t := range targets {
			m := c.Module(t.name)
			rulesOn := !m.Scope(key).RulesOff
			if spec.Options.Rules != nil {
				rulesOn = *spec.Options.Rules
			}
			x := sel.Exclusions(sections[i], t.name)
			out := []string{}
			for _, cc := range t.comps {
				if !x.Excludes(cc.Kind, cc.Name) && (rulesOn || cc.Kind != config.KindRule) {
					out = append(out, cc.String())
				}
			}
			pv.Selected[key][t.name] = out
		}
	}
	return pv, nil
}

// SelectionsPath is the selections.yaml the choices were read from.
func (c *Choices) SelectionsPath() string { return c.sel.Path }
