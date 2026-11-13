package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// profileMove runs lola profile --move: the target's scope from the scope
// flags (the found project, or user scope), the source layer from the one name given, and the
// move with the run's questions: replace it (Confirm), removals (sync's), trust (sync's).
func (a *app) profileMove(cmd *cobra.Command, eng *engine.Engine, args, modules []string, to string, keepOff, switchTo bool, f syncFlags) error {
	if switchTo && keepOff {
		return errs.WithHints(errors.New("--switch and --keep-off cannot be combined; pick one"),
			"keep --switch: "+runWithout(cmd, args, "keep-off"), "keep --keep-off: "+runWithout(cmd, args, "switch"))
	}
	if len(modules) == 0 {
		return errors.New("--move needs the modules to move")
	}
	if len(args) > 1 {
		return errs.WithHints(errors.New("--move takes one source layer, named first; the modules go after --move"),
			runWithout(cmd, args[:1], "move")+" --move "+strings.Join(append(slices.Clone(modules), args[1:]...), ","))
	}
	sc, other, err := a.moveScopes(cmd, eng, args)
	if err != nil {
		return err
	}
	a.printContext(cmd, eng, []engine.Scope{sc}, &engine.TargetRequest{Profile: to})
	p := a.prompter(cmd)
	opts := engine.Options{DryRun: f.dry, Force: f.force, Display: outputFlags(cmd), ForceRun: forceRun(cmd, args), RenderRun: renderRun(cmd, args), AskWhere: a.askWhere(p, nil),
		Target: engine.TargetRequest{
			Rerun: func(extra ...string) string {
				return strings.Join(append([]string{runWithout(cmd, args, append([]string{"to"}, consentFlags...)...)}, extra...), " ")
			},
			UserRun: func() string {
				return runWithout(cmd, args, slices.Concat(scopeFlags, consentFlags)...) + " --user"
			},
		}}
	opts.Trust, opts.Receipt = f.trust, a.receipt(cmd, f.trust, f.yes)
	a.hookOptions(cmd, &opts)
	req := engine.MoveRequest{Scope: sc, Other: other, Across: a.namesBothScopes(), Modules: modules, To: to, Switch: switchTo, KeepOff: keepOff, Yes: f.yes, Options: opts}
	req.Source = func(o engine.Scope) { a.addContext(cmd, eng, o, nil) } // the source's file is the move's to name
	if len(args) == 1 {
		req.From = args[0]
	}
	// Trust is a project's: its diff names the project's base, the source's in a move from one.
	name, run := manifestName(sc), "lola profile"
	if other != nil && sc.Kind == target.User {
		name = manifestName(*other)
	}
	diff := func(rep engine.TrustReport) func(io.Writer, style) {
		return func(w io.Writer, s style) { printTrustDiff(w, s, name, run, rep, "") }
	}
	var asked bool
	req.Show, req.Trust, req.ConfirmRemovals, _ = a.syncQuestions(cmd, f, true, "Remove these and move? No applies nothing", Nav{}, diff, &asked)
	if !a.json && p.Interactive() {
		req.Replace = func(module, label string) (bool, error) {
			return askYes(p, Question{Prompt: untrusted.Text(label, untrusted.MaxName) + " lists " + untrusted.Text(module, untrusted.MaxName) + " already. Replace its entry?"})
		}
	}
	res, err := eng.Move(cmd.Context(), req)
	if _, ok := errors.AsType[*engine.MoveSourceError](err); ok {
		// The name before --move was meant as a module: --move a b reads b as the source.
		err = errs.WithHints(err, "move them all: "+runWithout(cmd, nil, "move")+" --move "+strings.Join(append(slices.Clone(modules), args...), ","))
	}
	if flagErr, ok := errors.AsType[*engine.MoveFlagError](err); ok {
		err = errs.WithHints(err, "drop it: "+runWithout(cmd, args, flagErr.Flag))
	}
	stderr := style{color: a.color(cmd.ErrOrStderr())}
	if refusal, ok := errors.AsType[*engine.TrustRefusalError](err); ok && !a.json {
		printSyncRefusal(cmd.ErrOrStderr(), style{color: stderr.color, plain: a.plain}, name, run, refusal, "")
	}
	declined, isDeclined := errors.AsType[*engine.RemovalsDeclinedError](err)
	aborted := errors.Is(err, engine.ErrDeclined) || errors.Is(err, ErrAborted)
	switch {
	case (isDeclined || aborted) && other != nil:
		// A move between scopes asks before it writes either, so a No leaves both as they were.
		what := "removals declined; "
		if !isDeclined {
			what = "cancelled — "
		}
		return &abortedError{what + "user scope and the project were not changed"}
	case isDeclined:
		return removalsDeclined(eng, sc, declined)
	case aborted:
		return syncAborted(sc)
	}
	if err != nil {
		return err
	}
	if asked && res.Syncs[0].Equivalent != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), stderr.info("the same off a terminal: "+res.Syncs[0].Equivalent))
	}
	// A move that switched to its target ends, as a switch does, with the scope's new context.
	now := ""
	if act := res.Syncs[0].Activation; act != nil && act.Turned && !res.Syncs[0].DryRun {
		now = contextLine(eng.Context(sc, nil))
	}
	return a.emit(cmd, func(w io.Writer, s style) { printMove(w, s, res, now) }, res)
}

// namesBothScopes reports whether a move's scope flags name both scopes: --user, the target's,
// and -C, the project's the modules come from, with no other scope flag (which the scopes refuse
// beside --user).
func (a *app) namesBothScopes() bool {
	return a.user && a.root != "" && a.scopeFlag == "" && !a.project
}

// moveScopes is the target scope of lola profile --move and the other scope its modules may come
// from: with --user and -C, user scope and that project, which must be one
// (ResolveScope's project, with .lola/, not your home directory); else the scope the flags or the
// picker name, and the other: user scope from a project, the project found from here, when it has
// .lola/ and is not your home directory, from user scope.
func (a *app) moveScopes(cmd *cobra.Command, eng *engine.Engine, args []string) (engine.Scope, *engine.Scope, error) {
	user := engine.Scope{Kind: target.User}
	if !a.namesBothScopes() {
		scs, err := a.scopes(cmd, eng, args, "Move where?", false, true)
		if err != nil {
			return engine.Scope{}, nil, err
		}
		sc := scs[0].Scope
		if sc.Kind == target.Project {
			return sc, &user, nil
		}
		cwd, err := os.Getwd()
		if err != nil {
			return sc, nil, nil // no project is found from here
		}
		found := engine.Scope{Kind: target.Project, ProjectDir: engine.FindProject(cwd)}
		if fi, err := os.Stat(filepath.Join(found.ProjectDir, ".lola")); err != nil || !fi.IsDir() || eng.IsHome(found) {
			return sc, nil, nil
		}
		return sc, &found, nil
	}
	// --user names the target scope, -C the project the modules come from, which ResolveScope
	// resolves as -C alone; its refusal of --user with -C is for the commands with one scope.
	cwd, err := os.Getwd()
	if err != nil {
		return engine.Scope{}, nil, err
	}
	sreq, err := a.scopeRequest(cwd, false)
	if err != nil {
		return engine.Scope{}, nil, err
	}
	sreq.User, sreq.Command = false, cmd.Name()
	res, err := eng.ResolveScope(sreq)
	if err != nil {
		return engine.Scope{}, nil, err
	}
	proj := res.Scopes[0].Scope
	shown := untrusted.Text(a.root, untrusted.MaxName)
	switch {
	case eng.IsHome(proj):
		return engine.Scope{}, nil, fmt.Errorf("-C %s is your home directory, where user scope installs, not a project to move from", shown)
	case !res.Found:
		return engine.Scope{}, nil, fmt.Errorf("-C %s has no .lola/, so it is no project to move from", shown)
	}
	return user, &proj, nil
}

// printMove prints a move: a new profile it created, each write, then each scope's sync (the
// target scope's first, each named when there are two), after the profile it turned on (a dry
// run's too), then now: and now, the scope's context after a switch, when it is set. --plain
// prints an edit row per write (action, module, profile, path) and no now: line.
func printMove(w io.Writer, s style, res *engine.MoveResult, now string) {
	if res.Created != "" && !s.plain {
		fmt.Fprintln(w, s.ok("new profile "+strings.TrimSuffix(filepath.Base(res.Created), ".yml")+"  ("+res.Created+")"))
	}
	printEdits(w, s, res.Edits, len(res.Syncs) > 0 && res.Syncs[0].DryRun)
	for _, r := range res.Syncs {
		if len(res.Syncs) > 1 && !s.plain { // a move between scopes: whose plan each is
			where := "user scope:"
			if r.Scope != target.User.String() {
				where = "project " + untrusted.Text(r.Root, untrusted.MaxName) + ":"
			}
			fmt.Fprintln(w, s.paint("36", where)) // as the context line
		}
		if r.Activation != nil && r.Turned && !s.plain {
			fmt.Fprintln(w, turnedLine(r.Activation))
		}
		name := manifestName(engine.Scope{Kind: target.Project})
		if r.Scope == target.User.String() {
			name = manifestName(engine.Scope{Kind: target.User})
		}
		printSync(w, s, name, r, nil, "")
	}
	if now != "" && !s.plain {
		fmt.Fprintln(w, "now: "+now)
	}
}
