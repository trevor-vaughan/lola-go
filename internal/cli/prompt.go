package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// interactive reports whether lola may prompt: both stdin and stderr are terminals.
func interactive(cmd *cobra.Command) bool {
	return isTTY(cmd.InOrStdin()) && isTTY(cmd.ErrOrStderr())
}

// confirm asks question, at nav in its command's flow, before a destructive action, after show prints the plan on stderr in the style it is given, or, on a
// terminal, in the viewer when it is taller than the terminal (or the user's pager; see page); the
// injected Prompter or the huh adapter asks, under --json too, since the result goes to stdout.
// Off a terminal it refuses, hinting the run (args are its names) with -y. Under --json that
// refusal is all lola prints: one JSON line carrying plan, the plan it would have asked about
// (what -n --json prints, where the command has -n), so a script reads the plan
// without a second run.
func (a *app) confirm(cmd *cobra.Command, args []string, question string, nav Nav, plan any, show func(io.Writer, style)) error {
	var p Prompter
	if a.newPrompter != nil {
		p = a.newPrompter(cmd)
	} else {
		p = a.huhPrompter(cmd)
	}
	if p.Interactive() {
		// page returns once the viewer or pager exits, so the question is never asked inside it.
		if err := a.pageThen(cmd, cmd.ErrOrStderr(), style{color: a.color(cmd.ErrOrStderr()), asking: true}, question, show); err != nil {
			return err
		}
		ok, err := p.Confirm(question, nav)
		if err == nil && !ok {
			return ErrAborted
		}
		return err
	}
	// -y answers exactly this question, the one the plan asks.
	err := errs.WithHints(fmt.Errorf("%s needs confirmation, and there is no terminal to ask on", cmd.CommandPath()),
		runWithout(cmd, args, "yes")+" -y")
	if !a.json {
		show(cmd.ErrOrStderr(), style{color: a.color(cmd.ErrOrStderr()), asking: true})
		return err
	}
	fields, ferr := planFields(plan)
	if ferr != nil {
		return ferr
	}
	return &confirmRefusal{error: err, plan: fields}
}

// deletesRefusal is err, an install's refusal to delete files unasked (engine.DeletesRefusedError),
// with the plan shown as lola uninstall's refusal shows it: the removals on stderr, or under --json
// the plan in the refusal's JSON line. Any other error is returned as it is.
func (a *app) deletesRefusal(cmd *cobra.Command, err error) error {
	refused, ok := errors.AsType[*engine.DeletesRefusedError](err)
	if !ok {
		return err
	}
	if !a.json {
		printRemovals(cmd.ErrOrStderr(), style{color: a.color(cmd.ErrOrStderr()), plain: a.plain}, refused.Removals)
		return err
	}
	fields, ferr := planFields(refused.Plan)
	if ferr != nil {
		return ferr
	}
	return &confirmRefusal{error: err, plan: fields}
}

// confirmRefusal is confirm's refusal under --json: its JSON line carries the plan's keys.
type confirmRefusal struct {
	error
	plan map[string]any
}

func (e *confirmRefusal) Unwrap() error              { return e.error }
func (e *confirmRefusal) JSONFields() map[string]any { return e.plan }

// planFields is what -n --json prints for plan, where the command has -n, key by key, each
// value the bytes encoding/json writes for it there, so a refusal and the dry run cannot drift.
// Like that document, it carries paths lola would write raw and author text already cleaned.
func planFields(plan any) (map[string]any, error) {
	b, err := json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	fields := make(map[string]any, len(raw))
	for k, v := range raw {
		fields[k] = v
	}
	return fields, nil
}

// askScript shows the project's install script q, beside the project's root, and asks p whether to
// run it, or, with no copy for it to wrap now (q.Later), to trust it for later runs.
func askScript(cmd *cobra.Command, p Prompter, q engine.ScriptQuestion) (bool, error) {
	w := cmd.ErrOrStderr()
	fmt.Fprintf(w, "The project at %s has a %s script. It runs as you, unconfined, for every module lola installs there:\n", q.Root, q.Event)
	fmt.Fprintf(w, "    %s (sha256 %s)\n", q.Path, q.SHA256)
	fmt.Fprintln(w, "  Consent covers only the script listed, not other files it runs; review those too.")
	if q.Later {
		return p.Consent("Trust this project's " + q.Event + " script for later runs?")
	}
	return p.Consent("Run this project's " + q.Event + " script?")
}

// hookOptions sets the hook options of a command: install hooks' and scripts' output goes to
// stderr, a refusal for want of trust names the command, and on a terminal lola asks,
// defaulting to No, about the project's install scripts, and per module about the
// install hooks and runtime hooks, that opts.Trust does not cover;
// the engine asks only about those.
func (a *app) hookOptions(cmd *cobra.Command, opts *engine.Options) {
	opts.HookOutput, opts.Run = cmd.ErrOrStderr(), cmd.CommandPath()
	p := a.prompter(cmd) // never asks under --json
	if !p.Interactive() {
		return
	}
	opts.AskScript = func(q engine.ScriptQuestion) (bool, error) { return askScript(cmd, p, q) }
	opts.Ask = func(module string, hooks []engine.HookScript) (bool, error) {
		w := cmd.ErrOrStderr()
		fmt.Fprintf(w, "%s has install hooks. They run as you, unconfined:\n", module)
		for _, h := range hooks {
			fmt.Fprintf(w, "    %s: %s (sha256 %s)\n", h.Event, untrusted.Text(h.Script, len(h.Script)), h.SHA256)
		}
		fmt.Fprintln(w, "  Consent covers only the scripts listed, not other files they run; review those too.")
		return p.Consent("Run " + module + "'s install hooks?")
	}
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
		return p.Consent("Install " + module + "'s " + what + "?")
	}
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
