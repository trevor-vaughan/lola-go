package targetspec

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v3"
)

// Error is a spec that cannot be loaded: the file, the field path within it
// (eras[1].layout.rule.path), or "" for the whole file, what is wrong, and the fix.
type Error struct{ File, Field, Problem, Fix string }

func (e *Error) Error() string {
	if e.Field == "" {
		return e.File + ": " + e.Problem
	}
	return e.File + ": " + e.Field + ": " + e.Problem
}

// Parse decodes and validates one spec; file names it in errors. The spec's eras come back
// resolved (extends and remove applied, templates expanded) and its patterns compiled.
func Parse(file string, data []byte) (Spec, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var s Spec
	if err := dec.Decode(&s); err != nil {
		if errors.Is(err, io.EOF) {
			return Spec{}, &Error{File: file, Problem: "is empty", Fix: "write a target spec: id:, roots:, detect: and eras:"}
		}
		return Spec{}, &Error{File: file, Problem: err.Error(), Fix: "write the spec as one YAML mapping with only the documented fields"}
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return Spec{}, &Error{File: file, Problem: "holds more than one YAML document", Fix: "put each target spec in its own file"}
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return Spec{}, &Error{File: file, Problem: err.Error(), Fix: "write the spec as one YAML mapping with only the documented fields"}
	}
	if err := (validator{file: file}).plain(&doc, nil); err != nil {
		return Spec{}, err
	}
	if err := (validator{file: file}).spec(&s); err != nil {
		return Spec{}, err
	}
	s.File = file
	return s, nil
}

// patterns are the paths of a spec's regular expressions, "[]" standing for any index and "*" for
// any key.
var patterns = [][]string{
	{"probe", "[]", "exec", "match"},
	{"probe", "[]", "file", "match"},
	{"eras", "[]", "behaviors", "[]", "detect"},
	{"eras", "[]", "kinds", "*", "name"},
	{"eras", "[]", "kinds", "*", "keys", "*", "accepts", "pattern"},
}

// plain rejects a control or format character in any key or string of n, the spec's YAML at
// path: lola puts a spec's text into findings and hints, which reach the terminal. A pattern may
// hold a tab or a newline, for its expression to match. An alias is checked at the path it lands
// at, so text anchored at a pattern field cannot reach a free-text field.
func (v validator) plain(n *yaml.Node, path []string) error {
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			if err := v.plain(c, path); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for i, c := range n.Content {
			if err := v.plain(c, append(slices.Clip(path), fmt.Sprintf("[%d]", i))); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			p := append(slices.Clip(path), n.Content[i].Value)
			if err := v.plain(n.Content[i], p); err != nil {
				return err
			}
			if err := v.plain(n.Content[i+1], p); err != nil {
				return err
			}
		}
	case yaml.AliasNode:
		// Cycle-safe: Parse decodes into its typed struct before this walk, and no field's type
		// can hold itself, so the decode rejects a self-containing anchor as a type mismatch.
		return v.plain(n.Alias, path)
	case yaml.ScalarNode:
		pattern := slices.ContainsFunc(patterns, func(want []string) bool {
			if len(want) != len(path) {
				return false
			}
			for i, w := range want {
				if w != "*" && w != path[i] && (w != "[]" || !strings.HasPrefix(path[i], "[")) {
					return false
				}
			}
			return true
		})
		for _, r := range n.Value {
			if pattern && (r == '\t' || r == '\n') {
				continue
			}
			if r < 0x20 || r >= 0x7f && r <= 0x9f || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
				field := ""
				for _, s := range path {
					if field != "" && !strings.HasPrefix(s, "[") {
						field += "."
					}
					field += s
				}
				return v.fail(field, "holds a control or format character", "remove it; specs hold plain text")
			}
		}
	}
	return nil
}

// ParseFS parses every *.yaml file directly in dir of fsys, in file-name order; source + "/" +
// the file's name names it in errors. Two specs with one id are an error.
func ParseFS(fsys fs.FS, dir, source string) ([]Spec, error) {
	names, err := fs.Glob(fsys, path.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	var specs []Spec
	seen := map[string]string{}
	for _, n := range names {
		file := source + "/" + path.Base(n)
		data, err := fs.ReadFile(fsys, n)
		if err != nil {
			return nil, &Error{File: file, Problem: err.Error(), Fix: "make the file readable, or remove it"}
		}
		s, err := Parse(file, data)
		if err != nil {
			return nil, err
		}
		if prev, dup := seen[s.ID]; dup {
			return nil, &Error{File: file, Field: "id", Problem: fmt.Sprintf("%s is also the id of %s", s.ID, prev),
				Fix: "give each spec its own id, or delete one of the files"}
		}
		seen[s.ID] = file
		specs = append(specs, s)
	}
	return specs, nil
}

// extend resolves e on prev: a copy of prev with e's remove entries deleted, then e's layout
// kinds, keys per kind, other kind fields given, vars, behaviors by id and shared replacing
// prev's. field is e's path, for errors.
func (v validator) extend(field string, prev, e Era) (Era, error) {
	out := Era{Since: e.Since, Vars: map[string]Scoped{}, Entries: maps.Clone(prev.Entries), Kinds: map[string]Kind{},
		Behaviors: slices.Clone(prev.Behaviors), Shared: prev.Shared}
	maps.Copy(out.Vars, prev.Vars)
	for k, kind := range prev.Kinds {
		kind.Keys = maps.Clone(kind.Keys)
		out.Kinds[k] = kind
	}
	for i, r := range e.Remove {
		f := fmt.Sprintf("%s.remove[%d]", field, i)
		missing := v.fail(f, r+" is not in the previous era", "remove only entries the previous era has")
		parts := strings.Split(r, ".")
		switch {
		case len(parts) == 2 && parts[0] == "layout":
			if _, ok := out.Entries[parts[1]]; !ok {
				return Era{}, missing
			}
			delete(out.Entries, parts[1])
		case len(parts) == 4 && parts[0] == "kinds" && parts[2] == "keys":
			if _, ok := out.Kinds[parts[1]].Keys[parts[3]]; !ok {
				return Era{}, missing
			}
			delete(out.Kinds[parts[1]].Keys, parts[3])
		case len(parts) == 2 && parts[0] == "behaviors":
			i := slices.IndexFunc(out.Behaviors, func(b Behavior) bool { return b.ID == parts[1] })
			if i < 0 {
				return Era{}, missing
			}
			out.Behaviors = slices.Delete(out.Behaviors, i, i+1)
		default:
			return Era{}, v.fail(f, fmt.Sprintf("%q is not layout.<kind>, kinds.<kind>.keys.<key> or behaviors.<id>", r),
				"name the entry to delete, e.g. layout.rule")
		}
	}
	maps.Copy(out.Vars, e.Vars)
	if out.Entries == nil {
		out.Entries = map[string]Entry{}
	}
	maps.Copy(out.Entries, e.Entries)
	for k, x := range e.Kinds {
		kind := out.Kinds[k]
		if x.UnknownKeys != "" {
			kind.UnknownKeys = x.UnknownKeys
		}
		if x.Required != nil {
			kind.Required = x.Required
		}
		if x.Name != nil {
			kind.Name = x.Name
		}
		if kind.Keys == nil {
			kind.Keys = map[string]Key{}
		}
		maps.Copy(kind.Keys, x.Keys)
		out.Kinds[k] = kind
	}
	for _, b := range e.Behaviors {
		if i := slices.IndexFunc(out.Behaviors, func(x Behavior) bool { return x.ID == b.ID }); i >= 0 {
			out.Behaviors[i] = b
		} else {
			out.Behaviors = append(out.Behaviors, b)
		}
	}
	if e.Shared != nil {
		out.Shared = e.Shared
	}
	return out, nil
}
