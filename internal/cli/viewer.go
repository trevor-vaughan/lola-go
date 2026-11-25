package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"unicode"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"
)

// viewAnswer is how the viewer was left: in read mode q and Esc close it (viewClose) and Ctrl+C
// cancels (viewCancel), which ends the command when a question follows; with a question, Enter
// answers (viewAnswered), Esc goes back (viewBack), q quits (viewQuit) and Ctrl+C cancels.
type viewAnswer int

const (
	viewCancel viewAnswer = iota
	viewBack
	viewQuit
	viewClose
	viewAnswered
)

// viewerModel is lola's full-screen viewer: a viewport over content, wrapped to the
// terminal's width and wrapped again when it is resized, above the rule line, which says where the
// screen is, and the key line. With a question (prompt) only its answer letters, Enter, Backspace,
// Esc, q and Ctrl+C answer it (promptKey); in the pager the other keys scroll and search. nav
// places the question in its flow, as keyed places a form: Esc goes back when nav.Back is set and
// otherwise says q quits, and q asks quitQuestion first with two or more steps answered. s styles
// the footer, so NO_COLOR and LOLA_COLOR apply to it.
//
// / opens a search: type a term, Enter jumps to the first line at or below the top that holds it
// (all lowercase matches any case) and Esc drops the typing; an empty term clears the search.
// n and N (Ctrl+N and Ctrl+P with a question, where n answers) step through the matching lines,
// the current one in reverse video, wrapping at the ends. A term cut across two wrapped lines is
// not found.
//
// A report's folds (reportBuf) show closed, ▸; tab and shift+tab highlight the next or previous
// summary in reverse video, space opens or closes it (or pages with none highlighted), and Esc in
// read mode clears the highlight before it closes.
type viewerModel struct {
	vp viewport.Model
	// blocks is the report shown; open says, per fold of it in order, whether it shows open; hl is
	// the fold whose summary is highlighted, -1 for none; foldRows is, per fold, the first wrapped
	// line of its summary and how many it takes; foldEnd is, per fold, the wrapped line after it,
	// its detail included when it is open.
	blocks   []reportBlock
	open     []bool
	hl       int
	foldRows [][2]int
	foldEnd  []int
	answer   viewAnswer
	nav      Nav
	asking   bool // q asked quitQuestion
	// then is the question asked once the viewer closes; a read-mode footer then says q continues
	// to it instead of quitting.
	then string
	// prompt is the question asked in the viewer; nil in read mode. Its Show is not
	// used: blocks are its text.
	prompt *Question
	// inline draws the prompt line alone, under the text lola printed before the program: no
	// viewport and no alternate screen.
	inline bool
	field  string // the answer letter typed, "" when none
	note   string // the hint or the first-step note after the field, until the next key
	done   bool   // the prompt is answered or left: its line shows no cursor and no note
	width  int
	s      style
	// plain is the wrapped text's lines without styling, which search reads.
	plain     []string
	searching bool   // / is open: typed holds the term so far
	typed     string // the term being typed
	term      string // the term searched, "" when no search is active
	matches   []int  // the lines of plain that hold term
	hidden    int    // the detail lines of the closed folds that hold term, which plain lacks
	cur       int    // the index in matches of the line shown in reverse video
}

// newReportViewer is a viewer over report b, its folds closed, sized to a terminal of width
// columns and height rows, the last of which is the footer's.
func newReportViewer(b *reportBuf, width, height int) viewerModel {
	m := viewerModel{vp: viewport.New(), blocks: b.blocks, open: make([]bool, b.folds()), hl: -1}
	m.resize(width, height)
	return m
}

// newReportPrompt is a viewer asking q about report b, its folds closed, on a terminal of width
// columns and height rows: inline, the prompt line alone (b is empty: lola printed the text); else
// the pager with the question in it.
func newReportPrompt(b *reportBuf, q Question, inline bool, width, height int) viewerModel {
	m := viewerModel{vp: viewport.New(), blocks: b.blocks, open: make([]bool, b.folds()), hl: -1, prompt: &q, inline: inline, nav: q.Nav}
	m.resize(width, height)
	return m
}

// resize fits the viewer to a terminal of width columns and height rows, wrapping the text again
// and keeping the top line in range.
func (m *viewerModel) resize(width, height int) {
	m.width = width
	m.vp.SetWidth(max(width, 1))
	m.vp.SetHeight(max(height-m.footerRows(), 1))
	m.layout()
	m.vp.SetYOffset(m.vp.YOffset())
	if m.term != "" {
		m.find(m.term)
	} else {
		m.restyle()
	}
}

// layout renders the blocks, each fold open or closed, wrapped to the width when it is known, into
// the viewport and plain, and records where each fold's summary is and where the fold ends.
func (m *viewerModel) layout() {
	var rows []string
	m.foldRows, m.foldEnd = make([][2]int, len(m.open)), make([]int, len(m.open))
	for _, l := range renderLines(m.blocks, m.open) {
		text := l.text
		if m.width > 0 {
			text = ansi.Wrap(text, m.width, "")
		}
		wrapped := strings.Split(text, "\n")
		if l.fold >= 0 {
			m.foldRows[l.fold] = [2]int{len(rows), len(wrapped)}
		}
		rows = append(rows, wrapped...)
		switch {
		case l.fold >= 0:
			m.foldEnd[l.fold] = len(rows)
		case l.detail >= 0:
			m.foldEnd[l.detail] = len(rows)
		}
	}
	content := strings.Join(rows, "\n")
	m.vp.SetContent(content)
	m.plain = strings.Split(ansi.Strip(content), "\n")
}

// collect sets term as the search and the lines of plain that hold it (all lowercase matches any
// case), and counts the detail lines of the closed folds that hold it (hidden); an empty term
// clears the search.
func (m *viewerModel) collect(term string) {
	m.term, m.matches, m.cur, m.hidden = term, nil, 0, 0
	if term == "" {
		return
	}
	fold := term == strings.ToLower(term)
	holds := func(line string) bool {
		if fold {
			line = strings.ToLower(line)
		}
		return strings.Contains(line, term)
	}
	for i, line := range m.plain {
		if holds(line) {
			m.matches = append(m.matches, i)
		}
	}
	i := 0
	for _, bl := range m.blocks {
		if !bl.fold {
			continue
		}
		if !m.open[i] {
			for _, d := range bl.detail {
				if holds(ansi.Strip(d)) {
					m.hidden++
				}
			}
		}
		i++
	}
}

// find searches the wrapped lines for term and shows the first match at or below the top line, or
// the first one if none is; it clears the search for an empty term.
func (m *viewerModel) find(term string) {
	m.collect(term)
	for i, line := range m.matches {
		if line >= m.vp.YOffset() {
			m.cur = i
			break
		}
	}
	m.show()
}

// show scrolls to the current match, a third of the way down the screen so the lines above it
// show too.
func (m *viewerModel) show() {
	if len(m.matches) > 0 {
		m.vp.SetYOffset(max(m.matches[m.cur]-m.vp.Height()/3, 0))
	}
	m.restyle()
}

// restyle puts the current match and the highlighted fold's summary in reverse video.
func (m *viewerModel) restyle() {
	match, from, to := -1, -1, -1
	if len(m.matches) > 0 {
		match = m.matches[m.cur]
	}
	if m.hl >= 0 {
		from, to = m.foldRows[m.hl][0], m.foldRows[m.hl][0]+m.foldRows[m.hl][1]
	}
	if match < 0 && from < 0 {
		m.vp.StyleLineFunc = nil
		return
	}
	m.vp.StyleLineFunc = func(i int) lipgloss.Style {
		if i == match || i >= from && i < to {
			return lipgloss.NewStyle().Reverse(true)
		}
		return lipgloss.NewStyle()
	}
}

// moveFold highlights the fold delta away, wrapping at the ends (the first, or with shift+tab the
// last, when none is), and scrolls its summary into view.
func (m *viewerModel) moveFold(delta int) {
	n := len(m.open)
	switch {
	case m.hl < 0 && delta > 0:
		m.hl = 0
	case m.hl < 0:
		m.hl = n - 1
	default:
		m.hl = (m.hl + delta + n) % n
	}
	row := m.foldRows[m.hl]
	if row[0] < m.vp.YOffset() || row[0]+row[1] > m.vp.YOffset()+m.vp.Height() {
		m.vp.SetYOffset(max(row[0]-m.vp.Height()/3, 0))
	}
	m.restyle()
}

// toggle opens or closes the highlighted fold, keeping the top line, and lays the text out again;
// a fold it opens is scrolled into view, as much of its detail as fits under its summary (the
// summary at the top when it does not all fit). A search keeps its term and its matches are those
// of the lines now shown.
func (m *viewerModel) toggle() {
	m.open = slices.Clone(m.open)
	m.open[m.hl] = !m.open[m.hl]
	top, cur := m.vp.YOffset(), m.cur
	m.layout()
	if m.open[m.hl] {
		start, end := m.foldRows[m.hl][0], m.foldEnd[m.hl]
		switch {
		case start < top:
			top = start
		case end > top+m.vp.Height():
			top = min(end-m.vp.Height(), start)
		}
	}
	m.vp.SetYOffset(top)
	m.collect(m.term)
	m.cur = min(cur, max(len(m.matches)-1, 0))
	m.restyle()
}

// reprint is the text the viewer leaves in the scrollback: Folded, with the folds the user opened
// shown open.
func (m viewerModel) reprint() string { return joinLines(renderLines(m.blocks, m.open)) }

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
			m.answer = viewClose
			return m, tea.Quit
		case "esc":
			if m.hl >= 0 {
				m.hl = -1
				m.restyle()
				return m, nil
			}
			m.answer = viewClose
			return m, tea.Quit
		case "tab", "shift+tab":
			if len(m.open) > 0 {
				delta := 1
				if msg.String() == "shift+tab" {
					delta = -1
				}
				m.moveFold(delta)
			}
			return m, nil
		case "space":
			if m.hl >= 0 {
				m.toggle()
				return m, nil
			}
		case "ctrl+c":
			return m, tea.Quit
		case "/":
			m.searching, m.typed = true, ""
			return m, nil
		case "n", "ctrl+n":
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

// footerRows is how many rows the lines under the text take: the rule line and the key line's rows
// (keyRows); with a question, its prompt line's rows at their widest between them.
func (m viewerModel) footerRows() int {
	if m.prompt != nil {
		return 1 + promptRows(*m.prompt, m.width) + m.keyRows()
	}
	return 1 + m.keyRows()
}

// keyRows is how many rows the key line takes: the rows keyLineFor's keys wrap to at the width
// (wrapKeys), at most two, whatever a search adds, so the text keeps its height while one runs.
func (m viewerModel) keyRows() int {
	line, _ := m.baseKeys()
	return len(wrapKeys(line, m.width, 2))
}

// baseKeys is keyLineFor's key line for this viewer, and the keys that step through a search's
// matches in it.
func (m viewerModel) baseKeys() (string, string) {
	mode, next := keysRead, "n N  next/prev"
	switch {
	case m.prompt != nil:
		mode, next = keysPrompt, "ctrl+n ctrl+p  next/prev" // n is an answer here
	case m.then != "":
		mode = keysContinue
	}
	return keyLineFor(mode, m.prompt, m.then, len(m.open) > 0), next
}

// wrapKeys lays line's keys, separated by " · ", into rows of at most width columns (0 for no
// limit), at most limit of them, a key never split; what the last row cannot hold is cut, ending
// "…", as is a key wider than a row.
func wrapKeys(line string, width, limit int) []string {
	if width <= 0 {
		return []string{line}
	}
	var rows []string
	for k := range strings.SplitSeq(line, " · ") {
		last := len(rows) - 1
		switch {
		case last >= 0 && (ansi.StringWidth(rows[last]+" · "+k) <= width || len(rows) == limit):
			rows[last] += " · " + k
		default:
			rows = append(rows, k)
		}
	}
	for i, r := range rows {
		rows[i] = ansi.Truncate(r, width, "…")
	}
	return rows
}

// footer is the lines under the text: the rule line, the
// prompt line padded to its widest rows when a question is asked, then the key line.
func (m viewerModel) footer() string {
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

// keyLine is the keys under the rule, wrapped to the width in keyRows rows (wrapKeys): keyLineFor's,
// after the search's term, which match is current (or, with none, how many lines the closed folds
// hide that hold it) and the keys that step through them; while a term is typed, the typing.
func (m viewerModel) keyLine() string {
	line, next := m.baseKeys()
	if m.term != "" {
		found := "no match"
		if m.hidden > 0 {
			found += fmt.Sprintf(" (%d in closed folds — tab, space to open)", m.hidden)
		}
		if len(m.matches) > 0 {
			found = fmt.Sprintf("%d of %d", m.cur+1, len(m.matches))
		}
		line = "/" + m.term + " " + found + " · " + next + " · " + line
	}
	rows := wrapKeys(line, m.width, m.keyRows())
	if m.searching {
		rows = append(wrapKeys("/"+m.typed+"█  enter search · esc cancel", m.width, 1), make([]string, len(rows)-1)...)
	}
	return strings.Join(rows, "\n")
}

// view shows b, folded, in the viewer on stdout, on the alternate screen, and returns once the
// user leaves it, how. It then prints the text there (reprint), so it stays in the scrollback with
// the folds the user opened. then names the question asked next, for the key line.
func (a *app) view(cmd *cobra.Command, b *reportBuf, then string) (viewAnswer, error) {
	out := cmd.OutOrStdout()
	width, height := a.width(out), a.height(out)
	m := newReportViewer(b, width, height)
	m.s, m.then = style{color: a.color(out), bold: a.bold(out)}, then
	final, err := a.runViewer(cmd, out, m, width, height)
	if err != nil {
		return viewCancel, err
	}
	if _, err := io.WriteString(out, final.reprint()); err != nil {
		return viewCancel, err
	}
	return final.Answer(), nil
}

// runViewer runs m on out, a terminal width columns wide and height rows high, until it quits, and
// returns it (m itself when the program never ran it). Its keys come from stdin, or from the
// terminal when stdin is not one. Ctrl+C arriving as a signal ends it as Ctrl+C typed does; stdin
// ending sends it stdinEnded, so it draws its last frame, and returns errStdinEnded.
func (a *app) runViewer(cmd *cobra.Command, out io.Writer, m viewerModel, width, height int) (viewerModel, error) {
	final, err := runProgram(cmd.InOrStdin(), out, m, width, height)
	vm, ok := final.(viewerModel)
	if !ok {
		vm = m
	}
	if err != nil && !errors.Is(err, tea.ErrInterrupted) {
		return vm, err
	}
	return vm, nil
}

// runProgram runs m on out, a terminal width columns wide and height rows high, until it quits,
// and returns its last model (nil when it never ran). Its keys come from in, or from the terminal
// when in is a stdin that is not one. stdin ending sends m stdinEnded, so it draws its last frame,
// and is errStdinEnded; Ctrl+C arriving as a signal is tea.ErrInterrupted.
func runProgram(in io.Reader, out io.Writer, m tea.Model, width, height int) (tea.Model, error) {
	var ended atomic.Bool
	var p *tea.Program
	if f, ok := in.(*os.File); ok && (f != os.Stdin || isTTY(f)) {
		in = &eofFile{File: f, ended: func() { ended.Store(true); p.Send(stdinEnded{}) }}
	}
	opts := []tea.ProgramOption{tea.WithOutput(out), tea.WithWindowSize(width, height)}
	if in != os.Stdin {
		opts = append(opts, tea.WithInput(in))
	}
	p = tea.NewProgram(m, opts...)
	final, err := p.Run()
	if ended.Load() {
		return final, errStdinEnded
	}
	return final, err
}
