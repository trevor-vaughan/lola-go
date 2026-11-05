package engine

import (
	"slices"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/module"
)

// minSummarizedSkips is how many skips make a skip: list worth showing as its complement.
const minSummarizedSkips = 10

// PicksSummary is a long skip: list shown as what it leaves: the components a non-exact --only
// keeps (Picks, kind/name) and how many others are skipped. It is output only; JSON keeps the
// skips.
type PicksSummary struct {
	Picks  []string
	Others int
}

// summarizePicks shows skip, the skip: patterns in force for a module with the components all, as
// its complement when that is what the list is: the complement a non-exact --only saves, which
// installs a component a later version adds, as a hand-written skip: of the same shape does. It
// is nil, and the skips are shown as they are, unless the list has at least minSummarizedSkips
// patterns, every one names one component of all (no glob, which would also skip a later
// version's, and no stale name, which the complement would hide), and the components left are
// fewer than those skipped but not none.
func summarizePicks(skip []config.Pattern, all []Component) *PicksSummary {
	if len(skip) < minSummarizedSkips {
		return nil
	}
	skipped := 0
	var picks []string
	for _, p := range skip {
		if !p.Literal() || !slices.Contains(all, Component{Kind: p.Kind, Name: p.Name}) {
			return nil
		}
	}
	for _, c := range all {
		if slices.ContainsFunc(skip, func(p config.Pattern) bool { return p.Matches(c.Kind, c.Name) }) {
			skipped++
		} else {
			picks = append(picks, c.String())
		}
	}
	if len(picks) == 0 || len(picks) >= skipped {
		return nil
	}
	return &PicksSummary{Picks: picks, Others: skipped}
}

// resolvedSummary is the picks summary of a module resolved against x (resolveComponents): the
// skips x holds are what leave out every component r skipped, which is when the skips alone
// (not an only: list, not an include that spares one) decide what is installed. rulesOn says the
// module's rules count among its components. Nil otherwise.
func resolvedSummary(x config.Exclusions, m *module.Module, rulesOn bool, skipped []string) *PicksSummary {
	if only, _ := x.Only(); only != nil {
		return nil
	}
	all := slices.DeleteFunc(components(m), func(c Component) bool { return c.Kind == config.KindRule && !rulesOn })
	sum := summarizePicks(slices.Concat(x.Global.Skip, x.Scope.Skip), all)
	if sum == nil || sum.Others != len(skipped) {
		return nil
	}
	for i, p := range sum.Picks {
		sum.Picks[i] = shown(p)
	}
	return sum
}
