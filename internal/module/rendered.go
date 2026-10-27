package module

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// yamlLine finds the line yaml.v3 names in an error: "yaml: line 3: …", or "line 3: …" in a
// list of unmarshal errors.
var yamlLine = regexp.MustCompile(`(?:^yaml: |\n\s*)line (\d+): `)

// ErrRenderedFence is RenderedDoc's refusal of a rendered frontmatter line that is --- or ...,
// which would end the frontmatter in the written file however its text was built.
var ErrRenderedFence = errors.New("frontmatter")

// RenderedDoc assembles the Doc of a rendered template from its two regions (Plan 7 §5.6
// step 1): the frontmatter, when the template opens one, is parsed with ParseDoc's frontmatter
// rules; the body is kept as rendered. A refusal names the rendered file's line, the opening
// fence being line 1, when the YAML parser names one, and a line that is --- or ... with only
// spaces, tabs or a carriage return after it (ErrRenderedFence).
func RenderedDoc(front []byte, hasFront bool, body []byte) (Doc, error) {
	if !hasFront {
		return Doc{Body: string(body)}, nil
	}
	raw := string(front)
	for i, line := range strings.Split(raw, "\n") {
		if l := strings.TrimRight(line, " \t\r"); l == "---" || l == "..." {
			return Doc{}, fmt.Errorf("%w: rendered line %d is %s, which would end the frontmatter", ErrRenderedFence, i+2, l)
		}
	}
	if raw != "" && !strings.HasSuffix(raw, "\n") {
		raw += "\n" // a trimmed last line: the closing fence starts a line of its own
	}
	fm, err := parseFrontmatter(raw)
	if err != nil {
		msg := strings.TrimPrefix(err.Error(), "frontmatter: ")
		if m := yamlLine.FindStringSubmatchIndex(msg); m != nil {
			n, _ := strconv.Atoi(msg[m[2]:m[3]]) // the pattern matched digits only
			reason := strings.TrimSpace(strings.TrimPrefix(msg[m[1]:], "yaml: "))
			return Doc{}, errs.WithHints(fmt.Errorf("frontmatter: %s, rendered line %d", untrusted.Text(reason, 2*untrusted.MaxName), n+1), errs.Hints(err)...)
		}
		return Doc{}, errs.WithHints(errors.New("frontmatter: "+untrusted.Text(msg, 2*untrusted.MaxName)), errs.Hints(err)...)
	}
	if fm == nil {
		fm = map[string]any{}
	}
	return Doc{Frontmatter: fm, RawFrontmatter: raw, Body: string(body)}, nil
}

// RenderedRule is a rendered rule template's text: its body without the frontmatter, which may
// not scope the rule to paths, as at load.
func RenderedRule(file string, doc Doc) (string, error) {
	if err := rulePaths(file, doc); err != nil {
		return "", err
	}
	return doc.Body, nil
}

// Recheck runs on a module whose templates were rendered for one target the load checks that
// read frontmatter: frontmatter hooks, an agent's MCP servers, a sidecar's hooks, and for an
// Agent Plugins package, the agentskills.io rules its skills were loaded under (a template's
// are known only once rendered).
func Recheck(m *Module) error {
	if m.Format == FormatAgentPlugins {
		for _, s := range m.Skills {
			if why := agentSkillDoc(s.Doc, path.Base(s.Dir)); why != "" {
				return fmt.Errorf("skill %s: SKILL.md is not a valid Agent Skill: %s", s.Name, why)
			}
		}
	}
	if err := frontmatterHooks(m); err != nil {
		return err
	}
	if err := agentServers(m); err != nil {
		return err
	}
	for _, c := range m.Commands {
		for _, s := range c.Sidecars {
			if err := sidecarHooks(c.Name, s); err != nil {
				return err
			}
		}
	}
	return nil
}
