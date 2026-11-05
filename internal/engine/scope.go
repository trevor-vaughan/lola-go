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

// ScopeRequest is what an install was told about its scope.
type ScopeRequest struct {
	Scope       string // --scope: project, user, both or project,user; "" when not given
	User        bool   // --user
	ProjectDir  string // -C, absolute; "" when not given
	Cwd         string
	Interactive bool // the scope picker can run
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

// ResolveInstallScope implements the resolution table of Plan 5c §5.1: --scope, then --user,
// then -C, then the picker on a terminal, then a .lola/ project found walking up from Cwd,
// then default_scope in config.yaml; otherwise an error that names every way to say it. The
// picker preselects nothing (§14): default_scope and a found project apply off a terminal only.
func (e *Engine) ResolveInstallScope(req ScopeRequest) (ScopeResolution, error) {
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
		if req.ProjectDir != "" && len(names) == 1 && names[0] == config.ScopeUser {
			return ScopeResolution{}, errs.WithHints(errors.New("--scope user cannot take -C: user scope has no project directory"),
				"drop -C, or use --scope project or --scope both")
		}
		return ScopeResolution{Scopes: Scopes(names, root, SourceFlag), ProjectRoot: root, Found: found}, nil
	}
	if req.User {
		return ScopeResolution{Scopes: Scopes([]string{config.ScopeUser}, "", SourceFlag)}, nil
	}
	if req.ProjectDir != "" {
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
	if preset != nil {
		return ScopeResolution{Scopes: Scopes(preset, root, SourceConfig), ProjectRoot: root}, nil
	}
	return ScopeResolution{}, errs.WithHints(fmt.Errorf("lola install needs a scope: no .lola/ project was found in %s or above it", untrusted.Text(req.Cwd, untrusted.MaxName)),
		"--scope project installs into this directory (-C <dir> picks another)",
		"--user installs for your user, in every project",
		"--scope both does both",
		"or set a default: default_scope: project in "+e.Paths.Config)
}
