package engine

import (
	"path/filepath"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// FoundHere reports whether sc is the project a lola command run from the working directory
// (Getwd) finds: the nearest parent with .lola/, else the directory itself.
func (e *Engine) FoundHere(sc Scope) bool {
	cwd, err := e.Getwd()
	return err == nil && sc.Kind == target.Project && (Scope{Kind: target.Project, ProjectDir: FindProject(cwd)}).Key() == sc.Key()
}

// RootArgs are the flags that point a lola command run from the working directory (Getwd) at
// sc (Plan 6a §11.3, Plan 6b §12.3): --user at user scope; at project scope --project when sc is
// the project found from there, else -C <root>. A working directory lola cannot read gets -C,
// which works from anywhere. A command without a scope flag would ask for its scope on a
// terminal, so every hint carries one.
func (e *Engine) RootArgs(sc Scope) []string {
	switch {
	case sc.Kind != target.Project:
		return []string{"--user"}
	case e.FoundHere(sc):
		return []string{"--project"}
	}
	return []string{"-C", untrusted.Text(sc.ProjectDir, untrusted.MaxName)}
}

// SyncCommand is every lola sync command a hint, notice or closing line names (Plan 6a §11.3),
// built by Command.
func (e *Engine) SyncCommand(sc Scope, args ...string) string { return e.Command(sc, "sync", args...) }

// Command is a lola command a hint names for sc: lola, the subcommand, the scope's flags
// (RootArgs), then args, each through untrusted.Text and quoted for the user's shell, so the
// command acts on the scope the user ran from where they are.
func (e *Engine) Command(sc Scope, sub string, args ...string) string {
	cmd := append([]string{"lola", sub}, e.RootArgs(sc)...)
	for _, a := range args {
		cmd = append(cmd, untrusted.Text(a, untrusted.MaxName))
	}
	return errs.ShellJoin(cmd)
}

// rerun is the names and flags of the user's sync that a hint repeating it carries (Plan 6a
// §11.3): the names, --update and --force as given, and the hook flags only when given.
func (req SyncRequest) rerun(extra ...string) []string {
	out := append([]string{}, req.Modules...)
	for _, f := range []struct {
		on   bool
		flag string
	}{{req.Update, "--update"}, {req.Options.Force, "--force"}, {req.Options.AllowHooks, "--allow-hooks"},
		{req.Options.AllowRuntimeHooks, "--allow-runtime-hooks"}} {
		if f.on {
			out = append(out, f.flag)
		}
	}
	return append(out, extra...)
}

// ListAll is the command a capped list of sync's output names (Plan 6a §7.5, §11.3): the user's
// own run as a dry run under --json, which lists every file and step.
func (e *Engine) ListAll(req SyncRequest) string {
	return e.SyncCommand(req.Scope, req.rerun("-n", "--json")...)
}

// fromHere is location as a user in the working directory (Getwd) types it: relative to it,
// else the location itself when lola cannot tell.
func (e *Engine) fromHere(location string) string {
	cwd, err := e.Getwd()
	if err != nil {
		return location
	}
	rel, err := filepath.Rel(canonicalRoot(cwd), location)
	if err != nil {
		return location
	}
	return rel
}

// installFolder is the lola install command that records the folder module at location as the
// user's own (Plan 6a §7.5, §10.3): the folder relative to the working directory, written
// ./<folder> so it never reads as an option or a cached name, with the scope's flags.
func (e *Engine) installFolder(sc Scope, location string) string {
	arg := e.fromHere(location)
	if !filepath.IsAbs(arg) && arg != ".." && !strings.HasPrefix(arg, ".."+string(filepath.Separator)) {
		arg = "." + string(filepath.Separator) + arg
	}
	return e.Command(sc, "install", arg)
}

// reviewFolder is the git command that shows the commits that changed the folder at location
// (Plan 6a §7.5), relative to the working directory, with ./ before a name starting with '-'.
func (e *Engine) reviewFolder(location string) string {
	arg := untrusted.Text(e.fromHere(location), untrusted.MaxName)
	if strings.HasPrefix(arg, "-") {
		arg = "." + string(filepath.Separator) + arg
	}
	return errs.ShellJoin([]string{"git", "log", "-p", "--", arg})
}
