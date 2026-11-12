package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/errs"
)

// answerWords are the words an answer letter stands for: typed in their place in accessible mode,
// and named for Enter's default in the hint and the key line.
var answerWords = map[rune]string{'y': "yes", 'n': "no", 'c': "change"}

// parseAnswer reads input as an answer to a question taking answers ("yN", see Question): input
// trimmed, empty is the default (not ok when there is none); one of the letters, or its word
// (yes, no, change), in either case, is that letter, lowercased; anything else is not ok.
func parseAnswer(input, answers string) (rune, bool) {
	input = strings.ToLower(strings.TrimSpace(input))
	for _, r := range answers {
		l := unicode.ToLower(r)
		switch {
		case input == "" && unicode.IsUpper(r):
			return l, true
		case input != "" && (input == string(l) || input == answerWords[l]):
			return l, true
		}
	}
	return 0, false
}

// answerBrackets is answers as the prompt shows them: "[y/N]", "[y/c/n]".
func answerBrackets(answers string) string {
	return "[" + strings.Join(strings.Split(answers, ""), "/") + "]"
}

// answerHint is what the prompt says to a key or a line it does not take: the letters, and what
// Enter alone means when there is a default: "answer y or n (Enter means no)", "answer y, c or n".
func answerHint(answers string) string {
	var letters []string
	def := ""
	for _, r := range answers {
		l := unicode.ToLower(r)
		if unicode.IsUpper(r) {
			def = answerWords[l]
		}
		letters = append(letters, string(l))
	}
	hint := "answer " + strings.Join(letters[:len(letters)-1], ", ") + " or " + letters[len(letters)-1]
	if def != "" {
		hint += " (Enter means " + def + ")"
	}
	return hint
}

// ask asks q on stderr, under its text: inline, the text printed and the prompt line under it,
// when the two fit the terminal (fits) or paging prints (app.paging); in the viewer with the
// question in it when they do not fit; through $LOLA_PAGER the text is paged first and q asked
// inline after. It returns the answer, lowercased; ErrBack for Esc with a screen to go back to;
// a quit for q; ErrAborted for Ctrl+C; errStdinEnded when stdin ends.
func (a *app) ask(cmd *cobra.Command, q Question) (rune, error) {
	w := cmd.ErrOrStderr()
	s := style{color: a.color(w), asking: true}
	show := q.Show
	if show == nil {
		show = func(io.Writer, style) {}
	}
	var text bytes.Buffer
	show(&text, style{color: s.color, asking: true, paged: true})
	mode := a.paging(cmd)
	width, height := a.width(w), a.height(w)
	switch {
	case text.Len() == 0:
	case mode.argv != nil:
		if problem := a.runPager(cmd, mode, bytes.NewReader(text.Bytes())); problem != "" {
			fmt.Fprintln(w, style{color: s.color}.warn(problem+"; showing it here"))
			show(w, s)
		}
	case mode.viewer && width > 0 && height > 0 && !fits(text.String(), promptRows(q, width), width, height):
		return a.askIn(cmd, newPrompt(text.String(), q, false, width, height), text.String(), width, height)
	case mode.viewer:
		fmt.Fprint(w, text.String())
	default:
		show(w, s)
	}
	return a.askIn(cmd, newPrompt("", q, true, width, height), "", width, height)
}

// askIn runs m, a prompt, on stderr and returns its answer as ask does. After a pager the text,
// then the prompt line (answered, or empty when it was left or stdin ended), is printed there, so
// both stay in the scrollback; inline, the prompt line is the program's last frame.
func (a *app) askIn(cmd *cobra.Command, m viewerModel, text string, width, height int) (rune, error) {
	w := cmd.ErrOrStderr()
	m.s = style{color: a.color(w)}
	m, err := a.runViewer(cmd, w, m, width, height)
	if err != nil || m.answer != viewAnswered {
		m.field = ""
	}
	if errors.Is(err, errStdinEnded) && m.prompt.Trust != 0 {
		err = errs.WithHints(errInputEnded, hintTerminal,
			"or pass "+m.prompt.Trust.Arg()+" to consent without asking")
	}
	if m.inline && !m.done { // a signal ended the program: its last frame lacked the newline
		m.done = true
		if _, werr := fmt.Fprintln(w, m.promptView()); werr != nil && err == nil {
			err = werr
		}
	}
	if !m.inline {
		m.done = true
		if _, werr := fmt.Fprint(w, strings.TrimSuffix(text, "\n")+"\n"+m.promptView()+"\n"); werr != nil && err == nil {
			err = werr
		}
	}
	if err != nil {
		return 0, err
	}
	switch m.answer {
	case viewAnswered:
		return []rune(m.field)[0], nil
	case viewBack:
		return 0, ErrBack
	case viewQuit:
		return 0, &quitError{color: m.s.color}
	}
	return 0, ErrAborted
}
