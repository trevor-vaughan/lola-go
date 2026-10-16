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
	return errs.WithHints(err, "fetched modules stay in your cache; retry with: lola install "+strings.Join(fetched, " "))
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
type scopePlan struct {
	sc       Scope
	man      *manifest.Manifest
	led      *ledger.Ledger
	targets  []string
	given    []string
	resolved []resolvedRequest
	changes  []plan.ModuleChange
	copies   []moduleCopy
	entries  []manifest.Entry
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
func (e *Engine) InstallAll(ctx context.Context, spec InstallSpec) (*InstallResult, error) {
	opts := spec.Options
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
	out := &InstallResult{Result: res, Scopes: []ScopeResult{}}
	probes := e.newProbeRun(ctx)
	var fetched []string
	have := map[int]store.Version{} // a source fetched for one scope is not fetched again for the next
	plans := make([]*scopePlan, len(spec.Scopes))
	for _, i := range lockOrder(spec.Scopes) {
		si := spec.Scopes[i]
		man, led, unlock, err := e.load(si.Scope, opts.DryRun)
		if err != nil {
			return nil, err
		}
		unlocks = append(unlocks, unlock)
		plans[i] = &scopePlan{sc: si.Scope, man: man, led: led, targets: si.Targets, given: si.Targets}
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
		for i, r := range spec.Modules {
			pre := have[i]
			if pre.Name == "" && r.Version != nil {
				if pre, err = e.pinnedVersion(r); err != nil {
					return nil, withRetry(err, fetched)
				}
			}
			ent, v, didFetch, err := e.resolve(ctx, si.Scope, spec.Cwd, r, pre)
			if err != nil {
				return nil, withRetry(err, fetched)
			}
			if didFetch {
				fetched, have[i] = append(fetched, v.Name), v
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
			if err := e.planInstallModule(sp, rr, sel, opts, probes, res); err != nil {
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
		pr, err := e.prepare(sp.sc, sp.led, sp.changes, sp.copies, opts, res)
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
	if len(saves) > 0 {
		if err := config.Save(sel.Path, saves); err != nil {
			return nil, withRetry(err, fetched)
		}
	}
	for i, sp := range plans {
		err := e.commit(ctx, preps[i], opts, res, func(*plan.Plan) error {
			if len(sp.man.Targets) == 0 {
				sp.man.Targets = sp.targets
			}
			for _, ent := range sp.entries {
				sp.man.Upsert(ent)
			}
			if err := sp.man.Save(e.files(sp.sc).manifest); err != nil {
				return err
			}
			return e.makePluginData(sp.sc, sp.changes)
		})
		if err != nil && len(saves) > 0 {
			err = errApplied(err, sel.Path)
		}
		if err != nil {
			return nil, withRetry(err, fetched)
		}
	}
	return out, nil
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
	if prev != nil && len(given) == 0 && len(prev.Targets) > 0 {
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
		Entry: &ledger.Entry{Source: ent.Source, Ref: ent.Ref, Path: ent.Path, Commit: v.Meta.Commit, Digest: v.Meta.Digest}})
	replace, err := needsCopy(e.files(sp.sc).modules, v.Name, sp.led.Modules[v.Name], v.Meta.Digest)
	if err != nil {
		return err
	}
	sp.copies = append(sp.copies, moduleCopy{name: v.Name, src: v.ContentDir, replace: replace,
		hooks: v.Module.DeclaredHooks, targets: mr.Targets, runHooks: true})
	sp.entries = append(sp.entries, ent)
	return nil
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
	if _, found, err := e.Store.Origin(name); err == nil && found {
		hints = append(hints, "still in your cache: lola cache rm "+name)
	}
	list := "lola list"
	if sc.Kind == target.User {
		list += " --user"
	}
	hints = append(hints, "installed here: "+list)
	return errs.WithHints(fmt.Errorf("lola uninstall: %q is not installed %s", name, e.where(sc)), hints...)
}

// Uninstall removes modules' artifacts (on opts.Targets, or everywhere) and drops them from
// the manifest once nothing remains.
func (e *Engine) Uninstall(ctx context.Context, sc Scope, names []string, opts Options) (*Result, error) {
	man, led, unlock, err := e.load(sc, opts.DryRun)
	if err != nil {
		return nil, err
	}
	defer unlock()
	res := newResult(opts.DryRun)
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
		changes = append(changes, plan.ModuleChange{Name: name, Targets: ts})
		res.Modules = append(res.Modules, ModuleResult{Name: name, Targets: ts, MCPServers: []MCPRun{}, Hooks: []HookRun{}, RuntimeHooks: []RuntimeHook{}, Skipped: []string{}})
	}
	err = e.execute(ctx, sc, led, changes, nil, opts, res, func(p *plan.Plan) error {
		for _, name := range names {
			left := p.Ledger.Modules[name]
			if left == nil {
				man.Remove(name)
			} else if ment := man.Find(name); ment != nil {
				ment.Targets = allTargets(left, nil, &manifest.Manifest{})
			}
		}
		return man.Save(e.files(sc).manifest)
	})
	if err != nil {
		return nil, err
	}
	for i := range res.Modules {
		name := res.Modules[i].Name
		if _, found, err := e.Store.Origin(name); err == nil && found {
			res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: "still in your cache: lola cache rm " + name})
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

// Render re-plans every installed module from the store head for its manifest ref,
// removing orphans and refusing to overwrite hand edits.
func (e *Engine) Render(ctx context.Context, sc Scope, opts Options) (*Result, error) {
	man, led, unlock, err := e.load(sc, opts.DryRun)
	if err != nil {
		return nil, err
	}
	defer unlock()
	sel, err := e.selections()
	if err != nil {
		return nil, err
	}
	res := newResult(opts.DryRun)
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
		v, ok, err := e.Store.Head(name, entry.Ref)
		if err != nil {
			// One module that no longer loads (one installed before a new refusal, say) stops
			// every module's render, so name the way out.
			return nil, errs.WithHints(err, "if you no longer want "+name+", remove it: lola uninstall "+name)
		}
		if !ok {
			o, found, err := e.Store.Origin(name)
			if err != nil {
				return nil, err
			}
			if found {
				v, err = e.Fetch(ctx, source.Spec{Kind: source.Kind(o.Type), Location: o.Source, Path: o.Path, Ref: entry.Ref})
				if err != nil {
					return nil, err
				}
			} else {
				cwd := sc.ProjectDir
				if sc.Kind == target.User {
					cwd = "/"
				}
				spec, isSrc, err := source.Parse(entry.Source, cwd)
				if err != nil {
					return nil, fmt.Errorf("%s is no longer in your cache and its recorded source %q is unusable: %w", name, entry.Source, err)
				}
				if !isSrc {
					return nil, errs.WithHints(fmt.Errorf("%s is no longer in your cache", name), "fetch it again: lola install "+entry.Source)
				}
				spec.Ref, spec.Path = entry.Ref, entry.Path
				v, err = e.Fetch(ctx, spec)
				if err != nil {
					return nil, err
				}
			}
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
			Entry: &ledger.Entry{Source: entry.Source, Ref: entry.Ref, Path: entry.Path, Commit: v.Meta.Commit, Digest: v.Meta.Digest}})
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
// store head differs), skipped (the target rendered nothing of the module), or ok. A module copy's content is not compared: hooks build in it.
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
		entry := led.Modules[name]
		v, ok, err := e.Store.Head(name, entry.Ref)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			// The cached head cannot be loaded (corrupt, unknown key): report it and keep going.
			for _, id := range allTargets(entry, man.Find(name), man) {
				rows = append(rows, StatusRow{Name: name, Target: id, Status: "unreadable", Rules: man.Find(name).RulesOn(),
					Source: entry.Source, Ref: entry.Ref, Digest: entry.Digest, Skipped: []string{}})
			}
			continue
		}
		stale := ok && v.Meta.Digest != entry.Digest
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
		for _, id := range allTargets(entry, man.Find(name), man) {
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
			rows = append(rows, StatusRow{Name: name, Target: id, Status: status, Rules: man.Find(name).RulesOn(),
				Source: entry.Source, Ref: entry.Ref, Digest: entry.Digest, Skipped: skipped})
		}
	}
	return rows, nil
}
