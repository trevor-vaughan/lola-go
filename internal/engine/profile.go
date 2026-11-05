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
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Layer is one file of module entries a scope wants (Plan 6b §6.2): a profile or the scope's
// manifest. Name is what lock.json's from and every notice call it: the manifest's base name
// (project.yml, user.yml) or ledger.FromProfile of the profile's qualified name. Path is its
// file. A profile layer's entries come from profile.Entries, with relative local sources resolved
// against the profile's directory (§19 R5): made absolute for the user's own profiles, and kept
// relative to the project root, as project.yml's are, for a project's. Key is a project
// profile's name, its part of the trust record (§8.1), and read its parse, whose bytes a sync
// writes pins into (§6.4); "" and nil for any other layer. via names, for each entry of a
// profile that comes from a remote profile it extends, that remote profile (§4.3, §8.1); extends
// is the remote profiles it extends, by qualified name; over is, for each entry that replaced an
// extended one inside the layer, the layers it replaced there, lowest first (§4.3: no notice, but
// a profile's state and an activation's changes name them).
type Layer struct {
	Name    string
	Path    string
	Profile bool
	Key     string
	Entries []manifest.Entry
	read    *profile.Profile
	via     map[string]*trust.ExtendsView
	extends []string
	over    map[string][]string
	origin  *profile.Origin // a remote profile's source.json
}

// Wanted is the entry of one module that a scope's layers want: the entry of the last layer
// that lists it, whole (§6.2), From that layer's Name and Over the layers whose entry it
// replaced, lowest first. path is the winning layer's file, profile says it is a profile and key
// is a project profile's name (Layer.Key); via is the remote profile the entry comes from through
// the winning profile's extends:, nil when it is the profile's own (Layer.via).
type Wanted struct {
	manifest.Entry
	From    string
	Over    []string
	path    string
	profile bool
	key     string
	via     *trust.ExtendsView
}

// view is the entry as the trust record holds it: with the remote profile it comes from.
func (w *Wanted) view() trust.Entry {
	v := trust.EntryOf(w.Entry)
	v.Extends = w.via
	return v
}

// own reports whether the entry is the user's own: from a profile of theirs, not the project's,
// trusted as user.yml is (Plan 6b §8.2, §4.3).
func (w *Wanted) own() bool { return w.profile && w.key == "" }

// desired is the set of modules a scope should have installed, in name order (Plan 6b §6.2): the
// layers' entries, lowest layer first, an entry of a later layer replacing an earlier one of the
// same name whole. Each replacement by a different entry is a notice.
func desired(layers []Layer) ([]Wanted, []Notice) { return desiredNoting(layers, nil) }

// desiredNoting is desired with the replacement notices of the modules noted reports (all when
// noted is nil): an activation notes only the modules of the profiles it names.
func desiredNoting(layers []Layer, noted func(module string) bool) ([]Wanted, []Notice) {
	won := map[string]*Wanted{}
	var notices []Notice
	for _, l := range layers {
		for _, ent := range l.Entries {
			w := &Wanted{Entry: ent, From: l.Name, path: l.Path, profile: l.Profile, key: l.Key, via: l.via[ent.Name]}
			w.Targets = slices.Clone(ent.Targets)
			if prev := won[ent.Name]; prev != nil {
				w.Over = append(slices.Clone(prev.Over), prev.From)
				if !sameEntry(prev.Entry, ent) && (noted == nil || noted(ent.Name)) {
					notices = append(notices, overrideNotice(ent, prev.Entry, l.Name, prev.From))
				}
			}
			w.Over = append(w.Over, l.over[ent.Name]...)
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
		slices.Equal(a.Targets, b.Targets) && a.RulesOn() == b.RulesOn() && manifest.SameVars(a.Vars, b.Vars)
}

// overrideNotice is the notice of a layer's entry replacing a lower layer's (Plan 6b §6.2):
// "gopls: project.yml overrides profile:user:go (v2.0 over v1.2)", the versions left out when
// they read alike; when the replaced entry set vars the winner does not repeat, it says they are
// dropped, as the winner replaces them whole (Plan 7 §4.2).
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

// manifestLayer is the scope's manifest as its top layer.
func (e *Engine) manifestLayer(sc Scope, man *manifest.Manifest) Layer {
	path := e.files(sc).manifest
	return Layer{Name: filepath.Base(path), Path: path, Entries: man.Clone().Modules}
}

// profileLayers is the profile layers a run reads, each once (Plan 6b §19 S6): default's, when
// its file exists, and each listed active profile's, by qualified name. notices warns about the
// listed names whose file is gone (§10).
type profileLayers struct {
	def     *Layer
	named   map[string]Layer
	notices []Notice
}

// loadProfiles reads the profiles of the scope's active lists: default (at user scope) and every
// name any of lists holds, each once, so an activation's old and new lists share one parse. infos
// is the profiles sc can use when the caller listed them already, else nil. A name whose file is
// gone counts as off, with a warning when the last of lists, the one the run applies, holds it;
// a profile that does not load refuses the run. An active remote:<name> that a user profile of
// its name now shadows is read as that profile, with a notice (Plan 6b §4.4, D8). A project with
// no active profile in any list never lists the user's profiles directory, so a broken one cannot
// refuse every project.
func (e *Engine) loadProfiles(sc Scope, infos []profile.Info, lists ...[]ledger.Active) (*profileLayers, error) {
	pl := &profileLayers{named: map[string]Layer{}}
	if sc.Kind == target.Project && !slices.ContainsFunc(lists, func(l []ledger.Active) bool { return len(l) > 0 }) {
		return pl, nil
	}
	if infos == nil {
		var err error
		if infos, _, err = e.visible(sc); err != nil {
			return nil, err
		}
	}
	// Each remote profile is read once, whether it is active, extended, or both.
	remotes := map[string]Layer{}
	remote := func(i profile.Info) (Layer, *profile.Origin, error) {
		if l, ok := remotes[i.Qualified()]; ok {
			return l, l.origin, nil
		}
		p, o, err := profile.LoadRemote(i.Path)
		if errors.Is(err, fs.ErrNotExist) {
			return Layer{}, nil, err
		}
		if err != nil {
			return Layer{}, nil, e.readd(sc, i.Name, err)
		}
		// Every entry names a remote source (Plan 6b D4): nothing to resolve beside it.
		l := Layer{Name: ledger.FromProfile(i.Qualified()), Path: i.Path, Profile: true, Entries: p.Entries(), origin: &o}
		remotes[i.Qualified()] = l
		return l, &o, nil
	}
	// extend puts the entries of the remote profiles p extends before ents, its own, each later
	// entry replacing an earlier one of its name, and gives those without targets p's (§4.3).
	extend := func(path string, p *profile.Profile, ents []manifest.Entry) (Layer, error) {
		l := Layer{Entries: []manifest.Entry{}}
		put := func(ent manifest.Entry, via *trust.ExtendsView) {
			if len(ent.Targets) == 0 {
				ent.Targets = slices.Clone(p.Targets)
			}
			if k := slices.IndexFunc(l.Entries, func(o manifest.Entry) bool { return o.Name == ent.Name }); k >= 0 {
				if old := l.via[ent.Name]; old != nil {
					if l.over == nil {
						l.over = map[string][]string{}
					}
					l.over[ent.Name] = append(l.over[ent.Name], ledger.FromProfile(old.Name))
				}
				l.Entries[k] = ent
			} else {
				l.Entries = append(l.Entries, ent)
			}
			if via == nil {
				delete(l.via, ent.Name)
				return
			}
			if l.via == nil {
				l.via = map[string]*trust.ExtendsView{}
			}
			l.via[ent.Name] = via
		}
		for k, name := range p.Extends {
			at := slices.IndexFunc(infos, func(i profile.Info) bool { return i.Kind == profile.KindRemote && i.Name == name })
			if at < 0 {
				return Layer{}, p.ExtendsError(path, k)
			}
			r, o, err := remote(infos[at])
			if err != nil {
				return Layer{}, err
			}
			via := &trust.ExtendsView{Name: infos[at].Qualified(), Source: o.Source, Commit: o.Commit, Digest: o.Digest}
			for _, ent := range r.Entries {
				put(ent, via)
			}
			l.extends = append(l.extends, infos[at].Qualified())
		}
		for _, ent := range ents {
			put(ent, nil)
		}
		return l, nil
	}
	load := func(i profile.Info) (Layer, error) {
		if i.Kind == profile.KindRemote {
			l, _, err := remote(i)
			return l, err
		}
		var p *profile.Profile
		var err error
		base := filepath.Dir(i.Path)
		if i.Kind == profile.KindProject {
			p, err = profile.LoadProject(i.Path)
			base = profile.ProjectDir
		} else {
			p, err = profile.Load(i.Path)
		}
		if err != nil {
			return Layer{}, err
		}
		ents := p.Entries()
		for k := range ents {
			ents[k].Source = besideProfile(ents[k].Source, base)
		}
		l, err := extend(i.Path, p, ents)
		if err != nil {
			return Layer{}, err
		}
		l.Name, l.Path, l.Profile = ledger.FromProfile(i.Qualified()), i.Path, true
		if i.Kind == profile.KindProject {
			l.Key, l.read = i.Name, p
		}
		return l, nil
	}
	if k := slices.IndexFunc(infos, func(i profile.Info) bool { return i.Kind == profile.KindDefault }); k >= 0 {
		l, err := load(infos[k])
		if err != nil {
			return nil, err
		}
		pl.def = &l
	}
	gone := map[string]bool{}
	var shadowed []string
	for _, list := range lists {
		for _, a := range list {
			if _, read := pl.named[a.Name]; read || gone[a.Name] {
				continue
			}
			k := slices.IndexFunc(infos, func(i profile.Info) bool { return i.Qualified() == a.Name })
			if mine := userShadow(infos, a.Name); mine != nil {
				k = slices.Index(infos, *mine)
				shadowed = append(shadowed, a.Name)
			}
			if k < 0 {
				gone[a.Name] = true
				continue
			}
			l, err := load(infos[k])
			if err != nil {
				return nil, err
			}
			pl.named[a.Name] = l
		}
	}
	if len(lists) > 0 {
		for _, a := range lists[len(lists)-1] {
			switch {
			case gone[a.Name]:
				pl.notices = append(pl.notices, e.goneProfile(sc, a.Name))
			case slices.Contains(shadowed, a.Name):
				pl.notices = append(pl.notices, Notice{Level: LevelWarn, Text: shown(userShadow(infos, a.Name).Qualified()) + " now replaces " + shown(a.Name)})
			}
		}
	}
	return pl, nil
}

// userShadow is the user profile of infos that shadows the active remote profile q (Plan 6b
// §4.4, D8): the user's own file of its name, which appeared after q was turned on and is used in
// its place; nil when q is not remote or no such file exists. A project profile never does: it
// replaces an active profile only when turned on (ruling R-6b3a-1).
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

// shadowing is the local profile of infos that shadows i when i is a remote profile (Plan 6b
// §4.4, decision 4): the user's of its name, else the project's; nil when none does.
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

// assemble is the scope's layers for the active list active, lowest first (Plan 6b §6.2, D6):
// default (when read), the active profiles in order (a gone one left out, and a profile read
// twice, once in place of a remote one it shadows, kept at its first place), then top, the
// scope's manifest.
func (pl *profileLayers) assemble(top Layer, active []ledger.Active) []Layer {
	var out []Layer
	if pl.def != nil {
		out = append(out, *pl.def)
	}
	for _, a := range active {
		if l, ok := pl.named[a.Name]; ok && !slices.ContainsFunc(out, func(o Layer) bool { return o.Name == l.Name }) {
			out = append(out, l)
		}
	}
	return append(out, top)
}

// layers is the scope's layers for the active list lock.json holds, lowest first (Plan 6b §6.2,
// D6): at user scope default (when its file exists), then the active profiles in order, then
// user.yml; at project scope the active profiles, then project.yml. Each profile is read once,
// here, and its parse feeds the whole run (§19 S6); see loadProfiles.
func (e *Engine) layers(sc Scope, man *manifest.Manifest, led *ledger.Ledger) ([]Layer, []Notice, error) {
	pl, err := e.loadProfiles(sc, nil, led.Profiles)
	if err != nil {
		return nil, nil, err
	}
	return pl.assemble(e.manifestLayer(sc, man), led.Profiles), pl.notices, nil
}

// visible is the profiles sc can use, by name (Plan 6b §4.1): the user's, with default at user
// scope (profile.Visible), at project scope the project's (profile.Project), and the remote ones
// (profile.Remote), in name order. No profile is read. notices are what a listing says about the
// directories: how many files have names that are not valid profile names, never the names (§4.3,
// §19 S7), and a project's default.yml, which is not a profile.
func (e *Engine) visible(sc Scope) ([]profile.Info, []Notice, error) {
	infos, invalid, err := profile.Visible(e.Paths, sc.Kind == target.User)
	if err != nil {
		return nil, nil, err
	}
	notices := invalidNotices(e.Paths.UserProfiles, invalid)
	if sc.Kind == target.Project {
		project, invalid, ignored, err := profile.Project(sc.ProjectDir)
		if err != nil {
			return nil, nil, err
		}
		notices = append(notices, invalidNotices(filepath.Join(sc.ProjectDir, profile.ProjectDir), invalid)...)
		if ignored != "" {
			notices = append(notices, Notice{Level: LevelWarn, Text: "a project cannot define default; rename " + shown(ignored)})
		}
		infos = append(infos, project...)
	}
	remote, invalid, err := profile.Remote(e.Paths)
	if err != nil {
		return nil, nil, err
	}
	notices = append(notices, invalidNotices(e.Paths.RemoteProfiles, invalid)...)
	infos = append(infos, remote...)
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

// replacing is an activation's replacement of active user or remote profiles by project profiles
// of the same name (Plan 6b §4.4, D8; ruling R-6b3a-1): report is what the trust report shows of
// them.
// lola sync never makes one; only lola profile project:<name> --on, or --switch naming it, does.
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

// replaced is the replacements an activation makes (D8): each active user:<name> or
// remote:<name> of old that the new list drops while it gains project:<name>, as {user:<name>,
// project:<name>}.
func replaced(old, list []ledger.Active) [][2]string {
	has := func(l []ledger.Active, q string) bool {
		return slices.ContainsFunc(l, func(a ledger.Active) bool { return a.Name == q })
	}
	var out [][2]string
	for _, a := range old {
		kind, name, _ := strings.Cut(a.Name, ":")
		q := string(profile.KindProject) + ":" + name
		if replaceable(kind) && !has(list, a.Name) && has(list, q) && !has(old, q) {
			out = append(out, [2]string{a.Name, q})
		}
	}
	return out
}

// replacingOf is the report of the replacements pairs an activation makes (replaced), each with
// the modules whose winning entry it changes between old and want, the desired sets of the old
// and the new list; nil when there are none.
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

// replaceable reports whether an active profile of kind gives way to a project profile of its
// name turned on in its place (Plan 6b §4.4, D8): the user's own and a remote one do.
func replaceable(kind string) bool {
	return kind == string(profile.KindUser) || kind == string(profile.KindRemote)
}

// replacements is, for the active list active at project scope, each project profile of infos
// that has the name of an active user or remote profile and is not active itself (Plan 6b §4.4,
// D8), as {user:<name>, project:<name>}.
func replacements(infos []profile.Info, active []ledger.Active) [][2]string {
	var out [][2]string
	for _, a := range active {
		kind, name, _ := strings.Cut(a.Name, ":")
		if !replaceable(kind) {
			continue
		}
		q := string(profile.KindProject) + ":" + name
		if slices.ContainsFunc(infos, func(i profile.Info) bool { return i.Qualified() == q }) &&
			!slices.ContainsFunc(active, func(o ledger.Active) bool { return o.Name == q }) {
			out = append(out, [2]string{a.Name, q})
		}
	}
	return out
}

// replacementNotices is the notice of each project profile with the name of an active user or
// remote profile (replacements): lola never swaps them on its own, so it says how to (R-6b3a-1).
func (e *Engine) replacementNotices(sc Scope, pending [][2]string) []Notice {
	var out []Notice
	for _, p := range pending {
		out = append(out, Notice{Level: LevelWarn, Text: shown(p[1]) + " also exists → " + e.Command(sc, "profile", p[1], "--on")})
	}
	return out
}

// syncLayers is layers for a sync, with a notice for each project profile that has the name of
// an active user or remote profile (replacements): a sync never swaps one for the other (Plan 6b §4.4,
// ruling R-6b3a-1). Each profile is read once.
func (e *Engine) syncLayers(sc Scope, man *manifest.Manifest, led *ledger.Ledger) ([]Layer, []Notice, error) {
	if sc.Kind != target.Project || len(led.Profiles) == 0 {
		return e.layers(sc, man, led)
	}
	infos, _, err := e.visible(sc)
	if err != nil {
		return nil, nil, err
	}
	pl, err := e.loadProfiles(sc, infos, led.Profiles)
	if err != nil {
		return nil, nil, err
	}
	return pl.assemble(e.manifestLayer(sc, man), led.Profiles), append(pl.notices, e.replacementNotices(sc, replacements(infos, led.Profiles))...), nil
}

// besideProfile is a profile entry's source with a relative local path joined to dir, the
// profile's directory (Plan 6b §4.3, §19 R5): absolute for a user's profile, relative to the
// project root for a project's. URLs, git's scp form and absolute paths are kept as written.
func besideProfile(src, dir string) string {
	if strings.Contains(src, "://") || source.Host(src) != "" || filepath.IsAbs(src) {
		return src
	}
	return filepath.Join(dir, src)
}

// overridesProfiles is the precedence notice of each entry an install writes to the scope's
// manifest (Plan 6b §7) that differs from the entry the profiles alone want (desired over every
// layer but the manifest, the one merge rule): the manifest's entry wins now. layers ends with
// the manifest as loaded.
func overridesProfiles(layers []Layer, written []manifest.Entry) []Notice {
	profiles, _ := desired(layers[:len(layers)-1])
	var out []Notice
	for _, ent := range written {
		if w := find(profiles, ent.Name); w != nil && !sameEntry(w.Entry, ent) {
			out = append(out, overrideNotice(ent, w.Entry, layers[len(layers)-1].Name, w.From))
		}
	}
	return out
}

// refuseProfileModule refuses to uninstall a module an active profile or default lists (Plan 6b
// §7): uninstalling it would leave it wanted, and the next sync would install it again. Each
// profile gets the hint that turns it off, with the run's output flags (display); default, which
// cannot be turned off and has no command that drops the module, names that edit in the error.
func (e *Engine) refuseProfileModule(sc Scope, layers []Layer, name string, display []string) error {
	var by, hints []string
	edit := ""
	for _, l := range layers {
		if !l.Profile || !slices.ContainsFunc(l.Entries, func(p manifest.Entry) bool { return p.Name == name }) {
			continue
		}
		q := strings.TrimPrefix(l.Name, "profile:")
		by = append(by, q)
		if q == profile.DefaultName {
			edit = "; remove it from " + shown(l.Path) + ", which is always on at user scope"
			continue
		}
		// The label is lola's own: the profile's name, author text, is in the command only.
		hints = append(hints, "turn the profile off: "+e.Command(sc, "profile", append([]string{q, "--off"}, display...)...))
	}
	if len(by) == 0 {
		return nil
	}
	what := "profile " + by[0]
	if len(by) > 1 {
		what = "profiles " + andJoin(by)
	}
	return errs.WithHints(fmt.Errorf("%s is wanted by %s%s", untrusted.Text(name, untrusted.MaxName), what, edit), hints...)
}
