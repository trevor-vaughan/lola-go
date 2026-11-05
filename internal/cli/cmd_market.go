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

// marketActions are lola market's action flags, canonical names in the order a refusal names them
// (Plan 6c §3.1).
var marketActions = []engine.MarketAction{engine.MarketAdd, engine.MarketUpdate, engine.MarketRemove}

// marketFlags are the flags each action takes beside the scope and output flags (Plan 6c §3.1):
// one an action does not take, or any of them without an action, is refused, since a flag that
// does nothing must not look accepted.
var marketFlags = map[engine.MarketAction][]string{
	engine.MarketAdd:    {"ref", "path", "dry-run"},
	engine.MarketUpdate: {"all", "dry-run"},
	engine.MarketRemove: {"yes", "dry-run"},
}

// marketAction is the one action flag set on cmd, "" for none, checked on canonical names after
// parsing (Plan 6b §19 R7); two refuse, with the run keeping each one as hints (args are its
// names). A flag the action does not take refuses, naming the actions that do, with the run
// without it as the hint.
func marketAction(cmd *cobra.Command, args []string) (engine.MarketAction, error) {
	for _, f := range []string{"enable", "disable"} {
		if cmd.Flags().Changed(f) {
			return "", errors.New("unknown flag: --" + f) // the Python lola's set --enable and --disable only
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
		whys = append(whys, fmt.Sprintf("--%s applies to %s only", f, strings.Join(takes, " and ")))
	}
	return "", errs.WithHints(errors.New(strings.Join(whys, "; ")), runWithout(cmd, args, bad...))
}

// given reports whether the bool flag name is set on cmd, and true: --json=false is no --json.
func given(cmd *cobra.Command, name string) bool {
	f := cmd.Flags().Lookup(name)
	return f != nil && f.Changed && f.Value.String() == "true"
}

// outputFlags is the output flags set on cmd (--json, --plain, --no-pager), which every command a
// lola market hint names keeps, as lola profile's do (Plan 6b §12.3).
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
	var update, remove, all, dry, yes, enable, disable bool
	cmd := &cobra.Command{
		Use:     "market [<name>...] [--add <source> [--ref <ref>] [--path <file-or-dir>]|--update [--all]|--remove]",
		Aliases: []string{"marketplace", "markets"},
		Short:   "List, add and update the marketplaces whose catalogs you browse",
		Long: "A marketplace is a catalog of modules, each with the source it is fetched from. lola reads\n" +
			"Claude Code's .claude-plugin/marketplace.json, and the Python lola's YAML catalogs\n" +
			"(<name>.yml, marketplace.yml), into one model. Your marketplaces are yours alone, shared by\n" +
			"every scope: they are listed in $XDG_CONFIG_HOME/lola/markets.yaml, which you may edit, and\n" +
			"each fetched catalog is cached in $XDG_CACHE_HOME/lola/markets/<name>/; a wiped cache costs\n" +
			"a lola market --update --all.\n\n" +
			"Without a name, list your marketplaces; with one, show its source, pin, entries (by name) and\n" +
			"the entries lola skips, grouped by why, every line fitted to the terminal's width and paged\n" +
			"on a terminal; --plain and --json keep every text whole. Listing and showing never fetch and\n" +
			"never ask.\n\n" +
			"--add <source> fetches a catalog and registers it, named as you name it, else by the catalog's\n" +
			"own name. The source is a git repository (https://, ssh:// or user@host:path), an https://\n" +
			"archive, an https:// URL of the catalog file itself (.json, .yml, .yaml), or a folder: a\n" +
			"folder you are authoring is recorded by its absolute path and cached like any other, so its\n" +
			"edits reach lola only through --update. lola looks for .claude-plugin/marketplace.json, then\n" +
			"<name>.yml, <name>.yaml, marketplace.yml and marketplace.yaml, at the root or in the directory\n" +
			"--path names; --path may name the catalog file itself. Adding asks nothing (a catalog installs\n" +
			"and runs nothing) but, on a terminal, another name when yours is taken.\n" +
			"--update fetches the named marketplaces, or every one with --all, again from where each was\n" +
			"added and reports which entries were added, removed or changed; a failure costs only that\n" +
			"marketplace, whose cached catalog is kept.\n" +
			"--remove forgets a marketplace and drops its cached catalog; it asks first, and off a\n" +
			"terminal it needs -y. What you installed from it keeps its real source and stays installed.\n\n" +
			"lola install @<name>/<entry> installs an entry, recording its real source and pin; lola\n" +
			"install @<name> offers its entries on a terminal, and lola search searches every fetched\n" +
			"catalog. None of them fetches a catalog: --update does.\n\n" +
			"The Python lola's verbs are coached, not accepted: lola market add <name> <url> is\n" +
			"lola market <name> --add <url>, ls is lola market, rm is --remove, update is --update --all,\n" +
			"search is lola search; none of them can name a marketplace.\n\n" +
			"A catalog is a stranger's input. lola skips, with its reason, an entry it cannot use and keeps\n" +
			"the rest: npm and command sources, ssh sources (they would use your ssh keys on a stranger's\n" +
			"host), any source that is not https:// at a host given by name, entries that declare their\n" +
			"hooks, MCP or LSP servers or other components in the catalog (a relative entry may list the\n" +
			"skills, commands and agents of the marketplace's own tree that form it; an entry of another\n" +
			"repository may not), or carry headers or a headersHelper,\n" +
			"relative entries outside the marketplace or missing from it, malformed pins, and two entries\n" +
			"of one name. No catalog field ever runs a command, and the Python lola's install hooks are\n" +
			"never run. A catalog over 8 MiB, or with control or invisible characters, is refused whole.\n\n" +
			"Scope flags are accepted and change nothing: marketplaces belong to you, not to a scope.",
		Example: "  lola market\n  lola market acme\n  lola market acme --add https://github.com/acme/plugins\n" +
			"  lola market --add https://raw.githubusercontent.com/acme/market/main/general.yml\n" +
			"  lola market acme --update\n  lola market --update --all\n  lola market acme --remove -y",
		GroupID:           groupCore,
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: a.completeMarket,
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := a.engine()
			if err != nil {
				return err
			}
			if len(args) > 0 && slices.Contains(market.Verbs(), args[0]) {
				return marketVerb(cmd, eng, args, ref, path, dry, yes, all, enable, disable)
			}
			act, err := marketAction(cmd, args)
			if err != nil {
				return err
			}
			// Scope flags change nothing (Plan 6c D2), but one naming what cannot be is refused.
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
					Display: outputFlags(cmd)})
			case engine.MarketUpdate:
				return a.marketUpdate(cmd, eng, args, all, dry)
			case engine.MarketRemove:
				return a.marketRemove(cmd, eng, args, dry, yes)
			}
			switch len(args) {
			case 0:
				rows, notices, err := eng.Markets(outputFlags(cmd))
				if err != nil {
					return err
				}
				return a.emit(cmd, func(w io.Writer, s style) { printMarkets(w, s, rows, notices) },
					map[string]any{"markets": rows, "notices": notices})
			case 1:
				v, err := eng.ShowMarket(args[0], outputFlags(cmd))
				if err != nil {
					return err
				}
				// A catalog may list hundreds of entries: it is paged as consent diffs are.
				pg, err := a.pager(cmd, eng)
				if err != nil {
					return err
				}
				width := a.width(cmd.OutOrStdout())
				return a.emit(cmd, func(w io.Writer, s style) {
					pg.show(w, s, "", func(w io.Writer, s style, _ string) { printMarket(w, s, v, width) })
				}, v)
			}
			hints := []string{engine.MarketRun(outputFlags(cmd)...)}
			names, err := eng.MarketNames()
			if err != nil {
				return err
			}
			for _, n := range args {
				if slices.Contains(names, n) {
					hints = append(hints, engine.MarketRun(append([]string{n}, outputFlags(cmd)...)...))
				}
			}
			return errs.WithHints(errors.New("name one marketplace to show"), hints...)
		},
	}
	cmd.Flags().StringVar(&src, string(engine.MarketAdd), "", "fetch the catalog at this source and register it")
	cmd.Flags().BoolVar(&update, string(engine.MarketUpdate), false, "fetch the named marketplaces' catalogs again and report what changed")
	cmd.Flags().BoolVar(&all, "all", false, "with --update: every marketplace you have")
	cmd.Flags().StringVar(&ref, "ref", "", "with --add: the branch, tag or commit of a git source")
	cmd.Flags().StringVar(&path, "path", "", "with --add: the catalog file, or the directory to look for it in, inside the source")
	cmd.Flags().BoolVar(&remove, string(engine.MarketRemove), false, "forget the named marketplace and drop its cached catalog (asks; -y)")
	cmd.Flags().BoolVarP(&dry, "dry-run", "n", false, "report what the action would do, write nothing")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "with --remove: forget it without asking")
	// The Python lola's lola market set --enable and --disable, which are coached (Plan 6c D8).
	cmd.Flags().BoolVar(&enable, "enable", false, "the Python lola's set --enable")
	cmd.Flags().BoolVar(&disable, "disable", false, "the Python lola's set --disable")
	_ = cmd.Flags().MarkHidden("enable")
	_ = cmd.Flags().MarkHidden("disable")
	// Aliases for muscle memory (Plan 6c §3.2): lola profile and lola cache add, dnf and zypper's
	// --refresh. Additive and stable; the published table is pinned in store_test.go.
	aliasFlags(cmd, map[string][]string{
		string(engine.MarketAdd):    {"get"},
		string(engine.MarketUpdate): {"upgrade", "refresh"},
		string(engine.MarketRemove): {"rm", "delete", "del", "erase"},
	})
	return cmd
}

// marketAdd runs lola market [<name>] --add <source> (Plan 6c §3.1): one name at most; a name
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

// marketVerb coaches a Python lola lola market verb (Plan 6c D8, §3.1): args[0] is the verb and
// the rest its operands; an action flag beside it is no Python form, and the verb cannot name a
// marketplace, so that is refused alone.
func marketVerb(cmd *cobra.Command, eng *engine.Engine, args []string, ref, path string, dry, yes, all, enable, disable bool) error {
	for _, act := range marketActions {
		if cmd.Flags().Changed(string(act)) {
			return fmt.Errorf("%s cannot name a marketplace: it is how the Python lola spelled lola market %s", args[0], args[0])
		}
	}
	return eng.CoachMarketVerb(engine.MarketVerb{Verb: args[0], Operands: args[1:], Ref: ref, Path: path, DryRun: dry, Yes: yes, All: all,
		Enable: enable, Disable: disable, Display: outputFlags(cmd)})
}

// marketRemove runs lola market <name> --remove (Plan 6c §3.1): one name; it asks on a terminal,
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
			return p.Confirm("Forget marketplace " + r.Name + " (" + r.Source + ") and drop its cached catalog?")
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

// marketUpdate runs lola market <name>... --update, or --update --all (Plan 6c §3.1): names and
// --all are exclusive, and one of them is needed, each refusal coaching both forms. A marketplace
// that failed makes the run fail once the others are written.
func (a *app) marketUpdate(cmd *cobra.Command, eng *engine.Engine, args []string, all, dry bool) error {
	switch {
	case all && len(args) > 0:
		return errs.WithHints(errors.New("--all updates every marketplace; drop the names or --all"),
			runWithout(cmd, args, "all"), runWithout(cmd, nil, "all")+" --all")
	case !all && len(args) == 0:
		return errs.WithHints(errors.New("name the marketplaces to update, or update every one with --all"), runWithout(cmd, nil)+" --all")
	}
	res, err := eng.UpdateMarkets(cmd.Context(), engine.MarketUpdateRequest{Names: args, All: all, DryRun: dry, Display: outputFlags(cmd)})
	if err != nil {
		return err
	}
	failed := marketFailed(res)
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
