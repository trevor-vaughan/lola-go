package cli

import (
	"fmt"
	"io"
	"runtime"

	"github.com/spf13/cobra"
)

func (a *app) addMiscCommands(root *cobra.Command) {
	root.AddCommand(&cobra.Command{
		Use:     "version",
		Aliases: []string{"ver"},
		Short:   "Print lola's version",
		Example: "  lola version\n  lola version --json",
		GroupID: groupAdditional,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			v := map[string]string{"version": version, "go": runtime.Version()}
			return a.emit(cmd, func(w io.Writer, s style) {
				if s.plain {
					fmt.Fprintf(w, "%s\t%s\n", version, runtime.Version())
					return
				}
				fmt.Fprintf(w, "lola %s (%s)\n", version, runtime.Version())
			}, v)
		},
	})
	shells := []string{"bash", "zsh", "fish", "powershell"}
	root.AddCommand(&cobra.Command{
		Use:       "completion bash|zsh|fish|powershell",
		Short:     "Print a shell completion script",
		Long:      "Print a completion script for your shell. It completes commands, their aliases, and flags.",
		Example:   "  source <(lola completion bash)\n  lola completion zsh > \"${fpath[1]}/_lola\"\n  lola completion fish > ~/.config/fish/completions/lola.fish",
		GroupID:   groupAdditional,
		ValidArgs: shells,
		Args:      cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
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
