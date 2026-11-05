package errs_test

import (
	"errors"
	"fmt"
	"io/fs"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/errs"
)

var _ = Describe("hinted errors", func() {
	It("keeps the message and the wrapped error", func() {
		err := errs.WithHints(fs.ErrNotExist, "create it")
		Expect(err.Error()).To(Equal(fs.ErrNotExist.Error()))
		Expect(errors.Is(err, fs.ErrNotExist)).To(BeTrue())
	})

	It("collects hints through fmt wrapping, outermost first", func() {
		inner := errs.WithHints(errors.New("boom"), "inner hint")
		outer := errs.WithHints(fmt.Errorf("ctx: %w", inner), "outer hint")
		Expect(errs.Hints(outer)).To(Equal([]string{"outer hint", "inner hint"}))
		Expect(outer.Error()).To(Equal("ctx: boom"))
	})

	It("returns nil hints for plain errors and passes nil through", func() {
		Expect(errs.Hints(errors.New("x"))).To(BeEmpty())
		Expect(errs.WithHints(nil, "h")).To(BeNil())
	})
})
