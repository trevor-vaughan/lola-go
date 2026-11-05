package cli

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// expandHome replaces a leading "~" or "~/" in p with home. Other forms, such as "~name", are
// left as typed: lola does not look up other users' homes.
func expandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		return filepath.Join(home, rest)
	}
	return p
}

// dirSuggestions lists the completions of a directory path typed so far: every directory whose
// name starts with the last element of typed, in the directory the rest of typed names, each
// returned as typed plus the completion and a trailing "/". A "~/" start is read from home and
// a relative one from cwd; hidden directories are offered only when the element starts with ".".
// A directory that cannot be read has no completions.
func dirSuggestions(typed, home, cwd string) []string {
	head, tail := typed[:strings.LastIndex(typed, "/")+1], typed[strings.LastIndex(typed, "/")+1:]
	dir := expandHome(head, home)
	if head == "~" {
		return nil
	}
	if dir == "" {
		dir = cwd
	} else if !filepath.IsAbs(dir) {
		dir = filepath.Join(cwd, dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, tail) || strings.HasPrefix(name, ".") && !strings.HasPrefix(tail, ".") {
			continue
		}
		if fi, err := os.Stat(filepath.Join(dir, name)); err != nil || !fi.IsDir() {
			continue
		}
		out = append(out, head+name+"/")
	}
	sort.Strings(out)
	return out
}
