package fsx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
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
	cur, _ := lookup(&root, toks)
	if cur == nil {
		return nil, false, nil
	}
	c, err := CanonicalJSON(cur.Pack())
	return c, true, err
}

// lookup returns the value at toks and the indentation of the line that holds it, or nil
// when a member is missing or a parent is not an object.
func lookup(root *hujson.Value, toks []string) (*hujson.Value, string) {
	cur, indent := root, ""
	for _, t := range toks {
		obj, ok := cur.Value.(*hujson.Object)
		if !ok {
			return nil, ""
		}
		i := memberIndex(obj, t)
		if i < 0 {
			return nil, ""
		}
		indent, _ = objectItems(obj).indent(indent)
		cur = &obj.Members[i].Value
	}
	return cur, indent
}

// JSONHasMember reports whether the array at ptr contains member, a JSON value; elements
// compare as canonical JSON. A missing array is false; a value at ptr that is not an array is
// an error.
func JSONHasMember(b []byte, ptr string, member []byte) (bool, error) {
	want, err := CanonicalJSON(member)
	if err != nil {
		return false, err
	}
	v, found, err := JSONGet(b, ptr)
	if err != nil || !found {
		return false, err
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(v, &arr); err != nil {
		return false, fmt.Errorf("%q is not an array", ptr)
	}
	return slices.ContainsFunc(arr, func(e json.RawMessage) bool { return bytes.Equal(e, want) }), nil
}

// JSONAddMember appends member, a JSON value, to the array at ptr unless an element equal to it
// as canonical JSON is already there. A missing array is created, with any missing parent
// objects; created reports that. The element is written as ReadableJSON, laid out like its
// siblings, and bytes outside it are preserved.
func JSONAddMember(b []byte, ptr string, member []byte) (out []byte, created bool, err error) {
	toks, err := JSONPointerTokens(ptr)
	if err != nil {
		return nil, false, err
	}
	root, err := parseJSONC(b)
	if err != nil {
		return nil, false, err
	}
	want, err := CanonicalJSON(member)
	if err != nil {
		return nil, false, err
	}
	elem, err := ReadableJSON(member)
	if err != nil {
		return nil, false, err
	}
	cur, indent := lookup(&root, toks)
	if cur == nil {
		out, err := JSONSet(b, ptr, []byte("["+string(elem)+"]"))
		return out, err == nil, err
	}
	arr, ok := cur.Value.(*hujson.Array)
	if !ok {
		return nil, false, fmt.Errorf("%q is not an array", ptr)
	}
	i, err := elementIndex(arr, want)
	if err != nil || i >= 0 {
		return b, false, err
	}
	it := arrayItems(arr)
	childIndent, multiline := it.indent(indent)
	v, err := renderValue(elem, childIndent, multiline)
	if err != nil {
		return nil, false, err
	}
	v.BeforeExtra, v.AfterExtra = it.place(indent, childIndent, multiline)
	arr.Elements = append(arr.Elements, v)
	return root.Pack(), false, nil
}

// JSONRemoveMember removes the element equal to member, a JSON value, as canonical JSON from the
// array at ptr. When the array is then empty and dropEmpty is set, the array itself is removed,
// as are parent objects that became empty (JSONDelete). A missing array or member is not an
// error.
func JSONRemoveMember(b []byte, ptr string, member []byte, dropEmpty bool) ([]byte, error) {
	toks, err := JSONPointerTokens(ptr)
	if err != nil {
		return nil, err
	}
	root, err := parseJSONC(b)
	if err != nil {
		return nil, err
	}
	want, err := CanonicalJSON(member)
	if err != nil {
		return nil, err
	}
	cur, _ := lookup(&root, toks)
	if cur == nil {
		return b, nil
	}
	arr, ok := cur.Value.(*hujson.Array)
	if !ok {
		return nil, fmt.Errorf("%q is not an array", ptr)
	}
	i, err := elementIndex(arr, want)
	if err != nil || i < 0 {
		return b, err
	}
	arrayItems(arr).unlink(i)
	arr.Elements = slices.Delete(arr.Elements, i, i+1)
	if dropEmpty && len(arr.Elements) == 0 {
		return JSONDelete(root.Pack(), ptr)
	}
	return root.Pack(), nil
}

// CanonicalJSON returns raw (JSON or JSONC) re-encoded with sorted keys and no whitespace, and
// with & < > escaped as encoding/json does. Hashes, ledger records and comparisons use it.
func CanonicalJSON(raw []byte) ([]byte, error) {
	return reencode(raw, true)
}

// ReadableJSON is CanonicalJSON with & < > written as themselves: the form lola writes into
// files people read and shows them, so a shell operator such as && in a hook reads as one.
// Compare and hash with CanonicalJSON.
func ReadableJSON(raw []byte) ([]byte, error) {
	return reencode(raw, false)
}

func reencode(raw []byte, escapeHTML bool) ([]byte, error) {
	v, err := hujson.Parse(raw)
	if err != nil {
		return nil, err
	}
	v.Standardize()
	var x any
	if err := json.Unmarshal(v.Pack(), &x); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(escapeHTML)
	if err := enc.Encode(x); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
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
			return nil, fmt.Errorf("%q: %q is not an object", ptr, "/"+strings.Join(toks[:depth], "/"))
		}
		childIndent, multiline := objectItems(obj).indent(indent)
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
		objectItems(obj).unlink(i)
		obj.Members = slices.Delete(obj.Members, i, i+1)
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
	if n := len(obj.Members); !multiline && n > 0 {
		m.Value.BeforeExtra = obj.Members[n-1].Value.BeforeExtra
	}
	m.Name.BeforeExtra, m.Value.AfterExtra = objectItems(obj).place(parentIndent, indent, multiline)
	obj.Members = append(obj.Members, m)
}

// elementIndex returns the index of the first element of arr whose canonical JSON is want, or -1.
func elementIndex(arr *hujson.Array, want []byte) (int, error) {
	for i, e := range arr.Elements {
		e.BeforeExtra, e.AfterExtra = nil, nil
		c, err := CanonicalJSON(e.Pack())
		if err != nil {
			return -1, err
		}
		if bytes.Equal(c, want) {
			return i, nil
		}
	}
	return -1, nil
}

// items is the layout shared by an object's members and an array's elements. before(i) is
// the Extra in front of item i (for a member, in front of its name), after(i) the Extra
// between item i and its comma, and tail the composite's AfterExtra: what precedes the
// closing brace or bracket.
type items struct {
	n             int
	before, after func(i int) *hujson.Extra
	tail          *hujson.Extra
}

func objectItems(obj *hujson.Object) items {
	return items{n: len(obj.Members), tail: &obj.AfterExtra,
		before: func(i int) *hujson.Extra { return &obj.Members[i].Name.BeforeExtra },
		after:  func(i int) *hujson.Extra { return &obj.Members[i].Value.AfterExtra }}
}

func arrayItems(arr *hujson.Array) items {
	return items{n: len(arr.Elements), tail: &arr.AfterExtra,
		before: func(i int) *hujson.Extra { return &arr.Elements[i].BeforeExtra },
		after:  func(i int) *hujson.Extra { return &arr.Elements[i].AfterExtra }}
}

// indent reports the indentation of the items and whether they are laid out one per line.
// An empty composite is treated as multiline, indented two spaces deeper than parentIndent.
func (it items) indent(parentIndent string) (string, bool) {
	if it.n == 0 {
		return parentIndent + "  ", true
	}
	before := string(*it.before(0))
	nl := strings.LastIndexByte(before, '\n')
	if nl < 0 {
		return parentIndent, false
	}
	return before[nl+1:], true
}

// place lays out a new last item. It returns the item's BeforeExtra and AfterExtra (which
// keeps a trailing-comma style). A comment that ended the old last line stays on that line;
// the lines after it, comments included, stay between the new item and the closing brace or
// bracket, so removing the item again restores them.
func (it items) place(parentIndent, indent string, multiline bool) (before, after hujson.Extra) {
	if !multiline {
		switch {
		case it.n == 1:
			before = hujson.Extra(" ")
		case it.n > 1:
			before = *it.before(it.n - 1)
		}
		return before, nil
	}
	lineEnd, rest := splitAtNewline(*it.tail)
	if rest == "" {
		rest = "\n" + parentIndent
	}
	if it.n > 0 && *it.after(it.n - 1) != nil {
		after = hujson.Extra{}
	}
	*it.tail = hujson.Extra(rest)
	return hujson.Extra(lineEnd + "\n" + indent), after
}

// unlink fixes the layout around item i before it is removed: the item's own comments go
// with it, and a comment that ends the previous line stays.
func (it items) unlink(i int) {
	gone := *it.before(i)
	keep, _ := splitAtNewline(gone)
	if !strings.Contains(string(gone), "\n") {
		keep = ""
	}
	if i < it.n-1 {
		next := it.before(i + 1)
		if _, rest := splitAtNewline(*next); rest == "" {
			*next = gone
		} else {
			*next = hujson.Extra(keep + rest)
		}
		return
	}
	_, rest := splitAtNewline(*it.tail)
	*it.tail = hujson.Extra(keep + rest)
	if i > 0 {
		*it.after(i - 1) = *it.after(i)
	}
}

// JSONIsEmptyObject reports whether b is an object with no members (comments ignored).
func JSONIsEmptyObject(b []byte) (bool, error) {
	v, err := parseJSONC(b)
	if err != nil {
		return false, err
	}
	return len(v.Value.(*hujson.Object).Members) == 0, nil
}
