package untrusted_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

var _ = Describe("Text", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("replaces C0 controls, DEL, C1 controls and invalid UTF-8, keeping newlines", func() {
		Expect(untrusted.Text("a\x1b[31mb\x00c\x7fd\u009be\xffé�", 100)).To(Equal("a?[31mb?c?d?e?é�"))
	})

	It("indents continuation lines so they cannot pose as lola's output", func() {
		Expect(untrusted.Text("remote: one\n✓ installed\r\n", 100)).To(Equal("remote: one\n  ✓ installed?\n  "))
	})

	It("replaces format characters and line separators that can make text render differently from what it is", func() {
		Expect(untrusted.Text("a\u202eb\u2066c\u2069d\u200be\ufeff", 100)).To(Equal("a?b?c?d?e?"))
		Expect(untrusted.Text("a\u2028b\u2029c", 100)).To(Equal("a?b?c"))
		Expect(untrusted.Text("ünï ✓", 100)).To(Equal("ünï ✓"))
	})

	It("caps the text", func() {
		Expect(untrusted.Text(strings.Repeat("x", 300), untrusted.MaxName)).To(Equal(strings.Repeat("x", untrusted.MaxName) + "…"))
		Expect(untrusted.Text("abé", 3)).To(Equal("ab?…"))
	})
})
