// Package targetspec is the format of a target spec: facts about one assistant, such as where it
// reads each artifact kind, how lola finds it and what its frontmatter accepts, and the loader
// that decodes and validates them. It records facts only. What lola does about them
// (severities, hints, guards) is lola's policy, in internal/target/policy, so this package
// imports nothing of lola's.
package targetspec

import (
	"fmt"
	"path/filepath"
	"regexp"

	"go.yaml.in/yaml/v3"
	"golang.org/x/mod/semver"
)

// Spec is one target: an assistant, or a directory several assistants read.
type Spec struct {
	ID      string `yaml:"id"`
	File    string `yaml:"-"`       // the name Parse was given, for notices
	Readers string `yaml:"readers"` // for a target with no probe: what reads the files it writes
	// KindUnsupportedNote is added to the finding for an artifact of a kind the target does not
	// place, e.g. ".agents holds skills only".
	KindUnsupportedNote string       `yaml:"kind_unsupported_note"`
	Probe               []Probe      `yaml:"probe"`
	Roots               Scoped       `yaml:"roots"`
	Detect              DetectScopes `yaml:"detect"`
	Eras                []Era        `yaml:"eras"` // resolved by Parse: extends and remove applied
}

// Scoped is a value per scope.
type Scoped struct {
	Project string `yaml:"project"`
	User    string `yaml:"user"`
}

func (s Scoped) at(user bool) string {
	if user {
		return s.User
	}
	return s.Project
}

// Probe is one way to read the installed assistant's version: exactly one of Exec and File.
type Probe struct {
	Exec *Exec      `yaml:"exec"`
	File *FileProbe `yaml:"file"`
}

// FileProbe reads the version from a file of at most 1 MiB: Match's first group, or the JSON
// string at Pointer (RFC 6901); exactly one of the two.
type FileProbe struct {
	Path    string   `yaml:"path"` // {home}/, {config_home}/ or {data_home}/ then a clean relative path
	Match   *Pattern `yaml:"match"`
	Pointer string   `yaml:"pointer"`
}

// Expand is f's path for v, in the OS's form.
func (f FileProbe) Expand(v RootVars) string {
	vars := map[string]string{"home": v.Home, "config_home": v.ConfigHome, "data_home": v.DataHome}
	loc := varRef.FindStringSubmatchIndex(f.Path)
	return filepath.Join(vars[f.Path[loc[2]:loc[3]]], filepath.FromSlash(f.Path[loc[1]:]))
}

// Exec runs the first of Binaries found on PATH with Args; Match's first group is the version.
type Exec struct {
	Binaries []string `yaml:"binaries"`
	Args     []string `yaml:"args"`
	Match    *Pattern `yaml:"match"`
}

// DetectScopes is how lola sees the assistant set up, per scope.
type DetectScopes struct {
	Project Detect `yaml:"project"`
	User    Detect `yaml:"user"`
}

// Detect is set up when any of Any's markers exists under the root (one ending in / must be a
// directory), or when Dir is a directory there ("." for the root itself).
type Detect struct {
	Any []string `yaml:"any"`
	Dir string   `yaml:"dir"`
}

// Era is what the assistant reads from version Since on.
type Era struct {
	Since     string            `yaml:"since"`
	Extends   string            `yaml:"extends"` // "previous": start from the era before
	Remove    []string          `yaml:"remove"`  // layout.<kind>, kinds.<kind>.keys.<key>, behaviors.<id>
	Vars      map[string]Scoped `yaml:"vars"`
	Entries   map[string]Entry  `yaml:"layout"`
	Kinds     map[string]Kind   `yaml:"kinds"`
	Behaviors []Behavior        `yaml:"behaviors"`
	Shared    []string          `yaml:"shared"`

	project, user map[string]Entry // Layout with its templates expanded, per scope
}

// Layout is e's layout at a scope, every path template expanded.
func (e Era) Layout(user bool) map[string]Entry {
	if user {
		return e.user
	}
	return e.project
}

// Entry is where and how one artifact kind is written: Mech names the mechanism, the other
// fields are its parameters (which ones a mechanism takes is checked at load).
type Entry struct {
	Mech              string            `yaml:"mech"`
	Path              string            `yaml:"path"`
	Ext               string            `yaml:"ext"`
	Shared            bool              `yaml:"shared"` // other tools install into Path too
	File              FileRef           `yaml:"file"`
	Pointer           string            `yaml:"pointer"`
	Shape             string            `yaml:"shape"`
	Register          *Register         `yaml:"register"`
	Concat            string            `yaml:"concat"`
	ReadsSharedSkills bool              `yaml:"reads_shared_skills"`
	NameMatchesDir    bool              `yaml:"name_matches_dir"`
	PrependName       bool              `yaml:"prepend_name"`
	Defaults          map[string]string `yaml:"defaults"`
	Arguments         string            `yaml:"arguments"`
	Sidecar           string            `yaml:"sidecar"`
	HookSettings      string            `yaml:"hook_settings"`
}

// FileRef is a root-relative file: Path, or the first of First that exists, else First's last.
type FileRef struct {
	Path  string
	First []string
	// bad is what UnmarshalYAML found wrong with a well-formed value, for the validator to
	// report at the field's path.
	bad string
}

// UnmarshalYAML reads a path, or {first: [paths]}.
func (f *FileRef) UnmarshalYAML(n *yaml.Node) error {
	switch {
	case n.Kind == yaml.ScalarNode && n.Tag != "!!str":
		f.bad = fmt.Sprintf("%q is not a path", n.Value)
		return nil
	case n.Kind == yaml.ScalarNode:
		return n.Decode(&f.Path)
	case n.Kind == yaml.MappingNode && len(n.Content) == 2 && n.Content[0].Value == "first":
		if err := n.Content[1].Decode(&f.First); err != nil {
			return err
		}
		if len(f.First) == 0 {
			f.bad = "first: is empty"
		}
		return nil
	}
	return fmt.Errorf("line %d: a file is a path or {first: [paths]}", n.Line)
}

func (f FileRef) set() bool { return f.Path != "" || len(f.First) > 0 }

// Register adds a member naming what was written to the JSON list List in File.
type Register struct {
	File   FileRef `yaml:"file"`
	List   string  `yaml:"list"`
	Member string  `yaml:"member"` // dir: the module's directory; glob: <dir>/*.md
}

// Kind is what the assistant accepts in one kind's frontmatter.
type Kind struct {
	UnknownKeys string         `yaml:"unknown_keys"` // keep, or drop (the default)
	Required    []string       `yaml:"required"`
	Name        *Pattern       `yaml:"name"` // names the assistant accepts; nil accepts any
	Keys        map[string]Key `yaml:"keys"`
}

// Key is what the assistant does with one frontmatter key: exactly one of Accepts, ImpliedBy
// and Unsupported.
type Key struct {
	Accepts     *Accepts `yaml:"accepts"`
	ImpliedBy   string   `yaml:"implied_by"`  // file-name
	Unsupported string   `yaml:"unsupported"` // why, as a fact about the tool
	// AbsentMeans "unrestricted": without the key the artifact may do anything, so a value the
	// target does not keep must not be dropped quietly. Alternative is the key the tool
	// restricts with instead; Example is a value it accepts.
	AbsentMeans     string `yaml:"absent_means"`
	Alternative     string `yaml:"alternative"`
	Example         string `yaml:"example"`
	CaseInsensitive bool   `yaml:"case_insensitive"`
}

// Accepts is the YAML shape a value must have, and for strings a pattern.
type Accepts struct {
	Shape   string   `yaml:"shape"`
	Pattern *Pattern `yaml:"pattern"`
}

// Behavior is something the assistant does with a command body that Claude Code does not.
type Behavior struct {
	ID          string   `yaml:"id"`
	Kind        string   `yaml:"kind"`
	Detect      *Pattern `yaml:"detect"`
	Effect      string   `yaml:"effect"`
	What        string   `yaml:"what"`        // the construct, as the tool would name it
	Native      string   `yaml:"native"`      // the tool's own syntax for it
	Alternative string   `yaml:"alternative"` // a form the tool does run
}

// Pattern is a regular expression, compiled when the spec loads.
type Pattern struct {
	Source string
	re     *regexp.Regexp
}

// UnmarshalYAML reads the expression's source.
func (p *Pattern) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: a pattern is a string", n.Line)
	}
	p.Source = n.Value
	return nil
}

// RE is the compiled expression. Parse compiles every pattern of a spec it returns.
func (p *Pattern) RE() *regexp.Regexp { return p.re }

// RootVars are the values of a root template's variables.
type RootVars struct{ Project, Home, ConfigHome, DataHome string }

// Root is s's root at a scope. A root template is a variable alone or a variable, a slash and a
// clean relative path (Parse checks it), so the result is the variable's value joined with that
// path and never leaves it.
func (s Spec) Root(user bool, v RootVars) string {
	vars := map[string]string{"project": v.Project, "home": v.Home, "config_home": v.ConfigHome, "data_home": v.DataHome}
	t := s.Roots.at(user)
	loc := varRef.FindStringSubmatchIndex(t)
	head, rest := vars[t[loc[2]:loc[3]]], subst(t[loc[1]:], vars)
	if rest == "" {
		return head
	}
	return filepath.Join(head, filepath.FromSlash(rest))
}

// Select is the index of the era version selects: the last whose since is at or below it, the
// newest for "", and the first, with before set, for a version below the first era. version
// must be "" or valid (Canonical(version) != "").
func (s Spec) Select(version string) (era int, before bool) {
	if version == "" {
		return len(s.Eras) - 1, false
	}
	v, era := Canonical(version), -1
	for i, e := range s.Eras {
		if semver.Compare(Canonical(e.Since), v) <= 0 {
			era = i
		}
	}
	if era < 0 {
		return 0, true
	}
	return era, false
}

// Newest is s's last era, the one lola renders for.
func (s Spec) Newest() Era { return s.Eras[len(s.Eras)-1] }

// Kinds are the artifact kinds a layout places, in the order targets render them.
func Kinds() []string { return []string{"skill", "command", "agent", "mcp", "rule", "runtime-hook"} }
