package engine

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
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
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/trust"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// load refuses symlinked project state, takes the scope lock (unless dry) and reads the
// manifest and ledger.
func (e *Engine) load(sc Scope, dry bool) (*manifest.Manifest, *ledger.Ledger, func(), error) {
	if err := e.refuseSymlinkedState(sc); err != nil {
		return nil, nil, nil, err
	}
	unlock := func() {}
	if !dry {
		u, err := e.lock(sc)
		if err != nil {
			return nil, nil, nil, err
		}
		unlock = u
	}
	f := e.files(sc)
	man, err := manifest.Load(f.manifest)
	if err != nil {
		unlock()
		return nil, nil, nil, err
	}
	led, err := ledger.Load(f.ledger)
	if err != nil {
		unlock()
		return nil, nil, nil, err
	}
	return man, led, unlock, nil
}
func (e *Engine) noTargets() error {
	var ids []string
	for _, t := range e.Targets {
		ids = append(ids, t.ID())
	}
	return errs.WithHints(errors.New("no targets selected"), "pass -t with one of: "+strings.Join(ids, ", "))
}

// withRetry tells the user that fetched modules stayed in the store.
func withRetry(err error, fetched []string) error {
	if len(fetched) == 0 {
		return err
	}
	return errs.WithHints(err, "fetched modules stay in your cache; retry with: "+errs.ShellJoin(append([]string{"lola", "install"}, fetched...)))
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
// Options.Targets is not used: each scope has its own. GlobalEdits change the global section
// of selections.yaml.
type InstallSpec struct {
	Cwd         string
	Scopes      []ScopeInstall
	Modules     []InstallRequest
	GlobalEdits []SelectionEdit
	Options     Options
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
// the project's trust record (nil at user scope).
type scopePlan struct {
	sc       Scope
	man      *manifest.Manifest
	loaded   *manifest.Manifest
	trust    *trust.Record
	led      *ledger.Ledger
	targets  []string
	given    []string
	resolved []resolvedRequest
	changes  []plan.ModuleChange
	copies   []moduleCopy
	entries  []manifest.Entry
	replace  []plan.Replaceable
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
	swept := e.sweepRuns()
	run, err := e.runDir()
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(run)) }()
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
		user := []string{"lola", "install"}
		for _, r := range spec.Modules {
			user = append(user, untrusted.Text(r.Arg, untrusted.MaxName))
		}
		return nil, errs.WithHints(fmt.Errorf("project scope at %s is your home directory, where the user scope installs",
			untrusted.Text(e.Paths.Home, untrusted.MaxName)),
			"install at user scope: "+errs.ShellJoin(append(user, "--user")))
	}
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
	res.Notices = append(res.Notices, swept...)
	out := &InstallResult{Result: res, Scopes: []ScopeResult{}}
	probes := e.newProbeRun(ctx)
	var fetched []string
	have := map[int]store.Version{} // a source fetched for one scope is not fetched again for the next
	haveRef := map[int]string{}     // ... unless the next scope's entry records another ref
	plans := make([]*scopePlan, len(spec.Scopes))
	for _, i := range lockOrder(spec.Scopes) {
		si := spec.Scopes[i]
		man, led, unlock, err := e.load(si.Scope, opts.DryRun)
		if err != nil {
			return nil, err
		}
		unlocks = append(unlocks, unlock)
		// The targets an install by source keeps (planInstallModule) and the files it removes
		// come from the ledger, which is this checkout's own only while git does not track it.
		if err := e.refuseTrackedLedger(ctx, si.Scope, res); err != nil {
			return nil, err
		}
		rec, err := e.trustRecord(si.Scope)
		if err != nil {
			return nil, err
		}
		plans[i] = &scopePlan{sc: si.Scope, man: man, loaded: man.Clone(), trust: rec, led: led, targets: si.Targets, given: si.Targets,
			replace: si.Replace}
	}
	// A name installs the entry as project.yml has it: refused for an entry someone else
	// changed, at every scope before any request is resolved, so nothing is fetched (Plan 6a
	// §10.1).
	for k, sp := range plans {
		for _, r := range spec.Modules {
			if ent := sp.man.Find(r.Arg); ent != nil && !sp.covers(*ent) {
				return nil, e.changedSinceTrusted(spec.Scopes[k].Scope, spec.Cwd, ent, false)
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
			if ref == "" && r.Version != nil && sp.man.Find(r.Arg) == nil {
				if r.Ref, err = e.listedRef(*r.Version); err != nil {
					return nil, withRetry(err, fetched)
				}
				ref = r.Ref
			}
			pre := have[i]
			if haveRef[i] != ref {
				pre = store.Version{}
			}
			if pre.Name == "" && r.Version != nil {
				if pre, err = e.pinnedVersion(r); err != nil {
					return nil, withRetry(err, fetched)
				}
				// The pickers list the head of the entry's ref; any other version is not what was confirmed.
				if r.Ref == "" && sp.man.Find(r.Arg) != nil {
					if head, ok, err := e.Store.Head(r.Arg, ref); err != nil {
						return nil, withRetry(err, fetched)
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
			ent, v, didFetch, err := e.resolve(ctx, si.Scope, spec.Cwd, r, pre, entryRef(sp.man, r.Arg), run)
			if err != nil {
				return nil, withRetry(err, fetched)
			}
			if didFetch {
				fetched, have[i], haveRef[i] = append(fetched, v.Name), v, ref
			}
			sp.resolved = append(sp.resolved, resolvedRequest{req: r, ent: ent, v: v})
		}
		out.Scopes = append(out.Scopes, ScopeResult{Scope: si.Scope.Kind.String(), Root: root(si.Scope), ScopeSource: si.Source})
	}
	saves, err := e.editSelections(sel, spec, plans, res)
	if err != nil {
		return nil, withRetry(err, fetched)
	}
	for _, sp := range plans {
		for _, rr := range sp.resolved {
			if err := e.planInstallModule(sp, rr, spec.Cwd, sel, opts, probes, res); err != nil {
				return nil, withRetry(err, fetched)
			}
		}
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
		pr, err := e.prepare(sp.sc, sp.led, sp.changes, sp.copies, sp.replace, opts, res)
		if err != nil {
			return nil, withRetry(err, fetched)
		}
		for i := n; i < len(res.Steps); i++ {
			res.Steps[i].Root = root(sp.sc)
		}
		preps = append(preps, pr)
	}
	if opts.DryRun {
		return out, nil
	}
	if spec.BeforeWrite != nil {
		if err := spec.BeforeWrite(out); err != nil {
			return nil, withRetry(err, fetched)
		}
	}
	if len(saves) > 0 {
		if err := config.Save(sel.Path, saves); err != nil {
			return nil, withRetry(err, fetched)
		}
	}
	for i, sp := range plans {
		change := manifest.Change{Set: sp.entries}
		if len(sp.man.Targets) == 0 {
			change.Targets = sp.targets
		}
		err := e.saveManifest(sp.sc, sp.loaded, change, sp.trust, func(man *plan.ManifestWrite) error {
			return e.commit(ctx, preps[i], opts, res, man, func(*plan.Plan) error { return e.makePluginData(sp.sc, sp.changes) })
		})
		if err != nil && len(saves) > 0 {
			err = errApplied(err, sel.Path)
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
			err = fmt.Errorf("%w; lola stopped there and did not install into %s", err, strings.Join(skipped, " or "))
		}
		if err != nil {
			return nil, withRetry(err, fetched)
		}
	}
	return out, nil
}

// covers reports whether the scope's trust record covers ent; at user scope every entry is the
// user's own.
func (sp *scopePlan) covers(ent manifest.Entry) bool {
	return sp.trust == nil || sp.trust.View.Covers(ent)
}

// changedSinceTrusted refuses to rewrite ent, an entry of sc's manifest the trust record does
// not cover (Plan 6a §10.1, §10.4, §10.8): a pull or a hand edit changed it since lola wrote it,
// so lola acts on it only once the user installs it from its source, which records it as
// trusted. remove adds the other way out, uninstalling it. cwd is the directory lola runs in.
func (e *Engine) changedSinceTrusted(sc Scope, cwd string, ent *manifest.Entry, remove bool) error {
	name := untrusted.Text(ent.Name, untrusted.MaxName)
	hints := []string{installFromSource(sc, cwd, ent)}
	if remove {
		hints = append(hints, "or remove it: "+errs.ShellJoin([]string{"lola", "uninstall", name}))
	}
	return errs.WithHints(fmt.Errorf("%s: %s %w", name, filepath.Base(e.files(sc).manifest), ErrChangedSinceTrusted), hints...)
}

// ErrChangedSinceTrusted is wrapped by the refusal to rewrite a manifest entry the trust record
// does not cover.
var ErrChangedSinceTrusted = errors.New("changed for it since you last trusted it")

// installFromSource coaches installing ent, an entry of sc's manifest, from its source, run in
// cwd. A local source the manifest records relative to the project root is named relative to
// cwd, so the install records it relative again (resolve) and a committed manifest stays
// portable; one that no longer parses is joined to the root as recorded.
func installFromSource(sc Scope, cwd string, ent *manifest.Entry) string {
	location := ent.Source
	spec, ok, err := source.Parse(ent.Source, canonicalRoot(sc.ProjectDir))
	parsed := err == nil && ok
	if parsed {
		location = spec.Location
	}
	remote := strings.Contains(ent.Source, "://") || (parsed && spec.Kind == source.KindGit)
	if !remote && !filepath.IsAbs(ent.Source) {
		abs := filepath.Join(canonicalRoot(sc.ProjectDir), ent.Source)
		if parsed {
			abs = spec.Location
		}
		// Rel against cwd as given: source.Parse joins a relative argument to it the same way.
		if rel, err := filepath.Rel(cwd, abs); err == nil {
			location = rel
			if rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				location = "." + string(filepath.Separator) + rel
			}
		}
	}
	return "if you trust it, install it from its source: " + installCommand(location, ent.Ref, ent.Path)
}

// trustRecord is the trust record of a project scope, nil at user scope, whose user.yml is the
// user's own (Plan 6a §7.1).
func (e *Engine) trustRecord(sc Scope) (*trust.Record, error) {
	if sc.Kind != target.Project {
		return nil, nil
	}
	return e.Trust.Load(sc.ProjectDir)
}

// saveManifest is every manifest write (Plan 6a §7.4). It edits the scope's manifest by c, the
// change lola makes to loaded (the manifest as read under the scope lock), and hands the edit to
// apply, which commits it with the ledger. After a successful apply at project scope it
// refreshes rec, the project's trust record, per field (trust.Refresh): what this write changed
// becomes trusted, and nothing else does. A record that cannot be written leaves lola's own
// edits uncovered, which later commands refuse: the error says what was applied.
func (e *Engine) saveManifest(sc Scope, loaded *manifest.Manifest, c manifest.Change, rec *trust.Record, apply func(*plan.ManifestWrite) error) error {
	dst, data, err := manifest.Edit(e.files(sc).manifest, c)
	if err != nil {
		return err
	}
	if err := apply(&plan.ManifestWrite{Path: dst, Data: data}); err != nil || rec == nil {
		return err
	}
	view := trust.Refresh(rec.View, trust.ViewOf(loaded), trust.ViewOf(loaded.Applied(c)))
	if err := e.Trust.Save(sc.ProjectDir, view); err != nil {
		return errs.WithHints(fmt.Errorf("the project's files, lock.json and project.yml were updated, but lola could not record that you trust "+
			"what it wrote to project.yml: %w", err),
			"until it can, lola treats those entries as changed behind its back; fix the trust file, then install each of them again from its source")
	}
	return nil
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
// scope, a module keeps the targets its manifest entry has. cwd is the directory lola runs in.
func (e *Engine) planInstallModule(sp *scopePlan, rr resolvedRequest, cwd string, sel *config.Selections, opts Options, probes *probeRun, res *Result) error {
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
			return e.changedSinceTrusted(sp.sc, cwd, prev, false)
		}
		prev = nil
		sp.loaded.Modules = slices.DeleteFunc(sp.loaded.Modules, func(x manifest.Entry) bool { return x.Name == v.Name })
		var onLedger []string
		if in := sp.led.Modules[v.Name]; in != nil && len(given) == 0 {
			for _, a := range in.Artifacts {
				onLedger = append(onLedger, a.Target)
			}
		}
		slices.Sort(onLedger)
		if onLedger = slices.Compact(onLedger); len(onLedger) > 0 {
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
		if v, err = e.installedVersion(v, prev, installed); err != nil {
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
		if r.Ref == "" {
			ent.Ref = prev.Ref
		}
	}
	if prev != nil {
		ent.Rules = prev.Rules
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
	intents, finds, notes, mr, err := e.planModule(sp.sc, v, modTargets, &ent, x, opts, probes)
	if err != nil {
		return err
	}
	mr.Scope, mr.Root = sp.sc.Kind.String(), root(sp.sc)
	res.Modules, res.Findings, res.Notices = append(res.Modules, mr), append(res.Findings, finds...), append(res.Notices, notes...)
	sp.changes = append(sp.changes, plan.ModuleChange{Name: v.Name, Targets: modTargets, Intents: intents,
		Entry: &ledger.Entry{Source: ent.Source, Ref: ent.Ref, Path: ent.Path, Commit: commit, Digest: v.Meta.Digest}})
	replace, err := needsCopy(e.files(sp.sc).modules, v.Name, sp.led.Modules[v.Name], v.Meta.Digest)
	if err != nil {
		return err
	}
	sp.copies = append(sp.copies, moduleCopy{name: v.Name, src: v.ContentDir, replace: replace,
		hooks: v.Module.DeclaredHooks, targets: mr.Targets, runHooks: true})
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
func (e *Engine) installedVersion(v store.Version, prev *manifest.Entry, installed *ledger.Entry) (store.Version, error) {
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
		"lola render installs the cached version of "+untrusted.Text(v.Name, untrusted.MaxName)+" here again; run it, then add the assistants")
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
		for _, a := range entry.Artifacts {
			set[a.Target] = true
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
	list := "lola list"
	if sc.Kind == target.User {
		list += " --user"
	}
	hints = append(hints, "installed here: "+list)
	return errs.WithHints(fmt.Errorf("lola uninstall: %q is not installed %s", name, e.where(sc)), hints...)
}

// Uninstall removes modules' artifacts (on opts.Targets, or everywhere) and drops them from
// the manifest once nothing remains. cwd is the directory lola runs in, which hints name
// sources relative to.
func (e *Engine) Uninstall(ctx context.Context, sc Scope, cwd string, names []string, opts Options) (*Result, error) {
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
	var changes []plan.ModuleChange
	for _, name := range names {
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
			return nil, e.changedSinceTrusted(sc, cwd, ment, true)
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
	if err := e.saveManifest(sc, man, change, rec, func(mw *plan.ManifestWrite) error { return e.commit(ctx, pr, opts, res, mw, nil) }); err != nil {
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

// Render re-plans every installed module at the version lock.json records, removing orphans
// and refusing to overwrite hand edits. A git or archive module is rendered from a snapshot of
// that cached version, fetched by its pin when the cache no longer holds it; a folder module is
// read from its folder. Render never reads a cache head, so lola cache update never changes
// what is installed (Plan 6a §10.3, §10.7).
func (e *Engine) Render(ctx context.Context, sc Scope, opts Options) (_ *Result, err error) {
	man, led, unlock, err := e.load(sc, opts.DryRun)
	if err != nil {
		return nil, err
	}
	defer unlock()
	res := newResult(opts.DryRun)
	if err := e.refuseTrackedLedger(ctx, sc, res); err != nil {
		return nil, err
	}
	sel, err := e.selections()
	if err != nil {
		return nil, err
	}
	res.Notices = append(res.Notices, e.sweepRuns()...)
	run, err := e.runDir()
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(run)) }()
	probes := e.newProbeRun(ctx)
	names := make([]string, 0, len(led.Modules))
	for n := range led.Modules {
		names = append(names, n)
	}
	sort.Strings(names)
	var changes []plan.ModuleChange
	var copies []moduleCopy
	for _, name := range names {
		entry := led.Modules[name]
		v, err := e.installedForRender(ctx, sc, name, entry, run, res)
		if err != nil {
			return nil, err
		}
		ts := allTargets(entry, man.Find(name), man)
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
		intents, finds, notes, mr, err := e.planModule(sc, v, ts, man.Find(name), x, opts, probes)
		if err != nil {
			return nil, err
		}
		res.Modules, res.Findings, res.Notices = append(res.Modules, mr), append(res.Findings, finds...), append(res.Notices, notes...)
		changes = append(changes, plan.ModuleChange{Name: name, Targets: ts, Intents: intents,
			Entry: &ledger.Entry{Source: entry.Source, Ref: entry.Ref, Path: entry.Path, Commit: v.Commit, Digest: v.Meta.Digest}})
		replace, err := needsCopy(e.files(sc).modules, name, entry, v.Meta.Digest)
		if err != nil {
			return nil, err
		}
		copies = append(copies, moduleCopy{name: name, src: v.ContentDir, replace: replace,
			hooks: v.Module.DeclaredHooks, targets: mr.Targets, runHooks: replace})
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
// of the module), unreadable (the version installed is cached but does not load), or ok. A
// module copy's content is not compared: hooks build in it. It reads no network and changes
// nothing (Plan 6a §10.5).
func (e *Engine) Status(sc Scope) ([]StatusRow, error) {
	man, led, _, err := e.load(sc, true)
	if err != nil {
		return nil, err
	}
	sel, err := e.selections()
	if err != nil {
		return nil, err
	}
	rows := []StatusRow{}
	names := make([]string, 0, len(led.Modules))
	for n := range led.Modules {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		entry, ment := led.Modules[name], man.Find(name)
		pin, ref := "", entry.Ref
		if ment != nil {
			pin, ref = ment.Digest, ment.Ref
		}
		update, uerr := e.updateAvailable(name, ref, cmp.Or(pin, entry.Digest))
		base := StatusRow{Name: name, Rules: ment.RulesOn(), Source: entry.Source, Ref: entry.Ref, Digest: entry.Digest, Pin: pin,
			UpdateAvailable: update, Skipped: []string{}}
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
				return nil, err
			}
			skipped = resolveComponents(v.Module, x, true).skipped
		}
		copyMissing, err := needsCopy(e.files(sc).modules, name, entry, entry.Digest)
		if err != nil {
			return nil, err
		}
		for _, id := range allTargets(entry, ment, man) {
			if ok && !module.Supports(v.Module.Targets, id) {
				continue
			}
			t, err := e.target(id)
			if err != nil {
				return nil, err
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
					return nil, err
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
	return rows, nil
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
