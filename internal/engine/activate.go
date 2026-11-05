package engine

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/trust"
)

// ProfileAction is what lola profile does to a scope's active profile (Plan 6b §3.2, §6.3).
type ProfileAction string

// The activation actions; each is also the flag lola profile takes for it.
const (
	ProfileOn     ProfileAction = "on"
	ProfileOff    ProfileAction = "off"
	ProfileSwitch ProfileAction = "switch"
)

// ProfileChange is the activation a sync applies under the scope lock (Plan 6b §6.3, §19 R3; spec
// 2026-10-29 §6.2): Action on Name, the profile's name as the user typed it; "" only for --off,
// which then turns off whatever profile is on.
type ProfileChange struct {
	Action ProfileAction
	Name   string
	// New, with --switch, makes Name first (spec 2026-10-29 §6.2 --new): empty, a copy of From's
	// entries, or with Bundle (members as typed) a bundle; newProfiles.
	New    bool
	From   string
	Bundle []string
}

// Activation is what an activation's sync reports beside the sync's own result (Plan 6b §12.2,
// §19 R14; spec 2026-10-29 §8.5): Profile, the scope's active profile after the run by qualified
// name (the one before when the run applied nothing), null when only the base is loaded, and
// Changes, what the change loads, replaces and unloads. Named is the qualified name of the
// profile the run turns on or off (for --off, the one it turns off; "" when none is on), File its
// file ("" when it is gone), and Turned whether the run changes the profile on (false when it is
// already as asked, which a notice reports), for human output.
type Activation struct {
	Profile *string       `json:"profile"`
	Changes []LayerChange `json:"changes"`
	Named   string        `json:"-"`
	File    string        `json:"-"`
	Turned  bool          `json:"-"`
}

// OtherProfileOnError refuses --on of a profile while another is on (spec 2026-10-29 §6.2, §6.3):
// one profile is on at a time, and --on never replaces it without asking; --switch does. On and
// Name are the qualified names of the profile on and of the one named; the CLI asks on a terminal.
type OtherProfileOnError struct {
	On, Name string
	// Bundle is the bundle of yours a terminal's question offers in their place, and Members its
	// members, as memberName names them (spec 2026-10-29 §6.2 --on).
	Bundle  string
	Members []string
	// Into is the bundle on, as lola profile <name> --edit names it, when the one named can join it
	// (a bundle of yours, or a project's bundle naming a project or remote profile), and Joined its
	// members then, as its file writes them; "" when it cannot.
	Into   string
	Joined []string
	err    error
}

func (e *OtherProfileOnError) Error() string { return e.err.Error() }

func (e *OtherProfileOnError) Unwrap() error { return e.err }

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

// baseName addresses a scope's base in lola profile (spec 2026-10-29 §3.1): default at user
// scope, project.yml at project scope.
const baseName = "base"

// activeProfile is the scope's active profile after ch (spec 2026-10-29 §3.1, §6.2), infos being
// the profiles sc can use and cur the one on (nil for none). --switch makes the named profile the
// one on, whatever is on; --on does when none is, or when it is the project's profile of the name
// of the one on, yours or a remote one (Plan 6b §4.4, D8; ruling R-6b3a-1), and otherwise refuses
// (*OtherProfileOnError), hinting --switch: it never replaces a profile without asking. --off is
// turnOff. A remote profile a local one shadows is refused for --on and --switch (local beats
// remote). The base, by base or at user scope by default, is baseChange; default at project scope
// is refused, hinting the runs that would change something there. A profile already on is a
// notice. It returns the new profile on, the qualified name of the profile the run turns on or
// off and its file, and the notices. flags is the run's flags, which a hint repeating it keeps
// (and --on's --switch hint keeps the --trust of); again is what a hint naming another run keeps
// (SyncRequest.again).
func (e *Engine) activeProfile(sc Scope, infos []profile.Info, cur *ledger.Active, ch ProfileChange, flags, again []string) (*ledger.Active,
	string, string, []Notice, error) {
	kind, bare, qualified := strings.Cut(ch.Name, ":")
	if !qualified {
		kind, bare = "", ch.Name
	}
	isDefault := bare == profile.DefaultName && (kind == "" || kind == string(profile.KindUser))
	switch {
	case isDefault && sc.Kind == target.Project:
		// Each hint is a run that works and changes something: the project's base where that run
		// would act, and for --switch user scope's default while a profile is on there.
		var hints []string
		run := func(s Scope, args ...string) string { return e.Command(s, "profile", append(args, again...)...) }
		switch {
		case ch.Action == ProfileSwitch:
			if cur != nil {
				hints = append(hints, run(sc, baseName, "--switch"))
			}
			user := Scope{Kind: target.User}
			led, err := e.Ledger(user)
			if err != nil {
				return nil, "", "", nil, err
			}
			if led.Profile != nil {
				hints = append(hints, run(user, profile.DefaultName, "--switch"))
			}
		case ch.Action == ProfileOn && cur == nil:
			// no hint: a base --on would do nothing (exact coaching)
		case ch.Action == ProfileOn:
			hints = []string{run(sc, baseName, "--switch")}
		case cur != nil:
			hints = []string{run(sc, "--off")}
		}
		return nil, "", "", nil, errs.WithHints(errors.New("default is user scope's base; this project's base is project.yml"), hints...)
	case ch.Name == baseName || isDefault:
		return e.baseChange(sc, infos, cur, ch.Action, again)
	case ch.Action == ProfileOff:
		return e.turnOff(sc, infos, cur, ch.Name, flags)
	}
	if ch.Name == "" {
		return nil, "", "", nil, errs.WithHints(fmt.Errorf("--%s needs the name of a profile", ch.Action), "list them: "+e.Command(sc, "profile"))
	}
	typed := append([]string{ch.Name, "--" + string(ch.Action)}, flags...)
	i, err := e.resolveProfile(sc, infos, ch.Name, typed)
	if err != nil {
		return nil, "", "", nil, err
	}
	q := i.Qualified()
	if i.Kind == profile.KindRemote && (cur == nil || cur.Name != q) {
		// Local beats remote (Plan 6b §4.4, decision 4): a remote profile a local one shadows is
		// never turned on.
		if sh := shadowing(infos, i); sh != nil {
			return nil, "", "", nil, errs.WithHints(fmt.Errorf("%s is shadowed by %s %s (%s): a local profile always wins over a remote one of its name",
				shown(q), ownedBy(sh.Kind, false), shown(sh.Qualified()), shown(sh.Path)),
				// another profile than the one named: the run's -y answered nothing about it (ruling R-8)
				"use "+ownedBy(sh.Kind, true)+": "+e.Command(sc, "profile", qualifiedArgs(slices.DeleteFunc(slices.Clone(typed),
					func(a string) bool { return a == "-y" }), ch.Name, sh.Qualified())...),
				"or use both, bundled: "+e.bundleRun(sc, freeName(infos, i.Name+"-both"), []string{sh.Qualified(), q}, again),
				"or rename or delete "+shown(sh.Path))
		}
	}
	// standsIn: the profile on is the remote profile q, yours, is read in place of (D8): q is on.
	standsIn := cur != nil && i.Kind == profile.KindUser && cur.Name == string(profile.KindRemote)+":"+i.Name &&
		userShadow(infos, cur.Name) != nil
	switch {
	case cur != nil && cur.Name == q:
		return cur, q, shown(i.Path), []Notice{{Level: LevelInfo, Text: shown(q) + " is already on"}}, nil
	case standsIn && ch.Action == ProfileOn:
		return cur, q, shown(i.Path), []Notice{{Level: LevelInfo, Text: shown(q) + " is already on, in place of " + shown(cur.Name)}}, nil
	case ch.Action == ProfileSwitch:
		return &ledger.Active{Name: q, By: ledger.BySwitch}, q, shown(i.Path), nil, nil
	case cur == nil || replaces(cur, i):
		return &ledger.Active{Name: q, By: ledger.ByOn}, q, shown(i.Path), nil, nil
	}
	// The --switch hint keeps the run's --trust, never its --force or -y: --switch removes files
	// --on would not, so nothing may answer their removal unasked.
	sw := []string{q, "--switch"}
	if k := slices.IndexFunc(flags, trustArg); k >= 0 {
		sw = append(sw, flags[k])
	}
	// The bundle of both, which the terminal's question offers too (spec 2026-10-29 §6.2 --on).
	members := []string{i.Name}
	if k := activeInfo(infos, cur.Name); k >= 0 {
		members = []string{memberName(infos, infos[k]), memberName(infos, i)}
	}
	name := bundleName(infos, members)
	other := &OtherProfileOnError{On: cur.Name, Name: q, Bundle: name, Members: members}
	hints := []string{e.Command(sc, "profile", append(sw, again...)...), e.bundleRun(sc, name, members, append(slices.Clone(sw[2:]), again...))}
	// Or add it to the bundle on (spec 2026-10-29 §6.2 --edit --bundle), which keeps the bundle on.
	if k := activeInfo(infos, cur.Name); k >= 0 && (infos[k].Kind == profile.KindUser || infos[k].Kind == profile.KindProject && i.Kind != profile.KindUser) {
		p, _, err := e.readFile(infos[k])
		var pending *trust.Report
		if err == nil {
			pending, err = e.pendingMembers(sc, infos[k], p)
		}
		// Never a pulled member list you have not trusted (the edit would write it), nor one a bundle
		// cannot hold.
		if err == nil && pending == nil && p.Shape() == profile.ShapeBundle && len(p.Bundle) < profile.MaxMembers {
			add := memberName(infos, i)
			if infos[k].Kind == profile.KindProject {
				add = i.Name
				if i.Kind == profile.KindRemote {
					add = i.Qualified()
				}
			}
			if !slices.Contains(p.Bundle, add) && !slices.Contains(p.Bundle, i.Qualified()) { // in either spelling
				other.Into, other.Joined = shown(editName(infos, infos[k])), append(slices.Clone(p.Bundle), add)
				hints = append(hints, "or add it to bundle "+bundleLabel(infos, cur.Name)+": "+e.Command(sc, "profile",
					append([]string{other.Into, "--edit", "--bundle", strings.Join(other.Joined, ",")}, again...)...))
			}
		}
	}
	other.err = errs.WithHints(fmt.Errorf("%s is on, and --on does not replace it without asking; --switch does", shown(cur.Name)), hints...)
	return nil, "", "", nil, other
}

// replaces reports whether turning i on puts it in place of cur (Plan 6b §4.4, D8; ruling
// R-6b3a-1): i is the project's profile of the name of cur, yours or a remote one.
func replaces(cur *ledger.Active, i profile.Info) bool {
	kind, name, _ := strings.Cut(cur.Name, ":")
	return i.Kind == profile.KindProject && name == i.Name && replaceable(kind)
}

// turnOff is --off's half of activeProfile (spec 2026-10-29 §6.2): name names the profile on, by
// its qualified name or its bare one, whether or not its file is still there (with one profile on,
// a bare name cannot mean another: decisions.md, row 85), or name is "": the profile on goes off.
// A name that resolves to a profile that is not on is a notice, as is a bare name several kinds
// define when none of them is on; one that resolves to none is refused. flags is the run's flags,
// which a refusal's hints keep.
func (e *Engine) turnOff(sc Scope, infos []profile.Info, cur *ledger.Active, name string, flags []string) (*ledger.Active, string, string, []Notice, error) {
	if cur != nil {
		_, bare, _ := strings.Cut(cur.Name, ":")
		if name == "" || name == cur.Name || name == bare {
			file := ""
			if k := slices.IndexFunc(infos, func(i profile.Info) bool { return i.Qualified() == cur.Name }); k >= 0 {
				file = shown(infos[k].Path)
			}
			return nil, cur.Name, file, nil, nil
		}
	}
	if name == "" {
		return nil, "", "", []Notice{{Level: LevelInfo, Text: "no profile is on"}}, nil
	}
	i, err := e.resolveProfile(sc, infos, name, append([]string{name, "--off"}, flags...))
	if amb, ok := errors.AsType[*ambiguousProfileError](err); ok {
		var all []string
		for _, f := range amb.found {
			all = append(all, shown(f.Qualified()))
		}
		text := "neither " + strings.Join(all, " nor ") + " is on"
		if len(all) > 2 {
			text = "none of " + andJoin(all) + " is on"
		}
		return cur, "", "", []Notice{{Level: LevelInfo, Text: text}}, nil
	}
	if err != nil {
		return nil, "", "", nil, err
	}
	return cur, "", "", []Notice{{Level: LevelInfo, Text: shown(i.Qualified()) + " is not on"}}, nil
}

// baseChange is activeProfile for a name of the scope's base (spec 2026-10-29 §3.1, §6.3): --switch
// to it turns the profile on off (a notice when none is), --on of it is a notice when none is on
// and refused, hinting that --switch, when one is; --off of it is refused, hinting --off when a
// profile is on. again is what a hint keeps of the run.
func (e *Engine) baseChange(sc Scope, infos []profile.Info, cur *ledger.Active, action ProfileAction, again []string) (*ledger.Active,
	string, string, []Notice, error) {
	switch {
	case action == ProfileOff:
		var hints []string
		if cur != nil {
			hints = append(hints, e.Command(sc, "profile", append([]string{"--off"}, again...)...))
		}
		return nil, "", "", nil, errs.WithHints(errors.New("the base is always loaded; lola profile --off turns off the profile over it"), hints...)
	case cur == nil && action == ProfileOn:
		return nil, "", "", []Notice{{Level: LevelInfo, Text: "the base is always loaded"}}, nil
	case cur == nil:
		return nil, "", "", []Notice{{Level: LevelInfo, Text: "already on the base"}}, nil
	case action == ProfileOn:
		return nil, "", "", nil, errs.WithHints(fmt.Errorf("the base is always loaded; %s is on over it", shown(cur.Name)),
			e.Command(sc, "profile", append([]string{baseName, "--switch"}, again...)...))
	}
	return e.turnOff(sc, infos, cur, "", nil)
}

// activeName is a's qualified name, "" for none.
func activeName(a *ledger.Active) string {
	if a == nil {
		return ""
	}
	return a.Name
}

// layerChanges is the plan of an activation (Plan 6b §6.3 step 3): the difference between want,
// what the scope's layers want with the new active profile, and led, what lola installed. A module
// wanted and not installed is a load; one installed from another layer or at another source, ref,
// path or pin is a replace; one installed that no layer wants is an unload. manifest is the scope
// manifest's base name, the layer lock.json leaves unnamed. ps resolves the version an entry of
// yours asks for (pinSource), and a ref that stays the same while its commit moves is shown by commit.
func layerChanges(want []Wanted, led *ledger.Ledger, manifest string, ps pinSource) []LayerChange {
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
		c := LayerChange{Module: shown(w.Name), From: shown(w.From), Over: over, Version: ps.wantedVersion(w, in), name: w.Name}
		switch {
		case in == nil:
			c.Action = ChangeLoad
		case in.From != from || in.Source != w.Source || in.Ref != w.Ref || in.Path != w.Path || w.Digest != "" && w.Digest != in.Digest:
			c.Action = ChangeReplace
			c.Was = installedVersion(in)
			if ent, _, _, ok := ps.ownPin(w, in); ok && c.Was == c.Version && ent.Commit != in.Commit {
				c.Was, c.Version = shortCommit(in.Commit), shortCommit(ent.Commit)
			}
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
	return shown(cmp.Or(in.Ref, shortCommit(in.Commit), in.Source))
}

// shortCommit is the first 12 characters of a commit, as a notice names it.
func shortCommit(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

// pinSource is what resolves the version of an entry of your profiles, which carries no pin: the
// run's pins.yaml (nil off user scope), the versions another scope installed that a move between
// scopes carries, and whether the run updates. The zero value resolves only what lock.json keeps.
type pinSource struct {
	pins   *config.Pins
	seed   map[string]*ledger.Entry
	update bool
}

// ownPin is the entry of your profiles resolved to the version a sync installs (spec 2026-10-29
// §3.4): pins.yaml's pin of its key at user scope, else the version lock.json holds for its key,
// else a move's seed, which comes before pins.yaml (plan 12.2b ambiguity 5) so the next plain
// sync applies that pin. resolved is false when none applies, and ent is then w.Entry. validateSync
// and wantedVersion both resolve through it, so the change line names what the sync installs.
func (ps pinSource) ownPin(w Wanted, in *ledger.Entry) (ent manifest.Entry, fromPins, seeded, resolved bool) {
	ent = w.Entry
	if !w.own() || ent.Digest != "" || ps.update || source.IsFolderForm(ent.Source) {
		return ent, false, false, false
	}
	var pin *config.Pin
	if ps.pins != nil && !remoteLayer(w.From) {
		pin = ps.pins.Find(w.Name, ent.Source, ent.Ref, ent.Path)
	}
	s := ps.seed[w.Name]
	seeded = s != nil && s.Digest != "" && s.Source == ent.Source && s.Ref == ent.Ref && s.Path == ent.Path
	switch {
	case pin != nil && !seeded:
		ent.Commit, ent.Digest = pin.Commit, pin.Digest
		return ent, true, false, true
	case in != nil && in.Digest != "" && in.Source == ent.Source && in.Ref == ent.Ref && in.Path == ent.Path:
		ent.Commit, ent.Digest = in.Commit, in.Digest
		return ent, false, false, true
	case seeded:
		ent.Commit, ent.Digest = s.Commit, s.Digest
		return ent, false, true, true
	}
	return ent, false, false, false
}

// wantedVersion is the version w resolves to, as entryVersion names it, so a change line never
// names the source where a pin or lock.json records the version.
func (ps pinSource) wantedVersion(w Wanted, in *ledger.Entry) string {
	ent, _, _, _ := ps.ownPin(w, in)
	return entryVersion(ent)
}

// activation is an activation's state through a sync: the profiles it can name, the new profile
// on, the result's Activation, and whether the profile on changes at all.
type activation struct {
	infos   []profile.Info
	next    *ledger.Active
	action  ProfileAction // the action as applied: --off for a --switch to the base
	report  *Activation
	changed bool
}

// startActivation computes the new active profile of an activation (activeProfile, Plan 6b §6.3
// step 2) from one listing of the profiles directory. The stored name is kept: an active remote
// profile that a user profile of its name now shadows is read as that profile on every run
// (loadStack, D8), never rewritten by an action that does not name it. flags is the run's flags,
// for hints that repeat it (rerun, and -n in a dry run); again is what a hint naming another run
// keeps.
func (e *Engine) startActivation(sc Scope, led *ledger.Ledger, ch ProfileChange, flags, again []string) (*activation, []Notice, error) {
	infos, _, err := e.visible(sc)
	if err != nil {
		return nil, nil, err
	}
	next, named, file, notices, err := e.activeProfile(sc, infos, led.Profile, ch, flags, again)
	if err != nil {
		return nil, nil, err
	}
	changed := activeName(next) != activeName(led.Profile)
	after := led.Profile
	if changed {
		after = next
	}
	report := &Activation{Changes: []LayerChange{}, Named: named, File: file, Turned: changed}
	if after != nil {
		s := shown(after.Name)
		report.Profile = &s
	}
	action := ch.Action
	if ch.Action == ProfileSwitch && next == nil {
		action = ProfileOff // a --switch to the base (baseChange)
	}
	return &activation{infos: infos, next: next, action: action, report: report, changed: changed}, notices, nil
}

// moved is the part of all, the difference between what the new profile on wants (want) and
// lock.json (led), that the change of profile moves (Plan 6b §6.3 step 3): a module whose winning
// entry differs from the one the old profile gave (old: absent, from another layer, or another
// entry), and an installed module no layer of the new profile wants that the old profile wanted
// or lock.json says the named profile installed (a profile whose file is gone counts as off in the
// old stack too). Every other difference is pending from before and left to lola sync.
func moved(all []LayerChange, old, want []Wanted, led *ledger.Ledger, named string) []LayerChange {
	out := []LayerChange{}
	for _, c := range all {
		o := find(old, c.name)
		if c.Action == ChangeUnload {
			if o != nil || named != "" && led.Modules[c.name].From == ledger.FromProfile(named) {
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
