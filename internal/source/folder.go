package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
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
// digest takes execute bits from them, never from the file system lola copied to.
type Fetched struct {
	Dir         string
	Name        string // fallback module name: the base name of the module root's folder
	Commit      string
	Executables []string
	// Root is where Dir's tree was in the source, a slash path from its top ("" for the top
	// itself): the module root lola found (an archive's wrapping directory, a module/) or the
	// --path. A later fetch with that --path stages the same tree.
	Root string
}

// fetchFolder copies the module root of a folder source into a new directory under
// stagingParent. In a git work tree only `git ls-files --cached --others --exclude-standard`
// content is copied. A symbolic link is copied as the file or directory it names inside the folder
// (resolveLink).
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

	rel, err := filepath.Rel(spec.Location, root)
	if err != nil {
		return Fetched{}, err
	}
	dst, execs, _, err := copyTree(ctx, spec.Location, root, stagingParent, listDetect, spec.Components)
	if err != nil {
		return Fetched{}, err
	}
	return Fetched{Dir: dst, Name: name, Executables: execs, Root: fetchedRoot(rel)}, nil
}

// listing says how copyTree enumerates a tree's files.
type listing int

const (
	listDetect listing = iota // a folder source: git ls-files in a git work tree, else a walk
	listWalk                  // a tree lola unpacked, which git must never read: a directory walk
	listGit                   // a clone lola made: git ls-files of what is committed
)

// copyTree copies the files of root, as listed by mode, into a new directory under
// stagingParent and returns it with the sorted slash paths of the copied files the file
// system marks executable, and copyFiles' links. Ignored names are skipped; a symbolic link is
// copied as the file it names inside boundary, the source tree root is in.
func copyTree(ctx context.Context, boundary, root, stagingParent string, mode listing, components *Components) (string, []string, map[string]string, error) {
	if err := os.MkdirAll(stagingParent, 0o700); err != nil {
		return "", nil, nil, err
	}
	files, byGit, err := listFiles(ctx, root, mode)
	if err != nil {
		return "", nil, nil, err
	}
	dst, err := os.MkdirTemp(stagingParent, "fetch-*")
	if err != nil {
		return "", nil, nil, err
	}
	c := &copier{boundary: boundary, mode: mode, components: components, left: maxEntries}
	execs, links, err := c.copyFiles(ctx, root, dst, files, byGit && mode == listDetect, "", nil)
	if err != nil {
		return "", nil, nil, errors.Join(err, os.RemoveAll(dst))
	}
	slices.Sort(execs)
	return dst, execs, links, nil
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

// folderRoot applies the folder and git module-root rule to the tree at base:
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
			return "", "", refuse(untrusted.Text(filepath.ToSlash(current), untrusted.MaxName), modulePathLink, modulePathLinkHint)
		case err != nil || !fi.IsDir():
			return "", "", errs.WithHints(fmt.Errorf("%s is not a directory in the source", shown), pathHint)
		}
	}
	return filepath.Join(base, subpath), name, nil
}

// IsWithin reports whether child, after symbolic links, is inside parent, after symbolic links:
// a path relative to the resolved parent that is filepath.IsLocal. A child that does
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

// listFiles returns root-relative paths of candidate files, and whether git listed them.
func listFiles(ctx context.Context, root string, mode listing) ([]string, bool, error) {
	switch mode {
	case listGit:
		files, err := gitFiles(ctx, root, "--cached")
		return files, true, err
	case listDetect:
		out, err := GitCommand(ctx, root, "rev-parse", "--is-inside-work-tree").Output()
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if err == nil && strings.TrimSpace(string(out)) == "true" {
			files, err := gitFiles(ctx, root, "--cached", "--others", "--exclude-standard")
			return files, true, err
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
	return files, false, err
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

// copier copies the files of one source tree: boundary is the directory every link must stay
// inside, mode how a tree is listed, components the selection installed from the module ("nil" when
// it is installed whole), and left how many more files the directory links may expand to.
type copier struct {
	boundary   string
	mode       listing
	components *Components
	left       int
}

// copyFiles copies files, paths relative to root, from root to dst and returns the slash paths
// of those the file system marks executable. A symbolic link is copied, at its own path, as the
// file or directory it names inside the boundary, a directory root is in (resolveLink), read
// through an os.Root of the boundary; askGit also refuses a file link's target that git ignores
// (gitIgnored). links maps each file copied from a link, and in a linked directory every file
// copied, to its source path, a slash path relative to the boundary. A link lola refuses is an
// error, unless it is outside every installed component (Components.reaches): then it is left out.
// at is where root's tree is in the module root, "" for the module root itself, and active the
// real directories being expanded, outermost first.
func (c *copier) copyFiles(ctx context.Context, root, dst string, files []string, askGit bool, at string, active []string) (execs []string, links map[string]string, err error) {
	base, err := filepath.Rel(c.boundary, root)
	if err != nil {
		return nil, nil, err
	}
	broot, err := os.OpenRoot(c.boundary)
	if err != nil {
		return nil, nil, err
	}
	defer func() { err = errors.Join(err, broot.Close()) }()
	execs, links = []string{}, map[string]string{}
	for _, rel := range files {
		shown := untrusted.Text(filepath.ToSlash(rel), untrusted.MaxName)
		src := filepath.Join(root, rel)
		fi, err := os.Lstat(src)
		switch {
		case os.IsNotExist(err):
			continue // tracked by git but deleted in the work tree
		case err != nil:
			return nil, nil, fmt.Errorf("%s: %w", shown, untrusted.CleanErr(err))
		case fi.Mode()&fs.ModeSymlink != 0:
			linkRel := filepath.ToSlash(filepath.Join(base, rel))
			subExecs, subLinks, err := c.copyLink(ctx, broot, root, dst, rel, linkRel, askGit, path.Join(at, filepath.ToSlash(rel)), active)
			if errors.Is(err, ErrLinkInSource) && !c.components.reaches(path.Join(at, filepath.ToSlash(rel))) {
				continue
			}
			if err != nil {
				return nil, nil, err
			}
			execs = append(execs, subExecs...)
			maps.Copy(links, subLinks)
			continue
		case fi.IsDir():
			continue // a git submodule entry
		case !fi.Mode().IsRegular():
			return nil, nil, fmt.Errorf("%s: not a regular file", shown)
		default:
			if err := copyFile(src, filepath.Join(dst, rel), fi); err != nil {
				return nil, nil, fmt.Errorf("%s: %w", shown, untrusted.CleanErr(err))
			}
			if at != "" {
				links[filepath.ToSlash(rel)] = path.Join(filepath.ToSlash(base), filepath.ToSlash(rel))
			}
		}
		if fsx.NormalizeMode(fi.Mode()) == 0o755 {
			execs = append(execs, filepath.ToSlash(rel))
		}
	}
	return execs, links, nil
}

// copyLink copies the link at rel (linkRel from the boundary, at in the module root) as the file
// or directory it names, and returns what copyFiles returns for it, with paths relative to root.
func (c *copier) copyLink(ctx context.Context, broot *os.Root, root, dst, rel, linkRel string, askGit bool, at string, active []string) ([]string, map[string]string, error) {
	shown := untrusted.Text(linkRel, untrusted.MaxName)
	base, err := filepath.Rel(c.boundary, root)
	if err != nil {
		return nil, nil, err
	}
	target, tfi, err := resolveLink(broot, filepath.ToSlash(base), linkRel)
	if err != nil {
		return nil, nil, err
	}
	if tfi.IsDir() {
		execs, links, err := c.copyLinkedDir(ctx, shown, linkRel, target, filepath.Join(dst, rel), at, active)
		if err != nil {
			return nil, nil, err
		}
		in := filepath.ToSlash(rel) + "/"
		for i, e := range execs {
			execs[i] = in + e
		}
		prefixed := make(map[string]string, len(links))
		for p, from := range links {
			prefixed[in+p] = from
		}
		return execs, prefixed, nil
	}
	if askGit {
		if err := gitIgnored(ctx, root, shown, target, filepath.Join(c.boundary, filepath.FromSlash(target))); err != nil {
			return nil, nil, err
		}
	}
	in, err := broot.Open(filepath.FromSlash(target))
	if err == nil {
		err = copyOpened(in, filepath.Join(dst, rel), tfi)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", shown, untrusted.CleanErr(err))
	}
	var execs []string
	if fsx.NormalizeMode(tfi.Mode()) == 0o755 {
		execs = []string{filepath.ToSlash(rel)}
	}
	return execs, map[string]string{filepath.ToSlash(rel): target}, nil
}

// copyLinkedDir copies the directory target (a slash path from the boundary) that the link at
// linkRel names into out, as the files the source lists in it. A link whose directory holds the
// link, or is one already being expanded, or is nested more than maxLinkHops deep, is a loop; one
// that expands to more files than c.left allows is refused too, so nested links cannot multiply.
func (c *copier) copyLinkedDir(ctx context.Context, shown, linkRel, target, out, at string, active []string) ([]string, map[string]string, error) {
	if target == "." || strings.HasPrefix(linkRel, target+"/") || slices.Contains(active, target) || len(active) >= maxLinkHops {
		return nil, nil, refuse(shown, cycleWhy, cycleHint)
	}
	abs := filepath.Join(c.boundary, filepath.FromSlash(target))
	files, byGit, err := listFiles(ctx, abs, c.mode)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", shown, untrusted.CleanErr(err))
	}
	if c.left -= len(files); c.left < 0 {
		return nil, nil, refuse(shown, fmt.Sprintf("the directories it names hold more than %d files in all", maxEntries), "link fewer or smaller directories")
	}
	return c.copyFiles(ctx, abs, out, files, byGit && c.mode == listDetect, at, append(slices.Clone(active), target))
}

// copyFile copies src, which the caller found (with Lstat) to be the regular file fi. The
// opened file must still be that file: one swapped for a symlink or another file between the
// check and the open is refused, so a source can never redirect lola outside itself.
func copyFile(src, dst string, fi fs.FileInfo) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	return copyOpened(in, dst, fi)
}

// copyOpened copies in, which must be the regular file fi (copyFile), to dst, and closes it.
func copyOpened(in *os.File, dst string, fi fs.FileInfo) (err error) {
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
