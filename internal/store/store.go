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
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"go.yaml.in/yaml/v3"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/fsx"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Head maps a fetched ref ("" = the source's default) to its newest version id, with the
// commit the fetch that set it resolved (git only; empty in heads written before Plan 6a).
type Head struct {
	Ref    string `yaml:"ref"`
	Digest string `yaml:"digest"`
	Commit string `yaml:"commit,omitempty"`
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
	// Executables are the content's files the source marks executable, sorted; Digest reads
	// execute bits from them (Plan 6a §5.2).
	Executables []string `yaml:"executables,omitempty"`
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

// Version is one stored, immutable copy of a module. Commit is the commit the fetch or head
// that produced it resolved, when known; Meta.Commit is only the first fetch's, which may be
// another commit with the same content.
type Version struct {
	Name, ID, ContentDir string
	Commit               string
	Meta                 Meta
	Module               *module.Module
}

// PutRequest describes freshly fetched content. Staged must be under paths.Staging;
// Executables are the fetch's (source.Fetched.Executables). Digest, when set, is the digest
// the content must have (a pin): other content is refused before anything is cached.
// HeadIfAbsent sets the head for Ref only when it has none, so a pinned fetch never moves one.
// Name, when set, is the module the content must declare: content declaring another (a source
// that renamed its module) is refused with an *OtherNameError before anything is cached.
type PutRequest struct {
	Staged, FallbackName, Name      string
	Source, Type, Path, Ref, Commit string
	Executables                     []string
	Digest                          string
	HeadIfAbsent                    bool
	Now                             time.Time
}

// DigestMismatchError is Put's refusal of content whose digest is not PutRequest.Digest.
type DigestMismatchError struct{ Name, Got, Want string }

func (e *DigestMismatchError) Error() string {
	return fmt.Sprintf("%s: the content fetched has digest %s, not the pinned %s", e.Name, e.Got, untrusted.Text(e.Want, untrusted.MaxName))
}

// OtherNameError is Put's refusal of content that declares another module than
// PutRequest.Name.
type OtherNameError struct{ Want, Declared string }

func (e *OtherNameError) Error() string {
	return fmt.Sprintf("%s: its source now declares module %q", e.Want, untrusted.Text(e.Declared, untrusted.MaxName))
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
	if req.Name != "" && m.Name != req.Name {
		return Version{}, &OtherNameError{Want: req.Name, Declared: m.Name}
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
	execs := keptExecutables(req.Staged, req.Executables)
	digest, err := Digest(req.Staged, execs)
	if err != nil {
		return Version{}, err
	}
	if req.Digest != "" && digest != req.Digest {
		return Version{}, &DigestMismatchError{Name: m.Name, Got: digest, Want: req.Digest}
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
			Ref: req.Ref, Commit: req.Commit, FetchedAt: req.Now.UTC().Format(time.RFC3339), Digest: digest, Executables: execs}); err != nil {
			return Version{}, err
		}
	case err != nil:
		return Version{}, err
	default: // identical content is already stored; the staged copy is redundant
		if err := os.RemoveAll(req.Staged); err != nil {
			return Version{}, err
		}
		// A version cached before execute bits were recorded can have this id (a platform with
		// execute bits digested the same modes from the file system) and no executables in its
		// record; without them its digest can never be checked again, so record them now.
		var meta Meta
		if err := readYAML(filepath.Join(vdir, originFile), &meta); err != nil {
			return Version{}, err
		}
		if !slices.Equal(meta.Executables, execs) {
			meta.Executables = execs
			if err := writeYAML(filepath.Join(vdir, originFile), meta); err != nil {
				return Version{}, err
			}
		}
	}
	if !found {
		o = Origin{Source: req.Source, Type: req.Type, Path: req.Path}
	}
	if !req.HeadIfAbsent || !slices.ContainsFunc(o.Heads, func(h Head) bool { return h.Ref == req.Ref }) {
		o.Heads = setHead(o.Heads, Head{Ref: req.Ref, Digest: id, Commit: req.Commit})
	}
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
	v.Commit = req.Commit
	return v, nil
}

// keptExecutables is the sorted subset of execs that are regular files of the normalized
// content at dir, without the generated lola.yml, which is never executable.
func keptExecutables(dir string, execs []string) []string {
	var out []string
	for _, p := range execs {
		if p == "lola.yml" {
			continue
		}
		if fi, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(p))); err == nil && fi.Mode().IsRegular() {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
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

func setHead(heads []Head, head Head) []Head {
	out := []Head{head}
	for _, h := range heads {
		if h.Ref != head.Ref {
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
			v.Commit = h.Commit
			return v, err == nil, err
		}
	}
	return Version{}, false, nil
}

// Pinned returns the cached version whose digest is digest, read from a snapshot: its
// content/ is copied into a new directory under dir (the run's private staging) and hashed
// there with the recorded executables, and the version's module is loaded from that copy, so
// everything the caller derives from it comes from the bytes that were hashed (Plan 6a §6.1).
// found is false when the cache does not hold it. A snapshot whose digest, or whose recorded
// digest, is not digest is refused and removed.
func (s *Store) Pinned(name, digest, dir string) (v Version, found bool, err error) {
	hexits, ok := strings.CutPrefix(digest, "sha256:")
	if !ok || len(hexits) != 64 || strings.Trim(hexits, "0123456789abcdef") != "" {
		return Version{}, false, fmt.Errorf("%s: %q is not a digest", name, untrusted.Text(digest, untrusted.MaxName))
	}
	id := hexits[:12]
	if err := module.ValidateName("module", name); err != nil {
		return Version{}, false, err
	}
	vdir := filepath.Join(s.dir, name, "versions", id)
	var meta Meta
	switch err := readYAML(filepath.Join(vdir, originFile), &meta); {
	case errors.Is(err, fs.ErrNotExist):
		return Version{}, false, nil
	case err != nil:
		return Version{}, false, err
	}
	snap, err := os.MkdirTemp(dir, "snapshot-*")
	if err != nil {
		return Version{}, false, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.RemoveAll(snap))
		}
	}()
	// Content the copy or the digest refuses (a symbolic link) is no version lola cached;
	// their errors would name the snapshot, which is removed.
	got := ""
	if err := os.CopyFS(snap, os.DirFS(filepath.Join(vdir, "content"))); err == nil {
		got, _ = Digest(snap, meta.Executables)
	}
	if got != digest || meta.Digest != digest {
		return Version{}, false, errs.WithHints(fmt.Errorf("cached version %s/%s does not match its digest %s", name, id, digest), removeHint(name))
	}
	m, err := module.Load(snap, name, s.targets)
	if err != nil {
		return Version{}, false, fmt.Errorf("cached version %s/%s: %w", name, id, err)
	}
	return Version{Name: name, ID: id, ContentDir: snap, Meta: meta, Module: m}, true, nil
}

// removeHint coaches removing a cached module whose bytes or records lola refuses.
func removeHint(name string) string {
	return "remove it from your cache, then install it again from its source: " + errs.ShellJoin([]string{"lola", "cache", "rm", name})
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
	if hexits, ok := strings.CutPrefix(meta.Digest, "sha256:"+id); !ok || len(hexits) != 64-len(id) || strings.Trim(hexits, "0123456789abcdef") != "" {
		return Version{}, errs.WithHints(fmt.Errorf("cached version %s/%s records digest %q, which is not this version's",
			name, id, untrusted.Text(meta.Digest, untrusted.MaxName)), removeHint(name))
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
		if h.Commit != "" && !source.IsFullCommit(h.Commit) {
			return Origin{}, false, errs.WithHints(fmt.Errorf("cache entry %s: the head for ref %q records commit %q, which is not a full commit SHA",
				name, untrusted.Text(h.Ref, untrusted.MaxName), untrusted.Text(h.Commit, untrusted.MaxName)), removeHint(name))
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

// Has reports whether the cache holds an entry for name. It never reads index.yml, so an
// entry whose index lola refuses is still present (and removable).
func (s *Store) Has(name string) (bool, error) {
	if err := module.ValidateName("module", name); err != nil {
		return false, err
	}
	_, err := os.Lstat(filepath.Join(s.dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
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
	if found, err := s.Has(name); !found || err != nil {
		return false, err
	}
	return true, os.RemoveAll(filepath.Join(s.dir, name))
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
