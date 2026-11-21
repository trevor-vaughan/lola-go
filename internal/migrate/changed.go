package migrate

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/module"
)

// changedComponents lists, as kind/name, the components of fetched (the module at fetchedDir) that
// differ from, or are absent in, the Python lola's copy of the module at copyDir: what the Python
// lola never installed as it is now. A copy that is missing or cannot be loaded proves nothing, so
// every component counts as changed. known is the target ids a manifest's components may name.
func changedComponents(fetched *module.Module, fetchedDir, copyDir string, known []string) []string {
	old, err := module.Load(copyDir, filepath.Base(copyDir), known)
	if err != nil {
		old = &module.Module{}
	}
	var changed []string
	for _, s := range fetched.Skills {
		o, found := find(old.Skills, func(c module.Skill) bool { return c.Name == s.Name })
		if !found || !slices.Equal(s.Targets, o.Targets) ||
			digest(fetchedDir, s.Exclude, s.Dir, s.AttrsFile) != digest(copyDir, o.Exclude, o.Dir, o.AttrsFile) {
			changed = append(changed, engine.Component{Kind: KindSkill, Name: s.Name}.String())
		}
	}
	for _, c := range fetched.Commands {
		o, found := find(old.Commands, func(x module.Command) bool { return x.Name == c.Name })
		if !found || !slices.Equal(c.Targets, o.Targets) ||
			digest(fetchedDir, nil, c.File, c.SidecarDir, c.AttrsFile) != digest(copyDir, nil, o.File, o.SidecarDir, o.AttrsFile) {
			changed = append(changed, engine.Component{Kind: KindCommand, Name: c.Name}.String())
		}
	}
	for _, a := range fetched.Agents {
		o, found := find(old.Agents, func(x module.Agent) bool { return x.Name == a.Name })
		if !found || !slices.Equal(a.Targets, o.Targets) ||
			digest(fetchedDir, nil, a.File, a.AttrsFile) != digest(copyDir, nil, o.File, o.AttrsFile) {
			changed = append(changed, engine.Component{Kind: KindAgent, Name: a.Name}.String())
		}
	}
	for _, m := range fetched.MCPServers {
		if o, found := find(old.MCPServers, func(x module.MCPServer) bool { return x.Name == m.Name }); !found || !reflect.DeepEqual(m, o) {
			changed = append(changed, engine.Component{Kind: KindMCP, Name: m.Name}.String())
		}
	}
	for _, r := range fetched.Rules {
		if o, found := find(old.Rules, func(x module.Rule) bool { return x.Name == r.Name }); !found || !reflect.DeepEqual(r, o) {
			changed = append(changed, engine.Component{Kind: config.KindRule, Name: r.Name}.String())
		}
	}
	return changed
}

// pythonContent is the directory of the Python lola's copy of a module that holds its components:
// the Python lola copies the whole source when its source.yml names a content_dirname, then reads
// "/" as the copy's root and any other name as a subdirectory of it; with none it copies the
// module it found and reads its module/ subdirectory when it has one (models.py, parsers.py).
func pythonContent(copyDir, contentDirname string) string {
	switch contentDirname {
	case "/":
		return copyDir
	case "":
		if isDir(filepath.Join(copyDir, "module")) {
			return filepath.Join(copyDir, "module")
		}
		return copyDir
	}
	return filepath.Join(copyDir, filepath.FromSlash(contentDirname))
}

// find is the first of list that match accepts.
func find[T any](list []T, match func(T) bool) (T, bool) {
	if i := slices.IndexFunc(list, match); i >= 0 {
		return list[i], true
	}
	var zero T
	return zero, false
}

// digest is a sha256 of the regular files below each of rels (content-relative; "" is skipped),
// by path and content, leaving out what exclude (relative to rels[0]) names. A path that is
// missing or unreadable digests as such: it is never equal to one that holds a file.
func digest(dir string, exclude []string, rels ...string) string {
	h := sha256.New()
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "unreadable"
	}
	defer func() { _ = root.Close() }() // read-only
	for i, rel := range rels {
		if rel == "" {
			continue
		}
		h.Write([]byte("\x00path\x00" + rel + "\x00"))
		err := fs.WalkDir(root.FS(), rel, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			sub := p // relative to rel, "" for rel itself
			if rel != "." {
				sub = strings.TrimPrefix(strings.TrimPrefix(p, rel), "/")
			} else if p == "." {
				sub = ""
			}
			if i == 0 && sub != "" && slices.ContainsFunc(exclude, func(x string) bool { return sub == x || strings.HasPrefix(sub, x+"/") }) {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() {
				h.Write([]byte("\x00other\x00" + sub + "\x00" + d.Type().String() + "\x00"))
				return nil
			}
			b, err := root.ReadFile(p)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(b)
			h.Write([]byte("\x00file\x00" + sub + "\x00" + hex.EncodeToString(sum[:]) + "\x00"))
			return nil
		})
		if err != nil {
			h.Write([]byte("\x00unreadable\x00"))
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}
