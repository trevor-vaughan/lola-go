package cli_test

// One spec per row of the scope picker and trust question transition tables in
// docs/architecture/tui-flows.md, named "scope picker: <from> --<input>--> <to>" and
// "trust: <from> --<input>--> <to>", the input followed by the row's guard in parentheses where
// rows share a from and an input. Rows an existing spec pins already:
//
// also pinned (scope picker: scope --esc--> scope; scope picker: scope --ctrl+c--> cancelled): scope_paths_test.go "scope picker: start --run (lola sync)--> scope"
// also pinned (scope picker: directory --answer--> next step): scope_paths_test.go "scope picker: scope --answer (Other ticked)--> directory"
// also pinned (trust: start --run (lola sync)--> trust; trust: trust --Trust all--> next step): sync_test.go "asks the trust question after the diff on stderr, and a Cancel installs nothing"
// also pinned (trust: start --run (an activation)--> trust): profile_test.go "puts a project profile in place of your active one only when you turn it on, as its own change"
// also pinned (trust: start --run (no terminal)--> refused): sync_test.go "off a terminal, refuses a pulled change with the diff on stderr: -y does not trust it, --trust does"
// also pinned (trust: start --trust flag--> receipt; trust: start --trust flag (-y)--> next step; trust: receipt --no--> cancelled): trust_test.go "covered items print one receipt grouped by kind, then one question; -y answers it; No writes nothing"
// also pinned (trust: trust --Trust all (--trust covers the rest)--> receipt): trust_test.go "uncovered items are asked one by one before the receipt"
// also pinned (trust: receipt --yes--> done): trust_test.go "sync --scope both shows one receipt for both scopes; No applies neither"
// also pinned (trust: start --run (only removals)--> removals): scope_paths_test.go "trust: removals --esc (first screen)--> removals"
// also pinned (trust: trust --Trust all (removals follow)--> removals; trust: removals --no--> declined): sync_test.go "prints the removals it would make, then asks to confirm them; a No keeps the trust answer"
// also pinned (trust: removals --no (an activation)--> declined): profile_test.go "a No to an activation's removals names the activation to run again"
// also pinned (trust: trust --Choose…--> choose): keys_internal_test.go "trust: choose --esc (first list)--> trust"
// also pinned (trust: choose --answer--> next step): sync_test.go "the trust picker's equivalent command applies the same plan"
// also pinned (trust: trust --esc--> trust): keys_internal_test.go "trust: trust --esc--> trust"
// also pinned (trust: choose --empty submit--> choose): keys_internal_test.go "trust: choose --empty submit--> choose"
// also pinned (trust: choose --esc (first list)--> trust): keys_internal_test.go "trust: choose --esc (first list)--> trust"
// also pinned (trust: choose --esc (a later list)--> choose): keys_internal_test.go "trust: choose --esc (a later list)--> choose"
// also pinned (trust: choose --ctrl+c--> cancelled): keys_internal_test.go "trust: choose --ctrl+c--> cancelled"

import (
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/cli"
	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// trustAll answers the trust question with Trust all.
func trustAll(cli.TrustStep) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }

var _ = Describe("trust question paths", func() {
	var proj, manPath, kitMd, otherMd string
	BeforeEach(func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj = filepath.Join(tmp, "proj")
		manPath = filepath.Join(proj, ".lola", "project.yml")
		writeTree(filepath.Join(proj, "mods", "kit"), map[string]string{"lola.yml": "name: kit\n", "commands/kit.md": "Kit\n"})
		writeTree(filepath.Join(proj, "mods", "other"), map[string]string{"lola.yml": "name: other\n", "commands/other.md": "Other\n"})
		r := lola("install", filepath.Join(proj, "mods", "kit"), "-C", proj, "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		GinkgoT().Chdir(proj)
		kitMd, otherMd = filepath.Join(proj, ".claude", "commands", "kit.md"), filepath.Join(proj, ".claude", "commands", "other.md")
	})
	// swap is a pull that drops kit and adds other: a trust question, then a removals question.
	swap := func() {
		GinkgoHelper()
		Expect(os.WriteFile(manPath, []byte("version: 1\ntargets: [claude-code]\nmodules:\n  - name: other\n    source: mods/other\n"), 0o644)).To(Succeed())
	}
	const removeQ = "confirm:Remove these and sync? No applies nothing"

	It("trust: start --run (lola update)--> trust", func() {
		writeTree(filepath.Join(proj, "mods", "kit"), map[string]string{"commands/kit.md": "Kit v2\n"})
		sp := &scripted{trust: []func(cli.TrustStep) (engine.TrustAnswer, error){trustAll}}
		r := lolaP(sp, "update", "--project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"trust"}), "a folder's changed content is asked about")
		Expect(os.ReadFile(kitMd)).To(Equal([]byte("Kit v2\n")))
	})

	It("trust: start --run (-y)--> trust", func() {
		swap()
		sp := &scripted{trust: []func(cli.TrustStep) (engine.TrustAnswer, error){trustAll}}
		r := lolaP(sp, "sync", "--project", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"trust"}), "-y never trusts; it answers the removals")
		Expect(otherMd).To(BeARegularFile())
		Expect(kitMd).NotTo(BeAnExistingFile())
	})

	It("trust: start --run (-n)--> done", func() {
		swap()
		before, err := os.ReadFile(manPath)
		Expect(err).NotTo(HaveOccurred())
		sp := &scripted{}
		r := lolaP(sp, "sync", "--project", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(BeEmpty(), "a dry run asks nothing")
		Expect(r.out).To(ContainSubstring(".lola/project.yml changed since lola last wrote it or you last trusted it:\n"))
		Expect(os.ReadFile(manPath)).To(Equal(before))
		Expect(otherMd).NotTo(BeAnExistingFile())
		Expect(kitMd).To(BeARegularFile())
	})

	It("trust: trust --Cancel or ctrl+c--> cancelled", func() {
		swap()
		sp := &scripted{trust: []func(cli.TrustStep) (engine.TrustAnswer, error){aborted[cli.TrustStep, engine.TrustAnswer]}}
		r := lolaP(sp, "sync", "--project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"trust"}))
		Expect(otherMd).NotTo(BeAnExistingFile())
		Expect(kitMd).To(BeARegularFile())
		sp = &scripted{trust: []func(cli.TrustStep) (engine.TrustAnswer, error){aborted[cli.TrustStep, engine.TrustAnswer]}}
		lolaP(sp, "sync", "--project")
		Expect(sp.calls).To(Equal([]string{"trust"}), "nothing was recorded: it is asked again")
	})

	It("trust: removals --yes--> done", func() {
		swap()
		sp := &scripted{trust: []func(cli.TrustStep) (engine.TrustAnswer, error){trustAll}, confirm: []bool{true}}
		r := lolaP(sp, "sync", "--project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"trust", removeQ}))
		Expect(r.err).To(ContainSubstring("→ removing kit\n"))
		Expect(otherMd).To(BeARegularFile())
		Expect(kitMd).NotTo(BeAnExistingFile())
		sp = &scripted{}
		Expect(lolaP(sp, "sync", "--project").code).To(Equal(0))
		Expect(sp.calls).To(BeEmpty(), "the trust is recorded")
	})

	It("trust: removals --ctrl+c--> cancelled", func() {
		swap()
		sp := &scripted{trust: []func(cli.TrustStep) (engine.TrustAnswer, error){trustAll}, confirmCancels: 1}
		r := lolaP(sp, "sync", "--project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"trust", removeQ}))
		Expect(otherMd).NotTo(BeAnExistingFile())
		Expect(kitMd).To(BeARegularFile())
	})

	It("trust: removals --no (several scopes)--> declined", func() {
		pp, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		writeTree(filepath.Join(proj, "..", "solo"), map[string]string{"lola.yml": "name: solo\n", "commands/solo.md": "Solo\n"})
		writeTree(pp.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: solo, source: " +
			filepath.Join(proj, "..", "solo") + "}\n"})
		swap()
		sp := &scripted{trust: []func(cli.TrustStep) (engine.TrustAnswer, error){trustAll}, confirm: []bool{false}}
		r := lolaP(sp, "sync", "--scope", "both")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"trust", removeQ}))
		Expect(r.err).To(ContainSubstring("removals declined; the project was not changed. The changes you trusted are recorded"))
		Expect(filepath.Join(pp.Home, ".claude", "commands", "solo.md")).NotTo(BeAnExistingFile(), "user scope, after it, is not synced")
		Expect(kitMd).To(BeARegularFile())
	})

	It("trust: removals --esc (after a trust answer)--> trust", func() {
		swap()
		sp := &scripted{trust: []func(cli.TrustStep) (engine.TrustAnswer, error){trustAll, trustAll}, confirmBacks: 1, confirm: []bool{true}}
		r := lolaP(sp, "sync", "--project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"trust", removeQ, "trust", removeQ}))
		Expect(sp.confirmNavs[0].Back).To(BeTrue(), "the trust question before it is a screen to go back to")
		Expect(sp.trustSteps[0].Answer).To(Equal(engine.TrustAnswer{}), "first asked, the cursor on Cancel")
		Expect(sp.trustSteps[1].Answer).To(Equal(engine.TrustAnswer{All: true}), "asked again, its answer kept")
		Expect(sp.trustSteps[1].Changes).To(Equal(sp.trustSteps[0].Changes))
		Expect(r.err).To(MatchRegexp(`(?s)→ trusting .*→ back to the trust question: the answer above no longer stands\n.*→ trusting `),
			"the first answer's echo is marked as replaced")
		Expect(otherMd).To(BeARegularFile())
		Expect(kitMd).NotTo(BeAnExistingFile())
	})

	It("trust: removals --esc (first screen)--> removals", func() {
		Expect(os.WriteFile(manPath, []byte("version: 1\ntargets: [claude-code]\nmodules: []\n"), 0o644)).To(Succeed())
		sp := &scripted{trust: []func(cli.TrustStep) (engine.TrustAnswer, error){trustAll}, confirm: []bool{false}}
		Expect(lolaP(sp, "sync", "--project").code).To(Equal(0))
		sp = &scripted{confirm: []bool{true}}
		r := lolaP(sp, "sync", "--project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{removeQ}), "the trust given before is recorded: only the removals are asked")
		Expect(sp.confirmNavs[0].Back).To(BeFalse(), "a first screen: Esc stays")
		Expect(kitMd).NotTo(BeAnExistingFile())
	})
})

var _ = Describe("scope picker paths", func() {
	var tmp, home, proj, kitMd string
	var state func() map[string]string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		home = os.Getenv("HOME")
		proj = filepath.Join(tmp, "proj")
		writeTree(filepath.Join(tmp, "kit"), map[string]string{"lola.yml": "name: kit\n", "commands/kit.md": "Kit\n"})
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		r := lola("install", filepath.Join(tmp, "kit"), "-C", proj, "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		kitMd = filepath.Join(proj, ".claude", "commands", "kit.md")
		pp, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		writeTree(pp.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\n"})
		GinkgoT().Chdir(proj)
		// state is every file under tmp but lola's cache, logs and runtime files, as read now.
		state = func() map[string]string {
			GinkgoHelper()
			out := map[string]string{}
			Expect(filepath.WalkDir(tmp, func(path string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() || strings.Contains(path, string(filepath.Separator)+"cache"+string(filepath.Separator)) {
					return err
				}
				b, err := os.ReadFile(path)
				out[path] = string(b)
				return err
			})).To(Succeed())
			return out
		}
	})

	DescribeTable("a command that writes asks where on a terminal with no scope flag; the picker is its first screen",
		func(args []string, question string, one bool) {
			before := state()
			sp := &scripted{scope: []func(cli.ScopeStep) ([]string, error){aborted[cli.ScopeStep, []string]}}
			r := lolaP(sp, args...)
			Expect(r.code).To(Equal(0), r.err)
			Expect(sp.calls).To(Equal([]string{"scope"}))
			Expect(sp.scopeSteps[0]).To(And(HaveField("Question", question), HaveField("One", one), HaveField("Nav.Back", false),
				HaveField("Preselect", BeEmpty()), HaveField("ProjectRoot", proj), HaveField("Home", home)))
			Expect(state()).To(Equal(before), "Ctrl+C at the picker writes nothing")
		},
		Entry("scope picker: start --run (lola sync)--> scope", []string{"sync"}, "Sync where?", false),
		Entry("scope picker: start --run (lola update)--> scope", []string{"update"}, "Update where?", false),
		Entry("scope picker: start --run (lola tidy)--> scope", []string{"tidy"}, "Tidy where?", true),
		Entry("scope picker: start --run (lola repair)--> scope", []string{"repair"}, "Repair where?", true),
		Entry("scope picker: start --run (lola uninstall)--> scope", []string{"uninstall", "kit"}, "Uninstall from where?", true),
		Entry("scope picker: start --run (lola profile --on)--> scope", []string{"profile", "go", "--on"}, "Turn it on where?", true),
		Entry("scope picker: start --run (lola profile --off)--> scope", []string{"profile", "--off"}, "Turn it off where?", true),
		Entry("scope picker: start --run (lola profile --switch)--> scope", []string{"profile", "go", "--switch"}, "Switch where?", true),
		Entry("scope picker: start --run (lola profile --move)--> scope", []string{"profile", "--move", "kit", "--to", "go"}, "Move where?", true),
		Entry("scope picker: start --run (lola profile --split)--> scope", []string{"profile", "go", "--split"}, "Split where?", true),
		Entry("scope picker: start --run (lola profile --remove)--> scope", []string{"profile", "go", "--remove"}, "Check which scope before removing it?", true),
	)

	It("scope picker: start --scope flag--> next step", func() {
		for _, flag := range [][]string{{"--project"}, {"--user"}, {"-C", proj}, {"--scope", "project"}} {
			sp := &scripted{}
			r := lolaP(sp, append([]string{"sync"}, flag...)...)
			Expect(r.code).To(Equal(0), r.err)
			Expect(sp.calls).To(BeEmpty(), "%v names the scope: nothing is asked", flag)
		}
	})

	It("scope picker: start --run (-n, -y or --force)--> scope", func() {
		for _, flag := range []string{"-n", "-y", "--force"} {
			sp := &scripted{scope: []func(cli.ScopeStep) ([]string, error){aborted[cli.ScopeStep, []string]}}
			r := lolaP(sp, "sync", flag)
			Expect(r.code).To(Equal(0), r.err)
			Expect(sp.calls).To(Equal([]string{"scope"}), "%s does not name a scope", flag)
		}
	})

	It("scope picker: start --run (--json)--> next step", func() {
		r := lola("sync", "--json")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring(`"scope": "project"`), "the project found from here")
		GinkgoT().Chdir(tmp)
		r = lola("sync", "--json")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("lola sync needs a scope"))
	})

	It("scope picker: scope --answer--> next step", func() {
		sp := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes(cli.ScopeProject)}}
		Expect(os.Remove(kitMd)).To(Succeed())
		r := lolaP(sp, "sync")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"scope"}))
		Expect(kitMd).To(BeARegularFile(), "synced at the project picked")
	})

	It("scope picker: scope --answer (two ticked, one scope)--> scope", func() {
		sp := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes(cli.ScopeProject, cli.ScopeUser), scopes(cli.ScopeProject)}}
		r := lolaP(sp, "repair")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"scope", "scope"}))
		Expect(sp.scopeSteps[1].Problem).To(Equal("pick one scope"))
		Expect(r.err).To(HavePrefix("project " + proj))
	})

	It("scope picker: scope --answer (two ticked, sync or update)--> next step", func() {
		for _, cmd := range []string{"sync", "update"} {
			sp := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes(cli.ScopeUser, cli.ScopeProject)}}
			r := lolaP(sp, cmd)
			Expect(r.code).To(Equal(0), r.err)
			Expect(sp.calls).To(Equal([]string{"scope"}))
			Expect(strings.Index(r.out, "project")).To(BeNumerically("<", strings.Index(r.out, "user")), "%s: the project first, then user scope", cmd)
		}
	})

	It("scope picker: scope --answer (Project is the home directory, refused)--> scope", func() {
		GinkgoT().Chdir(home)
		for _, args := range [][]string{{"sync"}, {"update"}, {"profile", "go", "--on"}, {"profile", "--off"}, {"profile", "go", "--switch"},
			{"profile", "--move", "kit", "--to", "go"}, {"profile", "go", "--split"}, {"profile", "go", "--remove"}} {
			sp := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes(cli.ScopeProject), aborted[cli.ScopeStep, []string]}}
			r := lolaP(sp, args...)
			Expect(r.code).To(Equal(0), r.err)
			Expect(sp.calls).To(Equal([]string{"scope", "scope"}), "%v", args)
			Expect(sp.scopeSteps[1]).To(And(HaveField("Problem", "the project here is your home directory; tick User instead"),
				HaveField("Preselect", BeEmpty())), "%v", args)
		}
	})

	It("scope picker: scope --answer (Project is the home directory, accepted)--> next step", func() {
		GinkgoT().Chdir(home)
		for _, args := range [][]string{{"tidy", "-y"}, {"repair"}, {"uninstall", "kit"}} {
			sp := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes(cli.ScopeProject)}}
			r := lolaP(sp, args...)
			Expect(sp.calls).To(Equal([]string{"scope"}), "%v", args)
			Expect(r.err).To(ContainSubstring("project "+home), "%v runs at the project there: %s", args, r.err)
		}
	})

	It("scope picker: scope --answer (Project has no .lola/)--> refused", func() {
		bare := filepath.Join(tmp, "bare")
		Expect(os.MkdirAll(bare, 0o755)).To(Succeed())
		GinkgoT().Chdir(bare)
		sp := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes(cli.ScopeProject)}}
		r := lolaP(sp, "sync")
		Expect(sp.scopeSteps[0].ProjectRoot).To(Equal(bare), "the current directory is offered as the project")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: no project.yml in " + bare + "\n  hint: start one: lola install --project <source>\n"))
		Expect(filepath.Join(bare, ".lola")).NotTo(BeADirectory())
	})

	It("scope picker: scope --answer (Other ticked)--> directory", func() {
		other := filepath.Join(tmp, "other")
		writeTree(other, map[string]string{"mods/kit/lola.yml": "name: kit\n", "mods/kit/commands/kit.md": "Kit\n",
			".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: mods/kit}\n"})
		sp := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes(cli.ScopeOther)}, dirs: []func(cli.DirectoryStep) (string, error){dir(other)},
			trust: []func(cli.TrustStep) (engine.TrustAnswer, error){trustAll}}
		r := lolaP(sp, "sync")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"scope", "directory", "trust"}))
		Expect(sp.dirSteps[0]).To(And(HaveField("Value", ""), HaveField("Nav.Back", true)))
		Expect(filepath.Join(other, ".claude", "commands", "kit.md")).To(BeARegularFile(), "directory --answer--> next step: synced there")
	})

	It("scope picker: directory --answer (home, accepted)--> next step", func() {
		sp := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes(cli.ScopeOther)}, dirs: []func(cli.DirectoryStep) (string, error){dir("~")}}
		r := lolaP(sp, "repair")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"scope", "directory"}))
		Expect(r.err).To(HavePrefix("project "+home), "~ is your home directory, which repair takes as a project")
	})

	It("scope picker: directory --answer (not a directory)--> directory", func() {
		file := filepath.Join(tmp, "kit", "lola.yml")
		answers := []string{"", filepath.Join(tmp, "missing"), file, proj}
		var fns []func(cli.DirectoryStep) (string, error)
		for _, a := range answers {
			fns = append(fns, dir(a))
		}
		fns = append(fns, aborted[cli.DirectoryStep, string])
		before := state()
		sp := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes(cli.ScopeProject, cli.ScopeOther)}, dirs: fns}
		r := lolaP(sp, "sync")
		Expect(r.code).To(Equal(0), r.err)
		var problems []string
		for _, st := range sp.dirSteps[1:] {
			problems = append(problems, st.Problem)
		}
		Expect(problems).To(Equal([]string{"enter the path of a directory", filepath.Join(tmp, "missing") + " does not exist",
			file + " is not a directory", "that is the project above"}), "each answer is asked again, saying why; nothing is created")
		Expect(sp.dirSteps[2].Value).To(Equal(filepath.Join(tmp, "missing")), "the answer offered again")
		Expect(state()).To(Equal(before))
	})

	It("scope picker: directory --answer (home, refused)--> directory", func() {
		sp := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes(cli.ScopeOther)},
			dirs: []func(cli.DirectoryStep) (string, error){dir(home), aborted[cli.DirectoryStep, string]}}
		r := lolaP(sp, "sync")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.dirSteps[1].Problem).To(Equal("that is your user scope (~); tick User instead"))
	})

	It("scope picker: directory --esc--> scope", func() {
		sp := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes(cli.ScopeUser, cli.ScopeOther), aborted[cli.ScopeStep, []string]},
			dirs: []func(cli.DirectoryStep) (string, error){dir(filepath.Join(tmp, "missing")), goneBack[cli.DirectoryStep, string]}}
		r := lolaP(sp, "sync")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"scope", "directory", "directory", "scope"}))
		Expect(sp.scopeSteps[1]).To(And(HaveField("Preselect", []string{cli.ScopeUser, cli.ScopeOther}), HaveField("Problem", "")), "the ticks kept")
	})

	It("scope picker: directory --esc (typed path)--> scope", func() {
		sp := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes(cli.ScopeOther), scopes(cli.ScopeOther)},
			dirs: []func(cli.DirectoryStep) (string, error){dir(filepath.Join(tmp, "missing")), goneBack[cli.DirectoryStep, string], aborted[cli.DirectoryStep, string]}}
		r := lolaP(sp, "sync")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"scope", "directory", "directory", "scope", "directory"}))
		Expect(sp.dirSteps[2]).To(And(HaveField("Value", ""), HaveField("Problem", "")), "the path typed is dropped")
	})

	It("scope picker: directory --ctrl+c--> cancelled", func() {
		before := state()
		sp := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes(cli.ScopeProject, cli.ScopeOther)},
			dirs: []func(cli.DirectoryStep) (string, error){aborted[cli.DirectoryStep, string]}}
		r := lolaP(sp, "sync")
		Expect(r.code).To(Equal(0), r.err)
		Expect(state()).To(Equal(before), "nothing is synced, not even the project ticked with it")
	})
})
