// Package trust keeps the trust record (Plan 6a §7): per project, the view of project.yml that
// lola last wrote or the user last trusted, in $XDG_STATE_HOME/lola/trust.json. A manifest entry
// the record covers is one lola may act on as written; any other entry came from somewhere else
// (a pull, a hand edit) and is refused until the user installs it from its source. User scope
// has no record: user.yml is the user's own.
package trust

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/gofrs/flock"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/fsx"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/paths"
)

// Version is the trust file schema version this binary writes.
const Version = 1

// maxSize bounds trust.json, which is read whole.
const maxSize = 4 << 20

// Entry is one module's view fields: everything of a manifest entry that changes what lola
// fetches or writes.
type Entry struct {
	Name    string   `json:"name"`
	Source  string   `json:"source,omitempty"`
	Ref     string   `json:"ref,omitempty"`
	Path    string   `json:"path,omitempty"`
	Commit  string   `json:"commit,omitempty"`
	Digest  string   `json:"digest,omitempty"`
	Targets []string `json:"targets,omitempty"`
	Rules   *bool    `json:"rules,omitempty"`
}

// View is a manifest's top-level targets and entries, modules sorted by name (§7.2).
type View struct {
	Targets []string `json:"targets,omitempty"`
	Modules []Entry  `json:"modules,omitempty"`
}

// EntryOf is the view of one manifest entry.
func EntryOf(e manifest.Entry) Entry {
	return Entry{Name: e.Name, Source: e.Source, Ref: e.Ref, Path: e.Path, Commit: e.Commit, Digest: e.Digest,
		Targets: slices.Clone(e.Targets), Rules: e.Rules}
}

// ViewOf is the view of a manifest.
func ViewOf(m *manifest.Manifest) View {
	v := View{Targets: slices.Clone(m.Targets)}
	for _, e := range m.Modules {
		v.Modules = append(v.Modules, EntryOf(e))
	}
	v.sort()
	return v
}

func (v *View) sort() {
	slices.SortFunc(v.Modules, func(a, b Entry) int { return strings.Compare(a.Name, b.Name) })
}

// Hash is the hex sha256 of the view's canonical JSON.
func (v View) Hash() (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	if b, err = fsx.CanonicalJSON(b); err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// Find returns the view's entry for name, or nil.
func (v View) Find(name string) *Entry {
	for i := range v.Modules {
		if v.Modules[i].Name == name {
			return &v.Modules[i]
		}
	}
	return nil
}

// Covers reports whether the view holds an entry named like e whose every view field equals e's.
func (v View) Covers(e manifest.Entry) bool {
	r := v.Find(e.Name)
	return r != nil && r.equal(EntryOf(e))
}

func (a Entry) equal(b Entry) bool {
	return a.Name == b.Name && a.Source == b.Source && a.Ref == b.Ref && a.Path == b.Path && a.Commit == b.Commit &&
		a.Digest == b.Digest && slices.Equal(a.Targets, b.Targets) && sameRules(a.Rules, b.Rules)
}

func sameRules(a, b *bool) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

// Refresh is the view r after a write by lola turned loaded into saved (§7.4): every top-level
// or module field the write changed takes its saved value, an entry the write added is recorded
// whole and one it removed is dropped. Every other field keeps its trust state, so an entry r
// does not cover stays uncovered unless the write made every field match; a field changed in an
// entry r does not hold at all is not recorded.
func Refresh(r, loaded, saved View) View {
	out := View{Targets: slices.Clone(r.Targets)}
	for _, e := range r.Modules {
		if saved.Find(e.Name) != nil || loaded.Find(e.Name) == nil {
			out.Modules = append(out.Modules, e.clone())
		}
	}
	if !slices.Equal(loaded.Targets, saved.Targets) {
		out.Targets = slices.Clone(saved.Targets)
	}
	for _, s := range saved.Modules {
		l, cur := loaded.Find(s.Name), out.Find(s.Name)
		switch {
		case l == nil && cur != nil:
			*cur = s.clone()
		case l == nil:
			out.Modules = append(out.Modules, s.clone())
		case cur != nil:
			cur.take(*l, s)
		}
	}
	out.sort()
	return out
}

// take sets each field of e that differs between l and s to s's value.
func (e *Entry) take(l, s Entry) {
	for _, f := range []struct {
		dst  *string
		l, s string
	}{{&e.Source, l.Source, s.Source}, {&e.Ref, l.Ref, s.Ref},
		{&e.Path, l.Path, s.Path}, {&e.Commit, l.Commit, s.Commit}, {&e.Digest, l.Digest, s.Digest}} {
		if f.l != f.s {
			*f.dst = f.s
		}
	}
	if !slices.Equal(l.Targets, s.Targets) {
		e.Targets = slices.Clone(s.Targets)
	}
	if !sameRules(l.Rules, s.Rules) {
		e.Rules = s.Rules
	}
}

func (e Entry) clone() Entry {
	e.Targets = slices.Clone(e.Targets)
	return e
}

// Record is one project's entry in trust.json. A project with none has an empty View, which
// covers nothing.
type Record struct {
	View View
}

type project struct {
	SHA256 string `json:"sha256"`
	View   View   `json:"view"`
}

type file struct {
	Version  int                `json:"version"`
	Projects map[string]project `json:"projects"`
}

// Store is trust.json and its lock.
type Store struct{ path, lock string }

// New returns the store at p.Trust, locked by p.TrustLock.
func New(p paths.Paths) *Store { return &Store{path: p.Trust, lock: p.TrustLock} }

// Load returns the record of the project rooted at root (an absolute path).
func (s *Store) Load(root string) (*Record, error) {
	key, err := projectKey(root)
	if err != nil {
		return nil, err
	}
	f, err := s.read()
	if err != nil {
		return nil, err
	}
	for k, p := range f.Projects {
		if paths.SameKey(k, key) {
			return &Record{View: p.View}, nil
		}
	}
	return &Record{}, nil
}

// Save records v as the view of the project rooted at root. Under the trust lock it reads the
// file again and changes only this project's record, so concurrent saves for other projects
// survive; a file it cannot read is never overwritten, nor one it would grow past maxSize.
func (s *Store) Save(root string, v View) error {
	key, err := projectKey(root)
	if err != nil {
		return err
	}
	v.sort()
	hash, err := v.Hash()
	if err != nil {
		return err
	}
	// The lock file is created only in directories read would accept (§7.1).
	for _, c := range []struct {
		path string
		dir  bool
	}{{filepath.Dir(s.path), true}, {filepath.Dir(s.lock), true}, {s.lock, false}} {
		if err := checkPath(c.path, c.dir); err != nil {
			return err
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
	f, err := s.read()
	if err != nil {
		return err
	}
	for k := range f.Projects {
		if paths.SameKey(k, key) {
			delete(f.Projects, k)
		}
	}
	f.Version = Version
	f.Projects[key] = project{SHA256: hash, View: v}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if b = append(b, '\n'); len(b) > maxSize {
		return errs.WithHints(fmt.Errorf("%s would be larger than %d bytes, the most lola reads; it keeps a record for every project lola installed into", s.path, maxSize),
			"move it aside; lola then treats every project's modules as untrusted until you install them again")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	return fsx.WriteFileAtomic(s.path, b, 0o600)
}

// projectKey is root's paths.ProjectKey; a root that is not an absolute path is refused. Only a
// project has a record: user scope never asks for one.
func projectKey(root string) (string, error) {
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("trust records are kept for project roots only, not %q", root)
	}
	return paths.ProjectKey(root)
}

// read loads trust.json with the strictness of consent.json and at most maxSize bytes; a
// missing file has no records. The file and its directory are refused when either is a
// symbolic link or writable by others (§7.1).
func (s *Store) read() (*file, error) {
	f := &file{Version: Version, Projects: map[string]project{}}
	if err := checkPath(filepath.Dir(s.path), true); err != nil {
		return nil, err
	}
	if err := checkPath(s.path, false); err != nil {
		return nil, err
	}
	fh, err := os.Open(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = fh.Close() }()
	// The file opened must be the one checked, not a link swapped in since.
	opened, err := fh.Stat()
	if err != nil {
		return nil, err
	}
	if checked, err := os.Lstat(s.path); err != nil || !os.SameFile(opened, checked) {
		return nil, errs.WithHints(errors.Join(fmt.Errorf("%s changed while lola was reading it", s.path), err),
			"run the command again once nothing else is changing it")
	}
	b, err := io.ReadAll(io.LimitReader(fh, maxSize+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxSize {
		return nil, unreadable(s.path, fmt.Errorf("it is larger than %d bytes", maxSize))
	}
	// The version is read on its own first, as manifest.Load reads project.yml's, so a newer
	// lola's file is refused for its version, not for a key this lola does not know. A version
	// up to this lola's is read, 0 included, as consent.json reads its own.
	var v struct {
		Version int `json:"version"`
	}
	if err := json.NewDecoder(bytes.NewReader(b)).Decode(&v); err != nil {
		return nil, unreadable(s.path, err)
	}
	if v.Version > Version {
		return nil, errs.WithHints(fmt.Errorf("%s was written by a newer lola (trust version %d)", s.path, v.Version), "upgrade lola")
	}
	if v.Version < 0 {
		return nil, unreadable(s.path, fmt.Errorf("version %d is not a trust file version", v.Version))
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(f); err != nil {
		return nil, unreadable(s.path, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, unreadable(s.path, errors.New("data after the JSON object"))
	}
	if f.Projects == nil {
		f.Projects = map[string]project{}
	}
	for k, p := range f.Projects {
		hash, err := p.View.Hash()
		if err != nil {
			return nil, unreadable(s.path, err)
		}
		if hash != p.SHA256 {
			return nil, unreadable(s.path, fmt.Errorf("the record of %s does not match its sha256", k))
		}
	}
	return f, nil
}

// unreadable is the refusal of a trust file lola cannot read; it is never overwritten.
func unreadable(path string, err error) error {
	return errs.WithHints(fmt.Errorf("%s: %w", path, err),
		"move it aside; lola then treats every project's modules as untrusted until you install them again")
}

// checkPath refuses path when it is a symbolic link, not a directory (dir) or regular file, or
// writable by others. A missing path is fine.
func checkPath(path string, dir bool) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	kind, replace := "a file", "replace it with a regular file"
	if dir {
		kind, replace = "a directory", "replace it with a directory"
	}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		return errs.WithHints(fmt.Errorf("%s is a symbolic link, and lola keeps trust records only in %s of your own", path, kind), replace)
	case dir && !fi.IsDir() || !dir && !fi.Mode().IsRegular():
		return errs.WithHints(fmt.Errorf("%s is not %s", path, kind), replace)
	case writableByOthers(fi):
		return errs.WithHints(fmt.Errorf("%s is writable by others, who could then trust modules for you", path),
			errs.ShellJoin([]string{"chmod", "go-w", path}))
	}
	return nil
}
