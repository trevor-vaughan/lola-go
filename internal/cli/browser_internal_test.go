package cli

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("the browser launcher", func() {
	BeforeEach(func() { testenv.Isolate() })

	DescribeTable("names the platform's opener and passes the address as its last, single argument",
		func(goos, name string, args []string) {
			gotName, gotArgs := browserCommand(goos, "https://example.org/a?b=1&c=2")
			Expect(gotName).To(Equal(name))
			Expect(gotArgs).To(Equal(args))
		},
		Entry("Linux", "linux", "xdg-open", []string{"https://example.org/a?b=1&c=2"}),
		Entry("macOS", "darwin", "open", []string{"https://example.org/a?b=1&c=2"}),
		Entry("Windows, with no shell", "windows", "rundll32", []string{"url.dll,FileProtocolHandler", "https://example.org/a?b=1&c=2"}),
	)

	It("refuses to start anything for an address that is not a plain http(s) one", func() {
		for _, bad := range []string{"-x", "file:///etc/passwd", "https://example.org/a b", "https://example.org/\n", "https:///x", ""} {
			Expect(openBrowser(bad)).To(HaveOccurred(), bad)
		}
	})
})
