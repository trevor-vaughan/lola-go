package engine

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	"github.com/trevor-vaughan/lola-go/internal/source"
)

// localSettings is where runtime hooks go at project scope (Plan 5b design §1, §7.2): the
// per-user settings file, which must stay out of the repository.
const localSettings = ".claude/settings.local.json"

// localSettingsExposed says why git could commit dir's .claude/settings.local.json, for
// target.PlanContext: it is tracked, it is not ignored, or lola could not tell. It is "" when
// dir is in no git work tree, or git ignores the file and does not track it. Both checks run
// through source.GitCommand's hardening, since a cloned project's config is untrusted, and fail
// closed: any outcome but the two that mean "safe" exposes the file.
func localSettingsExposed(ctx context.Context, dir string) string {
	cantCheck := func(why string) string {
		return fmt.Sprintf("lola could not check that git ignores %s (%s)", localSettings, why)
	}
	inTree, err := source.InWorkTree(dir)
	switch {
	case err != nil:
		return cantCheck(err.Error())
	case !inTree:
		return ""
	}
	status := func(args ...string) (int, string) {
		err := source.GitCommand(ctx, dir, args...).Run()
		if exit, ok := errors.AsType[*exec.ExitError](err); ok {
			return exit.ExitCode(), ""
		}
		if errors.Is(err, exec.ErrNotFound) {
			return 0, cantCheck("git is not on PATH")
		}
		if err != nil {
			return 0, cantCheck(err.Error())
		}
		return 0, ""
	}
	switch code, failed := status("ls-files", "--error-unmatch", "--", localSettings); {
	case failed != "":
		return failed
	case code == 0:
		return localSettings + " is tracked by git, so hooks there would reach everyone who clones the repository; untrack it with git rm --cached"
	case code != 1:
		return cantCheck(fmt.Sprintf("git ls-files exited with status %d", code))
	}
	switch code, failed := status("check-ignore", "-q", "--no-index", "--", localSettings); {
	case failed != "":
		return failed
	case code == 1:
		return localSettings + " is not ignored by git, so hooks there could be committed for everyone who clones the repository"
	case code != 0:
		return cantCheck(fmt.Sprintf("git check-ignore exited with status %d", code))
	}
	return ""
}
