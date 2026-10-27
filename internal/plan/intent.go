// Package plan turns desired artifacts into a checked, byte-exact set of file changes
// (Compute) and applies them transactionally (Apply). Nothing touches the disk before every
// conflict is known.
package plan

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"strconv"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Op is the kind of change an Intent asks for.
type Op string

// Intent operations. CopyTree is expanded into one WriteFile per file before diffing.
const (
	OpWriteFile     Op = "write-file"
	OpCopyTree      Op = "copy-tree"
	OpSetJSON       Op = "set-json"
	OpAddJSONMember Op = "add-json-member"
)

// Intent is one desired artifact. Path is relative to the target's scope root.
type Intent struct {
	Target string
	Kind   string // skill, command, agent, mcp, rule
	Op     Op
	Path   string

	Data []byte      // WriteFile
	Mode fs.FileMode // WriteFile; 0 means 0644

	Src     string   // CopyTree: absolute source directory
	Exclude []string // CopyTree: Src-relative slash paths to skip

	Pointer string          // SetJSON: RFC 6901 pointer to an object member; AddJSONMember: to the array
	Value   []byte          // SetJSON: standard JSON
	Member  json.RawMessage // AddJSONMember: the element lola owns in the array, any JSON value (§6.4, Plan 5b §7.2)

	Consent *HookConsent // set when the intent installs runtime hooks; the engine writes it only with consent
}

// HookConsent marks an intent that installs runtime hooks into an assistant (Plan 5b design
// §7.3): a settings hook group, or any file of a component whose frontmatter carries hooks or,
// for an agent, inline MCP servers, which Claude Code starts with the agent.
// The engine writes it only under the user's recorded consent for its module and Settings.
type HookConsent struct {
	Settings string          // root-relative settings file whose consent covers the hooks
	Kind     string          // the component's kind (skill, command, agent); "" for a settings hook group
	Name     string          // the component's name; "" for a settings hook group
	Hooks    json.RawMessage // the component's hooks event map as canonical JSON; nil for a group (its Member) or a component without hooks
	// MCPServers are the inline MCP servers of an agent's frontmatter as rendered, a JSON list of
	// single-server objects; nil when it has none.
	MCPServers json.RawMessage
}

// StringMember is the Member of a string element.
func StringMember(s string) json.RawMessage {
	b, _ := json.Marshal(s) // encoding a string cannot fail
	return b
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
	ActionAbsent Action = "absent" // an owned non-string array member the user removed: kept owned, not rewritten
)

// Step is one line of a plan as shown to the user.
type Step struct {
	Action Action          `json:"action"`
	Scope  string          `json:"scope,omitempty"` // project or user; the engine sets it
	Root   string          `json:"root,omitempty"`  // the project root, set by install at project scope
	Module string          `json:"module,omitempty"`
	Target string          `json:"target"`
	Kind   string          `json:"kind"`
	Path   string          `json:"path"`
	Key    string          `json:"key,omitempty"`
	Value  string          `json:"value,omitempty"`  // the owned array element, for string array members
	Member json.RawMessage `json:"member,omitempty"` // the owned array element as written (fsx.ReadableJSON), for other array members
	Via    string          `json:"via,omitempty"`    // resolved destination when it is outside the scope root
	Forced string          `json:"forced,omitempty"` // "unowned", "hand-edited" or "missing" when --force overrode a conflict or restored a member
	// Replaced is set when the step overwrites (or, identical, adopts) an unowned destination
	// the request listed as replaceable, instead of refusing it as a conflict.
	Replaced bool   `json:"replaced,omitempty"`
	Note     string `json:"note,omitempty"`
}

// Replaceable is a destination the caller lets a plan overwrite although lola did not write it
// (lola migrate's files of the Python lola, which it backs up first): a file, a directory
// (every file under it), or with Key a JSON member at that pointer. It never covers a
// destination a module owns, nor one reached through a symbolic link below the target's root.
type Replaceable struct {
	Target string
	Path   string // slash-separated, relative to the target's root
	Key    string
	Dir    bool
}

// covers reports whether r lists the destination of target's path and key.
func (r Replaceable) covers(target, path, key string) bool {
	if r.Target != target || r.Key != key {
		return false
	}
	if r.Dir {
		return key == "" && strings.HasPrefix(path, r.Path+"/")
	}
	return r.Path == path
}

// Conflict is a destination lola refuses to touch. Value is the array element, a string or,
// for other members, its canonical JSON. Owner is the installed module that owns it, when that
// is why, and Claimant the module of the run that wants it ("" otherwise).
type Conflict struct {
	Target, Path, Key, Value, Reason string
	Forceable                        bool
	Owner, Claimant                  string
}

// ConflictError lists every conflict of a refused plan. Overridable is set when --force
// overrides every one of them and the run has a command with --force to hint.
type ConflictError struct {
	Conflicts   []Conflict
	Overridable bool
}

func (e *ConflictError) Error() string {
	var b strings.Builder
	if n := len(e.Conflicts); n == 1 {
		b.WriteString("refusing to change anything: 1 conflict")
	} else {
		fmt.Fprintf(&b, "refusing to change anything: %d conflicts", n)
	}
	if e.Overridable {
		b.WriteString(", which --force overrides")
	}
	for _, c := range e.Conflicts {
		dest := c.Path
		if c.Key != "" {
			dest += " " + c.Key
		}
		if c.Value != "" {
			dest += " " + untrusted.Text(strconv.Quote(c.Value), untrusted.MaxName)
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
