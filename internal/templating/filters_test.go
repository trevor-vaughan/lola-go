package templating_test

import (
	"context"
	"maps"
	"math"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/templating"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("filters", func() {
	var (
		data templating.Data
		lim  templating.Limits
	)
	declared := []string{"big", "huge", "ctl", "tools", "growing"}

	BeforeEach(func() {
		testenv.Isolate()
		lim = templating.DefaultLimits()
		data = templating.Data{
			Target: "claude-code", Scope: "project", Profiles: []string{"default", "work"},
			Vars: map[string]any{
				"big": strings.Repeat("x", 40<<10), "huge": strings.Repeat("x", 64<<10+1),
				"ctl": strings.Repeat("\x01", 11<<10), "tools": []string{"rg", "jq"},
				"growing": strings.Repeat("ɐ", 22<<10),
			},
			Attrs: map[string]any{
				"halves":  map[string]any{"a": []any{strings.Repeat("x", 32<<10)}, "b": []any{strings.Repeat("x", 32<<10)}},
				"nested":  []any{"a", []any{"b"}},
				"control": "a\x7fb\u0085c\u009b<>&\n\"",
				"n":       int64(12345678901234),
				"none":    nil,
				"listing": map[string]any{"b": []any{"y", true}, "a": "x"},
			},
		}
	})

	rendered := func(body string) string {
		out, err := templating.Render(context.Background(), source(body), templating.Partials{}, data, lim, declared)
		Expect(err).NotTo(HaveOccurred())
		return string(out)
	}
	refused := func(body string) *templating.Error { return refusal(source(body), data, lim, declared...) }

	It("chains lola's filters as liquid does", func() {
		Expect(rendered(`{{ vars.tools | join: ", " | upcase | prepend: "[" | append: "]" }} {{ target | split: "-" | last }}`)).
			To(Equal("[RG, JQ] code"))
	})

	It("slice with extreme start or length returns empty and never panics", func() {
		Expect(rendered("[{{ target | slice: 9223372036854775807, 5 }}]")).To(Equal("[]"))
		Expect(rendered("[{{ target | slice: 2, -9223372036854775808 }}]")).To(Equal("[]"))
		Expect(rendered("[{{ target | slice: -9223372036854775808, 2 }}]")).To(Equal("[cl]"))
		Expect(rendered("[{{ target | slice: 2, 9223372036854775807 }}]")).To(Equal("[aude-code]"))
		Expect(rendered("[{{ profiles | slice: 9223372036854775807 | size }}]")).To(Equal("[0]"))
		Expect(rendered("[{{ profiles | slice: -1, 9223372036854775807 | join }}]")).To(Equal("[work]"))
	})

	It("every growing filter refuses before allocating past 64 KiB", func() {
		const cap64 = "; a value in a lola template is at most 64 KiB"
		for _, c := range []struct {
			body, reason string
			tools        []string // vars.tools, when the row needs its own
			value        int      // lim.Value, when the row lowers it
			profiles     int
		}{
			{body: "{{ vars.big | append: vars.big }}", reason: "append would make a 80 KiB value" + cap64},
			{body: "{{ vars.big | prepend: vars.big }}", reason: "prepend would make a 80 KiB value" + cap64},
			{body: `{{ vars.big | replace: "", "xx" }}`, reason: "replace would make a 121 KiB value" + cap64},
			{body: `{{ vars.big | replace_first: "x", vars.big }}`, reason: "replace_first would make a 80 KiB value" + cap64},
			{body: `{{ vars.big | replace_last: "x", vars.big }}`, reason: "replace_last would make a 80 KiB value" + cap64},
			{body: "{{ vars.tools | join: vars.big }}", reason: "join would make a 81 KiB value" + cap64, tools: []string{"a", "b", "c"}},
			{body: "{{ vars.tools | concat: vars.tools }}", reason: "concat would make a 81 KiB value" + cap64, tools: []string{strings.Repeat("x", 40<<10)}},
			{body: `{{ vars.big | split: "" }}`, reason: "split would make a list of 40960 items; a list in a lola template holds at most 4096"},
			{body: `{{ profiles | map: "size" }}`, reason: "map would make a 2 KiB value; a value in a lola template is at most 1 KiB", value: 1 << 10, profiles: 200},
			{body: "{{ vars.big | truncate: 10, vars.big }}", reason: "truncate would make a 80 KiB value" + cap64},
			{body: "{{ vars.growing | upcase }}", reason: "upcase would make a 66 KiB value" + cap64},
			{body: "{{ vars.ctl | json }}", reason: "json could make a 67 KiB value" + cap64},
			{body: "{{ vars.tools | json }}", reason: "json could make a 88 KiB value" + cap64, tools: make([]string, 30000)},
			{body: "{% assign s = 'xx' %}{% for p in profiles %}{% assign s = s | append: s %}{% endfor %}{{ s }}",
				reason: "append would make a 128 KiB value" + cap64, profiles: 20},
		} {
			d, l := data, lim
			d.Vars = maps.Clone(data.Vars)
			if c.tools != nil {
				d.Vars["tools"] = c.tools
			}
			if c.value > 0 {
				l.Value = c.value
			}
			d.Profiles = make([]string, c.profiles)
			e := refusal(source(c.body), d, l, declared...)
			Expect(e.Code).To(Equal("template-value-too-large"), c.body)
			Expect(e.Line).To(Equal(1), c.body)
			Expect(e.Reason).To(Equal(c.reason), c.body)
		}
	})

	It("a filter given too many arguments says takes no arguments, at most one argument, or at most N arguments", func() {
		for body, reason := range map[string]string{
			"{{ 'x' | strip: 1 }}":            "strip takes no arguments; it got 1",
			"{{ 'x' | append: 1, 2 }}":        "append takes at most one argument; it got 2",
			"{{ 'x' | replace: 1, 2, 3, 4 }}": "replace takes at most 2 arguments; it got 4",
			"{{ nil | default: 1, 2 }}":       "default takes at most one argument; it got 2",
		} {
			e := refused(body)
			Expect(e.Code).To(Equal("template-syntax"), body)
			Expect(e.Reason).To(Equal(reason), body)
		}
	})

	It("a list or map argument to a string filter refuses as template-value-type", func() {
		for body, reason := range map[string]string{
			"{{ 'a' | append: vars.tools }}":               "append takes a string argument; argument 1 is a list",
			"{{ 'a' | prepend: attrs.listing }}":           "prepend takes a string argument; argument 1 is a map",
			"{{ 'a' | replace: 'a', vars.tools }}":         "replace takes a string argument; argument 2 is a list",
			"{{ 'a' | remove: attrs.listing }}":            "remove takes a string argument; argument 1 is a map",
			"{{ 'abcdef' | truncate: 3, vars.tools }}":     "truncate takes a string argument; argument 2 is a list",
			"{{ 'a b' | split: vars.tools }}":              "split takes a string argument; argument 1 is a list",
			"{{ vars.tools | join: attrs.listing }}":       "join takes a string argument; argument 1 is a map",
			"{{ 'a' | remove_last: vars.tools }}":          "remove_last takes a string argument; argument 1 is a list",
			"{{ 'a b c' | truncatewords: 1, vars.tools }}": "truncatewords takes a string argument; argument 2 is a list",
		} {
			e := refused(body)
			Expect(e.Code).To(Equal("template-value-type"), body)
			Expect(e.Reason).To(Equal(reason), body)
		}
		Expect(rendered("{{ 'a' | append: 1 }}{{ 'a' | append: true }}{{ 'a' | append: nil }}")).To(Equal("a1atruea"))
	})

	It("keyword arguments other than default's allow_false refuse", func() {
		for body, reason := range map[string]string{
			"{{ 'abc' | truncate: 2, foo: 1 }}":                       "truncate takes no keyword arguments; it got foo",
			"{{ 'a' | append: 'b', zz: 1, aa: 2 }}":                   "append takes no keyword arguments; it got aa, zz",
			"{{ 'a' | upcase: 1, k: 1 }}":                             "upcase takes no keyword arguments; it got k",
			"{{ nil | default: 'd', zz: 1 }}":                         "default takes only the allow_false keyword argument; it got zz",
			"{{ nil | default: 'd', allow_false: true, b: 1, a: 2 }}": "default takes only the allow_false keyword argument; it got a, b",
		} {
			e := refused(body)
			Expect(e.Code).To(Equal("template-syntax"), body)
			Expect(e.Reason).To(Equal(reason), body)
		}
		Expect(rendered("{{ false | default: 'd', allow_false: true }} {{ false | default: 'd' }}")).To(Equal("false d"))
	})

	It("Value and Items caps apply to filter arguments and arrays, and size counts nested lists and maps", func() {
		e := refused(`{{ "x" | append: vars.huge }}`)
		Expect(e.Code).To(Equal("template-value-too-large"))
		Expect(e.Reason).To(Equal("append got a 65 KiB value; a value in a lola template is at most 64 KiB"))

		data.Profiles = make([]string, 4096)
		e = refused("{{ profiles | concat: vars.tools | size }}")
		Expect(e.Reason).To(Equal("concat would make a list of 4098 items; a list in a lola template holds at most 4096"))
		data.Vars["tools"] = []string{}
		Expect(rendered("{{ profiles | concat: vars.tools | size }}")).To(Equal("4096"))

		e = refused("{{ attrs.halves | size }}")
		Expect(e.Reason).To(Equal("size got a 65 KiB value; a value in a lola template is at most 64 KiB"))
	})

	It("a value of an unlisted kind fails closed with template-value-type", func() {
		e := refused("{{ attrs.nested | join }}")
		Expect(e.Code).To(Equal("template-value-type"))
		Expect(e.Reason).To(Equal("join takes a list of strings, numbers or booleans; item 2 is a list"))
		e = refused("{{ attrs.listing | first }}")
		Expect(e.Reason).To(Equal("first takes a list; this is a map"))
	})

	It("json writes DEL and C1 runes escaped, integers in decimal and nil as null", func() {
		Expect(rendered("{{ attrs.control | json }}")).To(Equal(`"a\u007fb\u0085c\u009b<>&\n\""`))
		Expect(rendered("{{ attrs.n | json }} {{ attrs.none | json }} {{ 42 | json }}")).To(Equal("12345678901234 null 42"))
		Expect(rendered("{{ attrs.listing | json }} {{ vars.tools | json }}")).To(Equal(`{"a":"x","b":["y",true]} ["rg","jq"]`))
	})

	It("math filters follow liquid's numeric semantics and refuse overflow, non-finite results and division by zero", func() {
		data.Attrs["max"], data.Attrs["min"], data.Attrs["big"] = int64(math.MaxInt64), int64(math.MinInt64), int64(1<<53+1)
		huge := "1" + strings.Repeat("0", 308) + ".0" // 1e308, as liquid's float grammar has no exponent
		for body, want := range map[string]string{
			"{{ 2 | plus: 3 }} {{ -2 | plus: -3 }} {{ 1 | plus: 2.5 }} {{ 0.1 | plus: 0.2 }}":                                                  "5 -5 3.5 0.30000000000000004",
			"{{ 2 | minus: 3 }} {{ 2.5 | minus: 1 }} {{ attrs.min | minus: -1 }}":                                                              "-1 1.5 -9223372036854775807",
			"{{ -4 | times: 3 }} {{ 1.5 | times: 2 }} {{ attrs.max | times: 1 }} {{ 0 | times: attrs.min }}":                                   "-12 3 9223372036854775807 0",
			"{{ -1 | times: 3 }} {{ 3 | times: -1 }} {{ -1 | times: attrs.max }}":                                                              "-3 -3 -9223372036854775807",
			"{{ 7 | divided_by: 2 }} {{ -7 | divided_by: 2 }} {{ 7.9 | divided_by: 2 }} {{ 7 | divided_by: 2.0 }}":                             "3 -3 3 3.5",
			"{{ attrs.max | divided_by: 1 }} {{ attrs.big | divided_by: 1 }} {{ attrs.min | divided_by: 1 }} {{ attrs.big | divided_by: -3 }}": "9223372036854775807 9007199254740993 -9223372036854775808 -3002399751580331",
			"{{ 7 | modulo: 3 }} {{ -7 | modulo: 3 }} {{ 7 | modulo: -3 }} {{ 5.5 | modulo: 2 }}":                                              "1 -1 1 1.5",
			"{{ 1 | at_least: 2 }} {{ 3 | at_least: 2.5 }} {{ 1 | at_most: 2 }} {{ 3 | at_most: 2.5 }}":                                        "2 3 1 2.5",
			"{{ -3 | abs }} {{ -2.5 | abs }} {{ attrs.min | abs }}":                                                                            "3 2.5 9223372036854776000",
			"{{ " + huge + " | times: 1 }}": "1" + strings.Repeat("0", 308),
		} {
			Expect(rendered(body)).To(Equal(want), body)
		}
		for body, want := range map[string]string{
			"{{ attrs.max | plus: 1 }}":        "template-value-too-large plus would overflow a 64-bit integer",
			"{{ attrs.min | minus: 1 }}":       "template-value-too-large minus would overflow a 64-bit integer",
			"{{ attrs.max | times: 2 }}":       "template-value-too-large times would overflow a 64-bit integer",
			"{{ attrs.min | times: -1 }}":      "template-value-too-large times would overflow a 64-bit integer",
			"{{ -1 | times: attrs.min }}":      "template-value-too-large times would overflow a 64-bit integer",
			"{{ attrs.min | divided_by: -1 }}": "template-value-too-large divided_by would overflow a 64-bit integer",
			"{{ " + huge + " | times: 10 }}":   "template-value-too-large times would not give a finite number",
			"{{ 1.5 | divided_by: 0 }}":        "template-syntax divided_by 0: a number cannot be divided by zero",
			"{{ 1 | divided_by: 0.0 }}":        "template-syntax divided_by 0: a number cannot be divided by zero",
			"{{ 1 | modulo: 0 }}":              "template-syntax modulo 0: a number cannot be divided by zero",
			"{{ '3' | plus: 1 }}":              "template-value-type plus takes numbers; it got a string",
			"{{ 3 | plus: '1' }}":              "template-value-type plus takes numbers; it got a string",
			"{{ nil | abs }}":                  "template-value-type abs takes numbers; it got nil",
			"{{ 3 | at_most }}":                "template-syntax at_most is missing its argument, a number",
			"{{ 1 | plus }}":                   "template-syntax plus is missing its argument, a number",
			"{{ 1 | plus: nil }}":              "template-value-type plus takes numbers; it got nil",
			"{{ true | minus: 1 }}":            "template-value-type minus takes numbers; it got true or false",
			"{{ vars.tools | times: 1 }}":      "template-value-type times takes numbers; it got a list",
			"{{ -3 | abs: 1 }}":                "template-syntax abs takes no arguments; it got 1",
			"{{ 1 | modulo: 2, 3 }}":           "template-syntax modulo takes at most one argument; it got 2",
		} {
			e := refused(body)
			Expect(e.Code+" "+e.Reason).To(Equal(want), body)
			Expect(e.Line).To(Equal(1), body)
		}
	})
})
