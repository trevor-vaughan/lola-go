package engine

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
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

// projectLedger is a project's ledger relative to its root, as git pathspecs name it.
const projectLedger = ".lola/lock.json"

// ledgerTracked reports whether git tracks the project's ledger (Plan 6a §9.3 step 1): a
// cloned lock.json could claim committed files, which a render would then remove. It is false
// outside a git work tree. The check runs through source.GitCommand's hardening; git missing
// from PATH is an error wrapping exec.ErrNotFound, and any other failure is an error too, so a
// caller refusing a tracked ledger fails closed.
func ledgerTracked(ctx context.Context, root string) (bool, error) {
	inTree, err := source.InWorkTree(root)
	if err != nil || !inTree {
		return false, err
	}
	out, err := source.GitCommand(ctx, root, "ls-files", "--error-unmatch", "--", projectLedger).CombinedOutput()
	if exit, ok := errors.AsType[*exec.ExitError](err); ok && exit.ExitCode() == 1 {
		return false, nil
	}
	if err != nil {
		if len(out) > 0 {
			err = fmt.Errorf("%w: %s", err, untrusted.Text(strings.TrimSpace(string(out)), untrusted.MaxName))
		}
		return false, errs.WithHints(fmt.Errorf("lola could not check whether git tracks %s: %w", projectLedger, err),
			"lola asks git whether it tracks "+projectLedger+": make git status work in this repository, then run lola again")
	}
	return true, nil
}

// refuseTrackedLedger refuses a project scope whose ledger git tracks. Git missing from PATH
// cannot have checked one out, so it only warns.
func (e *Engine) refuseTrackedLedger(ctx context.Context, sc Scope, res *Result) error {
	if sc.Kind != target.Project {
		return nil
	}
	tracked, err := ledgerTracked(ctx, sc.ProjectDir)
	if errors.Is(err, exec.ErrNotFound) {
		res.Notices = append(res.Notices, Notice{Level: LevelWarn, Text: "git is not on PATH, so lola could not check that git does not track " + projectLedger})
		return nil
	}
	if err != nil {
		return err
	}
	if tracked {
		return e.trackedLedger(sc)
	}
	return nil
}

// trackedLedger refuses the project scope sc, whose ledger git tracks, hinting the git command
// that untracks it from the working directory: with -C <root> unless that is the project root.
// A root untrusted.Text would change gets no hint: the command could not be printed as it runs.
func (e *Engine) trackedLedger(sc Scope) error {
	refusal := fmt.Errorf("%s is tracked by git; lola keeps it private to your checkout, since a committed lock.json lets a "+
		"pull decide what lola deletes; if you never installed with lola in this checkout, untrack and delete it, then run %s",
		projectLedger, e.SyncCommand(sc))
	untrack := []string{"git", "rm", "--cached", projectLedger}
	if cwd, err := e.Getwd(); err != nil || canonicalRoot(cwd) != canonicalRoot(sc.ProjectDir) {
		if untrusted.Text(sc.ProjectDir, untrusted.MaxName) != sc.ProjectDir {
			return refusal
		}
		untrack = []string{"git", "-C", sc.ProjectDir, "rm", "--cached", projectLedger}
	}
	return errs.WithHints(refusal, errs.ShellJoin(untrack))
}
