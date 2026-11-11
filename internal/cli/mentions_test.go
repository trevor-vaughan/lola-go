package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/cli"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// mentionsModule is a module whose skills name each other: golang-testing mentions golang-gopls
// and golang-race, golang-race mentions golang-lint.
var mentionsModule = map[string]string{
	"lola.yml":                       "name: pack\n",
	"skills/golang-testing/SKILL.md": "---\ndescription: Testing\n---\nSee golang-gopls and golang-race.\n",
	"skills/golang-gopls/SKILL.md":   "---\ndescription: Gopls\n---\nNothing.\n",
	"skills/golang-race/SKILL.md":    "---\ndescription: Race\n---\nPairs with golang-lint.\n",
	"skills/golang-lint/SKILL.md":    "---\ndescription: Lint\n---\nLint.\n",
	"commands/notes.md":              "Notes.\n",
}

var _ = Describe("lola info lists mentions", func() {
	var tmp, proj, mod string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, mod = filepath.Join(tmp, "proj"), filepath.Join(tmp, "src", "pack")
		writeTree(mod, mentionsModule)
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
		GinkgoT().Chdir(proj)
	})

	It("shows what each component mentions, in human, --plain and JSON output", func() {
		r := lola("info", mod, "--project", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("  mentions:\n    skill/golang-race → skill/golang-lint\n" +
			"    skill/golang-testing → skill/golang-gopls, skill/golang-race\n"))
		Expect(r.out).NotTo(ContainSubstring("you did not pick"), "lola info's plan names no left-out mentions")

		r = lola("info", mod, "--project", "-t", "claude-code", "--plain")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("mention\tskill/golang-testing\tskill/golang-gopls\nmention\tskill/golang-testing\tskill/golang-race\n"))

		r = lola("info", mod, "--project", "-t", "claude-code", "--json")
		Expect(r.code).To(Equal(0), r.err)
		var v struct {
			Mentions []struct {
				Component string   `json:"component"`
				Mentions  []string `json:"mentions"`
			} `json:"mentions"`
		}
		Expect(json.Unmarshal([]byte(r.out), &v)).To(Succeed())
		Expect(v.Mentions).To(HaveLen(2))
		Expect(v.Mentions[0].Component).To(Equal("skill/golang-race"))
		Expect(v.Mentions[0].Mentions).To(Equal([]string{"skill/golang-lint"}))
	})

	It("gives mentions as [] in JSON and prints no mentions block when none", func() {
		plain := filepath.Join(tmp, "src", "plain")
		writeTree(plain, map[string]string{"lola.yml": "name: plain\n", "commands/ship.md": "Ship\n"})
		r := lola("info", plain, "--project", "-t", "claude-code", "--json")
		Expect(r.code).To(Equal(0), r.err)
		var v map[string]json.RawMessage
		Expect(json.Unmarshal([]byte(r.out), &v)).To(Succeed())
		Expect(string(v["mentions"])).To(Equal("[]"))
		Expect(string(v["notices"])).To(Equal("[]"))
		Expect(lola("info", plain, "--project", "-t", "claude-code").out).NotTo(ContainSubstring("mentions:"))
	})
})

var _ = Describe("mentions in lola install", func() {
	var tmp, proj, mod, selections string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, mod = filepath.Join(tmp, "proj"), filepath.Join(tmp, "src", "pack")
		writeTree(mod, mentionsModule)
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
		selections = filepath.Join(tmp, "config", "lola", "selections.yaml")
		GinkgoT().Chdir(proj)
	})
	label := func(pg cli.ComponentPage, id string) string {
		GinkgoHelper()
		for _, g := range pg.Groups {
			for _, o := range g.Options {
				if o.ID == id {
					return o.Label
				}
			}
		}
		Fail("no option " + id)
		return ""
	}

	It("labels the components that mention others on a staged module's page, and names what the picks leave out in the review by the source given, which the user may decline", func() {
		p := &scripted{components: []func(cli.ComponentPage) ([]string, error){pick("skill/golang-testing")}, review: []string{cli.ReviewQuit}}
		r := lolaP(p, "install", mod, "--scope", "project", "-t", "claude-code", "-v")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(ContainSubstring("  → pack: skill/golang-testing mentions 2 components you did not pick: skill/golang-gopls, skill/golang-race" +
			" → add them: lola install --project " + mod + " --with-mentions -t claude-code\n"))

		p = &scripted{components: []func(cli.ComponentPage) ([]string, error){pick("skill/golang-testing")}, review: []string{cli.ReviewApply}}
		r = lolaP(p, "install", mod, "--scope", "project", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(label(p.pages[0], "skill/golang-testing")).To(Equal("golang-testing — Testing (mentions 2)"))
		Expect(label(p.pages[0], "skill/golang-race")).To(Equal("golang-race — Race (mentions 1)"))
		Expect(label(p.pages[0], "skill/golang-gopls")).To(Equal("golang-gopls — Gopls"))
		Expect(r.err).To(ContainSubstring("  → pack: 1 notice (module 1) — lola check --project "+mod+"\n"),
			"detail is one line per module, whose lola check names the source the review may decline")
		c := lola("check", "--project", mod)
		Expect(c.code).To(Equal(0), c.err)
		Expect(c.out).To(ContainSubstring("→ pack: skill/golang-testing mentions 2 components you did not pick: skill/golang-gopls, skill/golang-race" +
			" → add them: lola install --project " + mod + " --with-mentions\n"))
	})

	It("ends each mention notice row of an install into two scopes with the scope whose command it gives", func() {
		p := &scripted{components: []func(cli.ComponentPage) ([]string, error){pick("skill/golang-testing"), pick("skill/golang-testing")}, review: []string{cli.ReviewApply}}
		r := lolaP(p, "install", mod, "--scope", "both", "-t", "claude-code", "--plain")
		Expect(r.code).To(Equal(0), r.err)
		for _, sc := range []string{"project", "user"} {
			Expect(r.out).To(MatchRegexp(`(?m)^notice\t\w+\tpack: skill/golang-testing mentions 2 components you did not pick: .* lola install --` + sc + ` .*\t` + sc + `$`))
		}
	})

	It("adds what the page's picks mention after the page with --with-mentions, and keeps the flag in the equivalent command", func() {
		p := &scripted{components: []func(cli.ComponentPage) ([]string, error){pick("skill/golang-race")}, review: []string{cli.ReviewApply}}
		r := lolaP(p, "install", mod, "--scope", "project", "-t", "claude-code", "--with-mentions")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(ContainElement("components:project.yml › pack"), "the page still shows")
		Expect(r.err).To(ContainSubstring("pack: + skill/golang-lint, + skill/golang-race"))
		Expect(r.err).To(ContainSubstring("  → pack: + skill/golang-lint (mentioned by skill/golang-race)\n"))
		Expect(equivalents(r.err)).To(Equal([]string{"lola install " + mod + " --scope project -t claude-code --skip pack:skill/golang-gopls" +
			" --skip pack:skill/golang-lint --skip pack:skill/golang-testing --skip pack:command/notes --with-mentions"}))
		Expect(filepath.Join(proj, ".claude/skills/golang-lint/SKILL.md")).To(BeARegularFile())
		Expect(filepath.Join(proj, ".claude/skills/golang-testing")).NotTo(BeAnExistingFile())
	})

	It("off a terminal adds one hop, and the hint it prints adds the next", func() {
		r := lola("install", mod, "--project", "-t", "claude-code", "--only", "skill/golang-testing", "--with-mentions", "-v")
		Expect(r.code).To(Equal(0), r.err)
		out := r.out + r.err
		Expect(out).To(ContainSubstring("→ pack: + skill/golang-gopls (mentioned by skill/golang-testing)\n"))
		Expect(out).To(ContainSubstring("→ pack: + skill/golang-race (mentioned by skill/golang-testing)\n"))
		Expect(out).To(ContainSubstring("→ pack: skill/golang-race mentions 1 component you did not pick: skill/golang-lint" +
			" → add them: lola install --project pack --with-mentions -t claude-code\n"))
		Expect(out).To(ContainSubstring("→ saved to " + selections))
		Expect(filepath.Join(proj, ".claude/skills/golang-lint")).NotTo(BeAnExistingFile())

		r = lola("install", "--project", "pack", "--with-mentions", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out + r.err).To(ContainSubstring("→ pack: + skill/golang-lint (mentioned by skill/golang-race)\n"))
		Expect(filepath.Join(proj, ".claude/skills/golang-lint/SKILL.md")).To(BeARegularFile())
	})

	It("counts what the picks leave out in the module's line, whose lola check names it with the install that adds it", func() {
		r := lola("install", mod, "--project", "-t", "claude-code", "--only", "skill/golang-testing", "--with-mentions")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("→ pack: 1 notice (module 1) — lola check --project pack\n"))
		Expect(r.out).NotTo(ContainSubstring("you did not pick"))

		c := lola("check", "--project", "pack")
		Expect(c.code).To(Equal(0), c.err)
		add := "lola install --project pack --with-mentions"
		Expect(c.out).To(ContainSubstring("→ pack: skill/golang-race mentions 1 component you did not pick: skill/golang-lint → add them: " + add + "\n"))
		c = lola("check", "--project", "pack", "--json")
		Expect(c.code).To(Equal(0), c.err)
		var doc struct {
			Modules []struct {
				Notices []map[string]any `json:"notices"`
			} `json:"modules"`
		}
		Expect(json.Unmarshal([]byte(c.out), &doc)).To(Succeed(), c.out)
		Expect(doc.Modules[0].Notices).To(ConsistOf(HaveKeyWithValue("class", "detail")))

		Expect(runHint(add).code).To(Equal(0))
		Expect(filepath.Join(proj, ".claude/skills/golang-lint/SKILL.md")).To(BeARegularFile())
		c = lola("check", "--project", "pack", "--json")
		Expect(json.Unmarshal([]byte(c.out), &doc)).To(Succeed(), c.out)
		Expect(doc.Modules[0].Notices).To(BeEmpty(), "[] once nothing is left out")
		Expect(c.out).To(ContainSubstring(`"notices": []`))
	})

	It("names the additions in JSON notices under -n and writes nothing", func() {
		r := lola("install", mod, "--project", "-t", "claude-code", "--only", "skill/golang-race", "--with-mentions", "--json", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(withoutContext(r.out)).NotTo(ContainSubstring("null"))
		var v struct {
			Notices []struct{ Level, Text string } `json:"notices"`
		}
		Expect(json.Unmarshal([]byte(r.out), &v)).To(Succeed())
		Expect(v.Notices).To(ContainElement(SatisfyAll(HaveField("Level", "info"), HaveField("Text", "pack: + skill/golang-lint (mentioned by skill/golang-race)"))))
		Expect(selections).NotTo(BeAnExistingFile())
	})

	It("says nothing the picks mention is left out when every mention is installed", func() {
		r := lola("install", mod, "--project", "-t", "claude-code", "--with-mentions")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out + r.err).To(ContainSubstring("→ pack: nothing the picked components mention is left out\n"))
		Expect(selections).NotTo(BeAnExistingFile())
	})
})
