package engine

import (
	"context"
	"sync"
	"time"

	"github.com/trevor-vaughan/lola-go/internal/market"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// A row's Status once CheckMarkets has judged it; StatusChecking and StatusNotChecked are the
// picker's own, before a check answers and for a row with none.
const (
	StatusChecking    = "checking…"
	StatusCurrent     = "up to date"
	StatusUpdate      = "update available"
	StatusMoved       = "upstream moved"
	StatusUnreachable = "unreachable"
	StatusNotChecked  = "not checked"
)

const (
	checkTimeout     = 10 * time.Second
	checkConcurrency = 8
)

// Check is the result for the picker row at Index of the rows CheckMarkets was given.
type Check struct {
	Index  int
	Status string
	// Reason is the error text, untrusted.CleanErr'd, when Status is StatusUnreachable; else "".
	Reason string
}

// PickerRow is one line of the marketplace picker: a marketplace you have, or a known one you have
// not enabled. Every string has been through untrusted.Text.
type PickerRow struct {
	// MarketRow is the registration and its cached catalog; for a known row you have not enabled
	// only Source is set, to the seed's.
	MarketRow
	// ID is the name of one of your marketplaces, or the seed id of a known one.
	ID string `json:"id"`
	// Known is whether the seed lists it, enabled or not.
	Known bool `json:"known"`
	// Enabled is whether it is one of your marketplaces backed by a seed entry: the seed's id as its
	// name, registered from the seed's source.
	Enabled bool `json:"enabled"`
	// Reason is the seed's reason, "" for one of your marketplaces the seed does not list.
	Reason string `json:"reason"`
	// AheadOfPin is whether an enabled known marketplace is registered at a commit other than the
	// seed's.
	AheadOfPin bool `json:"ahead_of_pin"`
	// Pin is the seed's commit, for known rows only.
	Pin string `json:"pin"`
	// Status is the result of checking the row against its remote; "" until it is checked.
	Status string `json:"status"`
}

// PickerRows is the picker's rows: own, your marketplaces in markets.yaml order, with an enabled
// known one marked as such; and unenabled, the known marketplaces you have not enabled, in the seed's
// order. A marketplace of a known id but another source is yours alone, and the seed's entry stays
// unenabled. Neither slice is nil. It never fetches; display is the run's output flags, which the
// notices' commands keep.
func (e *Engine) PickerRows(display []string) (own, unenabled []PickerRow, _ []Notice, _ error) {
	rows, notices, err := e.Markets(display)
	if err != nil {
		return nil, nil, nil, err
	}
	seed, err := e.seed()
	if err != nil {
		return nil, nil, nil, err
	}
	unlock, err := e.readMarkets(true)
	if err != nil {
		return nil, nil, nil, err
	}
	defer unlock()
	reg, _, err := e.registry()
	if err != nil {
		return nil, nil, nil, err
	}
	own, unenabled = []PickerRow{}, []PickerRow{}
	taken := make(map[string]bool, len(seed))
	for _, row := range rows {
		pr := PickerRow{MarketRow: row, ID: row.Name}
		if r := reg.Find(row.Name); r != nil {
			for _, k := range seed {
				if k.ID == r.Name && source.SameLocation(source.KindGit, r.Source, k.Source) {
					pr.Known, pr.Enabled, pr.Reason, pr.Pin = true, true, shown(k.Reason), shown(k.Commit)
					commit, ok := source.CommitRef(r.Ref)
					pr.AheadOfPin = ok && commit != k.Commit
					taken[k.ID] = true
					break
				}
			}
		}
		own = append(own, pr)
	}
	for _, k := range seed {
		if !taken[k.ID] {
			unenabled = append(unenabled, PickerRow{MarketRow: MarketRow{Source: shown(k.Source)}, ID: shown(k.ID), Known: true, Reason: shown(k.Reason), Pin: shown(k.Commit)})
		}
	}
	return own, unenabled, notices, nil
}

// Checkable is whether CheckMarkets checks the row: a git source with a fetched catalog, or a known
// marketplace you have not enabled, whose pin stands for one.
func (r PickerRow) Checkable() bool {
	return !market.IsCatalogURL(r.Source) && source.IsGitForm(r.Source) && (r.Commit != "" || r.Known && !r.Enabled)
}

// AtCommit is whether the row is a checkable marketplace of yours registered at a full commit (in
// any case),
// which --update only fetches again: once its remote's tip is another commit it reads upstream
// moved, and the picker's u moves it to that tip (UpdateKnownPastPin), asking first.
func (r PickerRow) AtCommit() bool {
	_, commit := source.CommitRef(r.Ref)
	return r.Checkable() && commit
}

// CheckMarkets compares each git row's cached catalog commit (a known row you have not enabled: the
// seed's pin) with its remote's tip, without fetching: up to 8 at once, 10 seconds each. A row whose
// source is not git, or that has no fetched catalog, gets no Check. The channel is closed when every
// check has ended; cancelling ctx ends them early and drops the checks not yet started.
func (e *Engine) CheckMarkets(ctx context.Context, rows []PickerRow) <-chan Check {
	out := make(chan Check, len(rows))
	tip := e.RemoteTip
	if tip == nil {
		tip = source.RemoteTip
	}
	go func() {
		defer close(out)
		var wg sync.WaitGroup
		slots := make(chan struct{}, checkConcurrency)
	rows:
		for i, row := range rows {
			if row.Checkable() {
				select {
				case slots <- struct{}{}:
				case <-ctx.Done():
					break rows
				}
				wg.Add(1)
				go func() {
					defer func() { <-slots; wg.Done() }()
					cctx, cancel := context.WithTimeout(ctx, checkTimeout)
					defer cancel()
					out <- checkRow(cctx, tip, i, row)
				}()
			}
		}
		wg.Wait()
	}()
	return out
}

// checkRow is the Check of one git row, tip being how its remote's tip is learned.
func checkRow(ctx context.Context, tip func(context.Context, source.Spec) (string, error), index int, row PickerRow) Check {
	cached, moved := row.Commit, row.AtCommit()
	if row.Known && !row.Enabled {
		cached, moved = row.Pin, true
	}
	got, err := tip(ctx, source.Spec{Kind: source.KindGit, Location: row.Source, Ref: row.Ref})
	switch {
	case err != nil:
		return Check{Index: index, Status: StatusUnreachable, Reason: untrusted.CleanErr(err).Error()}
	case got == cached:
		return Check{Index: index, Status: StatusCurrent}
	case moved:
		return Check{Index: index, Status: StatusMoved}
	}
	return Check{Index: index, Status: StatusUpdate}
}

// CheckedMarket is one of your marketplaces as lola market --check lists it: its row and Status, the
// result of CheckMarkets for it, "" for one it does not check (not a git source, or never fetched).
type CheckedMarket struct {
	MarketRow
	Status string `json:"status"`
}

// CheckedMarkets is Markets with each git marketplace's Status, as CheckMarkets finds it; it waits
// for every check, so it takes at most about checkTimeout once the checks run 8 at once. display is the
// run's output flags, which the notices' commands keep. The slice is never nil; it fails with
// ctx's error once ctx is done.
func (e *Engine) CheckedMarkets(ctx context.Context, display []string) ([]CheckedMarket, []Notice, error) {
	own, _, notices, err := e.PickerRows(display)
	if err != nil {
		return nil, nil, err
	}
	rows := make([]CheckedMarket, len(own))
	for i, r := range own {
		rows[i].MarketRow = r.MarketRow
	}
	for c := range e.CheckMarkets(ctx, own) {
		rows[c.Index].Status = c.Status
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err // a stopped check reads unreachable, which it is not
	}
	return rows, notices, nil
}

// Classify is whether so many checks failed that the picker should say the network, not the
// marketplaces, is the trouble: at least two failed and more than half of those checked.
func Classify(checks []Check) (banner bool) {
	failed := 0
	for _, c := range checks {
		if c.Status == StatusUnreachable {
			failed++
		}
	}
	return failed >= 2 && failed*2 > len(checks)
}
