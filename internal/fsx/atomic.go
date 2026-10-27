// Package fsx holds lola's filesystem primitives: atomic writes, symlink-aware destination
// resolution, and byte-preserving edits of JSONC files.
package fsx

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// WriteFileAtomic writes data to a temp file in path's directory, fsyncs it, renames it over
// path and fsyncs the directory, so readers see either the old or the new content.
func WriteFileAtomic(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	committed = true
	return SyncDir(dir)
}

// ReadOwn reads the regular file at path, a file lola wrote in its own state, refusing a symbolic
// link, one swapped in while it is opened, and more than limit bytes.
func ReadOwn(path string, limit int64) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err == nil && !os.SameFile(before, fi) {
		err = errors.New("changed while lola was reading it")
	}
	var b []byte
	if err == nil {
		b, err = io.ReadAll(io.LimitReader(f, limit+1))
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("larger than %d bytes", limit)
	}
	return b, nil
}

// SyncDir fsyncs the directory dir, so a rename into it survives a crash.
func SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

// NormalizeMode reduces a source file mode to what lola stores and renders: 0755 when any
// execute bit is set, else 0644. Owner-specific bits never leak from an author's machine.
func NormalizeMode(m fs.FileMode) fs.FileMode {
	if m.Perm()&0o111 != 0 {
		return 0o755
	}
	return 0o644
}
