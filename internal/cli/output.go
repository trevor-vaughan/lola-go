package cli

import (
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

// style renders human output: glyphs always, ANSI color only when enabled, nothing fancy
// under --plain.
type style struct{ color, plain bool }

func (a *app) style(cmd *cobra.Command) style {
	if a.plain {
		return style{plain: true}
	}
	return style{color: a.color(cmd.OutOrStdout())}
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

// emit is the one output path: JSON under --json, otherwise the human renderer.
func (a *app) emit(cmd *cobra.Command, human func(io.Writer, style), v any) error {
	w := cmd.OutOrStdout()
	if a.json {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	human(w, a.style(cmd))
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
