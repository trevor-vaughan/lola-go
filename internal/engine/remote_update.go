package engine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// ProfileUpdate is lola profile's action that fetches a kept remote profile again from its
// source (Plan 6b §3.2, §6.5); it is also the flag lola profile takes for it.
const ProfileUpdate ProfileAction = "update"

// ProfileUpdateRequest is one lola profile <name>... --update (Plan 6b §3.2): fetch the remote
// profiles Names names (as typed: go or remote:go), or every remote profile you have when All is
// set (§19 P21), again from the source, ref and --path each one's source.json records. Scopes
// are the scopes the scope flags named, none when no flag is given (§19 P20): without them the
// update is applied in every known scope that uses the profiles (knownScopes: the project found
// from the working directory, then user scope); one narrows it to that scope; --scope both names
// the project and user scope, in that order (ruling R-6b3c-2). The first of the scopes is the one
// whose lock.json annotates the content shown.
type ProfileUpdateRequest struct {
	Scopes []Scope
	Names  []string
	All    bool
	DryRun bool
	// Flags is the run's -y as given: a hint that repeats the run, or syncs only what it moves,
	// keeps it.
	Flags []string
	// Rerun is the run's own other flags (--trust for the kinds given, -n, --json, --plain,
	// --no-pager as given), which a hint that repeats the run keeps too.
	Rerun []string
	// CannotAsk says no question can be asked (off a terminal, or --json). Without a scope flag,
	// --trust answers only the profiles' consent, never a found project's trust (ruling
	// R-6b3c-7): a found project with changes to trust then refuses the run before any network
	// contact, naming it.
	CannotAsk bool
	// Consent shows how one profile changes and asks whether to keep the new one (Plan 6b §8.2):
	// it is called once per profile that changed, so a No keeps that one and the run goes on to
	// the next; an error stops the run (Ctrl+C). nil means it cannot ask: the run is refused
	// before any network contact, unless Trust covers profiles: then Show, when set, shows each
	// as a record, and Receipt, when set, is shown every profile that changed once, before any is
	// written (Plan 9 §3.4).
	Consent func([]RemoteProfile) (bool, error)
	Trust   TrustKinds
	Show    func([]RemoteProfile)
	Receipt func([]Consent) (bool, error)
	// Batch, when set, makes the update one of a command's runs (TrustBatch): what --trust covers
	// goes to it instead of Receipt, and every profile kept is staged, not written: each scope's
	// sync reads it, and CommitStaged writes it once the command's receipt is answered (CR3).
	Batch *TrustBatch
}

// What became of one remote profile of lola profile --update (Plan 6b §19 P21), as
// ProfileOutcome.Outcome reports it.
const (
	UpdateUpdated  = "updated"      // the new profile is written
	UpdateUpToDate = "up_to_date"   // the source serves the pin it has
	UpdateWould    = "would_update" // a dry run: it would be updated
	UpdateDeclined = "declined"     // you said No; it is kept as it was
	UpdateFailed   = "failed"       // it could not be fetched, read, checked or written; kept as it was
	UpdateSkipped  = "skipped"      // the run stopped (Ctrl+C) before it was updated; kept as it was
)

// ProfileOutcome is what became of one remote profile of the run, in the order they were named
// (by name for --all): its qualified name and file, the outcome (Update*), and for a failure its
// error and hints (Err, the error itself, for the caller to return when it is the run's only
// profile). Every string has been through untrusted.Text.
type ProfileOutcome struct {
	Profile string   `json:"profile"`
	File    string   `json:"file"`
	Outcome string   `json:"outcome"`
	Error   string   `json:"error"`
	Hints   []string `json:"hints"`
	Err     error    `json:"-"`
}

// ProfileUpdateResult is what lola profile --update reports: the remote profiles shown, each
// written when you accepted it (unless a dry run), every named profile's outcome (Updates), the
// notices, and Scopes, the scopes the updates apply in, in the order the run applies them ([]
// unless one was written).
type ProfileUpdateResult struct {
	RemoteResult
	Updates []ProfileOutcome `json:"updates"`
	Scopes  []UpdateScope    `json:"scopes"`
}

// Updated is the qualified names of the profiles the run wrote.
func (r *ProfileUpdateResult) Updated() []string {
	var out []string
	for _, u := range r.Updates {
		if u.Outcome == UpdateUpdated {
			out = append(out, u.Profile)
		}
	}
	return out
}

// UpdatedText says the profiles the run wrote are updated, for a message: "remote:go is updated",
// "remote:go and remote:docs are updated", "" for none. Updated's names have been through
// untrusted.Text already, so they are joined as they are.
func (r *ProfileUpdateResult) UpdatedText() string {
	updated := r.Updated()
	if len(updated) == 0 {
		return ""
	}
	return andJoin(updated) + " " + isAre(updated) + " updated"
}

// UpdateScope is one scope an update applies in (Plan 6b §19 P20): the scope (project or user)
// and the project's root ("" at user scope), and Apply, the modules the updates move there (§19
// P9), which the run that applies them syncs; [] when the scope uses none of the profiles written
// or nothing moves. Of is the scope itself.
type UpdateScope struct {
	Scope string   `json:"scope"`
	Root  string   `json:"root"`
	Apply []string `json:"apply"`
	Of    Scope    `json:"-"`
}

// UpdateStoppedError is a lola profile --update stopped by Err (Plan 6b §19 P21): an error of its
// consent such as Ctrl+C, of taking the profiles lock again after a write, or of a later staged
// profile that changed before CommitStaged wrote it. What it wrote is
// kept, nothing after was fetched or asked about, and nothing is applied. Result is the run so
// far: each profile's outcome (UpdateSkipped from where it stopped) and what each scope would
// apply.
type UpdateStoppedError struct {
	Result *ProfileUpdateResult
	Err    error
}

func (e *UpdateStoppedError) Error() string { return e.Err.Error() }
func (e *UpdateStoppedError) Unwrap() error { return e.Err }

// UpdateProfile fetches remote profiles again from their recorded sources (Plan 6b §3.2, §6.5): the
// names, the scopes and whether the run can ask for consent are checked before any network contact,
// and each profile's recorded source (as --add checks one) before its own fetch; each profile is
// then fetched, read as untrusted input, and how its entries changed shown with the whole content
// of every added or changed one (remoteContent), and asked about on its own (§19 P21): a profile
// whose pin did not change is a notice; a No keeps that one and goes on; a failure to fetch, read,
// check or write one is its outcome, and the run goes on; an error of the question (Ctrl+C) stops
// the run, keeping what it wrote (UpdateStoppedError, which carries the run so far). It asks no
// scope (§19 P20): a profile file is shared by every scope, so without a scope flag the scopes it
// applies in are every known one (knownScopes), as with --scope both (ruling R-6b3c-2), and a flag
// that names one scope narrows them to it. A scope lola cannot read is refused before any question,
// so nothing is kept that cannot be applied, with the run narrowed to the other scopes as hints.
// Fetching, showing and asking hold the profiles lock shared; it is taken exclusively only to check
// that a kept profile is still the one shown, and to replace it, crash-safe (writeRemote; ruling
// R-6b3b-8). In each of its scopes that uses a profile written, as an active profile or through the
// extends: of an active profile or default, Scopes names the modules the updates move there, those
// whose winning entry changed (§19 P9), and every other pending change is counted in a notice.
// Unless narrowed, a notice says other checkouts pick the update up at their next sync; narrowed,
// the other scope a user sees from here (user scope, or from user scope the project found from the
// working directory) is only read: a notice says how to apply the update there when it uses a
// profile written, or that it could not be read.
func (e *Engine) UpdateProfile(ctx context.Context, req ProfileUpdateRequest) (_ *ProfileUpdateResult, err error) {
	given, scopes := req.Scopes, req.Scopes
	var skipped []Notice
	if len(scopes) == 0 {
		scopes, skipped = e.knownScopes()
	}
	defer func() { err = e.CoachUnresolved(err, scopes, req.DryRun, outputOnly(req.Rerun)) }()
	sc := scopes[0]
	// Your home directory is user scope's root, never a project (ruling R-6b3c-9): a run named
	// there is refused before anything is fetched or kept, with the run at user scope instead.
	if len(given) == 1 && e.IsHome(given[0]) {
		args := []string{"--update", "--all"}
		if !req.All {
			args = nil
			for _, n := range req.Names {
				if !strings.Contains(n, ":") {
					n = string(profile.KindRemote) + ":" + n
				}
				args = append(args, n)
			}
			args = append(args, "--update")
		}
		err := fmt.Errorf("project scope at %s is your home directory, where user scope installs", shown(e.Paths.Home))
		// With neither names nor --all (an engine call; the CLI refuses it first) the run at user
		// scope would be refused too, so there is no hint.
		if !req.All && len(req.Names) == 0 {
			return nil, err
		}
		// Another scope is another question: none of the run's answers (-y, --trust, the hook
		// flags) travels into it (ruling R-6cux-4).
		display := slices.DeleteFunc(slices.Clone(req.Rerun), trustArg)
		return nil, errs.WithHints(err, "update at user scope only: "+e.ProfileRun([]Scope{{Kind: target.User}}, slices.Concat(args, display)...))
	}
	// The profiles lock comes first, and its sweep finishes an interrupted --update before the
	// names are looked up, so a profile left aside is never taken for none.
	unlock, err := e.readProfiles(req.DryRun)
	if err != nil {
		return nil, err
	}
	defer func() { unlock() }()
	names, err := e.updateNames(sc, given, req)
	if err != nil {
		return nil, err
	}
	res := &ProfileUpdateResult{RemoteResult: RemoteResult{Profiles: []RemoteProfile{}, Notices: append([]Notice{}, skipped...), DryRun: req.DryRun,
		Trusted: []Consent{}},
		Updates: []ProfileOutcome{}, Scopes: []UpdateScope{}}
	if len(names) == 0 {
		res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: "you have no remote profiles to update"})
		return res, nil
	}
	// run is the arguments of the run as a hint repeats it, with extra before the run's flags;
	// trusting is the run with --trust covering profiles too, which answers its question.
	var qs []string
	for _, n := range names {
		qs = append(qs, string(profile.KindRemote)+":"+n)
	}
	run := func(extra ...string) []string {
		args := []string{"--update"}
		if req.All {
			args = append(args, "--all")
		} else {
			args = append(slices.Clone(qs), args...)
		}
		return slices.Concat(args, extra, req.Flags, req.Rerun)
	}
	trusting := func() []string {
		arg := (req.Trust | TrustProfiles).Arg()
		return slices.DeleteFunc(run(arg), func(f string) bool { return trustArg(f) && f != arg })
	}
	covered := req.Trust.Covers(TrustProfiles)
	if req.Consent == nil && !covered && !req.DryRun {
		return nil, errs.WithHints(errors.New("lola profile --update shows how each profile changes and asks before keeping it, and there is no terminal to ask on"),
			"see it: "+e.ProfileRun(given, run("-n")...),
			"then update it unasked: "+e.ProfileRun(given, trusting()...))
	}
	// Every lola profile command refuses a tracked lock.json (Plan 6b §5, §19 S5), in every scope
	// it applies in; the first annotates the content shown.
	u := &updateRun{e: e, ctx: ctx, given: given, sc: sc, flags: slices.Concat(req.Flags, req.Rerun), probes: e.newProbeRun(ctx)}
	for i, s := range scopes {
		_, _, led, notes, err := e.profileScope(ctx, s)
		if err != nil {
			return nil, narrowUpdate(err, "", scopes, i, len(given) == 0, e, run())
		}
		if i == 0 {
			u.led = led
		}
		for _, n := range notes {
			if !slices.Contains(res.Notices, n) {
				res.Notices = append(res.Notices, n)
			}
		}
	}
	if u.templates, err = e.newTemplateRun(sc, u.led.Profiles); err != nil {
		return nil, err
	}
	res.Notices = append(res.Notices, e.sweepRuns()...)
	// What each scope wanted before the updates, to tell what they move (§19 P9): every scope they
	// apply in, which must be readable, and, when a flag narrowed it to one, the other one a user
	// sees from there, which they only report on.
	uses := make([]scopeUse, len(scopes))
	var theirs *scopeUse
	if !req.DryRun {
		for i, s := range scopes {
			if uses[i] = e.scopeBefore(s); uses[i].err != nil {
				return nil, narrowUpdate(uses[i].err, fmt.Sprintf("lola cannot tell whether %s %s in use %s, so it did not ask to update %s: ", subject(qs),
					isAre(qs), scopeAt(s), itThem(qs)), scopes, i, len(given) == 0, e, run())
			}
		}
		if err := e.refuseUnnamedTrust(req, scopes, run); err != nil {
			return nil, err
		}
		if len(given) == 1 {
			other, hasOther := Scope{Kind: target.User}, sc.Kind != target.User
			if !hasOther {
				other, hasOther = e.foundProject()
				hasOther = hasOther && !e.IsHome(other)
			}
			if hasOther {
				theirs = &scopeUse{sc: other}
				if _, _, _, _, err := e.profileScope(ctx, other); err != nil {
					theirs.err = err
				} else {
					*theirs = e.scopeBefore(other)
				}
			}
		}
	}
	var stop error
	var written []string // the qualified names written, as lock.json stores them
	locked := true       // the profiles lock is held, so the scopes can be read
	// keep writes the profile res.Profiles[i], q, whose outcome is res.Updates[k] (out until it is
	// appended), under the exclusive lock, then takes the shared one again for the next question;
	// without it the run stops there, and reads no scope.
	keep := func(i, k int, q string, was profile.Origin, out *ProfileOutcome) {
		if req.Batch != nil {
			e.stage(res.Profiles[i], was, k)
			res.Profiles[i].Written, out.Outcome = true, UpdateUpdated
			written = append(written, q)
			return
		}
		unlock()
		unlock = func() {}
		replaced, warn, err := u.write(&res.Profiles[i], was)
		switch {
		case replaced:
			res.Profiles[i].Written, out.Outcome = true, UpdateUpdated
			written = append(written, q)
		default:
			out.fail(err)
		}
		if warn != nil {
			res.Notices = append(res.Notices, *warn)
		}
		if relock, err := e.lockProfiles(false); err != nil {
			var hints []string
			for _, s := range scopes {
				hints = append(hints, "apply what was updated "+scopeAt(s)+": "+e.SyncCommand(s))
			}
			stop, locked = errs.WithHints(err, hints...), false
		} else {
			unlock = relock
		}
	}
	// trusted is each profile --trust covers: its outcome's index, its profile's and what it
	// replaces, kept once the receipt is answered (Plan 9 §3.4).
	type trustedUpdate struct {
		out, profile int
		q            string
		was          profile.Origin
	}
	var trusted []trustedUpdate
	for k, name := range names {
		q := qs[k]
		out := ProfileOutcome{Profile: shown(q), File: shown(e.profilePath(sc, profile.KindRemote, name)), Hints: []string{}}
		if stop != nil {
			out.Outcome = UpdateSkipped
			res.Updates = append(res.Updates, out)
			continue
		}
		rp, was, note, err := u.fetch(name)
		switch {
		case err != nil && ctx.Err() != nil:
			stop, out.Outcome = ctx.Err(), UpdateSkipped
		case err != nil:
			out.fail(err)
		case rp == nil:
			res.Notices = append(res.Notices, *note)
			out.Outcome = UpdateUpToDate
		case req.DryRun:
			res.Profiles = append(res.Profiles, *rp)
			if covered {
				res.Trusted = append(res.Trusted, Consent{Kind: TrustProfiles.String(), Item: rp.Profile, Change: ConsentChanged})
			}
			again := slices.DeleteFunc(slices.Concat([]string{q, "--update"}, req.Flags, req.Rerun), func(f string) bool { return f == "-n" })
			text := "to update " + shown(q) + ": " + e.ProfileRun(given, again...)
			if !covered {
				text += " (off a terminal: " + TrustProfiles.Arg() + ")"
			}
			res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: text})
			out.Outcome = UpdateWould
		case covered:
			res.Profiles = append(res.Profiles, *rp)
			if req.Show != nil {
				req.Show([]RemoteProfile{*rp})
			}
			res.Trusted = append(res.Trusted, Consent{Kind: TrustProfiles.String(), Item: rp.Profile, Change: ConsentChanged})
			trusted = append(trusted, trustedUpdate{out: len(res.Updates), profile: len(res.Profiles) - 1, q: q, was: was})
		default:
			res.Profiles = append(res.Profiles, *rp)
			ok, err := req.Consent([]RemoteProfile{*rp})
			switch {
			case err != nil:
				stop, out.Outcome = err, UpdateSkipped
			case !ok:
				out.Outcome = UpdateDeclined
			default:
				keep(len(res.Profiles)-1, len(res.Updates), q, was, &out)
			}
		}
		res.Updates = append(res.Updates, out)
	}
	if len(trusted) > 0 && req.Batch != nil && stop == nil {
		req.Batch.add(res.Trusted, nil)
	}
	if len(trusted) > 0 {
		ok, err := stop == nil, error(nil)
		if ok && req.Batch == nil && req.Receipt != nil {
			ok, err = req.Receipt(slices.Clone(res.Trusted))
		}
		for _, t := range trusted {
			switch {
			case err != nil:
				stop, res.Updates[t.out].Outcome = err, UpdateSkipped
			case stop != nil:
				res.Updates[t.out].Outcome = UpdateSkipped
			case !ok:
				res.Updates[t.out].Outcome = UpdateDeclined
			default:
				keep(t.profile, t.out, t.q, t.was, &res.Updates[t.out])
			}
		}
	}
	if len(written) > 0 && locked {
		if err := e.updateScopes(res, scopes, uses, theirs, written, len(given) != 1); err != nil {
			// Nothing is applied; every profile's outcome is still reported.
			res.Scopes = []UpdateScope{}
			stop = errors.Join(stop, err)
		}
	}
	if stop != nil {
		return nil, &UpdateStoppedError{Result: res, Err: stop}
	}
	return res, nil
}

// updateNames is the remote profiles an update names (Plan 6b §19 P21): with All, every remote
// profile you have (none is no error: the run says so); otherwise Names, each resolved as
// remoteNamed does (sc, given), a name given twice taken once. Neither is refused; the CLI
// refuses it first, with hints that keep the run's flags but the answers (-y and --trust), which a
// hint naming profiles you never named does not carry.
func (e *Engine) updateNames(sc Scope, given []Scope, req ProfileUpdateRequest) ([]string, error) {
	switch {
	case req.All:
		return e.RemoteProfileNames()
	case len(req.Names) == 0:
		// A hint for profiles never named answers no question for them: no -y or --trust (ruling
		// R-6b3c-8).
		display := slices.DeleteFunc(slices.Clone(req.Rerun), trustArg)
		return nil, errs.WithHints(errors.New("--update needs the remote profiles to update, or --all"),
			"update every one: "+e.ProfileRun(given, append([]string{"--update", "--all"}, display...)...))
	}
	var names []string
	for _, n := range req.Names {
		name, err := e.remoteNamed(sc, given, n, ProfileUpdate)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names, nil
}

// RemoteProfileNames is the names of the remote profiles you have, by the listing of their
// directory alone: for the completion of --update and --remove, and the hints of an --update that
// names none.
func (e *Engine) RemoteProfileNames() ([]string, error) {
	infos, _, err := profile.Remote(e.Paths)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, i := range infos {
		out = append(out, i.Name)
	}
	return out, nil
}

// fail records err as the outcome's failure: its message and hints, through untrusted.Text.
func (o *ProfileOutcome) fail(err error) {
	o.Outcome, o.Err, o.Error = UpdateFailed, err, shown(untrusted.CleanErr(err).Error())
	for _, h := range errs.Hints(err) {
		o.Hints = append(o.Hints, shown(h))
	}
}

// updateRun is what every profile of one lola profile --update shares: the scopes the flags named
// (none when empty), the scope whose lock.json (led) annotates the content shown, the run's flags, the
// assistants' versions probed once, and the one template budget its trust diffs render on (Plan 7
// §6.2).
type updateRun struct {
	e         *Engine
	ctx       context.Context
	given     []Scope
	sc        Scope
	led       *ledger.Ledger
	flags     []string
	probes    *probeRun
	templates *templateRun
}

// fetch fetches the kept remote profile name again from the source, ref and --path its
// source.json records, checked as a typed source is before git sees it, into a run directory of
// its own, removed before it returns: the profile to show, and the origin it replaces (was); or,
// when the source serves the pin it has, nil and the notice saying so.
func (u *updateRun) fetch(name string) (_ *RemoteProfile, was profile.Origin, _ *Notice, err error) {
	e, sc := u.e, u.sc
	defer func() { err = e.CoachUnresolved(err, nil, false, outputOnly(u.flags)) }()
	q := string(profile.KindRemote) + ":" + name
	path := e.profilePath(sc, profile.KindRemote, name)
	old, was, err := profile.LoadRemote(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, was, nil, e.noRemote(u.given, name, ProfileUpdate)
	}
	if err != nil {
		return nil, was, nil, e.readd(sc, name, err)
	}
	// The check's own hints are for a typed source, so only its message is kept.
	spec, err := e.remoteSpec(was.Source, was.Ref, was.Path)
	if err != nil {
		return nil, was, nil, e.readd(sc, name, errors.New(shown(filepath.Join(filepath.Dir(path), profile.OriginFile))+": "+err.Error()))
	}
	run, err := e.runDir()
	if err != nil {
		return nil, was, nil, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(run)) }()
	f, err := fetchProfiles(u.ctx, spec, run)
	if err != nil {
		return nil, was, nil, err
	}
	p, b, err := profile.Fetched(f.Dir, name)
	if err != nil {
		return nil, was, nil, err
	}
	o := profile.Origin{Version: profile.OriginVersion, Source: was.Source, Ref: was.Ref, Path: was.Path, Commit: f.Commit, Digest: profile.Digest(b)}
	if o == was {
		return nil, was, &Notice{Level: LevelInfo, Text: shown(q) + " is up to date with " + shown(o.Source) + " (" + pinText(o) + ")"}, nil
	}
	rp := &RemoteProfile{Profile: shown(q), File: shown(path), Source: shown(o.Source), Ref: shown(o.Ref), Path: shown(o.Path), Commit: shown(o.Commit),
		Digest: shown(o.Digest), PreviousCommit: shown(was.Commit), PreviousDigest: shown(was.Digest), ComparedWith: scopeName(sc),
		Description: shown(p.Description), name: name, data: b, origin: o, entries: p.Entries()}
	if rp.Changes, err = e.remoteContent(u.ctx, sc, q, u.led, old.Entries(), p.Entries(), run, u.probes, u.templates, outputOnly(u.flags)); err != nil {
		return nil, was, nil, err
	}
	return rp, was, nil, nil
}

// write replaces the kept remote profile rp.name with rp under the exclusive profiles lock, once
// it is still the one whose origin was shown (was), crash-safe (writeRemote; ruling R-6b3b-8).
// written reports that the new profile is in place; a failure after that is warn, not an error.
func (u *updateRun) write(rp *RemoteProfile, was profile.Origin) (written bool, warn *Notice, err error) {
	e := u.e
	unlock, err := e.lockProfiles(true)
	if err != nil {
		return false, nil, err
	}
	defer unlock()
	if err := e.sweepRemote(); err != nil {
		return false, nil, err
	}
	q := string(profile.KindRemote) + ":" + rp.name
	if _, now, err := profile.LoadRemote(e.profilePath(u.sc, profile.KindRemote, rp.name)); err != nil || now != was {
		return false, nil, errs.WithHints(errors.Join(fmt.Errorf("%s changed while you were asked; it was not updated", shown(q)), err),
			"run it again: "+e.ProfileRun(u.given, append([]string{q, "--update"}, u.flags...)...))
	}
	written, err = e.writeRemote(rp.name, rp.data, rp.origin)
	if !written {
		return false, nil, err
	}
	if err != nil {
		return true, &Notice{Level: LevelWarn, Text: shown(q) + " is written, but: " + shown(untrusted.CleanErr(err).Error())}, nil
	}
	return true, nil, nil
}

// updateScopes reads each scope of an update again once the profiles qs are written and records,
// in order, the modules they move there (res.Scopes, §19 P9), with a notice for a scope that uses
// none of them, one where they change nothing installed, and one counting what else is pending.
// Unnarrowed (every known scope), a notice says other checkouts catch up at their next sync; a
// narrowed run's other scope, theirs (nil when there is none), gets the notice that says how to
// apply them there, or that it could not be read.
func (e *Engine) updateScopes(res *ProfileUpdateResult, scopes []Scope, uses []scopeUse, theirs *scopeUse, qs []string, everywhere bool) error {
	if theirs != nil && theirs.err == nil {
		theirs.err = theirs.after(e, qs)
	}
	for i := range uses {
		u := &uses[i]
		if err := u.after(e, qs); err != nil {
			// Nothing is applied: the CLI syncs once every scope is read. A whole scope's sync
			// carries none of the run's answers (ruling R-6b3c-8).
			var hints []string
			for _, s := range scopes {
				hints = append(hints, "apply "+itThem(qs)+" "+scopeAt(s)+" once that is fixed: "+e.SyncCommand(s))
			}
			// What was written is said by the caller's stop (UpdateStoppedError.Result.Updates).
			return errs.WithHints(errors.New(fmt.Sprintf("lola could not tell whether %s %s in use %s: ", subject(qs), isAre(qs),
				scopeAt(u.sc))+untrusted.CleanErr(err).Error()), append(errs.Hints(err), hints...)...)
		}
		theirsUse := theirs != nil && theirs.err == nil && len(theirs.using) > 0
		switch {
		case len(u.using) == 0 && !everywhere && !theirsUse:
			res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: subject(qs) + " " + isAre(qs) + " not in use " + scopeAt(u.sc) +
				"; every scope that uses " + itThem(qs) + " picks the update up at its next lola sync"})
		case len(u.using) == 0:
			res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: subject(qs) + " " + isAre(qs) + " not in use " + scopeAt(u.sc)})
		case len(u.moved) == 0:
			res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: "the update changes nothing installed " + scopeAt(u.sc)})
		}
		switch n := u.pending; {
		case n == 1:
			res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: "1 other change is pending → " + e.SyncCommand(u.sc)})
		case n > 1:
			res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: fmt.Sprintf("%d other changes are pending → %s", n, e.SyncCommand(u.sc))})
		}
		res.Scopes = append(res.Scopes, UpdateScope{Scope: u.sc.Kind.String(), Root: shown(root(u.sc)), Apply: u.moved, Of: u.sc})
	}
	if everywhere {
		res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: "every other checkout that uses " + subject(qs) + " picks the update up at its next lola sync"})
	}
	if theirs != nil {
		res.Notices = append(res.Notices, theirs.notice(e, qs, len(uses[0].using) > 0)...)
	}
	return nil
}

// narrowUpdate is the refusal of an update that cannot read scopes[failed] for err: err, its
// message after prefix, with its hints, then the run (args, the arguments that repeat it) narrowed
// to each other scope it would apply in (Plan 6b §19 P20). A run a flag narrowed to one scope has
// none, and is err itself when there is no prefix. A run narrowed to one scope is another question,
// so it carries none of the run's answers: -y and --trust in any form (ruling R-6cux-4). A run
// with no scope flag (unscoped) says, of a project, which changes there you have not trusted
// (rulings R-6b3c-7, R-6b3c-8), since its --trust never answered them.
func narrowUpdate(err error, prefix string, scopes []Scope, failed int, unscoped bool, e *Engine, args []string) error {
	msg := prefix + untrusted.CleanErr(err).Error()
	run := slices.DeleteFunc(slices.Clone(args), func(a string) bool { return a == "-y" || trustArg(a) })
	var narrowed []string
	for i, s := range scopes {
		if i == failed {
			continue
		}
		if unscoped && s.Kind == target.Project {
			if mods, _, err := e.untrustedModules(s); err == nil && len(mods) > 0 {
				msg += "; project " + shown(s.ProjectDir) + " has changes you have not trusted (" + andJoin(mods) +
					"), which its sync asks about: the command for it leaves --trust out, which you add once you have reviewed them"
			}
		}
		narrowed = append(narrowed, "or update it "+scopeAt(s)+" only: "+e.Command(s, "profile", run...))
	}
	if prefix == "" && len(narrowed) == 0 {
		return err
	}
	// A new error, so its hints are said once (errs.Hints walks the wrap chain).
	return errs.WithHints(errors.New(msg), append(errs.Hints(err), narrowed...)...)
}

// refuseUnnamedTrust refuses, before any network contact, an update with no scope flag that
// cannot ask (req.CannotAsk) when a project among scopes has changes to project.yml or its
// active profiles you have not trusted (ruling R-6b3c-7): --trust answers only the profiles'
// consent there, never that project's trust, so its sync would refuse after the profiles were
// kept. It names the project and the modules, and hints the run (run, its arguments) with the
// project named, alone or with --scope both, and --trust covering the kinds they need too.
func (e *Engine) refuseUnnamedTrust(req ProfileUpdateRequest, scopes []Scope, run func(...string) []string) error {
	if !req.CannotAsk || len(req.Scopes) > 0 {
		return nil
	}
	for _, s := range scopes {
		if s.Kind != target.Project {
			continue
		}
		mods, need, err := e.untrustedModules(s)
		if err != nil {
			return err
		}
		if len(mods) == 0 {
			continue
		}
		// The run with --trust covering the kinds the project's changes need too (Plan 9 §3.5).
		arg := (req.Trust | need).Arg()
		args := append(slices.DeleteFunc(run(), trustArg), arg)
		return errs.WithHints(fmt.Errorf("project %s has changes you have not trusted (%s), which its sync asks about, and no flag named it: "+
			"--trust then answers only for the remote profiles", shown(s.ProjectDir), andJoin(mods)),
			"name it, and trust them: "+e.ProfileRun([]Scope{s}, args...),
			"or with user scope too: "+e.ProfileRun([]Scope{s, {Kind: target.User}}, args...))
	}
	return nil
}

// untrustedModules is the modules of sc's project.yml and active project profiles whose entries
// differ from what you trusted (Plan 6a §7, Plan 6b §8.1), through untrusted.Text: what a sync of
// that project asks about before it installs them; and the kinds of trust they need.
func (e *Engine) untrustedModules(sc Scope) ([]string, TrustKinds, error) {
	man, err := e.Manifest(sc)
	if err != nil {
		return nil, 0, err
	}
	layers, _, _, err := e.uses(sc)
	if err != nil {
		return nil, 0, err
	}
	rec, err := e.Trust.Load(sc.ProjectDir)
	if err != nil {
		return nil, 0, err
	}
	rep := checkParts(rec, trustParts(man, layers, nil))
	var out []string
	for _, c := range rep.Changes {
		if m := shown(c.Module); !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	if rep.Targets != nil {
		out = append(out, "project.yml's targets")
	}
	return out, neededTrust(rep, nil, nil), nil
}

// subject names the remote profiles qs for a message: remote:go, or remote:go and remote:docs;
// isAre and itThem agree with it.
func subject(qs []string) string {
	shownQs := make([]string, len(qs))
	for i, q := range qs {
		shownQs[i] = shown(q)
	}
	return andJoin(shownQs)
}

func isAre(qs []string) string {
	if len(qs) == 1 {
		return "is"
	}
	return "are"
}

func itThem(qs []string) string {
	if len(qs) == 1 {
		return "it"
	}
	return "them"
}

// scopeUse is how a scope uses remote profiles across an update of them (Plan 6b §6.5, §19 P9):
// what its layers wanted before (old), and, once the updates are written (after), the profiles of
// it that are one of them or extend one (using), the modules the updates move there and how many
// other differences from lock.json are pending; err is why the scope could not be read.
type scopeUse struct {
	sc      Scope
	old     []Wanted
	using   []string
	moved   []string
	pending int
	err     error
}

// scopeBefore reads what sc wants before an update.
func (e *Engine) scopeBefore(sc Scope) scopeUse {
	u := scopeUse{sc: sc, moved: []string{}}
	layers, _, _, err := e.uses(sc)
	if err != nil {
		u.err = err
		return u
	}
	u.old, _ = desired(layers)
	return u
}

// after reads sc again once the remote profiles qs are written and finds what they move: each
// module whose winning entry differs between before and now (another entry, another layer, or
// none) that the scope wants now or has installed. Every other difference between what it wants
// now and its lock.json is pending from before, and counted.
func (u *scopeUse) after(e *Engine, qs []string) error {
	if u.err != nil {
		return u.err
	}
	layers, led, using, err := e.uses(u.sc, qs...)
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

// notice is what an update of the remote profiles qs says about the other scope a user sees from
// here (ruling R-6b3b-2, for a run a scope flag narrowed): that it could not be read; or, when it
// uses one of them, the command that applies the update there, or that the update changes nothing
// installed there; of several, it names only the profiles the scope uses. also says the run's scope
// uses them too.
func (u scopeUse) notice(e *Engine, qs []string, also bool) []Notice {
	if u.err != nil {
		return []Notice{{Level: LevelWarn, Text: "lola could not check whether " + subject(qs) + " " + isAre(qs) + " in use " + scopeAt(u.sc) + ": " +
			shown(untrusted.CleanErr(u.err).Error())}}
	}
	if len(u.using) == 0 {
		return nil
	}
	text, apply := u.usedText(qs, also), "apply it there"
	if len(qs) > 1 {
		apply = "apply the update there"
	}
	if len(u.moved) == 0 {
		return []Notice{{Level: LevelInfo, Text: text + "; the update changes nothing installed there"}}
	}
	return []Notice{{Level: LevelWarn, Text: text + "; " + apply + ": " + e.SyncCommand(u.sc, u.moved...)}}
}

// usedText says how the scope uses the remote profiles qs (u.using, the scope's profiles that are
// one of them or extend one; also when the run's scope uses them too). Of one, it says it is on, or
// which profiles extend it; of several, it names only those on and those extending one, each as
// what it is, never that a profile it does not use is in use.
func (u scopeUse) usedText(qs []string, also bool) string {
	at, on := scopeAt(u.sc), "on"
	if also {
		on = "also on"
	}
	if len(qs) == 1 {
		if slices.Contains(u.using, shown(qs[0])) {
			return subject(qs) + " is " + on + " " + at
		}
		how := "is extended by " + andJoin(u.using) + ","
		if also {
			how = "is also extended by " + andJoin(u.using) + ","
		}
		return subject(qs) + " " + how + " " + at
	}
	var active, ext []string
	for _, n := range u.using {
		if slices.ContainsFunc(qs, func(q string) bool { return shown(q) == n }) {
			active = append(active, n)
		} else {
			ext = append(ext, n)
		}
	}
	extend := "extends"
	if len(ext) > 1 {
		extend = "extend"
	}
	switch {
	case len(ext) == 0:
		return andJoin(active) + " " + isAre(active) + " " + on + " " + at
	case len(active) == 0:
		return andJoin(ext) + " " + isAre(ext) + " " + on + " " + at + " and " + extend + " one of the updated profiles"
	}
	return andJoin(active) + " " + isAre(active) + " " + on + " " + at + ", and so " + isAre(ext) + " " + andJoin(ext) + ", which " + extend +
		" one of the updated profiles"
}

// remoteNamed is the remote profile name names as typed for action (--update, --remove): go or
// remote:go, a bare name meaning the remote profile since only those can be updated or removed.
// A name of another kind is refused, saying the action is for remote profiles (sc locates a local
// profile's file); one no remote profile has is refused, with the action for the remote profiles
// that exist. Hints carry the run's scope flags, for the scopes given (none when empty).
func (e *Engine) remoteNamed(sc Scope, given []Scope, name string, action ProfileAction) (string, error) {
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
			edit, "see the remote ones: "+e.ProfileRun(given))
	}
	if err := remoteName(bare); err != nil {
		return "", err
	}
	// A profile is a directory of lola's (profile.Remote): anything else there is none.
	fi, err := os.Lstat(filepath.Join(e.Paths.RemoteProfiles, bare))
	switch {
	case errors.Is(err, fs.ErrNotExist) || err == nil && !fi.IsDir():
		return "", e.noRemote(given, bare, action)
	case err != nil:
		return "", err
	}
	return bare, nil
}

// noRemote is the refusal of a remote profile name that lola keeps none of, hinting action on
// each remote profile it keeps (at most three, then the listing), with the run's scope flags for
// the scopes given (none when empty).
func (e *Engine) noRemote(given []Scope, name string, action ProfileAction) error {
	infos, _, err := profile.Remote(e.Paths)
	if err != nil {
		return err
	}
	var hints []string
	for k, i := range infos {
		if k == 3 {
			hints = append(hints, "see them all: "+e.ProfileRun(given))
			break
		}
		hints = append(hints, "--"+string(action)+" "+shown(i.Qualified())+": "+e.ProfileRun(given, i.Qualified(), "--"+string(action)))
	}
	if len(infos) == 0 {
		hints = []string{"you have none; see the profiles you have: " + e.ProfileRun(given)}
	}
	return errs.WithHints(fmt.Errorf("you have no remote profile %s", untrusted.Text(name, untrusted.MaxName)), hints...)
}

// readd is err, the remote profile name's that does not load, with the commands that replace it
// with a fresh copy. Removing it takes --force: a profile that does not load is in use wherever
// it was read, and no check of its use can pass; it never carries -y (ruling R-6b3c-8). Then, when its
// source.json still reads and --add would take its source, ref and --path, add it again from
// them; when --add would refuse them, say why.
func (e *Engine) readd(sc Scope, name string, err error) error {
	// Its source would serve the same entries again: CoachUnresolved says what can fix them.
	if _, ok := errors.AsType[*manifest.UnresolvedError](err); ok {
		return err
	}
	remove := []string{string(profile.KindRemote) + ":" + name, "--remove", "--force"}
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
// that are one of the remote profiles qs or extend one: each of qs that is on there (read as
// itself, not in place of a user profile that shadows it), and every active profile, or default,
// whose extends: names one.
func (e *Engine) uses(sc Scope, qs ...string) ([]Layer, *ledger.Ledger, []string, error) {
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
		if slices.ContainsFunc(qs, func(q string) bool { return l.Name == ledger.FromProfile(q) || slices.Contains(l.extends, q) }) {
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

// stagedRemote is a remote profile an update in a batch keeps without writing it: the layer each
// scope's sync reads in its place, and what CommitStaged writes; out is its outcome's index in
// the update's result.
type stagedRemote struct {
	rp  RemoteProfile
	was profile.Origin
	out int
}

// stage keeps rp, whose outcome is the update's out-th, in place of the remote profile at its
// path for the rest of the command (CR3).
func (e *Engine) stage(rp RemoteProfile, was profile.Origin, out int) {
	if e.staged == nil {
		e.staged = map[string]stagedRemote{}
	}
	e.staged[e.profilePath(Scope{Kind: target.User}, profile.KindRemote, rp.name)] = stagedRemote{rp: rp, was: was, out: out}
}

// CommitStaged writes every profile the update req staged, in the order it asked about them,
// under the exclusive profiles lock, each refused when it changed since it was read
// (updateRun.write, whose hint repeats req), then forgets them; DropStaged forgets them unwritten
// (a No at the command's receipt). A warning of a write is added to res, the update's result. The
// first refusal stops it: before any write, it is the error; after one, it is an
// UpdateStoppedError carrying res with what was written, the refused profile and those after it
// skipped, and nothing applied: its hints sync each scope of the update.
func (e *Engine) CommitStaged(req ProfileUpdateRequest, res *ProfileUpdateResult) error {
	defer e.DropStaged()
	staged := slices.SortedFunc(maps.Values(e.staged), func(a, b stagedRemote) int { return cmp.Compare(a.out, b.out) })
	u := &updateRun{e: e, given: req.Scopes, sc: Scope{Kind: target.User}, flags: slices.Concat(req.Flags, req.Rerun)}
	for i, s := range staged {
		written, warn, err := u.write(&s.rp, s.was)
		if warn != nil {
			res.Notices = append(res.Notices, *warn)
		}
		if written {
			continue
		}
		if i == 0 {
			return err
		}
		var hints []string
		for _, sc := range res.Scopes {
			hints = append(hints, "apply what was updated "+scopeAt(sc.Of)+": "+e.SyncCommand(sc.Of, outputOnly(u.flags)...))
		}
		for _, r := range staged[i:] {
			res.Updates[r.out].Outcome = UpdateSkipped
			for p := range res.Profiles {
				if res.Profiles[p].name == r.rp.name {
					res.Profiles[p].Written = false
				}
			}
		}
		res.Scopes = []UpdateScope{}
		return &UpdateStoppedError{Result: res, Err: errs.WithHints(err, hints...)}
	}
	return nil
}

// DropStaged forgets the staged profiles.
func (e *Engine) DropStaged() { e.staged = nil }
