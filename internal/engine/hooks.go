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

// allowed decides whether a module's hooks may run: --allow-hooks grants consent for this
// run, a recorded consent for the same digest grants it again, and otherwise opts.Ask may
// ask the user, whose yes is recorded.
func (e *Engine) allowed(name string, scripts []HookScript, digest string, opts Options) (bool, error) {
	if opts.AllowHooks {
		return true, nil
	}
	ok, err := e.Consent.Granted(name, digest)
	if err != nil || ok {
		return ok, err
	}
	if opts.Ask == nil {
		return false, nil
	}
	if ok, err = opts.Ask(name, scripts); err != nil || !ok {
		return false, err
	}
	return true, e.Consent.Grant(name, digest)
}

// planHooks prepares the hooks of every copy that runs them, before anything is written. A
// dry run lists them as would-run. Without consent a pre-install hook is an error and a
// post-install hook is declined with a warning.
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
		status := HookWouldRun
		if !opts.DryRun {
			ok, err := e.allowed(c.name, scripts, digest, opts)
			if err != nil {
				return nil, err
			}
			if ok {
				jobs = append(jobs, j)
				continue
			}
			if slices.ContainsFunc(scripts, func(s HookScript) bool { return s.Event == PreInstall }) {
				return nil, errs.WithHints(fmt.Errorf("%s has a pre-install hook, and lola runs hooks only with your consent", c.name),
					"review it: "+errs.ShellJoin([]string{"lola", "cache", "info", c.name}), "then rerun with --allow-hooks, or on a terminal answer the prompt")
			}
			status = HookDeclined
			res.Notices = append(res.Notices, Notice{Level: LevelWarn,
				Text: c.name + ": post-install hook skipped without your consent (rerun with --allow-hooks to run it)"})
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
