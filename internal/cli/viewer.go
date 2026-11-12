package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"unicode"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"
)

// viewAnswer is how the viewer was answered: in confirm mode y applies, c asks to
// change an answer, n or Ctrl+C cancel, q quits and Esc goes back a screen. In read mode q and Esc
// close it (viewClose) and Ctrl+C cancels (viewCancel), which ends the command when a question
// follows the viewer. In prompt mode an answer letter and Enter answer (viewAnswered), Esc goes
// back (viewBack), q quits and Ctrl+C cancels.
type viewAnswer int

const (
	viewCancel viewAnswer = iota
	viewApply
	viewChange
	viewBack
	viewQuit
	viewClose
	viewAnswered
)

// viewerModel is lola's full-screen viewer: a viewport over content, wrapped to the
// terminal's width and wrapped again when it is resized, above the rule line, which says where the
// screen is, and the key line (in confirm mode, one footer line). In confirm mode only y, c, n, q,
// Esc and Ctrl+C answer; there is no default answer. nav places a confirm-mode viewer in its flow, as keyed places a form: Esc goes
// back when nav.Back is set and otherwise says q quits, and q asks quitQuestion first with two or
// more steps answered. s styles the footer, so NO_COLOR and LOLA_COLOR apply to it.
//
// / opens a search: type a term, Enter jumps to the first line at or below the top that holds it
// (all lowercase matches any case) and Esc drops the typing; an empty term clears the search.
// n and N (Ctrl+N and Ctrl+P in confirm mode, where n cancels) step through the matching lines,
// the current one in reverse video, wrapping at the ends. A term cut across two wrapped lines is
// not found.
type viewerModel struct {
	vp      viewport.Model
	content string
	confirm bool
	// noChange drops the c key and its footer entry, for a review with no answer to change.
	noChange bool
	answer   viewAnswer
	nav      Nav
	asking   bool // q asked quitQuestion
	first    bool // Esc with no screen to go back to: the footer says q quits
	// then is the question asked once the viewer closes; a read-mode footer then says q continues
	// to it instead of quitting.
	then string
	// prompt is the question asked in the viewer; nil in read and confirm mode. Its Show is not
	// used: content is its text.
	prompt *Question
	// inline draws the prompt line alone, under the text lola printed before the program: no
	// viewport and no alternate screen.
	inline bool
	field  string // the answer letter typed, "" when none
	note   string // the hint or the first-step note after the field, until the next key
	done   bool   // the prompt is answered or left: its line shows no cursor and no note
	width  int
	s      style
	// plain is the wrapped content's lines without styling, which search reads.
	plain     []string
	searching bool   // / is open: typed holds the term so far
	typed     string // the term being typed
	term      string // the term searched, "" when no search is active
	matches   []int  // the lines of plain that hold term
	cur       int    // the index in matches of the line shown in reverse video
}

// newViewer is a viewer over content sized to a terminal of width columns and height rows, the
// last of which is the footer's.
func newViewer(content string, confirm bool, width, height int) viewerModel {
	m := viewerModel{vp: viewport.New(), content: strings.TrimSuffix(content, "\n"), confirm: confirm}
	m.resize(width, height)
	return m
}

// newPrompt is a viewer asking q about content on a terminal of width columns and height rows:
// inline, the prompt line alone (content is "": lola printed the text); else the pager with the
// question in it.
func newPrompt(content string, q Question, inline bool, width, height int) viewerModel {
	m := viewerModel{vp: viewport.New(), content: strings.TrimSuffix(content, "\n"), prompt: &q, inline: inline, nav: q.Nav}
	m.resize(width, height)
	return m
}

// resize fits the viewer to a terminal of width columns and height rows, wrapping the content
// again and keeping the top line in range.
func (m *viewerModel) resize(width, height int) {
	m.width = width
	m.vp.SetWidth(max(width, 1))
	m.vp.SetHeight(max(height-m.footerRows(), 1))
	wrapped := m.content
	if width > 0 {
		wrapped = ansi.Wrap(m.content, width, "")
	}
	m.vp.SetContent(wrapped)
	m.vp.SetYOffset(m.vp.YOffset())
	m.plain = strings.Split(ansi.Strip(wrapped), "\n")
	if m.term != "" {
		m.find(m.term)
	}
}

// find searches the wrapped lines for term and shows the first match at or below the top line,
// or the first one if none is: a term of all lowercase matches any case. It clears the search for
// an empty term.
func (m *viewerModel) find(term string) {
	m.term, m.matches, m.cur = term, nil, 0
	m.vp.StyleLineFunc = nil
	if term == "" {
		return
	}
	fold := term == strings.ToLower(term)
	for i, line := range m.plain {
		if fold {
			line = strings.ToLower(line)
		}
		if strings.Contains(line, term) {
			m.matches = append(m.matches, i)
		}
	}
	for i, line := range m.matches {
		if line >= m.vp.YOffset() {
			m.cur = i
			break
		}
	}
	m.show()
}

// show scrolls to the current match, a third of the way down the screen so the lines above it
// show too, and puts it in reverse video.
func (m *viewerModel) show() {
	if len(m.matches) == 0 {
		return
	}
	line := m.matches[m.cur]
	m.vp.SetYOffset(max(line-m.vp.Height()/3, 0))
	m.vp.StyleLineFunc = func(i int) lipgloss.Style {
		if i == line {
			return lipgloss.NewStyle().Reverse(true)
		}
		return lipgloss.NewStyle()
	}
}

// step moves the current match by delta, wrapping at the ends.
func (m *viewerModel) step(delta int) {
	if len(m.matches) == 0 {
		return
	}
	m.cur = (m.cur + delta + len(m.matches)) % len(m.matches)
	m.show()
}

// typeSearch handles a key while the term is being typed.
func (m viewerModel) typeSearch(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m.leave(viewCancel)
	case "enter":
		m.searching = false
		m.find(m.typed)
	case "esc":
		m.searching = false
	case "backspace":
		if runes := []rune(m.typed); len(runes) > 0 {
			m.typed = string(runes[:len(runes)-1])
		}
	default:
		m.typed += msg.Text
	}
	return m, nil
}

// stdinEnded is the message runViewer sends the viewer when stdin ends: it leaves as Ctrl+C does,
// drawing its last frame, and runViewer returns errStdinEnded.
type stdinEnded struct{}

// leave ends the viewer with answer a. A prompt left unanswered drops its field, so its last line
// never echoes a letter that was not the answer.
func (m viewerModel) leave(a viewAnswer) (viewerModel, tea.Cmd) {
	m.answer, m.done = a, true
	if a != viewAnswered {
		m.field = ""
	}
	return m, tea.Quit
}

// Answer is how the viewer was answered.
func (m viewerModel) Answer() viewAnswer { return m.answer }

func (m viewerModel) Init() tea.Cmd { return nil }

func (m viewerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.resize(msg.Width, msg.Height)
		return m, nil
	case stdinEnded:
		return m.leave(viewCancel)
	case tea.KeyPressMsg:
		m.first = false
		if m.searching {
			return m.typeSearch(msg)
		}
		if m.asking {
			switch msg.String() {
			case "y", "Y":
				return m.leave(viewQuit)
			case "n", "N", "enter", "esc":
				m.asking = false
			case "ctrl+c":
				return m.leave(viewCancel)
			}
			return m, nil
		}
		if m.prompt != nil {
			if next, cmd, taken := m.promptKey(msg); taken {
				return next, cmd
			}
		}
		switch msg.String() {
		case "q":
			switch {
			case !m.confirm:
				m.answer = viewClose
			case m.nav.Answered >= 2:
				m.asking = true
				return m, nil
			default:
				m.answer = viewQuit
			}
			return m, tea.Quit
		case "esc":
			switch {
			case !m.confirm:
				m.answer = viewClose
			case m.nav.Back:
				m.answer = viewBack
			default:
				m.first = true
				return m, nil
			}
			return m, tea.Quit
		case "ctrl+c":
			return m, tea.Quit
		case "y", "c":
			if m.confirm && (msg.String() == "y" || !m.noChange) {
				m.answer = viewApply
				if msg.String() == "c" {
					m.answer = viewChange
				}
				return m, tea.Quit
			}
			return m, nil
		case "/":
			m.searching, m.typed = true, ""
			return m, nil
		case "n", "ctrl+n":
			if msg.String() == "n" && m.confirm {
				return m, tea.Quit
			}
			m.step(1)
			return m, nil
		case "N", "ctrl+p":
			m.step(-1)
			return m, nil
		case "g", "home":
			m.vp.GotoTop()
			return m, nil
		case "G", "end":
			m.vp.GotoBottom()
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

// promptKey applies a key to the prompt: an answer letter, in either case, fills the field
// (replacing one there), Backspace clears it, Enter answers what it holds or the default (the hint
// when there is neither), Esc goes back with a screen to go back to (else the first-step note), q
// quits (asking quitQuestion first with two or more steps answered) and Ctrl+C cancels. taken is
// false for a key the pager scrolls or searches with; inline, every key is the prompt's, and any
// other printable one shows the hint.
func (m viewerModel) promptKey(msg tea.KeyPressMsg) (viewerModel, tea.Cmd, bool) {
	answers := m.prompt.Answers
	m.note = ""
	switch msg.String() {
	case "ctrl+c":
		m, cmd := m.leave(viewCancel)
		return m, cmd, true
	case "q":
		if m.nav.Answered >= 2 {
			m.asking = true
			return m, nil, true
		}
		m, cmd := m.leave(viewQuit)
		return m, cmd, true
	case "esc":
		if m.nav.Back {
			m, cmd := m.leave(viewBack)
			return m, cmd, true
		}
		m.note = firstStepNote(m.nav)
		return m, nil, true
	case "enter":
		if r, ok := parseAnswer(m.field, answers); ok {
			m.field, m.answer, m.done = string(r), viewAnswered, true
			return m, tea.Quit, true
		}
		m.note = answerHint(answers)
		return m, nil, true
	case "backspace":
		m.field = ""
		return m, nil, true
	}
	if r := []rune(msg.Text); len(r) == 1 && strings.ContainsRune(strings.ToLower(answers), unicode.ToLower(r[0])) {
		m.field = string(unicode.ToLower(r[0]))
		return m, nil, true
	}
	if m.inline {
		if msg.Text != "" {
			m.note = answerHint(answers)
		}
		return m, nil, true
	}
	return m, nil, false
}

// promptView is the prompt line wrapped to the width when it is known: the field with a cursor
// while it waits, then the note, or quitQuestion while q asks it; once answered or left, the field
// alone, without the esc note, which no longer applies.
func (m viewerModel) promptView() string {
	field, note := m.field+"█", m.note
	if m.asking {
		note = quitQuestion + " [y/N]"
	}
	q := *m.prompt
	if m.done {
		field, note = m.field, ""
		q.Nav.Back = false
	}
	line := promptLine(m.s, q, field, note)
	if m.width > 0 {
		line = ansi.Wrap(line, m.width, "")
	}
	return line
}

// promptLine is q's prompt line: the question, its answers in brackets in bold ("[y/N]"), field,
// then note and, with a screen to go back to, "· esc back".
func promptLine(s style, q Question, field, note string) string {
	line := q.Prompt + " " + s.strong(answerBrackets(q.Answers)) + " " + field
	if note != "" {
		line += "  " + note
	}
	if q.Nav.Back {
		line += " · esc back"
	}
	return line
}

// promptRows is how many rows q's prompt line takes on a terminal width columns wide at its
// widest: a letter and the cursor in the field, and the longest note it can show; so nothing the
// prompt shows later pushes the text up.
func promptRows(q Question, width int) int {
	notes := []string{answerHint(q.Answers)}
	if !q.Nav.Back {
		notes = append(notes, firstStepNote(q.Nav))
	}
	if q.Nav.Answered >= 2 {
		notes = append(notes, quitQuestion+" [y/N]")
	}
	rows := 1
	for _, n := range notes {
		if width > 0 {
			rows = max(rows, strings.Count(ansi.Wrap(promptLine(style{}, q, "y█", n), width, ""), "\n")+1)
		}
	}
	return rows
}

// View is, inline, the prompt line alone, ending in a newline once it is done: bubbletea erases
// from the last row of the last frame down when the program ends, which would take an answered
// line with it; else the visible lines above the footer, on the alternate screen.
func (m viewerModel) View() tea.View {
	if m.inline {
		if m.done {
			return tea.NewView(m.promptView() + "\n")
		}
		return tea.NewView(m.promptView())
	}
	v := tea.NewView(m.vp.View() + "\n" + m.footer())
	v.AltScreen = true
	return v
}

// footerRows is how many rows the lines under the text take: in confirm mode, its footer's one;
// else the rule line and the key line; with a question, its prompt line's rows at their widest
// between them.
func (m viewerModel) footerRows() int {
	switch {
	case m.confirm:
		return 1
	case m.prompt != nil:
		return 2 + promptRows(*m.prompt, m.width)
	}
	return 2
}

// footer is the lines under the text: in confirm mode confirmFooter, else the rule line, the
// prompt line padded to its widest rows when a question is asked, then the key line.
func (m viewerModel) footer() string {
	if m.confirm {
		return m.confirmFooter()
	}
	lines := []string{m.rule()}
	if m.prompt != nil {
		p := strings.Split(m.promptView(), "\n")
		for len(p) < promptRows(*m.prompt, m.width) {
			p = append(p, "")
		}
		lines = append(lines, p...)
	}
	return strings.Join(append(lines, m.keyLine()), "\n")
}

// rule is the line under the text, bold and across the width: where the screen is,
// "━━ lines 41–80 of 312 · ↓ 232 more ━━…", or "· end" once the last line shows.
func (m viewerModel) rule() string {
	total := m.vp.TotalLineCount()
	first := min(m.vp.YOffset()+1, total)
	last := min(m.vp.YOffset()+m.vp.Height(), total)
	where := "end"
	if last < total {
		where = fmt.Sprintf("↓ %d more", total-last)
	}
	label := fmt.Sprintf("━━ lines %d–%d of %d · %s ", first, last, total, where)
	fill := max(m.width-ansi.StringWidth(label), 0)
	return m.s.strong(ansi.Truncate(label+strings.Repeat("━", fill), m.width, ""))
}

// keyLine is the keys under the rule, cut from the right to the width: keyLineFor's, after the
// search's term, which match is current and the keys that step through them; while a term is
// typed, the typing.
func (m viewerModel) keyLine() string {
	mode, next := keysRead, "n N  next/prev"
	switch {
	case m.prompt != nil:
		mode, next = keysPrompt, "ctrl+n ctrl+p  next/prev" // n is an answer here
	case m.then != "":
		mode = keysContinue
	}
	line := keyLineFor(mode, m.prompt, m.then)
	if m.term != "" {
		found := "no match"
		if len(m.matches) > 0 {
			found = fmt.Sprintf("%d of %d", m.cur+1, len(m.matches))
		}
		line = "/" + m.term + " " + found + " · " + next + " · " + line
	}
	if m.searching {
		line = "/" + m.typed + "█  enter search · esc cancel"
	}
	return ansi.Truncate(line, m.width, "…")
}

// confirmFooter is a review's footer: the position, "lines 41–80 of 312 · 25%", in bold, then the
// answers, esc back and q quit, then the scrolling keys, cut to the width; quitQuestion while q
// asks it, and the first-screen note after Esc there.
func (m viewerModel) confirmFooter() string {
	total := m.vp.TotalLineCount()
	first := min(m.vp.YOffset()+1, total)
	last := min(m.vp.YOffset()+m.vp.Height(), total)
	pos := fmt.Sprintf("lines %d–%d of %d · %d%%", first, last, total, int(m.vp.ScrollPercent()*100))
	keys := "y apply · c change · n cancel"
	if m.noChange {
		keys = "y apply · n cancel"
	}
	if m.nav.Back {
		keys += " · esc back"
	}
	keys += " · q quit · / search · space/b page · g/G top/end"
	if m.term != "" {
		found := "no match"
		if len(m.matches) > 0 {
			found = fmt.Sprintf("%d of %d", m.cur+1, len(m.matches))
		}
		keys = "/" + m.term + " " + found + "  ^n/^p next/prev · " + keys
	}
	switch {
	case m.searching:
		keys = "/" + m.typed + "█  enter search · esc cancel"
	case m.asking:
		keys = quitQuestion + " [y/N]"
	case m.first:
		keys = "first step — q quits"
	}
	return ansi.Truncate(m.s.strong(pos)+"  "+m.s.paint("36", keys), m.width, "…")
}

// view shows content in the viewer on the terminal, on the alternate screen, and returns once the
// user quits: in read mode on stdout, in confirm mode on stderr, where lola asks its questions,
// at nav in its flow, with the answer. It then prints content where it showed it, so it stays in the terminal's
// scrollback. The keys come from stdin, or from the terminal when stdin is not one. Ctrl+C
// arriving as a signal is a quit. then names the question asked next, for the footer of a read-mode viewer.
func (a *app) view(cmd *cobra.Command, content string, confirm, change bool, nav Nav, then string) (viewAnswer, error) {
	out := cmd.OutOrStdout()
	if confirm {
		out = cmd.ErrOrStderr()
	}
	width, height := a.width(out), a.height(out)
	m := newViewer(content, confirm, width, height)
	m.s, m.noChange, m.nav, m.then = style{color: a.color(out)}, !change, nav, then
	final, err := a.runViewer(cmd, out, m, width, height)
	if err != nil {
		return viewCancel, err
	}
	if _, err := io.WriteString(out, content); err != nil {
		return viewCancel, err
	}
	return final.Answer(), nil
}

// runViewer runs m on out, a terminal width columns wide and height rows high, until it quits, and
// returns it (m itself when the program never ran it). Its keys come from stdin, or from the
// terminal when stdin is not one. Ctrl+C arriving as a signal ends it as Ctrl+C typed does; stdin
// ending sends it stdinEnded, so it draws its last frame, and returns errStdinEnded.
func (a *app) runViewer(cmd *cobra.Command, out io.Writer, m viewerModel, width, height int) (viewerModel, error) {
	var ended atomic.Bool
	var p *tea.Program
	in := cmd.InOrStdin()
	if f, ok := in.(*os.File); ok && (f != os.Stdin || isTTY(f)) {
		in = &eofFile{File: f, ended: func() { ended.Store(true); p.Send(stdinEnded{}) }}
	}
	opts := []tea.ProgramOption{tea.WithOutput(out), tea.WithWindowSize(width, height)}
	if in != os.Stdin {
		opts = append(opts, tea.WithInput(in))
	}
	p = tea.NewProgram(m, opts...)
	final, err := p.Run()
	vm, ok := final.(viewerModel)
	if !ok {
		vm = m
	}
	switch {
	case ended.Load():
		return vm, errStdinEnded
	case err != nil && !errors.Is(err, tea.ErrInterrupted):
		return vm, err
	}
	return vm, nil
}
