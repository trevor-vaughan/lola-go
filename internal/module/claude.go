package module

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// claudeManifest is a Claude Code plugin's manifest (code.claude.com plugins reference).
const claudeManifest = ".claude-plugin/plugin.json"

// claudeKept are manifest keys Claude Code itself does not use to load a plugin: lola keeps
// them under formats.claude-plugin and does not use them either.
var claudeKept = []string{"$schema", "displayName", "metadata", "icon", "documentationUrl", "supportUrl",
	"privacyPolicyUrl", "termsOfServiceUrl", "defaultEnabled"}

// claudeUnsupported are manifest keys for things lola does not install. They are kept under
// formats.claude-plugin, with a warning.
var claudeUnsupported = []string{"lspServers", "outputStyles", "workflows", "experimental", "settings", "userConfig",
	"channels", "dependencies", "types", "themes", "monitors"}

// claudeMetadata are the manifest keys that become module metadata; claudeComponents those
// that declare artifacts.
var (
	claudeMetadata   = []string{"name", "version", "description", "author", "homepage", "repository", "license", "keywords"}
	claudeComponents = []string{"skills", "commands", "agents", "mcpServers", "hooks"}
)

// claudePlugin adapts a Claude Code plugin (Plan 5b design §5).
type claudePlugin struct{}

func (claudePlugin) manifest() string { return claudeManifest }

func (claudePlugin) read(dir string) (*lolaFile, []string, bool, error) {
	var raw map[string]json.RawMessage
	if err := readJSON(dir, claudeManifest, &raw); err != nil {
		return nil, nil, false, err
	}
	if raw == nil {
		return nil, nil, false, errs.WithHints(fmt.Errorf("%s must be a JSON object", claudeManifest), `start it with {"name": "..."}`)
	}
	c := &claudeReader{dir: dir, raw: raw, kept: map[string]any{}}
	lf, err := c.convert()
	return lf, c.warnings, err == nil, err
}

// claudeReader converts one manifest.
type claudeReader struct {
	dir      string
	raw      map[string]json.RawMessage
	kept     map[string]any // formats.claude-plugin
	warnings []string
}

// fail is an error about the manifest key, with hints.
func (c *claudeReader) fail(key, msg string, hints ...string) error {
	return errs.WithHints(fmt.Errorf("%s: %s: %s", claudeManifest, key, msg), hints...)
}

// decode reads key into v when the manifest has it; want names the JSON type for the error.
func (c *claudeReader) decode(key string, v any, want string) error {
	b, ok := c.raw[key]
	if !ok {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return c.fail(key, "must be "+want, "fix the value of "+key+" in "+claudeManifest)
	}
	return nil
}

// keep records key's raw value under formats.claude-plugin.
func (c *claudeReader) keep(key string, raw json.RawMessage) error {
	v, err := jsonValue(raw)
	if err != nil {
		return c.fail(safeName(key), err.Error(), "fix the value of "+safeName(key)+" in "+claudeManifest)
	}
	c.kept[key] = v
	return nil
}

func (c *claudeReader) convert() (*lolaFile, error) {
	lf := &lolaFile{Format: FormatClaudePlugin}
	var author *Author
	for _, f := range []struct {
		key  string
		v    any
		want string
	}{
		{"name", &lf.Name, "a string"}, {"version", &lf.Version, "a string"}, {"description", &lf.Description, "a string"},
		{"homepage", &lf.Homepage, "a string"}, {"repository", &lf.Repository, "a string"}, {"license", &lf.License, "a string"},
		{"keywords", &lf.Keywords, "a list of strings"}, {"author", &author, "an object with name, email and url strings"},
	} {
		if err := c.decode(f.key, f.v, f.want); err != nil {
			return nil, err
		}
	}
	lf.Author = author
	if lf.Name == "" {
		return nil, c.fail("name", "is required", "Claude Code plugins name themselves in .claude-plugin/plugin.json")
	}
	if err := ValidateName("module", lf.Name); err != nil {
		return nil, errs.WithHints(fmt.Errorf("%s: %w", claudeManifest, err), "lola needs a module name it can use as a directory name: rename the plugin, or add a lola.yml "+
			"with a name lola can use; a lola.yml replaces the plugin manifest, so it must also list the plugin's components")
	}
	for _, key := range slices.Sorted(maps.Keys(c.raw)) {
		switch {
		case slices.Contains(claudeMetadata, key), slices.Contains(claudeComponents, key):
			continue
		case slices.Contains(claudeKept, key):
		case slices.Contains(claudeUnsupported, key):
			c.warnings = append(c.warnings, fmt.Sprintf("%s: %s is not supported: lola does not install it; it is kept under formats.%s",
				claudeManifest, key, FormatClaudePlugin))
		default:
			v, err := jsonValue(c.raw[key])
			if err != nil {
				c.warnings = append(c.warnings, fmt.Sprintf("%s: %s is not a plugin key lola knows, and its value cannot be kept (%v); it was dropped",
					claudeManifest, safeName(key), err))
				continue
			}
			c.warnings = append(c.warnings, fmt.Sprintf("%s: %s is not a plugin key lola knows; it is kept under formats.%s",
				claudeManifest, safeName(key), FormatClaudePlugin))
			c.kept[key] = v
			continue
		}
		if err := c.keep(key, c.raw[key]); err != nil {
			return nil, err
		}
	}
	for _, key := range claudeComponents {
		if b, ok := c.raw[key]; ok && string(bytes.TrimSpace(b)) == "null" {
			return nil, c.fail(key, "must not be null", "remove "+key+" to use the default location, or give it a value")
		}
	}
	var comps components
	var err error
	if comps.Skills, err = c.skills(lf.Name); err != nil {
		return nil, err
	}
	if comps.Commands, err = c.commands(); err != nil {
		return nil, err
	}
	if comps.Agents, err = c.agents(); err != nil {
		return nil, err
	}
	if comps.MCP, err = c.mcp(); err != nil {
		return nil, err
	}
	if comps.RuntimeHooks, err = c.hooks(); err != nil {
		return nil, err
	}
	comps.Rules = &[]pathEntry{} // a Claude plugin has no rules; a rules/ directory is not one
	if len(*comps.Skills)+len(*comps.Commands)+len(*comps.Agents)+len(*comps.MCP)+len(*comps.RuntimeHooks) == 0 {
		var hints []string
		for _, w := range c.warnings {
			hints = append(hints, "also noted: "+w)
		}
		return nil, errs.WithHints(fmt.Errorf("%s declares nothing lola can install", claudeManifest),
			append([]string{"lola installs skills, commands, agents, MCP servers and hooks; add one under the default directories or list it in the manifest"}, hints...)...)
	}
	lf.Components = &comps
	if len(c.kept) > 0 {
		lf.Formats = map[string]any{FormatClaudePlugin: c.kept}
	}
	return lf, nil
}

// paths reads key as one path or a list of paths.
func (c *claudeReader) paths(key string) ([]string, error) {
	var one string
	if err := json.Unmarshal(c.raw[key], &one); err == nil {
		return []string{one}, nil
	}
	var many []string
	if err := json.Unmarshal(c.raw[key], &many); err != nil {
		return nil, c.fail(key, "must be a path or a list of paths", `write it as "./path" or ["./a", "./b"]`)
	}
	return many, nil
}

// rel resolves a manifest path (§5.2): it starts with "./" (skills also take "."), and stays
// in the plugin, through no symbolic link, at something that exists. It returns the path
// relative to the plugin root and whether it is a directory.
func (c *claudeReader) rel(key, p string, rootOK bool) (string, bool, error) {
	if rootOK && (p == "." || p == "./") {
		return ".", true, nil
	}
	rest, ok := strings.CutPrefix(p, "./")
	if !ok || rest == "" {
		return "", false, c.fail(key, fmt.Sprintf("path %q must start with ./", safeName(p)),
			"Claude Code reads component paths relative to the plugin root, written ./path")
	}
	if !filepath.IsLocal(rest) {
		return "", false, c.fail(key, fmt.Sprintf("path %q must stay inside the plugin", safeName(p)),
			"give a path from the plugin root, written ./path, such as ./skills/review")
	}
	return localPath(c.dir, claudeManifest+": "+key, rest)
}

// ioFail reports a read error at rel, without the directory lola staged the plugin in.
func (c *claudeReader) ioFail(key, rel string, err error) error {
	return c.fail(key, safeName(rel)+": "+untrusted.CleanErr(err).Error(), "check that the path can be read")
}

// defaultDir reports whether rel is a real directory in the plugin; a symbolic link in its place
// warns, since lola never follows links.
func (c *claudeReader) defaultDir(rel string) bool {
	if fi, err := os.Lstat(filepath.Join(c.dir, rel)); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		c.warnings = append(c.warnings, safeName(rel)+" is a symbolic link; skipped")
		return false
	}
	return c.isDir(rel)
}

// isDir reports whether rel names a real directory in the plugin.
func (c *claudeReader) isDir(rel string) bool {
	fi, err := os.Lstat(filepath.Join(c.dir, rel))
	return err == nil && fi.IsDir()
}

// hasSkill reports whether rel is a directory holding SKILL.md.
func (c *claudeReader) hasSkill(rel string) bool {
	fi, err := os.Lstat(filepath.Join(c.dir, rel, "SKILL.md"))
	return err == nil && fi.Mode().IsRegular()
}

// claimName records that source provides the artifact of kind called name; sources maps
// names to the source that provides them. It reports whether source already did (a path
// listed twice), and refuses a name two sources provide.
func claimName(sources map[string]string, kind, name, source string) (bool, error) {
	prev, ok := sources[name]
	switch {
	case ok && prev == source:
		return true, nil
	case ok:
		return false, errs.WithHints(fmt.Errorf("%s: %s and %s are both %s %q; lola gives each %s one name, with no namespace",
			claudeManifest, safeName(prev), safeName(source), kind, name, kind), "rename one of them")
	}
	sources[name] = source
	return false, nil
}

// derived validates a name lola derives from a path.
func derived(kind, name, source string) error {
	if err := ValidateName(kind, name); err != nil {
		return errs.WithHints(fmt.Errorf("%s: %s: %w", claudeManifest, safeName(source), err), "rename it")
	}
	return nil
}

// skills adds the listed directories to the default skills/ (§5.3) and expands each into its
// skills: a directory holding SKILL.md is one, otherwise each <name>/SKILL.md below it is.
func (c *claudeReader) skills(module string) (*[]pathEntry, error) {
	var dirs []string
	if c.defaultDir("skills") {
		dirs = append(dirs, "skills")
	}
	if _, ok := c.raw["skills"]; ok {
		ps, err := c.paths("skills")
		if err != nil {
			return nil, err
		}
		for _, p := range ps {
			rel, isDir, err := c.rel("skills", p, true)
			if err != nil {
				return nil, err
			}
			if !isDir {
				return nil, c.fail("skills", fmt.Sprintf("path %q is not a directory", safeName(p)), "list skill directories")
			}
			if !slices.Contains(dirs, rel) {
				dirs = append(dirs, rel)
			}
		}
	} else if len(dirs) == 0 && c.hasSkill(".") {
		dirs = append(dirs, ".") // a plugin with only a root SKILL.md is one skill
	}
	out, sources := []pathEntry{}, map[string]string{}
	add := func(rel string) error {
		name := path.Base(rel)
		if rel == "." {
			name = module
		}
		if err := derived("skill", name, rel); err != nil {
			return err
		}
		if seen, err := claimName(sources, "skill", name, rel); err != nil || seen {
			return err
		}
		out = append(out, pathEntry{Path: rel})
		return nil
	}
	for _, d := range dirs {
		if c.hasSkill(d) {
			if err := add(d); err != nil {
				return nil, err
			}
			continue
		}
		es, err := os.ReadDir(filepath.Join(c.dir, d))
		if err != nil {
			return nil, c.ioFail("skills", d, err)
		}
		for _, e := range es {
			child := path.Join(d, e.Name())
			switch {
			case e.Type()&fs.ModeSymlink != 0:
				c.warnings = append(c.warnings, safeName(child)+" is a symbolic link; skipped")
			case !e.IsDir():
			case c.hasSkill(child):
				if err := add(child); err != nil {
					return nil, err
				}
			default:
				c.warnings = append(c.warnings, safeName(child)+" has no SKILL.md; skipped")
			}
		}
	}
	return &out, nil
}

// markdown lists the .md files under rel, recursively, sorted; a symbolic link is refused.
func (c *claudeReader) markdown(key, rel string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(filepath.Join(c.dir, rel), func(p string, d fs.DirEntry, err error) error {
		r, relErr := filepath.Rel(c.dir, p)
		if relErr != nil {
			return relErr
		}
		r = filepath.ToSlash(r)
		if err != nil {
			return c.ioFail(key, r, err)
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			return c.fail(key, fmt.Sprintf("%s is a symbolic link", safeName(r)), "lola never follows links in a module")
		case d.Type().IsRegular() && strings.HasSuffix(r, ".md"):
			out = append(out, r)
		}
		return nil
	})
	return out, err
}

// files expands a replacing key's paths (§5.3): files, and with dirs, directories of .md
// files. Without the key it expands the default directory. It warns, as Claude Code does,
// when the key leaves the default directory out.
func (c *claudeReader) files(key, def string, dirs bool, ps []string) ([]string, error) {
	if _, ok := c.raw[key]; !ok {
		if !c.defaultDir(def) {
			return nil, nil
		}
		return c.markdown(key, def)
	}
	var out []string
	usesDefault := false
	for _, p := range ps {
		rel, isDir, err := c.rel(key, p, false)
		if err != nil {
			return nil, err
		}
		usesDefault = usesDefault || rel == def || strings.HasPrefix(rel, def+"/")
		switch {
		case isDir && !dirs:
			return nil, c.fail(key, fmt.Sprintf("path %q is a directory; list the .md files", safeName(p)),
				"Claude Code reads no agent directories; list each agent's .md file")
		case isDir:
			found, err := c.markdown(key, rel)
			if err != nil {
				return nil, err
			}
			out = append(out, found...)
		case !strings.HasSuffix(rel, ".md"):
			return nil, c.fail(key, fmt.Sprintf("path %q is not a .md file", safeName(p)), "list Markdown files, whose names end in .md")
		default:
			out = append(out, rel)
		}
	}
	c.warnIgnoredDefault(key, def, usesDefault)
	return out, nil
}

// warnIgnoredDefault warns, as Claude Code does, when key replaces the default directory def
// and nothing it lists is inside it.
func (c *claudeReader) warnIgnoredDefault(key, def string, usesDefault bool) {
	if !usesDefault && c.isDir(def) {
		c.warnings = append(c.warnings, fmt.Sprintf("the default %s/ directory is ignored because %s sets %s", def, claudeManifest, key))
	}
}

// claudeCommand is one value of the commands object map.
type claudeCommand struct {
	Source       *string  `json:"source"`
	Content      *string  `json:"content"`
	Description  *string  `json:"description"`
	ArgumentHint *string  `json:"argumentHint"`
	Model        *string  `json:"model"`
	AllowedTools []string `json:"allowedTools"`
}

// commands replaces the default commands/ (§5.3): paths of files and directories, or an object
// map whose entries become inline commands.
func (c *claudeReader) commands() (*[]commandEntry, error) {
	out, sources := []commandEntry{}, map[string]string{}
	var obj map[string]json.RawMessage
	if b, ok := c.raw["commands"]; ok && json.Unmarshal(b, &obj) == nil && obj != nil {
		usesDefault := false
		for _, name := range slices.Sorted(maps.Keys(obj)) {
			at := "commands." + safeName(name)
			var cc claudeCommand
			dec := json.NewDecoder(bytes.NewReader(obj[name]))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&cc); err != nil {
				return nil, c.fail(at, "must be an object with source or content, and description, argumentHint, model or allowedTools",
					"remove other keys, or move them into the command's Markdown file")
			}
			e := commandEntry{Name: name, Frontmatter: map[string]any{}}
			switch {
			case (cc.Source == nil) == (cc.Content == nil):
				return nil, c.fail(at, "sets exactly one of source or content", "give the command a source file or inline content, not both or neither")
			case cc.Source != nil:
				rel, isDir, err := c.rel(at, *cc.Source, false)
				if err != nil {
					return nil, err
				}
				switch {
				case isDir:
					return nil, c.fail(at, fmt.Sprintf("source %q is a directory", safeName(*cc.Source)), "name the command's .md file")
				case !strings.HasSuffix(rel, ".md"):
					return nil, c.fail(at, fmt.Sprintf("source %q is not a .md file", safeName(*cc.Source)), "list Markdown files, whose names end in .md")
				}
				usesDefault = usesDefault || strings.HasPrefix(rel, "commands/")
				e.Path = rel
			case *cc.Content == "":
				return nil, c.fail(at, "content is empty", "write the command's prompt, or remove the command")
			default:
				e.Content = *cc.Content
			}
			for k, v := range map[string]*string{"description": cc.Description, "argument-hint": cc.ArgumentHint, "model": cc.Model} {
				if v != nil {
					e.Frontmatter[k] = *v
				}
			}
			if cc.AllowedTools != nil {
				e.Frontmatter["allowed-tools"] = cc.AllowedTools
			}
			if len(e.Frontmatter) == 0 {
				e.Frontmatter = nil
			}
			if err := derived("command", name, at); err != nil {
				return nil, err
			}
			if _, err := claimName(sources, "command", name, at); err != nil {
				return nil, err
			}
			out = append(out, e)
		}
		c.warnIgnoredDefault("commands", "commands", usesDefault)
		return &out, nil
	}
	var ps []string
	if _, ok := c.raw["commands"]; ok {
		var err error
		if ps, err = c.paths("commands"); err != nil {
			return nil, c.fail("commands", "must be a path, a list of paths or an object of commands",
				`write it as "./commands/" or {"name": {"content": "..."}}`)
		}
	}
	files, err := c.files("commands", "commands", true, ps)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		name := strings.TrimSuffix(path.Base(f), ".md")
		if err := derived("command", name, f); err != nil {
			return nil, err
		}
		if seen, err := claimName(sources, "command", name, f); err != nil {
			return nil, err
		} else if !seen {
			out = append(out, commandEntry{Path: f})
		}
	}
	return &out, nil
}

// agents replaces the default agents/ (§5.3) with .md files; a listed directory is an error,
// as in Claude Code.
func (c *claudeReader) agents() (*[]pathEntry, error) {
	var ps []string
	if _, ok := c.raw["agents"]; ok {
		var err error
		if ps, err = c.paths("agents"); err != nil {
			return nil, err
		}
	}
	files, err := c.files("agents", "agents", false, ps)
	if err != nil {
		return nil, err
	}
	out, sources := []pathEntry{}, map[string]string{}
	for _, f := range files {
		name := strings.TrimSuffix(path.Base(f), ".md")
		if err := derived("agent", name, f); err != nil {
			return nil, err
		}
		if seen, err := claimName(sources, "agent", name, f); err != nil {
			return nil, err
		} else if !seen {
			out = append(out, pathEntry{Path: f})
		}
	}
	return &out, nil
}

// values reads a merging key (§5.3) as one value or a list of values: strings and objects.
func (c *claudeReader) values(key string) (values []json.RawMessage, isList bool) {
	b, ok := c.raw[key]
	if !ok {
		return nil, false
	}
	if err := json.Unmarshal(b, &values); err != nil {
		return []json.RawMessage{b}, false
	}
	return values, true
}

// mcpEntryFields are the fields of an MCP server that lola reads, found from mcpEntry's JSON tags.
func mcpEntryFields() []string {
	var out []string
	for f := range reflect.TypeFor[mcpEntry]().Fields() {
		out = append(out, strings.Split(f.Tag.Get("json"), ",")[0])
	}
	return out
}

// mcp merges the default .mcp.json with the declared files and inline servers (§5.3). A server
// name declared twice is an error (§3.2). Bundles are not supported: they warn and are kept.
func (c *claudeReader) mcp() (*[]mcpComponent, error) {
	out, sources := []mcpComponent{}, map[string]string{}
	file := func(rel string) error {
		if slices.ContainsFunc(out, func(e mcpComponent) bool { return e.Path == rel }) {
			return nil
		}
		servers, err := mcpFile(c.dir, rel)
		if err != nil {
			return err
		}
		for _, s := range servers {
			if _, err := claimName(sources, "MCP server", s.Name, rel); err != nil {
				return err
			}
		}
		out = append(out, mcpComponent{Path: rel})
		return nil
	}
	if fi, err := os.Lstat(filepath.Join(c.dir, ".mcp.json")); err == nil && !fi.IsDir() {
		if err := file(".mcp.json"); err != nil {
			return nil, err
		}
	}
	var bundles []any
	list, isList := c.values("mcpServers")
	for i, v := range list {
		var s string
		var inline map[string]mcpEntry
		switch {
		case json.Unmarshal(v, &s) == nil:
			lower := strings.ToLower(s)
			if strings.HasSuffix(lower, ".mcpb") || strings.HasSuffix(lower, ".dxt") || strings.Contains(s, "://") {
				c.warnings = append(c.warnings, fmt.Sprintf("%s: mcpServers: the MCP bundle %s is not supported: lola does not install it; it is kept under formats.%s",
					claudeManifest, safeName(s), FormatClaudePlugin))
				bundles = append(bundles, s)
				continue
			}
			rel, isDir, err := c.rel("mcpServers", s, false)
			if err != nil {
				return nil, err
			}
			if isDir {
				return nil, c.fail("mcpServers", fmt.Sprintf("path %q is a directory", safeName(s)), "list the .json files of MCP servers")
			}
			if !strings.HasSuffix(rel, ".json") {
				return nil, c.fail("mcpServers", fmt.Sprintf("path %q is not a .json file", safeName(s)), "list a .json file in the shape of .mcp.json, or an MCP bundle")
			}
			if err := file(rel); err != nil {
				return nil, err
			}
		case json.Unmarshal(v, &inline) == nil && inline != nil:
			var fields map[string]map[string]json.RawMessage
			if err := json.Unmarshal(v, &fields); err != nil {
				return nil, c.fail("mcpServers", err.Error(), "fix the MCP servers in "+claudeManifest)
			}
			for _, name := range slices.Sorted(maps.Keys(inline)) {
				srv, err := inline[name].server(name)
				if err != nil {
					return nil, c.fail("mcpServers", err.Error(), errs.Hints(err)...)
				}
				for _, f := range slices.Sorted(maps.Keys(fields[name])) {
					if !slices.Contains(mcpEntryFields(), f) {
						c.warnings = append(c.warnings, fmt.Sprintf("%s: mcpServers: server %q has the field %q, which lola does not read; it is ignored",
							claudeManifest, name, safeName(f)))
					}
				}
				source := "mcpServers." + name
				if isList {
					source = fmt.Sprintf("mcpServers[%d].%s", i, name)
				}
				if _, err := claimName(sources, "MCP server", name, source); err != nil {
					return nil, err
				}
				out = append(out, mcpComponent{Name: name, Server: &serverSpec{Type: srv.Transport, Command: srv.Command,
					Args: srv.Args, Env: srv.Env, Cwd: srv.Cwd, URL: srv.URL, Headers: srv.Headers}})
			}
		default:
			return nil, c.fail("mcpServers", "must be a path, an object of servers, or a list of them",
				`write it as "./mcp.json", {"name": {"command": "..."}} or a list of those`)
		}
	}
	if len(bundles) > 0 {
		c.kept["mcpServers"] = bundles
	}
	return &out, nil
}

// hooks merges the default hooks/hooks.json with the declared hooks files and inline event
// maps (§5.3). They become runtime hooks, which lola does not render yet.
func (c *claudeReader) hooks() (*[]hooksEntry, error) {
	out := []hooksEntry{}
	if _, err := os.Lstat(filepath.Join(c.dir, "hooks", "hooks.json")); err == nil {
		if err := c.hooksFile("hooks/hooks.json"); err != nil {
			return nil, err
		}
		out = append(out, hooksEntry{Path: "hooks/hooks.json"})
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, c.ioFail("hooks", "hooks/hooks.json", err)
	}
	list, _ := c.values("hooks")
	for _, v := range list {
		var s string
		inline, err := jsonValue(v)
		if err != nil {
			return nil, c.fail("hooks", err.Error(), "fix the hooks in "+claudeManifest)
		}
		obj, isObj := inline.(map[string]any)
		switch {
		case json.Unmarshal(v, &s) == nil:
			rel, isDir, err := c.rel("hooks", s, false)
			if err != nil {
				return nil, err
			}
			switch {
			case isDir:
				return nil, c.fail("hooks", fmt.Sprintf("path %q is a directory", safeName(s)), "list the .json files of hooks")
			case !strings.HasSuffix(rel, ".json"):
				return nil, c.fail("hooks", fmt.Sprintf("path %q is not a .json file", safeName(s)), `list a .json file shaped {"hooks": {...}}`)
			case slices.ContainsFunc(out, func(e hooksEntry) bool { return e.Path == rel }):
				continue
			}
			if err := c.hooksFile(rel); err != nil {
				return nil, err
			}
			out = append(out, hooksEntry{Path: rel})
		case isObj:
			if _, err := hookEvents(claudeManifest+": hooks", v); err != nil {
				return nil, err
			}
			out = append(out, hooksEntry{Hooks: obj})
		default:
			return nil, c.fail("hooks", "must be a path, an object of hook events, or a list of them",
				`write it as "./hooks.json", {"PreToolUse": [...]} or a list of those`)
		}
	}
	return &out, nil
}

// hooksFile checks that rel is a hooks file, {"hooks": {...}}, with a valid event map.
func (c *claudeReader) hooksFile(rel string) error {
	var doc struct {
		Hooks json.RawMessage `json:"hooks"`
	}
	if err := readJSON(c.dir, rel, &doc); err != nil {
		return err
	}
	if doc.Hooks == nil {
		return c.fail("hooks", fmt.Sprintf("%s has no \"hooks\" object", safeName(rel)),
			`a hooks file wraps its event map: {"hooks": {"PreToolUse": [...]}}`)
	}
	_, err := hookEvents(claudeManifest+": hooks", doc.Hooks)
	return err
}
