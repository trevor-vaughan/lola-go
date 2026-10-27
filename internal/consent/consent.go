// Package consent records the install hooks the user agreed to run (§9.1): one entry per
// module, keyed by the digest of its hooks, so an unchanged hook never asks again and a
// changed one always does.
package consent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/trevor-vaughan/lola-go/internal/fsx"
)

// Version is the consent file schema version this binary writes.
const Version = 1

type file struct {
	Version int               `json:"version"`
	Modules map[string]string `json:"modules"` // module name → digest of the hooks consented to
}

// Store is the consent file at one path ($XDG_STATE_HOME/lola/consent.json).
type Store struct{ path string }

// New returns the store at path.
func New(path string) *Store { return &Store{path: path} }

// load reads the file; a missing file has no consents, and a corrupt one is an error, never
// overwritten.
func (s *Store) load() (*file, error) {
	f := &file{Version: Version, Modules: map[string]string{}}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(f); err != nil {
		return nil, fmt.Errorf("%s: %w", s.path, err)
	}
	if f.Version > Version {
		return nil, fmt.Errorf("%s was written by a newer lola (consent version %d); upgrade lola", s.path, f.Version)
	}
	if f.Modules == nil {
		f.Modules = map[string]string{}
	}
	return f, nil
}

// Granted reports whether the user consented to module's hooks with this digest.
func (s *Store) Granted(module, digest string) (bool, error) {
	f, err := s.load()
	if err != nil {
		return false, err
	}
	return f.Modules[module] == digest, nil
}

// Recorded reports whether some consent to key, under any digest, is recorded.
func (s *Store) Recorded(key string) (bool, error) {
	f, err := s.load()
	if err != nil {
		return false, err
	}
	_, ok := f.Modules[key]
	return ok, nil
}

// Grant records consent to module's hooks with this digest, replacing an earlier one.
func (s *Store) Grant(module, digest string) error {
	f, err := s.load()
	if err != nil {
		return err
	}
	f.Version = Version
	f.Modules[module] = digest
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	return fsx.WriteFileAtomic(s.path, append(b, '\n'), 0o600)
}
