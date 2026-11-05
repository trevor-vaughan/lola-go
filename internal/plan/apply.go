package plan

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"

	"github.com/trevor-vaughan/lola-go/internal/fsx"
)

// ManifestWrite is a manifest edit Apply writes in the same transaction as the ledger: Data
// at Path, mode 0600.
type ManifestWrite struct {
	Path string
	Data []byte
}

// Apply makes p's file changes, writes ledger to ledgerPath and, when man is set, the
// manifest, then runs after. Any failure up to and including the manifest write restores
// every touched file, the ledger and the manifest, and removes everything staged. The caller
// holds the scope lock.
func Apply(p *Plan, ledgerPath string, man *ManifestWrite, after func() error) error {
	tx := &txn{}
	if err := tx.run(p, ledgerPath, man); err != nil {
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
	backups   []backup          // files moved aside
	kept      []backup          // files backed up in place, replaced by one rename over them
	placed    []string          // destinations that now hold staged content
	newLedger string            // path to newly created ledger file, empty if ledger existed
}

func (t *txn) run(p *Plan, ledgerPath string, man *ManifestWrite) error {
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
		if err := t.backupIfPresent(fc.Abs); err != nil {
			return err
		}
		if !fc.Delete {
			if err := t.rename(fc.Abs); err != nil {
				return err
			}
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
	if err := p.Ledger.Save(ledgerPath); err != nil || man == nil {
		return err
	}
	// The manifest follows the ledger, staged next to it. The current one stays in place until
	// the new one is renamed over it, so a crash never leaves the scope without its manifest;
	// rollback renames the backup over it. Directories it needs are private to the user, as
	// user.yml's always were.
	if err := t.mkdirAll(filepath.Dir(man.Path), 0o700); err != nil {
		return err
	}
	if err := t.stage(FileChange{Abs: man.Path, Data: man.Data, Mode: 0o600}); err != nil {
		return err
	}
	return t.replace(man.Path)
}

// replace puts the staged content for abs in place with one rename over the current file,
// backed up first by keep, then syncs the directory.
func (t *txn) replace(abs string) error {
	if _, err := os.Lstat(abs); err == nil {
		if err := t.keep(abs); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := t.rename(abs); err != nil {
		return err
	}
	return fsx.SyncDir(filepath.Dir(abs))
}

// keep backs up the file at abs and leaves it in place: a hard link next to it, or a copy
// where the file system has no hard links.
func (t *txn) keep(abs string) error {
	fi, err := os.Lstat(abs)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(abs), "."+filepath.Base(abs)+".lola-bak-*")
	if err != nil {
		return err
	}
	bak := f.Name()
	if err := errors.Join(f.Close(), os.Remove(bak)); err != nil {
		return err
	}
	if err := os.Link(abs, bak); err != nil {
		if err := copyFile(abs, bak, fi.Mode().Perm()); err != nil {
			if rmErr := os.Remove(bak); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
				err = errors.Join(err, rmErr)
			}
			return err
		}
	}
	t.kept = append(t.kept, backup{abs: abs, bak: bak})
	return nil
}

// copyFile copies src to dst, which must not exist, with mode perm, and syncs it.
func copyFile(src, dst string, perm fs.FileMode) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}

// backupIfPresent moves the file at abs aside when there is one.
func (t *txn) backupIfPresent(abs string) error {
	if _, err := os.Lstat(abs); err == nil {
		return t.backup(abs)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// rename puts the staged content for abs in place.
func (t *txn) rename(abs string) error {
	if err := os.Rename(t.staged[abs], abs); err != nil {
		return err
	}
	delete(t.staged, abs)
	t.placed = append(t.placed, abs)
	return nil
}

func (t *txn) stage(fc FileChange) error {
	dir := filepath.Dir(fc.Abs)
	if err := t.mkdirAll(dir, 0o755); err != nil {
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

// mkdirAll creates dir, and its missing parents, with mode perm and remembers the outermost
// directory it had to create.
func (t *txn) mkdirAll(dir string, perm fs.FileMode) error {
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
	if err := os.MkdirAll(dir, perm); err != nil {
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
		if !slices.ContainsFunc(t.kept, func(b backup) bool { return b.abs == abs }) {
			errs = append(errs, os.Remove(abs))
		}
	}
	for _, b := range slices.Backward(t.backups) {
		errs = append(errs, os.Rename(b.bak, b.abs))
	}
	// A backup that is still a link to abs (nothing replaced it yet) survives the rename.
	for _, b := range t.kept {
		errs = append(errs, os.Rename(b.bak, b.abs))
		if err := os.Remove(b.bak); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
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
	for _, b := range slices.Concat(t.backups, t.kept) {
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
