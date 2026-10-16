package fsx_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/fsx"
)

var _ = Describe("pointer blocks", func() {
	block := "<!-- lola:instructions:start -->\n@.claude/lola/a.md\n@.claude/lola/b.md\n<!-- lola:instructions:end -->"

	It("creates a block in an empty file", func() {
		got, err := fsx.SetPointerBlock(nil, []string{"@.claude/lola/a.md", "@.claude/lola/b.md"})
		Expect(err).NotTo(HaveOccurred())
		Expect(string(got)).To(Equal(block + "\n"))
	})

	It("appends after user content, separated by a blank line, and removal restores the file", func() {
		orig := "# Project rules\n\nBe nice.\n"
		got, err := fsx.SetPointerBlock([]byte(orig), []string{"@.claude/lola/a.md", "@.claude/lola/b.md"})
		Expect(err).NotTo(HaveOccurred())
		Expect(string(got)).To(Equal(orig + "\n" + block + "\n"))
		back, err := fsx.SetPointerBlock(got, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(back)).To(Equal(orig))
	})

	It("adds a missing final newline before appending", func() {
		got, err := fsx.SetPointerBlock([]byte("x"), []string{"@a"})
		Expect(err).NotTo(HaveOccurred())
		Expect(string(got)).To(HavePrefix("x\n\n<!-- lola"))
	})

	It("replaces a block in place and leaves surrounding text byte-identical", func() {
		orig := "top\n" + block + "\nbottom\n"
		got, err := fsx.SetPointerBlock([]byte(orig), []string{"@.claude/lola/c.md"})
		Expect(err).NotTo(HaveOccurred())
		Expect(string(got)).To(Equal("top\n<!-- lola:instructions:start -->\n@.claude/lola/c.md\n<!-- lola:instructions:end -->\nbottom\n"))
	})

	It("reads the current block", func() {
		b, found, err := fsx.PointerBlock([]byte("x\n" + block + "\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(b).To(Equal(block))
		_, found, err = fsx.PointerBlock([]byte("x\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
	})

	DescribeTable("rejects malformed blocks instead of guessing",
		func(content string) {
			_, err := fsx.SetPointerBlock([]byte(content), []string{"@a"})
			Expect(err).To(MatchError(ContainSubstring("malformed")))
		},
		Entry("start without end", "<!-- lola:instructions:start -->\n@a\n"),
		Entry("end before start", "<!-- lola:instructions:end -->\n<!-- lola:instructions:start -->\n"),
		Entry("two blocks", block+"\n"+block+"\n"),
	)
})
