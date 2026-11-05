package module

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/netip"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// AgentPluginsSchema is the $schema of an Agent Plugins 1.0.0 manifest.
const AgentPluginsSchema = "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json"

// Agent Plugins 1.0.0 file names and identifiers (agent-plugins.org specification).
const (
	apManifest  = "plugin.json"
	apMCPSchema = "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json"
	apMCP       = "mcp.json"
	lolaNS      = "dev.getlola"
)

var (
	// apNameRE is the manifest name pattern without its lookahead, which apName checks.
	apNameRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`)
	// skillNameRE is agentskills.io's skill name rule.
	skillNameRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	// headerNameRE is an HTTP field name (RFC 9110 token).
	headerNameRE = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
)

// apNamespaces maps the client namespaces lola renders to their targets (Plan 5b design §6.3),
// in the order they are read; dev.getlola, last, reaches every target.
var apNamespaces = []apNamespace{
	{"com.anthropic.claude", []string{"claude-code"}},
	{"com.anthropic.claude-code", []string{"claude-code"}},
	{"ai.opencode", []string{"opencode"}},
	{"com.google.gemini-cli", []string{"gemini-cli"}},
	{lolaNS, nil},
}

// apNamespace is a client namespace and the targets its artifacts render to.
type apNamespace struct {
	id      string
	targets []string
}

// agentPlugins adapts an Agent Plugins 1.0.0 package (Plan 5b design §6).
type agentPlugins struct{}

func (agentPlugins) manifest() string { return apManifest }

func (agentPlugins) read(dir string) (*lolaFile, []string, bool, error) {
	var doc json.RawMessage
	if err := readJSON(dir, apManifest, &doc); err != nil {
		return nil, nil, false, err
	}
	var raw map[string]json.RawMessage
	var schema string
	if json.Unmarshal(doc, &raw) != nil || raw == nil || json.Unmarshal(raw["$schema"], &schema) != nil || schema != AgentPluginsSchema {
		return nil, []string{fmt.Sprintf("%s is not an Agent Plugins 1.0.0 manifest (its $schema is not %s); it was ignored", apManifest, AgentPluginsSchema)}, false, nil
	}
	a := &apReader{dir: dir, raw: raw}
	lf, err := a.convert()
	return lf, a.warnings, err == nil, err
}

// apReader converts one package.
type apReader struct {
	dir      string
	raw      map[string]json.RawMessage
	warnings []string
}

func apFail(file, field, msg, hint string) error {
	return errs.WithHints(fmt.Errorf("%s: %s: %s", file, field, msg), hint)
}

// ioFail reports a read error at rel, without the directory lola staged the package in.
func ioFail(rel string, err error) error {
	return errs.WithHints(fmt.Errorf("%s: %w", safeName(rel), untrusted.CleanErr(err)), "check that the path can be read")
}

// strict decodes b into v, refusing keys v does not have and null anywhere: Go decodes a null
// into a string, list or map as a no-op, but the closed schemas allow no null.
func strict(b json.RawMessage, v any) error {
	nulls := json.NewDecoder(bytes.NewReader(b))
	for {
		tok, err := nulls.Token()
		if err != nil {
			break
		}
		if tok == nil {
			return errors.New("null is not allowed")
		}
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func (a *apReader) convert() (*lolaFile, error) {
	known := []string{"$schema", "name", "version", "description", "author", "homepage", "repository", "license", "keywords", "extensions"}
	for _, k := range slices.Sorted(maps.Keys(a.raw)) {
		if !slices.Contains(known, k) {
			return nil, apFail(apManifest, safeName(k), "is not an Agent Plugins manifest field", "the manifest takes "+strings.Join(known, ", "))
		}
	}
	lf := &lolaFile{Format: FormatAgentPlugins}
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
		if b, ok := a.raw[f.key]; ok && strict(b, f.v) != nil {
			return nil, apFail(apManifest, f.key, "must be "+f.want, "fix the value in plugin.json; the Agent Plugins schema fixes the type of every field")
		}
	}
	lf.Author = author
	if err := apName(lf.Name); err != nil {
		return nil, err
	}
	exts, err := a.extensions()
	if err != nil {
		return nil, err
	}
	skills, err := a.skills()
	if err != nil {
		return nil, err
	}
	mcp, err := a.mcp()
	if err != nil {
		return nil, err
	}
	commands, agents, rules, err := a.namespaces(exts, lf.Name)
	if err != nil {
		return nil, err
	}
	if len(skills)+len(commands)+len(agents)+len(mcp)+len(rules) == 0 {
		hints := []string{"lola installs skills, commands, agents, MCP servers and instructions; add one under the default directories or declare it in extensions"}
		for _, w := range a.warnings {
			hints = append(hints, "also noted: "+w)
		}
		return nil, errs.WithHints(fmt.Errorf("%s declares nothing lola can install", apManifest), hints...)
	}
	lf.Components = &components{Skills: &skills, Commands: &commands, Agents: &agents, MCP: &mcp, Rules: &rules,
		RuntimeHooks: &[]hooksEntry{}}
	if kept, ok, err := a.keptExtensions(); err != nil {
		return nil, err
	} else if ok {
		lf.Formats = map[string]any{FormatAgentPlugins: map[string]any{"extensions": kept}}
	}
	return lf, nil
}

// keptExtensions is the extensions object lola records in the cached manifest. A namespace lola
// does not render is ignored without reading its contents (Agent Plugins §8.1), so one whose
// values lola cannot represent, such as 1e999, is left out; a rendered namespace must convert.
func (a *apReader) keptExtensions() (any, bool, error) {
	b, ok := a.raw["extensions"]
	if !ok {
		return nil, false, nil
	}
	var exts map[string]json.RawMessage
	if json.Unmarshal(b, &exts) != nil || exts == nil {
		v, err := jsonValue(b)
		return v, err == nil, nil
	}
	kept := map[string]any{}
	for ns, raw := range exts {
		v, err := jsonValue(raw)
		switch {
		case err == nil:
			kept[ns] = v
		case slices.ContainsFunc(apNamespaces, func(n apNamespace) bool { return n.id == ns }):
			return nil, false, apFail(apManifest, "extensions."+ns, err.Error(), "write extensions as plain JSON values")
		}
	}
	return kept, true, nil
}

// apName checks the manifest name (§6.1).
func apName(name string) error {
	if name == "" {
		return apFail(apManifest, "name", "is required", "set name in plugin.json")
	}
	if err := ValidatePluginName(name); err != nil {
		return errs.WithHints(fmt.Errorf("%s: name: %w", apManifest, err), errs.Hints(err)...)
	}
	return nil
}

// ValidatePluginName enforces Agent Plugins' name rule (Plan 5b design §6.1), which is stricter
// than ValidateName: no '_', no "--", and a letter or digit at both ends.
func ValidatePluginName(name string) error {
	if len(name) > 64 || !apNameRE.MatchString(name) || strings.Contains(name, "--") || strings.Contains(name, "..") {
		return errs.WithHints(fmt.Errorf("%q is not an Agent Plugins name", untrusted.Text(name, 64+1)),
			"use up to 64 characters from a-z, 0-9, '.' and '-', starting and ending with a letter or digit, with no '--' or '..'")
	}
	return nil
}

// extensions returns the namespace objects. A value that is not an object is reported and
// ignored (Agent Plugins §8.1, Plan 5b design §6.3).
func (a *apReader) extensions() (map[string]map[string]json.RawMessage, error) {
	out := map[string]map[string]json.RawMessage{}
	b, ok := a.raw["extensions"]
	if !ok {
		return out, nil
	}
	var exts map[string]json.RawMessage
	if json.Unmarshal(b, &exts) != nil || exts == nil {
		a.warnings = append(a.warnings, apManifest+": extensions is not an object; it was ignored")
		return out, nil
	}
	for _, ns := range slices.Sorted(maps.Keys(exts)) {
		if !slices.ContainsFunc(apNamespaces, func(n apNamespace) bool { return n.id == ns }) {
			a.warnings = append(a.warnings, fmt.Sprintf("%s: extensions: lola renders nothing for the namespace %s; it was ignored", apManifest, safeName(ns)))
			continue
		}
		var cfg map[string]json.RawMessage
		if json.Unmarshal(exts[ns], &cfg) != nil || cfg == nil {
			a.warnings = append(a.warnings, fmt.Sprintf("%s: extensions.%s is not an object; it was ignored", apManifest, ns))
			continue
		}
		for _, k := range slices.Sorted(maps.Keys(cfg)) {
			if k != "commands" && k != "agents" && k != "instructions" {
				a.warnings = append(a.warnings, fmt.Sprintf("%s: extensions.%s.%s is not a key lola reads (commands, agents, instructions); it was ignored",
					apManifest, ns, safeName(k)))
				delete(cfg, k)
			}
		}
		out[ns] = cfg
	}
	return out, nil
}

// skills discovers the portable skills (§6.2): skills/*/SKILL.md, one level deep. A skill that
// breaks the agentskills.io rules is reported and skipped.
func (a *apReader) skills() ([]pathEntry, error) {
	out := []pathEntry{}
	if !a.isDir("skills") {
		return out, nil
	}
	es, err := os.ReadDir(filepath.Join(a.dir, "skills"))
	if err != nil {
		return nil, ioFail("skills", err)
	}
	for _, e := range es {
		rel := "skills/" + e.Name()
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if e.Type()&fs.ModeSymlink != 0 {
			a.warnings = append(a.warnings, safeName(rel)+" is a symbolic link; skipped")
			continue
		}
		if !e.IsDir() {
			continue
		}
		fi, err := os.Lstat(filepath.Join(a.dir, rel, "SKILL.md"))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			a.warnings = append(a.warnings, safeName(rel)+" has no SKILL.md; skipped")
			continue
		case err != nil:
			a.warnings = append(a.warnings, fmt.Sprintf("%s/SKILL.md cannot be read: %s; skipped", safeName(rel), safeName(untrusted.CleanErr(err).Error())))
			continue
		case fi.Mode()&fs.ModeSymlink != 0:
			a.warnings = append(a.warnings, safeName(rel)+"/SKILL.md is a symbolic link; skipped")
			continue
		case !fi.Mode().IsRegular():
			a.warnings = append(a.warnings, safeName(rel)+"/SKILL.md is not a regular file; skipped")
			continue
		}
		if why := agentSkill(a.dir, rel, e.Name()); why != "" {
			a.warnings = append(a.warnings, fmt.Sprintf("%s/SKILL.md is not a valid Agent Skill: %s; skipped", safeName(rel), why))
			continue
		}
		out = append(out, pathEntry{Path: rel})
	}
	return out, nil
}

// isDir reports whether rel (a fixed location in the package) is a directory. One that exists as
// anything else is reported and treated as absent, so the rest of the package still loads
// (Agent Plugins §6.2).
func (a *apReader) isDir(rel string) bool {
	fi, err := os.Lstat(filepath.Join(a.dir, rel))
	switch {
	case err != nil:
		return false
	case fi.Mode()&fs.ModeSymlink != 0:
		a.warnings = append(a.warnings, safeName(rel)+" is a symbolic link; skipped")
	case !fi.IsDir():
		a.warnings = append(a.warnings, safeName(rel)+" is not a directory; skipped")
	default:
		return true
	}
	return false
}

// agentSkill checks a skill against agentskills.io and says what is wrong, or "".
func agentSkill(dir, rel, dirName string) string {
	b, err := os.ReadFile(filepath.Join(dir, rel, "SKILL.md"))
	if err != nil {
		return "it cannot be read: " + safeName(untrusted.CleanErr(err).Error())
	}
	doc, err := ParseDoc(b)
	if err != nil {
		return "its frontmatter does not parse"
	}
	name, ok := doc.Frontmatter["name"].(string)
	switch {
	case !ok || len(name) > 64 || !skillNameRE.MatchString(name):
		return "name must be 1-64 lowercase letters, digits and single hyphens"
	case name != dirName:
		return fmt.Sprintf("name %q differs from its directory", safeName(name))
	}
	desc, ok := doc.Frontmatter["description"].(string)
	if !ok || desc == "" || utf8.RuneCountInString(desc) > 1024 {
		return "description must be 1-1024 characters"
	}
	if c, set := doc.Frontmatter["compatibility"]; set {
		if s, ok := c.(string); !ok || utf8.RuneCountInString(s) > 500 {
			return "compatibility must be at most 500 characters"
		}
	}
	return ""
}

// mcp reads mcp.json, strictly (§6.2): any rule broken is a load error. Each server becomes an
// inline entry in the IR's shape, so a cached load needs no Agent Plugins rule.
func (a *apReader) mcp() ([]mcpComponent, error) {
	out := []mcpComponent{}
	if _, err := os.Lstat(filepath.Join(a.dir, apMCP)); errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	var doc map[string]json.RawMessage
	if err := readJSON(a.dir, apMCP, &doc); err != nil {
		return nil, err
	}
	var schema string
	switch {
	case doc == nil:
		return nil, errs.WithHints(fmt.Errorf("%s must hold a JSON object", apMCP), "write mcp.json as {\"$schema\": ..., \"mcpServers\": {...}}")
	case len(doc) != 2 || doc["$schema"] == nil || doc["mcpServers"] == nil:
		return nil, errs.WithHints(fmt.Errorf("%s must hold exactly $schema and mcpServers", apMCP), "remove any other top-level field")
	case json.Unmarshal(doc["$schema"], &schema) != nil || schema != apMCPSchema:
		return nil, apFail(apMCP, "$schema", "must be "+apMCPSchema, "set $schema to "+apMCPSchema)
	}
	var servers map[string]json.RawMessage
	if json.Unmarshal(doc["mcpServers"], &servers) != nil || servers == nil {
		return nil, apFail(apMCP, "mcpServers", "must be an object of servers", "list servers as an object keyed by server name under mcpServers")
	}
	for _, name := range slices.Sorted(maps.Keys(servers)) {
		at := "mcpServers." + safeName(name)
		if err := ValidateName("MCP server", name); err != nil {
			return nil, errs.WithHints(fmt.Errorf("%s: %w", apMCP, err), "rename the server")
		}
		var s struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(servers[name], &s); err != nil {
			return nil, apFail(apMCP, at, "must be a server object of type stdio, streamable-http or sse", "give each server an object with type stdio, streamable-http or sse")
		}
		var spec *serverSpec
		var err error
		switch s.Type {
		case "stdio":
			spec, err = a.stdio(at, servers[name])
		case "streamable-http", "sse":
			spec, err = remote(at, servers[name])
		default:
			err = apFail(apMCP, at+".type", fmt.Sprintf("%q is not stdio, streamable-http or sse", safeName(s.Type)), "set type to stdio, streamable-http or sse")
		}
		if err != nil {
			return nil, err
		}
		out = append(out, mcpComponent{Name: name, Server: spec})
	}
	return out, nil
}

// stdio checks a stdio server and converts it. lola provides PLUGIN_ROOT and PLUGIN_DATA to the
// process through its env, as the Agent Plugins host must (§6.2).
func (a *apReader) stdio(at string, b json.RawMessage) (*serverSpec, error) {
	var s struct {
		Type    string            `json:"type"`
		Command *string           `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
		Cwd     *string           `json:"cwd"`
	}
	if err := strict(b, &s); err != nil {
		return nil, apFail(apMCP, at, "a stdio server takes only type, command, args, env and cwd, as strings (args a list, env an object)", "remove the other fields, or use the type that takes them")
	}
	if s.Command == nil {
		return nil, apFail(apMCP, at+".command", "is required", "name the executable in command")
	}
	cmd := *s.Command
	rest, relative := strings.CutPrefix(cmd, "./")
	switch {
	case strings.ContainsFunc(cmd, isControl):
		return nil, apFail(apMCP, at+".command", "must not contain control characters", "write the executable's name only")
	case cmd == "" || strings.ContainsFunc(cmd, func(r rune) bool { return r == ' ' }):
		return nil, apFail(apMCP, at+".command", "must be one executable token", "put arguments in args")
	case strings.Contains(cmd, "${"):
		return nil, apFail(apMCP, at+".command", "takes no placeholders", "use a ./ path for an executable in the package")
	case relative && (rest == "" || !filepath.IsLocal(rest)):
		return nil, apFail(apMCP, at+".command", fmt.Sprintf("%q leaves the package", safeName(cmd)), "keep ./ paths inside the package")
	case relative:
		cmd = "${PLUGIN_ROOT}/" + rest
	case cmd == "." || cmd == "..":
		return nil, apFail(apMCP, at+".command", fmt.Sprintf("%q is not an executable name", cmd), "write a bare executable name such as npx, or ./bin/tool for a file in the package")
	case strings.ContainsAny(cmd, `/\`):
		return nil, apFail(apMCP, at+".command", fmt.Sprintf("%q is neither a bare executable name nor a ./ path", safeName(cmd)), "write a bare name such as npx, or ./bin/tool for a file in the package")
	}
	env := map[string]string{}
	for k, v := range s.Env {
		if k == "PLUGIN_ROOT" || k == "PLUGIN_DATA" {
			return nil, apFail(apMCP, at+".env", k+" is reserved: the host sets it", "remove it from env: lola provides it to the process")
		}
		env[k] = v
	}
	for _, arg := range s.Args {
		a.literals(at+".args", arg)
	}
	for _, k := range slices.Sorted(maps.Keys(env)) {
		a.literals(at+".env."+safeName(k), env[k])
	}
	if s.Cwd != nil {
		a.literals(at+".cwd", *s.Cwd)
	}
	env["PLUGIN_ROOT"], env["PLUGIN_DATA"] = "${PLUGIN_ROOT}", "${PLUGIN_DATA}"
	out := &serverSpec{Type: TransportStdio, Command: cmd, Args: s.Args, Env: env}
	if s.Cwd != nil {
		cwd, err := apCwd(at, *s.Cwd)
		if err != nil {
			return nil, err
		}
		out.Cwd = cwd
	}
	return out, nil
}

// placeholderRE finds ${NAME} placeholders.
var placeholderRE = regexp.MustCompile(`\$\{([^}]*)\}`)

// literals warns about each placeholder in v other than ${PLUGIN_ROOT} and ${PLUGIN_DATA}:
// Agent Plugins keeps it as literal text (§9.2), but the assistants expand ${NAME} in MCP
// server settings, so it would not reach the server as written.
func (a *apReader) literals(field, v string) {
	for _, m := range placeholderRE.FindAllStringSubmatch(v, -1) {
		if m[1] != "PLUGIN_ROOT" && m[1] != "PLUGIN_DATA" {
			a.warnings = append(a.warnings, fmt.Sprintf("%s: %s: %s is literal text under Agent Plugins, but the assistants expand it",
				apMCP, field, safeName(m[0])))
		}
	}
}

// apCwd checks a stdio cwd (§6.2): ./path, ${PLUGIN_ROOT}[/path] or ${PLUGIN_DATA}[/path], with
// the path staying inside. A ./ path becomes ${PLUGIN_ROOT}/path.
func apCwd(at, cwd string) (string, error) {
	base, rest := "", ""
	switch {
	case strings.HasPrefix(cwd, "./"):
		base, rest = "${PLUGIN_ROOT}", strings.TrimPrefix(cwd, "./")
	case cwd == "${PLUGIN_ROOT}" || cwd == "${PLUGIN_DATA}":
		return cwd, nil
	case strings.HasPrefix(cwd, "${PLUGIN_ROOT}/"), strings.HasPrefix(cwd, "${PLUGIN_DATA}/"):
		base, rest, _ = strings.Cut(cwd, "/")
	default:
		return "", apFail(apMCP, at+".cwd", fmt.Sprintf("%q must start with ./, ${PLUGIN_ROOT} or ${PLUGIN_DATA}", safeName(cwd)), "start cwd with ./, ${PLUGIN_ROOT} or ${PLUGIN_DATA}")
	}
	if rest = strings.TrimSuffix(rest, "/"); rest == "" {
		return base, nil
	}
	if !filepath.IsLocal(rest) || strings.Contains(rest, "${PLUGIN_ROOT}") || strings.Contains(rest, "${PLUGIN_DATA}") {
		return "", apFail(apMCP, at+".cwd", fmt.Sprintf("%q leaves the directory it starts in", safeName(cwd)), "keep cwd inside the directory it starts in, without ..")
	}
	return base + "/" + path.Clean(rest), nil
}

// remote checks a streamable-http or sse server (§6.2) and converts it: streamable-http is the
// IR's http.
func remote(at string, b json.RawMessage) (*serverSpec, error) {
	var s struct {
		Type    string            `json:"type"`
		URL     *string           `json:"url"`
		Headers map[string]string `json:"headers"`
	}
	if err := strict(b, &s); err != nil {
		return nil, apFail(apMCP, at, "a remote server takes only type, url and headers, as strings (headers an object)", "remove the other fields, or use the type that takes them")
	}
	if s.URL == nil {
		return nil, apFail(apMCP, at+".url", "is required", "set url to the server address")
	}
	u, err := url.Parse(*s.URL)
	shown := "the url"
	if err == nil {
		shown = safeName(u.Scheme + "://" + u.Host + u.EscapedPath())
	}
	switch {
	case err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.Hostname() == "":
		return nil, apFail(apMCP, at+".url", "must be an absolute http or https URL", "write the url as https://host/path")
	case u.User != nil || u.Fragment != "" || strings.Contains(*s.URL, "#"):
		return nil, apFail(apMCP, at+".url", shown+" must not carry user information or a fragment", "Agent Plugins forbids secrets in package data")
	case u.Scheme == "http" && !loopback(u.Hostname()):
		return nil, apFail(apMCP, at+".url", shown+" must use https: plain http is allowed only for a loopback host", "use https, or a loopback host such as localhost for local development")
	case strings.Contains(*s.URL, "${"):
		return nil, apFail(apMCP, at+".url", "takes no placeholders", "write the url without ${...}")
	}
	for _, k := range slices.Sorted(maps.Keys(s.Headers)) {
		v := s.Headers[k]
		switch {
		case !headerNameRE.MatchString(k):
			return nil, apFail(apMCP, at+".headers", fmt.Sprintf("%q is not a valid header name", safeName(k)), "use an RFC 9110 header name: letters, digits and !#$%&'*+-.^_`|~")
		case strings.ContainsFunc(v, func(r rune) bool { return isControl(r) && r != '\t' }):
			return nil, apFail(apMCP, at+".headers."+safeName(k), "is not a valid header value", "remove the line break and control characters from the value")
		case strings.Contains(v, "${"):
			return nil, apFail(apMCP, at+".headers."+safeName(k), "takes no placeholders", "write the value without ${...}; Agent Plugins packages carry no secrets")
		}
	}
	t := TransportSSE
	if s.Type == "streamable-http" {
		t = TransportHTTP
	}
	return &serverSpec{Type: t, URL: *s.URL, Headers: s.Headers}, nil
}

// isControl reports an ASCII control character, DEL included.
func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

// loopback reports whether host is localhost or a loopback IP literal; names are never resolved.
func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

// nsEntry is one artifact a namespace provides.
type nsEntry struct {
	ns, kind, name, path string
	targets              []string
}

// namespaces reads each client namespace's commands, agents and instructions (ADR #237;
// Plan 5b design §6.3), routes them to the namespace's targets, and resolves collisions.
func (a *apReader) namespaces(exts map[string]map[string]json.RawMessage, module string) ([]commandEntry, []pathEntry, []pathEntry, error) {
	var all []nsEntry
	for _, n := range apNamespaces {
		cfg := exts[n.id]
		for _, kind := range []string{"commands", "agents"} {
			dir, err := a.nsPath(n.id, kind, cfg[kind], n.id+"/"+kind, true)
			if err != nil {
				return nil, nil, nil, err
			}
			if dir == "" {
				continue
			}
			es, err := os.ReadDir(filepath.Join(a.dir, dir))
			if err != nil {
				return nil, nil, nil, ioFail(dir, err)
			}
			for _, e := range es {
				name, ok := strings.CutSuffix(e.Name(), ".md")
				if !ok || !e.Type().IsRegular() {
					continue
				}
				single := strings.TrimSuffix(kind, "s")
				if err := ValidateName(single, name); err != nil {
					return nil, nil, nil, errs.WithHints(fmt.Errorf("%s/%s: %w", safeName(dir), safeName(e.Name()), err), "rename the file")
				}
				all = append(all, nsEntry{n.id, single, name, path.Join(dir, e.Name()), n.targets})
			}
		}
		if b, ok := cfg["instructions"]; ok {
			file, err := a.nsPath(n.id, "instructions", b, "", false)
			if err != nil {
				return nil, nil, nil, err
			}
			all = append(all, nsEntry{n.id, "rule", module, file, n.targets})
		}
	}
	kept, err := a.resolve(all)
	if err != nil {
		return nil, nil, nil, err
	}
	commands, agents, rules := []commandEntry{}, []pathEntry{}, []pathEntry{}
	for _, e := range kept {
		switch e.kind {
		case "command":
			commands = append(commands, commandEntry{Path: e.path, Targets: e.targets})
		case "agent":
			agents = append(agents, pathEntry{Path: e.path, Targets: e.targets})
		default:
			rules = append(rules, pathEntry{Path: e.path, Name: e.name, Targets: e.targets})
		}
	}
	return commands, agents, rules, nil
}

// nsPath resolves extensions."<ns>".<key>: a ./ path in the package to a directory (dir) or a
// file. Without the key it is def when that directory exists, else "".
func (a *apReader) nsPath(ns, key string, raw json.RawMessage, def string, dir bool) (string, error) {
	field := "extensions." + ns + "." + key
	if raw == nil {
		if def != "" && a.isDir(def) {
			return def, nil
		}
		return "", nil
	}
	var p string
	if json.Unmarshal(raw, &p) != nil {
		return "", apFail(apManifest, field, "must be a ./ path", "write the path as a string, ./path")
	}
	rest, ok := strings.CutPrefix(p, "./")
	if !ok || rest == "" {
		return "", apFail(apManifest, field, fmt.Sprintf("path %q must start with ./", safeName(p)), "paths in a namespace are relative to the package root, written ./path")
	}
	rel, isDir, err := localPath(a.dir, apManifest+": "+field, rest)
	switch {
	case err != nil:
		return "", err
	case dir && !isDir:
		return "", apFail(apManifest, field, fmt.Sprintf("path %q is not a directory", safeName(p)), "point it at a directory in the package")
	case !dir && isDir:
		return "", apFail(apManifest, field, fmt.Sprintf("path %q is not a file", safeName(p)), "point it at a file in the package")
	case !dir && !strings.HasSuffix(rel, ".md"):
		return "", apFail(apManifest, field, fmt.Sprintf("path %q is not a .md file", safeName(p)), "point it at a markdown file")
	}
	return rel, nil
}

// resolve applies the collision rules (§6.3) to the namespace artifacts: dev.getlola wins over a
// client namespace, with a warning; two client namespaces that reach one target with one name
// are an error.
func (a *apReader) resolve(all []nsEntry) ([]nsEntry, error) {
	var out []nsEntry
	for i, e := range all {
		same := func(o nsEntry) bool { return o.kind == e.kind && o.name == e.name }
		if e.ns != lolaNS && slices.ContainsFunc(all, func(o nsEntry) bool { return same(o) && o.ns == lolaNS }) {
			a.warnings = append(a.warnings, fmt.Sprintf("%s %q from %s takes precedence over the one from %s on %s",
				e.kind, e.name, lolaNS, e.ns, strings.Join(e.targets, ", ")))
			continue
		}
		for _, o := range all[i+1:] {
			if same(o) && o.ns != e.ns && overlap(o.targets, e.targets) {
				return nil, errs.WithHints(fmt.Errorf("%s: %s and %s both provide %s %q for %s", apManifest, e.ns, o.ns, e.kind, e.name,
					strings.Join(e.targets, ", ")), "keep it in one of the namespaces, or move it to "+lolaNS)
			}
		}
		out = append(out, e)
	}
	return out, nil
}
