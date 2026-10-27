package source

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Components are the skills, commands and agents a marketplace's catalog lists for a relative
// entry (Plan 6c §4.1, user decision of 2026-10-21): paths in the module's directory, cleaned and
// slash-separated, a skill a directory, a command or an agent a file or a directory. The module is
// those alone, each at <kind>/<its last element>; every fetch of a source that records them
// selects them, so the module is the same wherever it is fetched again.
type Components struct {
	Skills   []string `yaml:"skills,omitempty,flow" json:"skills,omitempty"`
	Commands []string `yaml:"commands,omitempty,flow" json:"commands,omitempty"`
	Agents   []string `yaml:"agents,omitempty,flow" json:"agents,omitempty"`
}

// componentKinds are the lists of Components, by the directory each selects into.
var componentKinds = []string{"skills", "commands", "agents"}

// lists is c's lists in componentKinds order.
func (c *Components) lists() [][]string { return [][]string{c.Skills, c.Commands, c.Agents} }

// Clone is a copy of c that shares nothing with it; nil for nil.
func (c *Components) Clone() *Components {
	if c == nil {
		return nil
	}
	return &Components{Skills: slices.Clone(c.Skills), Commands: slices.Clone(c.Commands), Agents: slices.Clone(c.Agents)}
}

// Equal reports whether c and o select the same paths; nil selects nothing.
func (c *Components) Equal(o *Components) bool {
	if c == nil || o == nil {
		return c == nil && o == nil
	}
	return slices.Equal(c.Skills, o.Skills) && slices.Equal(c.Commands, o.Commands) && slices.Equal(c.Agents, o.Agents)
}

// String is c as a message shows it, "skills skills/xlsx, skills/docx; commands commands/x.md",
// every path through untrusted.Text: a file may be someone else's.
func (c *Components) String() string {
	var parts []string
	for i, l := range c.lists() {
		if len(l) == 0 {
			continue
		}
		shown := make([]string, len(l))
		for k, p := range l {
			shown[k] = untrusted.Text(p, untrusted.MaxName)
		}
		parts = append(parts, componentKinds[i]+" "+strings.Join(shown, ", "))
	}
	return strings.Join(parts, "; ")
}

// MaxComponents is the most paths a selection may name: Check runs on every load of a file that
// records one, and a catalog or a pulled file is someone else's.
const MaxComponents = 1024

// Check refuses a selection lola would not make or could not apply (a file may be someone
// else's): more than MaxComponents paths; a path that is empty, not as it prints, not clean,
// absolute, holding a backslash or a ".." element, or the module's directory itself; two paths of
// one list sharing their last element; a path inside another, or one path listed twice; and lists
// that select nothing. Each error names the path.
func (c *Components) Check() error {
	if n := len(c.Skills) + len(c.Commands) + len(c.Agents); n > MaxComponents {
		return fmt.Errorf("selects %d paths; lola takes at most %d", n, MaxComponents)
	}
	var all []string
	for i, l := range c.lists() {
		names := map[string]bool{}
		for _, p := range l {
			shown := untrusted.Text(p, untrusted.MaxName)
			switch {
			case p == "" || p == "." || !untrusted.IsPlain(p) || strings.Contains(p, `\`) || path.IsAbs(p) || path.Clean(p) != p ||
				slices.Contains(strings.Split(p, "/"), ".."):
				return fmt.Errorf("%s path %q is not a clean relative path inside the module's directory", componentKinds[i], shown)
			case names[path.Base(p)]:
				return fmt.Errorf("%s lists two paths named %s", componentKinds[i], untrusted.Text(path.Base(p), untrusted.MaxName))
			}
			names[path.Base(p)] = true
			all = append(all, p)
		}
	}
	if len(all) == 0 {
		return errors.New("selects no skill, command or agent")
	}
	set := make(map[string]bool, len(all))
	for _, p := range all {
		if set[p] {
			return fmt.Errorf("%s and %s overlap", untrusted.Text(p, untrusted.MaxName), untrusted.Text(p, untrusted.MaxName))
		}
		set[p] = true
	}
	for _, p := range all {
		for d := path.Dir(p); d != "."; d = path.Dir(d) {
			if set[d] {
				return fmt.Errorf("%s and %s overlap", untrusted.Text(d, untrusted.MaxName), untrusted.Text(p, untrusted.MaxName))
			}
		}
	}
	return nil
}

// selectComponents makes f, a fetch of the source spec, the components spec selects (Components):
// each path, which must be in the fetched tree as its kind needs and never a symbolic link, moves
// to <kind>/<its last element> of a new directory under stagingParent, with its executables; the
// rest of the tree is removed.
func selectComponents(f Fetched, spec Spec, stagingParent string) (_ Fetched, err error) {
	dir, err := os.MkdirTemp(stagingParent, "select-*")
	if err != nil {
		return Fetched{}, errors.Join(err, os.RemoveAll(f.Dir))
	}
	defer func() {
		err = errors.Join(err, os.RemoveAll(f.Dir))
		if err != nil {
			err = errors.Join(err, os.RemoveAll(dir))
		}
	}()
	execs := []string{}
	for i, l := range spec.Components.lists() {
		kind := componentKinds[i]
		for _, p := range l {
			fi, err := realFile(f.Dir, p)
			switch {
			case errors.Is(err, fs.ErrNotExist):
				return Fetched{}, fmt.Errorf("%s does not hold %s, which it selects among its %s", untrusted.Text(spec.Location, untrusted.MaxName),
					untrusted.Text(p, untrusted.MaxName), kind)
			case err != nil:
				return Fetched{}, err
			case kind == "skills" && !fi.IsDir(), !fi.IsDir() && !fi.Mode().IsRegular():
				what := "a file or a directory"
				if kind == "skills" {
					what = "a directory"
				}
				return Fetched{}, fmt.Errorf("%s selects %s among its %s, which is not %s", untrusted.Text(spec.Location, untrusted.MaxName),
					untrusted.Text(p, untrusted.MaxName), kind, what)
			}
			to := path.Join(kind, path.Base(p))
			if err := os.MkdirAll(filepath.Join(dir, kind), 0o700); err != nil {
				return Fetched{}, err
			}
			if err := os.Rename(filepath.Join(f.Dir, filepath.FromSlash(p)), filepath.Join(dir, filepath.FromSlash(to))); err != nil {
				return Fetched{}, err
			}
			for _, x := range f.Executables {
				if x == p || strings.HasPrefix(x, p+"/") {
					execs = append(execs, to+strings.TrimPrefix(x, p))
				}
			}
		}
	}
	slices.Sort(execs)
	return Fetched{Dir: dir, Name: f.Name, Commit: f.Commit, Executables: execs, Root: f.Root}, nil
}

// realFile is the file at rel, a clean slash path under root, refusing a symbolic link anywhere
// on the way or at the end.
func realFile(root, rel string) (fs.FileInfo, error) {
	cur := root
	var fi fs.FileInfo
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		cur = filepath.Join(cur, part)
		var err error
		if fi, err = os.Lstat(cur); err != nil {
			return nil, err
		}
		if i < len(parts)-1 && !fi.IsDir() && fi.Mode()&fs.ModeSymlink == 0 {
			return nil, fs.ErrNotExist
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return nil, fmt.Errorf("%s is a symbolic link; lola never follows links in a source", untrusted.Text(rel, untrusted.MaxName))
		}
	}
	return fi, nil
}
