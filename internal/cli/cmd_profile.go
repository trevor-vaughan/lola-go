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
// them.
var profileActions = []engine.ProfileAction{engine.ProfileOn, engine.ProfileOff, engine.ProfileSwitch, engine.ProfileEdit, engine.ProfileMove,
	engine.ProfileSplit, engine.ProfileAdd, engine.ProfileUpdate, engine.ProfileRemove}

// profileFlags are the flags each action takes beside the scope and output flags, in the order a
// refusal checks them: a flag the run's action does not take, or any of them without an action, is
// refused, since a flag that does nothing must not look accepted.
var profileFlags = map[engine.ProfileAction][]string{
	engine.ProfileOn:     {"yes", "trust", "dry-run", "force", "verbose"},
	engine.ProfileOff:    {"yes", "trust", "dry-run", "force", "verbose"},
	engine.ProfileSwitch: {"new", "from", "bundle", "yes", "trust", "dry-run", "force", "verbose"},
	engine.ProfileEdit: {"new", "from", "bundle", "skip", "only", "unskip", "exact", "with-mentions", "target", "override", "ref", "path", "yes", "trust",
		"dry-run", "force", "verbose"},
	engine.ProfileMove:   {"to", "keep-off", "yes", "trust", "dry-run", "force", "verbose"},
	engine.ProfileSplit:  {"to", "yes", "trust", "dry-run", "force", "verbose"},
	engine.ProfileAdd:    {"trust", "dry-run", "ref", "path"},
	engine.ProfileUpdate: {"yes", "trust", "dry-run", "all", "verbose"},
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
	return runAs(strings.Fields(cmd.CommandPath()), cmd, args, drop...)
}

// runAs is runWithout as the command words: the run's flags, kept the same way, on another
// command (lola info's next step is an install). On another command it keeps only the flags that
// command takes, and never -y, which answered a question of this one; nor in a dry run, which
// asks nothing.
func runAs(words []string, cmd *cobra.Command, args []string, drop ...string) string {
	out := slices.Clone(words)
	for _, a := range args {
		out = append(out, untrusted.Text(a, untrusted.MaxName))
	}
	var other *cobra.Command
	if strings.Join(words, " ") != cmd.CommandPath() {
		found, rest, err := cmd.Root().Find(words[1:])
		if err != nil || len(rest) > 0 {
			panic("lola hints a command it does not have: " + strings.Join(words, " "))
		}
		other = found
	}
	dry := cmd.Flags().Lookup("dry-run")
	asks := dry == nil || dry.Value.String() != "true" || slices.Contains(drop, "dry-run")
	cmd.Flags().Visit(func(f *pflag.Flag) {
		list, isList := f.Value.(pflag.SliceValue)
		switch {
		case slices.Contains(drop, f.Name):
		case f.Name == "yes" && (other != nil || !asks):
		case other != nil && other.Flags().Lookup(f.Name) == nil && other.InheritedFlags().Lookup(f.Name) == nil:
		case f.Name == "trust":
			// as parsed, its kinds after =, never all; before it is parsed, as given
			if t, ok := f.Value.(*trustFlag); ok && t.kinds != 0 {
				out = append(out, t.kinds.Arg())
			} else if ok {
				for _, a := range t.args() {
					out = append(out, untrusted.Text(a, untrusted.MaxName))
				}
			}
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

// consentFlags are the flags that answer a question of the run they are given to (-y and --trust
// of any kind), which a hint repeating the run for another reason drops: it may ask another
// question, and only you can answer it.
var consentFlags = []string{"yes", "trust"}

// completeRun is the user's run, its names args, without the flags named in drop and --trust, for
// a hint that completes it with what a terminal would have asked (a scope it lacked, an
// assistant): -y stays, since the completed run asks what it answered. A hint that changes a scope
// the user chose, or names another profile, is runWithout with consentFlags.
func completeRun(cmd *cobra.Command, args []string, drop ...string) string {
	return runWithout(cmd, args, slices.Concat(drop, []string{"trust"})...)
}

// forceRun is the user's run, with its names args, repeated with --force and none of its
// consentFlags: what a refusal of conflicts --force overrides hints (engine.Options.ForceRun).
func forceRun(cmd *cobra.Command, args []string) string {
	return runWithout(cmd, args, append([]string{"force"}, consentFlags...)...) + " --force"
}

// renderRun is forceRun as a real run, without -n: what renders a forceable skip, which a dry run
// plans but does not write (engine.Options.RenderRun).
func renderRun(cmd *cobra.Command, args []string) string {
	return runWithout(cmd, args, append([]string{"force", "dry-run"}, consentFlags...)...) + " --force"
}

// rerunFlags is the flags of lola profile --add or --update that a hint repeating the run keeps
// beside the scope, the names and the flags the engine is given: --trust for the
// kinds given (trust), -n, --json, --plain and --no-pager, as given.
func rerunFlags(cmd *cobra.Command, trust engine.TrustKinds) []string {
	var out []string
	if trust != 0 {
		out = append(out, trust.Arg())
	}
	for _, f := range [][2]string{{"dry-run", "-n"}, {"json", "--json"}, {"plain", "--plain"}, {"no-pager", "--no-pager"}} {
		if given(cmd, f[0]) {
			out = append(out, f[1])
		}
	}
	return out
}

// profileAction is the one action flag set on cmd, "" for none: cobra has
// parsed, so the check is on canonical names; two different actions refuse, naming both, with
// the run keeping each one of them as hints (args are its names). Beside --move, --switch is its
// modifier (switch to the profile it moves into), never an action.
func profileAction(cmd *cobra.Command, args []string) (engine.ProfileAction, error) {
	var set []engine.ProfileAction
	for _, act := range profileActions {
		if act == engine.ProfileSwitch && cmd.Flags().Changed(string(engine.ProfileMove)) {
			continue
		}
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
	var trust trustFlag
	var on, off, switchTo bool
	var add remoteFlags
	var update, remove, all, split bool
	var moveModules []string
	var to string
	var keepOff bool
	var create bool
	var from string
	var members []string
	var edit bool
	var ef editFlags
	cmd := &cobra.Command{
		Use: "profile [<name>] [--on|--off|--switch|--add <source>|--update [--all]|--remove] | [<name>] --edit [<module>...] | " +
			"[<from>] --move <module>... --to <name> | <name> --split [--to <name>]",
		Short: "List your profiles, show one, or turn them on and off",
		Long: "A profile is a named set of module entries in $XDG_CONFIG_HOME/lola/profiles/<name>.yml;\n" +
			"default.yml there is always on at user scope. A project may commit its own in\n" +
			".lola/profiles/<name>.yml: they are used at its project scope only, and their entries are\n" +
			"reviewed before use, and pinned, as project.yml's are. Without a name, list the profiles each\n" +
			"scope can use and which one is on. With a name, show the profile and, per scope, the layer\n" +
			"each of its modules comes from: the last layer listing a module wins, at user scope default,\n" +
			"then the active profile; at project scope project.yml, then the active profile.\n" +
			"For a profile that is off, it shows what switching to it would give. Remote\n" +
			"profiles, fetched from a source, are kept in $XDG_DATA_HOME/lola/profiles/<name>/ and used at\n" +
			"every scope; a profile of yours, or the project's, with the same name shadows one. A name is\n" +
			"the profile's file name; user:<name> says it is yours, project:<name> the project's,\n" +
			"remote:<name> the remote one. A name yours and the project's both define is refused at\n" +
			"project scope until you qualify it.\n\n" +
			"One profile is on at a time in a scope. --switch makes the named profile the one on, whatever\n" +
			"is on; --on turns it on when none is (with another on it asks, on a terminal, to switch to\n" +
			"it; off one it refuses: --switch is the way that never asks); --off turns off the profile on\n" +
			"(name it, or give no name). Each applies the change at once, as lola sync does for the\n" +
			"modules it changes: the modules no layer wants any more are removed, never one another layer\n" +
			"still wants. Any action that removes files asks first; -y confirms. --force removes them\n" +
			"without asking, edited ones too. Neither answers --on's question (on a terminal: switch, make\n" +
			"a bundle of both, when a bundle is on add it to that bundle, which stays on, or cancel).\n" +
			"default has no place at project scope. lola profile base --switch (default --switch at user\n" +
			"scope) turns off the profile that is on and leaves the base. One action and one name per run.\n\n" +
			"--edit [<module>...] edits the named profile (else the one edits go to: the profile on, or the\n" +
			"base) as lola install would write it: a module it adds, or its picks with --only, --skip and\n" +
			"--unskip (--only is the entry's only: list), applied where the profile is on and saved where it\n" +
			"is off. It asks no scope: the scope flag's, else the project found here, else user scope; a\n" +
			"profile of yours named with no scope flag is applied at user scope too when it is on there. A\n" +
			"module another layer gives asks, on a terminal, whether to change it there or override it in\n" +
			"the profile (--override; or name base). The first name is the profile when one has it and no\n" +
			"module does, or with --new, which makes it (yours; project:<name> the project's; with no\n" +
			"module it makes it empty, --from <profile> a copy, --bundle a,b a bundle); a name that is\n" +
			"both is asked about on a terminal and refused off one (qualify it: user:<name>). --bundle a,b\n" +
			"sets the named bundle's members, in order, making a bare one that names no profile, and\n" +
			"applies them where the bundle is on, or nested in the bundle on; what that removes asks first.\n" +
			"On a terminal, --edit with no module opens a page: the profile's modules ticked, then each\n" +
			"one's components (a module you untick is removed once asked, then each other scope where the\n" +
			"profile is on asks what it removes there; -y, --force answer both), a bundle's members, or for\n" +
			"a name no profile has, whether it lists modules or bundles profiles.\n\n" +
			"--move <module>... --to <name> moves whole entries, without a pin into a profile of yours,\n" +
			"from the layer each comes from (or <from>, named first: a profile, base, default or\n" +
			"project.yml) into <name>, a profile of yours or the project's, or base, in one transaction,\n" +
			"and applies it. The version installed is kept. A target that lists a module already asks\n" +
			"to replace it (-y, --force). A target that is off in the scope asks whether to switch to it\n" +
			"(--switch: the move switches too) or keep it off (--keep-off: the modules' files are removed\n" +
			"here, asked; -y, --force); neither -y nor --force answers that. A new name creates your\n" +
			"profile. A remote profile is read-only: nothing moves out of or into one.\n\n" +
			"--add <source> fetches profiles/<name>.yml from a git repository or an https archive (at\n" +
			"--ref, under --path), shows its entries with the files, MCP servers and hooks each brings,\n" +
			"and asks before keeping it; without a name, a terminal offers the source's profiles. Every\n" +
			"entry of a remote profile must be pinned. Adding turns nothing on, and off a terminal it\n" +
			"needs --trust (profiles), checked before anything is fetched. It asks no scope: the profile is shared\n" +
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
			"terminal. Other checkouts pick them up at their next sync.\n" + trustHelp("profile") + "\n" +
			"--remove deletes a profile of yours (user:<name>, or a bare name only yours has), moving its file\n" +
			"to lola's trash, or a remote profile (remote:<name>, or a bare name only a remote one has),\n" +
			"asking first (-y); a bare name both have is refused. It is refused while the scope, user scope\n" +
			"or the project found from here uses it (on, in the bundle on, or in a bundle the one on\n" +
			"nests), unless --force, which also answers the question (never trust or hook consent). A\n" +
			"project's profile is committed: git removes it, and the base is never removed.\n\n" +
			"Scope without an action: --project, --user, -C <dir> or --scope (both included); without them,\n" +
			"the project with .lola/ at or above the current directory, if any, and user scope. Listing and\n" +
			"showing never ask, and default_scope does not apply.\n" +
			"With --on, --off, --switch or --remove:\n" + strings.TrimPrefix(scopeHelp, "Scope: ") + "\n" +
			"Every lola profile refuses a project whose .lola/lock.json git tracks.",
		Example: "  lola profile\n  lola profile go\n  lola profile go --on\n  lola profile go --switch --user\n" +
			"  lola profile go --off -y\n  lola profile --user --json\n  lola profile go --add https://github.com/example/profiles\n" +
			"  lola profile go --update\n  lola profile --update --all\n  lola profile go --remove -y\n  lola profile --move my-notes --to me\n" +
			"  lola profile go-review --edit cc-skills-golang --only skill/golang-testing",
		GroupID:           groupCore,
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: a.completeProfile,
		RunE: func(cmd *cobra.Command, args []string) error {
			act, err := profileAction(cmd, args)
			if err != nil {
				return err
			}
			// --trust first, so a hint for another flag repeats it as parsed; an action that takes
			// none refuses it below.
			if slices.Contains(profileFlags[act], "trust") {
				command := "profile"
				if act == engine.ProfileAdd {
					command = "profile --add"
				}
				if f.trust, err = trust.parse(cmd, args, command, "profile"); err != nil {
					return err
				}
			}
			// Without a scope flag an update answers only the profiles' question with --trust, never a
			// project's: entries written there would answer nothing.
			if act == engine.ProfileUpdate && trust.names(engine.TrustEntries) && a.scopeFlag == "" && !a.user && !a.project && a.root == "" {
				return errs.WithHints(errors.New("--trust=entries trusts a project's changes, which lola profile --update answers only in a scope a flag names"),
					"name the project: "+runWithout(cmd, args)+" --project", "or with user scope too: "+runWithout(cmd, args)+" --scope both")
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
			case engine.ProfileMove:
				return a.profileMove(cmd, eng, args, moveModules, to, keepOff, switchTo, f)
			case engine.ProfileSplit:
				return a.profileSplit(cmd, eng, args, to, f)
			case engine.ProfileEdit:
				ef.create, ef.from, ef.members = create, from, members
				return a.profileEdit(cmd, args, ef, f)
			default:
				if len(args) > 1 {
					return a.oneName(cmd, eng, args, act, f)
				}
				ch := engine.ProfileChange{Action: act, New: create, From: from, Bundle: members}
				if len(args) == 1 {
					ch.Name = args[0]
				}
				if err := a.newFlags(cmd, eng, args, ch, f); err != nil {
					return err
				}
				return a.profileApply(cmd, eng, ch, f)
			}
			if len(args) > 1 {
				hints := []string{"list them all: " + runWithout(cmd, nil)}
				for _, n := range args {
					hints = append(hints, "or show one: "+runWithout(cmd, []string{n}))
				}
				return errs.WithHints(errors.New("name one profile to show"), hints...)
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
			a.printContext(cmd, eng, choiceScopes(scs), nil)
			if len(args) == 1 {
				var scopes []engine.Scope
				for _, c := range scs {
					scopes = append(scopes, c.Scope)
				}
				states, notices, err := eng.ProfileState(cmd.Context(), scopes, args[0], outputFlags(cmd)...)
				if engine.IsUnknownProfile(err) {
					// --switch --new is another action than the show, so it takes none of the run's
					// consent.
					if exists, refused := eng.NewOrSwitch(scopes[0], engine.ProfileChange{Name: args[0]}, nil); !exists && refused == nil {
						err = errs.WithHints(err, "make it: "+runAs(strings.Fields(cmd.CommandPath()), cmd,
							[]string{args[0], "--switch", "--new"}, consentFlags...))
					}
				}
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
					if !slices.ContainsFunc(notices, n.Same) {
						notices = append(notices, n)
					}
				}
			}
			return a.emit(cmd, func(w io.Writer, s style) { printProfiles(w, s, eng, rows, notices) },
				map[string]any{"profiles": rows, "notices": notices})
		},
	}
	cmd.Flags().BoolVar(&on, string(engine.ProfileOn), false, "turn the named profile on, when none is (with another on it asks to switch on a terminal, and refuses off one: use --switch), and apply")
	cmd.Flags().BoolVar(&off, string(engine.ProfileOff), false, "turn the profile on off (name it, or none) and apply (asks when files go; -y)")
	cmd.Flags().BoolVar(&switchTo, string(engine.ProfileSwitch), false, "make the named profile the one on, whatever is on, and apply; with --move: switch to the profile the entries move to")
	cmd.Flags().StringSliceVar(&moveModules, string(engine.ProfileMove), nil, "move these modules' entries (repeatable, or comma-separated) from the layer each comes from, or the profile named, to --to, keeping the version installed")
	cmd.Flags().StringVar(&to, "to", "", "with --move: the profile the entries move to (base: the scope's base; a new name creates your profile); with --split: the new profile's name")
	cmd.Flags().BoolVar(&split, string(engine.ProfileSplit), false, "rewrite a profile with both bundle: and modules: into a bundle of its members and a new <name>-modules profile of its modules (asks; -y)")
	cmd.Flags().BoolVar(&keepOff, "keep-off", false, "with --move into a profile that is off here: keep it off, removing the modules' files here")
	cmd.Flags().BoolVar(&edit, string(engine.ProfileEdit), false, "edit the named profile (else the one edits go to) or its modules' picks, as lola install would, and apply it where it is on")
	cmd.Flags().StringSliceVar(&ef.skips, "skip", nil, "with --edit: leave these components out, in the profile's entry (kind/name; repeatable or comma-separated)")
	cmd.Flags().StringSliceVar(&ef.onlys, "only", nil, "with --edit: take only these components, skipping the rest in the profile's entry, so a later version's new ones install (kind/name; repeatable or comma-separated)")
	cmd.Flags().StringSliceVar(&ef.unskips, "unskip", nil, "with --edit: take these components back (kind/name; repeatable or comma-separated)")
	cmd.Flags().BoolVar(&ef.exact, "exact", false, "with --edit --only: save the selectors as the entry's only: list instead, so components a later version adds are not installed")
	cmd.Flags().BoolVar(&ef.withMentions, "with-mentions", false, "with --edit: also take the components your picks mention by name (one hop)")
	cmd.Flags().BoolVar(&ef.override, "override", false, "with --edit: when the module comes from another layer, give the profile its own entry, starting from that one")
	a.addTargetFlag(cmd, &ef.targets, "with --edit: the assistants a module it adds installs for; <id>@<version> also records which version to write for")
	cmd.Flags().BoolVar(&create, "new", false, "with --switch or --edit: make the profile first (yours; project:<name> the project's), empty unless --from or --bundle")
	cmd.Flags().StringVar(&from, "from", "", "with --new: copy this profile's entries and targets (never a pin)")
	cmd.Flags().StringSliceVar(&members, "bundle", nil, "with --new: make a bundle of these profiles (comma-separated, in order); with --edit: set a bundle's members; a bare one that does not exist is made empty")
	cmd.Flags().StringVar(&add.source, string(engine.ProfileAdd), "", "fetch a remote profile from this git repository or https archive, show it and ask")
	cmd.Flags().BoolVar(&update, string(engine.ProfileUpdate), false, "fetch the named remote profiles again from their sources, show what changed, ask, and apply them where they are on")
	cmd.Flags().BoolVar(&all, "all", false, "with --update: every remote profile you have")
	cmd.Flags().BoolVar(&remove, string(engine.ProfileRemove), false, "delete a profile of yours (to the trash) or a remote one (asks; -y; refused while it is in use, unless --force)")
	cmd.Flags().StringVar(&add.ref, "ref", "", "with --add or --edit: the branch, tag or commit to fetch")
	cmd.Flags().StringVar(&add.path, "path", "", "with --add: the directory of the source that holds profiles/; with --edit: the module root inside the source")
	cmd.Flags().BoolVarP(&f.dry, "dry-run", "n", false, "print what the action would change, ask nothing and write nothing")
	cmd.Flags().BoolVarP(&f.yes, "yes", "y", false, "confirm removals, and --remove (only those: it never trusts changes)")
	cmd.Flags().BoolP("verbose", "v", false, "with an action that applies: print every notice, not a count per module of those that are not about safety")
	for _, name := range []string{"skip", "only", "unskip"} {
		_ = cmd.RegisterFlagCompletionFunc(name, a.completeComponents(1)) // lola profile <profile> --edit <module>...
	}
	_ = cmd.RegisterFlagCompletionFunc(string(engine.ProfileMove), a.completeMove)
	_ = cmd.RegisterFlagCompletionFunc("to", a.completeProfiles(true))
	_ = cmd.RegisterFlagCompletionFunc("from", a.completeProfiles(false))
	_ = cmd.RegisterFlagCompletionFunc("bundle", a.completeProfiles(false))
	_ = cmd.RegisterFlagCompletionFunc(string(engine.ProfileAdd), completeFiles)
	_ = cmd.RegisterFlagCompletionFunc("ref", cobra.NoFileCompletions)
	_ = cmd.RegisterFlagCompletionFunc("path", completeDirs)
	addTrustFlag(cmd, &trust, "profile", "trust these kinds without asking: entries, profiles, scripts, hooks, runtime-hooks or all; bare: entries,profiles; --add takes profiles only (recorded as a yes is; --update without a scope flag: profiles only)")
	cmd.Flags().BoolVar(&f.force, "force", false, "with --on, --off, --switch or --edit: remove files without asking, edited ones too; with --remove: remove a profile in use, without asking (never answers a consent question, nor --on's question)")
	// Aliases for muscle memory from other tools: systemctl and conda, nvm and
	// kubectl. Additive and stable; the published table is pinned in store_test.go.
	aliasFlags(cmd, map[string][]string{
		string(engine.ProfileOn):     {"enable", "activate"},
		string(engine.ProfileOff):    {"disable", "deactivate"},
		string(engine.ProfileSwitch): {"use"},
		string(engine.ProfileEdit):   {"pick", "modify"},
		string(engine.ProfileMove):   {"mv"},
		"new":                        {"create"},
		string(engine.ProfileAdd):    {"get"},
		string(engine.ProfileUpdate): {"upgrade"},
		string(engine.ProfileRemove): {"rm", "delete", "del", "erase", "uninstall"},
	})
	return cmd
}

// oneName refuses two or more names for act, one profile being on at a time: --switch and --on hint
// the bundle of them, made and switched to, after which --switch names it, keeping the run's
// --trust, -n and output flags (never -y or --force, which would answer removals the switch makes);
// --off hints the run with each name. The bundle's name and members are the engine's, read at the
// scope the run would take without asking (a found project, else user scope).
func (a *app) oneName(cmd *cobra.Command, eng *engine.Engine, args []string, act engine.ProfileAction, f syncFlags) error {
	if act == engine.ProfileOff {
		var hints []string
		for _, n := range args {
			if h := runWithout(cmd, []string{n}); !slices.Contains(hints, h) {
				hints = append(hints, h)
			}
		}
		return errs.WithHints(fmt.Errorf("one profile is on at a time; --%s takes one name", act), hints...)
	}
	sc, err := a.readScope(eng)
	if err != nil {
		return err
	}
	run, err := eng.BundleOffer(sc, args, rerunFlags(cmd, f.trust))
	if err != nil {
		return err
	}
	return errs.WithHints(fmt.Errorf("one profile is on at a time; to combine %s make a bundle", untrusted.Text(strings.Join(args, " and "), untrusted.MaxName)), run)
}

// readScope is the scope a refusal of lola profile reads for its hints: the one the run would take
// without asking (a found project, else user scope).
func (a *app) readScope(eng *engine.Engine) (engine.Scope, error) {
	c, err := a.readChoice(eng)
	return c.Scope, err
}

// readChoice is readScope's scope with where it came from (a flag, or found from here).
func (a *app) readChoice(eng *engine.Engine) (engine.ScopeChoice, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return engine.ScopeChoice{}, err
	}
	req, err := a.scopeRequest(cwd, false)
	if err != nil {
		return engine.ScopeChoice{}, err
	}
	scs, err := eng.ResolveReadScope(req)
	if err != nil {
		return engine.ScopeChoice{}, err
	}
	return scs[0], nil
}

// newFlags refuses --from and --bundle without --new, which they modify, and the two together:
// a new profile copies one, or bundles several. Each hint is a run that
// works: without one of the two; as the engine decides at the run's scope (NewOrSwitch), without
// the flag when a switch finds the profile, with --new added when --new would make it, else the
// engine's refusal with its hints.
func (a *app) newFlags(cmd *cobra.Command, eng *engine.Engine, args []string, ch engine.ProfileChange, f syncFlags) error {
	switch {
	case ch.From != "" && ch.Bundle != nil:
		return errs.WithHints(errors.New("--from copies a profile and --bundle combines several; pick one"),
			"keep --from: "+runWithout(cmd, args, "bundle"), "keep --bundle: "+runWithout(cmd, args, "from"))
	case ch.New || ch.From == "" && ch.Bundle == nil:
		return nil
	}
	flag := "from"
	if ch.Bundle != nil {
		flag = "bundle"
	}
	sc, err := a.readScope(eng)
	if err != nil {
		return err
	}
	exists, err := eng.NewOrSwitch(sc, ch, rerunFlags(cmd, f.trust))
	switch {
	case err != nil:
		return fmt.Errorf("--%s makes a new profile and goes with --new; %w", flag, err)
	case exists:
		return errs.WithHints(fmt.Errorf("--%s makes a new profile and goes with --new", flag), "switch to the one that exists: "+runWithout(cmd, args, flag))
	}
	return errs.WithHints(fmt.Errorf("--%s makes a new profile and goes with --new", flag), "make it: "+runWithout(cmd, args)+" --new")
}

// profileApply runs one action of lola profile: one scope, resolved as every
// writing command's is, then a sync carrying the change, with sync's questions and answers
// (-y, --trust). Its summary, the changes and the plan, is paged as sync's diff is.
func (a *app) profileApply(cmd *cobra.Command, eng *engine.Engine, ch engine.ProfileChange, f syncFlags) error {
	if ch.Name == "" && ch.Action != engine.ProfileOff {
		return errs.WithHints(fmt.Errorf("--%s needs the name of a profile", ch.Action), "list them: lola profile")
	}
	var names []string
	if ch.Name != "" {
		names = []string{ch.Name}
	}
	question := map[engine.ProfileAction]string{engine.ProfileOn: "Turn it on where?", engine.ProfileOff: "Turn it off where?",
		engine.ProfileSwitch: "Switch where?"}[ch.Action]
	scs, err := a.scopes(cmd, eng, names, question, false, true)
	if err != nil {
		return err
	}
	sc := scs[0].Scope
	a.printContext(cmd, eng, []engine.Scope{sc}, nil)
	alone := f
	alone.alone = true // the change the user named, at its one scope; the terminal's answer below is another
	res, more, err := a.syncScope(cmd, eng, sc, nil, &ch, alone)
	// --on never replaces the profile on unasked: a terminal is asked to
	// switch, which -y and --force never answer; off one, or under -n, which asks nothing, the
	// engine's refusal hints --switch.
	if other, ok := errors.AsType[*engine.OtherProfileOnError](err); ok && !f.dry && a.prompter(cmd).Interactive() {
		on, named := untrusted.Text(other.On, untrusted.MaxName), untrusted.Text(other.Name, untrusted.MaxName)
		bundle := untrusted.Text(other.Bundle, untrusted.MaxName)
		options := []string{"Switch to " + named, "Make bundle " + bundle + " of " + untrusted.Text(strings.Join(other.Members, " and "), untrusted.MaxName)}
		if other.Into != "" {
			options = append(options, "Add "+named+" to bundle "+other.Into)
		}
		i, aerr := a.prompter(cmd).Where(WhereStep{Title: on + " is on.", Options: options})
		if aerr != nil {
			return aerr // Cancel is ErrAborted
		}
		if i == 2 { // the bundle on stays on, with one more member: the edit its hint runs
			again := rerunFlags(cmd, 0)
			edit := func(flags []string) string {
				words := slices.Concat([]string{other.Into, "--edit", "--bundle", strings.Join(other.Joined, ",")}, flags, []string{"--force"})
				return eng.Command(sc, "profile", words...)
			}
			real := slices.DeleteFunc(slices.Clone(again), func(f string) bool { return f == "-n" })
			return a.editBundle(cmd, eng, scs[0], other.Into, other.Joined, engine.TargetRequest{Edit: true, Again: again}, f,
				edit(again), edit(real), true)
		}
		ch = engine.ProfileChange{Action: engine.ProfileSwitch, Name: other.Name}
		if i == 1 {
			ch = engine.ProfileChange{Action: engine.ProfileSwitch, Name: other.Bundle, New: true, Bundle: other.Members}
		}
		res, more, err = a.syncScope(cmd, eng, sc, nil, &ch, f)
	}
	if err != nil {
		return err
	}
	// After a change of the profile on, its new context is the run's last line.
	now := ""
	if !res.DryRun && !res.InSync {
		now = contextLine(eng.Context(sc, nil))
	}
	return a.emit(cmd, func(w io.Writer, s style) { printProfileAction(w, s, manifestName(sc), res, s.more(more), now) }, res)
}

// printProfileAction prints an action's summary: where the named profile the run
// turns on or off, with its file, now stands (one already as asked is its notice alone), each
// module it loads (+), replaces (~) or unloads (-) with the layer it comes from and
// the layers it overrides, then the sync's own lines, then, after a change of the profile on,
// now: and its new context; a --new's profiles made come first, a ✓ line each (under -n, "would
// make …"). --plain adds a row per profile made (created, or under -n would-create; profile, path)
// and per change: change, action, module, from and the
// overridden layers, comma-separated, and prints no now: line.
func printProfileAction(w io.Writer, s style, name string, r *engine.SyncResult, more, now string) {
	act := r.Activation
	printCreated(w, s, r.Created, r.DryRun)
	if s.plain {
		for _, c := range act.Changes {
			fmt.Fprintf(w, "change\t%s\t%s\t%s\t%s\n", c.Action, c.Module, c.From, strings.Join(c.Over, ","))
		}
		printSync(w, s, name, r, nil, more)
		return
	}
	if act.Turned {
		fmt.Fprintln(w, turnedLine(act))
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
		printNotices(w, s, r.Result)
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
	} else {
		printSync(w, s, name, r, nil, more)
	}
	if now != "" {
		fmt.Fprintln(w, "now: "+now)
	}
}

// printCreated prints the profiles a run made, as --new says it: under --plain a row each
// (created, or under -n would-create; profile, path), else a ✓ line each (under -n, "would make …").
func printCreated(w io.Writer, s style, created []engine.Created, dry bool) {
	for _, c := range created {
		switch {
		case s.plain && dry:
			fmt.Fprintf(w, "would-create\t%s\t%s\n", c.Profile, c.Path)
		case s.plain:
			fmt.Fprintf(w, "created\t%s\t%s\n", c.Profile, c.Path)
		case dry:
			fmt.Fprintln(w, s.info("would make "+strings.TrimPrefix(createdLine(c), "new ")))
		default:
			fmt.Fprintln(w, s.ok(createdLine(c)))
		}
	}
}

// createdLine is the write line of a profile --new made:
// "new bundle work (go-review, docs)", "new profile try, a copy of go-review (3 modules)", or
// "new profile go-review (<path>, empty)". Its strings have been through untrusted.Text.
func createdLine(c engine.Created) string {
	_, name, _ := strings.Cut(c.Profile, ":")
	switch {
	case c.Bundle != nil:
		return "new bundle " + name + " (" + strings.Join(c.Bundle, ", ") + ")"
	case c.From != "":
		modules := fmt.Sprintf("%d modules", c.Modules)
		if c.Modules == 1 {
			modules = "1 module"
		}
		return "new profile " + name + ", a copy of " + c.FromLabel + " (" + modules + ")"
	}
	return "new profile " + name + " (" + c.Path + ", empty)"
}

// turnedLine is the line of an activation that turned a profile on or off: "Profile user:go
// (<file>) → on".
func turnedLine(act *engine.Activation) string {
	state := "off"
	if act.Profile != nil && *act.Profile == act.Named {
		state = "on"
	}
	q := untrusted.Text(act.Named, len(act.Named))
	if act.File != "" {
		q += " (" + act.File + ")"
	}
	return "Profile " + q + " → " + state
}

// completeProfile offers the names of the profiles you can turn on, or with --remove those it
// takes, or with --update the remote ones; nothing after the first name, but for --update, which
// takes several (minus those given, remote:go counting as go), and nothing with --all.
func (a *app) completeProfile(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	update := cmd.Flags().Changed(string(engine.ProfileUpdate))
	eng, err := a.engine()
	// lola profile [<name>] --edit <module>...: the profile first, then modules, as install takes them.
	if edit := cmd.Flags().Changed(string(engine.ProfileEdit)); err == nil && edit {
		out, d := a.completeSource(cmd, args, toComplete)
		if len(args) == 0 {
			if names, err := eng.ProfileNames(); err == nil {
				for _, n := range names {
					if strings.HasPrefix(n, toComplete) {
						out = append(out, n)
					}
				}
			}
		}
		return out, d
	}
	if err != nil || cmd.Flags().Changed("all") || len(args) > 0 && !update {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	list, remote := eng.ProfileNames, update
	switch {
	case remote:
		list = eng.RemoteProfileNames
	case cmd.Flags().Changed(string(engine.ProfileRemove)):
		list = eng.RemoveNames
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

// stateText is where a row stands in its scope: base, on, in bundle <the
// bundle on>, or off; "(bundle)" marks a bundle that is on or off.
func stateText(r engine.ProfileRow) string {
	switch {
	case r.State == engine.StateMember:
		return "in bundle " + r.In
	case r.Bundle:
		return r.State + " (bundle)"
	}
	return r.State
}

// printProfiles prints lola profile's listing: a table whose rows each name their scope, then
// the notices. --plain rows are scope, name, kind, state (base, on, member or off), bundle (or
// empty for a profile), its members, comma-separated, and path, then the notices as notice rows.
func printProfiles(w io.Writer, s style, eng *engine.Engine, rows []engine.ProfileRow, notices []engine.Notice) {
	if s.plain {
		for _, r := range rows {
			bundle := ""
			if r.Bundle {
				bundle = "bundle"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.Scope, r.Name, r.Kind, r.State, bundle, strings.Join(r.Members, ","), r.Path)
		}
	} else if len(rows) == 0 {
		fmt.Fprintln(w, "no profiles yet (write one: "+untrusted.Text(eng.Paths.UserProfiles, untrusted.MaxName)+"/<name>.yml)")
	} else {
		// tabwriter counts color's escape bytes as width, so STATE is no cell of its own: it is
		// padded to its widest text, then painted, and shares the last cell with FILE.
		width := len("STATE")
		for _, r := range rows {
			width = max(width, utf8.RuneCountInString(stateText(r)))
		}
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintf(tw, "SCOPE\tNAME\tKIND\t%-*s  FILE\n", width, "STATE")
		for _, r := range rows {
			state := fmt.Sprintf("%-*s", width, stateText(r))
			if r.Active {
				state = s.paint("32", state)
			}
			path := r.Path
			if r.ShadowedBy != nil {
				path += " (shadowed by " + *r.ShadowedBy + ")"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s  %s\n", r.Scope, r.Name, r.Kind, state, path)
		}
		_ = tw.Flush()
	}
	for _, n := range notices {
		fmt.Fprintln(w, noticeLine(s, n))
	}
}

// printProfileState prints one profile's state: its name and file, then per scope where it
// stands and, for each of its modules, the layer the scope takes it from and the layers that
// entry replaced, and its picks (only:, skip:), then the notices. --plain rows are scope,
// module, from, the replaced layers, the only: and the skip: patterns, each list
// comma-separated, then the notices as notice rows.
func printProfileState(w io.Writer, s style, states []engine.ProfileState, notices []engine.Notice) {
	if s.plain {
		for _, st := range states {
			for _, m := range st.Modules {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", st.Scope, m.Name, m.From, strings.Join(m.Overrides, ","), strings.Join(m.Only, ","),
					strings.Join(m.Skip, ","))
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
		if len(states[0].Members) > 0 {
			fmt.Fprintln(w, "bundle of "+strings.Join(states[0].Members, ", "))
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
				fmt.Fprintf(w, "%s: off; switched to, its modules would come from:\n", where)
			case st.ReadAs != nil:
				fmt.Fprintf(w, "%s: %s, read as %s; its modules:\n", where, stateText(st.ProfileRow), *st.ReadAs)
			case st.ShadowedBy != nil:
				fmt.Fprintf(w, "%s: %s, in effect; %s would take its place if turned on; its modules:\n", where, stateText(st.ProfileRow),
					*st.ShadowedBy)
			default:
				fmt.Fprintf(w, "%s: %s\n", where, stateText(st.ProfileRow))
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
				if len(m.Only) > 0 {
					fmt.Fprintln(tw, "\tonly: "+strings.Join(m.Only, ", ")) // under FROM, keeping the column
				}
				switch {
				case m.Summary != nil:
					fmt.Fprintln(tw, "\t"+picksText(m.Summary))
				case len(m.Skip) > 0:
					fmt.Fprintln(tw, "\tskip: "+strings.Join(m.Skip, ", "))
				}
			}
			_ = tw.Flush()
		}
	}
	for _, n := range notices {
		fmt.Fprintln(w, noticeLine(s, n))
	}
}
