package templating_test

import (
	"context"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/templating"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// allowlisted are lola's 41 filters, as docs list them for authors.
var allowlisted = []string{
	"abs", "append", "at_least", "at_most", "capitalize", "compact", "concat", "default", "divided_by",
	"downcase", "first", "join", "json", "last", "lstrip", "map", "minus", "modulo", "plus", "prepend",
	"remove", "remove_first", "remove_last", "replace", "replace_first", "replace_last", "reverse",
	"rstrip", "size", "slice", "sort", "sort_natural", "split", "strip", "strip_newlines", "times",
	"truncate", "truncatewords", "uniq", "upcase", "where",
}

var _ = Describe("error mapping", func() {
	var data templating.Data

	BeforeEach(func() {
		testenv.Isolate()
		data = templating.Data{Target: "claude-code", Scope: "project"}
	})

	It("an undefined variable refuses with the file, the file's line and the token", func() {
		for _, c := range []struct {
			name, body, want string
			line             int
		}{
			{"no frontmatter", "a\n{{ targt }}", ":2: undefined variable in {{ targt }}", 1},
			{"a 5-line frontmatter", "{{ targt }}", ":6: undefined variable in {{ targt }}", 6},
			{"a 5-line frontmatter, later line", "x\n\n{% if targt %}y\n{% endif %}", ":8: undefined variable in {% if targt %}", 6},
			{"a BOM", "\uFEFF{{ targt }}", ":1: undefined variable in {{ targt }}", 1},
			{"CRLF", "a\r\nb\r\n{{ targt | upcase }}\r\n", ":3: undefined variable in {{ targt | upcase }}", 1},
			{"two tokens on the line", "{{ target }} {{ targt }}", ":1: undefined variable", 1},
			{"an if and its endif on one line", "{% if targt %}y{% endif %}", ":1: undefined variable in {% if targt %}", 1},
			{"an endif after the object on its line", "{% if target %}\n{{ targt }}{% endif %}", ":2: undefined variable in {{ targt }}", 1},
			{"raw and comment on the line", "{% raw %}{{ a }}{% endraw %}{% comment %}{{ b }}{% endcomment %}{{ targt }}", ":1: undefined variable in {{ targt }}", 1},
		} {
			e := refusal(templating.Source{File: file, Line: c.line, Body: c.body}, data, templating.DefaultLimits())
			Expect(e.Code).To(Equal("template-undefined-variable"), c.name)
			Expect(e.Error()).To(Equal(file+c.want), c.name)
		}
	})

	It("only the 41 allowlisted filters exist; each of the 16 others is undefined, with a suggestion when one is close", func() {
		Expect(allowlisted).To(HaveLen(41))
		for _, name := range allowlisted {
			_, err := templating.Render(context.Background(), source("{{ 'x' | "+name+" }}"), templating.Partials{}, data, templating.DefaultLimits(), nil)
			Expect(err).To(Or(BeNil(), Not(HaveField("Code", "template-undefined-filter"))), name)
		}
		list := "; lola templates have: " + strings.Join(allowlisted, ", ")
		for name, reason := range map[string]string{
			"ceil": list, "floor": list, "round": list, "sum": list,
			"date": list, "inspect": list, "type": list, "escape": list, "escape_once": list,
			"newline_to_br": list, "strip_html": list, "url_encode": list, "url_decode": list,
			"handleize": list, "handle": list, "pluralize": list,
		} {
			e := refusal(source("{{ 'x' | "+name+" }}"), data, templating.DefaultLimits())
			Expect(e.Code).To(Equal("template-undefined-filter"), name)
			Expect(e.Error()).To(Equal(file+`:1: undefined filter "`+name+`"`+reason), name)
		}
		e := refusal(source("\n{{ 'x' | uppcase }}"), data, templating.DefaultLimits())
		Expect(e.Error()).To(Equal(file + `:2: undefined filter "uppcase" (did you mean "upcase"?)`))
		e = refusal(source("{{ 'x' | strp }}"), data, templating.DefaultLimits())
		Expect(e.Error()).To(Equal(file+`:1: undefined filter "strp"`+list), "strip, lstrip, rstrip and sort are all close")
	})

	It("every author-derived string in an error is cleaned and capped", func() {
		long := strings.Repeat("a", 10<<10)
		ctl := templating.Source{File: "agents/\x1b[31mx\u202e.md.liquid", Line: 1}

		ctl.Body = "{{ targt | append: '\x1b]0;\u009b\u202e" + long + "' }}"
		e := refusal(ctl, data, templating.DefaultLimits())
		Expect(e.File).To(Equal("agents/?[31mx?.md.liquid"))
		Expect(e.Reason).To(Equal("undefined variable in {{ targt | append: '?]0;??" + strings.Repeat("a", 120-len("{{ targt | append: '\x1b]0;\u009b\u202e")) + "…"))

		e = refusal(templating.Source{File: long, Line: 1, Body: "{{ targt }}"}, data, templating.DefaultLimits())
		Expect(e.File).To(Equal(long[:256] + "…"))

		ctl.Body = "{{ " + long + " | }}"
		e = refusal(ctl, data, templating.DefaultLimits())
		Expect(e.Code).To(Equal("template-syntax"))
		Expect(len(e.Reason)).To(BeNumerically("<=", 512+len("…")))
		Expect(e.Reason).To(HaveSuffix("…"))

		ctl.Body = "{{ a\u009b" + long + " }}"
		e = refusal(ctl, data, templating.DefaultLimits())
		Expect(e.Code).To(Equal("template-panic"))
		Expect(e.Reason).To(HavePrefix("the template engine failed: syntax error in identifier a?aaa"))
		Expect(len(e.Reason)).To(BeNumerically("<=", len("the template engine failed: ")+512+len("…")))
	})
})
