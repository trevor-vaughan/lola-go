package cli

import (
	"strings"

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

var _ = Describe("fits", func() {
	BeforeEach(func() { testenv.Isolate() })

	DescribeTable("counts the text wrapped to the width, then the rows under it",
		func(text string, rows, width, height int, want bool) {
			Expect(fits(text, rows, width, height)).To(Equal(want))
		},
		Entry("text and one row fill the terminal", numbered(7), 1, 80, 8, true),
		Entry("one line more", numbered(8), 1, 80, 8, false),
		Entry("a line wrapped to two, and a row", strings.Repeat("x", 100)+"\n", 1, 80, 3, true),
		Entry("a line wrapped to two, and two rows", strings.Repeat("x", 100)+"\n", 2, 80, 3, false),
		Entry("no text", "", 1, 80, 1, true),
	)
})

var _ = Describe("the viewer's key line", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("names each mode's keys in table order, what Enter alone answers first and search last", func() {
		Expect(keyLineFor(keysRead, nil, "")).To(Equal("q quit · ↑↓ space b  scroll · g G  top/end · /  search"))
		Expect(keyLineFor(keysContinue, nil, "Restore which?")).To(Equal(
			"q  continue to: Restore which? · ↑↓ space b  scroll · g G  top/end · /  search"))
		q := Question{Prompt: "Uninstall kit?", Answers: "yN"}
		Expect(keyLineFor(keysPrompt, &q, "")).To(Equal(
			"⏎ alone = no · q quit · ↑↓ space b  scroll · g G  top/end · /  search"))
		q.Answers = "ycn"
		Expect(keyLineFor(keysPrompt, &q, "")).To(Equal("q quit · ↑↓ space b  scroll · g G  top/end · /  search"),
			"no default: nothing to say about Enter alone")
	})
})
