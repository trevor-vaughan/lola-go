package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// numbered is n lines, "line 01" to "line n", each ending in a newline.
func numbered(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %02d\n", i)
	}
	return b.String()
}

// press sends the viewer each key, as bubbletea reports them, and returns it.
func press(m viewerModel, ks ...tea.KeyPressMsg) viewerModel {
	for _, k := range ks {
		next, _ := m.Update(k)
		m = next.(viewerModel)
	}
	return m
}

// quits reports whether the command Update returned for k ends the program.
func quits(m viewerModel, k tea.KeyPressMsg) (viewerModel, bool) {
	next, cmd := m.Update(k)
	if cmd == nil {
		return next.(viewerModel), false
	}
	_, quit := cmd().(tea.QuitMsg)
	return next.(viewerModel), quit
}

var (
	keyJ     = tea.KeyPressMsg{Code: 'j', Text: "j"}
	keyK     = tea.KeyPressMsg{Code: 'k', Text: "k"}
	keyDown  = tea.KeyPressMsg{Code: tea.KeyDown}
	keyUp    = tea.KeyPressMsg{Code: tea.KeyUp}
	keySpace = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	keyB     = tea.KeyPressMsg{Code: 'b', Text: "b"}
	keyPgDn  = tea.KeyPressMsg{Code: tea.KeyPgDown}
	keyPgUp  = tea.KeyPressMsg{Code: tea.KeyPgUp}
	keyG     = tea.KeyPressMsg{Code: 'g', Text: "g"}
	keyShG   = tea.KeyPressMsg{Code: 'g', Text: "G", Mod: tea.ModShift}
	keyHome  = tea.KeyPressMsg{Code: tea.KeyHome}
	keyEnd   = tea.KeyPressMsg{Code: tea.KeyEnd}
	keyQ     = tea.KeyPressMsg{Code: 'q', Text: "q"}
	keyEsc   = tea.KeyPressMsg{Code: tea.KeyEscape}
	keyCtrlC = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	keyY     = tea.KeyPressMsg{Code: 'y', Text: "y"}
	keyC     = tea.KeyPressMsg{Code: 'c', Text: "c"}
	keyN     = tea.KeyPressMsg{Code: 'n', Text: "n"}
	keyShN   = tea.KeyPressMsg{Code: 'n', Text: "N", Mod: tea.ModShift}
	keySlash = tea.KeyPressMsg{Code: '/', Text: "/"}
	keyEnter = tea.KeyPressMsg{Code: tea.KeyEnter}
	keyBksp  = tea.KeyPressMsg{Code: tea.KeyBackspace}
	keyCtrlN = tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl}
	keyCtrlP = tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl}
)

// typed is the key presses of text, one per character.
func typed(text string) []tea.KeyPressMsg {
	var ks []tea.KeyPressMsg
	for _, r := range text {
		ks = append(ks, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return ks
}

// search types term after /, and Enter.
func search(m viewerModel, term string) viewerModel {
	return press(m, slices.Concat([]tea.KeyPressMsg{keySlash}, typed(term), []tea.KeyPressMsg{keyEnter})...)
}

// sgr matches every SGR escape sequence: color, bold and resets alike.
var sgr = regexp.MustCompile("\x1b\\[[0-9;]*m")

var _ = Describe("the viewer", func() {
	BeforeEach(func() { testenv.Isolate() })

	// A 30-line text on a 40×10 terminal: 9 lines of text above the footer.
	DescribeTable("scrolls by line, page and end, and its footer says where it is",
		func(want string, ks ...tea.KeyPressMsg) {
			m := press(newViewer(numbered(30), false, 40, 10), ks...)
			Expect(ansiStrip(m.footer())).To(HavePrefix(want))
		},
		Entry("at the start", "lines 1–9 of 30 · 0%"),
		Entry("j", "lines 2–10 of 30 · 4%", keyJ),
		Entry("↓", "lines 2–10 of 30 · 4%", keyDown),
		Entry("k after j j", "lines 2–10 of 30 · 4%", keyJ, keyJ, keyK),
		Entry("↑ after ↓ ↓", "lines 2–10 of 30 · 4%", keyDown, keyDown, keyUp),
		Entry("space", "lines 10–18 of 30 · 42%", keySpace),
		Entry("PgDn", "lines 10–18 of 30 · 42%", keyPgDn),
		Entry("b after space space", "lines 10–18 of 30 · 42%", keySpace, keySpace, keyB),
		Entry("PgUp after PgDn PgDn", "lines 10–18 of 30 · 42%", keyPgDn, keyPgDn, keyPgUp),
		Entry("G", "lines 22–30 of 30 · 100%", keyShG),
		Entry("End", "lines 22–30 of 30 · 100%", keyEnd),
		Entry("g after G", "lines 1–9 of 30 · 0%", keyShG, keyG),
		Entry("Home after End", "lines 1–9 of 30 · 0%", keyEnd, keyHome),
	)

	It("in read mode before a question, the footer says q continues to that question and what asks next", func() {
		m := newViewer(numbered(30), false, 120, 10)
		m.then = "Migrate?"
		Expect(ansiStrip(m.footer())).To(ContainSubstring("q continue: Migrate? is asked next · / search"))
		Expect(ansiStrip(m.footer())).NotTo(ContainSubstring("q quit"))
		Expect(ansiStrip(newViewer(numbered(30), false, 120, 10).footer())).To(ContainSubstring("q quit"))
	})

	It("shows the lines its footer names, and the keys", func() {
		m := press(newViewer(numbered(30), false, 100, 10), keySpace)
		lines := strings.Split(ansiStrip(m.View().Content), "\n")
		Expect(lines).To(HaveLen(10))
		Expect(strings.TrimSpace(lines[0])).To(Equal("line 10"))
		Expect(strings.TrimSpace(lines[8])).To(Equal("line 18"))
		Expect(lines[9]).To(Equal("lines 10–18 of 30 · 42%  q quit · / search · space/b page · g/G top/end"))
		Expect(m.View().AltScreen).To(BeTrue())
	})

	It("wraps the content to the width, and again when the terminal is resized", func() {
		m := newViewer(strings.Repeat("word ", 30)+"\n", false, 20, 4)
		Expect(m.vp.TotalLineCount()).To(Equal(8))
		next, _ := m.Update(tea.WindowSizeMsg{Width: 50, Height: 4})
		m = next.(viewerModel)
		Expect(m.vp.TotalLineCount()).To(Equal(3))
		Expect(ansiStrip(m.footer())).To(HavePrefix("lines 1–3 of 3 · 100%"))
	})

	It("keeps its place in range when a resize shortens the content", func() {
		m := press(newViewer(strings.Repeat("word ", 30), false, 20, 4), keyShG)
		Expect(ansiStrip(m.footer())).To(HavePrefix("lines 6–8 of 8"))
		next, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 4})
		Expect(ansiStrip(next.(viewerModel).footer())).To(HavePrefix("lines 1–1 of 1"))
	})

	DescribeTable("in read mode, q and Esc close it, Ctrl+C cancels, and y, c and n do nothing",
		func(k tea.KeyPressMsg, quit bool, answer viewAnswer) {
			m, q := quits(newViewer(numbered(30), false, 40, 10), k)
			Expect(q).To(Equal(quit))
			Expect(m.Answer()).To(Equal(answer))
		},
		Entry("q", keyQ, true, viewClose),
		Entry("Esc", keyEsc, true, viewClose),
		Entry("Ctrl+C", keyCtrlC, true, viewCancel),
		Entry("y", keyY, false, viewCancel),
		Entry("c", keyC, false, viewCancel),
		Entry("n", keyN, false, viewCancel),
	)

	DescribeTable("in confirm mode, y applies, c asks to change, n and Ctrl+C cancel, q quits and Esc goes back; other keys answer nothing",
		func(k tea.KeyPressMsg, quit bool, answer viewAnswer) {
			m := newViewer(numbered(30), true, 40, 10)
			m.nav = Nav{Back: true}
			m, q := quits(m, k)
			Expect(q).To(Equal(quit))
			Expect(m.Answer()).To(Equal(answer))
		},
		Entry("y", keyY, true, viewApply),
		Entry("c", keyC, true, viewChange),
		Entry("n", keyN, true, viewCancel),
		Entry("q", keyQ, true, viewQuit),
		Entry("Esc", keyEsc, true, viewBack),
		Entry("Ctrl+C", keyCtrlC, true, viewCancel),
		Entry("Enter, which is no default answer", tea.KeyPressMsg{Code: tea.KeyEnter}, false, viewCancel),
		Entry("j", keyJ, false, viewCancel),
	)

	It("in confirm mode with no screen to go back to, Esc says q quits and stays", func() {
		m, q := quits(newViewer(numbered(3), true, 100, 10), keyEsc)
		Expect(q).To(BeFalse())
		Expect(ansiStrip(m.footer())).To(HaveSuffix("  first step — q quits"))
		m = press(m, keyJ)
		Expect(ansiStrip(m.footer())).NotTo(ContainSubstring("first step"), "the next key brings the keys back")
		Expect(ansiStrip(m.footer())).NotTo(ContainSubstring("esc back"))
	})

	It("in confirm mode with two or more steps answered, q asks first: y quits; n, Enter and Esc go back to the review", func() {
		m := newViewer(numbered(30), true, 200, 10)
		m.nav = Nav{Back: true, Answered: 2}
		m = press(m, keyJ)
		m, q := quits(m, keyQ)
		Expect(q).To(BeFalse())
		Expect(ansiStrip(m.footer())).To(HaveSuffix("  Quit? your answers so far are dropped — nothing has been changed [y/N]"))
		for _, no := range []tea.KeyPressMsg{keyN, {Code: tea.KeyEnter}, keyEsc} {
			back, q := quits(m, no)
			Expect(q).To(BeFalse(), no.String())
			Expect(back.vp.YOffset()).To(Equal(1), "the review keeps its place")
			Expect(ansiStrip(back.footer())).To(ContainSubstring("y apply"))
		}
		m, q = quits(m, keyY)
		Expect(q).To(BeTrue())
		Expect(m.Answer()).To(Equal(viewQuit))

		m = newViewer(numbered(30), true, 200, 10)
		m.nav = Nav{Back: true, Answered: 1}
		m, q = quits(m, keyQ)
		Expect(q).To(BeTrue(), "with one step answered, q quits at once")
		Expect(m.Answer()).To(Equal(viewQuit))
	})

	It("names the answers first in a confirm-mode footer, then esc back and q quit, then the scrolling keys", func() {
		m := newViewer(numbered(3), true, 200, 10)
		m.nav = Nav{Back: true}
		Expect(ansiStrip(m.footer())).To(Equal(
			"lines 1–3 of 3 · 100%  y apply · c change · n cancel · esc back · q quit · / search · space/b page · g/G top/end"))
		Expect(ansiStrip(newViewer(numbered(3), true, 200, 10).footer())).To(Equal(
			"lines 1–3 of 3 · 100%  y apply · c change · n cancel · q quit · / search · space/b page · g/G top/end"),
			"with no screen to go back to, no esc back")
	})

	It("without a step to change, offers no c and ignores it", func() {
		m := newViewer(numbered(3), true, 120, 10)
		m.noChange = true
		Expect(ansiStrip(m.footer())).To(Equal("lines 1–3 of 3 · 100%  y apply · n cancel · q quit · / search · space/b page · g/G top/end"))
		_, q := quits(m, keyC)
		Expect(q).To(BeFalse(), "c does nothing")
		m, q = quits(m, keyY)
		Expect(q).To(BeTrue())
		Expect(m.Answer()).To(Equal(viewApply))
	})

	It("fits a review's whole footer in 120 columns, and at 80 keeps the answers, esc back and q quit", func() {
		m := press(newViewer(numbered(1000), true, 120, 40), keySpace, keySpace, keySpace)
		m.nav = Nav{Back: true}
		Expect(ansiStrip(m.footer())).To(And(HaveSuffix("/ search · space/b page · g/G top/end"), Not(ContainSubstring("…"))))
		m.resize(80, 40)
		Expect(ansiStrip(m.footer())).To(ContainSubstring("y apply · c change · n cancel · esc back · q quit"))
	})

	It("cuts its footer to the width", func() {
		f := ansiStrip(newViewer(numbered(30), true, 30, 10).footer())
		Expect(f).To(Equal("lines 1–9 of 30 · 0%  y apply…"))
	})

	It("keeps q quit in a read footer at 40 columns", func() {
		Expect(ansiStrip(newViewer(numbered(30), false, 40, 10).footer())).To(Equal("lines 1–9 of 30 · 0%  q quit · / search…"))
	})

	It("without color, styles its footer with bold alone (NO_COLOR, LOLA_COLOR=0)", func() {
		m := newViewer(numbered(30), true, 100, 10)
		Expect(sgr.FindAllString(m.View().Content, -1)).To(HaveExactElements("\x1b[1m", "\x1b[22m"))
		m.s = style{color: true}
		Expect(sgr.FindAllString(m.View().Content, -1)).To(ContainElement("\x1b[36m"), "with color, the keys are colored")
	})
})

// keys is a terminal's input: the keystrokes typed, then nothing more until the spec ends, as a
// terminal waits for the next key.
func keys(typed string) io.Reader {
	r, w, err := os.Pipe()
	Expect(err).NotTo(HaveOccurred())
	_, err = w.WriteString(typed)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() {
		Expect(w.Close()).To(Succeed())
		Expect(r.Close()).To(Succeed())
	})
	return r
}

// altScreen is the sequence that enters the alternate screen, which only the viewer writes.
const altScreen = "\x1b[?1049h"

var _ = Describe("a.view, the viewer on the terminal", func() {
	var out, errb bytes.Buffer
	var cmd *cobra.Command
	BeforeEach(func() {
		testenv.Isolate()
		GinkgoT().Setenv("COLUMNS", "40")
		GinkgoT().Setenv("LINES", "10")
		out.Reset()
		errb.Reset()
		cmd = &cobra.Command{}
		cmd.SetOut(&out)
		cmd.SetErr(&errb)
	})
	a := func() *app { return &app{getenv: os.Getenv} }

	It("in read mode shows the text on stdout, then prints it there whole", func() {
		cmd.SetIn(keys("jjq"))
		ans, err := a().view(cmd, numbered(30), false, false, Nav{}, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(ans).To(Equal(viewClose))
		Expect(out.String()).To(ContainSubstring(altScreen))
		Expect(out.String()).To(HaveSuffix(numbered(30)))
		Expect(errb.String()).To(BeEmpty())
	})

	DescribeTable("in confirm mode asks on stderr, then prints the text there whole, and returns the answer",
		func(typed string, answer viewAnswer) {
			cmd.SetIn(keys(typed))
			ans, err := a().view(cmd, numbered(30), true, true, Nav{Back: true}, "")
			Expect(err).NotTo(HaveOccurred())
			Expect(ans).To(Equal(answer))
			Expect(errb.String()).To(ContainSubstring(altScreen))
			Expect(errb.String()).To(HaveSuffix(numbered(30)))
			Expect(out.String()).To(BeEmpty())
		},
		Entry("y", "y", viewApply),
		Entry("after scrolling, y", "jj y", viewApply),
		Entry("c", "c", viewChange),
		Entry("n", "n", viewCancel),
		Entry("q", "q", viewQuit),
		Entry("Esc", "\x1b", viewBack),
		Entry("Ctrl+C", "\x03", viewCancel),
	)
})

var _ = Describe("which output opens in the viewer", func() {
	var tmp string
	BeforeEach(func() {
		tmp = testenv.Isolate()
		GinkgoT().Setenv("COLUMNS", "80")
	})
	// run runs lola with stdout and stderr terminals as outTTY and errTTY say, the keys typed.
	run := func(outTTY, errTTY bool, typed string, args ...string) (string, string, int) {
		root, err := NewRootCmd(os.Getenv, WithTerminals(outTTY, errTTY))
		Expect(err).NotTo(HaveOccurred())
		var out, errb bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&errb)
		root.SetIn(keys(typed))
		root.SetArgs(args)
		code := Execute(root)
		return out.String(), errb.String(), code
	}
	plain := func(args ...string) string {
		out, _, code := run(false, false, "", args...)
		Expect(code).To(Equal(0))
		return out
	}

	It("opens output taller than the terminal, then prints it, and prints output that fits as it is", func() {
		version := plain("version")
		Expect(version).To(HaveSuffix("\n"))
		Expect(strings.Count(version, "\n")).To(Equal(1))
		GinkgoT().Setenv("LINES", "1")
		out, _, code := run(true, true, "q", "version")
		Expect(code).To(Equal(0))
		Expect(out).To(ContainSubstring(altScreen), "one line does not fit a terminal of one: the prompt needs it")
		Expect(out).To(HaveSuffix(version))

		GinkgoT().Setenv("LINES", "2")
		out, _, code = run(true, true, "", "version")
		Expect(code).To(Equal(0))
		Expect(out).To(Equal(version))
	})

	It("shows --help, help and a bare command group's help taller than the terminal in the viewer", func() {
		GinkgoT().Setenv("LINES", "10")
		for _, args := range [][]string{{"install", "--help"}, {"help", "install"}, {}} {
			want := plain(args...)
			out, _, code := run(true, true, "q", args...)
			Expect(code).To(Equal(0), strings.Join(args, " "))
			Expect(out).To(ContainSubstring(altScreen), strings.Join(args, " "))
			Expect(out).To(HaveSuffix(want), strings.Join(args, " "))
		}
	})

	DescribeTable("prints without the viewer",
		func(outTTY, errTTY bool, setup func(), args ...string) {
			GinkgoT().Setenv("LINES", "10")
			if setup != nil {
				setup()
			}
			want := plain(append([]string{"install", "--help"}, args...)...)
			out, errOut, code := run(outTTY, errTTY, "", append([]string{"install", "--help"}, args...)...)
			Expect(code).To(Equal(0))
			Expect(out).To(Equal(want))
			Expect(errOut).To(BeEmpty())
		},
		Entry("under --plain", true, true, nil, "--plain"),
		Entry("when stdout is not a terminal", false, true, nil),
		Entry("when stderr is not a terminal", true, false, nil),
		Entry("in accessible mode, LOLA_ACCESSIBLE=1", true, true, func() { GinkgoT().Setenv("LOLA_ACCESSIBLE", "1") }),
		Entry("in accessible mode, TERM=dumb", true, true, func() { GinkgoT().Setenv("TERM", "dumb") }),
		Entry("with --no-pager", true, true, nil, "--no-pager"),
		Entry("with NO_PAGER set", true, true, func() { GinkgoT().Setenv("NO_PAGER", "1") }),
		Entry("with pager: false in config.yaml", true, true, func() {
			Expect(os.MkdirAll(filepath.Join(tmp, "config", "lola"), 0o700)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(tmp, "config", "lola", "config.yaml"), []byte("version: 1\npager: false\n"), 0o600)).To(Succeed())
		}),
		Entry("with LOLA_PAGER=cat", true, true, func() { GinkgoT().Setenv("LOLA_PAGER", "cat") }),
		Entry("when the terminal's height is unknown", true, true, func() { GinkgoT().Setenv("LINES", "") }),
	)

	It("prints JSON without the viewer", func() {
		GinkgoT().Setenv("LINES", "1")
		out, _, code := run(true, true, "", "version", "--json")
		Expect(code).To(Equal(0))
		Expect(out).To(HavePrefix("{\n"))
		Expect(out).NotTo(ContainSubstring(altScreen))
	})

	It("never takes $PAGER alone as the pager", func() {
		GinkgoT().Setenv("LINES", "10")
		Expect(os.WriteFile(filepath.Join(tmp, "bin", "fakepager"), []byte("#!/bin/sh\ntouch \"$PAGED\"\n"), 0o755)).To(Succeed())
		GinkgoT().Setenv("PAGED", filepath.Join(tmp, "paged"))
		GinkgoT().Setenv("PAGER", "fakepager")
		out, _, code := run(true, true, "q", "install", "--help")
		Expect(code).To(Equal(0))
		Expect(out).To(ContainSubstring(altScreen))
		Expect(filepath.Join(tmp, "paged")).NotTo(BeAnExistingFile())
	})

	It("prints, and says why, when config.yaml cannot be read", func() {
		GinkgoT().Setenv("LINES", "10")
		Expect(os.MkdirAll(filepath.Join(tmp, "config", "lola"), 0o700)).To(Succeed())
		cfg := filepath.Join(tmp, "config", "lola", "config.yaml")
		Expect(os.WriteFile(cfg, []byte("version: 1\npager: less\n"), 0o600)).To(Succeed())
		want := plain("install", "--help")
		out, errOut, code := run(true, true, "", "install", "--help")
		Expect(code).To(Equal(0))
		Expect(out).To(Equal(want))
		Expect(errOut).To(Equal("⚠ " + cfg + ":2: pager: must be true or false, not a value; printing without the viewer\n" +
			"  hint: write pager: false to print without the viewer\n"))
	})
	Describe("search", func() {
		// 30 lines on a 100×10 terminal; "line 2" holds line 20 to line 29.
		read := func() viewerModel { return newViewer(numbered(30), false, 100, 10) }

		It("jumps to the first match and says which it is, with the line in reverse video", func() {
			m := search(read(), "line 2")
			Expect(ansiStrip(m.footer())).To(HavePrefix("lines 17–25 of 30"))
			Expect(ansiStrip(m.footer())).To(ContainSubstring("/line 2 1 of 10  n/N next/prev"))
			Expect(m.View().Content).To(MatchRegexp("\x1b\\[[0-9;]*7[0-9;]*mline 20"))
			Expect(ansiStrip(m.View().Content)).To(ContainSubstring("line 20"))
		})

		It("steps through the matches with n and N, wrapping at the ends", func() {
			m := search(read(), "line 2")
			m = press(m, keyN)
			Expect(ansiStrip(m.footer())).To(ContainSubstring("/line 2 2 of 10"))
			m = press(m, keyShN, keyShN)
			Expect(ansiStrip(m.footer())).To(ContainSubstring("/line 2 10 of 10"), "N from the first wraps to the last")
			m = press(m, keyN)
			Expect(ansiStrip(m.footer())).To(ContainSubstring("/line 2 1 of 10"), "n from the last wraps to the first")
		})

		It("starts at the first match below the top line", func() {
			m := search(press(read(), keyShG), "line 1")
			Expect(ansiStrip(m.footer())).To(ContainSubstring("/line 1 1 of 10"), "none below, so the first")
			m = search(press(read(), keySpace, keySpace), "line 1")
			Expect(ansiStrip(m.footer())).To(ContainSubstring("/line 1 10 of 10"), "line 19 is at the top, and is the last match")
		})

		DescribeTable("matches any case for a lowercase term, and exactly for one with a capital",
			func(term, want string) {
				m := search(newViewer("Alpha\nbeta\n", false, 40, 10), term)
				Expect(ansiStrip(m.footer())).To(ContainSubstring(want))
			},
			Entry("lowercase", "alpha", "/alpha 1 of 1"),
			Entry("exact capital", "Alpha", "/Alpha 1 of 1"),
			Entry("wrong capitals", "ALPHA", "/ALPHA no match"),
			Entry("absent", "gamma", "/gamma no match"),
		)

		It("takes q and the other keys as text while typing, and Esc drops the typing", func() {
			m := press(read(), append([]tea.KeyPressMsg{keySlash}, typed("qn")...)...)
			Expect(ansiStrip(m.footer())).To(ContainSubstring("/qn█"))
			m = press(m, keyBksp)
			Expect(ansiStrip(m.footer())).To(ContainSubstring("/q█"))
			m, quit := quits(m, keyQ)
			Expect(quit).To(BeFalse())
			m = press(m, keyEsc)
			Expect(ansiStrip(m.footer())).To(HaveSuffix("q quit · / search · space/b page · g/G top/end"))
			Expect(m.term).To(BeEmpty())
		})

		It("clears the search for an empty term", func() {
			m := search(search(read(), "line 2"), "")
			Expect(m.term).To(BeEmpty())
			Expect(m.View().Content).NotTo(ContainSubstring("\x1b[7m"))
		})

		It("keeps the search through a resize", func() {
			m := search(read(), "line 2")
			m.resize(60, 6)
			Expect(m.matches).To(HaveLen(10))
			Expect(ansiStrip(m.footer())).To(ContainSubstring("/line 2 1 of 10"))
		})

		It("leaves n as cancel in confirm mode, where Ctrl+N and Ctrl+P step", func() {
			m := search(newViewer(numbered(30), true, 140, 10), "line 2")
			Expect(ansiStrip(m.footer())).To(ContainSubstring("^n/^p next/prev · y apply"))
			m = press(m, keyCtrlN)
			Expect(ansiStrip(m.footer())).To(ContainSubstring("/line 2 2 of 10"))
			m = press(m, keyCtrlP)
			Expect(ansiStrip(m.footer())).To(ContainSubstring("/line 2 1 of 10"))
			_, quit := quits(m, keyN)
			Expect(quit).To(BeTrue())
		})
	})
})

// ansiStrip is s without its escape sequences.
func ansiStrip(s string) string { return sgr.ReplaceAllString(s, "") }
