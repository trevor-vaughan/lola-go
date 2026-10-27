package templating_test

import (
	"context"
	"errors"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/templating"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// partials is a module's partials, by file name in _partials/.
func partials(files map[string]string) templating.Partials {
	p := templating.Partials{Files: map[string]string{}}
	for name, body := range files {
		p.Files["_partials/"+name] = body
	}
	return p
}

var _ = Describe("partials", func() {
	var data templating.Data
	BeforeEach(func() {
		testenv.Isolate()
		data = templating.Data{Target: "claude-code", Scope: "project", Profiles: []string{"default"},
			Vars: map[string]any{"lang": "go"}, Attrs: map[string]any{"model": "sonnet"}}
	})
	render := func(body string, p templating.Partials, lim templating.Limits) (string, error) {
		out, err := templating.Render(context.Background(), source(body), p, data, lim, []string{"lang"})
		return string(out), err
	}
	refused := func(body string, p templating.Partials, lim templating.Limits) *templating.Error {
		GinkgoHelper()
		_, err := render(body, p, lim)
		e, ok := errors.AsType[*templating.Error](err)
		Expect(ok).To(BeTrue(), "%v", err)
		return e
	}

	It("a partial resolves per target, then generic, else template-include-missing naming both paths", func() {
		p := partials(map[string]string{"header.liquid": "generic for {{ target }}", "header.claude-code.liquid": "claude for {{ target }}"})
		for target, want := range map[string]string{"claude-code": "claude for claude-code", "opencode": "generic for opencode"} {
			data.Target = target
			out, err := render("{% include 'header' %}", p, templating.DefaultLimits())
			Expect(err).NotTo(HaveOccurred(), target)
			Expect(out).To(Equal(want), target)
		}

		data.Target = "claude-code"
		p = partials(map[string]string{"header.opencode.liquid": "only opencode"})
		e := refused("a\n\n\n{% include 'header' %}", p, templating.DefaultLimits())
		Expect(e.Code).To(Equal("template-include-missing"))
		Expect(e.Error()).To(Equal("skills/review/SKILL.md.liquid:4: no partial header for claude-code " +
			"(looked for _partials/header.claude-code.liquid, _partials/header.liquid)"))
		out, err := render("{% if false %}{% include 'header' %}{% endif %}ok", p, templating.DefaultLimits())
		Expect(err).NotTo(HaveOccurred(), "a missing partial refuses only when a rendering include asks for it")
		Expect(out).To(Equal("ok"))
	})

	It("include cycles, chains over 8 and the 65th read refuse; a loop of 64 includes renders", func() {
		e := refused("{% include 'a' %}", partials(map[string]string{"a.liquid": "a{% include 'b' %}", "b.liquid": "b\n\n{% include 'a' %}"}), templating.DefaultLimits())
		Expect(e.Code).To(Equal("template-include-cycle"))
		Expect(e.Error()).To(Equal("_partials/b.liquid:3: includes a, which includes it again (a → b → a) (included from _partials/a.liquid:1)"))
		e = refused("{% include 'a' %}", partials(map[string]string{"a.liquid": "{% include 'a' %}"}), templating.DefaultLimits())
		Expect(e.Error()).To(Equal("_partials/a.liquid:1: includes a, which includes it again (a → a) (included from skills/review/SKILL.md.liquid:1)"))

		chain := map[string]string{"p9.liquid": "9"}
		for i := 1; i <= 8; i++ {
			chain[fmt.Sprintf("p%d.liquid", i)] = fmt.Sprintf("%d{%% include 'p%d' %%}", i, i+1)
		}
		e = refused("{% include 'p1' %}", partials(chain), templating.DefaultLimits())
		Expect(e.Code).To(Equal("template-include-too-deep"))
		Expect(e.Error()).To(Equal("_partials/p8.liquid:1: includes p9, which makes a chain of 9 partials " +
			"(p1 → p2 → p3 → p4 → p5 → p6 → p7 → p8 → p9); partials nest at most 8 deep (included from _partials/p7.liquid:1)"))
		out, err := render("{% include 'p2' %}", partials(chain), templating.DefaultLimits())
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(Equal("23456789"), "a chain of 8 partials renders")
		chain["p8.liquid"], chain["p9.liquid"] = "8{% if false %}{% include 'p9' %}{% endif %}", ""
		delete(chain, "p9.liquid")
		out, err = render("{% include 'p1' %}", partials(chain), templating.DefaultLimits())
		Expect(err).NotTo(HaveOccurred(), "a missing leaf is in no chain")
		Expect(out).To(Equal("12345678"))
		out, err = render("{% include 'a' %}", partials(map[string]string{"a.liquid": "a{% if false %}{% include 'm' %}{% include 'a2' %}{% endif %}",
			"a2.liquid": "{% include 'm' %}"}), templating.DefaultLimits())
		Expect(err).NotTo(HaveOccurred(), "nor in a cycle")
		Expect(out).To(Equal("a"))

		p := partials(map[string]string{"x.liquid": "x"})
		data.Profiles = names(64)
		out, err = render("{% for i in profiles %}{% include 'x' %}{% endfor %}", p, templating.DefaultLimits())
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(Equal(strings.Repeat("x", 64)))
		data.Profiles = names(65)
		e = refused("\n{% for i in profiles %}{% include 'x' %}{% endfor %}", p, templating.DefaultLimits())
		Expect(*e).To(Equal(templating.Error{Code: "template-include-limit", File: file, Line: 2,
			Reason: "more than 64 includes in one render; lola reads at most 64 partials per file"}))
	})

	It("a partial is screened as a template: capture, ranges, and its assigns count toward 256", func() {
		for body, want := range map[string]string{
			"a\n{% capture x %}{% endcapture %}": "_partials/x.liquid:2: the capture tag is not available in lola templates",
			"{{ (1..3) }}":                       "_partials/x.liquid:1: ranges like (1..n) are not available in lola templates; loop over a list",
			"\n{{ vars.nope }}":                  "_partials/x.liquid:2: vars.nope is not declared; review-kit declares lang",
			"{% include h %}":                    "_partials/x.liquid:1: include takes a partial name in quotes, such as {% include 'header' %}",
		} {
			e := refused("{% include 'x' %}", partials(map[string]string{"x.liquid": body}), templating.DefaultLimits())
			Expect(e.Error()).To(Equal(want+" (included from skills/review/SKILL.md.liquid:1)"), body)
		}

		lim := templating.DefaultLimits()
		lim.Assigns = 3
		p := partials(map[string]string{"x.liquid": "{% assign b = 1 %}\n{% assign c = 1 %}"})
		e := refused("{% assign a = 1 %}{% assign z = 1 %}{% include 'x' %}{% include 'x' %}", p, lim)
		Expect(e.Code).To(Equal("template-too-many-assigns"))
		Expect(e.Error()).To(Equal("_partials/x.liquid:2: more than 3 assign tags; a template and its partials have at most 3 (included from skills/review/SKILL.md.liquid:1)"))
		_, err := render("{% include 'x' %}{% include 'x' %}", p, lim)
		Expect(err).NotTo(HaveOccurred(), "a partial's assign tags count once, however often it is included")

		lim = templating.DefaultLimits()
		lim.Depth = 3
		p = partials(map[string]string{"x.liquid": "{% if true %}a{% endif %}\n{% if true %}{% if true %}b{% endif %}{% endif %}"})
		e = refused("{% if true %}{% if true %}{% include 'x' %}{% endif %}{% endif %}", p, lim)
		Expect(*e).To(Equal(templating.Error{Code: "template-too-deep", File: "_partials/x.liquid", Line: 2, Reason: "blocks nest more than 3 deep",
			IncludedFrom: "skills/review/SKILL.md.liquid:1"}))
		out, err := render("{% if true %}{% include 'x' %}{% endif %}", p, lim)
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(Equal("a\nb"))

		By("a {{ }} of a partial rendered from frontmatter refuses a line break without json, as the frontmatter does")
		p = partials(map[string]string{"fm.liquid": "model: {{ attrs.model | json }}\n{% include 'inner' %}", "inner.liquid": "{% assign v = \"x\ny\" %}\nmode: {{ v }}\n",
			"body.liquid": "{% assign v = \"x\ny\" %}{{ target }}"})
		doc := source("---\n{% include 'fm' %}\n---\n{% include 'body' %}\n")
		doc.Frontmatter = true
		_, err = templating.RenderDoc(context.Background(), doc, p, data, templating.DefaultLimits(), []string{"lang"}, module.PlainRead)
		e, _ = errors.AsType[*templating.Error](err)
		Expect(e).NotTo(BeNil(), "%v", err)
		Expect(e.Code).To(Equal("template-frontmatter-value"))
		Expect(e.Error()).To(Equal("_partials/inner.liquid:3: in frontmatter, this {{ }} rendered a line break; end it with | json so the value stays one YAML value: {{ v | json }} (included from _partials/fm.liquid:2)"))
		doc.Body = "---\nk: 1\n---\n{% include 'body' %}\n"
		r, err := templating.RenderDoc(context.Background(), doc, p, data, templating.DefaultLimits(), []string{"lang"}, module.PlainRead)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(r.Body)).To(Equal("claude-code\n"), "a partial included only in the body is not checked")
	})

	It("an error in a partial names the partial's file and line and the include site", func() {
		p := partials(map[string]string{"header.claude-code.liquid": "Header\n{{ titel }}\n", "header.liquid": "generic"})
		e := refused("a\nb\nc\n{% include 'header' %}", p, templating.DefaultLimits())
		Expect(*e).To(Equal(templating.Error{Code: "template-undefined-variable", File: "_partials/header.claude-code.liquid", Line: 2,
			Reason: "undefined variable in {{ titel }}", IncludedFrom: "skills/review/SKILL.md.liquid:4"}))
		Expect(e.Error()).To(Equal("_partials/header.claude-code.liquid:2: undefined variable in {{ titel }} (included from skills/review/SKILL.md.liquid:4)"))

		for body, want := range map[string]templating.Error{
			"{% assign x = %}": {Code: "template-syntax", File: "_partials/b.liquid", Line: 1, Reason: "syntax error in \"%assign x =\""},
			"\n{{ 'x' | truncate: 1, 2, 3 }}": {Code: "template-syntax", File: "_partials/b.liquid", Line: 2,
				Reason: "truncate takes at most 2 arguments; it got 3"},
			"{% for i in profiles %}{% include 'x' %}{% endfor %}": {Code: "template-include-limit", File: "_partials/b.liquid", Line: 1,
				Reason: "more than 64 includes in one render; lola reads at most 64 partials per file"},
		} {
			data.Profiles = names(65)
			p := partials(map[string]string{"a.liquid": "a\n{% include 'b' %}", "b.liquid": body, "x.liquid": "x"})
			e := refused("{% include 'a' %}", p, templating.DefaultLimits())
			want.IncludedFrom = "_partials/a.liquid:2"
			Expect(*e).To(Equal(want), body)
		}

		p = partials(map[string]string{"x.liquid": "x"})
		e = refused("{% include 'x' %}\n{{ nope }}", p, templating.DefaultLimits())
		Expect(*e).To(Equal(templating.Error{Code: "template-undefined-variable", File: file, Line: 2, Reason: "undefined variable in {{ nope }}"}),
			"an error in the template after an include is the template's")

		p = partials(map[string]string{"h.liquid": "\n{{ nope }}"})
		e = refused("{% if false %}{% include 'h' %}{% endif %}\n{% include 'h' %}", p, templating.DefaultLimits())
		Expect(e.IncludedFrom).To(Equal("skills/review/SKILL.md.liquid:1"), "the first include site in the graph, which the render never reached")
	})

	It("a missing partial refuses only when an include that renders asks for it", func() {
		p := partials(map[string]string{"a.liquid": "a\n{% if vars.lang == 'rust' %}{% include 'gone' %}{% endif %}", "x.liquid": "x"})
		out, err := render("{% if false %}{% include 'gone' %}{% endif %}{% include 'a' %}", p, templating.DefaultLimits())
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(Equal("a\n"))

		e := refused("one\n{% if true %}{% include 'gone' %}{% endif %}", p, templating.DefaultLimits())
		Expect(*e).To(Equal(templating.Error{Code: "template-include-missing", File: file, Line: 2,
			Reason: "no partial gone for claude-code (looked for _partials/gone.claude-code.liquid, _partials/gone.liquid)"}), "at the include tag, naming both paths")

		data.Vars = map[string]any{"lang": "rust"}
		e = refused("{% include 'x' %}\n{% include 'a' %}", p, templating.DefaultLimits())
		Expect(*e).To(Equal(templating.Error{Code: "template-include-missing", File: "_partials/a.liquid", Line: 2,
			Reason: "no partial gone for claude-code (looked for _partials/gone.claude-code.liquid, _partials/gone.liquid)", IncludedFrom: file + ":2"}),
			"a nested missing leaf names its includer's site")

		lim := templating.DefaultLimits()
		lim.Reads = 3
		data.Profiles = names(4)
		e = refused("{% for i in profiles %}{% include 'x' %}{% endfor %}{% include 'gone' %}", p, lim)
		Expect(e.Code).To(Equal("template-include-limit"), "an include in a loop counts a read per iteration")
		data.Profiles = names(3)
		e = refused("{% for i in profiles %}{% include 'x' %}{% endfor %}{% include 'gone' %}", p, lim)
		Expect(e.Code).To(Equal("template-include-limit"), "the missing partial's read counts too")

		By("cycles and depth stay static with a missing sibling")
		e = refused("{% if false %}{% include 'c1' %}{% endif %}", partials(map[string]string{"c1.liquid": "{% include 'gone' %}{% include 'c2' %}",
			"c2.liquid": "{% include 'c1' %}"}), templating.DefaultLimits())
		Expect(e.Code).To(Equal("template-include-cycle"))
		lim = templating.DefaultLimits()
		lim.Depth = 2
		e = refused("{% if false %}{% include 'gone' %}{% include 'd' %}{% endif %}", partials(map[string]string{"d.liquid": "{% if true %}{% if true %}{% endif %}{% endif %}"}), lim)
		Expect(e.Code).To(Equal("template-too-deep"))
	})

	It("the frontmatter and body of one file share one read count", func() {
		p := partials(map[string]string{"a.liquid": "x"})
		doc := source("---\n{% include 'a' %}\n---\n{% include 'a' %}\n{% include 'a' %}\n")
		doc.Frontmatter = true
		lim := templating.DefaultLimits()
		lim.Reads = 3
		r, err := templating.RenderDoc(context.Background(), doc, p, data, lim, nil, module.PlainRead)
		Expect(err).NotTo(HaveOccurred(), "three reads in all")
		Expect(string(r.Front) + "|" + string(r.Body)).To(Equal("x\n|x\nx\n"))

		lim.Reads = 2
		_, err = templating.RenderDoc(context.Background(), doc, p, data, lim, nil, module.PlainRead)
		e, ok := errors.AsType[*templating.Error](err)
		Expect(ok).To(BeTrue(), "%v", err)
		Expect(*e).To(Equal(templating.Error{Code: "template-include-limit", File: file, Line: 5,
			Reason: "more than 2 includes in one render; lola reads at most 2 partials per file"}),
			"each region alone reads at most 2; the body's second read is the file's third")
	})

	It("include takes only a literal partial name; render stays unavailable", func() {
		p := partials(map[string]string{"header.liquid": "h"})
		const wantName = "include takes a partial name in quotes, such as {% include 'header' %}"
		for _, body := range []string{
			"{% include header %}", "{% include 'header' with x %}", "{% include 'a/header' %}", "{% include 'Header' %}",
			"{% include '' %}", "{% include 'header' | append: 'x' %}", "{% include %}", "{% include 'header\" %}",
			"{% include '_x' %}", "{% include target %}", "{% include 'header' 'header' %}",
		} {
			e := refused("x\n"+body, p, templating.DefaultLimits())
			Expect(*e).To(Equal(templating.Error{Code: "template-include-name", File: file, Line: 2, Reason: wantName}), body)
		}
		out, err := render(`{% include "header" %}{%- include 'header' -%}{% include	'header'	%}`, p, templating.DefaultLimits())
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(Equal("hhh"))

		e := refused("{% render 'header' %}", p, templating.DefaultLimits())
		Expect(*e).To(Equal(templating.Error{Code: "template-tag-unavailable", File: file, Line: 1,
			Reason: "the render tag is not available in lola templates; use include"}))
		for _, body := range []string{"{% include '../header' %}", "{% include 'a..b' %}"} {
			e = refused(body, p, templating.DefaultLimits())
			Expect(*e).To(Equal(templating.Error{Code: "template-include-name", File: file, Line: 1, Reason: wantName}), "a path is a bad name, not a range: "+body)
		}
	})

	It("a partial's assign does not leak to the includer, and the partial sees target, vars and attrs", func() {
		p := partials(map[string]string{"p.liquid": "{{ x }}|{% assign x = 'inner' %}{% assign y = 1 %}{{ x }}|{{ target }}|{{ vars.lang }}|{{ attrs.model }}|{{ profiles[0] }}|"})
		out, err := render("{% assign x = 'outer' %}{% include 'p' %}{{ x }}", p, templating.DefaultLimits())
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(Equal("outer|inner|claude-code|go|sonnet|default|outer"))
		e := refused("{% assign x = 'outer' %}{% include 'p' %}\n{{ y }}", p, templating.DefaultLimits())
		Expect(e.Code).To(Equal("template-undefined-variable"))
		Expect(e.Reason).To(Equal("undefined variable in {{ y }}"))
	})

	It("Uses reads the partials a template includes for any target", func() {
		p := partials(map[string]string{"a.liquid": "{% include 'b' %}", "b.opencode.liquid": "{{ profiles | join: ',' }}", "b.liquid": "b", "c.liquid": "{{ target }}"})
		Expect(templating.Uses([]templating.Source{source("{% include 'a' %}")}, p, "profiles")).To(BeTrue(), "a partial for any target that reads it")
		Expect(templating.Uses([]templating.Source{source("{% include 'c' %}")}, p, "profiles")).To(BeFalse())
		Expect(templating.Uses([]templating.Source{source("{% raw %}{% include 'a' %}{% endraw %}")}, p, "profiles")).To(BeFalse())
	})
})
