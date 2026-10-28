package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
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
)

// sgr matches every SGR escape sequence: color, bold and resets alike.
var sgr = regexp.MustCompile("\x1b\\[[0-9;]*m")

var _ = Describe("the viewer (Plan 10 §2)", func() {
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

	It("shows the lines its footer names, and the keys", func() {
		m := press(newViewer(numbered(30), false, 100, 10), keySpace)
		lines := strings.Split(ansiStrip(m.View().Content), "\n")
		Expect(lines).To(HaveLen(10))
		Expect(strings.TrimSpace(lines[0])).To(Equal("line 10"))
		Expect(strings.TrimSpace(lines[8])).To(Equal("line 18"))
		Expect(lines[9]).To(Equal("lines 10–18 of 30 · 42%  ↑↓/jk line · space/b page · g/G top/end · q quit"))
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

	DescribeTable("in read mode, q, Esc and Ctrl+C quit, and y, c and n do nothing",
		func(k tea.KeyPressMsg, quit bool) {
			m, q := quits(newViewer(numbered(30), false, 40, 10), k)
			Expect(q).To(Equal(quit))
			Expect(m.Answer()).To(Equal(viewCancel))
		},
		Entry("q", keyQ, true),
		Entry("Esc", keyEsc, true),
		Entry("Ctrl+C", keyCtrlC, true),
		Entry("y", keyY, false),
		Entry("c", keyC, false),
		Entry("n", keyN, false),
	)

	DescribeTable("in confirm mode, y applies, c asks to change, and n, q, Esc and Ctrl+C cancel; other keys answer nothing",
		func(k tea.KeyPressMsg, quit bool, answer viewAnswer) {
			m, q := quits(newViewer(numbered(30), true, 40, 10), k)
			Expect(q).To(Equal(quit))
			Expect(m.Answer()).To(Equal(answer))
		},
		Entry("y", keyY, true, viewApply),
		Entry("c", keyC, true, viewChange),
		Entry("n", keyN, true, viewCancel),
		Entry("q", keyQ, true, viewCancel),
		Entry("Esc", keyEsc, true, viewCancel),
		Entry("Ctrl+C", keyCtrlC, true, viewCancel),
		Entry("Enter, which is no default answer", tea.KeyPressMsg{Code: tea.KeyEnter}, false, viewCancel),
		Entry("j", keyJ, false, viewCancel),
	)

	It("names y, c and n in a confirm-mode footer", func() {
		Expect(ansiStrip(newViewer(numbered(3), true, 100, 10).footer())).To(Equal(
			"lines 1–3 of 3 · 100%  ↑↓/jk line · space/b page · g/G top/end · y apply · c change · n cancel"))
	})

	It("cuts its footer to the width", func() {
		f := ansiStrip(newViewer(numbered(30), true, 30, 10).footer())
		Expect(f).To(Equal("lines 1–9 of 30 · 0%  ↑↓/jk l…"))
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

var _ = Describe("a.view, the viewer on the terminal (Plan 10 §2.1)", func() {
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
		ans, err := a().view(cmd, numbered(30), false)
		Expect(err).NotTo(HaveOccurred())
		Expect(ans).To(Equal(viewCancel))
		Expect(out.String()).To(ContainSubstring(altScreen))
		Expect(out.String()).To(HaveSuffix(numbered(30)))
		Expect(errb.String()).To(BeEmpty())
	})

	DescribeTable("in confirm mode asks on stderr, then prints the text there whole, and returns the answer",
		func(typed string, answer viewAnswer) {
			cmd.SetIn(keys(typed))
			ans, err := a().view(cmd, numbered(30), true)
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
		Entry("q", "q", viewCancel),
		Entry("Esc", "\x1b", viewCancel),
		Entry("Ctrl+C", "\x03", viewCancel),
	)
})

var _ = Describe("which output opens in the viewer (Plan 10 §2.1)", func() {
	var tmp string
	BeforeEach(func() {
		tmp = testenv.Isolate()
		GinkgoT().Setenv("COLUMNS", "80")
	})
	// run runs lola with stdout and stderr terminals as outTTY and errTTY say, the keys typed.
	run := func(outTTY, errTTY bool, typed string, args ...string) (string, string, int) {
		root := NewRootCmd(os.Getenv, WithTerminals(outTTY, errTTY))
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
})

// ansiStrip is s without its escape sequences.
func ansiStrip(s string) string { return sgr.ReplaceAllString(s, "") }
