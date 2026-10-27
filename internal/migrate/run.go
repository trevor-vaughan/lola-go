package migrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/fsx"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/trash"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Options configure a migration (design §7). The engine options carry --dry-run, --force and
// hook consent; --force also moves and replaces paths git tracks. KeepPython is --keep-python:
// install (still replacing with backup) but move nothing else to the trash.
type Options struct {
	engine.Options
	KeepPython bool
}

// keptByKeepPython is the reason a path --keep-python keeps is listed with.
const keptByKeepPython = "--keep-python keeps what lola-go did not replace"

// Destination statuses.
const (
	StatusPlanned   = "planned"
	StatusMigrated  = "migrated"
	StatusUnchanged = "unchanged" // nothing was installed, replaced or moved
	StatusFailed    = "failed"
)

// Python home actions.
const (
	HomeMove = "move"
	HomeKeep = "keep"
	HomeNone = "none"
)

// Result is what lola migrate did or, on a dry run, would do.
type Result struct {
	DryRun       bool          `json:"dry_run"`
	Nothing      bool          `json:"nothing_to_migrate"`
	PythonHome   string        `json:"python_home"`
	Trash        string        `json:"trash"`
	Destinations []*DestResult `json:"destinations"`
	Unmappable   []Unmapped    `json:"unmappable"`
	NotMigrated  []Item        `json:"not_migrated"`
	Markets      []Market      `json:"markets"`
	Home         HomeResult    `json:"home"`
	Warnings     []string      `json:"warnings"`
	// Trusted is what the run's --trust covered in every destination, each item once (Plan 9 §8):
	// under -n, what it would cover.
	Trusted []engine.Consent `json:"trusted"`
}

// DestResult is one destination of a Result: what lola-go installs, which recorded paths it
// replaced (backed up first) or moved to the trash, what it kept and why, and what is never
// migrated. Steps, modules, findings and notices are the install's.
type DestResult struct {
	ID          string                `json:"id"`
	Scope       string                `json:"scope"`
	Root        string                `json:"root,omitempty"`
	Status      string                `json:"status"`
	Error       string                `json:"error,omitempty"`
	Hints       []string              `json:"hints"`
	Installs    []InstallItem         `json:"installs"`
	Replaced    []Moved               `json:"replaced"`
	Moved       []Moved               `json:"moved"`
	Kept        []Kept                `json:"kept"`
	Unmappable  []Unmapped            `json:"unmappable"`
	NotMigrated []Item                `json:"not_migrated"`
	Steps       []plan.Step           `json:"steps"`
	Modules     []engine.ModuleResult `json:"modules"`
	Findings    []target.Finding      `json:"findings"`
	Notices     []engine.Notice       `json:"notices"`
}

// InstallItem is one module lola-go installs (or found installed) at a destination.
type InstallItem struct {
	Module        string   `json:"module"`
	PythonModules []string `json:"python_modules"`
	Source        string   `json:"source"`
	Ref           string   `json:"ref,omitempty"`
	Path          string   `json:"path,omitempty"`
	Targets       []string `json:"targets"`
	Status        string   `json:"status"` // install or already-migrated
}

// Moved is a recorded path replaced by lola-go or moved to the trash. Trash is "" on a dry run.
type Moved struct {
	Path  string `json:"path"`
	Key   string `json:"key,omitempty"`
	Kind  string `json:"kind"`
	Trash string `json:"trash"`
}

// Kept is a recorded path lola migrate leaves in place, with the reason.
type Kept struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// HomeResult says what happens to the Python lola's home.
type HomeResult struct {
	Path   string `json:"path"`
	Action string `json:"action"`
	Reason string `json:"reason,omitempty"`
	Trash  string `json:"trash,omitempty"`
}

// Install statuses.
const (
	InstallNew     = "install"
	InstallAlready = "already-migrated"
)

// action is what happens to a recorded path.
type action int

const (
	actNone    action = iota // it is gone
	actReplace               // lola-go writes it; backed up first
	actMove                  // moved to the trash
	actKeep                  // left in place, with a reason
	actForced                // --force writes it past a guard: backed up and reported through its forced steps
)

// decision is a recorded path's fate at one destination.
type decision struct {
	rec    Recorded
	act    action
	reason string
	backup string // trash path of its backup, once copied
}

// runner is one migration.
type runner struct {
	ctx   context.Context
	e     *engine.Engine
	st    *State
	opts  Options
	trash *trash.Trash
	// holds names the MCP entries left in place that reference a path (a module copy, the
	// Python home): such a path stays (§12.5).
	holds map[string][]string
	// written is what the destinations' installs recorded of what --trust covers, each item once.
	written []engine.Consent
	// blind is set when an MCP settings file could not be read: it may reference anything.
	blind string
}

// Migrate previews the migration p plans and, unless opts.DryRun, carries it out (design §6).
// The same plan serves a preview and the run it confirms: its installs are pinned to the
// versions it resolved. Every destination is planned first, then every hook consent is asked,
// so a refusal (a conflict outside the replaceable paths, a hook without consent) or a
// cancelled prompt writes nothing anywhere. Then each destination is migrated in turn: a failure stops that destination before
// it writes and the others go on; the Python home moves last, only when everything migrated.
// The error lists what failed; the result says what happened everywhere.
func Migrate(ctx context.Context, e *engine.Engine, p *Plan, opts Options) (*Result, error) {
	st := p.st
	r := &runner{ctx: ctx, e: e, st: st, opts: opts, holds: map[string][]string{}}
	rep := &Result{DryRun: true, PythonHome: st.Home, Destinations: []*DestResult{}, Unmappable: p.Unmappable,
		NotMigrated: p.NotMigrated, Markets: marketReport(p.Markets, opts.Display), Warnings: p.Warnings, Home: HomeResult{Path: st.Home, Action: HomeNone},
		Trusted: []engine.Consent{}}
	dry := opts
	dry.DryRun = true
	results := make([]*engine.InstallResult, len(p.Destinations))
	leds := make([]*ledger.Ledger, len(p.Destinations))
	var err error
	for i, d := range p.Destinations {
		if leds[i], results[i], err = r.previewInstall(d, dry); err != nil {
			return nil, fmt.Errorf("%s: %w", where(d), r.coachClash(d, leds[i], err))
		}
		if results[i] != nil {
			rep.Trusted = addTrusted(rep.Trusted, results[i].Trusted)
		}
	}
	if err := r.collectHolds(p, results, leds); err != nil {
		return nil, err
	}
	for i, d := range p.Destinations {
		dr, err := r.preview(d, results[i], leds[i])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", where(d), err)
		}
		rep.Destinations = append(rep.Destinations, dr)
	}
	rep.Home = r.home(p, rep)
	rep.Nothing = nothing(rep)
	if opts.DryRun || rep.Nothing {
		return rep, nil
	}
	if err = r.consent(p); err != nil {
		return nil, err
	}
	// From here a failure carries what the destinations recorded (R-9-16), [] before any did.
	r.written, rep.Trusted = []engine.Consent{}, []engine.Consent{}
	if r.trash, err = trash.OpenTrash(e.Paths.Trash, e.Now()); err != nil {
		return nil, &engine.TrustedError{Err: err, Trusted: r.written}
	}
	rep.DryRun, rep.Trash = false, r.trash.Dir
	var failed []string
	for i, d := range p.Destinations {
		dr := newDestResult(d)
		if err := r.migrate(d, dr); err != nil {
			dr.Status, dr.Error, dr.Hints = StatusFailed, err.Error(), append([]string{}, errs.Hints(err)...)
			failed = append(failed, where(d))
		}
		rep.Destinations[i] = dr
	}
	rep.Trusted = r.written
	rep.Markets = r.registerMarkets(p.Markets)
	rep.Home = r.home(p, rep)
	if rep.Home.Action == HomeMove {
		to, err := r.move("python-home", filepath.Dir(st.Home), st.Home)
		if err != nil {
			rep.Home = HomeResult{Path: st.Home, Action: HomeKeep, Reason: "moving it failed: " + untrusted.CleanErr(err).Error()}
			if _, ok := errors.AsType[*trash.ChangedError](err); ok {
				// Part of the home is gone from its place already: say where its copy is.
				rep.Home.Trash = to
				rep.Home.Reason = "moved in part (its copy is in " + untrusted.Text(to, untrusted.MaxName) + "): " + untrusted.CleanErr(err).Error()
			}
			failed = append(failed, "the Python home")
		} else {
			rep.Home.Trash = to
		}
	}
	closeErr := r.trash.Close()
	if !r.trash.Used() {
		rep.Trash = ""
	}
	if len(failed) == 0 && closeErr == nil {
		return rep, nil
	}
	hints := []string{"fix the errors the report shows and rerun lola migrate; what migrated is not done again"}
	var why []string
	if len(failed) > 0 {
		why = append(why, strings.Join(failed, "; ")+" failed")
	}
	if closeErr != nil {
		why = append(why, "closing the trash failed: "+untrusted.CleanErr(closeErr).Error())
		hints = append(hints, "the trash is "+untrusted.Text(r.trash.Dir, untrusted.MaxName)+"; its manifest.txt lists everything lola backed up or moved there")
	}
	return rep, &engine.TrustedError{Err: errs.WithHints(fmt.Errorf("lola migrate did not finish: %s", strings.Join(why, "; ")), hints...),
		Trusted: rep.Trusted}
}

// consent runs every destination's install up to its receipt, so the install and runtime hook
// prompts are all answered before anything is written, and collects what --trust covers in every
// destination, with the yes answers, for one receipt (Plan 9 §3.4, A23), shown once all of them
// were asked; its No is engine.ErrDeclined. Nothing is recorded here: each destination's install
// records its own grants just before it writes, when each is one the receipt showed by key and
// digest, else it fails with engine.ErrTrustChanged, writing nothing (R-9-16, S-M4). The installs
// that follow ask nothing: what was declined here stays declined.
func (r *runner) consent(p *Plan) error {
	batch := &engine.TrustBatch{}
	for _, d := range p.Destinations {
		if len(d.Installs) == 0 {
			continue
		}
		led, err := r.e.Ledger(d.Scope)
		if err != nil {
			return err
		}
		spec, err := r.installSpec(d, led, r.opts.Options)
		if err != nil {
			return err
		}
		spec.Options.Batch = batch
		if _, err := r.e.InstallAll(r.ctx, spec); err != nil && !errors.Is(err, engine.ErrBatched) {
			return fmt.Errorf("%s: %w", where(d), err)
		}
	}
	if len(batch.Items) > 0 && r.opts.Receipt != nil {
		ok, err := r.opts.Receipt(slices.Clone(batch.Items))
		if err != nil {
			return err
		}
		if !ok {
			return engine.ErrDeclined
		}
	}
	r.opts.Ask, r.opts.AskRuntimeHooks, r.opts.Receipt, r.opts.Expect = nil, nil, nil, batch
	return nil
}

// addTrusted is to with each of items it does not hold yet: a module's hooks consent covers it in
// every destination, so the receipt lists it once.
func addTrusted(to, items []engine.Consent) []engine.Consent {
	for _, c := range items {
		if !slices.Contains(to, c) {
			to = append(to, c)
		}
	}
	return to
}

// coachClash is the one place a refused preview of d's install is coached; led is d's ledger.
// A conflict on a path d's records list that a guard kept out of the replaceable paths is named
// with the guard (guardedConflicts); when every conflict is one, the engine's general hints
// give way to the guards'. A clash between two of d's modules is coached too (clashes).
func (r *runner) coachClash(d *Destination, led *ledger.Ledger, err error) error {
	var ce *plan.ConflictError
	if !errors.As(err, &ce) {
		return err
	}
	msgs, hints, all := r.guardedConflicts(d, led, ce.Conflicts)
	if slices.ContainsFunc(ce.Conflicts, func(c plan.Conflict) bool {
		return !c.Forceable && strings.HasPrefix(c.Reason, "also provided by module")
	}) {
		m, h := clashes(d)
		msgs, hints = append(msgs, m...), append(hints, h...)
	}
	switch {
	case len(hints) == 0:
		return err
	case all:
		err = ce
	}
	if len(msgs) == 0 {
		return errs.WithHints(err, hints...)
	}
	return errs.WithHints(fmt.Errorf("%s: %w", strings.Join(msgs, "; "), err), hints...)
}

// guardedConflicts names each recorded path of d, unowned in led, that a conflict lies at or
// below and that a guard keeps: installSpec left it out of the replaceable paths, so the
// engine saw a file lola did not write. all reports whether every conflict is such a one.
func (r *runner) guardedConflicts(d *Destination, led *ledger.Ledger, conflicts []plan.Conflict) (msgs, hints []string, all bool) {
	all = true
	seen, linked := map[string]bool{}, false
	for _, c := range conflicts {
		i := slices.IndexFunc(d.Recorded, func(rec Recorded) bool {
			return rec.Kind != KindModuleCopy && covers(rec, c.Target, c.Path, c.Key)
		})
		why := ""
		if i >= 0 && c.Forceable && owner(led, d.Recorded[i]) == "" {
			why = r.guard(d.Recorded[i])
		}
		if why == "" {
			all = false
			continue
		}
		if abs := d.Recorded[i].Abs(); !seen[abs] {
			seen[abs] = true
			msgs = append(msgs, fmt.Sprintf("the install writes %s, which lola migrate keeps (%s)", untrusted.Text(abs, untrusted.MaxName), why))
		}
		linked = linked || why == guardLinked
	}
	if len(msgs) == 0 {
		return nil, nil, false
	}
	hints = []string{"rerun lola migrate with --force, which replaces it after backing it up into the trash"}
	if linked {
		hints = append(hints, "or replace the symbolic link with a directory, then rerun lola migrate")
	}
	return msgs, hints, all
}

// clashes explains a plan two of d's modules collide in: the Python lola skips a component
// whose file another module already wrote, and lola installs every component, so the clash
// only appears now (observed against the Python lola at 91a2885). For each component two of
// d's installs share on a target, where one record lists it and the other does not, it names
// both modules and coaches removing the module the Python lola skipped it in with the Python
// lola's own uninstall, after which lola migrate migrates the other; otherwise the coaching is
// general. Installing that module with lola-go first instead would meet the Python lola's other
// files of it as conflicts only --force passes.
func clashes(d *Destination) (clashes, hints []string) {
	at := []string{"-s", "user"}
	if d.Scope.Kind == target.Project {
		at = []string{untrusted.Text(d.Root, untrusted.MaxName)}
	}
	for i, a := range d.accepted {
		for _, b := range d.accepted[i+1:] {
			ka, kb := installKey(a.in), installKey(b.in)
			if a.already || b.already || a.id != b.id || ka == kb {
				continue
			}
			for _, c := range shared(d.versions[ka].components, d.versions[kb].components) {
				la, lb := lists(a.rec, c), lists(b.rec, c)
				if la == lb {
					continue
				}
				kept, skipped := a, b
				if lb {
					kept, skipped = b, a
				}
				clashes = append(clashes, fmt.Sprintf("%s and %s both have %s for %s; the Python lola installed %s's and skipped %s's",
					a.rec.Module, b.rec.Module, c, a.id, kept.rec.Module, skipped.rec.Module))
				hint := "remove " + skipped.rec.Module + " here with the Python lola first (its command: " +
					errs.ShellJoin(append([]string{"lola", "uninstall", skipped.rec.Module, "-a", untrusted.Text(skipped.rec.Assistant, untrusted.MaxName)}, at...)) +
					"), then rerun lola migrate"
				if !slices.Contains(hints, hint) {
					hints = append(hints, hint)
				}
			}
		}
	}
	if len(clashes) == 0 {
		return nil, []string{"two modules the Python lola installed here render the same file; remove the one you want to give up " +
			"with the Python lola first (its command: lola uninstall <module> -a <assistant> " + errs.ShellJoin(at) + "), then rerun lola migrate"}
	}
	return clashes, hints
}

// shared lists, sorted, the components (kind/name) both a and b have.
func shared(a, b map[string]bool) []string {
	var out []string
	for c := range a {
		if b[c] {
			out = append(out, c)
		}
	}
	slices.Sort(out)
	return out
}

// lists reports whether rec names the component c (kind/name) as the Python lola installed it.
func lists(rec Record, c string) bool {
	kind, name, _ := strings.Cut(c, "/")
	names := map[string][]string{KindSkill: rec.Skills, KindCommand: rec.Commands, KindAgent: rec.Agents, KindMCP: rec.MCPs}[kind]
	return slices.Contains(names, name)
}

// where names a destination in messages.
func where(d *Destination) string {
	if d.Scope.Kind == target.User {
		return "user scope"
	}
	return "project " + untrusted.Text(d.Root, untrusted.MaxName)
}

func newDestResult(d *Destination) *DestResult {
	dr := &DestResult{ID: d.ID, Scope: d.Scope.Kind.String(), Status: StatusPlanned, Hints: []string{}, Installs: []InstallItem{},
		Replaced: []Moved{}, Moved: []Moved{}, Kept: []Kept{}, Unmappable: d.Unmappable, NotMigrated: slices.Clone(d.NotMigrated),
		Steps: []plan.Step{}, Modules: []engine.ModuleResult{}, Findings: []target.Finding{}, Notices: []engine.Notice{}}
	if d.Scope.Kind == target.Project {
		dr.Root = d.Root
	}
	for _, in := range d.Already {
		dr.Installs = append(dr.Installs, installItem(in, InstallAlready))
	}
	for _, in := range d.Installs {
		dr.Installs = append(dr.Installs, installItem(in, InstallNew))
	}
	return dr
}

func installItem(in Install, status string) InstallItem {
	return InstallItem{Module: in.Module, PythonModules: in.PythonModules, Source: untrusted.Text(in.Source, untrusted.MaxName),
		Ref: untrusted.Text(in.Ref, untrusted.MaxName), Path: untrusted.Text(in.Path, untrusted.MaxName), Targets: in.Targets, Status: status}
}

// installSpec is d's install: every module on its records' targets, with the recorded paths
// that may be replaced. A manifest without targets gets the records' targets.
func (r *runner) installSpec(d *Destination, led *ledger.Ledger, opts engine.Options) (engine.InstallSpec, error) {
	man, err := r.e.Manifest(d.Scope)
	if err != nil {
		return engine.InstallSpec{}, err
	}
	si := engine.ScopeInstall{ScopeChoice: engine.ScopeChoice{Scope: d.Scope, Source: "migrate"}}
	if len(man.Targets) == 0 {
		si.Targets = d.Targets
	}
	for _, rec := range d.Recorded {
		if rec.Kind == KindModuleCopy {
			continue
		}
		if ok, err := r.replaceable(rec, led); err != nil {
			return engine.InstallSpec{}, err
		} else if ok {
			si.Replace = append(si.Replace, plan.Replaceable{Target: rec.Target, Path: rec.Rel, Key: rec.Key, Dir: rec.Dir})
		}
	}
	spec := engine.InstallSpec{Cwd: d.Root, Scopes: []engine.ScopeInstall{si}, Options: opts}
	for _, in := range d.Installs {
		spec.Modules = append(spec.Modules, engine.InstallRequest{Arg: in.Source, Ref: in.Ref, Path: in.Path, Targets: in.Targets, Version: in.Version})
	}
	return spec, nil
}

// replaceable reports whether the install may overwrite rec: it exists, no lola-go module
// owns it, and no guard keeps it.
func (r *runner) replaceable(rec Recorded, led *ledger.Ledger) (bool, error) {
	present, err := r.present(rec)
	if err != nil || !present {
		return false, err
	}
	return owner(led, rec) == "" && r.guard(rec) == "", nil
}

// previewInstall reads d's ledger and plans its install as a dry run (nil when it installs
// nothing).
func (r *runner) previewInstall(d *Destination, opts Options) (*ledger.Ledger, *engine.InstallResult, error) {
	led, err := r.e.Ledger(d.Scope)
	if err != nil || len(d.Installs) == 0 {
		return led, nil, err
	}
	spec, err := r.installSpec(d, led, opts.Options)
	if err != nil {
		return nil, nil, err
	}
	res, err := r.e.InstallAll(r.ctx, spec)
	return led, res, err
}

// preview reports what migrating d would do, given its planned install.
func (r *runner) preview(d *Destination, res *engine.InstallResult, led *ledger.Ledger) (*DestResult, error) {
	dr := newDestResult(d)
	if res != nil {
		dr.addInstall(res)
	}
	decisions, err := r.decide(d, res, led, dr)
	if err != nil {
		return nil, err
	}
	for _, dc := range decisions {
		dr.report(dc, "")
	}
	fsteps, err := r.forcedSteps(d, res)
	if err != nil {
		return nil, err
	}
	for _, f := range fsteps {
		dr.Replaced = append(dr.Replaced, f.moved())
	}
	return dr, nil
}

// collectHolds finds, across every destination, the MCP entries the Python lola added that
// the installs leave in place, and records the module copies and Python home they reference
// (§12.5). The Python lola points a user-scope server at the module copy in whatever
// directory it ran in, so every destination's recorded copies are checked against every
// entry, each on its own: an entry that references one copy holds none of its siblings.
func (r *runner) collectHolds(p *Plan, results []*engine.InstallResult, leds []*ledger.Ledger) error {
	cands := []string{r.st.Home}
	if real, err := filepath.EvalSymlinks(r.st.Home); err == nil && real != r.st.Home {
		cands = append(cands, real)
	}
	for _, d := range p.Destinations {
		for _, rec := range d.Recorded {
			if rec.Kind == KindModuleCopy {
				cands = append(cands, rec.Abs())
			}
		}
	}
	for i, d := range p.Destinations {
		var steps []plan.Step
		if results[i] != nil {
			steps = results[i].Steps
		}
		for _, rec := range d.Recorded {
			if rec.Key == "" || owner(leds[i], rec) != "" || writes(steps, rec) {
				continue
			}
			v, found, err := r.mcpValue(rec)
			if err != nil {
				r.blind = rec.Abs()
				continue
			}
			if !found {
				continue
			}
			entry := fmt.Sprintf("MCP server %s in %s", rec.Name, rec.Abs())
			for _, c := range cands {
				if references(v, c) {
					r.holds[c] = append(r.holds[c], entry)
				}
			}
		}
	}
	return nil
}

// addInstall records an install's result and the lola-go names of its modules.
func (dr *DestResult) addInstall(res *engine.InstallResult) {
	dr.Steps, dr.Modules = append(dr.Steps, res.Steps...), append(dr.Modules, res.Modules...)
	dr.Findings, dr.Notices = append(dr.Findings, res.Findings...), append(dr.Notices, res.Notices...)
	n := 0
	for i := range dr.Installs {
		if dr.Installs[i].Status == InstallNew && n < len(res.Modules) {
			dr.Installs[i].Module = res.Modules[n].Name
			n++
		}
	}
}

// report adds a decision to dr; trash is where its backup or the moved path went.
func (dr *DestResult) report(dc decision, trash string) {
	path := dc.rec.Abs()
	switch dc.act {
	case actReplace:
		dr.Replaced = append(dr.Replaced, Moved{Path: path, Key: dc.rec.Key, Kind: dc.rec.Kind, Trash: trash})
	case actMove:
		dr.Moved = append(dr.Moved, Moved{Path: path, Kind: dc.rec.Kind, Trash: trash})
	case actKeep:
		dr.Kept = append(dr.Kept, Kept{Path: untrusted.Text(path, untrusted.MaxName), Reason: dc.reason})
	}
}

// migrate carries out d: install with backups taken just before the plan is applied, then move
// what lola-go did not take over to the trash.
func (r *runner) migrate(d *Destination, dr *DestResult) error {
	led, err := r.e.Ledger(d.Scope)
	if err != nil {
		return err
	}
	var decisions []decision
	if len(d.Installs) > 0 {
		spec, err := r.installSpec(d, led, r.opts.Options)
		if err != nil {
			return err
		}
		var fsteps []forced
		spec.BeforeWrite = func(res *engine.InstallResult) error {
			// InstallAll holds the scope lock here: decide against the ledger as it is under it,
			// not as it was read before.
			locked, derr := r.e.Ledger(d.Scope)
			if derr != nil {
				return derr
			}
			if decisions, derr = r.decide(d, res, locked, nil); derr != nil {
				return derr
			}
			if fsteps, derr = r.forcedSteps(d, res); derr != nil {
				return derr
			}
			return r.backup(d, decisions, fsteps)
		}
		res, err := r.e.InstallAll(r.ctx, spec)
		if te, ok := errors.AsType[*engine.TrustedError](err); ok {
			r.written = addTrusted(r.written, te.Trusted)
		}
		if err != nil {
			return err
		}
		r.written = addTrusted(r.written, res.Trusted)
		dr.addInstall(res)
		for _, f := range fsteps {
			dr.Replaced = append(dr.Replaced, f.moved())
		}
	} else if decisions, err = r.decide(d, nil, led, nil); err != nil {
		return err
	}
	dr.NotMigrated = slices.Clone(d.NotMigrated)
	r.addLeftMCP(d, decisions, dr)
	post, err := r.e.Ledger(d.Scope)
	if err != nil {
		return err
	}
	dr.Status = StatusMigrated
	var errList []error
	for _, dc := range decisions {
		if err := r.cleanup(d, dc, post, dr); err != nil {
			errList = append(errList, fmt.Errorf("%s: %w", untrusted.Text(dc.rec.Abs(), untrusted.MaxName), untrusted.CleanErr(err)))
		}
	}
	if len(d.Installs) == 0 && len(dr.Replaced) == 0 && len(dr.Moved) == 0 {
		dr.Status = StatusUnchanged
	}
	return errors.Join(errList...)
}

// backup copies into the trash every path the install replaces: a file, a whole directory, the
// settings file of an MCP entry (once), a module copy the install swaps out, and every file a
// step --force writes over. A forced step's file is backed up where it really is, past the
// symbolic links on its way; when a directory copied here already holds it, that copy is its
// backup.
func (r *runner) backup(d *Destination, decisions []decision, fsteps []forced) error {
	done := map[string]string{}
	dirs := map[string]string{} // the directories copied, by their resolved path
	failed := func(path string, err error) error {
		return errs.WithHints(fmt.Errorf("backing up %s: %w", untrusted.Text(path, untrusted.MaxName), untrusted.CleanErr(err)),
			"nothing was installed at this destination; fix the problem and rerun lola migrate")
	}
	copyOnce := func(path string) (string, error) {
		if to, ok := done[path]; ok {
			return to, nil
		}
		to, err := r.trash.Copy(d.ID, d.Root, path)
		if err != nil {
			return "", failed(path, err)
		}
		done[path] = to
		return to, nil
	}
	for i := range decisions {
		dc := &decisions[i]
		if dc.act != actReplace {
			continue
		}
		path := dc.rec.Abs()
		if dc.rec.Key != "" {
			path = filepath.Join(dc.rec.Root, filepath.FromSlash(dc.rec.Rel))
		}
		var err error
		if dc.backup, err = copyOnce(path); err != nil {
			return err
		}
		if dc.rec.Dir {
			real, err := filepath.EvalSymlinks(path)
			if err != nil {
				return failed(path, err)
			}
			dirs[real] = dc.backup
		}
	}
	for i := range fsteps {
		f := &fsteps[i]
		real, err := filepath.EvalSymlinks(filepath.Dir(f.file))
		if err != nil {
			return failed(f.file, err)
		}
		real = filepath.Join(real, filepath.Base(f.file))
		if f.backup = copiedWith(dirs, real); f.backup != "" {
			continue
		}
		if f.backup, err = copyOnce(real); err != nil {
			return err
		}
	}
	return nil
}

// copiedWith is where the copy of a directory in dirs (resolved path → trash path) that holds
// file, a resolved path below it, keeps it, or "".
func copiedWith(dirs map[string]string, file string) string {
	for dir, to := range dirs {
		if rel, err := filepath.Rel(dir, file); err == nil && dir != file && within(dir, file) {
			return filepath.Join(to, rel)
		}
	}
	return ""
}

// forced is a step --force writes over a conflict (plan.Step.Forced): file is what it changes
// on disk, through the step's Via when that leaves the destination, and path its logical path.
type forced struct {
	step       plan.Step
	path, file string
	backup     string // trash path of its backup, once copied
}

// moved is f as reported: replaced, with its backup.
func (f forced) moved() Moved {
	return Moved{Path: f.path, Key: f.step.Key, Kind: f.step.Kind, Trash: f.backup}
}

// forcedSteps lists the steps of res (nil when nothing is installed) that --force writes over
// a conflict and whose file exists: what the Python lola or the user had there is backed up
// before the install writes and reported replaced. Adopting or leaving a member absent writes
// nothing over it.
func (r *runner) forcedSteps(d *Destination, res *engine.InstallResult) ([]forced, error) {
	if res == nil {
		return nil, nil
	}
	var out []forced
	for _, s := range res.Steps {
		if s.Forced == "" || s.Action == plan.ActionAdopt || s.Action == plan.ActionAbsent || s.Action == plan.ActionForget {
			continue
		}
		f := forced{step: s, path: filepath.Join(root(r.e, d.Scope, s.Target), filepath.FromSlash(s.Path))}
		f.file = f.path
		if s.Via != "" {
			f.file = s.Via
		}
		switch _, err := os.Lstat(f.file); {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// cleanup finishes one decision after the install: a replaced directory loses the files
// lola-go did not write (unlinked when they match the backup), a path to move goes to the
// trash unless post, the ledger after the install, shows a lola-go module owns it now (the
// decision was made against the ledger before it), and the report gets the outcome.
func (r *runner) cleanup(d *Destination, dc decision, post *ledger.Ledger, dr *DestResult) error {
	switch dc.act {
	case actReplace:
		dr.report(dc, dc.backup)
		if dc.rec.Dir && dc.rec.Kind != KindModuleCopy {
			return r.leftovers(dc, post, dr)
		}
	case actMove:
		if m := owner(post, dc.rec); m != "" {
			dc.act, dc.reason = actKeep, "owned by lola-go module "+m
			dr.report(dc, "")
			return nil
		}
		to, err := r.move(d.ID, d.Root, dc.rec.Abs())
		if err != nil {
			if _, ok := errors.AsType[*trash.ChangedError](err); ok {
				dr.report(dc, to) // part of it is in the trash already
			}
			return err
		}
		dr.report(dc, to)
	case actKeep:
		dr.report(dc, "")
	}
	return nil
}

// move moves orig into dest's subdirectory of the trash, through its parent directory.
func (r *runner) move(dest, base, orig string) (string, error) {
	parent, err := os.OpenRoot(filepath.Dir(orig))
	if err != nil {
		return "", fmt.Errorf("cannot move %s into the trash: %w", untrusted.Text(orig, untrusted.MaxName), untrusted.CleanErr(err))
	}
	to, err := r.trash.Move(dest, base, parent, filepath.Base(orig))
	return to, errors.Join(err, parent.Close())
}

// leftovers removes, from a replaced directory, every file lola-go does not own whose content
// matches the backup (§12.3, §12.8); a file that differs is kept and listed. Directories it
// leaves empty go, the replaced directory included. With --keep-python it removes nothing and
// lists every such file as kept. The directory and its backup are read through os.Root, so
// nothing outside them is touched.
func (r *runner) leftovers(dc decision, post *ledger.Ledger, dr *DestResult) (err error) {
	abs := dc.rec.Abs()
	realAbs, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return err
	}
	// What lola-go owns, the directories (or links to one) on its way, and every link, directory
	// and file inside the directory it is reached through: the walk meets each by its own path.
	owned := map[string]bool{}
	for _, e := range post.Modules {
		for _, a := range e.Artifacts {
			if a.Target != dc.rec.Target || a.Key != "" || !strings.HasPrefix(a.Path, dc.rec.Rel+"/") {
				continue
			}
			rel := strings.TrimPrefix(a.Path, dc.rec.Rel+"/")
			for i := len(rel); i > 0; i = strings.LastIndex(rel[:i], "/") {
				owned[rel[:i]] = true
			}
			if err := ownReached(realAbs, rel, owned); err != nil {
				return err
			}
		}
	}
	backup, err := os.OpenRoot(dc.backup)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, backup.Close()) }()
	dir, err := os.OpenRoot(abs)
	if err != nil {
		return err
	}
	var dirs []string
	err = fs.WalkDir(dir.FS(), ".", func(p string, ent fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case ent.IsDir():
			dirs = append(dirs, p)
			return nil
		case owned[p]:
			return nil
		case r.opts.KeepPython:
			dr.Kept = append(dr.Kept, Kept{Path: untrusted.Text(filepath.Join(abs, filepath.FromSlash(p)), untrusted.MaxName), Reason: keptByKeepPython})
			return nil
		}
		gone, err := trash.UnlinkIfBackedUp(dir, backup, p)
		if err != nil || gone {
			return err
		}
		why := "differs from its backup in the trash, so it was edited after the backup"
		if _, err := backup.Lstat(p); errors.Is(err, fs.ErrNotExist) {
			why = "appeared after the backup, so lola did not remove it"
		} else if err != nil {
			return err
		}
		dr.Kept = append(dr.Kept, Kept{Path: untrusted.Text(filepath.Join(abs, filepath.FromSlash(p)), untrusted.MaxName), Reason: why})
		return nil
	})
	for i := len(dirs) - 1; i > 0 && err == nil && !r.opts.KeepPython; i-- {
		var ents []fs.DirEntry
		if ents, err = fs.ReadDir(dir.FS(), dirs[i]); err == nil && len(ents) == 0 {
			err = dir.Remove(dirs[i])
		}
	}
	if err = errors.Join(err, dir.Close()); err != nil || r.opts.KeepPython {
		return err
	}
	ents, err := os.ReadDir(abs)
	if err == nil && len(ents) == 0 {
		err = os.Remove(abs)
	}
	return err
}

// maxLinkHops bounds the links ownReached follows for one path, as the OS bounds a lookup.
const maxLinkHops = 40

// ownReached marks owned every path inside the replaced directory realAbs (its own links
// resolved) that rel, relative to it, is reached through: each link followed one hop at a
// time, each directory on the way and the file at the end. The walk tracks the physical path,
// so it follows links out of the directory and back and applies .. to where a link really
// led; it marks only what lies inside. It stops where a path is missing or the links run past
// maxLinkHops.
func ownReached(realAbs, rel string, owned map[string]bool) error {
	cur := realAbs
	rest := strings.Split(rel, "/")
	for hops := 0; len(rest) > 0; {
		c := rest[0]
		rest = rest[1:]
		switch c {
		case "", ".":
			continue
		case "..":
			cur = filepath.Dir(cur)
			continue
		}
		p := filepath.Join(cur, c)
		fi, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		if in, err := filepath.Rel(realAbs, p); err == nil && filepath.IsLocal(in) {
			owned[filepath.ToSlash(in)] = true
		}
		if fi.Mode()&fs.ModeSymlink == 0 {
			if !fi.IsDir() && len(rest) > 0 {
				return nil
			}
			cur = p
			continue
		}
		if hops++; hops > maxLinkHops {
			return nil
		}
		to, err := os.Readlink(p)
		if err != nil {
			return err
		}
		if filepath.IsAbs(to) {
			vol := filepath.VolumeName(to)
			cur, to = vol+string(filepath.Separator), to[len(vol):]
		}
		rest = append(strings.Split(filepath.ToSlash(to), "/"), rest...)
	}
	return nil
}

// present reports whether rec is still there: the path, or for an MCP entry its member.
func (r *runner) present(rec Recorded) (bool, error) {
	if rec.Key != "" {
		_, found, err := r.mcpValue(rec)
		return found, err
	}
	_, err := os.Lstat(rec.Abs())
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// mcpValue reads rec's MCP entry. A file that is missing has no entry; one lola cannot parse is
// an error.
func (r *runner) mcpValue(rec Recorded) ([]byte, bool, error) {
	b, err := os.ReadFile(filepath.Join(rec.Root, filepath.FromSlash(rec.Rel)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return fsx.JSONGet(b, rec.Key)
}

// owner is the lola-go module that owns rec (any artifact at it, or below a directory), or "".
func owner(led *ledger.Ledger, rec Recorded) string {
	names := make([]string, 0, len(led.Modules))
	for n := range led.Modules {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if rec.Kind == KindModuleCopy {
			if n == rec.Name {
				return n
			}
			continue
		}
		for _, a := range led.Modules[n].Artifacts {
			if a.Target != rec.Target {
				continue
			}
			switch {
			case rec.Key != "" && a.Path == rec.Rel && a.Key == rec.Key,
				rec.Key == "" && a.Path == rec.Rel,
				rec.Dir && strings.HasPrefix(a.Path, rec.Rel+"/"):
				return n
			}
		}
	}
	return ""
}

// guard says why rec must stay where it is, or "": it lies outside its root, it or a directory
// between its root and it is a symbolic link, or git tracks it (unless --force). An MCP entry
// is a member of a settings file, which every install edits in place whether git tracks it or
// not, so only the link check applies to it.
func (r *runner) guard(rec Recorded) string {
	if !within(rec.Root, rec.Abs()) {
		return "outside the destination"
	}
	linked, err := fsx.Linked(rec.Root, filepath.FromSlash(rec.Rel))
	switch {
	case err != nil:
		return "lola could not check it for symbolic links: " + untrusted.CleanErr(err).Error()
	case linked:
		return guardLinked
	case r.opts.Force, rec.Key != "":
		return ""
	}
	return tracked(r.ctx, rec.Abs())
}

// guardLinked is guard's reason for a symbolic link, or a path below one.
const guardLinked = "a symbolic link, or below one"

// tracked says why git keeps p: "" when p is in no git work tree or git tracks nothing at or
// below it. It fails closed: inside a work tree, a git that is missing or fails keeps p.
func tracked(ctx context.Context, p string) string {
	dir := filepath.Dir(p)
	in, err := source.InWorkTree(dir)
	switch {
	case err != nil:
		return "lola could not tell whether git tracks it: " + untrusted.CleanErr(err).Error()
	case !in:
		return ""
	}
	err = source.GitCommand(ctx, dir, "--literal-pathspecs", "ls-files", "--error-unmatch", "--", filepath.Base(p)).Run()
	exit, isExit := errors.AsType[*exec.ExitError](err)
	switch {
	case err == nil:
		return "tracked by git; --force moves it anyway"
	case isExit && exit.ExitCode() == 1:
		return ""
	case errors.Is(err, exec.ErrNotFound):
		return "in a git work tree, and git is not on PATH to tell whether it is tracked; --force moves it anyway"
	case isExit:
		return fmt.Sprintf("git ls-files exited with status %d, so lola cannot tell whether git tracks it", exit.ExitCode())
	}
	return "lola could not run git to tell whether it is tracked: " + untrusted.CleanErr(err).Error()
}

// decide works out every recorded path's fate at d, given the install's computed result (nil
// when nothing is installed) and the ledger before it (§6, §12): owned paths and guarded ones
// stay, except that a guarded path the install writes with --force is left to its forced
// steps (forcedSteps), which are backed up and reported; paths the install writes are
// replaced; the rest move, unless --keep-python. MCP
// entries the install does not write stay, as not migrated, and hold the module copies and
// the Python home they reference. dr, when set, gets those entries.
func (r *runner) decide(d *Destination, res *engine.InstallResult, led *ledger.Ledger, dr *DestResult) ([]decision, error) {
	var steps []plan.Step
	modules := map[string]bool{}
	if res != nil {
		steps = res.Steps
		for _, m := range res.Modules {
			modules[m.Name] = true
		}
	}
	if err := unaccountedCopy(d, modules, led); err != nil {
		return nil, err
	}
	var out []decision
	var copies []Recorded
	for _, rec := range d.Recorded {
		if rec.Kind == KindModuleCopy {
			copies = append(copies, rec)
			continue
		}
		present, err := r.present(rec)
		if err != nil && rec.Key != "" {
			r.blind = rec.Abs()
			if dr != nil {
				dr.NotMigrated = append(dr.NotMigrated, leftMCP(rec, "lola cannot read this file to check the entry: "+untrusted.CleanErr(err).Error()))
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		if !present {
			continue
		}
		dc := decision{rec: rec}
		m := owner(led, rec)
		guard := ""
		if m == "" {
			guard = r.guard(rec)
		}
		switch {
		case m != "":
			dc.act, dc.reason = actKeep, "owned by lola-go module "+m
		case guard != "" && r.opts.Force && writes(steps, rec):
			dc.act = actForced
		case guard != "":
			dc.act, dc.reason = actKeep, guard
		case writes(steps, rec):
			dc.act = actReplace
		case rec.Key != "":
			if dr != nil {
				dr.NotMigrated = append(dr.NotMigrated, leftMCP(rec, ""))
			}
			continue
		case r.opts.KeepPython:
			dc.act, dc.reason = actKeep, keptByKeepPython
		default:
			dc.act = actMove
		}
		out = append(out, dc)
	}
	for _, rec := range copies {
		present, err := r.present(rec)
		if err != nil {
			return nil, err
		}
		if !present {
			continue
		}
		dc := decision{rec: rec}
		switch held := r.held(rec.Abs()); {
		case owner(led, rec) != "":
			dc.act, dc.reason = actKeep, "lola-go's module copy of "+rec.Name
		case modules[rec.Name]:
			if guard := r.guard(rec); guard != "" && !r.opts.Force {
				return nil, errs.WithHints(fmt.Errorf("installing %s would replace %s, which lola migrate keeps: %s",
					untrusted.Text(rec.Name, untrusted.MaxName), untrusted.Text(rec.Abs(), untrusted.MaxName), guard),
					"rerun lola migrate with --force to replace "+untrusted.Text(rec.Abs(), untrusted.MaxName)+" (it is backed up first), or move it out of the way")
			}
			dc.act = actReplace // the install swaps in lola-go's copy of the module of the same name
		case held != "":
			dc.act, dc.reason = actKeep, held
		default:
			dc.act, dc.reason = r.moveUnlessGuarded(rec)
		}
		out = append(out, dc)
	}
	return out, nil
}

// unaccountedCopy refuses an install that would swap out a project's .lola/modules/<m>, for
// a module m it installs, that neither the ledger (lola-go's own copy) nor a record d migrates
// (the Python lola's copy, backed up first) accounts for: the engine replaces the copy of a
// module new to the ledger outright, so a directory nobody accounts for would be lost.
func unaccountedCopy(d *Destination, installs map[string]bool, led *ledger.Ledger) error {
	if d.Scope.Kind != target.Project {
		return nil
	}
	names := slices.Sorted(maps.Keys(installs))
	for _, m := range names {
		if led.Modules[m] != nil || slices.ContainsFunc(d.Recorded, func(rec Recorded) bool { return rec.Kind == KindModuleCopy && rec.Name == m }) {
			continue
		}
		dir := filepath.Join(d.Root, ".lola", "modules")
		p := filepath.Join(dir, m)
		if _, err := os.Lstat(p); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		return errs.WithHints(fmt.Errorf("installing %s would replace %s, which neither lola-go nor a Python lola install lola migrate migrates here owns",
			untrusted.Text(m, untrusted.MaxName), untrusted.Text(p, untrusted.MaxName)),
			"move it out of "+untrusted.Text(dir, untrusted.MaxName)+" if you still need it, then rerun lola migrate")
	}
	return nil
}

// moveUnlessGuarded is the fate of a path nothing else decided: kept by a guard or
// --keep-python, else moved.
func (r *runner) moveUnlessGuarded(rec Recorded) (action, string) {
	if why := r.guard(rec); why != "" {
		return actKeep, why
	}
	if r.opts.KeepPython {
		return actKeep, keptByKeepPython
	}
	return actMove, ""
}

// writes reports whether the install's steps write rec.
func writes(steps []plan.Step, rec Recorded) bool {
	return slices.ContainsFunc(steps, func(s plan.Step) bool {
		return s.Action != plan.ActionDelete && s.Action != plan.ActionForget && covers(rec, s.Target, s.Path, s.Key)
	})
}

// covers reports whether a destination (target, logical path, JSON pointer) is rec: the file
// or JSON member, or a file below the directory.
func covers(rec Recorded, tgt, path, key string) bool {
	switch {
	case tgt != rec.Target:
		return false
	case rec.Key != "":
		return path == rec.Rel && key == rec.Key
	case rec.Dir:
		return strings.HasPrefix(path, rec.Rel+"/")
	}
	return path == rec.Rel && key == ""
}

// leftMCP is an MCP entry the Python lola added that lola-go does not write (§12.4).
func leftMCP(rec Recorded, why string) Item {
	hint := "it keeps working; remove the entry by hand when you no longer need it"
	if why != "" {
		hint = why
	}
	return Item{What: ItemMCP, Module: rec.Module, Path: filepath.Join(rec.Root, filepath.FromSlash(rec.Rel)), Marker: rec.Key, Hints: []string{hint}}
}

// addLeftMCP lists, for the real run, the MCP entries the decisions left in place.
func (r *runner) addLeftMCP(d *Destination, decisions []decision, dr *DestResult) {
	for _, rec := range d.Recorded {
		if rec.Key == "" || slices.ContainsFunc(decisions, func(dc decision) bool { return dc.rec == rec }) {
			continue
		}
		if present, err := r.present(rec); err != nil {
			dr.NotMigrated = append(dr.NotMigrated, leftMCP(rec, "lola cannot read this file to check the entry: "+untrusted.CleanErr(err).Error()))
		} else if present {
			dr.NotMigrated = append(dr.NotMigrated, leftMCP(rec, ""))
		}
	}
}

// references reports whether a JSON value mentions path, as written or JSON-escaped.
func references(v []byte, path string) bool {
	esc, _ := json.Marshal(path) // encoding a string cannot fail
	return strings.Contains(string(v), path) || strings.Contains(string(v), strings.Trim(string(esc), `"`))
}

// held says which MCP entries left in place keep p, or "".
func (r *runner) held(p string) string {
	if r.blind != "" {
		return "an MCP settings file lola cannot read (" + untrusted.Text(r.blind, untrusted.MaxName) + ") may reference it"
	}
	var by []string
	for c, entries := range r.holds {
		if within(p, c) || within(c, p) {
			by = append(by, entries...)
		}
	}
	if len(by) == 0 {
		return ""
	}
	sort.Strings(by)
	return "referenced by " + strings.Join(slices.Compact(by), ", ")
}

// home decides the Python home's fate (§6, §12.5, §12.6): it moves to the trash last, only
// when every record migrated, every destination did, and it holds nothing but what the Python
// lola writes there.
func (r *runner) home(p *Plan, rep *Result) HomeResult {
	h := HomeResult{Path: r.st.Home, Action: HomeKeep}
	keep := func(why string) HomeResult {
		h.Reason = why
		if filepath.Dir(r.st.Home) == r.e.Paths.Home && filepath.Base(r.st.Home) == ".lola" {
			h.Reason += "; while ~/.lola stays, lola-go sees a project at ~ and refuses to install there"
		}
		return h
	}
	unmappable := len(p.Unmappable)
	// A marketplace not registered stays where its Python registration is, as an unmappable
	// record does (Plan 6c §6.9), but a disabled one: lola has no disabled state, and its listing
	// names the command that registers it.
	unregistered := 0
	for _, m := range rep.Markets {
		if m.Status == MarketSkipped && !m.disabled || m.Status == MarketFailed {
			unregistered++
		}
	}
	for _, d := range rep.Destinations {
		unmappable += len(d.Unmappable)
		if d.Status == StatusFailed {
			return keep("a destination failed")
		}
	}
	fi, err := os.Lstat(r.st.Home)
	switch {
	case !r.st.Exists:
		return HomeResult{Path: r.st.Home, Action: HomeNone}
	case r.opts.KeepPython:
		return keep("--keep-python")
	case unmappable > 0:
		return keep(fmt.Sprintf("%d record(s) lola-go cannot migrate are still in installed.yml", unmappable))
	case unregistered == 1:
		return keep("1 marketplace lola did not register is still in " + filepath.Join(r.st.Home, "market"))
	case unregistered > 1:
		return keep(fmt.Sprintf("%d marketplaces lola did not register are still in %s", unregistered, filepath.Join(r.st.Home, "market")))
	case err != nil:
		return keep("lola cannot read it: " + untrusted.CleanErr(err).Error())
	case fi.Mode()&fs.ModeSymlink != 0:
		return keep("it is a symbolic link")
	}
	for _, d := range rep.Destinations {
		if d.Root != "" && within(r.st.Home, d.Root) {
			return keep("it holds the project " + untrusted.Text(d.Root, untrusted.MaxName))
		}
	}
	for _, mine := range []string{r.e.Paths.Home, r.e.Paths.Trash, filepath.Join(r.e.Paths.ConfigHome, "lola"),
		filepath.Join(r.e.Paths.StateHome, "lola"), filepath.Join(r.e.Paths.CacheHome, "lola"), filepath.Join(r.e.Paths.DataHome, "lola")} {
		if within(r.st.Home, mine) {
			return keep("it is or holds " + untrusted.Text(mine, untrusted.MaxName))
		}
	}
	ents, err := os.ReadDir(r.st.Home)
	if err != nil {
		return keep("lola cannot list it: " + untrusted.CleanErr(err).Error())
	}
	for _, ent := range ents {
		n := ent.Name()
		if !slices.Contains([]string{"installed.yml", "modules", "market", "cache", "plugin-data"}, n) && !strings.HasPrefix(n, ".installed.yml.") {
			return keep("it holds " + untrusted.Text(n, untrusted.MaxName) + ", which the Python lola does not write there")
		}
	}
	if why := r.held(r.st.Home); why != "" {
		return keep(why)
	}
	if !r.opts.Force {
		if why := tracked(r.ctx, r.st.Home); why != "" {
			return keep(why)
		}
	}
	h.Action = HomeMove
	return h
}

// nothing reports whether a run would only keep and report: no install, replacement or move,
// and the home stays.
func nothing(rep *Result) bool {
	if rep.Home.Action == HomeMove || slices.ContainsFunc(rep.Markets, func(m Market) bool { return m.Status == MarketRegister }) {
		return false
	}
	for _, d := range rep.Destinations {
		for _, in := range d.Installs {
			if in.Status == InstallNew {
				return false
			}
		}
		if len(d.Replaced) > 0 || len(d.Moved) > 0 {
			return false
		}
	}
	return true
}
