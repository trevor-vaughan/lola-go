package cli

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// run is a run's result: caveman with a granted and a consented runtime hook and two pre-approved
// tool lists, kit with a seen permission mode, both with one load warning, and kit's steps.
func run(steps int) *engine.Result {
	tools := func(a, t string) target.Finding {
		return target.Finding{Severity: target.SeverityWarn, Module: "caveman", Target: "claude-code", Kind: "skill", Artifact: a,
			Code: "claude-code/allowed-tools", Text: "pre-approves tools Claude Code then runs without asking: " + t, New: new(true)}
	}
	agents := func(m string) target.Finding {
		return target.Finding{Severity: target.SeverityWarn, Module: m, Kind: "module", Code: "module-warning", Text: "AGENTS.md in a module is not installed", New: new(true)}
	}
	hook := func(cmd, status string) engine.RuntimeHook {
		return engine.RuntimeHook{Target: "claude-code", Event: "Stop", Type: "command", Command: cmd, Status: status, Settings: ".claude/settings.json",
			New: new(status != engine.RuntimeHooksConsented)}
	}
	r := &engine.Result{
		Modules: []engine.ModuleResult{
			{Name: "caveman", Targets: []string{"claude-code"}, Info: "lola info caveman",
				RuntimeHooks: []engine.RuntimeHook{hook("./new.sh", engine.RuntimeHooksGranted), hook("./old.sh", engine.RuntimeHooksConsented)}},
			{Name: "kit", Targets: []string{"claude-code"}, Info: "lola info kit"},
		},
		Findings: []target.Finding{tools("lint", "Bash"), tools("fmt", "Read, Write"), agents("caveman"), agents("kit"),
			{Severity: target.SeverityWarn, Module: "kit", Target: "claude-code", Kind: "agent", Artifact: "rev", Code: "claude-code/permission-mode",
				Text: "sets the permission mode it runs in: plan", Check: "lola check kit", New: new(false)}},
		Notices: []engine.Notice{}, Edits: []engine.Edit{}, Scripts: []engine.ScriptRun{},
	}
	for i := range steps {
		r.Steps = append(r.Steps, plan.Step{Action: plan.ActionCreate, Module: "kit", Target: "claude-code", Kind: "skill",
			Path: ".claude/skills/s" + strings.Repeat("x", i) + "/SKILL.md"})
	}
	return r
}

func rendered(s style, rep report) string {
	var b reportBuf
	printReport(&b, s, rep)
	return ansi.Strip(b.Folded())
}

var _ = Describe("printReport", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("prints its sections in order, each under a header when it has content", func() {
		out := rendered(style{}, report{Result: run(2), Verb: "installed"})
		order := []string{"Runtime hooks — new:", "Warnings — new:", "Unchanged since last run:", "Files:",
			"✓ installed caveman (claude-code)", "→ " + foldFooter}
		at := 0
		for _, h := range order {
			i := strings.Index(out[at:], h)
			Expect(i).To(BeNumerically(">=", 0), "%q after offset %d in\n%s", h, at, out)
			at += i
		}
		Expect(out).NotTo(ContainSubstring("Skipped — new:"), "no section without content")
		Expect(out).To(ContainSubstring("caveman: 1 runtime hook for claude-code in .claude/settings.json (granted)\n    "))
	})

	It("groups a warning shared by modules into one summary naming them, and pre-approved tools per module", func() {
		out := rendered(style{}, report{Result: run(0), Verb: "installed"})
		Expect(out).To(ContainSubstring("⚠ AGENTS.md in a module is not installed (2 modules)\n    caveman\n    kit\n"))
		Expect(out).To(ContainSubstring("⚠ caveman: pre-approves tools Claude Code then runs without asking (2 claude-code skills)\n    lint: Bash\n    fmt: Read, Write\n"))
	})

	It("folds what was seen before into one line, counting it and pointing where to read it", func() {
		out := rendered(style{}, report{Result: run(0), Verb: "installed"})
		Expect(out).To(ContainSubstring("▸ 1 warning, 1 runtime hook seen before\n"))
		Expect(out).NotTo(ContainSubstring("./old.sh"), "the seen hook is in the fold")
		var b reportBuf
		printReport(&b, style{}, report{Result: run(0), Verb: "installed"})
		Expect(joinLines(renderLines(b.blocks, []bool{true}))).To(And(
			ContainSubstring("caveman: 1 runtime hook — lola info caveman"),
			ContainSubstring("kit: 1 warning — lola check kit"),
			ContainSubstring("./old.sh")))
	})

	It("folds each module's steps, and caps them at stepsShown with the command that lists them all", func() {
		out := rendered(style{}, report{Result: run(stepsShown + 2), Verb: "synced", More: "lola sync -n --no-pager"})
		Expect(out).To(ContainSubstring(fmt.Sprintf("▸ kit: %d changes (claude-code %d)", stepsShown+2, stepsShown+2)))
		var b reportBuf
		printReport(&b, style{}, report{Result: run(stepsShown + 2), Verb: "synced", More: "lola sync -n --no-pager"})
		Expect(joinLines(renderLines(b.blocks, []bool{false, true}))).To(ContainSubstring("    … and 2 more for kit (lola sync -n --no-pager lists them all)\n"))
	})

	It("puts lola update's pin move in the outcome line", func() {
		r := run(1)
		out := rendered(style{}, report{Result: r, Verb: "updated", Sync: true, Update: true, Changed: []string{"kit"},
			Pins: []engine.Pin{{Module: "kit", PreviousDigest: "a1b2c3", Digest: "d4e5f6", Reason: engine.PinUpdate}}})
		Expect(out).To(ContainSubstring("✓ updated kit a1b2c3 → d4e5f6 (claude-code)\n✓ 1 unchanged\n"))
	})

	It("prints every fact under -v: each finding, hook command, step path and notice, and no footer", func() {
		r := run(3)
		r.Notices = []engine.Notice{{Level: engine.LevelInfo, Text: "detail note", Class: target.ClassDetail, Module: "kit", Check: "lola check kit"}}
		var b bytes.Buffer
		printReport(&b, style{verbose: true}, report{Result: r, Verb: "installed"})
		out := ansi.Strip(b.String())
		for _, fact := range []string{"lint: Bash", "fmt: Read, Write", "./new.sh", "./old.sh", "sets the permission mode it runs in: plan",
			".claude/skills/s/SKILL.md", ".claude/skills/sxx/SKILL.md", "detail note"} {
			Expect(out).To(ContainSubstring(fact))
		}
		Expect(out).NotTo(ContainSubstring(foldFooter))
	})

	It("prints a dry run's plan header, no outcome, the footer, then to apply:", func() {
		r := run(1)
		r.DryRun = true
		out := rendered(style{}, report{Result: r, Verb: "installed", ToApply: "lola install kit"})
		Expect(out).To(HavePrefix(planHeader(style{}) + "\n"))
		Expect(out).NotTo(ContainSubstring("✓ installed"))
		Expect(out).To(HaveSuffix("→ " + foldFooter + "\n→ to apply: lola install kit\n"))
	})

	It("prints the footer once after two reports in one output, as an install that also removes what was unticked does", func() {
		var b reportBuf
		printReport(&b, style{}, report{Result: run(1), Verb: "installed"})
		printReport(&b, style{}, report{Result: run(1), Verb: "uninstalled"})
		out := ansi.Strip(b.Folded())
		Expect(strings.Count(out, foldFooter)).To(Equal(1), out)
		Expect(out).To(HaveSuffix("✓ uninstalled kit (claude-code)\n→ "+foldFooter+"\n"), "after the last outcome")
	})

	It("prints a declined-hooks warning in place only under the declined hooks it is about, else in full", func() {
		r := run(0)
		r.Findings = append(r.Findings, target.Finding{Severity: target.SeverityWarn, Module: "kit", Target: "claude-code", Kind: "runtime-hook",
			Code: "claude-code/runtime-hooks-declined", Text: "runtime hooks skipped: they need your consent", Hint: "consent: lola install kit --trust=runtime-hooks", New: new(true)})
		out := rendered(style{}, report{Result: r, Verb: "installed"})
		Expect(out).To(ContainSubstring("⚠ kit: claude-code: runtime hooks skipped: they need your consent\n    hint: consent: lola install kit --trust=runtime-hooks\n"),
			"kit's run lists no declined hook to carry the hint")

		r.Modules[1].RuntimeHooks = []engine.RuntimeHook{{Target: "claude-code", Event: "Stop", Type: "command", Command: "./x.sh", Status: engine.RuntimeHooksDeclined,
			Settings: ".claude/settings.local.json", New: new(true)}}
		out = rendered(style{}, report{Result: r, Verb: "installed"})
		Expect(out).NotTo(ContainSubstring("runtime hooks skipped"))
		Expect(out).To(ContainSubstring("runs: ./x.sh\n    hint: consent: lola install kit --trust=runtime-hooks\n"))
	})

	It("folds only what the review showed, the same text and hint, printing anything else new in full", func() {
		shown := run(0)
		shown.Findings = shown.Findings[:1] // the review showed lint's tool list and caveman's granted hook
		r := run(0)
		r.Findings[1].Hint = "a hint the review did not show"
		out := rendered(style{}, report{Result: r, Verb: "installed", Reviewed: shown})
		Expect(out).To(HavePrefix("▸ 1 warning, 1 runtime hook shown in the review above\n"))
		Expect(out).To(ContainSubstring("Warnings — new:\n"))
		Expect(out).To(ContainSubstring("skill fmt: pre-approves tools Claude Code then runs without asking: Read, Write\n    hint: a hint the review did not show\n"), "the review did not show its hint")
		Expect(out).To(ContainSubstring("AGENTS.md in a module is not installed (2 modules)"))
		Expect(out).NotTo(ContainSubstring("Runtime hooks — new:"))
		Expect(out).NotTo(ContainSubstring("lint: Bash"), "folded")
	})

	It("keeps --plain's rows complete and in order, a run's ending with new or seen", func() {
		var b bytes.Buffer
		printReport(&b, style{plain: true}, report{Result: run(1), Verb: "installed"})
		Expect(b.String()).To(ContainSubstring("runtime-hook\tcaveman\tclaude-code\tStop\tcommand\tconsented\t-\tseen\tStop: command; runs: ./old.sh\n"))
		Expect(b.String()).To(ContainSubstring("\tclaude-code/permission-mode\tsets the permission mode it runs in: plan\tseen\n"))
		Expect(b.String()).NotTo(ContainSubstring("▸"))
	})
})

var _ = Describe("printSync", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("closes a run in sync with the in-sync line naming the modules, and no files section", func() {
		r := &engine.SyncResult{Result: run(0), InSync: true, Trust: engine.TrustReport{Status: engine.TrustStatusNotApplicable}}
		var b reportBuf
		printSync(&b, style{}, "project.yml", r, []string{"kit", "kit"}, "", false)
		out := ansi.Strip(b.Folded())
		Expect(out).To(ContainSubstring("✓ kit already in sync\n"))
		Expect(out).NotTo(ContainSubstring("Files:"))
		Expect(out).To(ContainSubstring("Unchanged since last run:"))
	})

	It("names lola update's pin move in the outcome and counts the rest unchanged", func() {
		res := run(1)
		r := &engine.SyncResult{Result: res, Installed: true, Changed: []string{"kit"}, Trust: engine.TrustReport{Status: engine.TrustStatusNotApplicable},
			Pins: []engine.Pin{{Module: "kit", PreviousDigest: "a1", Digest: "d4", Reason: engine.PinUpdate}}}
		var b reportBuf
		printSync(&b, style{}, "project.yml", r, nil, "", false)
		Expect(ansi.Strip(b.Folded())).To(ContainSubstring("✓ updated kit a1 → d4 (claude-code)\n✓ 1 unchanged\n"))
	})
})

var _ = Describe("printMove", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("lists the move's edits once, in each scope's files section", func() {
		edit := func(scope, path string) engine.Edit {
			return engine.Edit{Module: "kit", Action: engine.EditAdded, Profile: "work", Kind: "profile", Path: path, Scope: scope}
		}
		sync := func(scope string) *engine.SyncResult {
			return &engine.SyncResult{Result: run(1), Scope: scope, Root: "/p", Changed: []string{"kit"},
				Trust: engine.TrustReport{Status: engine.TrustStatusNotApplicable}}
		}
		res := &engine.MoveResult{Edits: []engine.Edit{edit("project", "/p/.lola/profiles/work.yml"), edit("user", "/u/profiles/work.yml")},
			Syncs: []*engine.SyncResult{sync("project"), sync("user")}}
		var b reportBuf
		printMove(&b, style{}, res, "")
		out := ansi.Strip(b.Folded())
		Expect(strings.Count(out, "/p/.lola/profiles/work.yml")).To(Equal(1))
		Expect(strings.Count(out, "/u/profiles/work.yml")).To(Equal(1))
		project, user, _ := strings.Cut(out, "user scope:")
		Expect(project).To(MatchRegexp(`(?s)Files:\n[^\n]*/p/\.lola/profiles/work\.yml`))
		Expect(user).To(MatchRegexp(`(?s)Files:\n[^\n]*/u/profiles/work\.yml`))
		Expect(res.Syncs[0].Edits).To(BeEmpty())
	})
})
