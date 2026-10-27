package module

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// maxNamedChars is how many distinct offending characters an invalid name's error names.
const maxNamedChars = 8

// ValidateName enforces the name rule shared by every name that becomes a path segment or a
// JSON key (§5.5). kind names the thing being named in the error ("module", "skill", ...), which
// says what is wrong with the name before it states the rule.
func ValidateName(kind, name string) error {
	if !nameRE.MatchString(name) || strings.Contains(name, "..") {
		return fmt.Errorf("%s name %q is invalid: %s; use 1-64 characters from a-z, 0-9, '.', '_' "+
			"and '-', starting with a letter or digit, with no '..'", kind, untrusted.Text(name, 65), nameProblems(name))
	}
	return nil
}

// nameProblems says what breaks the name rule in name, an invalid name: its uppercase letters,
// the other characters the rule does not allow (each once, escaped when it does not print), its
// first character, a "..", its length.
func nameProblems(name string) string {
	if name == "" {
		return "it is empty"
	}
	var upper, other []string
	for _, r := range name {
		switch {
		case r >= 'A' && r <= 'Z':
			if !slices.Contains(upper, string(r)) {
				upper = append(upper, string(r))
			}
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			if q := strconv.QuoteRuneToASCII(r); !slices.Contains(other, q) {
				other = append(other, q)
			}
		}
	}
	var out []string
	if len(upper) > 0 {
		out = append(out, charList(upper, "is uppercase", "are uppercase"))
	}
	if len(other) > 0 {
		out = append(out, charList(other, "is not allowed", "are not allowed"))
	}
	if c := name[0]; c == '.' || c == '_' || c == '-' {
		out = append(out, "it starts with '"+string(c)+"'")
	}
	if strings.Contains(name, "..") {
		out = append(out, "it has '..'")
	}
	if n := utf8.RuneCountInString(name); n > 64 {
		out = append(out, fmt.Sprintf("it is %d characters long", n))
	}
	return strings.Join(out, ", ")
}

// charList is items joined as prose ("a", "a and b", "a, b and c"; past maxNamedChars, the first
// of them and "N more") followed by one, the verb for a single item, or many.
func charList(items []string, one, many string) string {
	if len(items) == 1 {
		return items[0] + " " + one
	}
	last := items[len(items)-1]
	if len(items) > maxNamedChars {
		items, last = items[:maxNamedChars], fmt.Sprintf("%d more", len(items)-maxNamedChars)
	} else {
		items = items[:len(items)-1]
	}
	return strings.Join(items, ", ") + " and " + last + " " + many
}
