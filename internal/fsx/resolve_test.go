package fsx_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/fsx"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("Resolve", func() {
	var d, root, ext string
	BeforeEach(func() {
		var err error
		d, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		root = filepath.Join(d, "proj")
		ext = filepath.Join(d, "dotfiles")
		Expect(os.MkdirAll(filepath.Join(root, ".claude"), 0o755)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(ext, "skills"), 0o755)).To(Succeed())
	})

	It("returns the plain path when nothing is a symlink", func() {
		r, err := fsx.Resolve(root, ".claude/commands/a.md")
		Expect(err).NotTo(HaveOccurred())
		Expect(r).To(Equal(fsx.Resolved{Logical: ".claude/commands/a.md", Abs: filepath.Join(root, ".claude/commands/a.md")}))
	})

	It("follows a symlinked directory and flags a destination outside the root", func() {
		Expect(os.Symlink(filepath.Join(ext, "skills"), filepath.Join(root, ".claude", "skills"))).To(Succeed())
		r, err := fsx.Resolve(root, ".claude/skills/x/SKILL.md")
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Abs).To(Equal(filepath.Join(ext, "skills/x/SKILL.md")))
		Expect(r.Outside).To(BeTrue())
		Expect(r.Logical).To(Equal(".claude/skills/x/SKILL.md"))
	})

	It("writes through a dangling relative file symlink", func() {
		Expect(os.Symlink("../dotfiles/CLAUDE.md", filepath.Join(root, "CLAUDE.md"))).To(Succeed())
		r, err := fsx.Resolve(root, "CLAUDE.md")
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Abs).To(Equal(filepath.Join(ext, "CLAUDE.md")))
	})

	It("treats a symlinked root as inside", func() {
		Expect(os.Symlink(root, filepath.Join(d, "link"))).To(Succeed())
		r, err := fsx.Resolve(filepath.Join(d, "link"), ".claude/x")
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Outside).To(BeFalse())
	})

	It("rejects a symlink loop", func() {
		Expect(os.Symlink("loop", filepath.Join(root, "loop"))).To(Succeed())
		_, err := fsx.Resolve(root, "loop")
		Expect(err).To(HaveOccurred())
	})

	It("rejects logical paths that escape the root", func() {
		for _, p := range []string{"../x", "/etc/passwd", "a/../../x"} {
			_, err := fsx.Resolve(root, p)
			Expect(err).To(HaveOccurred(), p)
		}
	})
})
