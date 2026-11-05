package engine

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/fsx"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/trash"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// tidyMove is one run's moves into the trash, which opens only when something moves (§6.5).
type tidyMove struct {
	s    *tidyScanner
	res  *TidyResult
	dest string // the trash subdirectory: user, or the hash of the project's key
	base string // the resolved scope root, below which moved paths are mirrored
	t    *trash.Trash
}

// open opens the run's trash directory once.
func (m *tidyMove) open() error {
	if m.t != nil {
		return nil
	}
	t, err := trash.OpenTrash(m.s.e.Paths.Trash, m.s.e.Now())
	if err != nil {
		return err
	}
	m.t = t
	return nil
}

// skip reports a chosen unit that does not move, with a notice.
func (m *tidyMove) skip(u TidyUnit, code, reason string) {
	u.Code, u.Reason = code, reason
	m.res.Skipped = append(m.res.Skipped, u)
	m.res.Notices = append(m.res.Notices, Notice{Level: LevelWarn, Text: u.Kind + "/" + u.Name + " (" + u.Rel + ") " + reason})
}

// apply moves chosen into the trash, checking each unit again first (§6.4): its destination
// resolves where it did, with no destination-level lock; the entry is the one the scan found;
// no unit-level lock applies now. The first failed move stops the run; it is returned.
func (s *tidyScanner) apply(chosen []TidyUnit, res *TidyResult) error {
	m := &tidyMove{s: s, res: res, dest: "user", base: canonicalRoot(s.e.Paths.Home)}
	if sc := s.req.Scope; sc.Kind == target.Project {
		sum := sha256.Sum256([]byte(sc.Key()))
		m.dest, m.base = hex.EncodeToString(sum[:8]), canonicalRoot(sc.ProjectDir)
	}
	now, err := s.e.newTidyScanner(s.ctx, s.req, s.targetIDs, &tidyKeep{})
	if err != nil {
		return err
	}
	now.protects = s.protects
	for _, d := range now.dests {
		now.rootLock(d)
	}
	fresh := map[*tidyDest]*tidyDest{}
	var touched []*tidyDest
	for _, c := range chosen {
		old := s.item(c.ID).dest
		if _, ok := fresh[old]; ok {
			continue
		}
		i := slices.IndexFunc(now.dests, func(d *tidyDest) bool {
			return d.target == old.target && d.kind == old.kind && d.logical == old.logical
		})
		if i < 0 {
			// The target declares another destination now (OpenCode's config file changed).
			gone := *old
			gone.code, gone.reason, gone.items, gone.parent = codeChanged, whyChanged, nil, nil
			fresh[old] = &gone
			continue
		}
		d := now.dests[i]
		now.destLock(d)
		if d.code == "" && d.abs != old.abs {
			d.code, d.reason = codeChanged, whyChanged
		}
		fresh[old] = d
		touched = append(touched, d)
	}
	for _, c := range chosen {
		it := s.item(c.ID)
		d := fresh[it.dest]
		if d.code != "" || d.mcp() {
			continue
		}
		again := &tidyItem{TidyUnit: it.TidyUnit, dest: d, disk: it.disk, rawName: it.rawName}
		if d.parent == nil {
			if d.parent, err = os.OpenRoot(d.abs); err != nil {
				d.code, d.reason = codeChanged, whyChanged
				continue
			}
			defer func(r *os.Root) { _ = r.Close() }(d.parent)
		}
		fi, err := d.parent.Lstat(it.disk)
		if err != nil || !os.SameFile(fi, it.fi) || fi.Mode().Type() != it.fi.Mode().Type() {
			again.lock(codeChanged, whyChanged)
		} else {
			again.fi = fi
			now.unitLocks(again)
		}
		d.items = append(d.items, again)
	}
	now.gitLocks(touched, s.req.includeTracked())
	err = m.units(chosen, fresh)
	if err == nil {
		err = m.servers(chosen, fresh)
	}
	if m.t != nil {
		closeErr := m.t.Close()
		if m.t.Used() {
			res.Trash = m.t.Dir
		}
		err = errors.Join(err, closeErr)
	}
	sortTidy(res.Moved)
	sortTidy(res.Skipped)
	return err
}

// item is the scan's unit with id.
func (s *tidyScanner) item(id UnitID) *tidyItem {
	for _, d := range s.dests {
		for _, it := range d.items {
			if it.ID == id {
				return it
			}
		}
	}
	return nil
}

// units moves the chosen files, directories and links whose re-check passed.
func (m *tidyMove) units(chosen []TidyUnit, fresh map[*tidyDest]*tidyDest) error {
	for _, c := range chosen {
		it := m.s.item(c.ID)
		d := fresh[it.dest]
		if d.mcp() {
			continue
		}
		if d.code != "" {
			m.skip(c, d.code, d.reason)
			continue
		}
		again := d.items[slices.IndexFunc(d.items, func(x *tidyItem) bool { return x.ID == c.ID })]
		if again.Code != "" {
			m.skip(c, again.Code, again.Reason)
			continue
		}
		if err := m.open(); err != nil {
			return err
		}
		to, err := m.t.Move(m.dest, m.base, d.parent, it.disk)
		if changed, ok := errors.AsType[*trash.ChangedError](err); ok {
			c.Trash = to
			m.skip(c, codeChanged, "changed while lola moved it; "+leftBehind(changed.Left)+" stay in place, the rest is in the trash")
			continue
		}
		if err != nil {
			return err
		}
		c.Trash = to
		m.res.Moved = append(m.res.Moved, c)
		m.res.SizeBytes += it.Size
	}
	return nil
}

// servers removes the chosen MCP members, one file at a time: the file must still be the
// regular file the scan read, inside the root; a member goes only when it still holds the value
// the scan recorded. The bytes read are copied into the trash first, at 0600, and the edit is
// written to the resolved path, so a linked file stays a link.
func (m *tidyMove) servers(chosen []TidyUnit, fresh map[*tidyDest]*tidyDest) error {
	var files []*tidyDest
	for _, c := range chosen {
		if d := m.s.item(c.ID).dest; d.mcp() && !slices.Contains(files, d) {
			files = append(files, d)
		}
	}
	for _, old := range files {
		var mine []TidyUnit
		for _, c := range chosen {
			if m.s.item(c.ID).dest == old {
				mine = append(mine, c)
			}
		}
		d := fresh[old]
		if d.code == "" {
			m.s.readServersAgain(d)
		}
		if d.code != "" {
			for _, c := range mine {
				m.skip(c, d.code, d.reason)
			}
			continue
		}
		edited := d.data
		var gone []TidyUnit
		for _, c := range mine {
			it := m.s.item(c.ID)
			i := slices.IndexFunc(d.items, func(x *tidyItem) bool { return x.ID.Key == c.ID.Key })
			switch {
			case i < 0 || !bytes.Equal(d.items[i].value, it.value):
				m.skip(c, codeChanged, whyChanged)
			case d.items[i].Code != "":
				m.skip(c, d.items[i].Code, d.items[i].Reason)
			default:
				out, err := fsx.JSONDeleteMember(edited, c.ID.Key)
				if err != nil {
					return err
				}
				edited = out
				gone = append(gone, c)
			}
		}
		if len(gone) == 0 {
			continue
		}
		if err := m.open(); err != nil {
			return err
		}
		to, err := m.t.CopyBytes(m.dest, m.base, d.abs, d.data)
		if err != nil {
			return err
		}
		m.res.TrashCredentials = append(m.res.TrashCredentials, to)
		m.res.SizeBytes += int64(len(d.data))
		if err := fsx.WriteFileAtomic(d.abs, edited, d.mode); err != nil {
			return errors.New("cannot write " + untrusted.Text(d.abs, untrusted.MaxName) + ": " + untrusted.CleanErr(err).Error())
		}
		for _, c := range gone {
			c.Trash = to
			m.res.Moved = append(m.res.Moved, c)
		}
	}
	return nil
}

// readServersAgain reads the MCP file d again at apply, locking its members as tracked when
// git says so: d.items become what the file holds now.
func (s *tidyScanner) readServersAgain(d *tidyDest) {
	s.readServers(d)
	if d.code == "" && !s.req.includeTracked() {
		s.gitLocks([]*tidyDest{d}, false)
	}
}

// leftBehind names the paths a move across file systems left in place, the first few of them.
func leftBehind(left []string) string {
	const shown = 3
	var names []string
	for _, l := range left[:min(len(left), shown)] {
		names = append(names, untrusted.Text(l, untrusted.MaxName))
	}
	out := strings.Join(names, ", ")
	if len(left) > shown {
		out += fmt.Sprintf(" and %d more", len(left)-shown)
	}
	return out
}
