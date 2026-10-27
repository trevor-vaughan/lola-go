package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/market"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// completeStored offers stored module names.
func (a *app) completeStored(_ *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	eng, err := a.engine()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	names, err := eng.Store.Names()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []cobra.Completion
	for _, n := range names {
		if strings.HasPrefix(n, toComplete) {
			out = append(out, n)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// addStoreCommands adds search and init (Core group) and the cache group.
func (a *app) addStoreCommands(root *cobra.Command) {
	root.AddCommand(a.searchCmd(), a.initCmd())
	cache := &cobra.Command{
		Use:   "cache",
		Short: "Manage your module cache",
		Long: "Your cache is lola's machine-wide copy of fetched modules ($XDG_CACHE_HOME/lola/modules).\n" +
			"Installing renders from it; removing a module here does not touch installed files.\n" +
			"With no subcommand, lists the cache.",
		Example:                    "  lola cache add ./my-module\n  lola cache list\n  lola info my-module",
		GroupID:                    groupStore,
		Args:                       cobra.ArbitraryArgs,
		ValidArgsFunction:          aliasCompletions,
		SuggestionsMinimumDistance: 2,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return a.cacheListRun(cmd, args)
			}
			return groupRunE(cmd, args)
		},
	}
	cache.AddCommand(a.cacheAddCmd(), a.cacheRmCmd(), a.cacheListCmd(), a.cacheUpdateCmd(), a.cacheSearchCmd(), a.cacheCleanCmd())
	root.AddCommand(cache)
}

func printModuleView(w io.Writer, s style, mv engine.ModuleView) {
	if s.plain {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", mv.Name, mv.Version, mv.Digest, mv.Format, mv.Source)
		return
	}
	fmt.Fprintf(w, "%s %s\n", mv.Name, mv.Version)
	if mv.Description != "" {
		fmt.Fprintf(w, "  %s\n", mv.Description)
	}
	fmt.Fprintf(w, "  %-11s %s (%s)\n", "source:", mv.Source, mv.Type)
	if mv.Path != "" {
		fmt.Fprintf(w, "  %-11s %s\n", "path:", mv.Path)
	}
	fmt.Fprintf(w, "  %-11s %s\n  %-11s %s\n", "format:", mv.Format, "digest:", mv.Digest)
	for _, h := range mv.Heads {
		ref := h.Ref
		if ref == "" {
			ref = "(default)"
		}
		fmt.Fprintf(w, "  %-11s %s → %s\n", "head:", ref, h.Digest)
	}
	vars := make([]string, len(mv.Vars)) // language = "go"
	for i, v := range mv.Vars {
		vars[i] = v.Line()
	}
	for _, kv := range []struct {
		k string
		v []string
	}{{"skills", mv.Skills}, {"commands", mv.Commands}, {"agents", mv.Agents}, {"mcp", mv.MCPServers}, {"rules", mv.Rules},
		{"vars", vars}, {"templates", mv.Templates}, {"partials", mv.Partials}, {"attributes", mv.Attributes}} {
		if len(kv.v) > 0 {
			fmt.Fprintf(w, "  %-11s %s\n", kv.k+":", strings.Join(kv.v, ", "))
		}
	}
	if len(mv.Hooks) > 0 {
		fmt.Fprintln(w, s.warn(mv.Name+" has install hooks; lola runs them only with your consent:"))
		for _, h := range mv.Hooks {
			fmt.Fprintf(w, "    %s: %s\n", h.Event, h.Script)
		}
	}
	for _, warn := range mv.Warnings {
		fmt.Fprintln(w, s.warn(warn))
	}
}

func (a *app) cacheAddCmd() *cobra.Command {
	var ref, path string
	g := argGuide{noun: "source", find: source.AcceptedForms + ", or @market/module", usage: "lola cache add <source|@market/module> [--ref ref] [--path dir]",
		examples: []string{"lola cache add ./my-module", "lola cache add ./monorepo --path plugins/review",
			"lola cache add git@github.com:acme/review.git --ref main", "lola cache add https://example.com/review-1.2.0.zip",
			"lola cache add @acme/formatter"}}
	cmd := &cobra.Command{
		Use:     "add <source|@market/module>",
		Aliases: []string{"get"},
		Short:   "Fetch a module into your cache without installing it",
		Long: "Fetch a module into your cache without installing it. Sources:\n" + source.AcceptedForms + ".\n" +
			"--ref picks a git branch, tag or full commit SHA; --path picks the module root inside\n" +
			"the source. @market/module fetches an entry of one of your marketplaces as lola install\n" +
			"would, its commit pin checked against its ref (--path is refused: the catalog gives it).\n" +
			"lola cache update fetches every cached module from its source again.",
		Example:           g.example(),
		Args:              cobra.MatchAll(g.args(1), cobra.MaximumNArgs(1)),
		ValidArgsFunction: a.completeAddress,
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := a.engine()
			if err != nil {
				return err
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			addr, isAddr, err := market.ParseAddress(args[0])
			if err != nil {
				return err
			}
			if isAddr && path != "" {
				return errs.WithHints(fmt.Errorf("--path does not apply to %s: its catalog gives the path", addr), runWithout(cmd, args, "path"))
			}
			var v store.Version
			if isAddr {
				v, err = eng.AddEntry(cmd.Context(), args[0], ref, path, outputFlags(cmd))
			} else {
				v, err = eng.AddSource(cmd.Context(), cwd, args[0], ref, path, outputFlags(cmd))
			}
			if err != nil {
				return tagRefHint(cmd, args, err)
			}
			o, _, err := eng.Store.Origin(v.Name)
			if err != nil {
				return err
			}
			mv := engine.NewModuleView(v, o)
			return a.emit(cmd, func(w io.Writer, s style) {
				printModuleView(w, s, mv)
				if !s.plain {
					fmt.Fprintln(w, s.ok("cached "+mv.Name+"; install it with: "+errs.ShellJoin([]string{"lola", "install", mv.Name})))
				}
			}, mv)
		},
	}
	cmd.Flags().StringVar(&ref, "ref", "", "git branch, tag or full commit SHA (git sources)")
	cmd.Flags().StringVar(&path, "path", "", "module root inside the source (monorepos)")
	return cmd
}

func (a *app) cacheRmCmd() *cobra.Command {
	var yes bool
	g := argGuide{noun: "module", find: "lola cache list", usage: "lola cache rm <module>... [-y]",
		examples: []string{"lola cache rm my-module -y"}}
	cmd := &cobra.Command{
		Use:               "rm <module>...",
		Aliases:           []string{"remove", "uninstall", "erase", "delete", "del"},
		Short:             "Remove modules (every cached version) from your cache",
		Long:              "Remove every cached version of the modules. Installed modules keep working: each one runs\nfrom its own module copy, and lola render fetches the source again when it needs to.",
		Example:           g.example(),
		Args:              g.args(1),
		ValidArgsFunction: a.completeStored,
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := a.engine()
			if err != nil {
				return err
			}
			for _, n := range args {
				// Presence only: an entry whose index or versions lola refuses must stay removable.
				if found, err := eng.Store.Has(n); err != nil {
					return err
				} else if !found {
					return engine.NotStored(n, outputFlags(cmd)...)
				}
			}
			if !yes {
				show := func(w io.Writer) { fmt.Fprintf(w, "will remove from your cache: %s\n", strings.Join(args, ", ")) }
				plan := map[string]any{"would_remove": args}
				if err := a.confirm(cmd, args, "Remove "+strings.Join(args, ", ")+" from your cache?", plan, show); err != nil {
					return err
				}
			}
			for _, n := range args {
				if _, err := eng.Store.Remove(n); err != nil {
					return err
				}
			}
			return a.emit(cmd, func(w io.Writer, s style) {
				for _, n := range args {
					if s.plain {
						fmt.Fprintln(w, n)
					} else {
						fmt.Fprintln(w, s.ok("removed "+n+" from your cache"))
					}
				}
			}, map[string]any{"removed": args})
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

func (a *app) cacheCleanCmd() *cobra.Command {
	var unusedFor string
	var all, dry, yes bool
	cmd := &cobra.Command{
		Use:   "clean",
		Short: "Remove what your cache no longer needs",
		Long: "Remove the cached versions no head points to (superseded by a newer fetch of their ref) and\n" +
			"the scratch of lola runs that no longer run. --unused-for 30d (or 2w, weeks of 7 days) also\n" +
			"removes every version no run used for that long, current ones included, and a module whose\n" +
			"last version goes; cache_unused_for: in config.yaml sets that age for every clean, and\n" +
			"--unused-for never turns it off for one run. --all removes every module, marketplace\n" +
			"catalog and scratch but what a running lola holds. Installed modules keep working: a project\n" +
			"whose version was removed fetches it again on its next render, and a marketplace whose\n" +
			"catalog was removed needs lola market <name> --update before you install from it.",
		Example: "  lola cache clean\n  lola cache clean --unused-for 30d -n\n  lola cache clean --all -y",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cmd.Flags().Changed("unused-for") && unusedFor == "" {
				_, err := engine.ParseUnusedFor(unusedFor) // given empty, it must not read as not given
				return err
			}
			eng, err := a.engine()
			if err != nil {
				return err
			}
			plan, err := eng.PlanClean(engine.CleanRequest{UnusedFor: unusedFor, All: all})
			if err != nil {
				return err
			}
			if dry {
				return a.emit(cmd, func(w io.Writer, s style) { printClean(w, s, plan, false) }, plan)
			}
			if len(plan.Removed) > 0 && !yes {
				show := func(w io.Writer) { printClean(w, style{}, plan, false) }
				if err := a.confirm(cmd, nil, "Remove these from your cache?", plan, show); err != nil {
					return err
				}
			}
			res, err := eng.ApplyClean(plan)
			if err != nil {
				return err
			}
			planned := len(plan.Removed) > 0
			return a.emit(cmd, func(w io.Writer, s style) { printClean(w, s, res, planned) }, res)
		},
	}
	cmd.Flags().StringVar(&unusedFor, "unused-for", "", "also remove versions no run used for this long: <n>d, <n>w or never")
	cmd.Flags().BoolVar(&all, "all", false, "remove every module, catalog and scratch a running lola does not hold")
	cmd.MarkFlagsMutuallyExclusive("all", "unused-for")
	cmd.Flags().BoolVarP(&dry, "dry-run", "n", false, "print what would be removed and remove nothing")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

// printClean prints a clean's plan (dry run) or its result: each item removed, or that would be,
// with its reason and size, the total, and each item kept and why; --plain rows are action
// (remove, removed or kept), reason, path and bytes. planned is set for a result whose plan
// listed items to remove, so a result that removed none of them says so.
func printClean(w io.Writer, s style, res *engine.CleanResult, planned bool) {
	act := "removed"
	if res.DryRun {
		act = "remove"
	}
	if s.plain {
		for _, it := range res.Removed {
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\n", act, it.Reason, untrusted.Text(it.Path, len(it.Path)), it.Bytes)
		}
		for _, k := range res.Kept {
			fmt.Fprintf(w, "kept\t%s\t%s\t0\n", k.Reason, untrusted.Text(k.Path, len(k.Path)))
		}
		for _, n := range res.Notices {
			fmt.Fprintln(w, noticeLine(s, n))
		}
		return
	}
	for _, k := range res.Kept {
		fmt.Fprintln(w, s.info(fmt.Sprintf("kept %s (%s)", untrusted.Text(k.Path, len(k.Path)), k.Reason)))
	}
	for _, n := range res.Notices {
		fmt.Fprintln(w, noticeLine(s, n))
	}
	if len(res.Removed) == 0 && planned {
		fmt.Fprintln(w, s.info("nothing removed"))
		return
	}
	if len(res.Removed) == 0 {
		fmt.Fprintln(w, s.ok("nothing to clean"))
		return
	}
	if res.DryRun {
		fmt.Fprintln(w, "will remove from your cache:")
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, it := range res.Removed {
		line := fmt.Sprintf("    %s\t%s\t%s", untrusted.Text(it.Path, len(it.Path)), it.Reason, sizeText(it.Bytes))
		if !res.DryRun {
			line = s.ok("removed " + line[4:])
		}
		fmt.Fprintln(tw, line)
	}
	_ = tw.Flush()
	if res.DryRun {
		fmt.Fprintf(w, "  total: %s\n", sizeText(res.BytesFreed))
		return
	}
	fmt.Fprintln(w, s.ok("freed "+sizeText(res.BytesFreed)+" of your cache"))
}

func (a *app) cacheListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List modules in your cache",
		Example: "  lola cache list\n  lola cache list --json",
		Args:    cobra.NoArgs,
		RunE:    a.cacheListRun,
	}
}

// cacheListRun lists the store; it backs both "cache list" and a bare "cache".
func (a *app) cacheListRun(cmd *cobra.Command, _ []string) error {
	eng, err := a.engine()
	if err != nil {
		return err
	}
	cached, warnings, err := eng.CachedModules()
	if err != nil {
		return err
	}
	views := []engine.ModuleView{}
	for _, c := range cached {
		views = append(views, engine.NewModuleView(c.Version, c.Origin))
	}
	return a.emit(cmd, func(w io.Writer, s style) {
		printSkipped(cmd.ErrOrStderr(), s, warnings)
		if s.plain {
			for _, mv := range views {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", mv.Name, mv.Version, mv.Type, mv.Source)
			}
			return
		}
		if len(views) == 0 {
			fmt.Fprintln(w, "your cache is empty (add one: lola cache add <source>)")
			return
		}
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "MODULE\tVERSION\tHEADS\tSOURCE")
		for _, mv := range views {
			fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", mv.Name, mv.Version, len(mv.Heads), mv.Source)
		}
		_ = tw.Flush()
	}, map[string]any{"modules": views, "warnings": warnings})
}

func (a *app) cacheUpdateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update [module...]",
		Short: "Fetch cached modules again from their recorded sources",
		Long: "Re-fetch every ref of the modules (all modules if none are named). Installed modules keep the\n" +
			"versions they were installed at; lola install <module> installs the new version.",
		Example:           "  lola cache update\n  lola cache update my-module",
		ValidArgsFunction: a.completeStored,
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := a.engine()
			if err != nil {
				return err
			}
			up, err := eng.Update(cmd.Context(), args, outputFlags(cmd)...)
			if err != nil {
				return err
			}
			return a.emit(cmd, func(w io.Writer, s style) {
				for _, u := range up.Updates {
					ref := u.Ref
					if ref == "" {
						ref = "(default)"
					}
					switch {
					case s.plain:
						fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", u.Name, u.Ref, u.From, u.To)
					case u.Changed:
						fmt.Fprintln(w, s.ok(fmt.Sprintf("%s %s: %s → %s", u.Name, ref, u.From, u.To)))
					default:
						fmt.Fprintln(w, s.info(fmt.Sprintf("%s %s: unchanged", u.Name, ref)))
					}
				}
				for _, n := range up.Notices {
					fmt.Fprintln(w, noticeLine(s, n))
				}
			}, up)
		},
	}
}

// searchRun runs a search of your cache, and with markets of your fetched catalogs too (Plan 6c
// §6.8): lola search searches both, lola cache search the cache alone.
func (a *app) searchRun(markets bool) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		return a.search(cmd, args, markets)
	}
}

func (a *app) search(cmd *cobra.Command, args []string, markets bool) error {
	eng, err := a.engine()
	if err != nil {
		return err
	}
	hits, warnings, err := eng.Search(strings.Join(args, " "), markets, outputFlags(cmd))
	if err != nil {
		return err
	}
	return a.emit(cmd, func(w io.Writer, s style) {
		printSkipped(cmd.ErrOrStderr(), s, warnings)
		printHits(w, s, hits)
	}, map[string]any{"results": hits, "warnings": warnings})
}

// printSkipped reports cache entries that could not be loaded; JSON output carries them in "warnings".
func printSkipped(w io.Writer, s style, warnings []string) {
	for _, warning := range warnings {
		fmt.Fprintln(w, s.warn(warning))
	}
}

// printHits prints a search's hits, each by what lola install takes to install it (a cached
// module's name, a catalog entry's @market/module); --plain rows are name, version, where,
// description and that install value.
func printHits(w io.Writer, s style, hits []engine.SearchHit) {
	if s.plain {
		for _, h := range hits {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", h.Name, h.Version, h.Where, h.Description, h.Install)
		}
		return
	}
	if len(hits) == 0 {
		fmt.Fprintln(w, "no matches")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, h := range hits {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", h.Install, h.Version, h.Where, h.Description)
	}
	_ = tw.Flush()
}

func (a *app) cacheSearchCmd() *cobra.Command {
	g := argGuide{noun: "query", find: "any word from a module's name, description or keywords", usage: "lola cache search <query>",
		examples: []string{"lola cache search review"}}
	return &cobra.Command{
		Use:     "search <query>",
		Aliases: []string{"find"},
		Short:   "Search your cache",
		Example: g.example(),
		Args:    g.args(1),
		RunE:    a.searchRun(false),
	}
}

func (a *app) searchCmd() *cobra.Command {
	g := argGuide{noun: "query", find: "any word from a module's name, description or keywords", usage: "lola search <query>",
		examples: []string{"lola search review"}}
	return &cobra.Command{
		Use:     "search <query>",
		Aliases: []string{"find"},
		Short:   "Search for modules by name, description or keyword",
		Long: "Search modules case-insensitively by name, description and keywords: your cache first,\n" +
			"then the entries of every marketplace catalog you have fetched (lola market), each listed\n" +
			"by what lola install takes: a cached module's name, or @market/module. It never fetches;\n" +
			"lola market <name> --update fetches a catalog again. lola cache search searches your cache\n" +
			"alone.",
		Example: g.example(),
		GroupID: groupCore,
		Args:    g.args(1),
		RunE:    a.searchRun(true),
	}
}

func (a *app) initCmd() *cobra.Command {
	var format string
	var yes bool
	cmd := &cobra.Command{
		Use:   "init [dir]",
		Short: "Create a new module in a directory (default: the current one)",
		Long: "Scaffold a module named after the directory. The default format is an Agent Plugins package:\n" +
			"plugin.json, a portable skill and a dev.getlola command. --format lola writes the native layout:\n" +
			"lola.yml, one skill and one command.\n" +
			"With no dir, lola init asks before scaffolding the current directory; -y answers yes.",
		Example: "  lola init my-module\n  lola init my_module --format lola\n  lola init -y\n  lola install ./my-module -t claude-code",
		GroupID: groupCore,
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			} else if !yes {
				// A bare init scaffolds wherever you happen to be: ask first (ruling R-8-U8).
				plan, err := engine.PlanInit(dir, format)
				if err != nil {
					return err
				}
				// The name is validated; the directories above it may come from a clone or an archive.
				path := untrusted.Text(plan.Path, untrusted.MaxName)
				show := func(w io.Writer) {
					fmt.Fprintf(w, "will create module %s in %s (%s):\n", plan.Name, path, plan.Format)
					for _, f := range plan.Files {
						fmt.Fprintln(w, "    "+f)
					}
				}
				if err := a.confirm(cmd, nil, "Create module "+plan.Name+" in "+path+"?", plan, show); err != nil {
					return err
				}
			}
			s, err := engine.Init(dir, format)
			if err != nil {
				return err
			}
			return a.emit(cmd, func(w io.Writer, st style) {
				if st.plain {
					for _, f := range s.Files {
						fmt.Fprintln(w, filepath.Join(dir, f))
					}
					return
				}
				fmt.Fprintln(w, st.ok("created module "+s.Name+" in "+dir+" ("+s.Format+")"))
				for _, f := range s.Files {
					fmt.Fprintln(w, "    "+f)
				}
				fmt.Fprintln(w, st.info("try it: "+errs.ShellJoin([]string{"lola", "install", dir, "-t", "claude-code"})))
			}, s)
		},
	}
	cmd.Flags().StringVar(&format, "format", engine.InitFormats()[0], "the module format: "+strings.Join(engine.InitFormats(), " or "))
	_ = cmd.RegisterFlagCompletionFunc("format", cobra.FixedCompletions(engine.InitFormats(), cobra.ShellCompDirectiveNoFileComp))
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

// tagRefHint is err, the failure of a run of cmd with args and a --ref, hinting that run at the
// commit an annotated tag object points to when the --ref named the tag object (R-9-27, RR4).
func tagRefHint(cmd *cobra.Command, args []string, err error) error {
	t, ok := errors.AsType[*source.TagObjectError](err)
	if !ok {
		return err
	}
	return errs.WithHints(err, runWithout(cmd, args, "ref")+" --ref "+t.Commit)
}
