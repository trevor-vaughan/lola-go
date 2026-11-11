package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

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
# A scope's section overrides global. Project keys are git:<host>/<path>#<dir> for a clone, else absolute paths (no ~).
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
// A symlinked file is written at its target, keeping the link. A change to a section the file
// does not hold, a profile entry's picks or the user section, is refused before anything is read.
func Save(path string, changes []Change) error {
	for _, c := range changes {
		if c.Section.Kind == SectionProfile || c.Section.Kind == SectionUser {
			return fmt.Errorf("not saving your selections: selections.yaml has no %s section", c.Section)
		}
	}
	_, d, err := loadSelections(path)
	if err != nil {
		return errs.WithHints(fmt.Errorf("not saving your selections: %w", err), "your picks were not written and nothing was installed")
	}
	d, root, err := docRoot(d, template)
	if err != nil {
		return err
	}
	for _, c := range changes {
		if c.Entry.Empty() {
			if err := removeEntry(root, c.Section, c.Module); err != nil {
				return err
			}
			continue
		}
		sec, err := sectionNode(root, c.Section, c.Module)
		if err != nil {
			return err
		}
		setEntry(sec, c.Module, c.Entry)
	}
	return writeDoc(path, d, "nothing was installed")
}

// configTemplate starts a config.yaml lola creates.
const configTemplate = "# lola settings. Safe to edit by hand.\nversion: 1\n"

// docRoot is d, a document yamldoc.Read returned, with its root mapping; a missing file (nil d)
// starts from tmpl, and a file with no content gets the version key.
func docRoot(d *yaml.Node, tmpl string) (*yaml.Node, *yaml.Node, error) {
	switch {
	case d == nil:
		d = &yaml.Node{}
		if err := yaml.Unmarshal([]byte(tmpl), d); err != nil {
			return nil, nil, err
		}
		return d, d.Content[0], nil
	case yamldoc.Root(d) == nil:
		return d, yamldoc.NullDocRoot(d, Version), nil
	}
	return d, d.Content[0], nil
}

// loadConfigDoc checks that config.yaml at path loads, then returns its document and root
// mapping (docRoot). what names what the caller was saving, for the refusal.
func loadConfigDoc(path, home, what string) (*yaml.Node, *yaml.Node, error) {
	if _, err := LoadConfig(path, home); err != nil {
		return nil, nil, errs.WithHints(fmt.Errorf("not saving your %s: %w", what, err), "fix config.yaml by hand, then rerun; nothing was written to it")
	}
	d, _, err := yamldoc.Read(path, Version)
	if err != nil {
		return nil, nil, err
	}
	return docRoot(d, configTemplate)
}

// SaveTheme writes theme: theme to config.yaml at path, replacing the key's value
// or adding the key last; comments, order and every other key are kept. home is LoadConfig's.
// A file that does not load is never overwritten, and a symlinked file is written at its target.
func SaveTheme(path, home, theme string) error {
	d, root, err := loadConfigDoc(path, home, "theme")
	if err != nil {
		return err
	}
	if v := yamldoc.Value(root, "theme"); v != nil {
		v.Value, v.Tag, v.Style = theme, "!!str", 0
	} else {
		root.Content = append(root.Content, yamldoc.Str("theme"), yamldoc.Str(theme))
	}
	return writeDoc(path, d, "lola asks again on its next run")
}

// SaveTargetVersions sets these ids' versions under target_versions: in config.yaml at path,
// keeping every other key and id, creating the file (and key) when missing; a new key goes
// last. home is LoadConfig's. A file that does not load is never overwritten, and a symlinked
// file is written at its target.
func SaveTargetVersions(path, home string, set map[string]string) error {
	if len(set) == 0 {
		return nil
	}
	d, root, err := loadConfigDoc(path, home, "assistant versions")
	if err != nil {
		return err
	}
	tv := yamldoc.Value(root, "target_versions")
	switch {
	case tv == nil:
		tv = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content = append(root.Content, yamldoc.Str("target_versions"), tv)
	case tv.Kind != yaml.MappingNode: // LoadConfig refuses every non-mapping value but null
		tv.Kind, tv.Tag, tv.Value, tv.Style, tv.Content = yaml.MappingNode, "!!map", "", 0, nil
	}
	yamldoc.SetQuoted(tv, set)
	return writeDoc(path, d, "lola asks again on its next run")
}

// writeDoc encodes d and writes it atomically to path at mode 0600, or at the target of the
// symbolic link path is; outcome says what a failed write means for the run.
func writeDoc(path string, d *yaml.Node, outcome string) error {
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
			outcome)
	}
	return nil
}

// removeEntry deletes module from the section w without creating anything. If that removal
// took out the section's last module, the section (or the project key, and projects itself)
// is dropped too, unless it carries a comment, in which case it stays as an empty mapping.
func removeEntry(root *yaml.Node, w Where, module string) error {
	parent, key := root, w.Kind
	if w.Kind == SectionProject {
		if err := rekeyNode(root, w, module); err != nil {
			return err
		}
		projects := yamldoc.Value(root, "projects")
		if projects == nil || projects.Kind != yaml.MappingNode {
			return nil
		}
		i, err := projectNode(projects, w.Project)
		if err != nil || i < 0 {
			return err
		}
		parent, key = projects, projects.Content[i].Value
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

// sectionNode returns the mapping of the section w under root, creating it; module is the
// module about to be written there (rekeyNode).
func sectionNode(root *yaml.Node, w Where, module string) (*yaml.Node, error) {
	if w.Kind != SectionProject {
		return mappingAt(root, w.Kind), nil
	}
	if err := rekeyNode(root, w, module); err != nil {
		return nil, err
	}
	projects := mappingAt(root, "projects")
	i, err := projectNode(projects, w.Project)
	if err != nil {
		return nil, err
	}
	if i >= 0 {
		return mappingAt(projects, projects.Content[i].Value), nil
	}
	return mappingAt(projects, w.Project), nil
}

// projectNode is the index in projects' Content of the key that names the project key, or -1:
// a repository key as written, a path key in any spelling of the path (sameKey).
func projectNode(projects *yaml.Node, key string) (int, error) {
	for i := 0; i+1 < len(projects.Content); i += 2 {
		k := projects.Content[i].Value
		if !strings.HasPrefix(k, repoKeyPrefix) {
			var err error
			if k, err = paths.ProjectKey(k); err != nil {
				return -1, err
			}
		}
		if sameKey(k, key) {
			return i, nil
		}
	}
	return -1, nil
}

// rekeyNode moves the section under w's path key (Where.Path) to its repository key before
// module's entry is written or removed there: with no section under
// the repository key, the path key is rewritten in place, keeping its comments and the file's
// other entries; else module's entry leaves the path key's section, which is dropped once empty
// unless it carries a comment (Selections.rekey).
func rekeyNode(root *yaml.Node, w Where, module string) error {
	projects := yamldoc.Value(root, "projects")
	if w.Path == "" || projects == nil || projects.Kind != yaml.MappingNode {
		return nil
	}
	old, err := projectNode(projects, w.Path)
	if err != nil || old < 0 {
		return err
	}
	repo, err := projectNode(projects, w.Project)
	if err != nil {
		return err
	}
	if repo < 0 {
		projects.Content[old].Value = w.Project
		return nil
	}
	key := projects.Content[old].Value
	if sec := yamldoc.Value(projects, key); sec != nil && sec.Kind == yaml.MappingNode && yamldoc.RemoveKey(sec, module) && len(sec.Content) == 0 {
		dropUnlessCommented(projects, key)
	}
	return nil
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
	setList(m, "only", e.Only)
}

// setList writes a list under key in m, keeping an existing list node's style and comments;
// an empty list removes the key.
func setList(m *yaml.Node, key string, ps []Pattern) {
	if len(ps) == 0 {
		yamldoc.RemoveKey(m, key)
		return
	}
	strs := make([]string, len(ps))
	for i, p := range ps {
		strs[i] = p.String()
	}
	fresh := yamldoc.StrList(strs)
	if v := yamldoc.Value(m, key); v != nil {
		if v.Kind != yaml.SequenceNode {
			v.Kind, v.Tag, v.Value, v.Style = yaml.SequenceNode, "!!seq", "", fresh.Style
		} else if len(strs) > yamldoc.MaxFlowItems {
			v.Style &^= yaml.FlowStyle // a list grown long is a block list, as a new one would be
		}
		v.Content = fresh.Content
		return
	}
	m.Content = append(m.Content, yamldoc.Str(key), fresh)
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
