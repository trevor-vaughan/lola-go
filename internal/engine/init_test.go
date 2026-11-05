package engine_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("Init", func() {
	var tmp string
	BeforeEach(func() { tmp = testenv.Isolate() })

	It("scaffolds an Agent Plugins package by default, which loads as one", func() {
		Expect(engine.InitFormats()[0]).To(Equal("agent-plugins"))
		dir := filepath.Join(tmp, "my.tools")
		s, err := engine.Init(dir, "agent-plugins")
		Expect(err).NotTo(HaveOccurred())
		Expect(s).To(Equal(engine.Scaffolded{Name: "my.tools", Path: dir, Format: "agent-plugins",
			Files: []string{"plugin.json", "skills/hello/SKILL.md", "dev.getlola/commands/hello.md"}}))
		b, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(MatchJSON(`{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"my.tools",
			"version":"0.1.0","description":"Describe what this plugin does"}`))
		m, err := module.Detect(dir, "x", targets.IDs())
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Format).To(Equal("agent-plugins"))
		Expect(m.Name).To(Equal("my.tools"))
		Expect(m.Skills).To(HaveLen(1))
		Expect(m.Commands).To(HaveLen(1))
		Expect(m.Commands[0].Targets).To(BeNil(), "a dev.getlola command reaches every target")
		Expect(m.Warnings).To(BeEmpty())
	})

	It("scaffolds a native lola module, which loads as one", func() {
		dir := filepath.Join(tmp, "my_module")
		s, err := engine.Init(dir, "lola")
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Files).To(Equal([]string{"lola.yml", "skills/hello/SKILL.md", "commands/hello.md"}))
		m, err := module.Detect(dir, "x", targets.IDs())
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Format).To(Equal("lola"))
		Expect(m.Name).To(Equal("my_module"))
		Expect(m.Warnings).To(BeEmpty())
	})

	It("checks the directory name with the format's rule", func() {
		_, err := engine.Init(filepath.Join(tmp, "my_module"), "agent-plugins")
		Expect(err).To(MatchError(`"my_module" is not an Agent Plugins name`))
		Expect(errs.Hints(err)).To(ContainElement("or pass --format lola, whose names may also hold '_'"))
		_, err = engine.Init(filepath.Join(tmp, "Bad"), "lola")
		Expect(err).To(MatchError(ContainSubstring(`module name "Bad" is invalid`)))
		Expect(errs.Hints(err)).To(ConsistOf("run lola init in a directory whose name is a valid module name"))
		Expect(filepath.Join(tmp, "my_module")).NotTo(BeADirectory())
	})

	It("refuses an unknown format", func() {
		_, err := engine.Init(filepath.Join(tmp, "x"), "claude-plugin")
		Expect(err).To(MatchError(`lola init cannot scaffold format "claude-plugin"`))
		Expect(errs.Hints(err)).To(ConsistOf("use --format agent-plugins or --format lola"))
	})

	DescribeTable("refuses a directory that already holds a module manifest, whatever the format",
		func(manifest, format string) {
			dir := filepath.Join(tmp, "existing")
			Expect(os.MkdirAll(filepath.Dir(filepath.Join(dir, manifest)), 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(dir, manifest), []byte("{}"), 0o644)).To(Succeed())
			_, err := engine.Init(dir, format)
			Expect(err).To(MatchError(dir + " is already a module (it has " + manifest + ")"))
			Expect(filepath.Join(dir, "skills")).NotTo(BeADirectory())
		},
		Entry("lola.yml", "lola.yml", "agent-plugins"),
		Entry(".lola.yaml", ".lola.yaml", "lola"),
		Entry("plugin.json", "plugin.json", "lola"),
		Entry(".claude-plugin/plugin.json", ".claude-plugin/plugin.json", "agent-plugins"),
	)

	It("writes nothing when any file it would write exists", func() {
		dir := filepath.Join(tmp, "partial")
		Expect(os.MkdirAll(filepath.Join(dir, "dev.getlola", "commands"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dir, "dev.getlola", "commands", "hello.md"), []byte("mine"), 0o644)).To(Succeed())
		_, err := engine.Init(dir, "agent-plugins")
		Expect(err).To(MatchError(filepath.Join(dir, "dev.getlola/commands/hello.md") + " already exists; lola init never overwrites files"))
		Expect(filepath.Join(dir, "plugin.json")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(dir, "skills")).NotTo(BeADirectory())
		Expect(errs.Hints(err)).To(ConsistOf("remove or move it, or run lola init in a new directory"))
	})

	It("refuses a symbolic link between the directory and a file it would write, and writes nothing", func() {
		dir := filepath.Join(tmp, "linked")
		outside := filepath.Join(tmp, "outside")
		Expect(os.MkdirAll(outside, 0o755)).To(Succeed())
		Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
		Expect(os.Symlink(outside, filepath.Join(dir, "skills"))).To(Succeed())
		_, err := engine.Init(dir, "agent-plugins")
		Expect(err).To(MatchError(ContainSubstring("skills is a symbolic link; lola init never writes through links")))
		Expect(errs.Hints(err)).To(ConsistOf("remove the link, or run lola init in a new directory"))
		Expect(filepath.Join(dir, "plugin.json")).NotTo(BeAnExistingFile())
		entries, readErr := os.ReadDir(outside)
		Expect(readErr).NotTo(HaveOccurred())
		Expect(entries).To(BeEmpty())
	})
})
