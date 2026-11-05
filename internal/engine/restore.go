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

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/trust"
)

// restoreRemotes restores, before a run at sc reads its layers, each remote profile the run needs
// (needed) that this machine lacks and pins.yaml records (spec 2026-10-29 §3.4); extra is the
// profile an activation names ("" for none), and retry the run's own command, which a hint names
// when the source cannot be reached ("" for none). A dry run restores nothing: it warns, and the
// run's read of its layers plans the profile as missing (loadStack); the same run without -n
// restores it (a sync would not restore one a --switch -n names). A project whose lock.json git
// tracks is refused before its profile on is read, as every read of it is.
func (e *Engine) restoreRemotes(ctx context.Context, sc Scope, extra, retry string, dry bool) ([]Notice, error) {
	pins, err := config.LoadPins(e.Paths.Pins)
	if err != nil || len(pins.Remotes) == 0 {
		return nil, err
	}
	if err := e.refuseTrackedLedger(ctx, sc, newResult(true)); err != nil {
		return nil, err
	}
	names, err := e.needed(sc, extra)
	if err != nil {
		return nil, err
	}
	var out []Notice
	if !dry {
		out = e.sweepRuns()
	}
	for _, name := range names {
		r, recorded := pins.Remotes[name]
		if !recorded {
			continue
		}
		// Planned without it, the run would remove the modules it lists.
		if at := e.inTheWay(name); at != "" {
			return nil, inTheWayError(name, at, retry)
		}
		if e.haveRemote(name) {
			continue
		}
		if dry {
			out = append(out, Notice{Level: LevelWarn, Text: "remote:" + shown(name) + " is not on this machine yet; run this again without -n to restore it from pins.yaml"})
			continue
		}
		n, err := e.restoreOne(ctx, name, r, retry)
		if err != nil {
			return nil, err
		}
		if n != nil {
			out = append(out, *n)
		}
	}
	return out, nil
}

// haveRemote reports whether anything is at the place of the remote profile name: a profile, or
// something in its way, which a restore never writes over.
func (e *Engine) haveRemote(name string) bool {
	_, err := os.Lstat(filepath.Join(e.Paths.RemoteProfiles, name))
	return !errors.Is(err, fs.ErrNotExist)
}

// inTheWay is the path, as output shows it, of what is at the place of the remote profile name
// and is not a directory (a file, or a link, which lola never follows there), which a restore
// never writes over; "" when nothing, or a directory, is there.
func (e *Engine) inTheWay(name string) string {
	path := filepath.Join(e.Paths.RemoteProfiles, name)
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) || err == nil && fi.IsDir() {
		return ""
	}
	return shown(path)
}

// inTheWayError refuses a run that needs name, a remote profile pins.yaml records, while
// something else is at its place (at, inTheWay's), hinting retry, the run's command, once it is
// moved aside.
func inTheWayError(name, at, retry string) error {
	hint := "move it aside"
	if retry != "" {
		hint += ", then run it again: " + retry
	}
	return errs.WithHints(fmt.Errorf("%s is in the way of remote:%s, which pins.yaml records; lola never writes over it", at, shown(name)), hint)
}

// needed is the remote profiles a run at sc needs, by name (spec 2026-10-29 §3.4): the profile on
// and extra, the profile an activation names, which becomes the profile on: a remote one itself,
// when no profile of yours of its name shadows it (activeInfo, resolveProfile), or the remote
// members a bundle expands to that this machine lacks (Engine.members, read for names alone). A
// name no profile has is a remote one when it is remote:<name> or bare. A member reached through
// a project bundle is needed only while the trust record covers the member list of each project
// bundle on its way: a pulled member list is asked about first, and fetches nothing before it is
// trusted (the run plans the member missing). A profile file that does not load is left to the
// run's own read, which refuses it. With no profile on and none named it reads nothing more, so a
// project run never lists the profiles of yours for it (D8).
func (e *Engine) needed(sc Scope, extra string) ([]string, error) {
	led, err := e.Ledger(sc)
	if err != nil {
		return nil, err
	}
	if led.Profile == nil && extra == "" {
		return nil, nil
	}
	infos, _, err := e.visible(sc)
	if err != nil {
		return nil, err
	}
	var names []string
	add := func(n string) {
		if n != "" && !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	var rec *trust.Record
	trusted := func(chain []bundleRef) (bool, error) {
		for _, b := range chain {
			if b.key == "" {
				continue
			}
			if rec == nil {
				r, err := e.Trust.Load(sc.ProjectDir)
				if err != nil {
					return false, err
				}
				rec = r
			}
			if !slices.Equal(rec.View.Part(b.key).Bundle, b.members) {
				return false, nil
			}
		}
		return true, nil
	}
	uses := func(i profile.Info) error {
		if i.Kind == profile.KindRemote {
			return nil // it is here
		}
		_, x, err := e.members(sc, infos, i)
		if err != nil {
			return nil
		}
		for _, l := range x.left {
			ok, err := trusted(l.chain)
			if err != nil {
				return err
			}
			if ok {
				add(strings.TrimPrefix(l.name, string(profile.KindRemote)+":"))
			}
		}
		return nil
	}
	missing := func(name string) {
		if kind, bare, qualified := strings.Cut(name, ":"); !qualified {
			add(name)
		} else if kind == string(profile.KindRemote) {
			add(bare)
		}
	}
	if led.Profile != nil {
		if k := activeInfo(infos, led.Profile.Name); k < 0 {
			missing(led.Profile.Name)
		} else if err := uses(infos[k]); err != nil {
			return nil, err
		}
	}
	if extra != "" {
		if i, err := e.resolveProfile(sc, infos, extra, nil); err == nil {
			if err := uses(i); err != nil {
				return nil, err
			}
		} else if _, unknown := errors.AsType[*unknownProfileError](err); unknown {
			missing(extra)
		}
	}
	return names, nil
}

// restoreOne fetches the remote profile name at its record r and writes it as lola profile --add
// does (spec 2026-10-29 §3.4), with no consent question: the digest proves these are the bytes
// you accepted. The source rules come first (remoteSpec, before any network contact); for git the
// recorded commit must be on the recorded ref, is fetched by its SHA and must be what the fetch
// resolves; the bytes must have the recorded digest. A source that no longer serves the recorded
// version is refused hinting --add, which reviews what it serves now; one lola cannot reach keeps
// its own coaching and hints retry, the run's command. It writes under the profiles lock, taken
// exclusively only to check again that nothing took the profile's place while it fetched (an --add
// or --update, or a sweep putting back what a killed one left): then it writes nothing, and
// returns no notice.
func (e *Engine) restoreOne(ctx context.Context, name string, r config.Remote, retry string) (_ *Notice, err error) {
	named := func(err error, hints ...string) error {
		return errs.WithHints(fmt.Errorf("%s: %w", shown(name), err), hints...)
	}
	again := []string{name, "--add", r.Source}
	if r.Ref != "" {
		again = append(again, "--ref", r.Ref)
	}
	if r.Path != "" {
		again = append(again, "--path", r.Path)
	}
	moved := "review and take what it serves now: " + e.ProfileRun(nil, again...)
	var unreached []string
	if retry != "" {
		unreached = []string{"run it again: " + retry}
	}
	spec, err := e.remoteSpec(r.Source, r.Ref, r.Path)
	if err != nil {
		return nil, named(err)
	}
	run, done, err := e.runDir()
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, done()) }()
	fetch := spec
	if spec.Kind == source.KindGit {
		err := source.CheckReachable(ctx, spec, r.Commit, run)
		if _, notOn := errors.AsType[*source.NotOnRefError](err); notOn {
			return nil, named(err, moved)
		}
		if err != nil {
			return nil, named(err, unreached...)
		}
		fetch.Ref = r.Commit
	}
	f, err := fetchProfiles(ctx, fetch, run)
	if err != nil {
		return nil, named(err, unreached...)
	}
	_, b, err := profile.Fetched(f.Dir, name)
	if err != nil {
		return nil, named(err, moved)
	}
	if f.Commit != r.Commit || profile.Digest(b) != r.Digest {
		return nil, named(errors.New("the source no longer serves the recorded version"), moved)
	}
	unlock, err := e.lockProfiles(true)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := e.sweepRemote(); err != nil {
		return nil, err
	}
	if at := e.inTheWay(name); at != "" {
		return nil, inTheWayError(name, at, retry)
	}
	if e.haveRemote(name) {
		return nil, nil
	}
	o := profile.Origin{Version: profile.OriginVersion, Source: spec.Location, Ref: r.Ref, Path: r.Path, Commit: r.Commit, Digest: r.Digest}
	if _, err := e.writeRemote(name, b, o); err != nil {
		return nil, err
	}
	at := r.Commit
	if len(at) > 12 {
		at = at[:12]
	}
	if at == "" {
		at = versionID(r.Digest)
	}
	return &Notice{Level: LevelInfo, Text: "restored remote profile " + shown(name) + " from pins.yaml (" + shown(r.Source) + " @ " + at + ")"}, nil
}

// recorded reports whether q, a profile's qualified name, is a remote profile pins.yaml records,
// which a run that needs it restores (restoreRemotes). A pins.yaml that does not read records
// nothing here: the run that reads it refuses it.
func (e *Engine) recorded(q string) bool {
	name, ok := strings.CutPrefix(q, string(profile.KindRemote)+":")
	if !ok {
		return false
	}
	pins, err := config.LoadPins(e.Paths.Pins)
	if err != nil {
		return false
	}
	_, ok = pins.Remotes[name]
	return ok
}
