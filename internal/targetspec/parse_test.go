package targetspec_test

import (
	"errors"
	"strings"
	"testing/fstest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/targetspec"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

const base = `id: my-tool
probe:
  - exec: {binaries: [mytool], args: [--version], match: '(\d+\.\d+\.\d+)'}
roots: {project: "{project}", user: "{config_home}/mytool"}
detect:
  project: {any: [.mytool/, MYTOOL.md]}
  user: {dir: .}
eras:
  - since: "1.0"
    vars: {dir: {project: .mytool/, user: ""}}
    layout:
      skill: {mech: skill-dir, path: "{dir}skills"}
      command: {mech: markdown-file, path: "{dir}commands", ext: .md, sidecar: drop}
      mcp: {mech: json-servers, file: {first: [mytool.jsonc, mytool.json]}, pointer: /mcp, shape: opencode}
    kinds:
      command:
        unknown_keys: drop
        keys:
          description: {accepts: {shape: string}}
          hooks: {unsupported: "mytool runs no hooks", case_insensitive: true}
    behaviors:
      - {id: shell-fence, kind: command, detect: '(?m)^` + "```" + `!', effect: sent-as-text, what: fenced shell, alternative: "inline !` + "`…`" + `"}
`

func loadErr(src string) *targetspec.Error {
	_, err := targetspec.Parse("my.yaml", []byte(src))
	var e *targetspec.Error
	ExpectWithOffset(1, errors.As(err, &e)).To(BeTrue(), "want a *targetspec.Error, got %v", err)
	ExpectWithOffset(1, e.File).To(Equal("my.yaml"))
	ExpectWithOffset(1, e.Fix).NotTo(BeEmpty())
	return e
}

// withLayout adds a layout entry after mcp, then applies old/new pairs.
func withLayout(entry string, pairs ...string) string {
	src := edit("    kinds:\n", "      "+entry+"\n    kinds:\n")
	for i := 0; i < len(pairs); i += 2 {
		ExpectWithOffset(1, src).To(ContainSubstring(pairs[i]))
		src = strings.Replace(src, pairs[i], pairs[i+1], 1)
	}
	return src
}

// fileProbe is base with a second probe entry, file: f.
func fileProbe(f string) string { return edit("\nroots:", "\n  - file: "+f+"\nroots:") }

func edit(old, new string) string {
	ExpectWithOffset(1, base).To(ContainSubstring(old))
	return strings.Replace(base, old, new, 1)
}

var _ = Describe("Parse", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("decodes a valid spec and expands each scope's layout", func() {
		s, err := targetspec.Parse("my.yaml", []byte(base))
		Expect(err).NotTo(HaveOccurred())
		Expect(s.ID).To(Equal("my-tool"))
		Expect(s.Newest().Layout(false)["skill"].Path).To(Equal(".mytool/skills"))
		Expect(s.Newest().Layout(true)["skill"].Path).To(Equal("skills"))
		Expect(s.Newest().Layout(true)["mcp"].File.First).To(Equal([]string{"mytool.jsonc", "mytool.json"}))
		Expect(s.Probe[0].Exec.Match.RE().FindStringSubmatch("v1.2.3")[1]).To(Equal("1.2.3"))
		Expect(s.Newest().Behaviors[0].Detect.RE()).NotTo(BeNil())
		Expect(s.Root(true, targetspec.RootVars{ConfigHome: "/c/"})).To(Equal("/c/mytool"))
		Expect(s.Root(false, targetspec.RootVars{Project: "/p"})).To(Equal("/p"))
	})

	DescribeTable("rejects, naming the field",
		func(src, field, problem string) {
			e := loadErr(src)
			Expect(e.Field).To(Equal(field))
			Expect(e.Problem).To(ContainSubstring(problem))
		},
		Entry("a bad id", edit("id: my-tool", "id: My_Tool"), "id", "not a target id"),
		Entry("a layout path with ..", edit(`path: "{dir}skills"`, `path: "{dir}../skills"`), "eras[0].layout.skill.path", "escapes the target root"),
		Entry("an absolute layout path", edit(`path: "{dir}skills"`, `path: /etc/skills`), "eras[0].layout.skill.path", "escapes the target root"),
		Entry("{home} in a layout path", edit(`path: "{dir}skills"`, `path: "{home}/skills"`), "eras[0].layout.skill.path", "{home}"),
		Entry("a layout path empty at one scope", edit(`path: "{dir}skills"`, `path: "{dir}"`), "eras[0].layout.skill.path", "escapes the target root"),
		Entry("an unknown mechanism", edit("mech: skill-dir", "mech: skill-dirs"), "eras[0].layout.skill.mech", "unknown mechanism skill-dirs"),
		Entry("a mechanism for another kind", edit("mech: skill-dir", "mech: toml-command"), "eras[0].layout.skill.mech", "does not place skills"),
		Entry("a field the mechanism lacks", edit(`path: "{dir}skills"}`, `path: "{dir}skills", pointer: /x}`), "eras[0].layout.skill.pointer", "not a field of skill-dir"),
		Entry("a missing required field", edit(", ext: .md, sidecar: drop}", ", sidecar: drop}"), "eras[0].layout.command.ext", "is missing"),
		Entry("a command without sidecar", edit(", sidecar: drop}", "}"), "eras[0].layout.command.sidecar", "copy or drop"),
		Entry("an unknown effect", edit("effect: sent-as-text", "effect: ignored"), "eras[0].behaviors[0].effect", "unknown effect ignored"),
		Entry("sent-as-text without a fix", edit(`, alternative: "inline !`+"`…`"+`"}`, "}"), "eras[0].behaviors[0]", "exactly one of native: or alternative:"),
		Entry("an invalid regex", edit(`match: '(\d+\.\d+\.\d+)'`, `match: '(\d+'`), "probe[0].exec.match", "not a valid regular expression"),
		Entry("a probe regex without a group", edit(`match: '(\d+\.\d+\.\d+)'`, `match: '\d+'`), "probe[0].exec.match", "no group"),
		Entry("a binary with a path", edit("binaries: [mytool]", "binaries: [/bin/mytool]"), "probe[0].exec.binaries[0]", "not a command name"),
		Entry("a probe with no method", edit("  - exec: {binaries: [mytool], args: [--version], match: '(\\d+\\.\\d+\\.\\d+)'}\n", "  - {}\n"), "probe[0]", "has no method"),
		Entry("a probe with two methods", edit("  - exec:", "  - file: {path: \"{home}/.t/v\", pointer: /v}\n    exec:"), "probe[0]", "has two methods"),
		Entry("an empty exec binaries list", edit("binaries: [mytool]", "binaries: []"), "probe[0].exec.binaries", "is empty"),
		Entry("a second probe's bad exec", edit("\nroots:", "\n  - exec: {binaries: [x], match: '\\d+'}\nroots:"), "probe[1].exec.match", "no group"),
		Entry("a file probe with no path", fileProbe(`{pointer: /v}`), "probe[1].file.path", "is empty"),
		Entry("a file probe path without a variable", fileProbe(`{path: .t/v.json, pointer: /v}`), "probe[1].file.path", "does not start with"),
		Entry("a file probe path under the project variable", fileProbe(`{path: "{project}/v.json", pointer: /v}`), "probe[1].file.path", "does not start with"),
		Entry("a file probe path with ..", fileProbe(`{path: "{home}/../v.json", pointer: /v}`), "probe[1].file.path", "does not start with"),
		Entry("a file probe path that is only the variable", fileProbe(`{path: "{home}", pointer: /v}`), "probe[1].file.path", "does not start with"),
		Entry("a file probe with match and pointer", fileProbe(`{path: "{home}/v", pointer: /v, match: '(\d+)'}`), "probe[1].file", "sets both match and pointer"),
		Entry("a file probe with neither", fileProbe(`{path: "{home}/v"}`), "probe[1].file", "has neither match nor pointer"),
		Entry("a pointer without a leading slash", fileProbe(`{path: "{home}/v", pointer: version}`), "probe[1].file.pointer", "is not a JSON pointer"),
		Entry("a pointer with a bare tilde", fileProbe(`{path: "{home}/v", pointer: /a~2}`), "probe[1].file.pointer", "is not a JSON pointer"),
		Entry("a pointer ending in a tilde", fileProbe(`{path: "{home}/v", pointer: /a~}`), "probe[1].file.pointer", "is not a JSON pointer"),
		Entry("an invalid file match", fileProbe(`{path: "{home}/v", match: '(\d+'}`), "probe[1].file.match", "not a valid regular expression"),
		Entry("a file match without a group", fileProbe(`{path: "{home}/v", match: '\d+'}`), "probe[1].file.match", "no group"),
		Entry("no exec probe and no readers", edit("probe:\n  - exec: {binaries: [mytool], args: [--version], match: '(\\d+\\.\\d+\\.\\d+)'}\n", "probe:\n  - file: {path: \"{home}/v\", pointer: /v}\n"), "readers", "is empty for a target with no exec probe"),
		Entry("readers beside a probe", edit("id: my-tool", "id: my-tool\nreaders: x"), "readers", "no exec probe"),
		Entry("a root not under a variable", edit(`user: "{config_home}/mytool"`, `user: /etc/mytool`), "roots.user", "under a root variable"),
		Entry("a variable glued to .. in the user root", edit(`user: "{config_home}/mytool"`, `user: "{home}.."`), "roots.user", "under a root variable"),
		Entry("a variable followed by a parent path", edit(`user: "{config_home}/mytool"`, `user: "{home}../x"`), "roots.user", "under a root variable"),
		Entry("two variables back to back", edit(`user: "{config_home}/mytool"`, `user: "{home}{config_home}"`), "roots.user", "under a root variable"),
		Entry("text glued to a variable", edit(`user: "{config_home}/mytool"`, `user: "{config_home}x"`), "roots.user", "under a root variable"),
		Entry("a variable after the slash", edit(`user: "{config_home}/mytool"`, `user: "{home}/{config_home}"`), "roots.user", "under a root variable"),
		Entry("text glued to the project variable", edit(`project: "{project}"`, `project: "{project}x"`), "roots.project", "under a root variable"),
		Entry("{home} in the project root", edit(`project: "{project}"`, `project: "{home}"`), "roots.project", "{home}"),
		Entry("a detection marker with ..", edit("any: [.mytool/, MYTOOL.md]", "any: [../x]"), "detect.project.any[0]", "escapes the target root"),
		Entry("both any and dir", edit("user: {dir: .}", "user: {dir: ., any: [x]}"), "detect.user", "exactly one of any: or dir:"),
		Entry("an unknown shape", edit("shape: string}", "shape: text}"), "eras[0].kinds.command.keys.description.accepts.shape", "not a shape"),
		Entry("a key with two facts", edit(`{unsupported: "mytool runs no hooks", case_insensitive: true}`, `{unsupported: "x", implied_by: file-name}`), "eras[0].kinds.command.keys.hooks", "exactly one of"),
		Entry("folded keys equal ignoring case", edit("          hooks:", "          Hooks: {unsupported: x, case_insensitive: true}\n          hooks:"), "eras[0].kinds.command.keys.hooks", "ignoring case"),
		Entry("kinds for a kind not placed", edit("    kinds:\n", "    kinds:\n      agent: {unknown_keys: keep}\n"), "eras[0].kinds.agent", "does not place"),
		Entry("absent_means without a fix", edit(`{unsupported: "mytool runs no hooks", case_insensitive: true}`, `{unsupported: x, absent_means: unrestricted}`), "eras[0].kinds.command.keys.hooks", "exactly one of alternative: or example:"),
		Entry("an unknown shared fragment", edit("    behaviors:\n", "    shared: [rules]\n    behaviors:\n"), "eras[0].shared[0]", "unknown fragment rules"),
		Entry("a malformed since", edit(`since: "1.0"`, `since: "one"`), "eras[0].since", "not a version"),
		Entry("extends on the first era", edit(`since: "1.0"`, `since: "1.0"`+"\n    extends: previous"), "eras[0].extends", "no previous era"),
	)

	DescribeTable("rejects a layout entry lola cannot honour, naming the field",
		func(src, field, problem string) {
			e := loadErr(src)
			Expect(e.Field).To(Equal(field))
			Expect(e.Problem).To(ContainSubstring(problem))
		},
		Entry("arguments with hook_settings", edit("sidecar: drop}", "sidecar: copy, arguments: one-based, hook_settings: s.json}"), "eras[0].layout.command.arguments", "not honoured with hook_settings"),
		Entry("defaults with hook_settings", edit("sidecar: drop}", "sidecar: copy, defaults: {mode: x}, hook_settings: s.json}"), "eras[0].layout.command.defaults", "not honoured with hook_settings"),
		Entry("a dropped sidecar with hook_settings", edit("sidecar: drop}", "sidecar: drop, hook_settings: s.json}"), "eras[0].layout.command.sidecar", "not honoured with hook_settings"),
		Entry("name_matches_dir with hook_settings", edit(`path: "{dir}skills"}`, `path: "{dir}skills", name_matches_dir: true, hook_settings: s.json}`), "eras[0].layout.skill.name_matches_dir", "not honoured with hook_settings"),
		Entry("prepend_name on a skill with hook_settings", edit(`path: "{dir}skills"}`, `path: "{dir}skills", prepend_name: true, hook_settings: s.json}`), "eras[0].layout.skill.prepend_name", "not honoured with hook_settings"),
		Entry("prepend_name on a command with hook_settings", edit("sidecar: drop}", "sidecar: copy, prepend_name: true, hook_settings: s.json}"), "eras[0].layout.command.prepend_name", "not honoured with hook_settings"),
		Entry("a runtime-hook file with candidates", withLayout(`runtime-hook: {mech: claude-hooks, file: {first: [a.json, b.json]}}`), "eras[0].layout.runtime-hook.file", "a plain path"),
		Entry("hook_settings without a runtime-hook", edit("sidecar: drop}", "sidecar: copy, hook_settings: s.json}"), "eras[0].layout.command.hook_settings", "no claude-hooks runtime-hook"),
		Entry("hook_settings other than the runtime-hook file", withLayout(`runtime-hook: {mech: claude-hooks, file: b.json}`, "sidecar: drop}", "sidecar: copy, hook_settings: a.json}"), "eras[0].layout.command.hook_settings", "is not the runtime-hook file b.json"),
		Entry("hook_settings that differs from the runtime-hook file at one scope", withLayout(`runtime-hook: {mech: claude-hooks, file: x.json}`, "sidecar: drop}", "sidecar: copy, hook_settings: \"{v}\"}", "vars: {dir:", "vars: {v: {project: x.json, user: y.json}, dir:"), "eras[0].layout.command.hook_settings", "is not the runtime-hook file x.json"),
		Entry("a single candidate", edit("{first: [mytool.jsonc, mytool.json]}", "{first: [only.json]}"), "eras[0].layout.mcp.file", "one candidate"),
		Entry("a file that is not text", edit("{first: [mytool.jsonc, mytool.json]}", "3"), "eras[0].layout.mcp.file", "not a path"),
		Entry("an empty candidate list", edit("{first: [mytool.jsonc, mytool.json]}", "{first: []}"), "eras[0].layout.mcp.file", "empty"),
		Entry("an empty required key", edit("    kinds:\n      command:\n", "    kinds:\n      command:\n        required: [\"\"]\n"), "eras[0].kinds.command.required[0]", "empty"),
		Entry("a required key that is unsupported", edit("    kinds:\n      command:\n", "    kinds:\n      command:\n        required: [hooks]\n"), "eras[0].kinds.command.required[0]", "also unsupported"),
		Entry("a required key that is unsupported, differing in case", edit("    kinds:\n      command:\n", "    kinds:\n      command:\n        required: [Hooks]\n"), "eras[0].kinds.command.required[0]", "also unsupported"),
	)

	DescribeTable("rejects a control or format character, naming the field",
		func(src, field string) {
			e := loadErr(src)
			Expect(e.Field).To(Equal(field))
			Expect(e.Problem).To(Equal("holds a control or format character"))
		},
		Entry("an escape in an unsupported reason", edit(`{unsupported: "mytool runs no hooks",`, `{unsupported: "quill \e[31mRED",`), "eras[0].kinds.command.keys.hooks.unsupported"),
		Entry("a bidi override in a behavior's what", edit("what: fenced shell,", `what: "x\u202Ey",`), "eras[0].behaviors[0].what"),
		Entry("a bell in a layout path", edit(`path: "{dir}skills"`, `path: "{dir}sk\aills"`), "eras[0].layout.skill.path"),
		Entry("a tab in readers", edit("probe:\n  - exec: {binaries: [mytool], args: [--version], match: '(\\d+\\.\\d+\\.\\d+)'}\n", "readers: \"skills\\tfor x\"\n"), "readers"),
		Entry("a newline in a frontmatter key's name", edit("          hooks:", "          \"ho\\nooks\": {accepts: {shape: string}}\n          hooks:"), "eras[0].kinds.command.keys.ho\nooks"),
		Entry("an escape in a pattern", edit("detect: '(?m)^```!'", `detect: "\e(?m)^`+"```"+`!"`), "eras[0].behaviors[0].detect"),
	)

	Describe("with a YAML alias", func() {
		anchored := func(tail string) string {
			return edit("probe:\n  - exec: {binaries: [mytool], args: [--version], match: '(\\d+\\.\\d+\\.\\d+)'}\n",
				"probe:\n  - file: {path: \"{home}/v\", match: &m \"v\\t(\\\\d+)\"}\n") + tail
		}

		It("refuses a tab anchored at a pattern field and aliased into readers", func() {
			e := loadErr(anchored("readers: *m\n"))
			Expect(e.Field).To(Equal("readers"))
			Expect(e.Problem).To(Equal("holds a control or format character"))
		})

		It("accepts the same alias in another pattern field", func() {
			s, err := targetspec.Parse("my.yaml", []byte(strings.Replace(anchored("readers: x\n"), "detect: '(?m)^```!'", "detect: *m", 1)))
			Expect(err).NotTo(HaveOccurred())
			Expect(s.Newest().Behaviors[0].Detect.RE().MatchString("v\t7")).To(BeTrue())
		})

		It("refuses a self-referencing alias before walking it", func() {
			_, err := targetspec.Parse("my.yaml", []byte(edit("binaries: [mytool]", "binaries: &a [*a]")))
			Expect(err).To(MatchError(ContainSubstring("cannot unmarshal !!seq into string")))
		})
	})

	It("coaches a control or format character with plain text", func() {
		Expect(loadErr(edit("what: fenced shell,", `what: "x\u202Ey",`)).Fix).To(Equal("remove it; specs hold plain text"))
	})

	It("loads an exec probe followed by a file probe", func() {
		s, err := targetspec.Parse("my.yaml", []byte(fileProbe(`{path: "{home}/.mytool/v.json", pointer: /version}`)))
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Probe).To(HaveLen(2))
		Expect(s.Probe[1].File.Pointer).To(Equal("/version"))
	})

	It("loads a file-only probe with readers, and a file match that holds a tab", func() {
		src := edit("probe:\n  - exec: {binaries: [mytool], args: [--version], match: '(\\d+\\.\\d+\\.\\d+)'}\n",
			"readers: x\nprobe:\n  - file: {path: \"{home}/v\", match: \"v\\t(\\\\d+)\"}\n")
		s, err := targetspec.Parse("my.yaml", []byte(src))
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Probe[0].File.Match.RE().FindStringSubmatch("v\t7")[1]).To(Equal("7"))
	})

	It("loads a pattern that matches a tab or a newline", func() {
		s, err := targetspec.Parse("my.yaml", []byte(edit("detect: '(?m)^```!'", `detect: "(?m)^\t\n`+"```"+`!"`)))
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Newest().Behaviors[0].Detect.RE().MatchString("\t\n```!")).To(BeTrue())
	})

	It("fixes what it rejects with the exact next step", func() {
		Expect(loadErr(edit("sidecar: drop}", "sidecar: copy, arguments: one-based, hook_settings: s.json}")).Fix).To(Equal(
			"remove arguments: with hook_settings lola renders the command as Claude Code reads it, which ignores arguments"))
		Expect(loadErr(edit("sidecar: drop}", "sidecar: copy, prepend_name: true, hook_settings: s.json}")).Fix).To(Equal(
			"remove prepend_name: with hook_settings lola renders the command as Claude Code reads it, which ignores prepend_name"))
		Expect(loadErr(withLayout(`runtime-hook: {mech: claude-hooks, file: {first: [a.json, b.json]}}`)).Fix).To(Equal("name one settings file: file: b.json"))
		Expect(loadErr(edit("sidecar: drop}", "sidecar: copy, hook_settings: s.json}")).Fix).To(Equal(
			"add runtime-hook: {mech: claude-hooks, file: s.json}, or remove hook_settings"))
		Expect(loadErr(withLayout(`runtime-hook: {mech: claude-hooks, file: b.json}`, "sidecar: drop}", "sidecar: copy, hook_settings: a.json}")).Fix).To(Equal(
			"set hook_settings to the runtime-hook file, b.json: consent for a component's hooks covers that file"))
		Expect(loadErr(edit("{first: [mytool.jsonc, mytool.json]}", "{first: [only.json]}")).Fix).To(Equal("write file: only.json"))
		Expect(loadErr(edit("{first: [mytool.jsonc, mytool.json]}", "{first: []}")).Fix).To(Equal("list the candidate files, or write file: <path>"))
	})

	It("loads hook_settings with the prepend_name and copied sidecar the mechanism honours", func() {
		src := withLayout("runtime-hook: {mech: claude-hooks, file: s.json}\n      agent: {mech: markdown-file, path: \"{dir}agents\", ext: .md, prepend_name: true, hook_settings: s.json}",
			"sidecar: drop}", "sidecar: copy, hook_settings: s.json}")
		_, err := targetspec.Parse("my.yaml", []byte(src))
		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects an unknown field, naming it", func() {
		e := loadErr(edit("id: my-tool", "id: my-tool\ncolour: red"))
		Expect(e.Problem).To(ContainSubstring("colour"))
	})

	It("rejects an empty file and a file with two documents", func() {
		Expect(loadErr("").Problem).To(ContainSubstring("empty"))
		Expect(loadErr(base + "---\nid: x\n").Problem).To(ContainSubstring("more than one YAML document"))
	})
})

var _ = Describe("ParseFS", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("parses every spec in name order and rejects two with one id", func() {
		fsys := fstest.MapFS{"d/b.yaml": {Data: []byte(base)}, "d/a.yaml": {Data: []byte(edit("id: my-tool", "id: a-tool"))},
			"d/notes.txt": {Data: []byte("x")}}
		specs, err := targetspec.ParseFS(fsys, "d", "src")
		Expect(err).NotTo(HaveOccurred())
		Expect([]string{specs[0].ID, specs[1].ID}).To(Equal([]string{"a-tool", "my-tool"}))

		fsys["d/c.yaml"] = &fstest.MapFile{Data: []byte(base)}
		_, err = targetspec.ParseFS(fsys, "d", "src")
		var e *targetspec.Error
		Expect(errors.As(err, &e)).To(BeTrue())
		Expect(e.File).To(Equal("src/c.yaml"))
		Expect(e.Field).To(Equal("id"))
		Expect(e.Problem).To(ContainSubstring("src/b.yaml"))
	})
})

var _ = Describe("Builtin", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("loads lola's built-in specs", func() {
		specs, err := targetspec.Builtin()
		Expect(err).NotTo(HaveOccurred())
		ids := []string{}
		for _, s := range specs {
			ids = append(ids, s.ID)
		}
		Expect(ids).To(ContainElement("agents"))
	})
})

const twoEras = base + `  - since: "2.0"
    extends: previous
    remove: [layout.mcp, kinds.command.keys.hooks]
    kinds:
      command: {keys: {model: {accepts: {shape: string}}}}
    behaviors:
      - {id: shell-fence, kind: command, detect: x, effect: sent-as-text, what: fenced shell, native: "!{…}"}
`

var _ = Describe("eras", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("copies the previous era, removes entries and replaces them per kind, key and behavior id", func() {
		s, err := targetspec.Parse("my.yaml", []byte(twoEras))
		Expect(err).NotTo(HaveOccurred())
		first, second := s.Eras[0], s.Eras[1]
		Expect(first.Layout(false)).To(HaveKey("mcp"))
		Expect(second.Layout(false)).NotTo(HaveKey("mcp"))
		Expect(second.Layout(false)).To(HaveKey("command"))
		Expect(second.Kinds["command"].Keys).To(HaveKey("description"))
		Expect(second.Kinds["command"].Keys).To(HaveKey("model"))
		Expect(second.Kinds["command"].Keys).NotTo(HaveKey("hooks"))
		Expect(first.Kinds["command"].Keys).To(HaveKey("hooks"), "the previous era is not changed")
		Expect(second.Behaviors).To(HaveLen(1))
		Expect(second.Behaviors[0].Native).To(Equal("!{…}"))
		Expect(s.Newest().Since).To(Equal("2.0"))
	})

	DescribeTable("rejects",
		func(old, new, field, problem string) {
			e := loadErr(strings.Replace(twoEras, old, new, 1))
			Expect(e.Field).To(Equal(field))
			Expect(e.Problem).To(ContainSubstring(problem))
		},
		Entry("eras out of order", `since: "2.0"`, `since: "0.9"`, "eras[1].since", "not after previous era"),
		Entry("an equal since", `since: "2.0"`, `since: "1.0.0"`, "eras[1].since", "not after previous era"),
		Entry("remove of a missing entry", "layout.mcp,", "layout.agent,", "eras[1].remove[0]", "not in the previous era"),
		Entry("a malformed remove", "layout.mcp,", "mcp,", "eras[1].remove[0]", "is not layout."),
		Entry("remove without extends", "    extends: previous\n", "", "eras[1].remove", "extends: previous"),
		Entry("an unknown extends", "extends: previous", "extends: first", "eras[1].extends", "not previous"),
	)
})
