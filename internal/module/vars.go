package module

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"

	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Vars (Plan 7 §4): a module declares template vars with their defaults in lola.yml vars:, and a
// manifest entry may override them. A var is a string, true or false, a number (an int64 or a
// finite float64, Plan 7 §5.7.4) or a list of strings; both places are untrusted input, read from
// the YAML node by tag under the same caps.
const (
	maxVars      = 64       // vars a module declares or an entry sets
	maxVarString = 4096     // bytes of a string or a list item
	maxVarItems  = 64       // strings in a list
	maxVarsBytes = 64 << 10 // bytes of names and strings together
)

var varNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// VarError is a vars: mapping that breaks a rule: Line is the line of the node at fault, and
// Reason names it ("vars.Lang: a var name is …") without the file, which the caller knows.
type VarError struct {
	Line   int
	Reason string
}

func (e *VarError) Error() string { return fmt.Sprintf("%d: %s", e.Line, e.Reason) }

// ParseVars reads the vars: mapping n, which at names in messages ("vars",
// "modules[2].vars"), into var name → value: a string, a bool, an int64, a float64 or a
// []string. A nil n, a null and an empty mapping are no vars (nil).
func ParseVars(n *yaml.Node, at string) (map[string]any, error) {
	if n == nil || n.Kind == yaml.ScalarNode && n.Tag == "!!null" || n.Kind == yaml.MappingNode && len(n.Content) == 0 {
		return nil, nil
	}
	refuse := func(n *yaml.Node, at, reason string) error {
		return &VarError{Line: n.Line, Reason: untrusted.Text(at, untrusted.MaxName) + ": " + reason}
	}
	if n.Kind == yaml.AliasNode {
		return nil, refuse(n, at, aliasReason)
	}
	if n.Kind != yaml.MappingNode {
		return nil, refuse(n, at, "vars map each var name to its value")
	}
	if count := len(n.Content) / 2; count > maxVars {
		return nil, refuse(n, at, fmt.Sprintf("%d vars; a module declares at most %d", count, maxVars))
	}
	out := make(map[string]any, len(n.Content)/2)
	total := 0
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		name := at + "." + k.Value
		switch {
		case k.Kind == yaml.AliasNode:
			return nil, refuse(k, at, aliasReason)
		case k.Kind != yaml.ScalarNode: // a list or map as a key has no name to show
			return nil, refuse(k, at, varNameReason)
		case !varNameRE.MatchString(k.Value):
			return nil, refuse(k, name, varNameReason)
		}
		if _, dup := out[k.Value]; dup {
			return nil, refuse(k, name, "the var is given twice")
		}
		total += len(k.Value)
		switch {
		case v.Kind == yaml.SequenceNode:
			if len(v.Content) > maxVarItems {
				return nil, refuse(v, name, fmt.Sprintf("%d items; a list holds at most %d", len(v.Content), maxVarItems))
			}
			list := make([]string, 0, len(v.Content))
			for j, item := range v.Content {
				s, reason := varString(item)
				if reason != "" {
					return nil, refuse(item, name+"["+strconv.Itoa(j)+"]", reason)
				}
				list, total = append(list, s), total+len(s)
			}
			out[k.Value] = list
		case v.Kind == yaml.ScalarNode && v.Tag == "!!bool":
			var b bool
			if err := v.Decode(&b); err != nil {
				return nil, refuse(v, name, varShapeReason)
			}
			out[k.Value] = b
		case v.Kind == yaml.ScalarNode && v.Tag == "!!int":
			i, reason := yamlInt(v)
			if reason != "" {
				return nil, refuse(v, name, reason)
			}
			out[k.Value] = i
		case v.Kind == yaml.ScalarNode && v.Tag == "!!float":
			f, reason := yamlFloat(v)
			if reason != "" {
				return nil, refuse(v, name, reason)
			}
			out[k.Value] = f
		case v.Kind == yaml.ScalarNode && v.Tag == "!!str" && outOfRange(v):
			return nil, refuse(v, name, nonFiniteReason)
		default:
			s, reason := varString(v)
			if reason != "" {
				return nil, refuse(v, name, reason)
			}
			out[k.Value], total = s, total+len(s)
		}
	}
	if total > maxVarsBytes {
		return nil, refuse(n, at, fmt.Sprintf("%d KiB in total; at most %d KiB", (total+1<<10-1)>>10, maxVarsBytes>>10))
	}
	return out, nil
}

// The reasons a var name, and an alias where a var or its name stands, refuse.
const (
	varNameReason  = "a var name is a-z, 0-9 and _, starts with a letter, at most 64 characters"
	aliasReason    = "aliases are not supported"
	varShapeReason = "a var is a string, true or false, a number, or a list of strings"
)

// varString reads a string var or list item from n, or says why it is not one: a number is a
// var of its own, never a list item.
func varString(n *yaml.Node) (string, string) {
	switch {
	case n.Kind == yaml.AliasNode:
		return "", aliasReason
	case n.Kind != yaml.ScalarNode:
		return "", varShapeReason
	case n.Tag == "!!int" || n.Tag == "!!float" || n.Tag == "!!str" && outOfRange(n):
		return "", "numbers are not supported; quote it to make it a string: " + strconv.Quote(untrusted.Text(n.Value, untrusted.MaxName))
	case n.Tag == "!!timestamp":
		return "", "dates are not supported; quote it to make it a string: " + strconv.Quote(untrusted.Text(n.Value, untrusted.MaxName))
	case n.Tag != "!!str":
		return "", varShapeReason
	}
	return n.Value, textReason(n.Value)
}

// textReason says why s cannot be a var's string or list item, or "".
func textReason(s string) string {
	switch {
	case len(s) > maxVarString:
		return fmt.Sprintf("longer than %d bytes", maxVarString)
	case !utf8.ValidString(s):
		return "is not UTF-8"
	case strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) && r != '\t' && r != '\n' }):
		return "holds a control character"
	}
	return ""
}

// DecodedVars reads vars that encoding/json decoded from a file lola wrote but anyone may edit
// (lock.json's rendered_with) with UseNumber: by the rules ParseVars applies, each list as a
// []string, each number as DecodedNumber reads it. ok is false, and vars nil, when they break one.
func DecodedVars(raw map[string]any) (vars map[string]any, ok bool) {
	if len(raw) > maxVars {
		return nil, false
	}
	vars = make(map[string]any, len(raw))
	total := 0
	for k, v := range raw {
		if !varNameRE.MatchString(k) {
			return nil, false
		}
		total += len(k)
		switch x := v.(type) {
		case bool:
			vars[k] = x
		case json.Number:
			n, ok := DecodedNumber(x)
			if !ok {
				return nil, false
			}
			vars[k] = n
		case string:
			if textReason(x) != "" {
				return nil, false
			}
			vars[k], total = x, total+len(x)
		case []any:
			if len(x) > maxVarItems {
				return nil, false
			}
			list := make([]string, 0, len(x))
			for _, item := range x {
				s, isText := item.(string)
				if !isText || textReason(s) != "" {
					return nil, false
				}
				list, total = append(list, s), total+len(s)
			}
			vars[k] = list
		default:
			return nil, false
		}
	}
	if total > maxVarsBytes {
		return nil, false
	}
	return vars, true
}

// VarText is a var's value as lola shows it, through untrusted.Text: a string quoted, true or
// false, an integer in decimal, a float in its shortest decimal form with a point and without an
// exponent, a list in brackets.
func VarText(v any) string {
	quote := func(s string) string { return strconv.Quote(untrusted.Text(s, untrusted.MaxName)) }
	switch x := v.(type) {
	case string:
		return quote(x)
	case bool:
		return strconv.FormatBool(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64: // with a point, so a float reads apart from an integer: 2.0, not 2
		f := strconv.FormatFloat(x, 'f', -1, 64)
		if !strings.Contains(f, ".") {
			f += ".0"
		}
		return f
	case []string:
		items := make([]string, len(x))
		for i, s := range x {
			items[i] = quote(s)
		}
		return "[" + strings.Join(items, ", ") + "]"
	case []any: // as a trust record decodes a list
		items := make([]string, len(x))
		for i, s := range x {
			items[i] = VarText(s)
		}
		return "[" + strings.Join(items, ", ") + "]"
	}
	return untrusted.Text(fmt.Sprint(v), untrusted.MaxName)
}
