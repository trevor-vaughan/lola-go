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

// MarketVerb is a run of lola market <verb>, a word lola market reserves (Plan 6c D8, §3.1; Plan
// 10 §7): the verb, the arguments after it, and the flags typed with it that the lola command it
// is answered with keeps or reads.
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

// The lola commands a reserved word is answered with, each saying what it does (Plan 10 §7).
const (
	verbAdd       = "lola market <name> --add <source> adds a marketplace"
	verbList      = "lola market lists your marketplaces"
	verbShow      = "lola market <name> shows a marketplace"
	verbRemove    = "lola market <name> --remove forgets a marketplace"
	verbUpdate    = "lola market <name>... --update updates the marketplaces named"
	verbUpdateAll = "lola market --update --all updates every marketplace"
	verbSearch    = "lola search <query> searches your cache and every fetched catalog"
	noDisabled    = "; lola has no disabled state"
)

// CoachMarketVerb refuses lola market <verb> (Plan 6c D8, §3.1, rulings R-6c-11, R-6c-19; Plan 10
// §7), saying what the lola command is; that command is the hint only when it is complete and
// runs: a name that cannot be a marketplace, or one you do not have, gets the listing or no hint,
// and a verb missing its operand gets the message alone. The hint keeps --ref and --path (for
// add) and -n; -y only for rm and remove when you gave it, never for set --disable, whose question
// you never answered.
func (e *Engine) CoachMarketVerb(v MarketVerb) error {
	verb := untrusted.Text(v.Verb, untrusted.MaxName)
	// refuse says the verb is not lola market's, and what form, the lola command, does.
	refuse := func(form string, hint ...string) error {
		return errs.WithHints(fmt.Errorf("%s is not a lola market verb: %s", verb, form), hint...)
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
	// mine is the hint of a form naming the marketplace n: run when n is yours; the listing when
	// it can be a marketplace you do not have, said in the message; none when it cannot.
	// missing refuses n, a marketplace reg does not have, in form, naming the marketplaces close
	// to it: each one's run (run with n replaced, each name once, never -y, which answered the
	// removal of n), then the listing.
	missing := func(reg market.Registry, form, n string, run []string) error {
		msg := form + ", and you have no marketplace " + untrusted.Text(n, untrusted.MaxName)
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
	mine := func(form, n string, run []string) error {
		reg, _, err := e.registry()
		if err != nil {
			return err
		}
		switch {
		case market.ValidateName(n) != nil:
			return refuse(form + "; " + untrusted.Text(n, untrusted.MaxName) + " cannot name a marketplace")
		case reg.Find(n) == nil:
			return missing(reg, form, n, run)
		}
		return refuse(form, cmd(run...))
	}
	switch v.Verb {
	case "add":
		if len(ops) != 2 {
			return refuse(verbAdd)
		}
		if market.ValidateName(ops[0]) != nil {
			return refuse(verbAdd + "; " + untrusted.Text(ops[0], untrusted.MaxName) + " cannot name a marketplace")
		}
		run := addRun(ops[0], ops[1], v.Ref, v.Path, v.DryRun, nil)
		return refuse(verbAdd, cmd(run...))
	case "ls", "list":
		switch len(ops) {
		case 0:
			return refuse(verbList, cmd())
		case 1:
			return mine(verbShow, ops[0], []string{ops[0]})
		}
		return refuse(verbShow)
	case "show":
		if len(ops) != 1 {
			return refuse(verbShow)
		}
		return mine(verbShow, ops[0], []string{ops[0]})
	case "rm", "remove":
		if len(ops) != 1 {
			return refuse(verbRemove)
		}
		run := dry(ops[0], "--remove")
		if v.Yes {
			run = append(run, "-y")
		}
		return mine(verbRemove, ops[0], run)
	case "update":
		if len(ops) == 0 {
			return refuse(verbUpdateAll, cmd(dry("--update", "--all")...))
		}
		reg, _, err := e.registry()
		if err != nil {
			return err
		}
		for _, n := range ops {
			if market.ValidateName(n) != nil || reg.Find(n) == nil {
				return missing(reg, verbUpdate, n, dry(append(slices.Clone(ops), "--update")...))
			}
		}
		names := cmd(dry(append(slices.Clone(ops), "--update")...)...)
		if v.All {
			return refuse(verbUpdate+", and "+verbUpdateAll, names, cmd(dry("--update", "--all")...))
		}
		return refuse(verbUpdate, names)
	case "search":
		if len(ops) != 1 || !untrusted.IsPlain(ops[0]) || len(ops[0]) > untrusted.MaxName {
			return refuse(verbSearch) // no query, or a hint that would not be the query typed
		}
		return refuse(verbSearch, errs.ShellJoin(append([]string{"lola", "search", ops[0]}, v.Display...)))
	case "set":
		if len(ops) != 1 || v.Enable == v.Disable {
			return refuse(verbRemove + noDisabled)
		}
		n := ops[0]
		if v.Disable {
			// Never with -y: the run's -y answered no question of a removal.
			return mine(verbRemove+noDisabled, n, dry(n, "--remove"))
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
	return errors.New(verb + " is not a lola market verb")
}
