package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/fsx"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/market"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// MarketRemove is lola market's action that forgets a marketplace (Plan 6c §3.1).
const MarketRemove MarketAction = "remove"

// MarketRemoveRequest is one lola market <name> --remove (Plan 6c §3.1). Confirm asks whether to
// forget the marketplace; nil means it cannot ask, and the run is refused. The CLI passes one that
// says yes for -y.
type MarketRemoveRequest struct {
	Name    string
	DryRun  bool
	Confirm func(MarketRow) (bool, error)
	// Display is the run's output flags, which every hint keeps.
	Display []string
}

// MarketRemoveResult is what lola market --remove reports (Plan 6c §11.2).
type MarketRemoveResult struct {
	Removed []string `json:"removed"`
	Notices []Notice `json:"notices"`
	DryRun  bool     `json:"dry_run"`
}

// RemoveMarket forgets a marketplace (Plan 6c §3.1): its registration and its cached catalog. It
// asks first (Confirm); off a terminal without -y it refuses with the same run and -y, which
// approves exactly the removal refused and no other question (as ruling R-6b3c-11). There is no
// safety check, so there is no --force: nothing installed depends on a marketplace. The question is
// asked under the shared markets lock; the exclusive one is taken only to check the marketplace is
// still registered, write markets.yaml without it, and remove its cache, renamed aside first so it
// is gone in one step.
func (e *Engine) RemoveMarket(req MarketRemoveRequest) (*MarketRemoveResult, error) {
	unlock, err := e.readMarkets(req.DryRun)
	if err != nil {
		return nil, err
	}
	defer func() { unlock() }()
	reg, _, err := e.registry()
	if err != nil {
		return nil, err
	}
	r := reg.Find(req.Name)
	if r == nil {
		// Never with -y, which answered the removal of another name.
		run := func(n string) []string {
			if req.DryRun {
				return []string{n, "--remove", "-n"}
			}
			return []string{n, "--remove"}
		}
		return nil, noMarket(reg, req.Name, run, req.Display)
	}
	res := &MarketRemoveResult{Removed: []string{}, Notices: []Notice{}, DryRun: req.DryRun}
	if req.DryRun {
		res.Removed = append(res.Removed, r.Name)
		res.Notices = append(res.Notices, e.installedFrom(*r, true, req.Display)...)
		return res, nil
	}
	if req.Confirm == nil {
		return nil, errs.WithHints(errors.New("lola market --remove asks before it forgets a marketplace, and there is no terminal to ask on"),
			MarketRun(append([]string{r.Name, "--remove", "-y"}, req.Display...)...))
	}
	c, _ := e.loadMarket(*r) // only for the question's row: an unreadable cache is removed all the same
	ok, err := req.Confirm(e.marketRow(*r, c))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrDeclined
	}
	unlock()
	if unlock, err = lockAt(e.Paths.MarketsLock, true); err != nil {
		unlock = func() {}
		return nil, err
	}
	reg, b, err := e.registry()
	if err != nil {
		return nil, err
	}
	if reg.Find(r.Name) == nil {
		return nil, noMarket(reg, r.Name, nil, req.Display)
	}
	nb, _, err := market.RemoveRegistration(b, r.Name)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", shown(e.Paths.Markets), err)
	}
	if err := writeRegistry(e.Paths.Markets, nb); err != nil {
		return nil, err
	}
	res.Removed = append(res.Removed, r.Name)
	if err := e.dropMarketCache(r.Name); err != nil {
		res.Notices = append(res.Notices, Notice{Level: LevelWarn, Text: "forgot " + r.Name + ", but its cached catalog stays: " +
			shown(untrusted.CleanErr(err).Error())})
	}
	res.Notices = append(res.Notices, e.installedFrom(*r, false, req.Display)...)
	return res, nil
}

// sameMarketSource reports whether r, your registration of a marketplace, has src, the source an
// entry's market records (Plan 6c §6.6, R-6c-15).
func sameMarketSource(r market.Registration, src string) bool {
	ms := registrySource(r)
	kind := ms.spec.Kind
	if ms.file {
		kind = source.KindGit // compared as text, as a URL is
	}
	return source.SameLocation(kind, r.Source, src)
}

// installedFrom is the notice counting the entries of the known scopes (the project found from
// the working directory, and user scope) whose market is r, its name and source, which removing
// it leaves as they are (Plan 6c §3.1): they keep their real sources. Once it is gone the notice names the --add
// that brings it back, with display, the run's output flags, as lola sync --update follows a
// marketplace only while you have it; dry says it is not gone. A manifest it cannot read is a
// warning of its own.
func (e *Engine) installedFrom(r market.Registration, dry bool, display []string) []Notice {
	scopes, _ := e.knownScopes()
	var mans []*manifest.Manifest
	var out []Notice
	for _, sc := range scopes {
		m, err := e.Manifest(sc)
		if err != nil {
			// A file with marketplace addresses gets the hints of every reader (Plan 6c §6.7), read
			// from markets.yaml as it is now: once r is gone, none names it.
			err = e.CoachUnresolved(err, []Scope{sc}, dry, display)
			text := "could not count the entries from " + r.Name + " in " + filepath.Base(e.files(sc).manifest) + ": " + shown(untrusted.CleanErr(err).Error())
			if hints := errs.Hints(err); len(hints) > 0 {
				text += " → " + strings.Join(hints, "; ")
			}
			out = append(out, Notice{Level: LevelWarn, Text: text})
			continue
		}
		mans = append(mans, m)
	}
	n := marketCount(r, mans)
	if n == 0 {
		return out
	}
	where := "your user scope"
	if len(scopes) > 1 {
		where = "this project and your user scope"
	}
	what := fmt.Sprintf("%d entries in %s name %s as their marketplace", n, where, r.Name)
	if n == 1 {
		what = fmt.Sprintf("1 entry in %s names %s as its marketplace", where, r.Name)
	}
	text := what + "; installed from their real sources, they stay installed"
	if !dry {
		text += ", and lola sync --update follows " + r.Name + " again once you add it back → " +
			MarketRun(addRun(r.Name, r.Source, r.Ref, r.Path, false, display)...)
	}
	return append(out, Notice{Level: LevelInfo, Text: text})
}

// dropMarketCache removes the cached catalog of the marketplace name, renamed aside first (a
// .del- directory, which a sweep finishes if lola is killed). The caller holds the markets lock
// exclusively and has removed the registration, so listings no longer show it.
func (e *Engine) dropMarketCache(name string) error {
	dir := filepath.Join(e.Paths.MarketCache, name)
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	aside, err := os.MkdirTemp(e.Paths.MarketCache, delPrefix+name+"-*")
	if err != nil {
		return err
	}
	if err := os.Remove(aside); err != nil {
		return err
	}
	if err := os.Rename(dir, aside); err != nil {
		return err
	}
	if err := fsx.SyncDir(e.Paths.MarketCache); err != nil {
		return err
	}
	return os.RemoveAll(aside)
}
