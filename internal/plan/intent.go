// Package plan turns desired artifacts into a checked, byte-exact set of file changes
// (Compute) and applies them transactionally (Apply). Nothing touches the disk before every
// conflict is known.
package plan

import (
	"fmt"
	"io/fs"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/ledger"
)

// Op is the kind of change an Intent asks for.
type Op string

// Intent operations. CopyTree is expanded into one WriteFile per file before diffing.
const (
	OpWriteFile    Op = "write-file"
	OpCopyTree     Op = "copy-tree"
	OpSetJSON      Op = "set-json"
	OpPointerBlock Op = "pointer-block"
)

// KindPointerBlock is the artifact kind of a markdown pointer block.
const KindPointerBlock = "pointer-block"

// Intent is one desired artifact. Path is relative to the target's scope root.
type Intent struct {
	Target string
	Kind   string // skill, command, agent, mcp, instructions, pointer-block
	Op     Op
	Path   string

	Data []byte      // WriteFile
	Mode fs.FileMode // WriteFile; 0 means 0644

	Src     string   // CopyTree: absolute source directory
	Exclude []string // CopyTree: Src-relative slash paths to skip

	Pointer string // SetJSON: RFC 6901 pointer to an object member
	Value   []byte // SetJSON: standard JSON

	Lines []string // PointerBlock
}

// ModuleChange replaces one module's artifacts on Targets. Entry carries the new ledger
// metadata (its Artifacts are ignored); nil Entry means the module is being removed.
type ModuleChange struct {
	Name    string
	Entry   *ledger.Entry
	Targets []string
	Intents []Intent
}

// Action is what a step does to a destination.
type Action string

// Step actions.
const (
	ActionCreate Action = "create"
	ActionUpdate Action = "update"
	ActionAdopt  Action = "adopt"
	ActionDelete Action = "delete"
	ActionForget Action = "forget"
)

// Step is one line of a plan as shown to the user.
type Step struct {
	Action Action `json:"action"`
	Module string `json:"module,omitempty"` // "" for shared artifacts
	Target string `json:"target"`
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Key    string `json:"key,omitempty"`
	Via    string `json:"via,omitempty"`    // resolved destination when it is outside the scope root
	Forced string `json:"forced,omitempty"` // "unowned" or "hand-edited" when --force overrode a conflict
	Note   string `json:"note,omitempty"`
}

// Conflict is a destination lola refuses to touch.
type Conflict struct {
	Target, Path, Key, Reason string
	Forceable                 bool
}

// ConflictError lists every conflict of a refused plan.
type ConflictError struct{ Conflicts []Conflict }

func (e *ConflictError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "refusing to change anything: %d conflict(s)", len(e.Conflicts))
	for _, c := range e.Conflicts {
		dest := c.Path
		if c.Key != "" {
			dest += " " + c.Key
		}
		fmt.Fprintf(&b, "\n  %s: %s: %s", c.Target, dest, c.Reason)
	}
	return b.String()
}

// FileChange is the final content of one real file, or its deletion.
type FileChange struct {
	Target   string
	Logical  string // path relative to Root, as the target sees it
	Abs      string // symlink-resolved destination
	Root     string // the target's scope root, unresolved
	Outside  bool   // Abs is outside the resolved root
	Delete   bool
	Data     []byte
	Mode     fs.FileMode
	StopDirs []string // resolved dirs that empty-directory cleanup never removes
}
