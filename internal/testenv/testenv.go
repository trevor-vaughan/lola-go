// Package testenv isolates specs from the developer's home, XDG dirs and git config.
package testenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/onsi/ginkgo/v2"
)

// Isolate points HOME, every XDG base dir and git's config/identity at a fresh temp dir
// for the current spec and returns that dir. PATH becomes <dir>/bin, where a spec may put
// fake assistant binaries, followed by a separate temp dir that symlinks only the tools specs and
// their hook scripts run (specTools). Sharing no directory with the developer's PATH means a
// real assistant is never found, wherever it is installed. Values are restored after the spec.
func Isolate() string {
	t := ginkgo.GinkgoT()
	root := t.TempDir()
	tools := t.TempDir() // outside root, which specs may hash or walk
	search := []string{filepath.Join(root, "bin"), tools}
	realTools := map[string]string{} // resolved now: t.Setenv below replaces PATH
	for _, tool := range specTools {
		if p, err := exec.LookPath(tool); err == nil {
			realTools[tool] = p
		}
	}
	env := map[string]string{
		"HOME":                filepath.Join(root, "home"),
		"PATH":                strings.Join(search, string(os.PathListSeparator)),
		"XDG_CONFIG_HOME":     filepath.Join(root, "config"),
		"XDG_CACHE_HOME":      filepath.Join(root, "cache"),
		"XDG_STATE_HOME":      filepath.Join(root, "state"),
		"XDG_DATA_HOME":       filepath.Join(root, "data"),
		"GIT_CONFIG_GLOBAL":   os.DevNull,
		"GIT_CONFIG_SYSTEM":   os.DevNull,
		"GIT_AUTHOR_NAME":     "lola-test",
		"GIT_AUTHOR_EMAIL":    "lola-test@invalid",
		"GIT_COMMITTER_NAME":  "lola-test",
		"GIT_COMMITTER_EMAIL": "lola-test@invalid",
		"NO_COLOR":            "1",
		"LOLA_COLOR":          "",
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	for _, dir := range []string{"home", "bin"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatalf("testenv: %v", err)
		}
	}
	for tool, p := range realTools {
		if err := os.Symlink(p, filepath.Join(tools, tool)); err != nil {
			t.Fatalf("testenv: %v", err)
		}
	}
	return root
}

// specTools are the programs specs, fake assistants and hook scripts invoke through PATH.
var specTools = []string{"sh", "git", "sleep", "touch", "cat", "mkdir", "rm", "cp", "env"}
