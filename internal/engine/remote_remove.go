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
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// ProfileRemove is lola profile's action that deletes a profile of yours or a kept remote profile;
// it is also the flag lola profile takes for it.
const ProfileRemove ProfileAction = "remove"

// ProfileRemoveRequest is one lola profile <name> --remove:
// delete the profile Name names, a profile of yours (user:try, or a bare name only yours has),
// which goes to the trash, or a remote one (remote:go, or a bare name only a remote profile has).
// Scope is the scope the run is in: a profile it, user scope or the project found from the working
// directory uses is not removed unless Force, which overrides that check and answers the question
// (user decision, 2026-10-22).
type ProfileRemoveRequest struct {
	Scope  Scope
	Name   string
	Force  bool
	DryRun bool
	// Yes says the run answered the question with -y, so a hint that repeats it keeps -y.
	Yes bool
	// Display is the run's output flags, which the hints of a file with a marketplace address keep.
	Display []string
	// Confirm asks whether to delete the profile, unless Force. nil means it cannot ask: the run is
	// refused. The CLI passes one that says yes for -y.
	Confirm func(RemoteProfile) (bool, error)
}

// ProfileRemoveResult is what lola profile --remove reports: the profile, its file and its
// source ("" for a profile of yours), whether it was removed (false in a dry run), where a
// profile of yours went in the trash ("" for a remote one, and in a dry run), and the notices.
type ProfileRemoveResult struct {
	Profile string   `json:"profile"`
	File    string   `json:"file"`
	Source  string   `json:"source"`
	Removed bool     `json:"removed"`
	Trash   string   `json:"trash"`
	Notices []Notice `json:"notices"`
	DryRun  bool     `json:"dry_run"`
}

// RemoveProfile deletes the profile req.Name names: a
// profile of yours goes to the trash (removeOwn); a remote one is deleted. Either is refused while
// the scope the run is in, user scope or the project found from the working directory uses it (on,
// or a member of the bundle on; lola keeps no list of every checkout), or cannot be read to tell,
// unless Force, which makes those warnings; asked about first (Confirm, which -y answers), unless
// Force, which answers it too. The check and the question hold the profiles lock shared; it is taken exclusively only
// to check the profile is still there and rename it aside, then remove it, so it is gone in one
// step. The modules it installed stay until each scope's next sync, which warns about a profile
// that is on but gone.
func (e *Engine) RemoveProfile(ctx context.Context, req ProfileRemoveRequest) (_ *ProfileRemoveResult, err error) {
	sc := req.Scope
	defer func() { err = e.CoachUnresolved(err, []Scope{sc}, req.DryRun, req.Display) }()
	// The profiles lock comes first, and its sweep finishes an interrupted --update before the
	// name is looked up, so a profile left aside is never taken for none.
	unlock, err := e.readProfiles(req.DryRun)
	if err != nil {
		return nil, err
	}
	defer func() { unlock() }()
	if mine, err := e.ownToRemove(sc, req); err != nil || mine != nil {
		if err != nil {
			return nil, err
		}
		return e.removeOwn(ctx, req, *mine, &unlock)
	}
	name, err := e.remoteNamed(sc, []Scope{sc}, req.Name, ProfileRemove)
	if err != nil {
		return nil, err
	}
	q := string(profile.KindRemote) + ":" + name
	again := []string{q, "--remove"}
	if req.Force {
		again = append(again, "--force")
	}
	dir := filepath.Join(e.Paths.RemoteProfiles, name)
	res := &ProfileRemoveResult{Profile: shown(q), File: shown(e.profilePath(sc, profile.KindRemote, name)), Notices: []Notice{}, DryRun: req.DryRun}
	o, originErr := profile.ReadOrigin(dir)
	if originErr == nil {
		res.Source = shown(o.Source)
	} else {
		res.Notices = append(res.Notices, Notice{Level: LevelWarn, Text: shown(q) + ": " + shown(untrusted.CleanErr(originErr).Error())})
	}
	// A profile that does not load fails every check that reads it; their hints would name this
	// very removal, so only --force is offered.
	_, _, loadErr := profile.LoadRemote(e.profilePath(sc, profile.KindRemote, name))
	// In use is said before -y is offered: a -y hint never names a removal that is then refused.
	notes, _, err := e.refuseInUse(ctx, req, q, loadErr)
	if err != nil {
		return nil, err
	}
	// No hint carries -y but the one that asks for it: a -y answers the question
	// it was given for, and a hint must not answer another.
	confirm := slices.Concat(again, []string{"-y"})
	if req.Confirm == nil && !req.DryRun && !req.Force {
		return nil, errs.WithHints(errors.New("lola profile --remove asks before it deletes a profile, and there is no terminal to ask on"),
			"confirm it: "+e.Command(sc, "profile", confirm...))
	}
	res.Notices = append(res.Notices, notes...)
	if req.DryRun {
		text := "to remove it: " + e.Command(sc, "profile", again...)
		if !req.Force {
			text += " (off a terminal: -y)"
		}
		res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: text})
		return res, nil
	}
	if !req.Force {
		ok, err := req.Confirm(RemoteProfile{Profile: res.Profile, File: res.File, Source: res.Source, Changes: []TrustChange{}})
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrDeclined
		}
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
	if err := e.writePinsNow([]string{name}); err != nil {
		res.Notices = append(res.Notices, Notice{Level: LevelWarn, Text: "removed " + shown(q) + ", but pins.yaml still records it: " +
			shown(untrusted.CleanErr(err).Error()) + "; delete its entry under remotes: in " + shown(e.Paths.Pins)})
	}
	return res, nil
}

// unlist is the edit that removes a member from the bundle files that list it (listed, each
// with the member as it writes it).
func unlist(listed []listing) string {
	if len(listed) == 1 {
		return "remove " + shown(listed[0].typed) + " from the bundle: of " + shown(listed[0].path)
	}
	parts := make([]string, 0, len(listed))
	for _, l := range listed {
		parts = append(parts, shown(l.typed)+" from "+shown(l.path))
	}
	return "remove it from each bundle: that lists it: " + strings.Join(parts, ", ")
}
