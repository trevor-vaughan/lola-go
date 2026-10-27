package engine

import (
	"errors"
	"fmt"
	"io/fs"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("re-rendering after a switch", func() {
	It("a cache read error while rendering is returned, never a fallback", func() {
		e := &Engine{}
		led := ledger.New()
		led.Modules["r"] = &ledger.Entry{Digest: "sha256:" + fmt.Sprintf("%064d", 7), RenderedWith: &ledger.RenderedWith{Profiles: []string{"user:old"}, Vars: map[string]any{}}}
		list := []ledger.Active{{Name: "user:work", By: ledger.ByOn}}
		sc := Scope{Kind: target.Project, ProjectDir: testenv.Isolate()}

		broken := errors.New("cached version r/000000000000: permission denied")
		_, err := e.rerenders(sc, led, list, func(string, string) (store.Version, error) { return store.Version{}, broken })
		Expect(err).To(MatchError(broken))

		gone := func(string, string) (store.Version, error) {
			return store.Version{}, fmt.Errorf("lost: %w", fs.ErrNotExist)
		}
		names, err := e.rerenders(sc, led, list, gone)
		Expect(err).NotTo(HaveOccurred(), "a version the cache lost falls back to the recorded profiles")
		Expect(names).To(Equal([]string{"r"}))
	})
})
