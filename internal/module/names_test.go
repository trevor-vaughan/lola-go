package module_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("names", func() {
	DescribeTable("valid names",
		func(n string) { Expect(module.ValidateName("skill", n)).To(Succeed()) },
		Entry("simple", "review"), Entry("digits first", "0day"), Entry("dots", "a.b_c-d"),
		Entry("64 chars", strings.Repeat("a", 64)),
	)
	DescribeTable("invalid names name the kind",
		func(n string) { Expect(module.ValidateName("skill", n)).To(MatchError(ContainSubstring("skill name"))) },
		Entry("empty", ""), Entry("uppercase", "Review"), Entry("leading dash", "-x"), Entry("dotdot", "a..b"),
		Entry("slash", "a/b"), Entry("65 chars", strings.Repeat("a", 65)), Entry("space", "a b"),
	)

	It("bounds the name it echoes", func() {
		testenv.Isolate()
		err := module.ValidateName("skill", strings.Repeat("A", 3000))
		Expect(err).To(HaveOccurred())
		Expect(len(err.Error())).To(BeNumerically("<", 400))
	})

	It("quotes an invalid name with its control characters replaced and its length capped", func() {
		testenv.Isolate()
		err := module.ValidateName("skill", "\x1b[2J"+strings.Repeat("x", 1000))
		Expect(err.Error()).To(HavePrefix(`skill name "?[2J`))
		Expect(err.Error()).NotTo(ContainSubstring("\x1b"))
		Expect(len(err.Error())).To(BeNumerically("<", 500))
	})

	It("Supports treats an empty list as every target", func() {
		Expect(module.Supports(nil, "claude-code")).To(BeTrue())
		Expect(module.Supports([]string{"opencode"}, "claude-code")).To(BeFalse())
		Expect(module.Supports([]string{"opencode", "claude-code"}, "claude-code")).To(BeTrue())
	})
})
