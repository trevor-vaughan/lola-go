package module

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// ErrNoModule means a directory contains no artifact lola can install.
var ErrNoModule = errors.New("no module found")

const lookedFor = "skills/*/SKILL.md, SKILL.md, commands/*.md, agents/*.md, mcps.json or rules/*.md"

// lolaFile is the lola.yml schema (§5.4). Only name is required.
type lolaFile struct {
	Name        string            `yaml:"name"`
	Format      string            `yaml:"format,omitempty"`
	Description string            `yaml:"description,omitempty"`
	Version     string            `yaml:"version,omitempty"`
	Author      *Author           `yaml:"author,omitempty"`
	License     string            `yaml:"license,omitempty"`
	Homepage    string            `yaml:"homepage,omitempty"`
	Repository  string            `yaml:"repository,omitempty"`
	Keywords    []string          `yaml:"keywords,omitempty"`
	Targets     []string          `yaml:"targets,omitempty"`
	Formats     map[string]any    `yaml:"formats,omitempty"`
	Hooks       map[string]string `yaml:"hooks,omitempty"`
	Components  *components       `yaml:"components,omitempty"`
	Vars        *varsNode         `yaml:"vars,omitempty"`
}

// varsNode holds lola.yml's vars: as its YAML node, which ParseVars reads by tag: the mapping is
// never decoded into Go values of the decoder's choosing.
type varsNode struct{ node *yaml.Node }

func (v *varsNode) UnmarshalYAML(n *yaml.Node) error {
	v.node = n
	return nil
}

func (v varsNode) MarshalYAML() (any, error) { return v.node, nil }

// ManifestNames lists the module manifest file names in precedence order (ADR #243): the
// first one present is read and the others are ignored with a warning.
func ManifestNames() []string { return []string{"lola.yaml", "lola.yml", ".lola.yaml", ".lola.yml"} }

// ShadowWarning says that a lower-precedence manifest was ignored.
func ShadowWarning(winner, shadowed string) string {
	return shadowed + " was ignored: " + winner + " takes precedence"
}

const lolaKeys = "name, format, description, version, author, license, homepage, repository, keywords, targets, formats, hooks, components, vars"

var unknownFieldRE = regexp.MustCompile(`field (\S+) not found in type (\S+)`)

// Formats whose adapter can write a manifest (Plan 5b design §3.3).
const (
	FormatClaudePlugin = "claude-plugin"
	FormatAgentPlugins = "agent-plugins"
)

// rootSkillExclude lists module-level entries that are never part of a root-level skill,
// including the plugin manifests a format adapter read, the partials, the root skill's attribute
// files, and the Markdown files
// lola reserves there (rootDocs) and their .liquid forms, which are not templates
// (configTemplates).
func rootSkillExclude() []string {
	out := append(ManifestNames(), "SKILL.md", skillTemplate, "skills", "commands", "agents", "mcps.json", "rules",
		".claude-plugin", "plugin.json", ".mcp.json", "mcp.json", partialsDir,
		"SKILL"+attrsYAML, "SKILL"+attrsJSONC)
	for _, n := range rootDocs() {
		out = append(out, n, n+".liquid")
	}
	return out
}

// rootDocs lists the Markdown files at a module's root that are never part of its root skill.
func rootDocs() []string { return []string{"INSTRUCTIONS.md", "AGENTS.md"} }

// Load reads the module rooted at dir from its lola manifest or, without one, its native
// layout; it never reads a plugin format's manifest. The cache loads its versions this way, so
// a cached module's meaning cannot change with lola's format adapters. known lists the target
// ids that a components entry's targets may name.
func Load(dir, fallbackName string, known []string) (*Module, error) {
	return detect(dir, fallbackName, known, nil)
}

// build finishes a module whose manifest, if any, has been applied to m: it checks the
// module and loads its artifacts.
func build(dir string, m *Module, known []string) (*Module, error) {
	if err := ValidateName("module", m.Name); err != nil {
		return nil, errs.WithHints(err, "add a lola.yml with a valid name: field")
	}
	if err := checkHooks(dir, m); err != nil {
		return nil, err
	}
	if err := configTemplates(dir, m); err != nil {
		return nil, err
	}
	if err := loadPartials(dir, m, known); err != nil {
		return nil, err
	}
	l, c := loader{dir: dir, m: m, known: known}, m.components
	if c == nil {
		c = &components{}
	}
	steps := []func() error{
		listed(c.Skills, l.skills, func() error { return loadSkills(dir, m) }),
		listed(c.Commands, l.commands, func() error { return loadCommands(dir, m) }),
		listed(c.Agents, l.agents, func() error { return loadAgents(dir, m) }),
		listed(c.MCP, l.mcp, func() error { return loadMCP(dir, m) }),
		listed(c.Rules, l.rules, func() error { return loadRules(dir, m) }),
		listed(c.RuntimeHooks, l.runtimeHooks, func() error { return nil }), // never discovered
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return nil, err
		}
	}
	if err := loadAttributes(dir, m, known); err != nil {
		return nil, err
	}
	if err := frontmatterHooks(m); err != nil {
		return nil, err
	}
	if err := agentServers(m); err != nil {
		return nil, err
	}
	if len(m.Skills)+len(m.Commands)+len(m.Agents)+len(m.MCPServers)+len(m.Rules)+len(m.RuntimeHooks) == 0 {
		return nil, fmt.Errorf("%s: %w: looked for %s", dir, ErrNoModule, lookedFor)
	}
	return m, nil
}

// frontmatterHooks checks the hooks Claude Code runs from skill, command and agent frontmatter
// (any key that equals "hooks" ignoring case, which lola treats as hooks) with the checks of
// components.runtime_hooks: they reach Claude Code only with consent, which shows them.
func frontmatterHooks(m *Module) error {
	check := func(kind, name string, doc Doc) error {
		for _, k := range slices.Sorted(maps.Keys(doc.Frontmatter)) {
			if !strings.EqualFold(k, "hooks") {
				continue
			}
			at := fmt.Sprintf("%s %s: frontmatter %s", kind, name, safeName(k))
			b, err := json.Marshal(doc.Frontmatter[k])
			if err != nil {
				return entryErr(at, "every key must be a string", "quote keys that look like numbers or booleans")
			}
			if _, err := hookEvents(at, b); err != nil {
				return err
			}
		}
		return nil
	}
	for _, s := range m.Skills {
		if err := check("skill", s.Name, s.Doc); err != nil {
			return err
		}
	}
	for _, c := range m.Commands {
		if err := check("command", c.Name, c.Doc); err != nil {
			return err
		}
	}
	for _, a := range m.Agents {
		if err := check("agent", a.Name, a.Doc); err != nil {
			return err
		}
	}
	return nil
}

// serversExample is the coaching for an agent's mcpServers list.
const serversExample = "write each server as a list item: - github names a configured server, - pw: {command: npx} defines one"

// agentServers checks the MCP servers in agent frontmatter (any key that equals "mcpServers"
// ignoring case): a list whose items name a server the user configured, or define one server
// that Claude Code starts with the agent. A definition gets the checks of mcps.json
// (mcpEntry.server); it reaches Claude Code only with consent, which shows it.
func agentServers(m *Module) error {
	for _, a := range m.Agents {
		defined, named := map[string]bool{}, map[string]bool{}
		for _, k := range slices.Sorted(maps.Keys(a.Doc.Frontmatter)) {
			if !strings.EqualFold(k, "mcpServers") {
				continue
			}
			at := fmt.Sprintf("agent %s: frontmatter %s", a.Name, safeName(k))
			b, err := json.Marshal(a.Doc.Frontmatter[k])
			if err != nil {
				return entryErr(at, "every key must be a string", "quote keys that look like numbers or booleans")
			}
			var items []json.RawMessage
			if err := json.Unmarshal(b, &items); err != nil || items == nil {
				return entryErr(at, "must be a list of server names and inline servers", serversExample)
			}
			for i, item := range items {
				at := fmt.Sprintf("%s[%d]", at, i)
				name, inline, err := agentServer(at, item)
				switch {
				case err != nil:
					return err
				case inline && defined[name]:
					return entryErr(at, fmt.Sprintf("server %q is defined more than once", name), "rename one of the servers")
				case inline && named[name], !inline && defined[name]:
					return entryErr(at, fmt.Sprintf("server %q is both defined here and named as a configured server", safeName(name)),
						"rename the inline server, or remove the name")
				case inline:
					defined[name] = true
				default:
					named[name] = true
				}
			}
		}
	}
	return nil
}

// agentServer checks one item of an agent's mcpServers list, at the position at, and returns
// the server it names or defines; inline is true for a definition, whose name is valid.
func agentServer(at string, item json.RawMessage) (name string, inline bool, err error) {
	if string(item) == "null" {
		return "", false, entryErr(at, "must be a server name or one server's definition", serversExample)
	}
	if json.Unmarshal(item, &name) == nil {
		if strings.TrimSpace(name) == "" {
			return "", false, entryErr(at, "a server name must not be empty", "name a configured MCP server, or remove the item")
		}
		return name, false, nil
	}
	var def map[string]json.RawMessage
	if err := json.Unmarshal(item, &def); err != nil || def == nil {
		return "", false, entryErr(at, "must be a server name or one server's definition", serversExample)
	}
	if len(def) != 1 {
		return "", false, entryErr(at, "an inline server must map exactly one server name to its definition", "write each server as its own list item")
	}
	name = slices.Collect(maps.Keys(def))[0]
	raw := def[name]
	if err := ValidateName("MCP server", name); err != nil {
		return "", false, entryErr(at, err.Error(), "rename the server")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return "", false, entryErr(at, fmt.Sprintf("server %q must be a mapping of its fields", name),
			"give it command and args for a server Claude Code starts, or url for one it connects to")
	}
	var e mcpEntry
	if err := json.Unmarshal(raw, &e); err != nil {
		return "", false, entryErr(at, fmt.Sprintf("server %q: type, command, cwd and url must be strings, args a list of strings, and env and headers must map names to strings", name),
			"fix the field's value")
	}
	if _, err := e.server(name); err != nil {
		return "", false, fmt.Errorf("%s: %w", at, err)
	}
	return name, true, nil
}

// listed loads a kind from its components entries when the kind is listed, else by discovery
// (§3.1: listing a kind replaces its discovery).
func listed[E any](entries *[]E, load func([]E) error, discover func() error) func() error {
	if entries == nil {
		return discover
	}
	return func() error { return load(*entries) }
}

// readLolaFile reads the first lola manifest present in dir (ADR #243 precedence), recording
// the others as shadowed. It returns nil when dir has none.
func readLolaFile(dir string, m *Module) (*lolaFile, error) {
	var found []string
	for _, n := range ManifestNames() {
		_, err := os.Stat(filepath.Join(dir, n))
		switch {
		case err == nil:
			found = append(found, n)
		case !errors.Is(err, fs.ErrNotExist):
			return nil, err
		}
	}
	if len(found) == 0 {
		return nil, nil
	}
	m.Manifest, m.Shadowed = found[0], found[1:]
	for _, n := range m.Shadowed {
		m.Warnings = append(m.Warnings, ShadowWarning(m.Manifest, n))
	}
	b, err := os.ReadFile(filepath.Join(dir, found[0]))
	if err != nil {
		return nil, err
	}
	var lf lolaFile
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&lf); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s is empty; it needs at least a name: field", found[0])
		}
		var hints []string
		if mm := unknownFieldRE.FindStringSubmatch(err.Error()); mm != nil && knownKeys[mm[2]] != "" {
			hints = append(hints, fmt.Sprintf("remove or fix %q; known keys there are %s", mm[1], knownKeys[mm[2]]))
		}
		return nil, errs.WithHints(fmt.Errorf("%s: %s", found[0], untrusted.Text(err.Error(), 4*untrusted.MaxName)), hints...)
	}
	if lf.Name == "" {
		return nil, fmt.Errorf("%s: name is required", found[0])
	}
	return &lf, nil
}

// apply sets m from a manifest: a lola.yml, or one an adapter wrote.
func (m *Module) apply(lf *lolaFile) error {
	m.Format = "lola"
	switch lf.Format {
	case "":
	case FormatClaudePlugin, FormatAgentPlugins:
		m.Format = lf.Format
	default:
		return errs.WithHints(fmt.Errorf("%s: format %q is unknown", m.Manifest, untrusted.Text(lf.Format, untrusted.MaxName)),
			"format: records the adapter that wrote the file ("+FormatClaudePlugin+" or "+FormatAgentPlugins+"); remove it from a lola.yml you write")
	}
	m.components = lf.Components
	m.Name, m.Description, m.Version = lf.Name, lf.Description, lf.Version
	m.Author, m.License, m.Homepage, m.Repository = lf.Author, lf.License, lf.Homepage, lf.Repository
	m.Keywords, m.Targets, m.Formats = lf.Keywords, lf.Targets, lf.Formats
	var vars map[string]any
	var err error
	if lf.Vars != nil {
		vars, err = ParseVars(lf.Vars.node, "vars")
	}
	if ve, ok := errors.AsType[*VarError](err); ok {
		return fmt.Errorf("%s:%d: %s", m.Manifest, ve.Line, ve.Reason)
	} else if err != nil {
		return err
	}
	m.Vars = vars
	for event, script := range lf.Hooks {
		m.DeclaredHooks = append(m.DeclaredHooks, Hook{Event: event, Script: script})
	}
	sort.Slice(m.DeclaredHooks, func(i, j int) bool { return m.DeclaredHooks[i].Event < m.DeclaredHooks[j].Event })
	return nil
}

// readDoc parses a content-relative markdown file, naming the file in errors.
func readDoc(dir, rel string) (Doc, error) {
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		return Doc{}, fmt.Errorf("%s: %w", safeName(rel), untrusted.CleanErr(err))
	}
	d, err := ParseDoc(b)
	if err != nil {
		return Doc{}, fmt.Errorf("%s: %w", safeName(rel), err)
	}
	return d, nil
}

// entries lists a directory sorted by name; a missing directory has no entries.
func entries(dir string) ([]fs.DirEntry, error) {
	es, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return es, err
}

func loadSkills(dir string, m *Module) error {
	es, err := entries(filepath.Join(dir, "skills"))
	if err != nil {
		return err
	}
	for _, e := range es {
		if !e.IsDir() {
			continue
		}
		rel := "skills/" + e.Name()
		if ok, err := hasSkillFile(dir, rel, m); err != nil {
			return err
		} else if !ok {
			m.Warnings = append(m.Warnings, safeName(rel)+" has no SKILL.md; skipped")
			continue
		}
		if err := ValidateName("skill", e.Name()); err != nil {
			return err
		}
		s := Skill{Name: e.Name(), Dir: rel}
		if ok, err := skillDoc(dir, rel, m, &s); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("%s has no regular SKILL.md", safeName(rel))
		}
		m.Skills = append(m.Skills, s)
	}
	if ok, err := hasSkillFile(dir, ".", m); err != nil || !ok {
		return err
	}
	if slices.ContainsFunc(m.Skills, func(s Skill) bool { return s.Name == m.Name }) {
		return fmt.Errorf("SKILL.md at the module root and skills/%s/ both define skill %q", m.Name, m.Name)
	}
	exclude, err := rootExclude(dir)
	if err != nil {
		return err
	}
	s := Skill{Name: m.Name, Dir: ".", Exclude: exclude}
	if ok, err := skillDoc(dir, ".", m, &s); err != nil {
		return err
	} else if !ok {
		return errors.New("the module root has no regular SKILL.md")
	}
	m.Skills = append(m.Skills, s)
	sort.Slice(m.Skills, func(i, j int) bool { return m.Skills[i].Name < m.Skills[j].Name })
	return nil
}

// hasSkillFile reports whether the content-relative directory rel holds an entry named exactly
// SKILL.md or SKILL.md.liquid, of any type: one that is not a regular file refuses the skill.
// When it holds neither, the case variants of those names it holds warn (skillDoc warns them
// otherwise).
func hasSkillFile(dir, rel string, m *Module) (bool, error) {
	files, variants, err := skillFiles(dir, rel)
	if err != nil || len(files) > 0 {
		return len(files) > 0, err
	}
	for _, n := range variants {
		m.Warnings = append(m.Warnings, variantWarning(rel, n))
	}
	return false, nil
}

// markdownFiles lists sub/*.md and sub/*.md.liquid as (name, rel) pairs, validating each name
// as kind, and warns about a file whose suffix is a case variant of .md.liquid.
func markdownFiles(dir, sub, kind string, m *Module) ([][2]string, error) {
	es, err := entries(filepath.Join(dir, sub))
	if err != nil {
		return nil, err
	}
	var out [][2]string
	for _, e := range es {
		if !e.IsDir() && suffixVariant(e.Name()) {
			m.Warnings = append(m.Warnings, suffixWarning(sub+"/"+e.Name()))
			continue
		}
		name := ComponentName(e.Name())
		if e.IsDir() && !IsTemplate(e.Name()) || (!IsTemplate(e.Name()) && !strings.HasSuffix(e.Name(), ".md")) {
			continue
		}
		if err := ValidateName(kind, name); err != nil {
			return nil, err
		}
		out = append(out, [2]string{name, sub + "/" + e.Name()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out, nil
}

func loadCommands(dir string, m *Module) error {
	files, err := markdownFiles(dir, "commands", "command", m)
	if err != nil {
		return err
	}
	for _, f := range files {
		doc, err := readComponent(dir, f[1], m)
		if err != nil {
			return err
		}
		c := Command{Name: f[0], File: f[1], Doc: doc}
		if fi, err := os.Stat(filepath.Join(dir, "commands", f[0])); err == nil && fi.IsDir() {
			c.SidecarDir = "commands/" + f[0]
			if c.Sidecars, err = sidecars(dir, c, m); err != nil {
				return err
			}
		}
		m.Commands = append(m.Commands, c)
	}
	return nil
}

// sidecars reads the .md and .md.liquid files under a command's sidecar directory, which
// Claude Code reads as commands of their own, and refuses frontmatter hooks in them (a
// template's once rendered, sidecarHooks).
func sidecars(dir string, c Command, m *Module) ([]Sidecar, error) {
	var out []Sidecar
	err := filepath.WalkDir(filepath.Join(dir, filepath.FromSlash(c.SidecarDir)), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() && !IsTemplate(d.Name()) {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		switch {
		case suffixVariant(d.Name()):
			m.Warnings = append(m.Warnings, suffixWarning(rel))
			return nil
		case IsTemplate(d.Name()):
			doc, err := readComponent(dir, rel, m)
			out = append(out, Sidecar{File: strings.TrimSuffix(rel, ".liquid"), Source: rel, Doc: doc})
			return err
		case !strings.EqualFold(filepath.Ext(p), ".md"):
			return nil
		}
		doc, err := readDoc(dir, rel)
		if err != nil {
			return err
		}
		s := Sidecar{File: rel, Doc: doc}
		if err := sidecarHooks(c.Name, s); err != nil {
			return err
		}
		out = append(out, s)
		return nil
	})
	return out, err
}

// sidecarHooks refuses frontmatter hooks in a command's sidecar: Claude Code would run those
// hooks, while lola installs the directory as the command's files, outside the consent that
// covers the command's hooks.
func sidecarHooks(command string, s Sidecar) error {
	for _, k := range slices.Sorted(maps.Keys(s.Doc.Frontmatter)) {
		if strings.EqualFold(k, "hooks") {
			return entryErr(fmt.Sprintf("command %s: %s: frontmatter %s", command, safeName(s.File), safeName(k)),
				"Claude Code reads this file as a command, so lola would install its hooks without asking", "put hooks in the command itself")
		}
	}
	return nil
}

func loadAgents(dir string, m *Module) error {
	files, err := markdownFiles(dir, "agents", "agent", m)
	if err != nil {
		return err
	}
	for _, f := range files {
		doc, err := readComponent(dir, f[1], m)
		if err != nil {
			return err
		}
		m.Agents = append(m.Agents, Agent{Name: f[0], File: f[1], Doc: doc})
	}
	return nil
}

// mcpEntry is one server in a Claude-style mcps.json. Unknown keys are ignored: real-world
// files carry tool-specific extras, and lola only renders the fields it understands.
type mcpEntry struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	Cwd     string            `json:"cwd"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

func loadMCP(dir string, m *Module) error {
	if _, err := os.Lstat(filepath.Join(dir, "mcps.json")); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	servers, err := mcpFile(dir, "mcps.json")
	m.MCPServers = servers
	return err
}

// loadRules reads rules/*.md (§5.6), sorted by name, and warns about anything else in rules/
// and about legacy instruction files, which are never installed.
func loadRules(dir string, m *Module) error {
	es, err := entries(filepath.Join(dir, "rules"))
	if err != nil {
		return err
	}
	for _, e := range es {
		switch {
		case !e.IsDir() && suffixVariant(e.Name()):
			m.Warnings = append(m.Warnings, suffixWarning("rules/"+e.Name()))
			continue
		case e.IsDir() && !IsTemplate(e.Name()) || !IsTemplate(e.Name()) && !strings.HasSuffix(e.Name(), ".md"):
			m.Warnings = append(m.Warnings, safeName("rules/"+e.Name())+" is not a rule (rules/<name>.md); skipped")
			continue
		}
		name := ComponentName(e.Name())
		if err := ValidateName("rule", name); err != nil {
			return err
		}
		r, err := readRule(dir, "rules/"+e.Name(), m)
		if err != nil {
			return err
		}
		r.Name = name
		m.Rules = append(m.Rules, r)
	}
	sort.Slice(m.Rules, func(i, j int) bool { return m.Rules[i].Name < m.Rules[j].Name })
	for _, f := range []string{"INSTRUCTIONS.md", "AGENTS.md"} {
		_, err := os.Stat(filepath.Join(dir, f))
		switch {
		case err == nil:
			m.Warnings = append(m.Warnings, f+" in a module is not installed: lola never edits your assistants' "+
				"instruction files. Move it to rules/<topic>.md to ship it as always-on rules")
		case !errors.Is(err, fs.ErrNotExist):
			return err
		}
	}
	return nil
}

// readRule reads a rule file (§5.6): its body, without frontmatter, which may not scope it to
// paths, or a template's source, which the engine renders and checks so.
func readRule(dir, rel string, m *Module) (Rule, error) {
	doc, err := readComponent(dir, rel, m)
	if err != nil {
		return Rule{}, err
	}
	if doc.Template != nil {
		return Rule{Body: doc.Body, Template: doc.Template}, nil
	}
	if err := rulePaths(rel, doc); err != nil {
		return Rule{}, err
	}
	return Rule{Body: doc.Body}, nil
}

// rulePaths refuses a rule whose frontmatter scopes it to paths: a rule applies in every
// session. file names the rule's file in the refusal.
func rulePaths(file string, doc Doc) error {
	if _, ok := doc.Frontmatter["paths"]; ok {
		return errs.WithHints(fmt.Errorf("%s: a rule applies in every session, so paths: is not supported", safeName(file)),
			"path-scoped guidance belongs in a skill: skills/<name>/SKILL.md")
	}
	return nil
}

// checkHooks refuses hooks lola could not run: unknown events, and scripts that are not
// executable regular files inside the module.
func checkHooks(dir string, m *Module) error {
	for _, h := range m.DeclaredHooks {
		if h.Event != "pre-install" && h.Event != "post-install" {
			return fmt.Errorf("%s: hook %q is unknown; lola runs pre-install and post-install hooks", m.Manifest, h.Event)
		}
		if !filepath.IsLocal(h.Script) {
			return fmt.Errorf("%s: %s hook %q must be a relative path inside the module", m.Manifest, h.Event, h.Script)
		}
		fi, err := os.Lstat(filepath.Join(dir, h.Script))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return fmt.Errorf("%s: %s hook %q does not exist", m.Manifest, h.Event, h.Script)
		case err != nil:
			return err
		case !fi.Mode().IsRegular():
			return fmt.Errorf("%s: %s hook %q is not a regular file", m.Manifest, h.Event, h.Script)
		case fi.Mode().Perm()&0o111 == 0:
			return errs.WithHints(fmt.Errorf("%s: %s hook %q is not executable", m.Manifest, h.Event, h.Script),
				errs.ShellJoin([]string{"chmod", "+x", untrusted.Text(h.Script, len(h.Script))}))
		}
	}
	return nil
}

// Normalize rewrites the module at dir as the cache stores it (ADR #243): exactly one
// generated lola.yml, built from m, and no other manifest. m must have been loaded from dir.
func Normalize(dir string, m *Module) error {
	lf := lolaFile{Name: m.Name, Description: m.Description, Version: m.Version, Author: m.Author, License: m.License,
		Homepage: m.Homepage, Repository: m.Repository, Keywords: m.Keywords, Targets: m.Targets, Formats: m.Formats,
		Components: m.components}
	if m.Format == FormatClaudePlugin || m.Format == FormatAgentPlugins {
		lf.Format = m.Format
	}
	if len(m.Vars) > 0 {
		lf.Vars = &varsNode{node: &yaml.Node{}}
		if err := lf.Vars.node.Encode(m.Vars); err != nil {
			return err
		}
		for i := 0; i+1 < len(lf.Vars.node.Content); i += 2 { // as the encoder writes it, 1.0 would read back as 1
			if f, ok := m.Vars[lf.Vars.node.Content[i].Value].(float64); ok {
				lf.Vars.node.Content[i+1] = FloatNode(f)
			}
		}
	}
	for _, h := range m.DeclaredHooks {
		if lf.Hooks == nil {
			lf.Hooks = map[string]string{}
		}
		lf.Hooks[h.Event] = h.Script
	}
	var doc yaml.Node
	if err := doc.Encode(lf); err != nil {
		return err
	}
	quoteMerge(&doc)
	b, err := yaml.Marshal(&doc)
	if err != nil {
		return err
	}
	for _, n := range ManifestNames() {
		if err := os.Remove(filepath.Join(dir, n)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return os.WriteFile(filepath.Join(dir, "lola.yml"), b, 0o644)
}

// quoteMerge makes every "<<" scalar below n, key or value, a quoted string. The encoder tags
// it !!merge and writes it plain: as a key YAML reads it back as a merge key, so a server's
// env, a hooks event or a kept value would come back from the cache meaning something else,
// and as a value it reads as "!!merge <<" to any other YAML reader.
func quoteMerge(n *yaml.Node) {
	if n.Kind == yaml.ScalarNode && n.Value == "<<" {
		n.Tag, n.Style = "!!str", yaml.DoubleQuotedStyle
	}
	for _, c := range n.Content {
		quoteMerge(c)
	}
}
