// Package templating renders a module's Liquid templates in a sandbox (Plan 7 §5). A template
// is untrusted input: it sees only plain data, renders through lola's own filters, and runs
// under caps on its size, its output and its time, so a hostile template can at worst make one
// lola run fail with an *Error. The package reads no files: callers pass every source in.
package templating

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/osteele/liquid/parser"
	"github.com/osteele/liquid/render"
	"github.com/osteele/liquid/tags"
)

// Source is one template: the name of the module it belongs to, its content-relative slash
// path, the line of File its Body starts on (1 when zero), and the text. Frontmatter marks a
// component's file, whose frontmatter, when it opens one, renders as a region of its own
// under the rules of Plan 7 §5.2 (RenderDoc).
type Source struct {
	Module      string
	File        string
	Line        int
	Body        string
	Frontmatter bool
}

// Data holds what a template may read: the target id, the scope ("project" or "user"), the
// scope's active profiles in activation order, the module's vars and the component's attributes
// for the target. A var is a string, a bool, an int64, a float64 or a []string; an attribute is
// a string, a bool, an int64, a float64, nil, an []any or a map[string]any of these. Any other
// value refuses the render.
type Data struct {
	Target, Scope string
	Profiles      []string
	Vars, Attrs   map[string]any
}

// Limits caps one render. DefaultLimits returns the caps lola runs with; specs lower them.
type Limits struct {
	Body    int           // bytes of one template
	Assigns int           // assign tags in one template and the partials it includes
	Depth   int           // if, unless, case and for blocks open at once, across includes; parentheses nested in one tag
	Value   int           // size of a filter's input, argument or result
	Items   int           // elements of a list a filter makes
	Output  int           // bytes one render writes
	Timeout time.Duration // wall time of one render
	Reads   int           // partials one render reads
	Chain   int           // partials one chain of includes holds
}

// DefaultLimits returns the caps of Plan 7 §5.4.
func DefaultLimits() Limits {
	return Limits{
		Body:    256 << 10,
		Assigns: 256,
		Depth:   64,
		Value:   64 << 10,
		Items:   4096,
		Output:  1 << 20,
		Timeout: 2 * time.Second,
		Reads:   64,
		Chain:   8,
	}
}

// Render screens src, then renders it for d with the module's partials p; declared names the
// vars the module declares. It
// returns the rendered bytes, or an *Error; a canceled ctx returns ctx's error. Render returns
// by the earlier of ctx's deadline and lim.Timeout even when the template does not stop: a loop
// that neither writes nor filters runs on in its goroutine, keeping a core busy, until it ends
// or the process exits. A caller that gets template-timeout must therefore end the process once
// it has reported the refusal, and render nothing more.
//
// Render renders src whole, as one region, whatever its Frontmatter says: a component's file,
// whose frontmatter renders as a region of its own, renders through RenderDoc.
func Render(ctx context.Context, src Source, p Partials, d Data, lim Limits, declared []string) ([]byte, error) {
	src.Frontmatter = false
	r, err := renderWith(ctx, src, p, d, lim, declared, nil, nil)
	return r.Body, err
}

// RenderDoc is Render for a component's file: a src with Frontmatter that opens one renders its
// frontmatter and body regions as two templates of one file, sharing its deadline, its output
// cap and its assign budget, each with the file's own line numbers and bindings, so an assign in
// one is not seen by the other. A {{ }} in the frontmatter, or in a partial it includes, whose
// output holds a YAML line break refuses unless it ends with the json filter, which escapes them
// all, as does one that does not end with json and renders a string read says YAML would not read
// back as itself (Plan 7 §5.7.1, R-7-U18). read must not be nil.
func RenderDoc(ctx context.Context, src Source, p Partials, d Data, lim Limits, declared []string, read PlainReader) (Rendered, error) {
	return renderWith(ctx, src, p, d, lim, declared, read, nil)
}

// renderWith is RenderDoc with extra filters, registered after lola's own, which specs use to
// reach a panic inside the engine.
func renderWith(ctx context.Context, src Source, p Partials, d Data, lim Limits, declared []string, read PlainReader, extra map[string]any) (Rendered, error) {
	if src.Line < 1 {
		src.Line = 1
	}
	if len(src.Body) > lim.Body {
		return Rendered{}, newError(codeTooLarge, src.File, 0, "is %d bytes; a template file is at most %s", len(src.Body), sizeText(lim.Body))
	}
	if !utf8.ValidString(src.Body) {
		return Rendered{}, newError(codeInvalidUTF8, src.File, src.Line+invalidLine(src.Body), "is not UTF-8; a template file must be UTF-8 text")
	}
	if dl, ok := ctx.Deadline(); ok {
		lim.Timeout = min(lim.Timeout, max(time.Until(dl).Round(time.Millisecond), 0)) // the limit messages name
	}
	ctx, cancel := context.WithTimeout(ctx, lim.Timeout)
	defer cancel() // stops a template that still writes or filters after Render returns
	type result struct {
		out Rendered
		err error
	}
	if ctx.Err() != nil {
		return Rendered{}, stopped(ctx, src.File, lim.Timeout)
	}
	done := make(chan result, 1) // buffered: a goroutine that outlives Render never blocks
	go func() {
		var res result
		defer func() {
			if r := recover(); r != nil {
				res = result{err: newError(codePanic, src.File, 0, "the template engine failed: %s", clean(fmt.Sprint(r), maxReason))}
			}
			done <- res
		}()
		res.out, res.err = run(ctx, src, p, d, lim, declared, read, extra)
	}()
	select {
	case res := <-done:
		if ctx.Err() != nil {
			return Rendered{}, stopped(ctx, src.File, lim.Timeout) // canceled or past the deadline, whatever the render returned
		}
		if res.err != nil {
			return Rendered{}, res.err
		}
		return res.out, nil
	case <-ctx.Done():
		return Rendered{}, stopped(ctx, src.File, lim.Timeout)
	}
}

// run screens src, then compiles and renders each of its regions into one bounded writer. It
// runs in renderWith's goroutine, under its recover.
func run(ctx context.Context, src Source, p Partials, d Data, lim Limits, declared []string, read PlainReader, extra map[string]any) (Rendered, error) {
	g, err := screen(src, p, d.Target, lim, declared)
	if err != nil {
		return Rendered{}, err
	}
	front, body, err := regions(src)
	if err != nil {
		return Rendered{}, err
	}
	cfg := render.NewConfig()
	tags.AddStandardTags(&cfg)
	cfg.UnregisterTag("render")
	cfg.TemplateStore = newStore(src.File, d.Target, p, g.names(), lim.Reads)
	cfg.StrictVariables = true
	for name, fn := range allowlist(ctx, src.File, lim) {
		cfg.AddFilter(name, fn)
	}
	inFront := false // the region rendering, which lola_frontmatter_value checks in the frontmatter only
	cfg.AddFilter(frontValueFilter, frontValue(&inFront, read))
	cfg.AddFilter(frontBreakFilter, frontValue(&inFront, nil))
	for name, fn := range extra {
		cfg.AddFilter(name, fn)
	}
	w := &boundedWriter{ctx: ctx, file: src.File, timeout: lim.Timeout, max: lim.Output}
	var out Rendered
	if front != nil {
		inFront = true
		err := renderRegion(cfg, *front, d, w, g)
		inFront = false
		if err != nil {
			return Rendered{}, err
		}
		out.Front, out.HasFront = bytes.Clone(w.buf.Bytes()), true
		if out.Front == nil {
			out.Front = []byte{}
		}
	}
	split := w.buf.Len()
	if err := renderRegion(cfg, body, d, w, g); err != nil {
		return Rendered{}, err
	}
	out.Body = w.buf.Bytes()[split:]
	return out, nil
}

// renderRegion compiles one region of a template whose include graph is g and renders it into w
// with bindings of its own.
func renderRegion(cfg render.Config, src Source, d Data, w *boundedWriter, g *graph) error {
	node, perr := cfg.Compile(withValueCheck(src), parser.SourceLoc{Pathname: src.File, LineNo: src.Line})
	if perr != nil {
		return g.fromLiquid(src, perr)
	}
	vars, err := bindings(src.File, d)
	if err != nil {
		return err
	}
	if rerr := render.Render(node, w, vars, cfg); rerr != nil {
		return g.fromLiquid(src, rerr)
	}
	return nil
}

// invalidLine returns how many lines of s come before its first byte that is not UTF-8.
func invalidLine(s string) int {
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			return strings.Count(s[:i], "\n")
		}
		i += size
	}
	return 0
}

// boundedWriter holds a render's output, refusing the write that would pass max and every
// write after the deadline.
type boundedWriter struct {
	ctx     context.Context
	file    string
	timeout time.Duration
	max     int
	buf     bytes.Buffer
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if w.ctx.Err() != nil {
		return 0, stopped(w.ctx, w.file, w.timeout)
	}
	if w.buf.Len()+len(p) > w.max {
		return 0, newError(codeOutputTooLarge, w.file, 0, "renders more than %s; lola writes at most %s per file", sizeText(w.max), sizeText(w.max))
	}
	return w.buf.Write(p)
}
