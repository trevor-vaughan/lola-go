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

	"github.com/trevor-vaughan/lola-go/internal/config"
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
	"github.com/trevor-vaughan/lola-go/internal/trust"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Scope selects project or user state. ProjectDir is used at project scope only.
type Scope struct {
	Kind       target.Scope
	ProjectDir string
}

// Key identifies the destination a scope is (Plan 5c §14): "user", or a project's canonical key
// (paths.ProjectKey) in the form that compares as the OS compares paths, so every spelling of
// one project root is one destination.
func (s Scope) Key() string {
	if s.Kind == target.User {
		return config.ScopeUser
	}
	key, err := paths.ProjectKey(s.ProjectDir)
	if err != nil { // only a relative ProjectDir with no working directory; keep it as given
		key = s.ProjectDir
	}
	return paths.KeyID(key)
}

// Engine is the orchestration entry point. Environ is the environment install hooks and
// version probes inherit; HookTimeout bounds each hook run (§9.1) and ProbeTimeout each
// assistant version probe.
type Engine struct {
	Paths        paths.Paths
	Store        *store.Store
	Consent      *consent.Store
	Trust        *trust.Store
	Targets      []target.Target
	Now          func() time.Time
	Environ      func() []string
	HookTimeout  time.Duration
	ProbeTimeout time.Duration
}

// New returns an Engine over the real store and every registered target.
func New(p paths.Paths) *Engine {
	return &Engine{Paths: p, Store: store.New(p, targets.IDs()), Consent: consent.New(p.Consent), Trust: trust.New(p), Targets: targets.All(), Now: time.Now,
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

	// Runtime hooks (Plan 5b design §7.3), which an assistant runs on its own events.
	// AllowRuntimeHooks grants and records consent to the hooks the summary lists;
	// AskRuntimeHooks, when set, asks the user about hooks without a recorded consent, and a
	// yes is recorded. AllowHooks never covers them.
	AllowRuntimeHooks bool
	AskRuntimeHooks   func(module string, hooks []RuntimeHook) (bool, error)
}

// MCPRun says what an MCP server runs: the stdio command line or the remote URL.
type MCPRun struct {
	Name      string `json:"name"`
	Transport string `json:"transport"`
	Runs      string `json:"runs"`
}

// ModuleResult summarizes one module of an operation.
type ModuleResult struct {
	Name         string        `json:"name"`
	Scope        string        `json:"scope,omitempty"` // project or user, on install
	Root         string        `json:"root,omitempty"`  // the project root, on install at project scope
	Digest       string        `json:"digest,omitempty"`
	Targets      []string      `json:"targets"`
	MCPServers   []MCPRun      `json:"mcp_servers"`
	Hooks        []HookRun     `json:"hooks"`
	RuntimeHooks []RuntimeHook `json:"runtime_hooks"`
	Skipped      []string      `json:"skipped"`  // kind/name of each component selections.yaml excludes
	SavedTo      string        `json:"saved_to"` // selections.yaml when this install changed the module's entries
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
	// Targets, when set, are added to the targets the module already has in a scope's manifest
	// (none for a new module), instead of using the scope's targets: lola migrate installs each
	// module on the assistants the Python lola installed it for. A module the manifest already
	// has stays at the version installed there (InstallChoices lists that version too), so
	// adding an assistant never upgrades it.
	Targets []string
	// Version, when set, is the cached version InstallChoices resolved this argument to: the
	// install uses it instead of fetching the source or reading the head again, so it installs
	// exactly what the pickers listed.
	Version *VersionRef
}

// VersionRef names one cached version of a module.
type VersionRef struct {
	Name string `json:"name"`
	ID   string `json:"id"`
}

// StatusRow is one module × target line of `lola list`.
type StatusRow struct {
	Name   string `json:"name"`
	Target string `json:"target"`
	Status string `json:"status"` // ok, modified, missing, stale, skipped, unreadable
	Rules  bool   `json:"rules"`  // false when the manifest entry says rules: false
	Source string `json:"source"`
	Ref    string `json:"ref,omitempty"`
	Digest string `json:"digest"` // the version installed
	Pin    string `json:"pin"`    // the digest the manifest pins; "" when unpinned
	// UpdateAvailable is true when the cache head for the entry's ref holds another version
	// than the pin (the version installed, when unpinned); never for a folder.
	UpdateAvailable bool `json:"update_available"`
	// Skipped lists the components selections.yaml excludes at this scope (kind/name).
	Skipped []string `json:"skipped"`
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
// unlinks a lock file another process is waiting on; the name hashes the directory's
// paths.ProjectKey (the key selections.yaml uses), so every spelling of the project takes the
// same lock.
func (e *Engine) projectLock(lolaDir string) (string, error) {
	key, err := paths.ProjectKey(lolaDir)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(paths.KeyID(key)))
	return filepath.Join(e.Paths.Locks, hex.EncodeToString(sum[:8])+".lock"), nil
}

// FindProject walks up from dir to the nearest directory containing .lola/; if there is
// none, dir itself is the project.
func FindProject(dir string) string {
	d, _ := findProject(dir)
	return d
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

// Ledger loads the scope's ledger: what lola wrote there, and for which module.
func (e *Engine) Ledger(sc Scope) (*ledger.Ledger, error) {
	return ledger.Load(e.files(sc).ledger)
}

// refuseSymlinkedState fails when the project's .lola/, .lola/modules, .lola/plugin-data or
// .lola/project.yml is a symlink: a repository can commit one, and lola would then write,
// rename and delete outside the project. Missing paths are fine. User-scope state is the
// user's own and may link.
func (e *Engine) refuseSymlinkedState(sc Scope) error {
	f := e.files(sc)
	if f.lolaDir == "" {
		return nil
	}
	for _, p := range []string{f.lolaDir, f.modules, f.pluginData, f.manifest} {
		fi, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			if p == f.manifest {
				return errs.WithHints(fmt.Errorf("%s is a symlink, and lola keeps the project manifest in a regular file", p),
					"replace it with a regular file holding the same content, then rerun")
			}
			return errs.WithHints(fmt.Errorf("%s is a symlink, and lola keeps its state in real directories", p),
				"replace it with a directory: rm "+p+", then rerun")
		}
	}
	return nil
}

// ownGitignore writes .lola/.gitignore, which lola owns, at project scope. commit calls it, so
// a refusal or a cancel never writes it into a .lola/ that already existed.
func (e *Engine) ownGitignore(sc Scope) error {
	f := e.files(sc)
	if f.lolaDir == "" {
		return nil
	}
	gi := filepath.Join(f.lolaDir, ".gitignore")
	cur, err := os.ReadFile(gi)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if bytes.Equal(cur, []byte(lolaGitignore)) {
		return nil
	}
	return fsx.WriteFileAtomic(gi, []byte(lolaGitignore), 0o644)
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
		var err error
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

// Fetch copies a source into the store (lola cache add), staging it in a run directory of its own.
// It does not sweep (sweepRuns): it has no notices to report a failed sweep in, and the next
// install, render or cache update sweeps.
func (e *Engine) Fetch(ctx context.Context, spec source.Spec) (_ store.Version, err error) {
	run, err := e.runDir()
	if err != nil {
		return store.Version{}, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(run)) }()
	return e.fetchAs(ctx, spec, "", run)
}

// fetchAs copies a source into the store, staging it in the run directory run. Content must
// declare the module name, when name is set: content declaring another is refused before
// anything is cached (store.PutRequest.Name).
func (e *Engine) fetchAs(ctx context.Context, spec source.Spec, name, run string) (v store.Version, err error) {
	f, err := source.Fetch(ctx, spec, run)
	if err != nil {
		return store.Version{}, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(f.Dir)) }()
	return e.Store.Put(store.PutRequest{Staged: f.Dir, FallbackName: f.Name, Name: name, Source: spec.Location, Type: string(spec.Kind),
		Path: spec.Path, Ref: spec.Ref, Commit: f.Commit, Executables: f.Executables, Now: e.Now()})
}

// canonicalRoot is dir with symbolic links resolved: the root a relative source is recorded
// against and resolved from, so a source recorded through a link to the project resolves again
// (Plan 6a §8). A root a dry run has not created yet is kept lexically, as paths.ProjectKey does.
func canonicalRoot(dir string) string {
	if root, err := filepath.EvalSymlinks(dir); err == nil {
		return root
	}
	return dir
}

// resolve implements §7.1 for this build: a stored name first (no network), then a source.
// have, when it names a module, is the version this request already resolved to (fetched for
// another scope, or pinned by InstallRequest.Version): it is used as is, never fetched again.
// entryRef is the ref the scope's manifest records for a name argument: without --ref the
// name resolves to the cache head for it (Plan 6a §10.1). Fetches stage in the run directory run.
func (e *Engine) resolve(ctx context.Context, sc Scope, cwd string, r InstallRequest, have store.Version, entryRef, run string) (manifest.Entry, store.Version, bool, error) {
	if module.ValidateName("module", r.Arg) == nil {
		if r.Ref == "" {
			r.Ref = entryRef
		}
		o, found, err := e.Store.Origin(r.Arg)
		if err != nil {
			return manifest.Entry{}, store.Version{}, false, err
		}
		if found && r.Version != nil && r.Version.Name != r.Arg {
			return manifest.Entry{}, store.Version{}, false, pinMismatch(r)
		}
		if found {
			if err := e.recordable(sc, r.Arg, source.Recorded{Source: o.Source, Ref: r.Ref, Path: o.Path}, true); err != nil {
				return manifest.Entry{}, store.Version{}, false, err
			}
		}
		if found && have.Name == r.Arg {
			return manifest.Entry{Name: r.Arg, Source: o.Source, Ref: r.Ref, Path: o.Path}, have, false, nil
		}
		if found {
			v, ok, err := e.Store.Head(r.Arg, r.Ref)
			if err != nil {
				return manifest.Entry{}, store.Version{}, false, err
			}
			fetched := false
			if !ok {
				v, err = e.fetchAs(ctx, source.Spec{Kind: source.Kind(o.Type), Location: o.Source, Path: o.Path, Ref: r.Ref}, "", run)
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
	if r.Version != nil {
		o, found, err := e.Store.Origin(r.Version.Name)
		if err != nil {
			return manifest.Entry{}, store.Version{}, false, err
		}
		if !found || !source.SameLocation(spec.Kind, o.Source, spec.Location) || o.Path != spec.Path || o.Type != string(spec.Kind) {
			return manifest.Entry{}, store.Version{}, false, pinMismatch(r)
		}
	}
	src := spec.Location
	// A local path typed relative to the project is recorded that way, relative to the canonical
	// root as the location is canonical (Plan 6a §8); a URL is kept as given.
	if sc.Kind == target.Project && filepath.IsAbs(spec.Location) && !filepath.IsAbs(r.Arg) && !strings.HasPrefix(r.Arg, "file://") {
		if rel, err := filepath.Rel(canonicalRoot(sc.ProjectDir), spec.Location); err == nil {
			src = rel
		}
	}
	if err := e.recordable(sc, r.Arg, source.Recorded{Source: src, Ref: r.Ref, Path: r.Path}, false); err != nil {
		return manifest.Entry{}, store.Version{}, false, err
	}
	v, fetched := have, false
	if v.Name == "" {
		if v, err = e.fetchAs(ctx, spec, "", run); err != nil {
			return manifest.Entry{}, store.Version{}, false, err
		}
		fetched = true
	}
	return manifest.Entry{Name: v.Name, Source: src, Ref: r.Ref, Path: r.Path}, v, fetched, nil
}

// recordable refuses an install whose source, ref or path sc's manifest and ledger could not
// load again (source.Recorded.Check), before anything is fetched or written: recording one
// would leave every later command refusing the scope until the files were edited by hand.
// cached says the source and path are the cache's record of the module arg names, which the
// user fixes by removing it from the cache. The hint fits the first field refused.
func (e *Engine) recordable(sc Scope, arg string, rec source.Recorded, cached bool) error {
	err := rec.Check()
	if err == nil {
		return nil
	}
	var hint string
	switch {
	case cached && (source.Recorded{Source: rec.Source, Path: rec.Path}).Check() != nil:
		hint = "remove it from your cache, then install it again from its source: " + errs.ShellJoin([]string{"lola", "cache", "rm", arg})
	case (source.Recorded{Source: rec.Source}).Check() != nil:
		hint = "use a shorter source, or rename a folder or file whose name holds control or invisible characters, then install it again"
	case (source.Recorded{Source: rec.Source, Ref: rec.Ref}).Check() != nil:
		hint = "for --ref, use a shorter branch or tag name without control or invisible characters, or the full commit SHA"
	default:
		hint = "for --path, give a relative path inside the source without control or invisible characters"
	}
	f := e.files(sc)
	return errs.WithHints(fmt.Errorf("cannot record %s in %s or %s: %w", untrusted.Text(arg, untrusted.MaxName),
		filepath.Base(f.manifest), filepath.Base(f.ledger), err), hint)
}

// pinMismatch refuses a request whose pinned version belongs to another module or source than
// its argument.
func pinMismatch(r InstallRequest) error {
	return errs.WithHints(fmt.Errorf("%s is not the module or source version %s of %s was listed for",
		untrusted.Text(r.Arg, untrusted.MaxName), untrusted.Text(r.Version.ID, untrusted.MaxName), untrusted.Text(r.Version.Name, untrusted.MaxName)),
		"run lola install again to list the current versions")
}

// planModule asks every effective target for the intents and findings of the module's
// components that x does not exclude; ment is its manifest entry (nil when it has none).
func (e *Engine) planModule(sc Scope, v store.Version, targets []string, ment *manifest.Entry, x config.Exclusions, opts Options, probes *probeRun) ([]plan.Intent, []target.Finding, []Notice, ModuleResult, error) {
	r := resolveComponents(v.Module, x, ment.RulesOn())
	m := r.module
	f := e.files(sc)
	pctx := target.PlanContext{Scope: sc.Kind, ContentDir: v.ContentDir, PluginRoot: filepath.Join(f.modules, m.Name),
		PluginData: filepath.Join(f.pluginData, m.Name), SkipRules: !ment.RulesOn(), Force: opts.Force}
	if sc.Kind == target.Project && len(m.RuntimeHooks) > 0 {
		pctx.LocalSettingsExposed = probes.localSettings(sc.ProjectDir)
	}
	mr := ModuleResult{Name: m.Name, Digest: v.Meta.Digest, Targets: []string{}, MCPServers: []MCPRun{}, Hooks: []HookRun{}, RuntimeHooks: []RuntimeHook{},
		Skipped: r.skipped}
	finds := warningFindings(m)
	notes := r.notes
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
// after it is committed. Runtime hooks get consent before anything is planned.
func (e *Engine) execute(ctx context.Context, sc Scope, led *ledger.Ledger, changes []plan.ModuleChange, copies []moduleCopy,
	opts Options, res *Result, after func(*plan.Plan) error) error {
	pr, err := e.prepare(sc, led, changes, copies, nil, opts, res)
	if err != nil || opts.DryRun {
		return err
	}
	return e.commit(ctx, pr, opts, res, nil, after)
}

// prepared is a scope's computed plan whose every refusal and prompt has passed: what commit
// applies.
type prepared struct {
	sc      Scope
	plan    *plan.Plan
	jobs    []*hookJob
	changes []plan.ModuleChange
	copies  []moduleCopy
}

// prepare runs everything that may refuse or ask before a scope is written: runtime hook
// consent, --strict, the plan (conflicts, except on the replaceable destinations) and install
// hook consent. It writes nothing but recorded consents.
func (e *Engine) prepare(sc Scope, led *ledger.Ledger, changes []plan.ModuleChange, copies []moduleCopy, replace []plan.Replaceable,
	opts Options, res *Result) (*prepared, error) {
	if err := e.consentRuntimeHooks(sc, changes, opts, res); err != nil {
		return nil, err
	}
	if opts.Strict {
		if err := StrictError(res.Findings, "fix the module, or rerun without --strict to install what each target supports", true); err != nil {
			return nil, err
		}
	}
	req := plan.Request{Roots: map[string]string{}, BaseDirs: map[string][]string{}, Ledger: led, Changes: changes, Force: opts.Force,
		Replace: replace}
	for _, t := range e.Targets {
		req.Roots[t.ID()] = t.Root(sc.Kind, sc.ProjectDir, e.Paths)
		req.BaseDirs[t.ID()] = t.BaseDirs(sc.Kind)
	}
	p, err := plan.Compute(req)
	if err != nil {
		return nil, err
	}
	for i := range p.Steps {
		p.Steps[i].Scope = sc.Kind.String()
	}
	res.Steps = append(res.Steps, p.Steps...)
	res.Findings = append(res.Findings, driftFindings(p.Steps, e.files(sc).modules)...)
	jobs, err := e.planHooks(sc, copies, opts, res)
	if err != nil {
		return nil, err
	}
	return &prepared{sc: sc, plan: p, jobs: jobs, changes: changes, copies: copies}, nil
}

// commit applies a prepared scope: module copies, pre-install hooks, the plan with the
// manifest edit man (nil: none) under the scope lock, after (when set), then post-install hooks.
func (e *Engine) commit(ctx context.Context, pr *prepared, opts Options, res *Result, man *plan.ManifestWrite, after func(*plan.Plan) error) error {
	sc, p := pr.sc, pr.plan
	if err := e.ownGitignore(sc); err != nil {
		return err
	}
	dir := e.files(sc).modules
	perm := fs.FileMode(0o755)
	if sc.Kind == target.User {
		perm = 0o700
	}
	sw, err := swapCopies(dir, pr.copies, perm)
	if err != nil {
		return err
	}
	if err := e.runHooks(ctx, sc, pr.jobs, PreInstall, opts, res); err != nil {
		return errors.Join(err, sw.rollback())
	}
	committed := false
	err = plan.Apply(p, e.files(sc).ledger, man, func() error {
		committed = true
		if after == nil {
			return nil
		}
		return after(p)
	})
	if !committed {
		return errors.Join(err, sw.rollback())
	}
	errs := []error{err, sw.commit()}
	for _, c := range pr.changes {
		if p.Ledger.Modules[c.Name] == nil {
			errs = append(errs, os.RemoveAll(filepath.Join(dir, c.Name)))
		}
	}
	errs = append(errs, e.runHooks(ctx, sc, pr.jobs, PostInstall, opts, res))
	return errors.Join(errs...)
}
