package cli

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// scopeHelp is the scope rule of the commands that write, for their help (install has its own).
const scopeHelp = "Scope: --project, --user, -C <dir> or --scope; else, on a terminal, lola asks (nothing\n" +
	"preselected); else the project with .lola/ at or above the current directory; else default_scope\n" +
	"in $XDG_CONFIG_HOME/lola/config.yaml; otherwise it refuses to guess."

// scopes resolves the scopes of a command that writes (Plan 6b §3.1): engine.ResolveScope, then,
// on a terminal with no scope flag, the scope picker asking question. both says the command
// acts on two scopes in one run; the others take one, so the picker asks for one and the engine
// refuses --scope both or default_scope: both. refuseHome says the engine refuses a project at
// the home directory for this command (sync), so the picker asks again rather than offer it;
// render and uninstall take one an older lola installed.
func (a *app) scopes(cmd *cobra.Command, eng *engine.Engine, question string, both, refuseHome bool) ([]engine.ScopeChoice, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	p := a.prompter(cmd)
	req, err := a.scopeRequest(cwd, p.Interactive())
	if err != nil {
		return nil, err
	}
	req.Command, req.Both = cmd.Name(), both
	res, err := eng.ResolveScope(req)
	if err != nil {
		return nil, err
	}
	if res.Pick {
		_, scopes, err := (&scopePicker{p: p, eng: eng, cwd: cwd, refuseHome: refuseHome}).pick(res, ScopeStep{Question: question, One: !both})
		return scopes, err
	}
	return res.Scopes, nil
}

// scopePicker asks the scope picker (Plan 5c §14): Project (the project found from cwd, else
// cwd), User and Other directory….
type scopePicker struct {
	p          Prompter
	eng        *engine.Engine
	cwd        string
	other      string // the last Other directory as typed, offered again
	refuseHome bool   // the command refuses a project at the home directory: install and sync
}

// pick asks the picker for res, with step's question and preselection, until the answer names
// at least one scope (exactly one when step.One) and, when sp.refuseHome, no project at the home
// directory, asking for Other directory…'s directory when it is ticked. It returns the entries
// ticked and their scopes: project roots first, then the user scope, as --scope both orders them.
func (sp *scopePicker) pick(res engine.ScopeResolution, step ScopeStep) ([]string, []engine.ScopeChoice, error) {
	step.ProjectRoot, step.Home = untrusted.Text(res.ProjectRoot, untrusted.MaxName), sp.eng.Paths.Home
	home := engine.Scope{Kind: target.Project, ProjectDir: sp.eng.Paths.Home}.Key()
	for {
		entries, err := sp.p.Scope(step)
		if err != nil {
			return nil, nil, err
		}
		switch {
		case len(entries) == 0:
			step.Problem = "pick at least one scope"
			continue
		case step.One && len(entries) > 1:
			step.Problem = "pick one scope"
			continue
		case sp.refuseHome && slices.Contains(entries, ScopeProject) && (engine.Scope{Kind: target.Project, ProjectDir: res.ProjectRoot}).Key() == home:
			step.Problem = "the project here is your home directory; tick User instead"
			continue
		}
		var scopes []engine.ScopeChoice
		if slices.Contains(entries, ScopeProject) {
			scopes = engine.Scopes([]string{config.ScopeProject}, res.ProjectRoot, engine.SourcePicked)
		}
		if slices.Contains(entries, ScopeOther) {
			other, err := sp.otherDirectory(scopes)
			if err != nil {
				return nil, nil, err
			}
			scopes = append(scopes, engine.Scopes([]string{config.ScopeProject}, other, engine.SourcePicked)...)
		}
		if slices.Contains(entries, ScopeUser) {
			scopes = append(scopes, engine.Scopes([]string{config.ScopeUser}, "", engine.SourcePicked)...)
		}
		return entries, scopes, nil
	}
}

// otherDirectory asks for the directory of Other directory… (§14) until the answer is an
// existing directory that is not the project picked above, nor, when sp.refuseHome, the home
// directory (the user scope's root, which install and sync refuse as a project), and returns it
// absolute. A relative answer is
// taken from the current directory; ~ is not expanded. An answer is offered again to edit only
// when it is safe to show.
func (sp *scopePicker) otherDirectory(picked []engine.ScopeChoice) (string, error) {
	home := engine.Scope{Kind: target.Project, ProjectDir: sp.eng.Paths.Home}.Key()
	offer := func(in string) string {
		if untrusted.Text(in, len(in)) != in {
			return ""
		}
		return in
	}
	step := DirectoryStep{Value: sp.other}
	for {
		in, err := sp.p.OtherDirectory(step)
		if err != nil {
			return "", err
		}
		dir := in
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(sp.cwd, dir)
		}
		dir = filepath.Clean(dir)
		key := engine.Scope{Kind: target.Project, ProjectDir: dir}.Key()
		safe := untrusted.Text(dir, untrusted.MaxName)
		fi, err := os.Stat(dir)
		var problem string
		var pathErr *fs.PathError
		switch {
		case in == "":
			problem = "enter the path of a directory"
		case errors.Is(err, fs.ErrNotExist):
			problem = safe + " does not exist"
			if strings.HasPrefix(in, "~") {
				problem += " (~ is not expanded)"
			}
		case errors.As(err, &pathErr):
			problem = safe + ": " + pathErr.Err.Error()
		case err != nil:
			problem = untrusted.Text(err.Error(), untrusted.MaxName)
		case !fi.IsDir():
			problem = safe + " is not a directory"
		case slices.ContainsFunc(picked, func(c engine.ScopeChoice) bool { return c.Scope.Key() == key }):
			problem = "that is the project above"
		case sp.refuseHome && key == home:
			problem = "that is your user scope (~); tick User instead"
		}
		if problem == "" {
			sp.other = offer(in)
			return dir, nil
		}
		step = DirectoryStep{Value: offer(in), Problem: problem}
	}
}
