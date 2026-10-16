// Package config reads and writes lola's two hand-editable files in $XDG_CONFIG_HOME/lola/:
// config.yaml (settings) and selections.yaml (components the user chose not to install). Both
// are untrusted input: they are size-capped and strictly validated, and every error names the
// file, the line and the key path, with a hint.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"go.yaml.in/yaml/v3"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Version is the schema version of both files.
const Version = 1

// maxFile caps either file.
const maxFile = 1 << 20

// Scope values of default_scope and --scope.
const (
	ScopeProject = "project"
	ScopeUser    = "user"
	ScopeBoth    = "both"
)

// Config is config.yaml.
type Config struct {
	DefaultScope string // "", project, user or both
}

// LoadConfig reads config.yaml; a missing or empty file is the default configuration.
func LoadConfig(path string) (*Config, error) {
	c := &Config{}
	d0, err := readDoc(path)
	root := rootOf(d0)
	if err != nil || root == nil {
		return c, err
	}
	d := doc{path: path}
	if err := d.mapping(root, "", map[string]func(*yaml.Node, string) error{
		"version": d.version,
		"default_scope": func(n *yaml.Node, at string) error {
			s, err := d.scalar(n, at, "use project, user or both")
			if err != nil {
				return err
			}
			if !slices.Contains([]string{ScopeProject, ScopeUser, ScopeBoth}, s) {
				return d.fail(n, at, fmt.Sprintf("%q is not a scope", untrusted.Text(s, untrusted.MaxName)), "use project, user or both")
			}
			c.DefaultScope = s
			return nil
		},
	}, "version"); err != nil {
		return nil, err
	}
	return c, nil
}

// readDoc reads path as one YAML document node: nil for a missing or empty file. A file that
// holds only comments reads as `version: 1` under those comments, so a rewrite keeps them. It
// refuses a file that is not regular, one over maxFile, a second document, and anchors,
// aliases and merge keys, which would make one entry stand for another and which lola could
// not keep when it rewrites the file.
func readDoc(path string) (*yaml.Node, error) {
	// Opened without blocking, then checked: opening a FIFO for reading would otherwise wait for
	// a writer, and checking the open file leaves no gap for a swap. Regular files ignore the flag.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err == nil && !fi.Mode().IsRegular() {
		err = errs.WithHints(fmt.Errorf("%s is not a regular file", path), "replace it with a YAML file, or remove it")
	}
	var b []byte
	if err == nil {
		b, err = io.ReadAll(io.LimitReader(f, maxFile+1))
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return nil, err
	}
	if len(b) > maxFile {
		return nil, errs.WithHints(fmt.Errorf("%s is larger than %d bytes", path, maxFile), "shorten it; lola never writes one this large")
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	var d yaml.Node
	if err := dec.Decode(&d); errors.Is(err, io.EOF) {
		if len(bytes.TrimSpace(b)) == 0 {
			return nil, nil
		}
		// Only comments: yaml.v3 drops them with an empty document, so give them one.
		text := string(b)
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		if err := yaml.Unmarshal([]byte(text+"version: 1\n"), &d); err != nil {
			return nil, err
		}
		return &d, nil
	} else if err != nil {
		return nil, errs.WithHints(fmt.Errorf("%s: does not parse: %s", path, untrusted.Text(err.Error(), 2*untrusted.MaxName)),
			"fix it by hand, or move it aside (lola starts a new one) and rerun")
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errs.WithHints(fmt.Errorf("%s: holds more than one YAML document", path), "remove every --- line after the first document")
	}
	if n := special(&d); n != nil {
		return nil, errs.WithHints(fmt.Errorf("%s:%d: anchors, aliases and merge keys (<<) are not supported", path, n.Line),
			"write each entry out in full")
	}
	return &d, nil
}

// rootOf returns a document's root node, or nil for no document or an empty one.
func rootOf(d *yaml.Node) *yaml.Node {
	if d == nil || len(d.Content) == 0 || isNull(d.Content[0]) {
		return nil
	}
	return d.Content[0]
}

// special returns the first node under n that is an anchor, an alias or a merge key.
func special(n *yaml.Node) *yaml.Node {
	if n.Anchor != "" || n.Kind == yaml.AliasNode || n.Tag == "!!merge" {
		return n
	}
	for _, c := range n.Content {
		if s := special(c); s != nil {
			return s
		}
	}
	return nil
}

// doc reports validation errors for one file.
type doc struct{ path string }

// fail is an error at node n, whose key path is at.
func (d doc) fail(n *yaml.Node, at, problem string, hints ...string) error {
	where := d.path + ":" + strconv.Itoa(n.Line)
	if at != "" {
		where += ": " + at
	}
	return errs.WithHints(fmt.Errorf("%s: %s", where, problem), hints...)
}

// kindName names a node kind for an error; never a Go type name.
func kindName(n *yaml.Node) string {
	switch {
	case n.Kind == yaml.MappingNode:
		return "a mapping"
	case n.Kind == yaml.SequenceNode:
		return "a list"
	case n.Tag == "!!null":
		return "empty"
	default:
		return "a value"
	}
}

// isNull reports an empty value (key with nothing after it, or null), which every section and
// list accepts as empty.
func isNull(n *yaml.Node) bool { return n.Kind == yaml.ScalarNode && n.Tag == "!!null" }

// mapping walks a mapping with known keys; keys missing from fields are refused, as are keys
// written twice, and every key in required must be present.
func (d doc) mapping(n *yaml.Node, at string, fields map[string]func(*yaml.Node, string) error, required ...string) error {
	if n.Kind != yaml.MappingNode {
		return d.fail(n, at, "must be a mapping, not "+kindName(n), "write key: value lines")
	}
	known := make([]string, 0, len(fields))
	for k := range fields {
		known = append(known, k)
	}
	slices.Sort(known)
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		key := k.Value
		sub := join(at, key)
		if k.Kind != yaml.ScalarNode {
			return d.fail(k, at, "a key must be a plain value", "known keys: "+strings.Join(known, ", "))
		}
		if seen[key] {
			return d.fail(k, sub, "is written twice", "keep one of them")
		}
		seen[key] = true
		f, ok := fields[key]
		if !ok {
			return d.fail(k, at, fmt.Sprintf("unknown key %q", untrusted.Text(key, untrusted.MaxName)), "known keys: "+strings.Join(known, ", "))
		}
		if err := f(v, sub); err != nil {
			return err
		}
	}
	for _, r := range required {
		if !seen[r] {
			return d.fail(n, at, "missing key "+r, "add "+r+": "+strconv.Itoa(Version))
		}
	}
	return nil
}

// version checks the version key.
func (d doc) version(n *yaml.Node, at string) error {
	if n.Kind != yaml.ScalarNode || n.Tag != "!!int" || n.Value != strconv.Itoa(Version) {
		if n.Kind == yaml.ScalarNode && n.Tag == "!!int" {
			return d.fail(n, at, "version "+untrusted.Text(n.Value, 20)+" is not supported", "this lola reads version "+strconv.Itoa(Version)+"; upgrade lola")
		}
		return d.fail(n, at, "must be the number "+strconv.Itoa(Version), "write version: "+strconv.Itoa(Version))
	}
	return nil
}

// scalar returns a non-empty string value; hint says what to write instead.
func (d doc) scalar(n *yaml.Node, at, hint string) (string, error) {
	if n.Kind != yaml.ScalarNode || n.Tag == "!!null" || n.Value == "" {
		return "", d.fail(n, at, "must be a non-empty value, not "+kindName(n), hint)
	}
	return n.Value, nil
}

// join extends a key path; keys that are not simple names are quoted.
func join(at, key string) string {
	k := untrusted.Text(key, untrusted.MaxName)
	if strings.ContainsAny(key, `/.[]" `) || key == "" {
		k = "[" + strconv.Quote(k) + "]"
		if at == "" {
			return k
		}
		return at + k
	}
	if at == "" {
		return k
	}
	return at + "." + k
}
