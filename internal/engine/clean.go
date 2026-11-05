package engine

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Why lola cache clean removes an item (Plan 9 §6.5).
const (
	CleanSuperseded = "superseded" // a version no head points to
	CleanUnused     = "unused"     // a version unused for at least the age
	CleanScratch    = "scratch"    // a run's scratch no live run holds
	CleanAll        = "all"        // anything --all removes
)

// Why lola cache clean keeps an item it would otherwise remove (Plan 9 §6.5).
const (
	KeptInUse      = "in use"     // scratch a live run holds (A5)
	KeptChanged    = "changed"    // an item that no longer qualified when clean checked it again (A7)
	KeptUnreadable = "unreadable" // a module whose index.yml, or an entry, clean could not read (A28)
)

// cleanPrefix names the directory under Paths.Staging that a clean renames what it removes into.
const cleanPrefix = "clean-"

// CleanRequest is a lola cache clean run (Plan 9 §6.2): UnusedFor is --unused-for as given, ""
// for config.yaml's cache_unused_for; All removes everything but held scratch.
type CleanRequest struct {
	UnusedFor string
	All       bool
}

// CleanItem is one item a clean removes, or would. Module and Version are "" for scratch and
// catalogs. Path is the item's path relative to the cache's root ($XDG_CACHE_HOME/lola), as it
// is on disk when it is plain text (every validated name and name lola makes is), else cleaned
// (A28); Bytes counts its regular files.
type CleanItem struct {
	Module  string `json:"module"`
	Version string `json:"version"`
	Reason  string `json:"reason"`
	Path    string `json:"path"`
	Bytes   int64  `json:"bytes"`
	abs     string // the item
	lock    string // the lock that holds a scratch item, removed with it; "" for none
}

// CleanKept is an item a clean kept that it would otherwise remove, and why.
type CleanKept struct {
	Module  string `json:"module"`
	Version string `json:"version"`
	Reason  string `json:"reason"`
	Path    string `json:"path"`
}

// CleanResult is what a clean removed (or, as a plan, would remove) and kept. Notices warn of
// what set aside a clean could not delete, which the next clean removes.
type CleanResult struct {
	Removed    []CleanItem `json:"removed"`
	Kept       []CleanKept `json:"kept"`
	BytesFreed int64       `json:"bytes_freed"`
	DryRun     bool        `json:"dry_run"`
	Notices    []Notice    `json:"notices"`
	all        bool        // a plan's: --all
	age        config.Age  // a plan's: the age it removes unused versions at
	modules    []string    // a plan's: the modules it read, whose undated versions a real clean dates
}

// PlanClean plans lola cache clean (Plan 9 §6.2) and writes nothing: the plan is what -n prints
// and what ApplyClean applies. A version with no last_used counts as used today.
func (e *Engine) PlanClean(req CleanRequest) (*CleanResult, error) {
	res := &CleanResult{Removed: []CleanItem{}, Kept: []CleanKept{}, DryRun: true, Notices: []Notice{}, all: req.All}
	if req.All {
		if err := e.planAll(res); err != nil {
			return nil, err
		}
		return res.total(), nil
	}
	age, err := e.cleanAge(req.UnusedFor)
	if err != nil {
		return nil, err
	}
	res.age = age
	if err := e.planVersions(res); err != nil {
		return nil, err
	}
	for _, s := range []struct{ parent, prefix string }{{e.Paths.Staging, runPrefix}, {e.Paths.Staging, cleanPrefix}, {e.Paths.Tmp, checkPrefix}} {
		es, err := listDir(s.parent)
		if err != nil {
			return nil, err
		}
		for _, d := range es {
			if strings.HasPrefix(d.Name(), s.prefix) {
				e.planScratch(res, s.parent, d.Name(), es, CleanScratch)
			}
		}
	}
	return res.total(), nil
}

// cleanAge is the age of a clean: --unused-for (`--unused-for never` turns the configured one
// off for one run), else config.yaml's cache_unused_for.
func (e *Engine) cleanAge(flag string) (config.Age, error) {
	if flag != "" {
		return ParseUnusedFor(flag)
	}
	cfg, err := config.LoadConfig(e.Paths.Config, e.Paths.Home)
	if err != nil {
		return config.Age{}, err
	}
	return cfg.CacheUnusedFor, nil
}

// ParseUnusedFor reads --unused-for's value, refusing one that is not an age, "" included.
func ParseUnusedFor(flag string) (config.Age, error) {
	age, ok := config.ParseAge(flag)
	if !ok {
		return config.Age{}, fmt.Errorf("--unused-for %q is not an age; %s", untrusted.Text(flag, untrusted.MaxName), config.AgeForms)
	}
	return age, nil
}

// planVersions plans the versions a bare clean removes: each one no head points to, and with an
// age each one unused for that long, heads included. Module names and version ids are validated
// before they reach a path; a module directory or versions directory that is a link is left.
func (e *Engine) planVersions(res *CleanResult) error {
	today := e.today()
	es, err := listDir(e.Paths.Store)
	if err != nil {
		return err
	}
	for _, d := range es {
		name := d.Name()
		if !d.IsDir() || module.ValidateName("module", name) != nil {
			continue
		}
		dir := filepath.Join(e.Paths.Store, name)
		o, found, err := e.Store.Origin(name)
		if err != nil {
			res.Kept = append(res.Kept, CleanKept{Module: name, Reason: KeptUnreadable, Path: e.cacheRel(dir)})
			continue
		}
		if !found {
			continue
		}
		vs, err := listDir(filepath.Join(dir, "versions"))
		if err != nil {
			res.Kept = append(res.Kept, CleanKept{Module: name, Reason: KeptUnreadable, Path: e.cacheRel(dir)})
			continue
		}
		res.modules = append(res.modules, name)
		for _, v := range vs {
			id := v.Name()
			if !store.ValidID(id) {
				continue
			}
			reason := ""
			switch {
			case !slices.ContainsFunc(o.Heads, func(h store.Head) bool { return h.Digest == id }):
				reason = CleanSuperseded
			case res.age.Covers(daysSince(cmp.Or(o.LastUsed[id], today), today)):
				reason = CleanUnused
			default:
				continue
			}
			it := e.cleanItem(res, filepath.Join(dir, "versions", id), reason, name, id)
			res.add(it)
		}
	}
	return nil
}

// planScratch plans the scratch entry name under parent, whose entries are es: a run's
// directory with the lock beside it, or a lock alone, is removed when no live run holds the lock
// (R-9-9), and kept in use otherwise; a lock whose directory is there goes with the directory.
func (e *Engine) planScratch(res *CleanResult, parent, name string, es []fs.DirEntry, reason string) {
	abs, lock := filepath.Join(parent, name), filepath.Join(parent, name)+lockSuffix
	if base, ok := strings.CutSuffix(name, lockSuffix); ok {
		if slices.ContainsFunc(es, func(d fs.DirEntry) bool { return d.Name() == base }) {
			return // its directory's item
		}
		lock = abs
	}
	free, err := lockFree(lock)
	if err != nil {
		res.Kept = append(res.Kept, CleanKept{Reason: KeptUnreadable, Path: e.cacheRel(abs)})
		return
	}
	if !free {
		res.Kept = append(res.Kept, CleanKept{Reason: KeptInUse, Path: e.cacheRel(abs)})
		return
	}
	it := e.cleanItem(res, abs, reason, "", "")
	if it != nil {
		it.lock = lock
	}
	res.add(it)
}

// planAll plans --all: every entry of modules/, markets/, .staging/ and tmp/, but scratch a live
// run holds (A5). The lock file and probes.json, outside those, stay.
func (e *Engine) planAll(res *CleanResult) error {
	for _, parent := range []string{e.Paths.Store, e.Paths.MarketCache, e.Paths.Staging, e.Paths.Tmp} {
		es, err := listDir(parent)
		if err != nil {
			return err
		}
		for _, d := range es {
			name := d.Name()
			if parent == e.Paths.Staging && strings.HasPrefix(name, runPrefix) || parent == e.Paths.Tmp && strings.HasPrefix(name, checkPrefix) {
				e.planScratch(res, parent, name, es, CleanAll)
				continue
			}
			mod := ""
			if parent == e.Paths.Store && module.ValidateName("module", name) == nil {
				mod = name
			}
			res.add(e.cleanItem(res, filepath.Join(parent, name), CleanAll, mod, ""))
		}
	}
	return nil
}

// cleanItem is the item at abs, of module mod's version id when it is one, with its size; abs is
// never followed if it is a link. An item clean cannot read is kept as unreadable (A28): nil.
func (e *Engine) cleanItem(res *CleanResult, abs, reason, mod, id string) *CleanItem {
	n, err := regularBytes(abs)
	if err != nil {
		res.Kept = append(res.Kept, CleanKept{Module: mod, Version: id, Reason: KeptUnreadable, Path: e.cacheRel(abs)})
		return nil
	}
	return &CleanItem{Module: mod, Version: id, Reason: reason, Path: e.cacheRel(abs), Bytes: n, abs: abs}
}

// add adds it, when there is one, to what res removes.
func (res *CleanResult) add(it *CleanItem) {
	if it != nil {
		res.Removed = append(res.Removed, *it)
	}
}

// CleanError is an apply that failed part way (Plan 9 A28): its JSON carries what the clean had
// already removed from the cache, and the bytes of it that were deleted.
type CleanError struct {
	Err        error
	Removed    []CleanItem
	BytesFreed int64
}

func (e *CleanError) Error() string {
	if len(e.Removed) == 0 {
		return e.Err.Error()
	}
	what := fmt.Sprintf("%d items", len(e.Removed))
	if len(e.Removed) == 1 {
		what = "1 item"
	}
	return fmt.Sprintf("%v; this clean had removed %s from your cache first", e.Err, what)
}

func (e *CleanError) Unwrap() error { return e.Err }

// JSONFields are the keys the error's JSON carries beside error and hints.
func (e *CleanError) JSONFields() map[string]any {
	return map[string]any{"removed": e.Removed, "bytes_freed": e.BytesFreed}
}

// ApplyClean applies plan, a PlanClean the user confirmed (Plan 9 §6.4). It takes the markets
// lock for --all, then the cache lock, for its whole removal. Under the lock it first checks
// every item again (A7): one that no longer qualifies, or whose parent directories are no longer
// real directories, is kept as changed, with every item of a module whose directory is not; a
// module whose index.yml or one of whose versions it cannot read again is kept as unreadable,
// with all its items and no write of its index.yml, and an item of no module it cannot read is
// kept as unreadable; and .staging, which items are renamed into, must be a real directory. Then
// each module's index.yml drops the heads and last_used of its versions that go and dates today
// every version without a last_used (A28), so no head names a removed version, whatever fails
// later; when one cannot be written, those already written are put back. Then each item is
// renamed aside into .staging/clean-<x>, and a module whose last version went is renamed aside
// whole. When a rename fails, the heads and last_used of the versions that did not go are put
// back, and the error carries what was removed (CleanError). At the end the aside directory is
// deleted; BytesFreed counts what was, and what could not be is a notice.
func (e *Engine) ApplyClean(plan *CleanResult) (*CleanResult, error) {
	res := &CleanResult{Removed: []CleanItem{}, Kept: slices.Clone(plan.Kept), Notices: []Notice{}}
	if plan.all {
		unlockMarkets, err := lockAt(e.Paths.MarketsLock, true)
		if err != nil {
			return nil, err
		}
		defer unlockMarkets()
	}
	unlock, err := e.Store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	today := e.today()
	realMod := map[string]bool{}
	for _, name := range plan.modules {
		realMod[name] = e.realDirs(filepath.Join(e.Paths.Store, name))
	}
	origins := map[string]store.Origin{}
	unreadable := map[string]bool{}
	for _, name := range plan.modules {
		if !realMod[name] {
			continue
		}
		if o, found, err := e.Store.Origin(name); err != nil {
			unreadable[name] = true
		} else if found {
			origins[name] = o
		}
	}
	going := []CleanItem{}
	for _, it := range plan.Removed {
		if it.Version != "" && unreadable[it.Module] {
			continue
		}
		ok := it.Version == "" || realMod[it.Module]
		if ok {
			if ok, err = e.stillQualifies(it, plan.age, origins, today); err != nil {
				if it.Version != "" {
					unreadable[it.Module] = true
				} else {
					res.Kept = append(res.Kept, CleanKept{Module: it.Module, Reason: KeptUnreadable, Path: it.Path})
				}
				continue
			}
		}
		if !ok {
			res.Kept = append(res.Kept, CleanKept{Module: it.Module, Version: it.Version, Reason: KeptChanged, Path: it.Path})
			continue
		}
		going = append(going, it)
	}
	going = slices.DeleteFunc(going, func(it CleanItem) bool { return it.Version != "" && unreadable[it.Module] })
	gone := map[string][]string{}
	for _, it := range going {
		if it.Version != "" {
			gone[it.Module] = append(gone[it.Module], it.Version)
		}
	}
	for _, name := range plan.modules {
		if unreadable[name] {
			res.Kept = append(res.Kept, CleanKept{Module: name, Reason: KeptUnreadable, Path: e.cacheRel(filepath.Join(e.Paths.Store, name))})
		}
	}
	if len(going) > 0 {
		if err := os.MkdirAll(e.Paths.Staging, 0o700); err != nil {
			return nil, err
		}
		if !e.realDirs(e.Paths.Staging) {
			return nil, fmt.Errorf("%s is not a directory, or not one lola made: lola cache clean sets what it removes aside only in a real directory of your cache",
				shown(e.Paths.Staging))
		}
	}
	modules := []string{}
	before := map[string]store.Origin{}
	for _, name := range plan.modules {
		o, found := origins[name]
		if !found || unreadable[name] {
			continue
		}
		if err := e.Store.Cleaned(name, gone[name]); err != nil {
			return nil, e.restore(modules, before, gone, nil, err)
		}
		modules, before[name] = append(modules, name), o
	}
	if len(going) == 0 {
		return res, nil
	}
	aside, err := os.MkdirTemp(e.Paths.Staging, cleanPrefix+"*")
	if err != nil {
		return nil, e.restore(modules, before, gone, nil, err)
	}
	moved := map[string][]string{}
	var failed error
	for i, it := range going {
		ok, err := moveAside(it, filepath.Join(aside, strconv.Itoa(i)), os.Rename)
		if err != nil {
			failed = err
			break
		}
		if !ok {
			res.Kept = append(res.Kept, CleanKept{Module: it.Module, Version: it.Version, Reason: KeptChanged, Path: it.Path})
			continue
		}
		res.Removed = append(res.Removed, it)
		if it.Version != "" {
			moved[it.Module] = append(moved[it.Module], it.Version)
		}
	}
	if failed == nil {
		for _, name := range modules {
			if failed = e.dropEmpty(name, gone[name], filepath.Join(aside, "module-"+name)); failed != nil {
				break
			}
		}
	}
	if failed != nil {
		failed = e.restore(modules, before, gone, moved, failed)
	}
	for _, it := range res.Removed {
		res.BytesFreed += it.Bytes
	}
	if rmErr := os.RemoveAll(aside); rmErr != nil {
		left, err := regularBytes(aside)
		if err != nil {
			left = res.BytesFreed
		}
		res.BytesFreed = max(res.BytesFreed-left, 0)
		if failed != nil {
			failed = errors.Join(failed, rmErr)
		} else {
			res.Notices = append(res.Notices, Notice{Level: LevelWarn, Text: fmt.Sprintf("could not delete what this clean set aside in %s: %v; "+
				"lola cache clean retries it", e.cacheRel(aside), untrusted.CleanErr(rmErr))})
		}
	}
	if failed != nil {
		return nil, &CleanError{Err: failed, Removed: res.Removed, BytesFreed: res.BytesFreed}
	}
	return res, nil
}

// restore puts back in each of modules, after a clean failed with err, the heads and last_used
// before recorded for the versions in gone that it did not move and that are not known to be
// gone from the cache (Store.Restore), and returns err with any failure of that.
func (e *Engine) restore(modules []string, before map[string]store.Origin, gone, moved map[string][]string, err error) error {
	for _, name := range modules {
		left := slices.DeleteFunc(slices.Clone(gone[name]), func(id string) bool {
			_, err := os.Lstat(filepath.Join(e.Paths.Store, name, "versions", id))
			return slices.Contains(moved[name], id) || errors.Is(err, fs.ErrNotExist)
		})
		if len(left) > 0 {
			err = errors.Join(err, e.Store.Restore(name, before[name], left))
		}
	}
	return err
}

// stillQualifies checks a planned item again under the cache lock (A7): every directory between
// the cache's root and it is still a real directory, never a link (A28); it is still there; a
// superseded version is still pointed to by no head, and an unused one still unused for age.
// origins caches the index each module had when the clean took the lock.
func (e *Engine) stillQualifies(it CleanItem, age config.Age, origins map[string]store.Origin, today string) (bool, error) {
	if !e.realDirs(filepath.Dir(it.abs)) {
		return false, nil
	}
	if _, err := os.Lstat(it.abs); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if it.Version == "" || it.Reason == CleanAll {
		return true, nil
	}
	o, ok := origins[it.Module]
	if !ok {
		var found bool
		var err error
		if o, found, err = e.Store.Origin(it.Module); err != nil || !found {
			return false, err
		}
		origins[it.Module] = o
	}
	headed := slices.ContainsFunc(o.Heads, func(h store.Head) bool { return h.Digest == it.Version })
	if it.Reason == CleanSuperseded {
		return !headed, nil
	}
	return age.Covers(daysSince(cmp.Or(o.LastUsed[it.Version], today), today)), nil
}

// moveAside renames it to dst with rename (os.Rename; specs observe it); false when its scratch
// lock is held or it is gone. A scratch item's lock is held across the rename of its directory and
// released before the lock file is removed, which a system that refuses to remove an open file
// (Windows) needs; a lock alone is held across its own rename too, but on Windows, which refuses
// to rename an open file, it is released first.
func moveAside(it CleanItem, dst string, rename func(oldpath, newpath string) error) (bool, error) {
	release := func() {}
	if it.lock != "" {
		r, free, err := holdLock(it.lock)
		if err != nil || !free {
			return false, err
		}
		release = r
	}
	early := it.lock == it.abs && runtime.GOOS == "windows"
	if early {
		release()
	}
	err := rename(it.abs, dst)
	if !early {
		release()
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if it.lock != "" && it.lock != it.abs {
		if err := os.Remove(it.lock); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
	}
	return true, nil
}

// realDirs reports whether dir and every directory between it and the cache's root are real
// directories, never links, as clean checks them again under the lock (A28).
func (e *Engine) realDirs(dir string) bool {
	root := filepath.Dir(e.Paths.Store)
	for d := dir; d != root; d = filepath.Dir(d) {
		if rel, err := filepath.Rel(root, d); err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			return false
		}
		if fi, err := os.Lstat(d); err != nil || !fi.IsDir() {
			return false
		}
	}
	return true
}

// dropEmpty renames name's module directory to dst when the clean removed the versions ids and
// no version is left, so the module goes whole.
func (e *Engine) dropEmpty(name string, ids []string, dst string) error {
	if len(ids) == 0 {
		return nil
	}
	left, err := os.ReadDir(filepath.Join(e.Paths.Store, name, "versions"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) || len(left) > 0 {
		return err
	}
	return os.Rename(filepath.Join(e.Paths.Store, name), dst)
}

// total sums the bytes of what res removes.
func (res *CleanResult) total() *CleanResult {
	res.BytesFreed = 0
	for _, it := range res.Removed {
		res.BytesFreed += it.Bytes
	}
	return res
}

// lockFree reports whether no live run holds the lock file at path: it is missing, not a regular
// file, or its lock can be taken without waiting.
func lockFree(path string) (bool, error) {
	release, free, err := holdLock(path)
	if err == nil && free {
		release()
	}
	return free, err
}

// listDir lists dir, never through a link: a missing dir, or one that is not a directory, lists
// nothing.
func listDir(dir string) ([]fs.DirEntry, error) {
	fi, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) || err == nil && !fi.IsDir() {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return os.ReadDir(dir)
}

// regularBytes sums the sizes of the regular files at and below path, never following a link.
func regularBytes(path string) (int64, error) {
	var n int64
	err := filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		n += fi.Size()
		return nil
	})
	return n, err
}

// cacheRel is abs relative to the cache's root, with / separators, on one line: as it is when it
// is plain text, as every validated name and every name lola makes is, else cleaned (A28).
func (e *Engine) cacheRel(abs string) string {
	rel, err := filepath.Rel(filepath.Dir(e.Paths.Store), abs)
	if err != nil {
		rel = abs
	}
	return oneLine(filepath.ToSlash(rel))
}

// today is the UTC day of e.Now, as last_used records it.
func (e *Engine) today() string { return e.Now().UTC().Format(time.DateOnly) }

// daysSince is the number of whole days from the day since to the day today (both YYYY-MM-DD,
// which the store validated); 0 when since is later.
func daysSince(since, today string) int {
	a, errA := time.Parse(time.DateOnly, since)
	b, errB := time.Parse(time.DateOnly, today)
	if errA != nil || errB != nil || b.Before(a) {
		return 0
	}
	return int(b.Sub(a).Hours() / 24)
}
