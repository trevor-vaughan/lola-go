package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// HeadUpdate reports one re-fetched ref of a stored module.
type HeadUpdate struct {
	Name    string `json:"name"`
	Ref     string `json:"ref"`
	From    string `json:"from"`
	To      string `json:"to"`
	Changed bool   `json:"changed"`
}

// SearchHit is one module matching a search: Where is "cache", or "@<market>" for an entry of a
// marketplace's catalog (Plan 6c §6.8), and Install what lola install takes to install it: the
// cached module's name, or the entry's address.
type SearchHit struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Version     string   `json:"version"`
	Keywords    []string `json:"keywords"`
	Where       string   `json:"where"`
	Install     string   `json:"install"`
}

// NotStored is the error for a module that is not in the cache, hinting the listing with the
// run's output flags (display).
func NotStored(name string, display ...string) error {
	return errs.WithHints(fmt.Errorf("%s is not in your cache", name),
		"cached modules: "+errs.ShellJoin(append([]string{"lola", "cache", "list"}, display...)))
}

// AddSource fetches a source into the cache: lola cache add, and the install flow's Add a
// source…. Adding to the cache installs nothing. display is the run's output flags, which its
// hints keep.
func (e *Engine) AddSource(ctx context.Context, cwd, arg, ref, path string, display []string) (store.Version, error) {
	spec, ok, err := source.Parse(arg, cwd)
	if err != nil {
		return store.Version{}, err
	}
	if !ok {
		return store.Version{}, fmt.Errorf("%q is not a source; sources are %s", arg, source.AcceptedForms)
	}
	spec.Ref, spec.Path = ref, path
	v, err := e.Fetch(ctx, spec)
	return v, nameTaken(err, display)
}

// nameTaken replaces the hint of the cache's refusal of a second source for a name with one that
// keeps the run's output flags (display). It does so only for the refusal as the cache made it,
// its words and its one hint: one a caller wrapped with more words or more hints keeps its
// hints, which belong to that caller. Any other error is returned as it is.
func nameTaken(err error, display []string) error {
	taken, ok := errors.AsType[*store.NameTakenError](err)
	if !ok || err.Error() != taken.Error() || !slices.Equal(errs.Hints(err), []string{taken.RemoveHint()}) {
		return err
	}
	return errs.WithHints(taken, taken.RemoveHint(display...))
}

// Current returns a stored module's origin and the version used for its metadata: the
// default-ref head if there is one, else the first head. display is the run's output flags,
// which the refusal of a module not in the cache keeps in its hint.
func (e *Engine) Current(name string, display ...string) (store.Version, store.Origin, error) {
	o, found, err := e.Store.Origin(name)
	if err != nil {
		return store.Version{}, store.Origin{}, err
	}
	if !found || len(o.Heads) == 0 {
		return store.Version{}, store.Origin{}, NotStored(name, display...)
	}
	id := o.Heads[0].Digest
	for _, h := range o.Heads {
		if h.Ref == "" {
			id = h.Digest
		}
	}
	v, err := e.Store.Load(name, id)
	return v, o, err
}

// Cached is a stored module's current version with its origin.
type Cached struct {
	Version store.Version
	Origin  store.Origin
}

// CachedModules returns every stored module whose current version loads, in name order. A
// module that cannot be loaded (for example a lola.yml with an unknown key) does not fail
// the listing: it is skipped and reported as a "<name>: <error>" warning so the caller can
// show it; it stays removable with `cache rm`.
func (e *Engine) CachedModules() ([]Cached, []string, error) {
	names, err := e.Store.Names()
	if err != nil {
		return nil, nil, err
	}
	out, warnings := []Cached{}, []string{}
	for _, n := range names {
		v, o, err := e.Current(n)
		if err != nil {
			warnings = append(warnings, n+": "+err.Error())
			continue
		}
		out = append(out, Cached{Version: v, Origin: o})
	}
	return out, warnings, nil
}

// UpdateResult is what lola cache update reports.
type UpdateResult struct {
	Updates []HeadUpdate `json:"updates"`
	Notices []Notice     `json:"notices"`
}

// Update re-fetches every ref that has a head, for the named modules or all of them (§6.2).
// Moving a head changes no installation: render installs the version lock.json records (Plan
// 6a §10.7), which a notice says when a head moved. display is the run's output flags, which
// its hints keep.
func (e *Engine) Update(ctx context.Context, names []string, display ...string) (_ *UpdateResult, err error) {
	swept := e.sweepRuns()
	run, err := e.runDir()
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(run)) }()
	if len(names) == 0 {
		var err error
		if names, err = e.Store.Names(); err != nil {
			return nil, err
		}
	}
	out := []HeadUpdate{}
	for _, n := range names {
		o, found, err := e.Store.Origin(n)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, NotStored(n, display...)
		}
		for _, h := range o.Heads {
			v, err := e.fetchAs(ctx, source.Spec{Kind: source.Kind(o.Type), Location: o.Source, Path: o.Path, Ref: h.Ref, Components: o.Components}, "", n, run)
			if err != nil {
				return nil, fmt.Errorf("updating %s: %w", n, err)
			}
			if v.Name != n {
				return nil, fmt.Errorf("updating %s: its source now declares module %q (cached under that name)", n, v.Name)
			}
			out = append(out, HeadUpdate{Name: n, Ref: h.Ref, From: h.Digest, To: v.ID, Changed: v.ID != h.Digest})
		}
	}
	res := &UpdateResult{Updates: out, Notices: append([]Notice{}, swept...)}
	if slices.ContainsFunc(out, func(u HeadUpdate) bool { return u.Changed }) {
		res.Notices = append(res.Notices, Notice{Level: LevelInfo,
			Text: "installed modules keep the versions they were installed at; upgrade one with lola install <module>"})
	}
	return res, nil
}

// Search matches query case-insensitively against stored modules' names, descriptions and
// keywords, then, with markets, against the entries of every fetched catalog (Plan 6c §6.8, D7):
// their names, descriptions and tags, cache hits first, never fetching; an entry your cache holds
// from the same source is its cache hit alone. Unloadable modules and catalogs are not searched;
// they come back as warnings (see CachedModules, MarketGroups), whose commands keep display, the
// run's output flags.
func (e *Engine) Search(query string, markets bool, display []string) ([]SearchHit, []string, error) {
	q := strings.ToLower(query)
	cached, warnings, err := e.CachedModules()
	if err != nil {
		return nil, nil, err
	}
	hits := []SearchHit{}
	for _, c := range cached {
		m := c.Version.Module
		match := strings.Contains(strings.ToLower(m.Name), q) || strings.Contains(strings.ToLower(m.Description), q) ||
			slices.ContainsFunc(m.Keywords, func(k string) bool { return strings.Contains(strings.ToLower(k), q) })
		if match {
			kw := []string{}
			for _, k := range m.Keywords {
				kw = append(kw, untrusted.Text(k, untrusted.MaxName))
			}
			hits = append(hits, SearchHit{Name: m.Name, Description: untrusted.Text(m.Description, 4*untrusted.MaxName),
				Version: untrusted.Text(m.Version, untrusted.MaxName), Keywords: kw, Where: "cache", Install: m.Name})
		}
	}
	if !markets {
		return hits, warnings, nil
	}
	groups, unread, err := e.MarketGroups(display)
	if err != nil {
		return hits, append(warnings, "your marketplaces cannot be read, so search lists your cache alone: "+shown(untrusted.CleanErr(err).Error())), nil
	}
	for _, g := range groups {
		for _, o := range g.Entries {
			if !o.Cached && o.matches(q) {
				hits = append(hits, SearchHit{Name: o.Name, Description: o.Description, Version: o.Version, Keywords: o.Tags, Where: "@" + g.Name,
					Install: o.Install})
			}
		}
	}
	return hits, append(warnings, unread...), nil
}
