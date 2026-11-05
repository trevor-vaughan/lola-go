package module_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("Templates at load", func() {
	var root string
	BeforeEach(func() { root = filepath.Join(testenv.Isolate(), "mod") })
	load := func(files map[string]string) (*module.Module, error) {
		write(root, files)
		return module.Load(root, "kit", targets.IDs())
	}

	It("reads every .md.liquid component as a template with no frontmatter, its source in Body", func() {
		m, err := load(map[string]string{
			"skills/review/SKILL.md.liquid":         "---\ndescription: {{ target | json }}\n---\nReview.\n",
			"skills/review/ref/checks.md.liquid":    "{{ target }}\n",
			"skills/review/ref/plain.md":            "plain\n",
			"commands/ship.md.liquid":               "Ship for {{ target }}\n",
			"commands/ship/notes.md.liquid":         "notes {{ scope }}\n",
			"commands/ship/plain.md":                "plain\n",
			"agents/critic.md.liquid":               "Criticize.\n",
			"rules/style.md.liquid":                 "Style.\n",
			"SKILL.md.liquid":                       "Root.\n",
			"notes.md.liquid":                       "a support file of the root skill\n",
			"skills/review/ref/deep/more.md.liquid": "deep\n",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Skills).To(HaveLen(2))
		root, review := m.Skills[0], m.Skills[1]
		Expect(review.Doc).To(Equal(module.Doc{Body: "---\ndescription: {{ target | json }}\n---\nReview.\n",
			Template: &module.TemplateSource{File: "skills/review/SKILL.md.liquid"}}))
		Expect(review.Files).To(Equal([]module.SkillFile{
			{Rel: "ref/checks.md", Source: "ref/checks.md.liquid", Body: "{{ target }}\n"},
			{Rel: "ref/deep/more.md", Source: "ref/deep/more.md.liquid", Body: "deep\n"},
		}))
		Expect(root.Name).To(Equal("kit"))
		Expect(root.Doc.Template).To(Equal(&module.TemplateSource{File: "SKILL.md.liquid"}))
		Expect(root.Exclude).To(ContainElement("SKILL.md.liquid"))
		Expect(root.Files).To(Equal([]module.SkillFile{{Rel: "notes.md", Source: "notes.md.liquid", Body: "a support file of the root skill\n"}}))
		Expect(m.Commands).To(HaveLen(1))
		Expect(m.Commands[0].File).To(Equal("commands/ship.md.liquid"))
		Expect(m.Commands[0].Doc.Template).To(Equal(&module.TemplateSource{File: "commands/ship.md.liquid"}))
		Expect(m.Commands[0].Sidecars).To(Equal([]module.Sidecar{
			{File: "commands/ship/notes.md", Source: "commands/ship/notes.md.liquid",
				Doc: module.Doc{Body: "notes {{ scope }}\n", Template: &module.TemplateSource{File: "commands/ship/notes.md.liquid"}}},
			{File: "commands/ship/plain.md", Doc: module.Doc{Body: "plain\n"}},
		}))
		Expect(m.Agents[0].Name).To(Equal("critic"))
		Expect(m.Agents[0].Doc.Frontmatter).To(BeNil())
		Expect(m.Agents[0].Doc.Template).To(Equal(&module.TemplateSource{File: "agents/critic.md.liquid"}))
		Expect(m.Rules).To(Equal([]module.Rule{{Name: "style", Body: "Style.\n", Template: &module.TemplateSource{File: "rules/style.md.liquid"}}}))
		Expect(m.Warnings).To(BeEmpty())
	})

	It("accepts .md.liquid paths in components entries wherever they accept .md", func() {
		m, err := load(map[string]string{
			"lola.yml": "name: kit\ncomponents:\n  skills: [{path: s/review}]\n  commands: [{path: c/ship.md.liquid}]\n" +
				"  agents: [{path: a/critic.md.liquid}]\n  rules: [{path: r/style.md.liquid}]\n",
			"s/review/SKILL.md.liquid": "Review.\n",
			"c/ship.md.liquid":         "Ship.\n",
			"a/critic.md.liquid":       "Criticize.\n",
			"r/style.md.liquid":        "Style.\n",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Skills[0].Doc.Template).To(Equal(&module.TemplateSource{File: "s/review/SKILL.md.liquid"}))
		Expect(m.Commands[0].Name).To(Equal("ship"))
		Expect(m.Commands[0].Doc.Template).NotTo(BeNil())
		Expect(m.Agents[0].Name).To(Equal("critic"))
		Expect(m.Rules[0].Name).To(Equal("style"))
		Expect(m.Rules[0].Template).To(Equal(&module.TemplateSource{File: "r/style.md.liquid"}))
	})

	It("a components entry that sets frontmatter on a template refuses at load", func() {
		_, err := load(map[string]string{
			"lola.yml":         "name: kit\ncomponents:\n  commands: [{path: c/ship.md.liquid, frontmatter: {model: x}}]\n",
			"c/ship.md.liquid": "Ship.\n",
		})
		Expect(err).To(MatchError("components.commands[0]: frontmatter: cannot be set on a template; write the keys in c/ship.md.liquid"))
	})

	Describe("the claude-plugin and agent-plugins formats read templates as the native layout does", func() {
		It("for claude-plugin", func() {
			m, err := func() (*module.Module, error) {
				write(root, map[string]string{
					".claude-plugin/plugin.json":    `{"name":"kit"}`,
					"skills/review/SKILL.md.liquid": "Review.\n",
					"commands/ship.md.liquid":       "Ship.\n",
					"agents/critic.md.liquid":       "Criticize.\n",
				})
				return module.Detect(root, "fallback", targets.IDs())
			}()
			Expect(err).NotTo(HaveOccurred())
			Expect(m.Format).To(Equal("claude-plugin"))
			Expect(m.Skills).To(HaveLen(1))
			Expect(m.Skills[0].Doc.Template).To(Equal(&module.TemplateSource{File: "skills/review/SKILL.md.liquid"}))
			Expect(m.Commands).To(HaveLen(1))
			Expect(m.Commands[0].Name).To(Equal("ship"))
			Expect(m.Commands[0].Doc.Template).NotTo(BeNil())
			Expect(m.Agents).To(HaveLen(1))
			Expect(m.Agents[0].Name).To(Equal("critic"))
			Expect(m.Agents[0].Doc.Template).NotTo(BeNil())
		})

		It("for agent-plugins", func() {
			write(root, map[string]string{
				"plugin.json": `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"dev.kit"}`,
				// a template's name and description are checked after rendering, never at load
				"skills/review/SKILL.md.liquid":       "---\nname: {{ 'review' | json }}\n---\nReview.\n",
				"dev.getlola/commands/ship.md.liquid": "Ship.\n",
				"dev.getlola/agents/critic.md.liquid": "Criticize.\n",
			})
			m, err := module.Detect(root, "fallback", targets.IDs())
			Expect(err).NotTo(HaveOccurred())
			Expect(m.Format).To(Equal("agent-plugins"))
			Expect(m.Warnings).To(BeEmpty())
			Expect(m.Skills).To(HaveLen(1))
			Expect(m.Skills[0].Doc.Template).To(Equal(&module.TemplateSource{File: "skills/review/SKILL.md.liquid"}))
			Expect(m.Commands).To(HaveLen(1))
			Expect(m.Commands[0].Name).To(Equal("ship"))
			Expect(m.Commands[0].Doc.Template).NotTo(BeNil())
			Expect(m.Agents).To(HaveLen(1))
			Expect(m.Agents[0].Doc.Template).NotTo(BeNil())

			// once rendered, the skill must pass the agentskills.io rules its literal form loads under
			for front, why := range map[string]string{
				"description: d\n":                  "name must be 1-64 lowercase letters, digits and single hyphens",
				"name: Review\ndescription: d\n":    "name must be 1-64 lowercase letters, digits and single hyphens",
				"name: other\ndescription: d\n":     `name "other" differs from its directory`,
				"name: review\n":                    "description must be 1-1024 characters",
				"name: review\ndescription: \"\"\n": "description must be 1-1024 characters",
				"name: review\ndescription: d\ncompatibility: " + strings.Repeat("c", 501) + "\n": "compatibility must be at most 500 characters",
			} {
				doc, err := module.RenderedDoc([]byte(front), true, []byte("Review.\n"))
				Expect(err).NotTo(HaveOccurred())
				rendered := *m
				rendered.Skills = []module.Skill{m.Skills[0]}
				rendered.Skills[0].Doc = doc
				Expect(module.Recheck(&rendered)).To(MatchError("skill review: SKILL.md is not a valid Agent Skill: "+why), front)
			}
			doc, err := module.RenderedDoc([]byte("name: review\ndescription: d\n"), true, []byte("Review.\n"))
			Expect(err).NotTo(HaveOccurred())
			rendered := *m
			rendered.Skills = []module.Skill{m.Skills[0]}
			rendered.Skills[0].Doc = doc
			Expect(module.Recheck(&rendered)).To(Succeed())
		})

		It("warn on a suffix variant and refuse a directory named like a template, as the native layout does", func() {
			for _, f := range []struct{ manifest, cmds string }{
				{".claude-plugin/plugin.json", "commands"},
				{"plugin.json", "dev.getlola/commands"},
			} {
				Expect(os.RemoveAll(root)).To(Succeed())
				manifest := `{"name":"kit"}`
				if f.manifest == "plugin.json" {
					manifest = `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"dev.kit"}`
				}
				write(root, map[string]string{f.manifest: manifest, f.cmds + "/ok.md": "ok\n", f.cmds + "/x.MD.liquid": "x\n"})
				m, err := module.Detect(root, "fallback", targets.IDs())
				Expect(err).NotTo(HaveOccurred(), f.manifest)
				Expect(m.Commands).To(HaveLen(1), f.manifest)
				Expect(m.Warnings).To(ConsistOf(f.cmds+"/x.MD.liquid is not a template: the suffix is .md.liquid"), f.manifest)

				Expect(os.MkdirAll(filepath.Join(root, f.cmds, "d.md.liquid"), 0o755)).To(Succeed())
				write(root, map[string]string{f.cmds + "/d.md.liquid/inner.md": "inner\n"})
				_, err = module.Detect(root, "fallback", targets.IDs())
				Expect(err).To(MatchError(f.cmds+"/d.md.liquid: a template must be a regular file"), f.manifest)
			}
		})

		It("name the file that is not a regular file when SKILL.md.liquid is a directory beside SKILL.md", func() {
			write(root, map[string]string{
				"plugin.json":                  `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"dev.kit"}`,
				"skills/review/SKILL.md":       "---\nname: review\ndescription: d\n---\nReview.\n",
				"dev.getlola/commands/ship.md": "Ship.\n",
			})
			Expect(os.MkdirAll(filepath.Join(root, "skills", "review", "SKILL.md.liquid"), 0o755)).To(Succeed())
			m, err := module.Detect(root, "fallback", targets.IDs())
			Expect(err).NotTo(HaveOccurred())
			Expect(m.Warnings).To(ConsistOf("skills/review/SKILL.md.liquid is not a regular file; skipped"))
		})
	})

	DescribeTable("a literal file and its template refuse side by side, case folded",
		func(files map[string]string, msg string) {
			_, err := load(files)
			Expect(err).To(MatchError(msg))
		},
		Entry("SKILL.md", map[string]string{"skills/review/SKILL.md": "l\n", "skills/review/SKILL.md.liquid": "t\n"},
			"skills/review: both SKILL.md and SKILL.md.liquid exist; keep one"),
		Entry("a support file", map[string]string{"skills/review/SKILL.md": "l\n", "skills/review/ref.md": "l\n", "skills/review/REF.md.liquid": "t\n"},
			"skills/review/REF: both ref.md and REF.md.liquid exist; keep one"),
		Entry("a command", map[string]string{"commands/ship.MD": "l\n", "commands/ship.md.liquid": "t\n"},
			"commands/ship: both ship.MD and ship.md.liquid exist; keep one"),
		Entry("a command sidecar", map[string]string{"commands/ship.md": "l\n", "commands/ship/Notes.md": "l\n", "commands/ship/notes.md.liquid": "t\n"},
			"commands/ship/notes: both Notes.md and notes.md.liquid exist; keep one"),
		Entry("an agent", map[string]string{"agents/critic.md": "l\n", "agents/critic.md.liquid": "t\n"},
			"agents/critic: both critic.md and critic.md.liquid exist; keep one"),
		Entry("a rule", map[string]string{"rules/style.MD": "l\n", "rules/style.md.liquid": "t\n"},
			"rules/style: both style.MD and style.md.liquid exist; keep one"),
	)

	It("a skill whose SKILL.md is not a regular file refuses", func() {
		for _, name := range []string{"SKILL.md", "SKILL.md.liquid"} {
			Expect(os.RemoveAll(root)).To(Succeed())
			Expect(os.MkdirAll(filepath.Join(root, "skills", "review", name), 0o755)).To(Succeed())
			_, err := load(map[string]string{"commands/c.md": "c\n"})
			Expect(err).To(MatchError("skills/review has no regular SKILL.md"), name)
		}
		Expect(os.RemoveAll(root)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(root, "SKILL.md"), 0o755)).To(Succeed())
		_, err := load(map[string]string{"commands/c.md": "c\n"})
		Expect(err).To(MatchError("the module root has no regular SKILL.md"))
	})

	It("a suffix that is .md.liquid only under case folding warns and is not a template", func() {
		m, err := load(map[string]string{
			"commands/x.MD.liquid":          "x\n",
			"agents/y.md.LIQUID":            "y\n",
			"rules/z.Md.Liquid":             "z\n",
			"skills/review/SKILL.md":        "Review.\n",
			"skills/review/ref/w.MD.liquid": "w\n",
			"commands/ok.md":                "ok\n",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Warnings).To(ConsistOf(
			"commands/x.MD.liquid is not a template: the suffix is .md.liquid",
			"agents/y.md.LIQUID is not a template: the suffix is .md.liquid",
			"rules/z.Md.Liquid is not a template: the suffix is .md.liquid",
			"skills/review/ref/w.MD.liquid is not a template: the suffix is .md.liquid",
		))
		Expect(m.Commands).To(HaveLen(1))
		Expect(m.Agents).To(BeEmpty())
		Expect(m.Rules).To(BeEmpty())
		Expect(m.Skills[0].Files).To(BeEmpty())
		Expect(m.Skills[0].Exclude).NotTo(ContainElement("ref/w.MD.liquid"), "it is copied as today")
	})

	It("a .liquid file that is not Markdown is copied as is, and mcps.json.liquid warns", func() {
		m, err := load(map[string]string{
			"skills/review/SKILL.md":      "Review.\n",
			"skills/review/foo.liquid":    "{{ x }}",
			"skills/review/x.json.liquid": "{{ x }}",
			"mcps.json.liquid":            "{}",
			"lola.yml.liquid":             "name: x",
			".mcp.json.liquid":            "{}",
			"commands/ship.md":            "Ship.\n",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Skills[0].Files).To(BeEmpty())
		Expect(m.Skills[0].Exclude).To(BeEmpty())
		Expect(m.Warnings).To(ConsistOf(
			".mcp.json.liquid is not read: only Markdown components are templates",
			"lola.yml.liquid is not read: only Markdown components are templates",
			"mcps.json.liquid is not read: only Markdown components are templates",
		))
	})

	It("more than 256 templates refuse the module; a template over 256 KiB refuses before it is read whole", func() {
		files := map[string]string{}
		for i := range 256 {
			files[fmt.Sprintf("commands/c%03d.md.liquid", i)] = "x\n"
		}
		m, err := load(files)
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Commands).To(HaveLen(256))
		write(root, map[string]string{"agents/one.md.liquid": "x\n"})
		_, err = module.Load(root, "kit", targets.IDs())
		Expect(err).To(MatchError("257 template files; a module has at most 256"))

		Expect(os.RemoveAll(root)).To(Succeed())
		write(root, map[string]string{"agents/big.md.liquid": strings.Repeat("a", 256<<10)})
		_, err = module.Load(root, "kit", targets.IDs())
		Expect(err).NotTo(HaveOccurred(), "exactly 256 KiB loads")
		huge := filepath.Join(root, "agents", "huge.md.liquid")
		f, err := os.Create(huge)
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Truncate(1<<40)).To(Succeed(), "a sparse file of 1 TiB: reading it whole would never finish")
		Expect(f.Close()).To(Succeed())
		_, err = module.Load(root, "kit", targets.IDs())
		Expect(err).To(MatchError("agents/huge.md.liquid: is 1099511627776 bytes; a template file is at most 256 KiB"))

		Expect(os.Remove(huge)).To(Succeed())
		write(root, map[string]string{"agents/bad.md.liquid": "ok\nok\n\xff\n"})
		_, err = module.Load(root, "kit", targets.IDs())
		Expect(err).To(MatchError("agents/bad.md.liquid:3: is not UTF-8; a template file must be UTF-8 text"))
	})

	It("lists a case variant of SKILL.md.liquid in the skill's Exclude and warns", func() {
		m, err := load(map[string]string{
			"skills/review/SKILL.md":        "Review.\n",
			"skills/review/Skill.md.liquid": "not read\n",
			"SKILL.md":                      "Root.\n",
			"skill.MD.liquid":               "not read\n",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Skills[1].Exclude).To(ConsistOf("Skill.md.liquid"))
		Expect(m.Skills[1].Files).To(BeEmpty())
		Expect(m.Skills[0].Exclude).To(ContainElement("skill.MD.liquid"))
		Expect(m.Skills[0].Files).To(BeEmpty())
		Expect(m.Warnings).To(ConsistOf(
			"skills/review/Skill.md.liquid is not read; name it SKILL.md.liquid",
			"skill.MD.liquid is not read; name it SKILL.md.liquid",
		))
	})

	It("a directory named SKILL.md.liquid beside SKILL.md refuses with both names", func() {
		write(root, map[string]string{"skills/review/SKILL.md": "Review.\n"})
		Expect(os.MkdirAll(filepath.Join(root, "skills", "review", "SKILL.md.liquid"), 0o755)).To(Succeed())
		_, err := module.Load(root, "kit", targets.IDs())
		Expect(err).To(MatchError("skills/review: both SKILL.md and SKILL.md.liquid exist; keep one"))
	})

	It("a reserved root name's template is not read and warns", func() {
		m, err := load(map[string]string{
			"SKILL.md":               "Root.\n",
			"INSTRUCTIONS.md":        "Legacy.\n",
			"INSTRUCTIONS.md.liquid": "{{ target }}\n",
			"AGENTS.md.liquid":       "{{ target }}\n",
		})
		Expect(err).NotTo(HaveOccurred(), "never the side-by-side refusal")
		Expect(m.Skills).To(HaveLen(1))
		Expect(m.Skills[0].Files).To(BeEmpty(), "never a support file of the root skill")
		Expect(m.Skills[0].Exclude).To(ContainElements("INSTRUCTIONS.md.liquid", "AGENTS.md.liquid"), "never copied with it")
		Expect(m.Warnings).To(ContainElements(
			"INSTRUCTIONS.md.liquid is not read: only Markdown components are templates",
			"AGENTS.md.liquid is not read: only Markdown components are templates",
		))
		Expect(m.Warnings).NotTo(ContainElement(ContainSubstring("keep one")))

		Expect(os.RemoveAll(root)).To(Succeed())
		m, err = load(map[string]string{"commands/c.md": "c\n", "AGENTS.md.liquid": "{{ target }}\n"})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Warnings).To(ConsistOf("AGENTS.md.liquid is not read: only Markdown components are templates"))
	})

	It("a case variant of SKILL.md is not a skill and warns on every OS", func() {
		m, err := load(map[string]string{
			"skills/review/skill.md": "Review.\n",
			"Skill.md":               "Root.\n",
			"commands/c.md":          "c\n",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Skills).To(BeEmpty())
		Expect(m.Warnings).To(ConsistOf(
			"skills/review/skill.md is not read; name it SKILL.md",
			"skills/review has no SKILL.md; skipped",
			"Skill.md is not read; name it SKILL.md",
		))

		Expect(os.RemoveAll(root)).To(Succeed())
		_, err = load(map[string]string{"lola.yml": "name: kit\ncomponents:\n  skills: [{path: s}]\n", "s/skill.md": "S.\n"})
		Expect(err).To(MatchError("components.skills[0]: s has no SKILL.md"))

		Expect(os.RemoveAll(root)).To(Succeed())
		write(root, map[string]string{".claude-plugin/plugin.json": `{"name":"kit"}`, "skills/review/skill.md": "Review.\n", "commands/c.md": "c\n"})
		m, err = module.Detect(root, "fallback", targets.IDs())
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Skills).To(BeEmpty(), "claude-plugin")
		Expect(m.Warnings).To(ConsistOf("skills/review/skill.md is not read; name it SKILL.md", "skills/review has no SKILL.md; skipped"))

		Expect(os.RemoveAll(root)).To(Succeed())
		write(root, map[string]string{"plugin.json": `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"dev.kit"}`,
			"skills/review/skill.md": "---\nname: review\ndescription: d\n---\n", "dev.getlola/commands/c.md": "c\n"})
		m, err = module.Detect(root, "fallback", targets.IDs())
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Skills).To(BeEmpty(), "agent-plugins")
		Expect(m.Warnings).To(ConsistOf("skills/review/skill.md is not read; name it SKILL.md", "skills/review has no SKILL.md; skipped"))

		Expect(os.RemoveAll(root)).To(Succeed())
		write(root, map[string]string{"skills/both/SKILL.md": "Both.\n", "skills/both/skill.MD": "not read\n"})
		if es, err := os.ReadDir(filepath.Join(root, "skills", "both")); err != nil || len(es) != 2 {
			Skip("the file system folds case: SKILL.md and skill.MD are one file")
		}
		m, err = module.Load(root, "kit", targets.IDs())
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Skills).To(HaveLen(1))
		Expect(m.Skills[0].Name).To(Equal("both"))
		Expect(m.Skills[0].Exclude).To(ConsistOf("skill.MD"))
		Expect(m.Warnings).To(ConsistOf("skills/both/skill.MD is not read; name it SKILL.md"))
	})

	It("loader errors name the path cleaned of control characters", func() {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			Skip("chmod does not keep this user from reading a directory")
		}
		for dir, msg := range map[string]string{
			"skills/s\x1b[2J":     "skills/s?[2J: permission denied",
			"skills/s/ref\x1b[2J": "skills/s/ref?[2J: permission denied",
		} {
			Expect(os.RemoveAll(root)).To(Succeed())
			write(root, map[string]string{"skills/s/SKILL.md": "S.\n", "commands/c.md": "c\n"})
			full := filepath.Join(root, dir)
			Expect(os.MkdirAll(full, 0o755)).To(Succeed())
			Expect(os.Chmod(full, 0)).To(Succeed())
			_, err := module.Load(root, "kit", targets.IDs())
			Expect(os.Chmod(full, 0o755)).To(Succeed())
			Expect(err).To(HaveOccurred(), dir)
			Expect(err.Error()).NotTo(ContainSubstring("\x1b"), dir)
			Expect(err).To(MatchError(msg), dir)
			if dir == "skills/s\x1b[2J" {
				Expect(errs.Hints(err)).To(Equal([]string{"check that the path can be read"}), "an unreadable skill directory")
			}
		}
	})
})
