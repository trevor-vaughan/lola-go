package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
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

	It("draws a row with no description on two lines, the source dim and indented, a known row's reason above it", func() {
		out := drawn(newPicker(&checker{}, 100))
		Expect(out).To(MatchRegexp(`> \[ \] acme .*3 entries\n {6}https://example\.com/acme\.git\n`))
		Expect(out).To(MatchRegexp(`\[ \] kn .*known\n {6}curated skills\n {6}https://example\.com/kn\.git\n`))
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

	It("acts on the cursor row on m when nothing is ticked", func() {
		m, _ := pick(newPicker(&checker{}, 100), keyDown, keyDown, keyDown, keyM)
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
		Expect(out).To(ContainSubstring("lola market rm beta forgets it"))
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
		Expect(out).To(ContainSubstring("lola market rm acme forgets it"))
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

	It("fits its keys in two rows at most, never wider than the terminal", func() {
		for _, width := range []int{30, 50, 80} {
			for _, keys := range [][]tea.Msg{nil, {keySlash}} {
				m, _ := pick(newPicker(&checker{}, width), keys...)
				footer := m.footer()
				Expect(len(footer)).To(BeNumerically("<=", 2), "%d columns: %q", width, footer)
				for _, l := range footer {
					Expect(ansi.StringWidth(l)).To(BeNumerically("<=", width), l)
				}
			}
		}
		m := newPicker(&checker{}, 80)
		Expect(ansi.Strip(strings.Join(m.footer(), " · "))).To(Equal("u check+update · U all · x tick · / filter · a add · ⏎ open · o web · m menu · q quit"))
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
			Expect(out.String()).To(ContainSubstring("curated skills\n    https://example.com/kn.git\n"))
			Expect(out.String()).To(ContainSubstring("5. Add a source…"))
			Expect(out.String()).To(ContainSubstring("Choose marketplaces (numbers, comma separated; m1 for the menu; o1 for the web page):"))
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
			got, err := accessible(&out, "9", "x", "1,5", "m2").MarketPicker(step())
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

var (
	keyM     = tea.KeyPressMsg{Code: 'm', Text: "m"}
	keyI     = tea.KeyPressMsg{Code: 'i', Text: "i"}
	keyD     = tea.KeyPressMsg{Code: 'd', Text: "d"}
	keyO     = tea.KeyPressMsg{Code: 'o', Text: "o"}
	keyLeft  = tea.KeyPressMsg{Code: tea.KeyLeft}
	keyRight = tea.KeyPressMsg{Code: tea.KeyRight}
	keyX     = tea.KeyPressMsg{Code: 'x', Text: "x"}
)

// entryPicker is newPicker with acme offering two entries, the second installed, in a 100-wide terminal.
func entryPicker(c *checker) marketPicker {
	own, known := pickerRows()
	entries := map[string][]engine.PickerEntry{"acme": {
		{MarketOffer: engine.MarketOffer{Install: "@acme/review", Name: "review", Version: "1.2.0", Description: "Code review", Tags: []string{"go"}, URL: "https://example.com/acme.git", License: "MIT"}},
		{MarketOffer: engine.MarketOffer{Install: "@acme/fmt", Name: "fmt", Version: "0.3.0", Description: "Formatter", Cached: true}, Installed: true, At: strings.Repeat("c", 40)},
	}}
	return newMarketPicker(MarketPickerStep{Own: own, Known: known, Check: c.check, Entries: entries, Context: "project /srv/app", ScopeKind: "project"}, plainTheme(), 100, 40)
}

var _ = Describe("the marketplace picker's entries and detail", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("opens a marketplace's entries on Enter with nothing ticked, under the context line", func() {
		m, _ := pick(entryPicker(&checker{}), keyEnter)
		out := drawn(m)
		Expect(m.done).To(BeFalse())
		Expect(out).To(ContainSubstring("review 1.2.0"))
		Expect(out).To(ContainSubstring("fmt 0.3.0 · installed"))
		Expect(out).To(ContainSubstring("project /srv/app"))
		Expect(out).To(ContainSubstring("Code review"))
		Expect(out).NotTo(ContainSubstring("beta"))
	})

	It("keeps the menu on Enter with a row ticked, and on m", func() {
		m, _ := pick(entryPicker(&checker{}), keyDown, keyX, keyUp, keyEnter)
		Expect(m.answer).To(Equal(MarketPickerAnswer{Action: PickerEnter, Own: []string{"beta"}}))
		Expect(m.done).To(BeTrue())

		m, _ = pick(entryPicker(&checker{}), keyM)
		Expect(m.answer).To(Equal(MarketPickerAnswer{Action: PickerEnter, Own: []string{"acme"}}))
	})

	It("acts on a known row on m, and says a row without entries has none", func() {
		m, _ := pick(entryPicker(&checker{}), keyDown, keyDown, keyDown, keyM)
		Expect(m.answer).To(Equal(MarketPickerAnswer{Action: PickerEnter, Known: []string{"kn"}}))

		m, _ = pick(entryPicker(&checker{}), keyDown, keyDown, keyEnter)
		Expect(m.done).To(BeFalse())
		Expect(drawn(m)).To(ContainSubstring("local has no entries; u fetches its catalog"))
		m, _ = pick(m, keyDown)
		Expect(drawn(m)).NotTo(ContainSubstring("no entries"))
	})

	It("draws an entry's detail, and refuses i on an installed one and d on one that is not", func() {
		m, _ := pick(entryPicker(&checker{}), keyEnter, keyDown, keyEnter)
		out := drawn(m)
		for _, want := range []string{"project /srv/app", "fmt 0.3.0", "Formatter", "installed (ccccccc) · in your cache", "d uninstall"} {
			Expect(out).To(ContainSubstring(want))
		}
		Expect(out).NotTo(ContainSubstring("i install"))
		Expect(out).NotTo(ContainSubstring("license:"), "an entry whose catalog states no license has no license line")
		m, _ = pick(m, keyI)
		Expect(m.done).To(BeFalse())
		Expect(drawn(m)).To(ContainSubstring("already installed; d uninstalls"))
		m, cmd := pick(m, keyD)
		Expect(m.answer).To(Equal(MarketPickerAnswer{Action: PickerUninstall, Market: "acme", Entry: "fmt", Entries: []string{"fmt"}}))
		Expect(cmd()).To(Equal(tea.QuitMsg{}))

		m, _ = pick(entryPicker(&checker{}), keyEnter, keyEnter)
		out = drawn(m)
		for _, want := range []string{"review 1.2.0", "Code review", "tags: go", "url: https://example.com/acme.git", "license: MIT", "not installed", "i install"} {
			Expect(out).To(ContainSubstring(want))
		}
		Expect(out).To(MatchRegexp(`url: [^\n]*\n  license: MIT\n\n  not installed`), "license follows url, before the state block")
		Expect(out).NotTo(ContainSubstring("d uninstall"))
		m, _ = pick(m, keyD)
		Expect(m.done).To(BeFalse())
		Expect(drawn(m)).To(ContainSubstring("not installed; i installs"))
		m, _ = pick(m, keyI)
		Expect(m.answer).To(Equal(MarketPickerAnswer{Action: PickerInstall, Market: "acme", Entry: "review", Entries: []string{"review"}}))
	})

	It("indents everything under the detail page's title by two spaces, wrapped lines too", func() {
		own, known := pickerRows()
		entries := map[string][]engine.PickerEntry{"acme": {{MarketOffer: engine.MarketOffer{Name: "fmt", Version: "0.3.0", Cached: true,
			Description: strings.Repeat("word ", 30), Tags: []string{"go", "lint"}, URL: "https://example.com/acme.git", License: "Apache-2.0 OR MIT"}, Installed: true, At: "ccccccc"}}}
		m := newMarketPicker(MarketPickerStep{Own: own, Known: known, Entries: entries, Context: "project /srv/app", ScopeKind: "project"}, plainTheme(), 80, 40)
		m, _ = pick(m, keyEnter, keyEnter)
		out := drawn(m)
		Expect(out).To(MatchRegexp(`(?m)^fmt 0\.3\.0$`))
		for _, want := range []string{"  tags: go, lint", "  url: https://example.com/acme.git", "  license: Apache-2.0 OR MIT", "  installed (ccccccc) · in your cache"} {
			Expect(out).To(MatchRegexp(`(?m)^` + regexp.QuoteMeta(want) + `$`))
		}
		words := 0
		for l := range strings.SplitSeq(out, "\n") {
			if strings.Contains(l, "word") {
				Expect(l).To(HavePrefix("  word"), "wrapped description lines keep the indent")
				Expect(len([]rune(l))).To(BeNumerically("<=", 80))
				words++
			}
		}
		Expect(words).To(BeNumerically(">", 1))
	})

	It("installs and uninstalls from the entries page, saying why when the key does not apply", func() {
		m, _ := pick(entryPicker(&checker{}), keyEnter)
		Expect(drawn(m)).To(And(ContainSubstring("i install"), Not(ContainSubstring("d uninstall"))))
		m, _ = pick(m, keyD)
		Expect(m.done).To(BeFalse())
		Expect(drawn(m)).To(ContainSubstring("not installed; i installs"))
		m, cmd := pick(m, keyI)
		Expect(m.answer).To(Equal(MarketPickerAnswer{Action: PickerInstall, Market: "acme", Entry: "review", Entries: []string{"review"}, FromEntries: true}))
		Expect(cmd()).To(Equal(tea.QuitMsg{}))

		m, _ = pick(entryPicker(&checker{}), keyEnter, keyDown)
		Expect(drawn(m)).To(And(ContainSubstring("d uninstall"), Not(ContainSubstring("i install"))))
		m, _ = pick(m, keyI)
		Expect(m.done).To(BeFalse())
		Expect(drawn(m)).To(ContainSubstring("already installed; d uninstalls"))
		m, _ = pick(m, keyD)
		Expect(m.answer).To(Equal(MarketPickerAnswer{Action: PickerUninstall, Market: "acme", Entry: "fmt", Entries: []string{"fmt"}, FromEntries: true}))
	})

	It("acts on the entry the filter leaves under the cursor, and on none when it shows none", func() {
		m, _ := pick(entryPicker(&checker{}), append([]tea.Msg{keyEnter}, append(presses("/fmt"), keyEnter)...)...)
		m, _ = pick(m, keyD)
		Expect(m.answer).To(Equal(MarketPickerAnswer{Action: PickerUninstall, Market: "acme", Entry: "fmt", Entries: []string{"fmt"}, FromEntries: true}))

		m, _ = pick(entryPicker(&checker{}), append([]tea.Msg{keyEnter}, append(presses("/zzz"), keyEnter)...)...)
		Expect(func() { m, _ = pick(m, keyI, keyD) }).NotTo(Panic())
		Expect(m.done).To(BeFalse())
		Expect(m.answer).To(BeZero())
		Expect(drawn(m)).To(And(ContainSubstring("no entry matches the filter"), Not(ContainSubstring("i install")), Not(ContainSubstring("d uninstall"))))
	})

	It("takes i and d as filter characters while a filter is typed on the entries page", func() {
		m, _ := pick(entryPicker(&checker{}), append([]tea.Msg{keyEnter}, presses("/id")...)...)
		Expect(m.done).To(BeFalse())
		Expect(m.filter).To(Equal("id"))
		Expect(drawn(m)).To(And(ContainSubstring("⏎ keep filter"), Not(ContainSubstring("i install"))))
	})

	It("reopens on the entries page, the cursor on the entry, when Resume says it began there", func() {
		own, known := pickerRows()
		entries := entryPicker(&checker{}).entries
		m := newMarketPicker(MarketPickerStep{Own: own, Known: known, Entries: entries, Resume: PickerResume{Market: "acme", Entry: "fmt", OnEntries: true}}, plainTheme(), 100, 40)
		Expect(m.level).To(Equal(entriesLevel))
		Expect(drawn(m)).To(ContainSubstring("> [ ] fmt 0.3.0"))
		m, _ = pick(m, keyEsc)
		Expect(drawn(m)).To(ContainSubstring("> [ ] acme"))
	})

	It("keeps the entries footer within two rows at 80 columns and draws at width 40 and 0", func() {
		own, known := pickerRows()
		entries := entryPicker(&checker{}).entries
		for _, width := range []int{80, 40, 0} {
			m := newMarketPicker(MarketPickerStep{Own: own, Known: known, Entries: entries, Context: "project /srv/app", ScopeKind: "project"}, plainTheme(), width, 12)
			m, _ = pick(m, keyEnter)
			Expect(func() { _ = drawn(m) }).NotTo(Panic())
			if width == 80 {
				Expect(m.entriesFooter()).To(HaveLen(1))
			}
		}
	})

	Describe("opening an entry's web page", func() {
		const page = "https://github.com/acme/tools/tree/main/review"
		// webPicker is the entries of acme: review with a web page, fmt without, and the ones with the
		// addresses a hostile catalog could make; open records the addresses it is asked to launch.
		webPicker := func(width int, open func(string) error) marketPicker {
			own, known := pickerRows()
			entries := map[string][]engine.PickerEntry{"acme": {
				{MarketOffer: engine.MarketOffer{Name: "review"}, WebURL: page},
				{MarketOffer: engine.MarketOffer{Name: "fmt"}, Installed: true},
			}}
			return newMarketPicker(MarketPickerStep{Own: own, Known: known, Entries: entries, Context: "project /srv/app", ScopeKind: "project", OpenURL: open}, plainTheme(), width, 40)
		}

		It("launches the address from a command, not from the key, then says it opened it", func() {
			var launched []string
			m := webPicker(80, func(u string) error { launched = append(launched, u); return nil })
			m, _ = pick(m, keyEnter)
			m, cmd := pick(m, keyO)
			Expect(launched).To(BeEmpty(), "Update itself starts nothing")
			Expect(cmd).NotTo(BeNil())
			m, _ = pick(m, cmd())
			Expect(launched).To(Equal([]string{page}))
			Expect(m.done).To(BeFalse())
			Expect(drawn(m)).To(ContainSubstring("opened " + page))

			launched = nil
			m, _ = pick(m, keyEnter)
			m, cmd = pick(m, keyO)
			m, _ = pick(m, cmd())
			Expect(launched).To(Equal([]string{page}), "the detail page, of the same entry")
		})

		It("cuts the address in its note to fit", func() {
			m := webPicker(30, func(string) error { return nil })
			m, _ = pick(m, keyEnter)
			m, cmd := pick(m, keyO)
			m, _ = pick(m, cmd())
			for l := range strings.SplitSeq(strings.TrimRight(drawn(m), "\n"), "\n") {
				Expect(ansi.StringWidth(l)).To(BeNumerically("<=", 30), l)
			}
			Expect(drawn(m)).To(ContainSubstring("opened https://"))
		})

		It("says an entry with no web page has none, and launches nothing", func() {
			launched := false
			m := webPicker(80, func(string) error { launched = true; return nil })
			m, cmd := pick(m, keyEnter, keyDown, keyO)
			Expect(cmd).To(BeNil())
			Expect(launched).To(BeFalse())
			Expect(drawn(m)).To(ContainSubstring("no web page for this entry"))
			m, cmd = pick(m, keyEnter, keyO)
			Expect(cmd).To(BeNil(), "the detail page, of the same entry")
			Expect(launched).To(BeFalse())
			Expect(drawn(m)).To(ContainSubstring("no web page for this entry"))
		})

		DescribeTable("refuses an address that is not a plain http(s) one, launching nothing",
			func(bad string) {
				launched := false
				own, known := pickerRows()
				entries := map[string][]engine.PickerEntry{"acme": {{MarketOffer: engine.MarketOffer{Name: "bad"}, WebURL: bad}}}
				m := newMarketPicker(MarketPickerStep{Own: own, Known: known, Entries: entries, OpenURL: func(string) error { launched = true; return nil }}, plainTheme(), 80, 40)
				m, cmd := pick(m, keyEnter, keyO)
				for range 2 { // the entries page, then the detail page
					Expect(cmd).To(BeNil())
					Expect(launched).To(BeFalse())
					Expect(m.done).To(BeFalse())
					Expect(drawn(m)).To(ContainSubstring("refusing to open this entry's address"))
					Expect(m.View().Content).NotTo(ContainSubstring("\x1b[31m"))
					m, cmd = pick(m, keyEnter, keyO)
				}
			},
			Entry("a newline", "https://example.org/a\nb"),
			Entry("an escape sequence", "https://example.org/\x1b[31m"),
			Entry("a leading dash", "-oProxyCommand=x"),
			Entry("a leading dash after a space", " -x https://example.org"),
			Entry("a space inside", "https://example.org/a b"),
			Entry("another scheme", "ftp://example.org/x"),
			Entry("a file scheme", "file:///etc/passwd"),
			Entry("a scheme-less address", "example.org/x"),
			Entry("no host", "https:///x"),
			Entry("a non-ASCII character", "https://example.org/\u202ex"),
			Entry("a command substitution in the host", "https://a$(id)b.com/x/y"),
			Entry("a semicolon and quotes in the host", "https://a;b'c\"d<e>.com/x"),
			Entry("a glob in the host", "https://x*.com/x"),
			Entry("userinfo", "https://user@example.org/x"),
			Entry("userinfo with a second at sign", "https://a@b@example.org/x"),
			Entry("a port that is not digits", "https://example.org:80a/x"),
		)

		It("says what failed and the address when the launcher fails", func() {
			m := webPicker(200, func(string) error { return &exec.Error{Name: "xdg-open", Err: exec.ErrNotFound} })
			m, _ = pick(m, keyEnter)
			m, cmd := pick(m, keyO)
			m, _ = pick(m, cmd())
			Expect(m.done).To(BeFalse())
			Expect(drawn(m)).To(ContainSubstring("could not open a browser (xdg-open not found); the address is " + page))

			m, _ = pick(m, keyEnter)
			m, cmd = pick(m, keyO)
			m, _ = pick(m, cmd())
			Expect(drawn(m)).To(ContainSubstring("could not open a browser (xdg-open not found); the address is "+page), "the detail page too")

			m = webPicker(200, func(string) error { return errors.New("exit status 3") })
			m, _ = pick(m, keyEnter)
			m, cmd = pick(m, keyO)
			m, _ = pick(m, cmd())
			Expect(drawn(m)).To(ContainSubstring("could not open a browser (exit status 3); the address is " + page))
		})

		It("wraps the address of a failed launch rather than cutting it", func() {
			m := webPicker(30, func(string) error { return errors.New("no") })
			m, _ = pick(m, keyEnter)
			m, cmd := pick(m, keyO)
			m, _ = pick(m, cmd())
			Expect(strings.Join(strings.Fields(drawn(m)), "")).To(ContainSubstring("theaddressis" + page))
		})

		It("takes o as a filter character while a filter is typed, and does nothing on an empty list", func() {
			launched := false
			m := webPicker(80, func(string) error { launched = true; return nil })
			m, _ = pick(m, keyEnter)
			m, cmd := pick(m, append(presses("/o"), keyEnter)...)
			Expect(cmd).To(BeNil())
			Expect(m.filter).To(Equal("o"))
			Expect(launched).To(BeFalse())
			m, cmd = pick(m, append(presses("/zzz"), keyEnter)...)
			Expect(func() { m, cmd = pick(m, keyO) }).NotTo(Panic())
			Expect(cmd).To(BeNil())
			Expect(launched).To(BeFalse())
		})

		It("offers o and open only where they apply", func() {
			m := webPicker(80, func(string) error { return nil })
			m, _ = pick(m, keyEnter)
			Expect(drawn(m)).To(And(ContainSubstring("o web"), ContainSubstring("⏎/→ open")))
			m, _ = pick(m, keyDown)
			out := drawn(m)
			Expect(out).To(And(ContainSubstring("⏎/→ open"), Not(ContainSubstring("o web"))), "an entry with no web page")
			m, _ = pick(m, keyEnter)
			Expect(drawn(m)).NotTo(ContainSubstring("o web"))
			m, _ = pick(m, keyEsc)
			m, _ = pick(m, append(presses("/zzz"), keyEnter)...)
			out = drawn(m)
			Expect(out).To(And(ContainSubstring("/ filter"), Not(ContainSubstring("o web")), Not(ContainSubstring("⏎/→ open")),
				Not(ContainSubstring("i install")), Not(ContainSubstring("d uninstall"))))
			Expect(len(m.entriesFooter())).To(BeNumerically("<=", 2))
		})

		It("names o in the entries and detail footers, within two rows at 80 columns", func() {
			m := webPicker(80, func(string) error { return nil })
			m, _ = pick(m, keyEnter)
			Expect(len(m.entriesFooter())).To(BeNumerically("<=", 2))
			Expect(drawn(m)).To(ContainSubstring("o web"))
			m, _ = pick(m, keyEnter)
			Expect(len(m.entriesFooter())).To(BeNumerically("<=", 2))
			Expect(drawn(m)).To(ContainSubstring("o web"))
		})
	})

	It("goes back a level on Left, restoring the list's cursor and filter, and never cancels from the list", func() {
		m, _ := pick(entryPicker(&checker{}), append(presses("/acme"), keyEnter)...)
		m, _ = pick(m, keyRight, keyDown, keyRight)
		Expect(m.level).To(Equal(detailLevel))
		m, _ = pick(m, keyLeft)
		Expect(m.level).To(Equal(entriesLevel))
		Expect(drawn(m)).To(ContainSubstring("> [ ] fmt 0.3.0"), "the entries, on the entry")
		m, _ = pick(m, append(presses("/fmt"), keyEnter)...)
		m, _ = pick(m, keyLeft)
		Expect(m.level).To(Equal(listLevel), "Left pops the level even with an entries filter set")
		out := drawn(m)
		Expect(out).To(ContainSubstring("> [ ] acme"))
		Expect(out).To(ContainSubstring("/acme"))
		m, _ = pick(m, keyLeft)
		Expect(m.done).To(BeFalse())
		Expect(m.level).To(Equal(listLevel))
	})

	It("goes in a level on Right, only where Enter would drill or open", func() {
		m, _ := pick(entryPicker(&checker{}), keyRight)
		Expect(m.level).To(Equal(entriesLevel))
		m, _ = pick(m, keyRight)
		Expect(m.level).To(Equal(detailLevel))
		m, _ = pick(m, keyRight)
		Expect(m.level).To(Equal(detailLevel))
		Expect(m.done).To(BeFalse())

		m, _ = pick(entryPicker(&checker{}), keyDown, keyX, keyUp, keyRight)
		Expect(m.level).To(Equal(listLevel), "a ticked row: Right does not enter the menu")
		Expect(m.done).To(BeFalse())
		m, _ = pick(entryPicker(&checker{}), keyEnd, keyRight)
		Expect(m.done).To(BeFalse(), "Add a source…")
		m, _ = pick(entryPicker(&checker{}), keyDown, keyDown, keyRight)
		Expect(m.level).To(Equal(listLevel))
		Expect(drawn(m)).To(ContainSubstring("local has no entries; u fetches its catalog"))

		m, _ = pick(entryPicker(&checker{}), append([]tea.Msg{keyEnter}, append(presses("/zzz"), keyEnter)...)...)
		m, _ = pick(m, keyRight)
		Expect(m.level).To(Equal(entriesLevel), "no entry under the cursor")
	})

	It("ignores Left and Right while a filter is typed", func() {
		m, _ := pick(entryPicker(&checker{}), append([]tea.Msg{keyEnter}, presses("/")...)...)
		m, _ = pick(m, keyLeft, keyRight)
		Expect(m.level).To(Equal(entriesLevel))
		Expect(m.typing).To(BeTrue())
		m, _ = pick(entryPicker(&checker{}), presses("/")...)
		m, _ = pick(m, keyRight)
		Expect(m.level).To(Equal(listLevel))
		Expect(m.typing).To(BeTrue())
	})

	It("goes back a page on Esc, restoring the cursor and the filter, and cancels from the list", func() {
		m, _ := pick(entryPicker(&checker{}), append(presses("/acme"), keyEnter)...)
		m, _ = pick(m, keyEnter, keyDown, keyEnter)
		m, _ = pick(m, keyEsc)
		Expect(drawn(m)).To(ContainSubstring("> [ ] fmt 0.3.0"), "the entries, on the entry")
		m, _ = pick(m, keyEsc)
		out := drawn(m)
		Expect(out).To(ContainSubstring("> [ ] acme"))
		Expect(out).To(ContainSubstring("/acme"))
		Expect(out).NotTo(ContainSubstring("beta"))
		Expect(m.done).To(BeFalse())
		m, _ = pick(m, keyEsc, keyEsc)
		Expect(m.answer).To(Equal(MarketPickerAnswer{Action: PickerCancel}))
	})

	It("filters entries by name, description and tag, shows none for no match, and opens nothing then", func() {
		m, _ := pick(entryPicker(&checker{}), keyEnter)
		m, _ = pick(m, append(presses("/fmt"), keyEnter)...)
		Expect(drawn(m)).To(And(ContainSubstring("fmt 0.3.0"), Not(ContainSubstring("review"))))
		m, _ = pick(m, keyEsc)
		Expect(drawn(m)).To(ContainSubstring("review 1.2.0"))

		m, _ = pick(m, append(presses("/GO"), keyEnter)...)
		Expect(drawn(m)).To(And(ContainSubstring("review 1.2.0"), Not(ContainSubstring("fmt"))), "by tag, ignoring case")
		m, _ = pick(m, keyEsc)

		m, _ = pick(m, append(presses("/zzz"), keyEnter)...)
		Expect(drawn(m)).To(ContainSubstring("no entry matches the filter"))
		m, _ = pick(m, keyEnter, keyDown, keyUp, keyEnd, keyHome)
		Expect(m.done).To(BeFalse())
		Expect(m.level).To(Equal(entriesLevel))
		m, _ = pick(m, keyEsc)
		Expect(drawn(m)).To(ContainSubstring("review 1.2.0"))
	})

	It("quits on q at either page and aborts on Ctrl+C", func() {
		for _, keys := range [][]tea.Msg{{keyEnter, keyQ}, {keyEnter, keyEnter, keyQ}} {
			m, _ := pick(entryPicker(&checker{}), keys...)
			Expect(m.answer).To(Equal(MarketPickerAnswer{Action: PickerCancel}))
			Expect(m.aborted).To(BeFalse())
		}
		m, _ := pick(entryPicker(&checker{}), keyEnter, keyCtrlC)
		Expect(m.level).To(Equal(entriesLevel))
		Expect(m.aborted).To(BeTrue())
		Expect(m.done).To(BeTrue())
		m, _ = pick(entryPicker(&checker{}), keyEnter, keyEnter, keyCtrlC)
		Expect(m.aborted).To(BeTrue())
		Expect(m.done).To(BeTrue())
	})

	It("draws author text on one line without control sequences", func() {
		own, known := pickerRows()
		entries := map[string][]engine.PickerEntry{"acme": {{MarketOffer: engine.MarketOffer{Name: "evil\x1b[31m\nname", Version: "1\n2",
			Description: "line one\nline two", Tags: []string{"t\x1b[2Jag"}, URL: "h\nost"}, Installed: true, At: "x\ny"}}}
		m := newMarketPicker(MarketPickerStep{Own: own, Known: known, Entries: entries}, plainTheme(), 100, 40)
		m, _ = pick(m, keyEnter)
		Expect(m.View().Content).NotTo(ContainSubstring("\x1b[31m"))
		Expect(drawn(m)).To(MatchRegexp(`evil\S* name 1 2 · installed\n`))
		Expect(drawn(m)).To(ContainSubstring("line one line two"))
		m, _ = pick(m, keyEnter)
		Expect(m.View().Content).NotTo(ContainSubstring("\x1b[2J"))
		Expect(drawn(m)).To(MatchRegexp(`url: h ost\n`))
		Expect(drawn(m)).To(MatchRegexp(`tags: t\S*ag\n`))
		Expect(drawn(m)).To(MatchRegexp(`installed \(x y\)\n`))
	})

	It("shortens a recorded content digest to seven digits on the detail page, on one line at 80 columns", func() {
		own, known := pickerRows()
		digest := "sha256:1e74abc" + strings.Repeat("d", 57)
		entries := map[string][]engine.PickerEntry{"acme": {{MarketOffer: engine.MarketOffer{Name: "dir", Version: "1.0.0", Cached: true}, Installed: true, At: digest}}}
		m := newMarketPicker(MarketPickerStep{Own: own, Known: known, Entries: entries, Context: "project /srv/app", ScopeKind: "project"}, plainTheme(), 80, 40)
		m, _ = pick(m, keyEnter, keyEnter)
		Expect(drawn(m)).To(ContainSubstring("installed (sha256:1e74abc) · in your cache\n"))
		Expect(drawn(m)).NotTo(ContainSubstring("1e74abcddd"))
	})

	It("opens on the detail page Resume names, and on the list when it is gone", func() {
		own, known := pickerRows()
		entries := entryPicker(&checker{}).entries
		open := func(r PickerResume) marketPicker {
			return newMarketPicker(MarketPickerStep{Own: own, Known: known, Entries: entries, Resume: r}, plainTheme(), 100, 40)
		}
		Expect(open(PickerResume{Market: "acme", Entry: "review"}).level).To(Equal(detailLevel))
		for _, gone := range []PickerResume{{Market: "acme", Entry: "gone"}, {Market: "gone", Entry: "fmt"}, {Market: "kn", Entry: "fmt"}, {}} {
			Expect(open(gone).level).To(Equal(listLevel), "%v", gone)
		}
		m := open(PickerResume{Market: "acme", Entry: "fmt"})
		Expect(drawn(m)).To(ContainSubstring("Formatter"))
		m, _ = pick(m, keyEsc)
		Expect(drawn(m)).To(ContainSubstring("> [ ] fmt 0.3.0"))
		m, _ = pick(m, keyEsc)
		Expect(drawn(m)).To(ContainSubstring("> [ ] acme"))
	})

	It("draws every page at width 40 and without a width", func() {
		for _, width := range []int{40, 0} {
			own, known := pickerRows()
			entries := entryPicker(&checker{}).entries
			m := newMarketPicker(MarketPickerStep{Own: own, Known: known, Entries: entries, Context: "project /srv/app", ScopeKind: "project"}, plainTheme(), width, 12)
			Expect(func() {
				_ = drawn(m)
				m, _ = pick(m, keyEnter)
				_ = drawn(m)
				m, _ = pick(m, append(presses("/zzz"), keyEnter)...)
				_ = drawn(m)
				m, _ = pick(m, keyEsc, keyEsc, keyEnter, keyEnter)
				_ = drawn(m)
			}).NotTo(Panic())
			Expect(m.level).To(Equal(detailLevel))
		}
	})
})

var _ = Describe("the marketplace picker's entries in accessible mode", func() {
	BeforeEach(func() { testenv.Isolate() })

	// lines is the picker step over entryPicker's entries, read by line prompts answering answers.
	lines := func(entries map[string][]engine.PickerEntry, resume PickerResume, answers ...string) (MarketPickerAnswer, string, error) {
		own, known := pickerRows()
		var out bytes.Buffer
		got, err := accessible(&out, answers...).MarketPicker(MarketPickerStep{Own: own, Known: known, Entries: entries,
			Context: "project /srv/app", ScopeKind: "project", Resume: resume})
		return got, out.String(), err
	}
	entries := func() map[string][]engine.PickerEntry { return entryPicker(&checker{}).entries }

	It("drills into an entry's detail and uninstalls it", func() {
		got, out, err := lines(entries(), PickerResume{}, "1", "2", "d")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(MarketPickerAnswer{Action: PickerUninstall, Market: "acme", Entry: "fmt", Entries: []string{"fmt"}}))
		Expect(out).To(ContainSubstring("project /srv/app"))
		Expect(out).To(ContainSubstring("1. review 1.2.0"))
		Expect(out).To(ContainSubstring("2. fmt 0.3.0 · installed"))
		Expect(out).To(ContainSubstring("i<n,n…> install · d<n,n…> uninstall from project · o<n> web · Enter alone goes back\nChoose an entry (number or key above): "))
		Expect(out).To(ContainSubstring("installed (ccccccc) · in your cache"))
		Expect(out).To(ContainSubstring("d uninstall from project · Enter alone goes back: "))
		Expect(out).NotTo(ContainSubstring("i install"))
	})

	It("installs an entry that is not installed, offering only i", func() {
		got, out, err := lines(entries(), PickerResume{}, "1", "1", "i")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(MarketPickerAnswer{Action: PickerInstall, Market: "acme", Entry: "review", Entries: []string{"review"}}))
		Expect(out).To(ContainSubstring("i install · Enter alone goes back: "))
		Expect(out).NotTo(ContainSubstring("d uninstall"))
	})

	It("says why the other letter does not apply and asks again", func() {
		got, out, err := lines(entries(), PickerResume{}, "1", "1", "d", "x", "i")
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Action).To(Equal(PickerInstall))
		Expect(out).To(ContainSubstring("not installed; i installs"))
		Expect(strings.Count(out, "i install · Enter alone goes back: ")).To(Equal(3))
		got, out, err = lines(entries(), PickerResume{}, "1", "2", "i", "d")
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Action).To(Equal(PickerUninstall))
		Expect(out).To(ContainSubstring("already installed; d uninstalls"))
	})

	It("goes back one level on an empty answer and then cancels", func() {
		got, out, err := lines(entries(), PickerResume{}, "1", "1", "", "", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(MarketPickerAnswer{Action: PickerCancel}))
		Expect(strings.Count(out, "Choose an entry")).To(Equal(2))
		Expect(strings.Count(out, "Choose marketplaces")).To(Equal(2))
	})

	It("keeps m<N> for the menu answer", func() {
		got, _, err := lines(entries(), PickerResume{}, "m1")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(MarketPickerAnswer{Action: PickerEnter, Own: []string{"acme"}}))
		got, _, err = lines(entries(), PickerResume{}, "m1,2")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(MarketPickerAnswer{Action: PickerEnter, Own: []string{"acme", "beta"}}))
		got, _, err = lines(entries(), PickerResume{}, "m4")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(MarketPickerAnswer{Action: PickerEnter, Known: []string{"kn"}}), "a known row's menu acts as it did")
		got, _, err = lines(entries(), PickerResume{}, "1,2")
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Own).To(Equal([]string{"acme", "beta"}), "several numbers act as they did")
	})

	It("says what m takes when it has no valid number, and asks again", func() {
		for _, answer := range []string{"m", "M", "m,", "m  "} {
			got, out, err := lines(entries(), PickerResume{}, answer, "m1")
			Expect(err).NotTo(HaveOccurred(), answer)
			Expect(got).To(Equal(MarketPickerAnswer{Action: PickerEnter, Own: []string{"acme"}}), answer)
			Expect(strings.Count(out, "m takes numbers: m1, or m1,2\n")).To(Equal(1), answer)
			Expect(strings.Count(out, pickerPrompt)).To(Equal(2), answer)
		}
		got, out, err := lines(entries(), PickerResume{}, "m9", "m1")
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Action).To(Equal(PickerEnter))
		Expect(out).To(ContainSubstring("answer numbers from 1 to"))
		Expect(out).NotTo(ContainSubstring("m takes numbers"))
	})

	It("says a marketplace has no entries and asks again", func() {
		got, out, err := lines(entries(), PickerResume{}, "3", "m")
		Expect(err).To(MatchError(errInputEnded))
		Expect(got).To(Equal(MarketPickerAnswer{}))
		Expect(out).To(ContainSubstring("local has no entries; fetch its catalog with m3 (Update), or lola market update local"))
		Expect(strings.Count(out, "Choose marketplaces")).To(Equal(3))
	})

	It("aborts on Ctrl+C at the entries prompt", func() {
		own, known := pickerRows()
		var out bytes.Buffer
		h := accessible(&out, "1")
		h.in.interrupted = true
		_, err := h.MarketPicker(MarketPickerStep{Own: own, Known: known, Entries: entries(), Context: "project /srv/app", ScopeKind: "project"})
		Expect(err).To(MatchError(ErrAborted))
		Expect(out.String()).To(ContainSubstring("Choose an entry"))
	})

	It("reprints the prompt after a number out of range or not a number, at each level", func() {
		got, out, err := lines(entries(), PickerResume{}, "x", "1", "0", "3", "z", "1", "?", "i")
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Action).To(Equal(PickerInstall))
		Expect(strings.Count(out, "answer a number from 1 to 2")).To(Equal(3))
		Expect(strings.Count(out, "Choose an entry")).To(Equal(4))
		Expect(out).To(ContainSubstring("answer i to install, or Enter to go back"))
	})

	It("reopens at the detail Resume names, going back to its entries", func() {
		got, out, err := lines(entries(), PickerResume{Market: "acme", Entry: "fmt"}, "d")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(MarketPickerAnswer{Action: PickerUninstall, Market: "acme", Entry: "fmt", Entries: []string{"fmt"}}))
		Expect(out).NotTo(ContainSubstring("Choose marketplaces"))
		_, out, _ = lines(entries(), PickerResume{Market: "acme", Entry: "fmt"}, "")
		Expect(out).To(ContainSubstring("Choose an entry"))
	})

	It("refuses when input ends at the entries or the detail prompt", func() {
		for _, answers := range [][]string{{"1"}, {"1", "1"}} {
			_, out, err := lines(entries(), PickerResume{}, answers...)
			Expect(err).To(MatchError(errInputEnded))
			Expect(out).To(HaveSuffix("\n")) // the error prints under the prompt
		}
	})

	It("prints author text on one line without control sequences", func() {
		own, known := pickerRows()
		hostile := map[string][]engine.PickerEntry{"acme": {{MarketOffer: engine.MarketOffer{Name: "evil\x1b[31m\nname", Version: "1\n2",
			Description: "line one\nline two", Tags: []string{"t\x1b[2Jag"}, URL: "h\nost"}, Installed: true, At: "x\ny"}}}
		var out bytes.Buffer
		_, err := accessible(&out, "1", "1").MarketPicker(MarketPickerStep{Own: own, Known: known, Entries: hostile, Context: "ctx\nline"})
		Expect(err).To(MatchError(errInputEnded))
		Expect(out.String()).NotTo(ContainSubstring("\x1b"))
		Expect(out.String()).To(ContainSubstring("1. evil"))
		Expect(out.String()).To(MatchRegexp(`evil\S* name 1 2 · installed\n`))
		Expect(out.String()).To(ContainSubstring("line one line two"))
		Expect(out.String()).To(ContainSubstring("ctx line"))
		Expect(out.String()).To(MatchRegexp(`url: h ost\n`))
	})
})
