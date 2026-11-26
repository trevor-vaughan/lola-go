package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/x/ansi"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// scopedEntries are acme's entries for the specs below: review (not installed, with a web page) and
// fmt (installed, no page).
func scopedEntries() map[string][]engine.PickerEntry {
	return map[string][]engine.PickerEntry{"acme": {
		{MarketOffer: engine.MarketOffer{Install: "@acme/review", Name: "review", Version: "1.2.0"}, WebURL: "https://example.com/acme/review"},
		{MarketOffer: engine.MarketOffer{Install: "@acme/fmt", Name: "fmt", Version: "0.3.0"}, Installed: true},
	}}
}

// scopedStep is the picker over pickerRows with scopedEntries, acting on a project's scope; p is the
// known row's preview and open the browser launcher.
func scopedStep(p *previewer, open func(string) error) MarketPickerStep {
	own, known := pickerRows()
	p.pv.ID, p.pv.Curated, p.pv.Commit = "kn", "2026-11-22", strings.Repeat("b", 40)
	if p.pv.Entries == nil && p.err == nil {
		p.pv.Entries = previewEntries()
	}
	return MarketPickerStep{Own: own, Known: known, Entries: scopedEntries(), Context: "project /srv/app", ScopeKind: "project",
		Preview: p.preview, OpenURL: open}
}

var _ = Describe("the marketplace picker names the scope it acts on", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("prints the context line under the list's title, drawn and in accessible mode", func() {
		m := newMarketPicker(scopedStep(&previewer{}, nil), plainTheme(), 80, 40)
		lines := strings.Split(drawn(m), "\n")
		Expect(lines[0]).To(Equal("Marketplaces"))
		Expect(lines[1]).To(Equal("project /srv/app"))
		var out bytes.Buffer
		_, err := accessible(&out).MarketPicker(scopedStep(&previewer{}, nil))
		Expect(err).To(MatchError(errInputEnded))
		Expect(out.String()).To(MatchRegexp(`(?m)^Marketplaces\nproject /srv/app\n`))
	})

	It("keeps the context line in one line on the list, whatever it holds", func() {
		st := scopedStep(&previewer{}, nil)
		st.Context = "project /srv/\x1b[31mapp\nx"
		var out bytes.Buffer
		_, _ = accessible(&out).MarketPicker(st)
		Expect(out.String()).NotTo(ContainSubstring("\x1b"))
		Expect(out.String()).To(MatchRegexp(`project /srv/\S*app x\n`))
	})

	It("names the scope kind on the uninstall key, not the install key, of the entries and detail pages, in at most two rows at 80 columns", func() {
		for _, kind := range []string{"project", "user"} {
			st := scopedStep(&previewer{}, nil)
			st.ScopeKind = kind
			m := newMarketPicker(st, plainTheme(), 80, 40)
			m, _ = pick(m, keyEnter)
			foot := func() string { return ansi.Strip(strings.Join(m.entriesFooter(), "\n")) }
			Expect(foot()).To(ContainSubstring("i install"))
			Expect(foot()).NotTo(ContainSubstring(kind))
			Expect(len(m.entriesFooter())).To(BeNumerically("<=", 2))
			for _, key := range []string{"o web", "⏎/→ open", "←/esc back", "/ filter", "q quit"} {
				Expect(foot()).To(ContainSubstring(key), kind)
			}
			Expect(foot()).NotTo(ContainSubstring("…"))
			m, _ = pick(m, keyDown)
			Expect(foot()).To(ContainSubstring("d uninstall from " + kind))
			Expect(foot()).NotTo(ContainSubstring("i install"))
			m, _ = pick(m, keyEnter)
			Expect(m.level).To(Equal(detailLevel))
			Expect(foot()).To(ContainSubstring("d uninstall from " + kind))
			Expect(len(m.entriesFooter())).To(BeNumerically("<=", 2))
		}
	})

	It("keeps every footer row within 80 columns for the longest wording", func() {
		m := newMarketPicker(scopedStep(&previewer{}, nil), plainTheme(), 80, 40)
		m, _ = pick(m, keyEnter)
		for _, r := range m.entriesFooter() {
			Expect(ansi.StringWidth(r)).To(BeNumerically("<=", 80))
		}
	})

	It("says nothing of the scope in a preview, where i and d do not apply", func() {
		p := &previewer{}
		m := newMarketPicker(scopedStep(p, nil), plainTheme(), 80, 40)
		m, cmd := toKnown(m, keyEnter)
		m, _ = pick(m, cmd())
		Expect(m.preview).To(BeTrue())
		Expect(ansi.Strip(strings.Join(m.entriesFooter(), "\n"))).To(And(ContainSubstring("m enable"), Not(ContainSubstring(" to project")), Not(ContainSubstring("install"))))
	})

	It("names the scope in the accessible entries and detail prompts", func() {
		var out bytes.Buffer
		_, err := accessible(&out, "1", "1").MarketPicker(scopedStep(&previewer{}, nil))
		Expect(err).To(MatchError(errInputEnded))
		Expect(out.String()).To(ContainSubstring("i<n,n…> install · d<n,n…> uninstall from project · o<n> web · Enter alone goes back\nChoose an entry (number or key above): "))
		Expect(out.String()).To(ContainSubstring("i install · o web · Enter alone goes back: "))
		out.Reset()
		st := scopedStep(&previewer{}, nil)
		st.ScopeKind = "user"
		_, err = accessible(&out, "1", "2").MarketPicker(st)
		Expect(err).To(MatchError(errInputEnded))
		Expect(out.String()).To(ContainSubstring("d uninstall from user · Enter alone goes back: "))
	})
})

var _ = Describe("the marketplace picker's accessible entries prompt", func() {
	BeforeEach(func() { testenv.Isolate() })

	run := func(open func(string) error, answers ...string) (MarketPickerAnswer, string, error) {
		var out bytes.Buffer
		got, err := accessible(&out, append([]string{"1"}, answers...)...).MarketPicker(scopedStep(&previewer{}, open))
		return got, out.String(), err
	}

	It("installs entry N with i<N>, uninstalls with d<N>, as the TUI answers from the entries page", func() {
		got, _, err := run(nil, "i1")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(MarketPickerAnswer{Action: PickerInstall, Market: "acme", Entry: "review", Entries: []string{"review"}, FromEntries: true}))
		got, _, err = run(nil, "d2")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(MarketPickerAnswer{Action: PickerUninstall, Market: "acme", Entry: "fmt", Entries: []string{"fmt"}, FromEntries: true}))
		got, _, err = run(nil, " I 1 ")
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Action).To(Equal(PickerInstall))
	})

	It("says why a letter does not apply to its entry, and asks again", func() {
		got, out, err := run(nil, "i2", "d1", "o2", "d2")
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Action).To(Equal(PickerUninstall))
		Expect(out).To(And(ContainSubstring("already installed; d uninstalls\n"), ContainSubstring("not installed; i installs\n"),
			ContainSubstring("no web page for this entry\n")))
		Expect(strings.Count(out, "Choose an entry")).To(Equal(4))
	})

	It("opens an entry's page with o<N> through the step's opener, printing the address", func() {
		var opened []string
		_, out, err := run(func(u string) error { opened = append(opened, u); return nil }, "o1")
		Expect(err).To(MatchError(errInputEnded))
		Expect(opened).To(Equal([]string{"https://example.com/acme/review"}))
		Expect(out).To(ContainSubstring("opened https://example.com/acme/review\n"))
	})

	It("prints the whole address when the browser cannot start, and refuses an address that is not plain http(s)", func() {
		_, out, _ := run(func(string) error { return errors.New("no display\nat all") }, "o1")
		Expect(out).To(ContainSubstring("could not open a browser (no display at all); the address is https://example.com/acme/review\n"))
		own, known := pickerRows()
		st := MarketPickerStep{Own: own, Known: known, Entries: map[string][]engine.PickerEntry{"acme": {
			{MarketOffer: engine.MarketOffer{Name: "bad"}, WebURL: "javascript:alert(1)"}}}, OpenURL: func(string) error {
			Fail("opened a refused address")
			return nil
		}}
		var buf bytes.Buffer
		_, err := accessible(&buf, "1", "o1").MarketPicker(st)
		Expect(err).To(MatchError(errInputEnded))
		Expect(buf.String()).To(ContainSubstring("refusing to open this entry's address: it is not a plain http(s) address\n"))
	})

	It("refuses a letter with no number, a number out of range, a bad letter, and trailing text, then asks again", func() {
		for _, bad := range []string{"i", "d", "o", "i0", "i3", "d9", "x1", "ii1", "i1x", "o1,2", "-1", "m"} {
			got, out, err := run(nil, bad, "i1")
			Expect(err).NotTo(HaveOccurred(), bad)
			Expect(got.Action).To(Equal(PickerInstall), bad)
			Expect(strings.Count(out, "Choose an entry")).To(Equal(2), bad)
			Expect(out).To(MatchRegexp(`answer a number from 1 to 2|answer numbers from 1 to 2, comma separated|takes an entry's number`), bad)
		}
		_, out, _ := run(nil, "i", "i1")
		Expect(out).To(ContainSubstring("i takes an entry's number: i1\n"))
	})

	It("keeps a bare number opening the detail, and Enter alone going back", func() {
		got, _, err := run(nil, "2", "d")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(MarketPickerAnswer{Action: PickerUninstall, Market: "acme", Entry: "fmt", Entries: []string{"fmt"}}))
		_, out, _ := run(nil, "", "")
		Expect(strings.Count(out, "Choose marketplaces")).To(Equal(2))
	})

	It("ends at end of input and at Ctrl+C", func() {
		_, out, err := run(nil)
		Expect(err).To(MatchError(errInputEnded))
		Expect(out).To(HaveSuffix("\n"))
		own, known := pickerRows()
		var buf bytes.Buffer
		stop := make(chan struct{})
		DeferCleanup(func() { close(stop) })
		h := &huhForms{out: &buf, tty: true, accessible: true}
		h.in = &eofReader{r: &interruptingReader{h: h, lines: []string{"1"}, stop: stop}}
		_, err = h.MarketPicker(MarketPickerStep{Own: own, Known: known, Entries: scopedEntries()})
		Expect(err).To(MatchError(ErrAborted))
		Expect(buf.String()).To(ContainSubstring("Choose an entry"))
	})

	It("takes only digits for an entry's number", func() {
		for _, bad := range []string{"+1", "i+1", "d +1", "1.0", "i١"} {
			got, out, err := run(nil, bad, "i1")
			Expect(err).NotTo(HaveOccurred(), bad)
			Expect(got.Action).To(Equal(PickerInstall), bad)
			Expect(out).To(MatchRegexp(`answer (a number|numbers) from 1 to 2(, comma separated)?\n`), bad)
		}
	})

	It("offers i but not d, and refuses d, when the picker has no scope to read", func() {
		st := scopedStep(&previewer{}, nil)
		st.ScopeKind = ""
		var out bytes.Buffer
		_, err := accessible(&out, "1", "d1", "2", "d", "x").MarketPicker(st)
		Expect(err).To(MatchError(errInputEnded))
		Expect(out.String()).NotTo(MatchRegexp(`install (to|from)|uninstall|d<n>`))
		Expect(out.String()).To(ContainSubstring("i<n,n…> install · o<n> web · Enter alone goes back\nChoose an entry"))
		Expect(out.String()).To(ContainSubstring("no scope to read; d is unavailable\nEnter alone goes back: "))
		Expect(out.String()).To(ContainSubstring("answer Enter to go back\n"))
		m := newMarketPicker(st, plainTheme(), 80, 40)
		m, _ = pick(m, keyEnter)
		Expect(ansi.Strip(strings.Join(m.entriesFooter(), "\n"))).To(ContainSubstring("i install"))
		m, _ = pick(m, keyDown)
		m, _ = pick(m, keyD)
		Expect(m.done).To(BeFalse())
		Expect(drawn(m)).To(ContainSubstring("no scope to read; d is unavailable"))
		m, _ = pick(m, keyUp)
		m, _ = pick(m, keyI)
		Expect(m.answer.Action).To(Equal(PickerInstall))
	})

	It("prints hostile text on one line", func() {
		own, known := pickerRows()
		st := MarketPickerStep{Own: own, Known: known, ScopeKind: "project", Entries: map[string][]engine.PickerEntry{"acme": {
			{MarketOffer: engine.MarketOffer{Name: "evil\x1b[31m\nname"}, WebURL: "https://example.com/x\x1b[2J"}}},
			OpenURL: func(string) error { return nil }}
		var out bytes.Buffer
		_, err := accessible(&out, "1", "d1", "o1").MarketPicker(st)
		Expect(err).To(MatchError(errInputEnded))
		Expect(out.String()).NotTo(ContainSubstring("\x1b"))
		Expect(out.String()).To(ContainSubstring("not installed; i installs"))
		Expect(out.String()).To(ContainSubstring("refusing to open this entry's address: it is not a plain http(s) address\n"))
	})
})

var _ = Describe("the marketplace picker's accessible detail prompt", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("offers o next to the letter that applies, and opens the page as the entries prompt does", func() {
		var opened []string
		var out bytes.Buffer
		got, err := accessible(&out, "1", "1", "o", "d", "i").MarketPicker(scopedStep(&previewer{}, func(u string) error { opened = append(opened, u); return nil }))
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(MarketPickerAnswer{Action: PickerInstall, Market: "acme", Entry: "review", Entries: []string{"review"}}))
		Expect(opened).To(Equal([]string{"https://example.com/acme/review"}))
		Expect(out.String()).To(And(ContainSubstring("opened https://example.com/acme/review\n"), ContainSubstring("not installed; i installs\n")))
		Expect(out.String()).To(ContainSubstring("i install · o web · Enter alone goes back: "))
	})

	It("lists no o for an entry without a page, and says so when it is typed", func() {
		var out bytes.Buffer
		_, err := accessible(&out, "1", "2", "o", "?").MarketPicker(scopedStep(&previewer{}, func(string) error { return nil }))
		Expect(err).To(MatchError(errInputEnded))
		Expect(out.String()).To(ContainSubstring("d uninstall from project · Enter alone goes back: "))
		Expect(out.String()).To(ContainSubstring("no web page for this entry\n"))
		Expect(out.String()).To(ContainSubstring("answer d to uninstall, or Enter to go back\n"))
	})

	It("prints the failure with the address", func() {
		var out bytes.Buffer
		_, _ = accessible(&out, "1", "1", "o").MarketPicker(scopedStep(&previewer{}, func(string) error { return errors.New("boom") }))
		Expect(out.String()).To(ContainSubstring("could not open a browser (boom); the address is https://example.com/acme/review\n"))
	})
})

var _ = Describe("the marketplace picker's accessible preview of a known marketplace", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("prints the status line before the fetch starts, then the preview under its header", func() {
		p := &previewer{}
		p.pv.Skipped = skips(2)
		var out bytes.Buffer
		step := scopedStep(p, func(string) error { return nil })
		var before string
		fetch := step.Preview
		step.Preview = func(ctx context.Context, id string) (engine.KnownPreview, error) {
			before = out.String()
			return fetch(ctx, id)
		}
		_, err := accessible(&out, "4").MarketPicker(step)
		Expect(err).To(MatchError(errInputEnded))
		Expect(before).To(HaveSuffix("fetching kn at bbbbbbb from https://example.com — nothing is added or installed…\n"))
		Expect(out.String()).To(ContainSubstring("previewing kn — not added; pinned to bbbbbbb, curated 2026-11-22\nproject /srv/app\n"))
		Expect(out.String()).To(ContainSubstring("1. lint 1.0.0"))
		Expect(out.String()).To(ContainSubstring("2 entries skipped · s shows why\n"))
		Expect(out.String()).To(ContainSubstring("m enable · s skipped · o<n> web · Enter alone goes back\nChoose an entry (number or key above): "))
	})

	It("says i and d do not apply, and enables with m at the entries prompt and at the detail prompt", func() {
		for _, answers := range [][]string{{"4", "i1", "d2", "m"}, {"4", "1", "i", "d", "m"}} {
			p := &previewer{}
			var out bytes.Buffer
			got, err := accessible(&out, answers...).MarketPicker(scopedStep(p, nil))
			Expect(err).NotTo(HaveOccurred(), strings.Join(answers, ","))
			Expect(got).To(Equal(MarketPickerAnswer{Action: PickerEnable, Known: []string{"kn"}}), strings.Join(answers, ","))
			Expect(strings.Count(out.String(), "not added yet; m enables kn\n")).To(Equal(2), strings.Join(answers, ","))
		}
	})

	It("lists m and o on the preview's detail prompt", func() {
		var out bytes.Buffer
		_, err := accessible(&out, "4", "1").MarketPicker(scopedStep(&previewer{}, nil))
		Expect(err).To(MatchError(errInputEnded))
		Expect(out.String()).To(ContainSubstring("m enable · o web · Enter alone goes back: "))
	})

	It("reuses a kept preview, and going back leaves the preview", func() {
		p := &previewer{}
		var out bytes.Buffer
		_, err := accessible(&out, "4", "", "4").MarketPicker(scopedStep(p, nil))
		Expect(err).To(MatchError(errInputEnded))
		Expect(p.calls).To(Equal(1))
		Expect(strings.Count(out.String(), "fetching kn")).To(Equal(1))
		Expect(strings.Count(out.String(), "previewing kn")).To(Equal(2))
		Expect(strings.Count(out.String(), "Choose marketplaces")).To(Equal(2))
	})

	It("prints why a fetch failed with its first hint and asks the market prompt again", func() {
		p := &previewer{err: errs.WithHints(errors.New("git said\nno"), "check your network")}
		var out bytes.Buffer
		got, err := accessible(&out, "4", "4", "").MarketPicker(scopedStep(p, nil))
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(MarketPickerAnswer{Action: PickerCancel}))
		Expect(out.String()).To(ContainSubstring("could not fetch kn: git said no → check your network\n"))
		Expect(strings.Count(out.String(), "Choose marketplaces")).To(Equal(3))
		Expect(p.calls).To(Equal(2), "a failure is not kept")
	})

	It("says a catalog with no entry offers none, and stays on the list", func() {
		p := &previewer{pv: engine.KnownPreview{Entries: []engine.PickerEntry{}}}
		var out bytes.Buffer
		_, err := accessible(&out, "4").MarketPicker(scopedStep(p, nil))
		Expect(err).To(MatchError(errInputEnded))
		Expect(out.String()).To(ContainSubstring("kn at bbbbbbb offers no entries; lola market enable kn enables it anyway\n"))
		Expect(strings.Count(out.String(), "Choose marketplaces")).To(Equal(2))
	})

	It("answers several numbers with their rows, and refuses d on a known row, fetching nothing", func() {
		p := &previewer{}
		var out bytes.Buffer
		got, err := accessible(&out, "d4", "1,4").MarketPicker(scopedStep(p, nil))
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(MarketPickerAnswer{Action: PickerEnter, Own: []string{"acme"}, Known: []string{"kn"}}))
		Expect(out.String()).To(ContainSubstring("d removes your marketplaces: answer numbers from 1 to 3, comma separated\n"))
		Expect(p.calls).To(BeZero())
	})

	It("ends at end of input at each prompt of a preview", func() {
		for _, answers := range [][]string{{"4"}, {"4", "1"}} {
			var out bytes.Buffer
			_, err := accessible(&out, answers...).MarketPicker(scopedStep(&previewer{}, nil))
			Expect(err).To(MatchError(errInputEnded), strings.Join(answers, ","))
			Expect(out.String()).To(HaveSuffix("\n"))
		}
	})

	It("cancels the fetch on Ctrl+C and aborts, without waiting for it", func() {
		var out bytes.Buffer
		h := accessible(&out, "4")
		cancelled := make(chan error, 1)
		step := scopedStep(&previewer{}, nil)
		step.Preview = func(ctx context.Context, id string) (engine.KnownPreview, error) {
			h.in.sig <- os.Interrupt
			<-ctx.Done()
			cancelled <- ctx.Err()
			return engine.KnownPreview{}, ctx.Err()
		}
		_, err := h.MarketPicker(step)
		Expect(err).To(MatchError(ErrAborted))
		Eventually(cancelled).Should(Receive(MatchError(context.Canceled)))
		Expect(out.String()).To(ContainSubstring("fetching kn at bbbbbbb"))
		Expect(out.String()).NotTo(ContainSubstring("previewing"))
	})

	It("prints a hostile catalog's text on one line", func() {
		p := &previewer{}
		p.pv.Entries = []engine.PickerEntry{{MarketOffer: engine.MarketOffer{Name: "ev\x1b[31mil\nname", Description: "a\nb"}}}
		p.pv.Warnings = []string{"warn\x1b[2J\ning"}
		var out bytes.Buffer
		_, _ = accessible(&out, "4").MarketPicker(scopedStep(p, nil))
		Expect(out.String()).NotTo(ContainSubstring("\x1b"))
		Expect(out.String()).To(MatchRegexp(`warn\S* ing\n`))
	})
})

var _ = Describe("the marketplace picker's accessible prompts at a narrow width", func() {
	BeforeEach(func() { testenv.Isolate() })

	// printed is what the accessible picker prints for answers at width columns, ending at end of input.
	printed := func(width int, st MarketPickerStep, answers ...string) []string {
		var out bytes.Buffer
		h := accessible(&out, answers...)
		h.width = width
		_, err := h.MarketPicker(st)
		Expect(err).To(MatchError(errInputEnded))
		return strings.Split(out.String(), "\n")
	}

	It("never splits a key across lines or prints a prompt line wider than the terminal, at 80 and 40 columns", func() {
		keys := []string{"Choose marketplaces", "i<n,n…> install", "d<n,n…> uninstall from project", "o<n> web", "Enter alone goes back",
			"i install", "o web"}
		for _, width := range []int{80, 40} {
			lines := printed(width, scopedStep(&previewer{}, nil), "1", "1")
			joined := strings.Join(lines, "\n")
			for _, k := range keys {
				Expect(joined).To(ContainSubstring(k), "%s at %d", k, width)
			}
			for _, l := range lines {
				if i := strings.Index(l, "): "); strings.HasPrefix(l, "Choose") && i >= 0 {
					l = l[:i+2] // the next page's first line follows the prompt: no answer was typed
				} else if i := strings.Index(l, "above: "); strings.HasPrefix(l, "Choose") && i >= 0 {
					l = l[:i+7]
				}
				if strings.Contains(l, " · ") || strings.HasPrefix(l, "Choose") {
					Expect(ansi.StringWidth(l)).To(BeNumerically("<", width), "%q at %d", l, width)
				}
			}
		}
	})

	It("keeps the stable prompt when it fits the width, and moves the keys above a short one when it does not", func() {
		wide := strings.Join(printed(0, scopedStep(&previewer{}, nil), "1", "1"), "\n")
		Expect(wide).To(ContainSubstring("Choose marketplaces (numbers, comma separated; d1 removes; o1 for the web page): "))
		Expect(wide).To(ContainSubstring("i install · o web · Enter alone goes back: "))
		narrow := strings.Join(printed(40, scopedStep(&previewer{}, nil), "1", "1"), "\n")
		Expect(narrow).NotTo(ContainSubstring("numbers, comma separated; d1 removes; o1 for the web page): "))
		Expect(narrow).To(ContainSubstring("Choose a key above: "))
	})

	It("wraps the status line and notes at the width, between words", func() {
		p := &previewer{err: errors.New("it failed for a reason long enough to wrap")}
		lines := printed(40, scopedStep(p, nil), "4")
		for _, l := range lines {
			if strings.HasPrefix(l, "fetching") || strings.Contains(l, "could not fetch") || strings.Contains(l, "wrap") {
				Expect(ansi.StringWidth(l)).To(BeNumerically("<=", 40), l)
			}
		}
		Expect(strings.Join(lines, "\n")).To(ContainSubstring("installed…"))
	})
})

var _ = Describe("the marketplace picker's status line for a source with no host", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("omits from <host> when the source names none, in the footer and in accessible mode", func() {
		st := scopedStep(&previewer{}, nil)
		st.Known[0].Source = "file:///srv/kn"
		m := newMarketPicker(st, plainTheme(), 100, 40)
		m, _ = toKnown(m, keyEnter)
		Expect(drawn(m)).To(ContainSubstring("fetching kn at bbbbbbb — nothing is added or installed…"))
		Expect(drawn(m)).NotTo(ContainSubstring("from "))
		var out bytes.Buffer
		_, err := accessible(&out, "4").MarketPicker(st)
		Expect(err).To(MatchError(errInputEnded))
		Expect(out.String()).To(ContainSubstring("fetching kn at bbbbbbb — nothing is added or installed…\n"))
		Expect(out.String()).NotTo(ContainSubstring("from  "))
	})
})

// interruptingReader serves lines, then presses Ctrl+C on h's input and blocks until stop closes.
type interruptingReader struct {
	h     *huhForms
	lines []string
	stop  chan struct{}
}

func (r *interruptingReader) Read(p []byte) (int, error) {
	if len(r.lines) > 0 {
		n := copy(p, r.lines[0]+"\n")
		r.lines = r.lines[1:]
		return n, nil
	}
	r.h.in.sig <- os.Interrupt
	<-r.stop
	return 0, io.EOF
}

var _ = Describe("the marketplace picker's accessible wrapping", func() {
	BeforeEach(func() { testenv.Isolate() })

	const longURL = "https://example.com/a-very-long-hyphenated-path/with-more-hyphens-in-it/and-more"

	// printed is what the picker prints for answers at width columns.
	printed := func(width int, st MarketPickerStep, answers ...string) string {
		var out bytes.Buffer
		h := accessible(&out, answers...)
		h.width = width
		_, err := h.MarketPicker(st)
		Expect(err).To(MatchError(errInputEnded))
		return out.String()
	}

	It("never splits an address or a flag in a note, at 40 columns", func() {
		st := scopedStep(&previewer{}, func(string) error { return errors.New("no browser") })
		st.Entries["acme"][0].WebURL = longURL
		out := printed(40, st, "1", "o1")
		Expect(out).To(ContainSubstring(longURL + "\n"))
		p := &previewer{err: errs.WithHints(errors.New("git said no"), "lola market --enable-this-very-long-flag-name now")}
		out = printed(40, scopedStep(p, nil), "4")
		Expect(out).To(ContainSubstring("--enable-this-very-long-flag-name"))
		Expect(out).To(ContainSubstring("could not fetch kn: git said no"))
	})

	It("wraps the title, the preview header, the context line, entry heads and details at 40 columns, between spaces", func() {
		p := &previewer{}
		p.pv.Entries = []engine.PickerEntry{{MarketOffer: engine.MarketOffer{Name: "formatter", Version: "1.0.0",
			Description: "formats every source file the way the team agreed last spring"}, Installed: true}}
		st := scopedStep(p, nil)
		st.Context = "project /srv/app: base profile with many words in it"
		out := printed(40, st, "4")
		for _, prompt := range []string{"Choose marketplaces: ", "Choose an entry (number or key above): "} {
			out = strings.ReplaceAll(out, prompt, prompt+"\n")
		}
		for l := range strings.SplitSeq(out, "\n") {
			if strings.Contains(l, " ") && !strings.HasPrefix(strings.TrimSpace(l), "http") {
				Expect(ansi.StringWidth(l)).To(BeNumerically("<", 40), "%q", l)
			}
		}
		Expect(strings.Join(strings.Fields(out), " ")).To(ContainSubstring("previewing kn — not added; pinned to bbbbbbb, curated 2026-11-22"))
		Expect(out).To(MatchRegexp(`(?m)^      the team agreed last spring`), "details keep their indent on every line")
	})

	It("does not wrap when the width is unknown", func() {
		out := printed(0, scopedStep(&previewer{}, nil), "4")
		Expect(out).To(ContainSubstring("previewing kn — not added; pinned to bbbbbbb, curated 2026-11-22\n"))
	})

	It("keeps a prompt of exactly the terminal's width off the wrap edge", func() {
		var out bytes.Buffer
		h := &huhForms{out: &out, width: 10}
		Expect(h.fitPrompt("12345678: ", "a · b", "short: ")).To(Equal("short: "))
		Expect(h.fitPrompt("1234567: ", "a · b", "short: ")).To(Equal("1234567: "))
		Expect(out.String()).To(Equal("a · b\n"))
	})

	It("ends a Ctrl+C during a fetch with no blank line after the status", func() {
		var out bytes.Buffer
		h := accessible(&out, "4")
		step := scopedStep(&previewer{}, nil)
		step.Preview = func(ctx context.Context, id string) (engine.KnownPreview, error) {
			h.in.sig <- os.Interrupt
			<-ctx.Done()
			return engine.KnownPreview{}, ctx.Err()
		}
		_, err := h.MarketPicker(step)
		Expect(err).To(MatchError(ErrAborted))
		Expect(out.String()).To(HaveSuffix("nothing is added or installed…\n"))
		Expect(out.String()).NotTo(HaveSuffix("\n\n"))
	})
})

var _ = Describe("the marketplace picker's footer notes", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("keeps an address and a flag whole when a note wraps, at 40 columns", func() {
		const url = "https://example.com/a-very-long-hyphenated-path/with-more-hyphens-in-it"
		m := newMarketPicker(scopedStep(&previewer{}, nil), plainTheme(), 40, 40)
		m, _ = pick(m, keyEnter)
		m.note = "could not open a browser (no display); the address is " + url + " or lola market --enable-the-long-flag"
		foot := ansi.Strip(strings.Join(m.entriesFooter(), "\n"))
		Expect(foot).To(ContainSubstring(url + "\n"))
		Expect(foot).To(ContainSubstring("--enable-the-long-flag"))
	})
})
