package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/templating"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("renderTemplates", func() {
	var (
		root string
		e    *Engine
		ctx  = context.Background()
		d    = templating.Data{Target: "claude-code", Scope: "project", Profiles: []string{"default"}}
	)
	BeforeEach(func() {
		root = filepath.Join(testenv.Isolate(), "kit")
		e = &Engine{TemplateLimits: templating.DefaultLimits(), TemplateBudget: TemplateBudget{RunOutput: 32 << 20, RunTime: time.Minute}, Now: time.Now}
	})
	load := func(files map[string]string) *module.Module {
		for p, c := range files {
			Expect(os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(root, p), []byte(c), 0o644)).To(Succeed())
		}
		m, err := module.Load(root, "kit", targets.IDs())
		Expect(err).NotTo(HaveOccurred())
		return m
	}
	refusal := func(m *module.Module) *TemplateError {
		_, err := e.renderTemplates(ctx, m, d, &templateRun{})
		te, ok := errors.AsType[*TemplateError](err)
		Expect(ok).To(BeTrue(), "%v", err)
		return te
	}

	It("renders every kind of template for the target and scope, leaving the module as loaded", func() {
		m := load(map[string]string{
			"skills/review/SKILL.md.liquid": "---\ndescription: {{ target | json }}\n---\nReview for {{ scope }}.\n",
			"skills/review/ref/a.md.liquid": "{{ profiles | join: ',' }}\n",
			"commands/ship.md.liquid":       "Ship for {{ target }}\n",
			"commands/ship/notes.md.liquid": "---\nn: {{ scope | json }}\n---\nnotes\n",
			"agents/critic.md.liquid":       "---\nname: critic\nmodel: {% if target == 'claude-code' %}opus{% else %}x{% endif %}\n---\nCriticize.\n",
			"rules/style.md.liquid":         "---\ndescription: {{ 'd' | json }}\n---\nStyle {{ target }}.\n",
			"agents/plain.md":               "---\ndescription: p\n---\nPlain.\n",
		})
		r, err := e.renderTemplates(ctx, m, d, &templateRun{})
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Skills[0].Doc).To(Equal(module.Doc{Frontmatter: map[string]any{"description": "claude-code"},
			RawFrontmatter: "description: \"claude-code\"\n", Body: "Review for project.\n"}))
		Expect(r.Skills[0].Files).To(Equal([]module.SkillFile{{Rel: "ref/a.md", Source: "ref/a.md.liquid", Body: "default\n"}}))
		Expect(r.Commands[0].Doc).To(Equal(module.Doc{Body: "Ship for claude-code\n"}))
		Expect(r.Commands[0].Sidecars[0].Doc.Render()).To(Equal("---\nn: \"project\"\n---\nnotes\n"))
		Expect(r.Agents[0].Doc.String("model")).To(Equal("opus"))
		Expect(r.Agents[1]).To(Equal(m.Agents[1]))
		Expect(r.Rules[0]).To(Equal(module.Rule{Name: "style", Body: "Style claude-code.\n"}))

		Expect(m.Skills[0].Doc.Template).NotTo(BeNil(), "the loaded module is not changed")
		Expect(m.Skills[0].Files[0].Body).To(Equal("{{ profiles | join: ',' }}\n"))
		Expect(m.Commands[0].Sidecars[0].Doc.Template).NotTo(BeNil())
	})

	It("a module without templates plans byte-for-byte as before", func() {
		plain := load(map[string]string{
			"skills/review/SKILL.md": "---\ndescription: r\n---\nReview.\n", "skills/review/ref/a.md": "a\n", "skills/review/x.liquid": "{{ x }}",
			"commands/ship.md": "Ship.\n", "commands/ship/n.md": "n\n", "agents/critic.md": "Criticize.\n", "rules/style.md": "Style.\n",
		})
		same, err := e.renderTemplates(ctx, plain, d, &templateRun{})
		Expect(err).NotTo(HaveOccurred())
		Expect(same).To(BeIdenticalTo(plain), "every target plans the module as loaded")
	})

	It("refuses a template error with the module, target, component, file and line", func() {
		te := refusal(load(map[string]string{"commands/ship.md.liquid": "a\n{{ targt }}\n"}))
		Expect(*te).To(Equal(TemplateError{Module: "kit", Target: "claude-code", Scope: "project", Kind: "command", Artifact: "ship",
			File: "commands/ship.md.liquid", Line: 2, Code: "template-undefined-variable", Reason: "undefined variable in {{ targt }}"}))
		Expect(te.Error()).To(Equal("kit: claude-code command ship: commands/ship.md.liquid:2: undefined variable in {{ targt }}"))

		te = refusal(load(map[string]string{"skills/review/SKILL.md": "x\n", "skills/review/ref/b.md.liquid": "{% capture x %}{% endcapture %}"}))
		Expect(te.Error()).To(Equal("kit: claude-code skill review: skills/review/ref/b.md.liquid:1: the capture tag is not available in lola templates"))
	})

	It("refuses a rendered frontmatter that does not parse, naming the rendered line", func() {
		te := refusal(load(map[string]string{"agents/critic.md.liquid": "---\na: 1\nb: [\n---\nx\n"}))
		Expect(te.Code).To(Equal("template-frontmatter"))
		Expect(te.Error()).To(MatchRegexp(`^kit: claude-code agent critic: agents/critic\.md\.liquid \(rendered for claude-code\): frontmatter: .+, rendered line [34]$`))
	})

	It("a rendered name that disagrees with the path refuses", func() {
		te := refusal(load(map[string]string{"agents/reviewer.md.liquid": "---\nname: {% if target == 'claude-code' %}other{% else %}reviewer{% endif %}\n---\nx\n"}))
		Expect(te.Code).To(Equal("template-name-mismatch"))
		Expect(te.Error()).To(Equal("kit: claude-code agent reviewer: agents/reviewer.md.liquid (rendered for claude-code): renders name: other, but its path names it reviewer"))
		other := d
		other.Target = "opencode"
		_, err := e.renderTemplates(ctx, load(map[string]string{}), other, &templateRun{})
		Expect(err).NotTo(HaveOccurred(), "the name matches for opencode")
	})

	It("a rendered Recheck or rule failure is a template error with rendered true, and a skip finding in check", func() {
		rendered := func(err error, file, reason string) {
			GinkgoHelper()
			te, ok := errors.AsType[*TemplateError](err)
			Expect(ok).To(BeTrue(), "%v", err)
			Expect(te.Code).To(Equal("template-rendered-check"))
			Expect(te.rendered).To(BeTrue())
			Expect(te.File).To(Equal(file))
			Expect(te.Reason).To(Equal(reason))
		}
		_, err := e.renderTemplates(ctx, load(map[string]string{"agents/critic.md.liquid": "---\nmcpServers: {{ target | json }}\n---\nx\n"}), d, &templateRun{})
		rendered(err, "agents/critic.md.liquid", "agent critic: frontmatter mcpServers: must be a list of server names and inline servers")
		Expect(err).To(MatchError("kit: claude-code agent critic: agents/critic.md.liquid (rendered for claude-code): " +
			"agent critic: frontmatter mcpServers: must be a list of server names and inline servers"))
		Expect(errs.Hints(err)).To(ContainElement(HavePrefix("write each server as a list item")), "the load check's coaching stays")

		Expect(os.RemoveAll(root)).To(Succeed())
		_, err = e.renderTemplates(ctx, load(map[string]string{"commands/ship.md": "x\n", "commands/ship/n.md.liquid": "---\nhooks: {}\n---\nx\n"}), d, &templateRun{})
		rendered(err, "commands/ship/n.md.liquid", "command ship: commands/ship/n.md: frontmatter hooks: Claude Code reads this file as a command, "+
			"so lola would install its hooks without asking")

		Expect(os.RemoveAll(root)).To(Succeed())
		_, err = e.renderTemplates(ctx, load(map[string]string{"rules/style.md.liquid": "---\npaths: {{ 'src' | json }}\n---\nx\n"}), d, &templateRun{})
		rendered(err, "rules/style.md.liquid", "a rule applies in every session, so paths: is not supported")

		m := load(map[string]string{"agents/ok.md.liquid": "OK\n"})
		out, refused, err := e.checkTemplates(ctx, m, d, &templateRun{})
		Expect(err).NotTo(HaveOccurred(), "check reports each and goes on")
		Expect(refused).To(HaveLen(1))
		Expect(refused[0].finding()).To(Equal(target.Finding{Severity: target.SeveritySkip, Module: "kit", Target: "claude-code", Kind: "rule",
			Artifact: "style", Code: "template-rendered-check",
			Text: "rules/style.md.liquid (rendered for claude-code): a rule applies in every session, so paths: is not supported",
			Hint: "path-scoped guidance belongs in a skill: skills/<name>/SKILL.md"}), "the file once, and the load check's coaching kept")
		Expect(out.Rules).To(BeEmpty())
		Expect(out.Agents).To(HaveLen(1))
	})
})
