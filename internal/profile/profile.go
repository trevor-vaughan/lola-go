// Package profile reads profiles (Plan 6b §4): named, shareable sets of module entries in the
// shape of manifest entries. It reads the user's own profiles and default, from
// paths.UserProfiles; a project's committed profiles, from <root>/.lola/profiles, which it
// treats as someone else's (§19 S3); remote profiles, which lola profile --add keeps in
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
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/fsx"
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
	MaxExtends     = 16  // names in extends:
)

// Kind is where a profile comes from (Plan 6b §4.1).
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

// Qualified is the name an active list stores (Plan 6b §4.4, D8): "user:go", or "default".
func (i Info) Qualified() string {
	if i.Kind == KindDefault {
		return DefaultName
	}
	return string(i.Kind) + ":" + i.Name
}

// Visible is the profiles a scope can use, by name and in name order, read from the directory
// listing alone (Plan 6b §6.1, §19 S8): the user's profiles, and default at user scope
// (userScope). A file not named <name>.yml is not a profile; one named <name>.yml whose name
// fails the module-name rules is not either, and the count of those is returned, for listings
// to report without the names (§4.3, §19 S7). A directory holding more than MaxFiles entries is refused,
// naming it; a missing one holds none.
func Visible(p paths.Paths, userScope bool) ([]Info, int, error) {
	dir := p.UserProfiles
	names, invalid, err := list(dir)
	if err != nil {
		return nil, 0, err
	}
	out := []Info{}
	for _, name := range names {
		kind := KindUser
		if name == DefaultName {
			if !userScope {
				continue
			}
			kind = KindDefault
		}
		out = append(out, Info{Name: name, Kind: kind, Path: filepath.Join(dir, name+".yml")})
	}
	return out, invalid, nil
}

// Project is the profiles of the project rooted at root (Plan 6b §4.1), in name order, read from
// the listing of <root>/.lola/profiles alone, as Visible reads the user's, with the count of
// files whose names are not valid. A project cannot define default: a default.yml there is no
// profile, and ignored is its path, for listings to warn about ("" when there is none). The
// directory is committed, so it and .lola/ must be directories of the project's own: a symbolic
// link, or a path that resolves outside root, is refused before anything is listed (§19 S3). A
// missing one holds none.
func Project(root string) (infos []Info, invalid int, ignored string, err error) {
	if found, err := ownDirs(root); err != nil || !found {
		return []Info{}, 0, "", err
	}
	dir := filepath.Join(root, ProjectDir)
	names, invalid, err := list(dir)
	if err != nil {
		return nil, 0, "", err
	}
	infos = []Info{}
	for _, name := range names {
		path := filepath.Join(dir, name+".yml")
		if name == DefaultName {
			ignored = path
			continue
		}
		infos = append(infos, Info{Name: name, Kind: KindProject, Path: path})
	}
	return infos, invalid, ignored, nil
}

// ownDirs checks that .lola/ and .lola/profiles/ under root are directories of the project's
// own (§19 S3): not symbolic links, not other files, and not resolving outside root. found is
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
// does not (§4.3, §19 S7). A file not named <name>.yml is not a profile. A directory holding more
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
// it, when it holds more than MaxFiles entries (§19 S8) or cannot be read.
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

// Profile is a profile file as read. Entries without targets of their own use Targets (Entries).
// Extends names the remote profiles whose entries come before the profile's own (Plan 6b §4.3):
// the engine resolves them, since only it lists the remote profiles.
type Profile struct {
	Version     int              `yaml:"version"`
	Description string           `yaml:"description,omitempty"`
	Targets     []string         `yaml:"targets,omitempty,flow"`
	Extends     []string         `yaml:"extends,omitempty,flow"`
	Modules     []manifest.Entry `yaml:"modules,omitempty"`
	read        []byte           // the bytes it was read from
	hint        string           // how to fix the file
	remote      bool             // a remote profile's rules apply (checkRemote)
	extendsAt   []int            // the line of each name in Extends
}

// file is a profile as decoded: a manifest's entries without market, which only project.yml and
// user.yml record (Plan 6c §6.4, ruling R-6c-1), so a profile entry carrying it is refused at
// load as any unknown key is, naming its line.
type file struct {
	Version     int      `yaml:"version"`
	Description string   `yaml:"description"`
	Targets     []string `yaml:"targets"`
	Extends     []string `yaml:"extends"`
	Modules     []entry  `yaml:"modules"`
}

// entry is a profile's module entry as decoded: manifest.Entry without Market and Components.
// New manifest fields are refused in profiles by design: a profile entry carrying one is refused
// at load as any unknown key is.
type entry struct {
	Name    string         `yaml:"name"`
	Source  string         `yaml:"source"`
	Ref     string         `yaml:"ref"`
	Path    string         `yaml:"path"`
	Commit  string         `yaml:"commit"`
	Digest  string         `yaml:"digest"`
	Targets []string       `yaml:"targets"`
	Rules   *bool          `yaml:"rules"`
	Vars    map[string]any `yaml:"vars"` // replaced by what manifest.EntryVars reads from the node
}

// Load reads the profile at path once (Plan 6b §4.3, §6.1): through yamldoc.Read, as user.yml is
// read, which follows a symbolic link (a dotfile manager's, §19 P7) and refuses a file that is not
// regular, a file over yamldoc.MaxFile, more than one document, anchors and aliases; the version
// first, so a newer lola's profile is refused for its version; then every key known and every
// field checked, each refusal naming the file and the line. A missing file is an error that wraps fs.ErrNotExist; an empty one holds no modules.
func Load(path string) (*Profile, error) {
	return load(path, yamldoc.Read, "fix it by hand; lola never writes it", false)
}

// LoadProject reads the project profile at path once, as Load reads a user's, except that the
// file is someone else's (Plan 6b §6.1, §19 S3): it must be a regular file, never a symbolic
// link, and it is opened without following one (yamldoc.ReadNoFollow). Its refusals coach the
// fix a committed file takes.
func LoadProject(path string) (*Profile, error) {
	const hint = "fix it by hand, or restore it from version control"
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		return nil, errs.WithHints(fmt.Errorf("%s is a symbolic link; a project's profile must be a file of its own", untrusted.Text(path, untrusted.MaxName)),
			"replace it with the file it points to", hint)
	}
	return load(path, yamldoc.ReadNoFollow, hint, false)
}

// load reads the profile at path with read, every refusal coached with hint; remote applies a
// remote profile's rules.
func load(path string, read func(string, int) (*yaml.Node, []byte, error), hint string, remote bool) (*Profile, error) {
	d, b, err := read(path, Version)
	if err != nil {
		// yamldoc's hints are for files lola writes; a profile's cause is kept as text only.
		return nil, errs.WithHints(errors.New(err.Error()), hint)
	}
	if b == nil {
		return nil, fmt.Errorf("%s: %w", path, fs.ErrNotExist)
	}
	return parse(path, d, b, hint, remote)
}

// parse decodes and checks the profile document d, read from path as b; every refusal is
// coached with hint. remote applies a remote profile's rules (checkRemote).
func parse(path string, d *yaml.Node, b []byte, hint string, remote bool) (*Profile, error) {
	fixHint := func(err error) error { return errs.WithHints(err, hint) }
	p := &Profile{Version: Version, read: b, hint: hint, remote: remote}
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
	f := file{Version: p.Version}
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return nil, fixHint(fmt.Errorf("%s: %s", path, untrusted.Text(err.Error(), 2*untrusted.MaxName)))
	}
	p.Version, p.Description, p.Targets, p.Extends = f.Version, f.Description, f.Targets, f.Extends
	for _, e := range f.Modules {
		p.Modules = append(p.Modules, manifest.Entry{Name: e.Name, Source: e.Source, Ref: e.Ref, Path: e.Path, Commit: e.Commit, Digest: e.Digest,
			Targets: e.Targets, Rules: e.Rules, Vars: e.Vars})
	}
	err := p.check(root)
	if ue, ok := errors.AsType[*manifest.UnresolvedError](err); ok {
		ue.Path = path
		return nil, ue
	}
	if err != nil {
		return nil, fixHint(fmt.Errorf("%s:%w", path, err))
	}
	return p, nil
}

// check applies the field rules of Plan 6b §4.3 to a decoded profile, root being its document's
// mapping: each error starts with the line it is about and a colon, but for entries whose source
// is a marketplace address, which once every other rule passed are reported together in a
// *manifest.UnresolvedError without its path (Plan 6c §6.7).
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
	case p.remote && p.Extends != nil:
		return errs.WithHints(fmt.Errorf("%d: a remote profile cannot extend another", at("extends")),
			"only your profiles and a project's may use extends:")
	case len(p.Extends) > MaxExtends:
		return fmt.Errorf("%d: extends lists more than %d profiles", at("extends"), MaxExtends)
	}
	p.extendsAt = make([]int, len(p.Extends))
	if n := yamldoc.Value(root, "extends"); n != nil {
		for i := range p.Extends {
			p.extendsAt[i] = n.Line
			if i < len(n.Content) {
				p.extendsAt[i] = n.Content[i].Line
			}
		}
	}
	for i, name := range p.Extends {
		// An invalid name reads as a missing remote profile does (ExtendsError, §19 S4).
		if module.ValidateName("profile", name) != nil {
			return p.ExtendsError("", i)
		}
		if slices.Contains(p.Extends[:i], name) {
			return errs.WithHints(fmt.Errorf("%d: extends lists %s twice", p.extendsAt[i], name), "name each profile once")
		}
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

// ExtendsError is the refusal of the k-th name in Extends, of the profile read from path ("" when
// the caller adds the path), as one that names no remote profile you have (Plan 6b §4.3): an
// invalid name, a missing remote profile and a profile of another kind read alike, so the error
// never says which remote profiles exist (§19 S4).
func (p *Profile) ExtendsError(path string, k int) error {
	err := fmt.Errorf("%d: extends: %s is not a remote profile you have", p.extendsAt[k], untrusted.Text(p.Extends[k], untrusted.MaxName))
	const hint = "extends: names remote profiles you have added; see them: lola profile --user"
	if path == "" {
		return errs.WithHints(err, hint)
	}
	return errs.WithHints(fmt.Errorf("%s:%w", path, err), hint, p.hint)
}

// Pin is the file at path, from which p was read, with each entry of pins taking its commit and
// digest (Plan 6b §6.4, §19 R1): the bytes p was read from, so what lola writes is what the run
// checked (§19 S6), with those fields alone edited and comments and every other key kept. pins
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
	if _, err := parse(path, &check, out, p.hint, false); err != nil {
		return nil, errs.WithHints(fmt.Errorf("not writing %s, which lola could not load again: %w", untrusted.Text(path, untrusted.MaxName), err),
			"nothing was written")
	}
	return out, nil
}

// Unchanged checks, just before lola writes the profile at path of the project at root, that
// nothing moved since p was read (ruling R-6b3a-4): .lola/ and .lola/profiles/ are still
// directories of the project's own, and the file still holds the bytes p was read from. Any
// change refuses the write.
func (p *Profile) Unchanged(root, path string) error {
	if found, err := ownDirs(root); err != nil || !found {
		return errors.Join(fmt.Errorf("%s changed while lola was running; not writing its pins", untrusted.Text(path, untrusted.MaxName)), err)
	}
	_, now, err := yamldoc.ReadNoFollow(path, Version)
	if err != nil || !bytes.Equal(now, p.read) {
		return errs.WithHints(errors.Join(fmt.Errorf("%s changed while lola was running; not writing its pins", untrusted.Text(path, untrusted.MaxName)), err),
			"run the command again")
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
