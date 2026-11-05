package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/trust"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// ContentMCPServer is a module MCP server as the trust diff shows it, which is the only consent
// it gets (L1): Runs is the command with each argument quoted unless plain, so the arguments'
// boundaries stay visible, or the URL; Env ("NAME=value") and Headers ("Name: value") are
// sorted, and Cwd is "" when unset, since each can change what runs or where data goes. Change
// is how it differs from the version installed (Plan 6a §7.3: added, changed, unchanged or
// removed); a changed server carries Previous, its installed form.
type ContentMCPServer struct {
	Name      string            `json:"name"`
	Transport string            `json:"transport"`
	Change    string            `json:"change,omitempty"`
	Runs      string            `json:"runs"`
	Env       []string          `json:"env"`
	Headers   []string          `json:"headers"`
	Cwd       string            `json:"cwd"`
	Previous  *ContentMCPServer `json:"previous,omitempty"`
}

// MCP servers and hooks are marked by how they differ from the version installed (Plan 6a §7.3).
const ChangeUnchanged = "unchanged"

// FileChange is a file a module version adds, changes or removes (trust.Change kinds).
type FileChange struct {
	Path   string `json:"path"`
	Change string `json:"change"`
}

// Kinds of hooks a module's content declares.
const (
	HookKindInstall   = "install"    // an install hook: Event is pre-install or post-install, Detail its script
	HookKindRuntime   = "runtime"    // a runtime hook handler an assistant runs on Event
	HookKindMCPServer = "mcp_server" // an agent's inline MCP server
)

// ContentHook is one hook a module's content declares, as the trust diff shows it: Target is the
// assistant a runtime hook or agent MCP server is rendered for ("" for an install hook), Detail
// what it runs or where it connects. A hook is identified by all its fields, so Change is added,
// changed, unchanged or removed against the version installed: a hook whose command changed is one
// removed and one added, while an install hook whose script file changed is changed.
type ContentHook struct {
	Kind   string `json:"kind"`
	Target string `json:"target,omitempty"`
	Event  string `json:"event"`
	Detail string `json:"detail"`
	Change string `json:"change"`
}

// fillContent fills the content of each added or changed module of rep from the version the sync
// resolved for it (Plan 6a §7.3): files against the version lock.json records, from the cache
// (never fetched: a version the cache lost lists every file as added), MCP servers, and every
// hook the content declares (declaredRuntimeHooks), each MCP server and hook marked against the
// version installed. A module nothing was fetched for yet is left without content.
func (e *Engine) fillContent(sc Scope, rep *TrustReport, entries []*syncEntry, led *ledger.Ledger, probes *probeRun) error {
	for i := range rep.Changes {
		c := &rep.Changes[i]
		k := slices.IndexFunc(entries, func(se *syncEntry) bool { return se.ent.Name == c.Module })
		if c.Kind == trust.ChangeRemoved || k < 0 || entries[k].v.ContentDir == "" {
			continue
		}
		se := entries[k]
		now, err := e.contentHooks(sc, se.v, se.pinned(), probes)
		if err != nil {
			return fmt.Errorf("%s: runtime hooks: %w", se.ent.Name, err)
		}
		old, note := e.comparedVersion(se, led.Modules[se.ent.Name])
		var was []ContentHook
		if old != nil {
			// The version installed is only what this one is compared with: when its files cannot be
			// read or its hooks planned, nothing is compared and everything is listed as added.
			if c.Files, was, err = e.compareInstalled(sc, se, old, probes); err != nil {
				old, note = nil, unreadableNote(err)
			}
		}
		if old == nil {
			was = nil
			if c.Files, err = fileChanges(se.v, nil); err != nil {
				return err
			}
		}
		c.FilesNote = note
		var before []module.MCPServer
		if old != nil {
			before = old.Module.MCPServers
		}
		c.MCPServers = markServers(se.v.Module.MCPServers, before, old != nil)
		c.Hooks = markHooks(now, was, old != nil, c.Files)
	}
	return nil
}

// compareInstalled lists the files se's version changes against old, the version installed, and
// plans old's hooks, which are all that reading old can fail on.
func (e *Engine) compareInstalled(sc Scope, se *syncEntry, old *store.Version, probes *probeRun) ([]FileChange, []ContentHook, error) {
	files, err := fileChanges(se.v, old)
	if err != nil {
		return nil, nil, err
	}
	was, err := e.contentHooks(sc, *old, se.pinned(), probes)
	return files, was, err
}

// unreadableNote says why the version installed was not compared with.
func unreadableNote(err error) string {
	return "lola could not read the version installed from your cache (" + shown(untrusted.CleanErr(err).Error()) +
		"), so every file is listed as added"
}

// comparedVersion is the version lock.json records for se's module, read from the cache, which
// se's version is compared with: se's own version when it is the one installed, nil with nothing
// installed. When the cache no longer holds it, or cannot read it, it is nil with a note saying
// why, and the old version is never fetched to compare: the diff is the remedy for a damaged
// cache, so the cache never blocks it.
func (e *Engine) comparedVersion(se *syncEntry, installed *ledger.Entry) (*store.Version, string) {
	switch {
	case installed == nil:
		return nil, ""
	case installed.Digest == se.v.Meta.Digest:
		return &se.v, ""
	}
	old, err := e.Store.Load(se.ent.Name, versionID(installed.Digest))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, "the version installed is not in your cache, so every file is listed as added"
	case err != nil:
		return nil, unreadableNote(err)
	}
	return &old, ""
}

// markServers is each MCP server of now, the version a sync installs, as the diff shows it and
// marked against before, the version installed (matched by name): added, changed (with its
// previous form), unchanged; then each server of before that now lacks, removed. Without a
// version to compare (installed false) every server is added.
func markServers(now, before []module.MCPServer, installed bool) []ContentMCPServer {
	out := []ContentMCPServer{}
	for _, srv := range now {
		c := contentMCPServer(srv)
		c.Change = trust.ChangeAdded
		if k := slices.IndexFunc(before, func(b module.MCPServer) bool { return b.Name == srv.Name }); installed && k >= 0 {
			c.Change = ChangeUnchanged
			if !sameServer(srv, before[k]) {
				prev := contentMCPServer(before[k])
				c.Change, c.Previous = trust.ChangeChanged, &prev
			}
		}
		out = append(out, c)
	}
	for _, b := range before {
		if installed && !slices.ContainsFunc(now, func(n module.MCPServer) bool { return n.Name == b.Name }) {
			c := contentMCPServer(b)
			c.Change = trust.ChangeRemoved
			out = append(out, c)
		}
	}
	return out
}

// sameServer reports whether two servers are the same in every field, compared raw: the diff's
// strings drop characters, so two servers can look alike, and the targets are not shown at all.
func sameServer(a, b module.MCPServer) bool {
	return a.Transport == b.Transport && a.Command == b.Command && slices.Equal(a.Args, b.Args) && maps.Equal(a.Env, b.Env) &&
		a.Cwd == b.Cwd && a.URL == b.URL && maps.Equal(a.Headers, b.Headers) && slices.Equal(a.Targets, b.Targets)
}

// contentMCPServer is srv as the trust diff shows it, every string through untrusted.Text.
func contentMCPServer(srv module.MCPServer) ContentMCPServer {
	runs := srv.URL
	if srv.Transport == module.TransportStdio {
		words := []string{word(srv.Command)}
		for _, a := range srv.Args {
			words = append(words, word(a))
		}
		runs = strings.Join(words, " ")
	}
	out := ContentMCPServer{Name: shown(srv.Name), Transport: shown(srv.Transport), Runs: shown(runs), Env: []string{}, Headers: []string{}}
	if srv.Cwd != "" {
		out.Cwd = shown(word(srv.Cwd))
	}
	for _, k := range slices.Sorted(maps.Keys(srv.Env)) {
		out.Env = append(out.Env, shown(k+"="+srv.Env[k]))
	}
	for _, k := range slices.Sorted(maps.Keys(srv.Headers)) {
		out.Headers = append(out.Headers, shown(k+": "+srv.Headers[k]))
	}
	return out
}

// markHooks marks each hook of now against was, the version installed's hooks: one in both is
// unchanged, one only in now added, one only in was removed; an install hook in both whose script
// is among files, the changed files, is changed. Without a version to compare (installed false)
// every hook is added.
func markHooks(now, was []ContentHook, installed bool, files []FileChange) []ContentHook {
	out := []ContentHook{}
	left := slices.Clone(was)
	for _, h := range now {
		change := trust.ChangeAdded
		if k := slices.Index(left, h); installed && k >= 0 {
			change = ChangeUnchanged
			if h.Kind == HookKindInstall && slices.ContainsFunc(files, func(f FileChange) bool {
				return f.Change == trust.ChangeChanged && path.Clean(f.Path) == path.Clean(h.Detail)
			}) {
				change = trust.ChangeChanged
			}
			left = slices.Delete(left, k, k+1)
		}
		h.Change = change
		out = append(out, h)
	}
	for _, h := range left {
		if installed {
			h.Change = trust.ChangeRemoved
			out = append(out, h)
		}
	}
	return out
}

// contentHooks is every hook v declares, as the diff shows it, unmarked: its install hooks, then
// its runtime hooks and agent MCP servers (declaredRuntimeHooks).
func (e *Engine) contentHooks(sc Scope, v store.Version, ent manifest.Entry, probes *probeRun) ([]ContentHook, error) {
	var out []ContentHook
	for _, h := range v.Module.DeclaredHooks {
		out = append(out, ContentHook{Kind: HookKindInstall, Event: shown(h.Event), Detail: shown(h.Script)})
	}
	hooks, err := e.declaredRuntimeHooks(sc, v, ent, probes)
	if err != nil {
		return nil, err
	}
	for _, h := range hooks {
		out = append(out, contentHook(h))
	}
	return out, nil
}

// declaredRuntimeHooks is every runtime hook and agent MCP server v declares, as each assistant
// lola knows would render it for the entry ent, whatever its targets and the user's selections
// (Plan 6a §7.3, L1). Describing runs no assistant: one probes has not read is planned as the
// newest version lola knows.
func (e *Engine) declaredRuntimeHooks(sc Scope, v store.Version, ent manifest.Entry, probes *probeRun) ([]RuntimeHook, error) {
	quiet := &probeRun{e: e, ctx: probes.ctx, versions: maps.Clone(probes.versions), exposed: probes.exposed}
	ids := make([]string, len(e.Targets))
	for i, t := range e.Targets {
		ids[i] = t.ID()
		if _, ok := quiet.versions[ids[i]]; !ok {
			quiet.versions[ids[i]] = ""
		}
	}
	intents, _, _, _, err := e.planModule(sc, v, ids, &ent, config.Exclusions{}, Options{}, quiet)
	if err != nil {
		return nil, err
	}
	var gated []plan.Intent
	for _, in := range intents {
		if in.Consent != nil {
			gated = append(gated, in)
		}
	}
	hooks, _, err := describeRuntimeHooks(gated, v.Meta.Digest)
	return hooks, err
}

// contentHook is a runtime hook or agent MCP server as the trust diff shows it; its strings are
// already safe to print (describeRuntimeHooks).
func contentHook(h RuntimeHook) ContentHook {
	var parts []string
	if h.Component != "" {
		parts = append(parts, h.Component)
	}
	if h.MCPServer != "" {
		parts = append(parts, h.MCPServer+" ("+h.Type+")")
	} else {
		if h.Matcher != "" {
			parts = append(parts, "matcher "+h.Matcher)
		}
		parts = append(parts, h.Type)
	}
	for _, f := range []struct{ label, value string }{{"runs", h.Command}, {"url", h.URL}, {"calls tool", h.Tool}, {"prompt", h.Prompt}} {
		if f.value != "" {
			parts = append(parts, f.label+" "+f.value)
		}
	}
	kind := HookKindRuntime
	if h.MCPServer != "" {
		kind = HookKindMCPServer
	}
	return ContentHook{Kind: kind, Target: h.Target, Event: h.Event, Detail: strings.Join(parts, ": ")}
}

// generatedManifest is the lola.yml module.Normalize writes into every cached version (Plan 6a
// §5.1).
const generatedManifest = "lola.yml"

// fileChanges lists the files v adds, changes (content or execute bit) and removes against old,
// the version installed; with none (nil) every file is added.
func fileChanges(v store.Version, old *store.Version) ([]FileChange, error) {
	now, err := contentFiles(v.ContentDir, v.Meta.Executables)
	if err != nil {
		return nil, err
	}
	var before map[string]string
	if old != nil {
		if before, err = contentFiles(old.ContentDir, old.Meta.Executables); err != nil {
			return nil, err
		}
	}
	out := []FileChange{}
	for _, p := range slices.Sorted(maps.Keys(now)) {
		switch was, ok := before[p]; {
		case !ok && p == generatedManifest:
			// lola's own generated lola.yml comes with every new module; it is listed only when it
			// changes or goes (Plan 6a §7.3).
		case !ok:
			out = append(out, FileChange{Path: shown(p), Change: trust.ChangeAdded})
		case was != now[p]:
			out = append(out, FileChange{Path: shown(p), Change: trust.ChangeChanged})
		}
	}
	for _, p := range slices.Sorted(maps.Keys(before)) {
		if _, ok := now[p]; !ok {
			out = append(out, FileChange{Path: shown(p), Change: trust.ChangeRemoved})
		}
	}
	slices.SortStableFunc(out, func(a, b FileChange) int { return strings.Compare(a.Path, b.Path) })
	return out, nil
}

// contentFiles maps each regular file under dir, by slash path, to the sha256 of its bytes and
// whether execs (the version's executables) lists it.
func contentFiles(dir string, execs []string) (map[string]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		files = append(files, filepath.ToSlash(rel))
		return err
	})
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, rel := range files {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(b)
		out[rel] = hex.EncodeToString(sum[:]) + fmt.Sprint(slices.Contains(execs, rel))
	}
	return out, nil
}
