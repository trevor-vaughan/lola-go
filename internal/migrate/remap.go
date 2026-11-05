package migrate

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// RootMap is one --map-root OLD=NEW (Plan 10 §3.1): a project path the Python lola recorded at
// or below Old, missing on this machine, is looked for at the same place below New.
type RootMap struct{ Old, New string }

// RootSuggestion is a root that missing project paths and local sources of the Python lola share
// (Plan 10 §3.1): Old as oldRoot finds it, the records whose project path or source is below it,
// and New, the root lola suggests (your home).
type RootSuggestion struct {
	Old, New string
	Records  []Record
}

// TargetSuggestion is an assistant the Python lola installed for that this machine lacks (Plan
// 10 §3.2): Old, its records, and Detected, the targets whose assistant is on your PATH, which
// lola suggests installing them for.
type TargetSuggestion struct {
	Old      string
	Detected []string
	Records  []Record
}

// drivePath matches a Windows absolute path, which another machine's installed.yml may hold.
var drivePath = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

// homeShaped matches the home directory a path is under: /home/<u>, /Users/<u>, /root or
// <drive>:\Users\<u>.
var homeShaped = regexp.MustCompile(`^(/home/[^/]+|/Users/[^/]+|/root|[A-Za-z]:[\\/]Users[\\/][^\\/]+)(?:[\\/]|$)`)

// foreignAbs reports whether p is absolute as another OS writes it: a Windows path, or a path
// from the root on Windows. Such a path never exists here.
func foreignAbs(p string) bool { return drivePath.MatchString(p) || strings.HasPrefix(p, "/") }

// sepAt reports whether p[i] separates two names of p: '/', or '\' in a Windows path.
func sepAt(p string, i int) bool { return p[i] == '/' || p[i] == '\\' && drivePath.MatchString(p) }

// oldRoot is the root another machine had for p, a project path missing here (Plan 10 §3.1):
// the home-shaped prefix p is under, unless it is home, this machine's; else the shallowest
// ancestor of p that does not exist here; else p. A Windows path is wholly missing on another OS,
// so its root is its first name after the drive, with nothing to look up.
func oldRoot(p, home string) string {
	if m := homeShaped.FindStringSubmatch(p); m != nil && m[1] != home {
		return m[1]
	}
	start := 1
	if drivePath.MatchString(p) {
		if !filepath.IsAbs(p) {
			if i := strings.IndexAny(p[3:], `/\`); i >= 0 {
				return p[:3+i]
			}
			return p
		}
		start = 3
	}
	for i := start; i < len(p); i++ {
		if !sepAt(p, i) {
			continue
		}
		if _, err := os.Lstat(p[:i]); errors.Is(err, fs.ErrNotExist) {
			return p[:i]
		}
	}
	return p
}

// winNorm is a Windows path with one separator and an upper-case drive letter, the form in which
// two spellings of the same path are equal.
func winNorm(p string) string {
	p = strings.ReplaceAll(p, `\`, "/")
	return strings.ToUpper(p[:1]) + p[1:]
}

// dotDot reports whether a name of p, split at either separator, is "..".
func dotDot(p string) bool {
	return slices.Contains(strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }), "..")
}

// rebase moves p, at or below old, to the same place below to; it reports false when p is not
// old or below it, or has a ".." name, which would leave to. Two Windows paths compare with
// either separator and any case of the drive letter.
func rebase(p, old, to string) (string, bool) {
	if drivePath.MatchString(p) && drivePath.MatchString(old) {
		p, old = winNorm(p), winNorm(old)
	}
	rest, ok := strings.CutPrefix(p, old)
	if !ok || rest != "" && !sepAt(p, len(old)) || dotDot(rest) {
		return "", false
	}
	if drivePath.MatchString(p) {
		rest = strings.ReplaceAll(rest, `\`, "/")
	}
	return filepath.Join(to, filepath.FromSlash(rest)), true
}

// moveBy is p moved by the map of roots with the longest Old that p is at or below, and that
// map's index; -1 when there is none. Every map that matches p is marked used, the shadowed ones
// too: they are not maps that match nothing.
func moveBy(p string, roots []RootMap, used map[int]bool) (string, int) {
	best, to := -1, ""
	for j, m := range roots {
		np, ok := rebase(p, m.Old, m.New)
		if !ok {
			continue
		}
		used[j] = true
		if best < 0 || len(m.Old) > len(roots[best].Old) {
			best, to = j, np
		}
	}
	return to, best
}

// rerooted is recs with roots applied (Plan 10 §3.1): a project record whose path is missing
// here, at or below the Old of a map (the longest that matches), moves to the same place below
// its New and is checked again as Load checks it. moved[i] says whether a map moved recs[i];
// used gains the index of each map that moved one.
func rerooted(recs []Record, roots []RootMap, home string, used map[int]bool) (out []Record, moved []bool) {
	out, moved = slices.Clone(recs), make([]bool, len(recs))
	for i, rec := range out {
		if !rec.Missing {
			continue
		}
		to, j := moveBy(rec.ProjectPath, roots, used)
		if j < 0 {
			continue
		}
		out[i].ProjectPath, moved[i] = to, true
		out[i].Problem, out[i].Missing = projectProblem(to, home)
	}
	return out, moved
}

// sourceMissing reports whether src is a local folder or archive source this machine does not
// have (another OS's absolute path never is here): what a --map-root moves.
func sourceMissing(src Source) bool {
	want, known := kinds[src.Type]
	if !known || want == source.KindGit || strings.Contains(src.Location, "://") || dotDot(src.Location) {
		return false
	}
	if !filepath.IsAbs(src.Location) {
		return foreignAbs(src.Location)
	}
	_, err := os.Lstat(src.Location)
	return errors.Is(err, fs.ErrNotExist)
}

// rerootedSources is sources with roots applied (Plan 10 §3.1): a local source missing here
// moves as a project path does, and mapSource checks it again. moved names the modules whose
// source a map moved; used gains the index of each map that moved one.
func rerootedSources(sources map[string]Source, roots []RootMap, used map[int]bool) (out map[string]Source, moved map[string]bool) {
	out, moved = maps.Clone(sources), map[string]bool{}
	for module, src := range out {
		if !sourceMissing(src) {
			continue
		}
		to, j := moveBy(src.Location, roots, used)
		if j < 0 {
			continue
		}
		src.Location = to
		out[module], moved[module] = src, true
	}
	return out, moved
}

// suggestRoots groups by old root, in order, the records whose project path or local source is
// missing here and was not moved by a map, each record once per root and each root with your
// home as the new one (Plan 10 §3.1): one answer per old root covers both.
func suggestRoots(e *engine.Engine, recs []Record, moved []bool, srcs map[string]Source, srcMoved map[string]bool) []RootSuggestion {
	out, at, listed := []RootSuggestion{}, map[string]int{}, map[string]map[int]bool{}
	add := func(old string, i int, rec Record) {
		if movesEverything(strings.TrimRight(old, `/\`)) {
			return
		}
		n, ok := at[old]
		if !ok {
			n, at[old], listed[old] = len(out), len(out), map[int]bool{}
			out = append(out, RootSuggestion{Old: old, New: e.Paths.Home})
		}
		if !listed[old][i] {
			listed[old][i] = true
			out[n].Records = append(out[n].Records, rec)
		}
	}
	for i, rec := range recs {
		if rec.Missing && !moved[i] {
			add(oldRoot(rec.ProjectPath, e.Paths.Home), i, rec)
		}
		if src, ok := srcs[rec.Module]; ok && !srcMoved[rec.Module] && sourceMissing(src) {
			add(oldRoot(src.Location, e.Paths.Home), i, rec)
		}
	}
	slices.SortFunc(out, func(a, b RootSuggestion) int { return strings.Compare(a.Old, b.Old) })
	return out
}

// Suggest is what NewPlan leaves unmapped for want of this machine's paths or assistants under
// opts' maps, found without fetching anything: the CLI asks about each group before it plans.
func Suggest(e *engine.Engine, st *State, opts Options) ([]RootSuggestion, []TargetSuggestion) {
	used := map[int]bool{}
	recs, moved := rerooted(st.Records, opts.Roots, e.Paths.Home, used)
	srcs, srcMoved := rerootedSources(st.Sources, opts.Roots, used)
	tsugs, _ := suggestTargets(e, recs, opts.Targets)
	return suggestRoots(e, recs, moved, srcs, srcMoved), tsugs
}

// suggestTargets groups by assistant, in order, the records of recs without a problem whose
// assistant lola has no target for, or is neither on your PATH nor set up where the record goes,
// and which targets does not map; each with the assistants on your PATH (Plan 10 §3.2). It does
// not check that the record's source can be installed. mapped names each assistant of targets
// that such a record has.
func suggestTargets(e *engine.Engine, recs []Record, targets map[string][]string) (out []TargetSuggestion, mapped map[string]bool) {
	out, mapped = []TargetSuggestion{}, map[string]bool{}
	at, here := map[string]int{}, map[string]map[string]bool{}
	detected := onPath(e)
	for _, rec := range recs {
		if rec.Problem != "" {
			continue
		}
		sc := scopeOf(rec)
		if here[sc.Key()] == nil {
			here[sc.Key()] = installedHere(e, sc)
		}
		if id, ok := targetOf(e, rec.Assistant); ok && here[sc.Key()][id] {
			continue
		}
		if _, ok := targets[rec.Assistant]; ok {
			mapped[rec.Assistant] = true
			continue
		}
		n, ok := at[rec.Assistant]
		if !ok {
			n, at[rec.Assistant] = len(out), len(out)
			out = append(out, TargetSuggestion{Old: rec.Assistant, Detected: slices.Clone(detected)})
		}
		out[n].Records = append(out[n].Records, rec)
	}
	slices.SortFunc(out, func(a, b TargetSuggestion) int { return strings.Compare(a.Old, b.Old) })
	return out, mapped
}

// installedHere is, by target, whether its assistant is on your PATH or set up at sc (its
// markers exist there), as lola tidy counts it (engine/tidy.go): lola migrate installs a record
// for its own assistant only where it is (Plan 10 §3.2).
func installedHere(e *engine.Engine, sc engine.Scope) map[string]bool {
	out := map[string]bool{}
	for _, a := range e.Detect(sc) {
		out[a.Target] = a.Binary != "" || a.Marked
	}
	return out
}

// onPath is the targets whose assistant is on your PATH, in registry order.
func onPath(e *engine.Engine) []string {
	out := []string{}
	for _, a := range e.Detect(engine.Scope{Kind: target.User}) {
		if a.Binary != "" {
			out = append(out, a.Target)
		}
	}
	return out
}

// targetOf is the lola target the Python lola's assistant renders as, when lola has it.
func targetOf(e *engine.Engine, assistant string) (string, bool) {
	id, ok := assistants[assistant]
	return id, ok && slices.ContainsFunc(e.Targets, func(t target.Target) bool { return t.ID() == id })
}

// mapRootPlaceholder is the --map-root a hint names when lola has no root to offer.
const mapRootPlaceholder = "--map-root <old root>=<where it is on this machine>"

// mapRootFlag is the --map-root flag that moves p, a path missing here, to the same place below
// home, as the user types it (Plan 10 §3.1): p's old root, as RootSuggestions offers it, =home;
// mapRootPlaceholder when that root is one --map-root refuses, as no suggestion offers it.
func mapRootFlag(p, home string) string {
	old := oldRoot(p, home)
	if movesEverything(strings.TrimRight(old, `/\`)) {
		return mapRootPlaceholder
	}
	return errs.ShellJoin([]string{"--map-root", untrusted.Text(old, untrusted.MaxName) + "=" + untrusted.Text(home, untrusted.MaxName)})
}

// movesEverything reports whether old, without trailing separators, is a filesystem or drive
// root, which would match every path; ParseRootMaps refuses it, so no suggestion may offer it.
func movesEverything(old string) bool {
	return old == "" || len(old) == 2 && old[1] == ':'
}

// ParseRootMaps reads --map-root OLD=NEW flags (Plan 10 §3.1): OLD an absolute path as this
// machine or another OS writes it, kept without a trailing separator, and NEW an absolute path
// here, kept clean.
func ParseRootMaps(args []string) ([]RootMap, error) {
	out := []RootMap{}
	for _, a := range args {
		shown := untrusted.Text(a, untrusted.MaxName)
		old, to, ok := strings.Cut(a, "=")
		switch {
		case !ok || old == "" || to == "":
			return nil, fmt.Errorf("--map-root %q is not OLD=NEW: write the root the Python lola's project paths start with, =, and where it is on this machine, such as --map-root /Users/alice=/home/alice", shown)
		case !filepath.IsAbs(old) && !foreignAbs(old):
			return nil, fmt.Errorf("--map-root %q: %q is not an absolute path", shown, untrusted.Text(old, untrusted.MaxName))
		case !filepath.IsAbs(to):
			return nil, fmt.Errorf("--map-root %q: %q is not an absolute path on this machine", shown, untrusted.Text(to, untrusted.MaxName))
		}
		trimmed := strings.TrimRight(old, `/\`)
		if movesEverything(trimmed) {
			return nil, fmt.Errorf("--map-root %q would move every path: name the directory the Python lola's project paths start with, such as /Users/alice", shown)
		}
		out = append(out, RootMap{Old: trimmed, New: filepath.Clean(to)})
	}
	return out, nil
}

// ParseTargetMaps reads --map-target OLD=NEW[,NEW] flags (Plan 10 §3.2): OLD an assistant the
// Python lola names, given once, and each NEW a target of e, kept once each.
func ParseTargetMaps(e *engine.Engine, args []string) (map[string][]string, error) {
	ids := make([]string, 0, len(e.Targets))
	for _, t := range e.Targets {
		ids = append(ids, t.ID())
	}
	out := map[string][]string{}
	for _, a := range args {
		shown := untrusted.Text(a, untrusted.MaxName)
		old, list, ok := strings.Cut(a, "=")
		if !ok || old == "" || list == "" {
			return nil, fmt.Errorf("--map-target %q is not OLD=NEW[,NEW]: write the Python lola's assistant, =, and the assistants to install its modules for, such as --map-target claude-code=opencode", shown)
		}
		var to []string
		for _, id := range strings.Split(list, ",") {
			if !slices.Contains(ids, id) {
				return nil, fmt.Errorf("--map-target %q: %q is not an assistant lola installs for; write %s", shown, untrusted.Text(id, untrusted.MaxName), strings.Join(ids, ", "))
			}
			if !slices.Contains(to, id) {
				to = append(to, id)
			}
		}
		if prior, dup := out[old]; dup {
			name := untrusted.Text(old, untrusted.MaxName)
			merged := slices.Clone(prior)
			for _, id := range to {
				if !slices.Contains(merged, id) {
					merged = append(merged, id)
				}
			}
			return nil, fmt.Errorf("--map-target names %s twice: give it once, with every assistant after =, such as --map-target %s=%s", name, name, strings.Join(merged, ","))
		}
		out[old] = to
	}
	return out, nil
}
