package engine_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/market"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("the known marketplaces lola ships", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("holds sound entries, each a git repository by the classification --enable applies", func() {
		seed, err := market.Known()
		Expect(err).NotTo(HaveOccurred())
		Expect(seed).NotTo(BeEmpty())
		for _, k := range seed {
			spec, ok, err := source.Parse(k.Source, "")
			Expect(err).NotTo(HaveOccurred(), k.ID)
			Expect(ok).To(BeTrue(), k.ID)
			Expect(spec.Kind).To(Equal(source.KindGit), "%s: --enable refuses a --ref on a source that is not a git repository", k.ID)
		}
	})
})
