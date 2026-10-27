// Package engine orchestrates one lola operation over a scope: resolve and fetch modules into
// the store, ask targets for intents, compute and apply the plan, and update the manifest.
// It has no CLI concerns, so every flow is testable without cobra.
package engine

import (
	"bytes"
	"cmp"
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
	"github.com/trevor-vaughan/lola-go/internal/templating"
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
// version probes inherit; Getwd is the working directory hints are written for (Plan 6a
// §11.3); HookTimeout bounds each hook run (§9.1) and ProbeTimeout each assistant version
// probe.
type Engine struct {
	Paths        paths.Paths
	Store        *store.Store
	Consent      *consent.Store
	Trust        *trust.Store
	Targets      []target.Target
	Now          func() time.Time
	Environ      func() []string
	Getwd        func() (string, error)
	HookTimeout  time.Duration
	ProbeTimeout time.Duration
	// TemplateLimits caps each template render and TemplateBudget each plan pass's rendering
	// (Plan 7 §5.4), timed with Now; specs lower them.
	TemplateLimits templating.Limits
	TemplateBudget TemplateBudget
	// staged is the remote profiles an update in a batch keeps until the command's receipt is
	// answered (CommitStaged), by path; the layers of the command's syncs read them in place.
	staged map[string]stagedRemote
}

// New returns an Engine over the real store and every registered target.
func New(p paths.Paths) *Engine {
	return &Engine{Paths: p, Store: store.New(p, targets.IDs()), Consent: consent.New(p.Consent), Trust: trust.New(p), Targets: targets.All(), Now: time.Now,
		Environ: os.Environ, Getwd: os.Getwd, HookTimeout: 10 * time.Minute, ProbeTimeout: 5 * time.Second, TemplateLimits: templating.DefaultLimits(),
		TemplateBudget: TemplateBudget{RunOutput: 32 << 20, RunTime: 20 * time.Second}}
}

// Options apply to install, uninstall and render.
type Options struct {
	Targets []string // explicit target selection; empty means the manifest's targets
	DryRun  bool
	Force   bool  // overrides forceable conflicts and renders forceable skipped artifacts
	Strict  bool  // fail before writing anything when a warn or skip finding remains
	Rules   *bool // install only: false records rules: false, true clears it, nil keeps each entry's setting
	// ForceRun is the user's run with --force, which a refusal of conflicts --force overrides
	// hints; "" for no hint.
	ForceRun string
	// named is the modules the run's command line named (an install's), whose conflicts with
	// another installed module's destinations hint that module's uninstall.
	named []string
	// freshPin says the entry planned was pinned by this run, a fresh install's, so lola check of
	// its address fetches the version refused; no check reaches any other pin.
	freshPin bool
	// entryFrom is the layer the entry planned comes from, "" for the scope's manifest, which a
	// refusal of its vars names (checkVars).
	entryFrom string
	// askScripts says the run is a sync, which asks about its project's install scripts even when
	// it writes no copy for them to wrap: a pulled project.yml that adds or changes one asks (Plan
	// 9 §7).
	askScripts bool

	// Trust is the kinds of trust the run's --trust grants (Plan 9 §3.4): an item of a kind it
	// covers is not asked about but shown once to Receipt, which, when set, is called before the
	// first write and may answer No (ErrDeclined); the grants are recorded only after it.
	Trust   TrustKinds
	Receipt func([]Consent) (bool, error)
	// Rerun is the user's run with the --trust argument for the kinds given (TrustKinds.Arg), which
	// a refusal for want of trust hints; nil gives no such hint.
	Rerun func(TrustKinds) string
	// Batch, when set, takes the run's covered items and grants at its receipt instead, which then
	// stops the run with ErrBatched: the caller asks one receipt for several runs (TrustBatch).
	Batch *TrustBatch
	// Expect, when set, is the batch whose one receipt the user answered: the run asks no receipt,
	// and records its grants just before its own apply only when each is one the batch holds, by
	// key and digest, and each entries or profiles item one it showed; else it is ErrTrustChanged,
	// with nothing of it written (R-9-16).
	Expect *TrustBatch

	// Install hooks (§9.1). Ask, when set, asks the user about hooks without a recorded consent
	// that Trust does not cover; a yes is recorded. HookOutput receives the hooks' stdout and
	// stderr.
	Ask        func(module string, hooks []HookScript) (bool, error)
	HookOutput io.Writer
	// AskScript, when set, asks the user to trust a project's install script without a recorded
	// consent that Trust does not cover (Plan 9 §5.4); a yes is recorded once the run's receipt is
	// answered yes (A23). Run is the user's
	// command, "lola install", which a refusal for want of that trust names.
	AskScript func(ScriptQuestion) (bool, error)
	Run       string

	// Runtime hooks (Plan 5b design §7.3), which an assistant runs on its own events.
	// AskRuntimeHooks, when set, asks the user about hooks without a recorded consent that Trust
	// does not cover, and a yes is recorded.
	AskRuntimeHooks func(module string, hooks []RuntimeHook) (bool, error)

	// PreInstall and PostInstall are install's --pre-install and --post-install scripts (Plan 9
	// §5.1), which wrap every module copy the run installs or replaces; Given "" for none.
	PreInstall, PostInstall FlagScript

	// Display is the run's output flags (--json, --plain, --no-pager as given), which every
	// command a marketplace hint or notice names keeps (Plan 6c §11.3), as does the uninstall a
	// conflict over another module's destination hints.
	Display []string
	// ShowResolved, when set, is given install's resolved marketplace addresses, one line each
	// ("@acme/formatter → <source> path <path> @ <ref>"), before any consent is asked (Plan 6c
	// §6.3, ruling R-6c-15).
	ShowResolved func(lines []string)
}

// MCPRun says what an MCP server runs: the stdio command line or the remote URL.
type MCPRun struct {
	Name      string `json:"name"`
	Transport string `json:"transport"`
	Runs      string `json:"runs"`
	// name is the server's name as the module gives it, and argv and url what it runs as Runs
	// shows it, expanded and uncleaned: lola info shows them per assistant.
	name string
	argv []string
	url  string
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
	// Market is the marketplace an install through @<market>/<entry> records (Plan 6c §7),
	// through untrusted.Text; absent otherwise.
	Market *manifest.MarketRef `json:"market,omitempty"`

	renderedWith *ledger.RenderedWith // what its templates rendered with, which lock.json records
	// hooksKept says an install without consent keeps the module's copy and runs none of its
	// hooks: the copy holds the version already and no post-install hook is pending.
	hooksKept bool
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
	// Scripts is each install script for each module and target, in run order (Plan 9 §5.6).
	Scripts []ScriptRun `json:"scripts"`
	DryRun  bool        `json:"dry_run"`
	// Trusted is what the run's --trust covers (Plan 9 §3.4, §8), and under -n what it would:
	// [] when nothing, and nil, so absent from the JSON, for a command that takes no --trust.
	Trusted []Consent `json:"trusted,omitzero"`
	// remedies are the hints of the modules render left out, in module order, which a --strict
	// refusal names instead of its generic advice (Plan 6a §10.3).
	remedies []string
	// grants are the consent.json records Trusted's items give, written after the receipt; needs
	// is the kinds of trust the run met an item of that would be refused without it (R-9-6).
	grants []grant
	needs  TrustKinds
	// recorded is the items whose grants confirmTrusted wrote, which a later failure carries.
	recorded []Consent
}

func newResult(dry bool) *Result {
	return &Result{Modules: []ModuleResult{}, Steps: []plan.Step{}, Findings: []target.Finding{}, Notices: []Notice{}, Scripts: []ScriptRun{}, DryRun: dry}
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
	// market is the catalog entry an @<market>/<entry> argument resolved to (Plan 6c §6.3), set
	// by the engine under the markets lock before any scope is locked.
	market *marketPick
}

// VersionRef names one cached version of a module. commit is the commit a marketplace entry's
// version, or the git version lola info showed, was fetched at, which the cache's heads may not
// record: a catalog's commit pin never moves one (Plan 6c §6.4), and info's install pins the
// commit in place of the ref it fetched (Plan 9 §4.3).
type VersionRef struct {
	Name   string `json:"name"`
	ID     string `json:"id"`
	commit string
}

// StatusRow is one module × target line of `lola list`.
type StatusRow struct {
	Scope string `json:"scope"` // project or user
	Name  string `json:"name"`
	// From is the layer the module comes from (Plan 6b §7): project.yml, user.yml,
	// profile:user:go or profile:default.
	From   string `json:"from"`
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
	// Market is the marketplace the entry says it was installed through, as written (Plan 6c §7),
	// through untrusted.Text; absent when it names none.
	Market *manifest.MarketRef `json:"market,omitempty"`
}

// shownMarket is m with its name and source through untrusted.Text, nil for none: a file's
// provenance, as output shows it (Plan 6c §8).
func shownMarket(m *manifest.MarketRef) *manifest.MarketRef {
	if m == nil {
		return nil
	}
	return &manifest.MarketRef{Name: shown(m.Name), Source: shown(m.Source)}
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

// CheckTargets refuses the first id that names no target, as install, check and render do, so a
// command that only filters by target (lola list) coaches the same way.
func (e *Engine) CheckTargets(ids []string) error {
	for _, id := range ids {
		if _, err := e.target(id); err != nil {
			return err
		}
	}
	return nil
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
	if sc.Kind == target.User {
		return manifest.LoadUser(e.files(sc).manifest)
	}
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
	return e.fetchAs(ctx, spec, "", "", run)
}

// fetchAs copies a source into the store, staging it in the run directory run. Content must
// declare the module name, when name is set: content declaring another is refused before
// anything is cached (store.PutRequest.Name). Content that declares no name is named fallback,
// else name, else after its source (the repository, archive or folder): a module lola already
// knows by a name keeps it, as a catalog entry's does (Plan 6c §6.3).
func (e *Engine) fetchAs(ctx context.Context, spec source.Spec, name, fallback, run string) (v store.Version, err error) {
	f, err := source.Fetch(ctx, spec, run)
	if err != nil {
		return store.Version{}, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(f.Dir)) }()
	// A source the user names takes the place of a local one the cache records that no longer
	// exists (Plan 6a §6.5); Commit checks that under the cache lock.
	return e.Store.Put(store.PutRequest{Staged: f.Dir, FallbackName: cmp.Or(fallback, name, f.Name), Name: name, Source: spec.Location, Type: string(spec.Kind),
		Components: spec.Components,
		Path:       spec.Path, Ref: spec.Ref, Commit: f.Commit, Executables: f.Executables, Now: e.Now(), ReplaceGoneOrigin: true})
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

// resolve implements §7.1 for this build: a marketplace address (resolveEntry), a stored name
// (no network), then a source.
// have, when it names a module, is the version this request already resolved to (fetched for
// another scope, or pinned by InstallRequest.Version): it is used as is, never fetched again.
// entryRef is the ref the scope's manifest records for a name argument: without --ref the
// name resolves to the cache head for it (Plan 6a §10.1). A full-SHA ref, either one, is recorded
// lower-cased (R-9-31). Fetches stage in the run directory run.
// display is the run's output flags, which the `lola cache list` hint of the not-a-source
// refusal keeps.
func (e *Engine) resolve(ctx context.Context, sc Scope, cwd string, r InstallRequest, have store.Version, entryRef, run string,
	display []string) (manifest.Entry, store.Version, bool, error) {
	r.Ref, entryRef = source.RecordedRef(r.Ref), source.RecordedRef(entryRef)
	if r.market != nil {
		return e.resolveEntry(ctx, sc, r, have, run)
	}
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
			return manifest.Entry{Name: r.Arg, Source: o.Source, Ref: r.Ref, Path: o.Path, Components: o.Components}, have, false, nil
		}
		if found {
			v, ok, err := e.Store.Head(r.Arg, r.Ref)
			if err != nil {
				return manifest.Entry{}, store.Version{}, false, err
			}
			fetched := false
			if !ok {
				v, err = e.fetchAs(ctx, source.Spec{Kind: source.Kind(o.Type), Location: o.Source, Path: o.Path, Ref: r.Ref, Components: o.Components}, "", r.Arg, run)
				if err != nil {
					return manifest.Entry{}, store.Version{}, false, err
				}
				fetched = true
			}
			return manifest.Entry{Name: r.Arg, Source: o.Source, Ref: r.Ref, Path: o.Path, Components: o.Components}, v, fetched, nil
		}
	}
	spec, ok, err := source.Parse(r.Arg, cwd)
	if err != nil {
		return manifest.Entry{}, store.Version{}, false, err
	}
	if !ok {
		return manifest.Entry{}, store.Version{}, false, notASource(r.Arg, display)
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
		if v, err = e.cachedCommit(spec, run); err != nil {
			return manifest.Entry{}, store.Version{}, false, err
		}
	}
	if v.Name == "" {
		if v, err = e.fetchAs(ctx, spec, "", "", run); err != nil {
			return manifest.Entry{}, store.Version{}, false, err
		}
		fetched = true
	}
	return manifest.Entry{Name: v.Name, Source: src, Ref: r.Ref, Path: r.Path}, v, fetched, nil
}

// cachedCommit is the cached version of the git source spec at spec.Ref when that is a full commit
// SHA in any case (Plan 9 R-9-21, R-9-27): a module the cache holds from exactly that location, path and component
// selection, whose version records that commit as the one its fetch resolved. Its content is
// hashed again into the run directory run before it is used (store.Pinned). A version that is
// not cached, or any other ref, is the zero Version: the source is fetched.
func (e *Engine) cachedCommit(spec source.Spec, run string) (store.Version, error) {
	commit, ok := source.CommitRef(spec.Ref)
	if spec.Kind != source.KindGit || !ok {
		return store.Version{}, nil
	}
	names, err := e.Store.Names()
	if err != nil {
		return store.Version{}, err
	}
	for _, name := range names {
		o, found, err := e.Store.Origin(name)
		if err != nil || !found || o.Type != string(source.KindGit) || o.Source != spec.Location || o.Path != spec.Path ||
			!o.Components.Equal(spec.Components) {
			continue // an index lola cannot read is no version of this source
		}
		digest, err := e.Store.AtCommit(name, commit)
		if err != nil {
			return store.Version{}, err
		}
		if digest == "" {
			continue // another name of this source, cached at other commits
		}
		v, found, err := e.Store.Pinned(name, digest, run)
		if err != nil || !found {
			return store.Version{}, err
		}
		v.Commit = commit
		return v, nil
	}
	return store.Version{}, nil
}

// notASource refuses arg, an argument of install or info that is neither a module in the cache
// nor a source, hinting the cache's listing with the run's output flags (display).
func notASource(arg string, display []string) error {
	return errs.WithHints(fmt.Errorf("%q is neither a module in your cache nor a source; sources are %s", arg, source.AcceptedForms),
		"cached modules: "+errs.ShellJoin(append([]string{"lola", "cache", "list"}, display...)))
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
func (e *Engine) planModule(sc Scope, v store.Version, targets []string, ment *manifest.Entry, x config.Exclusions, opts Options, probes *probeRun,
	run *templateRun) ([]plan.Intent, []target.Finding, []Notice, ModuleResult, error) {
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
	if err := e.checkVars(sc, m, ment, opts.entryFrom, v.Meta.Digest, opts.Display); err != nil {
		return nil, nil, nil, mr, err
	}
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
		rendered, err := e.renderTemplates(probes.ctx, m, run.data(id, sc, resolvedVars(m, ment)), run)
		if err != nil {
			if h := e.checkHint(sc, err, ment, opts.Targets, opts.Display, opts.freshPin); h != "" {
				err = errs.WithHints(err, h)
			}
			return nil, nil, nil, mr, err
		}
		ins, fs, err := t.Plan(rendered, pctx)
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
		url := target.Expand(s.URL, pctx)
		runs := url
		var argv []string
		if s.Transport == module.TransportStdio {
			// Each argument quoted unless plain, so where one ends stays visible, as consent shows argv.
			words := make([]string, 0, len(s.Args)+1)
			for _, w := range append([]string{s.Command}, s.Args...) {
				argv = append(argv, target.Expand(w, pctx))
				words = append(words, word(argv[len(argv)-1]))
			}
			runs, url = strings.Join(words, " "), ""
		}
		mr.MCPServers = append(mr.MCPServers, MCPRun{Name: shown(s.Name), Transport: shown(s.Transport), Runs: shown(runs), name: s.Name, argv: argv, url: url})
	}
	var overrides map[string]any
	if ment != nil {
		overrides = ment.Vars
	}
	mr.renderedWith = renderedWith(m, run, overrides)
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
	return strictError(finds, rerun, offerForce, nil)
}

// strictError is StrictError where render left modules out: remedies, their own hints, come
// first, deduplicated, and the caller's advice follows only when another finding remains.
func strictError(finds []target.Finding, rerun string, offerForce bool, remedies []string) error {
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
	var hints []string
	for _, h := range remedies {
		if !slices.Contains(hints, h) {
			hints = append(hints, h)
		}
	}
	if len(remedies) == 0 || slices.ContainsFunc(bad, func(f target.Finding) bool { return !slices.Contains(leftOutCodes, f.Code) }) {
		hints = append(hints, rerun)
		if offerForce && slices.ContainsFunc(bad, func(f target.Finding) bool { return f.Forceable }) {
			hints = append(hints, "--force renders skipped artifacts that can be rendered anyway")
		}
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
	if err != nil {
		return err
	}
	if err := e.confirmTrusted(opts, res); err != nil || opts.DryRun {
		return err
	}
	return res.afterGrants(e.commit(ctx, pr, opts, res, nil, after))
}

// prepared is a scope's computed plan whose every refusal and prompt has passed: what commit
// applies.
type prepared struct {
	sc      Scope
	plan    *plan.Plan
	jobs    []*hookJob
	scripts scriptPlan // the install scripts that wrap jobs, and the pending ones that run alone
	changes []plan.ModuleChange
	copies  []moduleCopy
}

// ownedConflicts is err, the refusal of ce, saying what frees each destination another installed
// module owns (ruling R-6cux-7): uninstalling the owner, hinted, when the run's own command line
// named the module that wants it (opts.named); else removing that module from the file that
// lists it, a project.yml or a profile, which must never steer you into removing a module you
// installed.
func (e *Engine) ownedConflicts(sc Scope, ce *plan.ConflictError, err error, changes []plan.ModuleChange, opts Options) error {
	var owners []string
	for i, c := range ce.Conflicts {
		switch {
		case c.Owner == "":
		case slices.Contains(opts.named, c.Claimant):
			ce.Conflicts[i].Reason += ": uninstall " + c.Owner + " first"
			if !slices.Contains(owners, c.Owner) {
				owners = append(owners, c.Owner)
			}
		default:
			from := filepath.Base(e.files(sc).manifest)
			if k := slices.IndexFunc(changes, func(m plan.ModuleChange) bool { return m.Name == c.Claimant }); k >= 0 && changes[k].Entry != nil &&
				changes[k].Entry.From != "" {
				from = changes[k].Entry.From
			}
			ce.Conflicts[i].Reason += ": remove " + c.Claimant + " from " + shown(from)
		}
	}
	if len(owners) == 0 {
		return err
	}
	return errs.WithHints(err, e.Command(sc, "uninstall", append(owners, opts.Display...)...))
}

// prepare runs everything that may refuse or ask before a scope is written: runtime hook
// consent, --strict, the plan (conflicts, except on the replaceable destinations) and install
// hook consent. It writes nothing but recorded consents.
func (e *Engine) prepare(sc Scope, led *ledger.Ledger, changes []plan.ModuleChange, copies []moduleCopy, replace []plan.Replaceable,
	opts Options, res *Result) (*prepared, error) {
	// Install scripts are checked before anything is asked (Plan 9 §5.5), when a copy may be
	// installed or replaced for them to wrap; planScripts plans them once hooks decided which.
	// A sync that writes no copy asks only about its project's scripts, and a pending one runs alone:
	// your own and the flag's wrap nothing, so nothing checks them (I2, RR3).
	var all []installScript
	read := true
	switch {
	case slices.ContainsFunc(copies, func(c moduleCopy) bool { return c.runHooks }):
		var err error
		if all, err = e.installScripts(sc, opts); err != nil {
			return nil, err
		}
	case opts.askScripts && sc.Kind == target.Project || slices.ContainsFunc(copies, func(c moduleCopy) bool { return c.pendingScript }):
		ps, err := e.projectScripts(sc)
		if err == nil {
			all, err = runOrder(ps)
		}
		if err != nil {
			return nil, err
		}
	default:
		read = false
	}
	// A pending project post-install script whose key project.yml no longer has is pending no more.
	postKept := !read || slices.ContainsFunc(all, func(s installScript) bool { return s.source == ScriptProject && s.event == PostInstall })
	unasked, err := e.unaskedKinds(copies, all, opts)
	if err != nil {
		return nil, err
	}
	if err := e.consentRuntimeHooks(sc, changes, opts, unasked, res); err != nil {
		return nil, err
	}
	if opts.Strict {
		if err := strictError(res.Findings, "fix the module, or rerun without --strict to install what each target supports", true, res.remedies); err != nil {
			return nil, err
		}
	}
	req := plan.Request{Roots: map[string]string{}, BaseDirs: map[string][]string{}, Ledger: led, Changes: changes, Force: opts.Force,
		ForceRun: opts.ForceRun, Replace: replace}
	for _, t := range e.Targets {
		req.Roots[t.ID()] = t.Root(sc.Kind, sc.ProjectDir, e.Paths)
		req.BaseDirs[t.ID()] = t.BaseDirs(sc.Kind)
	}
	p, err := plan.Compute(req)
	if ce, ok := errors.AsType[*plan.ConflictError](err); ok {
		return nil, e.ownedConflicts(sc, ce, err, changes, opts)
	}
	if err != nil {
		return nil, err
	}
	for i := range p.Steps {
		p.Steps[i].Scope = sc.Kind.String()
	}
	res.Steps = append(res.Steps, p.Steps...)
	res.Findings = append(res.Findings, driftFindings(p.Steps, e.files(sc).modules)...)
	jobs, err := e.planHooks(sc, copies, opts, unasked, res)
	if err != nil {
		return nil, err
	}
	sp, err := e.planScripts(sc, all, jobs, copies, opts, res)
	if err != nil {
		return nil, err
	}
	// A post-install hook declined for want of consent stays pending in lock.json until it runs
	// (Plan 6a §9.3 step 10); a module whose hooks this run does not reach, or whose forced copy
	// is kept, keeps its mark.
	for _, c := range copies {
		entry := p.Ledger.Modules[c.name]
		if entry == nil {
			continue
		}
		ran := slices.ContainsFunc(jobs, func(j *hookJob) bool { return j.name == c.name && len(j.scripts) > 0 })
		post := declaresPostInstall(c.hooks)
		kept := !c.runHooks || c.forced && !ran
		declined := !kept && !ran && post
		entry.HooksPending = declined || kept && post && led.Modules[c.name] != nil && led.Modules[c.name].HooksPending
		// So does the project's post-install script declined for want of trust (Plan 9 §5.5),
		// on a copy it would have wrapped; a copy nothing wraps keeps its mark, which a pending
		// script that runs alone clears only once it has run (commit).
		wrapped := slices.ContainsFunc(jobs, func(j *hookJob) bool { return j.name == c.name })
		entry.ScriptsPending = wrapped && sp.declinedPost || !wrapped && postKept && led.Modules[c.name] != nil && led.Modules[c.name].ScriptsPending
	}
	return &prepared{sc: sc, plan: p, jobs: jobs, scripts: sp, changes: changes, copies: copies}, nil
}

// commit applies a prepared scope: module copies, pre-install scripts and hooks, the plan with
// the manifest edits man (none when empty) under the scope lock, after (when set), then
// post-install hooks and scripts, and the pending post-install scripts that run alone.
func (e *Engine) commit(ctx context.Context, pr *prepared, opts Options, res *Result, man []plan.ManifestWrite, after func(*plan.Plan) error) error {
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
	if err := e.runHooks(ctx, sc, pr.jobs, pr.scripts.run, PreInstall, opts, res); err != nil {
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
	errs = append(errs, e.runHooks(ctx, sc, pr.jobs, pr.scripts.run, PostInstall, opts, res))
	// A pending script that ran alone on every target is pending no more; one that failed or
	// changed stays pending, so the next sync naming its module offers it again.
	cleared := false
	for _, j := range pr.scripts.alone {
		from := len(res.Scripts)
		errs = append(errs, e.runScripts(ctx, sc, j, pr.scripts.pending, PostInstall, opts, res))
		if !slices.ContainsFunc(res.Scripts[from:], func(s ScriptRun) bool { return s.Status != HookRan }) {
			p.Ledger.Modules[j.name].ScriptsPending, cleared = false, true
		}
	}
	if cleared {
		errs = append(errs, p.Ledger.Save(e.files(sc).ledger))
	}
	return errors.Join(errs...)
}
