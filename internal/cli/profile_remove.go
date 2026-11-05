package cli

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
)

// profileRemove runs lola profile <name> --remove (Plan 6b §3.2): one scope, resolved as every
// action's is, which with user scope and the project found from here the engine checks the
// profile is not in use in (--force overrides that); then the question, on a terminal, or -y.
// --force answers it too (user decision, 2026-10-22).
func (a *app) profileRemove(cmd *cobra.Command, eng *engine.Engine, args []string, f syncFlags) error {
	if len(args) != 1 {
		return errs.WithHints(errors.New("--remove takes one remote profile's name"),
			"see yours: "+runWithout(cmd, nil, "remove", "yes", "trust", "dry-run", "force"))
	}
	scs, err := a.scopes(cmd, eng, args, "Check which scope before removing it?", false, true)
	if err != nil {
		return err
	}
	sc := scs[0].Scope
	req := engine.ProfileRemoveRequest{Scope: sc, Name: args[0], Force: f.force, DryRun: f.dry, Yes: f.yes, Display: outputFlags(cmd)}
	switch p := a.prompter(cmd); {
	case f.yes:
		req.Confirm = func(engine.RemoteProfile) (bool, error) { return true, nil }
	case p.Interactive():
		req.Confirm = func(rp engine.RemoteProfile) (bool, error) {
			return p.Confirm("Remove "+rp.Profile+" ("+rp.File+")? Its modules stay installed until each scope's next sync", Nav{})
		}
	}
	res, err := eng.RemoveProfile(cmd.Context(), req)
	if errors.Is(err, engine.ErrDeclined) || errors.Is(err, ErrAborted) {
		return &abortedError{"aborted; nothing was removed."}
	}
	if err != nil {
		return err
	}
	return a.emit(cmd, func(w io.Writer, s style) {
		for _, n := range res.Notices {
			fmt.Fprintln(w, noticeLine(s, n))
		}
		switch {
		case s.plain && res.DryRun:
			fmt.Fprintf(w, "would-remove\t%s\t%s\n", res.Profile, res.File)
		case s.plain:
			fmt.Fprintf(w, "removed\t%s\t%s\n", res.Profile, res.File)
		case res.DryRun:
			fmt.Fprintln(w, s.info("would remove "+res.Profile+" ("+res.File+")"))
		default:
			fmt.Fprintln(w, s.ok("removed "+res.Profile+" ("+res.File+")"))
		}
	}, res)
}
