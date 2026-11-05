package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// pager shows lola sync's consent diffs (Plan 6b §9, §19 R12, S9) and lola market's listing of a
// marketplace git-style: through the user's pager when lola pages, else as before. on is decided
// once per command.
type pager struct {
	a   *app
	cmd *cobra.Command
	eng *engine.Engine
	on  bool
}

// pager decides whether this command pages: human output (not --json or --plain), stdout and
// stderr both terminals (the diff would go to stderr, the question asks there), no --no-pager,
// and pager: true (the default) in the user's config.yaml, read only when the rest allow paging,
// and never from a project.
func (a *app) pager(cmd *cobra.Command, eng *engine.Engine) (*pager, error) {
	p := &pager{a: a, cmd: cmd, eng: eng}
	outTTY := a.termOut || isTTY(cmd.OutOrStdout())
	errTTY := a.termErr || isTTY(cmd.ErrOrStderr())
	if a.json || a.plain || a.noPager || !outTTY || !errTTY {
		return p, nil
	}
	cfg, err := config.LoadConfig(eng.Paths.Config)
	if err != nil {
		return nil, err
	}
	p.on = cfg.Pager
	return p, nil
}

// show prints with print: to the pager when lola pages, uncapped (print gets more ""), and
// otherwise to w in style s, capped, with more naming the command that lists everything. The
// pager's output goes to the terminal on stdout, its color decided by that terminal, not the
// pipe. show returns once the pager exits, so a question asked next is never inside it. A pager
// that cannot be found, is found in the current directory (exec.ErrDot), cannot start or exits
// non-zero is a warning, and the diff is then printed unpaged: consent is never given unseen.
func (p *pager) show(w io.Writer, s style, more string, print func(io.Writer, style, string)) {
	argv := p.command()
	if !p.on || argv == nil {
		print(w, s, more)
		return
	}
	warn := func(problem string) {
		fmt.Fprintln(p.cmd.ErrOrStderr(), style{color: p.a.color(p.cmd.ErrOrStderr())}.warn(problem+"; showing it here"))
		print(w, s, more)
	}
	name := untrusted.Text(argv[0], untrusted.MaxName)
	path, err := exec.LookPath(argv[0])
	switch {
	case errors.Is(err, exec.ErrDot):
		warn("not paging: the pager " + name + " is in the current directory, which lola never runs a program from")
		return
	case err != nil:
		warn("not paging: the pager " + name + " was not found")
		return
	}
	var buf bytes.Buffer
	print(&buf, style{color: p.a.color(p.cmd.OutOrStdout())}, "")
	run := exec.Command(path, argv[1:]...)
	run.Stdin, run.Stdout, run.Stderr = &buf, p.cmd.OutOrStdout(), p.cmd.ErrOrStderr()
	run.Env = p.eng.Environ()
	if p.a.getenv("LESS") == "" {
		run.Env = append(run.Env, "LESS=FRX")
	}
	// Ctrl+C belongs to the pager while it runs, as in git: lola ignores it rather than die
	// with the question unasked.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	err = run.Run()
	signal.Stop(sig)
	if err != nil {
		warn("the pager " + name + " failed: " + untrusted.CleanErr(err).Error())
	}
}

// command is the pager to run, split on whitespace with no shell: $LOLA_PAGER, else $PAGER,
// else less (more on Windows); nil when the value is cat, which means no pager. An empty
// variable cannot be told from an unset one through getenv, so it falls through to the next.
func (p *pager) command() []string {
	v := p.a.getenv("LOLA_PAGER")
	if strings.TrimSpace(v) == "" {
		v = p.a.getenv("PAGER")
	}
	if strings.TrimSpace(v) == "" {
		v = "less"
		if runtime.GOOS == "windows" {
			v = "more"
		}
	}
	argv := strings.Fields(v)
	if argv[0] == "cat" && len(argv) == 1 {
		return nil
	}
	return argv
}
