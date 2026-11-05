package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/trash"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// refuseInUse refuses the removal of q, a profile by qualified name, while the scope of req, user
// scope or the project found from the working directory uses it (on, or a member of the bundle on;
// lola keeps no list of every checkout), or cannot be read to tell (loadErr: q itself does not
// load), unless req.Force, which makes those warnings, returned with the bundle files that list q
// as a member of a bundle on, by path. Under --force only a failure of the run's own scope's
// profileScope (a tracked or unreadable lock.json) still refuses.
func (e *Engine) refuseInUse(ctx context.Context, req ProfileRemoveRequest, q string, loadErr error) ([]Notice, []string, error) {
	sc := req.Scope
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
	forced := []string{q, "--remove", "--force"}
	if req.DryRun {
		forced = append(forced, "-n")
	}
	force := "or remove it anyway: " + e.Command(sc, "profile", append(forced, req.Display...)...)
	var notices []Notice
	var files []string
	for _, s := range scopes {
		_, _, _, checked, err := e.profileScope(ctx, s)
		if err != nil && s.Key() == sc.Key() {
			return nil, nil, err
		}
		var using []string
		var layers []Layer
		if err == nil {
			notices = append(notices, checked...)
			layers, _, using, err = e.uses(s, q)
		}
		if err != nil && req.Force {
			notices = append(notices, Notice{Level: LevelWarn, Text: "could not check whether " + shown(q) + " is in use " + scopeAt(s) + ": " +
				shown(untrusted.CleanErr(err).Error())})
			continue
		}
		if err != nil && loadErr != nil {
			return nil, nil, errs.WithHints(errors.New("lola cannot tell whether "+shown(q)+" is in use "+scopeAt(s)+": "+shown(q)+" itself does not load: "+
				untrusted.CleanErr(loadErr).Error()), force)
		}
		if err != nil {
			return nil, nil, errs.WithHints(errors.New("lola cannot tell whether "+shown(q)+" is in use "+scopeAt(s)+": "+untrusted.CleanErr(err).Error()),
				append(errs.Hints(err), force)...)
		}
		if len(using) == 0 {
			continue
		}
		what := "is on " + scopeAt(s)
		if !slices.Contains(using, shown(q)) {
			what = "is in bundle " + andJoin(using) + ", on " + scopeAt(s)
		}
		// The files that list q as a member of the bundle on, nested ones included.
		listed := layers[0].reached[q]
		for _, l := range listed {
			files = append(files, l.path)
		}
		if !req.Force {
			hints := []string{}
			for _, u := range using {
				if u == shown(q) {
					hints = append(hints, "turn the profile off first: "+e.Command(s, "profile", append([]string{u, "--off"}, req.Display...)...))
					continue
				}
				hints = append(hints, "turn the bundle off first: "+e.Command(s, "profile", append([]string{u, "--off"}, req.Display...)...))
				hints = append(hints, e.unlistRuns(s, listed, req.Display)...)
			}
			return nil, nil, errs.WithHints(fmt.Errorf("%s %s", shown(q), what), append(hints, force)...)
		}
		stay, removes, refuses := "stay", "removes", "refuses"
		if req.DryRun {
			stay, removes, refuses = "would stay", "would remove", "would refuse"
		}
		text := shown(q) + " " + what + "; its modules " + stay + " installed there until the next " + e.SyncCommand(s) + ", which " + removes + " them"
		if len(listed) > 0 {
			// Its bundle then names a profile you do not have, which a run refuses (expansion.member),
			// until the runs that take it out (unlistRuns, each hint's "or " dropped).
			runs := e.unlistRuns(s, listed, req.Display)
			for k := range runs {
				runs[k] = strings.TrimPrefix(runs[k], "or ")
			}
			text = shown(q) + " " + what + "; its modules " + stay + " installed there, and " + e.SyncCommand(s) + " " + refuses + " bundle " + andJoin(using) +
				" until you " + strings.Join(runs, "; and ")
		}
		notices = append(notices, Notice{Level: LevelWarn, Text: text})
	}
	return notices, files, nil
}

// ownToRemove is the profile of yours req.Name names for --remove, nil when
// it names a remote profile, which RemoveProfile then removes: user:<name>, or a bare name that
// only a profile of yours has. A bare name that both a profile of yours and a remote one have is
// refused, hinting each qualified; so are the base (default, base), a project's profile, which is
// committed (its removal is git's), and a name no profile has. Its hints keep -n and the output
// flags, never -y or --force, which answer the question of the profile the user named.
func (e *Engine) ownToRemove(sc Scope, req ProfileRemoveRequest) (*profile.Info, error) {
	again := slices.Clone(req.Display)
	if req.DryRun {
		again = append([]string{"-n"}, again...)
	}
	run := func(q string) string { return e.Command(sc, "profile", append([]string{q, "--remove"}, again...)...) }
	list := "see the profiles: " + e.Command(sc, "profile", req.Display...)
	if isBase, isDefault := namesBase(req.Name); isBase || isDefault {
		return nil, errs.WithHints(errors.New("the base is always loaded and lola keeps it; --remove deletes a profile of yours or a remote one"), list)
	}
	kind, bare, qualified := strings.Cut(req.Name, ":")
	if !qualified {
		kind, bare = "", req.Name
	}
	if qualified && !slices.Contains([]string{string(profile.KindUser), string(profile.KindProject), string(profile.KindRemote)}, kind) {
		return nil, errs.WithHints(fmt.Errorf("%s is not user:, project: or remote: followed by a name", shown(req.Name)), list)
	}
	if kind == string(profile.KindRemote) || module.ValidateName("profile", bare) != nil {
		return nil, nil // remoteNamed refuses an invalid name
	}
	mine, _, _, err := profile.Visible(e.Paths, false)
	if err != nil {
		return nil, err
	}
	k := slices.IndexFunc(mine, func(i profile.Info) bool { return i.Name == bare && i.Kind == profile.KindUser && !isDir(i) })
	_, remoteErr := os.Lstat(filepath.Join(e.Paths.RemoteProfiles, bare))
	remote := remoteErr == nil
	switch {
	case kind == string(profile.KindProject):
		return nil, e.committedProfile(sc, bare, list)
	case kind == string(profile.KindUser) && k < 0:
		return nil, errs.WithHints(fmt.Errorf("you have no profile %s", shown(req.Name)), list)
	case kind == "" && k >= 0 && remote:
		return nil, errs.WithHints(fmt.Errorf("%s names your profile user:%s and the remote profile remote:%s", shown(bare), shown(bare), shown(bare)),
			run(string(profile.KindUser)+":"+bare), run(string(profile.KindRemote)+":"+bare))
	case k >= 0:
		return &mine[k], nil
	case kind == "" && !remote && sc.Kind == target.Project:
		if _, err := os.Lstat(e.profilePath(sc, profile.KindProject, bare)); err == nil {
			return nil, e.committedProfile(sc, bare, list)
		}
		fallthrough
	case kind == "" && !remote:
		return nil, errs.WithHints(fmt.Errorf("you have no profile %s and no remote profile %s", shown(bare), shown(bare)), list)
	}
	return nil, nil
}

// committedProfile refuses --remove of name, a profile of the project at sc: it is committed, so
// git removes it, which the hint runs; at user scope, or when the project has none of that name,
// there is no such profile.
func (e *Engine) committedProfile(sc Scope, name, list string) error {
	path := e.profilePath(sc, profile.KindProject, name)
	if _, err := os.Lstat(path); sc.Kind != target.Project || err != nil {
		return errs.WithHints(fmt.Errorf("there is no profile project:%s here; --remove deletes a profile of yours or a remote one", shown(name)), list)
	}
	rel := filepath.ToSlash(filepath.Join(profile.ProjectDir, name+".yml"))
	return errs.WithHints(fmt.Errorf("project:%s is committed with the project (%s); lola does not delete it", shown(name), shown(path)),
		"remove it from the project: "+errs.ShellJoin([]string{"git", "-C", shown(sc.ProjectDir), "rm", rel}))
}

// removeOwn moves i, a profile of yours, to the trash: refused while
// it is used (refuseInUse, before -y is offered and again under the exclusive lock), asked about
// first (Confirm; -y, --force); a bundle file of yours that names it is a notice, as it then
// refuses to load until edited. A file that changed while you were asked is kept. A link goes to
// the trash as a link: the file it points to stays where it is. pins.yaml is not written: the next
// user-scope apply drops the pins no file of yours lists, as for a file deleted by hand (only
// remotes: changes outside an apply). unlock is the profiles lock the caller holds shared, which
// the move takes exclusively.
func (e *Engine) removeOwn(ctx context.Context, req ProfileRemoveRequest, i profile.Info, unlock *func()) (*ProfileRemoveResult, error) {
	sc, q := req.Scope, i.Qualified()
	again := []string{q, "--remove"}
	if req.Force {
		again = append(again, "--force")
	}
	res := &ProfileRemoveResult{Profile: shown(q), File: shown(i.Path), Notices: []Notice{}, DryRun: req.DryRun}
	before, err := asRemoved(i.Path)
	if err != nil {
		return nil, err
	}
	_, loadErr := profile.Load(i.Path)
	// In use is said before -y is offered: a -y hint never names a removal that is then refused.
	notes, said, err := e.refuseInUse(ctx, req, q, loadErr)
	if err != nil {
		return nil, err
	}
	if req.Confirm == nil && !req.DryRun && !req.Force {
		return nil, errs.WithHints(fmt.Errorf("lola profile --remove asks before it moves %s to the trash, and there is no terminal to ask on", shown(i.Path)),
			"move "+shown(i.Path)+" to the trash: "+e.Command(sc, "profile", slices.Concat(again, []string{"-y"}, req.Display)...))
	}
	_, remoteErr := os.Lstat(filepath.Join(e.Paths.RemoteProfiles, i.Name))
	res.Notices = append(append(res.Notices, notes...), e.namingBundles(i, remoteErr == nil, req.DryRun, said, req.Display)...)
	if req.DryRun {
		text := "to remove it: " + e.Command(sc, "profile", again...)
		if !req.Force {
			text += " (off a terminal: -y)"
		}
		res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: text})
		return res, nil
	}
	if !req.Force {
		ok, err := req.Confirm(RemoteProfile{Profile: res.Profile, File: res.File, Changes: []TrustChange{}})
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrDeclined
		}
	}
	(*unlock)()
	if *unlock, err = e.lockProfiles(true); err != nil {
		*unlock = func() {}
		return nil, err
	}
	// A profile switched on while you were asked is in use again; --force asked nothing and
	// overrides the check.
	if !req.Force {
		if _, _, err := e.refuseInUse(ctx, req, q, loadErr); err != nil {
			return nil, err
		}
	}
	// What you were asked about is what is removed: an edit in between is kept.
	if now, err := asRemoved(i.Path); err != nil || !bytes.Equal(now, before) {
		return nil, errs.WithHints(errors.Join(fmt.Errorf("%s changed while you were asked; nothing was removed", shown(i.Path)), untrusted.CleanErr(err)),
			"run it again: "+e.Command(sc, "profile", slices.Concat(again, req.Display)...))
	}
	fi, err := os.Lstat(i.Path)
	if err != nil {
		return nil, err
	}
	link, stays := "", ", which stays where it is"
	if fi.Mode()&fs.ModeSymlink != 0 {
		if link, err = filepath.EvalSymlinks(i.Path); errors.Is(err, fs.ErrNotExist) {
			link, err = os.Readlink(i.Path)
			stays = ", which does not exist"
		}
		if err != nil {
			return nil, fmt.Errorf("reading the link %s: %w", shown(i.Path), untrusted.CleanErr(err))
		}
	}
	t, err := trash.OpenTrash(e.Paths.Trash, e.Now(), trash.RunRecord{Command: e.Command(sc, "profile", again...), Scope: target.User.String()})
	if err != nil {
		return nil, err
	}
	dir, err := os.OpenRoot(filepath.Dir(i.Path))
	if err != nil {
		return nil, errors.Join(err, t.Close())
	}
	to, err := t.Move("profiles", filepath.Dir(i.Path), dir, filepath.Base(i.Path))
	if err = errors.Join(err, dir.Close(), t.Close()); err != nil {
		return nil, err
	}
	res.Removed, res.Trash = true, shown(to)
	if link != "" {
		res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: shown(i.Path) + " was a link to " + shown(link) + stays})
	}
	return res, nil
}

// asRemoved is what --remove of a profile of yours at path compares before and after its question:
// the file's bytes, or for a link that points at nothing, where it points, so a dangling link of
// yours is removed as any file is.
func asRemoved(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		return b, nil
	}
	to, lerr := os.Readlink(path)
	if !errors.Is(err, fs.ErrNotExist) || lerr != nil {
		return nil, fmt.Errorf("reading %s: %w", shown(path), untrusted.CleanErr(err))
	}
	return []byte("link: " + to), nil
}

// namingBundles is a notice for each bundle file of yours that names i, a profile of yours, as a
// member (bare, or user:<name>): once i is gone it refuses to load until that member is taken out,
// with the run that does (unbundleRun, keeping display), or, when remote says a remote profile has
// i's name, its bare member then names that one; but for the files in said, which a notice already
// names. dry (-n) says what would happen. A bundle file that does not load is passed over: its own
// refusal names what to fix.
func (e *Engine) namingBundles(i profile.Info, remote, dry bool, said, display []string) []Notice {
	mine, _, _, err := profile.Visible(e.Paths, false)
	if err != nil {
		return nil
	}
	var out []Notice
	for _, b := range mine {
		if b.Kind != profile.KindUser || b.Path == i.Path || isDir(b) || slices.Contains(said, b.Path) {
			continue
		}
		p, _, err := e.readProfile(b)
		if err != nil || p.Shape() != profile.ShapeBundle {
			continue
		}
		for _, typed := range p.Bundle {
			kind, name, qualified := strings.Cut(typed, ":")
			if !qualified {
				kind, name = string(profile.KindUser), typed
			}
			if kind != string(profile.KindUser) || name != i.Name {
				continue
			}
			run := e.unbundleRun(Scope{Kind: target.User}, mine, b, p.Bundle, slices.Index(p.Bundle, typed), display)
			then := "; it refuses to load until you take it out → " + run
			switch {
			case remote && !qualified && dry:
				then = ", which would then name the remote profile remote:" + shown(name)
			case remote && !qualified:
				then = ", which then names the remote profile remote:" + shown(name)
			case dry:
				then = "; it would refuse to load until you take it out → " + run
			}
			out = append(out, Notice{Level: LevelWarn, Text: "bundle " + shown(b.Name) + " (" + shown(b.Path) + ") names " + shown(typed) + then})
			break
		}
	}
	return out
}

// RemoveNames is the names lola profile --remove takes, for completion: your
// profiles and the remote ones, each bare unless the other kind has its name, which a bare name
// would leave ambiguous; sorted.
func (e *Engine) RemoveNames() ([]string, error) {
	mine, _, _, err := profile.Visible(e.Paths, false)
	if err != nil {
		return nil, err
	}
	remote, _, err := profile.Remote(e.Paths)
	if err != nil {
		return nil, err
	}
	has := func(infos []profile.Info, name string) bool {
		return slices.ContainsFunc(infos, func(i profile.Info) bool { return i.Name == name })
	}
	var out []string
	for _, i := range mine {
		if has(remote, i.Name) {
			out = append(out, i.Qualified())
		} else {
			out = append(out, i.Name)
		}
	}
	for _, i := range remote {
		if has(mine, i.Name) {
			out = append(out, i.Qualified())
		} else {
			out = append(out, i.Name)
		}
	}
	slices.Sort(out)
	return out, nil
}
