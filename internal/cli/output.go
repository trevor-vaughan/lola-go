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
type style struct{ color, plain, paged, verbose bool }

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
	return style{color: a.color(cmd.OutOrStdout()), verbose: cmd.Flags().Lookup("verbose") == nil || given(cmd, "verbose")}
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
// than the terminal (Plan 10 §2.1).
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

// writeError prints err per the output contract.
func writeError(w io.Writer, err error, asJSON bool) {
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
		fmt.Fprintf(w, "  hint: %s\n", h)
	}
}
