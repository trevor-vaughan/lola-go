package engine

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("the order InstallAll locks destinations in (Plan 5c §14)", func() {
	It("locks project roots by canonical key, then the user scope, whatever order they were chosen in", func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		a, b := filepath.Join(tmp, "a"), filepath.Join(tmp, "b")
		Expect(os.MkdirAll(a, 0o755)).To(Succeed())
		Expect(os.MkdirAll(b, 0o755)).To(Succeed())
		dest := func(kind target.Scope, dir string) ScopeInstall {
			return ScopeInstall{ScopeChoice: ScopeChoice{Scope: Scope{Kind: kind, ProjectDir: dir}}}
		}
		chosen := []ScopeInstall{dest(target.User, ""), dest(target.Project, b), dest(target.Project, a)}
		Expect(lockOrder(chosen)).To(Equal([]int{2, 1, 0}))
		Expect(lockOrder([]ScopeInstall{chosen[2], chosen[0], chosen[1]})).To(Equal([]int{0, 2, 1}))
	})
})
