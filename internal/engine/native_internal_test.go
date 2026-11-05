package engine

import (
	"os"
	"path/filepath"
	"runtime"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/native/nativetest"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("installedFiles", func() {
	var dir string
	put := func(rel string) {
		full := filepath.Join(dir, rel)
		Expect(os.MkdirAll(filepath.Dir(full), 0o755)).To(Succeed())
		Expect(os.WriteFile(full, nativetest.ELF64(), 0o755)).To(Succeed())
	}
	BeforeEach(func() { dir = testenv.Isolate() })

	It("skips the skill's excluded files and directories, its SKILL.md and its templated sources", func() {
		for _, f := range []string{"s/SKILL.md", "s/keep", "s/out/a", "s/ref/x.md.liquid", "s/b", "other/z"} {
			put(f)
		}
		m := &module.Module{Skills: []module.Skill{{Dir: "s", Exclude: []string{"out", "b"},
			Files: []module.SkillFile{{Source: "ref/x.md.liquid"}}}}}
		Expect(installedFiles(dir, m)).To(Equal([]string{"s/keep"}))
	})

	It("keeps module-level directories out of a root-level skill through its exclusions", func() {
		for _, f := range []string{"SKILL.md", "scripts/fmt", "tests/fixtures/fmt"} {
			put(f)
		}
		m := &module.Module{Skills: []module.Skill{{Dir: ".", Exclude: []string{"tests"}}}}
		Expect(installedFiles(dir, m)).To(Equal([]string{"scripts/fmt"}))
	})

	It("lists sidecar files except templated sources, once, sorted", func() {
		for _, f := range []string{"commands/ship/b", "commands/ship/a", "commands/ship/t.md.liquid", "commands/ship/s/c"} {
			put(f)
		}
		m := &module.Module{
			Skills:   []module.Skill{{Dir: "commands/ship/s"}},
			Commands: []module.Command{{SidecarDir: "commands/ship", Sidecars: []module.Sidecar{{Source: "commands/ship/t.md.liquid"}}}, {}},
		}
		Expect(installedFiles(dir, m)).To(Equal([]string{"commands/ship/a", "commands/ship/b", "commands/ship/s/c"}))
	})

	It("returns regular files only: a FIFO neither blocks nor is listed, and a symlink is skipped", func() {
		if runtime.GOOS == "windows" {
			Skip("FIFOs need a Unix system")
		}
		put("s/real")
		Expect(mkfifoForTest(filepath.Join(dir, "s", "pipe"))).To(Succeed())
		Expect(os.Symlink(filepath.Join(dir, "s", "real"), filepath.Join(dir, "s", "link"))).To(Succeed())
		m := &module.Module{Name: "kit", Skills: []module.Skill{{Dir: "s"}}}
		Expect(installedFiles(dir, m)).To(Equal([]string{"s/real"}))
		fs, err := nativeFindings(dir, m, "kit", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(fs).To(HaveLen(1))
		Expect(fs[0].Text).To(HavePrefix("ships 1 native executable: s/real ("))
	})
})
