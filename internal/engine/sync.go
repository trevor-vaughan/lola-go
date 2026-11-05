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
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/profile"
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
	// for --trust; -y never sets it. It is asked only about the changes of the kinds
	// Options.Trust does not cover (Plan 9 §3.4); Show, when set, is shown the changes it covers,
	// as a record, before they reach the receipt.
	Trust func(TrustReport) (TrustAnswer, error)
	Show  func(TrustReport)
	// ConfirmRemovals asks the user to confirm the removals the sync would make. nil means it
	// cannot ask: a sync with removals is refused. The CLI passes an always-yes ConfirmRemovals
	// for -y.
	ConfirmRemovals func([]Removal) (bool, error)
	// Profiles, when set, changes the scope's active profile list and applies the change (Plan 6b
	// §6.3, §19 R3): the run is limited to the modules whose state differs from what the new list
	// wants, and the new list is written to lock.json in the same commit as the apply.
	Profiles *ProfileChange
	// Display is the run's output flags (--json, --plain, --no-pager as given), which the install
	// commands an --update's marketplace notices name keep (Plan 6c §11.3).
	Display []string
}

// TrustAnswer is the user's answer to the trust question (Plan 6a §9.2): All accepts every change
// of the run; otherwise Modules names the accepted entries, as the report names them, and Kinds
// accepts every change of the kinds --trust covers (Plan 9 §3.4). An answer accepting nothing is
// a Cancel.
type TrustAnswer struct {
	All     bool
	Modules []string
	Kinds   TrustKinds
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
	Replacements    []Replacement      `json:"replacements"`
	ManifestChanged bool               `json:"-"`
}

// Replacement is a project profile that an activation puts in place of an active profile of the
// same name (Plan 6b §4.4, D8; ruling R-6b3a-1): never silently, but as a trust change of its
// own, accepted with the activation's Trust all. Modules are the modules whose winning entry it
// changes. Its strings have been through untrusted.Text.
type Replacement struct {
	Profile  string   `json:"profile"`
	Replaces string   `json:"replaces"`
	Modules  []string `json:"modules"`
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

// Pin is a pin a sync writes to an entry of the manifest, or of a project profile (Profile);
// Ref, from the file, has been through untrusted.Text. Written is false in a dry run.
type Pin struct {
	Module         string `json:"module"`
	Profile        string `json:"profile,omitempty"` // the project profile it is written into (Plan 6b §6.4); "" for the manifest
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
	// *Activation is set by an activation (SyncRequest.Profiles) only, adding its profiles and
	// changes to the JSON document.
	*Activation
}

// syncEntry is one desired entry of a sync: validated (spec, targets), then resolved to a
// version. v is read from a snapshot of the cache or a staged fetch in the run directory; staged
// is set while the version is not in the cache, and headIfAbsent says its commit must not move
// a head (a pinned fetch, or any in a project dry run). pin is set when the sync writes a new pin for the entry. covered says
// the trust record covers the entry (always at user scope); folderChanged says it is a covered
// folder at project scope whose content is not the version lock.json records, and
// notInstalled that lock.json records none. from is the profile layer the entry won from
// (Wanted.From), "" for the scope's manifest, and file that profile's path; key is a project
// profile's name (Wanted.key), "" for any other layer; ledgerPin says its pin is the one
// lock.json records for the unpinned entry of the user's own profile.
type syncEntry struct {
	ent        manifest.Entry
	from, file string
	key        string
	via        *trust.ExtendsView // the remote profile the entry comes from through extends: (Wanted.via)
	ledgerPin  bool
	goneFrom   string // the description (describeSource) of the local source the cache records for the name, which no longer exists
	covered    bool
	// sameSource says the record holds the entry with this Source and Path, whatever else of it
	// changed: its location was trusted, so containment need not check it again (ruling R-8-U7).
	sameSource    bool
	folderChanged bool
	notInstalled  bool
	spec          source.Spec
	targets       []string
	update        bool
	v             store.Version
	staged        *store.Staged
	headIfAbsent  bool
	pin           *Pin
	follow        *marketPick // the catalog entry an --update follows (Plan 6c §6.6), nil for none
}

// own reports whether the entry is from a profile of the user's own, trusted as user.yml is
// (Plan 6b §8.2); a project profile's entry is reviewed as project.yml's is (§8.1).
func (se *syncEntry) own() bool { return se.from != "" && se.key == "" }

// part is the trust.Change.Profile of the entry's changes: its project profile's qualified name,
// through untrusted.Text, "" for the scope's manifest (and for the user's own profiles, which
// have no part).
func (se *syncEntry) part() string {
	if se.key == "" {
		return ""
	}
	return shown(string(profile.KindProject) + ":" + se.key)
}

// owns reports whether c, a change of a run's report, is about se's entry: its module, in the
// file se's entry comes from.
func (se *syncEntry) owns(c TrustChange) bool {
	return c.Module == shown(se.ent.Name) && c.Profile == se.part()
}

// entryOf is the entry of entries that c is about, or nil.
func entryOf(entries []*syncEntry, c TrustChange) *syncEntry {
	if k := slices.IndexFunc(entries, func(se *syncEntry) bool { return se.owns(c) }); k >= 0 {
		return entries[k]
	}
	return nil
}

// view is the entry as the trust record holds it: with the remote profile it comes from.
func (se *syncEntry) view() trust.Entry {
	v := trust.EntryOf(se.ent)
	v.Extends = se.via
	return v
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
	defer func() { err = e.CoachUnresolved(err, []Scope{sc}, opts.DryRun, req.Display) }()
	opts.Rerun = func(k TrustKinds) string { return e.commandOf(req.trusting(k), req.Display...) }
	opts.askScripts = true
	if home := (Scope{Kind: target.Project, ProjectDir: e.Paths.Home}); sc.Kind == target.Project && sc.Key() == home.Key() {
		return nil, errs.WithHints(fmt.Errorf("project scope at %s is your home directory, where the user scope installs",
			untrusted.Text(e.Paths.Home, untrusted.MaxName)), "sync the user scope: "+e.SyncCommand(Scope{Kind: target.User}))
	}
	// The profiles lock comes first (Plan 6b §10): the run reads the remote profiles once.
	unlockProfiles, err := e.readProfiles(opts.DryRun)
	if err != nil {
		return nil, err
	}
	defer unlockProfiles()
	res := &SyncResult{Result: newResult(opts.DryRun), Scope: sc.Kind.String(), Root: root(sc), Pins: []Pin{}, Removals: []Removal{},
		Trust: TrustReport{Status: TrustStatusNotApplicable, Changes: []TrustChange{}, Replacements: []Replacement{}}, EquivalentCommands: []string{}}
	res.Trusted = []Consent{}
	// A committed lock.json is refused before the profiles it names are read (Plan 6b §19 S5).
	if err := e.refuseTrackedLedger(ctx, sc, res.Result); err != nil {
		return nil, err
	}
	// --update follows the catalogs of the entries installed through your marketplaces, each
	// fetched once, under the markets lock: after the profiles lock, before the scope's (Plan 6c
	// §5, §6.6). Without --update no catalog is read.
	var catalogs *syncCatalogs
	var catalogNotices []Notice
	if req.Update {
		var unlockMarkets func()
		if catalogs, unlockMarkets, catalogNotices, err = e.fetchCatalogs(ctx, sc, req); err != nil {
			return nil, err
		}
		defer unlockMarkets()
	}
	man, led, unlock, err := e.load(sc, opts.DryRun)
	if err != nil {
		return nil, err
	}
	defer unlock()
	// An activation builds the layers of its old and new active lists from one read of each
	// profile; a list that does not change stops here, with its notice (Plan 6b §6.3 step 2).
	names := req.Modules
	active := led.Profiles // the active list the run renders templates with: an activation's new one
	var act *activation
	var repl *replacing
	var layers []Layer
	var layerNotices, overrides []Notice
	var want []Wanted
	if req.Profiles != nil {
		var notes []Notice
		var dry []string
		if req.Options.DryRun {
			dry = []string{"-n"}
		}
		if act, notes, err = e.startActivation(sc, led, *req.Profiles, req.rerun(dry...)); err != nil {
			return nil, err
		}
		res.Activation, res.Notices = act.report, append(append(res.Notices, notes...), e.replacementNotices(sc, replacements(act.infos, act.list))...)
		if !act.changed {
			res.InSync = true
			return res, nil
		}
		// Hints that repeat the run name the profiles as resolved (Plan 6b §12.3).
		req.Profiles = &ProfileChange{Action: req.Profiles.Action, Names: act.report.Named}
		pl, err := e.loadProfiles(sc, act.infos, led.Profiles, act.list)
		if err != nil {
			return nil, err
		}
		top := e.manifestLayer(sc, man)
		old, _ := desired(pl.assemble(top, led.Profiles))
		layers, layerNotices = pl.assemble(top, act.list), pl.notices
		active = act.list
		// Precedence notices only for the modules the named profiles list.
		want, overrides = desiredNoting(layers, func(m string) bool {
			return slices.ContainsFunc(act.report.Named, func(q string) bool {
				return slices.ContainsFunc(pl.named[q].Entries, func(ent manifest.Entry) bool { return ent.Name == m })
			})
		})
		// A project profile the user names in place of their active namesake is a trust change of
		// its own, never silent (Plan 6b §4.4, D8; ruling R-6b3a-1).
		repl = replacingOf(replaced(led.Profiles, act.list), old, want)
		all := layerChanges(want, led, top.Name)
		act.report.Changes = moved(all, old, want, led, act.report.Named)
		names = []string{}
		for _, c := range act.report.Changes {
			names = append(names, c.name)
		}
		// A module that stays where it is renders again when its templates read the list.
		rerender, err := e.rerenders(sc, led, act.list, e.Store.Load)
		if err != nil {
			return nil, err
		}
		for _, n := range rerender {
			if !slices.Contains(names, n) && find(want, n) != nil {
				names = append(names, n)
			}
		}
		switch n := len(all) - len(act.report.Changes); {
		case n == 1:
			res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: "1 other change is pending → " + e.SyncCommand(sc)})
		case n > 1:
			res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: fmt.Sprintf("%d other changes are pending → %s", n, e.SyncCommand(sc))})
		}
	} else {
		if layers, layerNotices, err = e.syncLayers(sc, man, led); err != nil {
			return nil, err
		}
		want, overrides = desired(layers)
		// Not for an activation, which always has a profile in play: the one it turns on or off.
		if err := e.refuseMissingManifest(sc, want, led); err != nil {
			return nil, err
		}
	}
	// The trust diff and the first plan pass render under one budget (Plan 7 §6.2).
	templates, err := e.newTemplateRun(sc, active)
	if err != nil {
		return nil, err
	}
	limit, err := e.limitTo(sc, layers, want, led, names)
	if err != nil {
		return nil, err
	}
	limit.only = act != nil
	res.Notices = append(append(append(res.Notices, layerNotices...), overrides...), e.sweepRuns()...)
	run, done, err := e.runDir()
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, done()) }()

	entries, kept, err := e.validateSync(sc, man, want, led, req, limit, catalogs)
	if err != nil {
		return nil, err
	}
	res.Notices = append(append(res.Notices, catalogNotices...), kept...)
	// An activation counted the changes it leaves pending above.
	if n, ok := outOfSync(want, led, limit, e.SyncCommand(sc)); ok && act == nil {
		res.Notices = append(res.Notices, n)
	}
	res.Notices = append(res.Notices, goneNotices(entries)...)
	var rec *trust.Record
	var early trust.Report
	manifestChanged, needTrust := false, false
	var need TrustKinds // the kinds of trust the changes known before any fetch need
	if sc.Kind == target.Project {
		if rec, err = e.Trust.Load(sc.ProjectDir); err != nil {
			return nil, err
		}
		// Coverage is the entry's own (§7.2): it decides containment and whether content the
		// entry pins may be cached before the question. Top-level targets the record does not
		// cover are a change of the report, which an entry that installs to them waits on.
		// The entry of a profile of the user's own is trusted as user.yml is (Plan 6b §8.2); a
		// project profile's is covered by that profile's part of the record (§8.1).
		for _, se := range entries {
			part := rec.View.Part(se.key)
			se.covered = se.own() || part.CoversEntry(se.view())
			if r := part.Find(se.ent.Name); r != nil {
				se.sameSource = r.Source == se.ent.Source && r.Path == se.ent.Path
			}
		}
		if err := e.containLocalSources(sc, entries, req.Display); err != nil {
			return nil, err
		}
		// The record covers what a folder entry says, not what the folder holds: a covered folder
		// is read now (no network), and its content needs trust unless it is the version installed.
		// A folder of the user's own profile is the user's own (Plan 6b §4.3).
		for _, se := range entries {
			if !se.covered || se.spec.Kind != source.KindFolder || se.own() {
				continue
			}
			if err := e.resolveSync(ctx, sc, se, run); err != nil {
				return nil, err
			}
			installed := led.Modules[se.ent.Name]
			se.notInstalled = installed == nil
			se.folderChanged = installed == nil || installed.Digest != se.v.Meta.Digest
		}
		early = runReport(checkParts(rec, trustParts(man, layers, nil)), limit, entries)
		manifestChanged = early.Status != trust.StatusTrusted
		needTrust = manifestChanged || repl != nil || slices.ContainsFunc(entries, func(se *syncEntry) bool { return se.folderChanged || needsPin(se) })
		need = neededTrust(early, repl, entries)
	} else {
		for _, se := range entries {
			se.covered = true
		}
	}
	// The removals are listed now, from lock.json and the manifest alone, and a run that cannot
	// ask for an answer it needs is refused here, once, before anything is fetched (Plan 6a §9.3
	// step 4).
	if res.Removals, err = e.syncRemovals(sc, want, led, entries, limit); err != nil {
		return nil, err
	}
	// --force answers the removals, never the trust question (user decision, 2026-10-22); --trust
	// answers it for the kinds it covers (Plan 9 §3.4).
	var missing TrustKinds
	if needTrust && req.Trust == nil {
		missing = need &^ opts.Trust
	}
	trustMissing, removalsMissing := missing != 0, len(res.Removals) > 0 && req.ConfirmRemovals == nil && !opts.Force
	if !opts.DryRun && (trustMissing || removalsMissing) {
		report := TrustReport{Status: res.Trust.Status, Changes: []TrustChange{}, Replacements: []Replacement{}}
		if sc.Kind == target.Project {
			report.Status = early.Status
		}
		// A folder is read for the refusal too: it is local, containment has passed, and its
		// content stays in the run's staging. Its pre-install hooks, and the project's pre-install
		// script around its install, refuse the next run too, so the hint answers them with the rest
		// (§3.5), a refusal for removals alone included (CR10).
		for _, se := range entries {
			if se.spec.Kind == source.KindFolder && se.v.ContentDir == "" {
				if err := e.resolveSync(ctx, sc, se, run); err != nil {
					return nil, err
				}
			}
		}
		hooks, err := e.hookNeeds(entries, led)
		if err != nil {
			return nil, err
		}
		scripts, err := e.scriptNeeds(sc)
		if err != nil {
			return nil, err
		}
		if trustMissing {
			report = trustReport(early)
			report.ManifestChanged = manifestChanged
			repl.add(&report)
			folderChanges(&report, entries, led)
			if err := e.folderEdits(sc, &report, entries, req.Display); err != nil {
				return nil, err
			}
			notFixed(&report, entries)
			pinReasons(&report, entries, false)
			if err := e.fillContent(sc, &report, entries, led, templates, e.newProbeRun(ctx), opts.Display); err != nil {
				return nil, err
			}
		}
		missing |= (hooks | scripts) &^ opts.Trust
		removals := []Removal{}
		if removalsMissing {
			removals = res.Removals
		}
		return nil, e.syncRefusal(sc, req, report, removals, missing, removalsMissing, entries)
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
		savedRep = runReport(checkParts(rec, trustParts(man, layers, entries)), limit, entries)
		res.Trust = trustReport(savedRep)
		res.Trust.ManifestChanged = manifestChanged
		repl.add(&res.Trust)
		folderChanges(&res.Trust, entries, led)
		if err := e.folderEdits(sc, &res.Trust, entries, req.Display); err != nil {
			return nil, err
		}
		notFixed(&res.Trust, entries)
		pinReasons(&res.Trust, entries, true)
		if err := e.fillContent(sc, &res.Trust, entries, led, templates, probes, opts.Display); err != nil {
			return nil, err
		}
		for i := range res.Trust.Changes {
			c := &res.Trust.Changes[i]
			se := entryOf(entries, *c)
			c.InstallsToTargets = res.Trust.Targets != nil && c.Kind != trust.ChangeRemoved && se != nil && len(se.ent.Targets) == 0
		}
	}
	// Plan every module now, from the bytes that were hashed, before the trust question: a module
	// that cannot be planned stops the run before anything is asked (Plan 6a §9.3 step 10).
	sel, err := e.selections()
	if err != nil {
		return nil, err
	}
	sp, err := e.planSync(sc, entries, led, templates, sel, opts, probes, req.Modules, man.PostInstall != "")
	if err != nil {
		return nil, err
	}
	var acc *acceptance
	// answered is what the terminal's answer alone accepted: a No to the removals records it (Plan
	// 6a X6), never what --trust covered, whose grant waits for the receipt (R4, R-9-16).
	var answered *acceptance
	if sc.Kind == target.Project && res.Trust.Status != trust.StatusTrusted {
		// The changes --trust covers go to the receipt; the user is asked about the rest (§3.4).
		covered, asked := res.Trust.split(opts.Trust)
		res.Trusted = append(res.Trusted, covered.consents()...)
		switch {
		case opts.DryRun:
		case asked.kinds() != 0 && req.Trust == nil: // refused at step 4 already; kept so no path applies untrusted content
			return nil, e.syncRefusal(sc, req, res.Trust, []Removal{}, asked.kinds(), false, entries)
		default:
			if covered.kinds() != 0 && req.Show != nil {
				req.Show(covered)
			}
			ans := TrustAnswer{All: true, Kinds: opts.Trust & TrustBare}
			if asked.kinds() != 0 {
				given, err := req.Trust(asked)
				if err != nil {
					return nil, err
				}
				ans.All, ans.Modules = given.All, given.Modules
				if answered, err = accept(savedRep, asked, given, entries); errors.Is(err, ErrDeclined) {
					answered = nil
				} else if err != nil {
					return nil, err
				}
			}
			// An activation commits its new list with the apply, so it applies all it planned or
			// nothing: an answer leaving some entries as installed is refused.
			if act != nil && !ans.All && len(ans.Modules) > 0 {
				return nil, errs.WithHints(errors.New("lola profile applies all of its changes or none; trust them all, or cancel"),
					"see them: "+e.commandOf(req, "-n"))
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
				// the pass after the answer renders again under a budget of its own (Plan 7 §6.2)
				fresh, err := e.newTemplateRun(sc, active)
				if err != nil {
					return nil, err
				}
				if sp, err = e.planSync(sc, entries, led, fresh, sel, opts, probes, req.Modules, man.PostInstall != ""); err != nil {
					return nil, err
				}
			}
		}
	}
	res.Modules, res.Findings, res.Notices = sp.modules, append(res.Findings, sp.findings...), append(res.Notices, sp.notices...)
	for _, se := range entries {
		if se.pin != nil && (se.from == "" || se.key != "") {
			pin := *se.pin
			pin.Profile = se.part()
			res.Pins = append(res.Pins, pin)
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
	if len(res.Removals) > 0 && !opts.DryRun && !opts.Force {
		if req.ConfirmRemovals == nil { // refused at step 4 already; kept so no path removes unasked
			return nil, e.syncRefusal(sc, req, TrustReport{Status: res.Trust.Status, Changes: []TrustChange{}, Replacements: []Replacement{}},
				res.Removals, 0, true, entries)
		}
		yes, err := req.ConfirmRemovals(slices.Clone(res.Removals))
		if err != nil {
			return nil, err
		}
		if !yes {
			return nil, e.declineRemovals(req, man, layers, led, rec, answered, entries)
		}
	}
	res.Findings = append(res.Findings, probes.findings...)

	pr, err := e.prepare(sc, led, changes, copies, nil, opts, res.Result)
	if ce, ok := errors.AsType[*plan.ConflictError](err); ok && ce.Overridable && len(res.Removals) > 0 {
		// The sync with --force its hint names also answers the removals (user decision,
		// 2026-10-22): the refusal says which, so that hint asks nothing it has not shown.
		var names []string
		for _, r := range res.Removals {
			what := strings.Join(r.Targets, ", ")
			if r.WholeModule {
				what = "whole module"
			}
			names = append(names, r.Module+" ("+what+")")
		}
		err = fmt.Errorf("%w\n  with --force it also removes, without asking: %s", err, strings.Join(names, ", "))
	}
	if err != nil {
		return nil, err
	}
	if act != nil {
		pr.plan.Ledger.Profiles = act.list // written in the apply's commit (Plan 6b §6.3 step 5)
	}
	keepUnhooked(copies, pr.jobs, opts)
	// A changed active list is a change the apply writes, even when it moves no module.
	// So is a pending project post-install script that runs alone (Plan 9 §5.5).
	res.InSync = len(res.Steps) == 0 && len(res.Pins) == 0 && len(res.Removals) == 0 && len(pr.scripts.alone) == 0 &&
		!slices.ContainsFunc(copies, func(c moduleCopy) bool { return c.replace }) && (act == nil || !act.changed)
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
	for _, j := range pr.scripts.alone {
		res.Changed = append(res.Changed, j.name)
	}
	slices.Sort(res.Changed)
	res.Changed = slices.Compact(res.Changed)
	if err := e.confirmTrusted(opts, res.Result); err != nil || opts.DryRun {
		if err != nil {
			return nil, err
		}
		// A dry run ends with the command that applies it: the user's own run without -n, and
		// the flags that answer its questions off a terminal: --trust for the kinds given and the
		// kinds the run would be refused without (R-9-6), and -y for its removals.
		// A sync in sync applies a trust it needs too: its project's scripts' (Plan 9 §7).
		needTrust := res.Trust.Status != trust.StatusTrusted && res.Trust.Status != TrustStatusNotApplicable
		if !res.InSync || needTrust || res.needs&^opts.Trust != 0 {
			res.ToApply = e.commandOf(req)
			missing := res.needs
			if needTrust {
				missing |= res.Trust.kinds()
			}
			missing &^= opts.Trust
			var answers []string
			if len(res.Removals) > 0 && !opts.Force {
				answers = append(answers, "-y")
			}
			res.EquivalentCommands = []string{res.ToApply}
			if missing != 0 || len(answers) > 0 {
				res.OffTerminal = e.commandOf(req.trusting(opts.Trust|missing), answers...)
				res.EquivalentCommands = []string{res.OffTerminal}
			}
		}
		return res, nil
	}
	defer func() { err = res.afterGrants(err) }() // a failure from here on carries the grants written
	apply := func(mw []plan.ManifestWrite) error {
		return e.commit(ctx, pr, opts, res.Result, mw, func(*plan.Plan) error { return e.makePluginData(sc, changes) })
	}
	// The trust record is written once, after the apply: it records the entries the user accepted,
	// and the top-level targets when they were accepted, as lola saves them; every other entry
	// keeps its record (Plan 6a §7.4), and so does every part of a project profile the run does
	// not check (Plan 6b §19 R2).
	var trusted *trust.Record
	if acc != nil {
		trusted = &trust.Record{View: acc.record(rec.View, trustParts(man, layers, nil))}
	}
	if pinned, profPins := pinsOf(entries, ""), profilePins(layers, entries); len(pinned) > 0 || len(profPins) > 0 {
		var files []string
		if len(pinned) > 0 {
			files = append(files, filepath.Base(e.files(sc).manifest))
		}
		for _, p := range profPins {
			files = append(files, projectProfileFile(p.layer.Key))
		}
		err = e.saveManifest(sc, man, manifest.Change{Set: pinned}, profPins, trusted, "the project's files, lock.json and "+andJoin(files)+" were updated",
			syncAgain(e.SyncCommand(sc)), apply)
	} else if err = apply(nil); err == nil && trusted != nil {
		if err = e.saveTrust(sc, trusted.View); err != nil {
			msg := "lola could not record that you trust " + acc.files(filepath.Base(e.files(sc).manifest))
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
// §19 X2): Trust all is the user's own run with --trust for the kinds given and the kinds of the
// changes it accepted (Plan 9 §3.5); a partial answer names every entry it accepted, whose record
// changes even when nothing is rendered, and every module the run changed, so a run limited to
// them applies the same plan and records the same trust. -y is added when the run's removals
// were confirmed.
func (e *Engine) equivalent(req SyncRequest, acc *acceptance, res *SyncResult) string {
	run := req.trusting(req.Options.Trust | res.Trust.kinds())
	var yes []string
	if len(res.Removals) > 0 && !req.Options.Force {
		yes = append(yes, "-y")
	}
	if req.Profiles != nil {
		return e.commandOf(run, yes...)
	}
	if !acc.all {
		run.Modules = slices.Clone(res.Changed)
		for _, m := range acc.names() {
			run.Modules = append(run.Modules, shown(m))
		}
		slices.Sort(run.Modules)
		run.Modules = slices.Compact(run.Modules)
	}
	return e.SyncCommand(req.Scope, run.rerun(yes...)...)
}

// pinsOf is each entry of the run from the file part names (syncEntry.part: "" for the scope's
// manifest) with the new pin the sync writes for it. A profile of the user's own is never
// written (Plan 6b §6.4, D7): lock.json records the version its entry installs.
func pinsOf(entries []*syncEntry, part string) []manifest.Entry {
	var out []manifest.Entry
	for _, se := range entries {
		if se.pin != nil && !se.own() && se.part() == part {
			out = append(out, se.pinned())
		}
	}
	return out
}

// profilePins is the pins a sync writes into each project profile of layers (Plan 6b §6.4, §19
// R1), in layer order: one profileEdit per profile with an entry of the run that gets a pin.
func profilePins(layers []Layer, entries []*syncEntry) []profileEdit {
	var out []profileEdit
	for _, l := range layers {
		if l.Key == "" {
			continue
		}
		if pins := pinsOf(entries, string(profile.KindProject)+":"+l.Key); len(pins) > 0 {
			out = append(out, profileEdit{layer: l, pins: pins})
		}
	}
	return out
}

// trustPart is one file whose entries a project-scope run checks against the trust record (Plan
// 6b §8.1, §19 R2): project.yml (key "") or a project profile of the run's layers (key its name,
// name its qualified name), with view the entries of it that win, as the run read them, or with
// the pins the run writes. shadowed names the modules the profile lists whose entry a later
// layer replaces: they are neither checked nor accepted until they win (ruling R-6b3a-3), so an
// entry is always reviewed with the content it installs.
type trustPart struct {
	key, name string
	view      trust.View
	shadowed  []string
}

// trustParts is the parts a run checks: project.yml's and each project profile's of layers, the
// active ones and those being turned on, each with its winning entries only; with the pins
// entries write when entries is not nil. Every other part of the record is carried, never
// checked (§19 R2).
func trustParts(man *manifest.Manifest, layers []Layer, entries []*syncEntry) []trustPart {
	out := []trustPart{{view: trust.ViewOf(man.Applied(manifest.Change{Set: pinsOf(entries, "")}))}}
	for i, l := range layers {
		if l.Key == "" {
			continue
		}
		p := trustPart{key: l.Key, name: string(profile.KindProject) + ":" + l.Key}
		m := &manifest.Manifest{}
		for _, ent := range l.Entries {
			if slices.ContainsFunc(layers[i+1:], func(later Layer) bool {
				return slices.ContainsFunc(later.Entries, func(o manifest.Entry) bool { return o.Name == ent.Name })
			}) {
				p.shadowed = append(p.shadowed, ent.Name)
				continue
			}
			m.Modules = append(m.Modules, ent)
		}
		p.view = trust.ViewOf(m.Applied(manifest.Change{Set: pinsOf(entries, p.name)}))
		// An entry from a remote profile the profile extends is recorded with it (§8.1, §19 S4).
		for k := range p.view.Modules {
			p.view.Modules[k].Extends = l.via[p.view.Modules[k].Name]
		}
		out = append(out, p)
	}
	return out
}

// checkParts is trust.Check of each part against its part of rec, as one report: the top-level
// targets are project.yml's, each change of a profile's part names the profile, and the status
// is trusted only when every part is. A shadowed entry's record is left out of the check, so it
// is not reported removed.
func checkParts(rec *trust.Record, parts []trustPart) trust.Report {
	out := trust.Report{Status: trust.StatusTrusted, Changes: []trust.Change{}}
	for _, p := range parts {
		rv := rec.View.Part(p.key)
		rv.Modules = slices.DeleteFunc(rv.Modules, func(e trust.Entry) bool { return slices.Contains(p.shadowed, e.Name) })
		rep := trust.Check(&trust.Record{View: rv, Exists: rec.Exists}, p.view)
		if p.key == "" {
			out.Targets = rep.Targets
		}
		for _, c := range rep.Changes {
			c.Profile = p.name
			out.Changes = append(out.Changes, c)
		}
		if rep.Status != trust.StatusTrusted {
			out.Status = rep.Status
		}
	}
	slices.SortStableFunc(out.Changes, func(a, b trust.Change) int { return strings.Compare(a.Module, b.Module) })
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
// that names its module (named) or gives --trust covering hooks; a pending project post-install
// script likewise, while project.yml still names one (postScript).
func (e *Engine) planSync(sc Scope, entries []*syncEntry, led *ledger.Ledger, templates *templateRun, sel *config.Selections, opts Options,
	probes *probeRun, named []string, postScript bool) (syncPlan, error) {
	sp := syncPlan{modules: []ModuleResult{}}
	for _, se := range entries {
		name := se.ent.Name
		ent := se.pinned()
		x, err := exclusions(sel, sc, name)
		if err != nil {
			return syncPlan{}, err
		}
		opts.entryFrom = se.from
		intents, finds, notes, mr, err := e.planModule(sc, se.v, se.targets, &ent, x, opts, probes, templates)
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
			Entry: &ledger.Entry{From: se.from, Source: ent.Source, Ref: ent.Ref, Path: ent.Path, Components: ent.Components.Clone(), Commit: se.v.Commit, Digest: se.v.Meta.Digest,
				Targets: mr.Targets, RenderedWith: mr.renderedWith}})
		replace, err := needsCopy(e.files(sc).modules, name, installed, se.v.Meta.Digest)
		if err != nil {
			return syncPlan{}, err
		}
		// A target the module is not installed on has never had its hooks run: the copy is
		// replaced to run them, as for a new version.
		replace = replace || addsTargets(installed, mr.Targets)
		// A pending post-install hook runs only in a copy replaced from the hashed version, after
		// the consent keyed on those bytes (Plan 6a §9.3 step 10, C1), and is offered only to a
		// run that names its module or gives --trust covering hooks: any other run keeps it
		// pending, asking nothing, and says how to run it.
		pending := installed != nil && installed.HooksPending && declaresPostInstall(se.v.Module.DeclaredHooks)
		if pending && !replace && !opts.Trust.Covers(TrustHooks) && !slices.Contains(named, name) {
			text := "pending post-install hook for " + name + " → " + e.SyncCommand(sc, name)
			if opts.Ask == nil {
				text += " (off a terminal: " + e.SyncCommand(sc, name, TrustHooks.Arg()) + ")"
			}
			sp.notices = append(sp.notices, Notice{Level: LevelWarn, Text: text})
			pending = false
		}
		// A pending project post-install script runs alone in the copy as it is, only with consent for
		// its current digest, and is offered only to a run that names its module or gives --trust
		// covering scripts (Plan 9 §5.5, A4); any other run keeps it pending and says how to run it.
		offer := installed != nil && installed.ScriptsPending && postScript && !replace && !pending && sc.Kind == target.Project
		noticed := offer && !opts.Trust.Covers(TrustScripts) && !slices.Contains(named, name)
		if noticed {
			sp.notices = append(sp.notices, e.pendingScriptNotice(sc, name, opts))
			offer = false
		}
		shows := se.v
		sp.copies = append(sp.copies, moduleCopy{name: name, src: se.v.ContentDir, replace: replace || pending,
			hooks: se.v.Module.DeclaredHooks, targets: mr.Targets, runHooks: replace || pending, forced: pending && !replace,
			pending: pending, pendingScript: offer, scriptNotice: noticed, shows: &shows, again: []string{"sync", name}})
	}
	return sp, nil
}

// acceptance is the user's trust answer applied to a run (Plan 6a §7.4, §9.3 step 7): the
// entries accepted, by their names in the record and the file, per part of the record (the key
// of a trustPart: "" for project.yml, a project profile's name, Plan 6b §8.1); whether the
// top-level targets are; and the modules that leave the run as installed, by the names the
// report shows.
type acceptance struct {
	all     bool
	modules map[string][]string
	targets bool
	leave   []string
}

// files names, for a message, the files whose trust the answer records: manifest (the scope
// manifest's base name) for project.yml's part, and each project profile whose part it changes.
func (a *acceptance) files(manifest string) string {
	var out, keys []string
	for k := range a.modules {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if k == "" {
			out = append(out, manifest)
		} else {
			out = append(out, projectProfileFile(k))
		}
	}
	if a.targets && !slices.Contains(out, manifest) {
		out = append([]string{manifest}, out...)
	}
	if len(out) == 0 {
		return manifest
	}
	return andJoin(out)
}

// names is every module whose entry the answer accepted in any part, in order, once.
func (a *acceptance) names() []string {
	var out []string
	for _, ms := range a.modules {
		out = append(out, ms...)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// record is the trust record r with the answer accepted (trust.Accept) in each of parts, the
// files the run checked, as the run read them; every other part of r is kept as it is.
func (a *acceptance) record(r trust.View, parts []trustPart) trust.View {
	for _, p := range parts {
		targets := p.key == "" && a.targets
		if ms := a.modules[p.key]; len(ms) > 0 || targets {
			r = trust.Accept(r, p.key, p.view, ms, targets)
		}
	}
	return r
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
	acc := &acceptance{all: ans.All, modules: map[string][]string{}, targets: rep.Targets != nil && (ans.All || ans.Kinds.Covers(TrustEntries))}
	for _, c := range rep.Changes {
		if !ans.All && !slices.Contains(ans.Modules, c.Module) && !ans.Kinds.Covers(c.kind()) {
			if !slices.Contains(acc.leave, c.Module) {
				acc.leave = append(acc.leave, c.Module)
			}
			continue
		}
		key := strings.TrimPrefix(c.Profile, string(profile.KindProject)+":")
		acc.modules[key] = append(acc.modules[key], names[c.Module])
		if se := entryOf(entries, c); rep.Targets != nil && c.Kind != trust.ChangeRemoved && se != nil && len(se.ent.Targets) == 0 {
			acc.targets = true
		}
	}
	// A replacement comes only with an activation, whose answer is all or none (Plan 6b §4.4, P10).
	replaced := len(rep.Replacements) > 0 && (ans.All || ans.Kinds.Covers(TrustProfiles))
	if len(acc.modules) == 0 && !acc.targets && !replaced {
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
// changes what lola render installs. Retry is the run to repeat: lola sync, or the activation
// as lola profile, with the scope's flags.
type RemovalsDeclinedError struct {
	Recorded, Render bool
	Again            []string
	Retry            string
}

func (e *RemovalsDeclinedError) Error() string {
	return "removals declined; the project was not changed"
}
func (e *RemovalsDeclinedError) Unwrap() error { return ErrDeclined }

// declineRemovals is a No to a run's removals (Plan 6a §9.3 step 9, §19 X6): nothing is applied,
// but the trust the user accepted (acc, nil when nothing was asked) is recorded, without the pins
// lola did not write: the answer stands, and the next sync asks again only about the removals and
// the accepted entries (of entries, the run's) whose new pin or folder content it did not install.
func (e *Engine) declineRemovals(req SyncRequest, man *manifest.Manifest, layers []Layer, led *ledger.Ledger, rec *trust.Record, acc *acceptance,
	entries []*syncEntry) error {
	sc := req.Scope
	if acc == nil {
		return &RemovalsDeclinedError{Retry: e.commandOf(req)}
	}
	view := acc.record(rec.View, trustParts(man, layers, nil))
	if err := e.saveTrust(sc, view); err != nil {
		return errs.WithHints(fmt.Errorf("lola could not record that you trust %s: %w", acc.files(filepath.Base(e.files(sc).manifest)), err),
			syncAgain(e.SyncCommand(sc)))
	}
	was, err := rec.View.Hash()
	if err != nil {
		return err
	}
	now, err := view.Hash()
	if err != nil {
		return err
	}
	d := &RemovalsDeclinedError{Recorded: !rec.Exists || now != was, Render: e.rendersOther(sc, rec.View, view, man, led, entries),
		Retry: e.commandOf(req)}
	for _, se := range entries {
		installed := led.Modules[se.ent.Name]
		folder := se.spec.Kind == source.KindFolder && (installed == nil || installed.Digest != se.v.Meta.Digest)
		if slices.Contains(acc.modules[se.key], se.ent.Name) && (se.pin != nil || folder) {
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
	out := TrustReport{Status: rep.Status, Changes: []TrustChange{}, Replacements: []Replacement{}}
	if rep.Targets != nil {
		out.Targets = &trust.FieldChange{Field: rep.Targets.Field, From: shownValue(rep.Targets.From), To: shownValue(rep.Targets.To)}
	}
	for _, c := range rep.Changes {
		c.Module, c.Profile = shown(c.Module), shown(c.Profile)
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
		se := entryOf(entries, *c)
		if se == nil {
			continue
		}
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
		k := slices.IndexFunc(rep.Changes, se.owns)
		if k < 0 {
			rep.Changes = append(rep.Changes, TrustChange{Change: trust.Change{Module: shown(se.ent.Name), Profile: se.part(), Kind: trust.ChangeChanged,
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
func (e *Engine) syncRefusal(sc Scope, req SyncRequest, rep TrustReport, removals []Removal, missing TrustKinds, removalsMissing bool,
	entries []*syncEntry) error {
	var answers, hints []string
	// missing may hold only the hook kind, which the hint adds and the message does not name.
	trustMissing, yesGiven := missing&TrustBare != 0, req.ConfirmRemovals != nil
	if trustMissing {
		manifestPath := e.files(sc).manifest
		name := filepath.Join(filepath.Base(filepath.Dir(manifestPath)), filepath.Base(manifestPath))
		// The files that changed: project.yml and each project profile with a change of its own,
		// not a pin or folder content the run adds.
		var files []string
		for _, c := range rep.Changes {
			file := name
			if c.Profile != "" {
				file = projectProfileFile(strings.TrimPrefix(c.Profile, string(profile.KindProject)+":"))
			}
			if !c.FolderContent && len(c.Fields) > 0 && !slices.Contains(files, file) {
				files = append(files, file)
			}
		}
		if rep.Targets != nil && !slices.Contains(files, name) {
			files = append([]string{name}, files...)
		}
		var causes, pins, folders []string
		// A replacement first, so a cause that follows never reads as qualifying it.
		for _, r := range rep.Replacements {
			causes = append(causes, r.Profile+" replacing "+r.Replaces)
		}
		switch {
		case !rep.ManifestChanged || len(files) == 0:
		case rep.Status == trust.StatusNew && len(files) == 1:
			causes = append(causes, files[0]+", which has not been trusted on this machine")
		case rep.Status == trust.StatusNew:
			causes = append(causes, andJoin(files)+", which have not been trusted on this machine")
		default:
			causes = append(causes, "changes to "+andJoin(files))
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
	// Each hint repeats the run in its output format (Plan 8 §9.2), with --trust as given; the
	// one that accepts the changes adds exactly the kinds missing (Plan 9 §3.5).
	again := func(r SyncRequest, extra ...string) string {
		return e.commandOf(r, append(slices.Clone(req.Display), extra...)...)
	}
	hints = append(hints, "see them: "+again(req, "-n"))
	accepting := req.trusting(req.Options.Trust | missing)
	switch {
	case trustMissing && (removalsMissing || yesGiven):
		hints = append(hints, "then accept them: "+again(accepting, "-y"))
	case trustMissing:
		hints = append(hints, "then accept them: "+again(accepting))
	default:
		hints = append(hints, "then confirm them: "+again(accepting, "-y"))
	}
	for _, se := range entries {
		// An install of the folder would take all of it, never the entry's selection.
		if trustMissing && se.folderChanged && se.spec.Components == nil {
			hints = append(hints, "edited it yourself? "+e.installFolder(sc, se.spec.Location, req.Display...))
		}
	}
	run := "lola sync"
	if req.Profiles != nil {
		run = "lola profile"
	}
	msg := run + " needs " + strings.Join(answers, "; and ") + ", and there is no terminal to ask on"
	if trustMissing && yesGiven {
		msg += "; -y confirms removals only; accept them with " + missing.Arg()
	}
	return errs.WithHints(&TrustRefusalError{Report: rep, Removals: removals, msg: msg}, hints...)
}

// andJoin joins phrases as prose: "a", "a and b", "a, b and c".
func andJoin(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// syncRemovals is what the run removes (Plan 6a §9.3 step 9), from lock.json and the layers
// alone: the targets each entry of the run no longer has, then each installed module of the run
// no layer wants (want), whole.
func (e *Engine) syncRemovals(sc Scope, want []Wanted, led *ledger.Ledger, entries []*syncEntry, limit syncLimit) ([]Removal, error) {
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
		if find(want, name) == nil && limit.has(name) {
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
// user's other files. It checks every entry whose Source and Path the trust record does not
// hold, and every one whose source as written (a relative one joined to the project) names a
// path inside the project, which a pulled link can lead out of; a held source outside the
// project is one the user installed or trusted. Locations are canonical (source.Parse); a root
// or location that cannot be resolved is refused, never let through. display is the run's
// output flags, which the install it hints keeps.
func (e *Engine) containLocalSources(sc Scope, entries []*syncEntry, display []string) error {
	var root string
	for _, se := range entries {
		local := source.IsLocal(se.spec.Kind, se.spec.Location)
		// A folder of the user's own profile is theirs, as user.yml's are; a project profile's is
		// checked as project.yml's are (Plan 6b §4.3, §19 R5). An entry whose location the record
		// trusts is not checked again unless it is written inside the project, where a pulled link
		// could lead it out.
		if !local || se.own() || (se.covered || se.sameSource) && !lexicallyInside(sc, se.ent.Source) {
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
			refused := fmt.Errorf("%s: local source %s resolves outside the project (%s)", se.ent.Name,
				untrusted.Text(se.ent.Source, untrusted.MaxName), untrusted.Text(se.spec.Location, untrusted.MaxName))
			if se.spec.Components != nil {
				return refused // an install of the folder would take all of it, never the entry's selection
			}
			// Validation refused any other source the cache has for the name, except a local one
			// that is gone, which the install replaces (§6.5): the install works as hinted.
			return errs.WithHints(refused, "install it yourself if you trust it: "+
				e.Command(sc, "install", append(installArgs(se.spec.Location, "", se.ent.Path), display...)...))
		}
	}
	return nil
}

// lexicallyInside reports whether a local source, as written in the manifest, names a path
// inside the project root before any symbolic link is followed: a relative path joined to the
// root, an absolute path or a file URL, below the root as given or as resolved, compared as the
// OS compares paths (paths.KeyID). A file URL that does not parse counts as inside, so it is
// checked.
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
		p = filepath.Join(sc.ProjectDir, p) // ../<project>/x names the project too
	}
	for _, root := range []string{sc.ProjectDir, canonicalRoot(sc.ProjectDir)} {
		// Compared as the OS compares paths, so a case variant of the root is the root.
		if rel, err := filepath.Rel(paths.KeyID(root), paths.KeyID(p)); err == nil && filepath.IsLocal(rel) {
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
		rep.Changes = append(rep.Changes, TrustChange{Change: trust.Change{Module: se.ent.Name, Profile: se.part(), Kind: kind,
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
func (e *Engine) folderEdits(sc Scope, rep *TrustReport, entries []*syncEntry, display []string) error {
	for i := range rep.Changes {
		c := &rep.Changes[i]
		se := entryOf(entries, *c)
		if !c.FolderContent || se == nil {
			continue
		}
		loc := se.spec.Location
		inTree, err := source.InWorkTree(loc)
		if err != nil {
			return err
		}
		if inTree {
			c.ReviewEdits = "review the edits: " + e.reviewFolder(loc)
		}
		if se.spec.Components == nil { // an install of the folder would take all of it
			c.InstallOwn = "edited it yourself? " + e.installFolder(sc, loc, display...)
		}
	}
	return nil
}

// needsPin reports whether a sync writes a pin for the entry into its file: a git or archive
// entry of the manifest or of a project profile under --update or without one; a folder is
// never pinned, and a profile of the user's own never written (Plan 6b §6.4, §19 R1). Each needs
// the user's trust at project scope (Plan 6a §7.3).
func needsPin(se *syncEntry) bool {
	return !se.own() && se.spec.Kind != source.KindFolder && (se.update || se.ent.Digest == "")
}

// SyncNames is the module names lola sync takes in sc, for completion: every module the scope's
// layers want, installed or not, and every module installed there, in name order. It reads the
// manifest, lock.json and the user's own active profiles, under the profiles lock taken shared
// as a dry run takes it (none before any lola made the lock file), and no other lock; it runs no
// git, so it does not check that lock.json is untracked, which every command it completes does.
// So it never reads a project profile: a committed lock.json could name one, and completion would
// parse checked-in files no command has checked (Plan 6b §19 S5). The modules of active project
// profiles that are not installed are left out.
func (e *Engine) SyncNames(sc Scope) ([]string, error) {
	unlock, err := e.readProfiles(true)
	if err != nil {
		return nil, err
	}
	defer unlock()
	man, led, _, err := e.load(sc, true)
	if err != nil {
		return nil, err
	}
	own := led.Clone()
	own.Profiles = slices.DeleteFunc(own.Profiles, func(a ledger.Active) bool {
		return strings.HasPrefix(a.Name, string(profile.KindProject)+":")
	})
	layers, _, err := e.layers(sc, man, own)
	if err != nil {
		return nil, err
	}
	want, _ := desired(layers)
	names := sortedNames(led)
	for _, w := range want {
		if led.Modules[w.Name] == nil {
			names = append(names, w.Name)
		}
	}
	slices.Sort(names)
	return names, nil
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

// refuseMissingManifest refuses a sync of a scope with no manifest and no profile in play: nothing
// a layer wants (want), no active profile and no module a profile installed (led). There is
// nothing it could make the scope match. A scope with no manifest that a profile wants modules
// in, or once did, syncs as an empty manifest (Plan 6b §6.2, §10, §19 R4), so the modules of a
// profile that is gone are removed; an existing manifest with no modules means "remove
// everything".
func (e *Engine) refuseMissingManifest(sc Scope, want []Wanted, led *ledger.Ledger) error {
	if len(want) > 0 || len(led.Profiles) > 0 || slices.ContainsFunc(sortedNames(led), func(n string) bool { return led.Modules[n].From != "" }) {
		return nil
	}
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
		"start one: "+e.Command(sc, "install")+" <source>", "or sync another project: lola sync -C <dir>")
}

// syncLimit is the set of modules a sync acts on (Plan 6a §3.1): the names given, or every
// module when none was and the run is not only those names. An activation's run is only the
// modules it changes (Plan 6b §6.3 step 4), which may be none.
type syncLimit struct {
	names []string
	only  bool
}

// all reports whether the run acts on every module.
func (l syncLimit) all() bool { return !l.only && len(l.names) == 0 }

func (l syncLimit) has(name string) bool { return l.all() || slices.Contains(l.names, name) }

// limitTo is the run names limit sync to, each of them an entry a layer wants (want) or a module
// installed there that no layer wants any more; any other name is refused before anything else,
// listing both.
func (e *Engine) limitTo(sc Scope, layers []Layer, want []Wanted, led *ledger.Ledger, names []string) (syncLimit, error) {
	var l syncLimit
	for _, name := range names {
		if find(want, name) == nil && led.Modules[name] == nil {
			base := layers[len(layers)-1].Name
			if len(layers) > 1 {
				var profiles []string
				for _, ly := range layers[:len(layers)-1] {
					profiles = append(profiles, ly.Name)
				}
				base += " or a profile (" + strings.Join(profiles, ", ") + ")"
			}
			listed, gone := []string{}, []string{}
			for _, w := range want {
				listed = append(listed, shown(w.Name))
			}
			for _, n := range sortedNames(led) {
				if find(want, n) == nil {
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
	if limit.all() {
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
func outOfSync(want []Wanted, led *ledger.Ledger, limit syncLimit, sync string) (n Notice, ok bool) {
	if limit.all() {
		return Notice{}, false
	}
	var names []string
	for _, w := range want {
		in := led.Modules[w.Name]
		if !limit.has(w.Name) && (in == nil || w.Digest != "" && w.Digest != in.Digest) {
			names = append(names, w.Name)
		}
	}
	for _, name := range sortedNames(led) {
		if !limit.has(name) && find(want, name) == nil {
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
// the name (§6.5); the effective targets are known: the entry's, else (a profile's entry has its
// profile's already) the manifest's (Plan 6b §6.2). Errors name the file the entry is in.
//
// lola never writes a profile of the user's own (Plan 6b §6.4, D7): its unpinned git or archive
// entry takes the pin lock.json records for its source, ref and path, as a manifest entry takes
// the one sync wrote into it, until --update resolves its ref again. A project profile's entry is
// checked, pinned and re-pinned as a manifest entry is (§19 R1). Which layer won before (from)
// does not matter: a planted lock.json sets it too, so resolveSync checks reachability instead. --update naming a profile's
// pinned entry is refused, since its pin is in a file lola does not write; an --update that names
// no module leaves those entries at their pins, with a notice each (kept).
func (e *Engine) validateSync(sc Scope, man *manifest.Manifest, want []Wanted, led *ledger.Ledger, req SyncRequest,
	limit syncLimit, catalogs *syncCatalogs) (_ []*syncEntry, kept []Notice, _ error) {
	var out []*syncEntry
	for _, w := range want {
		ent, name, update := w.Entry, w.Name, req.Update
		if !limit.has(name) {
			continue
		}
		base, from, file := filepath.Base(w.path), "", ""
		if w.key != "" {
			// A project profile's entry is checked, pinned and re-pinned as project.yml's (Plan 6b
			// §6.4, §19 R1).
			base, from, file = projectProfileFile(w.key), w.From, w.path
		}
		if w.own() {
			base, from, file = shown(w.path), w.From, w.path
		}
		// The pin of the user's own profile, or of a remote profile a profile extends, is in a
		// file lola sync never writes (Plan 6b §6.4).
		if w.own() || w.via != nil {
			pinFile, editPin := e.ownPin(sc, w.From, w.path, w.via)
			switch in := led.Modules[name]; {
			case ent.Digest != "" && update && !limit.all():
				// --update of a remote profile applies what it moves itself; a hand edit needs a sync.
				never, then := "lola never writes", editPin+", then: "+e.SyncCommand(sc, name)
				if remoteLayer(w.From) || w.via != nil {
					never, then = "only lola profile --update changes", editPin
				}
				return nil, nil, errs.WithHints(fmt.Errorf("%s: its pin is in %s, which %s", name, pinFile, never), then)
			case ent.Digest != "" && update:
				update = false
				kept = append(kept, Notice{Level: LevelInfo, Text: name + ": --update leaves the pin " + pinFile + " sets; " + editPin})
			case w.own() && ent.Digest == "" && !update && in != nil && in.Digest != "" && in.Source == ent.Source && in.Ref == ent.Ref && in.Path == ent.Path &&
				!source.IsFolderForm(ent.Source):
				ent.Commit, ent.Digest = in.Commit, in.Digest
			}
		}
		spec, ok, err := source.Parse(ent.Source, sourceRoot(sc))
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", name, err)
		}
		if !ok {
			return nil, nil, fmt.Errorf("%s: %s is not a source; sources are %s", name, untrusted.Text(ent.Source, untrusted.MaxName),
				source.AcceptedForms)
		}
		// A remote profile's entry names a remote source (D4): Parse never looks a URL or scp form
		// up on disk, and a path that still reads as local, a Windows drive, is refused (ruling
		// R-6b3b-5).
		if (remoteLayer(w.From) || w.via != nil) && source.IsLocal(spec.Kind, spec.Location) {
			return nil, nil, fmt.Errorf("%s: %s is on this machine; a remote profile names only git repositories and https archives", name,
				untrusted.Text(ent.Source, untrusted.MaxName))
		}
		spec.Ref, spec.Path, spec.Components = ent.Ref, ent.Path, ent.Components.Clone()
		if err := source.Validate(spec); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", name, err)
		}
		repin := e.SyncCommand(sc, "--update", name)
		if (w.own() || w.via != nil) && w.Digest != "" {
			_, repin = e.ownPin(sc, w.From, w.path, w.via)
		}
		if err := checkPinShape(name, base, spec.Kind, ent, repin); err != nil {
			return nil, nil, err
		}
		gone, err := e.refuseOtherSource(name, base, spec, e.commandOf(req))
		if err != nil {
			return nil, nil, err
		}
		ts := ent.Targets
		if len(ts) == 0 {
			ts = man.Targets
		}
		if len(ts) == 0 {
			return nil, nil, errs.WithHints(fmt.Errorf("%s: %s gives it no targets", name, base),
				"list the assistants in "+base+", for example targets: [claude-code]")
		}
		for _, id := range ts {
			if _, err := e.target(id); err != nil {
				return nil, nil, fmt.Errorf("%s: %w", name, err)
			}
		}
		// An entry installed through a marketplace from a source its catalog named, not the
		// marketplace's own, keeps the redirect rule of such a source in every fetch (Plan 6c
		// §10, ruling R-6c-13): the flag only adds refusals, so a forged market cannot loosen one.
		if ent.Market != nil && !source.SameLocation(spec.Kind, ent.Market.Source, spec.Location) {
			spec.FromCatalog = true
		}
		// An --update of an entry installed through a marketplace follows its catalog only within
		// the same source, path and ref (Plan 6c §6.6): the catalog's archive sum and redirect
		// rule then apply to the fetch.
		var follow *marketPick
		if update && ent.Market != nil && catalogs != nil {
			var n *Notice
			if follow, n = e.follow(sc, catalogs, ent, spec, req.Options.DryRun, req.Display); n != nil {
				kept = append(kept, *n)
			}
			if follow != nil {
				listed := follow.entry.Spec()
				spec.SHA256, spec.FromCatalog = listed.SHA256, spec.FromCatalog || listed.FromCatalog
			}
		}
		out = append(out, &syncEntry{ent: ent, from: from, file: file, key: w.key, via: w.via, ledgerPin: w.own() && w.Digest == "" && ent.Digest != "", goneFrom: gone,
			spec: spec, targets: ts, update: update, follow: follow})
	}
	return out, kept, nil
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
// is the user's sync, which the hint repeats once the name is out of the cache; base names the
// file the entry is in (the scope manifest, or a profile). A local source
// the cache records that no longer exists, for a local entry, is not refused: its description
// (describeSource) is returned, and the version the run publishes replaces it.
func (e *Engine) refuseOtherSource(name, base string, spec source.Spec, again string) (string, error) {
	o, found, err := e.Store.Origin(name)
	if err != nil || !found {
		return "", err
	}
	if o.Type == string(spec.Kind) && source.SameLocation(spec.Kind, o.Source, spec.Location) && o.Path == spec.Path && o.Components.Equal(spec.Components) {
		return "", nil
	}
	if o.LocalGone() && source.IsLocal(spec.Kind, spec.Location) {
		return describeSource(o.Source, o.Path, o.Components), nil
	}
	return "", errs.WithHints(fmt.Errorf("%s in your cache comes from %s, and %s asks for %s; the cache is shared by every project on this machine",
		name, describeSource(o.Source, o.Path, o.Components), base, describeSource(spec.Location, spec.Path, spec.Components)),
		"remove it from your cache: "+errs.ShellJoin([]string{"lola", "cache", "rm", name})+" (add -y off a terminal)", "then: "+again)
}

// goneNotices are the notices of the entries whose cached local source no longer exists, which
// the run caches from the entry's source instead (Plan 6a §6.5).
func goneNotices(entries []*syncEntry) []Notice {
	var out []Notice
	for _, se := range entries {
		if se.goneFrom != "" {
			out = append(out, Notice{Level: LevelWarn, Text: fmt.Sprintf("%s: your cache has it from %s, which no longer exists; caching it from %s",
				se.ent.Name, se.goneFrom, describeSource(se.spec.Location, se.spec.Path, se.spec.Components))})
		}
	}
	return out
}

// describeSource names a source and its path for a message; both may be someone else's.
func describeSource(location, path string, comps *source.Components) string {
	s := untrusted.Text(location, untrusted.MaxName)
	if path != "" {
		s += " (path " + untrusted.Text(path, untrusted.MaxName) + ")"
	}
	if comps != nil {
		s += " selecting " + comps.String()
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
		get := stage
		if se.follow != nil {
			get = func() error { return e.followPin(ctx, se, run) } // the catalog's checks, and its commit pin, not the ref's head
		}
		if err := get(); err != nil {
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
		// an entry the user has not trusted must name a commit on its ref (Plan 6a §6.4). So must
		// a profile's pin read from a project's lock.json, which a file planted in the checkout
		// could name (Plan 6b §11, S5).
		if (!se.covered || se.ledgerPin && sc.Kind == target.Project) && se.spec.Kind == source.KindGit {
			if err := source.CheckReachable(ctx, se.spec, ent.Commit, run); err != nil {
				base, repin := e.pinnedIn(sc, se)
				return syncPinError(name, base, ent, fmt.Errorf("%s: %w", name, err), repin)
			}
		}
		v.Commit = ent.Commit
		se.v = v
		return nil
	}
	st, err := e.fetchPin(ctx, name, se.spec, ent.Commit, ent.Digest, run)
	if err != nil {
		base, repin := e.pinnedIn(sc, se)
		return syncPinError(name, base, ent, err, repin)
	}
	se.v, se.staged, se.headIfAbsent = st.Version, &st, true
	se.v.Module.Warnings = st.LoadWarnings // what a cache hit would report, not what detection did
	return nil
}

// pinnedIn is where an entry's pin is written, for a pinned fetch's error: the manifest or a
// project profile, whose pin sync --update rewrites; lock.json for the unpinned entry of the
// user's own profile, which --update resolves again; or that profile itself, which only the user
// edits (Plan 6b §6.4).
func (e *Engine) pinnedIn(sc Scope, se *syncEntry) (file, repin string) {
	switch {
	case se.via != nil:
		return e.ownPin(sc, se.from, se.file, se.via)
	case se.from == "":
		return filepath.Base(e.files(sc).manifest), e.SyncCommand(sc, "--update", se.ent.Name)
	case se.key != "":
		return projectProfileFile(se.key), e.SyncCommand(sc, "--update", se.ent.Name)
	case se.ledgerPin:
		return "lock.json", e.SyncCommand(sc, "--update", se.ent.Name)
	}
	return e.ownPin(sc, se.from, se.file, nil)
}

// ownPin is where the pin of an entry from a profile of the user's own, or from a remote
// profile a profile extends (via), is, as a message names it, and how to change it (Plan 6b
// §6.4): from is the entry's layer and path its file. A remote profile's pin changes only with
// lola profile remote:<name> --update; any other's is edited by hand.
func (e *Engine) ownPin(sc Scope, from, path string, via *trust.ExtendsView) (file, repin string) {
	q := strings.TrimPrefix(from, ledger.FromProfile(""))
	if via != nil {
		q = via.Name
	}
	if via != nil || remoteLayer(from) {
		return shown(q), "update it: " + e.Command(sc, "profile", q, "--update")
	}
	return shown(path), "edit the pin in " + shown(path)
}

// remoteLayer reports whether the layer named from is a remote profile (profile:remote:<name>).
func remoteLayer(from string) bool {
	return strings.HasPrefix(from, ledger.FromProfile(string(profile.KindRemote)+":"))
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

// prepareFetch fetches spec into run and prepares it there; the content must declare name, or
// none, and then it is name, as the entry records it (a catalog entry's name, Plan 6c §6.3).
func (e *Engine) prepareFetch(ctx context.Context, name string, spec source.Spec, run string) (store.Staged, error) {
	f, err := source.Fetch(ctx, spec, run)
	if err != nil {
		return store.Staged{}, err
	}
	return e.Store.Prepare(store.PutRequest{Staged: f.Dir, FallbackName: name, Name: name, Source: spec.Location, Type: string(spec.Kind),
		Path: spec.Path, Ref: spec.Ref, Commit: f.Commit, Executables: f.Executables, Now: e.Now(), Components: spec.Components})
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
