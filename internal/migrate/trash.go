package migrate

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Trash is one run's directory under $XDG_STATE_HOME/lola/trash (design §6): named after
// the UTC time and created exclusively, with one subdirectory per destination and
// manifest.txt, whose lines are original-path<TAB>trash-path. Each line is appended and synced
// before its copy or move, so the manifest names everything that may have left its place.
// Restoring is a manual move back.
type Trash struct {
	Dir      string
	manifest *os.File
	used     bool
	// rename is os.Rename; specs replace it to simulate a move across file systems.
	rename func(oldpath, newpath string) error
}

// trashLayout is the timestamp format of a trash directory's name.
const trashLayout = "20060102T150405Z"

// OpenTrash creates this run's trash directory under base.
func OpenTrash(base string, now time.Time) (*Trash, error) {
	fail := func(err error) error {
		return errs.WithHints(fmt.Errorf("cannot create the trash directory under %s: %w", base, untrusted.CleanErr(err)),
			"make "+base+" a writable directory, then rerun; nothing was changed")
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, fail(err)
	}
	name := now.UTC().Format(trashLayout)
	for i := 1; ; i++ {
		dir := filepath.Join(base, name)
		if i > 1 {
			dir += fmt.Sprintf("-%d", i)
		}
		err := os.Mkdir(dir, 0o700)
		if errors.Is(err, fs.ErrExist) && i < 1000 {
			continue
		}
		if err != nil {
			return nil, fail(err)
		}
		f, err := os.OpenFile(filepath.Join(dir, "manifest.txt"), os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND, 0o600)
		if err != nil {
			return nil, fail(errors.Join(err, os.Remove(dir)))
		}
		if err := errors.Join(syncDir(dir), syncDir(base)); err != nil {
			return nil, fail(errors.Join(err, f.Close()))
		}
		return &Trash{Dir: dir, manifest: f, rename: os.Rename}, nil
	}
}

// Used reports whether anything was copied or moved into the trash.
func (t *Trash) Used() bool { return t.used }

// Close closes manifest.txt and removes the trash directory when nothing was put in it.
func (t *Trash) Close() error {
	err := t.manifest.Close()
	if !t.used {
		err = errors.Join(err, os.RemoveAll(t.Dir))
	}
	return err
}

// Copy backs orig up into dest's subdirectory, leaving it in place: a file, a symbolic link
// (as a link) or a directory with everything in it. It returns the trash path.
func (t *Trash) Copy(dest, base, orig string) (string, error) {
	to, err := t.record(dest, base, orig)
	if err != nil {
		return "", err
	}
	if err := copyPath(orig, to); err != nil {
		return "", copyFailed(orig, err)
	}
	if err := syncTree(t.Dir, to); err != nil {
		return "", copyFailed(orig, err)
	}
	return to, nil
}

// copyFailed names the path a failed copy or move was for, with the cause stripped of the
// untrusted names it carries.
func copyFailed(orig string, err error) error {
	return fmt.Errorf("cannot move %s into the trash: %w", untrusted.Text(orig, untrusted.MaxName), untrusted.CleanErr(err))
}

// Move moves orig into dest's subdirectory and returns the trash path. Across file systems it
// copies, syncs, then removes the original.
func (t *Trash) Move(dest, base, orig string) (string, error) {
	to, err := t.record(dest, base, orig)
	if err != nil {
		return "", err
	}
	err = t.rename(orig, to)
	if errors.Is(err, syscall.EXDEV) {
		// The original goes only once the whole backup, its directory entries too, is on disk.
		if err = copyPath(orig, to); err == nil {
			if err = syncTree(t.Dir, to); err == nil {
				err = os.RemoveAll(orig)
			}
		}
	}
	if err != nil {
		return "", copyFailed(orig, err)
	}
	return to, nil
}

// record picks orig's trash path in dest's subdirectory (its path relative to base, or under
// _outside when it is not below base), refuses one that exists, makes its parent and appends
// and syncs the manifest line.
func (t *Trash) record(dest, base, orig string) (string, error) {
	if strings.ContainsAny(orig, "\t\n\r") {
		return "", fmt.Errorf("%s cannot be recorded in manifest.txt: its path holds a tab or line break", untrusted.Text(orig, untrusted.MaxName))
	}
	rel := "_outside" + string(filepath.Separator) + strings.TrimLeft(orig[len(filepath.VolumeName(orig)):], `/\`)
	if r, err := filepath.Rel(base, orig); err == nil && within(base, orig) && r != "." {
		rel = r
	}
	to := filepath.Join(t.Dir, dest, rel)
	if err := t.makeParents(filepath.Dir(to)); err != nil {
		return "", err
	}
	if _, err := os.Lstat(to); err == nil {
		return "", fmt.Errorf("trash path %s already exists", untrusted.Text(to, untrusted.MaxName))
	}
	if _, err := t.manifest.WriteString(orig + "\t" + to + "\n"); err != nil {
		return "", untrusted.CleanErr(err)
	}
	if err := t.manifest.Sync(); err != nil {
		return "", untrusted.CleanErr(err)
	}
	t.used = true
	return to, nil
}

// makeParents creates the directories from t.Dir down to dir, refusing one that is not a real
// directory: a link copied into the trash must not steer a later write out of it.
func (t *Trash) makeParents(dir string) error {
	rel, err := filepath.Rel(t.Dir, dir)
	if err != nil {
		return err
	}
	p := t.Dir
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		p = filepath.Join(p, part)
		fi, err := os.Lstat(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if err := os.Mkdir(p, 0o700); err != nil {
				return untrusted.CleanErr(err)
			}
		case err != nil:
			return untrusted.CleanErr(err)
		case !fi.IsDir():
			return errs.WithHints(fmt.Errorf("%s in the trash is not a directory", untrusted.Text(p, untrusted.MaxName)),
				"it was copied into the trash as a link or file; nothing was moved")
		}
	}
	return nil
}

// syncTree makes the copy at to durable beyond its files: the directory entries of every
// directory under to and of each parent up to top.
func syncTree(top, to string) error {
	for d := filepath.Dir(to); ; d = filepath.Dir(d) {
		if err := syncDir(d); err != nil {
			return err
		}
		if d == top {
			break
		}
	}
	return filepath.WalkDir(to, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		return syncDir(p)
	})
}

// copyPath copies src to dst, which must not exist: a regular file with its permissions, a
// symbolic link as a link, or a directory tree read through an os.Root, so nothing outside src
// is reached. Every file is synced. Anything else is refused.
func copyPath(src, dst string) error {
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(target, dst)
	case fi.Mode().IsRegular():
		f, err := os.Open(src)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		now, err := f.Stat()
		if err != nil {
			return err
		}
		if !os.SameFile(fi, now) {
			return fmt.Errorf("%s changed while lola copied it", untrusted.Text(src, untrusted.MaxName))
		}
		out, err := os.OpenFile(dst, newFile, fi.Mode().Perm())
		if err != nil {
			return err
		}
		return writeCopy(f, out)
	case fi.IsDir():
		return copyDir(src, dst, fi.Mode().Perm())
	}
	return notCopyable(src)
}

func notCopyable(p string) error {
	return fmt.Errorf("%s is neither a file, a directory nor a symbolic link", untrusted.Text(p, untrusted.MaxName))
}

// newFile is how a copy opens the file it writes: it never overwrites one.
const newFile = os.O_WRONLY | os.O_CREATE | os.O_EXCL

// writeCopy writes r to out, a new file, syncs and closes it.
func writeCopy(r io.Reader, out *os.File) error {
	_, err := io.Copy(out, r)
	if err == nil {
		err = out.Sync()
	}
	return errors.Join(err, out.Close())
}

// copyDir copies the tree at src into the new directory dst, reading and writing through an
// os.Root on each, so no symbolic link is followed out of either.
func copyDir(src, dst string, perm fs.FileMode) (err error) {
	from, err := os.OpenRoot(src)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, from.Close()) }()
	if err := os.Mkdir(dst, perm|0o700); err != nil {
		return err
	}
	to, err := os.OpenRoot(dst)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, to.Close()) }()
	return fs.WalkDir(from.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == "." {
			return err
		}
		fi, err := from.Lstat(p)
		if err != nil {
			return err
		}
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			target, err := from.Readlink(p)
			if err != nil {
				return err
			}
			return to.Symlink(target, p)
		case fi.IsDir():
			return to.Mkdir(p, fi.Mode().Perm()|0o700)
		case fi.Mode().IsRegular():
			f, err := from.OpenFile(p, os.O_RDONLY, 0)
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			if now, err := f.Stat(); err != nil || !os.SameFile(fi, now) {
				return errors.Join(err, fmt.Errorf("%s changed while lola copied it", untrusted.Text(filepath.Join(src, p), untrusted.MaxName)))
			}
			out, err := to.OpenFile(p, newFile, fi.Mode().Perm())
			if err != nil {
				return err
			}
			return writeCopy(f, out)
		}
		return notCopyable(filepath.Join(src, p))
	})
}

// UnlinkIfBackedUp removes name, a file or symbolic link in dir, when backup (dir's copy in
// the trash) holds the same content at name (§12.8): it was backed up with the directory, so it
// is unlinked rather than moved a second time. One that differs from its backup, or that the
// backup lacks, is kept (false). Both are read through os.Root, so no link is followed out.
func UnlinkIfBackedUp(dir, backup *os.Root, name string) (bool, error) {
	a, err := fingerprint(dir, name)
	if err != nil {
		return false, err
	}
	b, err := fingerprint(backup, name)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil || !bytes.Equal(a, b) {
		return false, err
	}
	return true, dir.Remove(name)
}

// fingerprint identifies a file by its content's sha256, a symbolic link by its target.
func fingerprint(root *os.Root, name string) ([]byte, error) {
	fi, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		target, err := root.Readlink(name)
		return []byte("link:" + target), err
	case fi.Mode().IsRegular():
		f, err := root.Open(name)
		if err != nil {
			return nil, err
		}
		defer func() { _ = f.Close() }()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return nil, err
		}
		return append([]byte("file:"), h.Sum(nil)...), nil
	}
	return nil, notCopyable(filepath.Join(root.Name(), name))
}
