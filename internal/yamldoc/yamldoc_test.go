package yamldoc_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"go.yaml.in/yaml/v3"

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

type explainInner struct {
	Colour string `yaml:"colour"`
}

type explainDoc struct {
	Name   string       `yaml:"name"`
	Size   int          `yaml:"size"`
	Inner  explainInner `yaml:"inner"`
	Hidden string       `yaml:"-"`
}

var _ = Describe("Explain", func() {
	explain := func(doc string) string {
		GinkgoHelper()
		d, err := yamldoc.Decode([]byte(doc))
		Expect(err).NotTo(HaveOccurred())
		var v explainDoc
		dec := yaml.NewDecoder(strings.NewReader(doc))
		dec.KnownFields(true)
		err = dec.Decode(&v)
		Expect(err).To(HaveOccurred())
		return yamldoc.Explain("f.yml", err, yamldoc.Root(d), yamldoc.Shape{Type: reflect.TypeFor[explainDoc](), Noun: "a doc"}).Error()
	}

	It("lists the keys of the shape, never the Go type, skipping '-' keys", func() {
		Expect(explain("name: a\nbogus: 1\n")).To(Equal(`f.yml:2: unknown key "bogus" (a doc has: name, size, inner)`))
	})

	It("names an unknown key of a type it has no shape for without a key list or the type", func() {
		Expect(explain("inner:\n  shade: red\n")).To(Equal(`f.yml:2: unknown key "shade"`))
	})

	It("keeps a hostile key inert, as an unknown key and as the key of a wrong-kind value", func() {
		Expect(explain("\"a\\e[2J\\u202e\": 1\n")).NotTo(MatchRegexp("[\x1b\u202e]"))
		d, err := yamldoc.Decode([]byte("\"size\\u202e\": 1\n"))
		Expect(err).NotTo(HaveOccurred())
		root := yamldoc.Root(d)
		root.Content[0].Value = "size\u202e\x1b"
		err = &yaml.TypeError{Errors: []string{"line 1: cannot unmarshal !!str `x` into int"}}
		Expect(yamldoc.Explain("f.yml", err, root).Error()).NotTo(MatchRegexp("[\x1b\u202e]"))
	})

	It("says what a struct, a number and a date are in the file's terms", func() {
		Expect(explain("inner: x\n")).To(Equal("f.yml:1: inner: expected a mapping, found text"))
		Expect(explain("size: x\n")).To(Equal("f.yml:1: size: expected a number, found text"))
		Expect(explain("size: 2020-01-01\n")).To(Equal("f.yml:1: size: expected a number, found something else"))
	})

	It("keeps the decoder's words, behind the file, for an error that is no type error", func() {
		Expect(yamldoc.Explain("f.yml", errors.New("boom"), nil).Error()).To(Equal("f.yml: boom"))
	})
})
