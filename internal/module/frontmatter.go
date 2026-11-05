package module

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/trevor-vaughan/lola-go/internal/errs"
)

// Doc is a markdown document with optional YAML frontmatter between --- fences.
// RawFrontmatter is the author's exact text (without fences) so rendering keeps comments,
// key order and formatting.
type Doc struct {
	Frontmatter    map[string]any
	RawFrontmatter string
	Body           string
}

// opensFrontmatter reports whether s begins with a line a frontmatter parser might take as an
// opener: after one byte-order mark, anything that starts with "---". Parsers differ on what may
// follow (trailing whitespace of any Unicode kind, or text), so lola reads only an exact "---"
// line and treats every other such line as ambiguous.
func opensFrontmatter(s string) bool {
	return strings.HasPrefix(strings.TrimPrefix(s, "\ufeff"), "---")
}

// ParseDoc splits src into frontmatter and body. A document that does not start with a
// "---" line has no frontmatter, which is valid. A first line that starts with "---" but is not
// exactly that line (more dashes, trailing text or whitespace, or no line break) is refused: an assistant's
// parser may read frontmatter there, hooks included, that lola would not see (fail closed).
func ParseDoc(src []byte) (Doc, error) {
	s := strings.TrimPrefix(string(src), "\ufeff")
	if !strings.HasPrefix(s, "---\n") && !strings.HasPrefix(s, "---\r\n") {
		if opensFrontmatter(s) {
			return Doc{}, errs.WithHints(errors.New(`the first line starts with "---" but is not exactly "---", so lola cannot read the frontmatter an assistant might`),
				"start frontmatter with a line of exactly ---; for a rule (horizontal line) at the top, write *** or ___ instead, or begin the file with other text")
		}
		return Doc{Body: s}, nil
	}
	rest := s[strings.IndexByte(s, '\n')+1:]
	pos := 0
	for {
		nl := strings.IndexByte(rest[pos:], '\n')
		line := rest[pos:]
		if nl >= 0 {
			line = rest[pos : pos+nl]
		}
		if strings.TrimRight(line, "\r") == "---" {
			raw := rest[:pos]
			body := ""
			if nl >= 0 {
				body = rest[pos+nl+1:]
			}
			fm, err := parseFrontmatter(raw)
			if err != nil {
				return Doc{}, err
			}
			return Doc{Frontmatter: fm, RawFrontmatter: raw, Body: body}, nil
		}
		if nl < 0 {
			return Doc{}, errors.New("frontmatter starts with --- but has no closing --- line")
		}
		pos += nl + 1
	}
}

// parseFrontmatter decodes raw as one YAML mapping. It refuses what another parser might read
// differently (fail closed): a second YAML document, which some parsers take as the frontmatter,
// and a merge key at any depth, which carries keys lola would not list or fields it would not
// check (a server's command, a hook group's matcher).
func parseFrontmatter(raw string) (map[string]any, error) {
	dec := yaml.NewDecoder(strings.NewReader(raw))
	var root yaml.Node
	if err := dec.Decode(&root); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, fmt.Errorf("frontmatter: %w", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errs.WithHints(errors.New("frontmatter holds more than one YAML document, so parsers may disagree on which one is the frontmatter"),
			"remove the extra document, or quote the line that looks like a YAML document separator")
	}
	if hasMergeKey(&root) {
		return nil, errs.WithHints(errors.New("frontmatter uses a merge key (<<), which hides keys from lola"),
			"write the merged keys out in the frontmatter")
	}
	var fm map[string]any
	if err := root.Decode(&fm); err != nil {
		return nil, fmt.Errorf("frontmatter: %w", err)
	}
	return fm, nil
}

// hasMergeKey reports whether a mapping under n has a merge key: a plain << or one tagged
// !!merge, which the parser resolves to the !!merge tag; a quoted '<<' is a literal key. An
// alias is not followed, since its anchored node is checked where it is defined.
func hasMergeKey(n *yaml.Node) bool {
	for i, c := range n.Content {
		if n.Kind == yaml.MappingNode && i%2 == 0 && c.Tag == "!!merge" {
			return true
		}
		if hasMergeKey(c) {
			return true
		}
	}
	return false
}

// Render rebuilds the document from the raw frontmatter and body.
func (d Doc) Render() string {
	if d.RawFrontmatter == "" && d.Frontmatter == nil {
		// A body whose first line a parser would take as a frontmatter opener (the test ParseDoc
		// refuses on) gets an empty block first, so it is never read as frontmatter.
		if opensFrontmatter(d.Body) {
			return "---\n---\n" + d.Body
		}
		return d.Body
	}
	return "---\n" + d.RawFrontmatter + "---\n" + d.Body
}

// Prepend returns a copy with "key: value" as the first frontmatter line. value must be a
// plain YAML scalar (lola only prepends validated names).
func (d Doc) Prepend(key, value string) Doc {
	fm := make(map[string]any, len(d.Frontmatter)+1)
	for k, v := range d.Frontmatter {
		fm[k] = v
	}
	fm[key] = value
	return Doc{Frontmatter: fm, RawFrontmatter: key + ": " + value + "\n" + d.RawFrontmatter, Body: d.Body}
}

// With returns a copy whose frontmatter sets each key of fm, replacing a value the document
// already has and adding the others after its own keys, in key order. Comments and the order of
// the document's own keys are kept.
func (d Doc) With(fm map[string]any) (Doc, error) {
	if len(fm) == 0 {
		return d, nil
	}
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(d.RawFrontmatter), &root); err != nil {
		return Doc{}, fmt.Errorf("frontmatter: %w", err)
	}
	if len(root.Content) == 0 {
		root = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	m := root.Content[0]
	if m.Kind != yaml.MappingNode {
		return Doc{}, errors.New("frontmatter is not a mapping")
	}
	for _, k := range slices.Sorted(maps.Keys(fm)) {
		var v yaml.Node
		if err := v.Encode(fm[k]); err != nil {
			return Doc{}, fmt.Errorf("frontmatter %s: %w", k, err)
		}
		i := 0
		for i < len(m.Content) && m.Content[i].Value != k {
			i += 2
		}
		if i < len(m.Content) {
			m.Content[i+1] = &v
			continue
		}
		m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k}, &v)
	}
	var b strings.Builder
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		return Doc{}, fmt.Errorf("frontmatter: %w", err)
	}
	if err := enc.Close(); err != nil {
		return Doc{}, fmt.Errorf("frontmatter: %w", err)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(b.String()), &parsed); err != nil {
		return Doc{}, fmt.Errorf("frontmatter: %w", err)
	}
	return Doc{Frontmatter: parsed, RawFrontmatter: b.String(), Body: d.Body}, nil
}

// Without returns a copy without the given top-level frontmatter keys. The remaining keys
// keep their order and comments; when none of keys is present the document is unchanged.
func (d Doc) Without(keys ...string) (Doc, error) {
	var drop []string
	for _, k := range keys {
		if _, ok := d.Frontmatter[k]; ok {
			drop = append(drop, k)
		}
	}
	if len(drop) == 0 {
		return d, nil
	}
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(d.RawFrontmatter), &root); err != nil {
		return Doc{}, fmt.Errorf("frontmatter: %w", err)
	}
	m := root.Content[0]
	var kept []*yaml.Node
	for i := 0; i+1 < len(m.Content); i += 2 {
		if !slices.Contains(drop, m.Content[i].Value) {
			kept = append(kept, m.Content[i], m.Content[i+1])
		}
	}
	if len(kept) == 0 {
		return Doc{Body: d.Body}, nil
	}
	m.Content = kept
	var b strings.Builder
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		return Doc{}, fmt.Errorf("frontmatter: %w", err)
	}
	if err := enc.Close(); err != nil {
		return Doc{}, fmt.Errorf("frontmatter: %w", err)
	}
	fm := maps.Clone(d.Frontmatter)
	for _, k := range drop {
		delete(fm, k)
	}
	return Doc{Frontmatter: fm, RawFrontmatter: b.String(), Body: d.Body}, nil
}

// ambiguousScalar matches plain YAML scalars that YAML 1.1 and 1.2 parsers read differently:
// 1.1 booleans other than true/false, and integers with a leading zero (octal in 1.1).
var ambiguousScalar = regexp.MustCompile(`(?i)^(y|n|yes|no|on|off|[-+]?0[0-9]+)$`)

// Ambiguous lists, in document order, the top-level frontmatter keys whose values contain an
// unquoted scalar that assistants' YAML parsers may read differently (§5.2 of the target
// framework design).
func (d Doc) Ambiguous() ([]string, error) {
	if d.RawFrontmatter == "" {
		return nil, nil
	}
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(d.RawFrontmatter), &root); err != nil {
		return nil, fmt.Errorf("frontmatter: %w", err)
	}
	if len(root.Content) == 0 {
		return nil, nil
	}
	var keys []string
	m := root.Content[0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		if hasAmbiguous(m.Content[i+1]) {
			keys = append(keys, m.Content[i].Value)
		}
	}
	return keys, nil
}

func hasAmbiguous(n *yaml.Node) bool {
	if n.Kind == yaml.ScalarNode {
		return n.Style == 0 && ambiguousScalar.MatchString(n.Value)
	}
	for _, c := range n.Content {
		if hasAmbiguous(c) {
			return true
		}
	}
	return false
}

// String returns the frontmatter value for key when it is a string, else "".
func (d Doc) String(key string) string {
	s, _ := d.Frontmatter[key].(string)
	return s
}
