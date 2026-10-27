package engine

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// gitAnswer is what git said about the paths asked of one work tree: the tracked paths, or why
// it could not say.
type gitAnswer struct {
	paths []string
	cause string
}

// tracked reports whether rel, or anything below it, is among the paths git listed.
func (a gitAnswer) tracked(rel string) bool {
	rel = filepath.ToSlash(rel)
	return slices.ContainsFunc(a.paths, func(p string) bool { return p == rel || strings.HasPrefix(p, rel+"/") })
}

// gitLocks asks git, once per work tree, about the links on the way to dests and, unless
// units is false, about the units and MCP files in them (§4.4, R-6d-N1): a link git tracks skips
// its destination, and a tracked unit is locked; where git cannot answer, both are.
func (s *tidyScanner) gitLocks(dests []*tidyDest, includeTracked bool) {
	type ask struct {
		wt, rel string
	}
	where := map[string]ask{}
	causes := map[string]string{} // why lola cannot tell whether a path is in a work tree (R-6d-S5)
	queries := map[string][]string{}
	query := func(p string) {
		if _, ok := where[p]; ok {
			return
		}
		wt, err := source.WorkTree(filepath.Dir(p))
		if err != nil {
			where[p] = ask{}
			causes[p] = "lola cannot tell whether it is in a git work tree: " + untrusted.Text(untrusted.CleanErr(err).Error(), untrusted.MaxName)
			return
		}
		if wt == "" {
			where[p] = ask{}
			return
		}
		rel, err := filepath.Rel(wt, p)
		if err != nil {
			where[p] = ask{}
			return
		}
		where[p] = ask{wt, rel}
		queries[wt] = append(queries[wt], rel)
	}
	for _, d := range dests {
		if d.code != "" {
			continue
		}
		for _, l := range d.links {
			query(l)
		}
		if includeTracked {
			continue
		}
		if d.mcp() && len(d.items) > 0 {
			query(d.abs)
		}
		for _, it := range d.items {
			if !d.mcp() && it.Code == "" {
				query(it.ID.Path)
			}
		}
	}
	answers := map[string]gitAnswer{}
	wts := make([]string, 0, len(queries))
	for wt := range queries {
		wts = append(wts, wt)
	}
	slices.Sort(wts)
	for _, wt := range wts {
		a := s.lsFiles(wt, queries[wt])
		answers[wt] = a
		if a.cause != "" {
			s.notices = append(s.notices, Notice{Level: LevelWarn, Text: "git could not say what it tracks in " + untrusted.Text(wt, untrusted.MaxName) + ": " + a.cause})
		}
	}
	answer := func(p string) (bool, string) {
		if c, ok := causes[p]; ok {
			return false, c
		}
		q := where[p]
		if q.wt == "" {
			return false, ""
		}
		a := answers[q.wt]
		return a.cause == "" && a.tracked(q.rel), a.cause
	}
	for _, d := range dests {
		if d.code != "" {
			continue
		}
		for _, l := range d.links {
			if tracked, cause := answer(l); cause != "" {
				s.skip(d, codeTrackedLink, "git could not say whether its path passes through a symbolic link git tracks", "is reached through a symbolic link git could not say it tracks")
			} else if tracked {
				s.skip(d, codeTrackedLink, "its path passes through a symbolic link git tracks", "is reached through a symbolic link git tracks")
			}
		}
		if d.code != "" || includeTracked {
			continue
		}
		fileTracked, fileCause := false, ""
		if d.mcp() {
			fileTracked, fileCause = answer(d.abs)
		}
		for _, it := range d.items {
			if it.Code != "" {
				continue
			}
			tracked, cause := fileTracked, fileCause
			if !d.mcp() {
				tracked, cause = answer(it.ID.Path)
			}
			switch {
			case cause != "":
				it.gitCause = cause
				it.lock(codeTracked, whyUntold)
			case tracked:
				it.lock(codeTracked, whyTracked)
			}
		}
	}
}

// lsFiles asks git which of rels, paths relative to the work tree wt, it tracks: one
// ls-files with literal pathspecs, so a name like :(glob)* is a name (R-6d-N7).
func (s *tidyScanner) lsFiles(wt string, rels []string) gitAnswer {
	out, err := source.GitCommand(s.ctx, wt, append([]string{"--literal-pathspecs", "ls-files", "-z", "--"}, rels...)...).Output()
	if err != nil {
		return gitAnswer{cause: gitCause(err)}
	}
	if len(out) == 0 {
		return gitAnswer{}
	}
	if out[len(out)-1] != 0 {
		return gitAnswer{cause: "git printed what lola cannot read"}
	}
	var paths []string
	for _, p := range bytes.Split(out[:len(out)-1], []byte{0}) {
		if !filepath.IsLocal(filepath.FromSlash(string(p))) {
			return gitAnswer{cause: "git printed what lola cannot read"}
		}
		paths = append(paths, string(p))
	}
	return gitAnswer{paths: paths}
}

// gitCause says why a git command failed, safe to print.
func gitCause(err error) string {
	if errors.Is(err, exec.ErrNotFound) {
		return "git is not on PATH"
	}
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		return fmt.Sprintf("git ls-files exited with status %d", exit.ExitCode())
	}
	for u := errors.Unwrap(err); u != nil; u = errors.Unwrap(u) {
		err = u
	}
	return untrusted.Text(untrusted.CleanErr(err).Error(), untrusted.MaxName)
}
