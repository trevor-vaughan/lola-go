// Package untrusted makes text that lola did not write safe to print: names, paths and values
// from module content, remote sources and the tools lola runs.
package untrusted

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxName caps one untrusted name lola prints: an archive entry, a path, a URL, a key.
const MaxName = 256

// Text makes text chosen by a remote party safe to print: every C0 control except newline,
// DEL, every C1 control and every byte that is not UTF-8 becomes '?', so the text cannot drive
// the terminal; every format character (Unicode Cf: bidi overrides, zero-width characters,
// BOM) and line or paragraph separator (Zl, Zp) also becomes '?', so text cannot render
// differently from what it is. Each continuation line is indented by two spaces, so it cannot pose as lola's
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
		case replaced(s, i, r):
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

// IsPlain reports whether s holds nothing Text would replace and no newline: a value lola
// accepts in a file someone else may have written prints as it is, on one line.
func IsPlain(s string) bool {
	for i, r := range s {
		if r == '\n' || replaced(s, i, r) {
			return false
		}
	}
	return true
}

// replaced reports whether Text prints the rune r, at byte i of s, as '?': an invalid byte, a
// control other than newline, a format character or a line or paragraph separator.
func replaced(s string, i int, r rune) bool {
	switch {
	case r == utf8.RuneError && !strings.HasPrefix(s[i:], "\uFFFD"):
		return true
	case r == '\n':
		return false
	default:
		return r < 0x20 || r >= 0x7f && r <= 0x9f || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r)
	}
}

// CleanErr returns err without the file names an *fs.PathError or *os.LinkError carries, so an
// error built from untrusted names prints only names that went through Text.
func CleanErr(err error) error {
	if pe, ok := errors.AsType[*fs.PathError](err); ok {
		return pe.Err
	}
	if le, ok := errors.AsType[*os.LinkError](err); ok {
		return le.Err
	}
	return err
}
