package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/target"
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

func (a *app) addTargetFlag(cmd *cobra.Command, dst *[]string, usage string) {
	cmd.Flags().StringSliceVarP(dst, "target", "t", nil, usage+" (repeatable)")
	_ = cmd.RegisterFlagCompletionFunc("target", a.completeTargets)
}

// installOutput is install's JSON: the engine's result and the commands that repeat the run
// off a terminal (empty when no picker ran).
type installOutput struct {
	*engine.InstallResult
	EquivalentCommands []string `json:"equivalent_commands"`
}

// selectionEdits parses the --skip, --only and --unskip values; exact marks the --only edits
// (--exact).
func selectionEdits(skips, onlys, unskips []string, exact bool) ([]engine.SelectionEdit, error) {
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
			out = append(out, engine.SelectionEdit{Op: f.op, Selector: sel, Exact: exact && f.op == engine.EditOnly})
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
// or @<market>/<entry>, from your fetched catalogs; any other argument completes
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
	root.AddCommand(a.installCmd(), a.syncCmd(), a.uninstallCmd(), a.listCmd(), a.updateCmd(), a.tidyCmd(), a.profileCmd(), a.marketCmd(),
		a.checkCmd(), a.infoCmd())
}

func (a *app) installCmd() *cobra.Command {
	var f installFlags
	var trust trustFlag
	g := argGuide{noun: "module or source", find: "lola cache list (cached modules), a module directory, archive or git URL, or @market/module",
		usage: "lola install <source|name|@market/module>... [-t target]",
		examples: []string{"lola install ./my-module -t claude-code", "lola install my-module", "lola install ./monorepo --path plugins/review",
			"lola install https://github.com/acme/review.git --ref v1.2.0", "lola install ./review-1.2.0.tar.gz", "lola install @acme/formatter",
			"lola install review-kit --scope project --skip skill/slow-lint", "lola install review-kit --unskip skill/slow-lint",
			"lola install ./m -C /srv/app -t opencode@1.19", "lola install"}}
	cmd := &cobra.Command{
		Use:     "install [<source|name|@market/module>...]",
		Aliases: []string{"add"},
		Short:   "Install modules into this project (or --user)",
		Long: "Install modules into this project, or into your user profile with --user. Each argument is a\n" +
			"module already in your cache, a source (folder, archive or git URL) or @market/module.\n" +
			"Assistants (-t): " + strings.Join(a.reg.IDs(), ", ") + ".\n" +
			"Installing for fewer assistants than a module is on removes its files for the others,\n" +
			"after asking.\n" +
			"\n" +
			"lola copies the module into your cache, renders it for each assistant and records it in\n" +
			".lola/project.yml (user scope: your default profile). With no argument on a terminal it asks\n" +
			"for scope, modules and components, and shows a review before it writes anything.\n" +
			"\n" +
			"Scope: --project, --user, -C <dir> or --scope; else the terminal's picker, the project at or\n" +
			"above here, or default_scope in config.yaml; otherwise it refuses to guess.\n" +
			"The project's install scripts and install hooks run only with your consent: lola asks on a\n" +
			"terminal (default No); --trust=<kinds> answers for CI. --skip, --only and --unskip pick\n" +
			"components; -n writes nothing.",
		Example:           g.example(),
		GroupID:           groupCore,
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: a.completeSource,
		RunE: func(cmd *cobra.Command, args []string) error {
			var err error
			if f.trust, err = trust.parse(cmd, args, "install", "module"); err != nil {
				return err
			}
			f.unseen = trust.unseen
			if f.pinned, err = parseAt(f.at, runWithout(cmd, args, "at")); err != nil {
				return err
			}
			// A script path a hint repeats must print as itself, on one line.
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
	a.addTargetFlag(cmd, &f.targets, "assistants to install for; <id>@<version> also picks the version")
	cmd.Flags().StringSliceVar(&f.skips, "skip", nil, "do not install these components (kind/name or module:kind/name; repeatable)")
	cmd.Flags().StringSliceVar(&f.onlys, "only", nil, "only these components; a later version's new ones come too unless --exact")
	cmd.Flags().StringSliceVar(&f.unskips, "unskip", nil, "install these skipped components again (repeatable)")
	cmd.Flags().BoolVar(&f.global, "global", false, "save --skip, --only and --unskip globally, for every scope")
	cmd.Flags().BoolVar(&f.exact, "exact", false, "with --only: save an only: list, so a later version's new components stay out")
	cmd.Flags().BoolVar(&f.withMentions, "with-mentions", false, "also install the components your picks mention by name (one hop)")
	cmd.Flags().StringVar(&f.ref, "ref", "", "git branch, tag or full commit SHA (git sources)")
	cmd.Flags().StringVar(&f.path, "path", "", "module root inside the source (monorepos)")
	cmd.Flags().StringArrayVar(&f.at, "at", nil, atUsage)
	cmd.Flags().BoolVar(&f.override, "override", false, "give the profile its own entry for a module another layer provides")
	cmd.Flags().StringVar(&f.profile, "profile", "", "write the entry into this profile (base: the scope's base), not the one on")
	cmd.Flags().StringVar(&f.preInstall, "pre-install", "", "your script to run before each module copy is written (no trust asked)")
	cmd.Flags().StringVar(&f.postInstall, "post-install", "", "your script to run after each module copy is written (no trust asked)")
	cmd.Flags().BoolVarP(&f.dry, "dry-run", "n", false, "print the plan and write nothing")
	cmd.Flags().BoolVarP(&f.yes, "yes", "y", false, "ask no question but consent (only --trust answers); skip the review menu")
	cmd.Flags().BoolVar(&f.force, "force", false, "overwrite unowned or hand-edited files, skip the review; never answers consent")
	cmd.Flags().BoolVar(&f.strict, "strict", false, "fail, writing nothing, if a target would drop, change or skip part of a module")
	cmd.Flags().BoolP("verbose", "v", false, "print the report in full: every fold open and every notice")
	cmd.Flags().BoolVar(&f.noRules, "no-rules", false, "do not install the modules' always-on rules (remembered in the manifest)")
	cmd.Flags().BoolVar(&f.rules, "rules", false, "install the modules' rules again after --no-rules")
	cmd.MarkFlagsMutuallyExclusive("rules", "no-rules")
	addTrustFlag(cmd, &trust, "install", "trust without asking: scripts, hooks, runtime-hooks, all")
	for _, name := range []string{"skip", "only", "unskip"} {
		_ = cmd.RegisterFlagCompletionFunc(name, a.completeComponents(0))
	}
	_ = cmd.RegisterFlagCompletionFunc("ref", cobra.NoFileCompletions)
	_ = cmd.RegisterFlagCompletionFunc("path", completeDirs)
	_ = cmd.RegisterFlagCompletionFunc("at", a.completeAt)
	_ = cmd.RegisterFlagCompletionFunc("profile", a.completeProfiles(true))
	_ = cmd.RegisterFlagCompletionFunc("pre-install", completeFiles)
	_ = cmd.RegisterFlagCompletionFunc("post-install", completeFiles)
	return cmd
}

func (a *app) uninstallCmd() *cobra.Command {
	var targets []string
	var dry, yes, force, keepEdits, all bool
	var profileName string
	g := argGuide{noun: "module", find: "lola list", usage: "lola uninstall <module>... [-t target] [-y]",
		examples: []string{"lola uninstall my-module -y", "lola uninstall my-module -t claude-code", "lola uninstall my-module --dry-run"}}
	cmd := &cobra.Command{
		Use:     "uninstall <module>... | --all",
		Aliases: []string{"remove", "rm", "erase", "delete", "del"},
		Short:   "Remove modules from this project (or --user)",
		Long: "Remove the modules' entries from the layer each comes from — the profile on, or the base (asked\nwhen a profile is on; --profile base answers off a terminal) — and everything lola wrote for\nthem (on -t targets only, if given: the entry keeps the others). A module another layer still\nlists stays installed. The modules stay in your cache; remove them there with lola cache rm.\n\n" +
			"--all removes every module lola list shows for the scope, or, with --profile, those that profile\nlists; it asks once per scope, listing them, and --scope both runs it at each scope. It takes no names.\n\n" +
			scopeHelp,
		Example: g.example(),
		GroupID: groupCore,
		Args: func(cmd *cobra.Command, args []string) error {
			if !all {
				return g.args(1)(cmd, args)
			}
			if len(args) == 0 {
				return nil
			}
			return errs.WithHints(fmt.Errorf("--all removes every module, and %s names some", untrusted.Text(strings.Join(args, ", "), untrusted.MaxName)),
				"remove only those: "+runWithout(cmd, args, "all"), "remove them all: "+runWithout(cmd, nil))
		},
		ValidArgsFunction: a.completeInstalled,
		RunE: func(cmd *cobra.Command, args []string) error {
			if keepEdits && force {
				return errs.WithHints(errors.New("--keep-edits and --force cannot be combined; pick one"),
					"keep the edited files: "+runWithout(cmd, args, "force"), "delete them too: "+runWithout(cmd, args, "keep-edits"))
			}
			eng, err := a.engine()
			if err != nil {
				return err
			}
			scs, err := a.scopes(cmd, eng, args, "Uninstall from where?", all, false)
			if err != nil {
				return err
			}
			// --all answers which layer a module comes from, as -y does: it names every module.
			req := engine.TargetRequest{Profile: profileName, Yes: yes || all, Rerun: func(extra ...string) string {
				// -y stays, and a run that lacked it gains it: the hinted run then asks nothing the
				// hint itself does not answer (the layer by --profile, the plan by -y).
				if !yes && !dry {
					extra = append(extra, "-y")
				}
				return strings.Join(append([]string{runWithout(cmd, args, "profile", "trust", "unseen")}, extra...), " ")
			}, UserRun: func() string {
				return runWithout(cmd, args, slices.Concat(scopeFlags, consentFlags)...) + " --user"
			}, Again: rerunFlags(cmd, 0)}
			a.printContext(cmd, eng, choiceScopes(scs), &req)
			// The preview's where-question is answered once and reused by the run (askWhere remembers it).
			opts := engine.Options{Targets: targets, DryRun: true, Force: force, ForceRun: forceRun(cmd, args), Display: outputFlags(cmd),
				KeepEdits: keepEdits, KeepRun: keepRun(cmd, args), AllLayers: all && profileName == "", Target: req, AskWhere: a.askWhere(a.prompter(cmd), nil), Retarget: func(module string, ts []string) string {
					run := runWithout(cmd, []string{module}, "target")
					if len(ts) == 0 {
						return run
					}
					return run + " " + errs.ShellJoin([]string{"-t", untrusted.Text(strings.Join(ts, ","), untrusted.MaxName)})
				}}
			// uninstallIn removes names, or with --all every module sc lists, from sc, as the user answers.
			// Hints name args, not names: a run with --all hints --all, which names none.
			uninstallIn := func(sc engine.Scope, several bool) (res *engine.Result, names []string, err error) {
				names = args
				if all {
					if names, err = eng.UninstallAllNames(cmd.Context(), sc, req, outputFlags(cmd)); err != nil {
						return nil, nil, err
					}
				}
				// --force answers the question, as -y does (user decision, 2026-10-22).
				if !dry && !yes && !force && len(names) > 0 {
					// The preview is a dry run, whose refusal of an @ source would hint -n: coach it as this run.
					if _, err := eng.Manifest(sc); err != nil {
						if _, ok := errors.AsType[*manifest.UnresolvedError](err); ok {
							return nil, nil, eng.CoachUnresolved(err, []engine.Scope{sc}, dry, outputFlags(cmd))
						}
					}
					preview, err := eng.Uninstall(cmd.Context(), sc, names, opts)
					if err != nil {
						return nil, nil, err
					}
					// A plan that removes no files (it only edits a profile) has nothing to confirm.
					if len(preview.Modules) > 0 {
						shown := make([]string, len(names))
						for i, n := range names {
							shown[i] = untrusted.Text(n, untrusted.MaxName)
						}
						question := "Uninstall " + strings.Join(shown, ", ")
						if several {
							question += " from " + destLabel(sc)
						}
						show := func(w io.Writer, s style) { printReport(w, s, report{Result: preview}) }
						if err := a.confirm(cmd, args, question+"?", Nav{}, preview, show); err != nil {
							return nil, nil, err
						}
					}
				}
				opts.DryRun = dry
				res, err = eng.Uninstall(cmd.Context(), sc, names, opts)
				return res, names, err
			}
			results := make([]*engine.Result, 0, len(scs))
			for _, c := range scs {
				res, names, err := uninstallIn(c.Scope, len(scs) > 1)
				if err != nil && len(results) > 0 {
					return &uninstalledScopesError{err: err, done: results, failed: c.Scope, finished: choiceScopes(scs[:len(results)])}
				}
				if err != nil {
					return err
				}
				results = append(results, res)
				human := func(w io.Writer, s style) {
					if len(scs) > 1 && !s.plain {
						fmt.Fprintln(w, destLabel(c.Scope)+":")
					}
					if len(names) == 0 {
						if !s.plain {
							fmt.Fprintln(w, s.info("nothing to uninstall"))
						}
						return
					}
					printReport(w, s, report{Result: res, Verb: "uninstalled"})
				}
				switch {
				case len(scs) == 1:
					return a.emit(cmd, human, res)
				case !a.json:
					if err := a.emit(cmd, human, nil); err != nil {
						return err
					}
				}
			}
			if a.json {
				return a.emit(cmd, nil, map[string]any{"scopes": results})
			}
			return nil
		},
	}
	a.addTargetFlag(cmd, &targets, "only uninstall from these assistants")
	cmd.Flags().BoolVarP(&dry, "dry-run", "n", false, "print the plan and write nothing")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	cmd.Flags().BoolVar(&force, "force", false, "also delete files edited since lola wrote them, without asking")
	cmd.Flags().BoolVar(&keepEdits, "keep-edits", false, "leave files edited since lola wrote them as they are, and stop managing them")
	cmd.Flags().BoolVar(&all, "all", false, "remove every module the scope lists (--profile: that profile's)")
	cmd.Flags().BoolP("verbose", "v", false, "print the report in full: every fold open and every notice")
	cmd.Flags().StringVar(&profileName, "profile", "", "remove the entry from this profile (base: the scope's base)")
	_ = cmd.RegisterFlagCompletionFunc("profile", a.completeProfiles(true))
	return cmd
}

// uninstalledScopesError is an uninstall --all over several scopes (--scope both) that stopped at
// err in failed after the scopes in finished were uninstalled: the message says so, since err speaks
// of the failed scope alone, and their results stay in the --json error document.
type uninstalledScopesError struct {
	err      error
	done     []*engine.Result
	failed   engine.Scope
	finished []engine.Scope
}

func (e *uninstalledScopesError) Error() string {
	var names []string
	for _, sc := range e.finished {
		names = append(names, destLabel(sc))
	}
	return fmt.Sprintf("%s was left as it was; %s was uninstalled: %s", destLabel(e.failed), strings.Join(names, ", "), e.err)
}
func (e *uninstalledScopesError) Unwrap() error { return e.err }

// JSONFields carries the finished scopes' results and the failed scope beside the fields of err's own document.
func (e *uninstalledScopesError) JSONFields() map[string]any {
	fields := jsonFields(e.err)
	fields["scopes"], fields["failed_scope"] = e.done, destLabel(e.failed)
	return fields
}

func (a *app) listCmd() *cobra.Command {
	var targets []string
	cmd := &cobra.Command{
		Use:     "list [module...]",
		Aliases: []string{"ls"},
		Short:   "List installed modules and their status per target",
		Long: "List installed modules per target. Status is ok; modified (a file lola wrote was edited);\n" +
			"missing (a file, or the module copy, is gone), each with how many drifted (missing 2/9);\n" +
			"stale (project.yml pins another version than the one installed: run lola sync);\n" +
			"not installed (listed in project.yml: run lola sync); or skipped (nothing of the module\n" +
			"could be rendered for the target).\n" +
			"→ update available: your cache has a newer version for the ref: lola sync --update <module>.\n" +
			"FROM, shown when a profile is involved, names where the module comes from.\n\n" +
			"Name modules to list only them and see which components drifted, with the commands that\n" +
			"restore them (lola sync) or keep them out (lola install <module> --skip kind/name).\n\n" +
			"Scope: --project, --user, -C <dir> or --scope (both included); without them, the project with\n" +
			".lola/ at or above here, if any, and user scope. list never asks; default_scope does not apply.",
		Example:           "  lola list\n  lola list --user\n  lola list --user kit\n  lola list --scope both -C ~/work/app\n  lola list --json",
		GroupID:           groupCore,
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: a.completeInstalled,
		RunE: func(cmd *cobra.Command, args []string) error {
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
			a.printContext(cmd, eng, choiceScopes(scs), nil)
			all, rows, notices := []engine.StatusRow{}, []engine.StatusRow{}, []engine.Notice{}
			installed := map[string]bool{}
			holding := map[string]bool{} // scope kinds with a row for the -t targets, whatever the module names
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
					installed[r.Name] = true
					if len(targets) == 0 || slices.Contains(targets, r.Target) {
						holding[r.Scope] = true
						all = append(all, r)
					}
				}
				notices = append(notices, notes...)
				scopes = append(scopes, c.Scope)
			}
			kinds := make([]string, len(scopes))
			for i, sc := range scopes {
				kinds[i] = sc.Kind.String()
			}
			for _, arg := range args {
				if !installed[arg] {
					hints := make([]string, len(scopes))
					for i, sc := range scopes {
						hints[i] = "list the installed modules: " + eng.Command(sc, "list")
					}
					return errs.WithHints(fmt.Errorf("%s is not installed in %s scope", untrusted.Text(arg, untrusted.MaxName), strings.Join(kinds, " or ")),
						hints...)
				}
			}
			for _, r := range all {
				if len(args) == 0 || slices.Contains(args, r.Name) {
					rows = append(rows, r)
				}
			}
			detail := len(args) > 0
			return a.emit(cmd, func(w io.Writer, s style) {
				printStatus(w, s, scopes, rows, holding, targets, a.contexts, eng.Command, detail)
				if detail && !s.plain {
					printDrift(w, s, scopes, rows, eng.Command)
				}
				for _, n := range notices {
					fmt.Fprintln(w, noticeLine(s, n))
				}
			}, map[string]any{"modules": rows, "notices": notices})
		},
	}
	a.addTargetFlag(cmd, &targets, "only show these assistants")
	return cmd
}

// printStatus prints the rows of every scope list read: a table whose rows each name their
// scope, then a line per scope in which no module at all is installed (holding names the scope
// kinds with a row for the -t targets, however the module names cut rows), coaching install (the
// command install names for a scope; the line names the -t targets when given). A drifted row's status says how many of its files drifted. Without detail
// (no module named), a line per scope with drift names the modules to list for what changed.
// --plain rows are name, target, status, rules, source, scope, from, ref, update (update-available or -).
func printStatus(w io.Writer, s style, scopes []engine.Scope, rows []engine.StatusRow, holding map[string]bool, targets []string, contexts []engine.ScopeContext, command func(engine.Scope, string, ...string) string, detail bool) {
	if s.plain {
		for _, r := range rows {
			rules := "on"
			if !r.Rules {
				rules = "off"
			}
			update := "-"
			if r.UpdateAvailable {
				update = "update-available"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.Name, r.Target, r.Status, rules, r.Source, r.Scope, r.From, untrusted.Text(r.Ref, untrusted.MaxName), update)
		}
		return
	}
	var empty []string
	for _, sc := range scopes {
		if !holding[sc.Kind.String()] {
			where := "in " + untrusted.Text(sc.ProjectDir, untrusted.MaxName)
			if sc.Kind == target.User {
				where = "at user scope"
			}
			what := "nothing installed"
			if len(targets) > 0 {
				what += " for " + untrusted.Text(strings.Join(targets, ", "), untrusted.MaxName)
			}
			empty = append(empty, what+" "+where+" (try: "+command(sc, "install")+" <source>)")
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
	tw := &ansiTable{w: w}
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
		label, paint := r.Status, s.ok
		switch r.Status {
		case "modified", "stale", "unreadable", "skipped":
			paint = s.warn
		case "not_installed":
			label, paint = "not installed", s.warn
		case "missing":
			paint = s.bad
		}
		if n := driftedFiles(r); r.Status == "missing" || r.Status == "modified" {
			switch {
			case onlyCopyMissing(r):
				label += " (module copy)"
			case n > 0:
				label += fmt.Sprintf(" %d/%d", n, r.Recorded)
			}
		}
		status := paint(label)
		if r.UpdateAvailable {
			status += " → update available"
		}
		if r.SourceGone {
			status += " " + s.warn("source gone")
		}
		rules := "on"
		if !r.Rules {
			rules = s.warn("off")
		}
		if skipped {
			rules += "\t" + strconv.Itoa(len(r.Skipped))
		}
		source := r.Source
		if r.Ref != "" {
			source += " @ " + untrusted.Text(r.Ref, untrusted.MaxName)
		}
		line := strings.Join([]string{r.Scope, r.Name, r.Target, status, rules, source}, "\t")
		if profiled {
			line += "\t" + fromLabel(r.Scope, r.From, contexts)
		}
		fmt.Fprintln(tw, line)
	}
	_ = tw.Flush()
	for _, line := range empty {
		fmt.Fprintln(w, line)
	}
	if detail {
		return
	}
	for _, sc := range scopes {
		var drifted []string
		for _, r := range rows {
			if r.Scope == sc.Kind.String() && len(r.Drift) > 0 {
				drifted = append(drifted, r.Name)
			}
		}
		slices.Sort(drifted)
		if drifted = slices.Compact(drifted); len(drifted) > 0 {
			fmt.Fprintln(w, s.info("see what changed: "+command(sc, "list", drifted...)))
		}
	}
}

// onlyCopyMissing reports whether r's module copy is gone and no other entry is missing (files
// only edited do not count), so the copy is the one thing lost.
func onlyCopyMissing(r engine.StatusRow) bool {
	copyGone := false
	for _, d := range r.Drift {
		switch {
		case d.Kind == "copy":
			copyGone = copyGone || d.Status == "missing"
		case d.Status == "missing":
			return false
		}
	}
	return copyGone
}

// driftedFiles is how many of r's files drifted, not counting its module copy.
func driftedFiles(r engine.StatusRow) int {
	n := 0
	for _, d := range r.Drift {
		if d.Kind != "copy" {
			n += d.Files
		}
	}
	return n
}

// skippable are the component kinds install --skip takes.
var skippable = []string{"skill", "command", "agent", "mcp", "rule"}

// printDrift prints, for each drifted row, a block naming its drifted components (in the
// engine's order; the engine has already cleaned their text) and the commands that restore
// them or keep the missing ones out. scopes are the scopes list read, whose flags command puts
// in each hint.
func printDrift(w io.Writer, s style, scopes []engine.Scope, rows []engine.StatusRow, command func(engine.Scope, string, ...string) string) {
	for _, r := range rows {
		if len(r.Drift) == 0 {
			continue
		}
		i := slices.IndexFunc(scopes, func(sc engine.Scope) bool { return sc.Kind.String() == r.Scope })
		if i < 0 {
			continue // no scope to build the hint commands from
		}
		sc := scopes[i]
		fmt.Fprintf(w, "%s %s %s:\n", r.Scope, untrusted.Text(r.Name, untrusted.MaxName), r.Target)
		var keepOut []string
		for _, d := range r.Drift {
			var label string
			switch {
			case d.Kind == "copy":
				label = "module copy"
			case d.Name == "":
				label = strings.TrimSpace(d.Path + " " + d.Key)
			case d.Status == "modified":
				label = d.Kind + "/" + d.Name + " (modified)"
			case d.Files > 1:
				label = fmt.Sprintf("%s/%s (%d files)", d.Kind, d.Name, d.Files)
			default:
				label = d.Kind + "/" + d.Name
			}
			if d.Status == "modified" {
				fmt.Fprintln(w, "    "+s.warn(label))
				continue
			}
			fmt.Fprintln(w, "    "+s.bad(label))
			if d.Name != "" && slices.Contains(skippable, d.Kind) {
				keepOut = append(keepOut, d.Kind+"/"+d.Name)
			}
		}
		fmt.Fprintln(w, s.info("restore: "+command(sc, "sync")))
		if len(keepOut) > 0 {
			// One --skip per component: command cuts each argument at untrusted.MaxName.
			args := []string{r.Name}
			for _, c := range keepOut {
				args = append(args, "--skip", c)
			}
			fmt.Fprintln(w, s.info("keep out: "+command(sc, "install", args...)))
		}
	}
}

// fromLabel is the name of from, a layer of scope (a list row's), as the context line names it:
// default, project.yml, profile go-review, or for a member of the bundle on in that scope docs
// (bundle work). --plain and --json keep lock.json's from.
func fromLabel(scope, from string, contexts []engine.ScopeContext) string {
	q, ok := strings.CutPrefix(from, "profile:")
	if !ok {
		return from // project.yml
	}
	if q == "default" {
		return q
	}
	for _, c := range contexts {
		if c.Scope != scope || c.Profile == nil {
			continue
		}
		if c.Profile.Name == q {
			return "profile " + c.Profile.Label
		}
		for _, m := range c.Profile.Members {
			if m.Name == q {
				return m.Label + " (bundle " + c.Profile.Label + ")"
			}
		}
	}
	return "profile " + untrusted.Text(q, untrusted.MaxName) // a layer no longer on, which lock.json still names
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
			"current directory is checked. A source is a folder, an archive or a git URL.",
		Example: "  lola check\n  lola check ./my-module -t gemini-cli\n  lola check my-module --strict\n  lola check ./review-1.2.0.zip\n" +
			"  lola check @acme/formatter",
		GroupID:           groupCore,
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: a.completeSource,
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
			// -t takes <id>@<version> as install does: each target is checked for that version.
			rerun := func(ts []string) string {
				run := completeRun(cmd, args, "target")
				if len(ts) > 0 {
					run += " -t " + errs.ShellJoin([]string{untrusted.Text(strings.Join(ts, ","), untrusted.MaxName)})
				}
				return run
			}
			ids, versions, err := splitTargets(eng, targets, false, rerun)
			if err != nil {
				return err
			}
			if err := eng.CheckVersions(versions, func(id, since string) string { return rerun(withVersions(ids, versions, id, since)) }); err != nil {
				return err
			}
			rep, err := eng.Check(cmd.Context(), sc, cwd, args, ids, versions, outputFlags(cmd))
			if err != nil {
				return err
			}
			if rep.Read {
				a.printContext(cmd, eng, []engine.Scope{sc}, nil)
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
	a.addTargetFlag(cmd, &targets, "only check these assistants; <id>@<version> checks for that version")
	cmd.Flags().BoolVar(&strict, "strict", false, "exit with an error if any target would drop, change or skip part of a module")
	return cmd
}
