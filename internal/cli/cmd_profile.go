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
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// The usage rules of lola profile's verbs that need a profile: refusing one without it hints the
// usage and where to find one, never a run that lacks it.
var (
	profileShowGuide = argGuide{noun: "profile", find: "lola profile list", usage: "lola profile show <name>",
		examples: []string{"lola profile show go", "lola profile show go --user --json"}}
	profileOnGuide = argGuide{noun: "profile", find: "lola profile list", usage: "lola profile on <name> [-y] [-n]",
		examples: []string{"lola profile on go", "lola profile on go --user -y"}}
	profileSwitchGuide = argGuide{noun: "profile", find: "lola profile list", usage: "lola profile switch <name> [--new [--from <profile> | --bundle <a,b>]]",
		examples: []string{"lola profile switch go --user", "lola profile switch go-docs --new --bundle go,docs"}}
	profileSplitGuide = argGuide{noun: "profile", find: "lola profile list", usage: "lola profile split <name> [--to <new>]",
		examples: []string{"lola profile split work", "lola profile split work --to work-tools"}}
	profileRmGuide = argGuide{noun: "profile", find: "lola profile list", usage: "lola profile rm <name> [-y] [--force]",
		examples: []string{"lola profile rm go -y", "lola profile rm remote:go"}}
	profileMoveGuide = argGuide{noun: "module", find: "lola profile show <profile>", usage: "lola profile move <module>... --to <profile> [--from <layer>]",
		examples: []string{"lola profile move my-notes --to me", "lola profile move my-notes --to me --from go"}}
)

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
// of any kind, with --unseen, which widens it), which a hint repeating the run for another reason
// drops: it may ask another question, and only you can answer it.
var consentFlags = []string{"yes", "trust", "unseen"}

// completeRun is the user's run, its names args, without the flags named in drop and --trust (and
// --unseen), for a hint that completes it with what a terminal would have asked (a scope it lacked,
// an assistant): -y stays, since the completed run asks what it answered. A hint that changes a scope
// the user chose, or names another profile, is runWithout with consentFlags.
func completeRun(cmd *cobra.Command, args []string, drop ...string) string {
	return runWithout(cmd, args, slices.Concat(drop, []string{"trust", "unseen"})...)
}

// forceRun is the user's run, with its names args, repeated with --force and none of its
// consentFlags: what a refusal of conflicts --force overrides hints (engine.Options.ForceRun).
func forceRun(cmd *cobra.Command, args []string) string {
	return runWithout(cmd, args, append([]string{"force"}, consentFlags...)...) + " --force"
}

// keepRun is the user's run with --keep-edits: what an uninstall's refusal of a hand-edited file
// hints beside forceRun (engine.Options.KeepRun). Unlike --force it does not answer the uninstall's
// question, so it keeps -y only when the user gave it; the hinted run's own refusal adds it.
func keepRun(cmd *cobra.Command, args []string) string {
	return completeRun(cmd, args, "keep-edits", "force") + " --keep-edits"
}

// renderRun is forceRun as a real run, without -n: what renders a forceable skip, which a dry run
// plans but does not write (engine.Options.RenderRun).
func renderRun(cmd *cobra.Command, args []string) string {
	return runWithout(cmd, args, append([]string{"force", "dry-run"}, consentFlags...)...) + " --force"
}

// rerunFlags is the flags of lola profile add or update that a hint repeating the run keeps
// beside the scope, the names and the flags the engine is given: --trust for the
// kinds given (trust), --unseen beside a consent kind, -n, --json, --plain and --no-pager, as given.
func rerunFlags(cmd *cobra.Command, trust engine.TrustKinds) []string {
	var out []string
	if trust != 0 {
		out = append(out, trust.Arg())
	}
	if trust&engine.TrustConsent != 0 && given(cmd, "unseen") {
		out = append(out, "--unseen")
	}
	for _, f := range [][2]string{{"dry-run", "-n"}, {"json", "--json"}, {"plain", "--plain"}, {"no-pager", "--no-pager"}} {
		if given(cmd, f[0]) {
			out = append(out, f[1])
		}
	}
	return out
}

// syncFlagHelp is the help of the flags addSyncFlags registers that a verb words its own way: -y,
// and --force ("" for a verb without it).
type syncFlagHelp struct{ yes, force string }

// addSyncFlags registers the flags of a lola profile verb that applies a change to a scope: -n, -y,
// -v, --trust (with --unseen) with the kinds lola profile <verb> asks, and --force when help words
// it.
func addSyncFlags(cmd *cobra.Command, f *syncFlags, trust *trustFlag, help syncFlagHelp) {
	cmd.Flags().BoolVarP(&f.dry, "dry-run", "n", false, "print what it would change, ask nothing and write nothing")
	cmd.Flags().BoolVarP(&f.yes, "yes", "y", false, help.yes)
	cmd.Flags().BoolP("verbose", "v", false, "print every fold and notice in full")
	addTrustFlag(cmd, trust, "profile "+cmd.Name(), "trust without asking: entries, profiles, scripts, hooks, runtime-hooks, all")
	if help.force != "" {
		cmd.Flags().BoolVar(&f.force, "force", false, help.force)
	}
}

// profileTrust parses the --trust of the lola profile verb cmd, whose operands are args, into f.
func profileTrust(cmd *cobra.Command, args []string, trust *trustFlag, f *syncFlags) error {
	k, err := trust.parse(cmd, args, commandWords(cmd), "profile")
	if err != nil {
		return err
	}
	f.trust, f.unseen = k, trust.unseen
	return nil
}

// refuseProfileName refuses name for a profile a run of cmd would make when it is a word lola
// profile's commands answer to (engine.RefuseReserved); the hint is rerun(another name), user: or
// project: kept.
func refuseProfileName(eng *engine.Engine, name string, rerun func(name string) string) error {
	kind, bare, qualified := strings.Cut(name, ":")
	if !qualified {
		kind, bare = "", name
	} else {
		kind += ":"
	}
	return eng.RefuseReserved("profile", bare, func(n string) string { return rerun(kind + n) })
}

const (
	profileYesHelp   = "open no picker; confirm removals (it never trusts changes)"
	profileForceHelp = "remove files without asking, edited too"
)

func (a *app) profileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile [<name>]",
		Short: "List your profiles, show one, or turn them on and off",
		Long: "A profile is a named set of module entries: yours in $XDG_CONFIG_HOME/lola/profiles/<name>.yml,\n" +
			"a project's in .lola/profiles/, or a remote one fetched with add. One is on at a time in a\n" +
			"scope, over its base (default.yml at user scope, project.yml in a project).\n\n" +
			"Without a subcommand it lists the profiles each scope can use and which is on. lola profile <name>\n" +
			"is lola profile show <name>: the profile and, per scope, the layer each of its modules comes from.\n\n" +
			"on, off and switch apply at once; on asks to switch when another is on (off a terminal it\n" +
			"refuses), switch never asks. Removing files asks first (-y confirms, --force skips it).\n" +
			"edit changes a profile's modules and picks; move moves entries from a profile, base or\n" +
			"project.yml into another; add, update and rm manage remote profiles (rm also yours).\n\n" +
			"Write user:<name>, project:<name> or remote:<name> when names clash. Listing and showing read the\n" +
			"project here and user scope. on, off, switch, rm, move and split take --project,\n" +
			"--user, -C or --scope, else ask on a terminal, else use that project, else default_scope, else refuse.",
		Example: "  lola profile\n  lola profile go\n  lola profile on go\n  lola profile switch go --user\n" +
			"  lola profile off -y\n  lola profile list --user --json\n" +
			"  lola profile add https://github.com/example/profiles go\n" +
			"  lola profile update go\n  lola profile update --all\n  lola profile rm go -y\n" +
			"  lola profile switch go-docs --new --bundle go,docs\n  lola profile edit work --bundle go,docs\n" +
			"  lola profile edit go-review cc-skills-golang --only skill/golang-testing\n" +
			"  lola profile move my-notes --to me\n  lola profile move my-notes --to me --from go\n" +
			"  lola profile split work",
		GroupID:                    groupCore,
		Args:                       cobra.ArbitraryArgs,
		SuggestionsMinimumDistance: 2,
		ValidArgsFunction:          a.completeProfileArgs((*engine.Engine).ProfileNames, false),
	}
	ns := nounSpec{noun: "profile", bare: a.listProfiles, show: a.showProfile, exists: a.profileExists,
		retired: map[string]string{"uninstall": "rm"}}
	cmd.RunE = a.nounRunE(ns)
	cmd.AddCommand(a.profileListCmd(), a.profileShowCmd(), a.profileOnCmd(), a.profileOffCmd(), a.profileSwitchCmd(), a.profileEditCmd(),
		a.profileMoveCmd(), a.profileSplitCmd(), a.profileAddCmd(), a.profileUpdateCmd(), a.profileRmCmd())
	a.noteVerbWins(cmd, ns)
	a.coachFlags(cmd, ns, map[string]oldFlag{
		"on": {verb: "on"}, "enable": {verb: "on"}, "activate": {verb: "on"},
		"off": {verb: "off"}, "disable": {verb: "off"}, "deactivate": {verb: "off"},
		"switch": {verb: "switch"}, "use": {verb: "switch"},
		"edit": {verb: "edit"}, "pick": {verb: "edit"}, "modify": {verb: "edit"},
		"move": {verb: "move", takesValue: true, list: true, lead: "from"}, "mv": {verb: "move", takesValue: true, list: true, lead: "from"},
		"split": {verb: "split"},
		"add":   {verb: "add", takesValue: true}, "get": {verb: "add", takesValue: true},
		"update": {verb: "update"}, "upgrade": {verb: "update"},
		"remove": {verb: "rm"}, "rm": {verb: "rm"}, "delete": {verb: "rm"}, "del": {verb: "rm"}, "erase": {verb: "rm"}, "uninstall": {verb: "rm"},
	})
	return cmd
}

func (a *app) profileListCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "list",
		Aliases:           []string{"ls"},
		Short:             "List the profiles each scope can use and which is on",
		Long:              "List the profiles each scope can use, and which is on, as bare lola profile does.",
		Example:           "  lola profile list\n  lola profile list --user --json",
		Args:              cobra.NoArgs,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE:              func(cmd *cobra.Command, _ []string) error { return a.listProfiles(cmd) },
	}
}

func (a *app) profileShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "show <name>",
		Short:             "Show a profile and the layer each of its modules comes from",
		Long:              "Show the profile and, per scope, the layer each of its modules comes from. lola profile <name> is the same.",
		Example:           profileShowGuide.example(),
		Args:              profileShowGuide.args(1),
		ValidArgsFunction: a.completeProfileArgs((*engine.Engine).ProfileNames, false),
		RunE:              a.showProfile,
	}
}

func (a *app) profileOnCmd() *cobra.Command {
	var f syncFlags
	var trust trustFlag
	cmd := &cobra.Command{
		Use:     "on <name>",
		Aliases: []string{"enable", "activate"},
		Short:   "Turn a profile on and apply",
		Long: "Turn <name> on over the base and apply. When another profile is on, ask on a terminal to switch to\n" +
			"it, make a bundle of the two, or add it to the bundle on; off a terminal refuse, hinting switch.\n" +
			"Removing files asks first (-y confirms, --force skips it).",
		Example:           profileOnGuide.example(),
		Args:              profileOnGuide.args(1),
		ValidArgsFunction: a.completeProfileArgs((*engine.Engine).ProfileNames, false),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.profileChange(cmd, args, engine.ProfileChange{Action: engine.ProfileOn}, f, &trust)
		},
	}
	addSyncFlags(cmd, &f, &trust, syncFlagHelp{yes: profileYesHelp, force: profileForceHelp})
	return cmd
}

func (a *app) profileOffCmd() *cobra.Command {
	var f syncFlags
	var trust trustFlag
	cmd := &cobra.Command{
		Use:     "off [<name>]",
		Aliases: []string{"disable", "deactivate"},
		Short:   "Turn off the profile on and apply",
		Long: "Turn off the profile on (name it, or none) and apply: the base alone is loaded. Removing files asks\n" +
			"first (-y confirms, --force skips it).",
		Example: "  lola profile off\n  lola profile off go --user -y",
		Args: func(cmd *cobra.Command, args []string) error {
			if err := profileTrust(cmd, args, &trust, &f); err != nil { // first, so a hint repeats it as parsed
				return err
			}
			if len(args) > 1 {
				return oneEach(cmd, "one profile is on at a time; off takes one name", nil, args)
			}
			return nil
		},
		ValidArgsFunction: a.completeProfileArgs((*engine.Engine).ProfileNames, false),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.profileChange(cmd, args, engine.ProfileChange{Action: engine.ProfileOff}, f, &trust)
		},
	}
	addSyncFlags(cmd, &f, &trust, syncFlagHelp{yes: profileYesHelp, force: profileForceHelp})
	return cmd
}

func (a *app) profileSwitchCmd() *cobra.Command {
	var f syncFlags
	var trust trustFlag
	var ch engine.ProfileChange
	cmd := &cobra.Command{
		Use:     "switch <name>",
		Aliases: []string{"use"},
		Short:   "Make a profile the one on, whatever is on, and apply",
		Long: "Make <name> the profile on, whatever is on, and apply; it never asks to. --new makes it first: empty,\n" +
			"a copy of --from's entries and targets, or a bundle of --bundle's profiles. Removing files asks first\n" +
			"(-y confirms, --force skips it).",
		Example:           profileSwitchGuide.example(),
		Args:              profileSwitchGuide.args(1),
		ValidArgsFunction: a.completeProfileArgs((*engine.Engine).ProfileNames, false),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := ch
			c.Action = engine.ProfileSwitch
			return a.profileChange(cmd, args, c, f, &trust)
		},
	}
	cmd.Flags().BoolVar(&ch.New, "new", false, "make the profile first (empty unless --from or --bundle)")
	cmd.Flags().StringVar(&ch.From, "from", "", "the profile whose entries and targets the new one copies (never a pin)")
	cmd.Flags().StringSliceVar(&ch.Bundle, "bundle", nil, "the member profiles of the new bundle, in order (comma-separated)")
	_ = cmd.RegisterFlagCompletionFunc("from", a.completeProfiles(false))
	_ = cmd.RegisterFlagCompletionFunc("bundle", a.completeProfiles(false))
	addSyncFlags(cmd, &f, &trust, syncFlagHelp{yes: profileYesHelp, force: profileForceHelp})
	aliasFlags(cmd, map[string][]string{"new": {"create"}})
	return cmd
}

// profileEditNewMsg is the refusal of edit --new without a profile's name.
const profileEditNewMsg = "--new makes the profile edit names, and none was named: name it, as in lola profile edit <name> --new"

func (a *app) profileEditCmd() *cobra.Command {
	var f syncFlags
	var trust trustFlag
	var ef editFlags
	cmd := &cobra.Command{
		Use:     "edit [<name>] <module>...",
		Aliases: []string{"pick", "modify"},
		Short:   "Change a profile's modules and picks, as lola install would",
		Long: "Change the named profile's modules and their picks, as lola install would, applying it in each scope\n" +
			"it is on; with no name, the profile edits go to. With no module on a terminal, open the pages.\n" +
			"--new makes the profile first; --bundle sets a bundle's members.",
		Example: "  lola profile edit work --bundle go,docs\n  lola profile edit go-review cc-skills-golang --only skill/golang-testing\n" +
			"  lola profile edit try --new",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && given(cmd, "new") {
				return errs.WithHints(errors.New(profileEditNewMsg), "usage: lola profile edit <name> --new", "find one: lola profile list")
			}
			return nil
		},
		ValidArgsFunction: a.completeEdit,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := profileTrust(cmd, args, &trust, &f); err != nil {
				return err
			}
			return a.profileEdit(cmd, args, ef, f)
		},
	}
	cmd.Flags().BoolVar(&ef.create, "new", false, "make the profile first (empty unless --from or --bundle)")
	cmd.Flags().StringVar(&ef.from, "from", "", "the profile whose entries and targets the new one copies (never a pin)")
	cmd.Flags().StringSliceVar(&ef.members, "bundle", nil, "the bundle's member profiles, in order (comma-separated)")
	cmd.Flags().StringSliceVar(&ef.skips, "skip", nil, "leave these components out (kind/name; repeatable)")
	cmd.Flags().StringSliceVar(&ef.onlys, "only", nil, "take only these components (kind/name; repeatable)")
	cmd.Flags().StringSliceVar(&ef.unskips, "unskip", nil, "take these components back (kind/name; repeatable)")
	cmd.Flags().BoolVar(&ef.exact, "exact", false, "save --only as an only: list; later components stay out")
	cmd.Flags().BoolVar(&ef.withMentions, "with-mentions", false, "also take components your picks mention by name (one hop)")
	cmd.Flags().BoolVar(&ef.override, "override", false, "give the profile its own entry for another layer's module")
	a.addTargetFlag(cmd, &ef.targets, "assistants; <id>@<version> also picks the version")
	cmd.Flags().String("ref", "", "the branch, tag or commit to fetch") // profileEdit reads --ref and --path by name
	cmd.Flags().String("path", "", "the module root in the source")
	for _, name := range []string{"skip", "only", "unskip"} {
		_ = cmd.RegisterFlagCompletionFunc(name, a.completeComponents(1)) // lola profile edit <profile> <module>...
	}
	_ = cmd.RegisterFlagCompletionFunc("from", a.completeProfiles(false))
	_ = cmd.RegisterFlagCompletionFunc("bundle", a.completeProfiles(false))
	_ = cmd.RegisterFlagCompletionFunc("ref", cobra.NoFileCompletions)
	_ = cmd.RegisterFlagCompletionFunc("path", completeDirs)
	addSyncFlags(cmd, &f, &trust, syncFlagHelp{yes: profileYesHelp, force: profileForceHelp})
	aliasFlags(cmd, map[string][]string{"new": {"create"}})
	return cmd
}

func (a *app) profileMoveCmd() *cobra.Command {
	var f syncFlags
	var trust trustFlag
	var to, from string
	var switchTo, keepOff bool
	cmd := &cobra.Command{
		Use:     "move <module>...",
		Aliases: []string{"mv"},
		Short:   "Move modules' entries from a profile, base or project.yml into another",
		Long: "Move the modules' entries, keeping their version, from the layer each comes from (or --from's) to\n" +
			"--to: a profile, or base. Into a profile that is off, --switch turns it on and --keep-off leaves it\n" +
			"off, removing the modules' files here; without either a terminal asks. With --user and -C, the\n" +
			"entries move from that project into user scope.",
		Example: profileMoveGuide.example(),
		Args: func(cmd *cobra.Command, args []string) error {
			if err := profileTrust(cmd, args, &trust, &f); err != nil { // first, so a hint repeats it as parsed
				return err
			}
			if err := profileMoveGuide.args(1)(cmd, args); err != nil {
				return err
			}
			if given(cmd, "switch") && given(cmd, "keep-off") {
				return errs.WithHints(errors.New("--switch and --keep-off cannot be combined; pick one"),
					runWithout(cmd, args, "keep-off"), runWithout(cmd, args, "switch"))
			}
			return nil
		},
		ValidArgsFunction: a.completeMove,
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := a.engine()
			if err != nil {
				return err
			}
			return a.profileMove(cmd, eng, args, from, to, keepOff, switchTo, f)
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "the profile the entries move to, or base")
	cmd.Flags().StringVar(&from, "from", "", "the layer the entries leave: a profile, base or project.yml")
	cmd.Flags().BoolVar(&switchTo, "switch", false, "into a profile that is off: turn it on")
	cmd.Flags().BoolVar(&keepOff, "keep-off", false, "into a profile that is off: keep it off, removing its files here")
	_ = cmd.RegisterFlagCompletionFunc("to", a.completeProfiles(true))
	_ = cmd.RegisterFlagCompletionFunc("from", a.completeProfiles(true))
	addSyncFlags(cmd, &f, &trust, syncFlagHelp{yes: profileYesHelp, force: profileForceHelp})
	return cmd
}

func (a *app) profileSplitCmd() *cobra.Command {
	var f syncFlags
	var trust trustFlag
	var to string
	cmd := &cobra.Command{
		Use:   "split <name>",
		Short: "Split a profile of bundle: and modules: into a bundle and a profile of the modules",
		Long: "Split a profile that lists both bundle: and modules: into a bundle and a new profile of its modules,\n" +
			"<name>-modules unless --to names it. It shows the plan and asks on a terminal (-y and --force answer).",
		Example: profileSplitGuide.example(),
		Args: func(cmd *cobra.Command, args []string) error {
			if err := profileSplitGuide.args(1)(cmd, args); err != nil {
				return err
			}
			if len(args) > 1 {
				return errs.WithHints(errors.New("split takes the name of one profile"), "see them: "+runAs([]string{"lola", "profile", "list"}, cmd, nil))
			}
			return nil
		},
		ValidArgsFunction: a.completeProfileArgs((*engine.Engine).ProfileNames, false),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := profileTrust(cmd, args, &trust, &f); err != nil {
				return err
			}
			eng, err := a.engine()
			if err != nil {
				return err
			}
			if to != "" {
				if err := refuseProfileName(eng, to, func(n string) string {
					return runWithout(cmd, args, "to") + " " + errs.ShellJoin([]string{"--to", untrusted.Text(n, untrusted.MaxName)})
				}); err != nil {
					return err
				}
			}
			return a.profileSplit(cmd, eng, args, to, f)
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "the new profile's name (default <name>-modules)")
	_ = cmd.RegisterFlagCompletionFunc("to", cobra.NoFileCompletions)
	addSyncFlags(cmd, &f, &trust, syncFlagHelp{yes: profileYesHelp, force: profileForceHelp})
	return cmd
}

func (a *app) profileAddCmd() *cobra.Command {
	var f syncFlags
	var trust trustFlag
	var rf remoteFlags
	cmd := &cobra.Command{
		Use:     "add <source> [<name>]",
		Aliases: []string{"get"},
		Short:   "Fetch a remote profile from a git repository or https archive",
		Long: "Fetch the profiles/ of <source>, a git repository or https archive, show the profile named (on a\n" +
			"terminal with none named, the one you pick) and its content, and ask before adding it; off a terminal\n" +
			"--trust answers. Nothing is turned on.",
		Example: "  lola profile add https://github.com/example/profiles go\n  lola profile add https://github.com/example/profiles --ref v1 --path team",
		Args: func(cmd *cobra.Command, args []string) error {
			if err := profileTrust(cmd, args, &trust, &f); err != nil { // first, so a hint repeats it as parsed
				return err
			}
			return profileAddRule(cmd, args)
		},
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return completeFiles(cmd, args, toComplete)
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := a.engine()
			if err != nil {
				return err
			}
			rf.source = args[0]
			if len(args) == 2 {
				if err := refuseProfileName(eng, args[1], func(n string) string { return runWithout(cmd, []string{args[0], n}) }); err != nil {
					return err
				}
			}
			return a.profileAdd(cmd, eng, args, rf, f)
		},
	}
	cmd.Flags().StringVar(&rf.ref, "ref", "", "the branch, tag or commit to fetch")
	cmd.Flags().StringVar(&rf.path, "path", "", "the directory holding profiles/ in the source")
	_ = cmd.RegisterFlagCompletionFunc("ref", cobra.NoFileCompletions)
	_ = cmd.RegisterFlagCompletionFunc("path", completeDirs)
	cmd.Flags().BoolVarP(&f.dry, "dry-run", "n", false, "show what add would keep, ask nothing and write nothing")
	addTrustFlag(cmd, &trust, "profile add", "trust without asking: profiles")
	return cmd
}

// profileAddRule is add's Args rule: a source and one name at most; a name in the source's place,
// with the source second, is refused with the run in the right order.
func profileAddRule(cmd *cobra.Command, args []string) error {
	switch {
	case len(args) == 0:
		return errs.WithHints(fmt.Errorf("%s needs a source", cmd.CommandPath()), "usage: "+verbUsage(cmd)+" [--ref <ref>] [--path <dir>]",
			"example: lola profile add https://github.com/example/profiles go")
	case len(args) > 2:
		var hints []string
		for _, n := range args[1:] {
			hints = append(hints, "add "+untrusted.Text(n, untrusted.MaxName)+": "+runWithout(cmd, []string{args[0], n}))
		}
		return errs.WithHints(errors.New("add takes one profile name; add them one at a time"), hints...)
	case len(args) == 2:
		err := module.ValidateName("profile", args[1])
		if err != nil && (strings.ContainsAny(args[1], ":/") || strings.HasSuffix(args[1], ".git")) {
			return errs.WithHints(err, runWithout(cmd, []string{args[1], args[0]}))
		}
	}
	return nil
}

func (a *app) profileUpdateCmd() *cobra.Command {
	var f syncFlags
	var trust trustFlag
	var all bool
	cmd := &cobra.Command{
		Use:     "update [<name>...]",
		Aliases: []string{"upgrade"},
		Short:   "Fetch remote profiles again, show what changed, ask, and apply",
		Long: "Fetch the named remote profiles again, or with --all every one you have; show what changed and ask\n" +
			"about each (off a terminal --trust answers), then apply the changes in each scope they are on.",
		Example: "  lola profile update go\n  lola profile update --all\n  lola profile update go --user --trust -y",
		Args: func(cmd *cobra.Command, args []string) error {
			if err := profileTrust(cmd, args, &trust, &f); err != nil { // first, so a hint repeats it as parsed
				return err
			}
			eng, err := a.engine()
			if err != nil {
				return err
			}
			// The scope flags before the names: a hint of the names' refusal must name scopes that work.
			if _, err := a.flagScope(cmd, eng, args, ""); err != nil {
				return err
			}
			return updateNamesRefusal(cmd, eng, args, given(cmd, "all"))
		},
		ValidArgsFunction: a.completeProfileArgs((*engine.Engine).RemoteProfileNames, true),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Without a scope flag an update answers only the profiles' question with --trust, never a
			// project's: entries written there would answer nothing.
			if trust.names(engine.TrustEntries) && a.scopeFlag == "" && !a.user && !a.project && a.root == "" {
				return errs.WithHints(errors.New("--trust=entries trusts a project's changes, which lola profile update answers only in a scope a flag names"),
					"name the project: "+runWithout(cmd, args)+" --project", "or with user scope too: "+runWithout(cmd, args)+" --scope both")
			}
			eng, err := a.engine()
			if err != nil {
				return err
			}
			return a.profileUpdate(cmd, eng, args, all, f)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "every remote profile you have")
	addSyncFlags(cmd, &f, &trust, syncFlagHelp{yes: profileYesHelp})
	return cmd
}

func (a *app) profileRmCmd() *cobra.Command {
	var f syncFlags
	cmd := &cobra.Command{
		Use:     "rm <name>",
		Aliases: []string{"remove", "delete", "del", "erase"},
		Short:   "Delete a profile of yours (to the trash) or a remote one",
		Long: "Delete a profile of yours, which goes to the trash, or a remote one. It asks on a terminal (-y\n" +
			"answers) and refuses a profile in use in user scope or the project here unless --force.",
		Example: profileRmGuide.example(),
		Args: func(cmd *cobra.Command, args []string) error {
			if err := profileRmGuide.args(1)(cmd, args); err != nil {
				return err
			}
			if len(args) > 1 {
				return oneEach(cmd, "rm deletes one profile; name it", nil, args)
			}
			return nil
		},
		ValidArgsFunction: a.completeProfileArgs((*engine.Engine).RemoveNames, false),
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := a.engine()
			if err != nil {
				return err
			}
			return a.profileRemove(cmd, eng, args, f)
		},
	}
	cmd.Flags().BoolVarP(&f.dry, "dry-run", "n", false, "print what rm would remove, write nothing")
	cmd.Flags().BoolVarP(&f.yes, "yes", "y", false, "remove without asking")
	cmd.Flags().BoolVar(&f.force, "force", false, "remove a profile in use, without asking")
	return cmd
}

// profileChange runs lola profile on, off or switch: its --trust, two or more names refused
// (oneName), the name --new would make refused when lola profile answers to it, --new's modifiers
// checked (newFlags), then the change applied (profileApply).
func (a *app) profileChange(cmd *cobra.Command, args []string, ch engine.ProfileChange, f syncFlags, trust *trustFlag) error {
	if err := profileTrust(cmd, args, trust, &f); err != nil {
		return err
	}
	eng, err := a.engine()
	if err != nil {
		return err
	}
	if len(args) > 1 {
		return a.oneName(cmd, eng, args, f)
	}
	if len(args) == 1 {
		ch.Name = args[0]
	}
	if ch.New {
		if err := refuseProfileName(eng, ch.Name, func(n string) string { return runWithout(cmd, []string{n}) }); err != nil {
			return err
		}
	}
	if err := a.newFlags(cmd, eng, args, ch, f); err != nil {
		return err
	}
	return a.profileApply(cmd, eng, ch, f)
}

// profileExists reports whether name names a profile in the scopes listing and showing read
// (readScopes): lola profile <name> shows it, and a verb called by its name notes that it won. A
// name show would refuse for another reason than naming none (ambiguous, unreadable) names one.
func (a *app) profileExists(cmd *cobra.Command, name string) bool {
	eng, err := a.engine()
	if err != nil {
		return false
	}
	scs, err := a.readScopes(eng)
	if err != nil {
		return false
	}
	_, _, err = eng.ProfileState(cmd.Context(), choiceScopes(scs), name)
	return !engine.IsUnknownProfile(err)
}

// listProfiles is bare lola profile and lola profile list: the profiles each scope read can use and
// which is on, then the notices.
func (a *app) listProfiles(cmd *cobra.Command) error {
	eng, err := a.engine()
	if err != nil {
		return err
	}
	scs, err := a.readScopes(eng)
	if err != nil {
		return err
	}
	a.printContext(cmd, eng, choiceScopes(scs), nil)
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
}

// showProfile is lola profile show <name>, and lola profile <name>: the profile and, per scope read,
// where it stands and the layer each of its modules comes from. More names refuse, hinting the
// listing and each one shown, as the noun names them; an unknown one hints switch --new.
func (a *app) showProfile(cmd *cobra.Command, args []string) error {
	noun := cmd
	if cmd.Name() == "show" {
		noun = cmd.Parent()
	}
	if len(args) > 1 {
		words := strings.Fields(noun.CommandPath())
		hints := []string{"list them all: " + runAs(words, cmd, nil)}
		for _, n := range args {
			hints = append(hints, "or show one: "+runAs(words, cmd, []string{n}))
		}
		return errs.WithHints(errors.New("name one profile to show"), hints...)
	}
	eng, err := a.engine()
	if err != nil {
		return err
	}
	scs, err := a.readScopes(eng)
	if err != nil {
		return err
	}
	scopes := choiceScopes(scs)
	a.printContext(cmd, eng, scopes, nil)
	states, notices, err := eng.ProfileState(cmd.Context(), scopes, args[0], outputFlags(cmd)...)
	if engine.IsUnknownProfile(err) {
		// switch --new is another command than the show, so it takes none of the run's consent.
		if exists, refused := eng.NewOrSwitch(scopes[0], engine.ProfileChange{Name: args[0]}, nil); !exists && refused == nil {
			err = errs.WithHints(err, "make it: "+runAs([]string{"lola", "profile", "switch"}, cmd, []string{args[0], "--new"}, consentFlags...))
		}
	}
	if err != nil {
		return err
	}
	return a.emit(cmd, func(w io.Writer, s style) { printProfileState(w, s, states, notices) },
		map[string]any{"profiles": states, "notices": notices})
}

// readScopes is the scopes listing and showing read: the project here and user scope, or those the
// scope flags name.
func (a *app) readScopes(eng *engine.Engine) ([]engine.ScopeChoice, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	req, err := a.scopeRequest(cwd, false)
	if err != nil {
		return nil, err
	}
	return eng.ResolveReadScope(req)
}

// oneName refuses two or more names for on or switch, one profile being on at a time: it hints the
// bundle of them, made and switched to, after which switch names it, keeping the run's --trust, -n
// and output flags (never -y or --force, which would answer removals the switch makes). The bundle's
// name and members are the engine's, read at the scope the run would take without asking (a found
// project, else user scope).
func (a *app) oneName(cmd *cobra.Command, eng *engine.Engine, args []string, f syncFlags) error {
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
	scs, err := a.readScopes(eng)
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
	// on never replaces the profile on unasked: a terminal is asked to
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
				words := slices.Concat([]string{"edit", other.Into, "--bundle", strings.Join(other.Joined, ",")}, flags, []string{"--force"})
				return eng.Command(sc, "profile", words...)
			}
			real := slices.DeleteFunc(slices.Clone(again), func(f string) bool { return f == "-n" })
			return a.editBundle(cmd, eng, scs[0], other.Into, other.Joined, engine.TargetRequest{Edit: true, Again: again}, f,
				edit(again), edit(real), true, false)
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
		printSync(w, s, name, r, nil, more, false)
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
		printReport(w, s, report{Result: r.Result, Verb: "synced", Sync: true, InSync: "no files to change",
			ToApply: r.ToApply, OffTerminal: r.OffTerminal})
	} else {
		printSync(w, s, name, r, nil, more, false)
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

// completeProfileArgs offers the profile names a verb of lola profile takes, as names lists them
// (ProfileNames, RemoveNames or RemoteProfileNames): for its first operand only, or with many for
// each, minus those given (remote:go counting as go); none with --all.
func (a *app) completeProfileArgs(names func(*engine.Engine) ([]string, error), many bool) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		eng, err := a.engine()
		if err != nil || given(cmd, "all") || len(args) > 0 && !many {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		list, err := names(eng)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		typed := slices.Clone(args)
		for i, g := range typed {
			typed[i] = strings.TrimPrefix(g, "remote:")
		}
		var out []cobra.Completion
		for _, n := range list {
			if strings.HasPrefix(n, toComplete) && !slices.Contains(typed, n) {
				out = append(out, n)
			}
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	}
}

// completeEdit offers lola profile edit's operands: the profile first, then modules, as install
// takes them.
func (a *app) completeEdit(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	out, d := a.completeSource(cmd, args, toComplete)
	eng, err := a.engine()
	if err != nil || len(args) > 0 {
		return out, d
	}
	if names, err := eng.ProfileNames(); err == nil {
		for _, n := range names {
			if strings.HasPrefix(n, toComplete) {
				out = append(out, n)
			}
		}
	}
	return out, d
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
