package engine

import (
	"cmp"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/activeprofile"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/profile"
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
// qualified name each typed name resolved to, in order, Files each one's file ("" for one
// whose file is gone), and Turned whether the run turns each on or off (false for one already
// as asked, which a notice reports), for human output.
type Activation struct {
	Profiles []string      `json:"profiles"`
	Changes  []LayerChange `json:"changes"`
	Named    []string      `json:"-"`
	Files    []string      `json:"-"`
	Turned   []bool        `json:"-"`
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
// none, --off also takes a name the list holds whose file is gone, as typed or as user:<name> or
// project:<name>, refusing a bare name that matches both, and refusing as well a bare name that
// resolves to a profile that is off while the other kind's is on with its file gone, D8; --off of
// a bare name both kinds define is refused naming the one that is on when only one is, and is a
// notice when neither is), a name typed twice refused before any is resolved and one resolved
// twice after, --on appending in order, --off removing, --switch replacing the list, --on
// putting a project profile in place of the user's active namesake (D8). default is never in the
// list: --on of it is a notice, --off and --switch of it are refused with the edit that turns it
// off, and at project scope it is refused. A profile already on, or not on, is a notice; --off's
// come in the order typed. named is the qualified names in the order typed, files their files.
// flags is the run's flags, which a hint for an ambiguous name keeps.
func (e *Engine) activeList(sc Scope, infos []profile.Info, led *ledger.Ledger, ch ProfileChange, flags []string) (list []ledger.Active, named, files []string,
	notices []Notice, err error) {
	isActive := func(q string) bool {
		return slices.ContainsFunc(led.Profiles, func(a ledger.Active) bool { return a.Name == q })
	}
	typed := append(append(slices.Clone(ch.Names), "--"+string(ch.Action)), flags...)
	for k, n := range ch.Names {
		if slices.Contains(ch.Names[:k], n) {
			return nil, nil, nil, nil, errs.WithHints(fmt.Errorf("%s is named twice", shown(n)), "name each profile once")
		}
	}
	for _, n := range ch.Names {
		q := ""
		i, err := e.resolveProfile(sc, infos, n, typed)
		// For --off, the names the list holds for n, as typed or with its kind: a profile whose
		// file is gone is turned off by them.
		var stored []string
		if ch.Action == ProfileOff {
			for _, s := range []string{n, string(profile.KindUser) + ":" + n, string(profile.KindProject) + ":" + n} {
				if isActive(s) && activeprofile.Check(s) == nil && !slices.Contains(stored, s) {
					stored = append(stored, s)
				}
			}
		}
		// --off of an ambiguous name is refused naming the one that is on, with only its --off,
		// when one is; when neither is, nothing would change whichever was meant: a notice, as
		// --off of a profile that is off is. Both on keeps resolveProfile's refusal.
		if amb, ok := errors.AsType[*ambiguousProfileError](err); ok && ch.Action == ProfileOff {
			on := slices.DeleteFunc(slices.Clone(amb.found), func(f profile.Info) bool { return !isActive(f.Qualified()) })
			off := slices.DeleteFunc(slices.Clone(amb.found), func(f profile.Info) bool { return isActive(f.Qualified()) })
			switch len(on) {
			case 0:
				notices = append(notices, Notice{Level: LevelInfo, Text: fmt.Sprintf("neither %s nor %s is on",
					shown(amb.found[0].Qualified()), shown(amb.found[1].Qualified()))})
				continue
			case 1:
				err = errs.WithHints(fmt.Errorf("%s is ambiguous: %s %s (%s) is on and %s %s (%s) is off", shown(n),
					ownedBy(on[0].Kind, false), shown(on[0].Qualified()), shown(on[0].Path),
					ownedBy(off[0].Kind, false), shown(off[0].Qualified()), shown(off[0].Path)),
					"turn it off: "+e.Command(sc, "profile", qualifiedArgs(typed, n, on[0].Qualified())...))
			}
		}
		_, unknown := errors.AsType[*unknownProfileError](err)
		// A bare name is ambiguous when two active profiles hold it, or when it resolves to a profile
		// that is off while the other kind's, on, has lost its file (D8): refused, never guessed.
		otherOn := err == nil && !strings.Contains(n, ":") && !isActive(i.Qualified()) &&
			slices.ContainsFunc(stored, func(s string) bool { return s != i.Qualified() })
		switch {
		case unknown && len(stored) == 1:
			q, err = stored[0], nil
		case unknown && len(stored) == 2:
			var hints []string
			for _, kind := range []profile.Kind{profile.KindUser, profile.KindProject} {
				hints = append(hints, ownedBy(kind, true)+": "+e.Command(sc, "profile", qualifiedArgs(typed, n, string(kind)+":"+n)...))
			}
			err = errs.WithHints(fmt.Errorf("%s is ambiguous: %s and %s are both on, and neither file exists any more", shown(n),
				shown(string(profile.KindUser)+":"+n), shown(string(profile.KindProject)+":"+n)), hints...)
		case otherOn:
			on := stored[slices.IndexFunc(stored, func(s string) bool { return s != i.Qualified() })]
			kind, _, _ := strings.Cut(on, ":")
			// The file a profile of that kind and name is read from (profile.Visible, profile.Project).
			gone := filepath.Join(e.Paths.UserProfiles, n+".yml")
			if kind == string(profile.KindProject) {
				gone = filepath.Join(sc.ProjectDir, profile.ProjectDir, n+".yml")
			}
			hints := []string{"turn it off: " + e.Command(sc, "profile", qualifiedArgs(typed, n, on)...)}
			// --on of the project's puts it in place of the user's (D8); --on of the user's would
			// leave the project's on, so it is no way to use the user's instead.
			if i.Kind == profile.KindProject {
				hints = append(hints, "to use the project's instead: "+e.Command(sc, "profile", i.Qualified(), "--on"))
			}
			err = errs.WithHints(fmt.Errorf("%s is ambiguous: %s is on but its file %s is gone, and %s (%s) is off", shown(n),
				shown(on), shown(gone), shown(i.Qualified()), shown(i.Path)), hints...)
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
		if ch.Action == ProfileOff && !isActive(q) {
			notices = append(notices, Notice{Level: LevelInfo, Text: shown(q) + " is not on"})
		}
	}
	switch ch.Action {
	case ProfileOn:
		list = slices.Clone(led.Profiles)
		for _, q := range named {
			if isActive(q) {
				continue
			}
			// A project profile named in place of the user's active namesake takes its place in
			// the list (Plan 6b §4.4, D8; ruling R-6b3a-1), unless that one is named too.
			kind, name, _ := strings.Cut(q, ":")
			mine := string(profile.KindUser) + ":" + name
			if k := slices.IndexFunc(list, func(a ledger.Active) bool { return a.Name == mine }); kind == string(profile.KindProject) && k >= 0 &&
				!slices.Contains(named, mine) {
				list[k] = ledger.Active{Name: q, By: ledger.ByOn}
				continue
			}
			list = append(list, ledger.Active{Name: q, By: ledger.ByOn})
		}
		for _, q := range named {
			if isActive(q) {
				k := slices.IndexFunc(list, func(a ledger.Active) bool { return a.Name == q })
				notices = append(notices, Notice{Level: LevelInfo, Text: fmt.Sprintf("%s is already on (%s of %d)", shown(q), Ordinal(k+1), len(list))})
			}
		}
	case ProfileOff:
		list = slices.DeleteFunc(slices.Clone(led.Profiles), func(a ledger.Active) bool { return slices.Contains(named, a.Name) })
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
// 2) from one listing of the profiles directory. flags is the run's flags, for hints that repeat
// it (rerun, and -n in a dry run).
func (e *Engine) startActivation(sc Scope, led *ledger.Ledger, ch ProfileChange, flags []string) (*activation, []Notice, error) {
	infos, _, err := e.visible(sc)
	if err != nil {
		return nil, nil, err
	}
	list, named, files, notices, err := e.activeList(sc, infos, led, ch, flags)
	if err != nil {
		return nil, nil, err
	}
	turned := make([]bool, len(named))
	for i, q := range named {
		on := func(l []ledger.Active) bool {
			return slices.ContainsFunc(l, func(a ledger.Active) bool { return a.Name == q })
		}
		turned[i] = on(led.Profiles) != on(list)
	}
	act := &activation{infos: infos, list: list, changed: !slices.Equal(activeNames(list), activeNames(led.Profiles)),
		report: &Activation{Profiles: activeNames(list), Changes: []LayerChange{}, Named: named, Files: files, Turned: turned}}
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
