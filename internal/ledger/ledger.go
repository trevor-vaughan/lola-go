// Package ledger models lock.json: lola's record of what it wrote (§6.4). lola owns it; it is
// deterministic (sorted, no timestamps) and gitignored.
package ledger

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/activeprofile"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/fsx"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Version is the ledger schema version this binary writes.
const Version = 1

// Artifact is one thing lola wrote. Path is relative to the target's scope root; Key is an
// RFC 6901 pointer for JSON members. When Key is an array, lola owns one element of it: a
// string is recorded as Value, any other JSON value as Member, in canonical JSON (Plan 5b
// design §7.2). SHA256 is over the file bytes or the canonical JSON of the member or element.
// Resolved is set only when Path resolves outside the root.
type Artifact struct {
	Target   string          `json:"target"`
	Kind     string          `json:"kind"`
	Path     string          `json:"path"`
	Key      string          `json:"key,omitempty"`
	Value    string          `json:"value,omitempty"`
	Member   json.RawMessage `json:"member,omitempty"`
	SHA256   string          `json:"sha256,omitempty"`
	Resolved string          `json:"resolved,omitempty"`
}

// ID identifies the destination an artifact occupies.
func (a Artifact) ID() string {
	id := a.Target + "\x00" + a.Path + "\x00" + a.Key + "\x00" + a.Value
	if a.Member != nil {
		id += "\x00" + string(a.Member)
	}
	return id
}

// Element is the array element a owns, as canonical JSON, or nil when a owns no element.
func (a Artifact) Element() []byte {
	if a.Member != nil || a.Value == "" {
		return a.Member
	}
	b, _ := json.Marshal(a.Value) // encoding a string cannot fail
	return b
}

// FileRef names a JSON file lola created, or with Key an array lola created in one. lola
// removes either once its last member leaves it.
type FileRef struct {
	Target string `json:"target"`
	Path   string `json:"path"`
	Key    string `json:"key,omitempty"`
}

// ID orders and identifies refs.
func (r FileRef) ID() string { return r.Target + "\x00" + r.Path + "\x00" + r.Key }

// Entry is one installed module. From is the layer whose entry won when lola last applied it
// (Plan 6b §5): "profile:<qualified name>", or "profile:default"; empty for the scope's manifest,
// so an entry no profile placed is written as before profiles. HooksPending says a post-install hook of the version installed
// was declined for want of consent and has not run since (Plan 6a §9.3 step 10). Targets are
// the targets the module is installed on, sorted, including one that rendered none of its
// files; an entry written before the field records none (see InstalledTargets).
type Entry struct {
	From         string `json:"from,omitempty"`
	Source       string `json:"source"`
	Ref          string `json:"ref,omitempty"`
	Path         string `json:"path,omitempty"`
	Commit       string `json:"commit,omitempty"`
	Digest       string `json:"digest"`
	HooksPending bool   `json:"hooks_pending,omitempty"`
	// ScriptsPending says the project's post-install script was declined for want of trust after
	// this copy was installed and has not run on it since (Plan 9 §5.5).
	ScriptsPending bool       `json:"scripts_pending,omitempty"`
	Targets        []string   `json:"targets,omitempty"`
	Artifacts      []Artifact `json:"artifacts"`
	// Components is the selection of the module's directory the entry was installed with, which
	// render's fetch of the version applies again (Plan 6c §4.1).
	Components *source.Components `json:"components,omitempty"`
	// RenderedWith is what a module with templates was rendered with, nil for one without.
	RenderedWith *RenderedWith `json:"rendered_with,omitempty"`
}

// RenderedWith records the inputs of a module's templates that are not its source (Plan 7
// §6.3): Profiles, the active profiles, when some template reads them (nil otherwise); Vars,
// the winning entry's var overrides as written, {} for none.
type RenderedWith struct {
	Profiles []string    `json:"profiles,omitzero"`
	Vars     module.Vars `json:"vars"`
}

// valid reports whether r is what lola writes, and reads its vars as lola wrote them: profiles a
// list of distinct qualified names, default only first, and vars by the rules of an entry's
// (module.DecodedVars). lock.json is on disk and editable, so an entry whose RenderedWith is not
// valid is read as rendered with nothing known, and re-renders.
func (r *RenderedWith) valid() bool {
	vars, ok := module.DecodedVars(r.Vars)
	if len(r.Profiles) > activeprofile.Max+1 || !ok {
		return false
	}
	r.Vars = vars
	for i, q := range r.Profiles {
		if slices.Contains(r.Profiles[:i], q) || (q != "default" || i > 0) && activeprofile.Check(q) != nil {
			return false
		}
	}
	return true
}

// InstalledTargets is the targets e is installed on, sorted: those it records, else, for an
// entry written before it recorded them, those its artifacts are on.
func (e *Entry) InstalledTargets() []string {
	if e.Targets != nil {
		return slices.Clone(e.Targets)
	}
	var out []string
	for _, a := range e.Artifacts {
		out = append(out, a.Target)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// Active is one item of a scope's active profile list (Plan 6b §5): the qualified name its
// activation resolved (activeprofile.Check) and the action that put it there.
type Active struct {
	Name string `json:"name"`
	By   string `json:"by"`
}

// The actions an active profile records.
const (
	ByOn     = "on"
	BySwitch = "switch"
)

// FromProfile is an Entry's From for a profile's qualified name (profile.Info.Qualified).
func FromProfile(qualified string) string { return "profile:" + qualified }

// Ledger is the observed state of one scope. Profiles is its active list in order, nil when
// none is active, which leaves the key out, so a ledger without profiles is written as before.
type Ledger struct {
	Version  int               `json:"version"`
	Profiles []Active          `json:"profiles,omitempty"`
	Modules  map[string]*Entry `json:"modules"`
	Created  []FileRef         `json:"created"`
}

// New returns an empty ledger.
func New() *Ledger {
	return &Ledger{Version: Version, Modules: map[string]*Entry{}, Created: []FileRef{}}
}

// Load reads path; a missing file is an empty ledger. A ledger recording a path that leaves its
// target's root, which lola never writes, is refused (*StrayError, the first of Strays). The
// engine reads lock.json with LoadKeepingStrays and refuses every Stray itself, with the targets
// it knows.
func Load(path string) (*Ledger, error) {
	l, err := LoadKeepingStrays(path)
	if err != nil {
		return nil, err
	}
	if strays := l.Strays(nil); len(strays) > 0 {
		return nil, &StrayError{File: path, Stray: strays[0]}
	}
	return l, nil
}

// LoadKeepingStrays reads path as Load does, but keeps what lola could not have written (Strays)
// instead of refusing it, for the engine to refuse or lola repair to forget. Every other refusal
// stands.
func LoadKeepingStrays(path string) (*Ledger, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return New(), nil
	}
	if err != nil {
		return nil, err
	}
	l := New()
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	dec.UseNumber() // rendered_with's vars: an int64 stays exact (module.DecodedVars)
	if err := dec.Decode(l); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if l.Version > Version {
		return nil, fmt.Errorf("%s was written by a newer lola (ledger version %d); upgrade lola", path, l.Version)
	}
	if err := l.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, e := range l.Modules {
		if e.RenderedWith != nil && !e.RenderedWith.valid() {
			e.RenderedWith = nil
		}
	}
	l.normalize()
	return l, nil
}

// restoreHint coaches a lock.json lola could not have written and lola repair cannot fix. lola
// refuses a lock.json git tracks, so version control is never the way back.
const restoreHint = "restore lock.json from a backup, or remove the module's entry from it and reinstall the module"

// The kinds of Stray.
const (
	StrayArtifact = "artifact" // an item of a module's artifacts
	StrayTarget   = "target"   // an item of a module's targets
	StrayCreated  = "created"  // an item of the created list
)

// Stray is an item of lock.json lola could not have written, which every command refuses and
// lola repair forgets: an artifact or created item whose path leaves its target's root (Outside;
// lola writes only paths fsx.Resolve accepted, Plan 6a §4.3), or an artifact, created item or
// item of a module's targets on a target lola does not know (Unknown). Module is "" for an item
// of the created list; Path is "" for a target. id is the artifact's or created item's ID, which
// Forget matches.
type Stray struct {
	Kind, Module, Target, Path, Key string
	Outside, Unknown                bool
	id                              string
}

// String names s as lola repair's list and every refusal do, each untrusted value quoted:
// module kit: artifact "../x.md" on target "claude-code"; module kit: target "bogus";
// created "../settings.json" key "/hooks" on target "claude-code".
func (s Stray) String() string {
	key := ""
	if s.Key != "" {
		key = fmt.Sprintf(" key %q", s.Key)
	}
	switch s.Kind {
	case StrayTarget:
		return fmt.Sprintf("module %s: target %q", s.Module, s.Target)
	case StrayCreated:
		return fmt.Sprintf("created %q%s on target %q", s.Path, key, s.Target)
	}
	return fmt.Sprintf("module %s: artifact %q%s on target %q", s.Module, s.Path, key, s.Target)
}

// StrayError refuses a lock.json, File, that records Stray: Load's refusal of a path that leaves
// its target's root, and the engine's of a target lola does not know.
type StrayError struct {
	File  string
	Stray Stray
}

func (e *StrayError) Error() string {
	switch {
	case e.Stray.Outside:
		return e.File + ": " + e.Stray.String() + " leaves the target's root"
	case e.Stray.Kind == StrayTarget:
		return e.File + ": " + e.Stray.String() + " is not a target lola knows"
	}
	return e.File + ": " + e.Stray.String() + ", which is not a target lola knows"
}

// Strays lists what l records that lola could not have written, in lock.json's order: module by
// module in name order, each item of its targets that known rejects, then each artifact whose
// path is not filepath.IsLocal or whose target known rejects; then each item of the created list
// likewise. A nil known rejects no target: Load, which knows none, checks paths only.
func (l *Ledger) Strays(known func(target string) bool) []Stray {
	unknown := func(t string) bool { return known != nil && !known(t) }
	var out []Stray
	for _, name := range slices.Sorted(maps.Keys(l.Modules)) {
		e := l.Modules[name]
		for _, t := range e.Targets {
			if unknown(t) {
				out = append(out, Stray{Kind: StrayTarget, Module: name, Target: t, Unknown: true})
			}
		}
		for _, a := range e.Artifacts {
			s := Stray{Kind: StrayArtifact, Module: name, Target: a.Target, Path: a.Path, Key: a.Key,
				Outside: !filepath.IsLocal(a.Path), Unknown: unknown(a.Target), id: a.ID()}
			if s.Outside || s.Unknown {
				out = append(out, s)
			}
		}
	}
	for _, r := range l.Created {
		s := Stray{Kind: StrayCreated, Target: r.Target, Path: r.Path, Key: r.Key,
			Outside: !filepath.IsLocal(r.Path), Unknown: unknown(r.Target), id: r.ID()}
		if s.Outside || s.Unknown {
			out = append(out, s)
		}
	}
	return out
}

// Forget drops each item of drop (Strays' items) from l, then each module it took an item from
// that is left installed on no target, as plan's updateLedger drops one, and returns those
// modules in name order. A module loses each of its targets an item of drop names as unknown; an
// entry written before lock.json recorded targets first gets the targets its artifacts are on
// (InstalledTargets), so forgetting a path never uninstalls a module from its target. A module
// drop names nothing of is left as it is.
func (l *Ledger) Forget(drop []Stray) []string {
	var gone []string
	for _, name := range slices.Sorted(maps.Keys(l.Modules)) {
		if !slices.ContainsFunc(drop, func(s Stray) bool { return s.Module == name }) {
			continue
		}
		e := l.Modules[name]
		if e.Targets == nil {
			e.Targets = e.InstalledTargets()
		}
		e.Targets = slices.DeleteFunc(e.Targets, func(t string) bool {
			return slices.ContainsFunc(drop, func(s Stray) bool { return s.Module == name && s.Target == t && s.Unknown })
		})
		e.Artifacts = slices.DeleteFunc(e.Artifacts, func(a Artifact) bool {
			return slices.ContainsFunc(drop, func(s Stray) bool { return s.Kind == StrayArtifact && s.Module == name && s.id == a.ID() })
		})
		if len(e.Targets) == 0 {
			delete(l.Modules, name)
			gone = append(gone, name)
		}
	}
	l.Created = slices.DeleteFunc(l.Created, func(r FileRef) bool {
		return slices.ContainsFunc(drop, func(s Stray) bool { return s.Kind == StrayCreated && s.id == r.ID() })
	})
	return gone
}

// validate rejects what the JSON decoder accepts but the rest of lola cannot handle: null
// module entries, module names that are not safe path segments, recorded sources lola would
// not write (source.Recorded), targets that are not target names or are listed twice, artifacts
// with no target or on a target the entry does not list, and members lola could not have written. It records each member again as canonical JSON.
func (l *Ledger) validate() error {
	if err := l.validateProfiles(); err != nil {
		return errs.WithHints(err, "restore lock.json from a backup, or remove its profiles list")
	}
	for name, e := range l.Modules {
		if err := module.ValidateName("module", name); err != nil {
			return err
		}
		if e == nil {
			return fmt.Errorf("module %q has no entry", name)
		}
		if err := checkFrom(e.From); err != nil {
			return errs.WithHints(fmt.Errorf("module %s: %w", name, err), restoreHint)
		}
		// lock.json may have been planted or committed: its source and ref reach git, so they
		// pass the rules project.yml's do (Plan 6a §4.3).
		if err := (source.Recorded{Source: e.Source, Ref: e.Ref, Path: e.Path, Commit: e.Commit, Digest: e.Digest}).Check(); err != nil {
			return errs.WithHints(fmt.Errorf("module %s: %w", name, err), restoreHint)
		}
		if e.Components != nil {
			if err := e.Components.Check(); err != nil {
				return errs.WithHints(fmt.Errorf("module %s: components: %w", name, err), restoreHint)
			}
		}
		for i, t := range e.Targets {
			if err := module.ValidateName("target", t); err != nil {
				return errs.WithHints(fmt.Errorf("module %s: %w", name, err), restoreHint)
			}
			if slices.Contains(e.Targets[:i], t) {
				return errs.WithHints(fmt.Errorf("module %s: target %q is listed twice", name, t), restoreHint)
			}
		}
		for i, a := range e.Artifacts {
			if a.Target == "" {
				return fmt.Errorf("module %q has an artifact (%s) with no target", name, a.Path)
			}
			if e.Targets != nil && !slices.Contains(e.Targets, a.Target) {
				return errs.WithHints(fmt.Errorf("module %s: artifact %q is on target %q, which targets does not list", name, a.Path, a.Target), restoreHint)
			}
			if a.Member == nil {
				continue
			}
			if a.Key == "" || a.Value != "" {
				return fmt.Errorf("module %q has an artifact (%q) with a member but no key, or with a value too", name, a.Path)
			}
			// Save indents the member, and other tools may reformat or unescape it; any valid
			// member is recorded again as the canonical JSON its hash and identity use.
			c, err := fsx.CanonicalJSON(a.Member)
			if err != nil || !strings.ContainsRune("{[-0123456789tf", rune(c[0])) {
				return errs.WithHints(fmt.Errorf("module %q has an artifact (%q) whose member is not a JSON object, array, number or boolean", name, a.Path),
					restoreHint)
			}
			e.Artifacts[i].Member = c
		}
	}
	return nil
}

// validateProfiles checks the active list (Plan 6b §5): at most activeprofile.Max items, each a
// qualified name listed once, with the action lola records.
func (l *Ledger) validateProfiles() error {
	if len(l.Profiles) > activeprofile.Max {
		return fmt.Errorf("profiles lists more than %d profiles", activeprofile.Max)
	}
	for i, a := range l.Profiles {
		if err := activeprofile.Check(a.Name); err != nil {
			return fmt.Errorf("profiles: %w", err)
		}
		if slices.ContainsFunc(l.Profiles[:i], func(o Active) bool { return o.Name == a.Name }) {
			return fmt.Errorf("profiles: %s is listed twice", a.Name)
		}
		if a.By != ByOn && a.By != BySwitch {
			return fmt.Errorf("profiles: %s has by %q; lola writes on or switch", a.Name, untrusted.Text(a.By, untrusted.MaxName))
		}
	}
	return nil
}

// checkFrom refuses a From lola never writes: empty, or a profile's.
func checkFrom(from string) error {
	q, ok := strings.CutPrefix(from, "profile:")
	switch {
	case from == "":
		return nil
	case ok && (q == activeprofile.Default || activeprofile.Check(q) == nil):
		return nil
	}
	return fmt.Errorf("from %q is not a profile lola records", untrusted.Text(from, untrusted.MaxName))
}

func sortArtifacts(as []Artifact) {
	sort.Slice(as, func(i, j int) bool { return as[i].ID() < as[j].ID() })
}

// normalize sorts everything and replaces nil slices and maps with empty ones.
func (l *Ledger) normalize() {
	if l.Modules == nil {
		l.Modules = map[string]*Entry{}
	}
	for _, e := range l.Modules {
		if e.Artifacts == nil {
			e.Artifacts = []Artifact{}
		}
		sortArtifacts(e.Artifacts)
		slices.Sort(e.Targets)
	}
	if l.Created == nil {
		l.Created = []FileRef{}
	}
	sort.Slice(l.Created, func(i, j int) bool { return l.Created[i].ID() < l.Created[j].ID() })
}

// Save writes the ledger deterministically with mode 0600.
func (l *Ledger) Save(path string) error {
	l.Version = Version
	l.normalize()
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return fsx.WriteFileAtomic(path, append(b, '\n'), 0o600)
}

// Clone returns a deep copy.
func (l *Ledger) Clone() *Ledger {
	c := &Ledger{Version: l.Version, Profiles: slices.Clone(l.Profiles), Modules: make(map[string]*Entry, len(l.Modules)), Created: slices.Clone(l.Created)}
	for k, e := range l.Modules {
		ec := *e
		ec.Artifacts, ec.Targets = slices.Clone(e.Artifacts), slices.Clone(e.Targets)
		c.Modules[k] = &ec
	}
	c.normalize()
	return c
}
