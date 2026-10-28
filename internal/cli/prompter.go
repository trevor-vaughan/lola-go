package cli

import (
	"errors"

	"github.com/trevor-vaughan/lola-go/internal/engine"
)

// Prompter asks the install flow's questions (Plan 5c §3, §4): one method per step and one
// for the review menu; and sync's questions (Plan 6a §3.4): the trust picker and yes/no. The
// huh adapter draws them inline on stderr; specs inject a scripted one. A method returns
// ErrAborted when the user cancels. Scope, Modules, Assistants and a Required TidyKeep never
// answer with nothing picked: the huh adapter refuses that in the form (Plan 10 §4). Other
// validation a step needs (one scope, an Other directory that exists) runs in the flow after
// the method returns, which asks again with Problem set.
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
	// Trust asks sync's trust question (Plan 6a §7.5): trust all the changes, choose the
	// entries to trust, or cancel, which is ErrAborted (so is choosing nothing).
	Trust(TrustStep) (engine.TrustAnswer, error)
	// Confirm asks a yes/no question, default No: sync's removals.
	Confirm(question string) (bool, error)
	// Profiles asks which of a source's profiles lola profile --add adds: a filterable
	// multi-select, nothing preselected (Plan 6b §3.2).
	Profiles(ProfileStep) ([]string, error)
	// MarketName asks the name of a marketplace lola market --add adds when the one it has is
	// another marketplace's, or one no marketplace can have (Plan 6c §3.1), pre-filled with a
	// free one.
	MarketName(MarketNameStep) (string, error)
	// MarketEntry asks which marketplace entry a bare name your cache lacks means (Plan 6c §3.3),
	// answering its address.
	MarketEntry(MarketEntryStep) (string, error)
	// TidyPick asks which of lola tidy's candidates move to the trash (Plan 6d §3.4): a
	// filterable multi-select, nothing preselected, answering the IDs of the options chosen.
	TidyPick(TidyPickStep) ([]string, error)
	// TidyKeep asks which keep-list entries, for the candidates TidyPick left, to add: a
	// filterable multi-select, nothing preselected, answering the IDs of the options chosen.
	TidyKeep(TidyKeepStep) ([]string, error)
	// MigrateRoot asks where a root that missing project paths of the Python lola share is on this
	// machine (Plan 10 §3.1), offering Suggest: an absolute path, or "" to skip them.
	MigrateRoot(MigrateRootStep) (string, error)
	// MigrateTargets asks which assistants install the records of an assistant this machine
	// lacks (Plan 10 §3.2): a multi-select over Options, all preselected; none skips them.
	MigrateTargets(MigrateTargetStep) ([]string, error)
	// Background asks whether the terminal's background is dark, for the pickers' colors (Plan
	// 10 §4). lola asks once, when neither config.yaml nor the terminal says, and saves the answer.
	Background() (bool, error)
}

// MigrateRootStep asks for the new root of Old, below which Records of the Python lola's project
// paths are missing here; Suggest (your home) is offered. Old is safe to print.
type MigrateRootStep struct {
	Old, Suggest string
	Records      int
}

// MigrateTargetStep asks which of Options, the assistants on your PATH, install the Modules
// modules the Python lola installed for Old, which is not installed here or, unless Supported, is
// one lola has no target for. Old is safe to print.
type MigrateTargetStep struct {
	Old       string
	Supported bool
	Modules   int
	Options   []string
}

// TidyOption is one item lola tidy's pickers offer: ID names it to the command, and Label is
// "<target>  <kind>/<name>  <path>[ → <link target>]", an MCP server with its server label only,
// already made safe to print.
type TidyOption struct {
	ID, Label string
}

// TidyPickStep offers lola tidy's candidates. Locked are the items the scan locked, each as its
// label and reason, already made safe to print.
type TidyPickStep struct {
	Options []TidyOption
	Locked  []string
}

// TidyKeepStep offers the keep-list entries for the candidates TidyPick did not choose: each
// option's ID is an escaped literal selector, and its Label the selector and the assistants whose
// items it keeps, already made safe to print. Required is set when nothing was chosen to move:
// an empty answer would then do nothing, so the picker refuses it (Plan 10 §4).
type TidyKeepStep struct {
	Options  []TidyOption
	Required bool
}

// MarketNameStep asks for the name of a marketplace: Why the name it has will not do, Value the
// first free name, pre-filled, and Problem why the last answer was refused ("" on the first ask).
// Every string is safe to print.
type MarketNameStep struct {
	Why, Value, Problem string
}

// ProfileStep offers the profiles a source holds, by name; Invalid counts its files whose names
// are not valid profile names, never shown (Plan 6b §19 S7).
type ProfileStep struct {
	Source  string
	Names   []string
	Invalid int
}

// TrustStep is sync's trust question (Plan 6a §7.5). Changes is the number of changes "Trust
// all" accepts, one per row of the diff, the top-level targets' included. Groups are the entries Choose… offers, one group per kind of change in the
// diff's order, nothing preselected; with none (the top-level targets are the only change) the
// question offers only Trust all and Cancel.
type TrustStep struct {
	Changes int
	Groups  []TrustGroup
}

// TrustGroup is one kind of change in the picker, titled with its count.
type TrustGroup struct {
	Title   string
	Options []TrustOption
}

// TrustOption is one entry the picker offers: Module as the report names it, Label "<glyph>
// <module> <one-line summary>", already made safe to print.
type TrustOption struct {
	Module string
	Label  string
}

// The scope picker's entries (Plan 5c §14), in the order it offers them.
const (
	ScopeProject = "project"
	ScopeUser    = "user"
	ScopeOther   = "other"
)

// ScopeStep asks Question, offering Project (at ProjectRoot), User and Other directory…; One
// says the command takes one of them. Nothing is preselected on the first run; when install's
// Change scope asks again, Preselect is the last answer.
type ScopeStep struct {
	Question    string
	ProjectRoot string
	Home        string
	Preselect   []string
	Problem     string
	One         bool
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

// ModuleStep offers the cached modules, then one group per marketplace with a fetched catalog
// (Plan 6c §6.5), whose options are named by their addresses (@acme/formatter); with neither, the
// prompter offers Add a source… and Quit. Browse offers the market groups alone, without Add a
// source… (lola install @acme). The prompter selects exactly Preselect: nothing on the first run,
// so an installed module is never picked for the user, and the last answer when the step runs
// again.
type ModuleStep struct {
	Options   []ModuleOption
	Markets   []ModuleGroup
	Browse    bool
	Preselect []string
	Problem   string
}

// ModuleGroup is one marketplace in the module picker: Title is "@acme — <description>", and
// each option's Label its entry's name, version, description, tags and source host, already made
// safe to print.
type ModuleGroup struct {
	Title   string
	Options []ModuleOption
}

// MarketEntryStep asks which marketplace entry a bare name means (Plan 6c §3.3): Name is the
// name, which your cache does not hold, and Options the entries your catalogs list under it, one
// per marketplace, named by their addresses; nothing is preselected.
type MarketEntryStep struct {
	Name    string
	Options []ModuleOption
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
func (noPrompter) Confirm(string) (bool, error)                 { return false, errNoTerminal }
func (noPrompter) Profiles(ProfileStep) ([]string, error)       { return nil, errNoTerminal }
func (noPrompter) MarketName(MarketNameStep) (string, error)    { return "", errNoTerminal }
func (noPrompter) MarketEntry(MarketEntryStep) (string, error)  { return "", errNoTerminal }
func (noPrompter) TidyPick(TidyPickStep) ([]string, error)      { return nil, errNoTerminal }
func (noPrompter) TidyKeep(TidyKeepStep) ([]string, error)      { return nil, errNoTerminal }
func (noPrompter) Trust(TrustStep) (engine.TrustAnswer, error) {
	return engine.TrustAnswer{}, errNoTerminal
}
func (noPrompter) MigrateRoot(MigrateRootStep) (string, error) { return "", errNoTerminal }
func (noPrompter) MigrateTargets(MigrateTargetStep) ([]string, error) {
	return nil, errNoTerminal
}
func (noPrompter) Background() (bool, error) { return false, errNoTerminal }
