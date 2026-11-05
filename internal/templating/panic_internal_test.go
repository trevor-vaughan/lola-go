package templating

import (
	"context"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("renderWith's cancel", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("a cancel racing an erroring render returns the context's error", func() {
		ctx, cancel := context.WithCancel(context.Background())
		failing := map[string]any{"cancel": func(any) (any, error) {
			cancel()
			return nil, errors.New("failed after the cancel")
		}}
		_, err := renderWith(ctx, Source{File: "x.md.liquid", Line: 1, Body: "{{ target | cancel }}"}, Partials{}, Data{}, DefaultLimits(), nil, nil, failing)
		Expect(err).To(MatchError(context.Canceled))

		// A render that fails before Render reaches its select loses the race to the cancel only
		// now and then (about 6 in 20000 runs before the fix), so the race is run many times.
		canceled, stop := context.WithCancel(context.Background())
		stop()
		for range 20000 {
			_, err := renderWith(canceled, Source{File: "x.md.liquid", Line: 1, Body: "{% if %}"}, Partials{}, Data{}, DefaultLimits(), nil, nil, nil)
			Expect(errors.Is(err, context.Canceled)).To(BeTrue(), "%v", err)
		}
	})
})

var _ = Describe("renderWith's recover", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("a panic in screen, compile or render is template-panic with its file and a clean message, and the process lives", func() {
		const file = "agents/x\x1b.md.liquid"
		boom := map[string]any{"boom": func(any) any { panic(errors.New("filter \x1b[31mfailed\u009b")) }}
		for _, body := range []string{"{{ ☃ }}", "{{ a\u009bb }}", "{{ target | boom }}"} {
			_, err := renderWith(context.Background(), Source{File: file, Line: 1, Body: body}, Partials{}, Data{}, DefaultLimits(), nil, nil, boom)
			var e *Error
			Expect(errors.As(err, &e)).To(BeTrue(), body)
			Expect(e.Code).To(Equal("template-panic"), body)
			Expect(e.File).To(Equal("agents/x?.md.liquid"), body)
			Expect(e.Line).To(BeZero(), body)
			Expect(e.Reason).To(HavePrefix("the template engine failed: "), body)
			Expect(e.Error()).NotTo(ContainSubstring("\x1b"), body)
			Expect(e.Error()).NotTo(ContainSubstring("\u009b"), body)
		}
	})
})
