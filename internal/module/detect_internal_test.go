package module

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// fakeAdapter accepts its manifest as the module lf, or declines it with a warning.
type fakeAdapter struct {
	file    string
	lf      lolaFile
	decline string
}

func (f fakeAdapter) manifest() string { return f.file }

func (f fakeAdapter) read(string) (*lolaFile, []string, bool, error) {
	if f.decline != "" {
		return nil, []string{f.decline}, false, nil
	}
	lf := f.lf
	return &lf, []string{f.file + " read"}, true, nil
}

var _ = Describe("detect", func() {
	known := []string{"claude-code", "gemini-cli", "opencode"}
	var root string
	put := func(files map[string]string) {
		for p, c := range files {
			Expect(os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(root, p), []byte(c), 0o644)).To(Succeed())
		}
	}
	empty := &[]commandEntry{}
	first := fakeAdapter{file: "plugin.json", lf: lolaFile{Name: "first", Format: FormatAgentPlugins, Components: &components{Commands: empty}}}
	second := fakeAdapter{file: ".claude-plugin/plugin.json", lf: lolaFile{Name: "second", Format: FormatClaudePlugin}}
	BeforeEach(func() {
		root = filepath.Join(testenv.Isolate(), "mod")
		put(map[string]string{"commands/a.md": "x", "rules/r.md": "r", "plugin.json": "{}", ".claude-plugin/plugin.json": "{}"})
	})

	It("reads a lola manifest first and records every plugin manifest as shadowed", func() {
		put(map[string]string{"lola.yml": "name: mine\n", ".lola.yml": "name: other\n"})
		m, err := detect(root, "x", known, []adapter{first, second})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Name).To(Equal("mine"))
		Expect(m.Format).To(Equal("lola"))
		Expect(m.Manifest).To(Equal("lola.yml"))
		Expect(m.Shadowed).To(Equal([]string{".lola.yml", "plugin.json", ".claude-plugin/plugin.json"}))
		Expect(m.Warnings).To(Equal([]string{".lola.yml was ignored: lola.yml takes precedence",
			"plugin.json was ignored: lola.yml takes precedence", ".claude-plugin/plugin.json was ignored: lola.yml takes precedence"}))
	})

	It("uses the first adapter whose manifest is present, applies its components, and shadows the rest", func() {
		m, err := detect(root, "x", known, []adapter{first, second})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Name).To(Equal("first"))
		Expect(m.Format).To(Equal(FormatAgentPlugins))
		Expect(m.Manifest).To(Equal("plugin.json"))
		Expect(m.Shadowed).To(Equal([]string{".claude-plugin/plugin.json"}))
		Expect(m.Warnings).To(Equal([]string{"plugin.json read", ".claude-plugin/plugin.json was ignored: plugin.json takes precedence"}))
		Expect(m.Commands).To(BeEmpty(), "the listed, empty commands kind turns discovery off")
		Expect(m.Rules).To(HaveLen(1), "unlisted kinds are discovered")
	})

	It("falls through an adapter that declines its manifest, keeping its warning", func() {
		declining := first
		declining.decline = "plugin.json is not an Agent Plugins manifest"
		m, err := detect(root, "x", known, []adapter{declining, second})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Name).To(Equal("second"))
		Expect(m.Manifest).To(Equal(".claude-plugin/plugin.json"))
		Expect(m.Shadowed).To(BeEmpty())
		Expect(m.Warnings).To(Equal([]string{"plugin.json is not an Agent Plugins manifest", ".claude-plugin/plugin.json read"}))
	})

	It("skips an adapter whose manifest is absent and reads the native layout when none applies", func() {
		Expect(os.Remove(filepath.Join(root, "plugin.json"))).To(Succeed())
		m, err := detect(root, "x", known, []adapter{first, second})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Name).To(Equal("second"))
		Expect(m.Shadowed).To(BeEmpty())
		Expect(os.RemoveAll(filepath.Join(root, ".claude-plugin"))).To(Succeed())
		m, err = detect(root, "fallback", known, []adapter{first, second})
		Expect(err).NotTo(HaveOccurred())
		Expect(m).To(HaveField("Name", "fallback"))
		Expect(m).To(HaveField("Format", "native"))
		Expect(m).To(HaveField("Manifest", ""))
	})

	It("checks an adapter's manifest like a lola.yml", func() {
		bad := first
		bad.lf.Name = "Bad"
		_, err := detect(root, "x", known, []adapter{bad})
		Expect(err).To(MatchError(ContainSubstring(`module name "Bad" is invalid`)))
		bad = first
		bad.lf.Format = "other"
		_, err = detect(root, "x", known, []adapter{bad})
		Expect(err).To(MatchError(`plugin.json: format "other" is unknown`))
	})

	It("never consults an adapter when loading", func() {
		m, err := Load(root, "fallback", known)
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Format).To(Equal("native"))
		Expect(m.Shadowed).To(BeEmpty())
		Expect(m.Warnings).To(BeEmpty())
	})
})
