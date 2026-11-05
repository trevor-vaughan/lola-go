// Package testenv isolates specs from the developer's home, XDG dirs and git config.
package testenv

import (
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/onsi/ginkgo/v2"
)

// Isolate points HOME, every XDG base dir and git's config/identity at a fresh temp dir for
// the current spec, clears LOLA_HOME (the Python lola's home) and returns that dir. PATH
// becomes <dir>/bin, where a spec may put fake assistant binaries, followed by a separate temp
// dir that symlinks only the tools specs and their hook scripts run (specTools). Sharing no directory with the developer's PATH means a
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
		"LOLA_HOME":           "", // the Python lola's home, which lola migrate reads
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	// Specs run from a git hook or `git rebase --exec` inherit variables that point git at the
	// caller's repository, so a fixture's git commands would write into it. These are `git
	// rev-parse --local-env-vars`, plus GIT_NAMESPACE and GIT_CEILING_DIRECTORIES.
	for _, k := range []string{"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT",
		"GIT_OBJECT_DIRECTORY", "GIT_DIR", "GIT_WORK_TREE", "GIT_IMPLICIT_WORK_TREE", "GIT_GRAFT_FILE", "GIT_INDEX_FILE",
		"GIT_NO_REPLACE_OBJECTS", "GIT_REPLACE_REF_BASE", "GIT_PREFIX", "GIT_SHALLOW_FILE", "GIT_COMMON_DIR",
		"GIT_NAMESPACE", "GIT_CEILING_DIRECTORIES"} {
		t.Setenv(k, "") // restores the caller's value after the spec
		if err := os.Unsetenv(k); err != nil {
			t.Fatalf("testenv: %v", err)
		}
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

// ServeGit serves the bare repositories below root over loopback HTTP, with git's own
// http-backend, for the current spec, and returns the base URL: root/x.git is cloned from
// <url>/x.git. The backend reads no global or system git config. Call it after Isolate.
func ServeGit(root string) string {
	return ServeGitWith(root, nil)
}

// ServeGitWith is ServeGit that calls seen, when set, with every request before serving it, on
// the server's goroutine: a spec counts the requests lola makes, or looks at the file system
// while a fetch is in flight.
func ServeGitWith(root string, seen func(*http.Request)) string {
	t := ginkgo.GinkgoT()
	out, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Fatalf("testenv: git --exec-path: %v", err)
	}
	h := &cgi.Handler{Path: filepath.Join(strings.TrimSpace(string(out)), "git-http-backend"), Env: []string{
		"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_SYSTEM=" + os.DevNull}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			seen(r)
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// specTools are the programs specs, fake assistants and hook scripts invoke through PATH.
var specTools = []string{"sh", "git", "sleep", "touch", "cat", "mkdir", "rm", "cp", "env"}
