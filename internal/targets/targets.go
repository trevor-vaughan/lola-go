// Package targets is the registry of assistant targets, built from lola's target specs
// (internal/targetspec/specs) and the user's own (paths.Paths.Targets). Adding a built-in
// target means adding a spec, and its id to order.
package targets

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/target/spec"
	"github.com/trevor-vaughan/lola-go/internal/targetspec"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Registry is the assistant targets lola renders into: its built-in specs, each replaced by a
// user spec of the same id, then the targets only user specs define, sorted by id.
type Registry struct {
	targets  []target.Target            // each spec's newest era
	eras     map[string][]target.Target // id → a target per era, oldest first
	specs    map[string]targetspec.Spec
	replaced map[string]string // built-in id → the user spec file that replaces it
}

// All returns every target in registration order, which is also help and completion order.
func (r Registry) All() []target.Target { return r.targets }

// IDs returns the ids of All.
func (r Registry) IDs() []string {
	ids := make([]string, 0, len(r.targets))
	for _, t := range r.targets {
		ids = append(ids, t.ID())
	}
	return ids
}

// Lookup returns the target with id.
func (r Registry) Lookup(id string) (target.Target, bool) {
	for _, t := range r.targets {
		if t.ID() == id {
			return t, true
		}
	}
	return nil, false
}

// For is id's target for the era version selects: "" or a version at or above the newest era's
// since gives the newest (the target All and Lookup return). version must be "" or a valid
// version; callers validate it.
func (r Registry) For(id, version string) (target.Target, bool) {
	s, ok := r.specs[id]
	if !ok {
		return nil, false
	}
	i, _ := s.Select(version)
	return r.eras[id][i], true
}

// Era is the since of the era version selects for id, whether version lies below the first era,
// and whether id is registered. Era(id, "") is the newest era's since.
func (r Registry) Era(id, version string) (since string, before, ok bool) {
	s, ok := r.specs[id]
	if !ok {
		return "", false, false
	}
	i, before := s.Select(version)
	return s.Eras[i].Since, before, true
}

// Eras are the since of each of id's eras, oldest first; nil when id is not registered.
func (r Registry) Eras(id string) []string {
	s, ok := r.specs[id]
	if !ok {
		return nil
	}
	out := make([]string, len(s.Eras))
	for i, e := range s.Eras {
		out[i] = e.Since
	}
	return out
}

// Summaries are the summary of each of id's eras, aligned with Eras ("" for an era without one);
// nil when id is not registered.
func (r Registry) Summaries(id string) []string {
	s, ok := r.specs[id]
	if !ok {
		return nil
	}
	out := make([]string, len(s.Eras))
	for i, e := range s.Eras {
		out[i] = e.Summary
	}
	return out
}

// Replaced is the file of the user spec that replaces built-in id, "" when none does.
func (r Registry) Replaced(id string) string { return r.replaced[id] }

// builtinSpecs is lola's embedded specs by id, parsed once. They ship inside the binary and a
// spec checks they load, so an error is a build defect.
var builtinSpecs = sync.OnceValue(func() map[string]targetspec.Spec {
	specs, err := targetspec.Builtin()
	if err != nil {
		panic(err)
	}
	out := map[string]targetspec.Spec{}
	for _, s := range specs {
		out[s.ID] = s
	}
	return out
})

// order is the built-ins' registration order, which is also help and completion order.
func order() []string { return []string{"claude-code", "gemini-cli", "opencode", "agents"} }

// Builtin is the registry of lola's built-in specs alone.
func Builtin() Registry { return build(nil) }

// Load is the registry of lola's built-in specs and the user's, from p.Targets (*.yaml). A
// directory that does not exist holds none. Names starting with "." (an editor's lock or swap
// file) and names that are not a regular file once symlinks are followed (a FIFO, which would
// block the read, or a dangling symlink) are skipped. Any spec that cannot be loaded is an error: lola
// never renders with a built-in in place of a spec the user wrote.
func Load(p paths.Paths) (Registry, error) {
	if p.Targets == "" {
		return Builtin(), nil
	}
	dir := untrusted.Text(p.Targets, untrusted.MaxName)
	info, err := os.Stat(p.Targets)
	if errors.Is(err, fs.ErrNotExist) {
		return Builtin(), nil
	}
	if err != nil {
		return Registry{}, errs.WithHints(fmt.Errorf("cannot read your target specs at %s: %w", dir, untrusted.CleanErr(err)),
			"make it a readable directory, or move it aside")
	}
	if !info.IsDir() {
		return Registry{}, errs.WithHints(fmt.Errorf("%s is not a directory", dir),
			"move it aside: lola reads your target specs from the directory "+dir)
	}
	entries, err := os.ReadDir(p.Targets)
	if err != nil {
		return Registry{}, errs.WithHints(fmt.Errorf("cannot read your target specs at %s: %w", dir, untrusted.CleanErr(err)),
			"make it a readable directory, or move it aside")
	}
	var user []targetspec.Spec
	seen := map[string]string{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		file := filepath.Join(p.Targets, name)
		if base, isYML := strings.CutSuffix(name, ".yml"); isYML {
			return Registry{}, errs.WithHints(fmt.Errorf("%s is not read", untrusted.Text(file, untrusted.MaxName)),
				"rename it to "+untrusted.Text(base, untrusted.MaxName)+".yaml: lola reads target specs only from .yaml files")
		}
		if !strings.HasSuffix(name, ".yaml") {
			continue
		}
		if info, err := os.Stat(file); errors.Is(err, fs.ErrNotExist) || err == nil && !info.Mode().IsRegular() {
			continue
		}
		s, err := parseUser(file)
		if err == nil {
			if prev, dup := seen[s.ID]; dup {
				err = &targetspec.Error{File: file, Field: "id", Problem: fmt.Sprintf("%s is also the id of %s", s.ID, prev),
					Fix: "give each spec its own id, or delete one of the files"}
			}
		}
		if err != nil {
			return Registry{}, userSpecError(err)
		}
		seen[s.ID] = file
		user = append(user, s)
	}
	return build(user), nil
}

// maxProblem caps a spec error's problem, which can quote the file's content (a YAML decode error).
const maxProblem = 4 * untrusted.MaxName

// userSpecError is err, a failure to load a user's spec, safe to print: the file name and the
// problem, which come from the user's files, go through untrusted.Text, and the hints say how to
// fix it and that lola does not fall back to its built-in.
func userSpecError(err error) error {
	e, ok := errors.AsType[*targetspec.Error](err)
	if !ok {
		return untrusted.CleanErr(err)
	}
	clean := &targetspec.Error{
		File:    untrusted.Text(e.File, untrusted.MaxName),
		Field:   untrusted.Text(e.Field, untrusted.MaxName),
		Problem: untrusted.Text(e.Problem, maxProblem),
		Fix:     untrusted.Text(e.Fix, maxProblem),
	}
	return errs.WithHints(clean, clean.Fix,
		"lola stops rather than render with its built-in in place of your spec; fix or move aside "+clean.File)
}

// parseUser reads and parses one user spec file.
func parseUser(file string) (targetspec.Spec, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return targetspec.Spec{}, &targetspec.Error{File: file, Problem: untrusted.CleanErr(err).Error(), Fix: "make the file readable, or remove it"}
	}
	return targetspec.Parse(file, data)
}

// build registers the built-ins in order, each replaced by a user spec with its id, then the
// remaining user specs sorted by id.
func build(user []targetspec.Spec) Registry {
	byID := map[string]targetspec.Spec{}
	for _, s := range user {
		byID[s.ID] = s
	}
	r := Registry{eras: map[string][]target.Target{}, specs: map[string]targetspec.Spec{}, replaced: map[string]string{}}
	builtins := builtinSpecs()
	for _, id := range order() {
		s := builtins[id]
		if u, ok := byID[id]; ok {
			s = u
			r.replaced[id] = u.File
			delete(byID, id)
		}
		r.register(s)
	}
	rest := make([]targetspec.Spec, 0, len(byID))
	for _, s := range byID {
		rest = append(rest, s)
	}
	slices.SortFunc(rest, func(a, b targetspec.Spec) int { return strings.Compare(a.ID, b.ID) })
	for _, s := range rest {
		r.register(s)
	}
	return r
}

// register adds s: a target per era, the newest being the one All and Lookup return.
func (r *Registry) register(s targetspec.Spec) {
	for i := range s.Eras {
		r.eras[s.ID] = append(r.eras[s.ID], spec.At(s, i))
	}
	r.specs[s.ID] = s
	r.targets = append(r.targets, r.eras[s.ID][len(s.Eras)-1])
}
