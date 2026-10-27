package templating

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/osteele/liquid/tags"
	"github.com/osteele/liquid/values"
)

// codeValueTooLarge refuses a filter input, argument or result over a cap (Plan 7 §5.3).
const codeValueTooLarge = "template-value-too-large"

// filterRefusal is a filter's refusal. Liquid wraps it with the line of the filter's markup,
// and fromLiquid turns it into an *Error there. Author text in reason is already cleaned.
type filterRefusal struct{ code, reason string }

func (r *filterRefusal) Error() string { return r.reason }

// filterSet holds what lola's filters check against: the render's deadline and its caps.
type filterSet struct {
	ctx  context.Context
	file string
	lim  Limits
}

// filterFn is a filter's work, after filterSet checked its input and arguments: args holds the
// arguments given, so a filter tells a missing one from nil, as liquid's standard filters do.
type filterFn func(in any, args []any) (any, error)

// allowlist returns lola's 41 filters. Each behaves as liquid v1.9.2's standard filter of the
// same name for every input it accepts, and refuses an input, argument or result over the caps
// before it builds what would pass them (Plan 7 §5.3).
func allowlist(ctx context.Context, file string, lim Limits) map[string]any {
	f := &filterSet{ctx: ctx, file: file, lim: lim}
	m := map[string]any{"default": f.defaultFilter}
	for name, def := range map[string]struct {
		maxArgs int
		fn      filterFn
	}{
		"append":         {1, f.appendFilter},
		"prepend":        {1, f.prependFilter},
		"replace":        {2, f.replace},
		"replace_first":  {2, f.replaceFirst},
		"replace_last":   {2, f.replaceLast},
		"remove":         {1, onStringArg("remove", func(s, old string) string { return strings.ReplaceAll(s, old, "") })},
		"remove_first":   {1, onStringArg("remove_first", func(s, old string) string { return strings.Replace(s, old, "", 1) })},
		"remove_last":    {1, onStringArg("remove_last", removeLast)},
		"strip":          {0, onString(func(s string, _ []any) string { return strings.TrimSpace(s) })},
		"lstrip":         {0, onString(func(s string, _ []any) string { return strings.TrimLeftFunc(s, unicode.IsSpace) })},
		"rstrip":         {0, onString(func(s string, _ []any) string { return strings.TrimRightFunc(s, unicode.IsSpace) })},
		"strip_newlines": {0, onString(func(s string, _ []any) string { return strings.ReplaceAll(s, "\n", "") })},
		"upcase":         {1, onString(func(s string, _ []any) string { return strings.ToUpper(s) })},
		"downcase":       {1, onString(func(s string, _ []any) string { return strings.ToLower(s) })},
		"capitalize":     {1, onString(capitalize)},
		"truncate":       {2, f.truncate},
		"truncatewords":  {2, f.truncateWords},
		"slice":          {2, f.slice},
		"split":          {1, f.split},
		"join":           {1, f.join},
		"concat":         {1, f.concat},
		"map":            {1, f.mapFilter},
		"where":          {2, f.where},
		"compact":        {0, f.compact},
		"uniq":           {0, f.uniq},
		"first":          {0, f.first},
		"last":           {0, f.last},
		"size":           {0, func(in any, _ []any) (any, error) { return values.Length(in), nil }},
		"reverse":        {0, f.reverse},
		"sort":           {1, f.sortFilter},
		"sort_natural":   {1, f.sortNatural},
		"json":           {0, f.json},
		"plus":           {1, arith("plus", addInt, func(a, b float64) float64 { return a + b })},
		"minus":          {1, arith("minus", subInt, func(a, b float64) float64 { return a - b })},
		"times":          {1, arith("times", mulInt, func(a, b float64) float64 { return a * b })},
		"at_least":       {1, arith("at_least", func(a, b int64) (int64, bool) { return max(a, b), true }, atLeast)},
		"at_most":        {1, arith("at_most", func(a, b int64) (int64, bool) { return min(a, b), true }, atMost)},
		"divided_by":     {1, dividedBy},
		"modulo":         {1, modulo},
		"abs":            {0, abs},
	} {
		m[name] = f.wrap(name, def.maxArgs, def.fn)
	}
	return m
}

// wrap registers fn as the filter name, taking at most maxArgs arguments and no keyword
// arguments: it refuses past the deadline, an input or argument over the caps, and a result
// over them.
func (f *filterSet) wrap(name string, maxArgs int, fn filterFn) func(any, ...any) (any, error) {
	return func(in any, args ...any) (any, error) {
		if kw, _ := keywords(args); kw != nil {
			return nil, &filterRefusal{codeSyntax, fmt.Sprintf("%s takes no keyword arguments; it got %s", name, keywordNames(kw, ""))}
		}
		if len(args) > maxArgs {
			return nil, &filterRefusal{codeSyntax, fmt.Sprintf("%s takes %s; it got %d", name, argCount(maxArgs), len(args))}
		}
		if err := f.check(name, append([]any{in}, args...)...); err != nil {
			return nil, err
		}
		out, err := fn(in, args)
		if err != nil {
			return nil, err
		}
		if l, ok := out.([]any); ok && len(l) > f.lim.Items {
			return nil, f.tooManyItems(name, len(l))
		}
		if n, ok := size(out); !ok {
			return nil, f.unsupported(name)
		} else if n > f.lim.Value {
			return nil, f.tooLarge(name, n)
		}
		return out, nil
	}
}

// keywords splits liquid's keyword arguments, which it passes as a trailing map[string]any,
// from the positional ones. lola binds every map as a tags.IterationKeyedMap, which stays
// positional; liquid's forloop, a plain map, passed as an argument reads as keywords.
func keywords(args []any) (map[string]any, []any) {
	if n := len(args); n > 0 {
		if kw, ok := args[n-1].(map[string]any); ok {
			return kw, args[:n-1]
		}
	}
	return nil, args
}

// keywordNames lists kw's names but skip, sorted and cleaned, for a refusal.
func keywordNames(kw map[string]any, skip string) string {
	var names []string
	for k := range kw {
		if k != skip {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	return clean(strings.Join(names, ", "), maxToken)
}

// argCount says how many arguments a filter takes, at most.
func argCount(n int) string {
	switch n {
	case 0:
		return "no arguments"
	case 1:
		return "at most one argument"
	}
	return fmt.Sprintf("at most %d arguments", n)
}

// check refuses past the deadline, and a value that is over the Value cap or of a kind lola
// templates never hold.
func (f *filterSet) check(name string, vs ...any) error {
	if f.ctx.Err() != nil {
		return stopped(f.ctx, f.file, f.lim.Timeout)
	}
	for _, v := range vs {
		n, ok := size(v)
		if !ok {
			return f.unsupported(name)
		}
		if n > f.lim.Value {
			return &filterRefusal{codeValueTooLarge, fmt.Sprintf("%s got a %s value; a value in a lola template is at most %s", name, sizeText(n), sizeText(f.lim.Value))}
		}
	}
	return nil
}

func (f *filterSet) tooLarge(name string, n int) error {
	return &filterRefusal{codeValueTooLarge, fmt.Sprintf("%s would make a %s value; a value in a lola template is at most %s", name, sizeText(n), sizeText(f.lim.Value))}
}

func (f *filterSet) tooManyItems(name string, n int) error {
	return &filterRefusal{codeValueTooLarge, fmt.Sprintf("%s would make a list of %d items; a list in a lola template holds at most %d", name, n, f.lim.Items)}
}

func (f *filterSet) unsupported(name string) error {
	return &filterRefusal{codeValueType, "a value of an unsupported kind reached " + name}
}

// size is a value's size as Plan 7 §5.3 defines it: a string's bytes; 8 for a number, a
// boolean or nil; 16 plus its elements' sizes for a list; 16 plus its keys' bytes and its
// values' sizes for a map. It reports false for any other kind.
func size(v any) (int, bool) {
	switch v := v.(type) {
	case nil, bool, int, int64, float64:
		return 8, true
	case string:
		return len(v), true
	case []any:
		n := 16
		for _, e := range v {
			s, ok := size(e)
			if !ok {
				return 0, false
			}
			n += s
		}
		return n, true
	case tags.IterationKeyedMap:
		return mapSize(v)
	case map[string]any:
		return mapSize(v)
	}
	return 0, false
}

func mapSize(m map[string]any) (int, bool) {
	n := 16
	for k, e := range m {
		s, ok := size(e)
		if !ok {
			return 0, false
		}
		n += len(k) + s
	}
	return n, true
}

// The conversions below give a filter its input and arguments as liquid's standard filters get
// theirs (values.Call): nil is the zero value, a string argument is any value printed, an
// optional argument left out takes its default.

// toString converts v as liquid converts a filter's string parameter.
func toString(v any) string {
	if v == nil {
		return ""
	}
	s, _ := values.Convert(v, reflect.TypeFor[string]()) // never fails: liquid prints any value as a string
	return s.(string)
}

// str is argument i of the filter name as a string parameter: "" when left out or nil.
func str(name string, args []any, i int) (string, error) { return optString(name, args, i, "") }

// optString is argument i of the filter name as an optional string parameter: def when left
// out, "" when nil. It refuses a list or a map, which liquid would print in Go's syntax.
func optString(name string, args []any, i int, def string) (string, error) {
	if i >= len(args) {
		return def, nil
	}
	switch args[i].(type) {
	case []any, tags.IterationKeyedMap: // wrap refused a plain map as keyword arguments
		return "", &filterRefusal{codeValueType, fmt.Sprintf("%s takes a string argument; argument %d is %s", name, i+1, kind(args[i]))}
	}
	return toString(args[i]), nil
}

// optInt is argument i as an optional integer parameter: def when left out, 0 when nil.
func optInt(name string, args []any, i int, def int) (int, error) {
	if i >= len(args) {
		return def, nil
	}
	return toInt(name, args[i])
}

// toInt converts v as liquid converts a filter's integer parameter.
func toInt(name string, v any) (int, error) {
	if v == nil {
		return 0, nil
	}
	n, err := values.Convert(v, reflect.TypeFor[int]())
	if err != nil {
		return 0, &filterRefusal{codeValueType, fmt.Sprintf("%s takes a whole number; this is %s", name, kind(v))}
	}
	return n.(int), nil
}

// toList converts v as liquid converts a filter's list parameter, but refuses a map, whose
// values liquid would list in random order.
func toList(name string, v any) ([]any, error) {
	switch v := v.(type) {
	case nil:
		return nil, nil
	case []any:
		return v, nil
	}
	return nil, &filterRefusal{codeValueType, fmt.Sprintf("%s takes a list; this is %s", name, kind(v))}
}

// kind names v's kind in a refusal.
func kind(v any) string {
	switch v.(type) {
	case string:
		return "a string"
	case bool:
		return "true or false"
	case int, int64, float64:
		return "a number"
	case tags.IterationKeyedMap, map[string]any:
		return "a map"
	case []any:
		return "a list"
	case nil:
		return "nil"
	}
	return "a value of an unsupported kind"
}

// onString is a filter of its input as a string and its arguments, whose result wrap checks.
func onString(fn func(s string, args []any) string) filterFn {
	return func(in any, args []any) (any, error) { return fn(toString(in), args), nil }
}

// onStringArg is the filter name of its input and its one argument, both as strings, whose
// result wrap checks.
func onStringArg(name string, fn func(s, arg string) string) filterFn {
	return func(in any, args []any) (any, error) {
		arg, err := str(name, args, 0)
		if err != nil {
			return nil, err
		}
		return fn(toString(in), arg), nil
	}
}

func removeLast(s, old string) string {
	i := strings.LastIndex(s, old)
	if i < 0 {
		return s
	}
	return s[:i] + s[i+len(old):]
}

func (f *filterSet) appendFilter(in any, args []any) (any, error) {
	s := toString(in)
	suffix, err := str("append", args, 0)
	if err != nil {
		return nil, err
	}
	if n := len(s) + len(suffix); n > f.lim.Value {
		return nil, f.tooLarge("append", n)
	}
	return s + suffix, nil
}

func (f *filterSet) prependFilter(in any, args []any) (any, error) {
	s := toString(in)
	prefix, err := str("prepend", args, 0)
	if err != nil {
		return nil, err
	}
	if n := len(s) + len(prefix); n > f.lim.Value {
		return nil, f.tooLarge("prepend", n)
	}
	return prefix + s, nil
}

func (f *filterSet) replace(in any, args []any) (any, error) {
	s := toString(in)
	old, err := str("replace", args, 0)
	if err != nil {
		return nil, err
	}
	repl, err := str("replace", args, 1)
	if err != nil {
		return nil, err
	}
	n := int64(len(s)) + int64(strings.Count(s, old))*(int64(len(repl))-int64(len(old)))
	if n > int64(f.lim.Value) {
		return nil, f.tooLarge("replace", int(n))
	}
	return strings.ReplaceAll(s, old, repl), nil
}

func (f *filterSet) replaceFirst(in any, args []any) (any, error) {
	s := toString(in)
	old, err := str("replace_first", args, 0)
	if err != nil {
		return nil, err
	}
	repl, err := str("replace_first", args, 1)
	if err != nil {
		return nil, err
	}
	if n := len(s) + max(0, len(repl)-len(old)); n > f.lim.Value {
		return nil, f.tooLarge("replace_first", n)
	}
	return strings.Replace(s, old, repl, 1), nil
}

func (f *filterSet) replaceLast(in any, args []any) (any, error) {
	s := toString(in)
	old, err := str("replace_last", args, 0)
	if err != nil {
		return nil, err
	}
	repl, err := str("replace_last", args, 1)
	if err != nil {
		return nil, err
	}
	if n := len(s) + max(0, len(repl)-len(old)); n > f.lim.Value {
		return nil, f.tooLarge("replace_last", n)
	}
	i := strings.LastIndex(s, old)
	if i < 0 {
		return s, nil
	}
	return s[:i] + repl + s[i+len(old):], nil
}

// capitalize upper-cases the first rune and lower-cases the rest. As upcase and downcase, it
// grows a rune to at most three bytes, so wrap's check of the result bounds what it built to
// three times the Value cap.
func capitalize(s string, _ []any) string {
	runes := []rune(strings.ToLower(s))
	if len(runes) == 0 {
		return s
	}
	return strings.ToUpper(string(runes[0])) + string(runes[1:])
}

func (f *filterSet) truncate(in any, args []any) (any, error) {
	s := toString(in)
	n, err := optInt("truncate", args, 0, 50)
	if err != nil {
		return nil, err
	}
	el, err := optString("truncate", args, 1, "...")
	if err != nil {
		return nil, err
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s, nil
	}
	if size := len(s) + len(el); size > f.lim.Value {
		return nil, f.tooLarge("truncate", size)
	}
	prefix := min(max(n-utf8.RuneCountInString(el), 0), len(runes))
	return string(runes[:prefix]) + el, nil
}

func (f *filterSet) truncateWords(in any, args []any) (any, error) {
	s := toString(in)
	n, err := optInt("truncatewords", args, 0, 15)
	if err != nil {
		return nil, err
	}
	el, err := optString("truncatewords", args, 1, "...")
	if err != nil {
		return nil, err
	}
	words := strings.Fields(s)
	if len(words) <= max(n, 1) {
		return s, nil
	}
	if size := len(s) + len(el); size > f.lim.Value {
		return nil, f.tooLarge("truncatewords", size)
	}
	return strings.Join(words[:max(n, 1)], " ") + el, nil
}

// slice takes length elements (runes of a string) from start, a negative start counting from
// the end. Start and length are clamped, so no value of either can panic.
func (f *filterSet) slice(in any, args []any) (any, error) {
	start, err := optInt("slice", args, 0, 0)
	if err != nil {
		return nil, err
	}
	length, err := optInt("slice", args, 1, 1)
	if err != nil {
		return nil, err
	}
	bounds := func(n int) (int, int) {
		from := start
		if from < 0 {
			from = max(n+from, 0) // n+from cannot overflow: n ≥ 0 > from
		}
		from = min(from, n)
		return from, from + max(min(length, n-from), 0)
	}
	if s, ok := in.(string); ok {
		runes := []rune(s)
		from, to := bounds(len(runes))
		return string(runes[from:to]), nil
	}
	l, ok := in.([]any)
	if !ok {
		if _, isMap := in.(tags.IterationKeyedMap); isMap {
			return nil, &filterRefusal{codeValueType, "slice takes a list or a string; this is a map"}
		}
		if _, isMap := in.(map[string]any); isMap {
			return nil, &filterRefusal{codeValueType, "slice takes a list or a string; this is a map"}
		}
		return nil, nil
	}
	from, to := bounds(len(l))
	return l[from:to], nil
}

func (f *filterSet) split(in any, args []any) (any, error) {
	s := toString(in)
	sep, err := str("split", args, 0)
	if err != nil {
		return nil, err
	}
	var n int
	switch sep {
	case "":
		n = utf8.RuneCountInString(s)
	case " ": // pieces between runs of [[:space:]]
		n = 1
		for i := 0; i < len(s); i++ {
			if isPOSIXSpace(rune(s[i])) && (i == 0 || !isPOSIXSpace(rune(s[i-1]))) {
				n++
			}
		}
	default:
		n = strings.Count(s, sep) + 1
	}
	if n > f.lim.Items {
		return nil, f.tooManyItems("split", n)
	}
	var pieces []string
	if sep == " " {
		pieces = strings.FieldsFunc(s, isPOSIXSpace)
		if len(s) > 0 && isPOSIXSpace(rune(s[0])) {
			pieces = append([]string{""}, pieces...) // liquid keeps the empty piece before leading space
		}
	} else {
		pieces = strings.Split(s, sep)
	}
	for len(pieces) > 0 && pieces[len(pieces)-1] == "" {
		pieces = pieces[:len(pieces)-1]
	}
	out := make([]any, len(pieces))
	for i, p := range pieces {
		out[i] = p
	}
	return out, nil
}

// isPOSIXSpace reports whether r is in the regular expression class [[:space:]].
func isPOSIXSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\v' || r == '\f' || r == '\r'
}

func (f *filterSet) join(in any, args []any) (any, error) {
	l, err := toList("join", in)
	if err != nil {
		return nil, err
	}
	sep, err := optString("join", args, 0, " ")
	if err != nil {
		return nil, err
	}
	parts := make([]string, 0, len(l))
	n := 0
	for i, v := range l {
		switch v.(type) {
		case nil:
			continue
		case string, bool, int, int64, float64:
			parts = append(parts, fmt.Sprint(v))
			n += len(parts[len(parts)-1])
		default:
			return nil, &filterRefusal{codeValueType, fmt.Sprintf("join takes a list of strings, numbers or booleans; item %d is %s", i+1, kind(v))}
		}
	}
	if len(parts) > 1 {
		n += (len(parts) - 1) * len(sep)
	}
	if n > f.lim.Value {
		return nil, f.tooLarge("join", n)
	}
	return strings.Join(parts, sep), nil
}

func (f *filterSet) concat(in any, args []any) (any, error) {
	a, err := toList("concat", in)
	if err != nil {
		return nil, err
	}
	var b []any
	if len(args) > 0 {
		if b, err = toList("concat", args[0]); err != nil {
			return nil, err
		}
	}
	if len(a)+len(b) > f.lim.Items {
		return nil, f.tooManyItems("concat", len(a)+len(b))
	}
	sa, _ := size(a) // check already sized both
	sb, _ := size(b)
	if sa+sb > f.lim.Value {
		return nil, f.tooLarge("concat", sa+sb)
	}
	return append(append(make([]any, 0, len(a)+len(b)), a...), b...), nil
}

func (f *filterSet) mapFilter(in any, args []any) (any, error) {
	l, err := toList("map", in)
	if err != nil {
		return nil, err
	}
	k, err := str("map", args, 0)
	if err != nil {
		return nil, err
	}
	key := values.ValueOf(k)
	props := make([]any, len(l))
	n := 16
	for i, obj := range l {
		props[i] = values.ValueOf(obj).PropertyValue(key).Interface()
		s, ok := size(props[i])
		if !ok {
			return nil, f.unsupported("map")
		}
		n += s
	}
	if n > f.lim.Value {
		return nil, f.tooLarge("map", n)
	}
	if len(l) == 0 {
		return []any(nil), nil
	}
	return props, nil
}

// omitted stands for where's target value when the template gives none.
type omitted struct{}

func (f *filterSet) where(in any, args []any) (any, error) {
	l, err := toList("where", in)
	if err != nil {
		return nil, err
	}
	k, err := str("where", args, 0)
	if err != nil {
		return nil, err
	}
	key := values.ValueOf(k)
	var target any = omitted{}
	if len(args) > 1 {
		target = args[1]
	}
	_, truthy := target.(omitted) // with no target, where keeps the elements whose property is truthy
	var out []any
	for _, obj := range l {
		prop := values.ValueOf(obj).PropertyValue(key).Interface()
		if truthy && prop != nil && prop != false || !truthy && values.Equal(prop, target) {
			out = append(out, obj)
		}
	}
	return out, nil
}

func (f *filterSet) compact(in any, _ []any) (any, error) {
	l, err := toList("compact", in)
	if err != nil {
		return nil, err
	}
	var out []any
	for _, v := range l {
		if v != nil {
			out = append(out, v)
		}
	}
	return out, nil
}

func (f *filterSet) uniq(in any, _ []any) (any, error) {
	l, err := toList("uniq", in)
	if err != nil {
		return nil, err
	}
	var out []any
	seen := map[any]bool{}
	for _, v := range l {
		if err := f.check("uniq"); err != nil { // comparing lists is quadratic
			return nil, err
		}
		if v == nil || reflect.TypeOf(v).Comparable() {
			if !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
			continue
		}
		dup := false
		for _, o := range out {
			if o != nil && reflect.DeepEqual(v, o) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, v)
		}
	}
	return out, nil
}

func (f *filterSet) first(in any, _ []any) (any, error) {
	l, err := toList("first", in)
	if err != nil || len(l) == 0 {
		return nil, err
	}
	return l[0], nil
}

func (f *filterSet) last(in any, _ []any) (any, error) {
	l, err := toList("last", in)
	if err != nil || len(l) == 0 {
		return nil, err
	}
	return l[len(l)-1], nil
}

func (f *filterSet) reverse(in any, _ []any) (any, error) {
	l, err := toList("reverse", in)
	if err != nil {
		return nil, err
	}
	out := make([]any, len(l))
	for i, v := range l {
		out[len(out)-1-i] = v
	}
	return out, nil
}

func (f *filterSet) sortFilter(in any, args []any) (any, error) {
	l, err := toList("sort", in)
	if err != nil {
		return nil, err
	}
	out := append(make([]any, 0, len(l)), l...)
	if len(args) == 0 || args[0] == nil {
		values.Sort(out)
	} else {
		values.SortByProperty(out, fmt.Sprint(args[0]), true)
	}
	return out, nil
}

// sortNatural sorts strings ignoring case, or maps by the string at a key ignoring case; liquid
// panics on a list that mixes strings with other kinds or on a missing key, which lola refuses
// or sorts as "".
func (f *filterSet) sortNatural(in any, args []any) (any, error) {
	l, err := toList("sort_natural", in)
	if err != nil {
		return nil, err
	}
	out := append(make([]any, 0, len(l)), l...)
	switch {
	case len(out) == 0:
	case len(args) > 0 && args[0] != nil:
		key, ok := args[0].(string)
		if !ok {
			return nil, &filterRefusal{codeValueType, fmt.Sprintf("sort_natural takes a key that is a string; this is %s", kind(args[0]))}
		}
		sort.Sort(byKey{out, func(v any) string {
			var m map[string]any
			switch v := v.(type) {
			case tags.IterationKeyedMap:
				m = v
			case map[string]any:
				m = v
			}
			s, _ := m[key].(string)
			return strings.ToLower(s)
		}})
	default:
		if _, ok := out[0].(string); !ok && out[0] != nil {
			break // liquid sorts a list only when it starts with a string
		}
		for i, v := range out {
			if _, ok := v.(string); !ok {
				return nil, &filterRefusal{codeValueType, fmt.Sprintf("sort_natural sorts strings; item %d is %s", i+1, kind(v))}
			}
		}
		sort.Sort(byKey{out, func(v any) string { return strings.ToUpper(v.(string)) }})
	}
	return out, nil
}

// byKey sorts a list by a string key of each element, as liquid's sort_natural does.
type byKey struct {
	l   []any
	key func(any) string
}

func (s byKey) Len() int           { return len(s.l) }
func (s byKey) Swap(i, j int)      { s.l[i], s.l[j] = s.l[j], s.l[i] }
func (s byKey) Less(i, j int) bool { return s.key(s.l[i]) < s.key(s.l[j]) }

// json writes v as JSON, YAML's flow syntax, on one line: without HTML escaping, map keys
// sorted, and every control character escaped, DEL and C1 included, so the value stays one YAML
// value wherever it lands in frontmatter.
func (f *filterSet) json(in any, _ []any) (any, error) {
	if bound := jsonBound(in); bound > f.lim.Value {
		return nil, &filterRefusal{codeValueTooLarge, fmt.Sprintf("json could make a %s value; a value in a lola template is at most %s", sizeText(bound), sizeText(f.lim.Value))}
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(in); err != nil {
		return nil, &filterRefusal{codeValueType, "json cannot write this value"}
	}
	raw := bytes.TrimSuffix(b.Bytes(), []byte("\n"))
	var out strings.Builder
	for _, r := range string(raw) {
		if unicode.IsControl(r) {
			fmt.Fprintf(&out, `\u%04x`, r)
			continue
		}
		out.WriteRune(r)
	}
	return out.String(), nil
}

// jsonBound is the most bytes json can write for v, which check already sized: a string's
// every byte escaped to six, plus quotes; 24 for a number, a boolean or nil; a list's or a map's
// brackets, commas, and escaped quoted keys with their colons.
func jsonBound(v any) int {
	switch v := v.(type) {
	case string:
		return 6*len(v) + 2
	case []any:
		n := 2
		for _, e := range v {
			n += jsonBound(e) + 1
		}
		return n
	case tags.IterationKeyedMap:
		return jsonMapBound(v)
	case map[string]any:
		return jsonMapBound(v)
	}
	return 24
}

func jsonMapBound(m map[string]any) int {
	n := 2
	for k, e := range m {
		n += 6*len(k) + 4 + jsonBound(e)
	}
	return n
}

// defaultFilter is value, or its one argument when value is nil, false or empty; with
// allow_false: true, false is kept.
func (f *filterSet) defaultFilter(value any, args ...any) (any, error) {
	kw, args := keywords(args)
	if names := keywordNames(kw, "allow_false"); names != "" {
		return nil, &filterRefusal{codeSyntax, "default takes only the allow_false keyword argument; it got " + names}
	}
	if len(args) > 1 {
		return nil, &filterRefusal{codeSyntax, fmt.Sprintf("default takes %s; it got %d", argCount(1), len(args))}
	}
	var defaultValue any
	if len(args) == 1 {
		defaultValue = args[0]
	}
	if err := f.check("default", value, defaultValue); err != nil {
		return nil, err
	}
	allowFalse, _ := kw["allow_false"].(bool)
	switch {
	case value == nil:
		return defaultValue, nil
	case allowFalse:
		if r := reflect.ValueOf(value); (r.Kind() == reflect.Map || r.Kind() == reflect.Slice || r.Kind() == reflect.String) && r.Len() == 0 {
			return defaultValue, nil
		}
	case value == false || values.IsEmpty(value):
		return defaultValue, nil
	}
	return value, nil
}

// The math filters (Plan 7 §5.7.7) follow liquid v1.9.2's numeric semantics on integers and
// floats (filters/standard_filters.go): integers stay int64 and any float makes the result a
// float64. Where liquid would go silently wrong they refuse: an integer result that overflows
// int64, a float result that is not finite, and a division by zero.

// toNumber returns v as a math filter takes it: an integer as an int64, a float as a float64. It
// refuses any other value, a string included, which liquid would coerce.
func toNumber(name string, v any) (any, error) {
	switch v := v.(type) {
	case int:
		return int64(v), nil
	case int64, float64:
		return v, nil
	}
	return nil, &filterRefusal{codeValueType, fmt.Sprintf("%s takes numbers; it got %s", name, kind(v))}
}

// operands returns a math filter's input and its one argument as numbers, refusing an argument
// left out.
func operands(name string, in any, args []any) (any, any, error) {
	a, err := toNumber(name, in)
	if err != nil {
		return nil, nil, err
	}
	if len(args) == 0 {
		return nil, nil, &filterRefusal{codeSyntax, name + " is missing its argument, a number"}
	}
	b, err := toNumber(name, args[0])
	if err != nil {
		return nil, nil, err
	}
	return a, b, nil
}

// asFloat is a number toNumber returned as a float64.
func asFloat(v any) float64 {
	if i, ok := v.(int64); ok {
		return float64(i)
	}
	return v.(float64)
}

func overflows(name string) error {
	return &filterRefusal{codeValueTooLarge, name + " would overflow a 64-bit integer"}
}

// finite returns r, or refuses a result that is not a finite number.
func finite(name string, r float64) (any, error) {
	if math.IsInf(r, 0) || math.IsNaN(r) {
		return nil, &filterRefusal{codeValueTooLarge, name + " would not give a finite number"}
	}
	return r, nil
}

func byZero(name string) error {
	return &filterRefusal{codeSyntax, name + " 0: a number cannot be divided by zero"}
}

// arith is the math filter name of two numbers: intOp when both are integers, which reports
// false on overflow, else floatOp of both as floats.
func arith(name string, intOp func(a, b int64) (int64, bool), floatOp func(a, b float64) float64) filterFn {
	return func(in any, args []any) (any, error) {
		a, b, err := operands(name, in, args)
		if err != nil {
			return nil, err
		}
		ai, aInt := a.(int64)
		bi, bInt := b.(int64)
		if aInt && bInt {
			r, ok := intOp(ai, bi)
			if !ok {
				return nil, overflows(name)
			}
			return r, nil
		}
		return finite(name, floatOp(asFloat(a), asFloat(b)))
	}
}

// atLeast and atMost keep a unless b is strictly beyond it, liquid's < and > tie rule: -0.0 and
// 0.0 are equal, so a wins, where math.Max and math.Min order -0 below 0.
func atLeast(a, b float64) float64 {
	if a < b {
		return b
	}
	return a
}

func atMost(a, b float64) float64 {
	if a > b {
		return b
	}
	return a
}

func addInt(a, b int64) (int64, bool) {
	r := a + b
	return r, (b >= 0) == (r >= a)
}

func subInt(a, b int64) (int64, bool) {
	r := a - b
	return r, (b >= 0) == (r <= a)
}

func mulInt(a, b int64) (int64, bool) {
	switch {
	case a == 0 || b == 0:
		return 0, true
	case a == -1 || b == -1: // r/b == a misses -1 × MinInt64, which wraps to MinInt64
		return a * b, a != math.MinInt64 && b != math.MinInt64
	}
	r := a * b
	return r, r/b == a
}

// dividedBy divides as liquid does, but exactly: two integers divide as int64, truncating toward
// zero, where liquid converts the dividend to a float64 first and loses digits past 2^53; a float
// dividend with an integer divisor divides its integer part, truncating; a float divisor divides
// as floats.
func dividedBy(in any, args []any) (any, error) {
	a, b, err := operands("divided_by", in, args)
	if err != nil {
		return nil, err
	}
	q, isInt := b.(int64)
	switch {
	case !isInt && b.(float64) == 0, isInt && q == 0:
		return nil, byZero("divided_by")
	case !isInt:
		return finite("divided_by", asFloat(a)/b.(float64))
	}
	n, intDividend := a.(int64)
	if !intDividend {
		f := a.(float64)
		if f >= 1<<63 || f < -1<<63 { // int64 cannot hold its integer part
			return nil, overflows("divided_by")
		}
		n = int64(f)
	}
	if n == math.MinInt64 && q == -1 {
		return nil, overflows("divided_by")
	}
	return n / q, nil
}

// modulo is math.Mod of both numbers as floats, a float whose sign follows the dividend.
func modulo(in any, args []any) (any, error) {
	a, b, err := operands("modulo", in, args)
	if err != nil {
		return nil, err
	}
	if asFloat(b) == 0 {
		return nil, byZero("modulo")
	}
	return math.Mod(asFloat(a), asFloat(b)), nil
}

// abs is math.Abs of the number as a float.
func abs(in any, _ []any) (any, error) {
	a, err := toNumber("abs", in)
	if err != nil {
		return nil, err
	}
	return math.Abs(asFloat(a)), nil
}
