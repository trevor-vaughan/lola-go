package cli

import (
	"encoding/json"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("notice classes", func() {
	BeforeEach(func() { testenv.Isolate() })

	printed := func(s style, r *engine.Result) string {
		var b strings.Builder
		printNotices(&b, s, r)
		return b.String()
	}

	It("a finding or notice of a kind made without a class is safety: printed in full, and so classed in JSON", func() {
		r := &engine.Result{Findings: []target.Finding{{Severity: target.SeverityWarn, Module: "m", Target: "t", Kind: "skill", Artifact: "s", Code: "new-kind",
			Text: "a new kind", Hint: "do this", Check: "lola check --user m"}},
			Notices: []engine.Notice{{Level: engine.LevelWarn, Text: "m: a new notice"}}}
		Expect(printed(style{}, r)).To(Equal("⚠ m: t skill s: a new kind\n    hint: do this\n⚠ m: a new notice\n"))
		doc, err := json.Marshal(r)
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.Count(string(doc), `"class":"safety"`)).To(Equal(2), string(doc))
	})

	It("counts a detail notice with no target as the module's, in the line of its lola check, which names the targets of the rest", func() {
		r := &engine.Result{
			Findings: []target.Finding{{Severity: target.SeverityWarn, Module: "m", Target: "t", Kind: "skill", Artifact: "s", Code: "c", Text: "lost",
				Class: target.ClassDetail, Check: "lola check --user m"}},
			Notices: []engine.Notice{{Level: engine.LevelInfo, Text: "m: mentions skill/x → add it", Class: target.ClassDetail, Module: "m", Check: "lola check --user m"}}}
		Expect(printed(style{}, r)).To(Equal("→ m: 2 notices (t 1, module 1) — lola check --user m -t t\n"))
		Expect(printed(style{verbose: true}, r)).To(Equal("⚠ m: t skill s: lost\n→ m: mentions skill/x → add it\n"))
		Expect(printed(style{plain: true}, r)).To(Equal("finding\twarn\tm\tt\tskill\ts\tc\tlost\nnotice\tinfo\tm: mentions skill/x → add it\n"),
			"--plain keeps a row each")

		r.Findings = nil
		Expect(printed(style{}, r)).To(Equal("→ m: 1 notice (module 1) — lola check --user m\n"), "lola check lists mentions too")
	})

	It("prints in full the detail no lola check names", func() {
		r := &engine.Result{Findings: []target.Finding{{Severity: target.SeverityWarn, Module: "m", Target: "t", Kind: "skill", Artifact: "s", Code: "c",
			Text: "lost", Class: target.ClassDetail}},
			Notices: []engine.Notice{{Level: engine.LevelInfo, Text: "m: mentions skill/x", Class: target.ClassDetail, Module: "m"}}}
		Expect(printed(style{}, r)).To(Equal("⚠ m: t skill s: lost\n→ m: mentions skill/x\n"))
	})

	It("M-1: prints a line per scope, each with its scope's lola check", func() {
		f := func(check string) target.Finding {
			return target.Finding{Severity: target.SeverityWarn, Module: "m", Target: "t", Kind: "skill", Artifact: "s", Code: "c", Text: "lost",
				Class: target.ClassDetail, Check: check}
		}
		r := &engine.Result{Findings: []target.Finding{f("lola check --project m"), f("lola check --user m"), f("lola check --user m")}}
		Expect(printed(style{}, r)).To(Equal("→ m: 1 notice (t 1) — lola check --project m -t t\n→ m: 2 notices (t 2) — lola check --user m -t t\n"))
	})

	It("under -v prints, in place of a hint for the module's author, only what the engine added for the user", func() {
		f := target.Finding{Severity: target.SeveritySkip, Module: "m", Target: "t", Kind: "skill", Artifact: "s", Code: "c", Text: "no place",
			Class: target.ClassDetail, Hint: "rename it; or render it anyway: lola install m --force", AuthorHint: true, UserHint: "render it anyway: lola install m --force"}
		r := &engine.Result{Findings: []target.Finding{f, {Severity: target.SeverityWarn, Module: "m", Target: "t", Kind: "skill", Artifact: "z", Code: "d",
			Text: "lossy", Class: target.ClassDetail, Hint: "fix it in the module", AuthorHint: true}}}
		Expect(printed(style{verbose: true}, r)).To(Equal("✗ skipped m: t skill s: no place\n    hint: render it anyway: lola install m --force\n⚠ m: t skill z: lossy\n"))
	})
})
