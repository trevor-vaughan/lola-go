package migrate

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"go.yaml.in/yaml/v3"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// maxFile caps installed.yml and each source.yml (§3).
const maxFile = 1 << 20

// maxNames caps the names, keys and records one file stands for once aliases are followed: the
// Python lola writes aliases (PyYAML shares a list two records hold), so a small file could
// otherwise stand for a very large one.
const maxNames = 100000

// Scopes of a record.
const (
	ScopeProject = "project"
	ScopeUser    = "user"
)

// Record is one entry of the Python lola's installed.yml: a module installed for one assistant
// at one scope. Names are validated single path components; Problem says why a project-scope
// record's project_path cannot be used (§3), and is "" otherwise.
type Record struct {
	Module, Assistant, Scope, ProjectPath, Ref string
	Skills, Commands, Agents, MCPs             []string
	HasInstructions                            bool
	AppendContext                              []string
	Problem                                    string
}

// Source is a module's .lola/source.yml: where the Python lola fetched it from.
type Source struct {
	Location, Type, Ref, ContentDirname string
}

// State is what the Python lola recorded in its home. Sources holds the source.yml of every
// module a record names that has one.
type State struct {
	Home     string
	Exists   bool
	Records  []Record
	Sources  map[string]Source
	Warnings []string
}

// homeHint tells the user how lola finds the Python lola's home.
const homeHint = "lola reads the Python lola's state from $LOLA_HOME, else ~/.lola"

// Home is the Python lola's home: $LOLA_HOME when it is set, else ~/.lola. An empty
// $LOLA_HOME counts as unset; a relative one is refused, since lola cannot know which
// directory the Python lola resolved it against.
func Home(getenv func(string) string, userHome string) (string, error) {
	h := getenv("LOLA_HOME")
	switch {
	case h == "":
		return filepath.Join(userHome, ".lola"), nil
	case !filepath.IsAbs(h):
		return "", errs.WithHints(fmt.Errorf("LOLA_HOME must be an absolute path, not %q", untrusted.Text(h, untrusted.MaxName)),
			"set LOLA_HOME to the absolute path of the Python lola's home, or unset it to use ~/.lola")
	}
	return filepath.Clean(h), nil
}

// Load reads the Python lola's installed.yml and the source.yml of each module it names. Both
// are untrusted (§3): a file that does not parse, or names that are not single path
// components, stop the command; unknown keys are warnings. A missing home is an empty state.
func Load(home, userHome string) (*State, error) {
	st := &State{Home: home, Sources: map[string]Source{}, Warnings: []string{}}
	fi, err := os.Stat(home)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return st, nil
	case err != nil:
		return nil, errs.WithHints(fmt.Errorf("cannot read the Python lola's home %s: %w", home, untrusted.CleanErr(err)), homeHint)
	case !fi.IsDir():
		return nil, errs.WithHints(fmt.Errorf("the Python lola's home %s is not a directory", home), homeHint)
	}
	st.Exists = true
	r := &reader{path: filepath.Join(home, "installed.yml"), warnings: &st.Warnings, budget: maxNames}
	root, err := r.document()
	if err != nil || root == nil {
		return st, err
	}
	if st.Records, err = r.installed(root, userHome); err != nil {
		return nil, err
	}
	for _, rec := range st.Records {
		if _, done := st.Sources[rec.Module]; done {
			continue
		}
		sr := &reader{path: filepath.Join(home, "modules", rec.Module, ".lola", "source.yml"), warnings: &st.Warnings, budget: maxNames}
		n, err := sr.document()
		if err != nil {
			return nil, err
		}
		if n == nil {
			continue
		}
		src, err := sr.source(n)
		if err != nil {
			return nil, err
		}
		st.Sources[rec.Module] = src
	}
	return st, nil
}

// reader validates one YAML file. budget caps the names, keys and records it walks once aliases
// are followed.
type reader struct {
	path     string
	warnings *[]string
	budget   int
}

// fixHint is the advice for a file the Python lola wrote that lola cannot read.
const fixHint = "fix the file by hand (the Python lola wrote it; lola migrate never changes it), then rerun lola migrate"

// charge spends one unit of the budget, refusing the file once it is spent.
func (r *reader) charge() error {
	if r.budget--; r.budget < 0 {
		return errs.WithHints(fmt.Errorf("%s: more than %d names once aliases are followed", r.path, maxNames), fixHint)
	}
	return nil
}

func (r *reader) fail(n *yaml.Node, at, problem string) error {
	where := r.path + ":" + strconv.Itoa(n.Line)
	if at != "" {
		where += ": " + at
	}
	return errs.WithHints(fmt.Errorf("%s: %s", where, problem), fixHint)
}

// document reads the file as one YAML document and returns its root node: nil when the file
// does not exist or is empty. It refuses a file that is not regular (without blocking on a
// FIFO), one over maxFile and a second document.
func (r *reader) document() (*yaml.Node, error) {
	f, err := os.OpenFile(r.path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errs.WithHints(fmt.Errorf("cannot read %s: %w", r.path, untrusted.CleanErr(err)), homeHint)
	}
	fi, err := f.Stat()
	if err == nil && !fi.Mode().IsRegular() {
		err = errs.WithHints(fmt.Errorf("%s is not a regular file", r.path), fixHint)
	}
	var b []byte
	if err == nil {
		b, err = io.ReadAll(io.LimitReader(f, maxFile+1))
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return nil, err
	}
	if len(b) > maxFile {
		return nil, errs.WithHints(fmt.Errorf("%s is larger than %d bytes", r.path, maxFile), "the Python lola never writes one this large; "+fixHint)
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	var d yaml.Node
	if err := dec.Decode(&d); errors.Is(err, io.EOF) {
		return nil, nil
	} else if err != nil {
		return nil, errs.WithHints(fmt.Errorf("%s: does not parse: %s", r.path, untrusted.Text(err.Error(), 2*untrusted.MaxName)), fixHint)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errs.WithHints(fmt.Errorf("%s: holds more than one YAML document", r.path), fixHint)
	}
	if len(d.Content) == 0 || isNull(d.Content[0]) {
		return nil, nil
	}
	return d.Content[0], nil
}

func isNull(n *yaml.Node) bool { return n.Kind == yaml.ScalarNode && n.Tag == "!!null" }

// deref follows an alias to the node it names.
func deref(n *yaml.Node) *yaml.Node {
	if n.Kind == yaml.AliasNode && n.Alias != nil {
		return n.Alias
	}
	return n
}

func join(at, key string) string {
	if at == "" {
		return key
	}
	return at + "." + key
}

// fields walks mapping n: each known key goes to its handler, an unknown key is a warning, a
// key written twice is refused, and every key in required must be present.
func (r *reader) fields(n *yaml.Node, at string, known map[string]func(*yaml.Node, string) error, required ...string) error {
	n = deref(n)
	if n.Kind != yaml.MappingNode {
		return r.fail(n, at, "must be a mapping, not "+kindName(n))
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if err := r.charge(); err != nil {
			return err
		}
		if k.Kind != yaml.ScalarNode {
			return r.fail(k, at, "a key must be a plain value")
		}
		sub := join(at, untrusted.Text(k.Value, untrusted.MaxName))
		if seen[k.Value] {
			return r.fail(k, sub, "written twice")
		}
		seen[k.Value] = true
		h, ok := known[k.Value]
		if !ok {
			*r.warnings = append(*r.warnings, fmt.Sprintf("%s:%d: %s: unknown key, ignored", r.path, k.Line, sub))
			continue
		}
		if err := h(v, sub); err != nil {
			return err
		}
	}
	for _, k := range required {
		if !seen[k] {
			return r.fail(n, at, k+" is missing")
		}
	}
	return nil
}

func kindName(n *yaml.Node) string {
	switch {
	case n.Kind == yaml.MappingNode:
		return "a mapping"
	case n.Kind == yaml.SequenceNode:
		return "a list"
	case isNull(n):
		return "empty"
	default:
		return "a value"
	}
}

// text reads a string scalar, refusing control and format characters.
func (r *reader) text(n *yaml.Node, at string) (string, error) {
	n = deref(n)
	if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
		return "", r.fail(n, at, "must be a string, not "+kindName(n))
	}
	if !printable(n.Value) {
		return "", r.fail(n, at, "holds a control or format character")
	}
	return n.Value, nil
}

// optText reads a string scalar or an empty value ("").
func (r *reader) optText(n *yaml.Node, at string) (string, error) {
	if isNull(deref(n)) {
		return "", nil
	}
	return r.text(n, at)
}

// printable reports whether s prints as itself: no control, format or line-separator
// characters, and valid UTF-8.
func printable(s string) bool { return untrusted.Text(s, len(s)) == s }

// name reads a string that becomes a path component: one component, never "." or "..",
// no separator, NUL, control or format character.
func (r *reader) name(n *yaml.Node, at string) (string, error) {
	n = deref(n)
	if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
		return "", r.fail(n, at, "must be a string, not "+kindName(n))
	}
	if !validName(n.Value) {
		return "", r.fail(n, at, fmt.Sprintf("%q is not a single path component", untrusted.Text(n.Value, untrusted.MaxName)))
	}
	return n.Value, nil
}

func validName(s string) bool {
	return s != "" && s != "." && s != ".." && len(s) <= 255 && !strings.ContainsAny(s, `/\`) && printable(s)
}

// names reads a list of names; an empty value is an empty list.
func (r *reader) names(n *yaml.Node, at string) ([]string, error) {
	return r.list(n, at, r.name)
}

func (r *reader) list(n *yaml.Node, at string, item func(*yaml.Node, string) (string, error)) ([]string, error) {
	n = deref(n)
	out := []string{}
	if isNull(n) {
		return out, nil
	}
	if n.Kind != yaml.SequenceNode {
		return nil, r.fail(n, at, "must be a list, not "+kindName(n))
	}
	for i, c := range n.Content {
		if err := r.charge(); err != nil {
			return nil, err
		}
		s, err := item(c, at+"["+strconv.Itoa(i)+"]")
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// installed reads installed.yml's root: version "1.0" (or none) and the installations list.
func (r *reader) installed(root *yaml.Node, userHome string) ([]Record, error) {
	var recs []Record
	err := r.fields(root, "", map[string]func(*yaml.Node, string) error{
		"version": func(n *yaml.Node, at string) error {
			n = deref(n)
			if n.Kind != yaml.ScalarNode || n.Value != "1.0" {
				return errs.WithHints(fmt.Errorf("%s:%d: %s %q is not a version this lola reads", r.path, n.Line, at, untrusted.Text(n.Value, untrusted.MaxName)),
					"lola migrate reads installed.yml version 1.0, which the Python lola writes; check that LOLA_HOME names its home")
			}
			return nil
		},
		"installations": func(n *yaml.Node, at string) error {
			n = deref(n)
			if isNull(n) {
				return nil
			}
			if n.Kind != yaml.SequenceNode {
				return r.fail(n, at, "must be a list, not "+kindName(n))
			}
			for i, c := range n.Content {
				if err := r.charge(); err != nil {
					return err
				}
				rec, err := r.record(c, at+"["+strconv.Itoa(i)+"]", userHome)
				if err != nil {
					return err
				}
				recs = append(recs, rec)
			}
			return nil
		},
	})
	return recs, err
}

// record reads one installation the way the Python lola's Installation.from_dict does:
// scope defaults to project, lists to empty, and append_context may be one string.
func (r *reader) record(n *yaml.Node, at, userHome string) (Record, error) {
	rec := Record{Scope: ScopeProject, Skills: []string{}, Commands: []string{}, Agents: []string{}, MCPs: []string{}, AppendContext: []string{}}
	setList := func(dst *[]string) func(*yaml.Node, string) error {
		return func(n *yaml.Node, at string) (err error) {
			*dst, err = r.names(n, at)
			return err
		}
	}
	err := r.fields(n, at, map[string]func(*yaml.Node, string) error{
		"module":       into(&rec.Module, r.name),
		"assistant":    into(&rec.Assistant, r.name),
		"scope":        into(&rec.Scope, r.text),
		"project_path": into(&rec.ProjectPath, r.optText),
		"ref":          into(&rec.Ref, r.optText),
		"version": func(n *yaml.Node, at string) error {
			if n = deref(n); n.Kind != yaml.ScalarNode {
				return r.fail(n, at, "must be a value, not "+kindName(n))
			}
			return nil
		},
		"skills":   setList(&rec.Skills),
		"commands": setList(&rec.Commands),
		"agents":   setList(&rec.Agents),
		"mcps":     setList(&rec.MCPs),
		"has_instructions": func(n *yaml.Node, at string) error {
			n = deref(n)
			switch {
			case isNull(n):
			case n.Kind == yaml.ScalarNode && n.Tag == "!!bool":
				rec.HasInstructions = n.Value == "true" || n.Value == "True" || n.Value == "TRUE"
			default:
				return r.fail(n, at, "must be true or false")
			}
			return nil
		},
		"append_context": func(n *yaml.Node, at string) error {
			if d := deref(n); d.Kind == yaml.ScalarNode && !isNull(d) {
				s, err := r.text(d, at)
				rec.AppendContext = []string{s}
				return err
			}
			var err error
			rec.AppendContext, err = r.list(n, at, r.text)
			return err
		},
	}, "module", "assistant")
	if err != nil {
		return Record{}, err
	}
	switch rec.Scope {
	case ScopeUser:
	case ScopeProject:
		rec.Problem = projectProblem(rec.ProjectPath, userHome)
	default:
		rec.Problem = fmt.Sprintf("unknown scope %q", untrusted.Text(rec.Scope, untrusted.MaxName))
	}
	return rec, nil
}

// projectProblem says why p cannot be a destination (§3), or "" when it can: it must be an
// existing directory, absolute and equal to its clean and symlink-free forms, and neither
// the home directory nor one of its parents.
func projectProblem(p, userHome string) string {
	if p == "" {
		return "project scope without a project_path"
	}
	shown := "project_path " + untrusted.Text(p, untrusted.MaxName)
	switch {
	case !printable(p):
		return shown + " holds a control or format character"
	case !filepath.IsAbs(p):
		return shown + " is not an absolute path"
	case filepath.Clean(p) != p:
		return shown + " is not in its clean form"
	}
	homes := []string{userHome}
	if real, err := filepath.EvalSymlinks(userHome); err == nil {
		homes = append(homes, real)
	}
	for _, h := range homes {
		if within(p, h) {
			return shown + " is your home directory or one of its parents"
		}
	}
	fi, err := os.Stat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return shown + " no longer exists"
	case err != nil:
		return shown + " cannot be read: " + untrusted.CleanErr(err).Error()
	case !fi.IsDir():
		return shown + " is not a directory"
	}
	if real, err := filepath.EvalSymlinks(p); err != nil || real != p {
		return shown + " is a symbolic link or has one on its way"
	}
	return ""
}

// within reports whether child is parent or below it, lexically.
func within(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && !filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// into is a field handler that stores what read returns in dst.
func into(dst *string, read func(*yaml.Node, string) (string, error)) func(*yaml.Node, string) error {
	return func(n *yaml.Node, at string) (err error) {
		*dst, err = read(n, at)
		return err
	}
}

// source reads a source.yml: source and type, and optionally ref and content_dirname.
func (r *reader) source(n *yaml.Node) (Source, error) {
	var s Source
	err := r.fields(n, "", map[string]func(*yaml.Node, string) error{
		"source":          into(&s.Location, r.text),
		"type":            into(&s.Type, r.text),
		"ref":             into(&s.Ref, r.optText),
		"content_dirname": into(&s.ContentDirname, r.optText),
	}, "source", "type")
	return s, err
}
