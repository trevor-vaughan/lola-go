// Package testenv isolates specs from the developer's home, XDG dirs and git config.
package testenv

import (
	"os"
	"path/filepath"

	"github.com/onsi/ginkgo/v2"
)

// Isolate points HOME, every XDG base dir and git's config/identity at a fresh temp dir
// for the current spec and returns that dir. Values are restored after the spec.
func Isolate() string {
	t := ginkgo.GinkgoT()
	root := t.TempDir()
	env := map[string]string{
		"HOME":                filepath.Join(root, "home"),
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
	if err := os.MkdirAll(filepath.Join(root, "home"), 0o700); err != nil {
		t.Fatalf("testenv: %v", err)
	}
	return root
}
