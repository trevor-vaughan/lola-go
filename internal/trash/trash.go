// Package trash is lola's trash: one directory per run under $XDG_STATE_HOME/lola/trash, where
// lola moves or copies what it takes out of a destination, with a manifest.txt that says where
// each item came from. Only lola trash restores or empties it.
package trash

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Trash is one run's directory under $XDG_STATE_HOME/lola/trash: named after
// the UTC time and created exclusively, with one subdirectory per destination and
// manifest.txt, whose lines are original-path<TAB>trash-path. Each line is appended and synced
// before its copy or move, so the manifest names everything that may have left its place.
// Restore moves an entry back. run.yaml beside it records which command made the run and
// which copies may hold credentials.
type Trash struct {
	Dir         string
	manifest    *os.File
	used        bool
	rec         RunRecord
	now         time.Time
	credentials []string
	// rename is os.Rename; specs replace it to simulate a move across file systems.
	rename func(oldpath, newpath string) error
	// copyTree is copyFrom; specs replace it to change the original after the copy.
	copyTree func(parent *os.Root, name, to string) error
	// walk is walkEntry; specs replace it to change the original after the walk.
	walk func(parent *os.Root, name string) ([]string, map[string]bool, error)
}

// trashLayout is the timestamp format of a trash directory's name.
const trashLayout = "20060102T150405Z"

// RunRecord is what run.yaml says made a run: Command, the command line as lola's hints render it
// ("lola tidy --user"); Scope, "user" or "project"; Root, the project's root, "" at user scope.
type RunRecord struct {
	Command, Scope, Root string
}

// OpenTrash creates this run's trash directory under base, with manifest.txt and run.yaml (rec
// and now).
func OpenTrash(base string, now time.Time, rec RunRecord) (*Trash, error) {
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
		t := &Trash{Dir: dir, manifest: f, rec: rec, now: now, rename: os.Rename, copyTree: copyFrom, walk: walkEntry}
		if err := t.writeRun(); err != nil {
			return nil, fail(errors.Join(err, f.Close(), os.RemoveAll(dir)))
		}
		if err := errors.Join(syncDir(dir), syncDir(base)); err != nil {
			return nil, fail(errors.Join(err, f.Close()))
		}
		return t, nil
	}
}

// Used reports whether anything was copied or moved into the trash.
func (t *Trash) Used() bool { return t.used }

// MarkCredential records trashPath, a copy this run made, as one that may hold credentials (an MCP
// or settings file): Close lists it in run.yaml.
func (t *Trash) MarkCredential(trashPath string) {
	if !slices.Contains(t.credentials, trashPath) {
		t.credentials = append(t.credentials, trashPath)
	}
}

// Close closes manifest.txt and removes the trash directory when nothing was put in it; otherwise
// it rewrites run.yaml with the credential copies, when there are any.
func (t *Trash) Close() error {
	err := t.manifest.Close()
	switch {
	case !t.used:
		err = errors.Join(err, os.RemoveAll(t.Dir))
	case len(t.credentials) > 0:
		if werr := t.writeRun(); werr != nil {
			err = errors.Join(err, fmt.Errorf("cannot list the copies that may hold credentials in %s: %w",
				untrusted.Text(filepath.Join(t.Dir, runFileName), untrusted.MaxName), untrusted.CleanErr(werr)))
		}
	}
	return err
}

// runFileName is the run record's file in a run's directory.
const runFileName = "run.yaml"

// runFile is run.yaml as written and read. time is RFC 3339, UTC.
type runFile struct {
	Command     string   `yaml:"command"`
	Scope       string   `yaml:"scope"`
	Root        string   `yaml:"root"`
	Time        string   `yaml:"time"`
	Credentials []string `yaml:"credentials,omitempty"`
}

// writeRun replaces run.yaml atomically (replaceFile).
func (t *Trash) writeRun() error {
	b, err := yaml.Marshal(runFile{Command: t.rec.Command, Scope: t.rec.Scope, Root: t.rec.Root,
		Time: t.now.UTC().Format(time.RFC3339), Credentials: t.credentials})
	if err != nil {
		return err
	}
	return replaceFile(t.Dir, runFileName, b)
}

// replaceFile replaces name in dir with b atomically: a private temporary file in dir, synced,
// renamed over it, then dir synced.
func replaceFile(dir, name string, b []byte) error {
	f, err := os.CreateTemp(dir, "."+name+".tmp-*") // mode 0600
	if err != nil {
		return err
	}
	if err := writeCopy(bytes.NewReader(b), f); err != nil {
		return errors.Join(err, os.Remove(f.Name()))
	}
	if err := os.Rename(f.Name(), filepath.Join(dir, name)); err != nil {
		return errors.Join(err, os.Remove(f.Name()))
	}
	return syncDir(dir)
}

// Run is one trash run as ReadRun reads it back.
type Run struct {
	ID          string    // the run directory's name
	Time        time.Time // UTC: run.yaml's, else the one the directory's name says
	Command     string    // run.yaml's, or "unknown" for a run without a readable one
	Scope, Root string
	Credentials []string // the trash paths that may hold credentials
	Entries     []Entry  // manifest.txt's lines, in order, less those refused
	// Notices say what was refused or passed over: a malformed run.yaml, a manifest line or
	// credential path outside the run, an original that is not absolute. Paths in them are
	// escaped (untrusted.Text).
	Notices []string
}

// Entry is one manifest.txt line: where Original was put in the trash.
type Entry struct {
	Original, Trash string
	Credential      bool // Trash may hold credentials
}

// unknownCommand is a run's Command when run.yaml does not say.
const unknownCommand = "unknown"

// ReadRun reads the run at dir. Both files are untrusted data on disk: a run.yaml that does not
// parse is treated as absent, with a notice, and its unknown keys are ignored; a manifest line or
// credential path whose trash path does not lie inside dir, through real directories only, and a
// line whose original is not absolute, are refused with a notice. Only a manifest.txt that cannot
// be read is an error.
func ReadRun(dir string) (Run, error) {
	dir = filepath.Clean(dir)
	run := Run{ID: filepath.Base(dir), Command: unknownCommand, Credentials: []string{}, Entries: []Entry{}, Notices: []string{}}
	shown := func(p string) string { return untrusted.Text(p, untrusted.MaxName) }
	stamp, _, _ := strings.Cut(run.ID, "-")
	if at, err := time.Parse(trashLayout, stamp); err == nil {
		run.Time = at
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return Run{}, fmt.Errorf("cannot open the trash run %s: %w", shown(dir), untrusted.CleanErr(err))
	}
	defer func() { _ = root.Close() }()
	b, err := readRegular(root, runFileName)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		run.Notices = append(run.Notices, fmt.Sprintf("%s cannot be read, so the run's command is unknown: %v",
			shown(filepath.Join(dir, runFileName)), untrusted.CleanErr(err)))
	default:
		var rf runFile
		err := yaml.Unmarshal(b, &rf)
		var at time.Time
		if err == nil && rf.Time != "" {
			at, err = time.Parse(time.RFC3339, rf.Time)
		}
		if err != nil {
			run.Notices = append(run.Notices, fmt.Sprintf("%s is malformed, so the run's command is unknown: %v",
				shown(filepath.Join(dir, runFileName)), untrusted.CleanErr(err)))
			break
		}
		if rf.Command != "" {
			run.Command = rf.Command
		}
		if !at.IsZero() {
			run.Time = at.UTC()
		}
		run.Scope, run.Root = rf.Scope, rf.Root
		for _, c := range rf.Credentials {
			if why := outsideRun(dir, c); why != "" {
				run.Notices = append(run.Notices, fmt.Sprintf("run.yaml's credential copy %s is passed over: %s", shown(c), why))
				continue
			}
			run.Credentials = append(run.Credentials, filepath.Clean(c))
		}
	}
	m, err := readRegular(root, "manifest.txt")
	if err != nil {
		return Run{}, fmt.Errorf("cannot read the trash run's manifest.txt in %s: %w", shown(dir), untrusted.CleanErr(err))
	}
	for i, line := range strings.Split(strings.TrimSuffix(string(m), "\n"), "\n") {
		if line == "" {
			continue
		}
		orig, to, ok := strings.Cut(line, "\t")
		why := ""
		switch {
		case !ok:
			why = "it is not original<TAB>trash path"
		case !filepath.IsAbs(orig):
			why = "its original " + shown(orig) + " is not an absolute path"
		default:
			why = outsideRun(dir, to)
		}
		if why != "" {
			run.Notices = append(run.Notices, fmt.Sprintf("line %d of %s is refused: %s", i+1, shown(filepath.Join(dir, "manifest.txt")), why))
			continue
		}
		to = filepath.Clean(to)
		run.Entries = append(run.Entries, Entry{Original: orig, Trash: to, Credential: slices.Contains(run.Credentials, to)})
	}
	return run, nil
}

// maxFile caps what lola reads of a run's run.yaml and manifest.txt: both are untrusted data on
// disk, and a run lola wrote holds far less.
const maxFile = 1 << 20

// readRegular reads name in root, refusing anything but a regular file of at most maxFile bytes:
// a link or a pipe put there must not make a read wait or leave the run, nor a huge file exhaust
// memory.
func readRegular(root *os.Root, name string) ([]byte, error) {
	fi, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", name)
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxFile+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxFile {
		return nil, fmt.Errorf("%s is over 1 MiB", name)
	}
	return b, nil
}

// inRun is p, a trash path of the run at dir, relative to dir: it must be absolute and lie below
// dir, lexically. What lies between is checked by the os.Root on dir that reaches it.
func inRun(dir, p string) (string, error) {
	dir, clean := filepath.Clean(dir), filepath.Clean(p)
	if !filepath.IsAbs(p) || clean == dir || !within(dir, clean) {
		return "", errors.New("the trash path " + untrusted.Text(p, untrusted.MaxName) + " lies outside the run's directory")
	}
	return filepath.Rel(dir, clean)
}

// Measure counts what the run at dir holds at e.Trash, read through an os.Root on dir so that no
// link is followed and nothing outside the run is reached: its files and links, and its regular
// files' bytes. An entry that is gone is fs.ErrNotExist.
func Measure(dir string, e Entry) (files int, size int64, err error) {
	rel, err := inRun(dir, e.Trash)
	if err != nil {
		return 0, 0, err
	}
	root, err := os.OpenRoot(filepath.Clean(dir))
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = root.Close() }()
	fi, err := root.Lstat(rel)
	if err != nil {
		return 0, 0, err
	}
	if !fi.IsDir() {
		if fi.Mode().IsRegular() {
			size = fi.Size()
		}
		return 1, size, nil
	}
	err = fs.WalkDir(root.FS(), filepath.ToSlash(rel), func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		files++
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			size += info.Size()
		}
		return nil
	})
	return files, size, err
}

// Restore moves e, an entry of the run at dir as ReadRun read it, back to e.Original, making the
// directories above it, and never over what is there: an original that exists is refused
// (fs.ErrExist). The entry is reached again through an os.Root on dir, so a directory of the run
// swapped for a link since ReadRun cannot lead the move out of the run; a link is moved as a
// link. Across file systems the entry is copied through that root, then removed from the run.
// Every path credentials names at or below e.Trash (a credential copy, or a file inside a
// directory entry) is restored at mode 0600, unless it is a link, which is never changed; when
// that fails once the entry is back, the error is a *ModeError.
func Restore(dir string, e Entry, credentials []string) error {
	return restore(dir, e, credentials, os.Rename)
}

// ModeError is a restore that moved its entry back but could not make every credential copy
// there private (mode 0600): Paths[i] failed with Errs[i]. Every copy was tried.
type ModeError struct {
	Paths []string
	Errs  []error
}

// Failure says why Paths[i] is not private.
func (e *ModeError) Failure(i int) string {
	return fmt.Sprintf("cannot make %s private (mode 0600): %v", untrusted.Text(e.Paths[i], untrusted.MaxName), untrusted.CleanErr(e.Errs[i]))
}

func (e *ModeError) Error() string {
	out := make([]string, len(e.Paths))
	for i := range e.Paths {
		out[i] = e.Failure(i)
	}
	return strings.Join(out, "; ")
}

func (e *ModeError) Unwrap() []error { return e.Errs }

// restore is Restore with rename, os.Rename; specs replace it to restore across file systems.
func restore(dir string, e Entry, credentials []string, rename func(oldpath, newpath string) error) error {
	rel, err := inRun(dir, e.Trash)
	if err != nil {
		return err
	}
	run, err := os.OpenRoot(filepath.Clean(dir))
	if err != nil {
		return err
	}
	defer func() { _ = run.Close() }()
	parent, err := run.OpenRoot(filepath.Dir(rel))
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	name := filepath.Base(rel)
	if _, err := parent.Lstat(name); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(e.Original), 0o755); err != nil {
		return err
	}
	if _, err := os.Lstat(e.Original); err == nil {
		return fmt.Errorf("%s exists: %w", untrusted.Text(e.Original, untrusted.MaxName), fs.ErrExist)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	err = rename(filepath.Join(parent.Name(), name), e.Original)
	if errors.Is(err, syscall.EXDEV) {
		if err = copyFrom(parent, name, e.Original); err != nil {
			if _, ours := errors.AsType[*partialError](err); ours { // only a copy this run created is removed
				err = errors.Join(err, os.RemoveAll(e.Original))
			}
			return err
		}
		if err = syncTree(filepath.Dir(e.Original), e.Original); err == nil {
			err = parent.RemoveAll(name)
		}
		if err != nil { // the copy is complete at the original; the trash keeps its own
			return errs.WithHints(err, "the restored copy stays at "+untrusted.Text(e.Original, untrusted.MaxName)+
				" and the trash keeps its own; restoring again with --force moves that copy into a new trash run first")
		}
	}
	if err != nil {
		return err
	}
	var failed ModeError
	for _, c := range credentials {
		if c = filepath.Clean(c); within(e.Trash, c) {
			r, err := filepath.Rel(e.Trash, c)
			if err == nil {
				err = private(e.Original, r)
			}
			if err != nil {
				failed.Paths = append(failed.Paths, filepath.Join(e.Original, r))
				failed.Errs = append(failed.Errs, err)
			}
		}
	}
	if len(failed.Paths) > 0 {
		return &failed
	}
	return nil
}

// private sets the regular file at rel below top (top itself for ".") to mode 0600, reached
// through an os.Root and opened, then changed, only if it is the regular file Lstat saw: a link
// is left as it is, and no link leads the change out of top. One that is gone is passed over.
func private(top, rel string) error {
	dir, name := top, rel
	if rel == "." {
		dir, name = filepath.Dir(top), filepath.Base(top)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	fi, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil || !fi.Mode().IsRegular() {
		return err
	}
	f, err := root.Open(name)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	now, err := f.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(fi, now) {
		return fmt.Errorf("%s changed while lola restored it", untrusted.Text(filepath.Join(top, rel), untrusted.MaxName))
	}
	return f.Chmod(0o600)
}

// Forget rewrites the run at dir's manifest.txt without the lines of done, atomically; then, when
// it lists nothing and the run holds nothing but directories, manifest.txt and run.yaml, it
// removes the run and reports true. A line ReadRun refused is kept.
func Forget(dir string, done []Entry) (bool, error) {
	dir = filepath.Clean(dir)
	root, err := os.OpenRoot(dir)
	if err != nil {
		return false, err
	}
	defer func() { _ = root.Close() }()
	m, err := readRegular(root, "manifest.txt")
	if err != nil {
		return false, err
	}
	var kept strings.Builder
	for _, line := range strings.Split(strings.TrimSuffix(string(m), "\n"), "\n") {
		orig, to, ok := strings.Cut(line, "\t")
		if line == "" || ok && slices.ContainsFunc(done, func(e Entry) bool { return e.Original == orig && e.Trash == filepath.Clean(to) }) {
			continue
		}
		kept.WriteString(line + "\n")
	}
	if err := replaceFile(dir, "manifest.txt", []byte(kept.String())); err != nil {
		return false, err
	}
	if kept.Len() > 0 {
		return false, nil
	}
	return removeEmpty(root, dir)
}

// removeEmpty removes the run at dir, opened as root, when it holds nothing but directories,
// manifest.txt and run.yaml, and reports whether it did.
func removeEmpty(root *os.Root, dir string) (bool, error) {
	var dirs []string
	empty := true
	err := fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir():
			dirs = append(dirs, p)
		case p != "manifest.txt" && p != runFileName:
			empty = false
			return fs.SkipAll
		}
		return nil
	})
	if err != nil || !empty {
		return false, err
	}
	for _, name := range []string{"manifest.txt", runFileName} {
		if err := root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
	}
	for i := len(dirs) - 1; i > 0; i-- { // dirs[0] is "."
		if err := root.Remove(filepath.FromSlash(dirs[i])); err != nil {
			return false, err
		}
	}
	if err := os.Remove(dir); err != nil {
		return false, err
	}
	return true, syncDir(filepath.Dir(dir))
}

// outsideRun says why p is no path in the run at dir, a clean path, or "": p must be absolute
// and lie below dir, lexically, and every directory between them that exists must be a real
// directory, not a link that leads out.
func outsideRun(dir, p string) string {
	clean := filepath.Clean(p)
	if !filepath.IsAbs(p) || clean == dir || !within(dir, clean) {
		return "the trash path " + untrusted.Text(p, untrusted.MaxName) + " lies outside the run's directory"
	}
	rel, err := filepath.Rel(dir, filepath.Dir(clean))
	if err != nil {
		return untrusted.CleanErr(err).Error()
	}
	at := dir
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "." {
			break
		}
		at = filepath.Join(at, part)
		fi, err := os.Lstat(at)
		if err != nil {
			break // a missing directory leads nowhere; the entry is gone
		}
		if !fi.IsDir() {
			return "the trash path " + untrusted.Text(p, untrusted.MaxName) + " runs through " +
				untrusted.Text(at, untrusted.MaxName) + ", which is not a directory"
		}
	}
	return ""
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

// Move moves name, an entry of the directory parent, into dest's subdirectory and returns the
// trash path. parent's name is the path the caller opened it at, resolved or not: the manifest
// records the entry below it, and the rename uses it. Across file systems Move copies the entry, reading it
// through parent so no link is followed and no path is resolved again, syncs the copy, then,
// through parent, removes only the files and links that match the copy and the directories
// that are then empty. What remains was created or changed after the copy: it stays in place
// and Move returns the trash path with a *ChangedError naming it.
func (t *Trash) Move(dest, base string, parent *os.Root, name string) (string, error) {
	if name == "" || name == "." || name == ".." || !filepath.IsLocal(name) || filepath.Base(name) != name {
		return "", fmt.Errorf("%s is not one path component", untrusted.Text(name, untrusted.MaxName))
	}
	orig := filepath.Join(parent.Name(), name)
	to, err := t.record(dest, base, orig)
	if err != nil {
		return "", err
	}
	err = t.rename(orig, to)
	if errors.Is(err, syscall.EXDEV) {
		// The original goes only once the whole backup, its directory entries too, is on disk.
		if err = t.copyTree(parent, name, to); err == nil {
			if err = syncTree(t.Dir, to); err == nil {
				err := t.removeBackedUp(parent, name, to)
				if _, changed := errors.AsType[*ChangedError](err); err == nil || changed {
					return to, err
				}
				return to, fmt.Errorf("cannot remove %s after copying it into the trash, which holds the copy at %s: %w",
					untrusted.Text(orig, untrusted.MaxName), untrusted.Text(to, untrusted.MaxName), untrusted.CleanErr(err))
			}
		}
	}
	if err != nil {
		return "", copyFailed(orig, err)
	}
	return to, nil
}

// ChangedError is a move across file systems that left entries in place: they were created or
// changed after the copy, so the trash does not hold them as they are.
type ChangedError struct {
	Path string   // the moved entry
	Left []string // what stays, by path, sorted
}

func (e *ChangedError) Error() string {
	return fmt.Sprintf("%s changed while lola moved it: %d entries created or changed after the copy stay in place",
		untrusted.Text(e.Path, untrusted.MaxName), len(e.Left))
}

// walkEntry lists name, an entry of parent, and everything below it in walk order, marking the
// directories; a link is listed, never followed.
func walkEntry(parent *os.Root, name string) ([]string, map[string]bool, error) {
	dirs := map[string]bool{}
	fi, err := parent.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if !fi.IsDir() {
		return []string{name}, dirs, nil
	}
	var walked []string
	err = fs.WalkDir(parent.FS(), name, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		walked = append(walked, p)
		dirs[p] = d.IsDir()
		return nil
	})
	return walked, dirs, err
}

// removeBackedUp removes, through parent, the entry name wherever it matches its copy at to:
// files and links whose fingerprint matches, then directories, deepest first, that are empty.
// A directory left holding anything the walk did not list (made after it) counts as changed.
func (t *Trash) removeBackedUp(parent *os.Root, name, to string) error {
	backup, err := os.OpenRoot(filepath.Dir(to))
	if err != nil {
		return err
	}
	defer func() { _ = backup.Close() }()
	walked, dirs, err := t.walk(parent, name)
	if err != nil {
		return err
	}
	var left []string
	for i := len(walked) - 1; i >= 0; i-- {
		p := walked[i]
		fi, err := parent.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if dirs[p] && fi.IsDir() { // a directory swapped for a link is not opened through it when seen at Lstat
			f, err := parent.Open(p)
			if err != nil {
				return err
			}
			rest, err := f.Readdirnames(1)
			_ = f.Close()
			if err != nil && !errors.Is(err, io.EOF) {
				return err
			}
			if len(rest) > 0 {
				abs := filepath.Join(parent.Name(), p)
				if !slices.ContainsFunc(left, func(l string) bool { return strings.HasPrefix(l, abs+string(filepath.Separator)) }) {
					left = append(left, abs)
				}
				continue
			}
			if err := parent.Remove(p); err != nil {
				return err
			}
			continue
		}
		gone := false
		if fi.Mode()&fs.ModeSymlink != 0 || fi.Mode().IsRegular() {
			gone, err = UnlinkIfBackedUp(parent, backup, p)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
		}
		if !gone {
			left = append(left, filepath.Join(parent.Name(), p))
		}
	}
	if len(left) == 0 {
		return nil
	}
	slices.Sort(left)
	return &ChangedError{Path: filepath.Join(parent.Name(), name), Left: left}
}

// CopyBytes records orig as Copy does, then writes exactly data, never reading orig, to its
// trash path: a new file at mode 0600, synced. It returns the trash path.
func (t *Trash) CopyBytes(dest, base, orig string, data []byte) (string, error) {
	to, err := t.record(dest, base, orig)
	if err != nil {
		return "", err
	}
	out, err := os.OpenFile(to, newFile, 0o600)
	if err == nil {
		err = writeCopy(bytes.NewReader(data), out)
	}
	if err == nil {
		err = syncTree(t.Dir, to)
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
		return copyFile(f, fi, src, dst)
	case fi.IsDir():
		from, err := os.OpenRoot(src)
		if err != nil {
			return err
		}
		return errors.Join(copyDir(from, src, dst, fi.Mode().Perm()), from.Close())
	}
	return notCopyable(src)
}

// copyFrom is copyPath for name, an entry of parent, read through parent: no link on the way
// is followed and no path is resolved again.
func copyFrom(parent *os.Root, name, dst string) error {
	src := filepath.Join(parent.Name(), name)
	fi, err := parent.Lstat(name)
	if err != nil {
		return err
	}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		target, err := parent.Readlink(name)
		if err != nil {
			return err
		}
		return os.Symlink(target, dst)
	case fi.Mode().IsRegular():
		f, err := parent.Open(name)
		if err != nil {
			return err
		}
		return copyFile(f, fi, src, dst)
	case fi.IsDir():
		from, err := parent.OpenRoot(name)
		if err != nil {
			return err
		}
		return errors.Join(copyDir(from, src, dst, fi.Mode().Perm()), from.Close())
	}
	return notCopyable(src)
}

// copyFile copies f, opened for the regular file fi at src, to the new file dst with fi's
// permissions, and closes f. It refuses a file that is no longer the one fi describes.
func copyFile(f *os.File, fi fs.FileInfo, src, dst string) error {
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
	if err := writeCopy(f, out); err != nil {
		return &partialError{err: err}
	}
	return nil
}

// partialError is a copy that failed after it created its destination, which then holds a part
// of it that only this copy made.
type partialError struct{ err error }

func (e *partialError) Error() string { return e.err.Error() }
func (e *partialError) Unwrap() error { return e.err }

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

// copyDir copies the tree from, the directory at src, into the new directory dst, reading
// and writing through an os.Root on each, so no symbolic link is followed out of either.
func copyDir(from *os.Root, src, dst string, perm fs.FileMode) (err error) {
	if err := os.Mkdir(dst, perm|0o700); err != nil {
		return err
	}
	defer func() {
		if _, ok := errors.AsType[*partialError](err); err != nil && !ok {
			err = &partialError{err: err}
		}
	}()
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
// the trash) holds the same content at name: it was backed up with the
// directory, so it is unlinked rather than moved a second time. One that differs from its
// backup, or that the backup lacks, is kept (false). Both are read through os.Root, so no link
// is followed out.
func UnlinkIfBackedUp(dir, backup *os.Root, name string) (bool, error) {
	a, err := fingerprint(dir, name)
	if err != nil {
		return false, err
	}
	fi, err := backup.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !fi.Mode().IsRegular() && fi.Mode()&fs.ModeSymlink == 0 {
		return false, nil // the backup holds a directory or another kind there: it differs
	}
	b, err := fingerprint(backup, name)
	if err != nil || !bytes.Equal(a, b) {
		return false, err
	}
	return true, dir.Remove(name)
}

// within reports whether child is parent or lies below it, lexically.
func within(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && !filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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
