package templating_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/osteele/liquid"

	"github.com/trevor-vaughan/lola-go/internal/templating"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// row is one expression of the differential corpus, rendered as {{ expr }} by lola and by
// liquid's standard engine. differs is empty when lola must render what liquid renders, or
// refuse where liquid fails; else it is the code lola refuses with where liquid renders (a
// listed difference), "output" when both render but lola's output is want, or "renders" when
// liquid fails and lola renders want.
type row struct {
	expr    string
	differs string
	want    string
}

// corpus holds rows for every allowlisted filter: nil, empty and multibyte inputs, negative
// and zero arguments, mixed lists, and the inputs Plan 7 §12 spec 18 names. Its listed
// differences: capped inputs refuse, json drops HTML escaping and escapes DEL and C1, join
// refuses nested lists, a map where a list is expected refuses, since liquid would list the
// map's values in random order, a list or map argument to a string filter refuses, since liquid
// would print it in Go's syntax, keyword arguments refuse but default's allow_false, and the math
// filters refuse an integer overflow, a result that is not finite, modulo by zero and an input
// or argument that is not a number, which liquid would coerce, and divided_by divides two
// integers exactly, where liquid goes through a float64 and loses digits past 2^53.
var corpus = map[string][]row{
	"append": {{expr: "nil | append: 'x'"}, {expr: "'' | append: ''"}, {expr: "'é✓' | append: 'ü'"}, {expr: "'a' | append: nil"},
		{expr: "-1 | append: 0"}, {expr: "true | append: 3"}, {expr: "attrs.mixed | append: 'x'"}, {expr: "'a' | append"},
		{expr: "'a' | append: 'b', 'c'"}, {expr: "vars.big | append: vars.big", differs: "template-value-too-large"},
		{expr: "'a' | append: vars.tools", differs: "template-value-type"}},
	"prepend": {{expr: "nil | prepend: 'x'"}, {expr: "'' | prepend: ''"}, {expr: "'é✓' | prepend: 'ü'"}, {expr: "0 | prepend: -1"},
		{expr: "'a' | prepend"}, {expr: "vars.big | prepend: vars.big", differs: "template-value-too-large"},
		{expr: "'a' | prepend: attrs.pair", differs: "template-value-type"}},
	"replace": {{expr: "'aXbX' | replace: 'X', 'é'"}, {expr: "nil | replace: '', ''"}, {expr: "'' | replace: '', 'x'"},
		{expr: "'héllo' | replace: '', '-'"}, {expr: "'abc' | replace: 'b'"}, {expr: "'a1a' | replace: 1, 0"}, {expr: "'a-1' | replace: -1, 'x'"},
		{expr: "vars.big | replace: 'x', 'yy'", differs: "template-value-too-large"}},
	"replace_first": {{expr: "'aXbX' | replace_first: 'X', 'é'"}, {expr: "nil | replace_first: 'a', 'b'"}, {expr: "'' | replace_first: '', 'x'"},
		{expr: "'héllo' | replace_first: '', '-'"}, {expr: "'abc' | replace_first: 'b'"}},
	"replace_last": {{expr: "'aXbX' | replace_last: 'X', 'é'"}, {expr: "nil | replace_last: 'a', 'b'"}, {expr: "'' | replace_last: '', 'x'"},
		{expr: "'héllo' | replace_last: '', '-'"}, {expr: "'abc' | replace_last: 'z', 'y'"}},
	"remove":         {{expr: "'abcabc' | remove: 'b'"}, {expr: "'' | remove: ''"}, {expr: "nil | remove: 'x'"}, {expr: "'é✓é' | remove: 'é'"}, {expr: "'a0' | remove: 0"}},
	"remove_first":   {{expr: "'abcabc' | remove_first: 'b'"}, {expr: "'' | remove_first: ''"}, {expr: "nil | remove_first: 'x'"}, {expr: "'é✓é' | remove_first: 'é'"}},
	"remove_last":    {{expr: "'abcabc' | remove_last: 'b'"}, {expr: "'abc' | remove_last: ''"}, {expr: "nil | remove_last: 'x'"}, {expr: "'é✓é' | remove_last: 'é'"}},
	"strip":          {{expr: "attrs.text | strip"}, {expr: "nil | strip"}, {expr: "'' | strip"}, {expr: "'  ü  ' | strip"}, {expr: "-1 | strip"}},
	"lstrip":         {{expr: "attrs.text | lstrip"}, {expr: "nil | lstrip"}, {expr: "'' | lstrip"}, {expr: "'  ü  ' | lstrip"}},
	"rstrip":         {{expr: "attrs.text | rstrip"}, {expr: "nil | rstrip"}, {expr: "'' | rstrip"}, {expr: "'  ü  ' | rstrip"}},
	"strip_newlines": {{expr: "attrs.text | strip_newlines"}, {expr: "nil | strip_newlines"}, {expr: "'' | strip_newlines"}},
	"upcase":         {{expr: "'héllo wörld ß' | upcase"}, {expr: "nil | upcase"}, {expr: "'' | upcase"}, {expr: "3 | upcase"}, {expr: "'x' | upcase: 'ignored'"}},
	"downcase":       {{expr: "'HÉLLO WÖRLD' | downcase"}, {expr: "nil | downcase"}, {expr: "'' | downcase"}, {expr: "true | downcase"}},
	"capitalize":     {{expr: "'héllo WÖRLD' | capitalize"}, {expr: "'ǆemal' | capitalize"}, {expr: "nil | capitalize"}, {expr: "'' | capitalize"}, {expr: "'élan' | capitalize"}},
	"truncate": {{expr: "'héllo wörld ✓' | truncate: 5"}, {expr: "'héllo' | truncate: 0"}, {expr: "'héllo' | truncate: -1"},
		{expr: "'héllo wörld' | truncate: 3, '…'"}, {expr: "nil | truncate: 2"}, {expr: "'' | truncate: 0"}, {expr: "'abc' | truncate: nil"},
		{expr: "'abcdef' | truncate: 2, nil"}, {expr: "'abcdef' | truncate: '2'"}, {expr: "'abc' | truncate: 'x'"}, {expr: "attrs.long | truncate"},
		{expr: "'abc' | truncate: 2, foo: 1", differs: "template-syntax"}},
	"truncatewords": {{expr: "'a b  c d' | truncatewords: 2"}, {expr: "'a b c' | truncatewords: 0"}, {expr: "'a b c' | truncatewords: -1"},
		{expr: "'é ü ✓' | truncatewords: 1, '…'"}, {expr: "nil | truncatewords"}, {expr: "'' | truncatewords: 0"}, {expr: "'a b c' | truncatewords: nil"},
		{expr: "attrs.long | truncatewords"}},
	"slice": {{expr: "'héllo' | slice: 1, 2"}, {expr: "'héllo' | slice: -2"}, {expr: "'héllo' | slice: -10, 3"}, {expr: "'héllo' | slice: 0, 0"},
		{expr: "'héllo' | slice: 1"}, {expr: "'héllo' | slice: 10"}, {expr: "attrs.mixed | slice: 1, 2"}, {expr: "attrs.mixed | slice: -1"},
		{expr: "nil | slice: 0"}, {expr: "5 | slice: 0"}, {expr: "'abc' | slice: '1'"}, {expr: "'abc' | slice: 1, nil"}, {expr: "'abc' | slice"},
		{expr: "attrs.letters | slice: 0, 0"}, {expr: "'abc' | slice: 1, -1", differs: "renders", want: ""},
		{expr: "attrs.pair | slice: 0", differs: "template-value-type"}},
	"split": {{expr: "'a,b,,c,,' | split: ','"}, {expr: "nil | split: ','"}, {expr: "'' | split: ','"}, {expr: "'héllo' | split: ''"},
		{expr: "attrs.spaced | split: ' '"}, {expr: "'ab' | split"}, {expr: "'aXbXc' | split: 'X' | size"}, {expr: "'a1b' | split: 1"},
		{expr: "vars.big | split: ''", differs: "template-value-too-large"}},
	"join": {{expr: "vars.tools | join: ', '"}, {expr: "attrs.mixed | join: '-'"}, {expr: "attrs.empty | join"}, {expr: "nil | join"},
		{expr: "vars.tools | join: nil"}, {expr: "vars.tools | join"}, {expr: "profiles | join: 'é'"}, {expr: "'abc' | join"},
		{expr: "attrs.nested | join", differs: "template-value-type"}, {expr: "attrs.pair | join", differs: "template-value-type"}},
	"concat": {{expr: "vars.tools | concat: attrs.mixed"}, {expr: "nil | concat: vars.tools"}, {expr: "attrs.empty | concat: attrs.empty | size"},
		{expr: "vars.tools | concat"}, {expr: "vars.tools | concat: nil"}, {expr: "vars.tools | concat: 'x'"},
		{expr: "vars.tools | concat: attrs.pair", differs: "template-value-type"}},
	"map": {{expr: "attrs.objs | map: 'k'"}, {expr: "attrs.objs | map: 'missing'"}, {expr: "attrs.empty | map: 'k'"}, {expr: "nil | map: 'k'"},
		{expr: "vars.tools | map: 'size'"}, {expr: "attrs.objs | map: nil"}, {expr: "attrs.objs | map"}},
	"where": {{expr: "attrs.objs | where: 'k', 'a' | map: 'n'"}, {expr: "attrs.objs | where: 'k' | map: 'k'"}, {expr: "attrs.objs | where: 'n', 1 | map: 'k'"},
		{expr: "attrs.objs | where: 'k', nil | size"}, {expr: "nil | where: 'k'"}, {expr: "attrs.empty | where: 'k', 'a'"}, {expr: "attrs.objs | where | size"}},
	"compact": {{expr: "attrs.mixed | compact"}, {expr: "attrs.empty | compact"}, {expr: "nil | compact"}, {expr: "attrs.objs | map: 'k' | compact"}},
	"uniq": {{expr: "attrs.dups | uniq"}, {expr: "attrs.nested | uniq"}, {expr: "nil | uniq"}, {expr: "attrs.empty | uniq"},
		{expr: "attrs.objdups | uniq | size"}, {expr: "attrs.mixed | uniq"}},
	"first": {{expr: "vars.tools | first"}, {expr: "attrs.empty | first"}, {expr: "nil | first"}, {expr: "attrs.mixed | first"}, {expr: "'abc' | first"},
		{expr: "attrs.pair | first", differs: "template-value-type"}},
	"last": {{expr: "vars.tools | last"}, {expr: "attrs.empty | last"}, {expr: "nil | last"}, {expr: "attrs.mixed | last"}, {expr: "3 | last"}},
	"size": {{expr: "'héllo' | size"}, {expr: "vars.tools | size"}, {expr: "nil | size"}, {expr: "attrs | size"}, {expr: "5 | size"}, {expr: "'' | size"},
		{expr: "attrs.mixed | size"}},
	"default": {{expr: "nil | default: 'd'"}, {expr: "'' | default: 'd'"}, {expr: "false | default: 'd'"}, {expr: "false | default: 'd', allow_false: true"},
		{expr: "'' | default: 'd', allow_false: true"}, {expr: "nil | default: 'd', allow_false: true"}, {expr: "attrs.empty | default: 'd'"},
		{expr: "attrs.empty | default: 'd', allow_false: true"}, {expr: "'x' | default: 'd'"}, {expr: "0 | default: 'd'"}, {expr: "nil | default"},
		{expr: "vars.tools | default: 'd'"}, {expr: "true | default: 'd', allow_false: false"},
		{expr: "nil | default: 'd', zz: 1", differs: "template-syntax"}},
	"reverse": {{expr: "vars.tools | reverse"}, {expr: "attrs.mixed | reverse"}, {expr: "attrs.empty | reverse | size"}, {expr: "nil | reverse | size"}, {expr: "'ab' | reverse"}},
	"sort": {{expr: "attrs.letters | sort"}, {expr: "attrs.nums | sort"}, {expr: "attrs.objs | sort: 'k' | map: 'n'"}, {expr: "nil | sort | size"},
		{expr: "attrs.empty | sort | size"}, {expr: "attrs.objs | sort: 'n' | map: 'k'"},
		{expr: "attrs.pair | sort", differs: "template-value-type"}},
	"sort_natural": {{expr: "attrs.letters | sort_natural"}, {expr: "attrs.objs | sort_natural: 'k' | map: 'n'"}, {expr: "attrs.nums | sort_natural"},
		{expr: "attrs.mixed | sort_natural"}, {expr: "attrs.empty | sort_natural | size"}, {expr: "nil | sort_natural | size"}},
	"json": {{expr: "'héllo' | json"}, {expr: "nil | json"}, {expr: "attrs.objs | json"}, {expr: "attrs.mixed | json"}, {expr: "3 | json"},
		{expr: "true | json"}, {expr: "-1 | json"}, {expr: "'' | json"},
		{expr: "attrs.html | json", differs: "output", want: `"<a&b>"`},
		{expr: "attrs.control | json", differs: "output", want: `"a\u007fb\u0085"`}},
	"plus": {{expr: "1 | plus: 2"}, {expr: "-1 | plus: -2"}, {expr: "1 | plus: 2.5"}, {expr: "0.1 | plus: 0.2"}, {expr: "attrs.nums | first | plus: 0"},
		{expr: "attrs.max | plus: 1", differs: "template-value-too-large"}, {expr: "'1' | plus: 1", differs: "template-value-type"},
		{expr: "1 | plus: '1'", differs: "template-value-type"}, {expr: "nil | plus: 1", differs: "template-value-type"}},
	"minus": {{expr: "1 | minus: 2"}, {expr: "0 | minus: -0.5"}, {expr: "attrs.min | minus: -1"},
		{expr: "attrs.min | minus: 1", differs: "template-value-too-large"}, {expr: "'a' | minus: 1", differs: "template-value-type"}},
	"times": {{expr: "-4 | times: 3"}, {expr: "1.5 | times: 2"}, {expr: "0 | times: attrs.min"}, {expr: "-1 | times: 3"}, {expr: "attrs.max | times: -1"}, {expr: "attrs.max | times: 1"},
		{expr: "attrs.max | times: 2", differs: "template-value-too-large"}, {expr: "attrs.min | times: -1", differs: "template-value-too-large"},
		{expr: "true | times: 2", differs: "template-value-type"}},
	"divided_by": {{expr: "7 | divided_by: 2"}, {expr: "-7 | divided_by: 2"}, {expr: "7.9 | divided_by: 2"}, {expr: "7 | divided_by: 2.0"},
		{expr: "-7 | divided_by: -2.5"}, {expr: "1 | divided_by: 0"}, {expr: "1 | divided_by: 0.0"},
		{expr: "attrs.min | divided_by: -1", differs: "template-value-too-large"},
		{expr: "attrs.max | divided_by: 1", differs: "output", want: "9223372036854775807"}, {expr: "attrs.big | divided_by: 1", differs: "output", want: "9007199254740993"}, {expr: "'7' | divided_by: 2", differs: "template-value-type"}},
	"modulo": {{expr: "7 | modulo: 3"}, {expr: "-7 | modulo: 3"}, {expr: "7 | modulo: -3"}, {expr: "5.5 | modulo: 2"}, {expr: "0 | modulo: 5"},
		{expr: "1 | modulo: 0", differs: "template-syntax"}, {expr: "'7' | modulo: 2", differs: "template-value-type"}},
	"at_least": {{expr: "1 | at_least: 2"}, {expr: "3 | at_least: 2.5"}, {expr: "-1 | at_least: -2"}, {expr: "-0.0 | at_least: 0.0"}, {expr: "0.0 | at_least: -0.0"}, {expr: "'1' | at_least: 2", differs: "template-value-type"}},
	"at_most":  {{expr: "1 | at_most: 2"}, {expr: "3 | at_most: 2.5"}, {expr: "-1 | at_most: -2"}, {expr: "-0.0 | at_most: 0.0"}, {expr: "0.0 | at_most: -0.0"}, {expr: "nil | at_most: 2", differs: "template-value-type"}},
	"abs": {{expr: "-3 | abs"}, {expr: "-2.5 | abs"}, {expr: "0 | abs"}, {expr: "attrs.min | abs"},
		{expr: "'-3' | abs", differs: "template-value-type"}},
}

// outcome is what one engine made of a template: its output, or the error it returned or the
// panic it raised.
type outcome struct {
	out    string
	failed error
}

func standard(body string, b map[string]any) (o outcome) {
	defer func() {
		if r := recover(); r != nil {
			o = outcome{failed: fmt.Errorf("panic: %v", r)}
		}
	}()
	out, err := liquid.NewEngine().ParseAndRenderString(body, b)
	if err != nil {
		return outcome{failed: err}
	}
	return outcome{out: out}
}

var _ = Describe("the differential corpus", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("each allowlisted filter matches liquid's standard filter on the corpus", func() {
		big := strings.Repeat("x", 40<<10)
		obj := func(kv ...any) map[string]any {
			m := map[string]any{}
			for i := 0; i < len(kv); i += 2 {
				m[kv[i].(string)] = kv[i+1]
			}
			return m
		}
		attrs := map[string]any{
			"mixed":   []any{"b", int64(1), true, nil, "a"},
			"objs":    []any{obj("k", "b", "n", int64(2)), obj("k", "a", "n", int64(1)), obj("k", nil, "n", int64(3))},
			"objdups": []any{obj("k", "a"), obj("k", "a"), obj("k", "b")},
			"nested":  []any{"a", []any{"b"}},
			"dups":    []any{"a", "b", "a", int64(1), int64(1), true, nil, nil},
			"letters": []any{"b", "C", "a"},
			"nums":    []any{int64(3), int64(1), int64(2)},
			"empty":   []any{},
			"pair":    obj("a", "1", "b", "2"),
			"text":    " \ta b\r\nc\n ",
			"spaced":  " a  b \t c\n",
			"long":    strings.Repeat("word ", 30),
			"html":    "<a&b>",
			"control": "a\x7fb\u0085",
			"max":     int64(math.MaxInt64),
			"min":     int64(math.MinInt64),
			"big":     int64(1<<53 + 1),
		}
		data := templating.Data{Target: "claude-code", Scope: "project", Profiles: []string{"default", "wörk"},
			Vars: map[string]any{"tools": []string{"rg", "jq"}, "big": big}, Attrs: attrs}

		// liquid sees the values lola binds: lists as []any, maps as IterationKeyedMap.
		var asLiquid func(v any) any
		asLiquid = func(v any) any {
			switch v := v.(type) {
			case []any:
				out := make([]any, len(v))
				for i, e := range v {
					out[i] = asLiquid(e)
				}
				return out
			case map[string]any:
				out := map[string]any{}
				for k, e := range v {
					out[k] = asLiquid(e)
				}
				return liquid.IterationKeyedMap(out)
			}
			return v
		}
		bindings := map[string]any{
			"target": data.Target, "scope": data.Scope, "profiles": []any{"default", "wörk"},
			"vars":  liquid.IterationKeyedMap(map[string]any{"tools": []any{"rg", "jq"}, "big": big}),
			"attrs": asLiquid(attrs),
		}

		Expect(corpus).To(HaveLen(len(allowlisted)))
		for _, name := range allowlisted {
			Expect(corpus).To(HaveKey(name))
			for _, r := range corpus[name] {
				body := "{{ " + r.expr + " }}"
				std := standard(body, bindings)
				out, err := templating.Render(context.Background(), source(body), templating.Partials{}, data, templating.DefaultLimits(), []string{"tools", "big"})
				switch {
				case r.differs == "output":
					Expect(err).NotTo(HaveOccurred(), body)
					Expect(std.failed).NotTo(HaveOccurred(), body)
					Expect(string(out)).To(Equal(r.want), body)
					Expect(string(out)).NotTo(Equal(std.out), body)
				case r.differs == "renders":
					Expect(std.failed).To(HaveOccurred(), body)
					Expect(err).NotTo(HaveOccurred(), body)
					Expect(string(out)).To(Equal(r.want), body)
				case r.differs != "":
					Expect(std.failed).NotTo(HaveOccurred(), body)
					Expect(err).To(HaveField("Code", r.differs), body)
				case std.failed != nil:
					// liquid refuses or panics: lola must refuse cleanly, never panic or render
					var e *templating.Error
					Expect(errors.As(err, &e)).To(BeTrue(), "%s: liquid failed (%v) but lola rendered %q", body, std.failed, out)
					Expect(e.Code).NotTo(Equal("template-panic"), body)
				default:
					Expect(err).NotTo(HaveOccurred(), body)
					Expect(string(out)).To(Equal(std.out), body)
				}
			}
		}
	})
})
