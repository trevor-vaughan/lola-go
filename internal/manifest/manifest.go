// Package manifest models the desired state of a scope: project.yml (committable) or user.yml.
package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"go.yaml.in/yaml/v3"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
	"github.com/trevor-vaughan/lola-go/internal/yamldoc"
)

// Version is the manifest schema version this binary writes.
const Version = 1

// Entry is one desired module. Source is kept as the user gave it (relative paths are
// relative to the project root); Targets overrides the manifest's targets for this module.
// Commit and Digest pin the version (Plan 6a §4.1); an entry without Digest is unpinned, and a
// folder entry is never pinned.
// Rules is nil unless the user turned the module's rules off (rules: false, §5.6).
type Entry struct {
	Name    string   `yaml:"name"`
	Source  string   `yaml:"source"`
	Ref     string   `yaml:"ref,omitempty"`
	Path    string   `yaml:"path,omitempty"`
	Commit  string   `yaml:"commit,omitempty"` // git: the commit the fetch that made the pin resolved
	Digest  string   `yaml:"digest,omitempty"` // store.Digest of the normalized content; git and archive only
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

// Load reads path; a missing or empty file is an empty manifest. The file may be someone
// else's (a cloned project.yml), so it is read with yamldoc's checks, and its version is read
// on its own first, so a newer lola's file is refused for its version, not for a key this lola
// does not know.
func Load(path string) (*Manifest, error) {
	m := &Manifest{Version: Version}
	d, b, err := yamldoc.Read(path, Version)
	if err != nil {
		return nil, err
	}
	root := yamldoc.Root(d)
	if root == nil {
		return m, nil
	}
	var v struct {
		Version int `yaml:"version"`
	}
	if err := root.Decode(&v); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if v.Version > Version {
		return nil, fmt.Errorf("%s was written by a newer lola (manifest version %d); upgrade lola", path, v.Version)
	}
	if err := m.decode(b); err != nil {
		return nil, errs.WithHints(fmt.Errorf("%s: %w", path, err), "fix it by hand, or restore it from version control")
	}
	return m, nil
}

// decode reads b into m, refusing unknown keys, and validates the result.
func (m *Manifest) decode(b []byte) error {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(m); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return m.validate()
}

// validate applies the field rules of Plan 6a §4.3 that need neither the file system nor git.
func (m *Manifest) validate() error {
	if err := plainTargets(m.Targets); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, e := range m.Modules {
		if err := module.ValidateName("module", e.Name); err != nil {
			return err
		}
		if seen[e.Name] {
			return errs.WithHints(fmt.Errorf("module %s is listed twice", e.Name), "keep one entry per module")
		}
		seen[e.Name] = true
		if err := e.Validate(); err != nil {
			return fmt.Errorf("module %s: %w", e.Name, err)
		}
	}
	return nil
}

// Validate checks one entry's fields and the shape of its pin (§4.1): the source.Recorded rules,
// printable targets, and a pin the source can have. A profile's entries are checked by it too.
func (e *Entry) Validate() error {
	if err := (source.Recorded{Source: e.Source, Ref: e.Ref, Path: e.Path, Commit: e.Commit, Digest: e.Digest}).Check(); err != nil {
		return err
	}
	if err := plainTargets(e.Targets); err != nil {
		return err
	}
	switch {
	case e.Commit != "" && e.Digest == "":
		return errs.WithHints(errors.New("commit without digest"), "to pin a commit by hand, write it as ref: <full commit SHA> and drop commit")
	case source.IsFullCommit(e.Ref) && (e.Commit != "" || e.Digest != "") && e.Commit != e.Ref:
		return errs.WithHints(errors.New("commit must equal ref, a full commit SHA"), "drop commit and digest, and lola pins the commit again")
	case (e.Commit != "" || e.Digest != "") && source.IsFolderForm(e.Source):
		return errs.WithHints(fmt.Errorf("%s is a folder, and folders are never pinned", untrusted.Text(e.Source, untrusted.MaxName)), "drop commit and digest")
	}
	return nil
}

// plainTargets refuses a target id that would not print as it is.
func plainTargets(ids []string) error {
	for _, id := range ids {
		if !untrusted.IsPlain(id) {
			return errors.New("targets holds control or invisible characters")
		}
	}
	return nil
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
