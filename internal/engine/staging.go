package engine

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"

	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// staleRun is the age at which a run directory is taken to be left by a process that was killed.
const staleRun = 24 * time.Hour

// runDir creates the private staging directory of one staging command (Plan 6a §6.3): its
// fetch scratch, fetched copies, version builds and snapshots all go inside. Commands that report
// notices sweep first (sweepRuns). done, which the command calls when it ends, removes the
// directory and its lock (scratch).
func (e *Engine) runDir() (dir string, done func() error, err error) {
	return scratch(e.Paths.Staging, runPrefix)
}

// Scratch directories: a staging command's under Paths.Staging, lola check's under Paths.Tmp;
// each is held by an exclusive lock on the file of its name plus lockSuffix beside it.
const (
	runPrefix   = "run-"
	checkPrefix = "check-"
	lockSuffix  = ".lock"
)

// scratchTries bounds scratch's attempts at a name nobody else holds.
const scratchTries = 10

// scratch creates a scratch directory, prefix plus a random suffix, under parent for one live
// run, and holds an exclusive lock on the lock file of its name beside it for the run's life
// (R-9-9): lola cache clean removes only scratch whose lock it can take without waiting. The lock
// file is created and locked before the directory exists; done removes the directory, then
// releases and removes the lock. A lock file another lola removed between its creation and its
// locking (a clean that found it free) holds nothing, so scratch takes another name.
func scratch(parent, prefix string) (dir string, done func() error, err error) {
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", nil, err
	}
	for range scratchTries {
		f, err := os.CreateTemp(parent, prefix+"*"+lockSuffix)
		if err != nil {
			return "", nil, err
		}
		lock := f.Name()
		if err := f.Close(); err != nil {
			return "", nil, errors.Join(err, os.Remove(lock))
		}
		fl := flock.New(lock)
		if ok, err := fl.TryLock(); err != nil {
			return "", nil, errors.Join(err, os.Remove(lock))
		} else if !ok {
			continue // a clean holds it while it removes it
		}
		if !lockedAt(fl, lock) {
			if err := fl.Unlock(); err != nil {
				return "", nil, err
			}
			continue
		}
		dir := strings.TrimSuffix(lock, lockSuffix)
		if err := os.Mkdir(dir, 0o700); err != nil {
			err = errors.Join(err, fl.Unlock(), os.Remove(lock))
			if errors.Is(err, fs.ErrExist) {
				continue // a directory left without its lock: not ours to reuse
			}
			return "", nil, err
		}
		return dir, func() error {
			err := errors.Join(os.RemoveAll(dir), fl.Unlock())
			if rmErr := os.Remove(lock); !errors.Is(rmErr, fs.ErrNotExist) {
				err = errors.Join(err, rmErr)
			}
			return err
		}, nil
	}
	return "", nil, fmt.Errorf("could not create a scratch directory in %s: every lock lola created was taken", parent)
}

// lockedAt reports whether fl, which holds its lock, holds it on the file at path, not on one
// another lola removed from there.
func lockedAt(fl *flock.Flock, path string) bool {
	held, err := fl.Stat()
	if err != nil {
		return false
	}
	at, err := os.Lstat(path)
	return err == nil && os.SameFile(held, at)
}

// sweepRuns removes the run directories under Paths.Staging last modified more than staleRun
// ago whose lock no live run holds (R-9-9, A28): what killed processes left behind; a live run
// waiting at a prompt for longer keeps its directory. It is best effort: what it cannot remove is a warning, never a refusal, and the next sweep tries again.
// lola cache add, which reports no notices, does not sweep.
func (e *Engine) sweepRuns() []Notice {
	es, err := os.ReadDir(e.Paths.Staging)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	var errs []error
	if err != nil {
		errs = append(errs, err)
	}
	for _, d := range es {
		if !d.IsDir() || !strings.HasPrefix(d.Name(), runPrefix) {
			continue
		}
		fi, err := d.Info()
		if errors.Is(err, fs.ErrNotExist) {
			continue // another lola removed it
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if e.Now().Sub(fi.ModTime()) <= staleRun {
			continue
		}
		dir := filepath.Join(e.Paths.Staging, d.Name())
		release, free, err := holdLock(dir + lockSuffix)
		if err != nil || !free {
			errs = append(errs, err)
			continue
		}
		errs = append(errs, os.RemoveAll(dir))
		release()
	}
	if err := errors.Join(errs...); err != nil {
		return []Notice{{Level: LevelWarn, Text: fmt.Sprintf("could not remove what a killed lola left in %s: %v; remove it by hand",
			e.Paths.Staging, untrusted.CleanErr(err))}}
	}
	return nil
}

// holdLock takes the lock file at path without waiting, never creating it: free is false when a
// live run holds it. A missing path, or one that is not a regular file, holds nothing (release is
// then a no-op).
func holdLock(path string) (release func(), free bool, err error) {
	none := func() {}
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) || err == nil && !fi.Mode().IsRegular() {
		return none, true, nil
	} else if err != nil {
		return nil, false, err
	}
	fl := flock.New(path, flock.SetFlag(os.O_RDONLY))
	ok, err := fl.TryLock()
	if errors.Is(err, fs.ErrNotExist) {
		return none, true, nil // its run ended between the two looks
	} else if err != nil || !ok {
		return nil, false, err
	}
	return func() { _ = fl.Unlock() }, true, nil
}
