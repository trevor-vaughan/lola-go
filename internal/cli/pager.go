package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// paging is how human output reaches the terminal: printed (the zero value), shown
// in the viewer when it is taller than the terminal, or run through argv, the pager an explicit
// $LOLA_PAGER names, run in env.
type paging struct {
	viewer bool
	argv   []string
	env    []string
}

// paging decides how cmd's human output reaches the terminal, the first rule that matches
// winning: --json or --plain, stdout or stderr not a terminal, accessible mode (line prompts for
// screen readers never get a full screen), --no-pager, NO_PAGER set, or pager: false in the
// user's config.yaml (read only when the rules before it allow paging, and
// never from a project) print it; an explicitly set $LOLA_PAGER runs that command, split on
// whitespace with no shell, and cat alone prints; otherwise the viewer. $PAGER is not read. A
// config.yaml lola cannot read is a warning on stderr, and the output is printed: it never hides
// what a command did.
func (a *app) paging(cmd *cobra.Command) paging {
	outTTY := a.termOut || isTTY(cmd.OutOrStdout())
	errTTY := a.termErr || isTTY(cmd.ErrOrStderr())
	if a.json || a.plain || !outTTY || !errTTY || a.accessible() || a.noPager || a.getenv("NO_PAGER") != "" {
		return paging{}
	}
	eng, err := a.engine()
	var cfg *config.Config
	if err == nil {
		cfg, err = config.LoadConfig(eng.Paths.Config, eng.Paths.Home)
	}
	if err != nil {
		w := cmd.ErrOrStderr()
		fmt.Fprintln(w, style{color: a.color(w)}.warn(untrusted.CleanErr(err).Error()+"; printing without the viewer"))
		for _, h := range errs.Hints(err) {
			fmt.Fprintln(w, hintLine("  ", h, a.style(cmd).width))
		}
		return paging{}
	}
	if !cfg.Pager {
		return paging{}
	}
	if v := a.getenv("LOLA_PAGER"); strings.TrimSpace(v) != "" {
		argv := strings.Fields(v)
		if len(argv) == 1 && argv[0] == "cat" {
			return paging{}
		}
		return paging{argv: argv, env: eng.Environ()}
	}
	return paging{viewer: true}
}

// page prints what print writes to w in style s, unless paging says otherwise: then print renders
// with s.paged set, uncapped, and the text goes through the user's pager, or, when it is taller
// than the terminal, the viewer; text that fits is printed as it is. page returns once the pager
// or the viewer exits, so a question asked next is never inside it. A pager that cannot be found,
// is found in the current directory (exec.ErrDot), cannot start or exits non-zero, and a viewer
// that cannot start, is a warning, and the text is then printed capped: consent is never given
// unseen.
func (a *app) page(cmd *cobra.Command, w io.Writer, s style, print func(io.Writer, style)) {
	_ = a.pageThen(cmd, w, s, "", print) // with no question to follow, Ctrl+C only closes the viewer: always nil
}

// pageThen is page for output that a question follows: then names it ("Migrate?"), and the
// viewer's footer says q continues to it rather than quits. Ctrl+C in the viewer ends the command
// instead: pageThen returns ErrAborted, and the question is not asked.
func (a *app) pageThen(cmd *cobra.Command, w io.Writer, s style, then string, print func(io.Writer, style)) error {
	mode := a.paging(cmd)
	if !mode.viewer && mode.argv == nil {
		print(w, s)
		return nil
	}
	out := cmd.OutOrStdout()
	full := style{color: a.color(out), paged: true, verbose: s.verbose, asking: s.asking, width: s.width}
	var buf bytes.Buffer
	print(&buf, full)
	warn := func(problem string) {
		fmt.Fprintln(cmd.ErrOrStderr(), style{color: a.color(cmd.ErrOrStderr())}.warn(problem+"; showing it here"))
		print(w, s)
	}
	if mode.argv != nil {
		if problem := a.runPager(cmd, mode, &buf); problem != "" {
			warn(problem)
		}
		return nil
	}
	width, height := a.width(out), a.height(out)
	if width == 0 || height == 0 || fits(buf.String(), 1, width, height) {
		fmt.Fprint(w, buf.String())
		return nil
	}
	ans, err := a.view(cmd, buf.String(), false, false, Nav{}, then)
	switch {
	case err != nil:
		warn("the viewer failed: " + untrusted.CleanErr(err).Error())
	case ans == viewCancel && then != "":
		return ErrAborted
	}
	return nil
}

// fits reports whether text, then rows more under it, shows whole on a terminal width columns
// wide and height rows high, the text wrapped to the width as the viewer wraps it. It decides
// inline or the pager for a question (rows: its prompt line at its widest, promptRows) and for
// read-only output (rows: 1, the shell's prompt that follows).
func fits(text string, rows, width, height int) bool {
	lines := 0
	if text = strings.TrimSuffix(text, "\n"); text != "" {
		lines = strings.Count(ansi.Wrap(text, width, ""), "\n") + 1
	}
	return lines+rows <= height
}

// runPager runs mode's pager with text on its stdin, writing to the terminal on stdout, and returns
// what went wrong, or "". Ctrl+C belongs to the pager while it runs, as in git: lola ignores it
// rather than die with the question unasked.
func (a *app) runPager(cmd *cobra.Command, mode paging, text io.Reader) string {
	name := untrusted.Text(mode.argv[0], untrusted.MaxName)
	path, err := exec.LookPath(mode.argv[0])
	switch {
	case errors.Is(err, exec.ErrDot):
		return "not paging: the pager " + name + " is in the current directory, which lola never runs a program from"
	case err != nil:
		return "not paging: the pager " + name + " was not found"
	}
	run := exec.Command(path, mode.argv[1:]...)
	run.Stdin, run.Stdout, run.Stderr = text, cmd.OutOrStdout(), cmd.ErrOrStderr()
	run.Env = mode.env
	if a.getenv("LESS") == "" {
		run.Env = append(run.Env, "LESS=FRX")
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	err = run.Run()
	signal.Stop(sig)
	if err != nil {
		return "the pager " + name + " failed: " + untrusted.CleanErr(err).Error()
	}
	return ""
}
