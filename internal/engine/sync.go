package engine

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/fsx"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/trust"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// SyncRequest is one lola sync (Plan 6a §9.2): make Scope match its manifest. Modules limits the
// run to the modules named (§3.1); empty means every entry and every installed module. Update
// re-pins every git and archive entry of the run at its ref. Options are as for render: DryRun,
// Force and the hook options; Targets, Strict and Rules are not used.
type SyncRequest struct {
	Scope   Scope
	Modules []string
	Update  bool
	Options Options
	// Trust asks the user to trust the changes to project.yml since they last trusted it, the
	// pins the sync would write and the folders whose content changed (Plan 6a §7.5): all of
	// them, or the entries the answer names. nil means it cannot ask: a sync that needs the
	// question is refused before any network contact. The CLI passes a Trust that answers All
	// for --trust; -y never sets it.
	Trust func(TrustReport) (TrustAnswer, error)
	// ConfirmRemovals asks the user to confirm the removals the sync would make. nil means it
	// cannot ask: a sync with removals is refused. The CLI passes an always-yes ConfirmRemovals
	// for -y.
	ConfirmRemovals func([]Removal) (bool, error)
}

// TrustAnswer is the user's answer to the trust question (Plan 6a §9.2): All accepts every change
// of the run; otherwise Modules names the accepted entries, as the report names them. An answer
// accepting nothing is a Cancel.
type TrustAnswer struct {
	All     bool
	Modules []string
}

// Removal is what a sync removes from one module (Plan 6a §9.3 step 9): the whole module, which
// the manifest no longer lists, or the targets its entry no longer names, with every path
// lock.json says lola wrote there for it. Its strings, from lock.json, have been through
// untrusted.Text.
type Removal struct {
	Module      string        `json:"module"`
	Targets     []string      `json:"targets"`
	WholeModule bool          `json:"whole_module"`
	Paths       []RemovedPath `json:"paths"`
}

// RemovedPath is a path lola wrote for a target and a removal deletes; Path carries a JSON
// member's key after a space.
type RemovedPath struct {
	Target string `json:"target"`
	Path   string `json:"path"`
}

// ErrDeclined is a sync the user answered No to: nothing more was done.
var ErrDeclined = errors.New("declined")

// TrustStatusNotApplicable is the trust status of the user scope, whose user.yml is trusted.
const TrustStatusNotApplicable = "not_applicable"

// TrustReport is how the scope's manifest, with the pins the sync writes, differs from what the
// user last trusted (Plan 6a §7.3, §11.2). Recorded says this run wrote the trust record.
// Targets is the top-level targets' change, nil when the record covers them. ManifestChanged
// says the manifest itself differs from the record; when it does not, the changes are the pins
// the sync would write and folders whose content changed, which the diff's header says.
type TrustReport struct {
	Status          string             `json:"status"`
	Recorded        bool               `json:"recorded"`
	Targets         *trust.FieldChange `json:"targets"`
	Changes         []TrustChange      `json:"changes"`
	ManifestChanged bool               `json:"-"`
}

// TrustChange is one module's change with the content it brings (Plan 6a §7.3): the files the
// new version adds, changes and removes against the version installed (FilesNote says why every
// file is listed as added when lola cannot compare), its module MCP servers, and every hook it
// declares. Every string from the manifest or the module has been through untrusted.Text. A
// removed module, and every module of a report made before anything was fetched, brings none.
type TrustChange struct {
	trust.Change
	// Note says what an entry that does not pin its content installs (a git or archive entry
	// without a digest in the manifest): "content not fixed; installs what <ref> serves now",
	// naming the source for an archive, which has no ref.
	Note string `json:"note,omitempty"`
	// Reason says why the sync needs trust for an entry whose manifest entry may be as trusted:
	// the pin it would write ("re-pin from <ref> (--update)", "pin what <ref> serves now", each
	// noting "not fetched yet" before anything is) or a folder's changed content.
	Reason string `json:"reason,omitempty"`
	// Pin is the pin the sync writes for the entry, once fetched; the human diff prints its hashes
	// when no commit or digest field shows them (the pin the record holds already).
	Pin *Pin `json:"-"`
	// InstallsToTargets marks an added or changed entry with no targets of its own while the
	// top-level targets changed: accepting it accepts them (Plan 6a §7.4).
	InstallsToTargets bool `json:"-"`
	// ReviewEdits and InstallOwn are the lines the diff adds for a folder whose content changed
	// (Plan 6a §7.5): the git command that shows the edits, when the folder is in a git work
	// tree, and the install that records the content as the user's own.
	ReviewEdits string `json:"-"`
	InstallOwn  string `json:"-"`
	// FolderContent marks a change that is only a folder module's content differing from what
	// was installed, with project.yml as trusted (folderChanges); folderEdits gives such a change
	// its ReviewEdits and InstallOwn lines.
	FolderContent bool               `json:"-"`
	Files         []FileChange       `json:"files"`
	FilesNote     string             `json:"files_note,omitempty"`
	MCPServers    []ContentMCPServer `json:"mcp_servers"`
	Hooks         []ContentHook      `json:"hooks"`
}

// TrustRefusalError is a sync refused, before any network contact, because it needs answers it
// cannot ask for (Plan 6a §9.3 step 4): the user's trust (SyncRequest.Trust is nil), the
// confirmation of its removals (ConfirmRemovals is nil), or both. Report holds the trust changes,
// with fields only for git and archive entries, since nothing was fetched to show their content,
// and changes: [] when only the removals are missing; Removals lists the removals, [] when only
// trust is missing.
type TrustRefusalError struct {
	Report   TrustReport
	Removals []Removal
	msg      string
}

func (e *TrustRefusalError) Error() string { return e.msg }

// JSONFields are the keys the error document of a --json refusal carries next to error and
// hints (Plan 6a §11.2): the changes it needs trust for and the removals, so a CI consumer gets
// them without a second run.
func (e *TrustRefusalError) JSONFields() map[string]any {
	return map[string]any{"trust": e.Report, "removals": e.Removals}
}

// Pin reasons: why a sync writes an entry's pin.
const (
	PinUnpinned = "unpinned" // the entry had none: it pins the version its ref resolved to
	PinUpdate   = "update"   // --update fetched its ref again
)

// Pin is a pin a sync writes to an entry of the manifest; Ref, from the manifest, has been
// through untrusted.Text. Written is false in a dry run.
type Pin struct {
	Module         string `json:"module"`
	Ref            string `json:"ref"`
	Commit         string `json:"commit"`
	Digest         string `json:"digest"`
	PreviousDigest string `json:"previous_digest"`
	Reason         string `json:"reason"`
	Written        bool   `json:"written"`
}

// SyncResult is what a sync reports: Result over its one scope, the pins it writes, and whether
// the scope was already in sync (nothing to write, pin or remove). Changed names, in order, the
// modules the run changes (a step, a pin, a removal or a replaced copy): a sync limited to them
// with the same answers applies the same plan. A dry run that is not in sync names the command
// that applies it, ToApply, and, when that needs answers, the form for no terminal, OffTerminal
// (Plan 6a §11.1); EquivalentCommands is the one of them that applies it off a terminal, [] for
// any other run. Equivalent is, after a trust answer, the command that gives the same answers off
// a terminal (§7.5).
type SyncResult struct {
	*Result
	Scope              string      `json:"scope"`
	Root               string      `json:"root"`
	Trust              TrustReport `json:"trust"`
	Pins               []Pin       `json:"pins"`
	Removals           []Removal   `json:"removals"`
	InSync             bool        `json:"in_sync"`
	EquivalentCommands []string    `json:"equivalent_commands"`
	Changed            []string    `json:"-"`
	Equivalent         string      `json:"-"`
	ToApply            string      `json:"-"`
	OffTerminal        string      `json:"-"`
}

// desired is the set of modules a scope should have installed: in 6a, the manifest's entries
// in name order. Plan 6b adds the entries of active profiles here and nowhere else.
func desired(man *manifest.Manifest) []manifest.Entry {
	out := man.Clone().Modules
	slices.SortFunc(out, func(a, b manifest.Entry) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// syncEntry is one desired entry of a sync: validated (spec, targets), then resolved to a
// version. v is read from a snapshot of the cache or a staged fetch in the run directory; staged
// is set while the version is not in the cache, and headIfAbsent says its commit must not move
// a head (a pinned fetch, or any in a project dry run). pin is set when the sync writes a new pin for the entry. covered says
// the trust record covers the entry (always at user scope); folderChanged says it is a covered
// folder at project scope whose content is not the version lock.json records, and
// notInstalled that lock.json records none.
type syncEntry struct {
	ent           manifest.Entry
	goneFrom      string // the description (describeSource) of the local source the cache records for the name, which no longer exists
	covered       bool
	folderChanged bool
	notInstalled  bool
	spec          source.Spec
	targets       []string
	update        bool
	v             store.Version
	staged        *store.Staged
	headIfAbsent  bool
	pin           *Pin
}

// pinned is the entry as the sync records it: with its new pin, if it gets one.
func (se *syncEntry) pinned() manifest.Entry {
	ent := se.ent
	if se.pin != nil {
		ent.Commit, ent.Digest = se.pin.Commit, se.pin.Digest
	}
	return ent
}

// Sync makes a scope match its manifest (Plan 6a §9.3): it validates every entry before any
// network contact or prompt, resolves each to the version it pins (an unpinned entry to its
// ref's version, which is pinned; --update fetches the ref again), publishes what it fetched to
// the cache, and applies one plan under the scope lock, writing new pins to the manifest in the
// same transaction. At project scope, a manifest the trust record does not cover, a pin the sync
// would write, or a covered folder whose content changed since lola installed it needs the
// user's trust: that content stays in the run's staging until the user trusts it, and the
// record is written once, after the apply.
func (e *Engine) Sync(ctx context.Context, req SyncRequest) (_ *SyncResult, err error) {
	sc, opts := req.Scope, req.Options
	if home := (Scope{Kind: target.Project, ProjectDir: e.Paths.Home}); sc.Kind == target.Project && sc.Key() == home.Key() {
		return nil, errs.WithHints(fmt.Errorf("project scope at %s is your home directory, where the user scope installs",
			untrusted.Text(e.Paths.Home, untrusted.MaxName)), "sync the user scope: "+e.SyncCommand(Scope{Kind: target.User}))
	}
	man, led, unlock, err := e.load(sc, opts.DryRun)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := e.refuseMissingManifest(sc); err != nil {
		return nil, err
	}
	limit, err := e.limitTo(sc, man, led, req.Modules)
	if err != nil {
		return nil, err
	}
	res := &SyncResult{Result: newResult(opts.DryRun), Scope: sc.Kind.String(), Root: root(sc), Pins: []Pin{}, Removals: []Removal{},
		Trust: TrustReport{Status: TrustStatusNotApplicable, Changes: []TrustChange{}}, EquivalentCommands: []string{}}
	res.Notices = append(res.Notices, e.sweepRuns()...)
	run, err := e.runDir()
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(run)) }()
	if err := e.refuseTrackedLedger(ctx, sc, res.Result); err != nil {
		return nil, err
	}

	entries, err := e.validateSync(sc, man, req, limit)
	if err != nil {
		return nil, err
	}
	if n, ok := outOfSync(man, led, limit, e.SyncCommand(sc)); ok {
		res.Notices = append(res.Notices, n)
	}
	res.Notices = append(res.Notices, goneNotices(entries)...)
	var rec *trust.Record
	var early trust.Report
	manifestChanged, needTrust := false, false
	if sc.Kind == target.Project {
		if rec, err = e.Trust.Load(sc.ProjectDir); err != nil {
			return nil, err
		}
		// Coverage is the entry's own (§7.2): it decides containment and whether content the
		// entry pins may be cached before the question. Top-level targets the record does not
		// cover are a change of the report, which an entry that installs to them waits on.
		for _, se := range entries {
			se.covered = rec.View.Covers(se.ent)
		}
		if err := e.containLocalSources(sc, entries); err != nil {
			return nil, err
		}
		// The record covers what a folder entry says, not what the folder holds: a covered folder
		// is read now (no network), and its content needs trust unless it is the version installed.
		for _, se := range entries {
			if !se.covered || se.spec.Kind != source.KindFolder {
				continue
			}
			if err := e.resolveSync(ctx, sc, se, run); err != nil {
				return nil, err
			}
			installed := led.Modules[se.ent.Name]
			se.notInstalled = installed == nil
			se.folderChanged = installed == nil || installed.Digest != se.v.Meta.Digest
		}
		early = runReport(trust.Check(rec, trust.ViewOf(man)), limit, entries)
		manifestChanged = early.Status != trust.StatusTrusted
		needTrust = manifestChanged || slices.ContainsFunc(entries, func(se *syncEntry) bool { return se.folderChanged || needsPin(se) })
	} else {
		for _, se := range entries {
			se.covered = true
		}
	}
	// The removals are listed now, from lock.json and the manifest alone, and a run that cannot
	// ask for an answer it needs is refused here, once, before anything is fetched (Plan 6a §9.3
	// step 4).
	if res.Removals, err = e.syncRemovals(sc, man, led, entries, limit); err != nil {
		return nil, err
	}
	trustMissing, removalsMissing := needTrust && req.Trust == nil, len(res.Removals) > 0 && req.ConfirmRemovals == nil
	if !opts.DryRun && (trustMissing || removalsMissing) {
		report := TrustReport{Status: res.Trust.Status, Changes: []TrustChange{}}
		if sc.Kind == target.Project {
			report.Status = early.Status
		}
		if trustMissing {
			// An uncovered folder is read for the refusal too: it is local, containment has passed,
			// and its content stays in the run's staging.
			for _, se := range entries {
				if se.spec.Kind == source.KindFolder && se.v.ContentDir == "" {
					if err := e.resolveSync(ctx, sc, se, run); err != nil {
						return nil, err
					}
				}
			}
			report = trustReport(early)
			report.ManifestChanged = manifestChanged
			folderChanges(&report, entries, led)
			if err := e.folderEdits(sc, &report, entries); err != nil {
				return nil, err
			}
			notFixed(&report, entries)
			pinReasons(&report, entries, false)
			if err := e.fillContent(sc, &report, entries, led, e.newProbeRun(ctx)); err != nil {
				return nil, err
			}
		}
		removals := []Removal{}
		if removalsMissing {
			removals = res.Removals
		}
		return nil, e.syncRefusal(sc, req, report, removals, trustMissing, removalsMissing, entries)
	}

	for _, se := range entries {
		if se.v.ContentDir == "" { // a covered folder at project scope was read above
			if err := e.resolveSync(ctx, sc, se, run); err != nil {
				return nil, err
			}
		}
		// What the user trusts at its pin goes into the cache now; everything else waits. A
		// project dry run moves no head (Plan 6a §3.1), not even for an --update fetch, and
		// replaces no source the cache records (§6.5).
		if se.covered && se.pin == nil && !se.folderChanged && (!opts.DryRun || se.goneFrom == "") {
			if opts.DryRun && sc.Kind == target.Project {
				se.headIfAbsent = true
			}
			if err := e.publish(se); err != nil {
				return nil, err
			}
		}
	}
	// The pins the sync writes are trust changes too: the question is about the manifest as it
	// will be saved (Plan 6a §7.3).
	probes := e.newProbeRun(ctx)
	var savedRep trust.Report
	if sc.Kind == target.Project {
		saved := man.Applied(manifest.Change{Set: pinsOf(entries)})
		savedRep = runReport(trust.Check(rec, trust.ViewOf(saved)), limit, entries)
		res.Trust = trustReport(savedRep)
		res.Trust.ManifestChanged = manifestChanged
		folderChanges(&res.Trust, entries, led)
		if err := e.folderEdits(sc, &res.Trust, entries); err != nil {
			return nil, err
		}
		notFixed(&res.Trust, entries)
		pinReasons(&res.Trust, entries, true)
		if err := e.fillContent(sc, &res.Trust, entries, led, probes); err != nil {
			return nil, err
		}
		for i := range res.Trust.Changes {
			c := &res.Trust.Changes[i]
			k := slices.IndexFunc(entries, func(se *syncEntry) bool { return shown(se.ent.Name) == c.Module })
			c.InstallsToTargets = res.Trust.Targets != nil && c.Kind != trust.ChangeRemoved && k >= 0 && len(entries[k].ent.Targets) == 0
		}
	}
	// Plan every module now, from the bytes that were hashed, before the trust question: a module
	// that cannot be planned stops the run before anything is asked (Plan 6a §9.3 step 10).
	sel, err := e.selections()
	if err != nil {
		return nil, err
	}
	sp, err := e.planSync(sc, entries, led, sel, opts, probes, req.Modules)
	if err != nil {
		return nil, err
	}
	var acc *acceptance
	if sc.Kind == target.Project && res.Trust.Status != trust.StatusTrusted {
		switch {
		case opts.DryRun:
		case req.Trust == nil: // refused at step 4 already; kept so no path applies untrusted content
			return nil, e.syncRefusal(sc, req, res.Trust, []Removal{}, true, false, entries)
		default:
			ans, err := req.Trust(res.Trust)
			if err != nil {
				return nil, err
			}
			if acc, err = accept(savedRep, res.Trust, ans, entries); err != nil {
				return nil, err
			}
			// A partial answer takes the entries the user did not accept out of the run, as
			// installed, and plans the rest again from the same staged trees: the summary is the
			// apply (Plan 6a §9.3 step 7).
			if len(acc.leave) > 0 {
				entries = slices.DeleteFunc(entries, func(se *syncEntry) bool { return slices.Contains(acc.leave, shown(se.ent.Name)) })
				// Clones: the report the user answered keeps every change it showed.
				res.Removals = slices.DeleteFunc(slices.Clone(res.Removals), func(r Removal) bool { return slices.Contains(acc.leave, r.Module) })
				res.Trust.Changes = slices.DeleteFunc(slices.Clone(res.Trust.Changes), func(c TrustChange) bool { return slices.Contains(acc.leave, c.Module) })
				if !acc.targets {
					res.Trust.Targets = nil
				}
				res.Notices = append(res.Notices, Notice{Level: LevelWarn,
					Text: "left as installed until you trust them: " + nameList(acc.leave) + " → " + e.SyncCommand(sc)})
				if sp, err = e.planSync(sc, entries, led, sel, opts, probes, req.Modules); err != nil {
					return nil, err
				}
			}
		}
	}
	res.Modules, res.Findings, res.Notices = sp.modules, append(res.Findings, sp.findings...), append(res.Notices, sp.notices...)
	pinned := pinsOf(entries)
	for _, se := range entries {
		if se.pin != nil {
			res.Pins = append(res.Pins, *se.pin)
		}
	}
	// Publish what the user trusts now (Plan 6a §9.3 step 8); a declined entry's content is never
	// committed. A dry run publishes nothing more, except that --update -n at user scope, whose
	// user.yml is the user's own, fetches into the cache and moves heads, as -n always may there.
	for _, se := range entries {
		if opts.DryRun && (sc.Kind != target.User || !se.update) {
			continue
		}
		if err := e.publish(se); err != nil {
			return nil, err
		}
	}

	// Removals are confirmed before any hook consent is asked or recorded (Plan 6a §9.3 step 9).
	changes, copies := sp.changes, sp.copies
	for _, r := range res.Removals {
		if r.WholeModule {
			changes = append(changes, plan.ModuleChange{Name: r.Module, Targets: allTargets(led.Modules[r.Module], nil, &manifest.Manifest{})})
		}
	}
	if len(res.Removals) > 0 && !opts.DryRun {
		if req.ConfirmRemovals == nil { // refused at step 4 already; kept so no path removes unasked
			return nil, e.syncRefusal(sc, req, TrustReport{Status: res.Trust.Status, Changes: []TrustChange{}}, res.Removals, false, true, entries)
		}
		yes, err := req.ConfirmRemovals(slices.Clone(res.Removals))
		if err != nil {
			return nil, err
		}
		if !yes {
			return nil, e.declineRemovals(sc, man, led, rec, acc, entries)
		}
	}
	res.Findings = append(res.Findings, probes.findings...)

	pr, err := e.prepare(sc, led, changes, copies, nil, opts, res.Result)
	if err != nil {
		return nil, err
	}
	keepUnhooked(copies, pr.jobs, opts)
	res.InSync = len(res.Steps) == 0 && len(res.Pins) == 0 && len(res.Removals) == 0 &&
		!slices.ContainsFunc(copies, func(c moduleCopy) bool { return c.replace })
	for _, st := range res.Steps {
		res.Changed = append(res.Changed, st.Module)
	}
	for _, p := range res.Pins {
		res.Changed = append(res.Changed, p.Module)
	}
	for _, r := range res.Removals {
		res.Changed = append(res.Changed, r.Module)
	}
	for _, c := range copies {
		if c.replace {
			res.Changed = append(res.Changed, c.name)
		}
	}
	slices.Sort(res.Changed)
	res.Changed = slices.Compact(res.Changed)
	if opts.DryRun {
		// A dry run ends with the command that applies it: the user's own run without -n, and
		// the flags that answer its questions off a terminal (never --allow-hooks: consent is the
		// user's to give).
		needTrust := res.Trust.Status != trust.StatusTrusted && res.Trust.Status != TrustStatusNotApplicable
		if !res.InSync || needTrust {
			res.ToApply = e.SyncCommand(sc, req.rerun()...)
			var answers []string
			if needTrust {
				answers = append(answers, "--trust")
			}
			if len(res.Removals) > 0 {
				answers = append(answers, "-y")
			}
			res.EquivalentCommands = []string{res.ToApply}
			if len(answers) > 0 {
				res.OffTerminal = e.SyncCommand(sc, req.rerun(answers...)...)
				res.EquivalentCommands = []string{res.OffTerminal}
			}
		}
		return res, nil
	}
	apply := func(mw *plan.ManifestWrite) error {
		return e.commit(ctx, pr, opts, res.Result, mw, func(*plan.Plan) error { return e.makePluginData(sc, changes) })
	}
	// The trust record is written once, after the apply: it records the entries the user accepted,
	// and the top-level targets when they were accepted, as lola saves them; every other entry
	// keeps its record (Plan 6a §7.4).
	var trusted *trust.Record
	if acc != nil {
		trusted = &trust.Record{View: trust.Accept(rec.View, trust.ViewOf(man), acc.modules, acc.targets)}
	}
	if len(pinned) > 0 {
		err = e.saveManifest(sc, man, manifest.Change{Set: pinned}, trusted, "the project's files, lock.json and project.yml were updated",
			syncAgain(e.SyncCommand(sc)), apply)
	} else if err = apply(nil); err == nil && trusted != nil {
		if err = e.Trust.Save(sc.ProjectDir, trusted.View); err != nil {
			msg := "lola could not record that you trust project.yml"
			if !res.InSync {
				msg = "the project's files and lock.json were updated, but " + msg
			}
			err = errs.WithHints(fmt.Errorf("%s: %w", msg, err), syncAgain(e.SyncCommand(sc)))
		}
	}
	if err != nil {
		return nil, err
	}
	res.Trust.Recorded = trusted != nil
	for i := range res.Pins {
		res.Pins[i].Written = true
	}
	if acc != nil {
		res.Equivalent = e.equivalent(req, acc, res)
	}
	return res, nil
}

// equivalent is the command that gives a run's trust answer (acc) off a terminal (Plan 6a §7.5,
// §19 X2): Trust all is the user's own run with --trust; a partial answer names every entry it
// accepted, whose record changes even when nothing is rendered, and every module the run changed,
// so a run limited to them applies the same plan and records the same trust. -y is added when the
// run's removals were confirmed.
func (e *Engine) equivalent(req SyncRequest, acc *acceptance, res *SyncResult) string {
	run := req
	if !acc.all {
		run.Modules = slices.Clone(res.Changed)
		for _, m := range acc.modules {
			run.Modules = append(run.Modules, shown(m))
		}
		slices.Sort(run.Modules)
		run.Modules = slices.Compact(run.Modules)
	}
	var yes []string
	if len(res.Removals) > 0 {
		yes = append(yes, "-y")
	}
	return e.SyncCommand(req.Scope, append([]string{"--trust"}, run.rerun(yes...)...)...)
}

// pinsOf is each entry of the run with the new pin the sync writes for it.
func pinsOf(entries []*syncEntry) []manifest.Entry {
	var out []manifest.Entry
	for _, se := range entries {
		if se.pin != nil {
			out = append(out, se.pinned())
		}
	}
	return out
}

// syncPlan is a sync's plan of the entries of its run: the module changes and copies the apply
// uses, and what planning reported.
type syncPlan struct {
	changes  []plan.ModuleChange
	copies   []moduleCopy
	modules  []ModuleResult
	findings []target.Finding
	notices  []Notice
}

// planSync plans each entry from the bytes that were hashed, its snapshot or staged tree (Plan 6a
// §9.3 step 10): its intents for its effective targets and selections, the change that renders
// them over the targets lock.json has it on, and its module copy, replaced (running its hooks)
// when its version changes, the copy is gone or it adds a target, or to offer a pending post-install hook to a run
// that names its module (named) or gives --allow-hooks.
func (e *Engine) planSync(sc Scope, entries []*syncEntry, led *ledger.Ledger, sel *config.Selections, opts Options, probes *probeRun,
	named []string) (syncPlan, error) {
	sp := syncPlan{modules: []ModuleResult{}}
	for _, se := range entries {
		name := se.ent.Name
		ent := se.pinned()
		x, err := exclusions(sel, sc, name)
		if err != nil {
			return syncPlan{}, err
		}
		intents, finds, notes, mr, err := e.planModule(sc, se.v, se.targets, &ent, x, opts, probes)
		if err != nil {
			return syncPlan{}, err
		}
		sp.modules, sp.findings, sp.notices = append(sp.modules, mr), append(sp.findings, finds...), append(sp.notices, notes...)
		installed := led.Modules[name]
		ts := se.targets
		for _, t := range allTargets(installed, nil, &manifest.Manifest{}) {
			if !slices.Contains(ts, t) {
				ts = append(slices.Clone(ts), t)
			}
		}
		sp.changes = append(sp.changes, plan.ModuleChange{Name: name, Targets: ts, Intents: intents,
			Entry: &ledger.Entry{Source: ent.Source, Ref: ent.Ref, Path: ent.Path, Commit: se.v.Commit, Digest: se.v.Meta.Digest, Targets: mr.Targets}})
		replace, err := needsCopy(e.files(sc).modules, name, installed, se.v.Meta.Digest)
		if err != nil {
			return syncPlan{}, err
		}
		// A target the module is not installed on has never had its hooks run: the copy is
		// replaced to run them, as for a new version.
		replace = replace || addsTargets(installed, mr.Targets)
		// A pending post-install hook runs only in a copy replaced from the hashed version, after
		// the consent keyed on those bytes (Plan 6a §9.3 step 10, C1), and is offered only to a
		// run that names its module or gives --allow-hooks: any other run keeps it pending,
		// asking nothing, and says how to run it.
		pending := installed != nil && installed.HooksPending && declaresPostInstall(se.v.Module.DeclaredHooks)
		if pending && !replace && !opts.AllowHooks && !slices.Contains(named, name) {
			text := "pending post-install hook for " + name + " → " + e.SyncCommand(sc, name)
			if opts.Ask == nil {
				text += " (off a terminal: " + e.SyncCommand(sc, name, "--allow-hooks") + ")"
			}
			sp.notices = append(sp.notices, Notice{Level: LevelWarn, Text: text})
			pending = false
		}
		sp.copies = append(sp.copies, moduleCopy{name: name, src: se.v.ContentDir, replace: replace || pending,
			hooks: se.v.Module.DeclaredHooks, targets: mr.Targets, runHooks: replace || pending, forced: pending && !replace,
			pending: pending})
	}
	return sp, nil
}

// acceptance is the user's trust answer applied to a run (Plan 6a §7.4, §9.3 step 7): the
// entries accepted, by their names in the record and the manifest; whether the top-level targets
// are; and the modules that leave the run as installed, by the names the report shows.
type acceptance struct {
	all     bool
	modules []string
	targets bool
	leave   []string
}

// accept applies ans to rep, the run's report (raw, the check of the manifest as it would be
// saved, whose changes rep's shown ones extend with folders and pins). All accepts every change
// and the targets. Otherwise the changes ans names are accepted; the top-level targets are
// accepted with an accepted added or changed entry that has no targets of its own, since it
// installs to them, never with a removed one; and the run loses the changes not accepted and,
// when changed top-level targets are not accepted, every entry that installs to them. An answer
// accepting nothing is a Cancel.
func accept(raw trust.Report, rep TrustReport, ans TrustAnswer, entries []*syncEntry) (*acceptance, error) {
	names := map[string]string{} // shown → raw
	for _, c := range raw.Changes {
		names[shown(c.Module)] = c.Module
	}
	for _, se := range entries {
		names[shown(se.ent.Name)] = se.ent.Name
	}
	acc := &acceptance{all: ans.All, targets: rep.Targets != nil && ans.All}
	for _, c := range rep.Changes {
		if !ans.All && !slices.Contains(ans.Modules, c.Module) {
			acc.leave = append(acc.leave, c.Module)
			continue
		}
		acc.modules = append(acc.modules, names[c.Module])
		k := slices.IndexFunc(entries, func(se *syncEntry) bool { return shown(se.ent.Name) == c.Module })
		if rep.Targets != nil && c.Kind != trust.ChangeRemoved && k >= 0 && len(entries[k].ent.Targets) == 0 {
			acc.targets = true
		}
	}
	if len(acc.modules) == 0 && !acc.targets {
		return nil, ErrDeclined
	}
	if rep.Targets != nil && !acc.targets {
		for _, se := range entries {
			if len(se.ent.Targets) == 0 && !slices.Contains(acc.leave, shown(se.ent.Name)) {
				acc.leave = append(acc.leave, shown(se.ent.Name))
			}
		}
	}
	slices.Sort(acc.leave)
	return acc, nil
}

// RemovalsDeclinedError is a sync whose removals the user declined (Plan 6a §9.3 step 9): nothing
// was applied. Recorded says the trust the user accepted changed the record, so the next sync
// does not ask about it again; Again names the accepted entries it does ask about again, whose
// pin or folder content the record holds only once lola writes it; Render says the record
// changes what lola render installs.
type RemovalsDeclinedError struct {
	Recorded, Render bool
	Again            []string
}

func (e *RemovalsDeclinedError) Error() string {
	return "removals declined; the project was not changed"
}
func (e *RemovalsDeclinedError) Unwrap() error { return ErrDeclined }

// declineRemovals is a No to a run's removals (Plan 6a §9.3 step 9, §19 X6): nothing is applied,
// but the trust the user accepted (acc, nil when nothing was asked) is recorded, without the pins
// lola did not write: the answer stands, and the next sync asks again only about the removals and
// the accepted entries (of entries, the run's) whose new pin or folder content it did not install.
func (e *Engine) declineRemovals(sc Scope, man *manifest.Manifest, led *ledger.Ledger, rec *trust.Record, acc *acceptance,
	entries []*syncEntry) error {
	if acc == nil {
		return &RemovalsDeclinedError{}
	}
	view := trust.Accept(rec.View, trust.ViewOf(man), acc.modules, acc.targets)
	if err := e.Trust.Save(sc.ProjectDir, view); err != nil {
		return errs.WithHints(fmt.Errorf("lola could not record that you trust project.yml: %w", err), syncAgain(e.SyncCommand(sc)))
	}
	was, err := rec.View.Hash()
	if err != nil {
		return err
	}
	now, err := view.Hash()
	if err != nil {
		return err
	}
	d := &RemovalsDeclinedError{Recorded: !rec.Exists || now != was, Render: e.rendersOther(sc, rec.View, view, man, led, entries)}
	for _, se := range entries {
		installed := led.Modules[se.ent.Name]
		folder := se.spec.Kind == source.KindFolder && (installed == nil || installed.Digest != se.v.Meta.Digest)
		if slices.Contains(acc.modules, se.ent.Name) && (se.pin != nil || folder) {
			d.Again = append(d.Again, shown(se.ent.Name))
		}
	}
	return d
}

// rendersOther reports whether trusting view instead of old changes what render installs (Plan
// 6a §10.3), by render's own check: an installed module whose entry asks for the source lock.json
// records, that view covers (and, without targets of its own, covers the top-level targets) and
// old did not, is no longer left out. A folder this run did not stage, or staged with content
// that differs from the version installed, or whose cached source moved, stays left out,
// whatever the record says.
func (e *Engine) rendersOther(sc Scope, old, view trust.View, man *manifest.Manifest, led *ledger.Ledger, entries []*syncEntry) bool {
	rendered := func(v trust.View, ent manifest.Entry) bool {
		return v.Covers(ent) && (len(ent.Targets) > 0 || slices.Equal(v.Targets, man.Targets))
	}
	for _, ent := range man.Modules {
		in := led.Modules[ent.Name]
		if in == nil || ent.Source != in.Source || ent.Ref != in.Ref || ent.Path != in.Path || !rendered(view, ent) || rendered(old, ent) {
			continue
		}
		spec, ok, err := source.Parse(ent.Source, canonicalRoot(sc.ProjectDir))
		folder := err == nil && ok && spec.Kind == source.KindFolder
		if folder {
			// A folder this run did not stage (declined, or outside the run) may have changed content:
			// render would leave it out, so it is not claimed.
			k := slices.IndexFunc(entries, func(se *syncEntry) bool { return se.ent.Name == ent.Name })
			if k < 0 || entries[k].v.Meta.Digest != in.Digest {
				continue
			}
		}
		if moved, err := e.folderMoved(sc, ent.Name, in); err == nil && moved == "" {
			return true
		}
	}
	return false
}

// trustReport is the report of a trust check, every manifest value through untrusted.Text.
func trustReport(rep trust.Report) TrustReport {
	out := TrustReport{Status: rep.Status, Changes: []TrustChange{}}
	if rep.Targets != nil {
		out.Targets = &trust.FieldChange{Field: rep.Targets.Field, From: shownValue(rep.Targets.From), To: shownValue(rep.Targets.To)}
	}
	for _, c := range rep.Changes {
		c.Module = shown(c.Module)
		fields := make([]trust.FieldChange, len(c.Fields))
		for i, fc := range c.Fields {
			fields[i] = trust.FieldChange{Field: fc.Field, From: shownValue(fc.From), To: shownValue(fc.To)}
		}
		c.Fields = fields
		out.Changes = append(out.Changes, TrustChange{Change: c, Files: []FileChange{}, MCPServers: []ContentMCPServer{}, Hooks: []ContentHook{}})
	}
	return out
}

// notFixed marks the changes of entries whose manifest entry does not pin its content: a git or
// archive entry without a digest, which installs what its ref serves when it is resolved, even
// if the report shows the pin the sync is about to write (Plan 6a §4.2, §7.3).
func notFixed(rep *TrustReport, entries []*syncEntry) {
	for i := range rep.Changes {
		c := &rep.Changes[i]
		k := slices.IndexFunc(entries, func(se *syncEntry) bool { return se.ent.Name == c.Module })
		if k < 0 {
			continue
		}
		se := entries[k]
		c.ContentFixed = se.ent.Digest != ""
		if !c.ContentFixed && se.spec.Kind != source.KindFolder {
			c.Note = "content not fixed; installs what " + served(se) + " serves now"
		}
	}
}

// served names what an entry's source serves: its ref, the default branch of a git source
// without one, or the file or URL of an archive, which has no ref.
func served(se *syncEntry) string {
	switch {
	case se.spec.Kind != source.KindGit:
		return shown(se.ent.Source)
	case se.ent.Ref == "":
		return "the default branch"
	}
	return shown(se.ent.Ref)
}

// pinReasons gives each change of an entry the run writes a pin for its reason (Plan 6a §7.5):
// re-pin from its ref under --update, or pin what its ref serves now; an entry whose manifest
// entry is as trusted gets a change of its own, so the diff names it. Before anything is fetched
// (fetched false) the pin is not known, which the reason says. A folder whose content changed is
// given its reason too.
func pinReasons(rep *TrustReport, entries []*syncEntry, fetched bool) {
	for _, se := range entries {
		var reason string
		switch {
		case se.folderChanged && se.notInstalled:
			reason = "not installed yet"
		case se.folderChanged:
			reason = "content changed since lola installed it"
		case !needsPin(se) || fetched && se.pin == nil:
			continue
		case se.update && fetched:
			reason = "re-pin from " + served(se) + " (--update)"
		case se.update:
			reason = "re-pin from " + served(se) + " (--update; not fetched yet)"
		case fetched:
			reason = "pin what " + served(se) + " serves now"
		default:
			reason = "pin what " + served(se) + " serves now (not fetched yet)"
		}
		k := slices.IndexFunc(rep.Changes, func(c TrustChange) bool { return c.Module == shown(se.ent.Name) })
		if k < 0 {
			rep.Changes = append(rep.Changes, TrustChange{Change: trust.Change{Module: shown(se.ent.Name), Kind: trust.ChangeChanged,
				ContentFixed: se.ent.Digest != "", Fields: []trust.FieldChange{}}, Files: []FileChange{}, MCPServers: []ContentMCPServer{}, Hooks: []ContentHook{}})
			k = len(rep.Changes) - 1
			if rep.Status == trust.StatusTrusted {
				rep.Status = trust.StatusChanged
			}
		}
		rep.Changes[k].Reason = reason
		if fetched {
			rep.Changes[k].Pin = se.pin
		}
	}
	slices.SortStableFunc(rep.Changes, func(a, b TrustChange) int { return strings.Compare(a.Module, b.Module) })
}

// shownValue is a field value of a trust change with its strings through untrusted.Text.
func shownValue(v any) any {
	switch v := v.(type) {
	case string:
		return shown(v)
	case []string:
		out := make([]string, len(v))
		for i, s := range v {
			out[i] = shown(s)
		}
		return out
	}
	return v
}

// syncRefusal refuses a sync that needs answers it cannot ask for (Plan 6a §9.3 step 4, §11.3),
// naming in one error every answer missing and every cause: the user's trust (trustMissing) for
// the manifest, the pins the sync would write and the folders whose content changed, and the
// confirmation of the removals (removalsMissing), the answers joined by "; and" so a list of
// names never runs into the next answer. The hints repeat the user's run (req) at its
// scope with the flags that answer, so the user is never sent from one refusal to the next. The
// error carries the report and the removals for the caller to show.
func (e *Engine) syncRefusal(sc Scope, req SyncRequest, rep TrustReport, removals []Removal, trustMissing, removalsMissing bool,
	entries []*syncEntry) error {
	var answers, hints []string
	yesGiven, trustGiven := req.ConfirmRemovals != nil, req.Trust != nil
	if trustMissing {
		manifestPath := e.files(sc).manifest
		name := filepath.Join(filepath.Base(filepath.Dir(manifestPath)), filepath.Base(manifestPath))
		var causes, pins, folders []string
		switch {
		case !rep.ManifestChanged:
		case rep.Status == trust.StatusNew:
			causes = append(causes, name+", which has not been trusted on this machine")
		default:
			causes = append(causes, "changes to "+name)
		}
		for _, se := range entries {
			switch {
			case se.folderChanged:
				folders = append(folders, shown(se.ent.Name))
			case needsPin(se):
				pins = append(pins, shown(se.ent.Name))
			}
		}
		if len(pins) > 0 {
			causes = append(causes, "the pins it would write for "+strings.Join(pins, ", "))
		}
		if len(folders) > 0 {
			causes = append(causes, "the changed content of "+strings.Join(folders, ", "))
		}
		answers = append(answers, "your trust for "+andJoin(causes))
	}
	if removalsMissing {
		var names []string
		for _, r := range removals {
			names = append(names, r.Module)
		}
		answers = append(answers, "your confirmation to remove "+strings.Join(names, ", "))
	}
	hints = append(hints, "see them: "+e.SyncCommand(sc, req.rerun("-n")...))
	switch {
	case trustMissing && (removalsMissing || yesGiven):
		hints = append(hints, "then accept them: "+e.SyncCommand(sc, req.rerun("--trust", "-y")...))
	case trustMissing && len(req.Modules) == 1:
		hints = append(hints, "then accept them: "+e.SyncCommand(sc, req.rerun("--trust")...))
	case trustMissing:
		flags := req
		flags.Modules = nil
		hints = append(hints, "then accept them: "+e.SyncCommand(sc, req.rerun("--trust")...),
			"or accept some: "+e.SyncCommand(sc, flags.rerun("--trust")...)+" <module>…")
	case trustGiven:
		hints = append(hints, "then confirm them: "+e.SyncCommand(sc, req.rerun("--trust", "-y")...))
	default:
		hints = append(hints, "then confirm them: "+e.SyncCommand(sc, req.rerun("-y")...))
	}
	for _, se := range entries {
		if trustMissing && se.folderChanged {
			hints = append(hints, "edited it yourself? "+e.installFolder(sc, se.spec.Location))
		}
	}
	if trustMissing && yesGiven {
		hints = append(hints, "-y confirms removals only; accept the changes with --trust")
	}
	return errs.WithHints(&TrustRefusalError{Report: rep, Removals: removals,
		msg: "lola sync needs " + strings.Join(answers, "; and ") + ", and there is no terminal to ask on"}, hints...)
}

// andJoin joins phrases as prose: "a", "a and b", "a, b and c".
func andJoin(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// syncRemovals is what the run removes (Plan 6a §9.3 step 9), from lock.json and the manifest
// alone: the targets each entry of the run no longer has, then each installed module of the run
// the manifest no longer lists, whole.
func (e *Engine) syncRemovals(sc Scope, man *manifest.Manifest, led *ledger.Ledger, entries []*syncEntry, limit syncLimit) ([]Removal, error) {
	out := []Removal{}
	for _, se := range entries {
		r, err := e.removal(sc, se.ent.Name, led.Modules[se.ent.Name], se.targets)
		if err != nil {
			return nil, err
		}
		if r != nil {
			out = append(out, *r)
		}
	}
	for _, name := range sortedNames(led) {
		if man.Find(name) == nil && limit.has(name) {
			r, err := e.removal(sc, name, led.Modules[name], nil)
			if err != nil {
				return nil, err
			}
			out = append(out, *r)
		}
	}
	return out, nil
}

// containLocalSources refuses an entry whose local folder or archive resolves outside the
// project (Plan 6a §8): a pulled project.yml, or a pulled link, must not make lola read the
// user's other files. It checks every entry the trust record does not cover, and every covered
// one whose source as written names a path inside the project, which a pulled link can lead
// out of; a covered source outside the project is one the user installed. Locations are
// canonical (source.Parse); a root or location that cannot be resolved is refused, never let
// through.
func (e *Engine) containLocalSources(sc Scope, entries []*syncEntry) error {
	var root string
	for _, se := range entries {
		local := source.IsLocal(se.spec.Kind, se.spec.Location)
		if !local || se.covered && !lexicallyInside(sc, se.ent.Source) {
			continue
		}
		if root == "" {
			r, err := filepath.EvalSymlinks(sc.ProjectDir)
			if err != nil {
				return fmt.Errorf("resolving the project root: %w", err)
			}
			root = r
		}
		in, err := source.IsWithin(root, se.spec.Location)
		if err != nil {
			return fmt.Errorf("%s: resolving its local source: %w", se.ent.Name, untrusted.CleanErr(err))
		}
		if !in {
			// Validation refused any other source the cache has for the name, except a local one
			// that is gone, which the install replaces (§6.5): the install works as hinted.
			return errs.WithHints(fmt.Errorf("%s: local source %s resolves outside the project (%s)", se.ent.Name,
				untrusted.Text(se.ent.Source, untrusted.MaxName), untrusted.Text(se.spec.Location, untrusted.MaxName)),
				"install it yourself if you trust it: "+e.Command(sc, "install", installArgs(se.spec.Location, "", se.ent.Path)...))
		}
	}
	return nil
}

// lexicallyInside reports whether a local source, as written in the manifest, names a path
// inside the project root before any symbolic link is followed: a relative path that does not
// climb out, or an absolute path or file URL below the root as given or as resolved. A file URL
// that does not parse counts as inside, so it is checked.
func lexicallyInside(sc Scope, src string) bool {
	p := src
	if strings.HasPrefix(src, "file://") {
		u, err := url.Parse(src)
		if err != nil {
			return true
		}
		p = u.Path
		// file:///C:/x has the path /C:/x; on Windows the drive is the path's start.
		if v := strings.TrimPrefix(p, "/"); filepath.VolumeName(v) != "" {
			p = v
		}
	}
	if !filepath.IsAbs(p) {
		return filepath.IsLocal(p)
	}
	for _, root := range []string{sc.ProjectDir, canonicalRoot(sc.ProjectDir)} {
		if rel, err := filepath.Rel(root, p); err == nil && filepath.IsLocal(rel) {
			return true
		}
	}
	return false
}

// folderChanges adds to rep, as a change of its digest, each covered folder whose content is not
// the version lock.json records (an addition when lock.json records none), and makes a trusted report
// changed: the user trusts what an in-repo module holds by trusting the version installed.
func folderChanges(rep *TrustReport, entries []*syncEntry, led *ledger.Ledger) {
	for _, se := range entries {
		if !se.folderChanged {
			continue
		}
		var from any
		kind := trust.ChangeAdded
		if installed := led.Modules[se.ent.Name]; installed != nil {
			from, kind = shown(installed.Digest), trust.ChangeChanged
		}
		rep.Changes = append(rep.Changes, TrustChange{Change: trust.Change{Module: se.ent.Name, Kind: kind,
			Fields: []trust.FieldChange{{Field: "digest", From: from, To: se.v.Meta.Digest}}},
			FolderContent: true, Files: []FileChange{}, MCPServers: []ContentMCPServer{}, Hooks: []ContentHook{}})
		if rep.Status == trust.StatusTrusted {
			rep.Status = trust.StatusChanged
		}
	}
	slices.SortStableFunc(rep.Changes, func(a, b TrustChange) int { return strings.Compare(a.Module, b.Module) })
}

// folderEdits gives each folder whose content changed the diff's lines (Plan 6a §7.5): how to
// review the edits when the folder is in a git work tree, and how to record the content as the
// user's own when they made the edits.
func (e *Engine) folderEdits(sc Scope, rep *TrustReport, entries []*syncEntry) error {
	for i := range rep.Changes {
		c := &rep.Changes[i]
		k := slices.IndexFunc(entries, func(se *syncEntry) bool { return shown(se.ent.Name) == c.Module })
		if !c.FolderContent || k < 0 {
			continue
		}
		loc := entries[k].spec.Location
		inTree, err := source.InWorkTree(loc)
		if err != nil {
			return err
		}
		if inTree {
			c.ReviewEdits = "review the edits: " + e.reviewFolder(loc)
		}
		c.InstallOwn = "edited it yourself? " + e.installFolder(sc, loc)
	}
	return nil
}

// needsPin reports whether a sync writes a pin for the entry: a git or archive entry under
// --update or without one; a folder is never pinned. Each needs the user's trust at project
// scope (Plan 6a §7.3).
func needsPin(se *syncEntry) bool {
	return se.spec.Kind != source.KindFolder && (se.update || se.ent.Digest == "")
}

// sortedNames is the ledger's module names in order.
func sortedNames(led *ledger.Ledger) []string {
	names := make([]string, 0, len(led.Modules))
	for n := range led.Modules {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// refuseMissingManifest refuses a sync of a scope with no manifest: there is nothing it could
// make the scope match. An existing manifest with no modules means "remove everything".
func (e *Engine) refuseMissingManifest(sc Scope) error {
	path := e.files(sc).manifest
	_, err := os.Lstat(path)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if sc.Kind == target.User {
		return errs.WithHints(fmt.Errorf("no user.yml at %s", untrusted.Text(path, untrusted.MaxName)),
			"install a module at user scope first: lola install --user <source>")
	}
	return errs.WithHints(fmt.Errorf("no project.yml in %s", untrusted.Text(sc.ProjectDir, untrusted.MaxName)),
		"start one: lola install <source>", "or sync another project: lola sync -C <dir>")
}

// syncLimit is the set of modules a sync acts on (Plan 6a §3.1): the names given, or every
// module when none was.
type syncLimit struct{ names []string }

func (l syncLimit) has(name string) bool { return len(l.names) == 0 || slices.Contains(l.names, name) }

// limitTo is the run names limit sync to, each of them an entry of the scope's manifest or a
// module installed there that the manifest no longer lists; any other name is refused before
// anything else, listing both.
func (e *Engine) limitTo(sc Scope, man *manifest.Manifest, led *ledger.Ledger, names []string) (syncLimit, error) {
	var l syncLimit
	for _, name := range names {
		if man.Find(name) == nil && led.Modules[name] == nil {
			base := filepath.Base(e.files(sc).manifest)
			listed, gone := []string{}, []string{}
			for _, ent := range desired(man) {
				listed = append(listed, shown(ent.Name))
			}
			for _, n := range sortedNames(led) {
				if man.Find(n) == nil {
					gone = append(gone, shown(n))
				}
			}
			hints := []string{"modules in " + base + ": " + strings.Join(listed, ", ")}
			if len(listed) == 0 {
				hints[0] += "none"
			}
			if len(gone) > 0 {
				hints = append(hints, "installed but no longer listed: "+strings.Join(gone, ", "))
			}
			return syncLimit{}, errs.WithHints(fmt.Errorf("%s is neither in %s nor installed here", untrusted.Text(name, untrusted.MaxName), base), hints...)
		}
		if !slices.Contains(l.names, name) {
			l.names = append(l.names, name)
		}
	}
	return l, nil
}

// runReport is rep limited to the run (Plan 6a §3.1): the changes of the modules it names, and
// the top-level targets only when an entry of the run has no targets of its own, since it
// installs to them. An unlimited run's report is rep.
func runReport(rep trust.Report, limit syncLimit, entries []*syncEntry) trust.Report {
	if len(limit.names) == 0 {
		return rep
	}
	out := trust.Report{Status: rep.Status, Changes: []trust.Change{}}
	for _, c := range rep.Changes {
		if limit.has(c.Module) {
			out.Changes = append(out.Changes, c)
		}
	}
	if slices.ContainsFunc(entries, func(se *syncEntry) bool { return len(se.ent.Targets) == 0 }) {
		out.Targets = rep.Targets
	}
	if out.Targets == nil && len(out.Changes) == 0 {
		out.Status = trust.StatusTrusted
	}
	return out
}

// outOfSync is the notice of a limited run about the modules it left out that are not in sync,
// by a local check of the manifest against lock.json (Plan 6a §9.3 step 11): an entry not
// installed or pinned to another version than the one installed, and a module installed but no
// longer listed. ok is false for an unlimited run and when there are none.
func outOfSync(man *manifest.Manifest, led *ledger.Ledger, limit syncLimit, sync string) (n Notice, ok bool) {
	if len(limit.names) == 0 {
		return Notice{}, false
	}
	var names []string
	for _, ent := range desired(man) {
		in := led.Modules[ent.Name]
		if !limit.has(ent.Name) && (in == nil || ent.Digest != "" && ent.Digest != in.Digest) {
			names = append(names, ent.Name)
		}
	}
	for _, name := range sortedNames(led) {
		if !limit.has(name) && man.Find(name) == nil {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return Notice{}, false
	}
	slices.Sort(names)
	what := fmt.Sprintf("%d other modules are", len(names))
	if len(names) == 1 {
		what = "1 other module is"
	}
	return Notice{Level: LevelWarn, Text: what + " not in sync: " + nameList(names) + " → " + sync}, true
}

// nameList joins module names for a notice, each through untrusted.Text, at most 10 of them and
// then how many more there are.
func nameList(names []string) string {
	const most = 10
	shownNames := make([]string, 0, min(len(names), most))
	for _, n := range names[:min(len(names), most)] {
		shownNames = append(shownNames, untrusted.Text(n, untrusted.MaxName))
	}
	out := strings.Join(shownNames, ", ")
	if len(names) > most {
		out += fmt.Sprintf(" and %d more", len(names)-most)
	}
	return out
}

// validateSync checks every entry of the run before anything is fetched or asked (Plan 6a §9.3
// step 2), stopping at the first failure: the source parses and passes source.Validate with the
// entry's ref and path; the pin fits the source's kind; the cache holds no other source under
// the name (§6.5); the effective targets are known.
func (e *Engine) validateSync(sc Scope, man *manifest.Manifest, req SyncRequest, limit syncLimit) ([]*syncEntry, error) {
	base := filepath.Base(e.files(sc).manifest)
	var out []*syncEntry
	for _, ent := range desired(man) {
		name := ent.Name
		if !limit.has(name) {
			continue
		}
		spec, ok, err := source.Parse(ent.Source, sourceRoot(sc))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if !ok {
			return nil, errs.WithHints(fmt.Errorf("%s: %s is not a source", name, untrusted.Text(ent.Source, untrusted.MaxName)),
				"sources are "+source.AcceptedForms)
		}
		spec.Ref, spec.Path = ent.Ref, ent.Path
		if err := source.Validate(spec); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if err := checkPinShape(name, base, spec.Kind, ent, e.SyncCommand(sc, "--update", name)); err != nil {
			return nil, err
		}
		gone, err := e.refuseOtherSource(sc, name, spec, e.SyncCommand(sc, req.rerun()...))
		if err != nil {
			return nil, err
		}
		ts := ent.Targets
		if len(ts) == 0 {
			ts = man.Targets
		}
		if len(ts) == 0 {
			return nil, errs.WithHints(fmt.Errorf("%s: %s gives it no targets", name, base),
				"list the assistants in "+base+", for example targets: [claude-code]")
		}
		for _, id := range ts {
			if _, err := e.target(id); err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
		}
		out = append(out, &syncEntry{ent: ent, goneFrom: gone, spec: spec, targets: ts, update: req.Update})
	}
	return out, nil
}

// checkPinShape refuses a ref or a pin the entry's kind of source cannot have (Plan 6a §4.1): a
// ref on a folder or archive, a pin on a folder, a commit on an archive, a git digest without
// its commit. update is the sync command that re-pins the entry.
func checkPinShape(name, base string, kind source.Kind, ent manifest.Entry, update string) error {
	update = "re-pin it: " + update
	switch {
	case ent.Ref != "" && kind != source.KindGit:
		return errs.WithHints(fmt.Errorf("%s: its source is a %s, which has no ref", name, kind), "drop ref from its entry in "+base)
	case kind == source.KindFolder && (ent.Commit != "" || ent.Digest != ""):
		return errs.WithHints(fmt.Errorf("%s: its source is a folder, and folders are never pinned", name),
			"drop commit and digest from its entry in "+base)
	case kind == source.KindArchive && ent.Commit != "":
		return errs.WithHints(fmt.Errorf("%s: its source is an archive, which is pinned by its digest alone", name),
			"drop commit from its entry in "+base, update)
	case kind == source.KindGit && ent.Digest != "" && ent.Commit == "":
		return errs.WithHints(fmt.Errorf("%s: its digest is pinned without the commit it came from", name), update)
	}
	return nil
}

// refuseOtherSource refuses an entry whose name the cache holds from another source or path
// (Plan 6a §6.5): the cache keeps one source per name, for every project on the machine. again
// is the user's sync, which the hint repeats once the name is out of the cache. A local source
// the cache records that no longer exists, for a local entry, is not refused: its description
// (describeSource) is returned, and the version the run publishes replaces it.
func (e *Engine) refuseOtherSource(sc Scope, name string, spec source.Spec, again string) (string, error) {
	o, found, err := e.Store.Origin(name)
	if err != nil || !found {
		return "", err
	}
	if o.Type == string(spec.Kind) && source.SameLocation(spec.Kind, o.Source, spec.Location) && o.Path == spec.Path {
		return "", nil
	}
	if o.LocalGone() && source.IsLocal(spec.Kind, spec.Location) {
		return describeSource(o.Source, o.Path), nil
	}
	return "", errs.WithHints(fmt.Errorf("%s in your cache comes from %s, and %s asks for %s; the cache is shared by every project on this machine",
		name, describeSource(o.Source, o.Path), filepath.Base(e.files(sc).manifest), describeSource(spec.Location, spec.Path)),
		"remove it from your cache: "+errs.ShellJoin([]string{"lola", "cache", "rm", name})+" (add -y off a terminal)", "then: "+again)
}

// goneNotices are the notices of the entries whose cached local source no longer exists, which
// the run caches from the entry's source instead (Plan 6a §6.5).
func goneNotices(entries []*syncEntry) []Notice {
	var out []Notice
	for _, se := range entries {
		if se.goneFrom != "" {
			out = append(out, Notice{Level: LevelWarn, Text: fmt.Sprintf("%s: your cache has it from %s, which no longer exists; caching it from %s",
				se.ent.Name, se.goneFrom, describeSource(se.spec.Location, se.spec.Path))})
		}
	}
	return out
}

// describeSource names a source and its path for a message; both may be someone else's.
func describeSource(location, path string) string {
	s := untrusted.Text(location, untrusted.MaxName)
	if path != "" {
		s += " (path " + untrusted.Text(path, untrusted.MaxName) + ")"
	}
	return s
}

// resolveSync resolves an entry to the version it installs (Plan 6a §9.3 step 5), from a
// snapshot of the cache or a fetch staged in run: a folder is read every run; --update fetches
// the ref; an unpinned entry takes the cache head for its ref, or fetches the ref when there is
// none (or the head predates commits in heads); a pinned entry takes the cached version, or
// fetches its commit. A new pin is recorded in se.pin.
func (e *Engine) resolveSync(ctx context.Context, sc Scope, se *syncEntry, run string) error {
	name, ent := se.ent.Name, se.ent
	stage := func() error {
		st, err := e.prepareFetch(ctx, name, se.spec, run)
		if err != nil {
			return err
		}
		se.v, se.staged = st.Version, &st
		se.v.Module.Warnings = st.LoadWarnings // what a cache hit would report, not what detection did
		return nil
	}
	switch {
	case se.spec.Kind == source.KindFolder:
		return stage()
	case se.update:
		if err := stage(); err != nil {
			return err
		}
		if se.v.Commit != ent.Commit || se.v.Meta.Digest != ent.Digest {
			se.pin = &Pin{Module: name, Ref: shown(ent.Ref), Commit: se.v.Commit, Digest: se.v.Meta.Digest, PreviousDigest: ent.Digest, Reason: PinUpdate}
		}
		return nil
	case ent.Digest == "":
		head, ok, err := e.Store.Head(name, ent.Ref)
		if err != nil {
			return err
		}
		if ok && se.spec.Kind == source.KindGit && head.Commit == "" {
			ok = false // a head from before commits were recorded: fetch the ref
		}
		if ok {
			v, found, err := e.Store.Pinned(name, head.Meta.Digest, run)
			if err != nil {
				return err
			}
			ok = found
			v.Commit = head.Commit
			se.v = v
		}
		if !ok {
			if err := stage(); err != nil {
				return err
			}
		}
		se.pin = &Pin{Module: name, Ref: shown(ent.Ref), Commit: se.v.Commit, Digest: se.v.Meta.Digest, Reason: PinUnpinned}
		return nil
	}
	v, found, err := e.Store.Pinned(name, ent.Digest, run)
	if err != nil {
		return err
	}
	if found {
		// The cache may hold a commit from a branch nobody reviewed, cached for another project:
		// an entry the user has not trusted must name a commit on its ref (Plan 6a §6.4).
		if !se.covered && se.spec.Kind == source.KindGit {
			if err := source.CheckReachable(ctx, se.spec, ent.Commit, run); err != nil {
				return syncPinError(name, filepath.Base(e.files(sc).manifest), ent, fmt.Errorf("%s: %w", name, err), e.SyncCommand(sc, "--update", name))
			}
		}
		v.Commit = ent.Commit
		se.v = v
		return nil
	}
	st, err := e.fetchPin(ctx, name, se.spec, ent.Commit, ent.Digest, run)
	if err != nil {
		return syncPinError(name, filepath.Base(e.files(sc).manifest), ent, err, e.SyncCommand(sc, "--update", name))
	}
	se.v, se.staged, se.headIfAbsent = st.Version, &st, true
	se.v.Module.Warnings = st.LoadWarnings // what a cache hit would report, not what detection did
	return nil
}

// syncPinError coaches a pinned fetch that failed in a way re-pinning can resolve; update is the
// sync command that re-pins the entry.
func syncPinError(name, base string, ent manifest.Entry, err error, update string) error {
	if mismatch, ok := errors.AsType[*digestMismatchError](err); ok {
		return errs.WithHints(fmt.Errorf("%s: the source now serves different content than %s pins (pinned %s, got %s)",
			name, base, untrusted.Text(ent.Digest, untrusted.MaxName), mismatch.got),
			"if you expect the change, re-pin it: "+update,
			"otherwise the source changed under the same ref or commit: check it before re-pinning")
	}
	where := "the default branch"
	if ent.Ref != "" {
		where = untrusted.Text(ent.Ref, untrusted.MaxName)
	}
	if _, ok := errors.AsType[*source.NotOnRefError](err); ok {
		return errs.WithHints(err, "re-pin to what "+where+" serves: "+update)
	}
	if _, ok := errors.AsType[*commitFetchError](err); ok {
		return errs.WithHints(err, "if the server refuses fetching by commit, re-pin to what "+where+" serves now: "+update)
	}
	// Render's hint, to install what the source serves now, would trust it unseen.
	if served, ok := errors.AsType[*servedCommitError](err); ok {
		return errs.WithHints(served, "check the source before you re-pin it: "+update)
	}
	return err
}

// prepareFetch fetches spec into run and prepares it there; the content must declare name.
func (e *Engine) prepareFetch(ctx context.Context, name string, spec source.Spec, run string) (store.Staged, error) {
	f, err := source.Fetch(ctx, spec, run)
	if err != nil {
		return store.Staged{}, err
	}
	return e.Store.Prepare(store.PutRequest{Staged: f.Dir, FallbackName: f.Name, Name: name, Source: spec.Location, Type: string(spec.Kind),
		Path: spec.Path, Ref: spec.Ref, Commit: f.Commit, Executables: f.Executables, Now: e.Now()})
}

// publish commits an entry's staged version to the cache, once. A pinned fetch never moves a
// head; --update, an unpinned entry's fetch and a folder move the head for their ref, except in
// a project dry run.
func (e *Engine) publish(se *syncEntry) error {
	if se.staged == nil {
		return nil
	}
	se.staged.ReplaceGoneOrigin = se.goneFrom != ""
	if _, err := e.Store.Commit(*se.staged, se.headIfAbsent); err != nil {
		return err
	}
	se.staged = nil
	return nil
}

// removal is what a sync removes of the module name that lock.json records as installed: the
// targets it is installed on that keep (the targets its entry names; nil for a module the
// manifest no longer lists, which goes whole), with the paths lola wrote there. It is nil when
// nothing is removed. lock.json is hostile input: every target must be known and every path
// must resolve inside its target's root, or the sync is refused before anything is asked.
func (e *Engine) removal(sc Scope, name string, installed *ledger.Entry, keep []string) (*Removal, error) {
	if installed == nil {
		return nil, nil
	}
	r := &Removal{Module: shown(name), Targets: []string{}, WholeModule: keep == nil, Paths: []RemovedPath{}}
	var gone []string
	for _, t := range allTargets(installed, nil, &manifest.Manifest{}) {
		if !slices.Contains(keep, t) {
			gone = append(gone, t)
			r.Targets = append(r.Targets, shown(t))
		}
	}
	if len(gone) == 0 && !r.WholeModule {
		return nil, nil
	}
	roots := map[string]string{}
	for _, t := range gone {
		tgt, err := e.target(t)
		if err != nil {
			return nil, err
		}
		roots[t] = tgt.Root(sc.Kind, sc.ProjectDir, e.Paths)
	}
	for _, a := range installed.Artifacts {
		if !slices.Contains(gone, a.Target) {
			continue
		}
		if _, err := fsx.Resolve(roots[a.Target], a.Path); err != nil {
			return nil, err
		}
		path := untrusted.Text(a.Path, untrusted.MaxName)
		if a.Key != "" {
			path += " " + untrusted.Text(a.Key, untrusted.MaxName)
		}
		r.Paths = append(r.Paths, RemovedPath{Target: shown(a.Target), Path: path})
	}
	return r, nil
}
