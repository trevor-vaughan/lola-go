package manifest_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("manifest", func() {
	var path string
	BeforeEach(func() { path = filepath.Join(testenv.Isolate(), ".lola", "project.yml") })

	It("loads a missing file as version 1 with no modules", func() {
		m, err := manifest.Load(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(m).To(Equal(&manifest.Manifest{Version: 1}))
	})

	It("upserts, finds, removes, and saves sorted by name", func() {
		m := &manifest.Manifest{Version: 1, Targets: []string{"claude-code"}}
		m.Upsert(manifest.Entry{Name: "zeta", Source: "../zeta"})
		m.Upsert(manifest.Entry{Name: "alpha", Source: "https://x/a.git", Ref: "v1"})
		m.Upsert(manifest.Entry{Name: "zeta", Source: "../zeta", Targets: []string{"claude-code"}})
		Expect(m.Find("zeta").Targets).To(Equal([]string{"claude-code"}))
		Expect(m.Find("nope")).To(BeNil())
		Expect(m.Save(path)).To(Succeed())
		Expect(os.ReadFile(path)).To(MatchYAML(`
version: 1
targets: [claude-code]
modules:
  - name: alpha
    source: https://x/a.git
    ref: v1
  - name: zeta
    source: ../zeta
    targets: [claude-code]
`))
		Expect(m.Remove("alpha")).To(BeTrue())
		Expect(m.Remove("alpha")).To(BeFalse())
		back, err := manifest.Load(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(back.Modules).To(HaveLen(2))
	})

	It("rejects unknown keys and newer versions", func() {
		Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
		Expect(os.WriteFile(path, []byte("version: 1\nmodule: []\n"), 0o600)).To(Succeed())
		_, err := manifest.Load(path)
		Expect(err).To(MatchError(ContainSubstring("module")))
		Expect(os.WriteFile(path, []byte("version: 9\n"), 0o600)).To(Succeed())
		_, err = manifest.Load(path)
		Expect(err).To(MatchError(ContainSubstring("newer lola")))
	})
})
