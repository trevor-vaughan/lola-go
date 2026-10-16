package source_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("Parse", func() {
	var cwd string
	BeforeEach(func() {
		cwd = testenv.Isolate()
		Expect(os.MkdirAll(filepath.Join(cwd, "mods", "x"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(cwd, "file.txt"), nil, 0o644)).To(Succeed())
	})

	It("resolves relative and absolute directories", func() {
		s, ok, err := source.Parse("mods/x", cwd)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(s).To(Equal(source.Spec{Kind: source.KindFolder, Location: filepath.Join(cwd, "mods/x")}))
		s, ok, _ = source.Parse(filepath.Join(cwd, "mods", "x")+"/", "/")
		Expect(ok).To(BeTrue())
		Expect(s.Location).To(Equal(filepath.Join(cwd, "mods/x")))
	})

	It("accepts file:// URLs to directories", func() {
		s, ok, err := source.Parse("file://"+filepath.Join(cwd, "mods/x"), "/")
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(s.Location).To(Equal(filepath.Join(cwd, "mods/x")))
	})

	It("says 'not a source' for names, missing paths and plain files", func() {
		for _, arg := range []string{"example", "missing/dir", "file.txt"} {
			_, ok, err := source.Parse(arg, cwd)
			Expect(err).NotTo(HaveOccurred(), arg)
			Expect(ok).To(BeFalse(), arg)
		}
	})

	It("rejects file:// URLs with a remote host or a non-directory", func() {
		_, _, err := source.Parse("file://example.com/x", cwd)
		Expect(err).To(MatchError(ContainSubstring("remote host")))
		_, _, err = source.Parse("file://"+filepath.Join(cwd, "file.txt"), cwd)
		Expect(err).To(MatchError(ContainSubstring("not a directory")))
	})
})
