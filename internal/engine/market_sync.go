package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/market"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/trust"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// syncCatalogs are the catalogs a lola sync --update follows (Plan 6c §6.6, ruling R-6c-8): your
// registrations, the catalog of each marketplace an entry of the run was installed through and
// that you have from the same source, fetched once this run, and the marketplaces whose fetch
// failed, which keep their cached catalog.
type syncCatalogs struct {
	reg       market.Registry
	fetched   map[string]market.Catalog
	failed    map[string]bool
	untrusted map[string]bool // entries of a project.yml the trust record did not cover before the run
}

// fetchCatalogs fetches again, once each, the catalogs of the marketplaces that the entries of
// sc's manifest a sync --update of req would update were installed through, when your
// registration of the name has the source the entry records (Plan 6c §6.6, R-6c-8, R-6c-15), and,
// at project scope, when the trust record covers the entry before the run: a pulled file you have
// not trusted triggers no catalog request (ruling of 2026-10-22). The
// manifest is read here only to name the catalogs: the run reads it again under the scope lock,
// and an entry this read did not see makes no request of its own, though it follows a catalog
// the run fetched for another entry. Your registrations are read when any entry has a market, so
// follow can say whether a next run would follow an untrusted one. It takes the markets lock after the
// profiles lock and before the scope's, and returns it held; a failed fetch keeps the cached
// catalog and is a warning, and its entries update from their own sources.
func (e *Engine) fetchCatalogs(ctx context.Context, sc Scope, req SyncRequest) (*syncCatalogs, func(), []Notice, error) {
	none := func() {}
	out := &syncCatalogs{fetched: map[string]market.Catalog{}, failed: map[string]bool{}, untrusted: map[string]bool{}}
	man, err := e.Manifest(sc)
	if err != nil {
		return out, none, nil, nil // the scope's load refuses it
	}
	// A pulled file you have not trusted makes no request for its entries' catalogs (Plan 6c §6.6,
	// ruling of 2026-10-22): only entries the trust record covers before the run are followed.
	var rec *trust.Record
	if sc.Kind == target.Project {
		if rec, err = e.Trust.Load(sc.ProjectDir); err != nil {
			return out, none, nil, nil // the sync's own read refuses it
		}
	}
	var refs []manifest.MarketRef
	for _, ent := range man.Modules {
		if ent.Market == nil || len(req.Modules) > 0 && !slices.Contains(req.Modules, ent.Name) {
			continue
		}
		if rec != nil && !rec.View.Covers(ent) {
			out.untrusted[ent.Name] = true
			continue
		}
		if !slices.Contains(refs, *ent.Market) {
			refs = append(refs, *ent.Market)
		}
	}
	if len(refs) == 0 && len(out.untrusted) == 0 {
		return out, none, nil, nil
	}
	// A profile the run would read that has a marketplace address refuses it before any catalog is
	// fetched (Plan 6c §6.7): the scope's own read comes after the fetch. Any other failure to read
	// is left to that read.
	if led, err := ledger.Load(e.files(sc).ledger); err == nil {
		if _, err := e.loadProfiles(sc, nil, led.Profiles); err != nil {
			if _, ok := errors.AsType[*manifest.UnresolvedError](err); ok {
				return nil, none, nil, err
			}
		}
	}
	// Your registrations are read for untrusted entries too (follow's notice names whether a next
	// run would follow them); only refs are fetched.
	unlock, err := e.readMarkets(req.Options.DryRun)
	if err != nil {
		return nil, none, nil, err
	}
	if out.reg, _, err = e.registry(); err != nil {
		unlock()
		return nil, none, nil, err
	}
	var notices []Notice
	for _, ref := range refs {
		r := out.reg.Find(ref.Name)
		if _, done := out.fetched[ref.Name]; done || out.failed[ref.Name] || r == nil || !sameMarketSource(*r, ref.Source) {
			continue
		}
		f, _, err := e.updateMarket(ctx, *r, req.Options.DryRun, &unlock)
		if err != nil {
			if ctx.Err() != nil {
				unlock()
				return nil, none, nil, errors.Join(ctx.Err(), err)
			}
			out.failed[r.Name] = true
			notices = append(notices, Notice{Level: LevelWarn, Text: "could not fetch your marketplace " + r.Name +
				", so its entries update from their own sources: " + shown(untrusted.CleanErr(err).Error())})
			continue
		}
		out.fetched[r.Name] = f.cat
	}
	return out, unlock, notices, nil
}

// follow decides how a sync --update moves ent, an entry of sc's manifest installed through a
// marketplace, whose recorded source is spec (Plan 6c §6.6): the catalog's entry to follow when
// your marketplace of that name, from the source the entry records, lists it at the same source,
// path and ref, its commit pin then the new pin (fetchEntry checks it against the ref); otherwise
// nil, the entry updating as any other, with a notice saying why (an entry not trusted before the
// run among them, with the sync that follows the catalog next), and, when the catalog moved its ref,
// the install that takes it there (rulings R-6c-14, R-6c-15).
// A catalog this run did not fetch is never read. display is the run's output flags.
func (e *Engine) follow(sc Scope, c *syncCatalogs, ent manifest.Entry, spec source.Spec, dry bool, display []string) (*marketPick, *Notice) {
	m := ent.Market
	name, mkt := shown(ent.Name), shown(m.Name)
	r := c.reg.Find(m.Name)
	plain := func(why string) *Notice {
		return &Notice{Level: LevelInfo, Text: name + ": " + why + "; it updates from its own source"}
	}
	// Your registration is checked first, with no request: an entry no run would follow says
	// so, never promising the next update follows it.
	switch {
	case r == nil:
		return nil, plain("you have no marketplace " + mkt)
	case !sameMarketSource(*r, m.Source):
		return nil, plain("your marketplace " + mkt + " is not the one it was installed through, from " + shown(m.Source))
	case c.untrusted[ent.Name] && dry:
		// A dry run records no trust: the next run is no more trusted than this one.
		return nil, plain("lola follows a marketplace only for an entry you trusted before the run")
	case c.untrusted[ent.Name]:
		return nil, &Notice{Level: LevelInfo, Text: name + ": lola follows a marketplace only for an entry you trusted before the run; it " +
			"updates from its own source, and the next update follows " + mkt + " → " + e.SyncCommand(sc, append([]string{"--update", ent.Name}, display...)...)}
	case c.failed[r.Name]:
		return nil, nil // the warning of the failed fetch says it
	}
	cat, ok := c.fetched[r.Name]
	if !ok {
		return nil, plain("this run did not fetch your marketplace " + mkt)
	}
	k := slices.IndexFunc(cat.Entries, func(en market.Entry) bool { return en.Name == ent.Name })
	if k < 0 {
		return nil, plain("your marketplace " + mkt + " no longer lists it")
	}
	en := cat.Entries[k]
	listed := en.Spec()
	install := func(why string) *Notice {
		args := []string{"@" + r.Name + "/" + ent.Name}
		if dry {
			args = append(args, "-n")
		}
		return &Notice{Level: LevelInfo, Text: "your marketplace " + mkt + " " + why + "; " + name + " updates from its own source → " +
			e.Command(sc, "install", append(args, display...)...)}
	}
	// The cache holds one module of a name, from one source, path and selection: an install of the
	// address would be refused while this one is cached, so a move of those is a notice that says
	// what to do, with no command (ruling of the 6c.2 review, I1).
	cached := func(why string) *Notice {
		return &Notice{Level: LevelInfo, Text: "your marketplace " + mkt + " " + why + "; " + name + " updates from its own source (lola caches one " +
			name + " per machine: to install the catalog's, uninstall it and remove it from your cache first)"}
	}
	switch {
	case listed.Kind != spec.Kind || !source.SameLocation(spec.Kind, listed.Location, spec.Location) || listed.Path != spec.Path:
		where := shown(listed.Location)
		if listed.Path != "" {
			where += " path " + shown(listed.Path)
		}
		return nil, cached("lists " + name + " from " + where)
	case !listed.Components.Equal(spec.Components):
		return nil, cached("selects other components for " + name)
	case listed.Ref != spec.Ref:
		to := "the default branch"
		if listed.Ref != "" {
			to = "ref " + shown(listed.Ref)
		}
		return nil, install("moved " + name + " to " + to)
	}
	return &marketPick{addr: market.Address{Market: r.Name, Entry: ent.Name}, reg: *r, entry: en, display: display}, nil
}

// followPin fetches se's entry as its marketplace's catalog lists it (follow) and stages it (Plan
// 6c §6.6), through fetchEntry's checks: at the commit the catalog pins, checked against the
// entry's ref before anything is fetched, else the ref's head; an archive at the sha256 it lists.
func (e *Engine) followPin(ctx context.Context, se *syncEntry, run string) error {
	commit := se.follow.entry.Source.Commit
	f, err := e.fetchEntry(ctx, se.follow, se.spec, commit, run)
	if err != nil {
		return fmt.Errorf("%s: %w", se.ent.Name, err)
	}
	st, err := e.Store.Prepare(store.PutRequest{Staged: f.Dir, FallbackName: se.ent.Name, Name: se.ent.Name, Source: se.spec.Location,
		Type: string(se.spec.Kind), Path: se.spec.Path, Ref: se.spec.Ref, Commit: f.Commit, Executables: f.Executables, Now: e.Now(),
		Components: se.spec.Components})
	if err != nil {
		return err
	}
	se.v, se.staged, se.headIfAbsent = st.Version, &st, commit != "" // a pinned fetch never moves a head
	se.v.Module.Warnings = st.LoadWarnings
	return nil
}
