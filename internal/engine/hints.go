package engine

import (
	"maps"
	"path/filepath"
	"slices"
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
// sc: --user at user scope; at project scope --project when sc is
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

// SyncCommand is every lola sync command a hint, notice or closing line names,
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

// commandOf is the lola command a hint repeating req names, with extra appended: lola sync, lola
// update for an Installed run, for an activation lola profile with req's
// names and the action, or for a move lola profile with the move's words
// (moveArgs); Sync sets those names to the profiles' qualified names once it has resolved them.
// Each carries the scope's flags (Command) and the flags of the run (rerun).
func (e *Engine) commandOf(req SyncRequest, extra ...string) string {
	switch {
	case req.move != nil:
		// A move's own -y answers its "replace it?", and any removals with it.
		args := slices.Clone(req.move.args)
		for _, a := range req.rerun(extra...) {
			if a != "-y" || !slices.Contains(args, "-y") {
				args = append(args, a)
			}
		}
		sc := req.Scope
		if req.move.at != nil {
			sc = *req.move.at
		}
		return e.Command(sc, "profile", args...)
	case req.profileArgs != nil:
		return e.Command(req.Scope, "profile", append(slices.Clone(req.profileArgs), req.rerun(extra...)...)...)
	case req.Installed:
		return e.Command(req.Scope, "update", req.rerun(extra...)...)
	case req.Profiles == nil:
		return e.SyncCommand(req.Scope, req.rerun(extra...)...)
	}
	var args []string
	if req.Profiles.Name != "" {
		args = append(args, req.Profiles.Name)
	}
	args = append(args, "--"+string(req.Profiles.Action))
	if req.Profiles.New { // a run that would make the profile makes it again (newProfiles)
		args = append(append(args, "--new"), newModifiers(*req.Profiles)...)
	}
	return e.Command(req.Scope, "profile", append(args, req.rerun(extra...)...)...)
}

// rerun is the names and flags of the user's sync that a hint repeating it carries: the names, --update (--fetch for lola update, which always updates) and --force as
// given, --trust for the kinds given, each --at, and -y as given: once when extra adds it too, and not when extra makes the run a dry run, which asks
// nothing.
func (req SyncRequest) rerun(extra ...string) []string {
	out := append([]string{}, req.Modules...)
	for _, f := range []struct {
		on   bool
		flag string
	}{{req.Update && !req.Installed, "--update"}, {req.Refetch, "--fetch"}, {req.Options.Force, "--force"}, {req.Options.Trust != 0, req.Options.Trust.Arg()}} {
		if f.on {
			out = append(out, f.flag)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(req.At)) {
		out = append(out, "--at", name+"="+req.At[name])
	}
	if req.Yes && !slices.Contains(extra, "-y") && !slices.Contains(extra, "-n") {
		out = append(out, "-y")
	}
	return append(out, extra...)
}

// again is what a hint that repeats req, or runs another command in its stead, keeps beside
// rerun's flags: -n for a dry run, then the run's output flags (Display).
func (req SyncRequest) again() []string {
	if req.Options.DryRun {
		return append([]string{"-n"}, req.Display...)
	}
	return req.Display
}

// updateFlag is the flag of req that makes it re-pin, which a pin's reason names: --update for
// lola sync, --fetch for lola update given it, and none for lola update, which always re-pins.
func (req SyncRequest) updateFlag() string {
	switch {
	case !req.Installed:
		return "--update"
	case req.Refetch:
		return "--fetch"
	}
	return ""
}

// trusting is req with --trust granting kinds: the run a hint that answers its trust names.
func (req SyncRequest) trusting(kinds TrustKinds) SyncRequest {
	req.Options.Trust = kinds
	return req
}

// ListAll is the command a capped list of sync's output names: the user's
// own run as a dry run under --json, which lists every file and step.
func (e *Engine) ListAll(req SyncRequest) string {
	return e.commandOf(req, "-n", "--json")
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
// user's own: the folder relative to the working directory, written
// ./<folder> so it never reads as an option or a cached name, with the scope's flags and the
// run's output flags, display.
func (e *Engine) installFolder(sc Scope, location string, display ...string) string {
	return e.Command(sc, "install", append([]string{e.pathHere(location)}, display...)...)
}

// pathHere is a local source as the user types it from the working directory: relative to it,
// with ./ before a relative path, so it never reads as a cached name or an option.
func (e *Engine) pathHere(location string) string {
	arg := e.fromHere(location)
	if !filepath.IsAbs(arg) && arg != ".." && !strings.HasPrefix(arg, ".."+string(filepath.Separator)) {
		arg = "." + string(filepath.Separator) + arg
	}
	return arg
}

// reviewFolder is the git command that shows the commits that changed the folder at location,
// relative to the working directory, with ./ before a name starting with '-'.
func (e *Engine) reviewFolder(location string) string {
	arg := untrusted.Text(e.fromHere(location), untrusted.MaxName)
	if strings.HasPrefix(arg, "-") {
		arg = "." + string(filepath.Separator) + arg
	}
	return errs.ShellJoin([]string{"git", "log", "-p", "--", arg})
}
