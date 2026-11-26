package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/market"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// given reports whether the bool flag name is set on cmd, and true: --json=false is no --json.
func given(cmd *cobra.Command, name string) bool {
	f := cmd.Flags().Lookup(name)
	return f != nil && f.Changed && f.Value.String() == "true"
}

// outputFlags is the output flags set on cmd (--json, --plain, --no-pager), which every command a
// lola market hint names keeps, as lola profile's do.
func outputFlags(cmd *cobra.Command) []string {
	var out []string
	for _, f := range []string{"json", "plain", "no-pager"} {
		if given(cmd, f) {
			out = append(out, "--"+f)
		}
	}
	return out
}

// marketAddGuide, marketEnableGuide and marketRmGuide are the usage rules of the verbs that need an
// operand: refusing one without it hints the usage and where to find one, never a run that lacks it.
var (
	marketAddGuide = argGuide{noun: "source", find: "lola market known", usage: "lola market add <source> [<name>] [--ref <ref>] [--path <path>] [-n]",
		examples: []string{"lola market add https://github.com/acme/plugins acme", "lola market add https://github.com/acme/plugins --ref v1.2 --path catalog"}}
	marketEnableGuide = argGuide{noun: "known marketplace id", find: "lola market known", usage: "lola market enable <id> [<name>] [-y] [-n]",
		examples: []string{"lola market enable superpowers", "lola market enable superpowers mine -y"}}
	marketShowGuide = argGuide{noun: "marketplace", find: "lola market list", usage: "lola market show <name> [<entry>]",
		examples: []string{"lola market show acme", "lola market show acme formatter -t claude-code"}}
	marketRmGuide = argGuide{noun: "marketplace", find: "lola market list", usage: "lola market rm <name> [-y] [-n]",
		examples: []string{"lola market rm acme -y"}}
)

func (a *app) marketCmd() *cobra.Command {
	var targets []string
	cmd := &cobra.Command{
		Use:     "market [<name> [<entry>]]",
		Aliases: []string{"marketplace", "markets"},
		Short:   "List, add and update the marketplaces whose catalogs you browse, and the ones lola knows",
		Long: "A marketplace is a catalog of modules, each with the source it is fetched from: Claude Code's\n" +
			".claude-plugin/marketplace.json or a lola YAML catalog. Yours are shared by every scope, listed in\n" +
			"$XDG_CONFIG_HOME/lola/markets.yaml; each fetched catalog is cached in $XDG_CACHE_HOME/lola/markets/.\n" +
			"\n" +
			"Without a subcommand, list them; on a terminal, a picker of yours and the known ones instead: x ticks,\n" +
			"/ filters, Enter acts, a adds, o opens a row's web page, esc or q leaves; u checks the ticked rows (or\n" +
			"the cursor's) and U every row upstream, then offers to update yours that need it; nothing is contacted\n" +
			"before. list, --json, --plain, --no-pager and config.yaml market_picker: false list instead.\n" +
			"lola market <name> [<entry>] is lola market show: the marketplace's source, pin, entries and skipped\n" +
			"entries, or with an entry, it as lola info @<name>/<entry> does, fetching it. Nothing else fetches or\n" +
			"asks but add, enable, update, rm and check, which asks each remote for its tip. lola skips an entry it\n" +
			"cannot use, with its reason, and runs nothing from a catalog. known lists the marketplaces lola ships,\n" +
			"each pinned to a commit; enable <id> registers one.",
		Example: "  lola market\n  lola market list\n  lola market check\n  lola market acme\n  lola market show acme formatter -t claude-code\n" +
			"  lola market add https://github.com/acme/plugins acme\n" +
			"  lola market add https://github.com/acme/plugins acme --ref v1.2 --path catalog\n" +
			"  lola market add https://raw.githubusercontent.com/acme/market/main/general.yml\n" +
			"  lola market update acme\n  lola market update acme beta\n" +
			"  lola market update acme --ssh-host git.example.com\n" +
			"  lola market update --all\n  lola market rm acme -y\n" +
			"  lola market known\n  lola market known superpowers\n  lola market enable superpowers\n  lola market enable superpowers mine -y",
		GroupID:                    groupCore,
		Args:                       cobra.ArbitraryArgs,
		SuggestionsMinimumDistance: 2,
		ValidArgsFunction:          a.completeMarket,
	}
	ns := nounSpec{
		noun: "marketplace",
		bare: func(cmd *cobra.Command) error {
			if cmd.Flags().Changed("target") {
				return errs.WithHints(errors.New("--target applies to lola market <name> <entry> only"), runWithout(cmd, nil, "target"))
			}
			eng, err := a.engine()
			if err != nil {
				return err
			}
			if err := a.marketScopeFlags(eng); err != nil {
				return err
			}
			return a.listMarkets(cmd, eng, true)
		},
		show:   a.marketShorthand,
		exists: a.marketExists,
		// pflag refuses set acme --enable (or --disable) at the flag, before set is read: coach it as the word.
		coached: func(_ *cobra.Command, word, flag string, operands, display []string) error {
			if word != "set" || (flag != "enable" && flag != "disable") {
				return nil
			}
			eng, err := a.engine()
			if err != nil {
				return err
			}
			return eng.CoachMarketVerb(engine.MarketVerb{Verb: word, Operands: operands, Enable: flag == "enable", Disable: flag == "disable", Display: display})
		},
	}
	cmd.RunE = a.nounRunE(ns)
	a.addTargetFlag(cmd, &targets, "with <name> <entry>: assistants to plan for, as install's -t")
	cmd.AddCommand(a.marketListCmd(), a.marketShowCmd(), a.marketAddCmd(), a.marketEnableCmd(), a.marketKnownCmd(),
		a.marketUpdateCmd(), a.marketRmCmd(), a.marketCheckCmd())
	a.noteVerbWins(cmd, ns)
	a.coachFlags(cmd, ns, map[string]oldFlag{"add": {verb: "add", takesValue: true}, "get": {verb: "add", takesValue: true},
		"enable": {verb: "enable", takesValue: true}, "known": {verb: "known"}, "check": {verb: "check"}, "no-interactive": {verb: "list"},
		"update": {verb: "update"}, "upgrade": {verb: "update"}, "refresh": {verb: "update"},
		"remove": {verb: "rm"}, "rm": {verb: "rm"}, "delete": {verb: "rm"}, "del": {verb: "rm"}, "erase": {verb: "rm"}})
	return cmd
}

func (a *app) marketListCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "list",
		Aliases:           []string{"ls"},
		Short:             "List your marketplaces",
		Long:              "List your marketplaces as text: never the picker bare lola market opens on a terminal. Nothing is fetched.",
		Example:           "  lola market list\n  lola market list --json",
		Args:              cobra.NoArgs,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, _ []string) error {
			eng, err := a.engine()
			if err != nil {
				return err
			}
			if err := a.marketScopeFlags(eng); err != nil {
				return err
			}
			return a.listMarkets(cmd, eng, false)
		},
	}
}

func (a *app) marketShowCmd() *cobra.Command {
	var targets []string
	cmd := &cobra.Command{
		Use:   "show <name> [<entry>]",
		Short: "Show a marketplace, or one of its entries",
		Long: "Show a marketplace's source, pin, entries and skipped entries; with an entry too, show it as\n" +
			"lola info @<name>/<entry> does, fetching it. Showing a marketplace never fetches or asks.",
		Example:           marketShowGuide.example(),
		Args:              marketShowGuide.args(1),
		ValidArgsFunction: a.completeMarket,
		RunE:              func(cmd *cobra.Command, args []string) error { return a.marketShow(cmd, args) },
	}
	a.addTargetFlag(cmd, &targets, "with an entry: assistants to plan for, as install's -t")
	return cmd
}

func (a *app) marketAddCmd() *cobra.Command {
	var ref, path, enableID string
	var sshHosts []string
	var dry, yes bool
	cmd := &cobra.Command{
		Use:     "add <source> [<name>]",
		Aliases: []string{"get"},
		Short:   "Register the catalog at a source as a marketplace",
		Long: "Fetch the catalog at <source> (git repository, https:// archive or catalog file, or folder) and\n" +
			"register it as <name>, its own name by default. --enable <id> is lola market enable <id> [<name>].",
		Example: "  lola market add https://github.com/acme/plugins acme\n" +
			"  lola market add https://github.com/acme/plugins acme --ref v1.2 --path catalog\n" +
			"  lola market add https://raw.githubusercontent.com/acme/market/main/general.yml\n  lola market add --enable superpowers",
		Args: marketAddArgs,
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
			if err := a.marketScopeFlags(eng); err != nil {
				return err
			}
			if cmd.Flags().Changed("enable") {
				return a.marketAddEnable(cmd, eng, args, enableID, dry, yes)
			}
			req := engine.MarketAddRequest{Source: args[0], Ref: ref, Path: path, DryRun: dry, Display: outputFlags(cmd), SSHHosts: sshHosts}
			if len(args) == 2 {
				req.Name = args[1]
				if err := eng.RefuseReserved("market", req.Name, func(n string) string { return runWithout(cmd, []string{args[0], n}) }); err != nil {
					return err
				}
			}
			return a.marketAdd(cmd, eng, req)
		},
	}
	cmd.Flags().StringVar(&ref, "ref", "", "the branch, tag or commit of a git source")
	cmd.Flags().StringVar(&path, "path", "", "the catalog file, or the directory holding it, in the source")
	_ = cmd.RegisterFlagCompletionFunc("ref", cobra.NoFileCompletions)
	_ = cmd.RegisterFlagCompletionFunc("path", completeDirs)
	cmd.Flags().StringArrayVar(&sshHosts, "ssh-host", nil, "let ssh sources at this host use your keys (repeatable)")
	_ = cmd.RegisterFlagCompletionFunc("ssh-host", cobra.NoFileCompletions)
	cmd.Flags().BoolVarP(&dry, "dry-run", "n", false, "report what add would do, write nothing")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do it without asking (only beside --enable)")
	cmd.Flags().StringVar(&enableID, "enable", "", "register the known marketplace with this id, as enable does")
	_ = cmd.RegisterFlagCompletionFunc("enable", a.completeKnown)
	return cmd
}

func (a *app) marketEnableCmd() *cobra.Command {
	var sshHosts []string
	var dry, yes bool
	cmd := &cobra.Command{
		Use:   "enable <id> [<name>]",
		Short: "Register a known marketplace at its pinned commit",
		Long: "Register the known marketplace <id> (lola market known lists them) at the commit the maintainers\n" +
			"pinned, as <name>, its id by default. It asks on a terminal with what the pinned catalog holds; -y\n" +
			"answers, and -n previews and asks nothing.",
		Example: marketEnableGuide.example(),
		Args: func(cmd *cobra.Command, args []string) error {
			if err := marketEnableGuide.args(1)(cmd, args); err != nil {
				return err
			}
			if len(args) > 2 {
				return oneEach(cmd, "enable registers one marketplace; give it one name", args[:1], args[1:])
			}
			return nil
		},
		ValidArgsFunction: a.completeKnownFirst,
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := a.engine()
			if err != nil {
				return err
			}
			if err := a.marketScopeFlags(eng); err != nil {
				return err
			}
			name := ""
			if len(args) == 2 {
				name = args[1]
				if err := eng.RefuseReserved("market", name, func(n string) string { return runWithout(cmd, []string{args[0], n}) }); err != nil {
					return err
				}
			}
			return a.marketEnable(cmd, eng, args, args[0], name, dry, yes, sshHosts)
		},
	}
	cmd.Flags().StringArrayVar(&sshHosts, "ssh-host", nil, "let ssh sources at this host use your keys (repeatable)")
	_ = cmd.RegisterFlagCompletionFunc("ssh-host", cobra.NoFileCompletions)
	cmd.Flags().BoolVarP(&dry, "dry-run", "n", false, "report what enable would do, write nothing")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do it without asking")
	return cmd
}

func (a *app) marketKnownCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "known [<id>]",
		Short: "List the marketplaces lola ships, or show one by id",
		Long: "List the marketplaces lola ships, each pinned to a commit, or show one by id. It enables nothing:\n" +
			"lola market enable <id> does.",
		Example: "  lola market known\n  lola market known superpowers",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 1 {
				return oneEach(cmd, "known shows one marketplace; give it one id", nil, args)
			}
			return nil
		},
		ValidArgsFunction: a.completeKnownFirst,
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := a.engine()
			if err != nil {
				return err
			}
			if err := a.marketScopeFlags(eng); err != nil {
				return err
			}
			return a.marketKnown(cmd, eng, args)
		},
	}
}

func (a *app) marketUpdateCmd() *cobra.Command {
	var all, dry bool
	var sshHosts []string
	cmd := &cobra.Command{
		Use:     "update [<name>...]",
		Aliases: []string{"upgrade"},
		Short:   "Fetch the named marketplaces' catalogs again and report what changed",
		Long: "Fetch the named marketplaces' catalogs again and report what changed; --all updates every\n" +
			"marketplace you have. One that failed makes the run fail once the others are written.",
		Example: "  lola market update acme\n  lola market update acme beta\n  lola market update acme --ssh-host git.example.com\n" +
			"  lola market update --all",
		Args: func(cmd *cobra.Command, args []string) error {
			all := cmd.Flags().Changed("all")
			switch {
			case all && len(args) > 0:
				return errs.WithHints(errors.New("--all updates every marketplace you have, and takes no names"),
					runWithout(cmd, nil), runWithout(cmd, args, "all"))
			case !all && len(args) == 0:
				return errs.WithHints(errors.New("name the marketplaces to update, or update every one with --all"), runWithout(cmd, nil)+" --all")
			}
			return nil
		},
		ValidArgsFunction: a.completeMarket,
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := a.engine()
			if err != nil {
				return err
			}
			if err := a.marketScopeFlags(eng); err != nil {
				return err
			}
			return a.marketUpdate(cmd, eng, args, all, dry, sshHosts)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "every marketplace you have")
	cmd.Flags().StringArrayVar(&sshHosts, "ssh-host", nil, "let ssh sources at this host use your keys (repeatable)")
	_ = cmd.RegisterFlagCompletionFunc("ssh-host", cobra.NoFileCompletions)
	cmd.Flags().BoolVarP(&dry, "dry-run", "n", false, "report what update would do, write nothing")
	return cmd
}

func (a *app) marketRmCmd() *cobra.Command {
	var sshHosts []string
	var dry, yes bool
	cmd := &cobra.Command{
		Use:     "rm <name>",
		Aliases: []string{"remove", "delete", "del", "erase"},
		Short:   "Forget a marketplace and drop its cached catalog",
		Long: "Forget the marketplace and drop its cached catalog; it asks on a terminal, -y answers, and off a\n" +
			"terminal without -y it refuses. With --ssh-host, it only stops allowing those ssh hosts, which asks nothing.",
		Example: marketRmGuide.example() + "\n  lola market rm acme --ssh-host git.example.com",
		Args: func(cmd *cobra.Command, args []string) error {
			if err := marketRmGuide.args(1)(cmd, args); err != nil {
				return err
			}
			switch {
			case len(args) > 1 && cmd.Flags().Changed("ssh-host"):
				return oneEach(cmd, "rm --ssh-host stops allowing hosts for one marketplace; name it", nil, args)
			case len(args) > 1:
				return oneEach(cmd, "rm forgets one marketplace; name it", nil, args)
			}
			return nil
		},
		ValidArgsFunction: a.completeMarket,
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := a.engine()
			if err != nil {
				return err
			}
			if err := a.marketScopeFlags(eng); err != nil {
				return err
			}
			if len(sshHosts) > 0 {
				return a.marketRevoke(cmd, eng, args, sshHosts, dry, yes)
			}
			return a.marketRemove(cmd, eng, args, dry, yes)
		},
	}
	cmd.Flags().StringArrayVar(&sshHosts, "ssh-host", nil, "stop allowing ssh sources at this host to use your keys (repeatable)")
	_ = cmd.RegisterFlagCompletionFunc("ssh-host", cobra.NoFileCompletions)
	cmd.Flags().BoolVarP(&dry, "dry-run", "n", false, "report what rm would do, write nothing")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do it without asking")
	return cmd
}

func (a *app) marketCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "check",
		Short:             "List each git marketplace's upstream status",
		Long:              "List each git marketplace's upstream status, asking each remote for its tip; no catalog is fetched.",
		Example:           "  lola market check\n  lola market check --json",
		Args:              cobra.NoArgs,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, _ []string) error {
			eng, err := a.engine()
			if err != nil {
				return err
			}
			if err := a.marketScopeFlags(eng); err != nil {
				return err
			}
			checked, notices, err := eng.CheckedMarkets(cmd.Context(), outputFlags(cmd))
			if err != nil {
				return err
			}
			rows, status := make([]engine.MarketRow, len(checked)), make([]string, len(checked))
			for i, c := range checked {
				rows[i], status[i] = c.MarketRow, c.Status
			}
			return a.emit(cmd, func(w io.Writer, s style) { printMarkets(w, s, rows, status, notices) },
				map[string]any{"markets": checked, "notices": notices})
		},
	}
}

// marketExists reports whether name is one of your marketplaces: lola market <name> shows it, and
// a verb called by its name notes that it won.
func (a *app) marketExists(_ *cobra.Command, name string) bool {
	eng, err := a.engine()
	if err != nil {
		return false
	}
	names, err := eng.MarketNames()
	return err == nil && slices.Contains(names, name)
}

// marketScopeFlags refuses scope flags that name a scope that cannot be: they change nothing in a
// market verb, which is shared by every scope, but one that cannot be read is an error.
func (a *app) marketScopeFlags(eng *engine.Engine) error {
	if a.scopeFlag == "" && !a.user && !a.project && a.root == "" {
		return nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	req, err := a.scopeRequest(cwd, false)
	if err != nil {
		return err
	}
	_, err = eng.ResolveReadScope(req)
	return err
}

// listMarkets is lola market list, and bare lola market: your marketplaces as text, or, with
// picker, on a terminal with market_picker not false, the picker.
func (a *app) listMarkets(cmd *cobra.Command, eng *engine.Engine, picker bool) error {
	if picker {
		open, err := a.marketPickerOpens(cmd, eng)
		if err != nil {
			return err
		}
		if open {
			return a.marketPick(cmd, eng)
		}
	}
	rows, notices, err := eng.Markets(outputFlags(cmd))
	if err != nil {
		return err
	}
	return a.emit(cmd, func(w io.Writer, s style) { printMarkets(w, s, rows, nil, notices) },
		map[string]any{"markets": rows, "notices": notices})
}

// marketShorthand is lola market <name> [<entry>] for the first word of the run: a word of another
// tool's lola market (set, search) is answered with the lola command; any other is lola market show.
func (a *app) marketShorthand(cmd *cobra.Command, args []string) error {
	if slices.Contains(engine.CoachedMarketWords(), args[0]) {
		eng, err := a.engine()
		if err != nil {
			return err
		}
		return eng.CoachMarketVerb(engine.MarketVerb{Verb: args[0], Operands: args[1:], Display: outputFlags(cmd)})
	}
	return a.marketShow(cmd, args)
}

// marketShow is lola market show <name> [<entry>], and lola market <name> [<entry>] (shorthand):
// the marketplace, or with an entry too, lola info @<name>/<entry>.
func (a *app) marketShow(cmd *cobra.Command, args []string) error {
	eng, err := a.engine()
	if err != nil {
		return err
	}
	if len(args) > 2 {
		return tooManyEntries(cmd, eng, args)
	}
	entry := len(args) == 2
	if cmd.Flags().Changed("target") && !entry {
		return errs.WithHints(errors.New("--target applies to lola market <name> <entry> only"), runWithout(cmd, args, "target"))
	}
	if entry {
		// The run of lola info @<name>/<entry>, which resolves its scope itself.
		return a.showInfo(cmd, "@"+args[0]+"/"+args[1])
	}
	if err := a.marketScopeFlags(eng); err != nil {
		return err
	}
	v, err := eng.ShowMarket(args[0], outputFlags(cmd))
	if err != nil {
		return err
	}
	// The next step, lola info of the first entry, keeping the run's flags.
	hints := []string{}
	if len(v.Entries) > 0 {
		hints = append(hints, runAs([]string{"lola", "info"}, cmd, []string{v.Entries[0].Install}))
	}
	width := a.width(cmd.OutOrStdout())
	return a.emit(cmd, func(w io.Writer, s style) { printMarket(w, s, v, hints, width) }, marketShown{MarketView: v, Hints: hints})
}

// marketShown is lola market <name>'s JSON: the marketplace, and hints, the lola info of its first
// entry with the run's flags ([] without entries).
type marketShown struct {
	*engine.MarketView
	Hints []string `json:"hints"`
}

// tooManyEntries refuses lola market <name> <entry> <entry>...: it shows one entry.
// Each hint is the run for one entry the catalog lists and does not skip, with the run's flags;
// with none (or no such marketplace), the run that lists the marketplace, else the marketplaces.
func tooManyEntries(cmd *cobra.Command, eng *engine.Engine, args []string) error {
	var listed []string
	v, err := eng.ShowMarket(args[0], outputFlags(cmd))
	if err == nil {
		for _, e := range v.Entries {
			listed = append(listed, e.Name)
		}
	}
	var entries, hints []string
	for _, e := range args[1:] {
		entries = append(entries, untrusted.Text(e, untrusted.MaxName))
		if h := runWithout(cmd, []string{args[0], e}); slices.Contains(listed, e) && !slices.Contains(hints, h) {
			hints = append(hints, h)
		}
	}
	if len(hints) == 0 {
		hints = []string{engine.MarketRun(outputFlags(cmd)...)}
		if err == nil {
			hints = []string{engine.MarketRun(append([]string{args[0]}, outputFlags(cmd)...)...)}
		}
	}
	return errs.WithHints(fmt.Errorf("lola market <name> <entry> shows one entry, and %d were given: %s", len(entries),
		strings.Join(entries, ", ")), hints...)
}

// marketAdd runs lola market add <source> [<name>] (req): a name another marketplace has, or a
// catalog's own name no marketplace can have, is asked again on a terminal, never under --json or a
// dry run.
func (a *app) marketAdd(cmd *cobra.Command, eng *engine.Engine, req engine.MarketAddRequest) error {
	if p := a.prompter(cmd); p.Interactive() {
		req.Rename = func(st engine.MarketRename) (string, error) {
			return p.MarketName(MarketNameStep{Why: st.Why, Value: st.Suggest, Problem: st.Problem})
		}
		req.PickSSH = sshPicker(p)
	}
	res, err := eng.AddMarket(cmd.Context(), req)
	if errors.Is(err, ErrAborted) {
		return ErrAborted
	}
	if err != nil {
		return err
	}
	return a.emit(cmd, func(w io.Writer, s style) { printMarketResult(w, s, res) }, res)
}

// marketAddArgs is add's Args rule: a source, which --enable replaces with an id, and one name at
// most; -y answers --enable's question only. With --enable, a source (an operand that looks like
// one), --ref, --path, --ssh-host or a second name is refused with the run of each form that works.
func marketAddArgs(cmd *cobra.Command, args []string) error {
	if !cmd.Flags().Changed("enable") {
		if err := marketAddGuide.args(1)(cmd, args); err != nil {
			return err
		}
		switch {
		case cmd.Flags().Changed("yes"):
			return errs.WithHints(errors.New("--yes applies to add --enable only: add asks nothing to answer"), runWithout(cmd, args, "yes"))
		case len(args) > 2:
			return oneEach(cmd, "add registers one marketplace; give it one name", args[:1], args[1:])
		}
		return nil
	}
	var src string
	names := slices.Clone(args)
	if len(names) > 0 && looksLikeSource(names[0]) {
		src, names = names[0], names[1:]
	}
	if src == "" && len(names) < 2 && !cmd.Flags().Changed("ref") && !cmd.Flags().Changed("path") && !cmd.Flags().Changed("ssh-host") {
		return nil
	}
	id, _ := cmd.Flags().GetString("enable")
	name := names[:min(1, len(names))]
	hints := []string{runAs([]string{"lola", "market", "enable"}, cmd, slices.Concat([]string{id}, name))}
	if src != "" {
		hints = append(hints, runAs([]string{"lola", "market", "add"}, cmd, slices.Concat([]string{src}, name), "enable", "yes"))
	}
	return errs.WithHints(errors.New("add --enable registers a known marketplace: it takes an id and a name, no source, --ref, --path or --ssh-host"), hints...)
}

// oneEach refuses more names than a verb takes, hinting the run once for each name, after the
// operands in lead, keeping the run's flags.
func oneEach(cmd *cobra.Command, msg string, lead, names []string) error {
	var hints []string
	for _, n := range names {
		if h := runWithout(cmd, append(slices.Clone(lead), n)); !slices.Contains(hints, h) {
			hints = append(hints, h)
		}
	}
	return errs.WithHints(errors.New(msg), hints...)
}

// marketAddEnable is lola market add --enable <id> [<name>], which is lola market enable <id>
// [<name>] (marketAddArgs refused anything else).
func (a *app) marketAddEnable(cmd *cobra.Command, eng *engine.Engine, args []string, id string, dry, yes bool) error {
	name := ""
	if len(args) == 1 {
		name = args[0]
		if err := eng.RefuseReserved("market", name, func(n string) string { return runWithout(cmd, []string{n}) }); err != nil {
			return err
		}
	}
	return a.marketEnable(cmd, eng, args, id, name, dry, yes, nil)
}

// looksLikeSource reports whether word is more likely a marketplace's source than a name: it
// cannot be a name, and has a scheme, a path or a .git suffix.
func looksLikeSource(word string) bool {
	return market.ValidateName(word) != nil && (strings.ContainsAny(word, ":/") || strings.HasSuffix(word, ".git"))
}

// marketRemove runs lola market rm <name>: one name; it asks on a terminal,
// -y answers, and off a terminal without -y the engine refuses.
func (a *app) marketRemove(cmd *cobra.Command, eng *engine.Engine, args []string, dry, yes bool) error {
	req := engine.MarketRemoveRequest{Name: args[0], DryRun: dry, Display: outputFlags(cmd)}
	switch p := a.prompter(cmd); {
	case yes:
		req.Confirm = func(engine.MarketRow) (bool, error) { return true, nil }
	case p.Interactive():
		req.Confirm = func(r engine.MarketRow) (bool, error) {
			return askYes(p, Question{Prompt: "Forget marketplace " + r.Name + " (" + r.Source + ") and drop its cached catalog?"})
		}
	}
	res, err := eng.RemoveMarket(req)
	if errors.Is(err, engine.ErrDeclined) || errors.Is(err, ErrAborted) {
		return ErrAborted
	}
	if err != nil {
		return err
	}
	return a.emit(cmd, func(w io.Writer, s style) {
		for _, n := range res.Removed {
			switch {
			case s.plain && res.DryRun:
				fmt.Fprintf(w, "would-remove\t%s\n", n)
			case s.plain:
				fmt.Fprintf(w, "removed\t%s\n", n)
			case res.DryRun:
				fmt.Fprintln(w, s.info("would forget marketplace "+n+" and drop its cached catalog"))
			default:
				fmt.Fprintln(w, s.ok("forgot marketplace "+n+" and dropped its cached catalog"))
			}
		}
		for _, n := range res.Notices {
			fmt.Fprintln(w, noticeLine(s, n))
		}
	}, res)
}

// sshPicker asks p which of a catalog's ssh hosts to allow, for an add's or update's PickSSH.
func sshPicker(p Prompter) func(engine.MarketSSHStep) ([]string, error) {
	return func(st engine.MarketSSHStep) ([]string, error) {
		hosts := make([]SSHHostChoice, 0, len(st.Hosts))
		for _, h := range st.Hosts {
			hosts = append(hosts, SSHHostChoice{Host: h.Host, Entries: h.Entries})
		}
		var nav Nav
		if st.Back {
			nav = Nav{Back: true, Answered: 1} // the name question
		}
		return p.SSHHosts(SSHHostStep{Market: st.Market, Hosts: hosts, Nav: nav})
	}
}

// marketRevoke runs lola market rm <name> --ssh-host <host>...: one
// name; it asks nothing and takes no -y, since the host can be allowed again.
func (a *app) marketRevoke(cmd *cobra.Command, eng *engine.Engine, args, hosts []string, dry, yes bool) error {
	if yes {
		return errs.WithHints(errors.New("--yes applies to rm without --ssh-host: stopping allowing an ssh host asks nothing"),
			runWithout(cmd, args, "yes"))
	}
	res, err := eng.RevokeSSHHosts(engine.MarketSSHRevokeRequest{Name: args[0], Hosts: hosts, DryRun: dry, Display: outputFlags(cmd)})
	if err != nil {
		return err
	}
	// The engine accepted every host, so each is a host name, lower-cased as it stored it.
	var named []string
	for _, h := range hosts {
		named = append(named, untrusted.Text(strings.ToLower(h), untrusted.MaxName))
	}
	named = market.NormalizeHosts(named)
	what := "ssh host "
	if len(named) > 1 {
		what = "ssh hosts "
	}
	return a.emit(cmd, func(w io.Writer, s style) {
		switch {
		case s.plain:
		case res.DryRun:
			fmt.Fprintln(w, s.info("marketplace "+res.Updates[0].Market+" would no longer allow "+what+strings.Join(named, ", ")))
		default:
			fmt.Fprintln(w, s.ok("marketplace "+res.Updates[0].Market+" no longer allows "+what+strings.Join(named, ", ")))
		}
		printMarketResult(w, s, res)
	}, res)
}

// marketUpdate runs lola market update <name>..., or update --all (update's Args refuses both or
// neither). A marketplace that failed makes the run fail once the others are written.
func (a *app) marketUpdate(cmd *cobra.Command, eng *engine.Engine, args []string, all, dry bool, sshHosts []string) error {
	req := engine.MarketUpdateRequest{Names: args, All: all, DryRun: dry, Display: outputFlags(cmd), SSHHosts: sshHosts}
	if p := a.prompter(cmd); p.Interactive() {
		req.PickSSH = sshPicker(p)
	}
	res, err := eng.UpdateMarkets(cmd.Context(), req)
	if err != nil {
		return err
	}
	failed := marketFailed(res)
	// Ctrl+C in the host picker of a run's one marketplace; among several, it costs that one
	// marketplace, as any failure does.
	if errors.Is(failed, ErrAborted) {
		return &abortedError{"cancelled — no ssh host was allowed"}
	}
	if a.json && failed != nil {
		return &marketError{err: failed, res: res}
	}
	if err := a.emit(cmd, func(w io.Writer, s style) { printMarketResult(w, s, res) }, res); err != nil {
		return err
	}
	return failed
}

// marketFailed is the error of the marketplaces of the run that were not updated, nil for none:
// the marketplace's own when it was the run's only one; otherwise "1 of 2 marketplaces was not
// updated: acme: <why>", with every failure's hints.
func marketFailed(res *engine.MarketResult) error {
	var whys, hints []string
	for _, u := range res.Updates {
		switch u.Outcome {
		case engine.MarketFailed:
			if len(res.Updates) == 1 {
				return u.Err
			}
			whys = append(whys, u.Market+": "+u.Error)
			for _, h := range u.Hints {
				if !slices.Contains(hints, h) {
					hints = append(hints, h)
				}
			}
		case engine.MarketStopped:
			whys = append(whys, u.Market+": stopped before it was fetched")
		}
	}
	if len(whys) == 0 {
		return nil
	}
	verb := " was"
	if len(whys) > 1 {
		verb = " were"
	}
	return errs.WithHints(fmt.Errorf("%d of %d marketplaces%s not updated: %s", len(whys), len(res.Updates), verb, strings.Join(whys, "; ")), hints...)
}

// marketError is an update in which a marketplace failed; under --json its document is the
// run's, beside error and hints.
type marketError struct {
	err error
	res *engine.MarketResult
}

func (e *marketError) Error() string { return e.err.Error() }
func (e *marketError) Unwrap() error { return e.err }

// JSONFields carries the run's document beside error and hints.
func (e *marketError) JSONFields() map[string]any {
	return map[string]any{"markets": e.res.Markets, "changes": e.res.Changes, "updates": e.res.Updates, "notices": e.res.Notices,
		"dry_run": e.res.DryRun}
}

// completeMarket offers your marketplaces' names, minus those given; none after update --all,
// nor a second for rm, which takes one, or for show beyond its entry.
func (a *app) completeMarket(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	eng, err := a.engine()
	if err != nil || cmd.Flags().Changed("all") || (cmd.Name() == "rm" || cmd.Name() == "show" || cmd.Name() == "market") && len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	names, err := eng.MarketNames()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []cobra.Completion
	for _, n := range names {
		if strings.HasPrefix(n, toComplete) && !slices.Contains(args, n) {
			out = append(out, untrusted.Text(n, untrusted.MaxName))
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// cleanKnown is k with every text through untrusted.Text, limits at what the seed loader allows,
// so it changes nothing a loaded seed holds: the one place lola's known-marketplace text is cleaned
// for the human, --plain and JSON output alike.
func cleanKnown(k market.KnownMarket) market.KnownMarket {
	return market.KnownMarket{ID: untrusted.Text(k.ID, untrusted.MaxName), Source: untrusted.Text(k.Source, 4096),
		Path: untrusted.Text(k.Path, 4096), Commit: untrusted.Text(k.Commit, untrusted.MaxName),
		Curated: untrusted.Text(k.Curated, untrusted.MaxName), Reason: untrusted.Text(k.Reason, 2*untrusted.MaxName)}
}

// knownEnabled is lola market enable's JSON, the plan a refusal carries too: the run's result and
// the known marketplace it registers (or would).
type knownEnabled struct {
	*engine.MarketResult
	Known market.KnownMarket `json:"known"`
}

// marketEnable runs lola market enable <id> [<name>] (name is "" for the id, the default): args
// are the run's operands, which its hints repeat. It asks on a terminal with what the pinned catalog
// holds, -y answers, and off a terminal without -y it refuses with the run and -y, the plan under
// --json; -n previews and asks nothing. It never asks for another name or an ssh host: a name
// another marketplace holds is refused with the run that names a free one, and --ssh-host allows a
// host.
func (a *app) marketEnable(cmd *cobra.Command, eng *engine.Engine, args []string, id, name string, dry, yes bool, sshHosts []string) error {
	if name == "" {
		name = id
	}
	req := engine.MarketAddRequest{DryRun: dry, Display: outputFlags(cmd), SSHHosts: sshHosts}
	var confirm engine.KnownConfirm
	if !dry && !yes {
		confirm = func(k market.KnownMarket, preview *engine.MarketResult) error {
			k = cleanKnown(k)
			show := func(w io.Writer, s style) { printKnownPreview(w, s, k, preview) }
			return a.confirm(cmd, args, "Enable known marketplace "+untrusted.Text(name, untrusted.MaxName)+"?", Nav{}, knownEnabled{preview, k}, show)
		}
	}
	res, k, err := eng.EnableKnown(cmd.Context(), id, name, req, confirm)
	if errors.Is(err, ErrAborted) {
		return ErrAborted
	}
	if err != nil {
		return err
	}
	k = cleanKnown(k)
	return a.emit(cmd, func(w io.Writer, s style) { printKnownEnabled(w, s, k, res) }, knownEnabled{res, k})
}

// marketKnown runs lola market known [<id>]: the marketplaces lola ships, or the one whose id
// is given. Both are the document {known, hints}, known an array (of one for an id).
func (a *app) marketKnown(cmd *cobra.Command, eng *engine.Engine, args []string) error {
	display := outputFlags(cmd)
	var list []engine.KnownEntry
	hints := []string{}
	if len(args) == 1 {
		e, err := eng.ShowKnown(args[0], display)
		if err != nil {
			return err
		}
		list = []engine.KnownEntry{e}
		if e.Enabled {
			hints = append(hints, engine.MarketRun(append([]string{e.ID}, display...)...))
		} else {
			hints = append(hints, engine.MarketRun(append([]string{"enable", e.ID}, display...)...))
		}
	} else {
		var err error
		if list, err = eng.Known(); err != nil {
			return err
		}
		if len(list) > 0 {
			hints = append(hints, engine.MarketRun(append([]string{"known", list[0].ID}, display...)...))
		}
	}
	for i := range list {
		list[i].KnownMarket = cleanKnown(list[i].KnownMarket)
	}
	return a.emit(cmd, func(w io.Writer, s style) { printKnown(w, s, list, len(args) == 1, hints) },
		map[string]any{"known": list, "hints": hints})
}

// completeKnownFirst offers the ids of the known marketplaces for a verb's first operand.
func (a *app) completeKnownFirst(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return a.completeKnown(cmd, args, toComplete)
}

// completeKnown offers the ids of the known marketplaces.
func (a *app) completeKnown(*cobra.Command, []string, string) ([]cobra.Completion, cobra.ShellCompDirective) {
	eng, err := a.engine()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	list, err := eng.Known()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []cobra.Completion
	for _, k := range list {
		out = append(out, untrusted.Text(k.ID, untrusted.MaxName))
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// marketPickerOpens reports whether a bare lola market opens the picker: on a terminal (the
// Prompter can ask), with none of --json, --plain and --no-pager, and with market_picker not false
// in your config.yaml.
func (a *app) marketPickerOpens(cmd *cobra.Command, eng *engine.Engine) (bool, error) {
	if a.json || a.plain || a.noPager || !a.prompter(cmd).Interactive() {
		return false, nil
	}
	cfg, err := config.LoadConfig(eng.Paths.Config, eng.Paths.Home)
	if err != nil {
		return false, err
	}
	return cfg.MarketPicker, nil
}

// marketPick runs the picker of a bare lola market over your marketplaces and the known ones you
// have not enabled, read again, with their notices, each time it opens; it acts on each answer and
// opens again, until Esc or q leaves it. Ctrl+C in the picker or in an action's step (pickerEnded),
// or an interrupt, ends the run.
func (a *app) marketPick(cmd *cobra.Command, eng *engine.Engine) error {
	p := a.prompter(cmd)
	noticed := map[string]bool{}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	req, err := a.scopeRequest(cwd, false)
	if err != nil {
		return err
	}
	scopes, err := eng.ResolveReadScope(req)
	if err != nil {
		return err
	}
	if req.Scope != "" && len(scopes) > 1 {
		return errs.WithHints(fmt.Errorf("the market picker acts on one scope, and --scope %s names two", untrusted.Text(req.Scope, untrusted.MaxName)),
			"lola market --scope project", "lola market --scope user")
	}
	sc := scopes[0].Scope
	var preselect []string
	var resume PickerResume
	for {
		own, known, notices, err := eng.PickerRows(outputFlags(cmd))
		if err != nil {
			return err
		}
		w := cmd.ErrOrStderr()
		entries, warnings, err := eng.PickerEntries(sc, outputFlags(cmd))
		if err != nil {
			// Entries are a convenience: a lock.json lola cannot read must not lock the user out of
			// adding, updating and removing marketplaces, so the picker opens without them.
			entries, warnings = nil, nil
			text := untrusted.CleanErr(err).Error()
			if !noticed[text] {
				noticed[text] = true
				fmt.Fprintln(w, a.style(cmd).warn("entries are not shown: "+text))
				for _, h := range errs.Hints(err) {
					fmt.Fprintln(w, hintLine("  ", h, a.style(cmd).width))
				}
			}
		}
		skipped, err := eng.PickerSkips(outputFlags(cmd))
		if err != nil {
			skipped = nil // read from the same groups as entries: those that failed above already said why
		}
		for _, warning := range warnings {
			if !noticed[warning] {
				noticed[warning] = true
				fmt.Fprintln(w, a.style(cmd).warn(warning))
			}
		}
		for _, n := range notices {
			if !noticed[n.Text] {
				noticed[n.Text] = true
				fmt.Fprintln(w, noticeLine(style{color: a.color(w), bold: a.bold(w), width: a.style(cmd).width}, n))
			}
		}
		ans, err := p.MarketPicker(MarketPickerStep{Own: own, Known: known, PreselectOwn: preselect,
			Check: eng.CheckMarkets, Entries: entries, Skipped: skipped, Context: contextLine(eng.Context(sc, nil)), ScopeKind: sc.Kind.String(), Resume: resume, OpenURL: openBrowser,
			Preview: func(ctx context.Context, id string) (engine.KnownPreview, error) {
				return eng.PreviewKnown(ctx, id, sc, outputFlags(cmd))
			}})
		if err != nil {
			return err
		}
		preselect, resume = nil, PickerResume{}
		switch ans.Action {
		case PickerCancel:
			return nil
		case PickerAdd:
			preselect, err = a.marketPickAdd(cmd, eng, p, own)
			err = a.pickerEnded(cmd, err)
		case PickerUpdate:
			err = a.marketPickUpdate(cmd, eng, own, ans)
		case PickerEnter:
			err = a.marketPickEnter(cmd, eng, p, ans)
		case PickerInstall, PickerUninstall:
			err = a.pickerEnded(cmd, a.marketEntry(cmd, sc, ans, entries))
			resume = PickerResume{Market: ans.Market, Entry: ans.Entry, OnEntries: ans.FromEntries}
		}
		if err != nil {
			return err
		}
	}
}

// pickerEnded is pickerSaid for an action whose questions are never a yes/no one: a cancel there is
// Ctrl+C (or q), which ends the run as Ctrl+C in the picker does, while Cancel (ErrCancelled) and
// Esc go back to the picker.
func (a *app) pickerEnded(cmd *cobra.Command, err error) error {
	if errors.Is(err, ErrAborted) && !errors.Is(err, ErrCancelled) {
		return err
	}
	return a.pickerSaid(cmd, err)
}

// pickerSaid prints how an action the picker ran ended, before the picker opens again: a cancel's
// line for a yes/no question declined within it (a.confirm cannot tell a No from Ctrl+C, so both go
// back to the picker), or the error with its hints; nothing for none or Esc. Once the run is
// interrupted it prints nothing and returns err (else the interrupt), which ends the picker.
func (a *app) pickerSaid(cmd *cobra.Command, err error) error {
	if cerr := cmd.Context().Err(); cerr != nil {
		return cmp.Or(err, cerr)
	}
	w := cmd.ErrOrStderr()
	switch {
	case err == nil, errors.Is(err, ErrBack):
	case errors.Is(err, ErrAborted):
		msg := (&quitError{}).Error()
		if ae, ok := errors.AsType[*abortedError](err); ok {
			msg = ae.Error()
		}
		fmt.Fprintln(w, style{color: a.color(w)}.bad(msg))
	default:
		writeError(w, err, false, a.style(cmd).width)
	}
	return nil
}

// marketPickAdd runs the picker's Add a source…: it asks the source and ref, adds them as lola
// market add does, with its questions (another name, ssh hosts), and answers the names of the
// marketplaces it added, which the picker opens ticked. An empty source adds nothing.
func (a *app) marketPickAdd(cmd *cobra.Command, eng *engine.Engine, p Prompter, own []engine.PickerRow) ([]string, error) {
	src, ref, err := p.MarketSource(MarketSourceStep{})
	if err != nil || src == "" {
		return nil, err
	}
	if err := a.marketAdd(cmd, eng, engine.MarketAddRequest{Source: src, Ref: ref, Display: outputFlags(cmd)}); err != nil {
		return nil, err
	}
	names, err := eng.MarketNames()
	if err != nil {
		return nil, err
	}
	var added []string
	for _, n := range names {
		if n = untrusted.Text(n, len(n)); !slices.ContainsFunc(own, func(r engine.PickerRow) bool { return r.ID == n }) {
			added = append(added, n)
		}
	}
	return added, nil
}

// marketPickUpdate runs the update u offered on the rows of yours it names (the picker never offers
// a known one you have not enabled): one registered at a commit (AtCommit) moves past it, asking
// first (marketPastPin); the rest update as lola market update <name>... does.
func (a *app) marketPickUpdate(cmd *cobra.Command, eng *engine.Engine, own []engine.PickerRow, ans MarketPickerAnswer) error {
	var names []string
	for _, r := range own {
		switch {
		case !slices.Contains(ans.Own, r.ID):
		case r.AtCommit():
			if err := a.pickerSaid(cmd, a.marketPastPin(cmd, eng, r.ID)); err != nil {
				return err
			}
		default:
			names = append(names, r.ID)
		}
	}
	if len(names) == 0 {
		return nil
	}
	return a.pickerEnded(cmd, a.marketUpdate(cmd, eng, names, false, false, nil))
}

// marketPastPin moves name, a marketplace registered at a commit, to its remote's tip,
// asking "Update past the commit it is registered at?" over the catalog changes the move brings.
func (a *app) marketPastPin(cmd *cobra.Command, eng *engine.Engine, name string) error {
	req := engine.MarketUpdateRequest{Display: outputFlags(cmd), PickSSH: sshPicker(a.prompter(cmd))}
	res, err := eng.UpdateKnownPastPin(cmd.Context(), name, req, func(preview *engine.MarketResult) error {
		return a.confirm(cmd, []string{name}, "Update past the commit it is registered at?", Nav{}, preview,
			func(w io.Writer, s style) { printMarketResult(w, s, preview) })
	})
	if err != nil {
		return err
	}
	return a.emit(cmd, func(w io.Writer, s style) { printMarketResult(w, s, res) }, res)
}

// marketPickEnter acts on the rows ticked at Enter, the known ones first: each is enabled as lola
// market enable <id> does, asking first; then "Do what with these?" asks what to do with yours:
// browse their entries (marketBrowse), update them, or remove each, asking first as rm does.
func (a *app) marketPickEnter(cmd *cobra.Command, eng *engine.Engine, p Prompter, ans MarketPickerAnswer) error {
	for _, id := range ans.Known {
		if err := a.pickerSaid(cmd, a.marketEnable(cmd, eng, nil, id, "", false, false, nil)); err != nil {
			return err
		}
	}
	if len(ans.Own) == 0 {
		return nil
	}
	i, err := p.Where(WhereStep{Title: "Do what with these?", Options: []string{"Browse entries", "Update", "Remove"}, Nav: Nav{Back: true}})
	switch {
	case err != nil:
		return a.pickerEnded(cmd, err)
	case i == 0:
		return a.pickerEnded(cmd, a.marketBrowse(cmd, ans.Own))
	case i == 1:
		return a.pickerEnded(cmd, a.marketUpdate(cmd, eng, ans.Own, false, false, nil))
	}
	for _, n := range ans.Own {
		if err := a.pickerSaid(cmd, a.marketRemove(cmd, eng, []string{n}, false, false)); err != nil {
			return err
		}
	}
	return nil
}

// scopePin is the scope flags of a run, which marketEntry sets for an uninstall hand-off and puts back.
type scopePin struct {
	scope         string
	user, project bool
	root          string
}

// marketEntry runs lola install of the entries ans names, or lola uninstall of them, in one call as
// the command asks its own questions. An install keeps the scope flags lola market was given and
// resolves its scope as lola install does; an uninstall acts on what the picker marks installed, so
// its scope flags are set aside for the run and sc, the scope the picker reads, is the one it acts on,
// none asked. Each entry is found again in entries, read afresh; one the marketplace no longer offers
// ends the call, naming it, before anything runs.
func (a *app) marketEntry(cmd *cobra.Command, sc engine.Scope, ans MarketPickerAnswer, entries map[string][]engine.PickerEntry) error {
	name, args := "install", make([]string, 0, len(ans.Entries))
	if ans.Action == PickerUninstall {
		name = "uninstall"
	}
	for _, n := range ans.Entries {
		i := slices.IndexFunc(entries[ans.Market], func(e engine.PickerEntry) bool { return e.Name == n })
		if i < 0 {
			return errs.WithHints(fmt.Errorf("%s no longer offers %s", untrusted.Text(ans.Market, untrusted.MaxName), untrusted.Text(n, untrusted.MaxName)),
				errs.ShellJoin([]string{"lola", "market", "update", ans.Market}))
		}
		if ans.Action == PickerUninstall {
			args = append(args, n)
		} else {
			args = append(args, entries[ans.Market][i].Install)
		}
	}
	run, _, err := cmd.Root().Find([]string{name})
	if err != nil {
		return err
	}
	run.SetContext(cmd.Context())
	if ans.Action == PickerUninstall {
		was := scopePin{a.scopeFlag, a.user, a.project, a.root}
		defer func() { a.scopeFlag, a.user, a.project, a.root = was.scope, was.user, was.project, was.root }()
		a.scopeFlag, a.user, a.project, a.root = "", sc.Kind == target.User, sc.Kind == target.Project, sc.ProjectDir
	}
	return run.RunE(run, args)
}

// marketBrowse runs lola install @<name>... for names, your marketplaces: the install flow's picker
// of each one's entries, then the rest of its questions, as lola install @acme asks them.
func (a *app) marketBrowse(cmd *cobra.Command, names []string) error {
	install, _, err := cmd.Root().Find([]string{"install"})
	if err != nil {
		return err
	}
	install.SetContext(cmd.Context())
	args := make([]string, len(names))
	for i, n := range names {
		args[i] = "@" + n
	}
	return install.RunE(install, args)
}
