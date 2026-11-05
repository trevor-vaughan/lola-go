package engine

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/trust"
)

// Layer is one file of module entries a scope wants: the scope's base (project.yml, or default at
// user scope) or its active profile. Name is what lock.json's from and every notice call it:
// project.yml, or ledger.FromProfile of the profile's qualified name. Path is its file. A profile
// layer's entries come from profile.Entries, with relative local sources resolved against the
// profile's directory: made absolute for the user's own profiles, and kept relative to the project
// root, as project.yml's are, for a project's. Key is a project profile's name, its part of the
// trust record, "" for any other layer; read is the profile as read, whose bytes an edit
// (Profile.Edit) or a sync's pins (Profile.Pin) write; nil for the scope's manifest and a remote
// profile. member is set on a remote profile a project bundle names: its trust record entry, which
// records each of its entries in that bundle's part (Key).
type Layer struct {
	Name    string
	Path    string
	Profile bool
	Base    bool   // the scope's base: project.yml, or default at user scope
	Bundle  string // the qualified name of the bundle on that this member comes through, "" for none
	Key     string
	Entries []manifest.Entry
	read    *profile.Profile
	member  *trust.MemberView
	origin  *profile.Origin // a remote profile's source.json
	chain   []bundleRef     // the bundles this member comes through, the one on first (bundle.go)
	// expanded is, on an assembly's base layer, the project bundles the profile on expands to,
	// whether or not a member of theirs gives a layer (one whose members are all left out gives
	// none), so their member lists are asked about (trustParts) before a restore follows them.
	expanded []bundleRef
	// reached is, on an assembly's base layer, every profile and bundle the profile on reaches as a
	// member, nested ones and those that give no layer (an empty bundle, a member placed already)
	// included, by qualified name: the bundle files that list it, so a removal sees it is in use.
	reached map[string][]listing
	listed  []listing // a bundle's member: the bundle files that list it, each as it writes it
}

// Wanted is the entry of one module that a scope's layers want: the entry of the last layer that
// lists it, whole, From that layer's Name and Over the layers whose entry it replaced, lowest
// first. path is the winning layer's file, profile says it is a profile and key is a project
// profile's name (Layer.Key); member is the remote profile a project bundle names that the entry
// comes from (Layer.member), nil for any other.
type Wanted struct {
	manifest.Entry
	From     string
	Over     []string
	path     string
	profile  bool
	key      string
	member   *trust.MemberView
	inherits bool     // the entry installs to its project profile's top-level targets (Layer.inherits)
	bundle   string   // the winning layer's Bundle
	bundles  []string // the project bundles the winning layer comes through, by trust part (Layer.chain)
}

// inherits reports whether ent, an entry of l as the scope reads it, installs to l's top-level
// targets, which are trust content of a project profile: an entry of the profile's file without
// targets of its own. Only a project profile's entries do.
func (l Layer) inherits(ent manifest.Entry) bool {
	if l.Key == "" || l.read == nil || len(l.read.Targets) == 0 {
		return false
	}
	k := slices.IndexFunc(l.read.Modules, func(o manifest.Entry) bool { return o.Name == ent.Name })
	return k >= 0 && len(l.read.Modules[k].Targets) == 0
}

// projectBundles is the trust part of each project bundle l comes through.
func (l Layer) projectBundles() []string {
	var out []string
	for _, b := range l.chain {
		if b.key != "" {
			out = append(out, b.key)
		}
	}
	return out
}

// installsToTargets reports whether an entry layers load has no targets of its own, so it
// installs to project.yml's top-level targets.
func installsToTargets(layers []Layer) bool {
	want, _ := desired(layers)
	return slices.ContainsFunc(want, func(w Wanted) bool { return len(w.Targets) == 0 })
}

// own reports whether the entry is the user's own: from a profile of theirs, not the project's,
// trusted as all of yours is.
func (w *Wanted) own() bool { return w.profile && w.key == "" }

// desired is the set of modules a scope should have installed, in name order: the layers' entries,
// lowest layer first, an entry of a later layer replacing an earlier one of the same name whole.
// Each replacement by a different entry is a notice, which names the layers as layerLabel does
// without the profiles the scope can use.
func desired(layers []Layer) ([]Wanted, []Notice) { return desiredNoting(layers, nil, nil) }

// desiredNoting is desired with the replacement notices of the modules noted reports (all when
// noted is nil): an activation notes only the modules of the profiles it names. The notices name
// the layers as layerLabel does with infos, the profiles the scope can use.
func desiredNoting(layers []Layer, noted func(module string) bool, infos []profile.Info) ([]Wanted, []Notice) {
	won := map[string]*Wanted{}
	var notices []Notice
	for _, l := range layers {
		for _, ent := range l.Entries {
			w := &Wanted{Entry: ent, From: l.Name, path: l.Path, profile: l.Profile, key: l.Key, member: l.member, inherits: l.inherits(ent),
				bundle: l.Bundle, bundles: l.projectBundles()}
			w.Targets = slices.Clone(ent.Targets)
			if prev := won[ent.Name]; prev != nil {
				w.Over = append(slices.Clone(prev.Over), prev.From)
				if !sameEntry(prev.Entry, ent) && (noted == nil || noted(ent.Name)) {
					under := layerLabel(infos, prev.From)
					if l.Bundle != "" && prev.bundle == l.Bundle {
						under += " in bundle " + bundleLabel(infos, l.Bundle)
					}
					notices = append(notices, overrideNotice(ent, prev.Entry, layerLabel(infos, l.Name), under))
				}
			}
			won[ent.Name] = w
		}
	}
	out := make([]Wanted, 0, len(won))
	for _, w := range won {
		out = append(out, *w)
	}
	slices.SortFunc(out, func(a, b Wanted) int { return strings.Compare(a.Name, b.Name) })
	return out, notices
}

// sameEntry reports whether two entries for one module ask for the same thing.
func sameEntry(a, b manifest.Entry) bool {
	return a.Source == b.Source && a.Ref == b.Ref && a.Path == b.Path && a.Commit == b.Commit && a.Digest == b.Digest &&
		slices.Equal(a.Targets, b.Targets) && a.RulesOn() == b.RulesOn() && manifest.SameVars(a.Vars, b.Vars) &&
		slices.Equal(a.Only, b.Only) && slices.Equal(a.Skip, b.Skip)
}

// overrideNotice is the notice of a layer's entry replacing a lower layer's, each named by its
// label (layerLabel): "gopls: profile go overrides default (base) (v2.0 over v1.2)", the versions
// left out when they read alike; when the replaced entry set vars, or picks (only:, skip:), the
// winner does not repeat, it says they are dropped, as the winner replaces them whole.
func overrideNotice(ent, prev manifest.Entry, layer, under string) Notice {
	text := shown(ent.Name) + ": " + layer + " overrides " + under
	if now, was := entryVersion(ent), entryVersion(prev); now != was {
		text += " (" + now + " over " + was + ")"
	}
	if len(prev.Vars) > 0 && !manifest.SameVars(prev.Vars, ent.Vars) {
		sets := "none"
		if len(ent.Vars) > 0 {
			sets = "its own"
		}
		text += "; " + shown(ent.Name) + "'s vars from " + under + " are dropped: the entry from " + layer + " sets " + sets
	}
	if len(prev.Only)+len(prev.Skip) > 0 && (!sameSet(prev.Only, ent.Only) || !sameSet(prev.Skip, ent.Skip)) {
		sets := "none"
		if len(ent.Only)+len(ent.Skip) > 0 {
			sets = "its own"
		}
		text += "; " + shown(ent.Name) + "'s picks from " + under + " are dropped: the entry from " + layer + " sets " + sets
	}
	return Notice{Level: LevelWarn, Text: text}
}

// entryVersion names the version an entry asks for in a notice: its ref, else its pin, else its
// source.
func entryVersion(e manifest.Entry) string {
	commit := e.Commit
	if len(commit) > 12 {
		commit = commit[:12]
	}
	return shown(cmp.Or(e.Ref, commit, versionID(e.Digest), e.Source))
}

// find is the wanted entry named name, or nil.
func find(want []Wanted, name string) *Wanted {
	i := slices.IndexFunc(want, func(w Wanted) bool { return w.Name == name })
	if i < 0 {
		return nil
	}
	return &want[i]
}

// baseLayer is the scope's base as its lowest layer: project.yml at project scope, read as man; at
// user scope your default profile as loadStack read it, or an empty one when default.yml does not
// exist (an install writes it).
func (e *Engine) baseLayer(sc Scope, man *manifest.Manifest, pl *profileLayers) Layer {
	if sc.Kind == target.User {
		if pl.def != nil {
			l := *pl.def
			l.Base = true
			return l
		}
		return Layer{Name: ledger.FromProfile(profile.DefaultName), Path: e.defaultPath(), Profile: true, Base: true, Entries: []manifest.Entry{}}
	}
	path := e.files(sc).manifest
	return Layer{Name: filepath.Base(path), Path: path, Base: true, Entries: man.Clone().Modules}
}

// profileLayers is the profile layers a run reads, each once: default's, when its file exists, and
// the layers of each active profile it was given, by qualified name: the profile's own, or a
// bundle's members' in order. notices warns about the active profile whose file is gone and what
// expanding a bundle noted.
type profileLayers struct {
	def      *Layer
	named    map[string][]Layer
	expanded map[string][]bundleRef          // the project bundles each of named expands to (Layer.expanded)
	reached  map[string]map[string][]listing // what each of named reaches as a member (Layer.reached)
	notices  []Notice
}

// loadStack reads the profiles of a scope's stack: default (at user scope) and the active profile
// of each of actives, each once, so an activation's old and new profile share one parse. infos is
// the profiles sc can use when the caller listed them already, else nil. A name whose file is gone
// counts as off, with a warning when the last of actives, the one the run applies, is it; a
// bundle's remote member that this machine lacks and pins.yaml records is left out, with a warning
// (expansion.member); a profile that does not load refuses the run, but for a default.yml whose
// only fault is entries with a marketplace address, which comes back read beside its
// *manifest.UnresolvedError (layers reads it so for an install that replaces one). An active
// remote:<name> that a user profile of its name now shadows is read as that profile, with a notice.
// A project with no active profile in any of actives never lists the user's profiles directory, so
// a broken one cannot refuse every project.
func (e *Engine) loadStack(sc Scope, infos []profile.Info, actives ...*ledger.Active) (*profileLayers, error) {
	pl := &profileLayers{named: map[string][]Layer{}, expanded: map[string][]bundleRef{}, reached: map[string]map[string][]listing{}}
	if sc.Kind == target.Project && !slices.ContainsFunc(actives, func(a *ledger.Active) bool { return a != nil }) {
		return pl, nil
	}
	if infos == nil {
		var err error
		if infos, _, err = e.visible(sc); err != nil {
			return nil, err
		}
	}
	// Each remote profile is read once, whether it is on, a bundle's member, or both.
	remotes := map[string]Layer{}
	remote := func(i profile.Info) (Layer, *profile.Origin, error) {
		if l, ok := remotes[i.Qualified()]; ok {
			return l, l.origin, nil
		}
		if s, ok := e.staged[i.Path]; ok { // an update staged it, to be written after the receipt
			o := s.rp.origin
			l := Layer{Name: ledger.FromProfile(i.Qualified()), Path: i.Path, Profile: true, Entries: s.rp.entries, origin: &o}
			remotes[i.Qualified()] = l
			return l, &o, nil
		}
		p, o, err := profile.LoadRemote(i.Path)
		if errors.Is(err, fs.ErrNotExist) {
			return Layer{}, nil, err
		}
		if err != nil {
			return Layer{}, nil, e.readd(sc, i.Name, err)
		}
		// Every entry names a remote source: nothing to resolve beside it.
		l := Layer{Name: ledger.FromProfile(i.Qualified()), Path: i.Path, Profile: true, Entries: p.Entries(), origin: &o}
		remotes[i.Qualified()] = l
		return l, &o, nil
	}
	// left is the warnings about the remote members of a bundle that this machine lacks and
	// pins.yaml records: each is left out, as the active profile whose file is gone is, until a run
	// that needs it restores it (restoreRemotes).
	var left []Notice
	load := func(i profile.Info) (Layer, error) {
		if i.Kind == profile.KindRemote {
			l, _, err := remote(i)
			return l, err
		}
		p, base, err := e.readFile(i)
		// default.yml with marketplace addresses comes as read beside its error (Engine.Manifest).
		if _, unresolved := errors.AsType[*manifest.UnresolvedError](err); err != nil && (!unresolved || i.Kind != profile.KindDefault) {
			return Layer{}, e.mixedError(sc, infos, i, err)
		}
		if p.Shape() == profile.ShapeBundle {
			if i.Kind == profile.KindDefault {
				b := &baseBundleError{err: fmt.Errorf("%s:%d: default is the base and lists modules; to combine profiles make a bundle and switch to it",
					shown(i.Path), p.BundleLine()), hint: func(again []string) string {
					return "remove bundle: from " + shown(i.Path) + ", then switch to a bundle of them: " +
						e.bundleRun(Scope{Kind: target.User}, freeName(infos, "default-bundle"), p.Bundle, again)
				}}
				b.hinted = &errs.Error{Err: b, Hints: []string{b.hint(nil)}}
				return Layer{}, b.hinted
			}
			// A bundle lists no module: expand reads its members (bundle.go).
			return Layer{Name: ledger.FromProfile(i.Qualified()), Path: i.Path, Profile: true, read: p}, nil
		}
		kept := err
		ents := p.Entries()
		for k := range ents {
			ents[k].Source = besideProfile(ents[k].Source, base)
		}
		l := Layer{Name: ledger.FromProfile(i.Qualified()), Path: i.Path, Profile: true, Entries: ents, read: p}
		if i.Kind == profile.KindProject {
			l.Key = i.Name
		}
		return l, kept
	}
	var unresolved error
	if k := slices.IndexFunc(infos, func(i profile.Info) bool { return i.Kind == profile.KindDefault }); k >= 0 {
		l, err := load(infos[k])
		if _, ok := errors.AsType[*manifest.UnresolvedError](err); err != nil && !ok {
			return nil, err
		}
		pl.def, unresolved = &l, err
	}
	gone := map[string]bool{}
	for _, a := range actives {
		if a == nil {
			continue
		}
		if _, read := pl.named[a.Name]; read || gone[a.Name] {
			continue
		}
		k := activeInfo(infos, a.Name)
		if k < 0 {
			gone[a.Name] = true
			continue
		}
		x := expansion{e: e, sc: sc, infos: infos, load: load, remote: func(i profile.Info) (Layer, error) {
			l, _, err := remote(i)
			return l, err
		}, seen: map[string]bool{}, noted: map[string]bool{}}
		ls, err := x.expand(infos[k], nil, "")
		if err != nil {
			return nil, err
		}
		for j := range ls {
			ls[j].listed = x.listed[strings.TrimPrefix(ls[j].Name, ledger.FromProfile(""))]
		}
		pl.named[a.Name], pl.expanded[a.Name], pl.reached[a.Name], left = ls, x.project, x.listed, append(left, x.notices...)
	}
	if n := len(actives); n > 0 {
		pl.notices = e.activeNotices(sc, infos, actives[n-1])
	}
	pl.notices = append(left, pl.notices...)
	return pl, unresolved
}

// activeNotices is the warnings about the active profile a (nil for none) that every read of the
// scope's stack gives: its file is gone, and it counts as off, or a user profile of its name now
// shadows it and is read in its place.
func (e *Engine) activeNotices(sc Scope, infos []profile.Info, a *ledger.Active) []Notice {
	switch {
	case a == nil:
		return nil
	case activeInfo(infos, a.Name) < 0:
		return []Notice{e.goneProfile(sc, a.Name)}
	}
	if mine := userShadow(infos, a.Name); mine != nil {
		return []Notice{{Level: LevelWarn, Text: shown(mine.Qualified()) + " now replaces " + shown(a.Name)}}
	}
	return nil
}

// activeInfo is the index in infos of the profile read for the active profile q: the user profile
// that shadows it when q is remote and one does, else q's own; -1 when its file is gone.
func activeInfo(infos []profile.Info, q string) int {
	if mine := userShadow(infos, q); mine != nil {
		return slices.Index(infos, *mine)
	}
	return slices.IndexFunc(infos, func(i profile.Info) bool { return i.Qualified() == q })
}

// userShadow is the user profile of infos that shadows the active remote profile q: the user's own
// file of its name, which appeared after q was turned on and is used in its place; nil when q is
// not remote or no such file exists. A project profile never does: it replaces an active profile
// only when turned on.
func userShadow(infos []profile.Info, q string) *profile.Info {
	name, ok := strings.CutPrefix(q, string(profile.KindRemote)+":")
	if !ok {
		return nil
	}
	if k := slices.IndexFunc(infos, func(i profile.Info) bool { return i.Kind == profile.KindUser && i.Name == name }); k >= 0 {
		return &infos[k]
	}
	return nil
}

// shadowing is the local profile of infos that shadows i when i is a remote profile: the user's of
// its name, else the project's; nil when none does.
func shadowing(infos []profile.Info, i profile.Info) *profile.Info {
	if i.Kind != profile.KindRemote {
		return nil
	}
	for _, kind := range []profile.Kind{profile.KindUser, profile.KindProject} {
		if k := slices.IndexFunc(infos, func(o profile.Info) bool { return o.Kind == kind && o.Name == i.Name }); k >= 0 {
			return &infos[k]
		}
	}
	return nil
}

// assemble is the scope's layers with active on, lowest first: base, then the active profile (none
// when its file is gone), a bundle as its members in order, whose entry for a module wins over the
// base's, a later member's over an earlier one's.
func (pl *profileLayers) assemble(base Layer, active *ledger.Active) []Layer {
	out := []Layer{base}
	if active != nil {
		out[0].expanded = pl.expanded[active.Name]
		out[0].reached = pl.reached[active.Name]
		out = append(out, pl.named[active.Name]...)
	}
	return out
}

// layers is the scope's layers for the profile lock.json has on, lowest first: at user scope
// default (an empty one when its file is missing), then the active profile; at project scope
// project.yml, then the active profile. Each profile is read once, here, and its parse feeds the
// whole run; see loadStack.
func (e *Engine) layers(sc Scope, man *manifest.Manifest, led *ledger.Ledger) ([]Layer, []Notice, error) {
	pl, err := e.loadStack(sc, nil, led.Profile)
	if _, ok := errors.AsType[*manifest.UnresolvedError](err); ok && sc.Kind == target.User && holdsAddress(man) {
		// man is default.yml as written: an install of the run replaces one of its entries
		// (manifestReplacing), and writes the file as it reads it here.
		err = nil
	}
	if err != nil {
		return nil, nil, err
	}
	return pl.assemble(e.baseLayer(sc, man, pl), led.Profile), pl.notices, nil
}

// visible is the profiles sc can use, by name: the user's, with default at user scope
// (profile.Visible), at project scope the project's (profile.Project), and the remote ones
// (profile.Remote), in name order. No profile is read. notices are what a listing says about the
// directories: how many files have names that are not valid profile names, never the names, and a
// project's default.yml, which is not a profile.
func (e *Engine) visible(sc Scope) ([]profile.Info, []Notice, error) {
	infos, invalid, reserved, err := profile.Visible(e.Paths, sc.Kind == target.User)
	if err != nil {
		return nil, nil, err
	}
	notices := append(invalidNotices(e.Paths.UserProfiles, invalid), reservedNotices(reserved)...)
	if sc.Kind == target.Project {
		project, invalid, ignored, reserved, err := profile.Project(sc.ProjectDir)
		if err != nil {
			return nil, nil, err
		}
		notices = append(notices, invalidNotices(filepath.Join(sc.ProjectDir, profile.ProjectDir), invalid)...)
		if ignored != "" {
			notices = append(notices, Notice{Level: LevelWarn, Text: "a project cannot define default; rename " + shown(ignored)})
		}
		notices = append(notices, reservedNotices(reserved)...)
		infos = append(infos, project...)
	}
	remote, invalid, err := profile.Remote(e.Paths)
	if err != nil {
		return nil, nil, err
	}
	notices = append(notices, invalidNotices(e.Paths.RemoteProfiles, invalid)...)
	infos = append(infos, remote...)
	if e.creating != nil { // the files the run makes, listed as they will be
		for _, i := range e.creating.infos {
			if (i.Kind != profile.KindProject || sc.Kind == target.Project) && !slices.Contains(infos, i) {
				infos = append(infos, i)
			}
		}
	}
	slices.SortStableFunc(infos, func(a, b profile.Info) int { return strings.Compare(a.Name, b.Name) })
	return infos, notices, nil
}

// profilePath is the file a profile of kind and name is read from in sc (profile.Visible,
// profile.Project, profile.Remote), whether or not it exists.
func (e *Engine) profilePath(sc Scope, kind profile.Kind, name string) string {
	switch kind {
	case profile.KindProject:
		return filepath.Join(sc.ProjectDir, profile.ProjectDir, name+".yml")
	case profile.KindRemote:
		return filepath.Join(e.Paths.RemoteProfiles, name, profile.RemoteFile)
	}
	return filepath.Join(e.Paths.UserProfiles, name+".yml")
}

// reservedNotices is the warning about base.yml at path, a file in a profiles directory that is no
// profile: none when path is "".
func reservedNotices(path string) []Notice {
	if path == "" {
		return nil
	}
	return []Notice{{Level: LevelWarn, Text: "base is reserved for the scope's base; rename " + shown(path)}}
}

// invalidNotices is the warning about the n files in dir whose names are not valid profile
// names, if any.
func invalidNotices(dir string, n int) []Notice {
	switch {
	case n == 1:
		return []Notice{{Level: LevelWarn, Text: "1 file with an invalid name in " + shown(dir) + " is not a profile"}}
	case n > 1:
		return []Notice{{Level: LevelWarn, Text: fmt.Sprintf("%d files with invalid names in %s are not profiles", n, shown(dir))}}
	}
	return nil
}

// replacing is an activation's replacement of active user or remote profiles by project profiles of
// the same name: report is what the trust report shows of them. lola sync never makes one; only
// lola profile project:<name> --on, or --switch naming it, does.
type replacing struct {
	report []Replacement
}

// add puts the replacements in rep, a project scope's trust report: each is a change of its own,
// so a report with one is not trusted. A nil r adds nothing.
func (r *replacing) add(rep *TrustReport) {
	if r == nil {
		return
	}
	rep.Replacements = slices.Clone(r.report)
	if rep.Status == trust.StatusTrusted {
		rep.Status = trust.StatusChanged
	}
}

// replaced is the replacement an activation makes: old, yours or a remote profile, that next, the
// project's profile of its name, takes the place of, as {user:<name>, project:<name>}; none
// otherwise.
func replaced(old, next *ledger.Active) [][2]string {
	if old == nil || next == nil {
		return nil
	}
	kind, name, _ := strings.Cut(old.Name, ":")
	if q := string(profile.KindProject) + ":" + name; replaceable(kind) && next.Name == q {
		return [][2]string{{old.Name, q}}
	}
	return nil
}

// replacingOf is the report of the replacements pairs an activation makes (replaced), each with
// the modules whose winning entry it changes between old and want, the desired sets of the old
// and the new profile on; nil when there are none.
func replacingOf(pairs [][2]string, old, want []Wanted) *replacing {
	if len(pairs) == 0 {
		return nil
	}
	r := &replacing{}
	for _, p := range pairs {
		mods := []string{}
		for _, w := range want {
			if o := find(old, w.Name); w.From == ledger.FromProfile(p[1]) && (o == nil || o.From != w.From || !sameEntry(o.Entry, w.Entry)) {
				mods = append(mods, shown(w.Name))
			}
		}
		for _, o := range old {
			if w := find(want, o.Name); o.From == ledger.FromProfile(p[0]) && (w == nil || w.From != o.From) && !slices.Contains(mods, shown(o.Name)) {
				mods = append(mods, shown(o.Name))
			}
		}
		slices.Sort(mods)
		r.report = append(r.report, Replacement{Profile: shown(p[1]), Replaces: shown(p[0]), Modules: mods})
	}
	return r
}

// replaceable reports whether an active profile of kind gives way to a project profile of its name
// turned on in its place: the user's own and a remote one do.
func replaceable(kind string) bool {
	return kind == string(profile.KindUser) || kind == string(profile.KindRemote)
}

// replacements is, for the active profile at project scope, the project profile of infos that has
// its name when it is yours or a remote one, as {user:<name>, project:<name>}.
func replacements(infos []profile.Info, active *ledger.Active) [][2]string {
	if active == nil {
		return nil
	}
	kind, name, _ := strings.Cut(active.Name, ":")
	q := string(profile.KindProject) + ":" + name
	if replaceable(kind) && slices.ContainsFunc(infos, func(i profile.Info) bool { return i.Qualified() == q }) {
		return [][2]string{{active.Name, q}}
	}
	return nil
}

// replacementNotices is the notice of each project profile with the name of an active user or
// remote profile (replacements): lola never swaps them on its own, so it says how to.
func (e *Engine) replacementNotices(sc Scope, pending [][2]string) []Notice {
	var out []Notice
	for _, p := range pending {
		out = append(out, Notice{Level: LevelWarn, Text: shown(p[1]) + " also exists → " + e.Command(sc, "profile", p[1], "--on")})
	}
	return out
}

// syncLayers is layers for a sync, with a notice for each project profile that has the name of an
// active user or remote profile (replacements): a sync never swaps one for the other. Each profile
// is read once.
func (e *Engine) syncLayers(sc Scope, man *manifest.Manifest, led *ledger.Ledger) ([]Layer, []Notice, error) {
	if sc.Kind != target.Project || led.Profile == nil {
		return e.layers(sc, man, led)
	}
	infos, _, err := e.visible(sc)
	if err != nil {
		return nil, nil, err
	}
	pl, err := e.loadStack(sc, infos, led.Profile)
	if err != nil {
		return nil, nil, err
	}
	return pl.assemble(e.baseLayer(sc, man, pl), led.Profile), append(pl.notices, e.replacementNotices(sc, replacements(infos, led.Profile))...), nil
}

// besideProfile is a profile entry's source with a relative local path joined to dir, the profile's
// directory: absolute for a user's profile, relative to the project root for a project's. URLs,
// git's scp form, absolute paths and marketplace addresses (an unresolved entry an install
// replaces) are kept as written.
func besideProfile(src, dir string) string {
	if _, _, address := manifest.Address(src); address || strings.Contains(src, "://") || source.Host(src) != "" || filepath.IsAbs(src) {
		return src
	}
	return filepath.Join(dir, src)
}

// saveOnlyNotice is the warning about an entry an edit writes to the file labeled to for module,
// while the layer labeled winner, higher in the scope, lists it: the winner's entry applies until
// it is off.
func saveOnlyNotice(module, winner, to string) Notice {
	return Notice{Level: LevelWarn, Text: shown(module) + ": " + winner + "'s entry wins; this change to " + to + " applies when it is off"}
}

// targetNotices is what an install says of the entries written, t's: when the scope does not load
// t, that it is saved and how to switch to it (once), under -n that it would be, and with no switch
// for a profile the run makes (--new: it does not exist until the run writes it); for an entry a higher layer's wins over, the
// save-only warning; for one that replaces a lower layer's different entry, the override notice,
// whose modules it returns too (a write line names no override the notice names).
func (e *Engine) targetNotices(sc Scope, t writeTo, layers []Layer, written []manifest.Entry, infos []profile.Info, opts Options) ([]Notice, []string) {
	if !t.on {
		if len(written) == 0 {
			return nil, nil
		}
		text := "saved to " + t.label + "; it is not on " + scopeWhere(sc)
		switch {
		case opts.DryRun && opts.Target.New:
			text = "would be " + text
		case opts.DryRun:
			text = "would be " + text + " → " + e.switchRun(sc, t)
		default:
			text += " → " + e.switchRun(sc, t)
		}
		return []Notice{{Level: LevelInfo, Text: text}}, nil
	}
	at := slices.IndexFunc(layers, func(l Layer) bool { return l.Name == t.Name })
	below, _ := desired(layers[:max(at, 0)])
	var out []Notice
	var overridden []string
	for _, ent := range written {
		if l := winnerAbove(layers, t.Name, ent.Name); l != nil {
			out = append(out, saveOnlyNotice(ent.Name, layerLabel(infos, l.Name), t.label))
			continue
		}
		if w := find(below, ent.Name); w != nil && !sameEntry(w.Entry, ent) {
			out = append(out, overrideNotice(ent, w.Entry, t.label, layerLabel(infos, w.From)))
			overridden = append(overridden, ent.Name)
		}
	}
	return out, overridden
}
