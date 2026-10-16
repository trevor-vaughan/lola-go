// Package store manages lola's machine-wide module cache (§6.2): one directory per module
// holding index.yml and content-addressed versions, with one head per fetched ref. Names
// and version ids read back from the cache are validated before they are used in paths.
package store

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"go.yaml.in/yaml/v3"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/fsx"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Head maps a fetched ref ("" = the source's default) to its newest version id.
type Head struct {
	Ref    string `yaml:"ref"`
	Digest string `yaml:"digest"`
}

// Origin is where a stored module comes from (index.yml).
type Origin struct {
	Source string `yaml:"source"`
	Type   string `yaml:"type"`
	Path   string `yaml:"path,omitempty"`
	Heads  []Head `yaml:"heads"`
}

// Meta describes one stored version (.lola-origin); its values are those of the first fetch.
// Format, Manifest and OthersPresent describe the source before normalization.
type Meta struct {
	Format        string   `yaml:"format"`
	Manifest      string   `yaml:"manifest,omitempty"`
	OthersPresent []string `yaml:"others_present,omitempty"`
	Ref           string   `yaml:"ref"`
	Commit        string   `yaml:"commit,omitempty"`
	FetchedAt     string   `yaml:"fetched_at"`
	Digest        string   `yaml:"digest"`
}

// Cache file names.
const (
	indexFile  = "index.yml"
	originFile = ".lola-origin"
)

// idRE is the form of a version id: the first 12 hex digits of the content digest.
var idRE = regexp.MustCompile(`^[0-9a-f]{12}$`)

// checkID refuses a version id that is not one lola writes, before it reaches a path.
func checkID(name, id string) error {
	if !idRE.MatchString(id) {
		return fmt.Errorf("cache entry %s: version id %q is invalid (expected 12 hex digits)", name, id)
	}
	return nil
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

// Store is the machine-wide module store. targets are the target ids a module's per-entry
// targets may name. normalize is module.Normalize; specs replace it to break the cached copy.
type Store struct {
	dir, lock, staging string
	targets            []string
	normalize          func(dir string, m *module.Module) error
}

// New returns the store at p.Store for modules rendered to the targets ids.
func New(p paths.Paths, targets []string) *Store {
	return &Store{dir: p.Store, lock: p.StoreLock, staging: p.Staging, targets: targets, normalize: module.Normalize}
}

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

// describeOrigin names a module's origin for an error; source and path come from the cache
// index or a recorded source, so both are untrusted.
func describeOrigin(source, path string) string {
	source = untrusted.Text(source, untrusted.MaxName)
	if path == "" {
		return source
	}
	return source + " (path " + untrusted.Text(path, untrusted.MaxName) + ")"
}

// Put detects the staged content's format and reads it as a module (module.Detect), normalizes
// it (module.Normalize), stores it as a version (reusing an identical one) and points the head
// for req.Ref at it. The version's module carries the warnings detection reported.
func (s *Store) Put(req PutRequest) (Version, error) {
	m, err := module.Detect(req.Staged, req.FallbackName, s.targets)
	if err != nil {
		return Version{}, err
	}
	if err := s.normalize(req.Staged, m); err != nil {
		return Version{}, err
	}
	// The cache loads a version without format adapters (Plan 5b design §4), so the normalized
	// copy must read back as exactly what detection produced.
	cached, err := module.Load(req.Staged, req.FallbackName, s.targets)
	if err == nil && !module.SameMeaning(m, cached) {
		err = errors.New("its cached copy would not read the same as the fetched one")
	}
	if err != nil {
		return Version{}, errs.WithHints(fmt.Errorf("lola could not cache %s faithfully: %w", m.Name, err),
			"this is a bug in lola, not in the module: please report it, with the module's source")
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
			"remove the cached one first: "+errs.ShellJoin([]string{"lola", "cache", "rm", m.Name}))
	}
	vdir := filepath.Join(s.dir, m.Name, "versions", id)
	switch _, err := os.Stat(vdir); {
	case errors.Is(err, fs.ErrNotExist):
		if err := s.addVersion(req, vdir, Meta{Format: m.Format, Manifest: m.Manifest, OthersPresent: m.Shadowed,
			Ref: req.Ref, Commit: req.Commit, FetchedAt: req.Now.UTC().Format(time.RFC3339), Digest: digest}); err != nil {
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
	o.Heads = setHead(o.Heads, req.Ref, id)
	if err := writeYAML(filepath.Join(s.dir, m.Name, indexFile), o); err != nil {
		return Version{}, err
	}
	v, err := s.Load(m.Name, id)
	if err != nil {
		return Version{}, err
	}
	// A cached load runs no format adapter, so only detection saw what the adapter warned
	// about; its warnings include everything the cached load warns about.
	v.Module.Warnings = m.Warnings
	return v, nil
}

// addVersion assembles content/ and .lola-origin in staging, then renames the whole version
// into place, so a version directory is either complete or absent.
func (s *Store) addVersion(req PutRequest, vdir string, meta Meta) (err error) {
	build, err := os.MkdirTemp(s.staging, "version-*")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(build)) }()
	if err := os.Rename(req.Staged, filepath.Join(build, "content")); err != nil {
		return err
	}
	if err := writeYAML(filepath.Join(build, originFile), meta); err != nil {
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
	if err := module.ValidateName("module", name); err != nil {
		return Version{}, err
	}
	if err := checkID(name, id); err != nil {
		return Version{}, err
	}
	vdir := filepath.Join(s.dir, name, "versions", id)
	var meta Meta
	if err := readYAML(filepath.Join(vdir, originFile), &meta); err != nil {
		return Version{}, err
	}
	content := filepath.Join(vdir, "content")
	m, err := module.Load(content, name, s.targets)
	if err != nil {
		return Version{}, fmt.Errorf("cached version %s/%s: %w", name, id, err)
	}
	return Version{Name: name, ID: id, ContentDir: content, Meta: meta, Module: m}, nil
}

// Origin reads index.yml; found is false when the module is not stored.
func (s *Store) Origin(name string) (Origin, bool, error) {
	if err := module.ValidateName("module", name); err != nil {
		return Origin{}, false, err
	}
	var o Origin
	err := readYAML(filepath.Join(s.dir, name, indexFile), &o)
	if errors.Is(err, fs.ErrNotExist) {
		return Origin{}, false, nil
	}
	if err != nil {
		return Origin{}, false, err
	}
	for _, h := range o.Heads {
		if err := checkID(name, h.Digest); err != nil {
			return Origin{}, false, err
		}
	}
	return o, true, nil
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
		if _, err := os.Stat(filepath.Join(s.dir, e.Name(), indexFile)); e.IsDir() && err == nil {
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
