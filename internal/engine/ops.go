package engine

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/trust"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// load refuses symlinked project state, takes the scope lock (unless dry) and reads the
// manifest and ledger. A manifest with an entry whose source is a marketplace address is refused
// (*manifest.UnresolvedError, Plan 6c §6.7), before any trust check or network contact.
func (e *Engine) load(sc Scope, dry bool) (*manifest.Manifest, *ledger.Ledger, func(), error) {
	man, led, unlock, _, err := e.loadReplacing(sc, dry, nil)
	return man, led, unlock, err
}

// loadReplacing is load, reading the manifest as manifestReplacing does, with the unresolved
// entries the install leaves, which it warns of.
func (e *Engine) loadReplacing(sc Scope, dry bool, names []string) (*manifest.Manifest, *ledger.Ledger, func(), []manifest.Unresolved, error) {
	if err := e.refuseSymlinkedState(sc); err != nil {
		return nil, nil, nil, nil, err
	}
	unlock := func() {}
	if !dry {
		u, err := e.lock(sc)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		unlock = u
	}
	man, others, err := e.manifestReplacing(sc, names)
	if err != nil {
		unlock()
		return nil, nil, nil, nil, err
	}
	led, err := e.Ledger(sc)
	if err != nil {
		unlock()
		return nil, nil, nil, nil, err
	}
	return man, led, unlock, others, nil
}
func (e *Engine) noTargets() error {
	var ids []string
	for _, t := range e.Targets {
		ids = append(ids, t.ID())
	}
	return errs.WithHints(errors.New("no targets selected"), "pass -t with one of: "+strings.Join(ids, ", "))
}

// withRetry tells the user that fetched modules stayed in the cache, and how to install them at
// each scope of spec from where they are (Plan 6a §11.3), one command per scope; for lola profile
// --edit, the run as given (its Rerun, with its --override and --new), whose picks the retry needs.
// A refusal for want of trust (a project's entry kept out of your files included), for conflicts,
// or of a cached version that does not match its digest, keeps only its own hints, the way
// forward: retrying would refuse again.
func (e *Engine) withRetry(spec InstallSpec, err error, fetched []string) error {
	_, targets := errors.AsType[*untrustedTargetsError](err)
	_, conflicts := errors.AsType[*plan.ConflictError](err)
	_, template := errors.AsType[*TemplateError](err) // the same install refuses the same way
	_, hook := errors.AsType[*hookConsentError](err)
	_, script := errors.AsType[*scriptError](err)
	_, scriptConsent := errors.AsType[*scriptConsentError](err)
	_, digest := errors.AsType[*store.DigestError](err) // refused until removed from the cache
	_, kept := errors.AsType[*keptInProjectError](err)
	if len(fetched) == 0 || targets || conflicts || template || hook || script || scriptConsent || digest || kept || errors.Is(err, ErrNotTrusted) {
		return err
	}
	if t := spec.Options.Target; t.Edit && t.Rerun != nil {
		return errs.WithHints(err, "fetched modules stay in your cache; retry with: "+t.Rerun(t.ownFlags()...))
	}
	// The retry keeps the run's own scripts, the gate the user gave it (RR1).
	args := slices.Clone(fetched)
	for _, f := range []struct{ flag, given string }{{"--pre-install", spec.Options.PreInstall.Given}, {"--post-install", spec.Options.PostInstall.Given}} {
		if f.given != "" {
			args = append(args, f.flag, f.given)
		}
	}
	cmds := make([]string, len(spec.Scopes))
	for i, si := range spec.Scopes {
		cmds[i] = e.installRun(si.Scope, spec.Options.Target, args...)
	}
	return errs.WithHints(err, "fetched modules stay in your cache; retry with: "+strings.Join(cmds, "; "))
}

// Install installs reqs at one scope: InstallAll with a single scope given by flag.
func (e *Engine) Install(ctx context.Context, sc Scope, cwd string, reqs []InstallRequest, opts Options) (*Result, error) {
	r, err := e.InstallAll(ctx, InstallSpec{Cwd: cwd, Modules: reqs, Options: opts,
		Scopes: []ScopeInstall{{ScopeChoice: ScopeChoice{Scope: sc, Source: SourceFlag}, Targets: opts.Targets}}})
	if err != nil {
		return nil, err
	}
	return r.Result, nil
}

// ScopeInstall is one scope of an install, the targets given for it (none: the scope
// manifest's) and the selection edits for its section of selections.yaml.
type ScopeInstall struct {
	ScopeChoice
	Targets []string
	Edits   []SelectionEdit
	// Replace lists unowned destinations at this scope the install overwrites instead of
	// refusing them as conflicts (plan.Replaceable): lola migrate's backed-up Python files.
	Replace []plan.Replaceable
}

// InstallSpec is an install of the same modules at one or more scopes, applied together.
// Each scope has its own targets; Options.Targets is only the assistants the user gave (-t),
// which a hint repeats. GlobalEdits change the global section of selections.yaml.
type InstallSpec struct {
	Cwd         string
	Scopes      []ScopeInstall
	Modules     []InstallRequest
	GlobalEdits []SelectionEdit
	// NameMentions names, per module and scope, the components the picked components mention
	// that the run leaves out, with the install that adds them (cherry-pick design §5.2). lola
	// install sets it; lola info's plan lists the mentions itself, and lola migrate picks nothing.
	NameMentions bool
	// WithMentions adds, at each scope, the components the picked components mention directly and
	// the selection leaves out, as --unskip edits of the scope's section, saved like any pick
	// (cherry-pick design §5.3); what those mention in turn is not followed. A --skip flag of the
	// run keeps out a mention it matches; an untick on the component page does not.
	WithMentions bool
	Options      Options
	// Staged, when set, is the install's staged run (StagedRun): the versions the pickers or this
	// call fetch are read from it, and the apply commits those it installs to the cache before its
	// first write (cherry-pick design §7); a dry run, a refusal or a declined question leaves the
	// cache as it was. Without one, the call fetches into a run of its own and caches what it
	// fetches at once (lola migrate, lola info's plan).
	Staged *StagedRun
	// then, set by InstallAll for lola profile <name> --edit applied in several scopes (ruling P1),
	// is the scopes after the first, which sync the profile the first writes instead of planning
	// their own edit of it: no hint names them as scopes the profile is also on in (alsoOn).
	// profiles, when set, receives each profile file the install writes, as it would write it.
	then     []Scope
	profiles *[]fileEdit
	// ConfirmRemovals asks about the files a pick change deletes (confirmDeletes) and, for lola
	// profile <name> --edit applied in several scopes, the removals a later scope's sync makes,
	// before anything is written (confirmEdited), as a sync's ConfirmRemovals does; nil cannot
	// ask, and -y gives one that answers yes.
	ConfirmRemovals func([]Removal) (bool, error)
	// DeletesConfirmed says the user already answered yes to this plan, which lists the files it
	// deletes (the review's Apply): a pick change that deletes files asks nothing more (confirmDeletes).
	DeletesConfirmed bool
	// At is lola install's --at: per module, the version a refusal showed (a git commit, or a
	// folder's or archive's content digest). A module its source now serves at another version is
	// refused before anything is written, hinting AtFresh, the run's dry run without --at, so a
	// consent hint answers only the version it showed. A name no request resolves to is refused.
	At      map[string]string
	AtFresh string
	// UserRun is the user's run at user scope, the hint of a refusal of a project at the home
	// directory; "" gives it no hint.
	UserRun string
	// BeforeWrite, when set, runs once every scope's plan is computed and every refusal and
	// prompt has passed, before anything is written: its error stops the install with nothing
	// written. lola migrate backs up the paths the plan replaces there. A dry run never calls it.
	BeforeWrite func(*InstallResult) error
}

// ScopeResult says which scope an install acted on and where that choice came from.
type ScopeResult struct {
	Scope       string `json:"scope"`
	Root        string `json:"root,omitempty"`
	ScopeSource string `json:"scope_source"`
}

// InstallResult is Result over every scope of an install; modules and steps carry their
// scope.
type InstallResult struct {
	*Result
	Scopes []ScopeResult `json:"scopes"`
}

// scopePlan is one scope of an install, locked, with its modules resolved and then planned.
// loaded is the manifest as read under the lock, for the trust refresh (saveManifest); trust is
// the project's trust record (nil at user scope). untrustedTargets refuses a module that would
// install to top-level targets the record does not cover (nil when it covers them, or -t was
// given). layers is the scope's layers (Plan 6b §6.2), read once, before anything is fetched.
type scopePlan struct {
	sc               Scope
	man              *manifest.Manifest
	loaded           *manifest.Manifest
	saved            *config.Selections // selections.yaml as the file has it, without this run's edits
	trust            *trust.Record
	untrustedTargets error
	unresolved       bool // man holds a marketplace address (holdsAddress)
	led              *ledger.Ledger
	templates        *templateRun // the scope's one plan pass
	layers           []Layer
	targets          []string
	given            []string
	resolved         []resolvedRequest
	changes          []plan.ModuleChange
	copies           []moduleCopy
	writes           []targetWrite // what the install writes, per file
	replace          []plan.Replaceable
	target           writeTo                        // the file the install writes (writeTarget)
	picks            config.Where                   // where its picks are saved: selections.yaml's scope section for project.yml, else the target's entry
	scopePicks       config.Where                   // selections.yaml's section of the scope (where)
	planned          []string                       // the modules planned, written or not
	dropped          []string                       // modules overridden from project.yml, whose selections.yaml change is not saved
	infos            []profile.Info                 // the profiles the scope can use, once listed
	profiles         func() ([]profile.Info, error) // infos, else listed once when first asked for (visibleOnce)
	shared           map[string]bool                // the profile files an earlier scope of the run writes
	overrides        map[string]string              // module → the name of the layer whose entry the target's override copied
}

// targetWrite is the entries an install writes into one file, t's, each in its file form.
type targetWrite struct {
	t       writeTo
	entries []manifest.Entry
}

// resolvedRequest is an install argument resolved at a scope.
type resolvedRequest struct {
	req InstallRequest
	ent manifest.Entry
	v   store.Version
	// hashed says this run staged v and hashed it itself (not a publishing run, whose fetch may
	// return a cached version as the cache records it), so its digest is lola's own, not one a
	// file names.
	hashed bool
}

// InstallAll installs modules at every scope of spec (Plan 5c §7, §14); a destination listed
// twice is refused. Order: take the selections lock when there are edits (always before any
// scope lock), lock every scope in lockOrder, then resolve the modules per scope; apply the
// edits to selections.yaml in memory; plan through the resolver; run every refusal (--strict,
// conflicts) and consent prompt for every scope; write selections.yaml; then apply each
// scope. A refusal or a cancelled prompt writes nothing; a failure while applying leaves
// selections.yaml written, which the next sync converges on. A dry run writes neither. What an
// install adds to a file is followed by the move that fixes a wrong target, judged once the
// install has released its locks, in the state it leaves (installMoveHints).
//
// lola profile <name> --edit in several scopes (Options.Target.Edit; ruling P1, cherry-pick design
// §4.3) plans the edit once, at the first scope, from the profile's file, and writes it there; each
// other scope then syncs the modules it edited from the file as written (under -n, as it would be
// written), so the one file is never planned twice.
func (e *Engine) InstallAll(ctx context.Context, spec InstallSpec) (*InstallResult, error) {
	var then []ScopeInstall
	var wrote []fileEdit
	if spec.Options.Target.Edit && len(spec.Scopes) > 1 {
		spec.Scopes, then = spec.Scopes[:1], spec.Scopes[1:]
		for _, si := range then {
			spec.then = append(spec.then, si.Scope)
		}
		spec.profiles = &wrote
	}
	var confirmed map[string]bool
	if before := spec.BeforeWrite; len(then) > 0 {
		spec.BeforeWrite = func(out *InstallResult) error {
			if before != nil {
				if err := before(out); err != nil {
					return err
				}
			}
			var err error
			confirmed, err = e.confirmEdited(ctx, spec, then, wrote)
			return err
		}
	}
	var added []installAdded
	out, err := e.installAll(ctx, spec, &added)
	if err != nil {
		return out, err
	}
	var scopes []Scope
	for _, si := range spec.Scopes {
		scopes = append(scopes, si.Scope)
	}
	for _, a := range added {
		moves := e.installMoveHints(ctx, a, scopes, spec.Options.Display)
		out.Notices = append(append(out.Notices, e.inProjectNotices(a, scopes, spec.Options, moves)...), moves...)
	}
	for _, si := range then {
		if err := e.syncEdited(ctx, spec, si, wrote, confirmed[si.Scope.Key()], out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// editedSync is the sync of si's scope that applies what lola profile --edit wrote at the first
// scope (wrote) into a profile of yours loaded there: the modules it wrote into such a file, nil
// for none (an edit answered into the first scope's own files, project.yml or its selections,
// leaves si's scope as it is). pending reads the files as wrote would write them (-n, or the check
// before the write, confirmEdited).
func (e *Engine) editedSync(spec InstallSpec, si ScopeInstall, wrote []fileEdit, pending bool) (*SyncRequest, error) {
	layers, err := e.scopeLayers(si.Scope)
	if err != nil {
		return nil, err
	}
	var modules []string
	var mine []fileEdit
	for _, f := range wrote {
		if f.layer.Key != "" || !slices.ContainsFunc(layers, func(l Layer) bool { return l.Path == f.layer.Path }) {
			continue
		}
		mine = append(mine, f)
		for _, ent := range f.change.Set {
			modules = append(modules, ent.Name)
		}
	}
	if len(modules) == 0 {
		return nil, nil
	}
	slices.Sort(modules)
	req := &SyncRequest{Scope: si.Scope, Modules: slices.Compact(modules), Options: spec.Options, Display: spec.Options.Display}
	if pending {
		req.write = &profileWrites{read: map[string]*profile.Profile{}}
		for _, f := range mine {
			_, data, err := f.layer.read.Edit(f.layer.Path, f.change)
			if err != nil {
				return nil, err
			}
			_, p, err := madeFile(profile.KindUser, f.layer.Path, data)
			if err != nil {
				return nil, err
			}
			req.write.read[f.layer.Path] = p
		}
	}
	return req, nil
}

// removalNames are the modules rs removes, each with the assistants it is removed from where known.
func removalNames(rs []Removal) []string {
	names := make([]string, 0, len(rs))
	for _, r := range rs {
		n := shown(r.Module)
		if len(r.Targets) > 0 {
			n += " (" + strings.Join(shownAll(r.Targets), ", ") + ")"
		}
		names = append(names, n)
	}
	return names
}

// confirmEdited asks, before the edit writes anything, about the removals each later scope's sync
// (editedSync) would make, as a sync asks them (spec.ConfirmRemovals; -y answers yes, --force
// needs no answer); without a way to ask it refuses, nothing written, hinting the run with -y. It
// returns the scopes whose removals were confirmed.
func (e *Engine) confirmEdited(ctx context.Context, spec InstallSpec, then []ScopeInstall, wrote []fileEdit) (map[string]bool, error) {
	confirmed := map[string]bool{}
	for _, si := range then {
		req, err := e.editedSync(spec, si, wrote, true)
		if err != nil || req == nil {
			if err != nil {
				return nil, err
			}
			continue
		}
		req.Options.DryRun, req.held = true, true // the install holds the profiles lock
		res, err := e.Sync(ctx, *req)
		if err != nil {
			return nil, err
		}
		if len(res.Removals) == 0 || spec.Options.Force {
			continue
		}
		if spec.ConfirmRemovals == nil {
			err := fmt.Errorf("lola profile --edit needs your confirmation to remove %s %s, and there is no terminal to ask on; nothing was written",
				strings.Join(removalNames(res.Removals), ", "), scopeWhere(si.Scope))
			if t := spec.Options.Target; t.Rerun != nil {
				return nil, errs.WithHints(err, "see what goes: "+t.Rerun(append(t.ownFlags(), "-n")...),
					"confirm it: "+t.Rerun(append(t.ownFlags(), "-y")...))
			}
			return nil, err
		}
		yes, err := spec.ConfirmRemovals(slices.Clone(res.Removals))
		switch {
		case err != nil:
			return nil, err
		case !yes:
			return nil, ErrDeclined
		}
		confirmed[si.Scope.Key()] = true
	}
	return confirmed, nil
}

// syncEdited applies at si's scope what lola profile --edit wrote at spec's first scope into a
// profile of yours loaded there too (InstallAll; editedSync, none to apply, no sync), its result
// added to out. Its removals were asked before the write (confirmEdited: confirmed). A real run
// that fails here has written the file and applied it at the first scope: the refusal says so and
// hints the sync that finishes it.
func (e *Engine) syncEdited(ctx context.Context, spec InstallSpec, si ScopeInstall, wrote []fileEdit, confirmed bool, out *InstallResult) error {
	req, err := e.editedSync(spec, si, wrote, spec.Options.DryRun)
	if err != nil || req == nil {
		return err
	}
	req.ConfirmRemovals = spec.ConfirmRemovals
	if confirmed {
		req.ConfirmRemovals = func([]Removal) (bool, error) { return true, nil }
	}
	res, err := e.Sync(ctx, *req)
	if err != nil {
		if spec.Options.DryRun {
			return err
		}
		return errs.WithHints(fmt.Errorf("%w; the edit was saved and applied %s, not %s", err, scopeWhere(spec.Scopes[0].Scope), scopeWhere(si.Scope)),
			"apply it there: "+e.SyncCommand(si.Scope))
	}
	for i := range res.Modules {
		res.Modules[i].Scope, res.Modules[i].Root = si.Scope.Kind.String(), root(si.Scope)
	}
	for i := range res.Steps {
		res.Steps[i].Scope, res.Steps[i].Root = si.Scope.Kind.String(), root(si.Scope)
	}
	out.Modules, out.Steps = append(out.Modules, res.Modules...), append(out.Steps, res.Steps...)
	out.Findings, out.Notices = append(out.Findings, res.Findings...), append(out.Notices, res.Notices...)
	out.Scripts, out.Trusted = append(out.Scripts, res.Scripts...), append(out.Trusted, res.Trusted...)
	out.Scopes = append(out.Scopes, ScopeResult{Scope: si.Scope.Kind.String(), Root: root(si.Scope), ScopeSource: si.Source})
	return nil
}

// installAdded is the entries of modules one write of an install added to t at sp's scope
// (entries, all it wrote there), which the move hint follows.
type installAdded struct {
	sp      *scopePlan
	t       writeTo
	entries []manifest.Entry
	modules []string
}

// installAll is InstallAll but for the move hints: each write that adds entries is appended to
// added, except in a dry run, which adds nothing a move could find.
func (e *Engine) installAll(ctx context.Context, spec InstallSpec, added *[]installAdded) (_ *InstallResult, err error) {
	opts := spec.Options
	var scopes []Scope
	for _, si := range spec.Scopes {
		scopes = append(scopes, si.Scope)
	}
	defer func() {
		err = e.CoachUnresolved(err, scopes, opts.DryRun, opts.Display)
	}()
	swept := e.sweepRuns()
	run := spec.Staged
	if run == nil {
		if run, err = e.publishingRun(); err != nil {
			return nil, err
		}
		defer func() { err = errors.Join(err, run.Close()) }()
	}
	opts.staged = run
	var unlocks []func()
	defer func() {
		for i := len(unlocks) - 1; i >= 0; i-- {
			unlocks[i]()
		}
	}()
	seen := map[string]bool{}
	for _, si := range spec.Scopes {
		if seen[si.Scope.Key()] {
			where := "user scope"
			if si.Scope.Kind == target.Project {
				where = "project scope at " + untrusted.Text(si.Scope.ProjectDir, untrusted.MaxName)
			}
			return nil, errs.WithHints(fmt.Errorf("%s is listed twice", where), "list each destination once")
		}
		seen[si.Scope.Key()] = true
	}
	// A project rooted at the home directory renders where the user scope does (~/.claude, …):
	// its ledger and the user scope's would claim the same files, in this run or across runs,
	// and uninstalling one would delete the other's. uninstall still accepts one an older
	// lola installed.
	if home := (Scope{Kind: target.Project, ProjectDir: e.Paths.Home}); seen[home.Key()] {
		err := fmt.Errorf("project scope at %s is your home directory, where the user scope installs", untrusted.Text(e.Paths.Home, untrusted.MaxName))
		if spec.UserRun == "" {
			return nil, err
		}
		return nil, errs.WithHints(err, spec.UserRun)
	}
	// A remote profile a scope needs that this machine lacks is restored from pins.yaml first,
	// under the profiles lock it takes and releases itself (spec 2026-10-29 §3.4).
	var restored []Notice
	retry := ""
	if opts.Rerun != nil {
		retry = opts.Rerun(opts.Trust)
	}
	for _, si := range spec.Scopes {
		n, err := e.restoreRemotes(ctx, si.Scope, "", retry, opts.DryRun)
		if err != nil {
			return nil, err
		}
		restored = append(restored, n...)
	}
	// The profiles lock comes first (Plan 6b §10): the run reads the remote profiles once.
	unlockProfiles, err := e.readProfiles(opts.DryRun)
	if err != nil {
		return nil, err
	}
	unlocks = append(unlocks, unlockProfiles)
	// Marketplace addresses resolve under the markets lock, after the profiles lock and before
	// every other (Plan 6c §5), so nothing is fetched until each one names an entry.
	spec.Modules = slices.Clone(spec.Modules)
	unlockMarkets, err := e.resolveMarkets(spec.Modules, opts.DryRun, opts.Display)
	if err != nil {
		return nil, err
	}
	unlocks = append(unlocks, unlockMarkets)
	editing := len(spec.GlobalEdits) > 0 || slices.ContainsFunc(spec.Scopes, func(si ScopeInstall) bool { return len(si.Edits) > 0 })
	if editing && !opts.DryRun {
		unlock, err := config.Lock(e.Paths.SelectionsLock)
		if err != nil {
			return nil, err
		}
		unlocks = append(unlocks, unlock)
	}
	sel, err := e.selections()
	if err != nil {
		return nil, err
	}
	saved := sel // selections.yaml as the file has it: the run's own edits go to the copies withPicks makes
	res := newResult(opts.DryRun)
	res.Trusted = []Consent{} // a command that takes --trust says what it covered, [] for nothing
	res.Notices = append(res.Notices, swept...)
	res.Notices = append(res.Notices, restored...)
	res.home = e.Paths.Home
	out := &InstallResult{Result: res, Scopes: []ScopeResult{}}
	probes := e.newProbeRun(ctx)
	var fetched []string
	// staged: the arguments (arg) whose modules (name) were fetched into a staged run, in the cache
	// only once the apply commits them.
	type stagedArg struct{ name, arg string }
	var staged []stagedArg
	have := map[int]store.Version{} // a source fetched for one scope is not fetched again for the next
	hashed := map[int]bool{}        // have[i] is a version this run staged and hashed itself (resolvedRequest.hashed)
	haveRef := map[int]string{}     // ... unless the next scope's entry records another ref
	plans := make([]*scopePlan, len(spec.Scopes))
	replacing := replacingNames(spec.Modules)
	for _, i := range lockOrder(spec.Scopes) {
		si := spec.Scopes[i]
		man, led, unlock, others, err := e.loadReplacing(si.Scope, opts.DryRun, replacing)
		if err != nil {
			return nil, err
		}
		unlocks = append(unlocks, unlock)
		if len(others) > 0 {
			res.Notices = append(res.Notices, e.unresolvedNotices(si.Scope, others, opts.DryRun, opts.Display)...)
		}
		// The targets an install by source keeps (planInstallModule) and the files it removes
		// come from the ledger, which is this checkout's own only while git does not track it.
		if err := e.refuseTrackedLedger(ctx, si.Scope, res); err != nil {
			return nil, err
		}
		rec, err := e.trustRecord(si.Scope)
		if err != nil {
			return nil, err
		}
		// The scope's profiles are read before anything is fetched: one that does not load refuses
		// the install, and their entries give the precedence notices (Plan 6b §7, §10).
		layers, _, err := e.layers(si.Scope, man, led)
		if err != nil {
			return nil, err
		}
		plans[i] = &scopePlan{sc: si.Scope, man: man, loaded: man.Clone(), trust: rec, led: led, layers: layers, targets: si.Targets,
			given: si.Targets, replace: si.Replace, unresolved: holdsAddress(man), saved: saved}
		if plans[i].target, plans[i].infos, err = e.writeTarget(si.Scope, layers, nil, opts.Target, opts.Target.verb(), installTyped(spec.Modules), opts.AskWhere); err != nil {
			return nil, err
		}
		if plans[i].picks, err = e.picksAt(si.Scope, plans[i].target.from()); err != nil {
			return nil, err
		}
		if plans[i].scopePicks, err = e.scopeSection(si.Scope); err != nil {
			return nil, err
		}
		res.Notices = append(res.Notices, e.keyNotices(si.Scope)...)
		plans[i].profiles = e.visibleOnce(si.Scope)
		if infos := plans[i].infos; infos != nil {
			plans[i].profiles = func() ([]profile.Info, error) { return infos, nil }
		}
		if sel, err = withPicks(sel, append(slices.Clone(layers), plans[i].target.Layer)); err != nil {
			return nil, err
		}
		if plans[i].templates, err = e.newTemplateRun(si.Scope, led.Profile); err != nil {
			return nil, err
		}
		if len(si.Targets) == 0 && rec != nil {
			plans[i].untrustedTargets = e.uncoveredTargets(si.Scope, plans[i], spec.Modules, opts.Target)
		}
	}
	// A name installs the entry as project.yml has it: refused for an entry someone else
	// changed, at every scope before any request is resolved, so nothing is fetched (Plan 6a
	// §10.1).
	for k, sp := range plans {
		for _, r := range spec.Modules {
			if ent := sp.target.layerEntry(r.Arg); ent != nil && !sp.covers(*ent) {
				return nil, e.refuseUncovered(spec.Scopes[k].Scope, sp, ent)
			}
			if sp.untrustedTargets != nil && len(r.Targets) == 0 && !e.hasOwnTargets(sp, spec.Scopes[k].Scope, spec.Cwd, r) {
				return nil, sp.untrustedTargets
			}
		}
	}
	for k, si := range spec.Scopes {
		sp := plans[k]
		if len(sp.targets) == 0 {
			sp.targets = fileTargets(sp.target, sp.man)
		}
		if len(sp.targets) == 0 {
			return nil, e.noTargets()
		}
		for _, id := range sp.targets {
			if _, err := e.target(id); err != nil {
				if len(si.Targets) > 0 { // the user's -t, not a line of a file
					return nil, err
				}
				path, shownAs := e.basePath(si.Scope), ""
				switch {
				case sp.target.ownTargets() && sp.target.Key != "":
					path, shownAs = sp.target.Path, projectProfileFile(sp.target.Key)
				case sp.target.ownTargets():
					path, shownAs = sp.target.Path, shown(sp.target.Path)
				default:
					shownAs = filepath.Base(path)
				}
				at, _ := targetsOrigin(shownAs, path, "")
				return nil, fmt.Errorf("%s%w", at, err)
			}
		}
		for _, r := range spec.Modules {
			for _, id := range r.Targets {
				if _, err := e.target(id); err != nil {
					return nil, err
				}
			}
		}
		// The entries a name installs are the target file's, or the entry another layer gives the
		// scope for it (inheritedFrom), at its ref (spec 2026-10-29 §3.6).
		own := &manifest.Manifest{Modules: sp.target.Entries}
		for i, r := range spec.Modules {
			listed := entryRef(own, r.Arg)
			if w := inheritedFrom(sp.layers, sp.target, r.Arg); own.Find(r.Arg) == nil && w != nil {
				listed = providerEntry(sp.layers, w).Ref
			}
			ref := listed
			if r.Ref != "" {
				ref = r.Ref
			}
			// A scope with no entry installs the listed version and records the ref of the head
			// that holds it: its pin must be reachable from the ref its entry records. An entry
			// without a ref tracks the default branch and keeps doing so.
			if ref == "" && r.Version != nil && own.Find(r.Arg) == nil && r.market == nil {
				if r.Ref, err = e.listedRef(run, *r.Version); err != nil {
					return nil, e.withRetry(spec, err, fetched)
				}
				ref = r.Ref
			}
			pre, preHashed := have[i], hashed[i]
			if haveRef[i] != ref {
				pre, preHashed = store.Version{}, false
			}
			if pre.Name == "" && r.Version != nil {
				if pre, err = e.pinnedVersion(run, r); err != nil {
					return nil, e.withRetry(spec, err, fetched)
				}
				// The pickers list the head of the entry's ref; any other version is not what was confirmed.
				if r.Ref == "" && own.Find(r.Arg) != nil {
					if head, ok, err := e.Store.Head(r.Arg, ref); err != nil {
						return nil, e.withRetry(spec, err, fetched)
					} else if ok && head.ID != pre.ID {
						which := "records ref " + untrusted.Text(ref, untrusted.MaxName)
						if ref == "" {
							which = "tracks the default branch"
						}
						return nil, errs.WithHints(fmt.Errorf("%s was listed at version %s, but %s %s, whose cached version is %s",
							untrusted.Text(r.Arg, untrusted.MaxName), untrusted.Text(pre.ID, untrusted.MaxName), si.Scope.Key(),
							which, untrusted.Text(head.ID, untrusted.MaxName)),
							"run lola install again to list the current versions")
					}
				}
			}
			ent, v, didFetch, err := e.resolve(ctx, si.Scope, spec.Cwd, r, pre, listed, run, spec.Options.Display)
			if err != nil {
				return nil, e.withRetry(spec, err, fetched)
			}
			if want, ok := spec.At[v.Name]; ok {
				if err := e.installAt(run, v, ent.Ref, want, spec.AtFresh); err != nil {
					return nil, err
				}
			}
			if didFetch {
				again := v.Name
				if r.market != nil {
					again = r.Arg // its name would install it without its marketplace
				}
				// A staged fetch reaches the cache only when the apply commits it: until then the
				// retry hint, which installs it from the cache, would name nothing there.
				if run.publish {
					fetched = append(fetched, again)
				} else {
					staged = append(staged, stagedArg{name: v.Name, arg: again})
				}
				// Only a staged version's digest is lola's own (store.Prepare hashed the staged tree): a
				// publishing run's commit returns a version already cached as the cache records it.
				have[i], haveRef[i], hashed[i] = v, ref, !run.publish
			}
			// Installing a version is a use of it, however the run found it; a dry run, and so lola
			// info's plan, is none (Plan 9 §6.3). Recording it is best effort: a cache whose
			// index.yml cannot be written still installs.
			if !opts.DryRun {
				_ = e.Store.Used(v.Name, v.ID)
			}
			sp.resolved = append(sp.resolved, resolvedRequest{req: r, ent: ent, v: v, hashed: didFetch && hashed[i] || !didFetch && preHashed && v.Name == pre.Name && v.ID == pre.ID})
			if r.market != nil {
				if k == 0 {
					res.Notices = append(res.Notices, entryNotices(r.market, r.Ref)...)
				}
				res.Notices = append(res.Notices, e.dependencyNotices(si.Scope, r.market, opts.DryRun)...)
			}
		}
		out.Scopes = append(out.Scopes, ScopeResult{Scope: si.Scope.Kind.String(), Root: root(si.Scope), ScopeSource: si.Source})
	}
	for _, name := range slices.Sorted(maps.Keys(spec.At)) {
		if len(plans) > 0 && !slices.ContainsFunc(plans[0].resolved, func(rr resolvedRequest) bool { return rr.v.Name == name }) {
			return nil, errs.WithHints(fmt.Errorf("--at names %s, which this install does not include", shown(name)), "run it without --at: "+spec.AtFresh)
		}
	}
	// Each address is shown resolved before any question about what it installs (ruling R-6c-15).
	if opts.ShowResolved != nil && len(plans) > 0 {
		var lines []string
		for _, rr := range plans[0].resolved {
			if rr.req.market != nil {
				lines = append(lines, resolvedLine(rr.req.Arg, rr.ent, rr.v.Commit))
			}
		}
		if len(lines) > 0 {
			opts.ShowResolved(lines)
		}
	}
	saves, err := e.editSelections(sel, spec, plans, res)
	if err != nil {
		return nil, e.withRetry(spec, err, fetched)
	}
	for _, sp := range plans {
		for _, rr := range sp.resolved {
			if err := e.planInstallModule(ctx, sp, rr, sel, saves, opts, probes, res); err != nil {
				return nil, e.withRetry(spec, err, fetched)
			}
		}
		for _, tw := range sp.writes {
			notes, overridden := e.targetNotices(sp.sc, tw.t, sp.layers, tw.entries, sp.infos, opts)
			res.Notices = append(res.Notices, notes...)
			for _, m := range overridden {
				delete(sp.overrides, m)
			}
		}
	}
	// A profile two scopes write (on in both, spec 2026-10-29 §3.9) is written once, by the first
	// scope's apply; scopes that would write it differently are refused before any write.
	first := map[string][]manifest.Entry{}
	for _, sp := range plans {
		sp.shared = map[string]bool{}
		for _, tw := range sp.writes {
			if !tw.t.Profile {
				continue
			}
			was, seen := first[tw.t.Path]
			if !seen {
				first[tw.t.Path] = tw.entries
				continue
			}
			if !reflect.DeepEqual(was, tw.entries) {
				var runs []string
				for _, si := range spec.Scopes {
					args := requestArgs(spec.Modules, opts.Target)
					if len(si.Targets) > 0 {
						args = append(args, "-t", strings.Join(si.Targets, ","))
					}
					runs = append(runs, e.installRun(si.Scope, opts.Target, args...))
				}
				return nil, errs.WithHints(fmt.Errorf("%s would be written differently for each scope", tw.t.label),
					"install into one scope at a time: "+strings.Join(runs, ", then "))
			}
			sp.shared[tw.t.Path] = true
		}
	}
	// Each file written gets its write lines once: a profile two scopes write, by the first scope.
	for _, sp := range plans {
		for _, tw := range sp.writes {
			if sp.shared[tw.t.Path] {
				continue
			}
			edits := editsOf(ctx, sp.sc, tw.t, tw.entries, nil, sp.overrides)
			res.Edits = append(res.Edits, edits...)
			res.Notices = append(res.Notices, e.alsoOn(sp.sc, tw.t, append(slices.Clone(scopes), spec.then...))...)
			if spec.profiles != nil && tw.t.Profile {
				*spec.profiles = append(*spec.profiles, fileEdit{layer: tw.t.Layer, change: sp.fileChange(tw)})
			}
			var modules []string
			for _, ed := range edits {
				if ed.Action == EditAdded {
					modules = append(modules, ed.Module)
				}
			}
			if len(modules) > 0 && !opts.DryRun {
				*added = append(*added, installAdded{sp: sp, t: tw.t, entries: tw.entries, modules: modules})
			}
		}
	}
	// selections.yaml holds no profile entry's picks, nor those of a module overridden from
	// project.yml, which its profile entry now holds.
	var onFile []config.Change
	for _, c := range saves {
		if c.Section.Kind == config.SectionProfile || slices.ContainsFunc(plans, func(sp *scopePlan) bool {
			return c.Section == sp.scopePicks && slices.Contains(sp.dropped, c.Module)
		}) {
			continue
		}
		onFile = append(onFile, c)
	}
	for i := range res.Modules {
		name := res.Modules[i].Name
		if opts.DryRun || !slices.ContainsFunc(saves, func(c config.Change) bool { return c.Module == name }) {
			continue
		}
		if slices.ContainsFunc(onFile, func(c config.Change) bool { return c.Module == name }) {
			res.Modules[i].SavedTo = sel.Path
		}
		for _, sp := range plans {
			for _, tw := range sp.writes {
				if tw.t.Profile && slices.ContainsFunc(tw.entries, func(x manifest.Entry) bool { return x.Name == name }) {
					res.Modules[i].SavedTo = tw.t.Path
				}
			}
		}
	}
	// A project's base picks are personal (spec 2026-11-03 Pick-P2-a): a run whose --only, --skip
	// or --unskip flags save picks in a project's section says so once per section, and how a team
	// shares picks instead; picker answers and a plain install do not.
	flagged := map[config.Where]bool{}
	for i, si := range spec.Scopes {
		if slices.ContainsFunc(si.Edits, func(ed SelectionEdit) bool { return ed.Flag }) {
			flagged[plans[i].scopePicks] = true
		}
	}
	var personal []config.Where
	picked := map[config.Where][]string{}
	for _, c := range onFile {
		if opts.DryRun || c.Section.Kind != config.SectionProject || !flagged[c.Section] || c.Entry.Empty() || slices.Contains(picked[c.Section], shown(c.Module)) {
			continue
		}
		if picked[c.Section] == nil {
			personal = append(personal, c.Section)
		}
		picked[c.Section] = append(picked[c.Section], shown(c.Module))
	}
	for _, w := range personal {
		res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: fmt.Sprintf("picks for %s saved in %s: they are yours, not your teammates'; a project profile shares picks with your team",
			strings.Join(picked[w], ", "), untrusted.Text(w.Origin(), untrusted.MaxName))})
	}
	res.Findings = append(res.Findings, probes.findings...)
	var preps []*prepared
	for _, sp := range plans {
		n := len(res.Steps)
		named := opts
		named.named = nil
		for _, rr := range sp.resolved {
			named.named = append(named.named, rr.ent.Name)
		}
		pr, err := e.prepare(sp.sc, sp.led, sp.changes, sp.copies, sp.replace, named, res)
		if err != nil {
			return nil, e.withRetry(spec, err, fetched)
		}
		pr.resolved = named.named
		keepUnhooked(sp.copies, pr.jobs, opts)
		for i := n; i < len(res.Steps); i++ {
			res.Steps[i].Root = root(sp.sc)
		}
		preps = append(preps, pr)
	}
	if err := e.confirmDeletes(spec, out); err != nil {
		return nil, err
	}
	// What --trust covers is shown once, before the first write, then recorded (Plan 9 §3.4).
	if err := e.confirmTrusted(opts, res); err != nil {
		return nil, err
	}
	defer func() { err = res.afterGrants(err) }() // a failure from here on carries the grants written
	for _, sp := range plans {
		for _, tw := range sp.writes {
			if !tw.t.Profile {
				res.Notices = append(res.Notices, outsideNotices(sp.sc, tw.entries)...)
			}
		}
	}
	if opts.DryRun {
		return out, nil
	}
	// What a staged run fetched reaches the cache now, after the receipt and before the first
	// write, and only the versions this install writes (cherry-pick design §7): every refusal,
	// declined question and dry run above returned with the cache as it was. A different module
	// of the name cached since is refused here, with nothing of the install written; the retry
	// names what the commit cached before it.
	var changes []plan.ModuleChange
	for _, sp := range plans {
		changes = append(changes, sp.changes...)
	}
	committed, err := run.commit(e.Store, changes)
	for _, s := range staged {
		if slices.ContainsFunc(committed, func(v store.Version) bool { return v.Name == s.name }) {
			fetched = append(fetched, s.arg)
		}
	}
	if err != nil {
		return nil, e.withRetry(spec, err, fetched)
	}
	if spec.BeforeWrite != nil {
		if err := spec.BeforeWrite(out); err != nil {
			return nil, e.withRetry(spec, err, fetched)
		}
	}
	if len(onFile) > 0 {
		if err := config.Save(sel.Path, onFile); err != nil {
			return nil, e.withRetry(spec, err, fetched)
		}
	}
	for i, sp := range plans {
		var change manifest.Change
		var files []fileEdit
		for _, tw := range sp.writes {
			c := sp.fileChange(tw)
			switch {
			case !tw.t.Profile:
				change = c
			case !sp.shared[tw.t.Path]:
				files = append(files, fileEdit{layer: tw.t.Layer, change: c})
			}
		}
		updated := "the project's files, lock.json and " + e.manifestName(sp.sc) + " were updated"
		err := e.saveManifest(sp.sc, sp.loaded, change, nil, files, sp.trust, updated, syncAgain(e.SyncCommand(sp.sc)), func(man []plan.ManifestWrite) error {
			return e.commit(ctx, preps[i], opts, res, man, func(*plan.Plan) error { return e.makePluginData(sp.sc, sp.changes) })
		})
		if err != nil && len(onFile) > 0 {
			err = e.errApplied(err, sel.Path, sp.sc)
		}
		if err != nil && i < len(plans)-1 {
			var skipped []string
			for _, later := range plans[i+1:] {
				if later.sc.Kind == target.User {
					skipped = append(skipped, "the user scope")
				} else {
					skipped = append(skipped, "the project at "+untrusted.Text(later.sc.ProjectDir, untrusted.MaxName))
				}
			}
			err = fmt.Errorf("%w; lola stopped there and did not install into %s", err, andJoin(skipped))
		}
		if err != nil {
			return nil, e.withRetry(spec, err, fetched)
		}
	}
	return out, nil
}

// outsideNotices warns of each entry written to a project's manifest whose local source resolves
// outside the project: the manifest is committed, and a teammate's sync refuses such a source
// (checkLocalSources). A source that cannot be resolved here adds none; the sync names the problem.
func outsideNotices(sc Scope, entries []manifest.Entry) []Notice {
	var out []Notice
	for _, ent := range entries {
		spec, ok, err := source.Parse(ent.Source, sc.ProjectDir)
		if err != nil || !ok || !source.IsLocal(spec.Kind, spec.Location) {
			continue
		}
		if in, err := source.IsWithin(canonicalRoot(sc.ProjectDir), spec.Location); err != nil || in {
			continue
		}
		out = append(out, Notice{Level: LevelWarn, Text: fmt.Sprintf("%s comes from %s, outside the project: a teammate's sync will refuse it; "+
			"share a module through a git repository or an https:// archive", shown(ent.Name), untrusted.Text(ent.Source, untrusted.MaxName))})
	}
	return out
}

// covers reports whether the scope's trust record covers ent, an entry of the target (sp.target's
// file, layerEntry; covered).
func (sp *scopePlan) covers(ent manifest.Entry) bool { return sp.target.covered(sp.trust, ent) }

// covered reports whether rec, the scope's trust record, covers ent, an entry of t's file
// (layerEntry): project.yml's part, or a project profile's, which records each entry with the
// targets it installs to, its own or else the profile's (trust.ProfileView), so ent is compared
// so too; at user scope (rec nil), and for a profile of yours, every entry is the user's own.
func (t writeTo) covered(rec *trust.Record, ent manifest.Entry) bool {
	if rec == nil || t.Profile && t.Key == "" {
		return true
	}
	if t.Key != "" && len(ent.Targets) == 0 && t.read != nil {
		ent.Targets = slices.Clone(t.read.Targets)
	}
	return rec.View.Part(t.Key).Covers(ent)
}

// refuseUncovered is the refusal to rewrite ent, an entry of sp's target that the trust record does
// not cover: notTrustedIn for a project profile's, notTrusted for project.yml's.
func (e *Engine) refuseUncovered(sc Scope, sp *scopePlan, ent *manifest.Entry) error {
	if sp.target.Key != "" {
		return e.notTrustedIn(sc, sp.trust, sp.target, ent)
	}
	return e.notTrusted(sc, sp.trust, ent, false, sp.unresolved)
}

// fileTargets is the top-level targets an entry of t's file without targets of its own installs
// to: t's targets: when t is a profile that has them, else the base's, man's (project.yml's, or
// default.yml's at user scope).
func fileTargets(t writeTo, man *manifest.Manifest) []string {
	if t.ownTargets() {
		return t.read.Targets
	}
	return man.Targets
}

// targetsCovered reports whether rec, the scope's trust record (nil at user scope), covers the
// top-level targets an entry of t's file without targets of its own installs to (fileTargets),
// man being the scope's base: project.yml's are its part's targets, a project profile's its part's
// (spec 2026-10-29 §3.10), and yours are your own.
func targetsCovered(rec *trust.Record, t writeTo, man *manifest.Manifest) bool {
	switch {
	case rec == nil, t.ownTargets() && t.Key == "":
		return true
	case t.ownTargets():
		return slices.Equal(rec.View.Part(t.Key).Targets, t.read.Targets)
	}
	return len(man.Targets) == 0 || slices.Equal(rec.View.Targets, man.Targets)
}

// notTrusted refuses to rewrite ent, an entry of sc's manifest that rec, the project's trust
// record, does not cover (Plan 6a §10.1, §10.4, §10.8): a pull or a hand edit changed it since
// lola wrote it, or the project has no record on this machine. lola sync shows the change and
// asks; installing from the source instead would re-pin to what it serves and skip that review.
// remove adds the other way out, uninstalling it. unresolved, a manifest that holds a marketplace
// address (holdsAddress), drops both hints: that sync and that uninstall refuse the file.
func (e *Engine) notTrusted(sc Scope, rec *trust.Record, ent *manifest.Entry, remove, unresolved bool) error {
	why := untrustedWhy(rec, ent.Name)
	var hints []string
	if !unresolved {
		hints = append(hints, "review and apply it: "+e.SyncCommand(sc, ent.Name))
		if remove {
			hints = append(hints, "or remove it: "+e.Command(sc, "uninstall", ent.Name))
		}
	}
	return errs.WithHints(&notTrustedError{untrusted.Text(ent.Name, untrusted.MaxName) + ": " + e.manifestName(sc) + " " + why},
		hints...)
}

// notTrustedIn is notTrusted for ent, an entry of t, a project profile, that rec does not cover
// (Plan 6b §8.1): its file is named, and hinted the sync that reviews it, or for a profile that is
// not on, which a sync does not read, turning it on, which reviews it.
func (e *Engine) notTrustedIn(sc Scope, rec *trust.Record, t writeTo, ent *manifest.Entry) error {
	why := untrustedWhy(&trust.Record{View: rec.View.Part(t.Key), Exists: rec.Exists}, ent.Name)
	hint := "review and apply it: " + e.SyncCommand(sc, ent.Name)
	if !t.on {
		hint = "review it by turning the profile on: " + e.switchRun(sc, t)
	}
	return errs.WithHints(&notTrustedError{shown(ent.Name) + ": " + projectProfileFile(t.Key) + " " + why}, hint)
}

// untrustedWhy says why rec does not cover the manifest entry name, after the manifest's name:
// the project has no record on this machine, the record never held the entry (a pull added it),
// or the entry changed since it was trusted. A nil rec is a record that holds it.
func untrustedWhy(rec *trust.Record, name string) string {
	switch {
	case rec != nil && !rec.Exists:
		return "has not been trusted on this machine"
	case rec != nil && rec.View.Find(name) == nil:
		return "added it since you last trusted the file"
	}
	return "changed for it since you last trusted it"
}

// uncoveredTargets is the refusal of an install at sc that would take the top-level targets of
// sp's target file (fileTargets) while sp's trust record does not cover them (targetsCovered;
// Plan 6a §10.1, M2), nil when it does: a pull may have put them there, and installing to them
// would trust them unseen. The sync that reviews them is hinted, or for a profile that is not on,
// which a sync does not read, turning it on; then the install naming its assistants, with the
// run's --profile (req). While the manifest holds an unresolved entry (Plan 6c §6.7), the sync
// refuses the file, so only the install is hinted.
func (e *Engine) uncoveredTargets(sc Scope, sp *scopePlan, reqs []InstallRequest, req TargetRequest) error {
	t, rec := sp.target, sp.trust
	if targetsCovered(rec, t, sp.man) {
		return nil
	}
	file, why := e.manifestName(sc), "changed since you last trusted them"
	switch {
	case !rec.Exists:
		why = "have not been trusted on this machine"
	case t.ownTargets() && len(rec.View.Part(t.Key).Targets) == 0:
		why = "have not been trusted yet" // the record holds the profile's entries, never its targets
	}
	if t.ownTargets() {
		file = projectProfileFile(t.Key)
	}
	err, named := &untrustedTargetsError{file + "'s top-level targets " + why}, e.installRun(sc, req, requestArgs(reqs, req)...)+" -t <id>"
	switch {
	case t.ownTargets() && !t.on:
		return errs.WithHints(err, "review them by turning the profile on: "+e.switchRun(sc, t), "or name the assistants: "+named)
	case sp.unresolved:
		return errs.WithHints(err, "name the assistants: "+named)
	}
	// project.yml's top-level targets no entry the sync loads installs to are recorded only with
	// --trust=entries (spec 2026-10-29 §16.1): a plain sync would not ask about them.
	if !t.ownTargets() {
		layers, _, _, lerr := e.uses(sc)
		if lerr != nil {
			return lerr
		}
		if !installsToTargets(layers) {
			return errs.WithHints(err, "trust them: "+e.SyncCommand(sc, TrustEntries.Arg()), "or name the assistants: "+named)
		}
	}
	return errs.WithHints(err, "review and apply them: "+e.SyncCommand(sc), "or name the assistants: "+named)
}

// requestArgs is the arguments of an install of reqs as a hint repeats them: each argument with
// its --ref and --path, then req's --profile and --override.
func requestArgs(reqs []InstallRequest, req TargetRequest) []string {
	var args []string
	for _, r := range reqs {
		args = append(args, r.Arg)
		if r.Ref != "" {
			args = append(args, "--ref", r.Ref)
		}
		if r.Path != "" {
			args = append(args, "--path", r.Path)
		}
	}
	if req.Profile != "" {
		args = append(args, "--profile", req.Profile)
	}
	if req.Override {
		args = append(args, "--override")
	}
	return args
}

// hasOwnTargets reports whether r installs an entry of sp's target file that has targets of its
// own: the entry r names, or the one whose source is the location r gives. Anything else would
// install to the top-level targets, so a scope whose record does not cover them refuses it
// before a source is fetched.
func (e *Engine) hasOwnTargets(sp *scopePlan, sc Scope, cwd string, r InstallRequest) bool {
	if ent := sp.target.layerEntry(r.Arg); ent != nil {
		return len(ent.Targets) > 0
	}
	spec, ok, err := source.Parse(r.Arg, cwd)
	if err != nil || !ok {
		return false
	}
	for _, l := range sp.target.Entries {
		ent := sp.target.layerEntry(l.Name)
		loc := ent.Source
		if !filepath.IsAbs(loc) && !strings.Contains(loc, "://") && sc.Kind == target.Project {
			loc = filepath.Join(canonicalRoot(sc.ProjectDir), loc)
		}
		if len(ent.Targets) > 0 && ent.Path == r.Path && source.SameLocation(spec.Kind, loc, spec.Location) {
			return true
		}
	}
	return false
}

// untrustedTargetsError is untrustedTargets' refusal; its message says why the targets are not
// covered.
type untrustedTargetsError struct{ msg string }

func (e *untrustedTargetsError) Error() string { return e.msg }

// ErrNotTrusted is wrapped by the refusal to rewrite a manifest entry the trust record does not
// cover.
var ErrNotTrusted = errors.New("the trust record does not cover the entry")

// notTrustedError is notTrusted's refusal; its message says why the entry is not covered.
type notTrustedError struct{ msg string }

func (e *notTrustedError) Error() string { return e.msg }
func (e *notTrustedError) Unwrap() error { return ErrNotTrusted }

// trustRecord is the trust record of a project scope, nil at user scope, whose files are the
// user's own (Plan 6a §7.1).
func (e *Engine) trustRecord(sc Scope) (*trust.Record, error) {
	if sc.Kind != target.Project {
		return nil, nil
	}
	return e.Trust.Load(sc.ProjectDir)
}

// profileEdit is the pins a sync writes into one project profile (Plan 6b §6.4, §19 R1): layer
// is the profile as the run read it, and pins its entries, as the layer has them, with their new
// pins.
type profileEdit struct {
	layer Layer
	pins  []manifest.Entry
}

// fileEdit is an edit an install or uninstall makes to one profile file (spec 2026-10-29 §8.4):
// layer, the file as the run read it (read nil for a file of yours that did not exist), and
// change, the entries it sets, as the file records them, and those it removes.
type fileEdit struct {
	layer  Layer
	change manifest.Change
	// data, when set, is the file's bytes as lola makes or rewrites it whole (profileWrites):
	// written as they are once check passes. The trust record holds what the run took as trusted
	// of it before its check (profileWrites.trusted) and what the user accepted, never more.
	data  []byte
	check func() error
}

// installTyped is what an install names, as typed (its arguments), for a question about where it
// writes: "kit", or "kit and ./notes".
func installTyped(reqs []InstallRequest) string {
	args := make([]string, 0, len(reqs))
	for _, r := range reqs {
		args = append(args, r.Arg)
	}
	return andJoin(args)
}

// saveManifest is every manifest write (Plan 6a §7.4). It edits the scope's manifest by c, the
// change lola makes to loaded (the manifest as read under the scope lock), unless c changes
// nothing, writes the pins of profiles into those project profiles (Plan 6b §6.4), makes the
// edits of files to those profile files (default.yml at user scope), and hands the edits to
// apply, which commits them with the ledger. After a successful apply at project scope that wrote
// a manifest or profile it refreshes rec, the project's trust record, per field (trust.Refresh),
// in project.yml's part and each profile's: what this write changed becomes trusted, and nothing
// else does. A record that cannot be written leaves lola's own edits uncovered, which later
// commands refuse: the error says what was applied (the caller's applied) and how to get them
// trusted (its hint).
func (e *Engine) saveManifest(sc Scope, loaded *manifest.Manifest, c manifest.Change, profiles []profileEdit, files []fileEdit, rec *trust.Record,
	applied, hint string, apply func([]plan.ManifestWrite) error) error {
	var writes []plan.ManifestWrite
	edited := c.Targets != nil || len(c.Set) > 0 || len(c.Remove) > 0
	if edited {
		dst, data, err := manifest.Edit(e.files(sc).manifest, c)
		if err != nil {
			return err
		}
		writes = append(writes, plan.ManifestWrite{Path: dst, Data: data, KeepMode: sc.Kind == target.User})
	}
	for _, p := range profiles {
		data, err := p.layer.read.Pin(p.layer.Path, p.pins)
		if err != nil {
			return err
		}
		read, path := p.layer.read, p.layer.Path
		writes = append(writes, plan.ManifestWrite{Path: path, Data: data, Check: func() error { return read.Unchanged(sc.ProjectDir, path) }})
	}
	for _, f := range files {
		if f.data != nil {
			// A file of yours is written through its link and keeps its mode, as an edit's is
			// (Profile.Edit); a project's is a file of its own (its check refuses a link).
			dst := f.layer.Path
			if f.layer.Key == "" {
				var err error
				if dst, err = linkTarget(f.layer.Path); err != nil {
					return err
				}
			}
			writes = append(writes, plan.ManifestWrite{Path: dst, Data: f.data, Check: f.check, KeepMode: f.layer.Key == ""})
			continue
		}
		dst, data, err := f.layer.read.Edit(f.layer.Path, f.change)
		if err != nil {
			return err
		}
		read, path := f.layer.read, f.layer.Path
		check := func() error { return read.Current(path) }
		if f.layer.Key != "" {
			check = func() error { return read.Unchanged(sc.ProjectDir, path) }
		}
		writes = append(writes, plan.ManifestWrite{Path: dst, Data: data, Check: check, KeepMode: f.layer.Key == ""})
	}
	// Nothing written leaves the record nothing new to cover.
	if err := apply(writes); err != nil || rec == nil || len(writes) == 0 {
		return err
	}
	view := rec.View
	if edited {
		view = trust.Refresh(view, "", trust.ViewOf(loaded), trust.ViewOf(loaded.Applied(c)))
	}
	for _, p := range profiles {
		was := &manifest.Manifest{Modules: p.layer.Entries}
		view = trust.Refresh(view, p.layer.Key, trust.ViewOf(was), trust.ViewOf(was.Applied(manifest.Change{Set: p.pins})))
	}
	for _, f := range files {
		if f.layer.Key == "" {
			continue
		}
		if f.data != nil { // rec holds what of it is trusted already (profileWrites.trusted)
			continue
		}
		was := &manifest.Manifest{Modules: f.layer.Entries}
		if f.layer.read != nil {
			was.Targets = f.layer.read.Targets
		}
		saved := was.Applied(viewChange(f))
		for k := range saved.Modules {
			// Targets lola writes into a profile that had none are the targets of its entries without any.
			if f.change.Targets != nil && len(saved.Modules[k].Targets) == 0 {
				saved.Modules[k].Targets = slices.Clone(f.change.Targets)
			}
		}
		view = trust.Refresh(view, f.layer.Key, trust.ViewOf(was), trust.ViewOf(saved))
	}
	if err := e.saveTrust(sc, view); err != nil {
		var written []string
		if edited {
			written = append(written, e.manifestName(sc))
		}
		for _, p := range profiles {
			written = append(written, projectProfileFile(p.layer.Key))
		}
		for _, f := range files {
			if f.layer.Key != "" {
				written = append(written, projectProfileFile(f.layer.Key))
			}
		}
		return errs.WithHints(fmt.Errorf("%s, but lola could not record that you trust what it wrote to %s: %w", applied, andJoin(written), err), hint)
	}
	return nil
}

// viewChange is f's change as the trust record holds a project profile's part: the top-level
// targets it writes, and each entry's source as the scope reads it (beside .lola/profiles) and,
// without targets of its own, the profile's, as they are once written.
func viewChange(f fileEdit) manifest.Change {
	c := manifest.Change{Targets: f.change.Targets, Remove: f.change.Remove}
	targets := f.change.Targets
	if targets == nil && f.layer.read != nil {
		targets = f.layer.read.Targets
	}
	for _, ent := range f.change.Set {
		ent.Source = besideProfile(ent.Source, profile.ProjectDir)
		if len(ent.Targets) == 0 {
			ent.Targets = slices.Clone(targets)
		}
		c.Set = append(c.Set, ent)
	}
	return c
}

// saveTrust saves view as the trust record of sc, a project, without the parts of profiles
// whose file is no longer in .lola/profiles (ruling R-6b3a-5): those are dropped, never reported
// removed, so a deleted profile's record does not stay in trust.json for good.
func (e *Engine) saveTrust(sc Scope, view trust.View) error {
	for _, key := range slices.Sorted(maps.Keys(view.Profiles)) {
		if _, err := os.Lstat(filepath.Join(sc.ProjectDir, profile.ProjectDir, key+".yml")); errors.Is(err, fs.ErrNotExist) {
			view = view.WithPart(key, trust.View{})
		}
	}
	return e.Trust.Save(sc.ProjectDir, view)
}

// ProfileFile is how output names the file of the project profile q (project:<name>):
// .lola/profiles/<name>.yml.
func ProfileFile(q string) string {
	return projectProfileFile(strings.TrimPrefix(q, string(profile.KindProject)+":"))
}

// projectProfileFile is how output names the file of the project profile key,
// .lola/profiles/<key>.yml, through untrusted.Text: never a path to open.
func projectProfileFile(key string) string {
	return shown(filepath.Join(profile.ProjectDir, key+".yml"))
}

// syncAgain is the hint of every command whose trust record cannot be written after the project
// changed (Plan 6a §9.4): it fails closed, what lola wrote stays untrusted until the record can
// be written, and sync, the command named, asks about lola's own edits again.
func syncAgain(sync string) string {
	return "until it can, lola treats those entries as changed behind its back; fix the trust file, then run " + sync +
		" again to review and trust them"
}

// root is a scope's project root as results report it: "" at user scope.
func root(sc Scope) string {
	if sc.Kind == target.User {
		return ""
	}
	return sc.ProjectDir
}

// lockOrder is the order InstallAll locks scopes in (Plan 5c §14): project roots by canonical
// key, then the user scope, after the selections lock. Two runs that lock the same
// destinations take them in one order, so neither waits on the other in a cycle.
func lockOrder(scopes []ScopeInstall) []int {
	order := make([]int, len(scopes))
	for i := range order {
		order[i] = i
	}
	userLast := func(s Scope) int {
		if s.Kind == target.User {
			return 1
		}
		return 0
	}
	slices.SortStableFunc(order, func(a, b int) int {
		sa, sb := scopes[a].Scope, scopes[b].Scope
		return cmp.Or(cmp.Compare(userLast(sa), userLast(sb)), cmp.Compare(sa.Key(), sb.Key()))
	})
	return order
}

// editSelections applies spec's edits to sel in memory: the global edits, then each scope's,
// over the modules being installed. It returns the changes to save. With spec.WithMentions it
// adds, per scope, what the picks mention and leave out (editScope); with spec.NameMentions it
// names what they still leave out (mentionNotes). A module whose mentions cannot be read gets a
// warning instead and installs as picked.
func (e *Engine) editSelections(sel *config.Selections, spec InstallSpec, plans []*scopePlan, res *Result) ([]config.Change, error) {
	if len(plans) == 0 {
		return nil, nil
	}
	var mods []*module.Module
	for _, rr := range plans[0].resolved {
		mods = append(mods, rr.v.Module)
	}
	targets := editTargets(mods)
	var sections []func(string) config.Where
	all := places{path: sel.Path}
	for _, sp := range plans {
		sections, all.layers, all.infos = append(sections, sp.pickSection), append(all.layers, sp.picksLayers()...), append(all.infos, sp.infos...)
	}
	global := func(string) config.Where { return config.Global() }
	all.run = func(args ...string) string {
		return e.installRun(plans[0].sc, spec.Options.Target, append(slices.Clone(args), "--global")...)
	}
	changes, notes, err := applyEdits(sel, global, all, spec.GlobalEdits, targets, sections)
	if err != nil {
		return nil, err
	}
	res.Notices = append(res.Notices, notes...)
	scans := spec.Staged.scans()
	for i, si := range spec.Scopes {
		var mods map[string]mentionModule
		if spec.WithMentions || spec.NameMentions {
			mods = resolvedMentions(plans[i], spec, scans)
		}
		pl := places{path: sel.Path, layers: plans[i].picksLayers(), infos: plans[i].infos,
			run: func(args ...string) string { return e.installRun(plans[i].sc, spec.Options.Target, args...) }}
		se, err := editScope(sel, sections[i], pl, si.Edits, spec.GlobalEdits, targets, mods, spec.WithMentions)
		if err != nil {
			return nil, err
		}
		changes, res.Notices = append(changes, se.changes...), append(res.Notices, se.notes...)
		res.Notices = e.mentionNotes(res.Notices, si, se, targets, mods, spec)
	}
	return changes, nil
}

// fileChange is the change tw makes to its file at sp's scope: its entries, and the scope's targets
// as the file's top-level targets when it has none and is not a profile of yours used in other
// projects too, whose entries name the targets an install names instead (planInstallModule).
func (sp *scopePlan) fileChange(tw targetWrite) manifest.Change {
	c := manifest.Change{Set: tw.entries}
	if len(fileTargets(tw.t, sp.man)) == 0 && !tw.t.portable(sp.sc) {
		c.Targets = sp.targets
	}
	return c
}

// picksLayers is the layers whose profile entries hold picks at sp's scope: its layers and its
// write target, which may not be one of them.
func (sp *scopePlan) picksLayers() []Layer { return append(slices.Clone(sp.layers), sp.target.Layer) }

// pickSection is where module's picks are read and saved at sp's scope (pickSection).
func (sp *scopePlan) pickSection(module string) config.Where {
	return pickSection(sp.layers, sp.target, sp.picks, sp.scopePicks, module)
}

// planInstallModule plans one resolved module at sp's scope; saves are the run's selection edits
// (editSelections). With no targets given for the scope, a module keeps the targets its manifest
// entry has. An edit of a module another layer provides goes where placeEdit says; one that asks
// for what that entry says is a notice and writes nothing.
func (e *Engine) planInstallModule(ctx context.Context, sp *scopePlan, rr resolvedRequest, sel *config.Selections, saves []config.Change, opts Options, probes *probeRun,
	res *Result) error {
	r, ent, v, given := rr.req, rr.ent, rr.v, sp.given
	if slices.Contains(sp.planned, v.Name) {
		return fmt.Errorf("module %s is listed more than once", v.Name)
	}
	sp.planned = append(sp.planned, v.Name)
	// man is the target file's top-level targets, for the targets an entry takes.
	man := &manifest.Manifest{Targets: fileTargets(sp.target, sp.man)}
	modTargets := sp.targets
	prev := sp.target.layerEntry(v.Name)
	inherited := inheritedFrom(sp.layers, sp.target, v.Name)
	if inherited != nil {
		prev = providerEntry(sp.layers, inherited)
		// An override must never copy a pulled entry you have not trusted into a file you have (spec
		// 2026-10-29 §3.10): project.yml's entry is checked against its record first.
		if !sp.layers[0].Profile && inherited.From == sp.layers[0].Name && sp.trust != nil && !sp.trust.View.Covers(*prev) {
			return e.notTrusted(sp.sc, sp.trust, prev, false, sp.unresolved)
		}
	}
	// An install by source over an entry the trust record does not cover writes the entry as new
	// (Plan 6a.2 ruling S2): it inherits nothing a pull may have put there, and the trust refresh
	// records it whole. A request that adds targets (lola migrate's) keeps the entry's other
	// fields, so it is refused instead. With no targets given, the module stays on the targets
	// lock.json installed it on, which this checkout's own installs chose, so what is installed
	// is what the entry records.
	if inherited == nil && prev != nil && !sp.covers(*prev) {
		if len(r.Targets) > 0 {
			return e.refuseUncovered(sp.sc, sp, prev)
		}
		prev = nil
		unread := func(x manifest.Entry) bool { return x.Name == v.Name }
		if sp.target.Profile {
			sp.target.Entries = slices.DeleteFunc(slices.Clone(sp.target.Entries), unread)
		} else {
			sp.loaded.Modules = slices.DeleteFunc(sp.loaded.Modules, unread)
		}
		var onLedger []string
		if in := sp.led.Modules[v.Name]; in != nil && len(given) == 0 {
			onLedger = in.InstalledTargets()
		}
		if len(onLedger) > 0 {
			modTargets = onLedger
			if !slices.Equal(modTargets, man.Targets) {
				ent.Targets = modTargets
			}
		}
	}
	installed := sp.led.Modules[v.Name]
	keepPin := false
	if len(r.Targets) > 0 {
		var err error
		if v, err = e.installedVersion(sp.sc, v, prev, installed); err != nil {
			return err
		}
		// Adding assistants to an installed module never moves it, nor its pin.
		keepPin = prev != nil && installed != nil
		modTargets, ent.Targets = addedTargets(man, prev, sp.targets, r.Targets)
	} else if prev != nil && len(given) == 0 && len(prev.Targets) > 0 {
		modTargets, ent.Targets = prev.Targets, prev.Targets
	} else if len(given) > 0 && (sp.target.portable(sp.sc) || len(man.Targets) > 0 && !slices.Equal(given, man.Targets)) {
		ent.Targets = given
	}
	if prev != nil && r.Arg == v.Name {
		ent.Source = prev.Source
		ent.Path = prev.Path
		if r.Ref == "" || r.Ref == prev.Ref {
			// The entry stays where its marketplace listed it, so it keeps saying so (Plan 6c §6.4).
			ent.Ref, ent.Market = prev.Ref, prev.Market
		}
	}
	if sp.untrustedTargets != nil && len(r.Targets) == 0 && len(ent.Targets) == 0 {
		return sp.untrustedTargets // the module would install to the top-level targets
	}
	if prev != nil {
		ent.Rules, ent.Vars = prev.Rules, manifest.CloneVars(prev.Vars)
	}
	if opts.Rules != nil {
		ent.Rules = nil
		if !*opts.Rules {
			ent.Rules = opts.Rules
		}
	}
	section := sp.pickSection(v.Name)
	picksEdited := slices.ContainsFunc(saves, func(c config.Change) bool { return c.Module == v.Name && c.Section == section })
	wanted := sp.man.Targets // what the entry the request is compared with installs to
	if inherited == nil {
		wanted = man.Targets
	}
	if prev != nil && len(prev.Targets) > 0 {
		wanted = prev.Targets
	}
	// An inherited entry installs to the assistants its own layer gives it, whatever the write
	// target's top-level targets are, unless the run names others.
	if inherited != nil && len(given) == 0 && len(r.Targets) == 0 {
		modTargets = wanted
	}
	same := prev != nil && sameRequest(ent, *prev, modTargets, wanted) && r.Ref == "" && len(r.Targets) == 0
	dest := &sp.target // the file written; nil for none
	if opts.Target.Override && !slices.ContainsFunc(sp.layers, func(l Layer) bool {
		return l.Name != sp.target.Name && slices.ContainsFunc(l.Entries, func(x manifest.Entry) bool { return x.Name == v.Name })
	}) {
		res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: shown(v.Name) +
			": --override has nothing to override; no other layer provides it, and this install writes " + sp.target.label})
	}
	if inherited != nil {
		var err error
		if same && !picksEdited {
			res.Notices = append(res.Notices, inheritedNotice(v.Name, layerLabel(sp.infos, inherited.From), installed != nil))
			if installed != nil {
				return nil
			}
			dest = nil
		} else if dest, err = e.placeEdit(sp, inherited, &same, opts); err != nil {
			return err
		}
		if dest != nil && dest.Name == sp.target.Name && section != sp.picks {
			// Override: the target's entry starts from the inherited one's picks, edited.
			sel.Set(sp.picks, v.Name, sel.Entry(section, v.Name))
			if section == sp.scopePicks {
				sp.dropped = append(sp.dropped, v.Name)
			}
			section = sp.picks
		}
		// Written into a file whose top-level targets are not those, the entry names them.
		if dest != nil && len(prev.Targets) == 0 && len(ent.Targets) == 0 && !sameSet(modTargets, fileTargets(*dest, sp.man)) {
			ent.Targets = modTargets
		}
	}
	// Nothing changes but the edit (spec 2026-10-29 §3.6): an override, and a picks-only edit of an
	// installed module, install the version installed and keep its pin; a module the scope has not
	// installed yet installs what the inherited entry pins. A folder is never pinned, and a plain
	// sync installs the folder as it is: the edit installs the version the run resolved, the one
	// its component page offers, so the next sync contradicts nothing it did (cherry-pick design §14).
	overriding := inherited != nil && dest != nil && dest.Name == sp.target.Name
	if overriding {
		over := strings.TrimSuffix(strings.TrimPrefix(layerLabel(sp.infos, inherited.From), "profile "), " (base)")
		if sp.overrides == nil {
			sp.overrides = map[string]string{}
		}
		sp.overrides[v.Name] = over
		// An override copies a project file's entry into a file of yours, which no trust question
		// asks about: as a move carries it out (ruling P7).
		k := slices.IndexFunc(sp.layers, func(l Layer) bool { return l.Name == inherited.From })
		if from := (writeTo{Layer: sp.layers[k], on: true, label: layerLabel(sp.infos, inherited.From)}); sp.sc.Kind == target.Project &&
			(!from.Profile || from.Key != "") && dest.Profile && dest.Key == "" {
			if err := e.carriedOut(ctx, sp.sc, *dest, layerOf(from), []string{v.Name}, opts.Display); err != nil {
				return err
			}
		}
	}
	asListed := inherited != nil && ent.Source == prev.Source && ent.Ref == prev.Ref && ent.Path == prev.Path
	switch {
	case installed != nil && prev != nil && (overriding && asListed || same && picksEdited) && !e.isFolder(opts.staged, v.Name):
		var err error
		if v, err = e.installedVersion(sp.sc, v, prev, installed); err != nil {
			return err
		}
		keepPin = true
	case installed == nil && asListed && prev.Digest != "":
		// A version the run fetched and hashed itself is the pin when its digest is: the pin only
		// selects it. Any other is read as sync reads a pin (listedPin).
		if !rr.hashed || v.Meta.Digest != prev.Digest {
			var err error
			if v, err = e.listedPin(sp.sc, v, prev, layerLabel(sp.infos, inherited.From), opts.staged.dir); err != nil {
				return err
			}
		}
		keepPin = true
	}
	var commit string
	switch {
	case keepPin && installed == nil:
		// The inherited entry's pin is the version installed (listedPin); an override of the module,
		// which installs it, records that pin as the version installed.
		commit, ent.Commit, ent.Digest = prev.Commit, prev.Commit, prev.Digest
	case keepPin && overriding:
		// The override's entry pins the version installed, never the inherited entry's pin; a
		// folder's version is never pinned (ledgerPin, manifest's checkPinShape).
		commit, ent.Commit, ent.Digest = installed.Commit, "", ""
		if !e.isFolder(opts.staged, v.Name) {
			ent.Commit, ent.Digest = installed.Commit, installed.Digest
		}
	case keepPin:
		commit, ent.Commit, ent.Digest = installed.Commit, prev.Commit, prev.Digest
	default:
		var err error
		if commit, err = e.pinCommit(opts.staged, v, ent.Ref); err != nil {
			return err
		}
		if !e.isFolder(opts.staged, v.Name) {
			ent.Commit, ent.Digest = commit, v.Meta.Digest
		}
	}
	from := ""
	switch {
	case dest != nil:
		from = dest.from()
	default:
		from = ledgerFrom(inherited.From)
	}
	// An entry the scope does not load (a --profile that is not on), or one a higher layer's
	// entry wins over, is written and installs nothing (spec 2026-10-29 §3.5, ambiguity 3).
	if dest != nil && (!dest.on || winnerAbove(sp.layers, dest.Name, v.Name) != nil) {
		sp.write(sel, *dest, section, v.Name, ent)
		return nil
	}
	x, err := e.exclusions(sel, sp.sc, from, v.Name)
	if err != nil {
		return err
	}
	ignored, err := e.ignoredSection(sp.saved, sp.sc, sp.layers, sp.profiles, sp.led.Profile != nil, from, v.Name)
	if err != nil {
		return err
	}
	if ignored != nil {
		res.Notices = append(res.Notices, *ignored)
	}
	opts.freshPin, opts.entryFrom, opts.profiles = !keepPin, from, sp.profiles
	opts.checkArg = e.checkName(checkWord(mentionArgs(v.Name, rr.v, rr.req, opts.staged, opts.DryRun)), v, e.committing(opts.staged, opts.DryRun, v.Name))
	intents, finds, notes, mr, err := e.planModule(sp.sc, v, modTargets, &ent, x, opts, probes, sp.templates)
	if err != nil {
		return err
	}
	left, err := e.leftToAgents(sp.sc, v.Name, finds)
	if err != nil {
		return err
	}
	res.viaAgents = append(res.viaAgents, left...)
	mr.Scope, mr.Root, mr.Market = sp.sc.Kind.String(), root(sp.sc), shownMarket(ent.Market)
	res.Modules, res.Findings, res.Notices = append(res.Modules, mr), append(res.Findings, finds...), append(res.Notices, notes...)
	stale, err := e.staleNotices(sp.sc, v.Module, x, func(args ...string) (string, error) { return e.installRun(sp.sc, opts.Target, args...), nil })
	if err != nil {
		return err
	}
	res.Notices = append(res.Notices, stale...)
	adds, err := e.newComponentNotices(v, installed, x, ent.RulesOn(), func(origin string, args ...string) (string, string, error) {
		label, err := originLabel(x, origin, from, sp.profiles)
		return label, e.installRun(sp.sc, opts.Target, args...), err
	})
	if err != nil {
		return err
	}
	res.Notices = append(res.Notices, adds...)
	// -t naming fewer assistants than the module is installed on here narrows the entry the run
	// writes: the others' files go now, in this plan, as the next sync would remove them, and are
	// asked about as a pick change's are (confirmDeletes).
	planned := modTargets
	if installed != nil && dest != nil && len(given) > 0 && len(r.Targets) == 0 {
		for _, t := range installed.InstalledTargets() {
			if !slices.Contains(modTargets, t) {
				planned = append(slices.Clone(planned), t)
				res.dropped = append(res.dropped, droppedTarget{root: root(sp.sc), module: v.Name, target: t})
			}
		}
	}
	sp.changes = append(sp.changes, plan.ModuleChange{Name: v.Name, Targets: planned, Intents: intents,
		Entry: &ledger.Entry{Source: ent.Source, Ref: ent.Ref, Path: ent.Path, Commit: commit, Digest: v.Meta.Digest, Targets: mr.Targets,
			Components: ent.Components.Clone(), RenderedWith: mr.renderedWith, From: from}})
	// Hooks run only in a copy replaced from the hashed version, after the consent keyed on those
	// bytes (Plan 6a §9.3 step 10, C1). A copy that needs no new version is replaced only to run
	// them: Install keeps it when no hook runs, so what earlier hooks built in it stays. A target
	// the module is not installed on yet has never had them run, so its copy is never kept.
	replace, err := needsCopy(e.files(sp.sc).modules, v.Name, installed, v.Meta.Digest)
	if err != nil {
		return err
	}
	// A refusal reviews exactly the version installed: a source at the commit it resolved; a
	// cached name, or an address, whose catalog may have moved since, as the cache shows it
	// (reviewOf). It consents by the install again, while that installs the same version.
	review := []string{r.Arg}
	if commit != "" {
		review = append(review, "--ref", commit)
	}
	if r.Path != "" {
		review = append(review, "--path", r.Path)
	}
	var shows *store.Version
	if r.market != nil || r.Arg == v.Name && module.ValidateName("module", r.Arg) == nil {
		shows = &v
	}
	again := []string{"install", r.Arg}
	if r.Ref != "" {
		again = append(again, "--ref", r.Ref)
	}
	if r.Path != "" {
		again = append(again, "--path", r.Path)
	}
	for _, t := range mr.Targets {
		again = append(again, "--target", t)
		review = append(review, "--target", t)
	}
	if r.Version != nil && r.Ref == "" {
		// A listed version no ref names: installing the name again reads its current head.
		o, _, err := e.origin(opts.staged, r.Version.Name)
		if err != nil {
			return err
		}
		if currentHead(o) != r.Version.ID {
			again = nil
		}
	}
	at := ""
	if opts.PinConsent {
		if at = v.Name + "=" + v.Meta.Digest; commit != "" {
			at = v.Name + "=" + commit
		}
	}
	sp.copies = append(sp.copies, moduleCopy{name: v.Name, src: v.ContentDir, replace: true, at: at,
		hooks: v.Module.DeclaredHooks, targets: mr.Targets, runHooks: true, forced: !replace && !addsTargets(installed, mr.Targets),
		pending: installed != nil && installed.HooksPending && declaresPostInstall(v.Module.DeclaredHooks), review: review, again: again,
		shows: shows, reviewTargets: mr.Targets})
	if dest != nil {
		sp.write(sel, *dest, section, v.Name, ent)
	}
	return nil
}

// write records ent, the entry of module the install writes into t's file, in that file's form
// (fileEntry), with the picks saved for it in picks when t is a profile.
func (sp *scopePlan) write(sel *config.Selections, t writeTo, picks config.Where, module string, ent manifest.Entry) {
	if t.Profile {
		picked := sel.Entry(picks, module)
		ent.Only, ent.Skip = patternStrings(picked.Only), patternStrings(picked.Skip)
	}
	ent = t.fileEntry(sp.sc, ent)
	for i := range sp.writes {
		if sp.writes[i].t.Name == t.Name {
			// t as it is now: an entry written as new since is not in its entries (planInstallModule),
			// so the trust refresh records it whole.
			sp.writes[i].t = t
			sp.writes[i].entries = append(sp.writes[i].entries, ent)
			return
		}
	}
	sp.writes = append(sp.writes, targetWrite{t: t, entries: []manifest.Entry{ent}})
}

// winnerAbove is the layer above the one named at, in layers, whose entry for module wins: the
// last layer above at that lists it; nil when none does. A target not in layers has none above.
func winnerAbove(layers []Layer, at, module string) *Layer {
	k := slices.IndexFunc(layers, func(l Layer) bool { return l.Name == at })
	if k < 0 {
		return nil
	}
	for i := len(layers) - 1; i > k; i-- {
		if slices.ContainsFunc(layers[i].Entries, func(ent manifest.Entry) bool { return ent.Name == module }) {
			return &layers[i]
		}
	}
	return nil
}

// entryRef is the ref man records for the module named arg, or "".
func entryRef(man *manifest.Manifest, arg string) string {
	if ent := man.Find(arg); ent != nil {
		return ent.Ref
	}
	return ""
}

// listedRef is the ref of the cache head that holds the listed version v, as the pickers
// chose it (Current): the default branch's ("") when it holds v, else the first head that
// does. It is "" when no head holds v (a version fetched without a head, or since moved).
// A version run staged is read as the cache will hold it (Engine.origin); nil is the cache alone.
func (e *Engine) listedRef(run *StagedRun, v VersionRef) (string, error) {
	o, _, err := e.origin(run, v.Name)
	if err != nil {
		return "", err
	}
	ref := ""
	for _, h := range o.Heads {
		if h.Digest != v.ID {
			continue
		}
		if h.Ref == "" {
			return "", nil
		}
		if ref == "" {
			ref = h.Ref
		}
	}
	return ref, nil
}

// installAt checks lola install's --at for v, which the run resolved: the kind of version its
// source takes (atKind), then that it is the one --at names (atHolds), before anything is written.
func (e *Engine) installAt(run *StagedRun, v store.Version, ref, want, fresh string) error {
	o, found, err := e.origin(run, v.Name)
	if err != nil {
		return err
	}
	kind := source.KindArchive
	if found {
		kind = source.Kind(o.Type)
	}
	if err := atKind(v.Name, kind, want); err != nil {
		return err
	}
	got := v.Meta.Digest
	if kind == source.KindGit {
		if got, err = e.pinCommit(run, v, ref); err != nil {
			return err
		}
	}
	return atHolds(v.Name, got, want, fresh)
}

// isFolder reports whether the cache has name from a folder source, which is never pinned, as
// run will leave it (Engine.origin).
func (e *Engine) isFolder(run *StagedRun, name string) bool {
	o, found, err := e.origin(run, name)
	return err == nil && found && o.Type == string(source.KindFolder)
}

// pinCommit is the commit a git version is pinned at (Plan 6a §4.1): the one the fetch or the
// head that produced v resolved, else that of the cache's head for ref that holds v (a version
// listed by the pickers carries no commit). It is "" for other sources, and a git version no
// head for ref records a commit for is refused: its heads predate pins, or another ref's.
func (e *Engine) pinCommit(run *StagedRun, v store.Version, ref string) (string, error) {
	commit, git, err := e.headCommit(run, v, ref)
	if err != nil || !git || commit != "" {
		return commit, err
	}
	return "", errs.WithHints(fmt.Errorf("%s: your cache does not record which commit its version %s came from", v.Name, v.ID),
		"fetch it again, then install it: "+errs.ShellJoin([]string{"lola", "cache", "update", v.Name})+", then "+
			errs.ShellJoin([]string{"lola", "install", v.Name}))
}

// headCommit is pinCommit's commit without the refusal: git reports whether v is a git
// version, and commit is "" for one no head for ref records a commit for. run is read as
// Engine.origin reads it.
func (e *Engine) headCommit(run *StagedRun, v store.Version, ref string) (commit string, git bool, err error) {
	o, found, err := e.origin(run, v.Name)
	if err != nil || !found || o.Type != string(source.KindGit) {
		return "", false, err
	}
	if v.Commit != "" {
		return v.Commit, true, nil
	}
	for _, h := range o.Heads {
		if h.Digest == v.ID && h.Commit != "" && h.Ref == ref {
			commit = h.Commit
		}
	}
	return commit, true, nil
}

// installedVersion is the version a request that adds targets (InstallRequest.Targets)
// installs: for a module the scope's manifest has (prev) and its ledger records (installed),
// the version installed there, so adding an assistant never upgrades the module on the others;
// otherwise v, the version the request resolved to. An installed version the cache no longer
// holds is refused.
func (e *Engine) installedVersion(sc Scope, v store.Version, prev *manifest.Entry, installed *ledger.Entry) (store.Version, error) {
	if prev == nil || installed == nil || installed.Digest == v.Meta.Digest {
		return v, nil
	}
	id := strings.TrimPrefix(installed.Digest, "sha256:")
	if len(id) >= 12 {
		id = id[:12]
	}
	iv, err := e.Store.Load(v.Name, id)
	if err == nil {
		iv.Commit = installed.Commit
		return iv, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return store.Version{}, untrusted.CleanErr(err) // a sync would load it the same way
	}
	return store.Version{}, errs.WithHints(fmt.Errorf("the version of %s installed here (%s) is no longer in the cache", untrusted.Text(v.Name, untrusted.MaxName),
		untrusted.Text(id, untrusted.MaxName)),
		e.SyncCommand(sc, v.Name)+" installs the cached version of "+untrusted.Text(v.Name, untrusted.MaxName)+" here again; run it, then add the assistants")
}

// listedPin is the version ent, the entry the layer labeled from gives the scope, pins: what a
// first install of it installs, whatever head v, the version resolved at its ref, is. It is read
// as sync reads a pin (Store.Pinned): from a snapshot in run, the run's directory, hashed there
// and refused unless both its content and its record match the pin whole, since the pin is a
// pulled file's. A pin the cache does not hold is refused, hinting the sync that fetches it.
func (e *Engine) listedPin(sc Scope, v store.Version, ent *manifest.Entry, from, run string) (store.Version, error) {
	pv, found, err := e.Store.Pinned(v.Name, ent.Digest, run)
	if err != nil {
		return store.Version{}, err
	}
	if !found {
		return store.Version{}, errs.WithHints(fmt.Errorf("the version of %s %s pins (%s) is not in your cache", untrusted.Text(v.Name, untrusted.MaxName), from,
			untrusted.Text(versionID(ent.Digest), untrusted.MaxName)), e.SyncCommand(sc, v.Name)+" fetches it and installs it here")
	}
	pv.Commit = ent.Commit
	return pv, nil
}

// addedTargets are a module's targets when a request adds some (InstallRequest.Targets): the
// targets its manifest entry already installs on (none when it has no entry), then the added
// ones. The entry records them unless they are the manifest's targets (scopeTargets, for a
// manifest that has none yet).
func addedTargets(man *manifest.Manifest, prev *manifest.Entry, scopeTargets, add []string) ([]string, []string) {
	var ts []string
	if prev != nil {
		ts = slices.Clone(prev.Targets)
		if len(ts) == 0 {
			ts = slices.Clone(man.Targets)
		}
	}
	for _, id := range add {
		if !slices.Contains(ts, id) {
			ts = append(ts, id)
		}
	}
	defaults := man.Targets
	if len(defaults) == 0 {
		defaults = scopeTargets
	}
	if sameSet(ts, defaults) {
		return ts, nil
	}
	return ts, ts
}

// sameSet reports whether a and b hold the same strings.
func sameSet(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(slices.Compact(x), slices.Compact(y))
}

// makePluginData creates ${PLUGIN_DATA} for modules whose rendered artifacts reference it. A
// JSON value holds the directory JSON-escaped (a Windows path's backslashes are doubled).
func (e *Engine) makePluginData(sc Scope, changes []plan.ModuleChange) error {
	for _, c := range changes {
		dir := filepath.Join(e.files(sc).pluginData, c.Name)
		inJSON := bytes.Trim(plan.StringMember(dir), `"`)
		if slices.ContainsFunc(c.Intents, func(in plan.Intent) bool {
			return bytes.Contains(in.Data, []byte(dir)) || bytes.Contains(in.Value, inJSON) || bytes.Contains(in.Member, inJSON)
		}) {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return err
			}
		}
	}
	return nil
}

// allTargets is every target a module is installed on or meant for, sorted.
func allTargets(entry *ledger.Entry, ment *manifest.Entry, man *manifest.Manifest) []string {
	set := map[string]bool{}
	if entry != nil {
		for _, t := range entry.InstalledTargets() {
			set[t] = true
		}
	}
	want := man.Targets
	if ment != nil && len(ment.Targets) > 0 {
		want = ment.Targets
	}
	if entry != nil || ment != nil {
		for _, t := range want {
			set[t] = true
		}
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}
func (e *Engine) where(sc Scope) string {
	if sc.Kind == target.User {
		return "at user scope"
	}
	return "in this project"
}

// notInstalled is ADR #244's ambiguity error: it says where the module does exist.
// A single installed module that differs from name only in case is offered as the likely
// intent, with the user's scope and display flags kept.
func (e *Engine) notInstalled(sc Scope, name string, led *ledger.Ledger, display []string) error {
	var hints []string
	var like []string
	for installed := range led.Modules {
		if strings.EqualFold(installed, name) {
			like = append(like, installed)
		}
	}
	if len(like) == 1 {
		hints = append(hints, "did you mean "+untrusted.Text(like[0], untrusted.MaxName)+"? "+e.Command(sc, "uninstall", append([]string{like[0]}, display...)...))
	}
	if found, err := e.Store.Has(name); err == nil && found {
		hints = append(hints, "still in your cache: "+errs.ShellJoin([]string{"lola", "cache", "rm", name}))
	}
	hints = append(hints, "installed here: "+e.Command(sc, "list"))
	return errs.WithHints(fmt.Errorf("lola uninstall: %q is not installed %s", name, e.where(sc)), hints...)
}

// Uninstall removes modules' artifacts (on opts.Targets, or everywhere) and drops them from
// the manifest once nothing remains.
func (e *Engine) Uninstall(ctx context.Context, sc Scope, names []string, opts Options) (_ *Result, err error) {
	defer func() { err = e.CoachUnresolved(err, []Scope{sc}, opts.DryRun, opts.Display) }()
	if err := e.CheckTargets(opts.Targets); err != nil {
		return nil, err
	}
	// The profiles lock comes first (Plan 6b §10): the run reads the remote profiles once.
	unlockProfiles, err := e.readProfiles(opts.DryRun)
	if err != nil {
		return nil, err
	}
	defer unlockProfiles()
	// The selections lock comes before the scope's (config.Lock): the run clears the picks of
	// what it removes from a project's section.
	if sc.Kind == target.Project && !opts.DryRun {
		unlockSelections, err := config.Lock(e.Paths.SelectionsLock)
		if err != nil {
			return nil, err
		}
		defer unlockSelections()
	}
	man, led, unlock, err := e.load(sc, opts.DryRun)
	if err != nil {
		return nil, err
	}
	defer unlock()
	rec, err := e.trustRecord(sc)
	if err != nil {
		return nil, err
	}
	res := newResult(opts.DryRun)
	res.PicksCleared = []PickCleared{}
	// The files an uninstall removes are the ledger's, which is this checkout's own only while
	// git does not track it.
	if err := e.refuseTrackedLedger(ctx, sc, res); err != nil {
		return nil, err
	}
	layers, _, err := e.layers(sc, man, led)
	if err != nil {
		return nil, err
	}
	t, infos, err := e.uninstallTarget(sc, layers, opts.Target)
	if err != nil {
		return nil, err
	}
	want, _ := desired(layers)
	var changes []plan.ModuleChange
	removing := map[string]*writeTo{} // module → the layer its entry is removed from
	narrowed := map[string][]string{} // module → the targets its entry in removing keeps, when the run installs nothing of that edit
	saved := map[string]bool{}        // the files of profiles that are not on whose edit was said to be saved only
	noteSaved := func(from *writeTo) {
		if !from.on && !saved[from.Path] {
			saved[from.Path] = true
			text := "saved to " + from.label + "; it is not on " + scopeWhere(sc) + " → " + e.switchRun(sc, *from)
			if opts.DryRun {
				text = "would be " + text
			}
			res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: text})
		}
	}
	// covered refuses a rewrite of the targets of from's entry for name the trust record does not
	// cover: removing a module only reduces what is installed, removing it from some targets
	// rewrites an entry someone else may have changed (Plan 6a §10.4; a project profile's as
	// project.yml's).
	covered := func(name string, from *writeTo) error {
		ent := from.layerEntry(name)
		switch {
		case from.covered(rec, *ent):
			return nil
		case from.Key != "":
			return e.notTrustedIn(sc, rec, *from, ent)
		}
		return e.notTrusted(sc, rec, ent, true, false)
	}
	// narrow keeps, of from's entry for name, its own targets (its targets:, else its file's) but ts.
	narrow := func(name string, from *writeTo, ts []string) {
		own := from.layerEntry(name).Targets
		if len(own) == 0 {
			own = fileTargets(*from, man)
		}
		narrowed[name] = slices.DeleteFunc(slices.Clone(own), func(t string) bool { return slices.Contains(ts, t) })
	}
	for _, name := range names {
		entry, w := led.Modules[name], find(want, name)
		// --profile naming a profile that is not on: its file is edited, and nothing it lists is installed.
		if w == nil && opts.Target.Profile != "" && t.layerEntry(name) != nil {
			removing[name] = &t
		}
		if entry == nil && w == nil {
			if removing[name] == nil {
				return nil, e.notInstalled(sc, name, led, opts.Display)
			}
			if len(opts.Targets) > 0 {
				from := removing[name]
				own := from.layerEntry(name).Targets
				if len(own) == 0 {
					own = fileTargets(*from, man)
				}
				if !sharesAny(opts.Targets, own) {
					return nil, e.notOnTargets(sc, name, from.label, notOn{own: own, editOnly: true}, opts)
				}
				if err := covered(name, from); err != nil {
					return nil, err
				}
				narrow(name, from, opts.Targets)
			}
			noteSaved(removing[name])
			continue
		}
		var ment *manifest.Entry
		if w != nil {
			ment = &w.Entry
		}
		on := allTargets(entry, ment, man)
		var installed []string // what lock.json records it installed on
		if entry != nil {
			installed = entry.InstalledTargets()
		}
		ts := opts.Targets
		if len(ts) == 0 {
			ts = on
		}
		partial := slices.ContainsFunc(on, func(t string) bool { return !slices.Contains(ts, t) })
		if len(opts.Targets) > 0 {
			// -t is judged by what the run changes: the targets the module is installed on, and those
			// of the entry --profile names when that is not the one installed, which alone the run
			// edits while a layer on wants the module.
			why := notOn{installed: installed}
			if w != nil {
				why.listed = allTargets(nil, ment, man)
			}
			if opts.Target.Profile != "" && t.layerEntry(name) != nil && (w == nil || t.Name != w.From || !t.on) {
				if why.own = t.layerEntry(name).Targets; len(why.own) == 0 {
					why.own = fileTargets(t, man)
				}
				why.editOnly = w != nil
			}
			if why.editOnly && !sharesAny(opts.Targets, why.own) || !why.editOnly && !sharesAny(opts.Targets, installed) && !sharesAny(opts.Targets, why.own) {
				return nil, e.notOnTargets(sc, name, t.label, why, opts)
			}
		}
		if w != nil {
			from, err := e.removeFrom(sc, layers, infos, t, w, partial, opts)
			if err != nil {
				return nil, err
			}
			removing[name] = from
			// Another layer that still lists it keeps it installed (spec 2026-10-29 §6.1); -t
			// keeps the entry, so it still wants it there.
			if rest := stillWanted(layers, from.Name, name); rest != nil && !partial {
				noteSaved(from)
				res.Notices = append(res.Notices, e.staysNotices(sc, layers, infos, rest, entry)...)
				continue
			}
		}
		from := removing[name]
		if from != nil && partial {
			if err := covered(name, from); err != nil {
				return nil, err
			}
			// An entry that is not the winner's (a layer below it, or a profile that is not on)
			// loses ts from its own targets; what is installed follows the winner (spec 2026-10-29 §3.5).
			if w == nil || from.Name != w.From || !from.on {
				narrow(name, from, ts)
				noteSaved(from)
				if w != nil {
					if from.on {
						res.Notices = append(res.Notices, saveOnlyNotice(name, layerLabel(infos, w.From), from.label))
					}
					continue
				}
			}
		}
		if from != nil {
			noteSaved(from)
		}
		// What is uninstalled is what lock.json records installed, of ts: nothing for a module it
		// does not record, whose entry alone is edited, nor when -t names only targets of the entry
		// of a profile that is not on.
		if entry == nil {
			continue
		}
		if len(opts.Targets) > 0 {
			if ts = slices.DeleteFunc(slices.Clone(ts), func(t string) bool { return !slices.Contains(installed, t) }); len(ts) == 0 {
				continue
			}
		}
		changes = append(changes, plan.ModuleChange{Name: name, Targets: ts})
		res.Modules = append(res.Modules, ModuleResult{Name: name, Targets: slices.DeleteFunc(slices.Clone(ts), func(t string) bool { return !slices.Contains(installed, t) }),
			MCPServers: []MCPRun{}, Hooks: []HookRun{}, RuntimeHooks: []RuntimeHook{}, Skipped: []string{}, NotInOnly: []string{}})
	}
	pr, err := e.prepare(sc, led, changes, nil, nil, opts, res)
	if err != nil {
		return nil, err
	}
	var change manifest.Change
	var base *writeTo // project.yml, when change edits it
	files := map[string]*fileEdit{}
	targets := map[string]*writeTo{}
	var order []string
	for _, name := range names {
		from := removing[name]
		if from == nil {
			continue
		}
		var c manifest.Change
		left := pr.plan.Ledger.Modules[name]
		keep, isNarrowed := narrowed[name]
		switch {
		case isNarrowed && len(keep) > 0:
			kept := from.layerEntry(name)
			kept.Targets = ownTargets(keep, *from, man)
			c.Set = []manifest.Entry{from.fileEntry(sc, *kept)}
		case isNarrowed || left == nil || !slices.ContainsFunc(changes, func(m plan.ModuleChange) bool { return m.Name == name }):
			c.Remove = []string{name}
		default:
			if kept := from.layerEntry(name); kept != nil {
				kept.Targets = ownTargets(allTargets(left, nil, &manifest.Manifest{}), *from, man)
				c.Set = []manifest.Entry{from.fileEntry(sc, *kept)}
			}
		}
		if !from.Profile {
			base = from
			change.Remove, change.Set = append(change.Remove, c.Remove...), append(change.Set, c.Set...)
			continue
		}
		f := files[from.Path]
		if f == nil {
			f = &fileEdit{layer: from.Layer}
			files[from.Path], targets[from.Path], order = f, from, append(order, from.Path)
		}
		f.change.Remove, f.change.Set = append(f.change.Remove, c.Remove...), append(f.change.Set, c.Set...)
	}
	if base != nil {
		res.Edits = append(res.Edits, editsOf(ctx, sc, *base, change.Set, change.Remove, nil)...)
	}
	edits := make([]fileEdit, 0, len(order))
	for _, path := range order {
		edits = append(edits, *files[path])
		res.Edits = append(res.Edits, editsOf(ctx, sc, *targets[path], files[path].change.Set, files[path].change.Remove, nil)...)
		res.Notices = append(res.Notices, e.alsoOn(sc, *targets[path], append([]Scope{sc}, opts.Then...))...)
	}
	clearing, err := e.picksToClear(sc, names, changes, pr.plan.Ledger, opts, res)
	if err != nil {
		return nil, err
	}
	if opts.DryRun {
		return res, nil
	}
	removed := "lola removed what you uninstalled from the project's files, lock.json and " + e.manifestName(sc)
	if err := e.saveManifest(sc, man, change, nil, edits, rec, removed, syncAgain(e.SyncCommand(sc)),
		func(mw []plan.ManifestWrite) error { return e.commit(ctx, pr, opts, res, mw, nil) }); err != nil {
		return nil, err
	}
	if len(clearing) > 0 {
		if err := config.Save(e.Paths.Selections, clearing); err != nil {
			return nil, errs.WithHints(fmt.Errorf("lola removed what you uninstalled, but could not clear its picks from selections.yaml: %w", untrusted.CleanErr(err)),
				"a later install of the module would still apply them; delete its entry from "+untrusted.Text(e.Paths.Selections, untrusted.MaxName))
		}
	}
	for i := range res.Modules {
		name := res.Modules[i].Name
		if found, err := e.Store.Has(name); err == nil && found {
			res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: "still in your cache: " + errs.ShellJoin([]string{"lola", "cache", "rm", name})})
		}
		if dir := filepath.Join(e.files(sc).pluginData, name); dirExists(dir) {
			res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: name + ": plugin data kept at " + dir})
		}
	}
	return res, nil
}

// picksToClear is the removal of the picks a project's uninstall leaves behind (Pick-P2-f): of
// each module of names the run removes whole (it is in changes and the plan's ledger no longer
// holds it), the entry in the project's selections.yaml section, which the caller saves. It
// reports each in res.PicksCleared and a notice naming the command that restores it. A module
// -t leaves on another target, or another layer still lists, keeps its picks; the global
// section is never touched, and user scope has no section.
func (e *Engine) picksToClear(sc Scope, names []string, changes []plan.ModuleChange, after *ledger.Ledger, opts Options, res *Result) ([]config.Change, error) {
	if sc.Kind != target.Project {
		return nil, nil
	}
	w, err := e.scopeSection(sc)
	if err != nil {
		return nil, err
	}
	sel, err := e.selections()
	if err != nil {
		return nil, err
	}
	var clearing []config.Change
	for _, name := range names {
		ent := sel.Entry(w, name)
		if ent.Empty() || after.Modules[name] != nil || !slices.ContainsFunc(changes, func(m plan.ModuleChange) bool { return m.Name == name }) {
			continue
		}
		c := PickCleared{Module: name, Section: untrusted.Text(sel.Origin(w, name), untrusted.MaxName),
			Skip: patternStrings(ent.Skip), Include: patternStrings(ent.Include), Only: patternStrings(ent.Only)}
		args := []string{name}
		for _, f := range []struct {
			flag string
			sels []string
		}{{"--skip", c.Skip}, {"--unskip", c.Include}, {"--only", c.Only}} {
			for _, s := range f.sels {
				args = append(args, f.flag, s)
			}
		}
		if len(c.Only) > 0 {
			args = append(args, "--exact")
		}
		if len(opts.Targets) > 0 {
			args = append(args, "-t", strings.Join(opts.Targets, ","))
		}
		c.Restore = e.Command(sc, "install", append(args, opts.Display...)...)
		res.PicksCleared = append(res.PicksCleared, c)
		verb := "cleared"
		if opts.DryRun {
			verb = "would be cleared"
		}
		selectors := slices.Concat(c.Skip, c.Include, c.Only)
		res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: name + ": picks " + verb + ": " +
			untrusted.Text(strings.Join(selectors, ", "), untrusted.MaxName*4) + " (restore: " + c.Restore + ")"})
		clearing = append(clearing, config.Change{Section: w, Module: name})
	}
	if len(clearing) > 0 {
		res.Notices = append(res.Notices, e.keyNotices(sc)...)
	}
	return clearing, nil
}

// notOn is what a refusal of -t says of a module: the targets lock.json records it installed
// on, those the winning entry lists, and own, those of its entry in the layer --profile names
// when that is not the one installed; editOnly says the run would only edit that entry.
type notOn struct {
	installed, listed, own []string
	editOnly               bool
}

// notOnTargets refuses an uninstall of name whose -t shares nothing with what it changes (why),
// saying where it is installed and listed, never that it is installed where it is only listed.
// label is the layer of why.own. The hint is the run again for name alone, since the run's other
// modules may not be on those targets, with -t naming what it changes (its installed and own
// targets), else without -t, which removes the entry: opts.Retarget's, else lola uninstall with
// --profile and the output flags kept.
func (e *Engine) notOnTargets(sc Scope, name, label string, why notOn, opts Options) error {
	module, asked := shown(name), strings.Join(opts.Targets, ", ")
	list := func(ts []string) string { return strings.Join(shownAll(ts), ", ") }
	var msg string
	switch {
	case why.editOnly:
		msg = fmt.Sprintf("%s lists %s for %s, not for %s", label, module, list(why.own), asked)
	default:
		msg = module + " is not installed on " + asked
		if why.own != nil {
			msg += ", nor does " + label + " list it for them"
		}
		switch {
		case len(why.installed) == 0 && len(why.listed) > 0:
			msg += "; it is listed for " + list(why.listed) + " but not installed there"
		case len(why.installed) == 0:
			msg += "; it is not installed on any target"
		case sharesAny(opts.Targets, why.listed):
			msg += "; it is listed for " + list(why.listed) + " but installed only on " + list(why.installed)
		default:
			msg += "; it is installed on " + list(why.installed)
		}
		if why.own != nil {
			msg += "; " + label + " lists it for " + list(why.own)
		}
	}
	ts := slices.Clone(why.own)
	if !why.editOnly {
		ts = append(ts, why.installed...)
	}
	slices.Sort(ts)
	ts = slices.Compact(ts)
	var run string
	if opts.Retarget != nil {
		run = opts.Retarget(name, ts)
	} else {
		args := []string{name}
		if len(ts) > 0 {
			args = append(args, "-t", strings.Join(ts, ","))
		}
		if opts.Target.Profile != "" {
			args = append(args, "--profile", opts.Target.Profile)
		}
		run = e.Command(sc, "uninstall", append(args, opts.Display...)...)
	}
	if len(ts) == 0 {
		return errs.WithHints(errors.New(msg), "remove its entry instead: "+run)
	}
	return errs.WithHints(errors.New(msg), "uninstall it there: "+run)
}

// sharesAny reports whether a and b share a string.
func sharesAny(a, b []string) bool {
	return slices.ContainsFunc(a, func(s string) bool { return slices.Contains(b, s) })
}

// ownTargets is the targets: an entry of t's file that keeps ts writes: none when ts are the
// file's own (fileTargets), so the entry follows them, else ts.
func ownTargets(ts []string, t writeTo, man *manifest.Manifest) []string {
	if sameSet(ts, fileTargets(t, man)) {
		return nil
	}
	return ts
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// Status reports each installed module × target: missing (an owned destination or the
// module copy is gone), modified (a destination differs from what lola wrote), stale (the
// manifest pins another version than the one installed), skipped (the target rendered nothing
// of the module), unreadable (the version installed is cached but does not load), or ok; and
// each entry a layer wants that lock.json does not record, not_installed, per effective target.
// Each row names the layer its module comes from (From): the layer whose entry wins now, else
// the one lock.json recorded. A module copy's content is not compared: hooks build in it. It
// reads no network, never the trust record, and changes nothing (Plan 6a §10.5). It refuses a
// project whose lock.json git tracks, since a committed one could name an active profile (Plan
// 6b §5, §19 S5); the notices are the active profile when its file no longer exists, git
// missing, and how many modules are not installed with the sync that installs them, keeping
// display, the run's output flags.
func (e *Engine) Status(ctx context.Context, sc Scope, display []string) (_ []StatusRow, _ []Notice, err error) {
	defer func() { err = e.CoachUnresolved(err, []Scope{sc}, false, display) }()
	unlock, err := e.readProfiles(true)
	if err != nil {
		return nil, nil, err
	}
	defer unlock()
	man, led, _, err := e.load(sc, true)
	if err != nil {
		return nil, nil, err
	}
	checked := newResult(true)
	if err := e.refuseTrackedLedger(ctx, sc, checked); err != nil {
		return nil, nil, err
	}
	layers, notices, err := e.layers(sc, man, led)
	if err != nil {
		return nil, nil, err
	}
	notices = append(notices, checked.Notices...)
	if _, err := e.scopeSection(sc); err != nil {
		return nil, nil, err
	}
	notices = append(notices, e.keyNotices(sc)...)
	if what, _, _ := notInstalled(layers, led); what != "" {
		notices = append(notices, Notice{Level: LevelInfo, Text: what + " → " + e.SyncCommand(sc, display...)})
	}
	sel, err := e.selections()
	if err != nil {
		return nil, nil, err
	}
	if sel, err = withPicks(sel, layers); err != nil {
		return nil, nil, err
	}
	rows := []StatusRow{}
	profiles := e.visibleOnce(sc)
	names := sortedNames(led)
	want, _ := desired(layers)
	for _, w := range want {
		if led.Modules[w.Name] == nil {
			names = append(names, w.Name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		entry, w := led.Modules[name], find(want, name)
		var ment *manifest.Entry
		from := e.manifestName(sc)
		if entry != nil && entry.From != "" {
			from = entry.From
		}
		if w != nil {
			ment, from = &w.Entry, w.From
		}
		x, err := e.exclusions(sel, sc, from, name)
		if err != nil {
			return nil, nil, err
		}
		only, origin := x.Only()
		shown := []string{}
		for _, o := range only {
			shown = append(shown, untrusted.Text(o.String(), untrusted.MaxName))
		}
		onlyFrom, err := originLabel(x, origin, from, profiles)
		if err != nil {
			return nil, nil, err
		}
		if entry == nil {
			rows = append(rows, e.notInstalledRows(man, *w, shown, onlyFrom)...)
			continue
		}
		pin, ref := "", entry.Ref
		if ment != nil {
			pin, ref = ment.Digest, ment.Ref
		}
		update, uerr := e.updateAvailable(name, ref, cmp.Or(pin, entry.Digest), entry.Source)
		base := StatusRow{Name: name, From: from, Rules: ment.RulesOn(), Source: entry.Source, Ref: entry.Ref, Digest: entry.Digest, Pin: pin,
			UpdateAvailable: update, SourceGone: sourceGone(entry.Source, sourceRoot(sc)), Skipped: []string{}, Only: shown, OnlyFrom: onlyFrom}
		if ment != nil {
			base.Market = shownMarket(ment.Market)
		}
		var v store.Version
		err = fs.ErrNotExist
		if id := versionID(entry.Digest); id != "" {
			v, err = e.Store.Load(name, id)
		}
		ok := err == nil
		if uerr != nil || err != nil && !errors.Is(err, fs.ErrNotExist) {
			// The cache's record of the module or the version installed cannot be loaded
			// (corrupt, unknown key): report it and keep going.
			for _, id := range allTargets(entry, ment, man) {
				r := base
				r.Target, r.Status = id, "unreadable"
				rows = append(rows, r)
			}
			continue
		}
		stale := pin != "" && pin != entry.Digest
		skipped := []string{}
		if ok {
			skipped = resolveComponents(v.Module, x, true).skipped
		}
		copyMissing, err := needsCopy(e.files(sc).modules, name, entry, entry.Digest)
		if err != nil {
			return nil, nil, err
		}
		for _, id := range allTargets(entry, ment, man) {
			if ok && !module.Supports(v.Module.Targets, id) {
				continue
			}
			t, err := e.target(id)
			if err != nil {
				return nil, nil, err
			}
			root := t.Root(sc.Kind, sc.ProjectDir, e.Paths)
			status, n := "ok", 0
			for _, a := range entry.Artifacts {
				if a.Target != id {
					continue
				}
				n++
				h, err := plan.Observe(root, a)
				if err != nil {
					return nil, nil, err
				}
				if h == "" {
					status = "missing"
					break
				}
				if h != a.SHA256 {
					status = "modified"
				}
			}
			if n == 0 {
				status = "skipped"
			}
			if copyMissing {
				status = "missing"
			}
			if status == "ok" && stale {
				status = "stale"
			}
			r := base
			r.Target, r.Status, r.Skipped = id, status, skipped
			rows = append(rows, r)
		}
	}
	for i := range rows {
		rows[i].Scope = sc.Kind.String()
	}
	return rows, notices, nil
}

// sourceGone reports whether recorded, a source as lock.json records it, is a local path that
// does not exist under root. A URL or git's scp form is never looked up, and a path that cannot
// be checked (permission denied, a file in the way) is not reported as gone.
func sourceGone(recorded, root string) bool {
	if strings.Contains(recorded, "://") || source.IsSCP(recorded) {
		return false
	}
	if !filepath.IsAbs(recorded) {
		recorded = filepath.Join(root, recorded)
	}
	_, err := os.Stat(recorded)
	return errors.Is(err, fs.ErrNotExist)
}

// notInstalledRows are the list rows of w, an entry a layer wants that lock.json does not
// record: one per effective target, with no digest installed.
func (e *Engine) notInstalledRows(man *manifest.Manifest, w Wanted, only []string, onlyFrom string) []StatusRow {
	ment := &w.Entry
	ts := ment.Targets
	if len(ts) == 0 {
		ts = man.Targets
	}
	status, update := "not_installed", false
	if ment.Digest != "" {
		var err error
		if update, err = e.updateAvailable(ment.Name, ment.Ref, ment.Digest, ment.Source); err != nil {
			status = "unreadable" // the cache's record of the module cannot be loaded
		}
	}
	var rows []StatusRow
	for _, t := range ts {
		rows = append(rows, StatusRow{Name: ment.Name, From: w.From, Target: t, Status: status, Rules: ment.RulesOn(), Source: ment.Source, Ref: ment.Ref,
			Pin: ment.Digest, UpdateAvailable: update, Skipped: []string{}, Only: only, OnlyFrom: onlyFrom, Market: shownMarket(ment.Market)})
	}
	return rows
}

// InstallCoaching is what lola install without arguments says about sc (Plan 6a §10.2, Plan 6b
// §7): when its layers want modules lock.json does not record, the notice a terminal shows
// before the pickers and the hint the missing-argument error adds off one, both naming the
// layers that list them and the sync that installs them; "" when there are none. It reads the
// manifest, the ledger and the scope's profiles (default where read, then the active profile)
// only, and refuses a project whose lock.json git tracks before reading the profile it names, as
// every command that reads them does (Plan 6b §5, §19 S5).
func (e *Engine) InstallCoaching(ctx context.Context, sc Scope) (notice, hint string, err error) {
	unlock, err := e.readProfiles(true)
	if err != nil {
		return "", "", err
	}
	defer unlock()
	man, led, _, err := e.load(sc, true)
	if err != nil {
		return "", "", err
	}
	if err := e.refuseTrackedLedger(ctx, sc, newResult(true)); err != nil {
		return "", "", err
	}
	layers, _, err := e.layers(sc, man, led)
	if err != nil {
		return "", "", err
	}
	what, base, lists := notInstalled(layers, led)
	if what == "" {
		return "", "", nil
	}
	return what + " → " + e.SyncCommand(sc), "install what " + base + " " + lists + ": " + e.SyncCommand(sc), nil
}

// notInstalled says how many modules layers want that led does not record ("1 module in
// project.yml is not installed"; "" for none), with the layers that list them, joined, and the
// verb they take ("lists" or "list").
func notInstalled(layers []Layer, led *ledger.Ledger) (what, base, lists string) {
	want, _ := desired(layers)
	n, from := 0, map[string]bool{}
	for _, w := range want {
		if led.Modules[w.Name] == nil {
			n, from[w.From] = n+1, true
		}
	}
	if n == 0 {
		return "", "", ""
	}
	var files []string
	for _, l := range layers {
		if from[l.Name] {
			files = append(files, l.Name)
		}
	}
	base, lists = andJoin(files), "lists"
	if len(files) > 1 {
		lists = "list"
	}
	what = fmt.Sprintf("%d modules in %s are not installed", n, base)
	if n == 1 {
		what = "1 module in " + base + " is not installed"
	}
	return what, base, lists
}

// versionID is the cache version id of a digest: its first 12 hex digits ("" when it has none).
func versionID(digest string) string {
	id := strings.TrimPrefix(digest, "sha256:")
	if len(id) < 12 {
		return ""
	}
	return id[:12]
}

// updateAvailable reports whether the cache head for ref of name holds another version than
// digest, in the origin recorded for src as the entry writes it, else the newest. It reads the
// cache index only, and a folder's head never counts: a folder is read again by every sync.
func (e *Engine) updateAvailable(name, ref, digest, src string) (bool, error) {
	origins, err := e.Store.Origins(name)
	if err != nil || len(origins) == 0 {
		return false, err
	}
	o := origins[0]
	if i := slices.IndexFunc(origins, func(c store.Origin) bool { return c.Source == src }); i >= 0 {
		o = origins[i]
	}
	if o.Type == string(source.KindFolder) {
		return false, nil
	}
	for _, h := range o.Heads {
		if h.Ref == ref {
			return h.Digest != versionID(digest), nil
		}
	}
	return false, nil
}
