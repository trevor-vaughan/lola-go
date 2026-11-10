package targets_test

import (
	"os"
	"regexp"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/target/spec"
	"github.com/trevor-vaughan/lola-go/internal/targetspec"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// renamed is the built-in spec in specs/<file>.yaml under another id.
func renamed(file, id string) target.Target {
	raw, err := os.ReadFile("../targetspec/specs/" + file + ".yaml")
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	line := regexp.MustCompile(`(?m)^id: .*$`)
	ExpectWithOffset(1, line.Match(raw)).To(BeTrue())
	s, err := targetspec.Parse(file+".yaml", line.ReplaceAll(raw, []byte("id: "+id)))
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return spec.New(s)
}

var _ = Describe("a mechanism's findings", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("name the claude-code spec's own id, not claude-code", func() {
		t := renamed("claude-code", "claude-alt")
		ctx := target.PlanContext{Scope: target.Project, ContentDir: "/cache/example/content", PluginRoot: "/proj/.lola/modules/example",
			PluginData: "/proj/.lola/plugin-data/example", SettingsExposed: func(string) (string, string) { return "settings.local.json is not ignored", "settings.local.json" }}
		m := &module.Module{Name: "example",
			Skills:     []module.Skill{{Name: "review", Dir: "skills/review", Doc: doc("---\ndescription: d\nallowed-tools: [Bash(git *)]\n---\nR\n")}},
			Agents:     []module.Agent{{Name: "critic", Doc: doc("---\ndescription: c\npermissionMode: plan\n---\nC\n")}},
			MCPServers: []module.MCPServer{{Name: "gh", Transport: "stdio", Command: "gh", Cwd: "/tmp"}},
			RuntimeHooks: []module.RuntimeHookSet{
				{Source: "hooks/a.json", Hooks: []byte(`{"Stop":[{"hooks":[{"type":"command","command":"-x","args":["a"]}]}]}`)},
				{Source: "hooks/b.json", Hooks: []byte(`{"1bad":[{"hooks":[{"type":"prompt","prompt":"p"}]}]}`)},
			}}
		_, fs, err := t.Plan(m, ctx)
		Expect(err).NotTo(HaveOccurred())
		var got []string
		for _, f := range fs {
			got = append(got, f.Code)
			Expect(f.Target).To(Equal("claude-alt"))
		}
		Expect(got).To(ContainElements("claude-alt/allowed-tools", "claude-alt/permission-mode", "mcp-cwd-dropped",
			"claude-alt/runtime-hook-path-unsafe", "claude-alt/runtime-hook-event-invalid", "claude-alt/settings-local-not-ignored"))
		for _, c := range got {
			Expect(c).NotTo(HavePrefix("claude-code/"))
		}
		Expect(fs).To(ContainElement(HaveField("Text", `claude-alt has no working-directory setting; cwd "/tmp" dropped`)))
		Expect(fs).To(ContainElement(SatisfyAll(HaveField("Code", "claude-alt/settings-local-not-ignored"),
			HaveField("Hint", "add settings.local.json to .gitignore"))), "only Claude Code is known to ignore the file it creates")
	})

	It("name the agentskills spec's own id, not agents", func() {
		t := renamed("agents", "skills-alt")
		ctx := target.PlanContext{Scope: target.Project, ContentDir: "/cache/example/content", PluginRoot: "/p", PluginData: "/d"}
		m := &module.Module{Name: "example", Skills: []module.Skill{{Name: "review", Dir: "skills/review", Doc: doc("---\ndescription: r\nmetadata: v1\n---\nx\n")}}}
		_, fs, err := t.Plan(m, ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(codes(fs)).To(Equal([]string{"warn skills-alt/metadata-not-string review"}))
	})
})
