package engine

import (
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// ContextLayer is one file of a scope's context (spec 2026-10-29 §3.8): Name as JSON names it (a
// profile by its qualified name, the base by its files), Kind (base, user, project or remote),
// Path its file ("" for a profile whose file is gone), and Label how the context line names it: a
// profile by its bare name unless a profile of another kind has that name too. Every string has
// been through untrusted.Text.
type ContextLayer struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Path  string `json:"path"`
	Label string `json:"-"`
	// Via is, for a bundle's member, the bundles inside the one on that it comes through, by label,
	// joined with " › " ("" for a member the bundle on names itself): the line groups them.
	Via string `json:"-"`
	// Left is, for a bundle's member, that it is a remote profile this machine lacks and pins.yaml
	// records: it is left out until a run restores it.
	Left bool `json:"-"`
}

// ContextProfile is the profile on in a scope: its layer, whether it is a bundle, and a bundle's
// members as it expands them, in order (spec 2026-10-29 §3.3), [] for a profile.
type ContextProfile struct {
	ContextLayer
	Bundle  bool           `json:"bundle"`
	Members []ContextLayer `json:"members"`
}

// ScopeContext is what a scope command says of each scope it acts on (spec 2026-10-29 §3.8,
// §8.3): the scope, its root ("" at user scope), its base, the profile on over it (nil for none),
// the file an edit writes (nil for a command that edits none), Restore, the command that restores
// the profile on, a remote profile this machine lacks and pins.yaml records ("" for any other),
// and Error, why lola could not tell the profile on or read its file, "" when it could.
type ScopeContext struct {
	Scope    string          `json:"scope"`
	Root     string          `json:"root"`
	Base     ContextLayer    `json:"base"`
	Profile  *ContextProfile `json:"profile"`
	WritesTo *ContextLayer   `json:"writes_to"`
	Restore  string          `json:"restore,omitempty"`
	Error    string          `json:"error,omitempty"`
}

// contextBase is the kind of a scope's base in its context.
const contextBase = "base"

// Context is sc's context (spec 2026-10-29 §3.8), read once the scope is resolved and before the
// command reads the scope: from lock.json, the listing of the profiles sc can use and the active
// profile's file, read once with the loader its kind takes (readProfile). The base is project.yml,
// or at user scope default (default.yml). writes is what a command that edits the scope's files
// (install, uninstall) was told about where its edit goes, nil for any other command: WritesTo
// names that file (writesTo). What lola cannot read, a profile file that does not load included,
// is Error, never a refusal: a command that reads it refuses it, with its hints, and one that does
// not (tidy, repair) still runs.
func (e *Engine) Context(sc Scope, writes *TargetRequest) ScopeContext {
	man, name := e.basePath(sc), e.manifestName(sc)
	if sc.Kind == target.User {
		name = profile.DefaultName
	}
	c := ScopeContext{Scope: sc.Kind.String(), Root: shown(root(sc)),
		Base: ContextLayer{Name: name, Kind: contextBase, Path: shown(man), Label: name}}
	var infos []profile.Info
	done := func() ScopeContext {
		if writes != nil && writes.Profile != "" && infos == nil {
			var err error
			if infos, _, err = e.visible(sc); err != nil {
				return c
			}
		}
		c.WritesTo = e.writesTo(sc, c, writes, infos)
		return c
	}
	fail := func(err error) ScopeContext {
		c.Error = shown(untrusted.CleanErr(err).Error())
		return c
	}
	if err := e.refuseSymlinkedState(sc); err != nil {
		return fail(err)
	}
	// Strays are the command's to refuse (Engine.Ledger); the profile on is all the context needs.
	led, err := ledger.LoadKeepingStrays(e.files(sc).ledger)
	if err != nil {
		return fail(err)
	}
	if led.Profile == nil {
		return done()
	}
	q := led.Profile.Name
	kind, _, _ := strings.Cut(q, ":")
	p := &ContextProfile{ContextLayer: ContextLayer{Name: shown(q), Kind: kind, Label: shown(q)}, Members: []ContextLayer{}}
	c.Profile = p
	if infos, _, err = e.visible(sc); err != nil {
		return fail(err)
	}
	k := activeInfo(infos, q)
	if k < 0 {
		// A remote profile pins.yaml records is not gone: a run that needs it restores it (§3.4).
		if e.recorded(q) {
			if at := e.inTheWay(strings.TrimPrefix(q, string(profile.KindRemote)+":")); at != "" {
				c.Error = at + " is in the way of it; move it aside"
				return c
			}
			c.Restore = e.SyncCommand(sc)
			return done()
		}
		c.Error = "profile " + shown(q) + " no longer exists"
		return c
	}
	in := infos[k]
	p.Name, p.Kind, p.Path, p.Label = shown(in.Qualified()), string(in.Kind), shown(in.Path), shown(in.Qualified())
	if !slices.ContainsFunc(infos, func(o profile.Info) bool { return o.Name == in.Name && o.Kind != in.Kind }) {
		p.Label = shown(in.Name)
	}
	read, _, err := e.readProfile(in)
	if err != nil {
		return fail(err)
	}
	if read.Shape() == profile.ShapeBundle {
		ms, x, err := e.members(sc, infos, in)
		if err != nil {
			return fail(err)
		}
		p.Bundle = true
		p.Members = bundleMembers(infos, ms, x)
		if len(x.left) > 0 {
			// A remote member pins.yaml records is not gone: a run that needs it restores it (§3.4).
			c.Restore = e.SyncCommand(sc)
		}
	}
	return done()
}

// memberContext is m, a bundle's member layer (Engine.members), as the context names it: a remote
// one qualified, any other by its bare name unless a profile of another kind has it (layerLabel),
// with the bundles below the one on that it comes through.
func memberContext(infos []profile.Info, m Layer) ContextLayer {
	q := strings.TrimPrefix(m.Name, ledger.FromProfile(""))
	kind, _, _ := strings.Cut(q, ":")
	return ContextLayer{Name: shown(q), Kind: kind, Path: shown(m.Path), Label: strings.TrimPrefix(layerLabel(infos, m.Name), "profile "),
		Via: viaLabel(infos, m.chain)}
}

// bundleMembers is the members of a bundle as the context and lola profile <bundle> list them: the
// placed ones (ms) and the ones x left out, in the order the bundle names them.
func bundleMembers(infos []profile.Info, ms []Layer, x *expansion) []ContextLayer {
	out := []ContextLayer{}
	for k, m := range ms {
		out = append(out, x.leftBefore(infos, k)...)
		out = append(out, memberContext(infos, m))
	}
	return append(out, x.leftBefore(infos, len(ms))...)
}

// viaLabel is the bundles below the one on in chain, by label, joined with " › ".
func viaLabel(infos []profile.Info, chain []bundleRef) string {
	var via []string
	for _, b := range chain[1:] {
		via = append(via, bundleLabel(infos, b.name))
	}
	return strings.Join(via, " › ")
}

// leftBefore is the members x left out that stand before the k-th member placed, as the context
// names them: qualified, for a remote profile this machine lacks.
func (x *expansion) leftBefore(infos []profile.Info, k int) []ContextLayer {
	var out []ContextLayer
	for _, l := range x.left {
		if l.at == k {
			out = append(out, ContextLayer{Name: shown(l.name), Kind: string(profile.KindRemote), Label: shown(l.name), Via: viaLabel(infos, l.chain), Left: true})
		}
	}
	return out
}

// writesTo is the file an edit of sc would write, as the context names it (spec 2026-10-29 §3.8):
// nil when the run writes nothing (writes nil), when that file needs a question (a remote profile
// or a bundle on), or when the --profile named cannot be written (the command refuses it, with hints); the base
// with no profile on, or for --profile base (default, or user:default, at user scope). infos is the
// scope's listing (nil until read) and c's Profile the profile on (nil for none).
func (e *Engine) writesTo(sc Scope, c ScopeContext, writes *TargetRequest, infos []profile.Info) *ContextLayer {
	if writes == nil {
		return nil
	}
	name := writes.Profile
	isBase, isDefault := namesBase(name)
	switch {
	case isBase, name == "" && c.Profile == nil, sc.Kind == target.User && isDefault:
		b := c.Base
		return &b
	case name == "" && (c.Profile.Kind == string(profile.KindRemote) || c.Profile.Bundle):
		return nil // the write target needs a question (spec 2026-10-29 §3.5 rows 3 and 4)
	case name == "":
		l := c.Profile.ContextLayer
		return &l
	}
	i, err := e.resolveProfile(sc, infos, name, nil)
	if err != nil || i.Kind == profile.KindRemote {
		return nil
	}
	label := shown(i.Name)
	if slices.ContainsFunc(infos, func(o profile.Info) bool { return o.Name == i.Name && o.Kind != i.Kind }) {
		label = shown(i.Qualified())
	}
	return &ContextLayer{Name: shown(i.Qualified()), Kind: string(i.Kind), Path: shown(i.Path), Label: label}
}
