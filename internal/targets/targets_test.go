package targets_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("registry", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("lists every target once, in registration order, and looks them up by id", func() {
		Expect(targets.IDs()).To(Equal([]string{"claude-code", "gemini-cli", "opencode"}))
		for _, id := range targets.IDs() {
			t, ok := targets.Lookup(id)
			Expect(ok).To(BeTrue(), id)
			Expect(t.ID()).To(Equal(id))
		}
		_, ok := targets.Lookup("vim")
		Expect(ok).To(BeFalse())
	})
})
