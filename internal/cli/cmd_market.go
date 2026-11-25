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
	"github.com/spf13/pflag"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/market"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// marketEnable and marketKnown are the action flags of the known marketplaces: --enable registers
// one at its pinned commit, --known lists them. (engine.MarketAction names the others.)
const (
	marketEnable engine.MarketAction = "enable"
	marketKnown  engine.MarketAction = "known"
)

// marketActions are lola market's action flags, canonical names in the order a refusal names them.
var marketActions = []engine.MarketAction{engine.MarketAdd, engine.MarketUpdate, engine.MarketRemove, marketEnable, marketKnown}

// marketFlags are the flags each action takes beside the scope and output flags:
// one an action does not take, or any of them without an action, is refused, since a flag that
// does nothing must not look accepted.
var marketFlags = map[engine.MarketAction][]string{
	engine.MarketAdd:    {"ref", "path", "ssh-host", "dry-run"},
	engine.MarketUpdate: {"all", "ssh-host", "dry-run"},
	engine.MarketRemove: {"yes", "ssh-host", "dry-run"},
	marketEnable:        {"yes", "ssh-host", "dry-run"},
	marketKnown:         {},
}

// marketAction is the one action flag set on cmd, "" for none, checked on canonical names after
// parsing; two refuse, with the run keeping each one as hints (args are its
// names). A flag the action does not take refuses, naming the actions that do, with the run
// without it as the hint.
func marketAction(cmd *cobra.Command, args []string) (engine.MarketAction, error) {
	if cmd.Flags().Changed("disable") {
		return "", errors.New("unknown flag: --disable") // read beside the word set only (marketVerb)
	}
	var all []string
	for _, a := range marketActions {
		for _, f := range marketFlags[a] {
			if !slices.Contains(all, f) {
				all = append(all, f)
			}
		}
	}
	// misplaced is the flags set on cmd that act does not take, which a hint drops.
	misplaced := func(act engine.MarketAction) []string {
		var out []string
		for _, f := range all {
			if cmd.Flags().Changed(f) && !slices.Contains(marketFlags[act], f) {
				out = append(out, f)
			}
		}
		return out
	}
	var set []engine.MarketAction
	for _, act := range marketActions {
		if cmd.Flags().Changed(string(act)) {
			set = append(set, act)
		}
	}
	if len(set) > 1 {
		var hints []string
		for _, keep := range set {
			drop := misplaced(keep)
			for _, other := range set {
				if other != keep {
					drop = append(drop, string(other))
				}
			}
			hints = append(hints, runWithout(cmd, args, drop...))
		}
		var names []string
		for _, a := range set {
			names = append(names, "--"+string(a))
		}
		return "", errs.WithHints(fmt.Errorf("%s and %s cannot be combined; lola market takes one action per run",
			strings.Join(names[:len(names)-1], ", "), names[len(names)-1]), hints...)
	}
	act := engine.MarketAction("")
	if len(set) == 1 {
		act = set[0]
	}
	bad := misplaced(act)
	if len(bad) == 0 {
		return act, nil
	}
	var whys []string
	for _, f := range bad {
		var takes []string
		for _, a := range marketActions {
			if slices.Contains(marketFlags[a], f) {
				takes = append(takes, "--"+string(a))
			}
		}
		list := takes[len(takes)-1]
		if len(takes) > 1 {
			list = strings.Join(takes[:len(takes)-1], ", ") + " and " + list
		}
		whys = append(whys, fmt.Sprintf("--%s applies to %s only", f, list))
	}
	return "", errs.WithHints(errors.New(strings.Join(whys, "; ")), runWithout(cmd, args, bad...))
}

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

func (a *app) marketCmd() *cobra.Command {
	var src, ref, path, enableID string
	var targets, sshHosts []string
	var update, remove, known, all, dry, yes, disable, check bool
	cmd := &cobra.Command{
		Use:     "market [<name> [<entry>]] [flags]",
		Aliases: []string{"marketplace", "markets"},
		Short:   "List, add and update the marketplaces whose catalogs you browse, and the ones lola knows",
		Long: "A marketplace is a catalog of modules, each with the source it is fetched from: Claude Code's\n" +
			".claude-plugin/marketplace.json or a lola YAML catalog. Yours are shared by every scope, listed in\n" +
			"$XDG_CONFIG_HOME/lola/markets.yaml; each fetched catalog is cached in $XDG_CACHE_HOME/lola/markets/.\n" +
			"\n" +
			"Without a name, list them; with one, show its source, pin, entries and skipped entries; with an\n" +
			"entry too, show it as lola info @<name>/<entry> does, fetching it. Listing and showing a marketplace\n" +
			"never fetch or ask; --check adds each git source's upstream status, asking each remote for its tip.\n" +
			"On a terminal a bare lola market opens a picker of yours and the known ones instead: x ticks,\n" +
			"/ filters, Enter acts, a adds, o opens a row's web page, esc or q leaves; u checks the ticked rows (or the cursor's) and U\n" +
			"every row upstream, then offers to update yours that need it. Nothing is contacted before u or U.\n" +
			"--json, --plain, --no-pager, --no-interactive and config.yaml market_picker: false list instead.\n" +
			"--add <source> (git repository, https:// archive or catalog file, or folder) registers a catalog;\n" +
			"--update fetches again (--all for all); --remove forgets one (asks; -y). lola skips an entry it\n" +
			"cannot use, with its reason, and runs nothing from a catalog. --known lists the marketplaces lola\n" +
			"ships, each pinned to a commit; --enable <id> registers one (asks; -y).",
		Example: "  lola market\n  lola market --check\n  lola market --no-interactive\n  lola market acme\n  lola market acme formatter -t claude-code\n" +
			"  lola market acme --add https://github.com/acme/plugins\n" +
			"  lola market acme --add https://github.com/acme/plugins --ref v1.2 --path catalog\n" +
			"  lola market --add https://raw.githubusercontent.com/acme/market/main/general.yml\n" +
			"  lola market acme --update\n  lola market acme beta --update\n" +
			"  lola market acme --update --ssh-host git.example.com\n" +
			"  lola market --update --all\n  lola market acme --remove -y\n" +
			"  lola market --known\n  lola market --known superpowers\n  lola market --enable superpowers\n  lola market mine --enable superpowers -y",
		GroupID:           groupCore,
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: a.completeMarket,
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := a.engine()
			if err != nil {
				return err
			}
			refuseCheck := func() error {
				return errs.WithHints(errors.New("--check lists your marketplaces' upstream status; it takes no name, entry or action"),
					runWithout(cmd, args, "check"), engine.MarketRun(append(outputFlags(cmd), "--check")...))
			}
			// --no-interactive keeps a bare lola market from opening the picker; anything else never opens it.
			refuseNoInteractive := func() error {
				return errs.WithHints(errors.New("--no-interactive applies to a bare lola market only, which it lists as text"),
					runWithout(cmd, args, "no-interactive"))
			}
			if len(args) > 0 && slices.Contains(market.Verbs(), args[0]) {
				if check {
					return refuseCheck()
				}
				if cmd.Flags().Changed("no-interactive") {
					return refuseNoInteractive()
				}
				// --enable takes the next word as its value: -y after it was meant as a flag.
				enabled, named := cmd.Flags().Changed("enable"), enableID
				if enabled && flagTaken(cmd, enableID) {
					named = ""
				}
				return marketVerb(cmd, eng, args, ref, path, enabled, named, sshHosts, dry, yes, all, disable)
			}
			act, err := marketAction(cmd, args)
			if err != nil {
				return err
			}
			if check && (act != "" || len(args) > 0) {
				return refuseCheck()
			}
			if cmd.Flags().Changed("no-interactive") && (act != "" || len(args) > 0) {
				return refuseNoInteractive()
			}
			if act == "" && len(args) > 2 {
				return tooManyEntries(cmd, eng, args)
			}
			entry := act == "" && len(args) == 2
			if cmd.Flags().Changed("target") && !entry {
				return errs.WithHints(errors.New("--target applies to lola market <name> <entry> only"), runWithout(cmd, args, "target"))
			}
			if entry {
				// The run of lola info @<name>/<entry>, which resolves its scope itself.
				return a.showInfo(cmd, "@"+args[0]+"/"+args[1])
			}
			// Scope flags change nothing, but one naming what cannot be is refused.
			if a.scopeFlag != "" || a.user || a.project || a.root != "" {
				cwd, err := os.Getwd()
				if err != nil {
					return err
				}
				req, err := a.scopeRequest(cwd, false)
				if err != nil {
					return err
				}
				if _, err := eng.ResolveReadScope(req); err != nil {
					return err
				}
			}
			switch act {
			case engine.MarketAdd:
				return a.marketAdd(cmd, eng, args, engine.MarketAddRequest{Source: src, Ref: ref, Path: path, DryRun: dry,
					Display: outputFlags(cmd), SSHHosts: sshHosts})
			case engine.MarketUpdate:
				return a.marketUpdate(cmd, eng, args, all, dry, sshHosts)
			case engine.MarketRemove:
				if len(sshHosts) > 0 {
					return a.marketRevoke(cmd, eng, args, sshHosts, dry, yes)
				}
				return a.marketRemove(cmd, eng, args, dry, yes)
			case marketEnable:
				return a.marketEnable(cmd, eng, args, enableID, dry, yes, sshHosts)
			case marketKnown:
				return a.marketKnown(cmd, eng, args)
			}
			if check {
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
			}
			if len(args) == 0 {
				open, err := a.marketPickerOpens(cmd, eng)
				if err != nil {
					return err
				}
				if open {
					return a.marketPick(cmd, eng)
				}
				rows, notices, err := eng.Markets(outputFlags(cmd))
				if err != nil {
					return err
				}
				return a.emit(cmd, func(w io.Writer, s style) { printMarkets(w, s, rows, nil, notices) },
					map[string]any{"markets": rows, "notices": notices})
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
		},
	}
	cmd.Flags().StringVar(&src, string(engine.MarketAdd), "", "fetch the catalog at this source and register it")
	cmd.Flags().BoolVar(&update, string(engine.MarketUpdate), false, "fetch the named marketplaces' catalogs again and report what changed")
	cmd.Flags().BoolVar(&all, "all", false, "with --update: every marketplace you have")
	cmd.Flags().StringVar(&ref, "ref", "", "with --add: the branch, tag or commit of a git source")
	cmd.Flags().StringVar(&path, "path", "", "with --add: the catalog file, or the directory holding it, in the source")
	_ = cmd.RegisterFlagCompletionFunc(string(engine.MarketAdd), completeFiles)
	_ = cmd.RegisterFlagCompletionFunc("ref", cobra.NoFileCompletions)
	_ = cmd.RegisterFlagCompletionFunc("path", completeDirs)
	cmd.Flags().BoolVar(&remove, string(engine.MarketRemove), false, "forget the named marketplace and drop its cached catalog (asks; -y)")
	cmd.Flags().StringArrayVar(&sshHosts, "ssh-host", nil,
		"let ssh sources at this host use your keys (repeatable; --remove: revoke)")
	_ = cmd.RegisterFlagCompletionFunc("ssh-host", cobra.NoFileCompletions)
	cmd.Flags().BoolVarP(&dry, "dry-run", "n", false, "report what the action would do, write nothing")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "with --remove or --enable: do it without asking")
	cmd.Flags().BoolVar(&check, "check", false,
		"list each git marketplace's upstream status; fetches no catalog")
	cmd.Flags().Bool("no-interactive", false,
		"list as text, never the picker (config.yaml market_picker: false too)")
	cmd.Flags().StringVar(&enableID, string(marketEnable), "",
		"register the known marketplace with this id, at its pinned commit (asks; -y)")
	cmd.Flags().BoolVar(&known, string(marketKnown), false,
		"list the marketplaces lola ships, or show one by id; enables nothing")
	_ = cmd.RegisterFlagCompletionFunc(string(marketEnable), a.completeKnown)
	a.addTargetFlag(cmd, &targets, "with <name> <entry>: assistants to plan for, as install's -t")
	// Read beside the reserved word set only, which is answered with the lola command; hidden.
	// (set --enable <name> is read through --enable's value, marketVerb.)
	// pflag refuses a trailing --enable (set acme --enable) before the verb is read: coach it as the verb.
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		if verb := c.Flags().Args(); err.Error() == "flag needs an argument: --enable" && len(verb) > 0 && slices.Contains(market.Verbs(), verb[0]) {
			eng, eerr := a.engine()
			if eerr != nil {
				return eerr
			}
			return marketVerb(c, eng, verb, ref, path, true, "", sshHosts, dry, yes, all, disable)
		}
		return c.Parent().FlagErrorFunc()(c, err)
	})
	cmd.Flags().BoolVar(&disable, "disable", false, "with set: lola has no disabled state")
	_ = cmd.Flags().MarkHidden("disable")
	// Aliases for muscle memory: lola profile and lola cache add, dnf and zypper's
	// --refresh. Additive and stable; the published table is pinned in store_test.go.
	aliasFlags(cmd, map[string][]string{
		string(engine.MarketAdd):    {"get"},
		string(engine.MarketUpdate): {"upgrade", "refresh"},
		string(engine.MarketRemove): {"rm", "delete", "del", "erase"},
	})
	return cmd
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

// marketAdd runs lola market [<name>] --add <source>: one name at most; a name
// another marketplace has, or a catalog's own name no marketplace can have, is asked again on a
// terminal, never under --json or a dry run.
func (a *app) marketAdd(cmd *cobra.Command, eng *engine.Engine, args []string, req engine.MarketAddRequest) error {
	if len(args) > 1 {
		var hints []string
		for _, n := range args {
			hints = append(hints, runWithout(cmd, []string{n}))
		}
		return errs.WithHints(errors.New("--add registers one marketplace; give it one name"), hints...)
	}
	if len(args) == 1 {
		req.Name = args[0]
	}
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

// flagTaken sets the bool flag v names (-y, --json) and reports true when v is one: --enable took
// it as its value, as in lola market set acme --enable -y, where it was a flag.
func flagTaken(cmd *cobra.Command, v string) bool {
	var f *pflag.Flag
	switch {
	case strings.HasPrefix(v, "--"):
		f = cmd.Flags().Lookup(v[2:])
	case len(v) == 2 && v[0] == '-':
		f = cmd.Flags().ShorthandLookup(v[1:])
	}
	if f == nil || f.Value.Type() != "bool" {
		return false
	}
	_ = f.Value.Set("true")
	f.Changed = true
	return true
}

// marketVerb answers lola market <verb>, a word lola market reserves, with the lola command:
// args[0] is the verb and the rest its operands. Beside an action flag the verb would be a
// marketplace's name, which it cannot be (market.ValidateName).
func marketVerb(cmd *cobra.Command, eng *engine.Engine, args []string, ref, path string, enable bool, enableID string, sshHosts []string, dry, yes, all, disable bool) error {
	for _, act := range marketActions {
		if act != marketEnable && cmd.Flags().Changed(string(act)) {
			return market.ValidateName(args[0])
		}
	}
	// set --enable <name> and set <name> --enable are the same run: the name is --enable's value, or not.
	operands := args[1:]
	if enableID != "" {
		operands = append([]string{enableID}, operands...)
	}
	return eng.CoachMarketVerb(engine.MarketVerb{Verb: args[0], Operands: operands, Ref: ref, Path: path, DryRun: dry, Yes: yes, All: all,
		Enable: enable, Disable: disable, SSHHosts: sshHosts, Display: outputFlags(cmd)})
}

// marketRemove runs lola market <name> --remove: one name; it asks on a terminal,
// -y answers, and off a terminal without -y the engine refuses.
func (a *app) marketRemove(cmd *cobra.Command, eng *engine.Engine, args []string, dry, yes bool) error {
	if len(args) != 1 {
		return errors.New("--remove forgets one marketplace; name it")
	}
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

// sshPicker asks p which of a catalog's ssh hosts to allow, for an --add's or --update's PickSSH.
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

// marketRevoke runs lola market <name> --ssh-host <host>... --remove: one
// name; it asks nothing and takes no -y, since the host can be allowed again.
func (a *app) marketRevoke(cmd *cobra.Command, eng *engine.Engine, args, hosts []string, dry, yes bool) error {
	if yes {
		return errs.WithHints(errors.New("--yes applies to --remove without --ssh-host: stopping allowing an ssh host asks nothing"),
			runWithout(cmd, args, "yes"))
	}
	if len(args) != 1 {
		// One working run per marketplace named, keeping the run's other flags.
		var hints []string
		for _, n := range args {
			if h := runWithout(cmd, []string{n}); !slices.Contains(hints, h) {
				hints = append(hints, h)
			}
		}
		return errs.WithHints(errors.New("--ssh-host with --remove stops allowing hosts for one marketplace; name it"), hints...)
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

// marketUpdate runs lola market <name>... --update, or --update --all: names and
// --all are exclusive, and one of them is needed, each refusal coaching both forms. A marketplace
// that failed makes the run fail once the others are written.
func (a *app) marketUpdate(cmd *cobra.Command, eng *engine.Engine, args []string, all, dry bool, sshHosts []string) error {
	switch {
	case all && len(args) > 0:
		return errs.WithHints(errors.New("--all updates every marketplace; drop the names or --all"),
			runWithout(cmd, args, "all"), runWithout(cmd, nil, "all")+" --all")
	case !all && len(args) == 0:
		return errs.WithHints(errors.New("name the marketplaces to update, or update every one with --all"), runWithout(cmd, nil)+" --all")
	}
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

// marketError is an --update in which a marketplace failed; under --json its document is the
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

// completeMarket offers your marketplaces' names, minus those given; none for --add or --all,
// nor a second for --remove, which takes one.
func (a *app) completeMarket(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	eng, err := a.engine()
	if err != nil || cmd.Flags().Changed("all") || cmd.Flags().Changed(string(engine.MarketAdd)) ||
		cmd.Flags().Changed(string(engine.MarketRemove)) && len(args) > 0 {
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

// knownEnabled is lola market --enable's JSON, the plan a refusal carries too: the run's result and
// the known marketplace it registers (or would).
type knownEnabled struct {
	*engine.MarketResult
	Known market.KnownMarket `json:"known"`
}

// marketEnable runs lola market [<name>] --enable <id>: one name at most, the id by default. It
// asks on a terminal with what the pinned catalog holds, -y answers, and off a terminal without -y
// it refuses with the run and -y, the plan under --json; -n previews and asks nothing. It never
// asks for another name or an ssh host: a name another marketplace holds is refused with the run
// that names a free one, and --ssh-host allows a host.
func (a *app) marketEnable(cmd *cobra.Command, eng *engine.Engine, args []string, id string, dry, yes bool, sshHosts []string) error {
	if len(args) > 1 {
		var hints []string
		for _, n := range args {
			hints = append(hints, runWithout(cmd, []string{n}))
		}
		return errs.WithHints(errors.New("--enable registers one marketplace; give it one name"), hints...)
	}
	name := id
	if len(args) == 1 {
		name = args[0]
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

// marketKnown runs lola market --known [<id>]: the marketplaces lola ships, or the one whose id
// is given. Both are the document {known, hints}, known an array (of one for an id).
func (a *app) marketKnown(cmd *cobra.Command, eng *engine.Engine, args []string) error {
	display := outputFlags(cmd)
	if len(args) > 1 {
		var hints []string
		for _, id := range args {
			hints = append(hints, runWithout(cmd, []string{id}))
		}
		return errs.WithHints(errors.New("--known shows one marketplace; give it one id"), hints...)
	}
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
			hints = append(hints, engine.MarketRun(append([]string{"--enable", e.ID}, display...)...))
		}
	} else {
		var err error
		if list, err = eng.Known(); err != nil {
			return err
		}
		if len(list) > 0 {
			hints = append(hints, engine.MarketRun(append([]string{"--known", list[0].ID}, display...)...))
		}
	}
	for i := range list {
		list[i].KnownMarket = cleanKnown(list[i].KnownMarket)
	}
	return a.emit(cmd, func(w io.Writer, s style) { printKnown(w, s, list, len(args) == 1, hints) },
		map[string]any{"known": list, "hints": hints})
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
// Prompter can ask), with none of --json, --plain, --no-pager and --no-interactive, and with
// market_picker not false in your config.yaml.
func (a *app) marketPickerOpens(cmd *cobra.Command, eng *engine.Engine) (bool, error) {
	if a.json || a.plain || a.noPager || given(cmd, "no-interactive") || !a.prompter(cmd).Interactive() {
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
// market --add does, with its questions (another name, ssh hosts), and answers the names of the
// marketplaces it added, which the picker opens ticked. An empty source adds nothing.
func (a *app) marketPickAdd(cmd *cobra.Command, eng *engine.Engine, p Prompter, own []engine.PickerRow) ([]string, error) {
	src, ref, err := p.MarketSource(MarketSourceStep{})
	if err != nil || src == "" {
		return nil, err
	}
	if err := a.marketAdd(cmd, eng, nil, engine.MarketAddRequest{Source: src, Ref: ref, Display: outputFlags(cmd)}); err != nil {
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
// first (marketPastPin); the rest update as lola market <name>... --update does.
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
// market --enable <id> does, asking first; then "Do what with these?" asks what to do with yours:
// browse their entries (marketBrowse), update them, or remove each, asking first as --remove does.
func (a *app) marketPickEnter(cmd *cobra.Command, eng *engine.Engine, p Prompter, ans MarketPickerAnswer) error {
	for _, id := range ans.Known {
		if err := a.pickerSaid(cmd, a.marketEnable(cmd, eng, nil, id, false, false, nil)); err != nil {
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
				errs.ShellJoin([]string{"lola", "market", ans.Market, "--update"}))
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
