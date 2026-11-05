package market

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
	"github.com/trevor-vaughan/lola-go/internal/yamldoc"
)

// RegistryVersion is the markets.yaml schema version this binary reads and writes.
const RegistryVersion = 1

// registryTemplate starts a markets.yaml lola creates (Plan 6c §5).
const registryTemplate = "# lola marketplaces. Safe to edit by hand.\nversion: 1\n"

// Registration is one marketplace of markets.yaml (Plan 6c §5): its name, the source you added
// it from, the ref, and Path, the catalog file lola found there at --add ("" for a source that
// is itself a catalog file). Line is its line in the file, for messages.
type Registration struct {
	Name   string `yaml:"name"`
	Source string `yaml:"source"`
	Ref    string `yaml:"ref,omitempty"`
	Path   string `yaml:"path,omitempty"`
	Line   int    `yaml:"-"`
}

// Registry is markets.yaml: your marketplaces, in the file's order.
type Registry struct {
	Markets []Registration
}

// Find is the registration of name, nil for none.
func (r Registry) Find(name string) *Registration {
	for i := range r.Markets {
		if r.Markets[i].Name == name {
			return &r.Markets[i]
		}
	}
	return nil
}

// ParseRegistry reads b, the markets.yaml at file (named in messages), which you may have
// edited by hand (Plan 6c §5): one YAML document with yamldoc.Decode's guards, version first, then
// known keys only; each marketplace's name follows ValidateName and is given once; its source,
// ref and path pass source.Recorded.Check; a folder is an absolute path. Every refusal names the
// file and line.
func ParseRegistry(file string, b []byte) (Registry, error) {
	reg := Registry{Markets: []Registration{}}
	d, err := yamldoc.Decode(b)
	if err != nil {
		return reg, fmt.Errorf("%s: %w", file, err)
	}
	root := yamldoc.Root(d)
	if root == nil {
		return reg, nil
	}
	if root.Kind != yaml.MappingNode {
		return reg, fmt.Errorf("%s:%d: is not a YAML mapping of version and markets", file, root.Line)
	}
	if v := yamldoc.Value(root, "version"); v != nil {
		var n int
		if err := v.Decode(&n); err != nil {
			return reg, fmt.Errorf("%s:%d: version is not a number", file, v.Line)
		}
		switch {
		case n > RegistryVersion:
			return reg, fmt.Errorf("%s was written by a newer lola (version %d); upgrade lola to read it", file, n)
		case n != RegistryVersion:
			return reg, fmt.Errorf("%s:%d: version %d is not %d", file, v.Line, n, RegistryVersion)
		}
	}
	var doc struct {
		Version int            `yaml:"version"`
		Markets []Registration `yaml:"markets"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return reg, fmt.Errorf("%s: %s", file, untrusted.Text(err.Error(), 2*untrusted.MaxName))
	}
	var lines []int
	if seq := yamldoc.Value(root, "markets"); seq != nil {
		for _, n := range seq.Content {
			lines = append(lines, n.Line)
		}
	}
	seen := map[string]bool{}
	for i, m := range doc.Markets {
		m.Line = lines[i]
		at := fmt.Sprintf("%s:%d", file, m.Line)
		if err := ValidateName(m.Name); err != nil {
			return reg, fmt.Errorf("%s: %w", at, err)
		}
		if seen[m.Name] {
			return reg, fmt.Errorf("%s: marketplace %s is listed twice; keep one entry per name", at, m.Name)
		}
		seen[m.Name] = true
		if err := (source.Recorded{Source: m.Source, Ref: m.Ref, Path: m.Path}).Check(); err != nil {
			return reg, fmt.Errorf("%s: marketplace %s: %w", at, m.Name, err)
		}
		if source.IsFolderForm(m.Source) && !filepath.IsAbs(m.Source) {
			return reg, fmt.Errorf("%s: marketplace %s: source %s is a relative path; record the folder's absolute path", at, m.Name,
				untrusted.Text(m.Source, untrusted.MaxName))
		}
		reg.Markets = append(reg.Markets, m)
	}
	return reg, nil
}

// AddRegistration is b, a markets.yaml ParseRegistry read (nil or empty for none), with r appended
// to its marketplaces, its comments and order kept.
func AddRegistration(b []byte, r Registration) ([]byte, error) {
	d, err := yamldoc.Decode(b)
	if err != nil {
		return nil, err
	}
	root := yamldoc.Root(d)
	switch {
	case d == nil:
		d = &yaml.Node{}
		if err := yaml.Unmarshal([]byte(registryTemplate), d); err != nil {
			return nil, err
		}
		root = d.Content[0]
	case root == nil:
		root = yamldoc.NullDocRoot(d, RegistryVersion)
	}
	seq := yamldoc.Value(root, "markets")
	switch {
	case seq == nil:
		seq = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		root.Content = append(root.Content, yamldoc.Str("markets"), seq)
	case seq.Kind == yaml.ScalarNode && seq.Tag == "!!null":
		*seq = yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", HeadComment: seq.HeadComment, LineComment: seq.LineComment}
	case seq.Kind != yaml.SequenceNode:
		return nil, fmt.Errorf("markets is not a list (line %d)", seq.Line)
	}
	m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, kv := range [][2]string{{"name", r.Name}, {"source", r.Source}, {"ref", r.Ref}, {"path", r.Path}} {
		if kv[1] != "" {
			m.Content = append(m.Content, yamldoc.Str(kv[0]), yamldoc.Str(kv[1]))
		}
	}
	seq.Content = append(seq.Content, m)
	return encode(d)
}

// RemoveRegistration is b, a markets.yaml ParseRegistry read, without the marketplace name, its
// comments and the order of the others kept; found says it was there.
func RemoveRegistration(b []byte, name string) (_ []byte, found bool, _ error) {
	d, err := yamldoc.Decode(b)
	if err != nil {
		return nil, false, err
	}
	root := yamldoc.Root(d)
	if root == nil {
		return b, false, nil
	}
	seq := yamldoc.Value(root, "markets")
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return b, false, nil
	}
	for i, m := range seq.Content {
		if n := yamldoc.Value(m, "name"); n != nil && n.Value == name {
			seq.Content = append(seq.Content[:i], seq.Content[i+1:]...)
			out, err := encode(d)
			return out, true, err
		}
	}
	return b, false, nil
}

// encode writes the document d with lola's two-space indent.
func encode(d *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(d); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// PinVersion is the source.json schema version this binary reads and writes.
const PinVersion = 1

// Pin is a cached catalog's source.json (Plan 6c §5): the source, ref and path it was fetched
// from (the registration's), File, the catalog file read, a slash path in the source (its URL's
// last element for a catalog file), the commit a git source resolved to, the digest of the
// cached catalog's bytes, when it was fetched, Absent, the directories the catalog's relative
// entries name that the fetched tree did not hold, which Bind skips, and Root, where the tree the
// catalog was read from is in the source (source.Fetched.Root: "" for its top, else a wrapping
// directory such as an archive's repo-<sha>/), which Bind joins to each relative entry's path. A
// pin written before Root was recorded reads as "", the top.
type Pin struct {
	Version   int       `json:"version"`
	Source    string    `json:"source"`
	Ref       string    `json:"ref"`
	Path      string    `json:"path"`
	File      string    `json:"file"`
	Commit    string    `json:"commit"`
	Digest    string    `json:"digest"`
	FetchedAt time.Time `json:"fetched_at"`
	Absent    []string  `json:"absent"`
	Root      string    `json:"root,omitempty"`
}

// maxPin bounds source.json.
const maxPin = 1 << 20

// ParsePin reads b, a source.json lola wrote: known keys only, at most 1 MiB, and fields lola
// would have written (Pin.Check).
func ParsePin(b []byte) (Pin, error) {
	if len(b) > maxPin {
		return Pin{}, fmt.Errorf("is larger than %d bytes", maxPin)
	}
	var p Pin
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return Pin{}, errors.New(untrusted.Text(err.Error(), 2*untrusted.MaxName))
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Pin{}, errors.New("holds data after its object")
	}
	if err := p.Check(); err != nil {
		return p, err
	}
	if p.Absent == nil {
		return Pin{}, errors.New("has no absent list")
	}
	return p, nil
}

// Check refuses a pin lola would not have written: another version, a field that fails the
// recorded-source rules, no digest, a git source without its commit, and a file or absent
// directory or root that is not a relative path inside the source.
func (p Pin) Check() error {
	switch {
	case p.Version > PinVersion:
		return fmt.Errorf("written by a newer lola (version %d); upgrade lola to read it", p.Version)
	case p.Version != PinVersion:
		return fmt.Errorf("version %d is not %d", p.Version, PinVersion)
	}
	if err := (source.Recorded{Source: p.Source, Ref: p.Ref, Path: p.Path, Commit: p.Commit, Digest: p.Digest}).Check(); err != nil {
		return err
	}
	switch {
	case p.Digest == "":
		return errors.New("digest is missing")
	case source.IsGitForm(p.Source) && !IsCatalogURL(p.Source) && p.Commit == "":
		return errors.New("commit is missing")
	case FormatOf(p.File) == "":
		return fmt.Errorf("file %s is not a .json, .yml or .yaml catalog", untrusted.Text(p.File, untrusted.MaxName))
	case p.Root == ".":
		return errors.New(`root is ".", which lola records as ""`)
	}
	paths := append([]string{p.File}, p.Absent...)
	if p.Root != "" {
		paths = append(paths, p.Root)
	}
	for _, f := range paths {
		if !untrusted.IsPlain(f) {
			return errors.New("a path holds control or invisible characters")
		}
		if clean, err := CheckRelative(f); err != nil || clean != f {
			return fmt.Errorf("%s is not a clean relative path", untrusted.Text(f, untrusted.MaxName))
		}
	}
	return nil
}
