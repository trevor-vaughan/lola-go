package cli

import "bytes"

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
