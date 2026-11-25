package cli

import (
	"cmp"
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
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// MarketPickerAction is how the marketplace picker was left (MarketPickerAnswer.Action).
type MarketPickerAction int

const (
	PickerEnter     MarketPickerAction = iota // Enter: act on Own and Known
	PickerAdd                                 // a, or Add a source…: add a marketplace
	PickerUpdate                              // u's offer taken: update Own (Known is always empty)
	PickerCancel                              // esc or q: do nothing
	PickerInstall                             // i on an entry's page: install the entry (Market, Entry)
	PickerUninstall                           // d on an entry's page: uninstall it (Market, Entry)
)

// MarketPickerStep offers the marketplace picker's rows: Own, your marketplaces, then Known, the
// known ones you have not enabled (engine.PickerRows). Check starts a check of rows, as
// engine.CheckMarkets does, each Check's Index one into rows, until ctx is cancelled; the picker calls
// it only when the user presses u, on the rows u acts on, never on open. PreselectOwn is the IDs of
// the rows of yours ticked on open (the marketplace just added); a known one you have not enabled
// can share an ID, and is not ticked. Entries is each of your marketplaces' entries by ID, for the
// pages Enter opens (absent: nothing was fetched); Skipped is each one's skipped catalog entries, with
// why, for the page s opens on its entries page (absent: none); Context is the scope's context line, drawn under
// every page's title, and ScopeKind that scope's kind, which the keys that install and uninstall
// name; Resume reopens the picker on that entry's detail page, or its marketplace's entries page,
// the cursor on it, when OnEntries, and when it names a marketplace or entry no longer there, on the
// list.
type MarketPickerStep struct {
	Own, Known   []engine.PickerRow
	Check        func(ctx context.Context, rows []engine.PickerRow) <-chan engine.Check
	PreselectOwn []string
	Entries      map[string][]engine.PickerEntry
	Skipped      map[string][]engine.MarketSkip
	Context      string
	ScopeKind    string // "user" or "project": the scope i and d act on, named on their keys
	Resume       PickerResume
	// OpenURL opens an entry's web address in a browser; the picker passes it only an address
	// checkWebURL accepts, on the list, the entries and the detail pages.
	OpenURL func(address string) error
	// Preview fetches a known marketplace you have not enabled, by its ID, at its pin, for the
	// entries page Enter or Right opens on its row. It adds nothing, and is required. The picker
	// runs it in a command, never in Update, and cancels ctx when the user presses Esc or q or
	// opens another page.
	Preview func(ctx context.Context, id string) (engine.KnownPreview, error)
}

// PickerResume names the entry, by name, of one of your marketplaces, by ID, whose detail page
// the picker opens on, or whose entries page with the cursor on the entry when OnEntries; the zero
// value opens the list.
type PickerResume struct {
	Market, Entry string
	OnEntries     bool
}

// MarketPickerAnswer is the picker's answer: the action and the IDs of the rows it applies to, Own
// those of your marketplaces and Known those of the known ones you have not enabled, each in row
// order (none for PickerAdd, PickerCancel, PickerInstall and PickerUninstall). Market and Entries
// are the marketplace's ID and the names of the entries, in page order, that PickerInstall and
// PickerUninstall name, Entry the first of them; FromEntries is set when they were pressed on the
// entries page, not an entry's detail page.
type MarketPickerAnswer struct {
	Action        MarketPickerAction
	Own, Known    []string
	Market, Entry string
	Entries       []string
	FromEntries   bool
}

// The picker's network banner, the line a row's failed check prints its hints on, and the
// accessible line prompt.
const (
	networkBanner = "Several sources unreachable; check your network connection"
	pickerPrompt  = "Choose marketplaces (numbers, comma separated; m1 for the menu; o1 for the web page): "
	addSourceRow  = "Add a source…"
	pickerIndent  = "      " // a row's detail lines: under its name, past the cursor and checkbox
	nbsp          = "\u00a0" // a space ansi.Wrap never breaks at
)

// marketPicker is the marketplace picker: a filterable multi-select over two-line rows, own then
// known, ended by Add a source…, with two pages under a row of yours: Enter on it, when nothing is
// ticked and its catalog is fetched, opens its entries (entriesLevel, market_picker_entries.go),
// and Enter on an entry opens its detail (detailLevel), s the skipped entries with why (skippedLevel); Esc goes back a page. Enter on a known
// row you have not enabled likewise fetches its catalog at its pin, in a command (Esc cancels), and
// opens the entries as a preview (market_picker_preview.go): the same pages, where m enables it and
// i and d say it is not added. Keys: x or space
// ticks, ctrl+a ticks all or none of the rows shown, / types a filter (Enter keeps it, Esc drops
// it), a adds, u checks then offers to update the ticked rows or else the cursor's, U does so for
// every row (whatever the filter shows), Enter acts on the ticked rows or else the cursor's (adds
// on Add a source…; m does so on a row of yours too, where Enter opens its entries), Esc clears a
// filter and otherwise cancels, as q does; Ctrl+C aborts.
//
// u (or U) starts a check run of its rows that Checkable; their statuses fill in live. Once every
// result is in, unless the network banner shows, the footer offers to update those of yours that need it:
// a check found them behind, or they have no check (archive, folder and local sources). A known
// one you have not enabled is checked, never offered: Enter enables it. Enter or y takes the offer
// (PickerUpdate), Esc or n declines it and the picker stays open. A check run's results are read
// one at a time (readCheck) and tagged with the run; u again, or leaving, cancels the run, and the
// results of a run that is not the current one are dropped, so a check that ended because it was
// cancelled never reads as unreachable or counts in Classify.
type marketPicker struct {
	rows    []engine.PickerRow // Own then Known; each Status is the current run's
	own     int                // how many of rows are Own
	reason  []string           // per row, its failed check's reason
	ticked  []bool
	check   func(context.Context, []engine.PickerRow) <-chan engine.Check
	openURL func(string) error // opens an address in a browser; see MarketPickerStep.OpenURL

	run     int                // the check run whose results count; 0 before u
	stop    context.CancelFunc // cancels run's checks; nil when none was started
	targets []int              // the rows u acts on in run
	checked []int              // the rows run checks, a Check's Index one into it
	results []engine.Check     // run's results so far
	ended   bool               // every result of run is in
	banner  bool
	offer   []int // the rows the footer offers to update; nil when none is offered

	level  pickerLevel
	market int // the row whose entries are open (index into rows)
	entry  int // the entry open on the detail page (index into the market's entries)
	// entriesTop is the entries page's scroll, restored by Esc from the skipped page, which scrolls
	// from its own top; the cursor stays as it was.
	entriesTop int
	saved      struct {
		cursor, top int
		filter      string
	} // the list's position, restored by Esc from the entries
	entries map[string][]engine.PickerEntry
	// ticks are the names of the open marketplace's entries ticked on its entries page, reset when
	// another marketplace opens; a preview has none.
	ticks   map[string]bool
	skipped map[string][]engine.MarketSkip // each of your marketplaces' skipped entries, by ID
	context string
	scope   string // "user" or "project", named on the keys that install and uninstall

	// A known marketplace's preview: its entries page opens from the known row market, fetched by
	// previewFn in a command. previews are the fetches that succeeded, kept for the session and
	// keyed by ID apart from entries, since one of yours can share the ID; fetchID is the ID being
	// fetched ("" when none), fetchRun the fetch whose result counts and fetchStop cancels it.
	previewFn func(context.Context, string) (engine.KnownPreview, error)
	previews  map[string]engine.KnownPreview
	preview   bool // the entries and detail pages show previews[rows[market].ID]
	fetchID   string
	fetchRow  int // the row fetchID is
	fetchRun  int
	fetchStop context.CancelFunc

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
		check: st.Check, openURL: st.OpenURL, entries: st.Entries, skipped: st.Skipped, context: st.Context, scope: st.ScopeKind, previewFn: st.Preview,
		previews: map[string]engine.KnownPreview{}, ticks: map[string]bool{}, width: width, height: height, t: t}
	for i, r := range rows {
		m.rows[i].Status = ""
		m.ticked[i] = i < m.own && slices.Contains(st.PreselectOwn, r.ID)
	}
	m.resume(st.Resume)
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
	case openedMsg:
		m.note = msg.note(m.width)
	case previewMsg:
		m = m.previewed(msg)
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
		case m.level != listLevel:
			m, cmd = m.entriesKey(msg)
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
	case "o":
		return m.openRow(row)
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
	case "right":
		if !slices.Contains(m.ticked, true) {
			if next, cmd, ok := m.drillRow(row); ok {
				return next, cmd
			}
		}
	case "enter", "m":
		if row == len(m.rows) {
			return m.leave(PickerAdd, nil)
		}
		if msg.String() == "enter" && !slices.Contains(m.ticked, true) {
			if next, cmd, ok := m.drillRow(row); ok {
				return next, cmd
			}
		}
		return m.leave(PickerEnter, m.chosen(row))
	case "esc":
		if m.fetchID != "" {
			m.note = "cancelled fetching " + oneLine(m.fetchID)
			m.stopFetch()
			return m, nil
		}
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
// last character back, Enter keeps it and Esc drops it; the arrows still move, on a page as on the list.
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
		*m, _ = m.arrow(msg)
		return
	case "left", "right":
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

// leave ends the picker with action on rows, cancelling a check or a fetch that still runs.
func (m marketPicker) leave(action MarketPickerAction, rows []int) (marketPicker, tea.Cmd) {
	m.stopChecks()
	m.stopFetch()
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
	if m.level != listLevel {
		return m.entriesVisible()
	}
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

// detail is a row's lines under its first: its description (a known row's reason when its catalog
// has none), if any, then its source, then, once its run has ended
// with this row unreachable and no banner, the reason and what to do: for one of yours u retries
// and --remove forgets it, for a known one you have not enabled u checks again; with the only
// check failed, the connection is named first.
func (m marketPicker) detail(i int) []string {
	r := m.rows[i]
	var lines []string
	if d := cmp.Or(r.Description, r.Reason); d != "" {
		lines = append(lines, oneLine(d))
	}
	lines = append(lines, oneLine(r.Source))
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
	if m.level != listLevel {
		return m.entriesList()
	}
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
	if m.level != listLevel {
		return m.entriesHeader()
	}
	f := &m.t.Focused
	lines := []string{m.cut(f.Title.Render("Marketplaces"))}
	if m.banner {
		lines = append(lines, m.cut(f.ErrorMessage.UnsetString().Render("✗ "+networkBanner)))
	}
	lines = append(lines, m.contextLines()...)
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
	if m.level != listLevel {
		return m.entriesFooter()
	}
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
	if m.fetchID != "" {
		lines = append(lines, m.noteLines(m.fetching())...)
	}
	if m.note != "" {
		lines = append(lines, m.noteLines(m.note)...)
	}
	keys := "u check+update · U all · x tick · / filter · a add · ⏎ open · o web · m menu · q quit"
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
	if m.level == skippedLevel {
		if room := m.room(); room > 0 {
			m.top = max(min(m.top, len(m.list())-room), 0)
		} else {
			m.top = 0
		}
		return
	}
	m.cursor = max(min(m.cursor, len(m.visible())-1), 0)
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

// marketPickerLines is the picker in accessible mode: a page at a time under its title and the
// scope's context line, each its items numbered, then its prompt under the accessible break, one
// line of h.in at a time; an empty line goes back a page, and from the list cancels. The list's
// rows come with their detail and no status, Add a source… last: several numbers, or m before them,
// answer PickerEnter with their rows, Add a source…'s alone PickerAdd, and one number alone opens
// that marketplace as the TUI's Enter does: one of yours its entries, a known one's its preview,
// fetched first (previewPage). On an entries page a number opens the entry's detail, and i<n>, d<n>
// and o<n> install, uninstall or open entry n, answering PickerInstall and PickerUninstall; on a
// detail page i, d and o do so for it; in a preview m, on either page, answers PickerEnter for the
// known row. Any other line prints what it takes and asks again. End of input, and Ctrl+C, are
// inputEnd's.
func (h *huhForms) marketPickerLines(st MarketPickerStep) (MarketPickerAnswer, error) {
	m := newMarketPicker(st, plainTheme(), 0, 0)
	err := h.in.catch(func() error {
		for !m.done {
			var err error
			switch m.level {
			case listLevel:
				err = h.listPage(&m)
			case entriesLevel:
				err = h.entriesPage(&m)
			default:
				err = h.detailPage(&m)
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
	switch {
	case err == nil:
		return m.answer, nil
	case errors.Is(err, errFetchInterrupted):
		return MarketPickerAnswer{}, h.inputEnd()
	case h.in.eof || errors.Is(err, io.EOF):
		fmt.Fprintln(h.out) // the prompt has no newline of its own; the error prints under it
		return MarketPickerAnswer{}, h.inputEnd()
	}
	return MarketPickerAnswer{}, err
}

// listPage prints the marketplaces and reads lines until one answers the picker (m.done) or opens
// one's entries (m.level).
func (h *huhForms) listPage(m *marketPicker) error {
	add := len(m.rows) + 1
	h.printHeader(m)
	for i := range m.rows {
		h.sayLead(fmt.Sprintf("%2d. ", i+1), m.head(i, false))
		for _, d := range m.detail(i) {
			h.sayLead("    ", d)
		}
	}
	h.sayLead(fmt.Sprintf("%2d. ", add), addSourceRow)
	fmt.Fprintln(h.out, "Enter alone cancels.")
	promptBreak(h.out, style{color: h.color, bold: h.bold}, h.width, true)
	for {
		fmt.Fprint(h.out, h.fitPrompt(pickerPrompt, "numbers, comma separated · m1 for the menu · o1 web", "Choose marketplaces: "))
		text, err := readLine(h.in)
		if err != nil {
			return err
		}
		if t := strings.TrimSpace(text); t != "" && (t[0] == 'o' || t[0] == 'O') {
			h.openRow(m, t[1:], add)
			continue
		}
		menu := false
		if t := strings.TrimSpace(text); t != "" && (t[0] == 'm' || t[0] == 'M') {
			menu, text = true, t[1:]
		}
		picked, problem := pickedNumbers(text, add)
		switch {
		case problem != "":
			fmt.Fprintln(h.out, problem)
		case menu && len(picked) == 0:
			fmt.Fprintln(h.out, "m takes numbers: m1, or m1,2")
		case len(picked) == 0:
			m.answer, m.done = MarketPickerAnswer{Action: PickerCancel}, true
		case slices.Contains(picked, add) && len(picked) > 1:
			fmt.Fprintln(h.out, addSourceRow+" goes alone: answer its number on its own")
		case picked[0] == add:
			m.answer, m.done = MarketPickerAnswer{Action: PickerAdd}, true
		case !menu && len(picked) == 1 && picked[0] <= m.own:
			*m, _ = m.drill(picked[0] - 1)
			if m.level == listLevel {
				// u is the TUI's key; here the catalog is fetched from the menu (m<n>) or the command.
				id := oneLine(m.rows[picked[0]-1].ID)
				fmt.Fprintf(h.out, "%s has no entries; fetch its catalog with m%d (Update), or %s\n", id, picked[0],
					errs.ShellJoin([]string{"lola", "market", id, "--update"}))
				m.note = ""
				continue
			}
		case !menu && len(picked) == 1:
			if err := h.previewPage(m, picked[0]-1); err != nil {
				return err
			}
		default:
			rows := make([]int, len(picked))
			for k, n := range picked {
				rows[k] = n - 1
			}
			m.answer, m.done = m.answerFor(PickerEnter, rows), true
		}
		if m.done || m.level != listLevel {
			return nil
		}
	}
}

// openRow answers o<n> on the list's prompt, text being what follows the o: it opens row n's web
// address as the TUI's o does and prints the note it leaves, or says what o takes; add is the number
// of Add a source….
func (h *huhForms) openRow(m *marketPicker, text string, add int) {
	picked, problem := pickedNumbers(text, add)
	switch {
	case problem != "":
		fmt.Fprintln(h.out, problem)
	case len(picked) != 1:
		fmt.Fprintln(h.out, "o takes one number: o1")
	default:
		var open tea.Cmd
		if *m, open = m.openRow(picked[0] - 1); open != nil {
			m.note = open().(openedMsg).note(0)
		}
		h.printNote(m)
	}
}

// previewPage opens the preview of the known row, as the TUI's Enter does: a kept one at once, else
// after printing what is fetched and from where, then fetching it, which Ctrl+C cancels; a failure,
// or a catalog with no entry, prints its note and stays on the list.
func (h *huhForms) previewPage(m *marketPicker, row int) error {
	var cmd tea.Cmd
	*m, cmd = m.drillKnown(row)
	if cmd != nil {
		h.say(m.fetching())
		msg, err := h.fetched(m, cmd)
		if err != nil {
			return err
		}
		*m = m.previewed(msg.(previewMsg))
	}
	if m.level == listLevel {
		h.printNote(m)
	}
	return nil
}

// errFetchInterrupted is Ctrl+C while a preview was fetched: no prompt is waiting for its line to end.
var errFetchInterrupted = errors.New("interrupted while fetching")

// fetched runs cmd, a preview's fetch, and returns its message. Ctrl+C meanwhile cancels the fetch
// without waiting for it and ends the input as at a prompt, so inputEnd answers ErrAborted.
func (h *huhForms) fetched(m *marketPicker, cmd tea.Cmd) (tea.Msg, error) {
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		return msg, nil
	case <-h.in.sig:
		m.stopFetch()
		h.in.eof, h.in.interrupted = true, true
		return nil, errFetchInterrupted
	}
}

// entriesPage prints the open marketplace's entries and reads lines until one opens an entry's
// detail (m.level), goes back to the list, or answers the picker: a number opens that entry, and i,
// d and o with its number install, uninstall or open its page, as the TUI's keys do, saying why when
// one does not apply (i and d take several numbers, comma separated, as the TUI's take the ticked
// entries); s prints the entries its catalog skipped, with why, and asks again; in a
// preview m enables the marketplace.
func (h *huhForms) entriesPage(m *marketPicker) error {
	h.printHeader(m)
	entries := m.open()
	for i, e := range entries {
		h.sayLead(fmt.Sprintf("%2d. ", i+1), entryHead(e))
		for _, d := range entryDetail(e) {
			h.sayLead(entryBodyIndent+"  ", d) // two columns past the number line's name
		}
	}
	if skipped := m.skippedNote(); skipped != "" {
		h.say(skipped)
	}
	h.printNote(m)
	promptBreak(h.out, style{color: h.color, bold: h.bold}, h.width, true)
	h.printKeys(m.entriesKeys())
	for {
		fmt.Fprint(h.out, "Choose an entry (number or key above): ")
		text, err := readLine(h.in)
		if err != nil {
			return err
		}
		text = strings.ToLower(strings.TrimSpace(text))
		if text == "" {
			m.backToList()
			return nil
		}
		letter := ""
		if text == "s" && len(m.skips()) > 0 {
			h.skippedPage(m)
			continue
		}
		if text == "m" && m.preview {
			*m, _ = m.enablePreview()
			return nil
		}
		if strings.Contains("ido", text[:1]) {
			letter, text = text[:1], strings.TrimSpace(text[1:])
			if text == "" {
				fmt.Fprintf(h.out, "%s takes an entry's number: %s1\n", letter, letter)
				continue
			}
		}
		if letter == "i" || letter == "d" {
			h.entriesKeyMany(m, letter, text)
			if m.done {
				return nil
			}
			continue
		}
		n, err := strconv.Atoi(text)
		if err != nil || strings.Trim(text, "0123456789") != "" || n < 1 || n > len(entries) {
			fmt.Fprintf(h.out, "answer a number from 1 to %d\n", len(entries))
			continue
		}
		if letter == "" {
			m.level, m.entry = detailLevel, n-1
			return nil
		}
		h.entryKey(m, letter, entries[n-1])
		if m.done {
			return nil
		}
	}
}

// entriesKeyMany answers i or d on the entries page's prompt, text being the entry numbers after it,
// comma separated: one number is the key on that entry, as the TUI's is on the cursor's, several are
// it on all of them as it is on the ticked ones. It prints what it takes when text holds anything else,
// a number out of range, or a number twice.
func (h *huhForms) entriesKeyMany(m *marketPicker, key, text string) {
	entries := m.open()
	picked, problem := pickedNumbers(text, len(entries))
	listed := 0
	for f := range strings.SplitSeq(text, ",") {
		if f = strings.TrimSpace(f); f != "" {
			listed++
		}
		if strings.Trim(f, "0123456789") != "" { // Atoi takes a sign; an entry's number is digits only
			picked, problem = nil, fmt.Sprintf("answer numbers from 1 to %d, comma separated", len(entries))
		}
	}
	switch {
	case problem != "":
		fmt.Fprintln(h.out, problem)
	case len(picked) == 0:
		fmt.Fprintf(h.out, "%s takes entry numbers: %s1 or %s1,3\n", key, key, key)
	case len(picked) != listed:
		fmt.Fprintln(h.out, "name each number once")
	case len(picked) == 1:
		h.entryKey(m, key, entries[picked[0]-1])
	default:
		chosen := make([]engine.PickerEntry, len(picked))
		for k, n := range picked {
			chosen[k] = entries[n-1]
		}
		*m, _ = m.installMany(key, chosen, "those entries")
		h.printNote(m)
	}
}

// skippedPage prints the skipped page: its title, then each entry's name and, under it, its
// reason indented as the TUI draws it, wrapped at spaces.
func (h *huhForms) skippedPage(m *marketPicker) {
	h.say(m.skippedTitle())
	for _, s := range m.skips() {
		h.say(oneLine(s.Name))
		h.sayLead(entryIndent, oneLine(s.Reason))
	}
}

// detailPage prints the open entry's detail and reads lines until one answers the picker with i, d
// or m (m.done) or goes back to the entries; o opens the entry's page, and a letter that does not
// apply prints the TUI's note.
func (h *huhForms) detailPage(m *marketPicker) error {
	h.printHeader(m)
	for _, l := range m.detailLines() {
		text := ansi.Strip(l.text)
		h.sayLead(text[:len(text)-len(strings.TrimLeft(text, " "))], strings.TrimLeft(text, " "))
	}
	promptBreak(h.out, style{color: h.color, bold: h.bold}, h.width, true)
	e := m.open()[m.entry]
	var shown, answers []string
	for _, k := range m.entryKeys(e) {
		shown = append(shown, k.text())
		answers = append(answers, k.letter+" to "+k.verb)
	}
	shown = append(shown, "Enter alone goes back")
	reply := "answer Enter to go back"
	if len(answers) > 0 {
		reply = "answer " + strings.Join(answers, ", ") + ", or Enter to go back"
	}
	for {
		keys := strings.Join(shown, " · ")
		fmt.Fprint(h.out, h.fitPrompt(keys+": ", keys, "Choose a key above: "))
		text, err := readLine(h.in)
		if err != nil {
			return err
		}
		switch text = strings.ToLower(strings.TrimSpace(text)); {
		case text == "":
			*m, _ = m.detailKey("esc")
			return nil
		case text == "i" || text == "d" || text == "o" || (text == "m" && m.preview):
			h.entryKey(m, text, e)
			if m.done {
				return nil
			}
		default:
			fmt.Fprintln(h.out, reply)
		}
	}
}

// entryKey applies key (i, d, o, or m in a preview) to e as the TUI does, and prints the note it
// leaves: why a key does not apply, or what opening the page did, the address whole to be copied.
func (h *huhForms) entryKey(m *marketPicker, key string, e engine.PickerEntry) {
	switch key {
	case "o":
		var open tea.Cmd
		if *m, open = m.openEntry(e); open != nil {
			m.note = open().(openedMsg).note(0)
		}
	case "m":
		*m, _ = m.enablePreview()
	default:
		*m, _ = m.installKey(key, e)
	}
	h.printNote(m)
}

// printNote prints the picker's note, if it has one, and clears it.
func (h *huhForms) printNote(m *marketPicker) {
	if m.note != "" {
		h.say(m.note)
		m.note = ""
	}
}

// cols is how many columns accessible output may fill: one less than the terminal's width, so the
// cursor never sits at the wrap edge; 0 when the width is unknown, which wraps nothing.
func (h *huhForms) cols() int {
	if h.width <= 0 {
		return 0
	}
	return max(h.width-1, 1)
}

// say prints text as lines wrapped between words to cols.
func (h *huhForms) say(text string) { h.sayLead("", text) }

// sayLead prints text wrapped between words to cols under lead: the first line starts with it, the
// others with as many spaces. A word wider than a line stays whole, and nbsp print as spaces.
func (h *huhForms) sayLead(lead, text string) {
	w := h.cols()
	if w > 0 {
		w = max(w-ansi.StringWidth(lead), 1)
	}
	pad := strings.Repeat(" ", ansi.StringWidth(lead))
	for i, l := range strings.Split(wrapSpaces(text, w), "\n") {
		if i == 1 {
			lead = pad
		}
		fmt.Fprintln(h.out, strings.ReplaceAll(lead+l, nbsp, " "))
	}
}

// wrapSpaces wraps s to width columns at spaces only: a word is never split, not at a hyphen
// either, so an address or a flag stays whole even when wider than a line. A width of 0 or less
// wraps nothing. Spaces that are nbsp keep their words together.
func wrapSpaces(s string, width int) string {
	if width <= 0 {
		return s
	}
	var out []string
	for line := range strings.SplitSeq(s, "\n") {
		cur := ""
		for i, w := range strings.Split(line, " ") {
			switch {
			case i == 0:
				cur = w
			case ansi.StringWidth(cur)+1+ansi.StringWidth(w) <= width:
				cur += " " + w
			default:
				out = append(out, cur)
				cur = w
			}
		}
		out = append(out, cur)
	}
	return strings.Join(out, "\n")
}

// printKeys prints keys, a " · " separated list, as lines of at most cols, a key never split
// across two.
func (h *huhForms) printKeys(keys string) {
	for _, l := range wrapKeys(keys, h.cols(), len(keys)) {
		fmt.Fprintln(h.out, l)
	}
}

// fitPrompt is the prompt to ask: full when it fits the terminal's width, else short, with keys
// printed above it as printKeys does.
func (h *huhForms) fitPrompt(full, keys, short string) string {
	if h.width <= 0 || ansi.StringWidth(full) < h.width {
		return full
	}
	h.printKeys(keys)
	return short
}

// printHeader prints the lines the TUI draws above the open page: its title, then the scope's
// context line.
func (h *huhForms) printHeader(m *marketPicker) {
	for _, l := range m.header() {
		h.say(ansi.Strip(l))
	}
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
