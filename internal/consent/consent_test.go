package consent_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/consent"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("Store", func() {
	var path string
	var s *consent.Store
	BeforeEach(func() {
		path = filepath.Join(testenv.Isolate(), "state", "lola", "consent.json")
		s = consent.New(path)
	})

	It("grants per module and digest, so a changed hook asks again", func() {
		Expect(s.Granted("example", "sha256:a")).To(BeFalse())
		Expect(s.Grant("example", "sha256:a")).To(Succeed())
		Expect(s.Granted("example", "sha256:a")).To(BeTrue())
		Expect(s.Granted("example", "sha256:b")).To(BeFalse())
		Expect(s.Granted("other", "sha256:a")).To(BeFalse())
		Expect(s.Grant("example", "sha256:b")).To(Succeed())
		Expect(s.Granted("example", "sha256:a")).To(BeFalse(), "only the latest consent counts")
		fi, err := os.Stat(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o600)))
	})

	It("refuses a corrupt or newer file and never overwrites it", func() {
		Expect(os.MkdirAll(filepath.Dir(path), 0o700)).To(Succeed())
		for body, want := range map[string]string{
			`{`:                          "consent.json",
			`{"version":1,"extra":true}`: "extra",
			`{"version":9,"modules":{}}`: "newer lola",
		} {
			Expect(os.WriteFile(path, []byte(body), 0o600)).To(Succeed())
			_, err := s.Granted("example", "sha256:a")
			Expect(err).To(MatchError(ContainSubstring(want)), body)
			Expect(s.Grant("example", "sha256:a")).To(MatchError(ContainSubstring(want)), body)
			Expect(os.ReadFile(path)).To(Equal([]byte(body)))
		}
	})
})
