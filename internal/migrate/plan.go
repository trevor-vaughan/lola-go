package migrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// assistants maps the Python lola's assistants that lola-go renders to its target ids.
// cursor, copilot-cli, copilot-vscode and openclaw have no lola-go target.
var assistants = map[string]string{"claude-code": "claude-code", "gemini-cli": "gemini-cli", "opencode": "opencode"}

// kinds maps a source.yml type to the kind lola-go must read the same source as.
var kinds = map[string]source.Kind{"git": source.KindGit, "zip": source.KindArchive, "tar": source.KindArchive,
	"zipurl": source.KindArchive, "tarurl": source.KindArchive, "folder": source.KindFolder}

// Kinds of recorded paths and of items that are not migrated.
const (
	KindSkill      = "skill"
	KindCommand    = "command"
	KindAgent      = "agent"
	KindMCP        = "mcp"
	KindModuleCopy = "module-copy"

	ItemInstructions = "instructions"
	ItemGeminiSkills = "gemini-skills"
	ItemMCP          = "mcp"
	ItemPluginData   = "plugin-data"
	ItemLolaReq      = "lola-req"
	ItemMarket       = "market"
)

// Plan is what lola migrate would do with the Python lola's state, before the install is
// planned: the destinations (Plan 5c §14), the records that cannot be migrated anywhere, and
// what is reported but never acted on.
type Plan struct {
	Home         string
	Destinations []*Destination
	Unmappable   []Unmapped
	NotMigrated  []Item
	Warnings     []string

	st *State
}

// Destination is one project root, or the user scope, with the records the Python lola
// installed there.
type Destination struct {
	Scope engine.Scope
	// ID names the destination's trash subdirectory: "user", or a hash of the project's
	// canonical key.
	ID string
	// Root is the project root, or the user's home directory.
	Root string
	// Targets are the targets of every record that can be migrated here, in registry order:
	// the manifest's targets when it has none yet.
	Targets     []string
	Installs    []Install
	Already     []Install
	Recorded    []Recorded
	Unmappable  []Unmapped
	NotMigrated []Item

	man      *manifest.Manifest
	pending  []Record            // the records grouped here, until plan decides them
	accepted []planned           // the records plan accepted, for coachClash
	versions map[string]resolved // by installKey
}

// Install is one module lola-go installs at a destination (or found already migrated): its
// source as lola install takes it, the targets of the Python records it replaces, and their
// module names. Module is the lola-go name when it is known, from the manifest. Version is
// the cached version the plan resolved the source to, which the run installs (unset for an
// install found already migrated).
type Install struct {
	Module        string
	PythonModules []string
	Kind          source.Kind
	Source        string
	Ref           string
	Path          string
	Targets       []string
	Version       *engine.VersionRef
}

// Recorded is a path the Python lola wrote for a record and recorded in installed.yml (§6):
// a file, a directory, or with Key an MCP entry in the JSON file at Rel. Rel is relative to
// Root, the target's scope root; Target is "" for a module copy.
type Recorded struct {
	Module string
	Target string
	Kind   string
	Name   string
	Root   string
	Rel    string
	Key    string
	Dir    bool
}

// Abs is the recorded path on disk.
func (r Recorded) Abs() string { return filepath.Join(r.Root, filepath.FromSlash(r.Rel)) }

// Unmapped is a record lola-go leaves exactly as it is, with the reason and what to do.
type Unmapped struct {
	Module      string   `json:"module"`
	Assistant   string   `json:"assistant"`
	Scope       string   `json:"scope"`
	ProjectPath string   `json:"project_path,omitempty"`
	Reason      string   `json:"reason"`
	Hints       []string `json:"hints"`
}

// Item is something the Python lola wrote that lola migrate reports and never touches (§5):
// its file and marker (or JSON pointer), or its path, and how to remove it by hand.
type Item struct {
	What   string   `json:"what"`
	Module string   `json:"module,omitempty"`
	Path   string   `json:"path"`
	Marker string   `json:"marker,omitempty"`
	Hints  []string `json:"hints"`
}

// NewPlan groups st's records into destinations and decides each record's outcome (§4): a
// target lola-go has, at a usable destination, with a source lola-go can fetch, is installed
// unless the destination's manifest already has its module (matched by source) on that
// target, and the version lola-go resolves its source to holds every component the record
// lists. Everything else is unmappable and left as it is. Each install's source is fetched
// into the cache once, here, and the install pinned to that version, so a preview and the
// run that follows it install the same thing.
func NewPlan(ctx context.Context, e *engine.Engine, st *State) (*Plan, error) {
	p := &Plan{Home: st.Home, Unmappable: []Unmapped{}, NotMigrated: []Item{}, Warnings: st.Warnings, st: st}
	if dir := filepath.Join(st.Home, "market"); isDir(dir) {
		p.NotMigrated = append(p.NotMigrated, Item{What: ItemMarket, Path: dir,
			Hints: []string{"lola-go has no marketplaces; install those modules from their sources with lola install"}})
	}
	byKey := map[string]*Destination{}
	for _, rec := range st.Records {
		if rec.Problem != "" {
			p.Unmappable = append(p.Unmappable, unmapped(rec, rec.Problem, "lola migrate leaves this record and its files as they are"))
			continue
		}
		sc := engine.Scope{Kind: target.User}
		if rec.Scope == ScopeProject {
			sc = engine.Scope{Kind: target.Project, ProjectDir: rec.ProjectPath}
		}
		d := byKey[sc.Key()]
		if d == nil {
			d = newDestination(e, sc)
			man, err := e.Manifest(sc)
			if err != nil {
				return nil, err
			}
			d.man, byKey[sc.Key()] = man, d
			p.Destinations = append(p.Destinations, d)
		}
		d.pending = append(d.pending, rec)
	}
	sort.SliceStable(p.Destinations, func(i, j int) bool {
		a, b := p.Destinations[i], p.Destinations[j]
		if (a.Scope.Kind == target.User) != (b.Scope.Kind == target.User) {
			return b.Scope.Kind == target.User
		}
		return a.Scope.Key() < b.Scope.Key()
	})
	for _, d := range p.Destinations {
		if err := d.plan(ctx, e, st); err != nil {
			return nil, fmt.Errorf("%s: %w", where(d), err)
		}
		d.finish(e)
	}
	return p, nil
}

// planned is a record whose source lola-go can fetch, for the target id, with its install.
// already is set when the destination's manifest has its module on id.
type planned struct {
	rec     Record
	in      Install
	id      string
	already bool
}

// resolved is the cached version an install's source resolved to, and its components as
// kind/name. err is set when the source could not be resolved.
type resolved struct {
	ref        engine.VersionRef
	components map[string]bool
	err        error
}

// plan decides each of d's records (§4): unmappable, already migrated, or installed. The
// sources to install are resolved to cached versions, fetching them; a record listing a
// component its version lacks is unmappable (lola-go found another module root than the
// Python lola did), and every install is pinned to its version. A module's records at d
// migrate all or none: they share the Python lola's copy of the module, which an install
// would replace under a record left behind.
func (d *Destination) plan(ctx context.Context, e *engine.Engine, st *State) error {
	var list []planned
	for _, rec := range d.pending {
		if pl, ok := d.mapRecord(e, st, rec); ok {
			list = append(list, pl)
		}
	}
	d.pending = nil
	versions, err := d.resolve(ctx, e, list)
	if err != nil {
		return err
	}
	d.versions = versions
	var verified []planned
	for _, pl := range list {
		if !pl.already {
			v := versions[installKey(pl.in)]
			if errors.Is(v.err, engine.ErrNotTrusted) {
				d.Unmappable = append(d.Unmappable, unmapped(pl.rec, untrusted.Text(v.err.Error(), 2*untrusted.MaxName), errs.Hints(v.err)...))
				continue
			}
			if v.err != nil {
				src := untrusted.Text(pl.in.Source, untrusted.MaxName)
				d.Unmappable = append(d.Unmappable, unmapped(pl.rec,
					"lola-go cannot fetch "+src+": "+untrusted.Text(untrusted.CleanErr(v.err).Error(), 2*untrusted.MaxName),
					append(errs.Hints(v.err), "rerun lola migrate once the source is reachable")...))
				continue
			}
			if reason, hints := lacking(pl, v); reason != "" {
				d.Unmappable = append(d.Unmappable, unmapped(pl.rec, reason, hints...))
				continue
			}
			pl.in.Version = &v.ref
		}
		verified = append(verified, pl)
	}
	held := map[string]Unmapped{}
	for _, u := range d.Unmappable {
		if _, seen := held[u.Module]; !seen {
			held[u.Module] = u
		}
	}
	for _, pl := range verified {
		if u, ok := held[pl.rec.Module]; ok {
			d.Unmappable = append(d.Unmappable, unmapped(pl.rec,
				"kept with "+u.Module+" for "+u.Assistant+", which lola-go cannot migrate: they share the Python lola's copy of "+u.Module,
				"lola migrate migrates a module's records in one place together; once that record migrates or you uninstall it with the Python lola, rerun lola migrate"))
			continue
		}
		d.accept(e, st, pl)
	}
	return nil
}

// resolve resolves the sources of list's installs, once each, to cached versions through
// InstallChoices, which fetches them into the cache. Each install is resolved on its own, so a
// source that cannot be fetched makes only its records unmappable (resolved.err); a cancelled
// context aborts. The requests add targets, so a module the destination already has resolves
// to the version installed there, as the install will.
func (d *Destination) resolve(ctx context.Context, e *engine.Engine, list []planned) (map[string]resolved, error) {
	out := map[string]resolved{}
	for _, pl := range list {
		k := installKey(pl.in)
		if _, done := out[k]; done || pl.already {
			continue
		}
		ch, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: d.Root, Scopes: []engine.ScopeChoice{{Scope: d.Scope, Source: "migrate"}},
			Modules: []engine.InstallRequest{{Arg: pl.in.Source, Ref: pl.in.Ref, Path: pl.in.Path, Targets: pl.in.Targets}}})
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			out[k] = resolved{err: err}
			continue
		}
		ref := ch.Versions[0]
		v := resolved{ref: ref, components: map[string]bool{}}
		if mc := ch.Module(ref.Name); mc != nil && mc.ID == ref.ID {
			for _, c := range mc.Components {
				v.components[c.String()] = true
			}
		}
		out[k] = v
	}
	return out, nil
}

// installKey identifies an install by what lola-go fetches: kind, source, ref and path.
func installKey(in Install) string {
	return strings.Join([]string{string(in.Kind), in.Source, in.Ref, in.Path}, "\x00")
}

// lacking names the first component pl's record lists that v lacks, with a hint for each
// cause, or returns "". A skill the Python lola renamed to <module>_<skill>, when another module had its name,
// counts as <skill>; other renames are names the user chose and cannot be traced back.
func lacking(pl planned, v resolved) (string, []string) {
	for _, kind := range []struct {
		name  string
		names []string
	}{{KindSkill, pl.rec.Skills}, {KindCommand, pl.rec.Commands}, {KindAgent, pl.rec.Agents}, {KindMCP, pl.rec.MCPs}} {
		for _, n := range kind.names {
			if v.components[kind.name+"/"+n] {
				continue
			}
			if orig, ok := strings.CutPrefix(n, pl.rec.Module+"_"); ok && kind.name == KindSkill && v.components[kind.name+"/"+orig] {
				continue
			}
			src := untrusted.Text(pl.in.Source, untrusted.MaxName)
			at := src
			install := []string{"lola", "install", src}
			if pl.in.Ref != "" {
				install = append(install, "--ref", untrusted.Text(pl.in.Ref, untrusted.MaxName))
			}
			elsewhere := append(slices.Clone(install), "--path")
			if pl.in.Path != "" {
				p := untrusted.Text(pl.in.Path, untrusted.MaxName)
				at += " --path " + p
				install = append(install, "--path", p)
			}
			return "lola-go's module root for " + at + " holds no " + kind.name + " " + n + ", which the Python lola installed", []string{
				"if the module is in another directory of the source, install it with " + errs.ShellJoin(elsewhere) +
					" <dir>, then rerun lola migrate",
				"if the source no longer ships it, keep the Python lola's files, or install the module with " + errs.ShellJoin(install) +
					" and delete them by hand; lola migrate leaves this record and its files as they are",
			}
		}
	}
	return "", nil
}

func newDestination(e *engine.Engine, sc engine.Scope) *Destination {
	d := &Destination{Scope: sc, ID: "user", Root: e.Paths.Home, Targets: []string{}, Installs: []Install{}, Already: []Install{},
		Recorded: []Recorded{}, Unmappable: []Unmapped{}, NotMigrated: []Item{}}
	if sc.Kind == target.Project {
		sum := sha256.Sum256([]byte(sc.Key()))
		d.ID, d.Root = hex.EncodeToString(sum[:8]), sc.ProjectDir
	}
	return d
}

func unmapped(rec Record, reason string, hints ...string) Unmapped {
	return Unmapped{Module: rec.Module, Assistant: rec.Assistant, Scope: rec.Scope, ProjectPath: untrusted.Text(rec.ProjectPath, untrusted.MaxName),
		Reason: reason, Hints: append([]string{}, hints...)}
}

// mapRecord maps rec to a target and an install, or lists it as unmappable at d.
func (d *Destination) mapRecord(e *engine.Engine, st *State, rec Record) (planned, bool) {
	id, ok := assistants[rec.Assistant]
	if ok && !slices.ContainsFunc(e.Targets, func(t target.Target) bool { return t.ID() == id }) {
		ok = false
	}
	if !ok {
		d.Unmappable = append(d.Unmappable, unmapped(rec, "lola-go has no target for "+untrusted.Text(rec.Assistant, untrusted.MaxName),
			"its files stay exactly as the Python lola wrote them; remove them by hand when you no longer need them"))
		return planned{}, false
	}
	in, reason, hint := mapSource(rec, st)
	if reason != "" {
		d.Unmappable = append(d.Unmappable, unmapped(rec, reason, hint))
		return planned{}, false
	}
	in.Targets = []string{id}
	pl := planned{rec: rec, in: in, id: id}
	if ent := migrated(d.man, d.Scope, in); ent != nil {
		pl.in.Module = ent.Name
		pl.already = slices.Contains(effectiveTargets(d.man, ent), id)
	}
	return pl, true
}

// accept adds a planned record to d: what it recorded, what is not migrated, its target, and
// its install.
func (d *Destination) accept(e *engine.Engine, st *State, pl planned) {
	d.accepted = append(d.accepted, pl)
	d.Recorded = append(d.Recorded, recorded(e, d.Scope, pl.rec, pl.id)...)
	d.NotMigrated = append(d.NotMigrated, notMigrated(e, d.Scope, st.Home, pl.rec, pl.id)...)
	if !slices.Contains(d.Targets, pl.id) {
		d.Targets = append(d.Targets, pl.id)
	}
	if pl.already {
		d.Already = merge(d.Already, pl.in)
		return
	}
	d.Installs = merge(d.Installs, pl.in)
}

// mapSource maps rec's module source to a lola install source (§4): the source as lola install
// takes it, the record's ref else source.yml's, and content_dirname as --path. content_dirname
// "/" is the source's root: --path . for a folder or git source, lola-go's own rule for an
// archive (lola-go already looks for the module inside it); a subdirectory is --path for a
// folder or git source, and cannot be mapped for an archive, where the Python lola looked it
// up below the module directory it found. reason is set when lola-go cannot fetch it.
func mapSource(rec Record, st *State) (Install, string, string) {
	in := Install{PythonModules: []string{rec.Module}}
	src, ok := st.Sources[rec.Module]
	if !ok {
		return in, "the Python lola recorded no source for " + rec.Module + " (no modules/" + rec.Module + "/.lola/source.yml)",
			"install the module with lola install <source>; lola migrate leaves this record and its files as they are"
	}
	loc := untrusted.Text(src.Location, untrusted.MaxName)
	want, known := kinds[src.Type]
	if !known {
		return in, fmt.Sprintf("source type %q is not one lola-go can fetch", untrusted.Text(src.Type, untrusted.MaxName)),
			"lola-go fetches " + source.AcceptedForms
	}
	local := want != source.KindGit && !strings.Contains(src.Location, "://")
	if local && !filepath.IsAbs(src.Location) {
		return in, "the local source " + loc + " is not an absolute path", "install the module with lola install <source>"
	}
	spec, isSource, err := source.Parse(src.Location, string(filepath.Separator))
	switch {
	case err != nil:
		return in, "lola-go cannot fetch " + loc + ": " + untrusted.Text(untrusted.CleanErr(err).Error(), 2*untrusted.MaxName),
			"lola-go fetches " + source.AcceptedForms
	case !isSource && local:
		return in, "the local source " + loc + " no longer exists", "restore it there, or install the module from another source with lola install"
	case !isSource:
		return in, "lola-go cannot fetch " + loc, "lola-go fetches " + source.AcceptedForms
	case spec.Kind != want:
		return in, fmt.Sprintf("lola-go reads %s as a %s source, not %s", loc, spec.Kind, untrusted.Text(src.Type, untrusted.MaxName)),
			"install the module with lola install <source>"
	}
	in.Kind, in.Source, in.Ref = spec.Kind, spec.Location, rec.Ref
	if in.Ref == "" {
		in.Ref = src.Ref
	}
	switch dir := src.ContentDirname; {
	case dir == "/" && spec.Kind != source.KindArchive:
		in.Path = "."
	case dir == "/" || dir == "":
	case spec.Kind == source.KindArchive:
		return in, fmt.Sprintf("the Python lola looked up content_dirname %q inside the module it found in the archive %s, which lola-go cannot express",
				untrusted.Text(dir, untrusted.MaxName), loc),
			"install it with " + errs.ShellJoin([]string{"lola", "install", loc, "--path"}) + " <dir>, naming the directory in the archive that holds the module, and rerun lola migrate"
	default:
		in.Path = dir
	}
	return in, "", ""
}

// migrated returns the manifest entry with in's source and path, or nil. A relative source in a
// project manifest is relative to the project's canonical root, as install records it, because
// in.Source is canonical too.
func migrated(man *manifest.Manifest, sc engine.Scope, in Install) *manifest.Entry {
	root, err := filepath.EvalSymlinks(sc.ProjectDir)
	if err != nil {
		root = sc.ProjectDir
	}
	for i, ent := range man.Modules {
		same := source.SameLocation(in.Kind, ent.Source, in.Source)
		if !same && sc.Kind == target.Project && in.Kind != source.KindGit && !filepath.IsAbs(ent.Source) {
			same = filepath.Join(root, ent.Source) == in.Source
		}
		if same && ent.Path == in.Path {
			return &man.Modules[i]
		}
	}
	return nil
}

// effectiveTargets are the targets a manifest entry installs on.
func effectiveTargets(man *manifest.Manifest, ent *manifest.Entry) []string {
	if len(ent.Targets) > 0 {
		return ent.Targets
	}
	return man.Targets
}

// merge adds in to list, joining the install with the same source, ref and path.
func merge(list []Install, in Install) []Install {
	for i := range list {
		c := &list[i]
		if c.Kind == in.Kind && c.Source == in.Source && c.Ref == in.Ref && c.Path == in.Path {
			for _, m := range in.PythonModules {
				if !slices.Contains(c.PythonModules, m) {
					c.PythonModules = append(c.PythonModules, m)
				}
			}
			for _, t := range in.Targets {
				if !slices.Contains(c.Targets, t) {
					c.Targets = append(c.Targets, t)
				}
			}
			if c.Module == "" {
				c.Module = in.Module
			}
			return list
		}
	}
	return append(list, in)
}

// finish orders d's targets and every install's targets as the registry does, and sorts.
func (d *Destination) finish(e *engine.Engine) {
	order := func(ids []string) {
		rank := func(id string) int {
			return slices.IndexFunc(e.Targets, func(t target.Target) bool { return t.ID() == id })
		}
		slices.SortFunc(ids, func(a, b string) int { return rank(a) - rank(b) })
	}
	order(d.Targets)
	d.finishItems()
	slices.SortStableFunc(d.Recorded, func(a, b Recorded) int {
		return strings.Compare(a.Target+"\x00"+a.Rel+"\x00"+a.Key, b.Target+"\x00"+b.Rel+"\x00"+b.Key)
	})
	d.Recorded = slices.CompactFunc(d.Recorded, func(a, b Recorded) bool { return a.Target == b.Target && a.Rel == b.Rel && a.Key == b.Key })
	for _, list := range [][]Install{d.Installs, d.Already} {
		for i := range list {
			order(list[i].Targets)
			sort.Strings(list[i].PythonModules)
		}
	}
}

// root is a target's scope root at sc, as lola-go and the Python lola both use it.
func root(e *engine.Engine, sc engine.Scope, id string) string {
	for _, t := range e.Targets {
		if t.ID() == id {
			return t.Root(sc.Kind, sc.ProjectDir, e.Paths)
		}
	}
	return ""
}

// recorded lists the paths the Python lola wrote for rec on target id (§6, checked against
// src/lola/targets/*.py at 91a2885): skill directories, command files with their sidecar
// directories (claude-code and opencode copy one), agent files, the legacy OpenCode
// directories its uninstall still cleans, MCP entries, and at project scope the module copy.
func recorded(e *engine.Engine, sc engine.Scope, rec Record, id string) []Recorded {
	r := root(e, sc, id)
	var out []Recorded
	add := func(kind, name, rel string, dir bool) {
		out = append(out, Recorded{Module: rec.Module, Target: id, Kind: kind, Name: name, Root: r, Rel: rel, Dir: dir})
	}
	mcp := func(file, pointer string) {
		for _, n := range rec.MCPs {
			out = append(out, Recorded{Module: rec.Module, Target: id, Kind: KindMCP, Name: n, Root: r, Rel: file, Key: pointer + escapePointer(n)})
		}
	}
	switch id {
	case "claude-code":
		for _, n := range rec.Skills {
			add(KindSkill, n, ".claude/skills/"+n, true)
		}
		for _, n := range rec.Commands {
			add(KindCommand, n, ".claude/commands/"+n+".md", false)
			add(KindCommand, n, ".claude/commands/"+n, true)
		}
		for _, n := range rec.Agents {
			add(KindAgent, n, ".claude/agents/"+n+".md", false)
		}
		mcp(".mcp.json", "/mcpServers/")
	case "gemini-cli":
		for _, n := range rec.Commands {
			add(KindCommand, n, ".gemini/commands/"+n+".toml", false)
		}
		mcp(".gemini/settings.json", "/mcpServers/")
	case "opencode":
		pre := ".opencode/"
		if sc.Kind == target.User {
			pre = ""
		}
		for _, n := range rec.Skills {
			add(KindSkill, n, pre+"skills/"+n, true)
		}
		for _, n := range rec.Commands {
			add(KindCommand, n, pre+"commands/"+n+".md", false)
			add(KindCommand, n, pre+"commands/"+n, true)
			add(KindCommand, n, pre+"command/"+n+".md", false)
			add(KindCommand, n, pre+"command/"+rec.Module+"."+n+".md", false)
		}
		for _, n := range rec.Agents {
			add(KindAgent, n, pre+"agents/"+n+".md", false)
			add(KindAgent, n, pre+"agent/"+n+".md", false)
			add(KindAgent, n, pre+"agent/"+rec.Module+"."+n+".md", false)
		}
		mcp("opencode.json", "/mcp/")
	}
	if sc.Kind == target.Project {
		out = append(out, Recorded{Module: rec.Module, Kind: KindModuleCopy, Name: rec.Module, Root: sc.ProjectDir,
			Rel: ".lola/modules/" + rec.Module, Dir: true})
	}
	return out
}

// escapePointer escapes a name as one RFC 6901 reference token.
func escapePointer(s string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(s)
}

// notMigrated lists what rec left that lola migrate never touches (§5, §12.4): the
// instructions marker block, Gemini's skill section, and the module's plugin data.
func notMigrated(e *engine.Engine, sc engine.Scope, home string, rec Record, id string) []Item {
	r := root(e, sc, id)
	user := sc.Kind == target.User
	var out []Item
	block := "<!-- lola:module:" + rec.Module + ":start -->"
	instructions := map[string]string{"claude-code": "CLAUDE.md", "gemini-cli": "GEMINI.md", "opencode": "AGENTS.md"}[id]
	if user && id == "claude-code" {
		instructions = ".claude/CLAUDE.md"
	}
	if rec.HasInstructions || len(rec.AppendContext) > 0 {
		out = append(out, Item{What: ItemInstructions, Module: rec.Module, Path: filepath.Join(r, filepath.FromSlash(instructions)), Marker: block,
			Hints: []string{"it keeps working; delete the block from " + block + " to <!-- lola:module:" + rec.Module + ":end --> when you no longer want it"}})
	}
	if id == "gemini-cli" && len(rec.Skills) > 0 {
		out = append(out, Item{What: ItemGeminiSkills, Module: rec.Module, Path: filepath.Join(r, "GEMINI.md"),
			Marker: "<!-- lola:skills:start --> ### " + rec.Module,
			Hints:  []string{"delete the ### " + rec.Module + " section between <!-- lola:skills:start --> and <!-- lola:skills:end --> when you no longer want it"}})
	}
	data := filepath.Join(sc.ProjectDir, ".lola", "plugin-data", rec.Module)
	if user {
		data = filepath.Join(home, "plugin-data", rec.Module)
	}
	if isDir(data) {
		out = append(out, Item{What: ItemPluginData, Module: rec.Module, Path: data,
			Hints: []string{"MCP servers keep their state here; remove it by hand when none of them uses it"}})
	}
	return out
}

// finishItems adds the destination-wide items: a .lola-req, which lola-go does not read, and
// sorts and deduplicates them.
func (d *Destination) finishItems() {
	if d.Scope.Kind == target.Project {
		if req := filepath.Join(d.Root, ".lola-req"); isFile(req) {
			d.NotMigrated = append(d.NotMigrated, Item{What: ItemLolaReq, Path: req,
				Hints: []string{"lola-go records modules in .lola/project.yml; delete .lola-req when you no longer use the Python lola"}})
		}
	}
	slices.SortFunc(d.NotMigrated, func(a, b Item) int {
		return strings.Compare(a.Path+"\x00"+a.What+"\x00"+a.Module, b.Path+"\x00"+b.What+"\x00"+b.Module)
	})
	d.NotMigrated = slices.CompactFunc(d.NotMigrated, func(a, b Item) bool {
		return a.What == b.What && a.Module == b.Module && a.Path == b.Path && a.Marker == b.Marker && slices.Equal(a.Hints, b.Hints)
	})
}

func isDir(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.IsDir()
}

func isFile(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode().IsRegular()
}
