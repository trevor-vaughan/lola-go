package cli

import (
	"errors"
	"fmt"

	"charm.land/huh/v2"
	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
)

// interactive reports whether lola may prompt: both stdin and stderr are terminals.
func interactive(cmd *cobra.Command) bool {
	return isTTY(cmd.InOrStdin()) && isTTY(cmd.ErrOrStderr())
}

// confirm asks before a destructive action; yes skips it. The summary was already printed.
func confirm(cmd *cobra.Command, yes bool, question string) error {
	if yes {
		return nil
	}
	if !interactive(cmd) {
		return errs.WithHints(fmt.Errorf("%s needs confirmation, and there is no terminal to ask on", cmd.CommandPath()), "pass -y to confirm")
	}
	ok := false
	err := huh.NewForm(huh.NewGroup(huh.NewConfirm().Title(question).Value(&ok))).
		WithInput(cmd.InOrStdin()).WithOutput(cmd.ErrOrStderr()).Run()
	if errors.Is(err, huh.ErrUserAborted) || (err == nil && !ok) {
		return errAborted
	}
	return err
}

// pickTargets lets the user choose among detected targets, all preselected.
func pickTargets(cmd *cobra.Command, detected []string) ([]string, error) {
	var opts []huh.Option[string]
	for _, id := range detected {
		opts = append(opts, huh.NewOption(id, id).Selected(true))
	}
	var chosen []string
	err := huh.NewForm(huh.NewGroup(huh.NewMultiSelect[string]().Title("Install for which assistants?").Options(opts...).Value(&chosen))).
		WithInput(cmd.InOrStdin()).WithOutput(cmd.ErrOrStderr()).Run()
	if errors.Is(err, huh.ErrUserAborted) || (err == nil && len(chosen) == 0) {
		return nil, errAborted
	}
	return chosen, err
}

// hookOptions sets the install hook options of a command (§9.1): --allow-hooks grants consent
// for the run; otherwise, on a terminal, lola asks per module, defaulting to No. Hook output
// goes to stderr.
func (a *app) hookOptions(cmd *cobra.Command, opts *engine.Options, allow bool) {
	opts.AllowHooks, opts.HookOutput = allow, cmd.ErrOrStderr()
	if allow || !interactive(cmd) || a.json {
		return
	}
	opts.Ask = func(module string, hooks []engine.HookScript) (bool, error) {
		w := cmd.ErrOrStderr()
		fmt.Fprintf(w, "%s has install hooks. They run as you, unconfined:\n", module)
		for _, h := range hooks {
			fmt.Fprintf(w, "    %s: %s (sha256 %s)\n", h.Event, h.Script, h.SHA256)
		}
		fmt.Fprintln(w, "  Consent covers only the scripts listed, not other files they run; review those too.")
		ok := false
		err := huh.NewForm(huh.NewGroup(huh.NewConfirm().Title("Run " + module + "'s install hooks?").Value(&ok))).
			WithInput(cmd.InOrStdin()).WithOutput(w).Run()
		if errors.Is(err, huh.ErrUserAborted) {
			return false, errAborted
		}
		return ok, err
	}
}
