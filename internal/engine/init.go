package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/module"
)

// FormatLola is the native lola layout lola init can scaffold.
const FormatLola = "lola"

// InitFormats are the formats lola init scaffolds; the first is the default (Plan 5b design §8).
func InitFormats() []string { return []string{module.FormatAgentPlugins, FormatLola} }

// Scaffolded is what Init wrote.
type Scaffolded struct {
	Name   string   `json:"name"`
	Path   string   `json:"path"`
	Format string   `json:"format"`
	Files  []string `json:"files"` // relative to Path
}

const (
	helloSkill   = "Greet the user by name and offer help.\n"
	helloCommand = "---\ndescription: Say hello\n---\nSay hello to $ARGUMENTS.\n"
)

// scaffold returns the files of a new module of format, relative to its root.
func scaffold(format, name string) ([][2]string, error) {
	if format == FormatLola {
		return [][2]string{
			{"lola.yml", "name: " + name + "\ndescription: Describe what this module does\nversion: 0.1.0\n"},
			{"skills/hello/SKILL.md", "---\ndescription: Greet the user. Replace this skill with your own.\n---\n" + helloSkill},
			{"commands/hello.md", helloCommand},
		}, nil
	}
	manifest, err := json.MarshalIndent(map[string]string{"$schema": module.AgentPluginsSchema, "name": name, "version": "0.1.0",
		"description": "Describe what this plugin does"}, "", "  ")
	if err != nil {
		return nil, err
	}
	return [][2]string{
		{"plugin.json", string(manifest) + "\n"},
		{"skills/hello/SKILL.md", "---\nname: hello\ndescription: Greet the user. Replace this skill with your own.\n---\n" + helloSkill},
		{"dev.getlola/commands/hello.md", helloCommand},
	}, nil
}

// Init scaffolds a module of format in dir, named after the directory: an Agent Plugins
// package (plugin.json, a portable skill, a dev.getlola command) or a native lola module. It
// refuses a directory that already holds a module manifest, and writes nothing when any file it
// would write exists.
func Init(dir, format string) (Scaffolded, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Scaffolded{}, err
	}
	name := filepath.Base(abs)
	switch format {
	case module.FormatAgentPlugins:
		err = module.ValidatePluginName(name)
	case FormatLola:
		err = module.ValidateName("module", name)
	default:
		var each []string
		for _, f := range InitFormats() {
			each = append(each, errs.ShellJoin([]string{"--format", f}))
		}
		return Scaffolded{}, errs.WithHints(fmt.Errorf("lola init cannot scaffold format %q", format),
			"use "+strings.Join(each, " or "))
	}
	if err != nil {
		hints := []string{"run lola init in a directory whose name is a valid module name"}
		if format == module.FormatAgentPlugins {
			hints = append(hints, "or pass --format lola, whose names may also hold '_'")
		}
		return Scaffolded{}, errs.WithHints(err, hints...)
	}
	for _, m := range module.FormatManifests() {
		if _, err := os.Lstat(filepath.Join(abs, m)); err == nil {
			return Scaffolded{}, errs.WithHints(fmt.Errorf("%s is already a module (it has %s)", dir, m), "add to it by hand, or run lola init in a new directory")
		}
	}
	files, err := scaffold(format, name)
	if err != nil {
		return Scaffolded{}, err
	}
	out := Scaffolded{Name: name, Path: abs, Format: format, Files: []string{}}
	for _, f := range files {
		if err := checkNew(abs, f[0]); err != nil {
			return Scaffolded{}, err
		}
	}
	for _, f := range files {
		if err := writeNew(filepath.Join(abs, f[0]), f[1]); err != nil {
			return Scaffolded{}, err
		}
		out.Files = append(out.Files, f[0])
	}
	return out, nil
}

// checkNew refuses to scaffold rel under root when a directory on the way is a symbolic link
// (the write would land outside root) or when rel itself exists.
func checkNew(root, rel string) error {
	cur := root
	for _, part := range strings.Split(rel, "/") {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return nil
		case err != nil:
			return err
		case fi.Mode()&fs.ModeSymlink != 0 && cur != filepath.Join(root, rel):
			return errs.WithHints(fmt.Errorf("%s is a symbolic link; lola init never writes through links", cur),
				"remove the link, or run lola init in a new directory")
		}
	}
	return errs.WithHints(fmt.Errorf("%s already exists; lola init never overwrites files", filepath.Join(root, rel)), existsHint)
}

// existsHint coaches the refusal to overwrite.
const existsHint = "remove or move it, or run lola init in a new directory"

// writeNew creates a file that must not exist yet.
func writeNew(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return errs.WithHints(fmt.Errorf("%s already exists; lola init never overwrites files", path), existsHint)
	}
	if err != nil {
		return err
	}
	if _, err := f.WriteString(body); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
