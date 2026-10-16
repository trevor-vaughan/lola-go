// Package ledger models lock.json: lola's record of what it wrote (§6.4). lola owns it; it is
// deterministic (sorted, no timestamps) and gitignored.
package ledger

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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
	From         string     `json:"from,omitempty"`
	Source       string     `json:"source"`
	Ref          string     `json:"ref,omitempty"`
	Path         string     `json:"path,omitempty"`
	Commit       string     `json:"commit,omitempty"`
	Digest       string     `json:"digest"`
	HooksPending bool       `json:"hooks_pending,omitempty"`
	Targets      []string   `json:"targets,omitempty"`
	Artifacts    []Artifact `json:"artifacts"`
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

// Load reads path; a missing file is an empty ledger.
func Load(path string) (*Ledger, error) {
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
	if err := dec.Decode(l); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if l.Version > Version {
		return nil, fmt.Errorf("%s was written by a newer lola (ledger version %d); upgrade lola", path, l.Version)
	}
	if err := l.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	l.normalize()
	return l, nil
}

// restoreHint coaches a lock.json lola could not have written.
const restoreHint = "restore lock.json from a backup or version control, or remove the module's entry from it and reinstall the module"

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
