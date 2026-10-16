package cli

import (
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
)

// profileUpdate runs lola profile <name> --update (Plan 6b §3.2, §6.5): one scope, resolved as
// every action's is; how the profile changes, paged, then the question, or --trust off a
// terminal, which the engine checks before any network contact; then, when the scope uses the
// profile, a sync of its modules there, with sync's own questions and answers (-y, --trust).
func (a *app) profileUpdate(cmd *cobra.Command, eng *engine.Engine, args []string, f syncFlags) error {
	if len(args) != 1 {
		return errs.WithHints(errors.New("--update takes one remote profile's name"),
			"see yours: "+runWithout(cmd, nil, "update", "yes", "trust", "dry-run", "force", "allow-hooks", "allow-runtime-hooks"))
	}
	scs, err := a.scopes(cmd, eng, "Apply the update where?", false, true)
	if err != nil {
		return err
	}
	pg, err := a.pager(cmd, eng)
	if err != nil {
		return err
	}
	sc := scs[0].Scope
	// The hook flags as given, which every command repeating the run keeps (SyncRequest.rerun).
	var hookFlags []string
	if f.allowHooks {
		hookFlags = append(hookFlags, "--allow-hooks")
	}
	if f.allowRuntime {
		hookFlags = append(hookFlags, "--allow-runtime-hooks")
	}
	flags := slices.Clone(hookFlags)
	if f.yes {
		flags = append(flags, "-y")
	}
	req := engine.ProfileUpdateRequest{Scope: sc, Name: args[0], DryRun: f.dry, Flags: flags}
	p := a.prompter(cmd) // never asks under --json
	stderr := style{color: a.color(cmd.ErrOrStderr())}
	// Unpaged, the content is capped, naming the command that lists all of it.
	listAll := eng.Command(sc, "profile", args[0], "--update", "-n", "--json")
	show := func(rps []engine.RemoteProfile) {
		pg.show(cmd.ErrOrStderr(), stderr, listAll, func(w io.Writer, s style, more string) {
			printRemoteProfiles(w, rps, func(engine.RemoteProfile) string { return more })
		})
	}
	switch {
	case f.trust:
		req.Consent = func(rps []engine.RemoteProfile) (bool, error) {
			if !a.json {
				show(rps)
			}
			return true, nil
		}
	case p.Interactive():
		req.Consent = func(rps []engine.RemoteProfile) (bool, error) {
			show(rps)
			return p.Confirm("Update " + rps[0].Profile + "?")
		}
	}
	res, err := eng.UpdateProfile(cmd.Context(), req)
	if errors.Is(err, engine.ErrDeclined) || errors.Is(err, ErrAborted) {
		return &abortedError{"aborted; nothing was updated."}
	}
	if err != nil {
		return err
	}
	var synced *engine.SyncResult
	more := ""
	if len(res.Apply) > 0 {
		// The update is kept whatever the sync does: a refused or declined sync says so.
		if synced, more, err = a.syncScope(cmd, eng, pg, sc, res.Apply, nil, f); err != nil {
			// The update's own notices (the other scope, what else is pending) are not lost.
			hints := []string{"apply it later: " + eng.SyncCommand(sc, append(slices.Clone(res.Apply), hookFlags...)...)}
			for _, n := range res.Notices {
				if a.json {
					hints = append(hints, n.Text)
					continue
				}
				fmt.Fprintln(cmd.ErrOrStderr(), noticeLine(style{color: a.color(cmd.ErrOrStderr())}, n))
			}
			return errs.WithHints(fmt.Errorf("%s is updated, but not applied to %s: %w", res.Profiles[0].Profile, unchanged(sc), err), hints...)
		}
	}
	return a.emit(cmd, func(w io.Writer, s style) {
		if res.DryRun {
			pg.show(w, s, listAll, func(w io.Writer, _ style, more string) {
				printRemoteProfiles(w, res.Profiles, func(engine.RemoteProfile) string { return more })
			})
		}
		printRemoteResult(w, s, &res.RemoteResult)
		if synced != nil {
			printSync(w, s, manifestName(sc), synced, res.Apply, more)
		}
	}, profileUpdateDoc{ProfileUpdateResult: res, Sync: synced})
}

// profileUpdateDoc is lola profile --update's JSON document: the update's result (remote_profiles,
// notices, dry_run, apply) and the sync that applied it in the run's scope, null when it applied
// nothing.
type profileUpdateDoc struct {
	*engine.ProfileUpdateResult
	Sync *engine.SyncResult `json:"sync"`
}
