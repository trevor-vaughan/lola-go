package cli

import (
	"bytes"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("an install's skip lines over two scopes", func() {
	BeforeEach(func() { testenv.Isolate() })

	// printed is printInstall's human output of modules installed at a project and at user scope.
	printed := func(modules ...engine.ModuleResult) string {
		var b bytes.Buffer
		printInstall(&b, style{}, &engine.InstallResult{Result: &engine.Result{Modules: modules},
			Scopes: []engine.ScopeResult{{Scope: "project", Root: "/p"}, {Scope: "user"}}})
		return b.String()
	}
	kit := func(scope, root, in string, skipped ...string) engine.ModuleResult {
		return engine.ModuleResult{Name: "kit", Scope: scope, Root: root, Skipped: skipped, SkippedIn: in}
	}

	It("says a line both scopes share once, and names the scope of lines that differ", func() {
		out := printed(kit("project", "/p", "profile go", "skill/b"), kit("user", "", "profile go", "skill/b"))
		Expect(strings.Count(out, "→ kit: skipped in profile go: skill/b\n")).To(Equal(1), out)

		out = printed(kit("project", "/p", "profile go", "skill/b"), kit("user", "", "selections.yaml", "skill/a"))
		Expect(out).To(And(ContainSubstring("→ kit, project (/p): skipped in profile go: skill/b\n"),
			ContainSubstring("→ kit, user: skipped in selections.yaml: skill/a\n")))
	})
})

var _ = Describe("Pick-P2-g: an install's lines for components only: leaves out", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("words them as not in only:, apart from those a skip: leaves out", func() {
		var b bytes.Buffer
		m := engine.ModuleResult{Name: "kit", Scope: "user", Skipped: []string{"skill/a", "skill/b", "skill/c"}, SkippedIn: "selections.yaml",
			NotInOnly: []string{"skill/b", "skill/c"}, OnlyIn: "selections.yaml projects[/p]"}
		printInstall(&b, style{}, &engine.InstallResult{Result: &engine.Result{Modules: []engine.ModuleResult{m}}, Scopes: []engine.ScopeResult{{Scope: "user"}}})
		Expect(b.String()).To(And(ContainSubstring("→ kit: skipped in selections.yaml: skill/a\n"),
			ContainSubstring("→ kit: not in only: (selections.yaml projects[/p]): skill/b, skill/c\n")))

		b.Reset()
		m.Skipped, m.SkippedIn = m.NotInOnly, ""
		printInstall(&b, style{}, &engine.InstallResult{Result: &engine.Result{Modules: []engine.ModuleResult{m}}, Scopes: []engine.ScopeResult{{Scope: "user"}}})
		Expect(b.String()).NotTo(ContainSubstring("skipped in"))
		Expect(b.String()).To(ContainSubstring("→ kit: not in only: (selections.yaml projects[/p]): skill/b, skill/c\n"))

		b.Reset()
		printInstall(&b, style{}, &engine.InstallResult{Result: &engine.Result{Modules: []engine.ModuleResult{m}, DryRun: true}, Scopes: []engine.ScopeResult{{Scope: "user"}}})
		Expect(b.String()).To(ContainSubstring("→ kit: would be not in only: (selections.yaml projects[/p]): skill/b, skill/c\n"), "a dry run saves nothing")
	})
})

var _ = Describe("printFindings grouping", func() {
	BeforeEach(func() { testenv.Isolate() })

	mk := func(n int, text string) []target.Finding {
		var out []target.Finding
		for i := 0; i < n; i++ {
			out = append(out, target.Finding{Severity: target.SeverityWarn, Module: "ecc", Target: "opencode",
				Kind: "agent", Artifact: fmt.Sprintf("agent-%d", i), Code: "model-dropped", Text: text})
		}
		return out
	}
	render := func(s style, f []target.Finding) string {
		var b strings.Builder
		printFindings(&b, s, f)
		return b.String()
	}

	It("prints three identical findings one by one", func() {
		Expect(strings.Count(render(style{}, mk(3, "model dropped")), "⚠")).To(Equal(3))
	})
	It("merges four identical findings into one line with a count and a preview", func() {
		out := render(style{}, mk(4, "model dropped"))
		Expect(strings.Count(out, "⚠")).To(Equal(1), out)
		Expect(out).To(ContainSubstring("ecc: opencode agent: model dropped (4 agents: "))
		Expect(out).To(ContainSubstring("agent-0, agent-1, agent-2"))
		Expect(out).NotTo(ContainSubstring("agent-3"))
		Expect(out).To(ContainSubstring("--json"))
	})
	It("keeps the kind in the location and pluralises it for another kind", func() {
		f := mk(4, "model dropped")
		for i := range f {
			f[i].Kind = "skill"
		}
		Expect(render(style{}, f)).To(ContainSubstring("ecc: opencode skill: model dropped (4 skills: "))
	})
	It("does not merge findings whose text differs beyond the artifact", func() {
		f := mk(4, "model dropped")
		f[3].Text = "model dropped: opencode needs another string"
		Expect(strings.Count(render(style{}, f), "⚠")).To(Equal(4))
	})
	It("merges when the artifact name is inside the text", func() {
		f := mk(4, "")
		for i := range f {
			f[i].Text = "agent " + f[i].Artifact + " lost its colour"
		}
		out := render(style{}, f)
		Expect(strings.Count(out, "⚠")).To(Equal(1), out)
		Expect(out).To(ContainSubstring("agent … lost its colour"))
	})
	It("prints a merged group's hint once", func() {
		f := mk(5, "model dropped")
		for i := range f {
			f[i].Hint = "pick a model"
		}
		Expect(strings.Count(render(style{}, f), "hint: pick a model")).To(Equal(1))
	})
	It("merges findings whose hint names the artifact and prints the hint once with a placeholder", func() {
		f := mk(4, "model dropped")
		for i := range f {
			f[i].Hint = "in " + f[i].Artifact + ".targets.yaml beside the agent"
		}
		out := render(style{}, f)
		Expect(strings.Count(out, "⚠")).To(Equal(1), out)
		Expect(strings.Count(out, "hint: in <artifact>.targets.yaml beside the agent")).To(Equal(1), out)
		Expect(out).NotTo(ContainSubstring("hint: in agent-0"))
	})
	It("does not merge findings whose hints differ beyond the artifact name", func() {
		f := mk(4, "model dropped")
		for i := range f {
			f[i].Hint = "in " + f[i].Artifact + ".targets.yaml"
		}
		f[3].Hint = "in " + f[3].Artifact + ".other.yaml"
		Expect(strings.Count(render(style{}, f), "⚠")).To(Equal(4))
	})
	It("merges findings whose hint does not name the artifact and prints it unchanged", func() {
		f := mk(4, "model dropped")
		for i := range f {
			f[i].Hint = "pick a model"
		}
		out := render(style{}, f)
		Expect(strings.Count(out, "⚠")).To(Equal(1), out)
		Expect(out).To(ContainSubstring("hint: pick a model"))
	})
	It("never merges findings that have no artifact", func() {
		f := mk(5, "model dropped")
		for i := range f {
			f[i].Artifact = ""
		}
		Expect(strings.Count(render(style{}, f), "⚠")).To(Equal(5))
	})
	It("keeps groups in order of first appearance", func() {
		a, b := mk(4, "first"), mk(4, "second")
		out := render(style{}, append(append(append([]target.Finding{}, a[0]), b...), a[1:]...))
		Expect(strings.Index(out, "first")).To(BeNumerically("<", strings.Index(out, "second")), out)
	})
	DescribeTable("names each kind's artifacts in the plural",
		func(kind, plural string) {
			f := mk(4, "model dropped")
			for i := range f {
				f[i].Kind = kind
			}
			Expect(render(style{}, f)).To(ContainSubstring("(4 " + plural + ": "))
		},
		Entry(nil, "skill", "skills"),
		Entry(nil, "command", "commands"),
		Entry(nil, "agent", "agents"),
		Entry(nil, "rule", "rules"),
		Entry(nil, "mcp", "MCP servers"),
		Entry(nil, "module", "modules"),
		Entry(nil, "runtime-hook", "runtime-hooks"),
		Entry(nil, "", "findings"),
	)
	It("keeps a command word that is also an artifact's name, printing that finding on its own line", func() {
		f := mk(5, "model dropped")
		f[0].Artifact = "lola"
		for i := range f {
			f[i].Hint = "run lola sync for " + f[i].Artifact
		}
		out := render(style{}, f)
		Expect(strings.Count(out, "⚠")).To(Equal(2), out)
		Expect(out).To(ContainSubstring("hint: run lola sync for lola\n"))
		Expect(out).To(ContainSubstring("hint: run lola sync for <artifact>\n"))
		Expect(out).NotTo(ContainSubstring("<artifact> sync"))
	})
	It("replaces the artifact's name only where it stands whole", func() {
		f := mk(4, "")
		f[0].Artifact = "md"
		for i := range f {
			f[i].Text = "skills/" + f[i].Artifact + "/SKILL.md.liquid: " + f[i].Artifact + " lost its colour"
			f[i].Hint = "edit " + f[i].Artifact + ".md, then lola sync"
		}
		out := render(style{}, f)
		Expect(strings.Count(out, "⚠")).To(Equal(1), out)
		Expect(out).To(ContainSubstring("skills/…/SKILL.md.liquid: … lost its colour (4 agents: md, agent-1, agent-2, …"))
		Expect(out).To(ContainSubstring("hint: edit <artifact>.md, then lola sync\n"))
	})
	It("does not merge findings that repeat one artifact, where its name cannot be told from other words", func() {
		f := mk(4, "model dropped")
		for i := range f {
			f[i].Artifact = "lola"
			f[i].Hint = "run lola sync for lola"
		}
		out := render(style{}, f)
		Expect(strings.Count(out, "⚠")).To(Equal(4), out)
		Expect(out).NotTo(ContainSubstring("<artifact>"))
	})
	It("never groups --plain output", func() {
		Expect(strings.Count(render(style{plain: true}, mk(5, "model dropped")), "finding\t")).To(Equal(5))
	})
	It("prints nothing for a finding of unknown severity, and a row for it under --plain", func() {
		f := []target.Finding{{Severity: "bogus", Module: "ecc", Text: "x"}}
		Expect(render(style{}, f)).To(BeEmpty())
		Expect(render(style{plain: true}, f)).To(Equal("finding\tbogus\tecc\t\t\t\t\tx\n"))
	})
})

var _ = Describe("a plan step's bracket", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("says an adopted file is identical and not rewritten, and a replaced one that it replaces a file lola did not write", func() {
		adopt := stepLine(style{}, plan.Step{Action: plan.ActionAdopt, Target: "claude-code", Path: ".claude/skills/a/SKILL.md", Replaced: true})
		Expect(adopt).To(ContainSubstring("adopt"))
		Expect(adopt).To(ContainSubstring("[identical to a file lola did not write; kept, not rewritten]"))
		Expect(adopt).NotTo(ContainSubstring("replaces"))
		update := stepLine(style{}, plan.Step{Action: plan.ActionUpdate, Target: "claude-code", Path: ".claude/skills/a/SKILL.md", Replaced: true})
		Expect(update).To(ContainSubstring("[replaces a file lola did not write]"))
	})
})
