package engine

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/fsx"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Lock codes (Plan 6d §4.4): destination-level ones skip a whole destination; unit-level ones
// protect one unit. Hard locks yield to nothing; tracked yields to --include-tracked or --force,
// kept to --unkeep.
const (
	codeLinkOut          = "link_out"
	codeLinkOverlap      = "link_overlap"
	codeTrackedLink      = "tracked_link"
	codeOtherProject     = "other_project"
	codeUnparseable      = "unparseable"
	codeNotRegular       = "not_regular"
	codeDuplicateKey     = "duplicate_key"
	codeInstructionFile  = "instruction_file"
	codeReservedName     = "reserved_name"
	codeNotAFile         = "not_a_file"
	codeUnreadable       = "unreadable"
	codeUnrecordableName = "unrecordable_name"
	codeUnselectableName = "unselectable_name"
	codeTracked          = "tracked"
	codeKept             = "kept"
	codeChanged          = "changed"
)

// Reasons of the unit-level locks, as listings and refusals print them after the unit's name.
const (
	whyInstructionFile = "is or holds an instruction file (CLAUDE.md, AGENTS.md or GEMINI.md)"
	whyReservedName    = "is named like git's or lola's own directory (.git, .lola)"
	whyNotAFile        = "is or holds a fifo, socket or device"
	whyUnrecordable    = "has a tab or line break in its path, which the trash's manifest.txt cannot record; rename it, then rerun"
	whyUnselectable    = "has a name no selector names exactly (a control, format or invisible character, or over 128 characters)"
	whyTracked         = "tracked by git"
	whyUntold          = "git could not say whether it is tracked"
	whyChanged         = "changed since lola looked; run lola tidy again"
	whyAtRoot          = "leads to the root or above it"
)

// instructionFiles are the names tidy never moves, nor anything holding one (U2, R-6d-3).
var instructionFiles = []string{"claude.md", "agents.md", "gemini.md"}

// tidyDest is one declared destination of one target: a directory whose direct entries are
// units, or a JSON file whose server map's direct members are.
type tidyDest struct {
	target, kind    string
	logical, ext    string // root-relative, slash-separated; the extension a file unit's name loses
	pointer         string // the server map's JSON Pointer; "" for a directory
	root, realRoot  string // the target's root, as given and resolved
	abs             string // resolved
	outside, exists bool
	code, reason    string // a destination-level lock
	links           []string
	data            []byte      // an MCP file's bytes as read
	mode            fs.FileMode // an MCP file's permissions
	parent          *os.Root    // at apply, the resolved directory, opened once
	items           []*tidyItem
}

func (d *tidyDest) mcp() bool { return d.pointer != "" }

// dir is the resolved directory holding d's units: d itself, or an MCP file's parent.
func (d *tidyDest) dir() string {
	if d.mcp() {
		return filepath.Dir(d.abs)
	}
	return d.abs
}

// tidyItem is one unit as the scan found it.
type tidyItem struct {
	TidyUnit
	dest       *tidyDest
	disk       string      // the entry's on-disk name, before Ext is stripped
	rawName    string      // the unit's name, before untrusted.Text
	fi         fs.FileInfo // as the scan found it, for §6.4's re-check
	value      []byte      // an MCP member's canonical JSON
	owner      string      // the module that owns it
	ownerScope Scope       // the scope whose ledger records it
	gitCause   string      // why git could not say whether it is tracked
	keptBy     config.Pattern
}

func (it *tidyItem) lock(code, reason string) {
	if it.Code == "" {
		it.Code, it.Reason = code, reason
	}
}

// tidyLedger is a ledger whose artifacts count as owned, with the scope its paths are under.
type tidyLedger struct {
	sc  Scope
	led *ledger.Ledger
}

// tidyScanner scans one scope's destinations (§6.2) and moves what is chosen (§6.4).
type tidyScanner struct {
	e         *Engine
	ctx       context.Context
	req       TidyRequest
	targetIDs []string
	keep      []config.Pattern // the entries the scan honors
	keepHeld  []config.Pattern // the entries keep.yaml holds
	dests     []*tidyDest
	owned     []*tidyItem
	notices   []Notice
	hints     []string
	protects  []string // lola's own locations and the root's .git and .lola, as written and resolved
}

// newTidyScanner resolves every declared destination of targets at the request's scope.
func (e *Engine) newTidyScanner(ctx context.Context, req TidyRequest, targets []string, keep *tidyKeep) (*tidyScanner, error) {
	s := &tidyScanner{e: e, ctx: ctx, req: req, targetIDs: targets, keep: keep.entries, keepHeld: keep.held}
	dests, err := e.tidyDests(req.Scope, targets)
	if err != nil {
		return nil, err
	}
	s.dests = dests
	for _, l := range []string{e.Paths.ConfigHome, e.Paths.StateHome, e.Paths.CacheHome, e.Paths.DataHome} {
		s.protect(filepath.Join(l, "lola"))
	}
	roots := map[string]bool{}
	for _, d := range dests {
		roots[d.root] = true
	}
	for root := range roots {
		s.protect(filepath.Join(root, ".lola"))
		gitPath := filepath.Join(root, ".git")
		s.protect(gitPath)
		if b, err := os.ReadFile(gitPath); err == nil {
			if dir, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:"); ok {
				dir = strings.TrimSpace(dir)
				if !filepath.IsAbs(dir) {
					dir = filepath.Join(root, dir)
				}
				s.protect(dir)
			}
		}
	}
	return s, nil
}

// protect adds a location tidy never reaches, as written and, when it exists, as resolved.
func (s *tidyScanner) protect(p string) {
	s.protects = append(s.protects, filepath.Clean(p))
	if real, err := filepath.EvalSymlinks(p); err == nil {
		s.protects = append(s.protects, real)
	}
}

// tidyDests resolves the declared destinations of targets at sc.
func (e *Engine) tidyDests(sc Scope, targets []string) ([]*tidyDest, error) {
	var out []*tidyDest
	for _, id := range targets {
		t, err := e.target(id)
		if err != nil {
			return nil, err
		}
		root := t.Root(sc.Kind, sc.ProjectDir, e.Paths)
		decl, err := t.Destinations(sc.Kind, root)
		if err != nil {
			return nil, err
		}
		realRoot := canonicalRoot(root)
		var ds []*tidyDest
		for _, d := range decl.Dirs {
			ds = append(ds, &tidyDest{target: id, kind: d.Kind, logical: d.Path, ext: d.Ext, root: root, realRoot: realRoot})
		}
		for _, m := range decl.Servers {
			ds = append(ds, &tidyDest{target: id, kind: config.KindMCP, logical: m.File, pointer: m.Pointer, root: root, realRoot: realRoot})
		}
		for _, d := range ds {
			r, err := fsx.Resolve(root, filepath.FromSlash(d.logical))
			if err != nil {
				return nil, fmt.Errorf("cannot resolve %s: %w", untrusted.Text(filepath.Join(root, d.logical), untrusted.MaxName), untrusted.CleanErr(err))
			}
			d.abs, d.outside = r.Abs, r.Outside
			_, err = os.Lstat(d.abs)
			d.exists = err == nil
		}
		out = append(out, ds...)
	}
	return out, nil
}

// overlapsUser reports whether a project destination and a user-scope one are one place or one
// holds the other (a project at $HOME, or a link between them): the user ledger then counts too
// (§4.3). A user destination that does not resolve is an error: tidy cannot rule the overlap out.
func (s *tidyScanner) overlapsUser() (bool, error) {
	user, err := s.e.tidyDests(Scope{Kind: target.User}, s.targetIDs)
	if err != nil {
		return false, err
	}
	for _, d := range s.dests {
		if slices.ContainsFunc(user, func(u *tidyDest) bool { return overlaps(u.abs, d.abs) }) {
			return true, nil
		}
	}
	return false, nil
}

// within reports whether child is parent or lies below it.
func within(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// overlaps reports whether a and b are one place or one holds the other.
func overlaps(a, b string) bool { return within(a, b) || within(b, a) }

// scan finds the units of every destination and protects what §4.4 protects; ledgers' artifacts
// count as owned.
func (s *tidyScanner) scan(ledgers []tidyLedger) TidyScan {
	for _, d := range s.dests {
		s.rootLock(d)
	}
	for _, d := range s.dests {
		s.destLock(d)
	}
	owned := s.artifacts(ledgers)
	for _, d := range s.dests {
		if d.code != "" || !d.exists {
			continue
		}
		if d.mcp() {
			s.readServers(d)
		} else {
			s.readDir(d)
		}
		for _, it := range d.items {
			if m, sc := owned.owner(it); m != "" {
				it.owner, it.ownerScope = m, sc
				s.owned = append(s.owned, it)
				continue
			}
			s.unitLocks(it)
		}
		d.items = slices.DeleteFunc(d.items, func(it *tidyItem) bool { return it.owner != "" })
	}
	s.gitLocks(s.dests, s.req.includeTracked())
	for _, d := range s.dests {
		if d.code != "" {
			continue
		}
		for _, it := range d.items {
			if it.Code != "" {
				continue
			}
			for _, k := range s.keep {
				if k.Matches(it.Kind, it.Name) {
					it.keptBy = k
					it.lock(codeKept, "on your keep-list ("+untrusted.Text(k.String(), untrusted.MaxName)+")")
					break
				}
			}
		}
	}
	return s.result()
}

// result is the scan's lists, sorted, with a notice for each destination skipped.
func (s *tidyScanner) result() TidyScan {
	out := TidyScan{Candidates: []TidyUnit{}, Protected: []TidyUnit{}, Skipped: []TidyUnit{}}
	for _, d := range s.dests {
		if d.code != "" {
			if d.exists {
				out.Skipped = append(out.Skipped, d.skipped())
			}
			continue
		}
		for _, it := range d.items {
			if it.Code == "" {
				out.Candidates = append(out.Candidates, it.TidyUnit)
			} else {
				out.Protected = append(out.Protected, it.TidyUnit)
			}
		}
	}
	sortTidy(out.Candidates)
	sortTidy(out.Protected)
	sortTidy(out.Skipped)
	return out
}

// protected is every unit the scan locked.
func (s *tidyScanner) protected() []*tidyItem {
	var out []*tidyItem
	for _, d := range s.dests {
		if d.code != "" {
			continue
		}
		for _, it := range d.items {
			if it.Code != "" {
				out = append(out, it)
			}
		}
	}
	return out
}

// skipped is d as a Skipped item.
func (d *tidyDest) skipped() TidyUnit {
	return TidyUnit{Target: d.target, Kind: d.kind, Path: filepath.Join(d.root, filepath.FromSlash(d.logical)),
		Rel: untrusted.Text(d.logical, untrusted.MaxName), Code: d.code, Reason: d.reason}
}

// skip locks the whole destination d, with a notice; a hint, when given, goes with it.
func (s *tidyScanner) skip(d *tidyDest, code, reason, notice string, hint ...string) {
	if d.code != "" {
		return
	}
	d.code, d.reason = code, reason
	if d.exists {
		s.notices = append(s.notices, Notice{Level: LevelWarn, Text: untrusted.Text(d.logical, untrusted.MaxName) + " " + notice + "; lola tidy skips it"})
		s.hints = append(s.hints, hint...)
	}
}

// rootLock skips d when it resolves onto its root, above it or outside it (§4.4): such a
// destination is no place another one can lie in.
func (s *tidyScanner) rootLock(d *tidyDest) {
	switch {
	case d.abs == d.realRoot || within(d.abs, d.realRoot):
		s.skip(d, codeLinkOverlap, whyAtRoot, whyAtRoot)
	case d.outside:
		s.skip(d, codeLinkOut, "leads outside the root", "leads outside "+untrusted.Text(d.realRoot, untrusted.MaxName))
	}
}

// destLock applies the destination-level locks that need no git (§4.4), after rootLock has
// run on every destination: d overlapping another destination that lies inside the root, git's
// or lola's own files, or another lola project.
func (s *tidyScanner) destLock(d *tidyDest) {
	if d.code != "" {
		return
	}
	for _, o := range s.dests {
		if o != d && o.code != codeLinkOut && o.reason != whyAtRoot && overlaps(d.abs, o.abs) {
			s.skip(d, codeLinkOverlap, "leads to another destination", "leads to where "+untrusted.Text(o.logical, untrusted.MaxName)+" is")
			return
		}
	}
	for _, p := range s.protects {
		if overlaps(d.abs, p) {
			s.skip(d, codeLinkOverlap, "leads to git's or lola's own files", "leads to git's or lola's own files")
			return
		}
	}
	if other := s.otherProject(d); other != "" {
		r := s.req
		r.Scope, r.Select, r.All, r.Yes, r.Force = Scope{Kind: target.Project, ProjectDir: other}, nil, false, false, false
		s.skip(d, codeOtherProject, "inside another lola project",
			"is inside the lola project "+untrusted.Text(other, untrusted.MaxName), s.e.tidyRun(r, nil))
		return
	}
	d.links = s.linksOnTheWay(d)
}

// otherProject is the root of a lola project, other than this scope's, that d lies in: walking
// up from d, a directory holding .lola below the target's root (R-6d-S9, R-6d-N4). The root
// itself never counts: at user scope your home is never a found project (R-6b3c-9).
func (s *tidyScanner) otherProject(d *tidyDest) string {
	for p := d.dir(); within(d.realRoot, p) && p != d.realRoot; p = filepath.Dir(p) {
		if dirExists(filepath.Join(p, ".lola")) {
			return p
		}
	}
	return ""
}

// linksOnTheWay are the symbolic links below the root on the way to d, each by its path through
// its resolved parent.
func (s *tidyScanner) linksOnTheWay(d *tidyDest) []string {
	var out []string
	parent, p := d.realRoot, d.root
	for _, c := range strings.Split(d.logical, "/") {
		p = filepath.Join(p, c)
		fi, err := os.Lstat(p)
		if err != nil {
			break
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			out = append(out, filepath.Join(parent, c))
		}
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			break
		}
		parent = real
	}
	return out
}

// readDir lists d's direct entries as units (§4.2), never following a link.
func (s *tidyScanner) readDir(d *tidyDest) {
	fi, err := os.Stat(d.abs)
	if err != nil || !fi.IsDir() {
		return
	}
	entries, err := os.ReadDir(d.abs)
	if err != nil {
		s.skip(d, codeUnreadable, "cannot be read", "cannot be read: "+untrusted.CleanErr(err).Error())
		return
	}
	for _, en := range entries {
		p := filepath.Join(d.abs, en.Name())
		fi, err := os.Lstat(p)
		if err != nil {
			continue // gone since the listing
		}
		name := en.Name()
		if fi.Mode().IsRegular() || fi.Mode()&fs.ModeSymlink != 0 {
			if base, ok := strings.CutSuffix(name, d.ext); ok && d.ext != "" && base != "" {
				name = base
			}
		}
		it := &tidyItem{dest: d, disk: en.Name(), fi: fi}
		it.TidyUnit = TidyUnit{Target: d.target, Kind: d.kind, Name: untrusted.Text(name, untrusted.MaxName), Path: p,
			Rel: untrusted.Text(path.Join(d.logical, en.Name()), untrusted.MaxName), ID: UnitID{Target: d.target, Kind: d.kind, Path: p}}
		if fi.Mode()&fs.ModeSymlink != 0 {
			if to, err := os.Readlink(p); err == nil {
				it.Link = untrusted.Text(to, untrusted.MaxName)
			}
		}
		if fi.Mode().IsRegular() {
			it.Size = fi.Size()
		}
		it.Dir = fi.IsDir()
		it.rawName = name
		d.items = append(d.items, it)
	}
}

// readServers lists the direct members of d's server map as units (§4.5), skipping a file that
// is not a regular file, does not parse or names a server twice.
func (s *tidyScanner) readServers(d *tidyDest) {
	fi, err := os.Stat(d.abs)
	if err != nil || !fi.Mode().IsRegular() {
		s.skip(d, codeNotRegular, "not a regular file", "is not a regular file")
		return
	}
	b, err := os.ReadFile(d.abs)
	if err != nil {
		s.skip(d, codeUnparseable, "cannot be read", "cannot be read: "+untrusted.CleanErr(err).Error())
		return
	}
	d.data, d.mode = b, fi.Mode().Perm()
	if v, found, err := fsx.JSONGet(b, d.pointer); err != nil {
		s.skip(d, codeUnparseable, "does not parse as JSON", "does not parse as JSON")
		return
	} else if found && !bytes.HasPrefix(v, []byte("{")) {
		s.skip(d, codeUnparseable, "its server map is not an object", "holds "+d.pointer+", which is not an object")
		return
	}
	members, _, err := fsx.JSONMembers(b, d.pointer)
	if err != nil {
		s.skip(d, codeUnparseable, "does not parse as JSON", "does not parse as JSON")
		return
	}
	seen := map[string]bool{}
	for _, m := range members {
		if seen[m.Name] {
			s.skip(d, codeDuplicateKey, "names a server twice", "names the server "+untrusted.Text(m.Name, untrusted.MaxName)+" twice")
			d.items = nil
			return
		}
		seen[m.Name] = true
		key := d.pointer + "/" + strings.ReplaceAll(strings.ReplaceAll(m.Name, "~", "~0"), "/", "~1")
		it := &tidyItem{dest: d, disk: m.Name, value: m.Value, rawName: m.Name}
		it.TidyUnit = TidyUnit{Target: d.target, Kind: config.KindMCP, Name: untrusted.Text(m.Name, untrusted.MaxName), Path: d.abs,
			Key: untrusted.Text(key, untrusted.MaxName), Rel: untrusted.Text(d.logical, untrusted.MaxName), Label: serverLabel(m.Value),
			ID: UnitID{Target: d.target, Kind: config.KindMCP, Path: d.abs, Key: key}}
		d.items = append(d.items, it)
	}
}

// ownedPaths are the ledgers' artifacts, resolved, for §4.3's ownership test.
type ownedPaths []struct {
	abs, logical, key, module string
	sc                        Scope
}

// artifacts resolves every artifact of ledgers under its scope's target roots.
func (s *tidyScanner) artifacts(ledgers []tidyLedger) ownedPaths {
	var out ownedPaths
	for _, l := range ledgers {
		mods := make([]string, 0, len(l.led.Modules))
		for m := range l.led.Modules {
			mods = append(mods, m)
		}
		slices.Sort(mods)
		for _, m := range mods {
			for _, a := range l.led.Modules[m].Artifacts {
				t, err := s.e.target(a.Target)
				if err != nil {
					continue
				}
				root := t.Root(l.sc.Kind, l.sc.ProjectDir, s.e.Paths)
				logical := filepath.Join(root, filepath.FromSlash(a.Path))
				abs := logical // an artifact that no longer resolves still owns its logical path (fail closed)
				if r, err := fsx.Resolve(root, filepath.FromSlash(a.Path)); err == nil {
					abs = r.Abs
				}
				out = append(out, struct {
					abs, logical, key, module string
					sc                        Scope
				}{abs, logical, a.Key, m, l.sc})
			}
		}
	}
	return out
}

// owner is the module whose artifact lies at or below it, "" for none, compared both by the
// unit's path through its parent and by its logical path below the root, so a link lola writes
// through is owned: for an MCP unit, the module that records its file and pointer.
func (o ownedPaths) owner(it *tidyItem) (string, Scope) {
	d := it.dest
	logical := filepath.Join(d.root, filepath.FromSlash(d.logical))
	for _, a := range o {
		switch {
		case d.mcp() && a.key == it.ID.Key && (a.abs == d.abs || a.logical == logical):
			return a.module, a.sc
		case !d.mcp() && (within(it.ID.Path, a.abs) || within(filepath.Join(logical, it.disk), a.logical)):
			return a.module, a.sc
		}
	}
	return "", Scope{}
}

// unitLocks applies the unit-level hard locks (§4.4).
func (s *tidyScanner) unitLocks(it *tidyItem) {
	if it.dest.mcp() {
		if strings.ContainsAny(it.dest.abs, "\t\n\r") {
			it.lock(codeUnrecordableName, whyUnrecordable)
		}
		s.selectable(it)
		return
	}
	lower := strings.ToLower(it.disk)
	switch {
	case lower == ".git" || lower == ".lola":
		it.lock(codeReservedName, whyReservedName)
	case slices.Contains(instructionFiles, lower):
		it.lock(codeInstructionFile, whyInstructionFile)
	case it.fi.Mode().Type()&(fs.ModeNamedPipe|fs.ModeSocket|fs.ModeDevice|fs.ModeCharDevice|fs.ModeIrregular) != 0:
		it.lock(codeNotAFile, whyNotAFile)
	case it.fi.IsDir():
		s.walk(it)
	}
	if strings.ContainsAny(it.ID.Path, "\t\n\r") {
		it.lock(codeUnrecordableName, whyUnrecordable)
	}
	s.selectable(it)
}

// selectable locks a unit whose name no selector can name exactly (R-6d-S3).
func (s *tidyScanner) selectable(it *tidyItem) {
	if untrusted.Text(it.rawName, untrusted.MaxName) != it.rawName {
		it.lock(codeUnselectableName, whyUnselectable)
		return
	}
	if _, err := config.ParsePattern(it.Literal().String()); err != nil {
		it.lock(codeUnselectableName, whyUnselectable)
	}
}

// walk looks below a directory unit, never following a link, for instruction files, fifos,
// sockets and devices and entries it cannot read, adding up the size of its files.
func (s *tidyScanner) walk(it *tidyItem) {
	var size int64
	err := filepath.WalkDir(it.ID.Path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == it.ID.Path {
			return nil
		}
		if slices.Contains(instructionFiles, strings.ToLower(d.Name())) {
			it.lock(codeInstructionFile, whyInstructionFile)
		}
		if d.Type()&(fs.ModeNamedPipe|fs.ModeSocket|fs.ModeDevice|fs.ModeCharDevice|fs.ModeIrregular) != 0 {
			it.lock(codeNotAFile, whyNotAFile)
		}
		if d.Type().IsRegular() {
			fi, err := d.Info()
			if err != nil {
				return err
			}
			size += fi.Size()
		}
		return nil
	})
	if err != nil {
		it.lock(codeUnreadable, "holds an entry lola cannot read: "+untrusted.CleanErr(err).Error())
	}
	it.Size = size
}

// kindOrder is the order listings take kinds in.
func kindOrder(kind string) int {
	if i := slices.Index(config.Kinds(), kind); i >= 0 {
		return i
	}
	return len(config.Kinds())
}

// sortTidy sorts items by target, kind, name and path (§6.2).
func sortTidy(us []TidyUnit) {
	slices.SortStableFunc(us, func(a, b TidyUnit) int {
		return cmp.Or(cmp.Compare(a.Target, b.Target), cmp.Compare(kindOrder(a.Kind), kindOrder(b.Kind)), cmp.Compare(a.Name, b.Name),
			cmp.Compare(a.Path, b.Path), cmp.Compare(a.Key, b.Key))
	})
}
