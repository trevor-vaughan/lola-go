package module

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/fsx"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// components is the lola.yml components section (Plan 5b design §3). Listing a kind replaces
// discovery of that kind, so a kind listed with no entries has none; the pointers keep a listed
// empty kind apart from an unlisted one.
type components struct {
	Skills       *[]pathEntry    `yaml:"skills,omitempty"`
	Commands     *[]commandEntry `yaml:"commands,omitempty"`
	Agents       *[]pathEntry    `yaml:"agents,omitempty"`
	MCP          *[]mcpComponent `yaml:"mcp,omitempty"`
	Rules        *[]pathEntry    `yaml:"rules,omitempty"`
	RuntimeHooks *[]hooksEntry   `yaml:"runtime_hooks,omitempty"`
}

// pathEntry is a skill directory, or an agent or rule file.
type pathEntry struct {
	Path    string   `yaml:"path"`
	Name    string   `yaml:"name,omitempty"`
	Targets []string `yaml:"targets,omitempty"`
}

// commandEntry is a command file, or an inline command whose body is Content. Frontmatter
// sets keys on top of the file's own.
type commandEntry struct {
	Path        string         `yaml:"path,omitempty"`
	Name        string         `yaml:"name,omitempty"`
	Content     string         `yaml:"content,omitempty"`
	Frontmatter map[string]any `yaml:"frontmatter,omitempty"`
	Targets     []string       `yaml:"targets,omitempty"`
}

// mcpComponent is a file in Claude's .mcp.json shape, or one inline server.
type mcpComponent struct {
	Path    string      `yaml:"path,omitempty"`
	Name    string      `yaml:"name,omitempty"`
	Server  *serverSpec `yaml:"server,omitempty"`
	Targets []string    `yaml:"targets,omitempty"`
}

// serverSpec is an inline MCP server in the IR's shape; Type is a transport (stdio, http, sse).
type serverSpec struct {
	Type    string            `yaml:"type"`
	Command string            `yaml:"command,omitempty"`
	Args    []string          `yaml:"args,omitempty"`
	Env     map[string]string `yaml:"env,omitempty"`
	Cwd     string            `yaml:"cwd,omitempty"`
	URL     string            `yaml:"url,omitempty"`
	Headers map[string]string `yaml:"headers,omitempty"`
}

// hooksEntry is a Claude hooks file ({"hooks": {...}}), or an inline event map.
type hooksEntry struct {
	Path    string         `yaml:"path,omitempty"`
	Hooks   map[string]any `yaml:"hooks,omitempty"`
	Targets []string       `yaml:"targets,omitempty"`
}

// knownKeys lists the keys of each lola.yml mapping, by the Go type yaml names in an unknown
// field error, for the hint.
var knownKeys = map[string]string{
	"module.lolaFile":     lolaKeys,
	"module.Author":       "name, email, url",
	"module.components":   "skills, commands, agents, mcp, rules, runtime_hooks",
	"module.pathEntry":    "path, name, targets",
	"module.commandEntry": "path, name, content, frontmatter, targets",
	"module.mcpComponent": "path, name, server, targets",
	"module.serverSpec":   "type, command, args, env, cwd, url, headers",
	"module.hooksEntry":   "path, hooks, targets",
}

// loader builds a module's artifacts from its components section.
type loader struct {
	dir   string
	m     *Module
	known []string // the target ids a per-entry targets list may name
}

// claim is one artifact of a kind, for the duplicate check (§3.2).
type claim struct {
	name, source string
	targets      []string
}

func entryAt(kind string, i int) string { return fmt.Sprintf("components.%s[%d]", kind, i) }

func safeName(s string) string { return untrusted.Text(s, untrusted.MaxName) }

// entryErr is an error about the entry at, with hints.
func entryErr(at, msg string, hints ...string) error {
	return errs.WithHints(fmt.Errorf("%s: %s", at, msg), hints...)
}

// path checks an entry's path (§3.1) with localPath. dir says whether it must name a directory
// or a regular file; suffix, when set, is the file name ending it needs.
func (l loader) path(at, rel string, dir bool, suffix string) (string, error) {
	clean, isDir, err := localPath(l.dir, at, rel)
	switch {
	case err != nil:
		return "", err
	case dir && !isDir:
		return "", entryErr(at, fmt.Sprintf("path %q is not a directory", safeName(rel)),
			"a skill entry names a directory that holds SKILL.md")
	case !dir && isDir:
		return "", entryErr(at, fmt.Sprintf("path %q is not a regular file", safeName(rel)),
			"name a plain file, not a directory or the module root")
	case suffix != "" && !strings.HasSuffix(clean, suffix):
		return "", entryErr(at, fmt.Sprintf("path %q does not end in %s", safeName(rel), suffix),
			"name a file that ends in "+suffix)
	}
	return clean, nil
}

// localPath checks a path in a module: relative and inside it, existing, with no symbolic link
// in any element, and naming a directory or a regular file. It returns the path cleaned, with
// '/', and whether it is a directory. Errors start with at.
func localPath(dir, at, rel string) (string, bool, error) {
	clean := filepath.ToSlash(filepath.Clean(rel))
	if rel == "" || !filepath.IsLocal(rel) {
		return "", false, entryErr(at, fmt.Sprintf("path %q must be relative and inside the module", safeName(rel)),
			"give a path from the module root, such as skills/review")
	}
	if clean == "." {
		return clean, true, nil
	}
	cur, parts := dir, strings.Split(clean, "/")
	var fi fs.FileInfo
	for i, p := range parts {
		cur = filepath.Join(cur, p)
		var err error
		fi, err = os.Lstat(cur)
		switch {
		case errors.Is(err, fs.ErrNotExist), err == nil && i < len(parts)-1 && !fi.IsDir() && fi.Mode()&fs.ModeSymlink == 0:
			return "", false, entryErr(at, fmt.Sprintf("path %q does not exist", safeName(rel)), "fix the path, or remove the entry")
		case err != nil:
			return "", false, entryErr(at, fmt.Sprintf("path %q: %v", safeName(rel), untrusted.CleanErr(err)),
				"check that the path can be read")
		case fi.Mode()&fs.ModeSymlink != 0:
			return "", false, entryErr(at, fmt.Sprintf("path %q goes through the symbolic link %s", safeName(rel), safeName(strings.Join(parts[:i+1], "/"))),
				"lola never follows links in a module; name the real file or directory")
		}
	}
	if !fi.IsDir() && !fi.Mode().IsRegular() {
		return "", false, entryErr(at, fmt.Sprintf("path %q is neither a file nor a directory", safeName(rel)),
			"name a plain file or a directory, not a pipe or device")
	}
	return clean, fi.IsDir(), nil
}

// name is an entry's artifact name (§3.1): its name, or the one its path gives.
func (l loader) name(at, kind, name, derived string) (string, error) {
	if name == "" {
		name = derived
	}
	if err := ValidateName(kind, name); err != nil {
		return "", errs.WithHints(fmt.Errorf("%s: %w", at, err), "set name: on the entry")
	}
	return name, nil
}

// targets checks a per-entry targets list.
func (l loader) targets(at string, ts []string) error {
	if ts != nil && len(ts) == 0 {
		return entryErr(at, "targets: lists no target", "name the targets the entry is for, or remove targets: to use the module's")
	}
	for _, t := range ts {
		if !slices.Contains(l.known, t) {
			return entryErr(at, fmt.Sprintf("unknown target %q", safeName(t)), "valid targets: "+strings.Join(l.known, ", "))
		}
	}
	return nil
}

// unique refuses two artifacts of kind with one name whose effective targets overlap (§3.2).
func (l loader) unique(kind string, cs []claim) error {
	eff := func(c claim) []string {
		if len(c.targets) > 0 {
			return c.targets
		}
		return l.m.Targets
	}
	for i := range cs {
		for j := i + 1; j < len(cs); j++ {
			if cs[i].name != cs[j].name || !overlap(eff(cs[i]), eff(cs[j])) {
				continue
			}
			return errs.WithHints(fmt.Errorf("components: two %ss are named %q: %s and %s", kind, cs[i].name, cs[i].source, cs[j].source),
				"rename one with name:, or give them targets: that do not overlap")
		}
	}
	return nil
}

// overlap reports whether two effective target lists share a target; empty means every target.
func overlap(a, b []string) bool {
	return len(a) == 0 || len(b) == 0 || slices.ContainsFunc(a, func(t string) bool { return slices.Contains(b, t) })
}

func (l loader) skills(es []pathEntry) error {
	var cs []claim
	for i, e := range es {
		at := entryAt("skills", i)
		rel, err := l.path(at, e.Path, true, "")
		if err != nil {
			return err
		}
		derived := path.Base(rel)
		if rel == "." {
			derived = l.m.Name
		}
		name, err := l.name(at, "skill", e.Name, derived)
		if err != nil {
			return err
		}
		if err := l.targets(at, e.Targets); err != nil {
			return err
		}
		if fi, err := os.Lstat(filepath.Join(l.dir, rel, "SKILL.md")); err != nil || !fi.Mode().IsRegular() {
			return entryErr(at, fmt.Sprintf("%s has no SKILL.md", safeName(rel)), "a skill entry names one directory that holds SKILL.md")
		}
		doc, err := readDoc(l.dir, path.Join(rel, "SKILL.md"))
		if err != nil {
			return err
		}
		s := Skill{Name: name, Dir: rel, Doc: doc, Targets: e.Targets}
		if rel == "." {
			s.Exclude = rootSkillExclude()
		}
		l.m.Skills = append(l.m.Skills, s)
		cs = append(cs, claim{name, safeName(rel), e.Targets})
	}
	sort.SliceStable(l.m.Skills, func(i, j int) bool { return l.m.Skills[i].Name < l.m.Skills[j].Name })
	return l.unique("skill", cs)
}

func (l loader) commands(es []commandEntry) error {
	var cs []claim
	for i, e := range es {
		at := entryAt("commands", i)
		var c Command
		var source string
		switch {
		case e.Path != "" && e.Content != "":
			return entryErr(at, "takes path or content, not both", "keep path: for a file, or content: and name: for an inline command")
		case e.Path != "":
			rel, err := l.path(at, e.Path, false, ".md")
			if err != nil {
				return err
			}
			if c.Name, err = l.name(at, "command", e.Name, strings.TrimSuffix(path.Base(rel), ".md")); err != nil {
				return err
			}
			if c.Doc, err = readDoc(l.dir, rel); err != nil {
				return err
			}
			c.File, source = rel, safeName(rel)
		case e.Content != "":
			if e.Name == "" {
				return entryErr(at, "an inline command needs a name", "set name: on the entry")
			}
			var err error
			if c.Name, err = l.name(at, "command", e.Name, ""); err != nil {
				return err
			}
			if c.Doc, err = ParseDoc([]byte(e.Content)); err != nil {
				return entryErr(at, untrusted.Text(err.Error(), untrusted.MaxName), "start the content with a --- frontmatter block that is YAML, or with none")
			}
			source = "inline entry " + at
		default:
			return entryErr(at, "needs a path or content", "give path: for a file, or name: and content: for an inline command")
		}
		doc, err := c.Doc.With(e.Frontmatter)
		if err != nil {
			return entryErr(at, untrusted.Text(err.Error(), untrusted.MaxName))
		}
		if err := l.targets(at, e.Targets); err != nil {
			return err
		}
		c.Doc, c.Targets = doc, e.Targets
		l.m.Commands = append(l.m.Commands, c)
		cs = append(cs, claim{c.Name, source, e.Targets})
	}
	sort.SliceStable(l.m.Commands, func(i, j int) bool { return l.m.Commands[i].Name < l.m.Commands[j].Name })
	return l.unique("command", cs)
}

func (l loader) agents(es []pathEntry) error {
	var cs []claim
	for i, e := range es {
		at := entryAt("agents", i)
		rel, err := l.path(at, e.Path, false, ".md")
		if err != nil {
			return err
		}
		name, err := l.name(at, "agent", e.Name, strings.TrimSuffix(path.Base(rel), ".md"))
		if err != nil {
			return err
		}
		if err := l.targets(at, e.Targets); err != nil {
			return err
		}
		doc, err := readDoc(l.dir, rel)
		if err != nil {
			return err
		}
		l.m.Agents = append(l.m.Agents, Agent{Name: name, File: rel, Doc: doc, Targets: e.Targets})
		cs = append(cs, claim{name, safeName(rel), e.Targets})
	}
	sort.SliceStable(l.m.Agents, func(i, j int) bool { return l.m.Agents[i].Name < l.m.Agents[j].Name })
	return l.unique("agent", cs)
}

func (l loader) rules(es []pathEntry) error {
	var cs []claim
	for i, e := range es {
		at := entryAt("rules", i)
		rel, err := l.path(at, e.Path, false, ".md")
		if err != nil {
			return err
		}
		name, err := l.name(at, "rule", e.Name, strings.TrimSuffix(path.Base(rel), ".md"))
		if err != nil {
			return err
		}
		if err := l.targets(at, e.Targets); err != nil {
			return err
		}
		body, err := ruleBody(l.dir, rel)
		if err != nil {
			return err
		}
		l.m.Rules = append(l.m.Rules, Rule{Name: name, Body: body, Targets: e.Targets})
		cs = append(cs, claim{name, safeName(rel), e.Targets})
	}
	sort.SliceStable(l.m.Rules, func(i, j int) bool { return l.m.Rules[i].Name < l.m.Rules[j].Name })
	return l.unique("rule", cs)
}

func (l loader) mcp(es []mcpComponent) error {
	var cs []claim
	for i, e := range es {
		at := entryAt("mcp", i)
		switch {
		case e.Path != "" && (e.Name != "" || e.Server != nil):
			return entryErr(at, "takes a path, or a name and a server, not both", "use path: for a .mcp.json file, or name: and server: for an inline server")
		case e.Path != "":
			rel, err := l.path(at, e.Path, false, "")
			if err != nil {
				return err
			}
			if err := l.targets(at, e.Targets); err != nil {
				return err
			}
			servers, err := mcpFile(l.dir, rel)
			if err != nil {
				return err
			}
			for _, s := range servers {
				s.Targets = e.Targets
				l.m.MCPServers = append(l.m.MCPServers, s)
				cs = append(cs, claim{s.Name, fmt.Sprintf("server %q in %s", s.Name, safeName(rel)), e.Targets})
			}
		case e.Name == "" || e.Server == nil:
			return entryErr(at, "needs a path, or a name and a server", "use path: for a .mcp.json file, or name: and server: for an inline server")
		default:
			name, err := l.name(at, "MCP server", e.Name, "")
			if err != nil {
				return err
			}
			if err := l.targets(at, e.Targets); err != nil {
				return err
			}
			s, err := e.Server.server(at, name)
			if err != nil {
				return err
			}
			s.Targets = e.Targets
			l.m.MCPServers = append(l.m.MCPServers, s)
			cs = append(cs, claim{name, "inline entry " + at, e.Targets})
		}
	}
	sort.SliceStable(l.m.MCPServers, func(i, j int) bool { return l.m.MCPServers[i].Name < l.m.MCPServers[j].Name })
	return l.unique("MCP server", cs)
}

// server converts an inline server to the IR, refusing fields its transport does not have.
func (s serverSpec) server(at, name string) (MCPServer, error) {
	out := MCPServer{Name: name, Transport: s.Type, Command: s.Command, Args: s.Args, Env: s.Env, Cwd: s.Cwd, URL: s.URL, Headers: s.Headers}
	switch s.Type {
	case TransportStdio:
		if s.Command == "" {
			return MCPServer{}, entryErr(at, "a stdio server needs a command", "set command: to the program that serves MCP on stdin and stdout")
		}
		if s.URL != "" || len(s.Headers) > 0 {
			return MCPServer{}, entryErr(at, "a stdio server has no url or headers", "remove them, or use type: http or sse")
		}
	case TransportHTTP, TransportSSE:
		if s.URL == "" {
			return MCPServer{}, entryErr(at, fmt.Sprintf("an %s server needs a url", s.Type), "set url: to the server's address")
		}
		if s.Command != "" || len(s.Args) > 0 || len(s.Env) > 0 || s.Cwd != "" {
			return MCPServer{}, entryErr(at, fmt.Sprintf("an %s server has no command, args, env or cwd", s.Type), "remove them, or use type: stdio")
		}
	default:
		return MCPServer{}, entryErr(at, fmt.Sprintf("server type %q is unknown", safeName(s.Type)), "use stdio, http or sse")
	}
	return out, nil
}

func (l loader) runtimeHooks(es []hooksEntry) error {
	for i, e := range es {
		at := entryAt("runtime_hooks", i)
		var events json.RawMessage
		var source string
		switch {
		case e.Path != "" && e.Hooks != nil:
			return entryErr(at, "takes path or hooks, not both", "use path: for a hooks file, or hooks: for an inline event map")
		case e.Path != "":
			rel, err := l.path(at, e.Path, false, "")
			if err != nil {
				return err
			}
			var doc struct {
				Hooks json.RawMessage `json:"hooks"`
			}
			if err := readJSON(l.dir, rel, &doc); err != nil {
				return err
			}
			if doc.Hooks == nil {
				return entryErr(at, fmt.Sprintf("%s has no \"hooks\" object", safeName(rel)),
					`a hooks file wraps its event map: {"hooks": {"PreToolUse": [...]}}`)
			}
			events, source = doc.Hooks, safeName(rel)
		case e.Hooks != nil:
			b, err := json.Marshal(e.Hooks)
			if err != nil {
				return entryErr(at, "hooks: every key must be a string", "quote keys that look like numbers or booleans")
			}
			events = b
		default:
			return entryErr(at, "needs a path or hooks", "use path: for a hooks file, or hooks: for an inline event map")
		}
		if err := l.targets(at, e.Targets); err != nil {
			return err
		}
		canon, err := hookEvents(at, events)
		if err != nil {
			return err
		}
		l.m.RuntimeHooks = append(l.m.RuntimeHooks, RuntimeHookSet{Source: source, Hooks: canon, Targets: e.Targets})
	}
	return nil
}

// hookEvents checks a Claude hooks event map, {"<Event>": [{matcher group}, …]}, down to the
// handler fields lola rewrites or shows when it asks for consent, and returns it as canonical
// JSON. Other fields, and handler types lola does not know, are kept as authored.
func hookEvents(at string, raw json.RawMessage) (json.RawMessage, error) {
	var events map[string][]map[string]json.RawMessage
	if err := json.Unmarshal(raw, &events); err != nil || events == nil {
		return nil, entryErr(at, "hooks must map each event to a list of matcher groups",
			`for example {"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "./check.sh"}]}]}`)
	}
	if len(events) == 0 {
		return nil, entryErr(at, "hooks lists no event", "name at least one event, or remove the entry")
	}
	for _, event := range slices.Sorted(maps.Keys(events)) {
		if len(events[event]) == 0 {
			return nil, entryErr(at, fmt.Sprintf("hooks event %s lists no matcher group", quoted(event)),
				`add a matcher group, e.g. {"matcher": "Bash", "hooks": [...]}, or remove the event`)
		}
		for i, group := range events[event] {
			where := "/" + strings.NewReplacer("~", "~0", "/", "~1").Replace(event) + "/" + strconv.Itoa(i)
			if err := hookGroup(group); err != nil {
				return nil, entryErr(at, fmt.Sprintf("hooks %s%s", untrusted.Text(where, untrusted.MaxName), err.Error()),
					"see Claude Code's hooks reference for each handler type's fields")
			}
		}
	}
	return fsx.CanonicalJSON(raw)
}

// hookGroup checks one matcher group. Its error starts with the path below the group it is
// about ("" or "/hooks/<i>"), then ": ".
func hookGroup(group map[string]json.RawMessage) error {
	var matcher string
	if raw, ok := group["matcher"]; ok && json.Unmarshal(raw, &matcher) != nil {
		return errors.New(": matcher must be a string")
	}
	var handlers []map[string]json.RawMessage
	if err := json.Unmarshal(group["hooks"], &handlers); err != nil || len(handlers) == 0 ||
		slices.ContainsFunc(handlers, func(h map[string]json.RawMessage) bool { return h == nil }) {
		return errors.New(": hooks must be a list of one or more handlers")
	}
	for i, h := range handlers {
		if msg := hookHandler(h); msg != "" {
			return fmt.Errorf("/hooks/%d: %s", i, msg)
		}
	}
	return nil
}

// hookHandler checks one handler's fields by type and returns what is wrong, or "".
func hookHandler(h map[string]json.RawMessage) string {
	text := func(key string, required bool) string {
		raw, ok := h[key]
		var v *string
		switch {
		case !ok && !required:
			return ""
		case required && (json.Unmarshal(raw, &v) != nil || v == nil || *v == ""):
			return key + " must be a non-empty string"
		case !required && (json.Unmarshal(raw, &v) != nil || v == nil):
			return key + " must be a string"
		}
		return ""
	}
	list := func(key string) string {
		var v []string
		if raw, ok := h[key]; ok && (json.Unmarshal(raw, &v) != nil || v == nil) {
			return key + " must be a list of strings"
		}
		return ""
	}
	msgs := []string{text("type", true), text("if", false)}
	var async *bool
	if raw, ok := h["async"]; ok && (json.Unmarshal(raw, &async) != nil || async == nil) {
		msgs = append(msgs, "async must be true or false")
	}
	var typ string
	if json.Unmarshal(h["type"], &typ) == nil {
		switch typ {
		case "command":
			msgs = append(msgs, text("command", true), list("args"), text("shell", false))
		case "http":
			var headers map[string]string
			if raw, ok := h["headers"]; ok && (json.Unmarshal(raw, &headers) != nil || headers == nil) {
				msgs = append(msgs, "headers must map names to strings")
			}
			msgs = append(msgs, text("url", true), list("allowedEnvVars"))
		case "mcp_tool":
			msgs = append(msgs, text("server", true), text("tool", true))
		case "prompt", "agent":
			msgs = append(msgs, text("prompt", true))
		}
	}
	for _, m := range msgs {
		if m != "" {
			return m
		}
	}
	return ""
}

// mcpFile reads servers from a file in Claude's .mcp.json shape, sorted by name.
func mcpFile(dir, rel string) ([]MCPServer, error) {
	var doc struct {
		MCPServers map[string]mcpEntry `json:"mcpServers"`
	}
	if err := readJSON(dir, rel, &doc); err != nil {
		return nil, err
	}
	var out []MCPServer
	for name, e := range doc.MCPServers {
		s, err := e.server(name)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", safeName(rel), err)
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// server converts one server of Claude's .mcp.json shape to the IR. Every source in that shape
// (mcps.json, a Claude plugin's .mcp.json, the files it lists and its inline servers) comes
// through here, so all refuse a server whose fields do not fit one transport: lola would
// otherwise render one transport and silently drop the other's fields.
func (e mcpEntry) server(name string) (MCPServer, error) {
	if err := ValidateName("MCP server", name); err != nil {
		return MCPServer{}, errs.WithHints(err, "rename the server")
	}
	s := MCPServer{Name: name, Command: e.Command, Args: e.Args, Env: e.Env, Cwd: e.Cwd, URL: e.URL, Headers: e.Headers}
	fail := func(msg, hint string) (MCPServer, error) {
		return MCPServer{}, errs.WithHints(fmt.Errorf("server %q %s", name, msg), hint)
	}
	local := e.Command != "" || len(e.Args) > 0 || len(e.Env) > 0 || e.Cwd != ""
	remote := e.URL != "" || len(e.Headers) > 0
	switch e.Type {
	case "":
		switch {
		case local && remote:
			return fail("mixes stdio fields (command, args, env, cwd) with remote ones (url, headers)",
				"keep command, args, env and cwd for a server lola starts, or url and headers for one it connects to")
		case e.Command != "":
			s.Transport = TransportStdio
		case e.URL != "":
			s.Transport = TransportHTTP
		default:
			return fail("needs a command or a url", "set command to start a local server, or url to connect to a remote one")
		}
	case "stdio":
		s.Transport = TransportStdio
		switch {
		case remote:
			return fail(`has type "stdio", so it takes no url or headers`, `remove them, or set type to "http" or "sse"`)
		case e.Command == "":
			return fail(`has type "stdio" but no command`, "set command to the program that serves MCP on stdin and stdout")
		}
	case "http", "streamable-http", "sse":
		s.Transport = TransportHTTP
		if e.Type == "sse" {
			s.Transport = TransportSSE
		}
		switch {
		case local:
			return fail(fmt.Sprintf("has type %q, so it takes no command, args, env or cwd", e.Type), `remove them, or set type to "stdio"`)
		case e.URL == "":
			return fail(fmt.Sprintf("has type %q but no url", e.Type), "set url to the server's address")
		}
	default:
		return MCPServer{}, errs.WithHints(fmt.Errorf("server %q: unknown transport %q (use stdio, http or sse)", name, safeName(e.Type)),
			`set type to "stdio", "http" or "sse", or remove it`)
	}
	return s, nil
}
