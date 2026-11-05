package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
	"go.yaml.in/yaml/v3"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/fsx"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
	"github.com/trevor-vaughan/lola-go/internal/yamldoc"
)

// template starts a selections.yaml lola creates.
const template = `# lola selections: components you chose not to install.
# Written by ` + "`lola install`" + `; safe to edit by hand; takes effect on the next install or render.
# A scope's section overrides global. Project keys are absolute paths (no ~).
version: 1
`

// Change replaces one module's entry in one section; an empty Entry removes it.
type Change struct {
	Section Where
	Module  string
	Entry   Entry
}

// Lock takes the selections lock. Callers take it before any scope lock, so two commands
// never wait on each other's locks in opposite order.
func Lock(lockPath string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, err
	}
	fl := flock.New(lockPath)
	if err := fl.Lock(); err != nil {
		return nil, fmt.Errorf("locking %s: %w", lockPath, err)
	}
	return func() { _ = fl.Unlock() }, nil
}

// Save writes changes to selections.yaml at path; the caller holds the lock. The file is read
// again, so only the changed modules' entries are replaced and edits made since the caller's
// read survive; comments and order are kept. A file that does not load is never overwritten.
// A symlinked file is written at its target, keeping the link.
func Save(path string, changes []Change) error {
	_, d, err := loadSelections(path)
	if err != nil {
		return errs.WithHints(fmt.Errorf("not saving your selections: %w", err), "your picks were not written and nothing was installed")
	}
	var root *yaml.Node
	switch {
	case d == nil:
		d = &yaml.Node{}
		if err := yaml.Unmarshal([]byte(template), d); err != nil {
			return err
		}
		root = d.Content[0]
	case yamldoc.Root(d) == nil:
		root = yamldoc.NullDocRoot(d, Version)
	default:
		root = d.Content[0]
	}
	for _, c := range changes {
		if c.Entry.Empty() {
			if err := removeEntry(root, c.Section, c.Module); err != nil {
				return err
			}
			continue
		}
		sec, err := sectionNode(root, c.Section)
		if err != nil {
			return err
		}
		setEntry(sec, c.Module, c.Entry)
	}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(d); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	dst := path
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		if dst, err = filepath.EvalSymlinks(path); err != nil {
			return errs.WithHints(fmt.Errorf("%s is a symlink lola cannot follow: %w", path, untrusted.CleanErr(err)), "point the link at a file, or remove it")
		}
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if err := fsx.WriteFileAtomic(dst, b.Bytes(), 0o600); err != nil {
		return errs.WithHints(fmt.Errorf("could not write %s: %w", untrusted.Text(dst, untrusted.MaxName), untrusted.CleanErr(err)),
			"close any program that holds it open and check its directory's permissions, then rerun",
			"nothing was installed")
	}
	return nil
}

// removeEntry deletes module from the section w without creating anything. If that removal
// took out the section's last module, the section (or the project key, and projects itself)
// is dropped too, unless it carries a comment, in which case it stays as an empty mapping.
func removeEntry(root *yaml.Node, w Where, module string) error {
	parent, key := root, w.Kind
	if w.Kind == SectionProject {
		projects := yamldoc.Value(root, "projects")
		if projects == nil || projects.Kind != yaml.MappingNode {
			return nil
		}
		parent, key = projects, ""
		for i := 0; i+1 < len(projects.Content); i += 2 {
			k, err := paths.ProjectKey(projects.Content[i].Value)
			if err != nil {
				return err
			}
			if paths.SameKey(k, w.Project) {
				key = projects.Content[i].Value
				break
			}
		}
	}
	sec := yamldoc.Value(parent, key)
	if key == "" || sec == nil || sec.Kind != yaml.MappingNode {
		return nil
	}
	if !yamldoc.RemoveKey(sec, module) || len(sec.Content) > 0 {
		return nil
	}
	if !dropUnlessCommented(parent, key) {
		return nil
	}
	if w.Kind == SectionProject && len(parent.Content) == 0 {
		dropUnlessCommented(root, "projects")
	}
	return nil
}

// sectionNode returns the mapping of the section w under root, creating it.
func sectionNode(root *yaml.Node, w Where) (*yaml.Node, error) {
	if w.Kind != SectionProject {
		return mappingAt(root, w.Kind), nil
	}
	projects := mappingAt(root, "projects")
	for i := 0; i+1 < len(projects.Content); i += 2 {
		key, err := paths.ProjectKey(projects.Content[i].Value)
		if err != nil {
			return nil, err
		}
		if paths.SameKey(key, w.Project) {
			return mappingAt(projects, projects.Content[i].Value), nil
		}
	}
	return mappingAt(projects, w.Project), nil
}

// mappingAt returns the mapping under key in m, adding the key or turning an empty value into
// a mapping.
func mappingAt(m *yaml.Node, key string) *yaml.Node {
	if v := yamldoc.Value(m, key); v != nil {
		if v.Kind != yaml.MappingNode {
			v.Kind, v.Tag, v.Value, v.Style, v.Content = yaml.MappingNode, "!!map", "", 0, nil
		}
		return v
	}
	v := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	m.Content = append(m.Content, yamldoc.Str(key), v)
	return v
}

// setEntry replaces module's entry in sec; an empty entry removes the module's key.
func setEntry(sec *yaml.Node, module string, e Entry) {
	if e.Empty() {
		yamldoc.RemoveKey(sec, module)
		return
	}
	m := mappingAt(sec, module)
	setList(m, "skip", e.Skip)
	setList(m, "include", e.Include)
}

// setList writes a list under key in m, keeping an existing list node's style and comments;
// an empty list removes the key.
func setList(m *yaml.Node, key string, ps []Pattern) {
	if len(ps) == 0 {
		yamldoc.RemoveKey(m, key)
		return
	}
	items := make([]*yaml.Node, 0, len(ps))
	for _, p := range ps {
		items = append(items, yamldoc.Str(p.String()))
	}
	if v := yamldoc.Value(m, key); v != nil {
		if v.Kind != yaml.SequenceNode {
			v.Kind, v.Tag, v.Value, v.Style = yaml.SequenceNode, "!!seq", "", yaml.FlowStyle
		}
		v.Content = items
		return
	}
	m.Content = append(m.Content, yamldoc.Str(key), &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle, Content: items})
}

// dropUnlessCommented removes key from m unless the key or its value carries a comment, which
// is the user's and stays; it reports whether the key was removed.
func dropUnlessCommented(m *yaml.Node, key string) bool {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value != key {
			continue
		}
		for _, n := range m.Content[i : i+2] {
			if n.HeadComment != "" || n.LineComment != "" || n.FootComment != "" {
				return false
			}
		}
		m.Content = append(m.Content[:i], m.Content[i+2:]...)
		return true
	}
	return false
}
