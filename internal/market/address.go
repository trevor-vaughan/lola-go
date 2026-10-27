package market

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Address is a marketplace address as typed on the command line (Plan 6c §3.3):
// @<market>/<entry>, or @<market> alone, whose Entry is "".
type Address struct {
	Market, Entry string
}

// String is a as typed.
func (a Address) String() string {
	if a.Entry == "" {
		return "@" + a.Market
	}
	return "@" + a.Market + "/" + a.Entry
}

// ParseAddress reads arg as an Address (Plan 6c §3.3): ok is false for an argument that does not
// start with "@", a cached name or a source; one that does must be @<market> or
// @<market>/<entry>, each part passing the module-name rule, else it is an error. A directory
// named so is written ./@acme/x.
func ParseAddress(arg string) (_ Address, ok bool, _ error) {
	rest, ok := strings.CutPrefix(arg, "@")
	if !ok {
		return Address{}, false, nil
	}
	mkt, entry, _ := strings.Cut(rest, "/")
	a := Address{Market: mkt, Entry: entry}
	err := module.ValidateName("marketplace", mkt)
	if err == nil && (entry != "" || strings.HasSuffix(rest, "/")) {
		err = module.ValidateName("entry", entry)
	}
	if err != nil {
		return Address{}, true, fmt.Errorf("%s is not a marketplace address, @<marketplace>/<module>: %w (a directory of that name is ./%s)",
			untrusted.Text(arg, untrusted.MaxName), err, untrusted.Text(arg, untrusted.MaxName))
	}
	return a, true, nil
}

// maxClose is how many close names a refusal lists.
const maxClose = 10

// CloseNames are the names of the entries cat offers that are close to name (Plan 6c §6.3, T15),
// as closeTo finds them.
func CloseNames(cat Catalog, name string) []string {
	var names []string
	for _, e := range cat.Entries {
		names = append(names, e.Name)
	}
	return closeTo(names, name)
}

// CloseMarkets are the names of the marketplaces of reg that are close to name, a marketplace
// reg does not have, as closeTo finds them.
func CloseMarkets(reg Registry, name string) []string {
	var names []string
	for _, r := range reg.Markets {
		names = append(names, r.Name)
	}
	return closeTo(names, name)
}

// closeTo is those of names close to name: compared case-insensitively, within Levenshtein
// distance 2 of it or containing it; sorted by distance, then name; at most 10.
func closeTo(names []string, name string) []string {
	want := strings.ToLower(name)
	type near struct {
		name string
		dist int
	}
	var found []near
	for _, n := range names {
		have := strings.ToLower(n)
		if d := Distance(have, want); d <= 2 || strings.Contains(have, want) {
			found = append(found, near{name: n, dist: d})
		}
	}
	slices.SortFunc(found, func(a, b near) int { return cmp.Or(cmp.Compare(a.dist, b.dist), cmp.Compare(a.name, b.name)) })
	out := []string{}
	for _, n := range found[:min(len(found), maxClose)] {
		out = append(out, n.name)
	}
	return out
}

// Distance is the Levenshtein distance between a and b, by bytes: entry names and kinds of trust
// are ASCII.
func Distance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			sub := prev[j-1]
			if a[i-1] != b[j-1] {
				sub++
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, sub)
		}
		prev = cur
	}
	return prev[len(b)]
}
