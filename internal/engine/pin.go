package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// fetchPin fetches the version a pin names into run and prepares it there (store.Prepare), and
// returns it staged, not cached (Plan 6a §6.4): the caller commits it (store.Commit with
// headIfAbsent, so the pinned fetch never moves a head) once the content may enter the cache.
// spec is the entry's source with its ref. For git the commit must be reachable from that ref,
// it is fetched by its SHA, and the fetch must resolve to it; for every kind the content's digest
// must be digest. A tree that fails a check is removed with the run.
func (e *Engine) fetchPin(ctx context.Context, name string, spec source.Spec, commit, digest, run string) (store.Staged, error) {
	installAgain := "install what its source serves now: " + installCommand(spec.Location, spec.Ref, spec.Path)
	fetch := spec
	if spec.Kind == source.KindGit {
		if commit == "" {
			return store.Staged{}, errs.WithHints(fmt.Errorf("%s: no commit is recorded for the version installed, so lola cannot fetch it", name), installAgain)
		}
		if err := source.CheckReachable(ctx, spec, commit, run); err != nil {
			return store.Staged{}, fmt.Errorf("%s: %w", name, err)
		}
		fetch.Ref = commit
	}
	f, err := source.Fetch(ctx, fetch, run)
	if err != nil {
		if spec.Kind == source.KindGit {
			err = &commitFetchError{err}
		}
		return store.Staged{}, err
	}
	if f.Commit != commit {
		return store.Staged{}, errs.WithHints(&servedCommitError{name: name, location: spec.Location, got: f.Commit, want: commit}, installAgain)
	}
	st, err := e.Store.Prepare(store.PutRequest{Staged: f.Dir, FallbackName: f.Name, Name: name, Source: spec.Location, Type: string(spec.Kind),
		Path: spec.Path, Ref: spec.Ref, Commit: commit, Executables: f.Executables, Now: e.Now()})
	if err != nil {
		return store.Staged{}, err // a module that renamed itself: fetching again cannot help, and the caller names the way out
	}
	if got := st.Version.Meta.Digest; got != digest {
		return store.Staged{}, errs.WithHints(&digestMismatchError{name: name, got: got, want: digest}, installAgain)
	}
	return st, nil
}

// commitFetchError is fetchPin's failure to fetch a git commit by its SHA, which a server can
// refuse; its message is the fetch's.
type commitFetchError struct{ err error }

func (e *commitFetchError) Error() string { return e.err.Error() }
func (e *commitFetchError) Unwrap() error { return e.err }

// servedCommitError is fetchPin's refusal of a fetch by commit that resolved to another commit.
type servedCommitError struct{ name, location, got, want string }

func (e *servedCommitError) Error() string {
	return fmt.Sprintf("%s: %s served commit %s for %s", e.name, untrusted.Text(e.location, untrusted.MaxName),
		untrusted.Text(e.got, untrusted.MaxName), e.want)
}

// digestMismatchError is fetchPin's refusal of content whose digest is not the pin's.
type digestMismatchError struct{ name, got, want string }

func (e *digestMismatchError) Error() string {
	return fmt.Sprintf("%s: the content fetched has digest %s, not the pinned %s", e.name, e.got, untrusted.Text(e.want, untrusted.MaxName))
}

// installedForRender is the version render installs for a ledger module: the folder as it is
// now for a folder source (the cached version installed when the folder is gone), else the
// version lock.json records, from a snapshot of the cache or fetched by its pin. Its Commit is
// the one lock.json records.
func (e *Engine) installedForRender(ctx context.Context, sc Scope, name string, entry *ledger.Entry, run string, res *Result) (store.Version, error) {
	// One module that no longer loads (one installed before a new refusal, say) stops every
	// module's render, so name the way out.
	uninstall := "if you no longer want " + name + ", remove it: " + errs.ShellJoin([]string{"lola", "uninstall", name})
	spec, err := e.recordedSpec(sc, name, entry)
	if err != nil {
		return store.Version{}, errs.WithHints(err, uninstall)
	}
	if spec.Kind == source.KindFolder {
		if _, err := os.Stat(spec.Location); err == nil {
			v, err := e.fetchAs(ctx, spec, name, run)
			return v, errs.WithHints(err, uninstall)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return store.Version{}, fmt.Errorf("%s: %w", name, untrusted.CleanErr(err))
		}
		res.Notices = append(res.Notices, Notice{Level: LevelWarn, Text: fmt.Sprintf("%s: its source folder %s is gone; rendered the version installed",
			name, untrusted.Text(spec.Location, untrusted.MaxName))})
	}
	v, ok, err := e.Store.Pinned(name, entry.Digest, run)
	if err != nil {
		return store.Version{}, errs.WithHints(err, uninstall)
	}
	if !ok {
		if spec.Kind == source.KindFolder {
			return store.Version{}, errs.WithHints(fmt.Errorf("%s is no longer in your cache, and its source folder is gone", name),
				"remove it: "+errs.ShellJoin([]string{"lola", "uninstall", name}))
		}
		st, err := e.fetchPin(ctx, name, spec, entry.Commit, entry.Digest, run)
		if err == nil {
			_, err = e.Store.Commit(st, true)
		}
		if err != nil {
			return store.Version{}, errs.WithHints(err, uninstall)
		}
		v = st.Version
		v.Module.Warnings = st.LoadWarnings // what a cache hit would report, not what detection did
	}
	v.Commit = entry.Commit
	return v, nil
}

// installCommand is the lola install command line for a source, its ref and path, each passed
// through untrusted.Text because lock.json and the cache record author-chosen values. A location
// starting with '-' is written as a relative path, which lola does not read as an option.
func installCommand(location, ref, path string) string {
	location = untrusted.Text(location, untrusted.MaxName)
	if strings.HasPrefix(location, "-") {
		location = "./" + location
	}
	args := []string{"lola", "install", location}
	if ref != "" {
		args = append(args, "--ref", untrusted.Text(ref, untrusted.MaxName))
	}
	if path != "" {
		args = append(args, "--path", untrusted.Text(path, untrusted.MaxName))
	}
	return errs.ShellJoin(args)
}

// recordedSpec is the source a ledger module was installed from: the cache's record of it,
// else lock.json's, with lock.json's ref. A recorded source that is no longer a source is
// coached with the install command that fetches the same module.
func (e *Engine) recordedSpec(sc Scope, name string, entry *ledger.Entry) (source.Spec, error) {
	o, found, err := e.Store.Origin(name)
	if err != nil {
		return source.Spec{}, err
	}
	if found {
		return source.Spec{Kind: source.Kind(o.Type), Location: o.Source, Path: o.Path, Ref: entry.Ref}, nil
	}
	cwd := canonicalRoot(sc.ProjectDir)
	if sc.Kind == target.User {
		cwd = "/"
	}
	spec, isSrc, err := source.Parse(entry.Source, cwd)
	if err != nil {
		return source.Spec{}, fmt.Errorf("%s is no longer in your cache and its recorded source %q is unusable: %w", name, entry.Source, err)
	}
	if !isSrc {
		return source.Spec{}, errs.WithHints(fmt.Errorf("%s is no longer in your cache", name),
			"fetch it again: "+installCommand(entry.Source, entry.Ref, entry.Path))
	}
	spec.Ref, spec.Path = entry.Ref, entry.Path
	return spec, nil
}
