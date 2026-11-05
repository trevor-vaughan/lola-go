package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/market"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// marketActions are lola market's action flags, canonical names in the order a refusal names them.
var marketActions = []engine.MarketAction{engine.MarketAdd, engine.MarketUpdate, engine.MarketRemove}

// marketFlags are the flags each action takes beside the scope and output flags:
// one an action does not take, or any of them without an action, is refused, since a flag that
// does nothing must not look accepted.
var marketFlags = map[engine.MarketAction][]string{
	engine.MarketAdd:    {"ref", "path", "ssh-host", "dry-run"},
	engine.MarketUpdate: {"all", "ssh-host", "dry-run"},
	engine.MarketRemove: {"yes", "ssh-host", "dry-run"},
}

// marketAction is the one action flag set on cmd, "" for none, checked on canonical names after
// parsing; two refuse, with the run keeping each one as hints (args are its
// names). A flag the action does not take refuses, naming the actions that do, with the run
// without it as the hint.
func marketAction(cmd *cobra.Command, args []string) (engine.MarketAction, error) {
	for _, f := range []string{"enable", "disable"} {
		if cmd.Flags().Changed(f) {
			return "", errors.New("unknown flag: --" + f) // read beside the word set only (marketVerb)
		}
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
	var src, ref, path string
	var targets, sshHosts []string
	var update, remove, all, dry, yes, enable, disable bool
	cmd := &cobra.Command{
		Use: "market [<name> [<entry>] | [<name>] --add <source> [--ref <ref>] [--path <file-or-dir>] | <name>... --update | " +
			"--update --all | <name> --remove]",
		Aliases: []string{"marketplace", "markets"},
		Short:   "List, add and update the marketplaces whose catalogs you browse",
		Long: "A marketplace is a catalog of modules, each with the source it is fetched from. lola reads\n" +
			"Claude Code's .claude-plugin/marketplace.json, and lola YAML catalogs\n" +
			"(<name>.yml, marketplace.yml, lola.yml), into one model. Your marketplaces are yours alone, shared by\n" +
			"every scope: they are listed in $XDG_CONFIG_HOME/lola/markets.yaml, which you may edit, and\n" +
			"each fetched catalog is cached in $XDG_CACHE_HOME/lola/markets/<name>/; a wiped cache costs\n" +
			"a lola market --update --all.\n\n" +
			"Without a name, list your marketplaces; with one, show its source, pin, entries (by name) and\n" +
			"the entries lola skips, grouped by why, every line fitted to the terminal's width and paged\n" +
			"on a terminal; --plain and --json keep every text whole. Listing and showing a marketplace\n" +
			"never fetch and never ask. With a name and an entry, show the entry exactly as lola info\n" +
			"@<name>/<entry> does, with the same flags (a scope, -t, the output flags): it is fetched into\n" +
			"your cache, and nothing runs.\n\n" +
			"--add <source> fetches a catalog and registers it, named as you name it, else by the catalog's\n" +
			"own name. The source is a git repository (https://, ssh:// or user@host:path), an https://\n" +
			"archive, an https:// URL of the catalog file itself (.json, .yml, .yaml), or a folder: a\n" +
			"folder you are authoring is recorded by its absolute path and cached like any other, so its\n" +
			"edits reach lola only through --update. lola looks for .claude-plugin/marketplace.json, then\n" +
			"<name>.yml, <name>.yaml, marketplace.yml and marketplace.yaml, and as a last resort lola.yml and\n" +
			"lola.yaml, at the root or in the directory --path names; --path may name the catalog file\n" +
			"itself. Adding asks nothing (a catalog installs and runs nothing) but, on a terminal, another\n" +
			"name when yours is taken.\n" +
			"--update fetches the named marketplaces, or every one with --all, again from where each was\n" +
			"added and reports which entries were added, removed or changed; a failure costs only that\n" +
			"marketplace, whose cached catalog is kept.\n" +
			"--remove forgets a marketplace and drops its cached catalog; it asks first, and off a\n" +
			"terminal it needs -y. What you installed from it keeps its real source and stays installed.\n\n" +
			"lola install @<name>/<entry> installs an entry, recording its real source and pin; lola\n" +
			"install @<name> offers its entries on a terminal, and lola search searches every fetched\n" +
			"catalog. None of them fetches a catalog: --update does.\n\n" +
			"add, ls, list, rm, remove, update, set, show and search are not lola market verbs: each is\n" +
			"refused with the lola command to run instead (lola market add <name> <url> is lola market\n" +
			"<name> --add <url>, ls is lola market, rm is --remove, update is --update --all, search is\n" +
			"lola search), and none of them can name a marketplace.\n\n" +
			"A catalog is a stranger's input. lola skips, with its reason, an entry it cannot use and keeps\n" +
			"the rest: npm and command sources, ssh sources unless you allowed their host (--ssh-host),\n" +
			"any source that is not https:// at a host given by name, entries that declare their\n" +
			"hooks, MCP or LSP servers or other components in the catalog (a relative entry may list the\n" +
			"skills, commands and agents of the marketplace's own tree that form it; an entry of another\n" +
			"repository may not), or carry headers or a headersHelper,\n" +
			"relative entries outside the marketplace or missing from it, malformed pins, and two entries\n" +
			"of one name. No catalog field ever runs a command, and a YAML catalog's install hooks are\n" +
			"never run. A catalog over 8 MiB, or with control or invisible characters, is refused whole.\n" +
			"--ssh-host <host> (repeatable, with --add or --update) lets the catalog's ssh sources at that\n" +
			"host use your ssh keys and agent; on a terminal lola asks about the hosts a catalog names;\n" +
			"with --remove it stops allowing the host.\n\n" +
			"Scope flags change nothing but what lola market <name> <entry> plans for: marketplaces belong\n" +
			"to you, not to a scope.",
		Example: "  lola market\n  lola market acme\n  lola market acme formatter -t claude-code\n" +
			"  lola market acme --add https://github.com/acme/plugins\n" +
			"  lola market --add https://raw.githubusercontent.com/acme/market/main/general.yml\n" +
			"  lola market acme --update\n  lola market acme --update --ssh-host git.example.com\n" +
			"  lola market --update --all\n  lola market acme --remove -y",
		GroupID:           groupCore,
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: a.completeMarket,
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := a.engine()
			if err != nil {
				return err
			}
			if len(args) > 0 && slices.Contains(market.Verbs(), args[0]) {
				return marketVerb(cmd, eng, args, ref, path, sshHosts, dry, yes, all, enable, disable)
			}
			act, err := marketAction(cmd, args)
			if err != nil {
				return err
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
			}
			if len(args) == 0 {
				rows, notices, err := eng.Markets(outputFlags(cmd))
				if err != nil {
					return err
				}
				return a.emit(cmd, func(w io.Writer, s style) { printMarkets(w, s, rows, notices) },
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
	cmd.Flags().StringVar(&path, "path", "", "with --add: the catalog file, or the directory to look for it in, inside the source")
	_ = cmd.RegisterFlagCompletionFunc(string(engine.MarketAdd), completeFiles)
	_ = cmd.RegisterFlagCompletionFunc("ref", cobra.NoFileCompletions)
	_ = cmd.RegisterFlagCompletionFunc("path", completeDirs)
	cmd.Flags().BoolVar(&remove, string(engine.MarketRemove), false, "forget the named marketplace and drop its cached catalog (asks; -y)")
	cmd.Flags().StringArrayVar(&sshHosts, "ssh-host", nil,
		"with --add or --update: let the catalog's ssh sources at this host use your ssh keys (repeatable); with --remove: stop allowing it")
	_ = cmd.RegisterFlagCompletionFunc("ssh-host", cobra.NoFileCompletions)
	cmd.Flags().BoolVarP(&dry, "dry-run", "n", false, "report what the action would do, write nothing")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "with --remove: forget it without asking")
	a.addTargetFlag(cmd, &targets, "with <name> <entry>: assistants to plan for, as install's -t")
	// Read beside the reserved word set only, which is answered with the lola command; hidden.
	cmd.Flags().BoolVar(&enable, "enable", false, "with set: lola has no disabled state")
	cmd.Flags().BoolVar(&disable, "disable", false, "with set: lola has no disabled state")
	_ = cmd.Flags().MarkHidden("enable")
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
		return &abortedError{"aborted; no marketplace was added."}
	}
	if err != nil {
		return err
	}
	return a.emit(cmd, func(w io.Writer, s style) { printMarketResult(w, s, res) }, res)
}

// marketVerb answers lola market <verb>, a word lola market reserves, with the lola command:
// args[0] is the verb and the rest its operands. Beside an action flag the verb would be a
// marketplace's name, which it cannot be (market.ValidateName).
func marketVerb(cmd *cobra.Command, eng *engine.Engine, args []string, ref, path string, sshHosts []string, dry, yes, all, enable, disable bool) error {
	for _, act := range marketActions {
		if cmd.Flags().Changed(string(act)) {
			return market.ValidateName(args[0])
		}
	}
	return eng.CoachMarketVerb(engine.MarketVerb{Verb: args[0], Operands: args[1:], Ref: ref, Path: path, DryRun: dry, Yes: yes, All: all,
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
			return p.Confirm("Forget marketplace "+r.Name+" ("+r.Source+") and drop its cached catalog?", Nav{})
		}
	}
	res, err := eng.RemoveMarket(req)
	if errors.Is(err, engine.ErrDeclined) || errors.Is(err, ErrAborted) {
		return &abortedError{"aborted; nothing was removed."}
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
		return p.SSHHosts(SSHHostStep{Market: st.Market, Hosts: hosts})
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
		return &abortedError{"aborted; no ssh host was allowed."}
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
