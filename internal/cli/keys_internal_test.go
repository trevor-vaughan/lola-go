package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"github.com/charmbracelet/x/ansi"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// escKey is Esc as a terminal reporting key events sends it: unlike a lone ESC byte, it cannot
// run into the key typed after it.
const escKey = "\x1b[27u"

// onTerminal is the huh adapter on a full terminal, without color, with typed on its input.
func onTerminal(typed string) *huhForms {
	return onInput(keys(typed))
}

// onInput is the huh adapter on a full terminal, without color, reading in.
func onInput(in io.Reader) *huhForms {
	return &huhForms{stdin: in, in: &eofReader{r: strings.NewReader("")}, out: &bytes.Buffer{}, tty: true, height: 40}
}

// paced is a terminal's input typed in bursts, a pause after each: huh moves between a form's
// fields, and lola between forms, after the key that asks it, so the keys for the next field or
// form wait for it as a person typing does.
func paced(bursts ...string) io.Reader {
	r, w, err := os.Pipe()
	Expect(err).NotTo(HaveOccurred())
	go func() {
		for _, b := range bursts {
			if _, err := w.WriteString(b); err != nil {
				return
			}
			time.Sleep(150 * time.Millisecond)
		}
	}()
	DeferCleanup(func() {
		Expect(w.Close()).To(Succeed())
		Expect(r.Close()).To(Succeed())
	})
	return r
}

// isQuit reports whether err is the quit q answers, as against a Ctrl+C or a No.
func isQuit(err error) bool {
	var q *quitError
	return errors.As(err, &q)
}

// shown is what a form draws after it gets each key, without styling.
func shown(f *huh.Form, ks ...tea.KeyPressMsg) string {
	for _, k := range ks {
		f.Update(k)
	}
	return ansi.Strip(f.View())
}

var _ = Describe("lola's keys on the full-screen forms: q quits, Esc goes back a screen", func() {
	BeforeEach(func() { testenv.Isolate() })

	two := AssistantStep{Options: []string{"a", "b"}}
	// form is the assistant picker of st as runIn draws it, ready for keys.
	form := func(st AssistantStep) *huh.Form {
		h := &huhForms{in: &eofReader{r: strings.NewReader("")}, height: 40}
		var v []string
		f, _ := h.form(huh.ThemeFunc(func(bool) *huh.Styles { return plainTheme() }), st.Nav,
			h.required(h.multi("Install for which assistants?", "", []huh.Option[string]{huh.NewOption("a", "a"), huh.NewOption("b", "b")}, &v)))
		f.Init()
		return f
	}

	DescribeTable("q quits a picker at once when fewer than two steps are answered, as a quit",
		func(answered int) {
			st := two
			st.Nav = Nav{Back: true, Answered: answered}
			_, err := onTerminal("q").Assistants(st)
			Expect(err).To(MatchError(ErrAborted), "a quit cancels as Ctrl+C does")
			Expect(isQuit(err)).To(BeTrue())
		},
		Entry("none answered", 0),
		Entry("one answered", 1),
	)

	It("never takes keys typed after Esc or q in the same read as an answer", func() {
		st := AssistantStep{Options: []string{"a", "b"}, Preselect: []string{"a"}, Nav: Nav{Back: true}}
		for range 5 {
			_, err := onTerminal(escKey + "\r").Assistants(st)
			Expect(err).To(MatchError(ErrBack), "Esc, then Enter before the form ended")
			_, err = onTerminal("q\r").Assistants(st)
			Expect(isQuit(err)).To(BeTrue(), "q, then Enter before the form ended")
		}

		for _, end := range []tea.KeyPressMsg{keyEsc, keyQ} {
			f := form(st)
			f.Update(tea.KeyPressMsg{Code: 'x', Text: "x"}) // a pick, so Enter would submit
			f.Update(end)
			_, cmd := f.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			Expect(cmd).To(BeNil(), "once %s ended the form, Enter reaches no field", end.String())
		}
	})

	It("preselects a migrate assistant question asked again exactly as answered, none included", func() {
		st := MigrateTargetStep{Old: "claude-code", Supported: true, Modules: 1, Options: []string{"a", "b"}}
		got, err := onTerminal("\r").MigrateTargets(st)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal([]string{"a", "b"}), "asked first, every assistant is preselected")
		st.Again, st.Preselect = true, nil
		got, err = onTerminal("\r").MigrateTargets(st)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(BeEmpty(), "answered none before, none is preselected")
	})

	It("leaves Ctrl+C as it was: it cancels at once, without asking, and is no quit", func() {
		st := two
		st.Nav = Nav{Back: true, Answered: 5}
		_, err := onTerminal("x\x03").Assistants(st)
		Expect(err).To(MatchError(ErrAborted))
		Expect(isQuit(err)).To(BeFalse())
	})

	It("with two or more steps answered, q asks first: y quits; n, Enter and Esc go back to the screen as it was", func() {
		st := two
		st.Nav = Nav{Back: true, Answered: 2}
		_, err := onTerminal("xqy").Assistants(st)
		Expect(isQuit(err)).To(BeTrue(), "y quits")
		for _, no := range []string{"n", "\r", escKey} {
			got, err := onTerminal("xq" + no + "\r").Assistants(st)
			Expect(err).NotTo(HaveOccurred(), "%q", no)
			Expect(got).To(Equal([]string{"a"}), "%q keeps the tick made before q", no)
		}

		f := form(st)
		Expect(shown(f)).NotTo(ContainSubstring("Quit?"))
		Expect(shown(f, keyQ)).To(ContainSubstring(quitQuestion+" [y/N]"), "the question is in the key help's place")
		Expect(shown(f, keyN)).NotTo(ContainSubstring("Quit?"))
		Expect(shown(f)).To(ContainSubstring("q quit"))
	})

	It("asks before quitting a flow's closing yes/no question once two steps are answered; a question that stands alone quits at once", func() {
		ok, err := onTerminal("qny").Confirm("Move 1 item to the trash?", Nav{Answered: 2})
		Expect(err).NotTo(HaveOccurred(), "q asked, n went back to the question, and y answered it")
		Expect(ok).To(BeTrue())
		_, err = onTerminal("qy").Confirm("Move 1 item to the trash?", Nav{Answered: 2})
		Expect(isQuit(err)).To(BeTrue(), "y quits")
		_, err = onTerminal("q").Confirm("Remove these from your cache?", Nav{})
		Expect(isQuit(err)).To(BeTrue(), "a question that stands alone quits at once")

		h := &huhForms{in: &eofReader{r: strings.NewReader("")}}
		v := false
		f, _ := h.form(huh.ThemeFunc(func(bool) *huh.Styles { return plainTheme() }), Nav{Answered: 2}, huh.NewConfirm().Title("Migrate?").Value(&v))
		f.Init()
		Expect(shown(f, keyEsc)).To(ContainSubstring("no step to go back to — q quits"), "past the first step, Esc does not call this the first step")
	})

	It("asks before quitting a later list of a form when the lists before it make two answers", func() {
		pg := ComponentPage{Module: "m", Title: "m", Nav: Nav{Back: true, Answered: 1}, Groups: []ComponentGroup{
			{Kind: "skill", Title: "Skills (1)", Options: []ComponentOption{{ID: "skill/a", Label: "a"}}},
			{Kind: "command", Title: "Commands (1)", Options: []ComponentOption{{ID: "command/b", Label: "b"}}}}}
		_, err := onTerminal("q").Components(pg)
		Expect(isQuit(err)).To(BeTrue(), "the first list: one step answered")
		got, err := onInput(paced("\r", "qn\r")).Components(pg)
		Expect(err).NotTo(HaveOccurred(), "the second list asks, and n stays")
		Expect(got).To(BeEmpty())
	})

	It("types q in a filter and in a text input; Esc leaves the filter first, then q quits", func() {
		got, err := onTerminal("/q\rx\r").Profiles(ProfileStep{Source: "s", Names: []string{"aq", "b"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal([]string{"aq"}), "q filtered the list to aq, which x ticked")

		st := two
		st.Nav = Nav{Back: true}
		_, err = onTerminal("/b" + escKey + escKey + "q").Assistants(st)
		Expect(isQuit(err)).To(BeTrue(), "Esc set the filter, Esc cleared it, neither went back, and q quit")

		dir, err := onTerminal("q\r").OtherDirectory(DirectoryStep{Nav: Nav{Back: true, Answered: 3}})
		Expect(err).NotTo(HaveOccurred())
		Expect(dir).To(Equal("q"))
	})

	It("goes back a screen on Esc, from a picker, a menu and a text input alike", func() {
		st := two
		st.Nav = Nav{Back: true}
		_, err := onTerminal(escKey).Assistants(st)
		Expect(err).To(MatchError(ErrBack))
		_, err = onTerminal(escKey).OtherDirectory(DirectoryStep{Nav: Nav{Back: true}})
		Expect(err).To(MatchError(ErrBack))
		_, err = onTerminal(escKey).MigrateRoot(MigrateRootStep{Old: "/old", Suggest: "/new", Value: "/new", Nav: Nav{Back: true}})
		Expect(err).To(MatchError(ErrBack))
	})

	It("on the first screen, Esc says q quits in the key help, not as an error, and stays", func() {
		got, err := onTerminal(escKey + "x\r").Assistants(two)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal([]string{"a"}))

		f := form(two)
		Expect(shown(f, keyEsc)).To(ContainSubstring("first step — q quits"))
		Expect(shown(f, keyDown)).NotTo(ContainSubstring("first step"), "the next key brings the key help back")
		Expect(shown(f)).To(ContainSubstring("q quit"))

		in := onTerminal(escKey + "here\r")
		dir, err := in.OtherDirectory(DirectoryStep{})
		Expect(err).NotTo(HaveOccurred())
		Expect(dir).To(Equal("here"), "Esc types nothing in a text input")
	})

	It("on a later field of a form, Esc moves to the field before; on its first it goes back a screen", func() {
		pg := ComponentPage{Module: "m", Title: "m", Groups: []ComponentGroup{
			{Kind: "skill", Title: "Skills (1)", Options: []ComponentOption{{ID: "skill/a", Label: "a"}}},
			{Kind: "command", Title: "Commands (1)", Options: []ComponentOption{{ID: "command/b", Label: "b"}}}}}
		got, err := onInput(paced("\r", escKey, "x\r", "\r")).Components(pg)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal([]string{"skill/a"}), "Esc on Commands went back to Skills, where x ticked a")

		pg.Nav = Nav{Back: true}
		_, err = onInput(paced("\r", escKey, escKey)).Components(pg)
		Expect(err).To(MatchError(ErrBack))

		src, err := onInput(paced("x\t", escKey, "y\r", "\r")).addSource(nil, Nav{Back: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(src.Source).To(Equal("xy"), "Esc on Ref went back to Source")
	})

	It("goes back from the source to add to the module picker", func() {
		got, err := onTerminal(escKey).addSource([]string{"kit"}, Nav{Back: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(ModuleAnswer{Names: []string{"kit"}, AddSource: true}), "the picker runs again with kit selected")
	})

	It("goes back from Choose… to the trust question", func() {
		st := TrustStep{Changes: 2, Groups: []TrustGroup{{Title: "Scripts (1)", Options: []TrustOption{{Module: "m", Label: "m"}}}}}
		// kkkj puts the cursor on Choose… and kkk on Trust all, wherever it starts.
		got, err := onInput(paced("kkkj\r", escKey, "kkk\r")).Trust(st)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(engine.TrustAnswer{All: true}))
	})

	It("names esc back and q quit in the key help; the first screen leaves out esc back, a text input q quit", func() {
		st := two
		st.Nav = Nav{Back: true}
		Expect(shown(form(st))).To(ContainSubstring("esc back • q quit"))
		Expect(shown(form(two))).To(ContainSubstring("q quit"))
		Expect(shown(form(two))).NotTo(ContainSubstring("esc back"))

		wide := form(st)
		wide.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
		Expect(shown(wide)).To(ContainSubstring("esc back • q quit • x toggle"), "lola's keys come first, so an 80-column terminal shows them")

		h := &huhForms{in: &eofReader{r: strings.NewReader("")}}
		v := ""
		f, _ := h.form(huh.ThemeFunc(func(bool) *huh.Styles { return plainTheme() }), Nav{Back: true}, huh.NewInput().Title("Other directory").Value(&v))
		f.Init()
		Expect(shown(f)).To(ContainSubstring("esc back"))
		Expect(shown(f)).NotTo(ContainSubstring("q quit"))
		Expect(shown(f, keyEsc)).NotTo(ContainSubstring("first step"))
	})

	It("keeps accessible mode's numbered prompts: q is no answer there, and nothing goes back", func() {
		var out bytes.Buffer
		st := AssistantStep{Options: []string{"a"}, Nav: Nav{Back: true, Answered: 3}}
		got, err := accessible(&out, "q", "1", "0").Assistants(st)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal([]string{"a"}))
	})

	It("prints a quit as cancelled, nothing changed, and exits 0 as Ctrl+C does", func() {
		tmp := testenv.Isolate()
		GinkgoT().Chdir(tmp)
		h := onTerminal("q")
		root := NewRootCmd(os.Getenv, WithPrompter(h))
		var stdout, stderr bytes.Buffer
		root.SetOut(&stdout)
		root.SetErr(&stderr)
		root.SetArgs([]string{"install", "--scope", "user", "-t", "claude-code"})
		Expect(Execute(root)).To(Equal(0))
		Expect(stderr.String()).To(HaveSuffix("✗ cancelled — nothing was changed\n"))

		cmd := &cobra.Command{Use: "x", SilenceErrors: true, SilenceUsage: true, RunE: func(*cobra.Command, []string) error { return &quitError{color: true} }}
		cmd.SetErr(&stderr)
		cmd.SetArgs([]string{})
		stderr.Reset()
		Expect(Execute(cmd)).To(Equal(0))
		Expect(stderr.String()).To(Equal("\x1b[31m✗ cancelled — nothing was changed\x1b[0m\n"), "in color, as style paints it")
	})
})
