package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/gofrs/flock"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/fsx"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/trust"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// RemoteProfile is a remote profile as lola profile --add or --update shows and writes it (Plan
// 6b §6.5, §12.2): its qualified name and the file lola keeps it in, which every scope shares, the
// source, ref and --path it comes from and its pin (source.json), its description, and Changes, its
// entries as a trust diff against the entries it had (none for --add). Every added or changed
// entry lists the whole content its pinned version brings, files, MCP servers and hooks, whatever
// a scope has installed (ruling R-6b3b-7): ComparedWith names the scope ("user scope", "project
// <dir>") whose lock.json only annotates it (FilesNote). PreviousCommit and PreviousDigest are the
// pin an --update replaces ("" for --add). Written says lola wrote it. Every string has been
// through untrusted.Text.
type RemoteProfile struct {
	Profile        string        `json:"profile"`
	File           string        `json:"file"`
	Source         string        `json:"source"`
	Ref            string        `json:"ref"`
	Path           string        `json:"path"`
	Commit         string        `json:"commit"`
	Digest         string        `json:"digest"`
	PreviousCommit string        `json:"previous_commit"`
	PreviousDigest string        `json:"previous_digest"`
	ComparedWith   string        `json:"compared_with"`
	Description    string        `json:"description"`
	Changes        []TrustChange `json:"changes"`
	Written        bool          `json:"written"`
	name           string
	data           []byte
	origin         profile.Origin
	entries        []manifest.Entry // the profile's entries, which a staged update gives its layer
}

// RemoteResult is what lola profile --add and --update report: the remote profiles shown, and
// written unless DryRun, and the notices.
type RemoteResult struct {
	Profiles []RemoteProfile `json:"remote_profiles"`
	Notices  []Notice        `json:"notices"`
	DryRun   bool            `json:"dry_run"`
	// Trusted is what the run's --trust covers, as Result's (Plan 9 §8): [] for --add and
	// --update, absent for --remove, which takes no --trust.
	Trusted []Consent `json:"trusted,omitzero"`
}

// consentRemote is the answer to keeping rps, remote profiles an --add or an --update fetched:
// when --trust covers profiles, show shows them as a record and receipt is shown what is
// trusted, items, once (Plan 9 §3.4); otherwise ask asks.
func consentRemote(rps []RemoteProfile, items []Consent, covered bool, ask func([]RemoteProfile) (bool, error),
	show func([]RemoteProfile), receipt func([]Consent) (bool, error)) (bool, error) {
	if !covered {
		return ask(rps)
	}
	if show != nil {
		show(rps)
	}
	if receipt == nil {
		return true, nil
	}
	return receipt(slices.Clone(items))
}

// remoteName refuses a name no remote profile can have: one that fails the module-name rules,
// and default, which only your own default.yml is (Plan 6b §4.1).
func remoteName(name string) error {
	if err := module.ValidateName("profile", name); err != nil {
		return err
	}
	if name == profile.DefaultName {
		return errs.WithHints(errors.New("a remote profile cannot be named default"), "default is your own default.yml, always on at user scope")
	}
	return nil
}

// remoteSpec is the source a remote profile is fetched from (Plan 6b §4.2, D4): src as typed, a
// git repository or an https archive at a host named by name (source.CheckRemote, ruling
// R-6b3b-6), never a folder, a local archive or a file:// URL, with every recorded-source rule
// (source.Recorded.Check) and the fetcher's (source.Validate) checked before any network contact.
func (e *Engine) remoteSpec(src, ref, path string) (source.Spec, error) {
	if err := source.CheckRemote(src, profile.RemoteWho); err != nil {
		return source.Spec{}, err
	}
	cwd, err := e.Getwd()
	if err != nil {
		return source.Spec{}, err
	}
	spec, ok, err := source.Parse(src, cwd)
	switch {
	case err != nil:
		return source.Spec{}, err
	case !ok:
		return source.Spec{}, errs.WithHints(fmt.Errorf("%s is not a source", untrusted.Text(src, untrusted.MaxName)),
			"a remote profile comes from a git repository (https://, ssh:// or user@host:path) or an https:// archive")
	case source.IsLocal(spec.Kind, spec.Location): // a Windows drive path, which git's scp form also matches
		return source.Spec{}, errs.WithHints(fmt.Errorf("%s is on this machine; a remote profile comes from a git repository or an https archive",
			untrusted.Text(src, untrusted.MaxName)), "for a profile of your own, write it as "+shown(filepath.Join(e.Paths.UserProfiles, "<name>.yml")))
	case ref != "" && spec.Kind != source.KindGit:
		return source.Spec{}, errs.WithHints(fmt.Errorf("--ref applies to git sources only, not to archive %s", untrusted.Text(src, untrusted.MaxName)),
			"drop --ref: an archive has no branches, tags or commits")
	}
	spec.Ref, spec.Path = ref, path
	if err := (source.Recorded{Source: spec.Location, Ref: ref, Path: path}).Check(); err != nil {
		return source.Spec{}, err
	}
	return spec, source.Validate(spec)
}

// fetchProfiles fetches the profiles/ directory of spec, under its --path, into run (Plan 6b
// §4.2): only that directory, so a symbolic link is refused in it and on the way to it, and
// nowhere else in the source (§19 S3). A --path the source does not have, and a source without
// profiles/, are refused saying which.
func fetchProfiles(ctx context.Context, spec source.Spec, run string) (source.Fetched, error) {
	fetch := spec
	fetch.Path = path.Join(filepath.ToSlash(spec.Path), profile.SourceDir)
	f, err := source.Fetch(ctx, fetch, run)
	missing, ok := errors.AsType[*source.MissingPathError](err)
	switch {
	case !ok || missing.Path != fetch.Path:
		return f, err
	case spec.Path != "" && missing.Exists != path.Clean(filepath.ToSlash(spec.Path)):
		return source.Fetched{}, errs.WithHints(fmt.Errorf("--path %s is not a directory in the source", untrusted.Text(spec.Path, untrusted.MaxName)),
			"--path is relative to the source's root: the top of the git repository, or the archive's root")
	}
	where := "at its root"
	if spec.Path != "" {
		where = "under --path " + untrusted.Text(spec.Path, untrusted.MaxName)
	}
	return source.Fetched{}, errs.WithHints(fmt.Errorf("the source has no profiles/ directory %s", where),
		"a profile source holds profiles/<name>.yml at its root, or under --path")
}

// remoteContent is how a remote profile's entries now differ from was, its entries before (nil
// for one being added), as a trust diff (Plan 6b §6.5): every added or changed entry with the whole
// content its pinned version brings, fetched into run (never cached) and rendered in templates,
// which every profile of one lola profile command shares (Plan 7 §6.2), every file, MCP server and
// hook listed as added whatever a scope's lock.json says (ruling R-6b3b-7): a planted lock.json
// must not shorten what you consent to. led, the scope's, only annotates it (FilesNote). Each
// entry is classified without looking at the disk and must name a remote source (D4, ruling
// R-6b3b-5). An entry without targets, which the scope's manifest gives them at activation, is
// planned for every assistant lola knows, as every diff's hooks are.
func (e *Engine) remoteContent(ctx context.Context, sc Scope, q string, led *ledger.Ledger, was, now []manifest.Entry, run string,
	probes *probeRun, templates *templateRun, display []string) ([]TrustChange, error) {
	view := func(es []manifest.Entry) trust.View { return trust.ViewOf(&manifest.Manifest{Modules: es}) }
	rep := trustReport(trust.Check(&trust.Record{View: view(was), Exists: true}, view(now)))
	for i := range rep.Changes {
		c := &rep.Changes[i]
		if c.Kind == trust.ChangeRemoved {
			continue
		}
		k := slices.IndexFunc(now, func(ent manifest.Entry) bool { return shown(ent.Name) == c.Module })
		se, err := e.remoteEntry(ctx, sc, q, now[k], run)
		if err != nil {
			return nil, err
		}
		se.from = ledger.FromProfile(q) // a refusal names the remote profile's layer, not the scope's manifest
		hooks, err := e.contentHooks(sc, se.v, se.ent, se.from, probes, templates, display)
		_, badVars := errors.AsType[*varsRefusal](err)
		if _, refused := errors.AsType[*TemplateError](err); refused || badVars {
			return nil, err // it names the module, its component and file itself
		}
		if err != nil {
			return nil, fmt.Errorf("%s: runtime hooks: %w", se.ent.Name, err)
		}
		if c.Files, err = fileChanges(se.v, nil); err != nil {
			return nil, err
		}
		c.MCPServers = markServers(se.v.Module.MCPServers, nil, false)
		c.Hooks = markHooks(hooks, nil, false, c.Files)
		switch in := led.Modules[se.ent.Name]; {
		case in != nil && in.Digest == se.v.Meta.Digest:
			c.FilesNote = "this version is installed in " + scopeName(sc)
		case in != nil:
			c.FilesNote = scopeName(sc) + " has another version installed"
		}
	}
	return rep.Changes, nil
}

// remoteEntry is ent, an entry of the remote profile q, resolved to the version it pins in run: its
// source parsed without looking at the disk, which only a URL or git's scp form allows, so no
// directory named like it can stand in for it (ruling R-6b3b-5), then checked as a sync checks a
// pinned entry (source.Validate, checkPinShape) and refused when it is not remote.
func (e *Engine) remoteEntry(ctx context.Context, sc Scope, q string, ent manifest.Entry, run string) (*syncEntry, error) {
	spec, ok, err := source.Parse(ent.Source, sourceRoot(sc))
	switch {
	case err != nil:
		return nil, fmt.Errorf("%s: %w", ent.Name, err)
	case !ok || source.IsLocal(spec.Kind, spec.Location):
		return nil, errs.WithHints(fmt.Errorf("%s: %s is on this machine; a remote profile names only git repositories and https archives",
			ent.Name, untrusted.Text(ent.Source, untrusted.MaxName)), "ask the profile's author to fix it; lola did not add it")
	}
	spec.Ref, spec.Path = ent.Ref, ent.Path
	if err := source.Validate(spec); err != nil {
		return nil, fmt.Errorf("%s: %w", ent.Name, err)
	}
	if err := checkPinShape(ent.Name, "the profile.yml of "+shown(q), spec.Kind, ent, "only the author of "+shown(q)+" can"); err != nil {
		return nil, err
	}
	se := &syncEntry{ent: ent, spec: spec, targets: ent.Targets}
	return se, e.resolveSync(ctx, sc, se, run)
}

// readProfiles takes the profiles lock shared, for a run that reads the remote profiles (sync,
// activation, render, install, uninstall, list, lola profile's listing and state, the install
// coaching and completion, and --add, --update and --remove until they write), so it reads the
// set once (Plan 6b §10, §19 S6). It lists the remote profiles under the lock: when
// a killed --add, --update or --remove left a directory behind (leftover), it takes the lock
// exclusively, finishes that work (sweepRemote) and starts again, so a profile that was being
// replaced is never read as gone (§19 R11). A dry run writes nothing: it does not sweep, and
// when no lola has made the lock file yet, none has written a remote profile, so it takes none.
func (e *Engine) readProfiles(dry bool) (func(), error) {
	return readSwept(e.Paths.ProfilesLock, e.Paths.RemoteProfiles, remoteName, dry)
}

// readSwept takes the lock at lockPath shared, for a run that reads dir, the remote profiles or
// the cached marketplaces, which writers replace by rename (writeDir). When a killed writer left a
// directory behind (leftover), it takes the lock exclusively, finishes that work (sweepDir, valid
// naming what may be put back) and starts again, so a directory that was being replaced is never
// read as gone. A dry run writes nothing: it does not sweep, and when no lola has made the lock
// file yet, none has written dir, so it takes none.
func readSwept(lockPath, dir string, valid func(string) error, dry bool) (func(), error) {
	if _, err := os.Stat(lockPath); dry && errors.Is(err, fs.ErrNotExist) {
		return func() {}, nil
	}
	for {
		unlock, err := lockAt(lockPath, false)
		if err != nil {
			return nil, err
		}
		des, err := os.ReadDir(dir)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			unlock()
			return nil, err
		}
		if dry || !slices.ContainsFunc(des, func(d os.DirEntry) bool { return leftover(d.Name()) }) {
			return unlock, nil
		}
		unlock()
		if unlock, err = lockAt(lockPath, true); err != nil {
			return nil, err
		}
		err = sweepDir(dir, valid)
		unlock()
		if err != nil {
			return nil, err
		}
	}
}

// lockProfiles takes the profiles lock (Plan 6b §10, §19 S6), first of every lock: shared by the
// runs that read the remote profiles (readProfiles), and exclusive for the ones that write them,
// only while they check again and write (--add, --update, --remove), so no question is ever asked
// under it.
func (e *Engine) lockProfiles(exclusive bool) (func(), error) {
	return lockAt(e.Paths.ProfilesLock, exclusive)
}

// lockAt takes the file lock at path, shared or exclusive.
func lockAt(path string, exclusive bool) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	fl := flock.New(path)
	lock := fl.RLock
	if exclusive {
		lock = fl.Lock
	}
	if err := lock(); err != nil {
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}
	return func() { _ = fl.Unlock() }, nil
}

// The prefixes of the directories writeDir, sweepDir and the removals work in, under the
// directory that holds one directory per remote profile or marketplace: a name that starts with
// "." is never one of those (profile.Remote, Engine.Markets).
const (
	newPrefix = ".new-" // a directory being written
	oldPrefix = ".old-" // a directory being replaced, put back if nothing took its place
	delPrefix = ".del-" // a directory being removed, never put back
)

// leftover reports whether name is a directory a killed writer left (one of the prefixes above),
// which sweepDir finishes; any other name starting with "." is not lola's, and is left alone.
func leftover(name string) bool {
	return strings.HasPrefix(name, newPrefix) || strings.HasPrefix(name, oldPrefix) || strings.HasPrefix(name, delPrefix)
}

// dirFile is a file writeDir writes.
type dirFile struct {
	name string
	data []byte
}

// writeDir writes files into parent/name, crash-safe (Plan 6b §19 R11, Plan 6c §5): they go into a
// new directory under parent, which is renamed into place; a directory of that name is renamed
// aside first and removed after, and put back if the rename fails. A crash leaves the old
// directory or the new one, never a mix: between the two renames the old one waits aside, and
// sweepDir puts it back. The caller holds the lock of parent exclusively. written reports that the
// new directory is in place, even when an error follows (flushing parent, removing the old one,
// which the next sweep removes).
func writeDir(parent, name string, files []dirFile) (written bool, err error) {
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return false, err
	}
	tmp, err := os.MkdirTemp(parent, newPrefix+name+"-*")
	if err != nil {
		return false, err
	}
	defer func() {
		if err != nil && !written {
			err = errors.Join(err, os.RemoveAll(tmp))
		}
	}()
	for _, f := range files {
		if err := writeSynced(filepath.Join(tmp, f.name), f.data); err != nil {
			return false, err
		}
	}
	if err := fsx.SyncDir(tmp); err != nil {
		return false, err
	}
	dst := filepath.Join(parent, name)
	old := ""
	if _, err := os.Lstat(dst); err == nil {
		if old, err = os.MkdirTemp(parent, oldPrefix+name+"-*"); err != nil {
			return false, err
		}
		if err := os.Remove(old); err != nil {
			return false, err
		}
		if err := os.Rename(dst, old); err != nil {
			return false, err
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		if old != "" {
			err = errors.Join(err, os.Rename(old, dst))
		}
		return false, err
	}
	if err := fsx.SyncDir(parent); err != nil {
		return true, err
	}
	if old != "" {
		return true, os.RemoveAll(old)
	}
	return true, nil
}

// writeRemote writes the remote profile name, the bytes data and its source.json o, crash-safe
// (Plan 6b §4.2, §19 R11, writeDir). The caller holds the profiles lock exclusively.
func (e *Engine) writeRemote(name string, data []byte, o profile.Origin) (written bool, err error) {
	origin, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return false, err
	}
	return writeDir(e.Paths.RemoteProfiles, name, []dirFile{{profile.RemoteFile, data}, {profile.OriginFile, append(origin, '\n')}})
}

// writeSynced writes data to the new file path, private to the user, and flushes it to disk.
func writeSynced(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}

// sweepRemote finishes what a killed --add, --update or --remove left under paths.RemoteProfiles
// (§19 R11, sweepDir). The caller holds the profiles lock exclusively.
func (e *Engine) sweepRemote() error { return sweepDir(e.Paths.RemoteProfiles, remoteName) }

// sweepDir finishes what a killed writer left under parent (Plan 6b §19 R11): a directory an
// update renamed aside goes back in place when none took its place and valid accepts its name,
// and is removed otherwise; a directory being written, or removed, is removed. The caller holds
// the lock of parent exclusively, so nothing else is writing there.
func sweepDir(parent string, valid func(string) error) error {
	des, err := os.ReadDir(parent)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errList []error
	for _, de := range des {
		name := de.Name()
		path := filepath.Join(parent, name)
		switch {
		case strings.HasPrefix(name, newPrefix), strings.HasPrefix(name, delPrefix):
			errList = append(errList, os.RemoveAll(path))
		case strings.HasPrefix(name, oldPrefix):
			kept := strings.TrimPrefix(name, oldPrefix)
			if k := strings.LastIndex(kept, "-"); k > 0 {
				kept = kept[:k]
			}
			dst := filepath.Join(parent, kept)
			if _, err := os.Lstat(dst); errors.Is(err, fs.ErrNotExist) && valid(kept) == nil {
				errList = append(errList, os.Rename(path, dst))
				continue
			}
			errList = append(errList, os.RemoveAll(path))
		}
	}
	if err := errors.Join(errList...); err != nil {
		return errs.WithHints(fmt.Errorf("could not finish what an interrupted lola left in %s: %w", shown(parent), untrusted.CleanErr(err)),
			"remove the directories there whose names start with . by hand")
	}
	return nil
}

// scopeName is how a remote profile command's output names a scope: "user scope", or "project"
// and its root, through untrusted.Text.
func scopeName(sc Scope) string {
	if sc.Kind == target.User {
		return "user scope"
	}
	return "project " + shown(sc.ProjectDir)
}

// knownScopes are the scopes lola profile --add and --update know of when no scope flag names one
// (Plan 6b §19 P20, P22), as the read-only commands do (§3.1, D2): the project found from the
// working directory, when there is one, then user scope. They never ask: the profile file is
// shared by every scope. A .lola/ found in the home directory is no project (ruling R-6b3c-9):
// user scope installs there, and a sync refuses it; the notice says it was skipped.
func (e *Engine) knownScopes() ([]Scope, []Notice) {
	user := Scope{Kind: target.User}
	sc, ok := e.foundProject()
	switch {
	case !ok:
		return []Scope{user}, nil
	case e.IsHome(sc):
		return []Scope{user}, []Notice{{Level: LevelInfo, Text: shown(sc.ProjectDir) +
			" has .lola/, but it is your home directory, where user scope installs; it is not taken as a project"}}
	}
	return []Scope{sc, user}, nil
}

// IsHome reports whether sc is a project at the home directory, which is user scope's root.
func (e *Engine) IsHome(sc Scope) bool {
	return sc.Kind == target.Project && sc.Key() == (Scope{Kind: target.Project, ProjectDir: e.Paths.Home}).Key()
}

// ProfileRun is the lola profile command a hint names to repeat a run of --add or --update with
// args (Plan 6b §12.3): with the scope flags that name given, the scopes the flags named (one, or
// for --update --scope both the project and user scope, ruling R-6b3c-2), or with none when given
// is empty, since such a run picks its scopes itself and asks for none (§19 P20, P22). Each
// argument goes through untrusted.Text and is quoted for the user's shell.
func (e *Engine) ProfileRun(given []Scope, args ...string) string {
	cmd := []string{"lola", "profile"}
	switch len(given) {
	case 0:
	case 1:
		return e.Command(given[0], "profile", args...)
	default:
		cmd = append(cmd, "--scope", "both")
		if !e.FoundHere(given[0]) {
			cmd = append(cmd, "-C", untrusted.Text(given[0].ProjectDir, untrusted.MaxName))
		}
	}
	for _, a := range args {
		cmd = append(cmd, untrusted.Text(a, untrusted.MaxName))
	}
	return errs.ShellJoin(cmd)
}
