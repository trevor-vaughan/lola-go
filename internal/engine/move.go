package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// ProfileMove is lola profile --move: it moves entries between a scope's files (spec 2026-10-29
// §6.2).
const ProfileMove ProfileAction = "move"

// MoveRequest is lola profile --move (spec 2026-10-29 §6.2, §4 S4). Modules move whole from the
// layer each comes from at its scope, or from From (a profile, base, or default and project.yml for
// the user's and the project's base), to To (a profile of yours or the project's, or base; a missing
// name creates your profile). Scope is the scope the target is used in, the one the run's scope flag
// names; Other, when set, is the other scope a module may come from (a cross-scope move), and Across
// says the run named both (lola profile --user -C <dir>). Switch and
// KeepOff answer the question a target that is off in Scope asks; Yes (-y) and Options.Force answer
// "replace it?"; Options.AskWhere asks both on a terminal, Replace the second. ConfirmRemovals,
// Trust and Show are the syncs'.
type MoveRequest struct {
	Scope           Scope
	Other           *Scope
	Across          bool
	From            string
	Modules         []string
	To              string
	Switch, KeepOff bool
	Yes             bool
	Options         Options
	ConfirmRemovals func([]Removal) (bool, error)
	Trust           func(TrustReport) (TrustAnswer, error)
	Show            func(TrustReport)
	Replace         func(module, label string) (bool, error)
	// Source, when set, is told the other scope once the move knows it takes the entries from there,
	// before it asks or writes anything: the CLI prints that scope's context (spec 2026-10-29 §3.8).
	Source func(Scope)
}

// MoveResult is what a move reports: the files it wrote (Edits: each removal, then each
// addition), the sync of each scope it applied (the target scope's first) and the profile it
// created ("" for none).
type MoveResult struct {
	Edits   []Edit        `json:"edits"`
	Syncs   []*SyncResult `json:"syncs"`
	Created string        `json:"created,omitempty"`
}

// moveEdit is a move's edits of one scope's files (spec 2026-10-29 §6.2), which that scope's sync
// applies in the transaction that writes lock.json: modules, the moved names; out, per layer
// name, the modules whose entries leave it; in, per layer name, the entries it gains as the scope
// reads them; files, per path, the profile files' edits as they record them (order: first touched
// first); manifest, project.yml's. top is the target when the scope's stack lacks it though it is
// on: the profile lock.json has on whose file a dry run would create, which apply puts on top, or
// the one a dry run switches to and would create (creating, its qualified name). args
// is the move as lola profile takes it, which a hint repeating its sync names (commandOf), after
// the flags of the scope at names (the target's, for a move between scopes; nil: the sync's). origin
// is, per moved module whose entry comes from a project file, where the trust record covers it
// (trusted). selections is the removal of each moved module's entry from selections.yaml's section
// of a project whose project.yml it leaves, the picks the target entry carries now (carryPicks):
// the sync applies it to the selections it plans with and writes it after the scope's files. lost
// is each moved module whose carried picks would change what it installs (lostPicks).
type moveEdit struct {
	modules    []string
	out        map[string][]string
	in         map[string][]manifest.Entry
	files      map[string]*fileEdit
	order      []string
	manifest   manifest.Change
	top        *Layer
	creating   string
	args       []string
	at         *Scope
	origin     map[string]moveOrigin
	selections []config.Change
	written    []string // the projects key each of selections is under, as the file writes it
	lost       []*picksLostError
}

// moveOrigin is a moved entry of a project file as the trust record holds it: part, the file's part
// (Layer.Key, "" for project.yml), and view, the entry as the scope read it there.
type moveOrigin struct {
	part string
	view trust.Entry
}

func newMoveEdit() *moveEdit {
	return &moveEdit{out: map[string][]string{}, in: map[string][]manifest.Entry{}, files: map[string]*fileEdit{}, origin: map[string]moveOrigin{}}
}

// file is l's file edit, made on first use.
func (m *moveEdit) file(l writeTo) *fileEdit {
	f := m.files[l.Path]
	if f == nil {
		f = &fileEdit{layer: l.Layer}
		m.files[l.Path], m.order = f, append(m.order, l.Path)
	}
	return f
}

// remove takes module's entry out of l.
func (m *moveEdit) remove(l writeTo, module string) {
	m.out[l.Name] = append(m.out[l.Name], module)
	if !l.Profile {
		m.manifest.Remove = append(m.manifest.Remove, module)
		return
	}
	f := m.file(l)
	f.change.Remove = append(f.change.Remove, module)
}

// add puts ent, an entry as sc reads it, into l: as l's file records it (writeTo.fileEntry), and in
// the layer with l's targets when it has none of its own. It returns the entry as the file records
// it.
func (m *moveEdit) add(sc Scope, l writeTo, ent manifest.Entry) manifest.Entry {
	if !l.Profile {
		m.in[l.Name] = append(m.in[l.Name], ent)
		m.manifest.Set = append(m.manifest.Set, ent)
		return ent
	}
	rec := l.fileEntry(sc, ent)
	// The layer reads the entry from its file as loadStack does: a local source beside it, the
	// file's targets when it has none of its own.
	layerEnt := rec
	dir := filepath.Dir(l.Path)
	if l.Key != "" {
		dir = profile.ProjectDir
	}
	layerEnt.Source = besideProfile(rec.Source, dir)
	if len(layerEnt.Targets) == 0 && l.read != nil {
		layerEnt.Targets = slices.Clone(l.read.Targets)
	}
	m.in[l.Name] = append(m.in[l.Name], layerEnt)
	f := m.file(l)
	f.change.Set = append(f.change.Set, rec)
	return rec
}

// keeps reports whether m leaves layers, a scope's stack, as they are: it moves entries only
// between profiles that are off there (plan 12.5 P1).
func (m *moveEdit) keeps(layers []Layer) bool {
	return m.top == nil && !slices.ContainsFunc(layers, func(l Layer) bool { return len(m.out[l.Name])+len(m.in[l.Name]) > 0 })
}

// apply is layers with m's edits: each layer without the entries that leave it, with those it
// gains (replacing its own of their names), and top on top when it is set.
func (m *moveEdit) apply(layers []Layer) []Layer {
	out := make([]Layer, len(layers), len(layers)+1)
	for i, l := range layers {
		ents := slices.DeleteFunc(slices.Clone(l.Entries), func(e manifest.Entry) bool {
			return slices.Contains(m.out[l.Name], e.Name) || slices.ContainsFunc(m.in[l.Name], func(n manifest.Entry) bool { return n.Name == e.Name })
		})
		l.Entries = append(ents, m.in[l.Name]...)
		out[i] = l
	}
	if m.top != nil {
		top := *m.top
		top.Entries = slices.Clone(m.in[top.Name])
		out = append(out, top)
	}
	return out
}

// fileEdits is m's profile file edits, in the order first touched.
func (m *moveEdit) fileEdits() []fileEdit {
	out := make([]fileEdit, 0, len(m.order))
	for _, path := range m.order {
		out = append(out, *m.files[path])
	}
	return out
}

// pinned is profPins, the pins a sync writes into project profiles, with those into a file m
// edits folded into its edit (the entry it sets, or the file's own), which one write makes; the
// rest are returned.
func (m *moveEdit) pinned(profPins []profileEdit) []profileEdit {
	var rest []profileEdit
	for _, pe := range profPins {
		f := m.files[pe.layer.Path]
		if f == nil {
			rest = append(rest, pe)
			continue
		}
		for _, pin := range pe.pins {
			k := slices.IndexFunc(f.change.Set, func(e manifest.Entry) bool { return e.Name == pin.Name })
			if k < 0 {
				own := slices.IndexFunc(f.layer.read.Modules, func(e manifest.Entry) bool { return e.Name == pin.Name })
				f.change.Set, k = append(f.change.Set, f.layer.read.Modules[own]), len(f.change.Set)
			}
			f.change.Set[k].Commit, f.change.Set[k].Digest = pin.Commit, pin.Digest
		}
	}
	return rest
}

// trusted is v with lola's own writes recorded as trusted (decisions line 47, spec 2026-10-29
// §3.10): project.yml's edit, before being the manifest as read, and each project profile's. An
// entry moves its trust with it, never more: one from a project file whose part of v does not
// cover it is left out of the edit recorded, so the sync asks about it where it lands.
func (m *moveEdit) trusted(v trust.View, before *manifest.Manifest) trust.View {
	var untrusted []string
	for name, o := range m.origin {
		if !v.Part(o.part).CoversEntry(o.view) {
			untrusted = append(untrusted, name)
		}
	}
	covered := func(c manifest.Change) manifest.Change {
		c.Set = slices.DeleteFunc(slices.Clone(c.Set), func(e manifest.Entry) bool { return slices.Contains(untrusted, e.Name) })
		return c
	}
	if len(m.manifest.Set)+len(m.manifest.Remove) > 0 {
		v = trust.Refresh(v, "", trust.ViewOf(before), trust.ViewOf(before.Applied(covered(m.manifest))))
	}
	for _, f := range m.fileEdits() {
		if f.layer.Key == "" {
			continue
		}
		was := &manifest.Manifest{Modules: f.layer.Entries}
		if f.layer.read != nil {
			was.Targets = f.layer.read.Targets
		}
		v = trust.Refresh(v, f.layer.Key, trust.ViewOf(was), trust.ViewOf(was.Applied(covered(viewChange(f)))))
	}
	return v
}

// moveStack is what a move reads of one scope: its manifest, lock.json, layers and profiles.
type moveStack struct {
	sc     Scope
	man    *manifest.Manifest
	led    *ledger.Ledger
	layers []Layer
	infos  []profile.Info
}

func (e *Engine) moveStack(sc Scope) (*moveStack, error) {
	man, err := e.Manifest(sc)
	if err != nil {
		return nil, err
	}
	led, err := e.Ledger(sc)
	if err != nil {
		return nil, err
	}
	layers, _, err := e.layers(sc, man, led)
	if err != nil {
		return nil, err
	}
	infos, _, err := e.visible(sc)
	if err != nil {
		return nil, err
	}
	return &moveStack{sc: sc, man: man, led: led, layers: layers, infos: infos}, nil
}

// moveSource is the layer module's entry leaves, in st: from (the layer named before --move:
// base, default for user scope's base, project.yml for a project's, or a profile), else the layer
// its winning entry comes from. A module no layer of st lists is refused, and one from does not
// list; so is a remote profile's entry, on, a bundle's member or named (lola does not edit them),
// hinting what works (remoteOnHints); one of a remote profile that is off has no such command. off
// is the flags the turn-off hint takes: the run's output flags, and -y when the run cannot ask
// before removing files.
func (e *Engine) moveSource(st *moveStack, from, module string, off []string) (writeTo, error) {
	var l *Layer
	switch {
	case from == baseName || from == profile.DefaultName && st.sc.Kind == target.User || from == "project.yml" && st.sc.Kind == target.Project:
		l = &st.layers[0]
	case from != "":
		i, err := e.resolveProfile(st.sc, st.infos, from, nil)
		if _, unknown := errors.AsType[*unknownProfileError](err); unknown {
			if _, isDefault := namesBase(from); !isDefault {
				return writeTo{}, &MoveSourceError{err}
			}
		}
		if err != nil {
			return writeTo{}, err
		}
		if k := slices.IndexFunc(st.layers, func(x Layer) bool { return x.Name == ledger.FromProfile(i.Qualified()) }); k >= 0 {
			l = &st.layers[k]
			break
		}
		off, err := e.offLayer(i)
		if err != nil {
			return writeTo{}, e.mixedError(st.sc, st.infos, i, err)
		}
		l = &off
	default:
		want, _ := desired(st.layers)
		w := find(want, module)
		if w == nil {
			return writeTo{}, &notListedError{fmt.Errorf("%s: no layer %s lists it", shown(module), scopeWhere(st.sc))}
		}
		l = &st.layers[slices.IndexFunc(st.layers, func(x Layer) bool { return x.Name == w.From })]
	}
	t := writeTo{Layer: *l, on: slices.ContainsFunc(st.layers, func(x Layer) bool { return x.Name == l.Name }), label: layerLabel(st.infos, l.Name)}
	readOnly := func(name string, hints ...string) error {
		return errs.WithHints(fmt.Errorf("%s comes from %s, which is read-only; lola does not edit remote profiles", shown(module), name), hints...)
	}
	switch {
	case t.layerEntry(module) == nil:
		return writeTo{}, &notListedError{fmt.Errorf("%s does not list %s", t.label, shown(module))}
	case remoteLayer(t.Name) && t.on:
		return writeTo{}, readOnly(t.label, e.remoteOnHints(st.sc, st.infos, t.Layer, off)...)
	case remoteLayer(t.Name):
		return writeTo{}, readOnly(t.label)
	}
	return t, nil
}

// notListedError is moveSource's refusal of a module the layer it looks in does not list, or no
// layer of the scope: the refusal that sends a move to look in the other scope (providers).
type notListedError struct{ error }

func (e *notListedError) Unwrap() error { return e.error }

// MoveSourceError is a move whose source, the layer named before --move, names no profile the
// scope can use: the CLI hints the name as one of the modules (--move a,b).
type MoveSourceError struct{ err error }

func (e *MoveSourceError) Error() string { return e.err.Error() }

func (e *MoveSourceError) Unwrap() error { return e.err }

// moveTarget is --to in st (spec 2026-10-29 §6.2): base (default at user scope) is the base; a
// profile of yours or the project's, on or off; a name no profile has creates yours (created: its
// path, which the caller creates before the edits are built, createTarget). A remote profile and
// project.yml from user scope are refused; req's Options.Target.Rerun is the run without --to,
// which a refusal's hints give their own --to, and its UserRun the run at user scope.
func (e *Engine) moveTarget(st *moveStack, req MoveRequest) (t writeTo, created string, err error) {
	to := req.To
	if to == "project.yml" && st.sc.Kind == target.User {
		return writeTo{}, "", errors.New("project.yml is a project's base; lola profile --move never moves an entry into it from user scope")
	}
	if to == "project.yml" {
		to = baseName
	}
	tr := TargetRequest{Profile: to, Flag: "--to", Rerun: req.Options.Target.Rerun, UserRun: req.Options.Target.UserRun, Again: req.Options.Target.Again}
	t, err = e.namedTarget(st.sc, st.layers, st.infos, tr, "profile", baseTarget(st.layers[0]))
	_, unknown := errors.AsType[*unknownProfileError](err)
	if _, bundle := errors.AsType[*BundleTargetError](err); bundle {
		return writeTo{}, "", err // its hints name the members, which a move takes
	}
	if err != nil && !unknown {
		// namedTarget's hints name targets as --profile does; a move's name those it takes now.
		hints := e.moveHints(st, req)
		if _, isDefault := namesBase(to); isDefault && req.Options.Target.UserRun != nil && e.movesAtUser(req) {
			hints = append(hints, req.Options.Target.UserRun())
		}
		return writeTo{}, "", errs.WithHints(errors.New(err.Error()), hints...)
	}
	if !unknown || strings.Contains(to, ":") && !strings.HasPrefix(to, string(profile.KindUser)+":") {
		return t, "", err
	}
	name := strings.TrimPrefix(to, string(profile.KindUser)+":")
	if verr := remoteName(name); verr != nil {
		return writeTo{}, "", verr
	}
	q := string(profile.KindUser) + ":" + name
	path := e.profilePath(st.sc, profile.KindUser, name)
	l := Layer{Name: ledger.FromProfile(q), Path: path, Profile: true, Entries: []manifest.Entry{}}
	// A profile lock.json has on whose file is gone is on again once its file exists.
	on := st.led.Profile != nil && st.led.Profile.Name == q
	return writeTo{Layer: l, on: on, label: "profile " + shown(name)}, path, nil
}

// Move moves req.Modules (spec 2026-10-29 §6.2): every refusal and question before anything is
// written, then the target scope's sync, which writes both files in one transaction with lock.json
// and applies only what moves.
func (e *Engine) Move(ctx context.Context, req MoveRequest) (_ *MoveResult, err error) {
	if req.Options.Target.Rerun == nil { // without the CLI's run, a hint repeats the move as lola profile takes it
		run := moveArgs(MoveRequest{From: req.From, Modules: req.Modules})
		run = run[:len(run)-2] // without its --to
		req.Options.Target.Rerun = func(extra ...string) string { return e.Command(req.Scope, "profile", slices.Concat(run, extra)...) }
	}
	// The syncs of a move between scopes run under the profiles lock the move takes, which a
	// restore takes too: each scope's remote profiles are restored first, as a sync does.
	var restored []Notice
	if req.Other != nil {
		extra := req.To
		if isBase, isDefault := namesBase(extra); isBase || isDefault || extra == "project.yml" {
			extra = ""
		}
		for _, sc := range []Scope{req.Scope, *req.Other} {
			n, err := e.restoreRemotes(ctx, sc, extra, req.Options.Target.Rerun("--to", req.To), req.Options.DryRun)
			if err != nil {
				return nil, err
			}
			restored, extra = append(restored, n...), ""
		}
	}
	unlock, err := e.readProfiles(req.Options.DryRun)
	if err != nil {
		return nil, err
	}
	defer func() { unlock() }()
	dst, err := e.moveStack(req.Scope)
	if err != nil {
		return nil, err
	}
	if req.To == "" {
		return nil, errs.WithHints(errors.New("--move needs --to, the profile the entries move to"), e.moveHints(dst, req)...)
	}
	to, created, err := e.moveTarget(dst, req)
	if err != nil {
		return nil, err
	}
	src := dst // the stack the modules come from: dst's, or the other scope's (providers)
	if req.Other != nil {
		other, err := e.moveStack(*req.Other)
		if err != nil {
			return nil, err
		}
		cross, err := e.providers(ctx, req, dst, other, to)
		if err != nil {
			return nil, err
		}
		if cross {
			src = other
			if req.Source != nil {
				req.Source(other.sc)
			}
		}
	}
	if src != dst && dst.sc.Kind == target.Project && (!to.Profile || to.Key != "") {
		// The hints plan the moves they name (switchTarget), each under the profiles lock it takes.
		unlock()
		unlock = func() {}
		return nil, errs.WithHints(fmt.Errorf("%s comes from user scope; lola profile --move moves an entry from user scope into a profile of yours only",
			strings.Join(shownAll(req.Modules), ", ")), e.yoursHints(ctx, req, dst)...)
	}
	if src == dst {
		// What lands in a profile of yours is yours, which nothing asks about: an entry of a project
		// file moves into one only as the project installed it, trusted, as it leaves for user scope.
		carried, err := e.intoYours(dst, req, to)
		if err != nil {
			return nil, err
		}
		if len(carried) > 0 {
			if err := e.carriedOut(ctx, dst.sc, to, e.movedFrom(req, dst), carried, req.Options.Display); err != nil {
				return nil, err
			}
		}
	}
	sync := SyncRequest{Scope: req.Scope, Options: req.Options, ConfirmRemovals: req.ConfirmRemovals, Trust: req.Trust, Show: req.Show,
		Display: req.Options.Display}
	if sync, err = e.offTarget(req, dst, to, sync); err != nil {
		return nil, err
	}
	// The hints repeating the move carry the answer, given or asked (moveArgs); a --switch to the
	// profile on already switches nothing (Move says so).
	switchOn := req.Switch && to.on
	req.Switch, req.KeepOff = sync.Profiles != nil, !to.on && sync.Profiles == nil
	res := &MoveResult{Syncs: []*SyncResult{}}
	if created != "" && !req.Options.DryRun {
		var undo func() error
		if undo, err = e.createTarget(&to); err != nil {
			return nil, err
		}
		res.Created = shown(created)
		defer func() {
			if err != nil {
				err = errors.Join(err, undo())
			}
		}()
	}
	rm, add, edits, err := e.moveEdits(ctx, req, src, dst, to)
	if err != nil {
		return nil, err
	}
	var lost *Notice // a dry run's, or --force's, word on the picks the move cannot carry as they are
	if len(rm.lost) > 0 {
		var whys []string
		for _, l := range rm.lost {
			whys = append(whys, l.Error())
		}
		why := strings.Join(whys, "; ")
		run := req.Options.ForceRun // the CLI's: the run as given, with --force
		if run == "" {
			args := moveArgs(req)
			if src != dst {
				args = e.acrossArgs(req, dst, src, req.From, req.Modules, req.Yes)
			}
			run = e.Command(req.Scope, "profile", slices.Concat(args, []string{"--force"}, req.Options.Display)...)
		}
		anyway := "move it anyway: " + run
		switch {
		case req.Options.Force:
			lost = &Notice{Level: LevelWarn, Text: why + "; --force moves it all the same"}
		case req.Options.DryRun:
			lost = &Notice{Level: LevelWarn, Text: "the move refuses: " + why + " → " + anyway}
		default:
			return nil, errs.WithHints(&picksLostError{errors.New(why)}, anyway)
		}
	}
	if created != "" && req.Options.DryRun && (to.on || req.Switch) {
		// A dry run creates nothing: the profile it would create is put on top of the stack, and
		// a switch to it is planned as the switch to a profile whose file is gone.
		add.top = &to.Layer
		if !to.on {
			add.creating = strings.TrimPrefix(to.Name, ledger.FromProfile(""))
		}
	}
	res.Edits = edits
	sync.move = add
	if src != dst {
		// Both syncs run under the profiles lock taken here, which a sync of its own would take
		// again.
		if res.Syncs, err = e.moveAcross(ctx, req, sync, rm, dst, src, to); err != nil {
			return nil, err
		}
	} else {
		unlock()
		unlock = func() {}
		r, err := e.Sync(ctx, sync)
		if err != nil {
			return nil, err
		}
		res.Syncs = append(res.Syncs, r)
	}
	if len(restored) > 0 {
		res.Syncs[0].Notices = append(restored, res.Syncs[0].Notices...)
	}
	if lost != nil {
		res.Syncs[0].Notices = append(res.Syncs[0].Notices, *lost)
	}
	if switchOn {
		r := res.Syncs[0]
		r.Notices = append(r.Notices, Notice{Level: LevelInfo, Text: shown(strings.TrimPrefix(to.Name, ledger.FromProfile(""))) + " is already on"})
	}
	return res, nil
}

// yoursHints is the run with --to each profile of yours a move from user scope into dst's project
// takes (spec 2026-10-29 §6.2): the one on there, else, with --switch, each of yours that can take
// them when there are at most two (plan 12.2b P13) and switchTarget takes it, led by what the
// switch turns off; -y when the run cannot ask before it removes the modules' files at user scope.
func (e *Engine) yoursHints(ctx context.Context, req MoveRequest, dst *moveStack) []string {
	var names, clauses []string
	top := dst.layers[len(dst.layers)-1]
	on := strings.HasPrefix(top.Name, ledger.FromProfile(string(profile.KindUser)+":"))
	if on {
		names, clauses = []string{strings.TrimPrefix(layerLabel(dst.infos, top.Name), "profile ")}, []string{""}
	} else {
		var yours []profile.Info
		for _, i := range dst.infos {
			if _, _, ok := e.switchable(dst.layers, i, req.Modules); ok && i.Kind == profile.KindUser {
				yours = append(yours, i)
			}
		}
		if len(yours) > 2 {
			yours = nil
		}
		for _, i := range yours {
			// Qualified when a project profile has the name (layerLabel).
			name := strings.TrimPrefix(layerLabel(dst.infos, ledger.FromProfile(i.Qualified())), "profile ")
			if _, clause, ok := e.switchTarget(ctx, dst.layers, dst.infos, i, MoveRequest{Scope: req.Scope, Other: req.Other, Modules: req.Modules, To: name}); ok {
				names, clauses = append(names, name), append(clauses, clause)
			}
		}
	}
	var hints []string
	for k, n := range names {
		flags := []string{"--to", n}
		if !on {
			flags = append(flags, "--switch")
		}
		if req.Yes {
			flags = append(flags, "-y")
		}
		hint := e.flagRun(req.Scope, req.Options.Target, "profile", "", WhereChoice{Flags: flags})
		if clauses[k] != "" {
			hint = clauses[k] + ": " + hint
		}
		hints = append(hints, hint)
	}
	return hints
}

// providers says whether req's modules come from src's scope, the other one, rather than from
// dst's, the scope of the target (spec 2026-10-29 §6.2): a module comes from dst's layers when
// they provide it (it moves within dst's scope, plan 12.2b P6), else from src's when they do; one
// neither provides is dst's to refuse (moveEdits). req.From, the source layer named, is looked
// for in each scope the same way, base naming dst's. Only a module a scope does not list, or a
// From naming no profile it can use, sends providers to the other scope; any other refusal is
// returned as it is (P28). Refused, with the run that moves each part as hints: with req.Across
// (the run names both scopes) and no From, a module both provide; modules of both scopes in one
// run; modules of two layers of src's (the run that finishes a move needs its one source layer,
// moveAcross); and, from a project, modules its own sync would not leave as they are installed
// (a change not installed or not trusted, a folder's content included), which user scope would
// install unasked, or whose local source lies inside a project (carriedOut; to, the target).
func (e *Engine) providers(ctx context.Context, req MoveRequest, dst, src *moveStack, to writeTo) (bool, error) {
	look := func(st *moveStack, m string) (*writeTo, error) {
		t, err := e.moveSource(st, req.From, m, offFlags(req))
		_, notListed := errors.AsType[*notListedError](err)
		_, unknown := errors.AsType[*unknownProfileError](err)
		switch {
		case err == nil:
			return &t, nil
		case notListed || unknown:
			return nil, nil
		}
		return nil, err
	}
	var here, there []string
	froms := map[string]writeTo{} // the layer of src's each of there comes from
	for _, m := range req.Modules {
		inDst, err := look(dst, m)
		if err != nil {
			return false, err
		}
		if req.From == baseName || inDst != nil && (!req.Across || req.From != "") {
			here = append(here, m)
			continue
		}
		inSrc, err := look(src, m)
		if err != nil {
			return false, err
		}
		switch {
		case inDst != nil && inSrc != nil:
			hint := func(st *moveStack, t *writeTo) string {
				flags := []string{"--to", req.To, sourceName(st, *t)}
				if req.Yes {
					flags = append(flags, "-y")
				}
				return e.flagRun(dst.sc, req.Options.Target, "profile", "", WhereChoice{Flags: flags})
			}
			return false, errs.WithHints(fmt.Errorf("%s is in %s %s and in %s %s; name the layer it moves from", shown(m),
				inDst.label, scopeWhere(dst.sc), inSrc.label, scopeWhere(src.sc)), hint(dst, inDst), hint(src, inSrc))
		case inSrc != nil:
			there, froms[m] = append(there, m), *inSrc
		default:
			here = append(here, m)
		}
	}
	if len(there) == 0 {
		return false, nil
	}
	if len(here) > 0 {
		return false, errs.WithHints(fmt.Errorf("%s %s listed %s and %s %s; a move takes the modules of one scope", strings.Join(shownAll(here), ", "),
			isAre(here), scopeWhere(dst.sc), strings.Join(shownAll(there), ", "), scopeWhere(src.sc)),
			e.Command(dst.sc, "profile", append(moveArgs(MoveRequest{From: req.From, Modules: here, To: req.To, Yes: req.Yes}), req.Options.Display...)...),
			e.acrossRun(req, dst, src, req.From, there, req.Yes))
	}
	var layers []string // src's layers the modules come from, in the order of the first module of each
	byLayer := map[string][]string{}
	for _, m := range there {
		name := froms[m].Name
		if byLayer[name] == nil {
			layers = append(layers, name)
		}
		byLayer[name] = append(byLayer[name], m)
	}
	if len(layers) > 1 {
		var parts, hints []string
		for i, name := range layers {
			t := froms[byLayer[name][0]]
			part := strings.Join(shownAll(byLayer[name]), ", ")
			if i == 0 {
				part += " " + isAre(byLayer[name])
			}
			parts = append(parts, part+" in "+t.label)
			hints = append(hints, e.acrossRun(req, dst, src, sourceName(src, t), byLayer[name], req.Yes))
		}
		return false, errs.WithHints(fmt.Errorf("%s %s; a move between scopes takes the modules of one layer", andJoin(parts), scopeWhere(src.sc)), hints...)
	}
	if src.sc.Kind == target.Project {
		if err := e.carriedOut(ctx, src.sc, to, e.movedFrom(req, src), there, req.Options.Display); err != nil {
			return false, err
		}
	}
	return true, nil
}

// movedFrom is carriedOut's from for req's modules, entries of st's scope: the layer each moves
// from (moveSource).
func (e *Engine) movedFrom(req MoveRequest, st *moveStack) func(string) (writeTo, error) {
	return func(m string) (writeTo, error) { return e.moveSource(st, req.From, m, offFlags(req)) }
}

// localInProject refuses m, an entry of from (a layer of sc's), carried into t, when t is a file
// of yours and from a project file (project.yml or a project profile) whose local source for m
// lies inside a project (inProject; spec 2026-10-29 §16.2): a move's and a --new --from's check.
func (e *Engine) localInProject(sc Scope, t, from writeTo, m string) error {
	if !t.Profile || t.Key != "" || from.Profile && from.Key == "" {
		return nil
	}
	if p, ok := e.inProject(fileSource(sc, writeTo{Layer: Layer{Profile: true}}, from.layerEntry(m).Source)); ok {
		return errors.New(p.text(m, t.Path) + "; lola keeps a project's local modules out of your files")
	}
	return nil
}

// carriedOut refuses modules, entries of sc's files carried into t, a file of yours, where no trust
// question asks about them (spec 2026-10-29 §16.2): one whose local source lies inside a project
// (localInProject), then any the project's sync would not leave as installed and trusted
// (installedAsTrusted, the first module's layer saying whether it is on). from is the layer each
// comes from. A move's, a --new --from's and an install --override's check.
func (e *Engine) carriedOut(ctx context.Context, sc Scope, t writeTo, from func(module string) (writeTo, error), modules, display []string) error {
	var first writeTo
	for k, m := range modules {
		f, err := from(m)
		if err != nil {
			return err
		}
		if k == 0 {
			first = f
		}
		if err := e.localInProject(sc, t, f, m); err != nil {
			return &keptInProjectError{err}
		}
	}
	if err := e.installedAsTrusted(ctx, sc, first, modules, display); err != nil {
		return &keptInProjectError{err}
	}
	return nil
}

// keptInProjectError is carriedOut's refusal, which the same run meets again until the project is
// reviewed: an install's retry hint leaves it out (withRetry).
type keptInProjectError struct{ error }

func (e *keptInProjectError) Unwrap() error { return e.error }

// layerOf is a carriedOut from for modules that all come from l.
func layerOf(l writeTo) func(string) (writeTo, error) {
	return func(string) (writeTo, error) { return l, nil }
}

// installedAsTrusted refuses modules, entries of sc's project files a move or a --new --from carries where no trust
// question asks about them (user scope, or a profile of yours, whose entries are yours), unless the
// project's own sync, which judges the entries, their targets and a folder's content, would leave
// them as installed and trusted: lola copies or moves out of a project only what it installed as you trusted
// it. The refusal hints the sync that reviews them; from, the layer they come from, when it is off
// (the source the run names), installed none of them, and the hint is the switch that reviews it.
func (e *Engine) installedAsTrusted(ctx context.Context, sc Scope, from writeTo, modules, display []string) error {
	refuse := func(why, hint string) error {
		return errs.WithHints(fmt.Errorf("%s %s %s; lola copies or moves out of a project only what it installed as you trusted it",
			strings.Join(shownAll(modules), ", "), isAre(modules), why), hint)
	}
	if !from.on {
		return refuse("in "+from.label+", which is off "+scopeWhere(sc), "review it first: "+e.switchRun(sc, from))
	}
	plan, err := e.Sync(ctx, SyncRequest{Scope: sc, Modules: modules, Options: Options{DryRun: true}, Display: display, held: true})
	if err != nil {
		return err
	}
	if !plan.InSync || plan.Trust.Status != trust.StatusTrusted && plan.Trust.Status != TrustStatusNotApplicable {
		return refuse("not installed "+scopeWhere(sc)+" as its files list "+itThem(modules)+" now", "review the changes first: "+e.SyncCommand(sc, modules...))
	}
	return nil
}

// intoYours is the modules of req a move within dst's scope carries out of a project file
// (project.yml or a project profile) into t, a profile of yours: none at user scope, whose files
// are all yours, nor for any other target, whose sync judges the entries where they land.
func (e *Engine) intoYours(dst *moveStack, req MoveRequest, t writeTo) ([]string, error) {
	var out []string
	if dst.sc.Kind != target.Project || !t.Profile || t.Key != "" {
		return nil, nil
	}
	for _, m := range req.Modules {
		from, err := e.moveSource(dst, req.From, m, offFlags(req))
		if err != nil {
			return nil, err
		}
		if (!from.Profile || from.Key != "") && !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	return out, nil
}

// removesUnasked reports whether req cannot ask before it removes files. A hint never answers that
// for the user: it carries -y only as given (R-8).
func removesUnasked(req MoveRequest) bool {
	return req.ConfirmRemovals == nil && !req.Options.Force
}

// sourceName is t, a layer of st's, as lola profile names a move's source layer: default or
// project.yml for the base, a profile by the name its label gives.
func sourceName(st *moveStack, t writeTo) string {
	switch {
	case t.Name != st.layers[0].Name:
		return strings.TrimPrefix(t.label, "profile ")
	case st.sc.Kind == target.User:
		return profile.DefaultName
	}
	return "project.yml"
}

// acrossArgs is the move of modules from layer from ("" for the layer each comes from) of src's
// scope into req.To at dst's, as lola profile takes it after dst's scope flags (moveArgs, with yes
// for -y): at user scope with -C <root> when the project is not the one found from here.
func (e *Engine) acrossArgs(req MoveRequest, dst, src *moveStack, from string, modules []string, yes bool) []string {
	args := moveArgs(MoveRequest{From: from, Modules: modules, To: req.To, Switch: req.Switch, KeepOff: req.KeepOff, Yes: yes})
	if dst.sc.Kind == target.User && !e.FoundHere(src.sc) {
		args = append(args, "-C", src.sc.ProjectDir)
	}
	return args
}

// acrossRun is acrossArgs as the command a hint names, with --trust for the kinds req grants and
// the run's output flags.
func (e *Engine) acrossRun(req MoveRequest, dst, src *moveStack, from string, modules []string, yes bool) string {
	args := e.acrossArgs(req, dst, src, from, modules, yes)
	if req.Options.Trust != 0 {
		args = append(args, req.Options.Trust.Arg())
	}
	return e.Command(dst.sc, "profile", append(args, req.Options.Display...)...)
}

// moveAcross applies a move between scopes (spec 2026-10-29 §6.2) under the profiles lock Move
// holds and both scope locks, taken in lockOrder: add, dst's sync, which adds the entries (seeded
// with the versions src's scope installed), then rm's, src's, which removes them. Its removals
// are asked before anything is written: a dry run of rm finds them. A failure between the two
// leaves each module in both files, never in neither: the error says so and hints the run that
// finishes the move, naming the source layer. A dry run gives the command that applies it, with
// the answers off a terminal of both, once, on add's result.
func (e *Engine) moveAcross(ctx context.Context, req MoveRequest, add SyncRequest, rm *moveEdit, dst, src *moveStack, to writeTo) ([]*SyncResult, error) {
	modules := add.move.modules
	left, err := e.moveSource(src, req.From, modules[0], nil) // the one layer they leave (providers)
	if err != nil {
		return nil, err
	}
	// Until the add, a hint repeating either sync repeats the move as given.
	add.move.args, add.move.at = e.acrossArgs(req, dst, src, req.From, modules, req.Yes), &dst.sc
	rm.args, rm.at = add.move.args, &dst.sc
	add.held, add.seed = true, map[string]*ledger.Entry{}
	for _, m := range modules {
		if in := src.led.Modules[m]; in != nil {
			add.seed[m] = in
		}
	}
	remove := SyncRequest{Scope: src.sc, Options: req.Options, ConfirmRemovals: req.ConfirmRemovals, Trust: req.Trust, Show: req.Show,
		Display: req.Options.Display, move: rm, held: true}
	if req.Options.DryRun {
		first, err := e.Sync(ctx, add)
		if err != nil {
			return nil, err
		}
		second, err := e.Sync(ctx, remove)
		if err != nil {
			return nil, err
		}
		if second.ToApply != "" {
			first.ToApply, first.OffTerminal, first.EquivalentCommands = second.ToApply, second.OffTerminal, second.EquivalentCommands
			second.ToApply, second.OffTerminal, second.EquivalentCommands = "", "", []string{}
		}
		return []*SyncResult{first, second}, nil
	}
	if len(rm.selections) > 0 {
		// rm's sync writes selections.yaml (Sync): its lock comes before the scopes'.
		unlock, err := config.Lock(e.Paths.SelectionsLock)
		if err != nil {
			return nil, err
		}
		defer unlock()
	}
	scopes := []ScopeInstall{{ScopeChoice: ScopeChoice{Scope: dst.sc}}, {ScopeChoice: ScopeChoice{Scope: src.sc}}}
	for _, i := range lockOrder(scopes) {
		unlock, err := e.lock(scopes[i].Scope)
		if err != nil {
			return nil, err
		}
		defer unlock()
	}
	// The removals are asked first: what a No or no terminal refuses has written nothing.
	plan := remove
	plan.Options.DryRun = true
	planned, err := e.Sync(ctx, plan)
	if err != nil {
		return nil, err
	}
	// Consent the source's sync would ask for after the add is asked for here, off a terminal, or
	// named in the hint that finishes the move.
	missing := planned.needs
	if planned.Trust.Status != trust.StatusTrusted && planned.Trust.Status != TrustStatusNotApplicable {
		missing |= planned.Trust.kinds()
	}
	if missing &^= req.Options.Trust; missing != 0 && req.Trust == nil {
		var yes []string
		if len(planned.Removals) > 0 && removesUnasked(req) {
			yes = []string{"-y"}
		}
		return nil, errs.WithHints(fmt.Errorf("removing %s %s needs your trust (%s), and there is no terminal to ask on", strings.Join(shownAll(modules), ", "),
			scopeWhere(src.sc), missing.Arg()), "then accept it: "+e.commandOf(remove.trusting(req.Options.Trust|missing), append(slices.Clone(req.Options.Display), yes...)...))
	}
	if asked := planned.Removals; len(asked) > 0 && !req.Options.Force {
		if req.ConfirmRemovals == nil {
			return nil, e.syncRefusal(src.sc, remove, TrustReport{Status: planned.Trust.Status, Changes: []TrustChange{}, Replacements: []Replacement{}},
				asked, 0, nil)
		}
		yes, err := req.ConfirmRemovals(slices.Clone(asked))
		if err != nil {
			return nil, err
		}
		if !yes {
			return nil, &RemovalsDeclinedError{Retry: e.commandOf(remove)}
		}
		remove.ConfirmRemovals = func(rs []Removal) (bool, error) {
			if reflect.DeepEqual(rs, asked) {
				return true, nil
			}
			return req.ConfirmRemovals(rs)
		}
	}
	if err := e.sourceUnchanged(ctx, req, src, left, modules, e.acrossRun(req, dst, src, req.From, modules, req.Yes)); err != nil {
		return nil, err
	}
	first, err := e.Sync(ctx, add)
	if err != nil {
		return nil, err
	}
	// From here on each module is in both files: a hint repeating the move finishes it, from the
	// layer it is still in.
	from := sourceName(src, left)
	rm.args = e.acrossArgs(req, dst, src, from, modules, true)
	second, err := e.Sync(ctx, remove)
	if _, done := errors.AsType[*selectionsLeftError](err); done {
		return nil, err // the move is applied; only selections.yaml keeps the picks it carried
	}
	if err != nil {
		// The sync's own types are left out: a refusal or a No here does not leave the scopes as
		// they were, as the CLI would say of them.
		finishing := req
		finishing.Options.Trust |= missing
		finish := "finish the move: " + e.acrossRun(finishing, dst, src, from, modules, true)
		return nil, errs.WithHints(fmt.Errorf("%s was added to %s %s but is still in %s %s: %s", strings.Join(shownAll(modules), ", "), to.label,
			scopeWhere(dst.sc), left.label, scopeWhere(src.sc), err.Error()), append([]string{finish}, errs.Hints(err)...)...)
	}
	return []*SyncResult{first, second}, nil
}

// sourceUnchanged refuses a move between scopes whose source changed since the move read it
// (src, left being the layer the modules leave), hinting again, the move as given: under the scope
// locks, after every question, src's scope is read again, and each module's entry there and in
// lock.json must be as read; from a project, its sync judges them again (installedAsTrusted).
// What moves is then what was judged.
func (e *Engine) sourceUnchanged(ctx context.Context, req MoveRequest, src *moveStack, left writeTo, modules []string, again string) error {
	now, err := e.moveStack(src.sc)
	if err != nil {
		return err
	}
	read, err := e.moveSource(now, req.From, modules[0], nil)
	fileEntry := func(t writeTo, m string) *manifest.Entry {
		if t.read == nil {
			return nil
		}
		if k := slices.IndexFunc(t.read.Modules, func(x manifest.Entry) bool { return x.Name == m }); k >= 0 {
			return &t.read.Modules[k]
		}
		return nil
	}
	var changed []string
	for _, m := range modules {
		if err != nil || read.Name != left.Name || !reflect.DeepEqual(left.layerEntry(m), read.layerEntry(m)) ||
			!reflect.DeepEqual(fileEntry(left, m), fileEntry(read, m)) || !reflect.DeepEqual(src.led.Modules[m], now.led.Modules[m]) {
			changed = append(changed, m)
		}
	}
	if len(changed) > 0 {
		return errs.WithHints(fmt.Errorf("%s changed %s while lola was moving %s; nothing was written", strings.Join(shownAll(changed), ", "),
			scopeWhere(src.sc), itThem(changed)), "run it again: "+again)
	}
	if src.sc.Kind == target.Project {
		return e.installedAsTrusted(ctx, now.sc, read, modules, req.Options.Display)
	}
	return nil
}

// moveHints is the run with each --to the move of req.Modules takes in st as it stands (spec
// 2026-10-29 §3.7, §6.3: hints that work when run): the profile on, when it is yours or the
// project's, then the base; each only when every module comes from another layer and it lists
// none of them. None when neither does.
func (e *Engine) moveHints(st *moveStack, req MoveRequest) []string {
	cands := []writeTo{baseTarget(st.layers[0])}
	if top := st.layers[len(st.layers)-1]; len(st.layers) > 1 && !remoteLayer(top.Name) {
		cands = append([]writeTo{{Layer: top, on: true, label: layerLabel(st.infos, top.Name)}}, cands...)
	}
	var hints []string
	for _, c := range cands {
		if !e.takes(st, req, c) {
			continue
		}
		name := baseName
		if c.Name != st.layers[0].Name {
			name = strings.TrimPrefix(c.label, "profile ")
		}
		hints = append(hints, e.flagRun(req.Scope, req.Options.Target, "profile", "", WhereChoice{Flags: []string{"--to", name}}))
	}
	return hints
}

// takes reports whether a move of req.Modules in st into t, a file that is on, goes through
// without a question: every module comes from a layer other than t, which lists none of them.
func (e *Engine) takes(st *moveStack, req MoveRequest, t writeTo) bool {
	for _, m := range req.Modules {
		from, err := e.moveSource(st, req.From, m, nil)
		if err != nil || from.Name == t.Name || t.layerEntry(m) != nil {
			return false
		}
	}
	return len(req.Modules) > 0
}

// movesAtUser reports whether the move of req.Modules into default goes through at user scope
// (the run --user hints in place of --to default at project scope).
func (e *Engine) movesAtUser(req MoveRequest) bool {
	st, err := e.moveStack(Scope{Kind: target.User})
	if err != nil {
		return false
	}
	return e.takes(st, req, baseTarget(st.layers[0]))
}

// offTarget is sync for a move into t when st's scope does not load t (spec 2026-10-29 §4 S4,
// §6.2): --switch switches to it in the same plan; --keep-off leaves it off, so the moved modules
// are removed here (the sync asks; -y, --force); without either, AskWhere asks, and without it the
// move is refused with both as hints. Neither -y nor --force answers the question. A target that
// is on takes --switch when it is the profile on (Move says it is already on); --keep-off, and
// --switch with the base, are refused (MoveFlagError: the CLI hints the run without the flag).
func (e *Engine) offTarget(req MoveRequest, st *moveStack, t writeTo, sync SyncRequest) (SyncRequest, error) {
	if t.on {
		base := t.Name == st.layers[0].Name
		switch {
		case base && (req.Switch || req.KeepOff):
			flag := "switch"
			if req.KeepOff {
				flag = "keep-off"
			}
			return sync, &MoveFlagError{Flag: flag, err: fmt.Errorf("the base is always on %s; --%s applies to a profile", scopeWhere(st.sc), flag)}
		case req.KeepOff:
			return sync, &MoveFlagError{Flag: "keep-off", err: fmt.Errorf("%s is on %s already; --keep-off applies to a profile that is off",
				strings.TrimPrefix(t.label, "profile "), scopeWhere(st.sc))}
		}
		return sync, nil
	}
	name := strings.TrimPrefix(t.label, "profile ")
	switchTo := req.Switch
	if !req.Switch && !req.KeepOff {
		verb := " is removed here)"
		if len(req.Modules) > 1 {
			verb = " are removed here)"
		}
		choices := []WhereChoice{{Label: "Switch to " + name, Flags: []string{"--to", req.To, "--switch"}},
			{Label: "Keep it off (" + strings.Join(shownAll(req.Modules), ", ") + verb, Flags: []string{"--to", req.To, "--keep-off", "-y"}}}
		q := WhereQuestion{Scope: st.sc, Title: name + " is not on here.", Choices: choices}
		refused := name + " is not on " + scopeWhere(st.sc) + "; lola needs to know whether to switch to it, and there is no terminal to ask on"
		c, err := e.ask(req.Options.AskWhere, q, refused, req.Options.Target, "profile")
		if err != nil {
			return sync, err
		}
		switchTo = c.Label == choices[0].Label
	}
	if switchTo {
		sync.Profiles = &ProfileChange{Action: ProfileSwitch, Name: strings.TrimPrefix(t.Name, ledger.FromProfile(""))}
	}
	return sync, nil
}

// MoveFlagError refuses a move flag that does not apply to its target (--keep-off with a profile
// that is on, --switch or --keep-off with the base): the CLI hints the run without Flag.
type MoveFlagError struct {
	Flag string
	err  error
}

func (e *MoveFlagError) Error() string { return e.err.Error() }

func (e *MoveFlagError) Unwrap() error { return e.err }

// creatingActivation is the activation of a dry run that switches to name, a profile of yours the
// move would create (moveEdit.creating): loadStack reads it as a profile whose file is gone, and
// the move puts its layer on top.
func (e *Engine) creatingActivation(sc Scope, name, file string) (*activation, error) {
	infos, _, err := e.visible(sc)
	if err != nil {
		return nil, err
	}
	s := shown(name)
	report := &Activation{Changes: []LayerChange{}, Named: name, File: shown(file), Turned: true, Profile: &s}
	return &activation{infos: infos, next: &ledger.Active{Name: name, By: ledger.BySwitch}, action: ProfileSwitch, report: report, changed: true}, nil
}

// createTarget creates t, a profile of yours a move names that does not exist (spec 2026-10-29
// §6.2), before the move's edits are built, and reads it into t. undo removes it again when it
// still holds what was written (one edited since is the user's, and stays).
func (e *Engine) createTarget(t *writeTo) (undo func() error, err error) {
	var none *profile.Profile
	dst, data, err := none.Edit(t.Path, manifest.Change{})
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return nil, err
	}
	if err := writeSynced(dst, data); err != nil {
		return nil, err
	}
	undo = func() error {
		b, err := os.ReadFile(dst)
		if errors.Is(err, os.ErrNotExist) || err == nil && string(b) != string(data) {
			return nil
		}
		if err != nil {
			return err
		}
		return os.Remove(dst)
	}
	if t.read, err = profile.Load(t.Path); err != nil {
		return nil, errors.Join(err, undo())
	}
	return undo, nil
}

// moveEdits is the edits of a move of req.Modules from src's layers to t, a file of dst's scope:
// rm's of src's files, add's of dst's, one edit when src and dst are one stack (a move within a
// scope); and the Edits it reports. The entry moves whole: its picks, never a pin into your
// profile; into project.yml or a project profile, the version src's lock.json records, else its
// own pin (spec 2026-10-29 §6.2). An entry without targets of its own gets those it installs to
// when t would give it others (§4 S4: the move changes no installed file). A target that lists
// the module asks to replace it.
func (e *Engine) moveEdits(ctx context.Context, req MoveRequest, src, dst *moveStack, t writeTo) (rm, add *moveEdit, _ []Edit, _ error) {
	rm, add = newMoveEdit(), newMoveEdit()
	across := src.sc.Key() != dst.sc.Key()
	if !across {
		add = rm
	}
	var removed []Edit
	var added []manifest.Entry
	var carried []string // the modules whose picks leave selections.yaml with them, as shown
	var sel *config.Selections
	if src.sc.Kind == target.Project && t.Profile {
		var err error
		if sel, err = e.selections(); err != nil {
			return nil, nil, nil, err
		}
	}
	for _, m := range req.Modules {
		if slices.Contains(add.modules, m) {
			continue
		}
		from, err := e.moveSource(src, req.From, m, offFlags(req))
		if err != nil {
			return nil, nil, nil, err
		}
		if from.Name == t.Name && (!across || from.Path == t.Path) { // across, a profile of yours is one file at both scopes
			return nil, nil, nil, fmt.Errorf("%s is in %s already", shown(m), t.label)
		}
		if t.layerEntry(m) != nil {
			if err := e.confirmReplace(req, t, m); err != nil {
				return nil, nil, nil, err
			}
		}
		ent := *from.layerEntry(m)
		if from.read != nil {
			if k := slices.IndexFunc(from.read.Modules, func(x manifest.Entry) bool { return x.Name == m }); k >= 0 {
				ent.Only, ent.Skip = slices.Clone(from.read.Modules[k].Only), slices.Clone(from.read.Modules[k].Skip)
			}
		}
		if sel != nil && !from.Profile {
			// project.yml's picks live in selections.yaml (picksAt); the entry takes them with it
			// (spec 2026-11-03 A1: the move changes no installed file).
			w, err := e.scopeSection(src.sc)
			if err != nil {
				return nil, nil, nil, err
			}
			picked := sel.Entry(w, m)
			carry := carryPicks(picked)
			if !picked.Empty() {
				ent.Only, ent.Skip = nil, nil
				if len(carry.Only) > 0 {
					ent.Only = patternStrings(carry.Only)
				}
				if len(carry.Skip) > 0 {
					ent.Skip = patternStrings(carry.Skip)
				}
				rm.selections = append(rm.selections, config.Change{Section: w, Module: m})
				rm.written = append(rm.written, sel.WrittenKey(w, m))
				carried = append(carried, shown(m))
			}
			lost, err := e.lostPicks(sel, w, t, m, carry, src.led.Modules[m], ent.RulesOn())
			if err != nil {
				return nil, nil, nil, err
			}
			if lost != nil {
				rm.lost = append(rm.lost, lost)
			}
		}
		if in := src.led.Modules[m]; across && in != nil {
			// Across scopes the entry gets the targets it is installed to, never its file's line,
			// which the source scope may not have installed (or trusted) yet.
			ent.Targets = nil
			if was := in.InstalledTargets(); !sameSet(was, fileTargets(t, dst.man)) {
				ent.Targets = was
			}
		} else if len(ent.Targets) == 0 {
			was := from.Entries[slices.IndexFunc(from.Entries, func(x manifest.Entry) bool { return x.Name == m })].Targets
			if len(was) == 0 {
				was = src.man.Targets
			}
			if len(was) > 0 && !sameSet(was, fileTargets(t, dst.man)) {
				ent.Targets = slices.Clone(was)
			}
		}
		pin := [2]string{ent.Commit, ent.Digest}
		ent.Commit, ent.Digest = "", ""
		if !t.Profile || t.Key != "" {
			in := src.led.Modules[m]
			if in != nil && in.Digest != "" && in.Source == ent.Source && in.Ref == ent.Ref && in.Path == ent.Path && !source.IsFolderForm(ent.Source) {
				ent.Commit, ent.Digest = in.Commit, in.Digest
			} else {
				ent.Commit, ent.Digest = pin[0], pin[1]
			}
		}
		if across {
			// A local source the project reads relative to its root is absolute everywhere else,
			// as a profile of yours records it.
			ent.Source = fileSource(src.sc, writeTo{Layer: Layer{Profile: true}}, ent.Source)
		} else if !from.Profile || from.Key != "" {
			k := slices.IndexFunc(from.Entries, func(x manifest.Entry) bool { return x.Name == m })
			add.origin[m] = moveOrigin{part: from.Key, view: trust.EntryOf(from.Entries[k])}
		}
		rm.modules = append(rm.modules, m)
		if across {
			add.modules = append(add.modules, m)
		}
		rm.remove(from, m)
		added = append(added, add.add(dst.sc, t, ent))
		removed = append(removed, editsOf(ctx, src.sc, from, nil, []string{m}, nil)...)
	}
	rm.args = moveArgs(req)
	add.args = rm.args
	into := editsOf(ctx, dst.sc, t, added, nil, nil)
	for i := range into {
		if slices.Contains(carried, into[i].Module) {
			into[i].Picks = "carried from selections.yaml"
		}
	}
	return rm, add, append(removed, into...), nil
}

// carryPicks is e, a module's entry in selections.yaml's section of a project, as the picks of a
// profile entry, which are only: and skip: (cherry-pick design §4.1): an include joins the only:
// list when there is one, as --unskip adds to a profile entry's (unskip), and drops the skip equal
// to it. Each list keeps its form, so an exact only: list stays one. What an include does that
// this cannot say, lostPicks finds.
func carryPicks(e config.Entry) config.Entry {
	only := slices.Clone(e.Only)
	if len(only) > 0 {
		for _, inc := range e.Include {
			if !slices.Contains(only, inc) {
				only = append(only, inc)
			}
		}
	}
	skip := slices.DeleteFunc(slices.Clone(e.Skip), func(s config.Pattern) bool { return slices.Contains(e.Include, s) })
	return config.Entry{Only: only, Skip: skip}
}

// picksLostError refuses a move that carries a module out of project.yml into a profile whose
// entry, holding the picks carryPicks makes, would install another set of its installed
// components than selections.yaml's section does (spec 2026-11-03 A1: the move changes no
// installed file). --force moves it all the same; a dry run says it and plans that.
type picksLostError struct{ error }

func (e *picksLostError) Unwrap() error { return e.error }

// lostPicks is the refusal of the move of module, an entry of project.yml whose picks sel's
// section w holds, into t, a profile whose entry would hold picks (carryPicks), when the two
// would install different components of the version lock.json's entry installed records: a
// profile entry has no include:, takes the global skip, and not the global only: list
// (cherry-pick design §4.2). A rule counts only while rulesOn. nil when the sets match, or when
// lola cannot tell them (nothing installed, or the cache no longer holds that version).
func (e *Engine) lostPicks(sel *config.Selections, w config.Where, t writeTo, module string, picks config.Entry, installed *ledger.Entry,
	rulesOn bool) (*picksLostError, error) {
	comps, known, err := e.installedSet(module, installed)
	if err != nil || !known {
		return nil, err
	}
	section := sel.Exclusions(w, module)
	global := sel.Entry(config.Global(), module)
	profiled := config.Layered(config.Picks(t.Name), global, picks)
	var reasons, removed, added []string
	reason := func(r string) {
		if !slices.Contains(reasons, r) {
			reasons = append(reasons, r)
		}
	}
	matching := func(ps []config.Pattern, c Component) []string {
		var out []string
		for _, p := range ps {
			if p.Matches(c.Kind, c.Name) {
				out = append(out, untrusted.Text(p.String(), untrusted.MaxName))
			}
		}
		return out
	}
	for _, c := range comps {
		if c.Kind == config.KindRule && !rulesOn {
			continue
		}
		was, now := section.Excludes(c.Kind, c.Name), profiled.Excludes(c.Kind, c.Name)
		switch {
		case was == now:
			continue
		case now:
			// The section keeps c only by an include, which the profile entry lacks; what excludes
			// it there is the global skip or a skip carried from the section (carryPicks keeps every
			// include in an only: list, so that list never leaves c out).
			removed = append(removed, untrusted.Text(c.String(), untrusted.MaxName))
			inc := strings.Join(matching(sel.Entry(w, module).Include, c), ", ")
			if g := matching(global.Skip, c); len(g) > 0 {
				reason(fmt.Sprintf("include: %s cannot travel: a profile entry has no include:, and the global skip %s applies to it", inc, g[0]))
			} else {
				reason(fmt.Sprintf("include: %s cannot travel: a profile entry has no include: to override the skip %s", inc, matching(picks.Skip, c)[0]))
			}
		default:
			// Only the global only: list leaves out what the profile entry keeps: every other pick
			// of the section is carried, or an include that keeps the component in both.
			added = append(added, untrusted.Text(c.String(), untrusted.MaxName))
			reason("the global only: list in selections.yaml does not apply to a profile entry")
		}
	}
	if len(reasons) == 0 {
		return nil, nil
	}
	var change []string
	if len(removed) > 0 {
		change = append(change, "remove "+strings.Join(removed, ", "))
	}
	if len(added) > 0 {
		change = append(change, "install "+strings.Join(added, ", "))
	}
	return &picksLostError{fmt.Errorf("%s's picks in %s cannot move into %s as they are: %s; the move would %s here", shown(module),
		untrusted.Text(sel.Origin(w, module), untrusted.MaxName), t.label, strings.Join(reasons, "; "), strings.Join(change, " and "))}, nil
}

// offFlags is the flags of the hint that turns off the remote profile a move's entry comes from:
// req's output flags. It names another command, so never -y (R-8).
func offFlags(req MoveRequest) []string {
	return slices.Clone(req.Options.Display)
}

// moveArgs is req as lola profile takes it, after the scope's flags: the source named, the
// modules, --to, --switch or --keep-off, and -y as given.
func moveArgs(req MoveRequest) []string {
	var out []string
	if req.From != "" {
		out = append(out, req.From)
	}
	out = append(out, "--"+string(ProfileMove), strings.Join(req.Modules, ","), "--to", req.To)
	switch {
	case req.Switch:
		out = append(out, "--switch")
	case req.KeepOff:
		out = append(out, "--keep-off")
	}
	if req.Yes {
		out = append(out, "-y")
	}
	return out
}

// confirmReplace asks whether a move replaces the entry t has for module (spec 2026-10-29 §6.2):
// -y and --force answer it; on a terminal Replace asks; off one the move is refused, hinting -y.
func (e *Engine) confirmReplace(req MoveRequest, t writeTo, module string) error {
	if req.Yes || req.Options.Force {
		return nil
	}
	if req.Replace == nil {
		return errs.WithHints(fmt.Errorf("%s lists %s already; lola needs to know whether to replace its entry, and there is no terminal to ask on",
			t.label, shown(module)), e.flagRun(req.Scope, req.Options.Target, "profile", "", WhereChoice{Flags: []string{"--to", req.To, "-y"}}))
	}
	ok, err := req.Replace(module, t.label)
	if err == nil && !ok {
		err = ErrDeclined
	}
	return err
}
