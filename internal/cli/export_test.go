package cli

import (
	"bytes"
	"os"
)

// ShowText is what q.Show prints, in the style the huh adapter prints a question's text in without
// color; "" when q has none. The cli_test package's fake Prompters read questions through it.
func ShowText(q Question) string {
	if q.Show == nil {
		return ""
	}
	var b bytes.Buffer
	q.Show(&b, style{asking: true})
	return b.String()
}

// ShowTextColored is ShowText in color, as the huh adapter prints a question's text when stderr
// takes color.
func ShowTextColored(q Question) string {
	if q.Show == nil {
		return ""
	}
	var b bytes.Buffer
	q.Show(&b, style{color: true, asking: true})
	return b.String()
}

// ResolveHint is nil when words (lola first) name a command of the real tree whose flags parse and
// whose positional arguments its Args rule accepts; it runs nothing.
func ResolveHint(words []string) error {
	root, err := NewRootCmd(os.Getenv)
	if err != nil {
		return err
	}
	c, rest, err := root.Find(words[1:])
	if err != nil {
		return err
	}
	if err := c.ParseFlags(rest); err != nil {
		return err
	}
	return c.ValidateArgs(c.Flags().Args())
}
