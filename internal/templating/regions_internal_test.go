package templating

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("regions of one file", func() {
	const file = "skills/review/SKILL.md.liquid"
	data := Data{Target: "claude-code", Scope: "project", Vars: map[string]any{}}
	component := func(body string) Source {
		return Source{Module: "review-kit", File: file, Line: 1, Body: body, Frontmatter: true}
	}
	refused := func(src Source, lim Limits, extra map[string]any) *Error {
		_, err := renderWith(context.Background(), src, Partials{}, data, lim, nil, nil, extra)
		e, ok := errors.AsType[*Error](err)
		Expect(ok).To(BeTrue(), "%v", err)
		return e
	}
	BeforeEach(func() { testenv.Isolate() })

	It("a frontmatter and a body region keep the file's line numbers, deadline, output cap and assign budget, and an assign does not cross them", func() {
		lim := DefaultLimits()
		Expect(refused(component("---\na: 1\nb: {{ targt | json }}\n---\nx\n{{ scope }}{{ targt }}"), lim, nil).Error()).
			To(Equal(file + ":3: undefined variable in {{ targt | json }}"))
		Expect(refused(component("---\na: 1\n---\nx\n{{ targt }}"), lim, nil).Error()).To(Equal(file + ":5: undefined variable in {{ targt }}"))
		Expect(refused(component("---\n{% assign x = 'a' %}k: {{ x | json }}\n---\n{{ x }}"), lim, nil).Error()).
			To(Equal(file+":4: undefined variable in {{ x }}"), "the body does not see the frontmatter's assign")

		few := lim
		few.Assigns = 2
		Expect(refused(component("---\n{% assign a = 1 %}\n---\n{% assign b = 1 %}\n{% assign c = 1 %}"), few, nil).Error()).
			To(Equal(file + ":5: more than 2 assign tags; a template and its partials have at most 2"))

		small := lim
		small.Output = 10
		_, err := renderWith(context.Background(), component("---\na: {{ 'x' | json }}\n---\nyyy"), Partials{}, data, small, nil, nil, nil)
		Expect(err).NotTo(HaveOccurred(), "7 + 3 bytes")
		Expect(refused(component("---\na: {{ 'x' | json }}\n---\nyyyy"), small, nil).Error()).
			To(Equal(file + ": renders more than 10 bytes; lola writes at most 10 bytes per file"))

		// One deadline: each region alone takes 60% of it and renders; the two together pass it.
		slow := lim
		slow.Timeout = 500 * time.Millisecond
		nap := map[string]any{"nap": func(in any) any { time.Sleep(300 * time.Millisecond); return in }}
		for _, body := range []string{"---\na: {{ 1 | nap | json }}\n---\nb", "---\na: 1\n---\n{{ 1 | nap }}"} {
			_, err := renderWith(context.Background(), component(body), Partials{}, data, slow, nil, nil, nap)
			Expect(err).NotTo(HaveOccurred(), body)
		}
		start := time.Now()
		e := refused(component("---\na: {{ 1 | nap | json }}\n---\n{{ 1 | nap }}"), slow, nap)
		Expect(e.Error()).To(Equal(file + ": took longer than 500ms to render; lola stops a template after 500ms"))
		Expect(time.Since(start)).To(BeNumerically("<", slow.Timeout+time.Second))
	})
})

var _ = Describe("the frontmatter value check's rewrite", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("a frontmatter {{ }} refuses only when its rendered output holds a YAML line break and it does not end in json", func() {
		By("{{-}} keeps its lone - as markup, and the tokens after it keep their own check")
		Expect(withValueCheck(Source{File: "a.md.liquid", Line: 1, Body: "k: {{-}} v\nw: {{ x }}\n"})).
			To(Equal("k: {{ - | " + frontBreakFilter + "}} v\nw: {{ x | " + frontValueFilter + " }}\n"))

		By("only a line's whole value or whole key gets the round-trip check (R-7-57)")
		for body, filter := range map[string]string{
			"k: {{ x }}\n": frontValueFilter, "  - {{ x }} \r\n": frontValueFilter, "- k: {{ x }}": frontValueFilter, "{{ x }}: v\n": frontValueFilter,
			"  {{ x }}:\n": frontValueFilter, "k: {{ x | json }}\n": frontBreakFilter, "k: v{{ x }}\n": frontBreakFilter,
			"k: \"{{ x }}\"\n": frontBreakFilter, "k: {{ x }} # c\n": frontBreakFilter, "{{ x }}\n": frontBreakFilter,
			"k: {% if a %}{{ x }}{% endif %}\n": frontBreakFilter, "{{ x }}-y: v\n": frontBreakFilter, "# k: {{ x }}\n": frontBreakFilter,
		} {
			Expect(withValueCheck(Source{File: "a.md.liquid", Line: 1, Body: body})).To(ContainSubstring("| "+filter+" }}"), "%q", body)
		}
		Expect(withValueCheck(Source{File: "a.md.liquid", Line: 1, Body: "k: {{-}} {{ x }}\n"})).
			To(Equal("k: {{-}} {{ x | "+frontValueFilter+" }}\n"), "liquid's scanner reads {{-}} {{ x }} as one token, whose markup is }} {{ x")
	})
})

var _ = Describe("bounded work", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("the frontmatter value check visits each of a value's strings once", func() {
		value := []any{"a", map[string]any{"k1": "b", "k2": []any{"c", "d"}}, int64(1), nil, true, 2.5, []any{map[string]any{}}}
		visits := 0
		Expect(eachString(value, func(string) bool { visits++; return true })).To(BeTrue())
		Expect(visits).To(Equal(6), "a, k1, b, k2, c, d")
		visits = 0
		Expect(eachString([]any{"x\ny", "z"}, func(s string) bool { visits++; return !strings.ContainsAny(s, yamlBreaks) })).To(BeFalse())
		Expect(visits).To(Equal(1), "it stops at the first break")
	})

	It("Uses walks each partial at most once per module, however often it is included", func() {
		var all strings.Builder
		for i := range 32 {
			fmt.Fprintf(&all, "{%% include 'p%d' %%}{%% include 'p%d' %%}", i, i)
		}
		p := Partials{Files: map[string]string{"_partials/unused.liquid": "{{ profiles }}"}}
		for i := range 32 {
			p.Files[fmt.Sprintf("_partials/p%d.liquid", i)] = all.String()
			p.Files[fmt.Sprintf("_partials/p%d.claude-code.liquid", i)] = all.String()
		}
		srcs := make([]Source, 8)
		for i := range srcs {
			srcs[i] = Source{File: fmt.Sprintf("commands/c%d.md.liquid", i), Line: 1, Body: all.String()}
		}
		u := newUsage(p, "profiles")
		for _, src := range srcs {
			Expect(u.in(src)).To(BeFalse(), src.File)
		}
		Expect(u.walks).To(Equal(64), "each of the 64 included files once, across every source, and never the unused one")
		Expect(Uses(srcs, p, "profiles")).To(BeFalse())

		p.Files["_partials/p31.opencode.liquid"] = "{{ profiles | join: ',' }}"
		Expect(Uses(srcs, p, "profiles")).To(BeTrue(), "a partial that reads it, for any target, however late it is reached")
		Expect(Uses(nil, p, "profiles")).To(BeFalse(), "no source reads anything")
	})
})
