// Package profile reads profiles: named, shareable sets of module entries in the
// shape of manifest entries. It reads the user's own profiles and default, from
// paths.UserProfiles; a project's committed profiles, from <root>/.lola/profiles, which it
// treats as someone else's; remote profiles, which lola profile --add keeps in
// paths.RemoteProfiles; and the profiles of a source the engine fetched into staging. It does no
// network access and writes no file.
package profile

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/fsx"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
	"github.com/trevor-vaughan/lola-go/internal/yamldoc"
)

// Version is the profile schema version this binary reads.
const Version = 1

// Limits.
const (
	MaxFiles       = 256 // entries in a profiles directory
	MaxEntries     = 512 // modules in one profile
	MaxDescription = 256 // bytes of a description
	MaxMembers     = 64  // names in bundle: (activeprofile.Max, the cap after expansion)
)

// BaseName is reserved for a scope's base: a base.yml in a profiles
// directory is no profile, and no bundle names it.
const BaseName = "base"

// Shape is what a profile file holds: modules, a bundle of other
// profiles, or nothing yet.
type Shape int

// The shapes of a profile file.
const (
	ShapeEmpty   Shape = iota // neither modules: nor bundle:, an experiment not installed into yet
	ShapeModules              // modules: lists module entries
	ShapeBundle               // bundle: lists profiles, and nothing else
)

// MixedError is a file that has both bundle: and modules:, the one shape
// lola can fix (lola profile <name> --split, which the engine hints): Line is bundle:'s line, Name
// the profile's name (its file's base name).
type MixedError struct {
	Path string
	Line int
	Name string
}

func (e *MixedError) Error() string {
	return fmt.Sprintf("%s:%d: %s has both bundle: and modules:; a bundle combines profiles and a profile lists modules",
		untrusted.Text(e.Path, untrusted.MaxName), e.Line, untrusted.Text(e.Name, untrusted.MaxName))
}

// Kind is where a profile comes from.
type Kind string

// The kinds this version reads. A qualified name is "<kind>:<name>", except default's, which is
// "default".
const (
	KindUser    Kind = "user"    // $XDG_CONFIG_HOME/lola/profiles/<name>.yml
	KindDefault Kind = "default" // $XDG_CONFIG_HOME/lola/profiles/default.yml, user scope only
	KindProject Kind = "project" // <root>/.lola/profiles/<name>.yml, committed with the project
	KindRemote  Kind = "remote"  // <paths.RemoteProfiles>/<name>/profile.yml, fetched by lola profile --add
)

// ProjectDir is where a project keeps its profiles, relative to its root.
const ProjectDir = ".lola" + string(filepath.Separator) + "profiles"

// DefaultName is the name of the profile that is always on in user scope.
const DefaultName = "default"

// Info is a profile found by name, not read.
type Info struct {
	Name string
	Kind Kind
	Path string
}

// Qualified is the profile's name as lock.json records the active profile and as layers are named:
// "user:go", or "default", user scope's base, which is never the active one.
func (i Info) Qualified() string {
	if i.Kind == KindDefault {
		return DefaultName
	}
	return string(i.Kind) + ":" + i.Name
}

// Visible is the profiles a scope can use, by name and in name order, read from the directory
// listing alone: the user's profiles, and default at user scope (userScope). A file not named
// <name>.yml is not a profile; one named <name>.yml whose name fails the module-name rules is not
// either, and the count of those is returned, for listings to report without the names. base.yml is
// no profile either (BaseName): reserved is its path, for listings to warn about ("" when there is
// none). A directory holding more than MaxFiles entries is refused, naming it; a missing one holds
// none.
func Visible(p paths.Paths, userScope bool) (infos []Info, invalid int, reserved string, err error) {
	dir := p.UserProfiles
	names, invalid, err := list(dir)
	if err != nil {
		return nil, 0, "", err
	}
	infos = []Info{}
	for _, name := range names {
		kind := KindUser
		switch name {
		case BaseName:
			reserved = filepath.Join(dir, name+".yml")
			continue
		case DefaultName:
			if !userScope {
				continue
			}
			kind = KindDefault
		}
		infos = append(infos, Info{Name: name, Kind: kind, Path: filepath.Join(dir, name+".yml")})
	}
	return infos, invalid, reserved, nil
}

// Project is the profiles of the project rooted at root, in name order, read from
// the listing of <root>/.lola/profiles alone, as Visible reads the user's, with the count of
// files whose names are not valid. A project cannot define default: a default.yml there is no
// profile, and ignored is its path, for listings to warn about ("" when there is none); a base.yml
// is no profile either, and reserved is its path (Visible). The
// directory is committed, so it and .lola/ must be directories of the project's own: a symbolic
// link, or a path that resolves outside root, is refused before anything is listed. A
// missing one holds none.
func Project(root string) (infos []Info, invalid int, ignored, reserved string, err error) {
	if found, err := ownDirs(root); err != nil || !found {
		return []Info{}, 0, "", "", err
	}
	dir := filepath.Join(root, ProjectDir)
	names, invalid, err := list(dir)
	if err != nil {
		return nil, 0, "", "", err
	}
	infos = []Info{}
	for _, name := range names {
		path := filepath.Join(dir, name+".yml")
		switch name {
		case DefaultName:
			ignored = path
			continue
		case BaseName:
			reserved = path
			continue
		}
		infos = append(infos, Info{Name: name, Kind: KindProject, Path: path})
	}
	return infos, invalid, ignored, reserved, nil
}

// ownDirs checks that .lola/ and .lola/profiles/ under root are directories of the project's
// own: not symbolic links, not other files, and not resolving outside root. found is
// false when either is missing.
func ownDirs(root string) (found bool, err error) {
	for _, rel := range []string{".lola", ProjectDir} {
		path := filepath.Join(root, rel)
		fi, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir() {
			return false, errs.WithHints(fmt.Errorf("%s is not a directory of the project's own: lola reads a project's profiles only from %s inside it",
				untrusted.Text(path, untrusted.MaxName), ProjectDir), "make it a directory, or remove it")
		}
	}
	if r, err := fsx.Resolve(root, ProjectDir); err != nil || r.Outside {
		return false, errs.WithHints(errors.Join(fmt.Errorf("%s resolves outside the project", untrusted.Text(filepath.Join(root, ProjectDir), untrusted.MaxName)), err),
			"make it a directory, or remove it")
	}
	return true, nil
}

// list is the names of the profiles in dir, in name order, from its listing alone: each file
// named <name>.yml whose name follows the module-name rules, and the count of those whose name
// does not. A file not named <name>.yml is not a profile. A directory holding more
// than MaxFiles entries is refused, naming it; a missing one holds none.
func list(dir string) ([]string, int, error) {
	des, err := readDir(dir)
	if err != nil {
		return nil, 0, err
	}
	var names []string
	invalid := 0
	for _, de := range des {
		name, ok := strings.CutSuffix(de.Name(), ".yml")
		if !ok {
			continue
		}
		if module.ValidateName("profile", name) != nil {
			invalid++
			continue
		}
		names = append(names, name)
	}
	slices.Sort(names)
	return names, invalid, nil
}

// readDir is the listing of dir, a profiles directory: nil for a missing one, refused, naming
// it, when it holds more than MaxFiles entries or cannot be read.
func readDir(dir string) ([]os.DirEntry, error) {
	unreadable := func(err error) error {
		return errs.WithHints(fmt.Errorf("cannot list %s: %s", untrusted.Text(dir, untrusted.MaxName), untrusted.Text(err.Error(), 2*untrusted.MaxName)),
			"make "+untrusted.Text(dir, untrusted.MaxName)+" a directory lola can read, or remove it")
	}
	f, err := os.Open(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
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
		return nil, errs.WithHints(fmt.Errorf("%s holds more than %d files", untrusted.Text(dir, untrusted.MaxName), MaxFiles),
			"move the profiles you do not use out of it")
	}
	if des == nil {
		des = []os.DirEntry{}
	}
	return des, nil
}

// Profile is a profile file as read: a list of modules, whose entries
// without targets of their own use Targets (Entries), or a bundle of other profiles (Bundle), which
// the engine resolves, since only it lists them. read is the bytes it was read from, which Edit
// and Pin edit; project says it is a project's, read without following a link.
type Profile struct {
	Version     int              `yaml:"version"`
	Description string           `yaml:"description,omitempty"`
	Targets     []string         `yaml:"targets,omitempty,flow"`
	Bundle      []string         `yaml:"bundle,omitempty,flow"`
	Modules     []manifest.Entry `yaml:"modules,omitempty"`
	read        []byte           // the bytes it was read from
	hint        string           // how to fix the file
	remote      bool             // a remote profile's rules apply (checkRemote)
	project     bool             // a project's: read again without following a link
	bundleAt    []int            // the line of each name in Bundle
	bundleLine  int              // the line of bundle:
}

// file is a profile as decoded.
type file struct {
	Version     int       `yaml:"version"`
	Description string    `yaml:"description"`
	Targets     []string  `yaml:"targets"`
	Bundle      yaml.Node `yaml:"bundle"` // any node: checkBundle reads it, refusing what is no list of names
	Modules     []entry   `yaml:"modules"`
}

// Shape is what p holds: a bundle when its file has bundle: (an empty list included), modules when
// it lists any, else nothing.
func (p *Profile) Shape() Shape {
	switch {
	case p.Bundle != nil:
		return ShapeBundle
	case len(p.Modules) > 0:
		return ShapeModules
	}
	return ShapeEmpty
}

// MemberLine is the line of the k-th name in Bundle, for a refusal the engine makes about it (a
// member it cannot resolve, the one that closes a cycle). k must be less than len(p.Bundle).
func (p *Profile) MemberLine(k int) int { return p.bundleAt[k] }

// BundleLine is the line of bundle:, for a refusal of the file as a bundle (default.yml's).
func (p *Profile) BundleLine() int { return p.bundleLine }

// entry is a profile's module entry as decoded: manifest.Entry's fields, and the picks only a
// profile's entry takes. market: and components: are accepted in your
// profiles and a project's and refused in a remote one, as the picks are.
// Any other key is refused at load as unknown.
type entry struct {
	Name       string              `yaml:"name"`
	Source     string              `yaml:"source"`
	Ref        string              `yaml:"ref"`
	Path       string              `yaml:"path"`
	Commit     string              `yaml:"commit"`
	Digest     string              `yaml:"digest"`
	Targets    []string            `yaml:"targets"`
	Rules      *bool               `yaml:"rules"`
	Vars       map[string]any      `yaml:"vars"` // replaced by what manifest.EntryVars reads from the node
	Market     *manifest.MarketRef `yaml:"market"`
	Components *source.Components  `yaml:"components"`
	Only       []string            `yaml:"only"`
	Skip       []string            `yaml:"skip"`
}

// Load reads the profile at path once: through yamldoc.Read, as lola writes it, which follows a
// symbolic link (a dotfile manager's) and refuses a file that is not regular, a file over
// yamldoc.MaxFile, more than one document, anchors and aliases; the version first, so a newer
// lola's profile is refused for its version; then every key known and every field checked, each
// refusal naming the file and the line. A missing file is an error that wraps fs.ErrNotExist; an
// empty one holds no modules. A profile whose only fault is entries whose source is a marketplace
// address is returned as read with its *manifest.UnresolvedError, so an edit can replace one of
// them (Edit).
func Load(path string) (*Profile, error) {
	return load(path, yamldoc.Read, "fix it by hand", KindUser)
}

// LoadProject reads the project profile at path once, as Load reads a user's, except that the
// file is someone else's: it must be a regular file, never a symbolic
// link, and it is opened without following one (yamldoc.ReadNoFollow). Its refusals coach the
// fix a committed file takes.
func LoadProject(path string) (*Profile, error) {
	const hint = "fix it by hand, or restore it from version control"
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		return nil, errs.WithHints(fmt.Errorf("%s is a symbolic link; a project's profile must be a file of its own", untrusted.Text(path, untrusted.MaxName)),
			"replace it with the file it points to", hint)
	}
	return load(path, yamldoc.ReadNoFollow, hint, KindProject)
}

// Parse is the profile of yours at path whose bytes are b, checked as Load checks it: the profile
// a write in progress leaves (an engine run's own edit, before it is applied). As with Load, a
// *manifest.UnresolvedError comes with the profile.
func Parse(path string, b []byte) (*Profile, error) {
	d, err := yamldoc.Decode(b)
	if err != nil {
		return nil, errs.WithHints(fmt.Errorf("%s: %s", untrusted.Text(path, untrusted.MaxName), err.Error()), "fix it by hand")
	}
	return parse(path, d, b, "fix it by hand", KindUser)
}

// ParseProject is Parse for a project's profile at path: the profile a write in progress leaves in
// a project (a new one lola makes), checked as LoadProject checks it.
func ParseProject(path string, b []byte) (*Profile, error) {
	const hint = "fix it by hand, or restore it from version control"
	d, err := yamldoc.Decode(b)
	if err != nil {
		return nil, errs.WithHints(fmt.Errorf("%s: %s", untrusted.Text(path, untrusted.MaxName), err.Error()), hint)
	}
	return parse(path, d, b, hint, KindProject)
}

// NewBundle is a new bundle file of members, in their order, as lola writes one: its version and
// bundle: as a flow list. The caller parses it as the file it is for (Parse, ParseProject), which
// checks every member.
func NewBundle(members []string) ([]byte, error) {
	list := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
	for _, m := range members {
		list.Content = append(list.Content, yamldoc.Str(m))
	}
	root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
		yamldoc.Str("version"), {Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(Version)}, yamldoc.Str("bundle"), list}}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := errors.Join(enc.Encode(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}), enc.Close()); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// load reads the profile at path with read, every refusal coached with hint; kind is whose it is,
// whose rules apply (parse).
func load(path string, read func(string, int) (*yaml.Node, []byte, error), hint string, kind Kind) (*Profile, error) {
	d, b, err := read(path, Version)
	if err != nil {
		// yamldoc's hints are for files lola writes; a profile's cause is kept as text only.
		return nil, errs.WithHints(errors.New(err.Error()), hint)
	}
	if b == nil {
		return nil, fmt.Errorf("%s: %w", untrusted.Text(path, untrusted.MaxName), fs.ErrNotExist)
	}
	return parse(path, d, b, hint, kind)
}

// parse decodes and checks the profile document d, read from path as b; every refusal is
// coached with hint. kind is whose it is: a remote profile's rules (checkRemote) and a project's
// (no user: member, read again without following a link) apply by it; yours and default's are
// KindUser. A *manifest.UnresolvedError comes with the profile as read, a *MixedError alone.
func parse(path string, d *yaml.Node, b []byte, hint string, kind Kind) (*Profile, error) {
	fixHint := func(err error) error { return errs.WithHints(err, hint) }
	shown := untrusted.Text(path, untrusted.MaxName)
	p := &Profile{Version: Version, read: b, hint: hint, remote: kind == KindRemote, project: kind == KindProject}
	root := yamldoc.Root(d)
	if root == nil {
		return p, nil
	}
	var v struct {
		Version int `yaml:"version"`
	}
	if err := root.Decode(&v); err != nil {
		return nil, fixHint(errors.New(untrusted.Text(yamldoc.Explain(shown, err, root).Error(), 2*untrusted.MaxName)))
	}
	if v.Version > Version {
		return nil, errs.WithHints(fmt.Errorf("%s was written by a newer lola (profile version %d); upgrade lola", shown, v.Version), "upgrade lola")
	}
	if root.Kind == yaml.MappingNode && yamldoc.Value(root, "extends") != nil {
		return nil, extendsGone(path, root, kind, hint)
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	f := file{Version: p.Version}
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		err = yamldoc.Explain(shown, err, root, yamldoc.Shape{Type: reflect.TypeFor[file](), Noun: "a profile"},
			yamldoc.Shape{Type: reflect.TypeFor[entry](), Noun: "a profile entry"})
		return nil, fixHint(errors.New(untrusted.Text(err.Error(), 2*untrusted.MaxName)))
	}
	p.Version, p.Description, p.Targets = f.Version, f.Description, f.Targets
	for _, e := range f.Modules {
		p.Modules = append(p.Modules, manifest.Entry{Name: e.Name, Source: e.Source, Ref: e.Ref, Path: e.Path, Commit: e.Commit, Digest: e.Digest,
			Targets: e.Targets, Rules: e.Rules, Vars: e.Vars, Market: e.Market, Components: e.Components, Only: e.Only, Skip: e.Skip})
	}
	err := p.check(root)
	if ue, ok := errors.AsType[*manifest.UnresolvedError](err); ok {
		ue.Path = path
		return p, ue
	}
	if me, ok := errors.AsType[*MixedError](err); ok {
		me.Path, me.Name = path, strings.TrimSuffix(filepath.Base(path), ".yml")
		return nil, fixHint(me)
	}
	if err != nil {
		return nil, fixHint(fmt.Errorf("%s:%w", shown, err))
	}
	return p, nil
}

// keyLine is the line of key in the mapping root, or the mapping's own when it has none: a block
// list's value node starts at its first item, so a refusal naming the key reads the key node.
func keyLine(root *yaml.Node, key string) int {
	for k := 0; k+1 < len(root.Content); k += 2 {
		if root.Content[k].Value == key {
			return root.Content[k].Line
		}
	}
	return root.Line
}

// check applies the field rules to a decoded profile, root being its document's
// mapping: each error starts with the line it is about and a colon, but for entries whose source
// is a marketplace address, which once every other rule passed are reported together in a
// *manifest.UnresolvedError without its path.
func (p *Profile) check(root *yaml.Node) error {
	at := func(key string) int { return keyLine(root, key) }
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
	if err := p.checkBundle(root, at); err != nil {
		return err
	}
	var lines []*yaml.Node
	if n := yamldoc.Value(root, "modules"); n != nil {
		lines = n.Content
	}
	var unresolved []manifest.Unresolved
	for i := range p.Modules {
		e := &p.Modules[i]
		line := root.Line
		if i < len(lines) {
			line = lines[i].Line
		}
		if err := module.ValidateName("module", e.Name); err != nil {
			return fmt.Errorf("%d: %w", line, err)
		}
		if p.remote {
			for _, f := range []struct {
				key string
				set bool
			}{{"market", e.Market != nil}, {"components", e.Components != nil}, {"only", len(e.Only) > 0}, {"skip", len(e.Skip) > 0}} {
				if f.set {
					return errs.WithHints(fmt.Errorf("%d: module %s: a remote profile's entry takes no %s: key", line, e.Name, f.key),
						"set it on the entry of a profile of yours")
				}
			}
		}
		var node *yaml.Node
		if i < len(lines) {
			node = lines[i]
		}
		if err := checkPicks(node, *e, line); err != nil {
			return err
		}
		vars, err := manifest.EntryVars(root, i)
		if err != nil {
			return err // a *module.VarError, which starts with its line
		}
		e.Vars = vars
		if slices.ContainsFunc(p.Modules[:i], func(o manifest.Entry) bool { return o.Name == e.Name }) {
			return errs.WithHints(fmt.Errorf("%d: module %s is listed twice", line, e.Name), "keep one entry per module")
		}
		if mkt, mod, ok := manifest.Address(e.Source); ok {
			unresolved = append(unresolved, manifest.Unresolved{Name: e.Name, Market: mkt, Module: mod, Line: manifest.SourceLine(root, "modules", i)})
			continue
		}
		if err := e.Validate(); err != nil {
			return fmt.Errorf("%d: module %s: %w", line, e.Name, err)
		}
		if p.remote {
			if err := checkRemote(*e); err != nil {
				return fmt.Errorf("%d: module %s: %w", line, e.Name, err)
			}
		}
	}
	if len(unresolved) > 0 {
		return &manifest.UnresolvedError{Entries: unresolved}
	}
	return nil
}

// checkBundle reads bundle: into Bundle and applies the shape rules, root being its document's
// mapping and at the line of a key: a remote profile holds no bundle:; bundle: is a list (bundle:
// [] for none yet) of names; a bundle lists no modules (a *MixedError, which lola profile --split
// fixes) and no targets; it names at most MaxMembers profiles, each once under any spelling, each a
// profile name, bare or qualified by user:, project: or remote:, never default or base under any of
// them (always loaded) and, in a project's bundle, never one of yours. An invalid name in a
// project's bundle reads as one that names no profile, as the engine refuses a member it cannot
// resolve, so a project file cannot probe what you have. Each refusal starts with the line it is
// about.
func (p *Profile) checkBundle(root *yaml.Node, at func(string) int) error {
	n := yamldoc.Value(root, "bundle")
	if n == nil {
		return nil
	}
	line := keyLine(root, "bundle")
	switch {
	case p.remote:
		return errs.WithHints(fmt.Errorf("%d: bundle: a remote profile cannot combine profiles; its names would resolve on each reader's machine", line),
			"only your profiles and a project's may use bundle:")
	case n.Kind == yaml.ScalarNode && n.Tag == "!!null":
		return fmt.Errorf("%d: bundle: lists nothing; write bundle: [] for a bundle of no profiles yet, or list its profiles", line)
	case n.Kind != yaml.SequenceNode:
		return fmt.Errorf("%d: bundle: is not a list of profile names; write it as bundle: [go, docs]", line)
	case len(p.Modules) > 0:
		return &MixedError{Line: line}
	case len(p.Targets) > 0:
		return fmt.Errorf("%d: a bundle has no targets: its members' targets apply; remove targets:, or set them in its member profiles", at("targets"))
	case len(n.Content) > MaxMembers:
		return fmt.Errorf("%d: bundle lists more than %d profiles", line, MaxMembers)
	}
	p.Bundle, p.bundleAt = make([]string, 0, len(n.Content)), make([]int, 0, len(n.Content))
	p.bundleLine = line
	own := KindUser // whose profile a bare name is, for the same profile under two spellings
	if p.project {
		own = KindProject
	}
	seen := map[string]string{} // each member's profile, kind:name, to the spelling that named it
	for _, c := range n.Content {
		line := c.Line
		if c.Kind != yaml.ScalarNode || c.Tag != "!!str" {
			return fmt.Errorf("%d: bundle: a member must be a profile name", line)
		}
		m := c.Value
		shown := untrusted.Text(m, untrusted.MaxName)
		kind, name, qualified := strings.Cut(m, ":")
		if !qualified {
			kind, name = string(own), m
		}
		known := kind == string(KindUser) || kind == string(KindProject) || kind == string(KindRemote)
		switch {
		case !known && p.project:
			return fmt.Errorf("%d: bundle: %s is not a profile of this project or a remote profile you have", line, shown)
		case !known:
			return errs.WithHints(fmt.Errorf("%d: bundle: %s is not a profile name", line, shown), "name a profile bare, or as user:<name>, project:<name> or remote:<name>")
		case name == DefaultName:
			return fmt.Errorf("%d: bundle: default is always loaded at user scope; a bundle does not need it", line)
		case name == BaseName:
			return fmt.Errorf("%d: bundle: base is the scope's base, always loaded; a bundle does not need it", line)
		case p.project && kind == string(KindUser):
			return fmt.Errorf("%d: bundle: %s is a profile of yours; a project's bundle names project and remote profiles only", line, shown)
		}
		if err := module.ValidateName("profile", name); err != nil {
			switch {
			case p.project:
				return fmt.Errorf("%d: bundle: %s is not a profile of this project or a remote profile you have", line, shown)
			case qualified:
				return fmt.Errorf("%d: bundle: %s: %w", line, shown, err)
			}
			return fmt.Errorf("%d: bundle: %w", line, err)
		}
		if first, ok := seen[kind+":"+name]; ok {
			if first == m {
				return errs.WithHints(fmt.Errorf("%d: bundle lists %s twice", line, shown), "name each profile once")
			}
			return errs.WithHints(fmt.Errorf("%d: bundle lists %s:%s twice, as %s and %s", line, kind, name, untrusted.Text(first, untrusted.MaxName), shown),
				"name each profile once")
		}
		seen[kind+":"+name] = m
		p.Bundle, p.bundleAt = append(p.Bundle, m), append(p.bundleAt, line)
	}
	return nil
}

// checkPicks applies the cherry-pick rules to e's only: and skip:, n being e's node (nil for
// none) and line its line: each item is a kind/name selector (config.ParsePattern) without a
// module, which the entry names. A refusal starts with the item's line.
func checkPicks(n *yaml.Node, e manifest.Entry, line int) error {
	for _, l := range []struct {
		key  string
		list []string
	}{{"only", e.Only}, {"skip", e.Skip}} {
		var items []*yaml.Node
		if n != nil {
			if v := yamldoc.Value(n, l.key); v != nil {
				items = v.Content
			}
		}
		for k, s := range l.list {
			at := line
			if k < len(items) {
				at = items[k].Line
			}
			if _, bare, qualified := strings.Cut(s, ":"); qualified {
				hint := config.PatternForms
				if _, err := config.ParsePattern(bare); err == nil {
					hint = "write it as " + untrusted.Text(bare, untrusted.MaxName)
				}
				return errs.WithHints(fmt.Errorf("%d: %s: %q names a module; an entry's selectors are kind/name", at, l.key, untrusted.Text(s, untrusted.MaxName)), hint)
			}
			if _, err := config.ParsePattern(s); err != nil {
				// The error's own hints, but an entry's selector takes no module: coach kind/name alone.
				hints := errs.Hints(err)
				for k, h := range hints {
					if h == config.SelectorForms {
						hints[k] = config.PatternForms
					}
				}
				return errs.WithHints(fmt.Errorf("%d: %s: %s", at, l.key, err.Error()), hints...)
			}
		}
	}
	return nil
}

// ExtendsError is a profile file of yours or a project's that still has extends:, which bundles
// replaced, refused at the key's line. Hint is the edit that keeps what the file named: the remote
// profiles it extended become members of a bundle, before the file's own modules, which move to a
// profile of their own beside it (a bundle lists no modules and has no targets: its members'
// apply). default.yml, always loaded and never a bundle, keeps its modules: a bundle of the remote
// profiles is switched on instead. Every name in it has been through untrusted.Text.
type ExtendsError struct {
	Path    string   // the file, as output shows it
	Line    int      // extends:'s
	Default bool     // the file is default.yml
	Members []string // the remote profiles extends: lists, as bundle members (remote:<name>); nil when it lists none or is no list of names
	listed  bool     // extends: is a list of names (an empty one included)
	bundle  bool     // the file has bundle: too
	moved   []string // the keys a bundle cannot hold that the file has: modules:, targets:
	targets string   // targets:, as a flow list ("" when it has none or it is no list of names)
	name    string   // the file's profile name
	dir     string   // the file's directory, as read
}

// Beside is the path of a profile file name beside e's file.
func (e *ExtendsError) Beside(name string) string { return filepath.Join(e.dir, name+".yml") }

func (e *ExtendsError) Error() string {
	return fmt.Sprintf("%s:%d: extends: is no longer a key; a bundle combines profiles now", e.Path, e.Line)
}

// Hint is the edit that fixes e's file. taken reports whether a profile has name where a bare
// member of the file's bundle would find it (a file beside it, or a remote profile), so a new
// profile never shadows one; on is, for default.yml, the profile on at user scope as a bundle of
// yours names it ("" for none), which the bundle switched to keeps; split is the lola profile
// --split that moves the file's modules: and targets: into a profile of their own once extends: is
// a bundle: ("" when the caller has none: the hint then says how to move them by hand). An error
// of taken is returned.
func (e *ExtendsError) Hint(taken func(name string) (bool, error), on, split string) (string, error) {
	switch {
	case !e.listed:
		return "replace extends: in " + e.Path + " with bundle:, naming each remote profile it listed as remote:<name>", nil
	case len(e.Members) == 0:
		return "remove extends: from " + e.Path + "; it names no profile", nil
	}
	free := func(want string) (string, error) {
		if len(want) > 60 { // room for -<n> within a profile name's 64 characters
			want = "modules"
		}
		name := want
		for k := 2; k <= MaxFiles+1; k++ {
			t, err := taken(name)
			if err != nil || !t {
				return name, err
			}
			name = fmt.Sprintf("%s-%d", want, k)
		}
		return "", fmt.Errorf("no name from %s to %s is free", want, name)
	}
	if e.Default {
		name, err := free("default-bundle")
		if err != nil {
			return "", err
		}
		members, keep := e.Members, ""
		if on != "" {
			members, keep = append(slices.Clone(members), on), " and "+on+", the profile on now, which the switch keeps"
		}
		return "remove extends: from " + e.Path + ", then switch to a bundle of them" + keep + ": " +
			errs.ShellJoin([]string{"lola", "profile", "--user", name, "--switch", "--new", "--bundle", strings.Join(members, ",")}), nil
	}
	if e.bundle {
		return "remove extends: from " + e.Path + " and list its profiles first in bundle: as " + strings.Join(e.Members, ", "), nil
	}
	if len(e.moved) == 0 {
		return "make it a bundle: in " + e.Path + ", replace extends: with bundle: [" + strings.Join(e.Members, ", ") + "]", nil
	}
	// A bundle has no targets:, so its remote members' entries keep theirs.
	what := "this file's targets:"
	if e.targets != "" {
		what = "targets: " + e.targets
	}
	ownTargets := "; the entries of " + strings.Join(e.Members, ", ") + " then install to their own targets, not " + what
	if split != "" && slices.Contains(e.moved, "modules:") {
		hint := "make it a bundle: in " + e.Path + " replace extends: with bundle: [" + strings.Join(e.Members, ", ") + "]"
		if slices.Contains(e.moved, "targets:") {
			hint += ownTargets
		}
		return hint + "; then move its " + strings.Join(e.moved, " and ") + " into a profile of their own: " + split, nil
	}
	own, err := free(e.name + "-modules")
	if err != nil {
		return "", err
	}
	hint := "make it a bundle: move its " + strings.Join(e.moved, " and ") + " into a new profile beside it, " +
		untrusted.Text(e.Beside(own), untrusted.MaxName) + ", starting with version: 1; then in " + e.Path +
		" replace " + strings.Join(append([]string{"extends:"}, e.moved...), ", ") + " with bundle: [" + strings.Join(append(slices.Clone(e.Members), own), ", ") + "]"
	if slices.Contains(e.moved, "targets:") {
		hint += ownTargets
	}
	return hint, nil
}

// extendsGone is the refusal of a profile file that still has extends:, root being its
// document's mapping: an *ExtendsError for yours and a project's, hinted with names free beside
// the file (the engine hints it again knowing every profile); a remote profile's is refused with
// hint alone, as lola does not edit those.
func extendsGone(path string, root *yaml.Node, kind Kind, hint string) error {
	shown := untrusted.Text(path, untrusted.MaxName)
	e := &ExtendsError{Path: shown, name: strings.TrimSuffix(filepath.Base(path), ".yml"), dir: filepath.Dir(path)}
	for k := 0; k+1 < len(root.Content); k += 2 {
		if root.Content[k].Value == "extends" {
			e.Line = root.Content[k].Line
		}
	}
	if kind == KindRemote {
		return errs.WithHints(fmt.Errorf("%s:%d: extends: is no longer a key", shown, e.Line), hint)
	}
	e.Default = kind == KindUser && e.name == DefaultName
	n := yamldoc.Value(root, "extends")
	names := func(n *yaml.Node) ([]string, bool) {
		if n == nil || n.Kind != yaml.SequenceNode || slices.ContainsFunc(n.Content, func(c *yaml.Node) bool { return c.Kind != yaml.ScalarNode }) {
			return nil, false
		}
		out := make([]string, 0, len(n.Content))
		for _, c := range n.Content {
			out = append(out, untrusted.Text(c.Value, untrusted.MaxName))
		}
		return out, true
	}
	var listed []string
	listed, e.listed = names(n)
	if n.Kind == yaml.ScalarNode && n.Tag == "!!null" {
		e.listed = true
	}
	for _, m := range listed {
		e.Members = append(e.Members, "remote:"+m)
	}
	e.bundle = yamldoc.Value(root, "bundle") != nil
	has := func(key string) bool {
		n := yamldoc.Value(root, key)
		return n != nil && (n.Kind != yaml.ScalarNode || n.Tag != "!!null") && (n.Kind != yaml.SequenceNode || len(n.Content) > 0)
	}
	if has("modules") || has("targets") {
		for _, k := range []string{"modules", "targets"} {
			if yamldoc.Value(root, k) != nil {
				e.moved = append(e.moved, k+":")
			}
		}
	}
	if ts, ok := names(yamldoc.Value(root, "targets")); ok && len(ts) > 0 {
		e.targets = "[" + strings.Join(ts, ", ") + "]"
	}
	h, err := e.Hint(func(name string) (bool, error) {
		_, err := os.Lstat(e.Beside(name))
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return true, err
	}, "", "")
	if err != nil {
		return errors.Join(e, err)
	}
	return errs.WithHints(e, h)
}

// Pin is the file at path, from which p was read, with each entry of pins taking its commit and
// digest: the bytes p was read from, so what lola writes is what the run
// checked, with those fields alone edited and comments and every other key kept. pins
// name modules p lists. A file that no longer holds those bytes is refused, never overwritten,
// and so is a result that would not load again.
func (p *Profile) Pin(path string, pins []manifest.Entry) ([]byte, error) {
	_, now, err := yamldoc.ReadNoFollow(path, Version)
	if err != nil {
		return nil, errs.WithHints(errors.New(err.Error()), p.hint)
	}
	if !bytes.Equal(now, p.read) {
		return nil, errs.WithHints(fmt.Errorf("%s changed while lola was running; not writing its pins", untrusted.Text(path, untrusted.MaxName)),
			"run the command again")
	}
	var c manifest.Change
	for _, pin := range pins {
		i := slices.IndexFunc(p.Modules, func(e manifest.Entry) bool { return e.Name == pin.Name })
		if i < 0 {
			return nil, fmt.Errorf("%s lists no module %s to pin", untrusted.Text(path, untrusted.MaxName), untrusted.Text(pin.Name, untrusted.MaxName))
		}
		ent := p.Modules[i]
		ent.Commit, ent.Digest = pin.Commit, pin.Digest
		c.Set = append(c.Set, ent)
	}
	var d yaml.Node
	if err := yaml.Unmarshal(p.read, &d); err != nil {
		return nil, err
	}
	out, err := manifest.EditDocument(&d, c)
	if err != nil {
		return nil, err
	}
	var check yaml.Node
	if err := yaml.Unmarshal(out, &check); err != nil {
		return nil, err
	}
	if _, err := parse(path, &check, out, p.hint, KindProject); err != nil {
		return nil, errs.WithHints(fmt.Errorf("not writing %s, which lola could not load again: %w", untrusted.Text(path, untrusted.MaxName), err),
			"nothing was written")
	}
	return out, nil
}

// Edit is the file at path, from which p was read, with c applied: the bytes p was read from, so
// what lola writes is what the run checked, edited by manifest.EditDocument, which keeps comments,
// order and every key c does not change. A nil p is a profile of yours that did not exist when the
// run read it: the file is created, and must still be missing. Yours is read again through a
// symbolic link (a dotfile manager's), and dst is the file the link leads to, which the caller
// writes; a project's is read without following one (yamldoc.ReadNoFollow), and dst is path. A file
// that no longer holds the bytes p was read from is refused, never overwritten, and so is a result
// that would not load again, but for entries whose source is a marketplace address that p already
// holds, which stay as written. The caller writes the bytes in its transaction, calling Current
// (and, for a project's, Unchanged) just before.
func (p *Profile) Edit(path string, c manifest.Change) (dst string, out []byte, err error) {
	d, err := p.current(path)
	if err != nil {
		return "", nil, err
	}
	if out, err = manifest.EditDocument(d, c); err != nil {
		return "", nil, err
	}
	var check yaml.Node
	if err := yaml.Unmarshal(out, &check); err != nil {
		return "", nil, err
	}
	hint, kind := "fix it by hand", KindUser
	if p != nil {
		hint = p.hint
		if p.project {
			kind = KindProject
		}
	}
	if _, err := parse(path, &check, out, hint, kind); err != nil && !p.holds(err) {
		return "", nil, errs.WithHints(fmt.Errorf("not writing %s, which lola could not load again: %w", untrusted.Text(path, untrusted.MaxName), err),
			"nothing was written")
	}
	if p != nil && p.project {
		return path, out, nil
	}
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		if dst, err = filepath.EvalSymlinks(path); err != nil {
			return "", nil, errs.WithHints(fmt.Errorf("%s is a symlink lola cannot follow: %w", untrusted.Text(path, untrusted.MaxName),
				untrusted.CleanErr(err)), "point the link at a file, or remove it")
		}
		return dst, out, nil
	}
	return path, out, nil
}

// SetBundle is the file at path, from which p was read, with its bundle: set to members, in order:
// the bytes p was read from, bundle: replaced in the style it has (a new one as a flow list, added
// after the keys it has), each member it keeps with its own comments, every other key and comment
// kept. p is a bundle or an empty profile: one with targets: is refused naming that line (a bundle
// has none; its members' apply), and one with modules gets its parse's refusal. Like Edit, a file
// that no longer holds those bytes is refused, never overwritten, and so is a result that would not
// load again (a member the parse refuses, naming its line). The caller writes the bytes in its
// transaction, calling Current (for a project's, Unchanged) just before.
func (p *Profile) SetBundle(path string, members []string) ([]byte, error) {
	d, err := p.current(path)
	if err != nil {
		return nil, err
	}
	root := yamldoc.Root(d)
	if root == nil {
		root = yamldoc.NullDocRoot(d, Version)
	}
	if t := yamldoc.Value(root, "targets"); t != nil && len(t.Content) > 0 {
		return nil, errs.WithHints(fmt.Errorf("%s:%d: targets: a bundle has none (its members' apply)", untrusted.Text(path, untrusted.MaxName),
			keyLine(root, "targets")), "remove targets: there, then run this again")
	}
	list := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
	old := yamldoc.Value(root, "bundle")
	var kept []*yaml.Node // a member the list keeps keeps its node: its comments stay with it
	if old != nil && old.Kind == yaml.SequenceNode {
		list.Style, kept = old.Style, slices.Clone(old.Content)
	}
	for _, m := range members {
		if k := slices.IndexFunc(kept, func(n *yaml.Node) bool { return n != nil && n.Kind == yaml.ScalarNode && n.Value == m }); k >= 0 {
			list.Content, kept[k] = append(list.Content, kept[k]), nil
			continue
		}
		list.Content = append(list.Content, yamldoc.Str(m))
	}
	if len(members) == 0 {
		list.Style = yaml.FlowStyle // an empty block list has no YAML form
	}
	if old != nil {
		old.Kind, old.Tag, old.Style, old.Value, old.Content = list.Kind, list.Tag, list.Style, "", list.Content
	} else {
		root.Content = append(root.Content, yamldoc.Str("bundle"), list)
	}
	out, err := encode(d)
	if err != nil {
		return nil, err
	}
	parse := Parse
	if p != nil && p.project {
		parse = ParseProject
	}
	if _, err := parse(path, out); err != nil {
		return nil, errs.WithHints(fmt.Errorf("not writing %s, which lola could not load again: %w", untrusted.Text(path, untrusted.MaxName), err),
			"nothing was written")
	}
	return out, nil
}

// holds says err is a *manifest.UnresolvedError whose every entry p lists with the same
// marketplace address: what an edit of p kept, never an address it wrote.
func (p *Profile) holds(err error) bool {
	ue, ok := errors.AsType[*manifest.UnresolvedError](err)
	if !ok || p == nil {
		return false
	}
	for _, u := range ue.Entries {
		if !slices.ContainsFunc(p.Modules, func(e manifest.Entry) bool {
			mkt, mod, ok := manifest.Address(e.Source)
			return ok && e.Name == u.Name && mkt == u.Market && mod == u.Module
		}) {
			return false
		}
	}
	return true
}

// Current checks, just before the caller writes the profile at path, that the file still holds the
// bytes p was read from, or for a nil p that it is still missing, reading it as Edit does.
func (p *Profile) Current(path string) error {
	_, err := p.current(path)
	return err
}

// current reads the profile at path again, as p was read (a project's without following a link),
// refusing it unless it holds the bytes p was read from (missing, for a nil p).
func (p *Profile) current(path string) (*yaml.Node, error) {
	read, was, hint := yamldoc.Read, []byte(nil), "fix it by hand"
	if p != nil {
		was, hint = p.read, p.hint
		if p.project {
			read = yamldoc.ReadNoFollow
		}
	}
	d, now, err := read(path, Version)
	if err != nil {
		return nil, errs.WithHints(errors.New(err.Error()), hint)
	}
	if (now == nil) != (p == nil) || !bytes.Equal(now, was) {
		return nil, errs.WithHints(fmt.Errorf("%s changed while lola was reading it; nothing was written", untrusted.Text(path, untrusted.MaxName)),
			"run the same command again")
	}
	return d, nil
}

// Unchanged checks, just before lola writes the profile at path of the project at root, that
// nothing moved since p was read: .lola/ and .lola/profiles/ are still
// directories of the project's own (or, for a nil p, a profile lola makes, still missing), and
// the file still holds the bytes p was read from (a nil p: it is still missing). Any change
// refuses the write, as Current does, whatever lola was writing.
func (p *Profile) Unchanged(root, path string) error {
	changed := fmt.Errorf("%s changed while lola was reading it; nothing was written", untrusted.Text(path, untrusted.MaxName))
	if found, err := ownDirs(root); err != nil || !found && p != nil {
		return errors.Join(changed, err)
	}
	var was []byte
	if p != nil {
		was = p.read
	}
	_, now, err := yamldoc.ReadNoFollow(path, Version)
	if err != nil || (now == nil) != (p == nil) || !bytes.Equal(now, was) {
		return errs.WithHints(errors.Join(changed, err), "run the same command again")
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
