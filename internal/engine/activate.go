package engine

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/activeprofile"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/target"
)

// ProfileAction is what lola profile does to a scope's active list (Plan 6b §3.2, §6.3).
type ProfileAction string

// The activation actions; each is also the flag lola profile takes for it.
const (
	ProfileOn     ProfileAction = "on"
	ProfileOff    ProfileAction = "off"
	ProfileSwitch ProfileAction = "switch"
)

// ProfileChange is the activation a sync applies under the scope lock (Plan 6b §6.3, §19 R3):
// Action on Names, profile names as the user typed them, in order.
type ProfileChange struct {
	Action ProfileAction
	Names  []string
}

// Activation is what an activation's sync reports beside the sync's own result (Plan 6b §12.2,
// §19 R14): Profiles, the scope's active list after the run (the old one when it applied
// nothing), and Changes, what the change of list loads, replaces and unloads. Named is the
// qualified name each typed name resolved to, in order, and Files each one's file ("" for one
// whose file is gone), for human output.
type Activation struct {
	Profiles []string      `json:"profiles"`
	Changes  []LayerChange `json:"changes"`
	Named    []string      `json:"-"`
	Files    []string      `json:"-"`
}

// The kinds of LayerChange.
const (
	ChangeLoad    = "load"
	ChangeReplace = "replace"
	ChangeUnload  = "unload"
)

// LayerChange is one module an activation changes (Plan 6b §19 R14): load (no version is
// installed), replace (the winning entry is not the one installed) or unload (no layer wants it).
// From is the layer whose entry wins now, or for an unload the layer lock.json says it came from;
// Over the layers that entry replaced, lowest first. Version and Was are the versions it asks for
// and lock.json records, for human output. Every string has been through untrusted.Text.
type LayerChange struct {
	Module  string   `json:"module"`
	Action  string   `json:"action"`
	From    string   `json:"from"`
	Over    []string `json:"over"`
	Version string   `json:"-"`
	Was     string   `json:"-"`
	name    string   // the module's name, raw
}

// activeList is the scope's active list after ch (Plan 6b §3.2, §4.4), infos being the profiles
// sc can use: each typed name resolved to its qualified name (resolveProfile; when that finds
// none, --off also takes a name the list holds whose file is gone, as typed or as user:<name>),
// a name given twice refused, --on appending in order, --off removing, --switch replacing the
// list. default is never in the list: --on of it is a notice, --off and --switch of it are
// refused with the edit that turns it off, and at project scope it is refused. A profile already
// on, or not on, is a notice. named is the qualified names in the order typed, files their files.
func (e *Engine) activeList(sc Scope, infos []profile.Info, led *ledger.Ledger, ch ProfileChange) (list []ledger.Active, named, files []string,
	notices []Notice, err error) {
	isActive := func(q string) bool {
		return slices.ContainsFunc(led.Profiles, func(a ledger.Active) bool { return a.Name == q })
	}
	for _, n := range ch.Names {
		q := ""
		i, err := e.resolveProfile(sc, infos, n)
		if _, unknown := errors.AsType[*unknownProfileError](err); unknown && ch.Action == ProfileOff {
			for _, stored := range []string{n, string(profile.KindUser) + ":" + n} {
				if isActive(stored) && activeprofile.Check(stored) == nil {
					q, err = stored, nil
					break
				}
			}
		}
		if err != nil {
			return nil, nil, nil, nil, err
		}
		if q == "" {
			q = i.Qualified()
		}
		if i.Kind == profile.KindDefault {
			if ch.Action == ProfileOn {
				notices = append(notices, Notice{Level: LevelInfo, Text: "default is always on in user scope"})
				continue
			}
			return nil, nil, nil, nil, errs.WithHints(fmt.Errorf("default is always on in user scope; lola profile --%s cannot change that", ch.Action),
				"rename or delete "+shown(i.Path)+" to turn it off")
		}
		if slices.Contains(named, q) {
			return nil, nil, nil, nil, errs.WithHints(fmt.Errorf("%s is named twice", shown(q)), "name each profile once")
		}
		named, files = append(named, q), append(files, shown(i.Path))
	}
	switch ch.Action {
	case ProfileOn:
		list = slices.Clone(led.Profiles)
		for _, q := range named {
			if !isActive(q) {
				list = append(list, ledger.Active{Name: q, By: ledger.ByOn})
			}
		}
		for _, q := range named {
			if isActive(q) {
				k := slices.IndexFunc(list, func(a ledger.Active) bool { return a.Name == q })
				notices = append(notices, Notice{Level: LevelInfo, Text: fmt.Sprintf("%s is already on (%s of %d)", shown(q), Ordinal(k+1), len(list))})
			}
		}
	case ProfileOff:
		list = slices.DeleteFunc(slices.Clone(led.Profiles), func(a ledger.Active) bool { return slices.Contains(named, a.Name) })
		for _, q := range named {
			if !isActive(q) {
				notices = append(notices, Notice{Level: LevelInfo, Text: shown(q) + " is not on"})
			}
		}
	case ProfileSwitch:
		for _, q := range named {
			list = append(list, ledger.Active{Name: q, By: ledger.BySwitch})
		}
		if slices.Equal(activeNames(list), activeNames(led.Profiles)) {
			notices = append(notices, Notice{Level: LevelInfo, Text: "the active profiles are already " + activeText(list)})
		}
	}
	if len(list) > activeprofile.Max {
		return nil, nil, nil, nil, errs.WithHints(fmt.Errorf("at most %d profiles can be on in a scope", activeprofile.Max),
			"turn some off: "+e.Command(sc, "profile")+" <name>… --off")
	}
	return list, named, files, notices, nil
}

// activeNames is the qualified names of an active list, in order.
func activeNames(list []ledger.Active) []string {
	out := make([]string, len(list))
	for i, a := range list {
		out[i] = shown(a.Name)
	}
	return out
}

// activeText is an active list for a notice: its names in order, or "none".
func activeText(list []ledger.Active) string {
	if len(list) == 0 {
		return "none"
	}
	return strings.Join(activeNames(list), ", ")
}

// layerChanges is the plan of an activation (Plan 6b §6.3 step 3): the difference between want,
// what the scope's layers want with the new active list, and led, what lola installed. A module
// wanted and not installed is a load; one installed from another layer or at another source, ref,
// path or pin is a replace; one installed that no layer wants is an unload. manifest is the scope
// manifest's base name, the layer lock.json leaves unnamed.
func layerChanges(want []Wanted, led *ledger.Ledger, manifest string) []LayerChange {
	out := []LayerChange{}
	for _, w := range want {
		in := led.Modules[w.Name]
		from := ""
		if w.profile {
			from = w.From
		}
		over := make([]string, len(w.Over))
		for i, o := range w.Over {
			over[i] = shown(o)
		}
		c := LayerChange{Module: shown(w.Name), From: shown(w.From), Over: over, Version: entryVersion(w.Entry), name: w.Name}
		switch {
		case in == nil:
			c.Action = ChangeLoad
		case in.From != from || in.Source != w.Source || in.Ref != w.Ref || in.Path != w.Path || w.Digest != "" && w.Digest != in.Digest:
			c.Action = ChangeReplace
			c.Was = installedVersion(in)
		default:
			continue
		}
		out = append(out, c)
	}
	for _, name := range sortedNames(led) {
		if find(want, name) != nil {
			continue
		}
		in := led.Modules[name]
		out = append(out, LayerChange{Module: shown(name), Action: ChangeUnload, From: shown(cmp.Or(in.From, manifest)), Over: []string{},
			Version: installedVersion(in), name: name})
	}
	slices.SortStableFunc(out, func(a, b LayerChange) int { return strings.Compare(a.Module, b.Module) })
	return out
}

// installedVersion names the version lock.json records for a module: its ref, else its commit,
// else its source, as entryVersion names an unpinned entry's. lock.json records a digest for
// every module, so naming it would set every installed folder apart from its entry.
func installedVersion(in *ledger.Entry) string {
	commit := in.Commit
	if len(commit) > 12 {
		commit = commit[:12]
	}
	return shown(cmp.Or(in.Ref, commit, in.Source))
}

// activation is an activation's state through a sync: the profiles it can name, the new list,
// the result's Activation, and whether the list changed at all.
type activation struct {
	infos   []profile.Info
	list    []ledger.Active
	report  *Activation
	changed bool
}

// startActivation computes the new active list of an activation (activeList, Plan 6b §6.3 step
// 2) from one listing of the profiles directory.
func (e *Engine) startActivation(sc Scope, led *ledger.Ledger, ch ProfileChange) (*activation, []Notice, error) {
	infos, _, err := profile.Visible(e.Paths, sc.Kind == target.User)
	if err != nil {
		return nil, nil, err
	}
	list, named, files, notices, err := e.activeList(sc, infos, led, ch)
	if err != nil {
		return nil, nil, err
	}
	act := &activation{infos: infos, list: list, changed: !slices.Equal(activeNames(list), activeNames(led.Profiles)),
		report: &Activation{Profiles: activeNames(list), Changes: []LayerChange{}, Named: named, Files: files}}
	if !act.changed {
		act.report.Profiles = activeNames(led.Profiles)
	}
	return act, notices, nil
}

// moved is the part of all, the difference between what the new list wants (want) and lock.json
// (led), that the change of list moves (Plan 6b §6.3 step 3): a module whose winning entry
// differs from the one the old list gave (old: absent, from another layer, or another entry), and
// an installed module no layer of the new list wants that the old list wanted or lock.json says
// one of the named profiles installed (a profile whose file is gone counts as off in the old list
// too). Every other difference is pending from before and left to lola sync.
func moved(all []LayerChange, old, want []Wanted, led *ledger.Ledger, named []string) []LayerChange {
	out := []LayerChange{}
	for _, c := range all {
		o := find(old, c.name)
		if c.Action == ChangeUnload {
			if o != nil || slices.ContainsFunc(named, func(q string) bool { return led.Modules[c.name].From == ledger.FromProfile(q) }) {
				out = append(out, c)
			}
			continue
		}
		w := find(want, c.name)
		if o == nil || o.From != w.From || !sameEntry(o.Entry, w.Entry) {
			out = append(out, c)
		}
	}
	return out
}
