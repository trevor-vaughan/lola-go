package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"
)

// viewAnswer is how the viewer was answered (Plan 10 §2.1): in confirm mode y applies, c asks to
// change an answer, and n, q, Esc or Ctrl+C cancel. Read mode always ends in viewCancel, which its
// callers ignore.
type viewAnswer int

const (
	viewCancel viewAnswer = iota
	viewApply
	viewChange
)

// viewerModel is lola's full-screen viewer (Plan 10 §2): a viewport over content, wrapped to the
// terminal's width and wrapped again when it is resized, above a footer that always shows the
// position and the keys. In confirm mode only y, c and the cancel keys answer; there is no default
// answer. s styles the footer, so NO_COLOR and LOLA_COLOR apply to it.
type viewerModel struct {
	vp      viewport.Model
	content string
	confirm bool
	// noChange drops the c key and its footer entry, for a review with no answer to change.
	noChange bool
	answer   viewAnswer
	width    int
	s        style
}

// newViewer is a viewer over content sized to a terminal of width columns and height rows, the
// last of which is the footer's.
func newViewer(content string, confirm bool, width, height int) viewerModel {
	m := viewerModel{vp: viewport.New(), content: strings.TrimSuffix(content, "\n"), confirm: confirm}
	m.resize(width, height)
	return m
}

// resize fits the viewer to a terminal of width columns and height rows, wrapping the content
// again and keeping the top line in range.
func (m *viewerModel) resize(width, height int) {
	m.width = max(width, 1)
	m.vp.SetWidth(m.width)
	m.vp.SetHeight(max(height-1, 1))
	m.vp.SetContent(ansi.Wrap(m.content, m.width, ""))
	m.vp.SetYOffset(m.vp.YOffset())
}

// Answer is how a confirm-mode viewer was answered; viewCancel in read mode.
func (m viewerModel) Answer() viewAnswer { return m.answer }

func (m viewerModel) Init() tea.Cmd { return nil }

func (m viewerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.resize(msg.Width, msg.Height)
		return m, nil
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
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
		case "n":
			if m.confirm {
				return m, tea.Quit
			}
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

// View is the visible lines above the footer, on the alternate screen.
func (m viewerModel) View() tea.View {
	v := tea.NewView(m.vp.View() + "\n" + m.footer())
	v.AltScreen = true
	return v
}

// footer is the position, "lines 41–80 of 312 · 25%", in bold, then the keys, cut to the width.
func (m viewerModel) footer() string {
	total := m.vp.TotalLineCount()
	first := min(m.vp.YOffset()+1, total)
	last := min(m.vp.YOffset()+m.vp.Height(), total)
	pos := fmt.Sprintf("lines %d–%d of %d · %d%%", first, last, total, int(m.vp.ScrollPercent()*100))
	keys := "↑↓/jk line · space/b page · g/G top/end · q quit"
	if m.confirm {
		keys = "↑↓/jk line · space/b page · g/G top/end · y apply · c change · n cancel"
		if m.noChange {
			keys = "↑↓/jk line · space/b page · g/G top/end · y apply · n cancel"
		}
	}
	return ansi.Truncate(m.s.strong(pos)+"  "+m.s.paint("36", keys), m.width, "…")
}

// view shows content in the viewer on the terminal, on the alternate screen, and returns once the
// user quits: in read mode on stdout, in confirm mode on stderr, where lola asks its questions,
// with the answer (viewCancel in read mode). It then prints content where it showed it, so it stays in the terminal's
// scrollback. The keys come from stdin, or from the terminal when stdin is not one. Ctrl+C
// arriving as a signal is a quit.
func (a *app) view(cmd *cobra.Command, content string, confirm, change bool) (viewAnswer, error) {
	out := cmd.OutOrStdout()
	if confirm {
		out = cmd.ErrOrStderr()
	}
	width, height := a.width(out), a.height(out)
	m := newViewer(content, confirm, width, height)
	m.s, m.noChange = style{color: a.color(out)}, !change
	opts := []tea.ProgramOption{tea.WithOutput(out), tea.WithWindowSize(width, height)}
	if in := cmd.InOrStdin(); in != os.Stdin {
		opts = append(opts, tea.WithInput(in))
	}
	final, err := tea.NewProgram(m, opts...).Run()
	if err != nil && !errors.Is(err, tea.ErrInterrupted) {
		return viewCancel, err
	}
	if _, err := io.WriteString(out, content); err != nil {
		return viewCancel, err
	}
	if vm, ok := final.(viewerModel); ok {
		return vm.Answer(), nil
	}
	return viewCancel, nil
}
