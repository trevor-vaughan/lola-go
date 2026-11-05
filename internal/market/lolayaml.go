package market

import (
	"errors"
	"fmt"
	"slices"

	"go.yaml.in/yaml/v3"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
	"github.com/trevor-vaughan/lola-go/internal/yamldoc"
)

// validateEntryName is the module-name rule an entry's name follows (Plan 6c §4.4).
func validateEntryName(name string) error { return module.ValidateName("entry", name) }

// The fields lola reads in a Python lola catalog and in each of its modules (Plan 6c §4.2).
var (
	yamlTopFields    = []string{"name", "description", "version"}
	yamlModuleFields = []string{"name", "description", "version", "repository", "path", "ref"}
)

// scalarAdvice follows a refusal of a line break in a YAML catalog's text.
const scalarAdvice = ", so use a plain or quoted scalar"

// scriptsNote is the notice of a Python entry whose hooks lola never runs.
const scriptsNote = "catalog install scripts are ignored"

// plainYAML refuses the catalog when a key of the mapping m, or the text of one of fields in it,
// a scalar or a list of scalars, would not print as it is (ruling R-6c-20); prefix names m.
func plainYAML(m *yaml.Node, prefix string, fields []string) error {
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, v := m.Content[i], m.Content[i+1]
		if !untrusted.IsPlain(k.Value) {
			return fmt.Errorf("line %d: a key of %s %s", k.Line, cmpOr(prefix, "the catalog"), notPlain(k.Value, ""))
		}
		for _, f := range fields {
			if k.Value != f {
				continue
			}
			vals := []*yaml.Node{v}
			if v.Kind == yaml.SequenceNode {
				vals = v.Content
			}
			for _, s := range vals {
				if s.Kind == yaml.ScalarNode && !untrusted.IsPlain(s.Value) {
					return fmt.Errorf("line %d: %s %s", s.Line, join(prefix, f), notPlain(s.Value, scalarAdvice))
				}
			}
		}
	}
	return nil
}

// parseYAML reads a Python lola catalog (Plan 6c §4.2) through yamldoc.Decode: one document, no
// anchors, aliases, merge keys or duplicate keys. Each module needs only a name and a
// repository; one that cannot be used is skipped with its reason, and the rest load.
func parseYAML(b []byte) (Catalog, error) {
	if len(b) > maxYAMLCatalog {
		return Catalog{}, fmt.Errorf("is larger than %d bytes, the most lola reads of a YAML catalog", maxYAMLCatalog)
	}
	d, err := yamldoc.Decode(b)
	if err != nil {
		return Catalog{}, err
	}
	if n := nodes(d); n > maxNodes {
		return Catalog{}, fmt.Errorf("holds more than %d YAML nodes", maxNodes)
	}
	root := yamldoc.Root(d)
	if root == nil || root.Kind != yaml.MappingNode {
		return Catalog{}, errors.New("is not a YAML mapping")
	}
	if err := plainYAML(root, "", yamlTopFields); err != nil {
		return Catalog{}, err
	}
	modules := yamldoc.Value(root, "modules")
	if modules != nil && modules.Kind == yaml.SequenceNode {
		for i, m := range modules.Content {
			if m.Kind == yaml.MappingNode {
				if err := plainYAML(m, fmt.Sprintf("modules[%d]", i), slices.Concat(yamlModuleFields, []string{"tags"})); err != nil {
					return Catalog{}, err
				}
			}
		}
	}
	cat := Catalog{Format: FormatYAML, Entries: []Entry{}, Skipped: []Skip{}}
	text := func(m *yaml.Node, key string) (string, bool) {
		v := yamldoc.Value(m, key)
		if v == nil || v.Kind == yaml.ScalarNode && v.Tag == "!!null" {
			return "", true
		}
		return v.Value, v.Kind == yaml.ScalarNode
	}
	for _, f := range []struct {
		key string
		to  *string
	}{{"name", &cat.Name}, {"description", &cat.Description}, {"version", &cat.Version}} {
		v, ok := text(root, f.key)
		if !ok {
			return Catalog{}, fmt.Errorf("%s is not text", f.key)
		}
		*f.to = v
	}
	switch {
	case cat.Name == "":
		return Catalog{}, errors.New("has no name")
	case modules == nil:
		return Catalog{}, errors.New("has no modules")
	case modules.Kind != yaml.SequenceNode:
		return Catalog{}, errors.New("modules is not a list")
	case len(modules.Content) > maxEntries:
		return Catalog{}, fmt.Errorf("lists more than %d modules", maxEntries)
	}
	for _, m := range modules.Content {
		e, s := yamlEntry(m, text)
		cat.add(e, s)
	}
	cat.dedupe()
	return cat, nil
}

// nodes counts the nodes of the document d.
func nodes(d *yaml.Node) int {
	if d == nil {
		return 0
	}
	n := 1
	for _, c := range d.Content {
		n += nodes(c)
	}
	return n
}

// yamlEntry reads one module of a Python catalog, m; text reads a scalar field of it.
func yamlEntry(m *yaml.Node, text func(*yaml.Node, string) (string, bool)) (Entry, *skip) {
	if m.Kind != yaml.MappingNode {
		return Entry{}, skipped("malformed entry", "line %d: the module is not a mapping", m.Line)
	}
	vals := map[string]string{}
	for _, f := range yamlModuleFields {
		v, ok := text(m, f)
		if !ok {
			return Entry{}, skipped("malformed entry", "line %d: %s is not text", m.Line, f)
		}
		vals[f] = v
	}
	if vals["name"] == "" {
		return Entry{}, skipped("malformed entry", "line %d: the module has no name", m.Line)
	}
	if s := checkEntryName(vals["name"]); s != nil {
		return Entry{}, s
	}
	e := Entry{Name: vals["name"], Description: vals["description"], Version: vals["version"], Tags: []string{}, Notices: []string{}}
	if s := checkEntryText(e); s != nil {
		return e, s
	}
	if tags := yamldoc.Value(m, "tags"); tags != nil && (tags.Kind != yaml.ScalarNode || tags.Tag != "!!null") {
		if tags.Kind != yaml.SequenceNode {
			return e, skipped("malformed entry", "tags is not a list")
		}
		for _, t := range tags.Content {
			if t.Kind != yaml.ScalarNode {
				return e, skipped("malformed entry", "tags is not a list of text")
			}
			e.Tags = append(e.Tags, t.Value)
		}
	}
	if s := checkTags(e.Tags); s != nil {
		return e, s
	}
	if yamldoc.Value(m, "hooks") != nil {
		e.Notices = append(e.Notices, scriptsNote)
	}
	if vals["repository"] == "" {
		return e, skipped("malformed entry", "the module has no repository")
	}
	return e, mapPython(&e, vals["repository"], vals["ref"], vals["path"])
}

// Parse reads b, a catalog in format, into the model (Plan 6c §4): over MaxCatalog bytes, a
// catalog that does not decode, or one with an ambiguous key or text that would not print as it
// is refuses whole, saying why; an entry lola cannot use is skipped with its reason. Relative
// entries are left for Bind.
func Parse(format string, b []byte) (Catalog, error) {
	if len(b) > MaxCatalog {
		return Catalog{}, fmt.Errorf("is larger than %d bytes", MaxCatalog)
	}
	var cat Catalog
	var err error
	switch format {
	case FormatClaude:
		cat, err = parseClaude(b)
	case FormatYAML:
		cat, err = parseYAML(b)
	default:
		return Catalog{}, fmt.Errorf("format %s is not one lola reads", untrusted.Text(format, untrusted.MaxName))
	}
	if err != nil {
		return Catalog{}, err
	}
	for _, f := range []struct {
		field, value string
		limit        int
	}{{"name", cat.Name, maxText}, {"owner.name", cat.Owner, maxText}, {"version", cat.Version, maxText}, {"description", cat.Description, maxDescription}} {
		if len(f.value) > f.limit {
			return Catalog{}, fmt.Errorf("%s is longer than %d bytes", f.field, f.limit)
		}
	}
	return cat, nil
}

// checkEntryText skips an entry whose description or version is longer than lola prints.
func checkEntryText(e Entry) *skip {
	switch {
	case len(e.Description) > maxEntryDescription:
		return skipped("oversized entry", "description is longer than %d bytes", maxEntryDescription)
	case len(e.Version) > maxText:
		return skipped("oversized entry", "version is longer than %d bytes", maxText)
	}
	return nil
}
