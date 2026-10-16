package fsx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tailscale/hujson"
)

// JSONPointerTokens splits an RFC 6901 pointer into unescaped reference tokens.
// Only object navigation is supported: lola never addresses array elements.
func JSONPointerTokens(ptr string) ([]string, error) {
	if ptr == "" || ptr[0] != '/' {
		return nil, fmt.Errorf("json pointer %q must start with /", ptr)
	}
	parts := strings.Split(ptr[1:], "/")
	for i, p := range parts {
		if p == "" {
			return nil, fmt.Errorf("json pointer %q has an empty token", ptr)
		}
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(p, "~1", "/"), "~0", "~")
	}
	return parts, nil
}

// parseJSONC parses JSONC bytes; an empty or whitespace-only document is an empty object.
// The root must be an object.
func parseJSONC(b []byte) (hujson.Value, error) {
	if len(bytes.TrimSpace(b)) == 0 {
		b = []byte("{}\n")
	}
	v, err := hujson.Parse(b)
	if err != nil {
		return hujson.Value{}, err
	}
	if _, ok := v.Value.(*hujson.Object); !ok {
		return hujson.Value{}, fmt.Errorf("top-level value is not an object")
	}
	return v, nil
}

// JSONGet returns the canonical JSON of the value at ptr (sorted keys, no comments).
func JSONGet(b []byte, ptr string) (value []byte, found bool, err error) {
	toks, err := JSONPointerTokens(ptr)
	if err != nil {
		return nil, false, err
	}
	root, err := parseJSONC(b)
	if err != nil {
		return nil, false, err
	}
	cur := &root
	for _, t := range toks {
		obj, ok := cur.Value.(*hujson.Object)
		if !ok {
			return nil, false, nil
		}
		i := memberIndex(obj, t)
		if i < 0 {
			return nil, false, nil
		}
		cur = &obj.Members[i].Value
	}
	c, err := CanonicalJSON(cur.Pack())
	return c, true, err
}

// CanonicalJSON returns raw (JSON or JSONC) re-encoded with sorted keys and no whitespace.
func CanonicalJSON(raw []byte) ([]byte, error) {
	v, err := hujson.Parse(raw)
	if err != nil {
		return nil, err
	}
	v.Standardize()
	var x any
	if err := json.Unmarshal(v.Pack(), &x); err != nil {
		return nil, err
	}
	return json.Marshal(x)
}

// JSONSet sets the member at ptr to value (standard JSON), creating missing parent objects.
// Bytes outside the edited member are preserved; new members copy their siblings' indentation.
func JSONSet(b []byte, ptr string, value []byte) ([]byte, error) {
	toks, err := JSONPointerTokens(ptr)
	if err != nil {
		return nil, err
	}
	root, err := parseJSONC(b)
	if err != nil {
		return nil, err
	}
	cur, indent := &root, ""
	for depth, t := range toks {
		obj, ok := cur.Value.(*hujson.Object)
		if !ok {
			return nil, fmt.Errorf("%s: %q is not an object", ptr, "/"+strings.Join(toks[:depth], "/"))
		}
		childIndent, multiline := memberIndent(obj, indent)
		val := value
		if depth < len(toks)-1 {
			val = []byte("{}")
		}
		i := memberIndex(obj, t)
		if i >= 0 && depth < len(toks)-1 {
			cur, indent = &obj.Members[i].Value, childIndent
			continue
		}
		rendered, err := renderValue(val, childIndent, multiline)
		if err != nil {
			return nil, err
		}
		if i >= 0 {
			obj.Members[i].Value.Value = rendered.Value
		} else {
			appendMember(obj, t, rendered, indent, childIndent, multiline)
			i = len(obj.Members) - 1
		}
		cur, indent = &obj.Members[i].Value, childIndent
	}
	return root.Pack(), nil
}

// JSONDelete removes the member at ptr, then removes parent objects that became empty
// (the root object is kept). A missing member is not an error.
func JSONDelete(b []byte, ptr string) ([]byte, error) {
	toks, err := JSONPointerTokens(ptr)
	if err != nil {
		return nil, err
	}
	root, err := parseJSONC(b)
	if err != nil {
		return nil, err
	}
	path := []*hujson.Object{root.Value.(*hujson.Object)}
	for _, t := range toks[:len(toks)-1] {
		i := memberIndex(path[len(path)-1], t)
		if i < 0 {
			return b, nil
		}
		next, ok := path[len(path)-1].Members[i].Value.Value.(*hujson.Object)
		if !ok {
			return b, nil
		}
		path = append(path, next)
	}
	for d := len(path) - 1; d >= 0; d-- {
		obj := path[d]
		i := memberIndex(obj, toks[d])
		if i < 0 {
			return b, nil
		}
		removeMember(obj, i)
		if d == 0 || len(obj.Members) > 0 {
			break
		}
	}
	return root.Pack(), nil
}

func memberIndex(obj *hujson.Object, name string) int {
	for i, m := range obj.Members {
		if lit, ok := m.Name.Value.(hujson.Literal); ok && lit.String() == name {
			return i
		}
	}
	return -1
}

// memberIndent reports the indentation used for obj's members and whether obj is laid out
// one member per line. An empty object is treated as multiline, indented two spaces deeper.
func memberIndent(obj *hujson.Object, parentIndent string) (string, bool) {
	if len(obj.Members) == 0 {
		return parentIndent + "  ", true
	}
	before := string(obj.Members[0].Name.BeforeExtra)
	nl := strings.LastIndexByte(before, '\n')
	if nl < 0 {
		return parentIndent, false
	}
	return before[nl+1:], true
}

func renderValue(val []byte, indent string, multiline bool) (hujson.Value, error) {
	var buf bytes.Buffer
	var err error
	if multiline {
		err = json.Indent(&buf, val, indent, "  ")
	} else {
		err = json.Compact(&buf, val)
	}
	if err != nil {
		return hujson.Value{}, err
	}
	v, err := hujson.Parse(buf.Bytes())
	if err != nil {
		return hujson.Value{}, err
	}
	v.BeforeExtra = hujson.Extra(" ")
	return v, nil
}

// splitAtNewline splits extra into the part that ends the previous line (before the first
// newline) and the rest (from the first newline on).
func splitAtNewline(e hujson.Extra) (string, string) {
	s := string(e)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i], s[i:]
	}
	return s, ""
}

func appendMember(obj *hujson.Object, name string, val hujson.Value, parentIndent, indent string, multiline bool) {
	m := hujson.ObjectMember{Name: hujson.Value{Value: hujson.String(name)}, Value: val}
	if !multiline {
		if n := len(obj.Members); n > 0 {
			m.Name.BeforeExtra = obj.Members[n-1].Name.BeforeExtra
			m.Value.BeforeExtra = obj.Members[n-1].Value.BeforeExtra
			if n == 1 {
				m.Name.BeforeExtra = hujson.Extra(" ")
			}
		}
		obj.Members = append(obj.Members, m)
		return
	}
	lineEnd, rest := splitAtNewline(obj.AfterExtra)
	closing := "\n" + parentIndent
	if rest != "" {
		closing = rest[strings.LastIndexByte(rest, '\n'):]
	}
	if len(obj.Members) > 0 {
		last := &obj.Members[len(obj.Members)-1]
		if last.Value.AfterExtra != nil {
			m.Value.AfterExtra = hujson.Extra{}
		}
	}
	m.Name.BeforeExtra = hujson.Extra(lineEnd + "\n" + indent)
	obj.Members = append(obj.Members, m)
	obj.AfterExtra = hujson.Extra(closing)
}

func removeMember(obj *hujson.Object, i int) {
	gone := obj.Members[i]
	keep, _ := splitAtNewline(gone.Name.BeforeExtra)
	if !strings.Contains(string(gone.Name.BeforeExtra), "\n") {
		keep = ""
	}
	if i < len(obj.Members)-1 {
		next := &obj.Members[i+1]
		_, rest := splitAtNewline(next.Name.BeforeExtra)
		if rest == "" {
			next.Name.BeforeExtra = gone.Name.BeforeExtra
		} else {
			next.Name.BeforeExtra = hujson.Extra(keep + rest)
		}
	} else {
		_, rest := splitAtNewline(obj.AfterExtra)
		obj.AfterExtra = hujson.Extra(keep + rest)
		if i > 0 {
			obj.Members[i-1].Value.AfterExtra = gone.Value.AfterExtra
		}
	}
	obj.Members = append(obj.Members[:i], obj.Members[i+1:]...)
}

// JSONIsEmptyObject reports whether b is an object with no members (comments ignored).
func JSONIsEmptyObject(b []byte) (bool, error) {
	v, err := parseJSONC(b)
	if err != nil {
		return false, err
	}
	return len(v.Value.(*hujson.Object).Members) == 0, nil
}
