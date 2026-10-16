// Package store manages lola's machine-wide module store (§6.2): one directory per module
// holding origin.yml and content-addressed versions, with one head per fetched ref.
package store

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"go.yaml.in/yaml/v3"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/fsx"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/paths"
)

// Head maps a fetched ref ("" = the source's default) to its newest version id.
type Head struct {
	Ref    string `yaml:"ref"`
	Digest string `yaml:"digest"`
}

// Origin is where a stored module comes from (origin.yml).
type Origin struct {
	Source string `yaml:"source"`
	Type   string `yaml:"type"`
	Path   string `yaml:"path,omitempty"`
	Format string `yaml:"format"`
	Heads  []Head `yaml:"heads"`
}

// Meta describes one stored version (meta.yml); its values are those of the first fetch.
type Meta struct {
	Ref       string `yaml:"ref"`
	Commit    string `yaml:"commit,omitempty"`
	FetchedAt string `yaml:"fetched_at"`
	Digest    string `yaml:"digest"`
}

// Version is one stored, immutable copy of a module.
type Version struct {
	Name, ID, ContentDir string
	Meta                 Meta
	Module               *module.Module
}

// PutRequest describes freshly fetched content. Staged must be under paths.Staging.
type PutRequest struct {
	Staged, FallbackName            string
	Source, Type, Path, Ref, Commit string
	Now                             time.Time
}

// Store is the machine-wide module store.
type Store struct{ dir, lock, staging string }

// New returns the store at p.Store.
func New(p paths.Paths) *Store { return &Store{dir: p.Store, lock: p.StoreLock, staging: p.Staging} }

func (s *Store) lockStore() (func(), error) {
	if err := os.MkdirAll(filepath.Dir(s.lock), 0o700); err != nil {
		return nil, err
	}
	fl := flock.New(s.lock)
	if err := fl.Lock(); err != nil {
		return nil, fmt.Errorf("locking the cache: %w", err)
	}
	return func() { _ = fl.Unlock() }, nil
}

func describeOrigin(source, path string) string {
	if path == "" {
		return source
	}
	return source + " (path " + path + ")"
}

// Put validates the staged content as a module, stores it as a version (reusing an identical
// one) and points the head for req.Ref at it.
func (s *Store) Put(req PutRequest) (Version, error) {
	m, err := module.Load(req.Staged, req.FallbackName)
	if err != nil {
		return Version{}, err
	}
	digest, err := Digest(req.Staged)
	if err != nil {
		return Version{}, err
	}
	id := strings.TrimPrefix(digest, "sha256:")[:12]
	unlock, err := s.lockStore()
	if err != nil {
		return Version{}, err
	}
	defer unlock()

	o, found, err := s.Origin(m.Name)
	if err != nil {
		return Version{}, err
	}
	if found && (o.Source != req.Source || o.Path != req.Path) {
		return Version{}, errs.WithHints(fmt.Errorf("a different module named %q is already in your cache (from %s); this one is from %s",
			m.Name, describeOrigin(o.Source, o.Path), describeOrigin(req.Source, req.Path)),
			"remove the cached one first: lola cache rm "+m.Name)
	}
	vdir := filepath.Join(s.dir, m.Name, "versions", id)
	switch _, err := os.Stat(vdir); {
	case errors.Is(err, fs.ErrNotExist):
		if err := s.addVersion(req, vdir, digest); err != nil {
			return Version{}, err
		}
	case err != nil:
		return Version{}, err
	default: // identical content is already stored; the staged copy is redundant
		if err := os.RemoveAll(req.Staged); err != nil {
			return Version{}, err
		}
	}
	if !found {
		o = Origin{Source: req.Source, Type: req.Type, Path: req.Path}
	}
	o.Format = m.Format
	o.Heads = setHead(o.Heads, req.Ref, id)
	if err := writeYAML(filepath.Join(s.dir, m.Name, "origin.yml"), o); err != nil {
		return Version{}, err
	}
	return s.Load(m.Name, id)
}

// addVersion assembles content/ and meta.yml in staging, then renames the whole version into
// place, so a version directory is either complete or absent.
func (s *Store) addVersion(req PutRequest, vdir, digest string) (err error) {
	build, err := os.MkdirTemp(s.staging, "version-*")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(build)) }()
	if err := os.Rename(req.Staged, filepath.Join(build, "content")); err != nil {
		return err
	}
	meta := Meta{Ref: req.Ref, Commit: req.Commit, FetchedAt: req.Now.UTC().Format(time.RFC3339), Digest: digest}
	if err := writeYAML(filepath.Join(build, "meta.yml"), meta); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(vdir), 0o700); err != nil {
		return err
	}
	return os.Rename(build, vdir)
}

func setHead(heads []Head, ref, id string) []Head {
	out := []Head{{Ref: ref, Digest: id}}
	for _, h := range heads {
		if h.Ref != ref {
			out = append(out, h)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out
}

// Head returns the newest version fetched for ref.
func (s *Store) Head(name, ref string) (Version, bool, error) {
	o, found, err := s.Origin(name)
	if err != nil || !found {
		return Version{}, false, err
	}
	for _, h := range o.Heads {
		if h.Ref == ref {
			v, err := s.Load(name, h.Digest)
			return v, err == nil, err
		}
	}
	return Version{}, false, nil
}

// Load reads a stored version and re-derives its IR from content/.
func (s *Store) Load(name, id string) (Version, error) {
	vdir := filepath.Join(s.dir, name, "versions", id)
	var meta Meta
	if err := readYAML(filepath.Join(vdir, "meta.yml"), &meta); err != nil {
		return Version{}, err
	}
	content := filepath.Join(vdir, "content")
	m, err := module.Load(content, name)
	if err != nil {
		return Version{}, fmt.Errorf("cached version %s/%s: %w", name, id, err)
	}
	return Version{Name: name, ID: id, ContentDir: content, Meta: meta, Module: m}, nil
}

// Origin reads origin.yml; found is false when the module is not stored.
func (s *Store) Origin(name string) (Origin, bool, error) {
	var o Origin
	err := readYAML(filepath.Join(s.dir, name, "origin.yml"), &o)
	if errors.Is(err, fs.ErrNotExist) {
		return Origin{}, false, nil
	}
	return o, err == nil, err
}

// Names lists stored module names, sorted.
func (s *Store) Names() ([]string, error) {
	es, err := os.ReadDir(s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range es {
		if _, err := os.Stat(filepath.Join(s.dir, e.Name(), "origin.yml")); e.IsDir() && err == nil {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// Remove deletes every stored version of name; it reports whether anything was stored.
func (s *Store) Remove(name string) (bool, error) {
	if err := module.ValidateName("module", name); err != nil {
		return false, err
	}
	unlock, err := s.lockStore()
	if err != nil {
		return false, err
	}
	defer unlock()
	dir := filepath.Join(s.dir, name)
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return true, os.RemoveAll(dir)
}

func writeYAML(path string, v any) error {
	b, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	return fsx.WriteFileAtomic(path, b, 0o600)
}

func readYAML(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}
