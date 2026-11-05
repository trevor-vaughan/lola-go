package engine

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("the check hint", func() {
	It("a refusal with no resolved address carries no check hint", func() {
		tmp := testenv.Isolate()
		e := &Engine{Getwd: func() (string, error) { return tmp, nil }}
		sc := Scope{Kind: target.User}
		refused := &TemplateError{Module: "kit", Code: "template-undefined-variable"}
		const commit = "0123456789abcdef0123456789abcdef01234567"
		market := &manifest.MarketRef{Name: "acme", Source: "https://example.com/plugins.git"}
		for what, ent := range map[string]*manifest.Entry{
			"no entry":                               {},
			"a cached name's entry":                  {Name: "kit"},
			"a selection of a source":                {Name: "kit", Source: "/x/kit", Components: &source.Components{Skills: []string{"a"}}},
			"a git ref":                              {Name: "kit", Source: "https://example.com/kit.git", Ref: "v1"},
			"a pinned git entry":                     {Name: "kit", Source: "https://example.com/kit.git", Commit: commit, Digest: "sha256:x"},
			"a pinned market entry":                  {Name: "kit", Source: "https://example.com/plugins.git", Market: market, Commit: commit, Digest: "sha256:x"},
			"an address untrusted.Text would change": {Name: "kit", Source: "https://example.com/plugins.git", Market: &manifest.MarketRef{Name: "ac\x1bme"}},
		} {
			Expect(e.checkHint(sc, refused, ent, nil, []string{"--json"}, false)).To(BeEmpty(), what)
		}
		Expect(e.checkHint(sc, refused, nil, nil, nil, false)).To(BeEmpty(), "a module no layer wants")
		Expect(e.checkHint(sc, refused, &manifest.Entry{Name: "kit", Source: tmp}, nil, []string{"--json"}, false)).To(Equal("lola check --user " + tmp + " --json"))

		By("a fresh install, whose pin is the version a check fetches")
		Expect(e.checkHint(sc, refused, &manifest.Entry{Name: "kit", Source: "https://example.com/kit.git", Commit: commit, Digest: "sha256:x"},
			nil, []string{"--json"}, true)).To(Equal("lola check --user https://example.com/kit.git --json"))
		Expect(e.checkHint(sc, refused, &manifest.Entry{Name: "kit", Source: "https://example.com/plugins.git", Market: market, Commit: commit, Digest: "sha256:x"},
			nil, nil, true)).To(Equal("lola check --user @acme/kit"))

		By("a timeout and the run budget, which lola check refuses the same way")
		for _, code := range []string{codeTemplateTimeout, codeTemplateRunBudget} {
			Expect(e.checkHint(sc, &TemplateError{Module: "kit", Code: code}, &manifest.Entry{Name: "kit", Source: tmp}, nil, nil, true)).To(BeEmpty(), code)
		}
	})
})
