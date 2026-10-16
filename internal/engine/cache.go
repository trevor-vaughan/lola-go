package engine

import (
	"context"
	"fmt"
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

// SearchHit is one module matching a search.
type SearchHit struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Version     string   `json:"version"`
	Keywords    []string `json:"keywords"`
	Where       string   `json:"where"` // "cache" in this build; markets add "@<market>"
}

// NotStored is the error for a module that is not in the cache.
func NotStored(name string) error {
	return errs.WithHints(fmt.Errorf("%s is not in your cache", name), "cached modules: lola cache list")
}

// Current returns a stored module's origin and the version used for its metadata: the
// default-ref head if there is one, else the first head.
func (e *Engine) Current(name string) (store.Version, store.Origin, error) {
	o, found, err := e.Store.Origin(name)
	if err != nil {
		return store.Version{}, store.Origin{}, err
	}
	if !found || len(o.Heads) == 0 {
		return store.Version{}, store.Origin{}, NotStored(name)
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

// Update re-fetches every ref that has a head, for the named modules or all of them (§6.2).
func (e *Engine) Update(ctx context.Context, names []string) ([]HeadUpdate, error) {
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
			return nil, NotStored(n)
		}
		for _, h := range o.Heads {
			v, err := e.Fetch(ctx, source.Spec{Kind: source.Kind(o.Type), Location: o.Source, Path: o.Path, Ref: h.Ref})
			if err != nil {
				return nil, fmt.Errorf("updating %s: %w", n, err)
			}
			if v.Name != n {
				return nil, fmt.Errorf("updating %s: its source now declares module %q (cached under that name)", n, v.Name)
			}
			out = append(out, HeadUpdate{Name: n, Ref: h.Ref, From: h.Digest, To: v.ID, Changed: v.ID != h.Digest})
		}
	}
	return out, nil
}

// Search matches query case-insensitively against stored modules' names, descriptions and
// keywords. Unloadable modules are not searched; they come back as warnings (see CachedModules).
func (e *Engine) Search(query string) ([]SearchHit, []string, error) {
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
				Version: untrusted.Text(m.Version, untrusted.MaxName), Keywords: kw, Where: "cache"})
		}
	}
	return hits, warnings, nil
}
