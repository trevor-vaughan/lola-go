package engine_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("RemovalsShown", func() {
	BeforeEach(func() { testenv.Isolate() })
	removal := func(module string, paths ...string) engine.Removal {
		r := engine.Removal{Module: module, Targets: []string{"claude-code"}}
		for _, path := range paths {
			r.Paths = append(r.Paths, engine.RemovedPath{Target: "claude-code", Path: path})
		}
		return r
	}
	It("holds when every path a removal deletes was shown, in any order", func() {
		shown := []engine.Removal{removal("lint", ".claude/commands/lint.md", ".claude/commands/fmt.md"), removal("kit", ".claude/skills/a/SKILL.md")}
		Expect(engine.RemovalsShown(shown, []engine.Removal{removal("kit", ".claude/skills/a/SKILL.md"), removal("lint", ".claude/commands/fmt.md")})).To(BeTrue())
		Expect(engine.RemovalsShown(shown, nil)).To(BeTrue(), "nothing to remove")
	})
	It("fails for a path, a module or a target that was not shown", func() {
		shown := []engine.Removal{removal("lint", ".claude/commands/lint.md")}
		Expect(engine.RemovalsShown(shown, []engine.Removal{removal("lint", ".claude/commands/lint.md", ".claude/commands/extra.md")})).To(BeFalse())
		Expect(engine.RemovalsShown(shown, []engine.Removal{removal("kit", ".claude/commands/lint.md")})).To(BeFalse())
		other := removal("lint")
		other.Paths = []engine.RemovedPath{{Target: "gemini-cli", Path: ".claude/commands/lint.md"}}
		Expect(engine.RemovalsShown(shown, []engine.Removal{other})).To(BeFalse())
		Expect(engine.RemovalsShown(nil, []engine.Removal{removal("lint", ".claude/commands/lint.md")})).To(BeFalse())
	})
	It("compares a removal with no paths by its module and targets", func() {
		shown := []engine.Removal{removal("lint")}
		Expect(engine.RemovalsShown(shown, []engine.Removal{removal("lint")})).To(BeTrue())
		wider := removal("lint")
		wider.Targets = append(wider.Targets, "gemini-cli")
		Expect(engine.RemovalsShown(shown, []engine.Removal{wider})).To(BeFalse())
	})
})
