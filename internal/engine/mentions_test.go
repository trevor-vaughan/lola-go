package engine_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// mentionPack is a module whose components name each other; the
// comment above a file says which rule of the scan it exercises.
func mentionPack() map[string]string {
	const mib = 1 << 20
	edge := "golang-testing\n"
	return map[string]string{
		"lola.yml": "name: pack\n",
		// a name after '@' and before ',' is a whole token; "deploy." names the command and the agent
		"skills/golang-testing/SKILL.md": "---\ndescription: Testing\n---\nSee samber/cc-skills-golang@golang-gopls, then deploy.\n",
		// every file of a skill's directory is read; CRLF and parentheses bound a token
		"skills/golang-testing/ref/races.md": "Races:\r\n(golang-race)\r\n",
		"skills/golang-gopls/SKILL.md":       "---\ndescription: Gopls\n---\nNo links here.\n",
		// a NUL in the first 8 KiB: binary, skipped
		"skills/golang-gopls/blob.bin": "golang-lint\x00",
		// over 1 MiB: skipped
		"skills/golang-gopls/big.md": strings.Repeat("x", mib) + " golang-race",
		// a NUL past the first 8 KiB: text
		"skills/golang-gopls/late.txt": strings.Repeat("x", 8<<10) + "\x00 golang-testing",
		// inside a longer token, in another case or spelling: no mention
		"skills/golang-lint/SKILL.md": "---\ndescription: Lint\n---\nNot golang-linter, xgolang-gopls, golang-gopls2, GOLANG-RACE or golang_race.\n",
		"skills/golang-race/SKILL.md": "---\ndescription: Race\n---\nPairs with golang-lint.\n",
		// exactly 1 MiB: read
		"skills/golang-race/edge.md": edge + strings.Repeat("x", mib-len(edge)),
		"commands/deploy.md":         "Deploy after golang-testing; see style.\n",
		// vet is shorter than 4 bytes: never searched
		"commands/vet.md": "Vet.\n",
		// a component never mentions itself; "deployer" is no mention of deploy
		"agents/deploy.md": "---\ndescription: deployer\n---\nRun vet, then deploy.\n",
		// MCP server definitions are not read
		"mcps.json":      `{"mcpServers":{"gh-tools":{"command":"gh","args":["golang-lint"]}}}`,
		"rules/style.md": "Prefer golang-lint and gh-tools.\n",
	}
}

// packMentions is what the scan finds in mentionPack.
var packMentions = map[string][]string{
	"skill/golang-testing": {"agent/deploy", "command/deploy", "skill/golang-gopls", "skill/golang-race"},
	"skill/golang-gopls":   {"skill/golang-testing"},
	"skill/golang-race":    {"skill/golang-lint", "skill/golang-testing"},
	"command/deploy":       {"rule/style", "skill/golang-testing"},
	"agent/deploy":         {"command/deploy"},
	"rule/style":           {"mcp/gh-tools", "skill/golang-lint"},
}

func component(s string) engine.Component {
	kind, name, _ := strings.Cut(s, "/")
	return engine.Component{Kind: kind, Name: name}
}

func mentionsOf(m map[string][]string) engine.Mentions {
	out := engine.Mentions{}
	for k, vs := range m {
		var cs []engine.Component
		for _, v := range vs {
			cs = append(cs, component(v))
		}
		out[component(k)] = cs
	}
	return out
}

var _ = Describe("the mention scan", func() {
	var (
		tmp, proj, src string
		e              *engine.Engine
		scopes         []engine.ScopeChoice
		ctx            = context.Background()
	)
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		proj = filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		e.Getwd = func() (string, error) { return proj, nil }
		scopes = []engine.ScopeChoice{{Scope: engine.Scope{Kind: target.Project, ProjectDir: proj}, Source: engine.SourceFlag}}
		src = filepath.Join(tmp, "src", "pack")
		writeTree(src, mentionPack())
	})
	stage := func() *engine.StagedRun {
		GinkgoHelper()
		run, err := e.StageInstall()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(run.Close)
		return run
	}
	listed := func(run *engine.StagedRun, args ...string) *engine.Choices {
		GinkgoHelper()
		req := engine.ChoicesRequest{Cwd: proj, Scopes: scopes, Run: run}
		for _, a := range args {
			req.Modules = append(req.Modules, engine.InstallRequest{Arg: a})
		}
		ch, err := e.InstallChoices(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		return ch
	}

	It("finds whole-token names of a module's other components in its files, by the scan rules", func() {
		ch := listed(stage(), src)
		Expect(e.Store.Names()).To(BeEmpty(), "the version is read from the staged run, not the cache")
		Expect(ch.Mentions("pack")).To(Equal(mentionsOf(packMentions)))
	})

	It("reads a cached module's version the same way, and lists nothing for a module the choices do not list", func() {
		_, err := e.AddSource(ctx, tmp, src, "", "", nil)
		Expect(err).NotTo(HaveOccurred())
		ch := listed(nil)
		Expect(ch.Mentions("pack")).To(Equal(mentionsOf(packMentions)))
		Expect(ch.Mentions("nope")).To(BeNil())
	})

	It("matches a name holding '.' at a sentence's end, and a 4-byte name, but not inside a longer token", func() {
		dots := filepath.Join(tmp, "src", "dots")
		writeTree(dots, map[string]string{"lola.yml": "name: dots\n",
			"skills/fmt.v2/SKILL.md": "---\ndescription: f\n---\nThen lint.\n",
			"skills/lint/SKILL.md":   "---\ndescription: l\n---\nRun fmt.v2. Not fmt.v2x or fmt-v2.\n"})
		Expect(listed(stage(), dots).Mentions("dots")).To(Equal(mentionsOf(map[string][]string{
			"skill/fmt.v2": {"skill/lint"}, "skill/lint": {"skill/fmt.v2"}})))
	})

	It("never counts another module's component", func() {
		zeta := filepath.Join(tmp, "src", "zeta")
		writeTree(zeta, map[string]string{"lola.yml": "name: zeta\n", "commands/zeta-run.md": "Use golang-race.\n", "agents/golang-race.md": "Use zeta-run.\n"})
		ch := listed(stage(), src, zeta)
		Expect(ch.Mentions("zeta")).To(Equal(mentionsOf(map[string][]string{
			"command/zeta-run": {"agent/golang-race"}, "agent/golang-race": {"command/zeta-run"}})))
		Expect(ch.Mentions("pack")).To(Equal(mentionsOf(packMentions)))
	})

	DescribeTable("reads at most 16 MiB of a module",
		func(fill int, want map[string][]string) {
			big := filepath.Join(tmp, "src", "big")
			files := map[string]string{"lola.yml": "name: big\n",
				"skills/aaaa/SKILL.md": "---\ndescription: a\n---\nSee zzzz.\n",
				"skills/zzzz/SKILL.md": "---\ndescription: z\n---\nSee aaaa.\n"}
			for i := range fill {
				files[fmt.Sprintf("skills/aaaa/f%02d.txt", i)] = strings.Repeat("x", 1<<20)
			}
			writeTree(big, files)
			Expect(listed(stage(), big).Mentions("big")).To(Equal(mentionsOf(want)))
		},
		Entry("under the cap every file is read", 15, map[string][]string{"skill/aaaa": {"skill/zzzz"}, "skill/zzzz": {"skill/aaaa"}}),
		Entry("the file that would pass it, and every later one, is skipped", 16, map[string][]string{"skill/aaaa": {"skill/zzzz"}}),
	)
	It("finds the same mentions in a module of many components, names holding '.' among them", func() {
		many := filepath.Join(tmp, "src", "many")
		files := map[string]string{"lola.yml": "name: many\n",
			"skills/lib.v2/SKILL.md": "---\ndescription: l\n---\nx\n",
			"skills/v2.x/SKILL.md":   "---\ndescription: v\n---\nx\n",
			"skills/ends./SKILL.md":  "---\ndescription: e\n---\nx\n",
			// inside a dotted run, at a '.' on each side: a mention; "v2.x" is one too
			"skills/hub-a/SKILL.md": "---\ndescription: h\n---\npre.lib.v2.x, then ends. and ends.x\n",
			// bounded by a token byte, or a '.' run that is not the name: none
			"skills/hub-b/SKILL.md": "---\ndescription: h\n---\nlib.v2x xlib.v2 lib.v20 ends.x lib-v2\n",
		}
		want := map[string][]string{"skill/hub-a": {"skill/ends.", "skill/lib.v2", "skill/v2.x"}}
		for i := range 300 {
			files[fmt.Sprintf("skills/node%03d/SKILL.md", i)] = fmt.Sprintf("---\ndescription: n\n---\nNext: node%03d; not node%03dx.\n", (i+1)%300, (i+2)%300)
			want[fmt.Sprintf("skill/node%03d", i)] = []string{fmt.Sprintf("skill/node%03d", (i+1)%300)}
		}
		writeTree(many, files)
		Expect(listed(stage(), many).Mentions("many")).To(Equal(mentionsOf(want)))
	})

	It("reads an inline command's text, which has no file", func() {
		inline := filepath.Join(tmp, "src", "inline")
		writeTree(inline, map[string]string{
			"lola.yml":               "name: inline\ncomponents:\n  commands:\n    - {name: about-cmd, content: \"Ask the helper-agent.\"}\n",
			"agents/helper-agent.md": "---\ndescription: h\n---\nRun about-cmd.\n"})
		Expect(listed(stage(), inline).Mentions("inline")).To(Equal(mentionsOf(map[string][]string{
			"command/about-cmd": {"agent/helper-agent"}, "agent/helper-agent": {"command/about-cmd"}})))
	})

	It("reads a root skill's SKILL.md, but not the root files it excludes from the skill", func() {
		root := filepath.Join(tmp, "src", "rootsk")
		writeTree(root, map[string]string{"lola.yml": "name: rootsk\n",
			"SKILL.md":               "---\ndescription: r\n---\nRun other-cmd.\n",
			"AGENTS.md":              "Run third-cmd.\n",
			"notes.md":               "Run fourth-cmd.\n",
			"commands/other-cmd.md":  "Use rootsk.\n",
			"commands/third-cmd.md":  "x\n",
			"commands/fourth-cmd.md": "x\n"})
		Expect(listed(stage(), root).Mentions("rootsk")).To(Equal(mentionsOf(map[string][]string{
			"skill/rootsk": {"command/fourth-cmd", "command/other-cmd"}, "command/other-cmd": {"skill/rootsk"}})))
	})

	It("reads a templated skill's SKILL.md.liquid", func() {
		tpl := filepath.Join(tmp, "src", "tpl")
		writeTree(tpl, map[string]string{"lola.yml": "name: tpl\n",
			"skills/tpl-skill/SKILL.md.liquid": "---\ndescription: t\n---\nRun other-cmd.\n",
			"commands/other-cmd.md":            "Use tpl-skill.\n"})
		Expect(listed(stage(), tpl).Mentions("tpl")).To(Equal(mentionsOf(map[string][]string{
			"skill/tpl-skill": {"command/other-cmd"}, "command/other-cmd": {"skill/tpl-skill"}})))
	})

	It("reads a command's sidecar directory", func() {
		side := filepath.Join(tmp, "src", "side")
		writeTree(side, map[string]string{"lola.yml": "name: side\n",
			"commands/deploy.md":         "Deploy.\n",
			"commands/deploy/extra.md":   "Needs side-skill.\n",
			"skills/side-skill/SKILL.md": "---\ndescription: s\n---\nNothing.\n"})
		Expect(listed(stage(), side).Mentions("side")).To(Equal(mentionsOf(map[string][]string{
			"command/deploy": {"skill/side-skill"}})))
	})

	It("skips a symlinked file and a symlinked directory, which the loader refuses in a source but a cached tree could gain", func() {
		link := filepath.Join(tmp, "src", "link")
		writeTree(link, map[string]string{"lola.yml": "name: link\n",
			"skills/link-aaa/SKILL.md": "---\ndescription: a\n---\nNothing.\n",
			"skills/link-bbb/SKILL.md": "---\ndescription: b\n---\nNothing.\n",
			"commands/real/note.md":    "link-bbb\n"})
		_, err := e.AddSource(ctx, tmp, link, "", "", nil)
		Expect(err).NotTo(HaveOccurred())
		v, ok, err := e.Store.Head("link", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(os.Symlink(filepath.Join(v.ContentDir, "commands", "real", "note.md"), filepath.Join(v.ContentDir, "skills", "link-aaa", "note.md"))).To(Succeed())
		Expect(os.Symlink(filepath.Join(v.ContentDir, "commands", "real"), filepath.Join(v.ContentDir, "skills", "link-aaa", "dir"))).To(Succeed())
		Expect(listed(nil).Mentions("link")).To(BeEmpty())
	})

})

var _ = Describe("lola info lists what each component mentions", func() {
	var (
		tmp string
		e   *engine.Engine
		ctx = context.Background()
	)
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		e.Getwd = func() (string, error) { return tmp, nil }
	})
	// info shows arg without a scope: the mentions come before anything a scope decides.
	info := func(arg string) *engine.Info {
		GinkgoHelper()
		in, err := e.Info(ctx, engine.InfoRequest{Cwd: tmp, Arg: arg, Scope: engine.ScopeRequest{Cwd: tmp},
			Rerun: func(extra ...string) string { return "lola info " + strings.Join(extra, " ") }, UserRun: "lola info --user",
			Install: func(arg, _, _ string, _ engine.TrustKinds) string { return "lola install " + arg }})
		Expect(err).NotTo(HaveOccurred())
		return in
	}

	It("lists, in picker order, each component that mentions others with what it mentions", func() {
		src := filepath.Join(tmp, "src", "pack")
		writeTree(src, mentionPack())
		Expect(info(src).Mentions).To(Equal([]engine.ComponentMentions{
			{Component: "skill/golang-gopls", Mentions: []string{"skill/golang-testing"}},
			{Component: "skill/golang-race", Mentions: []string{"skill/golang-lint", "skill/golang-testing"}},
			{Component: "skill/golang-testing", Mentions: []string{"agent/deploy", "command/deploy", "skill/golang-gopls", "skill/golang-race"}},
			{Component: "command/deploy", Mentions: []string{"rule/style", "skill/golang-testing"}},
			{Component: "agent/deploy", Mentions: []string{"command/deploy"}},
			{Component: "rule/style", Mentions: []string{"mcp/gh-tools", "skill/golang-lint"}},
		}))
	})

	It("lists [] for a module whose components mention none", func() {
		src := filepath.Join(tmp, "src", "plain")
		writeTree(src, map[string]string{"lola.yml": "name: plain\n", "commands/ship.md": "Ship\n"})
		in := info(src)
		Expect(in.Mentions).To(Equal([]engine.ComponentMentions{}))
		Expect(in.Notices).To(BeEmpty())
	})

})

var _ = Describe("naming what the picks mention and leave out", func() {
	var (
		tmp, proj, src string
		e              *engine.Engine
		sc             engine.Scope
		ctx            = context.Background()
		claude         = []string{"claude-code"}
	)
	const hint = "lola install --project pack --with-mentions -t claude-code"
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		proj = filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		e.Getwd = func() (string, error) { return proj, nil }
		sc = engine.Scope{Kind: target.Project, ProjectDir: proj}
		src = filepath.Join(tmp, "src", "pack")
		writeTree(src, mentionPack())
	})
	edit := func(op, s string) engine.SelectionEdit {
		GinkgoHelper()
		sel, err := config.ParseSelector(s)
		Expect(err).NotTo(HaveOccurred())
		return engine.SelectionEdit{Op: op, Selector: sel}
	}
	only := func(s string) engine.SelectionEdit { return edit(engine.EditOnly, s) }
	skip := func(s string) engine.SelectionEdit { return edit(engine.EditSkip, s) }
	// install installs arg at sc for claude-code, with spec's flags and edits given as flags.
	install := func(arg string, spec engine.InstallSpec, edits ...engine.SelectionEdit) []string {
		GinkgoHelper()
		spec.Cwd, spec.ConfirmRemovals = proj, confirmAll
		spec.Scopes = []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: sc, Source: engine.SourceFlag}, Targets: claude}}
		spec.Modules = []engine.InstallRequest{{Arg: arg}}
		if spec.Options.Targets == nil {
			spec.Options.Targets = claude
		}
		Expect(spec.AddFlagEdits(edits, false)).To(Succeed())
		res, err := e.InstallAll(ctx, spec)
		Expect(err).NotTo(HaveOccurred())
		texts := []string{}
		for _, n := range res.Notices {
			texts = append(texts, n.Text)
		}
		return texts
	}

	It("names each picked component's mentions the selection leaves out, with the install that adds them", func() {
		Expect(install(src, engine.InstallSpec{NameMentions: true}, only("skill/golang-testing"))).To(ContainElement(
			"pack: skill/golang-testing mentions 4 components you did not pick: agent/deploy, command/deploy, skill/golang-gopls, skill/golang-race → add them: " + hint))
	})

	It("names several mentioners in one notice for the module", func() {
		Expect(install(src, engine.InstallSpec{NameMentions: true}, only("skill/golang-gopls"), only("skill/golang-race"), only("command/deploy"))).To(ContainElement(
			"pack: skill/golang-gopls, skill/golang-race, command/deploy mention 3 components you did not pick: rule/style, skill/golang-lint, skill/golang-testing → add them: " + hint))
	})

	It("keeps -t, --no-rules, -n and the output flags in the hint, names a staged source by its argument, and names no rule while rules are off", func() {
		run, err := e.StageInstall()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(run.Close)
		off := false
		texts := install(src, engine.InstallSpec{NameMentions: true, Staged: run,
			Options: engine.Options{Targets: claude, Rules: &off, DryRun: true, Display: []string{"--json"}}}, only("command/deploy"))
		Expect(texts).To(ContainElement("pack: command/deploy mentions 1 component you did not pick: skill/golang-testing → add them: lola install --project " +
			src + " --with-mentions --only command/deploy -t claude-code --no-rules -n --json"))
	})

	It("repeats in the hint each --skip flag that kept a mention out, so running the hint adds only what the notice names", func() {
		Expect(install(src, engine.InstallSpec{NameMentions: true}, only("skill/golang-race"), skip("skill/golang-lint"))).To(ContainElement(
			"pack: skill/golang-race mentions 1 component you did not pick: skill/golang-testing → add them: " +
				"lola install --project pack --with-mentions --skip skill/golang-lint -t claude-code"))
		texts := install("pack", engine.InstallSpec{NameMentions: true, WithMentions: true}, skip("skill/golang-lint"))
		Expect(texts).To(ContainElement("pack: + skill/golang-testing (mentioned by skill/golang-race)"))
		Expect(texts).NotTo(ContainElement(ContainSubstring("+ skill/golang-lint")))
	})

	It("repeats a --skip --global flag with --global, and no flag that selects nothing of the module", func() {
		zeta := filepath.Join(tmp, "src", "zeta")
		writeTree(zeta, map[string]string{"lola.yml": "name: zeta\n", "commands/zeta-run.md": "Run.\n"})
		lint, other := skip("pack:skill/golang-lint"), skip("zeta:command/zeta-run")
		lint.Flag, other.Flag = true, true
		spec := engine.InstallSpec{NameMentions: true, GlobalEdits: []engine.SelectionEdit{lint, other}}
		spec.Cwd = proj
		spec.Scopes = []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: sc, Source: engine.SourceFlag}, Targets: claude}}
		spec.Modules = []engine.InstallRequest{{Arg: src}, {Arg: zeta}}
		spec.Options.Targets = claude
		Expect(spec.AddFlagEdits([]engine.SelectionEdit{only("pack:skill/golang-race")}, false)).To(Succeed())
		res, err := e.InstallAll(ctx, spec)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo,
			Text: "pack: skill/golang-race mentions 1 component you did not pick: skill/golang-testing → add them: " +
				"lola install --project pack --with-mentions --skip pack:skill/golang-lint --global -t claude-code",
			Class: target.ClassDetail, Module: "pack", Check: "lola check --project pack"}), "a mention is detail, counted under its module and lola check")
	})

	It("repeats under -n every selection flag of the run, which a dry run saved none of", func() {
		exact := only("skill/golang-race")
		exact.Exact = true
		Expect(install(src, engine.InstallSpec{NameMentions: true, Options: engine.Options{DryRun: true}}, exact, skip("skill/golang-lint"))).To(ContainElement(
			"pack: skill/golang-race mentions 1 component you did not pick: skill/golang-testing → add them: " +
				"lola install --project pack --with-mentions --only skill/golang-race --skip skill/golang-lint --exact -t claude-code -n"))
	})

	It("names nothing when every mention is installed, or when the run does not ask (lola info's plan)", func() {
		Expect(install(src, engine.InstallSpec{NameMentions: true})).NotTo(ContainElement(ContainSubstring("you did not pick")))
		Expect(install(src, engine.InstallSpec{}, only("skill/golang-testing"))).NotTo(ContainElement(ContainSubstring("you did not pick")))
	})

	It("names at most five, then how many more", func() {
		wide := filepath.Join(tmp, "src", "wide")
		files := map[string]string{"lola.yml": "name: wide\n", "skills/hub0/SKILL.md": "---\ndescription: h\n---\nleaf1 leaf2 leaf3 leaf4 leaf5 leaf6 leaf7\n"}
		for i := 1; i <= 7; i++ {
			files[fmt.Sprintf("skills/leaf%d/SKILL.md", i)] = "---\ndescription: l\n---\nx\n"
		}
		writeTree(wide, files)
		Expect(install(wide, engine.InstallSpec{NameMentions: true}, only("skill/hub0"))).To(ContainElement(
			"wide: skill/hub0 mentions 7 components you did not pick: skill/leaf1, skill/leaf2, skill/leaf3, skill/leaf4, skill/leaf5 and 2 more → add them: " +
				"lola install --project wide --with-mentions -t claude-code"))
	})

	It("names the same in the review's preview, reading the staged version and naming it by its argument, as the user may decline", func() {
		run, err := e.StageInstall()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(run.Close)
		choice := engine.ScopeChoice{Scope: sc, Source: engine.SourceFlag}
		ch, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: []engine.ScopeChoice{choice}, Run: run,
			Modules: []engine.InstallRequest{{Arg: src}}})
		Expect(err).NotTo(HaveOccurred())
		spec := engine.InstallSpec{Cwd: proj, Staged: run, NameMentions: true, Options: engine.Options{Targets: claude},
			Modules: []engine.InstallRequest{{Arg: src, Version: &ch.Versions[0]}},
			Scopes:  []engine.ScopeInstall{{ScopeChoice: choice, Targets: claude, Edits: []engine.SelectionEdit{only("pack:skill/golang-testing")}}}}
		pv, err := ch.Preview(spec, []string{"pack"})
		Expect(err).NotTo(HaveOccurred())
		Expect(pv.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo,
			Text: "pack: skill/golang-testing mentions 4 components you did not pick: agent/deploy, command/deploy, skill/golang-gopls, skill/golang-race → add them: " +
				"lola install --project " + src + " --with-mentions -t claude-code", Class: target.ClassDetail, Module: "pack",
			Check: "lola check --project " + src}), "I-1: a version the review may decline is checked by its source")
	})
})

var _ = Describe("lola install --with-mentions", func() {
	var (
		tmp, proj, src, key string
		p                   paths.Paths
		e                   *engine.Engine
		sc                  engine.Scope
		ctx                 = context.Background()
		claude              = []string{"claude-code"}
	)
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		proj = filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		e.Getwd = func() (string, error) { return proj, nil }
		sc = engine.Scope{Kind: target.Project, ProjectDir: proj}
		key, err = paths.ProjectKey(proj)
		Expect(err).NotTo(HaveOccurred())
		src = filepath.Join(tmp, "src", "pack")
		writeTree(src, mentionPack())
	})
	edit := func(op, s string) engine.SelectionEdit {
		GinkgoHelper()
		sel, err := config.ParseSelector(s)
		Expect(err).NotTo(HaveOccurred())
		return engine.SelectionEdit{Op: op, Selector: sel}
	}
	only := func(s string) engine.SelectionEdit { return edit(engine.EditOnly, s) }
	pats := func(ss ...string) []config.Pattern {
		GinkgoHelper()
		var out []config.Pattern
		for _, s := range ss {
			pt, err := config.ParsePattern(s)
			Expect(err).NotTo(HaveOccurred())
			out = append(out, pt)
		}
		return out
	}
	newSpec := func(opts engine.Options) engine.InstallSpec {
		opts.Targets = claude
		return engine.InstallSpec{Cwd: proj, WithMentions: true, NameMentions: true, Options: opts, Modules: []engine.InstallRequest{{Arg: src}},
			Scopes: []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: sc, Source: engine.SourceFlag}, Targets: claude}}}
	}
	apply := func(spec engine.InstallSpec) *engine.InstallResult {
		GinkgoHelper()
		res, err := e.InstallAll(ctx, spec)
		Expect(err).NotTo(HaveOccurred())
		return res
	}
	install := func(opts engine.Options, edits ...engine.SelectionEdit) *engine.InstallResult {
		GinkgoHelper()
		spec := newSpec(opts)
		Expect(spec.AddFlagEdits(edits, false)).To(Succeed())
		return apply(spec)
	}
	texts := func(res *engine.InstallResult) []string {
		out := []string{}
		for _, n := range res.Notices {
			out = append(out, n.Text)
		}
		return out
	}
	saved := func(w config.Where) config.Entry {
		GinkgoHelper()
		s, err := config.LoadSelections(p.Selections)
		Expect(err).NotTo(HaveOccurred())
		return s.Entry(w, "pack")
	}
	installed := func(rel string) bool {
		_, err := os.Stat(filepath.Join(proj, ".claude", rel))
		return err == nil
	}

	It("adds what the picks mention directly, one hop, as --unskip edits saved to selections.yaml, and names each", func() {
		res := install(engine.Options{}, only("skill/golang-race"))
		Expect(texts(res)).To(ContainElements(
			"pack: + skill/golang-lint (mentioned by skill/golang-race)",
			"pack: + skill/golang-testing (mentioned by skill/golang-race)",
			"pack: skill/golang-testing mentions 3 components you did not pick: agent/deploy, command/deploy, skill/golang-gopls → add them: "+
				"lola install --project pack --with-mentions -t claude-code"))
		Expect(saved(config.Project(key))).To(Equal(config.Entry{
			Skip: pats("skill/golang-gopls", "command/deploy", "command/vet", "agent/deploy", "mcp/gh-tools", "rule/style")}))
		Expect(installed("skills/golang-lint/SKILL.md")).To(BeTrue())
		Expect(installed("skills/golang-testing/SKILL.md")).To(BeTrue())
		Expect(installed("skills/golang-gopls")).To(BeFalse(), "what golang-testing mentions is the next hop")
	})

	It("adds an include to the scope's section where a global only: list leaves a mention out", func() {
		Expect(os.MkdirAll(filepath.Dir(p.Selections), 0o700)).To(Succeed())
		Expect(os.WriteFile(p.Selections, []byte("version: 1\nglobal:\n  pack:\n    only: [skill/golang-race]\n"), 0o600)).To(Succeed())
		install(engine.Options{})
		Expect(saved(config.Project(key))).To(Equal(config.Entry{Include: pats("skill/golang-lint", "skill/golang-testing")}))
		Expect(saved(config.Global())).To(Equal(config.Entry{Only: pats("skill/golang-race")}))
		Expect(installed("skills/golang-lint/SKILL.md")).To(BeTrue())
	})

	It("says nothing the picks mention is left out, and writes nothing, when every mention is installed", func() {
		Expect(texts(install(engine.Options{}))).To(ContainElement("pack: nothing the picked components mention is left out"))
		Expect(p.Selections).NotTo(BeAnExistingFile())
	})

	It("never adds a rule while rules are off", func() {
		off := false
		res := install(engine.Options{Rules: &off}, only("command/deploy"))
		Expect(texts(res)).To(ContainElement("pack: + skill/golang-testing (mentioned by command/deploy)"))
		Expect(texts(res)).NotTo(ContainElement(ContainSubstring("+ rule/style")))
		Expect(saved(config.Project(key)).Skip).To(ContainElement(pats("rule/style")[0]))
	})

	It("writes nothing under -n but names what it would add", func() {
		res := install(engine.Options{DryRun: true}, only("skill/golang-race"))
		Expect(texts(res)).To(ContainElement("pack: + skill/golang-lint (mentioned by skill/golang-race)"))
		Expect(p.Selections).NotTo(BeAnExistingFile())
	})

	It("saves nothing the second time", func() {
		install(engine.Options{}, only("skill/golang-race"))
		before, err := os.ReadFile(p.Selections)
		Expect(err).NotTo(HaveOccurred())
		res := install(engine.Options{}, only("skill/golang-race"))
		Expect(res.Modules[0].SavedTo).To(BeEmpty())
		Expect(os.ReadFile(p.Selections)).To(Equal(before))
	})

	It("keeps out a mention this run's --skip flag names, and says so", func() {
		res := install(engine.Options{}, only("skill/golang-race"), edit(engine.EditSkip, "skill/golang-lint"))
		Expect(texts(res)).To(ContainElements(
			"pack: skill/golang-lint is mentioned by skill/golang-race but kept out by --skip skill/golang-lint",
			"pack: + skill/golang-testing (mentioned by skill/golang-race)",
			"pack: skill/golang-testing mentions 3 components you did not pick: agent/deploy, command/deploy, skill/golang-gopls → add them: "+
				"lola install --project pack --with-mentions --skip skill/golang-lint -t claude-code"))
		Expect(texts(res)).NotTo(ContainElement(ContainSubstring("+ skill/golang-lint")))
		Expect(texts(res)).NotTo(ContainElement(ContainSubstring("nothing the picked components mention")))
		Expect(saved(config.Project(key)).Skip).To(ContainElement(pats("skill/golang-lint")[0]))
		Expect(installed("skills/golang-lint")).To(BeFalse())
		Expect(installed("skills/golang-testing/SKILL.md")).To(BeTrue())
	})

	It("names nothing left out when a --skip flag kept out the only mention", func() {
		res := install(engine.Options{}, only("agent/deploy"), edit(engine.EditSkip, "command/deploy"))
		Expect(texts(res)).To(ContainElement("pack: command/deploy is mentioned by agent/deploy but kept out by --skip command/deploy"))
		Expect(texts(res)).NotTo(ContainElement(ContainSubstring("you did not pick")))
		Expect(texts(res)).NotTo(ContainElement(ContainSubstring("nothing the picked components mention")))
	})

	It("keeps out a mention a --skip --global flag names", func() {
		spec := newSpec(engine.Options{})
		Expect(spec.AddFlagEdits([]engine.SelectionEdit{only("skill/golang-race")}, false)).To(Succeed())
		Expect(spec.AddFlagEdits([]engine.SelectionEdit{edit(engine.EditSkip, "pack:skill/golang-lint")}, true)).To(Succeed())
		res := apply(spec)
		Expect(texts(res)).To(ContainElement("pack: skill/golang-lint is mentioned by skill/golang-race but kept out by --skip pack:skill/golang-lint"))
		Expect(installed("skills/golang-lint")).To(BeFalse())
	})

	It("adds a mention the component page unticked", func() {
		spec := newSpec(engine.Options{})
		spec.Scopes[0].Edits = []engine.SelectionEdit{only("skill/golang-race"), edit(engine.EditSkip, "pack:skill/golang-lint")}
		res := apply(spec)
		Expect(texts(res)).To(ContainElement("pack: + skill/golang-lint (mentioned by skill/golang-race)"))
		Expect(texts(res)).NotTo(ContainElement(ContainSubstring("kept out by")))
		Expect(installed("skills/golang-lint/SKILL.md")).To(BeTrue())
	})

	It("shows the additions in the review's preview", func() {
		run, err := e.StageInstall()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(run.Close)
		choice := engine.ScopeChoice{Scope: sc, Source: engine.SourceFlag}
		ch, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: []engine.ScopeChoice{choice}, Run: run,
			Modules: []engine.InstallRequest{{Arg: src}}})
		Expect(err).NotTo(HaveOccurred())
		spec := engine.InstallSpec{Cwd: proj, Staged: run, WithMentions: true, Options: engine.Options{Targets: claude},
			Modules: []engine.InstallRequest{{Arg: src, Version: &ch.Versions[0]}},
			Scopes:  []engine.ScopeInstall{{ScopeChoice: choice, Targets: claude, Edits: []engine.SelectionEdit{only("pack:skill/golang-race")}}}}
		pv, err := ch.Preview(spec, []string{"pack"})
		Expect(err).NotTo(HaveOccurred())
		Expect(pv.Selected[sc.Key()]["pack"]).To(Equal([]string{"skill/golang-lint", "skill/golang-race", "skill/golang-testing"}))
		Expect(pv.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "pack: + skill/golang-lint (mentioned by skill/golang-race)"}))
	})

	It("keeps out of the review's preview a mention this run's --skip flag names", func() {
		run, err := e.StageInstall()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(run.Close)
		choice := engine.ScopeChoice{Scope: sc, Source: engine.SourceFlag}
		ch, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: []engine.ScopeChoice{choice}, Run: run,
			Modules: []engine.InstallRequest{{Arg: src}}})
		Expect(err).NotTo(HaveOccurred())
		spec := engine.InstallSpec{Cwd: proj, Staged: run, WithMentions: true, Options: engine.Options{Targets: claude},
			Modules: []engine.InstallRequest{{Arg: src, Version: &ch.Versions[0]}},
			Scopes:  []engine.ScopeInstall{{ScopeChoice: choice, Targets: claude}}}
		Expect(spec.AddFlagEdits([]engine.SelectionEdit{only("pack:skill/golang-race"), edit(engine.EditSkip, "skill/golang-lint")}, false)).To(Succeed())
		pv, err := ch.Preview(spec, []string{"pack"})
		Expect(err).NotTo(HaveOccurred())
		Expect(pv.Selected[sc.Key()]["pack"]).To(Equal([]string{"skill/golang-race", "skill/golang-testing"}))
		Expect(pv.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo,
			Text: "pack: skill/golang-lint is mentioned by skill/golang-race but kept out by --skip skill/golang-lint"}))
	})
})
