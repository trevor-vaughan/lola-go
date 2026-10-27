package engine

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/trust"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// maxLabel caps a description shown in a picker; pickers cut it again to the terminal width.
const maxLabel = 200

// ChoicesRequest asks for what the install pickers offer at scopes. Modules are the module
// arguments: each is resolved, and a source is fetched into the cache, so its components can
// be picked.
type ChoicesRequest struct {
	Cwd     string
	Scopes  []ScopeChoice
	Modules []InstallRequest
	// DryRun is the run's -n and Display its output flags, which every hint keeps (Plan 6c §11.3).
	DryRun  bool
	Display []string
}

// Choices is everything the install pickers need (Plan 5c §7), computed by the engine. Every
// author-chosen string in it has gone through untrusted.Text.
type Choices struct {
	Scopes    []ScopeChoices `json:"scopes"`
	Modules   []ModuleChoice `json:"modules"`   // every cached module, by source then name
	Requested []string       `json:"requested"` // the module arguments, resolved to names
	// Versions are the cached versions the module arguments resolved to, in argument order;
	// InstallRequest.Version pins an install to them.
	Versions []VersionRef `json:"versions"`
	Warnings []string     `json:"warnings"` // cached modules that could not be loaded
	// Resolved is, for each module argument that is a marketplace address, where it resolved, as
	// the summary shows it before Apply (Plan 6c §6.3, ruling R-6c-15).
	Resolved []string `json:"resolved"`
	// Markets are your marketplaces with a fetched catalog and the entries each offers, which the
	// module picker shows after the cache (Plan 6c §6.5).
	Markets []MarketGroup `json:"markets"`

	sel *config.Selections // as read, for Preview
}

// ScopeChoices is what one scope offers: its assistants and the targets its manifest fixes.
// FixedTargets are the manifest's top-level targets when the trust record covers them; when it
// does not, they are UntrustedTargets instead, which an install may not take unasked (Plan 6a
// §10.1), so the assistant picker asks and never preselects them.
type ScopeChoices struct {
	ScopeChoice
	Name             string      `json:"scope"` // project or user; Scope.Key() tells two project roots apart
	FixedTargets     []string    `json:"fixed_targets"`
	UntrustedTargets []string    `json:"untrusted_targets"`
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
	Scope     string   `json:"scope"` // the destination: Scope.Key()
	Installed bool     `json:"installed"`
	Digest    string   `json:"digest"`  // the installed version, "" when not installed
	Targets   []string `json:"targets"` // the assistants it is installed for
	RulesOff  bool     `json:"rules_off"`
	Excluded  []string `json:"excluded"`           // kind/name that selections.yaml excludes here
	Current   []string `json:"current_components"` // kind/name installed now
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

// InstallChoices computes what the install pickers offer: the scopes with their assistants and
// fixed targets, every cached module (and the module arguments, resolved) with its components,
// and per scope whether it is installed, which components selections.yaml excludes and which
// are installed now. It writes nothing but a source argument's fetch into the cache.
func (e *Engine) InstallChoices(ctx context.Context, req ChoicesRequest) (_ *Choices, err error) {
	var scopes []Scope
	for _, s := range req.Scopes {
		scopes = append(scopes, s.Scope)
	}
	defer func() { err = e.CoachUnresolved(err, scopes, req.DryRun, req.Display) }()
	// An install of an address may replace an unresolved entry of its name (Plan 6c §6.3), so the
	// pickers read such a manifest as that install does.
	replacing := replacingNames(req.Modules)
	swept := e.sweepRuns()
	run, err := e.runDir()
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(run)) }()
	sel, err := e.selections()
	if err != nil {
		return nil, err
	}
	ch := &Choices{Scopes: []ScopeChoices{}, Modules: []ModuleChoice{}, Requested: []string{}, Versions: []VersionRef{}, Warnings: []string{},
		Markets: []MarketGroup{}, sel: sel}
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
			if pinned, err = e.pinnedVersion(r); err != nil {
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
			lv, u, err := e.listedVersion(pinned, req.Scopes, replacing)
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
				if ref, err = e.listedRef(VersionRef{Name: v.Name, ID: v.ID}); err != nil {
					return nil, err
				}
			}
			if _, err := e.pinCommit(store.Version{Name: v.Name, ID: v.ID}, ref); err != nil {
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
		sc  ScopeChoice
		man *manifest.Manifest
		led *ledger.Ledger
		rec *trust.Record
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
		fixed, untrustedTargets := slices.Clone(man.Targets), []string{}
		if fixed == nil {
			fixed = []string{}
		}
		if rec != nil && len(fixed) > 0 && !slices.Equal(rec.View.Targets, fixed) {
			fixed, untrustedTargets = []string{}, fixed
		}
		detected := e.Detect(s.Scope)
		for i := range detected {
			detected[i].Binary = untrusted.Text(detected[i].Binary, untrusted.MaxName)
		}
		ch.Scopes = append(ch.Scopes, ScopeChoices{ScopeChoice: s, Name: s.Scope.Kind.String(), FixedTargets: fixed,
			UntrustedTargets: untrustedTargets, Assistants: detected})
		states = append(states, state{sc: s, man: man, led: led, rec: rec})
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
	// A module argument whose default head does not load is not in cached; the version it
	// resolved to is still offered.
	for name, v := range asked {
		if !slices.ContainsFunc(cached, func(c Cached) bool { return c.Version.Name == name }) {
			o, _, err := e.Store.Origin(name)
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
			if v, u, err = e.listedVersion(v, req.Scopes, replacing); err != nil {
				return nil, err
			}
			if u != nil {
				ch.Warnings = append(ch.Warnings, untrusted.Text(c.Version.Name, untrusted.MaxName)+" is not listed: "+u.why+"; "+u.fix)
				continue
			}
		}
		mc := moduleChoice(v, c.Origin)
		for _, st := range states {
			ms, err := e.moduleScope(sel, st.sc.Scope, st.man, st.led, st.rec, v)
			if err != nil {
				return nil, err
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
		base := filepath.Base(e.files(s.Scope).manifest)
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
func (e *Engine) listedVersion(def store.Version, scopes []ScopeChoice, replacing []string) (store.Version, *unlisted, error) {
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
			head, ok, err := e.Store.Head(def.Name, ent.Ref)
			if err != nil {
				return store.Version{}, nil, err
			}
			if !ok {
				which := "ref " + untrusted.Text(ent.Ref, untrusted.MaxName)
				if ent.Ref == "" {
					which = "the default branch its entry tracks"
				}
				return store.Version{}, &unlisted{why: "your cache has no version of " + which,
					fix: "install it by name, which fetches it: " + errs.ShellJoin([]string{"lola", "install", name})}, nil
			}
			rv = head
		}
		// InstallAll pins the version at the commit of the head it records as the entry's ref.
		ref := ""
		if ent != nil {
			ref = ent.Ref
		} else if ref, err = e.listedRef(VersionRef{Name: rv.Name, ID: rv.ID}); err != nil {
			return store.Version{}, nil, err
		}
		if commit, git, err := e.headCommit(store.Version{Name: rv.Name, ID: rv.ID}, ref); err != nil {
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

// pinnedVersion loads the cached version r.Version names, with the commit it was fetched at when
// the reference carries one. A version removed from the cache since the pickers listed it, or an
// invalid reference, is refused with a way forward.
func (e *Engine) pinnedVersion(r InstallRequest) (store.Version, error) {
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

// moduleScope is v's module at sc: installed or not, its exclusions, and the components
// installed now (the installed version, or v when that is no longer cached, through the
// resolver with today's selections). An entry rec, sc's trust record, does not cover is taken
// as absent: an install from its source writes it as new.
func (e *Engine) moduleScope(sel *config.Selections, sc Scope, man *manifest.Manifest, led *ledger.Ledger, rec *trust.Record, v store.Version) (ModuleScope, error) {
	x, err := exclusions(sel, sc, v.Name)
	if err != nil {
		return ModuleScope{}, err
	}
	ment := man.Find(v.Name)
	if ment != nil && rec != nil && !rec.View.Covers(*ment) {
		ment = nil // installed from its source, it is written as new (planInstallModule)
	}
	ms := ModuleScope{Scope: sc.Key(), Targets: []string{}, RulesOff: !ment.RulesOn(),
		Excluded: resolveComponents(v.Module, x, true).skipped, Current: []string{}}
	entry := led.Modules[v.Name]
	if entry == nil {
		return ms, nil
	}
	ms.Installed, ms.Digest = true, untrusted.Text(entry.Digest, untrusted.MaxName)
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
	Changes  []config.Change                // the selections.yaml entries it would write
	Notices  []Notice
}

// Preview applies spec's selection edits to a copy of selections.yaml and resolves, per scope,
// the components each of modules would install: the summary shows exactly what InstallAll
// will do. Rules are left out where they are off.
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
	var sections []config.Where
	for _, si := range spec.Scopes {
		w, err := where(si.Scope)
		if err != nil {
			return nil, err
		}
		sections = append(sections, w)
	}
	pv := &Preview{Selected: map[string]map[string][]string{}}
	changes, notes, err := applyEdits(sel, config.Global(), spec.GlobalEdits, targets, sections)
	if err != nil {
		return nil, err
	}
	pv.Changes, pv.Notices = changes, notes
	for i, si := range spec.Scopes {
		ch, n, err := applyEdits(sel, sections[i], si.Edits, targets, nil)
		if err != nil {
			return nil, err
		}
		pv.Changes, pv.Notices = append(pv.Changes, ch...), append(pv.Notices, n...)
		key := si.Scope.Key()
		pv.Selected[key] = map[string][]string{}
		for _, t := range targets {
			m := c.Module(t.name)
			rulesOn := !m.Scope(key).RulesOff
			if spec.Options.Rules != nil {
				rulesOn = *spec.Options.Rules
			}
			x := sel.Exclusions(sections[i], t.name)
			out := []string{}
			for _, cc := range t.comps {
				if !x.Excludes(cc.Kind, cc.Name) && (rulesOn || cc.Kind != config.KindRule) {
					out = append(out, cc.String())
				}
			}
			pv.Selected[key][t.name] = out
		}
	}
	return pv, nil
}

// SelectionsPath is the selections.yaml the choices were read from.
func (c *Choices) SelectionsPath() string { return c.sel.Path }
