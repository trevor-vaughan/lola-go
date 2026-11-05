package engine

import (
	"path/filepath"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/target"
)

// projectSource is a local source of an entry that lies inside a project (spec 2026-10-29 §16.2):
// path, the source as a file of yours reads it, and root, the project. User scope never asks about
// an entry of yours, so what changes there later installs unreviewed.
type projectSource struct {
	path, root string
}

// inProject is src, an entry's source as a file of yours reads it (absolute), as a projectSource
// when it is a local path (a folder or a local archive) at or below a directory holding .lola/,
// found as a scope finds its project (findProject) from src with symbolic links evaluated. A URL,
// git's scp form and a marketplace address are no local path, and your home directory is never a
// project (IsHome).
func (e *Engine) inProject(src string) (projectSource, bool) {
	if _, _, address := manifest.Address(src); address || strings.Contains(src, "://") || source.Host(src) != "" || !filepath.IsAbs(src) {
		return projectSource{}, false
	}
	root, found := findProject(canonicalRoot(src))
	if !found || e.IsHome(Scope{Kind: target.Project, ProjectDir: root}) {
		return projectSource{}, false
	}
	return projectSource{path: src, root: root}, true
}

// text says that module's entry in file installs from p, which installs unreviewed wherever that
// file is on (user scope, or a project a profile of yours is on in): an install's warning, and the
// start of a move's refusal.
func (p projectSource) text(module, file string) string {
	return shown(module) + " (" + shown(file) + ") installs from " + shown(p.path) + ", inside project " + shown(p.root) +
		": changes there install without review wherever that file is on"
}
