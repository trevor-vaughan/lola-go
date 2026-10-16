// Package profile reads profiles (Plan 6b §4): named, shareable sets of module entries in the
// shape of manifest entries. This version reads the user's own profiles and default, from
// paths.UserProfiles. It does no network access and writes no file.
package profile

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
	"github.com/trevor-vaughan/lola-go/internal/yamldoc"
)

// Version is the profile schema version this binary reads.
const Version = 1

// Limits (Plan 6b §4.3, §19 S8).
const (
	MaxFiles       = 256 // entries in a profiles directory
	MaxEntries     = 512 // modules in one profile
	MaxDescription = 256 // bytes of a description
)

// Kind is where a profile comes from (Plan 6b §4.1).
type Kind string

// The kinds this version reads. A qualified name is "<kind>:<name>", except default's, which is
// "default".
const (
	KindUser    Kind = "user"    // $XDG_CONFIG_HOME/lola/profiles/<name>.yml
	KindDefault Kind = "default" // $XDG_CONFIG_HOME/lola/profiles/default.yml, user scope only
)

// DefaultName is the name of the profile that is always on in user scope.
const DefaultName = "default"

// Info is a profile found by name, not read.
type Info struct {
	Name string
	Kind Kind
	Path string
}

// Qualified is the name an active list stores (Plan 6b §4.4, D8): "user:go", or "default".
func (i Info) Qualified() string {
	if i.Kind == KindDefault {
		return DefaultName
	}
	return string(i.Kind) + ":" + i.Name
}

// Visible is the profiles a scope can use, by name and in name order, read from the directory
// listing alone (Plan 6b §6.1, §19 S8): the user's profiles, and default at user scope
// (userScope). A file whose name is not <name>.yml with a valid name is not a profile. A
// directory holding more than MaxFiles entries is refused, naming it; a missing one holds none.
func Visible(p paths.Paths, userScope bool) ([]Info, error) {
	dir := p.UserProfiles
	unreadable := func(err error) error {
		return errs.WithHints(fmt.Errorf("cannot list %s: %s", dir, untrusted.Text(err.Error(), 2*untrusted.MaxName)),
			"make "+dir+" a directory lola can read, or remove it")
	}
	f, err := os.Open(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return []Info{}, nil
	}
	if err != nil {
		return nil, unreadable(err)
	}
	des, err := f.ReadDir(MaxFiles + 1)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return nil, unreadable(err)
	}
	if len(des) > MaxFiles {
		return nil, errs.WithHints(fmt.Errorf("%s holds more than %d files", dir, MaxFiles), "move the profiles you do not use out of it")
	}
	out := []Info{}
	for _, de := range des {
		name, ok := strings.CutSuffix(de.Name(), ".yml")
		if !ok || module.ValidateName("profile", name) != nil {
			continue
		}
		kind := KindUser
		if name == DefaultName {
			if !userScope {
				continue
			}
			kind = KindDefault
		}
		out = append(out, Info{Name: name, Kind: kind, Path: filepath.Join(dir, de.Name())})
	}
	slices.SortFunc(out, func(a, b Info) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// Profile is a profile file as read. Entries without targets of their own use Targets (Entries).
type Profile struct {
	Version     int              `yaml:"version"`
	Description string           `yaml:"description,omitempty"`
	Targets     []string         `yaml:"targets,omitempty,flow"`
	Modules     []manifest.Entry `yaml:"modules,omitempty"`
}

// Load reads the profile at path once (Plan 6b §4.3, §6.1): through yamldoc.Read, as user.yml is
// read, which follows a symbolic link (a dotfile manager's, §19 P7) and refuses a file that is not
// regular, a file over yamldoc.MaxFile, more than one document, anchors and aliases; the version
// first, so a newer lola's profile is refused for its version; then every key known and every
// field checked, each refusal naming the file and the line. A missing file is an error that wraps fs.ErrNotExist; an empty one holds no modules.
func Load(path string) (*Profile, error) {
	d, b, err := yamldoc.Read(path, Version)
	if err != nil {
		// yamldoc's hints are for files lola writes; a profile's cause is kept as text only.
		return nil, fixHint(errors.New(err.Error()))
	}
	if b == nil {
		return nil, fmt.Errorf("%s: %w", path, fs.ErrNotExist)
	}
	p := &Profile{Version: Version}
	root := yamldoc.Root(d)
	if root == nil {
		return p, nil
	}
	var v struct {
		Version int `yaml:"version"`
	}
	if err := root.Decode(&v); err != nil {
		return nil, fixHint(fmt.Errorf("%s: %s", path, untrusted.Text(err.Error(), 2*untrusted.MaxName)))
	}
	if v.Version > Version {
		return nil, fixHint(errs.WithHints(fmt.Errorf("%s was written by a newer lola (profile version %d); upgrade lola", path, v.Version), "upgrade lola"))
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(p); err != nil && !errors.Is(err, io.EOF) {
		return nil, fixHint(fmt.Errorf("%s: %s", path, untrusted.Text(err.Error(), 2*untrusted.MaxName)))
	}
	if err := p.check(root); err != nil {
		return nil, fixHint(fmt.Errorf("%s:%w", path, err))
	}
	return p, nil
}

// fixHint coaches a profile lola refused to read.
func fixHint(err error) error { return errs.WithHints(err, "fix it by hand; lola never writes it") }

// check applies the field rules of Plan 6b §4.3 to a decoded profile, root being its document's
// mapping: each error starts with the line it is about and a colon.
func (p *Profile) check(root *yaml.Node) error {
	at := func(key string) int {
		if n := yamldoc.Value(root, key); n != nil {
			return n.Line
		}
		return root.Line
	}
	switch {
	case len(p.Description) > MaxDescription:
		return fmt.Errorf("%d: description is longer than %d bytes", at("description"), MaxDescription)
	case !untrusted.IsPlain(p.Description):
		return fmt.Errorf("%d: description holds control or invisible characters", at("description"))
	case slices.ContainsFunc(p.Targets, func(t string) bool { return !untrusted.IsPlain(t) }):
		return fmt.Errorf("%d: targets holds control or invisible characters", at("targets"))
	case len(p.Modules) > MaxEntries:
		return fmt.Errorf("%d: modules lists more than %d entries", at("modules"), MaxEntries)
	}
	var lines []*yaml.Node
	if n := yamldoc.Value(root, "modules"); n != nil {
		lines = n.Content
	}
	for i := range p.Modules {
		e := &p.Modules[i]
		line := root.Line
		if i < len(lines) {
			line = lines[i].Line
		}
		if err := module.ValidateName("module", e.Name); err != nil {
			return fmt.Errorf("%d: %w", line, err)
		}
		if slices.ContainsFunc(p.Modules[:i], func(o manifest.Entry) bool { return o.Name == e.Name }) {
			return errs.WithHints(fmt.Errorf("%d: module %s is listed twice", line, e.Name), "keep one entry per module")
		}
		if err := e.Validate(); err != nil {
			return fmt.Errorf("%d: module %s: %w", line, e.Name, err)
		}
	}
	return nil
}

// Entries is the profile's module entries, each without targets of its own given the profile's.
func (p *Profile) Entries() []manifest.Entry {
	out := make([]manifest.Entry, len(p.Modules))
	for i, e := range p.Modules {
		e.Targets = slices.Clone(e.Targets)
		if len(e.Targets) == 0 {
			e.Targets = slices.Clone(p.Targets)
		}
		out[i] = e
	}
	return out
}
