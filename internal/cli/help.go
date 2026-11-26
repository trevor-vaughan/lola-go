package cli

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"runtime/debug"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/trevor-vaughan/lola-go/internal/errs"
)

// cobraNameCell is the cell cobra's usage template renders for each subcommand name.
const cobraNameCell = "{{rpad .Name .NamePadding }}"

// cobraLocalFlags is the cell cobra's usage template renders a command's own flags with.
const cobraLocalFlags = "{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}"

// cobraGlobalFlags is the block cobra's usage template renders a command's inherited flags with,
// which long help leaves to its preamble (globalFlagsBlock).
const cobraGlobalFlags = "{{if .HasAvailableInheritedFlags}}\n\nGlobal Flags:\n{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}"

// flagAliasKey is the flag annotation that lists a flag's aliases.
const flagAliasKey = "lola_aliases"

// helpForm is which help a command prints.
type helpForm int

const (
	helpShort helpForm = iota // -h: Short and the usage, without the global flags
	helpLong                  // --help: Long, the full usage and the reference link
	helpAll                   // help --all: the command and every command below it, long
)

// releaseTag is a version task build stamps on a release: the tag, exactly.
var releaseTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// majorSuffix is a Go module path's major-version element (/v2), not part of the repository URL.
var majorSuffix = regexp.MustCompile(`^v[0-9]+$`)

// helpEnv is one environment variable lola reads, as the root's long help lists it.
type helpEnv struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// environment is the environment variables lola reads, in the order the root's long help lists
// them; prose (writeLongHelp) and --json (helpDoc) both read it.
func environment() []helpEnv {
	return []helpEnv{
		{"LOLA_COLOR", "1 or 0 forces color on or off"},
		{"NO_COLOR", "disable color when set (https://no-color.org)"},
		{"LOLA_ACCESSIBLE", "1 asks lola's questions and pickers as numbered line prompts and prints without the viewer (also TERM=dumb)"},
		{"LOLA_PAGER", "the pager for output taller than the terminal, instead of lola's viewer; cat prints it"},
		{"NO_PAGER", "set to a non-empty value, prints without the viewer or a pager"},
		{"LOLA_HOME", "the directory lola migrate reads (default ~/.lola)"},
		{"HOME", "user-scope root and base of the XDG defaults"},
		{"PATH", "where lola finds installed assistants; absolute entries only"},
		{"COLUMNS", "the width lola market <name> fits its lines to when stdout is not a terminal"},
		{"LINES", "the height lola's viewer takes the terminal to have when it cannot ask the terminal"},
		{"XDG_CONFIG_HOME", "config.yaml, selections.yaml, markets.yaml, keep.yaml, the user-scope manifest and OpenCode's user scope (default ~/.config)"},
		{"XDG_DATA_HOME", "user-scope module copies, plugin data and remote profiles (default ~/.local/share)"},
		{"XDG_STATE_HOME", "the user-scope ledger, hook consent, locks and the trash of lola migrate and lola tidy (default ~/.local/state)"},
		{"XDG_CACHE_HOME", "the cache, marketplace catalogs, downloads and assistant versions (default ~/.cache)"},
	}
}

// installHelp routes -h and `help` through renderHelp, shown as other output is (page), and makes
// command lists show aliases.
func (a *app) installHelp(root *cobra.Command) {
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
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		form := helpShort
		if longHelpAsked(cmd, args) {
			form = helpLong
		}
		// cobra's help func returns nothing, so a failure to write help (a closed stdout) is
		// reported here and the run still exits 0.
		if err := a.showHelp(cmd, form); err != nil {
			writeError(cmd.ErrOrStderr(), err, a.json, 0)
		}
	})

	var all, short bool
	help := &cobra.Command{
		Use:     "help [command]",
		Short:   "Show help for lola or a command",
		Long:    "Show a command's long help, as --help does. --short prints its short help, as -h does;\n--all prints it and every command below it.",
		Example: "  lola help install\n  lola help --short install\n  lola help --all | less",
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
			if all && short {
				return errs.WithHints(errors.New("--all and --short cannot be combined"),
					"pick one: lola help --all, or lola help --short <command>")
			}
			c, rest, err := cmd.Root().Find(args)
			if err != nil || len(rest) > 0 {
				return errs.WithHints(fmt.Errorf("no help topic %q", strings.Join(args, " ")), "see: lola help")
			}
			form := helpLong
			switch {
			case all:
				form = helpAll
			case short:
				form = helpShort
			}
			return a.showHelp(c, form)
		},
	}
	help.Flags().BoolVar(&all, "all", false, "print the command and every command below it")
	help.Flags().BoolVar(&short, "short", false, "print short help, as -h does")
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
// flag once: pflag normalizes an alias to its canonical name before lookup, so
// cobra parses --create as --new and Changed("new") sees either, and the aliases are kept in the
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
// inherits, a persistent flag of an ancestor or cobra's help flag: cobra merges
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
		if shown := f.Annotations[trustShownKey]; len(shown) > 0 {
			// The empty back-quoted name drops pflag's value placeholder: --trust shows its own.
			c.Name, c.NoOptDefVal, c.Usage = c.Name+shown[0], "", "``"+c.Usage
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

// longHelpAsked reports whether args, the arguments cobra's help function received, ask for
// long help (--help) rather than short (-h). cobra parses both into one flag, so this walks
// them as pflag does: a flag's value is never read as a flag (-t --help, --ref --help), and
// nothing after "--" is one.
func longHelpAsked(cmd *cobra.Command, args []string) bool {
	fs := cmd.Flags()
	takesValue := func(f *pflag.Flag) bool { return f != nil && f.NoOptDefVal == "" }
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			return false
		case strings.HasPrefix(arg, "--"):
			name, _, attached := strings.Cut(arg[2:], "=")
			if name == "help" {
				return true
			}
			if !attached && takesValue(fs.Lookup(name)) {
				i++
			}
		case len(arg) > 1 && arg[0] == '-':
			letters := arg[1:]
			for j := 0; j < len(letters); j++ {
				if letters[j] >= utf8.RuneSelf {
					break // pflag refuses it before help runs; ShorthandLookup panics on it
				}
				if takesValue(fs.ShorthandLookup(letters[j : j+1])) {
					if j == len(letters)-1 {
						i++ // the value is the next argument
					}
					break // else the value is the rest of the cluster
				}
			}
		}
	}
	return false
}

// showHelp is the single help path for -h, --help, `help` and a bare command group: under --json
// the help document (helpDoc) through emit, else renderHelp through page, so help taller than the
// terminal opens in the viewer.
func (a *app) showHelp(cmd *cobra.Command, form helpForm) error {
	if a.json {
		return a.emit(cmd, nil, helpDoc(cmd, form))
	}
	a.page(cmd, cmd.OutOrStdout(), style{}, func(w io.Writer, _ style) { renderHelp(w, cmd, form) })
	return nil
}

// renderHelp writes cmd's help in form to w.
func renderHelp(w io.Writer, cmd *cobra.Command, form helpForm) {
	switch form {
	case helpShort:
		cmd.InitDefaultHelpFlag() // cobra adds -h/--help on execution; help, bare groups and --all reach here before it did
		fmt.Fprintf(w, "%s\n\n%s", strings.TrimSpace(cmd.Short), strings.Replace(cmd.UsageString(), globalFlagsBlock(cmd), "", 1))
		fmt.Fprintf(w, "\nMore: %s --help\n", cmd.CommandPath())
	case helpLong:
		writeLongHelp(w, cmd, "")
	case helpAll:
		// The global flags print once: the root's are its own Flags; below it, they lead.
		if block := globalFlagsBlock(cmd); block != "" {
			fmt.Fprintf(w, "%s\n", strings.TrimPrefix(block, "\n\n"))
		}
		walk(cmd, func(c *cobra.Command) {
			fmt.Fprintf(w, "\n== %s ==\n", c.CommandPath())
			writeLongHelp(w, c, globalFlagsBlock(c))
		})
	}
}

// writeLongHelp prints c's long description, its usage without omit (a block of the usage; ""
// for none), the root's environment variables, and the link to c's reference page.
func writeLongHelp(w io.Writer, c *cobra.Command, omit string) {
	c.InitDefaultHelpFlag()
	desc := c.Long
	if desc == "" {
		desc = c.Short
	}
	usage := c.UsageString()
	if omit != "" {
		usage = strings.Replace(usage, omit, "", 1)
	}
	fmt.Fprintf(w, "%s\n\n%s", strings.TrimSpace(desc), usage)
	if c == c.Root() {
		env := environment()
		width := 0
		for _, e := range env {
			width = max(width, len(e.Name))
		}
		fmt.Fprint(w, "\nEnvironment:\n")
		for _, e := range env {
			fmt.Fprintf(w, "  %-*s  %s\n", width, e.Name, e.Description)
		}
	}
	if u := buildReferenceURL(c); u != "" {
		fmt.Fprintf(w, "\nReference: %s\n", u)
	}
}

// referenceURL is the web page documenting c, derived from modulePath, the module lola was built
// from (so it follows the repository when it moves), and ver, the version task build stamps: a
// release links to its own tag's docs, anything else to main. It assumes GitHub's /blob/ layout.
// "" when the module path is unknown.
func referenceURL(c *cobra.Command, modulePath, ver string) string {
	if modulePath == "" {
		return ""
	}
	if i := strings.LastIndex(modulePath, "/"); i > 0 && majorSuffix.MatchString(modulePath[i+1:]) {
		modulePath = modulePath[:i]
	}
	ref := "main"
	if releaseTag.MatchString(ver) {
		ref = ver
	}
	page := "usage.md"
	if c != c.Root() {
		page = "reference/" + strings.Fields(c.CommandPath())[1] + ".md"
	}
	return "https://" + modulePath + "/blob/" + ref + "/docs/" + page
}

// buildReferenceURL is referenceURL for the running build: "" when it records no build info.
func buildReferenceURL(c *cobra.Command) string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	ver := version
	if !releaseTag.MatchString(ver) && releaseTag.MatchString(bi.Main.Version) {
		ver = bi.Main.Version // go install …@vX.Y.Z stamps no version, but records the module's
	}
	return referenceURL(c, bi.Main.Path, ver)
}

// globalFlagsBlock is the "Global Flags:" block of c's usage as cobraGlobalFlags renders it ("" when
// c inherits none): long help prints the global flags once, in its preamble.
func globalFlagsBlock(c *cobra.Command) string {
	if !c.HasAvailableInheritedFlags() {
		return ""
	}
	return "\n\nGlobal Flags:\n" + strings.TrimRightFunc(c.InheritedFlags().FlagUsages(), unicode.IsSpace)
}

// helpListed reports whether help lists c among its parent's commands: cobra's usage template
// lists the available ones and help, and prose --all and --json follow it.
func helpListed(c *cobra.Command) bool {
	return c.IsAvailableCommand() || c.Name() == "help"
}

func walk(c *cobra.Command, fn func(*cobra.Command)) {
	fn(c)
	for _, sub := range c.Commands() {
		if helpListed(sub) {
			walk(sub, fn)
		}
	}
}

// helpCommand is a command's help as --json prints it (helpDoc). Keys a form omits are absent:
// helpDetail's when nil, global_flags and environment when nil; every listed array is non-nil.
type helpCommand struct {
	Path    string   `json:"path"`
	Name    string   `json:"name"`
	Aliases []string `json:"aliases"`
	Group   string   `json:"group"`
	Short   string   `json:"short"`
	Usage   string   `json:"usage"`
	*helpDetail
	Flags       []helpFlag `json:"flags"`
	GlobalFlags []helpFlag `json:"global_flags,omitzero"`
	Environment []helpEnv  `json:"environment,omitzero"`
	Commands    any        `json:"commands"` // []helpSummary; []helpCommand under --all
}

// helpDetail is what long help adds to short.
type helpDetail struct {
	Long         string   `json:"long"`
	Examples     []string `json:"examples"`
	ReferenceURL string   `json:"reference_url"`
}

// helpSummary is a subcommand as a command's help lists it.
type helpSummary struct {
	Path    string   `json:"path"`
	Name    string   `json:"name"`
	Aliases []string `json:"aliases"`
	Short   string   `json:"short"`
}

// helpFlag is one flag as help shows it.
type helpFlag struct {
	Name          string   `json:"name"`
	Shorthand     string   `json:"shorthand"`
	Aliases       []string `json:"aliases"`
	Type          string   `json:"type"`
	Value         string   `json:"value"`
	Usage         string   `json:"usage"`
	Default       string   `json:"default"`
	ValueOptional bool     `json:"value_optional"`
}

// helpDoc is cmd's help in form as one document: helpEntry, plus the global flags once, at the
// top, as prose prints them (long help in its usage, --all before the first command).
func helpDoc(cmd *cobra.Command, form helpForm) helpCommand {
	doc := helpEntry(cmd, form)
	if form != helpShort {
		doc.GlobalFlags = helpFlags(cmd.InheritedFlags())
	}
	return doc
}

// helpEntry is cmd's document in form without its global flags; under --all its commands are
// documents too, each in long form.
func helpEntry(cmd *cobra.Command, form helpForm) helpCommand {
	cmd.InitDefaultHelpFlag() // as renderHelp: help, bare groups and --all reach here before cobra adds it
	doc := helpCommand{
		Path:    cmd.CommandPath(),
		Name:    cmd.Name(),
		Aliases: append([]string{}, cmd.Aliases...),
		Group:   groupTitle(cmd),
		Short:   strings.TrimSpace(cmd.Short),
		Usage:   cmd.UseLine(),
		Flags:   helpFlags(cmd.LocalFlags()),
	}
	if form != helpShort {
		long := cmd.Long
		if long == "" {
			long = cmd.Short
		}
		examples := []string{}
		for line := range strings.SplitSeq(cmd.Example, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				examples = append(examples, line)
			}
		}
		doc.helpDetail = &helpDetail{Long: strings.TrimSpace(long), Examples: examples, ReferenceURL: buildReferenceURL(cmd)}
		if cmd == cmd.Root() {
			doc.Environment = environment()
		}
	}
	if form == helpAll {
		subs := []helpCommand{}
		for _, sub := range cmd.Commands() {
			if helpListed(sub) {
				subs = append(subs, helpEntry(sub, helpAll))
			}
		}
		doc.Commands = subs
		return doc
	}
	subs := []helpSummary{}
	for _, sub := range cmd.Commands() {
		if helpListed(sub) {
			subs = append(subs, helpSummary{Path: sub.CommandPath(), Name: sub.Name(),
				Aliases: append([]string{}, sub.Aliases...), Short: strings.TrimSpace(sub.Short)})
		}
	}
	doc.Commands = subs
	return doc
}

// groupTitle is the title of the group c's parent lists it under, without its colon ("Core");
// "" when it has none.
func groupTitle(c *cobra.Command) string {
	if p := c.Parent(); p != nil {
		for _, g := range p.Groups() {
			if g.ID == c.GroupID {
				return strings.TrimSuffix(g.Title, ":")
			}
		}
	}
	return ""
}

// helpFlags is fs's flags as help shows them, hidden ones skipped: --trust's value is the
// placeholder prose shows (trustShownKey), since its NoOptDefVal is a sentinel no one types.
func helpFlags(fs *pflag.FlagSet) []helpFlag {
	out := []helpFlag{}
	fs.VisitAll(func(f *pflag.Flag) {
		if f.Hidden {
			return
		}
		value, usage := pflag.UnquoteUsage(f)
		if shown := f.Annotations[trustShownKey]; len(shown) > 0 {
			value = shown[0]
		}
		out = append(out, helpFlag{
			Name:          f.Name,
			Shorthand:     f.Shorthand,
			Aliases:       append([]string{}, f.Annotations[flagAliasKey]...),
			Type:          f.Value.Type(),
			Value:         value,
			Usage:         usage,
			Default:       f.DefValue,
			ValueOptional: f.NoOptDefVal != "",
		})
	})
	return out
}
