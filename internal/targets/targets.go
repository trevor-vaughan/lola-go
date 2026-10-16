// Package targets is the registry of assistant targets: the one place that lists every
// implementation of target.Target. Adding a target means adding its package and one line here.
package targets

import (
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/target/claude"
)

// All returns every target in registration order, which is also help and completion order.
func All() []target.Target { return []target.Target{claude.Target{}} }

// IDs returns the ids of All.
func IDs() []string {
	var ids []string
	for _, t := range All() {
		ids = append(ids, t.ID())
	}
	return ids
}

// Lookup returns the target with id.
func Lookup(id string) (target.Target, bool) {
	for _, t := range All() {
		if t.ID() == id {
			return t, true
		}
	}
	return nil, false
}
