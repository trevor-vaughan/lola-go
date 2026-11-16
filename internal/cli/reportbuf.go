package cli

import (
	"fmt"
	"io"
	"slices"
	"strings"
)

// Fold markers: a closed fold's summary, an open one's, and the indent of its detail lines.
const (
	foldClosed = "▸ "
	foldOpen   = "▾ "
	foldDetail = "    "
)

// foldFooter closes a report that has a closed fold, after the outcome lines.
const foldFooter = "▸ lines are folded; -v prints them in full"

// folder is a writer that holds folds for the viewer to open: printFold hands it each one, and
// printFoldFooter the footer, instead of printing them.
type folder interface {
	Fold(summary string, detail []string)
	FoldFooter(line string)
}

// reportBuf holds a report as it is written: plain text, and the folds and footer printFold and
// printFoldFooter hand it, in order. The viewer opens and closes its folds; every other output
// prints Folded. Indent, when set, goes before every line written while it is set. A nil reportBuf
// is empty.
type reportBuf struct {
	Indent string
	blocks []reportBlock
}

// reportBlock is one piece of a report: text (whole lines), a fold, or the footer (footer set).
type reportBlock struct {
	indent  string
	text    []byte
	fold    bool
	summary string
	detail  []string
	footer  string
}

// reportLine is a rendered line; fold is the index of the fold whose summary it is, and detail
// that of the open fold whose detail line it is; -1 for any other line.
type reportLine struct {
	text         string
	fold, detail int
}

// Write holds p as text under the current Indent. It expects whole lines: a partial line left
// when Indent changes is rendered as a line of its own, the rest of it another.
func (b *reportBuf) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if n := len(b.blocks); n > 0 {
		if last := &b.blocks[n-1]; !last.fold && last.footer == "" && last.indent == b.Indent {
			last.text = append(last.text, p...)
			return len(p), nil
		}
	}
	b.blocks = append(b.blocks, reportBlock{indent: b.Indent, text: slices.Clone(p)})
	return len(p), nil
}

// Fold holds a fold: summary, and detail, the lines it shows open.
func (b *reportBuf) Fold(summary string, detail []string) {
	b.blocks = append(b.blocks, reportBlock{indent: b.Indent, fold: true, summary: summary, detail: slices.Clone(detail)})
}

// FoldFooter holds the footer line where it is written; only the last one renders, and only while a
// fold is closed.
func (b *reportBuf) FoldFooter(line string) {
	b.blocks = append(b.blocks, reportBlock{indent: b.Indent, footer: line})
}

// folds is how many folds b holds.
func (b *reportBuf) folds() int {
	if b == nil {
		return 0
	}
	n := 0
	for _, bl := range b.blocks {
		if bl.fold {
			n++
		}
	}
	return n
}

// Folded is b as printed outside the viewer: its text, each fold closed, and the footer.
func (b *reportBuf) Folded() string {
	if b == nil {
		return ""
	}
	return joinLines(renderLines(b.blocks, nil))
}

// Opened is b as printed where nothing can open a fold, before a question asked inline: each fold
// open as -v prints it, its summary unmarked over its detail, and no footer.
func (b *reportBuf) Opened() string {
	if b == nil {
		return ""
	}
	open := make([]bool, b.folds())
	for i := range open {
		open[i] = true
	}
	lines := renderLines(b.blocks, open)
	for i, l := range lines {
		if l.fold >= 0 {
			lines[i].text = strings.Replace(l.text, foldOpen, "", 1)
		}
	}
	return joinLines(lines)
}

// ReplayTo writes b into w: into another reportBuf its blocks, folds kept, each line under w's
// Indent too; into any other writer, Folded. Like Write it expects whole lines: b's text is kept as
// its own blocks, so a partial line w ends with and one b starts with render as two lines.
func (b *reportBuf) ReplayTo(w io.Writer) {
	if b == nil {
		return
	}
	to, ok := w.(*reportBuf)
	if !ok {
		fmt.Fprint(w, b.Folded())
		return
	}
	for _, bl := range b.blocks {
		bl.indent, bl.text = to.Indent+bl.indent, slices.Clone(bl.text)
		to.blocks = append(to.blocks, bl)
	}
}

// renderLines renders blocks, fold i open when open[i] (marked ▾, its detail under it) and closed
// otherwise (marked ▸); the footer is rendered where its last block is, while a fold is closed.
func renderLines(blocks []reportBlock, open []bool) []reportLine {
	last := -1
	for i, bl := range blocks {
		if bl.footer != "" {
			last = i
		}
	}
	var out []reportLine
	at, closed, fold := -1, false, 0
	for i, bl := range blocks {
		switch {
		case bl.fold:
			if fold < len(open) && open[fold] {
				out = append(out, reportLine{bl.indent + foldOpen + bl.summary, fold, -1})
				for _, d := range bl.detail {
					out = append(out, reportLine{bl.indent + foldDetail + d, -1, fold})
				}
			} else {
				closed = true
				out = append(out, reportLine{bl.indent + foldClosed + bl.summary, fold, -1})
			}
			fold++
		case bl.footer != "":
			if i == last {
				at = len(out)
			}
		default:
			for _, l := range strings.Split(strings.TrimSuffix(string(bl.text), "\n"), "\n") {
				out = append(out, reportLine{bl.indent + l, -1, -1})
			}
		}
	}
	if closed && at >= 0 {
		out = slices.Insert(out, at, reportLine{blocks[last].indent + blocks[last].footer, -1, -1})
	}
	return out
}

// joinLines is lines as text, each ending in a newline.
func joinLines(lines []reportLine) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.text)
		b.WriteByte('\n')
	}
	return b.String()
}

// textReport is text as a report with no fold.
func textReport(text string) *reportBuf {
	b := &reportBuf{}
	_, _ = b.Write([]byte(text))
	return b
}

// printFold writes a fold, summary over its detail lines. Under -v or --plain it prints both, the
// detail indented; a folder holds it for the viewer; any other writer gets the summary alone,
// marked ▸. Both already carry untrusted.Text where they hold author text.
func printFold(w io.Writer, s style, summary string, detail []string) {
	if s.verbose || s.plain {
		fmt.Fprintln(w, summary)
		for _, d := range detail {
			fmt.Fprintln(w, foldDetail+d)
		}
		return
	}
	if f, ok := w.(folder); ok {
		f.Fold(summary, detail)
		return
	}
	fmt.Fprintln(w, foldClosed+summary)
}

// printFoldFooter ends a report that printed a closed fold: a folder holds the line, which shows
// while a fold is still closed; any other writer prints it.
func printFoldFooter(w io.Writer, s style) {
	line := s.info(foldFooter)
	if f, ok := w.(folder); ok {
		f.FoldFooter(line)
		return
	}
	fmt.Fprintln(w, line)
}
