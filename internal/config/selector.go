package config

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"unicode"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Component kinds a selector names.
const (
	KindSkill   = "skill"
	KindCommand = "command"
	KindAgent   = "agent"
	KindMCP     = "mcp"
	KindRule    = "rule"
)

// Kinds lists the component kinds in the order pickers and listings show them.
func Kinds() []string { return []string{KindSkill, KindCommand, KindAgent, KindMCP, KindRule} }

// maxPattern caps a selector's name; component names are at most 64 characters.
const maxPattern = 128

// Pattern names components of one kind within a module: kind/name, where name may be a
// path.Match glob.
type Pattern struct{ Kind, Name string }

func (p Pattern) String() string { return p.Kind + "/" + p.Name }

// Matches reports whether the pattern names the component kind/name.
func (p Pattern) Matches(kind, name string) bool {
	if p.Kind != kind {
		return false
	}
	ok, err := path.Match(p.Name, name)
	return err == nil && ok
}

// Literal reports whether the pattern names one component rather than a glob.
func (p Pattern) Literal() bool { return !strings.ContainsAny(p.Name, `*?[\`) }

// Selector is a --skip, --only or --unskip value: a pattern, qualified by a module name or
// bare (every chosen module with a match).
type Selector struct {
	Module string
	Pattern
}

func (s Selector) String() string {
	if s.Module == "" {
		return s.Pattern.String()
	}
	return s.Module + ":" + s.Pattern.String()
}

// selectorForms is the hint every selector error carries.
const selectorForms = "a selector is kind/name or module:kind/name; kind is one of skill, command, agent, mcp, rule; name may be a glob (review-*)"

// ParsePattern parses kind/name.
func ParsePattern(s string) (Pattern, error) {
	if len(s) > maxPattern {
		return Pattern{}, errs.WithHints(fmt.Errorf("selector %q is longer than %d characters", untrusted.Text(s, 40), maxPattern), selectorForms)
	}
	if strings.ContainsFunc(s, func(r rune) bool { return unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp) }) {
		return Pattern{}, errs.WithHints(fmt.Errorf("selector %q holds a control or format character", untrusted.Text(s, maxPattern)),
			"retype it with printable characters only", selectorForms)
	}
	kind, name, ok := strings.Cut(s, "/")
	if !ok || name == "" {
		return Pattern{}, errs.WithHints(fmt.Errorf("selector %q has no kind/name", untrusted.Text(s, maxPattern)), selectorForms)
	}
	if !slices.Contains(Kinds(), kind) {
		return Pattern{}, errs.WithHints(fmt.Errorf("selector %q has an unknown kind %q", untrusted.Text(s, maxPattern), untrusted.Text(kind, maxPattern)),
			"kinds: "+strings.Join(Kinds(), ", "))
	}
	if strings.Contains(name, "/") {
		return Pattern{}, errs.WithHints(fmt.Errorf("selector %q: a component name has no /", untrusted.Text(s, maxPattern)), selectorForms)
	}
	if _, err := path.Match(name, ""); errors.Is(err, path.ErrBadPattern) {
		return Pattern{}, errs.WithHints(fmt.Errorf("selector %q is not a valid glob", untrusted.Text(s, maxPattern)),
			"globs use * ? and [a-z]; close every [ and escape a literal one as \\[")
	}
	return Pattern{Kind: kind, Name: name}, nil
}

// ParseSelector parses kind/name or module:kind/name.
func ParseSelector(s string) (Selector, error) {
	mod, rest, qualified := strings.Cut(s, ":")
	if !qualified {
		p, err := ParsePattern(s)
		return Selector{Pattern: p}, err
	}
	if err := module.ValidateName("module", mod); err != nil {
		return Selector{}, errs.WithHints(fmt.Errorf("selector %q: %w", untrusted.Text(s, maxPattern), err), selectorForms)
	}
	p, err := ParsePattern(rest)
	return Selector{Module: mod, Pattern: p}, err
}
