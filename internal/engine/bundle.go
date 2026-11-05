package engine

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/activeprofile"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/trust"
)

// maxNesting is how deep bundles nest (spec 2026-10-29 §3.3): a bundle on, and seven inside it.
const maxNesting = 8

// bundleRef is a bundle a member layer comes through (spec 2026-10-29 §3.3, §3.10): its qualified
// name, its trust part when it is the project's (its name; "" for one of yours), and its member
// list as its file writes it, which a project bundle's trust records.
type bundleRef struct {
	name    string
	key     string
	members []string
}

// listing is a bundle's file that lists a member, and the member as that file writes it.
type listing struct {
	path, typed string
}

// expansion is one active profile's expansion into layers (spec 2026-10-29 §3.3), within one
// loadStack: load and remote read a profile file once each (Plan 6b §19 S6), seen holds the
// profiles placed and the bundles expanded so far, noted the profiles a notice has said appear
// twice, placed and bundles count the profiles placed and the bundles expanded, and notices what
// the expansion noted.
type expansion struct {
	e       *Engine
	sc      Scope
	infos   []profile.Info
	load    func(profile.Info) (Layer, error)
	remote  func(profile.Info) (Layer, error)
	seen    map[string]bool
	noted   map[string]bool
	placed  int
	bundles int
	notices []Notice
	left    []leftOut            // the remote members left out, once each, which a restore brings back
	bundle  bool                 // the profile expanded is a bundle
	project []bundleRef          // the project bundles expanded, once each: their member lists are trust content
	listed  map[string][]listing // each member reached (profile or bundle), by qualified name: the bundle files that list it
}

// expand is the layers i gives, reached through the bundles via (the one on first; none for the
// profile on itself) by the member whose refusals start with at ("" for the profile on): a
// profile's one layer, or a bundle's members' layers depth-first in its
// order. A profile or bundle reached again keeps its first place, with one notice for its name
// however often it is reached, and a bundle is expanded once, so a pulled project's bundles that
// share members cost the files they have, never the paths through them; a member that names a
// bundle of via, or the bundle itself, closes a cycle; nesting deeper than maxNesting, more than
// activeprofile.Max profiles placed, or more than activeprofile.Max bundles expanded (so a pulled
// project's files are bounded too) is refused. Each refusal names the file and line of the member
// it is about. A remote member this machine lacks and pins.yaml records is left out with a
// warning, as a profile on whose file is gone counts as off, until a run that needs it restores it
// (restoreRemotes).
func (x *expansion) expand(i profile.Info, via []bundleRef, at string) ([]Layer, error) {
	var l Layer
	var err error
	if i.Kind == profile.KindRemote {
		l, err = x.remote(i)
	} else {
		l, err = x.load(i)
	}
	if err != nil {
		return nil, err
	}
	if l.read == nil || l.read.Shape() != profile.ShapeBundle {
		x.place(&l, via)
		return []Layer{l}, nil
	}
	b := l.read
	x.bundle = x.bundle || len(via) == 0
	if len(via) == maxNesting {
		return nil, fmt.Errorf("%s%s nests bundles more than %d deep", at, shown(bareName(i.Qualified())), maxNesting)
	}
	ref := bundleRef{name: i.Qualified(), members: b.Bundle}
	if i.Kind == profile.KindProject {
		ref.key = i.Name
		x.project = append(x.project, ref)
	}
	if x.bundles++; x.bundles > activeprofile.Max {
		return nil, fmt.Errorf("%s%s takes bundle %s past %d bundles", at, shown(bareName(i.Qualified())), bundleLabel(x.infos, firstName(via)), activeprofile.Max)
	}
	inner := append(slices.Clone(via), ref)
	x.seen[ref.name] = true
	var out []Layer
	for k := range b.Bundle {
		line := fmt.Sprintf("%s:%d: bundle: ", shown(i.Path), b.MemberLine(k))
		m, ok, err := x.member(i, b, k)
		if errors.Is(err, errPassedOver) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !ok {
			x.leaveOut(b.Bundle[k], inner)
			continue
		}
		q := m.Qualified()
		if x.listed == nil {
			x.listed = map[string][]listing{}
		}
		if at := (listing{path: i.Path, typed: b.Bundle[k]}); !slices.Contains(x.listed[q], at) {
			x.listed[q] = append(x.listed[q], at)
		}
		if slices.ContainsFunc(inner, func(r bundleRef) bool { return r.name == q }) {
			chain := make([]string, 0, len(inner)+1)
			for _, r := range inner {
				chain = append(chain, bundleLabel(x.infos, r.name))
			}
			return nil, fmt.Errorf("%s%s closes a cycle: %s → %s", line, shown(b.Bundle[k]), strings.Join(chain, " → "), bundleLabel(x.infos, q))
		}
		if x.seen[q] {
			if x.noted[q] {
				continue
			}
			x.noted[q] = true
			text := shown(m.Name) + " appears twice in bundle " + bundleLabel(x.infos, firstName(inner))
			if len(inner) > 1 {
				text += " (via " + bundleLabel(x.infos, ref.name) + ")"
			}
			x.notices = append(x.notices, Notice{Level: LevelInfo, Text: text + "; its first place counts"})
			continue
		}
		ls, err := x.expand(m, inner, line)
		if err != nil {
			return nil, err
		}
		if x.placed > activeprofile.Max {
			return nil, fmt.Errorf("%s%s takes bundle %s past %d profiles", line, shown(b.Bundle[k]), bundleLabel(x.infos, firstName(inner)), activeprofile.Max)
		}
		out = append(out, ls...)
	}
	return out, nil
}

// leftOut is a remote member that member left out: its qualified name, the number of members
// placed before it (its position among them) and the bundles it comes through.
type leftOut struct {
	name  string
	at    int
	chain []bundleRef
}

// leaveOut records the remote member typed, which member left out of the bundles via, once however
// often it is named.
func (x *expansion) leaveOut(typed string, via []bundleRef) {
	q := string(profile.KindRemote) + ":" + strings.TrimPrefix(typed, string(profile.KindRemote)+":")
	if !slices.ContainsFunc(x.left, func(l leftOut) bool { return l.name == q }) {
		x.left = append(x.left, leftOut{name: q, at: x.placed, chain: via})
	}
}

// firstName is the qualified name of the bundle on, the first of via.
func firstName(via []bundleRef) string { return via[0].name }

// place marks l, a profile layer, as reached through via, and counts it: its Bundle is the bundle
// on, and a remote member that a project bundle names is trust content of that bundle (spec
// 2026-10-29 §3.10): its key is the bundle's, and each entry it gives is recorded with the remote
// profile's name, source and pin (Layer.member).
func (x *expansion) place(l *Layer, via []bundleRef) {
	x.placed++
	x.seen[strings.TrimPrefix(l.Name, ledger.FromProfile(""))] = true
	if len(via) == 0 {
		return
	}
	l.Bundle, l.chain = firstName(via), via
	if inner := via[len(via)-1]; inner.key != "" && remoteLayer(l.Name) && l.origin != nil {
		l.Key = inner.key
		l.member = &trust.MemberView{Name: strings.TrimPrefix(l.Name, ledger.FromProfile("")), Source: l.origin.Source, Commit: l.origin.Commit,
			Digest: l.origin.Digest}
	}
}

// member is the profile the k-th name of b, the bundle read from i, names at x's scope (spec
// 2026-10-29 §3.3). In a bundle of yours a bare name is your profile, else a remote one (local beats
// remote), never the project's: project:<name> names that, at its project only; in a project's
// bundle a bare name is the project's profile, else a remote one, and user: was refused at load. A
// remote profile this machine lacks and pins.yaml records is left out (ok false), with a warning.
// A name that resolves to none is refused: in a project's bundle with one text whether it is
// missing or a name of yours, so the file cannot probe what you have; while a run mends another
// bundle file (Engine.recovering) it is passed over instead (errPassedOver), with that text as a
// warning naming its take-out.
func (x *expansion) member(i profile.Info, b *profile.Profile, k int) (_ profile.Info, ok bool, _ error) {
	typed := b.Bundle[k]
	kind, name, qualified := strings.Cut(typed, ":")
	if !qualified {
		kind, name = "", typed
	}
	find := func(kind profile.Kind) int {
		return slices.IndexFunc(x.infos, func(o profile.Info) bool { return o.Kind == kind && o.Name == name })
	}
	local := profile.KindUser
	if i.Kind == profile.KindProject {
		local = profile.KindProject
	}
	at := -1
	switch kind {
	case "":
		if at = find(local); at < 0 {
			at = find(profile.KindRemote)
		}
	case string(profile.KindUser), string(profile.KindProject), string(profile.KindRemote):
		at = find(profile.Kind(kind))
	}
	if at >= 0 {
		return x.infos[at], true, nil
	}
	if (kind == "" || kind == string(profile.KindRemote)) && x.e.recorded(string(profile.KindRemote)+":"+name) {
		why := ", which is not on this machine yet; it is left out until a run that needs it restores it from pins.yaml"
		if way := x.e.inTheWay(name); way != "" {
			why = ", which pins.yaml records but " + way + " is in the way of; it is left out until that is moved aside and a run that needs it restores it"
		}
		x.notices = append(x.notices, Notice{Level: LevelWarn, Text: shown(i.Qualified()) + "'s member remote:" + shown(name) + why})
		return profile.Info{}, false, nil
	}
	line := fmt.Sprintf("%s:%d: bundle: %s ", shown(i.Path), b.MemberLine(k), shown(typed))
	out := "or " + unlist([]listing{{path: i.Path, typed: typed}}) // a project's file is the team's to edit
	if i.Kind == profile.KindUser {
		out = "or take it out: " + x.e.unbundleRun(x.sc, x.infos, i, b.Bundle, k, x.e.Display)
	}
	why := "is not a profile of yours or a remote profile you have"
	switch {
	case i.Kind == profile.KindProject:
		why = "is not a profile of this project or a remote profile you have"
	case kind == string(profile.KindProject) && x.sc.Kind == target.User:
		why = "is a project's profile, which a bundle can use in its project only"
	case kind == string(profile.KindProject):
		why = "is not a profile of this project"
	case kind == string(profile.KindUser):
		why = "is not a profile of yours"
	case kind == string(profile.KindRemote):
		why = "is not a remote profile you have"
	}
	// A run that mends a bundle passes over another bundle file's broken member (Engine.recovering).
	if r := x.e.recovering; r != nil && !r.judged[i.Qualified()] {
		x.notices = append(x.notices, Notice{Level: LevelWarn, Text: line + why + "; it is left out until you " + strings.TrimPrefix(out, "or ")})
		return profile.Info{}, false, errPassedOver
	}
	return profile.Info{}, false, errs.WithHints(errors.New(line+why), "see the profiles: "+x.e.Command(x.sc, "profile", x.e.Display...), out)
}

// errPassedOver is member's answer for a member that resolves to no profile in a bundle file a run
// that mends another passes over (Engine.recovering): it is left out, with its warning noted.
var errPassedOver = errors.New("member passed over")

// unbundleRun is the run that takes the k-th of members, the members of i, a bundle of yours sc can
// use (infos), as its file writes them, out of it (spec 2026-10-29 §6.2 --edit --bundle), keeping
// again.
func (e *Engine) unbundleRun(sc Scope, infos []profile.Info, i profile.Info, members []string, k int, again []string) string {
	rest := slices.Delete(slices.Clone(members), k, k+1)
	return e.Command(sc, "profile", append([]string{editName(infos, i), "--edit", "--bundle", strings.Join(rest, ",")}, again...)...)
}

// unlistRuns is, for each of listed, a bundle file that lists a member, the run that takes it out
// when the file is a bundle of yours sc can use (unbundleRun), else the edit it takes (unlist): a
// project's file is the team's to edit.
func (e *Engine) unlistRuns(sc Scope, listed []listing, again []string) []string {
	infos, _, err := e.visible(sc)
	var out []string
	var rest []listing
	for _, l := range listed {
		k := slices.IndexFunc(infos, func(i profile.Info) bool { return i.Path == l.path && i.Kind == profile.KindUser })
		if err != nil || k < 0 {
			rest = append(rest, l)
			continue
		}
		p, _, rerr := e.readFile(infos[k])
		at := -1
		if rerr == nil {
			at = slices.Index(p.Bundle, l.typed)
		}
		if at < 0 {
			rest = append(rest, l)
			continue
		}
		out = append(out, "or take "+shown(l.typed)+" out of bundle "+bundleLabel(infos, infos[k].Qualified())+": "+e.unbundleRun(sc, infos, infos[k], p.Bundle, at, again))
	}
	if len(rest) > 0 {
		out = append(out, "or "+unlist(rest))
	}
	return out
}

// bareName is a qualified name's name, the kind left out.
func bareName(q string) string {
	_, name, _ := strings.Cut(q, ":")
	return name
}

// bundleLabel is how a notice names the bundle q (qualified): its bare name, unless a profile of
// another kind has that name too (layerLabel's rule).
func bundleLabel(infos []profile.Info, q string) string {
	return strings.TrimPrefix(layerLabel(infos, ledger.FromProfile(q)), "profile ")
}

// members is the expansion of i, a profile sc can use (infos), read for names and shapes alone
// (spec 2026-10-29 §3.8, §8.8): the context line and the templates' profiles list name a bundle's
// members without loading their entries. Its layers carry each member's name, file and the
// bundles it comes through; a remote member is not read. x.bundle says i is a bundle, x.left the
// remote members this machine lacks that pins.yaml records.
func (e *Engine) members(sc Scope, infos []profile.Info, i profile.Info) ([]Layer, *expansion, error) {
	name := func(i profile.Info) Layer {
		return Layer{Name: ledger.FromProfile(i.Qualified()), Path: i.Path, Profile: true}
	}
	x := &expansion{e: e, sc: sc, infos: infos, seen: map[string]bool{}, noted: map[string]bool{},
		load: func(i profile.Info) (Layer, error) {
			l := name(i)
			p, _, err := e.readFile(i)
			l.read = p
			return l, err
		},
		remote: func(i profile.Info) (Layer, error) { return name(i), nil }}
	ls, err := x.expand(i, nil, "")
	return ls, x, err
}
