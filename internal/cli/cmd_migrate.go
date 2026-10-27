package cli

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/migrate"
	"github.com/trevor-vaughan/lola-go/internal/target"
)

func (a *app) migrateCmd() *cobra.Command {
	var dry, yes, force, allowHooks, allowRuntime, keep bool
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Move from the Python lola: install its modules with lola and clean up",
		Long: "Read the Python lola's state ($LOLA_HOME, else ~/.lola), install every module it\n" +
			"installed for claude-code, gemini-cli or opencode again with lola, at the same\n" +
			"projects and user scope, and clean up after the Python lola.\n" +
			"\n" +
			"A file the Python lola recorded is treated as its own: it is copied into the trash\n" +
			"($XDG_STATE_HOME/lola/trash/<time>/, with manifest.txt) before lola installs over it,\n" +
			"or moved there when lola does not write it. Restoring is a move back. Instruction\n" +
			"blocks, MCP entries lola does not write, .lola-req and installs for other assistants\n" +
			"are listed, never changed. Each enabled marketplace of the Python lola is registered\n" +
			"with lola market (its catalog fetched); one lola cannot register is listed with the\n" +
			"lola market <name> --add command that would, when one can. The Python lola's home goes\n" +
			"to the trash last, when\n" +
			"nothing is left in it. Symbolic links and files git tracks are kept.\n" +
			"\n" +
			"lola migrate shows the plan and asks (default No); off a terminal it needs -y.\n" +
			"--force also overwrites other files in the way and moves files git tracks, and answers\n" +
			"the question as -y does (never hook consent). A second run has nothing to do.",
		Example: "  lola migrate --dry-run\n  lola migrate\n  lola migrate -y --keep-python",
		GroupID: groupAdditional,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			eng, err := a.engine()
			if err != nil {
				return err
			}
			home, err := migrate.Home(a.getenv, eng.Paths.Home)
			if err != nil {
				return err
			}
			st, err := migrate.Load(home, eng.Paths.Home)
			if err != nil {
				return err
			}
			p, err := migrate.NewPlan(cmd.Context(), eng, st, outputFlags(cmd))
			if err != nil {
				return err
			}
			opts := migrate.Options{KeepPython: keep}
			opts.Force, opts.DryRun, opts.ForceRun, opts.Display = force, true, forceRun(cmd, nil), outputFlags(cmd)
			preview, err := migrate.Migrate(cmd.Context(), eng, p, opts)
			if err != nil {
				return err
			}
			if dry || preview.Nothing {
				return a.emit(cmd, func(w io.Writer, s style) { printMigration(w, s, preview) }, preview)
			}
			// --force answers the question, as -y does; never the hooks' consent (ruling R-6c3-4).
			if !yes && !force {
				show := func(w io.Writer) { printMigration(w, style{}, preview) }
				if err := a.confirm(cmd, nil, "Migrate?", preview, show); err != nil {
					return err
				}
			}
			opts.DryRun = false
			a.hookOptions(cmd, &opts.Options, allowHooks, allowRuntime)
			res, err := migrate.Migrate(cmd.Context(), eng, p, opts)
			if res == nil {
				return err
			}
			if emitErr := a.emit(cmd, func(w io.Writer, s style) { printMigration(w, s, res) }, res); emitErr != nil {
				return emitErr
			}
			return err
		},
	}
	f := cmd.Flags()
	f.BoolVarP(&dry, "dry-run", "n", false, "print the plan and write nothing")
	f.BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	f.BoolVar(&force, "force", false, "also replace, after backing each up into the trash, unowned files outside the recorded paths, recorded files git tracks or that lie below a symbolic link, and module copies; and move files git tracks, without asking (never answers hook consent)")
	f.BoolVar(&allowHooks, "allow-hooks", false, "run the modules' install hooks without asking (consent for this run)")
	f.BoolVar(&allowRuntime, "allow-runtime-hooks", false, "install the modules' Claude Code runtime hooks without asking (consent is recorded)")
	f.BoolVar(&keep, "keep-python", false, "install and replace with backup, but move nothing else to the trash")
	return cmd
}

// migrationLabel names a migration destination in human output.
func migrationLabel(d *migrate.DestResult) string {
	if d.Root == "" {
		return "user scope"
	}
	return "project " + d.Root
}

// destColumn names a destination in --plain output.
func destColumn(d *migrate.DestResult) string {
	if d.Root == "" {
		return target.User.String()
	}
	return d.Root
}

// printMigration renders a migration: per destination what is installed, replaced, moved,
// kept, reported and, after a run, whether it migrated or failed; then the Python home and the trash.
// Every string from the Python lola's state was made safe to print by migrate.
func printMigration(w io.Writer, s style, r *migrate.Result) {
	if s.plain {
		printMigrationPlain(w, r)
		return
	}
	switch {
	case r.Nothing:
		fmt.Fprintln(w, "nothing to migrate")
	case r.DryRun:
		fmt.Fprintln(w, "plan (dry run; nothing was written):")
	}
	for _, d := range r.Destinations {
		fmt.Fprintln(w, migrationLabel(d)+":")
		for _, in := range d.Installs {
			line := in.Module
			if line == "" || !slices.Equal(in.PythonModules, []string{in.Module}) {
				line = strings.TrimSpace(line + " (Python lola: " + strings.Join(in.PythonModules, ", ") + ")")
			}
			line += " (" + strings.Join(in.Targets, ", ") + ") from " + in.Source
			if in.Status == migrate.InstallAlready {
				line = "already migrated: " + line
			} else {
				line = "install " + line
			}
			fmt.Fprintln(w, "  "+line)
		}
		for _, st := range d.Steps {
			fmt.Fprintln(w, "    "+stepLine(s, st))
		}
		for _, m := range d.Replaced {
			where := "the trash"
			if m.Trash != "" {
				where = m.Trash
			}
			fmt.Fprintf(w, "  %-7s %s (backed up to %s)\n", "replace", shownPath(m), where)
		}
		for _, m := range d.Moved {
			where := "the trash"
			if m.Trash != "" {
				where = m.Trash
			}
			fmt.Fprintf(w, "  %-7s %s to %s\n", "move", m.Path, where)
		}
		for _, k := range d.Kept {
			fmt.Fprintf(w, "  %-7s %s: %s\n", "keep", k.Path, k.Reason)
		}
		printReported(w, "  ", d.NotMigrated, d.Unmappable)
		for _, f := range d.Findings {
			if f.Severity == target.SeverityInfo && f.Hint == "" {
				continue
			}
			if line := findingLine(s, f); line != "" {
				fmt.Fprintln(w, "  "+line)
			}
		}
		for _, n := range d.Notices {
			fmt.Fprintln(w, "  "+noticeLine(s, n))
		}
		switch d.Status {
		case migrate.StatusMigrated:
			fmt.Fprintln(w, s.ok("migrated "+migrationLabel(d)))
		case migrate.StatusFailed:
			fmt.Fprintln(w, s.bad("failed "+migrationLabel(d)+": "+d.Error))
			printHints(w, "    ", d.Hints)
		}
	}
	printReported(w, "", r.NotMigrated, r.Unmappable)
	printMigratedMarkets(w, s, r.Markets)
	for _, warn := range r.Warnings {
		fmt.Fprintln(w, s.warn(warn))
	}
	home := "the Python lola's home " + r.Home.Path
	switch {
	case r.Home.Action == migrate.HomeMove && r.Home.Trash != "":
		fmt.Fprintln(w, s.ok(home+" moved to "+r.Home.Trash))
	case r.Home.Action == migrate.HomeMove:
		fmt.Fprintln(w, s.info(home+" moves to the trash"))
	case r.Home.Action == migrate.HomeKeep:
		fmt.Fprintln(w, s.info(home+" stays: "+r.Home.Reason))
	}
	if r.Trash != "" {
		fmt.Fprintln(w, "trash: "+r.Trash)
	}
}

// printMigratedMarkets lists what becomes of the Python lola's marketplaces (Plan 6c §6.9), each one
// not registered with its reason and hints.
func printMigratedMarkets(w io.Writer, s style, ms []migrate.Market) {
	if len(ms) == 0 {
		return
	}
	fmt.Fprintln(w, "marketplaces:")
	for _, m := range ms {
		from := m.Source
		if m.Path != "" {
			from += " path " + m.Path
		}
		switch m.Status {
		case migrate.MarketRegister:
			fmt.Fprintln(w, "  register "+m.Market+" from "+from)
		case migrate.MarketRegistered:
			fmt.Fprintln(w, "  "+s.ok("registered "+m.Market+" from "+from))
		case migrate.MarketAlready:
			fmt.Fprintln(w, "  already registered: "+m.Market+" from "+from)
		case migrate.MarketSkipped:
			fmt.Fprintln(w, "  not registered: "+m.Name+" ("+m.URL+"): "+m.Reason)
		case migrate.MarketFailed:
			fmt.Fprintln(w, "  "+s.bad("failed "+m.Name+" ("+m.URL+"): "+m.Reason))
		}
		printHints(w, "      ", m.Hints)
	}
}

// shownPath is a replaced path, with the JSON pointer of an MCP entry.
func shownPath(m migrate.Moved) string {
	if m.Key != "" {
		return m.Path + " " + m.Key
	}
	return m.Path
}

// printReported lists what is never migrated and the records lola cannot migrate, each with
// its hint.
func printReported(w io.Writer, indent string, items []migrate.Item, unmapped []migrate.Unmapped) {
	for _, it := range items {
		line := "not migrated: " + it.What + " " + it.Path
		if it.Marker != "" {
			line += " " + it.Marker
		}
		fmt.Fprintf(w, "%s%s\n", indent, line)
		printHints(w, indent+"    ", it.Hints)
	}
	for _, u := range unmapped {
		line := "unmappable " + u.Module + " (" + u.Assistant
		if indent == "" && u.ProjectPath != "" {
			line += " at " + u.ProjectPath
		}
		fmt.Fprintf(w, "%s%s): %s\n", indent, line, u.Reason)
		printHints(w, indent+"    ", u.Hints)
	}
}

// printHints prints each hint on a line of its own.
func printHints(w io.Writer, indent string, hints []string) {
	for _, h := range hints {
		fmt.Fprintf(w, "%shint: %s\n", indent, h)
	}
}

// printMigrationPlain renders a migration as tab-separated lines: one per install, step,
// replacement, move, kept path, reported item and record, destination status, marketplace
// (its status, Python name, lola name, source, path and reason), then the home,
// the trash and, when there is nothing to do, "nothing".
func printMigrationPlain(w io.Writer, r *migrate.Result) {
	row := func(cols ...string) { fmt.Fprintln(w, strings.Join(cols, "\t")) }
	for _, d := range r.Destinations {
		at := destColumn(d)
		for _, in := range d.Installs {
			kind := "install"
			if in.Status == migrate.InstallAlready {
				kind = "already-migrated"
			}
			row(kind, at, in.Module, strings.Join(in.Targets, ","), in.Source)
		}
		for _, st := range d.Steps {
			row("step", at, stepLine(style{plain: true}, st))
		}
		for _, m := range d.Replaced {
			row("replace", at, m.Path, m.Key, m.Trash)
		}
		for _, m := range d.Moved {
			row("move", at, m.Path, m.Key, m.Trash)
		}
		for _, k := range d.Kept {
			row("keep", at, k.Path, k.Reason)
		}
		for _, it := range d.NotMigrated {
			row("not-migrated", at, it.What, it.Path, it.Marker)
		}
		for _, u := range d.Unmappable {
			row("unmappable", at, u.Module, u.Assistant, u.Reason)
		}
		row("status", at, d.Status, d.Error)
	}
	for _, it := range r.NotMigrated {
		row("not-migrated", "", it.What, it.Path, it.Marker)
	}
	for _, u := range r.Unmappable {
		row("unmappable", u.ProjectPath, u.Module, u.Assistant, u.Reason)
	}
	for _, m := range r.Markets {
		row("market", m.Status, m.Name, m.Market, m.Source, m.Path, m.Reason)
	}
	row("home", r.Home.Action, r.Home.Path, r.Home.Reason, r.Home.Trash)
	if r.Trash != "" {
		row("trash", r.Trash)
	}
	if r.Nothing {
		row("nothing")
	}
}
