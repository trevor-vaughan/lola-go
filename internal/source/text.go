package source

import (
	"errors"
	"io/fs"
	"strings"
	"unicode/utf8"
)

// Caps for untrusted text that lola prints (design §3.2).
const (
	maxName   = 256     // one name: an archive entry, a module-root candidate, a URL, a status
	maxStderr = 4 << 10 // git's stderr
)

// printable makes text chosen by a remote party safe to print: every C0 control except
// newline, DEL, every C1 control and every byte that is not UTF-8 becomes '?', so the text
// cannot drive the terminal. Each continuation line is indented by two spaces, so it cannot
// pose as lola's own output. Text longer than limit bytes is cut and ends in "…".
func printable(s string, limit int) string {
	cut := len(s) > limit
	if cut {
		s = s[:limit] // a rune split here is not UTF-8 and becomes '?'
	}
	var b strings.Builder
	for i, r := range s {
		switch {
		case r == '\n':
			b.WriteString("\n  ")
		case r == utf8.RuneError && !strings.HasPrefix(s[i:], "\uFFFD"):
			b.WriteByte('?')
		case r < 0x20, r >= 0x7f && r <= 0x9f:
			b.WriteByte('?')
		default:
			b.WriteRune(r)
		}
	}
	if cut {
		b.WriteString("…")
	}
	return b.String()
}

// cleanErr returns err without the file name an *fs.PathError carries, so an error built
// from untrusted names prints only names that went through printable.
func cleanErr(err error) error {
	if pe, ok := errors.AsType[*fs.PathError](err); ok {
		return pe.Err
	}
	return err
}
