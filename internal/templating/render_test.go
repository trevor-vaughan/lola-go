package templating_test

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"runtime"
	"runtime/pprof"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/templating"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

const file = "skills/review/SKILL.md.liquid"

func source(body string) templating.Source {
	return templating.Source{Module: "review-kit", File: file, Line: 1, Body: body}
}

// refusal renders src and returns the *templating.Error it must fail with.
func refusal(src templating.Source, d templating.Data, lim templating.Limits, declared ...string) *templating.Error {
	out, err := templating.Render(context.Background(), src, templating.Partials{}, d, lim, declared)
	Expect(out).To(BeNil())
	var e *templating.Error
	Expect(errors.As(err, &e)).To(BeTrue(), "%v", err)
	return e
}

// renderGoroutines counts the goroutines Render started that are still running.
func renderGoroutines() int {
	var b bytes.Buffer
	Expect(pprof.Lookup("goroutine").WriteTo(&b, 2)).To(Succeed())
	return strings.Count(b.String(), "created by github.com/trevor-vaughan/lola-go/internal/templating.renderWith")
}

// names returns n profile names, a list a spec loops over.
func names(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "p"
	}
	return out
}

var _ = Describe("Render", func() {
	var data templating.Data

	BeforeEach(func() {
		testenv.Isolate()
		data = templating.Data{Target: "claude-code", Scope: "project", Profiles: []string{"default", "work"}}
	})

	It("renders text with the run's target, scope and profiles", func() {
		out, err := templating.Render(context.Background(),
			source("for {{ target }} in {{ scope }}:{% for p in profiles %} {{ p }}{% endfor %}\n"), templating.Partials{}, data, templating.DefaultLimits(), nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(out)).To(Equal("for claude-code in project: default work\n"))
	})

	It("binds profiles as an empty list when none is active", func() {
		data.Profiles = nil
		out, err := templating.Render(context.Background(), source("[{% for p in profiles %}{{ p }}{% endfor %}]"), templating.Partials{}, data, templating.DefaultLimits(), nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(out)).To(Equal("[]"))
	})

	It("output one byte over 1 MiB is template-output-too-large; exactly 1 MiB renders", func() {
		data.Profiles = names(16)
		loop := "{% for p in profiles %}" + strings.Repeat("x", 64<<10) + "{% endfor %}"
		out, err := templating.Render(context.Background(), source(loop), templating.Partials{}, data, templating.DefaultLimits(), nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(HaveLen(1 << 20))

		e := refusal(source(loop+"x"), data, templating.DefaultLimits())
		Expect(e.Code).To(Equal("template-output-too-large"))
		Expect(e.Error()).To(Equal(file + ": renders more than 1 MiB; lola writes at most 1 MiB per file"))
	})

	Describe("a render past its deadline returns template-timeout within the deadline plus 1 s", func() {
		var lim templating.Limits

		BeforeEach(func() {
			lim = templating.DefaultLimits()
			lim.Timeout = 50 * time.Millisecond
			data.Profiles = names(1000)
		})

		expectTimeout := func(body string) {
			start := time.Now()
			e := refusal(source(body), data, lim)
			Expect(time.Since(start)).To(BeNumerically("<", lim.Timeout+time.Second))
			Expect(e.Code).To(Equal("template-timeout"))
			Expect(e.Error()).To(Equal(file + ": took longer than 50ms to render; lola stops a template after 50ms"))
		}

		It("stops a loop that writes", func() {
			lim.Output = 1 << 30
			expectTimeout("{% for a in profiles %}{% for b in profiles %}{% for c in profiles %}x{% endfor %}{% endfor %}{% endfor %}")
			Eventually(renderGoroutines).Should(BeZero())
		})

		It("stops a loop that filters", func() {
			expectTimeout("{% for a in profiles %}{% for b in profiles %}{% for c in profiles %}{% assign x = a | append: b %}{% endfor %}{% endfor %}{% endfor %}")
			Eventually(renderGoroutines).Should(BeZero())
		})

		It("returns on a loop that neither writes nor filters, which runs on until it ends", func() {
			// 10⁶ empty iterations outlast the 50 ms deadline and end on their own soon after.
			DeferCleanup(func() { Eventually(renderGoroutines, 10*time.Second).Should(BeZero()) })
			expectTimeout("{% for a in profiles %}{% for b in profiles %}{% endfor %}{% endfor %}")
		})

		It("a timeout names the effective limit, the context's when it is earlier", func() {
			lim.Timeout = time.Hour
			lim.Output = 1 << 30
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			DeferCleanup(cancel)
			_, err := templating.Render(ctx, source("{% for a in profiles %}{% for b in profiles %}{% for c in profiles %}x{% endfor %}{% endfor %}{% endfor %}"), templating.Partials{}, data, lim, nil)
			Expect(err).To(HaveField("Code", "template-timeout"))
			// the context's 50 ms, measured from Render's start, rounds to 50 ms or just under
			m := regexp.MustCompile(`^` + regexp.QuoteMeta(file) + `: took longer than (\S+) to render; lola stops a template after (\S+)$`).
				FindStringSubmatch(err.Error())
			Expect(m).To(HaveLen(3), err.Error())
			took, after := m[1], m[2]
			Expect(took).To(Equal(after))
			limit, perr := time.ParseDuration(took)
			Expect(perr).NotTo(HaveOccurred())
			Expect(limit).To(And(BeNumerically(">", 0), BeNumerically("<=", 50*time.Millisecond)))
			Expect(err.Error()).NotTo(ContainSubstring(lim.Timeout.String()))
			Eventually(renderGoroutines).Should(BeZero())
		})

		It("returns the context's error when the context is canceled", func() {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_, err := templating.Render(ctx, source("x"), templating.Partials{}, data, lim, nil)
			Expect(err).To(MatchError(context.Canceled))
		})

		It("a pre-canceled context always returns the context's error", func() {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			for range 20000 {
				_, err := templating.Render(ctx, source("{% assign a = 1 %}"), templating.Partials{}, data, lim, nil)
				Expect(errors.Is(err, context.Canceled)).To(BeTrue(), "%v", err)
			}
		})
	})

	It("a 256 KiB file renders and one byte more is template-too-large; a file that is not UTF-8 is template-invalid-utf8", func() {
		body := strings.Repeat("x", 256<<10)
		out, err := templating.Render(context.Background(), source(body), templating.Partials{}, data, templating.DefaultLimits(), nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(HaveLen(256 << 10))

		e := refusal(source(body+"x"), data, templating.DefaultLimits())
		Expect(e.Code).To(Equal("template-too-large"))
		Expect(e.Error()).To(Equal(file + ": is 262145 bytes; a template file is at most 256 KiB"))

		e = refusal(templating.Source{File: file, Line: 4, Body: "one\ntwo \xff\n"}, data, templating.DefaultLimits())
		Expect(e.Code).To(Equal("template-invalid-utf8"))
		Expect(e.Error()).To(Equal(file + ":5: is not UTF-8; a template file must be UTF-8 text"))
	})

	It("chained right-trim output stays under the output cap", func() {
		body := `{% assign s = "` + strings.Repeat("x", 64<<10) + `" %}` + strings.Repeat("{{s-}}", 2000)
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		e := refusal(source(body), data, templating.DefaultLimits())
		runtime.ReadMemStats(&after)
		Expect(e.Code).To(Equal("template-output-too-large"))
		// 2000 links of 64 KiB are 125 MiB; liquid's trim buffer held them all before the patch
		Expect(after.TotalAlloc - before.TotalAlloc).To(BeNumerically("<", 64<<20))
	})

	It("reports a syntax error with the file's line, counting from the source's first line", func() {
		e := refusal(templating.Source{File: file, Line: 7, Body: "a\n{% if target %}\n"}, data, templating.DefaultLimits())
		Expect(e.Code).To(Equal("template-syntax"))
		Expect(e.File).To(Equal(file))
		Expect(e.Line).To(Equal(8))
		Expect(e.Reason).To(Equal(`unterminated "if" block`))
	})
})
