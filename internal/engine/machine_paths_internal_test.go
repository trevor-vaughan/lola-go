package engine

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("machinePaths", func() {
	BeforeEach(func() { testenv.Isolate() })

	DescribeTable("finds the project root or $HOME only at path boundaries",
		func(data, root, home string, want bool) {
			Expect(machinePaths([]byte(data), root, home)).To(Equal(want))
		},
		Entry("a sibling that shares the root's prefix", `"/home/u/proj2/x"`, "/home/u/proj", "", false),
		Entry("the root followed by a path", `"/home/u/proj/.lola"`, "/home/u/proj", "", true),
		Entry("the root closing its string", `"/home/u/proj"`, "/home/u/proj", "", true),
		Entry("the root ending the data", `/home/u/proj`, "/home/u/proj", "", true),
		Entry("the root followed by a Windows separator", `"C:\\u\\proj\\x"`, `C:\\u\\proj`, "", true),
		Entry("$HOME when the root is elsewhere", `"/home/u/.cache/x"`, "/srv/proj", "/home/u", true),
		Entry("a $HOME of /", `"/x"`, "/srv/proj", "/", false),
		Entry("an empty $HOME", `"/x"`, "/srv/proj", "", false),
		Entry("no path of this machine", `{"command":"npx"}`, "/home/u/proj", "/home/u", false),
	)
})
