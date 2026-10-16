// Package manifest models the desired state of a scope: project.yml (committable) or user.yml.
package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"go.yaml.in/yaml/v3"

	"github.com/trevor-vaughan/lola-go/internal/fsx"
	"github.com/trevor-vaughan/lola-go/internal/module"
)

// Version is the manifest schema version this binary writes.
const Version = 1

// Entry is one desired module. Source is kept as the user gave it (relative paths are
// relative to the project root); Targets overrides the manifest's targets for this module.
// Rules is nil unless the user turned the module's rules off (rules: false, §5.6).
type Entry struct {
	Name    string   `yaml:"name"`
	Source  string   `yaml:"source"`
	Ref     string   `yaml:"ref,omitempty"`
	Path    string   `yaml:"path,omitempty"`
	Targets []string `yaml:"targets,omitempty,flow"`
	Rules   *bool    `yaml:"rules,omitempty"`
}

// RulesOn reports whether the module's rules are installed; an absent entry means yes.
func (e *Entry) RulesOn() bool { return e == nil || e.Rules == nil || *e.Rules }

// Manifest is the desired state of a scope.
type Manifest struct {
	Version int      `yaml:"version"`
	Targets []string `yaml:"targets,omitempty,flow"`
	Modules []Entry  `yaml:"modules,omitempty"`
}

// Load reads path; a missing or empty file is an empty manifest.
func Load(path string) (*Manifest, error) {
	m := &Manifest{Version: Version}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(m); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if m.Version > Version {
		return nil, fmt.Errorf("%s was written by a newer lola (manifest version %d); upgrade lola", path, m.Version)
	}
	for _, e := range m.Modules {
		if err := module.ValidateName("module", e.Name); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	return m, nil
}

// Save writes the manifest, modules sorted by name, with mode 0600.
func (m *Manifest) Save(path string) error {
	m.Version = Version
	sort.Slice(m.Modules, func(i, j int) bool { return m.Modules[i].Name < m.Modules[j].Name })
	b, err := yaml.Marshal(m)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return fsx.WriteFileAtomic(path, b, 0o600)
}

// Find returns the entry for name, or nil.
func (m *Manifest) Find(name string) *Entry {
	for i := range m.Modules {
		if m.Modules[i].Name == name {
			return &m.Modules[i]
		}
	}
	return nil
}

// Upsert adds e or replaces the entry with the same name.
func (m *Manifest) Upsert(e Entry) {
	if cur := m.Find(e.Name); cur != nil {
		*cur = e
		return
	}
	m.Modules = append(m.Modules, e)
}

// Remove deletes the entry for name and reports whether it existed.
func (m *Manifest) Remove(name string) bool {
	for i := range m.Modules {
		if m.Modules[i].Name == name {
			m.Modules = append(m.Modules[:i], m.Modules[i+1:]...)
			return true
		}
	}
	return false
}
