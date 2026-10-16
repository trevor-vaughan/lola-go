package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"

	"go.yaml.in/yaml/v3"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
	"github.com/trevor-vaughan/lola-go/internal/yamldoc"
)

// Change is an edit lola makes to a manifest. Targets, when not nil, become the top-level
// targets; each entry of Set replaces the fields of the entry with its name, or is appended
// when there is none; each name in Remove loses its entry.
type Change struct {
	Targets []string
	Set     []Entry
	Remove  []string
}

// topKeys and entryKeys are the order lola writes keys in; a key lola adds goes after the
// keys that come before it in this order.
var (
	topKeys   = []string{"version", "targets", "modules"}
	entryKeys = []string{"name", "source", "ref", "path", "commit", "digest", "targets", "rules"}
)

// Edit returns the file to write and the manifest at path with c applied (Plan 6a §4.5). The
// file is read again, so the caller holds the scope lock and edits made since its own read
// survive; only the entries and fields c changes are edited, so comments, order and
// formatting stay. A file that does not load is never edited. A symlinked manifest is written
// at its target. The caller writes the bytes with mode 0600 (plan.Apply stages them with the
// ledger).
func Edit(path string, c Change) (string, []byte, error) {
	if _, err := Load(path); err != nil {
		return "", nil, errs.WithHints(fmt.Errorf("not editing it: %w", err), "nothing was installed or removed")
	}
	d, _, err := yamldoc.Read(path, Version)
	if err != nil {
		return "", nil, err
	}
	var root *yaml.Node
	switch {
	case d == nil:
		root = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		d = &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}
		setKey(root, topKeys, "version", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(Version)})
	case yamldoc.Root(d) == nil:
		root = yamldoc.NullDocRoot(d, Version)
	default:
		root = d.Content[0]
	}
	if c.Targets != nil {
		setKey(root, topKeys, "targets", flowList(c.Targets))
	}
	mods := yamldoc.Value(root, "modules")
	if mods == nil || mods.Kind != yaml.SequenceNode {
		mods = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	}
	mods.Content = slices.DeleteFunc(mods.Content, func(n *yaml.Node) bool { return slices.Contains(c.Remove, entryName(n)) })
	for _, e := range c.Set {
		i := slices.IndexFunc(mods.Content, func(n *yaml.Node) bool { return entryName(n) == e.Name })
		if i < 0 {
			mods.Content = append(mods.Content, &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"})
			i = len(mods.Content) - 1
		}
		setEntry(mods.Content[i], e)
	}
	if len(mods.Content) == 0 {
		yamldoc.RemoveKey(root, "modules")
	} else {
		mods.Style &^= yaml.FlowStyle
		setKey(root, topKeys, "modules", mods)
	}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := errors.Join(enc.Encode(d), enc.Close()); err != nil {
		return "", nil, err
	}
	if err := (&Manifest{}).decode(b.Bytes()); err != nil {
		return "", nil, errs.WithHints(fmt.Errorf("not writing %s, which lola could not load again: %w", path, err),
			"nothing was written; if the module came from your cache, remove it with lola cache rm, then install it again")
	}
	dst := path
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		if dst, err = filepath.EvalSymlinks(path); err != nil {
			return "", nil, errs.WithHints(fmt.Errorf("%s is a symlink lola cannot follow: %w", path, untrusted.CleanErr(err)), "point the link at a file, or remove it")
		}
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", nil, err
	}
	return dst, b.Bytes(), nil
}

// entryName is the name of a modules item, or "".
func entryName(n *yaml.Node) string {
	if n.Kind != yaml.MappingNode {
		return ""
	}
	if v := yamldoc.Value(n, "name"); v != nil {
		return v.Value
	}
	return ""
}

// setEntry makes the mapping m hold e's fields: a field e leaves empty is removed, and a
// field whose value is unchanged keeps its node, comments and style.
func setEntry(m *yaml.Node, e Entry) {
	for _, f := range []struct{ key, value string }{{"name", e.Name}, {"source", e.Source}, {"ref", e.Ref}, {"path", e.Path},
		{"commit", e.Commit}, {"digest", e.Digest}} {
		var v *yaml.Node
		if f.value != "" {
			v = yamldoc.Str(f.value)
		}
		setKey(m, entryKeys, f.key, v)
	}
	var ts, rules *yaml.Node
	if len(e.Targets) > 0 {
		ts = flowList(e.Targets)
	}
	if e.Rules != nil {
		rules = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(*e.Rules)}
	}
	setKey(m, entryKeys, "targets", ts)
	setKey(m, entryKeys, "rules", rules)
}

// flowList is a flow list of strings.
func flowList(ss []string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
	for _, s := range ss {
		n.Content = append(n.Content, yamldoc.Str(s))
	}
	return n
}

// setKey sets key in the mapping m to v, or removes it when v is nil. An existing value that
// already reads as v is left alone; another one takes v's value in place, keeping its
// comments. A new key goes after the last key of m that comes before it in order.
func setKey(m *yaml.Node, order []string, key string, v *yaml.Node) {
	if v == nil {
		yamldoc.RemoveKey(m, key)
		return
	}
	if cur := yamldoc.Value(m, key); cur != nil {
		if !sameValue(cur, v) {
			cur.Kind, cur.Tag, cur.Value, cur.Content = v.Kind, v.Tag, v.Value, v.Content
			if cur.Kind != yaml.ScalarNode || cur.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
				cur.Style = v.Style
			}
		}
		return
	}
	at := 0
	rank := slices.Index(order, key)
	for i := 0; i+1 < len(m.Content); i += 2 {
		if r := slices.Index(order, m.Content[i].Value); r >= 0 && r < rank {
			at = i + 2
		}
	}
	m.Content = slices.Insert(m.Content, at, yamldoc.Str(key), v)
}

// sameValue reports whether two nodes hold the same scalar or list of scalars.
func sameValue(a, b *yaml.Node) bool {
	if a.Kind != b.Kind || a.Value != b.Value || len(a.Content) != len(b.Content) {
		return false
	}
	if a.Kind == yaml.ScalarNode && a.ShortTag() != b.ShortTag() {
		return false
	}
	for i := range a.Content {
		if !sameValue(a.Content[i], b.Content[i]) {
			return false
		}
	}
	return true
}
