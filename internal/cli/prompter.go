package cli

import (
	"errors"
	"fmt"

	"github.com/trevor-vaughan/lola-go/internal/engine"
)

// Prompter asks the install flow's questions: one method per step and one
// for the review menu; and sync's questions: the trust picker and yes/no. The
// huh adapter draws them inline on stderr; specs inject a scripted one. A method returns
// ErrAborted when the user cancels. Scope, Modules, Assistants and a Required TidyKeep never
// answer with nothing picked: the huh adapter refuses that in the form. Other
// validation a step needs (one scope, an Other directory that exists) runs in the flow after
// the method returns, which asks again with Problem set. A method whose step's Nav sets Back
// returns ErrBack when the user goes back a screen (Esc).
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
	// Version asks which version of an assistant a scope's files are written for, answering
	// the index of the option picked.
	Version(VersionStep) (int, error)
	Components(ComponentPage) ([]string, error)
	// ConfirmEmpty asks "Install <module> with no components?", default No.
	ConfirmEmpty(module string, nav Nav) (bool, error)
	Review(ReviewStep) (string, error)
	// Trust asks sync's trust question: trust all the changes, choose the
	// entries to trust, or cancel, which is ErrAborted (so is choosing nothing).
	Trust(TrustStep) (engine.TrustAnswer, error)
	// Confirm asks a yes/no question, default No: sync's removals, and the question that closes
	// a flow (tidy's, migrate's), whose nav counts the steps answered before it.
	Confirm(question string, nav Nav) (bool, error)
	// Where asks where a change goes, or which restore lola trash's review
	// runs: Title, one row per option and Cancel last, default the first; it answers the index of
	// the option chosen, ErrCancelled for Cancel.
	Where(WhereStep) (int, error)
	// Profiles asks which of a source's profiles lola profile --add adds: a filterable
	// multi-select, nothing preselected.
	Profiles(ProfileStep) ([]string, error)
	// Members asks a bundle's members (MembersStep), answering those ticked in option order.
	Members(MembersStep) ([]string, error)
	// MarketName asks the name of a marketplace lola market --add adds when the one it has is
	// another marketplace's, or one no marketplace can have, pre-filled with a
	// free one.
	MarketName(MarketNameStep) (string, error)
	// SSHHosts asks which of the ssh hosts a catalog names lola market --add or --update allows: a
	// filterable multi-select, nothing preselected, answering the hosts ticked; none is an answer.
	SSHHosts(SSHHostStep) ([]string, error)
	// MarketEntry asks which marketplace entry a bare name your cache lacks means,
	// answering its address.
	MarketEntry(MarketEntryStep) (string, error)
	// TidyPick asks which of lola tidy's candidates move to the trash: a
	// filterable multi-select, nothing preselected, answering the IDs of the options chosen.
	TidyPick(TidyPickStep) ([]string, error)
	// TidyKeep asks which keep-list entries, for the candidates TidyPick left, to add: a
	// filterable multi-select, nothing preselected, answering the IDs of the options chosen.
	TidyKeep(TidyKeepStep) ([]string, error)
	// TrashPick asks lola trash's picker: the runs, each followed by its
	// files, to tick, then what to do with those ticked, answering the IDs ticked and the action.
	TrashPick(TrashPickStep) (TrashAnswer, error)
	// MigrateRoot asks where a root that missing project paths of the Python lola share is on this
	// machine, offering Suggest: an absolute path, or "" to skip them.
	MigrateRoot(MigrateRootStep) (string, error)
	// MigrateTargets asks which assistants install the records of an assistant this machine
	// lacks: a multi-select over Options, all preselected; none skips them.
	MigrateTargets(MigrateTargetStep) ([]string, error)
	// Background asks whether the terminal's background is dark, for the pickers' colors.
	// lola asks once, when neither config.yaml nor the terminal says, and saves the answer.
	Background() (bool, error)
	// Consent asks a hook or project-script consent question ("Run <m>'s install hooks?"), default
	// No: a plain yes/no on stderr, outside the forms' theme and keys, so neither q nor Esc answers
	// it. Ctrl+C, and end of input in accessible mode, are ErrAborted.
	Consent(question string) (bool, error)
}

// ErrBack is a Prompter's answer when the user goes back a screen (Esc): the flow asks the screen
// before again, its last answer preselected. Only a step whose Nav sets Back returns it.
var ErrBack = engine.ErrBack

// ErrCancelled is a question's Cancel option picked (Where's), as against Ctrl+C: it is
// ErrAborted to a caller that does not tell them apart.
var ErrCancelled = fmt.Errorf("%w", ErrAborted)

// Nav is where a question stands in its flow, for the keys of the full-screen forms: Back says
// there is a screen to go back to, which Esc does (on the first screen Esc says so instead), and
// Answered how many of the flow's steps the user has answered, which q asks before dropping when
// there are two or more. The zero value is a question that stands alone.
type Nav struct {
	Back     bool
	Answered int
}

// MigrateRootStep asks for the new root of Old, below which Records of the Python lola's project
// paths are missing here; Suggest (your home) is offered. Value is the answer pre-filled: Suggest
// on the first ask, the last answer when the user comes back to it. Old is safe to print.
type MigrateRootStep struct {
	Old, Suggest, Value string
	Records             int
	Nav                 Nav
}

// MigrateTargetStep asks which of Options, the assistants on your PATH, install the Modules
// modules the Python lola installed for Old, which is not installed here or, unless Supported, is
// one lola has no target for. Again says the user answered it before and comes back to it:
// Preselect, that answer, is then preselected, none included; otherwise every option is. Old is
// safe to print.
type MigrateTargetStep struct {
	Old       string
	Supported bool
	Modules   int
	Options   []string
	Again     bool
	Preselect []string
	Nav       Nav
}

// TidyOption is one item lola tidy's pickers offer: ID names it to the command, and Label is
// "<target>  <kind>/<name>  <path>[ → <link target>]", an MCP server with its server label only,
// already made safe to print.
type TidyOption struct {
	ID, Label string
}

// TidyPickStep offers lola tidy's candidates. Locked are the items the scan locked, each as its
// label and reason, already made safe to print. Preselect is the last answer when the user comes
// back from the keep step; nothing is preselected at first.
type TidyPickStep struct {
	Options   []TidyOption
	Locked    []string
	Preselect []string
}

// TidyKeepStep offers the keep-list entries for the candidates TidyPick did not choose: each
// option's ID is an escaped literal selector, and its Label the selector and the assistants whose
// items it keeps, already made safe to print. Required is set when nothing was chosen to move:
// an empty answer would then do nothing, so the picker refuses it. Preselect is the last answer
// when the closing question goes back to the picker (nothing at first).
type TidyKeepStep struct {
	Options   []TidyOption
	Required  bool
	Preselect []string
	Nav       Nav
}

// MarketNameStep asks for the name of a marketplace: Why the name it has will not do, Value the
// answer pre-filled (the first free name, or the last answer as typed when it was refused), and
// Problem why the last answer was refused ("" on the first ask).
// Every string is safe to print.
type MarketNameStep struct {
	Why, Value, Problem string
}

// SSHHostStep offers the ssh hosts a catalog names that you have not allowed, each with the entries
// it would admit; nothing is preselected. Nav places it for Esc: it goes back to the name question
// when lola market --add asked one. Strings are raw: the adapter prints them through
// untrusted.Text.
type SSHHostStep struct {
	Market string
	Hosts  []SSHHostChoice
	Nav    Nav
}

// SSHHostChoice is one host SSHHostStep offers, with the number of entries it would admit.
type SSHHostChoice struct {
	Host    string
	Entries int
}

// ProfileStep offers the profiles a source holds, by name; Invalid counts its files whose names
// are not valid profile names, never shown.
type ProfileStep struct {
	Source  string
	Names   []string
	Invalid int
}

// TrustStep is sync's trust question. Changes is the number of changes "Trust
// all" accepts, one per row of the diff, the top-level targets' included. Groups are the entries Choose… offers, one group per kind of change in the
// diff's order, nothing preselected; with none (the top-level targets are the only change) the
// question offers only Trust all and Cancel. Answer is the answer given before, when the question is
// asked again (Esc from the removals question after it): Trust all, or the entries Choose… ticked,
// under the cursor; the zero value puts the cursor on Cancel.
type TrustStep struct {
	Changes int
	Groups  []TrustGroup
	Answer  engine.TrustAnswer
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

// The scope picker's entries, in the order it offers them.
const (
	ScopeProject = "project"
	ScopeUser    = "user"
	ScopeOther   = "other"
)

// Steps numbers a step's fields across one pass of the install flow: its first
// field is step At of Of. The zero value, which every command but install asks with, numbers
// nothing.
type Steps struct{ At, Of int }

// title is the title of the step's i-th field: "Step n of m — t", or t when nothing is numbered.
func (s Steps) title(i int, t string) string {
	if s.Of < 2 {
		return t
	}
	return fmt.Sprintf("Step %d of %d — %s", s.At+i, s.Of, t)
}

// ScopeStep asks Question, offering Project (at ProjectRoot), User and Other directory…; One
// says the command takes one of them. Nothing is preselected on the first run; when install's
// Change scope asks again, Preselect is the last answer.
type ScopeStep struct {
	Question    string
	ProjectRoot string
	Home        string
	// ProjectContext and UserContext are what each scope's context line says after its name
	// (contextFacts), shown in the scope's row so the choice is made with them in view; "" shows
	// none.
	ProjectContext, UserContext string
	Preselect                   []string
	Problem                     string
	One                         bool
	Step                        Steps
	Nav                         Nav
}

// DirectoryStep asks for the Other directory. Value is the answer offered to edit (the last
// one, when it is safe to show); Problem says why the last answer was refused.
type DirectoryStep struct {
	Value   string
	Problem string
	Nav     Nav
}

// ModuleOption is one cached module in the module picker. Label is
// "source · name — description", marked (installed) when installed in a chosen scope, already
// made safe to print.
type ModuleOption struct {
	Name  string
	Label string
}

// ModuleStep offers the cached modules, then one group per marketplace with a fetched catalog,
// whose options are named by their addresses (@acme/formatter); with neither, the prompter offers
// Add a source… and Quit. Browse offers the market groups alone, without Add a source… (lola
// install @acme). The prompter selects exactly Preselect: nothing on the first run, so an installed
// module is never picked for the user, and the last answer when the step runs again.
type ModuleStep struct {
	Options   []ModuleOption
	Markets   []ModuleGroup
	Browse    bool
	Preselect []string
	Problem   string
	Step      Steps
	Nav       Nav
	// Title, when set, replaces "Install which modules?" (lola profile <name> --edit's page names
	// the profile: "profile go › modules"), and None makes an empty answer one: every module
	// unticked.
	Title string
	None  bool
}

// MembersStep asks which profiles a bundle combines: Title
// names the bundle, Options are the profiles it can name, each Name as its file would write it and
// Label as the page shows it (a member that names no profile says so), and Preselect its members
// now; a filterable multi-select, where ticking none is an answer (a bundle of nothing). Nav places
// it for Esc (a new bundle's page goes back to the question before it).
type MembersStep struct {
	Title     string
	Options   []ModuleOption
	Preselect []string
	Nav       Nav
}

// ModuleGroup is one marketplace in the module picker: Title is "@acme — <description>", and
// each option's Label its entry's name, version, description, tags and source host, already made
// safe to print.
type ModuleGroup struct {
	Title   string
	Options []ModuleOption
}

// MarketEntryStep asks which marketplace entry a bare name means: Name is the
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
	Step      Steps
	Nav       Nav
}

// VersionStep asks which version of Target a scope's files are written for: Options are its eras'
// answers, newest first, each its label (Labels) and what it changes, and Preselect the index of the
// one under the cursor, -1 for none: with Last the last answer, else the one Why says ("matches
// installed 2.2.5", "as declared"). Note says which version is installed, and Scope names the scope
// when the install has more than one ("" otherwise). Again says the review's Change versions asks
// it, alone: it is titled so and not numbered.
type VersionStep struct {
	Target    string
	Options   []string
	Labels    []string
	Preselect int
	Last      bool
	Why       string
	Note      string
	Scope     string
	Again     bool
	Step      Steps
	Nav       Nav
}

// ComponentOption is one component of a page. Label is "name — first line of description",
// with "(runs a command)" or "(skipped in <where the picks that skip it live>)" as they apply.
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
// preselections are equal. Footer is the read-only lines under the lists (runtime hooks, the
// only: list in force), one per line, or "". Step numbers its lists, the disabled ones left out.
type ComponentPage struct {
	Module string
	Title  string
	Groups []ComponentGroup
	Footer string
	Step   Steps
	Nav    Nav
}

// lists is how many lists the page asks: one per group, the disabled ones left out.
func (pg ComponentPage) lists() int {
	n := 0
	for _, g := range pg.Groups {
		if !g.Disabled {
			n++
		}
	}
	return n
}

// Review menu answers.
const (
	ReviewApply      = "apply"
	ReviewScope      = "scope"
	ReviewModules    = "modules"
	ReviewAssistants = "assistants"
	ReviewVersions   = "versions"
	ReviewComponents = "components"
	ReviewQuit       = "quit"
)

// ReviewStep is the install's last step: Summary, what the run will do, already made
// safe to print, and Options, the answers it offers, in order: Apply, a Change entry for each step
// the flags left open, and Quit.
type ReviewStep struct {
	Summary string
	Options []string
	Step    Steps
	Nav     Nav
}

// WhereStep is a question about where a change goes, or which restore lola trash runs: its title,
// its answers' labels, Preselect, the answer under the cursor (the last one, when asked again),
// and Nav, where it is in its command's flow.
type WhereStep struct {
	Title     string
	Options   []string
	Preselect int
	Nav       Nav
}

// lola trash's picker actions (TrashAnswer.Action).
const (
	TrashRestore = "restore" // Restore ticked
	TrashEmpty   = "empty"   // Empty ticked runs
)

// TrashPickStep offers lola trash's runs, newest first, each one's [whole run] row followed by its
// files' rows, indented: a run's row ticked takes the whole run, whatever of its files is ticked
// (huh shows its files unticked). Labels are already made safe to print. Preselect and Action
// are the last answer when the picker asks again (nothing and "" at first, which is Restore), and
// Problem why it does ("" at first).
type TrashPickStep struct {
	Options   []TidyOption
	Preselect []string
	Action    string
	Problem   string
}

// TrashAnswer is the trash picker's answer: the IDs of the rows ticked, and TrashRestore or
// TrashEmpty.
type TrashAnswer struct {
	Picked []string
	Action string
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
func (noPrompter) Version(VersionStep) (int, error)             { return 0, errNoTerminal }
func (noPrompter) Components(ComponentPage) ([]string, error)   { return nil, errNoTerminal }
func (noPrompter) ConfirmEmpty(string, Nav) (bool, error)       { return false, errNoTerminal }
func (noPrompter) Review(ReviewStep) (string, error)            { return "", errNoTerminal }
func (noPrompter) Confirm(string, Nav) (bool, error)            { return false, errNoTerminal }
func (noPrompter) Where(WhereStep) (int, error)                 { return 0, errNoTerminal }
func (noPrompter) Profiles(ProfileStep) ([]string, error)       { return nil, errNoTerminal }
func (noPrompter) MarketName(MarketNameStep) (string, error)    { return "", errNoTerminal }
func (noPrompter) SSHHosts(SSHHostStep) ([]string, error)       { return nil, errNoTerminal }
func (noPrompter) MarketEntry(MarketEntryStep) (string, error)  { return "", errNoTerminal }
func (noPrompter) TidyPick(TidyPickStep) ([]string, error)      { return nil, errNoTerminal }
func (noPrompter) Members(MembersStep) ([]string, error)        { return nil, errNoTerminal }
func (noPrompter) TidyKeep(TidyKeepStep) ([]string, error)      { return nil, errNoTerminal }
func (noPrompter) TrashPick(TrashPickStep) (TrashAnswer, error) { return TrashAnswer{}, errNoTerminal }
func (noPrompter) Trust(TrustStep) (engine.TrustAnswer, error) {
	return engine.TrustAnswer{}, errNoTerminal
}
func (noPrompter) MigrateRoot(MigrateRootStep) (string, error) { return "", errNoTerminal }
func (noPrompter) MigrateTargets(MigrateTargetStep) ([]string, error) {
	return nil, errNoTerminal
}
func (noPrompter) Background() (bool, error)    { return false, errNoTerminal }
func (noPrompter) Consent(string) (bool, error) { return false, errNoTerminal }
