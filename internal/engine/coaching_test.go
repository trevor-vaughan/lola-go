package engine_test

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("finding hints", func() {
	var (
		proj, kit string
		e         *engine.Engine
		sc        engine.Scope
		ctx       = context.Background()
	)
	BeforeEach(func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		proj, kit = filepath.Join(tmp, "proj"), filepath.Join(tmp, "src", "kit")
		writeTree(kit, map[string]string{"lola.yml": "name: kit\n", "agents/critic.md": "Criticize.\n"})
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		sc = engine.Scope{Kind: target.Project, ProjectDir: proj}
	})
	// critic installs kit for claude-code with opts and returns the finding for its agent, which
	// has no description: a forceable skip.
	critic := func(opts engine.Options) target.Finding {
		GinkgoHelper()
		opts.Targets = []string{"claude-code"}
		res, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: kit}}, opts)
		Expect(err).NotTo(HaveOccurred())
		for _, f := range res.Findings {
			if f.Code == "required-key-missing" && f.Artifact == "critic" {
				return f
			}
		}
		Fail("no required-key-missing finding for critic")
		return target.Finding{}
	}

	It("ends a forceable skip's hint with the run that renders it anyway", func() {
		Expect(critic(engine.Options{ForceRun: "lola install ./kit --force"}).Hint).To(
			Equal("add description: to the agent's frontmatter; or render it anyway: lola install ./kit --force"))
	})

	It("leaves the hint of a forced finding, and of a run with no --force, as the target gave it", func() {
		f := critic(engine.Options{ForceRun: "lola install ./kit --force", Force: true})
		Expect(f.Forced).To(BeTrue())
		Expect(f.Hint).To(Equal("add description: to the agent's frontmatter"))
		Expect(critic(engine.Options{}).Hint).To(Equal("add description: to the agent's frontmatter"))
	})
})
