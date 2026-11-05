package target

import (
	"cmp"
	"encoding/json"
	"strings"
)

// Severity says what a finding does to an artifact.
type Severity string

// Severities, from least to most consequential.
const (
	SeverityInfo Severity = "info" // a lossless adaptation or a user's choice; shown by lola check and --json, and by install and render when it has a hint
	SeverityWarn Severity = "warn" // lossy but working: the artifact is rendered
	SeveritySkip Severity = "skip" // the artifact is not rendered for this target
)

// Class says how install, sync, update and profile switches show a finding or a notice (spec
// 2026-11-03 §3.4, U6): a safety one in full, a detail one as a count in its module's summary line,
// which -v replaces with them all. JSON gives every one with its class.
type Class string

// Classes. Safety is an allowlist kept by where findings and notices are made: runtime hooks and
// their consent, native executables, hand-edited or unowned files, refusals, what a run did to
// the user's picks, and what an artifact may do with the user's permissions. Detail is what a
// target loses or adapts, and mentions. One made without a class is safety, so a new kind is
// shown until someone decides it is detail.
const (
	ClassSafety Class = "safety"
	ClassDetail Class = "detail"
)

// KindRuntimeHook is the artifact, intent and finding kind of a runtime hook (Plan 5b design
// §7). The engine's consent gate keys on it, so every target and the engine use this constant.
const KindRuntimeHook = "runtime-hook"

// Finding is something a target loses, adapts or cannot express when it renders a module.
// Content problems are always findings, never Plan errors.
type Finding struct {
	Severity  Severity `json:"severity"`
	Module    string   `json:"module,omitempty"` // set by the engine
	Target    string   `json:"target"`           // "" for findings about the module itself
	Kind      string   `json:"kind"`             // skill, command, agent, mcp, rule, module or assistant
	Artifact  string   `json:"artifact,omitempty"`
	Code      string   `json:"code"` // stable identifier, e.g. "required-key-missing"
	Text      string   `json:"text"`
	Hint      string   `json:"hint,omitempty"`
	Forceable bool     `json:"forceable,omitempty"` // a skip that --force may render anyway
	Forced    bool     `json:"forced,omitempty"`    // a forceable skip that --force rendered
	Class     Class    `json:"class"`               // "" is ClassSafety
	// AuthorHint says Hint, as the target made it, tells the module's author what to change in
	// its files: lola check prints it, and install, sync, update and profile switches print
	// UserHint in its place.
	AuthorHint bool `json:"-"`
	// UserHint is what the engine adds to Hint for whoever runs lola (the run with --force that
	// renders a skip anyway), or "".
	UserHint string `json:"-"`
	// Check is the lola check, set by the engine, that lists the finding with the rest of its
	// module's at its scope; "" when no lola check can name the version planned.
	Check string `json:"-"`
}

// MarshalJSON writes f with its class, ClassSafety when it was made without one.
func (f Finding) MarshalJSON() ([]byte, error) {
	type finding Finding
	g := finding(f)
	g.Class = cmp.Or(g.Class, ClassSafety)
	return json.Marshal(g)
}

// Skipped turns f into the finding for an artifact the target leaves out, and reports whether
// to render it anyway: with ctx.Force a forceable skip is rendered and becomes a forced warn.
func Skipped(ctx PlanContext, f Finding) (Finding, bool) {
	f.Severity = SeveritySkip
	if f.Forceable && ctx.Force {
		f.Severity, f.Forced = SeverityWarn, true
		return f, true
	}
	return f, false
}

// Describe is the finding on one line: "<module>: <target> <kind> <artifact>: <text>", leaving
// out the parts that are empty.
func (f Finding) Describe() string {
	var where []string
	if f.Target != "" {
		where = append(where, f.Target)
	}
	if f.Artifact != "" {
		where = append(where, f.Kind, f.Artifact)
	}
	text := f.Text
	if f.Forced {
		text += " (rendered anyway: --force)"
	}
	loc := strings.Join(where, " ")
	switch {
	case f.Module != "" && loc != "":
		return f.Module + ": " + loc + ": " + text
	case f.Module != "":
		return f.Module + ": " + text
	case loc != "":
		return loc + ": " + text
	}
	return text
}
