package engine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
	"github.com/trevor-vaughan/lola-go/internal/yamldoc"
)

// SyncRequest is one lola sync: make Scope match its manifest. Modules limits the run to the
// modules named; empty means every entry and every installed module. Update re-pins every git and
// archive entry of the run at its ref. Options are as for install: DryRun, Force and the hook
// options; Targets, Strict and Rules are not used.
type SyncRequest struct {
	Scope   Scope
	Modules []string
	Update  bool
	// Installed is lola update: the run is the modules installed in the scope (those of Modules,
	// when given) that a layer lists; it installs and removes nothing. Update is set with it.
	Installed bool
	// Refetch is lola update --fetch: every ref is fetched, however fresh its cache head.
	Refetch bool
	Options Options
	// Trust asks the user to trust the changes to project.yml since they last trusted it, the pins
	// the sync would write and the folders whose content changed: all of them, or the entries the
	// answer names. nil means it cannot ask: a sync that needs the question is refused before any
	// network contact. The CLI passes a Trust that answers All for --trust; -y never sets it. It is
	// asked only about the changes of the kinds Options.Trust does not cover; Show, when set, is
	// shown the changes it covers, as a record, before they reach the receipt, and an error it
	// returns (a Ctrl+C while the record is shown) ends the sync before anything is recorded.
	Trust func(TrustReport) (TrustAnswer, error)
	Show  func(TrustReport) error
	// ConfirmRemovals asks the user to confirm the removals the sync would make. nil means it
	// cannot ask: a sync with removals is refused. The CLI passes an always-yes ConfirmRemovals
	// for -y.
	ConfirmRemovals func([]Removal) (bool, error)
	// ConfirmBack, when set, asks the removals question in ConfirmRemovals' place right after Trust
	// asked the trust question in this run, with a way back: its ErrBack asks Trust again, the run
	// planned again from the report as first asked. Set it only where Trust shows the question; an
	// answer given without one has no screen to go back to. nil: ConfirmRemovals asks, and its
	// ErrBack is returned.
	ConfirmBack func([]Removal) (bool, error)
	// Yes is the run's -y, which a hint repeating the run keeps.
	Yes bool
	// At is --at: per module of the run, the version a refusal showed, a git commit or a folder's
	// or archive's content digest. A run given it applies only the versions a refusal showed: one
	// whose source serves another, and any other module whose version would change, is refused
	// before anything is written (resolveSync, unpinnedChange), so its --trust and -y answer only
	// what was listed. AtShared says the command gave --at to several scopes: At is this scope's
	// modules' alone (maybe none), a module it does not include is not refused, and the run still
	// applies only the versions a refusal showed.
	At       map[string]string
	AtShared bool
	// Profiles, when set, changes the scope's active profile and applies the change: the run is
	// limited to the modules whose state differs from what the new profile wants, and the new
	// profile is written to lock.json in the same commit as the apply.
	Profiles *ProfileChange
	// Display is the run's output flags (--json, --plain, --no-pager as given), which the install
	// commands an --update's marketplace notices name keep.
	Display []string
	// move, when set, is a move's edits of this scope's files (Move): the scope's layers are read
	// with them, the run is the moved modules, lola's own writes are trusted before the check, and
	// the files are written in the apply's transaction.
	move *moveEdit
	// seed is, per module, the version another scope installed, which an entry of yours a
	// cross-scope move brings takes when neither pins.yaml nor lock.json pins it.
	seed map[string]*ledger.Entry
	// held says the caller holds the profiles lock and this scope's lock (a cross-scope Move, which
	// takes both scopes' in lockOrder): the run takes neither, and restores nothing.
	held bool
	// write, when set, is the profile files a --split, an --edit --bundle or an --edit --new writes
	// in this run's transaction, which its layers are read with (profileWrites); writeOnly limits the
	// run to them: it applies nothing. profileArgs is the lola profile arguments that run it again (name,
	// --split, --to; name, --edit, --bundle), which the run's hints repeat: lola sync cannot stand
	// in for it (it cannot load the file a split fixes, nor writes what an edit writes).
	write       *profileWrites
	writeOnly   bool
	profileArgs []string
	// versions, when set, is the versions an install writes the scope's files for, which the run
	// reads in place of those the scope declares (probeRun.chosen): an install that changes them
	// plans the rewrite of the scope's other modules before it records them (installAll).
	versions map[string]string
}

// TrustAnswer is the user's answer to the trust question: All accepts every change of the run;
// otherwise Modules names the accepted entries, as the report names them, and Kinds accepts every
// change of the kinds --trust covers. An answer accepting nothing is a Cancel.
type TrustAnswer struct {
	All     bool
	Modules []string
	Kinds   TrustKinds
}

// Removal is what a sync removes from one module: the whole module, which the manifest no longer
// lists, or the targets its entry no longer names, with every path lock.json says lola wrote there
// for it. Its strings, from lock.json, have been through untrusted.Text.
type Removal struct {
	Module      string        `json:"module"`
	Targets     []string      `json:"targets"`
	WholeModule bool          `json:"whole_module"`
	Paths       []RemovedPath `json:"paths"`
	// Why is the reason when it is not the sync's own: "dropped by your pick change" for an
	// install that narrows picks (PickDeletes).
	Why string `json:"why,omitempty"`
	// Scope is the scope the paths are under, project or user. Where names it with its root,
	// "project <root>" or "user scope (<home>)", when the removals listed with it span more than
	// one scope (an install's --scope both); "" else.
	Scope string `json:"scope"`
	Where string `json:"where,omitempty"`
}

// RemovedPath is a path lola wrote for a target and a removal deletes; Path carries a JSON
// member's key after a space.
type RemovedPath struct {
	Target string `json:"target"`
	Path   string `json:"path"`
}

// RemovalsShown reports whether shown, removals the user confirmed, covers rs: every path rs
// deletes, by module and target, was among shown's, and a removal that lists no paths was shown
// for its module on each of its targets. A plan made again later can find more, which a caller
// that only takes what was confirmed must ask about.
func RemovalsShown(shown, rs []Removal) bool {
	seen := map[string]bool{}
	keys := func(r Removal) []string {
		var out []string
		for _, p := range r.Paths {
			out = append(out, r.Module+"\x00"+p.Target+"\x00"+p.Path)
		}
		if len(r.Paths) == 0 {
			for _, t := range r.Targets {
				out = append(out, r.Module+"\x00"+t)
			}
		}
		return out
	}
	for _, r := range shown {
		for _, k := range keys(r) {
			seen[k] = true
		}
	}
	for _, r := range rs {
		for _, k := range keys(r) {
			if !seen[k] {
				return false
			}
		}
	}
	return true
}

// ErrDeclined is a sync the user answered No to: nothing more was done.
var ErrDeclined = errors.New("declined")

// ErrBack is a question's answer when the user goes back a screen (Esc). Sync takes it from
// ConfirmBack as going back to the trust question; anywhere else it returns it, for the caller's
// screen before.
var ErrBack = errors.New("back")

// TrustStatusNotApplicable is the trust status of the user scope, whose files are your own.
const TrustStatusNotApplicable = "not_applicable"

// TrustReport is how the scope's manifest, with the pins the sync writes, differs from what the
// user last trusted. Recorded says this run wrote the trust record. Targets is the top-level
// targets' change, nil (and absent from JSON) when the record covers them. ManifestChanged says the
// manifest itself differs from the record; when it does not, the changes are the pins the sync
// would write and folders whose content changed, which the diff's header says.
type TrustReport struct {
	Status          string             `json:"status"`
	Recorded        bool               `json:"recorded"`
	Targets         *trust.FieldChange `json:"targets,omitzero"`
	TargetVersions  *trust.FieldChange `json:"target_versions,omitzero"`
	ProfileTargets  []ProfileTargets   `json:"profile_targets,omitzero"`
	Changes         []TrustChange      `json:"changes"`
	Replacements    []Replacement      `json:"replacements"`
	ManifestChanged bool               `json:"-"`
	// Drops are, for a question or refusal about TargetVersions, the findings of the values the
	// new versions drop that another era keeps (target.Finding.KeptSince), for the modules the run
	// installs; DropsRun is the dry run that lists them all.
	Drops    []target.Finding `json:"-"`
	DropsRun string           `json:"-"`
}

// versionDrops are the findings of fs that drop a value another era of their target keeps, for a
// target whose version rep's target_versions change changes.
func versionDrops(rep TrustReport, fs []target.Finding) []target.Finding {
	if rep.TargetVersions == nil {
		return nil
	}
	from, _ := rep.TargetVersions.From.(map[string]string)
	to, _ := rep.TargetVersions.To.(map[string]string)
	var out []target.Finding
	for _, f := range fs {
		was, had := from[f.Target]
		now, has := to[f.Target]
		if f.KeptSince != "" && (had != has || was != now) {
			out = append(out, f)
		}
	}
	return out
}

// ProfileTargets is a project profile's top-level targets' change in a TrustReport, which the
// record does not cover: Profile is the profile's qualified name, as TrustChange.Profile gives it,
// and the change is shown as project.yml's targets are.
type ProfileTargets struct {
	Profile string `json:"profile"`
	trust.FieldChange
}

// topChanged reports whether project.yml's top-level targets or its target_versions changed: what
// an entry installing to the top-level targets is trusted with.
func (rep TrustReport) topChanged() bool { return rep.Targets != nil || rep.TargetVersions != nil }

// TargetsOf is the change of the top-level targets of the file of part, a TrustChange.Profile:
// project.yml's for "", else that project profile's; nil when the record covers them.
func (rep TrustReport) TargetsOf(part string) *trust.FieldChange {
	if part == "" {
		return rep.Targets
	}
	for i := range rep.ProfileTargets {
		if rep.ProfileTargets[i].Profile == part {
			return &rep.ProfileTargets[i].FieldChange
		}
	}
	return nil
}

// Replacement is a project profile that an activation puts in place of an active profile of the
// same name: never silently, but as a trust change of its own, accepted with the activation's Trust
// all. Modules are the modules whose winning entry it changes. Its strings have been through
// untrusted.Text.
type Replacement struct {
	Profile  string   `json:"profile"`
	Replaces string   `json:"replaces"`
	Modules  []string `json:"modules"`
}

// TrustChange is one module's change with the content it brings: the files the new version adds,
// changes and removes against the version installed (FilesNote says why every file is listed as
// added when lola cannot compare), its module MCP servers, and every hook it declares. Every string
// from the manifest or the module has been through untrusted.Text. A removed module, and every
// module of a report made before anything was fetched, brings none.
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
	// top-level targets changed: accepting it accepts them.
	InstallsToTargets bool `json:"-"`
	// ReviewEdits and InstallOwn are the lines the diff adds for a folder whose content changed:
	// the git command that shows the edits, when the folder is in a git work tree, and the install
	// that records the content as the user's own.
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

// TrustRefusalError is a sync refused, before anything a pull asked for is fetched, because it
// needs answers it cannot ask for: the user's trust (SyncRequest.Trust is nil), the confirmation of
// its removals (ConfirmRemovals is nil), or both. Report holds the trust changes, with fields only
// for the git and archive entries not read yet, and changes: [] when only the removals are missing;
// Removals lists the removals, with the files the versions read no longer ship, [] when there are
// none.
type TrustRefusalError struct {
	Report   TrustReport
	Removals []Removal
	// Notices says what the refusal could not read: an entry whose fetch failed, whose removals
	// it therefore does not list.
	Notices []Notice
	msg     string
}

func (e *TrustRefusalError) Error() string { return e.msg }

// JSONFields are the keys the error document of a --json refusal carries next to error and hints:
// the changes it needs trust for and the removals, so a CI consumer gets them without a second run.
func (e *TrustRefusalError) JSONFields() map[string]any {
	notices := e.Notices
	if notices == nil {
		notices = []Notice{}
	}
	return map[string]any{"trust": e.Report, "removals": e.Removals, "notices": notices}
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
	Profile        string `json:"profile,omitempty"` // the project profile it is written into; "" for the manifest
	Ref            string `json:"ref"`
	Commit         string `json:"commit"`
	Digest         string `json:"digest"`
	PreviousDigest string `json:"previous_digest"`
	Reason         string `json:"reason"`
	Written        bool   `json:"written"`
}

// SyncResult is what a sync reports: Result over its one scope, the pins it writes, and whether the
// scope was already in sync (nothing to write, pin or remove). Changed names, in order, the modules
// the run changes (a step, a pin, a removal or a replaced copy): a sync limited to them with the
// same answers applies the same plan. A dry run that is not in sync names the command that applies
// it, ToApply, and, when that needs consent, the form for no terminal, OffTerminal, with --trust;
// EquivalentCommands is the last of them, [] for any other run. Neither carries a -y the user did
// not give. Equivalent is, after a trust answer, the command that gives the same answers off a
// terminal.
type SyncResult struct {
	*Result
	Scope              string      `json:"scope"`
	Root               string      `json:"root"`
	Trust              TrustReport `json:"trust"`
	Pins               []Pin       `json:"pins"`
	Removals           []Removal   `json:"removals"`
	InSync             bool        `json:"in_sync"`
	EquivalentCommands []string    `json:"equivalent_commands"`
	// Rewrites are the modules' files the sync rewrites for another era of their target, after
	// the version the scope declares changed.
	Rewrites    []Rewrite `json:"rewrites"`
	Failed      []string  `json:"-"` // lola update's modules left as installed because their fetch failed; findings in JSON
	Gone        []string  `json:"-"` // lola update's modules whose source folder is gone, so not checked; findings in JSON
	Installed   bool      `json:"-"` // the run was lola update (SyncRequest.Installed), whose human output says so
	Changed     []string  `json:"-"`
	Equivalent  string    `json:"-"`
	ToApply     string    `json:"-"`
	OffTerminal string    `json:"-"`
	// Created is the profile files a --new makes, in the order made: the members it makes first,
	// then the profile it switches to. Under -n they are what it would make.
	Created []Created `json:"created,omitempty"`
	// *Activation is set by an activation (SyncRequest.Profiles) only, adding its profile and
	// changes to the JSON document.
	*Activation
}

// syncEntry is one desired entry of a sync: validated (targets), then resolved to a version. v is
// read from a snapshot of the cache or a staged fetch in the run directory; staged is set while the
// version is not in the cache, and headIfAbsent says its commit must not move a head (a pinned
// fetch, or any in a project dry run). pin is set when the sync writes a new pin for the entry.
// covered says the trust record covers the entry (always at user scope); folderChanged says it is a
// covered folder at project scope whose content is not the version lock.json records, and
// notInstalled that lock.json records none. from is the profile layer the entry won from
// (Wanted.From), "" for the scope's manifest, and file that profile's path; key is a project
// profile's name (Wanted.key), "" for any other layer; ledgerPin says its pin is the one lock.json
// records for the unpinned entry of the user's own profile, pinsFile that it is the one pins.yaml
// records, and seeded that it is the version a move between scopes carries (SyncRequest.seed).
type syncEntry struct {
	ent        manifest.Entry
	from, file string
	key        string
	inherits   bool              // the entry installs to its project profile's top-level targets (Wanted.inherits)
	bundles    []string          // the project bundles the entry comes through, by part key (Wanted.bundles)
	member     *trust.MemberView // the remote profile a project bundle names that the entry comes from (Wanted.member)
	ledgerPin  bool
	pinsFile   bool
	seeded     bool
	goneFrom   string // the description (describeSource) of the local source the cache records for the name, which no longer exists
	covered    bool
	// sameSource says the record holds the entry with this Source and Path, whatever else of it
	// changed: its location was trusted, so containment need not check it again.
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
	follow        *marketPick // the catalog entry an --update follows, nil for none
	fresh         bool        // lola update's cache head for its ref was fetched within cache_fresh_for: the run does not fetch the ref
	failed        bool        // lola update could not fetch it: it stays as installed
	at            string      // the version --at says the run installs: a git commit, else a content digest
	atFresh       string      // the dry run without --at that a refusal for another version than at hints
	sourceGone    bool        // its source folder is gone: the version installed was rendered
	installed     string      // the digest lock.json records for the module, "" when none is installed
	targetsIn     targetsIn   // the file whose targets: the entry installs to
	notices       []Notice    // what resolving the entry has to say (a gone folder rendered from the cache)
}

// own reports whether the entry is from a profile of the user's own, trusted as all of theirs is; a
// project profile's entry is reviewed as project.yml's is.
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
	v.Member = se.member
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

// Sync makes a scope match its manifest: it validates every entry before any network contact or
// prompt, resolves each to the version it pins (an unpinned entry to its ref's version, which is
// pinned; --update fetches the ref again), publishes what it fetched to the cache, and applies one
// plan under the scope lock, writing new pins to the manifest in the same transaction. At project
// scope, a manifest the trust record does not cover, a pin the sync would write, or a covered
// folder whose content changed since lola installed it needs the user's trust: that content stays
// in the run's staging until the user trusts it, and the record is written once, after the apply.
func (e *Engine) Sync(ctx context.Context, req SyncRequest) (_ *SyncResult, err error) {
	sc, opts := req.Scope, req.Options
	defer func() { err = e.CoachUnresolved(err, []Scope{sc}, opts.DryRun, req.Display) }()
	newProbes := func() *probeRun {
		probes := e.newProbeRun(ctx)
		if req.versions != nil {
			probes.chosen[sc.Key()] = req.versions
		}
		return probes
	}
	if req.write != nil {
		e.creating = req.write
		defer func() { e.creating = nil }()
	}
	// --off (and a --switch to the base, which is --off) mends a bundle on that names a profile you
	// no longer have: it turns it off all the same.
	if ch := req.Profiles; ch != nil && e.recovering == nil {
		if isBase, isDefault := namesBase(ch.Name); ch.Action == ProfileOff || ch.Action == ProfileSwitch && (isBase || isDefault) {
			e.recovering = &recovery{}
			defer func() { e.recovering = nil }()
		}
	}
	// shownAt is the versions the run has read once it has resolved its entries (shownVersions),
	// which the hint that consents to a hook pins, so the consent covers the version shown alone.
	var shownAt map[string]string
	opts.Rerun = func(k TrustKinds) string {
		r := req.trusting(k)
		if shownAt != nil {
			r.At = shownAt
		}
		return e.commandOf(r, req.Display...)
	}
	opts.askScripts, opts.update, opts.activation = true, req.Update, req.Profiles != nil
	if home := (Scope{Kind: target.Project, ProjectDir: e.Paths.Home}); sc.Kind == target.Project && sc.Key() == home.Key() {
		return nil, errs.WithHints(fmt.Errorf("project scope at %s is your home directory, where the user scope installs",
			untrusted.Text(e.Paths.Home, untrusted.MaxName)), "sync the user scope: "+e.SyncCommand(Scope{Kind: target.User}))
	}
	// A remote profile the run needs that this machine lacks is restored from pins.yaml first,
	// under the profiles lock it takes and releases itself.
	extra := ""
	if req.Profiles != nil && req.Profiles.Action != ProfileOff {
		extra = req.Profiles.Name
	}
	var restored []Notice
	unlockProfiles := func() {}
	if !req.held {
		if restored, err = e.restoreRemotes(ctx, sc, extra, opts.Rerun(opts.Trust), opts.DryRun); err != nil {
			return nil, err
		}
		// The profiles lock comes first: the run reads the remote profiles once.
		if unlockProfiles, err = e.readProfiles(opts.DryRun); err != nil {
			return nil, err
		}
	}
	defer unlockProfiles()
	res := &SyncResult{Result: newResult(opts.DryRun), Installed: req.Installed, Scope: sc.Kind.String(), Root: root(sc), Pins: []Pin{}, Removals: []Removal{}, Rewrites: []Rewrite{},
		Trust: TrustReport{Status: TrustStatusNotApplicable, Changes: []TrustChange{}, Replacements: []Replacement{}}, EquivalentCommands: []string{},
		Failed: []string{}}
	res.Trusted = []Consent{}
	res.Notices = append(res.Notices, restored...)
	// A committed lock.json is refused before the profiles it names are read.
	if err := e.refuseTrackedLedger(ctx, sc, res.Result); err != nil {
		return nil, err
	}
	// --update follows the catalogs of the entries installed through your marketplaces, each
	// fetched once, under the markets lock: after the profiles lock, before the scope's. Without
	// --update no catalog is read.
	var catalogs *syncCatalogs
	var catalogNotices []Notice
	if req.Update {
		var unlockMarkets func()
		if catalogs, unlockMarkets, catalogNotices, err = e.fetchCatalogs(ctx, sc, req); err != nil {
			return nil, err
		}
		defer unlockMarkets()
	}
	// A move that carries picks out of selections.yaml writes it under the selections lock, taken
	// as an install takes it: after the markets lock, before the scope's (moveAcross holds it for a
	// move between scopes).
	if req.move != nil && len(req.move.selections) > 0 && !opts.DryRun && !req.held {
		unlockSelections, err := config.Lock(e.Paths.SelectionsLock)
		if err != nil {
			return nil, err
		}
		defer unlockSelections()
	}
	man, led, unlock, err := e.load(sc, opts.DryRun || req.held)
	if err != nil {
		return nil, err
	}
	defer unlock()
	defer func() { err = e.installedWayOut(sc, led, err, req) }()
	// An activation builds the layers of its old and new active profile from one read of each
	// profile; a profile on that does not change stops here, with its notice.
	names := req.Modules
	active := led.Profile // the profile the run renders templates with: an activation's new one
	var act *activation
	var repl *replacing
	var layers []Layer
	var layerNotices, overrides []Notice
	var want []Wanted
	var moveBefore *manifest.Manifest // the manifest as read, before a move's edit
	// pins is the pin source of your profiles' entries at user scope. An activation's change line
	// reads it, so loadPins runs once, where the first reader needs it.
	var pins *config.Pins
	loadPins := func() (err error) {
		if sc.Kind == target.User && pins == nil {
			pins, err = config.LoadPins(e.Paths.Pins)
		}
		return err
	}
	if req.Profiles != nil {
		var notes []Notice
		var dry []string
		if req.Options.DryRun {
			dry = []string{"-n"}
		}
		switch {
		case req.move != nil && req.move.creating != "":
			act, err = e.creatingActivation(sc, req.move.creating, req.move.top.Path)
		case req.Profiles.New:
			// --new: the files the switch makes are read in place of the disk until the apply
			// writes them, with lock.json, or nothing is written.
			var infos []profile.Info
			if infos, _, err = e.visible(sc); err != nil {
				return nil, err
			}
			if e.creating, err = e.newProfiles(ctx, sc, infos, *req.Profiles, e.switchRuns(sc, req.again()), req.Display); err != nil {
				return nil, err
			}
			defer func() { e.creating = nil }()
			res.Created = e.creating.made
			act, notes, err = e.startActivation(sc, led, *req.Profiles, req.rerun(dry...), req.again())
		default:
			act, notes, err = e.startActivation(sc, led, *req.Profiles, req.rerun(dry...), req.again())
		}
		if err != nil {
			return nil, err
		}
		res.Activation, res.Notices = act.report, append(append(res.Notices, notes...), e.replacementNotices(sc, replacements(act.infos, act.next))...)
		if !act.changed {
			res.InSync = true
			return res, nil
		}
		// Hints that repeat the run name the profile as resolved, and a switch to the base as the
		// --off it is.
		req.Profiles = &ProfileChange{Action: act.action, Name: act.report.Named, New: req.Profiles.New, From: req.Profiles.From, Bundle: req.Profiles.Bundle}
		pl, err := e.loadStack(sc, act.infos, led.Profile, act.next)
		if err != nil {
			return nil, err
		}
		top := e.baseLayer(sc, man, pl)
		old, _ := desired(pl.assemble(top, led.Profile))
		layers, layerNotices = pl.assemble(top, act.next), pl.notices
		if req.move != nil && req.move.creating != "" {
			// The profile a dry run's move would create reads as gone, which it is not.
			gone := e.goneProfile(sc, req.move.creating)
			layerNotices = slices.DeleteFunc(slices.Clone(layerNotices), gone.Same)
		}
		if req.move != nil {
			moveBefore = man
			layers, man = req.move.apply(layers), man.Applied(req.move.manifest)
		}
		active = act.next
		// Precedence notices only for the modules the named profile lists.
		named := pl.named[act.report.Named] // a bundle's members, each
		want, overrides = desiredNoting(layers, func(m string) bool {
			return slices.ContainsFunc(named, func(l Layer) bool {
				return slices.ContainsFunc(l.Entries, func(ent manifest.Entry) bool { return ent.Name == m })
			})
		}, act.infos)
		// A project profile the user names in place of their active namesake is a trust change of
		// its own, never silent.
		repl = replacingOf(replaced(led.Profile, act.next), old, want)
		if err := loadPins(); err != nil {
			return nil, err
		}
		all := layerChanges(want, led, top.Name, pinSource{pins: pins, seed: req.seed, update: req.Update})
		act.report.Changes = moved(all, old, want, led, act.report.Named)
		names = []string{}
		for _, c := range act.report.Changes {
			names = append(names, c.name)
		}
		// A module that stays where it is renders again when its templates read the list.
		rerender, err := e.rerenders(sc, led, act.next, e.Store.Load)
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
		if req.move != nil {
			moveBefore = man
			layers, man, names = req.move.apply(layers), man.Applied(req.move.manifest), req.move.modules
		}
		if req.writeOnly {
			names = []string{} // the files alone
		}
		// Only a profile on can override the base: then the notices name the layers as the scope's
		// profiles tell them apart.
		var infos []profile.Info
		if len(layers) > 1 {
			if infos, _, err = e.visible(sc); err != nil {
				return nil, err
			}
		}
		want, overrides = desiredNoting(layers, nil, infos)
		if req.move != nil {
			// An entry that leaves this scope's files uninstalled has nothing to remove here.
			names = slices.DeleteFunc(slices.Clone(names), func(n string) bool { return find(want, n) == nil && led.Modules[n] == nil })
		}
		// Not for an activation, which always has a profile in play: the one it turns on or off,
		// nor for lola update, which leaves each module no layer lists as installed, nor for a move
		// between profiles that are off here, which changes no layer.
		if !req.Installed && !req.writeOnly && (req.move == nil || !req.move.keeps(layers)) { // a split that applies nothing needs no base file
			if err := e.refuseMissingManifest(sc, want, led); err != nil {
				return nil, err
			}
		}
	}
	// The trust diff and the first plan pass render under one budget.
	templates, err := e.newTemplateRun(sc, active)
	if err != nil {
		return nil, err
	}
	var limit syncLimit
	if req.Installed {
		var leftOut []target.Finding
		if limit, leftOut, err = e.installedLimit(sc, want, led, names, req.Display); err != nil {
			return nil, err
		}
		res.Findings = append(res.Findings, leftOut...)
	} else {
		if limit, err = e.limitTo(sc, layers, want, led, names); err != nil {
			return nil, err
		}
		limit.only = act != nil || req.move != nil || req.writeOnly // a move's run is the modules it moves, which may be none here
		limit.files = req.writeOnly
	}
	res.Notices = append(append(append(res.Notices, layerNotices...), overrides...), e.sweepRuns()...)
	run, done, err := e.runDir()
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, done()) }()

	var age config.Age // the zero Age when no head is served from the cache: fresh never holds
	if req.Installed && !req.Refetch {
		if age, err = e.freshFor(); err != nil {
			return nil, err
		}
	}
	if err := loadPins(); err != nil {
		return nil, err
	}
	entries, kept, err := e.validateSync(sc, man, want, led, req, limit, catalogs, age, &res.Findings, pins)
	if err != nil {
		return nil, err
	}
	for _, name := range slices.Sorted(maps.Keys(req.At)) {
		k := slices.IndexFunc(entries, func(se *syncEntry) bool { return se.ent.Name == name })
		switch {
		case k < 0 && req.AtShared:
			continue
		case k < 0:
			without := req
			without.At = maps.Clone(req.At)
			delete(without.At, name)
			return nil, errs.WithHints(fmt.Errorf("--at names %s, which this run does not include", shown(name)), "drop it: "+e.commandOf(without, req.again()...))
		}
		if err := atKind(name, entries[k].spec.Kind, req.At[name]); err != nil {
			return nil, err
		}
		fresh := req
		fresh.At = nil
		entries[k].at, entries[k].atFresh = req.At[name], e.commandOf(fresh, append([]string{"-n"}, req.Display...)...)
	}
	res.Notices = append(append(res.Notices, catalogNotices...), kept...)
	// An activation counted the changes it leaves pending above; lola update's findings say what
	// installed modules it leaves out, and its notice the listed entries it does not install.
	switch {
	case req.Installed:
		res.Notices = append(res.Notices, e.listedNotInstalled(sc, want, led, req.again())...)
	case act == nil:
		if n, ok := outOfSync(want, led, limit, e.SyncCommand(sc)); ok {
			res.Notices = append(res.Notices, n)
		}
	}
	res.Notices = append(res.Notices, goneNotices(entries)...)
	var rec *trust.Record
	var early trust.Report
	manifestChanged, needTrust := false, false
	var need TrustKinds // the kinds of trust the changes known before any fetch need
	// spare is an unlimited run's top-level targets that changed while no entry it loads installs
	// to them: never asked about alone, recorded only with a consent that covers entries.
	spare := false
	if sc.Kind == target.Project {
		if rec, err = e.Trust.Load(sc.ProjectDir); err != nil {
			return nil, err
		}
		if req.move != nil {
			rec.View = req.move.trusted(rec.View, moveBefore)
		}
		if e.creating != nil { // what lola makes is trusted as it writes it (newProfiles)
			for _, key := range slices.Sorted(maps.Keys(e.creating.trusted)) {
				rec.View = rec.View.WithPart(key, e.creating.trusted[key])
			}
		}
		// Coverage is the entry's own: it decides containment and whether content the entry pins
		// may be cached before the question. Top-level targets the record does not cover are a
		// change of the report, which an entry that installs to them waits on. The entry of a
		// profile of the user's own is trusted as theirs; a project profile's is covered by that
		// profile's part of the record.
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
		// A folder of the user's own profile is the user's own.
		for _, se := range entries {
			if !se.covered || se.spec.Kind != source.KindFolder || se.own() {
				continue
			}
			if err := e.resolveSync(ctx, sc, se, run, req.Display); err != nil {
				return nil, err
			}
			installed := led.Modules[se.ent.Name]
			se.notInstalled = installed == nil
			se.folderChanged = installed == nil || installed.Digest != se.v.Meta.Digest
		}
		early = runReport(checkParts(rec, trustParts(man, layers, nil)), limit, entries)
		if spare = early.Targets != nil && !slices.ContainsFunc(entries, func(se *syncEntry) bool { return len(se.ent.Targets) == 0 }); spare {
			early = withoutTargets(early)
		}
		manifestChanged = early.Status != trust.StatusTrusted
		needTrust = manifestChanged || repl != nil || slices.ContainsFunc(entries, func(se *syncEntry) bool { return se.folderChanged || needsPin(se) })
		need = neededTrust(early, repl, entries)
	} else {
		for _, se := range entries {
			se.covered = true
		}
	}
	// The removals are listed now, from lock.json and the manifest alone, and a run that cannot ask
	// for an answer it needs is refused here, once, before anything a pull asked for is fetched.
	if !req.Installed { // lola update removes nothing
		if res.Removals, err = e.syncRemovals(sc, want, led, entries, limit); err != nil {
			return nil, err
		}
	}
	// --force answers the removals, never the trust question (user decision, 2026-10-22); --trust
	// answers it for the kinds it covers.
	var missing TrustKinds
	if needTrust && req.Trust == nil {
		missing = need &^ opts.Trust
	}
	// readEarly resolves se for a refusal, before the run would. An entry whose source fails to
	// serve it (sourceFailure) stays unread, as before this read: its removals are not listed, so
	// the refusal offers no -y for them, and unreadNotes says why, with the failure's hints. The run
	// that follows the refusal resolves it again and stops on, or reports, the failure as it always
	// has. A failure of lola's own state (the cache, the staging) stops the run here.
	var unreadNotes []Notice
	readEarly := func(se *syncEntry) error {
		err := e.resolveSync(ctx, sc, se, run, req.Display)
		if err == nil || ctx.Err() != nil || !sourceFailure(err) {
			return err
		}
		se.v, se.pin, se.staged = store.Version{}, nil, nil
		text := shown(se.ent.Name) + ": could not read the version this run would install, so the files it drops are not listed: " +
			untrusted.CleanErr(err).Error()
		if hints := shownAll(errs.Hints(err)); len(hints) > 0 {
			text += " → " + strings.Join(hints, "; ")
		}
		unreadNotes = append(unreadNotes, Notice{Level: LevelWarn, Text: text})
		return nil
	}
	// A run that cannot ask first fetches, into its staging, the entries the record covers that it
	// would pin: their source and ref are what the user trusted, so nothing a pull asked for is
	// fetched, and the refusal can name the files the new version drops (and its -y) at once. An
	// update that finds nothing new then needs no trust at all.
	if missing != 0 && !opts.DryRun {
		for _, se := range entries {
			if !se.covered || !needsPin(se) || se.v.ContentDir != "" {
				continue
			}
			if err := readEarly(se); err != nil {
				return nil, err
			}
		}
		needTrust = manifestChanged || repl != nil || slices.ContainsFunc(entries, func(se *syncEntry) bool { return se.folderChanged || needsPin(se) })
		need, missing = neededTrust(early, repl, entries), 0
		if needTrust {
			missing = need &^ opts.Trust
		}
	}
	trustMissing, removalsMissing := missing != 0, len(res.Removals) > 0 && req.ConfirmRemovals == nil && !opts.Force
	// selections is the picks the run plans with: selections.yaml, a move's, and the layers'.
	selections := func() (*config.Selections, error) {
		sel, err := e.selections()
		if err != nil {
			return nil, err
		}
		if req.move != nil {
			for _, c := range req.move.selections {
				sel.Set(c.Section, c.Module, c.Entry)
			}
		}
		return withPicks(sel, layers)
	}
	if !opts.DryRun && (trustMissing || removalsMissing) {
		report := TrustReport{Status: res.Trust.Status, Changes: []TrustChange{}, Replacements: []Replacement{}}
		if sc.Kind == target.Project {
			report.Status = early.Status
		}
		// A folder is read for the refusal too: it is local, containment has passed, and its
		// content stays in the run's staging. Its pre-install hooks, and the project's pre-install
		// script around its install, refuse the next run too, so the hint answers them with the
		// rest, a refusal for removals alone included (CR10).
		for _, se := range entries {
			if se.spec.Kind == source.KindFolder && se.v.ContentDir == "" {
				if err := e.resolveSync(ctx, sc, se, run, req.Display); err != nil {
					return nil, err
				}
			}
		}
		// With every trust it needs given, nothing keeps the run from reading each version now, as
		// it would next, so the refusal lists every removal its -y confirms.
		if !trustMissing {
			for _, se := range entries {
				if se.v.ContentDir != "" {
					continue
				}
				if err := readEarly(se); err != nil {
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
			pinReasons(&report, entries, req.updateFlag())
			if err := e.fillContent(sc, &report, entries, led, templates, newProbes(), opts.Display); err != nil {
				return nil, err
			}
			// What new target_versions drop is listed with the diff: each entry is planned as read
			// so far, else as the version installed, from your cache (one not there is left out).
			if report.TargetVersions != nil {
				var planned []*syncEntry
				for _, se := range entries {
					in := led.Modules[se.ent.Name]
					if se.v.ContentDir == "" && in != nil {
						v, err := e.Store.Load(se.ent.Name, versionID(in.Digest))
						if errors.Is(err, os.ErrNotExist) {
							continue
						}
						if err != nil {
							return nil, err
						}
						c := *se
						c.v = v
						se = &c
					}
					if se.v.ContentDir != "" {
						planned = append(planned, se)
					}
				}
				sel, err := selections()
				if err != nil {
					return nil, err
				}
				fresh, err := e.newTemplateRun(sc, active)
				if err != nil {
					return nil, err
				}
				sp, err := e.planSync(sc, planned, layers, active != nil, led, fresh, sel, opts, newProbes(), req.Modules, man.PostInstall != "")
				if err != nil {
					return nil, err
				}
				report.Drops, report.DropsRun = versionDrops(report, sp.findings), e.commandOf(req, "-n")
			}
		}
		missing |= (hooks | scripts) &^ opts.Trust
		removals := []Removal{}
		if removalsMissing {
			removals = res.Removals
		}
		// The files the versions read so far no longer ship, planned as the apply plans them (picks
		// and settings included), so the refusal lists only what the run would remove.
		if req.ConfirmRemovals == nil && !opts.Force {
			var read []*syncEntry
			for _, se := range entries {
				if se.v.ContentDir != "" {
					read = append(read, se)
				}
			}
			if len(read) > 0 {
				sel, err := selections()
				if err != nil {
					return nil, err
				}
				sp, err := e.planSync(sc, read, layers, active != nil, led, templates, sel, opts, newProbes(), req.Modules, man.PostInstall != "")
				if err != nil {
					return nil, err
				}
				removals = append(removals, e.droppedFiles(sc, led, sp.changes, res.Removals, opts)...)
			}
		}
		err = e.syncRefusal(sc, req, report, removals, missing, entries)
		if refusal, ok := errors.AsType[*TrustRefusalError](err); ok {
			refusal.Notices = unreadNotes
		}
		return nil, err
	}

	served := 0 // the entries lola update resolved from a fresh cache head, without a fetch
	for _, se := range entries {
		if se.v.ContentDir == "" { // a covered folder at project scope was read above
			// lola update leaves a module whose source it cannot fetch as installed and goes on; a
			// folder, and any failure of lola's own state, stops the run.
			err := e.resolveSync(ctx, sc, se, run, req.Display)
			if ff, ok := errors.AsType[*fetchFailedError](err); ok && req.Installed && se.spec.Kind != source.KindFolder {
				name := untrusted.Text(se.ent.Name, untrusted.MaxName)
				res.Failed = append(res.Failed, name)
				// The fetch's own hints (install git, check access, the refs there are) say what to
				// fix before the retry can work, so they come first.
				hints := append(shownAll(errs.Hints(ff.err)), "retry: "+e.Command(sc, "update", append([]string{se.ent.Name, "--fetch"}, req.Display...)...))
				res.Findings = append(res.Findings, target.Finding{Severity: target.SeverityWarn, Module: name, Kind: "module", Code: CodeFetchFailed,
					Text: "could not fetch it: " + untrusted.CleanErr(ff.err).Error() + "; it stays as installed",
					Hint: strings.Join(hints, "; ")})
				se.failed = true
				continue
			}
			if err != nil {
				return nil, err
			}
		}
		if se.sourceGone && req.Installed {
			name := untrusted.Text(se.ent.Name, untrusted.MaxName)
			res.Gone = append(res.Gone, name)
			res.Findings = append(res.Findings, target.Finding{Severity: target.SeverityWarn, Module: name, Kind: "module", Code: CodeSourceGone,
				Text: "source gone; not checked: its source folder " + untrusted.Text(se.spec.Location, untrusted.MaxName) + " does not exist, so the version installed was rendered",
				Hint: "restore the folder, or remove the module: " + e.Command(sc, "uninstall", append([]string{se.ent.Name}, req.Display...)...)})
		}
		if se.fresh && se.staged == nil {
			served++
		}
		if !se.sourceGone || !req.Installed { // lola update says it as a source-gone finding
			res.Notices = append(res.Notices, se.notices...)
		}
	}
	entries = slices.DeleteFunc(entries, func(se *syncEntry) bool { return se.failed })
	// A run given --at applies only the versions a refusal showed: any
	// other module whose installed version would change (a pin a pull wrote included) is refused
	// here, before anything is written, whatever its -y, --force or --trust say.
	if (len(req.At) > 0 || req.AtShared) && !opts.DryRun {
		for _, se := range entries {
			if se.at != "" || se.v.Meta.Digest == se.installed {
				continue
			}
			fresh := req
			fresh.At = nil
			return nil, errs.WithHints(fmt.Errorf("%s has a new version no refusal showed, and --at does not pin it: "+
				"a run given --at applies only the versions a refusal showed; nothing was written", shown(se.ent.Name)),
				"see what changed: "+e.commandOf(fresh, append([]string{"-n"}, req.Display...)...))
		}
	}
	if served > 0 {
		what := fmt.Sprintf("%d modules", served)
		if served == 1 {
			what = "1 module"
		}
		fetch := req
		fetch.Refetch = true
		res.Notices = append(res.Notices, Notice{Level: LevelInfo,
			Text: what + " served from your cache (fetched within cache_fresh_for) → " + e.commandOf(fetch, req.again()...)})
	}
	// The pins the sync writes are trust changes too: the question is about the manifest as it will
	// be saved.
	probes := newProbes()
	var savedRep trust.Report
	if sc.Kind == target.Project {
		savedRep = runReport(checkParts(rec, trustParts(man, layers, entries)), limit, entries)
		// Spare targets join a question the run asks anyway, or a --trust that covers entries.
		if spare && !opts.Trust.Covers(TrustEntries) && (req.Trust == nil || !needTrust || need&^opts.Trust == 0) {
			savedRep = withoutTargets(savedRep)
		}
		res.Trust = trustReport(savedRep)
		res.Trust.ManifestChanged = manifestChanged
		repl.add(&res.Trust)
		folderChanges(&res.Trust, entries, led)
		if err := e.folderEdits(sc, &res.Trust, entries, req.Display); err != nil {
			return nil, err
		}
		notFixed(&res.Trust, entries)
		pinReasons(&res.Trust, entries, req.updateFlag())
		if err := e.fillContent(sc, &res.Trust, entries, led, templates, probes, opts.Display); err != nil {
			return nil, err
		}
		for i := range res.Trust.Changes {
			c := &res.Trust.Changes[i]
			se := entryOf(entries, *c)
			c.InstallsToTargets = c.Kind != trust.ChangeRemoved && se != nil &&
				(res.Trust.topChanged() && len(se.ent.Targets) == 0 || se.inherits && res.Trust.TargetsOf(c.Profile) != nil)
		}
	}
	// Plan every module now, from the bytes that were hashed, before the trust question: a module
	// that cannot be planned stops the run before anything is asked.
	sel, err := selections()
	if err != nil {
		return nil, err
	}
	sp, err := e.planSync(sc, entries, layers, active != nil, led, templates, sel, opts, probes, req.Modules, man.PostInstall != "")
	if err != nil {
		return nil, err
	}
	var acc *acceptance
	// answered is what the terminal's answer alone accepted: a No to the removals records it, never
	// what --trust covered, whose grant waits for the receipt.
	var answered *acceptance
	var changes []plan.ModuleChange
	var copies []moduleCopy
	// before is the run as the trust question finds it, which going back to that question from the
	// removals question (ErrBack) restores; again says the question is being asked again.
	before := struct {
		entries                                    []*syncEntry
		removals                                   []Removal
		trust                                      TrustReport
		sp                                         syncPlan
		trusted, notices, findings, rewrites, pins int
	}{slices.Clone(entries), res.Removals, res.Trust, sp, len(res.Trusted), len(res.Notices), len(res.Findings), len(res.Rewrites), len(res.Pins)}
	again := false
	for {
		trustAsked := false
		if sc.Kind == target.Project && res.Trust.Status != trust.StatusTrusted {
			// The changes --trust covers go to the receipt; the user is asked about the rest.
			covered, asked := res.Trust.split(opts.Trust)
			res.Trusted = append(res.Trusted, covered.consents()...)
			switch {
			case opts.DryRun:
			case asked.kinds() != 0 && req.Trust == nil: // refused at step 4 already; kept so no path applies untrusted content
				return nil, e.syncRefusal(sc, req, res.Trust, []Removal{}, asked.kinds(), entries)
			default:
				if covered.kinds() != 0 && req.Show != nil && !again {
					if err := req.Show(covered); err != nil {
						return nil, err
					}
				}
				ans := TrustAnswer{All: true, Kinds: opts.Trust & TrustBare}
				if asked.kinds() != 0 {
					asked.Drops, asked.DropsRun = versionDrops(asked, sp.findings), e.commandOf(req, "-n")
					given, err := req.Trust(asked)
					if err != nil {
						return nil, err
					}
					trustAsked = true
					ans.All, ans.Modules = given.All, given.Modules
					if answered, err = accept(savedRep, asked, given, entries); errors.Is(err, ErrDeclined) {
						answered = nil
					} else if err != nil {
						return nil, err
					}
				}
				// An activation commits its new list with the apply, so it applies all it planned or
				// nothing: an answer leaving some entries as installed is refused.
				// So does a move, which writes its files whatever the answer.
				if (act != nil || req.move != nil) && !ans.All && len(ans.Modules) > 0 {
					return nil, errs.WithHints(errors.New("lola profile applies all of its changes or none; trust them all, or cancel"),
						"see them: "+e.commandOf(req, "-n"))
				}
				if acc, err = accept(savedRep, res.Trust, ans, entries); err != nil {
					return nil, err
				}
				// A partial answer takes the entries the user did not accept out of the run, as
				// installed, and plans the rest again from the same staged trees: the summary is the
				// apply.
				if len(acc.leave) > 0 {
					entries = slices.DeleteFunc(entries, func(se *syncEntry) bool { return slices.Contains(acc.leave, shown(se.ent.Name)) })
					// Clones: the report the user answered keeps every change it showed.
					res.Removals = slices.DeleteFunc(slices.Clone(res.Removals), func(r Removal) bool { return slices.Contains(acc.leave, r.Module) })
					res.Trust.Changes = slices.DeleteFunc(slices.Clone(res.Trust.Changes), func(c TrustChange) bool { return slices.Contains(acc.leave, c.Module) })
					if !acc.targets {
						res.Trust.Targets, res.Trust.TargetVersions = nil, nil
					}
					res.Trust.ProfileTargets = slices.DeleteFunc(slices.Clone(res.Trust.ProfileTargets), func(pt ProfileTargets) bool {
						return !acc.profileTargets[partKey(pt.Profile)]
					})
					res.Notices = append(res.Notices, Notice{Level: LevelWarn,
						Text: "left as installed until you trust them: " + nameList(acc.leave) + " → " + e.SyncCommand(sc)})
					// the pass after the answer renders again under a budget of its own
					fresh, err := e.newTemplateRun(sc, active)
					if err != nil {
						return nil, err
					}
					if sp, err = e.planSync(sc, entries, layers, active != nil, led, fresh, sel, opts, probes, req.Modules, man.PostInstall != ""); err != nil {
						return nil, err
					}
				}
			}
		}
		res.Modules, res.Findings, res.Notices = sp.modules, append(res.Findings, sp.findings...), append(res.Notices, sp.notices...)
		res.Rewrites = append(res.Rewrites, sp.rewrites...)
		for _, se := range entries {
			if se.pin != nil && (se.from == "" || se.key != "") {
				pin := *se.pin
				pin.Profile = se.part()
				res.Pins = append(res.Pins, pin)
			}
		}
		// Removals are confirmed before anything is published and before any hook consent is asked or
		// recorded: a refusal or a No leaves the cache as -n does.
		changes, copies = sp.changes, sp.copies
		for _, r := range res.Removals {
			if r.WholeModule {
				changes = append(changes, plan.ModuleChange{Name: r.Module, Targets: allTargets(led.Modules[r.Module], nil, &manifest.Manifest{})})
			}
		}
		// The files a version the run installs no longer ships (a component dropped or renamed, at
		// update, --update or a folder read again) are removals too, asked with the rest (one rule for
		// every deletion).
		res.Removals = append(res.Removals, e.droppedFiles(sc, led, changes, res.Removals, opts)...)
		if len(res.Removals) > 0 && !opts.DryRun && !opts.Force {
			// The manifest's removals were refused at; the files a version drops are known only now
			// that it is planned, so they are refused here, before the publish.
			if req.ConfirmRemovals == nil {
				return nil, e.syncRefusal(sc, req, TrustReport{Status: res.Trust.Status, Changes: []TrustChange{}, Replacements: []Replacement{}},
					res.Removals, 0, entries)
			}
			confirm, back := req.ConfirmRemovals, trustAsked && req.ConfirmBack != nil
			if back {
				confirm = req.ConfirmBack
			}
			yes, err := confirm(slices.Clone(res.Removals))
			if errors.Is(err, ErrBack) && back {
				// Back to the trust question: the run as it stood before its answer.
				entries, res.Removals, res.Trust, sp, acc, answered = slices.Clone(before.entries), before.removals, before.trust, before.sp, nil, nil
				res.Trusted, res.Notices, res.Findings = res.Trusted[:before.trusted], res.Notices[:before.notices], res.Findings[:before.findings]
				res.Rewrites, res.Pins, again = res.Rewrites[:before.rewrites], res.Pins[:before.pins], true
				continue
			}
			if err != nil {
				return nil, err
			}
			if !yes {
				return nil, e.declineRemovals(req, man, layers, led, rec, answered, entries)
			}
		}
		break
	}
	res.Findings = append(res.Findings, probes.findings...)

	shownAt = shownVersions(req, entries)
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
	if ce, ok := errors.AsType[*plan.ConflictError](err); ok {
		if hs := e.sameFileHints(ce, entries); len(hs) > 0 {
			err = errs.WithHints(err, hs...)
		}
	}
	if err != nil {
		return nil, err
	}
	pr.pins = pins
	for _, se := range entries {
		pr.resolved = append(pr.resolved, se.ent.Name)
		if se.seeded {
			pr.seeded = append(pr.seeded, se.ent.Name)
		}
	}
	if act != nil {
		pr.plan.Ledger.Profile = act.next // written in the apply's commit
	}
	keepUnhooked(copies, pr.jobs, opts)
	// A rewrite whose files come out the same byte for byte writes nothing: its notice says so.
	for _, r := range res.Rewrites {
		if slices.ContainsFunc(res.Steps, func(st plan.Step) bool { return st.Module == r.Module && st.Target == r.Target }) {
			continue
		}
		if i := slices.IndexFunc(res.Notices, e.rewriteNotice(r, opts.DryRun).Same); i >= 0 {
			res.Notices[i].Text += " — no file changes"
		}
	}
	// A changed active profile is a change the apply writes, even when it moves no module. So is a
	// pending project post-install script that runs alone.
	res.InSync = len(res.Steps) == 0 && len(res.Pins) == 0 && len(res.Removals) == 0 && len(pr.scripts.alone) == 0 &&
		!slices.ContainsFunc(copies, func(c moduleCopy) bool { return c.replace }) && (act == nil || !act.changed) && req.move == nil
	if len(res.Steps) > 0 { // the installed assistant's era matters to a sync that writes files
		res.Findings = append(res.Findings, probes.mismatched(sc)...)
	}
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
	// Nothing is written to the cache unless the run applies: every refusal and every No (the
	// trust question, the removals, the receipt, a hook's consent, a conflict) leaves it as it was.
	// A dry run caches what the user trusts at its pin, as -n always has (publishRun).
	if err := e.confirmTrusted(opts, res.Result); err != nil || opts.DryRun {
		if err != nil {
			return nil, err
		}
		if err := e.publishRun(sc, entries, opts); err != nil {
			return nil, err
		}
		// A dry run ends with the command that applies it: the user's own run without -n, and the
		// flags that answer its consent questions off a terminal: --trust for the kinds given and
		// the kinds the run would be refused without. -y only as given: off a terminal a run with
		// removals refuses, and its hint adds -y. A sync in sync applies a trust it needs too: its
		// project's scripts'.
		needTrust := res.Trust.Status != trust.StatusTrusted && res.Trust.Status != TrustStatusNotApplicable
		if !res.InSync || needTrust || res.needs&^opts.Trust != 0 {
			res.ToApply = e.commandOf(req)
			missing := res.needs
			if needTrust {
				missing |= res.Trust.kinds()
			}
			missing &^= opts.Trust
			res.EquivalentCommands = []string{res.ToApply}
			if missing != 0 {
				res.OffTerminal = e.commandOf(req.trusting(opts.Trust | missing))
				res.EquivalentCommands = []string{res.OffTerminal}
			}
		}
		if len(res.Failed) > 0 || len(res.Gone) > 0 {
			return res, &FetchesFailedError{Modules: res.Failed, Gone: res.Gone}
		}
		return res, nil
	}
	if err := e.publishRun(sc, entries, opts); err != nil {
		return nil, err
	}
	defer func() { err = res.afterGrants(err) }() // a failure from here on carries the grants written
	apply := func(mw []plan.ManifestWrite) error {
		return e.commit(ctx, pr, opts, res.Result, mw, func(*plan.Plan) error { return e.makePluginData(sc, changes) })
	}
	// The trust record is written once, after the apply: it records the entries the user accepted,
	// and the top-level targets when they were accepted, as lola saves them; every other entry
	// keeps its record, and so does every part of a project profile the run does not check.
	var trusted *trust.Record
	if acc != nil {
		trusted = &trust.Record{View: acc.record(rec.View, trustParts(man, layers, nil))}
	}
	pinned, profPins := pinsOf(entries, ""), profilePins(layers, entries)
	// A move's edits go in the same transaction: its project.yml change beside the pins (a pin of a
	// moved entry last, so it wins), and its profile files with the pins of each it edits.
	var moveFiles []fileEdit
	c := manifest.Change{Set: pinned}
	recFor := trusted
	if req.move != nil {
		profPins = req.move.pinned(profPins)
		moveFiles = req.move.fileEdits()
		c = manifest.Change{Set: append(slices.Clone(req.move.manifest.Set), pinned...), Remove: req.move.manifest.Remove}
		// lola's own writes into project files are trusted (moveEdit.trusted, above); a move of
		// files of yours alone leaves the record as it was.
		if recFor == nil && (len(req.move.manifest.Set)+len(req.move.manifest.Remove) > 0 ||
			slices.ContainsFunc(moveFiles, func(f fileEdit) bool { return f.layer.Key != "" })) {
			recFor = rec
		}
	}
	if e.creating != nil {
		moveFiles = append(moveFiles, e.creating.files...)
		if recFor == nil && slices.ContainsFunc(e.creating.files, func(f fileEdit) bool { return f.layer.Key != "" }) {
			recFor = rec
		}
	}
	if len(c.Set)+len(c.Remove) > 0 || len(profPins) > 0 || len(moveFiles) > 0 {
		var files []string
		if len(c.Set)+len(c.Remove) > 0 {
			files = append(files, e.manifestName(sc))
		}
		for _, p := range profPins {
			files = append(files, projectProfileFile(p.layer.Key))
		}
		for _, f := range moveFiles {
			if f.layer.Key != "" {
				files = append(files, projectProfileFile(f.layer.Key))
			} else {
				files = append(files, shown(f.layer.Path))
			}
		}
		err = e.saveManifest(sc, man, c, profPins, moveFiles, recFor, "the project's files, lock.json and "+andJoin(files)+" were updated",
			syncAgain(e.SyncCommand(sc)), apply)
	} else if err = apply(nil); err == nil && trusted != nil {
		if err = e.saveTrust(sc, trusted.View); err != nil {
			msg := "lola could not record that you trust " + acc.files(e.manifestName(sc))
			if !res.InSync {
				msg = "the project's files and lock.json were updated, but " + msg
			}
			err = errs.WithHints(fmt.Errorf("%s: %w", msg, err), syncAgain(e.SyncCommand(sc)))
		}
	}
	if err != nil {
		return nil, err
	}
	// The picks a move carried leave selections.yaml once the entries that hold them now are written.
	if req.move != nil && len(req.move.selections) > 0 {
		if err := config.Save(e.Paths.Selections, req.move.selections); err != nil {
			return nil, e.selectionsLeft(req.move.selections, req.move.written, err)
		}
	}
	if len(pinned) > 0 {
		res.Edits = append(res.Edits, editsOf(ctx, sc, baseTarget(layers[0]), pinned, nil, nil)...)
	}
	for _, p := range profPins {
		res.Edits = append(res.Edits, editsOf(ctx, sc, writeTo{Layer: p.layer, on: true, label: layerLabel(nil, p.layer.Name)}, p.pins, nil, nil)...)
	}
	res.Trust.Recorded = trusted != nil
	for i := range res.Pins {
		res.Pins[i].Written = true
	}
	if acc != nil {
		res.Equivalent = e.equivalent(req, acc, res)
	}
	if len(res.Failed) > 0 || len(res.Gone) > 0 {
		return res, &FetchesFailedError{Modules: res.Failed, Gone: res.Gone}
	}
	return res, nil
}

// equivalent is the command that gives a run's trust answer (acc) off a terminal: Trust all is the
// user's own run with --trust for the kinds given and the kinds of the changes it accepted; a
// partial answer names every entry it accepted, whose record changes even when nothing is rendered,
// and every module the run changed, so a run limited to them applies the same plan and records the
// same trust. -y is added when the run's removals were confirmed.
func (e *Engine) equivalent(req SyncRequest, acc *acceptance, res *SyncResult) string {
	run := req.trusting(req.Options.Trust | res.Trust.kinds())
	var yes []string
	if len(res.Removals) > 0 && !req.Options.Force {
		yes = append(yes, "-y")
	}
	if req.Profiles != nil || req.move != nil {
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
// manifest) with the new pin the sync writes for it. A profile of the user's own is never written:
// lock.json records the version its entry installs.
func pinsOf(entries []*syncEntry, part string) []manifest.Entry {
	var out []manifest.Entry
	for _, se := range entries {
		if se.pin != nil && !se.own() && se.part() == part {
			out = append(out, se.pinned())
		}
	}
	return out
}

// profilePins is the pins a sync writes into each project profile of layers, in layer order: one
// profileEdit per profile with an entry of the run that gets a pin.
func profilePins(layers []Layer, entries []*syncEntry) []profileEdit {
	var out []profileEdit
	for _, l := range layers {
		if l.Key == "" || l.read == nil { // a remote member of a project bundle is pinned in its own file
			continue
		}
		if pins := pinsOf(entries, string(profile.KindProject)+":"+l.Key); len(pins) > 0 {
			out = append(out, profileEdit{layer: l, pins: pins})
		}
	}
	return out
}

// trustPart is one file whose entries a project-scope run checks against the trust record:
// project.yml (key "") or a project profile of the run's layers (key its name, name its qualified
// name), with view the entries of it that win, as the run read them, or with the pins the run
// writes. shadowed names the modules the profile lists whose entry a later layer replaces: they are
// neither checked nor accepted until they win, so an entry is always reviewed with the content it
// installs.
type trustPart struct {
	key, name string
	view      trust.View
	shadowed  []string
}

// trustParts is the parts a run checks: project.yml's, every entry of it, those the active profile
// shadows too, as they apply once it is off; and each project profile's of layers, the active ones
// and those being turned on, each with its winning entries only; with the pins entries write when
// entries is not nil. Every other part of the record is carried, never checked.
func trustParts(man *manifest.Manifest, layers []Layer, entries []*syncEntry) []trustPart {
	out := []trustPart{{view: trust.ViewOf(man.Applied(manifest.Change{Set: pinsOf(entries, "")}))}}
	// A part may come from several layers: the remote members a project bundle names are its part.
	at := map[string]int{}
	files := map[string]*manifest.Manifest{}
	vias := map[string]map[string]*trust.MemberView{}
	for i, l := range layers {
		if l.Key == "" {
			continue
		}
		k, seen := at[l.Key]
		if !seen {
			out = append(out, trustPart{key: l.Key, name: string(profile.KindProject) + ":" + l.Key})
			k, at[l.Key], files[l.Key], vias[l.Key] = len(out)-1, len(out)-1, &manifest.Manifest{}, map[string]*trust.MemberView{}
		}
		m := files[l.Key]
		if l.read != nil {
			m.Targets = slices.Clone(l.read.Targets) // trust content, as project.yml's are
		}
		for _, ent := range l.Entries {
			later := slices.IndexFunc(layers[i+1:], func(later Layer) bool {
				return slices.ContainsFunc(later.Entries, func(o manifest.Entry) bool { return o.Name == ent.Name })
			})
			switch {
			case later >= 0 && layers[i+1+later].Key == l.Key: // a later layer of the part gives its entry
			case later >= 0:
				out[k].shadowed = append(out[k].shadowed, ent.Name)
			default:
				m.Modules = append(m.Modules, ent)
				vias[l.Key][ent.Name] = l.member
			}
		}
	}
	// A project bundle's member list is its part's trust content, whether or not a member gives the
	// part entries or a layer at all (Layer.expanded).
	bundles := map[string][]string{}
	for _, l := range layers {
		for _, b := range append(slices.Clone(l.expanded), l.chain...) {
			if b.key == "" {
				continue
			}
			bundles[b.key] = b.members
			if _, seen := at[b.key]; !seen {
				out = append(out, trustPart{key: b.key, name: string(profile.KindProject) + ":" + b.key})
				at[b.key], files[b.key] = len(out)-1, &manifest.Manifest{}
			}
		}
	}
	for k := range out[1:] {
		p := &out[k+1]
		p.view = trust.ViewOf(files[p.key].Applied(manifest.Change{Set: pinsOf(entries, p.name)}))
		p.view.Bundle = slices.Clone(bundles[p.key])
		// An entry from a remote profile that the project bundle names is recorded with it.
		for j := range p.view.Modules {
			p.view.Modules[j].Member = vias[p.key][p.view.Modules[j].Name]
		}
	}
	return out
}

// checkParts is trust.Check of each part against its part of rec, as one report: the top-level
// targets are project.yml's, a profile's are its ProfileTargets, each change of a profile's part
// names the profile, and the status is trusted only when every part is. A shadowed entry's record is left out of the check, so it
// is not reported removed.
func checkParts(rec *trust.Record, parts []trustPart) trust.Report {
	out := trust.Report{Status: trust.StatusTrusted, Changes: []trust.Change{}}
	for _, p := range parts {
		rv := rec.View.Part(p.key)
		rv.Modules = slices.DeleteFunc(rv.Modules, func(e trust.Entry) bool { return slices.Contains(p.shadowed, e.Name) })
		rep := trust.Check(&trust.Record{View: rv, Exists: rec.Exists}, p.view)
		// A project bundle's member list is a top-level change of its part, as a profile's targets
		// are; a bundle has no targets, so its part has at most one of them.
		top := cmp.Or(rep.Targets, rep.Bundle)
		switch {
		case p.key == "":
			out.Targets, out.TargetVersions = rep.Targets, rep.TargetVersions
		case top != nil:
			if out.ProfileTargets == nil {
				out.ProfileTargets = map[string]*trust.FieldChange{}
			}
			out.ProfileTargets[p.name] = top
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
	rewrites []Rewrite
}

// planSync plans each entry from the bytes that were hashed, its snapshot or staged tree: its
// intents for its effective targets and selections, the change that renders them over the targets
// lock.json has it on, and its module copy, replaced (running its hooks) when its version changes,
// the copy is gone or it adds a target, or to offer a pending post-install hook to a run that names
// its module (named) or gives --trust covering hooks; a pending project post-install script
// likewise, while project.yml still names one (postScript). layers are sc's, whose entries a
// re-pick hint's profile name must not read as, and on is whether a profile is on at sc once the
// run applies (repickRun).
func (e *Engine) planSync(sc Scope, entries []*syncEntry, layers []Layer, on bool, led *ledger.Ledger, templates *templateRun, sel *config.Selections, opts Options,
	probes *probeRun, named []string, postScript bool) (syncPlan, error) {
	sp := syncPlan{modules: []ModuleResult{}}
	opts.profiles = e.visibleOnce(sc)
	if _, err := e.scopeSection(sc); err != nil {
		return syncPlan{}, err
	}
	sp.notices = append(sp.notices, e.keyNotices(sc)...)
	for _, se := range entries {
		name := se.ent.Name
		ent := se.pinned()
		x, err := e.exclusions(sel, sc, se.from, name)
		if err != nil {
			return syncPlan{}, err
		}
		ignored, err := e.ignoredSection(sel, sc, layers, opts.profiles, on, se.from, name)
		if err != nil {
			return syncPlan{}, err
		}
		if ignored != nil {
			sp.notices = append(sp.notices, *ignored)
		}
		opts.entryFrom, opts.checkArg = se.from, e.syncCheckArg(sc, se, &ent, opts.DryRun)
		intents, finds, notes, mr, err := e.planModule(sc, se.v, se.targets, &ent, x, opts, probes, templates)
		if err != nil {
			return syncPlan{}, err
		}
		sp.modules, sp.findings, sp.notices = append(sp.modules, mr), append(sp.findings, finds...), append(sp.notices, notes...)
		installed := led.Modules[name]
		if installed != nil {
			for _, r := range e.eraRewrites(name, installed, mr.eras) {
				sp.rewrites, sp.notices = append(sp.rewrites, r), append(sp.notices, e.rewriteNotice(r, opts.DryRun))
			}
		}
		stale, err := e.staleNotices(sc, se.v.Module, x, func(args ...string) (string, error) {
			infos, err := opts.profiles()
			if err != nil {
				return "", err
			}
			return e.repickRun(sc, layers, infos, on, se.from, args[0], args[1:]...), nil
		})
		if err != nil {
			return syncPlan{}, err
		}
		sp.notices = append(sp.notices, stale...)
		// The re-pick names the provider (repickRun), so the profiles are listed only for a notice.
		adds, err := e.newComponentNotices(se.v, installed, x, ent.RulesOn(), func(origin string, args ...string) (string, string, error) {
			label, err := originLabel(x, origin, se.from, opts.profiles)
			if err != nil {
				return "", "", err
			}
			infos, err := opts.profiles()
			if err != nil {
				return "", "", err
			}
			return label, e.repickRun(sc, layers, infos, on, se.from, args[0], args[1:]...), nil
		})
		if err != nil {
			return syncPlan{}, err
		}
		sp.notices = append(sp.notices, adds...)
		ts := se.targets
		for _, t := range allTargets(installed, nil, &manifest.Manifest{}) {
			if !slices.Contains(ts, t) {
				ts = append(slices.Clone(ts), t)
			}
		}
		sp.changes = append(sp.changes, plan.ModuleChange{Name: name, Targets: ts, Intents: intents,
			Entry: &ledger.Entry{From: se.from, Source: ent.Source, Ref: ent.Ref, Path: ent.Path, Components: ent.Components.Clone(), Commit: se.v.Commit, Digest: se.v.Meta.Digest,
				Targets: mr.Targets, RenderedWith: mr.renderedWith, Eras: mr.eras}})
		replace, err := needsCopy(e.files(sc).modules, name, installed, se.v.Meta.Digest)
		if err != nil {
			return syncPlan{}, err
		}
		// A target the module is not installed on has never had its hooks run: the copy is
		// replaced to run them, as for a new version.
		replace = replace || addsTargets(installed, mr.Targets)
		// A pending post-install hook runs only in a copy replaced from the hashed version, after
		// the consent keyed on those bytes, and is offered only to a run that names its module or
		// gives --trust covering hooks: any other run keeps it pending, asking nothing, and says
		// how to run it.
		pending := installed != nil && installed.HooksPending && declaresPostInstall(se.v.Module.DeclaredHooks)
		if pending && !replace && !opts.Trust.Covers(TrustHooks) && !slices.Contains(named, name) {
			text := "pending post-install hook for " + name + " → " + e.SyncCommand(sc, name)
			if opts.Ask == nil {
				text += " (off a terminal: " + e.SyncCommand(sc, name, TrustHooks.Arg()) + ")"
			}
			sp.notices = append(sp.notices, Notice{Level: LevelWarn, Text: text})
			pending = false
		}
		// A pending project post-install script runs alone in the copy as it is, only with consent
		// for its current digest, and is offered only to a run that names its module or gives
		// --trust covering scripts; any other run keeps it pending and says how to run it.
		offer := installed != nil && installed.ScriptsPending && postScript && !replace && !pending && sc.Kind == target.Project
		noticed := offer && !opts.Trust.Covers(TrustScripts) && !slices.Contains(named, name)
		if noticed {
			sp.notices = append(sp.notices, e.pendingScriptNotice(sc, name, opts))
			offer = false
		}
		shows := se.v
		sp.copies = append(sp.copies, moduleCopy{name: name, src: se.v.ContentDir, replace: replace || pending,
			hooks: se.v.Module.DeclaredHooks, targets: mr.Targets, runHooks: replace || pending, forced: pending && !replace,
			pending: pending, pendingScript: offer, scriptNotice: noticed, shows: &shows, fromSource: e.reviewSource(se, mr.Targets),
			again: []string{"sync", name}})
	}
	return sp, nil
}

// acceptance is the user's trust answer applied to a run: the entries accepted, by their names in
// the record and the file, per part of the record (the key of a trustPart: "" for project.yml, a
// project profile's name); whether the top-level targets are; and the modules that leave the run as
// installed, by the names the report shows.
type acceptance struct {
	all            bool
	modules        map[string][]string
	targets        bool
	profileTargets map[string]bool // by part key: whether that project profile's top-level targets are accepted
	leave          []string
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
	for _, k := range slices.Sorted(maps.Keys(a.profileTargets)) {
		if f := projectProfileFile(k); a.profileTargets[k] && !slices.Contains(out, f) {
			out = append(out, f)
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
		targets := p.key == "" && a.targets || p.key != "" && a.profileTargets[p.key]
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
	acc := &acceptance{all: ans.All, modules: map[string][]string{}, targets: rep.topChanged() && (ans.All || ans.Kinds.Covers(TrustEntries)),
		profileTargets: map[string]bool{}}
	// A project profile's top-level targets are accepted as project.yml's are: with the answer
	// that covers its kind, or with an accepted entry that installs to them.
	for _, pt := range rep.ProfileTargets {
		acc.profileTargets[partKey(pt.Profile)] = ans.All || ans.Kinds.Covers(TrustProfiles)
	}
	for _, c := range rep.Changes {
		if !ans.All && !slices.Contains(ans.Modules, c.Module) && !ans.Kinds.Covers(c.kind()) {
			if !slices.Contains(acc.leave, c.Module) {
				acc.leave = append(acc.leave, c.Module)
			}
			continue
		}
		key := partKey(c.Profile)
		acc.modules[key] = append(acc.modules[key], names[c.Module])
		se := entryOf(entries, c)
		if rep.topChanged() && c.Kind != trust.ChangeRemoved && se != nil && len(se.ent.Targets) == 0 {
			acc.targets = true
		}
		if _, changed := acc.profileTargets[key]; changed && c.Kind != trust.ChangeRemoved && se != nil && se.inherits {
			acc.profileTargets[key] = true
		}
	}
	// A replacement comes only with an activation, whose answer is all or none.
	replaced := len(rep.Replacements) > 0 && (ans.All || ans.Kinds.Covers(TrustProfiles))
	if len(acc.modules) == 0 && !acc.targets && !slices.Contains(slices.Collect(maps.Values(acc.profileTargets)), true) && !replaced {
		return nil, ErrDeclined
	}
	leave := func(se *syncEntry) {
		if !slices.Contains(acc.leave, shown(se.ent.Name)) {
			acc.leave = append(acc.leave, shown(se.ent.Name))
		}
	}
	for _, se := range entries {
		if rep.topChanged() && !acc.targets && len(se.ent.Targets) == 0 {
			leave(se)
		}
		if accepted, changed := acc.profileTargets[se.key]; changed && !accepted && se.inherits {
			leave(se)
		}
		// An entry a project bundle brings waits on the bundle's member list as one that inherits
		// targets waits on them.
		if slices.ContainsFunc(se.bundles, func(key string) bool { accepted, changed := acc.profileTargets[key]; return changed && !accepted }) {
			leave(se)
		}
	}
	slices.Sort(acc.leave)
	return acc, nil
}

// partKey is the key of the trust part a TrustChange.Profile names: "" for project.yml, else the
// project profile's name.
func partKey(part string) string {
	return strings.TrimPrefix(part, string(profile.KindProject)+":")
}

// RemovalsDeclinedError is a sync whose removals the user declined: nothing was applied. Recorded
// says the trust the user accepted changed the record, so the next sync does not ask about it
// again; Again names the accepted entries it does ask about again, whose pin or folder content the
// record holds only once lola writes it; Trusted names the modules whose trust the user accepted,
// which lola sync <those> installs without removing anything. Retry is the run to repeat: lola
// sync, or the activation as lola profile, with the scope's flags.
type RemovalsDeclinedError struct {
	Recorded       bool
	Trusted, Again []string
	Retry          string
}

func (e *RemovalsDeclinedError) Error() string {
	return "removals declined; the project was not changed"
}
func (e *RemovalsDeclinedError) Unwrap() error { return ErrDeclined }

// declineRemovals is a No to a run's removals: nothing is applied, but the trust the user accepted
// (acc, nil when nothing was asked) is recorded, without the pins lola did not write: the answer
// stands, and the next sync asks again only about the removals and the accepted entries (of
// entries, the run's) whose new pin or folder content it did not install. A move's answer is not
// recorded: its record covers the files it did not write.
func (e *Engine) declineRemovals(req SyncRequest, man *manifest.Manifest, layers []Layer, led *ledger.Ledger, rec *trust.Record, acc *acceptance,
	entries []*syncEntry) error {
	sc := req.Scope
	// A move writes its files and the trust of its answer together, or neither.
	if acc == nil || req.move != nil {
		return &RemovalsDeclinedError{Retry: e.commandOf(req)}
	}
	view := acc.record(rec.View, trustParts(man, layers, nil))
	if err := e.saveTrust(sc, view); err != nil {
		return errs.WithHints(fmt.Errorf("lola could not record that you trust %s: %w", acc.files(e.manifestName(sc)), err),
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
	d := &RemovalsDeclinedError{Recorded: !rec.Exists || now != was, Retry: e.commandOf(req)}
	for _, se := range entries {
		installed := led.Modules[se.ent.Name]
		folder := se.spec.Kind == source.KindFolder && (installed == nil || installed.Digest != se.v.Meta.Digest)
		if !slices.Contains(acc.modules[se.key], se.ent.Name) {
			continue
		}
		if name := shown(se.ent.Name); !slices.Contains(d.Trusted, name) {
			d.Trusted = append(d.Trusted, name)
		}
		if se.pin != nil || folder {
			d.Again = append(d.Again, shown(se.ent.Name))
		}
	}
	return d
}

// trustReport is the report of a trust check, every manifest value through untrusted.Text.
func trustReport(rep trust.Report) TrustReport {
	out := TrustReport{Status: rep.Status, Changes: []TrustChange{}, Replacements: []Replacement{}}
	if rep.Targets != nil {
		out.Targets = &trust.FieldChange{Field: rep.Targets.Field, From: shownValue(rep.Targets.From), To: shownValue(rep.Targets.To)}
	}
	if rep.TargetVersions != nil {
		out.TargetVersions = &trust.FieldChange{Field: rep.TargetVersions.Field, From: shownValue(rep.TargetVersions.From), To: shownValue(rep.TargetVersions.To)}
	}
	for _, name := range slices.Sorted(maps.Keys(rep.ProfileTargets)) {
		t := rep.ProfileTargets[name]
		out.ProfileTargets = append(out.ProfileTargets, ProfileTargets{Profile: shown(name),
			FieldChange: trust.FieldChange{Field: t.Field, From: shownValue(t.From), To: shownValue(t.To)}})
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
// archive entry without a digest, which installs what its ref serves when it is resolved, even if
// the report shows the pin the sync is about to write.
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

// pinReasons gives each change of an entry the run writes a pin for its reason: re-pin from its
// ref, naming flag (SyncRequest.updateFlag), or pin what its ref serves now; an entry whose
// manifest entry is as trusted gets a change of its own, so the diff names it. Before an entry is
// resolved its pin is not known, which the reason says. A folder whose content changed is given its
// reason too.
func pinReasons(rep *TrustReport, entries []*syncEntry, flag string) {
	for _, se := range entries {
		var reason string
		fetched := se.v.ContentDir != ""
		switch {
		case se.folderChanged && se.notInstalled:
			reason = "not installed yet"
		case se.folderChanged:
			reason = "content changed since lola installed it"
		case !needsPin(se):
			continue
		case se.update:
			var notes []string
			if flag != "" {
				notes = append(notes, flag)
			}
			if !fetched && !se.fresh { // a fresh head is known without a fetch
				notes = append(notes, "not fetched yet")
			}
			reason = "re-pin from " + served(se)
			if len(notes) > 0 {
				reason += " (" + strings.Join(notes, "; ") + ")"
			}
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
	case map[string]string:
		out := make(map[string]string, len(v))
		for k, s := range v {
			out[shown(k)] = shown(s)
		}
		return out
	}
	return v
}

// syncRefusal refuses a sync that needs answers it cannot ask for, naming in one error every answer
// missing and every cause: the user's trust (trustMissing) for the manifest, the pins the sync
// would write and the folders whose content changed, and the confirmation of removals (those that
// need it, with the files a version read no longer ships), the answers joined by "; and" so a list
// of names never runs into the next answer. The hints repeat the user's run (req) at its scope with
// the flags that answer, so the user is never sent from one refusal to the next. The error carries
// the report and the removals for the caller to show.
func (e *Engine) syncRefusal(sc Scope, req SyncRequest, rep TrustReport, removals []Removal, missing TrustKinds,
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
		for _, pt := range rep.ProfileTargets {
			if file := projectProfileFile(partKey(pt.Profile)); !slices.Contains(files, file) {
				files = append(files, file)
			}
		}
		if (rep.Targets != nil || rep.TargetVersions != nil) && !slices.Contains(files, name) {
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
	// The removals the run is refused for are named by module; the files a version read for the
	// refusal no longer ships (droppedFiles, Why whyDropped) as that version's.
	var names, drops []string
	for _, r := range removals {
		switch {
		case r.Why != whyDropped:
			names = append(names, r.Module)
		case !slices.Contains(drops, r.Module+"'s"):
			drops = append(drops, r.Module+"'s")
		}
	}
	if len(names) > 0 {
		answers = append(answers, "your confirmation to remove "+strings.Join(names, ", "))
	}
	if len(drops) > 0 {
		answers = append(answers, "your confirmation to remove the files "+andJoin(drops)+" new version drops")
	}
	// The hint carries -y only when every removal it would confirm is listed here, the user's own
	// -y included: while an installed entry's new version is unread (a pulled change is never
	// fetched before trust, and a source may fail to serve one), the run it hints may remove other
	// files, so it carries no -y and that run lists them all and asks.
	var unread []string
	for _, se := range entries {
		if se.installed != "" && se.v.ContentDir == "" && (needsPin(se) || se.ent.Digest != se.installed) {
			unread = append(unread, shown(se.ent.Name))
		}
	}
	yes := len(unread) == 0 && (yesGiven || len(removals) > 0)
	// Each hint repeats the run in its output format, with --trust as given; the one that accepts
	// the changes adds exactly the kinds missing.
	again := func(r SyncRequest, extra ...string) string {
		return e.commandOf(r, append(slices.Clone(req.Display), extra...)...)
	}
	hints = append(hints, "see them: "+again(req, "-n"))
	accepting := req.trusting(req.Options.Trust | missing)
	accepting.Yes = accepting.Yes && yes
	accepting.At = shownVersions(req, entries)
	switch {
	case trustMissing && yes:
		hints = append(hints, "then accept them: "+again(accepting, "-y"))
	case trustMissing:
		hints = append(hints, "then accept them: "+again(accepting))
	case yes:
		hints = append(hints, "then confirm them: "+again(accepting, "-y"))
	default:
		hints = append(hints, "then fetch and list them all: "+again(accepting))
	}
	for _, se := range entries {
		// An install of the folder would take all of it, never the entry's selection.
		if trustMissing && se.folderChanged && se.spec.Components == nil {
			hints = append(hints, "edited it yourself? "+e.installFolder(sc, se.spec.Location, req.Display...))
		}
	}
	run := "lola sync"
	switch {
	case req.Installed:
		run = "lola update"
	case req.Profiles != nil || req.move != nil || req.profileArgs != nil:
		run = "lola profile"
	}
	msg := run + " needs " + strings.Join(answers, "; and ") + ", and there is no terminal to ask on"
	if trustMissing && yesGiven {
		msg += "; -y confirms removals only; accept them with " + missing.Arg()
	}
	switch {
	case len(unread) > 0 && len(removals) > 0:
		msg += "; until it reads " + andJoin(unread) + ", the removals listed may change, so -y waits until the next run lists them"
	case len(unread) > 0 && yesGiven:
		msg += "; until it reads " + andJoin(unread) + ", it cannot list what it removes, so -y waits until the next run lists it"
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

// syncRemovals is what the run removes, from lock.json and the layers alone: the targets each entry
// of the run no longer has, then each installed module of the run no layer wants (want), whole.
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

// containLocalSources refuses an entry whose local folder or archive resolves outside the project:
// a pulled project.yml, or a pulled link, must not make lola read the user's other files. It checks
// every entry whose Source and Path the trust record does not hold, and every one whose source as
// written (a relative one joined to the project) names a path inside the project, which a pulled
// link can lead out of; a held source outside the project is one the user installed or trusted.
// Locations are canonical (source.Parse); a root or location that cannot be resolved is refused,
// never let through. display is the run's output flags, which the install it hints keeps.
func (e *Engine) containLocalSources(sc Scope, entries []*syncEntry, display []string) error {
	var root string
	for _, se := range entries {
		local := source.IsLocal(se.spec.Kind, se.spec.Location)
		// A folder of the user's own profile is theirs; a project profile's is checked as
		// project.yml's are. An entry whose location the record trusts is not checked again unless
		// it is written inside the project, where a pulled link could lead it out.
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
			// that is gone, which the install replaces: the install works as hinted.
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

// folderEdits gives each folder whose content changed the diff's lines: how to review the edits
// when the folder is in a git work tree, and how to record the content as the user's own when they
// made the edits.
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

// needsPin reports whether a sync writes a pin for the entry into its file: a git or archive entry
// of the manifest or of a project profile under --update or without one; a folder is never pinned,
// and a profile of the user's own never written. Each needs the user's trust at project scope. Once
// the entry is resolved, an --update that found the version it pins writes none.
func needsPin(se *syncEntry) bool {
	return !se.own() && se.spec.Kind != source.KindFolder && (se.update || se.ent.Digest == "") && (se.v.ContentDir == "" || se.pin != nil)
}

// SyncNames is the module names lola sync takes in sc, for completion: every module the scope's
// layers want, installed or not, and every module installed there, in name order. It reads the
// manifest, lock.json and the user's own active profile, under the profiles lock taken shared as a
// dry run takes it (none before any lola made the lock file), and no other lock; it runs no git, so
// it does not check that lock.json is untracked, which every command it completes does. So it never
// reads a project profile: a committed lock.json could name one, and completion would parse
// checked-in files no command has checked. The modules of an active project profile that are not
// installed are left out.
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
	if own.Profile != nil && strings.HasPrefix(own.Profile.Name, string(profile.KindProject)+":") {
		own.Profile = nil
	}
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
// a layer wants (want), no active profile and no module a profile installed (led), at user scope no
// module at all, whose leftovers from an earlier build's user.yml a sync removes. There is nothing
// it could make the scope match. A scope with no manifest that a profile wants modules in, or once
// did, syncs as an empty manifest, so the modules of a profile that is gone are removed; an
// existing manifest with no modules means "remove everything".
func (e *Engine) refuseMissingManifest(sc Scope, want []Wanted, led *ledger.Ledger) error {
	if len(want) > 0 || led.Profile != nil || slices.ContainsFunc(sortedNames(led), func(n string) bool {
		return led.Modules[n].From != "" || sc.Kind == target.User
	}) {
		return nil
	}
	if sc.Kind == target.User {
		path := e.defaultPath()
		if _, err := os.Lstat(path); err == nil || !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return errs.WithHints(fmt.Errorf("no default.yml at %s", untrusted.Text(path, untrusted.MaxName)),
			"install a module at user scope first: lola install --user <source>")
	}
	path := e.files(sc).manifest
	_, err := os.Lstat(path)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return errs.WithHints(fmt.Errorf("no project.yml in %s", untrusted.Text(sc.ProjectDir, untrusted.MaxName)),
		"start one: "+e.Command(sc, "install")+" <source>", "or sync another project: lola sync -C <dir>")
}

// syncLimit is the set of modules a sync acts on: the names given, or every module when none was
// and the run is not only those names. An activation's run is only the modules it changes, which
// may be none.
type syncLimit struct {
	names []string
	only  bool
	// files says the run writes profile files alone and applies nothing (SyncRequest.writeOnly): no
	// part of the stack is judged, so a pulled change of the bundle on waits for the run that
	// applies it.
	files bool
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
			// The base is named apart from the active profile's layers.
			var base string
			var profiles []string
			for _, ly := range layers {
				if ly.Base {
					base = ly.Name
				} else {
					profiles = append(profiles, ly.Name)
				}
			}
			if len(profiles) > 0 {
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

// runReport is rep limited to the run: the changes of the modules it names, and the top-level
// targets only when an entry of the run has no targets of its own, since it installs to them. An
// unlimited run's report is rep.
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
		out.Targets, out.TargetVersions = rep.Targets, rep.TargetVersions
	}
	for name, t := range rep.ProfileTargets {
		// A project bundle's member list (Field bundle) decides which profiles the scope loads: it
		// is in every run's report that applies the scope, an activation's that loads nothing
		// included; a run that only writes files applies nothing it could decide.
		if (t.Field == "bundle" && !limit.files) || slices.ContainsFunc(entries, func(se *syncEntry) bool { return se.inherits && se.part() == shown(name) }) {
			if out.ProfileTargets == nil {
				out.ProfileTargets = map[string]*trust.FieldChange{}
			}
			out.ProfileTargets[name] = t
		}
	}
	if out.Targets == nil && out.TargetVersions == nil && len(out.ProfileTargets) == 0 && len(out.Changes) == 0 {
		out.Status = trust.StatusTrusted
	}
	return out
}

// withoutTargets is rep without its top-level targets' and target_versions' changes, trusted when
// nothing else changed.
func withoutTargets(rep trust.Report) trust.Report {
	rep.Targets, rep.TargetVersions = nil, nil
	if len(rep.ProfileTargets) == 0 && len(rep.Changes) == 0 {
		rep.Status = trust.StatusTrusted
	}
	return rep
}

// outOfSync is the notice of a limited run about the modules it left out that are not in sync, by a
// local check of the manifest against lock.json: an entry not installed or pinned to another
// version than the one installed, and a module installed but no longer listed. ok is false for an
// unlimited run and when there are none.
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

// validateSync checks every entry of the run before anything is fetched or asked, stopping at the
// first failure: the source parses and passes source.Validate with the entry's ref and path; the
// pin fits the source's kind; the cache holds no other source under the name; the effective targets
// are known: the entry's, else (a profile's entry has its profile's already) the manifest's. Errors
// name the file the entry is in.
//
// lola never writes pins into a profile of the user's own: its unpinned git or archive entry takes
// the pin pins.yaml records for its source, ref and path (pins, read at user scope, nil elsewhere),
// else the one lock.json records, as a manifest entry takes the one sync wrote into it, until
// --update resolves its ref again. A project profile's entry is checked, pinned and re-pinned as a
// manifest entry is. Which layer won before (from) does not matter: a planted lock.json sets it
// too, so resolveSync checks reachability instead. --update naming a profile's pinned entry is
// refused, since its pin is in a file lola does not write pins into; an --update that names no
// module leaves those entries at their pins, with a notice each (kept).
//
// Under lola update (req.Installed) such an entry is left as installed with that notice, never
// refused; an entry whose targets are not the ones installed is left as installed with a finding
// appended to leftOut; and a git or archive entry that does not follow its catalog is marked fresh
// when its cache head was fetched within age.
func (e *Engine) validateSync(sc Scope, man *manifest.Manifest, want []Wanted, led *ledger.Ledger, req SyncRequest,
	limit syncLimit, catalogs *syncCatalogs, age config.Age, leftOut *[]target.Finding, pins *config.Pins) (_ []*syncEntry, kept []Notice, _ error) {
	var out []*syncEntry
	for _, w := range want {
		ent, name, update, fromPins, seeded := w.Entry, w.Name, req.Update, false, false
		ps := pinSource{pins: pins, seed: req.seed, update: req.Update}
		if !limit.has(name) {
			continue
		}
		base, from, file := filepath.Base(w.path), "", ""
		if w.key != "" {
			// A project profile's entry is checked, pinned and re-pinned as project.yml's.
			base, from, file = projectProfileFile(w.key), w.From, w.path
		}
		if w.own() {
			base, from, file = shown(w.path), w.From, w.path
		}
		// The pin of the user's own profile, or of a remote profile a project bundle names, is in a
		// file lola sync never writes pins into.
		if w.own() || w.member != nil {
			pinFile, editPin := e.ownPin(sc, w.From, w.path)
			switch in := led.Modules[name]; {
			case ent.Digest != "" && update && !limit.all():
				// --update of a remote profile applies what it moves itself; a hand edit needs a sync.
				never, then := "lola never writes pins into", editPin+", then: "+e.SyncCommand(sc, name)
				if remoteLayer(w.From) {
					never, then = "only lola profile --update changes", editPin
				}
				if req.Installed { // lola update leaves it as installed
					kept = append(kept, Notice{Level: LevelInfo, Text: shown(name) + ": its pin is in " + pinFile + "; " + then})
					continue
				}
				return nil, nil, errs.WithHints(fmt.Errorf("%s: its pin is in %s, which %s", name, pinFile, never), then)
			case ent.Digest != "" && update:
				update = false
				kept = append(kept, Notice{Level: LevelInfo, Text: name + ": --update leaves the pin " + pinFile + " sets; " + editPin})
			case w.own() && ent.Digest == "" && !update && !source.IsFolderForm(ent.Source):
				// At user scope pins.yaml first for a profile of yours, then lock.json: a missing
				// pins.yaml never causes an unasked upgrade, and a newer synced pin is a change. A
				// move between scopes keeps the version installed: its seed comes before pins.yaml,
				// whose pin the next plain sync applies.
				if r, pinned, seed, ok := ps.ownPin(w, in); ok {
					ent, fromPins, seeded = r, pinned, seed
				}
			}
		}
		spec, ok, err := source.Parse(ent.Source, sourceRoot(sc))
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", name, err)
		}
		in, goneFolder := led.Modules[name], false
		if !ok && in != nil && in.Digest != "" && in.Source == ent.Source && source.IsFolderForm(ent.Source) {
			// A folder installed from this very path that is gone now: sync renders the cached
			// version installed, with a warning.
			loc := ent.Source
			if !filepath.IsAbs(loc) {
				loc = filepath.Join(sourceRoot(sc), loc)
			}
			spec, ok, goneFolder = source.Spec{Kind: source.KindFolder, Location: filepath.Clean(loc)}, true, true
		}
		if !ok {
			return nil, nil, fmt.Errorf("%s: %s is not a source; sources are %s", name, untrusted.Text(ent.Source, untrusted.MaxName),
				source.AcceptedForms)
		}
		// A remote profile's entry names a remote source: Parse never looks a URL or scp form up on
		// disk, and a path that still reads as local, a Windows drive, is refused.
		if remoteLayer(w.From) && source.IsLocal(spec.Kind, spec.Location) {
			return nil, nil, fmt.Errorf("%s: %s is on this machine; a remote profile names only git repositories and https archives", name,
				untrusted.Text(ent.Source, untrusted.MaxName))
		}
		spec.Ref, spec.Path, spec.Components = ent.Ref, ent.Path, ent.Components.Clone()
		if err := source.Validate(spec); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", name, err)
		}
		repin := e.SyncCommand(sc, "--update", name)
		if (w.own() || w.member != nil) && w.Digest != "" {
			_, repin = e.ownPin(sc, w.From, w.path)
		}
		if err := checkPinShape(name, base, spec.Kind, ent, repin); err != nil {
			return nil, nil, err
		}
		gone, err := e.goneOrigin(name, spec)
		if err != nil {
			return nil, nil, err
		}
		if goneFolder {
			gone = "" // nothing is cached from the gone folder: the cached version is what is installed
		}
		ts := ent.Targets
		if len(ts) == 0 {
			ts = man.Targets
		}
		if len(ts) == 0 {
			return nil, nil, errs.WithHints(fmt.Errorf("%s: %s gives it no targets", name, base),
				"list the assistants in "+base+", for example targets: [claude-code]")
		}
		// The ids are the entry's (or its profile's targets:, which it inherits) when it has any,
		// else the base's.
		tsIn := targetsIn{shown: filepath.Base(e.basePath(sc)), path: e.basePath(sc)}
		if len(ent.Targets) > 0 {
			tsIn = targetsIn{shown: base, path: w.path, name: name}
		}
		for _, id := range ts {
			if _, err := e.target(id); err != nil {
				// Where the file does not write them, no line is named.
				at, _ := targetsOrigin(tsIn.shown, tsIn.path, tsIn.name)
				return nil, nil, fmt.Errorf("%s%s: %w", at, name, err)
			}
		}
		// An entry installed through a marketplace from a source its catalog named, not the
		// marketplace's own, keeps the redirect rule of such a source in every fetch: the flag only
		// adds refusals, so a forged market cannot loosen one.
		if ent.Market != nil && !source.SameLocation(spec.Kind, ent.Market.Source, spec.Location) {
			spec.FromCatalog = true
		}
		// An --update of an entry installed through a marketplace follows its catalog only within
		// the same source, path and ref: the catalog's archive sum and redirect rule then apply to
		// the fetch.
		var follow *marketPick
		if update && ent.Market != nil && catalogs != nil {
			var n *Notice
			if follow, n = e.follow(sc, catalogs, ent, spec, req); n != nil {
				kept = append(kept, *n)
			}
			if follow != nil {
				listed := follow.entry.Spec()
				spec.SHA256, spec.FromCatalog = listed.SHA256, spec.FromCatalog || listed.FromCatalog
			}
		}
		se := &syncEntry{ent: ent, from: from, file: file, key: w.key, inherits: w.inherits, bundles: w.bundles, member: w.member, ledgerPin: w.own() && w.Digest == "" && ent.Digest != "" && !fromPins, pinsFile: fromPins, seeded: seeded, goneFrom: gone,
			spec: spec, targets: ts, targetsIn: tsIn, update: update, follow: follow}
		if in != nil {
			se.installed = in.Digest
		}
		if req.Installed {
			// lola update changes no module's targets: that is sync's.
			if installed := allTargets(led.Modules[name], nil, &manifest.Manifest{}); !slices.Equal(slices.Sorted(slices.Values(ts)), installed) {
				*leftOut = append(*leftOut, target.Finding{Severity: target.SeverityWarn, Module: untrusted.Text(name, untrusted.MaxName), Kind: "module",
					Code: CodeTargetsDiffer, Text: "its entry asks for other assistants than are installed; it stays as installed",
					Hint: "apply the change: " + e.SyncCommand(sc, append([]string{name}, req.Display...)...)})
				continue
			}
			// A fresh head serves the ref; an entry that follows its catalog fetches through it,
			// and one whose cached local source is gone fetches the new one, which replaces it. A
			// git head from before commits were recorded is fetched again (resolveHead).
			if update && follow == nil && gone == "" && spec.Kind != source.KindFolder {
				head, ok, err := e.Store.HeadFor(name, ent.Ref, sourceKey(spec))
				if err != nil {
					return nil, nil, err
				}
				se.fresh = ok && fresh(head.Fetched, e.Now(), age) && (spec.Kind != source.KindGit || head.Commit != "")
				// A fresh head that is the pin moves nothing: the entry is synced at its pin, with
				// no re-pin and so no trust to ask for.
				if se.fresh && ent.Digest != "" && head.Meta.Digest == ent.Digest && head.Commit == ent.Commit {
					se.update = false
				}
			}
		}
		out = append(out, se)
	}
	return out, kept, nil
}

// targetsOrigin is "<base>:<line>: " for where the targets that module name installs to are
// written in the file at path, base being how the refusal shows that file: the module's entry when
// it has a targets: key, else the file's top-level targets: key (name "" looks at that alone). ok
// is false when the file does not write them, or no longer holds them.
func targetsOrigin(base, path, name string) (prefix string, ok bool) {
	d, _, err := yamldoc.Read(path, manifest.Version)
	if err != nil {
		return "", false
	}
	root := yamldoc.Root(d)
	if root == nil {
		return "", false
	}
	if list := yamldoc.Value(root, "modules"); list != nil && name != "" {
		for _, n := range list.Content {
			if v := yamldoc.Value(n, "name"); v != nil && v.Value == name && yamldoc.Value(n, "targets") != nil {
				return base + ":" + strconv.Itoa(n.Line) + ": ", true
			}
		}
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "targets" {
			return base + ":" + strconv.Itoa(root.Content[i].Line) + ": ", true
		}
	}
	return "", false
}

// targetsIn is the file an entry's targets are written in, as targetsOrigin takes it: how a
// message shows the file, its path, and the module whose entry may write them (name "" for the
// file's top-level targets: alone).
type targetsIn struct{ shown, path, name string }

// where is "<file>:<line>" of the targets: key, or the file alone when it no longer writes them.
func (t targetsIn) where() string {
	if at, ok := targetsOrigin(t.shown, t.path, t.name); ok {
		return strings.TrimSuffix(at, ": ")
	}
	return t.shown
}

// sameFileHints say, for each conflict of two targets whose destinations are one real file (a
// .claude/skills/<n> linked to .agents/skills/<n>), which file's targets: to remove one of them
// from: a sync has no -t to hint a run with.
func (e *Engine) sameFileHints(ce *plan.ConflictError, entries []*syncEntry) []string {
	var out []string
	for _, c := range ce.Conflicts {
		k := slices.IndexFunc(entries, func(se *syncEntry) bool { return se.ent.Name == c.Claimant })
		if c.SameAs == "" || c.SameAs == c.Target || k < 0 {
			continue
		}
		var pair []string
		for _, t := range e.Targets {
			if t.ID() == c.SameAs || t.ID() == c.Target {
				pair = append(pair, t.ID())
			}
		}
		h := "remove " + pair[0] + " or " + pair[1] + " from targets: in " + entries[k].targetsIn.where()
		if !slices.Contains(out, h) {
			out = append(out, h)
		}
	}
	return out
}

// checkPinShape refuses a ref or a pin the entry's kind of source cannot have: a ref on a folder or
// archive, a pin on a folder, a commit on an archive, a git digest without its commit. update is
// the sync command that re-pins the entry.
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

// goneOrigin describes a local source the cache records for name that no longer exists, when spec
// is a local source the cache does not hold for the name yet: the version the run publishes drops
// it (store.Staged.ReplaceGoneOrigin). It is "" when there is none. The cache keeps an origin per
// source, so any other source of the name is no obstacle.
func (e *Engine) goneOrigin(name string, spec source.Spec) (string, error) {
	origins, err := e.Store.Origins(name) // an index lola cannot read is refused here, before any fetch
	if err != nil || !source.IsLocal(spec.Kind, spec.Location) {
		return "", err
	}
	if slices.ContainsFunc(origins, func(o store.Origin) bool { return o.From(sourceKey(spec)) }) {
		return "", nil
	}
	if i := slices.IndexFunc(origins, store.Origin.LocalGone); i >= 0 {
		return describeSource(origins[i].Source, origins[i].Path, origins[i].Components), nil
	}
	return "", nil
}

// sourceKey is the source spec names, as the cache keys its origins.
func sourceKey(spec source.Spec) store.SourceKey {
	return store.SourceKey{Type: string(spec.Kind), Location: spec.Location, Path: spec.Path, Components: spec.Components}
}

// installedWayOut gives a refusal that is one installed module's own state a way out, keeping
// the run's output flags. The run stays refused. A cache record (index.yml) that does not decode
// is the cache's copy alone: the hint removes it from the cache and repeats req (a dry run as
// one), which fetches the module again. Content that no longer loads or now declares another
// module is the module itself: the hint is the uninstall that removes it. A failure of lola's
// own state, such as a cache it cannot read, and an entry lola has not installed get none.
func (e *Engine) installedWayOut(sc Scope, led *ledger.Ledger, err error, req SyncRequest) error {
	if ie, ok := errors.AsType[*store.IndexError](err); ok && led.Modules[ie.Name] != nil {
		rm := errs.ShellJoin(append([]string{"lola", "cache", "rm", untrusted.Text(ie.Name, untrusted.MaxName)}, req.Display...))
		return errs.WithHints(err, "remove it from your cache: "+rm+" (add -y off a terminal)", "then: "+e.commandOf(req, req.again()...))
	}
	var name string
	if oe, ok := errors.AsType[*store.OtherNameError](err); ok {
		name = oe.Want
	} else if le, ok := errors.AsType[*store.LoadError](err); ok {
		name = le.Name
	}
	if led.Modules[name] == nil {
		return err
	}
	return errs.WithHints(err, "remove it: "+e.Command(sc, "uninstall", append([]string{name}, req.Display...)...))
}

// goneNotices are the notices of the entries whose cached local source no longer exists, which the
// run caches from the entry's source instead.
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

// resolveSync resolves an entry to the version it installs, from a snapshot of the cache or a fetch
// staged in run: a folder is read every run; --update fetches the ref; an unpinned entry takes the
// cache head for its ref, or fetches the ref when there is none (or the head predates commits in
// heads); a pinned entry takes the cached version, or fetches its commit. A new pin is recorded in
// se.pin. display is the run's output flags, which the hints keep.
func (e *Engine) resolveSync(ctx context.Context, sc Scope, se *syncEntry, run string, display []string) error {
	err := e.resolveVersion(ctx, sc, se, run, display)
	if err != nil && se.at != "" && sourceFailure(err) {
		// A version --at names that the source does not serve is that version's refusal, never a
		// failed fetch that lola update leaves as installed.
		return errs.WithHints(fmt.Errorf("%s: its source does not serve %s, which --at names: %s", shown(se.ent.Name), shown(se.at), untrusted.CleanErr(err).Error()),
			append(shownAll(errs.Hints(err)), "see what it serves now: "+se.atFresh)...)
	}
	if err != nil || se.at == "" {
		return err
	}
	got := se.v.Meta.Digest
	if se.spec.Kind == source.KindGit {
		got = se.v.Commit
	}
	return atHolds(se.ent.Name, got, se.at, se.atFresh)
}

// atKind refuses an --at version that is not the kind name's source takes: a git module's
// commit, a folder's or archive's content digest (sha256:<digest>).
func atKind(name string, kind source.Kind, want string) error {
	digest := strings.HasPrefix(want, "sha256:")
	switch {
	case kind == source.KindGit && digest:
		return fmt.Errorf("%s is a git module: --at takes its commit, not a sha256: digest", shown(name))
	case kind == source.KindArchive && !digest:
		return fmt.Errorf("%s is an archive module: --at takes its content digest, sha256:<digest>, not a commit", shown(name))
	case kind == source.KindFolder && !digest:
		return fmt.Errorf("%s is a folder module: --at takes its content digest, sha256:<digest>, not a commit", shown(name))
	}
	return nil
}

// atHolds refuses a module whose source serves got where --at names want, before anything is
// written, hinting fresh, the run's dry run without --at.
func atHolds(name, got, want, fresh string) error {
	if got == want {
		return nil
	}
	return errs.WithHints(fmt.Errorf("%s: its source now serves %s, not %s as --at says; nothing was written", shown(name), shown(got), shown(want)),
		"see what it serves now: "+fresh)
}

// resolveVersion is resolveSync's work, before the --at check.
func (e *Engine) resolveVersion(ctx context.Context, sc Scope, se *syncEntry, run string, display []string) error {
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
		if _, err := os.Stat(se.spec.Location); err == nil {
			return stage()
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%s: %w", name, untrusted.CleanErr(err))
		}
		v, found, err := e.Store.Pinned(name, se.installed, run)
		if err != nil {
			return err
		}
		if !found {
			return errs.WithHints(fmt.Errorf("%s is no longer in your cache, and its source folder is gone", name),
				"remove it: "+e.Command(sc, "uninstall", append([]string{name}, display...)...))
		}
		se.v, se.sourceGone = v, true
		se.notices = append(se.notices, Notice{Level: LevelWarn, Text: fmt.Sprintf("%s: its source folder %s is gone; rendered the version installed",
			name, untrusted.Text(se.spec.Location, untrusted.MaxName))})
		return nil
	case se.update:
		if se.fresh {
			done, err := e.resolveHead(se, run)
			if err != nil || done {
				return err
			}
		}
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
		head, ok, err := e.Store.HeadFor(name, ent.Ref, sourceKey(se.spec))
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
		// The cache may hold a commit from a branch nobody reviewed, cached for another project: an
		// entry the user has not trusted must name a commit on its ref. So must a profile's pin
		// read from a project's lock.json, which a file planted in the checkout could name.
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

// sourceFailure reports whether err is a source failing to serve an entry: its fetch (network,
// auth, a ref or source that is gone), a commit its ref does not reach, or content other than its
// pin, its catalog or its sha256 says; not a failure of lola's own state.
func sourceFailure(err error) bool {
	_, fetch := errors.AsType[*fetchFailedError](err)
	_, notOnRef := errors.AsType[*source.NotOnRefError](err)
	_, sum := errors.AsType[*source.SHA256MismatchError](err)
	_, catalog := errors.AsType[*catalogMismatchError](err)
	_, digest := errors.AsType[*digestMismatchError](err)
	_, served := errors.AsType[*servedCommitError](err)
	_, commit := errors.AsType[*commitFetchError](err)
	_, unreachable := errors.AsType[*source.UnreachableError](err)
	return fetch || notOnRef || sum || catalog || digest || served || commit || unreachable
}

// shownVersions is the --at a hint repeating req adds (nil for lola profile, which has no --at):
// req's own, and each new version the run showed of an entry, so the hinted run answers that
// version or is refused. An entry it read whose content is not the version installed gives its
// commit (git) or content digest; one it did not read (a pull's change, never fetched before
// trust) whose entry pins another version than installed gives that pin, as the diff printed it,
// so a second pull before the hinted run is refused.
func shownVersions(req SyncRequest, entries []*syncEntry) map[string]string {
	if req.move != nil || req.profileArgs != nil || req.Profiles != nil {
		return nil
	}
	at := maps.Clone(req.At)
	for _, se := range entries {
		var v string
		switch {
		case se.v.ContentDir != "" && se.v.Meta.Digest != se.installed:
			if v = se.v.Meta.Digest; se.spec.Kind == source.KindGit {
				v = se.v.Commit
			}
		case se.v.ContentDir == "" && se.ent.Digest != "" && se.ent.Digest != se.installed:
			if v = se.ent.Digest; se.spec.Kind == source.KindGit && se.ent.Commit != "" {
				v = se.ent.Commit
			}
		default:
			continue
		}
		if at == nil {
			at = map[string]string{}
		}
		at[se.ent.Name] = v
	}
	return at
}

// reviewSource is the lola info arguments that show a folder's or archive's version from its
// source, when the cache does not hold it (a refusal caches nothing): the folder or local archive
// as the user can type it from here (./ before a relative one, so it never reads as a cached
// name or an option), an archive's URL as written, and the targets the copy installs to. nil for
// a git source, whose commit reviewOf names.
func (e *Engine) reviewSource(se *syncEntry, targets []string) []string {
	if se.spec.Kind == source.KindGit {
		return nil
	}
	arg := se.spec.Location
	if se.spec.Kind == source.KindFolder || filepath.IsAbs(arg) {
		arg = e.pathHere(arg)
	}
	out := []string{untrusted.Text(arg, untrusted.MaxName)}
	for _, t := range targets {
		out = append(out, "--target", t)
	}
	return out
}

// pinnedIn is where an entry's pin is written, for a pinned fetch's error: the manifest or a
// project profile, whose pin sync --update rewrites; pins.yaml or lock.json, whichever gave the
// unpinned entry of the user's own profile its pin, which --update resolves again; or that profile
// itself, which only the user edits.
func (e *Engine) pinnedIn(sc Scope, se *syncEntry) (file, repin string) {
	switch {
	case se.member != nil:
		return e.ownPin(sc, se.from, se.file)
	case se.from == "":
		return e.manifestName(sc), e.SyncCommand(sc, "--update", se.ent.Name)
	case se.key != "":
		return projectProfileFile(se.key), e.SyncCommand(sc, "--update", se.ent.Name)
	case se.pinsFile:
		return "pins.yaml", e.SyncCommand(sc, "--update", se.ent.Name)
	case se.ledgerPin:
		return "lock.json", e.SyncCommand(sc, "--update", se.ent.Name)
	}
	return e.ownPin(sc, se.from, se.file)
}

// ownPin is where the pin of an entry from a profile of the user's own, or from a remote profile,
// is, as a message names it, and how to change it: from is the entry's layer and path its file. A
// remote profile's pin changes only with lola profile remote:<name> --update; any other's is edited
// by hand.
func (e *Engine) ownPin(sc Scope, from, path string) (file, repin string) {
	q := strings.TrimPrefix(from, ledger.FromProfile(""))
	if remoteLayer(from) {
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

// prepareFetch fetches spec into run and prepares it there; the content must declare name, or none,
// and then it is name, as the entry records it (a catalog entry's name).
func (e *Engine) prepareFetch(ctx context.Context, name string, spec source.Spec, run string) (store.Staged, error) {
	f, err := source.Fetch(ctx, spec, run)
	if err != nil {
		return store.Staged{}, &fetchFailedError{err}
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

// publishRun commits the run's staged versions to the cache, once nothing is left that could refuse
// or decline the run, so a refusal or a No leaves the cache as it was, every head and fetch time
// included; a declined entry, out of entries by then, is never committed. A dry run publishes what
// the user trusts at its pin (a project dry run moves no head, not even for an --update fetch, and
// replaces no source the cache records), and --update -n at user scope, whose files are the user's
// own, fetches into the cache and moves heads, as -n always may there.
func (e *Engine) publishRun(sc Scope, entries []*syncEntry, opts Options) error {
	for _, se := range entries {
		trusted := se.covered && se.pin == nil && !se.folderChanged
		if opts.DryRun && (!trusted || se.goneFrom != "") && (sc.Kind != target.User || !se.update) {
			continue
		}
		if opts.DryRun && trusted && sc.Kind == target.Project {
			se.headIfAbsent = true
		}
		if err := e.publish(se); err != nil {
			return err
		}
	}
	return nil
}

// droppedFiles is what a version the run installs no longer ships: the whole files lola wrote
// that changes, planned at sc over led as prepare plans them but before any consent narrows
// them, delete for a module whose version changes, at a module and target no removal in
// removals covers. A JSON member a plan moves or re-keys, and a file a pick or a setting drops
// from the same version, are not. A plan that does not compute (a conflict) lists none: prepare
// refuses it before anything is written.
func (e *Engine) droppedFiles(sc Scope, led *ledger.Ledger, changes []plan.ModuleChange, removals []Removal, opts Options) []Removal {
	moved := map[string]bool{}
	for _, c := range changes {
		if was := led.Modules[c.Name]; was != nil && c.Entry != nil && c.Entry.Digest != was.Digest {
			moved[c.Name] = true
		}
	}
	if len(moved) == 0 {
		return nil
	}
	req := plan.Request{Roots: map[string]string{}, Ledger: led, Changes: changes, Force: opts.Force}
	for _, t := range e.Targets {
		req.Roots[t.ID()] = t.Root(sc.Kind, sc.ProjectDir, e.Paths)
	}
	p, err := plan.Compute(req)
	if err != nil {
		return nil
	}
	for i := range p.Steps {
		p.Steps[i].Scope = sc.Kind.String() // as prepare sets it
	}
	steps := slices.DeleteFunc(p.Steps, func(st plan.Step) bool {
		return !moved[st.Module] || st.Key != "" ||
			slices.ContainsFunc(removals, func(r Removal) bool { return r.Module == shown(st.Module) && slices.Contains(r.Targets, st.Target) })
	})
	return deletedBy(steps, nil, nil, whyDropped, e.Paths.Home)
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
	r := &Removal{Module: shown(name), Targets: []string{}, WholeModule: keep == nil, Paths: []RemovedPath{}, Scope: sc.Kind.String()}
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

// selectionsLeftError is a move applied in full, the entries and their picks in their profile,
// project.yml and lock.json written, whose carried picks lola could not then remove from
// selections.yaml: no hint repeats the move, which is done.
type selectionsLeftError struct{ error }

func (e *selectionsLeftError) Unwrap() error { return e.error }

// selectionsLeft is the error of a failed write of changes, the removals of a move's carried picks
// from selections.yaml (moveEdit.selections), after the move was applied: it says what is on disk
// and names the file and the entries to delete, under each projects key as the file writes it
// (written, one per change), which no lola command removes. err's own hints, written for an
// install that wrote nothing, are left out.
func (e *Engine) selectionsLeft(changes []config.Change, written []string, err error) error {
	var modules, keys []string
	under := map[string][]string{}
	for i, c := range changes {
		modules = append(modules, shown(c.Module))
		key := untrusted.Text(written[i], untrusted.MaxName)
		if under[key] == nil {
			keys = append(keys, key)
		}
		under[key] = append(under[key], shown(c.Module))
	}
	var from, del []string
	for _, key := range keys {
		mods := strings.Join(under[key], ", ")
		from = append(from, fmt.Sprintf("%s from selections.yaml's projects[%s]", mods, key))
		del = append(del, fmt.Sprintf("%s under projects: %s:", mods, key))
	}
	file := untrusted.Text(e.Paths.Selections, untrusted.MaxName)
	return &selectionsLeftError{errs.WithHints(fmt.Errorf("%s moved: the profile entry holds the picks, and project.yml and lock.json are written, but lola could not "+
		"remove %s: %s", strings.Join(modules, ", "), strings.Join(from, ", and "), untrusted.CleanErr(err).Error()),
		fmt.Sprintf("no lola command removes it: in %s, delete %s; until then lola warns that it is ignored", file, strings.Join(del, ", and ")))}
}
