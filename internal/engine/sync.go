package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
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
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/trust"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// SyncRequest is one lola sync (Plan 6a §9.2): make Scope match its manifest. Update names the
// entries to re-pin at their ref; UpdateAll re-pins every entry. Options are as for render:
// DryRun, Force and the hook options; Targets, Strict and Rules are not used.
type SyncRequest struct {
	Scope     Scope
	Update    []string
	UpdateAll bool
	Options   Options
	// Trust asks the user to trust the changes to project.yml since they last trusted it, and
	// the pins the sync would write (Plan 6a §7.5). nil means it cannot ask: a sync that needs
	// the question is refused before any network contact. The CLI passes an always-yes Trust for
	// --trust; -y never sets it.
	Trust func(TrustReport) (bool, error)
	// ConfirmRemovals asks the user to confirm the removals the sync would make. nil means it
	// cannot ask: a sync with removals is refused. The CLI passes an always-yes ConfirmRemovals
	// for -y.
	ConfirmRemovals func([]Removal) (bool, error)
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
// Targets is the top-level targets' change, nil when the record covers them.
type TrustReport struct {
	Status   string             `json:"status"`
	Recorded bool               `json:"recorded"`
	Targets  *trust.FieldChange `json:"targets"`
	Changes  []TrustChange      `json:"changes"`
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
	// FolderContent marks a change that is only a folder module's content differing from what
	// was installed, with project.yml as trusted; the human diff words its header by it.
	FolderContent bool               `json:"-"`
	Files         []FileChange       `json:"files"`
	FilesNote     string             `json:"files_note,omitempty"`
	MCPServers    []ContentMCPServer `json:"mcp_servers"`
	Hooks         []ContentHook      `json:"hooks"`
}

// ContentMCPServer is a module MCP server as the trust diff shows it, which is the only consent
// it gets (L1): Runs is the command with each argument quoted unless plain, so the arguments'
// boundaries stay visible, or the URL; Env ("NAME=value") and Headers ("Name: value") are
// sorted, and Cwd is "" when unset, since each can change what runs or where data goes.
type ContentMCPServer struct {
	Name      string   `json:"name"`
	Transport string   `json:"transport"`
	Runs      string   `json:"runs"`
	Env       []string `json:"env"`
	Headers   []string `json:"headers"`
	Cwd       string   `json:"cwd"`
}

// FileChange is a file a module version adds, changes or removes (trust.Change kinds).
type FileChange struct {
	Path   string `json:"path"`
	Change string `json:"change"`
}

// Kinds of hooks a module's content declares.
const (
	HookKindInstall   = "install"    // an install hook: Event is pre-install or post-install, Detail its script
	HookKindRuntime   = "runtime"    // a runtime hook handler an assistant runs on Event
	HookKindMCPServer = "mcp_server" // an agent's inline MCP server
)

// ContentHook is one hook a module's content declares, as the trust diff shows it: Target is the
// assistant a runtime hook or agent MCP server is rendered for ("" for an install hook), Detail
// what it runs or where it connects.
type ContentHook struct {
	Kind   string `json:"kind"`
	Target string `json:"target,omitempty"`
	Event  string `json:"event"`
	Detail string `json:"detail"`
}

// TrustRefusalError is a sync refused because the user must trust changes to the manifest and
// there is no way to ask (SyncRequest.Trust is nil). Report holds the changes without their
// content: nothing was fetched to show it.
type TrustRefusalError struct {
	Report TrustReport
	msg    string
}

func (e *TrustRefusalError) Error() string { return e.msg }

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
// the scope was already in sync (nothing to write, pin or remove).
type SyncResult struct {
	*Result
	Scope    string      `json:"scope"`
	Root     string      `json:"root"`
	Trust    TrustReport `json:"trust"`
	Pins     []Pin       `json:"pins"`
	Removals []Removal   `json:"removals"`
	InSync   bool        `json:"in_sync"`
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
// folder at project scope whose content is not the version lock.json records.
type syncEntry struct {
	ent           manifest.Entry
	covered       bool
	folderChanged bool
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
	f := e.files(sc)
	if home := (Scope{Kind: target.Project, ProjectDir: e.Paths.Home}); sc.Kind == target.Project && sc.Key() == home.Key() {
		return nil, errs.WithHints(fmt.Errorf("project scope at %s is your home directory, where the user scope installs",
			untrusted.Text(e.Paths.Home, untrusted.MaxName)), "sync the user scope: "+errs.ShellJoin([]string{"lola", "sync", "--user"}))
	}
	man, led, unlock, err := e.load(sc, opts.DryRun)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := e.refuseMissingManifest(sc); err != nil {
		return nil, err
	}
	res := &SyncResult{Result: newResult(opts.DryRun), Scope: sc.Kind.String(), Root: root(sc), Pins: []Pin{}, Removals: []Removal{},
		Trust: TrustReport{Status: TrustStatusNotApplicable, Changes: []TrustChange{}}}
	res.Notices = append(res.Notices, e.sweepRuns()...)
	run, err := e.runDir()
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(run)) }()
	if err := e.refuseTrackedLedger(ctx, sc, res.Result); err != nil {
		return nil, err
	}

	entries, err := e.validateSync(sc, man, req)
	if err != nil {
		return nil, err
	}
	var rec *trust.Record
	manifestChanged := false
	if sc.Kind == target.Project {
		if rec, err = e.Trust.Load(sc.ProjectDir); err != nil {
			return nil, err
		}
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
			se.folderChanged = installed == nil || installed.Digest != se.v.Meta.Digest
		}
		// A run that cannot ask is refused here, before anything is fetched (Plan 6a §9.3 step 4).
		rep := trust.Check(rec, trust.ViewOf(man))
		manifestChanged = rep.Status != trust.StatusTrusted
		folderChanged := slices.ContainsFunc(entries, func(se *syncEntry) bool { return se.folderChanged })
		if !opts.DryRun && req.Trust == nil && (manifestChanged || folderChanged || slices.ContainsFunc(entries, needsPin)) {
			report := trustReport(rep)
			folderChanges(&report, entries, led)
			notFixed(&report, entries)
			return nil, trustRefusal(f.manifest, report, manifestChanged, entries)
		}
	} else {
		for _, se := range entries {
			se.covered = true
		}
	}

	for _, se := range entries {
		if se.v.ContentDir == "" { // a covered folder at project scope was read above
			if err := e.resolveSync(ctx, sc, se, run); err != nil {
				return nil, err
			}
		}
		// What the user trusts at its pin goes into the cache now; everything else waits. A
		// project dry run moves no head (Plan 6a §3.1), not even for an --update fetch.
		if se.covered && se.pin == nil && !se.folderChanged {
			if opts.DryRun && sc.Kind == target.Project {
				se.headIfAbsent = true
			}
			if err := e.publish(se); err != nil {
				return nil, err
			}
		}
	}
	var pinned []manifest.Entry
	for _, se := range entries {
		if se.pin != nil {
			res.Pins = append(res.Pins, *se.pin)
			pinned = append(pinned, se.pinned())
		}
	}

	// Plan every module now, from the bytes that were hashed, before the trust question: the apply
	// uses these intents (Plan 6a §9.3 step 10).
	sel, err := e.selections()
	if err != nil {
		return nil, err
	}
	probes := e.newProbeRun(ctx)
	var changes []plan.ModuleChange
	var copies []moduleCopy
	want := map[string]bool{}
	for _, se := range entries {
		name := se.ent.Name
		want[name] = true
		ent := se.pinned()
		x, err := exclusions(sel, sc, name)
		if err != nil {
			return nil, err
		}
		intents, finds, notes, mr, err := e.planModule(sc, se.v, se.targets, &ent, x, opts, probes)
		if err != nil {
			return nil, err
		}
		res.Modules, res.Findings, res.Notices = append(res.Modules, mr), append(res.Findings, finds...), append(res.Notices, notes...)
		installed := led.Modules[name]
		ts := se.targets
		for _, t := range allTargets(installed, nil, &manifest.Manifest{}) {
			if !slices.Contains(ts, t) {
				ts = append(slices.Clone(ts), t)
			}
		}
		changes = append(changes, plan.ModuleChange{Name: name, Targets: ts, Intents: intents,
			Entry: &ledger.Entry{Source: ent.Source, Ref: ent.Ref, Path: ent.Path, Commit: se.v.Commit, Digest: se.v.Meta.Digest}})
		replace, err := needsCopy(f.modules, name, installed, se.v.Meta.Digest)
		if err != nil {
			return nil, err
		}
		copies = append(copies, moduleCopy{name: name, src: se.v.ContentDir, replace: replace,
			hooks: se.v.Module.DeclaredHooks, targets: mr.Targets, runHooks: replace})
	}

	// The pins the sync writes are trust changes too: the question is about the manifest as it
	// will be saved (Plan 6a §7.3).
	accepted := false
	if sc.Kind == target.Project {
		saved := man.Applied(manifest.Change{Set: pinned})
		res.Trust = trustReport(trust.Check(rec, trust.ViewOf(saved)))
		folderChanges(&res.Trust, entries, led)
		notFixed(&res.Trust, entries)
		if err := e.fillContent(sc, &res.Trust, entries, led, probes); err != nil {
			return nil, err
		}
		if res.Trust.Status != trust.StatusTrusted {
			switch {
			case opts.DryRun:
			case req.Trust == nil:
				return nil, trustRefusal(f.manifest, res.Trust, manifestChanged, entries)
			default:
				yes, err := req.Trust(res.Trust)
				if err != nil {
					return nil, err
				}
				if !yes {
					return nil, ErrDeclined
				}
				accepted = true
			}
		}
	}
	// Publish what the user trusts now (Plan 6a §9.3 step 8). A dry run publishes nothing more,
	// except that --update -n at user scope, whose user.yml is the user's own, fetches into the
	// cache and moves heads, as -n always may there.
	for _, se := range entries {
		if opts.DryRun && (sc.Kind != target.User || !se.update) {
			continue
		}
		if err := e.publish(se); err != nil {
			return nil, err
		}
	}

	// Removals are confirmed before any hook consent is asked or recorded (Plan 6a §9.3 step 9).
	for _, se := range entries {
		r, err := e.removal(sc, se.ent.Name, led.Modules[se.ent.Name], se.targets)
		if err != nil {
			return nil, err
		}
		if r != nil {
			res.Removals = append(res.Removals, *r)
		}
	}
	for _, name := range sortedNames(led) {
		if !want[name] {
			r, err := e.removal(sc, name, led.Modules[name], nil)
			if err != nil {
				return nil, err
			}
			res.Removals = append(res.Removals, *r)
			changes = append(changes, plan.ModuleChange{Name: name, Targets: allTargets(led.Modules[name], nil, &manifest.Manifest{})})
		}
	}
	if len(res.Removals) > 0 && !opts.DryRun {
		if req.ConfirmRemovals == nil {
			return nil, errs.WithHints(errors.New("lola sync would remove modules, and there is no terminal to ask on"),
				"see them: "+errs.ShellJoin([]string{"lola", "sync", "-n"}), "then: "+errs.ShellJoin([]string{"lola", "sync", "-y"}))
		}
		yes, err := req.ConfirmRemovals(slices.Clone(res.Removals))
		if err != nil {
			return nil, err
		}
		if !yes {
			return nil, ErrDeclined
		}
	}
	res.Findings = append(res.Findings, probes.findings...)

	pr, err := e.prepare(sc, led, changes, copies, nil, opts, res.Result)
	if err != nil {
		return nil, err
	}
	res.InSync = len(res.Steps) == 0 && len(res.Pins) == 0 && len(res.Removals) == 0 &&
		!slices.ContainsFunc(copies, func(c moduleCopy) bool { return c.replace })
	if opts.DryRun {
		return res, nil
	}
	apply := func(mw *plan.ManifestWrite) error {
		return e.commit(ctx, pr, opts, res.Result, mw, func(*plan.Plan) error { return e.makePluginData(sc, changes) })
	}
	// The trust record is written once, after the apply: accepting trust records the view lola
	// saves, every field of it (a record equal to the loaded manifest refreshes to exactly the
	// saved view).
	var trusted *trust.Record
	if accepted {
		trusted = &trust.Record{View: trust.ViewOf(man)}
	}
	if len(pinned) > 0 {
		err = e.saveManifest(sc, man, manifest.Change{Set: pinned}, trusted, "the project's files, lock.json and project.yml were updated",
			trustSyncAgain, apply)
	} else if err = apply(nil); err == nil && accepted {
		if err = e.Trust.Save(sc.ProjectDir, trust.ViewOf(man)); err != nil {
			msg := "lola could not record that you trust project.yml"
			if !res.InSync {
				msg = "the project's files and lock.json were updated, but " + msg
			}
			err = errs.WithHints(fmt.Errorf("%s: %w", msg, err), trustSyncAgain)
		}
	}
	if err != nil {
		return nil, err
	}
	res.Trust.Recorded = accepted
	for i := range res.Pins {
		res.Pins[i].Written = true
	}
	return res, nil
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
			served := shown(se.ent.Source) // an archive has no ref: the file or URL serves it
			if se.spec.Kind == source.KindGit {
				served = "the default branch"
				if se.ent.Ref != "" {
					served = shown(se.ent.Ref)
				}
			}
			c.Note = "content not fixed; installs what " + served + " serves now"
		}
	}
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

// trustRefusal refuses a sync that needs the user's trust and cannot ask (Plan 6a §11.3): for
// the manifest, which changed since the user trusted it, else for the pins the sync would write,
// else for the folders whose content changed. The error carries the report for the caller to
// show.
func trustRefusal(manifestPath string, rep TrustReport, manifestChanged bool, entries []*syncEntry) error {
	name := filepath.Join(filepath.Base(filepath.Dir(manifestPath)), filepath.Base(manifestPath))
	msg := name + " changed since you last trusted it"
	switch {
	case manifestChanged:
	case slices.ContainsFunc(entries, needsPin):
		msg = "lola sync would write pins to " + name + ", which needs your trust"
	default:
		var names []string
		for _, se := range entries {
			if se.folderChanged {
				names = append(names, se.ent.Name)
			}
		}
		msg = "the content of " + strings.Join(names, ", ") + " changed since lola last installed it, which needs your trust"
	}
	msg += ", and there is no terminal to ask on"
	return errs.WithHints(&TrustRefusalError{Report: rep, msg: msg},
		"see the changes: "+errs.ShellJoin([]string{"lola", "sync", "-n"}),
		"then accept them: "+errs.ShellJoin([]string{"lola", "sync", "--trust"}))
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
		local := se.spec.Kind == source.KindFolder || se.spec.Kind == source.KindArchive && filepath.IsAbs(se.spec.Location)
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
			return errs.WithHints(fmt.Errorf("%s: local source %s resolves outside the project (%s)", se.ent.Name,
				untrusted.Text(se.ent.Source, untrusted.MaxName), untrusted.Text(se.spec.Location, untrusted.MaxName)),
				"install it yourself if you trust it: "+installCommand(se.spec.Location, "", se.ent.Path))
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
// the version lock.json records (none when lock.json records none), and makes a trusted report
// changed: the user trusts what an in-repo module holds by trusting the version installed.
func folderChanges(rep *TrustReport, entries []*syncEntry, led *ledger.Ledger) {
	for _, se := range entries {
		if !se.folderChanged {
			continue
		}
		var from any
		if installed := led.Modules[se.ent.Name]; installed != nil {
			from = shown(installed.Digest)
		}
		rep.Changes = append(rep.Changes, TrustChange{Change: trust.Change{Module: se.ent.Name, Kind: trust.ChangeChanged,
			Fields: []trust.FieldChange{{Field: "digest", From: from, To: se.v.Meta.Digest}}},
			FolderContent: true, Files: []FileChange{}, MCPServers: []ContentMCPServer{}, Hooks: []ContentHook{}})
		if rep.Status == trust.StatusTrusted {
			rep.Status = trust.StatusChanged
		}
	}
	slices.SortStableFunc(rep.Changes, func(a, b TrustChange) int { return strings.Compare(a.Module, b.Module) })
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

// validateSync checks every desired entry and the --update names before anything is fetched or
// asked (Plan 6a §9.3 step 2), stopping at the first failure: the source parses and passes
// source.Validate with the entry's ref and path; the pin fits the source's kind; the cache holds
// no other source under the name (§6.5); the effective targets are known.
func (e *Engine) validateSync(sc Scope, man *manifest.Manifest, req SyncRequest) ([]*syncEntry, error) {
	base := filepath.Base(e.files(sc).manifest)
	for _, name := range req.Update {
		if man.Find(name) == nil {
			var names []string
			for _, ent := range desired(man) {
				names = append(names, ent.Name)
			}
			return nil, errs.WithHints(fmt.Errorf("%s is not in %s", untrusted.Text(name, untrusted.MaxName), base),
				"modules in "+base+": "+strings.Join(names, ", "))
		}
	}
	cwd := "/"
	if sc.Kind == target.Project {
		cwd = canonicalRoot(sc.ProjectDir)
	}
	var out []*syncEntry
	for _, ent := range desired(man) {
		name := ent.Name
		spec, ok, err := source.Parse(ent.Source, cwd)
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
		if err := checkPinShape(name, base, spec.Kind, ent); err != nil {
			return nil, err
		}
		if err := e.refuseOtherSource(sc, name, spec); err != nil {
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
		out = append(out, &syncEntry{ent: ent, spec: spec, targets: ts, update: req.UpdateAll || slices.Contains(req.Update, name)})
	}
	return out, nil
}

// checkPinShape refuses a ref or a pin the entry's kind of source cannot have (Plan 6a §4.1): a
// ref on a folder or archive, a pin on a folder, a commit on an archive, a git digest without
// its commit.
func checkPinShape(name, base string, kind source.Kind, ent manifest.Entry) error {
	update := "re-pin it: " + errs.ShellJoin([]string{"lola", "sync", "--update", name})
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
// (Plan 6a §6.5): the cache keeps one source per name, for every project on the machine.
func (e *Engine) refuseOtherSource(sc Scope, name string, spec source.Spec) error {
	o, found, err := e.Store.Origin(name)
	if err != nil || !found {
		return err
	}
	if o.Type == string(spec.Kind) && source.SameLocation(spec.Kind, o.Source, spec.Location) && o.Path == spec.Path {
		return nil
	}
	return errs.WithHints(fmt.Errorf("%s in your cache comes from %s, and %s asks for %s; the cache is shared by every project on this machine",
		name, describeSource(o.Source, o.Path), filepath.Base(e.files(sc).manifest), describeSource(spec.Location, spec.Path)),
		"remove it from your cache: "+errs.ShellJoin([]string{"lola", "cache", "rm", name}), "then run lola sync again")
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
				return syncPinError(name, filepath.Base(e.files(sc).manifest), ent, fmt.Errorf("%s: %w", name, err))
			}
		}
		v.Commit = ent.Commit
		se.v = v
		return nil
	}
	st, err := e.fetchPin(ctx, name, se.spec, ent.Commit, ent.Digest, run)
	if err != nil {
		return syncPinError(name, filepath.Base(e.files(sc).manifest), ent, err)
	}
	se.v, se.staged, se.headIfAbsent = st.Version, &st, true
	se.v.Module.Warnings = st.LoadWarnings // what a cache hit would report, not what detection did
	return nil
}

// syncPinError coaches a pinned fetch that failed in a way re-pinning can resolve.
func syncPinError(name, base string, ent manifest.Entry, err error) error {
	update := errs.ShellJoin([]string{"lola", "sync", "--update", name})
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
	if _, err := e.Store.Commit(*se.staged, se.headIfAbsent); err != nil {
		return err
	}
	se.staged = nil
	return nil
}

// fillContent fills the content of each added or changed module of rep from the version the sync
// resolved for it (Plan 6a §7.3): files against the version lock.json records, from the cache
// (never fetched: a version the cache lost lists every file as added), MCP servers, and every
// hook the content declares (declaredRuntimeHooks).
func (e *Engine) fillContent(sc Scope, rep *TrustReport, entries []*syncEntry, led *ledger.Ledger, probes *probeRun) error {
	for i := range rep.Changes {
		c := &rep.Changes[i]
		k := slices.IndexFunc(entries, func(se *syncEntry) bool { return se.ent.Name == c.Module })
		if c.Kind == trust.ChangeRemoved || k < 0 {
			continue
		}
		se := entries[k]
		var err error
		if c.Files, c.FilesNote, err = e.fileChanges(se, led.Modules[se.ent.Name]); err != nil {
			return err
		}
		for _, srv := range se.v.Module.MCPServers {
			c.MCPServers = append(c.MCPServers, contentMCPServer(srv))
		}
		for _, h := range se.v.Module.DeclaredHooks {
			c.Hooks = append(c.Hooks, ContentHook{Kind: HookKindInstall, Event: shown(h.Event), Detail: shown(h.Script)})
		}
		hooks, err := e.declaredRuntimeHooks(sc, se, probes)
		if err != nil {
			return fmt.Errorf("%s: runtime hooks: %w", se.ent.Name, err)
		}
		for _, h := range hooks {
			c.Hooks = append(c.Hooks, contentHook(h))
		}
	}
	return nil
}

// contentMCPServer is srv as the trust diff shows it, every string through untrusted.Text.
func contentMCPServer(srv module.MCPServer) ContentMCPServer {
	runs := srv.URL
	if srv.Transport == module.TransportStdio {
		words := []string{word(srv.Command)}
		for _, a := range srv.Args {
			words = append(words, word(a))
		}
		runs = strings.Join(words, " ")
	}
	out := ContentMCPServer{Name: shown(srv.Name), Transport: shown(srv.Transport), Runs: shown(runs), Env: []string{}, Headers: []string{}, Cwd: shown(srv.Cwd)}
	for _, k := range slices.Sorted(maps.Keys(srv.Env)) {
		out.Env = append(out.Env, shown(k+"="+srv.Env[k]))
	}
	for _, k := range slices.Sorted(maps.Keys(srv.Headers)) {
		out.Headers = append(out.Headers, shown(k+": "+srv.Headers[k]))
	}
	return out
}

// declaredRuntimeHooks is every runtime hook and agent MCP server se's version declares, as each
// assistant lola knows would render it, whatever the entry's targets and the user's selections
// (Plan 6a §7.3, L1). Describing runs no assistant: one probes has not read is planned as the
// newest version lola knows.
func (e *Engine) declaredRuntimeHooks(sc Scope, se *syncEntry, probes *probeRun) ([]RuntimeHook, error) {
	quiet := &probeRun{e: e, ctx: probes.ctx, versions: maps.Clone(probes.versions), exposed: probes.exposed}
	ids := make([]string, len(e.Targets))
	for i, t := range e.Targets {
		ids[i] = t.ID()
		if _, ok := quiet.versions[ids[i]]; !ok {
			quiet.versions[ids[i]] = ""
		}
	}
	ent := se.pinned()
	intents, _, _, _, err := e.planModule(sc, se.v, ids, &ent, config.Exclusions{}, Options{}, quiet)
	if err != nil {
		return nil, err
	}
	var gated []plan.Intent
	for _, in := range intents {
		if in.Consent != nil {
			gated = append(gated, in)
		}
	}
	hooks, _, err := describeRuntimeHooks(gated, se.v.Meta.Digest)
	return hooks, err
}

// contentHook is a runtime hook or agent MCP server as the trust diff shows it; its strings are
// already safe to print (describeRuntimeHooks).
func contentHook(h RuntimeHook) ContentHook {
	var parts []string
	if h.Component != "" {
		parts = append(parts, h.Component)
	}
	if h.MCPServer != "" {
		parts = append(parts, h.MCPServer+" ("+h.Type+")")
	} else {
		if h.Matcher != "" {
			parts = append(parts, "matcher "+h.Matcher)
		}
		parts = append(parts, h.Type)
	}
	for _, f := range []struct{ label, value string }{{"runs", h.Command}, {"url", h.URL}, {"calls tool", h.Tool}, {"prompt", h.Prompt}} {
		if f.value != "" {
			parts = append(parts, f.label+" "+f.value)
		}
	}
	kind := HookKindRuntime
	if h.MCPServer != "" {
		kind = HookKindMCPServer
	}
	return ContentHook{Kind: kind, Target: h.Target, Event: h.Event, Detail: strings.Join(parts, ": ")}
}

// fileChanges lists the files se's version adds, changes (content or execute bit) and removes
// against installed's version, read from the cache. With no version installed every file is
// added; when the cache no longer holds the installed version, or cannot read it, every file is
// listed as added with a note saying why, and the old version is never fetched to compare: the
// diff is the remedy for a damaged cache, so the cache never blocks it.
func (e *Engine) fileChanges(se *syncEntry, installed *ledger.Entry) ([]FileChange, string, error) {
	now, err := contentFiles(se.v.ContentDir, se.v.Meta.Executables)
	if err != nil {
		return nil, "", err
	}
	var before map[string]string
	note := ""
	if installed != nil {
		if installed.Digest == se.v.Meta.Digest {
			return []FileChange{}, "", nil
		}
		old, err := e.Store.Load(se.ent.Name, versionID(installed.Digest))
		if err == nil {
			before, err = contentFiles(old.ContentDir, old.Meta.Executables)
		}
		switch {
		case errors.Is(err, os.ErrNotExist):
			before, note = nil, "the version installed is not in your cache, so every file is listed as added"
		case err != nil:
			before, note = nil, "lola could not read the version installed from your cache ("+shown(untrusted.CleanErr(err).Error())+
				"), so every file is listed as added"
		}
	}
	out := []FileChange{}
	for _, p := range slices.Sorted(maps.Keys(now)) {
		switch was, ok := before[p]; {
		case !ok:
			out = append(out, FileChange{Path: shown(p), Change: trust.ChangeAdded})
		case was != now[p]:
			out = append(out, FileChange{Path: shown(p), Change: trust.ChangeChanged})
		}
	}
	for _, p := range slices.Sorted(maps.Keys(before)) {
		if _, ok := now[p]; !ok {
			out = append(out, FileChange{Path: shown(p), Change: trust.ChangeRemoved})
		}
	}
	slices.SortStableFunc(out, func(a, b FileChange) int { return strings.Compare(a.Path, b.Path) })
	return out, note, nil
}

// contentFiles maps each regular file under dir, by slash path, to the sha256 of its bytes and
// whether execs (the version's executables) lists it.
func contentFiles(dir string, execs []string) (map[string]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		files = append(files, filepath.ToSlash(rel))
		return err
	})
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, rel := range files {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(b)
		out[rel] = hex.EncodeToString(sum[:]) + fmt.Sprint(slices.Contains(execs, rel))
	}
	return out, nil
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
