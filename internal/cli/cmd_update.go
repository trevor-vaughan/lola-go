package cli

import (
	"github.com/spf13/cobra"
)

// updateCmd is lola update: lola sync's run over the modules installed in
// the scope, each moved to the version its source holds now, installing and removing no module;
// the files a new version no longer ships are removals, confirmed as sync's are (-y).
func (a *app) updateCmd() *cobra.Command {
	f := syncFlags{installed: true}
	var trust trustFlag
	cmd := &cobra.Command{
		Use:     "update [<module>...]",
		Aliases: []string{"upgrade"},
		Short:   "Update the installed modules from their sources",
		Long: "Move every installed module (those named, if any) to the version its source holds now and\n" +
			"render it. A ref fetched within cache_fresh_for: (config.yaml; one day unless it says otherwise)\n" +
			"is served from your cache; --fetch fetches every ref now. update installs and removes no module:\n" +
			"a module no layer lists any more stays as installed and is reported (lola sync applies such changes).\n" +
			"Files a new version no longer ships are listed as removals and asked about on a terminal; off\n" +
			"one, -y (or --force) confirms them. New versions are shown and asked about as lola sync asks;\n" +
			"off a terminal, --trust accepts them, and --force never does. To render again at the installed\n" +
			"versions, fetching nothing: lola sync.\n" +
			"\n" + scopeHelp,
		Example:           "  lola update\n  lola update -n\n  lola update review-kit --fetch\n  lola update --scope both --trust",
		GroupID:           groupCore,
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: a.completeSync,
		RunE:              a.syncRun(&f, &trust, "update", "Update where?"),
	}
	cmd.Flags().BoolVarP(&f.dry, "dry-run", "n", false, "print the plan and the trust diff, ask nothing and write nothing")
	cmd.Flags().BoolVarP(&f.yes, "yes", "y", false, "open no picker; confirm removing files a new version drops (never trusts)")
	cmd.Flags().BoolVar(&f.refetch, "fetch", false, "fetch every ref now, however recently it was fetched")
	cmd.Flags().StringArrayVar(&f.at, "at", nil, atUsage)
	_ = cmd.RegisterFlagCompletionFunc("at", a.completeAt)
	cmd.Flags().BoolVar(&f.force, "force", false, "overwrite unowned or hand-edited files, render skipped, confirm removals")
	cmd.Flags().BoolVar(&f.strict, "strict", false, "fail, writing nothing, if a target would drop, change or skip part of a module")
	cmd.Flags().BoolP("verbose", "v", false, "print the report in full: every fold open and every notice")
	addTrustFlag(cmd, &trust, "update", "trust without asking: entries, profiles, scripts, hooks, runtime-hooks, all")
	// dnf and zypper's --refresh; additive and stable, the published table is
	// pinned in store_test.go.
	aliasFlags(cmd, map[string][]string{"fetch": {"refresh"}})
	return cmd
}
