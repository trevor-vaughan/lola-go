package templating_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/templating"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("bindings", func() {
	var data templating.Data
	declared := []string{"language", "strict", "tools"}

	BeforeEach(func() {
		testenv.Isolate()
		data = templating.Data{
			Target: "claude-code", Scope: "project", Profiles: []string{"default", "work"},
			Vars: map[string]any{"language": "go", "strict": true, "tools": []string{"rg", "jq"}},
			Attrs: map[string]any{
				"model": "sonnet", "size": "large", "n": int64(3), "none": nil,
				"tools":  []any{"Read", map[string]any{"b": "2", "a": "1"}},
				"nested": map[string]any{"z": "26", "y": map[string]any{"x": "24"}},
			},
		}
	})

	rendered := func(body string) string {
		out, err := templating.Render(context.Background(), source(body), templating.Partials{}, data, templating.DefaultLimits(), declared)
		Expect(err).NotTo(HaveOccurred())
		return string(out)
	}

	It("binds the module's vars and the component's attributes", func() {
		Expect(rendered("{{ vars.language }} {{ vars.strict }} {{ vars.tools[1] }} {{ attrs.model }} {{ attrs.n }} [{{ attrs.none }}] {{ attrs.nested.y.x }}")).
			To(Equal("go true jq sonnet 3 [] 24"))
	})

	It("binds vars and attrs as empty maps when Data has none", func() {
		data.Vars, data.Attrs = nil, nil
		Expect(rendered("[{% for k in vars %}{{ k }}{% endfor %}{% for k in attrs %}{{ k }}{% endfor %}]")).To(Equal("[]"))
	})

	It("a template sees only strings, booleans, numbers, lists and maps: no method of a bound or built value is callable", func() {
		for _, body := range []string{
			"{{ target.Len }}", "{{ vars.tools.Len }}", "{{ profiles.AsArray }}", "{% assign s = target %}{{ s.Len }}",
			"{% assign m = attrs.nested %}{{ m.Keys }}",
		} {
			e := refusal(source(body), data, templating.DefaultLimits(), declared...)
			Expect(e.Line).To(Equal(1), body)
			Expect(e.Reason).To(HavePrefix("undefined variable"), body)
		}
		Expect(rendered("[{{ attrs.Len }}{{ attrs.nested.Keys }}]")).To(Equal("[]"), "attrs is lenient: a method name is a missing key")
		for _, d := range []templating.Data{
			{Vars: map[string]any{"n": 5}},
			{Vars: map[string]any{"n": float32(5)}},
			{Attrs: map[string]any{"since": time.Time{}}},
			{Attrs: map[string]any{"list": []any{struct{ Len int }{}}}},
			{Attrs: map[string]any{"n": 5}},
		} {
			e := refusal(source("x"), d, templating.DefaultLimits(), declared...)
			Expect(e.Code).To(Equal("template-value-type"), "%v", d)
		}
		e := refusal(source("x"), templating.Data{Attrs: map[string]any{"list": []any{"a", 5}}}, templating.DefaultLimits())
		Expect(e.Error()).To(Equal(file + ": attrs.list[1] holds a value of an unsupported kind (int)"))
	})

	It("a missing attrs key reads as nil at any depth, and a missing vars name still refuses", func() {
		for body, want := range map[string]string{
			"[{{ attrs.model.x }}][{{ attrs.n.x }}][{{ attrs.model[0] }}][{{ attrs.none.x }}][{{ attrs.n.x.y }}]":                    "[][][][][]",
			"{{ attrs.model.size }} {{ attrs.tools.size }} {{ attrs.tools.first }} {{ attrs.tools.last.a }} {{ attrs.nested.size }}": "6 2 Read 1 2",
			"[{{ attrs.x }}]":                          "[]",
			"[{{ attrs.x.y.z }}][{{ attrs['x'][0] }}]": "[][]",
			"{% if attrs.model %}m{% endif %}{% if attrs.x %}x{% else %}none{% endif %}":                                                                       "mnone",
			"{% unless attrs.x.y %}u{% endunless %}":                                                                                                           "u",
			"{{ attrs.x | default: 'y' }} {{ attrs.nested.q | default: 'z' }}":                                                                                 "y z",
			"[{{ attrs.nested.y.w }}][{{ attrs.nested.y.x }}]":                                                                                                 "[][24]",
			"[{{ attrs.tools[5] }}][{{ attrs.tools[1].c }}][{{ attrs.tools[1].a }}]":                                                                           "[][][1]",
			"{% for k in attrs %}{{ k }},{% endfor %}":                                                                                                         "model,n,nested,none,size,tools,",
			"{% for k in attrs.nested %}{{ k }}{% endfor %}{% for k in attrs.x %}{{ k }}{% endfor %}":                                                          "yz",
			"{% if attrs.x == nil %}nil{% endif %}{% if attrs.x == '' %}empty{% endif %}{% if attrs.x == false %}false{% endif %}":                             "nil",
			"{% if attrs contains 'model' %}c{% endif %}{% if attrs contains 'x' %}x{% endif %}{% if attrs.x contains 'a' %}a{% endif %}":                      "c",
			"{% if attrs.nested contains 'z' %}z{% endif %}{% if attrs.nested.q contains 'z' %}q{% endif %}":                                                   "z",
			"{{ attrs.nested | json }} {{ attrs.x | json }} {{ attrs.tools | size }} {{ attrs.size }}":                                                         `{"y":{"x":"24"},"z":"26"} null 2 large`,
			"{% assign t = attrs.x %}[{{ t }}]{% case attrs.x %}{% when nil %}n{% endcase %}":                                                                  "[]n",
			"[{{ attrs.n.String }}][{{ attrs.model.String }}][{{ attrs.n.Error }}]":                                                                            "[][][]",
			"{% if attrs.x < 1 %}a{% endif %}{% if 1 < attrs.x %}b{% endif %}{% if attrs.x > 1 %}c{% endif %}{% if 1 > attrs.x %}d{% endif %}":                 "",
			"{% if attrs.x < 'a' %}a{% endif %}{% if 'a' > attrs.x %}b{% endif %}{% if attrs.x > attrs.y %}c{% endif %}{% if attrs.n > attrs.x %}d{% endif %}": "",
		} {
			Expect(rendered(body)).To(Equal(want), body)
		}
		for body, want := range map[string]string{
			"{{ attrs }}":                                     "attrs is a map; print one of its keys or loop over it",
			"{% assign m = attrs.nested %}{{ m.q }}":          "undefined variable",
			"{% for t in attrs.tools %}{{ t.c }}{% endfor %}": "undefined variable",
			"{{ vars.language.x }}":                           "undefined variable in {{ vars.language.x }}",
		} {
			Expect(refusal(source(body), data, templating.DefaultLimits(), declared...).Reason).To(Equal(want), body)
		}
		Expect(refusal(source("{{ vars.nope }}"), data, templating.DefaultLimits(), declared...).Code).To(Equal("template-undeclared-var"))
	})

	It("a bound map iterates in key order, and two renders are byte-equal", func() {
		data.Vars = map[string]any{}
		for _, k := range []string{"m", "c", "x", "a", "q", "b", "z", "k", "e", "t"} {
			data.Vars[k] = k
		}
		body := "{% for k in vars %}{{ k }}={{ vars[k] }};{% endfor %}|{% for k in attrs.nested %}{{ k }}{% endfor %}|{% for k in attrs.tools[1] %}{{ k }}{% endfor %}"
		first := rendered(body)
		Expect(first).To(Equal("a=a;b=b;c=c;e=e;k=k;m=m;q=q;t=t;x=x;z=z;|yz|ab"))
		for range 20 {
			Expect(rendered(body)).To(Equal(first))
		}
	})

	It("strict variables hold through bound vars at every depth and in if, attrs reads strictly through an alias, and contains tests presence", func() {
		for body, line := range map[string]int{
			"{% assign m = attrs.nested %}a\n{{ m.missing }}":             2,
			"{% assign m = attrs.nested %}{{ m.y.missing }}":              1,
			"{% assign t = attrs.tools %}\n\n{{ t[1].missing }}":          3,
			"{% assign m = attrs.nested %}{% if m.missing %}x{% endif %}": 1,
			"{% assign k = 'nope' %}\n{{ vars[k] }}":                      2,
			"{% for k in attrs.nested %}{{ k.x }}{% endfor %}":            1,
		} {
			e := refusal(source(body), data, templating.DefaultLimits(), declared...)
			Expect(e.Line).To(Equal(line), body)
			Expect(e.Reason).To(HavePrefix("undefined variable"), body)
		}
		Expect(rendered(`{% if attrs contains "model" %}model{% endif %}{% if attrs contains "mode" %}mode{% endif %}`)).To(Equal("model"))
		Expect(rendered("{{ attrs.size }}")).To(Equal("large"))
	})
})
