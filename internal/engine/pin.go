package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// fetchPin fetches the version a pin names into run and prepares it there (store.Prepare), and
// returns it staged, not cached: the caller commits it (store.Commit with
// headIfAbsent, so the pinned fetch never moves a head) once the content may enter the cache.
// spec is the entry's source with its ref. For git the commit must be reachable from that ref,
// it is fetched by its SHA, and the fetch must resolve to it; for every kind the content's digest
// must be digest. A tree that fails a check is removed with the run.
func (e *Engine) fetchPin(ctx context.Context, name string, spec source.Spec, commit, digest, run string) (store.Staged, error) {
	// An install by source would take the whole directory, never a selection of it.
	var installAgain []string
	if spec.Components == nil {
		installAgain = []string{"install what its source serves now: " + installCommand(spec.Location, spec.Ref, spec.Path)}
	}
	fetch := spec
	if spec.Kind == source.KindGit {
		if commit == "" {
			return store.Staged{}, errs.WithHints(fmt.Errorf("%s: no commit is recorded for the version installed, so lola cannot fetch it", name), installAgain...)
		}
		if err := source.CheckReachable(ctx, spec, commit, run); err != nil {
			return store.Staged{}, fmt.Errorf("%s: %w", name, err)
		}
		fetch.Ref = commit
	}
	f, err := source.Fetch(ctx, fetch, run)
	if err != nil {
		if spec.Kind == source.KindGit {
			err = &commitFetchError{name: name, err: err}
		}
		return store.Staged{}, err
	}
	if f.Commit != commit {
		return store.Staged{}, errs.WithHints(&servedCommitError{name: name, location: spec.Location, got: f.Commit, want: commit}, installAgain...)
	}
	st, err := e.Store.Prepare(store.PutRequest{Staged: f.Dir, FallbackName: name, Name: name, Source: spec.Location, Type: string(spec.Kind),
		Path: spec.Path, Ref: spec.Ref, Commit: commit, Executables: f.Executables, Now: e.Now(), Components: spec.Components})
	if err != nil {
		return store.Staged{}, err // a module that renamed itself: fetching again cannot help, and the caller names the way out
	}
	if got := st.Version.Meta.Digest; got != digest {
		return store.Staged{}, errs.WithHints(&digestMismatchError{name: name, got: got, want: digest}, installAgain...)
	}
	return st, nil
}

// commitFetchError is fetchPin's failure to fetch a git commit by its SHA, which a server can
// refuse; its message is the fetch's, naming the module once, as every fetchPin error does.
type commitFetchError struct {
	name string
	err  error
}

func (e *commitFetchError) Error() string { return e.name + ": " + e.err.Error() }
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

// sourceRoot is the directory a scope's recorded relative sources are read against: the
// project's canonical root, or / at user scope, whose sources are recorded absolute.
func sourceRoot(sc Scope) string {
	if sc.Kind == target.User {
		return "/"
	}
	return canonicalRoot(sc.ProjectDir)
}

// installCommand is the lola install command line for a source, its ref and path, each passed
// through untrusted.Text because lock.json and the cache record author-chosen values.
func installCommand(location, ref, path string) string {
	args := []string{"lola", "install"}
	for _, a := range installArgs(location, ref, path) {
		args = append(args, untrusted.Text(a, untrusted.MaxName))
	}
	return errs.ShellJoin(args)
}

// installArgs is lola install's arguments for a source, its ref and path, raw. A location
// starting with '-' is written as a relative path, which lola does not read as an option.
func installArgs(location, ref, path string) []string {
	if strings.HasPrefix(location, "-") {
		location = "./" + location
	}
	args := []string{location}
	if ref != "" {
		args = append(args, "--ref", ref)
	}
	if path != "" {
		args = append(args, "--path", path)
	}
	return args
}
