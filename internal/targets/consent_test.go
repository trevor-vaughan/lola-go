package targets_test

import (
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/target/spec"
	"github.com/trevor-vaughan/lola-go/internal/targetspec"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// quillSpec places agents with no hook_settings and keeps unknown frontmatter keys: only policy
// stands between a module's hooks and the written file.
const quillSpec = `id: quill
readers: "the quill editor"
roots: {project: "{project}", user: "{home}"}
detect: {project: {any: [.quill/]}, user: {any: [.quill/]}}
eras:
  - since: "1.0"
    layout:
      agent: {mech: markdown-file, path: .quill/agents, ext: .md}
    kinds:
      agent: {unknown_keys: keep}
`

var _ = Describe("a spec that sets up no consent", func() {
	It("never writes a module's frontmatter hooks or MCP servers", func() {
		root := testenv.Isolate()
		s, err := targetspec.Parse("quill.yaml", []byte(quillSpec))
		Expect(err).NotTo(HaveOccurred())
		ctx := target.PlanContext{Scope: target.Project, Root: root, ContentDir: filepath.Join(root, "content"),
			PluginRoot: "/proj/.lola/modules/example", PluginData: "/proj/.lola/plugin-data/example"}
		m := &module.Module{Name: "example", Agents: []module.Agent{{Name: "a", Doc: doc(
			"---\ndescription: d\nhooks: {Stop: [{hooks: [{type: command, command: x}]}]}\nmcpServers: {s: {command: z}}\n---\nBody.\n")}}}

		ins, fs, err := spec.New(s).Plan(m, ctx)
		Expect(err).NotTo(HaveOccurred())

		agent := byPath(ins)[".quill/agents/a.md"]
		Expect(string(agent.Data)).To(ContainSubstring("description: d"))
		Expect(string(agent.Data)).NotTo(ContainSubstring("hooks"))
		Expect(string(agent.Data)).NotTo(ContainSubstring("mcpServers"))
		for _, in := range ins {
			Expect(in.Consent).To(BeNil(), in.Path)
		}
		const why = " dropped: lola writes hooks and MCP servers into a component only under consent, which quill's spec does not set up"
		var texts []string
		for _, f := range fs {
			Expect(f.Severity).To(Equal(target.SeverityWarn))
			texts = append(texts, f.Text)
		}
		Expect(texts).To(ConsistOf("hooks"+why, "mcpServers"+why))
	})
})
