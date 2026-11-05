package module_test

import (
	"errors"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// write creates files under root from a path → content map; files ending in .sh are
// executable.
func write(root string, files map[string]string) {
	for p, c := range files {
		full := filepath.Join(root, p)
		mode := os.FileMode(0o644)
		if filepath.Ext(p) == ".sh" {
			mode = 0o755
		}
		Expect(os.MkdirAll(filepath.Dir(full), 0o755)).To(Succeed())
		Expect(os.WriteFile(full, []byte(c), mode)).To(Succeed())
	}
}

var _ = Describe("Load", func() {
	var root string
	BeforeEach(func() { root = filepath.Join(testenv.Isolate(), "mod") })

	It("loads a full native layout, sorted, under the fallback name", func() {
		write(root, map[string]string{
			"skills/zeta/SKILL.md":       "---\ndescription: z\n---\nZ\n",
			"skills/alpha/SKILL.md":      "---\ndescription: a\n---\nA\n",
			"skills/alpha/ref/notes.txt": "n",
			"commands/go.md":             "Run $ARGUMENTS\n",
			"commands/go/helper.sh":      "#!/bin/sh\n",
			"agents/rev.md":              "---\ndescription: reviews\nmodel: opus\n---\nYou review.\n",
			"mcps.json":                  `{"mcpServers":{"gh":{"command":"npx","args":["-y","gh"],"env":{"T":"1"}},"web":{"type":"sse","url":"https://x/sse"},"api":{"url":"https://x/mcp"}}}`,
			"rules/kind.md":              "---\ndescription: d\n---\nAlways be kind.\n",
		})
		m, err := module.Load(root, "example")
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Name).To(Equal("example"))
		Expect(m.Format).To(Equal("native"))
		Expect([]string{m.Skills[0].Name, m.Skills[1].Name}).To(Equal([]string{"alpha", "zeta"}))
		Expect(m.Skills[0].Dir).To(Equal("skills/alpha"))
		Expect(m.Skills[0].Doc.String("description")).To(Equal("a"))
		Expect(m.Commands).To(HaveLen(1))
		Expect(m.Commands[0]).To(MatchFields(IgnoreExtras, Fields{"Name": Equal("go"), "File": Equal("commands/go.md"), "SidecarDir": Equal("commands/go")}))
		Expect(m.Commands[0].Doc.Frontmatter).To(BeNil())
		Expect(m.Agents[0].Doc.String("model")).To(Equal("opus"))
		Expect(m.MCPServers).To(HaveLen(3))
		Expect(m.MCPServers[0]).To(MatchFields(IgnoreExtras, Fields{"Name": Equal("api"), "Transport": Equal("http"), "URL": Equal("https://x/mcp")}))
		Expect(m.MCPServers[1]).To(MatchFields(IgnoreExtras, Fields{"Name": Equal("gh"), "Transport": Equal("stdio"), "Command": Equal("npx")}))
		Expect(m.MCPServers[1].Args).To(Equal([]string{"-y", "gh"}))
		Expect(m.MCPServers[1].Env).To(Equal(map[string]string{"T": "1"}))
		Expect(m.MCPServers[2].Transport).To(Equal("sse"))
		Expect(m.Rules).To(Equal([]module.Rule{{Name: "kind", Body: "Always be kind.\n"}}))
		Expect(m.Warnings).To(BeEmpty())
	})

	It("reads lola.yml metadata, declared hooks and targets", func() {
		write(root, map[string]string{
			"lola.yml":         "name: example\ndescription: d\nversion: 1.2.0\nauthor: {name: A, email: a@x}\nlicense: MIT\nkeywords: [git]\ntargets: [claude-code]\nhooks: {pre-install: scripts/setup.sh, post-install: scripts/post.sh}\n",
			"commands/a.md":    "x",
			"scripts/setup.sh": "#!/bin/sh\n",
			"scripts/post.sh":  "#!/bin/sh\n",
		})
		m, err := module.Load(root, "ignored")
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Format).To(Equal("lola"))
		Expect(m.Name).To(Equal("example"))
		Expect(m.Version).To(Equal("1.2.0"))
		Expect(m.Author).To(Equal(&module.Author{Name: "A", Email: "a@x"}))
		Expect(m.Targets).To(Equal([]string{"claude-code"}))
		Expect(m.DeclaredHooks).To(Equal([]module.Hook{{Event: "post-install", Script: "scripts/post.sh"}, {Event: "pre-install", Script: "scripts/setup.sh"}}))
	})

	DescribeTable("refuses hooks lola could not run",
		func(hooks string, files map[string]string, msg string) {
			files["lola.yml"] = "name: example\nhooks: " + hooks + "\n"
			files["commands/a.md"] = "x"
			write(root, files)
			_, err := module.Load(root, "x")
			Expect(err).To(MatchError(ContainSubstring(msg)))
		},
		Entry("unknown event", "{pre-uninstall: x.sh}", map[string]string{"x.sh": ""}, `lola.yml: hook "pre-uninstall" is unknown`),
		Entry("absolute script", "{pre-install: /bin/sh}", map[string]string{}, `pre-install hook "/bin/sh" must be a relative path inside the module`),
		Entry("script outside the module", "{pre-install: ../x.sh}", map[string]string{}, "must be a relative path inside the module"),
		Entry("missing script", "{post-install: nope.sh}", map[string]string{}, "post-install hook nope.sh does not exist"),
		Entry("directory", "{pre-install: scripts}", map[string]string{"scripts/a.sh": ""}, "pre-install hook scripts is not a regular file"),
	)

	It("refuses a hook script without an execute bit, with the fix", func() {
		write(root, map[string]string{"lola.yml": "name: example\nhooks: {pre-install: setup.txt}\n", "setup.txt": "", "commands/a.md": "x"})
		_, err := module.Load(root, "x")
		Expect(err).To(MatchError("lola.yml: pre-install hook setup.txt is not executable"))
		Expect(errs.Hints(err)).To(Equal([]string{"chmod +x setup.txt"}))
	})

	It("rejects unknown lola.yml keys with a hint naming the key", func() {
		write(root, map[string]string{"lola.yml": "name: example\nsummary: typo\n", "commands/a.md": "x"})
		_, err := module.Load(root, "x")
		Expect(err).To(MatchError(ContainSubstring("lola.yml")))
		Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("summary")))
	})

	It("requires name in lola.yml", func() {
		write(root, map[string]string{"lola.yml": "description: d\n", "commands/a.md": "x"})
		_, err := module.Load(root, "x")
		Expect(err).To(MatchError(ContainSubstring("name")))
	})

	It("reads the first manifest by precedence and warns about each one it ignores", func() {
		write(root, map[string]string{".lola.yml": "name: d\n", "lola.yml": "name: b\n", ".lola.yaml": "name: c\n", "commands/a.md": "x"})
		m, err := module.Load(root, "x")
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Name).To(Equal("b"))
		Expect(m.Manifest).To(Equal("lola.yml"))
		Expect(m.Shadowed).To(Equal([]string{".lola.yaml", ".lola.yml"}))
		Expect(m.Warnings).To(Equal([]string{".lola.yaml was ignored: lola.yml takes precedence", ".lola.yml was ignored: lola.yml takes precedence"}))
		write(root, map[string]string{"lola.yaml": "name: a\n"})
		m, err = module.Load(root, "x")
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Name).To(Equal("a"))
	})

	It("treats a root SKILL.md as a skill named after the module, excluding module files", func() {
		write(root, map[string]string{"SKILL.md": "---\ndescription: s\n---\n", "helper.py": "", "lola.yml": "name: solo\n", "commands/c.md": "x"})
		m, err := module.Load(root, "x")
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Skills).To(HaveLen(1))
		Expect(m.Skills[0].Name).To(Equal("solo"))
		Expect(m.Skills[0].Dir).To(Equal("."))
		Expect(m.Skills[0].Exclude).To(ContainElements("lola.yml", "commands", "SKILL.md"))
	})

	It("skips a skill directory without SKILL.md, with a warning", func() {
		write(root, map[string]string{"skills/empty/readme.txt": "", "commands/a.md": "x"})
		m, err := module.Load(root, "x")
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Skills).To(BeEmpty())
		Expect(m.Warnings).To(ContainElement(ContainSubstring("skills/empty")))
	})

	It("warns about legacy INSTRUCTIONS.md and AGENTS.md and never loads them as rules", func() {
		write(root, map[string]string{"INSTRUCTIONS.md": "x", "AGENTS.md": "x", "commands/a.md": "x"})
		m, err := module.Load(root, "x")
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Rules).To(BeEmpty())
		Expect(m.Warnings).To(Equal([]string{
			"INSTRUCTIONS.md in a module is not installed: lola never edits your assistants' instruction files. Move it to rules/<topic>.md to ship it as always-on rules",
			"AGENTS.md in a module is not installed: lola never edits your assistants' instruction files. Move it to rules/<topic>.md to ship it as always-on rules",
		}))
	})

	It("loads a module that ships only rules, sorted by name, and skips anything else in rules/ with a warning", func() {
		write(root, map[string]string{"rules/a-b.md": "AB\n", "rules/a.md": "A\n", "rules/notes.txt": "n", "rules/sub/x.md": "x"})
		m, err := module.Load(root, "x")
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Rules).To(Equal([]module.Rule{{Name: "a", Body: "A\n"}, {Name: "a-b", Body: "AB\n"}}))
		Expect(m.Warnings).To(ConsistOf("rules/notes.txt is not a rule (rules/<name>.md); skipped", "rules/sub is not a rule (rules/<name>.md); skipped"))
	})

	It("refuses a path-scoped rule and points at skills", func() {
		write(root, map[string]string{"rules/x.md": "---\npaths: [\"src/**\"]\n---\nX\n"})
		_, err := module.Load(root, "x")
		Expect(err).To(MatchError("rules/x.md: a rule applies in every session, so paths: is not supported"))
		Expect(errs.Hints(err)).To(Equal([]string{"path-scoped guidance belongs in a skill: skills/<name>/SKILL.md"}))
	})

	It("reports ErrNoModule naming what it looked for", func() {
		write(root, map[string]string{"README.md": "x", "AGENTS.md": "x"})
		_, err := module.Load(root, "x")
		Expect(errors.Is(err, module.ErrNoModule)).To(BeTrue())
		Expect(err).To(MatchError(ContainSubstring("skills/*/SKILL.md")))
	})

	DescribeTable("rejects invalid artifacts",
		func(files map[string]string, msg string) {
			write(root, files)
			_, err := module.Load(root, "x")
			Expect(err).To(MatchError(ContainSubstring(msg)))
		},
		Entry("bad skill name", map[string]string{"skills/Bad/SKILL.md": "x"}, "skill name"),
		Entry("bad command name", map[string]string{"commands/a b.md": "x"}, "command name"),
		Entry("bad rule name", map[string]string{"rules/Bad.md": "x"}, "rule name"),
		Entry("broken rule frontmatter", map[string]string{"rules/a.md": "---\nx: [\n---\n"}, "rules/a.md"),
		Entry("MCP server without command or url", map[string]string{"mcps.json": `{"mcpServers":{"x":{}}}`}, "command or a url"),
		Entry("unknown MCP type", map[string]string{"mcps.json": `{"mcpServers":{"x":{"type":"ws","url":"u"}}}`}, "transport"),
		Entry("corrupt mcps.json", map[string]string{"mcps.json": `{`}, "mcps.json"),
		Entry("broken agent frontmatter", map[string]string{"agents/a.md": "---\nx: [\n---\n"}, "agents/a.md"),
	)

	It("rejects an invalid fallback module name", func() {
		write(root, map[string]string{"commands/a.md": "x"})
		_, err := module.Load(root, "Bad Name")
		Expect(err).To(MatchError(ContainSubstring("module name")))
	})

	It("sorts commands and agents by name, not file name", func() {
		write(root, map[string]string{
			"commands/a-b.md": "cmd a-b",
			"commands/a.md":   "cmd a",
			"agents/a-b.md":   "---\ndescription: d\n---\nagent a-b",
			"agents/a.md":     "---\ndescription: d\n---\nagent a",
		})
		m, err := module.Load(root, "x")
		Expect(err).NotTo(HaveOccurred())
		Expect([]string{m.Commands[0].Name, m.Commands[1].Name}).To(Equal([]string{"a", "a-b"}))
		Expect([]string{m.Agents[0].Name, m.Agents[1].Name}).To(Equal([]string{"a", "a-b"}))
	})
})

var _ = Describe("Normalize", func() {
	var root string
	BeforeEach(func() { root = filepath.Join(testenv.Isolate(), "mod") })

	It("leaves exactly one generated lola.yml that loads to the same module", func() {
		write(root, map[string]string{
			".lola.yaml":    "name: example\ndescription: d\nauthor: {name: A}\nkeywords: [git]\ntargets: [claude-code]\nformats: {claude-code: {x: 1}}\nhooks: {pre-install: setup.sh}\n",
			".lola.yml":     "name: shadowed\n",
			"commands/a.md": "x",
			"setup.sh":      "#!/bin/sh\n",
		})
		m, err := module.Load(root, "fallback")
		Expect(err).NotTo(HaveOccurred())
		Expect(module.Normalize(root, m)).To(Succeed())
		Expect(filepath.Join(root, ".lola.yml")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(root, ".lola.yaml")).NotTo(BeAnExistingFile())
		again, err := module.Load(root, "fallback")
		Expect(err).NotTo(HaveOccurred())
		Expect(again.Manifest).To(Equal("lola.yml"))
		Expect(again.Shadowed).To(BeEmpty())
		Expect(again.Name).To(Equal("example"))
		Expect(again.Description).To(Equal("d"))
		Expect(again.Author).To(Equal(&module.Author{Name: "A"}))
		Expect(again.Keywords).To(Equal([]string{"git"}))
		Expect(again.Targets).To(Equal([]string{"claude-code"}))
		Expect(again.Formats).To(Equal(map[string]any{"claude-code": map[string]any{"x": 1}}))
		Expect(again.DeclaredHooks).To(Equal(m.DeclaredHooks))
	})

	It("gives a native-layout module a lola.yml carrying its name", func() {
		write(root, map[string]string{"commands/a.md": "x"})
		m, err := module.Load(root, "fallback")
		Expect(err).NotTo(HaveOccurred())
		Expect(module.Normalize(root, m)).To(Succeed())
		b, err := os.ReadFile(filepath.Join(root, "lola.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal("name: fallback\n"))
	})
})
