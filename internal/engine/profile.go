package engine

import (
	"cmp"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Layer is one file of module entries a scope wants (Plan 6b §6.2): a profile or the scope's
// manifest. Name is what lock.json's from and every notice call it: the manifest's base name
// (project.yml, user.yml) or ledger.FromProfile of the profile's qualified name. Path is its
// file. A profile layer's entries come from profile.Entries, with relative local sources made
// absolute against the profile's directory (§19 R5).
type Layer struct {
	Name    string
	Path    string
	Profile bool
	Entries []manifest.Entry
}

// Wanted is the entry of one module that a scope's layers want: the entry of the last layer
// that lists it, whole (§6.2), From that layer's Name and Over the layers whose entry it
// replaced, lowest first. path is the winning layer's file, and profile says it is a profile.
type Wanted struct {
	manifest.Entry
	From    string
	Over    []string
	path    string
	profile bool
}

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
			w := &Wanted{Entry: ent, From: l.Name, path: l.Path, profile: l.Profile}
			w.Targets = slices.Clone(ent.Targets)
			if prev := won[ent.Name]; prev != nil {
				w.Over = append(slices.Clone(prev.Over), prev.From)
				if !sameEntry(prev.Entry, ent) && (noted == nil || noted(ent.Name)) {
					notices = append(notices, overrideNotice(ent, prev.Entry, l.Name, prev.From))
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
		slices.Equal(a.Targets, b.Targets) && a.RulesOn() == b.RulesOn()
}

// overrideNotice is the notice of a layer's entry replacing a lower layer's (Plan 6b §6.2):
// "gopls: project.yml overrides profile:user:go (v2.0 over v1.2)", the versions left out when
// they read alike.
func overrideNotice(ent, prev manifest.Entry, layer, under string) Notice {
	text := shown(ent.Name) + ": " + layer + " overrides " + under
	if now, was := entryVersion(ent), entryVersion(prev); now != was {
		text += " (" + now + " over " + was + ")"
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
// a profile that does not load refuses the run. A project with no active profile in any list
// never lists the user's profiles directory, so a broken one cannot refuse every project.
func (e *Engine) loadProfiles(sc Scope, infos []profile.Info, lists ...[]ledger.Active) (*profileLayers, error) {
	pl := &profileLayers{named: map[string]Layer{}}
	if sc.Kind == target.Project && !slices.ContainsFunc(lists, func(l []ledger.Active) bool { return len(l) > 0 }) {
		return pl, nil
	}
	if infos == nil {
		var err error
		if infos, _, err = profile.Visible(e.Paths, sc.Kind == target.User); err != nil {
			return nil, err
		}
	}
	load := func(i profile.Info) (Layer, error) {
		p, err := profile.Load(i.Path)
		if err != nil {
			return Layer{}, err
		}
		ents := p.Entries()
		for k := range ents {
			ents[k].Source = besideProfile(ents[k].Source, filepath.Dir(i.Path))
		}
		return Layer{Name: ledger.FromProfile(i.Qualified()), Path: i.Path, Profile: true, Entries: ents}, nil
	}
	if k := slices.IndexFunc(infos, func(i profile.Info) bool { return i.Kind == profile.KindDefault }); k >= 0 {
		l, err := load(infos[k])
		if err != nil {
			return nil, err
		}
		pl.def = &l
	}
	gone := map[string]bool{}
	for _, list := range lists {
		for _, a := range list {
			if _, read := pl.named[a.Name]; read || gone[a.Name] {
				continue
			}
			k := slices.IndexFunc(infos, func(i profile.Info) bool { return i.Qualified() == a.Name })
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
			if gone[a.Name] {
				pl.notices = append(pl.notices, e.goneProfile(sc, a.Name))
			}
		}
	}
	return pl, nil
}

// assemble is the scope's layers for the active list active, lowest first (Plan 6b §6.2, D6):
// default (when read), the active profiles in order (a gone one left out), then top, the scope's
// manifest.
func (pl *profileLayers) assemble(top Layer, active []ledger.Active) []Layer {
	var out []Layer
	if pl.def != nil {
		out = append(out, *pl.def)
	}
	for _, a := range active {
		if l, ok := pl.named[a.Name]; ok {
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

// besideProfile is a profile entry's source with a relative local path made absolute against
// dir, the profile's directory (Plan 6b §4.3, §19 R5). URLs, git's scp form and absolute paths
// are kept as written.
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
// profile gets the hint that turns it off; default, which cannot be turned off, the edit that
// drops the module.
func (e *Engine) refuseProfileModule(sc Scope, layers []Layer, name string) error {
	var by, hints []string
	for _, l := range layers {
		if !l.Profile || !slices.ContainsFunc(l.Entries, func(p manifest.Entry) bool { return p.Name == name }) {
			continue
		}
		q := strings.TrimPrefix(l.Name, "profile:")
		by = append(by, q)
		if q == profile.DefaultName {
			hints = append(hints, "remove it from "+shown(l.Path)+", which is always on at user scope")
			continue
		}
		hints = append(hints, "turn "+q+" off: "+e.Command(sc, "profile", q, "--off"))
	}
	if len(by) == 0 {
		return nil
	}
	what := "profile " + by[0]
	if len(by) > 1 {
		what = "profiles " + andJoin(by)
	}
	return errs.WithHints(fmt.Errorf("%s is wanted by %s", untrusted.Text(name, untrusted.MaxName), what), hints...)
}
