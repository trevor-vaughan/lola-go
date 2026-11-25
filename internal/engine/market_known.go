package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/market"
	"github.com/trevor-vaughan/lola-go/internal/source"
)

// KnownEntry is a known marketplace as lola market --known lists it: the maintainers' entry and
// whether you have enabled it, that is, a marketplace of its id is registered from its source.
type KnownEntry struct {
	market.KnownMarket
	Enabled bool `json:"enabled"`
}

// seed is the known marketplaces lola ships (e.KnownSeed when a spec replaces them).
func (e *Engine) seed() ([]market.KnownMarket, error) {
	if e.KnownSeed != nil {
		return e.KnownSeed()
	}
	return market.Known()
}

// Known is the marketplaces the lola maintainers curated, in the seed's order, never nil, each
// marked enabled when a marketplace named as its id is registered from its source.
func (e *Engine) Known() ([]KnownEntry, error) {
	seed, err := e.seed()
	if err != nil {
		return nil, err
	}
	unlock, err := e.readMarkets(true)
	if err != nil {
		return nil, err
	}
	defer unlock()
	reg, _, err := e.registry()
	if err != nil {
		return nil, err
	}
	out := make([]KnownEntry, 0, len(seed))
	for _, k := range seed {
		r := reg.Find(k.ID)
		out = append(out, KnownEntry{KnownMarket: k, Enabled: r != nil && source.SameLocation(source.KindGit, r.Source, k.Source)})
	}
	return out, nil
}

// KnownConfirm is asked, before anything is written, whether to enable k: preview is what enabling
// it would do (the -n result, the catalog's entries and what it skips). A non-nil error stops the
// run, and is the run's error.
type KnownConfirm func(k market.KnownMarket, preview *MarketResult) error

// ShowKnown is the known marketplace id as Known lists it; an id the seed lacks is refused, naming
// the ids it has.
func (e *Engine) ShowKnown(id string, display []string) (KnownEntry, error) {
	all, err := e.Known()
	if err != nil {
		return KnownEntry{}, err
	}
	seed := make([]market.KnownMarket, len(all))
	for i, k := range all {
		if k.ID == id {
			return k, nil
		}
		seed[i] = k.KnownMarket
	}
	return KnownEntry{}, unknownKnown(id, seed, display)
}

// EnableKnown registers the known marketplace id, as name (its id when ""), by the add flow
// (AddMarket) with the seed's source and path at its pinned commit as the ref: the catalog is
// read as any other and nothing in it runs, and a branch's later commits are never fetched.
// req carries the run's dry-run, display, rename and ssh settings; its source, ref, path and name
// are replaced. An id the seed lacks is refused, naming the ids it has. A name another marketplace
// has is refused with the --enable that names a free one, where the run cannot ask (as takenName),
// and always when confirm is set. A non-nil confirm is asked, with the -n result of the enable,
// before anything is written, and never under -n; nil asks nothing.
func (e *Engine) EnableKnown(ctx context.Context, id, name string, req MarketAddRequest, confirm KnownConfirm) (*MarketResult, market.KnownMarket, error) {
	seed, err := e.seed()
	if err != nil {
		return nil, market.KnownMarket{}, err
	}
	i := 0
	for i < len(seed) && seed[i].ID != id {
		i++
	}
	if i == len(seed) {
		return nil, market.KnownMarket{}, unknownKnown(id, seed, req.Display)
	}
	k := seed[i]
	if name == "" {
		name = k.ID
	}
	req.Name, req.Source, req.Ref, req.Path = name, k.Source, k.Commit, k.Path
	if req.Rename == nil || req.DryRun || confirm != nil {
		if err := e.knownNameFree(k, name, req); err != nil {
			return nil, market.KnownMarket{}, err
		}
	}
	if confirm != nil && !req.DryRun {
		preview := req
		preview.DryRun, preview.Rename, preview.PickSSH = true, nil, nil
		res, err := e.AddMarket(ctx, preview)
		if err != nil {
			return nil, market.KnownMarket{}, err
		}
		if err := confirm(k, res); err != nil {
			return nil, market.KnownMarket{}, err
		}
	}
	res, err := e.AddMarket(ctx, req)
	if err != nil {
		return nil, market.KnownMarket{}, err
	}
	return res, k, nil
}

// unknownKnown refuses id, which the seed lacks, naming the ids it has and hinting their list.
func unknownKnown(id string, seed []market.KnownMarket, display []string) error {
	ids := make([]string, len(seed))
	for i, k := range seed {
		ids[i] = k.ID
	}
	list := "this lola ships none"
	if len(ids) > 0 {
		list = "the known ones are " + strings.Join(ids, ", ")
	}
	return errs.WithHints(fmt.Errorf("%s is not a known marketplace; %s", shown(id), list),
		MarketRun(append([]string{"--known"}, display...)...))
}

// knownNameFree refuses name when a marketplace that is not k at its pin already has it, hinting
// the --enable of k under the first free name. The same marketplace passes: AddMarket coaches
// its --update.
func (e *Engine) knownNameFree(k market.KnownMarket, name string, req MarketAddRequest) error {
	reg, _, err := e.registry()
	if err != nil {
		return err
	}
	r := reg.Find(name)
	if r == nil || (source.SameLocation(source.KindGit, r.Source, k.Source) && r.Ref == k.Commit) {
		return nil
	}
	refusal := fmt.Errorf("%s is already a marketplace, from %s", shown(r.Name), marketWhere(r.Source, r.Ref, ""))
	free := market.FreeName(name, 2, func(n string) bool { return reg.Find(n) != nil })
	if free == "" {
		return refusal
	}
	hint := []string{free, "--enable", k.ID}
	if req.DryRun {
		hint = append(hint, "-n")
	}
	return errs.WithHints(refusal, MarketRun(append(hint, req.Display...)...))
}
