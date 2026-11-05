// Package yamldoc reads the YAML files people edit by hand and commit (config.yaml,
// selections.yaml, project.yml, user.yml) as one untrusted document. It refuses what would make
// lola block, run out of memory, or read one entry as another, before any key is interpreted.
package yamldoc

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
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
// elsewhere (Plan 6b §19 S3): path itself must not be a symbolic link. It is opened with
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
	f, err := os.OpenFile(path, flags, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err == nil && noFollow {
		if checked, lerr := os.Lstat(path); lerr != nil || !os.SameFile(fi, checked) {
			err = errs.WithHints(errors.Join(fmt.Errorf("%s changed while lola was reading it", untrusted.Text(path, untrusted.MaxName)), lerr),
				"run the command again once nothing else is changing it")
		}
	}
	if err == nil && !fi.Mode().IsRegular() {
		err = errs.WithHints(fmt.Errorf("%s is not a regular file", path), "replace it with a YAML file, or remove it")
	}
	var b []byte
	if err == nil {
		b, err = io.ReadAll(io.LimitReader(f, MaxFile+1))
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return nil, nil, err
	}
	if len(b) > MaxFile {
		return nil, nil, errs.WithHints(fmt.Errorf("%s is larger than %d bytes", path, MaxFile), "shorten it; lola never writes one this large")
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
		return nil, nil, errs.WithHints(fmt.Errorf("%s: does not parse: %s", path, untrusted.Text(err.Error(), 2*untrusted.MaxName)),
			"fix it by hand, or move it aside (lola starts a new one) and rerun")
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, nil, errs.WithHints(fmt.Errorf("%s: holds more than one YAML document", path), "remove every --- line after the first document")
	}
	if n := special(&d); n != nil {
		return nil, nil, errs.WithHints(fmt.Errorf("%s:%d: anchors, aliases and merge keys (<<) are not supported", path, n.Line),
			"write each entry out in full")
	}
	return &d, b, nil
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
