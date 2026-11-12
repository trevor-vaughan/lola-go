package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("parseAnswer", func() {
	BeforeEach(func() { testenv.Isolate() })

	DescribeTable("reads what was typed against the answers a question takes",
		func(input, answers string, want rune, ok bool) {
			got, gotOK := parseAnswer(input, answers)
			Expect(gotOK).To(Equal(ok))
			Expect(got).To(Equal(want))
		},
		Entry("empty, with a default", "", "yN", 'n', true),
		Entry("empty, without a default", "", "ycn", rune(0), false),
		Entry("whitespace alone, with a default", "  \t", "yN", 'n', true),
		Entry("y", "y", "yN", 'y', true),
		Entry("Y", "Y", "yN", 'y', true),
		Entry("n", "n", "yN", 'n', true),
		Entry("N, the default's own letter", "N", "yN", 'n', true),
		Entry("c, when allowed", "c", "ycn", 'c', true),
		Entry("c, when not allowed", "c", "yN", rune(0), false),
		Entry("yes", "yes", "yN", 'y', true),
		Entry("No", "No", "yN", 'n', true),
		Entry("change", "change", "ycn", 'c', true),
		Entry("change, when c is not allowed", "change", "yn", rune(0), false),
		Entry("surrounding whitespace", "  y \t", "yN", 'y', true),
		Entry("another word", "maybe", "yN", rune(0), false),
		Entry("two letters", "yn", "yN", rune(0), false),
	)
})

var _ = Describe("a question's words", func() {
	BeforeEach(func() { testenv.Isolate() })

	DescribeTable("brackets its answers, the default in capitals",
		func(answers, want string) { Expect(answerBrackets(answers)).To(Equal(want)) },
		Entry("yes or no", "yN", "[y/N]"),
		Entry("the review", "ycn", "[y/c/n]"),
		Entry("the review, nothing to change", "yn", "[y/n]"),
	)

	DescribeTable("hints the letters it takes, and what Enter alone means",
		func(answers, want string) { Expect(answerHint(answers)).To(Equal(want)) },
		Entry("yes or no", "yN", "answer y or n (Enter means no)"),
		Entry("the review", "ycn", "answer y, c or n"),
		Entry("the review, nothing to change", "yn", "answer y or n"),
	)
})

var _ = Describe("fits", func() {
	BeforeEach(func() { testenv.Isolate() })

	DescribeTable("counts the text wrapped to the width, then the rows under it",
		func(text string, rows, width, height int, want bool) {
			Expect(fits(text, rows, width, height)).To(Equal(want))
		},
		Entry("text and one row fill the terminal", numbered(7), 1, 80, 8, true),
		Entry("one line more", numbered(8), 1, 80, 8, false),
		Entry("a line wrapped to two, and a row", strings.Repeat("x", 100)+"\n", 1, 80, 3, true),
		Entry("a line wrapped to two, and two rows", strings.Repeat("x", 100)+"\n", 2, 80, 3, false),
		Entry("no text", "", 1, 80, 1, true),
	)
})

var _ = Describe("the viewer's key line", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("names each mode's keys in table order, what Enter alone answers first and search last", func() {
		Expect(keyLineFor(keysRead, nil, "")).To(Equal("q quit · ↑↓ space b  scroll · g G  top/end · /  search"))
		Expect(keyLineFor(keysContinue, nil, "Restore which?")).To(Equal(
			"q  continue to: Restore which? · ↑↓ space b  scroll · g G  top/end · /  search"))
		q := Question{Prompt: "Uninstall kit?", Answers: "yN"}
		Expect(keyLineFor(keysPrompt, &q, "")).To(Equal(
			"⏎ alone = no · q quit · ↑↓ space b  scroll · g G  top/end · /  search"))
		q.Answers = "ycn"
		Expect(keyLineFor(keysPrompt, &q, "")).To(Equal("q quit · ↑↓ space b  scroll · g G  top/end · /  search"),
			"no default: nothing to say about Enter alone")
	})
})

var _ = Describe("promptRows", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("counts a prompt line at its widest, its hint included", func() {
		// "ppp…p [y/N] y█" is 39 columns; with "  answer y or n (Enter means no)" 71.
		q := Question{Prompt: strings.Repeat("p", 30), Answers: "yN"}
		Expect(promptRows(q, 80)).To(Equal(1))
		Expect(promptRows(q, 60)).To(Equal(2), "the hint wraps")
		Expect(fits(numbered(6), promptRows(q, 60), 60, 8)).To(BeTrue())
		Expect(fits(numbered(7), promptRows(q, 60), 60, 8)).To(BeFalse(), "the wrapped hint counts")
	})
})

var _ = Describe("Ask, a yes/no question", func() {
	var out bytes.Buffer
	BeforeEach(func() {
		testenv.Isolate()
		out.Reset()
	})
	plan := Question{Show: func(w io.Writer, _ style) { fmt.Fprintln(w, "plan: remove kit") }, Prompt: "Uninstall kit?", Answers: "yN"}

	It("in accessible mode prints the question's text, then a [y/N] line prompt read through the parser", func() {
		ans, err := accessible(&out, "maybe", "Yes").Ask(plan)
		Expect(err).NotTo(HaveOccurred())
		Expect(ans).To(Equal('y'))
		Expect(out.String()).To(Equal("plan: remove kit\nUninstall kit? [y/N] answer y or n (Enter means no)\nUninstall kit? [y/N] "))
	})

	It("in accessible mode takes a blank line as the default", func() {
		ans, err := accessible(&out, "").Ask(plan)
		Expect(err).NotTo(HaveOccurred())
		Expect(ans).To(Equal('n'))
	})

	It("in accessible mode cancels at end of input, never answering the default", func() {
		_, err := accessible(&out).Ask(plan)
		Expect(err).To(MatchError(ErrAborted))
		Expect(out.String()).To(HaveSuffix("Uninstall kit? [y/N] \n"), "the cancel prints under the prompt, not beside it")
	})

	It("in accessible mode cancels an answer that input ends before its newline", func() {
		h := &huhForms{in: &eofReader{r: strings.NewReader("y")}, out: &out, tty: true, accessible: true}
		_, err := h.Ask(plan)
		Expect(err).To(MatchError(ErrAborted))
	})

	It("on a full terminal asks through the terminal's question", func() {
		var got Question
		h := &huhForms{tty: true, out: &out, ask: func(q Question) (rune, error) { got = q; return 'n', nil }}
		ans, err := h.Ask(plan)
		Expect(err).NotTo(HaveOccurred())
		Expect(ans).To(Equal('n'))
		Expect(got.Prompt).To(Equal("Uninstall kit?"))
		Expect(out.String()).To(BeEmpty(), "the terminal's question prints the text itself")
	})

	It("off a terminal never asks", func() {
		_, err := noPrompter{}.Ask(plan)
		Expect(err).To(MatchError(errNoTerminal))
	})

	It("askYes asks with No as the default and reports the answer", func() {
		var got Question
		p := &askStub{ans: 'y', got: &got}
		yes, err := askYes(p, Question{Prompt: "Go?"})
		Expect(err).NotTo(HaveOccurred())
		Expect(yes).To(BeTrue())
		Expect(got.Answers).To(Equal("yN"))
		yes, err = askYes(noPrompter{}, plan)
		Expect(yes).To(BeFalse())
		Expect(err).To(MatchError(errNoTerminal))
	})
})

// askStub is a Prompter whose Ask records its question and answers ans.
type askStub struct {
	noPrompter
	ans rune
	got *Question
}

func (s *askStub) Ask(q Question) (rune, error) { *s.got = q; return s.ans, nil }

// asker is a terminal that answers every question yes, keeping each.
type asker struct {
	Prompter
	asked []Question
}

func (*asker) Interactive() bool { return true }
func (p *asker) Ask(q Question) (rune, error) {
	p.asked = append(p.asked, q)
	return 'y', nil
}

var _ = Describe("hook and script consent", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("asks with the hooks, the Claude Code hooks and the script as the question's text", func() {
		p := &asker{}
		a := &app{getenv: os.Getenv, newPrompter: func(*cobra.Command) Prompter { return p }}
		cmd := &cobra.Command{}
		var errb bytes.Buffer
		cmd.SetErr(&errb)
		var opts engine.Options
		a.hookOptions(cmd, &opts)
		ok, err := opts.Ask("kit", []engine.HookScript{{Event: "pre-install", Script: "setup.sh", SHA256: "ab12"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		ok, err = opts.AskRuntimeHooks("kit", []engine.RuntimeHook{{Event: "PreToolUse", Type: "command", Command: "echo hi"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		ok, err = opts.AskScript(engine.ScriptQuestion{Event: engine.PreInstall, Path: "ci/check.sh", Root: "/work/proj", SHA256: "cd34"})
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())

		Expect(p.asked).To(HaveLen(3))
		Expect(p.asked[0].Prompt).To(Equal("Run kit's install hooks?"))
		Expect(ShowText(p.asked[0])).To(Equal("kit has install hooks. They run as you, unconfined:\n" +
			"    pre-install: setup.sh (sha256 ab12)\n" +
			"  Consent covers only the scripts listed, not other files they run; review those too.\n"))
		Expect(p.asked[1].Prompt).To(Equal("Install kit's Claude Code hooks?"))
		Expect(ShowText(p.asked[1])).To(ContainSubstring("    PreToolUse: command\n      runs: echo hi\n"))
		Expect(p.asked[2].Prompt).To(Equal("Run this project's pre-install script?"))
		Expect(ShowText(p.asked[2])).To(ContainSubstring("    ci/check.sh (sha256 cd34)\n"))
		Expect(errb.String()).To(BeEmpty(), "the hooks are printed with their question, never before it")
	})
})
