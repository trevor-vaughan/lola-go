package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"charm.land/huh/v2"
	"github.com/charmbracelet/x/ansi"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// lineReader returns one line per Read, as a terminal does; huh's accessible prompts make a
// new scanner per prompt, so a reader that returned more would lose answers.
type lineReader struct{ lines []string }

func (l *lineReader) Read(p []byte) (int, error) {
	if len(l.lines) == 0 {
		return 0, io.EOF
	}
	n := copy(p, l.lines[0]+"\n")
	l.lines = l.lines[1:]
	return n, nil
}

// onceEOFReader is a terminal where the user pressed Ctrl+D once: it returns before line by
// line, reports end of input once, and would then go on to after.
type onceEOFReader struct {
	before, after []string
	ended         bool
}

func (o *onceEOFReader) Read(p []byte) (int, error) {
	switch {
	case len(o.before) > 0:
		n := copy(p, o.before[0]+"\n")
		o.before = o.before[1:]
		return n, nil
	case !o.ended:
		o.ended = true
		return 0, io.EOF
	case len(o.after) > 0:
		n := copy(p, o.after[0]+"\n")
		o.after = o.after[1:]
		return n, nil
	}
	return 0, io.EOF
}

// panicField is a huh field that panics as panic does, after reading its input to the end
// when drain is set.
type panicField struct {
	*huh.Note
	drain bool
	panic func()
}

func (p panicField) RunAccessible(_ io.Writer, r io.Reader) error {
	if p.drain {
		if _, err := io.ReadAll(r); err != nil {
			return err
		}
	}
	p.panic()
	return nil
}

// accessible is the huh adapter in accessible mode over answers, writing to out.
func accessible(out *bytes.Buffer, answers ...string) *huhForms {
	return &huhForms{in: &eofReader{r: &lineReader{lines: answers}}, out: out, tty: true, accessible: true}
}

// syncBuffer is a buffer a spec reads while a form writes to it from another goroutine.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

var _ = Describe("the huh adapter in accessible mode", func() {
	var out bytes.Buffer
	BeforeEach(func() {
		testenv.Isolate()
		out.Reset()
	})

	It("offers Project, User and Other directory…, in that order, with nothing preselected (Plan 5c §14)", func() {
		got, err := accessible(&out, "3", "1", "0").Scope(ScopeStep{Question: "Install where?", ProjectRoot: "/src/api"})
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(ConsistOf(ScopeProject, ScopeOther))
		Expect(out.String()).To(ContainSubstring("Install where?"))
		Expect(out.String()).To(ContainSubstring("1.   Project (/src/api)\n"))
		Expect(out.String()).To(ContainSubstring("2.   User (~)\n"))
		Expect(out.String()).To(ContainSubstring("3.   Other directory…\n"))
		Expect(out.String()).NotTo(ContainSubstring("4."))

		out.Reset()
		_, err = accessible(&out, "0").Scope(ScopeStep{ProjectRoot: "/src/api", Preselect: []string{ScopeUser, ScopeOther}})
		Expect(err).NotTo(HaveOccurred())
		Expect(out.String()).To(ContainSubstring("2. ✓ User (~)\n"), "Change scope preselects the last answer")
		Expect(out.String()).To(ContainSubstring("3. ✓ Other directory…\n"))
	})

	It("asks a command that acts on one scope with nothing preselected (Plan 6b §3.1)", func() {
		got, err := accessible(&out, "2", "0").Scope(ScopeStep{Question: "Render where?", ProjectRoot: "/src/api", One: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal([]string{ScopeUser}))
		Expect(out.String()).To(ContainSubstring("Render where?"))
		Expect(out.String()).To(ContainSubstring("1.   Project (/src/api)\n"))
		Expect(out.String()).To(ContainSubstring("3.   Other directory…\n"))
	})

	It("resolves no scope from empty input: the answer is empty, never Project", func() {
		got, err := accessible(&out, "0").Scope(ScopeStep{Question: "Render where?", ProjectRoot: "/src/api", One: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(BeEmpty())
	})

	It("asks sync's trust question with Cancel under the cursor, and Choose… as numbered multi-selects with nothing preselected", func() {
		st := TrustStep{Changes: 3, Groups: []TrustGroup{
			{Title: "Changed (1)", Options: []TrustOption{{Module: "kit", Label: "~ kit rules (none) → false"}}},
			{Title: "Added (2)", Options: []TrustOption{{Module: "a", Label: "+ a source mods/a"}, {Module: "b", Label: "+ b source mods/b"}}},
		}}
		_, err := accessible(&out, "").Trust(st)
		Expect(err).To(MatchError(ErrAborted), "Enter alone is Cancel")
		Expect(out.String()).To(ContainSubstring("Trust these changes?"))
		Expect(out.String()).To(ContainSubstring("1. Trust all 3 changes\n2. Choose…\n3. Cancel (the project is not changed)\n"))

		ans, err := accessible(&out, "1").Trust(st)
		Expect(err).NotTo(HaveOccurred())
		Expect(ans).To(Equal(engine.TrustAnswer{All: true}))

		out.Reset()
		ans, err = accessible(&out, "2", "0", "2", "0").Trust(st)
		Expect(err).NotTo(HaveOccurred())
		Expect(ans).To(Equal(engine.TrustAnswer{Modules: []string{"b"}}))
		Expect(out.String()).To(ContainSubstring("Changed (1)"))
		Expect(out.String()).To(ContainSubstring("1.   ~ kit rules (none) → false\n"), "nothing preselected")
		Expect(out.String()).To(ContainSubstring("2.   + b source mods/b\n"))

		_, err = accessible(&out, "2", "0", "0").Trust(st)
		Expect(err).To(MatchError(ErrAborted), "choosing nothing is Cancel")

		out.Reset()
		_, err = accessible(&out, "").Trust(TrustStep{Changes: 1})
		Expect(err).To(MatchError(ErrAborted))
		Expect(out.String()).To(ContainSubstring("1. Trust this change\n2. Cancel (the project is not changed)\n"), "nothing to choose: no Choose…")
	})

	It("shows why a picker asks again in accessible mode, where huh hides descriptions", func() {
		_, err := accessible(&out, "0").Scope(ScopeStep{ProjectRoot: "/p", Problem: "pick at least one scope"})
		Expect(err).NotTo(HaveOccurred())
		Expect(out.String()).To(ContainSubstring("pick at least one scope"))
		_, err = accessible(&out, "0").Modules(ModuleStep{Options: []ModuleOption{{Name: "m", Label: "m"}}, Problem: "pick at least one module"})
		Expect(err).NotTo(HaveOccurred())
		Expect(out.String()).To(ContainSubstring("pick at least one module"))
		_, err = accessible(&out, "0").Assistants(AssistantStep{Options: []string{"claude-code"}, Problem: "pick at least one assistant"})
		Expect(err).NotTo(HaveOccurred())
		Expect(out.String()).To(ContainSubstring("pick at least one assistant"))
	})

	It("asks for the Other directory with the reason it is asked again, and cancels at end of input", func() {
		got, err := accessible(&out, "../elsewhere").OtherDirectory(DirectoryStep{})
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal("../elsewhere"))
		Expect(out.String()).To(ContainSubstring("Other directory"))

		out.Reset()
		_, err = accessible(&out, "x").OtherDirectory(DirectoryStep{Value: "missing", Problem: "/p/missing does not exist"})
		Expect(err).NotTo(HaveOccurred())
		Expect(out.String()).To(ContainSubstring("/p/missing does not exist"))

		_, err = accessible(&out).OtherDirectory(DirectoryStep{})
		Expect(err).To(MatchError(ErrAborted))
	})

	It("treats end of input as cancel, not as the default answer", func() {
		_, err := accessible(&out).Scope(ScopeStep{ProjectRoot: "/p", Preselect: []string{"project"}})
		Expect(err).To(MatchError(ErrAborted))
		_, err = accessible(&out, "0").Review(ReviewStep{Options: []string{ReviewApply, ReviewQuit}})
		Expect(err).To(MatchError(ErrAborted), "input ends after an answer huh rejected")
		_, err = accessible(&out, "7").Scope(ScopeStep{ProjectRoot: "/p"})
		Expect(err).To(MatchError(ErrAborted))
		_, err = accessible(&out).ConfirmEmpty("m")
		Expect(err).To(MatchError(ErrAborted))
	})

	It("asks sync's questions, defaulting to No, and cancels at end of input", func() {
		ok, err := accessible(&out, "y").Confirm("Trust these changes and continue?")
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(out.String()).To(ContainSubstring("Trust these changes and continue?"))
		ok, err = accessible(&out, "").Confirm("Remove these?")
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeFalse())
		_, err = accessible(&out).Confirm("Remove these?")
		Expect(err).To(MatchError(ErrAborted), "end of input is a cancel, not the default No")
	})

	It("stops a multi-field form at the first end of input, reading nothing after it", func() {
		pg := ComponentPage{Module: "kit", Title: "kit", Groups: []ComponentGroup{
			{Kind: "skill", Title: "Skills (1)", Options: []ComponentOption{{ID: "skill/a", Label: "a", Selected: true}}},
			{Kind: "command", Title: "Commands (1)", Options: []ComponentOption{{ID: "command/c", Label: "c", Selected: true}}},
		}}
		r := &onceEOFReader{after: []string{"1", "0"}}
		_, err := (&huhForms{in: &eofReader{r: r}, out: &out, tty: true, accessible: true}).Components(pg)
		Expect(err).To(MatchError(ErrAborted))
		Expect(r.after).To(HaveLen(2), "the Commands field must not wait for more input")

		r = &onceEOFReader{after: []string{"v1.0.0"}}
		_, err = (&huhForms{in: &eofReader{r: r}, out: &out, tty: true, accessible: true}).addSource(nil)
		Expect(err).To(MatchError(ErrAborted))
		Expect(r.after).To(HaveLen(1), "the Ref field must not wait for more input")
	})

	It("draws a component page: a list per kind, rules shown disabled, the runtime hooks line", func() {
		pg := ComponentPage{Module: "review-kit", Title: "review-kit", Footer: "Runtime hooks: 2 (you will be asked before they are installed)",
			Groups: []ComponentGroup{
				{Kind: "skill", Title: "Skills (2)", Options: []ComponentOption{{ID: "skill/a", Label: "a — A", Selected: true}, {ID: "skill/b", Label: "b", Selected: true}}},
				{Kind: "command", Title: "Commands (1)", Options: []ComponentOption{{ID: "command/c", Label: "c (skipped in selections.yaml)"}}},
				{Kind: "rule", Title: "Rules (1)", Disabled: true, Note: "(off: --no-rules)", Options: []ComponentOption{{ID: "rule/r", Label: "r"}}},
			}}
		got, err := accessible(&out, "1", "0", "1", "0").Components(pg)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal([]string{"skill/b", "command/c"}))
		Expect(out.String()).To(ContainSubstring("review-kit: Skills (2)"))
		Expect(out.String()).To(ContainSubstring("Rules (1) (off: --no-rules)"))
		Expect(out.String()).To(ContainSubstring("Runtime hooks: 2 (you will be asked before they are installed)"))
	})

	It("offers Add a source… after the modules and asks for the source and ref", func() {
		st := ModuleStep{Options: []ModuleOption{{Name: "m", Label: "/src · m — Mod"}, {Name: "n", Label: "/src · n (installed)"}}}
		got, err := accessible(&out, "1", "3", "0", "../src/new", "v1.2.0").Modules(st)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(ModuleAnswer{Names: []string{"m"}, AddSource: true, Source: "../src/new", Ref: "v1.2.0"}))
		Expect(out.String()).To(ContainSubstring("2.   /src · n (installed)"), "the first run preselects nothing")
		Expect(out.String()).To(ContainSubstring("3.   Add a source…"))
	})

	It("preselects exactly what the step asks, an installed module the user picked included", func() {
		st := ModuleStep{Options: []ModuleOption{{Name: "m", Label: "/src · m — Mod"}, {Name: "n", Label: "/src · n (installed)"}}, Preselect: []string{"n"}}
		got, err := accessible(&out, "0").Modules(st)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(ModuleAnswer{Names: []string{"n"}}))
		Expect(out.String()).To(ContainSubstring("1.   /src · m — Mod"))
		Expect(out.String()).To(ContainSubstring("2. ✓ /src · n (installed)"))
	})

	It("cancels on huh's index panic after input ended, and re-raises any other panic", func() {
		index := func() { _ = []int{}[len(os.Args)] }
		Expect(accessible(&out).run(panicField{Note: huh.NewNote(), drain: true, panic: index})).To(MatchError(ErrAborted))
		Expect(func() {
			_ = accessible(&out).run(panicField{Note: huh.NewNote(), drain: true, panic: func() { panic("boom") }})
		}).To(PanicWith("boom"))
		Expect(func() { _ = accessible(&out, "x").run(panicField{Note: huh.NewNote(), panic: index}) }).To(Panic(), "input has not ended")
	})

	It("asks consent questions in accessible mode when LOLA_ACCESSIBLE=1", func() {
		a := &app{getenv: func(k string) string {
			if k == "LOLA_ACCESSIBLE" {
				return "1"
			}
			return ""
		}}
		cmd := NewRootCmd(os.Getenv)
		cmd.SetIn(&lineReader{lines: []string{"y"}})
		cmd.SetErr(&out)
		ok, err := a.ask(cmd, "Install m's Claude Code hooks?")
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(out.String()).To(ContainSubstring("Install m's Claude Code hooks?"))
	})

	It("accessible mode: Ctrl+C at a sync question cancels with exit 0", func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj := filepath.Join(tmp, "proj")
		kit := filepath.Join(proj, "mods", "kit", "commands", "kit.md")
		Expect(os.MkdirAll(filepath.Dir(kit), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(proj, "mods", "kit", "lola.yml"), []byte("name: kit\n"), 0o644)).To(Succeed())
		Expect(os.WriteFile(kit, []byte("Kit\n"), 0o644)).To(Succeed())
		root := NewRootCmd(os.Getenv)
		root.SetArgs([]string{"install", filepath.Join(proj, "mods", "kit"), "-C", proj, "-t", "claude-code"})
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		Expect(Execute(root)).To(Equal(0))
		Expect(os.WriteFile(kit, []byte("Kit, pulled\n"), 0o644)).To(Succeed()) // sync asks to trust the folder's new content

		// Each question waits in a lola process of its own, which the interrupt reaches.
		for _, c := range []struct{ child, prompt, says string }{
			{"sync", "Trust these changes?", "aborted; the project was not changed.\n"},
			{"modules", "Install which modules?", "aborted; nothing changed.\n"},
			{"hooks", "Run m's install hooks?", "aborted; nothing changed.\n"},
		} {
			cmd := exec.Command(os.Args[0], "-test.run=^TestInterruptChild$")
			cmd.Env = append(os.Environ(), "LOLA_SPEC_CHILD="+c.child, "LOLA_SPEC_PROJECT="+proj, "LOLA_ACCESSIBLE=1")
			_, err := cmd.StdinPipe() // a terminal where nobody types; Wait closes it
			Expect(err).NotTo(HaveOccurred())
			var out syncBuffer
			cmd.Stdout, cmd.Stderr = &out, &out
			Expect(cmd.Start()).To(Succeed())
			DeferCleanup(func() { Expect(cmd.Process.Kill()).To(Or(Succeed(), MatchError(os.ErrProcessDone))) })
			waited := make(chan error, 1)
			go func() { waited <- cmd.Wait() }()
			Eventually(out.String, "10s").Should(ContainSubstring(c.prompt), "%s: the question waits for input", c.child)
			Expect(cmd.Process.Signal(os.Interrupt)).To(Succeed())
			Eventually(waited, "10s").Should(Receive(BeNil()), func() string {
				return c.child + ": exit 0, not killed by the signal: " + out.String()
			})
			Expect(out.String()).To(HaveSuffix(c.says), c.child)
		}
		Expect(os.ReadFile(filepath.Join(proj, ".claude", "commands", "kit.md"))).To(Equal([]byte("Kit\n")), "nothing installed")
	})

	It("answers an empty source with the picks and no source to add", func() {
		st := ModuleStep{Options: []ModuleOption{{Name: "m", Label: "m"}}}
		got, err := accessible(&out, "1", "2", "0", "", "").Modules(st)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(ModuleAnswer{Names: []string{"m"}, AddSource: true}))
	})

	It("offers Add a source… or Quit on an empty cache", func() {
		got, err := accessible(&out, "1", "./mod", "").Modules(ModuleStep{})
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(ModuleAnswer{AddSource: true, Source: "./mod"}))
		Expect(out.String()).To(ContainSubstring("no modules in your cache"))
		_, err = accessible(&out, "2").Modules(ModuleStep{})
		Expect(err).To(MatchError(ErrAborted))
	})

	It("offers the review menu with Apply as the default", func() {
		got, err := accessible(&out, "", "").Review(ReviewStep{Options: []string{ReviewApply, ReviewComponents, ReviewQuit}})
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(ReviewApply))
		Expect(out.String()).To(ContainSubstring("2. Change components"))
		got, err = accessible(&out, "2", "").Review(ReviewStep{Options: []string{ReviewApply, ReviewComponents, ReviewQuit}})
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(ReviewComponents))
		_, err = accessible(&out, "3", "").Review(ReviewStep{Options: []string{ReviewApply, ReviewComponents, ReviewQuit}})
		Expect(err).To(MatchError(ErrAborted))
	})

	It("sizes a full-screen list to show every option that fits, and caps a long one", func() {
		opts := []huh.Option[string]{huh.NewOption("project (/p)", "project"), huh.NewOption("user (~)", "user")}
		var v []string
		view := (&huhForms{height: 40}).multi("Install where?", "", opts, &v).WithTheme(huh.ThemeFunc(huh.ThemeBase)).View()
		Expect(view).To(ContainSubstring("project (/p)"))
		Expect(view).To(ContainSubstring("user (~)"))

		var many []huh.Option[string]
		for i := range 30 {
			many = append(many, huh.NewOption(fmt.Sprintf("module-%02d", i), fmt.Sprint(i)))
		}
		view = (&huhForms{height: 16}).multi("Install which modules?", "", many, &v).WithTheme(huh.ThemeFunc(huh.ThemeBase)).View()
		Expect(view).To(ContainSubstring("module-00"))
		Expect(view).NotTo(ContainSubstring("module-12"), "at most the terminal height minus 6 rows")

		three := []huh.Option[string]{huh.NewOption("one", "1"), huh.NewOption("two", "2"), huh.NewOption("three", "3")}
		view = (&huhForms{height: 40}).multi("Move which to the trash?", "2 locked:\n  a\n  b", three, &v).WithTheme(huh.ThemeFunc(huh.ThemeBase)).View()
		Expect(view).To(ContainSubstring("three"), "every line of a description counts toward the height")
	})

	It("keeps a label on one line when its source holds a newline", func() {
		Expect((&huhForms{}).cut("/src\n  evil · m")).To(Equal("/src evil · m"))
		st := ModuleStep{Options: []ModuleOption{{Name: "m", Label: "/src\n  evil · m — Mod"}}}
		_, err := accessible(&out, "1", "0").Modules(st)
		Expect(err).NotTo(HaveOccurred())
		Expect(out.String()).To(ContainSubstring("1.   /src evil · m — Mod"))
	})

	It("aborts a consent prompt at end of input and answers one that is given", func() {
		a := &app{getenv: func(k string) string {
			if k == "LOLA_ACCESSIBLE" {
				return "1"
			}
			return ""
		}}
		for _, tc := range []struct {
			in   []string
			want bool
		}{{[]string{"y"}, true}, {[]string{"n"}, false}} {
			cmd := NewRootCmd(os.Getenv)
			cmd.SetIn(&lineReader{lines: tc.in})
			cmd.SetErr(&out)
			ok, err := a.ask(cmd, "Run m's install hooks?")
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(Equal(tc.want))
		}
		cmd := NewRootCmd(os.Getenv)
		cmd.SetIn(&lineReader{})
		cmd.SetErr(&out)
		ok, err := a.ask(cmd, "Run m's install hooks?")
		Expect(err).To(MatchError(ErrAborted), "end of input is a cancel, not the default No")
		Expect(ok).To(BeFalse())
	})

	It("cuts labels to the terminal width with …", func() {
		h := &huhForms{width: 30}
		label := h.cut("/a/very/long/source/path · review-kit — Code review helpers")
		Expect(label).To(HaveSuffix("…"))
		Expect(ansi.StringWidth(label)).To(BeNumerically("<=", 22))
		Expect(h.cut("short")).To(Equal("short"))
	})

	It("takes accessible mode from LOLA_ACCESSIBLE or TERM=dumb through the injected getenv", func() {
		for env, want := range map[string]bool{"LOLA_ACCESSIBLE=1": true, "TERM=dumb": true, "TERM=xterm": false} {
			k, v, _ := strings.Cut(env, "=")
			a := &app{getenv: func(key string) string {
				if key == k {
					return v
				}
				return ""
			}}
			cmd := NewRootCmd(os.Getenv)
			Expect(a.huhPrompter(cmd).(*huhForms).accessible).To(Equal(want), env)
			Expect(a.huhPrompter(cmd).Interactive()).To(BeFalse(), "test streams are not terminals")
		}
	})

	It("drives lola install end to end through the real forms", func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		mod, proj := filepath.Join(tmp, "src", "deploy"), filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(filepath.Join(mod, "commands"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(mod, "commands", "deploy.md"), []byte("Deploy.\n"), 0o644)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(mod, "commands", "undo.md"), []byte("Undo.\n"), 0o644)).To(Succeed())
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		GinkgoT().Chdir(proj)
		// scope: toggle project, confirm; empty cache: Add a source…, then the source and no
		// ref; modules: confirm the new module; components: drop undo, confirm; review: Apply.
		var errb bytes.Buffer
		h := accessible(&errb, "1", "0", "1", mod, "", "0", "2", "0", "")
		root := NewRootCmd(os.Getenv, WithPrompter(h))
		var stdout bytes.Buffer
		root.SetOut(&stdout)
		root.SetErr(&errb)
		root.SetArgs([]string{"install", "-t", "claude-code"})
		Expect(Execute(root)).To(Equal(0), errb.String())
		Expect(errb.String()).To(ContainSubstring("Equivalent commands:\n  lola cache add " + mod + "\n  lola install deploy --scope project -t claude-code --skip deploy:command/undo\n"))
		Expect(filepath.Join(proj, ".claude/commands/deploy.md")).To(BeARegularFile())
		Expect(filepath.Join(proj, ".claude/commands/undo.md")).NotTo(BeAnExistingFile())

		errb.Reset()
		root = NewRootCmd(os.Getenv, WithPrompter(accessible(&errb, "1", "0")))
		root.SetOut(&stdout)
		root.SetErr(&errb)
		root.SetArgs([]string{"install", "-t", "claude-code", "--scope", "user"})
		Expect(Execute(root)).To(Equal(0))
		Expect(errb.String()).To(HaveSuffix("aborted; nothing changed.\n"), "end of input at the module picker cancels")
		Expect(filepath.Join(os.Getenv("HOME"), ".claude")).NotTo(BeADirectory())
	})

	It("drives lola install through Other directory… with the real forms", func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		mod, proj, other := filepath.Join(tmp, "src", "deploy"), filepath.Join(tmp, "proj"), filepath.Join(tmp, "other")
		Expect(os.MkdirAll(filepath.Join(mod, "commands"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(mod, "commands", "deploy.md"), []byte("Deploy.\n"), 0o644)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(mod, "commands", "undo.md"), []byte("Undo.\n"), 0o644)).To(Succeed())
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		Expect(os.MkdirAll(other, 0o755)).To(Succeed())
		GinkgoT().Chdir(proj)
		// scope: toggle Project and Other directory…, confirm; the path: first one that is not
		// a directory, then the other project; components (one page, both roots alike): drop
		// undo, confirm; review: Apply.
		var errb bytes.Buffer
		root := NewRootCmd(os.Getenv, WithPrompter(accessible(&errb, "1", "3", "0", "nowhere", "../other", "2", "0", "")))
		var stdout bytes.Buffer
		root.SetOut(&stdout)
		root.SetErr(&errb)
		root.SetArgs([]string{"install", mod, "-t", "claude-code"})
		Expect(Execute(root)).To(Equal(0), errb.String())
		Expect(errb.String()).To(ContainSubstring(filepath.Join(proj, "nowhere") + " does not exist"))
		Expect(errb.String()).To(ContainSubstring("Equivalent commands:\n" +
			"  lola install " + mod + " --scope project -C " + proj + " -t claude-code --skip deploy:command/undo\n" +
			"  lola install " + mod + " --scope project -C " + other + " -t claude-code --skip deploy:command/undo\n"))
		for _, dir := range []string{proj, other} {
			Expect(filepath.Join(dir, ".claude/commands/deploy.md")).To(BeARegularFile())
			Expect(filepath.Join(dir, ".claude/commands/undo.md")).NotTo(BeAnExistingFile())
		}
	})

	It("the picker preselects nothing and lists every locked item with its reason", func() {
		st := TidyPickStep{Options: []TidyOption{{ID: "0", Label: "claude-code  skill/old  .claude/skills/old/"},
			{ID: "1", Label: "claude-code  command/tmp  .claude/commands/tmp.md"}},
			Locked: []string{"claude-code  skill/notes  .claude/skills/notes/  on your keep-list (skill/notes)",
				"claude-code  command/CLAUDE  .claude/commands/CLAUDE.md  is an instruction file", "claude-code  agent/x  .claude/agents/x.md  tracked by git"}}
		got, err := accessible(&out, "2", "0").TidyPick(st)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal([]string{"1"}))
		Expect(out.String()).To(ContainSubstring("3 locked, which lola tidy never moves:\n" +
			"  claude-code  skill/notes  .claude/skills/notes/  on your keep-list (skill/notes)\n" +
			"  claude-code  command/CLAUDE  .claude/commands/CLAUDE.md  is an instruction file\n" +
			"  claude-code  agent/x  .claude/agents/x.md  tracked by git\n"))
		Expect(out.String()).To(ContainSubstring("Move which to the trash?"))
		Expect(out.String()).To(ContainSubstring("1.   claude-code  skill/old  .claude/skills/old/\n2.   claude-code  command/tmp  .claude/commands/tmp.md\n"),
			"nothing preselected")

		out.Reset()
		got, err = accessible(&out, "1", "0").TidyKeep(TidyKeepStep{Options: st.Options})
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal([]string{"0"}))
		Expect(out.String()).To(ContainSubstring("Add any of the rest to your keep-list?"))
		Expect(out.String()).To(ContainSubstring("1.   claude-code  skill/old"), "nothing preselected")

		By("full forms list the locked items in the description, cut to the terminal height")
		var v []string
		view := func(h *huhForms) string {
			fields := h.tidyPick(st, &v)
			Expect(fields).To(HaveLen(1))
			return fields[0].(*huh.MultiSelect[string]).WithTheme(huh.ThemeFunc(huh.ThemeBase)).View()
		}
		tall := view(&huhForms{height: 40})
		Expect(tall).To(ContainSubstring("3 locked, which lola tidy never moves:"))
		Expect(tall).To(ContainSubstring("agent/x  .claude/agents/x.md  tracked by git"))
		Expect(tall).To(ContainSubstring("command/tmp"))
		short := view(&huhForms{height: 12})
		Expect(short).To(ContainSubstring("skill/notes"))
		Expect(short).To(ContainSubstring("and 2 more"))
		Expect(short).NotTo(ContainSubstring("tracked by git"))
		Expect(short).To(ContainSubstring("skill/old"), "the cut leaves room for the options")
		shorter := view(&huhForms{height: 10})
		Expect(shorter).To(ContainSubstring("3 locked, which lola tidy never moves:"))
		Expect(shorter).To(ContainSubstring("skill/old"))
		Expect(shorter).To(ContainSubstring("command/tmp"))
		Expect(shorter).NotTo(ContainSubstring("skill/notes"), "no room for a locked line")

		By("labels the width would cut alike stay whole, since huh ticks every option with the label under the cursor")
		narrow := (&huhForms{width: 40}).tidyOptions([]TidyOption{{ID: "0", Label: "claude-code  skill/a-very-long-name-one  .claude/skills/a-very-long-name-one/"},
			{ID: "1", Label: "claude-code  skill/a-very-long-name-two  .claude/skills/a-very-long-name-two/"}, {ID: "2", Label: "claude-code  skill/b  .claude/skills/b/"}})
		Expect([]string{narrow[0].Key, narrow[1].Key}).To(Equal([]string{
			"claude-code  skill/a-very-long-name-one  .claude/skills/a-very-long-name-one/",
			"claude-code  skill/a-very-long-name-two  .claude/skills/a-very-long-name-two/"}))
		Expect(narrow[2].Key).To(Equal("claude-code  skill/b  .claude/s…"), "a label no other shares is cut")
		same := (&huhForms{}).tidyOptions([]TidyOption{{ID: "0", Label: "x  skill/a  p"}, {ID: "1", Label: "x  skill/a  p"}, {ID: "2", Label: "y"}})
		Expect([]string{same[0].Key, same[1].Key, same[2].Key}).To(Equal([]string{"x  skill/a  p [1]", "x  skill/a  p [2]", "y"}),
			"labels equal in full gain a suffix, so no two options share one")
	})

	It("picker, review, dry-run and refusal labels show only the server label", func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj := filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		userinfo := "SENTINEL_USER:SENTINEL_PW"
		servers := `{"mcpServers": {
  "stdio": {"command": "TOKEN=SENTINEL_ENVWORD /opt/bin/srv-cmd", "args": ["--token", "SENTINEL_ARG"], "env": {"K": "SENTINEL_ENV"}},
  "remote": {"type": "http", "url": "https://` + userinfo + `@mcp.example.com/SENTINEL_PATH?q=SENTINEL_QUERY#SENTINEL_FRAG",
    "headers": {"Authorization": "SENTINEL_HEADER"}}
}}`
		run := func(p Prompter, args ...string) (int, string) {
			GinkgoHelper()
			Expect(os.WriteFile(filepath.Join(proj, ".mcp.json"), []byte(servers), 0o644)).To(Succeed())
			opts := []Option{}
			if p != nil {
				opts = append(opts, WithPrompter(p))
			}
			root := NewRootCmd(os.Getenv, opts...)
			var stdout, stderr bytes.Buffer
			root.SetOut(&stdout)
			root.SetErr(&stderr)
			root.SetIn(strings.NewReader(""))
			root.SetArgs(append([]string{"tidy", "-C", proj, "-t", "claude-code"}, args...))
			code := Execute(root)
			Expect(stdout.String()+stderr.String()).NotTo(ContainSubstring("SENTINEL"), "%v", args)
			return code, stdout.String() + stderr.String()
		}
		// move picker: tick the first (mcp/remote), confirm; keep picker: confirm nothing; review: no.
		var errb bytes.Buffer
		code, got := run(accessible(&errb, "1", "0", "0", "n"))
		Expect(code).To(Equal(0), got)
		Expect(errb.String()).NotTo(ContainSubstring("SENTINEL"))
		Expect(errb.String()).To(ContainSubstring("1.   claude-code  mcp/remote (mcp.example.com)  .mcp.json /mcpServers/remote\n"))
		Expect(errb.String()).To(ContainSubstring("2.   claude-code  mcp/stdio (srv-cmd)  .mcp.json /mcpServers/stdio\n"))
		Expect(errb.String()).To(ContainSubstring("Add any of the rest to your keep-list?"))
		Expect(errb.String()).To(ContainSubstring("1.   mcp/stdio  claude-code\n"), "the keep picker names the selector it writes")
		Expect(got).To(ContainSubstring("  → claude-code  mcp/remote (mcp.example.com)  .mcp.json /mcpServers/remote\n"), "the review")
		Expect(errb.String()).To(ContainSubstring("Move 1 item to the trash?"))
		Expect(got).To(ContainSubstring("aborted; nothing changed."))

		code, got = run(nil, "-n")
		Expect(code).To(Equal(0), got)
		Expect(got).To(ContainSubstring("  → claude-code  mcp/stdio (srv-cmd)  .mcp.json /mcpServers/stdio\n"), "the dry run")
		code, got = run(nil)
		Expect(code).To(Equal(1), "the refusal's listing")
		Expect(got).To(ContainSubstring("  → claude-code  mcp/remote (mcp.example.com)  .mcp.json /mcpServers/remote\n"))
	})
})
