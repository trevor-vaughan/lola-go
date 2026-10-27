// Package module defines lola's intermediate representation (IR) of a module and loads it
// from a module root on disk.
package module

import (
	"bytes"
	"encoding/json"
	"path"
	"slices"

	"go.yaml.in/yaml/v3"
)

// Author is the module author as declared in lola.yml.
type Author struct {
	Name  string `yaml:"name,omitempty"`
	Email string `yaml:"email,omitempty"`
	URL   string `yaml:"url,omitempty"`
}

// Skill is a skill directory. Dir is relative to the module content root ("." when the root
// itself is the skill); Exclude lists Dir-relative entries that are not part of the skill.
type Skill struct {
	Name    string
	Dir     string
	Exclude []string
	Doc     Doc         // parsed SKILL.md, or SKILL.md.liquid unparsed
	Files   []SkillFile `yaml:",omitempty"` // the templated support files, sorted by Source
	Targets []string
	Attrs   map[string]map[string]any `yaml:",omitempty"` // target id → frontmatter keys from SKILL.targets.yaml or .jsonc, nil when none
	// AttrsFile is the content-relative path of the attribute file Attrs came from, "" for none.
	AttrsFile string `yaml:",omitempty"`
}

// SkillFile is a templated support file of a skill: Source is its Dir-relative .md.liquid path,
// Rel the path it is written to (Source without .liquid), and Body its source, or once the
// engine rendered it for a target, its output. It is never parsed.
type SkillFile struct {
	Rel, Source string
	Body        string
}

// Command is a slash command. File and SidecarDir are content-relative; SidecarDir is "" when
// the command has no commands/<name>/ directory. Sidecars are the .md files under SidecarDir,
// which Claude Code also reads as commands.
type Command struct {
	Name, File, SidecarDir string
	Doc                    Doc
	Sidecars               []Sidecar
	Targets                []string
	Attrs                  map[string]map[string]any `yaml:",omitempty"` // target id → frontmatter keys from its attribute file, nil when none
	AttrsFile              string                    `yaml:",omitempty"` // that file, content-relative; "" for none
}

// Sidecar is a Markdown file under a command's SidecarDir; File is the content-relative path it
// has in the copied directory. Source is the content-relative path of its .md.liquid source,
// "" for a literal file.
type Sidecar struct {
	File   string
	Source string `yaml:",omitempty"`
	Doc    Doc
}

// Agent is a subagent definition; File is content-relative.
type Agent struct {
	Name, File string
	Doc        Doc
	Targets    []string
	Attrs      map[string]map[string]any `yaml:",omitempty"` // target id → frontmatter keys from its attribute file, nil when none
	AttrsFile  string                    `yaml:",omitempty"` // that file, content-relative; "" for none
}

// MCP transports.
const (
	TransportStdio = "stdio"
	TransportHTTP  = "http"
	TransportSSE   = "sse"
)

// MCPServer is one MCP server declaration.
type MCPServer struct {
	Name      string
	Transport string
	Command   string
	Args      []string
	Env       map[string]string
	Cwd       string
	URL       string
	Headers   map[string]string
	Targets   []string
}

// Rule is always-on context (§5.6): rules/<Name>.md with its frontmatter removed. A template
// (Template set) holds its whole source in Body until the engine renders it.
type Rule struct {
	Name, Body string
	Template   *TemplateSource `yaml:",omitempty"`
	Targets    []string
}

// RuntimeHookSet is one components.runtime_hooks entry: a Claude Code hooks event map, which
// the assistant runs on its own events (Plan 5b design §7.1). Source is the hooks file it came
// from, or "" for an inline entry; Hooks is the event map (without the "hooks" wrapper) as
// canonical JSON.
type RuntimeHookSet struct {
	Source  string
	Hooks   json.RawMessage
	Targets []string
}

// Name identifies the set in findings: its file, or "inline".
func (h RuntimeHookSet) Name() string {
	if h.Source == "" {
		return "inline"
	}
	return h.Source
}

// Hook is an install hook a module declares (§9.1): Script is the module-relative path of an
// executable regular file. lola runs hooks only with the user's consent.
type Hook struct {
	Event  string `json:"event"`
	Script string `json:"script"`
}

// Module is the IR every format adapter produces and every target consumes.
type Module struct {
	Name, Description, Version, License, Homepage, Repository string
	Author                                                    *Author
	Keywords                                                  []string
	Targets                                                   []string       // declared support matrix; empty = all
	Formats                                                   map[string]any // verbatim passthrough keyed by format id
	Format                                                    string         // "native", "lola", or the format whose adapter wrote the manifest
	Manifest                                                  string         // manifest file read (ManifestNames), "" for the native layout
	Shadowed                                                  []string       // lower-precedence manifests that were ignored
	Skills                                                    []Skill
	Commands                                                  []Command
	Agents                                                    []Agent
	MCPServers                                                []MCPServer
	Rules                                                     []Rule
	RuntimeHooks                                              []RuntimeHookSet
	DeclaredHooks                                             []Hook
	Partials                                                  map[string]string // content-relative path → source (Plan 7 §3.3), nil when none
	Vars                                                      map[string]any    // lola.yml vars: name → default, a string, bool, int64, float64 or []string (Plan 7 §4.1, §5.7.4); nil when none
	Warnings                                                  []string          // non-fatal load problems, e.g. skipped skills

	components *components // the manifest's components section, which Normalize writes back
	templates  int         // template files read so far, which load caps
}

// Supports reports whether an artifact or module restricted to targets applies to id.
// An empty list means every target.
func Supports(targets []string, id string) bool {
	return len(targets) == 0 || slices.Contains(targets, id)
}

// SameMeaning reports whether a and b are the same module to every target and to the cache:
// it ignores how each was read (Format, Manifest, Shadowed) and the warnings. Values compare by their YAML encoding,
// so an empty list or map equals an absent one, and a number equals itself whether JSON or YAML
// decoded it.
func SameMeaning(a, b *Module) bool {
	ea, errA := meaning(a)
	eb, errB := meaning(b)
	return errA == nil && errB == nil && bytes.Equal(ea, eb)
}

func meaning(m *Module) ([]byte, error) {
	c := *m
	c.Format, c.Manifest, c.Shadowed, c.Warnings, c.components = "", "", nil, nil, nil
	return yaml.Marshal(c)
}

// Template is one template file of a module: its content-relative path and its source.
type Template struct {
	File, Body string
}

// Templates lists m's template files (Plan 7 §3.1): component templates, skill support files and
// command sidecars, in the order of m's skills, commands, agents and rules.
func (m *Module) Templates() []Template {
	var out []Template
	for _, s := range m.Skills {
		if s.Doc.Template != nil {
			out = append(out, Template{s.Doc.Template.File, s.Doc.Body})
		}
		for _, f := range s.Files {
			out = append(out, Template{path.Join(s.Dir, f.Source), f.Body})
		}
	}
	for _, c := range m.Commands {
		if c.Doc.Template != nil {
			out = append(out, Template{c.Doc.Template.File, c.Doc.Body})
		}
		for _, sc := range c.Sidecars {
			if sc.Doc.Template != nil {
				out = append(out, Template{sc.Source, sc.Doc.Body})
			}
		}
	}
	for _, a := range m.Agents {
		if a.Doc.Template != nil {
			out = append(out, Template{a.Doc.Template.File, a.Doc.Body})
		}
	}
	for _, r := range m.Rules {
		if r.Template != nil {
			out = append(out, Template{r.Template.File, r.Body})
		}
	}
	return out
}

// AttributeFiles lists the content-relative paths of m's attribute files (Plan 7 §3.4), sorted.
func (m *Module) AttributeFiles() []string {
	var out []string
	add := func(file string) {
		if file != "" {
			out = append(out, file)
		}
	}
	for _, s := range m.Skills {
		add(s.AttrsFile)
	}
	for _, c := range m.Commands {
		add(c.AttrsFile)
	}
	for _, a := range m.Agents {
		add(a.AttrsFile)
	}
	slices.Sort(out)
	return out
}
