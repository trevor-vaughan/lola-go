package cli_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/native/nativetest"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("native executables in the install output", func() {
	It("warns on its own line, naming the module, the file and what it is", func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, kit := filepath.Join(tmp, "proj"), filepath.Join(tmp, "kit")
		writeTree(kit, map[string]string{"skills/demo/SKILL.md": "---\nname: demo\ndescription: Demo\n---\nbody\n"})
		Expect(os.MkdirAll(filepath.Join(kit, "skills/demo/scripts"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(kit, "skills/demo/scripts/fmt"), nativetest.ELF64(), 0o755)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")

		r := lola("install", kit, "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("⚠ kit: ships 1 native executable: skills/demo/scripts/fmt (ELF executable, x86-64"))
		Expect(r.out).To(ContainSubstring("hint: lola info kit lists each"))
	})
})
