package source

import (
	"os"
	"path/filepath"
	"runtime"

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

	It("refuses a link's target that was replaced after the link was resolved", func() {
		if runtime.GOOS == "windows" {
			Skip("symbolic links need privileges on Windows")
		}
		dir, outside := testenv.Isolate(), GinkgoT().TempDir()
		Expect(os.MkdirAll(filepath.Join(dir, "ref"), 0o755)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(dir, "m"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dir, "ref", "a.md"), []byte("module text"), 0o644)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(outside, "secret"), []byte("not yours"), 0o600)).To(Succeed())
		Expect(os.Symlink("../ref/a.md", filepath.Join(dir, "m", "l"))).To(Succeed())
		root, err := os.OpenRoot(dir)
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(root.Close()).To(Succeed()) }()
		target, checked, err := resolveLink(root, ".", "m/l")
		Expect(err).NotTo(HaveOccurred())
		Expect(target).To(Equal("ref/a.md"))

		// Park the original instead of deleting it: a freed inode can be handed straight back to the new file,
		// and os.SameFile would then see no swap.
		Expect(os.Rename(filepath.Join(dir, "ref", "a.md"), filepath.Join(dir, "ref", "a-parked.md"))).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dir, "ref", "a.md"), []byte("another file"), 0o644)).To(Succeed())
		in, err := root.Open(filepath.FromSlash(target))
		Expect(err).NotTo(HaveOccurred())
		dst := filepath.Join(dir, "out", "l")
		Expect(copyOpened(in, dst, checked)).To(MatchError(ContainSubstring("changed while lola was reading it")))
		Expect(dst).NotTo(BeAnExistingFile())

		Expect(os.Remove(filepath.Join(dir, "ref", "a.md"))).To(Succeed())
		Expect(os.Symlink(filepath.Join(outside, "secret"), filepath.Join(dir, "ref", "a.md"))).To(Succeed())
		_, err = root.Open(filepath.FromSlash(target))
		Expect(err).To(HaveOccurred(), "the root never opens a file outside the source")
	})
})
