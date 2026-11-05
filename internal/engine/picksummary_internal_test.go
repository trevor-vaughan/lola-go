package engine

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("summarizePicks", func() {
	var all []Component
	skips := func(from, to int) []config.Pattern {
		var out []config.Pattern
		for i := from; i <= to; i++ {
			out = append(out, config.Pattern{Kind: config.KindSkill, Name: fmt.Sprintf("s%02d", i)})
		}
		return out
	}
	BeforeEach(func() {
		testenv.Isolate()
		all = nil
		for i := 1; i <= 40; i++ {
			all = append(all, Component{Kind: config.KindSkill, Name: fmt.Sprintf("s%02d", i)})
		}
	})

	It("names the picks a long literal complement leaves", func() {
		Expect(summarizePicks(skips(3, 40), all)).To(Equal(&PicksSummary{Picks: []string{"skill/s01", "skill/s02"}, Others: 38}))
	})
	It("leaves a short skip list alone", func() {
		Expect(summarizePicks(skips(1, 9), all)).To(BeNil())
	})
	It("leaves a list that keeps more than it skips alone", func() {
		Expect(summarizePicks(skips(1, 12), all)).To(BeNil())
	})
	It("leaves a list that skips everything alone", func() {
		Expect(summarizePicks(skips(1, 40), all)).To(BeNil())
	})
	It("leaves a list with a glob alone, which a later version's component would also match", func() {
		Expect(summarizePicks(append(skips(3, 39), config.Pattern{Kind: config.KindSkill, Name: "s4*"}), all)).To(BeNil())
	})
	It("leaves a list naming a component the module lacks alone", func() {
		Expect(summarizePicks(append(skips(3, 39), config.Pattern{Kind: config.KindSkill, Name: "gone"}), all)).To(BeNil())
	})
})
