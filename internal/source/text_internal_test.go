package source

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("printable", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("replaces C0 controls, DEL, C1 controls and invalid UTF-8, keeping newlines", func() {
		Expect(printable("a\x1b[31mb\x00c\x7fd\u009be\xffé�", 100)).To(Equal("a?[31mb?c?d?e?é�"))
	})

	It("indents continuation lines so they cannot pose as lola's output", func() {
		Expect(printable("remote: one\n✓ installed\r\n", 100)).To(Equal("remote: one\n  ✓ installed?\n  "))
	})

	It("caps the text", func() {
		Expect(printable(strings.Repeat("x", 300), maxName)).To(Equal(strings.Repeat("x", maxName) + "…"))
		Expect(printable("abé", 3)).To(Equal("ab?…"))
	})
})
