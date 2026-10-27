package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// ProfileAdd is lola profile's action that fetches a remote profile and keeps it (Plan 6b §3.2,
// §6.5); like the activation actions, it is also the flag lola profile takes for it.
const ProfileAdd ProfileAction = "add"

// ProfileAddRequest is one lola profile --add (Plan 6b §3.2): fetch Source at Ref (a git branch,
// tag or commit) and read profiles/<Name>.yml under its root, or under Path. Name is as typed: go
// or remote:go. Scope is the scope a scope flag named, nil for none: it only picks the scope whose
// lock.json annotates the content shown, since the profile is shared by every scope and adding
// turns nothing on (§19 P22); without it the read-only rule picks (knownScopes).
type ProfileAddRequest struct {
	Scope             *Scope
	Name              string
	Source, Ref, Path string
	DryRun            bool
	// Rerun is the run's own flags a hint that repeats it keeps beside the ones it names (--trust,
	// -n, --json, --plain, --no-pager as given).
	Rerun []string
	// Pick chooses, when no Name is given, the profiles to add among the source's names (and the
	// count of files whose names are not valid, never shown); nil means it cannot ask, and the run
	// is refused, listing them.
	Pick func(names []string, invalid int) ([]string, error)
	// Consent shows the profiles and asks whether to add them (Plan 6b §8.2). nil means it cannot
	// ask: unless Trust covers profiles, the run is refused before any network contact. When it
	// does, Show, when set, shows them as a record, and Receipt, when set, is shown them once,
	// before anything is written (Plan 9 §3.4).
	Consent func([]RemoteProfile) (bool, error)
	Trust   TrustKinds
	Show    func([]RemoteProfile)
	Receipt func([]Consent) (bool, error)
}

// AddProfile fetches a remote profile and adds it (Plan 6b §3.2, §6.5): the name and every source
// rule are checked, and a run that cannot ask for consent refused, before any network contact;
// the source's profiles/ is fetched into the run's staging and each profile read as untrusted
// input (profile.Fetched); its entries are shown with the whole content they bring, each module
// fetched at its pin into staging (remoteContent); and only an accepted profile is written, into a
// directory of its own (writeRemote). Nothing is cached or kept before consent, and a No or an
// error leaves nothing behind. Fetching, showing and asking hold the profiles lock shared, so
// syncs elsewhere go on; it is taken exclusively only to check again that no profile of that
// name appeared, and write (ruling R-6b3b-8).
func (e *Engine) AddProfile(ctx context.Context, req ProfileAddRequest) (_ *RemoteResult, err error) {
	defer func() { err = e.CoachUnresolved(err, nil, false, outputOnly(req.Rerun)) }()
	var given []Scope
	known, skipped := e.knownScopes()
	sc := known[0]
	if req.Scope != nil {
		given, sc, skipped = []Scope{*req.Scope}, *req.Scope, nil
	}
	name := req.Name
	// display is the run's flags a hint that changes its consent or its dry run keeps: --json,
	// --plain and --no-pager as given.
	display := slices.DeleteFunc(slices.Clone(req.Rerun), func(f string) bool { return trustArg(f) || f == "-n" })
	// kept is the flags of a hint that repeats the run for another reason, the --update of a
	// profile you have or the add at user scope: --trust answered this add's question, never
	// that one, so it is left out (rulings R-6b3c-8, R-6cux-4).
	kept := slices.DeleteFunc(slices.Clone(req.Rerun), trustArg)
	// typed is the run's arguments, the name aside, which every hint that repeats it keeps.
	typed := []string{"--add", untrusted.Text(req.Source, untrusted.MaxName)}
	for _, f := range [][2]string{{"--ref", req.Ref}, {"--path", req.Path}} {
		if f[1] != "" {
			typed = append(typed, f[0], f[1])
		}
	}
	if kind, bare, qualified := strings.Cut(name, ":"); qualified {
		if kind != string(profile.KindRemote) {
			args := slices.Concat([]string{bare}, typed, req.Rerun)
			return nil, errs.WithHints(fmt.Errorf("--add keeps remote profiles; %s names another kind", untrusted.Text(name, untrusted.MaxName)),
				"add it as a remote profile: "+e.ProfileRun(given, args...))
		}
		// remote: names a profile, never none: it does not start the picker.
		if err := remoteName(bare); err != nil {
			return nil, err
		}
		name = bare
	}
	if name != "" {
		if err := remoteName(name); err != nil {
			return nil, err
		}
		typed = append([]string{name}, typed...)
	}
	spec, err := e.remoteSpec(req.Source, req.Ref, req.Path)
	if err != nil {
		return nil, err
	}
	covered := req.Trust.Covers(TrustProfiles)
	if req.Consent == nil && !covered && !req.DryRun {
		return nil, errs.WithHints(errors.New("lola profile --add shows the profile and asks before adding it, and there is no terminal to ask on"),
			"see it: "+e.ProfileRun(given, slices.Concat(typed, []string{"-n"}, display)...),
			"then add it unasked: "+e.ProfileRun(given, slices.Concat(typed, []string{TrustProfiles.Arg()}, display)...))
	}
	// Every lola profile command refuses a tracked lock.json (Plan 6b §5, §19 S5). The found
	// project only labels the content, so the run at user scope is offered too.
	_, _, led, checked, err := e.profileScope(ctx, sc)
	if err != nil && len(given) == 0 && sc.Kind == target.Project {
		return nil, errs.WithHints(errors.New(untrusted.CleanErr(err).Error()), append(errs.Hints(err),
			"or compare with user scope: "+e.ProfileRun([]Scope{{Kind: target.User}}, append(slices.Clone(typed), kept...)...))...)
	}
	if err != nil {
		return nil, err
	}
	unlock, err := e.readProfiles(req.DryRun)
	if err != nil {
		return nil, err
	}
	defer func() { unlock() }()
	if name != "" {
		if err := e.refuseKept(given, name, kept); err != nil {
			return nil, err
		}
	}
	res := &RemoteResult{Profiles: []RemoteProfile{}, Notices: slices.Concat(skipped, checked, e.sweepRuns()), DryRun: req.DryRun, Trusted: []Consent{}}
	run, err := e.runDir()
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(run)) }()
	f, err := fetchProfiles(ctx, spec, run)
	if err != nil {
		return nil, err
	}
	names := []string{name}
	if name == "" {
		if names, err = e.pickRemote(given, f.Dir, typed, display, req.DryRun, req.Pick); err != nil {
			return nil, err
		}
		for _, n := range names {
			if err := e.refuseKept(given, n, kept); err != nil {
				return nil, err
			}
		}
	}
	probes := e.newProbeRun(ctx)
	templates, err := e.newTemplateRun(sc, led.Profiles) // one budget for every profile the command renders (Plan 7 §6.2)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		p, b, err := profile.Fetched(f.Dir, name)
		if err != nil {
			return nil, err
		}
		q := string(profile.KindRemote) + ":" + name
		o := profile.Origin{Version: profile.OriginVersion, Source: spec.Location, Ref: spec.Ref, Path: req.Path, Commit: f.Commit, Digest: profile.Digest(b)}
		rp := RemoteProfile{Profile: shown(q), File: shown(e.profilePath(sc, profile.KindRemote, name)),
			Source: shown(o.Source), Ref: shown(o.Ref), Path: shown(o.Path), Commit: shown(o.Commit), Digest: shown(o.Digest),
			ComparedWith: scopeName(sc), Description: shown(p.Description), name: name, data: b, origin: o}
		if rp.Changes, err = e.remoteContent(ctx, sc, q, led, nil, p.Entries(), run, probes, templates, outputOnly(req.Rerun)); err != nil {
			return nil, err
		}
		res.Profiles = append(res.Profiles, rp)
	}
	if covered {
		for _, rp := range res.Profiles {
			res.Trusted = append(res.Trusted, Consent{Kind: TrustProfiles.String(), Item: rp.Profile, Change: ConsentNew})
		}
	}
	if req.DryRun {
		for _, rp := range res.Profiles {
			args := slices.DeleteFunc(slices.Concat(typed, req.Rerun), func(f string) bool { return f == "-n" })
			if name == "" {
				args = append([]string{rp.name}, args...)
			}
			text := "to add " + rp.Profile + ": " + e.ProfileRun(given, args...)
			if !covered {
				text += " (off a terminal: " + TrustProfiles.Arg() + ")"
			}
			res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: text})
		}
		return res, nil
	}
	ok, err := consentRemote(slices.Clone(res.Profiles), res.Trusted, covered, req.Consent, req.Show, req.Receipt)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrDeclined
	}
	// Write under the exclusive lock, once nothing took the names while you were asked.
	unlock()
	if unlock, err = e.lockProfiles(true); err != nil {
		unlock = func() {}
		return nil, err
	}
	if err := e.sweepRemote(); err != nil {
		return nil, err
	}
	var written []string
	for i := range res.Profiles {
		rp := &res.Profiles[i]
		err := e.refuseKept(given, rp.name, kept)
		ok := false
		if err == nil {
			ok, err = e.writeRemote(rp.name, rp.data, rp.origin)
		}
		if ok && err != nil {
			// It is in place: what failed after is a warning, not a failed add.
			res.Notices = append(res.Notices, Notice{Level: LevelWarn, Text: rp.Profile + " is written, but: " + shown(untrusted.CleanErr(err).Error())})
			err = nil
		}
		if err != nil {
			if len(written) > 0 {
				err = fmt.Errorf("added %s, but not %s: %w", andJoin(written), rp.Profile, err)
			}
			return nil, err
		}
		rp.Written = true
		written = append(written, rp.Profile)
	}
	// Adding turns nothing on; a local profile of its name keeps it from being turned on.
	infos, _, err := e.visible(sc)
	if err != nil {
		res.Notices = append(res.Notices, Notice{Level: LevelWarn, Text: "added " + andJoin(written) +
			"; lola could not check whether a local profile shadows them: " + shown(untrusted.CleanErr(err).Error())})
		return res, nil
	}
	for _, rp := range res.Profiles {
		if sh := shadowing(infos, profile.Info{Name: rp.name, Kind: profile.KindRemote}); sh != nil {
			res.Notices = append(res.Notices, Notice{Level: LevelWarn, Text: rp.Profile + " is shadowed by " + shown(sh.Qualified()) +
				", which is used instead; to build on it, add extends: [" + shown(rp.name) + "] to " + shown(sh.Path)})
			continue
		}
		res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: "turn it on: " + e.Command(sc, "profile", rp.Profile, "--on")})
	}
	return res, nil
}

// refuseKept refuses to add the remote profile name when lola already keeps one of that name, or
// when something else, which is no profile, has its place; the hint's --update keeps the scope
// flag of the run, given (none when empty), and flags, the run's flags it keeps.
func (e *Engine) refuseKept(given []Scope, name string, flags []string) error {
	path := filepath.Join(e.Paths.RemoteProfiles, name)
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case !fi.IsDir():
		return errs.WithHints(fmt.Errorf("%s is not a profile lola wrote, and is in the way of remote:%s", shown(path), shown(name)),
			"move it out of "+shown(e.Paths.RemoteProfiles)+", then run this again")
	}
	q := string(profile.KindRemote) + ":" + name
	return errs.WithHints(fmt.Errorf("you already have a remote profile %s", shown(q)), "fetch it again: "+e.ProfileRun(given, slices.Concat([]string{q, "--update"}, flags)...))
}

// pickRemote is the profiles of the fetched tree to add, when none was named (Plan 6b §3.2): the
// names pick chooses among those of the tree's profiles/ that a remote profile may have (§19 S7,
// remoteName: default never is one, and counts with the invalid names). Without pick the run is
// refused, listing them, with the command that adds each, or for a dry run shows it (typed is the
// run's arguments, to repeat, with the scope flag given, none when empty, and display, the run's
// flags each hint keeps after --trust=profiles or -n). A choice of none is a No.
func (e *Engine) pickRemote(given []Scope, tree string, typed, display []string, dry bool, pick func([]string, int) ([]string, error)) ([]string, error) {
	all, invalid, err := profile.FetchedNames(tree)
	if err != nil {
		return nil, err
	}
	names := slices.DeleteFunc(all, func(n string) bool { return remoteName(n) != nil })
	invalid += len(all) - len(names)
	if len(names) == 0 {
		return nil, errs.WithHints(errors.New("the source has no profiles"), "a profile source holds profiles/<name>.yml at its root, or under --path")
	}
	if pick == nil {
		verb, flag := "add ", TrustProfiles.Arg()
		if dry {
			verb, flag = "see ", "-n"
		}
		var hints []string
		for _, n := range names {
			hints = append(hints, verb+n+": "+e.ProfileRun(given, slices.Concat([]string{n}, typed, []string{flag}, display)...))
		}
		return nil, errs.WithHints(fmt.Errorf("name the profile to add; the source has %s", andJoin(names)), hints...)
	}
	chosen, err := pick(names, invalid)
	if err != nil {
		return nil, err
	}
	if len(chosen) == 0 {
		return nil, ErrDeclined
	}
	for _, n := range chosen {
		if !slices.Contains(names, n) {
			return nil, fmt.Errorf("the source has no profile %s", untrusted.Text(n, untrusted.MaxName))
		}
	}
	return chosen, nil
}
