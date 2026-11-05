package engine

import (
	"errors"
	"slices"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/market"
)

// CoachUnresolved is err with the hints of Plan 6c §6.7 when it refuses a file whose entries have
// a marketplace address as their source (*manifest.UnresolvedError, ruling R-6c-7), and err as it
// is otherwise. scopes are the run's, whose manifests are the files hint (a) can name; dry and
// display, the run's -n and output flags, are what the hints keep (§11.3). Each entry gets
// unresolvedHint's hint, said once; an entry with none keeps the message's "edit the entry". An
// error a reader already coached is returned as it is.
func (e *Engine) CoachUnresolved(err error, scopes []Scope, dry bool, display []string) error {
	ue, ok := errors.AsType[*manifest.UnresolvedError](err)
	if !ok || slices.ContainsFunc(ue.Entries, func(u manifest.Unresolved) bool { return u.Coached }) {
		return err
	}
	reg, _, rerr := e.registry()
	if rerr != nil {
		// Which marketplaces you have is unknown: no hint can be told to work.
		return err
	}
	var in *Scope
	for i := range scopes {
		if e.files(scopes[i]).manifest == ue.Path {
			in = &scopes[i]
		}
	}
	var hints []string
	for i := range ue.Entries {
		h := e.unresolvedHint(reg, in, ue.Entries[i], dry, display)
		if h == "" {
			continue
		}
		ue.Entries[i].Coached = true
		if !slices.Contains(hints, h) {
			hints = append(hints, h)
		}
	}
	return errs.WithHints(err, hints...)
}

// unresolvedHint is the command that fixes u, an entry of a file whose source is a marketplace
// address (Plan 6c §6.7), for a reader whose marketplaces are reg, or "" for none: (a) in the
// manifest of in (nil for a profile), when you have the marketplace and the entry has the name
// the address installs, lola install @m/x at in's scope, keeping -n (dry) and display, which
// replaces the entry (§6.3); (b) in a profile, or for an entry named otherwise, which that
// install would not replace, lola market m, which shows the entry's source, ref and path to copy
// into the file; (c) none without the marketplace.
func (e *Engine) unresolvedHint(reg market.Registry, in *Scope, u manifest.Unresolved, dry bool, display []string) string {
	if reg.Find(u.Market) == nil {
		return ""
	}
	if in == nil || u.Name != u.Module {
		return MarketRun(append([]string{u.Market}, display...)...)
	}
	args := []string{market.Address{Market: u.Market, Entry: u.Module}.String()}
	if dry {
		args = append(args, "-n")
	}
	return e.Command(*in, "install", append(args, display...)...)
}

// unresolvedNotices are the warnings of an install that replaced some of the unresolved entries
// of sc's manifest (Plan 6c §6.3), one for each of others, those it left as written, with its own
// hint (unresolvedHint) or, without one, the message saying to edit it.
func (e *Engine) unresolvedNotices(sc Scope, others []manifest.Unresolved, dry bool, display []string) []Notice {
	reg, _, err := e.registry()
	if err != nil {
		reg = market.Registry{}
	}
	var out []Notice
	for _, u := range others {
		h := e.unresolvedHint(reg, &sc, u, dry, display)
		u.Coached = h != ""
		text := (&manifest.UnresolvedError{Path: e.files(sc).manifest, Entries: []manifest.Unresolved{u}}).Error()
		if h != "" {
			text += " → " + h
		}
		out = append(out, Notice{Level: LevelWarn, Text: text})
	}
	return out
}

// holdsAddress reports whether man, as manifestReplacing reads it, still records a marketplace
// address as a source, a replaced entry included: lola sync refuses such a file (Plan 6c §6.7), so
// no hint may name a sync of it until the install has run.
func holdsAddress(man *manifest.Manifest) bool {
	return slices.ContainsFunc(man.Modules, func(ent manifest.Entry) bool {
		_, _, ok := manifest.Address(ent.Source)
		return ok
	})
}

// replacingNames are the modules the marketplace addresses among reqs install, each named after
// its catalog entry: the unresolved entries of those names an install of reqs replaces (Plan 6c
// §6.3).
func replacingNames(reqs []InstallRequest) []string {
	var out []string
	for _, r := range reqs {
		if a, ok, err := market.ParseAddress(r.Arg); ok && err == nil && a.Entry != "" {
			out = append(out, a.Entry)
		}
	}
	return out
}

// InstallManifest is sc's manifest as an install of reqs reads it: as Manifest reads it, except
// that a manifest whose unresolved entries (Plan 6c §6.7) the addresses among reqs replace is read
// as written (loadReplacing), since that install may run.
func (e *Engine) InstallManifest(sc Scope, reqs []InstallRequest) (*manifest.Manifest, error) {
	man, _, err := e.manifestReplacing(sc, replacingNames(reqs))
	return man, err
}

// manifestReplacing is Manifest for an install whose marketplace addresses install the modules
// named: a manifest whose unresolved entries (Plan 6c §6.7) include one of names is read as
// written, with its other unresolved entries, which that install leaves as they are (§6.3). Any
// other manifest with unresolved entries is refused, as Manifest refuses it.
func (e *Engine) manifestReplacing(sc Scope, names []string) (*manifest.Manifest, []manifest.Unresolved, error) {
	man, err := e.Manifest(sc)
	ue, ok := errors.AsType[*manifest.UnresolvedError](err)
	replaced := func(u manifest.Unresolved) bool { return slices.Contains(names, u.Name) }
	if !ok || !slices.ContainsFunc(ue.Entries, replaced) {
		return man, nil, err
	}
	return ue.Manifest, slices.DeleteFunc(slices.Clone(ue.Entries), replaced), nil
}

// outputOnly is the output flags among flags, a run's flags as a hint repeats them: what a hint
// for another command keeps (Plan 6c §11.3).
func outputOnly(flags []string) []string {
	return slices.DeleteFunc(slices.Clone(flags), func(f string) bool { return f != "--json" && f != "--plain" && f != "--no-pager" })
}
