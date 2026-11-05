package cli

import (
	"fmt"

	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"
)

// clearLine returns the cursor to the line's start and erases the line.
const clearLine = "\r\x1b[2K"

// progress shows what a slow step is working on, on stderr where a person waits, so a long run
// does not look hung. It returns the function that reports a step and the one that clears what
// was shown, which a caller runs before it prints or asks anything. Both do nothing unless
// stderr is a terminal, so scripts and test buffers see no extra output. On a terminal that
// redraws, each step replaces the last in one line; in accessible mode (no cursor control) each
// is a line of its own.
func (a *app) progress(cmd *cobra.Command) (report func(string), done func()) {
	out := cmd.ErrOrStderr()
	if !a.termErr && !isTTY(out) {
		return func(string) {}, func() {}
	}
	if a.accessible() {
		return func(step string) { fmt.Fprintln(out, "… "+step) }, func() {}
	}
	width := a.width(out)
	shown := false
	return func(step string) {
			line := "… " + step
			if width > 1 {
				line = ansi.Truncate(line, width-1, "…")
			}
			fmt.Fprint(out, clearLine+line)
			shown = true
		}, func() {
			if shown {
				fmt.Fprint(out, clearLine)
				shown = false
			}
		}
}
