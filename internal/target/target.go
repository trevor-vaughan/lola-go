// Package target renders the module IR into each assistant's native layout. Targets are
// pure: they read the IR and return intents and notices, and never touch the disk.
package target

import (
	"strings"

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

// Notice levels.
const (
	LevelInfo = "info"
	LevelWarn = "warn"
)

// Notice is something the user should know about a plan that is not an error.
type Notice struct {
	Level string `json:"level"`
	Text  string `json:"text"`
}

// PlanContext carries what a target needs besides the IR.
type PlanContext struct {
	Scope      Scope
	ContentDir string // absolute content dir of the cached module version, which artifacts are read from
	PluginRoot string // absolute ${PLUGIN_ROOT}: the module copy (§6.4)
	PluginData string // absolute ${PLUGIN_DATA} dir for this module and scope
	SkipRules  bool   // the manifest entry says rules: false (§5.6)
}

// Target is one assistant lola renders into.
type Target interface {
	ID() string
	// Root is the directory artifact paths are relative to.
	Root(scope Scope, projectDir string, p paths.Paths) string
	// Detect reports whether the assistant appears to be used under root.
	Detect(root string) bool
	// BaseDirs are root-relative artifact directories that cleanup never removes.
	BaseDirs(scope Scope) []string
	// Plan returns the module's intents for this target.
	Plan(m *module.Module, ctx PlanContext) ([]plan.Intent, []Notice, error)
}

// All returns every target in registration order, which is also help and completion order.
func All() []Target { return []Target{Claude{}} }

// IDs returns the ids of All.
func IDs() []string {
	var ids []string
	for _, t := range All() {
		ids = append(ids, t.ID())
	}
	return ids
}

// Lookup returns the target with id.
func Lookup(id string) (Target, bool) {
	for _, t := range All() {
		if t.ID() == id {
			return t, true
		}
	}
	return nil, false
}

// Expand substitutes ${PLUGIN_ROOT}, ${CLAUDE_PLUGIN_ROOT} and ${PLUGIN_DATA} (§5.5).
func Expand(s string, ctx PlanContext) string {
	return strings.NewReplacer(
		"${PLUGIN_ROOT}", ctx.PluginRoot,
		"${CLAUDE_PLUGIN_ROOT}", ctx.PluginRoot,
		"${PLUGIN_DATA}", ctx.PluginData,
	).Replace(s)
}
