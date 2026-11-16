package engine

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/target"
)

// isState reports whether f says what a module is rather than what a run did: a safety finding
// about a module that is a component's pre-approved tools or permission mode, a load warning, the
// module's native executables, or an artifact skipped or, under --force, rendered anyway. Its key
// is recorded in lock.json (ledger.Entry.Safety), so a later run that shows it again reports it as
// seen. Every other finding is an event: new on every run, never recorded.
func isState(f target.Finding) bool {
	if cmp.Or(f.Class, target.ClassSafety) != target.ClassSafety || f.Module == "" {
		return false
	}
	switch {
	case strings.HasSuffix(f.Code, "/allowed-tools"), strings.HasSuffix(f.Code, "/permission-mode"),
		f.Code == "module-warning", f.Code == CodeNativeExecutable:
		return true
	}
	return (f.Severity == target.SeveritySkip || f.Forced) && f.Artifact != ""
}

// safetyKey is what lock.json records of a state finding: the sha256, in hex, of its module,
// target, kind, artifact, code and text, and for one rendered under --force that it was, since
// rendering an artifact is another fact than skipping it. A changed tool list changes the text,
// so it is new again.
func safetyKey(f target.Finding) string {
	parts := []string{f.Module, f.Target, f.Kind, f.Artifact, f.Code, f.Text}
	if f.Forced {
		parts = append(parts, "forced")
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

// declinedKey is what lock.json records of module's runtime hooks for targetID in the settings
// file at settings, declined: the sha256, in hex, of those and the consent digest of the hooks as
// rendered (describeRuntimeHooks), so a changed hook or another module version is new again, and
// of the declined status, so a hook granted later and declined again is new again too.
func declinedKey(module, targetID, settings, digest string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{module, targetID, settings, digest, RuntimeHooksDeclined}, "\x00")))
	return hex.EncodeToString(sum[:])
}

// safetyKeys is the keys of the state findings among finds, sorted, each once; [] for none.
func safetyKeys(finds []target.Finding) []string {
	keys := []string{}
	for _, f := range finds {
		if isState(f) {
			keys = append(keys, safetyKey(f))
		}
	}
	slices.Sort(keys)
	return slices.Compact(keys)
}

// markNew sets New on each safety finding of fs for scope sc that has none yet: false for a state
// finding whose key its module's entry in led, lock.json as the run read it, records; true
// otherwise. A finding another scope of the run is for (Finding.Scope) waits for that scope's.
func markNew(fs []target.Finding, sc Scope, led *ledger.Ledger) {
	for i := range fs {
		f := &fs[i]
		if f.New != nil || cmp.Or(f.Class, target.ClassSafety) != target.ClassSafety || f.Scope != "" && f.Scope != sc.Kind.String() {
			continue
		}
		seen := false
		if ent := led.Modules[f.Module]; ent != nil && isState(*f) {
			seen = slices.Contains(ent.Safety, safetyKey(*f))
		}
		f.New = new(!seen)
	}
}

// markEvents sets New to true on each safety finding of fs no lock.json was compared with: one
// about no module, or made after the plan, such as an era's choice or a probe's mismatch.
func markEvents(fs []target.Finding) {
	for i := range fs {
		if fs[i].New == nil && cmp.Or(fs[i].Class, target.ClassSafety) == target.ClassSafety {
			fs[i].New = new(true)
		}
	}
}
