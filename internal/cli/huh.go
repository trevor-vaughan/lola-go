package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/engine"
)

// addSource is the module picker's Add a source… entry; module names never hold a NUL.
const addSource = "\x00add"

// eofReader records that the input ended. huh's accessible prompts read end of input as an
// empty answer, the default; the adapter turns it into a cancel instead. A terminal reports
// end of input once and then waits for more, so after the first one every Read ends at once:
// the form's later fields must not wait for input the run will discard.
//
// Accessible prompts read stdin in cooked mode, where Ctrl+C is a SIGINT that would kill lola
// with no message (Plan 6a §3.4). While catch runs a form, a Read waits for the first of the
// input and an interrupt, and an interrupt ends the input: the form cancels. A Go
// read cannot be interrupted, so the read runs in a goroutine; one an interrupt leaves blocked
// on stdin ends with the process, and nothing reads stdin after it, since the cancel ends the
// command.
type eofReader struct {
	r       io.Reader
	eof     bool
	sig     chan os.Signal  // set while catch runs a form
	pending chan readResult // the read of r in progress, nil when none
}

// readResult is one Read of an eofReader's input, done in a goroutine.
type readResult struct {
	b   []byte
	err error
}

func (e *eofReader) Read(p []byte) (int, error) {
	if e.eof {
		return 0, io.EOF
	}
	if e.sig == nil {
		n, err := e.r.Read(p)
		if errors.Is(err, io.EOF) {
			e.eof = true
		}
		return n, err
	}
	if e.pending == nil {
		e.pending = make(chan readResult, 1)
		buf := make([]byte, len(p))
		go func(r io.Reader, out chan<- readResult) {
			n, err := r.Read(buf)
			out <- readResult{buf[:n], err}
		}(e.r, e.pending)
	}
	select {
	case res := <-e.pending:
		e.pending = nil
		if errors.Is(res.err, io.EOF) {
			e.eof = true
		}
		return copy(p, res.b), res.err
	case <-e.sig:
		e.eof = true
		return 0, io.EOF
	}
}

// catch runs a form that reads e, taking Ctrl+C (os.Interrupt) as the end of its input while it
// waits, so the form cancels as at end of input instead of lola dying of the signal.
func (e *eofReader) catch(run func() error) error {
	e.sig = make(chan os.Signal, 1)
	signal.Notify(e.sig, os.Interrupt)
	defer func() {
		signal.Stop(e.sig)
		e.sig = nil
	}()
	return run()
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
	// dark resolves the background lolaTheme draws for (app.darkBackground); theme calls it once,
	// before the first form in color, and keeps the answer in isDark.
	dark   func() (bool, error)
	isDark *bool
	view   func(content string, change bool) (viewAnswer, error) // the viewer in confirm mode on stderr (app.view)
	// viewer reports whether the review may open the viewer (app.paging), asked only when it
	// would: it reads config.yaml.
	viewer func() bool
}

// accessible reports whether forms use huh's line prompts: LOLA_ACCESSIBLE=1 or TERM=dumb, read
// through the injected getenv.
func (a *app) accessible() bool {
	return a.getenv("LOLA_ACCESSIBLE") == "1" || a.getenv("TERM") == "dumb"
}

// huhPrompter builds the huh adapter over the command's streams.
func (a *app) huhPrompter(cmd *cobra.Command) Prompter {
	h := &huhForms{stdin: cmd.InOrStdin(), in: &eofReader{r: cmd.InOrStdin()}, out: cmd.ErrOrStderr(), tty: interactive(cmd),
		accessible: a.accessible(), color: a.color(cmd.ErrOrStderr()), dark: func() (bool, error) { return a.darkBackground(cmd) },
		view:   func(content string, change bool) (viewAnswer, error) { return a.view(cmd, content, true, change) },
		viewer: func() bool { return a.paging(cmd).viewer }}
	if f, ok := cmd.ErrOrStderr().(*os.File); ok {
		if w, ht, err := term.GetSize(int(f.Fd())); err == nil {
			h.width, h.height = w, ht
		}
	}
	return h
}

func (h *huhForms) Interactive() bool { return h.tty }

// run runs one form in the forms' theme.
func (h *huhForms) run(fields ...huh.Field) error {
	t, err := h.theme()
	if err != nil {
		return err
	}
	return h.runIn(t, fields...)
}

// theme is the forms' theme (Plan 10 §4): plainTheme without color; in color, lolaTheme for the
// background h.dark resolves, once, before the first form. The ThemeFunc ignores the background
// huh guesses and passes it.
func (h *huhForms) theme() (huh.Theme, error) {
	if !h.color {
		return huh.ThemeFunc(func(bool) *huh.Styles { return plainTheme() }), nil
	}
	if h.isDark == nil {
		dark, err := h.dark()
		if err != nil {
			return nil, err
		}
		h.isDark = &dark
	}
	dark := *h.isDark
	return huh.ThemeFunc(func(bool) *huh.Styles { return lolaTheme(dark) }), nil
}

// runIn runs one form in theme t. Ctrl+C (huh's ErrUserAborted) and end of input cancel the run.
//
// huh v2.0.3's accessible number prompts return the last answer they rejected when input
// ends, and Select and MultiSelect then index their options with it and panic. That panic, an
// index out of range in accessible mode after input ended, is end of input too, so it cancels;
// any other panic is not ours to hide.
func (h *huhForms) runIn(t huh.Theme, fields ...huh.Field) (err error) {
	var in io.Reader = h.in
	if !h.accessible {
		in = h.stdin
	}
	f := huh.NewForm(huh.NewGroup(fields...)).WithInput(in).WithOutput(h.out).WithAccessible(h.accessible).WithTheme(t)
	defer func() {
		if r := recover(); r != nil {
			re, ok := r.(runtime.Error)
			if !h.accessible || !h.in.eof || !ok || !strings.Contains(re.Error(), "index out of range") {
				panic(r)
			}
			err = ErrAborted
		}
	}()
	if h.accessible {
		err = h.in.catch(f.Run)
	} else {
		err = f.Run()
	}
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
		height += strings.Count(desc, "\n") + 1
	}
	if h.height > 0 {
		height = min(height, max(h.height-6, 3))
	}
	return ms.Height(height)
}

// Scope offers Project, User and Other directory…, preselecting exactly st.Preselect. It is a
// multi-select even when st.One, so nothing is chosen until the user ticks it (a select would
// answer Project on Enter); the form refuses an empty answer (Plan 10 §4).
func (h *huhForms) Scope(st ScopeStep) ([]string, error) {
	opts := []huh.Option[string]{
		huh.NewOption(h.cut("Project ("+st.ProjectRoot+")"), ScopeProject).Selected(contains(st.Preselect, ScopeProject)),
		huh.NewOption("User (~)", ScopeUser).Selected(contains(st.Preselect, ScopeUser)),
		huh.NewOption("Other directory…", ScopeOther).Selected(contains(st.Preselect, ScopeOther)),
	}
	desc := st.Problem
	if desc == "" && st.One {
		desc = "pick one"
	}
	var chosen []string
	err := h.run(h.withProblem(st.Problem, h.required(h.multi(st.Step.title(0, st.Question), desc, opts, &chosen)))...)
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

// nothingSelected says a picker that needs a pick got none, and which keys make one: the full
// form's, or the line prompts' answers in accessible mode.
func (h *huhForms) nothingSelected() string {
	if h.accessible {
		return "nothing selected — type an option's number to select it, 0 confirms, ctrl+c quits"
	}
	return "nothing selected — space selects, enter confirms, ctrl+c quits"
}

// requiredPick is a multi-select that needs a pick (Plan 10 §4). huh validates a multi-select
// when it gains the focus and on every toggle, not only on Enter (field_multiselect.go:236-237,
// 548), and a group shows a field's error in place of the key help (group.go:396). So the field
// records whether the key it is handling is Enter, and its validator refuses an empty pick only
// then: a picker opens with huh's key help, Enter with nothing ticked shows the refusal, and the
// next key that revalidates, a toggle, clears it.
type requiredPick struct {
	*huh.MultiSelect[string]
	entered bool
}

// Update records whether msg is Enter before huh handles it, and returns the wrapper, which the
// group keeps in the multi-select's place.
func (r *requiredPick) Update(msg tea.Msg) (huh.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		r.entered = k.String() == "enter"
	}
	_, cmd := r.MultiSelect.Update(msg)
	return r, cmd
}

// required makes ms a picker that needs a pick (Plan 10 §4). In accessible mode, which has no
// keys, huh validates only when the user confirms, so the refusal applies at once. Once input has
// ended (Ctrl+C included) the validator accepts: huh's accessible MultiSelect asks again for as
// long as it refuses, and at end of input every ask answers 0, so refusing would never end; run
// then cancels.
func (h *huhForms) required(ms *huh.MultiSelect[string]) huh.Field {
	r := &requiredPick{MultiSelect: ms}
	ms.Validate(func(picked []string) error {
		if len(picked) > 0 || h.in.eof || !h.accessible && !r.entered {
			return nil
		}
		return errors.New(h.nothingSelected())
	})
	return r
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
	if len(st.Markets) > 0 {
		return h.withMarkets(st)
	}
	if len(st.Options) == 0 {
		choice := "add"
		err := h.run(huh.NewNote().Title("no modules in your cache"),
			huh.NewSelect[string]().Title(st.Step.title(0, "What next?")).Description(st.Problem).
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
	if err := h.run(h.withProblem(st.Problem, h.required(h.multi(st.Step.title(0, "Install which modules?"), st.Problem, opts, &chosen).Filterable(true)))...); err != nil {
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

// withMarkets is the module picker with the marketplace groups (Plan 6c §6.5): the cached modules
// and Add a source… first, unless st.Browse, then one filterable group per marketplace, so a pick
// may come from any of them. A lone marketplace group refuses an empty pick in the form; a form
// of several lists asks again after it instead (Plan 10 §4, R-W3-3), since huh validates a field
// on tab and shift+tab too, and would hold the cursor in a list left empty on purpose.
func (h *huhForms) withMarkets(st ModuleStep) (ModuleAnswer, error) {
	for {
		var cached []string
		picked := make([][]string, len(st.Markets))
		if err := h.run(h.marketFields(st, &cached, picked)...); err != nil {
			return ModuleAnswer{}, err
		}
		var chosen []string
		for _, c := range cached {
			if c != addSource {
				chosen = append(chosen, c)
			}
		}
		for _, p := range picked {
			chosen = append(chosen, p...)
		}
		if contains(cached, addSource) {
			return h.addSource(chosen)
		}
		if len(chosen) > 0 {
			return ModuleAnswer{Names: chosen}, nil
		}
		st.Problem = h.nothingSelected()
	}
}

// MarketEntry asks which entry a bare name means, one of the marketplaces listing it, nothing
// preselected (Plan 6c §3.3).
func (h *huhForms) MarketEntry(st MarketEntryStep) (string, error) {
	opts := make([]huh.Option[string], 0, len(st.Options))
	for _, o := range st.Options {
		opts = append(opts, huh.NewOption(h.cut(o.Label), o.Name))
	}
	var chosen string
	err := h.run(huh.NewSelect[string]().Title(h.cut(st.Name + " is not in your cache; your marketplaces list it. Install which?")).
		Options(opts...).Value(&chosen))
	return chosen, err
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

// marketFields is withMarkets' form, its answers going to cached and picked (one per
// marketplace). Each list is numbered (st.Step), and every list but the last says Enter moves on
// (Plan 10 §5). A lone marketplace group, browsed, refuses an empty pick.
func (h *huhForms) marketFields(st ModuleStep, cached *[]string, picked [][]string) []huh.Field {
	var fields []huh.Field
	n := 0 // lists so far
	if !st.Browse {
		desc := st.Problem
		if len(st.Markets) > 0 {
			desc = strings.TrimPrefix(desc+" · enter: next", " · ")
		}
		opts := make([]huh.Option[string], 0, len(st.Options)+1)
		for _, o := range st.Options {
			opts = append(opts, huh.NewOption(h.cut(o.Label), o.Name).Selected(contains(st.Preselect, o.Name)))
		}
		opts = append(opts, huh.NewOption("Add a source…", addSource))
		fields = append(fields, h.withProblem(st.Problem, h.multi(st.Step.title(n, "Install which modules?"), desc, opts, cached).Filterable(true))...)
		n++
	} else if h.accessible && st.Problem != "" {
		fields = append(fields, huh.NewNote().Title(h.cut(st.Problem)))
	}
	for i, g := range st.Markets {
		opts := make([]huh.Option[string], 0, len(g.Options))
		for _, o := range g.Options {
			opts = append(opts, huh.NewOption(h.cut(o.Label), o.Name).Selected(contains(st.Preselect, o.Name)))
		}
		desc := ""
		if st.Browse {
			desc = st.Problem
		}
		if i < len(st.Markets)-1 {
			desc = strings.TrimPrefix(desc+" · enter: next", " · ")
		}
		ms := h.multi(h.cut(st.Step.title(n, g.Title)), desc, opts, &picked[i]).Filterable(true)
		var f huh.Field = ms
		if st.Browse && len(st.Markets) == 1 {
			f = h.required(ms)
		}
		fields = append(fields, f)
		n++
	}
	return fields
}

func (h *huhForms) Assistants(st AssistantStep) ([]string, error) {
	opts := make([]huh.Option[string], 0, len(st.Options))
	for _, id := range st.Options {
		opts = append(opts, huh.NewOption(id, id).Selected(contains(st.Preselect, id)))
	}
	desc := ""
	if len(st.Scopes) > 1 {
		desc = "for " + strings.Join(st.Scopes, " and ")
	}
	var chosen []string
	err := h.run(h.required(h.multi(st.Step.title(0, "Install for which assistants?"), desc, opts, &chosen)))
	return chosen, err
}

func (h *huhForms) Components(pg ComponentPage) ([]string, error) {
	values := make([][]string, len(pg.Groups))
	if err := h.run(h.pageFields(pg, values)...); err != nil {
		return nil, err
	}
	var out []string
	for _, v := range values {
		out = append(out, v...)
	}
	return out, nil
}

// pageFields is a component page's form, each group's answer going to values: a filterable list
// per kind, numbered (pg.Step), every list but the last saying Enter moves on (Plan 10 §5); a
// disabled kind is a note, and the footer a note after them.
func (h *huhForms) pageFields(pg ComponentPage, values [][]string) []huh.Field {
	var fields []huh.Field
	last := -1 // the last list
	for i, g := range pg.Groups {
		if !g.Disabled {
			last = i
		}
	}
	n := 0 // lists so far
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
		desc := ""
		if i < last {
			desc = "enter: next"
		}
		fields = append(fields, h.multi(pg.Step.title(n, title), desc, opts, &values[i]).Filterable(true))
		n++
	}
	if pg.Footer != "" {
		fields = append(fields, huh.NewNote().Title(pg.Footer))
	}
	return fields
}

func (h *huhForms) ConfirmEmpty(module string) (bool, error) {
	ok := false
	return ok, h.run(huh.NewConfirm().Title("Install " + module + " with no components?").Value(&ok))
}

// Trust offers Trust all, Choose… (when there is anything to choose) and Cancel, with Cancel
// under the cursor, so Enter alone changes nothing; Choose… opens one form of filterable
// multi-selects, one per kind of change, with nothing preselected, since trust is security
// consent (Plan 6a §7.5). Choosing nothing cancels.
func (h *huhForms) Trust(st TrustStep) (engine.TrustAnswer, error) {
	const all, choose, cancel = "all", "choose", "cancel"
	opts := []huh.Option[string]{huh.NewOption(trustAllLabel(st.Changes), all)}
	if len(st.Groups) > 0 {
		opts = append(opts, huh.NewOption("Choose…", choose))
	}
	opts = append(opts, huh.NewOption("Cancel (the project is not changed)", cancel))
	choice := cancel
	if err := h.run(huh.NewSelect[string]().Title("Trust these changes?").Options(opts...).Value(&choice)); err != nil {
		return engine.TrustAnswer{}, err
	}
	switch choice {
	case all:
		return engine.TrustAnswer{All: true}, nil
	case cancel:
		return engine.TrustAnswer{}, ErrAborted
	}
	values := make([][]string, len(st.Groups))
	fields := make([]huh.Field, len(st.Groups))
	for i, g := range st.Groups {
		o := make([]huh.Option[string], len(g.Options))
		for k, opt := range g.Options {
			o[k] = huh.NewOption(h.cut(opt.Label), opt.Module)
		}
		fields[i] = h.multi(g.Title, "", o, &values[i]).Filterable(true)
	}
	if err := h.run(fields...); err != nil {
		return engine.TrustAnswer{}, err
	}
	var chosen []string
	for _, v := range values {
		chosen = append(chosen, v...)
	}
	if len(chosen) == 0 {
		return engine.TrustAnswer{}, ErrAborted
	}
	return engine.TrustAnswer{Modules: chosen}, nil
}

// trustAllLabel is the trust question's first answer for n changes.
func trustAllLabel(n int) string {
	if n == 1 {
		return "Trust this change"
	}
	return fmt.Sprintf("Trust all %d changes", n)
}

// Background asks whether the terminal's background is dark, in plainTheme: lola has no colors
// to choose until it knows.
func (h *huhForms) Background() (bool, error) {
	var choice string
	plain := huh.ThemeFunc(func(bool) *huh.Styles { return plainTheme() })
	if err := h.runIn(plain, huh.NewSelect[string]().Title("Is your terminal background dark or light?").
		Description("lola picks its colors for it and saves your answer in config.yaml").
		Options(huh.NewOption("Dark", config.ThemeDark), huh.NewOption("Light", config.ThemeLight)).Value(&choice)); err != nil {
		return false, err
	}
	return choice == config.ThemeDark, nil
}

func (h *huhForms) Confirm(question string) (bool, error) {
	ok := false
	err := h.run(huh.NewConfirm().Title(question).Value(&ok))
	return ok, err
}

// Profiles offers a source's profiles to add, filterable, nothing preselected; files whose names
// are not valid are counted in its description.
func (h *huhForms) Profiles(st ProfileStep) ([]string, error) {
	opts := make([]huh.Option[string], 0, len(st.Names))
	for _, n := range st.Names {
		opts = append(opts, huh.NewOption(h.cut(n), n))
	}
	desc := "from " + st.Source
	switch {
	case st.Invalid == 1:
		desc += "; 1 file with an invalid name is not offered"
	case st.Invalid > 1:
		desc += fmt.Sprintf("; %d files with invalid names are not offered", st.Invalid)
	}
	var chosen []string
	err := h.run(h.multi("Add which profiles?", h.cut(desc), opts, &chosen).Filterable(true))
	return chosen, err
}

// MarketName asks a marketplace's name, pre-filled with a free one, saying why the name it had
// will not do, and why the last answer was refused.
func (h *huhForms) MarketName(st MarketNameStep) (string, error) {
	var fields []huh.Field
	if st.Problem != "" {
		fields = append(fields, huh.NewNote().Title(h.cut(st.Problem)))
	}
	name := st.Value
	fields = append(fields, huh.NewInput().Title("Name the marketplace").
		Description(h.cut(st.Why)).Value(&name))
	err := h.run(fields...)
	return name, err
}

// TidyPick offers lola tidy's candidates, filterable, nothing preselected (Plan 6d §3.4).
func (h *huhForms) TidyPick(st TidyPickStep) ([]string, error) {
	var chosen []string
	err := h.run(h.tidyPick(st, &chosen)...)
	return chosen, err
}

// tidyPick is TidyPick's form: the multi-select, whose description counts and lists what is
// locked, cut to the terminal height; in accessible mode, where huh prints no description, a
// note above it lists every locked item.
func (h *huhForms) tidyPick(st TidyPickStep, value *[]string) []huh.Field {
	opts := h.tidyOptions(st.Options)
	if len(st.Locked) == 0 {
		return []huh.Field{h.multi("Move which to the trash?", "", opts, value).Filterable(true)}
	}
	title := fmt.Sprintf("%d locked, which lola tidy never moves:", len(st.Locked))
	lines := make([]string, len(st.Locked))
	for i, l := range st.Locked {
		lines[i] = h.cut("  " + l)
	}
	if h.accessible {
		return []huh.Field{huh.NewNote().Title(title).Description(strings.Join(lines, "\n")),
			h.multi("Move which to the trash?", "", opts, value).Filterable(true)}
	}
	if h.height > 0 {
		// multi caps the field at max(height-6, 3) rows; keep its title, the locked header and
		// min(len(opts), 3) option rows, and give the locked lines what is left.
		room := max(h.height-6, 3) - 2 - min(len(opts), 3)
		switch {
		case len(lines) <= room:
		case room < 2:
			lines = nil
		default:
			lines = append(lines[:room-1:room-1], fmt.Sprintf("  and %d more", len(lines)-room+1))
		}
	}
	desc := strings.Join(append([]string{title}, lines...), "\n")
	return []huh.Field{h.multi("Move which to the trash?", desc, opts, value).Filterable(true)}
}

// TidyKeep offers the candidates TidyPick left for the keep-list, filterable, nothing
// preselected (Plan 6d §3.4); when st.Required, it refuses an empty answer (Plan 10 §4).
func (h *huhForms) TidyKeep(st TidyKeepStep) ([]string, error) {
	var chosen []string
	ms := h.multi("Add any of the rest to your keep-list?", "what your keep-list names stays, and lola tidy stops offering it",
		h.tidyOptions(st.Options), &chosen).Filterable(true)
	var f huh.Field = ms
	if st.Required {
		f = h.required(ms)
	}
	err := h.run(f)
	return chosen, err
}

// tidyOptions are a tidy picker's options, each label cut to the terminal width unless that
// makes it equal to another's, and labels still equal in full numbered " [n]": huh v2.0.3 toggles
// every option whose label equals the one under the cursor, which would tick an item the user did
// not choose.
func (h *huhForms) tidyOptions(in []TidyOption) []huh.Option[string] {
	labels := make([]string, len(in))
	count := map[string]int{}
	for i, o := range in {
		labels[i] = h.cut(o.Label)
		count[labels[i]]++
	}
	full := map[string]int{}
	for i, o := range in {
		if count[labels[i]] > 1 {
			labels[i] = o.Label
		}
		full[labels[i]]++
	}
	used := map[string]bool{}
	for _, l := range labels {
		used[l] = true
	}
	n := map[string]int{}
	opts := make([]huh.Option[string], len(in))
	for i, o := range in {
		label := labels[i]
		if full[label] > 1 {
			base := label
			for {
				n[base]++
				label = fmt.Sprintf("%s [%d]", base, n[base])
				if !used[label] {
					break
				}
			}
			used[label] = true
		}
		opts[i] = huh.NewOption(label, o.ID)
	}
	return opts
}

// MigrateRoot asks for the new root of a missing one, pre-filled with the suggestion; an empty
// answer skips it. Accessible mode reads an empty answer as the pre-filled one, so there the
// suggestion is in the title and nothing is pre-filled.
func (h *huhForms) MigrateRoot(st MigrateRootStep) (string, error) {
	title, to := st.Old+" not found on this machine — new root (empty skips):", st.Suggest
	if h.accessible {
		title, to = st.Old+" not found on this machine — new root, such as "+st.Suggest+" (empty skips):", ""
	}
	desc := fmt.Sprintf("%d project path(s) of the Python lola start with it", st.Records)
	err := h.run(huh.NewInput().Title(h.cut(title)).Description(desc).Value(&to).Validate(func(s string) error {
		if s = strings.TrimSpace(s); s != "" && !filepath.IsAbs(s) {
			return fmt.Errorf("write an absolute path, such as %s, or leave it empty to skip", st.Suggest)
		}
		return nil
	}))
	return strings.TrimSpace(to), err
}

// MigrateTargets offers the assistants on your PATH for the records of one this machine lacks,
// all preselected; choosing none skips them.
func (h *huhForms) MigrateTargets(st MigrateTargetStep) ([]string, error) {
	why, which := " is not installed here", fmt.Sprintf("these %d modules", st.Modules)
	if !st.Supported {
		why = " has no lola target"
	}
	if st.Modules == 1 {
		which = "its module"
	}
	opts := make([]huh.Option[string], 0, len(st.Options))
	for _, id := range st.Options {
		opts = append(opts, huh.NewOption(id, id).Selected(true))
	}
	var chosen []string
	err := h.run(h.multi(h.cut(st.Old+why+" — install "+which+" for:"),
		"choosing none leaves them as they are", opts, &chosen))
	return chosen, err
}

// reviewBack is the Change menu's way back to the review; it is not an answer to the flow.
const reviewBack = "back"

// reviewLabels name the review menu's entries.
var reviewLabels = map[string]string{ReviewApply: "Apply", ReviewScope: "Change scope", ReviewModules: "Change modules",
	ReviewAssistants: "Change assistants", ReviewComponents: "Change components", ReviewQuit: "Quit", reviewBack: "Back to the review"}

// Review shows the summary and asks what to do with it (Plan 10 §5). On a full terminal it is the
// viewer's confirm mode: y applies, n cancels, and c opens the menu of the steps to change (the
// Change entries of st.Options, or back to the review), whose answer the flow asks again before it
// comes back to the review; with no step to change, the viewer does not offer c. In accessible mode,
// and wherever paging bypasses the viewer (--no-pager, NO_PAGER, pager: false, an explicit
// $LOLA_PAGER), the summary is printed, then the menu of every entry, Apply first and the default.
// Either way the summary is headed by the review's number when the pass asked other steps.
func (h *huhForms) Review(st ReviewStep) (string, error) {
	if st.Step.Of > 1 {
		st.Summary = st.Step.title(0, "Review") + "\n" + st.Summary
	}
	if !h.accessible && h.viewer() {
		changes := slices.DeleteFunc(slices.Clone(st.Options), func(o string) bool { return o == ReviewApply || o == ReviewQuit })
		for {
			ans, err := h.view(st.Summary, len(changes) > 0)
			switch {
			case err != nil:
				return "", err
			case ans == viewApply:
				return ReviewApply, nil
			case ans == viewCancel:
				return "", ErrAborted
			}
			choice, err := h.menu("Change what?", append(slices.Clone(changes), reviewBack), changes[0])
			if err != nil || choice != reviewBack {
				return choice, err
			}
		}
	}
	fmt.Fprint(h.out, st.Summary)
	choice, err := h.menu("Apply these changes?", st.Options, ReviewApply)
	if err == nil && choice == ReviewQuit {
		return "", ErrAborted
	}
	return choice, err
}

// menu asks title over the review menu's entries opts, labelled, with choice under the cursor.
func (h *huhForms) menu(title string, opts []string, choice string) (string, error) {
	options := make([]huh.Option[string], 0, len(opts))
	for _, o := range opts {
		options = append(options, huh.NewOption(reviewLabels[o], o))
	}
	if err := h.run(huh.NewSelect[string]().Title(title).Description("Ctrl+C cancels; nothing is written").
		Options(options...).Value(&choice)); err != nil {
		return "", err
	}
	return choice, nil
}
