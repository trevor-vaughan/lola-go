package config

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
	"github.com/trevor-vaughan/lola-go/internal/yamldoc"
)

// Section kinds of selections.yaml.
const (
	SectionGlobal  = "global"
	SectionUser    = "user"
	SectionProject = "project"
	// a profile entry's picks, held for the run: read from and written to the profile, never to selections.yaml (cherry-pick design §4.2)
	SectionProfile = "profile"
)

// Where names one section of selections.yaml: global, user, or a project by its
// paths.ProjectKey; or, held for the run only, a profile entry's picks (SectionProfile), by the
// profile layer's name, profile:<qualified> (spec 2026-10-29 §13 item 2).
type Where struct {
	Kind    string
	Project string
	Profile string
}

// Global, User, Project and Picks name the sections.
func Global() Where            { return Where{Kind: SectionGlobal} }
func User() Where              { return Where{Kind: SectionUser} }
func Project(key string) Where { return Where{Kind: SectionProject, Project: key} }
func Picks(layer string) Where { return Where{Kind: SectionProfile, Profile: layer} }

func (w Where) String() string {
	switch w.Kind {
	case SectionProject:
		return "projects[" + w.Project + "]"
	case SectionProfile:
		return "profile " + strings.TrimPrefix(w.Profile, "profile:")
	}
	return w.Kind
}

// Origin names the section in messages: selections.yaml global, user or projects[<dir>], or
// profile <name>. The project path and the profile name are the user's; print them through
// untrusted.Text.
func (w Where) Origin() string {
	if w.Kind == SectionProfile {
		return w.String()
	}
	return "selections.yaml " + w.String()
}

// Entry is one module's entry in a section. Only, when set, is the list of the components the
// module installs at the section's scope (spec 2026-10-29 cherry-pick §3.1); only: [] is none.
type Entry struct {
	Skip, Include, Only []Pattern
}

// Empty reports an entry with nothing in it.
func (e Entry) Empty() bool { return len(e.Skip) == 0 && len(e.Include) == 0 && len(e.Only) == 0 }

// Section maps module names to their entries.
type Section map[string]Entry

// projectSection is one projects entry: Key is canonical, Written is the key as the file
// spells it (kept when lola rewrites the entry).
type projectSection struct {
	Key, Written string
	Section      Section
}

// Selections is selections.yaml, validated.
type Selections struct {
	Path     string
	global   Section
	projects []projectSection
	profiles map[string]Section // a profile entry's picks, by layer name: the run's, never saved
}

// LoadSelections reads selections.yaml; a missing or empty file has no selections.
func LoadSelections(path string) (*Selections, error) {
	s, _, err := loadSelections(path)
	return s, err
}

// loadSelections also returns the document node, nil when the file is missing or blank. A
// document that holds only null (with or without comments) comes back with no root mapping.
func loadSelections(path string) (*Selections, *yaml.Node, error) {
	s := &Selections{Path: path, global: Section{}}
	d, _, err := yamldoc.Read(path, Version)
	if err != nil {
		return nil, nil, err
	}
	root := yamldoc.Root(d)
	if root == nil {
		return s, d, nil
	}
	if err := s.decode(root); err != nil {
		return nil, nil, err
	}
	return s, d, nil
}

func (s *Selections) decode(root *yaml.Node) error {
	d := doc{path: s.Path}
	// The user section went with user.yml (spec 2026-10-29 §7): user-scope modules come from
	// profiles, whose picks live in their entries. Its key's line names it.
	for i := 0; i+1 < len(root.Content); i += 2 {
		if k := root.Content[i]; k.Value == SectionUser {
			return d.fail(k, SectionUser, "is no longer used; picks for user-scope modules live in their profile entries",
				"delete the user: section from "+untrusted.Text(s.Path, untrusted.MaxName),
				"then re-apply its picks: lola install <module> --user --skip <kind/name>")
		}
	}
	return d.mapping(root, "", map[string]func(*yaml.Node, string) error{
		"version": d.version,
		"global":  func(n *yaml.Node, at string) error { return d.section(n, at, s.global, true) },
		"projects": func(n *yaml.Node, at string) error {
			if isNull(n) {
				return nil
			}
			if n.Kind != yaml.MappingNode {
				return d.fail(n, at, "must be a mapping of project paths, not "+kindName(n), "write /absolute/project/path: followed by module entries")
			}
			for i := 0; i+1 < len(n.Content); i += 2 {
				k, v := n.Content[i], n.Content[i+1]
				p, err := d.projectKey(k, at)
				if err != nil {
					return err
				}
				for _, q := range s.projects {
					if paths.SameKey(q.Key, p.Key) {
						return d.fail(k, join(at, k.Value), fmt.Sprintf("names the same project as %q", untrusted.Text(q.Written, untrusted.MaxName)),
							"merge the two entries into one")
					}
				}
				if err := d.section(v, join(at, k.Value), p.Section, false); err != nil {
					return err
				}
				s.projects = append(s.projects, p)
			}
			return nil
		},
	}, "version")
}

// projectKey validates a projects key: an absolute path, canonicalized like the project lock.
func (d doc) projectKey(k *yaml.Node, at string) (projectSection, error) {
	sub := join(at, k.Value)
	if k.Kind != yaml.ScalarNode || k.Value == "" {
		return projectSection{}, d.fail(k, at, "a project key must be a path", "write the project's absolute path")
	}
	if strings.HasPrefix(k.Value, "~") {
		return projectSection{}, d.fail(k, sub, "a project key cannot start with ~", "write the absolute path, with your home directory spelled out")
	}
	if !filepath.IsAbs(k.Value) {
		return projectSection{}, d.fail(k, sub, "a project key must be an absolute path", "write the project's absolute path")
	}
	key, err := paths.ProjectKey(k.Value)
	if err != nil {
		return projectSection{}, d.fail(k, sub, "cannot be resolved: "+untrusted.CleanErr(err).Error(), "fix the path, or remove the entry")
	}
	return projectSection{Key: key, Written: k.Value, Section: Section{}}, nil
}

// section decodes module entries into out. The global section takes skip: and only:, not
// include: an include there would have no scope to override.
func (d doc) section(n *yaml.Node, at string, out Section, global bool) error {
	if isNull(n) {
		return nil
	}
	if n.Kind != yaml.MappingNode {
		return d.fail(n, at, "must be a mapping of module names, not "+kindName(n), "write module-name: followed by skip:, include: and only: lists")
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		sub := join(at, k.Value)
		if k.Kind != yaml.ScalarNode {
			return d.fail(k, at, "a module key must be a module name", "write the module's name as lola list shows it")
		}
		if err := module.ValidateName("module", k.Value); err != nil {
			return d.fail(k, sub, err.Error(), "write the module's name as lola list shows it")
		}
		if _, dup := out[k.Value]; dup {
			return d.fail(k, sub, "is written twice", "merge the two entries into one")
		}
		var e Entry
		list := func(dst *[]Pattern) func(*yaml.Node, string) error {
			return func(n *yaml.Node, at string) error {
				ps, err := d.patterns(n, at)
				*dst = ps
				return err
			}
		}
		fields := map[string]func(*yaml.Node, string) error{"skip": list(&e.Skip), "include": list(&e.Include), "only": list(&e.Only)}
		if global {
			fields["include"] = func(n *yaml.Node, at string) error {
				return d.fail(n, at, "global takes skip: and only:, not include:; an include overrides a skip or an only: list within a scope",
					"move it to a projects: entry")
			}
		}
		if !isNull(v) {
			if err := d.mapping(v, sub, fields); err != nil {
				return err
			}
		}
		out[k.Value] = e
	}
	return nil
}

// patterns decodes a skip, include or only list.
func (d doc) patterns(n *yaml.Node, at string) ([]Pattern, error) {
	if isNull(n) {
		return nil, nil
	}
	if n.Kind != yaml.SequenceNode {
		return nil, d.fail(n, at, "must be a list, not "+kindName(n), "write [kind/name, …], e.g. [skill/slow-lint]")
	}
	var out []Pattern
	for i, c := range n.Content {
		sub := fmt.Sprintf("%s[%d]", at, i)
		s, err := d.scalar(c, sub, "write kind/name, e.g. skill/slow-lint")
		if err != nil {
			return nil, err
		}
		if strings.Contains(s, ":") {
			return nil, d.fail(c, sub, fmt.Sprintf("%q names a module; the module is the key above", untrusted.Text(s, maxPattern)), "write kind/name")
		}
		p, err := ParsePattern(s)
		if err != nil {
			return nil, d.fail(c, sub, err.Error(), SelectorForms)
		}
		out = append(out, p)
	}
	return out, nil
}

// section returns the section w names, or nil when the file has none.
func (s *Selections) section(w Where) Section {
	switch w.Kind {
	case SectionGlobal:
		return s.global
	case SectionProfile:
		return s.profiles[w.Profile]
	}
	for _, p := range s.projects {
		if paths.SameKey(p.Key, w.Project) {
			return p.Section
		}
	}
	return nil
}

// Entry returns module's entry in the section w.
func (s *Selections) Entry(w Where, module string) Entry { return s.section(w)[module] }

// Set replaces module's entry in the section w, in memory; Save writes it. An empty entry
// removes the module, and a project section it leaves empty.
func (s *Selections) Set(w Where, module string, e Entry) {
	sec := s.section(w)
	if e.Empty() {
		delete(sec, module)
		if w.Kind == SectionProject && sec != nil && len(sec) == 0 {
			s.projects = slices.DeleteFunc(s.projects, func(p projectSection) bool { return paths.SameKey(p.Key, w.Project) })
		}
		return
	}
	if sec == nil {
		sec = Section{}
		if w.Kind == SectionProfile {
			if s.profiles == nil {
				s.profiles = map[string]Section{}
			}
			s.profiles[w.Profile] = sec
		} else {
			s.projects = append(s.projects, projectSection{Key: w.Project, Written: w.Project, Section: sec})
		}
	}
	sec[module] = e
}

// Layer is one place that sets a module's selectors, named in messages by Origin (Where.Origin).
type Layer struct {
	Origin string
	Entry
}

// Exclusions are a module's selectors at a scope, one Layer per place that sets them: Global is
// selections.yaml's global section, Scope the scope's own section (zero when the global section
// itself is edited). The zero value excludes nothing. Precedence (cherry-pick design §3.1): the
// scope's include always installs a match; every layer's skip excludes its matches; the only: list
// of the most specific layer that has one excludes what it misses, replacing the others.
type Exclusions struct {
	Global, Scope Layer
}

// Exclusions returns module's effective exclusions at scope (User(), a Project, or a profile
// entry's Picks). A profile entry's picks replace every section's but the global skip, a "never
// anywhere" choice (cherry-pick design §4.2): the global only: does not apply to them.
func (s *Selections) Exclusions(scope Where, module string) Exclusions {
	return Layered(scope, s.Entry(Global(), module), s.Entry(scope, module))
}

// Layered is the exclusions at scope of a module whose global section entry is global and whose
// entry at scope is own, by Exclusions' rule: a profile entry's picks take the global skip alone.
func Layered(scope Where, global, own Entry) Exclusions {
	if scope.Kind == SectionProfile {
		global = Entry{Skip: global.Skip}
	}
	return Exclusions{Global: Layer{Origin: Global().Origin(), Entry: global}, Scope: Layer{Origin: scope.Origin(), Entry: own}}
}

// Only returns the only: list in force and the origin of the layer that set it; nil and "" when
// no layer has one.
func (x Exclusions) Only() ([]Pattern, string) {
	for _, l := range []Layer{x.Scope, x.Global} {
		if len(l.Only) > 0 {
			return l.Only, l.Origin
		}
	}
	return nil, ""
}

// Excludes reports whether the component kind/name is excluded.
func (x Exclusions) Excludes(kind, name string) bool {
	if matchesAny(x.Scope.Include, kind, name) {
		return false
	}
	if x.skipped(kind, name) {
		return true
	}
	only, _ := x.Only()
	return only != nil && !matchesAny(only, kind, name)
}

// LeftOutByOnly reports whether kind/name is excluded by the only: list in force alone: no skip
// names it and no include brings it back.
func (x Exclusions) LeftOutByOnly(kind, name string) bool {
	return x.Excludes(kind, name) && !x.skipped(kind, name)
}

func (x Exclusions) skipped(kind, name string) bool {
	return matchesAny(x.Global.Skip, kind, name) || matchesAny(x.Scope.Skip, kind, name)
}

// Sourced is a selector with the origin of the layer that wrote it.
type Sourced struct {
	Pattern
	Origin string
}

// Patterns is every pattern of every layer, with its origin, for warnings about patterns that
// name nothing.
func (x Exclusions) Patterns() []Sourced {
	var out []Sourced
	for _, l := range []Layer{x.Global, x.Scope} {
		for _, ps := range [][]Pattern{l.Skip, l.Include, l.Only} {
			for _, p := range ps {
				out = append(out, Sourced{Pattern: p, Origin: l.Origin})
			}
		}
	}
	return out
}

func matchesAny(ps []Pattern, kind, name string) bool {
	for _, p := range ps {
		if p.Matches(kind, name) {
			return true
		}
	}
	return false
}

// Clone returns a deep copy, for planning edits without touching s.
func (s *Selections) Clone() *Selections {
	c := &Selections{Path: s.Path, global: s.global.clone()}
	for _, p := range s.projects {
		c.projects = append(c.projects, projectSection{Key: p.Key, Written: p.Written, Section: p.Section.clone()})
	}
	for layer, sec := range s.profiles {
		if c.profiles == nil {
			c.profiles = map[string]Section{}
		}
		c.profiles[layer] = sec.clone()
	}
	return c
}

func (sec Section) clone() Section {
	out := Section{}
	for m, e := range sec {
		out[m] = Entry{Skip: slices.Clone(e.Skip), Include: slices.Clone(e.Include), Only: slices.Clone(e.Only)}
	}
	return out
}
