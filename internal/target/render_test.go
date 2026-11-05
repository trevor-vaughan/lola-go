package target_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("Expand", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("expands lola's and Claude's plugin variables and leaves the project directory as authored", func() {
		ctx := target.PlanContext{PluginRoot: "/copies/m", PluginData: "/data/m"}
		Expect(target.Expand("${PLUGIN_ROOT} ${CLAUDE_PLUGIN_ROOT} ${PLUGIN_DATA} ${CLAUDE_PLUGIN_DATA} ${CLAUDE_PROJECT_DIR}", ctx)).
			To(Equal("/copies/m /copies/m /data/m /data/m ${CLAUDE_PROJECT_DIR}"))
	})
})

var _ = Describe("SkillTree and SidecarTree", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("templated skill and sidecar files land in their tree with .liquid dropped and the tree's consent; sources never land", func() {
		ctx := target.PlanContext{ContentDir: "/content"}
		s := module.Skill{Name: "review", Dir: "skills/review", Exclude: []string{"SKILL.md.liquid"},
			Files: []module.SkillFile{{Rel: "ref/a.md", Source: "ref/a.md.liquid", Body: "rendered a"}}}
		tree := target.SkillTree("claude-code", s, ctx, ".claude/skills/review")
		Expect(tree.Exclude).To(ConsistOf("SKILL.md.liquid", "SKILL.md", "ref/a.md.liquid"))
		Expect(tree.Overlay).To(Equal([]plan.File{{Path: "ref/a.md", Data: []byte("rendered a")}}))

		consent := &plan.HookConsent{Settings: ".claude/settings.local.json", Kind: "command", Name: "ship"}
		cmd := module.Command{Name: "ship", File: "commands/ship.md", SidecarDir: "commands/ship", Sidecars: []module.Sidecar{
			{File: "commands/ship/notes.md", Source: "commands/ship/notes.md.liquid", Doc: module.Doc{RawFrontmatter: "a: 1\n", Frontmatter: map[string]any{"a": 1}, Body: "N\n"}},
			{File: "commands/ship/plain.md", Doc: module.Doc{Body: "plain\n"}},
		}}
		side := target.SidecarTree("claude-code", cmd, ctx, ".claude/commands/ship", consent)
		Expect(side).To(Equal(plan.Intent{Target: "claude-code", Kind: "command", Op: plan.OpCopyTree, Src: "/content/commands/ship",
			Path: ".claude/commands/ship", Exclude: []string{"notes.md.liquid"},
			Overlay: []plan.File{{Path: "notes.md", Data: []byte("---\na: 1\n---\nN\n")}}, Consent: consent}))
	})
})
