package engine_test

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("store operations", func() {
	var e *engine.Engine
	var src string
	ctx := context.Background()
	BeforeEach(func() {
		tmp := testenv.Isolate()
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		src = filepath.Join(tmp, "example")
		writeTree(src, map[string]string{"lola.yml": "name: example\ndescription: Review helpers\nkeywords: [Git]\n", "commands/a.md": "A"})
		_, err = e.Fetch(ctx, sourceSpec(src))
		Expect(err).NotTo(HaveOccurred())
	})

	It("Current returns the default-ref version and its origin", func() {
		v, o, err := e.Current("example")
		Expect(err).NotTo(HaveOccurred())
		Expect(v.Module.Description).To(Equal("Review helpers"))
		Expect(o.Source).To(Equal(src))
		_, _, err = e.Current("nope")
		Expect(err).To(MatchError("nope is not in your cache"))
		Expect(errs.Hints(err)).To(Equal([]string{"cached modules: lola cache list"}))
	})

	It("Update re-fetches each head and reports whether it moved", func() {
		ups, err := e.Update(ctx, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(ups).To(HaveLen(1))
		Expect(ups[0].Changed).To(BeFalse())
		writeTree(src, map[string]string{"commands/a.md": "B"})
		ups, err = e.Update(ctx, []string{"example"})
		Expect(err).NotTo(HaveOccurred())
		Expect(ups[0].Changed).To(BeTrue())
		Expect(ups[0].To).NotTo(Equal(ups[0].From))
		_, err = e.Update(ctx, []string{"nope"})
		Expect(err).To(MatchError("nope is not in your cache"))
	})

	It("Update refuses a source that renamed its module", func() {
		writeTree(src, map[string]string{"lola.yml": "name: renamed\n"})
		_, err := e.Update(ctx, []string{"example"})
		Expect(err).To(MatchError(ContainSubstring(`now declares module "renamed"`)))
	})

	It("Search matches name, description and keywords case-insensitively", func() {
		for _, q := range []string{"EXAM", "review", "git"} {
			hits, _, err := e.Search(q)
			Expect(err).NotTo(HaveOccurred())
			Expect(hits).To(HaveLen(1), q)
			Expect(hits[0].Where).To(Equal("cache"))
		}
		hits, _, err := e.Search("zzz")
		Expect(err).NotTo(HaveOccurred())
		Expect(hits).To(BeEmpty())
		Expect(hits).NotTo(BeNil())
	})
})
