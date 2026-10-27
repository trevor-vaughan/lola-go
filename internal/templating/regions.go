package templating

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/osteele/liquid/parser"
	"github.com/osteele/liquid/tags"
)

// Codes of the frontmatter region's refusals (Plan 7 §5.2).
const (
	codeFrontmatterFence = "template-frontmatter-fence"
	codeFrontmatterValue = "template-frontmatter-value"
)

// Rendered is a component template's output (Plan 7 §5.2): Front is its rendered frontmatter
// region, without the fences, when the template opens one (HasFront), and Body its rendered
// body region. The caller assembles the document from the two, never by splitting their
// concatenation again.
type Rendered struct {
	Front    []byte
	HasFront bool
	Body     []byte
}

// regions splits a component template into its frontmatter region and its body region, each a
// Source whose Line is the file line it starts on. A source without Frontmatter, or whose first
// line (after one byte-order mark) is not exactly ---, is all body. Otherwise the frontmatter
// ends at the first line that is exactly --- in literal text, outside raw and comment, at
// block depth 0; such a line inside a block refuses, as does a frontmatter that no literal ---
// line closes.
func regions(src Source) (front *Source, body Source, err error) {
	s := strings.TrimPrefix(src.Body, "\ufeff")
	if !src.Frontmatter || !strings.HasPrefix(s, "---\n") && !strings.HasPrefix(s, "---\r\n") {
		return nil, src, nil
	}
	start := len(src.Body) - len(s) + strings.IndexByte(s, '\n') + 1 // the frontmatter's first byte
	type block struct {
		name string
		line int
	}
	var (
		open             []block
		inRaw, inComment bool
		off              int
	)
	toks := parser.Scan(src.Body, parser.SourceLoc{Pathname: src.File, LineNo: src.Line}, nil)
	for i, tok := range toks {
		at := off
		off += len(tok.Source)
		switch {
		case inRaw:
			inRaw = tok.Type != parser.TagTokenType || tok.Name != "endraw"
			continue
		case inComment:
			inComment = tok.Type != parser.TagTokenType || tok.Name != "endcomment"
			continue
		case tok.Type == parser.TagTokenType:
			switch tok.Name {
			case "raw":
				inRaw = true
			case "comment":
				inComment = true
			case "if", "unless", "case", "for":
				open = append(open, block{tok.Name, tok.SourceLoc.LineNo})
			case "endif", "endunless", "endcase", "endfor":
				if len(open) > 0 {
					open = open[:len(open)-1]
				}
			}
			continue
		case tok.Type != parser.TextTokenType:
			continue
		}
		last := i == len(toks)-1
		for ls := at; ls < off; {
			nl := strings.IndexByte(src.Body[ls:off], '\n')
			lineEnd, next := off, off
			if nl >= 0 {
				lineEnd, next = ls+nl, ls+nl+1
			}
			whole := ls >= start && (ls == 0 || src.Body[ls-1] == '\n') && (nl >= 0 || last)
			if whole && strings.TrimRight(src.Body[ls:lineEnd], "\r") == "---" {
				line := src.Line + strings.Count(src.Body[:ls], "\n")
				if len(open) > 0 {
					b := open[len(open)-1]
					return nil, Source{}, newError(codeFrontmatterFence, src.File, line,
						"the %s opened on line %d is still open at the closing --- on line %d; close it before the fence", b.name, b.line, line)
				}
				f := Source{Module: src.Module, File: src.File, Line: src.Line + 1, Body: src.Body[start:ls], Frontmatter: true}
				return &f, Source{Module: src.Module, File: src.File, Line: line + 1, Body: src.Body[next:]}, nil
			}
			ls = next
		}
	}
	return nil, Source{}, newError(codeFrontmatterFence, src.File, src.Line, "frontmatter opens with --- but no literal --- line closes it outside a tag")
}

// yamlBreaks are the runes YAML reads as a line break (go.yaml.in/yaml/v3 is_break): a value
// that writes one into frontmatter could start a line of its own there.
const yamlBreaks = "\n\r\u0085\u2028\u2029"

// frontValueFilter is the filter lola appends to every {{ }} before compiling it (withValueCheck):
// in the frontmatter region it refuses a value whose output holds a YAML line break, and a string
// YAML would not read back as itself; in the body region it does nothing (Plan 7 §5.7.1). Templates
// may name it; they get the same check. Every other {{ }} gets frontBreakFilter instead, which
// checks only for line breaks (wholeValue).
const (
	frontValueFilter = "lola_frontmatter_value"
	frontBreakFilter = "lola_frontmatter_break"
)

// frontBreak is the reason of the line-break refusal, which frontValueRefusal words in full.
const frontBreak = "in frontmatter, this {{ }} rendered a line break"

// namedValueFilter is a template's own | lola_frontmatter_value or | lola_frontmatter_break, which
// a refusal's hint leaves out.
var namedValueFilter = regexp.MustCompile(`\s*\|\s*(` + frontValueFilter + `|` + frontBreakFilter + `)\b`)

// endsInJSON matches the markup of a {{ }} whose last filter is json.
var endsInJSON = regexp.MustCompile(`\|\s*json\s*$`)

// The text around a {{ }} on its line when it is the line's whole value (after "key: " or "- ")
// or its whole key (before ":"), each with only whitespace to the line's ends (R-7-57).
var (
	valueBefore = regexp.MustCompile(`^[ \t]*(?:(?:- +)+|(?:- +)*[^\s{}#"'][^{}]*: +)$`)
	valueAfter  = regexp.MustCompile(`^[ \t]*\r?$`)
	keyBefore   = regexp.MustCompile(`^[ \t]*(?:- +)*$`)
	keyAfter    = regexp.MustCompile(`^:(?:[ \t]|\r?$)`)
)

// wholeValue reports whether the {{ }} at body[start:end] is the whole value or the whole key of
// its line, the only places YAML reads its output alone: a value composed with other text
// (v{{ x }}, "{{ x }}" in the author's quotes) is the author's to shape, and is not round-trip
// checked.
func wholeValue(body string, start, end int) bool {
	before := body[strings.LastIndexByte(body[:start], '\n')+1 : start]
	after := body[end:]
	if nl := strings.IndexByte(after, '\n'); nl >= 0 {
		after = after[:nl]
	}
	return valueBefore.MatchString(before) && valueAfter.MatchString(after) || keyBefore.MatchString(before) && keyAfter.MatchString(after)
}

// PlainReader reports how YAML reads s written as a plain block-context scalar value (`k: s`):
// ok when it reads s itself, a string; otherwise what it reads instead, as a refusal names it.
// The caller provides it, so this package parses no YAML.
type PlainReader func(s string) (what string, ok bool)

// withValueCheck returns src's text with " | lola_frontmatter_value" appended to the markup of
// every {{ }} outside raw and comment that is its line's whole value or key and does not end with
// json, and " | lola_frontmatter_break" to every other.
// The token keeps its delimiters, trim markers and the whitespace around its markup, line breaks
// included, so every token stays on its line. Markup the appended filter cannot keep valid stays
// invalid, and refuses at compile as it would as written.
func withValueCheck(src Source) string {
	var b strings.Builder
	inRaw, inComment := false, false
	off := 0
	for _, tok := range parser.Scan(src.Body, parser.SourceLoc{Pathname: src.File, LineNo: max(src.Line, 1)}, nil) {
		start := off
		off += len(tok.Source)
		switch {
		case inRaw:
			inRaw = tok.Type != parser.TagTokenType || tok.Name != "endraw"
		case inComment:
			inComment = tok.Type != parser.TagTokenType || tok.Name != "endcomment"
		case tok.Type == parser.TagTokenType && tok.Name == "raw":
			inRaw = true
		case tok.Type == parser.TagTokenType && tok.Name == "comment":
			inComment = true
		case tok.Type == parser.ObjTokenType && strings.TrimSpace(tok.Args) != "": // {{ }} stays a syntax error as written
			// the markup is the first match of Args after the delimiter: the scanner reads a lone
			// "-" ({{-}}) as markup, not a trim marker, and so must the rebuilt token, whose "- |"
			// would read as one; a space keeps it markup, and the token refuses at compile as written
			at := len("{{") + strings.Index(tok.Source[len("{{"):], tok.Args)
			end := at + len(tok.Args)
			open := tok.Source[:at]
			if open == "{{" && strings.HasPrefix(tok.Args, "-") {
				open = "{{ "
			}
			filter := frontBreakFilter
			if !endsInJSON.MatchString(tok.Args) && wholeValue(src.Body, start, off) {
				filter = frontValueFilter
			}
			b.WriteString(open + tok.Source[at:end] + " | " + filter + tok.Source[end:])
			continue
		}
		b.WriteString(tok.Source)
	}
	return b.String()
}

// frontValue is the lola_frontmatter_value filter of one render, or with read nil its
// lola_frontmatter_break filter; front reports whether the region rendering is the frontmatter.
// A value other than a string prints with its own type, which YAML reads back, so read sees only
// strings.
func frontValue(front *bool, read PlainReader) func(any) (any, error) {
	return func(v any) (any, error) {
		if !*front {
			return v, nil
		}
		if !eachString(v, func(s string) bool { return !strings.ContainsAny(s, yamlBreaks) }) {
			return nil, &filterRefusal{codeFrontmatterValue, frontBreak}
		}
		if s, ok := v.(string); ok && read != nil {
			if what, ok := read(s); !ok {
				return nil, &filterRefusal{codeFrontmatterValue, fmt.Sprintf("in frontmatter, YAML would read %s as %s; end that {{ }} with | json",
					clean(strconv.Quote(s), maxToken), clean(what, maxToken))}
			}
		}
		return v, nil
	}
}

// eachString calls each with every string in the bytes liquid writes for v, through lists and
// maps, map keys included (a map prints in Go's map[k:v] form), each once, until each returns
// false; it reports whether none did. A value of any other kind is passed as fmt prints it,
// which fails closed.
func eachString(v any, each func(string) bool) bool {
	switch v := v.(type) {
	case nil, bool, int, int64, float64:
		return true
	case string:
		return each(v)
	case []any:
		for _, e := range v {
			if !eachString(e, each) {
				return false
			}
		}
		return true
	case tags.IterationKeyedMap:
		return eachEntry(v, each)
	case map[string]any:
		return eachEntry(v, each)
	}
	return each(fmt.Sprint(v))
}

func eachEntry(m map[string]any, each func(string) bool) bool {
	for k, e := range m {
		if !each(k) || !eachString(e, each) {
			return false
		}
	}
	return true
}

// frontValueRefusal is the refusal of the {{ }} at line of src, in the frontmatter region or a
// partial rendered from it, whose output holds a YAML line break: quoted with | json added when
// it is the one {{ }} that starts on the line.
func frontValueRefusal(src Source, line int) error {
	var objs []string
	scanMarkup(src, func(tok parser.Token) bool {
		if tok.Type == parser.ObjTokenType && tok.SourceLoc.LineNo == line {
			objs = append(objs, tok.Args)
		}
		return true
	})
	reason := "in frontmatter, this {{ }} rendered a line break; end it with | json"
	if len(objs) == 1 { // the template's own lola_frontmatter_value checks, never fixes: the hint drops it
		markup := strings.TrimSpace(namedValueFilter.ReplaceAllString(objs[0], ""))
		reason += fmt.Sprintf(" so the value stays one YAML value: {{ %s | json }}", clean(markup, maxToken))
	}
	return newError(codeFrontmatterValue, src.File, line, "%s", reason)
}

// Uses reports whether a tag or object of any of srcs, or of a partial one includes for any
// target, outside raw and comment, reads the binding name: an identifier name that is not a
// property and not inside a string, nor the variable an assign sets or a for loop binds. It
// walks each partial at most once, however many of srcs include it and however often.
func Uses(srcs []Source, p Partials, name string) bool {
	return slices.ContainsFunc(srcs, newUsage(p, name).in)
}

// usage is one module's scan for a binding (Uses): the partial files of each name, indexed
// once, and the files walked, shared by every source, as the result is an OR of them all.
type usage struct {
	p      Partials
	name   string
	byName map[string][]string // partial name to its files, generic and for each target
	seen   map[string]bool
	walks  int // partial files walked
}

func newUsage(p Partials, name string) *usage {
	u := &usage{p: p, name: name, byName: map[string][]string{}, seen: map[string]bool{}}
	for file := range p.Files {
		base := strings.TrimSuffix(strings.TrimPrefix(file, partialsDir), ".liquid")
		n, _, _ := strings.Cut(base, ".")
		u.byName[n] = append(u.byName[n], file)
	}
	return u
}

// in reports whether s, or a partial it includes that no earlier source reached, reads u.name.
func (u *usage) in(s Source) bool {
	found := false
	scanMarkup(s, func(tok parser.Token) bool {
		args := tok.Args
		switch {
		case tok.Type == parser.TagTokenType && tok.Name == "include":
			partial, ok := includeName(args)
			if !ok {
				return true
			}
			for _, file := range u.byName[partial] {
				if !u.seen[file] {
					u.seen[file] = true
					u.walks++
					found = found || u.in(Source{File: file, Line: 1, Body: u.p.Files[file]})
				}
			}
			return !found
		case tok.Type == parser.TagTokenType && (tok.Name == "assign" || tok.Name == "for"):
			bound := leadingName(args)
			args = strings.TrimSpace(args)[len(bound):]
		}
		for i := 0; i < len(args); {
			c := args[i]
			switch {
			case c == '"' || c == '\'':
				i = skipString(args, i)
			case c == '.' && i+1 < len(args) && isIdentStart(args[i+1]):
				i = identEnd(args, i+1)
			case isIdentStart(c):
				end := identEnd(args, i)
				if args[i:end] == u.name {
					found = true
					return false
				}
				i = end
			default:
				i++
			}
		}
		return true
	})
	return found
}

// AttrRef is a literal attrs path that a template or partial reads at File:Line: its keys, as
// attrs.a.b or attrs['a'] names them, up to the first that is not a literal key.
type AttrRef struct {
	File string
	Line int
	Path []string
}

// AttrRefs lists the literal attrs paths that srcs and every partial of p read in tags and
// objects outside raw and comment: those of srcs in the order given, then those of the partials
// in file name order, each file's in the order they appear; a caller that wants another order
// sorts. A path that starts with a computed key (attrs[k]) is not listed.
func AttrRefs(srcs []Source, p Partials) []AttrRef {
	for _, file := range slices.Sorted(maps.Keys(p.Files)) {
		srcs = append(srcs, Source{File: file, Line: 1, Body: p.Files[file]})
	}
	var out []AttrRef
	for _, src := range srcs {
		scanMarkup(src, func(tok parser.Token) bool {
			args := tok.Args
			for i := 0; i < len(args); {
				c := args[i]
				switch {
				case c == '"' || c == '\'':
					i = skipString(args, i)
				case c == '.' && i+1 < len(args) && isIdentStart(args[i+1]):
					i = identEnd(args, i+1) // a property, never the variable attrs
				case isIdentStart(c):
					end := identEnd(args, i)
					if args[i:end] == "attrs" {
						var path []string
						path, end = attrPath(args, end)
						if len(path) > 0 {
							out = append(out, AttrRef{File: src.File, Line: tok.SourceLoc.LineNo, Path: path})
						}
					}
					i = end
				default:
					i++
				}
			}
			return true
		})
	}
	return out
}

// attrPath reads the literal keys that follow attrs at markup[i:]: .<name>, ['<name>'] or
// ["<name>"], with spaces where liquid allows them, and returns them with the index after the
// last.
func attrPath(markup string, i int) ([]string, int) {
	var path []string
	for {
		rest := strings.TrimLeft(markup[i:], " \t\n\v\f\r")
		at := len(markup) - len(rest)
		switch {
		case strings.HasPrefix(rest, ".") && len(rest) > 1 && isIdentStart(rest[1]):
			end := identEnd(markup, at+1)
			path, i = append(path, markup[at+1:end]), end
		case strings.HasPrefix(rest, "["):
			key := strings.TrimLeft(rest[1:], " \t\n\v\f\r")
			if key == "" || key[0] != '"' && key[0] != '\'' {
				return path, i
			}
			kat := len(markup) - len(key)
			end := skipString(markup, kat)
			if end > len(markup) || !strings.HasPrefix(strings.TrimLeft(markup[end:], " \t\n\v\f\r"), "]") {
				return path, i
			}
			path = append(path, markup[kat+1:end-1])
			i = len(markup) - len(strings.TrimLeft(markup[end:], " \t\n\v\f\r")) + 1
		default:
			return path, i
		}
	}
}

// scanMarkup calls each with the tags and objects of src outside raw and comment, until each
// returns false.
func scanMarkup(src Source, each func(parser.Token) bool) {
	walk(src, func(tok parser.Token) bool {
		return tok.Type != parser.TagTokenType && tok.Type != parser.ObjTokenType || each(tok)
	})
}

// walk calls each with the tokens of src that liquid evaluates or writes as text: those outside
// the bodies of raw and comment, until each returns false.
func walk(src Source, each func(parser.Token) bool) {
	inRaw, inComment := false, false
	for _, tok := range parser.Scan(src.Body, parser.SourceLoc{Pathname: src.File, LineNo: max(src.Line, 1)}, nil) {
		switch {
		case inRaw:
			inRaw = tok.Type != parser.TagTokenType || tok.Name != "endraw"
		case inComment:
			inComment = tok.Type != parser.TagTokenType || tok.Name != "endcomment"
		case tok.Type == parser.TagTokenType && tok.Name == "raw":
			inRaw = true
		case tok.Type == parser.TagTokenType && tok.Name == "comment":
			inComment = true
		case !each(tok):
			return
		}
	}
}
