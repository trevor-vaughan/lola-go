package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
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

// KnownPreview is a known marketplace's catalog read at its pin without enabling it: the seed's
// entry, the host it is fetched from, the entries it offers (installed state read for the scope
// asked about), the catalog entries lola does not offer with why (ssh hosts not allowed, sources it
// does not take, directories the tree lacks; what `--enable -n` lists) and a warning for each cache record
// of an entry that could not be read.
type KnownPreview struct {
	market.KnownMarket
	Host     string
	Entries  []PickerEntry
	Skipped  []MarketSkip
	Warnings []string
}

// PreviewKnown fetches the known marketplace id at its pinned commit as the dry run of EnableKnown
// does (fetchMarket, with no ssh host allowed) and returns the entries it offers, with their
// installed state in sc. It registers nothing and writes neither markets.yaml nor the marketplace
// cache: the tree is fetched into the run's staging directory, which is removed when it returns,
// and no file of the catalog is kept. A marketplace of the id you already have, from another
// source, does not matter: nothing is named or registered. An id the seed lacks is refused, naming
// the ids it has. A failed fetch (offline, a pin the repository lacks, a catalog lola cannot
// read) is coached with the dry-run --enable that shows the same error and the list of known
// marketplaces; a cancelled ctx is its own error. display is the run's output flags, which the
// hints keep.
func (e *Engine) PreviewKnown(ctx context.Context, id string, sc Scope, display []string) (_ KnownPreview, err error) {
	seed, err := e.seed()
	if err != nil {
		return KnownPreview{}, err
	}
	i := slices.IndexFunc(seed, func(k market.KnownMarket) bool { return k.ID == id })
	if i < 0 {
		return KnownPreview{}, unknownKnown(id, seed, display)
	}
	k := seed[i]
	installed, err := e.InstalledVersions(sc)
	if err != nil {
		return KnownPreview{}, err
	}
	src, err := e.marketSource(k.ID, k.Source, k.Commit, k.Path, nil, true, display)
	if err != nil {
		return KnownPreview{}, err
	}
	run, done, err := e.runDir()
	if err != nil {
		return KnownPreview{}, err
	}
	defer func() { err = errors.Join(err, done()) }()
	two := func(file string) string {
		return MarketRun(addRun(k.ID, k.Source, k.Commit, file, nil, true, display)...)
	}
	f, err := e.fetchMarket(ctx, src, k.ID, k.Path, "", run, nil, two, "")
	if err != nil {
		if ctx.Err() != nil {
			return KnownPreview{}, ctx.Err()
		}
		return KnownPreview{}, errs.WithHints(err, append(errs.Hints(err),
			MarketRun(append([]string{"--enable", k.ID, "-n"}, display...)...), MarketRun(append([]string{"--known"}, display...)...))...)
	}
	g, warnings := e.groupOf(k.ID, f.cat, display)
	entries := make([]PickerEntry, len(g.Entries))
	for j, o := range g.Entries {
		at, ok := installed[o.Name]
		entries[j] = PickerEntry{MarketOffer: o, Installed: ok, At: at, WebURL: g.webURLs[j]}
	}
	return KnownPreview{KnownMarket: k, Host: shown(source.Host(k.Source)), Entries: entries, Skipped: marketSkips(f.cat), Warnings: warnings}, nil
}

// UpdateKnownPastPin moves the git marketplace name to the tip of its remote (of its branch or tag
// when its ref names one, else the default branch) and registers that
// full commit SHA as its ref, so later --update runs stay there. It learns the tip without
// fetching (e.RemoteTip), reads the catalog at it as a dry --update would and asks confirm, with
// that -n result, before anything is written; a non-nil error from confirm stops the run and is its
// error, and -n and a nil confirm ask nothing. It then rewrites the ref under the markets write
// lock, which refuses a registration that changed meanwhile, and fetches the commit. A failed
// fetch is the run's error and writes nothing. req carries the run's dry-run, display and ssh
// settings; its names are replaced.
func (e *Engine) UpdateKnownPastPin(ctx context.Context, name string, req MarketUpdateRequest, confirm func(*MarketResult) error) (*MarketResult, error) {
	r, err := e.pastPinTarget(name, req.Display)
	if err != nil {
		return nil, err
	}
	tipOf := e.RemoteTip
	if tipOf == nil {
		tipOf = source.RemoteTip
	}
	tip, err := tipOf(ctx, source.Spec{Kind: source.KindGit, Location: r.Source, Ref: r.Ref})
	if err != nil {
		return nil, err
	}
	if !source.IsFullCommit(tip) {
		return nil, fmt.Errorf("%s: the remote's tip is not a full commit SHA", shown(r.Source))
	}
	past := &pastPin{reg: r, tip: tip}
	req.Names, req.All = []string{name}, false
	run := func(dry bool, pick func(MarketSSHStep) ([]string, error)) (*MarketResult, error) {
		q := req
		q.DryRun, q.PickSSH = dry, pick
		res, err := e.updateMarkets(ctx, q, past)
		if err != nil {
			return nil, err
		}
		if out := res.Updates[0]; out.Outcome == MarketStopped {
			return nil, ctx.Err()
		} else if out.Err != nil {
			return nil, out.Err
		}
		return res, nil
	}
	preview, err := run(true, nil)
	if err != nil || req.DryRun {
		return preview, err
	}
	if confirm != nil {
		if err := confirm(preview); err != nil {
			return nil, err
		}
	}
	return run(false, req.PickSSH)
}

// pastPinTarget is the registration of name that UpdateKnownPastPin moves: a git source, the
// only kind a commit can pin. A name you have not registered is refused as every --update does.
func (e *Engine) pastPinTarget(name string, display []string) (market.Registration, error) {
	unlock, err := e.readMarkets(true)
	if err != nil {
		return market.Registration{}, err
	}
	defer unlock()
	reg, _, err := e.registry()
	if err != nil {
		return market.Registration{}, err
	}
	r := reg.Find(name)
	if r == nil {
		return market.Registration{}, noMarket(reg, name, func(c string) []string { return []string{c, "--update"} }, display)
	}
	if market.IsCatalogURL(r.Source) || !source.IsGitForm(r.Source) {
		return market.Registration{}, fmt.Errorf("%s is not a git repository, so no commit pins it", shown(r.Name))
	}
	return *r, nil
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
