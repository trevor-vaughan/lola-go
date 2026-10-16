package source

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// CheckReachable refuses commit unless it is reachable from spec.Ref ("" = the default
// branch) on the git source spec.Location, so a pin cannot name a commit from a branch nobody
// reviewed (Plan 6a §6.4). It runs in a new repository under stagingParent, apart from any
// content checkout: the ref is resolved as a fetch resolves it, its history is fetched without
// trees or blobs (a server without filter support sends everything, which is slower but the
// same answer), and git merge-base decides. GIT_NO_LAZY_FETCH stops git fetching a missing
// object behind lola's back. The check is skipped when the ref is the commit itself.
func CheckReachable(ctx context.Context, spec Spec, commit, stagingParent string) error {
	if err := validate(spec); err != nil {
		return err
	}
	if spec.Ref == commit {
		return nil
	}
	if !fullSHARE.MatchString(commit) {
		return fmt.Errorf("commit %q is not a full commit SHA", untrusted.Text(commit, untrusted.MaxName))
	}
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	name, err := remoteRef(ctx, spec.Location, spec.Ref)
	if err != nil {
		return err
	}
	if name == "" {
		name = "HEAD"
	}
	_, err = inScratch(stagingParent, func(repo string) (Fetched, error) {
		return Fetched{}, reachable(ctx, spec, commit, name, repo)
	})
	return err
}

// reachable is CheckReachable's work in the empty repository directory repo.
func reachable(ctx context.Context, spec Spec, commit, name, repo string) error {
	format := "sha1"
	if len(commit) == 64 {
		format = "sha256"
	}
	if _, err := runGit(ctx, repo, "init", "-q", "--object-format="+format); err != nil {
		return err
	}
	noLazy := []string{"GIT_NO_LAZY_FETCH=1"}
	if _, err := runGitEnv(ctx, noLazy, repo, "fetch", "-q", "--filter=tree:0", "--", spec.Location, name); err != nil {
		return err
	}
	where := "the default branch"
	if spec.Ref != "" {
		where = untrusted.Text(spec.Ref, untrusted.MaxName)
	}
	notOn := fmt.Errorf("commit %s is not on %s at %s", commit, where, untrusted.Text(spec.Location, untrusted.MaxName))
	if _, err := runGitEnv(ctx, noLazy, repo, "cat-file", "-e", commit+"^{commit}"); err != nil {
		if _, isExit := errors.AsType[*exec.ExitError](err); isExit {
			return notOn
		}
		return err
	}
	_, err := runGitEnv(ctx, noLazy, repo, "merge-base", "--is-ancestor", commit, "FETCH_HEAD^{commit}")
	if ee, isExit := errors.AsType[*exec.ExitError](err); isExit && ee.ExitCode() == 1 {
		return notOn
	}
	return err
}
