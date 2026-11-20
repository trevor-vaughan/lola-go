package engine

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/trash"
)

// ForcedFile is a file a step --force writes over or deletes (plan.Step.Forced unowned or
// hand-edited): Path is the step's path below its target's root, and File where the file really
// is: through the step's Via when that leaves the root, past the symbolic links in its parent
// directories.
type ForcedFile struct {
	Step       plan.Step
	Path, File string
}

// ForcedFiles lists the files steps at sc replace or delete under --force that exist, one per step:
// what the user had there, which a run copies into the trash before it writes (trashForced).
// Adopting, leaving absent or forgetting writes nothing over a file, and a member --force restores
// ("missing") replaces nothing of the user's.
func (e *Engine) ForcedFiles(sc Scope, steps []plan.Step) ([]ForcedFile, error) {
	var out []ForcedFile
	for _, s := range steps {
		if s.Forced != "unowned" && s.Forced != "hand-edited" ||
			s.Action == plan.ActionAdopt || s.Action == plan.ActionAbsent || s.Action == plan.ActionForget {
			continue
		}
		i := slices.IndexFunc(e.Targets, func(t target.Target) bool { return t.ID() == s.Target })
		if i < 0 {
			continue // no target of this run planned the step
		}
		f := ForcedFile{Step: s, Path: filepath.Join(e.Targets[i].Root(sc.Kind, sc.ProjectDir, e.Paths), filepath.FromSlash(s.Path))}
		file := cmp.Or(s.Via, f.Path)
		switch _, err := os.Lstat(file); {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			return nil, err
		}
		dir, err := filepath.EvalSymlinks(filepath.Dir(file))
		if err != nil {
			return nil, err
		}
		f.File = filepath.Join(dir, filepath.Base(file))
		out = append(out, f)
	}
	return out, nil
}

// trashDest is the subdirectory of a trash run sc's files go into ("user", or a hash of the
// project's key) and the resolved root below which their paths are mirrored.
func (e *Engine) trashDest(sc Scope) (dest, base string) {
	if sc.Kind == target.Project {
		sum := sha256.Sum256([]byte(sc.Key()))
		return hex.EncodeToString(sum[:8]), canonicalRoot(sc.ProjectDir)
	}
	return "user", canonicalRoot(e.Paths.Home)
}

// trashForced copies each file steps at sc replace or delete under --force (ForcedFiles) into the
// trash before the run writes: into opts.Trash when the caller keeps a run, else into a run of its
// own for sc, closed (and removed when empty) before it returns. A file a copy already holds
// (trash.Holds: the same file, or a directory above it the caller copied) is not copied again. A JSON
// member's whole file is copied and marked a credential copy. res gets the run and those copies.
func (e *Engine) trashForced(sc Scope, steps []plan.Step, opts Options, res *Result) (err error) {
	files, err := e.ForcedFiles(sc, steps)
	if err != nil || len(files) == 0 {
		return err
	}
	dest, base := e.trashDest(sc)
	t := opts.Trash
	if t == nil {
		rec := trash.RunRecord{Command: cmp.Or(opts.RenderRun, opts.ForceRun), Scope: sc.Kind.String()}
		if sc.Kind == target.Project {
			rec.Root = base
		}
		if t, err = trash.OpenTrash(e.Paths.Trash, e.Now(), rec); err != nil {
			return err
		}
		defer func() { err = errors.Join(err, t.Close()) }()
	}
	copied := 0
	for _, f := range files {
		to, ok := t.Holds(f.File)
		if !ok {
			if to, err = t.Copy(dest, base, f.File); err != nil {
				return err
			}
			copied++
		}
		if f.Step.Key != "" {
			t.MarkCredential(to)
			if !slices.Contains(res.TrashCredentials, to) {
				res.TrashCredentials = append(res.TrashCredentials, to)
			}
			shown := f.File
			if rel, err := filepath.Rel(base, f.File); err == nil && filepath.IsLocal(rel) {
				shown = filepath.ToSlash(rel)
			}
			if !slices.Contains(res.trashedCredentials, shown) {
				res.trashedCredentials = append(res.trashedCredentials, shown)
			}
		}
	}
	res.noteTrashed(t.Dir, copied)
	return nil
}

// noteTrashed records that n files were copied into the trash run dir; a run with none is not
// listed.
func (res *Result) noteTrashed(dir string, n int) {
	if n == 0 {
		return
	}
	if !slices.Contains(res.Trash, dir) {
		res.Trash = append(res.Trash, dir)
	}
	if res.trashed == nil {
		res.trashed = map[string]int{}
	}
	res.trashed[dir] += n
}

// addTrash adds the trash runs and credential copies of r, a run of the same command, to res.
func (res *Result) addTrash(r *Result) {
	for _, dir := range r.Trash {
		res.noteTrashed(dir, r.trashed[dir])
	}
	for _, c := range r.TrashCredentials {
		if !slices.Contains(res.TrashCredentials, c) {
			res.TrashCredentials = append(res.TrashCredentials, c)
		}
	}
	for _, c := range r.trashedCredentials {
		if !slices.Contains(res.trashedCredentials, c) {
			res.trashedCredentials = append(res.trashedCredentials, c)
		}
	}
}

// TrashedFiles is how many files the run copied into the trash run dir, one of Trash.
func (res *Result) TrashedFiles(dir string) int { return res.trashed[dir] }

// TrashedCredentialFiles is the file of each copy in TrashCredentials, once each: relative to its
// scope's root (slash-separated) when below it, else absolute.
func (res *Result) TrashedCredentialFiles() []string { return res.trashedCredentials }
