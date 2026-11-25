package cli

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// pickerRows are the picker's rows for the specs: two git marketplaces of yours with fetched
// catalogs, a folder one, and a known one you have not enabled.
func pickerRows() (own, known []engine.PickerRow) {
	git := func(name string) engine.PickerRow {
		return engine.PickerRow{ID: name, MarketRow: engine.MarketRow{Name: name, Source: "https://example.com/" + name + ".git",
			Commit: strings.Repeat("a", 40), FetchedAt: "2026-11-23T00:00:00Z", Entries: 3}}
	}
	folder := engine.PickerRow{ID: "local", MarketRow: engine.MarketRow{Name: "local", Source: "/srv/local", FetchedAt: "2026-11-23T00:00:00Z", Entries: 1}}
	kn := engine.PickerRow{ID: "kn", Known: true, Reason: "curated skills", Pin: strings.Repeat("b", 40),
		MarketRow: engine.MarketRow{Source: "https://example.com/kn.git"}}
	return []engine.PickerRow{git("acme"), git("beta"), folder}, []engine.PickerRow{kn}
}

// checker is a MarketPickerStep.Check that counts its calls and hands out, per call, a channel the
// spec fills; ctxs are the contexts it was called with and ids the IDs of the rows each was given.
type checker struct {
	calls int
	chans []chan engine.Check
	ctxs  []context.Context
	ids   [][]string
}

func (c *checker) check(ctx context.Context, rows []engine.PickerRow) <-chan engine.Check {
	c.calls++
	ch := make(chan engine.Check, 8)
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	c.chans, c.ctxs, c.ids = append(c.chans, ch), append(c.ctxs, ctx), append(c.ids, ids)
	return ch
}

// ended sends results to c's last run, in order, and closes it.
func (c *checker) ended(results ...engine.Check) {
	ch := c.chans[len(c.chans)-1]
	for _, r := range results {
		ch <- r
	}
	close(ch)
}

// newPicker is the picker over pickerRows on a terminal width columns wide and 40 rows high, in
// the theme without color, its checks c's.
func newPicker(c *checker, width int, preselect ...string) marketPicker {
	own, known := pickerRows()
	return newMarketPicker(MarketPickerStep{Own: own, Known: known, Check: c.check, PreselectOwn: preselect}, plainTheme(), width, 40)
}

// pick sends m each message and returns it with the command the last one returned.
func pick(m marketPicker, msgs ...tea.Msg) (marketPicker, tea.Cmd) {
	var cmd tea.Cmd
	for _, msg := range msgs {
		var next tea.Model
		next, cmd = m.Update(msg)
		m = next.(marketPicker)
	}
	return m, cmd
}

// presses is the key presses of text, one per character, as messages.
func presses(text string) []tea.Msg {
	var ms []tea.Msg
	for _, k := range typed(text) {
		ms = append(ms, k)
	}
	return ms
}

// drawn is what m draws, without styling.
func drawn(m marketPicker) string { return ansi.Strip(m.View().Content) }

// feed runs cmd, reading one result from the check channel, n times, sending each message to m.
func feed(m marketPicker, cmd tea.Cmd, n int) (marketPicker, tea.Cmd) {
	for range n {
		Expect(cmd).NotTo(BeNil())
		m, cmd = pick(m, cmd())
	}
	return m, cmd
}

var (
	keyCtrlA = tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl}
	keyU     = tea.KeyPressMsg{Code: 'u', Text: "u"}
	keyShU   = tea.KeyPressMsg{Code: 'u', Text: "U", Mod: tea.ModShift}
)

// offered is the picker over pickerRows with every row ticked and u pressed, its check run ended with
// results (Index into the rows checked: acme, beta, kn), and every result read.
func offered(c *checker, results ...engine.Check) marketPicker {
	m, cmd := pick(newPicker(c, 100), keyCtrlA, keyU)
	Expect(c.ids).To(Equal([][]string{{"acme", "beta", "kn"}}))
	c.ended(results...)
	m, cmd = feed(m, cmd, len(results)+1)
	Expect(cmd).To(BeNil())
	return m
}

var _ = Describe("the marketplace picker", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("makes no check on open and shows no status", func() {
		c := &checker{}
		m := newPicker(c, 100)
		Expect(m.Init()).To(BeNil())
		out := drawn(m)
		Expect(c.calls).To(Equal(0))
		Expect(out).To(ContainSubstring("acme"))
		Expect(out).To(ContainSubstring("u check+update"))
		for _, s := range []string{engine.StatusChecking, engine.StatusCurrent, engine.StatusUpdate, engine.StatusUnreachable, engine.StatusNotChecked} {
			Expect(out).NotTo(ContainSubstring(s))
		}
	})

	It("draws each row on two lines, the source dim and indented, a known row's reason after it", func() {
		out := drawn(newPicker(&checker{}, 100))
		Expect(out).To(MatchRegexp(`> \[ \] acme .*3 entries\n {6}https://example\.com/acme\.git\n`))
		Expect(out).To(MatchRegexp(`\[ \] kn .*known\n {6}https://example\.com/kn\.git — curated skills\n`))
		Expect(out).To(ContainSubstring("Add a source…"))
	})

	It("ticks with x, all and none with ctrl+a, and answers the ticks in row order on Enter", func() {
		m, _ := pick(newPicker(&checker{}, 100), append([]tea.Msg{keyDown, keyDown, keyDown}, presses("x")...)...)
		m, _ = pick(m, keyUp, keyUp, keyUp)
		m, _ = pick(m, presses("x")...)
		Expect(drawn(m)).To(ContainSubstring("> [x] acme"))
		m, cmd := pick(m, keyEnter)
		Expect(cmd).NotTo(BeNil())
		Expect(cmd()).To(Equal(tea.QuitMsg{}))
		Expect(m.answer).To(Equal(MarketPickerAnswer{Action: PickerEnter, Own: []string{"acme"}, Known: []string{"kn"}}))

		m, _ = pick(newPicker(&checker{}, 100), keyCtrlA)
		Expect(strings.Count(drawn(m), "[x]")).To(Equal(4))
		m, _ = pick(m, keyCtrlA)
		Expect(drawn(m)).NotTo(ContainSubstring("[x]"))
	})

	It("shortens a commit ref written in upper case, as it does one in lower case", func() {
		own, known := pickerRows()
		own[0].Ref, own[0].Known, own[0].Enabled, own[0].AheadOfPin = "ABCDEF"+strings.Repeat("0", 34), true, true, true
		Expect(drawn(newMarketPicker(MarketPickerStep{Own: own, Known: known}, plainTheme(), 100, 40))).To(
			ContainSubstring("acme  abcdef0 · 3 entries · known · ahead of pin\n"))
	})

	It("ticks the rows Preselect names on open", func() {
		m := newPicker(&checker{}, 100, "beta")
		Expect(drawn(m)).To(ContainSubstring("[x] beta"))
		Expect(strings.Count(drawn(m), "[x]")).To(Equal(1))
	})

	Context("when one of yours and a known one share an ID", func() {
		twins := func() MarketPickerStep {
			own, known := pickerRows()
			known[0].ID = "acme"
			return MarketPickerStep{Own: own, Known: known}
		}

		It("answers only the group of the row ticked", func() {
			m, _ := pick(newMarketPicker(twins(), plainTheme(), 100, 40), append([]tea.Msg{keyDown, keyDown, keyDown}, presses("x")...)...)
			m, _ = pick(m, keyEnter)
			Expect(m.answer).To(Equal(MarketPickerAnswer{Action: PickerEnter, Known: []string{"acme"}}))

			c := &checker{}
			st := twins()
			st.Check = c.check
			m, cmd := pick(newMarketPicker(st, plainTheme(), 100, 40), keyU)
			Expect(c.ids).To(Equal([][]string{{"acme"}}))
			c.ended(engine.Check{Index: 0, Status: engine.StatusUpdate})
			m, _ = feed(m, cmd, 2)
			m, _ = pick(m, keyEnter)
			Expect(m.answer).To(Equal(MarketPickerAnswer{Action: PickerUpdate, Own: []string{"acme"}}))
		})

		It("answers both when both are ticked", func() {
			m, _ := pick(newMarketPicker(twins(), plainTheme(), 100, 40), append(presses("x"), append([]tea.Msg{keyDown, keyDown, keyDown}, presses("x")...)...)...)
			m, _ = pick(m, keyEnter)
			Expect(m.answer).To(Equal(MarketPickerAnswer{Action: PickerEnter, Own: []string{"acme"}, Known: []string{"acme"}}))
		})

		It("preselects only your marketplace PreselectOwn names, not the known one", func() {
			st := twins()
			st.PreselectOwn = []string{"acme"}
			Expect(strings.Count(drawn(newMarketPicker(st, plainTheme(), 100, 40)), "[x]")).To(Equal(1))
			Expect(drawn(newMarketPicker(st, plainTheme(), 100, 40))).To(ContainSubstring("[x] acme  3 entries"))
		})

		It("answers each group's numbers in accessible mode", func() {
			var out bytes.Buffer
			got, err := accessible(&out, "1,4").MarketPicker(twins())
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(MarketPickerAnswer{Action: PickerEnter, Own: []string{"acme"}, Known: []string{"acme"}}))
		})
	})

	It("drops the update offer and checks again on u or U, as the unreachable row's hint says", func() {
		for _, key := range []tea.KeyPressMsg{keyU, keyShU} {
			c := &checker{}
			m := offered(c, engine.Check{Index: 0, Status: engine.StatusUnreachable, Reason: "no route"},
				engine.Check{Index: 1, Status: engine.StatusUpdate}, engine.Check{Index: 2, Status: engine.StatusCurrent})
			Expect(m.offer).NotTo(BeNil())
			Expect(drawn(m)).To(ContainSubstring("→ u retries"))
			m, cmd := pick(m, key)
			Expect(cmd).NotTo(BeNil(), key.Text)
			Expect(m.offer).To(BeNil(), key.Text)
			Expect(c.calls).To(Equal(2), key.Text)
			Expect(m.done).To(BeFalse(), key.Text)
		}
	})

	It("hints u checks again, not --remove, for an unreachable known marketplace you have not enabled", func() {
		m := offered(&checker{}, engine.Check{Index: 0, Status: engine.StatusCurrent},
			engine.Check{Index: 1, Status: engine.StatusCurrent}, engine.Check{Index: 2, Status: engine.StatusUnreachable, Reason: "no route"})
		out := drawn(m)
		Expect(out).To(MatchRegexp(`kn .*unreachable`))
		Expect(out).To(ContainSubstring("→ u checks again"))
		Expect(out).NotTo(ContainSubstring("u retries"))
		Expect(out).NotTo(ContainSubstring("--remove"))
	})

	It("narrows the list with / and takes a, u, r and q typed there as filter text", func() {
		c := &checker{}
		m, cmd := pick(newPicker(c, 100), append([]tea.Msg{keySlash}, presses("bauqr")...)...)
		Expect(cmd).To(BeNil())
		Expect(m.done).To(BeFalse())
		Expect(c.calls).To(Equal(0))
		Expect(drawn(m)).To(ContainSubstring("/bauqr"))
		m, _ = pick(m, keyBksp, keyBksp, keyBksp, keyBksp)
		Expect(drawn(m)).To(ContainSubstring("/b"))
		out := drawn(m)
		Expect(out).To(ContainSubstring("beta"))
		Expect(out).NotTo(ContainSubstring("acme"))
		Expect(out).To(ContainSubstring("Add a source…"))

		m, _ = pick(m, keyEnter) // keeps the filter, stops typing
		m, _ = pick(m, presses("x")...)
		Expect(drawn(m)).To(ContainSubstring("[x] beta"))
		m, _ = pick(m, keyEsc) // clears the filter, not the picker
		Expect(m.done).To(BeFalse())
		Expect(drawn(m)).To(ContainSubstring("acme"))
		Expect(drawn(m)).To(ContainSubstring("[x] beta"))
	})

	It("checks the cursor row on u when nothing is ticked, and the ticked checkable rows when some are", func() {
		c := &checker{}
		m, cmd := pick(newPicker(c, 100), keyDown, keyU)
		Expect(cmd).NotTo(BeNil())
		Expect(m.done).To(BeFalse())
		Expect(c.ids).To(Equal([][]string{{"beta"}}))
		Expect(drawn(m)).To(MatchRegexp(`beta .*checking…`))
		Expect(strings.Count(drawn(m), engine.StatusChecking)).To(Equal(1))

		c = &checker{}
		m, _ = pick(newPicker(c, 100), keyU) // acme, the cursor's
		m, _ = pick(m, keyDown, keyDown)
		m, _ = pick(m, presses("x")...)
		m, _ = pick(m, keyDown)
		m, _ = pick(m, presses("x")...)
		m, _ = pick(m, keyG)
		m, _ = pick(m, presses("x")...)
		m, _ = pick(m, keyU)
		Expect(c.ids).To(Equal([][]string{{"acme"}, {"acme", "kn"}}), "the folder row is never checked")
		Expect(m.done).To(BeFalse())
	})

	It("asks for a row on u with nothing ticked and the cursor on Add a source…", func() {
		c := &checker{}
		m, cmd := pick(newPicker(c, 100), keyShG, keyU)
		Expect(cmd).To(BeNil())
		Expect(c.calls).To(Equal(0))
		Expect(drawn(m)).To(ContainSubstring("tick a marketplace, or move to one, to update it"))
	})

	It("offers, once the check ends, to update the checked rows that need it and yours it cannot check", func() {
		for _, yes := range []tea.Msg{keyEnter, keyY} {
			m := offered(&checker{}, engine.Check{Index: 0, Status: engine.StatusUpdate},
				engine.Check{Index: 1, Status: engine.StatusMoved}, engine.Check{Index: 2, Status: engine.StatusMoved})
			out := drawn(m)
			Expect(out).To(ContainSubstring("update acme, beta, local? ⏎/y yes · esc/n no"))
			Expect(out).NotTo(ContainSubstring("u check+update"), "the question's keys stand alone")
			Expect(m.done).To(BeFalse())
			m, cmd := pick(m, yes)
			Expect(m.done).To(BeTrue())
			Expect(cmd()).To(Equal(tea.QuitMsg{}))
			Expect(m.answer).To(Equal(MarketPickerAnswer{Action: PickerUpdate, Own: []string{"acme", "beta", "local"}}),
				"kn, a known one you have not enabled, is checked, never updated")
		}
	})

	It("offers only the rows that need it, never those up to date or unreachable", func() {
		m := offered(&checker{}, engine.Check{Index: 0, Status: engine.StatusCurrent},
			engine.Check{Index: 1, Status: engine.StatusUnreachable, Reason: "no route"}, engine.Check{Index: 2, Status: engine.StatusCurrent})
		Expect(drawn(m)).To(ContainSubstring("update local? ⏎/y yes · esc/n no"))
		m, _ = pick(m, keyY)
		Expect(m.answer).To(Equal(MarketPickerAnswer{Action: PickerUpdate, Own: []string{"local"}}))
	})

	It("offers yours it cannot check at once, contacting no host", func() {
		c := &checker{}
		m, cmd := pick(newPicker(c, 100), keyDown, keyDown, keyU)
		Expect(cmd).To(BeNil())
		Expect(c.calls).To(Equal(0))
		Expect(drawn(m)).To(ContainSubstring("update local? ⏎/y yes · esc/n no"))
		m, _ = pick(m, keyEnter)
		Expect(m.answer).To(Equal(MarketPickerAnswer{Action: PickerUpdate, Own: []string{"local"}}))
	})

	It("declines with esc or n at the offer and stays open with the statuses shown", func() {
		for _, no := range []tea.Msg{keyEsc, keyN} {
			m := offered(&checker{}, engine.Check{Index: 0, Status: engine.StatusUpdate},
				engine.Check{Index: 1, Status: engine.StatusCurrent}, engine.Check{Index: 2, Status: engine.StatusCurrent})
			m, cmd := pick(m, no)
			Expect(cmd).To(BeNil())
			Expect(m.done).To(BeFalse())
			out := drawn(m)
			Expect(out).NotTo(ContainSubstring("⏎/y yes"))
			Expect(out).To(MatchRegexp(`acme .*update available`))
			Expect(out).To(ContainSubstring("u check+update"))
			m, _ = pick(m, keyEsc)
			Expect(m.answer).To(Equal(MarketPickerAnswer{Action: PickerCancel}))
		}
	})

	It("ignores other keys at the offer, and q or Ctrl+C there change nothing", func() {
		results := []engine.Check{{Index: 0, Status: engine.StatusUpdate}, {Index: 1, Status: engine.StatusCurrent}, {Index: 2, Status: engine.StatusCurrent}}
		m := offered(&checker{}, results...)
		m, cmd := pick(m, append([]tea.Msg{keyDown, keyCtrlA}, presses("xa")...)...)
		Expect(cmd).To(BeNil())
		Expect(m.done).To(BeFalse())
		Expect(drawn(m)).To(ContainSubstring("update acme, local? ⏎/y yes · esc/n no"))
		Expect(strings.Count(drawn(m), "[x]")).To(Equal(4), "the ticks are as they were")

		m, _ = pick(offered(&checker{}, results...), keyQ)
		Expect(m.answer).To(Equal(MarketPickerAnswer{Action: PickerCancel}))
		m, _ = pick(offered(&checker{}, results...), keyCtrlC)
		Expect(m.answer).To(Equal(MarketPickerAnswer{Action: PickerCancel}))
		Expect(m.aborted).To(BeTrue())
	})

	It("waits for a filter being typed to end before it offers", func() {
		c := &checker{}
		m, cmd := pick(newPicker(c, 100), keyU)
		m, _ = pick(m, keySlash)
		c.ended(engine.Check{Index: 0, Status: engine.StatusUpdate})
		m, _ = feed(m, cmd, 2)
		m, _ = pick(m, presses("y")...)
		Expect(m.done).To(BeFalse())
		Expect(drawn(m)).To(ContainSubstring("/y"))
		m, _ = pick(m, keyBksp, keyEnter)
		Expect(drawn(m)).To(ContainSubstring("update acme? ⏎/y yes · esc/n no"))
		m, _ = pick(m, keyY)
		Expect(m.answer).To(Equal(MarketPickerAnswer{Action: PickerUpdate, Own: []string{"acme"}}))
	})

	It("says when nothing checked needs updating, and stays open", func() {
		for _, tc := range []struct {
			down   int
			status string
			note   string
		}{
			{0, engine.StatusCurrent, "everything checked is up to date"},
			{0, engine.StatusUnreachable, "nothing to update; 1 unreachable"},
			{3, engine.StatusMoved, "nothing to update; ⏎ enables a known marketplace"},
		} {
			c := &checker{}
			m := newPicker(c, 100)
			for range tc.down {
				m, _ = pick(m, keyDown)
			}
			m, cmd := pick(m, keyU)
			c.ended(engine.Check{Index: 0, Status: tc.status})
			m, _ = feed(m, cmd, 2)
			Expect(m.done).To(BeFalse())
			Expect(drawn(m)).To(ContainSubstring(tc.note))
			Expect(drawn(m)).NotTo(ContainSubstring("⏎/y yes"))
			m, _ = pick(m, keyEnter)
			Expect(m.answer.Action).To(Equal(PickerEnter), "the keys work as before")
		}
	})

	It("says nothing to update, not up to date, when u checked nothing", func() {
		own, known := pickerRows()
		known[0].Source = "/srv/kn"
		c := &checker{}
		m := newMarketPicker(MarketPickerStep{Own: own, Known: known, Check: c.check}, plainTheme(), 100, 40)
		m, cmd := pick(m, keyDown, keyDown, keyDown, keyU)
		Expect(cmd).To(BeNil())
		Expect(c.calls).To(Equal(0))
		Expect(drawn(m)).To(ContainSubstring("nothing to update"))
		Expect(drawn(m)).NotTo(ContainSubstring("up to date"))
	})

	It("keeps the offer to one line at any width, its keys always shown, the list still in view", func() {
		many := func() MarketPickerStep {
			var own []engine.PickerRow
			for k := range 12 {
				own = append(own, engine.PickerRow{ID: fmt.Sprintf("folder-marketplace-%02d", k),
					MarketRow: engine.MarketRow{Source: fmt.Sprintf("/srv/m%02d", k), FetchedAt: "2026-11-23T00:00:00Z", Entries: 1}})
			}
			return MarketPickerStep{Own: own, Check: (&checker{}).check}
		}
		one := func() MarketPickerStep {
			own, _ := pickerRows()
			return MarketPickerStep{Own: own[2:], Check: (&checker{}).check}
		}
		for _, width := range []int{30, 50, 80} {
			for _, st := range []MarketPickerStep{one(), many()} {
				m, _ := pick(newMarketPicker(st, plainTheme(), width, 12), keyCtrlA, keyU)
				footer := m.footer()
				Expect(footer).To(HaveLen(1), "%d columns: %q", width, footer)
				Expect(ansi.StringWidth(footer[0])).To(BeNumerically("<=", width), footer[0])
				Expect(footer[0]).To(HaveSuffix("? ⏎/y yes · esc/n no"))
				Expect(footer[0]).To(HavePrefix("update "))
				Expect(m.room()).To(BeNumerically(">=", 12-len(m.header())-2))
			}
		}
		m, _ := pick(newMarketPicker(one(), plainTheme(), 80, 12), keyU)
		Expect(m.footer()[0]).To(Equal("update local? ⏎/y yes · esc/n no"), "named when it fits")
		m, _ = pick(newMarketPicker(many(), plainTheme(), 80, 12), keyCtrlA, keyU)
		Expect(m.footer()[0]).To(Equal("update 12 marketplaces? ⏎/y yes · esc/n no"), "counted when the names do not fit")
		m, _ = pick(newMarketPicker(one(), plainTheme(), 30, 12), keyU)
		Expect(m.footer()[0]).To(Equal("update 1 …? ⏎/y yes · esc/n no"))
		m, _ = pick(m, keyY)
		Expect(m.answer).To(Equal(MarketPickerAnswer{Action: PickerUpdate, Own: []string{"local"}}))
	})

	It("checks every row on U, ticked or not and whatever the filter shows, and offers those of yours that need it", func() {
		c := &checker{}
		m, _ := pick(newPicker(c, 100), keyDown)
		m, _ = pick(m, presses("x")...)
		m, _ = pick(m, append([]tea.Msg{keySlash}, presses("acme")...)...)
		m, _ = pick(m, keyEnter)
		Expect(drawn(m)).NotTo(ContainSubstring("beta"))
		m, cmd := pick(m, keyShU)
		Expect(m.done).To(BeFalse())
		Expect(c.ids).To(Equal([][]string{{"acme", "beta", "kn"}}), "the folder row is offered, never checked")
		c.ended(engine.Check{Index: 0, Status: engine.StatusUpdate}, engine.Check{Index: 1, Status: engine.StatusCurrent},
			engine.Check{Index: 2, Status: engine.StatusMoved})
		m, _ = feed(m, cmd, 4)
		Expect(drawn(m)).To(ContainSubstring("update acme, local? ⏎/y yes · esc/n no"))
		m, _ = pick(m, keyY)
		Expect(m.answer).To(Equal(MarketPickerAnswer{Action: PickerUpdate, Own: []string{"acme", "local"}}))
	})

	It("offers nothing on U under the network banner", func() {
		c := &checker{}
		m, cmd := pick(newPicker(c, 100), keyShU)
		c.ended(engine.Check{Index: 0, Status: engine.StatusUnreachable, Reason: "timeout"},
			engine.Check{Index: 1, Status: engine.StatusUnreachable, Reason: "timeout"}, engine.Check{Index: 2, Status: engine.StatusCurrent})
		m, _ = feed(m, cmd, 4)
		Expect(drawn(m)).To(ContainSubstring("Several sources unreachable"))
		Expect(drawn(m)).NotTo(ContainSubstring("⏎/y yes"))
		Expect(m.done).To(BeFalse())
	})

	It("re-checks on U after u, cancelling the first run and dropping its results, and stops on leaving", func() {
		c := &checker{}
		m, first := pick(newPicker(c, 100), keyU)
		m, second := pick(m, keyShU)
		Expect(c.ids).To(Equal([][]string{{"acme"}, {"acme", "beta", "kn"}}))
		Expect(c.ctxs[0].Err()).To(HaveOccurred())
		c.chans[0] <- engine.Check{Index: 0, Status: engine.StatusUnreachable, Reason: "context canceled"}
		m, more := feed(m, first, 1)
		Expect(more).To(BeNil())
		Expect(m.rows[0].Status).To(Equal(engine.StatusChecking))
		c.chans[1] <- engine.Check{Index: 1, Status: engine.StatusCurrent}
		m, _ = feed(m, second, 1)
		Expect(drawn(m)).To(MatchRegexp(`beta .*up to date`))

		m, _ = pick(m, keyQ)
		Expect(c.ctxs[1].Err()).To(HaveOccurred())
		Expect(m.answer).To(Equal(MarketPickerAnswer{Action: PickerCancel}))
	})

	It("takes U and u typed in the filter as text", func() {
		c := &checker{}
		m, cmd := pick(newPicker(c, 100), keySlash, keyShU, keyU)
		Expect(cmd).To(BeNil())
		Expect(c.calls).To(Equal(0))
		Expect(drawn(m)).To(ContainSubstring("/Uu"))
	})

	It("takes r as no key at all", func() {
		c := &checker{}
		m := newPicker(c, 100)
		before := drawn(m)
		m, cmd := pick(m, presses("r")...)
		Expect(cmd).To(BeNil())
		Expect(m.done).To(BeFalse())
		Expect(c.calls).To(Equal(0))
		Expect(drawn(m)).To(Equal(before))
	})

	It("acts on the cursor row on Enter when nothing is ticked", func() {
		m, _ := pick(newPicker(&checker{}, 100), keyDown, keyDown, keyDown, keyEnter)
		Expect(m.answer).To(Equal(MarketPickerAnswer{Action: PickerEnter, Known: []string{"kn"}}))
	})

	It("adds a source with a, or Enter on Add a source…", func() {
		m, _ := pick(newPicker(&checker{}, 100), presses("a")...)
		Expect(m.answer).To(Equal(MarketPickerAnswer{Action: PickerAdd}))
		m, _ = pick(newPicker(&checker{}, 100), keyShG, keyEnter)
		Expect(m.answer).To(Equal(MarketPickerAnswer{Action: PickerAdd}))
	})

	It("cancels with esc and q, and Ctrl+C aborts", func() {
		for _, k := range []tea.Msg{keyEsc, keyQ} {
			m, cmd := pick(newPicker(&checker{}, 100), k)
			Expect(cmd()).To(Equal(tea.QuitMsg{}))
			Expect(m.answer).To(Equal(MarketPickerAnswer{Action: PickerCancel}))
			Expect(m.aborted).To(BeFalse())
			Expect(m.View().Content).To(BeEmpty())
		}
		m, cmd := pick(newPicker(&checker{}, 100), keyCtrlC)
		Expect(cmd()).To(Equal(tea.QuitMsg{}))
		Expect(m.aborted).To(BeTrue())
	})

	It("checks on u, git rows reading checking…, and a result updates its row alone", func() {
		c := &checker{}
		m, _ := pick(newPicker(c, 100), keyCtrlA)
		m, cmd := pick(m, keyDown, keyU)
		Expect(c.calls).To(Equal(1))
		Expect(strings.Count(drawn(m), engine.StatusChecking)).To(Equal(3)) // acme, beta, kn; not the folder
		Expect(drawn(m)).To(MatchRegexp(`local .*1 entry\n`))

		c.chans[0] <- engine.Check{Index: 0, Status: engine.StatusUpdate}
		m, _ = feed(m, cmd, 1)
		out := drawn(m)
		Expect(out).To(MatchRegexp(`acme .*update available`))
		Expect(out).To(MatchRegexp(`> \[x\] beta .*checking…`))
		Expect(out).NotTo(ContainSubstring("⏎/y yes"), "nothing is offered before the check ends")
		Expect(strings.Count(out, engine.StatusChecking)).To(Equal(2))
	})

	It("re-checks on a second u, cancelling the first run and dropping its results", func() {
		c := &checker{}
		m, first := pick(newPicker(c, 100), keyU)
		m, second := pick(m, keyU)
		Expect(c.calls).To(Equal(2))
		Expect(c.ctxs[0].Err()).To(HaveOccurred())
		Expect(c.ctxs[1].Err()).NotTo(HaveOccurred())

		c.chans[0] <- engine.Check{Index: 0, Status: engine.StatusUnreachable, Reason: "context canceled"}
		m, more := feed(m, first, 1)
		Expect(more).To(BeNil())
		Expect(drawn(m)).NotTo(ContainSubstring("unreachable"))

		c.chans[1] <- engine.Check{Index: 0, Status: engine.StatusCurrent}
		m, _ = feed(m, second, 1)
		Expect(drawn(m)).To(MatchRegexp(`acme .*up to date`))
	})

	It("shows the network banner, and rows not checked, when two of three checks fail, and offers nothing", func() {
		m := offered(&checker{}, engine.Check{Index: 0, Status: engine.StatusUnreachable, Reason: "timeout"},
			engine.Check{Index: 1, Status: engine.StatusUnreachable, Reason: "timeout"}, engine.Check{Index: 2, Status: engine.StatusCurrent})
		out := drawn(m)
		Expect(out).NotTo(ContainSubstring("⏎/y yes"), "not even local, which needs no check")
		Expect(out).To(ContainSubstring("nothing to update until the checks get through; u checks again"))
		Expect(out).To(ContainSubstring("Several sources unreachable; check your network connection"))
		Expect(strings.Count(out, engine.StatusNotChecked)).To(Equal(2))
		Expect(out).NotTo(ContainSubstring("· " + engine.StatusUnreachable))
		Expect(out).NotTo(ContainSubstring("--remove"))
		Expect(out).To(MatchRegexp(`kn .*up to date`))
	})

	It("shows one unreachable row with its reason and both hints", func() {
		m := offered(&checker{}, engine.Check{Index: 0, Status: engine.StatusUpdate},
			engine.Check{Index: 1, Status: engine.StatusUnreachable, Reason: "dial tcp: i/o timeout"}, engine.Check{Index: 2, Status: engine.StatusCurrent})
		out := drawn(m)
		Expect(out).NotTo(ContainSubstring("Several sources"))
		Expect(out).To(MatchRegexp(`beta .*unreachable`))
		Expect(out).To(ContainSubstring("dial tcp: i/o timeout"))
		Expect(out).To(ContainSubstring("u retries"))
		Expect(out).To(ContainSubstring("lola market beta --remove forgets it"))
	})

	It("names the connection too when the only check fails", func() {
		own, _ := pickerRows()
		c := &checker{}
		m := newMarketPicker(MarketPickerStep{Own: own[:1], Check: c.check}, plainTheme(), 100, 40)
		m, cmd := pick(m, keyU)
		c.chans[0] <- engine.Check{Index: 0, Status: engine.StatusUnreachable, Reason: "no route"}
		close(c.chans[0])
		m, _ = feed(m, cmd, 2)
		out := drawn(m)
		Expect(out).To(MatchRegexp(`acme .*unreachable`))
		Expect(out).To(ContainSubstring("check your network connection"))
		Expect(out).To(ContainSubstring("lola market acme --remove forgets it"))
		Expect(out).NotTo(ContainSubstring("Several sources"))
	})

	It("stops its checks on leaving and reads no result after", func() {
		for _, k := range []tea.Msg{keyEsc, keyQ, keyCtrlC} {
			c := &checker{}
			m, cmd := pick(newPicker(c, 100), keyU)
			m, _ = pick(m, k)
			Expect(c.ctxs[0].Err()).To(HaveOccurred())
			c.chans[0] <- engine.Check{Index: 0, Status: engine.StatusUnreachable, Reason: "context canceled"}
			m, more := feed(m, cmd, 1)
			Expect(more).To(BeNil())
			Expect(m.rows[0].Status).To(Equal(engine.StatusChecking))
		}
	})

	It("renders a source's escape sequence harmless", func() {
		own, _ := pickerRows()
		own[0].Source = "https://example.com/\x1b[31mred"
		own[0].Reason = "\x1b]0;title\x07"
		m := newMarketPicker(MarketPickerStep{Own: own}, plainTheme(), 100, 40)
		Expect(m.View().Content).NotTo(ContainSubstring("\x1b[31m"))
		Expect(m.View().Content).NotTo(ContainSubstring("\x1b]0"))
		Expect(drawn(m)).To(ContainSubstring("?[31mred"))
	})

	It("wraps to a 30-column terminal without panicking", func() {
		own, known := pickerRows()
		own[0].Source = "https://example.com/" + strings.Repeat("long-path-", 8) + "market.git"
		m := newMarketPicker(MarketPickerStep{Own: own, Known: known}, plainTheme(), 30, 12)
		var out string
		Expect(func() {
			m, _ = pick(m, keyShG, keyG, keyDown, keyDown)
			m, _ = pick(m, tea.WindowSizeMsg{Width: 30, Height: 12})
			out = drawn(m)
		}).NotTo(Panic())
		for l := range strings.SplitSeq(out, "\n") {
			Expect(ansi.StringWidth(l)).To(BeNumerically("<=", 30), l)
		}
		Expect(out).To(ContainSubstring("long-path-"))
		Expect(strings.Count(out, "long-path-")).To(BeNumerically(">", 1))
		Expect(out).To(ContainSubstring("u check+update"))
		Expect(func() { _, _ = pick(m, tea.WindowSizeMsg{Width: 1, Height: 1}) }).NotTo(Panic())
	})

	It("wraps a row's status on a narrow terminal rather than cutting it", func() {
		c := &checker{}
		m, cmd := pick(newPicker(c, 30), keyU)
		c.chans[0] <- engine.Check{Index: 0, Status: engine.StatusUpdate}
		m, _ = feed(m, cmd, 1)
		Expect(drawn(m)).To(MatchRegexp(`> \[ \] acme  3 entries ·\n {6}update available\n`))
	})

	It("fits its keys in two rows at most, never wider than the terminal, on one row at 80 columns", func() {
		for _, width := range []int{30, 50, 80} {
			for _, keys := range [][]tea.Msg{nil, {keySlash}} {
				m, _ := pick(newPicker(&checker{}, width), keys...)
				footer := m.footer()
				Expect(len(footer)).To(BeNumerically("<=", 2), "%d columns: %q", width, footer)
				for _, l := range footer {
					Expect(ansi.StringWidth(l)).To(BeNumerically("<=", width), l)
				}
				if width == 80 {
					Expect(footer).To(HaveLen(1))
					Expect(ansi.StringWidth(footer[0])).To(BeNumerically("<=", 80), footer[0])
				}
			}
		}
		m := newPicker(&checker{}, 80)
		Expect(ansi.Strip(m.footer()[0])).To(Equal("u check+update · U all · x tick · / filter · a add · ⏎ act · esc back · q quit"))
		m, _ = pick(m, keySlash)
		Expect(ansi.Strip(m.footer()[0])).To(Equal("⏎ keep filter · esc clear · ↑↓ move"))
		m, _ = pick(newPicker(&checker{}, 80), keyCtrlA)
		Expect(strings.Count(drawn(m), "[x]")).To(Equal(4), "ctrl+a still ticks all, though the footer leaves it out")
	})

	It("keeps the cursor row in view on a short terminal", func() {
		m := newMarketPicker(func() MarketPickerStep { o, k := pickerRows(); return MarketPickerStep{Own: o, Known: k} }(), plainTheme(), 80, 9)
		m, _ = pick(m, keyShG)
		Expect(drawn(m)).To(ContainSubstring("> Add a source…"))
		Expect(strings.Count(drawn(m), "\n")).To(BeNumerically("<", 9))
		m, _ = pick(m, keyG)
		Expect(drawn(m)).To(ContainSubstring("> [ ] acme"))
	})

	Context("in accessible mode", func() {
		step := func() MarketPickerStep {
			own, known := pickerRows()
			return MarketPickerStep{Own: own, Known: known, Check: func(context.Context, []engine.PickerRow) <-chan engine.Check {
				Fail("accessible mode checked")
				return nil
			}}
		}

		It("lists the rows numbered, with no status, and answers the numbers chosen", func() {
			var out bytes.Buffer
			got, err := accessible(&out, "4, 1").MarketPicker(step())
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(MarketPickerAnswer{Action: PickerEnter, Own: []string{"acme"}, Known: []string{"kn"}}))
			Expect(out.String()).To(ContainSubstring("1. acme"))
			Expect(out.String()).To(ContainSubstring("https://example.com/kn.git — curated skills"))
			Expect(out.String()).To(ContainSubstring("5. Add a source…"))
			Expect(out.String()).To(ContainSubstring("Choose marketplaces (numbers, comma separated):"))
			Expect(out.String()).To(MatchRegexp(`\n\n-+\nChoose marketplaces`)) // under the accessible break
			Expect(out.String()).NotTo(ContainSubstring("check+update"))
		})

		It("adds a source for Add a source…'s number, and cancels on an empty answer", func() {
			var out bytes.Buffer
			got, err := accessible(&out, "5").MarketPicker(step())
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(MarketPickerAnswer{Action: PickerAdd}))
			got, err = accessible(&out, "").MarketPicker(step())
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(MarketPickerAnswer{Action: PickerCancel}))
		})

		It("asks again after an answer it cannot take", func() {
			var out bytes.Buffer
			got, err := accessible(&out, "9", "x", "1,5", "2").MarketPicker(step())
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(MarketPickerAnswer{Action: PickerEnter, Own: []string{"beta"}}))
			Expect(strings.Count(out.String(), "Choose marketplaces")).To(Equal(4))
			Expect(out.String()).To(ContainSubstring("answer numbers from 1 to 5"))
			Expect(out.String()).To(ContainSubstring("Add a source… goes alone"))
		})

		It("refuses when input ends", func() {
			var out bytes.Buffer
			_, err := accessible(&out).MarketPicker(step())
			Expect(err).To(MatchError(errInputEnded))
		})
	})

	It("never asks off a terminal", func() {
		_, err := noPrompter{}.MarketPicker(MarketPickerStep{})
		Expect(err).To(MatchError(errNoTerminal))
		_, _, err = noPrompter{}.MarketSource(MarketSourceStep{})
		Expect(err).To(MatchError(errNoTerminal))
	})

	It("asks Add a source…'s source and ref on line prompts, trimmed, and refuses when input ends", func() {
		var out bytes.Buffer
		src, ref, err := accessible(&out, "  https://git.test/m.git ", " v1 ").MarketSource(MarketSourceStep{})
		Expect(err).NotTo(HaveOccurred())
		Expect([]string{src, ref}).To(Equal([]string{"https://git.test/m.git", "v1"}))
		src, ref, err = accessible(&out, "/srv/m", "").MarketSource(MarketSourceStep{})
		Expect(err).NotTo(HaveOccurred())
		Expect([]string{src, ref}).To(Equal([]string{"/srv/m", ""}))
		_, _, err = accessible(&out).MarketSource(MarketSourceStep{})
		Expect(err).To(MatchError(errInputEnded))
	})
})
