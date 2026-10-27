package engine

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/market"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// ModuleView is a cached version's identity and contents (Plan 9 §4.3 items 1 and 2), as lola
// info, cache add and cache list show it: every text the module gives, cleaned.
type ModuleView struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Version     string        `json:"version"`
	Digest      string        `json:"digest"`
	Format      string        `json:"format"`
	Source      string        `json:"source"`
	Type        string        `json:"type"`
	Path        string        `json:"path,omitempty"`
	Heads       []HeadView    `json:"heads"`
	Skills      []string      `json:"skills"`
	Commands    []string      `json:"commands"`
	Agents      []string      `json:"agents"`
	MCPServers  []string      `json:"mcp_servers"`
	Rules       []string      `json:"rules"`
	Hooks       []module.Hook `json:"hooks"`
	Warnings    []string      `json:"warnings"`
	// Plan 7: the vars the module declares, its template files, partials and attribute files,
	// each author text, cleaned.
	Vars       []VarView `json:"vars"`
	Templates  []string  `json:"templates"`
	Partials   []string  `json:"partials"`
	Attributes []string  `json:"attributes"`
}

// maxVarText is the longest var default a view shows, as a var holds at most 4096 bytes.
const maxVarText = 4096

// VarView is a var a module declares: its name, type (string, boolean, number or list) and default.
type VarView struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Default any    `json:"default"`
	line    string
}

// Line is the var as the human view shows it: name = default.
func (v VarView) Line() string { return v.line }

// HeadView is one cached head: the ref fetched ("" for the default branch) and its version.
type HeadView struct {
	Ref    string `json:"ref"`
	Digest string `json:"digest"`
}

// NewModuleView is v's view, o being its module's record in the cache.
func NewModuleView(v store.Version, o store.Origin) ModuleView {
	m := v.Module
	mv := ModuleView{Name: m.Name, Description: untrusted.Text(m.Description, 4*untrusted.MaxName), Version: untrusted.Text(m.Version, untrusted.MaxName), Digest: v.Meta.Digest, Format: v.Meta.Format,
		Source: o.Source, Type: o.Type, Path: o.Path, Heads: []HeadView{}, Skills: []string{}, Commands: []string{}, Agents: []string{},
		MCPServers: []string{}, Rules: []string{}, Hooks: []module.Hook{}, Warnings: []string{}, Vars: []VarView{}, Templates: []string{},
		Partials: []string{}, Attributes: []string{}}
	for _, h := range o.Heads {
		mv.Heads = append(mv.Heads, HeadView{Ref: h.Ref, Digest: h.Digest})
	}
	for _, s := range m.Skills {
		mv.Skills = append(mv.Skills, s.Name)
	}
	for _, c := range m.Commands {
		mv.Commands = append(mv.Commands, c.Name)
	}
	for _, ag := range m.Agents {
		mv.Agents = append(mv.Agents, ag.Name)
	}
	for _, s := range m.MCPServers {
		mv.MCPServers = append(mv.MCPServers, s.Name)
	}
	for _, r := range m.Rules {
		mv.Rules = append(mv.Rules, r.Name)
	}
	for _, h := range m.DeclaredHooks {
		mv.Hooks = append(mv.Hooks, module.Hook{Event: h.Event, Script: untrusted.Text(h.Script, len(h.Script))})
	}
	for _, name := range slices.Sorted(maps.Keys(m.Vars)) {
		vv := VarView{Name: untrusted.Text(name, untrusted.MaxName), line: untrusted.Text(name, untrusted.MaxName) + " = " + module.VarText(m.Vars[name])}
		switch d := m.Vars[name].(type) {
		case bool:
			vv.Type, vv.Default = "boolean", d
		case int64, float64:
			vv.Type, vv.Default = "number", d
		case []string:
			items := []string{}
			for _, item := range d {
				items = append(items, untrusted.Text(item, maxVarText))
			}
			vv.Type, vv.Default = "list", items
		default: // a string
			vv.Type, vv.Default = "string", untrusted.Text(fmt.Sprint(d), maxVarText)
		}
		mv.Vars = append(mv.Vars, vv)
	}
	for _, t := range m.Templates() {
		mv.Templates = append(mv.Templates, untrusted.Text(t.File, untrusted.MaxName))
	}
	slices.Sort(mv.Templates)
	for _, p := range slices.Sorted(maps.Keys(m.Partials)) {
		mv.Partials = append(mv.Partials, untrusted.Text(p, untrusted.MaxName))
	}
	for _, f := range m.AttributeFiles() {
		mv.Attributes = append(mv.Attributes, untrusted.Text(f, untrusted.MaxName))
	}
	mv.Warnings = append(mv.Warnings, m.Warnings...)
	for _, n := range v.Meta.OthersPresent {
		// A version fresh from cache add already carries its detection warnings.
		if w := module.ShadowWarning(v.Meta.Manifest, n); !slices.Contains(mv.Warnings, w) {
			mv.Warnings = append(mv.Warnings, w)
		}
	}
	return mv
}

// What info knows of a consent an install would meet (Plan 9 §4.3 item 4, A13).
const (
	RecordedYes     = "yes"     // consent.json holds it for the current digest
	RecordedNo      = "no"      // it does not: the install asks, or needs --trust off a terminal
	RecordedUnknown = "unknown" // its key needs what info does not know: a scope and its settings file
)

// hookLines and hookLineCut bound how much of an install hook's script info shows (§4.3 item 3).
const (
	hookLines   = 5
	hookLineCut = 200
	hookRead    = 1 << 20
)

// InfoRequest is a lola info run (Plan 9 §4): the argument as install takes it, with --ref and
// --path, the scope flags (resolved without asking: §4.4), and -t. Display is the run's output
// flags, which its refusals' hints keep. Rerun is the info run, every flag kept, with extra
// arguments added: the hints of the no-scope and no-assistant sections. UserRun is the run at
// user scope, which a refusal of a project at the home directory hints. Install is the install
// the next step names (§4.3 item 5): of arg, with --ref ref and --path path when set, the run's
// other flags kept (its own --ref and --path never) and --trust= the kinds k when any.
type InfoRequest struct {
	Cwd            string
	Arg, Ref, Path string
	Scope          ScopeRequest
	Targets        []string
	Display        []string
	Rerun          func(extra ...string) string
	UserRun        string
	Install        func(arg, ref, path string, k TrustKinds) string
}

// Info is what lola info shows of a module (Plan 9 §4.3): its identity and contents, what would
// run code, and, per scope, what installing it there would do. NoScope says why no scope
// resolved (§4.4), "" when one did; NoAssistant says why a scope has no assistant to plan for, ""
// when every scope has one (R-9-23). Hints are the runs that resolve either, [] otherwise.
type Info struct {
	ModuleView
	Runs        InfoRuns      `json:"runs"`
	Install     []InfoInstall `json:"install"`
	NoScope     string        `json:"no_scope,omitempty"`
	NoAssistant string        `json:"no_assistant,omitempty"`
	Hints       []string      `json:"hints"`
}

// InfoRuns is what in the module would run code, and when (§4.3 item 3).
type InfoRuns struct {
	InstallHooks []InfoHook      `json:"install_hooks"`
	RuntimeHooks []RuntimeHook   `json:"runtime_hooks"`
	MCPServers   []InfoMCPServer `json:"mcp_servers"`
	Scripts      []InfoScript    `json:"scripts"`
}

// InfoScript is an install script that would wrap the module's install (Plan 9 §5.6), in run
// order: its event and source, its path as configured, through untrusted.Text, its sha256, and
// whether it needs trust: a project's without a recorded consent for its digest (§5.4).
type InfoScript struct {
	Event      string `json:"event"`
	Source     string `json:"source"`
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	NeedsTrust bool   `json:"needs_trust"`
	root       string
}

// Root is the canonical root of the project whose script s is, which human output shows beside its
// path (S1); "" for another source's.
func (s InfoScript) Root() string { return s.root }

// add appends each of hooks and servers that runs does not hold yet, compared by what info shows
// of them: a runtime hook's exported fields, never the settings file its consent covers, so a
// hook two scopes write to two files is shown once.
func (runs *InfoRuns) add(hooks []RuntimeHook, servers []InfoMCPServer) {
	for _, h := range hooks {
		h.settings = ""
		if !slices.ContainsFunc(runs.RuntimeHooks, func(x RuntimeHook) bool { return reflect.DeepEqual(x, h) }) {
			runs.RuntimeHooks = append(runs.RuntimeHooks, h)
		}
	}
	for _, srv := range servers {
		if !slices.ContainsFunc(runs.MCPServers, func(x InfoMCPServer) bool { return reflect.DeepEqual(x, srv) }) {
			runs.MCPServers = append(runs.MCPServers, srv)
		}
	}
}

// InfoHook is an install hook: its event, script path and sha256 (as consent keys it), and the
// script's first lines, each cut and cleaned.
type InfoHook struct {
	Event      string   `json:"event"`
	Script     string   `json:"script"`
	SHA256     string   `json:"sha256"`
	FirstLines []string `json:"first_lines"`
}

// InfoMCPServer is a module MCP server as an assistant would run it: the command line, or the URL
// it connects to.
type InfoMCPServer struct {
	Name    string   `json:"name"`
	Target  string   `json:"target"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	URL     string   `json:"url"`
}

// InfoInstall is what installing the module at one scope would do: the plan lola install -n
// prints there, the consent items it meets with whether each is recorded, and the kinds of
// trust still needed (§4.3 item 4). Scope is "" when none resolved. Command is the install of
// exactly the version shown (Digest), with --trust= the kinds every scope of the run needs: the
// run's next step; "" when there is no plan, and in every element whenever any scope's install
// would be refused. Notices are the plan's, for the human output.
type InfoInstall struct {
	Scope       string           `json:"scope"`
	Targets     []string         `json:"targets"`
	Steps       []plan.Step      `json:"steps"`
	Findings    []target.Finding `json:"findings"`
	Consent     []InfoConsent    `json:"consent"`
	TrustNeeded []string         `json:"trust_needed"`
	Digest      string           `json:"digest"`
	Command     string           `json:"command"`
	// NewProject says the install would create .lola/ at the scope's directory (R-9-24).
	NewProject bool `json:"new_project"`
	// Refusal is install -n's refusal at the scope, which info reports instead of failing:
	// installing there would refuse the same way. Absent when it plans.
	Refusal *InfoRefusal `json:"refusal,omitempty"`
	Notices []Notice     `json:"-"`
	root    string
	// runtimeHooks and mcpServers are the plan's, as consent shows them and as each assistant
	// would run them, which Runs gathers.
	runtimeHooks []RuntimeHook
	mcpServers   []InfoMCPServer
}

// Root is the project the element installs into, "" at user scope or with no scope.
func (in InfoInstall) Root() string { return in.root }

// InfoRefusal is a refusal as lola's error output carries it: the message and its hints.
type InfoRefusal struct {
	Error string   `json:"error"`
	Hints []string `json:"hints"`
}

// InfoConsent is one consent item an install would meet: its kind of trust, the item as the
// receipt names it, and RecordedYes, RecordedNo or RecordedUnknown.
type InfoConsent struct {
	Kind     string `json:"kind"`
	Item     string `json:"item"`
	Recorded string `json:"recorded"`
}

// installBy is how an install takes exactly the version info showed (§4.3 item 5, A3, R-9-11,
// R-9-U10): the argument, --ref and --path.
type installBy struct{ arg, ref, path string }

// Info resolves req's argument as install does, fetching a source or a marketplace entry into
// the cache as cache add does, and reports the version: what it holds, what would run code, and
// the plan install -n would print at each scope install would choose without asking, with the
// consent it would meet (Plan 9 §4). It runs nothing: no install hook, runtime hook, MCP server
// or script; templates render only in the plan's sandbox. Without a scope it never guesses one
// (§4.4).
func (e *Engine) Info(ctx context.Context, req InfoRequest) (_ *Info, err error) {
	req.Ref = source.RecordedRef(req.Ref) // the next step echoes a full-SHA ref lower-cased (R-9-31)
	run, done, err := e.runDir()
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, done()) }()
	v, o, by, err := e.infoVersion(ctx, req, run)
	if err != nil {
		return nil, err
	}
	info := &Info{ModuleView: NewModuleView(v, o), Runs: InfoRuns{InstallHooks: []InfoHook{}, RuntimeHooks: []RuntimeHook{}, MCPServers: []InfoMCPServer{},
		Scripts: []InfoScript{}},
		Install: []InfoInstall{}, Hints: []string{}}
	hooks, hookConsent, err := e.infoHooks(v)
	if err != nil {
		return nil, err
	}
	info.Runs.InstallHooks = hooks
	declared, err := declaredRuntimeHooks(v.Module)
	if err != nil {
		return nil, err
	}
	sreq := req.Scope
	sreq.Cwd, sreq.Interactive, sreq.Both, sreq.Command, sreq.Run = req.Cwd, false, true, "info", ""
	res, err := e.ResolveScope(sreq)
	if _, ok := errors.AsType[*noScopeError](err); ok {
		info.NoScope = "no .lola/ was found in " + untrusted.Text(req.Cwd, untrusted.MaxName) + " or above it, and config.yaml sets no default_scope"
		if canonicalRoot(req.Cwd) != canonicalRoot(e.Paths.Home) {
			info.Hints = append(info.Hints, req.Rerun("--project"))
		}
		info.Hints = append(info.Hints, req.Rerun("--user"))
		// Without a scope only your own scripts are known: config.yaml's (§4.4).
		if info.Runs.Scripts, err = e.infoScripts([]Scope{{Kind: target.User}}, false); err != nil {
			return nil, err
		}
		info.Install = append(info.Install, unplanned("", v, hookConsent, declared))
		info.Runs.MCPServers, info.Runs.RuntimeHooks = mcpServers(v.Module, e.targetIDs()), declared
		return info, nil
	}
	if err != nil {
		return nil, err
	}
	var scopes []Scope
	for _, c := range res.Scopes {
		scopes = append(scopes, c.Scope)
	}
	if info.Runs.Scripts, err = e.infoScripts(scopes, true); err != nil {
		return nil, err
	}
	pinned, commit, err := e.pinEntryRef(by, v, o, res.Scopes)
	if err != nil {
		return nil, err
	}
	by, v.Commit = pinned, cmp.Or(commit, v.Commit)
	request := InstallRequest{Arg: by.arg, Ref: by.ref, Path: by.path, Version: &VersionRef{Name: v.Name, ID: v.ID, commit: v.Commit}}
	scopeTargets, shownTargets := make([][]string, len(res.Scopes)), make([][]string, len(res.Scopes))
	for i, c := range res.Scopes {
		if len(req.Targets) > 0 {
			scopeTargets[i], shownTargets[i] = req.Targets, req.Targets
			continue
		}
		var hints []string
		if scopeTargets[i], shownTargets[i], info.NoAssistant, hints, err = e.infoTargets(c.Scope, request, req); err != nil {
			return nil, err
		}
		if info.NoAssistant != "" {
			info.Hints = hints
			for _, c := range res.Scopes {
				scripts, err := e.scriptConsent(c.Scope)
				if err != nil {
					return nil, err
				}
				in := unplanned(c.Scope.Kind.String(), v, append(slices.Clone(hookConsent), scripts...), declared)
				in.root = root(c.Scope)
				info.Install = append(info.Install, in)
			}
			info.Runs.MCPServers, info.Runs.RuntimeHooks = mcpServers(v.Module, e.targetIDs()), declared
			return info, nil
		}
	}
	for i, c := range res.Scopes {
		in, err := e.infoPlan(ctx, req, c, scopeTargets[i], shownTargets[i], request, v, hookConsent, func(k TrustKinds) string {
			// The kinds the install would need as far as known before its plan: hooks, and runtime
			// hooks the module declares (RR2).
			return req.Install(by.arg, by.ref, by.path, k|trustNeeded(unplanned("", v, hookConsent, declared).Consent))
		})
		if refusal(err) {
			in = unplanned(c.Scope.Kind.String(), v, hookConsent, declared)
			in.root, in.Targets = root(c.Scope), append([]string{}, shownTargets[i]...)
			in.Refusal = &InfoRefusal{Error: err.Error(), Hints: append([]string{}, errs.Hints(err)...)}
		} else if err != nil {
			return nil, err
		}
		info.Install = append(info.Install, in)
	}
	var refusedIDs []string
	refused := false
	for _, in := range info.Install {
		info.Runs.add(in.runtimeHooks, in.mcpServers)
		if in.Refusal != nil {
			refused, refusedIDs = true, append(refusedIDs, in.Targets...)
		}
	}
	if refused {
		// No plan rendered a refused scope's: what would run code there is what the module
		// declares (RR1, CR-T5-1).
		if len(refusedIDs) == 0 {
			refusedIDs = e.targetIDs()
		}
		info.Runs.add(declared, mcpServers(v.Module, slices.Compact(slices.Sorted(slices.Values(refusedIDs)))))
	}
	var needs TrustKinds
	for _, in := range info.Install {
		if in.Refusal != nil {
			return info, nil // no install of it works here as it is
		}
		needs |= trustNeeded(in.Consent)
	}
	for i := range info.Install {
		info.Install[i].Command = req.Install(by.arg, by.ref, by.path, needs)
	}
	return info, nil
}

// refusal reports whether err, install -n's failure at a scope, is the install refusing what it
// would do there (a typed refusal, or an error that coaches with hints), which info reports as
// the plan's (R-9-22); any other failure (an unreadable file, a lock) is info's own error.
func refusal(err error) bool {
	if err == nil {
		return false
	}
	_, conflict := errors.AsType[*plan.ConflictError](err)
	_, targets := errors.AsType[*untrustedTargetsError](err)
	_, template := errors.AsType[*TemplateError](err)
	_, script := errors.AsType[*scriptError](err)
	return conflict || targets || template || script || errors.Is(err, ErrNotTrusted) || len(errs.Hints(err)) > 0
}

// infoScripts is the install scripts that would wrap an install at scopes, in run order, each
// once (§4.3 item 3): your own from config.yaml, and a project's in its scope. A script install
// would refuse is left out when planned, since the plan of its scope reports the refusal;
// without a plan its refusal is info's.
func (e *Engine) infoScripts(scopes []Scope, planned bool) ([]InfoScript, error) {
	out := []InfoScript{}
	for _, sc := range scopes {
		scripts, err := e.installScripts(sc, Options{})
		if _, refused := errors.AsType[*scriptError](err); refused && planned {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, s := range scripts {
			is := InfoScript{Event: s.event, Source: s.source, Path: s.shown, SHA256: s.sha256, root: oneLine(s.root)}
			if s.source == ScriptProject {
				g := s.grant()
				granted, err := e.Consent.Granted(g.key, g.digest)
				if err != nil {
					return nil, err
				}
				is.NeedsTrust = !granted
			}
			if !slices.Contains(out, is) {
				out = append(out, is)
			}
		}
	}
	rank := func(s InfoScript) int { // pre-install user, project; post-install project, user
		r := 0
		if s.Source == ScriptProject {
			r = 1
		}
		if s.Event == PostInstall {
			r = 3 - r
		}
		return r
	}
	slices.SortStableFunc(out, func(a, b InfoScript) int { return cmp.Compare(rank(a), rank(b)) })
	return out, nil
}

// scriptConsent is the consent items of sc's project scripts, as the receipt names them, each
// recorded when consent.json holds the script's current digest for the project and event; none at
// user scope, or when a script would be refused (the plan reports that).
func (e *Engine) scriptConsent(sc Scope) ([]InfoConsent, error) {
	if sc.Kind != target.Project {
		return nil, nil
	}
	scripts, err := e.projectScripts(sc)
	if _, refused := errors.AsType[*scriptError](err); refused {
		return nil, nil
	}
	if err == nil {
		scripts, err = runOrder(scripts)
	}
	if err != nil {
		return nil, err
	}
	var out []InfoConsent
	for _, s := range scripts {
		g := s.grant()
		granted, err := e.Consent.Granted(g.key, g.digest)
		if err != nil {
			return nil, err
		}
		recorded := RecordedNo
		if granted {
			recorded = RecordedYes
		}
		out = append(out, InfoConsent{Kind: TrustScripts.String(), Item: s.event + " " + s.shown, Recorded: recorded})
	}
	return out, nil
}

// infoVersion resolves req's argument as install does (§4.2): a marketplace address is fetched
// through its catalog and a source fetched, each into the cache as cache add does; a cached name
// is read, or fetched at a --ref the cache has no head for. by is how an install takes exactly
// that version (§4.3 item 5, R-9-U10): a git source, and an address whose entry is a git source
// without a component selection, as that source at the commit fetched (R-9-11), which installs
// from the cache (R-9-21); anything else by its cached name at the head that holds it, which
// installs from the cache without touching the source (pinEntryRef may pin it further).
func (e *Engine) infoVersion(ctx context.Context, req InfoRequest, run string) (v store.Version, o store.Origin, by installBy, err error) {
	_, isAddr, err := market.ParseAddress(req.Arg)
	if err != nil {
		return store.Version{}, store.Origin{}, installBy{}, err
	}
	cached := false
	if module.ValidateName("module", req.Arg) == nil {
		if o, cached, err = e.Store.Origin(req.Arg); err != nil {
			return store.Version{}, store.Origin{}, installBy{}, err
		}
	}
	switch {
	case isAddr:
		var ref string
		if v, ref, _, err = e.addEntry(ctx, req.Arg, req.Ref, req.Path, req.Display); err != nil {
			return store.Version{}, store.Origin{}, installBy{}, err
		}
		by = installBy{arg: v.Name, ref: ref}
	case cached && req.Ref == "":
		if v, _, err = e.Current(req.Arg, req.Display...); err != nil {
			return store.Version{}, store.Origin{}, installBy{}, err
		}
		ref, err := e.listedRef(VersionRef{Name: v.Name, ID: v.ID})
		if err != nil {
			return store.Version{}, store.Origin{}, installBy{}, err
		}
		by = installBy{arg: v.Name, ref: ref}
	case cached:
		var ok bool
		if v, ok, err = e.Store.Head(req.Arg, req.Ref); err != nil {
			return store.Version{}, store.Origin{}, installBy{}, err
		}
		if !ok {
			if v, err = e.fetchAs(ctx, source.Spec{Kind: source.Kind(o.Type), Location: o.Source, Path: o.Path, Ref: req.Ref, Components: o.Components}, "", req.Arg, run); err != nil {
				return store.Version{}, store.Origin{}, installBy{}, err
			}
		}
		by = installBy{arg: v.Name, ref: req.Ref}
	default:
		spec, ok, err := source.Parse(req.Arg, req.Cwd)
		if err != nil {
			return store.Version{}, store.Origin{}, installBy{}, err
		} else if !ok {
			return store.Version{}, store.Origin{}, installBy{}, notASource(req.Arg, req.Display)
		}
		// A git source at a full commit SHA the cache holds for it is shown from the cache, hashed
		// again in run, as install takes it (R-9-21): a review hint names one (R-9-32).
		spec.Ref, spec.Path = req.Ref, req.Path
		if v, err = e.cachedCommit(spec, run); err != nil {
			return store.Version{}, store.Origin{}, installBy{}, err
		}
		if v.Name == "" {
			if v, err = e.AddSource(ctx, req.Cwd, req.Arg, req.Ref, req.Path, req.Display); err != nil {
				return store.Version{}, store.Origin{}, installBy{}, err
			}
		}
		by = installBy{arg: v.Name, ref: req.Ref}
	}
	if o, _, err = e.Store.Origin(v.Name); err != nil {
		return store.Version{}, store.Origin{}, installBy{}, err
	}
	switch {
	case !isAddr && !cached && o.Type == string(source.KindGit):
		by = installBy{arg: req.Arg, ref: v.Commit, path: req.Path}
	case isAddr && o.Type == string(source.KindGit) && v.Commit != "" && o.Components == nil:
		by = installBy{arg: o.Source, ref: v.Commit, path: o.Path}
	}
	return v, o, by, nil
}

// pinEntryRef is by, an install of the cached git module v by its name at a head's ref, pinned
// to that head's commit as its source (o) at the commit when a scope's manifest records the
// module at another ref (R-9-U10): the install would read that ref's head instead. commit is the
// commit v is pinned at then, "" otherwise.
func (e *Engine) pinEntryRef(by installBy, v store.Version, o store.Origin, scopes []ScopeChoice) (installBy, string, error) {
	if by.arg != v.Name || o.Type != string(source.KindGit) || o.Components != nil {
		return by, "", nil
	}
	for _, c := range scopes {
		man, err := e.InstallManifest(c.Scope, []InstallRequest{{Arg: v.Name}})
		if err != nil {
			continue // the plan of this scope reports it
		}
		if ent := man.Find(v.Name); ent != nil && ent.Ref != by.ref {
			commit, _, err := e.headCommit(v, by.ref)
			if err != nil || commit == "" {
				return by, "", err
			}
			return installBy{arg: o.Source, ref: commit, path: o.Path}, commit, nil
		}
	}
	return by, "", nil
}

// infoHooks is v's install hooks as info shows them, and the consent items of their events, each
// recorded when consent.json holds the module's current hook digest.
func (e *Engine) infoHooks(v store.Version) ([]InfoHook, []InfoConsent, error) {
	scripts, digest, err := hookScripts(v.ContentDir, v.Module.DeclaredHooks)
	if err != nil || len(scripts) == 0 {
		return []InfoHook{}, []InfoConsent{}, err
	}
	granted, err := e.Consent.Granted(v.Name, digest)
	if err != nil {
		return nil, nil, err
	}
	recorded := RecordedNo
	if granted {
		recorded = RecordedYes
	}
	hooks, consent := []InfoHook{}, []InfoConsent{}
	for _, s := range scripts {
		lines, err := firstLines(filepath.Join(v.ContentDir, s.path))
		if err != nil {
			return nil, nil, err
		}
		hooks = append(hooks, InfoHook{Event: s.Event, Script: oneLine(s.path), SHA256: s.SHA256, FirstLines: lines})
		item := shown(v.Name) + " " + s.Event
		if !slices.ContainsFunc(consent, func(c InfoConsent) bool { return c.Item == item }) {
			consent = append(consent, InfoConsent{Kind: TrustHooks.String(), Item: item, Recorded: recorded})
		}
	}
	return hooks, consent, nil
}

// firstLines is the first hookLines lines of the script at path, each cut at hookLineCut
// characters and cleaned, read from at most its first hookRead bytes.
func firstLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return readFirstLines(io.LimitReader(f, hookRead))
}

// readFirstLines is firstLines over the script's bytes in src.
func readFirstLines(src io.Reader) ([]string, error) {
	r := bufio.NewReader(src)
	lines := []string{}
	for len(lines) < hookLines {
		var b strings.Builder
		n, cut, end := 0, false, false
		for {
			c, _, err := r.ReadRune()
			if errors.Is(err, io.EOF) {
				end = true
				break
			}
			if err != nil {
				return nil, err
			}
			if c == '\n' {
				break
			}
			if n == hookLineCut {
				cut = true
				continue
			}
			b.WriteRune(c)
			n++
		}
		if end && n == 0 {
			break
		}
		line := oneLine(strings.TrimSuffix(b.String(), "\r"))
		if cut {
			line += "…"
		}
		lines = append(lines, line)
		if end {
			break
		}
	}
	return lines, nil
}

// oneLine is author text info shows on one line: cleaned, a line break included, uncut.
func oneLine(s string) string { return shown(strings.ReplaceAll(s, "\n", "\r")) }

// declaredRuntimeHooks is m's runtime hooks as it declares them, before any rendering: its
// runtime hook sets and the hooks and MCP servers in its components' frontmatter, which Claude
// Code runs, with ${CLAUDE_PLUGIN_ROOT} and its kin unexpanded. info shows them when it has no
// plan to show them rendered.
func declaredRuntimeHooks(m *module.Module) ([]RuntimeHook, error) {
	const claude = "claude-code"
	out := []RuntimeHook{}
	if !module.Supports(m.Targets, claude) {
		return out, nil
	}
	groups := func(raw []byte, component string) error {
		var events map[string][]json.RawMessage
		if err := json.Unmarshal(raw, &events); err != nil {
			return err
		}
		for _, event := range slices.Sorted(maps.Keys(events)) {
			for _, g := range events[event] {
				views, err := describeGroup(g, claude, event, component)
				if err != nil {
					return err
				}
				out = append(out, views...)
			}
		}
		return nil
	}
	for _, set := range m.RuntimeHooks {
		if module.Supports(set.Targets, claude) {
			if err := groups(set.Hooks, ""); err != nil {
				return nil, fmt.Errorf("%s: runtime hooks %s: %w", m.Name, set.Name(), err)
			}
		}
	}
	type component struct {
		kind, name string
		doc        module.Doc
		targets    []string
	}
	var comps []component
	for _, s := range m.Skills {
		comps = append(comps, component{"skill", s.Name, s.Doc, s.Targets})
	}
	for _, c := range m.Commands {
		comps = append(comps, component{"command", c.Name, c.Doc, c.Targets})
	}
	for _, a := range m.Agents {
		comps = append(comps, component{"agent", a.Name, a.Doc, a.Targets})
	}
	for _, c := range comps {
		if !module.Supports(c.targets, claude) {
			continue
		}
		label := c.kind + " " + c.name
		for _, k := range slices.Sorted(maps.Keys(c.doc.Frontmatter)) {
			raw, err := json.Marshal(c.doc.Frontmatter[k])
			if err != nil {
				return nil, fmt.Errorf("%s %s: frontmatter %s: %w", c.kind, c.name, shown(k), err)
			}
			switch {
			case strings.EqualFold(k, "hooks"):
				err = groups(raw, label)
			case c.kind == "agent" && strings.EqualFold(k, "mcpServers"):
				var items []json.RawMessage
				if err = json.Unmarshal(raw, &items); err == nil {
					items = slices.DeleteFunc(items, func(it json.RawMessage) bool { return len(it) > 0 && it[0] == '"' })
					var servers []RuntimeHook
					if servers, err = describeServers(mustJSON(items), claude, label); err == nil {
						out = append(out, servers...)
					}
				}
			}
			if err != nil {
				return nil, fmt.Errorf("%s %s: frontmatter %s: %w", c.kind, c.name, shown(k), err)
			}
		}
	}
	for i := range out {
		out[i].Status = RecordedUnknown
	}
	return out, nil
}

// mustJSON is items as a JSON list; a list of raw messages always marshals.
func mustJSON(items []json.RawMessage) []byte {
	b, _ := json.Marshal(items)
	return b
}

// unplanned is the install element of a scope info has no plan for (no scope resolved, or no
// assistant): the hooks' consent, and the module's runtime hooks as needed, their consent unknown
// without the settings file a scope and an assistant give (§4.4).
func unplanned(scope string, v store.Version, hookConsent []InfoConsent, declared []RuntimeHook) InfoInstall {
	consent := append([]InfoConsent{}, hookConsent...)
	if len(declared) > 0 {
		consent = append(consent, InfoConsent{Kind: TrustRuntimeHooks.String(), Item: shown(v.Name), Recorded: RecordedUnknown})
	}
	return InfoInstall{Scope: scope, Targets: []string{}, Steps: []plan.Step{}, Findings: []target.Finding{}, Consent: consent,
		TrustNeeded: kindNames(trustNeeded(consent)), Digest: v.Meta.Digest, Notices: []Notice{}}
}

// trustNeeded is the kinds of consent's items not recorded: what --trust= must name for an
// install to meet none of them unanswered.
func trustNeeded(consent []InfoConsent) TrustKinds {
	var k TrustKinds
	for _, c := range consent {
		if c.Recorded != RecordedYes {
			if i := slices.Index(trustKindNames(), c.Kind); i >= 0 {
				k |= TrustKinds(1) << i
			}
		}
	}
	return k
}

// kindNames is k's kinds by name, in canonical order.
func kindNames(k TrustKinds) []string {
	out := []string{}
	for _, kind := range k.List() {
		out = append(out, kind.String())
	}
	return out
}

// infoTargets is the assistants install would plan for at sc off a terminal, with no -t
// (DefaultTargets): none when the scope's manifest gives them (the plan uses those, shown), else
// the assistants set up there. With none set up, why says so, and hints are the info run with -t
// and each installed assistant (none when no assistant is installed), as install's refusal hints
// (§4.4).
func (e *Engine) infoTargets(sc Scope, r InstallRequest, req InfoRequest) (targets, shown []string, why string, hints []string, err error) {
	choice, err := e.DefaultTargets(sc, []InstallRequest{r})
	if err != nil {
		return nil, nil, "", nil, e.CoachUnresolved(err, []Scope{sc}, true, req.Display)
	}
	if len(choice.Manifest) > 0 {
		return nil, choice.Manifest, "", nil, nil
	}
	if why = choice.NoneHere(); why != "" {
		hints = []string{}
		for _, id := range choice.Installed {
			hints = append(hints, req.Rerun("-t", id))
		}
		return nil, nil, why, hints, nil
	}
	return choice.SetUp, choice.SetUp, "", nil, nil
}

// infoPlan is install -n of the version info showed (r pins it) at c for targets (none: the
// manifest's, shown), with the consent its hooks and runtime hooks would meet. A project without
// .lola/ says that install would start it there.
func (e *Engine) infoPlan(ctx context.Context, req InfoRequest, c ScopeChoice, targets, shownTargets []string, r InstallRequest, v store.Version,
	hookConsent []InfoConsent, rerun func(TrustKinds) string) (InfoInstall, error) {
	res, err := e.InstallAll(ctx, InstallSpec{Cwd: req.Cwd, Scopes: []ScopeInstall{{ScopeChoice: c, Targets: targets}}, Modules: []InstallRequest{r},
		Options: Options{Targets: req.Targets, DryRun: true, Display: req.Display, Rerun: rerun, ForceRun: rerun(0) + " --force"},
		UserRun: req.UserRun})
	if err != nil {
		return InfoInstall{}, err
	}
	in := InfoInstall{Scope: c.Scope.Kind.String(), Targets: append([]string{}, shownTargets...), Steps: res.Steps, Findings: res.Findings,
		Consent: []InfoConsent{}, Digest: v.Meta.Digest, Notices: res.Notices, root: root(c.Scope)}
	for _, m := range res.Modules {
		// A copy an install keeps runs no hook without consent, so it meets none it lacks.
		if m.Name == v.Name && len(m.Hooks) > 0 {
			for _, h := range hookConsent {
				if !m.hooksKept || h.Recorded == RecordedYes {
					in.Consent = append(in.Consent, h)
				}
			}
		}
		for _, srv := range m.MCPServers {
			for _, id := range m.Targets {
				if !slices.ContainsFunc(res.Steps, func(st plan.Step) bool {
					return st.Module == m.Name && st.Target == id && st.Kind == "mcp" && strings.HasSuffix(st.Key, "/"+srv.name)
				}) {
					continue // the assistant cannot express it: a finding says so
				}
				srvView := InfoMCPServer{Name: oneLine(srv.name), Target: id, Args: []string{}, URL: oneLine(srv.url)}
				if len(srv.argv) > 0 {
					srvView.Command = oneLine(srv.argv[0])
					for _, a := range srv.argv[1:] {
						srvView.Args = append(srvView.Args, oneLine(a))
					}
				}
				in.mcpServers = append(in.mcpServers, srvView)
			}
		}
	}
	// The project's scripts, when the install would write a copy for them to wrap.
	if slices.ContainsFunc(res.Scripts, func(sr ScriptRun) bool { return sr.Source == ScriptProject }) {
		scripts, err := e.scriptConsent(c.Scope)
		if err != nil {
			return InfoInstall{}, err
		}
		in.Consent = append(in.Consent, scripts...)
	}
	if c.Scope.Kind == target.Project && !dirExists(filepath.Join(c.Scope.ProjectDir, ".lola")) {
		in.NewProject = true
		in.Notices = append([]Notice{{Level: LevelInfo, Text: "new project at " + untrusted.Text(c.Scope.ProjectDir, untrusted.MaxName)}}, in.Notices...)
	}
	for _, m := range res.Modules {
		for _, h := range m.RuntimeHooks {
			item := shown(m.Name) + " in " + shown(h.settings)
			recorded := RecordedNo
			if h.Status == RuntimeHooksConsented {
				recorded = RecordedYes
			}
			if !slices.ContainsFunc(in.Consent, func(c InfoConsent) bool { return c.Kind == TrustRuntimeHooks.String() && c.Item == item }) {
				in.Consent = append(in.Consent, InfoConsent{Kind: TrustRuntimeHooks.String(), Item: item, Recorded: recorded})
			}
			in.runtimeHooks = append(in.runtimeHooks, h)
		}
	}
	in.TrustNeeded = kindNames(trustNeeded(in.Consent))
	return in, nil
}

// mcpServers is m's MCP servers as it declares them, for each of ids (with no plan, every
// assistant lola knows) that the server applies to: ${PLUGIN_ROOT} and its kin unexpanded, since
// they name a scope's module copy.
func mcpServers(m *module.Module, ids []string) []InfoMCPServer {
	out := []InfoMCPServer{}
	for _, s := range m.MCPServers {
		for _, id := range ids {
			if !module.Supports(m.Targets, id) || !module.Supports(s.Targets, id) {
				continue
			}
			args := []string{}
			for _, a := range s.Args {
				args = append(args, oneLine(a))
			}
			out = append(out, InfoMCPServer{Name: oneLine(s.Name), Target: id, Command: oneLine(s.Command), Args: args, URL: oneLine(s.URL)})
		}
	}
	return out
}

// targetIDs is every assistant lola knows, in registration order.
func (e *Engine) targetIDs() []string {
	var out []string
	for _, t := range e.Targets {
		out = append(out, t.ID())
	}
	return out
}
