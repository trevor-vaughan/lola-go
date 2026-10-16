package engine

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/module"
)

// moduleCopy is the cache version a module copy must hold (§6.4), whether the copy is
// replaced with it, and whether the module's install hooks run in it on targets.
type moduleCopy struct {
	name     string
	src      string // cache content dir
	replace  bool
	hooks    []module.Hook
	targets  []string
	runHooks bool
}

// needsCopy reports whether the copy of name under dir must be (re)placed: its version
// changes (prev is the ledger entry, nil when the module has none) or the copy is missing.
// A copy's own content is never compared: install hooks build in it (§6.4).
func needsCopy(dir, name string, prev *ledger.Entry, digest string) (bool, error) {
	if prev == nil || prev.Digest != digest {
		return true, nil
	}
	_, err := os.Lstat(filepath.Join(dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	return false, err
}

// copySwap is an in-progress replacement of module copies under dir. Each replaced copy
// <name> is first assembled as .<name>.new, the current one moves aside to .<name>.old, and
// the new one takes its place; the names are fixed because the scope lock is held.
type copySwap struct {
	dir      string
	replaced []string // names whose copy was swapped in, in order
	hadOld   map[string]bool
}

// swapCopies puts in place each wanted copy marked replace. perm is the mode for dir and
// its parents. On error it undoes what it did.
func swapCopies(dir string, want []moduleCopy, perm fs.FileMode) (sw *copySwap, err error) {
	sw = &copySwap{dir: dir, hadOld: map[string]bool{}}
	defer func() {
		if err != nil {
			err = errors.Join(err, sw.rollback())
			sw = nil
		}
	}()
	for _, c := range want {
		if !c.replace {
			continue
		}
		cur := filepath.Join(dir, c.name)
		newDir, oldDir := filepath.Join(dir, "."+c.name+".new"), filepath.Join(dir, "."+c.name+".old")
		for _, leftover := range []string{newDir, oldDir} {
			if err := os.RemoveAll(leftover); err != nil {
				return sw, err
			}
		}
		if err := os.MkdirAll(dir, perm); err != nil {
			return sw, err
		}
		if err := os.CopyFS(newDir, os.DirFS(c.src)); err != nil {
			return sw, errors.Join(fmt.Errorf("copying %s into %s: %w", c.name, dir, err), os.RemoveAll(newDir))
		}
		switch err := os.Rename(cur, oldDir); {
		case err == nil:
			sw.hadOld[c.name] = true
		case !errors.Is(err, fs.ErrNotExist):
			return sw, errors.Join(err, os.RemoveAll(newDir))
		}
		if err := os.Rename(newDir, cur); err != nil {
			if sw.hadOld[c.name] {
				err = errors.Join(err, os.Rename(oldDir, cur))
			}
			return sw, errors.Join(err, os.RemoveAll(newDir))
		}
		sw.replaced = append(sw.replaced, c.name)
	}
	return sw, nil
}

// rollback restores the copies that were replaced, newest first.
func (sw *copySwap) rollback() error {
	var errs []error
	for i := len(sw.replaced) - 1; i >= 0; i-- {
		name := sw.replaced[i]
		cur := filepath.Join(sw.dir, name)
		errs = append(errs, os.RemoveAll(cur))
		if sw.hadOld[name] {
			errs = append(errs, os.Rename(filepath.Join(sw.dir, "."+name+".old"), cur))
		}
	}
	sw.replaced = nil
	return errors.Join(errs...)
}

// commit drops the copies that were moved aside.
func (sw *copySwap) commit() error {
	var errs []error
	for _, name := range sw.replaced {
		errs = append(errs, os.RemoveAll(filepath.Join(sw.dir, "."+name+".old")))
	}
	return errors.Join(errs...)
}
