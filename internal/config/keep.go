package config

import (
	"fmt"
	"slices"

	"go.yaml.in/yaml/v3"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
	"github.com/trevor-vaughan/lola-go/internal/yamldoc"
)

// keepTemplate starts a keep.yaml lola creates.
const keepTemplate = "# lola keep-list: what `lola tidy` never offers. Safe to edit by hand.\nversion: 1\n"

// keepHint says what a keep.yaml list holds.
const keepHint = "write a list of kind/name selectors, e.g. [skill/my-notes, mcp/github]"

// keepForms is the hint a bad keep entry carries: entries are never module-qualified.
const keepForms = "a keep entry is kind/name; kind is one of skill, command, agent, mcp, rule; name may be a glob (scratch-*), and \\ escapes * ? [ \\"

// keepSection is one projects entry of keep.yaml: Key is canonical, Written is the key as the
// file spells it.
type keepSection struct {
	Key, Written string
	Entries      []Pattern
}

// Keep is keep.yaml (Plan 6d §5), validated: per scope, the selectors of what lola tidy never
// offers. The file is the user's own configuration; a section applies to its scope only.
type Keep struct {
	Path     string
	user     []Pattern
	projects []keepSection
}

// LoadKeep reads keep.yaml; a missing or empty file keeps nothing.
func LoadKeep(path string) (*Keep, error) {
	k, _, err := loadKeep(path)
	return k, err
}

// loadKeep also returns the document node, nil when the file is missing or blank.
func loadKeep(path string) (*Keep, *yaml.Node, error) {
	k := &Keep{Path: path}
	d, _, err := yamldoc.Read(path, Version)
	if err != nil {
		return nil, nil, err
	}
	root := yamldoc.Root(d)
	if root == nil {
		return k, d, nil
	}
	dc := doc{path: path}
	err = dc.mapping(root, "", map[string]func(*yaml.Node, string) error{
		"version": dc.version,
		"user": func(n *yaml.Node, at string) (err error) {
			k.user, err = dc.keepList(n, at)
			return err
		},
		"projects": func(n *yaml.Node, at string) error {
			if isNull(n) {
				return nil
			}
			if n.Kind != yaml.MappingNode {
				return dc.fail(n, at, "must be a mapping of project paths, not "+kindName(n), "write /absolute/project/path: followed by a list of selectors")
			}
			for i := 0; i+1 < len(n.Content); i += 2 {
				key, v := n.Content[i], n.Content[i+1]
				p, err := dc.projectKey(key, at)
				if err != nil {
					return err
				}
				for _, q := range k.projects {
					if paths.SameKey(q.Key, p.Key) {
						return dc.fail(key, join(at, key.Value), fmt.Sprintf("names the same project as %q", untrusted.Text(q.Written, untrusted.MaxName)),
							"merge the two entries into one")
					}
				}
				entries, err := dc.keepList(v, join(at, key.Value))
				if err != nil {
					return err
				}
				k.projects = append(k.projects, keepSection{Key: p.Key, Written: p.Written, Entries: entries})
			}
			return nil
		},
	}, "version")
	if err != nil {
		return nil, nil, err
	}
	return k, d, nil
}

// keepList decodes one section's list of selectors.
func (d doc) keepList(n *yaml.Node, at string) ([]Pattern, error) {
	if isNull(n) {
		return nil, nil
	}
	if n.Kind != yaml.SequenceNode {
		return nil, d.fail(n, at, "must be a list, not "+kindName(n), keepHint)
	}
	var out []Pattern
	for i, c := range n.Content {
		sub := fmt.Sprintf("%s[%d]", at, i)
		s, err := d.scalar(c, sub, keepHint)
		if err != nil {
			return nil, err
		}
		p, err := ParsePattern(s)
		if err != nil {
			return nil, d.fail(c, sub, err.Error(), keepForms)
		}
		out = append(out, p)
	}
	return out, nil
}

// Entries returns the selectors of the section w names, User() or a Project, in file order.
func (k *Keep) Entries(w Where) []Pattern {
	if w.Kind == SectionUser {
		return k.user
	}
	for _, p := range k.projects {
		if paths.SameKey(p.Key, w.Project) {
			return p.Entries
		}
	}
	return nil
}

// SaveKeep edits the section w of keep.yaml at path; the caller holds the keep lock. The file
// is read again, add is appended where the section does not already hold a selector as text,
// and every entry equal to one in remove, as text, goes. A section left empty goes unless it
// carries a comment. Comments and order are kept, and a symlinked file is written at its
// target. A file that does not load is never overwritten.
func SaveKeep(path string, w Where, add, remove []Pattern) error {
	_, d, err := loadKeep(path)
	if err != nil {
		return errs.WithHints(fmt.Errorf("not saving your keep-list: %w", err), "fix keep.yaml by hand, then rerun; nothing was written to it")
	}
	var root *yaml.Node
	switch {
	case d == nil:
		d = &yaml.Node{}
		if err := yaml.Unmarshal([]byte(keepTemplate), d); err != nil {
			return err
		}
		root = d.Content[0]
	case yamldoc.Root(d) == nil:
		root = yamldoc.NullDocRoot(d, Version)
	default:
		root = d.Content[0]
	}
	parent, key := root, SectionUser
	if w.Kind == SectionProject {
		parent, key = mappingAt(root, "projects"), w.Project
		for i := 0; i+1 < len(parent.Content); i += 2 {
			k, err := paths.ProjectKey(parent.Content[i].Value)
			if err != nil {
				return err
			}
			if paths.SameKey(k, w.Project) {
				key = parent.Content[i].Value
				break
			}
		}
	}
	list := yamldoc.Value(parent, key)
	if list == nil {
		list = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		parent.Content = append(parent.Content, yamldoc.Str(key), list)
	} else if list.Kind != yaml.SequenceNode {
		list.Kind, list.Tag, list.Value, list.Style, list.Content = yaml.SequenceNode, "!!seq", "", 0, nil
	}
	list.Content = slices.DeleteFunc(list.Content, func(n *yaml.Node) bool {
		return slices.ContainsFunc(remove, func(p Pattern) bool { return p.String() == n.Value })
	})
	for _, p := range add {
		if !slices.ContainsFunc(list.Content, func(n *yaml.Node) bool { return n.Value == p.String() }) {
			list.Content = append(list.Content, yamldoc.Str(p.String()))
		}
	}
	if len(list.Content) == 0 {
		// An empty list under a commented key would lose the comment or not encode as YAML; an
		// empty value keeps it.
		list.Kind, list.Tag, list.Style = yaml.ScalarNode, "!!null", 0
		if dropUnlessCommented(parent, key) && w.Kind == SectionProject && len(parent.Content) == 0 {
			dropUnlessCommented(root, "projects")
		}
	}
	return writeDoc(path, d, "nothing was written to your keep-list")
}
