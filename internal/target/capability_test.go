package target_test

import (
	"regexp"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

func doc(src string) module.Doc {
	d, err := module.ParseDoc([]byte(src))
	Expect(err).NotTo(HaveOccurred())
	return d
}

// codes lists each finding as "<severity> <code>".
func codes(fs []target.Finding) []string {
	out := []string{}
	for _, f := range fs {
		out = append(out, string(f.Severity)+" "+f.Code)
	}
	return out
}

var _ = Describe("Admit", func() {
	caps := target.Capabilities{
		Kinds: map[string]target.KindSpec{
			"agent": {
				Keys: map[string]target.Value{
					"description": {Shape: target.ShapeString},
					"model":       {Shape: target.ShapeString, Pattern: regexp.MustCompile(`^gemini-`)},
					"tools":       {Shape: target.ShapeStringList},
					"perms":       {Shape: target.ShapeBoolMap},
					"hidden":      {Shape: target.ShapeBool},
					"turns":       {Shape: target.ShapeNumber},
					"name":        {Drop: target.SeverityInfo, Note: "the file name names the agent"},
				},
				Required: []string{"description"},
				Name:     regexp.MustCompile(`^[a-z]+$`),
			},
			"skill":   {Open: true},
			"command": {Open: true},
		},
		Rules: []target.ContentRule{
			{Kind: "command", Pattern: regexp.MustCompile("!`"), Severity: target.SeveritySkip, Forceable: true, Code: "x/shell", Text: "no shell", Hint: "use !{}"},
			{Kind: "command", Pattern: regexp.MustCompile(`\$ARGUMENTS`), Severity: target.SeverityInfo, Code: "x/args", Text: "becomes {{args}}"},
		},
	}
	var ctx target.PlanContext
	BeforeEach(func() {
		testenv.Isolate()
		ctx = target.PlanContext{}
	})
	admit := func(kind, name, src string) (module.Doc, []target.Finding, bool) {
		return target.Admit("t", caps, ctx, kind, name, doc(src))
	}

	It("keeps a valid artifact as authored", func() {
		d, fs, ok := admit("agent", "rev", "---\ndescription: d\nmodel: gemini-2.5-pro\ntools: [a, b]\nperms: {write: false}\nhidden: true\nturns: 3\n---\nBody\n")
		Expect(ok).To(BeTrue())
		Expect(fs).To(BeEmpty())
		Expect(d.Render()).To(Equal("---\ndescription: d\nmodel: gemini-2.5-pro\ntools: [a, b]\nperms: {write: false}\nhidden: true\nturns: 3\n---\nBody\n"))
	})

	It("drops unknown keys and values of the wrong shape, with a warn each, and renders the rest", func() {
		d, fs, ok := admit("agent", "rev", "---\ndescription: d\nmodel: opus\ntools: Read, Grep\nperms: {write: no-thanks}\nhidden: \"true\"\nturns: many\ncolor: red\n---\nBody\n")
		Expect(ok).To(BeTrue())
		Expect(d.Render()).To(Equal("---\ndescription: d\n---\nBody\n"))
		Expect(codes(fs)).To(Equal([]string{"warn frontmatter-key-dropped", "warn frontmatter-value-dropped", "warn frontmatter-value-dropped",
			"warn frontmatter-value-dropped", "warn frontmatter-value-dropped", "warn frontmatter-value-dropped"}))
		Expect(fs[2].Text).To(Equal("model dropped: t needs a string matching ^gemini-"))
		Expect(fs[0]).To(Equal(target.Finding{Severity: target.SeverityWarn, Target: "t", Kind: "agent", Artifact: "rev",
			Code: "frontmatter-key-dropped", Text: "color dropped: t does not support it"}))
	})

	DescribeTable("checks positive integers and maps",
		func(src string, kept bool) {
			shapes := target.Capabilities{Kinds: map[string]target.KindSpec{"agent": {Keys: map[string]target.Value{
				"turns":   {Shape: target.ShapePositiveInt},
				"options": {Shape: target.ShapeMap},
			}}}}
			d, fs, ok := target.Admit("t", shapes, ctx, "agent", "rev", doc("---\n"+src+"\n---\nx\n"))
			Expect(ok).To(BeTrue())
			if kept {
				Expect(fs).To(BeEmpty())
				Expect(d.Render()).To(Equal("---\n" + src + "\n---\nx\n"))
				return
			}
			Expect(codes(fs)).To(Equal([]string{"warn frontmatter-value-dropped"}))
			Expect(d.Render()).To(Equal("x\n"))
		},
		Entry("an integer", "turns: 3", true),
		Entry("a whole float, which YAML readers in JavaScript load as an integer", "turns: 3.0", true),
		Entry("zero", "turns: 0", false),
		Entry("a negative integer", "turns: -2", false),
		Entry("a fraction", "turns: 1.5", false),
		Entry("infinity", "turns: .inf", false),
		Entry("a string", "turns: \"3\"", false),
		Entry("a map", "options: {a: 1, b: [x]}", true),
		Entry("a list where a map belongs", "options: [a]", false),
	)

	It("describes a positive integer in its finding", func() {
		shapes := target.Capabilities{Kinds: map[string]target.KindSpec{"agent": {Keys: map[string]target.Value{"turns": {Shape: target.ShapePositiveInt}}}}}
		_, fs, _ := target.Admit("t", shapes, ctx, "agent", "rev", doc("---\nturns: 0\n---\nx\n"))
		Expect(fs).To(HaveLen(1))
		Expect(fs[0].Text).To(Equal("turns dropped: t needs a positive-integer"))
	})

	Describe("a guard key, which restricts the artifact and leaves it unrestricted when dropped", func() {
		guard := &target.Finding{Code: "x/unenforceable", Text: "t cannot enforce it", Hint: "fix it"}
		guarded := target.Capabilities{Kinds: map[string]target.KindSpec{"agent": {Keys: map[string]target.Value{
			"tools": {Shape: target.ShapeStringList, Pattern: regexp.MustCompile(`^[a-z_]+$`), Guard: guard},
			"perms": {Drop: target.SeveritySkip, Guard: guard},
		}}}}
		admitGuarded := func(src string) (module.Doc, []target.Finding, bool) {
			return target.Admit("t", guarded, ctx, "agent", "rev", doc("---\n"+src+"\n---\nx\n"))
		}

		DescribeTable("keeps a value the target accepts, and drops an empty one without a finding",
			func(src, rendered string) {
				d, fs, ok := admitGuarded(src)
				Expect(ok).To(BeTrue())
				Expect(fs).To(BeEmpty())
				Expect(d.Render()).To(Equal(rendered))
			},
			Entry("an accepted list", "tools: [read_file]", "---\ntools: [read_file]\n---\nx\n"),
			Entry("an accepted empty list", "tools: []", "---\ntools: []\n---\nx\n"),
			Entry("an empty value of a key the target never keeps", "perms: []", "x\n"),
			Entry("a null value of a key the target never keeps", "perms:", "x\n"),
		)

		DescribeTable("skips the artifact, forceably, over a value the target cannot keep; --force renders it without the key",
			func(src string) {
				_, fs, ok := admitGuarded(src)
				Expect(ok).To(BeFalse())
				Expect(fs).To(Equal([]target.Finding{{Severity: target.SeveritySkip, Target: "t", Kind: "agent", Artifact: "rev",
					Code: "x/unenforceable", Text: "t cannot enforce it", Hint: "fix it", Forceable: true}}))
				ctx.Force = true
				d, fs, ok := admitGuarded(src)
				Expect(ok).To(BeTrue())
				Expect(d.Render()).To(Equal("x\n"))
				Expect(fs).To(ConsistOf(SatisfyAll(HaveField("Code", "x/unenforceable"), HaveField("Forced", true),
					HaveField("Severity", target.SeverityWarn))))
			},
			Entry("a list element that does not match", "tools: [read_file, Grep]"),
			Entry("a value of the wrong shape", "tools: Read, Grep"),
			Entry("any value of a key the target never keeps", "perms: {write: false}"),
		)
	})

	It("drops a key the target always drops, with the declared severity and note", func() {
		d, fs, ok := admit("agent", "rev", "---\nname: rev\ndescription: d\n---\nx\n")
		Expect(ok).To(BeTrue())
		Expect(d.Render()).To(Equal("---\ndescription: d\n---\nx\n"))
		Expect(fs).To(Equal([]target.Finding{{Severity: target.SeverityInfo, Target: "t", Kind: "agent", Artifact: "rev",
			Code: "frontmatter-key-dropped", Text: "name dropped: the file name names the agent"}}))
	})

	DescribeTable("skips an artifact missing a required key, forceably",
		func(src string) {
			_, fs, ok := admit("agent", "rev", src)
			Expect(ok).To(BeFalse())
			Expect(fs).To(ContainElement(target.Finding{Severity: target.SeveritySkip, Target: "t", Kind: "agent", Artifact: "rev",
				Code: "required-key-missing", Text: "t needs a description in the agent's frontmatter",
				Hint: "add description: to the agent's frontmatter", Forceable: true}))
			ctx.Force = true
			_, fs, ok = admit("agent", "rev", src)
			Expect(ok).To(BeTrue())
			Expect(fs).To(ContainElement(SatisfyAll(HaveField("Code", "required-key-missing"), HaveField("Forced", true),
				HaveField("Severity", target.SeverityWarn))))
		},
		Entry("absent", "x\n"),
		Entry("empty", "---\ndescription: \"\"\n---\nx\n"),
		Entry("of the wrong shape", "---\ndescription: [a]\n---\nx\n"),
	)

	It("skips a name the target rejects, forceably", func() {
		_, fs, ok := admit("agent", "Rev", "---\ndescription: d\n---\nx\n")
		Expect(ok).To(BeFalse())
		Expect(codes(fs)).To(Equal([]string{"skip name-rejected"}))
		Expect(fs[0].Text).To(Equal("t needs agent names matching ^[a-z]+$"))
	})

	It("skips a kind the target has no place for, even with Force", func() {
		ctx.Force = true
		_, fs, ok := admit("hook", "pre", "x\n")
		Expect(ok).To(BeFalse())
		Expect(fs).To(Equal([]target.Finding{{Severity: target.SeveritySkip, Target: "t", Kind: "hook", Artifact: "pre",
			Code: "kind-unsupported", Text: "t has no place for hooks"}}))
	})

	It("applies content rules of the artifact's kind", func() {
		_, fs, ok := admit("command", "go", "Run !`git status` on $ARGUMENTS\n")
		Expect(ok).To(BeFalse())
		Expect(codes(fs)).To(Equal([]string{"skip x/shell", "info x/args"}))
		Expect(fs[0].Hint).To(Equal("use !{}"))
		_, fs, ok = admit("skill", "go", "Run !`git status`\n")
		Expect(ok).To(BeTrue())
		Expect(fs).To(BeEmpty())
	})

	It("warns about unquoted values YAML parsers read differently, among the keys it keeps", func() {
		_, fs, ok := admit("skill", "go", "---\ndescription: d\nuser-invocable: no\nquoted: \"yes\"\n---\nx\n")
		Expect(ok).To(BeTrue())
		Expect(fs).To(Equal([]target.Finding{{Severity: target.SeverityWarn, Target: "t", Kind: "skill", Artifact: "go",
			Code: "frontmatter/yaml-ambiguous", Text: "user-invocable has an unquoted value that YAML parsers read differently",
			Hint: "quote it, e.g. \"yes\" or \"0755\""}}))
	})

	Describe("version bounds: output follows the newest version; the installed one only adds findings", func() {
		bounded := target.Capabilities{
			Kinds: map[string]target.KindSpec{
				"agent":   {Bounds: target.Bounds{Since: "1.0.0"}, Keys: map[string]target.Value{"description": {Shape: target.ShapeString}, "kind": {Shape: target.ShapeString, Bounds: target.Bounds{Since: "1.2.0"}}, "legacy": {Shape: target.ShapeString, Bounds: target.Bounds{Until: "1.1.0"}}}},
				"command": {Open: true},
				"hook":    {Bounds: target.Bounds{Until: "2.0.0"}},
			},
			Rules: []target.ContentRule{{Kind: "command", Pattern: regexp.MustCompile(`\$1`), Severity: target.SeveritySkip,
				Code: "x/positional", Text: "positional arguments are ignored", Bounds: target.Bounds{Until: "2.0.0"}}},
		}
		admitAt := func(version, kind, src string) ([]target.Finding, bool) {
			ctx.Version = version
			_, fs, ok := target.Admit("t", bounded, ctx, kind, "a", doc(src))
			return fs, ok
		}
		at := func(version, kind, src string) []string {
			fs, _ := admitAt(version, kind, src)
			return codes(fs)
		}

		It("warns when the installed version predates a kind or key, and renders anyway", func() {
			Expect(at("0.9.0", "agent", "---\ndescription: d\n---\nx\n")).To(Equal([]string{"warn version/unsupported"}))
			Expect(at("1.1.0", "agent", "---\ndescription: d\nkind: local\n---\nx\n")).To(Equal([]string{"warn version/unsupported"}))
			Expect(at("1.2.0", "agent", "---\ndescription: d\nkind: local\n---\nx\n")).To(BeEmpty())
			Expect(at("", "agent", "---\nkind: local\n---\nx\n")).To(BeEmpty(), "an unknown version adds nothing")
			Expect(at("nightly", "agent", "---\nkind: local\n---\nx\n")).To(BeEmpty(), "neither does a malformed one")
		})

		It("treats what the newest version removed as unsupported, whatever is installed", func() {
			fs, ok := admitAt("1.0.0", "agent", "---\nlegacy: x\n---\nx\n")
			Expect(ok).To(BeTrue())
			Expect(codes(fs)).To(Equal([]string{"warn frontmatter-key-dropped"}))
			fs, ok = admitAt("1.0.0", "hook", "x\n")
			Expect(ok).To(BeFalse())
			Expect(codes(fs)).To(Equal([]string{"skip kind-unsupported"}))
		})

		It("applies a problem the newest version fixed only as a warning for an older installed version", func() {
			Expect(at("1.5.0", "command", "Use $1\n")).To(Equal([]string{"warn version/limited"}))
			Expect(at("2.1.0", "command", "Use $1\n")).To(BeEmpty())
			Expect(at("", "command", "Use $1\n")).To(BeEmpty())
			Expect(at("nightly", "command", "Use $1\n")).To(BeEmpty(), "a malformed version adds nothing")
			Expect(at("2.1.287.1", "command", "Use $1\n")).To(BeEmpty())
		})
	})

	It("Describe puts a finding on one line", func() {
		f := target.Finding{Module: "m", Target: "t", Kind: "agent", Artifact: "rev", Text: "gone", Forced: true}
		Expect(f.Describe()).To(Equal("m: t agent rev: gone (rendered anyway: --force)"))
		Expect(target.Finding{Module: "m", Text: "legacy file"}.Describe()).To(Equal("m: legacy file"))
		Expect(target.Finding{Target: "t", Text: "not found"}.Describe()).To(Equal("t: not found"))
	})
})
