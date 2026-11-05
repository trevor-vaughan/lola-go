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
// each scope of spec from where they are (Plan 6a §11.3), one command per scope. A refusal for
// want of trust, or for conflicts, keeps only its own hints, the way forward: retrying would
// refuse again.
func (e *Engine) withRetry(spec InstallSpec, err error, fetched []string) error {
	_, targets := errors.AsType[*untrustedTargetsError](err)
	_, conflicts := errors.AsType[*plan.ConflictError](err)
	_, template := errors.AsType[*TemplateError](err) // the same install refuses the same way
	_, hook := errors.AsType[*hookConsentError](err)
	_, script := errors.AsType[*scriptError](err)
	_, scriptConsent := errors.AsType[*scriptConsentError](err)
	if len(fetched) == 0 || targets || conflicts || template || hook || script || scriptConsent || errors.Is(err, ErrNotTrusted) {
		return err
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
		cmds[i] = e.Command(si.Scope, "install", args...)
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
	Options     Options
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
	entries          []manifest.Entry
	replace          []plan.Replaceable
}

// resolvedRequest is an install argument resolved at a scope.
type resolvedRequest struct {
	req InstallRequest
	ent manifest.Entry
	v   store.Version
}

// InstallAll installs modules at every scope of spec (Plan 5c §7, §14); a destination listed
// twice is refused. Order: take the selections lock when there are edits (always before any
// scope lock), lock every scope in lockOrder, then resolve the modules per scope; apply the
// edits to selections.yaml in memory; plan through the resolver; run every refusal (--strict,
// conflicts) and consent prompt for every scope; write selections.yaml; then apply each
// scope. A refusal or a cancelled prompt writes nothing; a failure while applying leaves
// selections.yaml written, which the next render converges on. A dry run writes neither.
func (e *Engine) InstallAll(ctx context.Context, spec InstallSpec) (_ *InstallResult, err error) {
	opts := spec.Options
	var scopes []Scope
	for _, si := range spec.Scopes {
		scopes = append(scopes, si.Scope)
	}
	defer func() { err = nameTaken(e.CoachUnresolved(err, scopes, opts.DryRun, opts.Display), opts.Display) }()
	swept := e.sweepRuns()
	run, done, err := e.runDir()
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, done()) }()
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
	// and uninstalling one would delete the other's. render and uninstall still accept one an
	// older lola installed.
	if home := (Scope{Kind: target.Project, ProjectDir: e.Paths.Home}); seen[home.Key()] {
		err := fmt.Errorf("project scope at %s is your home directory, where the user scope installs", untrusted.Text(e.Paths.Home, untrusted.MaxName))
		if spec.UserRun == "" {
			return nil, err
		}
		return nil, errs.WithHints(err, spec.UserRun)
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
	res := newResult(opts.DryRun)
	res.Trusted = []Consent{} // a command that takes --trust says what it covered, [] for nothing
	res.Notices = append(res.Notices, swept...)
	out := &InstallResult{Result: res, Scopes: []ScopeResult{}}
	probes := e.newProbeRun(ctx)
	var fetched []string
	have := map[int]store.Version{} // a source fetched for one scope is not fetched again for the next
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
			given: si.Targets, replace: si.Replace, unresolved: holdsAddress(man)}
		if plans[i].templates, err = e.newTemplateRun(si.Scope, led.Profiles); err != nil {
			return nil, err
		}
		if len(si.Targets) == 0 && rec != nil && len(man.Targets) > 0 && !slices.Equal(rec.View.Targets, man.Targets) {
			plans[i].untrustedTargets = e.untrustedTargets(si.Scope, rec, spec.Modules, plans[i].unresolved)
		}
	}
	// A name installs the entry as project.yml has it: refused for an entry someone else
	// changed, at every scope before any request is resolved, so nothing is fetched (Plan 6a
	// §10.1).
	for k, sp := range plans {
		for _, r := range spec.Modules {
			if ent := sp.man.Find(r.Arg); ent != nil && !sp.covers(*ent) {
				return nil, e.notTrusted(spec.Scopes[k].Scope, sp.trust, ent, false, sp.unresolved)
			}
			if sp.untrustedTargets != nil && len(r.Targets) == 0 && !e.hasOwnTargets(sp, spec.Scopes[k].Scope, spec.Cwd, r) {
				return nil, sp.untrustedTargets
			}
		}
	}
	for k, si := range spec.Scopes {
		sp := plans[k]
		if len(sp.targets) == 0 {
			sp.targets = sp.man.Targets
		}
		if len(sp.targets) == 0 {
			return nil, e.noTargets()
		}
		for _, id := range sp.targets {
			if _, err := e.target(id); err != nil {
				return nil, err
			}
		}
		for _, r := range spec.Modules {
			for _, id := range r.Targets {
				if _, err := e.target(id); err != nil {
					return nil, err
				}
			}
		}
		for i, r := range spec.Modules {
			ref := entryRef(sp.man, r.Arg)
			if r.Ref != "" {
				ref = r.Ref
			}
			// A scope with no entry installs the listed version and records the ref of the head
			// that holds it: its pin must be reachable from the ref its entry records. An entry
			// without a ref tracks the default branch and keeps doing so.
			if ref == "" && r.Version != nil && sp.man.Find(r.Arg) == nil && r.market == nil {
				if r.Ref, err = e.listedRef(*r.Version); err != nil {
					return nil, e.withRetry(spec, err, fetched)
				}
				ref = r.Ref
			}
			pre := have[i]
			if haveRef[i] != ref {
				pre = store.Version{}
			}
			if pre.Name == "" && r.Version != nil {
				if pre, err = e.pinnedVersion(r); err != nil {
					return nil, e.withRetry(spec, err, fetched)
				}
				// The pickers list the head of the entry's ref; any other version is not what was confirmed.
				if r.Ref == "" && sp.man.Find(r.Arg) != nil {
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
			ent, v, didFetch, err := e.resolve(ctx, si.Scope, spec.Cwd, r, pre, entryRef(sp.man, r.Arg), run, spec.Options.Display)
			if err != nil {
				return nil, e.withRetry(spec, err, fetched)
			}
			if didFetch {
				again := v.Name
				if r.market != nil {
					again = r.Arg // its name would install it without its marketplace
				}
				fetched, have[i], haveRef[i] = append(fetched, again), v, ref
			}
			// Installing a version is a use of it, however the run found it; a dry run, and so lola
			// info's plan, is none (Plan 9 §6.3). Recording it is best effort: a cache whose
			// index.yml cannot be written still installs.
			if !opts.DryRun {
				_ = e.Store.Used(v.Name, v.ID)
			}
			sp.resolved = append(sp.resolved, resolvedRequest{req: r, ent: ent, v: v})
			if r.market != nil {
				if k == 0 {
					res.Notices = append(res.Notices, entryNotices(r.market, r.Ref)...)
				}
				res.Notices = append(res.Notices, e.dependencyNotices(si.Scope, r.market, opts.DryRun)...)
			}
		}
		out.Scopes = append(out.Scopes, ScopeResult{Scope: si.Scope.Kind.String(), Root: root(si.Scope), ScopeSource: si.Source})
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
			if err := e.planInstallModule(sp, rr, sel, opts, probes, res); err != nil {
				return nil, e.withRetry(spec, err, fetched)
			}
		}
		res.Notices = append(res.Notices, overridesProfiles(sp.layers, sp.entries)...)
	}
	for i := range res.Modules {
		if !opts.DryRun && slices.ContainsFunc(saves, func(c config.Change) bool { return c.Module == res.Modules[i].Name }) {
			res.Modules[i].SavedTo = sel.Path
		}
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
		keepUnhooked(sp.copies, pr.jobs, opts)
		for i := n; i < len(res.Steps); i++ {
			res.Steps[i].Root = root(sp.sc)
		}
		preps = append(preps, pr)
	}
	// What --trust covers is shown once, before the first write, then recorded (Plan 9 §3.4).
	if err := e.confirmTrusted(opts, res); err != nil {
		return nil, err
	}
	defer func() { err = res.afterGrants(err) }() // a failure from here on carries the grants written
	if opts.DryRun {
		return out, nil
	}
	if spec.BeforeWrite != nil {
		if err := spec.BeforeWrite(out); err != nil {
			return nil, e.withRetry(spec, err, fetched)
		}
	}
	if len(saves) > 0 {
		if err := config.Save(sel.Path, saves); err != nil {
			return nil, e.withRetry(spec, err, fetched)
		}
	}
	for i, sp := range plans {
		change := manifest.Change{Set: sp.entries}
		if len(sp.man.Targets) == 0 {
			change.Targets = sp.targets
		}
		updated := "the project's files, lock.json and project.yml were updated"
		err := e.saveManifest(sp.sc, sp.loaded, change, nil, sp.trust, updated, syncAgain(e.SyncCommand(sp.sc)), func(man []plan.ManifestWrite) error {
			return e.commit(ctx, preps[i], opts, res, man, func(*plan.Plan) error { return e.makePluginData(sp.sc, sp.changes) })
		})
		if err != nil && len(saves) > 0 {
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

// covers reports whether the scope's trust record covers ent; at user scope every entry is the
// user's own.
func (sp *scopePlan) covers(ent manifest.Entry) bool {
	return sp.trust == nil || sp.trust.View.Covers(ent)
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
	return errs.WithHints(&notTrustedError{untrusted.Text(ent.Name, untrusted.MaxName) + ": " + filepath.Base(e.files(sc).manifest) + " " + why},
		hints...)
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

// untrustedTargets refuses an install that would take its targets from sc's top-level targets
// while rec, the project's trust record, does not cover them (Plan 6a §10.1, M2): a pull may
// have put them there, and installing to them would trust them unseen. While the manifest holds
// an unresolved entry (Plan 6c §6.7), replaced ones included, the sync that reviews them refuses
// the file, so only the install naming its assistants is hinted.
func (e *Engine) untrustedTargets(sc Scope, rec *trust.Record, reqs []InstallRequest, unresolved bool) error {
	base := filepath.Base(e.files(sc).manifest)
	why := "changed since you last trusted them"
	if !rec.Exists {
		why = "have not been trusted on this machine"
	}
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
	err, named := &untrustedTargetsError{base + "'s top-level targets " + why}, e.Command(sc, "install", args...)+" -t <id>"
	if unresolved {
		return errs.WithHints(err, "name the assistants: "+named)
	}
	return errs.WithHints(err, "review and apply them: "+e.SyncCommand(sc), "or name the assistants: "+named)
}

// hasOwnTargets reports whether r installs an entry of sp's manifest that has targets of its
// own: the entry r names, or the one whose source is the location r gives. Anything else would
// install to the top-level targets, so a scope whose record does not cover them refuses it
// before a source is fetched.
func (e *Engine) hasOwnTargets(sp *scopePlan, sc Scope, cwd string, r InstallRequest) bool {
	if ent := sp.man.Find(r.Arg); ent != nil {
		return len(ent.Targets) > 0
	}
	spec, ok, err := source.Parse(r.Arg, cwd)
	if err != nil || !ok {
		return false
	}
	for _, ent := range sp.man.Modules {
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

// trustRecord is the trust record of a project scope, nil at user scope, whose user.yml is the
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

// saveManifest is every manifest write (Plan 6a §7.4). It edits the scope's manifest by c, the
// change lola makes to loaded (the manifest as read under the scope lock), unless c changes
// nothing, writes the pins of profiles into those project profiles (Plan 6b §6.4), and hands the
// edits to apply, which commits them with the ledger. After a successful apply at project scope
// that wrote a manifest or profile it refreshes rec, the project's trust record, per field
// (trust.Refresh), in project.yml's part and each profile's: what this write changed becomes
// trusted, and nothing else does. A record
// that cannot be written leaves lola's own edits uncovered, which later commands refuse: the
// error says what was applied (the caller's applied) and how to get them trusted (its hint).
func (e *Engine) saveManifest(sc Scope, loaded *manifest.Manifest, c manifest.Change, profiles []profileEdit, rec *trust.Record,
	applied, hint string, apply func([]plan.ManifestWrite) error) error {
	var writes []plan.ManifestWrite
	edited := c.Targets != nil || len(c.Set) > 0 || len(c.Remove) > 0
	if edited {
		dst, data, err := manifest.Edit(e.files(sc).manifest, c)
		if err != nil {
			return err
		}
		writes = append(writes, plan.ManifestWrite{Path: dst, Data: data})
	}
	for _, p := range profiles {
		data, err := p.layer.read.Pin(p.layer.Path, p.pins)
		if err != nil {
			return err
		}
		read, path := p.layer.read, p.layer.Path
		writes = append(writes, plan.ManifestWrite{Path: path, Data: data, Check: func() error { return read.Unchanged(sc.ProjectDir, path) }})
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
	if err := e.saveTrust(sc, view); err != nil {
		var files []string
		if edited {
			files = append(files, filepath.Base(e.files(sc).manifest))
		}
		for _, p := range profiles {
			files = append(files, projectProfileFile(p.layer.Key))
		}
		return errs.WithHints(fmt.Errorf("%s, but lola could not record that you trust what it wrote to %s: %w", applied, andJoin(files), err), hint)
	}
	return nil
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
// over the modules being installed. It returns the changes to save.
func (e *Engine) editSelections(sel *config.Selections, spec InstallSpec, plans []*scopePlan, res *Result) ([]config.Change, error) {
	if len(plans) == 0 {
		return nil, nil
	}
	var mods []*module.Module
	for _, rr := range plans[0].resolved {
		mods = append(mods, rr.v.Module)
	}
	targets := editTargets(mods)
	var sections []config.Where
	for _, sp := range plans {
		w, err := where(sp.sc)
		if err != nil {
			return nil, err
		}
		sections = append(sections, w)
	}
	changes, notes, err := applyEdits(sel, config.Global(), spec.GlobalEdits, targets, sections)
	if err != nil {
		return nil, err
	}
	res.Notices = append(res.Notices, notes...)
	for i, si := range spec.Scopes {
		c, n, err := applyEdits(sel, sections[i], si.Edits, targets, nil)
		if err != nil {
			return nil, err
		}
		changes, res.Notices = append(changes, c...), append(res.Notices, n...)
	}
	return changes, nil
}

// planInstallModule plans one resolved module at sp's scope. With no targets given for the
// scope, a module keeps the targets its manifest entry has.
func (e *Engine) planInstallModule(sp *scopePlan, rr resolvedRequest, sel *config.Selections, opts Options, probes *probeRun, res *Result) error {
	r, ent, v, given := rr.req, rr.ent, rr.v, sp.given
	if slices.ContainsFunc(sp.entries, func(x manifest.Entry) bool { return x.Name == v.Name }) {
		return fmt.Errorf("module %s is listed more than once", v.Name)
	}
	man := sp.man
	modTargets := sp.targets
	prev := man.Find(v.Name)
	// An install by source over an entry the trust record does not cover writes the entry as new
	// (Plan 6a.2 ruling S2): it inherits nothing a pull may have put there, and the trust refresh
	// records it whole. A request that adds targets (lola migrate's) keeps the entry's other
	// fields, so it is refused instead. With no targets given, the module stays on the targets
	// lock.json installed it on, which this checkout's own installs chose, so what is installed
	// is what the entry records.
	if prev != nil && !sp.covers(*prev) {
		if len(r.Targets) > 0 {
			return e.notTrusted(sp.sc, sp.trust, prev, false, sp.unresolved)
		}
		prev = nil
		sp.loaded.Modules = slices.DeleteFunc(sp.loaded.Modules, func(x manifest.Entry) bool { return x.Name == v.Name })
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
	} else if len(given) > 0 && len(man.Targets) > 0 && !slices.Equal(given, man.Targets) {
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
	var commit string
	if keepPin {
		commit, ent.Commit, ent.Digest = installed.Commit, prev.Commit, prev.Digest
	} else {
		var err error
		if commit, err = e.pinCommit(v, ent.Ref); err != nil {
			return err
		}
		if !e.isFolder(v.Name) {
			ent.Commit, ent.Digest = commit, v.Meta.Digest
		}
	}
	x, err := exclusions(sel, sp.sc, v.Name)
	if err != nil {
		return err
	}
	opts.freshPin, opts.entryFrom = !keepPin, ""
	intents, finds, notes, mr, err := e.planModule(sp.sc, v, modTargets, &ent, x, opts, probes, sp.templates)
	if err != nil {
		return err
	}
	mr.Scope, mr.Root, mr.Market = sp.sc.Kind.String(), root(sp.sc), shownMarket(ent.Market)
	res.Modules, res.Findings, res.Notices = append(res.Modules, mr), append(res.Findings, finds...), append(res.Notices, notes...)
	sp.changes = append(sp.changes, plan.ModuleChange{Name: v.Name, Targets: modTargets, Intents: intents,
		Entry: &ledger.Entry{Source: ent.Source, Ref: ent.Ref, Path: ent.Path, Commit: commit, Digest: v.Meta.Digest, Targets: mr.Targets,
			Components: ent.Components.Clone(), RenderedWith: mr.renderedWith}})
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
		o, _, err := e.Store.Origin(r.Version.Name)
		if err != nil {
			return err
		}
		if currentHead(o) != r.Version.ID {
			again = nil
		}
	}
	sp.copies = append(sp.copies, moduleCopy{name: v.Name, src: v.ContentDir, replace: true,
		hooks: v.Module.DeclaredHooks, targets: mr.Targets, runHooks: true, forced: !replace && !addsTargets(installed, mr.Targets),
		pending: installed != nil && installed.HooksPending && declaresPostInstall(v.Module.DeclaredHooks), review: review, again: again,
		shows: shows, reviewTargets: mr.Targets})
	sp.entries = append(sp.entries, ent)
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
func (e *Engine) listedRef(v VersionRef) (string, error) {
	o, _, err := e.Store.Origin(v.Name)
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

// isFolder reports whether the cache has name from a folder source, which is never pinned.
func (e *Engine) isFolder(name string) bool {
	o, found, err := e.Store.Origin(name)
	return err == nil && found && o.Type == string(source.KindFolder)
}

// pinCommit is the commit a git version is pinned at (Plan 6a §4.1): the one the fetch or the
// head that produced v resolved, else that of the cache's head for ref that holds v (a version
// listed by the pickers carries no commit). It is "" for other sources, and a git version no
// head for ref records a commit for is refused: its heads predate pins, or another ref's.
func (e *Engine) pinCommit(v store.Version, ref string) (string, error) {
	commit, git, err := e.headCommit(v, ref)
	if err != nil || !git || commit != "" {
		return commit, err
	}
	return "", errs.WithHints(fmt.Errorf("%s: your cache does not record which commit its version %s came from", v.Name, v.ID),
		"fetch it again, then install it: "+errs.ShellJoin([]string{"lola", "cache", "update", v.Name})+", then "+
			errs.ShellJoin([]string{"lola", "install", v.Name}))
}

// headCommit is pinCommit's commit without the refusal: git reports whether v is a git
// version, and commit is "" for one no head for ref records a commit for.
func (e *Engine) headCommit(v store.Version, ref string) (commit string, git bool, err error) {
	o, found, err := e.Store.Origin(v.Name)
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
	if errors.Is(err, fs.ErrNotExist) {
		err = fmt.Errorf("the version of %s installed here (%s) is no longer in the cache", untrusted.Text(v.Name, untrusted.MaxName),
			untrusted.Text(id, untrusted.MaxName))
	}
	return store.Version{}, errs.WithHints(untrusted.CleanErr(err),
		e.Command(sc, "render")+" installs the cached version of "+untrusted.Text(v.Name, untrusted.MaxName)+" here again; run it, then add the assistants")
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
func (e *Engine) notInstalled(sc Scope, name string) error {
	var hints []string
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
	// The profiles lock comes first (Plan 6b §10): the run reads the remote profiles once.
	unlockProfiles, err := e.readProfiles(opts.DryRun)
	if err != nil {
		return nil, err
	}
	defer unlockProfiles()
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
	// The files an uninstall removes are the ledger's, which is this checkout's own only while
	// git does not track it.
	if err := e.refuseTrackedLedger(ctx, sc, res); err != nil {
		return nil, err
	}
	layers, _, err := e.layers(sc, man, led)
	if err != nil {
		return nil, err
	}
	var changes []plan.ModuleChange
	for _, name := range names {
		if err := e.refuseProfileModule(sc, layers, name, opts.Display); err != nil {
			return nil, err
		}
		entry, ment := led.Modules[name], man.Find(name)
		if entry == nil && ment == nil {
			return nil, e.notInstalled(sc, name)
		}
		ts := opts.Targets
		if len(ts) == 0 {
			ts = allTargets(entry, ment, man)
		}
		// Removing a module only reduces what is installed; removing it from some targets
		// rewrites the targets of an entry someone else may have changed (Plan 6a §10.4).
		partial := slices.ContainsFunc(allTargets(entry, ment, man), func(t string) bool { return !slices.Contains(ts, t) })
		if ment != nil && rec != nil && partial && !rec.View.Covers(*ment) {
			return nil, e.notTrusted(sc, rec, ment, true, false)
		}
		changes = append(changes, plan.ModuleChange{Name: name, Targets: ts})
		res.Modules = append(res.Modules, ModuleResult{Name: name, Targets: ts, MCPServers: []MCPRun{}, Hooks: []HookRun{}, RuntimeHooks: []RuntimeHook{}, Skipped: []string{}})
	}
	pr, err := e.prepare(sc, led, changes, nil, nil, opts, res)
	if err != nil {
		return nil, err
	}
	if opts.DryRun {
		return res, nil
	}
	var change manifest.Change
	for _, name := range names {
		left := pr.plan.Ledger.Modules[name]
		if left == nil {
			change.Remove = append(change.Remove, name)
		} else if ment := man.Find(name); ment != nil {
			kept := *ment
			kept.Targets = allTargets(left, nil, &manifest.Manifest{})
			change.Set = append(change.Set, kept)
		}
	}
	removed := "lola removed what you uninstalled from the project's files, lock.json and project.yml"
	if err := e.saveManifest(sc, man, change, nil, rec, removed, syncAgain(e.SyncCommand(sc)),
		func(mw []plan.ManifestWrite) error { return e.commit(ctx, pr, opts, res, mw, nil) }); err != nil {
		return nil, err
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
func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// Render re-plans every installed module at the version its winning layer's entry pins
// (lock.json's when it pins none, Plan 6b §6.4), removing orphans and refusing to overwrite hand
// edits. A git or archive module is rendered from a snapshot of that cached version, fetched by its pin when the cache no
// longer holds it; a folder module is read from its folder. Render never reads a cache head, so
// lola cache update never changes what is installed, and it never acts on what the user has not
// trusted: a module no layer lists any more, asks for another source, does not cover in the
// trust record, or a folder whose content changed is left as installed, with a warn finding
// that names the remedy (Plan 6a §10.3, §10.7).
func (e *Engine) Render(ctx context.Context, sc Scope, opts Options) (_ *Result, err error) {
	defer func() { err = e.CoachUnresolved(err, []Scope{sc}, opts.DryRun, opts.Display) }()
	// The profiles lock comes first (Plan 6b §10): the run reads the remote profiles once.
	unlockProfiles, err := e.readProfiles(opts.DryRun)
	if err != nil {
		return nil, err
	}
	defer unlockProfiles()
	man, led, unlock, err := e.load(sc, opts.DryRun)
	if err != nil {
		return nil, err
	}
	defer unlock()
	res := newResult(opts.DryRun)
	res.Trusted = []Consent{} // a command that takes --trust says what it covered, [] for nothing
	if err := e.refuseTrackedLedger(ctx, sc, res); err != nil {
		return nil, err
	}
	sel, err := e.selections()
	if err != nil {
		return nil, err
	}
	layers, notices, err := e.layers(sc, man, led)
	if err != nil {
		return nil, err
	}
	want, _ := desired(layers)
	res.Notices = append(append(res.Notices, notices...), e.sweepRuns()...)
	run, done, err := e.runDir()
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, done()) }()
	rec, err := e.trustRecord(sc)
	if err != nil {
		return nil, err
	}
	// Every module is checked, locally, before anything is fetched.
	staged := map[string]*store.Staged{}
	var names []string
	for _, name := range sortedNames(led) {
		out, st, err := e.renderCheck(ctx, sc, man, find(want, name), rec, name, led.Modules[name], run, opts.Display)
		if err != nil {
			return nil, errs.WithHints(err, renderUninstall(name))
		}
		if out != nil {
			res.Findings, res.remedies = append(res.Findings, out.finding), append(res.remedies, out.hints...)
			continue
		}
		staged[name] = st
		names = append(names, name)
	}
	probes := e.newProbeRun(ctx)
	templates, err := e.newTemplateRun(sc, led.Profiles)
	if err != nil {
		return nil, err
	}
	var changes []plan.ModuleChange
	var copies []moduleCopy
	for _, name := range names {
		entry, w := led.Modules[name], find(want, name)
		var ment *manifest.Entry
		from := entry.From // a module no layer wants keeps the layer it came from
		if w != nil {
			ment, from = &w.Entry, ""
			if w.profile {
				from = w.From
			}
		}
		var v store.Version
		if st := staged[name]; st != nil {
			v, err = e.renderStaged(st, entry)
		} else {
			digest, commit, file, repin := e.renderPin(sc, name, entry, w)
			if v, err = e.installedForRender(ctx, sc, name, entry, digest, commit, run, res); err != nil {
				err = renderError(name, w, file, repin, err)
			}
		}
		if err != nil {
			return nil, err
		}
		ts := allTargets(entry, ment, man)
		if len(opts.Targets) > 0 {
			ts = slices.DeleteFunc(ts, func(t string) bool { return !slices.Contains(opts.Targets, t) })
		}
		if len(ts) == 0 {
			continue
		}
		x, err := exclusions(sel, sc, name)
		if err != nil {
			return nil, err
		}
		opts.entryFrom = from
		intents, finds, notes, mr, err := e.planModule(sc, v, ts, ment, x, opts, probes, templates)
		if err != nil {
			return nil, err
		}
		res.Modules, res.Findings, res.Notices = append(res.Modules, mr), append(res.Findings, finds...), append(res.Notices, notes...)
		changes = append(changes, plan.ModuleChange{Name: name, Targets: ts, Intents: intents,
			Entry: &ledger.Entry{From: from, Source: entry.Source, Ref: entry.Ref, Path: entry.Path, Commit: v.Commit, Digest: v.Meta.Digest, Targets: mr.Targets,
				Components: entry.Components.Clone(), RenderedWith: mr.renderedWith}})
		replace, err := needsCopy(e.files(sc).modules, name, entry, v.Meta.Digest)
		if err != nil {
			return nil, err
		}
		replace = replace || addsTargets(entry, mr.Targets) // a target its hooks never ran for
		copies = append(copies, moduleCopy{name: name, src: v.ContentDir, replace: replace,
			hooks: v.Module.DeclaredHooks, targets: mr.Targets, runHooks: replace, shows: &v, again: []string{"render"}})
	}
	res.Findings = append(res.Findings, probes.findings...)
	if err := e.execute(ctx, sc, led, changes, copies, opts, res, func(*plan.Plan) error { return e.makePluginData(sc, changes) }); err != nil {
		return nil, err
	}
	return res, nil
}

// Status reports each installed module × target: missing (an owned destination or the
// module copy is gone), modified (a destination differs from what lola wrote), stale (the
// manifest pins another version than the one installed), skipped (the target rendered nothing
// of the module), unreadable (the version installed is cached but does not load), or ok; and
// each entry a layer wants that lock.json does not record, not_installed, per effective target.
// Each row names the layer its module comes from (From): the layer whose entry wins now, else
// the one lock.json recorded. A module copy's content is not compared: hooks build in it. It
// reads no network, never the trust record, and changes nothing (Plan 6a §10.5). It refuses a
// project whose lock.json git tracks, since a committed one could name active profiles (Plan 6b
// §5, §19 S5); the notices are the active profiles that no longer exist, git missing, and how
// many modules are not installed with the sync that installs them, keeping display, the run's
// output flags.
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
	if what, _, _ := notInstalled(layers, led); what != "" {
		notices = append(notices, Notice{Level: LevelInfo, Text: what + " → " + e.SyncCommand(sc, display...)})
	}
	sel, err := e.selections()
	if err != nil {
		return nil, nil, err
	}
	rows := []StatusRow{}
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
		from := filepath.Base(e.files(sc).manifest)
		if entry != nil && entry.From != "" {
			from = entry.From
		}
		if w != nil {
			ment, from = &w.Entry, w.From
		}
		if entry == nil {
			rows = append(rows, e.notInstalledRows(man, *w)...)
			continue
		}
		pin, ref := "", entry.Ref
		if ment != nil {
			pin, ref = ment.Digest, ment.Ref
		}
		update, uerr := e.updateAvailable(name, ref, cmp.Or(pin, entry.Digest))
		base := StatusRow{Name: name, From: from, Rules: ment.RulesOn(), Source: entry.Source, Ref: entry.Ref, Digest: entry.Digest, Pin: pin,
			UpdateAvailable: update, Skipped: []string{}}
		if ment != nil {
			base.Market = shownMarket(ment.Market)
		}
		var v store.Version
		err := fs.ErrNotExist
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
			x, err := exclusions(sel, sc, name)
			if err != nil {
				return nil, nil, err
			}
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

// notInstalledRows are the list rows of w, an entry a layer wants that lock.json does not
// record: one per effective target, with no digest installed.
func (e *Engine) notInstalledRows(man *manifest.Manifest, w Wanted) []StatusRow {
	ment := &w.Entry
	ts := ment.Targets
	if len(ts) == 0 {
		ts = man.Targets
	}
	status, update := "not_installed", false
	if ment.Digest != "" {
		var err error
		if update, err = e.updateAvailable(ment.Name, ment.Ref, ment.Digest); err != nil {
			status = "unreadable" // the cache's record of the module cannot be loaded
		}
	}
	var rows []StatusRow
	for _, t := range ts {
		rows = append(rows, StatusRow{Name: ment.Name, From: w.From, Target: t, Status: status, Rules: ment.RulesOn(), Source: ment.Source, Ref: ment.Ref,
			Pin: ment.Digest, UpdateAvailable: update, Skipped: []string{}, Market: shownMarket(ment.Market)})
	}
	return rows
}

// InstallCoaching is what lola install without arguments says about sc (Plan 6a §10.2, Plan 6b
// §7): when its layers want modules lock.json does not record, the notice a terminal shows
// before the pickers and the hint the missing-argument error adds off one, both naming the
// layers that list them and the sync that installs them; "" when there are none. It reads the
// manifest, the ledger and the active profiles only, and refuses a project whose lock.json git
// tracks before reading the profiles it names, as every command that reads them does (Plan 6b
// §5, §19 S5).
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
// digest. It reads the cache index only, and a folder's head never counts: a folder is read
// again by every render.
func (e *Engine) updateAvailable(name, ref, digest string) (bool, error) {
	o, found, err := e.Store.Origin(name)
	if err != nil || !found || o.Type == string(source.KindFolder) {
		return false, err
	}
	for _, h := range o.Heads {
		if h.Ref == ref {
			return h.Digest != versionID(digest), nil
		}
	}
	return false, nil
}
