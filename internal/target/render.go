package target

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
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

// Shell is where Claude runs shell in a command or skill body: inline !`…` at a line start or
// after whitespace, or a fenced block opened with three or more backticks and !.
var Shell = regexp.MustCompile("(?m)(?:^|\\s)!`[^`]+`|^[ \\t]*`{3,}!")

// placeholder finds Claude's $ARGUMENTS, $ARGUMENTS[N] and $N where no single backslash escapes
// the $: Claude reads a doubled backslash as two literal ones, and still fills what follows.
var placeholder = regexp.MustCompile(`(?:^|[^\\]|\\\\)\$(?:ARGUMENTS\[\d+\]|ARGUMENTS(?:$|[^\[])|\d)`)

// SkillRules are the findings for a target that, unlike Claude, neither runs shell nor fills
// argument placeholders in skills: both reach the model as written.
func SkillRules(id string) []ContentRule {
	return []ContentRule{
		{Kind: "skill", Pattern: Shell, Severity: SeverityWarn,
			Code: "skill-shell-dropped", Text: id + " does not run shell in skills; it reaches the model as text"},
		{Kind: "skill", Pattern: placeholder, Severity: SeverityWarn,
			Code: "skill-arguments-dropped", Text: id + " does not fill argument placeholders in skills; they reach the model as written"},
	}
}

// argument is one of Claude's argument placeholders (§8.1): $ARGUMENTS[N], $ARGUMENTS, $N or
// $name. Arguments drops a $ARGUMENTS followed by [ and undeclared names.
var argument = regexp.MustCompile(`\$(?:ARGUMENTS\[(\d+)\]|(ARGUMENTS)|(\d+)|([A-Za-z_][A-Za-z0-9_]*))`)

// Arguments rewrites the argument placeholders in a Claude command's body in one pass: to gets
// each placeholder as written and its 0-based argument index, or -1 for $ARGUMENTS (all
// arguments), and returns its replacement. It also reports whether any placeholder names a
// single argument. doc is the command as written, so its arguments: frontmatter (a list, or a
// whitespace-separated string) declares the $name placeholders. An index too large for an int
// is kept as written. A placeholder whose $ follows exactly one backslash is escaped, a literal
// in Claude (a doubled backslash is two literal ones): to gets it with its backslash and index
// -1, its result replaces both, and it never names a single argument.
func Arguments(doc module.Doc, to func(placeholder string, index int) string) (string, bool) {
	var names []string
	switch v := doc.Frontmatter["arguments"].(type) {
	case string:
		names = strings.Fields(v)
	case []any:
		for _, e := range v {
			names = append(names, fmt.Sprint(e))
		}
	}
	body := doc.Body
	var b strings.Builder
	last, indexed := 0, false
	for _, m := range argument.FindAllStringSubmatchIndex(body, -1) {
		start, end := m[0], m[1]
		escaped := start > 0 && body[start-1] == '\\' && (start == 1 || body[start-2] != '\\')
		index := -1
		switch {
		case m[4] >= 0:
			if strings.HasPrefix(body[end:], "[") {
				continue
			}
		case m[8] >= 0:
			if index = slices.Index(names, body[m[8]:m[9]]); index < 0 {
				continue
			}
		default:
			digits := m[2:4]
			if digits[0] < 0 {
				digits = m[6:8]
			}
			n, err := strconv.Atoi(body[digits[0]:digits[1]])
			if err != nil && !escaped {
				indexed = true
				continue
			}
			index = n
		}
		if escaped {
			start, index = start-1, -1
		}
		indexed = indexed || index >= 0
		b.WriteString(body[last:start])
		b.WriteString(to(body[start:end], index))
		last = end
	}
	b.WriteString(body[last:])
	return b.String(), indexed
}
