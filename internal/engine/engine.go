// Package engine orchestrates one lola operation over a scope: resolve and fetch modules into
// the store, ask targets for intents, compute and apply the plan, and update the manifest.
// It has no CLI concerns, so every flow is testable without cobra.
package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/gofrs/flock"

	"github.com/trevor-vaughan/lola-go/internal/consent"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/fsx"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/targets"
)

// Scope selects project or user state. ProjectDir is used at project scope only.
type Scope struct {
	Kind       target.Scope
	ProjectDir string
}

// Engine is the orchestration entry point. Environ is the environment install hooks and
// version probes inherit; HookTimeout bounds each hook run (§9.1) and ProbeTimeout each
// assistant version probe.
type Engine struct {
	Paths        paths.Paths
	Store        *store.Store
	Consent      *consent.Store
	Targets      []target.Target
	Now          func() time.Time
	Environ      func() []string
	HookTimeout  time.Duration
	ProbeTimeout time.Duration
}

// New returns an Engine over the real store and every registered target.
func New(p paths.Paths) *Engine {
	return &Engine{Paths: p, Store: store.New(p, targets.IDs()), Consent: consent.New(p.Consent), Targets: targets.All(), Now: time.Now,
		Environ: os.Environ, HookTimeout: 10 * time.Minute, ProbeTimeout: 5 * time.Second}
}

// Options apply to install, uninstall and render.
type Options struct {
	Targets []string // explicit target selection; empty means the manifest's targets
	DryRun  bool
	Force   bool  // overrides forceable conflicts and renders forceable skipped artifacts
	Strict  bool  // fail before writing anything when a warn or skip finding remains
	Rules   *bool // install only: false records rules: false, true clears it, nil keeps each entry's setting

	// Install hooks (§9.1). AllowHooks grants consent for this run. Ask, when set, asks the
	// user about hooks without a recorded consent; a yes is recorded. HookOutput receives
	// the hooks' stdout and stderr.
	AllowHooks bool
	Ask        func(module string, hooks []HookScript) (bool, error)
	HookOutput io.Writer
}

// MCPRun says what an MCP server runs: the stdio command line or the remote URL.
type MCPRun struct {
	Name      string `json:"name"`
	Transport string `json:"transport"`
	Runs      string `json:"runs"`
}

// ModuleResult summarizes one module of an operation.
type ModuleResult struct {
	Name       string    `json:"name"`
	Digest     string    `json:"digest,omitempty"`
	Targets    []string  `json:"targets"`
	MCPServers []MCPRun  `json:"mcp_servers"`
	Hooks      []HookRun `json:"hooks"`
}

// Notice levels.
const (
	LevelInfo = "info"
	LevelWarn = "warn"
)

// Notice is something lola itself tells the user about an operation. What a target loses
// when it renders a module is a target.Finding instead.
type Notice struct {
	Level string `json:"level"`
	Text  string `json:"text"`
}

// Result is what install, uninstall and render report.
type Result struct {
	Modules  []ModuleResult   `json:"modules"`
	Steps    []plan.Step      `json:"steps"`
	Findings []target.Finding `json:"findings"`
	Notices  []Notice         `json:"notices"`
	DryRun   bool             `json:"dry_run"`
}

func newResult(dry bool) *Result {
	return &Result{Modules: []ModuleResult{}, Steps: []plan.Step{}, Findings: []target.Finding{}, Notices: []Notice{}, DryRun: dry}
}

// InstallRequest is one install argument (§7.1) with its --ref and --path.
type InstallRequest struct {
	Arg, Ref, Path string
}

// StatusRow is one module × target line of `lola list`.
type StatusRow struct {
	Name   string `json:"name"`
	Target string `json:"target"`
	Status string `json:"status"` // ok, modified, missing, stale, skipped
	Rules  bool   `json:"rules"`  // false when the manifest entry says rules: false
	Source string `json:"source"`
	Ref    string `json:"ref,omitempty"`
	Digest string `json:"digest"`
}

// stateFiles are a scope's state locations. lock is empty at project scope: the project's
// lock file is named after its resolved .lola/ directory (see projectLock). modules holds
// the module copies that ${PLUGIN_ROOT} points into (§6.4).
type stateFiles struct{ lolaDir, manifest, ledger, lock, pluginData, modules string }

func (e *Engine) files(sc Scope) stateFiles {
	if sc.Kind == target.User {
		return stateFiles{manifest: e.Paths.UserManifest, ledger: e.Paths.UserLedger, lock: e.Paths.UserLock,
			pluginData: e.Paths.PluginData, modules: e.Paths.UserModules}
	}
	d := filepath.Join(sc.ProjectDir, ".lola")
	return stateFiles{lolaDir: d, manifest: filepath.Join(d, "project.yml"), ledger: filepath.Join(d, "lock.json"),
		pluginData: filepath.Join(d, "plugin-data"), modules: filepath.Join(d, "modules")}
}

// lolaGitignore is the content of .lola/.gitignore, which lola owns.
const lolaGitignore = "lock.json\nmodules/\nplugin-data/\n"

// projectLock returns the lock file of the project whose existing .lola/ directory is
// lolaDir. It lives under Paths.Locks, outside .lola/, so removing an empty .lola/ never
// unlinks a lock file another process is waiting on; the name hashes the resolved path, so
// every spelling of the project takes the same lock.
func (e *Engine) projectLock(lolaDir string) (string, error) {
	real, err := filepath.EvalSymlinks(lolaDir)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(real))
	return filepath.Join(e.Paths.Locks, hex.EncodeToString(sum[:8])+".lock"), nil
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
// It checks: project.yml doesn't exist, ledger is absent or empty (no modules or created files).
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
	if len(l.Modules) == 0 && len(l.Created) == 0 {
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

// Manifest loads the scope's manifest.
func (e *Engine) Manifest(sc Scope) (*manifest.Manifest, error) {
	return manifest.Load(e.files(sc).manifest)
}

// refuseSymlinkedState fails when the project's .lola/, .lola/modules or .lola/plugin-data
// is a symlink: a repository can commit one, and lola would then write, rename and delete
// outside the project. Missing directories are fine. User-scope state is the user's own and
// may link.
func (e *Engine) refuseSymlinkedState(sc Scope) error {
	f := e.files(sc)
	if f.lolaDir == "" {
		return nil
	}
	for _, p := range []string{f.lolaDir, f.modules, f.pluginData} {
		fi, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return errs.WithHints(fmt.Errorf("%s is a symlink, and lola keeps its state in real directories", p),
				"replace it with a directory: rm "+p+", then rerun")
		}
	}
	return nil
}

// lock creates the scope's state directory and takes its exclusive lock.
func (e *Engine) lock(sc Scope) (func(), error) {
	f := e.files(sc)
	lolaDidExist := false
	lockFile := f.lock
	if f.lolaDir != "" {
		if _, err := os.Stat(f.lolaDir); err == nil {
			lolaDidExist = true
		}
		if err := os.MkdirAll(f.lolaDir, 0o755); err != nil {
			return nil, err
		}
		gi := filepath.Join(f.lolaDir, ".gitignore")
		cur, err := os.ReadFile(gi)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		if !bytes.Equal(cur, []byte(lolaGitignore)) {
			if err := fsx.WriteFileAtomic(gi, []byte(lolaGitignore), 0o644); err != nil {
				return nil, err
			}
		}
		if lockFile, err = e.projectLock(f.lolaDir); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(lockFile), 0o700); err != nil {
		return nil, err
	}
	fl := flock.New(lockFile)
	if err := fl.Lock(); err != nil {
		return nil, fmt.Errorf("locking %s: %w", lockFile, err)
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
	f, err := source.Fetch(ctx, spec, e.Paths.Staging)
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
	// A local path typed relative to the project is recorded that way; a URL is kept as given.
	if sc.Kind == target.Project && filepath.IsAbs(spec.Location) && !filepath.IsAbs(r.Arg) && !strings.HasPrefix(r.Arg, "file://") {
		if rel, err := filepath.Rel(sc.ProjectDir, spec.Location); err == nil {
			src = rel
		}
	}
	return manifest.Entry{Name: v.Name, Source: src, Ref: r.Ref, Path: r.Path}, v, true, nil
}

// planModule asks every effective target for the module's intents and findings; ment is its
// manifest entry (nil when it has none).
func (e *Engine) planModule(sc Scope, v store.Version, targets []string, ment *manifest.Entry, opts Options, probes *probeRun) ([]plan.Intent, []target.Finding, []Notice, ModuleResult, error) {
	m := v.Module
	f := e.files(sc)
	pctx := target.PlanContext{Scope: sc.Kind, ContentDir: v.ContentDir, PluginRoot: filepath.Join(f.modules, m.Name),
		PluginData: filepath.Join(f.pluginData, m.Name), SkipRules: !ment.RulesOn(), Force: opts.Force}
	mr := ModuleResult{Name: m.Name, Digest: v.Meta.Digest, Targets: []string{}, MCPServers: []MCPRun{}, Hooks: []HookRun{}}
	finds := warningFindings(m)
	var notes []Notice
	var intents []plan.Intent
	for i, id := range targets {
		if slices.Contains(targets[:i], id) {
			continue // a target named twice is planned once
		}
		if !module.Supports(m.Targets, id) {
			notes = append(notes, Notice{Level: LevelInfo, Text: fmt.Sprintf("%s does not support %s; skipped there", m.Name, id)})
			continue
		}
		t, err := e.target(id)
		if err != nil {
			return nil, nil, nil, mr, err
		}
		pctx.Root, pctx.Version = t.Root(sc.Kind, sc.ProjectDir, e.Paths), probes.version(t)
		ins, fs, err := t.Plan(m, pctx)
		if err != nil {
			return nil, nil, nil, mr, fmt.Errorf("%s for %s: %w", m.Name, id, err)
		}
		for i := range fs {
			fs[i].Module = m.Name
		}
		intents, finds = append(intents, ins...), append(finds, fs...)
		mr.Targets = append(mr.Targets, id)
	}
	for _, s := range m.MCPServers {
		run := MCPRun{Name: s.Name, Transport: s.Transport, Runs: target.Expand(s.URL, pctx)}
		if s.Transport == module.TransportStdio {
			run.Runs = target.Expand(strings.Join(append([]string{s.Command}, s.Args...), " "), pctx)
		}
		mr.MCPServers = append(mr.MCPServers, run)
	}
	return intents, finds, notes, mr, nil
}

// warningFindings turns a module's load warnings (legacy instruction files, skipped rule
// entries) into findings about the module itself.
func warningFindings(m *module.Module) []target.Finding {
	finds := []target.Finding{}
	for _, w := range m.Warnings {
		finds = append(finds, target.Finding{Severity: target.SeverityWarn, Module: m.Name, Kind: "module", Code: "module-warning", Text: w})
	}
	return finds
}

// StrictError lists the findings --strict refuses: every warn and skip, except skipped
// artifacts that --force rendered. It returns nil when none remains. rerun is the caller's
// advice for getting past the findings; offerForce says the command has --force, which is
// advised only when a refused finding is one --force can render.
func StrictError(finds []target.Finding, rerun string, offerForce bool) error {
	var bad []target.Finding
	for _, f := range finds {
		if f.Severity != target.SeverityInfo && !f.Forced {
			bad = append(bad, f)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "--strict: %d finding(s); nothing was changed", len(bad))
	for _, f := range bad {
		fmt.Fprintf(&b, "\n  %s", f.Describe())
	}
	hints := []string{rerun}
	if offerForce && slices.ContainsFunc(bad, func(f target.Finding) bool { return f.Forceable }) {
		hints = append(hints, "--force renders skipped artifacts that can be rendered anyway")
	}
	return errs.WithHints(errors.New(b.String()), hints...)
}

// execute computes and (unless dry) applies changes, then runs after under the scope lock.
// copies are the module copies the changes render against: they are put in place before
// the plan is applied and restored if the ledger is not committed. Copies of modules that
// leave the ledger are removed. Install hooks get consent before anything is written;
// pre-install hooks run in the new copies before the plan is applied, post-install hooks
// after it is committed.
func (e *Engine) execute(ctx context.Context, sc Scope, led *ledger.Ledger, changes []plan.ModuleChange, copies []moduleCopy,
	opts Options, res *Result, after func(*plan.Plan) error) error {
	if opts.Strict {
		if err := StrictError(res.Findings, "fix the module, or rerun without --strict to install what each target supports", true); err != nil {
			return err
		}
	}
	req := plan.Request{Roots: map[string]string{}, BaseDirs: map[string][]string{}, Ledger: led, Changes: changes, Force: opts.Force}
	for _, t := range e.Targets {
		req.Roots[t.ID()] = t.Root(sc.Kind, sc.ProjectDir, e.Paths)
		req.BaseDirs[t.ID()] = t.BaseDirs(sc.Kind)
	}
	p, err := plan.Compute(req)
	if err != nil {
		return err
	}
	res.Steps = append(res.Steps, p.Steps...)
	jobs, err := e.planHooks(sc, copies, opts, res)
	if err != nil || opts.DryRun {
		return err
	}
	dir := e.files(sc).modules
	perm := fs.FileMode(0o755)
	if sc.Kind == target.User {
		perm = 0o700
	}
	sw, err := swapCopies(dir, copies, perm)
	if err != nil {
		return err
	}
	if err := e.runHooks(ctx, sc, jobs, PreInstall, opts, res); err != nil {
		return errors.Join(err, sw.rollback())
	}
	committed := false
	err = plan.Apply(p, e.files(sc).ledger, func() error {
		committed = true
		return after(p)
	})
	if !committed {
		return errors.Join(err, sw.rollback())
	}
	errs := []error{err, sw.commit()}
	for _, c := range changes {
		if p.Ledger.Modules[c.Name] == nil {
			errs = append(errs, os.RemoveAll(filepath.Join(dir, c.Name)))
		}
	}
	errs = append(errs, e.runHooks(ctx, sc, jobs, PostInstall, opts, res))
	return errors.Join(errs...)
}
