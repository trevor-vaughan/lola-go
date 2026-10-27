package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/consent"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Install script sources (Plan 9 §5.1), in the order a pre-install phase runs them.
const (
	ScriptUser    = "user"    // config.yaml's pre_install and post_install
	ScriptProject = "project" // the project's project.yml; needs --trust=scripts
	ScriptFlag    = "flag"    // install's --pre-install and --post-install
)

// FlagScript is an install --pre-install or --post-install script: Given as the user wrote it,
// which messages name, and Path, that made absolute when the flag was parsed; Given "" for none.
type FlagScript struct{ Given, Path string }

// installScript is one install script a run plans (Plan 9 §5): its event and source, the path as
// configured (shown through untrusted.Text) and the absolute, resolved path it runs from (A1).
// where is how a refusal of it names it: "<file>:<line>: <key>: " for a configured one, the flag
// for a flag's.
type installScript struct {
	event, source string
	shown, path   string
	where         string
	sha256        string // hex, taken when the run planned it
	// declined says the run had no trust for it: it is listed where it would run, and not run.
	declined bool
	// root is the canonical project root a project's script is consented under and shown beside
	// (S1), as it is: output shows it through oneLine; "" for others.
	root string
}

// ScriptQuestion is a project's install script the user is asked to trust (Plan 9 §5.4): its event,
// its path from the canonical project root Root, and its sha256. Later says the run writes no
// copy for it to wrap: a yes trusts it for later runs, and nothing runs now.
type ScriptQuestion struct {
	Event, Path, Root, SHA256 string
	Later                     bool
}

// ScriptRun is one install script for one module and target in an operation's result (Plan 9
// §5.6): Path as configured, through untrusted.Text; Status one of the hook statuses.
type ScriptRun struct {
	Event  string `json:"event"`
	Source string `json:"source"`
	Path   string `json:"path"`
	Module string `json:"module"`
	Target string `json:"target"`
	Status string `json:"status"`
	// untrusted says a dry run lists a project's script the run has no trust for: run without -n,
	// it would ask, or need --trust=scripts off a terminal. root is a project script's canonical
	// project root, which human output shows beside its path (S1).
	untrusted bool
	root      string
}

// NeedsTrust reports whether a dry run lists s without the trust the run would need for it.
func (s ScriptRun) NeedsTrust() bool { return s.untrusted }

// Root is the canonical root of the project whose script s is, "" for another source's.
func (s ScriptRun) Root() string { return s.root }

// installScripts is the install scripts that wrap the module copies of a run of sc, each checked
// and hashed: the user's from config.yaml, the project's from project.yml only when sc is that
// project (A9), and the flag's from opts, in run order (§5.3): pre-install user, project, flag;
// post-install flag, project, user. A script that does not exist, is not a regular file or is not
// executable is refused, naming where it is configured; a project's that resolves outside the
// project or is a symbolic link too.
func (e *Engine) installScripts(sc Scope, opts Options) ([]installScript, error) {
	cfg, err := config.LoadConfig(e.Paths.Config, e.Paths.Home)
	if err != nil {
		return nil, err
	}
	var out []installScript
	for _, s := range []struct {
		event string
		c     config.Script
	}{{PreInstall, cfg.PreInstall}, {PostInstall, cfg.PostInstall}} {
		if s.c.Path == "" {
			continue
		}
		at := fmt.Sprintf("%s:%d: %s: ", e.Paths.Config, s.c.Line, s.c.Key)
		us, err := ownScript(s.event, ScriptUser, s.c.Path, s.c.Path, at)
		if err != nil {
			return nil, err
		}
		out = append(out, us)
	}
	if sc.Kind == target.Project {
		ps, err := e.projectScripts(sc)
		if err != nil {
			return nil, err
		}
		out = append(out, ps...)
	}
	for _, s := range []struct {
		event, flag string
		f           FlagScript
	}{{PreInstall, "--pre-install", opts.PreInstall}, {PostInstall, "--post-install", opts.PostInstall}} {
		if s.f.Given == "" {
			continue
		}
		flagged, err := ownScript(s.event, ScriptFlag, s.f.Given, s.f.Path, s.flag+" ")
		if err != nil {
			return nil, err
		}
		out = append(out, flagged)
	}
	return runOrder(out)
}

// runOrder is scripts, given by source (user, project, flag), each hashed, in run order (§5.3):
// the pre-install ones as given, then the post-install ones in reverse.
func runOrder(scripts []installScript) ([]installScript, error) {
	var pre, post []installScript
	for _, s := range scripts {
		var err error
		if s.sha256, err = fileSHA256(s.path); err != nil {
			return nil, fmt.Errorf("%s%s: %w", s.where, s.shown, untrusted.CleanErr(err))
		}
		if s.event == PreInstall {
			pre = append(pre, s)
		} else {
			post = append([]installScript{s}, post...)
		}
	}
	return append(pre, post...), nil
}

// ownScript checks a script of the user's own, from config.yaml or a flag, at the absolute path p,
// written given: it must be an executable regular file, and it runs from where it resolves.
func ownScript(event, src, given, p, at string) (installScript, error) {
	s := installScript{event: event, source: src, shown: oneLine(given), where: at}
	// Never relative: Go runs a relative path from the module copy, a file of that name there (A1).
	abs, err := filepath.Abs(p)
	if err != nil {
		return s, fmt.Errorf("%s%s: %w", at, s.shown, untrusted.CleanErr(err))
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return s, fmt.Errorf("%s%s: %w", at, s.shown, untrusted.CleanErr(err))
	}
	if err != nil {
		real = abs
	}
	s.path = real
	return s, s.check(given)
}

// projectScripts is the project's install scripts from project.yml, each resolved under the
// canonical project root, contained in it, and not a symbolic link (Plan 9 §5.1, A12).
func (e *Engine) projectScripts(sc Scope) ([]installScript, error) {
	man, err := e.Manifest(sc)
	if ue, ok := errors.AsType[*manifest.UnresolvedError](err); ok {
		man, err = ue.Manifest, nil // the entries an install leaves unresolved say nothing of scripts
	}
	if err != nil {
		return nil, err
	}
	var out []installScript
	for _, s := range []struct{ event, key, rel string }{{PreInstall, "pre_install", man.PreInstall}, {PostInstall, "post_install", man.PostInstall}} {
		if s.rel == "" {
			continue
		}
		root, err := filepath.EvalSymlinks(sc.ProjectDir)
		if err != nil {
			return nil, fmt.Errorf("resolving the project root: %w", err)
		}
		ps := installScript{event: s.event, source: ScriptProject, shown: oneLine(s.rel),
			where: fmt.Sprintf("%s:%d: %s: ", shown(e.files(sc).manifest), man.ScriptLine(s.key), s.key)}
		// A path that shows as written and names its file as the OS finds it (A27): plain text,
		// and no .., which Join would clean lexically past a link a shell would follow.
		if !untrusted.IsPlain(s.rel) {
			return nil, scriptProblem("%s%s holds a control character or a line break; lola runs only a script whose path is plain text", ps.where, ps.shown)
		}
		if slices.Contains(strings.Split(filepath.ToSlash(s.rel), "/"), "..") {
			return nil, scriptProblem("%s%s has a .. component; name the script by its path inside the project", ps.where, ps.shown)
		}
		// Relative to the project (§5.1): an absolute path names one machine's checkout.
		if filepath.IsAbs(s.rel) {
			return nil, scriptProblem("%s%s is an absolute path; name the script by its path inside the project", ps.where, ps.shown)
		}
		joined := filepath.Join(root, s.rel)
		fi, err := os.Lstat(joined)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, scriptProblem("%s%s does not exist", ps.where, ps.shown)
		}
		if err != nil {
			return nil, fmt.Errorf("%s%s: %w", ps.where, ps.shown, untrusted.CleanErr(err))
		}
		in, err := source.IsWithin(root, joined)
		if err != nil {
			return nil, fmt.Errorf("%s%s: %w", ps.where, ps.shown, untrusted.CleanErr(err))
		}
		if !in {
			resolved, rerr := filepath.EvalSymlinks(joined)
			if rerr != nil {
				resolved = filepath.Clean(joined)
			}
			return nil, scriptProblem("%s%s resolves outside the project (%s)", ps.where, ps.shown, shown(resolved))
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return nil, scriptProblem("%s%s is a symbolic link; lola runs only a regular file inside the project", ps.where, ps.shown)
		}
		if ps.path, err = filepath.EvalSymlinks(joined); err != nil { // a link among its directories, inside the project
			return nil, fmt.Errorf("%s%s: %w", ps.where, ps.shown, untrusted.CleanErr(err))
		}
		if err := ps.check(ps.path); err != nil {
			return nil, err
		}
		// Shown from here on as the file that runs: its path from the canonical project root.
		rel, err := filepath.Rel(root, ps.path)
		if err != nil {
			return nil, fmt.Errorf("%s%s: %w", ps.where, ps.shown, untrusted.CleanErr(err))
		}
		ps.shown, ps.root = oneLine(filepath.ToSlash(rel)), root
		out = append(out, ps)
	}
	return out, nil
}

// check refuses s unless its path is an executable regular file; the chmod hint names chmod, the
// path a shell in the user's directory reaches the file by.
func (s installScript) check(chmod string) error {
	fi, err := os.Stat(s.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return scriptProblem("%s%s does not exist", s.where, s.shown)
	case err != nil:
		return fmt.Errorf("%s%s: %w", s.where, s.shown, untrusted.CleanErr(err))
	case !fi.Mode().IsRegular():
		return scriptProblem("%s%s is not a regular file", s.where, s.shown)
	case fi.Mode().Perm()&0o111 == 0 && !untrusted.IsPlain(chmod): // shown, the path would name another file
		return scriptProblem("%s%s is not executable", s.where, s.shown)
	case fi.Mode().Perm()&0o111 == 0:
		return errs.WithHints(scriptProblem("%s%s is not executable", s.where, s.shown), errs.ShellJoin([]string{"chmod", "+x", chmod}))
	}
	return nil
}

// scriptPlan is the install scripts a scope's run plans: run wrap its jobs, in run order;
// declinedPost says the project's post-install script was declined, pending on every copy it
// would have wrapped; pending, the project's post-install script, runs alone in each copy of
// alone (Plan 9 §5.5, A4).
type scriptPlan struct {
	run          []installScript
	declinedPost bool
	pending      []installScript
	alone        []*hookJob
}

// planScripts plans scripts, the run's install scripts (installScripts), around every job
// (planHooks), and the project's post-install script pending on each copy offered it
// (moduleCopy.pendingScript), before anything is written. A project's script needs trust (§5.4),
// decided once per script: one the run's --trust or a recorded consent for its project, event and
// digest covers runs; else the user is asked on a terminal. Without it a pre-install script that
// would wrap a job is refused, and a post-install one is declined with a warning, pending in
// lock.json, or, offered alone, stays pending with its notice. A dry run plans the same, asking
// nothing, and lists them all as would-run, marking those it has no trust for.
func (e *Engine) planScripts(sc Scope, scripts []installScript, jobs []*hookJob, copies []moduleCopy, opts Options, res *Result) (scriptPlan, error) {
	var sp scriptPlan
	var alone []*hookJob
	for _, c := range copies {
		if c.pendingScript && !slices.ContainsFunc(jobs, func(j *hookJob) bool { return j.name == c.name }) {
			i := slices.IndexFunc(res.Modules, func(m ModuleResult) bool { return m.Name == c.name })
			alone = append(alone, &hookJob{name: c.name, dir: filepath.Join(e.files(sc).modules, c.name), targets: c.targets, result: &res.Modules[i]})
		}
	}
	listed := make([]string, len(scripts))                // the status each is listed with around jobs, "" for none
	lacksTrust := make([]bool, len(scripts))              // a dry run's project script without the run's trust
	listAlone, aloneLacksTrust := false, false            // a dry run lists the pending script, alone, as would-run
	asked := opts.askScripts && sc.Kind == target.Project // a sync asks about its project's scripts in any case (§7)
	for i, s := range scripts {
		offered := len(alone) > 0 && s.source == ScriptProject && s.event == PostInstall
		if len(jobs) == 0 && !offered && (!asked || s.source != ScriptProject) {
			continue
		}
		ok := true
		if s.source == ScriptProject {
			// With no copy to wrap a pre-install script is asked about for later runs; a post-install
			// one, which would be declined anyway, is not asked about (R-9-35).
			later := len(jobs) == 0 && !offered
			var err error
			if ok, err = e.trustScript(s, opts, res, !later || s.event == PreInstall, later); err != nil {
				return scriptPlan{}, err
			}
		}
		if offered {
			// A dry run plans as the run would, and lists the script; without trust it stays
			// pending, which the run's notice says.
			if ok {
				sp.pending, sp.alone = []installScript{s}, alone
			} else {
				for _, j := range alone {
					res.Notices = append(res.Notices, e.pendingScriptNotice(sc, j.name, opts))
				}
			}
			listAlone, aloneLacksTrust = opts.DryRun, !ok
		}
		if len(jobs) == 0 {
			// Nothing for it to wrap: a sync refuses an untrusted pre-install script all the same, so
			// the project's next install does not, and warns of a post-install one.
			switch {
			case ok || opts.DryRun || offered:
			case s.event == PreInstall:
				return scriptPlan{}, e.refuseScript(sc, s, jobs, copies, opts)
			case slices.ContainsFunc(copies, func(c moduleCopy) bool { return c.scriptNotice }):
				// pending on a copy, whose notice already says how to trust and run it
			default:
				res.Notices = append(res.Notices, Notice{Level: LevelWarn, Text: "post-install script " + s.shown +
					" (project) is not trusted on this machine; trust it: " + e.SyncCommand(sc, TrustScripts.Arg())})
			}
			continue
		}
		switch {
		case opts.DryRun:
			listed[i], lacksTrust[i] = HookWouldRun, !ok
		case ok:
			sp.run = append(sp.run, s)
		case s.event == PreInstall:
			return scriptPlan{}, e.refuseScript(sc, s, jobs, copies, opts)
		default:
			// Listed where it would run, when the post-install phase runs (runScripts).
			d := s
			d.declined, sp.declinedPost = true, true
			sp.run = append(sp.run, d)
			for _, j := range jobs {
				text := j.name + ": post-install script " + s.shown + " (project) skipped without your trust; review and run it: " + e.SyncCommand(sc, j.name)
				if opts.AskScript == nil {
					text += ", or trust it without asking: " + e.SyncCommand(sc, j.name, TrustScripts.Arg())
				}
				res.Notices = append(res.Notices, Notice{Level: LevelWarn, Text: text})
			}
		}
	}
	// In run order: each module's pre-install scripts, then each module's post-install ones, then
	// the pending ones that run alone.
	for _, event := range []string{PreInstall, PostInstall} {
		for _, j := range jobs {
			for i, s := range scripts {
				if s.event != event || listed[i] == "" {
					continue
				}
				for _, id := range j.targets {
					res.Scripts = append(res.Scripts, ScriptRun{Event: s.event, Source: s.source, Path: s.shown, Module: j.name, Target: id, Status: listed[i],
						untrusted: lacksTrust[i], root: oneLine(s.root)})
				}
			}
		}
	}
	if listAlone {
		post := scripts[slices.IndexFunc(scripts, func(s installScript) bool { return s.source == ScriptProject && s.event == PostInstall })]
		for _, j := range alone {
			for _, id := range j.targets {
				res.Scripts = append(res.Scripts, ScriptRun{Event: PostInstall, Source: ScriptProject, Path: post.shown, Module: j.name, Target: id, Status: HookWouldRun,
					untrusted: aloneLacksTrust, root: oneLine(post.root)})
			}
		}
	}
	return sp, nil
}

// pendingScriptNotice says the project's post-install script is pending on name's copy, and the
// sync that runs it: the one that asks, and off a terminal the one that trusts it (Plan 9 §5.5).
func (e *Engine) pendingScriptNotice(sc Scope, name string, opts Options) Notice {
	text := "pending post-install script for " + name + " → " + e.SyncCommand(sc, name)
	if opts.AskScript == nil {
		text += " (off a terminal: " + e.SyncCommand(sc, name, TrustScripts.Arg()) + ")"
	}
	return Notice{Level: LevelWarn, Text: text}
}

// trustScript decides whether s, a project's install script without a recorded consent for its
// project, event and digest, may run: a --trust covering scripts grants it, shown on the receipt;
// so does, in a batch's apply pass (opts.Expect), the grant the batch holds; otherwise, when ask,
// opts.AskScript may ask, for later runs when later. Each grant is recorded only once the run's receipt is answered yes
// (R-9-3, A23); a dry run asks nothing and records nothing. A pre-install script the run lacks
// consent for is a kind of trust the run needs (R-9-6).
func (e *Engine) trustScript(s installScript, opts Options, res *Result, ask, later bool) (bool, error) {
	g := s.grant()
	if ok, err := e.Consent.Granted(g.key, g.digest); err != nil || ok {
		return ok, err
	}
	if s.event == PreInstall {
		res.needs |= TrustScripts
	}
	if opts.Trust.Covers(TrustScripts) {
		change, err := e.consentChange(g.key)
		if err != nil {
			return false, err
		}
		res.cover(TrustScripts, s.event+" "+s.shown, change, &g)
		return true, nil
	}
	if opts.Expect != nil && slices.Contains(opts.Expect.grants, g) {
		res.grants = append(res.grants, g)
		return true, nil
	}
	if opts.DryRun || opts.AskScript == nil || !ask {
		return false, nil
	}
	if ok, err := opts.AskScript(ScriptQuestion{Event: s.event, Path: s.shown, Root: oneLine(s.root), SHA256: s.sha256, Later: later}); err != nil || !ok {
		return false, err
	}
	res.grants = append(res.grants, g)
	return true, nil
}

// scriptNeeds is TrustScripts when sc's project has a pre-install script without a recorded consent
// for its digest, which a sync asks about whatever it installs (§7).
func (e *Engine) scriptNeeds(sc Scope) (TrustKinds, error) {
	if sc.Kind != target.Project {
		return 0, nil
	}
	scripts, err := e.projectScripts(sc)
	if err == nil {
		scripts, err = runOrder(scripts)
	}
	if err != nil {
		return 0, err
	}
	for _, s := range scripts {
		if s.event != PreInstall {
			continue
		}
		g := s.grant()
		if ok, err := e.Consent.Granted(g.key, g.digest); err != nil || !ok {
			return TrustScripts, err
		}
	}
	return 0, nil
}

// grant is the consent of s, a project's install script: keyed by its canonical project root
// and event, for its digest (§5.4).
func (s installScript) grant() grant {
	return grant{key: consent.ScriptKey(s.root, s.event), digest: "sha256:" + s.sha256}
}

// scriptConsentError refuses a project's pre-install script without trust. Its hints review the
// version the run installs and trust the script; a retry without trust would refuse again, so
// withRetry adds none.
type scriptConsentError struct{ msg string }

func (e *scriptConsentError) Error() string { return e.msg }

// refuseScript is the refusal of s, a project's pre-install script the run has no trust for: its
// hints are those of a refusal of the first job's copy (consentHints), lola info of whose version
// lists the script.
func (e *Engine) refuseScript(sc Scope, s installScript, jobs []*hookJob, copies []moduleCopy, opts Options) error {
	change, err := e.consentChange(s.grant().key)
	if err != nil {
		return err
	}
	run := opts.Run
	if run == "" {
		run = "lola"
	}
	msg := fmt.Sprintf("%s needs your trust for the %s script %s of this project (%s), ", run, s.event, s.shown, change)
	if opts.AskScript == nil {
		msg += "and there is no terminal to ask on"
	} else {
		msg += "and you declined it"
	}
	// lola info of any module the run installs lists the script: the first it wraps, else the first
	// the run plans.
	name := ""
	if len(jobs) > 0 {
		name = jobs[0].name
	} else if len(copies) > 0 {
		name = copies[0].name
	}
	var hints []string
	if k := slices.IndexFunc(copies, func(c moduleCopy) bool { return c.name == name }); k >= 0 {
		if hints, err = e.consentHints(sc, copies[k], opts, TrustScripts); err != nil {
			return err
		}
	} else if opts.Rerun != nil {
		hints = []string{"then consent to it: " + opts.Rerun(opts.Trust|TrustScripts)}
	}
	return errs.WithHints(&scriptConsentError{msg: msg}, hints...)
}

// runScripts runs every script of scripts for event around j, once per target: a pre-install
// failure, or a script that changed since the run planned it (A16), stops and returns its error,
// nothing written; a post-install one becomes a warning, the installation kept.
func (e *Engine) runScripts(ctx context.Context, sc Scope, j *hookJob, scripts []installScript, event string, opts Options, res *Result) error {
	for _, s := range scripts {
		if s.event != event {
			continue
		}
		for _, id := range j.targets {
			if s.declined {
				res.Scripts = append(res.Scripts, ScriptRun{Event: event, Source: s.source, Path: s.shown, Module: j.name, Target: id, Status: HookDeclined, root: oneLine(s.root)})
				continue
			}
			err := s.unchanged()
			if err == nil {
				err = e.runHook(ctx, sc, j, s.path, event, id, opts.HookOutput, "LOLA_SCRIPT_SOURCE="+s.source)
				if err != nil {
					err = fmt.Errorf("%s script %s (%s) failed for %s on %s: %w", event, s.shown, s.source, j.name, id, err)
				}
			}
			status := HookRan
			if err != nil {
				status = HookFailed
			}
			res.Scripts = append(res.Scripts, ScriptRun{Event: event, Source: s.source, Path: s.shown, Module: j.name, Target: id, Status: status, root: oneLine(s.root)})
			switch {
			case err == nil:
			case event == PreInstall && errors.Is(err, errScriptChanged):
				return fmt.Errorf("%s script %s (%s) changed after lola checked it; nothing was installed", event, s.shown, s.source)
			case event == PreInstall:
				return err
			case errors.Is(err, errScriptChanged):
				res.Notices = append(res.Notices, Notice{Level: LevelWarn,
					Text: fmt.Sprintf("%s script %s (%s) changed after lola checked it; the installation was kept", event, s.shown, s.source)})
			default:
				res.Notices = append(res.Notices, Notice{Level: LevelWarn, Text: err.Error() + "; the installation was kept"})
			}
		}
	}
	return nil
}

// errScriptChanged is a script that is no longer the regular file of the digest the run planned.
var errScriptChanged = errors.New("changed after lola checked it")

// unchanged hashes s again just before it runs (§5.4, A16): it must still be the regular file,
// not a link, of the digest taken when the run planned it.
func (s installScript) unchanged() error {
	fi, err := os.Lstat(s.path)
	if err != nil || !fi.Mode().IsRegular() {
		return errScriptChanged
	}
	if sum, err := fileSHA256(s.path); err != nil || sum != s.sha256 {
		return errScriptChanged
	}
	return nil
}

// scriptError is a configured install script lola cannot run, which the same run would refuse
// again: an install keeps only its hints (withRetry).
type scriptError struct{ msg string }

func (e *scriptError) Error() string { return e.msg }

func scriptProblem(format string, args ...any) error {
	return &scriptError{msg: fmt.Sprintf(format, args...)}
}
