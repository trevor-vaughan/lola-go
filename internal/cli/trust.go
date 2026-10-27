package cli

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
)

// trustFlag is --trust[=kind,…] (Plan 9 §3.2), one definition for every command that can ask a
// consent question: each occurrence as pflag set it, a bare --trust as engine.TrustBareValue, and
// how many positional arguments came before it, which finds the argument written right after a bare
// --trust (R-9-2). kinds is what parse made of them, which a hint repeating the run writes.
type trustFlag struct {
	fs     *pflag.FlagSet
	values []string
	at     []int
	kinds  engine.TrustKinds
}

func (t *trustFlag) String() string { return strings.Join(t.values, ",") }
func (t *trustFlag) Type() string   { return "kinds" }

// Set records one occurrence, and the positional arguments pflag has read before it.
func (t *trustFlag) Set(v string) error {
	t.values = append(t.values, v)
	t.at = append(t.at, len(t.fs.Args()))
	return nil
}

// trustShownKey is the annotation of --trust that holds what help shows after its name
// (aliasedFlagUsages): pflag would show its NoOptDefVal, engine.TrustBareValue, which no one types.
const trustShownKey = "lola_trust_shown"

// args is --trust as the user gave it, for a hint built before parse: each value, a bare --trust
// as such, the others after =.
func (t *trustFlag) args() []string {
	var out []string
	for _, v := range t.values {
		if v == engine.TrustBareValue {
			out = append(out, "--trust")
		} else {
			out = append(out, "--trust="+v)
		}
	}
	return out
}

// names reports whether a value given names kind k itself, as written (not through all or a bare
// --trust).
func (t *trustFlag) names(k engine.TrustKinds) bool {
	for _, v := range t.values {
		if v != engine.TrustBareValue && slices.Contains(strings.Split(v, ","), k.String()) {
			return true
		}
	}
	return false
}

// addTrustFlag adds --trust to cmd, which asks the kinds of command (engine.CommandTrust's name).
// Help shows --trust[=entries,profiles] where a bare --trust grants those, and --trust=<kinds>
// where it grants none and is refused (install, render, migrate), with no value placeholder, since
// the value must be joined by = (R-9-2); completion shows the usage as written.
func addTrustFlag(cmd *cobra.Command, t *trustFlag, command, usage string) {
	t.fs = cmd.Flags()
	cmd.Flags().Var(t, "trust", usage)
	cmd.Flags().Lookup("trust").NoOptDefVal = engine.TrustBareValue
	shown := "=<kinds>"
	if bare := engine.CommandTrust(command) & engine.TrustBare; bare != 0 {
		shown = "[=" + engine.TrustBare.String() + "]"
	}
	_ = cmd.Flags().SetAnnotation("trust", trustShownKey, []string{shown}) // the flag was just added
}

// parse is the kinds of trust --trust grants command (engine.CommandTrust's name), checked before
// any network contact or write (Plan 9 §3.3): a kind written as its own argument after a bare
// --trust is refused first, then whatever engine.ParseTrust refuses; each hint is the run (args are
// its names) with --trust as it would work. noun is what command's arguments name.
func (t *trustFlag) parse(cmd *cobra.Command, args []string, command, noun string) (engine.TrustKinds, error) {
	if len(t.values) == 0 {
		return 0, nil
	}
	dash := cmd.ArgsLenAtDash()
	for i, v := range t.values {
		at := t.at[i]
		if v != engine.TrustBareValue || at >= len(args) || dash >= 0 && at >= dash {
			continue
		}
		k, ok := engine.TrustArgument(command, args[at])
		if !ok {
			continue
		}
		// The run with the argument as the value it was meant to be, beside the values written:
		// the bare --trust it followed adds none of its kinds (S-I1), and a kind the command never
		// asks is refused as written (CR1).
		fixed := slices.Delete(slices.Clone(t.values), i, i+1)
		fixed = append(fixed, strings.ToLower(args[at]))
		rest := slices.Delete(slices.Clone(args), at, at+1)
		given, err := engine.ParseTrust(command, fixed)
		if fe, ok := errors.AsType[*engine.TrustFlagError](err); ok {
			return 0, errs.WithHints(err, fixedRun(cmd, rest, fe.Fix))
		}
		if err != nil {
			return 0, err
		}
		return 0, errs.WithHints(fmt.Errorf("%q is a kind of trust, not a %s: write %s", args[at], noun, k.Arg()),
			fixedRun(cmd, rest, given))
	}
	k, err := engine.ParseTrust(command, t.values)
	if fe, ok := errors.AsType[*engine.TrustFlagError](err); ok {
		if !fe.HasFix {
			return 0, err
		}
		return 0, errs.WithHints(err, fixedRun(cmd, args, fe.Fix))
	}
	t.kinds = k
	return k, err
}

// fixedRun is the user's run, its names args, with --trust granting k in place of the --trust
// given; none for 0.
func fixedRun(cmd *cobra.Command, args []string, k engine.TrustKinds) string {
	run := runWithout(cmd, args, "trust")
	if k != 0 {
		run += " " + k.Arg()
	}
	return run
}

// trustRun is a run's engine Rerun (Plan 9 §3.5): the user's run, its names args, with --trust for
// the kinds a refusal for want of trust names in place of the --trust given.
func trustRun(cmd *cobra.Command, args []string) func(engine.TrustKinds) string {
	return func(k engine.TrustKinds) string { return fixedRun(cmd, args, k) }
}

// receipt is a run's engine Receipt (Plan 9 §3.4): what given, the run's --trust, covers, grouped by
// kind on stderr, then, on a terminal and without yes (-y), one Proceed? [y/N], default No; off a
// terminal the run proceeds. Under --json it proceeds and prints nothing: the document's trusted
// key lists the grants, and stderr holds only an error's JSON line (the output contract).
func (a *app) receipt(cmd *cobra.Command, given engine.TrustKinds, yes bool) func([]engine.Consent) (bool, error) {
	return func(items []engine.Consent) (bool, error) {
		if a.json {
			return true, nil
		}
		w := cmd.ErrOrStderr()
		printReceipt(w, style{color: a.color(w)}, given, items)
		p := a.prompter(cmd) // never asks under --json
		if yes || !p.Interactive() {
			return true, nil
		}
		ok, err := p.Confirm("Proceed?")
		if err == nil && !ok {
			return false, ErrAborted // nothing is written: aborted, as a declined confirmation is
		}
		return ok, err
	}
}

// printReceipt prints items, the run's grants, one line per kind in the order given (canonical),
// each item with its change: "→ hooks: kit pre-install (new)".
func printReceipt(w io.Writer, s style, given engine.TrustKinds, items []engine.Consent) {
	fmt.Fprintf(w, "trusting, as %s allows:\n", given.Arg())
	for i := 0; i < len(items); {
		kind := items[i].Kind
		var line []string
		for ; i < len(items) && items[i].Kind == kind; i++ {
			line = append(line, items[i].Item+" ("+items[i].Change+")")
		}
		fmt.Fprintln(w, "  "+s.info(kind+": "+strings.Join(line, ", ")))
	}
}

// trustHelp is the help of --trust for command (engine.CommandTrust's name): the kinds it takes.
func trustHelp(command string) string {
	takes := engine.CommandTrust(command)
	out := "--trust=<kinds> answers, without asking, what the run would ask about of the kinds named:\n" +
		strings.ReplaceAll(takes.String(), ",", ", ") + ", or all for every one.\n"
	if b := takes & engine.TrustBare; b != 0 {
		out += "Bare --trust is " + b.String() + ", which run no code.\n"
	}
	return out + "It lists what it covers before anything is written, and on a terminal asks once (-y\n" +
		"answers); a grant is recorded as a yes at the prompt is."
}
