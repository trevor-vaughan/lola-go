package cli

import (
	"bytes"
	"context"
	"strings"

	"github.com/charmbracelet/x/ansi"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// listStep is the picker's step over pickerRows with acme given a description and a web address and
// kn (the known row) its reason; open records the addresses it is asked to launch.
func listStep(open func(string) error) MarketPickerStep {
	own, known := pickerRows()
	own[0].Description = "Acme skills"
	own[0].WebURL = "https://github.com/acme/tools"
	own[1].WebURL = "https://github.com/beta/tools"
	return MarketPickerStep{Own: own, Known: known, OpenURL: open, Context: "project /srv/app", ScopeKind: "project",
		Check: func(context.Context, []engine.PickerRow) <-chan engine.Check { return nil }}
}

var _ = Describe("the marketplace picker's list rows", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("draws each row as its check line, its description, then its address, the last two indented", func() {
		out := drawn(newMarketPicker(listStep(nil), plainTheme(), 100, 40))
		Expect(out).To(MatchRegexp(`> \[ \] acme .*3 entries\n {6}Acme skills\n {6}https://example\.com/acme\.git\n`))
		Expect(out).To(MatchRegexp(`\[ \] beta .*3 entries\n {6}https://example\.com/beta\.git\n`), "no description line for an empty one")
		Expect(out).To(MatchRegexp(`\[ \] kn .*known\n {6}curated skills\n {6}https://example\.com/kn\.git\n`), "a known row's reason is its description")
		Expect(out).NotTo(ContainSubstring(" — "))
	})

	It("keeps a failed check's reason and hint after the address", func() {
		m := newMarketPicker(listStep(nil), plainTheme(), 100, 40)
		m.rows[0].Status, m.reason[0], m.ended = engine.StatusUnreachable, "no route to host", true
		Expect(drawn(m)).To(MatchRegexp(`acme .*unreachable\n {6}Acme skills\n {6}https://example\.com/acme\.git\n {6}no route to host\n {6}→ u retries`))
	})

	It("wraps a description to the width, every line under the indent", func() {
		step := listStep(nil)
		step.Own[0].Description = strings.Repeat("word ", 30)
		out := drawn(newMarketPicker(step, plainTheme(), 40, 60))
		words := 0
		for l := range strings.SplitSeq(out, "\n") {
			if strings.Contains(l, "word") {
				Expect(l).To(HavePrefix("      word"))
				Expect(ansi.StringWidth(l)).To(BeNumerically("<=", 40))
				words++
			}
		}
		Expect(words).To(BeNumerically(">", 1))
	})

	It("draws author text in a description on one line without control sequences", func() {
		step := listStep(nil)
		step.Own[0].Description = "bad\x1b[2Jline\nnext"
		out := drawn(newMarketPicker(step, plainTheme(), 100, 40))
		Expect(out).NotTo(ContainSubstring("\x1b"))
		Expect(out).To(MatchRegexp(`\n {6}bad.*line next\n`))
	})

	It("lists the same three lines under a number in accessible mode", func() {
		var out bytes.Buffer
		_, err := accessible(&out, "").MarketPicker(listStep(nil))
		Expect(err).NotTo(HaveOccurred())
		Expect(out.String()).To(ContainSubstring(" 1. acme"))
		Expect(out.String()).To(MatchRegexp(` 1\. acme .*3 entries\n {4}Acme skills\n {4}https://example\.com/acme\.git\n 2\. beta`))
		Expect(out.String()).To(MatchRegexp(` 4\. kn .*known\n {4}curated skills\n {4}https://example\.com/kn\.git\n 5\. Add a source…`))
		Expect(out.String()).NotTo(ContainSubstring(" — "))
	})
})

var _ = Describe("the marketplace picker's o key on the list", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("launches the cursor row's address from a command, not from the key, then says it opened it", func() {
		var launched []string
		m := newMarketPicker(listStep(func(u string) error { launched = append(launched, u); return nil }), plainTheme(), 80, 40)
		m, cmd := pick(m, keyO)
		Expect(launched).To(BeEmpty(), "Update itself starts nothing")
		Expect(cmd).NotTo(BeNil())
		m, _ = pick(m, cmd())
		Expect(launched).To(Equal([]string{"https://github.com/acme/tools"}))
		Expect(m.done).To(BeFalse())
		Expect(drawn(m)).To(ContainSubstring("opened https://github.com/acme/tools"))
		m, cmd = pick(m, keyDown, keyO)
		m, _ = pick(m, cmd())
		Expect(launched[1]).To(Equal("https://github.com/beta/tools"))
	})

	It("says a row with no web address has none, naming it, and launches nothing", func() {
		launched := false
		m := newMarketPicker(listStep(func(string) error { launched = true; return nil }), plainTheme(), 80, 40)
		m, cmd := pick(m, keyDown, keyDown, keyO) // the folder marketplace
		Expect(cmd).To(BeNil())
		Expect(launched).To(BeFalse())
		Expect(drawn(m)).To(ContainSubstring("no web address for local"))
	})

	It("says there is nothing to open on Add a source…", func() {
		m := newMarketPicker(listStep(func(string) error { Fail("launched"); return nil }), plainTheme(), 80, 40)
		m, cmd := pick(m, keyShG, keyO)
		Expect(cmd).To(BeNil())
		Expect(drawn(m)).To(ContainSubstring("nothing to open on Add a source…"))
	})

	DescribeTable("refuses an address that is not a plain http(s) one, launching nothing",
		func(bad string) {
			step := listStep(func(string) error { Fail("launched"); return nil })
			step.Own[0].WebURL = bad
			m, cmd := pick(newMarketPicker(step, plainTheme(), 80, 40), keyO)
			Expect(cmd).To(BeNil())
			Expect(drawn(m)).To(ContainSubstring("refusing to open"))
			Expect(m.View().Content).NotTo(ContainSubstring("\x1b[31m"))
		},
		Entry("a newline", "https://example.org/a\nb"),
		Entry("an escape sequence", "https://example.org/\x1b[31m"),
		Entry("a file URL", "file:///etc/passwd"),
	)

	It("takes o as a filter character while a filter is typed", func() {
		m := newMarketPicker(listStep(func(string) error { Fail("launched"); return nil }), plainTheme(), 80, 40)
		m, cmd := pick(m, keySlash, keyO)
		Expect(cmd).To(BeNil())
		Expect(m.filter).To(Equal("o"))
	})

	It("names o in the footer, within two rows at 80 columns", func() {
		m := newMarketPicker(listStep(nil), plainTheme(), 80, 40)
		Expect(len(m.footer())).To(BeNumerically("<=", 2))
		Expect(drawn(m)).To(ContainSubstring("o web"))
	})

	Context("in accessible mode", func() {
		run := func(open func(string) error, answers ...string) (MarketPickerAnswer, string, error) {
			var out bytes.Buffer
			got, err := accessible(&out, answers...).MarketPicker(listStep(open))
			return got, out.String(), err
		}

		It("opens row n's address on o<n>, says so, and asks again", func() {
			var launched []string
			got, out, err := run(func(u string) error { launched = append(launched, u); return nil }, "o1", "O2", "")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Action).To(Equal(PickerCancel))
			Expect(launched).To(Equal([]string{"https://github.com/acme/tools", "https://github.com/beta/tools"}))
			Expect(out).To(ContainSubstring("opened https://github.com/acme/tools"))
			Expect(strings.Count(out, "Choose marketplaces")).To(Equal(3))
		})

		It("says what has no address, and what it takes, and launches nothing", func() {
			_, out, err := run(func(string) error { Fail("launched"); return nil }, "o3", "o5", "o", "o9", "")
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(ContainSubstring("no web address for local"))
			Expect(out).To(ContainSubstring("nothing to open on Add a source…"))
			Expect(out).To(ContainSubstring("o takes one number: o1"))
			Expect(out).To(ContainSubstring("answer numbers from 1 to 5"))
		})

		It("names o in the prompt, its keys whole at 40 columns", func() {
			_, out, _ := run(nil, "")
			Expect(out).To(ContainSubstring("o1 for the web page"))
			var narrow bytes.Buffer
			h := accessible(&narrow, "")
			h.width = 40
			_, err := h.MarketPicker(listStep(nil))
			Expect(err).NotTo(HaveOccurred())
			Expect(narrow.String()).To(ContainSubstring("o1 web"))
			for l := range strings.SplitSeq(narrow.String(), "\n") {
				Expect(ansi.StringWidth(l)).To(BeNumerically("<=", 40), l)
			}
		})
	})
})

var _ = Describe("the marketplace picker's entries and detail breaks", func() {
	BeforeEach(func() { testenv.Isolate() })

	// pageOf is the page after opening acme's entries and, with detail, the first entry's.
	pageOf := func(e engine.PickerEntry, detail bool, width int) string {
		own, known := pickerRows()
		step := MarketPickerStep{Own: own, Known: known, Entries: map[string][]engine.PickerEntry{"acme": {e}}, ScopeKind: "project"}
		m, _ := pick(newMarketPicker(step, plainTheme(), width, 60), keyEnter)
		if detail {
			m, _ = pick(m, keyEnter)
		}
		return drawn(m)
	}
	entry := engine.PickerEntry{MarketOffer: engine.MarketOffer{Name: "review", Version: "1.2.0", Description: "Code review",
		Tags: []string{"go"}, URL: "https://example.com/acme.git"}}

	It("starts an entry's description two columns right of its name", func() {
		out := pageOf(entry, false, 80)
		Expect(out).To(MatchRegexp(`(?m)^> \[ \] review 1\.2\.0\n {8}Code review$`))
	})

	It("keeps every wrapped description line under that indent", func() {
		long := entry
		long.Description = strings.Repeat("word ", 30)
		words := 0
		for l := range strings.SplitSeq(pageOf(long, false, 40), "\n") {
			if strings.Contains(l, "word") {
				Expect(l).To(HavePrefix("        word"))
				Expect(ansi.StringWidth(l)).To(BeNumerically("<=", 40))
				words++
			}
		}
		Expect(words).To(BeNumerically(">", 1))
	})

	It("separates the detail page's description, metadata and state with one blank line each", func() {
		out := pageOf(entry, true, 80)
		Expect(out).To(MatchRegexp(`(?m)^review 1\.2\.0\n  Code review\n\n  tags: go\n  url: https://example\.com/acme\.git\n\n  not installed\n`))
	})

	It("leaves out a blank line for a block that is absent, never doubling or leading with one", func() {
		bare := engine.PickerEntry{MarketOffer: engine.MarketOffer{Name: "review", Tags: []string{"go"}}}
		Expect(pageOf(bare, true, 80)).To(MatchRegexp(`(?m)^review\n  tags: go\n\n  not installed\n`))
		plain := engine.PickerEntry{MarketOffer: engine.MarketOffer{Name: "review", Description: "Code review"}}
		Expect(pageOf(plain, true, 80)).To(MatchRegexp(`(?m)^review\n  Code review\n\n  not installed\n`))
		Expect(pageOf(engine.PickerEntry{MarketOffer: engine.MarketOffer{Name: "review"}}, true, 80)).To(MatchRegexp(`(?m)^review\n  not installed\n`))
		Expect(pageOf(entry, true, 80)).NotTo(ContainSubstring("\n\n\n"))
	})

	Context("in accessible mode", func() {
		run := func(e engine.PickerEntry, answers ...string) string {
			own, known := pickerRows()
			var out bytes.Buffer
			_, err := accessible(&out, answers...).MarketPicker(MarketPickerStep{Own: own, Known: known,
				Entries: map[string][]engine.PickerEntry{"acme": {e}}, ScopeKind: "project"})
			Expect(err).NotTo(HaveOccurred())
			return out.String()
		}

		It("indents an entry's description two columns past its number line's name", func() {
			Expect(run(entry, "1", "", "", "")).To(MatchRegexp(` 1\. review 1\.2\.0\n {6}Code review\n`))
		})

		It("prints the detail page's blank lines", func() {
			Expect(run(entry, "1", "1", "", "", "")).To(MatchRegexp(`\nreview 1\.2\.0\n  Code review\n\n  tags: go\n  url: https://example\.com/acme\.git\n\n  not installed\n`))
		})
	})
})
