package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/target"
)

// load takes the scope lock (unless dry) and reads the manifest and ledger.
func (e *Engine) load(sc Scope, dry bool) (*manifest.Manifest, *ledger.Ledger, func(), error) {
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

// Install resolves each request, renders it for the selected targets, applies the plan and
// records the modules in the manifest.
func (e *Engine) Install(ctx context.Context, sc Scope, cwd string, reqs []InstallRequest, opts Options) (*Result, error) {
	man, led, unlock, err := e.load(sc, opts.DryRun)
	if err != nil {
		return nil, err
	}
	defer unlock()
	targets := opts.Targets
	if len(targets) == 0 {
		targets = man.Targets
	}
	if len(targets) == 0 {
		return nil, e.noTargets()
	}
	for _, id := range targets {
		if _, err := e.target(id); err != nil {
			return nil, err
		}
	}
	res := newResult(opts.DryRun)
	var changes []plan.ModuleChange
	var entries []manifest.Entry
	var fetched []string
	for _, r := range reqs {
		ent, v, didFetch, err := e.resolve(ctx, sc, cwd, r)
		if err != nil {
			return nil, withRetry(err, fetched)
		}
		if didFetch {
			fetched = append(fetched, v.Name)
		}
		if slices.ContainsFunc(entries, func(x manifest.Entry) bool { return x.Name == v.Name }) {
			return nil, withRetry(fmt.Errorf("module %s is listed more than once", v.Name), fetched)
		}
		modTargets := targets
		if prev := man.Find(v.Name); prev != nil && len(opts.Targets) == 0 && len(prev.Targets) > 0 {
			modTargets, ent.Targets = prev.Targets, prev.Targets
		} else if len(opts.Targets) > 0 && len(man.Targets) > 0 && !slices.Equal(opts.Targets, man.Targets) {
			ent.Targets = opts.Targets
		}
		if prev := man.Find(v.Name); prev != nil && r.Arg == v.Name {
			ent.Source = prev.Source
			ent.Path = prev.Path
			if r.Ref == "" {
				ent.Ref = prev.Ref
			}
		}
		intents, notes, mr, err := e.planModule(sc, v, modTargets)
		if err != nil {
			return nil, withRetry(err, fetched)
		}
		res.Modules, res.Notices = append(res.Modules, mr), append(res.Notices, notes...)
		changes = append(changes, plan.ModuleChange{Name: v.Name, Targets: modTargets, Intents: intents,
			Entry: &ledger.Entry{Source: ent.Source, Ref: ent.Ref, Path: ent.Path, Commit: v.Meta.Commit, Digest: v.Meta.Digest}})
		entries = append(entries, ent)
	}
	err = e.execute(sc, led, changes, opts, res, func(*plan.Plan) error {
		if len(man.Targets) == 0 {
			man.Targets = targets
		}
		for _, ent := range entries {
			man.Upsert(ent)
		}
		if err := man.Save(e.files(sc).manifest); err != nil {
			return err
		}
		return e.makePluginData(sc, changes)
	})
	if err != nil {
		return nil, withRetry(err, fetched)
	}
	return res, nil
}

// makePluginData creates ${PLUGIN_DATA} for modules whose rendered artifacts reference it.
func (e *Engine) makePluginData(sc Scope, changes []plan.ModuleChange) error {
	for _, c := range changes {
		dir := filepath.Join(e.files(sc).pluginData, c.Name)
		if slices.ContainsFunc(c.Intents, func(in plan.Intent) bool {
			return bytes.Contains(in.Data, []byte(dir)) || bytes.Contains(in.Value, []byte(dir))
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
		res.Modules = append(res.Modules, ModuleResult{Name: name, Targets: ts, MCPServers: []MCPRun{}, Hooks: []module.Hook{}})
	}
	err = e.execute(sc, led, changes, opts, res, func(p *plan.Plan) error {
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
			res.Notices = append(res.Notices, target.Notice{Level: target.LevelInfo, Text: "still in your cache: lola cache rm " + name})
		}
		if dir := filepath.Join(e.files(sc).pluginData, name); dirExists(dir) {
			res.Notices = append(res.Notices, target.Notice{Level: target.LevelInfo, Text: name + ": plugin data kept at " + dir})
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
	res := newResult(opts.DryRun)
	names := make([]string, 0, len(led.Modules))
	for n := range led.Modules {
		names = append(names, n)
	}
	sort.Strings(names)
	var changes []plan.ModuleChange
	for _, name := range names {
		entry := led.Modules[name]
		v, ok, err := e.Store.Head(name, entry.Ref)
		if err != nil {
			return nil, err
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
		intents, notes, mr, err := e.planModule(sc, v, ts)
		if err != nil {
			return nil, err
		}
		res.Modules, res.Notices = append(res.Modules, mr), append(res.Notices, notes...)
		changes = append(changes, plan.ModuleChange{Name: name, Targets: ts, Intents: intents,
			Entry: &ledger.Entry{Source: entry.Source, Ref: entry.Ref, Path: entry.Path, Commit: v.Meta.Commit, Digest: v.Meta.Digest}})
	}
	if err := e.execute(sc, led, changes, opts, res, func(*plan.Plan) error { return e.makePluginData(sc, changes) }); err != nil {
		return nil, err
	}
	return res, nil
}

// Status reports each installed module × target: missing (an owned destination is gone),
// modified (one differs from what lola wrote), stale (the store head differs), or ok.
func (e *Engine) Status(sc Scope) ([]StatusRow, error) {
	man, led, _, err := e.load(sc, true)
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
				rows = append(rows, StatusRow{Name: name, Target: id, Status: "unreadable", Source: entry.Source, Ref: entry.Ref, Digest: entry.Digest})
			}
			continue
		}
		stale := ok && v.Meta.Digest != entry.Digest
		for _, id := range allTargets(entry, man.Find(name), man) {
			if ok && !module.Supports(v.Module.Targets, id) {
				continue
			}
			t, err := e.target(id)
			if err != nil {
				return nil, err
			}
			root := t.Root(sc.Kind, sc.ProjectDir, e.Paths)
			status := "ok"
			for _, a := range entry.Artifacts {
				if a.Target != id {
					continue
				}
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
			if status == "ok" && stale {
				status = "stale"
			}
			rows = append(rows, StatusRow{Name: name, Target: id, Status: status, Source: entry.Source, Ref: entry.Ref, Digest: entry.Digest})
		}
	}
	return rows, nil
}
