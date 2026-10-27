package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// cobraNameCell is the cell cobra's usage template renders for each subcommand name.
const cobraNameCell = "{{rpad .Name .NamePadding }}"

// cobraLocalFlags is the cell cobra's usage template renders a command's own flags with.
const cobraLocalFlags = "{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}"

// cobraGlobalFlags is the block cobra's usage template renders a command's inherited flags with,
// which long help leaves to its preamble (globalFlagsBlock).
const cobraGlobalFlags = "{{if .HasAvailableInheritedFlags}}\n\nGlobal Flags:\n{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}"

// flagAliasKey is the flag annotation that lists a flag's aliases (Plan 6b §3.3).
const flagAliasKey = "lola_aliases"

// installHelp routes -h and `help` through renderHelp and makes command lists show aliases.
func installHelp(root *cobra.Command) {
	tmpl := root.UsageTemplate()
	if n := strings.Count(tmpl, cobraNameCell); n != 3 {
		panic(fmt.Sprintf("cobra's usage template changed: expected 3 %q cells, found %d", cobraNameCell, n))
	}
	if n := strings.Count(tmpl, cobraLocalFlags); n != 1 {
		panic(fmt.Sprintf("cobra's usage template changed: expected 1 %q cell, found %d", cobraLocalFlags, n))
	}
	if n := strings.Count(tmpl, cobraGlobalFlags); n != 1 {
		panic(fmt.Sprintf("cobra's usage template changed: expected 1 %q block, found %d", cobraGlobalFlags, n))
	}
	tmpl = strings.ReplaceAll(tmpl, cobraNameCell, "{{aliasedPad .}}")
	root.SetUsageTemplate(strings.ReplaceAll(tmpl, cobraLocalFlags, "{{aliasedFlagUsages .LocalFlags | trimTrailingWhitespaces}}"))
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

// aliasFlags gives cmd's flags the aliases in aliases (canonical name → aliases), defining each
// flag once (Plan 6b §3.3): pflag normalizes an alias to its canonical name before lookup, so
// cobra parses --enable as --on and Changed("on") sees either, and the aliases are kept in the
// flag's annotations for help (aliasedFlagUsages) and the published alias table.
func aliasFlags(cmd *cobra.Command, aliases map[string][]string) {
	canonical := map[string]string{}
	for name, as := range aliases {
		if err := cmd.Flags().SetAnnotation(name, flagAliasKey, as); err != nil {
			panic(err) // a flag the command does not define: a programming error caught by every spec
		}
		for _, a := range as {
			if cmd.Flags().Lookup(a) != nil {
				panic("flag alias --" + a + " is a flag of " + cmd.CommandPath()) // a programming error, as above
			}
			canonical[a] = name
		}
	}
	cmd.Flags().SetNormalizeFunc(func(_ *pflag.FlagSet, name string) pflag.NormalizedName {
		if c, ok := canonical[name]; ok {
			return pflag.NormalizedName(c)
		}
		return pflag.NormalizedName(name)
	})
}

// checkFlagAliases panics when a flag alias of c or any command under it names a flag the command
// inherits, a persistent flag of an ancestor or cobra's help flag (Plan 6b §3.3): cobra merges
// those into the command's flag set, through the command's normalize function, so the inherited
// flag would silently become the alias's flag. aliasFlags checks the command's own flags; this
// runs once the tree is built, when the inherited ones are known.
func checkFlagAliases(c *cobra.Command) {
	c.LocalFlags().VisitAll(func(f *pflag.Flag) {
		for _, a := range f.Annotations[flagAliasKey] {
			if a == "help" {
				panic("flag alias --help of " + c.CommandPath() + " is cobra's help flag") // a programming error caught by every spec
			}
			for p := c.Parent(); p != nil; p = p.Parent() {
				if p.PersistentFlags().Lookup(a) != nil {
					panic("flag alias --" + a + " of " + c.CommandPath() + " is a flag " + p.CommandPath() + " gives every command") // as above
				}
			}
		}
	})
	for _, sub := range c.Commands() {
		checkFlagAliases(sub)
	}
}

// aliasedFlagUsages is fs's usage lines with each flag's aliases after its name:
// "--on (--enable, --activate)".
func aliasedFlagUsages(fs *pflag.FlagSet) string {
	out := pflag.NewFlagSet("", pflag.ContinueOnError)
	fs.VisitAll(func(f *pflag.Flag) {
		c := *f
		if as := f.Annotations[flagAliasKey]; len(as) > 0 {
			c.Name = f.Name + " (--" + strings.Join(as, ", --") + ")"
		}
		out.AddFlag(&c)
	})
	return out.FlagUsages()
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
		writeCommandHelp(w, cmd, "")
		return
	}
	writePreamble(w, cmd.Root())
	walk(cmd, func(c *cobra.Command) {
		fmt.Fprintf(w, "\n== %s ==\n", c.CommandPath())
		writeCommandHelp(w, c, globalFlagsBlock(c))
	})
}

// writeCommandHelp prints c's description and usage, without omit, a block of the usage (""
// for none).
func writeCommandHelp(w io.Writer, c *cobra.Command, omit string) {
	desc := c.Long
	if desc == "" {
		desc = c.Short
	}
	usage := c.UsageString()
	if omit != "" {
		usage = strings.Replace(usage, omit, "", 1)
	}
	fmt.Fprintf(w, "%s\n\n%s", strings.TrimSpace(desc), usage)
}

// globalFlagsBlock is the "Global Flags:" block of c's usage as cobraGlobalFlags renders it ("" when
// c inherits none): long help prints the global flags once, in its preamble.
func globalFlagsBlock(c *cobra.Command) string {
	if !c.HasAvailableInheritedFlags() {
		return ""
	}
	return "\n\nGlobal Flags:\n" + strings.TrimRightFunc(c.InheritedFlags().FlagUsages(), unicode.IsSpace)
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
           (or --force, which never answers trust or hook consent)

ENVIRONMENT
  LOLA_COLOR=1|0   force color on or off
  NO_COLOR         disable color when set (https://no-color.org)
  LOLA_ACCESSIBLE  1 asks lola's questions and pickers as numbered line prompts (also TERM=dumb)
  LOLA_HOME        the Python lola's home, which lola migrate reads (default ~/.lola)
  HOME             user-scope root and base of the XDG defaults
  PATH             where lola finds installed assistants; absolute entries only
  COLUMNS          the width lola market <name> fits its lines to when stdout is not a terminal
  XDG_CONFIG_HOME  config.yaml, selections.yaml, markets.yaml, keep.yaml, the user-scope manifest and OpenCode's user scope (default ~/.config)
  XDG_DATA_HOME    user-scope module copies, plugin data and remote profiles (default ~/.local/share)
  XDG_STATE_HOME   the user-scope ledger, hook consent, locks and the trash of lola migrate and lola tidy (default ~/.local/state)
  XDG_CACHE_HOME   the cache, marketplace catalogs, downloads and assistant versions (default ~/.cache)
`)
}
