package consent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/gofrs/flock"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/fsx"
)

// ShownVersion is the shown record schema version this binary writes.
const ShownVersion = 1

type shownFile struct {
	Version int      `json:"version"`
	Keys    []string `json:"keys"` // sorted, each once: the hex sha256 of an item's content as shown
}

// Shown is the record of the consent items (install hooks, runtime hooks, project scripts) lola
// has shown the user in full, by the sha256 of their content, at one path
// ($XDG_STATE_HOME/lola/shown.json). A --trust consent kind grants only items it holds. It is the
// user's own state: no project or cache holds it, and losing it costs only showing items again.
// lock is the file Add holds around its read-modify-write, so adds made at once all keep.
type Shown struct{ path, lock string }

// NewShown returns the record at path, locked by lock.
func NewShown(path, lock string) *Shown { return &Shown{path: path, lock: lock} }

// validKey reports whether k is a hex sha256, as every key is.
func validKey(k string) bool {
	return len(k) == 64 && strings.Trim(k, "0123456789abcdef") == ""
}

// load reads the record, untrusted input: a missing file holds nothing; one that is not one JSON
// object, has unknown fields, a version below 1, a key that is no lowercase hex sha256 or a key
// twice is an error that says it can be deleted; one of a newer lola says to upgrade. Neither is
// ever overwritten.
func (s *Shown) load() (*shownFile, error) {
	f := &shownFile{Version: ShownVersion, Keys: []string{}}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	f.Version = 0
	if err := dec.Decode(f); err != nil {
		return nil, s.corrupt(err)
	}
	if len(bytes.TrimLeft(b[dec.InputOffset():], " \t\r\n")) > 0 {
		return nil, s.corrupt(errors.New("data after the record"))
	}
	if f.Version < 1 {
		return nil, s.corrupt(fmt.Errorf("version %d", f.Version))
	}
	if f.Version > ShownVersion {
		return nil, fmt.Errorf("%s was written by a newer lola (shown version %d); upgrade lola", s.path, f.Version)
	}
	for _, k := range f.Keys {
		if !validKey(k) {
			return nil, s.corrupt(fmt.Errorf("%q is not a sha256", k))
		}
	}
	slices.Sort(f.Keys)
	for i := 1; i < len(f.Keys); i++ {
		if f.Keys[i] == f.Keys[i-1] {
			return nil, s.corrupt(fmt.Errorf("%s is in it twice", f.Keys[i]))
		}
	}
	return f, nil
}

// corrupt is err reading the record, with the hint that deleting it is safe.
func (s *Shown) corrupt(err error) error {
	return errs.WithHints(fmt.Errorf("%s is not a record lola can read: %w", s.path, err),
		"it can be deleted safely: "+errs.ShellJoin([]string{"rm", s.path})+"; lola then shows you each hook and script again before --trust consents to it")
}

// Holds reports whether the record holds every key of keys; it holds an empty set.
func (s *Shown) Holds(keys ...string) (bool, error) {
	f, err := s.load()
	if err != nil {
		return false, err
	}
	for _, k := range keys {
		if _, ok := slices.BinarySearch(f.Keys, k); !ok {
			return false, nil
		}
	}
	return true, nil
}

// Add records keys as shown under the record's lock, writing it atomically, 0600, only when one is
// new.
func (s *Shown) Add(keys ...string) error {
	for _, k := range keys {
		if !validKey(k) {
			return fmt.Errorf("shown record: %q is not a sha256", k)
		}
	}
	if err := os.MkdirAll(filepath.Dir(s.lock), 0o700); err != nil {
		return err
	}
	fl := flock.New(s.lock)
	if err := fl.Lock(); err != nil {
		return fmt.Errorf("locking %s: %w", s.lock, err)
	}
	defer func() { _ = fl.Unlock() }()
	f, err := s.load()
	if err != nil {
		return err
	}
	n := len(f.Keys)
	f.Keys = append(f.Keys, keys...)
	slices.Sort(f.Keys)
	if f.Keys = slices.Compact(f.Keys); len(f.Keys) == n {
		return nil
	}
	f.Version = ShownVersion
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	return fsx.WriteFileAtomic(s.path, append(b, '\n'), 0o600)
}
