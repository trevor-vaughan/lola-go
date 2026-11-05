package source

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// maxLinkHops is how many links one chain may follow to reach a file; cycleWhy states it.
const maxLinkHops = 8

// Why lola refuses a link, and hints for its author: each says what to change in the source.
const (
	cycleWhy     = "it is a loop or a chain of more than 8 links"
	cycleHint    = "point the link straight at a file in the source"
	danglingWhy  = "its target does not exist"
	danglingHint = "point the link at a file in the source, or remove it"
	dirWhy       = "its target is a directory; lola follows links only to files"
	dirLinkHint  = "link each file instead of a directory, or replace the link with the directory itself"
	outsideHint  = "make the link relative with its target inside the source, or replace the link with a copy of the file"

	// modulePathLink is why a link at or above the module root is refused.
	modulePathLink     = "the module path goes through it; lola follows links only to files"
	modulePathLinkHint = "name the real directory with --path, not the link"
)

// resolveLink follows the link at linkRel, a slash path below root's directory (the source's
// boundary), to the regular file it names, and returns that file's slash path below root and its
// FileInfo. Every hop is read through root, which never leaves the boundary, and its parent
// directories must be real directories, so the path returned is the file's real path. An absolute
// link, a target outside the boundary, a dangling link, a loop or a chain of more than maxLinkHops
// links, a target that is a directory or no regular file, a parent directory that is a link, and
// a target lola never copies (linkIgnored, below base, the module root's slash path from the
// boundary) are all refused with ErrLinkInSource; a path on the chain lola cannot read is a plain
// error (unreadable).
func resolveLink(root *os.Root, base, linkRel string) (string, fs.FileInfo, error) {
	shown := untrusted.Text(linkRel, untrusted.MaxName)
	cur := linkRel
	for hop := 0; ; hop++ {
		if hop > 0 && linkIgnored(base, cur) {
			return "", nil, refuseIgnored(shown, cur)
		}
		if err := realParents(root, shown, cur); err != nil {
			return "", nil, err
		}
		fi, err := root.Lstat(filepath.FromSlash(cur))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return "", nil, refuse(shown, danglingWhy, danglingHint)
		case err != nil:
			return "", nil, unreadable(shown, err)
		case fi.Mode()&fs.ModeSymlink != 0:
		case fi.IsDir():
			return "", nil, refuse(shown, dirWhy, dirLinkHint)
		case !fi.Mode().IsRegular():
			return "", nil, refuse(shown, "its target is not a regular file", "point the link at a regular file")
		default:
			return cur, fi, nil
		}
		if hop >= maxLinkHops {
			return "", nil, refuse(shown, cycleWhy, cycleHint)
		}
		to, err := root.Readlink(filepath.FromSlash(cur))
		if err != nil {
			return "", nil, unreadable(shown, err)
		}
		next, err := linkTarget(shown, cur, to)
		if err != nil {
			return "", nil, err
		}
		cur = next
	}
}

// linkTarget is where the link at cur, a slash path from the boundary, whose target is to,
// points, as a slash path from the boundary. An absolute target, or one outside the boundary, is
// refused, naming the link shown and its target as written.
func linkTarget(shown, cur, to string) (string, error) {
	if path.IsAbs(filepath.ToSlash(to)) || filepath.IsAbs(to) || filepath.VolumeName(to) != "" {
		return "", refuse(shown, "it points to "+untrusted.Text(to, untrusted.MaxName)+", an absolute path", outsideHint)
	}
	next := path.Join(path.Dir(cur), filepath.ToSlash(to))
	if !filepath.IsLocal(filepath.FromSlash(next)) {
		return "", refuse(shown, "its target "+untrusted.Text(to, untrusted.MaxName)+" leaves the source", outsideHint)
	}
	return next, nil
}

// realParents refuses rel, a slash path below root, unless each of its parent directories is a
// real directory: a linked one is refused, and a missing one, or a file, makes the link dangle.
func realParents(root *os.Root, shown, rel string) error {
	dir := ""
	for _, seg := range strings.Split(path.Dir(rel), "/") {
		if seg == "." {
			break
		}
		dir = path.Join(dir, seg)
		fi, err := root.Lstat(filepath.FromSlash(dir))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return refuse(shown, danglingWhy, danglingHint)
		case err != nil:
			return unreadable(shown, err)
		case fi.Mode()&fs.ModeSymlink != 0:
			return refuseLinkedDir(shown, dir)
		case !fi.IsDir():
			return refuse(shown, danglingWhy, danglingHint)
		}
	}
	return nil
}

// shortNameRE matches a Windows 8.3 short name (GIT~1, NODE_M~1.TXT), which NTFS resolves to a long
// name it cannot be compared with.
var shortNameRE = regexp.MustCompile(`~[0-9]+(\.[^.]*)?$`)

// belowBase is p, a slash path from the source's top, relative to base, the module root's path
// from there ("." for the top), when p is below it; else p itself.
func belowBase(base, p string) string {
	if below, ok := strings.CutPrefix(p, base+"/"); ok && base != "." {
		return below
	}
	return p
}

// linkIgnored reports whether a link's target p, a slash path from the source's top, is a path
// lola never copies, below base (belowBase): a segment that is an ignored name in any letter case,
// as a case-insensitive file system resolves .GIT to .git, or a Windows short name, which may
// stand for one. A link's target is compared more strictly than the copy's ignored, since the
// link's author chose it to reach whatever the file system resolves it to.
func linkIgnored(base, p string) bool {
	for _, seg := range strings.Split(belowBase(base, p), "/") {
		if shortNameRE.MatchString(seg) {
			return true
		}
		for name := range ignoredNames {
			if strings.EqualFold(seg, name) {
				return true
			}
		}
	}
	return false
}

// refuseIgnored refuses the link shown whose chain reaches p, a path lola never copies (linkIgnored).
func refuseIgnored(shown, p string) error {
	return refuse(shown, "its target is in "+untrusted.Text(p, untrusted.MaxName)+", which lola never copies",
		"keep the file the link names out of "+strings.Join(slices.Sorted(maps.Keys(ignoredNames)), ", ")+
			" in any letter case, and name it without a Windows short name (NAME~1)")
}

// refuseLinkedDir refuses the link shown whose target is below dir, a link to a directory.
func refuseLinkedDir(shown, dir string) error {
	return refuse(shown, "it goes through the linked directory "+untrusted.Text(dir, untrusted.MaxName)+"; lola follows links only to files", dirLinkHint)
}

// unreadable is the error for the link shown when reading a path on its chain failed with err: a
// transient failure on the user's side, not ErrLinkInSource, which is the source's to fix.
func unreadable(shown string, err error) error {
	return errs.WithHints(fmt.Errorf("%s: lola cannot read it: %w", shown, untrusted.CleanErr(err)),
		"check that you can read the source's files, then try again")
}

// refuse is ErrLinkInSource for the link shown (printable already), saying why and what to do.
func refuse(shown, why string, hints ...string) error {
	return errs.WithHints(fmt.Errorf("%s: %w: %s", shown, ErrLinkInSource, why), hints...)
}

// gitIgnored refuses the link shown when the git work tree of root, the module root, ignores its
// target, the absolute path abs (target as the boundary-relative path printed): a folder in a git
// work tree is copied without the files git ignores, and a link must not bring one in. A tracked
// file is never ignored, as for git ls-files, and a target outside the work tree is not git's to
// ignore: git never listed it, and the other link rules alone apply to it.
func gitIgnored(ctx context.Context, root, shown, target, abs string) error {
	gitHint := "check that git works in the module's folder (git status there), then try again"
	run := func(args ...string) (string, error) {
		cmd := GitCommand(ctx, root, args...)
		stderr := &headWriter{limit: maxStderr + 1}
		cmd.Stderr = stderr
		out, err := cmd.Output()
		if err != nil {
			err = fmt.Errorf("%w: %s", err, untrusted.Text(strings.TrimSpace(stderr.String()), maxStderr))
		}
		return string(out), err
	}
	top, err := run("rev-parse", "--show-toplevel")
	if err != nil {
		return errs.WithHints(fmt.Errorf("%s: git could not name the work tree it is in: %w", shown, err), gitHint)
	}
	within, err := IsWithin(filepath.FromSlash(strings.TrimSpace(top)), abs)
	if err != nil {
		return errs.WithHints(fmt.Errorf("%s: %w", shown, untrusted.CleanErr(err)), gitHint)
	}
	if !within {
		return nil
	}
	_, err = run("check-ignore", "-q", "--", abs)
	exit, isExit := errors.AsType[*exec.ExitError](err)
	switch {
	case err == nil:
		return refuse(shown, "git ignores its target "+untrusted.Text(target, untrusted.MaxName),
			"commit the file the link names, or stop ignoring it in .gitignore")
	case isExit && exit.ExitCode() == 1:
		return nil
	default:
		return errs.WithHints(fmt.Errorf("%s: git check-ignore of its target failed: %w", shown, err), gitHint)
	}
}
