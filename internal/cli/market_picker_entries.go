package cli

import (
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/source"
)

// pickerLevel is the page the marketplace picker shows: the list of marketplaces, one's entries,
// one entry's detail, or the entries its catalog skipped.
type pickerLevel int

const (
	listLevel pickerLevel = iota
	entriesLevel
	detailLevel
	skippedLevel
)

// entryIndent is the second lines' indent of a preview's entries, and of the detail and skipped
// pages, under the entry's name past the cursor; the entries with checkboxes use pickerIndent.
const entryIndent = "  "

// entryBodyIndent is an entry's description's indent in accessible mode, before the two columns
// that put it past its number's name.
const entryBodyIndent = entryIndent + "  "

// drill opens the entries of row, a row of yours, saving the list's cursor, scroll and filter for Esc
// to restore; with no entries fetched for it, it says so and stays on the list.
func (m marketPicker) drill(row int) (marketPicker, tea.Cmd) {
	cancelled := ""
	if m.fetchID != "" {
		cancelled = "cancelled fetching " + oneLine(m.fetchID) + "; "
	}
	m.stopFetch()
	id := m.rows[row].ID
	if len(m.entries[id]) == 0 {
		m.note = cancelled + oneLine(id) + " has no entries; u fetches its catalog"
		return m, nil
	}
	m.saved.cursor, m.saved.top, m.saved.filter = m.cursor, m.top, m.filter
	m.level, m.market, m.cursor, m.top, m.filter = entriesLevel, row, 0, 0, ""
	m.ticks = map[string]bool{}
	return m, nil
}

// resume opens the detail page of r's entry, or the entries page with the cursor on it when
// r.OnEntries; with no such marketplace of yours, or entry in it, the picker stays on the list.
func (m *marketPicker) resume(r PickerResume) {
	row := slices.IndexFunc(m.rows[:m.own], func(p engine.PickerRow) bool { return p.ID == r.Market })
	if r.Market == "" || row < 0 {
		return
	}
	entry := slices.IndexFunc(m.entries[r.Market], func(e engine.PickerEntry) bool { return e.Name == r.Entry })
	if entry < 0 {
		return
	}
	m.saved.cursor = row
	m.level, m.market, m.entry, m.cursor = detailLevel, row, entry, entry
	if r.OnEntries {
		m.level = entriesLevel
	}
}

// open is the entries of the marketplace whose entries are open: its preview's when it is a known
// one you have not enabled.
func (m marketPicker) open() []engine.PickerEntry {
	if m.preview {
		return m.previews[m.rows[m.market].ID].Entries
	}
	return m.entries[m.rows[m.market].ID]
}

// entriesVisible is the indexes into open() of the entries the filter shows: it matches their name,
// description and tags, ignoring case.
func (m marketPicker) entriesVisible() []int {
	var vis []int
	f := strings.ToLower(m.filter)
	for i, e := range m.open() {
		if f == "" || slices.ContainsFunc(append([]string{e.Name, e.Description}, e.Tags...), func(s string) bool {
			return strings.Contains(strings.ToLower(s), f)
		}) {
			vis = append(vis, i)
		}
	}
	return vis
}

// arrow applies the up or down key to whichever page is open.
func (m marketPicker) arrow(msg tea.KeyPressMsg) (marketPicker, tea.Cmd) {
	if m.level == listLevel {
		return m.key(msg)
	}
	return m.entriesKey(msg)
}

// entriesKey applies a key pressed on the entries or the detail page while no filter is being typed.
// Left goes back a level, whatever the filter, and Right goes in one, as Enter does.
func (m marketPicker) entriesKey(msg tea.KeyPressMsg) (marketPicker, tea.Cmd) {
	key := msg.String()
	if key == "q" {
		return m.leave(PickerCancel, nil)
	}
	switch m.level {
	case detailLevel:
		return m.detailKey(key)
	case skippedLevel:
		return m.skippedKey(key)
	}
	vis := m.entriesVisible()
	switch key {
	case "up", "k", "ctrl+p":
		m.cursor = max(m.cursor-1, 0)
	case "down", "j", "ctrl+n":
		m.cursor = max(min(m.cursor+1, len(vis)-1), 0)
	case "home", "g":
		m.cursor = 0
	case "end", "G":
		m.cursor = max(len(vis)-1, 0)
	case "/":
		m.typing = true
	case "enter", "right":
		if m.cursor < len(vis) {
			m.level, m.entry = detailLevel, vis[m.cursor]
		}
	case "x", "space":
		if m.cursor < len(vis) && !m.preview {
			name := m.open()[vis[m.cursor]].Name
			m.ticks[name] = !m.ticks[name]
		}
	case "ctrl+a":
		if !m.preview {
			m.tickAll(vis)
		}
	case "i", "d":
		shown, hidden := m.tickedEntries()
		if len(shown) > 0 {
			return m.installMany(key, shown, "the ticked entries")
		}
		if hidden > 0 {
			m.note = "ticked entries are hidden by the filter; esc clears it"
			return m, nil
		}
		if m.cursor < len(vis) {
			return m.installKey(key, m.open()[vis[m.cursor]])
		}
	case "o":
		if m.cursor < len(vis) {
			return m.openEntry(m.open()[vis[m.cursor]])
		}
	case "m":
		if m.preview {
			return m.enablePreview()
		}
	case "s":
		if len(m.skips()) > 0 {
			m.level, m.entriesTop, m.top = skippedLevel, m.top, 0
		}
	case "esc":
		if m.filter != "" {
			m.filter, m.cursor = "", 0
			return m, nil
		}
		m.backToList()
	case "left":
		m.backToList()
	}
	return m, nil
}

// tickedEntries is the open marketplace's ticked entries the filter shows, in page order, and how many
// more are ticked but hidden by it, which i and d leave alone and the ticks keep for when the filter
// clears; none in a preview.
func (m marketPicker) tickedEntries() (shown []engine.PickerEntry, hidden int) {
	if m.preview {
		return nil, 0
	}
	visible := m.entriesVisible()
	for i, e := range m.open() {
		switch {
		case !m.ticks[e.Name]:
		case slices.Contains(visible, i):
			shown = append(shown, e)
		default:
			hidden++
		}
	}
	return shown, hidden
}

// tickAll ticks the entries vis (indexes into open()) names, or unticks them when all are ticked.
func (m *marketPicker) tickAll(vis []int) {
	all := true
	for _, i := range vis {
		all = all && m.ticks[m.open()[i].Name]
	}
	for _, i := range vis {
		m.ticks[m.open()[i].Name] = !all
	}
}

// backToList leaves the entries page for the list, restoring the list's cursor, scroll and filter.
func (m *marketPicker) backToList() {
	m.level, m.preview = listLevel, false
	m.cursor, m.top, m.filter = m.saved.cursor, m.saved.top, m.saved.filter
}

// detailKey applies a key pressed on an entry's detail page: i installs the entry and d uninstalls
// it, and Esc or Left goes back to the entries, the cursor on it.
func (m marketPicker) detailKey(key string) (marketPicker, tea.Cmd) {
	e := m.open()[m.entry]
	if key == "esc" || key == "left" {
		m.level, m.top = entriesLevel, 0
		m.cursor = max(slices.Index(m.entriesVisible(), m.entry), 0)
		return m, nil
	}
	if key == "o" {
		return m.openEntry(e)
	}
	if key == "m" && m.preview {
		return m.enablePreview()
	}
	return m.installKey(key, e)
}

// skippedKey applies a key pressed on the skipped page, which only scrolls: Esc or Left goes back
// to the entries, as they were; follow keeps top within the page.
func (m marketPicker) skippedKey(key string) (marketPicker, tea.Cmd) {
	switch key {
	case "up", "k", "ctrl+p":
		m.top--
	case "down", "j", "ctrl+n":
		m.top++
	case "pgup", "b":
		m.top -= max(m.room(), 1)
	case "pgdown", "space", "f":
		m.top += max(m.room(), 1)
	case "home", "g":
		m.top = 0
	case "end", "G":
		m.top = len(m.list())
	case "esc", "left":
		m.level, m.top = entriesLevel, m.entriesTop
	}
	return m, nil
}

// skips is the open marketplace's skipped entries: its preview's when it is a known one you have
// not enabled.
func (m marketPicker) skips() []engine.MarketSkip {
	if m.preview {
		return m.previews[m.rows[m.market].ID].Skipped
	}
	return m.skipped[m.rows[m.market].ID]
}

// skippedTitle is the skipped page's title: the marketplace's ID and how many entries it skipped.
func (m marketPicker) skippedTitle() string {
	noun := "entries"
	if len(m.skips()) == 1 {
		noun = "entry"
	}
	return fmt.Sprintf("%s — %d skipped %s", oneLine(m.rows[m.market].ID), len(m.skips()), noun)
}

// skippedLines is the skipped page: each entry's name, then its reason wrapped under it, indented
// as the entries' second lines are.
func (m marketPicker) skippedLines() []line {
	f := &m.t.Focused
	var lines []line
	for _, s := range m.skips() {
		for w := range strings.SplitSeq(m.wrap(oneLine(s.Name), 0), "\n") {
			lines = append(lines, line{f.Option.Render(w), -1})
		}
		for w := range strings.SplitSeq(m.wrap(oneLine(s.Reason), len(entryIndent)), "\n") {
			lines = append(lines, line{entryIndent + f.Description.Render(w), -1})
		}
	}
	return lines
}

// openedMsg is the result of a browser launch: the address, and what went wrong, if anything.
type openedMsg struct {
	address string
	err     error
}

// openWeb opens address in a browser: the launch runs in the command this returns, never in
// Update; with no address the note is none, and with one checkWebURL refuses it is refused, and
// nothing is started.
func (m marketPicker) openWeb(address, none, refused string) (marketPicker, tea.Cmd) {
	switch {
	case address == "":
		m.note = none
	case checkWebURL(address) != nil:
		m.note = refused
	default:
		open := m.openURL
		return m, func() tea.Msg { return openedMsg{address, open(address)} }
	}
	return m, nil
}

// openEntry opens e's web page, as openWeb does.
func (m marketPicker) openEntry(e engine.PickerEntry) (marketPicker, tea.Cmd) {
	return m.openWeb(e.WebURL, "no web page for this entry", "refusing to open this entry's address: it is not a plain http(s) address")
}

// openRow opens the web address of list row i, as openWeb does; i is len(rows) on Add a source…,
// which has none.
func (m marketPicker) openRow(i int) (marketPicker, tea.Cmd) {
	if i == len(m.rows) {
		m.note = "nothing to open on " + addSourceRow
		return m, nil
	}
	id := oneLine(m.rows[i].ID)
	return m.openWeb(m.rows[i].WebURL, "no web address for "+id, "refusing to open "+id+"'s address: it is not a plain http(s) address")
}

// note is what the picker says of a launch, for a screen width: the address cut to fit when the
// browser was started, else what failed with the whole address, to copy.
func (o openedMsg) note(width int) string {
	switch {
	case o.err == nil && width > 0:
		return "opened " + ansi.Truncate(o.address, max(width-len("opened "), 1), "…")
	case o.err == nil:
		return "opened " + o.address
	}
	var missing *exec.Error
	if errors.As(o.err, &missing) && errors.Is(o.err, exec.ErrNotFound) {
		return "could not open a browser (" + missing.Name + " not found); the address is " + o.address
	}
	return "could not open a browser (" + oneLine(o.err.Error()) + "); the address is " + o.address
}

// installKey applies i or d to e, on the entries page or its detail page: i installs it and d
// uninstalls it, each saying why when it does not apply, and in a preview that the marketplace is
// not yours yet; any other key does nothing.
func (m marketPicker) installKey(key string, e engine.PickerEntry) (marketPicker, tea.Cmd) {
	switch {
	case m.preview && (key == "i" || key == "d"):
		m.note = "not added yet; m enables " + oneLine(m.rows[m.market].ID)
	case m.scope == "" && key == "d":
		m.note = "no scope to read; d is unavailable"
	case key == "i" && e.Installed:
		m.note = "already installed; d uninstalls"
	case key == "i":
		return m.leaveEntry(PickerInstall, e)
	case key == "d" && !e.Installed:
		m.note = "not installed; i installs"
	case key == "d":
		return m.leaveEntry(PickerUninstall, e)
	}
	return m, nil
}

// installMany applies i or d to es, the entries ticked on the entries page or numbered in the
// accessible prompt, which who names in a note: i installs all of them, those installed too, which
// install leaves as they are, and d uninstalls those that are installed; each says why when it does
// not apply, and in a preview that the marketplace is not yours yet.
func (m marketPicker) installMany(key string, es []engine.PickerEntry, who string) (marketPicker, tea.Cmd) {
	installed := slices.DeleteFunc(slices.Clone(es), func(e engine.PickerEntry) bool { return !e.Installed })
	switch {
	case m.preview:
		m.note = "not added yet; m enables " + oneLine(m.rows[m.market].ID)
	case m.scope == "" && key == "d":
		m.note = "no scope to read; d is unavailable"
	case key == "i" && len(installed) == len(es):
		m.note = "already installed; d uninstalls"
	case key == "i":
		return m.leaveEntry(PickerInstall, es...)
	case len(installed) == 0:
		m.note = "none of " + who + " is installed"
	default:
		return m.leaveEntry(PickerUninstall, installed...)
	}
	return m, nil
}

// leaveEntry ends the picker with action on es, which are not none, cancelling a check that still runs.
func (m marketPicker) leaveEntry(action MarketPickerAction, es ...engine.PickerEntry) (marketPicker, tea.Cmd) {
	m.stopChecks()
	names := make([]string, len(es))
	for i, e := range es {
		names[i] = e.Name
	}
	m.answer = MarketPickerAnswer{Action: action, Market: m.rows[m.market].ID, Entry: names[0], Entries: names, FromEntries: m.level == entriesLevel}
	m.done = true
	return m, tea.Quit
}

// entryHead is an entry's first line after its cursor: its name and version, then installed or else
// cached. The spaces inside each fact are nbsp, so wrapping never splits one; drawn, they are spaces.
func entryHead(e engine.PickerEntry) string {
	title := oneLine(e.Name)
	if v := oneLine(e.Version); v != "" {
		title += " " + v
	}
	facts := []string{title}
	switch {
	case e.Installed:
		facts = append(facts, "installed")
	case e.Cached:
		facts = append(facts, "cached")
	}
	for n, f := range facts {
		facts[n] = strings.ReplaceAll(f, " ", nbsp)
	}
	return strings.Join(facts, " · ")
}

// entryDetail is an entry's lines under its head: its description, none when it has none.
func entryDetail(e engine.PickerEntry) []string {
	if e.Description == "" {
		return nil
	}
	return []string{oneLine(e.Description)}
}

// entriesHeader is the lines above an entries or detail page: the marketplace's ID, the scope's
// context line, the filter.
func (m marketPicker) entriesHeader() []string {
	f := &m.t.Focused
	title := oneLine(m.rows[m.market].ID)
	switch {
	case m.level == skippedLevel:
		title = m.skippedTitle()
	case m.preview:
		title = m.previewTitle()
	}
	var lines []string
	for w := range strings.SplitSeq(m.wrap(title, 0), "\n") {
		lines = append(lines, f.Title.Render(w))
	}
	lines = append(lines, m.contextLines()...)
	if m.level != skippedLevel && (m.typing || m.filter != "") {
		cur := ""
		if m.typing {
			cur = "█"
		}
		lines = append(lines, m.cut(f.TextInput.Prompt.UnsetString().Render("/")+f.TextInput.Text.Render(oneLine(m.filter))+cur))
	}
	return lines
}

// contextLines is the scope's context line, drawn under the title of every page; none when the
// picker was given none.
func (m marketPicker) contextLines() []string {
	if m.context == "" {
		return nil
	}
	return []string{m.cut(m.t.Focused.Description.Render(oneLine(m.context)))}
}

// entriesList is the lines of the entries page, each entry's head and description wrapped to the
// width, or of the detail or skipped page, none of them an item (the skipped page scrolls from its
// top).
func (m marketPicker) entriesList() []line {
	switch m.level {
	case detailLevel:
		return m.detailLines()
	case skippedLevel:
		return m.skippedLines()
	}
	f := &m.t.Focused
	var lines []line
	vis := m.entriesVisible()
	if len(vis) == 0 {
		return []line{{m.cut(f.Description.Render("no entry matches the filter")), -1}}
	}
	// With checkboxes the name sits past them, and the description two columns past the name.
	lead, text := entryIndent, f.Option
	if !m.preview {
		lead = pickerIndent
	}
	for p, i := range vis {
		e := m.open()[i]
		cursor, box := "  ", ""
		if p == m.cursor {
			cursor = f.MultiSelectSelector.String()
		}
		if !m.preview {
			box, text = f.UnselectedPrefix.String(), f.UnselectedOption
			if m.ticks[e.Name] {
				box, text = f.SelectedPrefix.String(), f.SelectedOption
			}
		}
		for n, h := range strings.Split(strings.ReplaceAll(m.wrap(entryHead(e), len(lead)), nbsp, " "), "\n") {
			first := cursor + box
			if n > 0 {
				first = lead
			}
			lines = append(lines, line{first + text.Render(h), p})
		}
		for _, d := range entryDetail(e) {
			for w := range strings.SplitSeq(m.wrap(d, len(lead)+2), "\n") {
				lines = append(lines, line{lead + "  " + f.Description.Render(w), p})
			}
		}
	}
	return lines
}

// detailLines is the detail page: the entry's name and version, then under it, indented as the
// entries' lines are, its description, its tags, address and license when it has them, and whether it is
// installed, at which commit, and in your cache; one blank line sits between each of those blocks
// that is present.
func (m marketPicker) detailLines() []line {
	f := &m.t.Focused
	e := m.open()[m.entry]
	title := oneLine(e.Name)
	if v := oneLine(e.Version); v != "" {
		title += " " + v
	}
	state := "not installed"
	if e.Installed {
		state = "installed"
		if e.At != "" {
			at := oneLine(e.At)
			if commit, ok := source.CommitRef(e.At); ok {
				at = commit[:7]
			} else if hex, ok := strings.CutPrefix(e.At, "sha256:"); ok {
				at = "sha256:" + shortCommit(oneLine(hex))
			}
			state += " (" + at + ")"
		}
	}
	if e.Cached {
		state += " · in your cache"
	}
	var description, metadata []string
	for _, d := range entryDetail(e) {
		description = append(description, f.Description.Render(d))
	}
	if len(e.Tags) > 0 {
		tags := make([]string, len(e.Tags))
		for n, t := range e.Tags {
			tags[n] = oneLine(t)
		}
		metadata = append(metadata, f.Description.Render("tags: "+strings.Join(tags, ", ")))
	}
	if e.URL != "" {
		metadata = append(metadata, f.Description.Render("url: "+oneLine(e.URL)))
	}
	if e.License != "" {
		metadata = append(metadata, f.Description.Render("license: "+oneLine(e.License)))
	}
	var lines []line
	for w := range strings.SplitSeq(m.wrap(f.Title.Render(title), 0), "\n") {
		lines = append(lines, line{w, -1})
	}
	// The description, the metadata and the state are blocks under the title, a blank line between
	// those present.
	shownBlocks := 0
	for _, block := range [][]string{description, metadata, {f.Option.Render(state)}} {
		if len(block) == 0 {
			continue
		}
		if shownBlocks > 0 {
			lines = append(lines, line{"", -1})
		}
		shownBlocks++
		for _, t := range block {
			for w := range strings.SplitSeq(m.wrap(t, len(entryIndent)), "\n") {
				lines = append(lines, line{entryIndent + w, -1})
			}
		}
	}
	return lines
}

// entryKey is a key that acts on an entry, for the footer and the accessible detail prompt: its
// letter, what it does and, for uninstall, the scope it does it in ("from project").
type entryKey struct{ letter, verb, scope string }

// text is the key as the footer and the prompts name it.
func (k entryKey) text() string {
	return strings.TrimSpace(k.letter + " " + k.verb + " " + k.scope)
}

// entryKeys is the keys that act on e, in the order they are named: m enables the marketplace in a
// preview, else i installs e (the install flow asks its own scope, so none is named) or d uninstalls
// it from the scope the picker reads, which d names; with no such scope (production always has one)
// d is not offered; then o opens its web page when it has one.
func (m marketPicker) entryKeys(e engine.PickerEntry) []entryKey {
	var keys []entryKey
	switch {
	case m.preview:
		keys = append(keys, entryKey{letter: "m", verb: "enable"})
	case !e.Installed:
		keys = append(keys, entryKey{letter: "i", verb: "install"})
	case m.scope != "":
		keys = append(keys, entryKey{letter: "d", verb: "uninstall", scope: "from " + m.scope})
	}
	if e.WebURL != "" {
		keys = append(keys, entryKey{letter: "o", verb: "web"})
	}
	return keys
}

// skippedNote is what the entries page says of the catalog's entries lola could not take, and the
// key that shows why; empty when none were skipped.
func (m marketPicker) skippedNote() string {
	n := len(m.skips())
	if n == 0 {
		return ""
	}
	noun := "entries"
	if n == 1 {
		noun = "entry"
	}
	return fmt.Sprintf("%d %s skipped · s shows why", n, noun)
}

// entriesKeys is the keys of the accessible entries page, printed above its short prompt: in a
// preview m enables the marketplace and i and d only say it is not added, so it names neither; s
// is named when the catalog skipped entries.
func (m marketPicker) entriesKeys() string {
	keys := []string{"i<n,n…> install"}
	switch {
	case m.preview:
		keys = []string{"m enable"}
	case m.scope != "":
		keys = append(keys, "d<n,n…> uninstall from "+m.scope)
	}
	if len(m.skips()) > 0 {
		keys = append(keys, "s skipped")
	}
	return strings.Join(append(keys, "o<n> web", "Enter alone goes back"), " · ")
}

// keysText is keys as the footer names them, each with a trailing separator.
func keysText(keys []entryKey) string {
	var text string
	for _, k := range keys {
		text += k.text() + " · "
	}
	return text
}

// tickKeys is the keys of the entries page with the cursor on e, each with a trailing separator: with
// nothing ticked, those that act on e, then x tick; else how many are ticked (and how many of those the
// filter hides), i when a shown one is not installed, d when one is (and a scope is read), x tick, and
// o when e has a web page. A preview
// has nothing to tick.
func (m marketPicker) tickKeys(e engine.PickerEntry) string {
	ticked, hidden := m.tickedEntries()
	if m.preview {
		return keysText(m.entryKeys(e))
	}
	if len(ticked) == 0 && hidden == 0 {
		return keysText(m.entryKeys(e)) + "x tick · "
	}
	installed := 0
	for _, t := range ticked {
		if t.Installed {
			installed++
		}
	}
	keys := []entryKey{}
	if installed < len(ticked) {
		keys = append(keys, entryKey{letter: "i", verb: "install"})
	}
	if installed > 0 && m.scope != "" {
		keys = append(keys, entryKey{letter: "d", verb: "uninstall", scope: "from " + m.scope})
	}
	keys = append(keys, entryKey{letter: "x", verb: "tick"})
	if e.WebURL != "" {
		keys = append(keys, entryKey{letter: "o", verb: "web"})
	}
	count := fmt.Sprintf("%d ticked", len(ticked)+hidden)
	if hidden > 0 {
		count += fmt.Sprintf(" (%d hidden)", hidden)
	}
	return count + " · " + keysText(keys)
}

// entriesFooter is the lines under an entries or detail page: the note, then the keys, naming only
// the one of i and d that applies to the entry under the cursor (none on an empty filtered list).
func (m marketPicker) entriesFooter() []string {
	var lines []string
	if skipped := m.skippedNote(); skipped != "" && m.level == entriesLevel {
		lines = append(lines, m.noteLines(skipped)...)
	}
	if m.note != "" {
		lines = append(lines, m.noteLines(m.note)...)
	}
	more := ""
	if len(m.skips()) > 0 {
		more = "s skipped · "
	}
	keys := more + "/ filter · ←/esc back · q quit"
	switch vis := m.entriesVisible(); {
	case m.typing:
		keys = "⏎ keep filter · esc clear · ↑↓ move"
	case m.level == skippedLevel:
		keys = "↑↓ scroll · ←/esc back · q quit"
	case m.level == detailLevel:
		keys = keysText(m.entryKeys(m.open()[m.entry])) + "←/esc back · q quit"
	case m.cursor < len(vis):
		keys = m.tickKeys(m.open()[vis[m.cursor]]) + more + "⏎/→ open · ←/esc back · / filter · q quit"
	}
	for _, k := range wrapKeys(keys, m.width, 2) {
		lines = append(lines, m.t.Help.ShortDesc.Render(k))
	}
	return lines
}
