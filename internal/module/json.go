package module

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"unicode"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Limits for every JSON file lola reads from a module (Plan 5b design §3.1).
const (
	maxJSONSize  = 1 << 20 // bytes
	maxJSONDepth = 64      // nested objects and arrays; module configuration is a few levels deep
)

// readJSON decodes the module file rel (relative to dir) into v. The file must be a regular
// file of at most 1 MiB holding one JSON value that passes checkJSONKeys. Errors name rel, and
// every text from the file goes through untrusted.Text.
func readJSON(dir, rel string, v any) error {
	shown := untrusted.Text(rel, untrusted.MaxName)
	path := filepath.Join(dir, rel)
	notRegular := errs.WithHints(fmt.Errorf("%s is not a regular file", shown), "module JSON files must be plain files, not links, pipes or devices")
	linfo, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("%s: %w", shown, untrusted.CleanErr(err))
	}
	if linfo.Mode()&fs.ModeSymlink != 0 {
		return errs.WithHints(fmt.Errorf("%s is a symbolic link", shown), "lola never follows links in a module")
	}
	if !linfo.Mode().IsRegular() {
		return notRegular // checked before opening, so a FIFO never blocks
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%s: %w", shown, untrusted.CleanErr(err))
	}
	defer func() { _ = f.Close() }()
	finfo, err := f.Stat()
	if err != nil {
		return fmt.Errorf("%s: %w", shown, untrusted.CleanErr(err))
	}
	if !finfo.Mode().IsRegular() || !os.SameFile(linfo, finfo) {
		return notRegular // swapped for a link or special file between the checks
	}
	b, err := io.ReadAll(io.LimitReader(f, maxJSONSize+1))
	if err != nil {
		return fmt.Errorf("%s: %w", shown, untrusted.CleanErr(err))
	}
	if len(b) > maxJSONSize {
		return errs.WithHints(fmt.Errorf("%s is larger than 1 MiB", shown), "lola reads module JSON files of at most 1 MiB")
	}
	if err := checkJSONKeys(b); err != nil {
		return errs.WithHints(fmt.Errorf("%s: %s", shown, err.Error()), errs.Hints(err)...) // checkJSONKeys made its text safe
	}
	if err := json.Unmarshal(b, v); err != nil {
		if ute, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
			return errs.WithHints(typeErr(shown, ute), "fix the JSON in "+shown+" so it matches the layout lola documents")
		}
		return errs.WithHints(fmt.Errorf("%s: %s", shown, untrusted.Text(strings.TrimPrefix(err.Error(), "json: "), 2*untrusted.MaxName)),
			"fix the JSON in "+shown+" so it matches the layout lola documents")
	}
	return nil
}

// jsonLevel is one open object or array while checkJSONKeys walks a document.
type jsonLevel struct {
	object   bool
	wantKey  bool              // an object's next token is a key or its closing '}'
	freeForm bool              // the object is /mcpServers/<server>/env or /mcpServers/<server>/headers
	keys     map[string]bool   // member names
	folded   map[string]string // the first member name by foldKey
	key      string            // the object member whose value is being read
	index    int               // the array element being read
}

// checkJSONKeys refuses a document with nesting deeper than maxJSONDepth, or with an object
// key repeated at any depth: Go's decoder keeps the last of two keys, so a second server or
// setting could silently replace the first. The decoder also matches struct fields regardless
// of case (so "MCPSERVERS" and "mcpſervers" both fill mcpServers), so keys that differ only in
// case are refused too, except among the names in /mcpServers/<server>/env and
// /mcpServers/<server>/headers, where case-distinct names such as HTTP_PROXY and http_proxy are
// legitimate; nowhere else, so a server named env keeps the rule for its fields. Error text is
// safe to print. It walks the tokens without recursion, so a hostile depth costs no stack.
func checkJSONKeys(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var stack []*jsonLevel
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return errors.New(untrusted.Text(err.Error(), 2*untrusted.MaxName))
		}
		if n := len(stack); n > 0 && stack[n-1].wantKey {
			top := stack[n-1]
			if tok == json.Delim('}') {
				stack = valueDone(stack[:n-1])
				continue
			}
			key, _ := tok.(string) // the decoder returns only a key or '}' here
			first, seen := top.folded[foldKey(key)]
			switch {
			case top.keys[key]:
				return errs.WithHints(fmt.Errorf("key %s appears twice in %s", quoted(key), jsonPointer(stack[:n-1])),
					"keep one of them: a JSON reader would silently use only one")
			case seen && !top.freeForm:
				return errs.WithHints(fmt.Errorf("key %s differs from %s only in case, in %s", quoted(key), quoted(first), jsonPointer(stack[:n-1])),
					"keep one spelling: lola reads these keys regardless of case, so the two would clash; only the names in env and headers may differ just in case")
			case !seen:
				top.folded[foldKey(key)] = key
			}
			top.keys[key], top.key, top.wantKey = true, key, false
			continue
		}
		switch tok {
		case json.Delim('{'), json.Delim('['):
			if len(stack) == maxJSONDepth {
				return errs.WithHints(fmt.Errorf("nested deeper than %d levels", maxJSONDepth), "module JSON files are a few levels deep; flatten the nesting")
			}
			object := tok == json.Delim('{')
			freeForm := object && len(stack) == 3 && stack[0].object && stack[0].key == "mcpServers" && stack[1].object &&
				stack[2].object && (stack[2].key == "env" || stack[2].key == "headers")
			stack = append(stack, &jsonLevel{object: object, wantKey: object, freeForm: freeForm, keys: map[string]bool{}, folded: map[string]string{}})
		case json.Delim(']'):
			stack = valueDone(stack[:len(stack)-1])
		default:
			stack = valueDone(stack)
		}
	}
}

// valueDone records that a value inside the innermost open object or array ended.
func valueDone(stack []*jsonLevel) []*jsonLevel {
	if n := len(stack); n > 0 {
		if stack[n-1].object {
			stack[n-1].wantKey = true
		} else {
			stack[n-1].index++
		}
	}
	return stack
}

// quoted shows a key from a module file in an error: Go-quoted, so a newline or control
// character is escaped rather than printed, and cut to untrusted.MaxName.
func quoted(key string) string {
	return untrusted.Text(strconv.Quote(key), untrusted.MaxName)
}

// foldKey maps every key that strings.EqualFold treats as equal to the same string, the way
// Go's decoder matches struct fields.
func foldKey(s string) string {
	return strings.Map(func(r rune) rune {
		least := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			least = min(least, f)
		}
		return least
	}, s)
}

// jsonPointer names the object inside parents, e.g. "/mcpServers/gh" (RFC 6901), safe to print.
func jsonPointer(parents []*jsonLevel) string {
	if len(parents) == 0 {
		return "the top-level object"
	}
	var b strings.Builder
	for _, l := range parents {
		b.WriteByte('/')
		if l.object {
			b.WriteString(strings.NewReplacer("~", "~0", "/", "~1").Replace(l.key))
		} else {
			b.WriteString(strconv.Itoa(l.index))
		}
	}
	return untrusted.Text(b.String(), 2*untrusted.MaxName)
}

// jsonValue decodes raw as a plain value, with whole numbers as int64 rather than float64, so a
// value an adapter keeps reads back the same from the YAML that Normalize writes.
func jsonValue(raw json.RawMessage) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return plainNumbers(v)
}

func plainNumbers(v any) (any, error) {
	var err error
	switch x := v.(type) {
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i, nil
		}
		f, err := x.Float64()
		if err != nil {
			return nil, fmt.Errorf("number %s is out of range", untrusted.Text(x.String(), untrusted.MaxName))
		}
		if f == 0 {
			return 0.0, nil // -0 would be written as -0, which YAML reads back as the integer 0
		}
		return f, nil
	case map[string]any:
		for k, e := range x {
			if x[k], err = plainNumbers(e); err != nil {
				return nil, err
			}
		}
	case []any:
		for i, e := range x {
			if x[i], err = plainNumbers(e); err != nil {
				return nil, err
			}
		}
	}
	return v, nil
}

// typeErr words a value of the wrong JSON type without Go type names, which mean nothing to
// the author of the file.
func typeErr(shown string, e *json.UnmarshalTypeError) error {
	kind := "value"
	switch e.Type.Kind() {
	case reflect.Map, reflect.Struct:
		kind = "object"
	case reflect.Slice, reflect.Array:
		kind = "array"
	case reflect.String:
		kind = "string"
	case reflect.Bool:
		kind = "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
		kind = "number"
	}
	if e.Field == "" {
		return fmt.Errorf("%s must be a JSON %s", shown, kind)
	}
	return fmt.Errorf("%s: %s must be a JSON %s", shown, untrusted.Text(e.Field, 2*untrusted.MaxName), kind)
}
