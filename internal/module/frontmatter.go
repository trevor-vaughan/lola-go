package module

import (
	"errors"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Doc is a markdown document with optional YAML frontmatter between --- fences.
// RawFrontmatter is the author's exact text (without fences) so rendering keeps comments,
// key order and formatting.
type Doc struct {
	Frontmatter    map[string]any
	RawFrontmatter string
	Body           string
}

// ParseDoc splits src into frontmatter and body. A document that does not start with a
// "---" line has no frontmatter, which is valid.
func ParseDoc(src []byte) (Doc, error) {
	s := strings.TrimPrefix(string(src), "\ufeff")
	if !strings.HasPrefix(s, "---\n") && !strings.HasPrefix(s, "---\r\n") {
		return Doc{Body: s}, nil
	}
	rest := s[strings.IndexByte(s, '\n')+1:]
	pos := 0
	for {
		nl := strings.IndexByte(rest[pos:], '\n')
		line := rest[pos:]
		if nl >= 0 {
			line = rest[pos : pos+nl]
		}
		if strings.TrimRight(line, "\r") == "---" {
			raw := rest[:pos]
			body := ""
			if nl >= 0 {
				body = rest[pos+nl+1:]
			}
			var fm map[string]any
			if err := yaml.Unmarshal([]byte(raw), &fm); err != nil {
				return Doc{}, fmt.Errorf("frontmatter: %w", err)
			}
			return Doc{Frontmatter: fm, RawFrontmatter: raw, Body: body}, nil
		}
		if nl < 0 {
			return Doc{}, errors.New("frontmatter starts with --- but has no closing --- line")
		}
		pos += nl + 1
	}
}

// Render rebuilds the document from the raw frontmatter and body.
func (d Doc) Render() string {
	if d.RawFrontmatter == "" && d.Frontmatter == nil {
		return d.Body
	}
	return "---\n" + d.RawFrontmatter + "---\n" + d.Body
}

// Prepend returns a copy with "key: value" as the first frontmatter line. value must be a
// plain YAML scalar (lola only prepends validated names).
func (d Doc) Prepend(key, value string) Doc {
	fm := make(map[string]any, len(d.Frontmatter)+1)
	for k, v := range d.Frontmatter {
		fm[k] = v
	}
	fm[key] = value
	return Doc{Frontmatter: fm, RawFrontmatter: key + ": " + value + "\n" + d.RawFrontmatter, Body: d.Body}
}

// String returns the frontmatter value for key when it is a string, else "".
func (d Doc) String(key string) string {
	s, _ := d.Frontmatter[key].(string)
	return s
}
