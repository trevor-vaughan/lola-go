package engine

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/market"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// MarketVerb is a run of the Python lola's lola market <verb> (Plan 6c D8, §3.1): the verb, the
// arguments after it, and the flags typed with it that a translation keeps or reads.
type MarketVerb struct {
	Verb            string
	Operands        []string
	Ref, Path       string
	DryRun, Yes     bool
	All             bool
	Enable, Disable bool
	// Display is the run's output flags, which every hint keeps.
	Display []string
}

// CoachMarketVerb refuses a Python lola lola market verb (Plan 6c D8, §3.1, rulings R-6c-11,
// R-6c-19), naming lola's form; the translation is the hint only when it is a complete command
// that runs: a name that cannot be a marketplace, or one you do not have, gets the listing or no
// hint, and a verb missing its operand gets a usage message alone. The hint keeps --ref and --path
// (for add) and -n; -y only for rm and remove when you gave it, never for set --disable, whose
// question you never answered.
func (e *Engine) CoachMarketVerb(v MarketVerb) error {
	typed := "lola market " + v.Verb
	refuse := func(lolaForm string, hint ...string) error {
		return errs.WithHints(fmt.Errorf("%s is the Python lola's form; in lola: %s", typed, lolaForm), hint...)
	}
	usage := func(form, lolaForm string) error {
		return fmt.Errorf("lola market %s takes %s; in lola: %s", v.Verb, form, lolaForm)
	}
	dry := func(args ...string) []string {
		if v.DryRun {
			args = append(args, "-n")
		}
		return args
	}
	ops := v.Operands
	// cmd is the lola market command with args and the run's output flags.
	cmd := func(args ...string) string { return MarketRun(append(args, v.Display...)...) }
	// mine is the hint of a translation naming the marketplace n: run when n is yours; the listing
	// when it can be a marketplace you do not have, said in the message; none when it cannot.
	// missing refuses n, a marketplace reg does not have, in the translation lolaForm, naming the
	// marketplaces close to it: each one's run (run with n replaced, each name once, never -y,
	// which answered the removal of n), then the listing.
	missing := func(reg market.Registry, lolaForm, n string, run []string) error {
		msg := lolaForm + ", and you have no marketplace " + untrusted.Text(n, untrusted.MaxName)
		near := market.CloseMarkets(reg, n)
		if len(near) > 0 {
			msg += "; close names: " + strings.Join(near, ", ")
		}
		var hints []string
		for _, c := range near {
			var args []string
			for _, a := range run {
				if a == n {
					a = c
				}
				if a != "-y" && !slices.Contains(args, a) {
					args = append(args, a)
				}
			}
			hints = append(hints, cmd(args...))
		}
		return refuse(msg, append(hints, cmd())...)
	}
	mine := func(lolaForm, n string, run []string) error {
		reg, _, err := e.registry()
		if err != nil {
			return err
		}
		switch {
		case market.ValidateName(n) != nil:
			return refuse(lolaForm + "; " + untrusted.Text(n, untrusted.MaxName) + " cannot name a marketplace")
		case reg.Find(n) == nil:
			return missing(reg, lolaForm, n, run)
		}
		return refuse(lolaForm, cmd(run...))
	}
	switch v.Verb {
	case "add":
		if len(ops) != 2 {
			return usage("a name and a source", "lola market <name> --add <source>")
		}
		if market.ValidateName(ops[0]) != nil {
			return refuse("lola market <name> --add <source>; " + untrusted.Text(ops[0], untrusted.MaxName) + " cannot name a marketplace")
		}
		run := addRun(ops[0], ops[1], v.Ref, v.Path, v.DryRun, nil)
		return refuse("lola market <name> --add <source>", cmd(run...))
	case "ls", "list":
		switch len(ops) {
		case 0:
			return refuse("lola market", cmd())
		case 1:
			return mine("lola market <name>", ops[0], []string{ops[0]})
		}
		return usage("one name at most", "lola market <name>")
	case "show":
		if len(ops) != 1 {
			return usage("one name", "lola market <name>")
		}
		return mine("lola market <name>", ops[0], []string{ops[0]})
	case "rm", "remove":
		if len(ops) != 1 {
			return usage("one name", "lola market <name> --remove")
		}
		run := dry(ops[0], "--remove")
		if v.Yes {
			run = append(run, "-y")
		}
		return mine("lola market <name> --remove", ops[0], run)
	case "update":
		switch {
		case len(ops) == 0:
			return refuse("lola market --update --all", cmd(dry("--update", "--all")...))
		}
		reg, _, err := e.registry()
		if err != nil {
			return err
		}
		for _, n := range ops {
			if market.ValidateName(n) != nil || reg.Find(n) == nil {
				return missing(reg, "lola market <name>... --update", n, dry(append(slices.Clone(ops), "--update")...))
			}
		}
		names := cmd(dry(append(slices.Clone(ops), "--update")...)...)
		if v.All {
			return errs.WithHints(fmt.Errorf("%s takes names or --all, not both; in lola: lola market <name>... --update, "+
				"or lola market --update --all", typed), names, cmd(dry("--update", "--all")...))
		}
		return refuse("lola market <name>... --update", names)
	case "search":
		if len(ops) != 1 {
			return usage("one query", "lola search <query>")
		}
		err := fmt.Errorf("%s is the Python lola's form; in lola: lola search <query>", typed)
		if !untrusted.IsPlain(ops[0]) || len(ops[0]) > untrusted.MaxName {
			return err // the hint would not be the query typed
		}
		return errs.WithHints(err, errs.ShellJoin(append([]string{"lola", "search", ops[0]}, v.Display...)))
	case "set":
		if len(ops) != 1 || v.Enable == v.Disable {
			return usage("--enable or --disable and one name", "lola market <name> --remove, as lola has no disabled state")
		}
		n := ops[0]
		if v.Disable {
			// Never with -y: the run's -y answered no question of a removal.
			return mine("lola market <name> --remove; lola has no disabled state", n, dry(n, "--remove"))
		}
		reg, _, err := e.registry()
		if err != nil {
			return err
		}
		if market.ValidateName(n) == nil && reg.Find(n) != nil {
			return errs.WithHints(fmt.Errorf("%s is already active; lola has no disabled state", n), cmd(n))
		}
		return fmt.Errorf("lola has no disabled state, and you have no marketplace %s", untrusted.Text(n, untrusted.MaxName))
	}
	return errors.New("lola market " + untrusted.Text(v.Verb, untrusted.MaxName) + " is not a Python lola verb")
}
