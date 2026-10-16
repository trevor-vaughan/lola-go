package store

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("Put", func() {
	It("refuses, caching nothing, a module whose cached copy would not load as the fetched one did", func() {
		testenv.Isolate()
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		s := New(p, []string{"claude-code"})
		s.normalize = func(dir string, m *module.Module) error {
			if err := module.Normalize(dir, m); err != nil {
				return err
			}
			f, err := os.OpenFile(filepath.Join(dir, "lola.yml"), os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				return err
			}
			_, err = f.WriteString("description: not what was fetched\n")
			return errors.Join(err, f.Close())
		}
		Expect(os.MkdirAll(p.Staging, 0o700)).To(Succeed())
		staged, err := os.MkdirTemp(p.Staging, "fetch-*")
		Expect(err).NotTo(HaveOccurred())
		Expect(os.MkdirAll(filepath.Join(staged, "commands"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(staged, "commands", "a.md"), []byte("A"), 0o644)).To(Succeed())

		_, err = s.Put(PutRequest{Staged: staged, FallbackName: "example", Source: "/src/example", Type: "folder", Now: time.Now()})
		Expect(err).To(MatchError("lola could not cache example faithfully: its cached copy would not read the same as the fetched one"))
		Expect(errs.Hints(err)).NotTo(BeEmpty())
		Expect(filepath.Join(p.Store, "example")).NotTo(BeAnExistingFile())
	})
})
