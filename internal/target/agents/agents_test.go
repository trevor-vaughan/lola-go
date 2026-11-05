package agents_test

import (
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/target/agents"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

func doc(src string) module.Doc {
	d, err := module.ParseDoc([]byte(src))
	Expect(err).NotTo(HaveOccurred())
	return d
}

// byPath indexes intents by Path.
func byPath(ins []plan.Intent) map[string]plan.Intent {
	out := map[string]plan.Intent{}
	for _, in := range ins {
		out[in.Path] = in
	}
	return out
}

// codes lists each finding as "<severity> <code> <artifact>".
func codes(fs []target.Finding) []string {
	out := []string{}
	for _, f := range fs {
		out = append(out, string(f.Severity)+" "+f.Code+" "+f.Artifact)
	}
	return out
}

var _ = Describe("agents", func() {
	a := agents.Target{}
	var ctx target.PlanContext
	BeforeEach(func() {
		ctx = target.PlanContext{Scope: target.Project, Root: testenv.Isolate(), ContentDir: "/store/example/versions/abc/content",
			PluginRoot: "/proj/.lola/modules/example", PluginData: "/proj/.lola/plugin-data/example"}
	})
	plan1 := func(m *module.Module) ([]plan.Intent, []target.Finding) {
		ins, fs, err := a.Plan(m, ctx)
		Expect(err).NotTo(HaveOccurred())
		return ins, fs
	}
	skill := func(name, src string) *module.Module {
		return &module.Module{Name: "example", Skills: []module.Skill{{Name: name, Dir: "skills/" + name, Doc: doc(src)}}}
	}

	It("is the agents target, with no binary to look for", func() {
		Expect(a.ID()).To(Equal("agents"))
		Expect(a.Probe().Binaries).To(BeEmpty())
	})

	It("roots the project scope at the project and the user scope at the home lola resolved", func() {
		p, err := paths.Resolve(func(k string) string { return map[string]string{"HOME": "/home/u"}[k] })
		Expect(err).NotTo(HaveOccurred())
		Expect(a.Root(target.User, "/proj", p)).To(Equal("/home/u"))
		Expect(a.Root(target.Project, "/proj", p)).To(Equal("/proj"))
		Expect(a.Destinations(target.User, "/home/u")).To(Equal(target.Destinations{Dirs: []target.DestDir{{Kind: "skill", Path: ".agents/skills", Shared: true}}}))
	})

	It("is set up where .agents/skills exists, not where only .agents does", func() {
		for _, scope := range []target.Scope{target.Project, target.User} {
			root := GinkgoT().TempDir()
			Expect(a.Detect(scope, root)).To(BeFalse())
			Expect(os.Mkdir(filepath.Join(root, ".agents"), 0o755)).To(Succeed())
			Expect(a.Detect(scope, root)).To(BeFalse(), "bare .agents/ holds other tools' things")
			Expect(os.WriteFile(filepath.Join(root, ".agents", "skills"), nil, 0o644)).To(Succeed())
			Expect(a.Detect(scope, root)).To(BeFalse(), "a file is not the skills directory")
			Expect(os.Remove(filepath.Join(root, ".agents", "skills"))).To(Succeed())
			Expect(os.Mkdir(filepath.Join(root, ".agents", "skills"), 0o755)).To(Succeed())
			Expect(a.Detect(scope, root)).To(BeTrue())
		}
	})

	It("renders a skill under .agents/skills/ with its supporting files, adding a missing name", func() {
		m := &module.Module{Name: "example", Skills: []module.Skill{{Name: "review", Dir: "skills/review", Exclude: []string{"notes.txt"},
			Doc:   doc("---\ndescription: r\nlicense: MIT\n---\nRun ${PLUGIN_ROOT}/x\n"),
			Files: []module.SkillFile{{Rel: "ref.md", Source: "ref.md.liquid", Body: "rendered\n"}}}}}
		ins, fs := plan1(m)
		Expect(fs).To(BeEmpty())
		Expect(ins).To(HaveLen(2))
		Expect(ins[0]).To(Equal(plan.Intent{Target: "agents", Kind: "skill", Op: plan.OpCopyTree, Src: ctx.ContentDir + "/skills/review",
			Path: ".agents/skills/review", Exclude: []string{"notes.txt", "SKILL.md", "ref.md.liquid"},
			Overlay: []plan.File{{Path: "ref.md", Data: []byte("rendered\n")}}}))
		Expect(ins[1]).To(Equal(plan.Intent{Target: "agents", Kind: "skill", Op: plan.OpWriteFile, Path: ".agents/skills/review/SKILL.md",
			Data: []byte("---\nname: review\ndescription: r\nlicense: MIT\n---\nRun /proj/.lola/modules/example/x\n"), Mode: 0o644}))
	})

	It("keeps a name equal to the directory as written", func() {
		ins, fs := plan1(skill("review", "---\nname: review\ndescription: r\n---\nx\n"))
		Expect(fs).To(BeEmpty())
		Expect(string(byPath(ins)[".agents/skills/review/SKILL.md"].Data)).To(Equal("---\nname: review\ndescription: r\n---\nx\n"))
	})

	It("skips, forceably, a skill whose name breaks the agentskills rules", func() {
		long := strings.Repeat("a", 65)
		for _, name := range []string{"Review", "re_view", "re--view", "-review", "review-", "re.view", long} {
			m := skill(name, "---\ndescription: r\n---\nx\n")
			ins, fs := plan1(m)
			Expect(ins).To(BeEmpty(), name)
			Expect(fs).To(ConsistOf(SatisfyAll(HaveField("Severity", target.SeveritySkip), HaveField("Code", "name-rejected"),
				HaveField("Forceable", true), HaveField("Artifact", name), HaveField("Hint", Not(BeEmpty())))), name)
			ctx.Force = true
			ins, fs = plan1(m)
			Expect(byPath(ins)).To(HaveKey(".agents/skills/"+name+"/SKILL.md"), name)
			Expect(fs).To(ConsistOf(HaveField("Forced", true)), name)
			ctx.Force = false
		}
		_, fs := plan1(skill(long, "---\ndescription: r\n---\nx\n"))
		Expect(fs[0].Text).To(Equal("agents needs the skill's name to have at most 64 characters (it has 65); " +
			"tools that read .agents/skills may reject the skill if --force installs it anyway"))
		_, fs = plan1(skill("Review", "---\ndescription: r\n---\nx\n"))
		Expect(fs[0].Text).To(HavePrefix("agents needs the skill's name to match ^[a-z0-9]+(-[a-z0-9]+)*$; "))
		Expect(fs[0].Hint).To(Equal("rename the skill's directory to lowercase letters, digits and single inner hyphens, at most 64 characters"))
		_, fs = plan1(skill(strings.Repeat("a", 64), "---\ndescription: r\n---\nx\n"))
		Expect(fs).To(BeEmpty(), "64 characters is the limit, not over it")
	})

	It("skips, forceably, a skill whose name is not its directory's", func() {
		m := skill("review", "---\nname: other\ndescription: r\n---\nx\n")
		ins, fs := plan1(m)
		Expect(ins).To(BeEmpty())
		Expect(fs).To(Equal([]target.Finding{{Severity: target.SeveritySkip, Target: "agents", Kind: "skill", Artifact: "review", Code: "name-rejected",
			Text: "agents needs the skill's name to equal its directory name (it is other); tools that read .agents/skills may reject the skill if --force installs it anyway",
			Hint: "rename the directory to other, or set name: review", Forceable: true, Class: target.ClassDetail, AuthorHint: true}}))
		ctx.Force = true
		ins, _ = plan1(m)
		Expect(string(byPath(ins)[".agents/skills/review/SKILL.md"].Data)).To(Equal("---\nname: other\ndescription: r\n---\nx\n"))
	})

	It("gives a skill one name-rejected finding naming every problem with its name", func() {
		long := "A" + strings.Repeat("a", 64)
		_, fs := plan1(skill(long, "---\nname: other\ndescription: r\n---\nx\n"))
		Expect(fs).To(HaveLen(1))
		Expect(fs[0].Code).To(Equal("name-rejected"))
		Expect(fs[0].Text).To(Equal("agents needs the skill's name to match ^[a-z0-9]+(-[a-z0-9]+)*$ and have at most 64 characters (it has 65) " +
			"and equal its directory name (it is other); tools that read .agents/skills may reject the skill if --force installs it anyway"))
		Expect(fs[0].Hint).To(Equal("rename the skill's directory to lowercase letters, digits and single inner hyphens, at most 64 characters; " +
			"rename the directory to other, or set name: " + long))
	})

	It("skips a skill without a description, even with Force: tools that read .agents/skills drop it", func() {
		want := []target.Finding{{Severity: target.SeveritySkip, Target: "agents", Kind: "skill", Artifact: "review",
			Code: "required-key-missing", Text: "agents needs a description in the skill's frontmatter",
			Hint: "add description: to the skill's frontmatter", Class: target.ClassDetail, AuthorHint: true}}
		for _, force := range []bool{false, true} {
			ctx.Force = force
			for _, src := range []string{"Review.\n", "---\ndescription: ''\n---\nx\n"} {
				ins, fs := plan1(skill("review", src))
				Expect(ins).To(BeEmpty(), src)
				Expect(fs).To(Equal(want), src)
			}
		}
	})

	It("skips, forceably, a skill whose description is over 1024 characters", func() {
		ins, fs := plan1(skill("review", "---\ndescription: "+strings.Repeat("é", 1024)+"\n---\nx\n"))
		Expect(fs).To(BeEmpty(), "1024 characters, not bytes, is the limit")
		Expect(ins).To(HaveLen(2))
		m := skill("review", "---\ndescription: "+strings.Repeat("d", 1025)+"\n---\nx\n")
		ins, fs = plan1(m)
		Expect(ins).To(BeEmpty())
		Expect(fs).To(Equal([]target.Finding{{Severity: target.SeveritySkip, Target: "agents", Kind: "skill", Artifact: "review",
			Code: "description-too-long", Text: "agents needs a skill description of at most 1024 characters (it has 1025)",
			Hint: "shorten the skill's description: to 1024 characters", Forceable: true, Class: target.ClassDetail, AuthorHint: true}}))
		ctx.Force = true
		ins, fs = plan1(m)
		Expect(ins).To(HaveLen(2))
		Expect(fs).To(ConsistOf(HaveField("Forced", true)))
	})

	It("drops Claude-only hooks and MCP servers from a skill, folding case, and warns", func() {
		ins, fs := plan1(skill("review", "---\ndescription: r\nhooks: {Stop: []}\nmcpServers: {gh: {}}\nMCP_Servers: [gh]\n---\nx\n"))
		Expect(codes(fs)).To(Equal([]string{"warn frontmatter-key-dropped review", "warn frontmatter-key-dropped review", "warn frontmatter-key-dropped review"}))
		Expect(fs[0].Text).To(Equal("MCP_Servers dropped: the tools that read .agents/skills start no MCP servers from a skill's frontmatter"))
		Expect(fs[1].Text).To(Equal("hooks dropped: the tools that read .agents/skills run no hooks from a skill's frontmatter"))
		Expect(fs[2].Text).To(Equal("mcpServers dropped: the tools that read .agents/skills start no MCP servers from a skill's frontmatter"))
		Expect(string(byPath(ins)[".agents/skills/review/SKILL.md"].Data)).To(Equal("---\nname: review\ndescription: r\n---\nx\n"))
	})

	It("warns about metadata that is not a map of strings, and renders it as written", func() {
		ins, fs := plan1(skill("review", "---\ndescription: r\nmetadata: {version: 1, stable: true, owner: me}\n---\nx\n"))
		Expect(fs).To(Equal([]target.Finding{{Severity: target.SeverityWarn, Target: "agents", Kind: "skill", Artifact: "review",
			Code: "agents/metadata-not-string", Text: "agentskills metadata values are strings; stable, version are not",
			Hint: `quote them in the skill's metadata:, e.g. version: "1"`, Class: target.ClassDetail, AuthorHint: true}}))
		Expect(string(byPath(ins)[".agents/skills/review/SKILL.md"].Data)).To(ContainSubstring("metadata: {version: 1, stable: true, owner: me}"))
		_, fs = plan1(skill("review", "---\ndescription: r\nmetadata: v1\n---\nx\n"))
		Expect(codes(fs)).To(Equal([]string{"warn agents/metadata-not-string review"}))
		Expect(fs[0].Text).To(Equal("agentskills metadata is a map of string keys to string values"))
		_, fs = plan1(skill("review", "---\ndescription: r\nmetadata: {owner: me}\n---\nx\n"))
		Expect(fs).To(BeEmpty())
	})

	It("has no place for anything but skills: .agents holds skills only", func() {
		m := &module.Module{Name: "example",
			Commands:     []module.Command{{Name: "ship", Doc: doc("Ship\n")}},
			Agents:       []module.Agent{{Name: "rev", Doc: doc("---\ndescription: d\n---\nx\n")}},
			MCPServers:   []module.MCPServer{{Name: "gh", Transport: "stdio", Command: "gh"}},
			Rules:        []module.Rule{{Name: "brief", Body: "Be brief.\n"}},
			RuntimeHooks: []module.RuntimeHookSet{{Source: "hooks/hooks.json", Hooks: []byte(`{}`)}}}
		ins, fs := plan1(m)
		Expect(ins).To(BeEmpty())
		Expect(codes(fs)).To(Equal([]string{"skip kind-unsupported ship", "skip kind-unsupported rev", "skip kind-unsupported gh",
			"skip kind-unsupported brief", "skip kind-unsupported hooks/hooks.json"}))
		for _, f := range fs {
			Expect(f.Text).To(Equal("agents has no place for " + f.Kind + "s: .agents holds skills only"))
			Expect(f.Forceable).To(BeFalse())
		}
		ctx.SkipRules = true
		_, fs = plan1(m)
		Expect(codes(fs)).NotTo(ContainElement("skip kind-unsupported brief"), "rules: false leaves rules out silently")
	})

	It("leaves out artifacts restricted to other targets", func() {
		m := skill("review", "---\ndescription: r\n---\nx\n")
		m.Skills[0].Targets = []string{"claude-code"}
		m.Commands = []module.Command{{Name: "ship", Doc: doc("Ship\n"), Targets: []string{"claude-code"}}}
		ins, fs := plan1(m)
		Expect(ins).To(BeEmpty())
		Expect(fs).To(BeEmpty())
	})
})
