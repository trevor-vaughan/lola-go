package engine

import (
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/templating"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("the rendered name check", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("a rendered name that is not text refuses, naming its kind", func() {
		job := func(front string) templateJob {
			return templateJob{kind: "agent", name: "critic", src: templating.Source{Module: "kit", File: "agents/critic.md.liquid", Line: 1, Body: "---\n" + front + "---\n", Frontmatter: true}}
		}
		d := templating.Data{Target: "claude-code", Scope: "project"}
		for front, kind := range map[string]string{
			"name: 5\n": "a number", "name: true\n": "true or false", "name: [a]\n": "a list", "name: {a: 1}\n": "a map", "name: ~\n": "nil",
			"name: 2024-01-02\n": "a value of an unsupported kind",
		} {
			_, err := renderedDoc(job(front), templating.Rendered{Front: []byte(front), HasFront: true}, d, true)
			te, ok := errors.AsType[*TemplateError](err)
			Expect(ok).To(BeTrue(), "%s: %v", front, err)
			Expect(te.Code).To(Equal("template-name-mismatch"), front)
			Expect(te.Error()).To(Equal("kit: claude-code agent critic: agents/critic.md.liquid (rendered for claude-code): name must be text; it is "+kind), front)
		}
		_, err := renderedDoc(job("name: critic\n"), templating.Rendered{Front: []byte("name: critic\n"), HasFront: true}, d, true)
		Expect(err).NotTo(HaveOccurred())
	})
})
