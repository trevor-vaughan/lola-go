package cli_test

// One spec per row of the install transition table in docs/architecture/tui-flows.md, named
// "install: <from> --<input>--> <to>", the input followed by the row's guard in parentheses where
// rows share a from and an input. Rows an existing spec pins already:
//
// also pinned (start --held version--> next step): flow_test.go "asks no version, and offers no Change versions, for an assistant the scope's modules are written for"
// also pinned (start --run--> scope; scope --answer--> next step; modules --answer--> next step; assistants --answer--> next step; components --answer--> next step): flow_test.go "walks from nothing to an installed set: scope, modules, assistants, components, summary, apply"
// also pinned (start --run (every answer given)--> apply; start --run (only assistant)--> review; start --run (-y or --force)--> next step; review --q--> cancelled (review Quit text)): flow_test.go "asks nothing of a run the flags fully specify, and reviews an assistant lola chose"
// also pinned (start --run (only assistant or target set up)--> review): flow_test.go "on a terminal, offers agents after the assistants only where it is set up, and never counts it as one"
// also pinned (modules --esc--> scope; assistants --esc--> modules; review --esc (a component page)--> components; scope --esc (first screen)--> scope): flow_test.go "goes back a screen on Esc: the step before asks again with its answer preselected, then the flow carries on"
// also pinned (assistants --esc (first screen)--> assistants; components --esc (the first page)--> assistants; no components --esc--> components): flow_test.go "goes back from the first component page to the assistants, and from a no to no components to its page"
// also pinned (scope --answer (Other ticked)--> directory; directory --esc--> scope): flow_test.go "goes back from Other directory… to the scope picker, Other ticked"
// also pinned (review --Change modules--> modules; review --Change components--> components): flow_test.go "re-runs a step from the review menu with its answers preselected, keeping later answers that still apply"
// also pinned (review --Change scope--> scope): flow_test.go "installs at every scope after Change scope adds one, with a page for the added scope only"
// also pinned (scope --answer (Other not ticked)--> next step (picks of destinations dropped)): flow_test.go "keeps the picks of the destinations Change scope keeps, and drops and counts the rest"
// also pinned (modules --answer--> next step (an installed module is not preselected)): flow_test.go "does not preselect an installed module at first, and keeps it when the user picked it"
// also pinned (modules --answer--> next step (picks of modules removed are dropped and counted)): flow_test.go "drops the component picks of a module removed in the review, and says so"
// also pinned (modules --ctrl+c--> cancelled; directory --ctrl+c--> cancelled; review --quit--> cancelled): flow_test.go "cancel writes nothing and exits 0"
// also pinned (review --quit (n or ctrl+c)--> cancelled): huh_internal_test.go "asks the review with y, c and n and no default, c opening the steps to change"
// also pinned (directory --answer (a usable directory)--> next step; directory --answer (not usable)--> directory): flow_test.go "asks for the Other directory again, with the reason, until it is a directory that is not the project above"
// also pinned (directory --answer (missing)--> create other; create other --yes--> next step): flow_test.go "creates an Other directory that does not exist yet on your yes, and installs there"
// also pinned (create other --yes--> next step (removed again on a cancel)): flow_test.go "removes an Other directory it created when you quit at the review"
// also pinned (start --run (-C missing, on a terminal)--> -C confirm; -C confirm --yes--> next step): flow_test.go "on a terminal, asks before creating it and installs on your yes"
// also pinned (-C confirm --no--> cancelled): flow_test.go "on a terminal, your no creates nothing and exits 0"
// also pinned (start --run (-C missing and -n)--> next step): flow_test.go "plans a dry run as for the empty directory it would create, and creates nothing"
// also pinned (start --run (-C missing and -y or --force)--> next step): flow_test.go "creates it under -y or --force, says so, and installs there"
// also pinned (scope --answer (Project is the home directory)--> scope): flow_test.go "asks again when Other directory or Project is the home directory, User ticked or not"
// also pinned (components --empty submit--> no components; no components --yes--> next step; no components --no (the only module)--> cancelled): flow_test.go "asks before installing a module with no components; No drops it from the run"
// also pinned (answers --all answered (nothing would change)--> up to date): flow_test.go "says already up to date when nothing would change"
// also pinned (answers --dry-run refusal--> refused (no picker shown)): flow_test.go "keeps the run's output flags in the hints of a review's dry-run refusal"
// also pinned (start --run (--json or no terminal)--> apply): flow_test.go "never asks under --json, and its equivalent commands are a dry run's run that applies it"
// also pinned (start --component flag--> scope (only the component picker gives a Change entry)): flow_test.go "offers Apply, Change components and Quit when only the component picker ran"
// also pinned (review --Change modules--> modules (a Change entry goes once its step is not shown)): flow_test.go "drops a Change entry once its step is no longer shown"
// also pinned (modules --Add a source…--> add source; add source --answer (added)--> modules; add source --answer (the add fails)--> modules): flow_test.go "adds a source from the empty cache and selects it, and the summary repeats the cache add"
// also pinned (add source --empty submit--> modules): flow_test.go "returns to the module picker with the picks when the source to add is left empty"
// also pinned (add source --esc--> modules): keys_internal_test.go "goes back from the source to add to the module picker"
// also pinned (modules --answer (two picks are one module name)--> modules): marketflow_test.go "asks again when two picks are one module name, and installs the answer"
// also pinned (review --Change modules--> modules, twice: dropped picks counted once): flow_test.go "counts a dropped module's picks once, not once per scope, and not again in the next summary"
// also pinned (start --target version flag (target_versions declares it)--> next step): flow_test.go "never asks about a version the scope declares"
// also pinned (version --answer (another question is open)--> version): flow_test.go "asks once per scope, naming each, when the install has more than one"
// also pinned (version --answer (the last question)--> next step): flow_test.go "asks in the project lola runs in, preselecting the installed era, and records the installed version"
// also pinned (start --run (-y or --force)--> next step (no version question)): flow_test.go "-y and --force ask nothing and take the installed version here"
// also pinned (version --esc (the first question, an assistant picker shown)--> assistants; review --Change versions--> version): flow_test.go "goes back from the version question to the assistants, and Change versions asks only it again"
// also pinned (version --esc (asked by Change versions)--> review): flow_test.go "goes back from Change versions to the review, the answers kept"
// also pinned (version --esc (a later question)--> version): flow_test.go "goes back from a later scope's version question to the one before, its answer preselected"
// also pinned (review --Change versions--> version (a declared version)): flow_test.go "offers Change versions for a version the scope declares, its era preselected, and records the new answer"
// also pinned (review --q--> cancelled (q asks first once two answers are held)): keys_internal_test.go "with two or more steps answered, q asks first: y quits; n, Enter and Esc go back to the screen as it was"
// also pinned (scope --esc (first screen)--> scope; assistants --esc (first screen)--> assistants): keys_internal_test.go "on the first screen, Esc says q quits in the key help, not as an error, and stays"
// also pinned (review --q--> cancelled): keys_internal_test.go "prints a quit as cancelled, nothing changed, and exits 0 as Ctrl+C does"

import (
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/cli"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("install paths", func() {
	var tmp, proj, selections, home string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		home = os.Getenv("HOME")
		proj = filepath.Join(tmp, "proj")
		review, deploy := filepath.Join(tmp, "src", "review-kit"), filepath.Join(tmp, "src", "deploy")
		writeTree(review, map[string]string{
			"lola.yml":                  "name: review-kit\ndescription: Code review helpers\n",
			"skills/review/SKILL.md":    "---\ndescription: Review code\n---\nReview.\n",
			"skills/slow-lint/SKILL.md": "---\ndescription: Lint slowly\n---\nLint.\n",
			"commands/ship.md":          "---\ndescription: Ship it\n---\nShip.\n",
			"rules/style.md":            "Be brief.\n",
		})
		writeTree(deploy, map[string]string{"lola.yml": "name: deploy\n", "commands/deploy.md": "Deploy.\n"})
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
		selections = filepath.Join(tmp, "config", "lola", "selections.yaml")
		Expect(lola("cache", "add", review).code).To(Equal(0))
		Expect(lola("cache", "add", deploy).code).To(Equal(0))
		GinkgoT().Chdir(proj)
	})
	nothingWritten := func() {
		GinkgoHelper()
		Expect(selections).NotTo(BeAnExistingFile())
		Expect(filepath.Join(proj, ".lola")).NotTo(BeADirectory())
		Expect(filepath.Join(proj, ".claude", "commands")).NotTo(BeADirectory())
		Expect(filepath.Join(home, ".claude")).NotTo(BeADirectory())
	}

	DescribeTable("a step a guard answers is not asked and has no Change entry",
		func(p *scripted, args []string, calls, options []string, check func(result, *scripted)) {
			fakeAssistant(tmp, "gemini", "0.63.0")
			r := lolaP(p, append([]string{"install", "-n"}, args...)...)
			Expect(r.code).To(Equal(0), r.err)
			Expect(p.calls).To(Equal(calls))
			Expect(p.reviewSteps[0].Options).To(Equal(options))
			if check != nil {
				check(r, p)
			}
			nothingWritten()
		},
		Entry("install: start --scope flag--> modules", &scripted{
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit")},
			assistants: []func(cli.AssistantStep) ([]string, error){func(cli.AssistantStep) ([]string, error) { return []string{"claude-code"}, nil }},
			components: []func(cli.ComponentPage) ([]string, error){keep()}, review: []string{cli.ReviewApply}},
			[]string{"--scope", "project"}, []string{"modules", "assistants", "components:project.yml › review-kit", "review"},
			[]string{cli.ReviewApply, cli.ReviewModules, cli.ReviewAssistants, cli.ReviewComponents, cli.ReviewQuit},
			func(r result, _ *scripted) {
				Expect(r.err).To(HavePrefix(projectContext(proj)), "the context line, before any step")
			}),
		Entry("install: start --module argument--> scope", &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project")},
			assistants: []func(cli.AssistantStep) ([]string, error){func(cli.AssistantStep) ([]string, error) { return []string{"claude-code"}, nil }},
			components: []func(cli.ComponentPage) ([]string, error){keep()}, review: []string{cli.ReviewApply}},
			[]string{"review-kit"}, []string{"scope", "assistants", "components:project.yml › review-kit", "review"},
			[]string{cli.ReviewApply, cli.ReviewScope, cli.ReviewAssistants, cli.ReviewComponents, cli.ReviewQuit}, nil),
		Entry("install: start --target flag--> scope", &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep()}, review: []string{cli.ReviewApply}},
			[]string{"-t", "claude-code"}, []string{"scope", "modules", "components:project.yml › review-kit", "review"},
			[]string{cli.ReviewApply, cli.ReviewScope, cli.ReviewModules, cli.ReviewComponents, cli.ReviewQuit},
			func(_ result, p *scripted) {
				Expect(p.reviewSteps[0].Summary).To(ContainSubstring("    → for claude-code (from -t)\n"))
			}),
		Entry("install: start --component flag--> scope", &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project")},
			assistants: []func(cli.AssistantStep) ([]string, error){func(cli.AssistantStep) ([]string, error) { return []string{"claude-code"}, nil }},
			review:     []string{cli.ReviewApply}},
			[]string{"review-kit", "--skip", "skill/slow-lint"}, []string{"scope", "assistants", "review"},
			[]string{cli.ReviewApply, cli.ReviewScope, cli.ReviewAssistants, cli.ReviewQuit}, nil),
	)

	DescribeTable("Ctrl+C at a step cancels the run: nothing is written and it exits 0",
		func(p *scripted, calls []string, args ...string) {
			fakeAssistant(tmp, "gemini", "0.63.0")
			r := lolaP(p, append([]string{"install"}, args...)...)
			Expect(r.code).To(Equal(0), r.err)
			for i := range calls { // the -C directory, which the tree is built before
				calls[i] = strings.ReplaceAll(calls[i], "<fresh>", filepath.Join(proj, "fresh"))
			}
			Expect(p.calls).To(Equal(calls))
			Expect(r.err).To(HaveSuffix("✗ cancelled — nothing was changed\n"))
			nothingWritten()
			Expect(filepath.Join(proj, "fresh")).NotTo(BeAnExistingFile(), "a -C directory the run created is removed")
		},
		Entry("install: -C confirm --ctrl+c--> cancelled", &scripted{confirmCancels: 1},
			[]string{"confirm:Create <fresh>? It does not exist yet"}, "deploy", "-C", "fresh", "-t", "claude-code"),
		Entry("install: scope --ctrl+c--> cancelled", &scripted{scope: []func(cli.ScopeStep) ([]string, error){aborted[cli.ScopeStep, []string]}},
			[]string{"scope"}),
		Entry("install: assistants --ctrl+c--> cancelled", &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("deploy")}, assistants: []func(cli.AssistantStep) ([]string, error){aborted[cli.AssistantStep, []string]}},
			[]string{"scope", "modules", "assistants"}),
		Entry("install: components --ctrl+c--> cancelled", &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit")}, assistants: []func(cli.AssistantStep) ([]string, error){func(cli.AssistantStep) ([]string, error) { return []string{"claude-code"}, nil }},
			components: []func(cli.ComponentPage) ([]string, error){aborted[cli.ComponentPage, []string]}},
			[]string{"scope", "modules", "assistants", "components:project.yml › review-kit"}),
		Entry("install: no components --ctrl+c--> cancelled", &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit")}, assistants: []func(cli.AssistantStep) ([]string, error){func(cli.AssistantStep) ([]string, error) { return []string{"claude-code"}, nil }},
			components: []func(cli.ComponentPage) ([]string, error){pick()}, emptyCancels: 1},
			[]string{"scope", "modules", "assistants", "components:project.yml › review-kit", "empty:review-kit"}),
		Entry("install: components --ctrl+c (lola created the -C directory)--> cancelled", &scripted{confirm: []bool{true},
			components: []func(cli.ComponentPage) ([]string, error){aborted[cli.ComponentPage, []string]}},
			[]string{"confirm:Create <fresh>? It does not exist yet", "components:project.yml › review-kit"}, "review-kit", "-C", "fresh", "-t", "claude-code"),
	)

	DescribeTable("No or Esc to creating the Other directory asks for it again, creating nothing",
		func(p *scripted) {
			missing, other := filepath.Join(tmp, "missing"), filepath.Join(tmp, "other")
			Expect(os.MkdirAll(other, 0o755)).To(Succeed())
			p.scope = []func(cli.ScopeStep) ([]string, error){scopes("other")}
			p.dirs = []func(cli.DirectoryStep) (string, error){dir(missing), dir(other)}
			p.modules = []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("deploy")}
			p.review = []string{cli.ReviewQuit}
			r := lolaP(p, "install", "-t", "claude-code")
			Expect(r.code).To(Equal(0), r.err)
			Expect(p.calls).To(Equal([]string{"scope", "directory", "confirm:Create " + missing + "? It does not exist yet", "directory", "modules", "review"}))
			Expect(p.dirSteps[1].Problem).To(Equal(missing + " does not exist"))
			Expect(p.dirSteps[1].Value).To(Equal(missing), "the answer is offered again to edit")
			Expect(missing).NotTo(BeAnExistingFile())
		},
		Entry("install: create other --no--> directory", &scripted{confirm: []bool{false}}),
		Entry("install: create other --esc--> directory", &scripted{confirmBacks: 1}),
	)

	It("install: no components --no (other modules left)--> next step", func() {
		p := &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit", "deploy")},
			components: []func(cli.ComponentPage) ([]string, error){pick()},
			empty:      []bool{false},
			review:     []string{cli.ReviewApply},
		}
		r := lolaP(p, "install")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"scope", "modules", "components:project.yml › review-kit", "empty:review-kit", "review"}))
		Expect(p.reviewSteps[0].Summary).NotTo(ContainSubstring("review-kit"))
		Expect(p.reviewSteps[0].Summary).To(ContainSubstring("deploy: + command/deploy"))
		Expect(filepath.Join(proj, ".claude/commands/deploy.md")).To(BeARegularFile())
		Expect(filepath.Join(proj, ".claude/skills")).NotTo(BeADirectory())
	})

	It("install: review --apply--> done", func() {
		p := &scripted{
			scope:   []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("deploy")},
			review:  []string{cli.ReviewApply},
		}
		r := lolaP(p, "install")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"scope", "modules", "review"}))
		Expect(r.out).To(ContainSubstring("✓ installed deploy (claude-code)"))
		Expect(filepath.Join(proj, ".claude/commands/deploy.md")).To(BeARegularFile())
		led, err := ledger.Load(filepath.Join(proj, ".lola", "lock.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Modules).To(HaveKey("deploy"))
		m, err := manifest.Load(filepath.Join(proj, ".lola", "project.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Modules).To(HaveLen(1))
		Expect(m.Modules[0].Name).To(Equal("deploy"))
	})

	It("install: answers --dry-run refusal--> refused", func() {
		kit := filepath.Join(tmp, "src", "kit")
		writeTree(kit, map[string]string{"lola.yml": "name: kit\n", "commands/ship.md.liquid": "Ship\n{{ targt }}\n"})
		p := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes("project")}, review: []string{cli.ReviewApply}}
		r := lolaP(p, "install", kit)
		Expect(r.code).To(Equal(1), r.err)
		Expect(r.err).To(Equal(projectContext(proj) + "error: kit: claude-code command ship: commands/ship.md.liquid:2: undefined variable in {{ targt }}\n" +
			"  hint: lola check --project " + kit + "\n"))
		Expect(p.calls).To(Equal([]string{"scope"}), "the flow exits; it does not return to a step")
		Expect(filepath.Join(proj, ".lola")).NotTo(BeADirectory())
		Expect(filepath.Join(proj, ".claude", "commands")).NotTo(BeADirectory())
	})

	It("install: components --esc (a later page)--> components", func() {
		lint := filepath.Join(tmp, "src", "lint-kit")
		writeTree(lint, map[string]string{"lola.yml": "name: lint-kit\n", "skills/a/SKILL.md": "A.\n", "skills/b/SKILL.md": "B.\n"})
		Expect(lola("cache", "add", lint).code).To(Equal(0))
		p := &scripted{
			scope:   []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit", "lint-kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep("command/ship"), goneBack[cli.ComponentPage, []string],
				keep(), keep("skill/b")},
			review: []string{cli.ReviewApply},
		}
		r := lolaP(p, "install", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"scope", "modules", "components:project.yml › review-kit", "components:project.yml › lint-kit",
			"components:project.yml › review-kit", "components:project.yml › lint-kit", "review"}))
		Expect(ticked(p.pages[2])).To(Equal([]string{"skill/review", "skill/slow-lint", "rule/style"}), "asked again as answered")
		Expect(p.pages[1].Nav).To(Equal(cli.Nav{Back: true, Answered: 3}))
		Expect(equivalents(r.err)).To(Equal([]string{"lola install review-kit lint-kit --scope project -t claude-code " +
			"--skip review-kit:command/ship --skip lint-kit:skill/b"}))
		nothingWritten()
	})

	It("install: review --esc (no component page)--> modules", func() {
		p := &scripted{
			scope:   []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("deploy"), mods("deploy")},
			review:  []string{goBack, cli.ReviewApply},
		}
		r := lolaP(p, "install", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"scope", "modules", "review", "modules", "review"}))
		Expect(p.reviewSteps[0].Nav).To(Equal(cli.Nav{Back: true, Answered: 2}))
		Expect(p.moduleSteps[1].Preselect).To(Equal([]string{"deploy"}))
		nothingWritten()
	})

	It("install: review --Change components twice--> components", func() {
		p := &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep("command/ship"), keep("rule/style"), keep()},
			review:     []string{cli.ReviewComponents, cli.ReviewComponents, cli.ReviewApply},
		}
		r := lolaP(p, "install", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"scope", "modules", "components:project.yml › review-kit", "review",
			"components:project.yml › review-kit", "review", "components:project.yml › review-kit", "review"}))
		Expect(ticked(p.pages[1])).To(Equal([]string{"skill/review", "skill/slow-lint", "rule/style"}))
		Expect(ticked(p.pages[2])).To(Equal([]string{"skill/review", "skill/slow-lint"}))
		Expect(p.reviewSteps[2].Summary).To(ContainSubstring("review-kit: + skill/review, + skill/slow-lint\n"))
		Expect(equivalents(r.err)).To(Equal([]string{"lola install review-kit --scope project -t claude-code " +
			"--skip review-kit:command/ship --skip review-kit:rule/style"}))
	})

	It("install: review --Change modules (re-adding a removed module)--> components", func() {
		By("after Change components, the page it asked keeps that answer as the preselection")
		p := &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit", "deploy"), mods("deploy"), mods("deploy", "review-kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep("command/ship"), keep(), keep("rule/style")},
			review:     []string{cli.ReviewComponents, cli.ReviewModules, cli.ReviewModules, cli.ReviewApply},
		}
		r := lolaP(p, "install", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"scope", "modules", "components:project.yml › review-kit", "review",
			"components:project.yml › review-kit", "review", "modules", "review", "modules", "components:project.yml › review-kit", "review"}))
		Expect(ticked(p.pages[2])).To(Equal([]string{"skill/review", "skill/slow-lint", "rule/style"}), "the answer of Change components")
		Expect(p.reviewSteps[3].Summary).To(ContainSubstring("review-kit: + skill/review, + skill/slow-lint\n"), "the review lists the page's last answer")
		Expect(equivalents(r.err)).To(Equal([]string{"lola install deploy review-kit --scope project -t claude-code " +
			"--skip review-kit:command/ship --skip review-kit:rule/style"}))

		By("with no Change components, the page starts from the module's default")
		p = &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit"), mods("deploy"), mods("deploy", "review-kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep("command/ship"), keep()},
			review:     []string{cli.ReviewModules, cli.ReviewModules, cli.ReviewApply},
		}
		r = lolaP(p, "install", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(ticked(p.pages[1])).To(Equal([]string{"skill/review", "skill/slow-lint", "command/ship", "rule/style"}))
		Expect(equivalents(r.err)).To(Equal([]string{"lola install deploy review-kit --scope project -t claude-code"}))
	})

	// reviewedAgain checks that the run came back to the review with what it showed the first time.
	reviewedAgain := func(p *scripted) {
		GinkgoHelper()
		Expect(p.reviewSteps).To(HaveLen(2))
		Expect(p.reviewSteps[1].Summary).To(Equal(p.reviewSteps[0].Summary))
		Expect(p.reviewSteps[1].Options).To(Equal(p.reviewSteps[0].Options))
		Expect(p.reviewSteps[1].Step).To(Equal(p.reviewSteps[0].Step), "numbered, so titled, as it was")
	}

	It("install: change scope --esc--> review, scope unchanged", func() {
		p := &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project"), goneBack[cli.ScopeStep, []string]},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep("command/ship")},
			review:     []string{cli.ReviewScope, cli.ReviewApply},
		}
		r := lolaP(p, "install", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"scope", "modules", "components:project.yml › review-kit", "review", "scope", "review"}))
		Expect(p.scopeSteps[0].Nav.Back).To(BeFalse(), "the run's first screen")
		Expect(p.scopeSteps[1].Nav.Back).To(BeTrue(), "reopened from the review, Esc goes back to it")
		reviewedAgain(p)
		Expect(equivalents(r.err)).To(Equal([]string{"lola install review-kit --scope project -t claude-code --skip review-kit:command/ship"}))
	})

	It("install: change modules --esc--> review, modules unchanged", func() {
		p := &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit", "deploy"), goneBack[cli.ModuleStep, cli.ModuleAnswer]},
			components: []func(cli.ComponentPage) ([]string, error){keep("command/ship")},
			review:     []string{cli.ReviewModules, cli.ReviewApply},
		}
		r := lolaP(p, "install", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"scope", "modules", "components:project.yml › review-kit", "review", "modules", "review"}),
			"neither the scope nor a component page is asked again")
		reviewedAgain(p)
		Expect(equivalents(r.err)).To(Equal([]string{"lola install review-kit deploy --scope project -t claude-code --skip review-kit:command/ship"}))
	})

	It("install: change assistants --esc--> review, assistants unchanged", func() {
		fakeAssistant(tmp, "gemini", "0.63.0")
		p := &scripted{
			scope:   []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("deploy")},
			assistants: []func(cli.AssistantStep) ([]string, error){func(cli.AssistantStep) ([]string, error) { return []string{"gemini-cli"}, nil },
				goneBack[cli.AssistantStep, []string]},
			review: []string{cli.ReviewAssistants, cli.ReviewApply},
		}
		r := lolaP(p, "install", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"scope", "modules", "assistants", "review", "assistants", "review"}))
		Expect(p.assistantSteps[1].Nav.Back).To(BeTrue())
		reviewedAgain(p)
		Expect(equivalents(r.err)).To(Equal([]string{"lola install deploy --scope project -t gemini-cli"}))
	})

	It("install: change components --esc--> review, components unchanged", func() {
		lint := filepath.Join(tmp, "src", "lint-kit")
		writeTree(lint, map[string]string{"lola.yml": "name: lint-kit\n", "skills/a/SKILL.md": "A.\n", "skills/b/SKILL.md": "B.\n"})
		Expect(lola("cache", "add", lint).code).To(Equal(0))
		p := &scripted{
			scope:   []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit", "lint-kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep("command/ship"), keep("skill/b"),
				pick("skill/review"), goneBack[cli.ComponentPage, []string], goneBack[cli.ComponentPage, []string]},
			review: []string{cli.ReviewComponents, cli.ReviewApply},
		}
		r := lolaP(p, "install", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"scope", "modules", "components:project.yml › review-kit", "components:project.yml › lint-kit", "review",
			"components:project.yml › review-kit", "components:project.yml › lint-kit", "components:project.yml › review-kit", "review"}),
			"Esc on the later page goes back to the page before, Esc on the first to the review")
		Expect(ticked(p.pages[4])).To(Equal([]string{"skill/review"}), "the page before, as answered in this pass")
		Expect(p.pages[2].Nav.Back).To(BeTrue())
		reviewedAgain(p)
		Expect(equivalents(r.err)).To(Equal([]string{"lola install review-kit lint-kit --scope project -t claude-code " +
			"--skip review-kit:command/ship --skip lint-kit:skill/b"}))
	})
})

var _ = Describe("install paths through the version question", func() {
	var tmp, proj string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj = filepath.Join(tmp, "proj")
		writeTree(filepath.Join(tmp, "m"), map[string]string{"lola.yml": "name: m\n", "commands/c.md": "Go.\n"})
		writeTree(filepath.Join(tmp, "k"), map[string]string{"lola.yml": "name: k\n", "commands/a.md": "A.\n", "commands/b.md": "B.\n"})
		Expect(os.MkdirAll(filepath.Join(proj, ".opencode"), 0o755)).To(Succeed())
		fakeAssistant(tmp, "opencode", "opencode v2.2.5")
		Expect(lola("cache", "add", filepath.Join(tmp, "m")).code).To(Equal(0))
		Expect(lola("cache", "add", filepath.Join(tmp, "k")).code).To(Equal(0))
		GinkgoT().Chdir(proj)
	})
	declared := func() map[string]string {
		GinkgoHelper()
		m, err := manifest.Load(filepath.Join(proj, ".lola", "project.yml"))
		Expect(err).NotTo(HaveOccurred())
		return m.TargetVersions
	}
	eraOf := func(module string) string {
		GinkgoHelper()
		led, err := ledger.Load(filepath.Join(proj, ".lola", "lock.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Modules).To(HaveKey(module))
		return led.Modules[module].Eras["opencode"]
	}
	withClaude := func() {
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
	}
	opencode := func(cli.AssistantStep) ([]string, error) { return []string{"opencode"}, nil }

	It("install: version --ctrl+c--> cancelled", func() {
		p := &scripted{versions: []func(cli.VersionStep) (int, error){aborted[cli.VersionStep, int]}}
		r := lolaP(p, "install", "m", "--project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(HaveSuffix("✗ cancelled — nothing was changed\n"))
		Expect(p.calls).To(Equal([]string{"version:opencode"}))
		Expect(filepath.Join(proj, ".lola")).NotTo(BeADirectory())
	})

	It("install: version --esc (the first question, no assistant picker)--> modules", func() {
		p := &scripted{
			modules:  []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("m"), mods("m")},
			versions: []func(cli.VersionStep) (int, error){goneBack[cli.VersionStep, int], era(1)},
			review:   []string{cli.ReviewApply},
		}
		r := lolaP(p, "install", "--project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"modules", "version:opencode", "modules", "version:opencode", "review"}))
		Expect(p.versionSteps[0].Nav.Back).To(BeTrue())
		Expect(p.moduleSteps[1].Preselect).To(Equal([]string{"m"}))
		Expect(declared()).To(Equal(map[string]string{"opencode": "1.0"}))
	})

	It("install: components --esc (the first page, after a version question)--> version", func() {
		p := &scripted{
			versions: []func(cli.VersionStep) (int, error){era(1), func(st cli.VersionStep) (int, error) {
				Expect(st.Preselect).To(Equal(1), "the last answer")
				Expect(st.Again).To(BeFalse(), "a step of the flow, not Change versions")
				return 1, nil
			}},
			components: []func(cli.ComponentPage) ([]string, error){goneBack[cli.ComponentPage, []string], keep("command/b")},
			review:     []string{cli.ReviewApply},
		}
		r := lolaP(p, "install", "k", "--project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"version:opencode", "components:project.yml › k", "version:opencode", "components:project.yml › k", "review"}))
		Expect(p.pages[0].Nav).To(Equal(cli.Nav{Back: true, Answered: 1}))
		Expect(eraOf("k")).To(Equal("1.0"))
		Expect(filepath.Join(proj, ".opencode/commands/b.md")).NotTo(BeAnExistingFile())
	})

	It("install: review --Change modules--> modules", func() {
		withClaude()
		p := &scripted{
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("m"), mods("m", "k")},
			assistants: []func(cli.AssistantStep) ([]string, error){opencode},
			versions:   []func(cli.VersionStep) (int, error){era(1)},
			components: []func(cli.ComponentPage) ([]string, error){keep()},
			review:     []string{cli.ReviewModules, cli.ReviewApply},
		}
		r := lolaP(p, "install", "--project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"modules", "assistants", "version:opencode", "review", "modules", "components:project.yml › k", "review"}))
		Expect(p.reviewSteps[1].Summary).To(ContainSubstring("→ for opencode (picked)\n"))
		Expect(p.reviewSteps[1].Summary).To(ContainSubstring("opencode >=1.0 <2.0"))
		Expect(p.reviewSteps[1].Options).To(Equal([]string{cli.ReviewApply, cli.ReviewModules, cli.ReviewAssistants, cli.ReviewVersions,
			cli.ReviewComponents, cli.ReviewQuit}))
		Expect(declared()).To(Equal(map[string]string{"opencode": "1.0"}))
		Expect(eraOf("m")).To(Equal("1.0"))
		Expect(eraOf("k")).To(Equal("1.0"), "the module added is written for the kept version")
		Expect(filepath.Join(proj, ".claude/commands")).NotTo(BeADirectory(), "nor for an assistant not picked")
	})

	It("install: review --Change assistants--> assistants", func() {
		withClaude()
		p := &scripted{
			assistants: []func(cli.AssistantStep) ([]string, error){opencode, func(cli.AssistantStep) ([]string, error) {
				return []string{"claude-code", "opencode"}, nil
			}},
			versions: []func(cli.VersionStep) (int, error){era(1), func(st cli.VersionStep) (int, error) {
				Expect(st.Preselect).To(Equal(1), "the last answer")
				Expect(st.Again).To(BeFalse())
				return 1, nil
			}},
			components: []func(cli.ComponentPage) ([]string, error){keep("command/b")},
			review:     []string{cli.ReviewAssistants, cli.ReviewApply},
		}
		r := lolaP(p, "install", "k", "--project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"assistants", "version:opencode", "components:project.yml › k", "review", "assistants", "version:opencode", "review"}))
		Expect(p.assistantSteps[1].Preselect).To(Equal([]string{"opencode"}))
		Expect(filepath.Join(proj, ".claude/commands/a.md")).To(BeARegularFile())
		Expect(filepath.Join(proj, ".claude/commands/b.md")).NotTo(BeAnExistingFile(), "the page's answer applies to the assistant added")
		Expect(filepath.Join(proj, ".opencode/commands/b.md")).NotTo(BeAnExistingFile())
		Expect(declared()).To(Equal(map[string]string{"opencode": "1.0"}))
	})

	It("install: review --Change scope--> scope", func() {
		withClaude()
		p := &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project"), scopes("project")},
			assistants: []func(cli.AssistantStep) ([]string, error){opencode, opencode},
			versions: []func(cli.VersionStep) (int, error){era(1), func(st cli.VersionStep) (int, error) {
				Expect(st.Preselect).To(Equal(1), "the last answer")
				return 1, nil
			}},
			components: []func(cli.ComponentPage) ([]string, error){keep("command/b")},
			review:     []string{cli.ReviewScope, cli.ReviewApply},
		}
		r := lolaP(p, "install", "k")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"scope", "assistants", "version:opencode", "components:project.yml › k", "review",
			"scope", "assistants", "version:opencode", "review"}), "the kept scope keeps its page's answer")
		Expect(p.scopeSteps[1].Preselect).To(Equal([]string{"project"}))
		Expect(p.scopeSteps[1].Nav).To(Equal(cli.Nav{Back: true, Answered: 3}), "reopened from the review: Esc goes back to it")
		Expect(p.assistantSteps[1].Preselect).To(Equal([]string{"opencode"}))
		Expect(filepath.Join(proj, ".opencode/commands/a.md")).To(BeARegularFile())
		Expect(filepath.Join(proj, ".opencode/commands/b.md")).NotTo(BeAnExistingFile())
		Expect(declared()).To(Equal(map[string]string{"opencode": "1.0"}))
	})

	It("install: start --target version flag--> next step", func() {
		p := &scripted{components: []func(cli.ComponentPage) ([]string, error){keep()}, review: []string{cli.ReviewQuit}}
		r := lolaP(p, "install", "k", "--project", "-t", "opencode@1.0")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"components:project.yml › k", "review"}))
		Expect(p.reviewSteps[0].Options).To(Equal([]string{cli.ReviewApply, cli.ReviewVersions, cli.ReviewComponents, cli.ReviewQuit}))
		Expect(filepath.Join(proj, ".lola")).NotTo(BeADirectory())
	})
})
