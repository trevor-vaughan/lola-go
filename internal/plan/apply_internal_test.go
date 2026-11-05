package plan

import (
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("the manifest write's backup", func() {
	var dir, manPath string
	original := []byte("version: 1 # mine\n")
	// leftovers lists lola's temp and backup files in dir.
	leftovers := func() []string {
		var out []string
		es, err := os.ReadDir(dir)
		Expect(err).NotTo(HaveOccurred())
		for _, e := range es {
			if strings.Contains(e.Name(), ".lola-new-") || strings.Contains(e.Name(), ".lola-bak-") {
				out = append(out, e.Name())
			}
		}
		return out
	}
	BeforeEach(func() {
		dir = filepath.Join(testenv.Isolate(), ".lola")
		manPath = filepath.Join(dir, "project.yml")
		Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
		Expect(os.WriteFile(manPath, original, 0o600)).To(Succeed())
	})

	It("leaves the original manifest in place, so a crash right after it loses nothing", func() {
		t := &txn{staged: map[string]string{}}
		Expect(t.stage(FileChange{Abs: manPath, Data: []byte("version: 1\n"), Mode: 0o600})).To(Succeed())
		Expect(t.keep(manPath)).To(Succeed())
		Expect(os.ReadFile(manPath)).To(Equal(original))

		Expect(t.rollback()).To(Succeed())
		Expect(os.ReadFile(manPath)).To(Equal(original))
		Expect(leftovers()).To(BeEmpty())
	})

	It("restores the original over the new manifest on rollback", func() {
		t := &txn{staged: map[string]string{}}
		Expect(t.stage(FileChange{Abs: manPath, Data: []byte("version: 1\n"), Mode: 0o600})).To(Succeed())
		Expect(t.replace(manPath)).To(Succeed())
		Expect(os.ReadFile(manPath)).To(Equal([]byte("version: 1\n")))

		Expect(t.rollback()).To(Succeed())
		Expect(os.ReadFile(manPath)).To(Equal(original))
		fi, err := os.Stat(manPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o600)))
		Expect(leftovers()).To(BeEmpty())
	})
})
