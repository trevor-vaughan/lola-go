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
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// ProfileAdd is lola profile's action that fetches a remote profile and keeps it (Plan 6b §3.2,
// §6.5); like the activation actions, it is also the flag lola profile takes for it.
const ProfileAdd ProfileAction = "add"

// ProfileAddRequest is one lola profile --add (Plan 6b §3.2): fetch Source at Ref (a git branch,
// tag or commit) and read profiles/<Name>.yml under its root, or under Path. Name is as typed: go
// or remote:go. Scope is the scope whose lock.json annotates the content shown.
type ProfileAddRequest struct {
	Scope             Scope
	Name              string
	Source, Ref, Path string
	DryRun            bool
	// Pick chooses, when no Name is given, the profiles to add among the source's names (and the
	// count of files whose names are not valid, never shown); nil means it cannot ask, and the run
	// is refused, listing them.
	Pick func(names []string, invalid int) ([]string, error)
	// Consent shows the profiles and asks whether to add them (Plan 6b §8.2). nil means it cannot
	// ask: the run is refused before any network contact. The CLI passes one that says yes for
	// --trust, after showing them.
	Consent func([]RemoteProfile) (bool, error)
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
	sc := req.Scope
	name := req.Name
	// typed is the run's arguments, the name aside, which every hint that repeats it keeps.
	typed := []string{"--add", untrusted.Text(req.Source, untrusted.MaxName)}
	for _, f := range [][2]string{{"--ref", req.Ref}, {"--path", req.Path}} {
		if f[1] != "" {
			typed = append(typed, f[0], f[1])
		}
	}
	if kind, bare, qualified := strings.Cut(name, ":"); qualified {
		if kind != string(profile.KindRemote) {
			args := append([]string{bare}, typed...)
			if req.DryRun {
				args = append(args, "-n")
			}
			return nil, errs.WithHints(fmt.Errorf("--add keeps remote profiles; %s names another kind", untrusted.Text(name, untrusted.MaxName)),
				"add it as a remote profile: "+e.Command(sc, "profile", args...))
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
	if req.Consent == nil && !req.DryRun {
		return nil, errs.WithHints(errors.New("lola profile --add shows the profile and asks before adding it, and there is no terminal to ask on"),
			"see it: "+e.Command(sc, "profile", append(typed, "-n")...),
			"then add it unasked: "+e.Command(sc, "profile", append(typed, "--trust")...))
	}
	// Every lola profile command refuses a tracked lock.json (Plan 6b §5, §19 S5).
	_, _, led, checked, err := e.profileScope(ctx, sc)
	if err != nil {
		return nil, err
	}
	unlock, err := e.readProfiles(req.DryRun)
	if err != nil {
		return nil, err
	}
	defer func() { unlock() }()
	if name != "" {
		if err := e.refuseKept(sc, name); err != nil {
			return nil, err
		}
	}
	res := &RemoteResult{Profiles: []RemoteProfile{}, Notices: append(checked, e.sweepRuns()...), DryRun: req.DryRun}
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
		if names, err = e.pickRemote(sc, f.Dir, typed, req.DryRun, req.Pick); err != nil {
			return nil, err
		}
		for _, n := range names {
			if err := e.refuseKept(sc, n); err != nil {
				return nil, err
			}
		}
	}
	probes := e.newProbeRun(ctx)
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
		if rp.Changes, err = e.remoteContent(ctx, sc, q, led, nil, p.Entries(), run, probes); err != nil {
			return nil, err
		}
		res.Profiles = append(res.Profiles, rp)
	}
	if req.DryRun {
		for _, rp := range res.Profiles {
			args := typed
			if name == "" {
				args = append([]string{rp.name}, typed...)
			}
			res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: "to add " + rp.Profile + ": " + e.Command(sc, "profile", args...) +
				" (off a terminal: --trust)"})
		}
		return res, nil
	}
	ok, err := req.Consent(slices.Clone(res.Profiles))
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
		err := e.refuseKept(sc, rp.name)
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
// when something else, which is no profile, has its place.
func (e *Engine) refuseKept(sc Scope, name string) error {
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
	return errs.WithHints(fmt.Errorf("you already have a remote profile %s", shown(q)), "fetch it again: "+e.Command(sc, "profile", q, "--update"))
}

// pickRemote is the profiles of the fetched tree to add, when none was named (Plan 6b §3.2): the
// names pick chooses among those of the tree's profiles/ that a remote profile may have (§19 S7,
// remoteName: default never is one, and counts with the invalid names). Without pick the run is
// refused, listing them, with the command that adds each, or for a dry run shows it (typed is the
// run's arguments, to repeat). A choice of none is a No.
func (e *Engine) pickRemote(sc Scope, tree string, typed []string, dry bool, pick func([]string, int) ([]string, error)) ([]string, error) {
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
		verb, flag := "add ", "--trust"
		if dry {
			verb, flag = "see ", "-n"
		}
		var hints []string
		for _, n := range names {
			hints = append(hints, verb+n+": "+e.Command(sc, "profile", append(append([]string{n}, typed...), flag)...))
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
