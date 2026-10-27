package market

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// jval is a decoded JSON value whose objects keep every key in order, duplicates and case
// variants included (Plan 6c §4.1, ruling R-6c-17): encoding/json's struct decoding matches keys
// case-insensitively and keeps the last of a duplicate, so a catalog could show one key and act
// on another. kind is '{', '[', 's' (string), 'n' (number), 'b' (boolean) or 'z' (null).
type jval struct {
	kind  byte
	keys  []string
	vals  []*jval
	items []*jval
	str   string
}

// get is the value of the first key in o exactly equal to key, nil for none or a non-object.
func (o *jval) get(key string) *jval {
	if o == nil || o.kind != '{' {
		return nil
	}
	for i, k := range o.keys {
		if k == key {
			return o.vals[i]
		}
	}
	return nil
}

// ambiguous is the first key of o that is there twice, or that differs only in case from one of
// known (the keys lola reads or skips on there), "" for none.
func ambiguous(o *jval, known []string) string {
	seen := map[string]bool{}
	for _, k := range o.keys {
		if seen[k] {
			return k
		}
		seen[k] = true
		for _, kn := range known {
			if k != kn && strings.EqualFold(k, kn) {
				return k
			}
		}
	}
	return ""
}

// decodeJSON decodes b token by token into a jval, refusing nesting deeper than maxDepth and
// anything after the first value.
func decodeJSON(b []byte) (*jval, error) {
	if !utf8.Valid(b) {
		return nil, errors.New("holds bytes that are not UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	budget := maxValues
	v, err := readJSON(dec, 0, &budget)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("holds data after its JSON value")
	}
	return v, nil
}

// readJSON reads the next value of dec, inside depth containers, spending one of budget's values on
// it: the byte cap alone does not bound what decoding allocates (Plan 6c §10).
func readJSON(dec *json.Decoder, depth int, budget *int) (*jval, error) {
	if *budget--; *budget < 0 {
		return nil, fmt.Errorf("holds more than %d JSON values", maxValues)
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("does not parse: %s", untrusted.Text(err.Error(), untrusted.MaxName))
	}
	switch t := tok.(type) {
	case json.Delim:
		if depth >= maxDepth {
			return nil, fmt.Errorf("nests deeper than %d levels", maxDepth)
		}
		v := &jval{kind: '{'}
		if t == '[' {
			v.kind = '['
		}
		for dec.More() {
			if t == '{' {
				k, err := dec.Token()
				if err != nil {
					return nil, fmt.Errorf("does not parse: %s", untrusted.Text(err.Error(), untrusted.MaxName))
				}
				v.keys = append(v.keys, k.(string)) // json.Decoder yields only strings as object keys
			}
			item, err := readJSON(dec, depth+1, budget)
			if err != nil {
				return nil, err
			}
			if t == '{' {
				v.vals = append(v.vals, item)
			} else {
				v.items = append(v.items, item)
			}
		}
		if _, err := dec.Token(); err != nil { // the closing delimiter
			return nil, fmt.Errorf("does not parse: %s", untrusted.Text(err.Error(), untrusted.MaxName))
		}
		return v, nil
	case string:
		return &jval{kind: 's', str: t}, nil
	case json.Number:
		return &jval{kind: 'n', str: t.String()}, nil
	case bool:
		return &jval{kind: 'b'}, nil
	default:
		return &jval{kind: 'z'}, nil
	}
}

// The keys lola reads, or skips an entry on, in each object of a Claude catalog (Plan 6c §4.1).
var (
	topKeys      = []string{"name", "owner", "plugins", "description", "version", "metadata"}
	ownerKeys    = []string{"name", "email", "url"}
	metadataKeys = []string{"description", "version", "pluginRoot"}
	entryKeys    = []string{"name", "source", "description", "version", "category", "tags", "keywords", "dependencies"}
)

// skipOn are the entry fields whose presence skips the entry (ruling R-6c-4), with why: lola
// never sends a catalog's credentials or runs its commands, and reads components other than the
// content lists (contentKeys) from the plugin's own files, never from the catalog.
var skipOn = []struct{ key, kind, reason string }{
	{"headers", "entry with headers", "it carries headers, credentials lola never sends"},
	{"headersHelper", "entry with headersHelper", "its headersHelper runs a program; lola never does"},
	{"hooks", "entry declaring components", "the catalog declares its hooks"},
	{"outputStyles", "entry declaring components", "the catalog declares its outputStyles"},
	{"themes", "entry declaring components", "the catalog declares its themes"},
	{"mcpServers", "entry declaring components", "the catalog declares its mcpServers"},
	{"lspServers", "entry declaring components", "the catalog declares its lspServers"},
	{"userConfig", "entry declaring components", "the catalog declares its userConfig"},
	{"channels", "entry declaring components", "the catalog declares its channels"},
}

// componentsNote ends the reason of an entry skipped for declaring its components.
const componentsNote = "; lola reads a plugin's components from its own files, never from the catalog"

// contentKeys are the content lists an entry may give: the paths, in its own directory, of the
// skills, commands and agents that form the module (user decision, 2026-10-21, amending ruling
// R-6c-4). Only a relative entry may give them, as the marketplace's own tree is then the
// plugin's.
var contentKeys = []string{"skills", "commands", "agents"}

// entryKnown is every key lola reads or skips on in an entry.
var entryKnown = func() []string {
	out := append(append([]string{}, entryKeys...), contentKeys...)
	for _, s := range skipOn {
		out = append(out, s.key)
	}
	return out
}()

// notPlain is how a refusal says why s, which untrusted.IsPlain refuses, would not print as it is.
// A line break alone is named, with yaml's advice after it, since an author sees none in a YAML
// block scalar (description: >).
func notPlain(s, yaml string) string {
	if untrusted.IsPlain(strings.NewReplacer("\r", " ", "\n", " ").Replace(s)) {
		return "holds a line break; catalog text is one line" + yaml
	}
	return "holds control or invisible characters"
}

// plain refuses the catalog when a key of an object lola reads, or a string value of a field it
// reads or skips on (at any depth under that field), would not print as it is (ruling R-6c-20):
// field names it, as plugins[12].description.
func plain(v *jval, field string, walkKeys bool) error {
	switch v.kind {
	case 's':
		if !untrusted.IsPlain(v.str) {
			return fmt.Errorf("%s %s", untrusted.Text(field, untrusted.MaxName), notPlain(v.str, ""))
		}
	case '[':
		for i, item := range v.items {
			if err := plain(item, fmt.Sprintf("%s[%d]", field, i), walkKeys); err != nil {
				return err
			}
		}
	case '{':
		for i, k := range v.keys {
			if !untrusted.IsPlain(k) {
				return fmt.Errorf("a key of %s %s", untrusted.Text(cmpOr(field, "the catalog"), untrusted.MaxName), notPlain(k, ""))
			}
			if walkKeys {
				if err := plain(v.vals[i], field+"."+k, true); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// plainFields applies plain to the fields of o named in fields, and to every key of o.
func plainFields(o *jval, prefix string, fields []string) error {
	if err := plain(o, prefix, false); err != nil {
		return err
	}
	for i, k := range o.keys {
		for _, f := range fields {
			if k == f {
				if err := plain(o.vals[i], join(prefix, k), true); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// join is prefix.key, or key at the top level.
func join(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

// parseClaude reads a Claude Code marketplace.json (Plan 6c §4.1): the catalog is refused for
// what makes all of it unreadable or ambiguous, and each entry is skipped, with its reason, for
// what makes only it unusable.
func parseClaude(b []byte) (Catalog, error) {
	root, err := decodeJSON(b)
	if err != nil {
		return Catalog{}, err
	}
	if root.kind != '{' {
		return Catalog{}, errors.New("is not a JSON object")
	}
	if k := ambiguous(root, topKeys); k != "" {
		return Catalog{}, fmt.Errorf("the key %s is ambiguous: it is given twice, or differs only in case from one lola reads", untrusted.Text(k, untrusted.MaxName))
	}
	for _, o := range []struct {
		key   string
		known []string
	}{{"owner", ownerKeys}, {"metadata", metadataKeys}} {
		if v := root.get(o.key); v != nil && v.kind == '{' {
			if k := ambiguous(v, o.known); k != "" {
				return Catalog{}, fmt.Errorf("the key %s.%s is ambiguous: it is given twice, or differs only in case from one lola reads", o.key,
					untrusted.Text(k, untrusted.MaxName))
			}
		}
	}
	if err := plainFields(root, "", []string{"name", "description", "version"}); err != nil {
		return Catalog{}, err
	}
	for _, o := range []struct {
		key    string
		fields []string
	}{{"owner", ownerKeys}, {"metadata", metadataKeys}} {
		if v := root.get(o.key); v != nil && v.kind == '{' {
			if err := plainFields(v, o.key, o.fields); err != nil {
				return Catalog{}, err
			}
		}
	}
	plugins := root.get("plugins")
	if plugins != nil && plugins.kind == '[' {
		for i, item := range plugins.items {
			if item.kind != '{' {
				continue
			}
			if err := plainFields(item, fmt.Sprintf("plugins[%d]", i), entryKnown); err != nil {
				return Catalog{}, err
			}
		}
	}
	cat := Catalog{Format: FormatClaude, Entries: []Entry{}, Skipped: []Skip{}}
	str := func(o *jval, key, field string, required bool) (string, error) {
		v := o.get(key)
		switch {
		case v == nil && required:
			return "", fmt.Errorf("has no %s", field)
		case v == nil:
			return "", nil
		case v.kind != 's':
			return "", fmt.Errorf("%s is not a string", field)
		}
		return v.str, nil
	}
	if cat.Name, err = str(root, "name", "name", true); err != nil {
		return Catalog{}, err
	}
	owner := root.get("owner")
	switch {
	case owner == nil:
		return Catalog{}, errors.New("has no owner")
	case owner.kind != '{':
		return Catalog{}, errors.New("owner is not an object")
	}
	if cat.Owner, err = str(owner, "name", "owner.name", true); err != nil {
		return Catalog{}, err
	}
	if cat.Description, err = str(root, "description", "description", false); err != nil {
		return Catalog{}, err
	}
	if cat.Version, err = str(root, "version", "version", false); err != nil {
		return Catalog{}, err
	}
	pluginRoot := ""
	if meta := root.get("metadata"); meta != nil {
		if meta.kind != '{' {
			return Catalog{}, errors.New("metadata is not an object")
		}
		desc, err := str(meta, "description", "metadata.description", false)
		if err != nil {
			return Catalog{}, err
		}
		version, err := str(meta, "version", "metadata.version", false)
		if err != nil {
			return Catalog{}, err
		}
		if pluginRoot, err = str(meta, "pluginRoot", "metadata.pluginRoot", false); err != nil {
			return Catalog{}, err
		}
		if pluginRoot != "" {
			clean, err := CheckRelative(pluginRoot)
			if err != nil {
				return Catalog{}, fmt.Errorf("metadata.pluginRoot %w", err)
			}
			pluginRoot = clean
		}
		cat.Description = cmpOr(cat.Description, desc)
		cat.Version = cmpOr(cat.Version, version)
	}
	switch {
	case plugins == nil:
		return Catalog{}, errors.New("has no plugins")
	case plugins.kind != '[':
		return Catalog{}, errors.New("plugins is not a list")
	case len(plugins.items) > maxEntries:
		return Catalog{}, fmt.Errorf("lists more than %d plugins", maxEntries)
	}
	for _, item := range plugins.items {
		e, s := claudeEntry(item, pluginRoot)
		cat.add(e, s)
	}
	cat.dedupe()
	return cat, nil
}

// cmpOr is a, or b when a is "".
func cmpOr(a, b string) string {
	if a == "" {
		return b
	}
	return a
}

// claudeEntry reads one plugins item, o, whose relative sources resolve under pluginRoot.
func claudeEntry(o *jval, pluginRoot string) (Entry, *skip) {
	if o.kind != '{' {
		return Entry{}, skipped("malformed entry", "the entry is not an object")
	}
	if k := ambiguous(o, entryKnown); k != "" {
		// The name still counts toward "two entries of one name skip both" when it is itself unambiguous.
		var named Entry
		spellings := 0
		for _, key := range o.keys {
			if strings.EqualFold(key, "name") {
				spellings++
			}
		}
		if nv := o.get("name"); spellings == 1 && nv != nil && nv.kind == 's' {
			named.Name = nv.str
		}
		return named, skipped("ambiguous entry", "ambiguous key %s", untrusted.Text(k, untrusted.MaxName))
	}
	nameVal := o.get("name")
	if nameVal == nil || nameVal.kind != 's' {
		return Entry{}, skipped("malformed entry", "the entry has no name")
	}
	if err := checkEntryName(nameVal.str); err != nil {
		return Entry{}, err
	}
	e := Entry{Name: nameVal.str, Tags: []string{}, Notices: []string{}}
	for _, s := range skipOn {
		if o.get(s.key) != nil {
			reason := s.reason
			if s.kind == "entry declaring components" {
				reason += componentsNote
			}
			return e, skipped(s.kind, "%s", reason)
		}
	}
	for _, f := range []struct {
		key string
		to  *string
	}{{"description", &e.Description}, {"version", &e.Version}} {
		if v := o.get(f.key); v != nil {
			if v.kind != 's' {
				return e, skipped("malformed entry", "%s is not a string", f.key)
			}
			*f.to = v.str
		}
	}
	if s := checkEntryText(e); s != nil {
		return e, s
	}
	for _, key := range []string{"tags", "keywords"} {
		v := o.get(key)
		if v == nil {
			continue
		}
		if v.kind != '[' {
			return e, skipped("malformed entry", "%s is not a list of strings", key)
		}
		for _, t := range v.items {
			if t.kind != 's' {
				return e, skipped("malformed entry", "%s is not a list of strings", key)
			}
			e.Tags = append(e.Tags, t.str)
		}
	}
	if v := o.get("category"); v != nil {
		if v.kind != 's' {
			return e, skipped("malformed entry", "category is not a string")
		}
		e.Tags = append(e.Tags, v.str)
	}
	if s := checkTags(e.Tags); s != nil {
		return e, s
	}
	if v := o.get("dependencies"); v != nil {
		if v.kind != '[' {
			return e, skipped("malformed entry", "dependencies is not a list")
		}
		var names []string
		others := 0
		for _, d := range v.items {
			if d.kind == 's' {
				names = append(names, untrusted.Text(d.str, untrusted.MaxName))
				e.Dependencies = append(e.Dependencies, d.str)
			} else {
				others++
			}
		}
		if others > 0 {
			names = append(names, fmt.Sprintf("%d more", others))
		}
		if len(names) > 0 {
			e.Notices = append(e.Notices, "lists dependencies lola does not install: "+strings.Join(names, ", "))
		}
	}
	src := o.get("source")
	switch {
	case src == nil:
		return e, skipped("malformed entry", "the entry has no source")
	case src.kind == 's':
		if s := mapRelative(&e, src.str, pluginRoot); s != nil {
			return e, s
		}
		var s *skip
		e.Components, s = contentLists(o)
		return e, s
	case src.kind == '{':
		for _, k := range contentKeys {
			if o.get(k) != nil {
				return e, skipped("component list on another repository",
					"the catalog lists its %s, and it does not own that repository; lola reads such a plugin's components from its own files", k)
			}
		}
		return e, mapClaudeSource(&e, src)
	}
	return e, skipped("malformed entry", "source is neither a string nor an object")
}

// contentLists reads a relative entry's content lists (contentKeys), each a path or a list of
// paths in the entry's directory: relative, slash-separated, with no ".." and never the
// directory itself, and two paths of one kind never of one name, since each becomes
// <kind>/<name> in the module. It is nil when the entry gives none; lists that name nothing, or
// more than source.MaxComponents paths, skip it.
func contentLists(o *jval) (*Components, *skip) {
	listed := 0
	for _, key := range contentKeys {
		switch v := o.get(key); {
		case v == nil:
		case v.kind == '[':
			listed += len(v.items)
		default:
			listed++
		}
	}
	if listed > source.MaxComponents {
		return nil, skipped("oversized entry", "its skills, commands and agents lists name more than %d paths", source.MaxComponents)
	}
	var c *Components
	given := 0
	for _, key := range contentKeys {
		v := o.get(key)
		if v == nil {
			continue
		}
		items := []*jval{v}
		if v.kind == '[' {
			items = v.items
		}
		if c == nil {
			c = &Components{}
		}
		var paths []string
		names := map[string]bool{}
		for _, item := range items {
			if item.kind != 's' {
				return nil, skipped("malformed entry", "%s is not a path or a list of paths", key)
			}
			if len(item.str) > maxSource {
				return nil, skipped("oversized entry", "a %s path is longer than %d bytes", key, maxSource)
			}
			clean, err := CheckRelative(item.str)
			switch {
			case err != nil:
				return nil, skipped("path outside the plugin", "%s path %s is not a relative path inside the plugin's directory", key,
					untrusted.Text(item.str, untrusted.MaxName))
			case clean == ".":
				return nil, skipped("malformed entry", "%s path %s names the plugin's own directory, not one of its %s", key,
					untrusted.Text(item.str, untrusted.MaxName), key)
			case names[path.Base(clean)]:
				return nil, skipped("malformed entry", "%s lists two paths named %s", key, untrusted.Text(path.Base(clean), untrusted.MaxName))
			}
			names[path.Base(clean)] = true
			paths = append(paths, clean)
		}
		given += len(paths)
		switch key {
		case "skills":
			c.Skills = paths
		case "commands":
			c.Commands = paths
		default:
			c.Agents = paths
		}
	}
	if c != nil && given == 0 {
		return nil, skipped("malformed entry", "its skills, commands and agents lists name nothing")
	}
	if c != nil {
		if err := c.Check(); err != nil { // paths inside one another
			return nil, skipped("malformed entry", "%s", err.Error())
		}
	}
	return c, nil
}

// checkEntryName skips an entry whose name fails the module-name rule (Plan 6c §4.4).
func checkEntryName(name string) *skip {
	if err := validateEntryName(name); err != nil {
		return skipped("invalid name", "%s", err.Error())
	}
	return nil
}

// checkTags skips an entry with more than maxTags tags, or one longer than maxTag bytes.
func checkTags(tags []string) *skip {
	if len(tags) > maxTags {
		return skipped("oversized entry", "it has more than %d tags, keywords and categories", maxTags)
	}
	for _, t := range tags {
		if len(t) > maxTag {
			return skipped("oversized entry", "a tag is longer than %d bytes", maxTag)
		}
	}
	return nil
}

// add appends e to cat, or, for a skip, its name (when it has a usable one) and reason.
func (c *Catalog) add(e Entry, s *skip) {
	if s != nil {
		name := e.Name
		if validateEntryName(name) != nil {
			name = ""
		}
		c.Skipped = append(c.Skipped, Skip{Name: name, Reason: s.reason, Kind: s.kind})
		return
	}
	c.Entries = append(c.Entries, e)
}

// dedupe skips every entry whose name two or more entries of the catalog have, skipped ones
// included (Plan 6c §4.4).
func (c *Catalog) dedupe() {
	count := map[string]int{}
	for _, e := range c.Entries {
		count[e.Name]++
	}
	for _, s := range c.Skipped {
		if s.Name != "" {
			count[s.Name]++
		}
	}
	kept := []Entry{}
	for _, e := range c.Entries {
		if count[e.Name] > 1 {
			c.Skipped = append(c.Skipped, Skip{Name: e.Name, Reason: "the catalog lists " + e.Name + " twice; lola cannot tell which is meant", Kind: "name listed twice"})
			continue
		}
		kept = append(kept, e)
	}
	c.Entries = kept
}
