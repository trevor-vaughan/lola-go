package native_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/native"
	"github.com/trevor-vaughan/lola-go/internal/native/nativetest"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("Describe", func() {
	var root *os.Root
	var dir string
	put := func(rel string, data []byte) {
		GinkgoHelper()
		Expect(os.WriteFile(filepath.Join(dir, rel), data, 0o644)).To(Succeed())
	}
	BeforeEach(func() {
		testenv.Isolate()
		dir = GinkgoT().TempDir()
		var err error
		root, err = os.OpenRoot(dir)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(root.Close)
	})
	It("describes this test program: Go build info, digest, non-nil libraries", func() {
		self, err := os.Executable()
		Expect(err).NotTo(HaveOccurred())
		data, err := os.ReadFile(self)
		Expect(err).NotTo(HaveOccurred())
		put("self", data)
		d, err := native.Describe(root, "self")
		Expect(err).NotTo(HaveOccurred())
		Expect(d.Go).NotTo(BeNil())
		Expect(d.Go.GoVersion).To(HavePrefix("go"))
		sum := sha256.Sum256(data)
		Expect(d.SHA256).To(Equal(hex.EncodeToString(sum[:])))
		Expect(d.SHA256).To(HaveLen(64))
		Expect(d.Libraries).NotTo(BeNil())
	})
	It("reports linkage from the interpreter header", func() {
		put("dyn", nativetest.ELF64Dyn())
		put("static", nativetest.ELF64())
		d, err := native.Describe(root, "dyn")
		Expect(err).NotTo(HaveOccurred())
		Expect(d.Linkage).To(Equal("dynamic"))
		Expect(d.Go).To(BeNil())
		s, err := native.Describe(root, "static")
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Linkage).To(Equal("static"))
		Expect(s.Libraries).NotTo(BeNil())
	})
	It("returns an error for a file that is not a native binary", func() {
		put("t", []byte("just text"))
		_, err := native.Describe(root, "t")
		Expect(err).To(HaveOccurred())
	})
	It("returns an error for a file that is missing", func() {
		_, err := native.Describe(root, "gone")
		Expect(err).To(HaveOccurred())
	})
	It("describes a truncated ELF as malformed with a digest and no error", func() {
		put("bad", nativetest.ELF64()[:20])
		d, err := native.Describe(root, "bad")
		Expect(err).NotTo(HaveOccurred())
		Expect(d.Malformed).To(BeTrue())
		Expect(d.SHA256).To(HaveLen(64))
		Expect(d.Linkage).To(BeEmpty())
		Expect(d.Libraries).NotTo(BeNil())
	})
})
