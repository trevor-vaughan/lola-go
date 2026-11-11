package engine

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("re-rendering after a switch", func() {
	It("a cache read error while rendering is returned, never a fallback", func() {
		e := &Engine{}
		led := ledger.New()
		led.Modules["r"] = &ledger.Entry{Digest: "sha256:" + fmt.Sprintf("%064d", 7), RenderedWith: &ledger.RenderedWith{Profiles: []string{"user:old"}, Vars: map[string]any{}}}
		active := &ledger.Active{Name: "user:work", By: ledger.ByOn}
		sc := Scope{Kind: target.Project, ProjectDir: testenv.Isolate()}

		broken := errors.New("cached version r/000000000000: permission denied")
		_, err := e.rerenders(sc, led, active, func(string, string) (store.Version, error) { return store.Version{}, broken })
		Expect(err).To(MatchError(broken))

		gone := func(string, string) (store.Version, error) {
			return store.Version{}, fmt.Errorf("lost: %w", fs.ErrNotExist)
		}
		names, err := e.rerenders(sc, led, active, gone)
		Expect(err).NotTo(HaveOccurred(), "a version the cache lost falls back to the recorded profiles")
		Expect(names).To(Equal([]string{"r"}))
	})

	It("lists a module whose files were written for another era than the scope now declares", func() {
		proj := testenv.Isolate()
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(proj, ".lola", "project.yml"), []byte("version: 1\ntarget_versions: {opencode: \"1.4\"}\n"), 0o600)).To(Succeed())
		reg, err := targets.Load(paths.Paths{})
		Expect(err).NotTo(HaveOccurred())
		e := &Engine{Registry: reg}
		led := ledger.New()
		led.Modules["stale"] = &ledger.Entry{Targets: []string{"opencode"}}
		led.Modules["kept"] = &ledger.Entry{Targets: []string{"opencode"}, Eras: map[string]string{"opencode": "1.0"}}
		led.Modules["other"] = &ledger.Entry{Targets: []string{"claude-code"}}
		names, err := e.rerenders(Scope{Kind: target.Project, ProjectDir: proj}, led, nil, func(string, string) (store.Version, error) {
			return store.Version{}, fs.ErrNotExist
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(names).To(Equal([]string{"stale"}), "an entry without eras was written for the newest")
	})
})
