package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/trevor-vaughan/lola-go/internal/errs"
)

// isTTY reports whether a stream is a terminal. Test buffers never are, which keeps output
// deterministic.
func isTTY(s any) bool {
	f, ok := s.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// width is how many columns human output to w may fill: the terminal's when w is one, else
// $COLUMNS when it is a positive number, else 0, no limit, so a pipe or a file keeps every
// character.
func (a *app) width(w any) int {
	if f, ok := w.(*os.File); ok {
		if cols, _, err := term.GetSize(int(f.Fd())); err == nil && cols > 0 {
			return cols
		}
	}
	if n, err := strconv.Atoi(a.getenv("COLUMNS")); err == nil && n > 0 {
		return n
	}
	return 0
}

// height is how many rows the terminal w is on has: the terminal's when w is one, else $LINES
// when it is a positive number, else 0, unknown, which never opens the viewer.
func (a *app) height(w any) int {
	if f, ok := w.(*os.File); ok {
		if _, rows, err := term.GetSize(int(f.Fd())); err == nil && rows > 0 {
			return rows
		}
	}
	if n, err := strconv.Atoi(a.getenv("LINES")); err == nil && n > 0 {
		return n
	}
	return 0
}

// style renders human output: glyphs always, ANSI color only when enabled, nothing fancy
// under --plain. paged is set when the output goes to the viewer or a pager, which shows all of
// it, so printers drop their caps (more). verbose is -v: a run's detail notices are printed, not
// counted (printNotices); a command without -v prints them all, as it did before -v.
// asking marks a plan shown just before the question that applies it, so it is no dry run.
// width is the columns a hint wraps to (hintLine): the terminal's when stdout and stderr both are
// one, else 0, so a pipe or a file gets each hint on one line.
type style struct {
	color, plain, paged, verbose, asking bool
	width                                int
}

// more is the command a capped listing names to list everything, or "" when the output is paged
// and shows everything.
func (s style) more(cmd string) string {
	if s.paged {
		return ""
	}
	return cmd
}

func (a *app) style(cmd *cobra.Command) style {
	if a.plain {
		return style{plain: true}
	}
	s := style{color: a.color(cmd.OutOrStdout()), verbose: cmd.Flags().Lookup("verbose") == nil || given(cmd, "verbose")}
	if (a.termOut || isTTY(cmd.OutOrStdout())) && (a.termErr || isTTY(cmd.ErrOrStderr())) {
		s.width = a.width(cmd.OutOrStdout())
	}
	return s
}

// color decides ANSI color for output to w: LOLA_COLOR, then NO_COLOR, then whether w is a
// terminal. The install flow's forms on stderr follow the same rule.
func (a *app) color(w any) bool {
	switch a.getenv("LOLA_COLOR") {
	case "1":
		return true
	case "0":
		return false
	}
	return a.getenv("NO_COLOR") == "" && isTTY(w)
}

// emit is the one output path: JSON under --json, the run's contexts first (withContext),
// otherwise the human renderer, through page, which opens the viewer when the output is taller
// than the terminal.
func (a *app) emit(cmd *cobra.Command, human func(io.Writer, style), v any) error {
	w := cmd.OutOrStdout()
	if a.json {
		doc, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if doc, err = withContext(doc, a.contexts); err != nil {
			return err
		}
		var out bytes.Buffer
		if err := json.Indent(&out, doc, "", "  "); err != nil {
			return err
		}
		out.WriteByte('\n')
		_, err = w.Write(out.Bytes())
		return err
	}
	a.page(cmd, w, a.style(cmd), human)
	return nil
}

// jsonFielder is an error whose --json document carries more keys than error and hints (a sync
// refused for want of answers carries the changes and removals it needs them for).
type jsonFielder interface {
	error
	JSONFields() map[string]any
}

// writeError prints err per the output contract, its hints wrapped to width (hintLine).
func writeError(w io.Writer, err error, asJSON bool, width int) {
	hints := errs.Hints(err)
	if hints == nil {
		hints = []string{}
	}
	if asJSON {
		doc := map[string]any{}
		if f, ok := errors.AsType[jsonFielder](err); ok {
			maps.Copy(doc, f.JSONFields())
		}
		doc["error"], doc["hints"] = err.Error(), hints
		if json.NewEncoder(w).Encode(doc) == nil {
			return
		}
		// Encoding failed (or the write did): fall through to the human format.
	}
	fmt.Fprintf(w, "error: %s\n", err)
	for _, h := range hints {
		fmt.Fprintln(w, hintLine("  ", h, width))
	}
}

// minHintText is how many columns a wrapped hint keeps for its text after "hint: ": on a narrower
// terminal the hint prints as one line, which the terminal wraps.
const minHintText = 20

// hintLine is the "hint:" line for h after indent, wrapped at spaces to width columns (0 for no
// limit), each continuation under the hint's first word. A command in h, from a word "lola" to a
// word ending in ";" outside single quotes or to the end, is never broken, so it runs as copied;
// one wider than the line gets a line of its own, wider than width.
func hintLine(indent, h string, width int) string {
	lead := indent + "hint: "
	if width == 0 || ansi.StringWidth(lead+h) <= width || width < len(lead)+minHintText {
		return lead + h
	}
	var units []string
	cmd, quoted := "", false
	for word := range strings.SplitSeq(h, " ") { // a command keeps each space; prose needs none twice
		switch {
		case cmd == "" && word == "":
			continue
		case cmd == "" && word != "lola":
			units = append(units, word)
			continue
		case cmd == "":
			cmd = word
		default:
			cmd += " " + word
		}
		for i := 0; i < len(word); i++ {
			switch {
			case quoted:
				quoted = word[i] != '\''
			case word[i] == '\\':
				i++
			case word[i] == '\'':
				quoted = true
			}
		}
		if !quoted && strings.HasSuffix(word, ";") {
			units, cmd = append(units, cmd), ""
		}
	}
	if cmd != "" {
		units = append(units, cmd)
	}
	pad := strings.Repeat(" ", len(lead))
	var lines []string
	cur, empty := lead, true
	for _, u := range units {
		switch {
		case empty:
			cur += u
		case ansi.StringWidth(cur)+1+ansi.StringWidth(u) <= width:
			cur += " " + u
		default:
			lines, cur = append(lines, cur), pad+u
		}
		empty = false
	}
	return strings.Join(append(lines, cur), "\n")
}
