package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/trust"
)

// installMoveHints is what follows the entries a.modules, which an install added to a.t at
// a.sp's scope (spec 2026-10-29 §3.7; plan 12.2b ambiguity 6): each line one lola profile --move of
// them all that runs as printed in the state the install leaves. Into another profile of yours
// visible there that lists none of them (with --switch when it is off, and only when switchTarget
// takes it), then at user scope into the base when t is a profile and the base lists none: two at
// most, then the listing. With none, after a write to a shared file (project.yml, a project
// profile), into a new profile of yours, switched to. Then into the other scope, when the run
// (runs, its scopes) does not act on it and it lists none of them: every project after a project
// write; this project only after a user-scope write in a found project, into a profile of yours
// there switchTarget takes (the one on there first, then by name) or, with none, the two-step
// uninstall and install (with the targets they have here when the project names none, and only
// when they share them). No hint names project.yml or a project profile as a target. Off a
// terminal (ask false) each move that may remove files carries -y, and a switch that turns a
// profile off and removes its modules says so; every hint carries display, the run's output flags.
// When t is off here, each move names t as its source, an off target takes --keep-off, a move that
// installs them (into a profile on, or the base) only when it runs as printed (movesAsPrinted),
// and there is no keep-it-to-yourself or cross-scope line; when t is a project profile that is off, no line.
func (e *Engine) installMoveHints(ctx context.Context, a installAdded, runs []Scope, ask bool, display []string) []Notice {
	sp, t, modules := a.sp, a.t, a.modules
	if !t.on && t.Key != "" {
		return nil // a project profile that is off: the saved-to line says what to do (plan 12.5 P3)
	}
	infos, err := sp.profiles()
	if err != nil {
		return nil // no listing: no hint can name a profile
	}
	var out []Notice
	// lead is the line's words before its mark, clause what a switch turns off.
	hint := func(lead, clause, mark string, sc Scope, args []string) {
		if clause != "" {
			lead += " (" + clause + ")"
		}
		out = append(out, Notice{Level: LevelInfo, Text: lead + mark + " " + e.Command(sc, "profile", append(args, display...)...)})
	}
	// from names t as the moves' source when it is not on here: what was saved to it is in no
	// layer here, so a move without a source finds nothing, and with nothing installed from it a
	// move into a profile that is off keeps that profile off (plan 12.5 ambiguity 4).
	from := ""
	if !t.on {
		from = strings.TrimPrefix(layerLabel(infos, t.Name), "profile ")
	}
	move := func(to string, off bool) []string {
		if from != "" {
			return moveArgs(MoveRequest{From: from, Modules: modules, To: to, KeepOff: off})
		}
		return moveArgs(MoveRequest{Modules: modules, To: to, Switch: off, Yes: off && !ask})
	}
	type choice struct {
		name, clause string
		on           bool
	}
	var others []choice
	more := false           // a third alternative, which the listing stands for
	var mine []profile.Info // your profiles other than t
	for _, i := range infos {
		if i.Kind != profile.KindUser || ledger.FromProfile(i.Qualified()) == t.Name {
			continue
		}
		mine = append(mine, i)
		if len(others) == 2 {
			_, _, ok := e.switchable(sp.layers, i, modules)
			more = more || ok
			continue
		}
		name := strings.TrimPrefix(layerLabel(infos, ledger.FromProfile(i.Qualified())), "profile ")
		if from != "" {
			// --keep-off switches nothing, so installs nothing; a move into a profile that is on
			// installs them first, which may need trust or a hook's consent.
			if on, _, ok := e.switchable(sp.layers, i, modules); ok && (!on || e.movesAsPrinted(ctx, MoveRequest{Scope: sp.sc, From: from, Modules: modules, To: name})) {
				others = append(others, choice{name: name, on: on})
			}
			continue
		}
		if on, clause, ok := e.switchTarget(ctx, sp.layers, infos, i, MoveRequest{Scope: sp.sc, Modules: modules, To: name}); ok {
			others = append(others, choice{name, clause, on})
		}
	}
	if sp.sc.Kind == target.User && t.Profile && !t.Base && !listsAny(sp.layers[0], modules) {
		if len(others) == 2 {
			more = true
		} else if from == "" || e.movesAsPrinted(ctx, MoveRequest{Scope: sp.sc, From: from, Modules: modules, To: baseName}) {
			others = append(others, choice{name: baseName, on: true})
		}
	}
	for _, c := range others {
		hint("meant for another profile", c.clause, "?", sp.sc, move(c.name, !c.on))
	}
	if more {
		out = append(out, Notice{Level: LevelInfo, Text: "other profiles: " + e.Command(sp.sc, "profile", display...)})
	}
	if from != "" {
		return out
	}
	if len(others) == 0 && (!t.Profile || t.Key != "") {
		hint("keep it to yourself", switchesOff(sp.layers, infos, Layer{}, modules), ":", sp.sc, move(freeName(infos, "mine"), true))
	}
	user := Scope{Kind: target.User}
	if sp.sc.Kind == target.Project {
		if e.unlisted(user, t, modules, runs) != nil {
			args := e.acrossArgs(MoveRequest{To: baseName}, &moveStack{sc: user}, &moveStack{sc: sp.sc}, "", modules, !ask)
			hint("meant for every project", "", "?", user, args)
		}
		return out
	}
	cwd, err := e.Getwd()
	if err != nil {
		return out
	}
	root, found := findProject(cwd)
	project := Scope{Kind: target.Project, ProjectDir: root}
	if !found || e.IsHome(project) {
		return out
	}
	there := e.unlisted(project, t, modules, runs)
	if there == nil {
		return out
	}
	// The profile of yours on there takes them without a switch; the others by name.
	slices.SortStableFunc(mine, func(a, b profile.Info) int {
		onThere := func(i profile.Info) bool { return there.led.Profile != nil && there.led.Profile.Name == i.Qualified() }
		switch {
		case onThere(a) && !onThere(b):
			return -1
		case onThere(b) && !onThere(a):
			return 1
		}
		return 0
	})
	for _, i := range mine {
		req := MoveRequest{Scope: project, Other: &user, Modules: modules, To: i.Qualified()}
		if on, clause, ok := e.switchTarget(ctx, there.layers, there.infos, i, req); ok {
			hint("meant for this project only", clause, "?", project, moveArgs(MoveRequest{Modules: modules, To: i.Qualified(), Switch: !on, Yes: !ask}))
			return out
		}
	}
	if run := e.intoProjectRun(a, project, there, modules, ask, display); run != "" {
		out = append(out, Notice{Level: LevelInfo, Text: "meant for this project only? " + run})
	}
	return out
}

// intoProjectRun is the two-step run that puts modules, entries a wrote at user scope, into
// project instead (there, its move stack): their uninstall at user scope, then their install in
// project, with the targets they have at user scope when the project names none, or names ones
// its trust record does not cover for the file the install writes there (targetsCovered), which
// an install without -t refuses; "" when those differ between modules, as one install takes one
// -t, or when lola cannot tell. Off a terminal (ask false) the uninstall carries -y; both carry
// display, the run's output flags.
func (e *Engine) intoProjectRun(a installAdded, project Scope, there *moveStack, modules []string, ask bool, display []string) string {
	t, _, err := e.writeTarget(project, there.layers, there.infos, TargetRequest{}, "install", "", nil)
	if err != nil {
		return ""
	}
	rec, err := e.trustRecord(project)
	if err != nil {
		return ""
	}
	again := slices.Clone(modules)
	if len(there.man.Targets) == 0 || !targetsCovered(rec, t, there.man) {
		var ts []string
		for k, m := range modules {
			own := a.sp.targets
			if j := slices.IndexFunc(a.entries, func(x manifest.Entry) bool { return x.Name == m }); j >= 0 && len(a.entries[j].Targets) > 0 {
				own = a.entries[j].Targets
			}
			if k > 0 && !slices.Equal(own, ts) {
				return ""
			}
			ts = own
		}
		again = append(again, "-t", strings.Join(ts, ","))
	}
	var yes []string
	if !ask {
		yes = []string{"-y"}
	}
	return e.Command(Scope{Kind: target.User}, "uninstall", slices.Concat(modules, yes, display)...) + ", then " +
		e.Command(project, "install", append(again, display...)...)
}

// inProjectNotices warns, once, at the write, of each entry a.modules that an install added to
// a.t, a file of yours, whose local source lies inside a project (inProject; spec 2026-10-29
// §16.2); a later sync says nothing of it. After a user-scope write a warning is followed by the
// two-step uninstall and install into that project (intoProjectRun), when no layer there lists it,
// the run (runs) does not act on it, and moves, the run's move hints, do not name that run already.
func (e *Engine) inProjectNotices(a installAdded, runs []Scope, opts Options, moves []Notice) []Notice {
	if !a.t.Profile || a.t.Key != "" {
		return nil
	}
	var out []Notice
	for _, m := range a.modules {
		k := slices.IndexFunc(a.entries, func(x manifest.Entry) bool { return x.Name == m })
		if k < 0 {
			continue
		}
		p, ok := e.inProject(besideProfile(a.entries[k].Source, filepath.Dir(a.t.Path)))
		if !ok {
			continue
		}
		out = append(out, Notice{Level: LevelWarn, Text: p.text(m, a.t.Path)})
		if a.sp.sc.Kind != target.User {
			continue
		}
		project := Scope{Kind: target.Project, ProjectDir: p.root}
		there := e.unlisted(project, a.t, []string{m}, runs)
		if there == nil {
			continue
		}
		run := e.intoProjectRun(a, project, there, []string{m}, opts.AskWhere != nil, opts.Display)
		if run != "" && !slices.ContainsFunc(moves, func(n Notice) bool { return strings.HasSuffix(n.Text, " "+run) }) {
			out = append(out, Notice{Level: LevelInfo, Text: "keep it in the project instead: " + run})
		}
	}
	return out
}

// switchable is i, a profile of yours, as the target of a move of modules at a scope with layers,
// read without planning: whether it is on there, and, when it is off, its layer and whether a switch
// to it installs anything (an entry other than modules). ok is false when
// i lists one of modules (the move would replace its entry) or cannot be read.
func (e *Engine) switchable(layers []Layer, i profile.Info, modules []string) (on bool, off *Layer, ok bool) {
	if k := slices.IndexFunc(layers, func(l Layer) bool { return l.Name == ledger.FromProfile(i.Qualified()) }); k >= 0 {
		return true, nil, !listsAny(layers[k], modules)
	}
	l, err := e.offLayer(i)
	if err != nil || l.read.Shape() == profile.ShapeBundle || listsAny(l, modules) { // a bundle takes no module (spec 2026-10-29 §6.3)
		return false, nil, false
	}
	return false, &l, true
}

// switchTarget is i, a profile of yours, as the target of req, the move a hint names into it at a
// scope with layers and profiles infos (req.To naming i): whether it is on there and, when it is
// off, what the switch to it turns off (switchesOff). ok is false when switchable refuses it, or,
// when the switch installs anything, when the move does not run as printed off a terminal: its dry
// run, planned as the sync plans it, fails or needs trust or a hook's consent, which no hint
// answers. One plan per profile it is asked about.
func (e *Engine) switchTarget(ctx context.Context, layers []Layer, infos []profile.Info, i profile.Info, req MoveRequest) (on bool, clause string, ok bool) {
	on, off, ok := e.switchable(layers, i, req.Modules)
	if !ok || on {
		return on, "", ok
	}
	installs := slices.ContainsFunc(off.Entries, func(ent manifest.Entry) bool { return !slices.Contains(req.Modules, ent.Name) })
	if installs {
		req.Switch, req.Yes = true, true
		if !e.movesAsPrinted(ctx, req) {
			return false, "", false
		}
	}
	return false, switchesOff(layers, infos, *off, req.Modules), true
}

// movesAsPrinted reports whether req, the move a hint names, runs as printed off a terminal: its
// dry run, planned as the sync plans it, succeeds and needs no trust or hook's consent, which no
// hint answers.
func (e *Engine) movesAsPrinted(ctx context.Context, req MoveRequest) bool {
	req.Options = Options{DryRun: true}
	res, err := e.Move(ctx, req)
	if err != nil {
		return false
	}
	return !slices.ContainsFunc(res.Syncs, func(r *SyncResult) bool { return !asPrinted(r) })
}

// asPrinted reports whether r, a dry run's sync, needs no trust or hook's consent, which no hint
// answers.
func asPrinted(r *SyncResult) bool {
	return r.needs == 0 && (r.Trust.Status == trust.StatusTrusted || r.Trust.Status == TrustStatusNotApplicable)
}

// switchesOff says what a switch to the profile to, off at a scope with layers, turns off: the
// profile on there (a bundle, whole), when it lists a module other than modules that neither the
// base nor to lists, which the switch removes. "" when nothing is removed.
func switchesOff(layers []Layer, infos []profile.Info, to Layer, modules []string) string {
	if len(layers) < 2 {
		return ""
	}
	top := layers[len(layers)-1]
	name := strings.TrimPrefix(layerLabel(infos, top.Name), "profile ")
	if top.Bundle != "" {
		name = "bundle " + bundleLabel(infos, top.Bundle) // a switch turns the whole bundle off
	}
	for _, l := range layers[1:] {
		for _, ent := range l.Entries {
			if !slices.Contains(modules, ent.Name) && !listsAny(layers[0], []string{ent.Name}) && !listsAny(to, []string{ent.Name}) {
				return "switches " + name + " off, removing its modules"
			}
		}
	}
	return ""
}

// listsAny reports whether l has an entry of one of modules.
func listsAny(l Layer, modules []string) bool {
	return slices.ContainsFunc(modules, func(m string) bool { return (writeTo{Layer: l}).layerEntry(m) != nil })
}

// unlisted is sc's move stack when no layer of sc lists one of modules, nor will once t, the file
// the install writes, has them (a profile of yours on at sc too), and the run (runs, its scopes)
// does not act on sc: the scope a move hint may carry modules into. nil otherwise, or when sc
// cannot be read.
func (e *Engine) unlisted(sc Scope, t writeTo, modules []string, runs []Scope) *moveStack {
	if slices.ContainsFunc(runs, func(o Scope) bool { return o.Key() == sc.Key() }) {
		return nil
	}
	st, err := e.moveStack(sc)
	if err != nil {
		return nil
	}
	want, _ := desired(st.layers)
	if slices.ContainsFunc(modules, func(m string) bool { return find(want, m) != nil }) || t.Profile && slices.ContainsFunc(st.layers, func(l Layer) bool { return l.Name == t.Name }) {
		return nil
	}
	return st
}

// freeName is want, or want-2, want-3 … the first name no profile in infos has, of any kind (a
// bare --to names the user's, else the project's, else a remote one): the name a hint offers for a
// new profile of yours (spec 2026-10-29 §3.7).
func freeName(infos []profile.Info, want string) string {
	taken := func(n string) bool {
		return slices.ContainsFunc(infos, func(i profile.Info) bool { return i.Name == n })
	}
	name := want
	for k := 2; taken(name); k++ {
		name = fmt.Sprintf("%s-%d", want, k)
	}
	return name
}
