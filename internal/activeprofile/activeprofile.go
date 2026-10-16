// Package activeprofile checks the names a scope's active profile list stores in lock.json (Plan
// 6b §4.4, §5, D8). It is a leaf: the ledger imports it without reading any profile.
package activeprofile

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Max is the most profiles an active list holds (Plan 6b §19 S8).
const Max = 64

// Default is the profile that is always on in user scope and never listed.
const Default = "default"

// kinds are the kinds a qualified name may name: user, project and remote profiles.
var kinds = []string{"user", "project", "remote"}

// Check refuses a stored name that is not "<kind>:<name>" with a known kind and a name that
// follows the module-name rules and is not default.
func Check(q string) error {
	kind, name, ok := strings.Cut(q, ":")
	if !ok || !slices.Contains(kinds, kind) {
		return fmt.Errorf("profile %q is not user:, project: or remote: followed by a name", untrusted.Text(q, untrusted.MaxName))
	}
	if name == Default {
		return errors.New("default is always on and is never listed as active")
	}
	return module.ValidateName("profile", name)
}
