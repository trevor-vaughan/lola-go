package engine

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/trust"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// maxLabel caps a description shown in a picker; pickers cut it again to the terminal width.
const maxLabel = 200

// ChoicesRequest asks for what the install pickers offer at scopes. Modules are the module
// arguments: each is resolved, and a source is fetched, so its components can be picked.
type ChoicesRequest struct {
	Cwd     string
	Scopes  []ScopeChoice
	Modules []InstallRequest
	// Run, when set, is the install's staged run (StagedRun): what the module arguments fetch
	// stays in it, out of the cache, and a version an earlier call staged in it is listed from it.
	// Without one, the call fetches into a run of its own and caches what it fetches at once (lola
	// migrate's planning relies on that).
	Run *StagedRun
	// DryRun is the run's -n and Display its output flags, which every hint keeps (Plan 6c §11.3).
	DryRun  bool
	Display []string
	Target  TargetRequest // where the install writes (--profile, --override)
	// AskWhere, when set, asks the question about the file the install writes (writeTarget: the
	// bundle member question, the base under a remote profile), as InstallAll asks it, so an
	// AskWhere that answers a question once per run answers the install's with it; without it the
	// choices assume its first answer.
	AskWhere func(WhereQuestion) (WhereChoice, error)
}

// Choices is everything the install pickers need (Plan 5c §7), computed by the engine. Every
// author-chosen string in it has gone through untrusted.Text.
type Choices struct {
	Scopes    []ScopeChoices `json:"scopes"`
	Modules   []ModuleChoice `json:"modules"`   // every cached module, by source then name
	Requested []string       `json:"requested"` // the module arguments, resolved to names
	// Versions are the versions the module arguments resolved to, cached or staged in the
	// request's Run, in argument order;
	// InstallRequest.Version pins an install to them.
	Versions []VersionRef `json:"versions"`
	Warnings []string     `json:"warnings"` // cached modules that could not be loaded
	// Resolved is, for each module argument that is a marketplace address, where it resolved, as
	// the summary shows it before Apply (Plan 6c §6.3, ruling R-6c-15).
	Resolved []string `json:"resolved"`
	// Markets are your marketplaces with a fetched catalog and the entries each offers, which the
	// module picker shows after the cache (Plan 6c §6.5).
	Markets []MarketGroup `json:"markets"`

	sel     *config.Selections        // as read, with the scopes' profile entries' picks (withPicks), for Preview
	layers  map[string][]Layer        // each destination's layers (Scope.Key), for Preview's save-only modules
	targets map[string]writeTo        // each destination's write target, for Preview
	infos   map[string][]profile.Info // the profiles each destination can use, once its target listed them, for Preview's labels

	versions map[string]store.Version // the version listed of each module, cached or staged in the request's Run, for Mentions
	scans    mentionReads             // what Mentions read, shared with the install of the request's Run (StagedRun.scans)
	eng      *Engine                  // builds the hints of Preview's notices
}

// ScopeChoices is what one scope offers: its assistants and the targets its write target fixes.
// FixedTargets are the top-level targets an entry the install writes takes (fileTargets: the
// write target's, a profile's own or else the base's) when the trust record covers them; when it
// does not, they are UntrustedTargets instead, which an install may not take unasked (Plan 6a
// §10.1), so the assistant picker asks and never preselects them. TargetsFrom names the file
// they come from, "" for the scope's base (targetsFile).
type ScopeChoices struct {
	ScopeChoice
	Name             string      `json:"scope"` // project or user; Scope.Key() tells two project roots apart
	FixedTargets     []string    `json:"fixed_targets"`
	UntrustedTargets []string    `json:"untrusted_targets"`
	TargetsFrom      string      `json:"targets_from"`
	Assistants       []Assistant `json:"assistants"`
}

// ModuleChoice is one cached module, with its components and its state at each scope.
type ModuleChoice struct {
	Name         string            `json:"name"`
	Source       string            `json:"source"`
	Type         string            `json:"type"`
	Format       string            `json:"format"`
	Description  string            `json:"description"`
	Digest       string            `json:"digest"`
	ID           string            `json:"id"` // the cached version listed; InstallRequest.Version pins an install to it
	Components   []ComponentChoice `json:"components"`
	RuntimeHooks int               `json:"runtime_hooks"` // matcher groups the consent prompt will ask about
	PerScope     []ModuleScope     `json:"per_scope"`     // one per destination, in scope order
}

// ComponentChoice is one component with what its picker label shows.
type ComponentChoice struct {
	Component
	Description string `json:"description"` // the first line of its description
	RunsCommand bool   `json:"runs_command"`
}

// ModuleScope is a module's state at one destination.
type ModuleScope struct {
	Scope     string `json:"scope"` // the destination: Scope.Key()
	Installed bool   `json:"installed"`
	Listed    bool   `json:"listed"`    // the scope's write target lists it; an install of one it neither lists nor inherits writes its entry
	Inherited bool   `json:"inherited"` // the write target inherits its entry from a layer below, which an install asking the same keeps
	Digest    string `json:"digest"`    // the installed version, "" when not installed
	Commit    string `json:"commit"`    // the commit lock.json records, "" when not installed
	// ListedCommit is the commit an install pins where it is Listed: the head of the ref of the
	// write target's entry (InstallAll's pinCommit), git only.
	ListedCommit string   `json:"listed_commit"`
	Targets      []string `json:"targets"` // the assistants it is installed for
	RulesOff     bool     `json:"rules_off"`
	Excluded     []string `json:"excluded"`              // kind/name that your picks exclude here
	NotInOnly    []string `json:"not_in_only"`           // those of Excluded the only: list in force alone leaves out
	ExcludedIn   string   `json:"excluded_in,omitempty"` // where the skips that exclude the rest live (skippedIn), "" when none
	OnlyFrom     string   `json:"only_from"`             // the section whose only: list is in force here, "" when none
	Current      []string `json:"current_components"`    // kind/name installed now
	// New is kind/name of each component the listed version adds over the version installed here
	// ([] when nothing is installed or that version is no longer cached), and Provider the layer that
	// provides the module here when it is a profile, as layerLabel names it ("" otherwise)
	// (cherry-pick design §4.6, §6).
	New      []string `json:"new"`
	Provider string   `json:"provider"`
}

// Scope returns m's state at the destination whose Scope.Key() is key.
func (m ModuleChoice) Scope(key string) ModuleScope {
	for _, s := range m.PerScope {
		if s.Scope == key {
			return s
		}
	}
	return ModuleScope{Scope: key}
}

// Module returns the choice for name, or nil.
func (c *Choices) Module(name string) *ModuleChoice {
	for i := range c.Modules {
		if c.Modules[i].Name == name {
			return &c.Modules[i]
		}
	}
	return nil
}

// Mentions is what each component of the version of module name the choices list mentions
// (cherry-pick design §5.1), read on the first call from that version's content directory (the
// cache's, or the staged run's for a module argument the run fetched) and kept in the request's
// run, whose install reads it from there. A module the choices do not list has none. A read that
// fails is kept too: later calls return its error without reading again.
func (c *Choices) Mentions(name string) (Mentions, error) {
	v, ok := c.versions[name]
	if !ok {
		return nil, nil
	}
	return c.scans.read(v)
}

// listedMentions is, for each of targets, what its listed version's components mention
// (Mentions), whether its rules install at the destination key (not RulesOff there, unless
// --rules or --no-rules says) and the words a hint installs it with: spec's request for it, by
// the version it pins, named as given while the run holds it staged, since the user may decline
// the review. A module whose mentions cannot be read carries scanErr instead.
func (c *Choices) listedMentions(targets []editTarget, key string, spec InstallSpec) map[string]mentionModule {
	out := map[string]mentionModule{}
	for _, t := range targets {
		ms, err := c.Mentions(t.name)
		if err != nil {
			out[t.name] = mentionModule{scanErr: err}
			continue
		}
		on := !c.Module(t.name).Scope(key).RulesOff
		if spec.Options.Rules != nil {
			on = *spec.Options.Rules
		}
		r := InstallRequest{Arg: t.name}
		if i := slices.IndexFunc(spec.Modules, func(r InstallRequest) bool { return r.Version != nil && r.Version.Name == t.name }); i >= 0 {
			r = spec.Modules[i]
		}
		out[t.name] = mentionModule{mentions: ms, rulesOn: on, words: mentionArgs(t.name, c.versions[t.name], r, spec.Staged, true)}
	}
	return out
}

// InstallChoices computes what the install pickers offer: the scopes with their assistants and
// fixed targets, every cached module (and the module arguments, resolved) with its components,
// and per scope whether it is installed, which components selections.yaml excludes and which
// are installed now. It writes nothing to the cache when the request gives a Run; without one,
// what a module argument fetches is cached at once.
func (e *Engine) InstallChoices(ctx context.Context, req ChoicesRequest) (_ *Choices, err error) {
	var scopes []Scope
	for _, s := range req.Scopes {
		scopes = append(scopes, s.Scope)
	}
	defer func() { err = e.CoachUnresolved(err, scopes, req.DryRun, req.Display) }()
	// An install of an address may replace an unresolved entry of its name (Plan 6c §6.3), so the
	// pickers read such a manifest as that install does.
	replacing := replacingNames(req.Modules)
	// The profiles lock comes first (spec 2026-10-29 §16), as an install's does.
	unlockProfiles, err := e.readProfiles(true)
	if err != nil {
		return nil, err
	}
	defer unlockProfiles()
	swept := e.sweepRuns()
	run := req.Run
	if run == nil {
		if run, err = e.publishingRun(); err != nil {
			return nil, err
		}
		defer func() { err = errors.Join(err, run.Close()) }()
	}
	sel, err := e.selections()
	if err != nil {
		return nil, err
	}
	ch := &Choices{Scopes: []ScopeChoices{}, Modules: []ModuleChoice{}, Requested: []string{}, Versions: []VersionRef{}, Warnings: []string{},
		Markets: []MarketGroup{}, sel: sel, layers: map[string][]Layer{}, targets: map[string]writeTo{}, infos: map[string][]profile.Info{}, versions: map[string]store.Version{}, scans: run.scans(), eng: e}
	for _, n := range swept {
		ch.Warnings = append(ch.Warnings, n.Text)
	}
	ch.Resolved = []string{}
	// A version a module argument resolved to (its --ref, a fresh fetch, or the version pinned
	// by r.Version, which is not fetched again) replaces the default head.
	asked := map[string]store.Version{}
	// The pickers refuse what the install would refuse after confirmation: a project whose
	// ledger git tracks, before any argument is resolved.
	var checked Result
	for _, s := range req.Scopes {
		if err := e.refuseTrackedLedger(ctx, s.Scope, &checked); err != nil {
			return nil, err
		}
	}
	for _, n := range checked.Notices {
		ch.Warnings = append(ch.Warnings, n.Text)
	}
	// The trust state of each scope's entries: an install refuses to rewrite an uncovered one,
	// so the pickers must never offer what it would refuse after confirmation, and say once per
	// scope which entries lola sync would review.
	uncovered, notices, err := e.uncoveredEntries(req.Scopes, replacing)
	if err != nil {
		return nil, err
	}
	ch.Warnings = append(ch.Warnings, notices...)
	// A name an uncovered entry has is refused before any argument is resolved, so nothing is
	// fetched.
	for _, r := range req.Modules {
		if u, ok := uncovered[r.Arg]; ok {
			return nil, e.notTrusted(u.sc, u.rec, u.ent, false, u.unresolved)
		}
	}
	// Marketplace addresses resolve to their catalog entries before anything is fetched (Plan 6c
	// §6.3).
	req.Modules = slices.Clone(req.Modules)
	unlock, err := e.resolveMarkets(req.Modules, true, req.Display)
	if err != nil {
		return nil, err
	}
	defer unlock()
	for _, r := range req.Modules {
		sc := Scope{Kind: target.User}
		if len(req.Scopes) > 0 {
			sc = req.Scopes[0].Scope
		}
		var pinned store.Version
		if r.Version != nil {
			if pinned, err = e.pinnedVersion(run, r); err != nil {
				return nil, err
			}
		}
		// Without --ref a name takes the ref each scope's entry records; scopes must agree.
		refs := []string{r.Ref}
		if r.Ref == "" && module.ValidateName("module", r.Arg) == nil {
			if refs, err = e.entryRefs(r.Arg, req.Scopes, replacing); err != nil {
				return nil, err
			}
		}
		var v store.Version
		for _, ref := range refs {
			rr := r
			rr.Ref = ref
			ent, rv, _, err := e.resolve(ctx, sc, req.Cwd, rr, pinned, "", run, req.Display)
			if err != nil {
				return nil, err
			}
			if r.market != nil {
				ch.Resolved = append(ch.Resolved, resolvedLine(r.Arg, ent, rv.Commit))
			}
			if v.Name != "" && v.ID != rv.ID {
				return nil, twoVersions(rv.Name)
			}
			v = rv
		}
		// A version listed earlier (a pick kept when Change scope asks again) must be the one
		// every scope takes; InstallAll would refuse it only after confirmation.
		if r.Version != nil && r.Ref == "" && r.Arg == pinned.Name {
			lv, u, err := e.listedVersion(run, pinned, req.Scopes, replacing)
			if err != nil {
				return nil, err
			}
			if u == nil && lv.ID != pinned.ID {
				u = &unlisted{why: fmt.Sprintf("a scope's entry records a ref whose cached version is %s, not %s, the version listed",
					untrusted.Text(lv.ID, untrusted.MaxName), untrusted.Text(pinned.ID, untrusted.MaxName)),
					fix: oneScopeAtATime(untrusted.Text(pinned.Name, untrusted.MaxName), req.Scopes)}
			}
			if u != nil {
				return nil, errs.WithHints(fmt.Errorf("%s: %s", untrusted.Text(pinned.Name, untrusted.MaxName), u.why), u.fix, "run lola install again to pick again")
			}
		}
		// Adding targets to an installed module keeps it, and its pin (planInstallModule).
		keep := false
		if u, ok := uncovered[v.Name]; ok && len(r.Targets) > 0 {
			return nil, e.notTrusted(u.sc, u.rec, u.ent, false, u.unresolved)
		}
		if len(r.Targets) > 0 {
			man, led, _, _, err := e.loadReplacing(sc, true, replacing)
			if err != nil {
				return nil, err
			}
			prev, installed := man.Find(v.Name), led.Modules[v.Name]
			if v, err = e.installedVersion(sc, v, prev, installed); err != nil {
				return nil, err
			}
			keep = prev != nil && installed != nil
		}
		// InstallAll, given the version listed (which carries no commit), would refuse a git
		// version it cannot pin only after confirmation. A marketplace entry's version carries
		// the commit it was fetched at (VersionRef.commit).
		if keep || r.market != nil {
			refs = nil
		}
		for _, ref := range refs {
			if ref == "" {
				if ref, err = e.listedRef(run, VersionRef{Name: v.Name, ID: v.ID}); err != nil {
					return nil, err
				}
			}
			if _, err := e.pinCommit(run, store.Version{Name: v.Name, ID: v.ID}, ref); err != nil {
				return nil, err
			}
		}
		if prev, dup := asked[v.Name]; dup && prev.ID != v.ID {
			return nil, errs.WithHints(fmt.Errorf("module arguments resolve to two versions of %s", untrusted.Text(v.Name, untrusted.MaxName)),
				"name each module once, with one --ref")
		}
		asked[v.Name] = v
		ch.Requested = append(ch.Requested, v.Name)
		listed := VersionRef{Name: v.Name, ID: v.ID}
		if r.market != nil {
			listed.commit = v.Commit
		}
		ch.Versions = append(ch.Versions, listed)
	}
	type state struct {
		sc   ScopeChoice
		man  *manifest.Manifest
		led  *ledger.Ledger
		rec  *trust.Record
		want []Wanted
		// profiles is the profiles the scope can use, listed once (visibleOnce)
		profiles func() ([]profile.Info, error)
	}
	var states []state
	for _, s := range req.Scopes {
		man, led, _, _, err := e.loadReplacing(s.Scope, true, replacing)
		if err != nil {
			return nil, err
		}
		rec, err := e.trustRecord(s.Scope)
		if err != nil {
			return nil, err
		}
		if ch.layers[s.Scope.Key()], _, err = e.layers(s.Scope, man, led); err != nil {
			return nil, err
		}
		// Without AskWhere the preview assumes the first answer of a question about where the
		// install writes; InstallAll asks it.
		ask := req.AskWhere
		if ask == nil {
			ask = func(q WhereQuestion) (WhereChoice, error) { return q.Choices[0], nil }
		}
		t, infos, err := e.writeTarget(s.Scope, ch.layers[s.Scope.Key()], nil, req.Target, req.Target.verb(), installTyped(req.Modules), ask)
		if err != nil {
			return nil, err
		}
		ch.targets[s.Scope.Key()], ch.infos[s.Scope.Key()] = t, infos
		if sel, err = withPicks(sel, append(slices.Clone(ch.layers[s.Scope.Key()]), t.Layer)); err != nil {
			return nil, err
		}
		ch.sel = sel
		want, _ := desired(ch.layers[s.Scope.Key()])
		fixed, untrustedTargets := slices.Clone(fileTargets(t, man)), []string{}
		if fixed == nil {
			fixed = []string{}
		}
		if !targetsCovered(rec, t, man) {
			fixed, untrustedTargets = []string{}, fixed
		}
		detected := e.Detect(s.Scope)
		for i := range detected {
			detected[i].Binary = untrusted.Text(detected[i].Binary, untrusted.MaxName)
		}
		ch.Scopes = append(ch.Scopes, ScopeChoices{ScopeChoice: s, Name: s.Scope.Kind.String(), FixedTargets: fixed,
			UntrustedTargets: untrustedTargets, TargetsFrom: targetsFile(t), Assistants: detected})
		states = append(states, state{sc: s, man: man, led: led, rec: rec, want: want, profiles: e.visibleOnce(s.Scope)})
	}
	cached, warnings, err := e.CachedModules()
	if err != nil {
		return nil, err
	}
	for _, w := range warnings {
		ch.Warnings = append(ch.Warnings, untrusted.Text(w, untrusted.MaxName))
	}
	// Your marketplaces only add to what the pickers offer; no command fixes a file that does not
	// parse, so the warning names none. An address argument was refused already (resolveMarkets).
	groups, unread, err := e.MarketGroups(req.Display)
	if err != nil {
		groups, unread = []MarketGroup{}, []string{"your marketplaces cannot be read, so the pickers offer none: " + shown(untrusted.CleanErr(err).Error())}
	}
	ch.Markets, ch.Warnings = groups, append(ch.Warnings, unread...)
	// A module argument whose default head does not load, or that only the run holds, is not in
	// cached; the version it resolved to is still offered.
	for name, v := range asked {
		if !slices.ContainsFunc(cached, func(c Cached) bool { return c.Version.Name == name }) {
			o, _, err := e.origin(run, name)
			if err != nil {
				return nil, err
			}
			cached = append(cached, Cached{Version: v, Origin: o})
		}
	}
	for _, c := range cached {
		v := c.Version
		if a, ok := asked[v.Name]; ok {
			v = a
		} else {
			// Installing a cached module takes the head of the ref each scope's entry records,
			// so that is the version listed (Plan 6a §10.1); one that cannot be listed is
			// left out, with a warning, so the others can still be picked.
			// An uncovered entry is checked first, and left out under its scope's one notice:
			// listing its pulled ref could coach installing it by name, which is refused.
			if _, ok := uncovered[c.Version.Name]; ok {
				continue
			}
			var u *unlisted
			if v, u, err = e.listedVersion(nil, v, req.Scopes, replacing); err != nil {
				return nil, err
			}
			if u != nil {
				ch.Warnings = append(ch.Warnings, untrusted.Text(c.Version.Name, untrusted.MaxName)+" is not listed: "+u.why+"; "+u.fix)
				continue
			}
		}
		mc := moduleChoice(v, c.Origin)
		ch.versions[v.Name] = v
		// The version InstallAll pins (pinnedVersion): the request's carries the commit it was fetched
		// at for a marketplace entry, and one this run staged the fetch's; else a head's.
		pin := store.Version{Name: v.Name, ID: v.ID}
		if sv, ok := run.version(v.Name, v.ID); ok {
			pin.Commit = sv.Commit
		}
		if k := slices.IndexFunc(ch.Versions, func(r VersionRef) bool { return r.Name == v.Name && r.ID == v.ID }); k >= 0 {
			pin.Commit = cmp.Or(ch.Versions[k].commit, pin.Commit)
		}
		for _, st := range states {
			// A module no layer lists yet is written to the scope's write target, whose picks apply to it.
			key := st.sc.Scope.Key()
			from := ch.targets[key].from()
			if w := find(st.want, v.Name); w != nil {
				from = w.From
			}
			ms, err := e.moduleScope(sel, st.sc.Scope, ch.layers[key], ch.targets[key], from, st.man, st.led, st.rec, v, st.profiles)
			if err != nil {
				return nil, err
			}
			if ms.Listed {
				commit, _, err := e.headCommit(run, pin, ch.targets[key].layerEntry(v.Name).Ref)
				if err != nil {
					return nil, err
				}
				ms.ListedCommit = untrusted.Text(commit, untrusted.MaxName)
			}
			mc.PerScope = append(mc.PerScope, ms)
		}
		ch.Modules = append(ch.Modules, mc)
	}
	slices.SortFunc(ch.Modules, func(a, b ModuleChoice) int {
		return cmp.Or(cmp.Compare(a.Source, b.Source), cmp.Compare(a.Name, b.Name))
	})
	return ch, nil
}

// uncoveredEntry is an entry of a scope's manifest that rec, its trust record, does not cover.
type uncoveredEntry struct {
	sc         Scope
	rec        *trust.Record
	ent        *manifest.Entry
	unresolved bool // the scope's manifest holds a marketplace address (holdsAddress)
}

// uncoveredEntries maps each module name to the first project scope of scopes whose entry for
// it the trust record does not cover, and returns the pickers' notices (Plan 6a §10.1): one per
// such scope, naming its uncovered entries and the sync that reviews them.
func (e *Engine) uncoveredEntries(scopes []ScopeChoice, replacing []string) (map[string]uncoveredEntry, []string, error) {
	out := map[string]uncoveredEntry{}
	var notices []string
	for _, s := range scopes {
		rec, err := e.trustRecord(s.Scope)
		if err != nil {
			return nil, nil, err
		}
		if rec == nil {
			continue
		}
		man, _, err := e.manifestReplacing(s.Scope, replacing)
		if err != nil {
			return nil, nil, err
		}
		var names []string
		unresolved := holdsAddress(man)
		for i := range man.Modules {
			ent := &man.Modules[i]
			// An unresolved entry is the install's own to replace, or its warning (Plan 6c §6.3).
			if _, _, unresolved := manifest.Address(ent.Source); unresolved || rec.View.Covers(*ent) {
				continue
			}
			names = append(names, ent.Name)
			if _, seen := out[ent.Name]; !seen {
				out[ent.Name] = uncoveredEntry{sc: s.Scope, rec: rec, ent: ent, unresolved: unresolved}
			}
		}
		if len(names) == 0 {
			continue
		}
		slices.Sort(names)
		base := e.manifestName(s.Scope)
		// An entry the record never held (a pull added it) was never trusted, so it is new.
		added := slices.ContainsFunc(names, func(n string) bool { return rec.View.Find(n) == nil })
		what := fmt.Sprintf("%d modules in %s changed since you last trusted them", len(names), base)
		switch {
		case !rec.Exists && len(names) == 1:
			what = "1 module in " + base + " is not trusted on this machine yet"
		case !rec.Exists:
			what = fmt.Sprintf("%d modules in %s are not trusted on this machine yet", len(names), base)
		case added && len(names) == 1:
			what = "1 module in " + base + " is new since you last trusted the file"
		case added:
			what = fmt.Sprintf("%d modules in %s are new or changed since you last trusted the file", len(names), base)
		case len(names) == 1:
			what = "1 module in " + base + " changed since you last trusted it"
		}
		what += ": " + nameList(names)
		if !unresolved {
			what += " → " + e.SyncCommand(s.Scope)
		}
		notices = append(notices, what)
	}
	return out, notices, nil
}

// entryRefs is the distinct refs the scopes' entries for name record, "" for a scope with no
// entry or no ref; it is [""] when there are no scopes.
func (e *Engine) entryRefs(name string, scopes []ScopeChoice, replacing []string) ([]string, error) {
	refs := []string{}
	for _, s := range scopes {
		man, _, err := e.manifestReplacing(s.Scope, replacing)
		if err != nil {
			return nil, err
		}
		if ref := entryRef(man, name); !slices.Contains(refs, ref) {
			refs = append(refs, ref)
		}
	}
	if len(refs) == 0 {
		refs = append(refs, "")
	}
	return refs, nil
}

// unlisted says why no version of a module can be listed for the scopes, and how to install it.
type unlisted struct{ why, fix string }

// listedVersion is the version of def's module that installing it into every scope takes: the
// head of the ref each scope's entry records (the default branch's for an entry without one),
// else def for a scope with no entry. When no version can be listed (a ref whose head is not
// cached, or scopes that resolve to different versions) it says why instead, so nothing the
// install would refuse after confirmation is offered.
// The cache is read as run will leave it (Engine.origin); nil is the cache alone.
func (e *Engine) listedVersion(run *StagedRun, def store.Version, scopes []ScopeChoice, replacing []string) (store.Version, *unlisted, error) {
	name := untrusted.Text(def.Name, untrusted.MaxName)
	v, listed := def, false
	for _, s := range scopes {
		man, _, err := e.manifestReplacing(s.Scope, replacing)
		if err != nil {
			return store.Version{}, nil, err
		}
		rv := def
		ent := man.Find(def.Name)
		if ent != nil {
			o, _, err := e.origin(run, def.Name)
			if err != nil {
				return store.Version{}, nil, err
			}
			h, ok := o.Head(ent.Ref)
			if !ok {
				which := "ref " + untrusted.Text(ent.Ref, untrusted.MaxName)
				if ent.Ref == "" {
					which = "the default branch its entry tracks"
				}
				return store.Version{}, &unlisted{why: "your cache has no version of " + which,
					fix: "install it by name, which fetches it: " + errs.ShellJoin([]string{"lola", "install", name})}, nil
			}
			if rv, ok = run.version(def.Name, h.Digest); !ok {
				if rv, err = e.Store.Load(def.Name, h.Digest); err != nil {
					return store.Version{}, nil, err
				}
			}
			rv.Commit = h.Commit
		}
		// InstallAll pins the version at the commit of the head it records as the entry's ref.
		ref := ""
		if ent != nil {
			ref = ent.Ref
		} else if ref, err = e.listedRef(run, VersionRef{Name: rv.Name, ID: rv.ID}); err != nil {
			return store.Version{}, nil, err
		}
		if commit, git, err := e.headCommit(run, store.Version{Name: rv.Name, ID: rv.ID}, ref); err != nil {
			return store.Version{}, nil, err
		} else if git && commit == "" {
			return store.Version{}, &unlisted{why: "your cache does not record which commit its version " + rv.ID + " came from",
				fix: "fetch it again: " + errs.ShellJoin([]string{"lola", "cache", "update", name})}, nil
		}
		if listed && rv.ID != v.ID {
			return store.Version{}, &unlisted{why: "the scopes record refs of it that resolve to two versions", fix: oneScopeAtATime(name, scopes)}, nil
		}
		v, listed = rv, true
	}
	return v, nil, nil
}

// oneScopeAtATime coaches installing name into each of scopes on its own.
func oneScopeAtATime(name string, scopes []ScopeChoice) string {
	one := []string{}
	for _, s := range scopes {
		if c := errs.ShellJoin([]string{"lola", "install", name, "--scope", s.Scope.Kind.String()}); !slices.Contains(one, c) {
			one = append(one, c)
		}
	}
	return "install it into one scope at a time: " + strings.Join(one, ", then ")
}

// twoVersions refuses scopes whose entries record refs that resolve to different versions.
func twoVersions(name string) error {
	return errs.WithHints(fmt.Errorf("the scopes record refs of %s that resolve to two versions", untrusted.Text(name, untrusted.MaxName)),
		"install it into one scope at a time")
}

// pinnedVersion loads the version r.Version names, from run when run staged it, else from the
// cache, with the commit it was fetched at when the reference carries one. A version removed
// from the cache since the pickers listed it, or an invalid reference, is refused with a way
// forward.
func (e *Engine) pinnedVersion(run *StagedRun, r InstallRequest) (store.Version, error) {
	if v, ok := run.version(r.Version.Name, r.Version.ID); ok {
		v.Commit = cmp.Or(r.Version.commit, v.Commit)
		return v, nil
	}
	v, err := e.Store.Load(r.Version.Name, r.Version.ID)
	if err == nil {
		v.Commit = cmp.Or(r.Version.commit, v.Commit)
		return v, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		err = fmt.Errorf("version %s of %s was removed from the cache since it was listed",
			untrusted.Text(r.Version.ID, untrusted.MaxName), untrusted.Text(r.Version.Name, untrusted.MaxName))
	}
	return store.Version{}, errs.WithHints(untrusted.CleanErr(err), "run lola install again to list the current versions")
}

// firstLine is the first line of untrusted text, made safe and capped for a label.
func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return untrusted.Text(strings.TrimSpace(s), maxLabel)
}

// docLabel is a component's label: the first line of its description, or "(template)" for a
// template, whose frontmatter exists only once rendered for a target.
func docLabel(d module.Doc) string {
	if d.Template != nil {
		return "(template)"
	}
	return firstLine(d.String("description"))
}

// moduleChoice describes a cached version and its components.
func moduleChoice(v store.Version, o store.Origin) ModuleChoice {
	m := v.Module
	mc := ModuleChoice{Name: m.Name, Source: untrusted.Text(o.Source, untrusted.MaxName), Type: untrusted.Text(o.Type, untrusted.MaxName), Format: untrusted.Text(v.Meta.Format, untrusted.MaxName),
		Description: firstLine(m.Description), Digest: untrusted.Text(v.Meta.Digest, untrusted.MaxName), ID: v.ID, Components: []ComponentChoice{}, PerScope: []ModuleScope{}}
	for _, s := range m.Skills {
		mc.Components = append(mc.Components, ComponentChoice{Component: Component{config.KindSkill, s.Name}, Description: docLabel(s.Doc)})
	}
	for _, c := range m.Commands {
		mc.Components = append(mc.Components, ComponentChoice{Component: Component{config.KindCommand, c.Name}, Description: docLabel(c.Doc)})
	}
	for _, a := range m.Agents {
		mc.Components = append(mc.Components, ComponentChoice{Component: Component{config.KindAgent, a.Name}, Description: docLabel(a.Doc)})
	}
	for _, s := range m.MCPServers {
		cc := ComponentChoice{Component: Component{config.KindMCP, s.Name}, RunsCommand: s.Transport == module.TransportStdio}
		if !cc.RunsCommand {
			cc.Description = firstLine(s.URL)
		}
		mc.Components = append(mc.Components, cc)
	}
	for _, r := range m.Rules {
		mc.Components = append(mc.Components, ComponentChoice{Component: Component{config.KindRule, r.Name}, Description: firstLine(r.Body)})
	}
	for i := range mc.Components {
		mc.Components[i].Name = untrusted.Text(mc.Components[i].Name, untrusted.MaxName)
	}
	for _, h := range m.RuntimeHooks {
		mc.RuntimeHooks += runtimeHookGroups(h)
	}
	return mc
}

// moduleScope is v's module at sc: installed or not, its exclusions (those the only: list alone
// leaves out apart), the components v adds over the installed version, the profile that provides
// it, and the components installed now (the installed version, or v when that is no longer
// cached, through the resolver with today's selections). The exclusions and new components are
// componentStates', as lola info's are. An entry rec, sc's trust record, does not cover is taken
// as absent: an install from its source writes it as new. v is Listed when t, sc's write target,
// has an entry for it that rec covers, and Inherited when t has its entry from a layer below of
// sc's layers (inheritedFrom). from is the layer that provides v at sc (picksAt), and profiles the
// profiles sc can use, which name it (skippedIn).
func (e *Engine) moduleScope(sel *config.Selections, sc Scope, layers []Layer, t writeTo, from string, man *manifest.Manifest, led *ledger.Ledger, rec *trust.Record, v store.Version,
	profiles func() ([]profile.Info, error)) (ModuleScope, error) {
	x, err := exclusions(sel, sc, from, v.Name)
	if err != nil {
		return ModuleScope{}, err
	}
	ment := man.Find(v.Name)
	if ment != nil && rec != nil && !rec.View.Covers(*ment) {
		ment = nil // installed from its source, it is written as new (planInstallModule)
	}
	had, known, err := e.installedSet(v.Name, led.Modules[v.Name])
	if err != nil {
		// A cached installed version lola cannot read leaves nothing to call new; the page still
		// opens, as it did before New. The run's plan warns of it where an only: list is in force
		// (newComponentNotices), the one case where a new component goes uninstalled.
		had, known = nil, false
	}
	states, err := componentStates(v, x, ment.RulesOn(), had, known, func(origin string) (string, error) {
		return originLabel(x, origin, from, profiles)
	}, nil)
	if err != nil {
		return ModuleScope{}, err
	}
	excluded, notInOnly, skipped, added := []string{}, []string{}, []string{}, []string{}
	for _, st := range states {
		// The lists name a component as ComponentChoice does (moduleChoice), so callers match them
		// to its components; skippedIn reads the raw names.
		id := Component{st.Kind, untrusted.Text(st.Component.Name, untrusted.MaxName)}.String()
		switch st.State {
		case ComponentNotInOnly:
			excluded, notInOnly = append(excluded, id), append(notInOnly, id)
		case ComponentSkipped:
			excluded, skipped = append(excluded, id), append(skipped, st.Component.String())
		}
		if st.New {
			added = append(added, id)
		}
	}
	in, err := skippedIn(profiles, x, skipped, from)
	if err != nil {
		return ModuleScope{}, err
	}
	te := t.layerEntry(v.Name)
	ms := ModuleScope{Scope: sc.Key(), Listed: te != nil && t.covered(rec, *te), Inherited: inheritedFrom(layers, t, v.Name) != nil,
		Targets: []string{}, RulesOff: !ment.RulesOn(),
		Excluded: excluded, NotInOnly: notInOnly, ExcludedIn: in, Current: []string{}, New: added}
	if want, _ := desired(layers); find(want, v.Name) != nil && strings.HasPrefix(from, ledger.FromProfile("")) {
		// Profiles that do not list leave Provider "": the page opens without its footer line, as
		// lola info names no run when it cannot list them.
		if infos, err := profiles(); err == nil {
			ms.Provider = layerLabel(infos, from)
		}
	}
	_, origin := x.Only()
	if ms.OnlyFrom, err = originLabel(x, origin, from, profiles); err != nil {
		return ModuleScope{}, err
	}
	entry := led.Modules[v.Name]
	if entry == nil {
		return ms, nil
	}
	ms.Installed, ms.Digest, ms.Commit = true, untrusted.Text(entry.Digest, untrusted.MaxName), untrusted.Text(entry.Commit, untrusted.MaxName)
	for _, t := range allTargets(entry, ment, man) {
		ms.Targets = append(ms.Targets, untrusted.Text(t, untrusted.MaxName))
	}
	installed := v
	if entry.Digest != v.Meta.Digest {
		if id := strings.TrimPrefix(entry.Digest, "sha256:"); len(id) >= 12 {
			if iv, err := e.Store.Load(v.Name, id[:12]); err == nil {
				installed = iv
			}
		}
	}
	for _, c := range components(resolveComponents(installed.Module, x, ment.RulesOn()).module) {
		ms.Current = append(ms.Current, c.String())
	}
	return ms, nil
}

// runtimeHookGroups counts the matcher groups of a runtime hook set; a set that does not
// decode counts none (loading already validated it).
func runtimeHookGroups(h module.RuntimeHookSet) int {
	var events map[string][]json.RawMessage
	if json.Unmarshal(h.Hooks, &events) != nil {
		return 0
	}
	n := 0
	for _, groups := range events {
		n += len(groups)
	}
	return n
}

// ComponentDiff is what a run changes in one module's components for one assistant at one
// destination.
type ComponentDiff struct {
	Added     []string `json:"added"`
	Updated   []string `json:"updated"`
	Removed   []string `json:"removed"`
	Unchanged int      `json:"unchanged"`
}

// Diff compares the components a run selects (kind/name) for assistant id at the destination
// key with those installed for it there now. A component in both is updated when the cached
// version differs from the installed one.
func (m ModuleChoice) Diff(key, id string, selected []string) ComponentDiff {
	ms := m.Scope(key)
	var current []string
	if slices.Contains(ms.Targets, id) {
		current = ms.Current
	}
	d := ComponentDiff{Added: []string{}, Updated: []string{}, Removed: []string{}}
	for _, c := range selected {
		switch {
		case !slices.Contains(current, c):
			d.Added = append(d.Added, c)
		case ms.Digest != m.Digest:
			d.Updated = append(d.Updated, c)
		default:
			d.Unchanged++
		}
	}
	for _, c := range current {
		if !slices.Contains(selected, c) {
			d.Removed = append(d.Removed, c)
		}
	}
	return d
}

// Preview is what an install would select, without writing anything.
type Preview struct {
	Selected map[string]map[string][]string // destination (Scope.Key) → module → kind/name it installs
	// SaveOnly is, per destination, each module the install writes but does not install there,
	// with the label of the layer whose entry wins ("" when the file written is not on there).
	SaveOnly map[string]map[string]string
	// WritesTo is, per destination, the label of the file the install writes there (writeTarget).
	WritesTo map[string]string
	Changes  []config.Change // the entries it would write: selections.yaml's, or a profile entry's picks
	// PicksIn is, for each profile section of Changes (config.Where.Profile), the label of the
	// profile whose entry holds those picks.
	PicksIn map[string]string
	// Inherited is, per destination (Scope.Key) and section, each module whose picks there are another
	// layer's entry's than the write target's (pickSection), with the label of the file an override
	// of it writes instead.
	Inherited map[string]map[config.Where]map[string]string
	Notices   []Notice
}

// Preview applies spec's selection edits to a copy of selections.yaml and resolves, per scope,
// the components each of modules would install: the summary shows exactly what InstallAll
// will do. Rules are left out where they are off. A module whose entry in a layer above the write
// target wins installs nothing and gets the warning that it wins (saveOnlyNotice), as InstallAll
// does; one written to a profile that is not on installs nothing. With spec.NameMentions it names
// what the picks mention and leave out, as InstallAll does.
func (c *Choices) Preview(spec InstallSpec, modules []string) (*Preview, error) {
	sel := c.sel.Clone()
	var targets []editTarget
	for _, name := range modules {
		if m := c.Module(name); m != nil {
			t := editTarget{name: name}
			for _, cc := range m.Components {
				t.comps = append(t.comps, cc.Component)
			}
			targets = append(targets, t)
		}
	}
	// Each scope's picks go where its install saves them (InstallAll): its write target's, which
	// provides a module no layer lists yet, or for a module another layer provides that layer's
	// (pickSection), which an override copies.
	var sections []func(string) config.Where
	all := places{path: sel.Path}
	pv := &Preview{Selected: map[string]map[string][]string{}, SaveOnly: map[string]map[string]string{}, WritesTo: map[string]string{},
		PicksIn: map[string]string{}, Inherited: map[string]map[config.Where]map[string]string{}}
	for _, si := range spec.Scopes {
		key := si.Scope.Key()
		t := c.targets[key]
		picks, err := picksAt(si.Scope, t.from())
		if err != nil {
			return nil, err
		}
		scope, err := where(si.Scope)
		if err != nil {
			return nil, err
		}
		on, ls := c.layers[key], append(slices.Clone(c.layers[key]), t.Layer)
		sections = append(sections, func(m string) config.Where { return pickSection(on, t, picks, scope, m) })
		all.layers, all.infos, pv.WritesTo[key] = append(all.layers, ls...), append(all.infos, c.infos[key]...), t.label
		pv.Inherited[key] = map[config.Where]map[string]string{}
		for _, tn := range targets {
			if inheritedFrom(on, t, tn.name) == nil {
				continue
			}
			w := pickSection(on, t, picks, scope, tn.name)
			if pv.Inherited[key][w] == nil {
				pv.Inherited[key][w] = map[string]string{}
			}
			pv.Inherited[key][w][tn.name] = t.label
		}
		for _, l := range ls {
			if l.Profile {
				pv.PicksIn[l.Name] = layerLabel(c.infos[key], l.Name)
			}
		}
	}
	changes, notes, err := applyEdits(sel, func(string) config.Where { return config.Global() }, all, spec.GlobalEdits, targets, sections)
	if err != nil {
		return nil, err
	}
	pv.Changes, pv.Notices = changes, notes
	for i, si := range spec.Scopes {
		key := si.Scope.Key()
		var mods map[string]mentionModule
		if spec.WithMentions || spec.NameMentions {
			mods = c.listedMentions(targets, key, spec)
		}
		se, err := editScope(sel, sections[i], all, si.Edits, spec.GlobalEdits, targets, mods, spec.WithMentions)
		if err != nil {
			return nil, err
		}
		pv.Changes, pv.Notices = append(pv.Changes, se.changes...), append(pv.Notices, se.notes...)
		pv.Notices = c.eng.mentionNotes(pv.Notices, si, se, targets, mods, spec)
		pv.Selected[key] = map[string][]string{}
		t := c.targets[key]
		for _, tn := range targets {
			if l := winnerAbove(c.layers[key], t.Name, tn.name); l != nil || !t.on {
				winner := ""
				if l != nil {
					winner = layerLabel(c.infos[key], l.Name)
				}
				if pv.SaveOnly[key] == nil {
					pv.SaveOnly[key] = map[string]string{}
				}
				pv.SaveOnly[key][tn.name], pv.Selected[key][tn.name] = winner, []string{}
				if l != nil {
					pv.Notices = append(pv.Notices, saveOnlyNotice(tn.name, winner, t.label))
				}
				continue
			}
			m := c.Module(tn.name)
			rulesOn := !m.Scope(key).RulesOff
			if spec.Options.Rules != nil {
				rulesOn = *spec.Options.Rules
			}
			// The entry the install writes wins: its picks are those of its section, as InstallAll's.
			x := sel.Exclusions(sections[i](tn.name), tn.name)
			out := []string{}
			for _, cc := range tn.comps {
				if !x.Excludes(cc.Kind, cc.Name) && (rulesOn || cc.Kind != config.KindRule) {
					out = append(out, cc.String())
				}
			}
			pv.Selected[key][tn.name] = out
		}
	}
	return pv, nil
}

// Place is the file an edit of module at sc writes, as the context line names it (ContextLayer):
// the write target the choices were listed for, or, when that inherits module's entry
// (ModuleScope.Inherited), the answer to the inherited-edit question (spec 2026-10-29 §3.6),
// asked through opts.AskWhere as the install asks it (placeEdit), so an AskWhere that answers a
// question once per run answers the install's with it. It is asked before the edit is known, so
// its answers say where each part of an edit goes; opts.Target.Override answers without asking.
func (c *Choices) Place(sc Scope, module string, opts Options) (ContextLayer, error) {
	key := sc.Key()
	dest := c.targets[key]
	if w := inheritedFrom(c.layers[key], dest, module); w != nil {
		sp := &scopePlan{sc: sc, layers: c.layers[key], target: dest, infos: c.infos[key]}
		t, err := c.eng.placeEdit(sp, w, nil, opts)
		if err != nil {
			return ContextLayer{}, err
		}
		dest = *t
	}
	if dest.Base {
		name := c.eng.manifestName(sc)
		if sc.Kind == target.User {
			name = profile.DefaultName
		}
		return ContextLayer{Name: name, Kind: contextBase, Path: shown(dest.Path), Label: name}, nil
	}
	q := strings.TrimPrefix(dest.Name, ledger.FromProfile(""))
	kind, _, _ := strings.Cut(q, ":")
	return ContextLayer{Name: shown(q), Kind: kind, Path: shown(dest.Path), Label: strings.TrimPrefix(dest.label, "profile ")}, nil
}

// SelectionsPath is the selections.yaml the choices were read from.
func (c *Choices) SelectionsPath() string { return c.sel.Path }
