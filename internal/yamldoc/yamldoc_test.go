package yamldoc_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
	"github.com/trevor-vaughan/lola-go/internal/yamldoc"
)

var _ = Describe("ReadNoFollow", func() {
	It("refuses a symbolic link that Read follows", func() {
		dir := testenv.Isolate()
		file, link := filepath.Join(dir, "real.yml"), filepath.Join(dir, "link.yml")
		Expect(os.WriteFile(file, []byte("version: 1\n"), 0o644)).To(Succeed())
		if err := os.Symlink(file, link); err != nil {
			Skip("no symbolic links here: " + err.Error())
		}
		_, _, err := yamldoc.ReadNoFollow(link, 1)
		Expect(err).To(HaveOccurred())
		d, b, err := yamldoc.Read(link, 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(d).NotTo(BeNil())
		Expect(b).To(Equal([]byte("version: 1\n")))
	})
})

var _ = Describe("Decode", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("reads one document and refuses what would make one entry stand for another", func() {
		d, err := yamldoc.Decode([]byte("name: x\nversion: \"1.0\"\nmodules:\n  - {name: a}\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(yamldoc.Value(yamldoc.Root(d), "version").Value).To(Equal("1.0"))

		d, err = yamldoc.Decode([]byte("  \n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(d).To(BeNil())

		for in, want := range map[string]string{
			"a: 1\n---\nb: 2\n":                        "holds more than one YAML document",
			"a: &x 1\nb: 2\n":                          "line 1: anchors, aliases and merge keys (<<) are not supported",
			"a: 1\nb: *x\n":                            "does not parse",
			"base: {a: 1}\nm:\n  <<: {a: 2}\n":         "line 3: anchors, aliases and merge keys (<<) are not supported",
			"a: 1\nm:\n  - {name: x, name: y}\n":       "line 3: the key name is given twice",
			"a: 1\na: 2\n":                             "line 2: the key a is given twice",
			"a: [1\n":                                  "does not parse",
			"modules:\n  - name: x\n    name: \"y\"\n": "line 3: the key name is given twice",
		} {
			_, err := yamldoc.Decode([]byte(in))
			Expect(err).To(MatchError(ContainSubstring(want)), in)
		}
	})
})
