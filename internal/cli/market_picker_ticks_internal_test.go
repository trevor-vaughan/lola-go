package cli

import (
	"bytes"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// tickEntries are acme's three entries for the specs below (review and lint not installed, fmt
// installed) and beta's one.
func tickEntries() map[string][]engine.PickerEntry {
	return map[string][]engine.PickerEntry{
		"acme": {
			{MarketOffer: engine.MarketOffer{Install: "@acme/review", Name: "review", Version: "1.2.0", Description: "Code review"}},
			{MarketOffer: engine.MarketOffer{Install: "@acme/fmt", Name: "fmt", Version: "0.3.0", Description: "Formatter"}, Installed: true},
			{MarketOffer: engine.MarketOffer{Install: "@acme/lint", Name: "lint", Version: "2.0.0", Description: "Linter"}},
		},
		"beta": {{MarketOffer: engine.MarketOffer{Install: "@beta/doc", Name: "doc"}}},
	}
}

// tickPicker is the picker over pickerRows with tickEntries, on acme's entries page, width columns wide.
func tickPicker(width int) marketPicker {
	own, known := pickerRows()
	m := newMarketPicker(MarketPickerStep{Own: own, Known: known, Entries: tickEntries(), Context: "project /srv/app", ScopeKind: "project"}, plainTheme(), width, 40)
	m, _ = pick(m, keyEnter)
	Expect(m.level).To(Equal(entriesLevel))
	return m
}

// answered is the names of the entries m answered.
func answered(m marketPicker) []string { return m.answer.Entries }

var _ = Describe("the marketplace picker's entries page ticks", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("draws a checkbox before each entry, its description two spaces past its name, ticked with x and space", func() {
		m := tickPicker(80)
		out := drawn(m)
		Expect(out).To(MatchRegexp(`(?m)^> \[ \] review 1\.2\.0$`))
		Expect(out).To(MatchRegexp(`(?m)^ {8}Code review$`))
		Expect(out).To(MatchRegexp(`(?m)^ {2}\[ \] fmt 0\.3\.0 · installed$`))
		m, _ = pick(m, keyX, keyDown, keySpace)
		out = drawn(m)
		Expect(out).To(MatchRegexp(`(?m)^ {2}\[x\] review 1\.2\.0$`))
		Expect(out).To(MatchRegexp(`(?m)^> \[x\] fmt 0\.3\.0 · installed$`))
		Expect(out).To(MatchRegexp(`(?m)^ {2}\[ \] lint 2\.0\.0$`))
		m, _ = pick(m, keyX)
		Expect(drawn(m)).To(MatchRegexp(`(?m)^> \[ \] fmt `))
	})

	It("keeps a wrapped head and description under the name, past the checkbox", func() {
		own, known := pickerRows()
		es := map[string][]engine.PickerEntry{"acme": {{MarketOffer: engine.MarketOffer{Name: "fmt", Description: strings.Repeat("word ", 30)}}}}
		m := newMarketPicker(MarketPickerStep{Own: own, Known: known, Entries: es}, plainTheme(), 40, 40)
		m, _ = pick(m, keyEnter)
		words := 0
		for l := range strings.SplitSeq(drawn(m), "\n") {
			if strings.Contains(l, "word") {
				Expect(l).To(HavePrefix("        word"))
				Expect(ansi.StringWidth(l)).To(BeNumerically("<=", 40))
				words++
			}
		}
		Expect(words).To(BeNumerically(">", 1))
	})

	It("ticks all the entries the filter shows with ctrl+a, then none, and keeps ticks across a filter and a detail page", func() {
		m := tickPicker(80)
		m, _ = pick(m, append(presses("/i"), keyEnter)...)
		Expect(drawn(m)).To(And(ContainSubstring("review"), ContainSubstring("lint"), Not(ContainSubstring("fmt"))))
		m, _ = pick(m, keyCtrlA)
		m, _ = pick(m, keyEsc)
		out := drawn(m)
		Expect(out).To(MatchRegexp(`(?m)\[x\] review`))
		Expect(out).To(MatchRegexp(`(?m)\[ \] fmt`))
		Expect(out).To(MatchRegexp(`(?m)\[x\] lint`))
		m, _ = pick(m, keyEnter, keyEsc)
		Expect(m.level).To(Equal(entriesLevel))
		Expect(drawn(m)).To(MatchRegexp(`(?m)\[x\] review`))
		m, _ = pick(m, keyCtrlA)
		Expect(drawn(m)).To(MatchRegexp(`(?m)\[x\] fmt`))
		m, _ = pick(m, keyCtrlA)
		Expect(drawn(m)).NotTo(ContainSubstring("[x]"))
	})

	It("drops the ticks when the marketplace is left for another", func() {
		m, _ := pick(tickPicker(80), keyX, keyLeft, keyDown, keyEnter)
		Expect(drawn(m)).To(ContainSubstring("doc"))
		m, _ = pick(m, keyLeft, keyUp, keyEnter)
		Expect(drawn(m)).NotTo(ContainSubstring("[x]"))
	})

	It("offers no checkbox in a preview, and ignores x there", func() {
		m := previewing(&previewer{})
		Expect(drawn(m)).NotTo(ContainSubstring("[ ]"))
		m, _ = pick(m, keyX, keySpace, keyCtrlA)
		Expect(drawn(m)).NotTo(ContainSubstring("[x]"))
		Expect(ansi.Strip(strings.Join(m.entriesFooter(), "\n"))).NotTo(ContainSubstring("x tick"))
	})

	It("answers every ticked entry in page order on i, ticked or not installed, the first as Entry", func() {
		m, cmd := pick(tickPicker(80), keyDown, keyDown, keyX, keyUp, keyUp, keyX, keyI)
		Expect(m.answer).To(Equal(MarketPickerAnswer{Action: PickerInstall, Market: "acme", Entry: "review", Entries: []string{"review", "lint"}, FromEntries: true}))
		Expect(cmd).NotTo(BeNil())

		m, _ = pick(tickPicker(80), keyCtrlA, keyI)
		Expect(answered(m)).To(Equal([]string{"review", "fmt", "lint"}))
	})

	It("acts only on the ticked entries the filter shows, counting the hidden ones in the footer", func() {
		m, _ := pick(tickPicker(80), keyX, keyDown, keyX)
		m, _ = pick(m, append(presses("/fmt"), keyEnter)...)
		Expect(drawn(m)).To(ContainSubstring("2 ticked (1 hidden)"))
		m, _ = pick(m, keyD)
		Expect(answered(m)).To(Equal([]string{"fmt"}))
	})

	It("says ticked entries are hidden, runs nothing and keeps the ticks when none is shown", func() {
		m, _ := pick(tickPicker(80), keyX, keyDown, keyDown, keyX)
		m, _ = pick(m, append(presses("/fmt"), keyEnter)...)
		Expect(drawn(m)).To(ContainSubstring("2 ticked (2 hidden)"))
		for _, key := range []tea.KeyPressMsg{keyI, keyD} {
			m, _ = pick(m, key)
			Expect(m.done).To(BeFalse())
			Expect(drawn(m)).To(ContainSubstring("ticked entries are hidden by the filter; esc clears it"))
		}
		m, _ = pick(m, keyEsc, keyI)
		Expect(answered(m)).To(Equal([]string{"review", "lint"}))
	})

	It("says every ticked entry is installed on i, and runs nothing", func() {
		m, _ := pick(tickPicker(80), keyDown, keyX, keyI)
		Expect(m.done).To(BeFalse())
		Expect(drawn(m)).To(ContainSubstring("already installed; d uninstalls"))
	})

	It("answers the ticked entries that are installed on d, and says when none is, running nothing", func() {
		m, _ := pick(tickPicker(80), keyCtrlA, keyD)
		Expect(m.answer).To(Equal(MarketPickerAnswer{Action: PickerUninstall, Market: "acme", Entry: "fmt", Entries: []string{"fmt"}, FromEntries: true}))

		m, _ = pick(tickPicker(80), keyX, keyD)
		Expect(m.done).To(BeFalse())
		Expect(m.answer).To(BeZero())
		Expect(drawn(m)).To(ContainSubstring("none of the ticked entries is installed"))
	})

	It("refuses d on ticked entries when the picker has no scope", func() {
		own, known := pickerRows()
		m := newMarketPicker(MarketPickerStep{Own: own, Known: known, Entries: tickEntries()}, plainTheme(), 80, 40)
		m, _ = pick(m, keyEnter, keyCtrlA, keyD)
		Expect(m.done).To(BeFalse())
		Expect(drawn(m)).To(ContainSubstring("no scope to read; d is unavailable"))
	})

	It("keeps the cursor's entry for i and d with nothing ticked, answering it as Entries too", func() {
		m, _ := pick(tickPicker(80), keyI)
		Expect(m.answer).To(Equal(MarketPickerAnswer{Action: PickerInstall, Market: "acme", Entry: "review", Entries: []string{"review"}, FromEntries: true}))
		m, _ = pick(tickPicker(80), keyDown, keyD)
		Expect(m.answer).To(Equal(MarketPickerAnswer{Action: PickerUninstall, Market: "acme", Entry: "fmt", Entries: []string{"fmt"}, FromEntries: true}))
	})

	It("names x tick, and with ticks how many and which of i and d apply, in two rows of 80 columns", func() {
		m := tickPicker(80)
		foot := func() string { return ansi.Strip(strings.Join(m.entriesFooter(), "\n")) }
		Expect(foot()).To(ContainSubstring("x tick"))
		Expect(foot()).NotTo(ContainSubstring("ticked"))

		m, _ = pick(m, keyX)
		Expect(foot()).To(And(ContainSubstring("1 ticked"), ContainSubstring("i install"), Not(ContainSubstring("d uninstall"))))
		m, _ = pick(m, keyDown, keyX)
		Expect(foot()).To(And(ContainSubstring("2 ticked"), ContainSubstring("i install"), ContainSubstring("d uninstall from project")))
		m, _ = pick(m, keyUp, keyX)
		Expect(foot()).To(And(ContainSubstring("1 ticked"), Not(ContainSubstring("i install")), ContainSubstring("d uninstall from project")))
		m, _ = pick(m, keyCtrlA, keyCtrlA, keyCtrlA)
		for _, w := range []int{80, 60} {
			m.width = w
			Expect(len(m.entriesFooter())).To(BeNumerically("<=", 2))
			for _, r := range m.entriesFooter() {
				Expect(ansi.StringWidth(r)).To(BeNumerically("<=", w))
			}
		}
		m.width = 80
		for _, key := range []string{"3 ticked", "x tick", "⏎/→ open", "←/esc back", "/ filter", "q quit"} {
			Expect(foot()).To(ContainSubstring(key))
		}
		Expect(foot()).NotTo(ContainSubstring("…"))
	})

	It("leaves the detail page acting on its own entry whatever is ticked", func() {
		m, _ := pick(tickPicker(80), keyX, keyDown, keyDown, keyEnter, keyI)
		Expect(m.answer).To(Equal(MarketPickerAnswer{Action: PickerInstall, Market: "acme", Entry: "lint", Entries: []string{"lint"}}))
	})
})

var _ = Describe("the marketplace picker's accessible entries prompt takes lists", func() {
	BeforeEach(func() { testenv.Isolate() })

	run := func(answers ...string) (MarketPickerAnswer, string, error) {
		own, known := pickerRows()
		var out bytes.Buffer
		got, err := accessible(&out, append([]string{"1"}, answers...)...).MarketPicker(
			MarketPickerStep{Own: own, Known: known, Entries: tickEntries(), Context: "project /srv/app", ScopeKind: "project"})
		return got, out.String(), err
	}

	It("installs the entries i names, in page order, with the first as Entry", func() {
		got, _, err := run("i3,1")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(MarketPickerAnswer{Action: PickerInstall, Market: "acme", Entry: "review", Entries: []string{"review", "lint"}, FromEntries: true}))
		got, _, err = run("i 1, 2 ,3")
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Entries).To(Equal([]string{"review", "fmt", "lint"}))
	})

	It("uninstalls the installed ones d names, and says when none is", func() {
		got, _, err := run("d1,2")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(MarketPickerAnswer{Action: PickerUninstall, Market: "acme", Entry: "fmt", Entries: []string{"fmt"}, FromEntries: true}))
		got, out, err := run("d1,3", "d2")
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Entries).To(Equal([]string{"fmt"}))
		Expect(out).To(ContainSubstring("none of those entries is installed\n"))
	})

	It("says every entry i names is installed, and asks again", func() {
		own, known := pickerRows()
		es := tickEntries()
		es["acme"][0].Installed = true
		var out bytes.Buffer
		got, err := accessible(&out, "1", "i1,2", "i1,2,3").MarketPicker(MarketPickerStep{Own: own, Known: known, Entries: es, ScopeKind: "project"})
		Expect(err).NotTo(HaveOccurred())
		Expect(out.String()).To(ContainSubstring("already installed; d uninstalls\n"))
		Expect(strings.Count(out.String(), "Choose an entry")).To(Equal(2))
		Expect(got.Entries).To(Equal([]string{"review", "fmt", "lint"}))
	})

	It("keeps a single number's own notes", func() {
		_, out, _ := run("i2", "d1", "i1")
		Expect(out).To(And(ContainSubstring("already installed; d uninstalls\n"), ContainSubstring("not installed; i installs\n")))
	})

	It("refuses an out of range or repeated number, an empty list and a list for o, then asks again", func() {
		for _, bad := range []string{"i1,4", "d0,1", "i2,2", "i1,1", "i,", "o1,2", "i1,x"} {
			got, out, err := run(bad, "i1")
			Expect(err).NotTo(HaveOccurred(), bad)
			Expect(got.Entries).To(Equal([]string{"review"}), bad)
			Expect(strings.Count(out, "Choose an entry")).To(Equal(2), bad)
		}
		_, out, _ := run("i1,4", "i2,2", "i,", "o1,2")
		Expect(out).To(And(ContainSubstring("answer numbers from 1 to 3, comma separated\n"), ContainSubstring("each number once\n"),
			ContainSubstring("i takes entry numbers: i1 or i1,3\n"), ContainSubstring("answer a number from 1 to 3\n")))
	})

	It("names the lists on the keys line", func() {
		_, out, _ := run()
		Expect(out).To(ContainSubstring("i<n,n…> install · d<n,n…> uninstall from project · o<n> web · Enter alone goes back\n"))
	})
})
