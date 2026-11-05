package templating

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("the partial store", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("include and render are unavailable before partials, and no template reads a file", func() {
		const file = "skills/review/SKILL.md.liquid"
		for body, want := range map[string]*Error{
			"x\n{% include '/etc/passwd' %}": {Code: "template-include-name", File: file, Line: 2,
				Reason: "include takes a partial name in quotes, such as {% include 'header' %}"},
			"x\n{% include 'passwd' %}": {Code: "template-include-missing", File: file, Line: 2,
				Reason: "no partial passwd for claude-code (looked for _partials/passwd.claude-code.liquid, _partials/passwd.liquid)"},
			"x\n{% render '../x' %}": {Code: "template-tag-unavailable", File: file, Line: 2,
				Reason: "the render tag is not available in lola templates; use include"},
		} {
			_, err := Render(context.Background(), Source{File: file, Line: 1, Body: body}, Partials{}, Data{Target: "claude-code"}, DefaultLimits(), nil)
			Expect(err).To(Equal(want), body)
		}
		_, err := newStore(file, "claude-code", Partials{}, nil, 64).ReadTemplate("/etc/passwd")
		Expect(err).To(Equal(&storeRefusal{code: "template-syntax", reason: "no partial is served as /etc/passwd"}))
		Expect(errors.Is(err, fs.ErrNotExist)).To(BeFalse())
	})

	It("the store serves only the paths liquid asks for, and a nested include resolves as its includer's", func() {
		p := Partials{Files: map[string]string{
			"_partials/header.liquid":             "generic",
			"_partials/header.claude-code.liquid": "claude",
			"_partials/footer.opencode.liquid":    "opencode footer",
		}}
		const file = "skills/review/SKILL.md.liquid"
		key := filepath.Join("skills", "review", "header")
		for target, want := range map[string]string{"claude-code": "claude", "opencode": "generic", "gemini-cli": "generic"} {
			s := newStore(file, target, p, []string{"header", "footer"}, 64)
			b, err := s.ReadTemplate(key)
			Expect(err).NotTo(HaveOccurred(), target)
			Expect(string(b)).To(Equal(want), target)
			_, err = s.ReadTemplate(filepath.Join("skills", "review", "footer"))
			if target == "opencode" {
				Expect(err).NotTo(HaveOccurred(), "footer has only an opencode file")
			} else {
				Expect(err).To(MatchError("no partial footer for "+target+" (looked for _partials/footer."+target+".liquid, _partials/footer.liquid)"), target)
			}
		}
		s := newStore(file, "claude-code", p, []string{"header"}, 64)
		for _, path := range []string{"header", "_partials/header.liquid", filepath.Join("skills", "header"),
			filepath.Join("skills", "review", "header.liquid"), filepath.Join("commands", "header"), "/etc/passwd"} {
			_, err := s.ReadTemplate(path)
			Expect(err).To(HaveOccurred(), path)
			Expect(errors.Is(err, fs.ErrNotExist)).To(BeFalse(), path)
		}

		By("a nested include asks in its includer's directory, which is the template's")
		nested := Partials{Files: map[string]string{"_partials/a.liquid": "a{% include 'b' %}", "_partials/b.liquid": "b{% include 'c' %}", "_partials/c.liquid": "c"}}
		out, err := Render(context.Background(), Source{File: file, Line: 1, Body: "{% include 'a' %}"}, nested, Data{Target: "claude-code"}, DefaultLimits(), nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(out)).To(Equal("abc"))

		s = newStore(file, "claude-code", p, []string{"header"}, 3)
		for range 3 {
			_, err := s.ReadTemplate(key)
			Expect(err).NotTo(HaveOccurred())
		}
		_, err = s.ReadTemplate(key)
		Expect(err).To(MatchError("more than 3 includes in one render; lola reads at most 3 partials per file"))
		Expect(errors.Is(err, fs.ErrNotExist)).To(BeFalse())
	})
})
