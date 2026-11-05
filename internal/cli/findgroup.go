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

// A group of at least groupMin findings that differ only in their artifact prints as one line
// naming the first groupShown artifacts.
const (
	groupMin   = 4
	groupShown = 3
)

type findingGroup struct{ Findings []target.Finding }

// groupKey is what findings must share to merge: everything but the artifact, with the
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

// groupFindings keeps the order of first appearance. A finding without an artifact has nothing
// to list, so it is its own group, as is one whose artifact its group already lists: findings
// that repeat an artifact cannot tell its name from the same word used otherwise.
func groupFindings(fs []target.Finding) []findingGroup {
	var out []findingGroup
	index := map[string]int{}
	for _, f := range fs {
		if f.Artifact == "" {
			out = append(out, findingGroup{Findings: []target.Finding{f}})
			continue
		}
		k := groupKey(f)
		i, ok := index[k]
		if ok && slices.ContainsFunc(out[i].Findings, func(g target.Finding) bool { return g.Artifact == f.Artifact }) {
			out = append(out, findingGroup{Findings: []target.Finding{f}})
			continue
		}
		if !ok {
			i = len(out)
			index[k] = i
			out = append(out, findingGroup{})
		}
		out[i].Findings = append(out[i].Findings, f)
	}
	return out
}

// groupLine renders a group of groupMin or more as its first finding with the artifact replaced
// by a count and a preview, keeping that finding's severity styling and its hint once.
func groupLine(s style, g findingGroup) string {
	first := g.Findings[0]
	names := make([]string, 0, groupShown)
	for _, f := range g.Findings[:min(groupShown, len(g.Findings))] {
		names = append(names, untrusted.Text(f.Artifact, untrusted.MaxName))
	}
	// Every kind a finding has (target.Finding.Kind) takes an s but mcp, whose artifacts are servers.
	kinds := first.Kind + "s"
	switch first.Kind {
	case "":
		kinds = "findings"
	case "mcp":
		kinds = "MCP servers"
	}
	// Describe shows the kind only beside an artifact, so the kind rides on the target.
	merged := first
	merged.Artifact = ""
	merged.Target = strings.TrimSpace(first.Target + " " + first.Kind)
	merged.Text = replaceName(first.Text, first.Artifact, "…")
	// The hint came from the first member; its artifact name must not read as if it applied to all.
	merged.Hint = replaceName(first.Hint, first.Artifact, "<artifact>")
	merged.Text += fmt.Sprintf(" (%d %s: %s, …; --json lists each)", len(g.Findings), kinds, strings.Join(names, ", "))
	return findingLine(s, merged)
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
