package templating

import (
	"fmt"
	"maps"
	"slices"

	"github.com/osteele/liquid/tags"
	"github.com/osteele/liquid/values"
)

// codeValueType refuses a value of a kind lola does not bind or filter (Plan 7 §5.3).
const codeValueType = "template-value-type"

// bindings is the variables a template sees, built fresh for every render from d alone, so no
// struct or method is reachable. Every map is a tags.IterationKeyedMap, which a for loop walks
// in sorted key order, so one Data always renders the same bytes. vars stays strict: its names
// are checked before rendering; attrs is lenient (lenientValue).
func bindings(file string, d Data) (map[string]any, error) {
	profiles := make([]any, len(d.Profiles))
	for i, p := range d.Profiles {
		profiles[i] = p
	}
	vars := tags.IterationKeyedMap{}
	for _, k := range slices.Sorted(maps.Keys(d.Vars)) { // sorted: the first bad var is the one named
		bv, err := bindVar(file, "vars."+k, d.Vars[k])
		if err != nil {
			return nil, err
		}
		vars[k] = bv
	}
	attrs, err := bindAttr(file, "attrs", d.Attrs)
	if err != nil {
		return nil, err
	}
	return map[string]any{"target": d.Target, "scope": d.Scope, "profiles": profiles, "vars": vars, "attrs": lenient(values.ValueOf(attrs))}, nil
}

// lenientValue is a value of attrs as a template reads it (Plan 7 §5.7.3): a key or index
// it lacks, at any depth below attrs, reads as absent, never as liquid's undefined, which strict
// variables refuse. liquid's values.ValueOf returns a values.Value as it is, so a template reaches
// it only through these methods; Interface hands for, filters and json the plain data, so an
// alias or a loop variable of it reads strictly again. Every other method is the plain value's.
type lenientValue struct{ values.Value }

func (l *lenientValue) IndexValue(k values.Value) values.Value { return lenient(l.Value.IndexValue(k)) }
func (l *lenientValue) PropertyValue(k values.Value) values.Value {
	return lenient(l.Value.PropertyValue(k))
}

// lenient is v read from attrs: absent for liquid's undefined, else v wrapped so a key or index it
// lacks reads as absent too: a map's or list's missing key, and any property of a scalar but the
// built-ins liquid gives it (a string's size), as §5.7.3 reads "at any depth".
func lenient(v values.Value) values.Value {
	if values.IsUndefined(v) {
		return absent{}
	}
	return &lenientValue{v}
}

// absent is a key attrs lacks: nil when printed or passed on, false in a condition, equal only
// to nil, containing nothing, and absent again at any key below it.
type absent struct{}

func (absent) Interface() any                          { return nil }
func (absent) Int() int                                { return 0 }
func (absent) Equal(o values.Value) bool               { return o.Interface() == nil }
func (absent) Less(values.Value) bool                  { return false }
func (absent) Contains(values.Value) bool              { return false }
func (absent) IndexValue(values.Value) values.Value    { return absent{} }
func (absent) PropertyValue(values.Value) values.Value { return absent{} }
func (absent) Test() bool                              { return false }

// bindVar returns a var as a template sees it: a string, a bool, a number, or a list of strings.
func bindVar(file, path string, v any) (any, error) {
	switch v := v.(type) {
	case string, bool, int64, float64:
		return v, nil
	case []string:
		out := make([]any, len(v))
		for i, s := range v {
			out[i] = s
		}
		return out, nil
	}
	return nil, unsupported(file, path, v)
}

// bindAttr returns an attribute value as a template sees it, maps at every depth bound as
// tags.IterationKeyedMap.
func bindAttr(file, path string, v any) (any, error) {
	switch v := v.(type) {
	case string, bool, int64, float64, nil:
		return v, nil
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			be, err := bindAttr(file, fmt.Sprintf("%s[%d]", path, i), e)
			if err != nil {
				return nil, err
			}
			out[i] = be
		}
		return out, nil
	case map[string]any:
		out := make(tags.IterationKeyedMap, len(v))
		for _, k := range slices.Sorted(maps.Keys(v)) {
			be, err := bindAttr(file, path+"."+k, v[k])
			if err != nil {
				return nil, err
			}
			out[k] = be
		}
		return out, nil
	}
	return nil, unsupported(file, path, v)
}

// unsupported is the refusal of a bound value of a kind templates never see.
func unsupported(file, path string, v any) error {
	return newError(codeValueType, file, 0, "%s holds a value of an unsupported kind (%T)", clean(path, maxToken), v)
}
