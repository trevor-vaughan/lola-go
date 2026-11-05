package cli

import (
	"github.com/spf13/cobra"
)

// updateCmd is lola update (spec 2026-10-29 §3.1): lola sync's run over the modules installed in
// the scope, each moved to the version its source holds now, installing and removing nothing.
func (a *app) updateCmd() *cobra.Command {
	f := syncFlags{installed: true}
	var trust trustFlag
	cmd := &cobra.Command{
		Use:     "update [<module>...]",
		Aliases: []string{"upgrade"},
		Short:   "Update the installed modules from their sources",
		Long: "Move every installed module (those named, if any) to the version its source holds now and\n" +
			"render it. A ref fetched less than cache_fresh_for: ago (in config.yaml; one day unless it\n" +
			"says otherwise) is served from your cache; --fetch fetches every ref now. update installs\n" +
			"nothing and removes nothing: a module no layer lists any more, or whose entry asks for other\n" +
			"assistants, stays as installed and is reported (lola sync applies such changes). A module\n" +
			"whose fetch fails stays as installed, and the run fails after updating the rest.\n" +
			"New versions are shown and asked about as lola sync asks; off a terminal, --trust accepts\n" +
			"them. --force never answers that question.\n" +
			"To render again at the installed versions, fetching nothing: lola sync.\n" +
			trustHelp("update") + "\n\n" + scopeHelp + "\n" +
			"--scope both updates the project, then user scope, and stops at the first that fails or is\n" +
			"declined.",
		Example:           "  lola update\n  lola update -n\n  lola update review-kit --fetch\n  lola update --scope both --trust",
		GroupID:           groupCore,
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: a.completeSync,
		RunE:              a.syncRun(&f, &trust, "update", "Update where?"),
	}
	cmd.Flags().BoolVarP(&f.dry, "dry-run", "n", false, "print the plan and the trust diff, ask nothing and write nothing")
	cmd.Flags().BoolVar(&f.refetch, "fetch", false, "fetch every ref now, however recently it was fetched")
	cmd.Flags().BoolVar(&f.force, "force", false, "overwrite unowned or hand-edited files and render skipped artifacts that can be (never answers a consent question)")
	cmd.Flags().BoolVar(&f.strict, "strict", false, "fail without writing anything if any target would drop, change or skip part of a module")
	addTrustFlag(cmd, &trust, "update", "trust these kinds without asking: entries, profiles, scripts, hooks, runtime-hooks or all; bare: entries,profiles (recorded as a yes is; never bypasses a refusal)")
	// dnf and zypper's --refresh (Plan 6c §3.2); additive and stable, the published table is
	// pinned in store_test.go.
	aliasFlags(cmd, map[string][]string{"fetch": {"refresh"}})
	return cmd
}
