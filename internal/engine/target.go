package engine

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/target"
)

// writeTo is the file an edit of a scope writes (spec 2026-10-29 §3.5): Layer, the layer it is,
// whose Name is lock.json's from and every notice's name for it, and whose Profile, Key, Base and
// read say what kind of file it is and hold its parse (read nil for project.yml and for a file of
// yours that does not exist yet); on, whether the scope loads it (the base, or the profile on);
// and label, how a write line or a question names it.
type writeTo struct {
	Layer
	on    bool
	label string
}

// from is lock.json's from of an entry t writes: "" for project.yml.
func (t writeTo) from() string {
	if t.Profile {
		return t.Name
	}
	return ""
}

// kind is t's kind as an edit names it (spec 2026-10-29 §9): base for the scope's base
// (project.yml, default.yml), project for a project profile, user for a profile of yours.
func (t writeTo) kind() string {
	switch {
	case !t.Profile || t.Base:
		return "base"
	case t.Key != "":
		return "project"
	}
	return "user"
}

// baseTarget is base, a scope's base layer, as an edit's target: default (base) at user scope,
// project.yml (base) at project scope.
func baseTarget(base Layer) writeTo {
	return writeTo{Layer: base, on: true, label: layerLabel(nil, base.Name)}
}

// TargetRequest is what a writing command was told about where its edit goes (spec 2026-10-29
// §3.5, §3.6): Profile is --profile ("" for none; base, or at user scope default, names the
// scope's base), Override is --override; Rerun, when set, is the user's run without either and
// without its consent flags, with extra appended, and UserRun the run at user scope (its project
// flags replaced by --user): what a refusal hints. Without them a hint is lola <verb> <module>
// with the scope's flags. Flag is the flag a Profile choice is given with: --profile when empty,
// --to for a move, whose Rerun leaves its own --to out. Again is what a hint naming another
// command keeps of the run, as Rerun does: -n and its output flags.
//
// Edit says the run is lola profile <Profile> --edit (spec 2026-10-29 §6.2): a hint that would
// name lola install names lola profile <name> --edit (installRun), Rerun takes "--profile <name>" in
// extra as the profile the run names, and the flag form of Override names the write target. New is
// --new with it: Profile names a profile to make, of yours or with project:<name> the project's,
// which the run writes in its transaction (namedTarget). Catalog is the names your catalogs list,
// read before the run locks them (CatalogNames): a bare profile name a hint of Edit's names first
// is qualified when one of them, a module too, has it (editArg).
type TargetRequest struct {
	Profile  string
	Override bool
	Rerun    func(extra ...string) string
	UserRun  func() string
	Flag     string
	Again    []string
	Edit     bool
	New      bool
	Catalog  []string
	// CatalogPartial says lola could not read every catalog: a hint qualifies each profile name.
	CatalogPartial bool
}

// ownFlags is what Rerun leaves out of a lola profile --edit run that a run repeating it keeps:
// its --override and --new.
func (t TargetRequest) ownFlags() []string {
	var out []string
	if t.Override {
		out = append(out, "--override")
	}
	if t.New {
		out = append(out, "--new")
	}
	return out
}

// verb is the command a run with t is, as its refusals name it: install, or profile --edit.
func (t TargetRequest) verb() string {
	if t.Edit {
		return "profile --edit"
	}
	return "install"
}

// installRun is the command a hint of a run with req names to install args at sc: lola install with
// the scope's flags and the run's --profile, or for lola profile --edit (req.Edit) lola profile <the
// profile args name with --profile, else req.Profile> --edit and the rest of args.
func (e *Engine) installRun(sc Scope, req TargetRequest, args ...string) string {
	if !req.Edit {
		if req.Profile != "" && !slices.Contains(args, "--profile") { // the run's own target, kept
			args = append(slices.Clone(args), "--profile", req.Profile)
		}
		return e.Command(sc, "install", args...)
	}
	name, rest := ProfileFlag(args)
	name = cmp.Or(name, req.Profile)
	words := []string{"--edit"}
	if name != "" {
		words = []string{name, "--edit"}
	}
	return e.Command(sc, "profile", append(words, rest...)...)
}

// repickRun is the run that changes the picks of module where from provides it at sc (cherry-pick
// design §6; decision C2 of the 2026-11-01 audit), args following the module, sc's flags kept:
// lola profile <name> --edit <module> for a profile of yours, the project's or your default (named
// as editArg names it among infos, the profiles sc can use, and layers' entries); for project.yml's,
// lola install <module> while no profile is on at sc (on), else lola profile base --edit <module>,
// which asks nothing about where the change goes; for a remote profile's, which lola does not edit,
// the switch to a copy of yours (remoteReadOnly's run), without args.
func (e *Engine) repickRun(sc Scope, layers []Layer, infos []profile.Info, on bool, from, module string, args ...string) string {
	rest := append([]string{module}, args...)
	q, isProfile := strings.CutPrefix(from, ledger.FromProfile(""))
	switch {
	case !isProfile && !on:
		return e.Command(sc, "install", rest...)
	case !isProfile:
		return e.Command(sc, "profile", append([]string{baseName, "--edit"}, rest...)...)
	case remoteLayer(from):
		return e.Command(sc, "profile", freeName(infos, bareName(q)+"-mine"), "--switch", "--new", "--from", q)
	case q == profile.DefaultName:
		return e.Command(sc, "profile", append([]string{q, "--edit"}, rest...)...)
	}
	name := bareName(q)
	if namesake(infos, q) {
		name = q
	}
	return e.Command(sc, "profile", append([]string{e.editArg(sc, layers, infos, nil, false, name), "--edit"}, rest...)...)
}

// ProfileFlag splits args, a hint's words, into the profile their "--profile <name>" names ("" for
// none) and the rest, in order: lola profile <name> --edit puts that profile first instead.
func ProfileFlag(args []string) (string, []string) {
	var name string
	rest := []string{}
	for k := 0; k < len(args); k++ {
		if args[k] == "--profile" && k+1 < len(args) {
			name, k = args[k+1], k+1
			continue
		}
		rest = append(rest, args[k])
	}
	return name, rest
}

// WhereQuestion is a question about where a change goes (spec 2026-10-29 §3.5 row 4, §3.6), which
// neither -y nor --force answers: Title says why it is asked, Choices are its answers in order (a
// picker adds Cancel), Scope the scope it is about and Module the module ("" for the question
// about the profile on).
type WhereQuestion struct {
	Scope   Scope
	Module  string
	Title   string
	Choices []WhereChoice
}

// WhereChoice is one answer of a WhereQuestion: Label as a picker shows it, and its flag form:
// Profile, the --profile it is (base for the base), or Override, --override, or Flags when it is
// neither (--to me -y).
type WhereChoice struct {
	Label    string
	Profile  string
	Override bool
	Flags    []string
}

// ask is q's answer: ask's, or without ask (no terminal) the refusal refused, whose hints are the
// run with each choice's flag form (flagRun).
func (e *Engine) ask(ask func(WhereQuestion) (WhereChoice, error), q WhereQuestion, refused string, req TargetRequest, verb string) (WhereChoice, error) {
	if ask != nil {
		return ask(q)
	}
	hints := make([]string, 0, len(q.Choices))
	for _, c := range q.Choices {
		hints = append(hints, e.flagRun(q.Scope, req, verb, q.Module, c))
	}
	return WhereChoice{}, errs.WithHints(errors.New(refused), hints...)
}

// flagRun is the run again with c's flag form: its Flags, --override, or req's Flag (--profile
// when empty) and c's profile.
func (e *Engine) flagRun(sc Scope, req TargetRequest, verb, module string, c WhereChoice) string {
	flag := c.Flags
	switch {
	case len(flag) > 0:
	case c.Override:
		flag = []string{"--override"}
	default:
		flag = []string{cmp.Or(req.Flag, "--profile"), c.Profile}
	}
	if req.Rerun != nil {
		return req.Rerun(flag...)
	}
	var args []string
	if module != "" {
		args = append(args, module)
	}
	return e.Command(sc, verb, append(args, flag...)...)
}

// inheritedFrom is the winning entry of module when an edit of it at a scope whose layers are
// layers, writing t, edits another layer's entry (spec 2026-10-29 §3.6): one from another layer
// than t (the base, or another member of the bundle on); nil when the entry is t's own, the
// scope has no entry for module, or t is not loaded or is below the winner (save-only).
func inheritedFrom(layers []Layer, t writeTo, module string) *Wanted {
	if !t.on || winnerAbove(layers, t.Name, module) != nil {
		return nil
	}
	want, _ := desired(layers)
	w := find(want, module)
	if w == nil || w.From == t.Name {
		return nil
	}
	return w
}

// providerEntry is w's entry as its layer's file gives it: the source as the scope reads it, its
// own targets (none when it has none), its pin. An entry from a remote profile is its effective one.
func providerEntry(layers []Layer, w *Wanted) *manifest.Entry {
	k := slices.IndexFunc(layers, func(l Layer) bool { return l.Name == w.From })
	if k < 0 || remoteLayer(w.From) {
		ent := w.Entry
		return &ent
	}
	return writeTo{Layer: layers[k]}.layerEntry(w.Name)
}

// sameRequest reports whether ent, what an install asks for, asks what w's entry says (spec
// 2026-10-29 §3.6): its source, ref, path, rules and vars, and the assistants each installs to
// (targets, ent's; wanted, w's), whatever either pins.
func sameRequest(ent, w manifest.Entry, targets, wanted []string) bool {
	return ent.Source == w.Source && ent.Ref == w.Ref && ent.Path == w.Path && ent.RulesOn() == w.RulesOn() &&
		manifest.SameVars(ent.Vars, w.Vars) && sameSet(targets, wanted)
}

// inheritedNotice says an install asked for what module's entry in the layer labeled from says:
// nothing is written, and the module installs from there when lock.json does not record it.
func inheritedNotice(module, from string, installed bool) Notice {
	if installed {
		return Notice{Level: LevelInfo, Text: shown(module) + " is already installed from " + from}
	}
	return Notice{Level: LevelInfo, Text: shown(module) + " is listed in " + from + "; installing it from there"}
}

// placeEdit is where an install's edit of a module the scope has from w, another layer's entry
// than sp.target's, goes (spec 2026-10-29 §3.6): --override's answer, the target, without asking;
// else the answer to "Change it in <layer>" (the base: the run with --profile base) or "Override it
// in <target>" (the target). A read-only layer, a remote profile on or a member of the bundle on,
// offers Override alone. When the module comes from project.yml and the edit changes its picks
// alone (picksOnly), Change it there is "Change your picks for project.yml's module": they go to
// selections.yaml; picksOnly nil is a question asked before the edit is known (Choices.Place),
// whose Change says where each part goes. Neither -y nor --force answers it; without AskWhere it
// is refused with each answer's flag form.
func (e *Engine) placeEdit(sp *scopePlan, w *Wanted, picksOnly *bool, opts Options) (*writeTo, error) {
	if opts.Target.Override {
		return &sp.target, nil
	}
	provider := layerLabel(sp.infos, w.From)
	short := strings.TrimSuffix(strings.TrimPrefix(provider, "profile "), " (base)")
	own := strings.TrimPrefix(sp.target.label, "profile ")
	override := WhereChoice{Label: "Override it in " + sp.target.label + "  (" + own + " gets its own entry, starting from " + short + "'s)",
		Override: true}
	switch {
	case sp.target.Bundle != "": // a member, which --override alone would ask about again (memberTarget)
		override.Flags = []string{"--profile", own, "--override"}
	case opts.Target.Edit && sp.target.Profile && !sp.target.Base: // lola profile <target> --edit <module> --override (spec 2026-10-29 §6.2)
		override.Flags = []string{"--profile", cmp.Or(opts.Target.Profile, e.editArg(sp.sc, sp.layers, sp.infos, opts.Target.Catalog, opts.Target.CatalogPartial, own)), "--override"}
	}
	verb := opts.Target.verb()
	module := shown(w.Name)
	var q WhereQuestion
	var refused string
	if remoteLayer(w.From) {
		q = WhereQuestion{Scope: sp.sc, Module: w.Name, Title: module + " comes from " + provider + ", which is read-only.", Choices: []WhereChoice{override}}
		refused = module + " comes from " + provider + ", which is read-only; lola " + verb + " needs to know whether to override it in " +
			sp.target.label + ", and there is no terminal to ask on"
	} else {
		change := WhereChoice{Label: "Change it in " + short + "  (every profile sees it)", Profile: baseName}
		switch {
		case sp.layers[0].Profile:
		case picksOnly == nil:
			change.Label = "Change it for project.yml's module  (picks: selections.yaml; other edits: project.yml, shared)"
		case *picksOnly:
			change.Label = "Change your picks for project.yml's module  (selections.yaml, not shared)"
		}
		title := module + " comes from " + provider + ", loaded under every profile " + scopeWhere(sp.sc) + "."
		if w.From != sp.layers[0].Name { // another member of the bundle on (spec 2026-10-29 §3.6)
			change = WhereChoice{Label: "Change it in " + short + "  (every bundle naming " + short + " sees it)", Profile: short}
			if opts.Target.Edit {
				change.Profile = e.editArg(sp.sc, sp.layers, sp.infos, opts.Target.Catalog, opts.Target.CatalogPartial, short)
			}
			title = module + " comes from " + provider + ", another member of bundle " + bundleLabel(sp.infos, w.bundle) + "."
		}
		q = WhereQuestion{Scope: sp.sc, Module: w.Name, Title: title, Choices: []WhereChoice{change, override}}
		refused = module + " comes from " + provider + "; lola " + verb + " needs to know whether to change it there or override it in " +
			sp.target.label + ", and there is no terminal to ask on"
	}
	c, err := e.ask(opts.AskWhere, q, refused, opts.Target, "install")
	if err != nil {
		return nil, err
	}
	if c.Override {
		return &sp.target, nil
	}
	if k := slices.IndexFunc(sp.layers[1:], func(l Layer) bool { return l.Name == w.From }); k >= 0 {
		member := writeTo{Layer: sp.layers[k+1], on: true, label: layerLabel(sp.infos, w.From)}
		return &member, nil
	}
	base := baseTarget(sp.layers[0])
	return &base, nil
}

// uninstallTarget is the file an uninstall at sc writes without asking (spec 2026-10-29 §6.1):
// --profile's (req.Profile), the profile on when it is yours or the project's, else the base. A
// remote profile on asks nothing here: every entry it provides is refused (removeFrom). A bundle
// on, with members or none (layerlessBundle), has no such file (the zero writeTo): each module is
// removed from the layer it comes from, the base too, once asked (removeFrom), as under a profile.
func (e *Engine) uninstallTarget(sc Scope, layers []Layer, req TargetRequest) (writeTo, []profile.Info, error) {
	if req.Profile == "" && len(layers) < 2 {
		bundle, infos, err := e.layerlessBundle(sc, nil)
		switch {
		case err != nil:
			return writeTo{}, nil, err
		case bundle != "":
			return writeTo{}, infos, nil
		}
		return baseTarget(layers[0]), infos, nil
	}
	top := layers[len(layers)-1]
	if req.Profile != "" || !remoteLayer(top.Name) && top.Bundle == "" {
		return e.writeTarget(sc, layers, nil, req, "uninstall", "", nil)
	}
	infos, _, err := e.visible(sc)
	if top.Bundle != "" {
		return writeTo{}, infos, err
	}
	return baseTarget(layers[0]), infos, err
}

// removeFrom is the layer an uninstall removes module's entry from (spec 2026-10-29 §6.1): t, the
// target, when --profile named it or when w, the module's winning entry, is t's own; else w's layer,
// once ask says so (flag form --profile <it>), which neither -y nor --force answers (§3.6); under -t
// (partial), which keeps the entry with fewer targets, the question is about changing it there. A remote
// profile's entry, on or a bundle's member, is refused, hinting the --off that turns the profile on off. A
// refusal's hints keep the run's output flags (opts.Display).
func (e *Engine) removeFrom(sc Scope, layers []Layer, infos []profile.Info, t writeTo, w *Wanted, partial bool, opts Options) (*writeTo, error) {
	if opts.Target.Profile != "" {
		if t.layerEntry(w.Name) == nil {
			return nil, errs.WithHints(fmt.Errorf("%s does not list %s", t.label, shown(w.Name)), "see where it comes from: "+e.Command(sc, "list", opts.Display...))
		}
		return &t, nil
	}
	module := shown(w.Name)
	if remoteLayer(w.From) {
		k := slices.IndexFunc(layers, func(l Layer) bool { return l.Name == w.From })
		return nil, errs.WithHints(fmt.Errorf("%s comes from %s, which is read-only; lola does not edit remote profiles", module, layerLabel(infos, w.From)),
			e.remoteOnHints(sc, infos, layers[k], opts.Display)...)
	}
	if w.From == t.Name {
		return &t, nil
	}
	k := slices.IndexFunc(layers, func(l Layer) bool { return l.Name == w.From })
	from := writeTo{Layer: layers[k], on: true, label: layerLabel(infos, w.From)}
	label, verb := "Remove it from "+from.label, "remove"
	if partial {
		label, verb = "Change it in "+from.label, "change"
	}
	title := module + " comes from " + from.label + ", loaded under every profile " + scopeWhere(sc) + "."
	if k > 0 && from.Bundle != "" {
		title = module + " comes from " + from.label + ", a member of bundle " + bundleLabel(infos, from.Bundle) + "."
	}
	q := WhereQuestion{Scope: sc, Module: w.Name, Title: title, Choices: []WhereChoice{{Label: label, Profile: layerFlag(layers, w.From)}}}
	refused := module + " comes from " + from.label + "; lola uninstall needs to know whether to " + verb + " it there, and there is no terminal to ask on"
	if _, err := e.ask(opts.AskWhere, q, refused, opts.Target, "uninstall"); err != nil {
		return nil, err
	}
	return &from, nil
}

// remoteOnHints is what works for an edit of an entry of l, a remote profile at sc whose profiles
// are infos: turning it off, or, for a remote profile on (not a bundle's member), switching to a
// copy of it of yours (--new --from), which you can edit (spec 2026-10-29 §3.5; plan 12.2b
// carry-forward). A bundle's member goes off with its bundle. display is the run's output flags,
// which each keeps.
func (e *Engine) remoteOnHints(sc Scope, infos []profile.Info, l Layer, display []string) []string {
	if l.Bundle != "" { // or take the member out of the bundle files of yours that list it
		return append([]string{"turn bundle " + bundleLabel(infos, l.Bundle) + " off: " + e.Command(sc, "profile", append([]string{"--off"}, display...)...)},
			e.unlistRuns(sc, l.listed, display)...)
	}
	q := strings.TrimPrefix(l.Name, ledger.FromProfile(""))
	return []string{"turn it off: " + e.Command(sc, "profile", append([]string{"--off"}, display...)...),
		"or switch to a copy of yours: " + e.Command(sc, "profile", append([]string{freeName(infos, bareName(q)+"-mine"), "--switch", "--new", "--from", q},
			display...)...)}
}

// layerFlag is the --profile that names the layer called name of a scope whose layers are layers:
// base for the base, else the profile's qualified name.
func layerFlag(layers []Layer, name string) string {
	if name == layers[0].Name {
		return baseName
	}
	return strings.TrimPrefix(name, ledger.FromProfile(""))
}

// stillWanted is the winning entry of module once the layer named gone no longer lists it: another
// layer's, which keeps it installed; nil when none lists it.
func stillWanted(layers []Layer, gone, module string) *Wanted {
	rest := make([]Layer, 0, len(layers))
	for _, l := range layers {
		if l.Name == gone {
			l.Entries = slices.DeleteFunc(slices.Clone(l.Entries), func(e manifest.Entry) bool { return e.Name == module })
		}
		rest = append(rest, l)
	}
	want, _ := desired(rest)
	return find(want, module)
}

// staysNotices say an uninstall left module installed because rest, another layer's entry, still
// wants it (spec 2026-10-29 §6.1): the run that removes it there (none for a remote profile's, which
// lola does not edit); and, when that entry is not the one installed (installed, lock.json's), the
// sync that applies it. The run is a new one: it keeps the scope's flags only (Engine.Command).
func (e *Engine) staysNotices(sc Scope, layers []Layer, infos []profile.Info, rest *Wanted, installed *ledger.Entry) []Notice {
	label := layerLabel(infos, rest.From)
	text := shown(rest.Name) + " stays: " + label + " lists it too"
	if !remoteLayer(rest.From) {
		text += " → also remove it there: " + e.flagRun(sc, TargetRequest{}, "uninstall", rest.Name, WhereChoice{Profile: layerFlag(layers, rest.From)})
	}
	out := []Notice{{Level: LevelInfo, Text: text}}
	if installed == nil || installed.Source != rest.Source || installed.Ref != rest.Ref || installed.Path != rest.Path || installed.From != ledgerFrom(rest.From) {
		out = append(out, Notice{Level: LevelInfo, Text: shown(rest.Name) + ": " + label + "'s entry applies at the next sync → " + e.SyncCommand(sc, rest.Name)})
	}
	return out
}

// ledgerFrom is lock.json's from of a layer's name: "" for the scope's manifest.
func ledgerFrom(layer string) string {
	if strings.HasPrefix(layer, ledger.FromProfile("")) {
		return layer
	}
	return ""
}

// pickSection is where module's picks are read and saved at a scope whose layers are layers,
// writing t, whose picks are saved in picks: the section of the layer the module comes from when an
// edit of it is inherited (inheritedFrom), so the edit starts from that entry's picks (spec
// 2026-10-29 §3.6), which for project.yml is selections.yaml's section of the scope, scope; else
// picks.
func pickSection(layers []Layer, t writeTo, picks, scope config.Where, module string) config.Where {
	w := inheritedFrom(layers, t, module)
	switch {
	case w == nil:
		return picks
	case strings.HasPrefix(w.From, ledger.FromProfile("")):
		return config.Picks(w.From)
	}
	return scope
}

// layerLabel is how a write line or a question names the layer called name (Layer.Name) of a
// scope that can use infos: default (base) and project.yml (base) for the bases; profile <name>
// for one of yours or the project's, its bare name unless a profile of another kind has it (the
// context line's rule); remote:<name> for a remote one.
func layerLabel(infos []profile.Info, name string) string {
	q, isProfile := strings.CutPrefix(name, ledger.FromProfile(""))
	switch {
	case !isProfile:
		return shown(name) + " (base)"
	case q == profile.DefaultName:
		return "default (base)"
	case remoteLayer(name):
		return shown(q)
	}
	if namesake(infos, q) {
		return "profile " + shown(q)
	}
	return "profile " + shown(bareName(q))
}

// namesake reports whether a profile in infos of another kind than q (qualified) has its name, so
// output names q qualified (layerLabel's rule); your default is never one.
func namesake(infos []profile.Info, q string) bool {
	kind, bare, _ := strings.Cut(q, ":")
	return slices.ContainsFunc(infos, func(i profile.Info) bool {
		return i.Name == bare && string(i.Kind) != kind && i.Kind != profile.KindDefault
	})
}

// scopeWhere names sc in a sentence about where a profile is on: at user scope, in this project.
func scopeWhere(sc Scope) string {
	if sc.Kind == target.Project {
		return "in this project"
	}
	return "at user scope"
}

// writeTarget is the file an edit at sc writes (spec 2026-10-29 §3.5), layers being the scope's
// stack (assemble): --profile's profile (req.Profile), or the base it names; else the profile on,
// when it is yours or the project's; else, while a remote profile is on, the base once ask says so
// (nil: a refusal hinting --profile base); else the base. A profile not on in the scope is read for
// the edit (offLayer): the run writes it and applies nothing of it. infos is the profiles sc can
// use, nil when the caller did not list them: it is listed when a name must be resolved or labeled,
// and returned. verb names the command (install, uninstall), what what it writes as the user typed
// it ("" when unknown), for the member question. While a bundle is on, the member ask names
// (memberTarget).
func (e *Engine) writeTarget(sc Scope, layers []Layer, infos []profile.Info, req TargetRequest, verb, what string,
	ask func(WhereQuestion) (WhereChoice, error)) (writeTo, []profile.Info, error) {
	base := baseTarget(layers[0])
	if req.Profile == "" && len(layers) < 2 {
		bundle, infos, err := e.layerlessBundle(sc, infos)
		if err != nil {
			return writeTo{}, nil, err
		}
		if bundle == "" {
			return base, infos, nil
		}
		t, err := e.memberTarget(sc, layers, infos, bundle, req, verb, what, ask)
		return t, infos, err
	}
	if infos == nil {
		var err error
		if infos, _, err = e.visible(sc); err != nil {
			return writeTo{}, nil, err
		}
	}
	if req.Profile != "" {
		t, err := e.namedTarget(sc, layers, infos, req, verb, base)
		return t, infos, err
	}
	top := layers[len(layers)-1]
	if top.Bundle != "" {
		t, err := e.memberTarget(sc, layers, infos, top.Bundle, req, verb, what, ask)
		return t, infos, err
	}
	if !remoteLayer(top.Name) {
		return writeTo{Layer: top, on: true, label: layerLabel(infos, top.Name)}, infos, nil
	}
	name, baseFile := shown(strings.TrimPrefix(top.Name, ledger.FromProfile(""))), e.baseFile(sc)
	q := WhereQuestion{Scope: sc, Title: name + " is read-only.",
		Choices: []WhereChoice{{Label: "Write to the base (" + baseFile + ")", Profile: baseName}}}
	refused := name + " is read-only; lola " + verb + " needs to know whether to write to the base (" + baseFile + "), and there is no terminal to ask on"
	if _, err := e.ask(ask, q, refused, req, verb); err != nil {
		if ask != nil {
			return writeTo{}, infos, err
		}
		// Or keep the remote profile on, bundled with a profile of yours that takes the edit
		// (spec 2026-10-29 §3.5 row 4): made empty by the bundle's --new.
		bare := bareName(strings.TrimPrefix(top.Name, ledger.FromProfile("")))
		members := []string{string(profile.KindRemote) + ":" + bare, freeName(infos, bare+"-extras")}
		run := e.bundleRun(sc, freeName(infos, bare+"-work"), members, req.Again)
		if he, ok := errors.AsType[*errs.Error](err); ok { // after the --profile base hint, its refusal kept as it is
			he.Hints = append(he.Hints, "or bundle it with a profile of yours: "+run)
		}
		return writeTo{}, infos, err
	}
	return base, infos, nil
}

// memberTarget is writeTarget while a bundle is on (spec 2026-10-29 §3.5 row 3, §3.6): the
// member ask names among the writable ones, yours and the project's, last first (the last wins
// overlaps). It is asked even with one, so an edit under a bundle never lands in a member the user
// did not name; neither -y nor --force answers it, and without ask (no terminal) it is refused
// with a --profile per member. A bundle of read-only members only asks where row 4 does: the
// base, or nothing; so does a bundle that gives no layer (layerlessBundle). on is the bundle's
// qualified name; what is what the edit writes, as typed ("" when unknown).
func (e *Engine) memberTarget(sc Scope, layers []Layer, infos []profile.Info, on string, req TargetRequest, verb, what string,
	ask func(WhereQuestion) (WhereChoice, error)) (writeTo, error) {
	bundle := bundleLabel(infos, on)
	gets := cmp.Or(shown(what), "this change")
	var choices []WhereChoice
	var targets []writeTo
	for k := len(layers) - 1; k > 0; k-- {
		l := layers[k]
		if remoteLayer(l.Name) {
			continue
		}
		label := layerLabel(infos, l.Name)
		c := WhereChoice{Label: strings.TrimPrefix(label, "profile "), Profile: strings.TrimPrefix(label, "profile ")}
		if req.Edit {
			c.Profile = e.editArg(sc, layers, infos, req.Catalog, req.CatalogPartial, c.Profile)
		}
		if len(choices) == 0 {
			c.Label += "  (last member: wins overlaps)"
		}
		choices, targets = append(choices, c), append(targets, writeTo{Layer: l, on: true, label: label})
	}
	if len(choices) == 0 {
		baseFile := e.baseFile(sc)
		q := WhereQuestion{Scope: sc, Title: "bundle " + bundle + " has no profile lola can write.",
			Choices: []WhereChoice{{Label: "Write to the base (" + baseFile + ")", Profile: baseName}}}
		refused := "bundle " + bundle + " has no profile lola can write; lola " + verb + " needs to know whether to write to the base (" + baseFile +
			"), and there is no terminal to ask on"
		_, err := e.ask(ask, q, refused, req, verb)
		if he, ok := errors.AsType[*errs.Error](err); ok && ask == nil {
			// Or give a bundle of yours a profile of yours that takes the edit (--edit --bundle).
			if k := activeInfo(infos, on); k >= 0 && infos[k].Kind == profile.KindUser {
				if p, _, rerr := e.readFile(infos[k]); rerr == nil && len(p.Bundle) < profile.MaxMembers {
					extra := freeName(infos, bareName(on)+"-extras")
					he.Hints = append(he.Hints, "or add a profile of yours to it, made empty: "+e.Command(sc, "profile",
						append([]string{editName(infos, infos[k]), "--edit", "--bundle", strings.Join(append(slices.Clone(p.Bundle), extra), ",")}, req.Again...)...))
				}
			}
		}
		if err != nil {
			return writeTo{}, err
		}
		return baseTarget(layers[0]), nil
	}
	q := WhereQuestion{Scope: sc, Title: bundle + " is a bundle. Which profile gets " + gets + "?", Choices: choices}
	refused := "lola " + verb + " needs to know which profile of bundle " + bundle + " gets " + gets + ", and there is no terminal to ask on"
	c, err := e.ask(ask, q, refused, req, verb)
	if err != nil {
		return writeTo{}, err
	}
	return targets[slices.IndexFunc(choices, func(o WhereChoice) bool { return o.Profile == c.Profile })], nil
}

// layerlessBundle is the qualified name of the bundle on at sc when it gives no layer (spec
// 2026-10-29 §3.5 row 3): an empty bundle, or one whose members are all left out, is still on, so
// an edit asks before it writes the base. "" when no profile is on, its file is gone, or it is not
// a bundle. infos is listed when nil and a profile is on, and returned.
func (e *Engine) layerlessBundle(sc Scope, infos []profile.Info) (string, []profile.Info, error) {
	led, err := ledger.LoadKeepingStrays(e.files(sc).ledger)
	if err != nil || led.Profile == nil {
		return "", infos, err
	}
	if infos == nil {
		if infos, _, err = e.visible(sc); err != nil {
			return "", nil, err
		}
	}
	k := activeInfo(infos, led.Profile.Name)
	if k < 0 || infos[k].Kind == profile.KindRemote {
		return "", infos, nil
	}
	_, x, err := e.members(sc, infos, infos[k])
	if err != nil || !x.bundle {
		return "", infos, err
	}
	return infos[k].Qualified(), infos, nil
}

// baseFile names the scope's base in a question: default at user scope, project.yml in a project.
func (e *Engine) baseFile(sc Scope) string {
	if sc.Kind == target.User {
		return profile.DefaultName
	}
	return e.manifestName(sc)
}

// namedTarget is writeTarget for --profile (req.Profile): base, or default at user scope, is the
// base; default at project scope is refused, hinting the run with base and the run at user scope;
// a remote profile is refused, hinting the base and your profiles (two at most); an ambiguous bare
// name is refused with a hint per profile it names.
func (e *Engine) namedTarget(sc Scope, layers []Layer, infos []profile.Info, req TargetRequest, verb string, base writeTo) (writeTo, error) {
	baseRun := e.flagRun(sc, req, verb, "", WhereChoice{Profile: baseName})
	switch isBase, isDefault := namesBase(req.Profile); {
	case isBase || isDefault && sc.Kind == target.User:
		return base, nil
	case isDefault:
		hints := []string{baseRun}
		if req.UserRun != nil {
			hints = append(hints, req.UserRun())
		}
		return writeTo{}, errs.WithHints(errors.New("default is user scope's base; this project's base is project.yml"), hints...)
	}
	if req.New {
		return e.madeTarget(sc, infos, req)
	}
	i, err := e.resolveProfile(sc, infos, req.Profile, nil)
	if amb, ok := errors.AsType[*ambiguousProfileError](err); ok {
		var hints []string
		for _, f := range amb.found {
			hints = append(hints, e.flagRun(sc, req, verb, "", WhereChoice{Profile: f.Qualified()}))
		}
		return writeTo{}, errs.WithHints(errors.New(amb.Error()), hints...)
	}
	if err != nil {
		return writeTo{}, err
	}
	if i.Kind == profile.KindRemote {
		hints := []string{baseRun}
		for _, o := range infos {
			if o.Kind == profile.KindUser && len(hints) < 3 {
				hints = append(hints, e.flagRun(sc, req, verb, "", WhereChoice{Profile: o.Qualified()}))
			}
		}
		return writeTo{}, errs.WithHints(fmt.Errorf("%s is read-only; lola does not edit remote profiles", shown(i.Qualified())), hints...)
	}
	name := ledger.FromProfile(i.Qualified())
	for _, l := range layers[1:] {
		if l.Name == name {
			return writeTo{Layer: l, on: true, label: layerLabel(infos, name)}, nil
		}
	}
	l, err := e.offLayer(i)
	if err != nil {
		return writeTo{}, e.mixedError(sc, infos, i, err)
	}
	if l.read.Shape() == profile.ShapeBundle {
		return writeTo{}, e.bundleTargetError(sc, infos, i, req, verb)
	}
	return writeTo{Layer: l, label: layerLabel(infos, name)}, nil
}

// madeTarget is namedTarget for lola profile <req.Profile> --edit --new (spec 2026-10-29 §6.2): the
// profile --new makes, yours or with project:<name> the project's, refused as --switch --new
// refuses it (newTarget), with the hints worded as the run words them (req.Rerun): a file of yours,
// or the project's, that the run creates in its transaction and does not apply (it is not on).
func (e *Engine) madeTarget(sc Scope, infos []profile.Info, req TargetRequest) (writeTo, error) {
	k, name, err := e.newTarget(sc, infos, ProfileChange{Action: ProfileEdit, Name: req.Profile, New: true}, e.editRuns(sc, req))
	if err != nil {
		return writeTo{}, err
	}
	q := string(k) + ":" + name
	l := Layer{Name: ledger.FromProfile(q), Path: e.profilePath(sc, k, name), Profile: true}
	if k == profile.KindProject {
		l.Key = name
	}
	return writeTo{Layer: l, label: layerLabel(append(slices.Clone(infos), profile.Info{Name: name, Kind: k, Path: l.Path}), l.Name)}, nil
}

// BundleTargetError is a --profile or --to that names a bundle (spec 2026-10-29 §3.3, §6.3): a
// bundle lists profiles, never modules. Its hints name each writable member in its place.
type BundleTargetError struct{ err error }

func (e *BundleTargetError) Error() string { return e.err.Error() }

func (e *BundleTargetError) Unwrap() error { return e.err }

// bundleTargetError refuses i, a bundle, as the file an edit writes, hinting the run with req's
// flag (--profile, or --to for a move) naming each member lola can write, as the bundle expands
// them at sc, or the base when it has none (empty, or only remote members). A bundle whose members
// cannot be expanded refuses with that expansion's error, which names the file and line to fix.
func (e *Engine) bundleTargetError(sc Scope, infos []profile.Info, i profile.Info, req TargetRequest, verb string) error {
	ms, _, err := e.members(sc, infos, i)
	if err != nil {
		return &BundleTargetError{err}
	}
	var hints []string
	for _, m := range ms {
		if !remoteLayer(m.Name) {
			hints = append(hints, e.flagRun(sc, req, verb, "", WhereChoice{Profile: strings.TrimPrefix(layerLabel(infos, m.Name), "profile ")}))
		}
	}
	text, into := bundleLabel(infos, i.Qualified())+" is a bundle", "one of its members"
	if len(hints) == 0 {
		text, into = text+" with no profile lola can write", "another profile"
		hints = append(hints, e.flagRun(sc, req, verb, "", WhereChoice{Profile: baseName}))
	}
	if req.Flag == "--to" {
		return &BundleTargetError{errs.WithHints(errors.New(text+"; move into "+into), hints...)}
	}
	return &BundleTargetError{errs.WithHints(errors.New(text+"; it lists profiles, not modules"), hints...)}
}

// namesBase reports whether name, a --profile, is base, or default (bare or user:default), which
// names the base at user scope only.
func namesBase(name string) (isBase, isDefault bool) {
	kind, bare, qualified := strings.Cut(name, ":")
	if !qualified {
		kind, bare = "", name
	}
	return name == baseName, bare == profile.DefaultName && (kind == "" || kind == string(profile.KindUser))
}

// readProfile reads i with the loader its kind takes (spec 2026-10-29 §16): a remote profile's
// profile.yml, a project's without following a link, else yours (default.yml included); dir is the
// directory its relative local sources are resolved against (besideProfile), "" for a remote one,
// whose entries name remote sources only. A loader's profile comes back beside its error.
func (e *Engine) readProfile(i profile.Info) (p *profile.Profile, dir string, err error) {
	switch i.Kind {
	case profile.KindRemote:
		p, _, err = profile.LoadRemote(i.Path)
		return p, "", err
	case profile.KindProject:
		p, err = profile.LoadProject(i.Path)
		return p, profile.ProjectDir, e.coachExtends(err, i)
	}
	p, err = profile.Load(i.Path)
	return p, filepath.Dir(i.Path), e.coachExtends(err, i)
}

// coachExtends hints err again when it refuses a file that still has extends: (a
// *profile.ExtendsError), knowing what the profile package cannot: the remote profiles, so the
// profile its edit makes shadows none, the --split that finishes the edit of i, yours or the
// project's (named as its scope resolves it: a bare name finds yours before a remote one, and the
// project's is named qualified), and, for default.yml, the profile on at user scope, which
// the bundle it switches to keeps. Any other err is returned as it is.
func (e *Engine) coachExtends(err error, i profile.Info) error {
	ee, ok := errors.AsType[*profile.ExtendsError](err)
	if !ok {
		return err
	}
	taken := func(name string) (bool, error) {
		for _, path := range []string{ee.Beside(name), filepath.Join(e.Paths.RemoteProfiles, name)} {
			if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
				return true, err
			}
		}
		return false, nil
	}
	on := ""
	if ee.Default {
		on = e.userOn()
	}
	split := ""
	switch i.Kind {
	case profile.KindUser:
		split = e.Command(Scope{Kind: target.User}, "profile", i.Name, "--split")
	case profile.KindProject:
		root := strings.TrimSuffix(filepath.Dir(i.Path), string(filepath.Separator)+profile.ProjectDir)
		split = e.Command(Scope{Kind: target.Project, ProjectDir: root}, "profile", i.Qualified(), "--split")
	}
	h, herr := ee.Hint(taken, on, split)
	if herr != nil {
		return errors.Join(ee, herr)
	}
	return &errs.Error{Err: ee, Hints: []string{h}}
}

// userOn is the profile on at user scope as a bundle of yours names it (memberName), "" for none or
// when the ledger or the listing does not read: what a hint switching default.yml's bundle of
// profiles keeps on.
func (e *Engine) userOn() string {
	user := Scope{Kind: target.User}
	led, lerr := e.Ledger(user)
	infos, _, verr := e.visible(user)
	if lerr == nil && verr == nil && led.Profile != nil {
		if k := activeInfo(infos, led.Profile.Name); k >= 0 {
			return shown(memberName(infos, infos[k]))
		}
	}
	return ""
}

// offLayer reads i, a profile of yours or the project's, as the layer an edit writes when it is not
// on in the scope: its own entries, local sources resolved as loadStack resolves them, and its
// parse.
func (e *Engine) offLayer(i profile.Info) (Layer, error) {
	p, dir, err := e.readProfile(i)
	if err != nil {
		return Layer{}, err
	}
	ents := p.Entries()
	for k := range ents {
		ents[k].Source = besideProfile(ents[k].Source, dir)
	}
	l := Layer{Name: ledger.FromProfile(i.Qualified()), Path: i.Path, Profile: true, Entries: ents, read: p}
	if i.Kind == profile.KindProject {
		l.Key = i.Name
	}
	return l, nil
}

// ownTargets reports whether t is a profile with top-level targets of its own, which its entries
// without targets of their own install to (fileTargets).
func (t writeTo) ownTargets() bool {
	return t.Profile && !t.Base && t.read != nil && len(t.read.Targets) > 0
}

// portable reports whether t, written at sc, is a profile of yours without top-level targets in a
// project: one used in other projects too, whose entries take the targets an install names as
// their own, never as its top-level targets (planInstallModule).
func (t writeTo) portable(sc Scope) bool {
	return sc.Kind == target.Project && t.Profile && !t.Base && t.Key == "" && !t.ownTargets()
}

// targetsFile names the file whose top-level targets an entry of t's file without targets of its
// own installs to (fileTargets), as "from <file>" says it: t's label for a profile with its own,
// "" for the scope's base, which output names as it names the base elsewhere.
func targetsFile(t writeTo) string {
	if t.ownTargets() {
		return t.label
	}
	return ""
}

// switchRun is the command that turns t, a profile that is not on at sc, on there (lola profile
// <name> --switch): what reviews a project profile's trust and applies what an install saved.
func (e *Engine) switchRun(sc Scope, t writeTo) string {
	return e.Command(sc, "profile", strings.TrimPrefix(t.label, "profile "), "--switch")
}

// layerEntry is t's entry for name as the scope reads it (local source resolved), with the targets
// its file gives it (none when it has none of its own); nil when t lists no such module.
func (t writeTo) layerEntry(name string) *manifest.Entry {
	k := slices.IndexFunc(t.Entries, func(e manifest.Entry) bool { return e.Name == name })
	if k < 0 {
		return nil
	}
	ent := t.Entries[k]
	if t.read != nil {
		if r := slices.IndexFunc(t.read.Modules, func(e manifest.Entry) bool { return e.Name == name }); r >= 0 {
			ent.Targets = slices.Clone(t.read.Modules[r].Targets)
		}
	}
	return &ent
}

// fileEntry is ent, an entry as the scope reads it, as t's file records it: a profile of yours
// never holds a pin (spec 2026-10-29 U8), and the source is in the form t's file reads it
// (fileSource), or as the file holds it already while that names the same place.
func (t writeTo) fileEntry(sc Scope, ent manifest.Entry) manifest.Entry {
	if t.Profile && t.Key == "" {
		ent.Commit, ent.Digest = "", ""
	}
	src := ent.Source
	ent.Source = fileSource(sc, t, src)
	if t.read != nil {
		if k := slices.IndexFunc(t.read.Modules, func(x manifest.Entry) bool { return x.Name == ent.Name }); k >= 0 {
			dir := filepath.Dir(t.Path)
			if t.Key != "" {
				dir = profile.ProjectDir // a project profile's sources are relative to the project root
			}
			if own := t.read.Modules[k].Source; besideProfile(own, dir) == src {
				ent.Source = own
			}
		}
	}
	return ent
}

// fileSource is src, a local source as sc reads it (absolute, or relative to the project root at
// project scope), as t's file records it (Plan 6b §4.3, §19 R5): a profile of yours is read from
// your profiles directory, so a relative path is made absolute; a project profile is read from
// .lola/profiles, so it is made relative to it; project.yml keeps it. URLs, git's scp form,
// marketplace addresses and absolute paths are kept.
func fileSource(sc Scope, t writeTo, src string) string {
	if _, _, address := manifest.Address(src); address || !t.Profile || strings.Contains(src, "://") || source.Host(src) != "" || filepath.IsAbs(src) {
		return src
	}
	if t.Key != "" {
		if rel, err := filepath.Rel(profile.ProjectDir, src); err == nil {
			return rel
		}
		return src
	}
	return filepath.Join(canonicalRoot(sc.ProjectDir), src)
}

// patternStrings is ps as an entry's only: or skip: list writes them.
func patternStrings(ps []config.Pattern) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.String())
	}
	return slices.Clip(out)
}
