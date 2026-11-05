package engine

import (
	"fmt"
	"slices"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// The states of a component at a scope (cherry-pick design §6): installed (the selection takes
// it), skipped (a skip: leaves it out), not_in_only (only the only: list in force leaves it out) or
// rules_off (a rule, while the module's rules are off).
const (
	ComponentInstalled = "installed"
	ComponentSkipped   = "skipped"
	ComponentNotInOnly = "not_in_only"
	ComponentRulesOff  = "rules_off"
)

// InfoComponent is one component of a module version at one scope that provides the module
// (cherry-pick design §6): its state, the layer whose picks leave it out (Origin, as output names
// it; "" unless skipped or not_in_only), whether the version adds it over the version installed
// there (New; false when that is unknown), and what it mentions ([] for none).
type InfoComponent struct {
	Kind     string   `json:"kind"`
	Name     string   `json:"name"`
	State    string   `json:"state"`
	Origin   string   `json:"origin"`
	New      bool     `json:"new"`
	Mentions []string `json:"mentions"`
}

// componentState is one row of componentStates: the component itself, unshown, for callers that
// match rows to components (moduleScope), and its row as output shows it.
type componentState struct {
	Component Component
	InfoComponent
}

// componentStates is the one classification of a module's components at a scope: any view of
// their states (lola info's, the component page's) gets them here. It is each component of v, in picker order, as x
// (the module's exclusions there) and rulesOn leave it. A skipped component names the layer whose
// skip matches (the scope's before the global one), a not_in_only one the only: list's layer, each
// through label (originLabel, bound to the module's provider). New compares with had, the
// components of the version installed there (installedSet), and is false unless known. Mentions
// come from ms (scanMentions', keyed by full name; nil for none), truncated only to show. A rule
// both skipped and off is skipped: the pick outlives a later --rules.
func componentStates(v store.Version, x config.Exclusions, rulesOn bool, had []Component, known bool,
	label func(origin string) (string, error), ms Mentions) ([]componentState, error) {
	out := []componentState{}
	for _, c := range components(v.Module) {
		ic := InfoComponent{Kind: c.Kind, Name: shown(c.Name), State: ComponentInstalled, New: known && !slices.Contains(had, c), Mentions: []string{}}
		origin := ""
		switch {
		case x.LeftOutByOnly(c.Kind, c.Name):
			ic.State = ComponentNotInOnly
			_, origin = x.Only()
		case x.Excludes(c.Kind, c.Name):
			ic.State, origin = ComponentSkipped, x.Global.Origin
			if slices.ContainsFunc(x.Scope.Skip, func(p config.Pattern) bool { return p.Matches(c.Kind, c.Name) }) {
				origin = x.Scope.Origin
			}
		case c.Kind == config.KindRule && !rulesOn:
			ic.State = ComponentRulesOff
		}
		var err error
		if ic.Origin, err = label(origin); err != nil {
			return nil, err
		}
		for _, o := range ms[c] {
			ic.Mentions = append(ic.Mentions, untrusted.Text(o.String(), untrusted.MaxName))
		}
		out = append(out, componentState{Component: c, InfoComponent: ic})
	}
	return out, nil
}

// infoComponents fills in, lola info's element for sc, with v's module there as info shows it to
// re-pick (cherry-pick design §6; spec 2026-10-29 §13 item 9): From, the layer that provides it
// (lock.json's form; "" for none), Components, their states (componentStates, against the version
// installed there), and Change, the run that changes its picks (repickRun, a profile being on as
// the scope's ledger says). It leaves them unset ("", [], "") where no layer provides the module. A
// version installed there that the cache holds but cannot read is a warning, and nothing is then
// new; a failure to list the profiles is a warning, profiles are then named bare and there is no
// Change. Any other error leaves in as it was: the caller keeps a refused scope's refusal, which
// already says what fails there (a scope whose profile does not load), rather than fail lola info.
func (e *Engine) infoComponents(in *InfoInstall, sc Scope, v store.Version, ms Mentions) error {
	unlock, err := e.readProfiles(true)
	if err != nil {
		return err
	}
	defer unlock()
	man, led, _, err := e.load(sc, true)
	if err != nil {
		return err
	}
	layers, _, err := e.layers(sc, man, led)
	if err != nil {
		return err
	}
	want, _ := desired(layers)
	w := find(want, v.Name)
	if w == nil {
		return nil
	}
	entry := led.Modules[v.Name]
	from := "" // lock.json names a profile layer, not the scope's manifest (as activate compares it)
	if w.profile {
		from = w.From
	}
	in.current = in.current && entry != nil && entry.Digest == v.Meta.Digest && entry.From == from
	sel, err := e.selections()
	if err != nil {
		return err
	}
	if sel, err = withPicks(sel, layers); err != nil {
		return err
	}
	x, err := exclusions(sel, sc, w.From, v.Name)
	if err != nil {
		return err
	}
	had, known, err := e.installedSet(v.Name, entry)
	if err != nil {
		// The plan's new-component notice may have said so already.
		if n := (Notice{Level: LevelWarn, Text: fmt.Sprintf("%s: cannot tell which components are new: %s", v.Name, untrusted.CleanErr(err).Error())}); !slices.Contains(in.Notices, n) {
			in.Notices = append(in.Notices, n)
		}
	}
	infos, err := e.visibleOnce(sc)()
	listed := err == nil
	if !listed {
		in.Notices = append(in.Notices, Notice{Level: LevelWarn,
			Text: fmt.Sprintf("%s: cannot list the profiles, so lola info names no run that re-picks its components: %s", v.Name, untrusted.CleanErr(err).Error())})
	}
	states, err := componentStates(v, x, w.RulesOn(), had, known, func(origin string) (string, error) {
		return originLabel(x, origin, w.From, func() ([]profile.Info, error) { return infos, nil })
	}, ms)
	if err != nil {
		return err
	}
	in.From, in.FromLabel, in.Components = shown(w.From), layerLabel(infos, w.From), make([]InfoComponent, 0, len(states))
	for _, st := range states {
		in.Components = append(in.Components, st.InfoComponent)
	}
	if listed {
		in.Change = e.repickRun(sc, layers, infos, led.Profile != nil, w.From, v.Name)
	}
	return nil
}
