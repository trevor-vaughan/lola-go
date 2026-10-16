package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// profileActions are lola profile's action flags, canonical names in the order a refusal names
// them (Plan 6b §3.2).
var profileActions = []engine.ProfileAction{engine.ProfileOn, engine.ProfileOff, engine.ProfileSwitch}

// profileAction is the one action flag set on cmd, "" for none (Plan 6b §19 R7): cobra has
// parsed, so the check is on canonical names; two different actions refuse, naming both.
func profileAction(cmd *cobra.Command) (engine.ProfileAction, error) {
	var set []engine.ProfileAction
	for _, act := range profileActions {
		if cmd.Flags().Changed(string(act)) {
			set = append(set, act)
		}
	}
	switch len(set) {
	case 0:
		return "", nil
	case 1:
		return set[0], nil
	}
	return "", errs.WithHints(fmt.Errorf("--%s and --%s cannot be combined; pick one", set[0], set[1]), "lola profile <name>… --on|--off|--switch")
}

func (a *app) profileCmd() *cobra.Command {
	var f syncFlags
	var on, off, switchTo bool
	cmd := &cobra.Command{
		Use:   "profile [<name>...] [--on|--off|--switch]",
		Short: "List your profiles, show one, or turn them on and off",
		Long: "A profile is a named set of module entries, kept in $XDG_CONFIG_HOME/lola/profiles/<name>.yml;\n" +
			"default.yml there is always on at user scope. Without a name, list the profiles each scope can\n" +
			"use and where each stands in the scope's active list. With a name, show the profile and, per\n" +
			"scope, the layer each of its modules comes from: the last layer listing a module wins, at user\n" +
			"scope default, then the active profiles in order, then user.yml; at project scope the active\n" +
			"profiles, then project.yml. For a profile that is off, it shows what turning it on would give.\n" +
			"A name is the profile's file name; user:<name> says it is yours.\n\n" +
			"--on turns the named profiles on, appended to the scope's active list in the order given;\n" +
			"--off turns them off; --switch makes them the active list, in order (default stays on). Each\n" +
			"applies the change at once, as lola sync does for the modules it changes: the modules no\n" +
			"layer wants any more are removed, never one another layer still wants. Any action that\n" +
			"removes files asks first; -y confirms. default cannot be turned off (rename or delete\n" +
			"default.yml) and has no place at project scope. One action per run.\n\n" +
			"Scope without an action: --project, --user, -C <dir> or --scope (both included); without them,\n" +
			"the project with .lola/ at or above the current directory, if any, and user scope. Listing and\n" +
			"showing never ask, and default_scope does not apply.\n" +
			"With an action: " + strings.TrimPrefix(scopeHelp, "Scope: ") + "\n" +
			"Every lola profile refuses a project whose .lola/lock.json git tracks.",
		Example: "  lola profile\n  lola profile go\n  lola profile go --on\n  lola profile go docs --switch --user\n" +
			"  lola profile go --off -y\n  lola profile --user --json",
		GroupID:           groupCore,
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: a.completeProfile,
		RunE: func(cmd *cobra.Command, args []string) error {
			act, err := profileAction(cmd)
			if err != nil {
				return err
			}
			eng, err := a.engine()
			if err != nil {
				return err
			}
			if act != "" {
				return a.profileApply(cmd, eng, engine.ProfileChange{Action: act, Names: args}, f)
			}
			for _, flag := range []string{"yes", "trust", "dry-run", "allow-hooks", "allow-runtime-hooks"} {
				if cmd.Flags().Changed(flag) {
					return errs.WithHints(fmt.Errorf("--%s applies to --on, --off and --switch only", flag), "lola profile <name>… --on|--off|--switch --"+flag)
				}
			}
			if len(args) > 1 {
				return errs.WithHints(errors.New("name one profile to show"), "list them all: lola profile",
					"or act on them: lola profile <name>… --on|--off|--switch")
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			req, err := a.scopeRequest(cwd, false)
			if err != nil {
				return err
			}
			scs, err := eng.ResolveReadScope(req)
			if err != nil {
				return err
			}
			if len(args) == 1 {
				var scopes []engine.Scope
				for _, c := range scs {
					scopes = append(scopes, c.Scope)
				}
				states, notices, err := eng.ProfileState(cmd.Context(), scopes, args[0])
				if err != nil {
					return err
				}
				return a.emit(cmd, func(w io.Writer, s style) { printProfileState(w, s, states, notices) },
					map[string]any{"profiles": states, "notices": notices})
			}
			rows, notices := []engine.ProfileRow{}, []engine.Notice{}
			for i, c := range scs {
				got, notes, err := eng.Profiles(cmd.Context(), c.Scope)
				if err != nil && len(scs) == 2 && c.Source == engine.SourceDiscovered {
					// No flag chose these scopes, so one that cannot be read must not hide the other.
					return errs.WithHints(err, "list the other scope's: "+eng.Command(scs[1-i].Scope, "profile"))
				}
				if err != nil {
					return err
				}
				rows = append(rows, got...)
				// Both scopes read one profiles directory: its warnings are said once.
				for _, n := range notes {
					if !slices.Contains(notices, n) {
						notices = append(notices, n)
					}
				}
			}
			return a.emit(cmd, func(w io.Writer, s style) { printProfiles(w, s, eng, rows, notices) },
				map[string]any{"profiles": rows, "notices": notices})
		},
	}
	cmd.Flags().BoolVar(&on, string(engine.ProfileOn), false, "turn the named profiles on, appended to the active list in order, and apply")
	cmd.Flags().BoolVar(&off, string(engine.ProfileOff), false, "turn the named profiles off and apply (asks when files go; -y)")
	cmd.Flags().BoolVar(&switchTo, string(engine.ProfileSwitch), false, "make the named profiles the active list, in order, and apply")
	cmd.Flags().BoolVarP(&f.dry, "dry-run", "n", false, "print what the action would change, ask nothing and write nothing")
	cmd.Flags().BoolVarP(&f.yes, "yes", "y", false, "confirm removals (only removals: it never trusts changes)")
	cmd.Flags().BoolVar(&f.trust, "trust", false, "accept the changes to project.yml the run installs without asking (never grants hook consent)")
	cmd.Flags().BoolVar(&f.allowHooks, "allow-hooks", false, "run the modules' install hooks without asking (consent for this run)")
	cmd.Flags().BoolVar(&f.allowRuntime, "allow-runtime-hooks", false, "install the modules' Claude Code runtime hooks without asking (consent is recorded)")
	// Aliases for muscle memory from other tools (Plan 6b §3.3): systemctl and conda, nvm and
	// kubectl. Additive and stable; the published table is pinned in store_test.go.
	aliasFlags(cmd, map[string][]string{
		string(engine.ProfileOn):     {"enable", "activate"},
		string(engine.ProfileOff):    {"disable", "deactivate"},
		string(engine.ProfileSwitch): {"use"},
	})
	return cmd
}

// profileApply runs one action of lola profile (Plan 6b §6.3): one scope, resolved as every
// writing command's is, then a sync carrying the change, with sync's questions and answers
// (-y, --trust). Its summary, the changes and the plan, is paged as sync's diff is (§9).
func (a *app) profileApply(cmd *cobra.Command, eng *engine.Engine, ch engine.ProfileChange, f syncFlags) error {
	if len(ch.Names) == 0 {
		return errs.WithHints(fmt.Errorf("--%s needs the names of the profiles", ch.Action), "list them: lola profile")
	}
	question := map[engine.ProfileAction]string{engine.ProfileOn: "Turn them on where?", engine.ProfileOff: "Turn them off where?",
		engine.ProfileSwitch: "Switch where?"}[ch.Action]
	scs, err := a.scopes(cmd, eng, question, false, true)
	if err != nil {
		return err
	}
	pg, err := a.pager(cmd, eng)
	if err != nil {
		return err
	}
	sc := scs[0].Scope
	res, more, err := a.syncScope(cmd, eng, pg, sc, nil, &ch, f)
	if err != nil {
		return err
	}
	return a.emit(cmd, func(w io.Writer, s style) {
		pg.show(w, s, more, func(w io.Writer, s style, more string) {
			printProfileAction(w, s, ch.Action, manifestName(sc), res, more)
		})
	}, res)
}

// printProfileAction prints an action's summary (Plan 6b §12.1): where each named profile, with
// its file, now stands, each module it loads (+), replaces (~) or unloads (-) with the layer it comes from and
// the layers it overrides, then the sync's own lines. --plain adds a row per change: change,
// action, module, from and the overridden layers, comma-separated.
func printProfileAction(w io.Writer, s style, action engine.ProfileAction, name string, r *engine.SyncResult, more string) {
	act := r.Activation
	if s.plain {
		for _, c := range act.Changes {
			fmt.Fprintf(w, "change\t%s\t%s\t%s\t%s\n", c.Action, c.Module, c.From, strings.Join(c.Over, ","))
		}
		printSync(w, s, name, r, nil, more)
		return
	}
	switch action {
	case engine.ProfileSwitch:
		list := "none"
		if len(act.Profiles) > 0 {
			list = strings.Join(act.Profiles, ", ")
		}
		fmt.Fprintln(w, "Active profiles → "+list)
	default:
		for i, q := range act.Named {
			state := "off"
			if k := slices.Index(act.Profiles, q); k >= 0 {
				state = fmt.Sprintf("on, %s of %d", engine.Ordinal(k+1), len(act.Profiles))
			}
			if act.Files[i] != "" {
				q += " (" + act.Files[i] + ")"
			}
			fmt.Fprintf(w, "Profile %s → %s\n", q, state)
		}
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	glyph := map[string]string{engine.ChangeLoad: "+", engine.ChangeReplace: "~", engine.ChangeUnload: "-"}
	for _, c := range act.Changes {
		version := c.Version
		if c.Action == engine.ChangeReplace && c.Was != c.Version {
			version = c.Was + " → " + c.Version
		}
		from := c.From
		if len(c.Over) > 0 {
			from += " overrides " + strings.Join(c.Over, ", ")
		}
		fmt.Fprintf(tw, "  %s %s\t%s\t%s\n", glyph[c.Action], c.Module, version, from)
	}
	_ = tw.Flush()
	if len(act.Changes) == 0 {
		printFindings(w, s, r.Findings)
		for _, n := range r.Notices {
			fmt.Fprintln(w, noticeLine(s, n))
		}
		if r.DryRun {
			if r.ToApply != "" {
				fmt.Fprintln(w, s.info("to apply: "+r.ToApply))
			}
			if r.OffTerminal != "" {
				fmt.Fprintln(w, s.info("off a terminal: "+r.OffTerminal))
			}
			fmt.Fprintln(w, s.info("no files to change"))
		} else {
			fmt.Fprintln(w, s.ok("no files to change"))
		}
		return
	}
	printSync(w, s, name, r, nil, more)
}

// completeProfile offers the names of the profiles you can turn on, minus those already given.
func (a *app) completeProfile(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	eng, err := a.engine()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	names, err := eng.ProfileNames()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []cobra.Completion
	for _, n := range names {
		if strings.HasPrefix(n, toComplete) && !slices.Contains(args, n) {
			out = append(out, n)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// activeText is where a row stands in its scope's active list: "2nd of 3", "always on" or "off".
func activeText(r engine.ProfileRow) string {
	switch {
	case r.Position != nil:
		return engine.Ordinal(*r.Position) + " of " + fmt.Sprint(r.Of)
	case r.Active:
		return "always on"
	}
	return "off"
}

// printProfiles prints lola profile's listing: a table whose rows each name their scope, then
// the notices. --plain rows are scope, name, kind, on or off, the position in the active list
// (empty when off, and for default, which is always on) and path, then the notices as notice rows.
func printProfiles(w io.Writer, s style, eng *engine.Engine, rows []engine.ProfileRow, notices []engine.Notice) {
	if s.plain {
		for _, r := range rows {
			on, pos := "off", ""
			if r.Active {
				on = "on"
			}
			if r.Position != nil {
				pos = fmt.Sprint(*r.Position)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", r.Scope, r.Name, r.Kind, on, pos, r.Path)
		}
	} else if len(rows) == 0 {
		fmt.Fprintln(w, "no profiles yet (write one: "+untrusted.Text(eng.Paths.UserProfiles, untrusted.MaxName)+"/<name>.yml)")
	} else {
		// tabwriter counts color's escape bytes as width, so ACTIVE is no cell of its own: it is
		// padded to its widest text, then painted, and shares the last cell with PATH.
		width := len("ACTIVE")
		for _, r := range rows {
			width = max(width, utf8.RuneCountInString(activeText(r)))
		}
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintf(tw, "SCOPE\tPROFILE\tKIND\t%-*s  PATH\n", width, "ACTIVE")
		for _, r := range rows {
			active := fmt.Sprintf("%-*s", width, activeText(r))
			if r.Active {
				active = s.paint("32", active)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s  %s\n", r.Scope, r.Name, r.Kind, active, r.Path)
		}
		_ = tw.Flush()
	}
	for _, n := range notices {
		fmt.Fprintln(w, noticeLine(s, n))
	}
}

// printProfileState prints one profile's state: its name and file, then per scope where it
// stands and, for each of its modules, the layer the scope takes it from and the layers that
// entry replaced, then the notices. --plain rows are scope, module, from and the replaced
// layers, comma-separated, then the notices as notice rows.
func printProfileState(w io.Writer, s style, states []engine.ProfileState, notices []engine.Notice) {
	if s.plain {
		for _, st := range states {
			for _, m := range st.Modules {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", st.Scope, m.Name, m.From, strings.Join(m.Overrides, ","))
			}
		}
	} else {
		fmt.Fprintf(w, "Profile %s (%s)\n", states[0].Qualified, states[0].Path)
		for _, st := range states {
			where := st.Scope
			if st.Root != "" {
				where += " (" + st.Root + ")"
			}
			switch {
			case !st.Active:
				fmt.Fprintf(w, "%s: off; turned on, its modules would come from:\n", where)
			default:
				fmt.Fprintf(w, "%s: %s\n", where, activeText(st.ProfileRow))
			}
			if len(st.Modules) == 0 {
				fmt.Fprintln(w, "  (no modules)")
				continue
			}
			tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
			for _, m := range st.Modules {
				line := "  " + m.Name + "\t" + m.From
				if len(m.Overrides) > 0 {
					line += "\t(over " + strings.Join(m.Overrides, ", ") + ")"
				}
				fmt.Fprintln(tw, line)
			}
			_ = tw.Flush()
		}
	}
	for _, n := range notices {
		fmt.Fprintln(w, noticeLine(s, n))
	}
}
