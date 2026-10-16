// Package errs attaches coaching hints to errors. The CLI prints them as indented
// "hint:" lines, or as the "hints" array under --json.
package errs

import (
	"errors"
	"runtime"
	"strings"
)

// Error wraps Err with hints. Error() is Err's message, so hints never pollute logs or tests
// that match on the message.
type Error struct {
	Err   error
	Hints []string
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// WithHints wraps err with hints; a nil err stays nil.
func WithHints(err error, hints ...string) error {
	if err == nil {
		return nil
	}
	return &Error{Err: err, Hints: hints}
}

// Hints returns every hint on err's wrap chain, outermost first.
func Hints(err error) []string {
	var out []string
	for err != nil {
		var e *Error
		if !errors.As(err, &e) {
			break
		}
		out = append(out, e.Hints...)
		err = e.Err
	}
	return out
}

// ShellJoin quotes each argument where it needs it, so a command in a hint (or an equivalent
// command) pastes as shown into the user's shell: PowerShell on Windows, a POSIX shell elsewhere.
// The PowerShell form needs PowerShell 7.3 or later: Windows PowerShell 5.1 drops empty arguments
// and mangles embedded double quotes when it runs a program.
func ShellJoin(args []string) string { return shellJoin(runtime.GOOS, args) }

func shellJoin(goos string, args []string) string {
	quote := posixQuote
	if goos == "windows" {
		quote = powerShellQuote
	}
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = quote(a)
	}
	return strings.Join(q, " ")
}

// bareChars mean nothing to either shell inside an argument.
const bareChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-./:=+"

func posixQuote(a string) string {
	if a != "" && strings.Trim(a, bareChars+",@%") == "" {
		return a
	}
	return "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
}

// psSingleQuotes are the characters PowerShell reads as a single quote (language spec §2.3.5.2).
const psSingleQuotes = "'\u2018\u2019\u201A\u201B"

// powerShellQuote leaves a bare only when PowerShell's argument mode passes it unchanged: so not
// with "," (an array), "@" (splatting) or "%" (so never the stop-parsing token --%), and not when it
// starts with "-" and holds a ".", which PowerShell splits in two there (PowerShell/PowerShell#6291).
// Otherwise a goes in single quotes, with every quote character doubled.
func powerShellQuote(a string) string {
	if a != "" && strings.Trim(a, bareChars) == "" && (!strings.HasPrefix(a, "-") || !strings.Contains(a, ".")) {
		return a
	}
	var b strings.Builder
	b.WriteByte('\'')
	for _, r := range a {
		if strings.ContainsRune(psSingleQuotes, r) {
			b.WriteRune(r)
		}
		b.WriteRune(r)
	}
	b.WriteByte('\'')
	return b.String()
}
