package cli

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("removing the directories a run created", func() {
	var root string
	BeforeEach(func() { root = testenv.Isolate() })

	It("removes them innermost first while empty, and answers which it removed", func() {
		made, err := mkdirAll(filepath.Join(root, "a", "b", "c"))
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(root, "a", "kept"), nil, 0o600)).To(Succeed())
		Expect(removeCreated(made)).To(Equal([]string{filepath.Join(root, "a", "b", "c"), filepath.Join(root, "a", "b")}))
		Expect(filepath.Join(root, "a", "kept")).To(BeARegularFile())
	})

	It("never removes a file or a symlink that took the place of a directory it made", func() {
		made, err := mkdirAll(filepath.Join(root, "a", "b"))
		Expect(err).NotTo(HaveOccurred())
		Expect(os.Remove(filepath.Join(root, "a", "b"))).To(Succeed())
		Expect(os.WriteFile(filepath.Join(root, "a", "b"), nil, 0o600)).To(Succeed())
		Expect(removeCreated(made)).To(BeEmpty())
		Expect(filepath.Join(root, "a", "b")).To(BeARegularFile())

		Expect(os.Remove(filepath.Join(root, "a", "b"))).To(Succeed())
		Expect(os.Mkdir(filepath.Join(root, "elsewhere"), 0o755)).To(Succeed())
		Expect(os.Symlink(filepath.Join(root, "elsewhere"), filepath.Join(root, "a", "b"))).To(Succeed())
		Expect(removeCreated(made)).To(BeEmpty())
		Expect(filepath.Join(root, "elsewhere")).To(BeADirectory())
		_, err = os.Lstat(filepath.Join(root, "a", "b"))
		Expect(err).NotTo(HaveOccurred())
	})
})
