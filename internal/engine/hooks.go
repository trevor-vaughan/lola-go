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
	"time"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/module"
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

// hookJob is one module's install hooks in an operation.
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
			b, err := os.ReadFile(filepath.Join(dir, h.Script))
			if err != nil {
				return nil, "", err
			}
			sum := sha256.Sum256(b)
			s := HookScript{Event: h.Event, Script: shown(h.Script), SHA256: hex.EncodeToString(sum[:]), path: h.Script}
			fmt.Fprintf(all, "%s\x00%s\x00%s\n", s.Event, s.path, s.SHA256)
			out = append(out, s)
		}
	}
	return out, "sha256:" + hex.EncodeToString(all.Sum(nil)), nil
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

// planHooks prepares the hooks of every copy that runs them, before anything is written. A
// dry run lists them as would-run. Without consent a pre-install hook is an error, whose hint is
// the run with --trust covering hooks too (opts.Rerun), and a post-install hook is declined with
// a warning.
func (e *Engine) planHooks(sc Scope, copies []moduleCopy, opts Options, res *Result) ([]*hookJob, error) {
	var jobs []*hookJob
	for _, c := range copies {
		if !c.runHooks || len(c.hooks) == 0 {
			continue
		}
		scripts, digest, err := hookScripts(c.src, c.hooks)
		if err != nil {
			return nil, err
		}
		i := slices.IndexFunc(res.Modules, func(m ModuleResult) bool { return m.Name == c.name })
		j := &hookJob{name: c.name, dir: filepath.Join(e.files(sc).modules, c.name), targets: c.targets, scripts: scripts, result: &res.Modules[i]}
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
		status := HookWouldRun
		if !opts.DryRun {
			if ok {
				jobs = append(jobs, j)
				continue
			}
			if c.forced && !c.pending {
				continue // the copy is kept as it is, with what its hooks built when they ran
			}
			if pre {
				hints := []string{"review it: " + errs.ShellJoin([]string{"lola", "cache", "info", c.name})}
				if opts.Rerun != nil {
					hints = append(hints, "then consent to it: "+opts.Rerun(opts.Trust|TrustHooks))
				}
				return nil, errs.WithHints(fmt.Errorf("%s has a pre-install hook, and lola runs hooks only with your consent", c.name), hints...)
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

// runHooks runs every job's hooks for event, once per target. A failing pre-install hook
// stops and returns its error; a failing post-install hook becomes a warning.
func (e *Engine) runHooks(ctx context.Context, sc Scope, jobs []*hookJob, event string, opts Options, res *Result) error {
	for _, j := range jobs {
		for _, s := range j.scripts {
			if s.Event != event {
				continue
			}
			for _, id := range j.targets {
				err := e.runHook(ctx, sc, j, s, id, opts.HookOutput)
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
	}
	return nil
}

// runHook runs one script in the module copy with the §9.1 environment, streaming its
// output to out, under e.HookTimeout.
func (e *Engine) runHook(ctx context.Context, sc Scope, j *hookJob, s HookScript, targetID string, out io.Writer) error {
	t, err := e.target(targetID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, e.HookTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(j.dir, s.path))
	cmd.Dir = j.dir
	cmd.Env = append(e.Environ(),
		"LOLA_MODULE_NAME="+j.name,
		"LOLA_MODULE_PATH="+j.dir,
		"LOLA_PROJECT_PATH="+t.Root(sc.Kind, sc.ProjectDir, e.Paths),
		"LOLA_ASSISTANT="+targetID,
		"LOLA_SCOPE="+sc.Kind.String(),
		"LOLA_HOOK="+s.Event)
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
