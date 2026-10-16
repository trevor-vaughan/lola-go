package plan

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"sort"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/fsx"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
)

// Request is everything Compute needs. Roots and BaseDirs are keyed by target id.
type Request struct {
	Roots    map[string]string
	BaseDirs map[string][]string
	Ledger   *ledger.Ledger // observed state; never mutated
	Changes  []ModuleChange
	Force    bool
}

// Plan is a checked set of changes plus the ledger that results from applying them.
type Plan struct {
	Steps  []Step
	Files  []FileChange
	Ledger *ledger.Ledger
}

// content kinds of a destination file, which decide how "empty" is judged.
const (
	contentFile = iota
	contentJSON
)

type file struct {
	target, logical string
	res             fsx.Resolved
	exists          bool
	orig, cur       []byte
	mode, origMode  fs.FileMode
	content         int
	touched, del    bool
	created         bool // listed in ledger.Created, or created by this plan
}

type planner struct {
	req       Request
	files     map[string]*file
	order     []string
	owners    map[string]string          // artifact ID → module that keeps owning it
	releasing map[string]ledger.Artifact // artifact ID → old artifact of an owner being replanned
	claimed   map[string]string          // artifact ID → owner desiring it in this plan
	arrays    map[ledger.FileRef]bool    // JSON arrays lola created: recorded, or created by this plan
	desired   map[string][]ledger.Artifact
	steps     []Step
	conflicts []Conflict
	twin      bool // a module claimed one destination twice
}

// owned is one replanned owner's old artifacts, keyed by artifact ID.
type owned struct {
	owner string
	old   map[string]ledger.Artifact
}

// Compute diffs the desired intents against the ledger and the disk. It returns a
// *ConflictError (with hints) when any destination is unsafe to touch, and a plain error
// for unreadable or unparseable files.
func Compute(req Request) (*Plan, error) {
	p := &planner{req: req, files: map[string]*file{}, owners: map[string]string{}, releasing: map[string]ledger.Artifact{},
		claimed: map[string]string{}, arrays: map[ledger.FileRef]bool{}, desired: map[string][]ledger.Artifact{}}
	for _, r := range req.Ledger.Created {
		if r.Key != "" {
			p.arrays[r] = true
		}
	}
	var replanned []owned
	var intents [][]Intent
	for _, c := range req.Changes {
		o := owned{owner: c.Name, old: map[string]ledger.Artifact{}}
		if e := req.Ledger.Modules[c.Name]; e != nil {
			for _, a := range e.Artifacts {
				if slices.Contains(c.Targets, a.Target) {
					o.old[a.ID()] = a
				}
			}
		}
		replanned, intents = append(replanned, o), append(intents, c.Intents)
	}

	for _, o := range replanned {
		for id, a := range o.old {
			p.releasing[id] = a
		}
	}
	for name, e := range req.Ledger.Modules {
		for _, a := range e.Artifacts {
			if _, ok := p.releasing[a.ID()]; !ok {
				p.owners[a.ID()] = name
			}
		}
	}

	// Pass 1: every desired intent, so ownership can move between replanned owners.
	for i, o := range replanned {
		for _, in := range intents[i] {
			ins, err := expand(in)
			if err != nil {
				return nil, err
			}
			for _, x := range ins {
				if err := p.want(o.owner, x); err != nil {
					return nil, err
				}
			}
		}
	}
	// Pass 2: orphans — old artifacts nobody claimed.
	for _, o := range replanned {
		ids := make([]string, 0, len(o.old))
		for id := range o.old {
			if _, ok := p.claimed[id]; !ok {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		for _, id := range ids {
			if err := p.drop(o.owner, o.old[id]); err != nil {
				return nil, err
			}
		}
	}
	// Check for symlink aliasing: multiple logical destinations resolving to the same real file
	// Iterate in p.order for determinism.
	absToFirstKey := make(map[string]string) // abs → first key seen
	for _, key := range p.order {
		f := p.files[key]
		if f.touched {
			if firstKey, ok := absToFirstKey[f.res.Abs]; ok {
				// This abs was seen before with a different logical path
				other := p.files[firstKey]
				p.conflicts = append(p.conflicts, Conflict{Target: f.target, Path: f.logical,
					Reason:    fmt.Sprintf("resolves to the same file as %s %s (via symlink); refusing to write it twice", other.target, other.logical),
					Forceable: false})
			} else {
				absToFirstKey[f.res.Abs] = key
			}
		}
	}
	if err := p.conflictError(); err != nil {
		return nil, err
	}
	return p.finish()
}

// expand turns a CopyTree into one WriteFile per regular file. Reads go through os.Root, so
// nothing outside Src can be reached even if the tree changes during the walk.
func expand(in Intent) (out []Intent, err error) {
	if in.Op != OpCopyTree {
		return []Intent{in}, nil
	}
	root, err := os.OpenRoot(in.Src)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	err = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == "." {
			return err
		}
		if slices.Contains(in.Exclude, p) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		data, err := root.ReadFile(p)
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, Intent{Target: in.Target, Kind: in.Kind, Op: OpWriteFile, Path: path.Join(in.Path, p),
			Data: data, Mode: fsx.NormalizeMode(fi.Mode())})
		return nil
	})
	return out, err
}

// artifact is the ledger record of what in desires, without its hash. An array member is
// recorded in canonical JSON: a string as Value, any other value as Member.
func artifact(in Intent) (ledger.Artifact, error) {
	a := ledger.Artifact{Target: in.Target, Kind: in.Kind, Path: in.Path, Key: in.Pointer}
	if in.Op != OpAddJSONMember {
		return a, nil
	}
	c, err := fsx.CanonicalJSON(in.Member)
	if err != nil {
		return a, fmt.Errorf("%s %s: member: %w", in.Path, in.Pointer, err)
	}
	if c[0] != '"' {
		a.Member = c
		return a, nil
	}
	if err := json.Unmarshal(c, &a.Value); err != nil || a.Value == "" {
		return a, fmt.Errorf("%s %s: member: an empty string", in.Path, in.Pointer)
	}
	return a, nil
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// hashOf hashes what currently occupies a destination; "" means absent. elem, canonical JSON,
// names an owned element of the array at key.
func hashOf(content []byte, exists bool, key string, elem []byte) (string, error) {
	switch {
	case !exists:
		return "", nil
	case elem != nil:
		found, err := fsx.JSONHasMember(content, key, elem)
		if err != nil || !found {
			return "", err
		}
		return sum(elem), nil
	case key != "":
		v, found, err := fsx.JSONGet(content, key)
		if err != nil || !found {
			return "", err
		}
		return sum(v), nil
	default:
		return sum(content), nil
	}
}

// Observe returns the hash of what currently occupies a's destination under root ("" when
// it is absent), comparable with a.SHA256.
func Observe(root string, a ledger.Artifact) (string, error) {
	res, err := fsx.Resolve(root, a.Path)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(res.Abs)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	h, err := hashOf(b, true, a.Key, a.Element())
	if err != nil {
		return "", fmt.Errorf("%s: %w", a.Path, err)
	}
	return h, nil
}

func (p *planner) file(target, logical string) (*file, error) {
	key := target + "\x00" + logical
	if f := p.files[key]; f != nil {
		return f, nil
	}
	root, ok := p.req.Roots[target]
	if !ok {
		return nil, fmt.Errorf("no scope root for target %q", target)
	}
	res, err := fsx.Resolve(root, logical)
	if err != nil {
		return nil, err
	}
	f := &file{target: target, logical: logical, res: res, mode: 0o644}
	f.created = slices.Contains(p.req.Ledger.Created, ledger.FileRef{Target: target, Path: logical})
	b, err := os.ReadFile(res.Abs)
	switch {
	case err == nil:
		fi, err := os.Stat(res.Abs)
		if err != nil {
			return nil, err
		}
		f.exists, f.orig, f.cur = true, b, b
		f.mode, f.origMode = fi.Mode().Perm(), fi.Mode().Perm()
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}
	p.files[key] = f
	p.order = append(p.order, key)
	return f, nil
}

// unparseable is the error for a destination lola cannot read or edit as it must.
func unparseable(logical string, err error) error {
	return errs.WithHints(fmt.Errorf("%s: %w", logical, err),
		"lola never rewrites a file it cannot parse; fix it by hand and retry (--force does not override this)")
}

func (p *planner) hash(f *file, key string, elem []byte) (string, error) {
	h, err := hashOf(f.orig, f.exists, key, elem)
	if err != nil {
		return "", unparseable(f.logical, err)
	}
	return h, nil
}

func desiredHash(in Intent, a ledger.Artifact) (string, error) {
	switch in.Op {
	case OpSetJSON:
		c, err := fsx.CanonicalJSON(in.Value)
		if err != nil {
			return "", fmt.Errorf("%s %s: %w", in.Path, in.Pointer, err)
		}
		return sum(c), nil
	case OpAddJSONMember:
		return sum(a.Element()), nil
	default:
		return sum(in.Data), nil
	}
}

// shown is an artifact's array element as a conflict shows it: the string, or canonical JSON.
func shown(a ledger.Artifact) string {
	if a.Member != nil {
		return string(a.Member)
	}
	return a.Value
}

func (p *planner) conflict(a ledger.Artifact, reason string, forceable bool) {
	p.conflicts = append(p.conflicts, Conflict{Target: a.Target, Path: a.Path, Key: a.Key, Value: shown(a), Reason: reason, Forceable: forceable})
}

// want plans one desired intent, following the conflict matrix of spec §9. The previous
// artifact may come from another replanned owner: ownership then moves without a conflict. A
// destination claimed twice in one operation is a conflict, also when one module claims it
// twice (Plan 5b design §3.2).
func (p *planner) want(owner string, in Intent) error {
	art, err := artifact(in)
	if err != nil {
		return err
	}
	id := art.ID()
	var prev *ledger.Artifact
	if a, ok := p.releasing[id]; ok {
		prev = &a
	}
	if other, ok := p.claimed[id]; ok {
		// One module claiming a destination twice would otherwise be written by its last claim.
		reason := fmt.Sprintf("provided twice by module %q", owner)
		p.twin = p.twin || other == owner
		if other != owner {
			reason = fmt.Sprintf("also provided by module %q in this operation", other)
		}
		p.conflict(art, reason, false)
		return nil
	}
	p.claimed[id] = owner
	if other, ok := p.owners[id]; ok && other != owner {
		p.conflict(art, fmt.Sprintf("owned by module %q; uninstall it first", other), false)
		return nil
	}
	f, err := p.file(in.Target, in.Path)
	if err != nil {
		return err
	}
	cur, err := p.hash(f, in.Pointer, art.Element())
	if err != nil {
		return err
	}
	want, err := desiredHash(in, art)
	if err != nil {
		return err
	}
	art.SHA256 = want
	if f.res.Outside {
		art.Resolved = f.res.Abs
	}
	p.desired[owner] = append(p.desired[owner], art)

	step, err := stepOf(owner, art)
	if err != nil {
		return err
	}

	// Detect mode-only changes
	desiredMode := fsx.NormalizeMode(in.Mode)
	modeDiffers := in.Op == OpWriteFile && f.exists && fsx.NormalizeMode(f.origMode) != desiredMode

	switch {
	case cur == want && prev != nil && !modeDiffers:
		return nil // already in the desired state
	case cur == want && art.Member != nil:
		// A string member is a reference lola may share; any other member, such as a hook
		// group, is the user's own configuration, which owning would delete on uninstall.
		step.Action, step.Forced = ActionAdopt, "unowned"
		p.conflict(art, "exists and was not written by lola; owning it would remove it on uninstall", true)
	case cur == want && !modeDiffers:
		step.Action = ActionAdopt
	case cur == want && modeDiffers:
		step.Action = ActionUpdate
		step.Note = "mode changed"
	case cur == "" && prev != nil && art.Member != nil && !p.req.Force:
		// A non-string member is identified by its content, so one the user removed is
		// usually one the user edited; writing it again would run both copies (a hook group).
		// It stays owned but absent until --force restores it.
		step.Action, step.Note = ActionAbsent, "missing; --force restores it"
		p.steps = append(p.steps, step)
		return nil
	case cur == "":
		step.Action = ActionCreate
		if prev != nil {
			step.Note = "was missing; rewritten"
			if art.Member != nil {
				step.Forced = "missing"
			}
		}
	case prev != nil && cur == prev.SHA256:
		step.Action = ActionUpdate
	case prev != nil:
		step.Action, step.Forced = ActionUpdate, "hand-edited"
		p.conflict(art, "hand-edited since lola wrote it", true)
	default:
		step.Action, step.Forced = ActionUpdate, "unowned"
		p.conflict(art, "exists and was not written by lola", true)
	}
	if f.res.Outside {
		step.Via = f.res.Abs
	}
	p.steps = append(p.steps, step)
	if step.Action == ActionAdopt {
		return nil
	}
	return p.edit(f, in, art)
}

func (p *planner) edit(f *file, in Intent, art ledger.Artifact) error {
	var err error
	switch in.Op {
	case OpSetJSON:
		f.content = contentJSON
		f.cur, err = fsx.JSONSet(f.cur, in.Pointer, in.Value)
	case OpAddJSONMember:
		f.content = contentJSON
		var created bool
		f.cur, created, err = fsx.JSONAddMember(f.cur, in.Pointer, art.Element())
		if created {
			p.arrays[ledger.FileRef{Target: in.Target, Path: in.Path, Key: in.Pointer}] = true
		}
	default:
		f.cur = in.Data
		f.mode = fsx.NormalizeMode(in.Mode)
	}
	if err != nil {
		return unparseable(in.Path, err)
	}
	if !f.exists && in.Op != OpWriteFile {
		f.created = true
	}
	f.touched, f.del = true, false
	return nil
}

// stepOf is the plan step for artifact a of owner. A non-string member is shown as written
// (fsx.ReadableJSON), not as the canonical JSON the ledger records.
func stepOf(owner string, a ledger.Artifact) (Step, error) {
	step := Step{Module: owner, Target: a.Target, Kind: a.Kind, Path: a.Path, Key: a.Key, Value: a.Value}
	if a.Member != nil {
		m, err := fsx.ReadableJSON(a.Member)
		if err != nil {
			return step, fmt.Errorf("%s %s: member: %w", a.Path, a.Key, err)
		}
		step.Member = m
	}
	return step, nil
}

// drop plans the removal of an artifact nobody desires any more.
func (p *planner) drop(owner string, a ledger.Artifact) error {
	f, err := p.file(a.Target, a.Path)
	if err != nil {
		return err
	}
	cur, err := p.hash(f, a.Key, a.Element())
	if err != nil {
		return err
	}
	step, err := stepOf(owner, a)
	if err != nil {
		return err
	}
	if f.res.Outside {
		step.Via = f.res.Abs
	}
	switch {
	case cur == "":
		step.Action, step.Note = ActionForget, "already gone"
		p.steps = append(p.steps, step)
		return nil
	case cur != a.SHA256:
		step.Forced = "hand-edited"
		p.conflict(a, "hand-edited since lola wrote it; kept", true)
	}
	step.Action = ActionDelete
	p.steps = append(p.steps, step)
	switch {
	case a.Element() != nil:
		f.content = contentJSON
		dropEmpty := p.arrays[ledger.FileRef{Target: a.Target, Path: a.Path, Key: a.Key}]
		f.cur, err = fsx.JSONRemoveMember(f.cur, a.Key, a.Element(), dropEmpty)
	case a.Key != "":
		f.content = contentJSON
		f.cur, err = fsx.JSONDelete(f.cur, a.Key)
	default:
		f.del = true
	}
	if err != nil {
		return fmt.Errorf("%s: %w", a.Path, err)
	}
	f.touched = true
	return nil
}

// conflictError returns nil when the plan may proceed: no conflicts, or only forceable ones
// under --force.
func (p *planner) conflictError() error {
	if len(p.conflicts) == 0 {
		return nil
	}
	forceable := !slices.ContainsFunc(p.conflicts, func(c Conflict) bool { return !c.Forceable })
	if forceable && p.req.Force {
		return nil
	}
	hint := "uninstall the module that owns these destinations first; --force cannot override ownership"
	if p.twin {
		hint = "two artifacts in this module render to the same place; rename one"
	}
	if forceable {
		hint = "rerun with --force to overwrite or delete them anyway (each one is listed in the summary)"
	}
	return errs.WithHints(&ConflictError{Conflicts: p.conflicts}, hint)
}

func (p *planner) finish() (*Plan, error) {
	out := &Plan{Steps: p.steps, Ledger: p.req.Ledger.Clone()}
	created := map[ledger.FileRef]bool{}
	for _, r := range p.req.Ledger.Created {
		created[r] = true
	}
	for _, key := range p.order {
		f := p.files[key]
		ref := ledger.FileRef{Target: f.target, Path: f.logical}
		if f.touched && !f.del && f.created {
			empty := false
			if f.content == contentJSON {
				e, err := fsx.JSONIsEmptyObject(f.cur)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", f.logical, err)
				}
				empty = e
			}
			f.del = empty
		}
		created[ref] = f.created && !f.del
		if !f.touched || (!f.del && f.exists && bytes.Equal(f.cur, f.orig) && (f.content != contentFile || f.mode == fsx.NormalizeMode(f.origMode))) {
			continue
		}
		if f.del && !f.exists {
			continue
		}
		stops, err := p.stopDirs(f.target)
		if err != nil {
			return nil, err
		}
		out.Files = append(out.Files, FileChange{Target: f.target, Logical: f.logical, Abs: f.res.Abs, Root: p.req.Roots[f.target],
			Outside: f.res.Outside, Delete: f.del, Data: f.cur, Mode: f.mode, StopDirs: stops})
	}
	sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].Abs < out.Files[j].Abs })
	for ref := range p.arrays {
		keep, err := p.arrayRemains(ref)
		if err != nil {
			return nil, err
		}
		created[ref] = keep
	}
	for i := range out.Files {
		if out.Files[i].Delete {
			out.Files[i].Data, out.Files[i].Mode = nil, 0
		}
	}
	p.updateLedger(out.Ledger, created)
	sort.SliceStable(out.Steps, func(i, j int) bool {
		a, b := out.Steps[i], out.Steps[j]
		return a.Module+"\x00"+a.Target+"\x00"+a.Path+"\x00"+a.Key < b.Module+"\x00"+b.Target+"\x00"+b.Path+"\x00"+b.Key
	})
	return out, nil
}

// arrayRemains reports whether a JSON array lola created still exists once the plan is
// applied, so the ledger keeps remembering it.
func (p *planner) arrayRemains(ref ledger.FileRef) (bool, error) {
	f := p.files[ref.Target+"\x00"+ref.Path]
	switch {
	case f == nil || !f.touched:
		return true, nil
	case f.del:
		return false, nil
	}
	_, found, err := fsx.JSONGet(f.cur, ref.Key)
	if err != nil {
		return false, fmt.Errorf("%s: %w", f.logical, err)
	}
	return found, nil
}

func (p *planner) stopDirs(target string) ([]string, error) {
	root := p.req.Roots[target]
	r, err := fsx.Resolve(root, ".")
	if err != nil {
		return nil, err
	}
	stops := []string{r.Abs}
	for _, base := range p.req.BaseDirs[target] {
		b, err := fsx.Resolve(root, base)
		if err != nil {
			return nil, err
		}
		stops = append(stops, b.Abs)
	}
	return stops, nil
}

func (p *planner) updateLedger(l *ledger.Ledger, created map[ledger.FileRef]bool) {
	for _, c := range p.req.Changes {
		var kept []ledger.Artifact
		old := l.Modules[c.Name]
		if old != nil {
			for _, a := range old.Artifacts {
				if !slices.Contains(c.Targets, a.Target) {
					kept = append(kept, a)
				}
			}
		}
		all := append(kept, p.desired[c.Name]...)
		switch {
		case c.Entry != nil:
			e := *c.Entry
			e.Artifacts = all
			l.Modules[c.Name] = &e
		case len(kept) > 0:
			old.Artifacts = kept
		default:
			delete(l.Modules, c.Name)
		}
	}
	l.Created = l.Created[:0]
	for ref, ok := range created {
		if ok {
			l.Created = append(l.Created, ref)
		}
	}
	sort.Slice(l.Created, func(i, j int) bool { return l.Created[i].ID() < l.Created[j].ID() })
}
