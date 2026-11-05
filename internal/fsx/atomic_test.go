package fsx_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/fsx"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("WriteFileAtomic", func() {
	var dir string
	BeforeEach(func() { dir = testenv.Isolate() })

	It("writes content with the requested mode and leaves no temp files", func() {
		p := filepath.Join(dir, "lock.json")
		Expect(fsx.WriteFileAtomic(p, []byte("{}"), 0o600)).To(Succeed())
		Expect(os.ReadFile(p)).To(Equal([]byte("{}")))
		fi, err := os.Stat(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o600)))
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			Expect(e.Name()).NotTo(ContainSubstring(".tmp-"))
		}
	})

	It("replaces an existing file", func() {
		p := filepath.Join(dir, "f")
		Expect(os.WriteFile(p, []byte("old"), 0o644)).To(Succeed())
		Expect(fsx.WriteFileAtomic(p, []byte("new"), 0o644)).To(Succeed())
		Expect(os.ReadFile(p)).To(Equal([]byte("new")))
	})

	It("fails when the parent directory is missing", func() {
		Expect(fsx.WriteFileAtomic(filepath.Join(dir, "nope", "f"), []byte("x"), 0o600)).NotTo(Succeed())
	})
})

var _ = Describe("ReadOwn", func() {
	It("reads a regular file within its limit, never through a symbolic link", func() {
		dir := testenv.Isolate()
		p := filepath.Join(dir, "source.json")
		Expect(os.WriteFile(p, []byte("12345"), 0o600)).To(Succeed())
		Expect(fsx.ReadOwn(p, 5)).To(Equal([]byte("12345")))
		_, err := fsx.ReadOwn(p, 4)
		Expect(err).To(MatchError("larger than 4 bytes"))
		_, err = fsx.ReadOwn(dir, 4)
		Expect(err).To(MatchError("not a regular file"))
		link := filepath.Join(dir, "link.json")
		if err := os.Symlink(p, link); err != nil {
			Skip("no symbolic links here: " + err.Error())
		}
		_, err = fsx.ReadOwn(link, 5)
		Expect(err).To(MatchError("not a regular file"))
	})
})

var _ = DescribeTable("NormalizeMode keeps only executability",
	func(in, want os.FileMode) { Expect(fsx.NormalizeMode(in)).To(Equal(want)) },
	Entry("plain file", os.FileMode(0o600), os.FileMode(0o644)),
	Entry("group-writable file", os.FileMode(0o664), os.FileMode(0o644)),
	Entry("owner-executable", os.FileMode(0o700), os.FileMode(0o755)),
	Entry("other-executable only", os.FileMode(0o641), os.FileMode(0o755)),
)
