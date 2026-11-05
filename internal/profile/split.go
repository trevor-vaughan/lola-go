package profile

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"

	"go.yaml.in/yaml/v3"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
	"github.com/trevor-vaughan/lola-go/internal/yamldoc"
)

// Mixed is a profile file with both bundle: and modules:, read for lola
// profile --split: the bytes the split rewrites, read as Load reads yours or, for a
// project's, as LoadProject does.
type Mixed struct {
	Path    string
	read    []byte
	project bool
}

// ReadMixed reads the mixed file at path, a project's when project is set (never through a
// link). A file that does not have both bundle: and modules: is refused: it has nothing to split;
// so is one its loader refuses for anything but having both (a bundle: that is no list, say), as
// the split would rewrite what it cannot read. The halves are checked when the caller parses
// Split's bytes.
func ReadMixed(path string, project bool) (*Mixed, error) {
	read, hint := yamldoc.Read, "fix it by hand"
	if project {
		hint = "fix it by hand, or restore it from version control"
		if fi, err := os.Lstat(path); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
			return nil, errs.WithHints(fmt.Errorf("%s is a symbolic link; a project's profile must be a file of its own", untrusted.Text(path, untrusted.MaxName)),
				"replace it with the file it points to", hint)
		}
		read = yamldoc.ReadNoFollow
	}
	d, b, err := read(path, Version)
	switch {
	case err != nil:
		return nil, errs.WithHints(errors.New(err.Error()), hint)
	case b == nil:
		return nil, fmt.Errorf("%s: %w", untrusted.Text(path, untrusted.MaxName), fs.ErrNotExist)
	}
	root := yamldoc.Root(d)
	if root == nil || yamldoc.Value(root, "bundle") == nil || yamldoc.Value(root, "modules") == nil {
		return nil, fmt.Errorf("%s does not have both bundle: and modules:; there is nothing to split", untrusted.Text(path, untrusted.MaxName))
	}
	parse := Parse
	if project {
		parse = ParseProject
	}
	_, err = parse(path, b)
	if _, mixed := errors.AsType[*MixedError](err); !mixed {
		if err == nil {
			return nil, fmt.Errorf("%s does not have both bundle: and modules:; there is nothing to split", untrusted.Text(path, untrusted.MaxName))
		}
		return nil, err
	}
	return &Mixed{Path: path, read: b, project: project}, nil
}

// Split is the two files m becomes: bundle, m's file without modules: and
// targets:, its bundle: listing its members and then modules (the new profile's name), every other
// key and comment kept; and profile, a new file of version, m's targets: and its modules:, each
// moved whole with its comments.
func (m *Mixed) Split(modules string) (bundle, profile []byte, err error) {
	var d yaml.Node
	if err := yaml.Unmarshal(m.read, &d); err != nil {
		return nil, nil, err
	}
	root := yamldoc.Root(&d)
	// Each key moves with its value, so the comments on the key's line and above it move too.
	moved := func(key string) []*yaml.Node {
		for k := 0; k+1 < len(root.Content); k += 2 {
			if root.Content[k].Value == key {
				pair := root.Content[k : k+2 : k+2]
				root.Content = append(root.Content[:k:k], root.Content[k+2:]...)
				return pair
			}
		}
		return nil
	}
	targets, mods := moved("targets"), moved("modules")
	list := yamldoc.Value(root, "bundle") // a list: ReadMixed refused any other bundle:
	list.Content = append(list.Content, yamldoc.Str(modules))
	if bundle, err = encode(&d); err != nil {
		return nil, nil, err
	}
	p := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
		yamldoc.Str("version"), {Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(Version)}}}
	p.Content = append(append(p.Content, targets...), mods...)
	profile, err = encode(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{p}})
	return bundle, profile, err
}

// Current checks, just before the split rewrites m's file, that it still holds the bytes it was
// read from; for a project's, under root, that .lola/ and .lola/profiles/ are still the project's
// own (Profile.Current, Profile.Unchanged).
func (m *Mixed) Current(root string) error {
	p := &Profile{read: m.read, project: m.project, hint: "fix it by hand"}
	if m.project {
		return p.Unchanged(root, m.Path)
	}
	return p.Current(m.Path)
}

// encode is d as lola writes a YAML file: two-space indent.
func encode(d *yaml.Node) ([]byte, error) {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := errors.Join(enc.Encode(d), enc.Close()); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
