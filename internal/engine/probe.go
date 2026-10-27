package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/trevor-vaughan/lola-go/internal/fsx"
	"github.com/trevor-vaughan/lola-go/internal/target"
)

// Assistant is what lola found of one target's assistant in a scope.
type Assistant struct {
	Target string `json:"target"`
	Binary string `json:"binary,omitempty"` // resolved path of the assistant's binary; "" when not found
	Marked bool   `json:"marked"`           // the scope has the assistant's project markers
}

// Detect reports, for every target in registration order, whether its assistant is installed
// (found on PATH) and set up in sc (its project markers exist). Nothing is executed.
func (e *Engine) Detect(sc Scope) []Assistant {
	var out []Assistant
	for _, t := range e.Targets {
		out = append(out, Assistant{Target: t.ID(), Binary: e.findBinary(t.Probe()),
			Marked: t.Detect(sc.Kind, t.Root(sc.Kind, sc.ProjectDir, e.Paths))})
	}
	return out
}

// TargetChoice is what decides the assistants an install plans for at a scope off a terminal,
// with no -t (Plan 5c §5.3): the scope manifest's targets when it lists any, else the installed
// assistants set up there. Installed is the assistants found on PATH and SetUp those of them whose
// project markers the scope has.
type TargetChoice struct {
	Manifest, Installed, SetUp []string
}

// DefaultTargets is sc's TargetChoice for an install of reqs, reading the scope's manifest as that
// install would (InstallManifest), whose error it returns as it is.
func (e *Engine) DefaultTargets(sc Scope, reqs []InstallRequest) (TargetChoice, error) {
	man, err := e.InstallManifest(sc, reqs)
	if err != nil {
		return TargetChoice{}, err
	}
	c := TargetChoice{Manifest: man.Targets}
	for _, as := range e.Detect(sc) {
		if as.Binary == "" {
			continue
		}
		c.Installed = append(c.Installed, as.Target)
		if as.Marked {
			c.SetUp = append(c.SetUp, as.Target)
		}
	}
	return c, nil
}

// NoneHere is why c gives no assistant ("" when it gives some): none is installed, or none of
// those installed is set up in the scope.
func (c TargetChoice) NoneHere() string {
	switch {
	case len(c.Manifest) > 0 || len(c.SetUp) > 0:
		return ""
	case len(c.Installed) == 0:
		return "no assistant found on your PATH, so lola does not know where to install"
	}
	return "none of your installed assistants (" + strings.Join(c.Installed, ", ") + ") is set up here, so lola does not know where to install"
}

// findBinary returns the first of p.Binaries that is an executable regular file in a
// directory of e.Paths.Search, which holds only absolute PATH entries.
func (e *Engine) findBinary(p target.Probe) string {
	for _, name := range p.Binaries {
		for _, dir := range e.Paths.Search {
			path := filepath.Join(dir, name)
			if fi, err := os.Stat(path); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
				return path
			}
		}
	}
	return ""
}

// probeEntry is one cached version, valid while the binary keeps its size and mtime.
type probeEntry struct {
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
	Version string    `json:"version"`
}

// probeRun reads the installed versions of the assistants one command renders for. Each
// target is probed once; a binary is executed only when the cache has no entry for its build.
// It also asks git, once, whether the project could commit .claude/settings.local.json.
type probeRun struct {
	e        *Engine
	ctx      context.Context
	versions map[string]string
	findings []target.Finding
	exposed  *string // localSettingsExposed, once known
}

// localSettings is localSettingsExposed for the project at dir, checked once per command.
func (r *probeRun) localSettings(dir string) string {
	if r.exposed == nil {
		s := localSettingsExposed(r.ctx, dir)
		r.exposed = &s
	}
	return *r.exposed
}

func (e *Engine) newProbeRun(ctx context.Context) *probeRun {
	return &probeRun{e: e, ctx: ctx, versions: map[string]string{}}
}

// version returns the installed version of t's assistant, or "" when it is not installed or
// its version cannot be read; either case is recorded as an info finding.
func (r *probeRun) version(t target.Target) string {
	id := t.ID()
	if v, ok := r.versions[id]; ok {
		return v
	}
	v, f := r.read(t)
	r.versions[id] = v
	if f != nil {
		r.findings = append(r.findings, *f)
	}
	return v
}

func (r *probeRun) read(t target.Target) (string, *target.Finding) {
	p := t.Probe()
	if len(p.Binaries) == 0 {
		return "", nil
	}
	note := func(code, text string) *target.Finding {
		return &target.Finding{Severity: target.SeverityInfo, Target: t.ID(), Kind: "assistant", Code: code, Text: text}
	}
	bin := r.e.findBinary(p)
	if bin == "" {
		return "", note("probe/not-found", fmt.Sprintf("%s is not on PATH; checked against the newest version lola knows", p.Binaries[0]))
	}
	fi, err := os.Stat(bin)
	if err != nil {
		return "", note("probe/unknown-version", err.Error())
	}
	cache := r.e.loadProbes()
	if c, ok := cache[bin]; ok && c.Size == fi.Size() && c.ModTime.Equal(fi.ModTime()) {
		return c.Version, nil
	}
	ctx, cancel := context.WithTimeout(r.ctx, r.e.ProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, p.VersionArgs...)
	cmd.Env = r.e.Environ()
	cmd.WaitDelay = r.e.ProbeTimeout // do not wait forever on children that keep the output open
	out := &headWriter{max: probeOutputLimit}
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		return "", note("probe/unknown-version", fmt.Sprintf("%s %s failed: %v", bin, strings.Join(p.VersionArgs, " "), err))
	}
	m := p.VersionRE.FindSubmatch(out.buf)
	if m == nil {
		return "", note("probe/unknown-version", fmt.Sprintf("%s %s printed no version lola recognizes", bin, strings.Join(p.VersionArgs, " ")))
	}
	v := string(m[1])
	cache[bin] = probeEntry{Size: fi.Size(), ModTime: fi.ModTime(), Version: v}
	if err := r.e.saveProbes(cache); err != nil {
		return v, note("probe/cache", "the version was read but could not be cached: "+err.Error())
	}
	return v, nil
}

// probeOutputLimit is how much of a version probe's output lola reads; a version is printed
// first, and an assistant that prints megabytes must not grow lola's memory with it.
const probeOutputLimit = 4 << 10

// headWriter keeps the first max bytes written to it and discards the rest. It keeps
// accepting writes so the child never blocks on a full pipe.
type headWriter struct {
	buf []byte
	max int
}

func (w *headWriter) Write(p []byte) (int, error) {
	if room := w.max - len(w.buf); room > 0 {
		w.buf = append(w.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

// loadProbes reads the probe cache. It is lola's own disposable cache: a missing or unreadable
// file is an empty cache, and the next successful probe rewrites it.
func (e *Engine) loadProbes() map[string]probeEntry {
	cache := map[string]probeEntry{}
	b, err := os.ReadFile(e.Paths.Probes)
	if err != nil {
		return cache
	}
	if json.Unmarshal(b, &cache) != nil {
		return map[string]probeEntry{}
	}
	return cache
}

func (e *Engine) saveProbes(cache map[string]probeEntry) error {
	b, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(e.Paths.Probes), 0o700); err != nil {
		return err
	}
	return fsx.WriteFileAtomic(e.Paths.Probes, append(b, '\n'), 0o600)
}
