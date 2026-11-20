package engine

import (
	"bytes"
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
	"unicode/utf8"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
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
	// HookLater is a project's script a dry run with nothing to install checks: the run
	// trusts it, with consent, for later runs, and runs nothing.
	HookLater = "later"
)

// HookScript is one install hook with the sha256 of its script, as consent shows it. Script is
// the script's path, as the module names it, through untrusted.Text; path is that path itself,
// which runs and keys consent.
type HookScript struct {
	Event  string `json:"event"`
	Script string `json:"script"`
	SHA256 string `json:"sha256"`
	// Body is the script as it was hashed, raw (readBody): what output prints in full so that the
	// hook counts as shown. Withheld says why it is not (BodyTooLarge, BodyNotText): such a hook can
	// never be shown, and only --unseen consents to it.
	Body     string `json:"body"`
	Withheld string `json:"withheld,omitempty"`
	// New says, in a run, whether the shown record lacks the hook (ShownKey), so output prints its
	// body in full rather than folded; nil outside a run.
	New  *bool `json:"new,omitempty"`
	path string
	file string // the script's absolute path, which a withheld body names to inspect
}

// Inspect is the file a withheld body names: the script as lola read it.
func (s HookScript) Inspect() string { return s.file }

// ShownKey is s's key in the shown record, which only output that prints s's body in full adds to
// it (its path and sha256 alone never do): the sha256, in hex, of its event, path and script's
// sha256, whatever module declares it.
func (s HookScript) ShownKey() string { return contentKey("hook", s.Event, s.path, s.SHA256) }

// contentKey is the sha256, in hex, of parts joined by NUL: a shown record key.
func contentKey(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

// shownKeys is the shown keys of scripts.
func shownKeys(scripts []HookScript) []string {
	keys := make([]string, len(scripts))
	for i, s := range scripts {
		keys[i] = s.ShownKey()
	}
	return keys
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

// hookScripts hashes and reads each hook's script under dir, pre-install first, and returns the
// digest consent is keyed by: it changes whenever an event, a script path or a script changes.
func hookScripts(dir string, hooks []module.Hook) ([]HookScript, string, error) {
	var out []HookScript
	all := sha256.New()
	for _, event := range []string{PreInstall, PostInstall} {
		for _, h := range hooks {
			if h.Event != event {
				continue
			}
			file := filepath.Join(dir, h.Script)
			sum, body, withheld, err := readBody(file)
			if err != nil {
				return nil, "", err
			}
			s := HookScript{Event: h.Event, Script: shown(h.Script), SHA256: sum, Body: body, Withheld: withheld, path: h.Script, file: file}
			fmt.Fprintf(all, "%s\x00%s\x00%s\n", s.Event, s.path, s.SHA256)
			out = append(out, s)
		}
	}
	return out, "sha256:" + hex.EncodeToString(all.Sum(nil)), nil
}

// Why a script's body is withheld from output (HookScript.Withheld, ScriptRun.Withheld).
const (
	BodyTooLarge = "too_large" // more than MaxBody bytes
	BodyNotText  = "not_text"  // not valid UTF-8, or it holds a NUL
)

// MaxBody is the most bytes of a script output prints in full.
const MaxBody = 64 << 10

// readBody is the hex sha256 of the file at path, hashed as a stream, and its text, raw, read in
// the same pass: "" with why it is withheld when the file is larger than MaxBody (of which at most
// MaxBody+1 bytes are kept) or is not text (BodyTooLarge, BodyNotText).
func readBody(path string) (sum, body, withheld string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	var kept bytes.Buffer
	if _, err := io.Copy(io.MultiWriter(h, &capped{buf: &kept, left: MaxBody + 1}), f); err != nil {
		return "", "", "", err
	}
	sum = hex.EncodeToString(h.Sum(nil))
	switch b := kept.Bytes(); {
	case len(b) > MaxBody:
		return sum, "", BodyTooLarge, nil
	case !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0:
		return sum, "", BodyNotText, nil
	default:
		return sum, string(b), "", nil
	}
}

// capped is a writer that keeps at most left more bytes in buf and drops the rest, never failing.
type capped struct {
	buf  *bytes.Buffer
	left int
}

func (c *capped) Write(p []byte) (int, error) {
	if c.left > 0 {
		n := min(len(p), c.left)
		c.buf.Write(p[:n])
		c.left -= n
	}
	return len(p), nil
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
// a --trust covering hooks grants it; so
// does, in a batch's apply pass (opts.Expect), the grant for name and digest the batch holds, a
// terminal yes of its collect pass; otherwise opts.Ask may ask the user. Each grant is recorded only once the run's receipt is answered yes, before the
// first write. A dry run
// decides the same way but asks nothing and records nothing. A grant by --trust is added to res.Trusted, once per event the
// module hooks.
func (e *Engine) allowed(name string, scripts []HookScript, digest string, opts Options, res *Result) (bool, error) {
	trusted, err := e.trustGrants(opts, TrustHooks, shownKeys(scripts), !slices.ContainsFunc(scripts, func(s HookScript) bool { return s.Withheld != "" }), res)
	if err != nil {
		return false, err
	}
	if trusted {
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
		res.answer(TrustHooks, true)
		return true, nil
	}
	if opts.DryRun || opts.Ask == nil {
		res.answer(TrustHooks, false)
		return false, nil
	}
	ok, err := opts.Ask(name, scripts)
	if err != nil {
		return false, err
	}
	shownNow := slices.DeleteFunc(slices.Clone(scripts), func(s HookScript) bool { return s.Withheld != "" })
	if err := e.Shown.Add(shownKeys(shownNow)...); err != nil { // the question showed them, but a withheld body
		return false, err
	}
	res.answer(TrustHooks, ok)
	if !ok {
		return false, nil
	}
	res.grants = append(res.grants, grant{key: name, digest: digest})
	return true, nil
}

// reviewOf is the lola info arguments that show exactly v, the version a run would install, as
// run will leave the cache (Engine.origin; nil is the cache alone): a git version
// without a component selection as its source at its commit, which info finds in the cache
// without fetching, or fetches at that commit when run still holds it; else its cached
// name, at the head that holds it. It is nil when no lola info command shows it, as for a version
// run staged and has not committed, whose name the cache does not hold yet.
func (e *Engine) reviewOf(run *StagedRun, v store.Version) ([]string, error) {
	o, found, err := e.origin(run, v.Name)
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
	if run.holds(v) {
		return nil, nil
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

// consentHintPrefix begins the hint of a refusal for want of consent that names the run that consents.
const consentHintPrefix = "then consent to it: "

// consentHints are the hints of a refusal of c's pre-install hook or script for want of kinds:
// lola info of exactly the version c installs (moduleCopy.review, or reviewOf its version, read
// now, when the run has put it in the cache), when one shows it, and the run that consents:
// opts.Rerun, the user's run with --trust adding kinds, else the command that installs c
// (moduleCopy.again) with them.
func (e *Engine) consentHints(sc Scope, c moduleCopy, opts Options, kinds TrustKinds) ([]string, error) {
	review := c.review
	if c.shows != nil {
		var err error
		if review, err = e.reviewOf(opts.staged, *c.shows); err != nil {
			return nil, err
		}
		if review != nil {
			for _, t := range c.reviewTargets {
				review = append(review, "--target", t)
			}
		} else {
			review = c.fromSource
		}
	}
	var hints []string
	if review != nil {
		hints = append(hints, "review it: "+e.Command(sc, "info", append(slices.Clone(review), opts.Display...)...))
	}
	k := opts.Trust | kinds
	// c.at is the version shown, which the consent alone covers (PinRun)
	switch {
	case opts.PinRun != nil && c.at != "":
		return append(hints, consentHintPrefix+opts.PinRun(k, c.at)), nil
	case opts.Rerun != nil:
		return append(hints, consentHintPrefix+opts.Rerun(k)), nil
	case len(c.again) > 0:
		var pin []string
		if c.at != "" {
			pin = []string{"--at", c.at}
		}
		hints = append(hints, consentHintPrefix+e.Command(sc, c.again[0], slices.Concat(c.again[1:], opts.Display, []string{k.Arg()}, pin)...))
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
		if opts.Ask != nil || k.Covers(TrustHooks) {
			break
		}
		if opts.Trust.Covers(TrustHooks) || !c.runHooks || c.forced && !c.pending || !slices.ContainsFunc(c.hooks, func(h module.Hook) bool { return h.Event == PreInstall }) {
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

// ErrConsentDeclined marks a consent refusal the user caused by answering No on a terminal. Unlike
// ErrDeclined, which the CLI turns into a cancel, the refusal prints as it is, and exits 4.
var ErrConsentDeclined = errors.New("consent declined")

// hookConsentError refuses a pre-install hook without consent. Its hints review the version and
// consent to it; a retry without consent would refuse again, so withRetry adds none. The message
// names the module's hooks, each script and its sha256; unseen says the run's --trust covered
// them, but lola had not shown them.
type hookConsentError struct {
	module  string
	scripts []HookScript
	unseen  bool
	// declined: the user was asked and said no, which is a decline (ErrConsentDeclined), not a refusal
	// for want of an answer.
	declined bool
}

func (e *hookConsentError) Error() string {
	var shown []string
	for _, s := range e.scripts {
		shown = append(shown, s.Event+": "+s.Script+" (sha256 "+s.SHA256+")")
	}
	if e.unseen {
		return e.module + " has a pre-install hook lola had not shown you: " + strings.Join(shown, ", ")
	}
	return e.module + " has a pre-install hook, and lola runs hooks only with your consent: " + strings.Join(shown, ", ")
}

// Is marks the refusal as one only the user's answer lifts (errs.ErrNeedsAnswer), or, when the
// user declined the question, as a decline (ErrConsentDeclined).
func (e *hookConsentError) Is(target error) bool {
	return target == errs.ErrNeedsAnswer && !e.declined || target == ErrConsentDeclined && e.declined
}

// planHooks prepares the hooks of every copy that runs them, before anything is written, and
// returns a job for every copy the run installs or replaces, which install scripts wrap:
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
		for k := range scripts {
			held, err := e.Shown.Holds(scripts[k].ShownKey())
			if err != nil {
				return nil, err
			}
			scripts[k].New = new(!held)
		}
		res.Modules[i].hooksKept = c.forced && !c.pending
		pre := slices.ContainsFunc(scripts, func(s HookScript) bool { return s.Event == PreInstall })
		ok, err := e.Consent.Granted(c.name, digest)
		if err == nil && !ok {
			if pre && (!c.forced || c.pending) {
				res.needs |= TrustHooks // a kept copy runs nothing, so needs nothing
				if c.at != "" {
					res.pinned = append(res.pinned, c.at)
				}
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
				held, err := e.Shown.Holds(shownKeys(scripts)...)
				if err != nil {
					return nil, err
				}
				// The refusal names the hooks but does not show them, so it records nothing. Ones lola
				// has not shown are hinted the dry run that shows them first, since --trust alone
				// would be refused for them; unseen is --trust covering them all the same.
				refusal := &hookConsentError{module: c.name, scripts: scripts, unseen: opts.Trust.Covers(TrustHooks) && !held, declined: opts.Ask != nil}
				if k := slices.IndexFunc(scripts, func(s HookScript) bool { return s.Withheld != "" }); k >= 0 {
					hints = withheldInstead(hints, "this hook", scripts[k].Withheld, scripts[k].file)
				} else if !held {
					hints = unseenInstead(hints, "this hook", "it", refusal.unseen)
				}
				return nil, errs.WithHints(refusal, hints...)
			}
			// The hook stays pending in lock.json: sync replaces the copy from the hashed version
			// and asks again.
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
// scripts of the run: before a module's pre-install hooks, and after its
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
				opts.Progress.holding(false, "running %s hook %s of %s for %s", event, untrusted.Text(s.Script, untrusted.MaxName), untrusted.Text(j.name, untrusted.MaxName), id)
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

// runHook runs the executable at path in the module copy of j with the hook environment for event
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
