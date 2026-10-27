package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/store"
)

// Install hook events, in the order they run.
const (
	PreInstall  = "pre-install"
	PostInstall = "post-install"
)

// Hook run statuses.
const (
	HookWouldRun = "would-run" // dry run
	HookRan      = "ran"
	HookFailed   = "failed"
	HookDeclined = "declined" // no consent; only post-install hooks can be declined without aborting
)

// HookScript is one install hook with the sha256 of its script, as consent shows it. Script is
// the script's path, as the module names it, through untrusted.Text; path is that path itself,
// which runs and keys consent.
type HookScript struct {
	Event  string `json:"event"`
	Script string `json:"script"`
	SHA256 string `json:"sha256"`
	path   string
}

// HookRun is one hook for one target in an operation's result.
type HookRun struct {
	HookScript
	Target string `json:"target"`
	Status string `json:"status"`
}

// hookJob is one module copy an operation installs or replaces: the install hooks that run in it
// (none when it has none or they were declined), which install scripts wrap.
type hookJob struct {
	name, dir string // dir is the module copy: working directory and LOLA_MODULE_PATH
	targets   []string
	scripts   []HookScript
	result    *ModuleResult
}

func (j *hookJob) record(s HookScript, targetID, status string) {
	j.result.Hooks = append(j.result.Hooks, HookRun{HookScript: s, Target: targetID, Status: status})
}

// hookScripts hashes each hook's script under dir, pre-install first, and returns the digest
// consent is keyed by: it changes whenever an event, a script path or a script changes.
func hookScripts(dir string, hooks []module.Hook) ([]HookScript, string, error) {
	var out []HookScript
	all := sha256.New()
	for _, event := range []string{PreInstall, PostInstall} {
		for _, h := range hooks {
			if h.Event != event {
				continue
			}
			sum, err := fileSHA256(filepath.Join(dir, h.Script))
			if err != nil {
				return nil, "", err
			}
			s := HookScript{Event: h.Event, Script: shown(h.Script), SHA256: sum, path: h.Script}
			fmt.Fprintf(all, "%s\x00%s\x00%s\n", s.Event, s.path, s.SHA256)
			out = append(out, s)
		}
	}
	return out, "sha256:" + hex.EncodeToString(all.Sum(nil)), nil
}

// fileSHA256 is the hex sha256 of the file at path, read as a stream.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// allowed decides whether a module's hooks without a recorded consent for their digest may run:
// a --trust covering hooks grants it; so does, in a batch's apply pass (opts.Expect), the grant
// for name and digest the batch holds, a terminal yes of its collect pass; otherwise opts.Ask may
// ask the user. Each grant is recorded only once the run's receipt is answered yes, before the
// first write (R-9-3, A23). A dry run
// decides the same way but asks nothing and records nothing. A grant by --trust is added to res.Trusted, once per event the
// module hooks.
func (e *Engine) allowed(name string, scripts []HookScript, digest string, opts Options, res *Result) (bool, error) {
	if opts.Trust.Covers(TrustHooks) {
		change, err := e.consentChange(name)
		if err != nil {
			return false, err
		}
		var events []string
		for _, s := range scripts {
			if !slices.Contains(events, s.Event) {
				events = append(events, s.Event)
			}
		}
		for i, ev := range events {
			var g *grant
			if i == 0 {
				g = &grant{key: name, digest: digest}
			}
			res.cover(TrustHooks, shown(name)+" "+ev, change, g)
		}
		return true, nil
	}
	if g := (grant{key: name, digest: digest}); opts.Expect != nil && slices.Contains(opts.Expect.grants, g) {
		res.grants = append(res.grants, g)
		return true, nil
	}
	if opts.DryRun || opts.Ask == nil {
		return false, nil
	}
	if ok, err := opts.Ask(name, scripts); err != nil || !ok {
		return false, err
	}
	res.grants = append(res.grants, grant{key: name, digest: digest})
	return true, nil
}

// reviewOf is the lola info arguments that show exactly v, the version a run would install, from
// the cache (Plan 9 R-9-32): a git version without a component selection as its source at its
// commit, which info finds in the cache without fetching (R-9-21); else its cached name, at the
// head that holds it; nil when no lola info command shows it.
func (e *Engine) reviewOf(v store.Version) ([]string, error) {
	o, found, err := e.Store.Origin(v.Name)
	if err != nil || !found {
		return nil, err
	}
	if o.Type == string(source.KindGit) && v.Commit != "" && o.Components == nil {
		args := []string{o.Source, "--ref", v.Commit}
		if o.Path != "" {
			args = append(args, "--path", o.Path)
		}
		return args, nil
	}
	if currentHead(o) == v.ID {
		return []string{v.Name}, nil
	}
	for _, h := range o.Heads {
		if h.Digest == v.ID {
			return []string{v.Name, "--ref", h.Ref}, nil
		}
	}
	return nil, nil
}

// consentHints are the hints of a refusal of c's pre-install hook or script for want of kinds:
// lola info of exactly the version c installs (moduleCopy.review, or reviewOf its version, read
// now, when the run has put it in the cache), when one shows it, and the run that consents:
// opts.Rerun, the user's run with --trust adding kinds, else the command that installs c
// (moduleCopy.again) with them.
func (e *Engine) consentHints(sc Scope, c moduleCopy, opts Options, kinds TrustKinds) ([]string, error) {
	review := c.review
	if c.shows != nil {
		var err error
		if review, err = e.reviewOf(*c.shows); err != nil {
			return nil, err
		}
		if review != nil {
			for _, t := range c.reviewTargets {
				review = append(review, "--target", t)
			}
		}
	}
	var hints []string
	if review != nil {
		hints = append(hints, "review it: "+e.Command(sc, "info", append(slices.Clone(review), opts.Display...)...))
	}
	k := opts.Trust | kinds
	if opts.Rerun != nil {
		return append(hints, "then consent to it: "+opts.Rerun(k)), nil
	}
	if len(c.again) > 0 {
		hints = append(hints, "then consent to it: "+e.Command(sc, c.again[0], append(append(slices.Clone(c.again[1:]), opts.Display...), k.Arg())...))
	}
	return hints, nil
}

// unaskedKinds is the kinds of trust beyond opts.Trust that a run which cannot ask would be
// refused for, decided before anything asks: a pre-install hook of a copy that runs hooks, and
// the project's pre-install script (one of scripts) when something checks it, each without a
// recorded consent or the batch's grant. A refusal for one kind hints them all, so the run it
// names is not refused for the next (the union of the kinds given and every kind missing). A run
// that can ask about a kind asks rather than refuses, so that kind is left out.
func (e *Engine) unaskedKinds(copies []moduleCopy, scripts []installScript, opts Options) (TrustKinds, error) {
	var k TrustKinds
	held := func(key, digest string) (bool, error) {
		if opts.Expect != nil && slices.Contains(opts.Expect.grants, grant{key: key, digest: digest}) {
			return true, nil
		}
		return e.Consent.Granted(key, digest)
	}
	for _, c := range copies {
		if opts.Ask != nil || opts.Trust.Covers(TrustHooks) || k.Covers(TrustHooks) {
			break
		}
		if !c.runHooks || c.forced && !c.pending || !slices.ContainsFunc(c.hooks, func(h module.Hook) bool { return h.Event == PreInstall }) {
			continue
		}
		_, digest, err := hookScripts(c.src, c.hooks)
		if err != nil {
			return 0, err
		}
		ok, err := held(c.name, digest)
		if err != nil {
			return 0, err
		}
		if !ok {
			k |= TrustHooks
		}
	}
	checked := opts.askScripts || slices.ContainsFunc(copies, func(c moduleCopy) bool { return c.runHooks })
	for _, s := range scripts {
		if !checked || opts.AskScript != nil || opts.Trust.Covers(TrustScripts) || s.source != ScriptProject || s.event != PreInstall {
			continue
		}
		g := s.grant()
		ok, err := held(g.key, g.digest)
		if err != nil {
			return 0, err
		}
		if !ok {
			k |= TrustScripts
		}
	}
	return k, nil
}

// hookConsentError refuses a pre-install hook without consent. Its hints review the version and
// consent to it; a retry without consent would refuse again, so withRetry adds none.
type hookConsentError struct{ module string }

func (e *hookConsentError) Error() string {
	return e.module + " has a pre-install hook, and lola runs hooks only with your consent"
}

// planHooks prepares the hooks of every copy that runs them, before anything is written, and
// returns a job for every copy the run installs or replaces, which install scripts wrap (R-9-8):
// a job's scripts are the hooks that run in it, none when its module has none or they were
// declined. A dry run lists the hooks as would-run. Without consent a pre-install hook is an
// error, whose hints review the version and consent to it (consentHints), and a post-install hook
// is declined with a warning. A forced copy whose hooks do not run is kept as it is, so it gets no job.
// unasked is the other kinds the run would be refused for (unaskedKinds), which a refusal hints too.
func (e *Engine) planHooks(sc Scope, copies []moduleCopy, opts Options, unasked TrustKinds, res *Result) ([]*hookJob, error) {
	var jobs []*hookJob
	for _, c := range copies {
		if !c.runHooks {
			continue
		}
		i := slices.IndexFunc(res.Modules, func(m ModuleResult) bool { return m.Name == c.name })
		j := &hookJob{name: c.name, dir: filepath.Join(e.files(sc).modules, c.name), targets: c.targets, result: &res.Modules[i]}
		if len(c.hooks) == 0 {
			if !c.forced {
				jobs = append(jobs, j)
			}
			continue
		}
		scripts, digest, err := hookScripts(c.src, c.hooks)
		if err != nil {
			return nil, err
		}
		res.Modules[i].hooksKept = c.forced && !c.pending
		pre := slices.ContainsFunc(scripts, func(s HookScript) bool { return s.Event == PreInstall })
		ok, err := e.Consent.Granted(c.name, digest)
		if err == nil && !ok {
			if pre && (!c.forced || c.pending) {
				res.needs |= TrustHooks // a kept copy runs nothing, so needs nothing
			}
			ok, err = e.allowed(c.name, scripts, digest, opts, res)
		}
		if err != nil {
			return nil, err
		}
		if ok || !c.forced {
			jobs = append(jobs, j)
		}
		status := HookWouldRun
		if !opts.DryRun {
			if ok {
				j.scripts = scripts
				continue
			}
			if c.forced && !c.pending {
				continue // the copy is kept as it is, with what its hooks built when they ran
			}
			if pre {
				hints, err := e.consentHints(sc, c, opts, TrustHooks|unasked)
				if err != nil {
					return nil, err
				}
				return nil, errs.WithHints(&hookConsentError{module: c.name}, hints...)
			}
			// The hook stays pending in lock.json: sync replaces the copy from the hashed version
			// and asks again (Plan 6a §11.3).
			status = HookDeclined
			text := c.name + ": post-install hook skipped without your consent"
			if c.forced {
				text = c.name + ": its post-install hook has not run (you declined it)"
			}
			text += "; review and run it: " + e.SyncCommand(sc, c.name)
			if opts.Ask == nil {
				text += ", or consent without asking: " + e.SyncCommand(sc, c.name, TrustHooks.Arg())
			}
			res.Notices = append(res.Notices, Notice{Level: LevelWarn, Text: text})
		}
		for _, s := range scripts {
			for _, id := range c.targets {
				j.record(s, id, status)
			}
		}
	}
	return jobs, nil
}

// runHooks runs, for event, every job's hooks once per target, wrapped in scripts, the install
// scripts of the run (Plan 9 §5.3): before a module's pre-install hooks, and after its
// post-install hooks. A failing pre-install hook or script stops and returns its error; a failing
// post-install one becomes a warning.
func (e *Engine) runHooks(ctx context.Context, sc Scope, jobs []*hookJob, scripts []installScript, event string, opts Options, res *Result) error {
	for _, j := range jobs {
		if event == PreInstall {
			if err := e.runScripts(ctx, sc, j, scripts, event, opts, res); err != nil {
				return err
			}
		}
		for _, s := range j.scripts {
			if s.Event != event {
				continue
			}
			for _, id := range j.targets {
				err := e.runHook(ctx, sc, j, filepath.Join(j.dir, s.path), event, id, opts.HookOutput)
				if err == nil {
					j.record(s, id, HookRan)
					continue
				}
				j.record(s, id, HookFailed)
				err = fmt.Errorf("%s hook %s of %s failed for %s: %w", event, s.Script, j.name, id, err)
				if event == PreInstall {
					return errs.WithHints(err, "nothing was installed; the hook's output is above")
				}
				res.Notices = append(res.Notices, Notice{Level: LevelWarn, Text: err.Error() + "; the installation was kept"})
			}
		}
		if event == PostInstall {
			if err := e.runScripts(ctx, sc, j, scripts, event, opts, res); err != nil {
				return err
			}
		}
	}
	return nil
}

// runHook runs the executable at path in the module copy of j with the §9.1 environment for event
// and targetID, and env, streaming its output to out, under e.HookTimeout. LOLA_SCRIPT_SOURCE
// is set only by env, an install script's: a module hook never inherits one.
func (e *Engine) runHook(ctx context.Context, sc Scope, j *hookJob, path, event, targetID string, out io.Writer, env ...string) error {
	t, err := e.target(targetID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, e.HookTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path)
	cmd.Dir = j.dir
	parent := slices.DeleteFunc(e.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "LOLA_SCRIPT_SOURCE=") })
	cmd.Env = append(append(parent,
		"LOLA_MODULE_NAME="+j.name,
		"LOLA_MODULE_PATH="+j.dir,
		"LOLA_PROJECT_PATH="+t.Root(sc.Kind, sc.ProjectDir, e.Paths),
		"LOLA_ASSISTANT="+targetID,
		"LOLA_SCOPE="+sc.Kind.String(),
		"LOLA_HOOK="+event), env...)
	if out == nil {
		out = io.Discard
	}
	cmd.Stdout, cmd.Stderr = out, out
	cmd.WaitDelay = 5 * time.Second // do not wait forever on children that keep the output open
	err = cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("timed out after %s", e.HookTimeout)
	}
	return err
}

// declaresPostInstall reports whether hooks hold a post-install hook, the only one a pending
// mark can stand for.
func declaresPostInstall(hooks []module.Hook) bool {
	return slices.ContainsFunc(hooks, func(h module.Hook) bool { return h.Event == PostInstall })
}
