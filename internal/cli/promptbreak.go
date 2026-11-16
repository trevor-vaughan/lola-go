package cli

import (
	"fmt"
	"io"
	"strings"
)

// breakRows is how many rows promptBreak takes: the blank line and the rule.
const breakRows = 2

// promptBreak sets a prompt line or a form apart from what printed before it, so the eye finds
// where to answer: a blank line, then a bold rule of ━ across the terminal (80 columns when its
// width is unknown). In accessible mode it is "---", unstyled, which a screen reader reads once.
func promptBreak(w io.Writer, s style, width int, accessible bool) {
	if accessible {
		fmt.Fprint(w, "\n---\n")
		return
	}
	if width <= 0 {
		width = 80
	}
	fmt.Fprint(w, "\n"+s.strong(strings.Repeat("━", width))+"\n")
}
