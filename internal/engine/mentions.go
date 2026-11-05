package engine

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Limits of the mention scan (cherry-pick design §5.1).
const (
	mentionMinName = 4        // bytes; a shorter name is not searched
	mentionFileMax = 1 << 20  // bytes; a larger file is skipped
	mentionProbe   = 8 << 10  // bytes; a NUL among the first this many marks a file binary, skipped
	mentionBudget  = 16 << 20 // bytes read per module; the text that would pass it, and every later one, is skipped
)

// Mentions maps a component of a module to the module's other components its text names
// (cherry-pick design §5.1), each list sorted by kind/name; a component that names none has no
// key. A mention is information: never a dependency.
type Mentions map[Component][]Component

// mentionReads keeps what scanMentions read of each version (by name@ID), a failure with it, so
// that a run reads a version once: StagedRun.scans shares one between an install's pickers, its
// review and its apply.
type mentionReads map[string]mentionRead

// mentionRead is one version's scan: what it found, or why it could not read the version.
type mentionRead struct {
	mentions Mentions
	err      error
}

// read is what v's components mention, scanned on the first call for v and kept, a failure too.
func (r mentionReads) read(v store.Version) (Mentions, error) {
	id := v.Name + "@" + v.ID
	got, ok := r[id]
	if !ok {
		got.mentions, got.err = scanMentions(v)
		r[id] = got
	}
	return got.mentions, got.err
}

// scanMentions reads v's content directory, a cached version's or one staged in an install's
// run, for the mentions of v's components: a skill's main doc (SKILL.md or its
// template) and every other file but those its Exclude lists, a command's file (an inline one's
// document) and its sidecar directory's files, an agent's file and a rule's body (the loader keeps
// no rule file). MCP server definitions are not read. A name is searched when it is
// mentionMinName bytes or longer, and found only as a whole token (nameMatcher). Components
// are read in picker order and each directory in lexical order, so the budget skips the same files
// every time. Only regular files are read; one that cannot be read is an error.
func scanMentions(v store.Version) (_ Mentions, err error) {
	names := map[string][]Component{}
	for _, c := range components(v.Module) {
		if len(c.Name) >= mentionMinName {
			names[c.Name] = append(names[c.Name], c)
		}
	}
	out := Mentions{}
	if len(names) == 0 {
		return out, nil
	}
	s := &mentionScan{names: newNameMatcher(names), left: mentionBudget, found: map[Component]map[Component]bool{}}
	root, err := os.OpenRoot(v.ContentDir)
	if err != nil {
		return nil, mentionErr(v.Name, err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	s.fsys = root.FS()
	m := v.Module
	for _, sk := range m.Skills {
		comp := Component{config.KindSkill, sk.Name}
		main := path.Join(filepath.ToSlash(sk.Dir), "SKILL.md")
		if sk.Doc.Template != nil {
			main = sk.Doc.Template.File
		}
		if err := s.file(comp, main); err != nil {
			return nil, mentionErr(v.Name, err)
		}
		if err := s.tree(comp, sk.Dir, sk.Exclude, main); err != nil {
			return nil, mentionErr(v.Name, err)
		}
	}
	for _, c := range m.Commands {
		comp := Component{config.KindCommand, c.Name}
		if c.File == "" {
			s.text(comp, []byte(c.Doc.Render())) // an inline command has no file
		} else if err := s.file(comp, c.File); err != nil {
			return nil, mentionErr(v.Name, err)
		}
		if c.SidecarDir != "" {
			if err := s.tree(comp, c.SidecarDir, nil, ""); err != nil {
				return nil, mentionErr(v.Name, err)
			}
		}
	}
	for _, a := range m.Agents {
		if err := s.file(Component{config.KindAgent, a.Name}, a.File); err != nil {
			return nil, mentionErr(v.Name, err)
		}
	}
	for _, r := range m.Rules {
		s.text(Component{config.KindRule, r.Name}, []byte(r.Body))
	}
	for c, set := range s.found {
		out[c] = slices.SortedFunc(maps.Keys(set), func(a, b Component) int { return strings.Compare(a.String(), b.String()) })
	}
	return out, nil
}

// mentionErr says which module's mentions could not be read, and why without the file names the
// cause carries (they come from the module's author).
func mentionErr(name string, err error) error {
	return fmt.Errorf("%s: cannot read what its components mention: %w", untrusted.Text(name, untrusted.MaxName), untrusted.CleanErr(err))
}

// mentionScan is one module's scan: the names searched; the bytes it may still read, and whether
// the budget ended the scan (done); and the mentions found so far.
type mentionScan struct {
	fsys  fs.FS
	names *nameMatcher
	left  int
	done  bool
	found map[Component]map[Component]bool
}

// tree reads, as c's text, every regular file under dir (content-relative) but the dir-relative
// slash paths exclude lists, as the copy of a skill skips them (plan.expand), and but skip, a
// content-relative file the caller read already ("" for none).
func (s *mentionScan) tree(c Component, dir string, exclude []string, skip string) error {
	dir = filepath.ToSlash(dir)
	return fs.WalkDir(s.fsys, dir, func(p string, d fs.DirEntry, err error) error {
		if s.done {
			return fs.SkipAll
		}
		if err != nil || p == dir {
			return err
		}
		rel := p
		if dir != "." {
			rel = strings.TrimPrefix(p, dir+"/")
		}
		if slices.Contains(exclude, rel) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || p == skip {
			return nil
		}
		return s.file(c, p)
	})
}

// file reads rel, content-relative, as c's text. One that is not a regular file (a symlink), or
// is over mentionFileMax bytes, is skipped unread; one that is binary is skipped after its first
// mentionProbe bytes, and one that does not fit what is left of the budget after them, which ends
// the scan. Once the scan has ended no file is opened.
func (s *mentionScan) file(c Component, rel string) (err error) {
	if s.done {
		return nil
	}
	rel = filepath.ToSlash(rel)
	fi, err := fs.Lstat(s.fsys, rel)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() || fi.Size() > mentionFileMax {
		return nil
	}
	f, err := s.fsys.Open(rel)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	head, err := io.ReadAll(io.LimitReader(f, mentionProbe))
	if err != nil {
		return err
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return nil
	}
	if fi.Size() > int64(s.left) {
		s.done = true
		return nil
	}
	rest, err := io.ReadAll(io.LimitReader(f, mentionFileMax+1-int64(len(head))))
	if err != nil {
		return err
	}
	s.text(c, append(head, rest...))
	return nil
}

// text searches b, c's text, for the names of the module's other components. It skips b when b is
// over mentionFileMax bytes or binary (a NUL in its first mentionProbe bytes), and when b does not
// fit what is left of the module's budget, which then ends: every later text is skipped too.
func (s *mentionScan) text(c Component, b []byte) {
	if s.done || len(b) > mentionFileMax || bytes.IndexByte(b[:min(len(b), mentionProbe)], 0) >= 0 {
		return
	}
	if len(b) > s.left {
		s.done = true
		return
	}
	s.left -= len(b)
	for _, o := range s.names.find(b) {
		if o == c {
			continue
		}
		if s.found[c] == nil {
			s.found[c] = map[Component]bool{}
		}
		s.found[c][o] = true
	}
}

// ComponentMentions is a component (kind/name) and what it mentions, sorted, as lola info shows
// them (cherry-pick design §5.2).
type ComponentMentions struct {
	Component string   `json:"component"`
	Mentions  []string `json:"mentions"`
}

// componentMentions lists, in picker order, each component of v that mentions others, with what
// it mentions (ms, scanMentions'); [] when none. Names go through untrusted.Text.
func componentMentions(v store.Version, ms Mentions) []ComponentMentions {
	out := []ComponentMentions{}
	for _, c := range components(v.Module) {
		if len(ms[c]) == 0 {
			continue
		}
		cm := ComponentMentions{Component: untrusted.Text(c.String(), untrusted.MaxName), Mentions: []string{}}
		for _, o := range ms[c] {
			cm.Mentions = append(cm.Mentions, untrusted.Text(o.String(), untrusted.MaxName))
		}
		out = append(out, cm)
	}
	return out
}

// mentionGap is a component a selection installs and the components it mentions that the
// selection leaves out.
type mentionGap struct {
	by   Component
	left []Component
}

// mentionModule is what the mention handling knows of a module at a scope: what its components
// mention, whether its rules install there, and the words a hint installs it with (mentionArgs).
// scanErr is why its mentions could not be read (mentionErr); the install goes on without them.
type mentionModule struct {
	mentions Mentions
	rulesOn  bool
	words    []string
	scanErr  error
}

// mentionGaps lists, in comps' order, each component x installs (a rule only when rulesOn) that
// mentions components x leaves out, with those (cherry-pick design §5.2). A rule is never left
// out while rules are off: no --unskip installs it then.
func mentionGaps(comps []Component, ms Mentions, x config.Exclusions, rulesOn bool) []mentionGap {
	ruled := func(c Component) bool { return rulesOn || c.Kind != config.KindRule }
	var out []mentionGap
	for _, c := range comps {
		if !ruled(c) || x.Excludes(c.Kind, c.Name) {
			continue
		}
		var left []Component
		for _, o := range ms[c] {
			if ruled(o) && x.Excludes(o.Kind, o.Name) {
				left = append(left, o)
			}
		}
		if len(left) > 0 {
			out = append(out, mentionGap{by: c, left: left})
		}
	}
	return out
}

// namedList names cs (kind/name through untrusted.Text): the first maxNamed, then how many more.
func namedList(cs []Component) string {
	shown := cs[:min(len(cs), maxNamed)]
	names := make([]string, 0, len(shown))
	for _, c := range shown {
		names = append(names, untrusted.Text(c.String(), untrusted.MaxName))
	}
	out := strings.Join(names, ", ")
	if more := len(cs) - len(shown); more > 0 {
		out += fmt.Sprintf(" and %d more", more)
	}
	return out
}

// mentionArgs is the words a hint installs module name, version v, with: its name, which the
// cache holds or the run's apply commits; but for a version run only staged, r, when the run may
// end without committing it (unsure: a dry run, or a review the user may decline), the argument as
// given, with its --ref and --path, which fetches it again.
func mentionArgs(name string, v store.Version, r InstallRequest, run *StagedRun, unsure bool) []string {
	if !unsure || !run.holds(v) {
		return []string{name}
	}
	args := []string{r.Arg}
	if r.Ref != "" {
		args = append(args, "--ref", r.Ref)
	}
	if r.Path != "" {
		args = append(args, "--path", r.Path)
	}
	return args
}

// selectionFlags is the selection flags of the run, edits (a scope's) and global (given with
// --global), that a hint installing module t repeats: under -n (dry) every --skip, --only and
// --unskip flag, since a dry run saved none of them; else each --skip flag, which keeps out again
// what --with-mentions would add (an untick on the component page was saved, and does not). Only
// a flag that selects a component of t, since the hint installs t alone; then --global, after
// flags of global (a run gives its flags to one section, AddFlagEdits), and --exact after an exact
// --only.
func selectionFlags(t editTarget, edits, global []SelectionEdit, dry bool) []string {
	var out []string
	var isGlobal, exact bool
	for i, ed := range slices.Concat(global, edits) {
		if !ed.Flag || !dry && ed.Op != EditSkip || len(matching(ed.Selector, t)) == 0 {
			continue
		}
		out = append(out, "--"+ed.Op, ed.Selector.String())
		isGlobal, exact = isGlobal || i < len(global), exact || ed.Exact
	}
	if isGlobal {
		out = append(out, "--global")
	}
	if exact {
		out = append(out, "--exact")
	}
	return out
}

// mentionNotice names, in one info notice, what gaps, module name's at sc, leave out, with the
// install that adds them (cherry-pick design §5.2): lola install (or the run's lola profile <name>
// --edit, installRun) with the run's --profile, sc's flags, words (mentionArgs),
// --with-mentions and flags (selectionFlags), keeping the run's -t, --rules or --no-rules, -n and
// output flags. The mentioners are in picker order, the components left out sorted by kind/name.
// None without gaps.
func (e *Engine) mentionNotice(sc Scope, name string, words, flags []string, gaps []mentionGap, opts Options) []Notice {
	if len(gaps) == 0 {
		return nil
	}
	var by, left []Component
	for _, g := range gaps {
		by = append(by, g.by)
		for _, c := range g.left {
			if !slices.Contains(left, c) {
				left = append(left, c)
			}
		}
	}
	slices.SortFunc(left, func(a, b Component) int { return strings.Compare(a.String(), b.String()) })
	verb, what := "mentions", "components"
	if len(by) > 1 {
		verb = "mention"
	}
	if len(left) == 1 {
		what = "component"
	}
	args := slices.Concat(words, []string{"--with-mentions"}, flags)
	if len(opts.Targets) > 0 {
		args = append(args, "-t", strings.Join(opts.Targets, ","))
	}
	switch {
	case opts.Rules != nil && *opts.Rules:
		args = append(args, "--rules")
	case opts.Rules != nil:
		args = append(args, "--no-rules")
	}
	if opts.DryRun {
		args = append(args, "-n")
	}
	args = append(args, opts.Display...)
	return []Notice{{Level: LevelInfo, Text: fmt.Sprintf("%s: %s %s %d %s you did not pick: %s → add them: %s",
		untrusted.Text(name, untrusted.MaxName), namedList(by), verb, len(left), what, namedList(left), e.installRun(sc, opts.Target, args...))}}
}

// resolvedMentions is, for each module sp resolved, what its version's components mention (read
// once per version and kept in scans, a failed read with it; the version is the one the install
// writes, staged or cached), whether its rules install at sp's scope (its entry's setting, an
// entry the trust record does not cover counting as none as planInstallModule writes it new,
// unless --rules or --no-rules says) and the words a hint installs it with. A scan that fails
// never fails the install: the module carries scanErr and no mentions.
func resolvedMentions(sp *scopePlan, spec InstallSpec, scans mentionReads) map[string]mentionModule {
	out := map[string]mentionModule{}
	for _, rr := range sp.resolved {
		var mm mentionModule
		mm.mentions, mm.scanErr = scans.read(rr.v)
		ent := sp.target.layerEntry(rr.v.Name)
		if ent != nil && !sp.covers(*ent) {
			ent = nil
		}
		mm.rulesOn = ent.RulesOn()
		if spec.Options.Rules != nil {
			mm.rulesOn = *spec.Options.Rules
		}
		mm.words = mentionArgs(rr.v.Name, rr.v, rr.req, spec.Staged, spec.Options.DryRun)
		out[rr.v.Name] = mm
	}
	return out
}

// mentionNotes adds to dst, each once, what editScope said of the mentions at si (se.added) and,
// per module of targets, a warn notice when its mentions could not be read (mods' scanErr), else,
// with spec.NameMentions, the notice of what its picks mention and leave out (mentionNotice), its
// hint repeating the selection flags of si and spec (selectionFlags). Two scopes that say the
// same thing say it once.
func (e *Engine) mentionNotes(dst []Notice, si ScopeInstall, se scopeEdits, targets []editTarget, mods map[string]mentionModule, spec InstallSpec) []Notice {
	notes := slices.Clone(se.added)
	for _, t := range targets {
		mm := mods[t.name]
		switch {
		case mm.scanErr != nil:
			notes = append(notes, Notice{Level: LevelWarn, Text: mm.scanErr.Error()})
		case spec.NameMentions:
			flags := selectionFlags(t, si.Edits, spec.GlobalEdits, spec.Options.DryRun)
			notes = append(notes, e.mentionNotice(si.Scope, t.name, mm.words, flags, se.gaps[t.name], spec.Options)...)
		}
	}
	for _, n := range notes {
		if !slices.Contains(dst, n) {
			dst = append(dst, n)
		}
	}
	return dst
}
