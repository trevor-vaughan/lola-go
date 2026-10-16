// Package engine orchestrates one lola operation over a scope: resolve and fetch modules into
// the store, ask targets for intents, compute and apply the plan, and update the manifest.
// It has no CLI concerns, so every flow is testable without cobra.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/target"
)

// Scope selects project or user state. ProjectDir is used at project scope only.
type Scope struct {
	Kind       target.Scope
	ProjectDir string
}

// Engine is the orchestration entry point.
type Engine struct {
	Paths   paths.Paths
	Store   *store.Store
	Targets []target.Target
	Now     func() time.Time
}

// New returns an Engine over the real store and every registered target.
func New(p paths.Paths) *Engine {
	return &Engine{Paths: p, Store: store.New(p), Targets: target.All(), Now: time.Now}
}

// Options apply to install, uninstall and render.
type Options struct {
	Targets []string // explicit target selection; empty means the manifest's targets
	DryRun  bool
	Force   bool
}

// MCPRun says what an MCP server runs: the stdio command line or the remote URL.
type MCPRun struct {
	Name      string `json:"name"`
	Transport string `json:"transport"`
	Runs      string `json:"runs"`
}

// ModuleResult summarizes one module of an operation.
type ModuleResult struct {
	Name       string        `json:"name"`
	Digest     string        `json:"digest,omitempty"`
	Targets    []string      `json:"targets"`
	MCPServers []MCPRun      `json:"mcp_servers"`
	Hooks      []module.Hook `json:"hooks"`
}

// Result is what install, uninstall and render report.
type Result struct {
	Modules []ModuleResult  `json:"modules"`
	Steps   []plan.Step     `json:"steps"`
	Notices []target.Notice `json:"notices"`
	DryRun  bool            `json:"dry_run"`
}

func newResult(dry bool) *Result {
	return &Result{Modules: []ModuleResult{}, Steps: []plan.Step{}, Notices: []target.Notice{}, DryRun: dry}
}

// InstallRequest is one install argument (§7.1) with its --ref and --path.
type InstallRequest struct {
	Arg, Ref, Path string
}

// StatusRow is one module × target line of `lola list`.
type StatusRow struct {
	Name   string `json:"name"`
	Target string `json:"target"`
	Status string `json:"status"` // ok, modified, missing, stale
	Source string `json:"source"`
	Ref    string `json:"ref,omitempty"`
	Digest string `json:"digest"`
}

type stateFiles struct{ lolaDir, manifest, ledger, lock, pluginData string }

func (e *Engine) files(sc Scope) stateFiles {
	if sc.Kind == target.User {
		return stateFiles{manifest: e.Paths.UserManifest, ledger: e.Paths.UserLedger, lock: e.Paths.UserLock, pluginData: e.Paths.PluginData}
	}
	d := filepath.Join(sc.ProjectDir, ".lola")
	return stateFiles{lolaDir: d, manifest: filepath.Join(d, "project.yml"), ledger: filepath.Join(d, "lock.json"),
		lock: filepath.Join(d, ".lock"), pluginData: filepath.Join(d, "plugin-data")}
}

// FindProject walks up from dir to the nearest directory containing .lola/; if there is
// none, dir itself is the project.
func FindProject(dir string) string {
	for d := dir; ; d = filepath.Dir(d) {
		if fi, err := os.Stat(filepath.Join(d, ".lola")); err == nil && fi.IsDir() {
			return d
		}
		if filepath.Dir(d) == d {
			return dir
		}
	}
}

// shouldRemoveLola reports whether dir (a .lola directory) contains no recorded state and should be removed.
// It checks: project.yml doesn't exist, ledger is absent or empty (no modules, shared, or created).
// If the ledger can't be read, it returns false with the error (don't remove if we can't check).
func shouldRemoveLola(dir string) (bool, error) {
	// Check if project.yml exists
	if _, err := os.Stat(filepath.Join(dir, "project.yml")); err == nil {
		return false, nil
	}

	// Check if ledger is empty
	lockPath := filepath.Join(dir, "lock.json")
	l, err := ledger.Load(lockPath)
	if err != nil {
		// If the file doesn't exist, dir is empty
		if errors.Is(err, fs.ErrNotExist) {
			return true, nil
		}
		// If there's another error (corrupt file), don't remove
		return false, err
	}

	// Check if ledger is empty
	if len(l.Modules) == 0 && len(l.Shared) == 0 && len(l.Created) == 0 {
		return true, nil
	}

	return false, nil
}

func (e *Engine) target(id string) (target.Target, error) {
	for _, t := range e.Targets {
		if t.ID() == id {
			return t, nil
		}
	}
	var ids []string
	for _, t := range e.Targets {
		ids = append(ids, t.ID())
	}
	return nil, errs.WithHints(fmt.Errorf("unknown target %q", id), "valid targets: "+strings.Join(ids, ", "))
}

// Detect lists the targets whose assistants appear to be used in sc, in registration order.
func (e *Engine) Detect(sc Scope) []string {
	var out []string
	for _, t := range e.Targets {
		if t.Detect(t.Root(sc.Kind, sc.ProjectDir, e.Paths)) {
			out = append(out, t.ID())
		}
	}
	return out
}

// Manifest loads the scope's manifest.
func (e *Engine) Manifest(sc Scope) (*manifest.Manifest, error) {
	return manifest.Load(e.files(sc).manifest)
}

// lock creates the scope's state directory and takes its exclusive lock.
func (e *Engine) lock(sc Scope) (func(), error) {
	f := e.files(sc)
	lolaDidExist := false
	if f.lolaDir != "" {
		if _, err := os.Stat(f.lolaDir); err == nil {
			lolaDidExist = true
		}
		if err := os.MkdirAll(f.lolaDir, 0o755); err != nil {
			return nil, err
		}
		gi := filepath.Join(f.lolaDir, ".gitignore")
		if _, err := os.Stat(gi); errors.Is(err, fs.ErrNotExist) {
			if err := os.WriteFile(gi, []byte("lock.json\nplugin-data/\n.lock\n"), 0o644); err != nil {
				return nil, err
			}
		}
	} else if err := os.MkdirAll(filepath.Dir(f.lock), 0o700); err != nil {
		return nil, err
	}
	fl := flock.New(f.lock)
	if err := fl.Lock(); err != nil {
		return nil, fmt.Errorf("locking %s: %w", f.lock, err)
	}
	return func() {
		// Decide and remove while still holding the lock: after Unlock a waiting process could
		// save a ledger that this removal would then delete.
		if f.lolaDir != "" && !lolaDidExist {
			if ok, err := shouldRemoveLola(f.lolaDir); ok && err == nil {
				_ = os.RemoveAll(f.lolaDir)
			}
		}
		_ = fl.Unlock()
	}, nil
}

// Fetch copies a source into the store (cache add, and install of a source).
func (e *Engine) Fetch(ctx context.Context, spec source.Spec) (v store.Version, err error) {
	if spec.Ref != "" && spec.Kind == source.KindFolder {
		return store.Version{}, fmt.Errorf("--ref applies to git sources only; %s is a folder", spec.Location)
	}
	f, err := source.FetchFolder(ctx, spec, e.Paths.Staging)
	if err != nil {
		return store.Version{}, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(f.Dir)) }()
	return e.Store.Put(store.PutRequest{Staged: f.Dir, FallbackName: f.Name, Source: spec.Location, Type: string(spec.Kind),
		Path: spec.Path, Ref: spec.Ref, Commit: f.Commit, Now: e.Now()})
}

// resolve implements §7.1 for this build: a stored name first (no network), then a source.
func (e *Engine) resolve(ctx context.Context, sc Scope, cwd string, r InstallRequest) (manifest.Entry, store.Version, bool, error) {
	if module.ValidateName("module", r.Arg) == nil {
		o, found, err := e.Store.Origin(r.Arg)
		if err != nil {
			return manifest.Entry{}, store.Version{}, false, err
		}
		if found {
			v, ok, err := e.Store.Head(r.Arg, r.Ref)
			if err != nil {
				return manifest.Entry{}, store.Version{}, false, err
			}
			fetched := false
			if !ok {
				v, err = e.Fetch(ctx, source.Spec{Kind: source.Kind(o.Type), Location: o.Source, Path: o.Path, Ref: r.Ref})
				if err != nil {
					return manifest.Entry{}, store.Version{}, false, err
				}
				fetched = true
			}
			return manifest.Entry{Name: r.Arg, Source: o.Source, Ref: r.Ref, Path: o.Path}, v, fetched, nil
		}
	}
	spec, ok, err := source.Parse(r.Arg, cwd)
	if err != nil {
		return manifest.Entry{}, store.Version{}, false, err
	}
	if !ok {
		return manifest.Entry{}, store.Version{}, false, errs.WithHints(
			fmt.Errorf("%q is neither a module in your cache nor a source", r.Arg),
			"sources are "+source.AcceptedForms, "cached modules: lola cache list")
	}
	spec.Ref, spec.Path = r.Ref, r.Path
	v, err := e.Fetch(ctx, spec)
	if err != nil {
		return manifest.Entry{}, store.Version{}, false, err
	}
	src := spec.Location
	if sc.Kind == target.Project && !filepath.IsAbs(r.Arg) && !strings.HasPrefix(r.Arg, "file://") {
		if rel, err := filepath.Rel(sc.ProjectDir, spec.Location); err == nil {
			src = rel
		}
	}
	return manifest.Entry{Name: v.Name, Source: src, Ref: r.Ref, Path: r.Path}, v, true, nil
}

// planModule asks every effective target for the module's intents.
func (e *Engine) planModule(sc Scope, v store.Version, targets []string) ([]plan.Intent, []target.Notice, ModuleResult, error) {
	m := v.Module
	pctx := target.PlanContext{Scope: sc.Kind, ContentDir: v.ContentDir, PluginData: filepath.Join(e.files(sc).pluginData, m.Name)}
	mr := ModuleResult{Name: m.Name, Digest: v.Meta.Digest, Targets: []string{}, MCPServers: []MCPRun{}, Hooks: m.DeclaredHooks}
	if mr.Hooks == nil {
		mr.Hooks = []module.Hook{}
	}
	var notes []target.Notice
	for _, w := range m.Warnings {
		notes = append(notes, target.Notice{Level: target.LevelWarn, Text: m.Name + ": " + w})
	}
	if m.LegacyAgentsMD {
		notes = append(notes, target.Notice{Level: target.LevelWarn, Text: m.Name + ": " + module.LegacyAgentsMDWarning})
	}
	var intents []plan.Intent
	for _, id := range targets {
		if !module.Supports(m.Targets, id) {
			notes = append(notes, target.Notice{Level: target.LevelInfo, Text: fmt.Sprintf("%s does not support %s; skipped there", m.Name, id)})
			continue
		}
		t, err := e.target(id)
		if err != nil {
			return nil, nil, mr, err
		}
		ins, n, err := t.Plan(m, pctx)
		if err != nil {
			return nil, nil, mr, fmt.Errorf("%s for %s: %w", m.Name, id, err)
		}
		intents, notes = append(intents, ins...), append(notes, n...)
		mr.Targets = append(mr.Targets, id)
	}
	for _, s := range m.MCPServers {
		run := MCPRun{Name: s.Name, Transport: s.Transport, Runs: target.Expand(s.URL, pctx)}
		if s.Transport == module.TransportStdio {
			run.Runs = target.Expand(strings.Join(append([]string{s.Command}, s.Args...), " "), pctx)
		}
		mr.MCPServers = append(mr.MCPServers, run)
	}
	return intents, notes, mr, nil
}

// sharedFor recomputes shared artifacts on targets from the modules that will ship
// instructions there once changes are applied.
func (e *Engine) sharedFor(sc Scope, led *ledger.Ledger, changes []plan.ModuleChange, targets []string) ([]plan.Intent, error) {
	var out []plan.Intent
	for _, id := range targets {
		t, err := e.target(id)
		if err != nil {
			return nil, err
		}
		has := map[string]bool{}
		replanned := map[string]bool{}
		for _, c := range changes {
			if slices.Contains(c.Targets, id) {
				replanned[c.Name] = true
				for _, in := range c.Intents {
					if in.Target == id && in.Kind == "instructions" {
						has[c.Name] = true
					}
				}
			}
		}
		for name, entry := range led.Modules {
			if replanned[name] {
				continue
			}
			for _, a := range entry.Artifacts {
				if a.Target == id && a.Kind == "instructions" {
					has[name] = true
				}
			}
		}
		var names []string
		for n := range has {
			names = append(names, n)
		}
		sort.Strings(names)
		out = append(out, t.Shared(sc.Kind, names)...)
	}
	return out, nil
}

// execute computes and (unless dry) applies changes, then runs after under the scope lock.
func (e *Engine) execute(sc Scope, led *ledger.Ledger, changes []plan.ModuleChange, opts Options, res *Result, after func(*plan.Plan) error) error {
	var affected []string
	for _, c := range changes {
		for _, t := range c.Targets {
			if !slices.Contains(affected, t) {
				affected = append(affected, t)
			}
		}
	}
	shared, err := e.sharedFor(sc, led, changes, affected)
	if err != nil {
		return err
	}
	req := plan.Request{Roots: map[string]string{}, BaseDirs: map[string][]string{}, Ledger: led, Changes: changes,
		SharedTargets: affected, Shared: shared, Force: opts.Force}
	for _, t := range e.Targets {
		req.Roots[t.ID()] = t.Root(sc.Kind, sc.ProjectDir, e.Paths)
		req.BaseDirs[t.ID()] = t.BaseDirs(sc.Kind)
	}
	p, err := plan.Compute(req)
	if err != nil {
		return err
	}
	res.Steps = append(res.Steps, p.Steps...)
	if opts.DryRun {
		return nil
	}
	return plan.Apply(p, e.files(sc).ledger, func() error { return after(p) })
}
