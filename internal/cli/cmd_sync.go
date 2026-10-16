package cli

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/trust"
)

// errSyncAborted is a sync the user declined or cancelled at a question: exit 0. The cache may
// already hold content of entries the user trusted, so "nothing changed" would be untrue.
var errSyncAborted = fmt.Errorf("%w; nothing installed", ErrAborted)

// filesShown caps the file paths the human trust diff lists per module; JSON lists all.
const filesShown = 40

func (a *app) syncCmd() *cobra.Command {
	var dry, yes, trustIt, force, update, allowHooks, allowRuntime bool
	cmd := &cobra.Command{
		Use:   "sync [--update [<module>...]]",
		Short: "Make this project (or --user) match its manifest",
		Long: "Install every module project.yml lists at the version it pins, render the modules already\n" +
			"installed, and remove the ones project.yml no longer lists (asking first; -y confirms).\n" +
			"An entry without a pin is installed from your cache's version for its ref, and lola writes\n" +
			"the pin into project.yml. --update fetches the refs again and re-pins (every module, or the\n" +
			"ones named).\n" +
			"When project.yml changed since lola last wrote it or you last trusted it (after a git pull,\n" +
			"say), sync shows each change with the files, MCP servers and hooks it brings, and asks before\n" +
			"anything is cached or installed. Off a terminal, --trust accepts the changes; -y does not.\n" +
			"Install hooks, runtime hooks and agent MCP servers still need their own consent: neither -y\n" +
			"nor --trust gives it.",
		Example:           "  lola sync\n  lola sync --dry-run\n  lola sync --update\n  lola sync --update review-kit\n  lola sync --user -y",
		GroupID:           groupCore,
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: a.completeEntries,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 && !update {
				return errs.WithHints(errors.New("lola sync takes module names only with --update"),
					errs.ShellJoin(append([]string{"lola", "sync", "--update"}, args...)))
			}
			eng, sc, err := a.session()
			if err != nil {
				return err
			}
			opts := engine.Options{DryRun: dry, Force: force}
			a.hookOptions(cmd, &opts, allowHooks, allowRuntime)
			req := engine.SyncRequest{Scope: sc, Update: args, UpdateAll: update && len(args) == 0, Options: opts}
			name := manifestName(sc)
			p := a.prompter(cmd) // never asks under --json
			stderr := style{color: a.color(cmd.ErrOrStderr())}
			switch {
			case trustIt:
				// --trust answers the question; the diff it accepted is still shown, as a record.
				req.Trust = func(rep engine.TrustReport) (bool, error) {
					if !a.json {
						printTrustDiff(cmd.ErrOrStderr(), stderr, name, rep)
					}
					return true, nil
				}
			case p.Interactive():
				req.Trust = func(rep engine.TrustReport) (bool, error) {
					printTrustDiff(cmd.ErrOrStderr(), stderr, name, rep)
					return p.Confirm("Trust these changes and continue?")
				}
			}
			switch {
			case yes:
				req.ConfirmRemovals = func([]engine.Removal) (bool, error) { return true, nil }
			case p.Interactive():
				req.ConfirmRemovals = func(rs []engine.Removal) (bool, error) {
					printRemovals(cmd.ErrOrStderr(), stderr, rs)
					return p.Confirm("Remove these?")
				}
			}
			res, err := eng.Sync(cmd.Context(), req)
			if refusal, ok := errors.AsType[*engine.TrustRefusalError](err); ok && !a.json {
				printTrustDiff(cmd.ErrOrStderr(), stderr, name, refusal.Report)
			}
			if errors.Is(err, engine.ErrDeclined) || errors.Is(err, ErrAborted) {
				return errSyncAborted
			}
			if err != nil {
				return err
			}
			return a.emit(cmd, func(w io.Writer, s style) { printSync(w, s, name, res) }, res)
		},
	}
	cmd.Flags().BoolVarP(&dry, "dry-run", "n", false, "print the plan and the trust diff, ask nothing and write nothing")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "confirm removals (only removals: it never trusts changes)")
	cmd.Flags().BoolVar(&trustIt, "trust", false, "accept the changes to project.yml without asking (never bypasses a refusal or grants hook consent)")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite unowned or hand-edited files and render skipped artifacts that can be (never skips a prompt)")
	cmd.Flags().BoolVar(&update, "update", false, "fetch the refs again and re-pin: every module, or the ones named")
	cmd.Flags().BoolVar(&allowHooks, "allow-hooks", false, "run the modules' install hooks without asking (consent for this run)")
	cmd.Flags().BoolVar(&allowRuntime, "allow-runtime-hooks", false, "install the modules' Claude Code runtime hooks without asking (consent is recorded)")
	return cmd
}

// completeEntries offers the names of the entries of the current scope's manifest.
func (a *app) completeEntries(_ *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	eng, sc, err := a.session()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	man, err := eng.Manifest(sc)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []cobra.Completion
	for _, ent := range man.Modules {
		if strings.HasPrefix(ent.Name, toComplete) {
			out = append(out, ent.Name)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// manifestName is how output names the scope's manifest.
func manifestName(sc engine.Scope) string {
	if sc.Kind == target.User {
		return "user.yml"
	}
	return filepath.Join(".lola", "project.yml")
}

// printSync renders a sync result: in a dry run the trust diff first, then the plan as render
// shows it, the pins and the removals.
func printSync(w io.Writer, s style, name string, r *engine.SyncResult) {
	if r.DryRun && r.Trust.Status != trust.StatusTrusted && r.Trust.Status != engine.TrustStatusNotApplicable {
		printTrustDiff(w, s, name, r.Trust)
	}
	if r.InSync && !r.DryRun && !s.plain {
		printFindings(w, s, r.Findings)
		for _, n := range r.Notices {
			fmt.Fprintln(w, noticeLine(s, n))
		}
		fmt.Fprintln(w, s.ok("already in sync"))
		return
	}
	printPlan(w, s, r.Result, nil)
	printPins(w, s, r.Pins)
	printRemovals(w, s, r.Removals)
	printFindings(w, s, r.Findings)
	for _, n := range r.Notices {
		fmt.Fprintln(w, noticeLine(s, n))
	}
	if r.DryRun || s.plain {
		if r.InSync && !s.plain {
			fmt.Fprintln(w, s.info("already in sync"))
		}
		return
	}
	printDone(w, s, "synced", r.Result, nil)
	for _, rm := range r.Removals {
		if rm.WholeModule {
			fmt.Fprintln(w, s.ok("removed "+rm.Module))
		}
	}
}

// printPins lists the pins a sync writes; --plain rows are pin, module, digest, reason.
func printPins(w io.Writer, s style, pins []engine.Pin) {
	if len(pins) == 0 {
		return
	}
	if s.plain {
		for _, p := range pins {
			fmt.Fprintf(w, "pin\t%s\t%s\t%s\n", p.Module, p.Digest, p.Reason)
		}
		return
	}
	fmt.Fprintln(w, "pins:")
	for _, p := range pins {
		commit := ""
		if p.Commit != "" {
			commit = " (commit " + p.Commit + ")"
		}
		line := p.Module + " pinned " + p.Digest + commit + ", was unpinned"
		if p.Reason == engine.PinUpdate {
			line = p.Module + " re-pinned " + p.PreviousDigest + " → " + p.Digest + commit + " (--update)"
		}
		fmt.Fprintln(w, "  "+s.info(line))
	}
}

// printRemovals lists what a sync removes, per module and target, with the paths lola wrote
// there; --plain rows are remove, module, target, path, one per path (a target lola wrote no
// path for has one row with none).
func printRemovals(w io.Writer, s style, rs []engine.Removal) {
	if len(rs) == 0 {
		return
	}
	if s.plain {
		for _, r := range rs {
			for _, t := range r.Targets {
				rows := 0
				for _, p := range r.Paths {
					if p.Target == t {
						fmt.Fprintf(w, "remove\t%s\t%s\t%s\n", r.Module, t, p.Path)
						rows++
					}
				}
				if rows == 0 {
					fmt.Fprintf(w, "remove\t%s\t%s\t\n", r.Module, t)
				}
			}
		}
		return
	}
	fmt.Fprintln(w, "removals:")
	for _, r := range rs {
		why := "(no longer in its targets)"
		if r.WholeModule {
			why = "(whole module)"
		}
		fmt.Fprintln(w, "  "+s.bad(r.Module+": "+strings.Join(r.Targets, ", ")+" "+why))
		if len(r.Paths) > 0 {
			paths := make([]string, len(r.Paths))
			for i, p := range r.Paths {
				paths[i] = p.Path
			}
			fmt.Fprintln(w, "      "+strings.Join(paths, ", "))
		}
	}
}

// printTrustDiff shows how the manifest differs from what the user last trusted (Plan 6a §7.5):
// a source that changed host first, marked !, then each added (+), changed (~) and removed (-)
// module with its fields, and the content an added or changed module brings. Every value has
// been through untrusted.Text. --plain rows are trust, module, change. A report with no change
// prints nothing: a refusal made before the pins are known (--update) has none to show.
func printTrustDiff(w io.Writer, s style, name string, rep engine.TrustReport) {
	if len(rep.Changes) == 0 && rep.Targets == nil {
		return
	}
	if s.plain {
		for _, c := range rep.Changes {
			fmt.Fprintf(w, "trust\t%s\t%s\n", c.Module, c.Kind)
		}
		if rep.Targets != nil {
			fmt.Fprintf(w, "trust\t\ttargets\n")
		}
		return
	}
	switch {
	case rep.Status == trust.StatusNew:
		fmt.Fprintf(w, "%s has not been trusted on this machine (a fresh clone, or a moved project):\n", name)
	case onlyContentChanged(rep):
		fmt.Fprintf(w, "%s is unchanged, but module content changed since lola installed it or you last trusted it:\n", name)
	default:
		fmt.Fprintf(w, "%s changed since lola last wrote it or you last trusted it:\n", name)
	}
	var first, rest []engine.TrustChange
	for _, c := range rep.Changes {
		if c.HostChanged {
			first = append(first, c)
		} else {
			rest = append(rest, c)
		}
	}
	for _, c := range append(first, rest...) {
		printTrustChange(w, c)
	}
	if rep.Targets != nil {
		fmt.Fprintf(w, "  ~ %-13s %s → %s\n", "targets", shownValue(rep.Targets.From), shownValue(rep.Targets.To))
	}
}

// onlyContentChanged reports whether the report holds nothing but folder modules whose content
// differs from the version installed: project.yml itself is as the user trusted it.
func onlyContentChanged(rep engine.TrustReport) bool {
	if rep.Targets != nil || len(rep.Changes) == 0 {
		return false
	}
	for _, c := range rep.Changes {
		if !c.FolderContent {
			return false
		}
	}
	return true
}

// printTrustChange is one module of the trust diff.
func printTrustChange(w io.Writer, c engine.TrustChange) {
	const pad = "                  " // under the value column
	glyph := map[string]string{trust.ChangeAdded: "+", trust.ChangeChanged: "~", trust.ChangeRemoved: "-"}[c.Kind]
	if c.HostChanged {
		glyph = "!"
	}
	var lines []string
	if c.HostChanged {
		for _, f := range c.Fields {
			if f.Field == "source" {
				lines = append(lines, "source host changed: "+shownValue(f.From)+" → "+shownValue(f.To))
			}
		}
	}
	switch c.Kind {
	case trust.ChangeAdded:
		var src []string
		for _, f := range c.Fields {
			switch f.Field {
			case "source":
				src = append(src, shownValue(f.To))
			case "ref", "path":
				src = append(src, f.Field+" "+shownValue(f.To))
			default:
				lines = append(lines, f.Field+" "+shownValue(f.To))
			}
		}
		lines = append([]string{strings.Join(src, "  ")}, lines...)
	case trust.ChangeChanged:
		for _, f := range c.Fields {
			if c.HostChanged && f.Field == "source" {
				continue
			}
			lines = append(lines, f.Field+" "+shownValue(f.From)+" → "+shownValue(f.To))
		}
	}
	if c.Note != "" && len(lines) > 0 {
		lines[0] += " (" + c.Note + ")"
	}
	head := fmt.Sprintf("  %s %-13s", glyph, c.Module)
	if len(lines) == 0 {
		fmt.Fprintln(w, strings.TrimRight(head, " "))
	}
	for i, l := range lines {
		if i == 0 {
			fmt.Fprintln(w, head+" "+l)
		} else {
			fmt.Fprintln(w, pad+l)
		}
	}
	if len(c.Files) > 0 {
		var fs []string
		for i, f := range c.Files {
			if i == filesShown {
				fs = append(fs, fmt.Sprintf("… and %d more (see lola sync -n --json)", len(c.Files)-filesShown))
				break
			}
			fs = append(fs, map[string]string{trust.ChangeAdded: "+", trust.ChangeChanged: "~", trust.ChangeRemoved: "-"}[f.Change]+" "+f.Path)
		}
		fmt.Fprintln(w, "      files: "+strings.Join(fs, "  "))
	}
	if c.FilesNote != "" {
		fmt.Fprintln(w, "      ("+c.FilesNote+")")
	}
	for _, m := range c.MCPServers {
		fmt.Fprintf(w, "      MCP server %s (%s) runs: %s\n", m.Name, m.Transport, m.Runs)
		for _, env := range m.Env {
			fmt.Fprintln(w, "        env: "+env)
		}
		for _, header := range m.Headers {
			fmt.Fprintln(w, "        header: "+header)
		}
		if m.Cwd != "" {
			fmt.Fprintln(w, "        cwd: "+m.Cwd)
		}
	}
	for _, h := range c.Hooks {
		switch h.Kind {
		case engine.HookKindInstall:
			fmt.Fprintf(w, "      install hook %s: %s\n", h.Event, h.Detail)
		case engine.HookKindMCPServer:
			fmt.Fprintf(w, "      agent MCP server for %s: %s\n", h.Target, h.Detail)
		default:
			fmt.Fprintf(w, "      runtime hook for %s on %s: %s\n", h.Target, h.Event, h.Detail)
		}
	}
}

// shownValue prints a trust field value: none for an absent one, a list in brackets.
func shownValue(v any) string {
	switch v := v.(type) {
	case nil:
		return "(none)"
	case string:
		return v
	case []string:
		return "[" + strings.Join(v, ", ") + "]"
	}
	return fmt.Sprint(v)
}
