package cli

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// profileActions are lola profile's action flags, canonical names in the order a refusal names
// them (Plan 6b §3.2).
var profileActions = []engine.ProfileAction{engine.ProfileOn, engine.ProfileOff, engine.ProfileSwitch, engine.ProfileAdd, engine.ProfileUpdate,
	engine.ProfileRemove}

// profileFlags are the flags each action takes beside the scope and output flags (Plan 6b §3.2,
// §19 P13), in the order a refusal checks them: a flag the run's action does not take, or any of
// them without an action, is refused, since a flag that does nothing must not look accepted.
var profileFlags = map[engine.ProfileAction][]string{
	engine.ProfileOn:     {"yes", "trust", "dry-run", "allow-hooks", "allow-runtime-hooks"},
	engine.ProfileOff:    {"yes", "trust", "dry-run", "allow-hooks", "allow-runtime-hooks"},
	engine.ProfileSwitch: {"yes", "trust", "dry-run", "allow-hooks", "allow-runtime-hooks"},
	engine.ProfileAdd:    {"trust", "dry-run", "ref", "path"},
	engine.ProfileUpdate: {"yes", "trust", "dry-run", "allow-hooks", "allow-runtime-hooks", "all"},
	engine.ProfileRemove: {"yes", "dry-run", "force"},
}

// refuseProfileFlags refuses a flag of profileFlags that cmd's action act ("" for none) does not
// take, naming the actions that do; the hint is the run without it (args are its names).
func refuseProfileFlags(cmd *cobra.Command, args []string, act engine.ProfileAction) error {
	var all []string
	for _, a := range profileActions {
		for _, f := range profileFlags[a] {
			if !slices.Contains(all, f) {
				all = append(all, f)
			}
		}
	}
	for _, f := range all {
		if !cmd.Flags().Changed(f) || slices.Contains(profileFlags[act], f) {
			continue
		}
		var takes []string
		for _, a := range profileActions {
			if slices.Contains(profileFlags[a], f) {
				takes = append(takes, "--"+string(a))
			}
		}
		applies := takes[0]
		if len(takes) > 1 {
			applies = strings.Join(takes[:len(takes)-1], ", ") + " and " + takes[len(takes)-1]
		}
		return errs.WithHints(fmt.Errorf("--%s applies to %s only", f, applies), "drop it: "+runWithout(cmd, args, f))
	}
	return nil
}

// runWithout is the command the user ran (lola profile, lola market, lola install), its names
// args and every flag they set but those named in drop, by canonical name, a list flag once per
// value, a bool only as it was given (never false made true), quoted for the shell: what a hint
// offers to run instead.
func runWithout(cmd *cobra.Command, args []string, drop ...string) string {
	out := strings.Fields(cmd.CommandPath())
	for _, a := range args {
		out = append(out, untrusted.Text(a, untrusted.MaxName))
	}
	cmd.Flags().Visit(func(f *pflag.Flag) {
		list, isList := f.Value.(pflag.SliceValue)
		switch {
		case slices.Contains(drop, f.Name):
		case f.Value.Type() == "bool" && f.Value.String() == "true":
			out = append(out, "--"+f.Name)
		case f.Value.Type() == "bool" && f.DefValue == "true":
			out = append(out, "--"+f.Name+"=false") // a bool given false says something only against a true default
		case f.Value.Type() == "bool": // given false, as it is by default: nothing to repeat
		case isList:
			for _, v := range list.GetSlice() {
				out = append(out, "--"+f.Name, untrusted.Text(v, untrusted.MaxName))
			}
		default:
			out = append(out, "--"+f.Name, untrusted.Text(f.Value.String(), untrusted.MaxName))
		}
	})
	return errs.ShellJoin(out)
}

// consentFlags are the flags that answer a question of the run they are given to (-y, --trust,
// the hook flags), which a hint repeating the run for another reason drops: it may ask another
// question, and only you can answer it (ruling R-6b3c-8, R-6cux-4).
var consentFlags = []string{"yes", "trust", "allow-hooks", "allow-runtime-hooks"}

// forceRun is the user's run, with its names args, repeated with --force and none of its
// consentFlags: what a refusal of conflicts --force overrides hints (engine.Options.ForceRun).
func forceRun(cmd *cobra.Command, args []string) string {
	return runWithout(cmd, args, append([]string{"force"}, consentFlags...)...) + " --force"
}

// rerunFlags is the flags of lola profile --add or --update that a hint repeating the run keeps
// beside the scope, the names and the flags the engine is given (Plan 6b §12.3): --trust, -n,
// --json, --plain and --no-pager, as given.
func rerunFlags(cmd *cobra.Command) []string {
	var out []string
	for _, f := range [][2]string{{"trust", "--trust"}, {"dry-run", "-n"}, {"json", "--json"}, {"plain", "--plain"}, {"no-pager", "--no-pager"}} {
		if given(cmd, f[0]) {
			out = append(out, f[1])
		}
	}
	return out
}

// profileAction is the one action flag set on cmd, "" for none (Plan 6b §19 R7): cobra has
// parsed, so the check is on canonical names; two different actions refuse, naming both, with
// the run keeping each one of them as hints (args are its names).
func profileAction(cmd *cobra.Command, args []string) (engine.ProfileAction, error) {
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
	var hints []string
	for _, keep := range set {
		var drop []string
		for _, other := range set {
			if other != keep {
				drop = append(drop, string(other))
			}
		}
		hints = append(hints, "keep --"+string(keep)+": "+runWithout(cmd, args, drop...))
	}
	return "", errs.WithHints(fmt.Errorf("--%s and --%s cannot be combined; pick one", set[0], set[1]), hints...)
}

func (a *app) profileCmd() *cobra.Command {
	var f syncFlags
	var on, off, switchTo bool
	var add remoteFlags
	var update, remove, all bool
	cmd := &cobra.Command{
		Use:   "profile [<name>...] [--on|--off|--switch|--add <source>|--update [--all]|--remove]",
		Short: "List your profiles, show one, or turn them on and off",
		Long: "A profile is a named set of module entries, kept in $XDG_CONFIG_HOME/lola/profiles/<name>.yml;\n" +
			"default.yml there is always on at user scope. A project may commit its own in\n" +
			".lola/profiles/<name>.yml: they are used at its project scope only, and their entries are\n" +
			"reviewed before use, and pinned, as project.yml's are. Without a name, list the profiles each\n" +
			"scope can use and where each stands in the scope's active list. With a name, show the profile\n" +
			"and, per scope, the layer each of its modules comes from: the last layer listing a module\n" +
			"wins, at user scope default, then the active profiles in order, then user.yml; at project\n" +
			"scope the active profiles, then project.yml. For a profile that is off, it shows what turning\n" +
			"it on would give.\n" +
			"Remote profiles, fetched from a source, are kept in $XDG_DATA_HOME/lola/profiles/<name>/ and\n" +
			"used at every scope; a profile of yours, or the project's, with the same name shadows one.\n" +
			"A name is the profile's file name; user:<name> says it is yours, project:<name> the project's,\n" +
			"remote:<name> the remote one. A name yours and the project's both define is refused at\n" +
			"project scope until you qualify it.\n\n" +
			"--on turns the named profiles on, appended to the scope's active list in the order given;\n" +
			"--off turns them off; --switch makes them the active list, in order (default stays on). Each\n" +
			"applies the change at once, as lola sync does for the modules it changes: the modules no\n" +
			"layer wants any more are removed, never one another layer still wants. Any action that\n" +
			"removes files asks first; -y confirms. default cannot be turned off (rename or delete\n" +
			"default.yml) and has no place at project scope. One action per run.\n\n" +
			"--add <source> fetches profiles/<name>.yml from a git repository or an https archive (at\n" +
			"--ref, under --path), shows its entries with the files, MCP servers and hooks each brings,\n" +
			"and asks before keeping it; without a name, a terminal offers the source's profiles. Every\n" +
			"entry of a remote profile must be pinned. Adding turns nothing on, and off a terminal it\n" +
			"needs --trust, checked before anything is fetched. It asks no scope: the profile is shared\n" +
			"by every scope, and a scope flag only names the scope whose installs its content is\n" +
			"compared with, else the project found from here, else user scope.\n" +
			"--update fetches the named remote profiles, or with --all every one you have, again from the\n" +
			"source, ref and path each was added from, shows how each one's entries change and asks about\n" +
			"each on its own: No keeps that one and goes on to the next, a failure is reported and the\n" +
			"run goes on (and then fails), Ctrl+C at a question stops, keeping the ones updated. It\n" +
			"applies them in each known scope that uses them, the project found from here (never your\n" +
			"home directory), then user scope, stopping at the first that fails or is declined (so does\n" +
			"--scope both); --project, --user or -C narrows it to that scope. Without a scope flag,\n" +
			"--trust answers only for the profiles: a project's own trust is asked, or refused off a\n" +
			"terminal. Other checkouts pick them up at their next sync.\n" +
			"--remove deletes a remote profile, asking first (-y); it is refused while the scope, user scope\n" +
			"or the project found from here uses it, unless --force, which also answers the question\n" +
			"(never trust or hook consent).\n\n" +
			"Scope without an action: --project, --user, -C <dir> or --scope (both included); without them,\n" +
			"the project with .lola/ at or above the current directory, if any, and user scope. Listing and\n" +
			"showing never ask, and default_scope does not apply.\n" +
			"With --on, --off, --switch or --remove:\n" + strings.TrimPrefix(scopeHelp, "Scope: ") + "\n" +
			"Every lola profile refuses a project whose .lola/lock.json git tracks.",
		Example: "  lola profile\n  lola profile go\n  lola profile go --on\n  lola profile go docs --switch --user\n" +
			"  lola profile go --off -y\n  lola profile --user --json\n  lola profile go --add https://github.com/example/profiles\n" +
			"  lola profile go --update\n  lola profile --update --all\n  lola profile go --remove -y",
		GroupID:           groupCore,
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: a.completeProfile,
		RunE: func(cmd *cobra.Command, args []string) error {
			act, err := profileAction(cmd, args)
			if err != nil {
				return err
			}
			if err := refuseProfileFlags(cmd, args, act); err != nil {
				return err
			}
			eng, err := a.engine()
			if err != nil {
				return err
			}
			switch act {
			case "":
			case engine.ProfileAdd:
				return a.profileAdd(cmd, eng, args, add, f)
			case engine.ProfileUpdate:
				return a.profileUpdate(cmd, eng, args, all, f)
			case engine.ProfileRemove:
				return a.profileRemove(cmd, eng, args, f)
			default:
				return a.profileApply(cmd, eng, engine.ProfileChange{Action: act, Names: args}, f)
			}
			if len(args) > 1 {
				return errs.WithHints(errors.New("name one profile to show"), "list them all: "+runWithout(cmd, nil),
					"or turn them on: "+runWithout(cmd, args)+" --on")
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
				states, notices, err := eng.ProfileState(cmd.Context(), scopes, args[0], outputFlags(cmd)...)
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
	cmd.Flags().StringVar(&add.source, string(engine.ProfileAdd), "", "fetch a remote profile from this git repository or https archive, show it and ask")
	cmd.Flags().BoolVar(&update, string(engine.ProfileUpdate), false, "fetch the named remote profiles again from their sources, show what changed, ask, and apply them where they are on")
	cmd.Flags().BoolVar(&all, "all", false, "with --update: every remote profile you have")
	cmd.Flags().BoolVar(&remove, string(engine.ProfileRemove), false, "delete a remote profile (asks; -y; refused while it is in use, unless --force)")
	cmd.Flags().StringVar(&add.ref, "ref", "", "with --add: the branch, tag or commit to fetch")
	cmd.Flags().StringVar(&add.path, "path", "", "with --add: the directory of the source that holds profiles/")
	cmd.Flags().BoolVarP(&f.dry, "dry-run", "n", false, "print what the action would change, ask nothing and write nothing")
	cmd.Flags().BoolVarP(&f.yes, "yes", "y", false, "confirm removals, and --remove (only those: it never trusts changes)")
	cmd.Flags().BoolVar(&f.trust, "trust", false, "accept the changes to project.yml and its profiles the run installs, a project profile taking your profile's place, and a remote profile --add or --update shows, without asking (never grants hook consent; --update without a scope flag: the profiles only)")
	cmd.Flags().BoolVar(&f.force, "force", false, "with --remove: remove a remote profile in use, without asking (never answers a consent question)")
	cmd.Flags().BoolVar(&f.allowHooks, "allow-hooks", false, "run the modules' install hooks without asking (consent for this run)")
	cmd.Flags().BoolVar(&f.allowRuntime, "allow-runtime-hooks", false, "install the modules' Claude Code runtime hooks without asking (consent is recorded)")
	// Aliases for muscle memory from other tools (Plan 6b §3.3): systemctl and conda, nvm and
	// kubectl. Additive and stable; the published table is pinned in store_test.go.
	aliasFlags(cmd, map[string][]string{
		string(engine.ProfileOn):     {"enable", "activate"},
		string(engine.ProfileOff):    {"disable", "deactivate"},
		string(engine.ProfileSwitch): {"use"},
		string(engine.ProfileAdd):    {"get"},
		string(engine.ProfileUpdate): {"upgrade"},
		string(engine.ProfileRemove): {"rm", "delete", "del", "erase", "uninstall"},
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
	scs, err := a.scopes(cmd, eng, ch.Names, question, false, true)
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

// printProfileAction prints an action's summary (Plan 6b §12.1): where each named profile the run
// turns on or off, with its file, now stands (one already as asked is its notice alone), each module it loads (+), replaces (~) or unloads (-) with the layer it comes from and
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
			if !act.Turned[i] {
				continue
			}
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

// completeProfile offers the names of the profiles you can turn on, or with --update or --remove
// the remote ones (none with --all, nor a second for --remove, which takes one), minus those
// already given (remote:go counting as go).
func (a *app) completeProfile(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	remove := cmd.Flags().Changed(string(engine.ProfileRemove))
	eng, err := a.engine()
	if err != nil || cmd.Flags().Changed("all") || remove && len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	list, remote := eng.ProfileNames, remove || cmd.Flags().Changed(string(engine.ProfileUpdate))
	if remote {
		list = eng.RemoteProfileNames
	}
	names, err := list()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	// For remote profiles, remote:go names go.
	given := slices.Clone(args)
	if remote {
		for i, g := range given {
			given[i] = strings.TrimPrefix(g, "remote:")
		}
	}
	var out []cobra.Completion
	for _, n := range names {
		if strings.HasPrefix(n, toComplete) && !slices.Contains(given, n) {
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
			path := r.Path
			if r.ShadowedBy != nil {
				path += " (shadowed by " + *r.ShadowedBy + ")"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s  %s\n", r.Scope, r.Name, r.Kind, active, path)
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
		if st := states[0]; st.Source != "" {
			from := "from " + st.Source
			if st.Ref != "" {
				from += " (ref " + st.Ref + ")"
			}
			fmt.Fprintln(w, from)
			if st.Commit != "" {
				fmt.Fprintln(w, "commit "+st.Commit)
			}
			fmt.Fprintln(w, "digest "+st.Digest)
		}
		if len(states[0].Extends) > 0 {
			fmt.Fprintln(w, "extends "+strings.Join(states[0].Extends, ", "))
		}
		for _, st := range states {
			where := st.Scope
			if st.Root != "" {
				where += " (" + st.Root + ")"
			}
			switch {
			case !st.Active && st.ShadowedBy != nil:
				fmt.Fprintf(w, "%s: off, shadowed by %s; its modules:\n", where, *st.ShadowedBy)
			case !st.Active:
				fmt.Fprintf(w, "%s: off; turned on, its modules would come from:\n", where)
			case st.ReadAs != nil:
				fmt.Fprintf(w, "%s: %s, read as %s; its modules:\n", where, activeText(st.ProfileRow), *st.ReadAs)
			case st.ShadowedBy != nil && st.ShadowPosition != nil:
				wins := "its entry"
				if st.Position != nil && *st.ShadowPosition < *st.Position {
					wins = "this one's entry"
				}
				fmt.Fprintf(w, "%s: %s, in effect; %s is on too, %s of %d, and %s wins where both list a module; its modules:\n", where,
					activeText(st.ProfileRow), *st.ShadowedBy, engine.Ordinal(*st.ShadowPosition), st.Of, wins)
			case st.ShadowedBy != nil:
				fmt.Fprintf(w, "%s: %s, in effect; %s would take its place if turned on; its modules:\n", where, activeText(st.ProfileRow),
					*st.ShadowedBy)
			default:
				fmt.Fprintf(w, "%s: %s\n", where, activeText(st.ProfileRow))
			}
			if len(st.Modules) == 0 {
				fmt.Fprintln(w, "  (no modules)")
				continue
			}
			tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
			for _, m := range st.Modules {
				line := "  " + m.Name + "\t" + cmp.Or(m.From, "not wanted here")
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
