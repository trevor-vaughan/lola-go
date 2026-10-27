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

	DescribeTable("an invalid name says what is wrong with it before the rule",
		func(n, why string) {
			testenv.Isolate()
			Expect(module.ValidateName("marketplace", n)).To(MatchError(`marketplace name "` + n + `" is invalid: ` + why +
				`; use 1-64 characters from a-z, 0-9, '.', '_' and '-', starting with a letter or digit, with no '..'`))
		},
		Entry("uppercase", "Bad_Name", "B and N are uppercase"),
		Entry("one uppercase letter, twice", "Acme-Acme", "A is uppercase"),
		Entry("characters outside the rule", "a b/c", "' ' and '/' are not allowed"),
		Entry("both", "My Tools", "M and T are uppercase, ' ' is not allowed"),
		Entry("the first character", "_x", "it starts with '_'"),
		Entry("dot dot", "a..b", "it has '..'"),
		Entry("many characters", "ABCDEFGHIJ", "A, B, C, D, E, F, G, H and 2 more are uppercase"),
		Entry("empty", "", "it is empty"),
		Entry("too long", strings.Repeat("a", 65), "it is 65 characters long"),
	)

	It("names a control character escaped", func() {
		testenv.Isolate()
		Expect(module.ValidateName("skill", "a\tb")).To(MatchError(HavePrefix(`skill name "a?b" is invalid: '\t' is not allowed; use`)))
	})

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
