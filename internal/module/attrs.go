package module

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"

	"github.com/trevor-vaughan/lola-go/internal/fsx"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
	"github.com/trevor-vaughan/lola-go/internal/yamldoc"
)

// Attribute files (Plan 7 §3.4): beside a skill's SKILL.md, a command's or an agent's file,
// <base>.targets.yaml or <base>.targets.jsonc maps target ids to frontmatter keys the engine
// merges into the component for that target. The file is untrusted data: it is capped, decoded
// by kind, never into a Go value of the decoder's choosing, and checked by the same rules in
// both syntaxes.
const (
	attrsYAML     = ".targets.yaml"
	attrsJSONC    = ".targets.jsonc"
	maxAttrsBytes = 64 << 10 // bytes of one attribute file
	maxAttrKeys   = 64       // frontmatter keys one target sets
	maxAttrKey    = 256      // bytes of a key
	maxAttrString = 16 << 10 // bytes of a string
	maxAttrItems  = 4096     // items of a list or keys of a map, at every depth
	maxAttrDepth  = 8        // maps and lists nested, the file's own map counting
)

// attrNames lists the file names, beside a component file of base name base, that hold its
// attributes, and the ones lola does not read.
func attrNames(base string) (read, unread []string) {
	return []string{base + attrsYAML, base + attrsJSONC}, []string{base + ".targets.yml", base + ".targets.json"}
}

// loadAttributes reads the attribute file of each skill, command and agent of m that has one,
// warning about the names lola does not read; a skill's copy leaves all of them out. known lists
// the target ids a file may name.
func loadAttributes(dir string, m *Module, known []string) error {
	listings := map[string][]string{} // content-relative directory → entry names
	attrs := func(file string) (map[string]map[string]any, string, []string, error) {
		parent, base := path.Dir(file), ComponentName(file)
		if path.Base(file) == "SKILL.md" || path.Base(file) == skillTemplate {
			base = "SKILL"
		}
		names, ok := listings[parent]
		if !ok {
			es, err := os.ReadDir(filepath.Join(dir, filepath.FromSlash(parent)))
			if err != nil {
				return nil, "", nil, ioFail(parent, err)
			}
			for _, e := range es {
				names = append(names, e.Name())
			}
			listings[parent] = names
		}
		return readAttributes(dir, parent, base, names, known, m)
	}
	for i := range m.Skills {
		s := &m.Skills[i]
		file := path.Join(s.Dir, "SKILL.md")
		if s.Doc.Template != nil {
			file = s.Doc.Template.File
		}
		a, at, found, err := attrs(file)
		if err != nil {
			return err
		}
		s.Attrs, s.AttrsFile = a, at
		for _, n := range found {
			if !slices.Contains(s.Exclude, n) {
				s.Exclude = append(s.Exclude, n)
			}
		}
	}
	for i := range m.Commands {
		c := &m.Commands[i]
		if c.File == "" { // an inline command has no file beside which to look
			continue
		}
		a, at, _, err := attrs(c.File)
		if err != nil {
			return err
		}
		c.Attrs, c.AttrsFile = a, at
	}
	for i := range m.Agents {
		a, at, _, err := attrs(m.Agents[i].File)
		if err != nil {
			return err
		}
		m.Agents[i].Attrs, m.Agents[i].AttrsFile = a, at
	}
	return nil
}

// readAttributes reads the attribute file of base in the content-relative directory parent,
// whose entries are names, and returns its content-relative path: nil and "" when it has none.
// found lists every entry named as an attribute
// file of base, read or not, under case folding, as spelled there; each one lola does not read
// warns.
func readAttributes(dir, parent, base string, names, known []string, m *Module) (attrs map[string]map[string]any, file string, found []string, err error) {
	read, unread := attrNames(base)
	rel := func(n string) string { return path.Join(parent, n) }
	for _, n := range names {
		switch {
		case slices.Contains(read, n):
			if file != "" {
				return nil, "", nil, fmt.Errorf("%s: both %s and %s exist; keep one", safeName(rel(base)), safeName(file), safeName(n))
			}
			file = n
		case slices.ContainsFunc(unread, func(u string) bool { return strings.EqualFold(n, u) }):
			m.Warnings = append(m.Warnings, safeName(rel(n))+" is not read; name it "+safeName(rel(read[0]))+" or "+safeName(rel(read[1])))
		case slices.ContainsFunc(read, func(r string) bool { return strings.EqualFold(n, r) }):
			want := read[0]
			if strings.HasSuffix(strings.ToLower(n), attrsJSONC) {
				want = read[1]
			}
			m.Warnings = append(m.Warnings, safeName(rel(n))+" is not read; name it "+safeName(rel(want)))
		default:
			continue
		}
		found = append(found, n)
	}
	if file == "" {
		return nil, "", found, nil
	}
	attrs, err = decodeAttributes(dir, rel(file), known)
	return attrs, rel(file), found, err
}

// decodeAttributes reads and decodes the attribute file at the content-relative rel.
func decodeAttributes(dir, rel string, known []string) (map[string]map[string]any, error) {
	b, err := readRegular(dir, rel, maxAttrsBytes, fmt.Errorf("%s: an attribute file must be a regular file", safeName(rel)),
		func(size string) error {
			return fmt.Errorf("%s: is %s; an attribute file is at most %d KiB", safeName(rel), size, maxAttrsBytes>>10)
		})
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(b) {
		return nil, fmt.Errorf("%s:%d: is not UTF-8; an attribute file must be UTF-8 text", safeName(rel), invalidLine(b))
	}
	var root any
	rule := attrRule{known: known}
	if strings.HasSuffix(rel, attrsJSONC) {
		root, err = jsoncAttributes(rel, b, rule)
	} else {
		root, err = yamlAttributes(rel, b, rule)
	}
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]any{}
	for id, v := range root.(map[string]any) {
		out[id] = v.(map[string]any)
	}
	return out, nil
}

// attrRule is the one rule both syntaxes apply to a decoded attribute file: at is where a value
// sits, as a list of keys and indexes from the file's root. It returns the reason a value
// breaks it, or "". known lists the target ids the file's root may name.
type attrRule struct{ known []string }

// key checks a key of a map at depth (the file's root map being 1): a key of the root names a
// known target, and a target's frontmatter key, in a map at depth 2, may not be name.
func (r attrRule) key(k string, depth int) string {
	switch {
	case k == "":
		return "a key cannot be empty"
	case len(k) > maxAttrKey:
		return fmt.Sprintf("a key is at most %d bytes", maxAttrKey)
	case strings.ContainsFunc(k, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }):
		return "a key holds a control or invisible character"
	case depth == 1 && !slices.Contains(r.known, k):
		return "unknown target; the targets are " + strings.Join(r.known, ", ")
	case depth == 2 && k == "name":
		return "the name comes from the path"
	}
	return ""
}

// str checks a string value.
func (attrRule) str(s string) string {
	switch {
	case len(s) > maxAttrString:
		return fmt.Sprintf("longer than %d KiB", maxAttrString>>10)
	case strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) && r != '\t' && r != '\n' }):
		return "holds a control character"
	}
	return ""
}

// shape checks that a map or list (isMap) may stand at depth: the root's value and each
// target's are maps, of target ids and of frontmatter keys, and nothing nests deeper than
// maxAttrDepth. A scalar is neither.
func (attrRule) shape(isMap, scalar bool, depth int) string {
	switch {
	case depth == 1 && (scalar || !isMap):
		return "an attribute file maps target ids to frontmatter keys"
	case depth == 2 && (scalar || !isMap):
		return "a target's attributes are a map of frontmatter keys"
	case !scalar && depth > maxAttrDepth:
		return fmt.Sprintf("maps and lists nest at most %d deep", maxAttrDepth)
	}
	return ""
}

// count checks the n items of a map or list (isMap) at depth.
func (attrRule) count(isMap bool, n, depth int) string {
	switch {
	case depth == 2 && n > maxAttrKeys:
		return fmt.Sprintf("%d keys; a target sets at most %d", n, maxAttrKeys)
	case isMap && n > maxAttrItems:
		return fmt.Sprintf("%d keys; a map holds at most %d", n, maxAttrItems)
	case !isMap && n > maxAttrItems:
		return fmt.Sprintf("%d items; a list holds at most %d", n, maxAttrItems)
	}
	return ""
}

var yamldocLine = regexp.MustCompile(`^(does not parse: )?(?:yaml: )?line (\d+): `)

// yamlAttributes decodes an attribute file in YAML from its node tree, by tag: a refusal names
// the file, the line and the dotted key path.
func yamlAttributes(rel string, b []byte, rule attrRule) (any, error) {
	d, err := yamldoc.Decode(b)
	if err != nil {
		// yamldoc names a line as "line N: " (after "does not parse: yaml: " for a parse error):
		// written file:N: as every other refusal here.
		msg := err.Error()
		if m := yamldocLine.FindStringSubmatch(msg); m != nil {
			return nil, fmt.Errorf("%s:%s: %s%s", safeName(rel), m[2], m[1], msg[len(m[0]):])
		}
		return nil, fmt.Errorf("%s: %s", safeName(rel), msg)
	}
	root := yamldoc.Root(d)
	if root == nil {
		return map[string]any{}, nil
	}
	return yamlValue(rel, rule, root, "", 1)
}

func yamlValue(rel string, rule attrRule, n *yaml.Node, at string, depth int) (any, error) {
	refuse := func(n *yaml.Node, at, reason string) error {
		if at == "" {
			return fmt.Errorf("%s:%d: %s", safeName(rel), n.Line, reason)
		}
		return fmt.Errorf("%s:%d: %s: %s", safeName(rel), n.Line, untrusted.Text(at, untrusted.MaxName), reason)
	}
	switch n.Kind {
	case yaml.MappingNode:
		if r := rule.shape(true, false, depth) + rule.count(true, len(n.Content)/2, depth) + collectionTag(n, "!!map"); r != "" {
			return nil, refuse(n, at, r)
		}
		out := make(map[string]any, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind != yaml.ScalarNode || k.Tag != "!!str" {
				return nil, refuse(k, at, "a key must be text; quote it")
			}
			sub := k.Value
			if at != "" {
				sub = at + "." + k.Value
			}
			if r := rule.key(k.Value, depth); r != "" {
				return nil, refuse(k, sub, r)
			}
			v, err := yamlValue(rel, rule, n.Content[i+1], sub, depth+1)
			if err != nil {
				return nil, err
			}
			out[k.Value] = v
		}
		return out, nil
	case yaml.SequenceNode:
		if r := rule.shape(false, false, depth) + rule.count(false, len(n.Content), depth) + collectionTag(n, "!!seq"); r != "" {
			return nil, refuse(n, at, r)
		}
		out := make([]any, 0, len(n.Content))
		for i, c := range n.Content {
			v, err := yamlValue(rel, rule, c, at+"["+strconv.Itoa(i)+"]", depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case yaml.ScalarNode:
	default: // an alias, which yamldoc.Decode already refuses
		return nil, refuse(n, at, "anchors, aliases and merge keys (<<) are not supported")
	}
	if r := rule.shape(false, true, depth); r != "" {
		return nil, refuse(n, at, r)
	}
	switch n.Tag {
	case "!!str":
		if outOfRange(n) {
			return nil, refuse(n, at, nonFiniteReason)
		}
		if r := rule.str(n.Value); r != "" {
			return nil, refuse(n, at, r)
		}
		return n.Value, nil
	case "!!bool":
		var v bool
		if n.Decode(&v) != nil { // yaml.v3's message quotes the raw value
			return nil, refuse(n, at, "a boolean is true or false")
		}
		return v, nil
	case "!!int":
		v, r := yamlInt(n)
		if r != "" {
			return nil, refuse(n, at, r)
		}
		return v, nil
	case "!!null":
		return nil, nil
	case "!!float":
		v, r := yamlFloat(n)
		if r != "" {
			return nil, refuse(n, at, r)
		}
		return v, nil
	case "!!timestamp":
		return nil, refuse(n, at, "a date is not supported; quote it to make it a string")
	case "!!binary":
		return nil, refuse(n, at, "binary data is not supported; write it as a string")
	}
	return nil, refuse(n, at, unsupportedTag(n.Tag))
}

// collectionTag returns the reason a map or list n refuses a tag other than its default, def,
// as a scalar refuses an unknown one: "" when it has none.
func collectionTag(n *yaml.Node, def string) string {
	if n.Tag == def {
		return ""
	}
	return unsupportedTag(n.Tag)
}

func unsupportedTag(tag string) string {
	return "the tag " + untrusted.Text(tag, untrusted.MaxName) + " is not supported; remove it"
}

// jsoncAttributes decodes an attribute file in JSONC token by token: a refusal names the file,
// the line and the JSON pointer of the value.
func jsoncAttributes(rel string, b []byte, rule attrRule) (any, error) {
	std, err := fsx.StandardJSON(b)
	if err != nil {
		return nil, fmt.Errorf("%s: does not parse: %s", safeName(rel), untrusted.Text(err.Error(), 2*untrusted.MaxName))
	}
	if len(bytes.TrimSpace(std)) == 0 { // only whitespace and comments, as an empty YAML file
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(std))
	dec.UseNumber()
	return jsonAttrs{rel: rel, std: std, dec: dec, rule: rule}.value("", 1) // hujson refused anything after the one value
}

// jsonAttrs is the token decoder of one JSONC attribute file, std as fsx.StandardJSON made it:
// its lines are the file's.
type jsonAttrs struct {
	rel  string
	std  []byte
	dec  *json.Decoder
	rule attrRule
}

// line is the line of the token the decoder read last: JSON tokens never span lines.
func (j jsonAttrs) line() int { return 1 + bytes.Count(j.std[:j.dec.InputOffset()], []byte("\n")) }

func (j jsonAttrs) refuse(at string, line int, reason string) error {
	if at == "" {
		return fmt.Errorf("%s:%d: %s", safeName(j.rel), line, reason)
	}
	return fmt.Errorf("%s:%d: %s: %s", safeName(j.rel), line, untrusted.Text(at, untrusted.MaxName), reason)
}

// items counts the items of the map or list whose opening delimiter the decoder read last,
// reading it again from there with a decoder of its own, so the count rule sees the true count
// before any item is checked, as in YAML. std is standard JSON that hujson parsed.
func (j jsonAttrs) items(isMap bool) int {
	dec := json.NewDecoder(bytes.NewReader(j.std[j.dec.InputOffset()-1:]))
	n := 0
	if _, err := dec.Token(); err != nil {
		return 0
	}
	for ; dec.More(); n++ {
		if isMap {
			if _, err := dec.Token(); err != nil {
				return n
			}
		}
		var skip json.RawMessage
		if dec.Decode(&skip) != nil {
			return n
		}
	}
	return n
}

func (j jsonAttrs) value(at string, depth int) (any, error) {
	rule := j.rule
	tok, err := j.dec.Token()
	if err != nil {
		return nil, fmt.Errorf("%s: does not parse: %s", safeName(j.rel), untrusted.Text(err.Error(), 2*untrusted.MaxName))
	}
	if d, ok := tok.(json.Delim); ok {
		isMap, opened := d == '{', j.line()
		if r := rule.shape(isMap, false, depth); r != "" {
			return nil, j.refuse(at, opened, r)
		}
		if r := rule.count(isMap, j.items(isMap), depth); r != "" {
			return nil, j.refuse(at, opened, r)
		}
		var out any
		if isMap {
			m := map[string]any{}
			for j.dec.More() {
				kt, err := j.dec.Token()
				if err != nil {
					return nil, fmt.Errorf("%s: does not parse: %s", safeName(j.rel), untrusted.Text(err.Error(), 2*untrusted.MaxName))
				}
				k := kt.(string)
				sub := at + "/" + strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1")
				if r := rule.key(k, depth); r != "" {
					return nil, j.refuse(sub, j.line(), r)
				}
				if _, dup := m[k]; dup {
					return nil, j.refuse(sub, j.line(), "the key is given twice")
				}
				v, err := j.value(sub, depth+1)
				if err != nil {
					return nil, err
				}
				m[k] = v
			}
			out = m
		} else {
			l := []any{}
			for j.dec.More() {
				v, err := j.value(at+"/"+strconv.Itoa(len(l)), depth+1)
				if err != nil {
					return nil, err
				}
				l = append(l, v)
			}
			out = l
		}
		if _, err := j.dec.Token(); err != nil { // the closing delimiter
			return nil, fmt.Errorf("%s: does not parse: %s", safeName(j.rel), untrusted.Text(err.Error(), 2*untrusted.MaxName))
		}
		return out, nil
	}
	line := j.line()
	if r := rule.shape(false, true, depth); r != "" {
		return nil, j.refuse(at, line, r)
	}
	switch v := tok.(type) {
	case string:
		if r := rule.str(v); r != "" {
			return nil, j.refuse(at, line, r)
		}
		return v, nil
	case bool:
		return v, nil
	case nil:
		return nil, nil
	case json.Number:
		n, r := jsonNumber(v)
		if r != "" {
			return nil, j.refuse(at, line, r)
		}
		return n, nil
	}
	return nil, j.refuse(at, line, "a value of an unsupported kind")
}
