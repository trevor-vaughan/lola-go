package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/trust"
)

// Created is a profile file a run made: Profile its
// qualified name and Path its file; Bundle a bundle's members as written; From the profile it is a
// copy of, with Modules the entries it got. Every string has been through untrusted.Text.
type Created struct {
	Profile string   `json:"profile"`
	Path    string   `json:"path"`
	Bundle  []string `json:"bundle,omitempty"`
	From    string   `json:"from,omitempty"`
	// FromLabel is From as a line names it: bare, qualified only beside a namesake (layerLabel).
	FromLabel string `json:"-"`
	Modules   int    `json:"modules"`
}

// profileWrites is the profile files a run makes or rewrites in its apply's transaction: files, each with the bytes lola writes and the check run
// just before the write; read, each file's profile as those bytes parse, which the run's layers
// read in place of the disk (Engine.readFile); infos, the profiles of the files it makes, which
// the scope's listing lacks until they exist (Engine.visible); made, what the result says it made;
// and trusted, per project file, what lola writes there that the record takes as trusted before
// the run's check (decisions line 47): what it wrote on the user's command, or what the record
// already covered where it copied from.
type profileWrites struct {
	files   []fileEdit
	read    map[string]*profile.Profile
	infos   []profile.Info
	made    []Created
	trusted map[string]trust.View
}

// readFile reads i as readProfile does, or, for a file the run makes (e.creating), the profile its
// bytes parse to, beside the directory its relative sources are resolved against.
func (e *Engine) readFile(i profile.Info) (*profile.Profile, string, error) {
	if e.creating != nil {
		if p, ok := e.creating.read[i.Path]; ok {
			dir := profile.ProjectDir
			if i.Kind != profile.KindProject {
				dir = filepath.Dir(i.Path)
			}
			return p, dir, nil
		}
	}
	return e.readProfile(i)
}

// add records the file lola makes at path for the profile kind:name: its bytes, as p parses them,
// written only while path is still missing (in a project, while .lola/profiles is the project's
// own), and c, what the result says of it.
func (w *profileWrites) add(sc Scope, kind profile.Kind, name, path string, data []byte, p *profile.Profile, c Created) {
	l := Layer{Name: ledger.FromProfile(string(kind) + ":" + name), Path: path, Profile: true}
	var none *profile.Profile
	check := func() error { return none.Current(path) }
	if kind == profile.KindProject {
		l.Key = name
		check = func() error { return none.Unchanged(sc.ProjectDir, path) }
	}
	w.files = append(w.files, fileEdit{layer: l, data: data, check: check})
	w.read[path], w.infos, w.made = p, append(w.infos, profile.Info{Name: name, Kind: kind, Path: path}), append(w.made, c)
}

// newProfiles is what lola profile <name> --switch --new writes at sc: the
// profile ch.Name names, yours or with project:<name> the project's, which must not exist; empty,
// a copy of ch.From's entries and top-level targets (no pin into a profile of yours), or with
// ch.Bundle a bundle of those members, each that names no profile made too, empty (newMembers).
// infos is what sc can use; runs words the hints (switchRuns for --switch, editRuns for --edit),
// display is the run's output flags.
func (e *Engine) newProfiles(ctx context.Context, sc Scope, infos []profile.Info, ch ProfileChange, runs newRuns, display []string) (*profileWrites, error) {
	k, name, err := e.newTarget(sc, infos, ch, runs)
	if err != nil {
		return nil, err
	}
	kind := string(k)
	w := &profileWrites{read: map[string]*profile.Profile{}, trusted: map[string]trust.View{}}
	path := e.profilePath(sc, k, name)
	c := Created{Profile: shown(kind + ":" + name), Path: shown(path)}
	var data []byte
	keep := copied{targets: true, bundle: true, module: func(string) bool { return true }} // what you name is yours to trust
	switch {
	case ch.Bundle != nil:
		if err = e.newMembers(sc, infos, w, k, name, ch.Bundle, runs); err != nil {
			return nil, err
		}
		data, err = profile.NewBundle(ch.Bundle)
		c.Bundle = shownAll(ch.Bundle)
	case ch.From != "":
		data, keep, c, err = e.copyOf(ctx, sc, infos, k, path, ch.From, c, display)
	}
	if err != nil {
		return nil, err
	}
	data, p, err := madeFile(k, path, data)
	if err != nil {
		return nil, err
	}
	w.add(sc, k, name, path, data, p, c)
	if k == profile.KindProject {
		v := madeView(p)
		if !keep.targets {
			v.Targets = nil
		}
		if !keep.bundle {
			v.Bundle = nil
		}
		v.Modules = slices.DeleteFunc(v.Modules, func(ent trust.Entry) bool { return !keep.module(ent.Name) })
		w.trusted[name] = v
	}
	return w, nil
}

// madeFile is the file lola makes at path for a profile of kind: data, or for nil an empty
// profile, with the profile it parses to as that kind's files are read.
func madeFile(kind profile.Kind, path string, data []byte) ([]byte, *profile.Profile, error) {
	if data == nil {
		var none *profile.Profile
		var err error
		if _, data, err = none.Edit(path, manifest.Change{}); err != nil {
			return nil, nil, err
		}
	}
	parse := profile.Parse
	if kind == profile.KindProject {
		parse = profile.ParseProject
	}
	p, err := parse(path, data)
	return data, p, err
}

// newRuns is what --new's refusals hint (newTarget, refuseExisting, newMembers), as the run that
// makes a profile words them: make, that run making name instead, with modifiers (--from, --bundle);
// plain, the run that makes name and nothing else; use, the run that takes the existing profile q
// in its place, which useLabel introduces; list, the listing.
type newRuns struct {
	make     func(name string, modifiers ...string) string
	plain    func(name string) string
	use      func(q string) string
	useLabel string
	list     string
}

// switchRuns is newRuns for lola profile <name> --switch --new at sc, each run keeping again (what
// a hint naming another run keeps).
func (e *Engine) switchRuns(sc Scope, again []string) newRuns {
	run := func(args ...string) string { return e.Command(sc, "profile", append(args, again...)...) }
	return newRuns{make: func(name string, modifiers ...string) string {
		return run(append([]string{name, "--switch", "--new"}, modifiers...)...)
	},
		plain: func(name string) string { return run(name, "--switch", "--new") }, use: func(q string) string { return run(q, "--switch") },
		useLabel: "switch to it", list: e.listRun(sc, again)}
}

// listRun is lola profile at sc, keeping of again the output flags but not -n.
func (e *Engine) listRun(sc Scope, again []string) string {
	return e.Command(sc, "profile", slices.DeleteFunc(slices.Clone(again), func(a string) bool { return a == "-n" })...)
}

// newTarget is the kind and name of the profile --new makes for ch.Name at sc, refused as
// newProfiles refuses it: a remote profile, a project's outside a project, a kind lola has not,
// a name newName refuses, and one a profile sc can use (infos) has (refuseExisting). runs words
// the hints.
func (e *Engine) newTarget(sc Scope, infos []profile.Info, ch ProfileChange, runs newRuns) (profile.Kind, string, error) {
	modifiers := newModifiers(ch)
	kind, name, qualified := strings.Cut(ch.Name, ":")
	if !qualified {
		kind, name = string(profile.KindUser), ch.Name
	}
	switch {
	case kind == string(profile.KindRemote):
		return "", "", errs.WithHints(errors.New("--new makes a profile of yours or the project's; lola profile --add fetches a remote one"),
			"make it yours: "+runs.make(name, modifiers...))
	case kind == string(profile.KindProject) && sc.Kind != target.Project:
		return "", "", errs.WithHints(fmt.Errorf("%s is a project's profile; --new makes one in a project only", shown(ch.Name)),
			"make it yours: "+runs.make(name, modifiers...))
	case kind != string(profile.KindUser) && kind != string(profile.KindProject):
		return "", "", errs.WithHints(fmt.Errorf("%s is not user:, project: or remote: followed by a name", shown(ch.Name)), "see the profiles: "+runs.list)
	}
	if err := newName(name); err != nil {
		return "", "", err
	}
	if err := e.refuseExisting(sc, infos, name, ch, runs); err != nil {
		return "", "", err
	}
	return profile.Kind(kind), name, nil
}

// isDir reports whether i, a profile of yours or the project's, is a directory by its file name,
// which holds the name but which no switch reads.
func isDir(i profile.Info) bool {
	fi, err := os.Stat(i.Path)
	return err == nil && fi.IsDir() && i.Kind != profile.KindRemote
}

// NewOrSwitch decides what lola profile <ch.Name> --switch given --from or --bundle without --new
// means at sc, for the refusal's hint: exists when a switch resolves the name
// to a profile (the hint drops the modifier); else nil when --new would make it (the hint adds
// --new); else the refusal of both (the switch's when the name is ambiguous, else --new's), with
// their working hints; again is what those keep of the run.
func (e *Engine) NewOrSwitch(sc Scope, ch ProfileChange, again []string) (exists bool, err error) {
	infos, _, err := e.visible(sc)
	if err != nil {
		return false, err
	}
	i, err := e.resolveProfile(sc, infos, ch.Name, nil)
	if _, unknown := errors.AsType[*unknownProfileError](err); err != nil && !unknown {
		return false, err
	}
	if err == nil && !isDir(i) {
		return true, nil
	}
	_, _, err = e.newTarget(sc, infos, ch, e.switchRuns(sc, again))
	return false, err
}

// copied is what of a profile --from copies the record already trusts: its top-level targets, a
// bundle's members, and each entry by name.
type copied struct {
	targets, bundle bool
	module          func(name string) bool
}

// madeView is p, a project profile lola writes, as its part of the trust record holds it: its
// top-level targets, a bundle's members, and each entry with its source as the project reads it
// (beside .lola/profiles) and the profile's targets when it has none of its own.
func madeView(p *profile.Profile) trust.View {
	m := &manifest.Manifest{Targets: slices.Clone(p.Targets)}
	for _, ent := range p.Entries() {
		ent.Source = besideProfile(ent.Source, profile.ProjectDir)
		m.Modules = append(m.Modules, ent)
	}
	v := trust.ViewOf(m)
	v.Bundle = slices.Clone(p.Bundle)
	return v
}

// newModifiers is the --from or --bundle of ch, as a hint repeating the run gives them.
func newModifiers(ch ProfileChange) []string {
	switch {
	case ch.Bundle != nil:
		return []string{"--bundle", strings.Join(ch.Bundle, ",")}
	case ch.From != "":
		return []string{"--from", ch.From}
	}
	return nil
}

// newName refuses a name --new cannot give a profile: one that is no profile name, default (user
// scope's base, always there) and base (reserved for the scope's base).
func newName(name string) error {
	switch name {
	case profile.DefaultName:
		return errors.New("default is user scope's base, which always exists; name the profile something else")
	case profile.BaseName:
		return errors.New("base is reserved for the scope's base; name the profile something else")
	}
	return module.ValidateName("profile", name)
}

// refuseExisting refuses --new of name while a profile sc can use (infos) has that name, of any
// kind: a new one of yours would shadow a remote one, or be ambiguous beside the project's. The
// hints take each one that has it in its place (none a directory by its file name, which no run
// reads), then make one by a free name with the same modifiers, as runs words them.
func (e *Engine) refuseExisting(sc Scope, infos []profile.Info, name string, ch ProfileChange, runs newRuns) error {
	kind, _, qualified := strings.Cut(ch.Name, ":")
	var named, hints []string
	var beside profile.Kind // a local profile of the other kind, which a bare name would then share
	for _, i := range infos {
		if i.Name != name || i.Kind == profile.KindDefault {
			continue
		}
		if qualified && i.Kind != profile.KindRemote && string(i.Kind) != kind {
			beside = i.Kind
		}
		if isDir(i) {
			named = append(named, shown(i.Qualified())+" ("+shown(i.Path)+", a directory)")
			continue
		}
		named = append(named, shown(i.Qualified())+" ("+shown(i.Path)+")")
		hints = append(hints, runs.useLabel+": "+runs.use(i.Qualified()))
	}
	if len(named) == 0 {
		return nil
	}
	free := freeName(infos, name)
	if qualified {
		free = kind + ":" + free
	}
	hints = append(hints, "or make another: "+runs.make(free, newModifiers(ch)...))
	why := ""
	if beside != "" {
		why = fmt.Sprintf(" (%s beside %s would make a bare %s name two profiles)", shown(ch.Name), shown(string(beside)+":"+name), shown(name))
	}
	return errs.WithHints(fmt.Errorf("%s names %s; --new makes a profile that does not exist yet%s", shown(name), andJoin(named), why), hints...)
}

// newMembers checks the members of a bundle kind:name that --new --bundle makes: each resolves as the bundle will read it (expansion.member), and each bare one, or
// user:<name> in a bundle of yours, that names no profile is made empty, of the bundle's own kind,
// in w. A bundle naming itself, a project: member that names no profile here (making the project's
// shared file on the side is too big a side effect) and a remote profile
// you do not have are refused (the profile package refuses default, base, a name twice and user:
// in a project's bundle, naming the line, when the bundle's bytes are parsed). runs words the
// hints.
func (e *Engine) newMembers(sc Scope, infos []profile.Info, w *profileWrites, kind profile.Kind, name string, members []string, runs newRuns) error {
	for _, m := range members {
		mk, mn, qualified := strings.Cut(m, ":")
		if !qualified {
			mk, mn = "", m
		}
		if mn == name && (mk == "" || mk == string(kind)) {
			return fmt.Errorf("--bundle: %s is the bundle itself; a bundle cannot name itself", shown(m))
		}
		local := string(kind)
		has := func(k string) bool {
			return slices.ContainsFunc(infos, func(i profile.Info) bool { return string(i.Kind) == k && i.Name == mn }) ||
				slices.ContainsFunc(w.infos, func(i profile.Info) bool { return string(i.Kind) == k && i.Name == mn })
		}
		switch {
		case mk == "" && (has(local) || has(string(profile.KindRemote)) || e.recorded(string(profile.KindRemote)+":"+mn)):
		case mk != "" && has(mk), mk == string(profile.KindRemote) && e.recorded(m):
		case mk == string(profile.KindRemote):
			return errs.WithHints(fmt.Errorf("--bundle: %s is not a remote profile you have; lola profile --add fetches one", shown(m)),
				"see yours: "+runs.list)
		case mk == string(profile.KindProject):
			hint := "see the profiles: " + runs.list
			if sc.Kind == target.Project {
				hint = "make it first: " + runs.plain(m)
			}
			return errs.WithHints(fmt.Errorf("--bundle: %s is not a profile of this project", shown(m)), hint)
		case mk == "" || mk == local:
			if mn == profile.DefaultName || mn == profile.BaseName {
				continue // the bundle's parse refuses them, naming the line
			}
			if err := newName(mn); err != nil {
				return fmt.Errorf("--bundle: %w", err)
			}
			path := e.profilePath(sc, kind, mn)
			data, p, err := madeFile(kind, path, nil)
			if err != nil {
				return err
			}
			w.add(sc, kind, mn, path, data, p, Created{Profile: shown(string(kind) + ":" + mn), Path: shown(path)})
		}
	}
	return nil
}

// copyOf is the file --new --from makes at path for a profile of kind:
// from's top-level targets and entries, each as the new file records it (writeTo.fileEntry: no pin
// in a profile of yours, local sources in the form the new file reads them), or, from a bundle, a
// bundle of its members. keep is what the trust record takes as trusted in a new project profile:
// all of it from a profile of yours or a remote one, from a project profile what that profile's
// part of the record covers (an entry moves its trust with it, never more). From a project profile
// into a profile of yours, whose entries nothing asks about, it copies what a move would carry
// (outOfProject). c gains From, FromLabel and Modules.
func (e *Engine) copyOf(ctx context.Context, sc Scope, infos []profile.Info, kind profile.Kind, path, from string, c Created,
	display []string) ([]byte, copied, Created, error) {
	all := copied{targets: true, bundle: true, module: func(string) bool { return true }}
	i, err := e.resolveProfile(sc, infos, from, nil)
	if err != nil {
		return nil, all, c, err
	}
	src, dir, err := e.readProfile(i)
	if err != nil {
		return nil, all, c, e.mixedError(sc, infos, i, err)
	}
	c.From = shown(i.Qualified())
	c.FromLabel = profile.DefaultName // layerLabel names the default profile's layer "default (base)"
	if i.Kind != profile.KindDefault {
		c.FromLabel = strings.TrimPrefix(layerLabel(infos, ledger.FromProfile(i.Qualified())), "profile ")
	}
	keep := all
	if i.Kind == profile.KindProject {
		rec, err := e.trustRecord(sc)
		if err != nil {
			return nil, all, c, err
		}
		read := rec.View.Part(i.Name)
		was := map[string]manifest.Entry{}
		for _, ent := range src.Entries() {
			ent.Source = besideProfile(ent.Source, dir)
			was[ent.Name] = ent
		}
		keep = copied{targets: slices.Equal(read.Targets, src.Targets), bundle: slices.Equal(read.Bundle, src.Bundle),
			module: func(name string) bool { ent, ok := was[name]; return ok && read.Covers(ent) }}
	}
	if src.Shape() == profile.ShapeBundle {
		members := e.sameMembers(sc, infos, i, src, kind)
		data, err := profile.NewBundle(members)
		c.Bundle = shownAll(members)
		return data, keep, c, err
	}
	t := writeTo{Layer: Layer{Path: path, Profile: true}}
	if kind == profile.KindProject {
		t.Key = strings.TrimSuffix(filepath.Base(path), ".yml")
	}
	if i.Kind == profile.KindProject && kind != profile.KindProject && len(src.Modules) > 0 {
		if err := e.outOfProject(ctx, sc, i, src, t, display); err != nil {
			return nil, all, c, err
		}
	}
	ch := manifest.Change{Targets: slices.Clone(src.Targets)}
	for _, ent := range src.Modules {
		ent.Source = besideProfile(ent.Source, dir)
		ch.Set = append(ch.Set, t.fileEntry(sc, ent))
	}
	c.Modules = len(ch.Set)
	var none *profile.Profile
	_, data, err := none.Edit(path, ch)
	return data, keep, c, err
}

// outOfProject refuses the copy of src, i's profile as read, a project profile of sc's, into t, a
// profile of yours, unless a move of its entries there would go (decisions rows 90, 184): no local source inside a project (localInProject), and every entry installed as
// the project's own sync, which judges entries, targets and a folder's content, would leave it
// (installedAsTrusted). A profile that changed while it was judged is refused too.
func (e *Engine) outOfProject(ctx context.Context, sc Scope, i profile.Info, src *profile.Profile, t writeTo, display []string) error {
	st, err := e.moveStack(sc)
	if err != nil {
		return err
	}
	l, err := e.offLayer(i)
	if err != nil {
		return err
	}
	from := writeTo{Layer: l, label: layerLabel(st.infos, l.Name)}
	if k := slices.IndexFunc(st.layers, func(x Layer) bool { return x.Name == l.Name }); k >= 0 {
		from.Layer, from.on = st.layers[k], true
	}
	var modules []string
	for _, ent := range src.Modules {
		modules = append(modules, ent.Name)
	}
	if err := e.carriedOut(ctx, sc, t, layerOf(from), modules, display); err != nil {
		return err
	}
	if now, _, err := e.readProfile(i); err != nil || !reflect.DeepEqual(now.Modules, src.Modules) || !slices.Equal(now.Targets, src.Targets) {
		return errs.WithHints(errors.Join(fmt.Errorf("%s changed while lola was reading it; nothing was written", shown(i.Path)), err),
			"run the same command again")
	}
	return nil
}

// sameMembers is the members of src, i's bundle, as a new bundle of kind names them so that each
// names the profile it names in src (a bare name reads as the bundle's own kind first, yours or the
// project's): a bundle of yours names each as memberName does, a project's bundle copied from a
// project bundle keeps them as written, and from another kind names each qualified. A member src
// cannot resolve is kept as written, for the new bundle's expansion to refuse; one pins.yaml
// records but this machine lacks is the remote profile it is.
func (e *Engine) sameMembers(sc Scope, infos []profile.Info, i profile.Info, src *profile.Profile, kind profile.Kind) []string {
	if kind == profile.KindProject && i.Kind == profile.KindProject {
		return slices.Clone(src.Bundle)
	}
	x := &expansion{e: e, sc: sc, infos: infos, seen: map[string]bool{}, noted: map[string]bool{}}
	out := make([]string, len(src.Bundle))
	for k, typed := range src.Bundle {
		m, ok, err := x.member(i, src, k)
		switch {
		case err != nil:
			out[k] = typed
			continue
		case !ok:
			m = profile.Info{Kind: profile.KindRemote, Name: strings.TrimPrefix(typed, string(profile.KindRemote)+":")}
		}
		if kind == profile.KindUser {
			out[k] = memberName(infos, m)
		} else {
			out[k] = m.Qualified()
		}
	}
	return out
}

// memberName is how a bundle of yours names i as a member: bare when a bare
// name reads as i there (yours, or a remote one no profile of yours shadows), else qualified.
func memberName(infos []profile.Info, i profile.Info) string {
	switch {
	case i.Kind == profile.KindUser:
		return i.Name
	case i.Kind == profile.KindRemote && !slices.ContainsFunc(infos, func(o profile.Info) bool { return o.Kind == profile.KindUser && o.Name == i.Name }):
		return i.Name
	}
	return i.Qualified()
}

// bundleName is the name lola gives a bundle of yours of members (as memberName names them): their
// names joined with -, or with -2 … appended when a profile sc can use (infos) has it (freeName).
func bundleName(infos []profile.Info, members []string) string {
	return freeName(infos, strings.Join(bareNames(members), "-"))
}

// bundleRun is the run that makes the bundle name of members and switches to it at sc: lola profile <name> --switch --new --bundle a,b; again is what it keeps
// of the run.
func (e *Engine) bundleRun(sc Scope, name string, members []string, again []string) string {
	return e.Command(sc, "profile", append([]string{name, "--switch", "--new", "--bundle", strings.Join(members, ",")}, again...)...)
}

// BundleOffer is the run that combines the profiles names (as typed at sc) into a bundle of yours
// and switches to it: what lola profile --switch a b hints, since one profile is on at a time.
// Its name is the names joined with -. A name that names no profile, or
// several, is refused as --switch refuses it.
func (e *Engine) BundleOffer(sc Scope, names []string, again []string) (string, error) {
	infos, _, err := e.visible(sc)
	if err != nil {
		return "", err
	}
	var members []string
	for _, n := range names {
		i, err := e.resolveProfile(sc, infos, n, nil)
		if err != nil {
			return "", err
		}
		if m := memberName(infos, i); !slices.Contains(members, m) {
			members = append(members, m)
		}
	}
	return e.bundleRun(sc, bundleName(infos, members), members, again), nil
}

// bareNames is members, as memberName names them, each without its kind: the words of a bundle's
// generated name.
func bareNames(members []string) []string {
	out := make([]string, len(members))
	for i, m := range members {
		if _, name, ok := strings.Cut(m, ":"); ok {
			m = name
		}
		out[i] = m
	}
	return out
}
