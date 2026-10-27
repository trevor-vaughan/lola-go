package module_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

var _ = Describe("Attribute files at load", func() {
	var root string
	BeforeEach(func() { root = filepath.Join(testenv.Isolate(), "mod") })
	load := func(files map[string]string) (*module.Module, error) {
		GinkgoHelper()
		Expect(os.RemoveAll(root)).To(Succeed())
		write(root, files)
		return module.Load(root, "kit", targets.IDs())
	}
	// refusal loads an agent whose attribute file, in syntax ext, holds body, and returns the
	// refusal's message.
	refusal := func(ext, body string) string {
		GinkgoHelper()
		_, err := load(map[string]string{"agents/reviewer.md": "---\ndescription: r\n---\nR.\n", "agents/reviewer.targets." + ext: body})
		Expect(err).To(HaveOccurred(), body)
		return err.Error()
	}

	It("reads <base>.targets.yaml beside a skill, command, agent and root skill into Attrs", func() {
		m, err := load(map[string]string{
			"SKILL.md":               "Root.\n",
			"SKILL.targets.yaml":     "opencode: {mode: primary}\n",
			"skills/review/SKILL.md": "Review.\n",
			"skills/review/SKILL.targets.yaml": "claude-code:\n  model: sonnet\n  tools: [Read, Grep]\n  n: 3\n  " +
				"on: true\n  off: null\n  nested: {a: [1, {b: c}]}\n",
			"commands/ship.md":            "Ship.\n",
			"commands/ship.targets.jsonc": "{\n  // ship\n  \"gemini-cli\": {\"description\": \"Ship it\",},\n}\n",
			"agents/critic.md.liquid":     "Critic.\n",
			"agents/critic.targets.yaml":  "claude-code: {}\n",
			"agents/plain.md":             "Plain.\n",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Warnings).To(BeEmpty())
		byName := map[string]module.Skill{}
		for _, s := range m.Skills {
			byName[s.Name] = s
		}
		Expect(byName["review"].Attrs).To(Equal(map[string]map[string]any{"claude-code": {
			"model": "sonnet", "tools": []any{"Read", "Grep"}, "n": int64(3), "on": true, "off": nil,
			"nested": map[string]any{"a": []any{int64(1), map[string]any{"b": "c"}}}}}))
		Expect(byName["kit"].Attrs).To(Equal(map[string]map[string]any{"opencode": {"mode": "primary"}}))
		Expect(byName["kit"].Exclude).To(ContainElement("SKILL.targets.yaml"))
		Expect(byName["review"].Exclude).To(ContainElement("SKILL.targets.yaml"))
		Expect(m.Commands[0].Attrs).To(Equal(map[string]map[string]any{"gemini-cli": {"description": "Ship it"}}))
		Expect(m.Commands[0].AttrsFile).To(Equal("commands/ship.targets.jsonc"))
		Expect(byName["kit"].AttrsFile).To(Equal("SKILL.targets.yaml"))
		Expect(m.Agents[0].Attrs).To(Equal(map[string]map[string]any{"claude-code": {}}), "a template has attributes too")
		Expect(m.Agents[1].Attrs).To(BeNil(), "no file, no attributes")
	})

	It("an attribute file refuses bad shapes at load, naming the line or pointer", func() {
		keys := func(n int, yaml bool) string {
			var b strings.Builder
			for i := range n {
				if yaml {
					fmt.Fprintf(&b, "  k%d: 1\n", i)
				} else {
					fmt.Fprintf(&b, "%s\"k%d\": 1", map[bool]string{true: "", false: ","}[i == 0], i)
				}
			}
			return b.String()
		}
		deep := func(n int, yaml bool) string {
			if yaml {
				return "claude-code:\n  a: " + strings.Repeat("[", n-2) + strings.Repeat("]", n-2) + "\n"
			}
			return `{"claude-code": {"a": ` + strings.Repeat("[", n-2) + strings.Repeat("]", n-2) + "}}"
		}
		for _, c := range []struct{ ext, body, want string }{
			{"yaml", "claude-code: {}\nnope: {}\n", "agents/reviewer.targets.yaml:2: nope: unknown target; the targets are claude-code, gemini-cli, opencode"},
			{"jsonc", "{\n  \"claude-code\": {},\n  \"nope\": {}\n}", "agents/reviewer.targets.jsonc:3: /nope: unknown target; the targets are claude-code, gemini-cli, opencode"},
			{"jsonc", `{"nope": {"a": ` + strings.Repeat("[", 20) + strings.Repeat("]", 20) + `}}`, "agents/reviewer.targets.jsonc:1: /nope: unknown target; the targets are claude-code, gemini-cli, opencode"},
			{"yaml", "claude-code:\n  name: other\n", "agents/reviewer.targets.yaml:2: claude-code.name: the name comes from the path"},
			{"jsonc", `{"claude-code": {"name": "other"}}`, "agents/reviewer.targets.jsonc:1: /claude-code/name: the name comes from the path"},
			{"yaml", "base: &b {model: x}\nclaude-code: *b\n", "agents/reviewer.targets.yaml:1: anchors, aliases and merge keys (<<) are not supported"},
			{"yaml", "claude-code:\n  <<: {model: x}\n", "agents/reviewer.targets.yaml:2: anchors, aliases and merge keys (<<) are not supported"},
			{"yaml", "claude-code:\n" + keys(65, true), "agents/reviewer.targets.yaml:2: claude-code: 65 keys; a target sets at most 64"},
			{"jsonc", `{"claude-code": {` + keys(65, false) + "}}", "agents/reviewer.targets.jsonc:1: /claude-code: 65 keys; a target sets at most 64"},
			{"yaml", "claude-code:\n" + keys(100, true), "agents/reviewer.targets.yaml:2: claude-code: 100 keys; a target sets at most 64"},
			{"jsonc", `{"claude-code": {` + keys(100, false) + "}}", "agents/reviewer.targets.jsonc:1: /claude-code: 100 keys; a target sets at most 64"},
			{"jsonc", `{"claude-code": {` + keys(99, false) + `, "k99": 0.5}}`, "agents/reviewer.targets.jsonc:1: /claude-code: 100 keys; a target sets at most 64"},
			{"yaml", deep(9, true), "agents/reviewer.targets.yaml:2: claude-code.a[0][0][0][0][0][0]: maps and lists nest at most 8 deep"},
			{"jsonc", deep(9, false), "agents/reviewer.targets.jsonc:1: /claude-code/a/0/0/0/0/0/0: maps and lists nest at most 8 deep"},
			{"yaml", "claude-code:\n  a: '" + strings.Repeat("x", 64<<10) + "'\n", fmt.Sprintf("agents/reviewer.targets.yaml: is %d bytes; an attribute file is at most 64 KiB", 64<<10+21)},
			{"jsonc", `{"claude-code": {"a": "` + strings.Repeat("x", 64<<10) + `"}}`, fmt.Sprintf("agents/reviewer.targets.jsonc: is %d bytes; an attribute file is at most 64 KiB", 64<<10+26)},
			{"yaml", "- claude-code\n", "agents/reviewer.targets.yaml:1: an attribute file maps target ids to frontmatter keys"},
			{"jsonc", `{"claude-code": [1]}`, "agents/reviewer.targets.jsonc:1: /claude-code: a target's attributes are a map of frontmatter keys"},
			{"jsonc", "// c\n[]", "agents/reviewer.targets.jsonc:2: an attribute file maps target ids to frontmatter keys"},
			{"jsonc", `"claude-code"`, "agents/reviewer.targets.jsonc:1: an attribute file maps target ids to frontmatter keys"},
			{"yaml", "claude-code: sonnet\n", "agents/reviewer.targets.yaml:1: claude-code: a target's attributes are a map of frontmatter keys"},
			{"yaml", "claude-code:\n  1: x\n", "agents/reviewer.targets.yaml:2: claude-code: a key must be text; quote it"},
			{"yaml", "claude-code:\n  '': x\n", "agents/reviewer.targets.yaml:2: claude-code.: a key cannot be empty"},
			{"jsonc", `{"claude-code": {"` + strings.Repeat("k", 257) + `": 1}}`, "agents/reviewer.targets.jsonc:1: " + untrusted.Text("/claude-code/"+strings.Repeat("k", 257), untrusted.MaxName) + ": a key is at most 256 bytes"},
			{"yaml", "claude-code:\n  \"a\\u200eb\": 1\n", "agents/reviewer.targets.yaml:2: " + untrusted.Text("claude-code.a\u200eb", untrusted.MaxName) + ": a key holds a control or invisible character"},
			{"jsonc", `{"claude-code": {"a": 1,}`, "agents/reviewer.targets.jsonc: does not parse: hujson: line 1, column 26: parsing object after value: unexpected EOF"},
		} {
			Expect(refusal(c.ext, c.body)).To(Equal(c.want), c.body)
		}
	})

	It("a .targets.jsonc file is read as a .targets.yaml one, and both beside one component refuse", func() {
		yaml := "claude-code:\n  model: sonnet\n  tools: [Read]\n  max: 5\n  quiet: false\n  gone: null\nopencode: {mode: subagent}\n"
		jsonc := "{\n  \"claude-code\": {\"model\": \"sonnet\", \"tools\": [\"Read\"], \"max\": 5, \"quiet\": false, \"gone\": null}, // c\n  /* o */ \"opencode\": {\"mode\": \"subagent\"},\n}\n"
		a, err := load(map[string]string{"agents/reviewer.md": "R.\n", "agents/reviewer.targets.yaml": yaml})
		Expect(err).NotTo(HaveOccurred())
		b, err := load(map[string]string{"agents/reviewer.md": "R.\n", "agents/reviewer.targets.jsonc": jsonc})
		Expect(err).NotTo(HaveOccurred())
		Expect(b.Agents[0].Attrs).To(Equal(a.Agents[0].Attrs))
		Expect(a.Agents[0].Attrs["claude-code"]).To(HaveKeyWithValue("max", int64(5)))

		a, err = load(map[string]string{"agents/reviewer.md": "R.\n", "agents/reviewer.targets.yaml": "# only a comment\n"})
		Expect(err).NotTo(HaveOccurred())
		b, err = load(map[string]string{"agents/reviewer.md": "R.\n", "agents/reviewer.targets.jsonc": "// only\n/* comments */\n"})
		Expect(err).NotTo(HaveOccurred(), "a JSONC file of only comments is {}, as a YAML one is")
		Expect(b.Agents[0].Attrs).To(Equal(a.Agents[0].Attrs))
		Expect(b.Agents[0].Attrs).To(BeEmpty())
		Expect(refusal("jsonc", "// c\n/* open\n")).To(HavePrefix("agents/reviewer.targets.jsonc: does not parse: "), "a comment that does not end")

		_, err = load(map[string]string{"agents/reviewer.md": "R.\n", "agents/reviewer.targets.yaml": yaml, "agents/reviewer.targets.jsonc": jsonc})
		Expect(err).To(MatchError("agents/reviewer: both reviewer.targets.jsonc and reviewer.targets.yaml exist; keep one"))
		_, err = load(map[string]string{"skills/s/SKILL.md": "S.\n", "skills/s/SKILL.targets.yaml": "{}\n", "skills/s/SKILL.targets.jsonc": "{}"})
		Expect(err).To(MatchError("skills/s/SKILL: both SKILL.targets.jsonc and SKILL.targets.yaml exist; keep one"))
	})

	It(".targets.json and .targets.yml beside a component warn and are not read", func() {
		m, err := load(map[string]string{
			"agents/reviewer.md": "R.\n", "agents/reviewer.targets.json": `{"claude-code": {"model": "x"}}`,
			"commands/ship.md": "S.\n", "commands/ship.targets.yml": "nope: {}\n",
			"skills/s/SKILL.md": "S.\n", "skills/s/SKILL.targets.JSON": "{}",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Agents[0].Attrs).To(BeNil())
		Expect(m.Commands[0].Attrs).To(BeNil())
		Expect(m.Skills[0].Exclude).To(ContainElement("SKILL.targets.JSON"), "never copied with the skill")
		Expect(m.Warnings).To(ConsistOf(
			"agents/reviewer.targets.json is not read; name it agents/reviewer.targets.yaml or agents/reviewer.targets.jsonc",
			"commands/ship.targets.yml is not read; name it commands/ship.targets.yaml or commands/ship.targets.jsonc",
			"skills/s/SKILL.targets.JSON is not read; name it skills/s/SKILL.targets.yaml or skills/s/SKILL.targets.jsonc",
		))
	})

	It("a case variant of an attribute file name is not read, warns, and is excluded from the skill", func() {
		m, err := load(map[string]string{
			"skills/s/SKILL.md": "S.\n", "skills/s/skill.targets.yaml": "nope: {}\n",
			"SKILL.md": "Root.\n", "Skill.targets.JSONC": "{}",
			"agents/reviewer.md": "R.\n", "agents/Reviewer.targets.yaml": "nope: {}\n",
		})
		Expect(err).NotTo(HaveOccurred())
		for _, s := range m.Skills {
			Expect(s.Attrs).To(BeNil())
		}
		Expect(m.Skills[0].Exclude).To(ContainElement("Skill.targets.JSONC"))
		Expect(m.Skills[1].Exclude).To(ContainElement("skill.targets.yaml"))
		Expect(m.Warnings).To(ConsistOf(
			"skills/s/skill.targets.yaml is not read; name it skills/s/SKILL.targets.yaml",
			"Skill.targets.JSONC is not read; name it SKILL.targets.jsonc",
			"agents/Reviewer.targets.yaml is not read; name it agents/reviewer.targets.yaml",
		))
	})

	It("attribute values decode by tag: a date, binary, a non-finite number and a custom tag refuse in YAML; a non-finite number and a key twice refuse in JSONC", func() {
		for body, want := range map[string]string{
			"claude-code:\n  model: x\n  since: 2024-01-02\n": "agents/reviewer.targets.yaml:3: claude-code.since: a date is not supported; quote it to make it a string",
			"claude-code:\n  t: .nan\n":                       "agents/reviewer.targets.yaml:2: claude-code.t: NaN and infinity are not supported",
			"claude-code:\n  t: [-.inf]\n":                    "agents/reviewer.targets.yaml:2: claude-code.t[0]: NaN and infinity are not supported",
			"claude-code:\n  t: 1e999\n":                      "agents/reviewer.targets.yaml:2: claude-code.t: NaN and infinity are not supported",
			"claude-code:\n  t: !!float x\n":                  "agents/reviewer.targets.yaml:2: claude-code.t: not a number; remove the !!float tag to make it a string",
			"claude-code:\n  n: 99999999999999999999999\n":    "agents/reviewer.targets.yaml:2: claude-code.n: a number beyond 64 bits is not supported; quote it to make it a string",
			"claude-code:\n  b: !!binary aGk=\n":              "agents/reviewer.targets.yaml:2: claude-code.b: binary data is not supported; write it as a string",
			"claude-code:\n  c: !mine x\n":                    "agents/reviewer.targets.yaml:2: claude-code.c: the tag !mine is not supported; remove it",
			"claude-code:\n  n: 9223372036854775808\n":        "agents/reviewer.targets.yaml:2: claude-code.n: a number beyond 64 bits is not supported; quote it to make it a string",
			"claude-code:\n  n: !!int -0x8000000000000001\n":  "agents/reviewer.targets.yaml:2: claude-code.n: a number beyond 64 bits is not supported; quote it to make it a string",
			"claude-code:\n  n: !!int abc\n":                  "agents/reviewer.targets.yaml:2: claude-code.n: not a whole number; remove the !!int tag to make it a string",
			"claude-code:\n  n: !!int 1.5\n":                  "agents/reviewer.targets.yaml:2: claude-code.n: not a whole number; remove the !!int tag to make it a string",
			"claude-code:\n  c: !mine {a: 1}\n":               "agents/reviewer.targets.yaml:2: claude-code.c: the tag !mine is not supported; remove it",
			"claude-code:\n  s: !!set {a: null}\n":            "agents/reviewer.targets.yaml:2: claude-code.s: the tag !!set is not supported; remove it",
			"claude-code:\n  l: !mine [1]\n":                  "agents/reviewer.targets.yaml:2: claude-code.l: the tag !mine is not supported; remove it",
			"claude-code:\n  o: !!omap [{a: 1}]\n":            "agents/reviewer.targets.yaml:2: claude-code.o: the tag !!omap is not supported; remove it",
			"claude-code: !mine {}\n":                         "agents/reviewer.targets.yaml:1: claude-code: the tag !mine is not supported; remove it",
			"!mine {claude-code: {}}\n":                       "agents/reviewer.targets.yaml:1: the tag !mine is not supported; remove it",
			"claude-code:\n  b: !!bool \"\\e[31mx\"\n":        "agents/reviewer.targets.yaml:2: claude-code.b: a boolean is true or false",
			"claude-code:\n  a: [1\n":                         "agents/reviewer.targets.yaml:1: does not parse: did not find expected ',' or ']'",
			"claude-code:\n  a: 1\n  a: 2\n":                  "agents/reviewer.targets.yaml:3: the key a is given twice",
		} {
			Expect(refusal("yaml", body)).To(Equal(want), body)
		}
		for body, want := range map[string]string{
			"{\n  // c\n  \"claude-code\": {\n    \"t\": 1e999,\n  },\n}\n": "agents/reviewer.targets.jsonc:4: /claude-code/t: NaN and infinity are not supported",
			"{\n  \"claude-code\": {\n    \"a\": 1,\n    \"a\": 2\n  }\n}":  "agents/reviewer.targets.jsonc:4: /claude-code/a: the key is given twice",
			`{"claude-code": {"temperature": -1e400}}`:                      "agents/reviewer.targets.jsonc:1: /claude-code/temperature: NaN and infinity are not supported",
			`{"claude-code": {"n": 9223372036854775808}}`:                   "agents/reviewer.targets.jsonc:1: /claude-code/n: a number beyond 64 bits is not supported; quote it to make it a string",
			`{"claude-code": {"a": 1, "a": 2}}`:                             "agents/reviewer.targets.jsonc:1: /claude-code/a: the key is given twice",
			`{"claude-code": {}, "claude-code": {}}`:                        "agents/reviewer.targets.jsonc:1: /claude-code: the key is given twice",
			`{"claude-code": {"a/b~": {"c": 1, "c": 2}}}`:                   "agents/reviewer.targets.jsonc:1: /claude-code/a~1b~0/c: the key is given twice",
			`{"claude-code": {}} {"opencode": {}}`:                          "agents/reviewer.targets.jsonc: does not parse: hujson: line 1, column 21: invalid character '{' after top-level value",
		} {
			Expect(refusal("jsonc", body)).To(Equal(want), body)
		}
		m, err := load(map[string]string{"agents/reviewer.md": "R.\n", "agents/reviewer.targets.yaml": "claude-code:\n  since: '2024-01-02'\n  n: 0x10\n  s: !!str 5\n  m: !!map {a: !!seq [1]}\n"})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Agents[0].Attrs["claude-code"]).To(Equal(map[string]any{"since": "2024-01-02", "n": int64(16), "s": "5", "m": map[string]any{"a": []any{int64(1)}}}),
			"a quoted date is a string; the default tags of a map and a list are allowed")
		m, err = load(map[string]string{"agents/reviewer.md": "R.\n", "agents/reviewer.targets.yaml": "claude-code:\n  t: 0.5\n  one: 1.0\n  n: 1\n"})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Agents[0].Attrs["claude-code"]).To(Equal(map[string]any{"t": 0.5, "one": 1.0, "n": int64(1)}), "a finite float accepts; 1.0 is a float")
		m, err = load(map[string]string{"agents/reviewer.md": "R.\n", "agents/reviewer.targets.jsonc": `{"claude-code": {"t": 0.5, "e": 1e2, "n": 1}}`})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Agents[0].Attrs["claude-code"]).To(Equal(map[string]any{"t": 0.5, "e": 100.0, "n": int64(1)}), "a JSONC fraction or exponent is a float")
	})

	It("attribute strings refuse control characters, and lists and maps over 4096 refuse at every depth, in YAML and JSONC", func() {
		items := func(n int, f string) string {
			parts := make([]string, n)
			for i := range parts {
				parts[i] = fmt.Sprintf(f, i)
			}
			return strings.Join(parts, ", ")
		}
		for _, c := range []struct{ ext, body, want string }{
			{"yaml", "claude-code:\n  a: \"x\\u0007y\"\n", "agents/reviewer.targets.yaml:2: claude-code.a: holds a control character"},
			{"yaml", "claude-code:\n  a: [ok, \"\\u0085\"]\n", "agents/reviewer.targets.yaml:2: claude-code.a[1]: holds a control character"},
			{"jsonc", `{"claude-code": {"a": {"b": "x\u007fy"}}}`, "agents/reviewer.targets.jsonc:1: /claude-code/a/b: holds a control character"},
			{"yaml", "claude-code:\n  a: [" + items(4097, "%d") + "]\n", "agents/reviewer.targets.yaml:2: claude-code.a: 4097 items; a list holds at most 4096"},
			{"jsonc", `{"claude-code": {"a": [[` + items(4097, "%d") + `]]}}`, "agents/reviewer.targets.jsonc:1: /claude-code/a/0: 4097 items; a list holds at most 4096"},
			{"yaml", "claude-code:\n  a: [{" + items(4097, "k%d: 1") + "}]\n", "agents/reviewer.targets.yaml:2: claude-code.a[0]: 4097 keys; a map holds at most 4096"},
			{"jsonc", `{"claude-code": {"a": {` + items(4097, `"k%d": 1`) + `}}}`, "agents/reviewer.targets.jsonc:1: /claude-code/a: 4097 keys; a map holds at most 4096"},
			{"yaml", "claude-code:\n  a: [" + items(5000, "%d") + "]\n", "agents/reviewer.targets.yaml:2: claude-code.a: 5000 items; a list holds at most 4096"},
			{"jsonc", `{"claude-code": {"a": [` + items(5000, "%d") + `]}}`, "agents/reviewer.targets.jsonc:1: /claude-code/a: 5000 items; a list holds at most 4096"},
			{"jsonc", `{"claude-code": {"a": {` + items(5000, `"k%d": 1`) + `}}}`, "agents/reviewer.targets.jsonc:1: /claude-code/a: 5000 keys; a map holds at most 4096"},
			{"yaml", "claude-code:\n  a: \"" + strings.Repeat("x", 16<<10+1) + "\"\n", "agents/reviewer.targets.yaml:2: claude-code.a: longer than 16 KiB"},
			{"jsonc", "{\"claude-code\": {\"a\": \"x\xffy\"}}", "agents/reviewer.targets.jsonc:1: is not UTF-8; an attribute file must be UTF-8 text"},
		} {
			Expect(refusal(c.ext, c.body)).To(Equal(c.want), c.body)
		}
		m, err := load(map[string]string{"agents/reviewer.md": "R.\n", "agents/reviewer.targets.jsonc": `{"claude-code": {"a": "tab\there\nline", "l": [` + items(4096, "%d") + `]}}`})
		Expect(err).NotTo(HaveOccurred(), "tab and line feed are allowed, and 4096 items")
		Expect(m.Agents[0].Attrs["claude-code"]["a"]).To(Equal("tab\there\nline"))
	})
})
