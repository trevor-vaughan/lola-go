package engine

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/trash"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// TrashRun is one run of the trash as lola trash lists it (spec 2026-11-03 §3.5). Every text is
// escaped (untrusted.Text): a run's files are data on disk. Time is RFC 3339, UTC, "" when
// neither run.yaml nor the run's name says; Files counts the files and links its entries hold.
type TrashRun struct {
	ID        string    `json:"id"`
	Time      string    `json:"time"`
	Command   string    `json:"command"`
	Scope     string    `json:"scope"`
	Root      string    `json:"root"`
	Files     int       `json:"files"`
	SizeBytes int64     `json:"size_bytes"`
	id        string    // the run's directory name, as it is
	at        time.Time // the run's time, zero when unknown
}

// TrashList is lola trash: every run, newest first, and notices for what under the trash is no
// run lola can read.
type TrashList struct {
	Runs    []TrashRun `json:"runs"`
	Notices []Notice   `json:"notices"`
}

// TrashEntry is one entry of a run: where Original was put in the trash, escaped. Credentials
// says the entry is, or holds, a copy that may hold credentials.
type TrashEntry struct {
	Original    string `json:"original"`
	Trash       string `json:"trash"`
	Credentials bool   `json:"credentials"`
}

// TrashShow is lola trash <run>: the run and its entries, in manifest order.
type TrashShow struct {
	TrashRun
	Entries []TrashEntry `json:"entries"`
	// Existing is the originals that exist now, escaped: a restore is refused over them unless
	// it has --force, which moves them into a new trash run first.
	Existing []string      `json:"existing"`
	Notices  []Notice      `json:"notices"`
	raw      []trash.Entry // the run's entries, as Entries shows them
}

// trashRun is a run read back from the trash: its directory, as lola wrote it (unresolved, so
// ReadRun's lexical containment holds), what ReadRun read, and its listing.
type trashRun struct {
	dir  string
	read trash.Run
	view TrashRun
}

// trashCommand is the lola trash command line of args, escaped, for a hint.
func trashCommand(args ...string) string {
	out := []string{"lola", "trash"}
	for _, a := range args {
		out = append(out, untrusted.Text(a, untrusted.MaxName))
	}
	return errs.ShellJoin(out)
}

// noTrashRun refuses id, which names no run of the trash, hinting the listing.
func noTrashRun(id string, display []string) error {
	return errs.WithHints(fmt.Errorf("there is no trash run %s", shown(id)), "see the runs: "+trashCommand(display...))
}

// readTrashRun reads the run id names: one name, a real directory directly under the trash (a
// link there is no run), holding a manifest.txt.
func (e *Engine) readTrashRun(id string, display []string) (*trashRun, error) {
	dir, read, err := e.openTrashRun(id, display)
	if err != nil {
		return nil, err
	}
	r := &trashRun{dir: dir, read: read, view: TrashRun{ID: shown(read.ID), Command: shown(read.Command), Scope: shown(read.Scope),
		Root: shown(read.Root), id: read.ID, at: read.Time}}
	if !read.Time.IsZero() {
		r.view.Time = read.Time.UTC().Format(time.RFC3339)
	}
	for _, en := range read.Entries {
		files, size, err := trash.Measure(dir, en)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("cannot measure %s in the trash: %w", shown(en.Trash), untrusted.CleanErr(err))
		}
		r.view.Files += files
		r.view.SizeBytes += size
	}
	return r, nil
}

// openTrashRun is the directory of the run id names and what trash.ReadRun read there, measuring
// nothing: one name, a real directory directly under the trash (a link there is no run).
func (e *Engine) openTrashRun(id string, display []string) (string, trash.Run, error) {
	if id == "" || id == "." || id == ".." || !filepath.IsLocal(id) || filepath.Base(id) != id {
		return "", trash.Run{}, noTrashRun(id, display)
	}
	dir := filepath.Join(e.Paths.Trash, id)
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return "", trash.Run{}, noTrashRun(id, display)
	}
	read, err := trash.ReadRun(dir)
	if err != nil {
		return "", trash.Run{}, errs.WithHints(err, "see the runs: "+trashCommand(display...))
	}
	return dir, read, nil
}

// notices is r's ReadRun notices, which are escaped already, as warnings.
func (r *trashRun) notices() []Notice {
	out := []Notice{}
	for _, n := range r.read.Notices {
		out = append(out, Notice{Level: LevelWarn, Text: n})
	}
	return out
}

// entry is en as lola trash shows it: marked when a credential copy lies at or below it.
func (r *trashRun) entry(en trash.Entry) TrashEntry {
	cred := slices.ContainsFunc(r.read.Credentials, func(c string) bool { return within(en.Trash, c) })
	return TrashEntry{Original: shown(en.Original), Trash: shown(en.Trash), Credentials: cred}
}

// trashRuns reads every run of the trash, newest first; what is there but no run is a notice.
func (e *Engine) trashRuns() ([]*trashRun, []Notice, error) {
	notices := []Notice{}
	ds, err := os.ReadDir(e.Paths.Trash)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, notices, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("cannot list the trash %s: %w", shown(e.Paths.Trash), untrusted.CleanErr(err))
	}
	var runs []*trashRun
	for _, d := range ds {
		if !d.IsDir() {
			notices = append(notices, Notice{Level: LevelWarn, Text: shown(filepath.Join(e.Paths.Trash, d.Name())) + " is no trash run: it is not a directory"})
			continue
		}
		r, err := e.readTrashRun(d.Name(), nil)
		if err != nil {
			notices = append(notices, Notice{Level: LevelWarn, Text: shown(d.Name()) + " is no trash run lola can read: " + err.Error()})
			continue
		}
		runs = append(runs, r)
	}
	slices.SortFunc(runs, func(a, b *trashRun) int {
		return cmp.Or(b.view.at.Compare(a.view.at), strings.Compare(b.read.ID, a.read.ID))
	})
	return runs, notices, nil
}

// ListTrash lists the trash's runs, newest first (lola trash).
func (e *Engine) ListTrash() (*TrashList, error) {
	runs, notices, err := e.trashRuns()
	if err != nil {
		return nil, err
	}
	list := &TrashList{Runs: []TrashRun{}, Notices: notices}
	for _, r := range runs {
		list.Runs = append(list.Runs, r.view)
		list.Notices = append(list.Notices, r.notices()...)
	}
	return list, nil
}

// ShowTrash shows the run id names (lola trash <run>); display is the output flags a hint keeps.
func (e *Engine) ShowTrash(id string, display []string) (*TrashShow, error) {
	r, err := e.readTrashRun(id, display)
	if err != nil {
		return nil, err
	}
	show := &TrashShow{TrashRun: r.view, Entries: []TrashEntry{}, Existing: []string{}, Notices: r.notices(), raw: r.read.Entries}
	for _, en := range r.read.Entries {
		show.Entries = append(show.Entries, r.entry(en))
		if _, err := os.Lstat(en.Original); err == nil {
			show.Existing = append(show.Existing, shown(en.Original))
		}
	}
	return show, nil
}

// RestoreHint is the hint that ends lola trash <run>: restore, the run's restore command, which
// works only while no original exists; else that command with --force, saying what it replaces
// and where that goes.
func (s *TrashShow) RestoreHint(restore string) string {
	if len(s.Existing) == 0 {
		return "restore them: " + restore
	}
	verb := "moves"
	if len(s.Existing) > 1 {
		verb = "move"
	}
	return "restore them (replaces " + andJoin(s.Existing) + ", which " + verb + " to a new trash run first): " + restore + " --force"
}

// TrashEntryRef names one entry of a run by its raw original and trash paths, which its escaped
// TrashEntry may not tell apart from another's (TrashShow.Ref).
type TrashEntryRef struct {
	original, trash string
}

// Ref names s.Entries[i] for a TrashRestoreRequest.
func (s *TrashShow) Ref(i int) TrashEntryRef {
	return TrashEntryRef{original: s.raw[i].Original, trash: s.raw[i].Trash}
}

// TrashRestoreRequest is lola trash <run> [<original>...] --restore.
type TrashRestoreRequest struct {
	Run       string   // the run's id
	Originals []string // the entries to restore, by absolute original path; none restores every one
	// Entries names the entries to restore as a show listed them (TrashShow.Ref), beside Originals.
	Entries []TrashEntryRef
	Force   bool     // move an original that exists into a new trash run first
	DryRun  bool     // -n: the plan is the result
	Display []string // the output flags a hint keeps
}

// TrashRestoreResult is a restore's plan, or what it did. Restored lists the entries restored,
// or that would be; Existing the originals that exist, which refuse the restore without Force
// and with it move to the run Displaced names; RunRemoved says the emptied run was removed.
type TrashRestoreResult struct {
	Run        string       `json:"run"`
	Restored   []TrashEntry `json:"restored"`
	Existing   []string     `json:"existing"`
	Displaced  string       `json:"displaced"`
	RunRemoved bool         `json:"run_removed"`
	DryRun     bool         `json:"dry_run"`
	Notices    []Notice     `json:"notices"`
	req        TrashRestoreRequest
	run        *trashRun
	picked     []trash.Entry // the entries to restore
	gone       []trash.Entry // entries whose trash path is gone, forgotten with the restored ones
	replacing  []Notice      // the notices of whole-file copies the restore would put over a file, which its result says it did
}

// PlanTrashRestore plans req and writes nothing (spec 2026-11-03 §3.5): the run's entries, or
// those whose originals req names (each must be one of the run's), less those gone from the
// trash, which are notices. Two entries of one original, or one inside another's, are refused:
// a restore of both cannot put both back. The originals that exist are listed; RestoreTrash
// refuses them unless req.Force. A whole-file copy restored over the file there (wholeCopy) says
// so in a notice: it replaces the file, changes made since included.
func (e *Engine) PlanTrashRestore(req TrashRestoreRequest) (*TrashRestoreResult, error) {
	r, err := e.readTrashRun(req.Run, req.Display)
	if err != nil {
		return nil, err
	}
	res := &TrashRestoreResult{Run: r.view.ID, Restored: []TrashEntry{}, Existing: []string{}, DryRun: true, Notices: r.notices(), req: req, run: r}
	see := "see the run's entries: " + trashCommand(append([]string{req.Run}, req.Display...)...)
	picked := r.read.Entries
	if len(req.Originals) > 0 || len(req.Entries) > 0 {
		picked = nil
		for _, ref := range req.Entries {
			i := slices.IndexFunc(r.read.Entries, func(en trash.Entry) bool { return en.Original == ref.original && en.Trash == ref.trash })
			if i < 0 {
				return nil, errs.WithHints(fmt.Errorf("trash run %s no longer holds %s", shown(req.Run), shown(ref.original)), see)
			}
			if !slices.Contains(picked, r.read.Entries[i]) {
				picked = append(picked, r.read.Entries[i])
			}
		}
		for _, o := range req.Originals {
			o = filepath.Clean(o)
			found := false
			for _, en := range r.read.Entries {
				if en.Original == o || shown(en.Original) == o {
					found = true
					if !slices.Contains(picked, en) {
						picked = append(picked, en)
					}
				}
			}
			if !found {
				return nil, errs.WithHints(fmt.Errorf("trash run %s does not hold %s", shown(req.Run), shown(o)), see)
			}
		}
	}
	for i, a := range picked {
		for _, b := range picked[i+1:] {
			if overlaps(a.Original, b.Original) {
				return nil, errs.WithHints(fmt.Errorf("trash run %s holds %s and %s, which one restore cannot both put back; restore one at a time",
					shown(req.Run), shown(a.Original), shown(b.Original)),
					"restore one: "+trashCommand(append([]string{req.Run, a.Original, "--restore"}, req.Display...)...), see)
			}
		}
	}
	for _, en := range picked {
		if _, _, err := trash.Measure(r.dir, en); errors.Is(err, fs.ErrNotExist) {
			res.gone = append(res.gone, en)
			res.Notices = append(res.Notices, Notice{Level: LevelWarn, Text: shown(en.Trash) + " is gone from the trash, so " + shown(en.Original) +
				" cannot be restored; its line in manifest.txt is dropped"})
			continue
		}
		res.picked = append(res.picked, en)
		res.Restored = append(res.Restored, r.entry(en))
		if _, err := os.Lstat(en.Original); err == nil {
			res.Existing = append(res.Existing, shown(en.Original))
			if r.wholeCopy(en) {
				n := Notice{Level: LevelWarn, Text: "the restore replaces the whole file " + shown(en.Original) + " with the copy trash run " + r.view.ID +
					" holds, at mode 0600 (it may hold credentials); the file there now, with every change made since, moves to a new trash run first"}
				res.Notices, res.replacing = append(res.Notices, n), append(res.replacing, n)
			}
		}
	}
	if len(res.Existing) > 0 && !req.Force {
		res.Notices = append(res.Notices, Notice{Level: LevelWarn, Text: andJoin(res.Existing) + existVerb(res.Existing) + ", so the restore is refused; " +
			"move what is there to a new trash run, then restore: " + e.forceRestore(req)})
	}
	return res, nil
}

// TrashRestoresOverlap refuses plans of several runs that would restore one original twice, or
// one inside another's: restoring the second would find the first's in its place. The error names
// the originals and both runs, escaped; nil when no two overlap.
func TrashRestoresOverlap(plans ...*TrashRestoreResult) error {
	for i, a := range plans {
		for _, b := range plans[i+1:] {
			for _, x := range a.picked {
				for _, y := range b.picked {
					switch {
					case x.Original == y.Original:
						return fmt.Errorf("trash runs %s and %s both hold %s, which one restore cannot put back twice", a.Run, b.Run, shown(x.Original))
					case overlaps(x.Original, y.Original):
						return fmt.Errorf("trash run %s holds %s and trash run %s holds %s, one inside the other, which one restore cannot both put back",
							a.Run, shown(x.Original), b.Run, shown(y.Original))
					}
				}
			}
		}
	}
	return nil
}

// wholeCopy reports whether en is a copy of a whole file that may hold credentials, as lola tidy
// keeps of a config file it removes an MCP server from (the file itself stays): restored, it
// replaces the file there, at mode 0600.
func (r *trashRun) wholeCopy(en trash.Entry) bool {
	return slices.Contains(r.read.Credentials, en.Trash)
}

// existVerb is " exists" after one path, " exist" after several.
func existVerb(paths []string) string {
	if len(paths) == 1 {
		return " exists"
	}
	return " exist"
}

// forceRestore is req's run with --force: what a restore refused for existing originals hints.
func (e *Engine) forceRestore(req TrashRestoreRequest) string {
	return trashCommand(slices.Concat([]string{req.Run}, req.Originals, []string{"--restore", "--force"}, req.Display)...)
}

// RestoreTrash applies plan, a PlanTrashRestore (spec 2026-11-03 §3.5). An original that exists
// now refuses the whole restore, unless the plan's request has Force: then each one is moved into
// a new trash run first, never deleted (marked a credential copy when the entry put back is
// one), and a whole-file copy put back over its file says so, naming that run. Each entry is then
// moved back (trash.Restore); the lines of those restored, and of those gone, are dropped from
// manifest.txt, and a run left empty is removed. A failure part way stops
// there: what was restored is reported in the error and its lines are dropped.
func (e *Engine) RestoreTrash(plan *TrashRestoreResult) (*TrashRestoreResult, error) {
	req, r := plan.req, plan.run
	res := &TrashRestoreResult{Run: plan.Run, Restored: []TrashEntry{}, Existing: []string{},
		Notices: slices.DeleteFunc(slices.Clone(plan.Notices), func(n Notice) bool { return slices.Contains(plan.replacing, n) })}
	var existing []trash.Entry
	for _, en := range plan.picked {
		if _, err := os.Lstat(en.Original); err == nil {
			existing = append(existing, en)
			res.Existing = append(res.Existing, shown(en.Original))
		}
	}
	if len(existing) > 0 && !req.Force {
		return nil, errs.WithHints(fmt.Errorf("lola trash restores nothing over what is there: %s%s", andJoin(res.Existing), existVerb(res.Existing)),
			"move what is there to a new trash run, then restore: "+e.forceRestore(req),
			"see the run's entries: "+trashCommand(append([]string{req.Run}, req.Display...)...))
	}
	if len(existing) > 0 {
		id, err := e.displace(req, r, existing)
		res.Displaced = id
		if err != nil {
			return nil, err
		}
	}
	done := slices.Clone(plan.gone)
	for _, en := range plan.picked {
		err := trash.Restore(r.dir, en, r.read.Credentials)
		if me, ok := errors.AsType[*trash.ModeError](err); ok { // the entry is back; only its copies' modes are not private
			for i := range me.Paths {
				res.Notices = append(res.Notices, Notice{Level: LevelWarn, Text: "restored " + shown(en.Original) + ", but " + me.Failure(i)})
			}
			err = nil
		}
		if err != nil {
			_, ferr := trash.Forget(r.dir, done)
			return nil, errs.WithHints(errors.Join(fmt.Errorf("cannot restore %s from trash run %s after restoring %d of %d: %w", shown(en.Original),
				shown(req.Run), len(res.Restored), len(plan.picked), untrusted.CleanErr(err)), ferr),
				"see what the run still holds: "+trashCommand(append([]string{req.Run}, req.Display...)...))
		}
		done = append(done, en)
		res.Restored = append(res.Restored, r.entry(en))
		if slices.Contains(existing, en) && r.wholeCopy(en) {
			res.Notices = append(res.Notices, Notice{Level: LevelWarn, Text: "replaced the whole file " + shown(en.Original) + " with the copy from trash run " +
				r.view.ID + ", at mode 0600; the file that was there is in trash run " + shown(res.Displaced)})
		}
	}
	removed, err := trash.Forget(r.dir, done)
	if err != nil {
		return nil, fmt.Errorf("restored %d entries, but cannot drop their lines from trash run %s: %w", len(res.Restored), shown(req.Run), untrusted.CleanErr(err))
	}
	res.RunRemoved = removed
	return res, nil
}

// displace moves the originals of existing, which a forced restore puts its entries back over,
// into a new trash run, made by this restore, and returns its id.
func (e *Engine) displace(req TrashRestoreRequest, r *trashRun, existing []trash.Entry) (string, error) {
	t, err := trash.OpenTrash(e.Paths.Trash, e.Now(), trash.RunRecord{
		Command: trashCommand(slices.Concat([]string{req.Run}, req.Originals, []string{"--restore", "--force"})...),
		Scope:   r.read.Scope, Root: r.read.Root})
	if err != nil {
		return "", err
	}
	for _, en := range existing {
		vol := filepath.VolumeName(en.Original) + string(filepath.Separator)
		parent, err := os.OpenRoot(filepath.Dir(en.Original))
		if err != nil {
			return filepath.Base(t.Dir), errors.Join(err, t.Close())
		}
		to, err := t.Move("restore", vol, parent, filepath.Base(en.Original))
		if err = errors.Join(err, parent.Close()); err != nil {
			return filepath.Base(t.Dir), errors.Join(err, t.Close())
		}
		if r.entry(en).Credentials {
			t.MarkCredential(to)
		}
	}
	return filepath.Base(t.Dir), t.Close()
}

// TrashEmptyRequest is lola trash [<run>...] --empty [--older-than <age>].
type TrashEmptyRequest struct {
	Runs      []string // the runs to delete by id; none means every run
	OlderThan string   // only runs older than this age (config.ParseAge), "" for any
	Display   []string // the output flags a hint keeps
}

// ParseOlderThan reads --older-than's value, in days, refusing one that is not an age of days or
// weeks, "" and never included.
func ParseOlderThan(flag string) (int, error) {
	age, ok := config.ParseAge(flag)
	if !ok || age.Never {
		return 0, fmt.Errorf("--older-than %q is not an age; write a whole number of days or weeks, such as 30d or 2w",
			untrusted.Text(flag, untrusted.MaxName))
	}
	if age.Weeks {
		return age.N * 7, nil
	}
	return age.N, nil
}

// TrashEmptyResult is what an empty deleted, or as a plan would delete, and the bytes it freed.
type TrashEmptyResult struct {
	Removed    []TrashRun `json:"removed"`
	BytesFreed int64      `json:"bytes_freed"`
	DryRun     bool       `json:"dry_run"`
	Notices    []Notice   `json:"notices"`
	display    []string   // a plan's: the output flags a hint keeps
	rerun      string     // a plan's: the lola trash run that plans it again
}

// PlanTrashEmpty plans req and writes nothing (spec 2026-11-03 §3.5): the named runs, each of
// which must be one, or every run; with an age, only those older than it. A run whose time is
// unknown is kept, with a notice, when an age is given.
func (e *Engine) PlanTrashEmpty(req TrashEmptyRequest) (*TrashEmptyResult, error) {
	var cutoff time.Time
	if req.OlderThan != "" {
		days, err := ParseOlderThan(req.OlderThan)
		if err != nil {
			return nil, err
		}
		cutoff = e.Now().AddDate(0, 0, -days)
	}
	again := slices.Concat(req.Runs, []string{"--empty"})
	if req.OlderThan != "" {
		again = append(again, "--older-than", req.OlderThan)
	}
	res := &TrashEmptyResult{Removed: []TrashRun{}, DryRun: true, Notices: []Notice{}, display: req.Display,
		rerun: trashCommand(append(again, req.Display...)...)}
	var runs []*trashRun
	if len(req.Runs) == 0 {
		all, notices, err := e.trashRuns()
		if err != nil {
			return nil, err
		}
		runs, res.Notices = all, notices
	}
	for _, id := range req.Runs {
		r, err := e.readTrashRun(id, req.Display)
		if err != nil {
			return nil, err
		}
		if !slices.ContainsFunc(runs, func(o *trashRun) bool { return o.dir == r.dir }) {
			runs = append(runs, r)
		}
	}
	for _, r := range runs {
		switch {
		case cutoff.IsZero():
		case r.view.at.IsZero():
			res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: "trash run " + r.view.ID + " is kept: lola cannot tell its age"})
			continue
		case !r.view.at.Before(cutoff):
			if len(req.Runs) > 0 {
				res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: "trash run " + r.view.ID + " is kept: it is newer than " +
					untrusted.Text(req.OlderThan, untrusted.MaxName)})
			}
			continue
		}
		res.Removed = append(res.Removed, r.view)
		res.BytesFreed += r.view.SizeBytes
	}
	return res, nil
}

// EmptyTrash applies plan, a PlanTrashEmpty the user confirmed: each run is deleted through an
// os.Root on the trash, so no link in it is followed out. A run gone since the plan is a notice;
// so is one whose files or size changed since (a lola still writing it), which is kept, so that
// only what the user was shown is deleted.
func (e *Engine) EmptyTrash(plan *TrashEmptyResult) (*TrashEmptyResult, error) {
	res := &TrashEmptyResult{Removed: []TrashRun{}, Notices: slices.Clone(plan.Notices)}
	if len(plan.Removed) == 0 {
		return res, nil
	}
	root, err := os.OpenRoot(e.Paths.Trash)
	if err != nil {
		return nil, fmt.Errorf("cannot open the trash %s: %w", shown(e.Paths.Trash), untrusted.CleanErr(err))
	}
	defer func() { _ = root.Close() }()
	for _, r := range plan.Removed {
		if fi, err := root.Lstat(r.id); err != nil || !fi.IsDir() {
			res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: "trash run " + r.ID + " is gone already"})
			continue
		}
		if now, err := e.readTrashRun(r.id, nil); err != nil || now.view.Files != r.Files || now.view.SizeBytes != r.SizeBytes {
			res.Notices = append(res.Notices, Notice{Level: LevelWarn, Text: "trash run " + r.ID + " changed since the plan, so it is kept; rerun: " + plan.rerun})
			continue
		}
		if err := root.RemoveAll(r.id); err != nil {
			return nil, errs.WithHints(fmt.Errorf("cannot delete trash run %s after deleting %d of %d: %w", r.ID, len(res.Removed), len(plan.Removed),
				untrusted.CleanErr(err)), "see the runs left: "+trashCommand(plan.display...))
		}
		res.Removed = append(res.Removed, r)
		res.BytesFreed += r.SizeBytes
	}
	return res, nil
}
