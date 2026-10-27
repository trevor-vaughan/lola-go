package engine

import (
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/market"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// MarketGroup is one of your marketplaces with a fetched catalog, as the install pickers, search
// and completion browse it (Plan 6c §6.5, §6.8): its name and description and the entries an
// install of its address can take, in catalog order. Every string has been through untrusted.Text.
type MarketGroup struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Entries     []MarketOffer `json:"entries"`
}

// MarketOffer is one entry a marketplace offers: the address that installs it, its name,
// version, description and tags, the host its source is fetched from (the marketplace's own for a
// relative entry; "" for a folder), and whether your cache already holds it, by its name, from
// the same source and path.
type MarketOffer struct {
	Install     string   `json:"install"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Host        string   `json:"host"`
	Cached      bool     `json:"cached"`
}

// MarketGroups are your marketplaces with a fetched catalog, in markets.yaml's order, each with
// the entries it offers (Plan 6c §6.5, §6.8). It never fetches. A marketplace whose cache cannot
// be read is left out, with a warning naming the --update that fetches it again, and an entry
// whose cache record cannot be read browses as not cached, with a warning naming the lola cache rm
// that clears it; display is the run's output flags, which those commands keep.
func (e *Engine) MarketGroups(display []string) ([]MarketGroup, []string, error) {
	unlock, err := e.readMarkets(true)
	if err != nil {
		return nil, nil, err
	}
	defer unlock()
	reg, _, err := e.registry()
	if err != nil {
		return nil, nil, err
	}
	groups, warnings := []MarketGroup{}, []string{}
	for _, r := range reg.Markets {
		c, err := e.loadMarket(r)
		switch {
		case err != nil:
			warnings = append(warnings, shown(r.Name)+": "+shown(untrusted.CleanErr(err).Error())+" → "+
				MarketRun(append([]string{r.Name, "--update"}, display...)...))
			continue
		case c == nil:
			continue
		}
		g := MarketGroup{Name: r.Name, Description: shown(c.cat.Description), Entries: []MarketOffer{}}
		for _, en := range c.cat.Entries {
			// A cache record lola cannot read costs that name alone: it browses as not cached.
			o, found, err := e.Store.Origin(en.Name)
			if err != nil {
				warnings = append(warnings, shown(en.Name)+": your cache's record of it cannot be read: "+shown(untrusted.CleanErr(err).Error())+
					" → "+errs.ShellJoin(append([]string{"lola", "cache", "rm", shown(en.Name)}, display...)))
				found = false
			}
			spec := en.Spec()
			tags := shownAll(en.Tags)
			g.Entries = append(g.Entries, MarketOffer{Install: "@" + r.Name + "/" + shown(en.Name), Name: shown(en.Name), Version: shown(en.Version),
				Description: shown(en.Description), Tags: tags, Host: shown(source.Host(spec.Location)),
				Cached: found && o.Type == string(spec.Kind) && source.SameLocation(spec.Kind, o.Source, spec.Location) && o.Path == spec.Path &&
					o.Components.Equal(spec.Components)})
		}
		groups = append(groups, g)
	}
	return groups, warnings, nil
}

// MarketMeaning is what the install argument name may mean among your marketplaces (Plan 6c
// §3.3): when it is a module name your cache does not hold and no source here (cwd), the entries
// of that name your fetched catalogs offer, one per marketplace; otherwise none, as name means
// the cached module or the source. lola never picks one of them for you.
func (e *Engine) MarketMeaning(name, cwd string, display []string) ([]MarketOffer, error) {
	out := []MarketOffer{}
	if module.ValidateName("module", name) != nil {
		return out, nil
	}
	if _, found, err := e.Store.Origin(name); err != nil || found {
		return out, err
	}
	if _, isSrc, err := source.Parse(name, cwd); err != nil || isSrc {
		return out, nil // a source, or one Parse refuses, which the install reports
	}
	groups, _, err := e.MarketGroups(display)
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		if k := slices.IndexFunc(g.Entries, func(o MarketOffer) bool { return o.Name == name }); k >= 0 {
			out = append(out, g.Entries[k])
		}
	}
	return out, nil
}

// matches reports whether q, lower-cased, is in o's name, description or tags, case-insensitively
// (Plan 6c §6.8).
func (o MarketOffer) matches(q string) bool {
	return strings.Contains(strings.ToLower(o.Name), q) || strings.Contains(strings.ToLower(o.Description), q) ||
		slices.ContainsFunc(o.Tags, func(t string) bool { return strings.Contains(strings.ToLower(t), q) })
}

// marketCount is how many entries of the scopes' manifests mans name r, by its name and source,
// as their market (Plan 6c §3.1).
func marketCount(r market.Registration, mans []*manifest.Manifest) int {
	n := 0
	for _, m := range mans {
		for _, ent := range m.Modules {
			if ent.Market != nil && ent.Market.Name == r.Name && sameMarketSource(r, ent.Market.Source) {
				n++
			}
		}
	}
	return n
}
