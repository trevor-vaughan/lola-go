package module

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/trevor-vaughan/lola-go/internal/errs"
)

// ErrNoModule means a directory contains no artifact lola can install.
var ErrNoModule = errors.New("no module found")

const lookedFor = "skills/*/SKILL.md, SKILL.md, commands/*.md, agents/*.md, mcps.json or rules/*.md"

// lolaFile is the lola.yml schema (§5.4). Only name is required.
type lolaFile struct {
	Name        string            `yaml:"name"`
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
}

// ManifestNames lists the module manifest file names in precedence order (ADR #243): the
// first one present is read and the others are ignored with a warning.
func ManifestNames() []string { return []string{"lola.yaml", "lola.yml", ".lola.yaml", ".lola.yml"} }

// ShadowWarning says that a lower-precedence manifest was ignored.
func ShadowWarning(winner, shadowed string) string {
	return shadowed + " was ignored: " + winner + " takes precedence"
}

const lolaKeys = "name, description, version, author, license, homepage, repository, keywords, targets, formats, hooks"

var unknownFieldRE = regexp.MustCompile(`field (\S+) not found`)

// rootSkillExclude lists module-level entries that are never part of a root-level skill.
func rootSkillExclude() []string {
	return append(ManifestNames(), "SKILL.md", "skills", "commands", "agents", "mcps.json", "rules", "INSTRUCTIONS.md", "AGENTS.md")
}

// Load reads the module rooted at dir.
func Load(dir, fallbackName string) (*Module, error) {
	m := &Module{Format: "native", Name: fallbackName}
	if err := loadLolaFile(dir, m); err != nil {
		return nil, err
	}
	if err := ValidateName("module", m.Name); err != nil {
		return nil, errs.WithHints(err, "add a lola.yml with a valid name: field")
	}
	if err := checkHooks(dir, m); err != nil {
		return nil, err
	}
	steps := []func(string, *Module) error{loadSkills, loadCommands, loadAgents, loadMCP, loadRules}
	for _, step := range steps {
		if err := step(dir, m); err != nil {
			return nil, err
		}
	}
	if len(m.Skills)+len(m.Commands)+len(m.Agents)+len(m.MCPServers)+len(m.Rules) == 0 {
		return nil, fmt.Errorf("%s: %w: looked for %s", dir, ErrNoModule, lookedFor)
	}
	return m, nil
}

func loadLolaFile(dir string, m *Module) error {
	var found []string
	for _, n := range ManifestNames() {
		_, err := os.Stat(filepath.Join(dir, n))
		switch {
		case err == nil:
			found = append(found, n)
		case !errors.Is(err, fs.ErrNotExist):
			return err
		}
	}
	if len(found) == 0 {
		return nil
	}
	m.Manifest, m.Shadowed = found[0], found[1:]
	for _, n := range m.Shadowed {
		m.Warnings = append(m.Warnings, ShadowWarning(m.Manifest, n))
	}
	b, err := os.ReadFile(filepath.Join(dir, found[0]))
	if err != nil {
		return err
	}
	var lf lolaFile
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&lf); err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%s is empty; it needs at least a name: field", found[0])
		}
		var hints []string
		if mm := unknownFieldRE.FindStringSubmatch(err.Error()); mm != nil {
			hints = append(hints, fmt.Sprintf("remove or fix %q; known keys are %s", mm[1], lolaKeys))
		}
		return errs.WithHints(fmt.Errorf("%s: %w", found[0], err), hints...)
	}
	if lf.Name == "" {
		return fmt.Errorf("%s: name is required", found[0])
	}
	m.Format = "lola"
	m.Name, m.Description, m.Version = lf.Name, lf.Description, lf.Version
	m.Author, m.License, m.Homepage, m.Repository = lf.Author, lf.License, lf.Homepage, lf.Repository
	m.Keywords, m.Targets, m.Formats = lf.Keywords, lf.Targets, lf.Formats
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
		return Doc{}, err
	}
	d, err := ParseDoc(b)
	if err != nil {
		return Doc{}, fmt.Errorf("%s: %w", rel, err)
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
		if _, err := os.Stat(filepath.Join(dir, rel, "SKILL.md")); err != nil {
			m.Warnings = append(m.Warnings, rel+" has no SKILL.md; skipped")
			continue
		}
		if err := ValidateName("skill", e.Name()); err != nil {
			return err
		}
		doc, err := readDoc(dir, rel+"/SKILL.md")
		if err != nil {
			return err
		}
		m.Skills = append(m.Skills, Skill{Name: e.Name(), Dir: rel, Doc: doc})
	}
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err == nil {
		if slices.ContainsFunc(m.Skills, func(s Skill) bool { return s.Name == m.Name }) {
			return fmt.Errorf("SKILL.md at the module root and skills/%s/ both define skill %q", m.Name, m.Name)
		}
		doc, err := readDoc(dir, "SKILL.md")
		if err != nil {
			return err
		}
		m.Skills = append(m.Skills, Skill{Name: m.Name, Dir: ".", Exclude: rootSkillExclude(), Doc: doc})
		sort.Slice(m.Skills, func(i, j int) bool { return m.Skills[i].Name < m.Skills[j].Name })
	}
	return nil
}

// markdownFiles lists sub/*.md as (name, rel) pairs, validating each name as kind.
func markdownFiles(dir, sub, kind string) ([][2]string, error) {
	es, err := entries(filepath.Join(dir, sub))
	if err != nil {
		return nil, err
	}
	var out [][2]string
	for _, e := range es {
		name, ok := strings.CutSuffix(e.Name(), ".md")
		if e.IsDir() || !ok {
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
	files, err := markdownFiles(dir, "commands", "command")
	if err != nil {
		return err
	}
	for _, f := range files {
		doc, err := readDoc(dir, f[1])
		if err != nil {
			return err
		}
		c := Command{Name: f[0], File: f[1], Doc: doc}
		if fi, err := os.Stat(filepath.Join(dir, "commands", f[0])); err == nil && fi.IsDir() {
			c.SidecarDir = "commands/" + f[0]
		}
		m.Commands = append(m.Commands, c)
	}
	return nil
}

func loadAgents(dir string, m *Module) error {
	files, err := markdownFiles(dir, "agents", "agent")
	if err != nil {
		return err
	}
	for _, f := range files {
		doc, err := readDoc(dir, f[1])
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
	b, err := os.ReadFile(filepath.Join(dir, "mcps.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var doc struct {
		MCPServers map[string]mcpEntry `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return fmt.Errorf("mcps.json: %w", err)
	}
	for name, e := range doc.MCPServers {
		if err := ValidateName("MCP server", name); err != nil {
			return fmt.Errorf("mcps.json: %w", err)
		}
		s := MCPServer{Name: name, Command: e.Command, Args: e.Args, Env: e.Env, Cwd: e.Cwd, URL: e.URL, Headers: e.Headers}
		switch {
		case e.Type == "sse":
			s.Transport = TransportSSE
		case e.Type == "http" || e.Type == "streamable-http" || (e.Type == "" && e.URL != ""):
			s.Transport = TransportHTTP
		case (e.Type == "stdio" || e.Type == "") && e.Command != "":
			s.Transport = TransportStdio
		case e.Type != "" && e.Type != "stdio":
			return fmt.Errorf("mcps.json: server %q: unknown transport %q (use stdio, http or sse)", name, e.Type)
		default:
			return fmt.Errorf("mcps.json: server %q needs a command or a url", name)
		}
		if s.Transport != TransportStdio && s.URL == "" {
			return fmt.Errorf("mcps.json: server %q needs a command or a url", name)
		}
		m.MCPServers = append(m.MCPServers, s)
	}
	sort.Slice(m.MCPServers, func(i, j int) bool { return m.MCPServers[i].Name < m.MCPServers[j].Name })
	return nil
}

// loadRules reads rules/*.md (§5.6), sorted by name, and warns about anything else in rules/
// and about legacy instruction files, which are never installed.
func loadRules(dir string, m *Module) error {
	es, err := entries(filepath.Join(dir, "rules"))
	if err != nil {
		return err
	}
	for _, e := range es {
		name, ok := strings.CutSuffix(e.Name(), ".md")
		if e.IsDir() || !ok {
			m.Warnings = append(m.Warnings, "rules/"+e.Name()+" is not a rule (rules/<name>.md); skipped")
			continue
		}
		if err := ValidateName("rule", name); err != nil {
			return err
		}
		rel := "rules/" + e.Name()
		doc, err := readDoc(dir, rel)
		if err != nil {
			return err
		}
		if _, ok := doc.Frontmatter["paths"]; ok {
			return errs.WithHints(fmt.Errorf("%s: a rule applies in every session, so paths: is not supported", rel),
				"path-scoped guidance belongs in a skill: skills/<name>/SKILL.md")
		}
		m.Rules = append(m.Rules, Rule{Name: name, Body: doc.Body})
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
			return fmt.Errorf("%s: %s hook %s does not exist", m.Manifest, h.Event, h.Script)
		case err != nil:
			return err
		case !fi.Mode().IsRegular():
			return fmt.Errorf("%s: %s hook %s is not a regular file", m.Manifest, h.Event, h.Script)
		case fi.Mode().Perm()&0o111 == 0:
			return errs.WithHints(fmt.Errorf("%s: %s hook %s is not executable", m.Manifest, h.Event, h.Script), "chmod +x "+h.Script)
		}
	}
	return nil
}

// Normalize rewrites the module at dir as the cache stores it (ADR #243): exactly one
// generated lola.yml, built from m, and no other manifest. m must have been loaded from dir.
func Normalize(dir string, m *Module) error {
	lf := lolaFile{Name: m.Name, Description: m.Description, Version: m.Version, Author: m.Author, License: m.License,
		Homepage: m.Homepage, Repository: m.Repository, Keywords: m.Keywords, Targets: m.Targets, Formats: m.Formats}
	for _, h := range m.DeclaredHooks {
		if lf.Hooks == nil {
			lf.Hooks = map[string]string{}
		}
		lf.Hooks[h.Event] = h.Script
	}
	b, err := yaml.Marshal(lf)
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
