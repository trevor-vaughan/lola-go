package engine_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("the run budget", func() {
	It("a failed render is charged to the run budget, in check too", func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e := engine.New(p)
		proj, src := filepath.Join(tmp, "proj"), filepath.Join(tmp, "src", "kit")
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		writeTree(src, map[string]string{"commands/a.md.liquid": "{{ nope }}", "commands/b.md.liquid": "{{ nope }}", "commands/c.md.liquid": "{{ nope }}"})
		clock := time.Unix(0, 0)
		e.Now = func() time.Time { clock = clock.Add(time.Second); return clock } // every render takes 1 s
		e.TemplateBudget.RunTime = 2500 * time.Millisecond
		_, err = e.Check(context.Background(), engine.Scope{Kind: target.Project, ProjectDir: proj}, proj, []string{src}, []string{"claude-code"}, nil)
		te, ok := errors.AsType[*engine.TemplateError](err)
		Expect(ok).To(BeTrue(), "the third failed render passes 2.5 s: %v", err)
		Expect(te.Code).To(Equal("template-run-budget"))
	})
	It("fillContent shares the first sync pass's run budget", func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e := engine.New(p)
		proj := filepath.Join(tmp, "proj")
		e.Getwd = func() (string, error) { return proj, nil }
		writeTree(filepath.Join(proj, "mods", "a"), map[string]string{"lola.yml": "name: a\ntargets: [claude-code]\n", "commands/a.md.liquid": "aaaa"})
		writeTree(filepath.Join(proj, ".lola"), map[string]string{"project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: mods/a}\n"})
		sync := func() error {
			_, err := e.Sync(context.Background(), engine.SyncRequest{Scope: engine.Scope{Kind: target.Project, ProjectDir: proj},
				ConfirmRemovals: func([]engine.Removal) (bool, error) { return true, nil },
				Trust:           func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }})
			return err
		}
		e.TemplateBudget.RunOutput = 6 // the trust diff's render of a (4 bytes) and the plan's (4) pass it together
		err = sync()
		te, ok := errors.AsType[*engine.TemplateError](err)
		Expect(ok).To(BeTrue(), "%v", err)
		Expect(te.Code).To(Equal("template-run-budget"))
		e.TemplateBudget.RunOutput = 8
		Expect(sync()).To(Succeed())
	})
})
