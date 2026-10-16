package target

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/plan"
)

// Expand substitutes ${PLUGIN_ROOT}, ${CLAUDE_PLUGIN_ROOT} and ${PLUGIN_DATA} (§5.5).
func Expand(s string, ctx PlanContext) string {
	return strings.NewReplacer(
		"${PLUGIN_ROOT}", ctx.PluginRoot,
		"${CLAUDE_PLUGIN_ROOT}", ctx.PluginRoot,
		"${PLUGIN_DATA}", ctx.PluginData,
	).Replace(s)
}

// ExpandAll applies Expand to each string.
func ExpandAll(in []string, ctx PlanContext) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = Expand(s, ctx)
	}
	return out
}

// ExpandMap applies Expand to each value.
func ExpandMap(in map[string]string, ctx PlanContext) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = Expand(v, ctx)
	}
	return out
}

// WriteText is the WriteFile intent of a text artifact, with ${…} expanded (§5.5).
func WriteText(id, kind, path, body string, ctx PlanContext) plan.Intent {
	return plan.Intent{Target: id, Kind: kind, Op: plan.OpWriteFile, Path: path, Data: []byte(Expand(body, ctx)), Mode: 0o644}
}

// SkillTree copies a skill's directory to dst, except SKILL.md, which each target writes
// itself (possibly rewritten).
func SkillTree(id string, s module.Skill, ctx PlanContext, dst string) plan.Intent {
	return plan.Intent{Target: id, Kind: "skill", Op: plan.OpCopyTree, Src: filepath.Join(ctx.ContentDir, s.Dir), Path: dst,
		Exclude: append(slices.Clone(s.Exclude), "SKILL.md")}
}

// HasAny reports whether root holds the directory dir or any of files: the usual project
// markers of an assistant.
func HasAny(root, dir string, files ...string) bool {
	if fi, err := os.Stat(filepath.Join(root, dir)); err == nil && fi.IsDir() {
		return true
	}
	for _, f := range files {
		if _, err := os.Stat(filepath.Join(root, f)); err == nil {
			return true
		}
	}
	return false
}
