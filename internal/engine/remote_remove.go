package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/fsx"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// ProfileRemove is lola profile's action that deletes a kept remote profile (Plan 6b §3.2); it
// is also the flag lola profile takes for it.
const ProfileRemove ProfileAction = "remove"

// ProfileRemoveRequest is one lola profile <name> --remove (Plan 6b §3.2): delete the remote
// profile Name names (go or remote:go). Scope is the scope the run is in: a profile it, user scope
// or the project found from the working directory uses is not removed unless Force, which
// overrides that check and never the question.
type ProfileRemoveRequest struct {
	Scope  Scope
	Name   string
	Force  bool
	DryRun bool
	// Yes says the run answered the question with -y, so a hint that repeats it keeps -y.
	Yes bool
	// Confirm asks whether to delete the profile. nil means it cannot ask: the run is refused. The
	// CLI passes one that says yes for -y.
	Confirm func(RemoteProfile) (bool, error)
}

// ProfileRemoveResult is what lola profile --remove reports: the profile, its file and its
// source, whether it was removed (false in a dry run), and the notices.
type ProfileRemoveResult struct {
	Profile string   `json:"profile"`
	File    string   `json:"file"`
	Source  string   `json:"source"`
	Removed bool     `json:"removed"`
	Notices []Notice `json:"notices"`
	DryRun  bool     `json:"dry_run"`
}

// RemoveProfile deletes a remote profile (Plan 6b §3.2, §6.5): refused while the scope the run is
// in, user scope or the project found from the working directory uses it (on, or extended by an
// active profile or default; lola keeps no list of every checkout), or cannot be read to tell,
// unless Force, which makes those warnings; asked about first (Confirm, which -y answers; never
// Force). The check and the question hold the profiles lock shared; it is taken exclusively only
// to check the profile is still there and rename it aside, then remove it, so it is gone in one
// step. The modules it installed stay until each scope's next sync, which warns about a profile
// that is on but gone (§10).
func (e *Engine) RemoveProfile(ctx context.Context, req ProfileRemoveRequest) (*ProfileRemoveResult, error) {
	sc := req.Scope
	// The profiles lock comes first, and its sweep finishes an interrupted --update before the
	// name is looked up, so a profile left aside is never taken for none.
	unlock, err := e.readProfiles(req.DryRun)
	if err != nil {
		return nil, err
	}
	defer func() { unlock() }()
	name, err := e.remoteNamed(sc, req.Name, ProfileRemove)
	if err != nil {
		return nil, err
	}
	q := string(profile.KindRemote) + ":" + name
	again := []string{q, "--remove"}
	if req.Force {
		again = append(again, "--force")
	}
	// A hint that repeats the run keeps -y, without which it refuses off a terminal.
	yes := []string{}
	if req.Yes {
		yes = []string{"-y"}
	}
	confirm := slices.Concat(again, []string{"-y"})
	again = append(again, yes...)
	if req.Confirm == nil && !req.DryRun {
		return nil, errs.WithHints(errors.New("lola profile --remove asks before it deletes a profile, and there is no terminal to ask on"),
			"confirm it: "+e.Command(sc, "profile", confirm...))
	}
	dir := filepath.Join(e.Paths.RemoteProfiles, name)
	res := &ProfileRemoveResult{Profile: shown(q), File: shown(e.profilePath(sc, profile.KindRemote, name)), Notices: []Notice{}, DryRun: req.DryRun}
	o, originErr := profile.ReadOrigin(dir)
	if originErr == nil {
		res.Source = shown(o.Source)
	} else {
		res.Notices = append(res.Notices, Notice{Level: LevelWarn, Text: shown(q) + ": " + shown(untrusted.CleanErr(originErr).Error())})
	}
	// The scope of the run, user scope, and the project found from the working directory: lola
	// keeps no list of every checkout.
	scopes := []Scope{sc}
	others := []Scope{{Kind: target.User}}
	if found, ok := e.foundProject(); ok {
		others = append(others, found)
	}
	for _, o := range others {
		if !slices.ContainsFunc(scopes, func(s Scope) bool { return s.Key() == o.Key() }) {
			scopes = append(scopes, o)
		}
	}
	forced := append([]string{q, "--remove", "--force"}, yes...)
	if req.DryRun {
		forced = append(forced, "-n")
	}
	force := "or remove it anyway: " + e.Command(sc, "profile", forced...)
	// A profile that does not load fails every check that reads it; their hints would name this
	// very removal, so only --force is offered.
	_, _, loadErr := profile.LoadRemote(e.profilePath(sc, profile.KindRemote, name))
	for _, s := range scopes {
		// Under --force, only a failure of the run's own profileScope (a tracked or unreadable
		// lock.json) still refuses; a scope whose layers cannot be read becomes a warning.
		_, _, _, checked, err := e.profileScope(ctx, s)
		if err != nil && s.Key() == sc.Key() {
			return nil, err
		}
		var using []string
		if err == nil {
			res.Notices = append(res.Notices, checked...)
			_, _, using, err = e.uses(s, q)
		}
		if err != nil && req.Force {
			res.Notices = append(res.Notices, Notice{Level: LevelWarn, Text: "could not check whether " + shown(q) + " is in use " + scopeAt(s) + ": " +
				shown(untrusted.CleanErr(err).Error())})
			continue
		}
		if err != nil && loadErr != nil {
			return nil, errs.WithHints(errors.New("lola cannot tell whether "+shown(q)+" is in use "+scopeAt(s)+": "+shown(q)+" itself does not load: "+
				untrusted.CleanErr(loadErr).Error()), force)
		}
		if err != nil {
			return nil, errs.WithHints(errors.New("lola cannot tell whether "+shown(q)+" is in use "+scopeAt(s)+": "+untrusted.CleanErr(err).Error()),
				append(errs.Hints(err), force)...)
		}
		if len(using) == 0 {
			continue
		}
		what := "is on " + scopeAt(s)
		if !slices.Contains(using, shown(q)) {
			what = "is extended by " + andJoin(using) + ", on " + scopeAt(s)
		}
		if !req.Force {
			hints := []string{}
			for _, u := range using {
				switch u {
				case profile.DefaultName: // always on: it stops extending q only when edited
					hints = append(hints, "remove "+shown(name)+" from the extends: of "+shown(e.profilePath(s, profile.KindUser, profile.DefaultName))+" first")
				case shown(q):
					hints = append(hints, "turn "+u+" off first: "+e.Command(s, "profile", append([]string{u, "--off"}, yes...)...))
				default:
					kind, own, _ := strings.Cut(u, ":")
					hints = append(hints, "turn "+u+" off first: "+e.Command(s, "profile", append([]string{u, "--off"}, yes...)...),
						"or remove "+shown(name)+" from the extends: of "+shown(e.profilePath(s, profile.Kind(kind), own)))
				}
			}
			return nil, errs.WithHints(fmt.Errorf("%s %s", shown(q), what), append(hints, force)...)
		}
		res.Notices = append(res.Notices, Notice{Level: LevelWarn, Text: shown(q) + " " + what + "; its modules stay installed there until the next " +
			e.SyncCommand(s) + ", which removes them"})
	}
	if req.DryRun {
		text := "to remove it: " + e.Command(sc, "profile", again...)
		if !req.Yes {
			text += " (off a terminal: -y)"
		}
		res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: text})
		return res, nil
	}
	ok, err := req.Confirm(RemoteProfile{Profile: res.Profile, File: res.File, Source: res.Source, Changes: []TrustChange{}})
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrDeclined
	}
	unlock()
	if unlock, err = e.lockProfiles(true); err != nil {
		unlock = func() {}
		return nil, err
	}
	if err := e.sweepRemote(); err != nil {
		return nil, err
	}
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return nil, errs.WithHints(fmt.Errorf("%s was removed while you were asked", shown(q)), "see the profiles you have: "+e.Command(sc, "profile"))
	}
	// What you were asked about is what is removed: an --update in between is refused.
	if now, err := profile.ReadOrigin(dir); originErr == nil && (err != nil || now != o) {
		return nil, errs.WithHints(fmt.Errorf("%s changed while you were asked; nothing was removed", shown(q)),
			"run it again: "+e.Command(sc, "profile", again...))
	}
	aside, err := os.MkdirTemp(e.Paths.RemoteProfiles, delPrefix+name+"-*")
	if err != nil {
		return nil, err
	}
	if err := os.Remove(aside); err != nil {
		return nil, err
	}
	if err := os.Rename(dir, aside); err != nil {
		return nil, err
	}
	res.Removed = true
	// It is gone once renamed aside; what is left there, a .del- directory, the next run sweeps.
	if err := errors.Join(fsx.SyncDir(e.Paths.RemoteProfiles), os.RemoveAll(aside)); err != nil {
		res.Notices = append(res.Notices, Notice{Level: LevelWarn, Text: "removed " + shown(q) + ", but could not delete " + shown(aside) + ": " +
			shown(untrusted.CleanErr(err).Error()) + "; the next lola run removes it"})
	}
	return res, nil
}
