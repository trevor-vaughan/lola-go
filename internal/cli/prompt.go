package cli

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"charm.land/huh/v2"
	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// interactive reports whether lola may prompt: both stdin and stderr are terminals.
func interactive(cmd *cobra.Command) bool {
	return isTTY(cmd.InOrStdin()) && isTTY(cmd.ErrOrStderr())
}

// confirm asks before a destructive action; yes skips it. The summary was already printed.
func (a *app) confirm(cmd *cobra.Command, yes bool, question string) error {
	if yes {
		return nil
	}
	if !interactive(cmd) {
		return errs.WithHints(fmt.Errorf("%s needs confirmation, and there is no terminal to ask on", cmd.CommandPath()), "pass -y to confirm")
	}
	ok := false
	err := huh.NewForm(huh.NewGroup(huh.NewConfirm().Title(question).Value(&ok))).
		WithInput(cmd.InOrStdin()).WithOutput(cmd.ErrOrStderr()).WithAccessible(a.accessible()).Run()
	if errors.Is(err, huh.ErrUserAborted) || (err == nil && !ok) {
		return ErrAborted
	}
	return err
}

// hookOptions sets the hook options of a command. Install hooks (§9.1): --allow-hooks grants
// consent for the run, and hook output goes to stderr. Runtime hooks (Plan 5b design §7.3):
// --allow-runtime-hooks grants and records consent. Otherwise, on a terminal, lola asks per
// module, defaulting to No.
func (a *app) hookOptions(cmd *cobra.Command, opts *engine.Options, allow, allowRuntime bool) {
	opts.AllowHooks, opts.HookOutput, opts.AllowRuntimeHooks = allow, cmd.ErrOrStderr(), allowRuntime
	if !interactive(cmd) || a.json {
		return
	}
	if !allow {
		opts.Ask = func(module string, hooks []engine.HookScript) (bool, error) {
			w := cmd.ErrOrStderr()
			fmt.Fprintf(w, "%s has install hooks. They run as you, unconfined:\n", module)
			for _, h := range hooks {
				fmt.Fprintf(w, "    %s: %s (sha256 %s)\n", h.Event, untrusted.Text(h.Script, len(h.Script)), h.SHA256)
			}
			fmt.Fprintln(w, "  Consent covers only the scripts listed, not other files they run; review those too.")
			return a.ask(cmd, "Run "+module+"'s install hooks?")
		}
	}
	if !allowRuntime {
		opts.AskRuntimeHooks = func(module string, hooks []engine.RuntimeHook) (bool, error) {
			w := cmd.ErrOrStderr()
			what := "Claude Code hooks"
			if slices.ContainsFunc(hooks, func(h engine.RuntimeHook) bool { return h.MCPServer != "" }) {
				what = "Claude Code hooks and agent MCP servers"
			}
			fmt.Fprintf(w, "%s has %s:\n", module, what)
			for _, h := range hooks {
				for _, line := range runtimeHookLines(h) {
					fmt.Fprintln(w, "    "+line)
				}
			}
			fmt.Fprintln(w, "  These run inside Claude Code with your permissions.")
			return a.ask(cmd, "Install "+module+"'s "+what+"?")
		}
	}
}

// ask asks a yes/no question on stderr, defaulting to No. In accessible mode end of input is a
// cancel, not the default: huh would otherwise answer No and the install would go on.
func (a *app) ask(cmd *cobra.Command, question string) (bool, error) {
	ok := false
	accessible := a.accessible()
	in := &eofReader{r: cmd.InOrStdin()}
	form := huh.NewForm(huh.NewGroup(huh.NewConfirm().Title(question).Value(&ok))).
		WithOutput(cmd.ErrOrStderr()).WithAccessible(accessible)
	if accessible {
		form = form.WithInput(in)
	} else {
		form = form.WithInput(cmd.InOrStdin())
	}
	err := form.Run()
	if errors.Is(err, huh.ErrUserAborted) || in.eof {
		return false, ErrAborted
	}
	return ok, err
}

// runtimeHookLines describes one runtime hook for consent and the summary: its event, matcher
// and type with the conditions that are set, then what it runs or where it sends data. An
// agent's inline MCP server is described the same way, by its name and transport, then its
// command line or URL, headers and env. The engine has already made every string safe to print.
func runtimeHookLines(h engine.RuntimeHook) []string {
	head := h.Event
	if h.MCPServer != "" {
		head = "MCP server " + h.MCPServer
	}
	if h.Component != "" {
		head = h.Component + ": " + head
	}
	if h.Matcher != "" {
		head += ", matcher " + h.Matcher
	}
	head += ": " + h.Type
	if h.If != "" {
		head += ", if " + h.If
	}
	if h.Async {
		head += ", async"
	}
	if h.Shell != "" {
		head += ", shell " + h.Shell
	}
	lines := []string{head}
	add := func(label, value string) {
		if value != "" {
			lines = append(lines, "  "+label+": "+value)
		}
	}
	add("runs", h.Command)
	if h.MCPServer != "" {
		add("connects to", h.URL)
	} else {
		add("sends data to", h.URL)
	}
	for _, header := range h.Headers {
		add("header", header)
	}
	for _, env := range h.Env {
		add("env", env)
	}
	add("sends env", strings.Join(h.AllowedEnvVars, ", "))
	if h.Tool != "" {
		add("calls tool", h.Tool+" on MCP server "+h.Server)
	}
	add("prompt", h.Prompt)
	add("fields", h.Fields)
	add("group fields", h.GroupFields)
	// untrusted.Text indents each continuation line by two spaces; showing those newlines as
	// "\n" keeps every value on its own line, so none can pose as another hook line.
	for i := range lines {
		lines[i] = strings.ReplaceAll(lines[i], "\n  ", `\n`)
	}
	return lines
}
