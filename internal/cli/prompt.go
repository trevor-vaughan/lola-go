package cli

import (
	"errors"
	"fmt"

	"charm.land/huh/v2"
	"github.com/spf13/cobra"

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
