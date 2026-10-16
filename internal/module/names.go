package module

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ValidateName enforces the name rule shared by every name that becomes a path segment or a
// JSON key (§5.5). kind names the thing being named in the error ("module", "skill", ...).
func ValidateName(kind, name string) error {
	if !nameRE.MatchString(name) || strings.Contains(name, "..") {
		return fmt.Errorf("%s name %q is invalid: use 1-64 characters from a-z, 0-9, '.', '_' "+
			"and '-', starting with a letter or digit, with no '..'", kind, untrusted.Text(name, 65))
	}
	return nil
}
