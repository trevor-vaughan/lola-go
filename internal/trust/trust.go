// Package trust keeps the trust record (Plan 6a §7): per project, the view of project.yml that
// lola last wrote or the user last trusted, in $XDG_STATE_HOME/lola/trust.json. A manifest entry
// the record covers is one lola may act on as written; any other entry came from somewhere else
// (a pull, a hand edit) and is refused until the user reviews and trusts it with lola sync. User
// scope has no record: user.yml is the user's own.
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
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/gofrs/flock"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/fsx"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Version is the trust file schema version this binary writes.
const Version = 1

// maxSize bounds trust.json, which is read whole.
const maxSize = 4 << 20

// Entry is one module's view fields: everything of a manifest entry that changes what lola
// fetches or writes. Extends is set on a project profile's entry that comes from a remote profile
// it extends (Plan 6b §8.1), and omitted otherwise, so a view without one hashes as in Plan 6a.
type Entry struct {
	Name    string       `json:"name"`
	Source  string       `json:"source,omitempty"`
	Ref     string       `json:"ref,omitempty"`
	Path    string       `json:"path,omitempty"`
	Commit  string       `json:"commit,omitempty"`
	Digest  string       `json:"digest,omitempty"`
	Targets []string     `json:"targets,omitempty"`
	Rules   *bool        `json:"rules,omitempty"`
	Extends *ExtendsView `json:"extends,omitempty"`
	// Market is the entry's marketplace as its file writes it (Plan 6c §8): provenance, shown as
	// written and never verified. Omitted when absent, so a view without it hashes as before.
	Market *manifest.MarketRef `json:"market,omitempty"`
	// Components is the selection of the module's directory the entry records (Plan 6c §4.1):
	// it decides what is installed, so it is trusted like the source. Omitted when absent.
	Components *source.Components `json:"components,omitempty"`
	// Vars overrides the module's template vars (Plan 7 §8): what its templates render with is
	// trusted like the source. Omitted when absent, so a view without it hashes as before.
	Vars module.Vars `json:"vars,omitempty"`
}

// ExtendsView is the remote profile an entry comes from through extends: (Plan 6b §8.1, §19 S4):
// its qualified name and the source and pin of its source.json. Another pin (after lola profile
// --update) or another source of that name is a change to every entry it gives.
type ExtendsView struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Commit string `json:"commit,omitempty"`
	Digest string `json:"digest"`
}

// View is a manifest's top-level targets and entries, modules sorted by name (§7.2), and the
// views of the project's profiles that the user trusted or lola wrote, by profile name (Plan 6b
// §8.1). Profiles is omitted when empty, so a view without profiles hashes as in Plan 6a.
type View struct {
	Targets  []string               `json:"targets,omitempty"`
	Modules  []Entry                `json:"modules,omitempty"`
	Profiles map[string]ProfileView `json:"profiles,omitempty"`
}

// ProfileView is a project profile's part of a view (Plan 6b §8.1): its entries, sorted by name,
// each with the targets it installs to, its own or else the profile's, so a change to a
// profile's targets is a change to every entry it gives them to.
type ProfileView struct {
	Modules []Entry `json:"modules,omitempty"`
}

// decodedNumbers reads in place each var of v's entries that the decoder kept as a json.Number
// as module.DecodedNumber does, and reports false when one is not a finite number or an integer
// beyond 64 bits.
func (v View) decodedNumbers() bool {
	read := func(entries []Entry) bool {
		for _, e := range entries {
			for k, val := range e.Vars {
				if n, isNumber := val.(json.Number); isNumber {
					d, ok := module.DecodedNumber(n)
					if !ok {
						return false
					}
					e.Vars[k] = d
				}
			}
		}
		return true
	}
	ok := read(v.Modules)
	for _, p := range v.Profiles {
		ok = ok && read(p.Modules)
	}
	return ok
}

// Part is the part of v that one file gives: for key "", project.yml's top-level targets and
// entries; for any other key, the entries of the project profile of that name, without targets.
// It shares nothing with v.
func (v View) Part(key string) View {
	if key == "" {
		return View{Targets: slices.Clone(v.Targets), Modules: cloneEntries(v.Modules)}
	}
	return View{Modules: cloneEntries(v.Profiles[key].Modules)}
}

// WithPart is v with the part key gives replaced by part, Part's inverse: every other part is
// kept as it is. A profile's part with no entries leaves the profile out.
func (v View) WithPart(key string, part View) View {
	out := View{Targets: slices.Clone(v.Targets), Modules: cloneEntries(v.Modules)}
	for k, pv := range v.Profiles {
		if k != key {
			out.setProfile(k, pv.Modules)
		}
	}
	if key == "" {
		out.Targets, out.Modules = slices.Clone(part.Targets), cloneEntries(part.Modules)
	} else {
		out.setProfile(key, part.Modules)
	}
	out.sort()
	return out
}

// setProfile records a copy of modules as profile key's part, unless there are none.
func (v *View) setProfile(key string, modules []Entry) {
	if len(modules) == 0 {
		return
	}
	if v.Profiles == nil {
		v.Profiles = map[string]ProfileView{}
	}
	v.Profiles[key] = ProfileView{Modules: cloneEntries(modules)}
}

func cloneEntries(es []Entry) []Entry {
	if es == nil {
		return nil
	}
	out := make([]Entry, len(es))
	for i, e := range es {
		out[i] = e.clone()
	}
	return out
}

// EntryOf is the view of one manifest entry.
func EntryOf(e manifest.Entry) Entry {
	out := Entry{Name: e.Name, Source: e.Source, Ref: e.Ref, Path: e.Path, Commit: e.Commit, Digest: e.Digest,
		Targets: slices.Clone(e.Targets), Rules: e.Rules}
	if e.Market != nil {
		m := *e.Market
		out.Market = &m
	}
	out.Components = e.Components.Clone()
	out.Vars = manifest.CloneVars(e.Vars)
	return out
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
	byName := func(a, b Entry) int { return strings.Compare(a.Name, b.Name) }
	slices.SortFunc(v.Modules, byName)
	for _, pv := range v.Profiles {
		slices.SortFunc(pv.Modules, byName)
	}
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
func (v View) Covers(e manifest.Entry) bool { return v.CoversEntry(EntryOf(e)) }

// CoversEntry is Covers for an entry's view, which may come from a remote profile (Extends).
func (v View) CoversEntry(e Entry) bool {
	r := v.Find(e.Name)
	return r != nil && r.equal(e)
}

func (a Entry) equal(b Entry) bool {
	return a.Name == b.Name && a.Source == b.Source && a.Ref == b.Ref && a.Path == b.Path && a.Commit == b.Commit &&
		a.Digest == b.Digest && slices.Equal(a.Targets, b.Targets) && sameRules(a.Rules, b.Rules) && sameExtends(a.Extends, b.Extends) &&
		sameMarket(a.Market, b.Market) && a.Components.Equal(b.Components) && manifest.SameVars(a.Vars, b.Vars)
}

func sameMarket(a, b *manifest.MarketRef) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func sameExtends(a, b *ExtendsView) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func sameRules(a, b *bool) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

// Refresh is the record r after a write by lola turned loaded into saved (§7.4), loaded and
// saved being the views of the file whose part of r key names (Part): every top-level or module
// field the write changed takes its saved value, an entry the write added is recorded whole and
// one it removed is dropped. Every other field keeps its trust state, so an entry r does not
// cover stays uncovered unless the write made every field match; a field changed in an entry r
// does not hold at all is not recorded. Every other part of r is kept as it is (Plan 6b §19 R2).
func Refresh(r View, key string, loaded, saved View) View {
	return r.WithPart(key, refresh(r.Part(key), loaded, saved))
}

func refresh(r, loaded, saved View) View {
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

// Accept is the record r after the user trusted, in the view v of the file as loaded whose part
// of r key names (Part), the entries named in modules (an entry v lacks is dropped from the
// record), and the top-level targets when targets is set (§7.4). Every other entry keeps its
// record, so a declined or unnamed entry stays as covered, or as uncovered, as it was, and so
// does every other part of r (Plan 6b §19 R2). Accepting every change of v and its targets makes
// v that part.
func Accept(r View, key string, v View, modules []string, targets bool) View {
	return r.WithPart(key, accept(r.Part(key), v, modules, targets))
}

func accept(r, v View, modules []string, targets bool) View {
	out := View{Targets: slices.Clone(r.Targets)}
	if targets {
		out.Targets = slices.Clone(v.Targets)
	}
	for _, e := range r.Modules {
		if !slices.Contains(modules, e.Name) {
			out.Modules = append(out.Modules, e.clone())
		}
	}
	for _, e := range v.Modules {
		if slices.Contains(modules, e.Name) {
			out.Modules = append(out.Modules, e.clone())
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
	if !sameExtends(l.Extends, s.Extends) {
		e.Extends = s.Extends
	}
	if !sameMarket(l.Market, s.Market) {
		e.Market = s.Market
	}
	if !l.Components.Equal(s.Components) {
		e.Components = s.Components.Clone()
	}
	if !manifest.SameVars(l.Vars, s.Vars) {
		e.Vars = manifest.CloneVars(s.Vars)
	}
}

func (e Entry) clone() Entry {
	e.Targets = slices.Clone(e.Targets)
	if e.Extends != nil {
		x := *e.Extends
		e.Extends = &x
	}
	if e.Market != nil {
		m := *e.Market
		e.Market = &m
	}
	e.Components = e.Components.Clone()
	e.Vars = manifest.CloneVars(e.Vars)
	return e
}

// Statuses of a manifest against its project's trust record (§7.3).
const (
	StatusTrusted = "trusted" // the record is the manifest's view, or the manifest is empty
	StatusNew     = "new"     // the project has no record: nothing is covered
	StatusChanged = "changed"
)

// Kinds of a module's change.
const (
	ChangeAdded   = "added"
	ChangeRemoved = "removed"
	ChangeChanged = "changed"
)

// FieldChange is one view field whose value differs from the record's. From and To keep the
// field's JSON type: a string, a list of target ids for targets, a boolean for rules; nil for
// an absent value.
type FieldChange struct {
	Field string `json:"field"`
	From  any    `json:"from"`
	To    any    `json:"to"`
}

// Change is how one module's entry differs from the record: added (every field it has, from
// nil), removed (every field it had, to nil) or changed (each differing field). HostChanged says
// a changed source names another host, scheme or kind of place (local or remote); ContentFixed
// says the entry pins its content by digest. Profile names the project profile whose entry it is
// (project:<name>, Plan 6b §8.1), "" for project.yml's: Check leaves it for the caller to set.
type Change struct {
	Module       string        `json:"module"`
	Profile      string        `json:"profile,omitempty"`
	Kind         string        `json:"change"`
	HostChanged  bool          `json:"host_changed"`
	ContentFixed bool          `json:"content_fixed"`
	Fields       []FieldChange `json:"fields"`
}

// Report is the result of Check: the status, the top-level targets when the record does not
// cover them (nil when it does), and each module's change in name order.
type Report struct {
	Status  string
	Targets *FieldChange
	Changes []Change
}

// Check compares v, the view of a manifest, with the record r (§7.3): with r's project.yml part
// (View.Part("")), never its profiles'. A profile's view is checked against a record holding
// that profile's part.
func Check(r *Record, v View) Report {
	v.Modules = slices.Clone(v.Modules)
	v.sort()
	rv := r.View
	rv.Modules = slices.Clone(rv.Modules)
	rv.sort()
	rep := Report{Status: StatusTrusted, Changes: []Change{}}
	if !slices.Equal(rv.Targets, v.Targets) {
		rep.Targets = &FieldChange{Field: "targets", From: listValue(rv.Targets), To: listValue(v.Targets)}
	}
	for _, e := range v.Modules {
		old := rv.Find(e.Name)
		switch {
		case old == nil:
			rep.Changes = append(rep.Changes, Change{Module: e.Name, Kind: ChangeAdded, ContentFixed: e.Digest != "", Fields: fieldChanges(Entry{}, e)})
		case !old.equal(e):
			rep.Changes = append(rep.Changes, Change{Module: e.Name, Kind: ChangeChanged, ContentFixed: e.Digest != "",
				HostChanged: hostChanged(old.Source, e.Source), Fields: fieldChanges(*old, e)})
		}
	}
	for _, e := range rv.Modules {
		if v.Find(e.Name) == nil {
			rep.Changes = append(rep.Changes, Change{Module: e.Name, Kind: ChangeRemoved, Fields: fieldChanges(e, Entry{})})
		}
	}
	slices.SortStableFunc(rep.Changes, func(a, b Change) int { return strings.Compare(a.Module, b.Module) })
	// Without a record every entry and the targets are changes; a manifest with neither has
	// nothing to trust.
	switch {
	case rep.Targets == nil && len(rep.Changes) == 0:
	case !r.Exists:
		rep.Status = StatusNew
	default:
		rep.Status = StatusChanged
	}
	return rep
}

// hostChanged reports whether a changed source names another host, scheme or kind of place.
// A source in git's scp form may name a local folder instead (source.Parse checks the disk
// first), so a change involving one always counts: the warning errs toward being shown.
func hostChanged(from, to string) bool {
	if from == to {
		return false
	}
	isSCP := func(loc string) bool { return !strings.Contains(loc, "://") && source.Host(loc) != "" }
	return isSCP(from) || isSCP(to) || source.Host(from) != source.Host(to)
}

// fieldChanges lists the view fields whose values differ between from and to, in the order
// lola writes them.
func fieldChanges(from, to Entry) []FieldChange {
	out := []FieldChange{}
	for _, f := range []struct {
		name     string
		from, to string
	}{{"source", from.Source, to.Source}, {"ref", from.Ref, to.Ref}, {"path", from.Path, to.Path},
		{"commit", from.Commit, to.Commit}, {"digest", from.Digest, to.Digest}} {
		if f.from != f.to {
			out = append(out, FieldChange{Field: f.name, From: stringValue(f.from), To: stringValue(f.to)})
		}
	}
	if !slices.Equal(from.Targets, to.Targets) {
		out = append(out, FieldChange{Field: "targets", From: listValue(from.Targets), To: listValue(to.Targets)})
	}
	if !sameRules(from.Rules, to.Rules) {
		out = append(out, FieldChange{Field: "rules", From: boolValue(from.Rules), To: boolValue(to.Rules)})
	}
	if !sameExtends(from.Extends, to.Extends) {
		out = append(out, FieldChange{Field: "extends", From: extendsValue(from.Extends), To: extendsValue(to.Extends)})
	}
	if !sameMarket(from.Market, to.Market) {
		out = append(out, FieldChange{Field: "market", From: marketValue(from.Market), To: marketValue(to.Market)})
	}
	if !from.Components.Equal(to.Components) {
		out = append(out, FieldChange{Field: "components", From: componentsValue(from.Components), To: componentsValue(to.Components)})
	}
	if !manifest.SameVars(from.Vars, to.Vars) {
		out = append(out, FieldChange{Field: "vars", From: varsValue(from.Vars), To: varsValue(to.Vars)})
	}
	return out
}

// varsValue is an entry's vars as a field change shows them, by name, each value as lola cache
// info writes a var and through untrusted.Text: `language = "go", strict = true, tools = ["rg"]`.
func varsValue(vars map[string]any) any {
	if len(vars) == 0 {
		return nil
	}
	parts := make([]string, 0, len(vars))
	for _, k := range slices.Sorted(maps.Keys(vars)) {
		parts = append(parts, untrusted.Text(k, untrusted.MaxName)+" = "+module.VarText(vars[k]))
	}
	return strings.Join(parts, ", ")
}

// componentsValue is an entry's selection as a field change shows it, as its file writes it, each
// path through untrusted.Text: "skills skills/xlsx; commands commands/x.md".
func componentsValue(c *source.Components) any {
	if c == nil {
		return nil
	}
	return c.String()
}

// marketValue is an entry's marketplace as a field change shows it, as its file writes it, each
// part through untrusted.Text: "acme from https://github.com/acme/plugins". lola never checks it
// (Plan 6c §8).
func marketValue(m *manifest.MarketRef) any {
	if m == nil {
		return nil
	}
	return untrusted.Text(m.Name, untrusted.MaxName) + " from " + untrusted.Text(m.Source, untrusted.MaxName)
}

// extendsValue is the remote profile an entry comes from, as a field change shows it: its name,
// source, commit (12 characters, when it has one) and digest ("remote:go from https://… commit
// 0123456789ab sha256:…").
func extendsValue(x *ExtendsView) any {
	if x == nil {
		return nil
	}
	if x.Commit != "" {
		return x.Name + " from " + x.Source + " commit " + x.Commit[:min(12, len(x.Commit))] + " " + x.Digest
	}
	return x.Name + " from " + x.Source + " " + x.Digest
}

func stringValue(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func listValue(ss []string) any {
	if len(ss) == 0 {
		return nil
	}
	return slices.Clone(ss)
}

func boolValue(b *bool) any {
	if b == nil {
		return nil
	}
	return *b
}

// Record is one project's entry in trust.json. Exists is false for a project the file has no
// record for: its View is empty and covers nothing, and Check tells it apart from a record of an
// empty manifest.
type Record struct {
	View   View
	Exists bool
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
			return &Record{View: p.View, Exists: true}, nil
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
	// a var keeps its exact int64 and its float spelling (2.0 is not 2) for the trust decision;
	// Hash re-encodes the view without either, so the sha256 only checks the record's integrity
	dec.UseNumber()
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
		if !p.View.decodedNumbers() {
			return nil, unreadable(s.path, fmt.Errorf("the record of %s holds a var that is not a finite number or beyond 64 bits", k))
		}
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
