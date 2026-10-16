// Package module defines lola's intermediate representation (IR) of a module and loads it
// from a module root on disk.
package module

import "slices"

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
	Doc     Doc // parsed SKILL.md
	Targets []string
}

// Command is a slash command. File and SidecarDir are content-relative; SidecarDir is "" when
// the command has no commands/<name>/ directory.
type Command struct {
	Name, File, SidecarDir string
	Doc                    Doc
	Targets                []string
}

// Agent is a subagent definition; File is content-relative.
type Agent struct {
	Name, File string
	Doc        Doc
	Targets    []string
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

// Hook is an install hook a module declares. lola never runs hooks; it lists them.
type Hook struct {
	Event   string `json:"event"`
	Command string `json:"command"`
}

// Module is the IR every format adapter produces and every target consumes.
type Module struct {
	Name, Description, Version, License, Homepage, Repository string
	Author                                                    *Author
	Keywords                                                  []string
	Targets                                                   []string       // declared support matrix; empty = all
	Formats                                                   map[string]any // verbatim passthrough keyed by format id
	Format                                                    string         // detected source format: "lola" or "native"
	Skills                                                    []Skill
	Commands                                                  []Command
	Agents                                                    []Agent
	MCPServers                                                []MCPServer
	Instructions                                              string // INSTRUCTIONS.md content, "" if absent
	LegacyAgentsMD                                            bool
	DeclaredHooks                                             []Hook
	Warnings                                                  []string // non-fatal load problems, e.g. skipped skills
}

// LegacyAgentsMDWarning is printed at cache add and install when a module ships AGENTS.md
// (ADR #242: lola never installs a module's AGENTS.md).
const LegacyAgentsMDWarning = "AGENTS.md in a module is not installed: lola owns no part of your " +
	"assistants' AGENTS.md. Rename it to INSTRUCTIONS.md to ship always-on instructions (ADR #242)."

// Supports reports whether an artifact or module restricted to targets applies to id.
// An empty list means every target.
func Supports(targets []string, id string) bool {
	return len(targets) == 0 || slices.Contains(targets, id)
}
