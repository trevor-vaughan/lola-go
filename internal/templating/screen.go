package templating

import (
	"slices"
	"strings"

	"github.com/osteele/liquid/parser"
)

// Codes of the screen's refusals (Plan 7 §5.2).
const (
	codeTagUnavailable   = "template-tag-unavailable"
	codeRangeUnavailable = "template-range-unavailable"
	codeTooManyAssigns   = "template-too-many-assigns"
	codeTooDeep          = "template-too-deep"
	codeUndeclaredVar    = "template-undeclared-var"
	codeIncludeName      = "template-include-name"
)

// maxToken caps a piece of markup quoted in a refusal (Plan 7 §5.5).
const maxToken = 120

// mark is a place in a template or partial the include graph needs: a block it opens or an
// include of name, at line, with open blocks open there, the block itself included.
type mark struct {
	include bool
	name    string
	line    int
	open    int
}

// fileInfo is what screenFile finds in one template or partial: its blocks and includes in
// order, and the lines of its assign tags.
type fileInfo struct {
	src     Source
	marks   []mark
	assigns []int
}

// screenFile refuses what liquid would accept but a lola template or partial may not use: a
// denied or unknown tag, a range (.. outside a string literal), blocks or parentheses nested
// deeper than lim.Depth, an include of anything but a quoted partial name, a literal vars name that declared lacks, an object that
// prints vars or attrs whole, and forloop used other than through one of its properties. It
// reads the tokens liquid's parser reads, treating the bodies of raw and comment as text, as the
// parser does. The include graph (screen) adds what spans files: assigns, nesting and includes.
func screenFile(src Source, lim Limits, declared []string) (fileInfo, error) {
	info := fileInfo{src: src}
	var (
		open             int // the if, unless, case and for blocks open at the token
		inRaw, inComment bool
		declaredSet      = make(map[string]bool, len(declared))
	)
	for _, name := range declared {
		declaredSet[name] = true
	}
	for _, tok := range parser.Scan(src.Body, parser.SourceLoc{Pathname: src.File, LineNo: src.Line}, nil) {
		line := tok.SourceLoc.LineNo
		switch {
		case inRaw:
			inRaw = tok.Type != parser.TagTokenType || tok.Name != "endraw"
			continue
		case inComment:
			inComment = tok.Type != parser.TagTokenType || tok.Name != "endcomment"
			continue
		case tok.Type != parser.TagTokenType && tok.Type != parser.ObjTokenType:
			continue
		}
		if tok.Type == parser.TagTokenType {
			if err := screenTag(src.File, line, tok.Name); err != nil {
				return fileInfo{}, err
			}
		}
		if _, ok := includeName(tok.Args); tok.Type == parser.TagTokenType && tok.Name == "include" && !ok { // a path is a bad name, never a range
			return fileInfo{}, newError(codeIncludeName, src.File, line, "include takes a partial name in quotes, such as {%% include 'header' %%}")
		}
		if strings.Contains(unquoted(tok.Args), "..") {
			return fileInfo{}, newError(codeRangeUnavailable, src.File, line, "ranges like (1..n) are not available in lola templates; loop over a list")
		}
		if parenDepth(tok.Args) > lim.Depth {
			return fileInfo{}, newError(codeTooDeep, src.File, line, "parentheses nest more than %d deep", lim.Depth)
		}
		for _, name := range varRefs(tok.Args) {
			if !declaredSet[name] {
				return fileInfo{}, undeclared(src, line, name, declared)
			}
		}
		if tok.Type != parser.TagTokenType {
			switch name := strings.TrimSpace(tok.Args); name {
			case "vars", "attrs": // always maps, which liquid prints in Go's syntax
				return fileInfo{}, newError(codeSyntax, src.File, line, "%s is a map; print one of its keys or loop over it", name)
			}
			if forloopWhole(tok.Args) {
				return fileInfo{}, forloopRefusal(src.File, line)
			}
			continue
		}
		switch tok.Name {
		case "raw":
			inRaw = true
		case "comment":
			inComment = true
		case "assign":
			switch name := leadingName(tok.Args); name {
			case "forloop":
				return fileInfo{}, newError(codeSyntax, src.File, line, "assign cannot set forloop; it belongs to the for tag")
			case "vars", "attrs":
				return fileInfo{}, newError(codeSyntax, src.File, line, "assign cannot set %s; it is lola's", name)
			}
			info.assigns = append(info.assigns, line)
		case "if", "unless", "case", "for":
			if tok.Name == "for" {
				switch name := leadingName(tok.Args); name {
				case "forloop":
					return fileInfo{}, newError(codeSyntax, src.File, line, "for cannot name its variable forloop; it belongs to the for tag")
				case "vars", "attrs":
					return fileInfo{}, newError(codeSyntax, src.File, line, "for cannot name its variable %s; it is lola's", name)
				}
			}
			if open++; open > lim.Depth {
				return fileInfo{}, newError(codeTooDeep, src.File, line, "blocks nest more than %d deep", lim.Depth)
			}
			info.marks = append(info.marks, mark{line: line, open: open})
		case "endif", "endunless", "endcase", "endfor":
			open = max(open-1, 0)
		case "include":
			name, _ := includeName(tok.Args) // a valid name, checked above
			info.marks = append(info.marks, mark{include: true, name: name, line: line, open: open})
		}
		if forloopWhole(tok.Args) {
			return fileInfo{}, forloopRefusal(src.File, line)
		}
	}
	return info, nil
}

// includeName returns the partial an include's markup names: a single- or double-quoted literal
// partial name and nothing else, so lola knows every include before rendering.
func includeName(markup string) (string, bool) {
	s := strings.TrimSpace(markup)
	if len(s) < 2 || s[0] != '"' && s[0] != '\'' || s[len(s)-1] != s[0] {
		return "", false
	}
	name := s[1 : len(s)-1]
	return name, PartialName(name)
}

// leadingName returns the identifier markup starts with: the variable an assign sets or a for
// loop binds. Neither may be forloop: liquid's cycle and loops read it as their own map and
// panic on another value.
func leadingName(markup string) string {
	name := strings.TrimSpace(markup)
	if name == "" || !isIdentStart(name[0]) {
		return ""
	}
	return name[:identEnd(name, 0)]
}

// forloopWhole reports whether markup uses forloop other than through a property, forloop.<name>
// or forloop['<name>']: liquid keeps it as a plain map, which it prints in Go's syntax and
// iterates in random order, and a filter would get it whole. An alias (assign x = forloop) is
// refused too, so no other name can carry it, and so is a bracket key that is not a quoted
// literal, which could compute any key, or one starting with ".", which names liquid's own
// state (forloop['.cycles']), never a property.
func forloopWhole(markup string) bool {
	for i := 0; i < len(markup); {
		c := markup[i]
		switch {
		case c == '"' || c == '\'':
			i = skipString(markup, i)
		case c == '.' && i+1 < len(markup) && isIdentStart(markup[i+1]):
			i = identEnd(markup, i+1) // a property, never the variable forloop
		case isIdentStart(c):
			end := identEnd(markup, i)
			if markup[i:end] == "forloop" {
				rest := strings.TrimLeft(markup[end:], " \t\n\v\f\r")
				key, bracket := strings.CutPrefix(rest, "[")
				key = strings.TrimLeft(key, " \t\n\v\f\r")
				literal := len(key) > 1 && (key[0] == '"' || key[0] == '\'') && key[1] != '.' &&
					strings.HasPrefix(strings.TrimLeft(key[min(skipString(key, 0), len(key)):], " \t\n\v\f\r"), "]")
				property := bracket && literal || !bracket && len(rest) > 1 && rest[0] == '.' && isIdentStart(rest[1])
				if !property {
					return true
				}
			}
			i = end
		default:
			i++
		}
	}
	return false
}

// forloopRefusal is the refusal of forloop used whole.
func forloopRefusal(file string, line int) error {
	return newError(codeSyntax, file, line, "forloop can only be read through one of its properties, like forloop.index")
}

// screenTag refuses a tag name a lola template may not use. Blocks cannot be unregistered from
// liquid, so the screen refuses every tag but these before compiling.
func screenTag(file string, line int, name string) error {
	switch strings.TrimPrefix(name, "end") {
	case "capture", "tablerow":
		return newError(codeTagUnavailable, file, line, "the %s tag is not available in lola templates", strings.TrimPrefix(name, "end"))
	case "render":
		return newError(codeTagUnavailable, file, line, "the render tag is not available in lola templates; use include")
	}
	switch name {
	case "assign", "if", "elsif", "else", "endif", "unless", "endunless", "case", "when", "endcase",
		"for", "endfor", "break", "continue", "cycle", "comment", "endcomment", "raw", "endraw", "include":
		return nil
	}
	return newError(codeTagUnavailable, file, line, "unknown tag %q", clean(name, maxToken))
}

// parenDepth returns how deep parentheses nest in markup, counting those inside quotes too.
func parenDepth(markup string) int {
	depth, deepest := 0, 0
	for _, c := range markup {
		switch c {
		case '(':
			depth++
			deepest = max(deepest, depth)
		case ')':
			depth = max(depth-1, 0)
		}
	}
	return deepest
}

// varRefs returns the names markup reads from vars with a literal, as vars.<name> or
// vars['<name>'], lexing strings and identifiers as liquid's expression scanner does, so a quoted
// "vars.x" is text.
func varRefs(markup string) []string {
	var names []string
	for i := 0; i < len(markup); {
		c := markup[i]
		switch {
		case c == '"' || c == '\'':
			i = skipString(markup, i)
		case c == '.' && i+1 < len(markup) && isIdentStart(markup[i+1]):
			i = identEnd(markup, i+1) // a property, never the variable vars
		case isIdentStart(c):
			end := identEnd(markup, i)
			if markup[i:end] == "vars" && (end == len(markup) || markup[end] != ':') {
				if name, ok := varName(markup[end:]); ok {
					names = append(names, name)
				}
			}
			i = end
		default:
			i++
		}
	}
	return names
}

// varName reads the literal name that follows the identifier vars: .<name>, or ['<name>'] or
// ["<name>"], with spaces where liquid allows them.
func varName(rest string) (string, bool) {
	rest = strings.TrimLeft(rest, " \t\n\v\f\r")
	switch {
	case strings.HasPrefix(rest, ".") && len(rest) > 1 && isIdentStart(rest[1]):
		return rest[1:identEnd(rest, 1)], true
	case strings.HasPrefix(rest, "["):
		rest = strings.TrimLeft(rest[1:], " \t\n\v\f\r")
		if rest == "" || rest[0] != '"' && rest[0] != '\'' {
			return "", false
		}
		end := skipString(rest, 0)
		if end > len(rest) || !strings.HasPrefix(strings.TrimLeft(rest[end:], " \t\n\v\f\r"), "]") {
			return "", false
		}
		return rest[1 : end-1], true
	}
	return "", false
}

// unquoted returns markup with each string literal replaced by a space, the literals read as
// liquid's expression scanner reads them (expressions/scanner.rl): a double-quoted string in
// which a backslash escapes any byte, and a single-quoted string with no escapes. An
// unterminated quote and everything after it stay, so text that is not a string to liquid is
// never taken for one.
func unquoted(markup string) string {
	var b strings.Builder
	for i := 0; i < len(markup); {
		c := markup[i]
		if c != '"' && c != '\'' {
			b.WriteByte(c)
			i++
			continue
		}
		end := skipString(markup, i)
		if end > len(markup) {
			b.WriteString(markup[i:])
			break
		}
		b.WriteByte(' ')
		i = end
	}
	return b.String()
}

// skipString returns the index after the string literal that starts at s[i]: a double-quoted
// string may escape a quote with a backslash, a single-quoted one may not. An unterminated
// string runs to len(s)+1.
func skipString(s string, i int) int {
	q := s[i]
	for j := i + 1; j < len(s); j++ {
		switch {
		case q == '"' && s[j] == '\\':
			j++
		case s[j] == q:
			return j + 1
		}
	}
	return len(s) + 1
}

// isIdentStart reports whether c can start a liquid identifier: a letter, an underscore or
// the first byte of a multi-byte rune.
func isIdentStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

// identEnd returns the index after the identifier that starts at s[i], with its optional
// trailing '?'.
func identEnd(s string, i int) int {
	j := i + 1
	for j < len(s) && (isIdentStart(s[j]) || s[j] == '-' || s[j] >= '0' && s[j] <= '9') {
		j++
	}
	if j < len(s) && s[j] == '?' {
		j++
	}
	return j
}

// undeclared is the refusal of a vars name the module does not declare.
func undeclared(src Source, line int, name string, declared []string) error {
	if len(declared) == 0 {
		return newError(codeUndeclaredVar, src.File, line, "vars.%s is not declared; %s declares no vars", clean(name, maxToken), clean(src.Module, maxToken))
	}
	return newError(codeUndeclaredVar, src.File, line, "vars.%s is not declared; %s declares %s",
		clean(name, maxToken), clean(src.Module, maxToken), clean(strings.Join(slices.Sorted(slices.Values(declared)), ", "), maxReason))
}
