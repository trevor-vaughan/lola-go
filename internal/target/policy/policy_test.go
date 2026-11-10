package policy_test

import (
	"fmt"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/target/policy"
	"github.com/trevor-vaughan/lola-go/internal/targetspec"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

const src = `id: tool
probe: [{exec: {binaries: [tool], args: [--version], match: '(\d+)'}}]
roots: {project: "{project}", user: "{home}"}
detect: {project: {any: [.tool/]}, user: {any: [.tool/]}}
eras:
  - since: "1"
    layout:
      skill: {mech: skill-dir, path: .tool/skills}
      command: {mech: markdown-file, path: .tool/commands, ext: .md, sidecar: drop}
      agent: {mech: markdown-file, path: .tool/agents, ext: .md}
      mcp: {mech: json-servers, file: tool.json, pointer: /mcp, shape: opencode}
    kinds:
      agent:
        unknown_keys: keep
        required: [description]
        name: '^[a-z]+$'
        keys:
          name: {implied_by: file-name}
          hooks: {unsupported: "tool runs no hooks from frontmatter", case_insensitive: true}
          color: {accepts: {shape: string, pattern: '^#[0-9a-f]{6}$'}}
          tools: {unsupported: "tool configures tools with permissions", absent_means: unrestricted, alternative: permission}
          skills: {accepts: {shape: string-list}, absent_means: unrestricted, example: read_file}
    behaviors:
      - {id: shell-fence, kind: command, detect: x, effect: sent-as-text, what: fenced shell, alternative: "inline !` + "`…`" + `"}
      - {id: shell-native, kind: command, detect: y, effect: sent-as-text, what: "!` + "`…`" + `", native: "!{…}"}
      - {id: shell-unchecked, kind: command, detect: z, effect: runs-without-permission-check, what: "!` + "`…`" + `"}
    shared: [skill-rules]
`

var _ = Describe("Capabilities", func() {
	var caps target.Capabilities
	BeforeEach(func() {
		testenv.Isolate()
		s, err := targetspec.Parse("tool.yaml", []byte(src))
		Expect(err).NotTo(HaveOccurred())
		mech := target.ContentRule{Kind: "command", Pattern: regexp.MustCompile("m"), Code: "tool/mech"}
		caps = policy.Capabilities("tool", s.Newest(), []target.ContentRule{mech})
	})

	It("declares every kind the layout places, and only those", func() {
		Expect(caps.Kinds).To(HaveLen(4))
		Expect(caps.Kinds).To(HaveKey("mcp"))
		Expect(caps.Kinds["mcp"].Keys).To(BeNil())
	})

	It("maps kind facts", func() {
		k := caps.Kinds["agent"]
		Expect(k.Open).To(BeTrue())
		Expect(caps.Kinds["command"].Open).To(BeFalse())
		Expect(k.Required).To(Equal([]string{"description"}))
		Expect(k.Name.String()).To(Equal("^[a-z]+$"))
	})

	It("maps key facts to values", func() {
		k := caps.Kinds["agent"].Keys
		Expect(k["name"]).To(Equal(target.Value{Drop: target.SeverityInfo, Note: "the file name names the agent"}))
		Expect(k["hooks"]).To(Equal(target.Value{Drop: target.SeverityWarn, Fold: true, Note: "tool runs no hooks from frontmatter"}))
		Expect(k["color"].Shape).To(Equal(target.ShapeString))
		Expect(k["color"].Pattern.String()).To(Equal("^#[0-9a-f]{6}$"))
		Expect(k["tools"].Drop).To(Equal(target.SeveritySkip))
		Expect(*k["tools"].Guard).To(Equal(target.Finding{Code: "agent-tools-unenforceable",
			Text: "tool cannot enforce this agent's tools; without it the agent is unrestricted",
			Hint: "under tool: in the agent's .targets.yaml, set tools: null and restrict the agent with permission:"}))
		Expect(k["skills"].Drop).To(BeEmpty())
		Expect(k["skills"].Shape).To(Equal(target.ShapeStringList))
		Expect(k["skills"].Guard.Hint).To(Equal("under tool: in the agent's .targets.yaml, list its skills by tool's names, such as read_file"))
	})

	It("orders rules: behaviors, then the mechanisms', then shared fragments", func() {
		var got []string
		for _, r := range caps.Rules {
			got = append(got, r.Code)
		}
		Expect(got).To(Equal([]string{"tool/shell-fence", "tool/shell-native", "tool/shell-unchecked", "tool/mech",
			"skill-shell-dropped", "skill-arguments-dropped"}))
	})

	It("names the construct, not shell, in the native hint", func() {
		inline := strings.Replace(src, "    shared: [skill-rules]",
			"      - {id: inline, kind: command, detect: q, effect: sent-as-text, what: inline expansion, native: \"@{…}\"}\n    shared: [skill-rules]", 1)
		s, err := targetspec.Parse("quill.yaml", []byte(strings.Replace(inline, "id: tool", "id: quill", 1)))
		Expect(err).NotTo(HaveOccurred())
		var hint string
		for _, r := range policy.Capabilities("quill", s.Newest(), nil).Rules {
			if r.Code == "quill/inline" {
				hint = r.Hint
			}
		}
		Expect(hint).To(HavePrefix("rewrite inline expansion as quill's @{…}"))
	})

	It("builds behavior findings from the effect", func() {
		fence, native, unchecked := caps.Rules[0], caps.Rules[1], caps.Rules[2]
		Expect(fence.Severity).To(Equal(target.SeveritySkip))
		Expect(fence.Forceable).To(BeTrue())
		Expect(fence.Text).To(Equal("tool does not run fenced shell; the command would send it to the model as text"))
		Expect(fence.Hint).To(Equal("use inline !`…`"))
		Expect(native.Hint).To(Equal("rewrite !`…` as tool's !{…} in the command's .md.liquid template, inside {% if target == \"tool\" %}…{% else %}!`…`{% endif %}"))
		Expect(unchecked.Severity).To(Equal(target.SeverityWarn))
		Expect(unchecked.Forceable).To(BeFalse())
		Expect(unchecked.Text).To(Equal("tool runs !`…` without Claude's permission checks (allowed-tools is dropped)"))
		Expect(unchecked.Hint).To(Equal("if the shell needs Claude's permission checks, wrap it in {% if target != \"tool\" %}…{% endif %} in the command's .md.liquid template"))
		Expect(unchecked.Pattern.String()).To(Equal("z"))
	})

	It("accepts every shape targetspec accepts", func() {
		for _, shape := range []target.Shape{target.ShapeString, target.ShapeStringList, target.ShapeBoolMap, target.ShapeBool,
			target.ShapeNumber, target.ShapePositiveInt, target.ShapeMap} {
			s := `id: t
readers: x
roots: {project: "{project}", user: "{home}"}
detect: {project: {any: [.t/]}, user: {any: [.t/]}}
eras: [{since: "1", layout: {agent: {mech: markdown-file, path: a, ext: .md}}, kinds: {agent: {keys: {k: {accepts: {shape: ` + string(shape) + `}}}}}}]
`
			sp, err := targetspec.Parse("t.yaml", []byte(s))
			Expect(err).NotTo(HaveOccurred(), string(shape))
			Expect(policy.Capabilities("t", sp.Newest(), nil).Kinds["agent"].Keys["k"].Shape).To(Equal(shape))
		}
	})
})

var _ = Describe("Capabilities consent keys", func() {
	const quill = `id: quill
readers: "the quill editor"
roots: {project: "{project}", user: "{home}"}
detect: {project: {any: [.quill/]}, user: {any: [.quill/]}}
eras:
  - since: "1.0"
    layout:
      agent: {mech: markdown-file, path: .quill/agents, ext: .md%s}
%s    kinds:
      agent: {unknown_keys: keep%s}
`
	const gate = ", hook_settings: .quill/settings.json"
	const hookLayout = "      runtime-hook: {mech: claude-hooks, file: .quill/settings.json}\n"

	// admit runs the frontmatter through the agent kind of a quill spec built from the format
	// arguments and returns the surviving keys and the findings.
	admit := func(hookSettings, extraLayout, kinds string) (target.Capabilities, map[string]any, []target.Finding) {
		testenv.Isolate()
		text := fmt.Sprintf(quill, hookSettings, extraLayout, kinds)
		s, err := targetspec.Parse("quill.yaml", []byte(text))
		Expect(err).NotTo(HaveOccurred())
		caps := policy.Capabilities("quill", s.Newest(), nil)
		in, err := module.ParseDoc([]byte("---\nhooks: {Stop: [x]}\nHOOKS: x\nmcpservers: {s: {command: z}}\ncolor: red\n---\nbody\n"))
		Expect(err).NotTo(HaveOccurred())
		d, fs, ok := target.Admit("quill", caps, target.PlanContext{}, "agent", "a", in)
		Expect(ok).To(BeTrue())
		return caps, d.Frontmatter, fs
	}
	drop := func(key string) target.Finding {
		return target.Finding{Severity: target.SeverityWarn, Target: "quill", Kind: "agent", Artifact: "a", Class: target.ClassDetail,
			Code: "frontmatter-key-dropped", Key: key, AuthorHint: true,
			Text: key + " dropped: lola writes hooks and MCP servers into a component only under consent, which quill's spec does not set up"}
	}
	texts := func(fs []target.Finding) []string {
		var out []string
		for _, f := range fs {
			out = append(out, f.Text)
		}
		return out
	}

	It("drops hooks and mcpServers in any case when the layout has no hook_settings", func() {
		_, fm, fs := admit("", "", "")
		Expect(fm).To(Equal(map[string]any{"color": "red"}))
		Expect(texts(fs)).To(ConsistOf(drop("hooks").Text, drop("HOOKS").Text, drop("mcpservers").Text))
		for _, f := range fs {
			Expect(f.Severity).To(Equal(target.SeverityWarn))
		}
	})

	It("is not undone by a spec that accepts hooks", func() {
		_, fm, fs := admit("", "", ", keys: {hooks: {accepts: {shape: map}}}")
		Expect(fm).NotTo(HaveKey("hooks"))
		Expect(texts(fs)).To(ContainElement(drop("hooks").Text))
		for _, f := range fs {
			Expect(f.Severity).To(Equal(target.SeverityWarn))
		}
	})

	It("keeps the spec's own reason when it folds the key and says why", func() {
		_, fm, fs := admit("", "", ", keys: {hooks: {unsupported: quill runs no hooks, case_insensitive: true}}")
		Expect(fm).To(Equal(map[string]any{"color": "red"}))
		Expect(texts(fs)).To(ContainElement("hooks dropped: quill runs no hooks"))
		Expect(texts(fs)).To(ContainElement("HOOKS dropped: quill runs no hooks"))
		Expect(texts(fs)).To(ContainElement(drop("mcpservers").Text))
	})

	It("also drops a differently cased key the spec accepts beside its own folded drop", func() {
		_, fm, fs := admit("", "", ", keys: {hooks: {unsupported: quill runs no hooks, case_insensitive: true}, HOOKS: {accepts: {shape: map}}}")
		Expect(fm).To(Equal(map[string]any{"color": "red"}))
		Expect(texts(fs)).To(ContainElements("hooks dropped: quill runs no hooks", "HOOKS dropped: quill runs no hooks"))
	})

	It("replaces a spec's differently cased key that does not fold, leaving no two keys equal ignoring case", func() {
		caps, fm, fs := admit("", "", ", keys: {Hooks: {unsupported: quill runs no hooks}}")
		Expect(fm).To(Equal(map[string]any{"color": "red"}))
		Expect(texts(fs)).To(ContainElements(drop("hooks").Text, drop("HOOKS").Text))
		seen := map[string]bool{}
		for name := range caps.Kinds["agent"].Keys {
			Expect(seen[strings.ToLower(name)]).To(BeFalse(), name)
			seen[strings.ToLower(name)] = true
		}
	})

	It("keeps them when the layout sets hook_settings, since consent gates them there", func() {
		_, fm, fs := admit(gate, hookLayout, "")
		Expect(fm).To(HaveKey("hooks"))
		Expect(fm).To(HaveKey("mcpservers"))
		Expect(fs).To(BeEmpty())
	})
})
