package fsx

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Resolved is a logical destination and where writes to it actually land.
type Resolved struct {
	Logical string // root-relative path as the target sees it
	Abs     string // fully symlink-resolved absolute path (may not exist yet)
	Outside bool   // Abs is outside the resolved root
}

// Resolve maps a root-relative logical path to its real location. Symlinked directories on
// the way are followed, a symlink at the final component is written through (dangling links
// included), and components that do not exist yet are appended lexically.
func Resolve(root, logical string) (Resolved, error) {
	if !filepath.IsLocal(logical) {
		return Resolved{}, fmt.Errorf("path %q must be relative and stay inside the root", logical)
	}
	realRoot, err := resolveAbs(filepath.Clean(root), 0)
	if err != nil {
		return Resolved{}, err
	}
	abs, err := resolveAbs(filepath.Join(root, logical), 0)
	if err != nil {
		return Resolved{}, err
	}
	rel, err := filepath.Rel(realRoot, abs)
	outside := err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
	return Resolved{Logical: filepath.ToSlash(logical), Abs: abs, Outside: outside}, nil
}

const maxLinkHops = 40

func resolveAbs(abs string, hops int) (string, error) {
	if hops > maxLinkHops {
		return "", fmt.Errorf("%s: too many levels of symbolic links", abs)
	}
	existing, rest := abs, ""
	for {
		fi, err := os.Lstat(existing)
		if err == nil {
			if fi.Mode()&fs.ModeSymlink != 0 {
				if _, err := os.Stat(existing); errors.Is(err, fs.ErrNotExist) {
					target, err := os.Readlink(existing)
					if err != nil {
						return "", err
					}
					if !filepath.IsAbs(target) {
						dir, err := resolveAbs(filepath.Dir(existing), hops+1)
						if err != nil {
							return "", err
						}
						target = filepath.Join(dir, target)
					}
					return resolveAbs(filepath.Join(target, rest), hops+1)
				}
			}
			real, err := filepath.EvalSymlinks(existing)
			if err != nil {
				return "", err
			}
			return filepath.Join(real, rest), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return abs, nil
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = parent
	}
}
