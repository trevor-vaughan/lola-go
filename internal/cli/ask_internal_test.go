package cli

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("parseAnswer", func() {
	BeforeEach(func() { testenv.Isolate() })

	DescribeTable("reads what was typed against the answers a question takes",
		func(input, answers string, want rune, ok bool) {
			got, gotOK := parseAnswer(input, answers)
			Expect(gotOK).To(Equal(ok))
			Expect(got).To(Equal(want))
		},
		Entry("empty, with a default", "", "yN", 'n', true),
		Entry("empty, without a default", "", "ycn", rune(0), false),
		Entry("whitespace alone, with a default", "  \t", "yN", 'n', true),
		Entry("y", "y", "yN", 'y', true),
		Entry("Y", "Y", "yN", 'y', true),
		Entry("n", "n", "yN", 'n', true),
		Entry("N, the default's own letter", "N", "yN", 'n', true),
		Entry("c, when allowed", "c", "ycn", 'c', true),
		Entry("c, when not allowed", "c", "yN", rune(0), false),
		Entry("yes", "yes", "yN", 'y', true),
		Entry("No", "No", "yN", 'n', true),
		Entry("change", "change", "ycn", 'c', true),
		Entry("change, when c is not allowed", "change", "yn", rune(0), false),
		Entry("surrounding whitespace", "  y \t", "yN", 'y', true),
		Entry("another word", "maybe", "yN", rune(0), false),
		Entry("two letters", "yn", "yN", rune(0), false),
	)
})

var _ = Describe("a question's words", func() {
	BeforeEach(func() { testenv.Isolate() })

	DescribeTable("brackets its answers, the default in capitals",
		func(answers, want string) { Expect(answerBrackets(answers)).To(Equal(want)) },
		Entry("yes or no", "yN", "[y/N]"),
		Entry("the review", "ycn", "[y/c/n]"),
		Entry("the review, nothing to change", "yn", "[y/n]"),
	)

	DescribeTable("hints the letters it takes, and what Enter alone means",
		func(answers, want string) { Expect(answerHint(answers)).To(Equal(want)) },
		Entry("yes or no", "yN", "answer y or n (Enter means no)"),
		Entry("the review", "ycn", "answer y, c or n"),
		Entry("the review, nothing to change", "yn", "answer y or n"),
	)
})
