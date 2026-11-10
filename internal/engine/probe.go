package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/trevor-vaughan/lola-go/internal/fsx"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/targetspec"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
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
		out = append(out, Assistant{Target: t.ID(), Binary: e.findBinary(t.Probe().Binaries()),
			Marked: t.Detect(sc.Kind, t.Root(sc.Kind, sc.ProjectDir, e.Paths))})
	}
	return out
}

// TargetChoice is what decides the assistants an install plans for at a scope off a terminal,
// with no -t: the top-level targets of its write target's file when it has any
// (Manifest, fileTargets: a profile's own, else the base's; From names that file, "" for the base,
// targetsFile),
// else the installed assistants set up there (Here). Installed is the assistants found on PATH and
// SetUp those of them whose project markers the scope has. SetUpOnly is the targets with no binary
// (agents) whose markers the scope has: never installed assistants.
type TargetChoice struct {
	Manifest, Installed, SetUp, SetUpOnly []string
	From                                  string
}

// DefaultTargets is sc's TargetChoice for an install of reqs told req about where it writes,
// reading the scope's manifest as that install would (InstallManifest), whose error it returns as
// it is, and its layers and write target (writeTarget, with the first answer of a question, which
// the install asks or refuses).
func (e *Engine) DefaultTargets(sc Scope, reqs []InstallRequest, req TargetRequest) (TargetChoice, error) {
	man, err := e.InstallManifest(sc, reqs)
	if err != nil {
		return TargetChoice{}, err
	}
	led, err := e.Ledger(sc)
	if err != nil {
		return TargetChoice{}, err
	}
	layers, _, err := e.layers(sc, man, led)
	if err != nil {
		return TargetChoice{}, err
	}
	first := func(q WhereQuestion) (WhereChoice, error) { return q.Choices[0], nil }
	t, _, err := e.writeTarget(sc, layers, nil, req, "install", "", first)
	if err != nil {
		return TargetChoice{}, err
	}
	c := TargetChoice{Manifest: fileTargets(t, man), From: targetsFile(t)}
	for i, as := range e.Detect(sc) {
		switch {
		case target.NoBinary(e.Targets[i]):
			if as.Marked {
				c.SetUpOnly = append(c.SetUpOnly, as.Target)
			}
		case as.Binary != "":
			c.Installed = append(c.Installed, as.Target)
			if as.Marked {
				c.SetUp = append(c.SetUp, as.Target)
			}
		}
	}
	return c, nil
}

// Here is the targets set up in the scope that an install off a terminal plans for when the
// manifest gives none: the installed assistants set up there, else the targets with no binary set
// up there, which are never added beside a set-up assistant.
func (c TargetChoice) Here() []string {
	if len(c.SetUp) > 0 {
		return c.SetUp
	}
	return c.SetUpOnly
}

// RerunTargets is the targets a refusal for c's "no assistant here" hints a run with -t of, as
// install's and info's do: the installed assistants, then the targets with no binary (agents),
// which need none installed.
func (e *Engine) RerunTargets(c TargetChoice) []string {
	ids := slices.Clone(c.Installed)
	for _, t := range e.Targets {
		if target.NoBinary(t) {
			ids = append(ids, t.ID())
		}
	}
	return ids
}

// NoneHere is why c gives no assistant ("" when it gives some): none is installed, or none of
// those installed is set up in the scope, and no target without a binary is set up there either.
func (c TargetChoice) NoneHere() string {
	switch {
	case len(c.Manifest) > 0 || len(c.Here()) > 0:
		return ""
	case len(c.Installed) == 0:
		return "no assistant found on your PATH, so lola does not know where to install"
	}
	return "none of your installed assistants (" + strings.Join(c.Installed, ", ") + ") is set up here, so lola does not know where to install"
}

// findBinary returns the first of names that is an executable regular file in a directory of
// e.Paths.Search, which holds only absolute PATH entries.
func (e *Engine) findBinary(names []string) string {
	for _, name := range names {
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
// It also asks git, once per file, whether the project could commit a settings file, and reads
// each scope's target_versions once.
type probeRun struct {
	e        *Engine
	ctx      context.Context
	versions map[string]string
	findings []target.Finding
	exposed  map[string]string            // settingsExposed by project-relative file, once known
	declares map[string]declaration       // Engine.declared by Scope.Key, once read
	eras     map[string]bool              // scope key and target id whose era findings are recorded
	chosen   map[string]map[string]string // by Scope.Key, the versions an install is given, which declared reads in place of the scope's
	// mismatches are the version/era-mismatch findings, by Scope.Key, apart from findings: a run
	// reports a scope's only when it writes files there (mismatched).
	mismatches map[string][]target.Finding
}

// mismatched is the version/era-mismatch findings recorded for sc.
func (r *probeRun) mismatched(sc Scope) []target.Finding { return r.mismatches[sc.Key()] }

// declaration is what Engine.declared returned for a scope.
type declaration struct {
	versions map[string]string
	file     string
	err      error
}

// settings is settingsExposed for abs, the absolute path of a settings file a target writes in
// the project at dir, checked once per command. It also returns abs relative to dir, in slash
// form, which is how git and the coaching name it. A file outside dir cannot be checked against
// the project's repository, so it is exposed (fail closed).
func (r *probeRun) settings(dir, abs string) (why, rel string) {
	rel, err := filepath.Rel(dir, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Sprintf("lola could not check that git ignores %s (it is outside the project)", abs), abs
	}
	rel = filepath.ToSlash(rel)
	why, ok := r.exposed[rel]
	if !ok {
		why = settingsExposed(r.ctx, dir, rel)
		r.exposed[rel] = why
	}
	return why, rel
}

func (e *Engine) newProbeRun(ctx context.Context) *probeRun {
	return &probeRun{e: e, ctx: ctx, versions: map[string]string{}, exposed: map[string]string{}, declares: map[string]declaration{},
		eras: map[string]bool{}, chosen: map[string]map[string]string{}, mismatches: map[string][]target.Finding{}}
}

// declared is Engine.declared for sc, read once per run, with the versions the run is given for
// sc (chosen) in place of those it declares.
func (r *probeRun) declared(sc Scope) (map[string]string, string, error) {
	d, ok := r.declares[sc.Key()]
	if !ok {
		d.versions, d.file, d.err = r.e.declared(sc)
		if chosen := r.chosen[sc.Key()]; d.err == nil && len(chosen) > 0 {
			d.versions = maps.Clone(d.versions)
			if d.versions == nil {
				d.versions = map[string]string{}
			}
			maps.Copy(d.versions, chosen)
		}
		r.declares[sc.Key()] = d
	}
	return d.versions, d.file, d.err
}

// version returns the installed version of t's assistant, or "" when it is not installed or
// its version cannot be read; either case is recorded as an info finding. Once per scope and
// target, it records a warning when what sc declares and what is installed disagree (eraFinding).
func (r *probeRun) version(sc Scope, t target.Target) string {
	id := t.ID()
	v, ok := r.versions[id]
	if !ok {
		if f := r.e.Registry.Replaced(id); f != "" {
			r.findings = append(r.findings, target.Finding{Severity: target.SeverityInfo, Target: id, Kind: "assistant",
				Code: "target/user-spec", Text: "uses your target spec " + untrusted.Text(f, untrusted.MaxName) + " in place of lola's built-in"})
		}
		var f *target.Finding
		v, f = r.read(t)
		r.versions[id] = v
		if f != nil {
			r.findings = append(r.findings, *f)
		}
	}
	if key := sc.Key() + "\x00" + id; !r.eras[key] {
		r.eras[key] = true
		switch f := r.eraFinding(sc, id, v); {
		case f == nil:
		case f.Code == "version/era-mismatch":
			r.mismatches[sc.Key()] = append(r.mismatches[sc.Key()], *f)
		default:
			r.findings = append(r.findings, *f)
		}
	}
	return v
}

// eraFinding is the warning for id at sc when the version sc declares lies below every era lola
// knows, or when the installed version probed is of another era than the one lola writes for:
// the declared one's, or the newest when sc declares none. nil when they agree, when the run
// records a version it is given for id (chosen), or when sc's declaration cannot be read (whoever
// resolves the target refuses that).
func (r *probeRun) eraFinding(sc Scope, id, probed string) *target.Finding {
	tv, file, err := r.declared(sc)
	if err != nil {
		return nil
	}
	warn := func(code, text, hint string) *target.Finding {
		return &target.Finding{Severity: target.SeverityWarn, Target: id, Kind: "assistant", Code: code, Text: text, Hint: hint}
	}
	file = untrusted.Text(file, untrusted.MaxName)
	decl, has := tv[id]
	since, before, ok := r.e.Registry.Era(id, decl)
	if !ok {
		return nil
	}
	installed := ""
	if targetspec.Canonical(probed) != "" {
		installed, _, _ = r.e.Registry.Era(id, probed)
	}
	shownDecl, shownProbed := untrusted.Text(decl, untrusted.MaxName), untrusted.Text(probed, untrusted.MaxName)
	switch {
	case has && before:
		return warn("version/before-first-era",
			fmt.Sprintf("%s declares %s %s, older than any %s lola knows; lola writes for %s %s", file, id, shownDecl, id, id, since),
			fmt.Sprintf("upgrade %s to %s or later, then set target_versions.%s to match in %s", id, since, id, file))
	case installed == "" || installed == since:
		return nil
	case sc.Kind == target.Project && !r.e.FoundHere(sc):
		// This machine's assistant says nothing of the one reading a project in another directory.
		return nil
	case r.chosen[sc.Key()][id] != "":
		// The run records the version it was given; its notice says which one is installed here.
		return nil
	case has:
		return warn("version/era-mismatch",
			fmt.Sprintf("%s declares %s %s, but %s %s is installed; lola writes for %s", file, id, shownDecl, id, shownProbed, shownDecl),
			fmt.Sprintf("%s; or install a release of %s %s", r.e.versionsEdit(sc, file, id, probed), id, r.e.eraOf(id, decl).label))
	}
	return warn("version/undeclared",
		fmt.Sprintf("%s %s is installed, but %s declares no version for it, so lola writes for %s %s", id, shownProbed, file, id, since),
		fmt.Sprintf("add %s: %q under target_versions: in %s", id, shownProbed, file))
}

// read tries t's probe methods in order and returns the first version one yields. A method whose
// binary or file is missing is passed over; when none yields a version, the first failure is the
// finding, else the first missing binary or file.
func (r *probeRun) read(t target.Target) (string, *target.Finding) {
	p := t.Probe()
	if len(p.Methods) == 0 {
		return "", nil
	}
	note := func(code, text string) *target.Finding {
		return &target.Finding{Severity: target.SeverityInfo, Target: t.ID(), Kind: "assistant", Code: code, Text: text}
	}
	var failed *target.Finding
	for _, m := range p.Methods {
		var v string
		var found bool
		var err error
		if m.File != nil {
			v, found, err = fileVersion(m.File(r.e.Paths), m)
		} else {
			v, found, err = r.execVersion(t.ID(), m)
		}
		switch {
		case err != nil && failed == nil:
			failed = note("probe/unknown-version", err.Error())
		case found && err == nil:
			return v, nil
		}
	}
	if failed != nil {
		return "", failed
	}
	for _, m := range p.Methods {
		if m.File == nil {
			return "", note("probe/not-found", untrusted.Text(m.Binaries[0], untrusted.MaxName)+" is not on PATH")
		}
	}
	return "", note("probe/not-found", untrusted.Text(p.Methods[0].File(r.e.Paths), untrusted.MaxName)+" does not exist")
}

// execVersion runs the first of m's binaries found on PATH and reads the version it prints;
// found is false when none is. A version read is cached by the binary's size and mtime; one
// that could not be cached is still returned, with a probe/cache finding recorded for target id.
func (r *probeRun) execVersion(id string, m target.ProbeMethod) (v string, found bool, err error) {
	bin := r.e.findBinary(m.Binaries)
	if bin == "" {
		return "", false, nil
	}
	fi, err := os.Stat(bin)
	if err != nil {
		return "", true, err
	}
	cache := r.e.loadProbes()
	if c, ok := cache[bin]; ok && c.Size == fi.Size() && c.ModTime.Equal(fi.ModTime()) {
		return c.Version, true, nil
	}
	ctx, cancel := context.WithTimeout(r.ctx, r.e.ProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, m.Args...)
	cmd.Env = r.e.Environ()
	cmd.WaitDelay = r.e.ProbeTimeout // do not wait forever on children that keep the output open
	out := &headWriter{max: probeOutputLimit}
	cmd.Stdout = out
	stderr := &headWriter{max: probeOutputLimit}
	cmd.Stderr = stderr
	run := untrusted.Text(bin+" "+strings.Join(m.Args, " "), untrusted.MaxName)
	if err := cmd.Run(); err != nil {
		text := fmt.Sprintf("%s failed: %v", run, untrusted.CleanErr(err))
		if why := strings.Join(strings.Fields(string(stderr.buf)), " "); why != "" {
			text += ": " + untrusted.Text(why, untrusted.MaxName)
		}
		return "", true, errors.New(text)
	}
	sub := m.Match.FindSubmatch(out.buf)
	if sub == nil {
		return "", true, fmt.Errorf("%s printed no version lola recognizes", run)
	}
	v = string(sub[1])
	if targetspec.Canonical(v) == "" {
		return "", true, fmt.Errorf("%s gave %q, which is not a version", run, untrusted.Text(v, untrusted.MaxName))
	}
	cache[bin] = probeEntry{Size: fi.Size(), ModTime: fi.ModTime(), Version: v}
	if err := r.e.saveProbes(cache); err != nil {
		r.findings = append(r.findings, target.Finding{Severity: target.SeverityInfo, Target: id, Kind: "assistant",
			Code: "probe/cache", Text: "the version was read but could not be cached: " + err.Error()})
	}
	return v, true, nil
}

// probeFileLimit is the largest version file lola reads.
const probeFileLimit = 1 << 20

// fileVersion reads the version from the file at path, by m's Match or its Pointer into the
// file's JSON; found is false when the file does not exist. Only a regular file of at most
// probeFileLimit bytes is read, so a FIFO or a device never blocks or floods lola.
func fileVersion(path string, m target.ProbeMethod) (v string, found bool, err error) {
	name := untrusted.Text(path, untrusted.MaxName)
	// Opened without blocking and checked on the open file, so a FIFO swapped in after a stat
	// cannot hold lola: the type and size are those of the file read.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", true, untrusted.CleanErr(err)
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	switch {
	case err != nil:
		return "", true, untrusted.CleanErr(err)
	case !fi.Mode().IsRegular():
		return "", true, fmt.Errorf("%s is not a regular file", name)
	case fi.Size() > probeFileLimit:
		return "", true, fmt.Errorf("%s is larger than 1 MiB", name)
	}
	b, err := io.ReadAll(io.LimitReader(f, probeFileLimit+1))
	switch {
	case err != nil:
		return "", true, untrusted.CleanErr(err)
	case len(b) > probeFileLimit:
		return "", true, fmt.Errorf("%s is larger than 1 MiB", name)
	}
	if m.Match != nil {
		sub := m.Match.FindSubmatch(b)
		if sub == nil {
			return "", true, fmt.Errorf("%s holds no version lola recognizes", name)
		}
		v = string(sub[1])
	} else {
		var doc any
		if err := json.Unmarshal(b, &doc); err != nil {
			return "", true, fmt.Errorf("%s is not JSON: %w", name, err)
		}
		s, ok := pointed(doc, m.Pointer)
		if ok {
			v, ok = s.(string)
		}
		if !ok {
			return "", true, fmt.Errorf("%s has no version string at %s", name, untrusted.Text(m.Pointer, untrusted.MaxName))
		}
	}
	if targetspec.Canonical(v) == "" {
		return "", true, fmt.Errorf("%s gave %q, which is not a version", name, untrusted.Text(v, untrusted.MaxName))
	}
	return v, true, nil
}

// pointed is the value at the JSON pointer ptr in v ("" for the whole document).
func pointed(v any, ptr string) (any, bool) {
	if ptr == "" {
		return v, true
	}
	for _, tok := range strings.Split(ptr[1:], "/") {
		tok = strings.ReplaceAll(strings.ReplaceAll(tok, "~1", "/"), "~0", "~")
		switch x := v.(type) {
		case map[string]any:
			var ok bool
			if v, ok = x[tok]; !ok {
				return nil, false
			}
		case []any:
			i, err := strconv.Atoi(tok)
			if err != nil || i < 0 || i >= len(x) || tok != strconv.Itoa(i) {
				return nil, false
			}
			v = x[i]
		default:
			return nil, false
		}
	}
	return v, true
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
