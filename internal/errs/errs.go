// Package errs attaches coaching hints to errors. The CLI prints them as indented
// "hint:" lines, or as the "hints" array under --json.
package errs

import "errors"

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
