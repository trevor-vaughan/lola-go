package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/fsx"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// ignoredNames are never copied from a folder source.
var ignoredNames = map[string]bool{".git": true, ".venv": true, "node_modules": true, ".lola": true, "__pycache__": true, ".DS_Store": true}

// Fetched is module content staged for the store. Dir is a fresh directory the caller owns.
// Executables are the files the source's own metadata marks executable (git's tree modes, an
// archive's entry modes, a folder's file system), as sorted slash paths relative to Dir: the
// digest takes execute bits from them, never from the file system lola copied to (Plan 6a §5.2).
type Fetched struct {
	Dir         string
	Name        string // fallback module name: the base name of the module root's folder
	Commit      string
	Executables []string
}

// fetchFolder copies the module root of a folder source into a new directory under
// stagingParent. In a git work tree only `git ls-files --cached --others --exclude-standard`
// content is copied. Symlinks are refused.
func fetchFolder(ctx context.Context, spec Spec, stagingParent string) (Fetched, error) {
	root, name, err := folderRoot(spec.Location, spec.Path, filepath.Base(spec.Location))
	if err != nil {
		return Fetched{}, err
	}

	// Check containment BEFORE creating the staging directory
	if within, err := IsWithin(root, stagingParent); err != nil {
		return Fetched{}, err
	} else if within {
		return Fetched{}, errs.WithHints(
			fmt.Errorf("refusing to copy %s into lola's cache at %s, which is inside it", root, stagingParent),
			"install from a directory that does not contain lola's cache")
	}

	dst, execs, err := copyTree(ctx, root, stagingParent, listDetect)
	if err != nil {
		return Fetched{}, err
	}
	return Fetched{Dir: dst, Name: name, Executables: execs}, nil
}

// listing says how copyTree enumerates a tree's files (design §3).
type listing int

const (
	listDetect listing = iota // a folder source: git ls-files in a git work tree, else a walk
	listWalk                  // a tree lola unpacked, which git must never read: a directory walk
	listGit                   // a clone lola made: git ls-files of what is committed
)

// copyTree copies the files of root, as listed by mode, into a new directory under
// stagingParent and returns it with the sorted slash paths of the copied files the file
// system marks executable. Ignored names are skipped and symlinks refused.
func copyTree(ctx context.Context, root, stagingParent string, mode listing) (string, []string, error) {
	if err := os.MkdirAll(stagingParent, 0o700); err != nil {
		return "", nil, err
	}
	files, err := listFiles(ctx, root, mode)
	if err != nil {
		return "", nil, err
	}
	dst, err := os.MkdirTemp(stagingParent, "fetch-*")
	if err != nil {
		return "", nil, err
	}
	execs, err := copyFiles(root, dst, files)
	if err != nil {
		return "", nil, errors.Join(err, os.RemoveAll(dst))
	}
	slices.Sort(execs)
	return dst, execs, nil
}

// executablesUnder returns, of all (slash paths relative to a tree), the ones below the
// module root rel ("." for the tree itself) that were copied into dst, relative to rel, sorted.
func executablesUnder(all []string, rel, dst string) []string {
	prefix := ""
	if rel != "." {
		prefix = rel + "/"
	}
	out := []string{}
	for _, p := range all {
		sub, ok := strings.CutPrefix(p, prefix)
		if !ok {
			continue
		}
		if fi, err := os.Lstat(filepath.Join(dst, filepath.FromSlash(sub))); err == nil && fi.Mode().IsRegular() {
			out = append(out, sub)
		}
	}
	slices.Sort(out)
	return out
}

// pathHint coaches a --path of a folder or git source that picks no directory.
const pathHint = "--path is relative to the source's root: the folder itself, or the top of the git repository"

// folderRoot applies the folder and git module-root rule (design §3.1) to the tree at base:
// --path, else module/ when it is a directory, else base itself. A --path of "." picks base
// itself explicitly, skipping module/. The fallback name is baseName unless --path picks a
// directory below base. Errors show the --path as given, never base, which for git is lola's
// scratch directory.
func folderRoot(base, subpath, baseName string) (string, string, error) {
	if subpath == "" {
		if fi, err := os.Stat(filepath.Join(base, "module")); err == nil && fi.IsDir() {
			return checkedRoot(base, "module", baseName, "module/")
		}
	}
	if subpath == "" || filepath.Clean(subpath) == "." {
		if fi, err := os.Stat(base); err != nil {
			return "", "", err
		} else if !fi.IsDir() {
			return "", "", fmt.Errorf("%s is not a directory", untrusted.Text(base, untrusted.MaxName))
		}
		return base, baseName, nil
	}
	shown := fmt.Sprintf("--path %q", untrusted.Text(subpath, untrusted.MaxName))
	if !filepath.IsLocal(subpath) {
		return "", "", errs.WithHints(fmt.Errorf("%s must be a relative path inside the source", shown), pathHint)
	}
	return checkedRoot(base, subpath, filepath.Base(filepath.Join(base, subpath)), shown)
}

// checkedRoot returns base/subpath and name once no component of subpath is a symlink and it
// is a directory. shown names subpath in errors.
func checkedRoot(base, subpath, name, shown string) (string, string, error) {
	current := ""
	for _, comp := range strings.Split(filepath.ToSlash(subpath), "/") {
		current = filepath.Join(current, comp)
		fi, err := os.Lstat(filepath.Join(base, current))
		switch {
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			return "", "", fmt.Errorf("%s: %w", shown, untrusted.CleanErr(err))
		case err == nil && fi.Mode()&fs.ModeSymlink != 0:
			return "", "", fmt.Errorf("%s: symbolic links are not allowed in module content; lola never follows links in a source",
				untrusted.Text(filepath.ToSlash(current), untrusted.MaxName))
		case err != nil || !fi.IsDir():
			return "", "", errs.WithHints(fmt.Errorf("%s is not a directory in the source", shown), pathHint)
		}
	}
	return filepath.Join(base, subpath), name, nil
}

// IsWithin reports whether child, after symbolic links, is inside parent, after symbolic links:
// a path relative to the resolved parent that is filepath.IsLocal (Plan 6a §8). A child that does
// not exist yet is resolved through its nearest existing ancestor. An error resolving either is
// returned, never taken as an answer.
func IsWithin(parent, child string) (bool, error) {
	p, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return false, err
	}
	// Resolve child through its nearest existing ancestor, since child may not exist yet, and
	// append the components below that ancestor as they are. A ".." in a path that does not
	// exist is refused: cleaning it lexically would skip a link it passes through.
	anc := child
	for {
		_, err := os.Lstat(anc)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
		if anc == child && slices.Contains(strings.Split(filepath.ToSlash(child), "/"), "..") {
			return false, fmt.Errorf("cannot place %s: it does not exist and contains \"..\"", child)
		}
		up := filepath.Dir(anc)
		if up == anc {
			return false, fmt.Errorf("no part of %s exists", child)
		}
		anc = up
	}
	c, err := filepath.EvalSymlinks(anc)
	if err != nil {
		return false, err
	}
	below, err := filepath.Rel(anc, child)
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(p, filepath.Join(c, below))
	if err != nil {
		return false, err
	}
	return filepath.IsLocal(rel), nil
}

func ignored(rel string) bool {
	return slices.ContainsFunc(strings.Split(filepath.ToSlash(rel), "/"), func(seg string) bool { return ignoredNames[seg] })
}

// listFiles returns root-relative paths of candidate files.
func listFiles(ctx context.Context, root string, mode listing) ([]string, error) {
	switch mode {
	case listGit:
		return gitFiles(ctx, root, "--cached")
	case listDetect:
		out, err := GitCommand(ctx, root, "rev-parse", "--is-inside-work-tree").Output()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err == nil && strings.TrimSpace(string(out)) == "true" {
			return gitFiles(ctx, root, "--cached", "--others", "--exclude-standard")
		}
	}
	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return err
		}
		if ignoredNames[d.Name()] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			files = append(files, rel)
		}
		return nil
	})
	return files, err
}

// gitFiles lists the files under root that git ls-files selects with which.
func gitFiles(ctx context.Context, root string, which ...string) ([]string, error) {
	args := append(append([]string{"ls-files", "-z"}, which...), "--", ".")
	cmd := GitCommand(ctx, root, args...)
	stderr := &headWriter{limit: maxStderr + 1}
	cmd.Stderr = stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files in the source failed: %w: %s", err, untrusted.Text(strings.TrimSpace(stderr.String()), maxStderr))
	}
	var files []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" && !ignored(f) {
			files = append(files, filepath.FromSlash(f))
		}
	}
	slices.Sort(files)
	return slices.Compact(files), nil
}

// copyFiles copies files from root to dst and returns the slash paths of those the file
// system marks executable.
func copyFiles(root, dst string, files []string) ([]string, error) {
	execs := []string{}
	for _, rel := range files {
		src := filepath.Join(root, rel)
		fi, err := os.Lstat(src)
		switch {
		case os.IsNotExist(err):
			continue // tracked by git but deleted in the work tree
		case err != nil:
			return nil, fmt.Errorf("%s: %w", untrusted.Text(filepath.ToSlash(rel), untrusted.MaxName), untrusted.CleanErr(err))
		case fi.Mode()&fs.ModeSymlink != 0:
			return nil, fmt.Errorf("%s: symbolic links are not allowed in module content; lola never follows links in a source", untrusted.Text(filepath.ToSlash(rel), untrusted.MaxName))
		case fi.IsDir():
			continue // a git submodule entry
		case !fi.Mode().IsRegular():
			return nil, fmt.Errorf("%s: not a regular file", untrusted.Text(filepath.ToSlash(rel), untrusted.MaxName))
		}
		if err := copyFile(src, filepath.Join(dst, rel), fi); err != nil {
			return nil, fmt.Errorf("%s: %w", untrusted.Text(filepath.ToSlash(rel), untrusted.MaxName), untrusted.CleanErr(err))
		}
		if fsx.NormalizeMode(fi.Mode()) == 0o755 {
			execs = append(execs, filepath.ToSlash(rel))
		}
	}
	return execs, nil
}

// copyFile copies src, which the caller found (with Lstat) to be the regular file fi. The
// opened file must still be that file: one swapped for a symlink or another file between the
// check and the open is refused, so a source can never redirect lola outside itself.
func copyFile(src, dst string, fi fs.FileInfo) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, in.Close()) }()
	opened, err := in.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(fi, opened) || !opened.Mode().IsRegular() {
		return errors.New("changed while lola was reading it; refusing it")
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fsx.NormalizeMode(fi.Mode()))
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
