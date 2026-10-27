package engine

import (
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("checkVars", func() {
	It("refuses an override of a kind no entry can set, whatever the default is", func() {
		testenv.Isolate()
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e := New(p)
		m := &module.Module{Name: "kit", Vars: map[string]any{"since": "x", "n": "1", "tools": []string{"rg"}}}
		for k, v := range map[string]any{"since": time.Time{}, "n": uint64(1), "tools": []any{"rg"}} {
			err := e.checkVars(Scope{Kind: target.User}, m, &manifest.Entry{Name: "kit", Vars: map[string]any{k: v}}, "", "", nil)
			want := "user.yml: modules[kit].vars." + k + ": kit's default is a string; this is unsupported"
			if k == "tools" {
				want = "user.yml: modules[kit].vars.tools: kit's default is a list of strings; this is unsupported"
			}
			Expect(err).To(MatchError(want), k)
		}
	})
})

var _ = Describe("checksEntry", func() {
	It("matches a marketplace address to the entry of that marketplace and name only", func() {
		testenv.Isolate()
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e := New(p)
		sc := Scope{Kind: target.User}
		ent := manifest.Entry{Name: "kit", Source: "https://example.com/kit.git", Market: &manifest.MarketRef{Name: "acme", Source: "https://example.com/acme.git"}}
		Expect(e.checksEntry(sc, "/", "@acme/kit", ent)).To(BeTrue())
		Expect(e.checksEntry(sc, "/", "@other/kit", ent)).To(BeFalse(), "another marketplace")
		Expect(e.checksEntry(sc, "/", "@acme/tool", ent)).To(BeFalse(), "another module")
		ent.Market = nil
		Expect(e.checksEntry(sc, "/", "@acme/kit", ent)).To(BeFalse(), "an entry not from a marketplace")
	})
})
