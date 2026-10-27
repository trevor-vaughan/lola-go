//go:build unix

package module_test

import (
	"os"
	"path/filepath"
	"syscall"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

func mkfifo(path string) {
	Expect(syscall.Mkfifo(path, 0o600)).To(Succeed())
}

var _ = Describe("template files on unix", func() {
	It("a template file is checked as a regular file on the opened file", func() {
		root := filepath.Join(testenv.Isolate(), "mod")
		Expect(os.MkdirAll(filepath.Join(root, "agents"), 0o755)).To(Succeed())
		By("a FIFO")
		mkfifo(filepath.Join(root, "agents", "pipe.md.liquid"))
		_, err := module.Load(root, "kit", targets.IDs())
		Expect(err).To(MatchError("agents/pipe.md.liquid: a template must be a regular file"))
		Expect(os.Remove(filepath.Join(root, "agents", "pipe.md.liquid"))).To(Succeed())
		By("a directory named x.md.liquid refuses")
		for _, dir := range []string{"agents/x.md.liquid", "rules/x.md.liquid", "commands/c/x.md.liquid", "skills/s/x.md.liquid"} {
			Expect(os.RemoveAll(root)).To(Succeed())
			for _, d := range []string{dir, "commands", "skills/s"} {
				Expect(os.MkdirAll(filepath.Join(root, d), 0o755)).To(Succeed())
			}
			Expect(os.WriteFile(filepath.Join(root, "commands", "c.md"), []byte("c\n"), 0o644)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(root, "skills", "s", "SKILL.md"), []byte("s\n"), 0o644)).To(Succeed())
			_, err = module.Load(root, "kit", targets.IDs())
			Expect(err).To(MatchError(dir+": a template must be a regular file"), dir)
		}
		Expect(os.RemoveAll(root)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(root, "agents"), 0o755)).To(Succeed())

		By("a symlinked template file refuses")
		Expect(os.WriteFile(filepath.Join(root, "real.md"), []byte("x\n"), 0o644)).To(Succeed())
		Expect(os.Symlink(filepath.Join(root, "real.md"), filepath.Join(root, "agents", "link.md.liquid"))).To(Succeed())
		_, err = module.Load(root, "kit", targets.IDs())
		Expect(err).To(MatchError("agents/link.md.liquid: a template must be a regular file"))
	})
})

var _ = Describe("partial files on unix", func() {
	It("a partial is read only from a regular file", func() {
		root := filepath.Join(testenv.Isolate(), "mod")
		Expect(os.MkdirAll(filepath.Join(root, "_partials"), 0o755)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(root, "commands"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(root, "commands", "c.md"), []byte("c\n"), 0o644)).To(Succeed())
		By("a FIFO")
		mkfifo(filepath.Join(root, "_partials", "pipe.liquid"))
		_, err := module.Load(root, "kit", targets.IDs())
		Expect(err).To(MatchError("_partials/pipe.liquid: a partial must be a regular file"))
		Expect(os.Remove(filepath.Join(root, "_partials", "pipe.liquid"))).To(Succeed())

		By("a symbolic link to a file")
		Expect(os.WriteFile(filepath.Join(root, "real.liquid"), []byte("x\n"), 0o644)).To(Succeed())
		Expect(os.Symlink(filepath.Join(root, "real.liquid"), filepath.Join(root, "_partials", "link.liquid"))).To(Succeed())
		_, err = module.Load(root, "kit", targets.IDs())
		Expect(err).To(MatchError("_partials/link.liquid: a partial must be a regular file"))
		Expect(os.Remove(filepath.Join(root, "_partials", "link.liquid"))).To(Succeed())

		By("a symbolic link in place of _partials")
		Expect(os.RemoveAll(filepath.Join(root, "_partials"))).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(root, "elsewhere"), 0o755)).To(Succeed())
		Expect(os.Symlink(filepath.Join(root, "elsewhere"), filepath.Join(root, "_partials"))).To(Succeed())
		_, err = module.Load(root, "kit", targets.IDs())
		Expect(err).To(MatchError("_partials is not a directory; partials are _partials/<name>.liquid files"))
	})
})
