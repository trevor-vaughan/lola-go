package source

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("copyFile", func() {
	It("refuses a file that was replaced by a symlink after it was checked", func() {
		dir := testenv.Isolate()
		src, secret, dst := filepath.Join(dir, "a.md"), filepath.Join(dir, "secret"), filepath.Join(dir, "out", "a.md")
		Expect(os.WriteFile(src, []byte("module text"), 0o644)).To(Succeed())
		Expect(os.WriteFile(secret, []byte("not yours"), 0o600)).To(Succeed())
		checked, err := os.Lstat(src)
		Expect(err).NotTo(HaveOccurred())

		Expect(os.Remove(src)).To(Succeed())
		Expect(os.Symlink(secret, src)).To(Succeed())
		Expect(copyFile(src, dst, checked)).To(MatchError(ContainSubstring("changed while lola was reading it")))
		Expect(dst).NotTo(BeAnExistingFile())
	})

	It("copies the file it checked, with a normalized mode", func() {
		dir := testenv.Isolate()
		src, dst := filepath.Join(dir, "run.sh"), filepath.Join(dir, "out", "run.sh")
		Expect(os.WriteFile(src, []byte("#!/bin/sh\n"), 0o700)).To(Succeed())
		checked, err := os.Lstat(src)
		Expect(err).NotTo(HaveOccurred())
		Expect(copyFile(src, dst, checked)).To(Succeed())
		fi, err := os.Stat(dst)
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o755)))
	})
})
