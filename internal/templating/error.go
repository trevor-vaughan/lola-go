package templating

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/osteele/liquid/expressions"
	"github.com/osteele/liquid/parser"

	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Codes of the refusals Render returns (Plan 7 §11.2).
const (
	codeTooLarge        = "template-too-large"
	codeInvalidUTF8     = "template-invalid-utf8"
	codeSyntax          = "template-syntax"
	codeUndefinedVar    = "template-undefined-variable"
	codeUndefinedFilter = "template-undefined-filter"
	codeOutputTooLarge  = "template-output-too-large"
	codeTimeout         = "template-timeout"
	codePanic           = "template-panic"
)

// Byte caps on the author-derived text an Error holds (Plan 7 §5.5).
const (
	maxReason = 512 // liquid's cause text, a panic value
	maxFile   = 256
)

// Error is a template refusal. File, Reason and IncludedFrom passed untrusted.Text, so they
// print as they are; Line is a line of File, 0 when no line applies. For a refusal in a partial,
// File is the partial's and IncludedFrom the partial's first include site in graph
// (depth-first) order, <file>:<line>: liquid does not report which site was rendering.
type Error struct {
	Code         string
	File         string
	Line         int
	Reason       string
	IncludedFrom string
}

func (e *Error) Error() string {
	s := e.File + ": " + e.Reason
	if e.Line > 0 {
		s = fmt.Sprintf("%s:%d: %s", e.File, e.Line, e.Reason)
	}
	if e.IncludedFrom != "" {
		s += " (included from " + e.IncludedFrom + ")"
	}
	return s
}

// newError returns an *Error for file, cleaning the file name. Author text in the reason's
// arguments must be cleaned by the caller, with its own cap.
func newError(code, file string, line int, format string, args ...any) *Error {
	return &Error{Code: code, File: clean(file, maxFile), Line: line, Reason: fmt.Sprintf(format, args...)}
}

// clean makes author-derived text safe to print, cut to limit bytes.
func clean(s string, limit int) string { return untrusted.Text(s, limit) }

// stopped is the refusal of a render whose context is done: template-timeout past a deadline,
// else the context's own error.
func stopped(ctx context.Context, file string, timeout time.Duration) error {
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ctx.Err()
	}
	return newError(codeTimeout, file, 0, "took longer than %s to render; lola stops a template after %s", timeout, timeout)
}

// fromLiquid maps an error from liquid's compile or render of src, a region of the template
// whose include graph is g, to an *Error: one of lola's own that the writer or a filter returned
// through liquid as it is; one liquid reports under the path it compiled a partial under in that
// partial, at its own line, with its first include site; any other in src (mapLiquid).
func (g *graph) fromLiquid(src Source, err parser.Error) error {
	if e, ok := errors.AsType[*Error](err); ok {
		return e
	}
	if n, ok := g.keys[err.Path()]; ok {
		return n.refusal(mapLiquid(n.src, err))
	}
	return mapLiquid(src, err)
}

// mapLiquid maps an error from liquid's compile or render of src to an *Error: the store's or a
// filter's refusal at the include's or the filter's line; an undefined variable, with the markup
// it is in; an undefined filter, with the allowlisted name it is close to; else template-syntax
// with liquid's line and cleaned reason: the cause's text, or for an error without a cause the
// message liquidMessage cuts out of Error(), never the location prefix and suffix around it.
func mapLiquid(src Source, err parser.Error) error {
	line := err.LineNumber()
	if r, ok := errors.AsType[*storeRefusal](err); ok {
		return newError(r.code, src.File, line, "%s", r.reason)
	}
	if fe, ok := errors.AsType[expressions.FilterError](err); ok {
		if e, ok := errors.AsType[*Error](fe.Err); ok {
			return e
		}
		if r, ok := errors.AsType[*filterRefusal](fe.Err); ok && r.code == codeFrontmatterValue && r.reason == frontBreak {
			return frontValueRefusal(src, line)
		} else if ok {
			return newError(r.code, src.File, line, "%s", r.reason)
		}
	}
	if ie, ok := errors.AsType[expressions.InterpreterError](err); ok && ie == "undefined variable" {
		// liquid names no variable, so the markup tells the author which one
		if tok, ok := markupOn(src, line); ok {
			return newError(codeUndefinedVar, src.File, line, "undefined variable in %s", clean(tok, maxToken))
		}
		return newError(codeUndefinedVar, src.File, line, "undefined variable")
	}
	if uf, ok := errors.AsType[expressions.UndefinedFilter](err); ok {
		return newError(codeUndefinedFilter, src.File, line, "undefined filter %q%s", clean(string(uf), maxToken), filterHint(string(uf)))
	}
	reason := liquidMessage(err)
	if cause := err.Cause(); cause != nil {
		reason = cause.Error()
	}
	// liquid quotes the markup it compiled, which withValueCheck extended: the author sees theirs
	reason = strings.ReplaceAll(strings.ReplaceAll(reason, " | "+frontValueFilter, ""), " | "+frontBreakFilter, "")
	return newError(codeSyntax, src.File, line, "%s", clean(reason, maxReason))
}

// markupOn returns the one tag or object that starts on line of src, and false when none or
// more than one does. raw and comment blocks, which liquid never evaluates, and tags that hold
// no variable do not count.
func markupOn(src Source, line int) (string, bool) {
	var (
		found []string
		skip  string // the end tag of the raw or comment block the token is in
	)
	for _, tok := range parser.Scan(src.Body, parser.SourceLoc{Pathname: src.File, LineNo: src.Line}, nil) {
		switch {
		case skip != "":
			if tok.Type == parser.TagTokenType && tok.Name == skip {
				skip = ""
			}
		case tok.Type == parser.TagTokenType && (tok.Name == "raw" || tok.Name == "comment"):
			skip = "end" + tok.Name
		case tok.Type == parser.TagTokenType && slices.Contains([]string{"endif", "endunless", "endcase", "endfor", "else", "break", "continue"}, tok.Name):
			// a tag that never reads a variable; when and elsif can, so they count
		case (tok.Type == parser.TagTokenType || tok.Type == parser.ObjTokenType) && tok.SourceLoc.LineNo == line:
			found = append(found, tok.Source)
		}
	}
	if len(found) != 1 {
		return "", false
	}
	return found[0], true
}

// filterHint names the one allowlisted filter within two edits of name, else lists them all.
func filterHint(name string) string {
	names := slices.Sorted(maps.Keys(allowlist(context.Background(), "", DefaultLimits())))
	var near []string
	for _, n := range names {
		if editDistance(name, n) <= 2 {
			near = append(near, n)
		}
	}
	if len(near) == 1 {
		return fmt.Sprintf(" (did you mean %q?)", near[0])
	}
	return "; lola templates have: " + strings.Join(names, ", ")
}

// editDistance is the Levenshtein distance between a and b, in bytes.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// liquidMessage returns the message of a liquid error that has no cause, the text its Error()
// puts between the location prefix and the " in <path>" suffix.
func liquidMessage(err parser.Error) string {
	s := strings.TrimPrefix(err.Error(), "Liquid error")
	if err.LineNumber() > 0 {
		s = strings.TrimPrefix(s, fmt.Sprintf(" (line %d)", err.LineNumber()))
	}
	s = strings.TrimPrefix(s, ": ")
	if err.Path() != "" {
		s = strings.TrimSuffix(s, " in "+err.Path())
	}
	return s
}

// sizeText writes a byte count as lola's messages do: "256 KiB", "1 MiB", "70 KiB" (rounded
// up), or "10 bytes" under 1 KiB.
func sizeText(n int) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d bytes", n)
	case n%(1<<20) == 0:
		return fmt.Sprintf("%d MiB", n>>20)
	default:
		return fmt.Sprintf("%d KiB", (n+1<<10-1)>>10)
	}
}
