package paths

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("project key forms per OS", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("writes Windows keys with forward slashes and a lower-case drive letter", func() {
		Expect(keyForm("windows", `C:\Users\Me\src\api`)).To(Equal("c:/Users/Me/src/api"))
		Expect(keyForm("windows", `\\server\share\api`)).To(Equal("//server/share/api"))
		Expect(keyForm("linux", `/home/Me/a\b`)).To(Equal(`/home/Me/a\b`), "a backslash is a file name character on Linux")
	})

	It("compares keys case-insensitively on darwin and Windows only", func() {
		Expect(keyID("windows", `C:\Users\Me`)).To(Equal(keyID("windows", "c:/users/me")))
		Expect(keyID("darwin", "/Users/Me/src")).To(Equal(keyID("darwin", "/users/me/SRC")))
		Expect(keyID("linux", "/home/Me")).NotTo(Equal(keyID("linux", "/home/me")))
	})
})
