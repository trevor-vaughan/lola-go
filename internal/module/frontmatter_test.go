package module_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/module"
)

var _ = Describe("ParseDoc", func() {
	It("splits YAML frontmatter from the body and keeps the raw text", func() {
		d, err := module.ParseDoc([]byte("---\ndescription: Review code\nmodel: opus\n---\nDo the review.\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(d.String("description")).To(Equal("Review code"))
		Expect(d.RawFrontmatter).To(Equal("description: Review code\nmodel: opus\n"))
		Expect(d.Body).To(Equal("Do the review.\n"))
		Expect(d.Render()).To(Equal("---\ndescription: Review code\nmodel: opus\n---\nDo the review.\n"))
	})

	It("accepts a document without frontmatter (Python bug: commands without frontmatter failed)", func() {
		d, err := module.ParseDoc([]byte("Just a prompt with $ARGUMENTS\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(d.Frontmatter).To(BeNil())
		Expect(d.Render()).To(Equal("Just a prompt with $ARGUMENTS\n"))
	})

	It("handles CRLF fences and a byte-order mark", func() {
		d, err := module.ParseDoc([]byte("\ufeff---\r\ndescription: x\r\n---\r\nbody\r\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(d.String("description")).To(Equal("x"))
		Expect(d.Body).To(Equal("body\r\n"))
	})

	It("rejects an unterminated frontmatter block", func() {
		_, err := module.ParseDoc([]byte("---\ndescription: x\nbody\n"))
		Expect(err).To(MatchError(ContainSubstring("closing ---")))
	})

	It("rejects frontmatter that is not a mapping", func() {
		_, err := module.ParseDoc([]byte("---\n- a\n- b\n---\nbody\n"))
		Expect(err).To(MatchError(ContainSubstring("frontmatter")))
	})

	It("Prepend adds a key at the top and keeps the author's text", func() {
		d, err := module.ParseDoc([]byte("---\n# comment\ndescription: x\n---\nbody\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(d.Prepend("name", "reviewer").Render()).To(Equal("---\nname: reviewer\n# comment\ndescription: x\n---\nbody\n"))
		bare, _ := module.ParseDoc([]byte("body\n"))
		Expect(bare.Prepend("name", "r").Render()).To(Equal("---\nname: r\n---\nbody\n"))
	})

	It("String ignores non-string values", func() {
		d, _ := module.ParseDoc([]byte("---\ntools: [a]\n---\n"))
		Expect(d.String("tools")).To(Equal(""))
	})
})
