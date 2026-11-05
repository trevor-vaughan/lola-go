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

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/trust"
)

// ProfileEdit is lola profile [<name>] --edit [<module>...] (spec 2026-10-29 §6.2; cherry-pick
// design §4.3): it edits a profile's modules and their picks, or with --bundle a bundle's members.
const ProfileEdit ProfileAction = "edit"

// EditTarget is the file lola profile [<name>] --edit edits at a scope (spec 2026-10-29 §6.2):
// Name as the run's hints name it (base, or the profile's name as the scope resolves it alone),
// Label as a write line names it, Path its file, Kind base, user or project, and either its modules
// by name or, for a bundle, its members as its file writes them; CacheAdd is, for each module, the
// lola cache add run of its entry's source (with its --ref and --path), which puts a module your
// cache lacks there. Every string has been through untrusted.Text.
type EditTarget struct {
	Name     string
	Label    string
	Path     string
	Kind     string
	Modules  []string
	Bundle   bool
	Members  []string
	CacheAdd map[string]string
}

// EditTarget is what lola profile [<req.Profile>] --edit edits at sc (spec 2026-10-29 §6.2): the
// profile req.Profile names (base, default at user scope, a profile of yours or the project's, or
// a bundle of them), else the write target, asked as an install asks it (ask; nil refuses with the
// flag forms, which req.Rerun words). A remote profile is refused, hinting a copy of yours; a name
// that names no profile is an unknownProfileError (IsUnknownProfile) saying --new makes it, its
// hint the run with --new (req.Rerun), unless req.New.
func (e *Engine) EditTarget(sc Scope, req TargetRequest, ask func(WhereQuestion) (WhereChoice, error)) (*EditTarget, error) {
	unlock, err := e.readProfiles(true)
	if err != nil {
		return nil, err
	}
	defer unlock()
	layers, err := e.scopeLayers(sc)
	if err != nil {
		return nil, err
	}
	infos, _, err := e.visible(sc)
	if err != nil {
		return nil, err
	}
	if req.Profile != "" {
		if isBase, isDefault := namesBase(req.Profile); !isBase && (!isDefault || sc.Kind != target.User) {
			i, err := e.resolveProfile(sc, infos, req.Profile, nil)
			if unknown, ok := errors.AsType[*unknownProfileError](err); ok && !req.New && req.Rerun != nil {
				return nil, makeIt(unknown, req)
			}
			if err != nil {
				return nil, err
			}
			if i.Kind == profile.KindRemote {
				return nil, e.remoteReadOnly(sc, infos, i, req.Again)
			}
			p, _, err := e.readFile(i)
			if err != nil {
				return nil, e.mixedError(sc, infos, i, err)
			}
			if p.Shape() == profile.ShapeBundle {
				return &EditTarget{Name: shown(editName(infos, i)), Label: "bundle " + bundleLabel(infos, i.Qualified()), Path: shown(i.Path),
					Kind: string(i.Kind), Modules: []string{}, Bundle: true, Members: shownAll(p.Bundle)}, nil
			}
		}
	}
	t, _, err := e.writeTarget(sc, layers, infos, req, "profile --edit", "", ask)
	if err != nil {
		return nil, err
	}
	out := &EditTarget{Name: layerFlag(layers, t.Name), Label: t.label, Path: shown(t.Path), Kind: t.kind(), Modules: []string{}, Members: []string{},
		CacheAdd: map[string]string{}}
	if t.Profile && !t.Base {
		out.Name = strings.TrimPrefix(t.label, "profile ")
	}
	for _, ent := range t.Entries {
		out.Modules = append(out.Modules, shown(ent.Name))
		run := []string{"lola", "cache", "add", shown(ent.Source)}
		if ent.Ref != "" {
			run = append(run, "--ref", shown(ent.Ref))
		}
		if ent.Path != "" {
			run = append(run, "--path", shown(ent.Path))
		}
		out.CacheAdd[shown(ent.Name)] = errs.ShellJoin(run)
	}
	slices.Sort(out.Modules)
	return out, nil
}

// makeIt is unknown, a name no profile has, as lola profile <name> --edit refuses it: what it says,
// that --new makes it, and the run with --new (req.Rerun). unknown's own hints (write a file, fetch a
// remote one) are left out: --new is the run's way to make it.
func makeIt(unknown *unknownProfileError, req TargetRequest) error {
	var cause error = unknown
	if hinted, ok := errors.AsType[*errs.Error](unknown); ok {
		cause = hinted.Err
	}
	return &unknownProfileError{errs.WithHints(fmt.Errorf("%w; --new makes it", cause), "make it: "+req.Rerun("--new"))}
}

// scopeLayers is sc's layers as its files and lock.json give them now.
func (e *Engine) scopeLayers(sc Scope) ([]Layer, error) {
	man, err := e.Manifest(sc)
	if err != nil {
		return nil, err
	}
	led, err := e.Ledger(sc)
	if err != nil {
		return nil, err
	}
	layers, _, err := e.layers(sc, man, led)
	return layers, err
}

// remoteReadOnly refuses an edit of i, a remote profile sc can use (infos), hinting the switch to
// a copy of yours, which you can edit, keeping again.
func (e *Engine) remoteReadOnly(sc Scope, infos []profile.Info, i profile.Info, again []string) error {
	return errs.WithHints(fmt.Errorf("%s is read-only; lola does not edit remote profiles", shown(i.Qualified())),
		"switch to a copy of yours, which you can edit: "+e.Command(sc, "profile", append([]string{freeName(infos, bareName(i.Qualified())+"-mine"),
			"--switch", "--new", "--from", i.Qualified()}, again...)...))
}

// editName is i as lola profile <name> --edit names it at a scope that can use infos: its bare
// name, unless a profile of another kind has it (layerLabel's rule).
func editName(infos []profile.Info, i profile.Info) string {
	return strings.TrimPrefix(layerLabel(infos, ledger.FromProfile(i.Qualified())), "profile ")
}

// editArg is name, a profile sc can use (infos), as a hint names it first in lola profile <name>
// --edit (ruling P2): qualified when, bare, it also names a module (readsAsModule; catalog, the
// names your catalogs list, read before the run took any lock: TargetRequest.Catalog), so the run
// reads it as the profile; else as given. A name lola cannot judge is qualified, one beside a
// catalog lola could not read (partial) too: a qualified name is never read as a module.
func (e *Engine) editArg(sc Scope, layers []Layer, infos []profile.Info, catalog []string, partial bool, name string) string {
	if strings.Contains(name, ":") || name == baseName {
		return name
	}
	i, err := e.resolveProfile(sc, infos, name, nil)
	if err != nil {
		return name // the run refuses it as it would refuse it here
	}
	cwd, err := e.Getwd()
	if err != nil || partial {
		return i.Qualified()
	}
	if module, err := e.readsAsModule(cwd, name, layers, catalog); err != nil || module {
		return i.Qualified()
	}
	return name
}

// readsAsModule reports whether name, typed after lola profile --edit at a scope whose layers are
// layers, also names a module: a cached one, a source or a folder from cwd, an entry of the layers,
// or an entry of your fetched catalogs (catalog, CatalogNames).
func (e *Engine) readsAsModule(cwd, name string, layers []Layer, catalog []string) (bool, error) {
	if module.ValidateName("module", name) == nil {
		if _, found, err := e.Store.Origin(name); err != nil || found {
			return found, err
		}
	}
	if _, isSrc, _ := source.Parse(name, cwd); isSrc {
		return true, nil
	}
	if fi, err := os.Stat(filepath.Join(cwd, name)); err == nil && fi.IsDir() {
		return true, nil
	}
	if want, _ := desired(layers); find(want, name) != nil {
		return true, nil
	}
	return slices.Contains(catalog, name), nil
}

// CatalogNames is the names of the entries your fetched catalogs offer (MarketGroups; display, the
// run's output flags), which lola profile --edit reads before an install locks the catalogs: an
// install argument of one of them is a marketplace entry (TargetRequest.Catalog). An edit needs
// none of them to run, so a catalog lola cannot read fails nothing: complete is false (some
// names may be missing), and a hint qualifies every profile name instead (editArg).
func (e *Engine) CatalogNames(display []string) (names []string, complete bool) {
	groups, warnings, err := e.MarketGroups(display)
	names = []string{}
	for _, g := range groups {
		for _, o := range g.Entries {
			names = append(names, o.Name)
		}
	}
	return names, err == nil && len(warnings) == 0
}

// EditScopes is where lola profile [<name>] --edit applies (ruling P1; cherry-pick design §4.3):
// read, the one scope the run takes without asking (the scope flags, else the project found from
// here, else user scope), and, when no flag named it and name is a profile of yours on at user
// scope too (or a bundle the one on there nests), user scope as well: the profile is written once
// and applied in each scope it is on.
// A name EditTarget refuses (unknown, ambiguous) is left to it: read alone.
func (e *Engine) EditScopes(read ScopeChoice, name string) ([]ScopeChoice, error) {
	out := []ScopeChoice{read}
	if isBase, isDefault := namesBase(name); name == "" || isBase || isDefault || read.Source == SourceFlag || read.Scope.Kind != target.Project {
		return out, nil
	}
	unlock, err := e.readProfiles(true)
	if err != nil {
		return nil, err
	}
	defer unlock()
	infos, _, err := e.visible(read.Scope)
	if err != nil {
		return nil, err
	}
	i, err := e.resolveProfile(read.Scope, infos, name, nil)
	_, unknown := errors.AsType[*unknownProfileError](err)
	_, ambiguous := errors.AsType[*ambiguousProfileError](err)
	switch {
	case unknown || ambiguous:
		return out, nil
	case err != nil:
		return nil, err
	case i.Kind != profile.KindUser:
		return out, nil
	}
	user := Scope{Kind: target.User}
	layers, err := e.scopeLayers(user)
	if err != nil {
		return nil, err
	}
	on := slices.ContainsFunc(layers, func(l Layer) bool { return l.Name == ledger.FromProfile(i.Qualified()) })
	if !on { // a bundle on there, or one the bundle on nests, gives its members' layers, never its own
		if on, err = e.reaches(user, i, nil); err != nil {
			return nil, err
		}
	}
	if on {
		out = append(out, ScopeChoice{Scope: user, Source: SourceDiscovered})
	}
	return out, nil
}

// IsUnknownProfile reports whether err refuses a profile name that names no profile the scope can
// use: what lola profile <name> --edit offers to make (--new).
func IsUnknownProfile(err error) bool {
	_, ok := errors.AsType[*unknownProfileError](err)
	return ok
}

// editRuns is newRuns for lola profile <name> --edit --new at sc, worded as the run is (req.Rerun,
// which takes "--profile <name>" as the profile the run names): make keeps the run's modules and
// flags, --from and --bundle among them, so make ignores the modifiers a caller offers (repeating
// them would fail, "bundle lists a twice"); plain makes the profile alone.
func (e *Engine) editRuns(sc Scope, req TargetRequest) newRuns {
	return newRuns{make: func(name string, _ ...string) string {
		return req.Rerun("--profile", name, "--new")
	}, plain: func(name string) string {
		return e.Command(sc, "profile", append([]string{name, "--edit", "--new"}, req.Again...)...)
	},
		use: func(q string) string { return req.Rerun("--profile", q) }, useLabel: "edit it", list: e.listRun(sc, req.Again)}
}

// EditArgsRequest is what lola profile [<name>] --edit [<module>...] was given at Scope, for
// EditArgs: Args, the names typed; Made, --new; Cwd, where a source resolves; Display, the run's
// output flags; Catalog, the names your catalogs list (CatalogNames; CatalogPartial when lola could
// not read them all); Rerun, the run with other names in place of Args (its hints); Ask, the
// question on a terminal (nil off one).
type EditArgsRequest struct {
	Scope          Scope
	Cwd            string
	Args           []string
	Made           bool
	Display        []string
	Catalog        []string
	CatalogPartial bool
	Rerun          func(args ...string) string
	Ask            func(WhereQuestion) (WhereChoice, error)
}

// EditArgs splits req.Args into the profile the run names and the modules it edits (spec
// 2026-10-29 §6.2, ambiguity 1 as amended): the first is the profile when it is base or default,
// qualified (user:, project:, remote:) or made by --new; a bare name is the profile when it names a
// profile the scope can use (or several, which the run then refuses as ambiguous) and no module
// (readsAsModule: a cached module, a source or folder, an entry of your catalogs or of the scope's
// layers), and the first module when it names a module and no profile. A name that is both is
// asked (bothRead); one that is neither is the profile, which the run refuses or --new makes.
// Otherwise every argument is a module, written to the write target (name "").
func (e *Engine) EditArgs(req EditArgsRequest) (string, []string, error) {
	if len(req.Args) == 0 {
		return "", nil, nil
	}
	first, rest := req.Args[0], req.Args[1:]
	kind, _, qualified := strings.Cut(first, ":")
	isBase, isDefault := namesBase(first)
	switch {
	case req.Made, isBase, isDefault, qualified && slices.Contains([]string{string(profile.KindUser), string(profile.KindProject), string(profile.KindRemote)}, kind):
		return first, rest, nil
	case module.ValidateName("profile", first) != nil:
		return "", req.Args, nil // a source or an address, never a profile's name
	}
	infos, _, err := e.visible(req.Scope)
	if err != nil {
		return "", nil, err
	}
	i, err := e.resolveProfile(req.Scope, infos, first, nil)
	amb, ambiguous := errors.AsType[*ambiguousProfileError](err)
	isProfile := err == nil || ambiguous
	// What a name reads as never waits on a bundle's member that resolves to nothing: that is the
	// run's own load to judge, and --edit --bundle takes it out (Engine.recovering).
	saved := e.recovering
	if saved == nil {
		e.recovering = &recovery{}
	}
	layers, err := e.scopeLayers(req.Scope)
	e.recovering = saved
	if err != nil {
		return "", nil, err
	}
	isModule, err := e.readsAsModule(req.Cwd, first, layers, req.Catalog)
	switch {
	case err != nil:
		return "", nil, err
	case isProfile && isModule:
		profiles := []profile.Info{i}
		if ambiguous {
			profiles = amb.found
		}
		return e.bothRead(req, profiles, layers, infos)
	case isModule:
		return "", req.Args, nil
	}
	return first, rest, nil // a profile, or names none: the run refuses it, or --new makes it
}

// bothRead is EditArgs' answer when req's first name is a module and profiles' name (ruling P2):
// on a terminal, which to edit, asked (req.Ask); off one, a refusal naming both, its hints the run
// with each profile qualified (with no other module, the profile's listing: an edit by no module
// refuses off a terminal), then with the module and the profile edits go to named first (or each
// member of the bundle on, as EditTarget's question offers them). layers and infos are the scope's.
func (e *Engine) bothRead(req EditArgsRequest, profiles []profile.Info, layers []Layer, infos []profile.Info) (string, []string, error) {
	first, rest := req.Args[0], req.Args[1:]
	module := shown(first)
	// The module's hints name the write target first, the members' as its question names them.
	treq := TargetRequest{Edit: true, Rerun: func(extra ...string) string {
		member, flags := ProfileFlag(extra)
		return strings.Join(append([]string{req.Rerun(append([]string{member}, req.Args...)...)}, flags...), " ")
	}}
	treq.Catalog, treq.CatalogPartial = req.Catalog, req.CatalogPartial
	t, terr := e.EditTarget(req.Scope, treq, nil)
	var named, hints []string
	var choices []WhereChoice
	for _, i := range profiles {
		q := i.Qualified()
		named = append(named, "profile "+shown(q)+" ("+shown(i.Path)+")")
		choices = append(choices, WhereChoice{Label: "Edit profile " + shown(q), Profile: q})
		if len(rest) == 0 { // an edit by no module refuses off a terminal (ruling P10): it shows the profile
			hints = append(hints, "see profile "+shown(q)+", then name a module after it: "+e.Command(req.Scope, "profile", append([]string{q}, req.Display...)...))
			continue
		}
		hints = append(hints, "edit profile "+shown(q)+": "+req.Rerun(append([]string{q}, rest...)...))
	}
	modLabel := "Edit module " + module
	switch {
	case terr == nil:
		modLabel += " in " + t.Label
		hints = append(hints, "edit module "+module+" in "+t.Label+": "+req.Rerun(append([]string{e.editArg(req.Scope, layers, infos, req.Catalog, req.CatalogPartial, t.Name)}, req.Args...)...))
	case len(errs.Hints(terr)) == 0: // not the question of a bundle on, which the module's run asks
		return "", nil, terr
	case req.Ask == nil:
		for _, h := range errs.Hints(terr) {
			hints = append(hints, "edit module "+module+": "+h)
		}
	}
	if req.Ask == nil {
		return "", nil, errs.WithHints(fmt.Errorf("%s names %s and a module; lola profile --edit needs to know which to edit, and there is no terminal to ask on",
			module, andJoin(named)), hints...)
	}
	c, err := req.Ask(WhereQuestion{Scope: req.Scope, Title: module + " names " + andJoin(named) + " and a module.",
		Choices: append(choices, WhereChoice{Label: modLabel})})
	switch {
	case err != nil:
		return "", nil, err
	case c.Profile != "":
		return c.Profile, rest, nil
	}
	return "", req.Args, nil
}

// MakeRequest is lola profile <Name> --edit --new without modules (spec 2026-10-29 §6.2): make the
// profile, empty, or From's copy, or with Bundle (members as typed) a bundle, and apply nothing (it
// is not on). Target words the hints as the run does (its Rerun, Again); Options is the run's (-n
// writes nothing).
type MakeRequest struct {
	Scope   Scope
	Name    string
	From    string
	Bundle  []string
	Target  TargetRequest
	Options Options
}

// MakeProfile makes req.Name at req.Scope (spec 2026-10-29 §6.2 --new, as --switch --new makes it,
// newProfiles) in a transaction with lock.json, and applies nothing: the result names what it made
// (Created) and how to turn it on.
func (e *Engine) MakeProfile(ctx context.Context, req MakeRequest) (*SyncResult, error) {
	sc := req.Scope
	infos, _, err := e.visible(sc)
	if err != nil {
		return nil, err
	}
	// Without a module, an edit of the profile that has the name has nothing to apply: the hint
	// shows it instead (ruling P10).
	runs := e.editRuns(sc, req.Target)
	runs.use, runs.useLabel = func(q string) string { return e.Command(sc, "profile", append([]string{q}, req.Options.Display...)...) }, "see it"
	w, err := e.newProfiles(ctx, sc, infos, ProfileChange{Action: ProfileEdit, Name: req.Name, New: true, From: req.From, Bundle: req.Bundle}, runs,
		req.Options.Display)
	if err != nil {
		return nil, err
	}
	res, err := e.Sync(ctx, SyncRequest{Scope: sc, Options: req.Options, Display: req.Options.Display, write: w, writeOnly: true})
	if err != nil {
		return nil, err
	}
	res.Created = w.made
	if req.Options.DryRun { // nothing was made: no run can turn it on
		return res, nil
	}
	made := w.infos[len(w.infos)-1] // the profile named: newProfiles adds it after the members it makes
	res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: shown(editName(infos, made)) + " is not on " + scopeWhere(sc) + " → " +
		e.Command(sc, "profile", append([]string{shown(editName(infos, made)), "--switch"}, req.Target.Again...)...)})
	return res, nil
}

// BundleEditRequest is lola profile <Name> --edit --bundle <Members> (spec 2026-10-29 §6.2): set the
// members of Name, a bundle or an empty profile, in order. Also is where else the run applies a
// bundle of yours that reaches there (EditScopes' later scopes, ruling P1). Target words the hints
// as the run does; Options, ConfirmRemovals, Trust and Show are the apply's, as a sync's are.
type BundleEditRequest struct {
	Scope           Scope
	Also            []Scope
	Name            string
	Members         []string
	Target          TargetRequest
	Options         Options
	ConfirmRemovals func([]Removal) (bool, error)
	Trust           func(TrustReport) (TrustAnswer, error)
	Show            func(TrustReport)
}

// BundleEdit is what lola profile --edit --bundle did: the bundle (Profile, its qualified name, and
// Path), its members as written now and before (Was), the profiles it made for members that named
// none, Sync, the apply of the scope when the bundle is on there (null otherwise, and when
// nothing changed), and Also, the apply of each other scope it reaches (BundleEditRequest.Also).
// Every string has been through untrusted.Text.
type BundleEdit struct {
	Profile string      `json:"profile"`
	Path    string      `json:"path"`
	Members []string    `json:"members"`
	Was     []string    `json:"was"`
	Created []Created   `json:"created"`
	Sync    *SyncResult `json:"sync"`
	Also    []ScopeSync `json:"also"`
	// Trust is, under -n of a bundle that is off, the trust question the run would ask first (a
	// pulled change to its members): null when there is none. When the bundle is on, Sync.Trust
	// carries it.
	Trust   *TrustReport `json:"trust"`
	Notices []Notice     `json:"notices"`
	DryRun  bool         `json:"dry_run"`
}

// ScopeSync is the apply of one more scope a run reaches: Scope, and its result, whose JSON names
// the scope (SyncResult.Scope, Root).
type ScopeSync struct {
	Scope Scope `json:"-"`
	*SyncResult
}

// EditBundle sets the members of req.Name at req.Scope (spec 2026-10-29 §6.2 --edit --bundle): its
// bundle: is rewritten in place (profile.SetBundle), each bare member (user:<name> in a bundle of
// yours) that names no profile is made empty (newMembers), and both are written in one transaction
// with lock.json. When the bundle is on in the scope, or a bundle the one on nests, before the edit
// or after it, that transaction is the scope's sync, which applies what the members now give;
// otherwise it writes the files alone; a bundle of yours is applied in each of req.Also it reaches
// too, its removals there asked before anything is written. The result must load: a member the
// parse refuses, a cycle, nesting too deep or too many bundles is refused, naming the line. No name,
// the base, a profile that lists modules and a remote profile are refused, each hinting what does
// it. A project bundle's members are trusted as you name them; what the members bring is asked
// about as any change is, and nothing but --trust answers that.
func (e *Engine) EditBundle(ctx context.Context, req BundleEditRequest) (*BundleEdit, error) {
	sc := req.Scope
	infos, _, err := e.visible(sc)
	if err != nil {
		return nil, err
	}
	again := req.Target.Again
	if req.Name == "" {
		return nil, e.nameTheBundle(sc, infos, req.Members, again, req.Options.Display)
	}
	if isBase, isDefault := namesBase(req.Name); isBase || isDefault {
		return nil, errs.WithHints(fmt.Errorf("%s is the base and lists modules; a bundle is a profile of its own", e.baseFile(sc)),
			"make one of these and switch to it: "+e.bundleRun(sc, bundleName(infos, req.Members), req.Members, again))
	}
	i, err := e.resolveProfile(sc, infos, req.Name, nil)
	if err != nil {
		return nil, err
	}
	if i.Kind == profile.KindRemote {
		return nil, e.remoteReadOnly(sc, infos, i, again)
	}
	// Its own members are judged in full; another bundle file's broken member never refuses its
	// take-out (Engine.recovering).
	e.recovering = &recovery{judged: map[string]bool{i.Qualified(): true}}
	defer func() { e.recovering = nil }()
	p, _, err := e.readFile(i)
	if err != nil {
		return nil, e.mixedError(sc, infos, i, err)
	}
	q := i.Qualified()
	if p.Shape() == profile.ShapeModules {
		members := append([]string{memberName(infos, i)}, slices.DeleteFunc(slices.Clone(req.Members), func(m string) bool { return m == memberName(infos, i) })...)
		return nil, errs.WithHints(fmt.Errorf("%s lists modules; a bundle is a file of its own", shown(editName(infos, i))),
			"bundle it with these instead: "+e.bundleRun(sc, bundleName(infos, members), members, again))
	}
	res := &BundleEdit{Profile: shown(q), Path: shown(i.Path), Members: shownAll(req.Members), Was: shownAll(p.Bundle), Created: []Created{},
		Also: []ScopeSync{}, Notices: []Notice{}, DryRun: req.Options.DryRun}
	if res.Was == nil {
		res.Was = []string{}
	}
	if slices.Equal(p.Bundle, req.Members) && p.Bundle != nil {
		res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: "bundle " + bundleLabel(infos, q) + " already lists " + strings.Join(res.Members, ", ")})
		return res, nil
	}
	args := []string{shown(editName(infos, i)), "--edit", "--bundle", strings.Join(req.Members, ",")}
	pending, err := e.pendingMembers(sc, i, p)
	if err != nil {
		return nil, err
	}
	w := &profileWrites{read: map[string]*profile.Profile{}, trusted: map[string]trust.View{}}
	if err := e.newMembers(sc, infos, w, i.Kind, i.Name, req.Members, e.editRuns(sc, req.Target)); err != nil {
		return nil, err
	}
	data, err := p.SetBundle(i.Path, req.Members)
	if err != nil {
		return nil, err
	}
	parse, check := profile.Parse, func() error { return p.Current(i.Path) }
	l := Layer{Name: ledger.FromProfile(q), Path: i.Path, Profile: true}
	if i.Kind == profile.KindProject {
		parse, check, l.Key = profile.ParseProject, func() error { return p.Unchanged(sc.ProjectDir, i.Path) }, i.Name
	}
	set, err := parse(i.Path, data)
	if err != nil {
		return nil, err
	}
	w.files, w.read[i.Path] = append(w.files, fileEdit{layer: l, data: data, check: check}), set
	if i.Kind == profile.KindProject { // the members you name are yours to trust; what they bring is asked about
		rec, err := e.trustRecord(sc)
		if err != nil {
			return nil, err
		}
		part := rec.View.Part(i.Name)
		part.Bundle = slices.Clone(req.Members)
		w.trusted[i.Name] = part
	}
	res.Created = append(res.Created, w.made...)
	judged, err := e.newlyReached(sc, i, w)
	if err != nil {
		return nil, err
	}
	e.recovering = &recovery{judged: judged}
	on, err := e.reaches(sc, i, w)
	if err != nil {
		return nil, err
	}
	var also []Scope
	for _, o := range req.Also {
		if i.Kind != profile.KindUser { // only a profile of yours is on in another scope
			break
		}
		if r, err := e.reaches(o, i, w); err != nil {
			return nil, err
		} else if r {
			also = append(also, o)
		}
	}
	sync := SyncRequest{Scope: sc, Options: req.Options, ConfirmRemovals: req.ConfirmRemovals, Trust: req.Trust, Show: req.Show, Display: req.Options.Display,
		write: w, writeOnly: !on, profileArgs: args}
	// The list you type is recorded as trusted: a pulled change to the list on disk is asked about
	// first, never taken in with it, and with it every change the run's own sync would ask about, so
	// that an answer, or the --trust a hint offers, covers only what was shown (ruling, Task 5 fix
	// rounds 1 and 2).
	var shownTrust *TrustReport
	if pending != nil && !req.Options.Trust.Covers(TrustProfiles) {
		dry := sync
		dry.Options.DryRun, dry.Trust, dry.Show, dry.ConfirmRemovals = true, nil, nil, nil
		planned, err := e.Sync(ctx, dry)
		if err != nil {
			return nil, err
		}
		all := withPending(planned.Trust, trustReport(*pending))
		switch {
		case req.Options.DryRun:
			shownTrust = &all
		case req.Trust != nil:
			ans, err := req.Trust(all)
			if err != nil {
				return nil, err
			}
			if !ans.All {
				return nil, ErrDeclined
			}
			sync.Trust = func(rep TrustReport) (TrustAnswer, error) { // what was shown is answered; anything else is asked
				if shownCovers(all, rep) {
					return TrustAnswer{All: true}, nil
				}
				return req.Trust(rep)
			}
		default:
			return nil, errs.WithHints(&TrustRefusalError{Report: all, Removals: []Removal{},
				msg: "lola profile needs your trust for " + trustItems(all) + ", and there is no terminal to ask on; nothing was written"},
				"see them: "+e.Command(sc, "profile", slices.Concat(args, again, []string{"-n"})...),
				"then accept them: "+e.Command(sc, "profile", slices.Concat(args, again, []string{(req.Options.Trust | TrustProfiles | all.kinds()).Arg()})...))
		}
	}
	confirmed, err := e.confirmAlso(ctx, sync, also)
	if err != nil {
		return nil, err
	}
	out, err := e.Sync(ctx, sync)
	if err != nil {
		return nil, err
	}
	switch {
	case on:
		res.Sync = out
		if shownTrust != nil {
			res.Sync.Trust = *shownTrust
		}
	case req.Options.DryRun:
		res.Trust = shownTrust
		res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: "would be saved to bundle " + bundleLabel(infos, q) + "; it is not on " + scopeWhere(sc)})
	default:
		res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: "saved to bundle " + bundleLabel(infos, q) + "; it is not on " + scopeWhere(sc) +
			" → " + e.Command(sc, "profile", append([]string{shown(editName(infos, i)), "--switch"}, again...)...)})
	}
	for _, o := range also {
		then := SyncRequest{Scope: o, Options: req.Options, ConfirmRemovals: req.ConfirmRemovals, Display: req.Options.Display, profileArgs: args}
		if req.Options.DryRun {
			then.write = w // nothing was written: the scope reads the files as the edit would write them
		}
		switch {
		case confirmed:
			then.ConfirmRemovals = func([]Removal) (bool, error) { return true, nil }
		case req.ConfirmRemovals != nil && !req.Options.DryRun:
			// Removals the check before the write did not see: a No or a cancel here leaves the
			// files written, so neither may read as a run that changed nothing (ErrDeclined, the
			// CLI's aborted).
			then.ConfirmRemovals = func(rs []Removal) (bool, error) {
				ok, err := req.ConfirmRemovals(rs)
				if err != nil {
					return false, errors.New(err.Error())
				}
				return ok, nil
			}
		}
		r, err := e.Sync(ctx, then)
		if err != nil && !req.Options.DryRun {
			if errors.Is(err, ErrDeclined) {
				err = errors.New("you declined its removals there")
			}
			return nil, errs.WithHints(fmt.Errorf("%w; the edit was saved and applied %s, not %s", err, scopeWhere(sc), scopeWhere(o)),
				"apply it there: "+e.SyncCommand(o))
		}
		if err != nil {
			return nil, err
		}
		res.Also = append(res.Also, ScopeSync{Scope: o, SyncResult: r})
	}
	return res, nil
}

// confirmAlso asks, before first (the run's own sync) writes anything, about the removals the sync
// of each scope in also would make once first's files are written, as a sync asks them
// (first.ConfirmRemovals; -y answers yes, --force needs no answer); without a way to ask it refuses,
// nothing written, hinting the run with -y. confirmed says every removal there was answered yes.
func (e *Engine) confirmAlso(ctx context.Context, first SyncRequest, also []Scope) (confirmed bool, err error) {
	if first.Options.DryRun || first.Options.Force {
		return false, nil
	}
	for _, o := range also {
		dry := SyncRequest{Scope: o, Options: first.Options, Display: first.Display, write: first.write}
		dry.Options.DryRun = true
		res, err := e.Sync(ctx, dry)
		if err != nil {
			return false, err
		}
		if len(res.Removals) == 0 {
			continue
		}
		if first.ConfirmRemovals == nil {
			return false, errs.WithHints(fmt.Errorf("lola profile needs your confirmation to remove %s %s, and there is no terminal to ask on; nothing was written",
				strings.Join(removalNames(res.Removals), ", "), scopeWhere(o)),
				"see what goes: "+e.commandOf(first, "-n"), "then confirm them: "+e.commandOf(first, "-y"))
		}
		yes, err := first.ConfirmRemovals(slices.Clone(res.Removals))
		switch {
		case err != nil:
			return false, err
		case !yes:
			return false, ErrDeclined
		}
		confirmed = true
	}
	return confirmed, nil
}

// nameTheBundle refuses lola profile --edit --bundle members with no name at sc (ruling P4): the
// write target an edit defaults to is never a bundle. Its hints run as printed: adding members to
// the bundle of yours or the project's that is on, and making a bundle of them.
func (e *Engine) nameTheBundle(sc Scope, infos []profile.Info, members, again, display []string) error {
	var hints []string
	led, err := e.Ledger(sc)
	if err != nil {
		return err
	}
	if led.Profile != nil {
		if k := activeInfo(infos, led.Profile.Name); k >= 0 && infos[k].Kind != profile.KindRemote {
			p, _, err := e.readFile(infos[k])
			var pending *trust.Report
			if err == nil {
				pending, err = e.pendingMembers(sc, infos[k], p)
			}
			// A hint never repeats a pulled member list you have not trusted: running it would.
			if err == nil && pending == nil && p.Shape() == profile.ShapeBundle && len(members) > 0 {
				all := slices.Clone(p.Bundle)
				for _, m := range members {
					if !slices.Contains(all, m) {
						all = append(all, m)
					}
				}
				hints = append(hints, "add them to "+bundleLabel(infos, infos[k].Qualified())+", the bundle on: "+
					e.Command(sc, "profile", append([]string{editName(infos, infos[k]), "--edit", "--bundle", strings.Join(all, ",")}, again...)...))
			}
		}
	}
	if len(members) > 0 {
		label := "make a bundle of them: "
		if len(hints) > 0 {
			label = "or " + label
		}
		// Named so that it is none of its members: a bundle cannot name itself.
		taken := slices.Clone(infos)
		for _, m := range bareNames(members) {
			taken = append(taken, profile.Info{Name: m})
		}
		hints = append(hints, label+e.Command(sc, "profile", append([]string{freeName(taken, strings.Join(bareNames(members), "-")), "--edit", "--new",
			"--bundle", strings.Join(members, ",")}, again...)...))
	} else {
		hints = append(hints, "see the profiles: "+e.Command(sc, "profile", display...))
	}
	return errs.WithHints(errors.New("--bundle sets the members of the bundle --edit names, and none was named: name the bundle to edit, as in "+
		"lola profile <bundle> --edit --bundle <profile>,<profile>"), hints...)
}

// withPending is rep, the report of a run's own sync, with pending, the change to a project bundle's
// members a pull made (pendingMembers), added: one question naming both, for a file that changed.
func withPending(rep, pending TrustReport) TrustReport {
	out := rep
	out.ProfileTargets = slices.Concat(pending.ProfileTargets, slices.DeleteFunc(slices.Clone(rep.ProfileTargets), func(pt ProfileTargets) bool {
		return slices.ContainsFunc(pending.ProfileTargets, func(o ProfileTargets) bool { return o.Profile == pt.Profile && o.Field == pt.Field })
	}))
	if out.Changes == nil {
		out.Changes = []TrustChange{}
	}
	if out.Replacements == nil {
		out.Replacements = []Replacement{}
	}
	if out.Status == trust.StatusTrusted || out.Status == "" || out.Status == TrustStatusNotApplicable {
		out.Status = pending.Status
	}
	out.ManifestChanged = true
	return out
}

// shownCovers reports whether every change of rep is one shown has: what an answer to shown
// accepted.
func shownCovers(shown, rep TrustReport) bool {
	in := func(have, want any) bool { return reflect.DeepEqual(have, want) }
	if rep.Targets != nil && !in(shown.Targets, rep.Targets) {
		return false
	}
	for _, pt := range rep.ProfileTargets {
		if !slices.ContainsFunc(shown.ProfileTargets, func(o ProfileTargets) bool { return in(o, pt) }) {
			return false
		}
	}
	for _, c := range rep.Changes {
		if !slices.ContainsFunc(shown.Changes, func(o TrustChange) bool { return in(o, c) }) {
			return false
		}
	}
	for _, r := range rep.Replacements {
		if !slices.ContainsFunc(shown.Replacements, func(o Replacement) bool { return in(o, r) }) {
			return false
		}
	}
	return true
}

// trustItems names each change of rep as a refusal does: a bundle's members, a file's targets, a
// module's entry, a replacement.
func trustItems(rep TrustReport) string {
	var items []string
	for _, r := range rep.Replacements {
		items = append(items, r.Profile+" replacing "+r.Replaces)
	}
	if rep.Targets != nil {
		items = append(items, "changes to the targets of project.yml")
	}
	for _, pt := range rep.ProfileTargets {
		what := "targets"
		if pt.Field == "bundle" {
			what = "members"
		}
		items = append(items, "changes to the "+what+" of "+pt.Profile)
	}
	for _, c := range rep.Changes {
		where := "project.yml"
		if c.Profile != "" {
			where = c.Profile
		}
		items = append(items, "the entry "+c.Module+" in "+where)
	}
	return andJoin(items)
}

// pendingMembers is the change to the member list of i, a project bundle as read (p), that its part
// of the trust record does not cover, as a sync reports it (a pull changed it, or it was never
// trusted); nil when the record covers it, and for any other profile.
func (e *Engine) pendingMembers(sc Scope, i profile.Info, p *profile.Profile) (*trust.Report, error) {
	if i.Kind != profile.KindProject {
		return nil, nil
	}
	rec, err := e.trustRecord(sc)
	if err != nil {
		return nil, err
	}
	part := rec.View.Part(i.Name)
	if slices.Equal(part.Bundle, p.Bundle) {
		return nil, nil
	}
	change := &trust.FieldChange{Field: "bundle", To: slices.Clone(p.Bundle)}
	if part.Bundle != nil {
		change.From = slices.Clone(part.Bundle)
	}
	status := trust.StatusChanged
	if !rec.Exists {
		status = trust.StatusNew
	}
	return &trust.Report{Status: status, Changes: []trust.Change{}, ProfileTargets: map[string]*trust.FieldChange{i.Qualified(): change}}, nil
}

// newlyReached is i, a bundle an edit writes as w has it, and each profile and bundle it reaches
// once w is written that it does not reach now, by qualified name: what the edit's own member list
// brings in, judged in full (recovery), so an edit never writes breakage it creates. The current
// expansion passes over what it cannot resolve; one that fails reaches nothing.
func (e *Engine) newlyReached(sc Scope, i profile.Info, w *profileWrites) (map[string]bool, error) {
	saved := e.recovering
	e.recovering = &recovery{}
	defer func() { e.recovering, e.creating = saved, nil }()
	seen := func(overlay *profileWrites) (map[string]bool, error) {
		e.creating = overlay
		infos, _, err := e.visible(sc)
		if err != nil {
			return nil, err
		}
		_, x, err := e.members(sc, infos, i)
		if err != nil {
			return nil, err
		}
		return x.seen, nil
	}
	now, err := seen(nil)
	if err != nil {
		now = nil
	}
	then, err := seen(w)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{i.Qualified(): true}
	for q := range then {
		if !now[q] {
			out[q] = true
		}
	}
	return out, nil
}

// reaches reports whether i is on at sc, or a bundle that the bundle on nests, as the scope reads its
// files now or once w is written: an edit of its members then changes what the scope loads. i as w
// writes it must load: a cycle, nesting too deep or too many bundles is refused, naming the line.
func (e *Engine) reaches(sc Scope, i profile.Info, w *profileWrites) (bool, error) {
	defer func() { e.creating = nil }()
	led, err := e.Ledger(sc)
	if err != nil {
		return false, err
	}
	on := led.Profile != nil && led.Profile.Name == i.Qualified()
	overlays := []*profileWrites{w, nil}
	if w == nil { // the files as they are: one pass
		overlays = overlays[1:]
	}
	for _, overlay := range overlays {
		e.creating = overlay
		infos, _, err := e.visible(sc)
		if err != nil {
			return false, err
		}
		if overlay != nil {
			if _, _, err := e.members(sc, infos, i); err != nil {
				return false, err
			}
		}
		if on || led.Profile == nil {
			continue
		}
		k := activeInfo(infos, led.Profile.Name)
		if k < 0 || infos[k].Kind == profile.KindRemote {
			continue
		}
		if _, x, err := e.members(sc, infos, infos[k]); err == nil && x.seen[i.Qualified()] { // expanded on the way, whether or not a member gives a layer
			on = true
		} // an expansion that fails is the sync's to refuse, naming the line
	}
	return on, nil
}

// BundleMembers is what the members page of a bundle offers (MemberChoices): Options, each profile
// the bundle can name, once, as its file would write it, and Members, its members now as its file
// writes them. Unresolved is the members (among Options, first) that resolve to no profile at the
// scope. Every string has been through untrusted.Text.
type BundleMembers struct {
	Options, Members, Unresolved []string
}

// MemberChoices is what the members page of the bundle name at sc offers (spec 2026-10-29 §3.3,
// §6.2 --edit --bundle): each profile the bundle can name — in a bundle of yours your profiles and
// the remote ones (memberName), and at project scope the project's (project:<name>); in a project's
// bundle the project's profiles bare and the remote ones qualified — and its members now. A member
// is matched to the profile it resolves to (ruling P11), whose option then keeps the file's
// spelling; a member the scope cannot resolve is offered on its own, first, and named Unresolved,
// so unticking it is a choice made in view. Never the bundle itself, default or base. A name that
// names no profile yet (--new) is a bundle of yours, or with project:<name> the project's, of no
// members.
func (e *Engine) MemberChoices(sc Scope, name string) (*BundleMembers, error) {
	unlock, err := e.readProfiles(true)
	if err != nil {
		return nil, err
	}
	defer unlock()
	infos, _, err := e.visible(sc)
	if err != nil {
		return nil, err
	}
	kind, self := profile.KindUser, name
	spelled := map[string]string{} // a member's profile (qualified) → the file's spelling of it
	out := &BundleMembers{Members: []string{}, Unresolved: []string{}}
	k, _, qualified := strings.Cut(name, ":")
	switch i, err := e.resolveProfile(sc, infos, name, nil); {
	case err == nil:
		kind, self = i.Kind, i.Qualified()
		p, _, err := e.readFile(i)
		if err != nil {
			return nil, e.mixedError(sc, infos, i, err)
		}
		x := &expansion{e: e, sc: sc, infos: infos, seen: map[string]bool{}, noted: map[string]bool{}}
		for at, typed := range p.Bundle {
			out.Members = append(out.Members, shown(typed))
			if m, ok, err := x.member(i, p, at); err == nil && ok {
				spelled[m.Qualified()] = shown(typed)
			} else {
				out.Unresolved = append(out.Unresolved, shown(typed))
			}
		}
	case k == string(profile.KindProject) && qualified:
		kind = profile.KindProject
	case !qualified:
		self = string(profile.KindUser) + ":" + name
	}
	out.Options = append([]string{}, out.Unresolved...)
	for _, i := range infos {
		if m := spelled[i.Qualified()]; m != "" { // a member now, whatever it is, stays offered
			out.Options = append(out.Options, m)
			continue
		}
		if i.Kind == profile.KindDefault || i.Qualified() == self || isDir(i) {
			continue
		}
		var m string
		switch {
		case kind == profile.KindProject && i.Kind == profile.KindUser:
			continue // a project's bundle never names yours (U9)
		case kind == profile.KindProject && i.Kind == profile.KindProject:
			m = shown(i.Name)
		case kind == profile.KindProject:
			m = shown(i.Qualified())
		default:
			m = shown(memberName(infos, i))
		}
		out.Options = append(out.Options, m)
	}
	return out, nil
}
