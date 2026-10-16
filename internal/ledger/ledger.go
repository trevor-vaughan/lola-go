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

	"github.com/trevor-vaughan/lola-go/internal/fsx"
	"github.com/trevor-vaughan/lola-go/internal/module"
)

// Version is the ledger schema version this binary writes.
const Version = 1

// Artifact is one thing lola wrote. Path is relative to the target's scope root; Key is an
// RFC 6901 pointer for JSON members; SHA256 is over the file bytes, the canonical JSON of the
// member, or the pointer-block text. Resolved is set only when Path resolves outside the root.
type Artifact struct {
	Target   string `json:"target"`
	Kind     string `json:"kind"`
	Path     string `json:"path"`
	Key      string `json:"key,omitempty"`
	Value    string `json:"value,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
	Resolved string `json:"resolved,omitempty"`
}

// ID identifies the destination an artifact occupies.
func (a Artifact) ID() string { return a.Target + "\x00" + a.Path + "\x00" + a.Key }

// FileRef names a shared file lola created and may delete once it is empty again.
type FileRef struct {
	Target string `json:"target"`
	Path   string `json:"path"`
}

// Entry is one installed module.
type Entry struct {
	Source    string     `json:"source"`
	Ref       string     `json:"ref,omitempty"`
	Path      string     `json:"path,omitempty"`
	Commit    string     `json:"commit,omitempty"`
	Digest    string     `json:"digest"`
	Artifacts []Artifact `json:"artifacts"`
}

// Ledger is the observed state of one scope.
type Ledger struct {
	Version int               `json:"version"`
	Modules map[string]*Entry `json:"modules"`
	Shared  []Artifact        `json:"shared"`
	Created []FileRef         `json:"created"`
}

// New returns an empty ledger.
func New() *Ledger {
	return &Ledger{Version: Version, Modules: map[string]*Entry{}, Shared: []Artifact{}, Created: []FileRef{}}
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

// validate rejects what the JSON decoder accepts but the rest of lola cannot handle: null
// module entries, module names that are not safe path segments, and artifacts with no target.
func (l *Ledger) validate() error {
	for name, e := range l.Modules {
		if err := module.ValidateName("module", name); err != nil {
			return err
		}
		if e == nil {
			return fmt.Errorf("module %q has no entry", name)
		}
		for _, a := range e.Artifacts {
			if a.Target == "" {
				return fmt.Errorf("module %q has an artifact (%s) with no target", name, a.Path)
			}
		}
	}
	for _, a := range l.Shared {
		if a.Target == "" {
			return fmt.Errorf("shared artifact %s has no target", a.Path)
		}
	}
	return nil
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
	}
	if l.Shared == nil {
		l.Shared = []Artifact{}
	}
	sortArtifacts(l.Shared)
	if l.Created == nil {
		l.Created = []FileRef{}
	}
	sort.Slice(l.Created, func(i, j int) bool {
		a, b := l.Created[i], l.Created[j]
		return a.Target+"\x00"+a.Path < b.Target+"\x00"+b.Path
	})
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
	c := &Ledger{Version: l.Version, Modules: make(map[string]*Entry, len(l.Modules)),
		Shared: slices.Clone(l.Shared), Created: slices.Clone(l.Created)}
	for k, e := range l.Modules {
		ec := *e
		ec.Artifacts = slices.Clone(e.Artifacts)
		c.Modules[k] = &ec
	}
	c.normalize()
	return c
}
