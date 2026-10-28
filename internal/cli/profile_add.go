package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// remoteFlags are lola profile --add's own flags: its source, and the ref and --path to fetch.
type remoteFlags struct{ source, ref, path string }

// profileAdd runs lola profile [<name>] --add <source> (Plan 6b §3.2, §6.5): it asks no scope (§19
// P22); a scope flag, when given, only names the scope whose installed modules the profile's
// content is compared with, else the engine picks; on a terminal the picker when no name is given,
// then the profile and its content, paged, and the question; off a terminal --trust, without which
// the engine refuses before any network contact.
func (a *app) profileAdd(cmd *cobra.Command, eng *engine.Engine, args []string, rf remoteFlags, f syncFlags) error {
	if len(args) > 1 {
		var hints []string
		for _, n := range args {
			hints = append(hints, "add "+untrusted.Text(n, untrusted.MaxName)+": "+runWithout(cmd, []string{n}))
		}
		return errs.WithHints(errors.New("--add takes one profile name; add them one at a time"), hints...)
	}
	given, err := a.flagScope(cmd, eng, args, "--add compares the content with one")
	if err != nil {
		return err
	}
	req := engine.ProfileAddRequest{Source: rf.source, Ref: rf.ref, Path: rf.path, DryRun: f.dry, Rerun: rerunFlags(cmd, f.trust),
		Trust: f.trust, Receipt: a.receipt(cmd, f.trust, false)}
	if len(args) == 1 {
		req.Name = args[0]
	}
	if len(given) == 1 {
		req.Scope = &given[0]
	}
	p := a.prompter(cmd) // never asks under --json
	stderr := style{color: a.color(cmd.ErrOrStderr())}
	// Unpaged, the content is capped, naming the command that lists all of a profile's, picked or
	// named; paged, it is not.
	printer := func(rps []engine.RemoteProfile) func(io.Writer, style) {
		return func(w io.Writer, s style) {
			printRemoteProfiles(w, rps, func(rp engine.RemoteProfile) string {
				return s.more(eng.ProfileRun(given, append(append([]string{rp.Profile}, profileAddArgs("", rf)[2:]...), "-n", "--json")...))
			})
		}
	}
	show := func(rps []engine.RemoteProfile) { a.page(cmd, cmd.ErrOrStderr(), stderr, printer(rps)) }
	// --trust answers the question; what it accepted is still shown, as a record.
	req.Show = func(rps []engine.RemoteProfile) {
		if !a.json {
			show(rps)
		}
	}
	if p.Interactive() {
		req.Consent = func(rps []engine.RemoteProfile) (bool, error) {
			show(rps)
			var names []string
			for _, rp := range rps {
				names = append(names, rp.Profile)
			}
			return p.Confirm("Add " + strings.Join(names, ", ") + "? Nothing is turned on")
		}
	}
	if p.Interactive() {
		req.Pick = func(names []string, invalid int) ([]string, error) {
			return p.Profiles(ProfileStep{Source: rf.source, Names: names, Invalid: invalid})
		}
	}
	res, err := eng.AddProfile(cmd.Context(), req)
	if errors.Is(err, engine.ErrDeclined) || errors.Is(err, ErrAborted) {
		return &abortedError{"aborted; nothing was added."}
	}
	if err != nil {
		return err
	}
	return a.emit(cmd, func(w io.Writer, s style) {
		if res.DryRun {
			printer(res.Profiles)(w, s)
		}
		printRemoteResult(w, s, res)
	}, res)
}

// profileAddArgs is lola profile's --add command line for the profile name ("" for none) and the
// run's --add flags, every value through untrusted.Text.
func profileAddArgs(name string, rf remoteFlags) []string {
	args := []string{"lola", "profile"}
	if name != "" {
		args = append(args, untrusted.Text(name, untrusted.MaxName))
	}
	args = append(args, "--add", untrusted.Text(rf.source, untrusted.MaxName))
	for _, f := range [][2]string{{"--ref", rf.ref}, {"--path", rf.path}} {
		if f[1] != "" {
			args = append(args, f[0], untrusted.Text(f[1], untrusted.MaxName))
		}
	}
	return args
}

// printRemoteProfiles shows the remote profiles lola profile --add or --update is about to keep
// (Plan 6b §6.5): each with the file lola keeps it in, which every scope shares, the scope whose
// install its content is compared with, its source, ref, --path and pin (the pin it replaces
// first, for an update), its description, then each entry as the trust diff shows a module's
// change: its MCP servers and hooks first, then its files, which moreFor's command for the
// profile, when not "", caps (ruling R-6b3b-8, ADV-5). Every value has been through untrusted.Text.
func printRemoteProfiles(w io.Writer, rps []engine.RemoteProfile, moreFor func(engine.RemoteProfile) string) {
	for _, rp := range rps {
		more := moreFor(rp)
		from := rp.Source
		for _, f := range [][2]string{{"ref", rp.Ref}, {"path", rp.Path}} {
			if f[1] != "" {
				from += " " + f[0] + " " + f[1]
			}
		}
		// The file is one for every scope; only the content is compared with one scope's install.
		fmt.Fprintf(w, "%s → %s (shared by every scope); content compared with %s\n", rp.Profile, rp.File, rp.ComparedWith)
		fmt.Fprintln(w, "  from "+from)
		switch {
		case rp.PreviousCommit != "" && rp.PreviousCommit != rp.Commit:
			fmt.Fprintln(w, "  commit "+rp.PreviousCommit+"\n      → "+rp.Commit)
		case rp.Commit != "":
			fmt.Fprintln(w, "  commit "+rp.Commit)
		}
		if rp.PreviousDigest != "" && rp.PreviousDigest != rp.Digest {
			fmt.Fprintln(w, "  digest "+rp.PreviousDigest+"\n      → "+rp.Digest)
		} else {
			fmt.Fprintln(w, "  digest "+rp.Digest)
		}
		if rp.Description != "" {
			fmt.Fprintln(w, "  "+rp.Description)
		}
		switch {
		case len(rp.Changes) == 0 && rp.PreviousDigest != "":
			fmt.Fprintln(w, "  (no entry changes)")
		case len(rp.Changes) == 0:
			fmt.Fprintln(w, "  (no modules)")
		}
		for _, c := range rp.Changes {
			files, note := c.Files, c.FilesNote
			c.Files, c.FilesNote = nil, ""
			printTrustChange(w, c, more)
			printFiles(w, files, more)
			if note != "" {
				fmt.Fprintln(w, "      ("+note+")")
			}
		}
	}
}

// printRemoteResult is the end of lola profile --add or --update: each profile added or updated,
// or in a dry run what would be, then the notices. --plain rows are the verb (added, updated,
// would-add, would-update), the profile and its file.
func printRemoteResult(w io.Writer, s style, res *engine.RemoteResult) {
	verb, would := "added", "would add"
	if len(res.Profiles) > 0 && res.Profiles[0].PreviousDigest != "" {
		verb, would = "updated", "would update"
	}
	if res.DryRun {
		verb = would
	}
	for _, rp := range res.Profiles {
		switch {
		case s.plain:
			fmt.Fprintf(w, "%s\t%s\t%s\n", strings.ReplaceAll(verb, " ", "-"), rp.Profile, rp.File)
		case res.DryRun:
			fmt.Fprintln(w, s.info(verb+" "+rp.Profile+" ("+rp.File+")"))
		default:
			fmt.Fprintln(w, s.ok(verb+" "+rp.Profile+" ("+rp.File+")"))
		}
	}
	for _, n := range res.Notices {
		fmt.Fprintln(w, noticeLine(s, n))
	}
}
