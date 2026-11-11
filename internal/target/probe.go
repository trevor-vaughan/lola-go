package target

import (
	"regexp"

	"github.com/trevor-vaughan/lola-go/internal/paths"
)

// Probe tells lola how to find a target's assistant and read its version. lola only looks
// binaries up to decide which targets to offer; it reads a version only for targets taking
// part in a command, trying Methods in order until one yields a version.
type Probe struct {
	Methods []ProbeMethod
	// Readers is, for a target with no binary, what reads the files it writes, as -t completion
	// describes the target where it is not set up.
	Readers string
}

// ProbeMethod is one way to read the version: run a binary (Binaries set) or read a file
// (File set).
type ProbeMethod struct {
	Binaries []string                   // names looked up on PATH
	Args     []string                   // e.g. {"--version"}
	File     func(p paths.Paths) string // the file's absolute path
	Pointer  string                     // JSON Pointer to the version string, for a file
	Match    *regexp.Regexp             // first submatch is the version; nil for a file read by Pointer
}

// Binaries are the binaries of p's exec methods, in order.
func (p Probe) Binaries() []string {
	var out []string
	for _, m := range p.Methods {
		out = append(out, m.Binaries...)
	}
	return out
}

// NoBinary reports whether t has no binary to find (agents): it is never an installed assistant,
// and is set up where its markers are.
func NoBinary(t Target) bool { return len(t.Probe().Binaries()) == 0 }
