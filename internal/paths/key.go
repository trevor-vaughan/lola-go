package paths

import (
	"path/filepath"
	"runtime"
	"strings"
)

// ProjectKey is the canonical form of a project directory: absolute, clean, symlinks resolved
// (a path that cannot be resolved, such as a directory that no longer exists, is kept
// lexically), and on Windows with forward slashes and a lower-case drive letter. The project lock and the project sections of selections.yaml both
// use it, so every spelling of one project is one key.
func ProjectKey(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		real = abs
	}
	return keyForm(runtime.GOOS, real), nil
}

// SameKey reports whether two project keys name one project: case-insensitively on darwin and
// Windows, whose file systems usually are.
func SameKey(a, b string) bool { return KeyID(a) == KeyID(b) }

// KeyID is the form of a project key that compares as the OS compares paths. The project lock
// file is named after it.
func KeyID(key string) string { return keyID(runtime.GOOS, key) }

// keyForm writes a clean absolute path as a key for goos.
func keyForm(goos, p string) string {
	if goos != "windows" {
		return p
	}
	p = strings.ReplaceAll(p, `\`, "/")
	if len(p) >= 2 && p[1] == ':' {
		p = strings.ToLower(p[:1]) + p[1:]
	}
	return p
}

func keyID(goos, key string) string {
	if goos == "windows" || goos == "darwin" {
		return strings.ToLower(keyForm(goos, key))
	}
	return key
}
