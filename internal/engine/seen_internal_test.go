package engine

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("state and event findings", func() {
	BeforeEach(func() { testenv.Isolate() })

	warn := func(code string) target.Finding {
		return target.Finding{Severity: target.SeverityWarn, Module: "kit", Target: "claude-code", Kind: "skill", Artifact: "a", Code: code, Text: "t"}
	}
	DescribeTable("isState",
		func(f target.Finding, state bool) { Expect(isState(f)).To(Equal(state)) },
		Entry("pre-approved tools", warn("claude-code/allowed-tools"), true),
		Entry("a permission mode", warn("claude-code/permission-mode"), true),
		Entry("a load warning", target.Finding{Severity: target.SeverityWarn, Module: "kit", Kind: "module", Code: "module-warning", Text: "w"}, true),
		Entry("native executables", target.Finding{Severity: target.SeverityWarn, Module: "kit", Kind: "module", Code: CodeNativeExecutable, Text: "n"}, true),
		Entry("a skipped artifact", target.Finding{Severity: target.SeveritySkip, Module: "kit", Target: "gemini-cli", Kind: "agent", Artifact: "r", Code: "x", Text: "t"}, true),
		Entry("declined runtime hooks", warn("claude-code/runtime-hooks-declined"), false),
		Entry("not in the manifest", target.Finding{Severity: target.SeverityWarn, Module: "kit", Code: "not-in-manifest", Text: "t"}, false),
		Entry("no module", target.Finding{Severity: target.SeverityWarn, Code: "version/chosen-era", Text: "t"}, false),
		Entry("an artifact rendered under --force", func() target.Finding { f := warn("claude-code/x"); f.Forced = true; return f }(), true),
		Entry("a warning with no artifact", func() target.Finding { f := warn("claude-code/x"); f.Artifact = ""; return f }(), false),
		Entry("a detail finding", func() target.Finding { f := warn("claude-code/allowed-tools"); f.Class = target.ClassDetail; return f }(), false),
	)

	It("keys a finding by module, target, kind, artifact, code and text alone", func() {
		f := warn("claude-code/allowed-tools")
		g := f
		g.Hint, g.Check, g.Scope = "h", "lola check kit", "project"
		Expect(safetyKey(g)).To(Equal(safetyKey(f)))
		g.Text = "t2"
		Expect(safetyKey(g)).NotTo(Equal(safetyKey(f)))
		Expect(safetyKeys([]target.Finding{f, f, warn("claude-code/runtime-hooks-declined")})).To(Equal([]string{safetyKey(f)}))
		Expect(safetyKeys(nil)).To(Equal([]string{}))
	})

	It("keys an artifact rendered under --force apart from the same one skipped", func() {
		skipped := warn("claude-code/x")
		skipped.Severity = target.SeveritySkip
		forced := warn("claude-code/x")
		forced.Forced = true
		Expect(safetyKey(forced)).NotTo(Equal(safetyKey(skipped)))
	})
})
