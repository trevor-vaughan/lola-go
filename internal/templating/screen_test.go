package templating_test

import (
	"context"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/templating"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("the screen", func() {
	var data templating.Data

	BeforeEach(func() {
		testenv.Isolate()
		data = templating.Data{Target: "claude-code", Scope: "project"}
	})

	refused := func(body string, declared ...string) *templating.Error {
		return refusal(source(body), data, templating.DefaultLimits(), declared...)
	}
	rendered := func(body string, declared ...string) string {
		out, err := templating.Render(context.Background(), source(body), templating.Partials{}, data, templating.DefaultLimits(), declared)
		Expect(err).NotTo(HaveOccurred())
		return string(out)
	}

	It("capture and tablerow are refused with their line", func() {
		e := refused("a\n\n{% capture x %}y{% endcapture %}")
		Expect(e.Code).To(Equal("template-tag-unavailable"))
		Expect(e.Error()).To(Equal(file + ":3: the capture tag is not available in lola templates"))
		e = refused("{% tablerow p in profiles %}{{ p }}{% endtablerow %}")
		Expect(e.Error()).To(Equal(file + ":1: the tablerow tag is not available in lola templates"))
		e = refused("{% endcapture %}")
		Expect(e.Error()).To(Equal(file + ":1: the capture tag is not available in lola templates"))
	})

	It("a tag inside raw or comment is text and is never refused", func() {
		Expect(rendered("{% raw %}{% capture x %}{{ (1..5) }}{% include 'x' %}{% endraw %}")).
			To(Equal("{% capture x %}{{ (1..5) }}{% include 'x' %}"))
		Expect(rendered("a{% comment %}{% capture x %}{{ (1..5) }}{% foo %}{% endcomment %}b")).To(Equal("ab"))
		Expect(rendered("{%- # a note -%}")).To(Equal("{%- # a note -%}"))
	})

	It(".. is refused outside string literals in markup, so no template builds a range", func() {
		for _, body := range []string{
			"{% for i in (1..1000000000) %}{% endfor %}",
			"{% assign r = (1..50000000) %}{{ r.AsArray }}",
			"{{ (1..3) | join: ',' }}",
			"{% for i in (1..3) %}{% endfor %}",
			`{{ "\\".. }}`,
			"{{ 'a' | append: 'b'..'c' }}",
			"{{ 'a..b }}",
		} {
			e := refused(body)
			Expect(e.Code).To(Equal("template-range-unavailable"), body)
			Expect(e.Error()).To(Equal(file+":1: ranges like (1..n) are not available in lola templates; loop over a list"), body)
		}
		Expect(rendered(`{{ '\' }}..`)).To(Equal(`\..`))
		Expect(rendered(`{{ "\"" | append: ".." }}`)).To(Equal(`"..`))
		Expect(rendered("{{ 'a..b' }} {{ \"a..b\" }}")).To(Equal("a..b a..b"))
		Expect(rendered("{% if target == '..' %}y{% endif %}")).To(BeEmpty())
		Expect(rendered("a..b")).To(Equal("a..b"))
	})

	It("256 assigns of 64 KiB values render and a 257th assign tag is refused", func() {
		var b strings.Builder
		b.WriteString(`{% assign v0 = "` + strings.Repeat("x", 64<<10) + `" %}`)
		for i := 1; i < 256; i++ {
			b.WriteString("\n{% assign v" + strconv.Itoa(i) + " = v" + strconv.Itoa(i-1) + " %}")
		}
		Expect(rendered(b.String() + "{{ v255 }}")).To(HaveLen(255 + 64<<10))

		e := refused(b.String() + "\n{% assign v256 = v255 %}")
		Expect(e.Code).To(Equal("template-too-many-assigns"))
		Expect(e.Error()).To(Equal(file + ":257: more than 256 assign tags; a template and its partials have at most 256"))
	})

	It("nesting is capped: 64 open blocks render, 65 are template-too-deep; 65 nested parentheses are template-too-deep", func() {
		open := func(n int) string {
			return strings.Repeat("{% if true %}\n", n) + "x" + strings.Repeat("{% endif %}", n)
		}
		Expect(rendered(open(64))).To(Equal(strings.Repeat("\n", 64) + "x"))

		e := refused(open(65))
		Expect(e.Code).To(Equal("template-too-deep"))
		Expect(e.Error()).To(Equal(file + ":65: blocks nest more than 64 deep"))

		e = refused("\n{{ " + strings.Repeat("(", 65) + "true" + strings.Repeat(")", 65) + " }}")
		Expect(e.Code).To(Equal("template-too-deep"))
		Expect(e.Error()).To(Equal(file + ":2: parentheses nest more than 64 deep"))
	})

	It("assign to forloop is refused before compile with its line", func() {
		data.Profiles = []string{"a", "b"}
		e := refused("x\n{% assign forloop = 1 %}{% for i in profiles %}{% cycle 'a','b' %}{% endfor %}")
		Expect(e.Code).To(Equal("template-syntax"))
		Expect(e.Line).To(Equal(2))
		Expect(refused("{% assign  forloop=1 %}").Code).To(Equal("template-syntax"))
		Expect(rendered("{% assign forloops = 1 %}{{ forloops }}")).To(Equal("1"))
	})

	It("the assign-to-forloop refusal says assign cannot set forloop; it belongs to the for tag", func() {
		Expect(refused("{% assign forloop = 1 %}").Error()).To(Equal(file + ":1: assign cannot set forloop; it belongs to the for tag"))
	})

	It("a for loop variable named forloop is refused before compile with its line", func() {
		data.Profiles = []string{"a", "b"}
		e := refused("x\n\n{% for forloop in profiles %}{% cycle 'a','b' %}{% endfor %}")
		Expect(e.Code).To(Equal("template-syntax"))
		Expect(e.Error()).To(Equal(file + ":3: for cannot name its variable forloop; it belongs to the for tag"))
		Expect(rendered("{% for forloops in profiles %}{{ forloops }}{% endfor %}")).To(Equal("ab"))
	})

	It("messages list names sorted, whatever the map order", func() {
		e := refused("{{ vars.x }}", "tools", "language", "strict")
		Expect(e.Reason).To(Equal("vars.x is not declared; review-kit declares language, strict, tools"))

		bad := templating.Data{
			Vars:  map[string]any{"m": 1, "c": 2, "x": 3, "a": 4, "q": 5},
			Attrs: map[string]any{"z": "ok", "y": map[string]any{"m": 1, "b": 2, "k": 3}},
		}
		for range 20 {
			Expect(refusal(source("x"), bad, templating.DefaultLimits()).Reason).To(HavePrefix("vars.a holds"))
		}
		bad.Vars = nil
		for range 20 {
			Expect(refusal(source("x"), bad, templating.DefaultLimits()).Reason).To(HavePrefix("attrs.y.b holds"))
		}
	})

	It("printing vars or attrs whole is refused before compile with its line", func() {
		data.Profiles = []string{"a"}
		for body, want := range map[string]string{
			"x\n{{ vars }}":     ":2: vars is a map; print one of its keys or loop over it",
			"{{attrs}}":         ":1: attrs is a map; print one of its keys or loop over it",
			"{{- \tattrs\n -}}": ":1: attrs is a map; print one of its keys or loop over it",
		} {
			e := refused(body, "x")
			Expect(e.Code).To(Equal("template-syntax"), body)
			Expect(e.Error()).To(Equal(file+want), body)
		}
		data.Vars = map[string]any{"x": "v"}
		data.Attrs = map[string]any{"k": "w"}
		// liquid's size of a map is 0
		Expect(rendered("{{ vars.x }} {{ attrs | size }} {{ attrs | json }} {% for p in profiles %}{{ forloop.index }}{% endfor %} {{ vars['x'] }}", "x")).
			To(Equal(`v 0 {"k":"w"} 1 v`))
	})

	It("an unknown tag is named as unknown, never as a misplaced end tag", func() {
		e := refused("ok\n{% foo %}x{% endfoo %}")
		Expect(e.Code).To(Equal("template-tag-unavailable"))
		Expect(e.Error()).To(Equal(file + `:2: unknown tag "foo"`))
		for _, name := range []string{"increment", "decrement", "echo", "liquid"} {
			Expect(refused("{% "+name+" x %}").Error()).To(Equal(file+`:1: unknown tag "`+name+`"`), name)
		}
	})

	It("a reference to an undeclared vars name is refused before rendering, naming the declared vars", func() {
		declared := []string{"language", "strict", "tools"}
		for _, body := range []string{
			"{% if false %}\n{{ vars.lang }}{% endif %}",
			"{% if false %}\n{{ vars['lang'] }}{% endif %}",
			"{% if false %}\n{% assign x = target | append: vars .lang %}{% endif %}",
		} {
			e := refused(body, declared...)
			Expect(e.Code).To(Equal("template-undeclared-var"), body)
			Expect(e.Error()).To(Equal(file+":2: vars.lang is not declared; review-kit declares language, strict, tools"), body)
		}
		for _, body := range []string{"{{ vars\f.zz }}", "{{ vars\v.zz }}", "{{ vars[\f'zz'] }}", "{{ vars['zz'\v] }}", "{% if vars\f.zz %}x{% endif %}"} {
			e := refused(body, declared...)
			Expect(e.Code).To(Equal("template-undeclared-var"), "%q", body)
			Expect(e.Error()).To(Equal(file+":1: vars.zz is not declared; review-kit declares language, strict, tools"), "%q", body)
		}
		Expect(refused("{{ vars.language }}").Error()).To(Equal(file + ":1: vars.language is not declared; review-kit declares no vars"))
		e := refusal(templating.Source{Module: "kit\x1b[2J", File: file, Line: 1, Body: "{{ vars.x }}"}, data, templating.DefaultLimits())
		Expect(e.Reason).To(Equal("vars.x is not declared; kit?[2J declares no vars"))

		Expect(rendered("{% if false %}{{ vars.language }}{{ vars[\"tools\"] }}{% endif %}", declared...)).To(BeEmpty())
		Expect(rendered("{{ 'vars.lang' }} {{ \"vars.x\" }}", declared...)).To(Equal("vars.lang vars.x"))
	})
})
