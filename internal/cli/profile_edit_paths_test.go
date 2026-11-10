package cli_test

// One spec per row of the profile edit transition table in docs/architecture/tui-flows.md, named
// "profile edit: <from> --<input>--> <to>", the input followed by the row's guard in parentheses
// where rows share a from and an input. Rows an existing spec pins already:
//
// also pinned (start --module argument (the write target asks)--> where; where --answer--> next step): profile_edit_test.go "asks where the edit goes before the page, which is titled with the profile Override writes"
// also pinned (where --answer (a bundle on, then an inherited module)--> where): profile_edit_test.go "asks a bundle's member question, then the inherited-edit question, before the page"
// also pinned (where --answer (a remote profile on)--> next step): profile_edit_test.go "asks where to write while a remote profile is on before the page, titled with the base"
// also pinned (where --Cancel or ctrl+c--> cancelled; review --apply (-n)--> done): profile_edit_test.go "opens no page and writes nothing when the question is cancelled, and writes nothing under -n"
// also pinned (where --Cancel or ctrl+c--> cancelled): profile_edit_test.go "opens no page and writes nothing when the bundle's member question or the remote profile's question is cancelled"
// also pinned (start --run (a profile)--> modules; modules --answer--> next step): profile_edit_test.go "shows the profile's module page, its modules ticked, a component page per module, and removes what you untick once asked"
// also pinned (start --run (a bundle)--> members; members --answer--> done): profile_edit_test.go "keeps what you untick when you say No, and shows a bundle's members page, the members kept in order and new ones after"
// also pinned (start --run (a name no profile has)--> new profile; members --answer (a new bundle)--> created): profile_edit_test.go "asks what a new profile is for a name no profile has: a bundle of the members picked, or an empty profile when nothing is picked"
// also pinned (members --answer (unchanged)--> done; members --answer (a pulled member list)--> trust; trust --decline or ctrl+c--> cancelled): profile_edit_test.go "never records a pulled member list as trusted from the members page: Enter changes nothing, and a change asks the trust question first, -y or not"
// also pinned (modules --answer (only modules your cache lacks are kept)--> unchanged): profile_edit_test.go "says the profile is unchanged when only modules your cache lacks are kept, hinting the lola cache add that lets you pick them"
// also pinned (modules --answer (what is kept is unchanged, something unticked)--> remove): profile_edit_test.go "removes what you untick when what you keep is unchanged, showing the removal first, and lists it in the review -y applies"
// also pinned (remove --yes (the profile on in two scopes)--> remove there): profile_edit_test.go "removes what you untick in each scope the profile is on, and under -n says it would"
// also pinned (start --pick flags (no module)--> refused; modules --empty submit (-y or --force)--> done): profile_edit_test.go "removes what you untick unasked under -y or --force, and keeps --edit's refusal for picks without a module"
// also pinned (which --esc--> which; where --esc--> where; new profile --esc--> new profile): keys_internal_test.go "profile edit: which --esc--> which"
// also pinned (members --esc (first screen)--> members): keys_internal_test.go "profile edit: members --esc (first screen)--> members"
// also pinned (review --apply (a new profile, nothing ticked)--> created): profile_edit_paths_test.go "profile edit: modules --empty submit (a new profile)--> review"
// also pinned (remove --yes--> done): profile_edit_paths_test.go "profile edit: modules --empty submit (a profile with modules)--> remove"
// also pinned (apply removals --yes--> done): profile_edit_paths_test.go "profile edit: members --answer (removes modules of the bundle on)--> apply removals"

import (
	"fmt"
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

var _ = Describe("profile edit paths", func() {
	var (
		p                       paths.Paths
		tmp, kit, lint          string
		goYml, lintMd, skillA   string
		goBefore, defaultBefore []byte
	)
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		kit, lint = filepath.Join(tmp, "kit"), filepath.Join(tmp, "lint")
		writeTree(kit, map[string]string{"lola.yml": "name: kit\n", "skills/a/SKILL.md": "---\ndescription: A\n---\nA\n",
			"skills/b/SKILL.md": "---\ndescription: B\n---\nB\n"})
		writeTree(lint, map[string]string{"lola.yml": "name: lint\n", "commands/lint.md": "L\n"})
		Expect(lola("cache", "add", kit).code).To(Equal(0))
		Expect(lola("cache", "add", lint).code).To(Equal(0))
		goYml = filepath.Join(p.UserProfiles, "go.yml")
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\n",
			"go.yml": "version: 1\nmodules:\n  - {name: lint, source: " + lint + "}\n"})
		Expect(lola("profile", "go", "--switch", "--user", "-y").code).To(Equal(0))
		lintMd = filepath.Join(p.Home, ".claude", "commands", "lint.md")
		skillA = filepath.Join(p.Home, ".claude", "skills", "a", "SKILL.md")
		Expect(lintMd).To(BeARegularFile())
		goBefore, err = os.ReadFile(goYml)
		Expect(err).NotTo(HaveOccurred())
		defaultBefore, err = os.ReadFile(filepath.Join(p.UserProfiles, "default.yml"))
		Expect(err).NotTo(HaveOccurred())
	})
	// nothingWritten checks that the run left the profiles and the files they install as they were.
	nothingWritten := func() {
		GinkgoHelper()
		Expect(os.ReadFile(goYml)).To(Equal(goBefore))
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "default.yml"))).To(Equal(defaultBefore))
		Expect(lintMd).To(BeARegularFile())
		Expect(skillA).NotTo(BeAnExistingFile())
		Expect(filepath.Join(p.UserProfiles, "try.yml")).NotTo(BeAnExistingFile())
	}

	It("profile edit: review --apply (modules unticked)--> done", func() {
		sp := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep()}, review: []string{cli.ReviewApply}}
		r := lolaP(sp, "profile", "go", "--edit", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"modules", "components:profile go › kit", "review"}), "the review's Apply is the confirmation")
		Expect(sp.reviewSteps[0].Summary).To(ContainSubstring("Remove lint from profile go, as you unticked at user scope:\n"))
		Expect(r.out).To(And(ContainSubstring("✓ installed kit (claude-code)"), ContainSubstring("✓ removed lint from profile go")))
		Expect(os.ReadFile(goYml)).To(And(ContainSubstring("name: kit"), Not(ContainSubstring("lint"))))
		Expect(skillA).To(BeARegularFile())
		Expect(lintMd).NotTo(BeAnExistingFile())
	})

	It("profile edit: modules --empty submit (a new profile)--> review", func() {
		try := filepath.Join(p.UserProfiles, "try.yml")
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
		fakeAssistant(tmp, "gemini", "0.63.0")
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\n"}) // two assistants found, none fixed: nothing to ask them for
		sp := &scripted{where: []int{0}, modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods()}, review: []string{cli.ReviewApply}}
		r := lolaP(sp, "profile", "try", "--edit", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"where", "modules", "review"}), "an empty profile is written after a review, as any write")
		Expect(sp.reviewSteps[0].Summary).To(Equal("→ would make profile try (" + try + ", empty)\n"))
		Expect(sp.reviewSteps[0].Options).To(Equal([]string{cli.ReviewApply, cli.ReviewModules, cli.ReviewQuit}))
		Expect(r.out).To(ContainSubstring("✓ new profile try (" + try + ", empty)\n"))
		Expect(os.ReadFile(try)).To(Equal([]byte("version: 1\n")))
	})

	It("profile edit: review --esc (a new profile, nothing ticked)--> modules", func() {
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
		fakeAssistant(tmp, "gemini", "0.63.0")
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\n"})
		defaultBefore = []byte("version: 1\n")
		sp := &scripted{where: []int{0}, modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods(), mods()},
			review: []string{goBack, cli.ReviewQuit}}
		r := lolaP(sp, "profile", "try", "--edit", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"where", "modules", "review", "modules", "review"}))
		Expect(sp.reviewSteps[0].Nav.Back).To(BeTrue())
		Expect(sp.moduleSteps[1].Title).To(Equal("new profile try › modules"))
		nothingWritten()
	})

	It("profile edit: remove --esc--> modules", func() {
		sp := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods(), mods("lint")}, confirmBacks: 1}
		r := lolaP(sp, "profile", "go", "--edit", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"modules", "confirm:Remove lint from profile go?", "modules"}))
		Expect(sp.confirmNavs[0].Back).To(BeTrue(), "a closing question after the page goes back to it")
		Expect(sp.moduleSteps[1].Preselect).To(BeEmpty(), "the page as answered: nothing ticked")
		Expect(r.out).To(ContainSubstring("already up to date"))
		nothingWritten()
	})

	It("profile edit: apply removals --esc--> members", func() {
		work := filepath.Join(p.UserProfiles, "work.yml")
		writeTree(p.UserProfiles, map[string]string{"work.yml": "version: 1\nbundle: [go]\n", "docs.yml": "version: 1\n"})
		Expect(lola("profile", "work", "--switch", "--user", "-y").code).To(Equal(0))
		sp := &scripted{members: []func(cli.MembersStep) ([]string, error){
			func(cli.MembersStep) ([]string, error) { return []string{"docs"}, nil },
			func(cli.MembersStep) ([]string, error) {
				Expect(os.ReadFile(work)).To(Equal([]byte("version: 1\nbundle: [go]\n")), "nothing written before Esc went back")
				Expect(lintMd).To(BeARegularFile())
				return []string{"go", "docs"}, nil
			}}, confirmBacks: 1}
		r := lolaP(sp, "profile", "work", "--edit", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"members", "confirm:Remove these and apply? No applies nothing", "members"}))
		Expect(sp.confirmNavs[0].Back).To(BeTrue(), "a closing question after the members page goes back to it")
		Expect(sp.memberSteps[1].Preselect).To(Equal([]string{"docs"}), "the page as answered")
		Expect(r.err).To(HavePrefix("user scope: bundle work (go) over default\nremovals:\n"))
		Expect(strings.Count(r.err, "user scope:")).To(Equal(1), "the context line once")
		Expect(os.ReadFile(work)).To(Equal([]byte("version: 1\nbundle: [go, docs]\n")))
		Expect(lintMd).To(BeARegularFile())
	})

	It("profile edit: new profile --Lists modules--> modules", func() {
		try := filepath.Join(p.UserProfiles, "try.yml")
		sp := &scripted{where: []int{0}, modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep("skill/b")}, review: []string{cli.ReviewApply}}
		r := lolaP(sp, "profile", "try", "--edit", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"where", "modules", "components:profile try › kit", "review"}))
		Expect(sp.whereSteps[0]).To(Equal(cli.WhereStep{Title: "New profile try:", Options: []string{"Lists modules", "Bundles profiles"}}))
		Expect(sp.moduleSteps[0]).To(And(HaveField("Title", "new profile try › modules"), HaveField("Preselect", BeEmpty()), HaveField("Nav.Back", true)),
			"Esc goes back to the question")
		Expect(r.err).To(HavePrefix("user scope: profile go over default\n"))
		Expect(strings.Count(r.err, "user scope:")).To(Equal(1), "the context line once, before the question")
		Expect(r.out).To(ContainSubstring("✓ added kit to profile try  (" + try + ", new)"))
		Expect(os.ReadFile(try)).To(ContainSubstring("  - name: kit\n    source: " + kit + "\n    skip: [skill/b]\n"))
		Expect(os.ReadFile(goYml)).To(Equal(goBefore))
	})

	It("profile edit: modules --esc (a new profile)--> new profile", func() {
		try := filepath.Join(p.UserProfiles, "try.yml")
		sp := &scripted{where: []int{0, 1}, modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){goneBack[cli.ModuleStep, cli.ModuleAnswer]},
			members: []func(cli.MembersStep) ([]string, error){pickMembers("go")}}
		r := lolaP(sp, "profile", "try", "--edit", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"where", "modules", "where", "members"}))
		Expect(sp.moduleSteps[0].Nav.Back).To(BeTrue(), "the question before it is a screen to go back to")
		Expect(sp.whereSteps[1].Preselect).To(Equal(0), "the question as answered: Lists modules")
		Expect(strings.Count(r.err, "user scope:")).To(Equal(1), "the context line once")
		Expect(os.ReadFile(try)).To(Equal([]byte("version: 1\nbundle: [go]\n")))
	})

	It("profile edit: members --esc (a new bundle)--> new profile", func() {
		try := filepath.Join(p.UserProfiles, "try.yml")
		sp := &scripted{where: []int{1, 0}, members: []func(cli.MembersStep) ([]string, error){goneBack[cli.MembersStep, []string]},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods()}, review: []string{cli.ReviewApply}}
		r := lolaP(sp, "profile", "try", "--edit", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"where", "members", "where", "modules", "review"}))
		Expect(sp.memberSteps[0].Nav.Back).To(BeTrue(), "the question before it is a screen to go back to")
		Expect(sp.whereSteps[1].Preselect).To(Equal(1), "the question as answered: Bundles profiles")
		Expect(os.ReadFile(try)).To(Equal([]byte("version: 1\n")))
	})

	It("profile edit: review --Change modules (a new profile)--> modules", func() {
		try := filepath.Join(p.UserProfiles, "try.yml")
		sp := &scripted{where: []int{0}, modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods(), mods("lint")},
			review: []string{cli.ReviewModules, cli.ReviewApply}}
		r := lolaP(sp, "profile", "try", "--edit", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"where", "modules", "review", "modules", "review"}))
		Expect(sp.moduleSteps[1]).To(And(HaveField("Title", "new profile try › modules"), HaveField("Preselect", BeEmpty()), HaveField("Nav.Back", true)))
		Expect(sp.reviewSteps[1].Summary).To(ContainSubstring("lint"), "the review of what was picked now")
		Expect(os.ReadFile(try)).To(ContainSubstring("name: lint"))
	})

	It("profile edit: modules --empty submit (a new profile, after Change modules)--> review", func() {
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
		fakeAssistant(tmp, "gemini", "0.63.0")
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\n"})
		defaultBefore = []byte("version: 1\n")
		sp := &scripted{where: []int{0}, modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("lint"), mods(), mods()},
			assistants: []func(cli.AssistantStep) ([]string, error){func(cli.AssistantStep) ([]string, error) { return []string{"gemini-cli"}, nil }},
			review:     []string{cli.ReviewModules, goBack, cli.ReviewQuit}}
		r := lolaP(sp, "profile", "try", "--edit", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"where", "modules", "assistants", "review", "modules", "review", "modules", "review"}))
		Expect(sp.reviewSteps[1].Options).To(Equal([]string{cli.ReviewApply, cli.ReviewModules, cli.ReviewQuit}), "nothing to install: no assistants to change")
		Expect(sp.reviewSteps[1].Summary).To(HaveSuffix(", empty)\n"))
		nothingWritten()
	})

	It("profile edit: modules --empty submit (a new profile, -y)--> created", func() {
		try := filepath.Join(p.UserProfiles, "try.yml")
		sp := &scripted{where: []int{0}, modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods()}}
		r := lolaP(sp, "profile", "try", "--edit", "--user", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"where", "modules"}), "-y answers the review")
		Expect(r.err).To(ContainSubstring("→ would make profile try ("+try+", empty)\n"), "the summary is printed")
		Expect(r.out).To(ContainSubstring("✓ new profile try (" + try + ", empty)\n"))
		Expect(os.ReadFile(try)).To(Equal([]byte("version: 1\n")))
	})

	It("profile edit: review --quit--> cancelled", func() {
		sp := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep()}, review: []string{cli.ReviewQuit}}
		r := lolaP(sp, "profile", "go", "--edit", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"modules", "components:profile go › kit", "review"}))
		nothingWritten()
	})

	// bundleOn turns on bundle work, whose one member go brings lint, beside profile docs, which
	// brings nothing: a members answer without go removes lint.
	bundleOn := func() string {
		GinkgoHelper()
		writeTree(p.UserProfiles, map[string]string{"work.yml": "version: 1\nbundle: [go]\n", "docs.yml": "version: 1\n"})
		Expect(lola("profile", "work", "--switch", "--user", "-y").code).To(Equal(0))
		return filepath.Join(p.UserProfiles, "work.yml")
	}
	// kitProfile adds a profile named kit, as the module kit is.
	kitProfile := func() string {
		GinkgoHelper()
		writeTree(p.UserProfiles, map[string]string{"kit.yml": "version: 1\n"})
		return filepath.Join(p.UserProfiles, "kit.yml")
	}
	kitAndLint := "version: 1\nmodules:\n  - {name: lint, source: %s}\n  - {name: kit, source: %s}\n"

	DescribeTable("Ctrl+C or Cancel at a step cancels the run: nothing is written and it exits 0",
		func(setup func(), sp *scripted, args []string, calls []string) {
			if setup != nil {
				setup()
			}
			r := lolaP(sp, append(args, "--user")...)
			Expect(r.code).To(Equal(0), r.err)
			Expect(sp.calls).To(Equal(calls))
			nothingWritten()
		},
		Entry("profile edit: which --Cancel or ctrl+c--> cancelled", func() { kitProfile() }, &scripted{where: []int{-1}},
			[]string{"profile", "--edit", "kit"}, []string{"where"}),
		Entry("profile edit: new profile --Cancel or ctrl+c--> cancelled", nil, &scripted{where: []int{-1}},
			[]string{"profile", "try", "--edit"}, []string{"where"}),
		Entry("profile edit: modules --ctrl+c--> cancelled", nil,
			&scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){aborted[cli.ModuleStep, cli.ModuleAnswer]}},
			[]string{"profile", "go", "--edit"}, []string{"modules"}),
		Entry("profile edit: components --ctrl+c--> cancelled", nil,
			&scripted{components: []func(cli.ComponentPage) ([]string, error){aborted[cli.ComponentPage, []string]}},
			[]string{"profile", "go", "--edit", "kit"}, []string{"components:profile go › kit"}),
		Entry("profile edit: remove --ctrl+c--> cancelled", nil,
			&scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods()}, confirmCancels: 1},
			[]string{"profile", "go", "--edit"}, []string{"modules", "confirm:Remove lint from profile go?"}),
	)

	DescribeTable("Ctrl+C at a bundle's step cancels the run: the bundle is as it was and it exits 0",
		func(sp *scripted, calls []string) {
			work := bundleOn()
			r := lolaP(sp, "profile", "work", "--edit", "--user")
			Expect(r.code).To(Equal(0), r.err)
			Expect(sp.calls).To(Equal(calls))
			Expect(os.ReadFile(work)).To(Equal([]byte("version: 1\nbundle: [go]\n")))
			Expect(lintMd).To(BeARegularFile())
		},
		Entry("profile edit: members --ctrl+c--> cancelled",
			&scripted{members: []func(cli.MembersStep) ([]string, error){aborted[cli.MembersStep, []string]}}, []string{"members"}),
		Entry("profile edit: apply removals --ctrl+c--> cancelled",
			&scripted{members: []func(cli.MembersStep) ([]string, error){pickMembers("docs")}, confirmCancels: 1},
			[]string{"members", "confirm:Remove these and apply? No applies nothing"}),
		Entry("profile edit: apply removals --no--> cancelled",
			&scripted{members: []func(cli.MembersStep) ([]string, error){pickMembers("docs")}, confirm: []bool{false}},
			[]string{"members", "confirm:Remove these and apply? No applies nothing"}),
	)

	It("profile edit: start --module argument--> components", func() {
		sp := &scripted{components: []func(cli.ComponentPage) ([]string, error){keep("skill/b")}, review: []string{cli.ReviewApply}}
		r := lolaP(sp, "profile", "go", "--edit", "kit", "--user", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"components:profile go › kit", "review"}), "no scope picker, no module picker")
		Expect(sp.reviewSteps[0].Options).To(Equal([]string{cli.ReviewApply, cli.ReviewComponents, cli.ReviewQuit}), "no Change scope, no Change modules")
		Expect(sp.reviewSteps[0].Summary).To(ContainSubstring("  profile go:\n    kit: skip [skill/b]\n"))
		nothingWritten()
	})

	It("profile edit: components --esc (the first page, module arguments)--> components", func() {
		sp := &scripted{components: []func(cli.ComponentPage) ([]string, error){keep()}, review: []string{cli.ReviewQuit}}
		r := lolaP(sp, "profile", "go", "--edit", "kit", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.pages[0].Nav).To(Equal(cli.Nav{}), "the run's first screen: Esc stays, its key help saying q quits")
		nothingWritten()
	})

	It("profile edit: review --apply--> done", func() {
		sp := &scripted{components: []func(cli.ComponentPage) ([]string, error){keep("skill/b")}, review: []string{cli.ReviewApply}}
		r := lolaP(sp, "profile", "go", "--edit", "kit", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(And(ContainSubstring("✓ added kit to profile go  ("+goYml+")\n"), ContainSubstring("✓ installed kit (claude-code)")))
		Expect(os.ReadFile(goYml)).To(ContainSubstring("  - {name: lint, source: " + lint + "}\n  - name: kit\n    source: " + kit + "\n    skip: [skill/b]\n"))
		Expect(skillA).To(BeARegularFile())
		Expect(filepath.Join(p.Home, ".claude", "skills", "b")).NotTo(BeADirectory())
		Expect(lintMd).To(BeARegularFile())
	})

	It("profile edit: start --module argument (a profile and a module)--> which", func() {
		kitProfile()
		sp := &scripted{where: []int{-1}}
		r := lolaP(sp, "profile", "--edit", "kit", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.whereSteps).To(Equal([]cli.WhereStep{{Title: "kit names profile user:kit (" + filepath.Join(p.UserProfiles, "kit.yml") + ") and a module.",
			Options: []string{"Edit profile user:kit", "Edit module kit in profile go"}}}))
		nothingWritten()
	})

	It("profile edit: which --Edit profile (other module arguments)--> next step", func() {
		kitYml := kitProfile()
		sp := &scripted{where: []int{0}}
		r := lolaP(sp, "profile", "--edit", "kit", "lint", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"where"}), "lint has one component and lola chose nothing: no review")
		Expect(os.ReadFile(kitYml)).To(ContainSubstring("name: lint"))
		Expect(os.ReadFile(goYml)).To(Equal(goBefore))
	})

	It("profile edit: which --Edit profile (no other module)--> modules", func() {
		kitProfile()
		sp := &scripted{where: []int{0}, modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods()}}
		r := lolaP(sp, "profile", "--edit", "kit", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"where", "modules"}))
		Expect(sp.moduleSteps[0].Title).To(Equal("profile kit › modules"))
		Expect(r.out).To(Equal("→ profile kit is unchanged\n"))
		nothingWritten()
	})

	It("profile edit: which --Edit module--> next step", func() {
		kitYml := kitProfile()
		sp := &scripted{where: []int{1}, components: []func(cli.ComponentPage) ([]string, error){keep()}, review: []string{cli.ReviewApply}}
		r := lolaP(sp, "profile", "--edit", "kit", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"where", "components:profile go › kit", "review"}))
		Expect(os.ReadFile(goYml)).To(ContainSubstring("name: kit"))
		Expect(os.ReadFile(kitYml)).To(Equal([]byte("version: 1\n")))
	})

	It("profile edit: start --module argument (a name no profile has)--> refused", func() {
		sp := &scripted{}
		r := lolaP(sp, "profile", "nope", "--edit", "kit", "--user")
		Expect(r.code).To(Equal(1), r.err)
		Expect(sp.calls).To(BeEmpty())
		Expect(r.err).To(Equal("user scope: profile go over default\nerror: no profile nope for user scope; --new makes it\n" +
			"  hint: make it: lola profile nope kit --edit --user --new\n"))
		nothingWritten()
	})

	It("profile edit: start --new flag with modules--> components", func() {
		try := filepath.Join(p.UserProfiles, "try.yml")
		sp := &scripted{components: []func(cli.ComponentPage) ([]string, error){keep()}, review: []string{cli.ReviewApply}}
		r := lolaP(sp, "profile", "try", "--edit", "--new", "kit", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"components:profile try › kit", "review"}))
		Expect(r.out).To(ContainSubstring("✓ added kit to profile try  (" + try + ", new)"))
		Expect(os.ReadFile(try)).To(ContainSubstring("name: kit"))
	})

	It("profile edit: start --new flag--> created", func() {
		try := filepath.Join(p.UserProfiles, "try.yml")
		sp := &scripted{}
		r := lolaP(sp, "profile", "try", "--edit", "--new", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(BeEmpty(), "the run names everything: no screen")
		Expect(r.out).To(ContainSubstring("✓ new profile try (" + try + ", empty)\n"))
		Expect(os.ReadFile(try)).To(Equal([]byte("version: 1\n")))
	})

	It("profile edit: start --bundle flag--> apply removals", func() {
		work := bundleOn()
		sp := &scripted{confirm: []bool{true}}
		r := lolaP(sp, "profile", "work", "--edit", "--bundle", "docs", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"confirm:Remove these and apply? No applies nothing"}), "no members page")
		Expect(sp.confirmNavs[0].Back).To(BeFalse(), "nothing before it to go back to")
		Expect(os.ReadFile(work)).To(Equal([]byte("version: 1\nbundle: [docs]\n")))
		Expect(lintMd).NotTo(BeAnExistingFile())
	})

	It("profile edit: members --answer (removes modules of the bundle on)--> apply removals", func() {
		work := bundleOn()
		sp := &scripted{members: []func(cli.MembersStep) ([]string, error){pickMembers("docs")}, confirm: []bool{true}}
		r := lolaP(sp, "profile", "work", "--edit", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"members", "confirm:Remove these and apply? No applies nothing"}))
		Expect(sp.memberSteps[0]).To(And(HaveField("Title", "bundle work › members"), HaveField("Preselect", []string{"go"})))
		Expect(r.err).To(ContainSubstring("removals:\n  ✗ lint: claude-code (whole module)\n"), "the question shows what goes")
		Expect(r.out).To(ContainSubstring("✓ changed bundle work (go → docs)  (" + work + ")\n"))
		Expect(os.ReadFile(work)).To(Equal([]byte("version: 1\nbundle: [docs]\n")))
		Expect(lintMd).NotTo(BeAnExistingFile())
	})

	It("profile edit: new profile --Bundles profiles--> members", func() {
		try := filepath.Join(p.UserProfiles, "try.yml")
		sp := &scripted{where: []int{1}, members: []func(cli.MembersStep) ([]string, error){pickMembers("go")}}
		r := lolaP(sp, "profile", "try", "--edit", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"where", "members"}), "the members page is the answer: no review")
		Expect(sp.memberSteps[0]).To(And(HaveField("Title", "new bundle try › members"), HaveField("Preselect", BeEmpty())))
		Expect(r.out).To(ContainSubstring("✓ new bundle try (go)"))
		Expect(os.ReadFile(try)).To(Equal([]byte("version: 1\nbundle: [go]\n")))
	})

	It("profile edit: modules --esc (first screen)--> modules", func() {
		sp := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("lint")}}
		r := lolaP(sp, "profile", "go", "--edit", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.moduleSteps[0]).To(And(HaveField("Title", "profile go › modules"), HaveField("Nav", cli.Nav{}), HaveField("None", true)),
			"no screen before it: Esc stays, its key help saying q quits")
		Expect(r.out).To(Equal("✓ already up to date\n"))
	})

	It("profile edit: modules --answer (nothing would change)--> up to date", func() {
		sp := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("lint")}}
		r := lolaP(sp, "profile", "go", "--edit", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"modules"}), "no review")
		Expect(r.out).To(Equal("✓ already up to date\n"))
		nothingWritten()
	})

	It("profile edit: modules --empty submit (a profile with modules)--> remove", func() {
		sp := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods()}, confirm: []bool{true}}
		r := lolaP(sp, "profile", "go", "--edit", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"modules", "confirm:Remove lint from profile go?"}), "no review: the question lists the removal")
		Expect(r.err).To(ContainSubstring("Remove lint from profile go, as you unticked at user scope:\n→ would remove lint from profile go  (" + goYml + ")\n"))
		Expect(r.out).To(ContainSubstring("✓ removed lint from profile go  (" + goYml + ")\n"))
		Expect(os.ReadFile(goYml)).To(Equal([]byte("version: 1\n")))
		Expect(lintMd).NotTo(BeAnExistingFile())
	})

	It("profile edit: remove --no--> done", func() {
		sp := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods()}, confirm: []bool{false}}
		r := lolaP(sp, "profile", "go", "--edit", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(Equal("→ kept lint in profile go\n"))
		nothingWritten()
	})

	It("profile edit: remove --esc (a component page was asked)--> components", func() {
		both := fmt.Sprintf(kitAndLint, lint, kit)
		writeTree(p.UserProfiles, map[string]string{"go.yml": both})
		Expect(lola("sync", "--user", "-y").code).To(Equal(0))
		sp := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep(), func(pg cli.ComponentPage) ([]string, error) {
				Expect(os.ReadFile(goYml)).To(Equal([]byte(both)), "nothing written before Esc went back")
				Expect(lintMd).To(BeARegularFile())
				return keep()(pg)
			}}, confirmBacks: 1, confirm: []bool{true}}
		r := lolaP(sp, "profile", "go", "--edit", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"modules", "components:profile go › kit", "confirm:Remove lint from profile go?",
			"components:profile go › kit", "confirm:Remove lint from profile go?"}))
		Expect(sp.confirmNavs[0]).To(Equal(cli.Nav{Back: true, Answered: 2}))
		Expect(ticked(sp.pages[1])).To(Equal([]string{"skill/a", "skill/b"}), "the page as answered")
		Expect(os.ReadFile(goYml)).To(And(ContainSubstring("name: kit"), Not(ContainSubstring("lint"))))
		Expect(lintMd).NotTo(BeAnExistingFile())
	})

	It("profile edit: components --esc (the first page, the module page shown)--> modules", func() {
		sp := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("lint", "kit"), mods("lint", "kit")},
			components: []func(cli.ComponentPage) ([]string, error){goneBack[cli.ComponentPage, []string], keep()}, review: []string{cli.ReviewQuit}}
		r := lolaP(sp, "profile", "go", "--edit", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"modules", "components:profile go › kit", "modules", "components:profile go › kit", "review"}))
		Expect(sp.pages[0].Nav.Back).To(BeTrue())
		Expect(sp.moduleSteps[1].Preselect).To(Equal([]string{"lint", "kit"}), "the page as answered")
		nothingWritten()
	})

	It("profile edit: no components --no (module arguments, other modules left)--> done", func() {
		docs := filepath.Join(tmp, "docs")
		writeTree(docs, map[string]string{"lola.yml": "name: docs\n", "commands/docs.md": "D\n"})
		Expect(lola("cache", "add", docs).code).To(Equal(0))
		sp := &scripted{components: []func(cli.ComponentPage) ([]string, error){pick()}, empty: []bool{false}}
		r := lolaP(sp, "profile", "go", "--edit", "kit", "docs", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"components:profile go › kit", "empty:kit"}),
			"kit dropped, docs has one component and lola chose nothing: no review, so kit cannot be brought back in this run")
		Expect(r.out).To(And(ContainSubstring("✓ added docs to profile go"), Not(ContainSubstring("kit"))))
		Expect(os.ReadFile(goYml)).To(And(ContainSubstring("name: docs"), Not(ContainSubstring("name: kit"))))
	})

	It("profile edit: review --esc--> components", func() {
		sp := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("lint", "kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep("skill/b"), keep()}, review: []string{goBack, cli.ReviewQuit}}
		r := lolaP(sp, "profile", "go", "--edit", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"modules", "components:profile go › kit", "review", "components:profile go › kit", "review"}))
		Expect(ticked(sp.pages[1])).To(Equal([]string{"skill/a"}), "the last page, as answered")
		nothingWritten()
	})

	It("profile edit: review --Change modules--> modules", func() {
		sp := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("kit"), mods("kit", "lint")},
			components: []func(cli.ComponentPage) ([]string, error){keep("skill/b")}, review: []string{cli.ReviewModules, cli.ReviewApply}}
		r := lolaP(sp, "profile", "go", "--edit", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"modules", "components:profile go › kit", "review", "modules", "review"}), "kit's page keeps its answer")
		Expect(sp.reviewSteps[0].Options).To(Equal([]string{cli.ReviewApply, cli.ReviewModules, cli.ReviewComponents, cli.ReviewQuit}), "never Change scope")
		Expect(sp.moduleSteps[1]).To(And(HaveField("Preselect", []string{"kit"}), HaveField("Nav.Back", true)))
		Expect(sp.reviewSteps[1].Summary).NotTo(ContainSubstring("Remove lint"), "lint ticked again: nothing to remove")
		Expect(os.ReadFile(goYml)).To(And(ContainSubstring("name: lint"), ContainSubstring("skip: [skill/b]")))
		Expect(lintMd).To(BeARegularFile())
	})

	It("profile edit: change modules --esc--> review", func() {
		sp := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("kit"), goneBack[cli.ModuleStep, cli.ModuleAnswer]},
			components: []func(cli.ComponentPage) ([]string, error){keep()}, review: []string{cli.ReviewModules, cli.ReviewApply}}
		r := lolaP(sp, "profile", "go", "--edit", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"modules", "components:profile go › kit", "review", "modules", "review"}))
		Expect(sp.reviewSteps[1].Summary).To(Equal(sp.reviewSteps[0].Summary), "every answer as it was")
		Expect(os.ReadFile(goYml)).To(And(ContainSubstring("name: kit"), Not(ContainSubstring("lint"))))
	})

	It("profile edit: change components --esc--> review", func() {
		sp := &scripted{components: []func(cli.ComponentPage) ([]string, error){keep("skill/b"), goneBack[cli.ComponentPage, []string]},
			review: []string{cli.ReviewComponents, cli.ReviewApply}}
		r := lolaP(sp, "profile", "go", "--edit", "kit", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"components:profile go › kit", "review", "components:profile go › kit", "review"}))
		Expect(sp.pages[1].Nav.Back).To(BeTrue(), "reopened from the review, Esc goes back to it")
		Expect(sp.reviewSteps[1].Summary).To(Equal(sp.reviewSteps[0].Summary))
		Expect(os.ReadFile(goYml)).To(ContainSubstring("skip: [skill/b]"))
	})
})

// pickMembers answers a members page with names.
func pickMembers(names ...string) func(cli.MembersStep) ([]string, error) {
	return func(cli.MembersStep) ([]string, error) { return names, nil }
}

var _ = Describe("profile edit paths across two scopes", func() {
	var goYml, goBefore, userLint, projLint string
	BeforeEach(func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		lint, ship, proj := filepath.Join(tmp, "lint"), filepath.Join(tmp, "ship"), filepath.Join(tmp, "proj")
		writeTree(lint, map[string]string{"lola.yml": "name: lint\n", "commands/lint.md": "L\n"})
		writeTree(ship, map[string]string{"lola.yml": "name: ship\n", "commands/ship.md": "S\n"})
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\n"})
		goYml, goBefore = filepath.Join(p.UserProfiles, "go.yml"), "version: 1\nmodules:\n  - {name: lint, source: "+lint+"}\n"
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\n", "go.yml": goBefore})
		Expect(lola("cache", "add", lint).code).To(Equal(0))
		Expect(lola("cache", "add", ship).code).To(Equal(0))
		Expect(lola("sync", "-C", proj, "--trust=entries", "-y").code).To(Equal(0))
		GinkgoT().Chdir(proj)
		Expect(lola("profile", "go", "--switch", "--user", "-y").code).To(Equal(0))
		Expect(lola("profile", "user:go", "--switch", "-y").code).To(Equal(0))
		userLint, projLint = filepath.Join(p.Home, ".claude", "commands", "lint.md"), filepath.Join(proj, ".claude", "commands", "lint.md")
		Expect(userLint).To(BeARegularFile())
		Expect(projLint).To(BeARegularFile())
	})
	neitherWritten := func(r result) {
		GinkgoHelper()
		Expect(r.code).To(Equal(0), r.err)
		Expect(os.ReadFile(goYml)).To(Equal([]byte(goBefore)))
		Expect(projLint).To(BeARegularFile())
		Expect(userLint).To(BeARegularFile())
		Expect(filepath.Join(filepath.Dir(projLint), "ship.md")).NotTo(BeAnExistingFile())
	}

	It("profile edit: remove there --no--> cancelled", func() {
		sp := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods()}, confirm: []bool{true, false}}
		r := lolaP(sp, "profile", "go", "--edit")
		Expect(sp.calls).To(Equal([]string{"modules", "confirm:Remove lint from profile go?", "confirm:Remove these there too? No changes nothing"}))
		Expect(r.err).To(ContainSubstring("removals:\n  ✗ lint: claude-code (whole module)\n"), "the question shows what goes there")
		neitherWritten(r)
	})

	It("profile edit: review --apply (modules unticked, the profile on in two scopes)--> remove there", func() {
		sp := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("ship")}, review: []string{cli.ReviewApply}, confirm: []bool{false}}
		r := lolaP(sp, "profile", "go", "--edit")
		Expect(sp.calls).To(Equal([]string{"modules", "review", "confirm:Remove these there too? No changes nothing"}), "asked before the install writes")
		neitherWritten(r)
	})

	It("profile edit: remove there --ctrl+c--> cancelled", func() {
		sp := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("ship")}, review: []string{cli.ReviewApply}, confirmCancels: 1}
		r := lolaP(sp, "profile", "go", "--edit")
		Expect(sp.calls).To(Equal([]string{"modules", "review", "confirm:Remove these there too? No changes nothing"}))
		neitherWritten(r)
	})

	It("profile edit: remove there --yes--> done", func() {
		sp := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods()}, confirm: []bool{true, true}}
		r := lolaP(sp, "profile", "go", "--edit")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"modules", "confirm:Remove lint from profile go?", "confirm:Remove these there too? No changes nothing"}), "asked once")
		Expect(os.ReadFile(goYml)).To(Equal([]byte("version: 1\n")))
		Expect(projLint).NotTo(BeAnExistingFile())
		Expect(userLint).NotTo(BeAnExistingFile())
	})
})

var _ = Describe("profile edit paths through a pulled bundle", func() {
	It("profile edit: trust --accept--> done", func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj := filepath.Join(tmp, "proj")
		team := filepath.Join(proj, ".lola", "profiles", "team.yml")
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\n", ".lola/profiles/style.yml": "version: 1\n",
			".lola/profiles/deploy.yml": "version: 1\n", ".lola/profiles/team.yml": "version: 1\nbundle: [style]\n"})
		GinkgoT().Chdir(proj)
		Expect(lola("profile", "team", "--switch", "--trust=all", "-y").code).To(Equal(0))
		Expect(os.WriteFile(team, []byte("version: 1\nbundle: [style, deploy]\n"), 0o600)).To(Succeed()) // as a pull would

		sp := &scripted{members: []func(cli.MembersStep) ([]string, error){pickMembers("style")},
			trust: []func(cli.TrustStep) (engine.TrustAnswer, error){func(cli.TrustStep) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }}}
		r := lolaP(sp, "profile", "team", "--edit")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"members", "trust"}))
		Expect(r.err).To(MatchRegexp(`~ bundle +\[style\] → \[style, deploy\]`), "the question shows the pulled change")
		Expect(r.out).To(ContainSubstring("✓ changed bundle team (style, deploy → style)"))
		Expect(os.ReadFile(team)).To(Equal([]byte("version: 1\nbundle: [style]\n")))
		Expect(lola("sync").code).To(Equal(0), "what you answered is trusted")
	})

	It("profile edit: apply removals --esc (after the trust question)--> members", func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj := filepath.Join(tmp, "proj")
		team := filepath.Join(proj, ".lola", "profiles", "team.yml")
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\n", "mods/kit/lola.yml": "name: kit\n",
			"mods/kit/commands/kit.md": "K\n", ".lola/profiles/style.yml": "version: 1\nmodules:\n  - {name: kit, source: ../../mods/kit}\n",
			".lola/profiles/deploy.yml": "version: 1\n", ".lola/profiles/team.yml": "version: 1\nbundle: [style]\n"})
		GinkgoT().Chdir(proj)
		sw := lola("profile", "team", "--switch", "--trust=all", "-y")
		Expect(sw.code).To(Equal(0), sw.err)
		kitMd := filepath.Join(proj, ".claude", "commands", "kit.md")
		Expect(kitMd).To(BeARegularFile())
		Expect(os.WriteFile(team, []byte("version: 1\nbundle: [style, deploy]\n"), 0o600)).To(Succeed()) // as a pull would

		sp := &scripted{members: []func(cli.MembersStep) ([]string, error){pickMembers("deploy"), aborted[cli.MembersStep, []string]},
			trust: []func(cli.TrustStep) (engine.TrustAnswer, error){trustAll}, confirmBacks: 1}
		r := lolaP(sp, "profile", "team", "--edit")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"members", "trust", "confirm:Remove these and apply? No applies nothing", "members"}),
			"Esc at the removals goes back to the members page, as the trust question was the bundle edit's own")
		Expect(r.err).NotTo(ContainSubstring("error: back"))
		Expect(os.ReadFile(team)).To(Equal([]byte("version: 1\nbundle: [style, deploy]\n")))
		Expect(kitMd).To(BeARegularFile())
	})

	It("profile edit: apply removals --esc (bundle flag, after the trust question)--> apply removals", func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj := filepath.Join(tmp, "proj")
		team := filepath.Join(proj, ".lola", "profiles", "team.yml")
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\n", "mods/kit/lola.yml": "name: kit\n",
			"mods/kit/commands/kit.md": "K\n", ".lola/profiles/style.yml": "version: 1\nmodules:\n  - {name: kit, source: ../../mods/kit}\n",
			".lola/profiles/deploy.yml": "version: 1\n", ".lola/profiles/team.yml": "version: 1\nbundle: [style]\n"})
		GinkgoT().Chdir(proj)
		sw := lola("profile", "team", "--switch", "--trust=all", "-y")
		Expect(sw.code).To(Equal(0), sw.err)
		kitMd := filepath.Join(proj, ".claude", "commands", "kit.md")
		Expect(os.WriteFile(team, []byte("version: 1\nbundle: [style, deploy]\n"), 0o600)).To(Succeed()) // as a pull would

		sp := &scripted{trust: []func(cli.TrustStep) (engine.TrustAnswer, error){trustAll}, confirm: []bool{true}}
		r := lolaP(sp, "profile", "team", "--edit", "--bundle", "deploy")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"trust", "confirm:Remove these and apply? No applies nothing"}))
		Expect(sp.confirmNavs[0].Back).To(BeFalse(), "the trust question was the bundle edit's own, which nothing can go back to: Esc stays")
		Expect(os.ReadFile(team)).To(Equal([]byte("version: 1\nbundle: [deploy]\n")))
		Expect(kitMd).NotTo(BeAnExistingFile())
	})
})
