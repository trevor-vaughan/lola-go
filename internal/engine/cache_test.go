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

	It("Update re-fetches each head, reports whether it moved, and says installs keep their versions", func() {
		up, err := e.Update(ctx, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(up.Updates).To(HaveLen(1))
		Expect(up.Updates[0].Changed).To(BeFalse())
		Expect(up.Notices).To(BeEmpty())
		writeTree(src, map[string]string{"commands/a.md": "B"})
		up, err = e.Update(ctx, []string{"example"})
		Expect(err).NotTo(HaveOccurred())
		Expect(up.Updates[0].Changed).To(BeTrue())
		Expect(up.Updates[0].To).NotTo(Equal(up.Updates[0].From))
		Expect(up.Notices).To(Equal([]engine.Notice{{Level: engine.LevelInfo,
			Text: "installed modules keep the versions they were installed at; upgrade one with lola install <module>"}}))
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
			hits, _, err := e.Search(q, false, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(hits).To(HaveLen(1), q)
			Expect(hits[0].Where).To(Equal("cache"))
		}
		hits, _, err := e.Search("zzz", false, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(hits).To(BeEmpty())
		Expect(hits).NotTo(BeNil())
	})
})
