package plan

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"
)

// Apply makes p's file changes, writes ledger to ledgerPath, then runs after (for example
// saving the manifest). Any failure up to and including the ledger write restores every
// touched file and removes everything staged. The caller holds the scope lock.
func Apply(p *Plan, ledgerPath string, after func() error) error {
	tx := &txn{}
	if err := tx.run(p, ledgerPath); err != nil {
		if rbErr := tx.rollback(); rbErr != nil {
			return errors.Join(err, fmt.Errorf("rollback incomplete: %w", rbErr))
		}
		return err
	}
	tidyErr := errors.Join(tx.commit(), cleanup(p))
	var afterErr error
	if after != nil {
		afterErr = after()
	}
	if tidyErr != nil {
		return errors.Join(afterErr, fmt.Errorf("changes applied, but tidying up failed: %w", tidyErr))
	}
	return afterErr
}

type backup struct{ abs, bak string }

// txn records every reversible step taken so far.
type txn struct {
	staged    map[string]string // destination → staged temp file
	madeDirs  []string          // directories created for staging, outermost first
	backups   []backup
	placed    []string // destinations that now hold staged content
	newLedger string   // path to newly created ledger file, empty if ledger existed
}

func (t *txn) run(p *Plan, ledgerPath string) error {
	t.staged = map[string]string{}
	for _, fc := range p.Files {
		if fc.Delete {
			continue
		}
		if err := t.stage(fc); err != nil {
			return err
		}
	}
	for _, fc := range p.Files {
		if _, err := os.Lstat(fc.Abs); err == nil {
			if err := t.backup(fc.Abs); err != nil {
				return err
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if !fc.Delete {
			if err := os.Rename(t.staged[fc.Abs], fc.Abs); err != nil {
				return err
			}
			delete(t.staged, fc.Abs)
			t.placed = append(t.placed, fc.Abs)
		}
	}
	// Backup the existing ledger before saving the new one; if Save fails after
	// renaming the file into place, rollback can restore it.
	if _, err := os.Lstat(ledgerPath); err == nil {
		if err := t.backup(ledgerPath); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	} else {
		t.newLedger = ledgerPath
	}
	return p.Ledger.Save(ledgerPath)
}

func (t *txn) stage(fc FileChange) error {
	dir := filepath.Dir(fc.Abs)
	if err := t.mkdirAll(dir); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(fc.Abs)+".lola-new-*")
	if err != nil {
		return err
	}
	t.staged[fc.Abs] = f.Name()
	_, err = f.Write(fc.Data)
	if err == nil {
		err = f.Chmod(fc.Mode)
	}
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}

// mkdirAll creates dir and remembers the outermost directory it had to create.
func (t *txn) mkdirAll(dir string) error {
	missing := ""
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Lstat(d); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		missing = d
		if filepath.Dir(d) == d {
			break
		}
	}
	if missing == "" {
		return nil
	}
	t.madeDirs = append(t.madeDirs, missing)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return nil
}

// backup moves the current file aside, next to it, so restoring is a rename.
func (t *txn) backup(abs string) error {
	f, err := os.CreateTemp(filepath.Dir(abs), "."+filepath.Base(abs)+".lola-bak-*")
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(abs, f.Name()); err != nil {
		return errors.Join(err, os.Remove(f.Name()))
	}
	t.backups = append(t.backups, backup{abs: abs, bak: f.Name()})
	return nil
}

func (t *txn) rollback() error {
	var errs []error
	for _, abs := range slices.Backward(t.placed) {
		errs = append(errs, os.Remove(abs))
	}
	for _, b := range slices.Backward(t.backups) {
		errs = append(errs, os.Rename(b.bak, b.abs))
	}
	for _, tmp := range t.staged {
		if err := os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	for _, d := range slices.Backward(t.madeDirs) {
		errs = append(errs, os.RemoveAll(d))
	}
	if t.newLedger != "" {
		if err := os.Remove(t.newLedger); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (t *txn) commit() error {
	var errs []error
	for _, b := range t.backups {
		errs = append(errs, os.Remove(b.bak))
	}
	return errors.Join(errs...)
}

// cleanup removes directories that deletes left empty, never climbing past a stop dir or
// out of the root, and removes a logical symlink whose target was just deleted.
func cleanup(p *Plan) error {
	var errs []error
	for _, fc := range p.Files {
		if !fc.Delete {
			continue
		}
		if link := filepath.Join(fc.Root, filepath.FromSlash(fc.Logical)); link != fc.Abs {
			if fi, err := os.Lstat(link); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
				if _, err := os.Stat(link); errors.Is(err, fs.ErrNotExist) {
					errs = append(errs, os.Remove(link))
				}
			}
		}
		if fc.Outside {
			continue
		}
		for dir := filepath.Dir(fc.Abs); !slices.Contains(fc.StopDirs, dir) && len(fc.StopDirs) > 0 && within(fc.StopDirs[0], dir); dir = filepath.Dir(dir) {
			err := os.Remove(dir)
			if err == nil {
				continue
			}
			if !errors.Is(err, syscall.ENOTEMPTY) && !errors.Is(err, syscall.EEXIST) && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
			}
			break
		}
	}
	return errors.Join(errs...)
}

func within(root, dir string) bool {
	rel, err := filepath.Rel(root, dir)
	return err == nil && rel != "." && filepath.IsLocal(rel)
}
