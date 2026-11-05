// Package untrusted makes text that lola did not write safe to print: names, paths and values
// from module content, remote sources and the tools lola runs.
package untrusted

import (
	"errors"
	"io/fs"
	"strings"
	"unicode/utf8"
)

// MaxName caps one untrusted name lola prints: an archive entry, a path, a URL, a key.
const MaxName = 256

// Text makes text chosen by a remote party safe to print: every C0 control except newline,
// DEL, every C1 control and every byte that is not UTF-8 becomes '?', so the text cannot drive
// the terminal. Each continuation line is indented by two spaces, so it cannot pose as lola's
// own output. Text longer than limit bytes is cut and ends in "…".
func Text(s string, limit int) string {
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

// CleanErr returns err without the file name an *fs.PathError carries, so an error built
// from untrusted names prints only names that went through Text.
func CleanErr(err error) error {
	if pe, ok := errors.AsType[*fs.PathError](err); ok {
		return pe.Err
	}
	return err
}
