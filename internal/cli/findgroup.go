package cli

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// groupRule is how a group's members differ: by artifact (the default), by module (the same
// finding in several modules), or by artifact and tools (pre-approved tools).
type groupRule int

const (
	groupArtifacts groupRule = iota
	groupModules
	groupTools
)

type findingGroup struct {
	Findings []target.Finding
	Rule     groupRule
}

// groupKey is what findings must share to merge by artifact: everything but the artifact, with the
// artifact's name replaced where it stands whole in the text and hint (replaceName). A finding
// whose text or hint also holds its artifact's name as an ordinary word, such as a skill lola in
// "run lola sync", gets a key no differently named artifact's finding shares, so it prints on its
// own line and no merged hint turns that word into a placeholder. The caller keeps findings
// without an artifact out.
func groupKey(f target.Finding) string {
	text := replaceName(f.Text, f.Artifact, "\x00")
	hint := replaceName(f.Hint, f.Artifact, "\x00")
	return strings.Join([]string{string(f.Severity), f.Module, f.Target, f.Kind, f.Code, text, hint, fmt.Sprint(f.Forced)}, "\x1f")
}

// sameKey is every field of f but its module: findings of several modules that share it say the
// same thing.
func sameKey(f target.Finding) string {
	return strings.Join([]string{string(f.Severity), f.Scope, f.Target, f.Kind, f.Artifact, f.Code, f.Text, f.Hint, fmt.Sprint(f.Forced)}, "\x1f")
}

// groupFindings groups fs in the order of first appearance, each finding joining the first rule
// whose key it matches: the same finding in two or more modules (groupModules); a component's
// pre-approved tools, per module, target, kind, severity, hint, forced mark and the text before the tools (groupTools); then
// findings of one module that differ only in their artifact (groupArtifacts). A finding without an
// artifact has nothing to list, so it is its own group, as is one whose artifact its group already
// lists: findings that repeat an artifact cannot tell its name from the same word used otherwise.
func groupFindings(fs []target.Finding) []findingGroup {
	modules := map[string]map[string]bool{}
	for _, f := range fs {
		if f.Module == "" {
			continue
		}
		k := sameKey(f)
		if modules[k] == nil {
			modules[k] = map[string]bool{}
		}
		modules[k][f.Module] = true
	}
	var out []findingGroup
	index := map[string]int{}
	join := func(key string, rule groupRule, f target.Finding) {
		if i, ok := index[key]; ok {
			out[i].Findings = append(out[i].Findings, f)
			return
		}
		index[key] = len(out)
		out = append(out, findingGroup{Findings: []target.Finding{f}, Rule: rule})
	}
	for _, f := range fs {
		prefix, _, hasTools := strings.Cut(f.Text, ": ")
		switch {
		case f.Module != "" && len(modules[sameKey(f)]) >= 2:
			k := "m\x1e" + sameKey(f)
			if i, ok := index[k]; ok && slices.ContainsFunc(out[i].Findings, func(g target.Finding) bool { return g.Module == f.Module }) {
				continue // a module repeating the finding is listed once
			}
			join(k, groupModules, f)
		case f.Module != "" && f.Artifact != "" && strings.HasSuffix(f.Code, "/allowed-tools") && hasTools:
			join("t\x1e"+strings.Join([]string{f.Module, f.Scope, f.Target, f.Kind, f.Code, prefix, string(f.Severity), f.Hint, fmt.Sprint(f.Forced)}, "\x1f"), groupTools, f)
		case f.Artifact == "":
			out = append(out, findingGroup{Findings: []target.Finding{f}})
		default:
			k := "a\x1e" + groupKey(f)
			if i, ok := index[k]; ok && slices.ContainsFunc(out[i].Findings, func(g target.Finding) bool { return g.Artifact == f.Artifact }) {
				out = append(out, findingGroup{Findings: []target.Finding{f}})
				continue
			}
			join(k, groupArtifacts, f)
		}
	}
	return out
}

// kindNoun is a finding kind (target.Finding.Kind) counted: an s on every kind but mcp, whose
// artifacts are servers, and "findings" for none.
func kindNoun(kind string) string {
	switch kind {
	case "":
		return "findings"
	case "mcp":
		return "MCP servers"
	}
	return kind + "s"
}

// groupLines renders g: one finding as its line (findingLine); two or more as a summary, styled as
// the first and with its hint once, then a line per member naming what sets it apart: its module,
// its artifact and tools, or its artifact.
func groupLines(s style, g findingGroup) []string {
	first := g.Findings[0]
	if len(g.Findings) == 1 {
		return strings.Split(findingLine(s, first), "\n")
	}
	merged := first
	var detail []string
	switch g.Rule {
	case groupModules:
		merged.Module = ""
		merged.Text += fmt.Sprintf(" (%d modules)", len(g.Findings))
		for _, f := range g.Findings {
			detail = append(detail, untrusted.Text(f.Module, untrusted.MaxName))
		}
	case groupTools:
		prefix, _, _ := strings.Cut(first.Text, ": ")
		merged.Target, merged.Kind, merged.Artifact = "", "", ""
		merged.Text = fmt.Sprintf("%s (%d %s %s)", prefix, len(g.Findings), first.Target, kindNoun(first.Kind))
		for _, f := range g.Findings {
			_, tools, _ := strings.Cut(f.Text, ": ")
			detail = append(detail, untrusted.Text(f.Artifact, untrusted.MaxName)+": "+tools)
		}
	default:
		// Describe shows the kind only beside an artifact, so the kind rides on the target.
		merged.Artifact = ""
		merged.Target = strings.TrimSpace(first.Target + " " + first.Kind)
		merged.Text = replaceName(first.Text, first.Artifact, "…") + fmt.Sprintf(" (%d %s)", len(g.Findings), kindNoun(first.Kind))
		// The hint came from the first member; its artifact name must not read as if it applied to all.
		merged.Hint = replaceName(first.Hint, first.Artifact, "<artifact>")
		for _, f := range g.Findings {
			detail = append(detail, untrusted.Text(f.Artifact, untrusted.MaxName))
		}
	}
	lines := strings.Split(findingLine(s, merged), "\n")
	for _, d := range detail {
		lines = append(lines, foldDetail+d)
	}
	return lines
}

// replaceName replaces by with each occurrence of name, a non-empty artifact name, in s that
// stands whole: a letter, digit, - or _ on either side, or a . before it, makes it part of a
// longer word. Text names an artifact as skills/x/SKILL.md, x.md or "x", so / and a . after it
// end the name, while a . before it is a suffix such as SKILL.md's, not the artifact md. The
// characters of name itself (which may hold - . or /) are matched literally.
func replaceName(s, name, by string) string {
	inWord := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' }
	var b strings.Builder
	for i := 0; ; {
		j := strings.Index(s[i:], name)
		if j < 0 {
			b.WriteString(s[i:])
			return b.String()
		}
		j += i
		end := j + len(name)
		before, _ := utf8.DecodeLastRuneInString(s[:j])
		after, _ := utf8.DecodeRuneInString(s[end:])
		if (j == 0 || !inWord(before) && before != '.') && (end == len(s) || !inWord(after)) {
			b.WriteString(s[i:j])
			b.WriteString(by)
			i = end
			continue
		}
		b.WriteString(s[i : j+1])
		i = j + 1
	}
}
