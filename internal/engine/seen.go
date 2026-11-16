package engine

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// runtimeHookKey is what lock.json records of module's runtime hook h, as consent shows it, in
// the settings file h.Settings: the sha256, in hex, of those and of whether h is declined or
// installed (granted and consented alike), so a changed hook is new again but an unchanged one of
// another module version is not, and a hook granted where it was declined, or declined where it
// was granted, is new again. Its granted form is the hook's key in the shown record: the content,
// with no version, so the same hook shown for another version or project counts as shown.
func runtimeHookKey(module string, h RuntimeHook) (string, error) {
	h.New, h.DeclinedEarlier = nil, false
	if h.Status != RuntimeHooksDeclined {
		h.Status = RuntimeHooksGranted
	}
	view, err := json.Marshal(h)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{module, h.Settings, string(view)}, "\x00")))
	return hex.EncodeToString(sum[:]), nil
}

// answeredNoKey is what lock.json records of module's runtime hook h when a question about it, as it
// is, was answered No: runtimeHookKey of h declined, marked as answered, so a decline where nothing
// asked (off a terminal) is not one, and a later terminal run asks.
func answeredNoKey(module string, h RuntimeHook) (string, error) {
	h.Status = RuntimeHooksDeclined
	k, err := runtimeHookKey(module, h)
	if err != nil {
		return "", err
	}
	return contentKey("answered-no", k), nil
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

// markNew sets New on each safety warning or skip of fs for scope sc that has none yet: false for
// a state finding whose key its module's entry in led, lock.json as the run read it, records;
// true otherwise. An info finding is never new or seen: New stays nil. A finding another scope of
// the run is for (Finding.Scope) waits for that scope's.
func markNew(fs []target.Finding, sc Scope, led *ledger.Ledger) {
	for i := range fs {
		f := &fs[i]
		if f.New != nil || cmp.Or(f.Class, target.ClassSafety) != target.ClassSafety || f.Severity == target.SeverityInfo ||
			f.Scope != "" && f.Scope != sc.Kind.String() {
			continue
		}
		seen := false
		if ent := led.Modules[f.Module]; ent != nil && isState(*f) {
			seen = slices.Contains(ent.Safety, safetyKey(*f))
		}
		f.New = new(!seen)
	}
}

// markEvents sets New to true on each safety warning or skip of fs no lock.json was compared
// with: one about no module, or made after the plan, such as an era's choice or a probe's
// mismatch.
func markEvents(fs []target.Finding) {
	for i := range fs {
		if fs[i].New == nil && cmp.Or(fs[i].Class, target.ClassSafety) == target.ClassSafety && fs[i].Severity != target.SeverityInfo {
			fs[i].New = new(true)
		}
	}
}
