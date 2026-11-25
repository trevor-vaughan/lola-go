package cli

import (
	"bytes"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var keyS = tea.KeyPressMsg{Code: 's', Text: "s"}

// skips are the entries a catalog's skipped list holds in the specs: n of them, each with a reason
// long enough to wrap at 40 columns.
func skips(n int) []engine.MarketSkip {
	out := make([]engine.MarketSkip, n)
	for i := range out {
		out[i] = engine.MarketSkip{Name: fmt.Sprintf("skip-%02d", i), Reason: fmt.Sprintf("its source is an npm package, which lola does not take (%d)", i)}
	}
	return out
}

// skippedOwn is entryPicker with acme's catalog skipping n entries, on a terminal width by height.
func skippedOwn(n, width, height int) marketPicker {
	own, known := pickerRows()
	st := MarketPickerStep{Own: own, Known: known, Entries: scopedEntries(), Skipped: map[string][]engine.MarketSkip{"acme": skips(n)},
		Context: "project /srv/app", ScopeKind: "project"}
	return newMarketPicker(st, plainTheme(), width, height)
}

// skippedPreview is the picker with the known row's preview, which skipped n entries, open.
func skippedPreview(n int) marketPicker {
	return previewing(&previewer{pv: engine.KnownPreview{Skipped: skips(n), Entries: previewEntries()}})
}

var _ = Describe("the marketplace picker's skipped entries", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("counts the skipped entries on the entries page of a preview and of your own marketplace, and names s", func() {
		for _, m := range []marketPicker{skippedPreview(3), skippedOwn(3, 100, 40)} {
			if m.level == listLevel {
				m, _ = pick(m, keyEnter)
			}
			out := drawn(m)
			Expect(out).To(ContainSubstring("3 entries skipped · s shows why"))
			Expect(out).To(ContainSubstring("s skipped"))
			m, _ = pick(m, keyDown)
			Expect(drawn(m)).To(ContainSubstring("3 entries skipped · s shows why"), "it stays while you browse")
		}
		m, _ := pick(skippedOwn(1, 100, 40), keyEnter)
		Expect(drawn(m)).To(ContainSubstring("1 entry skipped · s shows why"))
	})

	It("keeps the entries page's keys, s among them, in two rows at 80 columns", func() {
		m, _ := pick(skippedOwn(3, 80, 40), keyEnter)
		lines := strings.Split(strings.TrimRight(drawn(m), "\n"), "\n")
		keys := strings.Join(lines[len(lines)-2:], " ")
		Expect(keys).To(And(ContainSubstring("i install · o web · x tick · s skipped · ⏎/→ open"), HaveSuffix("q quit")))
		Expect(lines[len(lines)-3]).To(Equal("3 entries skipped · s shows why"))
	})

	It("says nothing, and takes no s, when nothing was skipped", func() {
		m, _ := pick(skippedOwn(0, 100, 40), keyEnter)
		Expect(drawn(m)).NotTo(ContainSubstring("skipped"))
		m, _ = pick(m, keyS)
		Expect(m.level).To(Equal(entriesLevel))
		Expect(drawn(m)).NotTo(ContainSubstring("skipped"))
		m = previewing(&previewer{})
		Expect(drawn(m)).NotTo(ContainSubstring("skipped"))
		m, _ = pick(m, keyS)
		Expect(m.level).To(Equal(entriesLevel))
	})

	It("opens a read-only page on s: the title, then each entry's name with its reason two spaces under it", func() {
		m, _ := pick(skippedOwn(2, 100, 40), keyEnter, keyS)
		Expect(m.level).To(Equal(skippedLevel))
		out := drawn(m)
		Expect(out).To(ContainSubstring("acme — 2 skipped entries\nproject /srv/app\n"))
		Expect(out).To(ContainSubstring("skip-00\n  its source is an npm package, which lola does not take (0)\nskip-01\n  its source is an npm package, which lola does not take (1)\n"))
		Expect(out).NotTo(ContainSubstring("review"), "the entries are not drawn")

		m = skippedPreview(1)
		m, _ = pick(m, keyS)
		Expect(drawn(m)).To(ContainSubstring("kn — 1 skipped entry\n"), "a preview's title is its id, not the preview title")
		Expect(drawn(m)).NotTo(ContainSubstring("previewing"))
	})

	It("wraps a long reason under its name, two spaces in, and keeps the keys whole at 80 columns", func() {
		m, _ := pick(skippedOwn(2, 40, 40), keyEnter, keyS)
		lines := strings.Split(drawn(m), "\n")
		at := slices.Index(lines, "skip-00")
		Expect(at).To(BeNumerically(">=", 0))
		Expect(lines[at+1]).To(HavePrefix("  its source is an npm package,"))
		Expect(lines[at+2]).To(HavePrefix("  "), "the wrapped rest keeps the indent")
		for _, l := range lines {
			Expect(len([]rune(l))).To(BeNumerically("<=", 40), l)
		}
		m, _ = pick(skippedOwn(2, 80, 40), keyEnter, keyS)
		Expect(drawn(m)).To(ContainSubstring("↑↓ scroll · ←/esc back · q quit"))
	})

	It("leaves with Esc or Left for the entries page, the cursor and the scroll as they were", func() {
		for _, back := range []tea.KeyPressMsg{keyEsc, keyLeft} {
			m, _ := pick(skippedOwn(3, 100, 40), keyEnter, keyDown, keyS)
			m, _ = pick(m, back)
			Expect(m.level).To(Equal(entriesLevel))
			Expect(m.cursor).To(Equal(1))
			Expect(drawn(m)).To(ContainSubstring("fmt 0.3.0"))
		}
		m := skippedPreview(3)
		m, _ = pick(m, keyDown, keyS, keyEsc)
		Expect(m.level).To(Equal(entriesLevel))
		Expect(m.preview).To(BeTrue())
		Expect(m.cursor).To(Equal(1))

		// Scrolled entries page: a short terminal, the cursor on the last entry.
		m, _ = pick(skippedOwn(3, 100, 8), keyEnter, keyDown, keyS, keyDown, keyEsc)
		top := m.top
		m, _ = pick(skippedOwn(3, 100, 8), keyEnter, keyDown)
		Expect(top).To(Equal(m.top))
	})

	It("scrolls with the arrows, g and G, never past either end, and q and ctrl+c leave as elsewhere", func() {
		m, _ := pick(skippedOwn(16, 80, 12), keyEnter, keyS)
		Expect(drawn(m)).To(And(ContainSubstring("skip-00"), Not(ContainSubstring("skip-15"))))
		m, _ = pick(m, keyUp)
		Expect(m.top).To(BeZero())
		m, _ = pick(m, keyDown, keyDown)
		Expect(m.top).To(Equal(2))
		m, _ = pick(m, tea.KeyPressMsg{Code: tea.KeyEnd})
		Expect(drawn(m)).To(ContainSubstring("skip-15"))
		last := m.top
		m, _ = pick(m, keyDown)
		Expect(m.top).To(Equal(last), "no further than the last line")
		m, _ = pick(m, tea.KeyPressMsg{Code: tea.KeyHome})
		Expect(m.top).To(BeZero())

		m, cmd := pick(m, keyQ)
		Expect(m.answer.Action).To(Equal(PickerCancel))
		Expect(cmd()).To(Equal(tea.QuitMsg{}))
		m, cmd = pick(skippedOwn(2, 80, 12), keyEnter, keyS, keyCtrlC)
		Expect(m.aborted).To(BeTrue())
		Expect(cmd()).To(Equal(tea.QuitMsg{}))
	})

	It("takes no entry key on the skipped page", func() {
		m, _ := pick(skippedOwn(2, 100, 40), keyEnter, keyS)
		for _, k := range []tea.KeyPressMsg{keyI, keyD, keyO, keyM, keyEnter, keyRight, keyS} {
			m, _ = pick(m, k)
			Expect(m.level).To(Equal(skippedLevel))
			Expect(m.done).To(BeFalse())
		}
	})

	It("neutralises what a catalog wrote in a name or a reason", func() {
		st := MarketPickerStep{Entries: scopedEntries(), Skipped: map[string][]engine.MarketSkip{"acme": {{Name: "bad\x1b[31m\nname", Reason: "line\none\x1b]0;x\a"}}}}
		st.Own, st.Known = pickerRows()
		m, _ := pick(newMarketPicker(st, plainTheme(), 100, 40), keyEnter, keyS)
		Expect(m.View().Content).NotTo(ContainSubstring("\x1b]"))
		Expect(drawn(m)).NotTo(ContainSubstring("\a"))
	})

	Describe("in accessible mode", func() {
		It("prints the page on s, then asks the entries prompt again", func() {
			var out bytes.Buffer
			st := scopedStep(&previewer{}, nil)
			st.Skipped = map[string][]engine.MarketSkip{"acme": skips(2)}
			got, err := accessible(&out, "1", "s", "1", "i").MarketPicker(st)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(MarketPickerAnswer{Action: PickerInstall, Market: "acme", Entry: "review", Entries: []string{"review"}}))
			text := out.String()
			Expect(text).To(ContainSubstring("2 entries skipped · s shows why\n"))
			Expect(text).To(ContainSubstring("i<n,n…> install · d<n,n…> uninstall from project · s skipped · o<n> web · Enter alone goes back"))
			Expect(text).To(ContainSubstring("acme — 2 skipped entries\nskip-00\n  its source is an npm package, which lola does not take (0)\nskip-01\n  its source is an npm package, which lola does not take (1)\n"))
			Expect(strings.Count(text, "Choose an entry (number or key above): ")).To(Equal(2), "once before s and once after it")
		})

		It("wraps at spaces only, an address whole, and prints the preview's page too", func() {
			var out bytes.Buffer
			p := &previewer{pv: engine.KnownPreview{Skipped: []engine.MarketSkip{{Name: "far", Reason: "see https://example.com/a-very-long-address/that/does/not/fit for why"}}}}
			h := accessible(&out, "4", "s")
			h.width = 30
			_, err := h.MarketPicker(scopedStep(p, nil))
			Expect(err).To(MatchError(errInputEnded))
			text := out.String()
			Expect(text).To(ContainSubstring("kn — 1 skipped entry\nfar\n  see\n  https://example.com/a-very-long-address/that/does/not/fit\n  for why\n"))
			Expect(text).To(ContainSubstring("m enable · s skipped\no<n> web\nEnter alone goes back"), "keys whole at any width")
		})

		It("offers no s when nothing was skipped, and says what it takes", func() {
			var out bytes.Buffer
			_, err := accessible(&out, "1", "s").MarketPicker(scopedStep(&previewer{}, nil))
			Expect(err).To(MatchError(errInputEnded))
			Expect(out.String()).NotTo(ContainSubstring("s skipped"))
			Expect(out.String()).To(ContainSubstring("answer a number from 1 to 2\n"))
		})
	})
})
