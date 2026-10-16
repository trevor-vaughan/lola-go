package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

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

// style renders human output: glyphs always, ANSI color only when enabled, nothing fancy
// under --plain.
type style struct{ color, plain bool }

func (a *app) style(cmd *cobra.Command) style {
	if a.plain {
		return style{plain: true}
	}
	switch a.getenv("LOLA_COLOR") {
	case "1":
		return style{color: true}
	case "0":
		return style{}
	}
	if a.getenv("NO_COLOR") != "" {
		return style{}
	}
	return style{color: isTTY(cmd.OutOrStdout())}
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

// writeError prints err per the output contract.
func writeError(w io.Writer, err error, asJSON bool) {
	hints := errs.Hints(err)
	if hints == nil {
		hints = []string{}
	}
	if asJSON {
		if json.NewEncoder(w).Encode(map[string]any{"error": err.Error(), "hints": hints}) == nil {
			return
		}
		// Encoding failed (or the write did): fall through to the human format.
	}
	fmt.Fprintf(w, "error: %s\n", err)
	for _, h := range hints {
		fmt.Fprintf(w, "  hint: %s\n", h)
	}
}
