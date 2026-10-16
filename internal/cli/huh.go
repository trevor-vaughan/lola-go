package cli

import (
	"errors"
	"io"
	"os"
	"runtime"
	"strings"

	"charm.land/huh/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// addSource is the module picker's Add a source… entry; module names never hold a NUL.
const addSource = "\x00add"

// eofReader records that the input ended. huh's accessible prompts read end of input as an
// empty answer, the default; the adapter turns it into a cancel instead. A terminal reports
// end of input once and then waits for more, so after the first one every Read ends at once:
// the form's later fields must not wait for input the run will discard.
type eofReader struct {
	r   io.Reader
	eof bool
}

func (e *eofReader) Read(p []byte) (int, error) {
	if e.eof {
		return 0, io.EOF
	}
	n, err := e.r.Read(p)
	if errors.Is(err, io.EOF) {
		e.eof = true
	}
	return n, err
}

// huhForms is the Prompter that draws the install flow's steps as inline huh forms on stderr
// (Plan 5c §3): no alternate screen, and in accessible mode huh's line prompts. Only accessible
// mode reads through in, the eofReader: the full forms need stdin itself, which bubbletea puts
// in raw mode only when it is a terminal file.
type huhForms struct {
	stdin         io.Reader
	in            *eofReader
	out           io.Writer
	tty           bool
	accessible    bool
	color         bool
	width, height int // of the terminal stderr is on; 0 when unknown
}

// accessible reports whether forms use huh's line prompts: LOLA_ACCESSIBLE=1 or TERM=dumb, read
// through the injected getenv.
func (a *app) accessible() bool {
	return a.getenv("LOLA_ACCESSIBLE") == "1" || a.getenv("TERM") == "dumb"
}

// huhPrompter builds the huh adapter over the command's streams.
func (a *app) huhPrompter(cmd *cobra.Command) Prompter {
	h := &huhForms{stdin: cmd.InOrStdin(), in: &eofReader{r: cmd.InOrStdin()}, out: cmd.ErrOrStderr(), tty: interactive(cmd),
		accessible: a.accessible(), color: a.color(cmd.ErrOrStderr())}
	if f, ok := cmd.ErrOrStderr().(*os.File); ok {
		if w, ht, err := term.GetSize(int(f.Fd())); err == nil {
			h.width, h.height = w, ht
		}
	}
	return h
}

func (h *huhForms) Interactive() bool { return h.tty }

// run runs one form. Ctrl+C (huh's ErrUserAborted) and end of input cancel the run.
//
// huh v2.0.3's accessible number prompts return the last answer they rejected when input
// ends, and Select and MultiSelect then index their options with it and panic. That panic, an
// index out of range in accessible mode after input ended, is end of input too, so it cancels;
// any other panic is not ours to hide.
func (h *huhForms) run(fields ...huh.Field) (err error) {
	var in io.Reader = h.in
	if !h.accessible {
		in = h.stdin
	}
	f := huh.NewForm(huh.NewGroup(fields...)).WithInput(in).WithOutput(h.out).WithAccessible(h.accessible)
	if !h.color {
		f = f.WithTheme(huh.ThemeFunc(huh.ThemeBase))
	}
	defer func() {
		if r := recover(); r != nil {
			re, ok := r.(runtime.Error)
			if !h.accessible || !h.in.eof || !ok || !strings.Contains(re.Error(), "index out of range") {
				panic(r)
			}
			err = ErrAborted
		}
	}()
	err = f.Run()
	if errors.Is(err, huh.ErrUserAborted) || h.in.eof {
		return ErrAborted
	}
	return err
}

// cut puts a label on one line (a newline becomes a space) and fits it to the terminal width,
// ending a cut one with "…"; 8 columns are left for huh's cursor, checkbox and border.
func (h *huhForms) cut(s string) string {
	if strings.Contains(s, "\n") {
		lines := strings.Split(s, "\n")
		for i, l := range lines {
			lines[i] = strings.TrimSpace(l)
		}
		s = strings.Join(lines, " ")
	}
	room := h.width - 8
	if h.width == 0 || ansi.StringWidth(s) <= room {
		return s
	}
	return ansi.Truncate(s, max(room, 1), "…")
}

// multi is a MultiSelect over opts, as tall as its title, description and options, capped at
// the terminal height minus 6 rows. The height is always set: huh v2.0.3 otherwise sizes the
// list as its options minus the title's lines and shows one option too few.
func (h *huhForms) multi(title, desc string, opts []huh.Option[string], value *[]string) *huh.MultiSelect[string] {
	ms := huh.NewMultiSelect[string]().Title(title).Description(desc).Options(opts...).Value(value)
	height := len(opts) + 1
	if desc != "" {
		height++
	}
	if h.height > 0 {
		height = min(height, max(h.height-6, 3))
	}
	return ms.Height(height)
}

// Scope offers Project, User and Other directory…, preselecting exactly st.Preselect.
func (h *huhForms) Scope(st ScopeStep) ([]string, error) {
	opts := []huh.Option[string]{
		huh.NewOption(h.cut("Project ("+st.ProjectRoot+")"), ScopeProject).Selected(contains(st.Preselect, ScopeProject)),
		huh.NewOption("User (~)", ScopeUser).Selected(contains(st.Preselect, ScopeUser)),
		huh.NewOption("Other directory…", ScopeOther).Selected(contains(st.Preselect, ScopeOther)),
	}
	var chosen []string
	err := h.run(h.withProblem(st.Problem, h.multi("Install where?", st.Problem, opts, &chosen))...)
	return chosen, err
}

// withProblem puts a note saying why a picker asks again above it in accessible mode, where huh
// does not print a field's description; full forms show the reason as the description.
func (h *huhForms) withProblem(problem string, f huh.Field) []huh.Field {
	if h.accessible && problem != "" {
		return []huh.Field{huh.NewNote().Title(h.cut(problem)), f}
	}
	return []huh.Field{f}
}

// OtherDirectory asks for the path of Other directory…. The reason it asks again is a note
// above the input: huh's accessible Input does not print its description.
func (h *huhForms) OtherDirectory(st DirectoryStep) (string, error) {
	var fields []huh.Field
	if st.Problem != "" {
		fields = append(fields, huh.NewNote().Title(h.cut(st.Problem)))
	}
	dir := st.Value
	fields = append(fields, huh.NewInput().Title("Other directory").
		Description("a project directory, relative to the current directory; ~ is not expanded").Value(&dir))
	err := h.run(fields...)
	return dir, err
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func (h *huhForms) Modules(st ModuleStep) (ModuleAnswer, error) {
	if len(st.Options) == 0 {
		choice := "add"
		err := h.run(huh.NewNote().Title("no modules in your cache"),
			huh.NewSelect[string]().Title("What next?").Description(st.Problem).
				Options(huh.NewOption("Add a source…", "add"), huh.NewOption("Quit", "quit")).Value(&choice))
		if err != nil {
			return ModuleAnswer{}, err
		}
		if choice == "quit" {
			return ModuleAnswer{}, ErrAborted
		}
		return h.addSource(nil)
	}
	opts := make([]huh.Option[string], 0, len(st.Options)+1)
	for _, o := range st.Options {
		opts = append(opts, huh.NewOption(h.cut(o.Label), o.Name).Selected(contains(st.Preselect, o.Name)))
	}
	opts = append(opts, huh.NewOption("Add a source…", addSource))
	var chosen []string
	if err := h.run(h.withProblem(st.Problem, h.multi("Install which modules?", st.Problem, opts, &chosen).Filterable(true))...); err != nil {
		return ModuleAnswer{}, err
	}
	if contains(chosen, addSource) {
		names := make([]string, 0, len(chosen))
		for _, c := range chosen {
			if c != addSource {
				names = append(names, c)
			}
		}
		return h.addSource(names)
	}
	return ModuleAnswer{Names: chosen}, nil
}

// addSource asks for a source and an optional ref; the flow adds it exactly as lola cache add
// does. An empty source is an answer with AddSource and no Source: the flow goes back to the
// picker with names selected.
func (h *huhForms) addSource(names []string) (ModuleAnswer, error) {
	var src, ref string
	err := h.run(huh.NewInput().Title("Source").Description("a module directory, archive, URL or git repository").Value(&src),
		huh.NewInput().Title("Ref (optional)").Description("a git branch, tag or full commit SHA").Value(&ref))
	if err != nil {
		return ModuleAnswer{}, err
	}
	src, ref = strings.TrimSpace(src), strings.TrimSpace(ref)
	return ModuleAnswer{Names: names, AddSource: true, Source: src, Ref: ref}, nil
}

func (h *huhForms) Assistants(st AssistantStep) ([]string, error) {
	opts := make([]huh.Option[string], 0, len(st.Options))
	for _, id := range st.Options {
		opts = append(opts, huh.NewOption(id, id).Selected(contains(st.Preselect, id)))
	}
	desc := st.Problem
	if desc == "" && len(st.Scopes) > 1 {
		desc = "for " + strings.Join(st.Scopes, " and ")
	}
	var chosen []string
	err := h.run(h.withProblem(st.Problem, h.multi("Install for which assistants?", desc, opts, &chosen))...)
	return chosen, err
}

func (h *huhForms) Components(pg ComponentPage) ([]string, error) {
	var fields []huh.Field
	values := make([][]string, len(pg.Groups))
	for i, g := range pg.Groups {
		if g.Disabled {
			var lines []string
			for _, o := range g.Options {
				lines = append(lines, h.cut(o.Label))
			}
			fields = append(fields, huh.NewNote().Title(g.Title+" "+g.Note).Description(strings.Join(lines, "\n")))
			continue
		}
		opts := make([]huh.Option[string], 0, len(g.Options))
		for _, o := range g.Options {
			opts = append(opts, huh.NewOption(h.cut(o.Label), o.ID).Selected(o.Selected))
		}
		title := g.Title
		if i == 0 {
			title = pg.Title + ": " + title
		}
		fields = append(fields, h.multi(title, "", opts, &values[i]).Filterable(true))
	}
	if pg.Footer != "" {
		fields = append(fields, huh.NewNote().Title(pg.Footer))
	}
	if err := h.run(fields...); err != nil {
		return nil, err
	}
	var out []string
	for _, v := range values {
		out = append(out, v...)
	}
	return out, nil
}

func (h *huhForms) ConfirmEmpty(module string) (bool, error) {
	ok := false
	return ok, h.run(huh.NewConfirm().Title("Install " + module + " with no components?").Value(&ok))
}

// reviewLabels name the review menu's entries.
var reviewLabels = map[string]string{ReviewApply: "Apply", ReviewScope: "Change scope", ReviewModules: "Change modules",
	ReviewAssistants: "Change assistants", ReviewComponents: "Change components", ReviewQuit: "Quit"}

func (h *huhForms) Review(st ReviewStep) (string, error) {
	opts := make([]huh.Option[string], 0, len(st.Options))
	for _, o := range st.Options {
		opts = append(opts, huh.NewOption(reviewLabels[o], o))
	}
	choice := ReviewApply
	if err := h.run(huh.NewSelect[string]().Title("Apply these changes?").Description("Ctrl+C cancels; nothing is written").
		Options(opts...).Value(&choice)); err != nil {
		return "", err
	}
	if choice == ReviewQuit {
		return "", ErrAborted
	}
	return choice, nil
}
