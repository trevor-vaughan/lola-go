package engine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/market"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// marketPick is an @<market>/<entry> argument resolved against your marketplace (Plan 6c §6.3):
// the address, your registration of the marketplace, the catalog entry, the entry's dependencies
// the same catalog offers (which notices coach installing, Plan 6c §7), and the run's output
// flags, which every hint keeps, with dry for a run that writes nothing.
type marketPick struct {
	addr    market.Address
	reg     market.Registration
	entry   market.Entry
	deps    []string
	display []string
	dry     bool
}

// maxBrowse is how many entries the refusal of a bare @<market> names.
const maxBrowse = 10

// resolveMarkets resolves, in place, every argument of reqs that is a marketplace address (Plan
// 6c §6.3), under the shared markets lock, which it returns held (taken only when an argument
// needs it); dry says the run writes nothing. An address with --path is refused: the catalog
// decides the path. The caller holds the profiles lock and no other (lock order, Plan 6c §5).
func (e *Engine) resolveMarkets(reqs []InstallRequest, dry bool, display []string) (_ func(), err error) {
	unlock, locked := func() {}, false
	defer func() {
		if err != nil {
			unlock()
		}
	}()
	for i := range reqs {
		addr, ok, err := market.ParseAddress(reqs[i].Arg)
		switch {
		case err != nil:
			return nil, err
		case !ok:
			continue
		case reqs[i].Path != "":
			return nil, fmt.Errorf("--path does not apply to %s: its catalog gives the path", addr)
		}
		if !locked {
			if unlock, err = e.readMarkets(dry); err != nil {
				unlock = func() {}
				return nil, err
			}
			locked = true
		}
		if reqs[i].market, err = e.resolveMarket(addr, display); err != nil {
			return nil, err
		}
		reqs[i].market.dry = dry
	}
	return unlock, nil
}

// resolveMarket resolves addr against your registration of its marketplace and the catalog you
// have cached (Plan 6c §6.3), never fetching the catalog; the caller holds the markets lock. A
// marketplace you do not have, or have not fetched, an entry its catalog skips or does not list
// (naming the close names), and a bare @<market> (naming its first entries) are refused, each
// with the command that shows what there is.
func (e *Engine) resolveMarket(addr market.Address, display []string) (*marketPick, error) {
	reg, _, err := e.registry()
	if err != nil {
		return nil, err
	}
	r := reg.Find(addr.Market)
	if r == nil {
		return nil, noMarket(reg, addr.Market, nil, display)
	}
	update := MarketRun(append([]string{r.Name, "--update"}, display...)...)
	show := MarketRun(append([]string{r.Name}, display...)...)
	c, err := e.loadMarket(*r)
	switch {
	case err != nil:
		return nil, errs.WithHints(fmt.Errorf("marketplace %s: %w", r.Name, untrusted.CleanErr(err)), update)
	case c == nil:
		return nil, errs.WithHints(fmt.Errorf("marketplace %s is not fetched: its catalog is not in your cache", r.Name), update)
	case addr.Entry == "":
		return nil, errs.WithHints(browseRefusal(r.Name, c.cat), show)
	}
	i := slices.IndexFunc(c.cat.Entries, func(en market.Entry) bool { return en.Name == addr.Entry })
	if i < 0 {
		if k := slices.IndexFunc(c.cat.Skipped, func(s market.Skip) bool { return s.Name == addr.Entry }); k >= 0 {
			return nil, errs.WithHints(fmt.Errorf("marketplace %s skips %s: %s", r.Name, addr.Entry, shown(c.cat.Skipped[k].Reason)), show)
		}
		msg := fmt.Sprintf("marketplace %s lists no %s", r.Name, addr.Entry)
		if near := market.CloseNames(c.cat, addr.Entry); len(near) > 0 {
			msg += "; close names: " + strings.Join(shownAll(near), ", ")
		}
		return nil, errs.WithHints(errors.New(msg), show)
	}
	en := c.cat.Entries[i]
	pick := &marketPick{addr: addr, reg: *r, entry: en, display: display}
	for _, d := range en.Dependencies {
		if slices.ContainsFunc(c.cat.Entries, func(o market.Entry) bool { return o.Name == d }) {
			pick.deps = append(pick.deps, d)
		}
	}
	return pick, nil
}

// browseRefusal refuses @<market> alone off a terminal (Plan 6c §6.5, ruling R-6c-11), naming
// the first entries of the catalog, as the addresses that install them.
func browseRefusal(name string, cat market.Catalog) error {
	var addrs []string
	for _, en := range cat.Entries {
		addrs = append(addrs, "@"+name+"/"+shown(en.Name))
	}
	if len(addrs) == 0 {
		return fmt.Errorf("@%s is a marketplace, and it offers no entry lola can install", name)
	}
	list := strings.Join(addrs[:min(len(addrs), maxBrowse)], ", ")
	if more := len(addrs) - maxBrowse; more > 0 {
		list += fmt.Sprintf(" and %d more", more)
	}
	return fmt.Errorf("@%s is a marketplace; name the entry to install: %s", name, list)
}

// spec is where p's entry is fetched from, with the commit it pins ("" for none): its source at
// the catalog's ref, or at ref, which replaces the catalog's ref and drops its commit pin (Plan 6c
// §6.3, ruling R-6c-9).
func (p *marketPick) spec(ref string) (source.Spec, string) {
	spec, commit := p.entry.Spec(), p.entry.Source.Commit
	if ref != "" {
		spec.Ref, commit = ref, ""
	}
	return spec, commit
}

// fetchEntry fetches p's entry from spec into run (Plan 6c §6.4, ruling R-6c-12): a commit pin
// must be reachable from spec's ref, the catalog's (the default branch when it gives none), before
// anything is fetched, and the fetch, by that commit, must serve it; an archive must have the
// sha256 the catalog lists before anything is extracted.
func (e *Engine) fetchEntry(ctx context.Context, p *marketPick, spec source.Spec, commit, run string) (source.Fetched, error) {
	update := MarketRun(append([]string{p.reg.Name, "--update"}, p.display...)...)
	fetch := spec
	if commit != "" {
		if err := source.CheckReachable(ctx, spec, commit, run); err != nil {
			if _, ok := errors.AsType[*source.NotOnRefError](err); ok {
				where := "the default branch"
				if spec.Ref != "" {
					where = shown(spec.Ref)
				}
				return source.Fetched{}, errs.WithHints(fmt.Errorf("marketplace %s lists %s at %s, which %s does not reach", p.reg.Name,
					p.entry.Name, commit, where), update)
			}
			return source.Fetched{}, err
		}
		fetch.Ref = commit
	}
	f, err := source.Fetch(ctx, fetch, run)
	if mismatch, ok := errors.AsType[*source.SHA256MismatchError](err); ok {
		return source.Fetched{}, errs.WithHints(fmt.Errorf("marketplace %s lists %s with sha256 %s; the download is %s", p.reg.Name,
			p.entry.Name, shown(mismatch.Want), mismatch.Got), update)
	}
	if err != nil {
		return source.Fetched{}, err
	}
	if commit != "" && f.Commit != commit {
		return source.Fetched{}, errors.Join(&servedCommitError{name: p.entry.Name, location: spec.Location, got: f.Commit, want: commit},
			os.RemoveAll(f.Dir))
	}
	return f, nil
}

// cacheEntry fetches p's entry from spec at commit (fetchEntry) and caches it under the entry's
// name (Plan 6c §6.3): a module that names itself otherwise is refused before anything is
// cached, and one that does not takes the entry's name. A pinned fetch never moves a head.
func (e *Engine) cacheEntry(ctx context.Context, p *marketPick, spec source.Spec, commit, run string) (_ store.Version, err error) {
	f, err := e.fetchEntry(ctx, p, spec, commit, run)
	if err != nil {
		return store.Version{}, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(f.Dir)) }()
	name := p.entry.Name
	st, err := e.Store.Prepare(store.PutRequest{Staged: f.Dir, FallbackName: name, Name: name, Source: spec.Location, Type: string(spec.Kind),
		Path: spec.Path, Ref: spec.Ref, Commit: f.Commit, Executables: f.Executables, Now: e.Now(), Components: spec.Components})
	if other, ok := errors.AsType[*store.OtherNameError](err); ok {
		return store.Version{}, renamedEntry(p, other.Declared, spec, commit)
	}
	if err != nil {
		return store.Version{}, err
	}
	return e.Store.Commit(st, commit != "")
}

// resolveEntry is resolve for an argument that is a marketplace address (Plan 6c §6.3, §6.4): its
// entry, recorded with its real source, path and the catalog's ref (or --ref), and with the
// marketplace's name and source as provenance unless --ref replaced the catalog's ref; the
// version is have (the version this request already resolved to) or the entry fetched and
// cached under its name. A version the pickers listed carries the commit it was fetched at,
// which must be the catalog's pin.
func (e *Engine) resolveEntry(ctx context.Context, sc Scope, r InstallRequest, have store.Version, run string) (manifest.Entry, store.Version, bool, error) {
	p := r.market
	spec, commit := p.spec(r.Ref)
	if err := e.recordable(sc, r.Arg, source.Recorded{Source: spec.Location, Ref: spec.Ref, Path: spec.Path}, false); err != nil {
		return manifest.Entry{}, store.Version{}, false, err
	}
	ent := manifest.Entry{Name: p.entry.Name, Source: spec.Location, Ref: spec.Ref, Path: spec.Path, Components: spec.Components.Clone()}
	if r.Ref == "" {
		ent.Market = &manifest.MarketRef{Name: p.reg.Name, Source: p.reg.Source}
	}
	if have.Name != "" {
		// The version listed must be the one the cache holds from where the catalog lists the
		// entry now: the catalog may have changed since it was listed.
		o, found, err := e.Store.Origin(have.Name)
		if err != nil {
			return manifest.Entry{}, store.Version{}, false, err
		}
		if have.Name != ent.Name || !found || o.Type != string(spec.Kind) || !source.SameLocation(spec.Kind, o.Source, spec.Location) || o.Path != spec.Path ||
			!o.Components.Equal(spec.Components) {
			return manifest.Entry{}, store.Version{}, false, pinMismatch(r)
		}
		if have.Commit == "" && r.Version != nil {
			have.Commit = r.Version.commit
		}
		if commit != "" && have.Commit != commit {
			return manifest.Entry{}, store.Version{}, false, errs.WithHints(fmt.Errorf("marketplace %s lists %s at %s, not at the commit it was listed at",
				p.reg.Name, ent.Name, commit), "run lola install again to list the current versions")
		}
		return ent, have, false, nil
	}
	v, err := e.cacheEntry(ctx, p, spec, commit, run)
	if renamed, ok := errors.AsType[*RenamedEntryError](err); ok && renamed.Source != nil {
		args := slices.Clone(renamed.Source)
		if p.dry {
			args = append(args, "-n")
		}
		err = errs.WithHints(err, e.Command(sc, "install", append(args, p.display...)...))
	}
	if err != nil {
		return manifest.Entry{}, store.Version{}, false, err
	}
	return ent, v, true, nil
}

// entryNotices are what an install of p says beside the module, once (Plan 6c §6.3, §7): that
// --ref (ref) replaced the catalog's ref, and what the catalog says lola does not do for it (its
// dependencies, its install scripts).
func entryNotices(p *marketPick, ref string) []Notice {
	var out []Notice
	name := shown(p.entry.Name)
	if ref != "" {
		out = append(out, Notice{Level: LevelInfo, Text: name + ": pinned to your ref " + shown(ref) + "; not following " + p.reg.Name})
	}
	for _, n := range p.entry.Notices {
		out = append(out, Notice{Level: LevelInfo, Text: name + ": " + shown(n)})
	}
	return out
}

// dependencyNotices are, for each dependency of p's entry that the same marketplace offers, the
// install that adds it at sc, with dry's -n and the run's output flags (Plan 6c §7): one command
// per dependency, never one that installs them all.
func (e *Engine) dependencyNotices(sc Scope, p *marketPick, dry bool) []Notice {
	var out []Notice
	name := shown(p.entry.Name)
	for _, d := range p.deps {
		args := []string{"@" + p.reg.Name + "/" + d}
		if dry {
			args = append(args, "-n")
		}
		out = append(out, Notice{Level: LevelInfo, Text: "install " + name + "'s dependency " + shown(d) + " → " +
			e.Command(sc, "install", append(args, p.display...)...)})
	}
	return out
}

// resolvedLine is how the install summary shows an address resolved to ent, at the commit the
// fetch pinned (Plan 6c §6.3, ruling R-6c-15): "@acme/formatter → <source> path <path> @ <ref>
// (<commit>)", then any selection of its components, every part through untrusted.Text.
func resolvedLine(addr string, ent manifest.Entry, commit string) string {
	line := shown(addr) + " → " + shown(ent.Source)
	if ent.Path != "" {
		line += " path " + shown(ent.Path)
	}
	switch {
	case ent.Ref != "" && commit != "":
		line += " @ " + shown(ent.Ref) + " (" + shown(commit) + ")"
	case ent.Ref != "":
		line += " @ " + shown(ent.Ref)
	case commit != "":
		line += " @ " + shown(commit)
	}
	if ent.Components != nil {
		line += ", selecting " + ent.Components.String()
	}
	return line
}

// AddEntry fetches the entry a marketplace address names into your cache (lola cache add
// @<market>/<entry>, Plan 6c §3.3), as an install would, without installing it: --ref replaces the
// catalog's ref; --path is refused, as the catalog gives the path. display is the run's output
// flags, which every hint keeps.
func (e *Engine) AddEntry(ctx context.Context, arg, ref, path string, display []string) (store.Version, error) {
	v, _, _, err := e.addEntry(ctx, arg, ref, path, display)
	return v, err
}

// addEntry is AddEntry, which also returns the ref the version was cached under and the commit
// the catalog pins it at ("" for none: --ref replaces the catalog's pin, and a catalog may give
// none), which lola info's next step installs by (Plan 9 §4.3).
func (e *Engine) addEntry(ctx context.Context, arg, ref, path string, display []string) (_ store.Version, cachedRef, commit string, err error) {
	reqs := []InstallRequest{{Arg: arg, Ref: ref, Path: path}}
	unlock, err := e.resolveMarkets(reqs, false, display)
	if err != nil {
		return store.Version{}, "", "", err
	}
	defer unlock()
	p := reqs[0].market
	if p == nil {
		return store.Version{}, "", "", fmt.Errorf("%s is not a marketplace address, @<marketplace>/<module>", untrusted.Text(arg, untrusted.MaxName))
	}
	run, done, err := e.runDir()
	if err != nil {
		return store.Version{}, "", "", err
	}
	defer func() { err = errors.Join(err, done()) }()
	spec, commit := p.spec(ref)
	v, err := e.cacheEntry(ctx, p, spec, commit, run)
	if renamed, ok := errors.AsType[*RenamedEntryError](err); ok && renamed.Source != nil {
		cmd := []string{"lola", "cache", "add"}
		for _, a := range append(slices.Clone(renamed.Source), display...) {
			cmd = append(cmd, untrusted.Text(a, untrusted.MaxName))
		}
		err = errs.WithHints(err, errs.ShellJoin(cmd))
	}
	return v, spec.Ref, commit, nameTaken(err, display)
}

// checkEntry loads the entry a marketplace address names for lola check (Plan 6c §3.3): fetched
// as an install would fetch it, into a directory under parent, never cached, named as the entry.
func (e *Engine) checkEntry(ctx context.Context, arg, parent string, display []string) (*module.Module, string, error) {
	reqs := []InstallRequest{{Arg: arg}}
	unlock, err := e.resolveMarkets(reqs, true, display)
	if err != nil {
		return nil, "", err
	}
	defer unlock()
	p := reqs[0].market
	spec, commit := p.spec("")
	f, err := e.fetchEntry(ctx, p, spec, commit, filepath.Join(parent, "fetch"))
	if err != nil {
		return nil, "", err
	}
	m, err := module.Detect(f.Dir, p.entry.Name, targets.IDs())
	if err == nil && m.Name != p.entry.Name {
		err = renamedEntry(p, m.Name, spec, commit)
		// lola check takes no --ref and no --path: only a source at its root, at its default
		// branch, checks what the catalog lists.
		if renamed, _ := errors.AsType[*RenamedEntryError](err); len(renamed.Source) == 1 {
			cmd := []string{"lola", "check"}
			for _, a := range append(slices.Clone(renamed.Source), display...) {
				cmd = append(cmd, untrusted.Text(a, untrusted.MaxName))
			}
			err = errs.WithHints(err, errs.ShellJoin(cmd))
		}
	}
	return m, f.Dir, err
}

// RenamedEntryError refuses a catalog entry whose module names itself another name than the
// entry's (Plan 6c §6.3, ruling of 2026-10-22): install, cache add and check alike. Source is
// what installs or caches the module from its source under its own name, as the catalog lists
// it (the location, then --ref and --path); nil when no command can, as for an entry whose
// catalog selects its components.
type RenamedEntryError struct {
	Market, Entry, Declared string
	Source                  []string
	// Kept names what a command typed by hand would not keep, which is why no hint offers one:
	// the catalog's sha256 check, or its redirect rule; "" when Source is set, or for a selection.
	Kept string
}

func (e *RenamedEntryError) Error() string {
	msg := fmt.Sprintf("marketplace %s lists %s, and the module at its source names itself %s; lola installs a catalog entry only under "+
		"the entry's name", e.Market, e.Entry, untrusted.Text(e.Declared, untrusted.MaxName))
	if e.Kept != "" {
		msg += ", and offers no install of its source by hand, which would not keep the catalog's " + e.Kept
	}
	return msg
}

// renamedEntry is the refusal of p's entry, fetched from spec at commit, whose module names itself
// declared. Its Source is the module's own source at the commit the catalog pins (else its ref),
// for the hints to install, cache or check by hand; none for a selection, which no command typed
// by hand makes, nor for a source a typed command would fetch under looser rules: an archive the
// catalog lists with a sha256, a source the catalog names (its redirect rule, Plan 6c §10).
func renamedEntry(p *marketPick, declared string, spec source.Spec, commit string) error {
	err := &RenamedEntryError{Market: p.reg.Name, Entry: p.entry.Name, Declared: declared}
	switch {
	case spec.Components != nil:
	case spec.SHA256 != "":
		err.Kept = "sha256 check"
	case spec.FromCatalog:
		err.Kept = "redirect rule"
	default:
		err.Source = []string{spec.Location}
		if ref := cmp.Or(commit, spec.Ref); ref != "" {
			err.Source = append(err.Source, "--ref", ref)
		}
		if spec.Path != "" {
			err.Source = append(err.Source, "--path", spec.Path)
		}
	}
	return err
}
