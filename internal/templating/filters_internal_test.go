package templating

import (
	"context"
	"errors"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("case filters on bytes that are not UTF-8", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("re-checks the result, which grows each invalid byte to three", func() {
		upcase := allowlist(context.Background(), "x.md.liquid", DefaultLimits())["upcase"].(func(any, ...any) (any, error))
		out, err := upcase(strings.Repeat("\xff", 21<<10))
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(HaveLen(63 << 10))
		_, err = upcase(strings.Repeat("\xff", 22<<10))
		Expect(err).To(MatchError("upcase would make a 66 KiB value; a value in a lola template is at most 64 KiB"))
	})
})

var _ = Describe("a value of a kind no template can reach", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("fails closed with template-value-type", func() {
		// liquid's cycle counters, which the screen keeps every template from reading
		json := allowlist(context.Background(), "x.md.liquid", DefaultLimits())["json"].(func(any, ...any) (any, error))
		_, err := json(map[string]int{"a": 1})
		r, ok := errors.AsType[*filterRefusal](err)
		Expect(ok).To(BeTrue())
		Expect(*r).To(Equal(filterRefusal{code: "template-value-type", reason: "a value of an unsupported kind reached json"}))
	})
})
