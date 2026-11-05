// Package source parses source arguments (§7.2) and fetches module content into staging.
package source

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Kind is the type of a source.
type Kind string

// KindFolder is a local directory.
const KindFolder Kind = "folder"

// AcceptedForms names the source forms this build understands; used in error hints.
const AcceptedForms = "a directory path or a file:// URL to a directory"

// Spec is a parsed source. Location is an absolute path for folders.
type Spec struct {
	Kind     Kind
	Location string
	Ref      string
	Path     string
}

// Parse classifies arg. ok is false when arg is not a source form at all (for example a
// bare module name); err is set when arg is a source form that is unusable.
func Parse(arg, cwd string) (Spec, bool, error) {
	p, isURL := arg, strings.HasPrefix(arg, "file://")
	if isURL {
		u, err := url.Parse(arg)
		if err != nil {
			return Spec{}, false, fmt.Errorf("%s: %w", arg, err)
		}
		if u.Host != "" && u.Host != "localhost" {
			return Spec{}, false, fmt.Errorf("%s: file URLs must not name a remote host", arg)
		}
		p = u.Path
	} else if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	fi, err := os.Stat(p)
	if err != nil || !fi.IsDir() {
		if isURL {
			return Spec{}, false, fmt.Errorf("%s: not a directory", arg)
		}
		return Spec{}, false, nil
	}
	return Spec{Kind: KindFolder, Location: filepath.Clean(p)}, true, nil
}
