package module_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("Partials at load", func() {
	var root string
	BeforeEach(func() { root = filepath.Join(testenv.Isolate(), "mod") })
	load := func(files map[string]string) (*module.Module, error) {
		write(root, files)
		return module.Load(root, "kit", targets.IDs())
	}

	It("reads _partials/<name>.liquid and <name>.<target>.liquid into Partials", func() {
		m, err := load(map[string]string{
			"commands/c.md":                                    "c\n",
			"_partials/header.liquid":                          "Header.\n",
			"_partials/header.claude-code.liquid":              "Claude header.\n",
			"_partials/a_b-9.liquid":                           "",
			"_partials/" + strings.Repeat("x", 64) + ".liquid": "x\n",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Partials).To(Equal(map[string]string{
			"_partials/header.liquid":                          "Header.\n",
			"_partials/header.claude-code.liquid":              "Claude header.\n",
			"_partials/a_b-9.liquid":                           "",
			"_partials/" + strings.Repeat("x", 64) + ".liquid": "x\n",
		}))
		Expect(m.Warnings).To(BeEmpty())

		Expect(os.RemoveAll(root)).To(Succeed())
		m, err = load(map[string]string{"commands/c.md": "c\n"})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Partials).To(BeNil(), "a module without _partials has none")
	})

	It("_partials refuses over 256 files or 4 MiB and warns on other entries", func() {
		files := map[string]string{"commands/c.md": "c\n"}
		for i := range 300 {
			files[fmt.Sprintf("_partials/p%d.liquid", i)] = "p\n"
		}
		_, err := load(files)
		Expect(err).To(MatchError("300 partial files; a module has at most 256"), "the true count")
		Expect(errs.Hints(err)).To(Equal([]string{"move or remove files in _partials/"}))

		Expect(os.RemoveAll(root)).To(Succeed())
		files = map[string]string{"commands/c.md": "c\n"}
		for i := range 16 {
			files[fmt.Sprintf("_partials/p%d.liquid", i)] = strings.Repeat("x", 256<<10)
		}
		_, err = load(files)
		Expect(err).NotTo(HaveOccurred(), "16 partials of 256 KiB are 4 MiB")
		Expect(os.WriteFile(filepath.Join(root, "_partials", "p16.liquid"), []byte("x"), 0o644)).To(Succeed())
		_, err = module.Load(root, "kit", targets.IDs())
		Expect(err).To(MatchError("_partials: the partials hold 4194305 bytes; a module's partials are at most 4 MiB together"))
		Expect(errs.Hints(err)).To(Equal([]string{"move or remove files in _partials/"}))
		for i := 17; i < 20; i++ {
			Expect(os.WriteFile(filepath.Join(root, "_partials", fmt.Sprintf("p%d.liquid", i)), []byte(strings.Repeat("x", 256<<10)), 0o644)).To(Succeed())
		}
		_, err = module.Load(root, "kit", targets.IDs())
		Expect(err).To(MatchError("_partials: the partials hold 4980737 bytes; a module's partials are at most 4 MiB together"), "the true total")

		Expect(os.RemoveAll(root)).To(Succeed())
		_, err = load(map[string]string{"commands/c.md": "c\n", "_partials/big.liquid": strings.Repeat("x", 256<<10+1)})
		Expect(err).To(MatchError("_partials/big.liquid: is 262145 bytes; a partial file is at most 256 KiB"))
		Expect(os.WriteFile(filepath.Join(root, "_partials", "big.liquid"), []byte(strings.Repeat("x", 4<<20+1)), 0o644)).To(Succeed())
		_, err = module.Load(root, "kit", targets.IDs())
		Expect(err).To(MatchError("_partials/big.liquid: is 4194305 bytes; a partial file is at most 256 KiB"),
			"one file over both caps is named by the per-file one")

		Expect(os.RemoveAll(root)).To(Succeed())
		_, err = load(map[string]string{"commands/c.md": "c\n", "_partials/bad.liquid": "ok\n\xff\n"})
		Expect(err).To(MatchError("_partials/bad.liquid:2: is not UTF-8; a partial file must be UTF-8 text"))

		Expect(os.RemoveAll(root)).To(Succeed())
		m, err := load(map[string]string{
			"commands/c.md":           "c\n",
			"_partials/ok.liquid":     "ok\n",
			"_partials/x.md":          "not a partial\n",
			"_partials/Header.liquid": "upper case\n",
			"_partials/_x.liquid":     "starts with _\n",
			"_partials/" + strings.Repeat("x", 65) + ".liquid": "too long\n",
			"_partials/h.nosuch.liquid":                        "unknown target\n",
			"_partials/h.claude-code.x.liquid":                 "two suffixes\n",
			"_partials/h.LIQUID":                               "suffix case\n",
			"_partials/sub/deep.liquid":                        "a subdirectory\n",
			"_partials/h.liquid.md":                            "wrong order\n",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Partials).To(Equal(map[string]string{"_partials/ok.liquid": "ok\n"}))
		const why = " is not a partial (_partials/<name>.liquid or <name>.<target>.liquid); ignored"
		Expect(m.Warnings).To(ConsistOf(
			"_partials/x.md"+why, "_partials/Header.liquid"+why, "_partials/_x.liquid"+why,
			"_partials/"+strings.Repeat("x", 65)+".liquid"+why, "_partials/h.nosuch.liquid"+why,
			"_partials/h.claude-code.x.liquid"+why, "_partials/h.LIQUID"+why, "_partials/sub"+why,
			"_partials/h.liquid.md"+why,
		))

		Expect(os.RemoveAll(root)).To(Succeed())
		_, err = load(map[string]string{"commands/c.md": "c\n", "_partials": "a file\n"})
		Expect(err).To(MatchError("_partials is not a directory; partials are _partials/<name>.liquid files"))
		Expect(errs.Hints(err)).To(Equal([]string{"rename the _partials file, or move it into a _partials/ directory"}))
	})

	It("_partials is read only from the entry named exactly _partials", func() {
		write(root, map[string]string{"commands/c.md": "c\n", "_partials/h.liquid": "h\n", "_Partials/x.liquid": "x\n"})
		if es, err := os.ReadDir(root); err != nil || len(es) != 3 {
			Skip("the file system folds case: _partials and _Partials are one directory")
		}
		m, err := module.Load(root, "kit", targets.IDs())
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Partials).To(Equal(map[string]string{"_partials/h.liquid": "h\n"}))
		Expect(m.Warnings).To(ConsistOf("_Partials is not read; name it _partials"))
	})

	It("a component path under _partials refuses", func() {
		for manifest, msg := range map[string]string{
			"skills: [{path: _partials}]":        "components.skills[0]: _partials: _partials holds partials, not components",
			"commands: [{path: _Partials/c.md}]": "components.commands[0]: _Partials/c.md: _partials holds partials, not components",
			"agents: [{path: ./_partials/a.md}]": "components.agents[0]: _partials/a.md: _partials holds partials, not components",
		} {
			Expect(os.RemoveAll(root)).To(Succeed())
			_, err := load(map[string]string{"lola.yml": "name: kit\ncomponents:\n  " + manifest + "\n", "_partials/c.md": "c\n", "_partials/a.md": "a\n"})
			Expect(err).To(MatchError(msg), manifest)
			Expect(errs.Hints(err)).To(Equal([]string{"move the component out of _partials/"}), manifest)
		}
	})

	It("a case variant of _partials is not read and warns", func() {
		m, err := load(map[string]string{"commands/c.md": "c\n", "_Partials/header.liquid": "h\n"})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Partials).To(BeNil())
		Expect(m.Warnings).To(ConsistOf("_Partials is not read; name it _partials"))

		for _, manifest := range []string{"", "name: kit\ncomponents:\n  skills: [{path: .}]\n"} {
			Expect(os.RemoveAll(root)).To(Succeed())
			files := map[string]string{"SKILL.md": "Root.\n", "_Partials/header.liquid": "h\n"}
			if manifest != "" {
				files["lola.yml"] = manifest
			}
			m, err = load(files)
			Expect(err).NotTo(HaveOccurred(), manifest)
			Expect(m.Skills).To(HaveLen(1), manifest)
			Expect(m.Skills[0].Exclude).To(ContainElements("_partials", "_Partials"), "a root skill copies neither: "+manifest)
		}
	})
})
