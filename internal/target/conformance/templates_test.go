package conformance_test

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// watched is a registered target that records each template its Plan is handed.
type watched struct {
	target.Target
	templates *[]string
}

func (w watched) Plan(m *module.Module, ctx target.PlanContext) ([]plan.Intent, []target.Finding, error) {
	saw := func(what string) { *w.templates = append(*w.templates, w.ID()+" "+what) }
	for _, s := range m.Skills {
		if s.Doc.Template != nil {
			saw("skill " + s.Name)
		}
	}
	for _, c := range m.Commands {
		if c.Doc.Template != nil {
			saw("command " + c.Name)
		}
		for _, sc := range c.Sidecars {
			if sc.Doc.Template != nil {
				saw("sidecar " + sc.File)
			}
		}
	}
	for _, a := range m.Agents {
		if a.Doc.Template != nil {
			saw("agent " + a.Name)
		}
	}
	for _, r := range m.Rules {
		if r.Template != nil {
			saw("rule " + r.Name)
		}
	}
	saw("planned")
	return w.Target.Plan(m, ctx)
}

var _ = Describe("templates and the registry", func() {
	It("no target in the live registry is handed a Doc whose Template is set", func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e := engine.New(p)
		var seen []string
		e.Targets = nil
		for _, t := range targets.All() {
			e.Targets = append(e.Targets, watched{Target: t, templates: &seen})
		}
		proj, src := filepath.Join(tmp, "proj"), filepath.Join(tmp, "src", "kit")
		for rel, body := range map[string]string{
			"skills/review/SKILL.md.liquid": "---\ndescription: {{ target | json }}\n---\nReview.\n",
			"skills/review/ref/a.md.liquid": "a {{ target }}\n",
			"commands/ship.md.liquid":       "Ship {{ target }}\n",
			"commands/ship/n.md.liquid":     "n {{ target }}\n",
			"agents/critic.md.liquid":       "---\ndescription: {{ 'c' | json }}\n---\nCriticize.\n",
			"rules/style.md.liquid":         "Style {{ target }}.\n",
		} {
			Expect(os.MkdirAll(filepath.Dir(filepath.Join(src, rel)), 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(src, rel), []byte(body), 0o644)).To(Succeed())
		}
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		e.Getwd = func() (string, error) { return proj, nil }
		_, err = e.Install(context.Background(), engine.Scope{Kind: target.Project, ProjectDir: proj}, proj,
			[]engine.InstallRequest{{Arg: src}}, engine.Options{Targets: targets.IDs()})
		Expect(err).NotTo(HaveOccurred())
		Expect(seen).To(ConsistOf("claude-code planned", "gemini-cli planned", "opencode planned"))
	})
})
