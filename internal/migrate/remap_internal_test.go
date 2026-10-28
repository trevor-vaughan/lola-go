package migrate

import (
	"path/filepath"
	"runtime"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("Another machine's roots (Plan 10 §3.1)", func() {
	var tmp string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
	})

	It("takes the home-shaped prefix as the old root, else the shallowest ancestor missing here", func() {
		Expect(oldRoot("/Users/alice/src/proj", "/home/me")).To(Equal("/Users/alice"))
		Expect(oldRoot("/home/olduser/proj", "/home/me")).To(Equal("/home/olduser"))
		Expect(oldRoot("/root/proj", "/home/me")).To(Equal("/root"))
		Expect(oldRoot(`C:\Users\alice\src\proj`, "/home/me")).To(Equal(`C:\Users\alice`))
		Expect(oldRoot(`E:\work\proj`, "/home/me")).To(Equal(`E:\work`))
		Expect(oldRoot(`E:\`, "/home/me")).To(Equal(`E:\`))
		Expect(oldRoot(filepath.Join(tmp, "gone", "deeper", "proj"), "/home/me")).To(Equal(filepath.Join(tmp, "gone")))
	})

	It("never takes this machine's home as the old root", func() {
		if runtime.GOOS != "linux" {
			Skip("needs a home-shaped path none of whose ancestors exist: /Users on Linux")
		}
		Expect(oldRoot("/Users/me/old/proj", "/Users/me")).To(Equal("/Users"))
		Expect(oldRoot(`D:\work\proj`, "/home/me")).To(Equal(`D:\work`), "nothing of a Windows path exists here")
	})

	DescribeTable("moves a path at or below the old root to the same place below the new one",
		func(p, old, want string, ok bool) {
			got, moved := rebase(p, old, "/home/me")
			Expect(moved).To(Equal(ok))
			Expect(got).To(Equal(want))
		},
		Entry("below it", "/Users/alice/src/proj", "/Users/alice", filepath.Join("/home/me", "src", "proj"), true),
		Entry("the root itself", "/Users/alice", "/Users/alice", filepath.Clean("/home/me"), true),
		Entry("a sibling sharing its prefix", "/Users/alicex/proj", "/Users/alice", "", false),
		Entry("elsewhere", "/srv/proj", "/Users/alice", "", false),
		Entry("a Windows path", `C:\Users\alice\src\proj`, `C:\Users\alice`, filepath.Join("/home/me", "src", "proj"), true),
		Entry("a Windows path, its drive letter in lower case", `C:\Users\alice\src\proj`, `c:\Users\alice`, filepath.Join("/home/me", "src", "proj"), true),
		Entry("a Windows path, its root written with /", `C:\Users\alice\src\proj`, "C:/Users/alice", filepath.Join("/home/me", "src", "proj"), true),
		Entry("a Windows sibling sharing its prefix", `C:\Users\alicex\proj`, `C:\Users\alice`, "", false),
		Entry("a .. that would leave the new root", "/Users/alice/../../etc/kit", "/Users/alice", "", false),
		Entry("a Windows .. that would leave the new root", `C:\Users\alice\..\..\etc`, `C:\Users\alice`, "", false),
	)
})
