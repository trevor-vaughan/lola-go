// Package store manages lola's machine-wide module cache (§6.2): one directory per module
// holding index.yml and content-addressed versions, with one head per fetched ref. Names
// and version ids read back from the cache are validated before they are used in paths.
package store

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
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
	// Fetched is when a fetch of Ref last reached the cache (UTC, RFC 3339); lola update fetches
	// the ref again once it is older than cache_fresh_for. A folder's head has none: a folder is
	// read on every run.
	Fetched string `yaml:"fetched,omitempty"`
}

// Origin is one source a stored module was fetched from, with the heads of its refs (index.yml
// holds one per source: the cache keys a module by its name and its source). Store.Origin returns
// the newest one as a view: see index.view.
type Origin struct {
	Source string `yaml:"source"`
	Type   string `yaml:"type"`
	Path   string `yaml:"path,omitempty"`
	// Components is the selection of the source's module directory the name was cached with
	// (source.Components): a fetch of the name again applies it.
	Components *source.Components `yaml:"components,omitempty"`
	Heads      []Head             `yaml:"heads"`
	// LastUsed is the UTC day (YYYY-MM-DD) a run last used each version, by id (Plan 9 §6.3):
	// lola cache clean --unused-for removes the versions unused that long. It is the module's,
	// not the origin's, since versions are shared by content: the index holds it once.
	LastUsed map[string]string `yaml:"-"`
	// all is every origin of the name as the index holds them, newest first, when this is
	// Store.Origin's view, which Store.Restore puts heads back into.
	all []Origin
}

// SourceKey names one source a module is fetched from: what tells the origins of a name apart.
type SourceKey struct {
	Type, Location, Path string
	Components           *source.Components
}

// From reports whether o is the origin of k's source.
func (o Origin) From(k SourceKey) bool {
	return o.Type == k.Type && source.SameLocation(source.Kind(o.Type), o.Source, k.Location) && o.Path == k.Path && o.Components.Equal(k.Components)
}

// index is a module's index.yml: the origins it was fetched from, newest first, and the day each
// of its versions was last used.
type index struct {
	Origins  []Origin          `yaml:"origins"`
	LastUsed map[string]string `yaml:"last_used,omitempty"`
}

// view is the Origin of x.Origins[i] a reader sees: its heads, then, when merged, the heads of
// the other origins after them, so a version any origin heads counts as headed (Head finds the
// newest origin's first); and the days of use.
func (x index) view(i int, merged bool) Origin {
	o := x.Origins[i]
	o.Heads = slices.Clone(o.Heads)
	if merged {
		for j, other := range x.Origins {
			if j != i {
				o.Heads = append(o.Heads, other.Heads...)
			}
		}
		o.all = slices.Clone(x.Origins)
	}
	o.LastUsed = maps.Clone(x.LastUsed)
	return o
}

// find is the position of the origin of k's source, or -1.
func (x index) find(k SourceKey) int {
	return slices.IndexFunc(x.Origins, func(o Origin) bool { return o.From(k) })
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

// dayLayout is the form of a last_used day.
const dayLayout = "2006-01-02"

// idRE is the form of a version id: the first 12 hex digits of the content digest.
var idRE = regexp.MustCompile(`^[0-9a-f]{12}$`)

// digestRE is the form of a content digest as Digest computes it.
var digestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// checkID refuses a version id that is not one lola writes, before it reaches a path.
func checkID(name, id string) error {
	if !ValidID(id) {
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
	// Fetched is when its head's ref was last fetched, set by Head only; zero when unknown.
	Fetched time.Time
	Meta    Meta
	Module  *module.Module
}

// PutRequest describes freshly fetched content. Staged must be in the run's staging directory
// (a run-* under paths.Staging), where the version is assembled beside it; Executables are the
// fetch's (source.Fetched.Executables).
// Name, when set, is the module the content must declare: content declaring another (a source
// that renamed its module) is refused with an *OtherNameError before anything is cached.
// ReplaceGoneOrigin lets the commit record this source for the name when the cache records a
// local one that no longer exists (Staged.ReplaceGoneOrigin).
type PutRequest struct {
	Staged, FallbackName, Name      string
	Source, Type, Path, Ref, Commit string
	Executables                     []string
	Now                             time.Time
	ReplaceGoneOrigin               bool
	// Components is the selection the staged content was fetched with (source.Spec.Components).
	Components *source.Components
}

// OtherNameError is Prepare's refusal of content that declares another module than
// PutRequest.Name.
type OtherNameError struct{ Want, Declared string }

func (e *OtherNameError) Error() string {
	return fmt.Sprintf("%s: its source now declares module %q", e.Want, untrusted.Text(e.Declared, untrusted.MaxName))
}

// LoadError is Prepare's refusal of content that does not read as a module (module.Detect):
// the module's own content, never lola's cache. Its message is the load's.
type LoadError struct {
	Name string
	Err  error
}

func (e *LoadError) Error() string { return e.Err.Error() }
func (e *LoadError) Unwrap() error { return e.Err }

// IndexError is Origin's refusal of a module's index.yml that the cache holds but that does not
// decode: that module's own record, not the cache as a whole, which a read failure would be.
type IndexError struct {
	Name string
	Err  error
}

func (e *IndexError) Error() string { return e.Err.Error() }
func (e *IndexError) Unwrap() error { return e.Err }

// DigestError is Pinned's refusal of a cached version whose content or recorded digest is not
// the pin, Digest, or, when Recorded, Load's of one whose record holds Digest, which is not its
// own: either stays refused until the version is removed from the cache.
type DigestError struct {
	Name, ID, Digest string
	Recorded         bool
}

func (e *DigestError) Error() string {
	if e.Recorded {
		return fmt.Sprintf("cached version %s/%s records digest %q, which is not this version's", e.Name, e.ID, untrusted.Text(e.Digest, untrusted.MaxName))
	}
	return fmt.Sprintf("cached version %s/%s does not match its digest %s", e.Name, e.ID, e.Digest)
}

// Staged is content Prepare checked in the run's staging directory, which is not in the cache
// until Commit publishes it (Plan 6a §6.3). Version is what the cached version will be, except
// that its ContentDir is the staged tree, which the run renders from: the bytes that were
// hashed. Version.Commit is the commit the fetch resolved. Version.Module.Warnings are what
// detection reported, which install shows; LoadWarnings are what a load of the cached copy
// reports, which is all a cache hit reports, so a run that renders the staged tree uses them.
//
// ReplaceGoneOrigin lets Commit drop the origins the cache records for the name that are local
// folders or archives whose location no longer exists, as a moved project's in-repo modules
// leave (Plan 6a §6.5), when this version's source is local too: Commit checks again, under the
// cache lock, that each old location is still missing. Any other origin stays.
type Staged struct {
	Version           Version
	LoadWarnings      []string
	ReplaceGoneOrigin bool
	source, typ, path string
	ref               string
	components        *source.Components
}

// Key is the source st was fetched from.
func (st Staged) Key() SourceKey {
	return SourceKey{Type: st.typ, Location: st.source, Path: st.path, Components: st.components}
}

// Index is the origin of st's source once Commit publishes st: o, the cache's origin of that
// source (found says it holds one), else a new one; with the head for the fetched ref pointed at
// st's version (with headIfAbsent, only when that ref has no head). Nothing in the cache changes.
func (st Staged) Index(o Origin, found, headIfAbsent bool) Origin {
	if !found {
		o = Origin{Source: st.source, Type: st.typ, Path: st.path, Components: st.components}
	}
	if !headIfAbsent || !slices.ContainsFunc(o.Heads, func(h Head) bool { return h.Ref == st.ref }) {
		h := Head{Ref: st.ref, Digest: st.Version.ID, Commit: st.Version.Commit, Fetched: st.Version.Meta.FetchedAt}
		// A folder is read every run and never stale (spec 2026-10-29 §4): a stamp nothing reads
		// would rewrite index.yml on every run that read it again.
		if st.typ == string(source.KindFolder) {
			h.Fetched = ""
		}
		o.Heads = setHead(o.Heads, h)
	}
	return o
}

// Store is the machine-wide module store. targets are the target ids a module's per-entry
// targets may name. normalize is module.Normalize; specs replace it to break the cached copy.
// now is the clock last_used days are read from.
type Store struct {
	dir, lock string
	targets   []string
	normalize func(dir string, m *module.Module) error
	now       func() time.Time
}

// New returns the store at p.Store for modules rendered to the targets ids, dating last_used by
// now.
func New(p paths.Paths, targets []string, now func() time.Time) *Store {
	return &Store{dir: p.Store, lock: p.StoreLock, targets: targets, normalize: module.Normalize, now: now}
}

// today is the UTC day last_used records for a use now.
func (s *Store) today() string { return s.now().UTC().Format(dayLayout) }

// Lock takes the cache lock exclusively for a caller that changes many entries in one critical
// section (lola cache clean); the returned function releases it. Its holder calls none of the
// store's methods that take the lock themselves (Commit, Used, Remove), which would wait for it.
func (s *Store) Lock() (func(), error) { return s.lockStore() }

// ValidID reports whether id is a version id lola writes, so a caller may join it into a path.
func ValidID(id string) bool { return idRE.MatchString(id) }

// Restore puts back in name's index.yml, for lola cache clean holding the cache lock (Lock), the
// heads and last_used that before (Store.Origin's view) recorded for the versions ids: Cleaned
// dropped them, and the clean failed before it removed those versions (Plan 9 A28). Each head
// goes back to the origin it came from.
func (s *Store) Restore(name string, before Origin, ids []string) error {
	idx, found, err := s.readIndex(name)
	if err != nil || !found {
		return err
	}
	for _, b := range before.all {
		i := idx.find(SourceKey{Type: b.Type, Location: b.Source, Path: b.Path, Components: b.Components})
		if i < 0 {
			continue
		}
		for _, h := range b.Heads {
			if slices.Contains(ids, h.Digest) && !slices.ContainsFunc(idx.Origins[i].Heads, func(c Head) bool { return c.Ref == h.Ref }) {
				idx.Origins[i].Heads = setHead(idx.Origins[i].Heads, h)
			}
		}
	}
	for _, id := range ids {
		if day, ok := before.LastUsed[id]; ok {
			if idx.LastUsed == nil {
				idx.LastUsed = map[string]string{}
			}
			idx.LastUsed[id] = day
		}
	}
	return writeYAML(filepath.Join(s.dir, name, indexFile), idx)
}

// Cleaned records in name's index.yml what lola cache clean, holding the cache lock (Lock), did:
// it removed the versions ids, so the heads that point to them go, with their last_used; and it
// saw every version still cached, so each one without a last_used is dated today (Plan 9 §6.3).
// It writes index.yml only when that changes it, and never for a module the cache no longer holds.
func (s *Store) Cleaned(name string, ids []string) error {
	idx, found, err := s.readIndex(name)
	if err != nil || !found {
		return err
	}
	changed := false
	for i := range idx.Origins {
		heads := slices.DeleteFunc(slices.Clone(idx.Origins[i].Heads), func(h Head) bool { return slices.Contains(ids, h.Digest) })
		if len(heads) != len(idx.Origins[i].Heads) {
			idx.Origins[i].Heads, changed = heads, true
		}
	}
	for _, id := range ids {
		if _, ok := idx.LastUsed[id]; ok {
			delete(idx.LastUsed, id)
			changed = true
		}
	}
	vs, err := os.ReadDir(filepath.Join(s.dir, name, "versions"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, v := range vs {
		if !ValidID(v.Name()) || slices.Contains(ids, v.Name()) || idx.LastUsed[v.Name()] != "" {
			continue
		}
		if idx.LastUsed == nil {
			idx.LastUsed = map[string]string{}
		}
		idx.LastUsed[v.Name()], changed = s.today(), true
	}
	if !changed {
		return nil
	}
	return writeYAML(filepath.Join(s.dir, name, indexFile), idx)
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

// Put caches freshly fetched content and points the head for req.Ref at it: Prepare, then
// Commit. The staged content is removed once it is cached.
func (s *Store) Put(req PutRequest) (Version, error) {
	st, err := s.Prepare(req)
	if err != nil {
		return Version{}, err
	}
	v, err := s.Commit(st, false)
	if err != nil {
		return Version{}, err
	}
	return v, os.RemoveAll(req.Staged)
}

// NoModuleAt names the source the user gave (and its --path) in an ErrNoModule from loading dir,
// in place of lola's staging directory, which module.Detect no longer prints, and says what to do
// when dir holds skills directly: a --path naming a skills/ directory is a SkillsDirError, whose
// caller hints the same run with --path at its parent (none for the top), keeping the user's other
// flags; any other directory of skills is the source's to change.
func NoModuleAt(source, p, dir string, err error) error {
	at := untrusted.Text(source, untrusted.MaxName)
	if p != "" {
		at += " --path " + untrusted.Text(p, untrusted.MaxName)
	}
	err = fmt.Errorf("%s: %w", at, err)
	if !module.DirOfSkills(dir) {
		return err
	}
	const under = "that directory holds skills; a module root holds them under skills/: "
	clean := path.Clean(filepath.ToSlash(p))
	switch {
	case p == "":
		return errs.WithHints(err, "the source holds skills directly, not under skills/: move them into a skills/ directory in the source")
	case path.Base(clean) != "skills":
		return errs.WithHints(err, under+"move them into a skills/ directory in the source, then point --path at its parent")
	}
	parent := path.Dir(clean)
	if parent == "." {
		parent = ""
	}
	return &SkillsDirError{Source: source, Parent: parent, err: err}
}

// SkillsDirError is a --path that names the skills/ directory of Source, which is no module root:
// the module is Parent, the directory above it ("" for the source's top). Its caller hints the run
// that names Parent instead.
type SkillsDirError struct {
	Source, Parent string
	err            error
}

func (e *SkillsDirError) Error() string {
	return e.err.Error() + "; that directory holds skills, and a module root holds them under skills/"
}

func (e *SkillsDirError) Unwrap() error { return e.err }

// Prepare detects the staged content's format and reads it as a module (module.Detect),
// normalizes it in place (module.Normalize), checks that the cached copy would read the same and
// digests it. Nothing in the cache changes and no lock is taken: untrusted content stays in the
// run's staging until Commit. The version's module carries the warnings detection reported.
func (s *Store) Prepare(req PutRequest) (Staged, error) {
	m, err := module.Detect(req.Staged, req.FallbackName, s.targets)
	if errors.Is(err, module.ErrNoModule) {
		err = NoModuleAt(req.Source, req.Path, req.Staged, err)
	}
	if err != nil {
		return Staged{}, &LoadError{Name: cmp.Or(req.Name, req.FallbackName), Err: err}
	}
	if req.Name != "" && m.Name != req.Name {
		return Staged{}, &OtherNameError{Want: req.Name, Declared: m.Name}
	}
	if err := s.normalize(req.Staged, m); err != nil {
		return Staged{}, err
	}
	// The cache loads a version without format adapters (Plan 5b design §4), so the normalized
	// copy must read back as exactly what detection produced.
	cached, err := module.Load(req.Staged, req.FallbackName, s.targets)
	if err == nil && !module.SameMeaning(m, cached) {
		err = errors.New("its cached copy would not read the same as the fetched one")
	}
	if err != nil {
		return Staged{}, errs.WithHints(fmt.Errorf("lola could not cache %s faithfully: %w", m.Name, err),
			"this is a bug in lola, not in the module: please report it, with the module's source")
	}
	execs := keptExecutables(req.Staged, req.Executables)
	digest, err := Digest(req.Staged, execs)
	if err != nil {
		return Staged{}, err
	}
	// A cached load runs no format adapter, so only detection saw what the adapter warned
	// about; its warnings include everything the cached load warns about.
	loadWarnings := cached.Warnings
	cached.Warnings = m.Warnings
	ref := source.RecordedRef(req.Ref)
	meta := Meta{Format: m.Format, Manifest: m.Manifest, OthersPresent: m.Shadowed, Ref: ref, Commit: req.Commit,
		FetchedAt: req.Now.UTC().Format(time.RFC3339), Digest: digest, Executables: execs}
	v := Version{Name: m.Name, ID: strings.TrimPrefix(digest, "sha256:")[:12], ContentDir: req.Staged, Commit: req.Commit, Meta: meta, Module: cached}
	return Staged{Version: v, LoadWarnings: loadWarnings, ReplaceGoneOrigin: req.ReplaceGoneOrigin, source: req.Source, typ: req.Type, path: req.Path,
		ref: ref, components: req.Components.Clone()}, nil
}

// Commit publishes a prepared version under the cache lock: it records its source as an origin of
// the name, or updates the one the cache holds for that source (checked again here, since another
// process may have cached the name since the caller checked), and makes it the newest, which a
// reader that names the module and no source gets. It copies the staged tree into a new version
// (reusing an identical one) and sets the head for the fetched ref. With headIfAbsent the head
// is set only when that ref has none, so a pinned fetch never moves one. The staged tree is
// left in place for the run, which keeps rendering from it; the returned version is the
// cache's.
func (s *Store) Commit(st Staged, headIfAbsent bool) (Version, error) {
	sv := st.Version
	unlock, err := s.lockStore()
	if err != nil {
		return Version{}, err
	}
	defer unlock()

	idx, _, err := s.readIndex(sv.Name)
	if err != nil {
		return Version{}, err
	}
	i := idx.find(st.Key())
	var o Origin
	if i >= 0 {
		o = idx.Origins[i]
		idx.Origins = slices.Delete(idx.Origins, i, i+1)
	}
	if st.ReplaceGoneOrigin && source.IsLocal(source.Kind(st.typ), st.source) {
		idx.Origins = slices.DeleteFunc(idx.Origins, Origin.LocalGone)
	}
	idx.Origins = slices.Insert(idx.Origins, 0, st.Index(o, i >= 0, headIfAbsent))
	vdir := filepath.Join(s.dir, sv.Name, "versions", sv.ID)
	switch _, err := os.Stat(vdir); {
	case errors.Is(err, fs.ErrNotExist):
		if err := addVersion(sv.ContentDir, vdir, sv.Meta); err != nil {
			return Version{}, err
		}
	case err != nil:
		return Version{}, err
	default: // identical content is already stored
		// A version cached before execute bits were recorded can have this id (a platform with
		// execute bits digested the same modes from the file system) and no executables in its
		// record; without them its digest can never be checked again, so record them now.
		var meta Meta
		if err := readYAML(filepath.Join(vdir, originFile), &meta); err != nil {
			return Version{}, err
		}
		if !slices.Equal(meta.Executables, sv.Meta.Executables) {
			meta.Executables = sv.Meta.Executables
			if err := writeYAML(filepath.Join(vdir, originFile), meta); err != nil {
				return Version{}, err
			}
		}
	}
	if idx.LastUsed == nil {
		idx.LastUsed = map[string]string{}
	}
	idx.LastUsed[sv.ID] = s.today()
	if err := writeYAML(filepath.Join(s.dir, sv.Name, indexFile), idx); err != nil {
		return Version{}, err
	}
	v, err := s.Load(sv.Name, sv.ID)
	if err != nil {
		return Version{}, err
	}
	v.Module.Warnings = sv.Module.Warnings
	v.Commit = sv.Commit
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

// addVersion assembles a copy of the staged tree as content/ and .lola-origin beside it, in the
// run's staging directory, then renames the whole version into place, so a version directory is
// either complete or absent.
func addVersion(staged, vdir string, meta Meta) (err error) {
	build, err := os.MkdirTemp(filepath.Dir(staged), "version-*")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(build)) }()
	if err := os.CopyFS(filepath.Join(build, "content"), os.DirFS(staged)); err != nil {
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

// Head returns the newest version fetched for ref from the newest origin of name, else from
// another. A ref that is a full commit SHA, in any case, is served only by a head that records
// that commit as the one its fetch resolved (R-9-27). A caller that knows the source it means
// asks HeadFor.
func (s *Store) Head(name, ref string) (Version, bool, error) {
	o, found, err := s.Origin(name)
	return s.headOf(name, o, found, ref, err)
}

// HeadFor is Head for the origin of the source k names alone.
func (s *Store) HeadFor(name, ref string, k SourceKey) (Version, bool, error) {
	o, found, err := s.OriginFor(name, k)
	return s.headOf(name, o, found, ref, err)
}

func (s *Store) headOf(name string, o Origin, found bool, ref string, err error) (Version, bool, error) {
	if err != nil || !found {
		return Version{}, false, err
	}
	h, ok := o.Head(ref)
	if !ok {
		return Version{}, false, nil
	}
	v, err := s.Load(name, h.Digest)
	v.Commit = h.Commit
	v.Fetched, _ = time.Parse(time.RFC3339, h.Fetched) // none or unreadable: zero, which is stale
	return v, err == nil, err
}

// Head is o's head for ref, matched as Store.Head matches it.
func (o Origin) Head(ref string) (Head, bool) {
	commit, isCommit := source.CommitRef(ref)
	for _, h := range o.Heads {
		if isCommit && strings.ToLower(h.Ref) == commit && h.Commit == commit || !isCommit && h.Ref == ref {
			return h, true
		}
	}
	return Head{}, false
}

// AtCommit is the digest of name's version whose recorded commit is commit (the commit a fetch
// resolved, which a head or the version itself records; never a head's ref), "" when none. A
// version whose record holds no well-formed digest with its id is none.
func (s *Store) AtCommit(name, commit string) (string, error) {
	o, found, err := s.Origin(name)
	if err != nil || !found || commit == "" {
		return "", err
	}
	ids := []string{}
	for _, h := range o.Heads {
		if h.Commit == commit {
			ids = append(ids, h.Digest)
		}
	}
	entries, err := os.ReadDir(filepath.Join(s.dir, name, "versions"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	for _, ent := range entries {
		ids = append(ids, ent.Name())
	}
	for _, id := range ids {
		if checkID(name, id) != nil {
			continue
		}
		var meta Meta
		if readYAML(filepath.Join(s.dir, name, "versions", id, originFile), &meta) != nil {
			continue // a record lola cannot read names no commit: the source is fetched instead
		}
		if !digestRE.MatchString(meta.Digest) || meta.Digest[len("sha256:"):][:12] != id {
			continue // a record that names no digest of this version: the source is fetched instead
		}
		if meta.Commit == commit || slices.ContainsFunc(o.Heads, func(h Head) bool { return h.Commit == commit && h.Digest == id }) {
			return meta.Digest, nil
		}
	}
	return "", nil
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
		return Version{}, false, errs.WithHints(&DigestError{Name: name, ID: id, Digest: digest}, removeHint(name))
	}
	m, err := module.Load(snap, name, s.targets)
	if err != nil {
		return Version{}, false, fmt.Errorf("cached version %s/%s: %w", name, id, err)
	}
	// Recording the use is best effort: a cache whose index.yml cannot be written still serves
	// its versions as it did before last_used (Plan 9 §6.3).
	_ = s.Used(name, id)
	return Version{Name: name, ID: id, ContentDir: snap, Meta: meta, Module: m}, true, nil
}

// Used records that a run used name's version id today (last_used, Plan 9 §6.3). It writes
// index.yml, under the cache lock, at most once per version per day, and never for a version or
// module the cache no longer holds.
func (s *Store) Used(name, id string) error {
	if err := checkID(name, id); err != nil {
		return err
	}
	today := s.today()
	if idx, found, err := s.readIndex(name); err != nil || !found || idx.LastUsed[id] == today {
		return err
	}
	unlock, err := s.lockStore()
	if err != nil {
		return err
	}
	defer unlock()
	idx, found, err := s.readIndex(name)
	if err != nil || !found || idx.LastUsed[id] == today {
		return err
	}
	if _, err := os.Lstat(filepath.Join(s.dir, name, "versions", id)); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if idx.LastUsed == nil {
		idx.LastUsed = map[string]string{}
	}
	idx.LastUsed[id] = today
	return writeYAML(filepath.Join(s.dir, name, indexFile), idx)
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
		return Version{}, errs.WithHints(&DigestError{Name: name, ID: id, Digest: meta.Digest, Recorded: true}, removeHint(name))
	}
	content := filepath.Join(vdir, "content")
	m, err := module.Load(content, name, s.targets)
	if err != nil {
		return Version{}, fmt.Errorf("cached version %s/%s: %w", name, id, err)
	}
	return Version{Name: name, ID: id, ContentDir: content, Meta: meta, Module: m}, nil
}

// LocalGone reports whether the source o records is a local folder or archive whose location no
// longer exists (os.Lstat reports it missing), as after its project moved. A symbolic link
// that dangles still exists, and a location that cannot be examined (a parent that is a file, no
// permission) is not reported gone: o is hostile input, so the caller refuses with its own
// message instead of echoing the system's.
func (o Origin) LocalGone() bool {
	if !source.IsLocal(source.Kind(o.Type), o.Source) {
		return false
	}
	_, err := os.Lstat(o.Source)
	return errors.Is(err, fs.ErrNotExist)
}

// Origin is the newest origin of name (the source last committed to the cache), as a view whose
// Heads hold the other origins' after its own (index.view); found is false when the module is
// not stored. Where the source a run asks for is known, OriginFor is the origin of that source.
func (s *Store) Origin(name string) (Origin, bool, error) {
	idx, found, err := s.readIndex(name)
	if err != nil || !found {
		return Origin{}, false, err
	}
	return idx.view(0, true), true, nil
}

// OriginFor is the origin of name for the source k names, with its own heads only; found is false
// when the module is not stored from that source.
func (s *Store) OriginFor(name string, k SourceKey) (Origin, bool, error) {
	idx, found, err := s.readIndex(name)
	if err != nil || !found {
		return Origin{}, false, err
	}
	i := idx.find(k)
	if i < 0 {
		return Origin{}, false, nil
	}
	return idx.view(i, false), true, nil
}

// Origins are every origin of name, the newest first, each with its own heads only; none when the
// module is not stored.
func (s *Store) Origins(name string) ([]Origin, error) {
	idx, found, err := s.readIndex(name)
	if err != nil || !found {
		return nil, err
	}
	out := make([]Origin, len(idx.Origins))
	for i := range idx.Origins {
		out[i] = idx.view(i, false)
	}
	return out, nil
}

// readIndex reads and checks index.yml; found is false when the module is not stored.
func (s *Store) readIndex(name string) (idx index, found bool, err error) {
	if err := module.ValidateName("module", name); err != nil {
		return index{}, false, err
	}
	err = readYAML(filepath.Join(s.dir, name, indexFile), &idx)
	if errors.Is(err, fs.ErrNotExist) {
		return index{}, false, nil
	}
	if _, read := errors.AsType[*fs.PathError](err); err != nil && !read {
		return index{}, false, &IndexError{Name: name, Err: err}
	}
	if err != nil {
		return index{}, false, err
	}
	if len(idx.Origins) == 0 {
		return index{}, false, errs.WithHints(&IndexError{Name: name, Err: errors.New("it records no origin")}, removeHint(name))
	}
	for _, o := range idx.Origins {
		if o.Components != nil {
			if err := o.Components.Check(); err != nil {
				return index{}, false, errs.WithHints(fmt.Errorf("cache entry %s: components: %w", name, err), removeHint(name))
			}
		}
		for _, h := range o.Heads {
			if err := checkID(name, h.Digest); err != nil {
				return index{}, false, err
			}
			if h.Commit != "" && !source.IsFullCommit(h.Commit) {
				return index{}, false, errs.WithHints(fmt.Errorf("cache entry %s: the head for ref %q records commit %q, which is not a full commit SHA",
					name, untrusted.Text(h.Ref, untrusted.MaxName), untrusted.Text(h.Commit, untrusted.MaxName)), removeHint(name))
			}
		}
	}
	// A last_used entry lola did not write is dropped, never refused: its key reaches no path, and
	// its version counts as first seen on the next clean's day (Plan 9 §6.3, R-9-44).
	maps.DeleteFunc(idx.LastUsed, func(id, day string) bool {
		_, err := time.Parse(dayLayout, day)
		return !ValidID(id) || err != nil
	})
	return idx, true, nil
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
// entry whose index lola refuses is still present (and removable); Names, which lists only
// entries with an index.yml, can leave out one Has counts.
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
