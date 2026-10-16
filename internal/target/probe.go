package target

import (
	"regexp"
	"strings"

	"golang.org/x/mod/semver"
)

// Probe tells lola how to find a target's assistant and read its version. lola only looks
// binaries up to decide which targets to offer; it runs one, with VersionArgs, only for
// targets taking part in a command.
type Probe struct {
	Binaries    []string       // names looked up on PATH; empty for assistants without a CLI
	VersionArgs []string       // e.g. {"--version"}
	VersionRE   *regexp.Regexp // its first submatch is the version
}

// Bounds limits a capability to the assistant versions in [Since, Until); empty is unbounded.
// On a ContentRule it limits the problem the rule describes. Rendering always assumes the
// newest version, so output never depends on what is installed; a probed version outside the
// bounds only adds a finding.
type Bounds struct{ Since, Until string }

// newest reports whether the newest version of the assistant has what the bounds describe.
func (b Bounds) newest() bool { return b.Until == "" }

// excludes reports whether a known version lies outside the bounds; an unknown or malformed
// version excludes nothing.
func (b Bounds) excludes(version string) bool {
	v := canonical(version)
	if v == "" {
		return false
	}
	if s := canonical(b.Since); s != "" && semver.Compare(v, s) < 0 {
		return true
	}
	u := canonical(b.Until)
	return u != "" && semver.Compare(v, u) >= 0
}

// canonical turns "1.2.3" into the "v1.2.3" form semver compares, or "" when it is not a
// version.
func canonical(v string) string {
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if !semver.IsValid(v) {
		return ""
	}
	return v
}
