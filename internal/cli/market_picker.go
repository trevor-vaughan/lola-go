package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// MarketPickerAction is how the marketplace picker was left (MarketPickerAnswer.Action).
type MarketPickerAction int

const (
	PickerEnter  MarketPickerAction = iota // Enter: act on Own and Known
	PickerAdd                              // a, or Add a source…: add a marketplace
	PickerUpdate                           // u's offer taken: update Own (Known is always empty)
	PickerCancel                           // esc or q: do nothing
)

// MarketPickerStep offers the marketplace picker's rows: Own, your marketplaces, then Known, the
// known ones you have not enabled (engine.PickerRows). Check starts a check of rows, as
// engine.CheckMarkets does, each Check's Index one into rows, until ctx is cancelled; the picker calls
// it only when the user presses u, on the rows u acts on, never on open. PreselectOwn is the IDs of
// the rows of yours ticked on open (the marketplace just added); a known one you have not enabled
// can share an ID, and is not ticked.
type MarketPickerStep struct {
	Own, Known   []engine.PickerRow
	Check        func(ctx context.Context, rows []engine.PickerRow) <-chan engine.Check
	PreselectOwn []string
}

// MarketPickerAnswer is the picker's answer: the action and the IDs of the rows it applies to, Own
// those of your marketplaces and Known those of the known ones you have not enabled, each in row
// order (none for PickerAdd and PickerCancel).
type MarketPickerAnswer struct {
	Action     MarketPickerAction
	Own, Known []string
}

// The picker's network banner, the line a row's failed check prints its hints on, and the
// accessible line prompt.
const (
	networkBanner = "Several sources unreachable; check your network connection"
	pickerPrompt  = "Choose marketplaces (numbers, comma separated): "
	addSourceRow  = "Add a source…"
	pickerIndent  = "      " // a row's detail lines: under its name, past the cursor and checkbox
	nbsp          = "\u00a0" // a space ansi.Wrap never breaks at
)

// marketPicker is the marketplace picker: a filterable multi-select over two-line rows, own then
// known, ended by Add a source…. Keys: x or space ticks, ctrl+a ticks all or none of the rows shown,
// / types a filter (Enter keeps it, Esc drops it), a adds, u checks then offers to update the ticked
// rows or else the cursor's, U does so for every row (whatever the filter shows), Enter acts on the
// ticked rows or else the cursor's (adds on Add a source…), Esc clears a filter and otherwise
// cancels, as q does; Ctrl+C aborts.
//
// u (or U) starts a check run of its rows that Checkable; their statuses fill in live. Once every result
// is in, unless the network banner shows, the footer offers to update those of yours that need it:
// a check found them behind, or they have no check (archive, folder and local sources). A known
// one you have not enabled is checked, never offered: Enter enables it. Enter or y takes the offer
// (PickerUpdate), Esc or n declines it and the picker stays open. A check run's results are read
// one at a time (readCheck) and tagged with the run; u again, or leaving, cancels the run, and the
// results of a run that is not the current one are dropped, so a check that ended because it was
// cancelled never reads as unreachable or counts in Classify.
type marketPicker struct {
	rows   []engine.PickerRow // Own then Known; each Status is the current run's
	own    int                // how many of rows are Own
	reason []string           // per row, its failed check's reason
	ticked []bool
	check  func(context.Context, []engine.PickerRow) <-chan engine.Check

	run     int                // the check run whose results count; 0 before u
	stop    context.CancelFunc // cancels run's checks; nil when none was started
	targets []int              // the rows u acts on in run
	checked []int              // the rows run checks, a Check's Index one into it
	results []engine.Check     // run's results so far
	ended   bool               // every result of run is in
	banner  bool
	offer   []int // the rows the footer offers to update; nil when none is offered

	filter string
	typing bool // / is open: keys type the filter
	cursor int  // in visible()
	top    int  // the first list line drawn
	note   string

	width, height int
	t             *huh.Styles

	answer  MarketPickerAnswer
	aborted bool // Ctrl+C
	done    bool
}

// checkMsg is one read of run's check channel: a result, or ok false once it is closed.
type checkMsg struct {
	run  int
	c    engine.Check
	ok   bool
	from <-chan engine.Check
}

// readCheck reads the next result of run's checks from ch.
func readCheck(run int, ch <-chan engine.Check) tea.Cmd {
	return func() tea.Msg {
		c, ok := <-ch
		return checkMsg{run: run, c: c, ok: ok, from: ch}
	}
}

// newMarketPicker is the picker over st's rows in theme t, on a terminal width columns wide and
// height rows high (0 when unknown).
func newMarketPicker(st MarketPickerStep, t *huh.Styles, width, height int) marketPicker {
	rows := slices.Concat(st.Own, st.Known)
	m := marketPicker{rows: rows, own: len(st.Own), reason: make([]string, len(rows)), ticked: make([]bool, len(rows)),
		check: st.Check, width: width, height: height, t: t}
	for i, r := range rows {
		m.rows[i].Status = ""
		m.ticked[i] = i < m.own && slices.Contains(st.PreselectOwn, r.ID)
	}
	m.follow()
	return m
}

func (m marketPicker) Init() tea.Cmd { return nil }

func (m marketPicker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case stdinEnded:
		return m.leave(PickerCancel, nil)
	case checkMsg:
		return m.result(msg)
	case tea.KeyPressMsg:
		m.note = ""
		var cmd tea.Cmd
		if msg.String() == "ctrl+c" {
			m.aborted = true
			return m.leave(PickerCancel, nil)
		}
		switch {
		case m.typing:
			m.typeFilter(msg)
		case m.offer != nil:
			m, cmd = m.answerOffer(msg)
		default:
			m, cmd = m.key(msg)
		}
		if m.done {
			return m, cmd
		}
		m.follow()
		return m, cmd
	}
	m.follow()
	return m, nil
}

// key applies a key pressed while no filter is being typed.
func (m marketPicker) key(msg tea.KeyPressMsg) (marketPicker, tea.Cmd) {
	vis := m.visible()
	row := vis[m.cursor] // len(m.rows) on Add a source…
	switch msg.String() {
	case "up", "k", "ctrl+p":
		m.cursor = max(m.cursor-1, 0)
	case "down", "j", "ctrl+n":
		m.cursor = min(m.cursor+1, len(vis)-1)
	case "home", "g":
		m.cursor = 0
	case "end", "G":
		m.cursor = len(vis) - 1
	case "x", "space":
		if row < len(m.rows) {
			m.ticked[row] = !m.ticked[row]
		}
	case "ctrl+a":
		all := true
		for _, i := range vis[:len(vis)-1] {
			all = all && m.ticked[i]
		}
		for _, i := range vis[:len(vis)-1] {
			m.ticked[i] = !all
		}
	case "/":
		m.typing = true
	case "a":
		return m.leave(PickerAdd, nil)
	case "u":
		if rows := m.chosen(row); len(rows) > 0 {
			return m.checkUpdates(rows)
		}
		m.note = "tick a marketplace, or move to one, to update it"
	case "U":
		all := make([]int, len(m.rows))
		for i := range all {
			all[i] = i
		}
		return m.checkUpdates(all)
	case "enter":
		if row == len(m.rows) {
			return m.leave(PickerAdd, nil)
		}
		return m.leave(PickerEnter, m.chosen(row))
	case "esc":
		if m.filter != "" {
			m.filter, m.cursor = "", 0
			return m, nil
		}
		return m.leave(PickerCancel, nil)
	case "q":
		return m.leave(PickerCancel, nil)
	}
	return m, nil
}

// answerOffer applies a key pressed while the footer offers an update: Enter or y takes it, Esc or n
// declines it, u or U declines it and checks again as with no offer, q cancels the picker; any other
// key does nothing.
func (m marketPicker) answerOffer(msg tea.KeyPressMsg) (marketPicker, tea.Cmd) {
	switch msg.String() {
	case "enter", "y":
		return m.leave(PickerUpdate, m.offer)
	case "u", "U":
		m.offer = nil
		return m.key(msg)
	case "esc", "n":
		m.offer = nil
	case "q":
		return m.leave(PickerCancel, nil)
	}
	return m, nil
}

// typeFilter applies a key pressed while the filter is typed: text extends it, Backspace takes its
// last character back, Enter keeps it and Esc drops it; the arrows still move.
func (m *marketPicker) typeFilter(msg tea.KeyPressMsg) {
	switch msg.String() {
	case "enter":
		m.typing = false
	case "esc":
		m.typing, m.filter = false, ""
	case "backspace":
		if r := []rune(m.filter); len(r) > 0 {
			m.filter = string(r[:len(r)-1])
		}
	case "up", "down":
		next, _ := m.key(msg)
		*m = next
		return
	default:
		if msg.Text != "" && msg.Mod&^tea.ModShift == 0 && !strings.ContainsFunc(msg.Text, func(r rune) bool { return !unicode.IsPrint(r) }) {
			m.filter += msg.Text
		}
	}
	m.cursor = 0
}

// chosen is the indexes of the ticked rows, in row order, or else row, the cursor's, unless that is
// Add a source….
func (m marketPicker) chosen(row int) []int {
	var rows []int
	for i, t := range m.ticked {
		if t {
			rows = append(rows, i)
		}
	}
	if len(rows) == 0 && row < len(m.rows) {
		rows = []int{row}
	}
	return rows
}

// answerFor is the answer action on rows, ascending indexes, their IDs split into the groups.
func (m marketPicker) answerFor(action MarketPickerAction, rows []int) MarketPickerAnswer {
	a := MarketPickerAnswer{Action: action}
	for _, i := range rows {
		if i < m.own {
			a.Own = append(a.Own, m.rows[i].ID)
		} else {
			a.Known = append(a.Known, m.rows[i].ID)
		}
	}
	return a
}

// leave ends the picker with action on rows, cancelling a check that still runs.
func (m marketPicker) leave(action MarketPickerAction, rows []int) (marketPicker, tea.Cmd) {
	m.stopChecks()
	m.answer, m.done = m.answerFor(action, rows), true
	return m, tea.Quit
}

// stopChecks cancels the current check run, if one was started.
func (m marketPicker) stopChecks() {
	if m.stop != nil {
		m.stop()
	}
}

// checkUpdates starts a check run of rows, cancelling the one before: every other row's status is
// cleared and each of rows that Checkable reads checking…. With none to check the run ends at once.
func (m marketPicker) checkUpdates(rows []int) (marketPicker, tea.Cmd) {
	m.stopChecks()
	m.run, m.stop, m.targets, m.checked, m.results, m.ended, m.banner = m.run+1, nil, rows, nil, nil, false, false
	for i := range m.rows {
		m.rows[i].Status, m.reason[i] = "", ""
	}
	var given []engine.PickerRow
	for _, i := range rows {
		if m.rows[i].Checkable() {
			m.rows[i].Status = engine.StatusChecking
			m.checked, given = append(m.checked, i), append(given, m.rows[i])
		}
	}
	if len(given) == 0 {
		return m.endRun(), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.stop = cancel
	return m, readCheck(m.run, m.check(ctx, given))
}

// endRun ends the run, every result in: it classifies them and, unless the banner shows, offers to
// update the run's rows of yours that need it, or notes why none does.
func (m marketPicker) endRun() marketPicker {
	m.ended, m.banner = true, engine.Classify(m.results)
	if m.banner {
		m.note = "nothing to update until the checks get through; u checks again"
		return m
	}
	unreachable, behind := 0, false
	for _, i := range m.targets {
		s := m.rows[i].Status
		late := s == engine.StatusUpdate || s == engine.StatusMoved
		switch {
		case s == engine.StatusUnreachable:
			unreachable++
		case i >= m.own:
			behind = behind || late
		case late || !m.rows[i].Checkable():
			m.offer = append(m.offer, i)
		}
	}
	switch {
	case m.offer != nil:
	case unreachable > 0:
		m.note = fmt.Sprintf("nothing to update; %d unreachable", unreachable)
	case behind:
		m.note = "nothing to update; ⏎ enables a known marketplace"
	case m.checked == nil:
		m.note = "nothing to update"
	default:
		m.note = "everything checked is up to date"
	}
	return m
}

// result applies one read of a check run: a result fills in its row and the next is read; the
// channel closed ends the run (endRun). Another run's reads are dropped, and so are reads after the
// picker is left.
func (m marketPicker) result(msg checkMsg) (tea.Model, tea.Cmd) {
	if msg.run != m.run || m.done {
		return m, nil
	}
	if !msg.ok {
		m = m.endRun()
		m.follow()
		return m, nil
	}
	if k := msg.c.Index; k >= 0 && k < len(m.checked) {
		i := m.checked[k]
		m.rows[i].Status, m.reason[i] = msg.c.Status, msg.c.Reason
		m.results = append(m.results, msg.c)
	}
	m.follow()
	return m, readCheck(m.run, msg.from)
}

// visible is the indexes of the rows the filter shows, own then known, and len(rows) last for Add
// a source…, which every filter shows.
func (m marketPicker) visible() []int {
	var vis []int
	f := strings.ToLower(m.filter)
	for i, r := range m.rows {
		if f == "" || slices.ContainsFunc([]string{r.ID, r.Source, r.Reason, r.Description}, func(s string) bool {
			return strings.Contains(strings.ToLower(s), f)
		}) {
			vis = append(vis, i)
		}
	}
	return append(vis, len(m.rows))
}

// oneLine is untrusted text made safe to print on one line.
func oneLine(s string) string {
	return untrusted.Text(strings.ReplaceAll(s, "\n", " "), untrusted.MaxName)
}

// head is a row's first line after its checkbox: its id, then its ref or pin, its entries, known
// and ahead of pin, and, with status, its check's status (not checked under the banner). The
// spaces inside each of them are nbsp, so wrapping never splits one; drawn, they are spaces.
func (m marketPicker) head(i int, status bool) string {
	r := m.rows[i]
	facts := []string{oneLine(r.ID)}
	ref, label := r.Ref, ""
	if i >= m.own {
		ref, label = r.Pin, "pin "
	}
	if ref != "" {
		if commit, ok := source.CommitRef(ref); ok {
			ref = commit[:7]
		}
		facts = append(facts, label+oneLine(ref))
	}
	switch {
	case r.FetchedAt == "":
	case r.Entries == 1:
		facts = append(facts, "1 entry")
	default:
		facts = append(facts, fmt.Sprintf("%d entries", r.Entries))
	}
	if r.Known {
		facts = append(facts, "known")
	}
	if r.AheadOfPin {
		facts = append(facts, "ahead of pin")
	}
	if s := r.Status; status && s != "" {
		if m.banner && s == engine.StatusUnreachable {
			s = engine.StatusNotChecked
		}
		facts = append(facts, s)
	}
	for n, f := range facts {
		facts[n] = strings.ReplaceAll(f, " ", nbsp)
	}
	return facts[0] + "  " + strings.Join(facts[1:], " · ")
}

// detail is a row's second line, its source and a known row's reason, then, once its run has ended
// with this row unreachable and no banner, the reason and what to do: for one of yours u retries
// and --remove forgets it, for a known one you have not enabled u checks again; with the only
// check failed, the connection is named first.
func (m marketPicker) detail(i int) []string {
	r := m.rows[i]
	lines := []string{oneLine(r.Source)}
	if r.Reason != "" {
		lines[0] += " — " + oneLine(r.Reason)
	}
	if !m.ended || m.banner || r.Status != engine.StatusUnreachable {
		return lines
	}
	if m.reason[i] != "" {
		lines = append(lines, oneLine(m.reason[i]))
	}
	hint := "→ u checks again"
	if i < m.own {
		hint = "→ u retries · lola market " + oneLine(r.ID) + " --remove forgets it"
	}
	if len(m.results) == 1 {
		hint = "→ check your network connection; " + strings.TrimPrefix(hint, "→ ")
	}
	return append(lines, hint)
}

// line is one line of the list and the position in visible() of the item it draws, -1 for a
// group's title.
type line struct {
	text string
	item int
}

// list is the list's lines: your marketplaces, then the known ones, each group under its title
// when the filter shows any of it, then Add a source…; each row's head and detail wrapped to the
// width, under its name.
func (m marketPicker) list() []line {
	f := &m.t.Focused
	var lines []line
	vis := m.visible()
	group := ""
	for p, i := range vis {
		cursor := "  "
		if p == m.cursor {
			cursor = f.MultiSelectSelector.String()
		}
		if i == len(m.rows) {
			lines = append(lines, line{m.cut(cursor + f.Option.Render(addSourceRow)), p})
			break
		}
		g := "Known marketplaces"
		if i < m.own {
			g = "Your marketplaces"
		}
		if g != group {
			group = g
			lines = append(lines, line{m.cut(f.Title.Render(g)), -1})
		}
		box, text := f.UnselectedPrefix.String(), f.UnselectedOption
		if m.ticked[i] {
			box, text = f.SelectedPrefix.String(), f.SelectedOption
		}
		for n, h := range strings.Split(strings.ReplaceAll(m.wrap(m.head(i, true), len(pickerIndent)), nbsp, " "), "\n") {
			lead := cursor + box
			if n > 0 {
				lead = pickerIndent
			}
			lines = append(lines, line{lead + text.Render(h), p})
		}
		for _, d := range m.detail(i) {
			for w := range strings.SplitSeq(m.wrap(d, len(pickerIndent)), "\n") {
				lines = append(lines, line{pickerIndent + f.Description.Render(w), p})
			}
		}
	}
	return lines
}

// cut fits s to the width, ending a cut line with "…".
func (m marketPicker) cut(s string) string {
	if m.width <= 0 {
		return s
	}
	return ansi.Truncate(s, m.width, "…")
}

// wrap wraps s to the width less indent columns, at least one.
func (m marketPicker) wrap(s string, indent int) string {
	if m.width <= 0 {
		return s
	}
	return ansi.Wrap(s, max(m.width-indent, 1), "")
}

// header is the lines above the list: the title, the banner, the filter.
func (m marketPicker) header() []string {
	f := &m.t.Focused
	lines := []string{m.cut(f.Title.Render("Marketplaces"))}
	if m.banner {
		lines = append(lines, m.cut(f.ErrorMessage.UnsetString().Render("✗ "+networkBanner)))
	}
	if m.typing || m.filter != "" {
		cur := ""
		if m.typing {
			cur = "█"
		}
		lines = append(lines, m.cut(f.TextInput.Prompt.UnsetString().Render("/")+f.TextInput.Text.Render(oneLine(m.filter))+cur))
	}
	return lines
}

// footer is the lines under the list: the offer to update, unless a filter is being typed, on one
// line, its rows named when they fit and else counted, its keys never cut; else the note, then the
// keys, u first so it always shows, in at most two rows (ctrl+a is left out to keep them short).
func (m marketPicker) footer() []string {
	if m.offer != nil && !m.typing {
		names := make([]string, len(m.offer))
		for k, i := range m.offer {
			names[k] = oneLine(m.rows[i].ID)
		}
		const keys = "? ⏎/y yes · esc/n no"
		question := "update " + strings.Join(names, ", ")
		if m.width > 0 && ansi.StringWidth(question+keys) > m.width {
			question = fmt.Sprintf("update %d marketplaces", len(names))
			if len(names) == 1 {
				question = "update 1 marketplace"
			}
			question = ansi.Truncate(question, max(m.width-ansi.StringWidth(keys), 0), "…")
		}
		return []string{m.cut(m.t.Focused.Description.Render(question + keys))}
	}
	var lines []string
	if m.note != "" {
		lines = append(lines, m.cut(m.t.Focused.Description.Render(m.note)))
	}
	keys := "u check+update · U all · x tick · / filter · a add · ⏎ act · esc back · q quit"
	if m.typing {
		keys = "⏎ keep filter · esc clear · ↑↓ move"
	}
	for _, k := range wrapKeys(keys, m.width, 2) {
		lines = append(lines, m.t.Help.ShortDesc.Render(k))
	}
	return lines
}

// room is how many list lines fit under the header and over the footer, a line kept free so the
// inline frame never fills the terminal; 0 when the height is unknown, which fits all.
func (m marketPicker) room() int {
	if m.height <= 0 {
		return 0
	}
	return max(m.height-len(m.header())-len(m.footer())-1, 1)
}

// follow keeps the cursor in range and its item's lines in view: top moves only as far as it must.
func (m *marketPicker) follow() {
	m.cursor = min(max(m.cursor, 0), len(m.visible())-1)
	lines, room := m.list(), m.room()
	if room == 0 {
		m.top = 0
		return
	}
	first, last := -1, -1
	for n, l := range lines {
		if l.item == m.cursor {
			if first < 0 {
				first = n
			}
			last = n
		}
	}
	if first > 0 && lines[first-1].item == -1 {
		first-- // the group's title with its first row
	}
	m.top = min(m.top, first)
	m.top = max(m.top, last-room+1)
	m.top = min(max(m.top, 0), max(len(lines)-room, 0))
}

func (m marketPicker) View() tea.View {
	if m.done {
		return tea.NewView("")
	}
	lines := m.header()
	list := m.list()
	if room := m.room(); room > 0 {
		list = list[min(m.top, len(list)):min(m.top+room, len(list))]
	}
	for _, l := range list {
		lines = append(lines, l.text)
	}
	return tea.NewView(strings.Join(append(lines, m.footer()...), "\n") + "\n")
}

// marketPickerLines is the picker in accessible mode: the rows numbered, each with its detail and
// no status, Add a source… last, then pickerPrompt under the accessible break, one line of h.in at a time. Numbers answer
// PickerEnter with their rows, Add a source…'s alone PickerAdd, an empty line PickerCancel; any
// other line prints what it takes and asks again. End of input, and Ctrl+C, are inputEnd's.
func (h *huhForms) marketPickerLines(st MarketPickerStep) (MarketPickerAnswer, error) {
	m := newMarketPicker(st, plainTheme(), 0, 0)
	add := len(m.rows) + 1
	fmt.Fprintln(h.out, "Marketplaces")
	for i := range m.rows {
		fmt.Fprintf(h.out, "%2d. %s\n", i+1, strings.ReplaceAll(m.head(i, false), nbsp, " "))
		for _, d := range m.detail(i) {
			fmt.Fprintf(h.out, "    %s\n", d)
		}
	}
	fmt.Fprintf(h.out, "%2d. %s\n", add, addSourceRow)
	fmt.Fprintln(h.out, "Enter alone cancels.")
	promptBreak(h.out, style{color: h.color, bold: h.bold}, h.width, true)
	var answer MarketPickerAnswer
	err := h.in.catch(func() error {
		for {
			fmt.Fprint(h.out, pickerPrompt)
			text, err := readLine(h.in)
			if err != nil {
				return err
			}
			picked, problem := pickedNumbers(text, add)
			switch {
			case problem != "":
				fmt.Fprintln(h.out, problem)
				continue
			case len(picked) == 0:
				answer = MarketPickerAnswer{Action: PickerCancel}
			case slices.Contains(picked, add) && len(picked) > 1:
				fmt.Fprintln(h.out, addSourceRow+" goes alone: answer its number on its own")
				continue
			case picked[0] == add:
				answer = MarketPickerAnswer{Action: PickerAdd}
			default:
				rows := make([]int, len(picked))
				for k, n := range picked {
					rows[k] = n - 1
				}
				answer = m.answerFor(PickerEnter, rows)
			}
			return nil
		}
	})
	switch {
	case err == nil:
		return answer, nil
	case h.in.eof || errors.Is(err, io.EOF):
		fmt.Fprintln(h.out) // the prompt has no newline of its own; the error prints under it
		return MarketPickerAnswer{}, h.inputEnd()
	}
	return MarketPickerAnswer{}, err
}

// pickedNumbers is the numbers of text, comma separated, each from 1 to n, sorted and without
// repeats; or, when text holds anything else, what it should hold.
func pickedNumbers(text string, n int) ([]int, string) {
	var picked []int
	for f := range strings.SplitSeq(text, ",") {
		if f = strings.TrimSpace(f); f == "" {
			continue
		}
		k, err := strconv.Atoi(f)
		if err != nil || k < 1 || k > n {
			return nil, fmt.Sprintf("answer numbers from 1 to %d, comma separated", n)
		}
		picked = append(picked, k)
	}
	slices.Sort(picked)
	return slices.Compact(picked), ""
}
