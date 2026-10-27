// Package manifest models the desired state of a scope: project.yml (committable) or user.yml.
package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

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
	// Market is which marketplace the entry was installed through, as written (Plan 6c §6.4,
	// ruling R-6c-15): provenance only, accepted in project.yml and user.yml and never in a
	// profile. Nothing is resolved or decided from it but lola sync --update's catalog lookup,
	// which never changes the source, path or ref, and every lola sync's fetch of an entry whose
	// market names another source than its own, which runs under source.Spec.FromCatalog (that
	// rule only adds refusals, so a forged market cannot loosen it).
	Market *MarketRef `yaml:"market,omitempty"`
	// Components is the selection a marketplace's catalog made of the module's directory (Plan 6c
	// §4.1, user decision of 2026-10-22): the module is those skills, commands and agents alone,
	// in every fetch of the entry.
	Components *source.Components `yaml:"components,omitempty"`
	// Vars overrides the module's template vars, name → value: a string, a bool or a []string
	// (Plan 7 §4.2). Read from the file's node by tag (EntryVars), as lola.yml vars are; the
	// module's declaration is checked when it is planned.
	Vars map[string]any `yaml:"vars,omitempty"`
}

// MarketRef names a marketplace by its name and its source, as the entry's file records them:
// someone else's text, which lola never verifies.
type MarketRef struct {
	Name   string `yaml:"name" json:"name"`
	Source string `yaml:"source" json:"source"`
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
	err = m.decode(b, root)
	if ue, ok := errors.AsType[*UnresolvedError](err); ok {
		ue.Path, ue.Manifest = path, m
		return nil, ue
	}
	if ve, ok := errors.AsType[*module.VarError](err); ok {
		err = fmt.Errorf("%s:%d: %s", path, ve.Line, ve.Reason)
	} else if err != nil {
		err = fmt.Errorf("%s: %w", path, err)
	}
	if err != nil {
		return nil, errs.WithHints(err, "fix it by hand, or restore it from version control")
	}
	return m, nil
}

// decode reads b, whose document's mapping is root, into m, refusing unknown keys, and validates
// the result.
func (m *Manifest) decode(b []byte, root *yaml.Node) error {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(m); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return m.validate(root)
}

// validate applies the field rules of Plan 6a §4.3 that need neither the file system nor git to
// m, read from the mapping root (nil for none). An entry whose source is a marketplace address is
// checked last (Plan 6c §6.7): once every other rule passed, every such entry is reported in one
// *UnresolvedError, its source's line found in root.
func (m *Manifest) validate(root *yaml.Node) error {
	if err := plainTargets(m.Targets); err != nil {
		return err
	}
	seen := map[string]bool{}
	var unresolved []Unresolved
	for i, e := range m.Modules {
		if err := module.ValidateName("module", e.Name); err != nil {
			return err
		}
		if seen[e.Name] {
			return errs.WithHints(fmt.Errorf("module %s is listed twice", e.Name), "keep one entry per module")
		}
		seen[e.Name] = true
		vars, err := EntryVars(root, i) // an unresolved entry's too: lola install @m/x keeps them
		if err != nil {
			return err
		}
		m.Modules[i].Vars = vars
		if mkt, mod, ok := Address(e.Source); ok {
			unresolved = append(unresolved, Unresolved{Name: e.Name, Market: mkt, Module: mod, Line: SourceLine(root, "modules", i)})
			continue
		}
		if err := e.Validate(); err != nil {
			return fmt.Errorf("module %s: %w", e.Name, err)
		}
	}
	if len(unresolved) > 0 {
		return &UnresolvedError{Entries: unresolved}
	}
	return nil
}

// EntryVars reads the vars of the i-th entry of the modules list in the mapping root (nil for
// none) from its node, by tag, as module.ParseVars does: a refusal is a *module.VarError naming
// modules[i].vars and the line.
func EntryVars(root *yaml.Node, i int) (map[string]any, error) {
	if root == nil {
		return nil, nil
	}
	list := yamldoc.Value(root, "modules")
	if list == nil || list.Kind != yaml.SequenceNode || i >= len(list.Content) || list.Content[i].Kind != yaml.MappingNode {
		return nil, nil
	}
	return module.ParseVars(yamldoc.Value(list.Content[i], "vars"), fmt.Sprintf("modules[%d].vars", i))
}

// SameVars reports whether two entries' vars are the same overrides, however they were decoded:
// none and an empty mapping alike, a number by its kind and value (2.0 is not 2).
func SameVars(a, b map[string]any) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == len(b)
	}
	ja, errA := json.Marshal(module.Vars(a))
	jb, errB := json.Marshal(module.Vars(b))
	return errA == nil && errB == nil && bytes.Equal(ja, jb)
}

// CloneVars returns a copy of vars that shares no list with it.
func CloneVars(vars map[string]any) map[string]any {
	if vars == nil {
		return nil
	}
	out := make(map[string]any, len(vars))
	for k, v := range vars {
		switch l := v.(type) {
		case []string:
			v = slices.Clone(l)
		case []any:
			v = slices.Clone(l)
		}
		out[k] = v
	}
	return out
}

// Address reports whether source is a marketplace address, @<market>/<module>, both parts passing
// the module-name rule (Plan 6c §3.3), and its parts. Command-line input only: in a file it is an
// unresolved entry (§6.7). Any other source starting with @ is a path, as before.
func Address(source string) (mkt, mod string, ok bool) {
	rest, found := strings.CutPrefix(source, "@")
	if !found {
		return "", "", false
	}
	mkt, mod, found = strings.Cut(rest, "/")
	if !found || module.ValidateName("marketplace", mkt) != nil || module.ValidateName("module", mod) != nil {
		return "", "", false
	}
	return mkt, mod, true
}

// SourceLine is the line of the source of the i-th entry of the list at key in the mapping root
// (nil for none): the entry's own line when it has no source key, root's when the list is
// shorter, and 0 with no root.
func SourceLine(root *yaml.Node, key string, i int) int {
	if root == nil {
		return 0
	}
	list := yamldoc.Value(root, key)
	if list == nil || i >= len(list.Content) {
		return root.Line
	}
	if n := yamldoc.Value(list.Content[i], "source"); n != nil {
		return n.Line
	}
	return list.Content[i].Line
}

// Unresolved is an entry of a file whose source is a marketplace address (Plan 6c §6.7, ruling
// R-6c-7): Name is the entry's, Market and Module the address's parts, Line its source's. Coached
// is set by whoever adds the hint that fixes the entry; until then the message says to edit it.
type Unresolved struct {
	Name, Market, Module string
	Line                 int
	Coached              bool
}

// UnresolvedError refuses a file, project.yml, user.yml or a profile at Path, whose Entries have a
// marketplace address as their source (Plan 6c §6.7): a file must record each module's real
// source, so that it does not depend on the reader's marketplaces. Manifest is project.yml or
// user.yml as read, every entry as written, which lola install @m/x edits to replace one (§6.3);
// nil for a profile. It carries no hints: they depend on the reader's marketplaces.
type UnresolvedError struct {
	Path     string
	Entries  []Unresolved
	Manifest *Manifest
}

func (e *UnresolvedError) Error() string {
	line := func(u Unresolved) string {
		s := fmt.Sprintf("%s:%d: %s's source is @%s/%s", e.Path, u.Line, u.Name, u.Market, u.Module)
		if len(e.Entries) == 1 {
			s += ", a marketplace address; a file must record the module's real source"
		}
		if !u.Coached {
			s += "; edit the entry to name the module's source, ref and path"
		}
		return s
	}
	if len(e.Entries) == 1 {
		return line(e.Entries[0])
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s records %d marketplace addresses as sources; a file must record each module's real source", e.Path, len(e.Entries))
	for _, u := range e.Entries {
		b.WriteString("\n  " + line(u))
	}
	return b.String()
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
	refCommit, isCommit := source.CommitRef(e.Ref)
	switch {
	case e.Commit != "" && e.Digest == "":
		return errs.WithHints(errors.New("commit without digest"), "to pin a commit by hand, write it as ref: <full commit SHA> and drop commit")
	case isCommit && (e.Commit != "" || e.Digest != "") && e.Commit != refCommit:
		return errs.WithHints(errors.New("commit must equal ref, a full commit SHA"), "drop commit and digest, and lola pins the commit again")
	case (e.Commit != "" || e.Digest != "") && source.IsFolderForm(e.Source):
		return errs.WithHints(fmt.Errorf("%s is a folder, and folders are never pinned", untrusted.Text(e.Source, untrusted.MaxName)), "drop commit and digest")
	}
	if e.Market != nil {
		if err := e.Market.check(); err != nil {
			return fmt.Errorf("market: %w", err)
		}
	}
	if e.Components != nil {
		if err := e.Components.Check(); err != nil {
			return fmt.Errorf("components: %w", err)
		}
	}
	return nil
}

// check refuses a market a file records that lola could not name back (Plan 6c §6.4): a name
// that is no marketplace name (the module-name rule) and a source that is no recorded source,
// each printable as it is.
func (m *MarketRef) check() error {
	if !untrusted.IsPlain(m.Name) {
		return errors.New("name holds control or invisible characters")
	}
	if err := module.ValidateName("marketplace", m.Name); err != nil {
		return err
	}
	return source.Recorded{Source: m.Source}.Check()
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
