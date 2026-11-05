// Package cli is lola's cobra wiring: command groups, kref-style help and aliases, output and
// error contracts, and prompts. Behavior lives in engine; this package only adapts it.
package cli

import (
	"errors"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"sync"

	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// version is stamped at build time with -ldflags "-X .../internal/cli.version=<v>".
var version = "dev"

// ErrAborted means the user declined or cancelled a prompt: exit 0, "aborted; nothing
// changed." A Prompter returns it for Ctrl+C, end of input in accessible mode, and Quit.
var ErrAborted = errors.New("aborted")

// abortedError is a cancelled prompt whose command says what the cancel left as it was: exit 0,
// with its message printed in place of "aborted; nothing changed.".
type abortedError struct{ msg string }

func (e *abortedError) Error() string        { return e.msg }
func (e *abortedError) Is(target error) bool { return target == ErrAborted }

// quitError is a quit the user asked for with q, as against Ctrl+C or a No: exit 0, "✗ cancelled —
// nothing was changed", in color when the forms that took the q are. A command that words its own
// cancel (abortedError) keeps its wording for a quit too, as it says what the cancel left as it was.
type quitError struct{ color bool }

func (e *quitError) Error() string        { return "cancelled — nothing was changed" }
func (e *quitError) Is(target error) bool { return target == ErrAborted }

// Command group ids, in display order.
const (
	groupCore       = "core"
	groupStore      = "store"
	groupAdditional = "additional"
)

// app holds per-invocation state shared by every command. newPrompter builds the Prompter
// the install flow asks.
type app struct {
	getenv      func(string) string
	json        bool
	plain       bool
	scopeFlag   string   // --scope
	user        bool     // --user
	project     bool     // --project
	root        string   // -C/--root
	noPager     bool     // --no-pager
	display     []string // the run's output flags (outputFlags), which engine() gives the engine as Engine.Display
	termOut     bool     // stdout is a terminal whatever it is (WithTerminals)
	termErr     bool     // stderr is a terminal whatever it is (WithTerminals)
	eng         *engine.Engine
	newPrompter func(*cobra.Command) Prompter
	// queryBackground asks the terminal for its background color; nil is terminalBackground.
	// Specs fake the terminal's answer with it.
	queryBackground func(*cobra.Command) (color.Color, error)
}

// Option configures the command tree.
type Option func(*app)

// WithPrompter makes install ask p instead of the terminal; specs script the install flow
// with it.
func WithPrompter(p Prompter) Option {
	return func(a *app) { a.newPrompter = func(*cobra.Command) Prompter { return p } }
}

// WithTerminals makes lola treat stdout and stderr as terminals when out and errw say so, which
// decides paging; specs page through a fake pager, or the viewer, with it.
func WithTerminals(out, errw bool) Option {
	return func(a *app) { a.termOut, a.termErr = out, errw }
}

var cobraOnce sync.Once

// setupCobra sets cobra's process-wide globals exactly once (§4.2).
func setupCobra() {
	cobraOnce.Do(func() {
		cobra.EnableCommandSorting = false
		cobra.AddTemplateFunc("aliasedPad", aliasedPad)
		cobra.AddTemplateFunc("aliasedFlagUsages", aliasedFlagUsages)
	})
}

// NewRootCmd builds the full command tree. getenv is injected so tests control the
// environment; nothing reads os.Getenv directly.
func NewRootCmd(getenv func(string) string, opts ...Option) *cobra.Command {
	setupCobra()
	a := &app{getenv: getenv}
	for _, o := range opts {
		o(a)
	}
	// lola --version is an alias of lola version, on the root alone; with arguments the root
	// refuses them as the unknown command they are.
	var showVersion bool
	runRoot := func(cmd *cobra.Command, args []string) error {
		if showVersion && len(args) == 0 {
			return a.printVersion(cmd)
		}
		return a.groupRunE(cmd, args)
	}
	root := &cobra.Command{
		Use:   "lola",
		Short: "Install AI-assistant skills, commands, agents, MCP servers and instructions",
		Long: "lola installs a module once and renders it into each assistant's native layout,\n" +
			"tracking exactly what it wrote so it can render again or uninstall cleanly.",
		Example:           "  lola install ./my-module -t claude-code\n  lola list\n  lola uninstall my-module -y",
		SilenceErrors:     true,
		SilenceUsage:      true,
		Args:              cobra.ArbitraryArgs,
		RunE:              runRoot,
		ValidArgsFunction: aliasCompletions,
		// cobra only defaults this inside its own lookup; groupRunE calls SuggestionsFor directly.
		SuggestionsMinimumDistance: 2,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if a.json && a.plain {
				return errs.WithHints(errors.New("--json and --plain cannot be combined"), "pick one output format")
			}
			a.display = outputFlags(cmd)
			return nil
		},
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.Flags().BoolVar(&showVersion, "version", false, "print lola's version: lola version")
	pf := root.PersistentFlags()
	pf.BoolVar(&a.json, "json", false, "print machine-readable JSON")
	pf.BoolVar(&a.plain, "plain", false, "print tab-separated lines without color")
	pf.StringVar(&a.scopeFlag, "scope", "", "act on this scope: project, user or both (also project,user)")
	_ = root.RegisterFlagCompletionFunc("scope", cobra.FixedCompletions([]string{"project", "user", "both"}, cobra.ShellCompDirectiveNoFileComp))
	pf.BoolVar(&a.project, "project", false, "act on project scope: --scope project")
	pf.BoolVar(&a.user, "user", false, "act on user scope (your home directory): --scope user")
	pf.StringVarP(&a.root, "root", "C", "", "the project at this directory; implies project scope")
	pf.BoolVar(&a.noPager, "no-pager", false, "print without lola's viewer or a pager")

	root.AddGroup(
		&cobra.Group{ID: groupCore, Title: "Core:"},
		&cobra.Group{ID: groupStore, Title: "Cache:"},
		&cobra.Group{ID: groupAdditional, Title: "Additional:"},
	)
	a.addCoreCommands(root)
	a.addStoreCommands(root)
	a.addMiscCommands(root)
	a.installHelp(root)
	checkFlagAliases(root)
	return root
}

// engine returns the engine, resolving XDG paths on first use.
func (a *app) engine() (*engine.Engine, error) {
	if a.eng == nil {
		p, err := paths.Resolve(a.getenv)
		if err != nil {
			return nil, err
		}
		a.eng = engine.New(p)
		a.eng.Display = a.display
	}
	return a.eng, nil
}

// scopeRequest is what the scope flags say, for the engine to resolve from cwd: -C/--root
// becomes an absolute directory, which must exist.
func (a *app) scopeRequest(cwd string, interactive bool) (engine.ScopeRequest, error) {
	req := engine.ScopeRequest{Scope: a.scopeFlag, User: a.user, Project: a.project, Cwd: cwd, Interactive: interactive}
	if a.root == "" {
		return req, nil
	}
	dir, err := filepath.Abs(a.root)
	if err != nil {
		return req, err
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return req, fmt.Errorf("-C %s: not a directory", untrusted.Text(a.root, untrusted.MaxName))
	}
	req.ProjectDir = dir
	return req, nil
}

// scope is the one scope of a command that never asks for it: the scope flags, which must name
// one scope, else the nearest parent with .lola/, else the current directory.
func (a *app) scope(cmd *cobra.Command, eng *engine.Engine) (engine.Scope, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return engine.Scope{}, err
	}
	if a.scopeFlag == "" && !a.user && !a.project && a.root == "" {
		return engine.Scope{Kind: target.Project, ProjectDir: engine.FindProject(cwd)}, nil
	}
	req, err := a.scopeRequest(cwd, false)
	if err != nil {
		return engine.Scope{}, err
	}
	req.Command = cmd.Name()
	res, err := eng.ResolveScope(req)
	if err != nil {
		return engine.Scope{}, err
	}
	return res.Scopes[0].Scope, nil
}

// Execute runs root and maps the outcome to an exit code: 0 on success or a cancelled
// prompt, 1 on any error (printed per the output contract).
func Execute(root *cobra.Command) int {
	err := root.Execute()
	var quit *quitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &quit):
		fmt.Fprintln(root.ErrOrStderr(), style{color: quit.color}.bad(quit.Error()))
		return 0
	case errors.As(err, new(*abortedError)):
		fmt.Fprintln(root.ErrOrStderr(), err.Error())
		return 0
	case errors.Is(err, ErrAborted):
		fmt.Fprintln(root.ErrOrStderr(), "aborted; nothing changed.")
		return 0
	}
	// "json" is registered on the root by NewRootCmd, so GetBool only fails if that registration
	// is removed; report errors in the human format rather than hide the real error behind it.
	asJSON, flagErr := root.PersistentFlags().GetBool("json")
	if flagErr != nil {
		asJSON = false
	}
	writeError(root.ErrOrStderr(), err, asJSON)
	return 1
}

// groupRunE makes a command group fail on unknown subcommands instead of printing help and
// exiting 0, and print help when run bare.
func (a *app) groupRunE(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		a.showHelp(cmd, !isTTY(cmd.OutOrStdout()))
		return nil
	}
	hints := []string{"see: " + cmd.CommandPath() + " --help"}
	if s := cmd.SuggestionsFor(args[0]); len(s) > 0 {
		hints = append([]string{"did you mean: " + cmd.CommandPath() + " " + s[0]}, hints...)
	}
	return errs.WithHints(fmt.Errorf("unknown command %q for %q", args[0], cmd.CommandPath()), hints...)
}
