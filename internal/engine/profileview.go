package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// ProfileRow is one profile a scope can use, as lola profile lists it (Plan 6b §12.2). Name is
// the profile's own name and Kind where it comes from; Position counts from 1 in the scope's
// active list, nil when the profile is off or is default, which is always on. A remote
// profile's Source, Ref, Commit and Digest are its source.json's: where it came from and the pin
// you consented to ("" for a local profile, and for a remote one whose
// source.json cannot be read, which a notice reports); ShadowedBy is the local profile of its
// name that shadows it, nil when none does (§4.4). Extends is the remote profiles the profile
// extends, by qualified name, in a profile's state; a listing reads no profile file, so it is []
// there. Every string has been through untrusted.Text.
type ProfileRow struct {
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	Path       string   `json:"path"`
	Source     string   `json:"source"`
	Ref        string   `json:"ref"`
	Commit     string   `json:"commit"`
	Digest     string   `json:"digest"`
	Active     bool     `json:"active"`
	Position   *int     `json:"position"`
	Scope      string   `json:"scope"`
	ShadowedBy *string  `json:"shadowed_by"`
	Extends    []string `json:"extends"`
	Qualified  string   `json:"-"`
	Root       string   `json:"-"`
	Of         int      `json:"-"` // the length of the scope's active list
}

// ProfileState is one profile's state in a scope (Plan 6b §12.1, §12.2): its row and each of its
// modules with the layer whose entry wins there. ReadAs is, for an active remote profile, the
// user profile of its name read in its place (D8), nil otherwise: a project profile that shadows
// it takes its place only when turned on. ShadowPosition is that project profile's place in the
// active list when it is on too (both are layers, the later one winning), nil when it is off.
type ProfileState struct {
	ProfileRow
	ReadAs         *string         `json:"read_as"`
	ShadowPosition *int            `json:"-"`
	Modules        []ProfileModule `json:"modules"`
}

// ProfileModule is a module a profile lists, with the layer whose entry the scope uses (From)
// and the layers that entry replaced (Overrides), lowest first; From is "" when no layer of the
// scope wants it (a remote profile's module while a local profile shadows it).
type ProfileModule struct {
	Name      string   `json:"name"`
	From      string   `json:"from"`
	Overrides []string `json:"overrides"`
}

// Ordinal is n as a position in a list: 1st, 2nd, 3rd, 4th, 11th, 21st.
func Ordinal(n int) string {
	suffix := "th"
	switch {
	case n%100 >= 11 && n%100 <= 13:
	case n%10 == 1:
		suffix = "st"
	case n%10 == 2:
		suffix = "nd"
	case n%10 == 3:
		suffix = "rd"
	}
	return strconv.Itoa(n) + suffix
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

// Profiles is every profile sc can use, in name order (Plan 6b §3.2 "lola profile"), with its
// place in the scope's active list. It reads the profiles directories' listings, lock.json and
// each remote profile's source.json, no profile file (§19 S8). An active profile whose file is
// gone is a notice, as in a sync (§10), and so is a remote profile a user profile now shadows;
// files whose names are not valid profile names are a count, never shown (§4.3, §19 S7).
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
	for _, i := range infos {
		row, note := e.profileRow(sc, infos, i, led)
		rows = append(rows, row)
		if note != nil {
			notices = append(notices, *note)
		}
	}
	for _, a := range led.Profiles {
		switch mine := userShadow(infos, a.Name); {
		case mine != nil:
			notices = append(notices, Notice{Level: LevelWarn, Text: shown(mine.Qualified()) + " now replaces " + shown(a.Name)})
		case !slices.ContainsFunc(infos, func(i profile.Info) bool { return i.Qualified() == a.Name }):
			notices = append(notices, e.goneProfile(sc, a.Name))
		}
	}
	if sc.Kind == target.Project {
		notices = append(notices, e.replacementNotices(sc, replacements(infos, led.Profiles))...)
	}
	return rows, append(notices, listing...), nil
}

// ProfileNames is the names of the profiles you can turn on or off, for completion: your own
// profiles (default is always on), then the remote ones no profile of yours shadows, by the
// listings of their directories alone.
func (e *Engine) ProfileNames() ([]string, error) {
	infos, _, err := profile.Visible(e.Paths, false)
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

// profileRow is i's row in sc, whose lock.json is led and whose profiles are infos, with a
// warning when i is a remote profile whose source.json cannot be read.
func (e *Engine) profileRow(sc Scope, infos []profile.Info, i profile.Info, led *ledger.Ledger) (ProfileRow, *Notice) {
	row := ProfileRow{Name: shown(i.Name), Kind: string(i.Kind), Path: shown(i.Path), Scope: sc.Kind.String(), Extends: []string{},
		Qualified: shown(i.Qualified()), Root: shown(root(sc)), Of: len(led.Profiles)}
	var note *Notice
	if i.Kind == profile.KindRemote {
		o, err := profile.ReadOrigin(filepath.Dir(i.Path))
		if err != nil {
			note = &Notice{Level: LevelWarn, Text: shown(i.Qualified()) + ": " + shown(untrusted.CleanErr(err).Error())}
		}
		row.Source, row.Ref, row.Commit, row.Digest = shown(o.Source), shown(o.Ref), shown(o.Commit), shown(o.Digest)
		if sh := shadowing(infos, i); sh != nil {
			by := shown(sh.Qualified())
			row.ShadowedBy = &by
		}
	}
	if i.Kind == profile.KindDefault {
		row.Active = true
		return row, note
	}
	if k := slices.IndexFunc(led.Profiles, func(a ledger.Active) bool { return a.Name == i.Qualified() }); k >= 0 {
		pos := k + 1
		row.Active, row.Position = true, &pos
	}
	return row, note
}

// goneProfile is the warning about an active profile whose file is gone (Plan 6b §10).
func (e *Engine) goneProfile(sc Scope, q string) Notice {
	return Notice{Level: LevelWarn, Text: "profile " + shown(q) + " is on but no longer exists → " + e.Command(sc, "profile", q, "--off")}
}

// ProfileState is the state of the profile name names in each of scopes that has it (Plan 6b
// §12.1): its row and, for each module it lists, the layer whose entry wins in that scope; for a
// profile that is off, the layer that would win were it turned on, appended to the active list as
// --on does. A scope the name does not resolve in is left out (default at project scope, say);
// when it resolves in none, the first scope's refusal is returned. The named profile is read, and
// the scope's active ones, once each, as a sync reads them.
func (e *Engine) ProfileState(ctx context.Context, scopes []Scope, name string) ([]ProfileState, []Notice, error) {
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
	row, note := e.profileRow(sc, infos, i, led)
	if note != nil {
		notices = append(notices, *note)
	}
	st := &ProfileState{ProfileRow: row, Modules: []ProfileModule{}}
	view := led.Clone()
	// A remote profile a local one shadows cannot be turned on (§4.4), and one that is on is read
	// as the local one (D8): either is shown as it stands, its own entries.
	shadowed := i.Kind == profile.KindRemote && st.ShadowedBy != nil
	if shadowed && st.Active {
		at := slices.IndexFunc(led.Profiles, func(a ledger.Active) bool { return shown(a.Name) == *st.ShadowedBy })
		switch mine := userShadow(infos, i.Qualified()); {
		case mine != nil:
			by := shown(mine.Qualified())
			st.ReadAs = &by
		case at >= 0:
			pos := at + 1
			st.ShadowPosition = &pos
		default:
			notices = append(notices, Notice{Level: LevelInfo, Text: "to use " + *st.ShadowedBy + " instead: " +
				e.Command(sc, "profile", *st.ShadowedBy, "--on")})
		}
	}
	if i.Kind != profile.KindDefault && !st.Active && !shadowed {
		view.Profiles = append(view.Profiles, ledger.Active{Name: i.Qualified(), By: ledger.ByOn})
	}
	pl, err := e.loadProfiles(sc, infos, view.Profiles)
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
	default:
		own, read = pl.named[i.Qualified()]
	}
	if !read {
		// infos holds i, so loadProfiles read it or failed; this guards that rather than index on it.
		return nil, nil, errs.WithHints(fmt.Errorf("profile %s no longer exists", shown(i.Qualified())), "see the profiles: "+e.Command(sc, "profile"))
	}
	for _, x := range own.extends {
		st.Extends = append(st.Extends, shown(x))
	}
	want, _ := desired(pl.assemble(e.manifestLayer(sc, man), view.Profiles))
	for _, ent := range own.Entries {
		m := ProfileModule{Name: shown(ent.Name), Overrides: []string{}}
		if w := find(want, ent.Name); w != nil {
			m.From = shown(w.From)
			for _, o := range w.Over {
				m.Overrides = append(m.Overrides, shown(o))
			}
		}
		st.Modules = append(st.Modules, m)
	}
	slices.SortFunc(st.Modules, func(a, b ProfileModule) int { return strings.Compare(a.Name, b.Name) })
	return st, append(notices, pl.notices...), nil
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
