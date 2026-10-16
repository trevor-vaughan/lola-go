package cli

import (
	"errors"
)

// Prompter asks the install flow's questions (Plan 5c §3, §4): one method per step and one
// for the review menu. The huh adapter draws them inline on stderr; specs inject a scripted
// one. A method returns ErrAborted when the user cancels. Validation a step needs (at least
// one scope, an Other directory that exists) runs in the flow after the method returns, which
// asks again with Problem set.
type Prompter interface {
	// Interactive reports whether the prompter can ask: for the huh adapter, stdin and stderr
	// are terminals.
	Interactive() bool
	// Scope answers the scope picker's entries: ScopeProject, ScopeUser, ScopeOther.
	Scope(ScopeStep) ([]string, error)
	// OtherDirectory asks for the directory of Other directory…, as typed.
	OtherDirectory(DirectoryStep) (string, error)
	Modules(ModuleStep) (ModuleAnswer, error)
	Assistants(AssistantStep) ([]string, error)
	Components(ComponentPage) ([]string, error)
	// ConfirmEmpty asks "Install <module> with no components?", default No.
	ConfirmEmpty(module string) (bool, error)
	Review(ReviewStep) (string, error)
}

// The scope picker's entries (Plan 5c §14), in the order it offers them.
const (
	ScopeProject = "project"
	ScopeUser    = "user"
	ScopeOther   = "other"
)

// ScopeStep offers Project (at ProjectRoot), User and Other directory…. Nothing is preselected
// on the first run; when Change scope asks again, Preselect is the last answer.
type ScopeStep struct {
	ProjectRoot string
	Home        string
	Preselect   []string
	Problem     string
}

// DirectoryStep asks for the Other directory. Value is the answer offered to edit (the last
// one, when it is safe to show); Problem says why the last answer was refused.
type DirectoryStep struct {
	Value   string
	Problem string
}

// ModuleOption is one cached module in the module picker. Label is
// "source · name — description", marked (installed) when installed in a chosen scope, already
// made safe to print.
type ModuleOption struct {
	Name  string
	Label string
}

// ModuleStep offers the cached modules; with none, the prompter offers Add a source… and Quit.
// The prompter selects exactly Preselect: nothing on the first run, so an installed module is
// never picked for the user, and the last answer when the step runs again.
type ModuleStep struct {
	Options   []ModuleOption
	Preselect []string
	Problem   string
}

// ModuleAnswer is the module picker's answer: modules, or a source to add (then the picker
// runs again with Names and the new module selected). AddSource with an empty Source means the
// user left the source empty: the picker runs again with Names selected.
type ModuleAnswer struct {
	Names     []string
	AddSource bool
	Source    string
	Ref       string
}

// AssistantStep offers the installed assistants for Scopes, the scopes whose targets are open.
type AssistantStep struct {
	Options   []string
	Preselect []string
	Scopes    []string
	Problem   string
}

// ComponentOption is one component of a page. Label is "name — first line of description",
// with "(runs a command)" or "(skipped in selections.yaml)" as they apply.
type ComponentOption struct {
	ID       string // kind/name
	Label    string
	Selected bool
}

// ComponentGroup is one kind on a page, titled with its count. A disabled group is shown
// read-only with Note (rules under --no-rules).
type ComponentGroup struct {
	Kind     string
	Title    string
	Options  []ComponentOption
	Disabled bool
	Note     string
}

// ComponentPage is one module's components for one scope, or for both when their
// preselections are equal. Footer is the read-only runtime hooks line, or "".
type ComponentPage struct {
	Module string
	Title  string
	Groups []ComponentGroup
	Footer string
}

// Review menu answers.
const (
	ReviewApply      = "apply"
	ReviewScope      = "scope"
	ReviewModules    = "modules"
	ReviewAssistants = "assistants"
	ReviewComponents = "components"
	ReviewQuit       = "quit"
)

// ReviewStep is the menu after the summary. Options are the answers it offers, in order: Apply,
// a Change entry for each step the flags left open, and Quit.
type ReviewStep struct {
	Options []string
}

// errNoTerminal is what the non-interactive Prompter answers; the flow never asks it.
var errNoTerminal = errors.New("there is no terminal to ask on")

// noPrompter is the Prompter off a terminal and under --json: it never asks.
type noPrompter struct{}

func (noPrompter) Interactive() bool                            { return false }
func (noPrompter) Scope(ScopeStep) ([]string, error)            { return nil, errNoTerminal }
func (noPrompter) OtherDirectory(DirectoryStep) (string, error) { return "", errNoTerminal }
func (noPrompter) Modules(ModuleStep) (ModuleAnswer, error)     { return ModuleAnswer{}, errNoTerminal }
func (noPrompter) Assistants(AssistantStep) ([]string, error)   { return nil, errNoTerminal }
func (noPrompter) Components(ComponentPage) ([]string, error)   { return nil, errNoTerminal }
func (noPrompter) ConfirmEmpty(string) (bool, error)            { return false, errNoTerminal }
func (noPrompter) Review(ReviewStep) (string, error)            { return "", errNoTerminal }
