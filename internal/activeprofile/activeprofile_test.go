package activeprofile_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/activeprofile"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("activeprofile", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("checks the qualified names of an active list", func() {
		Expect(activeprofile.Check("user:go")).To(Succeed())
		Expect(activeprofile.Check("project:go")).To(Succeed())
		Expect(activeprofile.Check("remote:go")).To(Succeed())
		Expect(activeprofile.Check("go")).To(MatchError(ContainSubstring("is not user:, project: or remote:")))
		Expect(activeprofile.Check("default")).To(MatchError(ContainSubstring("is not user:, project: or remote:")))
		Expect(activeprofile.Check("default:go")).To(MatchError(ContainSubstring("is not user:, project: or remote:")))
		Expect(activeprofile.Check("user:default")).To(MatchError(ContainSubstring("default is always on")))
		Expect(activeprofile.Check("user:../x")).To(MatchError(ContainSubstring("is invalid")))
	})
})
