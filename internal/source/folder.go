package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/fsx"
)

// ignoredNames are never copied from a folder source.
var ignoredNames = map[string]bool{".git": true, ".venv": true, "node_modules": true, ".lola": true, "__pycache__": true, ".DS_Store": true}

// Fetched is module content staged for the store. Dir is a fresh directory the caller owns.
type Fetched struct {
	Dir    string
	Name   string // fallback module name: the base name of the module root's folder
	Commit string
}

// FetchFolder copies the module root of a folder source into a new directory under
// stagingParent. In a git work tree only `git ls-files --cached --others --exclude-standard`
// content is copied. Symlinks are refused.
func FetchFolder(ctx context.Context, spec Spec, stagingParent string) (Fetched, error) {
	root := spec.Location
	name := filepath.Base(root)
	var subpath string // Track the relative path from spec.Location to root for symlink checking

	if spec.Path != "" {
		if !filepath.IsLocal(spec.Path) {
			return Fetched{}, fmt.Errorf("--path %q must be a relative path inside the source", spec.Path)
		}
		subpath = spec.Path
		root = filepath.Join(root, spec.Path)
		name = filepath.Base(root)
	} else if fi, err := os.Stat(filepath.Join(root, "module")); err == nil && fi.IsDir() {
		subpath = "module"
		root = filepath.Join(root, "module")
	}

	// Check for symlinks in the sub-path components (but not in spec.Location itself)
	if subpath != "" {
		if err := checkNoSymlinks(spec.Location, subpath); err != nil {
			return Fetched{}, err
		}
	}

	if fi, err := os.Stat(root); err != nil {
		return Fetched{}, err
	} else if !fi.IsDir() {
		return Fetched{}, fmt.Errorf("%s is not a directory", root)
	}

	// Check containment BEFORE creating the staging directory
	if within, err := isWithin(root, stagingParent); err != nil {
		return Fetched{}, err
	} else if within {
		return Fetched{}, errs.WithHints(
			fmt.Errorf("refusing to copy %s into lola's cache at %s, which is inside it", root, stagingParent),
			"install from a directory that does not contain lola's cache")
	}

	if err := os.MkdirAll(stagingParent, 0o700); err != nil {
		return Fetched{}, err
	}
	files, err := listFiles(ctx, root)
	if err != nil {
		return Fetched{}, err
	}
	dst, err := os.MkdirTemp(stagingParent, "fetch-*")
	if err != nil {
		return Fetched{}, err
	}
	if err := copyFiles(root, dst, files); err != nil {
		_ = os.RemoveAll(dst)
		return Fetched{}, err
	}
	return Fetched{Dir: dst, Name: name}, nil
}

// checkNoSymlinks validates that no component of the relative path subpath (relative to root)
// is a symlink. Returns an error with the first symlinked component.
func checkNoSymlinks(root, subpath string) error {
	components := strings.Split(filepath.ToSlash(subpath), "/")
	var current string
	for _, comp := range components {
		if current == "" {
			current = comp
		} else {
			current = filepath.Join(current, comp)
		}
		full := filepath.Join(root, current)
		fi, err := os.Lstat(full)
		if err != nil {
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s: symbolic links are not allowed in module content; lola never follows links in a source", filepath.ToSlash(current))
		}
	}
	return nil
}

func isWithin(parent, child string) (bool, error) {
	p, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return false, err
	}
	// For child, resolve through the nearest existing ancestor since child may not exist yet
	c := child
	for {
		if _, err := os.Lstat(c); err == nil {
			break
		}
		parent := filepath.Dir(c)
		if parent == c {
			// Reached root, can't resolve further
			return false, nil
		}
		c = parent
	}
	c, err = filepath.EvalSymlinks(c)
	if err != nil {
		return false, err
	}
	// Append the remaining non-existent components lexically to the resolved ancestor
	if child != c {
		rel := strings.TrimPrefix(strings.TrimPrefix(child, c), string(filepath.Separator))
		if rel != "" {
			c = filepath.Join(c, rel)
		}
	}
	rel, err := filepath.Rel(p, c)
	return err == nil && filepath.IsLocal(rel), nil
}

func ignored(rel string) bool {
	return slices.ContainsFunc(strings.Split(filepath.ToSlash(rel), "/"), func(seg string) bool { return ignoredNames[seg] })
}

// listFiles returns root-relative paths of candidate files.
func listFiles(ctx context.Context, root string) ([]string, error) {
	out, err := gitCommand(ctx, root, "rev-parse", "--is-inside-work-tree").Output()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err == nil && strings.TrimSpace(string(out)) == "true" {
		return gitFiles(ctx, root)
	}
	var files []string
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
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

// gitCommand builds a git invocation for an UNTRUSTED source repository. Its .git/config is
// attacker-controlled, so options that make git execute configured programs (core.fsmonitor)
// or write to the repository are disabled on the command line, which outranks repository config.
func gitCommand(ctx context.Context, dir string, args ...string) *exec.Cmd {
	full := append([]string{"-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	return cmd
}

func gitFiles(ctx context.Context, root string) ([]string, error) {
	cmd := gitCommand(ctx, root, "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", ".")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files in %s: %w: %s", root, err, strings.TrimSpace(stderr.String()))
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

func copyFiles(root, dst string, files []string) error {
	for _, rel := range files {
		src := filepath.Join(root, rel)
		fi, err := os.Lstat(src)
		switch {
		case os.IsNotExist(err):
			continue // tracked by git but deleted in the work tree
		case err != nil:
			return err
		case fi.Mode()&fs.ModeSymlink != 0:
			return fmt.Errorf("%s: symbolic links are not allowed in module content; lola never follows links in a source", filepath.ToSlash(rel))
		case fi.IsDir():
			continue // a git submodule entry
		case !fi.Mode().IsRegular():
			return fmt.Errorf("%s: not a regular file", filepath.ToSlash(rel))
		}
		if err := copyFile(src, filepath.Join(dst, rel), fsx.NormalizeMode(fi.Mode())); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string, mode fs.FileMode) (err error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, in.Close()) }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
