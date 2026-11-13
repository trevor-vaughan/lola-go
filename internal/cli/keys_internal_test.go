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

// interrupter is a field that z interrupts as a SIGINT from outside does: the program gets an
// interrupt while the form is live, not aborted by huh.
type interrupter struct{ huh.Field }

func (i *interrupter) Update(msg tea.Msg) (huh.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "z" {
		return i, tea.Interrupt
	}
	m, cmd := i.Field.Update(msg)
	i.Field = m.(huh.Field)
	return i, cmd
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
		_, err = onTerminal("\x03").Where(WhereStep{Title: "Restore 1 item from the trash?", Options: []string{"Restore"}, Nav: Nav{Back: true}})
		Expect(err).To(MatchError(ErrAborted))
		Expect(errors.Is(err, ErrCancelled)).To(BeFalse(), "Ctrl+C is not the question's Cancel")
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

	It("past the first step, Esc on a form's first field says there is no step to go back to", func() {
		h := &huhForms{in: &eofReader{r: strings.NewReader("")}}
		v := ""
		f, _ := h.form(huh.ThemeFunc(func(bool) *huh.Styles { return plainTheme() }), Nav{Answered: 2},
			huh.NewSelect[string]().Title("Restore which?").Options(huh.NewOption("a", "a")).Value(&v))
		f.Init()
		Expect(shown(f, keyEsc)).To(ContainSubstring("no step to go back to — q quits"))
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

	It("the ssh host picker filters, x ticks, and Enter answers the raw hosts ticked, none included", func() {
		st := SSHHostStep{Market: "priv", Hosts: []SSHHostChoice{{Host: "git.corp.test", Entries: 2}, {Host: "other.test", Entries: 1}}}
		got, err := onTerminal("/other\rx\r").SSHHosts(st)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal([]string{"other.test"}), "the filter left other.test, which x ticked")
		got, err = onTerminal("x\r").SSHHosts(st)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal([]string{"git.corp.test"}))
		got, err = onTerminal("\r").SSHHosts(st)
		Expect(err).NotTo(HaveOccurred(), "ticking nothing is an answer")
		Expect(got).To(BeEmpty())
		var out bytes.Buffer
		got, err = accessible(&out, "0").SSHHosts(st)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(BeEmpty())
		Expect(out.String()).To(And(ContainSubstring("git.corp.test (2 entries)"), ContainSubstring("other.test (1 entry)")))
		_, err = onTerminal("\x03").SSHHosts(st)
		Expect(err).To(MatchError(ErrAborted))
	})

	It("the trash picker ticks runs and files with x, then Enter asks what to do with them; its key help says what each key does", func() {
		st := TrashPickStep{Options: []TidyOption{{ID: "0", Label: "2026-11-03 12:00:00 UTC  lola tidy  project /p  1 file, 5 B"},
			{ID: "0/0", Label: "    /p/a.md"}}}
		got, err := onInput(paced("x\r", "\r")).TrashPick(st)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(TrashAnswer{Picked: []string{"0"}, Action: TrashRestore}))
		got, err = onInput(paced("jx\r", "j\r")).TrashPick(st)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(TrashAnswer{Picked: []string{"0/0"}, Action: TrashEmpty}))
		again := st
		again.Preselect = []string{"0/0"}
		got, err = onInput(paced("\r", "\r")).TrashPick(again)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Picked).To(Equal([]string{"0/0"}), "the last answer stays ticked")
		again.Action = TrashEmpty
		got, err = onInput(paced("\r", "\r")).TrashPick(again)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Action).To(Equal(TrashEmpty), "and the last action stays under the cursor")
		again.Action = ""
		_, err = onTerminal("\x03").TrashPick(st)
		Expect(err).To(MatchError(ErrAborted))

		By("accessible mode numbers the rows and the actions, and shows why it asks again")
		var out bytes.Buffer
		again.Preselect, again.Problem = nil, "nothing ticked: tick files or runs, then pick what to do with them"
		got, err = accessible(&out, "2", "0", "2").TrashPick(again)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(TrashAnswer{Picked: []string{"0/0"}, Action: TrashEmpty}))
		Expect(out.String()).To(ContainSubstring("nothing ticked: tick files or runs, then pick what to do with them"))
		Expect(out.String()).To(ContainSubstring("1.   2026-11-03 12:00:00 UTC  lola tidy  project /p  1 file, 5 B\n2.       /p/a.md\n"))
		Expect(out.String()).To(And(ContainSubstring("1. Restore ticked"), ContainSubstring("2. Empty ticked runs")))

		By("the full form's key help")
		h := &huhForms{in: &eofReader{r: strings.NewReader("")}, height: 40}
		var picked []string
		var action string
		f, _ := h.form(huh.ThemeFunc(func(bool) *huh.Styles { return plainTheme() }), Nav{}, h.trashFields(st, &picked, &action)...)
		f.Init()
		list := shown(f)
		Expect(list).To(ContainSubstring("Restore or empty which?"))
		Expect(list).To(ContainSubstring("a [whole run] row takes the whole run, whatever of its files is ticked"))
		Expect(list).To(ContainSubstring("enter: next"))
		Expect(list).To(ContainSubstring("q quit • x toggle • ↑ up • ↓ down • / filter • enter confirm"))
		Expect(list).To(And(ContainSubstring("Do what with the ticked?"), ContainSubstring("> Restore ticked"), ContainSubstring("Empty ticked runs")))
	})

	It("tidy: move --esc--> move", func() {
		st := TidyPickStep{Options: []TidyOption{{ID: "0", Label: "claude-code  skill/old  .claude/skills/old/"}}}
		got, err := onInput(paced(escKey, "x\r")).TidyPick(st)
		Expect(err).NotTo(HaveOccurred(), "a first screen, after the scope picker too: Esc stays")
		Expect(got).To(Equal([]string{"0"}))
	})

	It("trash: pick --esc--> pick", func() {
		st := TrashPickStep{Options: []TidyOption{{ID: "0", Label: "2026-11-03 12:00:00 UTC  lola tidy  project /p  1 file, 5 B"}}}
		got, err := onInput(paced(escKey, "x\r", "\r")).TrashPick(st)
		Expect(err).NotTo(HaveOccurred(), "a first screen: Esc stays")
		Expect(got).To(Equal(TrashAnswer{Picked: []string{"0"}, Action: TrashRestore}))
	})

	It("asks the keep picker again with its last answer ticked", func() {
		st := TidyKeepStep{Options: []TidyOption{{ID: "skill/a", Label: "skill/a  claude-code"}, {ID: "skill/b", Label: "skill/b  claude-code"}},
			Preselect: []string{"skill/b"}}
		got, err := onInput(paced("\r")).TidyKeep(st)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal([]string{"skill/b"}))
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
		_, err = onTerminal(escKey).Where(WhereStep{Title: "Restore 1 item from the trash?", Options: []string{"Restore"}, Nav: Nav{Back: true}})
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

		f = form(two)
		Expect(shown(f, keyEnter)).To(ContainSubstring("nothing selected"))
		Expect(shown(f, keyEsc)).To(And(ContainSubstring("first step — q quits"), Not(ContainSubstring("nothing selected"))),
			"Esc's note replaces the empty submit's")

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

	It("profile edit: which --esc--> which", func() {
		got, err := onTerminal(escKey + "\r").Where(WhereStep{Title: "kit names profile kit and a module.", Options: []string{"Edit profile kit", "Edit module kit"}})
		Expect(err).NotTo(HaveOccurred(), "a first screen: Esc stays")
		Expect(got).To(Equal(0), "and Enter then answers it")

		got, err = onTerminal("\r").Where(WhereStep{Title: "New profile try:", Options: []string{"Lists modules", "Bundles profiles"}, Preselect: 1})
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(1), "asked again, the last answer is under the cursor")
	})

	It("profile edit: members --esc (first screen)--> members", func() {
		got, err := onTerminal(escKey + "\r").Members(MembersStep{Title: "bundle work › members",
			Options: []ModuleOption{{Name: "docs", Label: "docs"}, {Name: "go", Label: "go"}}, Preselect: []string{"go"}})
		Expect(err).NotTo(HaveOccurred(), "a first screen: Esc stays")
		Expect(got).To(Equal([]string{"go"}), "and Enter then answers it as ticked")

		_, err = onTerminal(escKey).Members(MembersStep{Title: "new bundle try › members", Options: []ModuleOption{{Name: "go", Label: "go"}}, Nav: Nav{Back: true}})
		Expect(err).To(MatchError(ErrBack), "a new bundle's page goes back to the question before it")
	})

	It("trust: choose --esc (first list)--> trust", func() {
		st := TrustStep{Changes: 2, Groups: []TrustGroup{{Title: "Scripts (1)", Options: []TrustOption{{Module: "m", Label: "m"}, {Module: "n", Label: "n"}}}}}
		got, err := onInput(paced("k\r", "x", escKey, "\r", "\r")).Trust(st) // k from Cancel is Choose…
		Expect(err).NotTo(HaveOccurred(), "Enter on the question opens Choose… again: the cursor stayed on it")
		Expect(got).To(Equal(engine.TrustAnswer{Modules: []string{"m"}}), "and m is still ticked")
	})

	It("asks the trust question again with its last answer under the cursor, the entries Choose… ticked still ticked", func() {
		st := TrustStep{Changes: 2, Answer: engine.TrustAnswer{All: true}, Groups: []TrustGroup{{Title: "Scripts (2)",
			Options: []TrustOption{{Module: "m", Label: "m"}, {Module: "n", Label: "n"}}}}}
		got, err := onInput(paced("\r")).Trust(st)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(engine.TrustAnswer{All: true}))

		st.Answer = engine.TrustAnswer{Modules: []string{"n"}}
		got, err = onInput(paced("\r", "\r")).Trust(st)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(engine.TrustAnswer{Modules: []string{"n"}}))
	})

	It("trust: trust --esc--> trust", func() {
		st := TrustStep{Changes: 1}
		_, err := onInput(paced(escKey, "\r")).Trust(st)
		Expect(err).To(MatchError(ErrAborted), "a first screen: Esc stays, the cursor still on Cancel, which Enter answers")
		Expect(errors.Is(err, ErrBack)).To(BeFalse())
	})

	It("trust: choose --esc (a later list)--> choose", func() {
		st := TrustStep{Changes: 2, Groups: []TrustGroup{{Title: "Added (1)", Options: []TrustOption{{Module: "a", Label: "a"}}},
			{Title: "Scripts (1)", Options: []TrustOption{{Module: "m", Label: "m"}}}}}
		got, err := onInput(paced("k\r", "\r", escKey, "x\r", "\r")).Trust(st)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(engine.TrustAnswer{Modules: []string{"a"}}), "Esc on Scripts went back to Added, where x ticked a")
	})

	It("trust: choose --ctrl+c--> cancelled", func() {
		st := TrustStep{Changes: 2, Groups: []TrustGroup{{Title: "Scripts (1)", Options: []TrustOption{{Module: "m", Label: "m"}}}}}
		_, err := onInput(paced("k\r", "x", "\x03")).Trust(st)
		Expect(err).To(MatchError(ErrAborted))
	})

	It("trust: choose --empty submit--> choose", func() {
		st := TrustStep{Changes: 2, Groups: []TrustGroup{{Title: "Added (1)", Options: []TrustOption{{Module: "a", Label: "a"}}},
			{Title: "Scripts (1)", Options: []TrustOption{{Module: "m", Label: "m"}}}}}
		// Enter on the first list moves on, empty; on the last, with nothing ticked in any, it is refused.
		got, err := onInput(paced("k\r", "\r", "\r", "x\r")).Trust(st)
		Expect(err).NotTo(HaveOccurred(), "nothing ticked is refused and the lists stay; Cancel is the way out")
		Expect(got).To(Equal(engine.TrustAnswer{Modules: []string{"m"}}))
	})

	It("names esc back and q quit in the key help; the first screen leaves out esc back, a text input says ctrl+c quits for q quit", func() {
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
		Expect(shown(f)).To(ContainSubstring("esc back • ctrl+c quits • enter submit"), "q is text there")
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

	DescribeTable("erases a form left by Esc, q or Ctrl+C as a submit erases it, so no ended form stays above the next screen",
		func(typed string, want error) {
			// The last render of an ended form draws it as nothing: the cursor up over it, then clear to the end.
			const erased = "\x1b[A\x1b[J"
			h := onTerminal(typed)
			_, err := h.Assistants(AssistantStep{Options: []string{"a", "b"}, Nav: Nav{Back: true}})
			if want == nil {
				Expect(err).NotTo(HaveOccurred())
			} else {
				Expect(err).To(MatchError(want))
			}
			Expect(h.out.(*bytes.Buffer).String()).To(ContainSubstring(erased))
		},
		Entry("a submit", "x\r", nil),
		Entry("Esc", escKey, ErrBack),
		Entry("q", "q", ErrAborted),
		Entry("Ctrl+C", "\x03", ErrAborted),
	)

	It("keeps an interrupt the form did not take (a SIGINT from outside) a cancel, never a submit", func() {
		h := onTerminal("xz")
		var v []string
		err := h.run(Nav{}, &interrupter{Field: h.multi("Install for which assistants?", "", []huh.Option[string]{huh.NewOption("a", "a")}, &v)})
		Expect(err).To(MatchError(ErrAborted))
		Expect(errors.Is(err, ErrBack)).To(BeFalse())
	})

	It("prints a quit as cancelled, nothing changed, and exits 0 as Ctrl+C does", func() {
		tmp := testenv.Isolate()
		GinkgoT().Chdir(tmp)
		h := onTerminal("q")
		root, err := NewRootCmd(os.Getenv, WithPrompter(h))
		Expect(err).NotTo(HaveOccurred())
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

var _ = Describe("the questions of lola init, lola market --add and hook and script consent", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("Esc on the background question stays: it is a first screen", func() {
		dark, err := onTerminal(escKey + "\r").Background()
		Expect(err).NotTo(HaveOccurred())
		Expect(dark).To(BeTrue(), "Dark, the first option, answered after Esc")
	})

	It("Esc on the marketplace name question stays: it is a first screen", func() {
		h := onTerminal(escKey + "\r")
		name, err := h.MarketName(MarketNameStep{Why: "acme is already a marketplace, from /src", Value: "acme-2"})
		Expect(err).NotTo(HaveOccurred())
		Expect(name).To(Equal("acme-2"), "the pre-filled name answered after Esc")
	})
})
