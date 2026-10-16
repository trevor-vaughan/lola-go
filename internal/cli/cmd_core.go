package cli

import (
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/targets"
)

// session returns the engine and scope for a command.
func (a *app) session() (*engine.Engine, engine.Scope, error) {
	eng, err := a.engine()
	if err != nil {
		return nil, engine.Scope{}, err
	}
	sc, err := a.scope()
	return eng, sc, err
}

func addTargetFlag(cmd *cobra.Command, dst *[]string, usage string) {
	cmd.Flags().StringSliceVarP(dst, "target", "t", nil, usage+" (repeatable or comma-separated: "+strings.Join(targets.IDs(), ", ")+")")
	_ = cmd.RegisterFlagCompletionFunc("target", cobra.FixedCompletions(targets.IDs(), cobra.ShellCompDirectiveNoFileComp))
}

// installOutput is install's JSON: the engine's result and the commands that repeat the run
// off a terminal (empty when no picker ran).
type installOutput struct {
	*engine.InstallResult
	EquivalentCommands []string `json:"equivalent_commands"`
}

// selectionEdits parses the --skip, --only and --unskip values.
func selectionEdits(skips, onlys, unskips []string) ([]engine.SelectionEdit, error) {
	var out []engine.SelectionEdit
	for _, f := range []struct {
		op   string
		vals []string
	}{{engine.EditSkip, skips}, {engine.EditOnly, onlys}, {engine.EditUnskip, unskips}} {
		for _, v := range f.vals {
			sel, err := config.ParseSelector(v)
			if err != nil {
				return nil, fmt.Errorf("--%s: %w", f.op, err)
			}
			out = append(out, engine.SelectionEdit{Op: f.op, Selector: sel})
		}
	}
	return out, nil
}

// savedTo is the selections.yaml an install wrote, or "".
func savedTo(r *engine.Result) string {
	for _, m := range r.Modules {
		if m.SavedTo != "" {
			return m.SavedTo
		}
	}
	return ""
}

// completeInstalled offers the names of modules installed in the current scope.
func (a *app) completeInstalled(cmd *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	eng, sc, err := a.session()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	rows, err := eng.Status(sc)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []cobra.Completion
	for _, r := range rows {
		if strings.HasPrefix(r.Name, toComplete) && (len(out) == 0 || out[len(out)-1] != r.Name) {
			out = append(out, r.Name)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

func (a *app) addCoreCommands(root *cobra.Command) {
	root.AddCommand(a.installCmd(), a.syncCmd(), a.uninstallCmd(), a.listCmd(), a.renderCmd(), a.checkCmd())
}

func (a *app) installCmd() *cobra.Command {
	var f installFlags
	g := argGuide{noun: "module or source", find: "lola cache list (cached modules), or a module directory, archive or git URL",
		usage: "lola install <source|name>... [-t target]",
		examples: []string{"lola install ./my-module -t claude-code", "lola install my-module", "lola install ./monorepo --path plugins/review",
			"lola install https://github.com/acme/review.git --ref v1.2.0", "lola install ./review-1.2.0.tar.gz",
			"lola install review-kit --scope project --skip skill/slow-lint", "lola install"}}
	cmd := &cobra.Command{
		Use:     "install [<source|name>...]",
		Aliases: []string{"add"},
		Short:   "Install modules into this project (or --user)",
		Long: "Install modules. Each argument is a module already in your cache (used without\n" +
			"touching its source) or a source:\n" + source.AcceptedForms + ".\n" +
			"--ref picks a git branch, tag or full commit SHA; --path picks the module root inside\n" +
			"the source.\n" +
			"The module is copied into your cache, rendered for each target, and recorded in\n" +
			".lola/project.yml (user scope: $XDG_CONFIG_HOME/lola/user.yml).\n" +
			"\n" +
			"With no argument on a terminal, lola asks for the scope, modules, assistants and\n" +
			"components, shows a summary with the equivalent commands, and applies on your say.\n" +
			"Ctrl+C cancels and writes nothing. LOLA_ACCESSIBLE=1 (or TERM=dumb) asks as numbered\n" +
			"line prompts instead. -y applies without the final menu.\n" +
			"\n" +
			"Scope: --scope, --user or -C; else, on a terminal, the picker (Project, User, Other\n" +
			"directory…; nothing preselected, pick any of them); else a project with .lola/ at or\n" +
			"above the current directory; else default_scope in $XDG_CONFIG_HOME/lola/config.yaml\n" +
			"(version: 1, default_scope: project|user|both); otherwise install refuses to guess.\n" +
			"\n" +
			"Components: $XDG_CONFIG_HOME/lola/selections.yaml lists the components you skip, globally,\n" +
			"at user scope and per project; install, render and check honor it. --skip, --only and\n" +
			"--unskip take kind/name or module:kind/name (kind: skill, command, agent, mcp, rule;\n" +
			"name may be a glob) and save to that file; --global saves to its global section.\n" +
			"\n" +
			"--no-rules installs everything except the module's always-on rules and records that\n" +
			"choice, so later renders keep it; --rules turns them back on.\n" +
			"Install hooks run only with your consent: lola asks on a terminal (default No) and\n" +
			"remembers a yes until one of those scripts changes. Consent covers only the scripts\n" +
			"themselves, not files they run. --allow-hooks consents for one run.\n" +
			"Claude Code runtime hooks (a plugin's hooks) are installed only with your consent too:\n" +
			"lola lists them and asks on a terminal (default No), and --allow-runtime-hooks records\n" +
			"consent off one. It holds until the module version, the hooks or the project path changes.",
		Example: g.example(),
		GroupID: groupCore,
		Args:    cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runInstall(cmd, g, args, f)
		},
	}
	addTargetFlag(cmd, &f.targets, "assistants to install for")
	cmd.Flags().StringVar(&f.scope, "scope", "", "where to install: project, user or both")
	_ = cmd.RegisterFlagCompletionFunc("scope", cobra.FixedCompletions([]string{"project", "user", "both"}, cobra.ShellCompDirectiveNoFileComp))
	cmd.Flags().StringArrayVar(&f.skips, "skip", nil, "do not install these components, and remember it in selections.yaml (kind/name or module:kind/name; repeatable)")
	cmd.Flags().StringArrayVar(&f.onlys, "only", nil, "install only these components of each module that has them, and remember it (repeatable)")
	cmd.Flags().StringArrayVar(&f.unskips, "unskip", nil, "install these skipped components again, and remember it (repeatable)")
	cmd.Flags().BoolVar(&f.global, "global", false, "save --skip, --only and --unskip in the global section of selections.yaml, for every scope")
	cmd.Flags().StringVar(&f.ref, "ref", "", "git branch, tag or full commit SHA (git sources)")
	cmd.Flags().StringVar(&f.path, "path", "", "module root inside the source (monorepos)")
	cmd.Flags().BoolVarP(&f.dry, "dry-run", "n", false, "print the plan and write nothing (not even selections.yaml)")
	cmd.Flags().BoolVarP(&f.yes, "yes", "y", false, "apply after the pickers without the review menu")
	cmd.Flags().BoolVar(&f.force, "force", false, "overwrite unowned or hand-edited files and render skipped artifacts that can be (never skips a prompt)")
	cmd.Flags().BoolVar(&f.strict, "strict", false, "fail without writing anything if any target would drop, change or skip part of a module")
	cmd.Flags().BoolVar(&f.noRules, "no-rules", false, "do not install the modules' always-on rules (remembered in the manifest)")
	cmd.Flags().BoolVar(&f.rules, "rules", false, "install the modules' rules again after --no-rules")
	cmd.MarkFlagsMutuallyExclusive("rules", "no-rules")
	cmd.Flags().BoolVar(&f.allowHooks, "allow-hooks", false, "run the modules' install hooks without asking (consent for this run)")
	cmd.Flags().BoolVar(&f.allowRuntime, "allow-runtime-hooks", false, "install the modules' Claude Code runtime hooks without asking (consent is recorded)")
	return cmd
}

func (a *app) uninstallCmd() *cobra.Command {
	var targets []string
	var dry, yes, force bool
	g := argGuide{noun: "module", find: "lola list", usage: "lola uninstall <module>... [-t target] [-y]",
		examples: []string{"lola uninstall my-module -y", "lola uninstall my-module -t claude-code", "lola uninstall my-module --dry-run"}}
	cmd := &cobra.Command{
		Use:               "uninstall <module>...",
		Aliases:           []string{"remove", "rm", "erase", "delete", "del"},
		Short:             "Remove modules from this project (or --user)",
		Long:              "Remove everything lola wrote for the modules (on -t targets only, if given). The modules stay\nin your cache; remove them there with lola cache rm.",
		Example:           g.example(),
		GroupID:           groupCore,
		Args:              g.args(1),
		ValidArgsFunction: a.completeInstalled,
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, sc, err := a.session()
			if err != nil {
				return err
			}
			opts := engine.Options{Targets: targets, DryRun: true, Force: force}
			if !dry && !yes {
				preview, err := eng.Uninstall(cmd.Context(), sc, args, opts)
				if err != nil {
					return err
				}
				printResult(cmd.ErrOrStderr(), style{}, "", preview)
				if err := a.confirm(cmd, false, "Uninstall "+strings.Join(args, ", ")+"?"); err != nil {
					return err
				}
			}
			opts.DryRun = dry
			res, err := eng.Uninstall(cmd.Context(), sc, args, opts)
			if err != nil {
				return err
			}
			return a.emit(cmd, func(w io.Writer, s style) { printResult(w, s, "uninstalled", res) }, res)
		},
	}
	addTargetFlag(cmd, &targets, "only uninstall from these assistants")
	cmd.Flags().BoolVarP(&dry, "dry-run", "n", false, "print the plan and write nothing")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	cmd.Flags().BoolVar(&force, "force", false, "also delete files edited since lola wrote them (never skips a prompt)")
	return cmd
}

func (a *app) listCmd() *cobra.Command {
	var targets []string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List installed modules and their status per target",
		Long: "List installed modules per target. Status is ok; modified (a file lola wrote was edited);\n" +
			"missing (a file lola wrote, or the module copy, is gone); stale (project.yml pins another version\n" +
			"than the one installed: run lola sync); not installed (listed in project.yml: run lola sync); or\n" +
			"skipped (nothing of the module could be rendered for the target).\n" +
			"→ update available: your cache has a newer version for the ref: lola sync --update <module>.",
		Example: "  lola list\n  lola list --user\n  lola list --json",
		GroupID: groupCore,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			eng, sc, err := a.session()
			if err != nil {
				return err
			}
			rows, err := eng.Status(sc)
			if err != nil {
				return err
			}
			if len(targets) > 0 {
				kept := rows[:0]
				for _, r := range rows {
					if strings.Contains(","+strings.Join(targets, ",")+",", ","+r.Target+",") {
						kept = append(kept, r)
					}
				}
				rows = kept
			}
			return a.emit(cmd, func(w io.Writer, s style) { printStatus(w, s, sc, rows) }, map[string]any{"modules": rows})
		},
	}
	addTargetFlag(cmd, &targets, "only show these assistants")
	return cmd
}

func printStatus(w io.Writer, s style, sc engine.Scope, rows []engine.StatusRow) {
	if s.plain {
		for _, r := range rows {
			rules := "on"
			if !r.Rules {
				rules = "off"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Name, r.Target, r.Status, rules, r.Source)
		}
		return
	}
	if len(rows) == 0 {
		where := "in " + sc.ProjectDir
		if sc.Kind == target.User {
			where = "at user scope"
		}
		fmt.Fprintf(w, "nothing installed %s (try: lola install <source>)\n", where)
		return
	}
	// The SKIPPED column (components selections.yaml excludes) appears only when one is.
	skipped := slices.ContainsFunc(rows, func(r engine.StatusRow) bool { return len(r.Skipped) > 0 })
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	head := "MODULE\tTARGET\tSTATUS\tRULES"
	if skipped {
		head += "\tSKIPPED"
	}
	fmt.Fprintln(tw, head+"\tSOURCE")
	for _, r := range rows {
		status := s.ok(r.Status)
		switch r.Status {
		case "modified", "stale", "unreadable", "skipped":
			status = s.warn(r.Status)
		case "not_installed":
			status = s.warn("not installed")
		case "missing":
			status = s.bad(r.Status)
		}
		if r.UpdateAvailable {
			status += " → update available"
		}
		rules := "on"
		if !r.Rules {
			rules = s.warn("off")
		}
		if skipped {
			rules += "\t" + strconv.Itoa(len(r.Skipped))
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.Name, r.Target, status, rules, r.Source)
	}
	_ = tw.Flush()
}

func (a *app) renderCmd() *cobra.Command {
	var targets []string
	var dry, force, strict, allowHooks, allowRuntime bool
	cmd := &cobra.Command{
		Use:     "render",
		Aliases: []string{"update"},
		Short:   "Render every installed module again from your cache",
		Long: "Re-render every installed module at the version it was installed at (a folder module from its\n" +
			"folder), fetching that version again if your cache lost it, and remove files the module no\n" +
			"longer ships. Files edited by hand are reported and kept unless --force.\n" +
			"Claude Code runtime hooks whose consent no longer matches (a new version, or a moved\n" +
			"project) are asked about again, and removed unless you consent.",
		Example: "  lola render\n  lola render --dry-run\n  lola render -t claude-code --force",
		GroupID: groupCore,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			eng, sc, err := a.session()
			if err != nil {
				return err
			}
			opts := engine.Options{Targets: targets, DryRun: dry, Force: force, Strict: strict}
			a.hookOptions(cmd, &opts, allowHooks, allowRuntime)
			res, err := eng.Render(cmd.Context(), sc, opts)
			if err != nil {
				return err
			}
			return a.emit(cmd, func(w io.Writer, s style) { printResult(w, s, "rendered", res) }, res)
		},
	}
	addTargetFlag(cmd, &targets, "only render for these assistants")
	cmd.Flags().BoolVarP(&dry, "dry-run", "n", false, "print the plan and write nothing")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite or delete hand-edited files and render skipped artifacts that can be (never skips a prompt)")
	cmd.Flags().BoolVar(&strict, "strict", false, "fail without writing anything if any target would drop, change or skip part of a module")
	cmd.Flags().BoolVar(&allowHooks, "allow-hooks", false, "run the install hooks of updated modules without asking (consent for this run)")
	cmd.Flags().BoolVar(&allowRuntime, "allow-runtime-hooks", false, "install Claude Code runtime hooks that need consent without asking (consent is recorded)")
	return cmd
}

func (a *app) checkCmd() *cobra.Command {
	var targets []string
	var strict bool
	cmd := &cobra.Command{
		Use:   "check [module-or-source...]",
		Short: "Report what each assistant would lose, change or skip from a module",
		Long: "Render modules for every assistant (or those given with -t) without writing anything, and\n" +
			"report what each target drops, adapts or cannot express. A source is fetched the way\n" +
			"lola cache add fetches it but is not added to your cache; any other argument names a\n" +
			"cached module. With no argument, the current directory is checked. Sources:\n" + source.AcceptedForms + ".",
		Example: "  lola check\n  lola check ./my-module -t gemini-cli\n  lola check my-module --strict\n  lola check ./review-1.2.0.zip",
		GroupID: groupCore,
		Args:    cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, sc, err := a.session()
			if err != nil {
				return err
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			if len(args) == 0 {
				args = []string{"."}
			}
			rep, err := eng.Check(cmd.Context(), sc, cwd, args, targets)
			if err != nil {
				return err
			}
			if err := a.emit(cmd, func(w io.Writer, s style) { printCheck(w, s, rep) }, rep); err != nil || !strict {
				return err
			}
			var all []target.Finding
			for _, m := range rep.Modules {
				all = append(all, m.Findings...)
			}
			return engine.StrictError(all, "fix the module; lola install renders what each target supports", false)
		},
	}
	addTargetFlag(cmd, &targets, "only check these assistants")
	cmd.Flags().BoolVar(&strict, "strict", false, "exit with an error if any target would drop, change or skip part of a module")
	return cmd
}
