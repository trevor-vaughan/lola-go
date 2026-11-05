package engine

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("nameTaken", func() {
	It("gives the cache's own refusal a hint with the run's flags, and keeps the hints a caller added", func() {
		testenv.Isolate()
		taken := &store.NameTakenError{Name: "kit"}
		own := errs.WithHints(taken, taken.RemoveHint())
		Expect(errs.Hints(nameTaken(own, []string{"--json"}))).To(Equal([]string{"remove the cached one first: lola cache rm kit --json"}))

		wrapped := errs.WithHints(own, "lola cache list --json")
		Expect(nameTaken(wrapped, []string{"--json"})).To(BeIdenticalTo(wrapped), "a wrap that adds only hints keeps them")
	})
})
