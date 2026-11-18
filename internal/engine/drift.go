package engine

import (
	"cmp"
	"slices"

	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

const (
	driftMissing  = "missing"
	driftModified = "modified"
)

// DriftEntry is one component of a list row whose files differ from what lola wrote.
type DriftEntry struct {
	Kind   string `json:"kind"`   // skill, command, agent, mcp, rule, runtime-hook, …, or "copy"
	Name   string `json:"name"`   // the component; "" when lock.json records none
	Path   string `json:"path"`   // the first drifted path of the component; the copy's directory for "copy"
	Key    string `json:"key"`    // the JSON pointer of a keyed artifact, else ""
	Status string `json:"status"` // missing or modified
	Files  int    `json:"files"`  // drifted artifacts in the entry
}

// driftedArtifact is a recorded artifact whose destination is missing or modified.
type driftedArtifact struct {
	a      ledger.Artifact
	status string
}

// groupDrift folds ds, in ledger order, into entries: artifacts of a named component share one
// entry per (kind, name, status), an unnamed one (a file the module's rules share, or an entry
// written before names were recorded) is its own. Path and Key come from the first artifact
// of an entry. Missing entries sort before modified, then by kind, name and path. Names, paths
// and keys are author-chosen, so they pass through untrusted.Text.
func groupDrift(ds []driftedArtifact) []DriftEntry {
	type group struct{ kind, name, status string }
	at := map[group]int{}
	out := []DriftEntry{}
	for _, d := range ds {
		g := group{d.a.Kind, d.a.Name, d.status}
		if i, ok := at[g]; ok && d.a.Name != "" {
			out[i].Files++
			continue
		}
		at[g] = len(out)
		out = append(out, DriftEntry{Kind: untrusted.Text(d.a.Kind, untrusted.MaxName), Name: untrusted.Text(d.a.Name, untrusted.MaxName),
			Path: untrusted.Text(d.a.Path, untrusted.MaxName), Key: untrusted.Text(d.a.Key, untrusted.MaxName),
			Status: d.status, Files: 1})
	}
	slices.SortStableFunc(out, func(a, b DriftEntry) int {
		return cmp.Or(
			cmp.Compare(a.Status, b.Status), // "missing" sorts before "modified"
			cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Name, b.Name), cmp.Compare(a.Path, b.Path))
	})
	return out
}
