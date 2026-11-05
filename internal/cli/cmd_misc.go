package cli

import (
	"errors"
	"fmt"
	"io"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// printVersion prints lola's version and the Go it was built with: lola version, and lola
// --version, its alias.
func (a *app) printVersion(cmd *cobra.Command) error {
	v := map[string]string{"version": version, "go": runtime.Version()}
	return a.emit(cmd, func(w io.Writer, s style) {
		if s.plain {
			fmt.Fprintf(w, "%s\t%s\n", version, runtime.Version())
			return
		}
		fmt.Fprintf(w, "lola %s (%s)\n", version, runtime.Version())
	}, v)
}

func (a *app) addMiscCommands(root *cobra.Command) {
	root.AddCommand(a.migrateCmd(), a.repairCmd(), a.trashCmd())
	root.AddCommand(&cobra.Command{
		Use:               "version",
		Aliases:           []string{"ver"},
		Short:             "Print lola's version",
		Example:           "  lola version\n  lola version --json",
		GroupID:           groupAdditional,
		Args:              cobra.NoArgs,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.printVersion(cmd)
		},
	})
	shells := []string{"bash", "zsh", "fish", "powershell"}
	root.AddCommand(&cobra.Command{
		Use:   "completion bash|zsh|fish|powershell",
		Short: "Print a shell completion script",
		Long: "Print a completion script for your shell. It completes commands, their aliases, and flags,\n" +
			"and the values lola knows: module names from your cache and this scope (plus file names, for\n" +
			"install and check, which take paths); a module's components for --skip, --only and --unskip;\n" +
			"profile names for --profile, --to, --from and --bundle; the modules --move can move;\n" +
			"<module>= and the installed version for --at; trash runs, newest first, then a run's files;\n" +
			"and for -t, the assistants found on this machine first. Completion reads local files only:\n" +
			"it writes nothing and never uses the network. --ref and other free-text values offer nothing;\n" +
			"--path and -C offer directories.\n\n" +
			"zsh loads _lola from a directory on fpath when compinit runs: add fpath=(~/.zfunc $fpath) to ~/.zshrc\n" +
			"before autoload -Uz compinit && compinit.",
		Example:   "  source <(lola completion bash)\n  mkdir -p ~/.zfunc && lola completion zsh > ~/.zfunc/_lola\n  lola completion fish > ~/.config/fish/completions/lola.fish",
		GroupID:   groupAdditional,
		ValidArgs: shells,
		Args: func(_ *cobra.Command, args []string) error {
			var problem string
			switch {
			case len(args) == 0:
				problem = "lola completion needs a shell: " + strings.Join(shells, ", ")
			case len(args) > 1:
				problem = "lola completion takes one shell, and " + strconv.Itoa(len(args)) + " were given"
			case !slices.Contains(shells, args[0]):
				problem = fmt.Sprintf("%s is not a shell lola completes: %s", untrusted.Text(args[0], untrusted.MaxName), strings.Join(shells, ", "))
			default:
				return nil
			}
			return errs.WithHints(errors.New(problem), "source <(lola completion bash)", "lola completion --help")
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			w := cmd.OutOrStdout()
			switch args[0] {
			case "bash":
				return root.GenBashCompletionV2(w, true)
			case "zsh":
				return root.GenZshCompletion(w)
			case "fish":
				return root.GenFishCompletion(w, true)
			default:
				return root.GenPowerShellCompletionWithDesc(w)
			}
		},
	})
}
