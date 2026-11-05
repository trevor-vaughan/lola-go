package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// ProfileRow is one profile a scope can use, as lola profile lists it (Plan 6b §12.2; spec
// 2026-10-29 §6.2): Name is the profile's own name and Kind where it comes from; State is where it
// stands in the scope: base (default, at user scope), on (the profile on), member (reached by the
// bundle on, In naming that bundle) or off; Active is State other than off. Bundle says the file is a
// bundle, and Members is its members as it expands them ([] for a profile, and for a bundle whose
// expansion fails, which a notice reports). A remote profile's Source, Ref, Commit and Digest are its
// source.json's: where it came from and the pin you consented to ("" for a local profile, and for a
// remote one whose source.json cannot be read, which a notice reports); ShadowedBy is the local
// profile of its name that shadows it, nil when none does (§4.4).
// Every string has been through untrusted.Text.
type ProfileRow struct {
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	Path       string   `json:"path"`
	Source     string   `json:"source"`
	Ref        string   `json:"ref"`
	Commit     string   `json:"commit"`
	Digest     string   `json:"digest"`
	Active     bool     `json:"active"`
	State      string   `json:"state"`
	Bundle     bool     `json:"bundle"`
	Members    []string `json:"members"`
	Scope      string   `json:"scope"`
	ShadowedBy *string  `json:"shadowed_by"`
	In         string   `json:"-"` // a member's bundle on, by label
	Qualified  string   `json:"-"`
	Root       string   `json:"-"`
}

// The states of a ProfileRow (spec 2026-10-29 §6.2).
const (
	StateBase   = "base"
	StateOn     = "on"
	StateMember = "member"
	StateOff    = "off"
)

// ProfileState is one profile's state in a scope (Plan 6b §12.1, §12.2): its row and each of its
// modules with the layer whose entry wins there. ReadAs is, for an active remote profile, the
// user profile of its name read in its place (D8), nil otherwise: a project profile that shadows
// it takes its place only when turned on.
type ProfileState struct {
	ProfileRow
	ReadAs  *string         `json:"read_as"`
	Modules []ProfileModule `json:"modules"`
}

// ProfileModule is a module a profile lists, with the layer whose entry the scope uses (From)
// and the layers that entry replaced (Overrides), lowest first; From is "" when no layer of the
// scope wants it (a remote profile's module while a local profile shadows it).
type ProfileModule struct {
	Name      string   `json:"name"`
	From      string   `json:"from"`
	Overrides []string `json:"overrides"`
	// Only and Skip are the entry's picks (cherry-pick design §4.6), kind/name patterns as the
	// profile writes them; [] for none.
	Only []string `json:"only"`
	Skip []string `json:"skip"`
}

// profileScope is what lola profile reads of a scope: the profiles it can use, with what a
// listing says about their directories (visible), and its lock.json. A project whose lock.json
// git tracks is refused before the profiles it names are read (Plan 6b §5, §19 S5); a warning
// that git is missing is in notices, after the listing's.
func (e *Engine) profileScope(ctx context.Context, sc Scope) (infos []profile.Info, listing []Notice, led *ledger.Ledger, notices []Notice, err error) {
	if err := e.refuseSymlinkedState(sc); err != nil {
		return nil, nil, nil, nil, err
	}
	checked := newResult(true)
	if err := e.refuseTrackedLedger(ctx, sc, checked); err != nil {
		return nil, nil, nil, nil, err
	}
	if led, err = e.Ledger(sc); err != nil {
		return nil, nil, nil, nil, err
	}
	if infos, listing, err = e.visible(sc); err != nil {
		return nil, nil, nil, nil, err
	}
	return infos, listing, led, checked.Notices, nil
}

// Profiles is every profile sc can use, in name order (Plan 6b §3.2 "lola profile"), with its state
// in the scope (spec 2026-10-29 §6.2). It reads the profiles directories' listings, lock.json, each
// remote profile's source.json and each profile file of yours and the project's, to tell a bundle
// and its members: one that does not load is listed all the same, with a notice (§3.8: a listing is
// never refused by a profile it does not need). An active profile whose file is gone is a notice,
// as in a sync (§10), and so is a remote profile a user profile now shadows; files whose names are
// not valid profile names are a count, never shown (§4.3, §19 S7).
func (e *Engine) Profiles(ctx context.Context, sc Scope) ([]ProfileRow, []Notice, error) {
	unlock, err := e.readProfiles(true)
	if err != nil {
		return nil, nil, err
	}
	defer unlock()
	infos, listing, led, notices, err := e.profileScope(ctx, sc)
	if err != nil {
		return nil, nil, err
	}
	rows := make([]ProfileRow, 0, len(infos))
	on := e.onState(sc, infos, led)
	for _, i := range infos {
		row, notes := e.profileRow(sc, infos, i, led, on)
		rows, notices = append(rows, row), append(notices, notes...)
	}
	notices = append(notices, e.activeNotices(sc, infos, led.Profile)...)
	if sc.Kind == target.Project {
		notices = append(notices, e.replacementNotices(sc, replacements(infos, led.Profile))...)
	}
	return rows, append(notices, listing...), nil
}

// ProfileNames is the names of the profiles you can turn on or off, for completion: your own
// profiles (default is always on), then the remote ones no profile of yours shadows, by the
// listings of their directories alone.
func (e *Engine) ProfileNames() ([]string, error) {
	infos, _, _, err := profile.Visible(e.Paths, false)
	if err != nil {
		return nil, err
	}
	remote, _, err := profile.Remote(e.Paths)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(infos)+len(remote))
	for _, i := range infos {
		out = append(out, i.Name)
	}
	for _, i := range remote {
		if !slices.Contains(out, i.Name) {
			out = append(out, i.Name)
		}
	}
	return out, nil
}

// onState is what a listing says of the profile on at sc, whose lock.json is led: when it is a
// bundle, its label and the profiles and nested bundles its expansion reaches, by qualified name.
// A bundle that does not expand reaches none: its own row's notice says why (profileRow).
type onState struct {
	bundle  string
	members map[string]bool
}

func (e *Engine) onState(sc Scope, infos []profile.Info, led *ledger.Ledger) onState {
	st := onState{members: map[string]bool{}}
	if led.Profile == nil {
		return st
	}
	k := activeInfo(infos, led.Profile.Name)
	if k < 0 || infos[k].Kind == profile.KindRemote {
		return st
	}
	ms, x, err := e.members(sc, infos, infos[k])
	if err != nil || !x.bundle {
		return st
	}
	st.bundle = bundleLabel(infos, infos[k].Qualified())
	for _, m := range ms {
		st.members[strings.TrimPrefix(m.Name, ledger.FromProfile(""))] = true
		for _, b := range m.chain[1:] {
			st.members[b.name] = true
		}
	}
	return st
}

// profileRow is i's row in sc, whose lock.json is led, whose profiles are infos and whose profile on
// is on, with a warning when i is a remote profile whose source.json cannot be read, or a file of
// yours or the project's that does not load (its row then says no bundle).
func (e *Engine) profileRow(sc Scope, infos []profile.Info, i profile.Info, led *ledger.Ledger, on onState) (ProfileRow, []Notice) {
	row := ProfileRow{Name: shown(i.Name), Kind: string(i.Kind), Path: shown(i.Path), Scope: sc.Kind.String(), State: StateOff, Members: []string{},
		Qualified: shown(i.Qualified()), Root: shown(root(sc))}
	var notes []Notice
	switch {
	case i.Kind == profile.KindDefault:
		row.State = StateBase
	case led.Profile != nil && led.Profile.Name == i.Qualified():
		row.State = StateOn
	case on.members[i.Qualified()]:
		row.State, row.In = StateMember, on.bundle
	}
	row.Active = row.State != StateOff
	if i.Kind == profile.KindRemote {
		o, err := profile.ReadOrigin(filepath.Dir(i.Path))
		if err != nil {
			notes = append(notes, Notice{Level: LevelWarn, Text: shown(i.Qualified()) + ": " + shown(untrusted.CleanErr(err).Error())})
		}
		row.Source, row.Ref, row.Commit, row.Digest = shown(o.Source), shown(o.Ref), shown(o.Commit), shown(o.Digest)
		if sh := shadowing(infos, i); sh != nil {
			by := shown(sh.Qualified())
			row.ShadowedBy = &by
		}
		return row, notes // a remote profile never bundles (spec 2026-10-29 §3.3)
	}
	if i.Kind == profile.KindDefault || isDir(i) {
		return row, notes
	}
	p, _, err := e.readFile(i)
	if _, unresolved := errors.AsType[*manifest.UnresolvedError](err); err != nil && !unresolved {
		text := shown(i.Qualified()) + " does not load: " + shown(untrusted.CleanErr(err).Error())
		if offer, ok := errors.AsType[*SplitOfferError](e.mixedError(sc, infos, i, err)); ok {
			text += " → " + e.Command(offer.Scope, "profile", offer.Name, "--split")
		}
		return row, append(notes, Notice{Level: LevelWarn, Text: text})
	}
	if row.Bundle = p.Shape() == profile.ShapeBundle; !row.Bundle {
		return row, notes
	}
	ms, x, err := e.members(sc, infos, i)
	if err != nil {
		return row, append(notes, Notice{Level: LevelWarn, Text: shown(i.Qualified()) + " does not expand: " + shown(untrusted.CleanErr(err).Error())})
	}
	for _, m := range bundleMembers(infos, ms, x) {
		row.Members = append(row.Members, m.Name)
	}
	return row, notes
}

// goneProfile is the warning about an active profile whose file is gone (Plan 6b §10), or, for a
// remote profile pins.yaml records, not on this machine yet: a sync restores it (spec 2026-10-29
// §3.4).
func (e *Engine) goneProfile(sc Scope, q string) Notice {
	if e.recorded(q) {
		if at := e.inTheWay(strings.TrimPrefix(q, string(profile.KindRemote)+":")); at != "" {
			return Notice{Level: LevelWarn, Text: "profile " + shown(q) + " is on but " + at + " is in the way of it → move it aside, then " + e.SyncCommand(sc) +
				" restores it from pins.yaml"}
		}
		return Notice{Level: LevelWarn, Text: "profile " + shown(q) + " is on but not on this machine yet → " + e.SyncCommand(sc) + " restores it from pins.yaml"}
	}
	return Notice{Level: LevelWarn, Text: "profile " + shown(q) + " is on but no longer exists → " + e.Command(sc, "profile", q, "--off")}
}

// ProfileState is the state of the profile name names in each of scopes that has it (Plan 6b
// §12.1): its row and, for each module it lists, the layer whose entry wins in that scope; for a
// profile that is off, the layer that would win were it switched to, in place of the profile on,
// as --switch does. A scope the name does not resolve in is left out (default at project scope, say);
// when it resolves in none, the first scope's refusal is returned. The named profile is read, and
// the scope's active ones, once each, as a sync reads them. display is the run's output flags,
// which the hints of a file with a marketplace address keep (Plan 6c §6.7).
func (e *Engine) ProfileState(ctx context.Context, scopes []Scope, name string, display ...string) (_ []ProfileState, _ []Notice, err error) {
	defer func() { err = e.CoachUnresolved(err, scopes, false, display) }()
	unlock, err := e.readProfiles(true)
	if err != nil {
		return nil, nil, err
	}
	defer unlock()
	out, notices := []ProfileState{}, []Notice{}
	var first error
	for _, sc := range scopes {
		st, notes, err := e.profileState(ctx, sc, name)
		if _, unknown := errors.AsType[*unknownProfileError](err); unknown {
			if first == nil {
				first = err
			}
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		out, notices = append(out, *st), append(notices, notes...)
	}
	if len(out) == 0 {
		return nil, nil, first
	}
	return out, notices, nil
}

func (e *Engine) profileState(ctx context.Context, sc Scope, name string) (*ProfileState, []Notice, error) {
	infos, _, led, notices, err := e.profileScope(ctx, sc)
	if err != nil {
		return nil, nil, err
	}
	i, err := e.resolveProfile(sc, infos, name, nil)
	if err != nil {
		return nil, nil, err
	}
	man, err := e.Manifest(sc)
	if err != nil {
		return nil, nil, err
	}
	row, notes := e.profileRow(sc, infos, i, led, e.onState(sc, infos, led))
	notices = append(notices, notes...)
	st := &ProfileState{ProfileRow: row, Modules: []ProfileModule{}}
	view := led.Clone()
	// A remote profile a local one shadows cannot be turned on (§4.4), and one that is on is read
	// as the local one (D8): either is shown as it stands, its own entries.
	shadowed := i.Kind == profile.KindRemote && st.ShadowedBy != nil
	if shadowed && st.State == StateOn {
		if mine := userShadow(infos, i.Qualified()); mine != nil {
			by := shown(mine.Qualified())
			st.ReadAs = &by
		} else {
			notices = append(notices, Notice{Level: LevelInfo, Text: "to use " + *st.ShadowedBy + " instead: " +
				e.Command(sc, "profile", *st.ShadowedBy, "--on")})
		}
	}
	if st.State == StateOff && !shadowed {
		view.Profile = &ledger.Active{Name: i.Qualified(), By: ledger.BySwitch}
	}
	pl, err := e.loadStack(sc, infos, view.Profile)
	if err != nil {
		return nil, nil, err
	}
	var own Layer
	var read bool
	switch {
	case i.Kind == profile.KindDefault:
		if read = pl.def != nil; read {
			own = *pl.def
		}
	case shadowed:
		p, _, err := profile.LoadRemote(i.Path)
		if err != nil {
			return nil, nil, e.readd(sc, i.Name, err)
		}
		own, read = Layer{Entries: p.Entries()}, true
	case st.State == StateMember:
		// Its layers as the bundle on places them: its own, or a nested bundle's members.
		var mine []Layer
		for _, l := range pl.named[led.Profile.Name] {
			if l.Name == ledger.FromProfile(i.Qualified()) || slices.ContainsFunc(l.chain, func(b bundleRef) bool { return b.name == i.Qualified() }) {
				mine = append(mine, l)
			}
		}
		own, read = stackLayer(mine), true
	default:
		var stack []Layer
		stack, read = pl.named[i.Qualified()]
		own = stackLayer(stack)
	}
	if !read {
		// infos holds i, so loadStack read it or failed; this guards that rather than index on it.
		return nil, nil, errs.WithHints(fmt.Errorf("profile %s no longer exists", shown(i.Qualified())), "see the profiles: "+e.Command(sc, "profile"))
	}
	want, _ := desired(pl.assemble(e.baseLayer(sc, man, pl), view.Profile))
	for _, ent := range own.Entries {
		m := ProfileModule{Name: shown(ent.Name), Overrides: []string{}, Only: []string{}, Skip: []string{}}
		for _, o := range ent.Only {
			m.Only = append(m.Only, shown(o))
		}
		for _, k := range ent.Skip {
			m.Skip = append(m.Skip, shown(k))
		}
		if w := find(want, ent.Name); w != nil {
			m.From = shown(w.From)
			for _, o := range w.Over {
				m.Overrides = append(m.Overrides, shown(o))
			}
		}
		st.Modules = append(st.Modules, m)
	}
	slices.SortFunc(st.Modules, func(a, b ProfileModule) int { return strings.Compare(a.Name, b.Name) })
	// The scope's warnings are about its own active profile, which a view of another replaces.
	return st, append(notices, e.activeNotices(sc, infos, led.Profile)...), nil
}

// stackLayer is the layers of a profile on, its own or a bundle's members' (loadStack), as one
// layer for its state: each module once, from the first layer that lists it.
func stackLayer(stack []Layer) Layer {
	out := Layer{Entries: []manifest.Entry{}}
	for _, l := range stack {
		for _, ent := range l.Entries {
			if !slices.ContainsFunc(out.Entries, func(o manifest.Entry) bool { return o.Name == ent.Name }) {
				out.Entries = append(out.Entries, ent)
			}
		}
	}
	return out
}

// ownedBy is whose a profile of kind is, as an ambiguous name's refusal says it: "your profile",
// "the project's" or "the remote profile", or as a hint's label, "yours", "the project's" or "the
// remote one".
func ownedBy(kind profile.Kind, label bool) string {
	switch {
	case kind == profile.KindUser && label:
		return "yours"
	case kind == profile.KindUser:
		return "your profile"
	case kind == profile.KindProject:
		return "the project's"
	case kind == profile.KindRemote && label:
		return "the remote one"
	case kind == profile.KindRemote:
		return "the remote profile"
	}
	return string(kind)
}

// qualifiedArgs is args, what the user typed after lola profile, with name replaced by q, its
// qualified name, and q kept once where the user also typed it: the command a hint gives for an
// ambiguous name. nil args, the state form, is q alone.
func qualifiedArgs(args []string, name, q string) []string {
	if args == nil {
		return []string{q}
	}
	out := []string{}
	for _, a := range args {
		if a == name {
			a = q
		}
		if a != q || !slices.Contains(out, q) {
			out = append(out, a)
		}
	}
	return out
}

// unknownProfileError is a profile name that names no profile a scope can use.
type unknownProfileError struct{ error }

func (e *unknownProfileError) Unwrap() error { return e.error }

// ambiguousProfileError is a bare profile name that profiles of several local kinds define (D8),
// with those profiles, so --off can say which of them are on.
type ambiguousProfileError struct {
	error
	found []profile.Info
}

func (e *ambiguousProfileError) Unwrap() error { return e.error }

// resolveProfile is the profile a name typed on the command line names in sc (Plan 6b §4.4),
// among infos, the profiles sc can use. A bare name is a local profile of that name, at project
// scope the project's or the user's, at user scope the user's; else the remote one: local beats
// remote, which a local namesake shadows. A bare name that local profiles of two or more kinds
// define is ambiguous and refused, naming every one of them and its file and hinting, for each,
// the user's command with the name qualified (D8). default is default.yml, at user scope only. A
// qualified name (user:go, project:go, remote:go) names its kind. Any other name is refused,
// listing the profiles sc can use. args is what the user typed after lola profile, the names and
// the action flag, or nil for the state form of a single name.
func (e *Engine) resolveProfile(sc Scope, infos []profile.Info, name string, args []string) (profile.Info, error) {
	kind, bare, qualified := strings.Cut(name, ":")
	if !qualified {
		bare, kind = name, ""
	}
	if bare == profile.DefaultName && (kind == "" || kind == string(profile.KindUser)) {
		if sc.Kind == target.Project {
			return profile.Info{}, &unknownProfileError{errs.WithHints(errors.New("default applies to user scope only"),
				"see it: "+e.Command(Scope{Kind: target.User}, "profile", profile.DefaultName))}
		}
		if k := slices.IndexFunc(infos, func(i profile.Info) bool { return i.Kind == profile.KindDefault }); k >= 0 {
			return infos[k], nil
		}
	}
	if bare == profile.BaseName && qualified && slices.Contains([]profile.Kind{profile.KindUser, profile.KindProject, profile.KindRemote}, profile.Kind(kind)) {
		msg := shown(name) + " is no profile: base is reserved for the scope's base"
		hint := baseNames
		led, err := e.Ledger(sc)
		if err != nil {
			return profile.Info{}, err
		}
		free := e.freeName(sc, led.Profiles())
		if path, renamed := e.reservedPaths(sc, profile.Kind(kind), free); path != "" {
			msg += ", so " + shown(path) + " is not a profile"
			hint = "rename " + shown(path) + " to " + shown(renamed) + " to use it as the profile " + kind + ":" + free
		}
		return profile.Info{}, &unknownProfileError{errs.WithHints(errors.New(msg), hint)}
	}
	var found []profile.Info
	if module.ValidateName("profile", bare) == nil {
		for _, i := range infos {
			if i.Name == bare && i.Kind != profile.KindDefault && (kind == "" && i.Kind != profile.KindRemote || kind == string(i.Kind)) {
				found = append(found, i)
			}
		}
		if len(found) == 0 && kind == "" {
			if k := slices.IndexFunc(infos, func(i profile.Info) bool { return i.Kind == profile.KindRemote && i.Name == bare }); k >= 0 {
				found = append(found, infos[k])
			}
		}
	}
	switch {
	case len(found) == 1:
		return found[0], nil
	case len(found) > 1:
		var named, hints []string
		for _, i := range found {
			named = append(named, ownedBy(i.Kind, false)+" "+shown(i.Qualified())+" ("+shown(i.Path)+")")
			hints = append(hints, ownedBy(i.Kind, true)+": "+e.Command(sc, "profile", qualifiedArgs(args, name, i.Qualified())...))
		}
		all := "both"
		if len(found) > 2 {
			all = "all"
		}
		return profile.Info{}, &ambiguousProfileError{errs.WithHints(fmt.Errorf("%s is ambiguous: %s are %s named %s", shown(bare),
			andJoin(named), all, shown(bare)), hints...), found}
	}
	var names []string
	for _, i := range infos {
		names = append(names, shown(i.Qualified()))
	}
	where := "user scope"
	if sc.Kind == target.Project {
		where = "the project at " + shown(sc.ProjectDir)
	}
	hint := "profiles " + where + " can use: " + strings.Join(names, ", ")
	if len(names) == 0 {
		hint = "write one: " + shown(filepath.Join(e.Paths.UserProfiles, "<name>.yml"))
		if sc.Kind == target.Project {
			hint += ", or the project's: " + shown(filepath.Join(sc.ProjectDir, profile.ProjectDir, "<name>.yml"))
		}
		hint += "; lola profile --add fetches a remote one"
	}
	return profile.Info{}, &unknownProfileError{errs.WithHints(fmt.Errorf("no profile %s for %s", untrusted.Text(name, untrusted.MaxName), where), hint)}
}
