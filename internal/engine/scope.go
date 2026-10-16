package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Where an install's scope came from (Plan 5c §5.1), reported as scope_source.
const (
	SourceFlag       = "flag"
	SourceConfig     = "config"
	SourceDiscovered = "discovered"
	SourcePicked     = "picked"
)

// ScopeRequest is what a command was told about its scope.
type ScopeRequest struct {
	Scope       string // --scope: project, user, both or project,user; "" when not given
	User        bool   // --user
	Project     bool   // --project
	ProjectDir  string // -C/--root, absolute; "" when not given
	Cwd         string
	Interactive bool   // the scope picker can run
	Command     string // the command, named by the refusal: install, sync, uninstall or render
	Both        bool   // the command acts on both scopes in one run; else two scopes are refused
}

// ScopeChoice is one scope an install acts on and where that choice came from.
type ScopeChoice struct {
	Scope  Scope
	Source string
}

// ScopeResolution is the outcome of §5.1. Either Scopes is set, or Pick is: the scope picker
// decides, offering a project at ProjectRoot (Found says it has .lola/) with nothing
// preselected (§14).
type ScopeResolution struct {
	Scopes      []ScopeChoice
	Pick        bool
	ProjectRoot string
	Found       bool
}

// findProject walks up from dir to the nearest directory containing .lola/.
func findProject(dir string) (string, bool) {
	for d := dir; ; d = filepath.Dir(d) {
		if fi, err := os.Stat(filepath.Join(d, ".lola")); err == nil && fi.IsDir() {
			return d, true
		}
		if filepath.Dir(d) == d {
			return dir, false
		}
	}
}

// ParseScope reads a --scope or default_scope value: project, user, both or project,user.
func ParseScope(v string) ([]string, error) {
	switch strings.ReplaceAll(v, " ", "") {
	case config.ScopeProject:
		return []string{config.ScopeProject}, nil
	case config.ScopeUser:
		return []string{config.ScopeUser}, nil
	case config.ScopeBoth, "project,user", "user,project":
		return []string{config.ScopeProject, config.ScopeUser}, nil
	}
	return nil, errs.WithHints(fmt.Errorf("--scope %q is not a scope", v), "use --scope project, --scope user or --scope both")
}

// Scopes turns scope names into scopes, the project at root.
func Scopes(names []string, root, source string) []ScopeChoice {
	var out []ScopeChoice
	for _, n := range names {
		sc := Scope{Kind: target.User}
		if n == config.ScopeProject {
			sc = Scope{Kind: target.Project, ProjectDir: root}
		}
		out = append(out, ScopeChoice{Scope: sc, Source: source})
	}
	return out
}

// ResolveReadScope is the scope rule of the commands that only read (Plan 6b §3.1, D2, §19 R6):
// the scope flags when given, --scope both included; otherwise the project found walking up
// from Cwd, when there is one, then user scope. It never asks for a scope, on a terminal or off
// it, and default_scope does not apply: every row a read-only command prints names its scope.
func (e *Engine) ResolveReadScope(req ScopeRequest) ([]ScopeChoice, error) {
	if req.Scope != "" || req.User || req.Project || req.ProjectDir != "" {
		req.Interactive, req.Both = false, true
		res, err := e.ResolveScope(req)
		return res.Scopes, err
	}
	var out []ScopeChoice
	if root, found := findProject(req.Cwd); found {
		out = Scopes([]string{config.ScopeProject}, root, SourceDiscovered)
	}
	return append(out, Scopes([]string{config.ScopeUser}, "", SourceDiscovered)...), nil
}

// ResolveScope implements the resolution table of Plan 5c §5.1, which every command that writes
// uses (Plan 6b §3.1): --scope, then --user or --project, then -C, then the picker on a
// terminal, then a .lola/ project found walking up from Cwd, then default_scope in config.yaml;
// otherwise an error that names every way to say it. The picker preselects nothing (§14):
// default_scope and a found project apply off a terminal only.
func (e *Engine) ResolveScope(req ScopeRequest) (ScopeResolution, error) {
	switch {
	case req.User && req.Project:
		return ScopeResolution{}, errs.WithHints(errors.New("--user and --project disagree"), "name one scope: --project or --user")
	case req.User && req.ProjectDir != "":
		return ScopeResolution{}, errs.WithHints(errors.New("--user cannot be combined with -C/--root"), "user scope has no project directory")
	}
	root, found := findProject(req.Cwd)
	if req.ProjectDir != "" {
		root, found = req.ProjectDir, dirExists(filepath.Join(req.ProjectDir, ".lola"))
	}
	if req.Scope != "" {
		names, err := ParseScope(req.Scope)
		if err != nil {
			return ScopeResolution{}, err
		}
		if req.User && (len(names) != 1 || names[0] != config.ScopeUser) {
			return ScopeResolution{}, errs.WithHints(fmt.Errorf("--user and --scope %s disagree", req.Scope),
				"drop --user, or use --scope user")
		}
		if req.Project && (len(names) != 1 || names[0] != config.ScopeProject) {
			return ScopeResolution{}, errs.WithHints(fmt.Errorf("--project and --scope %s disagree", req.Scope),
				"drop --project, or use --scope project")
		}
		if req.ProjectDir != "" && len(names) == 1 && names[0] == config.ScopeUser {
			return ScopeResolution{}, errs.WithHints(errors.New("--scope user cannot take -C: user scope has no project directory"),
				"drop -C, or use --scope project or --scope both")
		}
		if len(names) > 1 && !req.Both {
			return ScopeResolution{}, req.oneScope("--scope " + req.Scope)
		}
		return ScopeResolution{Scopes: Scopes(names, root, SourceFlag), ProjectRoot: root, Found: found}, nil
	}
	if req.User {
		return ScopeResolution{Scopes: Scopes([]string{config.ScopeUser}, "", SourceFlag)}, nil
	}
	if req.Project || req.ProjectDir != "" {
		return ScopeResolution{Scopes: Scopes([]string{config.ScopeProject}, root, SourceFlag), ProjectRoot: root, Found: found}, nil
	}
	cfg, err := config.LoadConfig(e.Paths.Config)
	if err != nil {
		return ScopeResolution{}, err
	}
	var preset []string
	if cfg.DefaultScope != "" {
		if preset, err = ParseScope(cfg.DefaultScope); err != nil {
			return ScopeResolution{}, err
		}
	}
	if req.Interactive {
		return ScopeResolution{Pick: true, ProjectRoot: root, Found: found}, nil
	}
	if found {
		return ScopeResolution{Scopes: Scopes([]string{config.ScopeProject}, root, SourceDiscovered), ProjectRoot: root, Found: true}, nil
	}
	if len(preset) > 1 && !req.Both {
		return ScopeResolution{}, req.oneScope("default_scope: both in " + e.Paths.Config)
	}
	if preset != nil {
		return ScopeResolution{Scopes: Scopes(preset, root, SourceConfig), ProjectRoot: root}, nil
	}
	hints := []string{"--project acts on this directory (-C <dir> picks another)", "--user acts on your user scope, for every project"}
	if req.Both {
		hints = append(hints, "--scope both does both")
	}
	return ScopeResolution{}, errs.WithHints(fmt.Errorf("lola %s needs a scope: no .lola/ project was found in %s or above it", req.Command, untrusted.Text(req.Cwd, untrusted.MaxName)),
		append(hints, "or set a default: default_scope: project in "+e.Paths.Config)...)
}

// oneScope refuses two scopes, which why names, for a command that acts on one (!Both).
func (req ScopeRequest) oneScope(why string) error {
	return errs.WithHints(fmt.Errorf("lola %s acts on one scope at a time, and %s names two", req.Command, why),
		"name one: --project or --user")
}
