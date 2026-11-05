package engine

import (
	"fmt"
	"slices"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Component is one component of a module, as selectors name it.
type Component struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

func (c Component) String() string { return c.Kind + "/" + c.Name }

// components lists a module's components in the order pickers show them: skills, commands,
// agents, MCP servers, rules.
func components(m *module.Module) []Component {
	var out []Component
	for _, s := range m.Skills {
		out = append(out, Component{config.KindSkill, s.Name})
	}
	for _, c := range m.Commands {
		out = append(out, Component{config.KindCommand, c.Name})
	}
	for _, a := range m.Agents {
		out = append(out, Component{config.KindAgent, a.Name})
	}
	for _, s := range m.MCPServers {
		out = append(out, Component{config.KindMCP, s.Name})
	}
	for _, r := range m.Rules {
		out = append(out, Component{config.KindRule, r.Name})
	}
	return out
}

// resolved is a module narrowed to the components a scope installs.
type resolved struct {
	module  *module.Module
	skipped []string // kind/name of every component the exclusions remove
	notes   []Notice // exclusions that name no component of the module
}

// resolveComponents is the one resolver (Plan 5c §7): the cached module minus the effective
// exclusions, and without rules when they are off. install, render and check all plan what it
// returns, so a component excluded after installation is an orphan the planner removes.
func resolveComponents(m *module.Module, x config.Exclusions, rulesOn bool) resolved {
	out := *m
	r := resolved{module: &out, skipped: []string{}}
	keep := func(kind, name string) bool {
		if x.Excludes(kind, name) {
			r.skipped = append(r.skipped, kind+"/"+name)
			return false
		}
		return true
	}
	out.Skills = slices.DeleteFunc(slices.Clone(m.Skills), func(s module.Skill) bool { return !keep(config.KindSkill, s.Name) })
	out.Commands = slices.DeleteFunc(slices.Clone(m.Commands), func(c module.Command) bool { return !keep(config.KindCommand, c.Name) })
	out.Agents = slices.DeleteFunc(slices.Clone(m.Agents), func(a module.Agent) bool { return !keep(config.KindAgent, a.Name) })
	out.MCPServers = slices.DeleteFunc(slices.Clone(m.MCPServers), func(s module.MCPServer) bool { return !keep(config.KindMCP, s.Name) })
	out.Rules = slices.DeleteFunc(slices.Clone(m.Rules), func(ru module.Rule) bool { return !keep(config.KindRule, ru.Name) })
	if !rulesOn {
		out.Rules = nil
	}
	all := components(m)
	for _, p := range x.Patterns() {
		if !slices.ContainsFunc(all, func(c Component) bool { return p.Matches(c.Kind, c.Name) }) {
			r.notes = append(r.notes, Notice{Level: LevelWarn, Text: fmt.Sprintf("selections.yaml: %s has no component %s; remove it from the file",
				m.Name, untrusted.Text(p.String(), untrusted.MaxName))})
		}
	}
	return r
}

// selections loads selections.yaml for one command.
func (e *Engine) selections() (*config.Selections, error) {
	return config.LoadSelections(e.Paths.Selections)
}

// where is the selections.yaml section of a scope.
func where(sc Scope) (config.Where, error) {
	if sc.Kind == target.User {
		return config.User(), nil
	}
	key, err := paths.ProjectKey(sc.ProjectDir)
	if err != nil {
		return config.Where{}, err
	}
	return config.Project(key), nil
}

// exclusions returns module's effective exclusions at sc.
func exclusions(sel *config.Selections, sc Scope, module string) (config.Exclusions, error) {
	w, err := where(sc)
	if err != nil {
		return config.Exclusions{}, err
	}
	return sel.Exclusions(w, module), nil
}
