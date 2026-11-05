package target

import "strings"

// Severity says what a finding does to an artifact.
type Severity string

// Severities, from least to most consequential.
const (
	SeverityInfo Severity = "info" // a lossless adaptation; shown by lola check and --json
	SeverityWarn Severity = "warn" // lossy but working: the artifact is rendered
	SeveritySkip Severity = "skip" // the artifact is not rendered for this target
)

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
