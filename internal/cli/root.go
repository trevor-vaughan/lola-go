// Package cli is lola's cobra wiring: command groups, kref-style help and aliases, output and
// error contracts, and prompts. Behavior lives in engine; this package only adapts it.
package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
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
	user        bool
	project     string
	eng         *engine.Engine
	newPrompter func(*cobra.Command) Prompter
}

// Option configures the command tree.
type Option func(*app)

// WithPrompter makes install ask p instead of the terminal; specs script the install flow
// with it.
func WithPrompter(p Prompter) Option {
	return func(a *app) { a.newPrompter = func(*cobra.Command) Prompter { return p } }
}

var cobraOnce sync.Once

// setupCobra sets cobra's process-wide globals exactly once (§4.2).
func setupCobra() {
	cobraOnce.Do(func() {
		cobra.EnableCommandSorting = false
		cobra.AddTemplateFunc("aliasedPad", aliasedPad)
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
	root := &cobra.Command{
		Use:   "lola",
		Short: "Install AI-assistant skills, commands, agents, MCP servers and instructions",
		Long: "lola installs a module once and renders it into each assistant's native layout,\n" +
			"tracking exactly what it wrote so it can render again or uninstall cleanly.",
		Example:           "  lola install ./my-module -t claude-code\n  lola list\n  lola uninstall my-module -y",
		SilenceErrors:     true,
		SilenceUsage:      true,
		Args:              cobra.ArbitraryArgs,
		RunE:              groupRunE,
		ValidArgsFunction: aliasCompletions,
		// cobra only defaults this inside its own lookup; groupRunE calls SuggestionsFor directly.
		SuggestionsMinimumDistance: 2,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if a.json && a.plain {
				return errs.WithHints(errors.New("--json and --plain cannot be combined"), "pick one output format")
			}
			if a.user && a.project != "" {
				return errs.WithHints(errors.New("--user cannot be combined with -C/--project"), "user scope has no project directory")
			}
			return nil
		},
	}
	root.CompletionOptions.DisableDefaultCmd = true
	pf := root.PersistentFlags()
	pf.BoolVar(&a.json, "json", false, "print machine-readable JSON")
	pf.BoolVar(&a.plain, "plain", false, "print tab-separated lines without color")
	pf.StringVarP(&a.project, "project", "C", "", "project root; without it, every command but install uses the nearest parent with .lola/, else the current directory (install: see lola help install)")
	pf.BoolVar(&a.user, "user", false, "act on user scope (your home directory) instead of a project")

	root.AddGroup(
		&cobra.Group{ID: groupCore, Title: "Core:"},
		&cobra.Group{ID: groupStore, Title: "Cache:"},
		&cobra.Group{ID: groupAdditional, Title: "Additional:"},
	)
	a.addCoreCommands(root)
	a.addStoreCommands(root)
	a.addMiscCommands(root)
	installHelp(root)
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
	}
	return a.eng, nil
}

// projectDir returns -C as an absolute directory, or "" when it was not given.
func (a *app) projectDir() (string, error) {
	if a.project == "" {
		return "", nil
	}
	dir, err := filepath.Abs(a.project)
	if err != nil {
		return "", err
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("-C %s: not a directory", a.project)
	}
	return dir, nil
}

// scope resolves --user and -C into an engine scope. install resolves its scopes itself
// (installScopes); every other command acts where lola already installed something.
func (a *app) scope() (engine.Scope, error) {
	if a.user {
		return engine.Scope{Kind: target.User}, nil
	}
	if a.project != "" {
		dir, err := a.projectDir()
		if err != nil {
			return engine.Scope{}, err
		}
		return engine.Scope{Kind: target.Project, ProjectDir: dir}, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return engine.Scope{}, err
	}
	return engine.Scope{Kind: target.Project, ProjectDir: engine.FindProject(cwd)}, nil
}

// Execute runs root and maps the outcome to an exit code: 0 on success or a cancelled
// prompt, 1 on any error (printed per the output contract).
func Execute(root *cobra.Command) int {
	err := root.Execute()
	switch {
	case err == nil:
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
func groupRunE(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		renderHelp(cmd, !isTTY(cmd.OutOrStdout()))
		return nil
	}
	hints := []string{"see: " + cmd.CommandPath() + " --help"}
	if s := cmd.SuggestionsFor(args[0]); len(s) > 0 {
		hints = append([]string{"did you mean: " + cmd.CommandPath() + " " + s[0]}, hints...)
	}
	return errs.WithHints(fmt.Errorf("unknown command %q for %q", args[0], cmd.CommandPath()), hints...)
}
