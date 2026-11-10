package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/cli"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// quillSpec is a user target with no binary that writes agents under .quill/agents.
const quillSpec = `id: quill
readers: "the quill editor"
roots: {project: "{project}", user: "{home}"}
detect: {project: {any: [.quill/]}, user: {any: [.quill/]}}
eras:
  - since: "1.0"
    layout:
      agent: {mech: markdown-file, path: .quill/agents, ext: .md}
`

var _ = Describe("user target specs", func() {
	var tmp, specs string
	BeforeEach(func() {
		tmp = testenv.Isolate()
		specs = filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "lola", "targets")
		Expect(os.MkdirAll(specs, 0o755)).To(Succeed())
	})
	write := func(name, text string) {
		GinkgoHelper()
		Expect(os.WriteFile(filepath.Join(specs, name), []byte(text), 0o600)).To(Succeed())
	}

	It("lists a user target beside the built-ins in a target flag's usage", func() {
		write("quill.yaml", quillSpec)
		r := lola("install", "--help")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("comma-separated: claude-code, gemini-cli, opencode, agents, quill)"))
	})

	It("installs into a user target and accepts it in a module entry's targets", func() {
		write("quill.yaml", quillSpec)
		mod, proj := filepath.Join(tmp, "kit"), filepath.Join(tmp, "proj")
		writeTree(mod, map[string]string{
			"lola.yml":         "name: kit\ncomponents:\n  agents: [{path: agents/helper.md, targets: [quill]}]\n",
			"agents/helper.md": "---\ndescription: Helps.\n---\nHelp.\n",
		})
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		r := lola("install", mod, "-t", "quill", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).NotTo(ContainSubstring("unknown target"))
		Expect(filepath.Join(proj, ".quill", "agents", "helper.md")).To(BeARegularFile())
	})

	It("refuses a module entry that names a target nobody defined", func() {
		mod, proj := filepath.Join(tmp, "kit"), filepath.Join(tmp, "proj")
		writeTree(mod, map[string]string{
			"lola.yml":         "name: kit\ncomponents:\n  agents: [{path: agents/helper.md, targets: [quill]}]\n",
			"agents/helper.md": "---\ndescription: Helps.\n---\nHelp.\n",
		})
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		r := lola("install", mod, "-t", "claude-code", "-C", proj)
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring(`unknown target "quill"`))
		Expect(filepath.Join(proj, ".quill")).NotTo(BeADirectory())
	})

	It("offers a user target when completing --target", func() {
		write("quill.yaml", quillSpec)
		got, dir := completions("install", "-t", "qu")
		Expect(got).To(ContainElement(HavePrefix("quill")))
		Expect(dir).To(Equal(dirOrdered))
	})

	Describe("a user spec that replaces a built-in", func() {
		BeforeEach(func() {
			write("mine.yaml", strings.Replace(quillSpec, "id: quill", "id: opencode", 1))
		})

		It("says so once on stderr in human output", func() {
			note := "note: opencode uses your target spec " + filepath.Join(specs, "mine.yaml") + " in place of lola's built-in\n"
			r := lola("list")
			Expect(r.code).To(Equal(0), r.err)
			Expect(strings.Count(r.err, note)).To(Equal(1), r.err)
		})

		It("stays quiet under --json and --plain", func() {
			for _, f := range []string{"--json", "--plain"} {
				r := lola("list", f)
				Expect(r.code).To(Equal(0), r.err)
				Expect(r.err).NotTo(ContainSubstring("in place of lola's built-in"), f)
			}
		})
	})

	Describe("a spec that cannot be loaded", func() {
		BeforeEach(func() {
			write("quill.yaml", "id: quill\nroots: {project: \"{project}\", user: \"{home}\"}\n")
		})

		It("stops the command tree from being built, naming the file", func() {
			root, err := cli.NewRootCmd(os.Getenv)
			Expect(root).To(BeNil())
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("quill.yaml"))
		})

		It("is reported as one JSON object when the command line asks for JSON", func() {
			_, err := cli.NewRootCmd(os.Getenv)
			Expect(err).To(HaveOccurred())
			var buf bytes.Buffer
			Expect(cli.StartupError(&buf, err, []string{"version", "--json"})).To(Equal(1))
			var doc struct {
				Error string   `json:"error"`
				Hints []string `json:"hints"`
			}
			Expect(json.Unmarshal(buf.Bytes(), &doc)).To(Succeed(), buf.String())
			Expect(doc.Error).To(ContainSubstring("quill.yaml"))
			Expect(doc.Hints).NotTo(BeEmpty())
		})

		It("is reported as an error with hint lines otherwise", func() {
			_, err := cli.NewRootCmd(os.Getenv)
			Expect(err).To(HaveOccurred())
			var buf bytes.Buffer
			Expect(cli.StartupError(&buf, err, []string{"version"})).To(Equal(1))
			Expect(buf.String()).To(HavePrefix("error: "))
			Expect(buf.String()).To(ContainSubstring("quill.yaml"))
			Expect(strings.Count(buf.String(), "  hint: ")).To(BeNumerically(">", 0))
		})
	})
})
