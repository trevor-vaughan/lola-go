package cli

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("grouping findings", func() {
	BeforeEach(func() { testenv.Isolate() })

	warn := func(module, artifact, code, text string) target.Finding {
		return target.Finding{Severity: target.SeverityWarn, Module: module, Target: "claude-code", Kind: "skill", Artifact: artifact, Code: code, Text: text}
	}
	lines := func(fs ...target.Finding) []string {
		var out []string
		for _, g := range groupFindings(fs) {
			out = append(out, groupLines(style{}, g)...)
		}
		return out
	}

	It("groups one warning shared by several modules into a summary naming the modules", func() {
		mw := func(m string) target.Finding {
			return target.Finding{Severity: target.SeverityWarn, Module: m, Kind: "module", Code: "module-warning", Text: "AGENTS.md in a module is not installed"}
		}
		Expect(lines(mw("a"), mw("b"), mw("c"))).To(Equal([]string{
			"⚠ AGENTS.md in a module is not installed (3 modules)", "    a", "    b", "    c"}))
	})

	It("groups pre-approved tools per module, target and kind, each artifact with its tools", func() {
		Expect(lines(
			warn("kit", "lint", "claude-code/allowed-tools", "pre-approves tools Claude Code then runs without asking: Bash"),
			warn("kit", "fmt", "claude-code/allowed-tools", "pre-approves tools Claude Code then runs without asking: Read, Write"),
		)).To(Equal([]string{
			"⚠ kit: pre-approves tools Claude Code then runs without asking (2 claude-code skills)",
			"    lint: Bash", "    fmt: Read, Write"}))
	})

	It("splits the tools at the first \": \" only, and leaves a text without one to the other rules", func() {
		Expect(lines(
			warn("kit", "a", "claude-code/allowed-tools", "pre-approves tools: mcp__x: y"),
			warn("kit", "b", "claude-code/allowed-tools", "pre-approves tools: Bash"),
		)).To(ContainElement("    a: mcp__x: y"))
		Expect(lines(warn("kit", "a", "claude-code/allowed-tools", "odd"), warn("kit", "b", "claude-code/allowed-tools", "odd"))).To(Equal([]string{
			"⚠ kit: claude-code skill: odd (2 skills)", "    a", "    b"}))
	})

	It("groups two findings that differ only in their artifact, listing every artifact", func() {
		Expect(lines(warn("kit", "a", "c", "a dropped"), warn("kit", "b", "c", "b dropped"))).To(Equal([]string{
			"⚠ kit: claude-code skill: … dropped (2 skills)", "    a", "    b"}))
	})

	It("prints a group of one as its finding line", func() {
		Expect(lines(warn("kit", "a", "c", "t"))).To(Equal([]string{"⚠ kit: claude-code skill a: t"}))
	})
	It("lists a module once and counts distinct modules when it repeats the finding", func() {
		mw := func(m string) target.Finding {
			return target.Finding{Severity: target.SeverityWarn, Module: m, Kind: "module", Code: "module-warning", Text: "t"}
		}
		Expect(lines(mw("a"), mw("a"), mw("b"))).To(Equal([]string{"⚠ t (2 modules)", "    a", "    b"}))
	})
	It("keeps the same text in two modules apart when the artifacts differ", func() {
		Expect(lines(warn("m1", "a", "c", "t"), warn("m2", "b", "c", "t"))).To(Equal([]string{
			"⚠ m1: claude-code skill a: t", "⚠ m2: claude-code skill b: t"}))
	})
	It("groups by module before pre-approved tools, so tools shared by two modules list the modules", func() {
		Expect(lines(
			warn("m1", "a", "claude-code/allowed-tools", "pre-approves tools: Bash"),
			warn("m2", "a", "claude-code/allowed-tools", "pre-approves tools: Bash"),
		)).To(HaveLen(3))
		Expect(lines(
			warn("m1", "a", "claude-code/allowed-tools", "pre-approves tools: Bash"),
			warn("m2", "a", "claude-code/allowed-tools", "pre-approves tools: Bash"),
		)[0]).To(ContainSubstring("(2 modules)"))
	})
	It("does not group a forced pre-approved-tools finding with an unforced one, so its marker prints", func() {
		forced := warn("kit", "a", "claude-code/allowed-tools", "pre-approves tools: Bash")
		forced.Forced = true
		out := strings.Join(lines(forced, warn("kit", "b", "claude-code/allowed-tools", "pre-approves tools: Bash")), "\n")
		Expect(out).To(ContainSubstring("rendered anyway"))
		Expect(strings.Count(out, "⚠")).To(Equal(2), out)
	})
})
