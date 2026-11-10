// Package mech holds the mechanisms a target spec's layout names: the Go that renders one
// artifact kind in one way (a skill directory, a TOML command, a JSON object of servers, …),
// parameterised by the layout entry.
package mech

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/targetspec"
)

// Env is what a mechanism renders one kind with.
type Env struct {
	ID    string
	Caps  target.Capabilities
	Ctx   target.PlanContext
	Entry targetspec.Entry // the kind's layout entry, expanded at Ctx.Scope
}

// Plan renders m's artifacts of kind through env.Entry's mechanism.
func Plan(kind string, env Env, m *module.Module) ([]plan.Intent, []target.Finding, error) {
	switch env.Entry.Mech {
	case "agentskills-dir":
		return agentSkills(env, m)
	case "skill-dir":
		return skillDir(env, m)
	case "markdown-file":
		return markdownFiles(kind, env, m)
	case "toml-command":
		return tomlCommands(env, m)
	case "json-servers":
		return servers(env, m)
	case "rules-dir":
		return rulesDir(env, m)
	case "claude-hooks":
		return claudeHooks(env, m)
	}
	return nil, nil, fmt.Errorf("%s: no mechanism %s for %ss", env.ID, env.Entry.Mech, kind)
}

// Rules are the content rules the mechanisms of layout contribute to target id's capabilities.
func Rules(id string, layout map[string]targetspec.Entry) []target.ContentRule {
	if layout["command"].Mech == "toml-command" {
		return []target.ContentRule{wholeArguments(id), positional(id)}
	}
	return nil
}

// Resolve names f under root: its path, or the first of its candidates that exists there, else
// the last. id names the target when a candidate cannot be checked.
func Resolve(id string, f targetspec.FileRef, root string) (string, error) {
	if f.Path != "" {
		return f.Path, nil
	}
	for _, c := range f.First[:len(f.First)-1] {
		_, err := os.Stat(filepath.Join(root, filepath.FromSlash(c)))
		switch {
		case err == nil:
			return c, nil
		case !errors.Is(err, fs.ErrNotExist):
			return "", fmt.Errorf("%s: cannot read %s: %w", id, filepath.Join(root, filepath.FromSlash(c)), err)
		}
	}
	return f.First[len(f.First)-1], nil
}
