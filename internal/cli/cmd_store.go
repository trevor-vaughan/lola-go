package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/store"
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

// addStoreCommands adds search and init (Core group), the cache group and the mod
// compatibility group.
func (a *app) addStoreCommands(root *cobra.Command) {
	root.AddCommand(a.searchCmd(), a.initCmd())
	cache := &cobra.Command{
		Use:   "cache",
		Short: "Manage your module cache",
		Long: "Your cache is lola's machine-wide copy of fetched modules ($XDG_DATA_HOME/lola/modules).\n" +
			"Installing renders from it; removing a module here does not touch installed files.\n" +
			"With no subcommand, lists the cache.",
		Example:                    "  lola cache add ./my-module\n  lola cache list\n  lola cache info my-module",
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
	cache.AddCommand(a.cacheAddCmd(), a.cacheRmCmd(), a.cacheListCmd(), a.cacheInfoCmd(), a.cacheUpdateCmd(), a.cacheSearchCmd())
	root.AddCommand(cache)

	initCmd := a.initCmd()
	initCmd.GroupID = ""
	mod := &cobra.Command{
		Use:                        "mod",
		Short:                      "Compatibility commands: forward to lola cache and lola init",
		Example:                    "  lola mod add ./my-module\n  lola mod ls\n  lola mod init my-module",
		GroupID:                    groupCompat,
		Args:                       cobra.ArbitraryArgs,
		ValidArgsFunction:          aliasCompletions,
		RunE:                       groupRunE,
		SuggestionsMinimumDistance: 2,
	}
	mod.AddCommand(a.cacheAddCmd(), a.cacheRmCmd(), a.cacheListCmd(), a.cacheInfoCmd(), a.cacheUpdateCmd(), a.cacheSearchCmd(), initCmd)
	root.AddCommand(mod)
}

// moduleView is a stored version as shown by cache add and cache info.
type moduleView struct {
	Name         string        `json:"name"`
	Description  string        `json:"description"`
	Version      string        `json:"version"`
	Digest       string        `json:"digest"`
	Format       string        `json:"format"`
	Source       string        `json:"source"`
	Type         string        `json:"type"`
	Path         string        `json:"path,omitempty"`
	Heads        []headView    `json:"heads"`
	Skills       []string      `json:"skills"`
	Commands     []string      `json:"commands"`
	Agents       []string      `json:"agents"`
	MCPServers   []string      `json:"mcp_servers"`
	Instructions bool          `json:"instructions"`
	Hooks        []module.Hook `json:"hooks"`
	Warnings     []string      `json:"warnings"`
}

type headView struct {
	Ref    string `json:"ref"`
	Digest string `json:"digest"`
}

func newModuleView(v store.Version, o store.Origin) moduleView {
	m := v.Module
	mv := moduleView{Name: m.Name, Description: m.Description, Version: m.Version, Digest: v.Meta.Digest, Format: o.Format,
		Source: o.Source, Type: o.Type, Path: o.Path, Heads: []headView{}, Skills: []string{}, Commands: []string{}, Agents: []string{},
		MCPServers: []string{}, Instructions: m.Instructions != "", Hooks: []module.Hook{}, Warnings: []string{}}
	for _, h := range o.Heads {
		mv.Heads = append(mv.Heads, headView{Ref: h.Ref, Digest: h.Digest})
	}
	for _, s := range m.Skills {
		mv.Skills = append(mv.Skills, s.Name)
	}
	for _, c := range m.Commands {
		mv.Commands = append(mv.Commands, c.Name)
	}
	for _, ag := range m.Agents {
		mv.Agents = append(mv.Agents, ag.Name)
	}
	for _, s := range m.MCPServers {
		mv.MCPServers = append(mv.MCPServers, s.Name)
	}
	mv.Hooks = append(mv.Hooks, m.DeclaredHooks...)
	mv.Warnings = append(mv.Warnings, m.Warnings...)
	if m.LegacyAgentsMD {
		mv.Warnings = append(mv.Warnings, module.LegacyAgentsMDWarning)
	}
	return mv
}

func printModuleView(w io.Writer, s style, mv moduleView) {
	if s.plain {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", mv.Name, mv.Version, mv.Digest, mv.Format, mv.Source)
		return
	}
	fmt.Fprintf(w, "%s %s\n", mv.Name, mv.Version)
	if mv.Description != "" {
		fmt.Fprintf(w, "  %s\n", mv.Description)
	}
	fmt.Fprintf(w, "  source:   %s (%s)\n", mv.Source, mv.Type)
	if mv.Path != "" {
		fmt.Fprintf(w, "  path:     %s\n", mv.Path)
	}
	fmt.Fprintf(w, "  format:   %s\n  digest:   %s\n", mv.Format, mv.Digest)
	for _, h := range mv.Heads {
		ref := h.Ref
		if ref == "" {
			ref = "(default)"
		}
		fmt.Fprintf(w, "  head:     %s → %s\n", ref, h.Digest)
	}
	for _, kv := range []struct {
		k string
		v []string
	}{{"skills", mv.Skills}, {"commands", mv.Commands}, {"agents", mv.Agents}, {"mcp", mv.MCPServers}} {
		if len(kv.v) > 0 {
			fmt.Fprintf(w, "  %-9s %s\n", kv.k+":", strings.Join(kv.v, ", "))
		}
	}
	if mv.Instructions {
		fmt.Fprintln(w, "  instructions: yes")
	}
	if len(mv.Hooks) > 0 {
		fmt.Fprintln(w, s.warn(mv.Name+" declares install hooks that lola does not run:"))
		for _, h := range mv.Hooks {
			fmt.Fprintf(w, "    %s: %s\n", h.Event, h.Command)
		}
		fmt.Fprintln(w, "  Review the script and run it yourself if you trust it.")
	}
	for _, warn := range mv.Warnings {
		fmt.Fprintln(w, s.warn(warn))
	}
}

func (a *app) cacheAddCmd() *cobra.Command {
	var ref, path string
	g := argGuide{noun: "source", find: "a module directory (" + source.AcceptedForms + ")", usage: "lola cache add <source> [--ref ref] [--path dir]",
		examples: []string{"lola cache add ./my-module", "lola cache add ./monorepo --path plugins/review"}}
	cmd := &cobra.Command{
		Use:     "add <source>",
		Aliases: []string{"get"},
		Short:   "Fetch a module into your cache without installing it",
		Example: g.example(),
		Args:    cobra.MatchAll(g.args(1), cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := a.engine()
			if err != nil {
				return err
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			spec, ok, err := source.Parse(args[0], cwd)
			if err != nil {
				return err
			}
			if !ok {
				return errs.WithHints(fmt.Errorf("%q is not a source", args[0]), "sources are "+source.AcceptedForms)
			}
			spec.Ref, spec.Path = ref, path
			v, err := eng.Fetch(cmd.Context(), spec)
			if err != nil {
				return err
			}
			o, _, err := eng.Store.Origin(v.Name)
			if err != nil {
				return err
			}
			mv := newModuleView(v, o)
			return a.emit(cmd, func(w io.Writer, s style) {
				printModuleView(w, s, mv)
				if !s.plain {
					fmt.Fprintln(w, s.ok("cached "+mv.Name+"; install it with: lola install "+mv.Name))
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
		Long:              "Remove every cached version of the modules. Installed files stay, but MCP servers that\nrun from the cache (${PLUGIN_ROOT}) stop working until you install again.",
		Example:           g.example(),
		Args:              g.args(1),
		ValidArgsFunction: a.completeStored,
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := a.engine()
			if err != nil {
				return err
			}
			for _, n := range args {
				// Presence only: an unloadable entry must stay removable.
				if _, found, err := eng.Store.Origin(n); err != nil {
					return err
				} else if !found {
					return engine.NotStored(n)
				}
			}
			if !yes {
				fmt.Fprintf(cmd.ErrOrStderr(), "will remove from your cache: %s\n", strings.Join(args, ", "))
				fmt.Fprintln(cmd.ErrOrStderr(), "installed files stay; MCP servers that run from the cache will stop working.")
				if err := confirm(cmd, false, "Remove "+strings.Join(args, ", ")+" from your cache?"); err != nil {
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
	views := []moduleView{}
	for _, c := range cached {
		views = append(views, newModuleView(c.Version, c.Origin))
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

func (a *app) cacheInfoCmd() *cobra.Command {
	g := argGuide{noun: "module", find: "lola cache list", usage: "lola cache info <module>", examples: []string{"lola cache info my-module"}}
	return &cobra.Command{
		Use:               "info <module>",
		Aliases:           []string{"show", "view"},
		Short:             "Show a cached module's metadata, source, artifacts and refused hooks",
		Example:           g.example(),
		Args:              cobra.MatchAll(g.args(1), cobra.MaximumNArgs(1)),
		ValidArgsFunction: a.completeStored,
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := a.engine()
			if err != nil {
				return err
			}
			v, o, err := eng.Current(args[0])
			if err != nil {
				return err
			}
			mv := newModuleView(v, o)
			return a.emit(cmd, func(w io.Writer, s style) { printModuleView(w, s, mv) }, mv)
		},
	}
}

func (a *app) cacheUpdateCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "update [module...]",
		Short:             "Fetch cached modules again from their recorded sources",
		Long:              "Re-fetch every ref of the modules (all modules if none are named). Projects pick up the\nnew versions with lola render; lola list shows them as stale until then.",
		Example:           "  lola cache update\n  lola cache update my-module",
		ValidArgsFunction: a.completeStored,
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := a.engine()
			if err != nil {
				return err
			}
			ups, err := eng.Update(cmd.Context(), args)
			if err != nil {
				return err
			}
			return a.emit(cmd, func(w io.Writer, s style) {
				for _, u := range ups {
					ref := u.Ref
					if ref == "" {
						ref = "(default)"
					}
					switch {
					case s.plain:
						fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", u.Name, u.Ref, u.From, u.To)
					case u.Changed:
						fmt.Fprintln(w, s.ok(fmt.Sprintf("%s %s: %s → %s (run lola render to apply)", u.Name, ref, u.From, u.To)))
					default:
						fmt.Fprintln(w, s.info(fmt.Sprintf("%s %s: unchanged", u.Name, ref)))
					}
				}
			}, map[string]any{"updates": ups})
		},
	}
}

func (a *app) searchRun(cmd *cobra.Command, args []string) error {
	eng, err := a.engine()
	if err != nil {
		return err
	}
	hits, warnings, err := eng.Search(strings.Join(args, " "))
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

func printHits(w io.Writer, s style, hits []engine.SearchHit) {
	if s.plain {
		for _, h := range hits {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", h.Name, h.Version, h.Where, h.Description)
		}
		return
	}
	if len(hits) == 0 {
		fmt.Fprintln(w, "no matches")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, h := range hits {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", h.Name, h.Version, h.Where, h.Description)
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
		RunE:    a.searchRun,
	}
}

func (a *app) searchCmd() *cobra.Command {
	g := argGuide{noun: "query", find: "any word from a module's name, description or keywords", usage: "lola search <query>",
		examples: []string{"lola search review"}}
	return &cobra.Command{
		Use:     "search <query>",
		Aliases: []string{"find"},
		Short:   "Search for modules by name, description or keyword",
		Long:    "Search modules case-insensitively by name, description and keywords. This build searches\nyour cache; marketplaces are on the roadmap.",
		Example: g.example(),
		GroupID: groupCore,
		Args:    g.args(1),
		RunE:    a.searchRun,
	}
}

// scaffold is the module `lola init` writes, relative to the module root.
var scaffold = []struct{ path, body string }{
	{"lola.yml", "name: %s\ndescription: Describe what this module does\nversion: 0.1.0\n"},
	{"skills/hello/SKILL.md", "---\ndescription: Greet the user. Replace this skill with your own.\n---\nGreet the user by name and offer help.\n"},
	{"commands/hello.md", "---\ndescription: Say hello\n---\nSay hello to $ARGUMENTS.\n"},
}

func (a *app) initCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "init [dir]",
		Short:   "Create a new module in a directory (default: the current one)",
		Long:    "Scaffold a native-layout module: lola.yml, one skill and one command. The module is named\nafter the directory, which must be a valid module name.",
		Example: "  lola init my-module\n  lola install ./my-module -t claude-code",
		GroupID: groupCore,
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			abs, err := filepath.Abs(dir)
			if err != nil {
				return err
			}
			name := filepath.Base(abs)
			if err := module.ValidateName("module", name); err != nil {
				return errs.WithHints(err, "run lola init in a directory whose name is a valid module name")
			}
			for _, f := range []string{"lola.yml", "lola.yaml"} {
				if _, err := os.Stat(filepath.Join(abs, f)); err == nil {
					return fmt.Errorf("%s is already a module (it has %s)", dir, f)
				}
			}
			for _, f := range scaffold {
				if _, err := os.Lstat(filepath.Join(abs, f.path)); err == nil {
					return fmt.Errorf("%s already exists; lola init never overwrites files", filepath.Join(abs, f.path))
				} else if !errors.Is(err, fs.ErrNotExist) {
					return err
				}
			}
			files := []string{}
			for _, f := range scaffold {
				body := f.body
				if f.path == "lola.yml" {
					body = fmt.Sprintf(body, name)
				}
				if err := writeNew(filepath.Join(abs, f.path), body); err != nil {
					return err
				}
				files = append(files, f.path)
			}
			return a.emit(cmd, func(w io.Writer, s style) {
				if s.plain {
					for _, f := range files {
						fmt.Fprintln(w, filepath.Join(dir, f))
					}
					return
				}
				fmt.Fprintln(w, s.ok("created module "+name+" in "+dir))
				for _, f := range files {
					fmt.Fprintln(w, "    "+f)
				}
				fmt.Fprintln(w, s.info("try it: lola install "+dir+" -t claude-code"))
			}, map[string]any{"name": name, "path": abs, "files": files})
		},
	}
}

// writeNew creates a file that must not exist yet.
func writeNew(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%s already exists; lola init never overwrites files", path)
	}
	if err != nil {
		return err
	}
	if _, err := f.WriteString(body); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
