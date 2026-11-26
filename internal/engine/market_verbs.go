package engine

import (
	"errors"
	"fmt"
	"slices"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/market"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// MarketVerb is a run of lola market <verb>, a word of another tool's lola market that lola does
// not have: the verb, the arguments after it, and the flags typed with it that the lola command it is
// answered with keeps or reads.
type MarketVerb struct {
	Verb            string
	Operands        []string
	Enable, Disable bool
	// Display is the run's output flags, which every hint keeps.
	Display []string
}

// coachedMarketWords are the words of another tool's lola market that lola answers with a lola
// command rather than having a subcommand for; no marketplace can be named one.
var coachedMarketWords = []string{"search", "set"}

// CoachedMarketWords is the words lola market answers with the lola command, a copy.
func CoachedMarketWords() []string { return slices.Clone(coachedMarketWords) }

// The lola commands a coached word is answered with, each saying what it does.
const (
	verbRemove = "lola market rm <name> forgets a marketplace"
	verbSearch = "lola search <query> searches your cache and every fetched catalog"
	noDisabled = "; lola has no disabled state"
)

// RefuseReserved refuses name, an item of noun ("market", "profile"), when it is a word that
// noun's commands answer to (Reserved): lola <noun> <name> would run the command, not show the
// item. rerun is the run that repeats the refused one with another name, which the hint offers.
func (e *Engine) RefuseReserved(noun, name string, rerun func(name string) string) error {
	if !slices.Contains(e.Reserved[noun], name) {
		return nil
	}
	what, is := noun, "a lola "+noun+" command"
	if noun == "market" {
		what = "marketplace"
		if slices.Contains(coachedMarketWords, name) { // answered with a lola command, but not one itself
			is = "a word lola market answers to"
		}
	}
	return errs.WithHints(fmt.Errorf("%s is %s, so no %s can be named %s", name, is, what, name),
		"name it otherwise: "+rerun(name+"-1"))
}

// CoachMarketVerb refuses lola market <word>, a word of another tool's lola market, saying what the
// lola command is; that command is the hint only when it is complete and runs: a name that cannot be
// a marketplace, or one you do not have, gets the listing or no hint, and a word missing its operand
// gets the message alone. set --disable is rm, which asks, so its hint never carries -y.
func (e *Engine) CoachMarketVerb(v MarketVerb) error {
	verb := untrusted.Text(v.Verb, untrusted.MaxName)
	// refuse says the word is not lola market's, and what form, the lola command, does.
	refuse := func(form string, hint ...string) error {
		return errs.WithHints(fmt.Errorf("%s is not a lola market verb: %s", verb, form), hint...)
	}
	ops := v.Operands
	// cmd is the lola market command with args and the run's output flags.
	cmd := func(args ...string) string { return MarketRun(append(args, v.Display...)...) }
	switch v.Verb {
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
		reg, _, err := e.registry()
		if err != nil {
			return err
		}
		have := market.ValidateName(n) == nil && reg.Find(n) != nil
		switch {
		case v.Disable && have:
			return refuse(verbRemove+noDisabled, cmd("rm", n))
		case v.Disable && market.ValidateName(n) != nil:
			return refuse(verbRemove + noDisabled + "; " + untrusted.Text(n, untrusted.MaxName) + " cannot name a marketplace")
		case v.Disable:
			return noMarket(reg, n, func(c string) []string { return []string{"rm", c} }, v.Display)
		case have:
			return errs.WithHints(fmt.Errorf("%s is already active; lola has no disabled state", n), cmd(n))
		}
		return fmt.Errorf("lola has no disabled state, and you have no marketplace %s", untrusted.Text(n, untrusted.MaxName))
	}
	return errors.New(verb + " is not a lola market verb")
}
