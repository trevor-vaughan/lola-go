// Package target is the contract every assistant target implements, plus helpers they share.
// Targets live in their own packages (internal/target/<name>) and are listed by
// internal/targets. They are pure: they read the IR and return intents and findings, and never
// write to the disk. Whatever a target cannot express is reported as a Finding.
package target

import (
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/plan"
)

// Scope is where artifacts are installed.
type Scope int

// Scopes.
const (
	Project Scope = iota
	User
)

func (s Scope) String() string {
	if s == User {
		return "user"
	}
	return "project"
}

// PlanContext carries what a target needs besides the IR.
type PlanContext struct {
	Scope      Scope
	Root       string // absolute scope root of this target: Root(Scope, …)
	ContentDir string // absolute content dir of the cached module version, which artifacts are read from
	PluginRoot string // absolute ${PLUGIN_ROOT}: the module copy
	PluginData string // absolute ${PLUGIN_DATA} dir for this module and scope
	SkipRules  bool   // the manifest entry says rules: false
	Force      bool   // render forceable skips (Skipped)
	Version    string // the installed assistant's version, "" when unknown; adds findings only (Bounds)
	// SettingsExposed says, at project scope, why git could commit abs, the absolute path of a
	// settings file the target writes runtime hooks to: tracked, not ignored, or not checkable.
	// why is "" when git ignores the file or the project is not in a git work tree; rel is the
	// file relative to the project, in slash form, for coaching. Nil when the engine did not
	// check, as at user scope or with no runtime hooks. The engine runs git; targets stay pure.
	SettingsExposed func(abs string) (why, rel string)
	// SharedSkills are the module's skills the agents target delivers at this scope: ones it
	// admits with no skip and no forced finding, on a module whose targets include it. A target
	// whose assistant reads .agents/skills writes no copy of its own of these, so the assistant
	// loads each once; nil when agents is not among the module's targets.
	SharedSkills map[string]bool
}

// Target is one assistant lola renders into.
type Target interface {
	ID() string
	// Root is the directory artifact paths are relative to.
	Root(scope Scope, projectDir string, p paths.Paths) string
	// Detect reports whether the assistant appears to be set up under root, the target's
	// root for scope.
	Detect(scope Scope, root string) bool
	// Capabilities is what the target can express; Plan admits every artifact through it.
	Capabilities() Capabilities
	// Probe tells lola how to find the assistant and read its version.
	Probe() Probe
	// Destinations are where the target writes at scope, as lola tidy sweeps them.
	// Pure like Plan: root is the target's root for scope.
	Destinations(scope Scope, root string) (Destinations, error)
	// Plan returns the module's intents for this target and what rendering loses. A content
	// problem is a Finding; the error is for failures that are not about the module's content.
	Plan(m *module.Module, ctx PlanContext) ([]plan.Intent, []Finding, error)
}

// Destinations are the places a target writes artifacts into: directories whose direct entries
// are units of one kind, and JSON objects whose direct members are MCP servers. lola tidy offers
// only what lies there and lola does not own.
type Destinations struct {
	Dirs    []DestDir   // root-relative directories whose direct entries are units
	Servers []ServerMap // JSON files and the object of servers lola writes members into
}

// DestDir is one destination directory. Path is root-relative, slash-separated; Ext is stripped
// from a file entry's name to name the unit. Shared says other tools install into it too, so
// lola tidy names what lola does not own there as theirs.
type DestDir struct {
	Kind, Path, Ext string
	Shared          bool
}

// ServerMap is a root-relative JSON file and the JSON Pointer of its object of MCP servers.
type ServerMap struct{ File, Pointer string }
