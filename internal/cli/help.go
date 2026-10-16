package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

// cobraNameCell is the cell cobra's usage template renders for each subcommand name.
const cobraNameCell = "{{rpad .Name .NamePadding }}"

// installHelp routes -h and `help` through renderHelp and makes command lists show aliases.
func installHelp(root *cobra.Command) {
	tmpl := root.UsageTemplate()
	if n := strings.Count(tmpl, cobraNameCell); n != 3 {
		panic(fmt.Sprintf("cobra's usage template changed: expected 3 %q cells, found %d", cobraNameCell, n))
	}
	root.SetUsageTemplate(strings.ReplaceAll(tmpl, cobraNameCell, "{{aliasedPad .}}"))
	root.SetHelpFunc(func(cmd *cobra.Command, _ []string) { renderHelp(cmd, !isTTY(cmd.OutOrStdout())) })

	var long, short bool
	help := &cobra.Command{
		Use:     "help [command]",
		Short:   "Show help for lola or a command",
		Long:    "Show help. On a terminal it is concise; piped or redirected, it prints the full command tree.",
		Example: "  lola help install\n  lola help --long | less",
		GroupID: groupAdditional,
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
			c, _, err := cmd.Root().Find(args)
			if err != nil {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			var out []cobra.Completion
			for _, sub := range c.Commands() {
				if sub.IsAvailableCommand() && strings.HasPrefix(sub.Name(), toComplete) {
					out = append(out, sub.Name())
				}
			}
			return out, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if long && short {
				return errors.New("--long and --short cannot be combined")
			}
			c, rest, err := cmd.Root().Find(args)
			if err != nil || len(rest) > 0 {
				return fmt.Errorf("no help topic %q; see: lola help", strings.Join(args, " "))
			}
			mode := !isTTY(cmd.OutOrStdout())
			if long || short {
				mode = long
			}
			renderHelp(c, mode)
			return nil
		},
	}
	help.Flags().BoolVarP(&long, "long", "l", false, "print the full command tree")
	help.Flags().BoolVarP(&short, "short", "s", false, "print concise help")
	root.SetHelpCommand(help)
}

// aliasedName is a command's name followed by its aliases: "uninstall (remove, rm)".
func aliasedName(c *cobra.Command) string {
	if len(c.Aliases) == 0 {
		return c.Name()
	}
	return c.Name() + " (" + strings.Join(c.Aliases, ", ") + ")"
}

// aliasedPad pads aliasedName to the widest sibling so command lists stay aligned.
func aliasedPad(c *cobra.Command) string {
	width := len(aliasedName(c))
	if p := c.Parent(); p != nil {
		for _, s := range p.Commands() {
			if s.IsAvailableCommand() && len(aliasedName(s)) > width {
				width = len(aliasedName(s))
			}
		}
	}
	return fmt.Sprintf("%-*s", width, aliasedName(c))
}

// aliasCompletions completes subcommand aliases, which cobra itself never offers.
func aliasCompletions(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []cobra.Completion
	for _, sub := range cmd.Commands() {
		if !sub.IsAvailableCommand() {
			continue
		}
		for _, alias := range sub.Aliases {
			if strings.HasPrefix(alias, toComplete) && !strings.HasPrefix(sub.Name(), toComplete) {
				out = append(out, cobra.CompletionWithDesc(alias, sub.Short))
			}
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// renderHelp is the single help path for -h and `help`. Concise help is the description
// plus cobra's usage; long help is a preamble plus every command below cmd.
func renderHelp(cmd *cobra.Command, long bool) {
	w := cmd.OutOrStdout()
	if !long {
		writeCommandHelp(w, cmd)
		return
	}
	writePreamble(w, cmd.Root())
	walk(cmd, func(c *cobra.Command) {
		fmt.Fprintf(w, "\n== %s ==\n", c.CommandPath())
		writeCommandHelp(w, c)
	})
}

func writeCommandHelp(w io.Writer, c *cobra.Command) {
	desc := c.Long
	if desc == "" {
		desc = c.Short
	}
	fmt.Fprintf(w, "%s\n\n%s", strings.TrimSpace(desc), c.UsageString())
}

func walk(c *cobra.Command, fn func(*cobra.Command)) {
	fn(c)
	for _, sub := range c.Commands() {
		if sub.IsAvailableCommand() || sub.Name() == "help" {
			walk(sub, fn)
		}
	}
}

func writePreamble(w io.Writer, root *cobra.Command) {
	fmt.Fprintf(w, "GLOBAL FLAGS\n%s\n", root.PersistentFlags().FlagUsages())
	fmt.Fprint(w, `OUTPUT CONTRACT
  --json   one JSON document on stdout; snake_case keys; array keys are always present ([] when empty)
  --plain  tab-separated lines on stdout, no color
  errors   "error: <message>" on stderr plus indented "hint:" lines;
           under --json a single line {"error": "...", "hints": [...]}
  exit     0 on success or a cancelled prompt, 1 on any error
  prompts  go to stderr; without a terminal, destructive commands need -y

ENVIRONMENT
  LOLA_COLOR=1|0   force color on or off
  NO_COLOR         disable color when set (https://no-color.org)
  HOME             user-scope root and base of the XDG defaults
  PATH             where lola finds installed assistants; absolute entries only
  XDG_CONFIG_HOME  config, the user-scope manifest and OpenCode's user scope (default ~/.config)
  XDG_DATA_HOME    user-scope module copies and plugin data (default ~/.local/share)
  XDG_STATE_HOME   the user-scope ledger, hook consent and locks (default ~/.local/state)
  XDG_CACHE_HOME   the cache, downloads and assistant versions (default ~/.cache)
`)
}
