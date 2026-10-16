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
// active list, nil when the profile is off or is default, which is always on. Source,
// ShadowedBy and Extends belong to remote profiles and extends:, which a later plan adds: they
// are "", nil and [] here. Every string has been through untrusted.Text.
type ProfileRow struct {
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	Path       string   `json:"path"`
	Source     string   `json:"source"`
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
// modules with the layer whose entry wins there.
type ProfileState struct {
	ProfileRow
	Modules []ProfileModule `json:"modules"`
}

// ProfileModule is a module a profile lists, with the layer whose entry the scope uses (From)
// and the layers that entry replaced (Overrides), lowest first.
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
// place in the scope's active list. It reads the profiles directory's listing and lock.json, no
// profile file (§19 S8). An active profile whose file is gone is a notice, as in a sync (§10),
// and files whose names are not valid profile names are a count, never shown (§4.3, §19 S7).
func (e *Engine) Profiles(ctx context.Context, sc Scope) ([]ProfileRow, []Notice, error) {
	infos, listing, led, notices, err := e.profileScope(ctx, sc)
	if err != nil {
		return nil, nil, err
	}
	rows := make([]ProfileRow, 0, len(infos))
	for _, i := range infos {
		rows = append(rows, e.profileRow(sc, i, led))
	}
	for _, a := range led.Profiles {
		if !slices.ContainsFunc(infos, func(i profile.Info) bool { return i.Qualified() == a.Name }) {
			notices = append(notices, e.goneProfile(sc, a.Name))
		}
	}
	if sc.Kind == target.Project {
		notices = append(notices, e.replacementNotices(sc, replacements(infos, led.Profiles))...)
	}
	return rows, append(notices, listing...), nil
}

// ProfileNames is the names of the profiles you can turn on or off, for completion: your own
// profiles (default is always on), by the listing of their directory alone.
func (e *Engine) ProfileNames() ([]string, error) {
	infos, _, err := profile.Visible(e.Paths, false)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(infos))
	for k, i := range infos {
		out[k] = i.Name
	}
	return out, nil
}

// profileRow is i's row in sc, whose lock.json is led.
func (e *Engine) profileRow(sc Scope, i profile.Info, led *ledger.Ledger) ProfileRow {
	row := ProfileRow{Name: shown(i.Name), Kind: string(i.Kind), Path: shown(i.Path), Scope: sc.Kind.String(), Extends: []string{},
		Qualified: shown(i.Qualified()), Root: shown(root(sc)), Of: len(led.Profiles)}
	if i.Kind == profile.KindDefault {
		row.Active = true
		return row
	}
	if k := slices.IndexFunc(led.Profiles, func(a ledger.Active) bool { return a.Name == i.Qualified() }); k >= 0 {
		pos := k + 1
		row.Active, row.Position = true, &pos
	}
	return row
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
	st := &ProfileState{ProfileRow: e.profileRow(sc, i, led), Modules: []ProfileModule{}}
	view := led.Clone()
	if i.Kind != profile.KindDefault && !st.Active {
		view.Profiles = append(view.Profiles, ledger.Active{Name: i.Qualified(), By: ledger.ByOn})
	}
	pl, err := e.loadProfiles(sc, infos, view.Profiles)
	if err != nil {
		return nil, nil, err
	}
	var own Layer
	var read bool
	if i.Kind == profile.KindDefault {
		if read = pl.def != nil; read {
			own = *pl.def
		}
	} else {
		own, read = pl.named[i.Qualified()]
	}
	if !read {
		// infos holds i, so loadProfiles read it or failed; this guards that rather than index on it.
		return nil, nil, errs.WithHints(fmt.Errorf("profile %s no longer exists", shown(i.Qualified())), "see the profiles: "+e.Command(sc, "profile"))
	}
	want, _ := desired(pl.assemble(e.manifestLayer(sc, man), view.Profiles))
	for _, ent := range own.Entries {
		w := find(want, ent.Name)
		over := []string{}
		for _, o := range w.Over {
			over = append(over, shown(o))
		}
		st.Modules = append(st.Modules, ProfileModule{Name: shown(ent.Name), From: shown(w.From), Overrides: over})
	}
	slices.SortFunc(st.Modules, func(a, b ProfileModule) int { return strings.Compare(a.Name, b.Name) })
	return st, append(notices, pl.notices...), nil
}

// ownedBy is whose a profile of kind is, as an ambiguous name's refusal says it: "your profile"
// or "the project's", or as a hint's label, "yours" or "the project's".
func ownedBy(kind profile.Kind, label bool) string {
	switch {
	case kind == profile.KindUser && label:
		return "yours"
	case kind == profile.KindUser:
		return "your profile"
	case kind == profile.KindProject:
		return "the project's"
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

// ambiguousProfileError is a bare profile name both a user and a project profile define (D8),
// with the two profiles, so --off can say which of them is on.
type ambiguousProfileError struct {
	error
	found []profile.Info
}

func (e *ambiguousProfileError) Unwrap() error { return e.error }

// resolveProfile is the profile a name typed on the command line names in sc (Plan 6b §4.4),
// among infos, the profiles sc can use: a bare name is, at project scope, the project's profile
// or the user's of that name, and at user scope the user's (remote profiles, which it would also
// look in, come with Plan 6b.3b); a bare name both a project and a user profile define is
// ambiguous and refused, naming both profiles and their files and hinting, for each, the user's
// command with the name qualified (D8); default is default.yml, at user scope only; a qualified
// name (user:go, project:go) names its kind. Any other name is refused, listing the profiles sc
// can use; remote: names are refused so until their kind is read (§19 P8). args is what the
// user typed after lola profile, the names and the action flag, or nil for the state form of a
// single name.
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
			if i.Name == bare && i.Kind != profile.KindDefault && (kind == "" || kind == string(i.Kind)) {
				found = append(found, i)
			}
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 2:
		var named, hints []string
		for _, i := range found {
			named = append(named, ownedBy(i.Kind, false)+" "+shown(i.Qualified())+" ("+shown(i.Path)+")")
			hints = append(hints, ownedBy(i.Kind, true)+": "+e.Command(sc, "profile", qualifiedArgs(args, name, i.Qualified())...))
		}
		return profile.Info{}, &ambiguousProfileError{errs.WithHints(fmt.Errorf("%s is ambiguous: %s and %s are both named %s", shown(bare),
			named[0], named[1], shown(bare)), hints...), found}
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
	}
	return profile.Info{}, &unknownProfileError{errs.WithHints(fmt.Errorf("no profile %s for %s", untrusted.Text(name, untrusted.MaxName), where), hint)}
}
