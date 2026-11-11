package spec_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/target/spec"
	"github.com/trevor-vaughan/lola-go/internal/targetspec"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

const tool = `id: tool
readers: skills for Tool
kind_unsupported_note: tool places skills only
roots: {project: "{project}", user: "{config_home}/tool"}
detect:
  project: {any: [.tool/]}
  user: {dir: .}
eras:
  - since: "1.0"
    layout:
      skill: {mech: agentskills-dir, path: skills}
    kinds:
      skill: {unknown_keys: keep}
`

var _ = Describe("a target built from a spec", func() {
	var t spec.Target
	BeforeEach(func() {
		testenv.Isolate()
		s, err := targetspec.Parse("tool.yaml", []byte(tool))
		Expect(err).NotTo(HaveOccurred())
		t = spec.New(s)
	})

	It("roots at the spec's scope roots", func() {
		p := paths.Paths{Home: "/h", ConfigHome: "/c/"}
		Expect(t.Root(target.User, "/proj", p)).To(Equal("/c/tool"))
		Expect(t.Root(target.Project, "/proj", p)).To(Equal("/proj"))
	})

	It("detects a user root that is a directory, and not one that is missing", func() {
		root := testenv.Isolate()
		Expect(t.Detect(target.User, root)).To(BeTrue())
		Expect(t.Detect(target.User, filepath.Join(root, "missing"))).To(BeFalse())
	})

	It("detects a project by its marker directory", func() {
		root := testenv.Isolate()
		Expect(t.Detect(target.Project, root)).To(BeFalse())
		Expect(os.MkdirAll(filepath.Join(root, ".tool", "x"), 0o755)).To(Succeed())
		Expect(t.Detect(target.Project, root)).To(BeTrue())
	})

	It("reports a kind its layout lacks as unsupported, with the spec's note", func() {
		m := &module.Module{Name: "example", Commands: []module.Command{{Name: "ship"}}}
		ins, finds, err := t.Plan(m, target.PlanContext{Scope: target.Project, Root: testenv.Isolate()})
		Expect(err).NotTo(HaveOccurred())
		Expect(ins).To(BeEmpty())
		Expect(finds).To(HaveLen(1))
		Expect(finds[0].Code).To(Equal("kind-unsupported"))
		Expect(finds[0].Text).To(HaveSuffix(": tool places skills only"))
	})

	It("writes each rule as its own file when the layout has no concat", func() {
		const ruled = `id: ruled
readers: rules for Ruled
roots: {project: "{project}", user: "{home}"}
detect:
  project: {any: [.ruled/]}
  user: {any: [.ruled/]}
eras:
  - since: "1.0"
    layout:
      rule: {mech: rules-dir, path: .ruled/lola}
    kinds:
      rule: {}
`
		s, err := targetspec.Parse("ruled.yaml", []byte(ruled))
		Expect(err).NotTo(HaveOccurred())
		m := &module.Module{Name: "example", Rules: []module.Rule{{Name: "style", Body: "Prefer small diffs.\n"}, {Name: "tone", Body: "Be brief.\n"}}}
		ins, finds, err := spec.New(s).Plan(m, target.PlanContext{Scope: target.Project, Root: testenv.Isolate()})
		Expect(err).NotTo(HaveOccurred())
		Expect(finds).To(BeEmpty())
		Expect(ins).To(HaveLen(2))
		Expect(ins[0].Path).To(Equal(".ruled/lola/example/style.md"))
		Expect(string(ins[0].Data)).To(Equal(module.Doc{Body: "Prefer small diffs.\n"}.Render()))
		Expect(ins[1].Path).To(Equal(".ruled/lola/example/tone.md"))
		Expect(string(ins[1].Data)).To(Equal(module.Doc{Body: "Be brief.\n"}.Render()))
	})

	It("maps every probe, in order, to a method", func() {
		const probed = `id: probed
probe:
  - file: {path: "{config_home}/probed/state.json", pointer: /version}
  - exec: {binaries: [probed, probed-cli], args: [--version], match: 'v(\d+\.\d+\.\d+)'}
roots: {project: "{project}", user: "{home}"}
detect:
  project: {any: [.probed/]}
  user: {any: [.probed/]}
eras:
  - since: "1.0"
    layout:
      skill: {mech: agentskills-dir, path: skills}
`
		s, err := targetspec.Parse("probed.yaml", []byte(probed))
		Expect(err).NotTo(HaveOccurred())
		p := spec.New(s).Probe()
		Expect(p.Methods).To(HaveLen(2))
		file, run := p.Methods[0], p.Methods[1]
		Expect(file.File(paths.Paths{Home: "/h", ConfigHome: "/c", DataHome: "/d"})).To(Equal(filepath.Join("/c", "probed", "state.json")))
		Expect(file.Pointer).To(Equal("/version"))
		Expect(file.Match).To(BeNil())
		Expect(file.Binaries).To(BeEmpty())
		Expect(run.File).To(BeNil())
		Expect(run.Binaries).To(Equal([]string{"probed", "probed-cli"}))
		Expect(run.Args).To(Equal([]string{"--version"}))
		Expect(run.Match.FindStringSubmatch("probed v1.2.3")).To(Equal([]string{"v1.2.3", "1.2.3"}))
		Expect(p.Binaries()).To(Equal([]string{"probed", "probed-cli"}))
		Expect(p.Readers).To(BeEmpty())
	})

	It("has no binaries when it probes no binary", func() {
		Expect(t.Probe().Methods).To(BeEmpty())
		Expect(t.Probe().Binaries()).To(BeEmpty())
		Expect(target.NoBinary(t)).To(BeTrue())
	})
})
