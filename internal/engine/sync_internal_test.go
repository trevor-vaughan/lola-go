package engine

import (
	"errors"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("syncPinError", func() {
	ent := manifest.Entry{Name: "example", Source: "https://example.com/x.git", Ref: "main", Commit: strings.Repeat("a", 40)}

	It("re-pins after a failed fetch of the pinned commit only if the server refuses fetching by commit", func() {
		testenv.Isolate()
		err := syncPinError("example", "project.yml", ent, &commitFetchError{errors.New("fetch failed")})
		Expect(err).To(MatchError("fetch failed"))
		Expect(errs.Hints(err)).To(Equal([]string{"if the server refuses fetching by commit, re-pin to what main serves now: lola sync --update example"}))
	})

	It("never coaches a sync to install from the source when the source serves another commit than the pin", func() {
		testenv.Isolate()
		served := errs.WithHints(&servedCommitError{name: "example", location: ent.Source, got: strings.Repeat("b", 40), want: ent.Commit},
			"install what its source serves now: lola install "+ent.Source)
		err := syncPinError("example", "project.yml", ent, served)
		Expect(err).To(MatchError("example: " + ent.Source + " served commit " + strings.Repeat("b", 40) + " for " + ent.Commit))
		Expect(errs.Hints(err)).To(Equal([]string{"check the source before you re-pin it: lola sync --update example"}))
	})
})
