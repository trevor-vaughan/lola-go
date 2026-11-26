package cli

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// failureCap is how many bytes of a failed fetch's reason the list's note shows: more than a name's
// cap, since the reason is what the user needs.
const failureCap = 1024

// previewMsg is the result of fetching the known marketplace id, the fetch numbered run.
type previewMsg struct {
	run int
	id  string
	pv  engine.KnownPreview
	err error
}

// drillRow opens what Enter or Right opens on row, with nothing ticked: the entries of one of yours
// (drill), or the preview of a known one you have not enabled (drillKnown). ok is false for
// Add a source…, which the caller treats as its key always did.
func (m marketPicker) drillRow(row int) (_ marketPicker, _ tea.Cmd, ok bool) {
	switch {
	case row < m.own:
		m, cmd := m.drill(row)
		return m, cmd, true
	case row < len(m.rows):
		m, cmd := m.drillKnown(row)
		return m, cmd, true
	}
	return m, nil, false
}

// drillKnown opens the preview of the known row: at once when it was fetched before this session
// (a preview of a pinned commit never goes stale), else by starting the fetch in a command, which
// the user can cancel; the fetch of this row already running is left to finish, and any other is
// cancelled, as opening a kept preview does.
func (m marketPicker) drillKnown(row int) (marketPicker, tea.Cmd) {
	id := m.rows[row].ID
	if m.fetchID == id {
		return m, nil
	}
	m.stopFetch()
	if _, ok := m.previews[id]; ok {
		return m.openPreview(row), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.fetchRun++
	m.fetchID, m.fetchRow, m.fetchStop = id, row, cancel
	run, fetch := m.fetchRun, m.previewFn
	return m, func() tea.Msg {
		pv, err := fetch(ctx, id)
		return previewMsg{run: run, id: id, pv: pv, err: err}
	}
}

// stopFetch cancels the fetch that runs, if any; its result is dropped when it arrives.
func (m *marketPicker) stopFetch() {
	if m.fetchStop != nil {
		m.fetchStop()
	}
	m.fetchID, m.fetchStop = "", nil
}

// fetching is the line the footer shows while a preview is fetched: what is fetched, from where,
// and that it changes nothing.
func (m marketPicker) fetching() string {
	r := m.rows[m.fetchRow]
	line := "fetching " + oneLine(r.ID) + " at " + shortCommit(oneLine(r.Pin))
	if host := oneLine(source.Host(r.Source)); host != "" {
		line += " from " + host
	}
	return line + " — nothing is added or installed…"
}

// previewed applies the result of a fetch: a preview with entries opens its entries page; a failure,
// or a catalog with no entry, leaves the list with a note saying why. A result that arrives while
// the filter is typed or an update is offered is kept for Enter, with a note, and does not move the
// screen. A result of a fetch that is not the current one, or that arrives once the picker is left,
// is dropped.
func (m marketPicker) previewed(msg previewMsg) marketPicker {
	if msg.run != m.fetchRun || msg.id != m.fetchID || m.done {
		return m
	}
	row := m.fetchRow
	m.stopFetch()
	id := oneLine(msg.id)
	switch {
	case msg.err != nil:
		reason := untrusted.Text(strings.ReplaceAll(untrusted.CleanErr(msg.err).Error(), "\n", " "), failureCap)
		m.note = "could not fetch " + id + ": " + reason
		if strings.HasPrefix(reason, "could not fetch") {
			m.note = id + ": " + reason
		}
		if hints := errs.Hints(msg.err); len(hints) > 0 {
			m.note += " → " + oneLine(hints[0])
		}
	case len(msg.pv.Entries) == 0:
		m.note = id + " at " + shortCommit(oneLine(msg.pv.Commit)) + " offers no entries; " +
			errs.ShellJoin([]string{"lola", "market", "enable", id}) + " enables it anyway"
	case m.level != listLevel || m.typing || m.offer != nil:
		m.previews[msg.id] = msg.pv
		m.note = id + " fetched; ⏎ opens it"
	default:
		m.previews[msg.id] = msg.pv
		m = m.openPreview(row)
		if len(msg.pv.Warnings) > 0 {
			m.note = oneLine(msg.pv.Warnings[0])
		}
	}
	return m
}

// openPreview opens the entries page of the known row's preview, saving the list's cursor, scroll
// and filter for Esc to restore.
func (m marketPicker) openPreview(row int) marketPicker {
	m.saved.cursor, m.saved.top, m.saved.filter = m.cursor, m.top, m.filter
	m.level, m.market, m.preview, m.cursor, m.top, m.filter = entriesLevel, row, true, 0, 0, ""
	return m
}

// previewTitle is the entries page's title for a preview: it says the marketplace is not yours
// yet, and which commit of it is shown.
func (m marketPicker) previewTitle() string {
	pv := m.previews[m.rows[m.market].ID]
	return "previewing " + oneLine(m.rows[m.market].ID) + " — not added; pinned to " + shortCommit(oneLine(pv.Commit)) +
		", curated " + oneLine(pv.Curated)
}

// enablePreview leaves the picker to enable the known row of the preview, as m on its pages does.
func (m marketPicker) enablePreview() (marketPicker, tea.Cmd) {
	return m.leave(PickerEnable, []int{m.market})
}

// noteLines is a note wrapped to the width, one footer line each.
func (m marketPicker) noteLines(note string) []string {
	var lines []string
	for w := range strings.SplitSeq(wrapSpaces(note, m.width), "\n") {
		lines = append(lines, m.t.Focused.Description.Render(w))
	}
	return lines
}
