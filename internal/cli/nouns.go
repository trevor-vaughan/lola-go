package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// nounSpec is what a noun command (profile, market, trash) does without a verb: bare lists or
// opens its picker; with words, show; exists says whether a word names one of the noun's items.
// retired maps a word the noun's commands no longer answer to (a dropped alias) to the verb that
// does what it did. coached, where set, answers a run whose first word is one of another tool's
// verbs the noun answers with a lola command (market's set) and whose flag pflag refused: its
// operands are the words after the verb, and display the run's output flags; nil is a word and
// flag it does not answer, which coachFlags then coaches.
type nounSpec struct {
	noun    string
	bare    func(cmd *cobra.Command) error
	show    func(cmd *cobra.Command, args []string) error
	exists  func(cmd *cobra.Command, name string) bool
	retired map[string]string
	coached func(cmd *cobra.Command, word, flag string, operands, display []string) error
}

// nounRunE is a noun's RunE: no words run bare; a first word that is retired, or near a verb
// (cobra's suggestions), and names no item refuses, hinting the verb with the run's operands and
// flags; else the words are show's (lola <noun> <name> is lola <noun> show <name>).
func (a *app) nounRunE(ns nounSpec) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return ns.bare(cmd)
		}
		if verb, ok := ns.retired[args[0]]; ok && !ns.exists(cmd, args[0]) {
			path := append(strings.Fields(cmd.CommandPath()), verb)
			return errs.WithHints(fmt.Errorf("%s is not a %s command; it is %s", args[0], cmd.CommandPath(), strings.Join(path, " ")),
				a.runHints(cmd, cmd, nil, oldRun{verb: verbNamed(cmd, verb)}, args[1:], nil)...)
		}
		if s := cmd.SuggestionsFor(args[0]); len(s) > 0 && !ns.exists(cmd, args[0]) {
			return errs.WithHints(fmt.Errorf("unknown %s or command %q", ns.noun, untrusted.Text(args[0], untrusted.MaxName)),
				"did you mean: "+runAs(append(strings.Fields(cmd.CommandPath()), s[0]), cmd, args[1:]),
				"see them: "+runAs(append(strings.Fields(cmd.CommandPath()), "list"), cmd, nil))
		}
		return ns.show(cmd, args)
	}
}

// verbNamed is the verb of parent named name; a noun coaches only to verbs it has.
func verbNamed(parent *cobra.Command, name string) *cobra.Command {
	v, _, err := parent.Find([]string{name})
	if err != nil || v == parent {
		panic("lola coaches to a verb it does not have: " + parent.CommandPath() + " " + name)
	}
	return v
}

// verbWinsNote is the note a verb prints when the word it was called by also names an item of its
// noun: the verb ran, and show is how to see the item. nil when the word names none.
func verbWinsNote(cmd *cobra.Command, noun string, exists func(*cobra.Command, string) bool) []string {
	word := cmd.CalledAs()
	if word == "" || !exists(cmd, word) {
		return nil
	}
	return []string{fmt.Sprintf("ran %s; to show the %s named %s: %s show %s",
		word, noun, word, cmd.Parent().CommandPath(), errs.ShellJoin([]string{word}))}
}

// noteVerbWins wires every verb of parent, with ns, to carry the note of verbWinsNote: its Args rule
// first sets it as the run's notes (app.notes), which emit adds to the output (the notices of a
// --json document, else lines on stderr after it); a refusal, or a run that emits nothing, prints
// them on stderr (printNotes).
func (a *app) noteVerbWins(parent *cobra.Command, ns nounSpec) {
	for _, v := range parent.Commands() {
		args, run := v.Args, v.RunE
		v.Args = func(cmd *cobra.Command, operands []string) error {
			a.notes = nil
			for _, t := range verbWinsNote(cmd, ns.noun, ns.exists) {
				a.notes = append(a.notes, engine.Notice{Level: engine.LevelInfo, Text: t})
			}
			if args == nil {
				return nil
			}
			err := args(cmd, operands)
			if err != nil {
				a.printNotes(cmd)
			}
			return err
		}
		v.RunE = func(cmd *cobra.Command, operands []string) error {
			err := run(cmd, operands)
			a.printNotes(cmd)
			return err
		}
	}
}

// printNotes prints the run's notes on stderr, but under --json, and drops them: they print once.
func (a *app) printNotes(cmd *cobra.Command) {
	if !a.json {
		w := cmd.ErrOrStderr()
		for _, n := range a.notes {
			fmt.Fprintln(w, noticeLine(style{color: a.color(w), bold: a.bold(w), plain: a.plain, width: a.style(cmd).width}, n))
		}
	}
	a.notes = nil
}

// oldFlag is an action flag a noun had before its actions were verbs: the verb it is now and
// whether it took a value (lola market --add <src>), which becomes the verb's first operand, or with
// list a comma-separated list of them (lola profile --move a,b); lead is the flag of the verb the
// first word typed before the old flag becomes, where that word meant something else than an
// operand (lola profile <src> --move a is lola profile move a --from <src>).
type oldFlag struct {
	verb       string
	takesValue bool
	list       bool
	lead       string
}

// coachFlags sets a flag-error func on parent and its verbs. A first word the noun answers with
// another command (nounSpec.coached) is answered so; a run that carries an old action flag, at the
// unknown flag or after it, is refused with one runnable hint per old action flag (oldRunHints); a
// sibling verb's flag with one usage line per verb that takes it; any other flag error is the
// root's. pflag stops at an unknown flag and no error carries its value, so the value and
// everything after the flag are read from the run's arguments (app.argv).
func (a *app) coachFlags(parent *cobra.Command, ns nounSpec, old map[string]oldFlag) {
	coach := func(c *cobra.Command, err error) error {
		name, ok := unknownFlag(parent, err)
		if !ok {
			return parent.Root().FlagErrorFunc()(c, err)
		}
		// The flags after the unknown one never parsed, and --json among them says how to print this.
		a.json = a.json || jsonRequested(a.argv)
		at := unknownAt(a.argv, err, name)
		if before := c.Flags().Args(); c == parent && len(before) > 0 && ns.coached != nil && at >= 0 {
			// The words after the flag are the coached word's operands; output flags are the run's.
			words, flags, _ := parseTail(parent, old, a.argv[at+1:])
			display := outputFlags(c)
			for _, tf := range flags {
				if slices.Contains([]string{"json", "plain", "no-pager"}, tf.name) {
					display = append(display, tf.tokens...)
				}
			}
			if cerr := ns.coached(c, before[0], name, slices.Concat(before[1:], words), display); cerr != nil {
				return cerr
			}
		}
		if j := oldAt(a.argv, max(at, 0), c, old); j >= 0 {
			if at < 0 || at > j {
				at = j
			}
			return errs.WithHints(fmt.Errorf("--%s is not a flag of %s", oldName(a.argv[j]), parent.CommandPath()), a.oldRunHints(parent, c, old, at, j)...)
		}
		var takers []*cobra.Command
		for _, v := range parent.Commands() {
			if v != c && v.Flags().Lookup(name) != nil {
				takers = append(takers, v)
			}
		}
		if len(takers) == 0 {
			return parent.Root().FlagErrorFunc()(c, err)
		}
		var names, hints []string
		for _, v := range takers {
			names = append(names, v.Name())
			f := v.Flags().Lookup(name)
			value, _ := unknownFlagValue(a.argv, name, f.Value.Type() != "bool")
			given := []string{"--" + name}
			if f.Value.Type() != "bool" && value != "" {
				given = append(given, untrusted.Text(value, untrusted.MaxName))
			}
			hints = append(hints, "usage: "+verbUsage(v)+" "+errs.ShellJoin(given))
		}
		if before := c.Flags().Args(); c == parent && len(before) > 0 && !ns.exists(c, before[0]) {
			if verb, ok := ns.retired[before[0]]; ok {
				// A retired word is coached to its verb, which may take this flag: the run from the flag on.
				var tail []string
				if at >= 0 {
					tail = a.argv[at:]
				}
				path := append(strings.Fields(parent.CommandPath()), verb)
				return errs.WithHints(fmt.Errorf("%s is not a %s command; it is %s", before[0], parent.CommandPath(), strings.Join(path, " ")),
					a.runHints(parent, c, old, oldRun{verb: verbNamed(parent, verb)}, before[1:], tail)...)
			}
		}
		return errs.WithHints(fmt.Errorf("--%s applies to %s %s", name, parent.CommandPath(), andJoinWords(names)), hints...)
	}
	parent.SetFlagErrorFunc(coach)
	for _, v := range parent.Commands() {
		v.SetFlagErrorFunc(coach)
	}
}

// oldRun is one old action flag of a refused run: the verb it is now, the flag as it was and the
// value it was given.
type oldRun struct {
	verb  *cobra.Command
	of    oldFlag
	value string
}

// tailFlag is a flag typed after the unknown one: its name and the words it was typed with.
type tailFlag struct {
	name   string
	tokens []string
}

// oldRunHints is the hints for a run whose first old action flag is argv[j], pflag having stopped at
// the unknown flag argv[at] (at <= j): for that flag and any more after it, the runnable
// lola <noun> <verb> whose operands are the flag's value, if it took one (each item of a list),
// then every word of the run, parsed or not, in typed order (but the first word before the flag,
// where the flag has a lead: that flag's value), and whose flags are the run's that the verb
// defines, those typed between the unknown flag and the old one too. A candidate its verb refuses
// (its flags do not parse, or its Args rule refuses the operands) is not offered as a command: its
// usage and the noun's list, where it has one, are.
func (a *app) oldRunHints(parent, c *cobra.Command, old map[string]oldFlag, at, j int) []string {
	name := oldName(a.argv[j])
	of := old[name]
	value, next := "", j+1
	if v, ok := strings.CutPrefix(a.argv[j], "--"+name+"="); ok {
		value = v
	} else if of.takesValue && j+1 < len(a.argv) {
		value, next = a.argv[j+1], j+2
	}
	tail := slices.Concat(a.argv[at:j], a.argv[next:])
	return a.runHints(parent, c, old, oldRun{verb: verbNamed(parent, of.verb), of: of, value: value}, c.Flags().Args(), tail)
}

// oldAt is the index in argv of the first old action flag (--<old> or --<old>=…) from from on, up
// to --, that c does not define (market add has a real --enable); -1 when there is none.
func oldAt(argv []string, from int, c *cobra.Command, old map[string]oldFlag) int {
	for i := from; i < len(argv) && argv[i] != "--"; i++ {
		if !strings.HasPrefix(argv[i], "--") {
			continue
		}
		if _, isOld := old[oldName(argv[i])]; isOld && c.Flags().Lookup(oldName(argv[i])) == nil {
			return i
		}
	}
	return -1
}

// oldName is the flag name of t, a --name or --name=value token.
func oldName(t string) string {
	n, _, _ := strings.Cut(strings.TrimPrefix(t, "--"), "=")
	return n
}

// runHints is the hints for the run first, typed with before as the words before its flag and tail
// as the arguments after it: oldRunHints for an old action flag; for a retired word, first is its
// verb alone, before the words after it, and tail the run from the first flag the noun refused.
func (a *app) runHints(parent, c *cobra.Command, old map[string]oldFlag, first oldRun, before, tail []string) []string {
	more, flags, runs := parseTail(parent, old, tail)
	runs = append([]oldRun{first}, runs...)
	words := slices.Concat(before, more)
	var hints []string
	add := func(h string) {
		if !slices.Contains(hints, h) {
			hints = append(hints, h)
		}
	}
	listed := false
	for _, r := range runs {
		operands := slices.Clone(words)
		var kept []string
		if r.of.lead != "" && len(before) > 0 && r.verb.Flags().Lookup(r.of.lead) != nil {
			operands = operands[1:]
			kept = append(kept, "--"+r.of.lead, before[0])
		}
		values := []string{r.value}
		if r.of.list {
			values = slices.DeleteFunc(strings.Split(r.value, ","), func(v string) bool { return v == "" })
		}
		if r.value != "" {
			operands = slices.Insert(operands, 0, values...)
		}
		for _, tf := range flags {
			if tf.name != "" && r.verb.Flags().Lookup(tf.name) != nil {
				kept = append(kept, tf.tokens...)
			}
		}
		run := runAs(strings.Fields(r.verb.CommandPath()), c, operands)
		for _, k := range kept {
			run += " " + errs.ShellJoin([]string{untrusted.Text(k, untrusted.MaxName)})
		}
		if a.verbAccepts(r.verb, c, operands, kept) {
			add(run)
			continue
		}
		add("usage: " + verbUsage(r.verb))
		if list, _, err := parent.Find([]string{"list"}); err == nil && list != parent && !listed {
			listed = true
			// The run's flags list takes, parsed or typed after the unknown one.
			find := runAs(strings.Fields(list.CommandPath()), c, nil)
			for _, tf := range flags {
				if tf.name != "" && list.Flags().Lookup(tf.name) != nil {
					for _, k := range tf.tokens {
						find += " " + errs.ShellJoin([]string{untrusted.Text(k, untrusted.MaxName)})
					}
				}
			}
			add("find one: " + find)
		}
	}
	return hints
}

// parseTail splits tail, the arguments of a refused run from the flag pflag stopped at, into its
// words (every one after --), its flags, with the words each was typed with, and its old action
// flags as runs. A flag of a verb of the noun says whether it takes a value. A shorthand cluster is
// read as pflag reads it: a flag a letter at a time, one that takes a value taking the rest of the
// token, else the next argument; a letter no verb takes is no flag a hint can keep.
func parseTail(parent *cobra.Command, old map[string]oldFlag, tail []string) (words []string, flags []tailFlag, runs []oldRun) {
	verbs := append([]*cobra.Command{parent}, parent.Commands()...)
	lookup := func(long, short string) *pflag.Flag {
		for _, v := range verbs {
			f := v.Flags().Lookup(long)
			if f == nil {
				f = v.Flags().ShorthandLookup(short)
			}
			if f != nil {
				return f
			}
		}
		return nil
	}
	for i := 0; i < len(tail); i++ {
		t := tail[i]
		switch {
		case t == "--":
			words = append(words, tail[i+1:]...)
			i = len(tail)
		case strings.HasPrefix(t, "--"):
			n, v, hasValue := strings.Cut(t[2:], "=")
			if of, isOld := old[n]; isOld {
				if of.takesValue && !hasValue && i+1 < len(tail) {
					v, i = tail[i+1], i+1
				}
				runs = append(runs, oldRun{verb: verbNamed(parent, of.verb), of: of, value: v})
				continue
			}
			tf := tailFlag{name: n, tokens: []string{t}}
			if f := lookup(n, ""); f != nil && f.Value.Type() != "bool" && !hasValue && i+1 < len(tail) {
				tf.tokens, i = append(tf.tokens, tail[i+1]), i+1
			}
			flags = append(flags, tf)
		case strings.HasPrefix(t, "-") && len(t) > 1:
			for k := 1; k < len(t); k++ {
				f := lookup("", t[k:k+1])
				if f == nil {
					continue
				}
				tf := tailFlag{name: f.Name, tokens: []string{"-" + t[k:k+1]}}
				if f.Value.Type() == "bool" {
					flags = append(flags, tf)
					continue
				}
				switch rest := t[k+1:]; {
				case strings.HasPrefix(rest, "="):
					tf.tokens = append(tf.tokens, rest[1:])
				case rest != "":
					tf.tokens = append(tf.tokens, rest)
				case i+1 < len(tail):
					tf.tokens, i = append(tf.tokens, tail[i+1]), i+1
				}
				flags = append(flags, tf)
				break
			}
		default:
			words = append(words, t)
		}
	}
	return words, flags, runs
}

// verbAccepts reports whether verb would take operands and the flags of the refused run c, which
// verb defines, and kept, the words of the flags typed after the unknown one: its flags parse and
// its Args rule accepts the operands. The run never goes on, so verb's own flags are reset and used;
// the flags it inherits are the run's, shared with c, and keep their values.
func (a *app) verbAccepts(verb, c *cobra.Command, operands, kept []string) bool {
	if verb != c {
		own := verb.LocalNonPersistentFlags()
		own.VisitAll(func(f *pflag.Flag) {
			if sv, ok := f.Value.(pflag.SliceValue); ok {
				_ = sv.Replace([]string{})
			} else {
				_ = f.Value.Set(f.DefValue)
			}
			f.Changed = false
		})
		c.Flags().Visit(func(f *pflag.Flag) {
			if own.Lookup(f.Name) == nil || f.Name == "yes" {
				return
			}
			if sv, ok := f.Value.(pflag.SliceValue); ok {
				for _, v := range sv.GetSlice() {
					_ = verb.Flags().Set(f.Name, v)
				}
				return
			}
			_ = verb.Flags().Set(f.Name, f.Value.String())
		})
	}
	if err := verb.Flags().Parse(kept); err != nil {
		return false
	}
	return verb.ValidateArgs(operands) == nil
}

// verbUsage is the usage line of verb: its path and the operands its Use names.
func verbUsage(verb *cobra.Command) string {
	_, operands, _ := strings.Cut(verb.Use, " ")
	return strings.TrimSpace(verb.CommandPath() + " " + operands)
}

// unknownFlag is the long name of the flag err refuses as unknown (pflag: "unknown flag: --x", or
// "unknown shorthand flag: 'y' in -y", named by the verb of parent that defines that shorthand).
func unknownFlag(parent *cobra.Command, err error) (string, bool) {
	if name, ok := strings.CutPrefix(err.Error(), "unknown flag: --"); ok {
		return name, true
	}
	rest, ok := strings.CutPrefix(err.Error(), "unknown shorthand flag: '")
	if !ok {
		return "", false
	}
	short, _, _ := strings.Cut(rest, "'")
	if len(short) != 1 {
		return "", false
	}
	for _, v := range parent.Commands() {
		if f := v.Flags().ShorthandLookup(short); f != nil {
			return f.Name, true
		}
	}
	return "", false
}

// unknownAt is the index in argv of the flag err refuses as unknown, whose long name is name: the
// shorthand cluster pflag names ("unknown shorthand flag: 'y' in -yn"), else --name or --name=…;
// -1 when argv holds none.
func unknownAt(argv []string, err error, name string) int {
	if _, token, ok := strings.Cut(err.Error(), "' in "); ok && strings.HasPrefix(err.Error(), "unknown shorthand flag: '") {
		return slices.Index(argv, token)
	}
	return slices.IndexFunc(argv, func(s string) bool { return s == "--"+name || strings.HasPrefix(s, "--"+name+"=") })
}

// unknownFlagValue is the value --name was given in argv ("" for a flag that takes none, or none
// given) and the arguments after the flag and its value. A flag not found (a shorthand cluster) has
// no value and leaves no tail.
func unknownFlagValue(argv []string, name string, takesValue bool) (value string, tail []string) {
	i := slices.IndexFunc(argv, func(s string) bool { return s == "--"+name || strings.HasPrefix(s, "--"+name+"=") })
	if i < 0 {
		return "", nil
	}
	if v, ok := strings.CutPrefix(argv[i], "--"+name+"="); ok {
		return v, argv[i+1:]
	}
	if takesValue && i+1 < len(argv) {
		return argv[i+1], argv[i+2:]
	}
	return "", argv[i+1:]
}

// reservedWords is every name and alias of noun's verbs, and extra, sorted once each: what no item
// of the noun may be named, lola <noun> <word> being the verb.
func reservedWords(noun *cobra.Command, extra ...string) []string {
	out := slices.Clone(extra)
	for _, v := range noun.Commands() {
		out = append(out, v.Name())
		out = append(out, v.Aliases...)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// andJoinWords joins phrases as prose: "a", "a and b", "a, b and c".
func andJoinWords(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}
