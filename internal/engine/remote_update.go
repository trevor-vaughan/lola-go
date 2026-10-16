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
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// ProfileUpdate is lola profile's action that fetches a kept remote profile again from its
// source (Plan 6b §3.2, §6.5); it is also the flag lola profile takes for it.
const ProfileUpdate ProfileAction = "update"

// ProfileUpdateRequest is one lola profile <name> --update (Plan 6b §3.2): fetch the remote
// profile Name names (as typed: go or remote:go) again from the source, ref and --path its
// source.json records. Scope is the scope whose lock.json annotates the content shown, and where
// the update is applied when the scope uses the profile.
type ProfileUpdateRequest struct {
	Scope  Scope
	Name   string
	DryRun bool
	// Flags is the run's --allow-hooks, --allow-runtime-hooks and -y as given, in canonical
	// form: a hint that repeats the run keeps them.
	Flags []string
	// Consent shows how the profile changes and asks whether to keep the new one (Plan 6b §8.2).
	// nil means it cannot ask: the run is refused before any network contact. The CLI passes one
	// that says yes for --trust, after showing it.
	Consent func([]RemoteProfile) (bool, error)
}

// ProfileUpdateResult is what lola profile --update reports: the remote profile shown, and
// written unless a dry run, the notices, and Apply, the modules the update moves in the run's
// scope (§19 P9), which the run that applies the update there syncs; [] when the scope does not
// use the profile or nothing moves.
type ProfileUpdateResult struct {
	RemoteResult
	Apply []string `json:"apply"`
}

// UpdateProfile fetches a remote profile again from its recorded source (Plan 6b §3.2, §6.5): the
// recorded source is checked as --add checks one, and a run that cannot ask for consent refused,
// before any network contact; the profile is read as untrusted input, and how its entries changed
// is shown with the whole content of every added or changed one (remoteContent). A profile whose
// pin did not change is a notice. A run's scope lola cannot read is refused before the question,
// so nothing is kept that cannot be applied. Fetching, showing and asking hold the profiles lock
// shared; it is taken exclusively only to check that the kept profile is still the one shown, and
// to replace it, crash-safe (writeRemote; ruling R-6b3b-8). When the scope uses the profile, as an
// active profile or through the extends: of an active profile or default, Apply names the
// modules the update moves there, those whose winning entry changed (§19 P9), and every other
// pending change is counted in a notice. The other scope a user sees from here (user scope, or
// from user scope the project found from the working directory) is only read: a notice says how
// to apply the update there when it uses the profile, or that it could not be read (ruling
// R-6b3b-2); other scopes pick it up at their next sync.
func (e *Engine) UpdateProfile(ctx context.Context, req ProfileUpdateRequest) (_ *ProfileUpdateResult, err error) {
	sc := req.Scope
	// The profiles lock comes first, and its sweep finishes an interrupted --update before the
	// name is looked up, so a profile left aside is never taken for none.
	unlock, err := e.readProfiles(req.DryRun)
	if err != nil {
		return nil, err
	}
	defer func() { unlock() }()
	name, err := e.remoteNamed(sc, req.Name, ProfileUpdate)
	if err != nil {
		return nil, err
	}
	q := string(profile.KindRemote) + ":" + name
	if req.Consent == nil && !req.DryRun {
		return nil, errs.WithHints(errors.New("lola profile --update shows how the profile changes and asks before keeping it, and there is no terminal to ask on"),
			"see it: "+e.Command(sc, "profile", append([]string{q, "--update", "-n"}, req.Flags...)...),
			"then update it unasked: "+e.Command(sc, "profile", append([]string{q, "--update", "--trust"}, req.Flags...)...))
	}
	// Every lola profile command refuses a tracked lock.json (Plan 6b §5, §19 S5).
	_, _, led, checked, err := e.profileScope(ctx, sc)
	if err != nil {
		return nil, err
	}
	path := e.profilePath(sc, profile.KindRemote, name)
	old, was, err := profile.LoadRemote(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, e.noRemote(sc, name, ProfileUpdate)
	}
	yes := slices.Contains(req.Flags, "-y")
	if err != nil {
		return nil, e.readd(sc, name, yes, err)
	}
	// source.json is lola's file, but it is checked as a typed source is before git sees it. The
	// check's own hints are for a typed source, so only its message is kept.
	spec, err := e.remoteSpec(was.Source, was.Ref, was.Path)
	if err != nil {
		return nil, e.readd(sc, name, yes, errors.New(shown(filepath.Join(filepath.Dir(path), profile.OriginFile))+": "+err.Error()))
	}
	res := &ProfileUpdateResult{RemoteResult: RemoteResult{Profiles: []RemoteProfile{}, Notices: append(checked, e.sweepRuns()...), DryRun: req.DryRun},
		Apply: []string{}}
	run, err := e.runDir()
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(run)) }()
	f, err := fetchProfiles(ctx, spec, run)
	if err != nil {
		return nil, err
	}
	p, b, err := profile.Fetched(f.Dir, name)
	if err != nil {
		return nil, err
	}
	o := profile.Origin{Version: profile.OriginVersion, Source: was.Source, Ref: was.Ref, Path: was.Path, Commit: f.Commit, Digest: profile.Digest(b)}
	if o == was {
		res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: shown(q) + " is up to date with " + shown(o.Source) + " (" + pinText(o) + ")"})
		return res, nil
	}
	rp := RemoteProfile{Profile: shown(q), File: shown(path), Source: shown(o.Source), Ref: shown(o.Ref), Path: shown(o.Path), Commit: shown(o.Commit),
		Digest: shown(o.Digest), PreviousCommit: shown(was.Commit), PreviousDigest: shown(was.Digest), ComparedWith: scopeName(sc),
		Description: shown(p.Description), name: name, data: b, origin: o}
	if rp.Changes, err = e.remoteContent(ctx, sc, q, led, old.Entries(), p.Entries(), run, e.newProbeRun(ctx)); err != nil {
		return nil, err
	}
	res.Profiles = append(res.Profiles, rp)
	if req.DryRun {
		res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: "to update it: " + e.Command(sc, "profile", append([]string{q, "--update"}, req.Flags...)...) +
			" (off a terminal: --trust)"})
		return res, nil
	}
	// What each scope wanted before the update, to tell what it moves (§19 P9): the run's scope,
	// which it applies to and must be readable, and the other one a user sees from here, which it
	// only reports on.
	mine := e.scopeBefore(sc, q)
	if mine.err != nil {
		return nil, errs.WithHints(fmt.Errorf("lola cannot tell whether %s is in use %s, so it did not ask to update it: %w", shown(q), scopeAt(sc), mine.err),
			errs.Hints(mine.err)...)
	}
	other, hasOther := Scope{Kind: target.User}, sc.Kind != target.User
	if !hasOther {
		other, hasOther = e.foundProject()
	}
	theirs := scopeUse{sc: other, q: q}
	if hasOther {
		if _, _, _, _, err := e.profileScope(ctx, other); err != nil {
			theirs.err = err
		} else {
			theirs = e.scopeBefore(other, q)
		}
	}
	ok, err := req.Consent(slices.Clone(res.Profiles))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrDeclined
	}
	// Replace it under the exclusive lock, once it is still the profile you were shown.
	unlock()
	if unlock, err = e.lockProfiles(true); err != nil {
		unlock = func() {}
		return nil, err
	}
	if err := e.sweepRemote(); err != nil {
		return nil, err
	}
	if _, now, err := profile.LoadRemote(path); err != nil || now != was {
		return nil, errs.WithHints(errors.Join(fmt.Errorf("%s changed while you were asked; nothing was updated", shown(q)), err),
			"run it again: "+e.Command(sc, "profile", append([]string{q, "--update"}, req.Flags...)...))
	}
	written, err := e.writeRemote(name, b, o)
	if !written {
		return nil, err
	}
	res.Profiles[0].Written = true
	if err != nil {
		// It is in place: what failed after is a warning, not a failed update.
		res.Notices = append(res.Notices, Notice{Level: LevelWarn, Text: shown(q) + " is written, but: " + shown(untrusted.CleanErr(err).Error())})
	}
	if err := mine.after(e); err != nil {
		return nil, errs.WithHints(fmt.Errorf("%s is updated, but lola could not tell whether it is in use %s: %w", shown(q), scopeAt(sc), err),
			"apply it there once that is fixed: "+e.SyncCommand(sc))
	}
	switch {
	case len(mine.using) == 0:
		res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: shown(q) + " is not in use " + scopeAt(sc) +
			"; every scope that uses it picks the update up at its next lola sync"})
	case len(mine.moved) == 0:
		res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: "the update changes nothing installed " + scopeAt(sc)})
	}
	res.Apply = mine.moved
	switch n := mine.pending; {
	case n == 1:
		res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: "1 other change is pending → " + e.SyncCommand(sc)})
	case n > 1:
		res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: fmt.Sprintf("%d other changes are pending → %s", n, e.SyncCommand(sc))})
	}
	if hasOther {
		if theirs.err == nil {
			theirs.err = theirs.after(e)
		}
		res.Notices = append(res.Notices, theirs.notice(e, q, len(mine.using) > 0)...)
	}
	return res, nil
}

// scopeUse is how a scope uses a remote profile across an update of it (Plan 6b §6.5, §19 P9):
// what its layers wanted before (old), the profiles of it that are the remote profile or extend
// it (using), and, once the update is written (after), the modules the update moves there and how
// many other differences from lock.json are pending; err is why the scope could not be read.
type scopeUse struct {
	sc      Scope
	q       string
	old     []Wanted
	using   []string
	moved   []string
	pending int
	err     error
}

// scopeBefore reads how sc uses the remote profile q before an update of it.
func (e *Engine) scopeBefore(sc Scope, q string) scopeUse {
	u := scopeUse{sc: sc, q: q, moved: []string{}}
	layers, _, using, err := e.uses(sc, q)
	if err != nil {
		u.err = err
		return u
	}
	u.old, _ = desired(layers)
	u.using = using
	return u
}

// after reads sc again once the update is written and finds what it moves: each module whose
// winning entry differs between before and now (another entry, another layer, or none) that the
// scope wants now or has installed. Every other difference between what it wants now and its
// lock.json is pending from before, and counted.
func (u *scopeUse) after(e *Engine) error {
	if u.err != nil {
		return u.err
	}
	layers, led, using, err := e.uses(u.sc, u.q)
	if err != nil {
		return err
	}
	u.using = using
	if len(using) == 0 {
		return nil
	}
	now, _ := desired(layers)
	for _, w := range slices.Concat(u.old, now) {
		o, n := find(u.old, w.Name), find(now, w.Name)
		same := o != nil && n != nil && o.From == n.From && sameEntry(o.Entry, n.Entry)
		if !same && (n != nil || led.Modules[w.Name] != nil) && !slices.Contains(u.moved, w.Name) {
			u.moved = append(u.moved, w.Name)
		}
	}
	slices.Sort(u.moved)
	for _, c := range layerChanges(now, led, filepath.Base(e.files(u.sc).manifest)) {
		if !slices.Contains(u.moved, c.name) {
			u.pending++
		}
	}
	return nil
}

// notice is what an update says about the other scope a user sees from here (ruling R-6b3b-2):
// that it could not be read; or, when it uses the profile, the command that applies the update
// there, or that the update changes nothing installed there. also says the run's scope uses it
// too.
func (u scopeUse) notice(e *Engine, q string, also bool) []Notice {
	if u.err != nil {
		return []Notice{{Level: LevelWarn, Text: "lola could not check whether " + shown(q) + " is in use " + scopeAt(u.sc) + ": " +
			shown(untrusted.CleanErr(u.err).Error())}}
	}
	if len(u.using) == 0 {
		return nil
	}
	how := "is on"
	if !slices.Contains(u.using, shown(q)) {
		how = "is extended by " + andJoin(u.using) + ","
	}
	if also {
		how = "is also" + strings.TrimPrefix(how, "is")
	}
	if len(u.moved) == 0 {
		return []Notice{{Level: LevelInfo, Text: shown(q) + " " + how + " " + scopeAt(u.sc) + "; the update changes nothing installed there"}}
	}
	return []Notice{{Level: LevelWarn, Text: shown(q) + " " + how + " " + scopeAt(u.sc) + "; apply it there: " + e.SyncCommand(u.sc, u.moved...)}}
}

// remoteNamed is the remote profile name names as typed for action (--update, --remove): go or
// remote:go, a bare name meaning the remote profile since only those can be updated or removed.
// A name of another kind is refused, saying the action is for remote profiles; one no remote
// profile has is refused, with the action for the remote profiles that exist.
func (e *Engine) remoteNamed(sc Scope, name string, action ProfileAction) (string, error) {
	kind, bare, qualified := strings.Cut(name, ":")
	if !qualified {
		kind, bare = string(profile.KindRemote), name
	}
	if kind != string(profile.KindRemote) {
		edit := "edit your own and the project's profiles by hand"
		if kind == string(profile.KindUser) || kind == string(profile.KindProject) {
			edit = "edit " + shown(e.profilePath(sc, profile.Kind(kind), bare)) + " by hand: lola updates only remote profiles"
		}
		return "", errs.WithHints(fmt.Errorf("--%s acts on remote profiles; %s is not one", action, untrusted.Text(name, untrusted.MaxName)),
			edit, "see the remote ones: "+e.Command(sc, "profile"))
	}
	if err := remoteName(bare); err != nil {
		return "", err
	}
	// A profile is a directory of lola's (profile.Remote): anything else there is none.
	fi, err := os.Lstat(filepath.Join(e.Paths.RemoteProfiles, bare))
	switch {
	case errors.Is(err, fs.ErrNotExist) || err == nil && !fi.IsDir():
		return "", e.noRemote(sc, bare, action)
	case err != nil:
		return "", err
	}
	return bare, nil
}

// noRemote is the refusal of a remote profile name that lola keeps none of, hinting action on
// each remote profile it keeps (at most three, then the listing).
func (e *Engine) noRemote(sc Scope, name string, action ProfileAction) error {
	infos, _, err := profile.Remote(e.Paths)
	if err != nil {
		return err
	}
	var hints []string
	for k, i := range infos {
		if k == 3 {
			hints = append(hints, "see them all: "+e.Command(sc, "profile"))
			break
		}
		hints = append(hints, "--"+string(action)+" "+shown(i.Qualified())+": "+e.Command(sc, "profile", i.Qualified(), "--"+string(action)))
	}
	if len(infos) == 0 {
		hints = []string{"you have none; see the profiles you have: " + e.Command(sc, "profile")}
	}
	return errs.WithHints(fmt.Errorf("you have no remote profile %s", untrusted.Text(name, untrusted.MaxName)), hints...)
}

// readd is err, the remote profile name's that does not load, with the commands that replace it
// with a fresh copy. Removing it takes --force: a profile that does not load is in use wherever
// it was read, and no check of its use can pass; yes keeps the run's -y. Then, when its
// source.json still reads and --add would take its source, ref and --path, add it again from
// them; when --add would refuse them, say why.
func (e *Engine) readd(sc Scope, name string, yes bool, err error) error {
	remove := []string{string(profile.KindRemote) + ":" + name, "--remove", "--force"}
	if yes {
		remove = append(remove, "-y")
	}
	hints := []string{"remove it: " + e.Command(sc, "profile", remove...)}
	if o, oerr := profile.ReadOrigin(filepath.Join(e.Paths.RemoteProfiles, name)); oerr == nil {
		if _, serr := e.remoteSpec(o.Source, o.Ref, o.Path); serr != nil {
			hints = append(hints, "its recorded source cannot be added again: "+untrusted.CleanErr(serr).Error()+"; add "+shown(name)+
				" from a source lola profile --add accepts")
			return errs.WithHints(err, hints...)
		}
		args := []string{name, "--add", o.Source}
		for _, f := range [][2]string{{"--ref", o.Ref}, {"--path", o.Path}} {
			if f[1] != "" {
				args = append(args, f[0], f[1])
			}
		}
		hints = append(hints, "then add it again: "+e.Command(sc, "profile", args...))
	}
	return errs.WithHints(err, hints...)
}

// pinText is a remote profile's pin for a message: its commit, short, and its digest.
func pinText(o profile.Origin) string {
	if o.Commit == "" {
		return shown(o.Digest)
	}
	return "commit " + shown(o.Commit[:min(12, len(o.Commit))]) + ", " + shown(o.Digest)
}

// uses is sc's layers and lock.json, with the qualified names of the profiles among those layers
// that are the remote profile q or extend it: q when it is on there (read as itself, not in place
// of a user profile that shadows it), and every active profile, or default, whose extends: names
// it.
func (e *Engine) uses(sc Scope, q string) ([]Layer, *ledger.Ledger, []string, error) {
	man, err := e.Manifest(sc)
	if err != nil {
		return nil, nil, nil, err
	}
	led, err := e.Ledger(sc)
	if err != nil {
		return nil, nil, nil, err
	}
	pl, err := e.loadProfiles(sc, nil, led.Profiles)
	if err != nil {
		return nil, nil, nil, err
	}
	layers := pl.assemble(e.manifestLayer(sc, man), led.Profiles)
	var using []string
	for _, l := range layers {
		if l.Name == ledger.FromProfile(q) || slices.Contains(l.extends, q) {
			using = append(using, shown(strings.TrimPrefix(l.Name, ledger.FromProfile(""))))
		}
	}
	return layers, led, using, nil
}

// foundProject is the project a lola command run from the working directory (Getwd) finds: the
// nearest parent with .lola/; false when there is none, or the working directory cannot be read.
func (e *Engine) foundProject() (Scope, bool) {
	cwd, err := e.Getwd()
	if err != nil {
		return Scope{}, false
	}
	root := FindProject(cwd)
	if fi, err := os.Stat(filepath.Join(root, ".lola")); err != nil || !fi.IsDir() {
		return Scope{}, false
	}
	return Scope{Kind: target.Project, ProjectDir: root}, true
}

// scopeAt is where a scope is, for a notice: "at user scope", or "in project" and its root,
// through untrusted.Text.
func scopeAt(sc Scope) string {
	if sc.Kind == target.User {
		return "at user scope"
	}
	return "in project " + shown(sc.ProjectDir)
}
