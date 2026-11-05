package engine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/trust"
	"github.com/trevor-vaughan/lola-go/internal/yamldoc"
)

// ProfileSplit is lola profile <name> --split (spec 2026-10-29 §5.2).
const ProfileSplit ProfileAction = "split"

// SplitRequest is lola profile <name> --split at Scope (spec 2026-10-29 §5.2): Name as typed; To,
// --to, the new profile's name ("" for <name>-modules, -2 … on a collision). Confirm asks "Split?"
// once the plan is shown; Yes (-y) and Options.Force answer it, and without any of them (off a
// terminal) the run is refused. WriteOnly writes the two files and applies nothing: the command
// that offered the split (SplitOfferError) applies the scope next. ConfirmRemovals, Trust and Show
// are the apply's, as a sync's are.
type SplitRequest struct {
	Scope           Scope
	Name            string
	To              string
	Confirm         func(SplitPlan) (bool, error)
	Yes             bool
	WriteOnly       bool
	Options         Options
	ConfirmRemovals func([]Removal) (bool, error)
	Trust           func(TrustReport) (TrustAnswer, error)
	Show            func(TrustReport)
}

// SplitPlan is what a split writes (spec 2026-10-29 §5.2): Bundle, the file it rewrites into a
// bundle, with its members; Profile, the new profile its modules move to, with its top-level
// targets and the modules, by name. Every string has been through untrusted.Text.
type SplitPlan struct {
	Bundle  SplitFile `json:"bundle"`
	Profile SplitFile `json:"profile"`
}

// SplitFile is one file of a SplitPlan: its profile's qualified name and its path, and what it
// holds once split.
type SplitFile struct {
	Name    string   `json:"name"`
	Path    string   `json:"path"`
	Members []string `json:"members"`
	Targets []string `json:"targets"`
	Modules []string `json:"modules"`
}

// SplitResult is a split's outcome: its plan, and Sync, the apply of the scope when the bundle is
// on there (null otherwise, and under -n and WriteOnly).
type SplitResult struct {
	Plan   SplitPlan   `json:"plan"`
	Sync   *SyncResult `json:"sync"`
	DryRun bool        `json:"dry_run"`
}

// SplitOfferError is a run refused by a profile file with both bundle: and modules: (spec
// 2026-10-29 §5.1): Scope and Name, the profile as lola profile --split names it, which a
// terminal is offered (the CLI); off one the refusal hints the --split.
type SplitOfferError struct {
	Scope Scope
	Name  string
	err   error
}

func (e *SplitOfferError) Error() string { return e.err.Error() }

func (e *SplitOfferError) Unwrap() error { return e.err }

// mixedError is err, the read of i at sc, as a SplitOfferError when it is a *profile.MixedError,
// coached with the --split that fixes it; any other err as it is. The split names i qualified when
// it is the project's, or when a profile of another kind sc can use (infos) has its name, so the
// name resolves to i alone (ruling P2).
func (e *Engine) mixedError(sc Scope, infos []profile.Info, i profile.Info, err error) error {
	if _, mixed := errors.AsType[*profile.MixedError](err); !mixed {
		return err
	}
	if i.Kind == profile.KindDefault { // the base lists modules: no split makes it a bundle (spec 2026-10-29 §3.1)
		return errs.WithHints(errors.New(err.Error()), e.baseMembersHint(i.Path))
	}
	name := i.Name
	if i.Kind == profile.KindProject || slices.ContainsFunc(infos, func(o profile.Info) bool {
		return o.Name == i.Name && o.Kind != i.Kind && o.Kind != profile.KindDefault
	}) {
		name = i.Qualified()
	}
	return &SplitOfferError{Scope: sc, Name: name, err: errs.WithHints(errors.New(err.Error()),
		"move its modules into a profile of their own: "+e.Command(sc, "profile", name, "--split"))}
}

// splitOn reports whether i is on at sc once split by w: the profile on, or a member of the bundle
// on, which the expansion then reaches through i to made, the profile the split makes (ruling P3:
// decided through the expansion, the files w writes read in place of the disk; a bundle is never a
// layer itself).
func (e *Engine) splitOn(sc Scope, i profile.Info, made string, w *profileWrites) (bool, error) {
	led, err := e.Ledger(sc)
	if err != nil || led.Profile == nil {
		return false, err
	}
	e.creating = w
	defer func() { e.creating = nil }()
	infos, _, err := e.visible(sc)
	if err != nil {
		return false, err
	}
	k := activeInfo(infos, led.Profile.Name)
	if k < 0 {
		return false, nil
	}
	if infos[k].Path == i.Path {
		return true, nil
	}
	ms, _, err := e.members(sc, infos, infos[k])
	return slices.ContainsFunc(ms, func(l Layer) bool { return l.Path == made }), err
}

// baseMembersHint is the hint of a default.yml at path that has bundle: beside modules:: remove
// bundle:, then switch to a bundle of its members, as written, and the profile on at user scope,
// which the switch keeps (as default.yml's extends: refusal says), so no member is lost and nothing
// is turned off. A bundle: that does not read as a list of names (the loader refuses it first)
// gets the edit alone.
func (e *Engine) baseMembersHint(path string) string {
	remove := "remove bundle: from " + shown(path) + "; the base lists modules"
	var members []string
	if d, _, err := yamldoc.Read(path, profile.Version); err == nil {
		if root := yamldoc.Root(d); root != nil {
			if list := yamldoc.Value(root, "bundle"); list != nil {
				for _, n := range list.Content {
					members = append(members, shown(n.Value))
				}
			}
		}
	}
	if len(members) == 0 {
		return remove
	}
	keep := ""
	if on := e.userOn(); on != "" && !slices.Contains(members, on) {
		members, keep = append(members, on), " and "+on+", the profile on now, which the switch keeps"
	}
	user := Scope{Kind: target.User}
	infos, _, err := e.visible(user)
	if err != nil {
		return remove
	}
	return "remove bundle: from " + shown(path) + ", then switch to a bundle of them" + keep + ": " +
		e.bundleRun(user, freeName(infos, "default-bundle"), members, nil)
}

// Split splits req.Name's file (spec 2026-10-29 §5.2): its modules and top-level targets move,
// whole, into a new profile of its kind, and the file becomes a bundle of its old members and then
// the new profile, so its own entries still win over its members'. Your profile is split at any
// scope; a project's only in its project. The plan is asked about (Confirm, -y, --force) before
// both files are written in one transaction with lock.json; when the bundle is on at the scope,
// that transaction is the scope's sync, which installs what the split file now gives. The trust
// record takes as trusted only what it covered before: an entry the file's part covered, in the
// new profile's part; the bundle's members, which the file never loaded with, are asked about.
func (e *Engine) Split(ctx context.Context, req SplitRequest) (*SplitResult, error) {
	sc := req.Scope
	infos, _, err := e.visible(sc)
	if err != nil {
		return nil, err
	}
	run := func(args ...string) string { return e.Command(sc, "profile", append(args, req.Options.Display...)...) }
	if isBase, isDefault := namesBase(req.Name); isBase || isDefault {
		return nil, errors.New("the base lists modules and bundles nothing; --split splits a profile that does both")
	}
	i, err := e.resolveProfile(sc, infos, req.Name, nil)
	if err != nil {
		return nil, err
	}
	// A name is taken when a profile sc can use has it, of any kind, or pins.yaml records a remote
	// profile by it that a run restores: the bundle's bare member must read as the new profile alone.
	taken := func(n string) bool {
		return slices.ContainsFunc(infos, func(o profile.Info) bool { return o.Name == n }) || e.recorded(string(profile.KindRemote)+":"+n)
	}
	free := func(want string) string {
		if len(want) > 60 { // room for -<n> within a profile name's 64 characters
			want = strings.TrimRight(want[:60], "-.")
		}
		n := want
		for k := 2; taken(n); k++ {
			n = fmt.Sprintf("%s-%d", want, k)
		}
		return n
	}
	if i.Kind == profile.KindRemote { // its loader refuses bundle: in it: there is never one to split
		return nil, errs.WithHints(fmt.Errorf("%s is read-only; lola does not edit remote profiles, and none has a bundle: key", shown(i.Qualified())),
			"switch to a copy of yours, which you can edit: "+run(free(i.Name), "--switch", "--new", "--from", i.Qualified()))
	}
	m, err := profile.ReadMixed(i.Path, i.Kind == profile.KindProject)
	if err != nil {
		return nil, err
	}
	want := i.Name + "-modules"
	if len(want) > 60 {
		want = "modules"
	}
	name := cmp.Or(req.To, free(want))
	if err := newName(name); err != nil {
		return nil, err
	}
	if taken(name) {
		return nil, errs.WithHints(fmt.Errorf("%s names a profile already; --to names the new one", shown(name)),
			"name another: "+run(req.Name, "--split", "--to", free(name)))
	}
	bundleBytes, modulesBytes, err := m.Split(name)
	if err != nil {
		return nil, err
	}
	newPath := e.profilePath(sc, i.Kind, name)
	bundleBytes, bundle, err := madeFile(i.Kind, i.Path, bundleBytes)
	if err != nil {
		return nil, err
	}
	modulesBytes, modules, err := madeFile(i.Kind, newPath, modulesBytes)
	if err != nil {
		return nil, err
	}
	res := &SplitResult{DryRun: req.Options.DryRun, Plan: SplitPlan{
		Bundle: SplitFile{Name: shown(i.Qualified()), Path: shown(i.Path), Members: shownAll(bundle.Bundle), Targets: []string{}, Modules: []string{}},
		Profile: SplitFile{Name: shown(string(i.Kind) + ":" + name), Path: shown(newPath), Members: []string{}, Targets: shownAll(modules.Targets),
			Modules: []string{}}}}
	for _, ent := range modules.Modules {
		res.Plan.Profile.Modules = append(res.Plan.Profile.Modules, shown(ent.Name))
	}
	if res.Plan.Profile.Targets == nil {
		res.Plan.Profile.Targets = []string{}
	}
	again := []string{req.Name, "--split"}
	if req.To != "" {
		again = append(again, "--to", req.To)
	}
	if !req.Options.DryRun && !req.Yes && !req.Options.Force {
		if req.Confirm == nil {
			return nil, errs.WithHints(fmt.Errorf("lola profile --split rewrites %s and makes %s, and there is no terminal to ask on", shown(i.Path), shown(newPath)),
				"split it: "+run(append(again, "-y")...))
		}
		ok, err := req.Confirm(res.Plan)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrDeclined
		}
	}
	rewrite := Layer{Name: ledger.FromProfile(i.Qualified()), Path: i.Path, Profile: true}
	if i.Kind == profile.KindProject {
		rewrite.Key = i.Name
	}
	w := &profileWrites{read: map[string]*profile.Profile{i.Path: bundle}, trusted: map[string]trust.View{},
		files: []fileEdit{{layer: rewrite, data: bundleBytes, check: func() error { return m.Current(sc.ProjectDir) }}}}
	w.add(sc, i.Kind, name, newPath, modulesBytes, modules, Created{Profile: res.Plan.Profile.Name, Path: res.Plan.Profile.Path})
	if i.Kind == profile.KindProject {
		rec, err := e.trustRecord(sc)
		if err != nil {
			return nil, err
		}
		old, v := rec.View.Part(i.Name), madeView(modules)
		v.Modules = slices.DeleteFunc(v.Modules, func(ent trust.Entry) bool { return !old.CoversEntry(ent) })
		if !slices.Equal(old.Targets, v.Targets) {
			v.Targets = nil
		}
		w.trusted[i.Name], w.trusted[name] = trust.View{}, v
	}
	on, err := e.splitOn(sc, i, newPath, w)
	if err != nil {
		return nil, err
	}
	sync := SyncRequest{Scope: sc, Options: req.Options, ConfirmRemovals: req.ConfirmRemovals, Trust: req.Trust, Show: req.Show, Display: req.Options.Display,
		write: w, writeOnly: req.WriteOnly || !on, profileArgs: again}
	out, err := e.Sync(ctx, sync)
	if err != nil {
		return nil, err
	}
	if on && !req.WriteOnly {
		res.Sync = out
	}
	return res, nil
}
