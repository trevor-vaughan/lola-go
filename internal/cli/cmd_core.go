package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
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

// selectTargets implements §4.1 for install: -t, else the manifest's targets (nil lets the
// engine use them), else the installed assistants: picked on a terminal (those set up here
// preselected), otherwise those both installed and set up here.
func (a *app) selectTargets(cmd *cobra.Command, eng *engine.Engine, sc engine.Scope, flag []string) ([]string, error) {
	if len(flag) > 0 {
		return flag, nil
	}
	man, err := eng.Manifest(sc)
	if err != nil {
		return nil, err
	}
	if len(man.Targets) > 0 {
		return nil, nil
	}
	var installed, here []string
	for _, as := range eng.Detect(sc) {
		if as.Binary == "" {
			continue
		}
		installed = append(installed, as.Target)
		if as.Marked {
			here = append(here, as.Target)
		}
	}
	if len(installed) == 0 {
		return nil, errs.WithHints(errors.New("no assistant found on your PATH, so lola does not know where to install"),
			"pass -t with one or more of: "+strings.Join(targets.IDs(), ", "),
			"example: lola install ./my-module -t claude-code")
	}
	if interactive(cmd) && !a.json {
		return pickTargets(cmd, installed, here)
	}
	if len(here) == 0 {
		return nil, errs.WithHints(errors.New("none of your installed assistants is set up here, so lola does not know where to install"),
			"installed: "+strings.Join(installed, ", "),
			"pass -t with one or more of them")
	}
	return here, nil
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
	root.AddCommand(a.installCmd(), a.uninstallCmd(), a.listCmd(), a.renderCmd(), a.checkCmd())
}

func (a *app) installCmd() *cobra.Command {
	var targets []string
	var ref, path string
	var dry, force, strict, rules, noRules, allowHooks bool
	g := argGuide{noun: "module or source", find: "lola cache list (cached modules), or a module directory",
		usage:    "lola install <source|name>... [-t target]",
		examples: []string{"lola install ./my-module -t claude-code", "lola install my-module", "lola install ./monorepo --path plugins/review"}}
	cmd := &cobra.Command{
		Use:     "install <source|name>...",
		Aliases: []string{"add"},
		Short:   "Install modules into this project (or --user)",
		Long: "Install modules. Each argument is a module already in your cache (used without\n" +
			"touching its source) or a source: " + source.AcceptedForms + ".\n" +
			"The module is copied into your cache, rendered for each target, and recorded in\n" +
			".lola/project.yml (user scope: $XDG_CONFIG_HOME/lola/user.yml).\n" +
			"--no-rules installs everything except the module's always-on rules and records that\n" +
			"choice, so later renders keep it; --rules turns them back on.\n" +
			"Install hooks run only with your consent: lola asks on a terminal (default No) and\n" +
			"remembers a yes until one of those scripts changes. Consent covers only the scripts\n" +
			"themselves, not files they run. --allow-hooks consents for one run.",
		Example: g.example(),
		GroupID: groupCore,
		Args:    g.args(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if (ref != "" || path != "") && len(args) > 1 {
				return errors.New("--ref and --path apply to a single source")
			}
			eng, sc, err := a.session()
			if err != nil {
				return err
			}
			sel, err := a.selectTargets(cmd, eng, sc, targets)
			if err != nil {
				return err
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			var reqs []engine.InstallRequest
			for _, arg := range args {
				reqs = append(reqs, engine.InstallRequest{Arg: arg, Ref: ref, Path: path})
			}
			opts := engine.Options{Targets: sel, DryRun: dry, Force: force, Strict: strict}
			if rules || noRules {
				opts.Rules = &rules
			}
			a.hookOptions(cmd, &opts, allowHooks)
			res, err := eng.Install(cmd.Context(), sc, cwd, reqs, opts)
			if err != nil {
				return err
			}
			return a.emit(cmd, func(w io.Writer, s style) { printResult(w, s, "installed", res) }, res)
		},
	}
	addTargetFlag(cmd, &targets, "assistants to install for")
	cmd.Flags().StringVar(&ref, "ref", "", "git branch, tag or full commit SHA (git sources)")
	cmd.Flags().StringVar(&path, "path", "", "module root inside the source (monorepos)")
	cmd.Flags().BoolVarP(&dry, "dry-run", "n", false, "print the plan and write nothing")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite unowned or hand-edited files and render skipped artifacts that can be (never skips a prompt)")
	cmd.Flags().BoolVar(&strict, "strict", false, "fail without writing anything if any target would drop, change or skip part of a module")
	cmd.Flags().BoolVar(&noRules, "no-rules", false, "do not install the modules' always-on rules (remembered in the manifest)")
	cmd.Flags().BoolVar(&rules, "rules", false, "install the modules' rules again after --no-rules")
	cmd.MarkFlagsMutuallyExclusive("rules", "no-rules")
	cmd.Flags().BoolVar(&allowHooks, "allow-hooks", false, "run the modules' install hooks without asking (consent for this run)")
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
				if err := confirm(cmd, false, "Uninstall "+strings.Join(args, ", ")+"?"); err != nil {
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
			"missing (a file lola wrote, or the module copy, is gone); or stale (your cache has a newer version: run lola render).",
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
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "MODULE\tTARGET\tSTATUS\tRULES\tSOURCE")
	for _, r := range rows {
		status := s.ok(r.Status)
		switch r.Status {
		case "modified", "stale", "unreadable", "skipped":
			status = s.warn(r.Status)
		case "missing":
			status = s.bad(r.Status)
		}
		rules := "on"
		if !r.Rules {
			rules = s.warn("off")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.Name, r.Target, status, rules, r.Source)
	}
	_ = tw.Flush()
}

func (a *app) renderCmd() *cobra.Command {
	var targets []string
	var dry, force, strict, allowHooks bool
	cmd := &cobra.Command{
		Use:     "render",
		Aliases: []string{"update"},
		Short:   "Render every installed module again from your cache",
		Long: "Re-render every installed module from the cached version for its ref, removing files the\n" +
			"module no longer ships. Files edited by hand are reported and kept unless --force.",
		Example: "  lola render\n  lola render --dry-run\n  lola render -t claude-code --force",
		GroupID: groupCore,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			eng, sc, err := a.session()
			if err != nil {
				return err
			}
			opts := engine.Options{Targets: targets, DryRun: dry, Force: force, Strict: strict}
			a.hookOptions(cmd, &opts, allowHooks)
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
	return cmd
}

func (a *app) checkCmd() *cobra.Command {
	var targets []string
	var strict bool
	cmd := &cobra.Command{
		Use:   "check [module-or-folder...]",
		Short: "Report what each assistant would lose, change or skip from a module",
		Long: "Render modules for every assistant (or those given with -t) without writing anything, and\n" +
			"report what each target drops, adapts or cannot express. A folder is read the way lola\n" +
			"cache add reads it but is not added to your cache; any other argument names a cached\n" +
			"module. With no argument, the current directory is checked.",
		Example: "  lola check\n  lola check ./my-module -t gemini-cli\n  lola check my-module --strict",
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
