package cli_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/cli"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// scripted is a Prompter that answers from queues and records each step it was asked, so
// specs check step order, steps not shown and what each step offered.
type scripted struct {
	calls      []string
	scope      []func(cli.ScopeStep) ([]string, error)
	modules    []func(cli.ModuleStep) (cli.ModuleAnswer, error)
	assistants []func(cli.AssistantStep) ([]string, error)
	components []func(cli.ComponentPage) ([]string, error)
	dirs       []func(cli.DirectoryStep) (string, error)
	empty      []bool
	review     []string

	scopeSteps     []cli.ScopeStep
	dirSteps       []cli.DirectoryStep
	moduleSteps    []cli.ModuleStep
	assistantSteps []cli.AssistantStep
	pages          []cli.ComponentPage
	reviewSteps    []cli.ReviewStep
}

func pop[T any](q *[]T, step string) T {
	if len(*q) == 0 {
		Fail("the flow asked an unscripted " + step + " step")
	}
	v := (*q)[0]
	*q = (*q)[1:]
	return v
}

func (s *scripted) Interactive() bool { return true }
func (s *scripted) Scope(st cli.ScopeStep) ([]string, error) {
	s.calls, s.scopeSteps = append(s.calls, "scope"), append(s.scopeSteps, st)
	return pop(&s.scope, "scope")(st)
}
func (s *scripted) OtherDirectory(st cli.DirectoryStep) (string, error) {
	s.calls, s.dirSteps = append(s.calls, "directory"), append(s.dirSteps, st)
	return pop(&s.dirs, "directory")(st)
}
func (s *scripted) Modules(st cli.ModuleStep) (cli.ModuleAnswer, error) {
	s.calls, s.moduleSteps = append(s.calls, "modules"), append(s.moduleSteps, st)
	return pop(&s.modules, "modules")(st)
}
func (s *scripted) Assistants(st cli.AssistantStep) ([]string, error) {
	s.calls, s.assistantSteps = append(s.calls, "assistants"), append(s.assistantSteps, st)
	return pop(&s.assistants, "assistants")(st)
}
func (s *scripted) Components(pg cli.ComponentPage) ([]string, error) {
	s.calls, s.pages = append(s.calls, "components:"+pg.Title), append(s.pages, pg)
	return pop(&s.components, "components")(pg)
}
func (s *scripted) ConfirmEmpty(module string) (bool, error) {
	s.calls = append(s.calls, "empty:"+module)
	return pop(&s.empty, "confirm-empty"), nil
}
func (s *scripted) Review(st cli.ReviewStep) (string, error) {
	s.calls, s.reviewSteps = append(s.calls, "review"), append(s.reviewSteps, st)
	r := pop(&s.review, "review")
	if r == cli.ReviewQuit {
		return "", cli.ErrAborted
	}
	return r, nil
}

// answers return fixed values.
func scopes(names ...string) func(cli.ScopeStep) ([]string, error) {
	return func(cli.ScopeStep) ([]string, error) { return names, nil }
}
func dir(path string) func(cli.DirectoryStep) (string, error) {
	return func(cli.DirectoryStep) (string, error) { return path, nil }
}
func mods(names ...string) func(cli.ModuleStep) (cli.ModuleAnswer, error) {
	return func(cli.ModuleStep) (cli.ModuleAnswer, error) { return cli.ModuleAnswer{Names: names}, nil }
}
func pick(ids ...string) func(cli.ComponentPage) ([]string, error) {
	return func(cli.ComponentPage) ([]string, error) { return ids, nil }
}

// keep answers a page with its preselection minus drop.
func keep(drop ...string) func(cli.ComponentPage) ([]string, error) {
	return func(pg cli.ComponentPage) ([]string, error) {
		var out []string
		for _, g := range pg.Groups {
			for _, o := range g.Options {
				if o.Selected && !slices.Contains(drop, o.ID) {
					out = append(out, o.ID)
				}
			}
		}
		return out, nil
	}
}

// lolaP runs lola with p as its Prompter.
func lolaP(p cli.Prompter, args ...string) result {
	root := cli.NewRootCmd(os.Getenv, cli.WithPrompter(p))
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetIn(strings.NewReader(""))
	root.SetArgs(args)
	code := cli.Execute(root)
	return result{out: out.String(), err: errb.String(), code: code}
}

// equivalents returns the equivalent commands the last summary printed.
func equivalents(stderr string) []string {
	i := strings.LastIndex(stderr, "Equivalent commands:\n")
	Expect(i).NotTo(BeNumerically("<", 0), stderr)
	after := stderr[i+len("Equivalent commands:\n"):]
	var out []string
	for _, l := range strings.Split(after, "\n") {
		if !strings.HasPrefix(l, "  ") {
			break
		}
		out = append(out, strings.TrimSpace(l))
	}
	return out
}

// shellWords splits a command line shellQuote wrote: words separated by spaces, '…' quoted.
func shellWords(line string) []string {
	var out []string
	var cur strings.Builder
	quoted, inWord := false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '\'':
			quoted, inWord = !quoted, true
		case c == ' ' && !quoted:
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
			}
			inWord = false
		case c == '\\' && !quoted && i+1 < len(line):
			i++
			cur.WriteByte(line[i])
			inWord = true
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out
}

var _ = Describe("the install flow (Plan 5c §3, §4)", func() {
	var tmp, proj, review, deploy, selections, home string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		home = os.Getenv("HOME")
		proj = filepath.Join(tmp, "proj")
		review, deploy = filepath.Join(tmp, "src", "review-kit"), filepath.Join(tmp, "src", "deploy")
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

	It("walks from nothing to an installed set: scope, modules, assistants, components, summary, apply", func() {
		fakeAssistant(tmp, "gemini", "0.63.0")
		p := &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit", "deploy")},
			assistants: []func(cli.AssistantStep) ([]string, error){func(cli.AssistantStep) ([]string, error) { return []string{"claude-code"}, nil }},
			components: []func(cli.ComponentPage) ([]string, error){keep("skill/slow-lint")},
			review:     []string{cli.ReviewApply},
		}
		r := lolaP(p, "install")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"scope", "modules", "assistants", "components:review-kit", "review"}),
			"deploy has one component and no exclusion, so it gets no page")

		Expect(p.scopeSteps[0]).To(Equal(cli.ScopeStep{ProjectRoot: proj, Home: home}), "nothing preselected")
		Expect(p.moduleSteps[0].Options).To(Equal([]cli.ModuleOption{
			{Name: "deploy", Label: deploy + " · deploy"},
			{Name: "review-kit", Label: review + " · review-kit — Code review helpers"}}))
		Expect(p.assistantSteps[0]).To(Equal(cli.AssistantStep{Options: []string{"claude-code", "gemini-cli"}, Preselect: []string{"claude-code"}, Scopes: []string{"project (" + proj + ")"}}))
		pg := p.pages[0]
		Expect(pg.Groups).To(HaveLen(3))
		Expect(pg.Groups[0].Title).To(Equal("Skills (2)"))
		Expect(pg.Groups[0].Options[0]).To(Equal(cli.ComponentOption{ID: "skill/review", Label: "review — Review code", Selected: true}))
		Expect(pg.Groups[2].Title).To(Equal("Rules (1)"))
		Expect(p.reviewSteps[0].Options).To(Equal([]string{"apply", "scope", "modules", "assistants", "components", "quit"}))

		Expect(r.err).To(ContainSubstring("Summary:\n  project (" + proj + "), picked:\n    claude-code:\n" +
			"      review-kit: + skill/review, + command/ship, + rule/style\n      deploy: + command/deploy\n"))
		Expect(r.err).To(ContainSubstring("  selections.yaml (" + selections + "):\n    review-kit in projects[" + proj + "]: skip [skill/slow-lint]\n"))
		Expect(equivalents(r.err)).To(Equal([]string{"lola install review-kit deploy --scope project -t claude-code --skip review-kit:skill/slow-lint"}))
		Expect(r.err).To(ContainSubstring("→ saved to " + selections))
		Expect(r.out).To(ContainSubstring("✓ installed review-kit (claude-code)"))
		Expect(filepath.Join(proj, ".claude/skills/slow-lint")).NotTo(BeADirectory())
		Expect(filepath.Join(proj, ".claude/commands/deploy.md")).To(BeARegularFile())
	})

	It("does not show steps the flags or the situation answer, and skips the menu when no picker ran", func() {
		p := &scripted{}
		r := lolaP(p, "install", "deploy", "--scope", "project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(BeEmpty(), "scope by flag, module by argument, one assistant, one component")
		Expect(r.err).NotTo(ContainSubstring("Summary:"))

		p = &scripted{components: []func(cli.ComponentPage) ([]string, error){keep()}}
		r = lolaP(p, "install", "review-kit", "--scope", "project", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"components:review-kit"}), "-y applies without the review menu")
		Expect(r.err).To(ContainSubstring("Summary:"))

		p = &scripted{}
		r = lolaP(p, "install", "review-kit", "--scope", "project", "--skip", "rule/style")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(BeEmpty(), "component flags replace the component step")
	})

	It("re-runs a step from the review menu with its answers preselected, keeping later answers that still apply", func() {
		p := &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit", "deploy"), mods("review-kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep("command/ship"), keep("rule/style")},
			review:     []string{cli.ReviewComponents, cli.ReviewModules, cli.ReviewApply},
		}
		r := lolaP(p, "install", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"scope", "modules", "components:review-kit", "review", "components:review-kit", "review", "modules", "review"}))
		var second []string
		for _, g := range p.pages[1].Groups {
			for _, o := range g.Options {
				if o.Selected {
					second = append(second, o.ID)
				}
			}
		}
		Expect(second).NotTo(ContainElement("command/ship"), "the re-run page starts from the last answer")
		Expect(p.moduleSteps[1].Preselect).To(Equal([]string{"review-kit", "deploy"}))
		Expect(equivalents(r.err)).To(Equal([]string{"lola install review-kit --scope project -t claude-code --skip review-kit:command/ship --skip review-kit:rule/style"}))
		Expect(selections).NotTo(BeAnExistingFile(), "a dry run writes nothing")
		Expect(filepath.Join(proj, ".lola")).NotTo(BeADirectory())
	})

	It("installs at every scope after Change scope adds one, with a page for the added scope only", func() {
		p := &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project"), scopes("project", "user")},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit", "deploy")},
			components: []func(cli.ComponentPage) ([]string, error){keep("command/ship"), keep()},
			review:     []string{cli.ReviewScope, cli.ReviewApply},
		}
		r := lolaP(p, "install")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"scope", "modules", "components:review-kit", "review", "scope", "components:review-kit — user", "review"}))
		Expect(p.scopeSteps[1].Preselect).To(Equal([]string{"project"}))
		Expect(filepath.Join(proj, ".claude/commands/deploy.md")).To(BeARegularFile())
		Expect(filepath.Join(home, ".claude/commands/deploy.md")).To(BeARegularFile())
		Expect(filepath.Join(proj, ".claude/commands/ship.md")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(home, ".claude/commands/ship.md")).To(BeARegularFile())
	})

	It("keeps the versions the pickers listed when Change scope asks again", func() {
		p := &scripted{
			scope: []func(cli.ScopeStep) ([]string, error){scopes("project"), scopes("project")},
			components: []func(cli.ComponentPage) ([]string, error){func(pg cli.ComponentPage) ([]string, error) {
				writeTree(review, map[string]string{"commands/ship.md": "Changed.\n"})
				return keep()(pg)
			}},
			review: []string{cli.ReviewScope, cli.ReviewApply},
		}
		r := lolaP(p, "install", review)
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"scope", "components:review-kit", "review", "scope", "review"}))
		b, err := os.ReadFile(filepath.Join(proj, ".claude/commands/ship.md"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(ContainSubstring("Ship."), "the source was not fetched again")
	})

	It("does not preselect an installed module at first, and keeps it when the user picked it", func() {
		Expect(lola("install", "deploy", "--scope", "project").code).To(Equal(0))
		p := &scripted{
			scope: []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){
				func(st cli.ModuleStep) (cli.ModuleAnswer, error) {
					Expect(st.Preselect).To(BeEmpty())
					return cli.ModuleAnswer{Names: []string{"deploy", "review-kit"}}, nil
				},
				func(st cli.ModuleStep) (cli.ModuleAnswer, error) {
					Expect(st.Preselect).To(Equal([]string{"deploy", "review-kit"}))
					return cli.ModuleAnswer{Names: st.Preselect}, nil
				},
			},
			components: []func(cli.ComponentPage) ([]string, error){keep()},
			review:     []string{cli.ReviewModules, cli.ReviewApply},
		}
		r := lolaP(p, "install", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(equivalents(r.err)).To(Equal([]string{"lola install deploy review-kit --scope project"}), "the manifest fixes the targets now")
	})

	It("drops the component picks of a module removed in the review, and says so", func() {
		p := &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit", "deploy"), mods("deploy")},
			components: []func(cli.ComponentPage) ([]string, error){keep("command/ship")},
			review:     []string{cli.ReviewModules, cli.ReviewApply},
		}
		r := lolaP(p, "install", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(ContainSubstring("  3 component picks dropped: module removed\n"), "one per picked component of review-kit")
		Expect(equivalents(r.err)[0]).To(Equal("lola install deploy --scope project -t claude-code"))
	})

	DescribeTable("cancel writes nothing and exits 0",
		func(p *scripted) {
			r := lolaP(p, "install")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.err).To(HaveSuffix("aborted; nothing changed.\n"))
			Expect(selections).NotTo(BeAnExistingFile())
			Expect(filepath.Join(proj, ".lola")).NotTo(BeADirectory())
			Expect(filepath.Join(proj, ".claude/commands")).NotTo(BeADirectory())
		},
		Entry("Ctrl+C at a picker", &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){func(cli.ModuleStep) (cli.ModuleAnswer, error) { return cli.ModuleAnswer{}, cli.ErrAborted }}}),
		Entry("Quit in the review menu", &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("deploy")}, review: []string{cli.ReviewQuit}}),
		Entry("Ctrl+C at the Other directory path", &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes("project", "other")},
			dirs: []func(cli.DirectoryStep) (string, error){func(cli.DirectoryStep) (string, error) { return "", cli.ErrAborted }}}),
	)

	It("asks again until at least one scope and one module are picked", func() {
		p := &scripted{
			scope:   []func(cli.ScopeStep) ([]string, error){scopes(), scopes("user")},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods(), mods("deploy")},
			review:  []string{cli.ReviewApply},
		}
		r := lolaP(p, "install", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.scopeSteps[1].Problem).To(Equal("pick at least one scope"))
		Expect(p.moduleSteps[1].Problem).To(Equal("pick at least one module"))
	})

	It("preselects nothing on a terminal, though default_scope is set and the project uses lola (Plan 5c §14)", func() {
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		Expect(os.MkdirAll(filepath.Dir(selections), 0o700)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(filepath.Dir(selections), "config.yaml"), []byte("version: 1\ndefault_scope: user\n"), 0o600)).To(Succeed())
		p := &scripted{scope: []func(cli.ScopeStep) ([]string, error){func(st cli.ScopeStep) ([]string, error) { return nil, cli.ErrAborted }}}
		r := lolaP(p, "install")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.scopeSteps[0]).To(Equal(cli.ScopeStep{ProjectRoot: proj, Home: home}))
	})

	It("asks for the Other directory again, with the reason, until it is a directory that is not the project above", func() {
		writeTree(proj, map[string]string{"notes.txt": "x"})
		other := filepath.Join(tmp, "other")
		Expect(os.MkdirAll(other, 0o755)).To(Succeed())
		p := &scripted{
			scope:   []func(cli.ScopeStep) ([]string, error){scopes("project", "other")},
			dirs:    []func(cli.DirectoryStep) (string, error){dir(""), dir("~/elsewhere"), dir("missing"), dir("notes.txt"), dir("."), dir(proj + "/"), dir("../other")},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("deploy")},
			review:  []string{cli.ReviewApply},
		}
		r := lolaP(p, "install", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		var problems []string
		for _, st := range p.dirSteps {
			problems = append(problems, st.Problem)
		}
		Expect(problems).To(Equal([]string{"", "enter the path of a directory",
			filepath.Join(proj, "~/elsewhere") + " does not exist (~ is not expanded)",
			filepath.Join(proj, "missing") + " does not exist",
			filepath.Join(proj, "notes.txt") + " is not a directory",
			"that is the project above", "that is the project above"}))
		Expect(p.dirSteps[3].Value).To(Equal("missing"), "the answer is offered again to edit")
		Expect(filepath.Join(proj, ".claude/commands/deploy.md")).To(BeARegularFile())
		Expect(filepath.Join(other, ".claude/commands/deploy.md")).To(BeARegularFile())
		Expect(r.err).To(ContainSubstring("  project (" + other + "), picked:\n"))
	})

	It("asks again when Other directory or Project is the home directory, User ticked or not", func() {
		for _, answer := range [][]string{{"other"}, {"user", "other"}} {
			p := &scripted{
				scope: []func(cli.ScopeStep) ([]string, error){scopes(answer...)},
				dirs:  []func(cli.DirectoryStep) (string, error){dir(home), func(cli.DirectoryStep) (string, error) { return "", cli.ErrAborted }},
			}
			r := lolaP(p, "install")
			Expect(r.code).To(Equal(0), r.err)
			Expect(p.dirSteps[1].Problem).To(Equal("that is your user scope (~); tick User instead"), fmt.Sprint(answer))
		}

		Expect(os.MkdirAll(home, 0o755)).To(Succeed())
		GinkgoT().Chdir(home)
		for _, answer := range [][]string{{"project"}, {"project", "user"}} {
			p := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes(answer...), func(cli.ScopeStep) ([]string, error) { return nil, cli.ErrAborted }}}
			r := lolaP(p, "install")
			Expect(r.code).To(Equal(0), r.err)
			Expect(p.scopeSteps[1].Problem).To(Equal("the project here is your home directory; tick User instead"), fmt.Sprint(answer))
		}
	})

	It("says why a path that cannot be read is refused, past the cut of a long path", func() {
		p := &scripted{
			scope: []func(cli.ScopeStep) ([]string, error){scopes("other")},
			dirs:  []func(cli.DirectoryStep) (string, error){dir(strings.Repeat("a", 300)), func(cli.DirectoryStep) (string, error) { return "", cli.ErrAborted }},
		}
		lolaP(p, "install")
		Expect(p.dirSteps[1].Problem).To(HaveSuffix(": file name too long"))
		Expect(p.dirSteps[1].Problem).To(ContainSubstring("…"), "the path is cut")
	})

	It("takes the project directory as Other when Project is not picked", func() {
		p := &scripted{
			scope:   []func(cli.ScopeStep) ([]string, error){scopes("user", "other")},
			dirs:    []func(cli.DirectoryStep) (string, error){dir(".")},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("deploy")},
			review:  []string{cli.ReviewApply},
		}
		r := lolaP(p, "install", "-t", "claude-code", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(equivalents(r.err)).To(Equal([]string{
			"lola install deploy --scope project -C " + proj + " -t claude-code",
			"lola install deploy --scope user -t claude-code"}), "project roots first, then user")
	})

	It("shows an Other directory holding control characters made safe", func() {
		evil := filepath.Join(tmp, "evil\x1b[2J")
		p := &scripted{
			scope: []func(cli.ScopeStep) ([]string, error){scopes("other")},
			dirs:  []func(cli.DirectoryStep) (string, error){dir(evil), func(cli.DirectoryStep) (string, error) { return "", cli.ErrAborted }},
		}
		lolaP(p, "install")
		Expect(p.dirSteps[1].Problem).To(Equal(filepath.Join(tmp, "evil?[2J") + " does not exist"))
		Expect(p.dirSteps[1].Value).To(BeEmpty(), "an unsafe answer is not offered again")
	})

	It("installs into Project, Other and User with a page per destination when their picks differ, and one shared page when equal", func() {
		other := filepath.Join(tmp, "other")
		Expect(os.MkdirAll(other, 0o755)).To(Succeed())
		Expect(os.MkdirAll(filepath.Dir(selections), 0o700)).To(Succeed())
		Expect(os.WriteFile(selections, []byte("version: 1\nuser:\n  review-kit:\n    skip: [rule/style]\n"), 0o600)).To(Succeed())
		p := &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project", "user", "other")},
			dirs:       []func(cli.DirectoryStep) (string, error){dir(other)},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep("command/ship"), keep("skill/slow-lint"), keep()},
			review:     []string{cli.ReviewApply},
		}
		r := lolaP(p, "install", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"scope", "directory", "modules", "components:review-kit — project (" + proj + ")",
			"components:review-kit — project (" + other + ")", "components:review-kit — user", "review"}))
		Expect(r.err).To(ContainSubstring("Summary:\n  project (" + proj + "), picked:\n"))
		Expect(r.err).To(ContainSubstring("\n  project (" + other + "), picked:\n"))
		Expect(r.err).To(ContainSubstring("\n  user, picked:\n"))
		Expect(equivalents(r.err)).To(Equal([]string{
			"lola install review-kit --scope project -C " + proj + " -t claude-code --skip review-kit:command/ship",
			"lola install review-kit --scope project -C " + other + " -t claude-code --skip review-kit:skill/slow-lint",
			"lola install review-kit --scope user -t claude-code"}))
		b, err := os.ReadFile(selections)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(ContainSubstring("  " + proj + ":\n    review-kit:\n      skip: [command/ship]\n"))
		Expect(string(b)).To(ContainSubstring("  " + other + ":\n    review-kit:\n      skip: [skill/slow-lint]\n"))
		Expect(filepath.Join(proj, ".claude/commands/ship.md")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(other, ".claude/skills/slow-lint")).NotTo(BeADirectory())
		Expect(filepath.Join(other, ".claude/commands/ship.md")).To(BeARegularFile())
		Expect(filepath.Join(home, ".claude/commands/ship.md")).To(BeARegularFile())
		projSteps, otherSteps, ok := strings.Cut(r.out, "project ("+other+"):\n")
		Expect(ok).To(BeTrue(), r.out)
		Expect(projSteps).NotTo(ContainSubstring("commands/ship.md"), "each root lists its own steps")
		Expect(otherSteps).To(ContainSubstring("commands/ship.md"))

		Expect(os.Remove(selections)).To(Succeed())
		p = &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project", "user", "other")},
			dirs:       []func(cli.DirectoryStep) (string, error){dir(other)},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep("skill/review")},
			review:     []string{cli.ReviewApply},
		}
		r = lolaP(p, "install", "-t", "claude-code", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"scope", "directory", "modules", "components:review-kit", "review"}))
		Expect(equivalents(r.err)).To(HaveLen(3))
	})

	It("keeps the picks of the destinations Change scope keeps, and drops and counts the rest", func() {
		other := filepath.Join(tmp, "other")
		Expect(os.MkdirAll(other, 0o755)).To(Succeed())
		p := &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project", "other", "user"), scopes("project", "user")},
			dirs:       []func(cli.DirectoryStep) (string, error){dir("../other")},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep("command/ship")},
			review:     []string{cli.ReviewScope, cli.ReviewApply},
		}
		r := lolaP(p, "install", "-t", "claude-code", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"scope", "directory", "modules", "components:review-kit", "review", "scope", "review"}))
		Expect(p.scopeSteps[1].Preselect).To(Equal([]string{"project", "other", "user"}))
		Expect(r.err).To(ContainSubstring("  3 component picks dropped: scope removed\n"))
		Expect(equivalents(r.err)).To(Equal([]string{
			"lola install review-kit --scope project -C " + proj + " -t claude-code --skip review-kit:command/ship",
			"lola install review-kit --scope user -t claude-code --skip review-kit:command/ship"}))
	})

	It("offers the last Other directory again when Change scope asks", func() {
		other := filepath.Join(tmp, "other")
		Expect(os.MkdirAll(other, 0o755)).To(Succeed())
		p := &scripted{
			scope:   []func(cli.ScopeStep) ([]string, error){scopes("other"), scopes("other")},
			dirs:    []func(cli.DirectoryStep) (string, error){dir("../other"), func(st cli.DirectoryStep) (string, error) { return st.Value, nil }},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("deploy")},
			review:  []string{cli.ReviewScope, cli.ReviewApply},
		}
		r := lolaP(p, "install", "-t", "claude-code", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.dirSteps[1]).To(Equal(cli.DirectoryStep{Value: "../other"}))
		Expect(equivalents(r.err)).To(Equal([]string{"lola install deploy --scope project -C " + other + " -t claude-code"}))
	})

	It("gives each destination its own page when their preselections differ, and one shared page otherwise", func() {
		Expect(os.MkdirAll(filepath.Dir(selections), 0o700)).To(Succeed())
		Expect(os.WriteFile(selections, []byte("version: 1\nuser:\n  review-kit:\n    skip: [rule/style]\n"), 0o600)).To(Succeed())
		p := &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project", "user")},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep("command/ship"), keep()},
			review:     []string{cli.ReviewApply},
		}
		r := lolaP(p, "install", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"scope", "modules", "components:review-kit — project (" + proj + ")", "components:review-kit — user", "review"}))
		Expect(p.pages[1].Groups[2].Options[0].Label).To(Equal("style — Be brief. (skipped in selections.yaml)"))
		Expect(equivalents(r.err)).To(Equal([]string{
			"lola install review-kit --scope project -C " + proj + " -t claude-code --skip review-kit:command/ship",
			"lola install review-kit --scope user -t claude-code"}))
		Expect(filepath.Join(proj, ".claude/commands/ship.md")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(home, ".claude/commands/ship.md")).To(BeARegularFile())

		Expect(os.Remove(selections)).To(Succeed())
		p = &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project", "user")},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("deploy", "review-kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep("skill/slow-lint")},
			review:     []string{cli.ReviewApply},
		}
		r = lolaP(p, "install", "-t", "claude-code", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"scope", "modules", "components:review-kit", "review"}))
		Expect(equivalents(r.err)).To(Equal([]string{
			"lola install deploy review-kit --scope project -C " + proj + " -t claude-code --skip review-kit:skill/slow-lint",
			"lola install deploy review-kit --scope user -t claude-code --skip review-kit:skill/slow-lint"}))
	})

	It("shows rules disabled under --no-rules and the runtime hooks line", func() {
		writeTree(review, map[string]string{"lola.yml": "name: review-kit\ncomponents:\n  runtime_hooks: [{path: hooks.json}]\n",
			"hooks.json": `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"x"}]}]}}`})
		Expect(lola("cache", "add", review).code).To(Equal(0))
		p := &scripted{components: []func(cli.ComponentPage) ([]string, error){keep()}, review: []string{cli.ReviewApply}}
		r := lolaP(p, "install", "review-kit", "--scope", "project", "--no-rules", "-n")
		Expect(r.code).To(Equal(0), r.err)
		rules := p.pages[0].Groups[2]
		Expect(rules.Disabled).To(BeTrue())
		Expect(rules.Note).To(Equal("(off: --no-rules)"))
		Expect(rules.Options[0].Selected).To(BeFalse())
		Expect(p.pages[0].Footer).To(Equal("Runtime hooks: 1 (you will be asked before they are installed)"))
		Expect(equivalents(r.err)).To(Equal([]string{"lola install review-kit --scope project -t claude-code --no-rules"}))
	})

	It("gives no page to a module whose only components are rules that are off", func() {
		house := filepath.Join(tmp, "src", "house-rules")
		writeTree(house, map[string]string{"lola.yml": "name: house-rules\n", "rules/brief.md": "Be brief.\n", "rules/kind.md": "Be kind.\n"})
		Expect(lola("cache", "add", house).code).To(Equal(0))
		p := &scripted{}
		r := lolaP(p, "install", "house-rules", "--scope", "project", "--no-rules", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(BeEmpty(), "there is nothing to pick")
	})

	It("refuses --ref or --path without a source argument, before asking anything", func() {
		for _, flag := range []string{"--ref", "--path"} {
			p := &scripted{}
			r := lolaP(p, "install", flag, "v1")
			Expect(r.code).NotTo(Equal(0))
			Expect(r.err).To(ContainSubstring("--ref and --path apply to a source argument, and none was given"))
			Expect(r.err).To(ContainSubstring("lola install <source> " + flag + " …"))
			Expect(p.calls).To(BeEmpty())
		}
	})

	It("shows a project path holding control characters made safe", func() {
		evil := filepath.Join(tmp, "evil\x1b[2J")
		Expect(os.MkdirAll(filepath.Join(evil, ".lola"), 0o755)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(evil, ".claude"), 0o755)).To(Succeed())
		GinkgoT().Chdir(evil)
		safe := filepath.Join(tmp, "evil?[2J")
		r := lola("install", "deploy", "-t", "claude-code", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(ContainSubstring("scope: project (found .lola/ at " + safe + ")"))
		Expect(r.err).NotTo(ContainSubstring("\x1b"))

		p := &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep("command/ship")},
			review:     []string{cli.ReviewApply},
		}
		r = lolaP(p, "install", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.scopeSteps[0].ProjectRoot).To(Equal(safe))
		Expect(r.err).To(ContainSubstring("  project (" + safe + "), picked:\n"))
		Expect(r.err).To(ContainSubstring("    review-kit in projects[" + safe + "]: skip [command/ship]\n"))
		Expect(r.err).NotTo(ContainSubstring("\x1b"))
	})

	It("asks before installing a module with no components; No drops it from the run", func() {
		p := &scripted{
			components: []func(cli.ComponentPage) ([]string, error){pick()},
			empty:      []bool{false},
		}
		r := lolaP(p, "install", "review-kit", "--scope", "project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"components:review-kit", "empty:review-kit"}))
		Expect(r.err).To(HaveSuffix("aborted; nothing changed.\n"), "nothing is left to install")
		Expect(selections).NotTo(BeAnExistingFile())

		p = &scripted{components: []func(cli.ComponentPage) ([]string, error){pick()}, empty: []bool{true}, review: []string{cli.ReviewApply}}
		r = lolaP(p, "install", "review-kit", "--scope", "project")
		Expect(r.code).To(Equal(0), r.err)
		b, err := os.ReadFile(selections)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(ContainSubstring("skip: [skill/review, skill/slow-lint, command/ship, rule/style]"))
	})

	It("installs the version the pickers listed, though the source changed meanwhile", func() {
		p := &scripted{
			components: []func(cli.ComponentPage) ([]string, error){func(pg cli.ComponentPage) ([]string, error) {
				writeTree(review, map[string]string{"commands/late.md": "Late.\n", "commands/ship.md": "Changed.\n"})
				return keep()(pg)
			}},
			review: []string{cli.ReviewApply},
		}
		r := lolaP(p, "install", review, "--scope", "project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(filepath.Join(proj, ".claude/commands/late.md")).NotTo(BeAnExistingFile())
		b, err := os.ReadFile(filepath.Join(proj, ".claude/commands/ship.md"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(ContainSubstring("Ship."))
		Expect(equivalents(r.err)).To(Equal([]string{"lola install " + review + " --scope project -t claude-code"}))
	})

	It("installs the cached version a picker listed, though the cache moved on meanwhile", func() {
		p := &scripted{
			scope:   []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("deploy")},
			review:  []string{cli.ReviewApply},
		}
		p.modules[0] = func(cli.ModuleStep) (cli.ModuleAnswer, error) {
			writeTree(deploy, map[string]string{"commands/deploy.md": "Deploy v2.\n"})
			Expect(lola("cache", "update", "deploy").code).To(Equal(0))
			return cli.ModuleAnswer{Names: []string{"deploy"}}, nil
		}
		r := lolaP(p, "install")
		Expect(r.code).To(Equal(0), r.err)
		b, err := os.ReadFile(filepath.Join(proj, ".claude/commands/deploy.md"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal("Deploy.\n"))
	})

	It("says already up to date when nothing would change", func() {
		Expect(lola("install", "deploy", "--scope", "project").code).To(Equal(0))
		p := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes("project")}, modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("deploy")}}
		r := lolaP(p, "install")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(Equal("✓ already up to date\n"))
		Expect(p.moduleSteps[0].Options[0]).To(Equal(cli.ModuleOption{Name: "deploy", Label: deploy + " · deploy (installed)"}))
	})

	It("repairs a missing file and reports a hand-edited one instead of saying already up to date", func() {
		Expect(lola("install", "deploy", "--scope", "project").code).To(Equal(0))
		rendered := filepath.Join(proj, ".claude/commands/deploy.md")
		Expect(os.Remove(rendered)).To(Succeed())
		p := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes("project")}, modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("deploy")},
			review: []string{cli.ReviewApply}}
		r := lolaP(p, "install")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).NotTo(ContainSubstring("already up to date"))
		Expect(r.out).To(ContainSubstring("(was missing; rewritten)"))
		Expect(rendered).To(BeARegularFile())

		Expect(os.WriteFile(rendered, []byte("Hand-edited.\n"), 0o644)).To(Succeed())
		p = &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes("project")}, modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("deploy")},
			review: []string{cli.ReviewApply}}
		r = lolaP(p, "install")
		Expect(r.code).To(Equal(1), r.out)
		Expect(r.err).To(ContainSubstring("hand-edited since lola wrote it"))
	})

	It("goes through the engine under --force though nothing changed", func() {
		Expect(lola("install", "deploy", "--scope", "project").code).To(Equal(0))
		p := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes("project")}, modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("deploy")},
			review: []string{cli.ReviewApply}}
		r := lolaP(p, "install", "--force")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).NotTo(ContainSubstring("already up to date"))
		Expect(r.out).To(ContainSubstring("✓ installed deploy (claude-code)"))
	})

	It("adds a source from the empty cache and selects it, and the summary repeats the cache add", func() {
		Expect(lola("cache", "rm", "review-kit", "-y").code).To(Equal(0))
		Expect(lola("cache", "rm", "deploy", "-y").code).To(Equal(0))
		p := &scripted{
			scope: []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){
				func(st cli.ModuleStep) (cli.ModuleAnswer, error) {
					Expect(st.Options).To(BeEmpty())
					return cli.ModuleAnswer{AddSource: true, Source: filepath.Join(tmp, "nowhere")}, nil
				},
				func(st cli.ModuleStep) (cli.ModuleAnswer, error) {
					Expect(st.Problem).To(HavePrefix("could not add "))
					return cli.ModuleAnswer{AddSource: true, Source: "../src/deploy"}, nil
				},
				func(st cli.ModuleStep) (cli.ModuleAnswer, error) {
					Expect(st.Preselect).To(Equal([]string{"deploy"}))
					return cli.ModuleAnswer{Names: st.Preselect}, nil
				},
			},
			review: []string{cli.ReviewApply},
		}
		r := lolaP(p, "install", "-n")
		Expect(r.code).To(Equal(0), r.out+r.err)
		Expect(equivalents(r.err)).To(Equal([]string{"lola cache add ../src/deploy", "lola install deploy --scope project -t claude-code"}))
	})

	It("prints equivalent commands that resolve to the same plan through the real command tree", func() {
		writeTree(proj, map[string]string{"sub dir/.keep": ""})
		Expect(os.MkdirAll(filepath.Join(tmp, "other"), 0o755)).To(Succeed())
		GinkgoT().Chdir(filepath.Join(proj, "sub dir"))
		Expect(os.MkdirAll(filepath.Dir(selections), 0o700)).To(Succeed())
		Expect(os.WriteFile(selections, []byte("version: 1\nglobal:\n  review-kit:\n    skip: [rule/style]\n"), 0o600)).To(Succeed())
		before, err := os.ReadFile(selections)
		Expect(err).NotTo(HaveOccurred())
		// Drops a component and takes back the one the global section skips.
		page := func(pg cli.ComponentPage) ([]string, error) {
			out, _ := keep("command/ship")(pg)
			return append(out, "rule/style"), nil
		}
		for _, tc := range []struct {
			answer, flags []string
		}{{answer: []string{"project"}}, {answer: []string{"user"}}, {answer: []string{"project", "user"}},
			{answer: []string{"project", "other", "user"}},
			{answer: []string{"project"}, flags: []string{"--skip", "review-kit:skill/slow-lint"}},
			{answer: []string{"project", "user"}, flags: []string{"--only", "review-kit:skill/review", "--global"}}} {
			answer := tc.answer
			p := &scripted{
				scope:   []func(cli.ScopeStep) ([]string, error){scopes(answer...)},
				modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit", "deploy")},
				review:  []string{cli.ReviewApply},
				dirs:    []func(cli.DirectoryStep) (string, error){dir(filepath.Join(tmp, "other"))},
			}
			if tc.flags == nil {
				p.components = []func(cli.ComponentPage) ([]string, error){page}
			}
			r := lolaP(p, append([]string{"install", "-n"}, tc.flags...)...)
			Expect(r.code).To(Equal(0), r.err)
			want := r.out
			var got strings.Builder
			for _, line := range equivalents(r.err) {
				words := shellWords(line)
				Expect(words[:2]).To(Equal([]string{"lola", "install"}))
				for _, f := range tc.flags {
					n := 0
					for _, w := range words {
						if w == f {
							n++
						}
					}
					Expect(n).To(Equal(1), line+": each flag once")
				}
				eq := lola(append(words[1:], "-n")...)
				Expect(eq.code).To(Equal(0), line+"\n"+eq.err)
				got.WriteString(eq.out)
			}
			for _, l := range strings.Split(strings.TrimSpace(got.String()), "\n") {
				if strings.HasPrefix(l, "  ") {
					Expect(want).To(ContainSubstring(l+"\n"), fmt.Sprint(answer))
				}
			}
			Expect(strings.Count(want, "\n  ")).To(Equal(strings.Count(got.String(), "\n  ")), fmt.Sprint(answer)+"\n"+want+"\n---\n"+got.String())
			after, err := os.ReadFile(selections)
			Expect(err).NotTo(HaveOccurred())
			Expect(after).To(Equal(before), "dry runs write nothing")
		}
	})

	It("keeps the version a cache pick was listed at when Change scope lists again", func() {
		p := &scripted{
			scope: []func(cli.ScopeStep) ([]string, error){scopes("project"), scopes("project")},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){func(cli.ModuleStep) (cli.ModuleAnswer, error) {
				writeTree(deploy, map[string]string{"commands/deploy.md": "Deploy v2.\n", "commands/extra.md": "Extra.\n"})
				Expect(lola("cache", "update", "deploy").code).To(Equal(0))
				return cli.ModuleAnswer{Names: []string{"deploy"}}, nil
			}},
			review: []string{cli.ReviewScope, cli.ReviewApply},
		}
		r := lolaP(p, "install")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"scope", "modules", "review", "scope", "review"}))
		b, err := os.ReadFile(filepath.Join(proj, ".claude/commands/deploy.md"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal("Deploy.\n"), "the version the module picker listed")
		Expect(filepath.Join(proj, ".claude/commands/extra.md")).NotTo(BeAnExistingFile())
		if b, err := os.ReadFile(selections); err == nil {
			Expect(string(b)).NotTo(ContainSubstring("extra"), "no page ever showed it, so nothing skips it")
		}
	})

	It("says once on stderr why a cached module is left out of the picker, though Change scope lists again", func() {
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(proj, ".lola", "project.yml"),
			[]byte("modules:\n  - name: deploy\n    source: "+deploy+"\n    ref: v9\n"), 0o644)).To(Succeed())
		p := &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project"), scopes("project")},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep(), keep()},
			review:     []string{cli.ReviewScope, cli.ReviewApply},
		}
		r := lolaP(p, "install", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.moduleSteps[0].Options).To(HaveLen(1), "deploy is left out")
		Expect(strings.Count(r.err, "⚠ deploy is not listed: your cache has no version of ref v9")).To(Equal(1), r.err)
	})

	It("offers Apply, Change components and Quit when only the component picker ran", func() {
		p := &scripted{components: []func(cli.ComponentPage) ([]string, error){keep()}, review: []string{cli.ReviewApply}}
		r := lolaP(p, "install", "review-kit", "--scope", "project", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.reviewSteps[0].Options).To(Equal([]string{"apply", "components", "quit"}))
	})

	It("never asks under --json and prints no equivalent commands", func() {
		p := &scripted{}
		r := lolaP(p, "install", "review-kit", "--scope", "project", "--json", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(BeEmpty())
		v := parseInstall(r.out)
		Expect(v.EquivalentCommands).NotTo(BeNil())
		Expect(v.EquivalentCommands).To(BeEmpty())
	})

	It("counts a dropped module's picks once, not once per scope, and not again in the next summary", func() {
		p := &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project", "user")},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit", "deploy"), mods("deploy"), mods("deploy", "review-kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep("command/ship"), keep()},
			review:     []string{cli.ReviewModules, cli.ReviewModules, cli.ReviewApply},
		}
		r := lolaP(p, "install", "-t", "claude-code", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(ContainSubstring("  3 component picks dropped: module removed\n"))
		Expect(strings.Count(r.err, "component picks dropped")).To(Equal(1), "the next summary starts from zero")
	})

	It("counts one dropped pick in the singular", func() {
		p := &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit", "deploy"), mods("review-kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep()},
			review:     []string{cli.ReviewModules, cli.ReviewApply},
		}
		r := lolaP(p, "install", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(ContainSubstring("  1 component pick dropped: module removed\n"))

		p = &scripted{
			scope:   []func(cli.ScopeStep) ([]string, error){scopes("project", "user"), scopes("project")},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("deploy")},
			review:  []string{cli.ReviewScope, cli.ReviewApply},
		}
		r = lolaP(p, "install", "-t", "claude-code", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(ContainSubstring("  1 component pick dropped: scope removed\n"))
	})

	It("reports the picks lost when Change scope removes a scope", func() {
		p := &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project", "user"), scopes("project")},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep("command/ship")},
			review:     []string{cli.ReviewScope, cli.ReviewApply},
		}
		r := lolaP(p, "install", "-t", "claude-code", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(ContainSubstring("  3 component picks dropped: scope removed\n"))
	})

	It("drops a Change entry once its step is no longer shown", func() {
		p := &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit", "deploy"), mods("deploy")},
			components: []func(cli.ComponentPage) ([]string, error){keep()},
			review:     []string{cli.ReviewModules, cli.ReviewApply},
		}
		r := lolaP(p, "install", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.reviewSteps[0].Options).To(Equal([]string{"apply", "scope", "modules", "components", "quit"}))
		Expect(p.reviewSteps[1].Options).To(Equal([]string{"apply", "scope", "modules", "quit"}), "deploy has no page, so there is nothing to change")
	})

	It("prints an equivalent command with an unsafe -C path made safe, and says it is not copyable", func() {
		evil := filepath.Join(tmp, "evil\x1b[2J")
		Expect(os.MkdirAll(filepath.Join(evil, ".claude"), 0o755)).To(Succeed())
		p := &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep("command/ship")},
			review:     []string{cli.ReviewApply},
		}
		r := lolaP(p, "-C", evil, "install", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(ContainSubstring("-C '" + filepath.Join(tmp, "evil?[2J") + "'"))
		Expect(r.err).To(ContainSubstring("not copyable as shown"))
		Expect(r.err).NotTo(ContainSubstring("\x1b"))
	})

	It("does not preselect a module twice when its source is added again", func() {
		p := &scripted{
			scope: []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){
				func(cli.ModuleStep) (cli.ModuleAnswer, error) {
					return cli.ModuleAnswer{AddSource: true, Source: deploy}, nil
				},
				func(st cli.ModuleStep) (cli.ModuleAnswer, error) {
					Expect(st.Preselect).To(Equal([]string{"deploy"}))
					return cli.ModuleAnswer{AddSource: true, Source: deploy}, nil
				},
				func(st cli.ModuleStep) (cli.ModuleAnswer, error) {
					Expect(st.Preselect).To(Equal([]string{"deploy"}))
					return cli.ModuleAnswer{Names: st.Preselect}, nil
				},
			},
			review: []string{cli.ReviewApply},
		}
		r := lolaP(p, "install", "-n")
		Expect(r.code).To(Equal(0), r.out+r.err)
	})

	It("installs the version a source added again fetched, not the one picked before", func() {
		p := &scripted{
			scope: []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){
				mods("deploy"),
				func(cli.ModuleStep) (cli.ModuleAnswer, error) {
					writeTree(deploy, map[string]string{"commands/deploy.md": "Deploy v2.\n"})
					return cli.ModuleAnswer{Names: []string{"deploy"}, AddSource: true, Source: deploy}, nil
				},
				func(st cli.ModuleStep) (cli.ModuleAnswer, error) { return cli.ModuleAnswer{Names: st.Preselect}, nil },
			},
			review: []string{cli.ReviewModules, cli.ReviewApply},
		}
		r := lolaP(p, "install")
		Expect(r.code).To(Equal(0), r.err)
		b, err := os.ReadFile(filepath.Join(proj, ".claude/commands/deploy.md"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal("Deploy v2.\n"))
	})

	It("returns to the module picker with the picks when the source to add is left empty", func() {
		p := &scripted{
			scope: []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){
				func(cli.ModuleStep) (cli.ModuleAnswer, error) {
					return cli.ModuleAnswer{AddSource: true, Source: deploy}, nil
				},
				func(cli.ModuleStep) (cli.ModuleAnswer, error) {
					return cli.ModuleAnswer{Names: []string{"review-kit"}, AddSource: true}, nil
				},
				func(st cli.ModuleStep) (cli.ModuleAnswer, error) {
					Expect(st.Preselect).To(Equal([]string{"review-kit"}), "the picks, not the module the first step added")
					Expect(st.Problem).To(BeEmpty(), "an empty source is not a failed add")
					Expect(st.Options).To(HaveLen(2))
					return cli.ModuleAnswer{Names: st.Preselect}, nil
				},
			},
			components: []func(cli.ComponentPage) ([]string, error){keep("command/ship")},
			review:     []string{cli.ReviewApply},
		}
		r := lolaP(p, "install", "-n")
		Expect(r.code).To(Equal(0), r.out+r.err)
		Expect(r.err).NotTo(ContainSubstring("could not add"))
	})
})
