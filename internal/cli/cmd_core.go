package cli

import (
	"errors"
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
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// session returns the engine and scope for a command.
func (a *app) session(cmd *cobra.Command) (*engine.Engine, engine.Scope, error) {
	eng, err := a.engine()
	if err != nil {
		return nil, engine.Scope{}, err
	}
	sc, err := a.scope(cmd, eng)
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
	eng, sc, err := a.session(cmd)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	rows, _, err := eng.Status(cmd.Context(), sc, nil)
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

// completeAddress completes an argument that starts with @ to a marketplace address, @<market>
// or @<market>/<entry>, from your fetched catalogs (Plan 6c §3.3); any other argument completes
// as the shell completes a path.
func (a *app) completeAddress(_ *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if !strings.HasPrefix(toComplete, "@") {
		return nil, cobra.ShellCompDirectiveDefault
	}
	eng, err := a.engine()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	groups, _, err := eng.MarketGroups(nil)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []cobra.Completion
	for _, g := range groups {
		for _, addr := range append([]string{"@" + g.Name}, offerAddresses(g)...) {
			if strings.HasPrefix(addr, toComplete) {
				out = append(out, addr)
			}
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// offerAddresses are the addresses of the entries g offers.
func offerAddresses(g engine.MarketGroup) []string {
	var out []string
	for _, o := range g.Entries {
		out = append(out, o.Install)
	}
	return out
}

func (a *app) addCoreCommands(root *cobra.Command) {
	root.AddCommand(a.installCmd(), a.syncCmd(), a.uninstallCmd(), a.listCmd(), a.renderCmd(), a.tidyCmd(), a.profileCmd(), a.marketCmd(),
		a.checkCmd(), a.infoCmd())
}

func (a *app) installCmd() *cobra.Command {
	var f installFlags
	var trust trustFlag
	g := argGuide{noun: "module or source", find: "lola cache list (cached modules), a module directory, archive or git URL, or @market/module",
		usage: "lola install <source|name|@market/module>... [-t target]",
		examples: []string{"lola install ./my-module -t claude-code", "lola install my-module", "lola install ./monorepo --path plugins/review",
			"lola install https://github.com/acme/review.git --ref v1.2.0", "lola install ./review-1.2.0.tar.gz", "lola install @acme/formatter",
			"lola install review-kit --scope project --skip skill/slow-lint", "lola install"}}
	cmd := &cobra.Command{
		Use:     "install [<source|name|@market/module>...]",
		Aliases: []string{"add"},
		Short:   "Install modules into this project (or --user)",
		Long: "Install modules. Each argument is a module already in your cache (used without\n" +
			"touching its source) or a source:\n" + source.AcceptedForms + ".\n" +
			"--ref picks a git branch, tag or full commit SHA; --path picks the module root inside\n" +
			"the source.\n" +
			"@market/module installs an entry of one of your marketplaces (lola market): lola shows\n" +
			"where it resolved, then records the module's real source, path, ref and pin, with\n" +
			"market: {name, source} as provenance, so a teammate needs no marketplace. A catalog's\n" +
			"commit pin must be on its ref. --ref replaces the catalog's ref and records no market;\n" +
			"--path is refused, as the catalog gives the path. @market/module is a command-line\n" +
			"form only, never a file's source: (a directory of that name is ./@market/module); a\n" +
			"project.yml, user.yml or profile that has one is refused, naming its line, and\n" +
			"lola install @market/module replaces such an entry of the module's name.\n" +
			"The module is copied into your cache, rendered for each target, and recorded in\n" +
			".lola/project.yml (user scope: $XDG_CONFIG_HOME/lola/user.yml).\n" +
			"\n" +
			"With no argument on a terminal, lola asks for the scope, modules, assistants and\n" +
			"components, shows a summary with the equivalent commands, and applies on your say.\n" +
			"Ctrl+C cancels and writes nothing. LOLA_ACCESSIBLE=1 (or TERM=dumb) asks as numbered\n" +
			"line prompts instead. -y applies without the final menu.\n" +
			"\n" +
			"Scope: --scope, --user, --project or -C; else, on a terminal, the picker (Project, User, Other\n" +
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
			"themselves, not files they run. --trust=hooks consents without asking.\n" +
			"Claude Code runtime hooks (a plugin's hooks) are installed only with your consent too:\n" +
			"lola lists them and asks on a terminal (default No), and --trust=runtime-hooks consents\n" +
			"without asking. It holds until the module version, the hooks or the project path changes.\n" +
			"Install scripts wrap every module copy the install writes, once per assistant, around its\n" +
			"hooks: pre_install and post_install in config.yaml (yours), in .lola/project.yml (the\n" +
			"project's, run only in that project's scope, and only with --trust=scripts or a yes on a\n" +
			"terminal, until the script changes) and --pre-install/--post-install (this run's). Each\n" +
			"runs from its absolute path, with the module copy as working directory, the hook\n" +
			"environment and LOLA_SCRIPT_SOURCE=user, project or flag.\n" +
			trustHelp("install"),
		Example:           g.example(),
		GroupID:           groupCore,
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: a.completeAddress,
		RunE: func(cmd *cobra.Command, args []string) error {
			var err error
			if f.trust, err = trust.parse(cmd, args, "install", "module"); err != nil {
				return err
			}
			// A script path a hint repeats must print as itself, on one line (RR4).
			for _, s := range []struct{ flag, given string }{{"--pre-install", f.preInstall}, {"--post-install", f.postInstall}} {
				if s.given != "" && !untrusted.IsPlain(s.given) {
					// Text keeps a line break and cleans \r to ?, so a \n made \r shows as ?, on one line.
					return fmt.Errorf("%s %s holds a control character or a line break; lola runs only a script whose path is plain text",
						s.flag, untrusted.Text(strings.ReplaceAll(s.given, "\n", "\r"), len(s.given)))
				}
			}
			return a.runInstall(cmd, g, args, f)
		},
	}
	addTargetFlag(cmd, &f.targets, "assistants to install for")
	cmd.Flags().StringArrayVar(&f.skips, "skip", nil, "do not install these components, and remember it in selections.yaml (kind/name or module:kind/name; repeatable)")
	cmd.Flags().StringArrayVar(&f.onlys, "only", nil, "install only these components of each module that has them, and remember it (repeatable)")
	cmd.Flags().StringArrayVar(&f.unskips, "unskip", nil, "install these skipped components again, and remember it (repeatable)")
	cmd.Flags().BoolVar(&f.global, "global", false, "save --skip, --only and --unskip in the global section of selections.yaml, for every scope")
	cmd.Flags().StringVar(&f.ref, "ref", "", "git branch, tag or full commit SHA (git sources)")
	cmd.Flags().StringVar(&f.path, "path", "", "module root inside the source (monorepos)")
	cmd.Flags().StringVar(&f.preInstall, "pre-install", "", "a script to run before each module copy the install writes, once per assistant (yours: no trust asked)")
	cmd.Flags().StringVar(&f.postInstall, "post-install", "", "a script to run after each module copy the install writes, once per assistant (yours: no trust asked)")
	cmd.Flags().BoolVarP(&f.dry, "dry-run", "n", false, "print the plan and write nothing (not even selections.yaml)")
	cmd.Flags().BoolVarP(&f.yes, "yes", "y", false, "apply after the pickers without the review menu")
	cmd.Flags().BoolVar(&f.force, "force", false, "overwrite unowned or hand-edited files and render skipped artifacts that can be, applying without the review menu (never answers a consent question)")
	cmd.Flags().BoolVar(&f.strict, "strict", false, "fail without writing anything if any target would drop, change or skip part of a module")
	cmd.Flags().BoolVar(&f.noRules, "no-rules", false, "do not install the modules' always-on rules (remembered in the manifest)")
	cmd.Flags().BoolVar(&f.rules, "rules", false, "install the modules' rules again after --no-rules")
	cmd.MarkFlagsMutuallyExclusive("rules", "no-rules")
	addTrustFlag(cmd, &trust, "install", "trust these kinds without asking: scripts, hooks, runtime-hooks or all (recorded as a yes is)")
	return cmd
}

func (a *app) uninstallCmd() *cobra.Command {
	var targets []string
	var dry, yes, force bool
	g := argGuide{noun: "module", find: "lola list", usage: "lola uninstall <module>... [-t target] [-y]",
		examples: []string{"lola uninstall my-module -y", "lola uninstall my-module -t claude-code", "lola uninstall my-module --dry-run"}}
	cmd := &cobra.Command{
		Use:     "uninstall <module>...",
		Aliases: []string{"remove", "rm", "erase", "delete", "del"},
		Short:   "Remove modules from this project (or --user)",
		Long: "Remove everything lola wrote for the modules (on -t targets only, if given). The modules stay\nin your cache; remove them there with lola cache rm.\n\n" +
			scopeHelp,
		Example:           g.example(),
		GroupID:           groupCore,
		Args:              g.args(1),
		ValidArgsFunction: a.completeInstalled,
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := a.engine()
			if err != nil {
				return err
			}
			scs, err := a.scopes(cmd, eng, args, "Uninstall from where?", false, false)
			if err != nil {
				return err
			}
			sc := scs[0].Scope
			opts := engine.Options{Targets: targets, DryRun: true, Force: force, ForceRun: forceRun(cmd, args), Display: outputFlags(cmd)}
			// --force answers the question, as -y does (user decision, 2026-10-22).
			if !dry && !yes && !force {
				// The preview is a dry run, whose refusal of an @ source would hint -n: coach it as this run.
				if _, err := eng.Manifest(sc); err != nil {
					if _, ok := errors.AsType[*manifest.UnresolvedError](err); ok {
						return eng.CoachUnresolved(err, []engine.Scope{sc}, dry, outputFlags(cmd))
					}
				}
				preview, err := eng.Uninstall(cmd.Context(), sc, args, opts)
				if err != nil {
					return err
				}
				show := func(w io.Writer) { printResult(w, style{}, "", preview) }
				if err := a.confirm(cmd, args, "Uninstall "+strings.Join(args, ", ")+"?", Nav{}, preview, show); err != nil {
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
	cmd.Flags().BoolVar(&force, "force", false, "also delete files edited since lola wrote them, without asking (never answers a consent question)")
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
			"→ update available: your cache has a newer version for the ref: lola sync --update <module>.\n" +
			"FROM, shown when a profile is involved, names where the module comes from: project.yml, user.yml\n" +
			"or a profile (profile:default, profile:user:<name>).\n\n" +
			"Scope: --project, --user, -C <dir> or --scope (both included); without them, the project with\n" +
			".lola/ at or above the current directory, if any, and user scope. list never asks, and\n" +
			"default_scope does not apply. Every row names its scope.",
		Example: "  lola list\n  lola list --user\n  lola list --scope both -C ~/work/app\n  lola list --json",
		GroupID: groupCore,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			eng, err := a.engine()
			if err != nil {
				return err
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			req, err := a.scopeRequest(cwd, false)
			if err != nil {
				return err
			}
			if err := eng.CheckTargets(targets); err != nil {
				return err
			}
			scs, err := eng.ResolveReadScope(req)
			if err != nil {
				return err
			}
			rows, notices := []engine.StatusRow{}, []engine.Notice{}
			var scopes []engine.Scope
			for i, c := range scs {
				got, notes, err := eng.Status(cmd.Context(), c.Scope, outputFlags(cmd))
				if err != nil && len(scs) == 2 && c.Source == engine.SourceDiscovered {
					// No flag chose these scopes, so one that cannot be read must not hide the other.
					return errs.WithHints(err, "list the other scope: "+eng.Command(scs[1-i].Scope, "list"))
				}
				if err != nil {
					return err
				}
				for _, r := range got {
					if len(targets) == 0 || slices.Contains(targets, r.Target) {
						rows = append(rows, r)
					}
				}
				notices = append(notices, notes...)
				scopes = append(scopes, c.Scope)
			}
			return a.emit(cmd, func(w io.Writer, s style) {
				printStatus(w, s, scopes, rows, func(sc engine.Scope) string { return eng.Command(sc, "install") })
				for _, n := range notices {
					fmt.Fprintln(w, noticeLine(s, n))
				}
			}, map[string]any{"modules": rows, "notices": notices})
		},
	}
	addTargetFlag(cmd, &targets, "only show these assistants")
	return cmd
}

// printStatus prints the rows of every scope list read: a table whose rows each name their
// scope, then a line per scope with nothing installed, coaching install (the command install
// names for a scope). --plain rows are name, target, status, rules, source, scope, from.
func printStatus(w io.Writer, s style, scopes []engine.Scope, rows []engine.StatusRow, install func(engine.Scope) string) {
	if s.plain {
		for _, r := range rows {
			rules := "on"
			if !r.Rules {
				rules = "off"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.Name, r.Target, r.Status, rules, r.Source, r.Scope, r.From)
		}
		return
	}
	var empty []string
	for _, sc := range scopes {
		if !slices.ContainsFunc(rows, func(r engine.StatusRow) bool { return r.Scope == sc.Kind.String() }) {
			where := "in " + untrusted.Text(sc.ProjectDir, untrusted.MaxName)
			if sc.Kind == target.User {
				where = "at user scope"
			}
			empty = append(empty, "nothing installed "+where+" (try: "+install(sc)+" <source>)")
		}
	}
	if len(rows) == 0 {
		for _, line := range empty {
			fmt.Fprintln(w, line)
		}
		return
	}
	// The SKIPPED column (components selections.yaml excludes) appears only when one is, and
	// FROM only when a module comes from a profile.
	skipped := slices.ContainsFunc(rows, func(r engine.StatusRow) bool { return len(r.Skipped) > 0 })
	profiled := slices.ContainsFunc(rows, func(r engine.StatusRow) bool { return strings.HasPrefix(r.From, "profile:") })
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	head := "SCOPE\tMODULE\tTARGET\tSTATUS\tRULES"
	if skipped {
		head += "\tSKIPPED"
	}
	head += "\tSOURCE"
	if profiled {
		head += "\tFROM"
	}
	fmt.Fprintln(tw, head)
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
		line := strings.Join([]string{r.Scope, r.Name, r.Target, status, rules, r.Source}, "\t")
		if profiled {
			line += "\t" + r.From
		}
		fmt.Fprintln(tw, line)
	}
	_ = tw.Flush()
	for _, line := range empty {
		fmt.Fprintln(w, line)
	}
}

func (a *app) renderCmd() *cobra.Command {
	var targets []string
	var dry, force, strict bool
	var trust trustFlag
	cmd := &cobra.Command{
		Use:     "render",
		Aliases: []string{"update"},
		Short:   "Render every installed module again from your cache",
		Long: "Re-render every installed module at the version it was installed at (a folder module from its\n" +
			"folder), fetching that version again if your cache lost it, and remove files the module no\n" +
			"longer ships. Files edited by hand are reported and kept unless --force.\n" +
			"Claude Code runtime hooks whose consent no longer matches (a new version, or a moved\n" +
			"project) are asked about again, and removed unless you consent.\n" + trustHelp("render") + "\n\n" + scopeHelp,
		Example: "  lola render\n  lola render --dry-run\n  lola render -t claude-code --force",
		GroupID: groupCore,
		// Arguments reach RunE so that a kind written after a bare --trust is coached
		// (trust.parse); any other is refused as cobra.NoArgs refuses it.
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			kinds, err := trust.parse(cmd, args, "render", "module")
			if err != nil {
				return err
			}
			if err := cobra.NoArgs(cmd, args); err != nil {
				return err
			}
			eng, err := a.engine()
			if err != nil {
				return err
			}
			scs, err := a.scopes(cmd, eng, nil, "Render where?", false, false)
			if err != nil {
				return err
			}
			sc := scs[0].Scope
			opts := engine.Options{Targets: targets, DryRun: dry, Force: force, Strict: strict, ForceRun: forceRun(cmd, nil), Display: outputFlags(cmd),
				Trust: kinds, Receipt: a.receipt(cmd, kinds, false), Rerun: trustRun(cmd, nil)}
			a.hookOptions(cmd, &opts)
			res, err := eng.Render(cmd.Context(), sc, opts)
			if err != nil {
				return err
			}
			return a.emit(cmd, func(w io.Writer, s style) { printResult(w, s, "rendered", res) }, res)
		},
	}
	addTargetFlag(cmd, &targets, "only render for these assistants")
	cmd.Flags().BoolVarP(&dry, "dry-run", "n", false, "print the plan and write nothing")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite or delete hand-edited files and render skipped artifacts that can be (never answers a consent question)")
	cmd.Flags().BoolVar(&strict, "strict", false, "fail without writing anything if any target would drop, change or skip part of a module")
	addTrustFlag(cmd, &trust, "render", "trust these kinds without asking: scripts, hooks, runtime-hooks or all (recorded as a yes is)")
	return cmd
}

func (a *app) checkCmd() *cobra.Command {
	var targets []string
	var strict bool
	cmd := &cobra.Command{
		Use:   "check [module-or-source...]",
		Short: "Report what each assistant would lose, change or skip from a module",
		Long: "Render modules for every assistant (or those given with -t) without writing anything, and\n" +
			"report what each target drops, adapts or cannot express. A source, or @market/module, an\n" +
			"entry of one of your marketplaces, is fetched the way lola cache add fetches it but is not\n" +
			"added to your cache; any other argument names a cached module. With no argument, the\n" +
			"current directory is checked. Sources:\n" + source.AcceptedForms + ".",
		Example: "  lola check\n  lola check ./my-module -t gemini-cli\n  lola check my-module --strict\n  lola check ./review-1.2.0.zip\n" +
			"  lola check @acme/formatter",
		GroupID:           groupCore,
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: a.completeAddress,
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, sc, err := a.session(cmd)
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
			rep, err := eng.Check(cmd.Context(), sc, cwd, args, targets, outputFlags(cmd))
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
