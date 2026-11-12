package cli

// One spec per row of the viewer and pager, accessible mode and form keys transition tables in
// docs/architecture/tui-flows.md, named "viewer: <from> --<input>--> <to>", "accessible: …" and
// "keys: …", the input followed by the row's guard in parentheses where rows share a from and an
// input. Rows an existing spec pins already:
//
// also pinned (viewer: start --run (--json or --plain)--> printed; viewer: start --run (pager: false)--> printed): pager_test.go "never pages"
// also pinned (viewer: start --run (stdout or stderr not a terminal)--> printed; viewer: start --run (accessible mode)--> printed; viewer: start --run (--no-pager or NO_PAGER)--> printed; viewer: start --run (LOLA_PAGER=cat)--> printed): viewer_internal_test.go "prints without the viewer"
// also pinned (viewer: start --run (config.yaml unreadable)--> printed): viewer_internal_test.go "prints, and says why, when config.yaml cannot be read"
// also pinned (viewer: start --run (taller than the terminal)--> viewer; viewer: start --run (fits, or the height unknown)--> printed): viewer_internal_test.go "opens output taller than the terminal, then prints it, and prints output that fits as it is"
// also pinned (viewer: start --run (a review, the viewer not bypassed)--> review): huh_internal_test.go "opens the review in the viewer when nothing bypasses it"
// also pinned (viewer: pager --exits--> done): pager_test.go "the question comes after the pager exits"
// also pinned (viewer: pager --fails--> printed): pager_test.go "prints the diff unpaged, capped, before the question when the pager fails"
// also pinned (viewer: viewer --scroll--> viewer): viewer_internal_test.go "scrolls by line, page and end, and its footer says where it is"
// also pinned (viewer: viewer --/--> search; viewer: search --answer (a term)--> viewer): viewer_internal_test.go "jumps to the first match and says which it is, with the line in reverse video"
// also pinned (viewer: viewer --n or N--> viewer): viewer_internal_test.go "steps through the matches with n and N, wrapping at the ends"
// also pinned (viewer: viewer --q--> done; viewer: viewer --esc--> done; viewer: viewer --y, c or n--> viewer): viewer_internal_test.go "in read mode, q and Esc close it, Ctrl+C cancels, and y, c and n do nothing"
// also pinned (viewer: viewer --ctrl+c (a question follows)--> cancelled): pager_test.go "ends the sync on Ctrl+C in the trust diff's viewer, before the question"
// also pinned (viewer: search --answer (an empty term)--> viewer): viewer_internal_test.go "clears the search for an empty term"
// also pinned (viewer: search --esc--> viewer; viewer: search --q--> search): viewer_internal_test.go "takes q and the other keys as text while typing, and Esc drops the typing"
// also pinned (viewer: review --y--> apply; viewer: review --c (a step to change)--> change; viewer: review --n--> cancelled; viewer: review --ctrl+c--> cancelled; viewer: review --esc (a screen before)--> back; viewer: review --empty submit--> review): viewer_internal_test.go "in confirm mode, y applies, c asks to change, n and Ctrl+C cancel, q quits and Esc goes back; other keys answer nothing"
// also pinned (viewer: review --c (no step to change)--> review): viewer_internal_test.go "without a step to change, offers no c and ignores it"
// also pinned (viewer: review --q (fewer than two answers)--> cancelled; viewer: review --q (two or more answers)--> quit; viewer: quit --y--> cancelled; viewer: quit --n, enter or esc--> review): viewer_internal_test.go "in confirm mode with two or more steps answered, q asks first: y quits; n, Enter and Esc go back to the review"
// also pinned (viewer: review --esc (no screen before)--> review): viewer_internal_test.go "in confirm mode with no screen to go back to, Esc says q quits and stays"
// also pinned (viewer: review --ctrl+n or ctrl+p--> review): viewer_internal_test.go "leaves n as cancel in confirm mode, where Ctrl+N and Ctrl+P step"
// also pinned (accessible: pick --a number--> pick; accessible: pick --answer--> next): huh_internal_test.go "offers Project, User and Other directory…, in that order, with nothing preselected"
// also pinned (accessible: pick --empty submit (a pick needed)--> pick; accessible: pick --end of input--> cancelled): huh_internal_test.go "a picker that needs a pick refuses none, naming the keys, and still cancels at end of input"
// also pinned (accessible: pick --empty submit (none is an answer)--> next): huh_internal_test.go "asks a bundle's members with its members ticked, and takes ticking none, as lola profile --edit's module page does"
// also pinned (accessible: pick --q--> pick): keys_internal_test.go "keeps accessible mode's numbered prompts: q is no answer there, and nothing goes back"
// also pinned (accessible: choose --answer--> next; accessible: choose --a blank line (a default)--> next): huh_internal_test.go "asks which version to write for as a numbered list, the title, then the scope and note, Enter taking the preselected one"
// also pinned (accessible: choose --end of input--> cancelled): huh_internal_test.go "treats end of input as cancel, not as the default answer"
// also pinned (accessible: choose --ctrl+c--> cancelled): huh_internal_test.go "accessible mode: Ctrl+C at a sync question cancels with exit 0"
// also pinned (accessible: line --answer--> next; accessible: line --end of input--> cancelled): huh_internal_test.go "asks for the Other directory with the reason it is asked again, and cancels at end of input"
// also pinned (accessible: line --a blank line (nothing pre-filled)--> next): huh_internal_test.go "asks where a missing root is, offering your home, and skips on an empty answer"
// also pinned (accessible: yesno --y or n--> next; accessible: yesno --end of input--> cancelled): huh_internal_test.go "aborts a consent prompt at end of input and answers one that is given"
// also pinned (accessible: yesno --a blank line--> next): huh_internal_test.go "asks sync's questions, defaulting to No, and cancels at end of input"
// also pinned (keys: screen --answer--> next; keys: picker --x--> picker; keys: picker --empty submit (none is an answer)--> next; keys: picker --/--> filter): keys_internal_test.go "the ssh host picker filters, x ticks, and Enter answers the raw hosts ticked, none included"
// also pinned (keys: screen --q (fewer than two answers)--> cancelled): keys_internal_test.go "q quits a picker at once when fewer than two steps are answered, as a quit"
// also pinned (keys: screen --q (two or more answers)--> quit; keys: quit --y--> cancelled; keys: quit --n, enter or esc--> screen): keys_internal_test.go "with two or more steps answered, q asks first: y quits; n, Enter and Esc go back to the screen as it was"
// also pinned (keys: screen --esc (a field before it)--> screen): keys_internal_test.go "on a later field of a form, Esc moves to the field before; on its first it goes back a screen"
// also pinned (keys: screen --esc (its first field, a screen before)--> back): keys_internal_test.go "goes back a screen on Esc, from a picker, a menu and a text input alike"
// also pinned (keys: screen --esc (nothing before)--> screen): keys_internal_test.go "on the first screen, Esc says q quits in the key help, not as an error, and stays"
// also pinned (keys: screen --esc (nothing before, answers held)--> screen): keys_internal_test.go "past the first step, Esc on a form's first field says there is no step to go back to"
// also pinned (keys: screen --ctrl+c--> cancelled): keys_internal_test.go "leaves Ctrl+C as it was: it cancels at once, without asking, and is no quit"
// also pinned (keys: screen --end of input--> refused): huh_internal_test.go "ends a full-terminal form with a coaching error when input ends, instead of waiting for keys that never come"
// also pinned (keys: start --a form (in color, the background unknown)--> theme; keys: theme --answer--> screen): theme_internal_test.go "is asked before the form, and its answer stays saved when the form's question is declined"
// also pinned (keys: theme --answer (the save fails)--> screen): theme_internal_test.go "warns when it cannot save the answer, and goes on with it"
// also pinned (keys: theme --esc--> theme): keys_internal_test.go "Esc on the background question stays: it is a first screen"
// also pinned (keys: theme --end of input--> refused): theme_internal_test.go "tells how to skip the question when input ends before it is answered, and saves nothing"
// also pinned (keys: theme --ctrl+c--> cancelled): theme_internal_test.go "cancels when the question is cancelled, and saves nothing"
// also pinned (keys: input --q--> input; keys: filter --q--> filter; keys: filter --enter or esc--> filtered; keys: filtered --esc--> picker): keys_internal_test.go "types q in a filter and in a text input; Esc leaves the filter first, then q quits"
// also pinned (keys: picker --empty submit (a pick needed)--> picker): huh_internal_test.go "a full-screen picker that needs a pick opens with its key help, refuses Enter with nothing ticked, and clears on the next toggle"

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("the viewer's and pager's transitions", func() {
	var tmp string
	BeforeEach(func() {
		tmp = testenv.Isolate()
		GinkgoT().Setenv("COLUMNS", "80")
	})
	// run runs lola on a terminal, the keys typed, and returns stdout, stderr and the exit code.
	run := func(typed string, args ...string) (string, string, int) {
		root, err := NewRootCmd(os.Getenv, WithTerminals(true, true))
		Expect(err).NotTo(HaveOccurred())
		var out, errb bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&errb)
		root.SetIn(keys(typed))
		root.SetArgs(args)
		code := Execute(root)
		return out.String(), errb.String(), code
	}

	It("viewer: start --run (LOLA_PAGER set)--> pager", func() {
		paged := filepath.Join(tmp, "paged")
		Expect(os.WriteFile(filepath.Join(tmp, "bin", "fakepager"), []byte("#!/bin/sh\ncat > \"$PAGED\"\n"), 0o755)).To(Succeed())
		GinkgoT().Setenv("PAGED", paged)
		GinkgoT().Setenv("LOLA_PAGER", "fakepager")
		GinkgoT().Setenv("LINES", "40")
		out, _, code := run("", "version")
		Expect(code).To(Equal(0))
		Expect(out).To(BeEmpty(), "the pager took the output")
		got, err := os.ReadFile(paged)
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.Count(string(got), "\n")).To(Equal(1), "one line, far shorter than the terminal, still goes to the pager")
	})

	It("viewer: viewer --ctrl+c (nothing asked after)--> done", func() {
		GinkgoT().Setenv("LOLA_PAGER", "")
		GinkgoT().Setenv("LINES", "1")
		out, errOut, code := run("\x03", "version")
		Expect(code).To(Equal(0))
		Expect(out).To(ContainSubstring(altScreen), "the viewer opened")
		Expect(out).To(HaveSuffix("\n"), "the text printed after it")
		Expect(errOut).NotTo(ContainSubstring("cancelled"), "with no question after it, Ctrl+C only closes the viewer")
	})

	It("viewer: viewer --resize--> viewer", func() {
		m := search(newViewer(numbered(30), false, 40, 11), "line 1")
		Expect(m.matches).To(Equal([]int{9, 10, 11, 12, 13, 14, 15, 16, 17, 18}))
		Expect(m.matches[m.cur]).To(Equal(9), "line 10, the first match")
		m = press(m, keyJ, keyJ, keyJ, keyJ, keyJ, keyJ)
		Expect(m.vp.YOffset()).To(Equal(12))
		m.resize(80, 13)
		Expect(m.matches[m.cur]).To(Equal(12), "the current match is the first at or below the top line, line 13")
		Expect(ansiStrip(m.footer())).To(ContainSubstring("/line 1 4 of 10"))
	})

	It("viewer: search --ctrl+c--> cancelled", func() {
		m := newViewer(numbered(30), false, 40, 10)
		m.then = "Migrate?"
		m, q := quits(press(m, keySlash), keyCtrlC)
		Expect(q).To(BeTrue())
		Expect(m.Answer()).To(Equal(viewCancel), "a cancel, which ends the command before the question")
	})

	It("viewer: quit --ctrl+c--> cancelled", func() {
		m := newViewer(numbered(30), true, 200, 10)
		m.nav = Nav{Back: true, Answered: 2}
		m, q := quits(m, keyQ)
		Expect(q).To(BeFalse(), "q asks first")
		m, q = quits(m, keyCtrlC)
		Expect(q).To(BeTrue())
		Expect(m.Answer()).To(Equal(viewCancel), "a cancel, not a quit")
	})
})

var _ = Describe("the form keys' transitions", func() {
	BeforeEach(func() { testenv.Isolate() })

	two := AssistantStep{Options: []string{"a", "b"}}
	modules := ModuleStep{Options: []ModuleOption{{Name: "kit", Label: "kit"}, {Name: "docs", Label: "docs"}}, None: true}

	It("keys: quit --ctrl+c--> cancelled", func() {
		st := two
		st.Nav = Nav{Back: true, Answered: 2}
		_, err := onTerminal("xq\x03").Assistants(st)
		Expect(err).To(MatchError(ErrAborted))
		Expect(isQuit(err)).To(BeFalse(), "Ctrl+C at the quit question cancels; it is not its yes")
	})

	It("keys: picker --ctrl+a (any other picker)--> picker", func() {
		got, err := onTerminal("\x01\r").Assistants(two)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal([]string{"a", "b"}))
		got, err = onTerminal("\x01\x01x\r").Assistants(two)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal([]string{"a"}), "the second ctrl+a unticked both, and x ticked a")
	})

	It("keys: filtered --q--> cancelled", func() {
		st := two
		st.Nav = Nav{Back: true}
		_, err := onTerminal("/b" + escKey + "q").Assistants(st)
		Expect(isQuit(err)).To(BeTrue(), "Esc set the filter; q, no longer typed into it, quits")
	})

	It("keys: picker --ctrl+a (a module picker)--> picker", func() {
		got, err := onInput(paced("\x01", "\r", escKey)).Modules(modules)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(ModuleAnswer{Names: []string{"kit", "docs"}}), "every module ticked, Add a source… not")

		got, err = onInput(paced("j", "\x01", "x", "\r", escKey)).Modules(modules)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(ModuleAnswer{Names: []string{"kit"}}), "the cursor stays on docs, which x then unticks")
	})

	It("keeps a tick the user gave Add a source… through ctrl+a", func() {
		// G and x tick Add a source…, the last option; ctrl+a ticks the modules beside it; Enter then
		// asks for the source, whose Esc answers with the picks.
		got, err := onInput(paced("G", "x", "\x01", "\r", escKey)).Modules(modules)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(ModuleAnswer{Names: []string{"kit", "docs"}, AddSource: true}))
	})

	It("keys: picker --ctrl+a (a module picker, every module shown ticked)--> picker", func() {
		got, err := onInput(paced("\x01", "\x01", "\r", escKey)).Modules(modules)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Names).To(BeEmpty(), "the second ctrl+a unticks every module")
		Expect(got.AddSource).To(BeFalse())
	})

	It("keys: picker --ctrl+a (a module picker above marketplace lists)--> picker", func() {
		st := modules
		st.Markets = []ModuleGroup{{Title: "acme", Options: []ModuleOption{{Name: "@acme/fmt", Label: "fmt"}}}}
		got, err := onInput(paced("\x01", "\r", "\r", escKey)).Modules(st)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(ModuleAnswer{Names: []string{"kit", "docs"}}), "the cached modules ticked, Add a source… and the marketplace's not")
	})

	It("keys: filter --ctrl+a--> filter", func() {
		// "o" shows docs and Add a source…, not kit.
		got, err := onInput(paced("/o", "\x01", "\r", "\r", escKey)).Modules(modules)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(ModuleAnswer{Names: []string{"docs"}}), "the shown module ticked, Add a source… not")
	})
})

var _ = Describe("accessible mode's transitions", func() {
	var out bytes.Buffer
	BeforeEach(func() {
		testenv.Isolate()
		out.Reset()
	})

	two := AssistantStep{Options: []string{"a", "b"}}
	review := ReviewStep{Summary: "Summary:\n", Options: []string{ReviewApply, ReviewComponents, ReviewQuit}}

	It("accessible: pick --a blank line--> pick", func() {
		got, err := accessible(&out, "", "1", "0").Assistants(two)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal([]string{"a"}))
		Expect(out.String()).To(ContainSubstring("Invalid: must be a number between 0 and 2"))
	})

	It("accessible: pick --esc--> pick", func() {
		got, err := accessible(&out, "\x1b", "1", "0").Assistants(AssistantStep{Options: []string{"a", "b"}, Nav: Nav{Back: true}})
		Expect(err).NotTo(HaveOccurred(), "Esc goes back nowhere")
		Expect(got).To(Equal([]string{"a"}))
		Expect(out.String()).To(ContainSubstring("Invalid: must be a number between 0 and 2"))
	})

	It("accessible: choose --a blank line (no default)--> choose", func() {
		got, err := accessible(&out, "", "1").Review(review)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(ReviewApply), "Apply needs its number")
		Expect(out.String()).To(ContainSubstring("Invalid: must be a number between 1 and 3"))
		Expect(strings.Count(out.String(), "Enter a number between 1 and 3")).To(Equal(2), "the blank line asks again")
	})

	It("takes no default at the trust question, asked again with Trust all under the cursor", func() {
		st := TrustStep{Changes: 2, Answer: engine.TrustAnswer{All: true}}
		got, err := accessible(&out, "", "1").Trust(st)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(engine.TrustAnswer{All: true}), "trust is given by its number")
		Expect(out.String()).To(ContainSubstring("Invalid: must be a number between 1 and 2"), "a blank line consents to nothing")
		_, err = accessible(&out, "").Trust(TrustStep{Changes: 2})
		Expect(err).To(MatchError(ErrAborted), "a blank line, then end of input: cancelled, never trusted")
	})

	It("accessible: choose --invalid--> choose", func() {
		got, err := accessible(&out, "x", "1").Where(WhereStep{Title: "Restore 1 item from the trash?", Options: []string{"Restore"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(0))
		Expect(out.String()).To(ContainSubstring("Invalid: must be a number between 1 and 2"))
	})

	It("accessible: line --a blank line (a value pre-filled)--> next", func() {
		got, err := accessible(&out, "").OtherDirectory(DirectoryStep{Value: "missing", Problem: "/p/missing does not exist"})
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal("missing"), "a path asked again cannot be emptied")
	})

	It("accessible: yesno --invalid--> yesno", func() {
		ans, err := accessible(&out, "maybe", "y").Ask(Question{Prompt: "Uninstall kit?", Answers: "yN"})
		Expect(err).NotTo(HaveOccurred())
		Expect(ans).To(Equal('y'))
		Expect(out.String()).To(ContainSubstring("answer y or n (Enter means no)"))
	})

	It("takes the first option on a blank line where a select has no preselection, as the marketplace entry and background questions do", func() {
		got, err := accessible(&out, "").MarketEntry(MarketEntryStep{Name: "fmt", Options: []ModuleOption{{Name: "@a/fmt", Label: "@a/fmt"}, {Name: "@b/fmt", Label: "@b/fmt"}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal("@a/fmt"))
		dark, err := accessible(&out, "").Background()
		Expect(err).NotTo(HaveOccurred())
		Expect(dark).To(BeTrue())
	})
})

var _ = Describe("a question's transitions", func() {
	BeforeEach(func() { testenv.Isolate() })

	keyX := tea.KeyPressMsg{Code: 'x', Text: "x"}
	yesNo := Question{Prompt: "Uninstall kit?", Answers: "yN"}
	review := Question{Prompt: "Apply these changes?", Answers: "ycn"}
	// at is q at nav on a 120×10 terminal: inline, or in the viewer over 30 lines of text.
	at := func(q Question, nav Nav, inline bool) viewerModel {
		q.Nav = nav
		text := numbered(30)
		if inline {
			text = ""
		}
		return newPrompt(text, q, inline, 120, 10)
	}

	// The frame bubbletea draws last is the one it erases below when the program ends, so the
	// answered line must end in a newline to stay on the screen.
	DescribeTable("leaves the inline prompt line, answered or not, ending in a newline and without the esc note",
		func(ks []tea.KeyPressMsg, line string) {
			m := at(review, Nav{Back: true}, true)
			for _, k := range ks[:len(ks)-1] {
				m = press(m, k)
			}
			m, q := quits(m, ks[len(ks)-1])
			Expect(q).To(BeTrue())
			Expect(ansiStrip(m.View().Content)).To(Equal(line + "\n"))
		},
		Entry("an answer", []tea.KeyPressMsg{keyY, keyEnter}, "Apply these changes? [y/c/n] y"),
		Entry("esc back", []tea.KeyPressMsg{keyEsc}, "Apply these changes? [y/c/n] "),
		Entry("ctrl+c", []tea.KeyPressMsg{keyN, keyCtrlC}, "Apply these changes? [y/c/n] "),
	)

	It("does not wrap the prompt line to a width it does not know", func() {
		q := Question{Prompt: "Uninstall a module with a long enough name to wrap?", Answers: "yN"}
		Expect(ansiStrip(newPrompt("", q, true, 0, 0).promptView())).To(Equal("Uninstall a module with a long enough name to wrap? [y/N] █"))
		Expect(promptRows(q, 0)).To(Equal(1))
	})

	It("keeps no cursor or note on the prompt line while it waits for an answer", func() {
		m := press(at(review, Nav{Back: true}, true), keyN)
		Expect(ansiStrip(m.View().Content)).To(Equal("Apply these changes? [y/c/n] n█ · esc back"))
	})

	DescribeTable("answers, goes back, quits and cancels as its row says",
		func(m viewerModel, ks []tea.KeyPressMsg, quit bool, answer viewAnswer, field, line string) {
			for _, k := range ks[:len(ks)-1] {
				m = press(m, k)
			}
			m, q := quits(m, ks[len(ks)-1])
			Expect(q).To(Equal(quit))
			Expect(m.Answer()).To(Equal(answer))
			Expect(m.field).To(Equal(field))
			Expect(ansiStrip(m.promptView())).To(ContainSubstring(line))
		},
		Entry("viewer: inline --an answer letter--> inline",
			at(yesNo, Nav{}, true), []tea.KeyPressMsg{keyY, keyN}, false, viewCancel, "n", "Uninstall kit? [y/N] n█"),
		Entry("viewer: inline --other key--> inline",
			at(yesNo, Nav{}, true), []tea.KeyPressMsg{keyX}, false, viewCancel, "", "[y/N] █  answer y or n (Enter means no)"),
		Entry("viewer: inline --backspace--> inline",
			at(yesNo, Nav{}, true), []tea.KeyPressMsg{keyY, keyBksp}, false, viewCancel, "", "[y/N] █"),
		Entry("viewer: inline --enter (a letter typed, or a default)--> answered",
			at(yesNo, Nav{}, true), []tea.KeyPressMsg{keyY, keyEnter}, true, viewAnswered, "y", "Uninstall kit? [y/N] y"),
		Entry("viewer: inline --enter (nothing typed, no default)--> inline",
			at(review, Nav{}, true), []tea.KeyPressMsg{keyEnter}, false, viewCancel, "", "[y/c/n] █  answer y, c or n"),
		Entry("viewer: inline --esc (a screen before)--> back",
			at(yesNo, Nav{Back: true}, true), []tea.KeyPressMsg{keyEsc}, true, viewBack, "", "Uninstall kit? [y/N] "),
		Entry("viewer: inline --esc (no screen before)--> inline",
			at(yesNo, Nav{}, true), []tea.KeyPressMsg{keyEsc}, false, viewCancel, "", "first step — q quits"),
		Entry("Esc with answers held says there is no step to go back to",
			at(yesNo, Nav{Answered: 1}, true), []tea.KeyPressMsg{keyEsc}, false, viewCancel, "", "no step to go back to — q quits"),
		Entry("viewer: inline --q (fewer than two answers)--> cancelled",
			at(yesNo, Nav{Answered: 1}, true), []tea.KeyPressMsg{keyQ}, true, viewQuit, "", "Uninstall kit?"),
		Entry("viewer: inline --q (two or more answers)--> quit",
			at(yesNo, Nav{Answered: 2}, true), []tea.KeyPressMsg{keyQ}, false, viewCancel, "", quitQuestion+" [y/N]"),
		Entry("viewer: inline --ctrl+c--> cancelled",
			at(yesNo, Nav{}, true), []tea.KeyPressMsg{keyY, keyCtrlC}, true, viewCancel, "", "Uninstall kit? [y/N] "),
		Entry("viewer: quit --n, enter or esc (inline)--> inline",
			at(yesNo, Nav{Answered: 2}, true), []tea.KeyPressMsg{keyQ, keyN}, false, viewCancel, "", "[y/N] █"),
		Entry("a capital, or a pasted word, answers by its first letter",
			at(yesNo, Nav{}, true), append(typed("Yes"), keyEnter), true, viewAnswered, "y", "[y/N] y"),
		Entry("viewer: prompt --an answer letter--> prompt",
			at(yesNo, Nav{}, false), []tea.KeyPressMsg{keyY}, false, viewCancel, "y", "[y/N] y█"),
		Entry("viewer: prompt --enter (a letter typed, or a default)--> answered",
			at(yesNo, Nav{}, false), []tea.KeyPressMsg{keyEnter}, true, viewAnswered, "n", "Uninstall kit? [y/N] n"),
		Entry("viewer: prompt --enter (nothing typed, no default)--> prompt",
			at(review, Nav{}, false), []tea.KeyPressMsg{keyEnter}, false, viewCancel, "", "answer y, c or n"),
		Entry("viewer: prompt --esc (a screen before)--> back",
			at(yesNo, Nav{Back: true}, false), []tea.KeyPressMsg{keyEsc}, true, viewBack, "", "Uninstall kit? [y/N] "),
		Entry("viewer: prompt --esc (no screen before)--> prompt",
			at(yesNo, Nav{}, false), []tea.KeyPressMsg{keyEsc}, false, viewCancel, "", "first step — q quits"),
		Entry("viewer: prompt --q (fewer than two answers)--> cancelled",
			at(yesNo, Nav{}, false), []tea.KeyPressMsg{keyQ}, true, viewQuit, "", "Uninstall kit?"),
		Entry("viewer: prompt --q (two or more answers)--> quit",
			at(yesNo, Nav{Answered: 2}, false), []tea.KeyPressMsg{keyQ}, false, viewCancel, "", quitQuestion+" [y/N]"),
		Entry("viewer: prompt --ctrl+c--> cancelled",
			at(yesNo, Nav{}, false), []tea.KeyPressMsg{keyY, keyCtrlC}, true, viewCancel, "", "Uninstall kit? [y/N] "),
		Entry("viewer: quit --n, enter or esc (in the viewer)--> prompt",
			at(yesNo, Nav{Answered: 2}, false), []tea.KeyPressMsg{keyQ, keyEsc}, false, viewCancel, "", "[y/N] █"),
	)

	It("viewer: prompt --scroll--> prompt", func() {
		// 10 rows: 7 of text, the rule, the prompt and the keys.
		m := press(at(yesNo, Nav{}, false), keyJ, keySpace)
		Expect(ansiStrip(m.rule())).To(HavePrefix("━━ lines 9–15 of 30 · ↓ 15 more ━"))
		Expect(m.field).To(BeEmpty())
		r := press(at(review, Nav{}, false), keyEnter)
		Expect(r.vp.YOffset()).To(Equal(0), "Enter never scrolls")
	})

	It("viewer: prompt --/--> search", func() {
		m := search(at(yesNo, Nav{}, false), "line 2")
		Expect(m.term).To(Equal("line 2"))
		Expect(m.field).To(BeEmpty(), "what is typed into the search is no answer")
	})

	It("viewer: search --enter or esc (a question asked)--> prompt", func() {
		m := press(at(yesNo, Nav{}, false), keySlash, keyN, keyEsc)
		Expect(m.searching).To(BeFalse())
		Expect(m.field).To(BeEmpty(), "what is typed into the search is no answer")
		Expect(m.keyLine()).To(HavePrefix("⏎ alone = no"))
		m = search(m, "line 2")
		Expect(m.searching).To(BeFalse())
		Expect(m.field).To(BeEmpty())
		Expect(ansiStrip(m.footer())).To(ContainSubstring("Uninstall kit? [y/N] █"))
		Expect(m.keyLine()).To(HavePrefix("/line 2 1 of 10 · ctrl+n ctrl+p  next/prev · ⏎ alone = no"))
	})

	It("leaves the prompt with nothing in its field when stdin ends", func() {
		m := press(at(yesNo, Nav{}, true), keyY)
		next, cmd := m.Update(stdinEnded{})
		m = next.(viewerModel)
		Expect(cmd).NotTo(BeNil())
		Expect(cmd()).To(BeAssignableToTypeOf(tea.QuitMsg{}))
		Expect(m.Answer()).To(Equal(viewCancel))
		Expect(m.field).To(BeEmpty())
		Expect(ansiStrip(m.promptView())).To(Equal("Uninstall kit? [y/N] "))
	})

	It("viewer: prompt --ctrl+n or ctrl+p--> prompt", func() {
		m := search(at(yesNo, Nav{}, false), "line 2")
		Expect(m.keyLine()).To(ContainSubstring("/line 2 1 of 10 · ctrl+n ctrl+p  next/prev"))
		m = press(m, keyCtrlN)
		Expect(m.keyLine()).To(ContainSubstring("/line 2 2 of 10"))
		m = press(m, keyCtrlP)
		Expect(m.keyLine()).To(ContainSubstring("/line 2 1 of 10"))
		m = press(m, keyN)
		Expect(m.field).To(Equal("n"), "n is an answer here")
		Expect(m.keyLine()).To(ContainSubstring("/line 2 1 of 10"))
	})

	It("viewer: prompt --resize--> prompt", func() {
		q := Question{Prompt: "Remove these and sync? No applies nothing", Answers: "yN", Nav: Nav{Back: true}}
		next, _ := newPrompt(numbered(30), q, false, 120, 10).Update(tea.WindowSizeMsg{Width: 30, Height: 10})
		m := next.(viewerModel)
		rows := promptRows(q, 30)
		Expect(rows).To(BeNumerically(">", 1), "at 30 columns the prompt line wraps")
		lines := strings.Split(ansiStrip(m.View().Content), "\n")
		Expect(lines).To(HaveLen(10), "the screen is the terminal's height")
		Expect(lines[8-rows]).To(HavePrefix("━━ lines"))
		Expect(lines[9]).To(HavePrefix("⏎ alone = no"), "the key line is the last row")
		Expect(strings.Join(lines[9-rows:9], " ")).To(ContainSubstring("nothing"), "the prompt line wraps, never cut")
	})
})

var _ = Describe("a question on the terminal", func() {
	var out, errb bytes.Buffer
	var cmd *cobra.Command
	var tmp string
	BeforeEach(func() {
		tmp = testenv.Isolate()
		GinkgoT().Setenv("LOLA_PAGER", "")
		GinkgoT().Setenv("COLUMNS", "120")
		GinkgoT().Setenv("LINES", "8")
		out.Reset()
		errb.Reset()
		cmd = &cobra.Command{}
		cmd.SetOut(&out)
		cmd.SetErr(&errb)
	})
	a := func() *app { return &app{getenv: os.Getenv, termOut: true, termErr: true} }
	plan := func(n int) Question {
		return Question{Show: func(w io.Writer, _ style) { fmt.Fprint(w, numbered(n)) }, Prompt: "Uninstall kit?", Answers: "yN"}
	}

	It("viewer: start --ask (fits, or the viewer bypassed)--> inline", func() {
		cmd.SetIn(keys("y\r"))
		ans, err := a().ask(cmd, plan(7))
		Expect(err).NotTo(HaveOccurred())
		Expect(ans).To(Equal('y'))
		Expect(errb.String()).To(HavePrefix(numbered(7)), "7 lines and the prompt fill 8 rows: printed, the prompt under them")
		Expect(errb.String()).NotTo(ContainSubstring(altScreen))
		Expect(ansi.Strip(errb.String())).To(ContainSubstring("Uninstall kit? [y/N]"))

		errb.Reset()
		GinkgoT().Setenv("NO_PAGER", "1")
		cmd.SetIn(keys("\r"))
		ans, err = a().ask(cmd, plan(30))
		Expect(err).NotTo(HaveOccurred())
		Expect(ans).To(Equal('n'), "Enter alone answers the default")
		Expect(errb.String()).To(HavePrefix(numbered(30)), "the viewer bypassed: printed, however tall")
		Expect(errb.String()).NotTo(ContainSubstring(altScreen))
	})

	// The 8-row case of the spec: the text and the prompt together decide.
	It("viewer: start --ask (taller than the terminal with its prompt)--> prompt", func() {
		cmd.SetIn(keys("y\r"))
		ans, err := a().ask(cmd, plan(8))
		Expect(err).NotTo(HaveOccurred())
		Expect(ans).To(Equal('y'))
		Expect(errb.String()).To(ContainSubstring(altScreen), "8 lines and the prompt do not fit 8 rows")
		Expect(ansiStrip(errb.String())).To(HaveSuffix(numbered(8)+"Uninstall kit? [y/N] y\n"),
			"the text and the answered prompt line stay in the scrollback")
		Expect(out.String()).To(BeEmpty())
	})

	It("viewer: start --ask (LOLA_PAGER set)--> pager", func() {
		paged := filepath.Join(tmp, "paged")
		Expect(os.WriteFile(filepath.Join(tmp, "bin", "fakepager"), []byte("#!/bin/sh\ncat > \"$PAGED\"\n"), 0o755)).To(Succeed())
		GinkgoT().Setenv("PAGED", paged)
		GinkgoT().Setenv("LOLA_PAGER", "fakepager")
		cmd.SetIn(keys("y\r"))
		ans, err := a().ask(cmd, plan(30))
		Expect(err).NotTo(HaveOccurred())
		Expect(ans).To(Equal('y'), "asked inline once the pager exited")
		Expect(os.ReadFile(paged)).To(Equal([]byte(numbered(30))))
		Expect(errb.String()).NotTo(ContainSubstring("line 01"), "the text went to the pager only")
		Expect(errb.String()).NotTo(ContainSubstring(altScreen))
	})

	It("viewer: inline --end of input--> refused", func() {
		r, w, err := os.Pipe()
		Expect(err).NotTo(HaveOccurred())
		Expect(w.Close()).To(Succeed())
		DeferCleanup(r.Close)
		cmd.SetIn(r)
		_, err = a().ask(cmd, plan(3))
		Expect(err).To(MatchError(errInputEnded))
		Expect(errs.Hints(err)).To(ContainElement("run lola in a terminal that can answer its questions"))
	})

	It("viewer: inline --end of input (a consent question)--> refused, naming --trust, which -y does not answer", func() {
		r, w, err := os.Pipe()
		Expect(err).NotTo(HaveOccurred())
		Expect(w.Close()).To(Succeed())
		DeferCleanup(r.Close)
		cmd.SetIn(r)
		q := plan(3)
		q.Trust = engine.TrustHooks | engine.TrustRuntimeHooks
		_, err = a().ask(cmd, q)
		Expect(err).To(MatchError(errInputEnded))
		Expect(errs.Hints(err)).To(Equal([]string{"run lola in a terminal that can answer its questions",
			"or pass --trust=hooks,runtime-hooks to consent without asking"}))
	})

	It("viewer: prompt --end of input--> refused", func() {
		r, w, err := os.Pipe()
		Expect(err).NotTo(HaveOccurred())
		_, err = w.WriteString("y")
		Expect(err).NotTo(HaveOccurred())
		Expect(w.Close()).To(Succeed())
		DeferCleanup(r.Close)
		cmd.SetIn(r)
		_, err = a().ask(cmd, plan(8))
		Expect(err).To(MatchError(errInputEnded))
		Expect(errb.String()).To(ContainSubstring(altScreen))
		Expect(ansiStrip(errb.String())).To(HaveSuffix(numbered(8)+"Uninstall kit? [y/N] \n"),
			"the text and the unanswered prompt line stay in the scrollback")
	})

	It("asks a question with no text inline, without a blank line, however short the terminal", func() {
		GinkgoT().Setenv("LINES", "1")
		cmd.SetIn(keys("y\r"))
		ans, err := a().ask(cmd, Question{Prompt: "Install kit with no components?", Answers: "yN"})
		Expect(err).NotTo(HaveOccurred())
		Expect(ans).To(Equal('y'))
		Expect(errb.String()).NotTo(ContainSubstring(altScreen))
		Expect(ansi.Strip(errb.String())).NotTo(HavePrefix("\n"))
	})
})
