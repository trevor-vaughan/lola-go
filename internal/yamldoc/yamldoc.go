// Package yamldoc reads the YAML files people edit by hand and commit (config.yaml,
// selections.yaml, project.yml, profiles) as one untrusted document. It refuses what would make
// lola block, run out of memory, or read one entry as another, before any key is interpreted.
package yamldoc

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"go.yaml.in/yaml/v3"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// MaxFile caps a file Read accepts.
const MaxFile = 1 << 20

// Read reads path as one YAML document node and returns it with the bytes read: nil for a
// missing or empty file. A file that holds only comments reads as `version: <version>` under
// those comments, so a rewrite keeps them. It refuses a file that is not regular, one over
// MaxFile, a second document, and anchors, aliases and merge keys, which would make one entry
// stand for another and which lola could not keep when it rewrites the file.
func Read(path string, version int) (*yaml.Node, []byte, error) { return read(path, version, false) }

// ReadNoFollow is Read for a file someone else may have committed, which must not lead lola
// elsewhere: path itself must not be a symbolic link. It is opened with
// O_NOFOLLOW where the system has it, and on every system the file opened must be the one
// os.Lstat finds at path, so a link, or one swapped in while it reads, is refused. The caller
// coaches a link it finds first.
func ReadNoFollow(path string, version int) (*yaml.Node, []byte, error) {
	return read(path, version, true)
}

func read(path string, version int, noFollow bool) (*yaml.Node, []byte, error) {
	// Opened without blocking, then checked: opening a FIFO for reading would otherwise wait for
	// a writer, and checking the open file leaves no gap for a swap. Regular files ignore the flag.
	flags := os.O_RDONLY | syscall.O_NONBLOCK
	if noFollow {
		flags |= oNoFollow
	}
	// The path may name a directory someone else chose (a cloned project), so every error names
	// it as shown, and an I/O error keeps only its cause.
	shown := untrusted.Text(path, untrusted.MaxName)
	cannotRead := func(err error) error { return fmt.Errorf("cannot read %s: %w", shown, untrusted.CleanErr(err)) }
	f, err := os.OpenFile(path, flags, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, cannotRead(err)
	}
	fi, err := f.Stat()
	if err != nil {
		err = cannotRead(err)
	} else if noFollow {
		if checked, lerr := os.Lstat(path); lerr != nil || !os.SameFile(fi, checked) {
			err = errs.WithHints(errors.Join(fmt.Errorf("%s changed while lola was reading it", shown), untrusted.CleanErr(lerr)),
				"run the command again once nothing else is changing it")
		}
	}
	if err == nil && !fi.Mode().IsRegular() {
		err = errs.WithHints(fmt.Errorf("%s is not a regular file", shown), "replace it with a YAML file, or remove it")
	}
	var b []byte
	if err == nil {
		if b, err = io.ReadAll(io.LimitReader(f, MaxFile+1)); err != nil {
			err = cannotRead(err)
		}
	}
	if cerr := f.Close(); cerr != nil {
		err = errors.Join(err, cannotRead(cerr))
	}
	if err != nil {
		return nil, nil, err
	}
	if len(b) > MaxFile {
		return nil, nil, errs.WithHints(fmt.Errorf("%s is larger than %d bytes", shown, MaxFile), "shorten it; lola never writes one this large")
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	var d yaml.Node
	if err := dec.Decode(&d); errors.Is(err, io.EOF) {
		if len(bytes.TrimSpace(b)) == 0 {
			return nil, b, nil
		}
		// Only comments: yaml.v3 drops them with an empty document, so give them one.
		text := string(b)
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		if err := yaml.Unmarshal([]byte(text+"version: "+strconv.Itoa(version)+"\n"), &d); err != nil {
			return nil, nil, err
		}
		return &d, b, nil
	} else if err != nil {
		return nil, nil, errs.WithHints(fmt.Errorf("%s: does not parse: %s", shown, untrusted.Text(err.Error(), 2*untrusted.MaxName)),
			"fix it by hand, or move it aside (lola starts a new one) and rerun")
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, nil, errs.WithHints(fmt.Errorf("%s: holds more than one YAML document", shown), "remove every --- line after the first document")
	}
	if n := special(&d); n != nil {
		return nil, nil, errs.WithHints(fmt.Errorf("%s:%d: anchors, aliases and merge keys (<<) are not supported", shown, n.Line),
			"write each entry out in full")
	}
	return &d, b, nil
}

// Decode reads b, bytes lola did not write and the caller has already capped in size, as one
// YAML document node, nil for none. It refuses a second document,
// anchors, aliases and merge keys, which would make one entry stand for another, and a mapping
// that holds a key twice, which yaml.v3 refuses only when it decodes into a Go value. It applies
// no version rule. Errors name the line, never a file: the caller says which bytes these are.
func Decode(b []byte) (*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	var d yaml.Node
	if err := dec.Decode(&d); errors.Is(err, io.EOF) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("does not parse: %s", untrusted.Text(err.Error(), 2*untrusted.MaxName))
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("holds more than one YAML document")
	}
	if n := special(&d); n != nil {
		return nil, fmt.Errorf("line %d: anchors, aliases and merge keys (<<) are not supported", n.Line)
	}
	if n := duplicate(&d); n != nil {
		return nil, fmt.Errorf("line %d: the key %s is given twice", n.Line, untrusted.Text(n.Value, untrusted.MaxName))
	}
	return &d, nil
}

// duplicate returns the second key node of the first mapping under n that holds a key twice.
func duplicate(n *yaml.Node) *yaml.Node {
	if n.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i].Value
			if seen[k] {
				return n.Content[i]
			}
			seen[k] = true
		}
	}
	for _, c := range n.Content {
		if d := duplicate(c); d != nil {
			return d
		}
	}
	return nil
}

// Root returns a document's root node, or nil for no document or an empty one.
func Root(d *yaml.Node) *yaml.Node {
	if d == nil || len(d.Content) == 0 || d.Content[0].Kind == yaml.ScalarNode && d.Content[0].Tag == "!!null" {
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

// NullDocRoot gives a document that holds only null (such as `# notes` followed by `---`, `~`
// or `null`) a root mapping holding `version: <version>`, moving the comments onto the mapping
// so a write keeps them.
func NullDocRoot(d *yaml.Node, version int) *yaml.Node {
	root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
		Str("version"), {Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(version)},
	}}
	if len(d.Content) > 0 {
		old := d.Content[0]
		root.HeadComment, root.FootComment = old.HeadComment, old.FootComment
		root.Content[1].LineComment = old.LineComment
	}
	root.HeadComment = d.HeadComment + root.HeadComment
	d.HeadComment = ""
	d.Content = []*yaml.Node{root}
	return root
}

// Value returns the value under key in the mapping m, or nil.
func Value(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// RemoveKey deletes key and its value from the mapping m, reporting whether it was there.
func RemoveKey(m *yaml.Node, key string) bool {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return true
		}
	}
	return false
}

// Str is a string scalar node.
func Str(s string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s} }

// SetQuoted sets each key of set (in sorted order) in the mapping m to its value as a
// double-quoted string, so a version such as 2.10 is never read as a number. A key m already
// holds with that same string keeps its node; another value replaces the node's, keeping its
// comments; a new key goes last.
func SetQuoted(m *yaml.Node, set map[string]string) {
	for _, k := range slices.Sorted(maps.Keys(set)) {
		cur := Value(m, k)
		switch {
		case cur == nil:
			m.Content = append(m.Content, Str(k), &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: set[k], Style: yaml.DoubleQuotedStyle})
		case cur.Kind != yaml.ScalarNode || cur.ShortTag() != "!!str" || cur.Value != set[k]:
			cur.Kind, cur.Tag, cur.Value, cur.Content, cur.Style = yaml.ScalarNode, "!!str", set[k], nil, yaml.DoubleQuotedStyle
		}
	}
}

// MaxFlowItems is the longest list StrList writes on one line.
const MaxFlowItems = 8

// StrList is a list of strings as lola writes one it is the author of: a flow list up to
// MaxFlowItems items, a block list (one item a line) beyond, which a long complement of picks
// needs to stay readable.
func StrList(ss []string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	if len(ss) <= MaxFlowItems {
		n.Style = yaml.FlowStyle
	}
	for _, s := range ss {
		n.Content = append(n.Content, Str(s))
	}
	return n
}

// Shape names a struct a file decodes into and what to call it in a refusal, which lists the
// keys the struct's yaml tags give it ("a profile has: version, description, …").
type Shape struct {
	Type reflect.Type
	Noun string
}

var (
	unknownKeyRE = regexp.MustCompile(`^line (\d+): field (\S+) not found in type (\S+)$`)
	wrongKindRE  = regexp.MustCompile("^line (\\d+): cannot unmarshal (!!\\w+)(?: `[^`]*`)? into (.+)$")
	lineRE       = regexp.MustCompile(`^line (\d+): (.*)$`)
)

// Explain turns the error of a strict decode of the file shown into refusals that start with the
// file and the line, as the rest of lola's file refusals do, and never name a Go type: an unknown
// key lists the keys of its shape (shapes, matched by the type yaml names), and a value of the
// wrong kind says what was expected, and names its key when root, the file's parsed document (nil
// for none), has exactly one mapping value on that line. The decoder's own words are kept for any
// other error. The result still holds what the file says, so the caller shows it through untrusted.Text.
func Explain(shown string, err error, root *yaml.Node, shapes ...Shape) error {
	var te *yaml.TypeError
	if !errors.As(err, &te) {
		return fmt.Errorf("%s: %w", shown, err)
	}
	lines := make([]string, 0, len(te.Errors))
	for _, msg := range te.Errors {
		lines = append(lines, explainOne(shown, msg, root, shapes))
	}
	return errors.New(strings.Join(lines, "\n"))
}

func explainOne(shown, msg string, root *yaml.Node, shapes []Shape) string {
	if m := unknownKeyRE.FindStringSubmatch(msg); m != nil {
		line := fmt.Sprintf("%s:%s: unknown key %q", shown, m[1], untrusted.Text(m[2], untrusted.MaxName))
		for _, s := range shapes {
			if s.Type.String() == m[3] {
				return line + " (" + s.Noun + " has: " + strings.Join(yamlKeys(s.Type), ", ") + ")"
			}
		}
		return line
	}
	if m := wrongKindRE.FindStringSubmatch(msg); m != nil {
		at := m[1]
		if line, err := strconv.Atoi(m[1]); err == nil {
			if keys := keysAt(root, line); len(keys) == 1 {
				at += ": " + untrusted.Text(keys[0], untrusted.MaxName)
			}
		}
		return fmt.Sprintf("%s:%s: expected %s, found %s", shown, at, goKind(m[3]), yamlKind(m[2]))
	}
	if m := lineRE.FindStringSubmatch(msg); m != nil {
		return fmt.Sprintf("%s:%s: %s", shown, m[1], m[2])
	}
	return shown + ": " + msg
}

// yamlKeys lists the keys the yaml tags of struct type t give it, in field order, as the decoder
// reads them: the tag's name, else the lower-cased field name; "-" and unexported fields have none.
func yamlKeys(t reflect.Type) []string {
	var keys []string
	for f := range t.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		switch {
		case !f.IsExported() || name == "-":
		case name == "":
			keys = append(keys, strings.ToLower(f.Name))
		default:
			keys = append(keys, name)
		}
	}
	return keys
}

// goKind says what the decoder wanted, from the Go type it names, in the file's terms.
func goKind(goType string) string {
	switch {
	case strings.HasPrefix(goType, "[]"):
		return "a list"
	case goType == "string":
		return "text"
	case goType == "bool":
		return "true or false"
	case strings.HasPrefix(goType, "int"), strings.HasPrefix(goType, "uint"), strings.HasPrefix(goType, "float"):
		return "a number"
	}
	return "a mapping"
}

// yamlKind says what the file held, from the tag the decoder found.
func yamlKind(tag string) string {
	switch tag {
	case "!!str":
		return "text"
	case "!!seq":
		return "a list"
	case "!!map":
		return "a mapping"
	case "!!bool":
		return "true or false"
	case "!!int", "!!float":
		return "a number"
	}
	return "something else"
}

// keysAt lists the keys, in the mapping nodes under n, whose value starts on line.
func keysAt(n *yaml.Node, line int) []string {
	if n == nil {
		return nil
	}
	var keys []string
	for i, c := range n.Content {
		if n.Kind == yaml.MappingNode && i%2 == 1 && c.Line == line {
			keys = append(keys, n.Content[i-1].Value)
		}
		keys = append(keys, keysAt(c, line)...)
	}
	return keys
}
