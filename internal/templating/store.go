package templating

import (
	"fmt"
	"path/filepath"
)

// codeIncludeLimit is the refusal of a render that includes more partials than Limits.Reads.
const codeIncludeLimit = "template-include-limit"

// Partials holds a module's partials (Plan 7 §3.3): content-relative path, _partials/<name>.liquid
// or _partials/<name>.<target>.liquid, to source.
type Partials struct {
	Files map[string]string
}

// partialsDir is PartialsDir as the prefix of a partial's content-relative path.
const partialsDir = PartialsDir + "/"

// resolve returns the file that partial name is for target, its target's file when p has one,
// else its generic file, and false when p has neither.
func (p Partials) resolve(name, target string) (string, bool) {
	for _, file := range []string{partialsDir + name + "." + target + ".liquid", partialsDir + name + ".liquid"} {
		if _, ok := p.Files[file]; ok {
			return file, true
		}
	}
	return "", false
}

// store is the TemplateStore of one render (Plan 7 §3.3). liquid asks for an include of name in
// a template whose path is file as filepath.Join(filepath.Dir(file), name), and compiles the
// partial under that path, so a nested include asks in the same directory: served maps each such
// path to the partial file it stands for, and missing each such path of a partial name that has
// no file for target to that name. Every read counts, and the one past limit refuses; a read of a
// missing partial refuses with template-include-missing (Plan 7 §5.7.5). No error wraps
// fs.ErrNotExist, so liquid never falls back to its own cache.
type store struct {
	p       Partials
	target  string
	served  map[string]string
	missing map[string]string
	reads   int
	limit   int
}

// newStore serves names, the partials a render of the template at file may include, as resolved
// for target, up to limit reads.
func newStore(file, target string, p Partials, names []string, limit int) *store {
	s := &store{p: p, target: target, served: map[string]string{}, missing: map[string]string{}, limit: limit}
	for _, name := range names {
		if f, ok := p.resolve(name, target); ok {
			s.served[storeKey(file, name)] = f
		} else {
			s.missing[storeKey(file, name)] = name
		}
	}
	return s
}

// storeKey is the path liquid asks the store for when the template or partial compiled under
// path file includes name (tags/template_path.go): file's directory joined with name.
func storeKey(file, name string) string { return filepath.Join(filepath.Dir(file), name) }

// storeRefusal is a read the store refuses, which liquid reports at the include tag.
type storeRefusal struct{ code, reason string }

func (r *storeRefusal) Error() string { return r.reason }

func (s *store) ReadTemplate(path string) ([]byte, error) {
	if s.reads++; s.reads > s.limit {
		return nil, &storeRefusal{codeIncludeLimit, fmt.Sprintf("more than %d includes in one render; lola reads at most %d partials per file", s.limit, s.limit)}
	}
	if name, ok := s.missing[path]; ok {
		return nil, &storeRefusal{codeIncludeMissing, fmt.Sprintf("no partial %s for %s (looked for %s%s.%s.liquid, %s%s.liquid)",
			name, s.target, partialsDir, name, s.target, partialsDir, name)}
	}
	file, ok := s.served[path]
	if !ok {
		return nil, &storeRefusal{codeSyntax, "no partial is served as " + clean(path, maxToken)}
	}
	return []byte(withValueCheck(Source{File: file, Line: 1, Body: s.p.Files[file]})), nil
}
