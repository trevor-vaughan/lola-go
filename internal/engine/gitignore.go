package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// settingsExposed says why git could commit dir's file, the project-relative settings file a
// target writes runtime hooks to, for target.PlanContext.SettingsExposed:
// it is tracked, it is not ignored, or lola could not tell. It is "" when
// dir is in no git work tree, or git ignores the file and does not track it. Both checks run
// through source.GitCommand's hardening, since a cloned project's config is untrusted, and fail
// closed: any outcome but the two that mean "safe" exposes the file.
func settingsExposed(ctx context.Context, dir, file string) string {
	cantCheck := func(why string) string {
		return fmt.Sprintf("lola could not check that git ignores %s (%s)", file, why)
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
	switch code, failed := status("ls-files", "--error-unmatch", "--", file); {
	case failed != "":
		return failed
	case code == 0:
		return file + " is tracked by git, so hooks there would reach everyone who clones the repository; untrack it with git rm --cached"
	case code != 1:
		return cantCheck(fmt.Sprintf("git ls-files exited with status %d", code))
	}
	switch code, failed := status("check-ignore", "-q", "--no-index", "--", file); {
	case failed != "":
		return failed
	case code == 1:
		return file + " is not ignored by git, so hooks there could be committed for everyone who clones the repository"
	case code != 0:
		return cantCheck(fmt.Sprintf("git check-ignore exited with status %d", code))
	}
	return ""
}

// projectLedger is a project's ledger relative to its root, as git pathspecs name it.
const projectLedger = ".lola/lock.json"

// gitTracked reports whether git tracks rel, a slash-separated path relative to root, in the
// project at root: false outside a git work tree. The check runs through source.GitCommand's
// hardening; git missing from PATH is an error wrapping exec.ErrNotFound, and any other failure is
// an error too, so a caller refusing a tracked file fails closed. A cloned lock.json
// (projectLedger) could claim committed files, which a sync would then remove.
func gitTracked(ctx context.Context, root, rel string) (bool, error) {
	inTree, err := source.InWorkTree(root)
	if err != nil || !inTree {
		return false, err
	}
	out, err := source.GitCommand(ctx, root, "ls-files", "--error-unmatch", "--", rel).CombinedOutput()
	if exit, ok := errors.AsType[*exec.ExitError](err); ok && exit.ExitCode() == 1 {
		return false, nil
	}
	if err != nil {
		if len(out) > 0 {
			err = fmt.Errorf("%w: %s", err, untrusted.Text(strings.TrimSpace(string(out)), untrusted.MaxName))
		}
		name := untrusted.Text(rel, untrusted.MaxName)
		return false, errs.WithHints(fmt.Errorf("lola could not check whether git tracks %s: %w", name, err),
			"lola asks git whether it tracks "+name+": make git status work in this repository, then run lola again")
	}
	return true, nil
}

// refuseTrackedLedger refuses a project scope whose ledger git tracks. Git missing from PATH
// cannot have checked one out, so it only warns.
func (e *Engine) refuseTrackedLedger(ctx context.Context, sc Scope, res *Result) error {
	if sc.Kind != target.Project {
		return nil
	}
	tracked, err := gitTracked(ctx, sc.ProjectDir, projectLedger)
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

// untrackCommand is the git command, written for the working directory, that untracks operands,
// paths relative to sc's project root (after a "--" when the caller adds one): with -C <root>
// unless that is the working directory. ok is false when a root untrusted.Text would change makes
// the command unprintable as it runs.
func (e *Engine) untrackCommand(sc Scope, operands ...string) (cmd string, ok bool) {
	untrack := append([]string{"git", "rm", "--cached"}, operands...)
	if cwd, err := e.Getwd(); err != nil || canonicalRoot(cwd) != canonicalRoot(sc.ProjectDir) {
		if untrusted.Text(sc.ProjectDir, untrusted.MaxName) != sc.ProjectDir {
			return "", false
		}
		untrack = append([]string{"git", "-C", sc.ProjectDir, "rm", "--cached"}, operands...)
	}
	return errs.ShellJoin(untrack), true
}

// trackedLedger refuses the project scope sc, whose ledger git tracks, hinting the git command
// that untracks it (untrackCommand).
func (e *Engine) trackedLedger(sc Scope) error {
	refusal := fmt.Errorf("%s is tracked by git; lola keeps it private to your checkout, since a committed lock.json lets a "+
		"pull decide what lola deletes; if you never installed with lola in this checkout, untrack and delete it, then run %s",
		projectLedger, e.SyncCommand(sc))
	cmd, ok := e.untrackCommand(sc, projectLedger)
	if !ok {
		return refusal
	}
	return errs.WithHints(refusal, cmd)
}

// machinePaths reports whether data, a rendered file, holds a path of this machine: root, the
// project's absolute path, or home, the user's, at a path boundary (the byte after it is "/", a
// quote, "\" or the end), so /home/u/proj2 is not /home/u/proj. An empty root or home, and a home
// of "/", match nothing.
func machinePaths(data []byte, root, home string) bool {
	for _, p := range []string{root, home} {
		if p == "" || p == "/" {
			continue
		}
		rest := data
		for {
			i := bytes.Index(rest, []byte(p))
			if i < 0 {
				break
			}
			rest = rest[i+len(p):]
			if len(rest) == 0 || strings.IndexByte(`/"\`, rest[0]) >= 0 {
				return true
			}
		}
	}
	return false
}

// machinePathNotices warns, for each JSON file the plan writes in project sc that git tracks and
// whose rendered bytes hold a path of this machine, that a teammate's sync will refuse it. The
// members the run wrote there are named. A file outside the project, or a project outside a git
// work tree, adds none; git missing from PATH cannot track a file, so adds none either.
func (e *Engine) machinePathNotices(ctx context.Context, sc Scope, p *plan.Plan) []Notice {
	if sc.Kind != target.Project {
		return nil
	}
	root := canonicalRoot(sc.ProjectDir)
	var out []Notice
	for _, f := range p.Files {
		members := map[string][]string{} // module -> member names written to f
		for _, s := range p.Steps {
			if s.Target == f.Target && s.Path == f.Logical && s.Key != "" && s.Action != plan.ActionDelete && s.Action != plan.ActionForget && s.Action != plan.ActionKeep {
				members[s.Module] = append(members[s.Module], untrusted.Text(s.Name, untrusted.MaxName))
			}
		}
		if f.Delete || f.Outside || len(members) == 0 || (!machinePaths(f.Data, root, e.Paths.Home) && !machinePaths(f.Data, sc.ProjectDir, "")) {
			continue
		}
		rel, err := filepath.Rel(root, f.Abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		rel = filepath.ToSlash(rel)
		tracked, err := gitTracked(ctx, root, rel)
		if errors.Is(err, exec.ErrNotFound) || (err == nil && !tracked) {
			continue
		}
		name := untrusted.Text(rel, untrusted.MaxName)
		if err != nil {
			out = append(out, Notice{Level: LevelWarn, Text: untrusted.CleanErr(err).Error()})
			continue
		}
		for _, module := range slices.Sorted(maps.Keys(members)) {
			n := Notice{Level: LevelWarn, Module: module, Text: fmt.Sprintf("%s is tracked by git and now holds paths of this machine (%s): "+
				"a teammate's sync will refuse it as not written by lola; untrack it, then add it to .gitignore", name, strings.Join(members[module], ", "))}
			if cmd, ok := e.untrackCommand(sc, "--", rel); ok {
				n.Hints = []string{cmd}
			}
			out = append(out, n)
		}
	}
	return out
}
