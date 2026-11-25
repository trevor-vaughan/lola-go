package cli

import (
	"context"
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// previewer is a MarketPickerStep.Preview that counts its calls, records each context and returns
// what the spec set.
type previewer struct {
	calls int
	ctxs  []context.Context
	pv    engine.KnownPreview
	err   error
}

func (p *previewer) preview(ctx context.Context, id string) (engine.KnownPreview, error) {
	p.calls++
	p.ctxs = append(p.ctxs, ctx)
	return p.pv, p.err
}

// previewEntries are the entries a known marketplace's catalog offers in the specs: one with a web
// page, one without.
func previewEntries() []engine.PickerEntry {
	return []engine.PickerEntry{
		{MarketOffer: engine.MarketOffer{Install: "@kn/lint", Name: "lint", Version: "1.0.0", Description: "Lints"}, WebURL: "https://example.com/kn/lint"},
		{MarketOffer: engine.MarketOffer{Install: "@kn/fmt", Name: "fmt"}},
	}
}

// previewPicker is pickerRows with p as the known row's preview (entries previewEntries unless the
// spec set them or an error), on a terminal width by height.
func previewPicker(p *previewer, width, height int) marketPicker {
	own, known := pickerRows()
	p.pv.ID, p.pv.Curated, p.pv.Commit = "kn", "2026-11-22", strings.Repeat("b", 40)
	if p.pv.Entries == nil && p.err == nil {
		p.pv.Entries = previewEntries()
	}
	return newMarketPicker(MarketPickerStep{Own: own, Known: known, Preview: p.preview, Context: "project /srv/app", ScopeKind: "project",
		OpenURL: func(string) error { return nil }}, plainTheme(), width, height)
}

// toKnown moves the cursor onto the known row and presses key.
func toKnown(m marketPicker, key tea.KeyPressMsg) (marketPicker, tea.Cmd) {
	return pick(m, keyDown, keyDown, keyDown, key)
}

// previewing is the picker with the known row's preview open on its entries page.
func previewing(p *previewer) marketPicker {
	m, cmd := toKnown(previewPicker(p, 100, 40), keyEnter)
	m, _ = pick(m, cmd())
	Expect(m.level).To(Equal(entriesLevel))
	return m
}

var _ = Describe("the marketplace picker's preview of a known marketplace", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("starts the fetch from a command on Enter, never in Update, and says plainly what is going on", func() {
		p := &previewer{}
		m, cmd := toKnown(previewPicker(p, 100, 40), keyEnter)
		Expect(cmd).NotTo(BeNil())
		Expect(p.calls).To(BeZero(), "Update itself fetches nothing")
		Expect(m.done).To(BeFalse())
		Expect(m.level).To(Equal(listLevel))
		Expect(drawn(m)).To(ContainSubstring("fetching kn at bbbbbbb from https://example.com — nothing is added or installed…"))
		m, _ = pick(m, cmd())
		Expect(p.calls).To(Equal(1))
		Expect(drawn(m)).NotTo(ContainSubstring("fetching kn"))
	})

	It("starts the fetch on Right too", func() {
		m, cmd := toKnown(previewPicker(&previewer{}, 100, 40), keyRight)
		Expect(cmd).NotTo(BeNil())
		Expect(drawn(m)).To(ContainSubstring("fetching kn"))
	})

	It("opens the entries in preview under a header that says it is not added, above the context line", func() {
		m := previewing(&previewer{})
		out := drawn(m)
		lines := strings.Split(out, "\n")
		Expect(lines[0]).To(Equal("previewing kn — not added; pinned to bbbbbbb, curated 2026-11-22"))
		Expect(lines[1]).To(Equal("project /srv/app"))
		Expect(out).To(And(ContainSubstring("lint 1.0.0"), ContainSubstring("fmt"), Not(ContainSubstring("acme"))))
	})

	It("lets the entries and detail pages work as for your own: filter, detail, o, arrows, esc", func() {
		var opened []string
		p := &previewer{}
		m := previewPicker(p, 100, 40)
		m.openURL = func(u string) error { opened = append(opened, u); return nil }
		m, cmd := toKnown(m, keyEnter)
		m, _ = pick(m, cmd())
		m, _ = pick(m, append(presses("/fmt"), keyEnter)...)
		Expect(drawn(m)).To(And(ContainSubstring("> fmt"), Not(ContainSubstring("lint 1.0.0"))))
		m, _ = pick(m, keyEsc)
		m, _ = pick(m, keyRight)
		Expect(m.level).To(Equal(detailLevel))
		Expect(drawn(m)).To(ContainSubstring("lint 1.0.0"))
		m, cmd = pick(m, keyO)
		m, _ = pick(m, cmd())
		Expect(opened).To(Equal([]string{"https://example.com/kn/lint"}))
		m, _ = pick(m, keyLeft)
		Expect(m.level).To(Equal(entriesLevel))
		m, _ = pick(m, keyEsc)
		Expect(m.level).To(Equal(listLevel))
		Expect(drawn(m)).To(And(ContainSubstring("> [ ] kn"), Not(ContainSubstring("previewing"))))
	})

	It("names m enable in the footers and leaves out install and uninstall", func() {
		m := previewing(&previewer{})
		for range 2 {
			Expect(drawn(m)).To(And(ContainSubstring("m enable"), ContainSubstring("o web"),
				Not(ContainSubstring("i install")), Not(ContainSubstring("d uninstall"))))
			m, _ = pick(m, keyEnter)
		}
		Expect(m.level).To(Equal(detailLevel))
	})

	It("answers i and d with a note, on the entries page and the detail page, and never leaves the picker", func() {
		m := previewing(&previewer{})
		for range 2 {
			for _, k := range []tea.KeyPressMsg{keyI, keyD} {
				var cmd tea.Cmd
				m, cmd = pick(m, k)
				Expect(m.done).To(BeFalse())
				Expect(cmd).To(BeNil())
				Expect(drawn(m)).To(ContainSubstring("not added yet; m enables kn"))
			}
			m, _ = pick(m, keyEnter)
		}
		Expect(m.level).To(Equal(detailLevel))
	})

	It("answers m on the entries page and on the detail page as m on the known row does", func() {
		m, cmd := pick(previewing(&previewer{}), keyM)
		Expect(m.answer).To(Equal(MarketPickerAnswer{Action: PickerEnter, Known: []string{"kn"}}))
		Expect(cmd()).To(Equal(tea.QuitMsg{}))
		m, _ = pick(previewing(&previewer{}), keyEnter, keyM)
		Expect(m.level).To(Equal(detailLevel))
		Expect(m.answer).To(Equal(MarketPickerAnswer{Action: PickerEnter, Known: []string{"kn"}}))
	})

	It("stays on the list with the real reason and a working hint when the fetch fails, and opens no entries page", func() {
		p := &previewer{err: errs.WithHints(errors.New("could not reach example.com: no route to host"), "lola market --enable kn -n", "lola market --known")}
		m, cmd := toKnown(previewPicker(p, 100, 40), keyEnter)
		m, _ = pick(m, cmd())
		Expect(m.level).To(Equal(listLevel))
		Expect(m.done).To(BeFalse())
		Expect(drawn(m)).To(And(ContainSubstring("could not reach example.com: no route to host"),
			ContainSubstring("→ lola market --enable kn -n"), Not(ContainSubstring("fetching kn"))))
	})

	It("stays on the list, saying so, when the pinned catalog offers no entry", func() {
		p := &previewer{pv: engine.KnownPreview{Entries: []engine.PickerEntry{}}}
		m, cmd := toKnown(previewPicker(p, 100, 40), keyEnter)
		m, _ = pick(m, cmd())
		Expect(m.level).To(Equal(listLevel))
		Expect(drawn(m)).To(ContainSubstring("kn at bbbbbbb offers no entries; m enables it anyway"))
	})

	It("cancels the fetch on Esc, stays on the list, and drops a result that still arrives", func() {
		p := &previewer{}
		m, cmd := toKnown(previewPicker(p, 100, 40), keyEnter)
		late := cmd()
		m, _ = pick(m, keyEsc)
		Expect(p.ctxs[0].Err()).To(MatchError(context.Canceled))
		Expect(m.done).To(BeFalse())
		Expect(drawn(m)).To(And(Not(ContainSubstring("fetching kn at")), ContainSubstring("cancelled fetching kn")))
		m, _ = pick(m, late)
		Expect(m.level).To(Equal(listLevel))
	})

	It("cancels the fetch when the picker is quit with q or Ctrl+C", func() {
		for _, k := range []tea.KeyPressMsg{keyQ, {Code: 'c', Mod: tea.ModCtrl}} {
			p := &previewer{}
			m, cmd := toKnown(previewPicker(p, 100, 40), keyEnter)
			cmd()
			m, _ = pick(m, k)
			Expect(m.done).To(BeTrue())
			Expect(m.answer.Action).To(Equal(PickerCancel))
			Expect(p.ctxs[0].Err()).To(MatchError(context.Canceled))
		}
	})

	It("cancels the fetch when you open one of yours, and a result that arrives later changes nothing", func() {
		p := &previewer{}
		m := entryPicker(&checker{})
		m.previewFn = p.preview
		m, cmd := toKnown(m, keyEnter)
		late := cmd()
		m, _ = pick(m, keyUp, keyUp, keyUp, keyEnter)
		Expect(p.ctxs[0].Err()).To(MatchError(context.Canceled))
		m, _ = pick(m, late)
		Expect(m.level).To(Equal(entriesLevel))
		Expect(drawn(m)).To(And(ContainSubstring("review 1.2.0"), Not(ContainSubstring("previewing"))))
	})

	It("cancels the fetch of one known row when another is opened, and starts that one's", func() {
		p := &previewer{}
		own, known := pickerRows()
		known = append(known, engine.PickerRow{ID: "k2", Known: true, Pin: strings.Repeat("d", 40), MarketRow: engine.MarketRow{Source: "https://example.org/k2.git"}})
		m := newMarketPicker(MarketPickerStep{Own: own, Known: known, Preview: p.preview}, plainTheme(), 100, 40)
		m, first := toKnown(m, keyEnter)
		late := first()
		m, second := pick(m, keyDown, keyEnter)
		Expect(second).NotTo(BeNil())
		Expect(p.ctxs[0].Err()).To(MatchError(context.Canceled))
		Expect(drawn(m)).To(ContainSubstring("fetching k2 at ddddddd from https://example.org"))
		m, _ = pick(m, late)
		Expect(drawn(m)).To(ContainSubstring("fetching k2"), "the first fetch's result is dropped")
		Expect(m.level).To(Equal(listLevel))
	})

	It("opens a preview kept from before without a running fetch of another row taking over the screen", func() {
		var calls []string
		own, known := pickerRows()
		known = append(known, engine.PickerRow{ID: "k2", Known: true, Pin: strings.Repeat("d", 40), MarketRow: engine.MarketRow{Source: "https://example.org/k2.git"}})
		fetch := func(ctx context.Context, id string) (engine.KnownPreview, error) {
			calls = append(calls, id)
			pv := engine.KnownPreview{Entries: previewEntries()}
			pv.ID, pv.Curated, pv.Commit = id, "2026-11-22", strings.Repeat("b", 40)
			return pv, nil
		}
		m := newMarketPicker(MarketPickerStep{Own: own, Known: known, Preview: fetch, Context: "project /srv/app", ScopeKind: "project"}, plainTheme(), 100, 40)
		m, cmd := toKnown(m, keyEnter) // preview kn
		m, _ = pick(m, cmd())
		m, _ = pick(m, keyEsc, keyDown)
		m, a := pick(m, keyEnter) // k2's fetch starts
		Expect(drawn(m)).To(ContainSubstring("fetching k2"))
		late := a()
		m, _ = pick(m, keyUp)
		m, cmd = pick(m, keyEnter) // kn again, from what was kept
		Expect(cmd).To(BeNil())
		Expect(drawn(m)).To(ContainSubstring("previewing kn"))
		m, _ = pick(m, late)
		Expect(m.level).To(Equal(entriesLevel))
		Expect(drawn(m)).To(And(ContainSubstring("previewing kn"), Not(ContainSubstring("previewing k2"))))
		m, _ = pick(m, keyEsc)
		Expect(m.level).To(Equal(listLevel))
		Expect(drawn(m)).To(ContainSubstring("> [ ] kn"), "the list's cursor is on kn's row")
		Expect(drawn(m)).NotTo(ContainSubstring("fetching k2"))
		Expect(m.previews).NotTo(HaveKey("k2"), "the cancelled fetch's result is dropped")
	})

	It("keeps a result in the session, saying so, when the picker is no longer on the plain list", func() {
		for _, setup := range []func(marketPicker) marketPicker{
			func(m marketPicker) marketPicker {
				m, _ = pick(m, keyDown, keyDown, keyDown)
				m, _ = pick(m, presses("/")...)
				return m
			},
			func(m marketPicker) marketPicker { m.offer = []int{0}; return m },
		} {
			p := &previewer{}
			m, cmd := toKnown(previewPicker(p, 100, 40), keyEnter)
			result := cmd()
			m = setup(m)
			m, _ = pick(m, result)
			Expect(m.level).To(Equal(listLevel))
			Expect(m.previews).To(HaveKey("kn"))
			Expect(m.note).To(Equal("kn fetched; ⏎ opens it"))
		}
	})

	It("says a fetch it cancelled by opening a row of yours without entries", func() {
		p := &previewer{}
		m := entryPicker(&checker{})
		m.previewFn = p.preview
		m, _ = toKnown(m, keyEnter)
		m, _ = pick(m, keyUp, keyEnter)
		Expect(m.fetchID).To(BeEmpty())
		Expect(drawn(m)).To(ContainSubstring("cancelled fetching kn; local has no entries; u fetches its catalog"))
	})

	It("starts a failure note with what was being fetched and keeps the reason and the hint visible at 80 columns", func() {
		reason := strings.Repeat("far away ", 40) + "TAIL-REASON"
		p := &previewer{err: errs.WithHints(errors.New(reason), "lola market --enable kn -n")}
		m, cmd := toKnown(previewPicker(p, 80, 40), keyEnter)
		m, _ = pick(m, cmd())
		out := drawn(m)
		Expect(out).To(And(ContainSubstring("could not fetch kn: far away"), ContainSubstring("TAIL-REASON"), MatchRegexp(`→ lola\s+market --enable kn -n`)))
	})

	It("bounds the reason of a failed fetch to failureCap, ending the note cleanly", func() {
		p := &previewer{err: errors.New(strings.Repeat("far away ", 400) + "TAIL-REASON")}
		m, cmd := toKnown(previewPicker(p, 100, 40), keyEnter)
		m, _ = pick(m, cmd())
		Expect(m.note).To(HavePrefix("could not fetch kn: far away"))
		Expect(m.note).NotTo(ContainSubstring("TAIL-REASON"))
		Expect(len(m.note)).To(BeNumerically("<=", len("could not fetch kn: ")+failureCap+len("…")))
	})

	It("shows the catalog's first warning on one line, stripped of control characters, when a preview opens", func() {
		p := &previewer{pv: engine.KnownPreview{Entries: previewEntries(), Warnings: []string{"first\nline \x1b[31mred", "second"}}}
		m := previewing(p)
		Expect(m.note).To(ContainSubstring("first line"))
		Expect(m.note).NotTo(ContainSubstring("second"))
		Expect(m.note).NotTo(ContainSubstring("\n"))
		Expect(m.note).NotTo(ContainSubstring("\x1b"))
		Expect(drawn(m)).To(ContainSubstring("first line"))
	})

	It("does not say could not fetch twice when the error already does", func() {
		p := &previewer{err: errors.New("could not fetch https://example.com/kn.git: Could not resolve host: example.com")}
		m, cmd := toKnown(previewPicker(p, 100, 40), keyEnter)
		m, _ = pick(m, cmd())
		Expect(m.note).To(Equal("kn: could not fetch https://example.com/kn.git: Could not resolve host: example.com"))

		p = &previewer{err: errors.New("the catalog is not valid")}
		m, cmd = toKnown(previewPicker(p, 100, 40), keyEnter)
		m, _ = pick(m, cmd())
		Expect(m.note).To(Equal("could not fetch kn: the catalog is not valid"))
	})

	It("says on the preview page how many entries lola does not offer, and which key shows why", func() {
		for n, want := range map[int]string{1: "1 entry skipped · s shows why", 3: "3 entries skipped · s shows why"} {
			p := &previewer{pv: engine.KnownPreview{Skipped: skips(n), Entries: previewEntries()}}
			m, cmd := toKnown(previewPicker(p, 100, 40), keyEnter)
			m, _ = pick(m, cmd())
			Expect(drawn(m)).To(ContainSubstring(want))
			m, _ = pick(m, keyDown)
			Expect(drawn(m)).To(ContainSubstring(want), "it stays while you browse")
		}
		m := previewing(&previewer{})
		Expect(drawn(m)).NotTo(ContainSubstring("skipped"))
	})

	It("keeps the old Enter on a known row when rows are ticked, and fetches nothing", func() {
		p := &previewer{}
		m, _ := toKnown(previewPicker(p, 100, 40), keyX)
		m, cmd := pick(m, keyEnter)
		Expect(m.answer).To(Equal(MarketPickerAnswer{Action: PickerEnter, Known: []string{"kn"}}))
		Expect(cmd()).To(Equal(tea.QuitMsg{}))
		Expect(p.calls).To(BeZero())
	})

	It("keeps m on the known row as the menu, without fetching", func() {
		p := &previewer{}
		m, _ := toKnown(previewPicker(p, 100, 40), keyM)
		Expect(m.answer).To(Equal(MarketPickerAnswer{Action: PickerEnter, Known: []string{"kn"}}))
		Expect(p.calls).To(BeZero())
	})

	It("keeps the preview for the session: going back and in again fetches nothing", func() {
		p := &previewer{}
		m := previewing(p)
		m, _ = pick(m, keyEsc)
		m, cmd := pick(m, keyEnter)
		Expect(cmd).To(BeNil())
		Expect(m.level).To(Equal(entriesLevel))
		Expect(p.calls).To(Equal(1))
		Expect(drawn(m)).To(ContainSubstring("previewing kn"))
	})

	It("does not mix a known preview with the entries of one of yours of the same id", func() {
		p := &previewer{}
		m := entryPicker(&checker{})
		m.previewFn = p.preview
		m.rows[m.own].ID = "acme"
		p.pv.ID, p.pv.Curated, p.pv.Commit, p.pv.Entries = "acme", "2026-11-22", strings.Repeat("b", 40), previewEntries()
		m, cmd := toKnown(m, keyEnter)
		m, _ = pick(m, cmd())
		Expect(drawn(m)).To(And(ContainSubstring("lint 1.0.0"), Not(ContainSubstring("review 1.2.0"))))
	})

	It("draws author-chosen text on one line, whatever it holds", func() {
		p := &previewer{err: errors.New("bad\nline \x1b[31mred\x1b[0m\r\nmore")}
		m := previewPicker(p, 100, 40)
		m.rows[m.own].ID, m.rows[m.own].Source = "k\nn\x1b[31m", "https://ex\nample.com/kn.git"
		m, cmd := toKnown(m, keyEnter)
		Expect(drawn(m)).NotTo(ContainSubstring("\x1b"))
		Expect(drawn(m)).To(ContainSubstring("fetching k n"))
		m, _ = pick(m, cmd())
		out := drawn(m)
		Expect(out).NotTo(ContainSubstring("\x1b"))
		Expect(out).To(MatchRegexp(`bad line .*red.* more`))
	})

	It("draws the fetching line, the preview and a failure at width 40 and 0 without panicking", func() {
		for _, width := range []int{40, 0} {
			p := &previewer{}
			m, cmd := toKnown(previewPicker(p, width, 12), keyEnter)
			Expect(func() { _ = drawn(m) }).NotTo(Panic())
			m, _ = pick(m, cmd())
			Expect(func() { _ = drawn(m) }).NotTo(Panic())
			m, _ = pick(m, keyEnter)
			Expect(func() { _ = drawn(m) }).NotTo(Panic())

			bad := &previewer{err: errors.New(strings.Repeat("far away ", 20))}
			m, cmd = toKnown(previewPicker(bad, width, 12), keyEnter)
			m, _ = pick(m, cmd())
			Expect(func() { _ = drawn(m) }).NotTo(Panic())
		}
	})

	It("draws no line wider than 40 cells at the list, entries, detail and preview levels, and none at width 0", func() {
		fits := func(m marketPicker, width int) {
			for l := range strings.SplitSeq(drawn(m), "\n") {
				Expect(ansi.StringWidth(l)).To(BeNumerically("<=", width), "level %d: %q", m.level, l)
			}
		}
		for _, width := range []int{40, 0} {
			own, known := pickerRows()
			p := &previewer{}
			p.pv.ID, p.pv.Curated, p.pv.Commit, p.pv.Entries = "kn", "2026-11-22", strings.Repeat("b", 40), previewEntries()
			m := newMarketPicker(MarketPickerStep{Own: own, Known: known, Preview: p.preview, Context: "project /srv/app", ScopeKind: "project",
				Entries: entryPicker(&checker{}).entries}, plainTheme(), width, 40)
			var shown []marketPicker
			shown = append(shown, m)
			m, _ = pick(m, keyEnter)
			shown = append(shown, m)
			m, _ = pick(m, keyEnter)
			shown = append(shown, m)
			m, _ = pick(m, keyEsc, keyEsc)
			m, cmd := toKnown(m, keyEnter)
			shown = append(shown, m)
			m, _ = pick(m, cmd())
			shown = append(shown, m)
			m, _ = pick(m, keyEnter)
			shown = append(shown, m)
			Expect(shown[1].level).To(Equal(entriesLevel))
			Expect(shown[2].level).To(Equal(detailLevel))
			Expect(m.preview).To(BeTrue())
			if width == 0 {
				for _, m := range shown {
					Expect(func() { _ = drawn(m) }).NotTo(Panic())
				}
				continue
			}
			for _, m := range shown {
				fits(m, width)
			}
		}
	})
})
