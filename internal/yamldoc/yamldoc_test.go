package yamldoc_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
	"github.com/trevor-vaughan/lola-go/internal/yamldoc"
)

var _ = Describe("ReadNoFollow", func() {
	It("refuses a symbolic link that Read follows", func() {
		dir := testenv.Isolate()
		file, link := filepath.Join(dir, "real.yml"), filepath.Join(dir, "link.yml")
		Expect(os.WriteFile(file, []byte("version: 1\n"), 0o644)).To(Succeed())
		if err := os.Symlink(file, link); err != nil {
			Skip("no symbolic links here: " + err.Error())
		}
		_, _, err := yamldoc.ReadNoFollow(link, 1)
		Expect(err).To(HaveOccurred())
		d, b, err := yamldoc.Read(link, 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(d).NotTo(BeNil())
		Expect(b).To(Equal([]byte("version: 1\n")))
	})
})
