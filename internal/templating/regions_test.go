package templating_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/templating"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("frontmatter and body regions", func() {
	var data templating.Data
	BeforeEach(func() {
		testenv.Isolate()
		data = templating.Data{Target: "claude-code", Scope: "project", Vars: map[string]any{"lang": "go"}}
	})
	component := func(body string) templating.Source {
		src := source(body)
		src.Frontmatter = true
		return src
	}
	renderDoc := func(src templating.Source, lim templating.Limits) (templating.Rendered, error) {
		return templating.RenderDoc(context.Background(), src, templating.Partials{}, data, lim, []string{"lang"}, module.PlainRead)
	}
	refusedDoc := func(src templating.Source, lim templating.Limits) *templating.Error {
		_, err := renderDoc(src, lim)
		e, ok := errors.AsType[*templating.Error](err)
		Expect(ok).To(BeTrue(), "%v", err)
		return e
	}

	It("renders the frontmatter and the body as two parts of one file", func() {
		r, err := renderDoc(component("---\nt: {{ target | json }}\n---\nbody {{ scope }}\n"), templating.DefaultLimits())
		Expect(err).NotTo(HaveOccurred())
		Expect(r).To(Equal(templating.Rendered{Front: []byte("t: \"claude-code\"\n"), HasFront: true, Body: []byte("body project\n")}))

		r, err = renderDoc(component("\ufeff---\r\nt: {{ scope | json }}\r\n---\r\nb"), templating.DefaultLimits())
		Expect(err).NotTo(HaveOccurred())
		Expect(r).To(Equal(templating.Rendered{Front: []byte("t: \"project\"\r\n"), HasFront: true, Body: []byte("b")}))

		r, err = renderDoc(component("no frontmatter {{ target }}"), templating.DefaultLimits())
		Expect(err).NotTo(HaveOccurred())
		Expect(r).To(Equal(templating.Rendered{Body: []byte("no frontmatter claude-code")}))

		src := component("---\nt: {{ target }}\n---\nb")
		src.Frontmatter = false
		r, err = renderDoc(src, templating.DefaultLimits())
		Expect(err).NotTo(HaveOccurred())
		Expect(r).To(Equal(templating.Rendered{Body: []byte("---\nt: claude-code\n---\nb")}), "a support file has no regions")

		out, err := templating.Render(context.Background(), component("---\nt: {{ target | json }}\n---\nb"), templating.Partials{}, data, templating.DefaultLimits(), nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(out)).To(Equal("---\nt: \"claude-code\"\n---\nb"), "Render renders a component's file whole, as one region")
	})

	It("the closing fence must be literal: none, or a block open across it, is template-frontmatter-fence", func() {
		lim := templating.DefaultLimits()
		for body, msg := range map[string]string{
			"---\nt: 1\n": file + ":1: frontmatter opens with --- but no literal --- line closes it outside a tag",
			"---\nt: 1\n-{% if target %}--{% endif %}\nb":       file + ":1: frontmatter opens with --- but no literal --- line closes it outside a tag",
			"---\nt: 1\n{{ '---' }}\nb":                         file + ":1: frontmatter opens with --- but no literal --- line closes it outside a tag",
			"---\n{% raw %}\n---\n{% endraw %}\nb":              file + ":1: frontmatter opens with --- but no literal --- line closes it outside a tag",
			"---\n{% comment %}\n---\n{% endcomment %}\nb":      file + ":1: frontmatter opens with --- but no literal --- line closes it outside a tag",
			"---\n{% if target %}\nt: 1\n---\n{% endif %}\nb":   file + ":4: the if opened on line 2 is still open at the closing --- on line 4; close it before the fence",
			"---\n{% for p in profiles %}{% unless p %}\n---\n": file + ":3: the unless opened on line 2 is still open at the closing --- on line 3; close it before the fence",
			"---\nt: {% if target %}1{% endif %}---\nb":         file + ":1: frontmatter opens with --- but no literal --- line closes it outside a tag",
		} {
			e := refusedDoc(component(body), lim)
			Expect(e.Code).To(Equal("template-frontmatter-fence"), body)
			Expect(e.Error()).To(Equal(msg), body)
		}
		r, err := renderDoc(component("---\n{% if target %}t: 1{% endif %}\n---\n---\n"), lim)
		Expect(err).NotTo(HaveOccurred())
		Expect(r).To(Equal(templating.Rendered{Front: []byte("t: 1\n"), HasFront: true, Body: []byte("---\n")}), "the first literal fence closes it")
	})

	It("a frontmatter {{ }} refuses only when its rendered output holds a YAML line break and it does not end in json", func() {
		data.Attrs = map[string]any{
			"plain": "fine", "nl": "a\nb", "cr": "a\rb", "nel": "a\u0085b", "ls": "a\u2028permission: {bash: allow}", "ps": "a\u2029b",
			"list": []any{"ok", "x\ny"}, "map": map[string]any{"k": "v\nw"}, "key": map[string]any{"k\nx": "v"}, "n": int64(3),
		}
		one := func(markup string) string {
			return "in frontmatter, this {{ }} rendered a line break; end it with | json so the value stays one YAML value: {{ " + markup + " | json }}"
		}
		for body, msg := range map[string]string{
			"---\nm: {{ attrs.nl }}\n---\n":                                     file + ":2: " + one("attrs.nl"),
			"---\nm: {{ attrs.cr }}\n---\n":                                     file + ":2: " + one("attrs.cr"),
			"---\nm: {{ attrs.nel }}\n---\n":                                    file + ":2: " + one("attrs.nel"),
			"---\nm: {{ attrs.ls }}\n---\n":                                     file + ":2: " + one("attrs.ls"),
			"---\nm: {{ attrs.ps }}\n---\n":                                     file + ":2: " + one("attrs.ps"),
			"---\nm: {{ attrs.list }}\n---\n":                                   file + ":2: " + one("attrs.list"),
			"---\nm: {{ attrs.map }}\n---\n":                                    file + ":2: " + one("attrs.map"),
			"---\nm: {{ attrs.key }}\n---\n":                                    file + ":2: " + one("attrs.key"),
			"---\nm: {{- attrs.nl -}}\n---\n":                                   file + ":2: " + one("attrs.nl"),
			"---\nm: {{ attrs.nl | lola_frontmatter_value }}\n---\n":            file + ":2: " + one("attrs.nl"),
			"---\nm: {{ attrs.plain | json | append: attrs.nl }}\n---\n":        file + ":2: " + one("attrs.plain | json | append: attrs.nl"),
			"---\na: 1\n\n{% if true %}\nb: {{ attrs.nl }}\n{% endif %}\n---\n": file + ":5: " + one("attrs.nl"),
			"---\nm: {{ attrs.plain }} {{ attrs.nl }}\n---\n":                   file + ":2: in frontmatter, this {{ }} rendered a line break; end it with | json",
		} {
			e := refusedDoc(component(body), templating.DefaultLimits())
			Expect(e.Code).To(Equal("template-frontmatter-value"), body)
			Expect(e.Error()).To(Equal(msg), body)
		}
		r, err := renderDoc(component("---\na: {{ attrs.plain }}\nb: {{ attrs.n }} {{ attrs.list[0] }}\nc: {{ attrs.nl | json }}\n"+
			"d: {{ attrs.ls | json }}\ne: {{ attrs.nel|json }}\nf: {{ attrs.key | json }}\n"+
			"h: {{\n attrs.plain\n}}\ni: {{ attrs.plain }}\n---\n{{ attrs.nl }} {{ attrs.map }}"), templating.DefaultLimits())
		Expect(err).NotTo(HaveOccurred())
		Expect(string(r.Front)).To(Equal("a: fine\nb: 3 ok\nc: \"a\\nb\"\nd: \"a\\u2028permission: {bash: allow}\"\ne: \"a\\u0085b\"\n"+
			"f: {\"k\\nx\":\"v\"}\nh: fine\ni: fine\n"), "json escapes every break")
		Expect(string(r.Body)).To(Equal("a\nb map[k:v\nw]"), "the body region is a no-op")

		By("a partial rendered from frontmatter is checked; the same partial in the body is not")
		p := templating.Partials{Files: map[string]string{"_partials/v.liquid": "\n{{ attrs.nl }}"}}
		doc := component("---\n{% include 'v' %}\n---\n{% include 'v' %}\n")
		_, err = templating.RenderDoc(context.Background(), doc, p, data, templating.DefaultLimits(), []string{"lang"}, module.PlainRead)
		e, _ := errors.AsType[*templating.Error](err)
		Expect(e).NotTo(BeNil(), "%v", err)
		Expect(e.Error()).To(Equal("_partials/v.liquid:2: " + one("attrs.nl") + " (included from " + file + ":2)"))
		doc.Body = "---\nk: 1\n---\n{% include 'v' %}\n"
		r, err = templating.RenderDoc(context.Background(), doc, p, data, templating.DefaultLimits(), []string{"lang"}, module.PlainRead)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(r.Body)).To(Equal("\na\nb\n"))

		By("markup the appended filter cannot keep valid refuses at compile, in either region, as written and as before")
		p = templating.Partials{Files: map[string]string{}}
		for obj, quoted := range map[string]string{`{{ "a }}`: `"a`, "{{ target | }}": "target |", "{{ target | append: }}": "target | append:",
			"{{ target | default: 'a', allow_false: }}": "target | default: 'a', allow_false:", "{{ }}": " ", "{{ target target }}": "target target", "{{-}}": "-", "{{-}} {{ target }}": "}} {{ target", "{{ target target | json }}": "target target | json"} {
			for _, body := range []string{"---\nk: " + obj + "\n---\n", "---\nk: 1\n---\n" + obj} {
				e := refusedDoc(component(body), templating.DefaultLimits())
				Expect(e.Code).To(Equal("template-syntax"), body)
				Expect(e.Error()).To(HaveSuffix("syntax error in "+strconv.Quote(quoted)), body)
			}
			p.Files["_partials/bad.liquid"] = "\n" + obj
			_, err := templating.RenderDoc(context.Background(), component("---\n{% include 'bad' %}\n---\n"), p, data, templating.DefaultLimits(), []string{"lang"}, module.PlainRead)
			Expect(err).To(MatchError("_partials/bad.liquid:2: syntax error in "+strconv.Quote(quoted)+" (included from "+file+":2)"), obj)
		}
		for _, body := range []string{"---\nk: {{ targt }}\n---\n", "---\nk: {{ target | nope }}\n---\n", "---\nk: {{ attrs.nl | append: attrs.n | lola_frontmatter_value }}\n---\n"} {
			_, err := renderDoc(component(body), templating.DefaultLimits())
			Expect(err).To(HaveOccurred(), body)
			Expect(err.Error()).NotTo(ContainSubstring("| lola_frontmatter_value"), "no error shows the filter lola appends: %s", body)
		}
		r, err = renderDoc(component("---\nk: 1\n---\n[{{ '' }}]"), templating.DefaultLimits())
		Expect(err).NotTo(HaveOccurred())
		Expect(string(r.Body)).To(Equal("[]"))

		By("a string YAML would not read back as itself refuses unless its {{ }} ends with json (R-7-U18)")
		reads := func(value, what string) string {
			return "in frontmatter, YAML would read " + value + " as " + what + "; end that {{ }} with | json"
		}
		for value, what := range map[string]string{
			"sonnet #x": `a comment after "sonnet"`, "a: b": "a syntax error", "[a": "a syntax error", "{a": "a syntax error",
			"*a": "a syntax error", "&a x": `"x"`, "!a x": "a value tagged !a", "| x": "a syntax error", ">": `""`,
			"%x": "a syntax error", "@x": "a syntax error", "`x": "a syntax error", "- x": "a syntax error", "? x": "a syntax error",
			"123": "a number", "0x1F": "a number", "true": "true or false", "null": "null", "~": "null", "a  ": `"a"`, "": "null",
			"2024-01-01": "a date", "\x1b[31m: x": "a syntax error",
		} {
			data.Attrs = map[string]any{"v": value}
			quoted := strconv.Quote(value)
			for body, msg := range map[string]string{
				"---\nm: {{ attrs.v }}\n---\n":              file + ":2: " + reads(quoted, what),
				"---\na: 1\n{{ attrs.v }}: x\n---\n":        file + ":3: " + reads(quoted, what),
				"---\nm: {{ attrs.v | append: '' }}\n---\n": file + ":2: " + reads(quoted, what),
			} {
				e := refusedDoc(component(body), templating.DefaultLimits())
				Expect(e.Code).To(Equal("template-frontmatter-value"), "%q %s", value, body)
				Expect(e.Error()).To(Equal(msg), "%q %s", value, body)
			}
			r, err := renderDoc(component("---\nm: {{ attrs.v | json }}\n---\n{{ attrs.v }}"), templating.DefaultLimits())
			Expect(err).NotTo(HaveOccurred(), "json and the body pass: %q", value)
			Expect(string(r.Body)).To(Equal(value))
		}
		data.Attrs = map[string]any{"v": "sonnet #x"}
		p = templating.Partials{Files: map[string]string{"_partials/m.liquid": "\nm: {{ attrs.v }}"}}
		_, err = templating.RenderDoc(context.Background(), component("---\n{% include 'm' %}\n---\n"), p, data, templating.DefaultLimits(), []string{"lang"}, module.PlainRead)
		Expect(err).To(MatchError("_partials/m.liquid:2: " + reads(`"sonnet #x"`, `a comment after "sonnet"`) + " (included from " + file + ":2)"))
		Expect(refusedDoc(component("---\nm: {{ attrs.v | json | upcase }}\n---\n"), templating.DefaultLimits()).Error()).
			To(Equal(file+":2: "+reads(`"\"SONNET #X\""`, `"SONNET #X"`)), "a filter after json is checked on its own output")
		Expect(refusedDoc(component("---\nm: {{ attrs.v | lola_frontmatter_value }}\n---\n"), templating.DefaultLimits()).Error()).
			To(Equal(file+":2: "+reads(`"sonnet #x"`, `a comment after "sonnet"`)), "a template naming the filter gets the same check")
		data.Attrs = map[string]any{"v": strings.Repeat("a ", 200)}
		Expect(refusedDoc(component("---\nm: {{ attrs.v }}\n---\n"), templating.DefaultLimits()).Error()).
			To(HaveSuffix("…; end that {{ }} with | json"), "the value is capped")

		By("plain values, numbers and booleans render as written")
		for _, value := range []any{"sonnet", "opus-4", "claude 3", "path/x", "a-b", "a#b", int64(123), 0.5, true, false} {
			data.Attrs = map[string]any{"model": value}
			r, err := renderDoc(component("---\nmodel: {{ attrs.model }}\n{{ attrs.model }}: x\n---\n"), templating.DefaultLimits())
			Expect(err).NotTo(HaveOccurred(), "%v", value)
			Expect(string(r.Front)).To(Equal(fmt.Sprintf("model: %v\n%v: x\n", value, value)))
		}

		By("only a {{ }} that is a line's whole value or whole key is round-trip checked (R-7-57)")
		vars := func(v map[string]any, body string) (templating.Rendered, error) {
			d := data
			d.Vars = v
			return templating.RenderDoc(context.Background(), component(body), templating.Partials{}, d, templating.DefaultLimits(), []string{"ver", "d"}, module.PlainRead)
		}
		for body, want := range map[string]string{
			"---\nversion: v{{ vars.ver }}\n---\n":      "version: v4\n",
			"---\ndescription: \"{{ vars.d }}\"\n---\n": "description: \"a #b\"\n",
			"---\nx: Review {{ vars.d }}\n---\n":        "x: Review a #b\n",
			"---\n- {{ vars.ver }}x\n---\n":             "- 4x\n",
		} {
			r, err := vars(map[string]any{"ver": "4", "d": "a #b"}, body)
			Expect(err).NotTo(HaveOccurred(), body)
			Expect(string(r.Front)).To(Equal(want), body)
		}
		for body, line := range map[string]string{
			"---\ndescription: {{ vars.d }}\n---\n": "2",
			"---\n- {{ vars.d }}  \n---\n":          "2",
			"---\nl:\n  - k: {{ vars.d }}\r\n---\n": "3",
			"---\nk: 1\n  {{ vars.d }}: v\n---\n":   "3",
			"---\nh: {{\n vars.d\n}}\n---\n":        "2",
		} {
			_, err := vars(map[string]any{"ver": "4", "d": "Reviews #security"}, body)
			Expect(err).To(MatchError(file+":"+line+": "+reads(`"Reviews #security"`, `a comment after "Reviews"`)), body)
		}
	})

	It("forloop is refused before compile unless read through a property, with its line", func() {
		loop := func(inner string) string { return "{% for p in profiles %}" + inner + "{% endfor %}" }
		for body, line := range map[string]int{
			loop("\n{% for kv in forloop %}{% endfor %}"):                   2,
			loop("{%for kv in  forloop limit:2%}{% endfor %}"):              1,
			loop("\n\n{{ forloop }}"):                                       3,
			loop("{{- \tforloop\n -}}"):                                     1,
			loop("{% assign f = forloop %}{{ f }}"):                         1,
			loop("{{ 'a' | append: forloop }}"):                             1,
			loop("{{ 'a' | replace: 'a', forloop }}"):                       1,
			loop("{{ forloop | json }}"):                                    1,
			loop("{% if forloop == x %}{% endif %}"):                        1,
			loop("{{ forloop['.cycles'] }}"):                                1,
			loop("\n{{ forloop[ \".cycles\" ] | size }}"):                   2,
			loop(`{% assign k = "." | append: "cycles" %}{{ forloop[k] }}`): 1,
			loop("{{ forloop[profiles[0]] }}"):                              1,
			loop("{{ forloop['first' | append: ''] }}"):                     1,
		} {
			e := refusal(source(body), data, templating.DefaultLimits())
			Expect(*e).To(Equal(templating.Error{Code: "template-syntax", File: file, Line: line,
				Reason: "forloop can only be read through one of its properties, like forloop.index"}), body)
		}
		out, err := templating.Render(context.Background(), source(loop("{{ forloop.index }}{{ forloop [ 'first' ] }}{{ 'forloop' }}{% if forloop.last %}!{% endif %}")), templating.Partials{},
			templating.Data{Profiles: []string{"a"}, Vars: map[string]any{}}, templating.DefaultLimits(), nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(out)).To(Equal("1trueforloop!"), "its properties read as before")
	})

	It("assign and for cannot rebind a lola binding", func() {
		for body, msg := range map[string]string{
			"x\n{% assign vars = 'x' %}{{ vars }}":        file + ":2: assign cannot set vars; it is lola's",
			"{% assign attrs = 1 %}":                      file + ":1: assign cannot set attrs; it is lola's",
			"\n\n{% for attrs in profiles %}{% endfor %}": file + ":3: for cannot name its variable attrs; it is lola's",
			"{% for vars in profiles %}{% endfor %}":      file + ":1: for cannot name its variable vars; it is lola's",
		} {
			e := refusal(source(body), data, templating.DefaultLimits())
			Expect(e.Code).To(Equal("template-syntax"), body)
			Expect(e.Error()).To(Equal(msg), body)
		}
		out, err := templating.Render(context.Background(), source("{% assign varsx = 1 %}{% for attrsy in profiles %}{% endfor %}{{ varsx }}"), templating.Partials{},
			data, templating.DefaultLimits(), nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(out)).To(Equal("1"))
	})

	It("tells whether a template reads a binding", func() {
		for body, uses := range map[string]bool{
			"{{ profiles | join: ',' }}":                   true,
			"{% for p in profiles %}{% endfor %}":          true,
			"{% if profiles contains 'work' %}{% endif %}": true,
			"---\nk: {{ profiles | json }}\n---\n":         true,
			"{{ 'profiles' }}{{ vars.profiles }}profiles":  false,
			"{% raw %}{{ profiles }}{% endraw %}":          false,
			"{% comment %}{{ profiles }}{% endcomment %}":  false,
			"{{ profiles_x }}":                             false,
			"{% assign profiles = 1 %}":                    false,
			"{% for profiles in vars.x %}{% endfor %}":     false,
			"{% assign x = profiles %}":                    true,
			"{% for p in profiles %}{% endfor %}x":         true,
		} {
			Expect(templating.Uses([]templating.Source{source(body)}, templating.Partials{}, "profiles")).To(Equal(uses), body)
		}
	})
})
