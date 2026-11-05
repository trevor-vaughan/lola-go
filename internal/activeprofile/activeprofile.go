// Package activeprofile checks the qualified name of the active profile a scope's lock.json stores
// (Plan 6b §4.4, §5, D8; spec 2026-10-29 §3.4). It is a leaf: the ledger imports it without reading any profile.
package activeprofile

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Max bounds the profiles lock.json's rendered_with records, default aside (Plan 6b §19 S8).
const Max = 64

// Default is the profile that is always on in user scope and never listed.
const Default = "default"

// Base is the name reserved for a scope's base (spec 2026-10-29 §3.1): no profile, active or
// remote, has it.
const Base = "base"

// ErrReserved is Check's refusal of a name of the kind:base, which a lola before Base was reserved
// could record (the ledger's ReservedError coaches it).
var ErrReserved = errors.New("base is reserved for the scope's base; it is never listed as active")

// kinds are the kinds a qualified name may name: user, project and remote profiles.
var kinds = []string{"user", "project", "remote"}

// Check refuses a stored name that is not "<kind>:<name>" with a known kind and a name that
// follows the module-name rules and is not default.
func Check(q string) error {
	kind, name, ok := strings.Cut(q, ":")
	if !ok || !slices.Contains(kinds, kind) {
		return fmt.Errorf("profile %q is not user:, project: or remote: followed by a name", untrusted.Text(q, untrusted.MaxName))
	}
	switch name {
	case Default:
		return errors.New("default is always on and is never listed as active")
	case Base:
		return ErrReserved
	}
	return module.ValidateName("profile", name)
}
