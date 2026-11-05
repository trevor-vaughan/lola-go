package module_test

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.yaml.in/yaml/v3"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("lola.yml vars", func() {
	var root string
	BeforeEach(func() { root = filepath.Join(testenv.Isolate(), "mod") })
	load := func(lola string) (*module.Module, error) {
		GinkgoHelper()
		write(root, map[string]string{"lola.yml": lola, "commands/c.md": "c\n"})
		return module.Load(root, "kit", targets.IDs())
	}

	It("reads vars as strings, booleans and lists of strings, and keeps them through Normalize", func() {
		m, err := load("name: kit\nvars:\n  language: go\n  strict: false\n  tools: [rg, jq]\n  empty: []\n  quoted: '5'\n  text: \"a\\tb\\nc\"\n")
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Vars).To(Equal(map[string]any{"language": "go", "strict": false, "tools": []string{"rg", "jq"}, "empty": []string{},
			"quoted": "5", "text": "a\tb\nc"}))
		Expect(module.Normalize(root, m)).To(Succeed())
		again, err := module.Load(root, "kit", targets.IDs())
		Expect(err).NotTo(HaveOccurred())
		Expect(again.Vars).To(Equal(m.Vars))
		Expect(module.SameMeaning(m, again)).To(BeTrue())
		again.Vars["language"] = "rust"
		Expect(module.SameMeaning(m, again)).To(BeFalse(), "vars are part of a module's meaning")

		m, err = load("name: kit\n")
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Vars).To(BeNil(), "a module may declare none")
		m, err = load("name: kit\nvars: {}\n")
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Vars).To(BeNil(), "an empty vars: declares none")
	})

	It("lola.yml vars refuse bad names, types, sizes and counts, naming the line", func() {
		many := func(n int) string {
			var b strings.Builder
			for i := range n {
				fmt.Fprintf(&b, "  v%d: x\n", i)
			}
			return b.String()
		}
		items := strings.TrimSuffix(strings.Repeat("x, ", 65), ", ")
		big := strings.Repeat("y", 4000)
		for vars, want := range map[string]string{
			"  Lang: go\n": "lola.yml:3: vars.Lang: a var name is a-z, 0-9 and _, starts with a letter, at most 64 characters",
			"  1st: go\n":  "lola.yml:3: vars.1st: a var name is a-z, 0-9 and _, starts with a letter, at most 64 characters",
			"  " + strings.Repeat("a", 65) + ": go\n": "lola.yml:3: vars." + strings.Repeat("a", 65) + ": a var name is a-z, 0-9 and _, starts with a letter, at most 64 characters",
			many(65):                                       "lola.yml:3: vars: 65 vars; a module declares at most 64",
			"  ratio: .nan\n":                              "lola.yml:3: vars.ratio: NaN and infinity are not supported",
			"  ratio: -.Inf\n":                             "lola.yml:3: vars.ratio: NaN and infinity are not supported",
			"  ratio: 1e999\n":                             "lola.yml:3: vars.ratio: NaN and infinity are not supported",
			"  ratio: !!float 1e400\n":                     "lola.yml:3: vars.ratio: NaN and infinity are not supported",
			"  ratio: !!float abc\n":                       "lola.yml:3: vars.ratio: not a number; remove the !!float tag to make it a string",
			"  max: 99999999999999999999999\n":             "lola.yml:3: vars.max: a number beyond 64 bits is not supported; quote it to make it a string",
			"  max: 9223372036854775808\n":                 "lola.yml:3: vars.max: a number beyond 64 bits is not supported; quote it to make it a string",
			"  max: !!int abc\n":                           "lola.yml:3: vars.max: not a whole number; remove the !!int tag to make it a string",
			"  big: [1e999]\n":                             `lola.yml:3: vars.big[0]: numbers are not supported; quote it to make it a string: "1e999"`,
			"  since: 2024-01-02\n":                        `lola.yml:3: vars.since: dates are not supported; quote it to make it a string: "2024-01-02"`,
			"  nested: {a: b}\n":                           "lola.yml:3: vars.nested: a var is a string, true or false, a number, or a list of strings",
			"  none:\n":                                    "lola.yml:3: vars.none: a var is a string, true or false, a number, or a list of strings",
			"  deep: [[a]]\n":                              "lola.yml:3: vars.deep[0]: a var is a string, true or false, a number, or a list of strings",
			"  mixed: [a, 1]\n":                            `lola.yml:3: vars.mixed[1]: numbers are not supported; quote it to make it a string: "1"`,
			"  intro: " + strings.Repeat("x", 4097) + "\n": "lola.yml:3: vars.intro: longer than 4096 bytes",
			"  bell: \"a\\u0007\"\n":                       "lola.yml:3: vars.bell: holds a control character",
			"  c1: \"a\\u0085\"\n":                         "lola.yml:3: vars.c1: holds a control character",
			"  tools: [" + items + "]\n":                   "lola.yml:3: vars.tools: 65 items; a list holds at most 64",
			"  a: [" + strings.TrimSuffix(strings.Repeat(big+", ", 17), ", ") + "]\n": "lola.yml:3: vars: 67 KiB in total; at most 64 KiB",
			"  a: x\n  a: y\n":              "lola.yml:4: vars.a: the var is given twice",
			"  x: !custom y\n":              "lola.yml:3: vars.x: a var is a string, true or false, a number, or a list of strings",
			"  b: !!bool \"x\"\n":           "lola.yml:3: vars.b: a var is a string, true or false, a number, or a list of strings",
			"  base: &b go\n  lang: *b\n":   "lola.yml:4: vars.lang: aliases are not supported",
			"  base: &b go\n  l: [a, *b]\n": "lola.yml:4: vars.l[1]: aliases are not supported",
			"  base: &b go\n  *b : x\n":     "lola.yml:4: vars: aliases are not supported",
			"  ? [a]\n  : x\n":              "lola.yml:3: vars: a var name is a-z, 0-9 and _, starts with a letter, at most 64 characters",
			"  ok: x\n  ? {a: b}\n  : x\n":  "lola.yml:4: vars: a var name is a-z, 0-9 and _, starts with a letter, at most 64 characters",
		} {
			_, err := load("name: kit\nvars:\n" + vars)
			Expect(err).To(MatchError(want), vars[:min(len(vars), 40)])
		}
		_, err := load("name: kit\nvars: [a]\n")
		Expect(err).To(MatchError("lola.yml:2: vars: vars map each var name to its value"))

		m, err := load("name: kit\nvars:\n  max: 5\n  ratio: 0.5\n  one: 1.0\n  neg: -0.0\n  exp: 1e2\n  low: -9223372036854775808\n  quoted: '1e999'\n")
		Expect(err).NotTo(HaveOccurred(), "number defaults are accepted, int and float")
		Expect(m.Vars).To(Equal(map[string]any{"max": int64(5), "ratio": 0.5, "one": 1.0, "neg": math.Copysign(0, -1), "exp": 100.0,
			"low": int64(math.MinInt64), "quoted": "1e999"}))
		Expect(math.Signbit(m.Vars["neg"].(float64))).To(BeTrue(), "-0.0 keeps its sign")
	})

	It("ParseVars refuses an entry's var that is not UTF-8", func() {
		str := func(line int, v string) *yaml.Node {
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v, Line: line}
		}
		n := &yaml.Node{Kind: yaml.MappingNode, Line: 4, Content: []*yaml.Node{str(5, "lang"), str(5, "go\xff")}}
		_, err := module.ParseVars(n, "modules[0].vars")
		Expect(err).To(MatchError("5: modules[0].vars.lang: is not UTF-8"))
		n.Content[1] = &yaml.Node{Kind: yaml.SequenceNode, Line: 5, Content: []*yaml.Node{str(5, "ok"), str(6, "\xfe")}}
		_, err = module.ParseVars(n, "modules[0].vars")
		Expect(err).To(MatchError("6: modules[0].vars.lang[1]: is not UTF-8"))
	})
})

var _ = Describe("DecodedVars", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("reads lock.json's vars by the rules of an entry's, each list as a []string", func() {
		vars, ok := module.DecodedVars(map[string]any{"lang": "go", "strict": true, "tools": []any{"rg", "jq"}})
		Expect(ok).To(BeTrue())
		Expect(vars).To(Equal(map[string]any{"lang": "go", "strict": true, "tools": []string{"rg", "jq"}}))
	})

	It("refuses vars that break a rule, returning none", func() {
		many, big := map[string]any{}, map[string]any{}
		for i := range 65 {
			many[fmt.Sprintf("v%d", i)] = "x"
		}
		for i := range 17 {
			big[fmt.Sprintf("v%d", i)] = strings.Repeat("y", 4000)
		}
		for why, raw := range map[string]map[string]any{
			"a bad name":           {"Lang": "go"},
			"more than 64 vars":    many,
			"over 64 KiB together": big,
			"a control character":  {"lang": "go\u0007"},
			"a non-string item":    {"tools": []any{"rg", float64(1)}},
			"a nested map":         {"lang": map[string]any{"a": "b"}},
			"a null":               {"lang": nil},
		} {
			vars, ok := module.DecodedVars(raw)
			Expect(ok).To(BeFalse(), why)
			Expect(vars).To(BeNil(), why)
		}
	})
})
